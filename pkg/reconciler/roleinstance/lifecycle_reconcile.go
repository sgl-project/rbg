/*
Copyright 2026 The RBG Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package roleinstance

import (
	"context"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/features"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/lifecycle"
)

const lifecycleHookRequeue = 30 * time.Second

type lifecycleOperationResult struct {
	blocked   bool
	operation bool
}

func (r *reconciler) lifecycleEnabled(instance *workloadsv1alpha2.RoleInstance) bool {
	return features.RoleInstanceLifecycleHooksEnabled() &&
		instance != nil && workloadsv1alpha2.HasRoleInstanceLifecycleHooks(instance.Spec.Lifecycle)
}

func (r *reconciler) reconcileLifecycleDeletion(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
) (bool, error) {
	if !r.lifecycleEnabled(instance) || instance.Spec.Lifecycle.PreDelete == nil {
		// The preDelete hook will not run here: it is unconfigured, no hooks
		// remain, or the feature gate is disabled. The reserved finalizer must
		// not outlive the hook that installed it, or deletion hangs forever.
		if removed, err := r.hookControl.RemovePreDeleteFinalizer(ctx, instance); err != nil {
			return false, err
		} else if removed {
			return false, nil
		}
		return false, nil
	}

	if !containsFinalizer(instance, constants.RoleInstancePreDeleteFinalizer) {
		// Kubernetes forbids adding a finalizer after deletion has begun. This is
		// the documented Alpha enablement window; the deletion continues without
		// exposing the hook.
		r.recorder.Eventf(instance, v1.EventTypeWarning, "PreDeleteFinalizerMissing",
			"RoleInstance has preDelete configuration but deletion began before the reserved finalizer was added")
		return false, nil
	}

	state := lifecycle.HookState(instance)
	// Terminating is durable authorization: preDelete has already been
	// acknowledged, so a retry after a partial cleanup failure must resume
	// deletion instead of replaying the hook and asking for a second
	// acknowledgement.
	if state != constants.RoleInstanceHookStatePreDelete &&
		state != constants.RoleInstanceHookStateTerminating {
		pods, _, err := r.getOwnedPods(ctx, instance)
		if err != nil {
			return true, err
		}
		if err := r.hookControl.EnterHook(
			ctx, instance, constants.RoleInstanceHookStatePreDelete, instance.Spec.Lifecycle.PreDelete, pods); err != nil {
			return true, err
		}
		r.recorder.Eventf(instance, v1.EventTypeNormal, "LifecycleHookEntered",
			"preDelete lifecycle hook entered; waiting for signal labels to be removed")
		r.setTerminatingPhase(ctx, instance)
		return true, nil
	}

	if state == constants.RoleInstanceHookStatePreDelete &&
		!lifecycle.HookReleased(instance, instance.Spec.Lifecycle.PreDelete) {
		pods, _, err := r.getOwnedPods(ctx, instance)
		if err != nil {
			return true, err
		}
		if err := r.hookControl.SyncRuntimeLabels(ctx, instance, instance.Spec.Lifecycle, pods); err != nil {
			return true, err
		}
		r.setTerminatingPhase(ctx, instance)
		return true, nil
	}

	pods, inactivePods, err := r.getOwnedPods(ctx, instance)
	if err != nil {
		return true, err
	}
	if err := r.hookControl.SetHookState(ctx, instance, constants.RoleInstanceHookStateTerminating, pods); err != nil {
		return true, err
	}
	r.setTerminatingPhase(ctx, instance)
	for _, pod := range append(pods, inactivePods...) {
		if pod == nil {
			continue
		}
		if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			return true, err
		}
	}
	if _, err := r.hookControl.RemovePreDeleteFinalizer(ctx, instance); err != nil {
		return true, err
	}
	return false, nil
}

func (r *reconciler) setTerminatingPhase(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) {
	base := instance.DeepCopy()
	instance.Status.Phase = workloadsv1alpha2.RoleInstancePhaseTerminating
	if err := r.Status().Patch(ctx, instance, client.MergeFrom(base)); err != nil {
		// A failed status write must not bypass the deletion barrier. The hook
		// label remains authoritative, and the next reconcile retries the write.
		*instance = *base
	}
}

func (r *reconciler) prepareLifecycleOperation(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
	newStatus *workloadsv1alpha2.RoleInstanceStatus,
	facts roleInstancePhaseFacts,
	pods []*v1.Pod,
) (lifecycleOperationResult, error) {
	if !r.lifecycleEnabled(instance) {
		if err := r.cleanupStaleLifecycle(ctx, instance, pods); err != nil {
			return lifecycleOperationResult{}, err
		}
		return lifecycleOperationResult{}, nil
	}
	lifecycleConfig := instance.Spec.Lifecycle
	if err := r.hookControl.SyncRuntimeLabels(ctx, instance, lifecycleConfig, pods); err != nil {
		return lifecycleOperationResult{}, err
	}
	if err := r.hookControl.CleanupPostReady(ctx, instance, lifecycleConfig, pods); err != nil {
		return lifecycleOperationResult{}, err
	}
	if lifecycleConfig.PreDelete == nil {
		if _, err := r.hookControl.RemovePreDeleteFinalizer(ctx, instance); err != nil {
			return lifecycleOperationResult{}, err
		}
	} else {
		if _, err := r.hookControl.EnsurePreDeleteFinalizer(ctx, instance); err != nil {
			return lifecycleOperationResult{}, err
		}
	}

	update := facts.updateActive && instance.Status.Phase != workloadsv1alpha2.RoleInstancePhaseCreating
	// A genuinely new instance starts at Creating, not Recovering: entering
	// preRecover before the first Pod exists would emit a spurious recovery
	// signal that the next reconcile bypasses once the phase latches.
	recovery := !update && !facts.podsPresent && !facts.newInstance &&
		instance.Status.Phase != workloadsv1alpha2.RoleInstancePhaseCreating
	if !update && !recovery {
		return lifecycleOperationResult{}, nil
	}

	if update {
		newStatus.Phase = workloadsv1alpha2.RoleInstancePhaseUpdating
		// preUpdate is gated by the owning RoleInstanceSet controller before it
		// mutates workload Pods. The RoleInstance controller must not re-enter
		// the hook after that controller has already applied the update.
		return lifecycleOperationResult{operation: true}, nil
	}

	newStatus.Phase = workloadsv1alpha2.RoleInstancePhaseRecovering
	if lifecycleConfig.PreRecover == nil {
		if lifecycle.HookState(instance) == constants.RoleInstanceHookStatePreRecover {
			if err := r.hookControl.SetHookState(ctx, instance, constants.RoleInstanceHookStateRecovering, pods); err != nil {
				return lifecycleOperationResult{}, err
			}
		}
		return lifecycleOperationResult{operation: true}, nil
	}
	hookState := lifecycle.HookState(instance)
	// Recovering is durable authorization: preRecover has already been
	// acknowledged, so a retry after partial Pod creation resumes recovery
	// instead of replaying the hook and asking for a second acknowledgement.
	if hookState != constants.RoleInstanceHookStatePreRecover &&
		hookState != constants.RoleInstanceHookStateRecovering {
		if err := r.hookControl.EnterHook(
			ctx, instance, constants.RoleInstanceHookStatePreRecover, lifecycleConfig.PreRecover, pods); err != nil {
			return lifecycleOperationResult{}, err
		}
		r.recorder.Eventf(instance, v1.EventTypeNormal, "LifecycleHookEntered",
			"preRecover lifecycle hook entered; waiting for signal labels to be removed")
		return lifecycleOperationResult{blocked: true, operation: true}, nil
	}
	if hookState == constants.RoleInstanceHookStatePreRecover &&
		!lifecycle.HookReleased(instance, lifecycleConfig.PreRecover) {
		if err := r.hookControl.SyncRuntimeLabels(ctx, instance, lifecycleConfig, pods); err != nil {
			return lifecycleOperationResult{}, err
		}
		return lifecycleOperationResult{blocked: true, operation: true}, nil
	}
	if hookState != constants.RoleInstanceHookStateRecovering {
		if err := r.hookControl.SetHookState(ctx, instance, constants.RoleInstanceHookStateRecovering, pods); err != nil {
			return lifecycleOperationResult{}, err
		}
	}
	return lifecycleOperationResult{operation: true}, nil
}

// cleanupStaleLifecycle removes protocol state that the current
// configuration no longer owns. With the gate enabled but no hooks
// configured, every runtime signal label and the hook-state label are stale.
// The preDelete finalizer is removed even when the gate is disabled: it must
// never block deletion of an object whose hook the controller will not run.
// Every operation here is a no-op when no protocol state is present, so
// instances that never used the feature stay untouched.
func (r *reconciler) cleanupStaleLifecycle(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
	pods []*v1.Pod,
) error {
	if instance == nil {
		return nil
	}
	if features.RoleInstanceLifecycleHooksEnabled() {
		if lifecycle.HookState(instance) != "" {
			if err := r.hookControl.SetHookState(ctx, instance, "", pods); err != nil {
				return err
			}
		}
		if err := r.hookControl.SyncRuntimeLabels(ctx, instance, instance.Spec.Lifecycle, pods); err != nil {
			return err
		}
	}
	_, err := r.hookControl.RemovePreDeleteFinalizer(ctx, instance)
	return err
}

func (r *reconciler) finishLifecycleOperation(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
	newStatus *workloadsv1alpha2.RoleInstanceStatus,
	facts roleInstancePhaseFacts,
	operation bool,
	pods []*v1.Pod,
) error {
	if !r.lifecycleEnabled(instance) || !phaseCompletionSatisfied(facts, newStatus) {
		return nil
	}
	if instance.Spec.Lifecycle.PostReady != nil {
		controlledOperation := operation || facts.newInstance ||
			instance.Status.Phase == workloadsv1alpha2.RoleInstancePhaseCreating ||
			instance.Status.Phase == workloadsv1alpha2.RoleInstancePhaseUpdating ||
			instance.Status.Phase == workloadsv1alpha2.RoleInstancePhaseRecovering
		if !controlledOperation {
			return nil
		}
		return r.hookControl.EnterHook(
			ctx, instance, constants.RoleInstanceHookStatePostReady, instance.Spec.Lifecycle.PostReady, pods)
	}

	state := lifecycle.HookState(instance)
	if state == constants.RoleInstanceHookStateUpdating ||
		state == constants.RoleInstanceHookStateRecovering ||
		state == constants.RoleInstanceHookStatePostReady {
		return r.hookControl.SetHookState(ctx, instance, "", pods)
	}
	return nil
}

func containsFinalizer(instance *workloadsv1alpha2.RoleInstance, finalizer string) bool {
	return instance != nil && controllerutil.ContainsFinalizer(instance, finalizer)
}

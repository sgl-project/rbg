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

package statefulmode

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancelifecycle "sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/readiness"
	roleinstancelifecycle "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/lifecycle"
)

const preDeleteHoldLabel = "consumer.example/held"

func holdPreDelete(t *testing.T, h *rollbackTestHarness, markNotReady bool, instances ...*workloadsv1alpha2.RoleInstance) client.WithWatch {
	t.Helper()
	h.set.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceSetLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceSetLifecycleHook{
			LabelsHandler: map[string]string{preDeleteHoldLabel: "true"},
			MarkNotReady:  markNotReady,
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	builder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&workloadsv1alpha2.RoleInstance{})
	for _, instance := range instances {
		instance.Labels[preDeleteHoldLabel] = "true"
		if markNotReady {
			instance.Spec.ReadinessGates = []workloadsv1alpha2.RoleInstanceReadinessGate{{ConditionType: workloadsv1alpha2.RoleInstanceCustomReady}}
			instance.Status.Conditions = append(instance.Status.Conditions,
				workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceCustomReady, Status: corev1.ConditionTrue})
		}
		builder.WithObjects(instance)
	}
	c := builder.Build()
	h.control.lifecycleControl = instancelifecycle.New(c)
	return c
}

func readLifecycle(t *testing.T, c client.Client, instance *workloadsv1alpha2.RoleInstance) constants.RoleInstanceLifecycleStateType {
	t.Helper()
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), instance))
	return instancelifecycle.GetRoleInstanceLifecycleState(instance)
}

func TestStatefulRollingUpdateAndScaleDownWaitForPreDeleteHook(t *testing.T) {
	for _, scaleDown := range []bool{false, true} {
		name := "rolling-update"
		if scaleDown {
			name = "scale-down"
		}
		t.Run(name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "hook-"+name, 1, 0, 0)
			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
			if scaleDown {
				h.set.Spec.Replicas = ptr.To(int32(0))
			}
			instance := h.instance(0, h.intermediate.Name, true)
			instance.ResourceVersion = "1"
			c := holdPreDelete(t, h, false, instance)
			for pass := 0; pass < 2; pass++ {
				h.reconcile(t, instance)
				h.assertDeleted(t)
				require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, instance))
			}
			delete(instance.Labels, preDeleteHoldLabel)
			require.NoError(t, c.Update(context.Background(), instance))
			h.reconcile(t, instance)
			h.assertDeleted(t, instance.Name)
		})
	}
}

func TestStatefulPreUpdateBlocksWorkloadUpdateAndClearsPodMirror(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-pre-update", 1, 0, 0)
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"patio.workloads.x-k8s.io/pre-update": "blocked"},
		},
	}
	instance := h.instance(0, h.intermediate.Name, true)
	instance.ResourceVersion = "1"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				constants.RoleInstanceNameLabelKey: instance.Name,
			},
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance, pod).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	h.reconcile(t, instance)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceHookStatePreUpdate, roleinstancelifecycle.HookState(instance))
	require.Equal(t, "blocked", instance.Labels["patio.workloads.x-k8s.io/pre-update"])
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pod), pod))
	require.Equal(t, "blocked", pod.Labels["patio.workloads.x-k8s.io/pre-update"])

	delete(instance.Labels, "patio.workloads.x-k8s.io/pre-update")
	require.NoError(t, c.Update(context.Background(), instance))
	h.reconcile(t, instance)
	h.assertDeleted(t, instance.Name)
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pod), pod))
	require.Empty(t, pod.Labels["patio.workloads.x-k8s.io/pre-update"])
}

// KEP-527 preDelete is not a set-level barrier: the set controller deletes
// the RoleInstance, and the drain runs inside the finalizer-held deletion.
// The set controller must not write hook state or signal labels itself.
func TestStatefulPreDeleteRunsDuringRoleInstanceDeletion(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-delete-then-hook", 1, 0, 0)
	h.set.Spec.Replicas = ptr.To(int32(0))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/drain": "true"},
		},
	}
	instance := h.instance(0, h.current.Name, true)
	instance.ResourceVersion = "1"
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	h.reconcile(t, instance)
	h.assertDeleted(t, instance.Name)
	require.Empty(t, roleinstancelifecycle.HookState(instance),
		"the set controller must not arm preDelete itself")
	require.NotContains(t, instance.Labels, "consumer.example/drain")
}

// While a finalizer-held deletion is still draining its preDelete hook, the
// next condemned ordinal must wait in OrderedReady mode, and the waiting
// instance must not have a set-level hook armed on it.
func TestStatefulOrderedScaleDownSerializesPreDeleteDrains(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-ordered-drain", 2, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	h.set.Spec.Replicas = ptr.To(int32(0))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/drain": "true"},
		},
	}
	low := h.instance(0, h.current.Name, true)
	high := h.instance(1, h.current.Name, true)
	high.DeletionTimestamp = ptr.To(metav1.Now())
	high.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(low, high).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	h.reconcile(t, low, high)
	h.assertDeleted(t)
	require.Empty(t, roleinstancelifecycle.HookState(low),
		"a waiting ordinal must not carry a set-level preDelete hook")
	require.NotContains(t, low.Labels, "consumer.example/drain")
}

func TestStatefulPreDeleteWaitsForFinalizer(t *testing.T) {
	for _, test := range []struct {
		name           string
		configureLabel bool
	}{
		{name: "finalizer only"},
		{name: "configured label absent", configureLabel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "hook-finalizer", 0, 0, 0)
			instance := h.instance(0, h.current.Name, true)
			c := holdPreDelete(t, h, false, instance)
			hook := h.set.Spec.Lifecycle.PreDelete
			if !test.configureLabel {
				hook.LabelsHandler = nil
			}
			delete(instance.Labels, preDeleteHoldLabel)
			const holdFinalizer = "consumer.example/drain"
			hook.FinalizersHandler = []string{holdFinalizer}
			instance.Finalizers = []string{holdFinalizer}
			require.NoError(t, c.Update(context.Background(), instance))

			h.reconcile(t, instance)
			h.assertDeleted(t)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, instance))

			// The first pass announces the drain. The next pass must honor the finalizer.
			h.reconcile(t, instance)
			h.assertDeleted(t)

			instance.Finalizers = nil
			require.NoError(t, c.Update(context.Background(), instance))
			h.reconcile(t, instance)
			h.assertDeleted(t, instance.Name)
		})
	}
}

func TestStatefulScaleDownCancellationRestoresPreDeleteReadiness(t *testing.T) {
	h := newRollbackTestHarness(t, "cancel-scale-down", 1, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	retained := h.instance(0, h.current.Name, true)
	held := h.instance(1, h.current.Name, true)
	c := holdPreDelete(t, h, true, held)
	check := func(state constants.RoleInstanceLifecycleStateType, gate corev1.ConditionStatus) {
		t.Helper()
		h.assertDeleted(t)
		require.Equal(t, state, readLifecycle(t, c, held))
		if got := readiness.GetReadinessCondition(held); got == nil || got.Status != gate {
			t.Fatalf("readiness gate = %+v, want %q", got, gate)
		}
	}

	h.reconcile(t, retained, held)
	check(constants.RoleInstanceLifecycleStatePreparingDelete, corev1.ConditionFalse)
	h.set.Spec.Replicas = ptr.To(int32(2))
	h.reconcile(t, retained, held)
	check(constants.RoleInstanceLifecycleStateNormal, corev1.ConditionTrue)

	h.set.Spec.Replicas = ptr.To(int32(1))
	h.reconcile(t, retained, held)
	check(constants.RoleInstanceLifecycleStatePreparingDelete, corev1.ConditionFalse)
	delete(held.Labels, preDeleteHoldLabel)
	require.NoError(t, c.Update(context.Background(), held))
	h.reconcile(t, retained, held)
	h.assertDeleted(t, held.Name)
}

func TestCancelScaleDownRestoresTargetRevisionAfterHookRemoval(t *testing.T) {
	h := newRollbackTestHarness(t, "cancel-scale-down-without-hook", 1, 0, 0)
	h.set.Spec.UpdateStrategy.Paused = true
	stale := h.instance(0, h.intermediate.Name, true)
	retained := h.instance(1, h.current.Name, true)
	c := holdPreDelete(t, h, true, retained)

	h.reconcile(t, stale, retained)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, retained))
	require.False(t, isHealthy(retained))

	// The stale member keeps the rollout pending while the retained member is at its target revision.
	h.set.Spec.Lifecycle = nil
	h.set.Spec.Replicas = ptr.To(int32(2))
	h.reconcile(t, stale, retained)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStateNormal, readLifecycle(t, c, retained))
	gate := readiness.GetReadinessCondition(retained)
	require.NotNil(t, gate)
	require.Equal(t, corev1.ConditionTrue, gate.Status)
}

func TestPreDeleteLifecyclePatchFailureStopsReconcile(t *testing.T) {
	for _, test := range []struct {
		name    string
		restore bool
	}{
		{name: "enter PreparingDelete"},
		{name: "restore Normal", restore: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "pre-delete-patch", 1, 0, 0)
			base := h.instance(0, h.current.Name, true)
			held := h.instance(1, h.current.Name, true)
			c := holdPreDelete(t, h, true, held)
			instances := []*workloadsv1alpha2.RoleInstance{base, held}
			if test.restore {
				h.reconcile(t, instances...)
				h.assertDeleted(t)
				require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, held))

				h.set.Spec.Replicas = ptr.To(int32(2))
				// An excess member whose hook has released can be deleted if reconcile continues.
				excess := h.instance(2, h.current.Name, true)
				instancelifecycle.SetRoleInstanceLifecycle(constants.RoleInstanceLifecycleStatePreparingDelete)(excess)
				instances = append(instances, excess)
			}
			stateBefore := instancelifecycle.GetRoleInstanceLifecycleState(held)
			patchErr := errors.New("lifecycle patch failed")
			h.control.lifecycleControl = instancelifecycle.New(interceptor.NewClient(c, interceptor.Funcs{
				Patch: func(_ context.Context, _ client.WithWatch, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
					require.Equal(t, held.Name, obj.GetName())
					return patchErr
				},
			}))

			_, err := h.control.updateStatefulInstanceSet(context.Background(), h.set, h.current, h.current,
				0, instances, []*appsv1.ControllerRevision{h.current, h.intermediate})
			require.ErrorIs(t, err, patchErr)
			h.assertDeleted(t)
			require.Equal(t, stateBefore, readLifecycle(t, c, held))
		})
	}
}

func TestOrderedScaleDownWaitsForHighestPreDeleteHook(t *testing.T) {
	h := newRollbackTestHarness(t, "ordered-hooks", 1, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	base := h.instance(0, h.current.Name, true)
	lower := h.instance(1, h.current.Name, true)
	higher := h.instance(2, h.current.Name, true)
	c := holdPreDelete(t, h, false, lower, higher)
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, higher))
	require.NotEqual(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, lower))
	delete(higher.Labels, preDeleteHoldLabel)
	require.NoError(t, c.Update(context.Background(), higher))
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t, higher.Name)
}

func TestOrderedRolloutProceedsOnceScaleDownDeletionStarts(t *testing.T) {
	for _, test := range []struct {
		name            string
		alreadyDeleting bool
	}{
		{name: "delete issued"},
		{name: "already terminating", alreadyDeleting: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "ordered-rollout", 1, 0, 0)
			h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
			target := h.instance(0, h.intermediate.Name, true)
			surplus := h.instance(1, h.current.Name, true)
			c := holdPreDelete(t, h, false, target, surplus)

			h.reconcile(t, target, surplus)
			h.assertDeleted(t)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, surplus))
			require.NotEqual(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))

			delete(surplus.Labels, preDeleteHoldLabel)
			require.NoError(t, c.Update(context.Background(), surplus))
			if test.alreadyDeleting {
				withTerminating(surplus)
			}
			h.reconcile(t, target, surplus)
			if test.alreadyDeleting {
				h.assertDeleted(t)
			} else {
				h.assertDeleted(t, surplus.Name)
			}
			// The deletion may still be draining preDelete behind its
			// finalizer, so the target waits one reconcile until the
			// condemned object is gone.
			h.reconcile(t, target)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))
		})
	}
}

func TestPausedRolloutPreservesPreDeleteForSparseUpdateTarget(t *testing.T) {
	h := newRollbackTestHarness(t, "paused-rollout", 1, 2, 0)
	h.set.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1,3"}
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	base := h.instance(0, h.current.Name, true)
	protected := h.instance(2, h.intermediate.Name, true)
	target := h.instance(4, h.intermediate.Name, true)
	c := holdPreDelete(t, h, false, protected, target)
	h.reconcile(t, base, protected, target)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, protected))
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))

	h.set.Spec.Replicas = ptr.To(int32(3))
	h.set.Spec.UpdateStrategy.Paused = true
	h.reconcile(t, base, protected, target)
	h.assertDeleted(t)
	// Ordinal 2 is below partition 2 by member position; ordinal 4 is at the partition.
	require.Equal(t, constants.RoleInstanceLifecycleStateNormal, readLifecycle(t, c, protected))
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))

	h.set.Spec.UpdateStrategy.Paused = false
	delete(target.Labels, preDeleteHoldLabel)
	require.NoError(t, c.Update(context.Background(), target))
	h.reconcile(t, base, protected, target)
	h.assertDeleted(t, target.Name)
}

func TestRemovingPreDeleteHookUnblocksOrderedScaleDown(t *testing.T) {
	h := newRollbackTestHarness(t, "remove-pre-delete", 1, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	base := h.instance(0, h.current.Name, true)
	lower := h.instance(1, h.current.Name, false)
	higher := h.instance(2, h.current.Name, true)
	c := holdPreDelete(t, h, true, higher)
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, higher))
	require.False(t, isHealthy(higher))

	h.set.Spec.Lifecycle = nil
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t, higher.Name)
	// The unhealthy lower ordinal follows once the deleting object is gone.
	h.reconcile(t, base, lower)
	h.assertDeleted(t, lower.Name)
}

func TestRollingUpdateCountsUnavailableDrainOnce(t *testing.T) {
	for _, test := range []struct {
		name   string
		stable bool
	}{
		{name: "recently NotReady"},
		{name: "stably NotReady", stable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "pre-delete-budget", 2, 0, 0)
			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
			lower := h.instance(0, h.intermediate.Name, true)
			higher := h.instance(1, h.intermediate.Name, true)
			c := holdPreDelete(t, h, true, lower, higher)
			h.reconcile(t, lower, higher)
			h.assertDeleted(t)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, higher))
			require.False(t, isHealthy(higher))
			require.NotEqual(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, lower))

			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(2))
			if test.stable {
				setUnhealthySince(h.set, higher, time.Now().Add(-2*stableUnhealthyDuration))
			}
			h.reconcile(t, lower, higher)
			h.assertDeleted(t)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, lower))
		})
	}
}

func TestStaleSurgeDrainPreservesBaseRolloutBudget(t *testing.T) {
	h := newRollbackTestHarness(t, "surge-pre-delete", 2, 0, 2)
	base0 := h.instance(0, h.intermediate.Name, true)
	base1 := h.instance(1, h.intermediate.Name, true)
	readySurge := h.instance(2, h.current.Name, true)
	staleSurge := h.instance(3, h.intermediate.Name, true)
	c := holdPreDelete(t, h, false, base0, base1, staleSurge)

	// With maxUnavailable=0, the ready surge supplies the one available base rollout slot.
	h.reconcile(t, base0, base1, readySurge, staleSurge)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, staleSurge))
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, base1))
	require.NotEqual(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, base0))

	delete(staleSurge.Labels, preDeleteHoldLabel)
	require.NoError(t, c.Update(context.Background(), staleSurge))
	h.reconcile(t, base0, base1, readySurge, staleSurge)
	h.assertDeleted(t, staleSurge.Name)
}

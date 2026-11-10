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

package sync

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

// GroupRestartBudgetExceeded is the reason reported by the RI circuit breaker.
const (
	GroupRestartBudgetExceeded = "GroupRestartBudgetExceeded"
	// GroupRestartBudgetMessage is shared by computed and persisted conditions.
	GroupRestartBudgetMessage = "Automatic group recovery exhausted; annotate this RoleInstance with rbg.workloads.x-k8s.io/recover=true to recover"
	// RestartBudgetRequeueAfter is a safety backstop; RI/Pod watches drive normal progress.
	RestartBudgetRequeueAfter = 30 * time.Second

	restartPolicyTriggeredReason  = "RestartPolicyTriggered"
	restartPolicyTriggeredMessage = "Instance is being recreated due to restart policy"
)

// groupRestartRevision intentionally excludes policy, delays, budget, component
// size and RI metadata. Editing those must not reset a revision's circuit breaker.
func groupRestartRevision(instance *workloadsv1alpha2.RoleInstance) string {
	components := instance.DeepCopy().Spec.Components
	for i := range components {
		components[i].Size = nil
	}
	slices.SortFunc(components, func(a, b workloadsv1alpha2.RoleInstanceComponent) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	data, _ := json.Marshal(components) // API types contain no non-JSON values.
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func copyRestartBudgetStatus(to, from *workloadsv1alpha2.RoleInstance) {
	to.Status.GroupRestartCount = from.Status.GroupRestartCount
	to.Status.GroupRestartRevision = from.Status.GroupRestartRevision
	to.Status.RestartBudgetExhausted = from.Status.RestartBudgetExhausted
}

func setBudgetCondition(instance *workloadsv1alpha2.RoleInstance, typ workloadsv1alpha2.RoleInstanceConditionType, status v1.ConditionStatus) {
	condition := workloadsv1alpha2.RoleInstanceCondition{Type: typ, Status: status,
		Reason: GroupRestartBudgetExceeded, Message: GroupRestartBudgetMessage, LastTransitionTime: metav1.Now()}
	for i := range instance.Status.Conditions {
		if instance.Status.Conditions[i].Type == typ {
			if instance.Status.Conditions[i].Status == status {
				condition.LastTransitionTime = instance.Status.Conditions[i].LastTransitionTime
			}
			instance.Status.Conditions[i] = condition
			return
		}
	}
	instance.Status.Conditions = append(instance.Status.Conditions, condition)
}

// EnforceRestartBudgetConditions is also used by the generic status writer after
// merging the LIVE budget state, so a stale readiness calculation cannot unfreeze
// an exhausted RI. The budget controller is the sole owner of Degraded.
func EnforceRestartBudgetConditions(instance *workloadsv1alpha2.RoleInstance) {
	if !instance.Status.RestartBudgetExhausted {
		return
	}
	setBudgetCondition(instance, workloadsv1alpha2.RoleInstanceDegraded, v1.ConditionTrue)
	setBudgetCondition(instance, workloadsv1alpha2.RoleInstanceReady, v1.ConditionFalse)
}

// prepareGroupRestartBudget is the pure mutation seam for the accepted-restart
// status transaction. Call ONLY after fresh owned failure evidence and restart/
// backoff guards have accepted a group rebuild. A caller that keeps additional
// bookkeeping can compose this mutation with its own state and
// updateRestartTracking in ONE optimistic status patch. Exhaustion does not
// consume an attempt.
func prepareGroupRestartBudget(instance *workloadsv1alpha2.RoleInstance) bool {
	limit := instance.Spec.GetRestartPolicyConfig().MaxGroupRestarts
	if limit == nil && instance.Status.GroupRestartRevision == "" {
		return true
	}
	if limit != nil && instance.Status.GroupRestartCount >= *limit {
		// Denial must not mutate the baseline used by exhaustRestartBudget:
		// that transaction owns persisting the exhausted marker before teardown.
		return false
	}
	instance.Status.GroupRestartRevision = groupRestartRevision(instance)
	// Saturate the durable API counter rather than overflowing after long-lived
	// unbounded operation with a previously configured budget.
	if instance.Status.GroupRestartCount < 1<<31-1 {
		instance.Status.GroupRestartCount++
	}
	return true
}

func (c *realControl) readBudgetInstance(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error) {
	if c.apiReader == nil {
		return nil, fmt.Errorf("restart budget requires an uncached APIReader")
	}
	fresh := &workloadsv1alpha2.RoleInstance{}
	if err := c.apiReader.Get(ctx, client.ObjectKeyFromObject(instance), fresh); err != nil {
		return nil, err
	}
	if fresh.UID != instance.UID || fresh.Generation != instance.Generation {
		return nil, fmt.Errorf("RoleInstance identity or generation changed while evaluating restart budget")
	}
	return fresh, nil
}

// listBudgetPods uses the stable RI selector (not desired component/ordinal
// labels), then filters controller UID. Terminal, removed-component and deleting
// members are included without listing an entire namespace or using cache indexes.
func (c *realControl) listBudgetPods(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) ([]*v1.Pod, error) {
	list := &v1.PodList{}
	if err := c.apiReader.List(ctx, list, client.InNamespace(instance.Namespace), client.MatchingLabels(instanceutil.GetSelectorMatchLabels(instance.Name))); err != nil {
		return nil, err
	}
	pods := make([]*v1.Pod, 0, len(list.Items))
	for i := range list.Items {
		if metav1.IsControlledBy(&list.Items[i], instance) {
			pods = append(pods, &list.Items[i])
		}
	}
	return pods, nil
}

func (c *realControl) patchBudgetStatus(ctx context.Context, fresh, base *workloadsv1alpha2.RoleInstance) error {
	return c.Status().Patch(ctx, fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// ensureBudgetOwner protects cleanup on background/foreground RI deletion, not
// just normal reconcile: a Pod finalizer must never outlive its cleanup owner.
func (c *realControl) ensureBudgetOwner(ctx context.Context, fresh *workloadsv1alpha2.RoleInstance, clearRecovery bool) error {
	_, hasRecovery := fresh.Annotations[constants.RoleInstanceRecoverAnnotationKey]
	if slices.Contains(fresh.Finalizers, constants.GroupRestartBudgetCleanupFinalizer) && (!clearRecovery || !hasRecovery) {
		return nil
	}
	base := fresh.DeepCopy()
	if !slices.Contains(fresh.Finalizers, constants.GroupRestartBudgetCleanupFinalizer) {
		fresh.Finalizers = append(fresh.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
	}
	if clearRecovery {
		delete(fresh.Annotations, constants.RoleInstanceRecoverAnnotationKey)
	}
	return c.Patch(ctx, fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// hasRestartBudgetLifecycle keeps all uncached budget work off the ordinary RI
// hot path, including teardown. Keep the finalizer check even after a limit unset.
func hasRestartBudgetLifecycle(instance *workloadsv1alpha2.RoleInstance) bool {
	return instance.Spec.GetRestartPolicyConfig().MaxGroupRestarts != nil ||
		instance.Status.GroupRestartRevision != "" || instance.Status.GroupRestartCount != 0 ||
		instance.Status.RestartBudgetExhausted || slices.Contains(instance.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
}

// reconcileRestartBudget runs BEFORE backoff and ordinary Pod processing. It
// handles persisted suppression even if the policy/budget has since been removed.
// nil-budget healthy RIs are never blocked by this lifecycle.
func (c *realControl) reconcileRestartBudget(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) (bool, error) {
	if !hasRestartBudgetLifecycle(instance) {
		return false, nil
	}
	fresh, err := c.readBudgetInstance(ctx, instance)
	if err != nil {
		return true, err
	}
	copyRestartBudgetStatus(instance, fresh)
	if fresh.DeletionTimestamp != nil {
		return true, c.CleanupRestartBudget(ctx, fresh)
	}
	revisionChanged := fresh.Status.GroupRestartRevision != "" && fresh.Status.GroupRestartRevision != groupRestartRevision(fresh)
	recovering := fresh.Status.RestartBudgetExhausted && fresh.Annotations[constants.RoleInstanceRecoverAnnotationKey] == "true"
	if revisionChanged || recovering {
		// Release retained objects FIRST. If we crash, the persisted exhausted state
		// and recovery annotation/revision mismatch will retry this idempotently.
		if err := c.releaseBudgetPods(ctx, fresh); err != nil {
			return true, err
		}
		base := fresh.DeepCopy()
		fresh.Status.GroupRestartRevision = groupRestartRevision(fresh)
		fresh.Status.GroupRestartCount = 0
		fresh.Status.RestartBudgetExhausted = false
		fresh.Status.Conditions = slices.DeleteFunc(fresh.Status.Conditions, func(cond workloadsv1alpha2.RoleInstanceCondition) bool {
			return cond.Type == workloadsv1alpha2.RoleInstanceDegraded || cond.Type == workloadsv1alpha2.RoleInstanceRestarting
		})
		fresh.Status.RestartCount = 0
		fresh.Status.LastRestartTime = nil
		if err := c.patchBudgetStatus(ctx, fresh, base); err != nil {
			return true, err
		}
		c.ClearRestarting(fresh)
		if recovering {
			base = fresh.DeepCopy()
			delete(fresh.Annotations, constants.RoleInstanceRecoverAnnotationKey)
			if err := c.Patch(ctx, fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
				return true, err
			}
		}
		copyRestartBudgetStatus(instance, fresh)
		// Force a fresh pass after releasing old same-name objects/status.
		return true, nil
	}
	if fresh.Status.RestartBudgetExhausted {
		EnforceRestartBudgetConditions(instance)
		return true, c.terminateBudgetPods(ctx, fresh)
	}
	limit := fresh.Spec.GetRestartPolicyConfig().MaxGroupRestarts
	if limit == nil {
		return false, nil
	}
	if !workloadsv1alpha2.IsRoleInstanceRecreatePolicy(fresh.Spec.GetRestartPolicy()) {
		return true, fmt.Errorf("maxGroupRestarts requires a whole-RoleInstance recreate policy")
	}
	if err := c.ensureBudgetOwner(ctx, fresh, false); err != nil {
		return true, err
	}
	if fresh.Status.GroupRestartCount < *limit {
		return false, nil
	}
	// No remaining budget: do not let exponential backoff postpone exhaustion.
	pods, err := c.listBudgetPods(ctx, fresh)
	if err != nil {
		return true, err
	}
	if isInstanceRestarting(fresh) || !shouldRecreateInstance(fresh, pods, fresh.Status.InPlaceUpdateContainerBaselines) {
		return false, nil
	}
	if err := c.exhaustRestartBudget(ctx, fresh); err != nil {
		return true, err
	}
	copyRestartBudgetStatus(instance, fresh)
	EnforceRestartBudgetConditions(instance)
	return true, c.terminateBudgetPods(ctx, fresh)
}

func (c *realControl) exhaustRestartBudget(ctx context.Context, fresh *workloadsv1alpha2.RoleInstance) error {
	// Invalidate any annotation predating exhaustion BEFORE the status CAS. An
	// intervening annotation/spec update conflicts with that CAS and must retry.
	if err := c.ensureBudgetOwner(ctx, fresh, true); err != nil {
		return err
	}
	base := fresh.DeepCopy()
	fresh.Status.GroupRestartRevision = groupRestartRevision(fresh)
	fresh.Status.RestartBudgetExhausted = true
	EnforceRestartBudgetConditions(fresh)
	return c.patchBudgetStatus(ctx, fresh, base)
}

// acceptBudgetRestart revalidates live identity, generation and owned Pod failure
// evidence. Budget + Restarting + backoff are persisted before the caller deletes
// any Pod. It returns the live complete group, never the cached delete list.
func (c *realControl) acceptBudgetRestart(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) ([]*v1.Pod, bool, error) {
	fresh, err := c.readBudgetInstance(ctx, instance)
	if err != nil {
		return nil, false, err
	}
	if fresh.DeletionTimestamp != nil || fresh.Status.RestartBudgetExhausted || isInstanceRestarting(fresh) {
		return nil, false, nil
	}
	pods, err := c.listBudgetPods(ctx, fresh)
	if err != nil {
		return nil, false, err
	}
	if !shouldRecreateInstance(fresh, pods, fresh.Status.InPlaceUpdateContainerBaselines) {
		return nil, false, nil
	}
	base := fresh.DeepCopy()
	if !prepareGroupRestartBudget(fresh) {
		if err := c.exhaustRestartBudget(ctx, fresh); err != nil {
			return nil, false, err
		}
		copyRestartBudgetStatus(instance, fresh)
		return nil, false, c.terminateBudgetPods(ctx, fresh)
	}
	// No in-memory cache mutation until the optimistic status transaction succeeds.
	updateRestartTracking(fresh)
	setBudgetRestarting(fresh)
	if err := c.patchBudgetStatus(ctx, fresh, base); err != nil {
		return nil, false, err
	}
	instance.Status = *fresh.Status.DeepCopy()
	restartingCache.Store(instanceKey(instance), true)
	return pods, true, nil
}

func setBudgetRestarting(instance *workloadsv1alpha2.RoleInstance) {
	// setRestartingCondition also writes a cache; use a local snapshot until commit.
	for i := range instance.Status.Conditions {
		if instance.Status.Conditions[i].Type == workloadsv1alpha2.RoleInstanceRestarting {
			instance.Status.Conditions = slices.Delete(instance.Status.Conditions, i, i+1)
			break
		}
	}
	instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{
		Type: workloadsv1alpha2.RoleInstanceRestarting, Status: v1.ConditionTrue,
		LastTransitionTime: metav1.Now(), Reason: restartPolicyTriggeredReason, Message: restartPolicyTriggeredMessage,
	})
}

func (c *realControl) mutateBudgetPodFinalizer(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, pod *v1.Pod, retain bool) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &v1.Pod{}
		if err := c.apiReader.Get(ctx, client.ObjectKeyFromObject(pod), fresh); err != nil {
			return client.IgnoreNotFound(err)
		}
		if fresh.UID != pod.UID || !metav1.IsControlledBy(fresh, instance) {
			return nil
		}
		base := fresh.DeepCopy()
		has := slices.Contains(fresh.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
		if retain {
			if has || fresh.DeletionTimestamp != nil {
				return nil
			}
			fresh.Finalizers = append(fresh.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
		} else {
			if !has {
				return nil
			}
			fresh.Finalizers = slices.DeleteFunc(fresh.Finalizers, func(s string) bool { return s == constants.GroupRestartBudgetCleanupFinalizer })
		}
		return c.Patch(ctx, fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	})
}

func (c *realControl) terminateBudgetPods(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) error {
	pods, err := c.listBudgetPods(ctx, instance)
	if err != nil {
		return err
	}
	// Retain every still-eligible object BEFORE initiating any deletion.
	for _, pod := range pods {
		if err := c.mutateBudgetPodFinalizer(ctx, instance, pod, true); err != nil {
			return err
		}
	}
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil {
			continue
		}
		// ResourceVersion must NOT be a deletion precondition: kubelet status updates
		// and our finalizer patches legitimately change it between read and delete.
		fresh := &v1.Pod{}
		if err := c.apiReader.Get(ctx, client.ObjectKeyFromObject(pod), fresh); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if fresh.UID != pod.UID || !metav1.IsControlledBy(fresh, instance) || fresh.DeletionTimestamp != nil {
			continue
		}
		uid := fresh.UID
		if err := c.Delete(ctx, fresh, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (c *realControl) releaseBudgetPods(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) error {
	pods, err := c.listBudgetPods(ctx, instance)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		if err := c.mutateBudgetPodFinalizer(ctx, instance, pod, false); err != nil {
			return err
		}
	}
	return nil
}

// CleanupRestartBudget is teardown, NEVER recovery: preserve counters and do not
// create/delete Pods here. The RI finalizer keeps this retryable through failures.
func (c *realControl) CleanupRestartBudget(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) error {
	if !hasRestartBudgetLifecycle(instance) {
		return nil
	}
	if c.apiReader == nil {
		return fmt.Errorf("restart budget cleanup requires an uncached APIReader")
	}
	if err := c.releaseBudgetPods(ctx, instance); err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &workloadsv1alpha2.RoleInstance{}
		if err := c.apiReader.Get(ctx, client.ObjectKeyFromObject(instance), fresh); err != nil {
			return client.IgnoreNotFound(err)
		}
		if fresh.UID != instance.UID || fresh.DeletionTimestamp == nil {
			return nil
		}
		if !slices.Contains(fresh.Finalizers, constants.GroupRestartBudgetCleanupFinalizer) {
			return nil
		}
		base := fresh.DeepCopy()
		fresh.Finalizers = slices.DeleteFunc(fresh.Finalizers, func(s string) bool { return s == constants.GroupRestartBudgetCleanupFinalizer })
		return c.Patch(ctx, fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	})
}

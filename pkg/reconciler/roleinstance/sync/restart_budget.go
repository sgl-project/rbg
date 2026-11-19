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
	"errors"
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

	recoveryAnnotationValue       = "true"
	restartPolicyTriggeredReason  = "RestartPolicyTriggered"
	restartPolicyTriggeredMessage = "Instance is being recreated due to restart policy"
)

var errRestartBudgetGenerationChanged = errors.New("RoleInstance generation changed while evaluating restart budget")

// groupRestartRevision identifies the group's component topology and Pod
// templates, including sizes, as LWS revisions do. Policy, delays, budget and RI
// metadata are excluded; scaling the number of RI replicas does not change it.
func groupRestartRevision(instance *workloadsv1alpha2.RoleInstance) string {
	components := instance.DeepCopy().Spec.Components
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
	if fresh.UID != instance.UID {
		return nil, fmt.Errorf("RoleInstance identity changed while evaluating restart budget")
	}
	if fresh.Generation != instance.Generation {
		return nil, errRestartBudgetGenerationChanged
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

func (c *realControl) clearRecoveryAnnotation(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) error {
	if c.apiReader == nil {
		return fmt.Errorf("restart budget recovery cleanup requires an uncached APIReader")
	}
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &workloadsv1alpha2.RoleInstance{}
		if err := c.apiReader.Get(ctx, client.ObjectKeyFromObject(instance), fresh); err != nil {
			return client.IgnoreNotFound(err)
		}
		if fresh.UID != instance.UID || fresh.Generation != instance.Generation || fresh.DeletionTimestamp != nil ||
			fresh.Status.RestartBudgetExhausted || fresh.Annotations[constants.RoleInstanceRecoverAnnotationKey] != recoveryAnnotationValue {
			return nil
		}
		base := fresh.DeepCopy()
		delete(fresh.Annotations, constants.RoleInstanceRecoverAnnotationKey)
		return c.Patch(ctx, fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	})
}

// HasRestartBudgetLifecycle reports whether budget configuration or persisted
// lifecycle state requires budget-aware reconciliation and status preservation.
// Keep the finalizer check even after a limit is unset.
func HasRestartBudgetLifecycle(instance *workloadsv1alpha2.RoleInstance) bool {
	return instance.Spec.GetRestartPolicyConfig().MaxGroupRestarts != nil ||
		instance.Status.GroupRestartRevision != "" || instance.Status.GroupRestartCount != 0 ||
		instance.Status.RestartBudgetExhausted || slices.Contains(instance.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
}

// reconcileRestartBudget runs BEFORE backoff and ordinary Pod processing. It
// handles persisted suppression even if the policy/budget has since been removed.
// nil-budget healthy RIs are never blocked by this lifecycle.
func (c *realControl) reconcileRestartBudget(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) (bool, error) {
	if !HasRestartBudgetLifecycle(instance) {
		return false, nil
	}
	fresh, err := c.readBudgetInstance(ctx, instance)
	if errors.Is(err, errRestartBudgetGenerationChanged) {
		// The spec watch will supply the new generation; do not act on old intent.
		return true, nil
	}
	if err != nil {
		return true, err
	}
	copyRestartBudgetStatus(instance, fresh)
	if fresh.DeletionTimestamp != nil {
		return true, c.CleanupRestartBudget(ctx, fresh)
	}
	if fresh.Annotations[constants.RoleInstanceRecoverAnnotationKey] == recoveryAnnotationValue && !fresh.Status.RestartBudgetExhausted {
		// Recovery reset committed before annotation consumption; finish the
		// idempotent metadata cleanup on the next pass after a crash or error.
		return true, c.clearRecoveryAnnotation(ctx, fresh)
	}
	revisionChanged := fresh.Status.GroupRestartRevision != "" && fresh.Status.GroupRestartRevision != groupRestartRevision(fresh)
	recovering := fresh.Status.RestartBudgetExhausted && fresh.Annotations[constants.RoleInstanceRecoverAnnotationKey] == recoveryAnnotationValue
	if revisionChanged || recovering {
		if fresh.Status.RestartBudgetExhausted {
			// Exhaustion may have persisted before its termination requests succeeded.
			// Keep suppression and recovery intent until the old group is actually gone;
			// otherwise our own Terminating Pods would immediately spend the new budget.
			done, err := c.drainExhaustedBudgetPods(ctx, fresh)
			if err != nil || !done {
				return true, err
			}
		} else if err := c.releaseBudgetPods(ctx, fresh); err != nil {
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
			if err := c.clearRecoveryAnnotation(ctx, fresh); err != nil {
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
	if isInstanceRestarting(fresh) {
		// Drain may return early while the informer still lacks this guard (or
		// holds the previous completed wave). Propagate it to computed status.
		syncRestartingConditionFromFresh(instance, fresh)
		return true, c.resumeBudgetRestartWave(ctx, instance, fresh)
	}
	limit := fresh.Spec.GetRestartPolicyConfig().MaxGroupRestarts
	if limit == nil {
		return false, nil
	}
	if fresh.Spec.GetRestartPolicy() != workloadsv1alpha2.RecreateRoleInstanceOnPodRestart {
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
	recreate := shouldRecreateInstance(fresh, pods, fresh.Status.InPlaceUpdateContainerBaselines)
	if isInstanceRestarting(fresh) || !recreate {
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

// resumeBudgetRestartWave retries the original UID drain through the existing
// wave helpers. Persist completion before a subsequent rebuild can be accepted.
func (c *realControl) resumeBudgetRestartWave(ctx context.Context, instance, fresh *workloadsv1alpha2.RoleInstance) error {
	pods, err := c.listBudgetPods(ctx, fresh)
	if err != nil {
		return err
	}
	if c.isAlreadyRestarting(ctx, fresh, pods, fresh) {
		pending := pendingWaveDeletions(fresh, pods)
		if len(pending) == 0 {
			return nil
		}
		_, err := c.deletePods(ctx, fresh, pending)
		return err
	}
	base := fresh.DeepCopy()
	completeRestartingCondition(fresh)
	if err := c.patchBudgetStatus(ctx, fresh, base); err != nil {
		return err
	}
	c.ClearRestarting(fresh)
	syncRestartingConditionFromFresh(instance, fresh)
	return nil
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
	reason, podName := recreateTriggerReason(fresh, pods, fresh.Status.InPlaceUpdateContainerBaselines)
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
	rememberRestartWave(instance, pods)
	c.recorder.Eventf(fresh, v1.EventTypeNormal, "ReCreateInstance",
		"RestartPolicy is %s, recreate all pods of instance: %v (reason=%s, pod=%s, restartCount=%d)",
		fresh.Spec.GetRestartPolicy(), client.ObjectKeyFromObject(fresh), reason, podName, fresh.Status.RestartCount)
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

// drainExhaustedBudgetPods retries termination without reacquiring the retention
// finalizer. Exhaustion already retained eligible Pods; recovery requests any
// missing deletes, releases our finalizer, then waits for the old owned Pods to disappear.
func (c *realControl) drainExhaustedBudgetPods(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) (bool, error) {
	pods, err := c.listBudgetPods(ctx, instance)
	if err != nil {
		return false, err
	}
	for _, pod := range pods {
		fresh := &v1.Pod{}
		if err := c.apiReader.Get(ctx, client.ObjectKeyFromObject(pod), fresh); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, err
		}
		if fresh.UID != pod.UID || !metav1.IsControlledBy(fresh, instance) {
			continue
		}
		if fresh.DeletionTimestamp == nil {
			uid := fresh.UID
			if err := c.Delete(ctx, fresh, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
				return false, err
			}
		}
		if err := c.mutateBudgetPodFinalizer(ctx, instance, fresh, false); err != nil {
			return false, err
		}
	}
	remaining, err := c.listBudgetPods(ctx, instance)
	if err != nil {
		return false, err
	}
	// Delete acceptance and finalizer release are not completion; wait for the old
	// API objects to disappear before resetting exhaustion or consuming recovery.
	return len(remaining) == 0, nil
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
	if !HasRestartBudgetLifecycle(instance) {
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

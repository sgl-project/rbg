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
	"strings"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

// GroupRestartBudgetExceeded is the reason reported by the RI circuit breaker.
const (
	GroupRestartBudgetExceeded = workloadsv1alpha2.ReasonGroupRestartBudgetExceeded
	// GroupRestartBudgetMessage is shared by computed and persisted conditions.
	GroupRestartBudgetMessage = "Automatic group recovery exhausted; annotate this RoleInstance with rbg.workloads.x-k8s.io/recover=true to recover"
	// GroupRestartBudgetRecovered is emitted after an exhausted budget is reset.
	GroupRestartBudgetRecovered = "GroupRestartBudgetRecovered"
	// RestartBudgetRequeueAfter is a safety backstop; RI/Pod watches drive normal progress.
	RestartBudgetRequeueAfter = 30 * time.Second

	recoveryAnnotationValue       = "true"
	restartPolicyTriggeredReason  = "RestartPolicyTriggered"
	restartPolicyTriggeredMessage = "Instance is being recreated due to restart policy"
)

var errRestartBudgetGenerationChanged = errors.New("RoleInstance generation changed while evaluating restart budget")

func selectedControllerRevision(revision *apps.ControllerRevision) string {
	if revision == nil {
		return ""
	}
	return revision.Name
}

// groupRestartRevision uses the selected ControllerRevision identity, which the
// RI revision reconciler preserves across serializer drift, plus component sizes
// (excluded from the normal RI revision). Never re-hash typed Pod templates here.
func groupRestartRevision(instance *workloadsv1alpha2.RoleInstance, selectedRevision string) string {
	type componentSize struct {
		Name string `json:"name"`
		Size *int32 `json:"size"`
	}
	sizes := make([]componentSize, 0, len(instance.Spec.Components))
	for _, component := range instance.Spec.Components {
		sizes = append(sizes, componentSize{Name: component.Name, Size: component.Size})
	}
	slices.SortFunc(sizes, func(a, b componentSize) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	data, _ := json.Marshal(sizes)
	return fmt.Sprintf("%s/%x", selectedRevision, sha256.Sum256(data))
}

// isLegacyGroupRestartRevision reports a budget revision persisted by builds up
// to v0.9.0-alpha.5: a bare SHA-256 digest of the component JSON. The current
// identity always separates the ControllerRevision name and size digest by "/".
func isLegacyGroupRestartRevision(revision string) bool {
	return revision != "" && !strings.Contains(revision, "/")
}

func copyRestartBudgetStatus(to, from *workloadsv1alpha2.RoleInstance) {
	to.Status.GroupRestartCount = from.Status.GroupRestartCount
	to.Status.GroupRestartRevision = from.Status.GroupRestartRevision
	to.Status.RestartBudgetExhausted = from.Status.RestartBudgetExhausted
}

// hasExhaustedBudgetConditions reports whether persisted conditions still carry
// budget-owned facts after a reset.
func hasExhaustedBudgetConditions(instance *workloadsv1alpha2.RoleInstance) bool {
	return slices.ContainsFunc(instance.Status.Conditions, func(cond workloadsv1alpha2.RoleInstanceCondition) bool {
		return (cond.Type == workloadsv1alpha2.RoleInstanceReady ||
			cond.Type == workloadsv1alpha2.RoleInstanceProgressing) &&
			cond.Reason == GroupRestartBudgetExceeded
	})
}

// removeExhaustedBudgetConditions drops the budget-owned Ready and Progressing
// facts; the generic status writer recomputes both from live Pod state.
func removeExhaustedBudgetConditions(instance *workloadsv1alpha2.RoleInstance) {
	instance.Status.Conditions = slices.DeleteFunc(instance.Status.Conditions, func(cond workloadsv1alpha2.RoleInstanceCondition) bool {
		return (cond.Type == workloadsv1alpha2.RoleInstanceReady ||
			cond.Type == workloadsv1alpha2.RoleInstanceProgressing) &&
			cond.Reason == GroupRestartBudgetExceeded
	})
}

func setBudgetCondition(instance *workloadsv1alpha2.RoleInstance, typ workloadsv1alpha2.RoleInstanceConditionType, status v1.ConditionStatus) {
	condition := workloadsv1alpha2.RoleInstanceCondition{Type: typ, Status: status,
		Reason: GroupRestartBudgetExceeded, Message: GroupRestartBudgetMessage, LastTransitionTime: metav1.Now(),
		ObservedGeneration: instance.Generation}
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
// an exhausted RI or reclassify it as repair activity. After a reset, it drops the
// budget-owned Ready and Progressing facts so a stale snapshot cannot resurrect
// them. The budget controller is the sole owner of Degraded.
func EnforceRestartBudgetConditions(instance *workloadsv1alpha2.RoleInstance) {
	if !instance.Status.RestartBudgetExhausted {
		removeExhaustedBudgetConditions(instance)
		return
	}
	// Keep a more specific drain-progress message written by the budget
	// lifecycle when generic status calculation enforces suppression again.
	if slices.ContainsFunc(instance.Status.Conditions, func(cond workloadsv1alpha2.RoleInstanceCondition) bool {
		return cond.Type == workloadsv1alpha2.RoleInstanceDegraded && cond.Status == v1.ConditionTrue && cond.Reason == GroupRestartBudgetExceeded
	}) {
		for i := range instance.Status.Conditions {
			if instance.Status.Conditions[i].Type == workloadsv1alpha2.RoleInstanceDegraded &&
				instance.Status.Conditions[i].Status == v1.ConditionTrue &&
				instance.Status.Conditions[i].Reason == GroupRestartBudgetExceeded {
				instance.Status.Conditions[i].ObservedGeneration = instance.Generation
			}
		}
	} else {
		setBudgetCondition(instance, workloadsv1alpha2.RoleInstanceDegraded, v1.ConditionTrue)
	}
	setBudgetCondition(instance, workloadsv1alpha2.RoleInstanceReady, v1.ConditionFalse)
	setBudgetCondition(instance, workloadsv1alpha2.RoleInstanceProgressing, v1.ConditionTrue)
}

// prepareGroupRestartBudget is the pure mutation seam for the accepted-restart
// status transaction. Call ONLY after fresh owned failure evidence and restart/
// backoff guards have accepted a group rebuild. A caller that keeps additional
// bookkeeping can compose this mutation with its own state and
// updateRestartTracking in ONE optimistic status patch. Exhaustion does not
// consume an attempt.
func prepareGroupRestartBudget(instance *workloadsv1alpha2.RoleInstance, selectedRevision string) bool {
	limit := instance.Spec.GetRestartPolicyConfig().MaxGroupRestarts
	if limit == nil && instance.Status.GroupRestartRevision == "" {
		return true
	}
	if limit != nil && instance.Status.GroupRestartCount >= *limit {
		// Denial must not mutate the baseline used by exhaustRestartBudget:
		// that transaction owns persisting the exhausted marker before teardown.
		return false
	}
	instance.Status.GroupRestartRevision = groupRestartRevision(instance, selectedRevision)
	// Saturate the durable API counter rather than overflowing after long-lived
	// unbounded operation with a previously configured budget.
	if instance.Status.GroupRestartCount < 1<<31-1 {
		instance.Status.GroupRestartCount++
	}
	return true
}

// finishBudgetMetadataCleanup finishes a committed reset, including legacy
// cleanup owners acquired before the first exhaustion.
func (c *realControl) finishBudgetMetadataCleanup(ctx context.Context, instance, fresh *workloadsv1alpha2.RoleInstance) (bool, error) {
	if hasExhaustedBudgetConditions(fresh) {
		base := fresh.DeepCopy()
		removeExhaustedBudgetConditions(fresh)
		if err := c.patchBudgetStatus(ctx, fresh, base); err != nil {
			return true, err
		}
	}
	syncRestartingConditionFromFresh(instance, fresh)
	if err := c.releaseBudgetOwner(ctx, fresh); err != nil {
		return true, err
	}
	return true, c.clearRecoveryAnnotation(ctx, fresh)
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

func (c *realControl) releaseBudgetOwner(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) error {
	if c.apiReader == nil {
		return fmt.Errorf("restart budget owner cleanup requires an uncached APIReader")
	}
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &workloadsv1alpha2.RoleInstance{}
		if err := c.apiReader.Get(ctx, client.ObjectKeyFromObject(instance), fresh); err != nil {
			return client.IgnoreNotFound(err)
		}
		if fresh.UID != instance.UID || fresh.Generation != instance.Generation || fresh.DeletionTimestamp != nil ||
			fresh.Status.RestartBudgetExhausted || !slices.Contains(fresh.Finalizers, constants.GroupRestartBudgetCleanupFinalizer) {
			return nil
		}
		// Keep the cleanup owner until no owned Pod carries its finalizer. A
		// failed patch or crash is retried from the still-present RI finalizer.
		if err := c.releaseBudgetPods(ctx, fresh); err != nil {
			return err
		}
		base := fresh.DeepCopy()
		fresh.Finalizers = slices.DeleteFunc(fresh.Finalizers, func(finalizer string) bool {
			return finalizer == constants.GroupRestartBudgetCleanupFinalizer
		})
		return c.Patch(ctx, fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	})
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

func (c *realControl) reconcileRestartBudgetOnce(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, selectedRevision string, podSnapshots ...[]*v1.Pod) (bool, error) {
	observation := budgetObservationFrom(ctx)
	if observation == nil || !HasRestartBudgetLifecycle(instance) {
		return c.reconcileRestartBudget(ctx, instance, selectedRevision, podSnapshots...)
	}
	if observation.checked && observation.uid == instance.UID && observation.generation == instance.Generation &&
		observation.selectedRevision == selectedRevision {
		if observation.fresh != nil {
			copyRestartBudgetStatus(instance, observation.fresh)
		}
		return observation.blocked, observation.err
	}
	*observation = budgetObservation{uid: instance.UID, generation: instance.Generation, selectedRevision: selectedRevision}
	observation.blocked, observation.err = c.reconcileRestartBudget(ctx, instance, selectedRevision, podSnapshots...)
	observation.checked = true
	return observation.blocked, observation.err
}

// reconcileRestartBudget runs BEFORE backoff and ordinary Pod processing. It
// handles persisted suppression even if the policy/budget has since been removed.
// nil-budget healthy RIs are never blocked by this lifecycle.
func (c *realControl) reconcileRestartBudget(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, selectedRevision string, podSnapshots ...[]*v1.Pod) (bool, error) {
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
	if observation := budgetObservationFrom(ctx); observation != nil && observation.uid == instance.UID &&
		observation.generation == instance.Generation && observation.selectedRevision == selectedRevision {
		observation.fresh = fresh.DeepCopy()
	}
	if fresh.DeletionTimestamp != nil {
		return true, c.CleanupRestartBudget(ctx, fresh)
	}
	if fresh.Status.RestartBudgetExhausted {
		// Reacquire a manually removed owner before any Pod retention retry.
		// Do not consume a post-exhaustion recovery request here.
		if err := c.ensureBudgetOwner(ctx, fresh, false); err != nil {
			return true, err
		}
	}
	if !fresh.Status.RestartBudgetExhausted && (fresh.Annotations[constants.RoleInstanceRecoverAnnotationKey] == recoveryAnnotationValue ||
		slices.Contains(fresh.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)) {
		return c.finishBudgetMetadataCleanup(ctx, instance, fresh)
	}
	if selectedRevision == "" {
		return true, fmt.Errorf("restart budget requires a selected ControllerRevision")
	}
	if err := c.migrateGroupRestartRevision(ctx, instance, fresh, selectedRevision); err != nil {
		return true, err
	}
	revisionChanged := fresh.Status.GroupRestartRevision != "" && fresh.Status.GroupRestartRevision != groupRestartRevision(fresh, selectedRevision)
	recovering := fresh.Status.RestartBudgetExhausted && fresh.Annotations[constants.RoleInstanceRecoverAnnotationKey] == recoveryAnnotationValue
	if revisionChanged || recovering {
		wasExhausted := fresh.Status.RestartBudgetExhausted
		if wasExhausted {
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
		return true, c.resetRestartBudget(ctx, instance, fresh, recovering, selectedRevision)
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
	// The RI cleanup owner is acquired only when exhaustion is persisted, so a
	// healthy budgeted RI does not block deletion after a controller downgrade.
	if fresh.Status.GroupRestartCount < *limit {
		return false, nil
	}
	return c.reconcileRestartBudgetAtLimit(ctx, instance, fresh, selectedRevision, podSnapshots...)
}

func (c *realControl) reconcileRestartBudgetAtLimit(ctx context.Context, instance, fresh *workloadsv1alpha2.RoleInstance, selectedRevision string, podSnapshots ...[]*v1.Pod) (bool, error) {
	// A complete informer snapshot is only a hint to avoid healthy-path LISTs;
	// suspected faults still require the authoritative member list below. A
	// snapshot lagging a new fault only delays exhaustion to that Pod's watch
	// event; acceptBudgetRestart revalidates live state, so it cannot overspend.
	if len(podSnapshots) > 0 && !shouldRecreateInstance(fresh, podSnapshots[0], fresh.Status.InPlaceUpdateContainerBaselines) {
		return false, nil
	}
	pods, err := c.listBudgetPods(ctx, fresh)
	if err != nil {
		return true, err
	}
	if !shouldRecreateInstance(fresh, pods, fresh.Status.InPlaceUpdateContainerBaselines) {
		return false, nil
	}
	if err := c.exhaustRestartBudget(ctx, fresh, selectedRevision, pods...); err != nil {
		return true, err
	}
	copyRestartBudgetStatus(instance, fresh)
	EnforceRestartBudgetConditions(instance)
	return true, c.terminateBudgetPods(ctx, fresh)
}

// migrateGroupRestartRevision re-stamps a legacy budget revision in place. The
// legacy digest is not comparable with the current identity (and was itself
// subject to serializer drift), so treating the format change as a topology
// change would reset spent budgets and release exhausted groups without
// recover=true. Count, exhaustion, conditions and restart tracking are kept; a
// later real topology change selects a new revision and resets as usual.
func (c *realControl) migrateGroupRestartRevision(ctx context.Context, instance, fresh *workloadsv1alpha2.RoleInstance, selectedRevision string) error {
	if !isLegacyGroupRestartRevision(fresh.Status.GroupRestartRevision) {
		return nil
	}
	base := fresh.DeepCopy()
	fresh.Status.GroupRestartRevision = groupRestartRevision(fresh, selectedRevision)
	if err := c.patchBudgetStatus(ctx, fresh, base); err != nil {
		return err
	}
	copyRestartBudgetStatus(instance, fresh)
	if observation := budgetObservationFrom(ctx); observation != nil && observation.fresh != nil && observation.uid == fresh.UID {
		copyRestartBudgetStatus(observation.fresh, fresh)
	}
	return nil
}

// resetRestartBudget commits a new topology budget after any exhausted group
// has drained. A non-exhausted revision change leaves the wave/backoff intact.
func (c *realControl) resetRestartBudget(ctx context.Context, instance, fresh *workloadsv1alpha2.RoleInstance, recovering bool, selectedRevision string) error {
	wasExhausted := fresh.Status.RestartBudgetExhausted
	base := fresh.DeepCopy()
	fresh.Status.GroupRestartRevision = groupRestartRevision(fresh, selectedRevision)
	fresh.Status.GroupRestartCount = 0
	fresh.Status.RestartBudgetExhausted = false
	removeExhaustedBudgetConditions(fresh)
	fresh.Status.Conditions = slices.DeleteFunc(fresh.Status.Conditions, func(cond workloadsv1alpha2.RoleInstanceCondition) bool {
		// Degraded and an exhausted Restarting wave belong to the budget lifecycle.
		return cond.Type == workloadsv1alpha2.RoleInstanceDegraded ||
			(wasExhausted && cond.Type == workloadsv1alpha2.RoleInstanceRestarting)
	})
	// Preserve the old accepted wave and backoff on a non-exhausted revision
	// change; #573 absorbs the controller's own Terminating-member triggers.
	if wasExhausted {
		fresh.Status.RestartCount = 0
		fresh.Status.LastRestartTime = nil
	}
	if err := c.patchBudgetStatus(ctx, fresh, base); err != nil {
		return err
	}
	if wasExhausted {
		c.ClearRestarting(fresh)
		c.recorder.Event(fresh, v1.EventTypeNormal, GroupRestartBudgetRecovered,
			"Automatic group recovery resumed after the old group terminated")
	}
	syncRestartingConditionFromFresh(instance, fresh)
	copyRestartBudgetStatus(instance, fresh)
	if err := c.releaseBudgetOwner(ctx, fresh); err != nil {
		return err
	}
	if recovering {
		return c.clearRecoveryAnnotation(ctx, fresh)
	}
	return nil
}

func (c *realControl) exhaustRestartBudget(ctx context.Context, fresh *workloadsv1alpha2.RoleInstance, selectedRevision string, pods ...*v1.Pod) error {
	if fresh.Status.RestartBudgetExhausted {
		return nil
	}
	// Invalidate any annotation predating exhaustion BEFORE the status CAS. An
	// intervening annotation/spec update conflicts with that CAS and must retry.
	if err := c.ensureBudgetOwner(ctx, fresh, true); err != nil {
		return err
	}
	base := fresh.DeepCopy()
	fresh.Status.GroupRestartRevision = groupRestartRevision(fresh, selectedRevision)
	fresh.Status.RestartBudgetExhausted = true
	EnforceRestartBudgetConditions(fresh)
	if err := c.patchBudgetStatus(ctx, fresh, base); err != nil {
		return err
	}
	reason, podName := recreateTriggerReason(fresh, pods, fresh.Status.InPlaceUpdateContainerBaselines)
	c.recorder.Eventf(fresh, v1.EventTypeWarning, GroupRestartBudgetExceeded,
		"Automatic group recovery exhausted (reason=%s, pod=%s, count=%d, limit=%d); annotate the RoleInstance with %s=true to recover",
		reason, podName, fresh.Status.GroupRestartCount, ptr.Deref(fresh.Spec.GetRestartPolicyConfig().MaxGroupRestarts, 0), constants.RoleInstanceRecoverAnnotationKey)
	return nil
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
func (c *realControl) acceptBudgetRestart(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, selectedRevision string) ([]*v1.Pod, bool, error) {
	if selectedRevision == "" {
		return nil, false, fmt.Errorf("restart budget requires a selected ControllerRevision")
	}
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
	if !prepareGroupRestartBudget(fresh, selectedRevision) {
		if err := c.exhaustRestartBudget(ctx, fresh, selectedRevision, pods...); err != nil {
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
		if pod.DeletionTimestamp != nil || slices.Contains(pod.Finalizers, constants.GroupRestartBudgetCleanupFinalizer) {
			continue
		}
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
		// Live-listed Pods already Terminating need no delete-decision read; only
		// this controller adds the budget finalizer, so the live LIST (or the
		// fresh GET below) is authoritative for whether a release is needed.
		if pod.DeletionTimestamp == nil {
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
			pod = fresh
		}
		if !slices.Contains(pod.Finalizers, constants.GroupRestartBudgetCleanupFinalizer) {
			continue
		}
		if err := c.mutateBudgetPodFinalizer(ctx, instance, pod, false); err != nil {
			return false, err
		}
	}
	remaining, err := c.listBudgetPods(ctx, instance)
	if err != nil {
		return false, err
	}
	if len(remaining) > 0 {
		base := instance.DeepCopy()
		EnforceRestartBudgetConditions(instance)
		message := fmt.Sprintf("Recovery is waiting for %d old owned Pod object(s) to disappear; other finalizers or unfinished Pod termination may block progress", len(remaining))
		for i := range instance.Status.Conditions {
			cond := &instance.Status.Conditions[i]
			if cond.Type == workloadsv1alpha2.RoleInstanceDegraded {
				if cond.Message == message {
					return false, nil
				}
				cond.Message = message
				return false, c.patchBudgetStatus(ctx, instance, base)
			}
		}
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
		// The live LIST is authoritative for our own finalizer; skip the per-Pod
		// GET for Pods that never carried it.
		if !slices.Contains(pod.Finalizers, constants.GroupRestartBudgetCleanupFinalizer) {
			continue
		}
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

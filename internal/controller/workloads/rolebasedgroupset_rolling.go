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

package workloads

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const groupSetReplicasAnnotation = constants.RBGPrefix + "groupset-observed-replicas"

type groupSetRolloutLimits struct {
	partition      int
	maxSurge       int
	maxUnavailable int
}

func resolveGroupSetRollout(set *workloadsv1alpha2.RoleBasedGroupSet) (groupSetRolloutLimits, error) {
	var limits groupSetRolloutLimits
	strategy := set.Spec.RolloutStrategy
	if strategy.Type != "" && strategy.Type != workloadsv1alpha2.RecreateStrategyType {
		return limits, fmt.Errorf("unsupported RoleBasedGroupSet rollout strategy %q", strategy.Type)
	}
	replicas := int(*set.Spec.Replicas)
	resolve := func(value *intstr.IntOrString, defaultValue int, roundUp bool) (int, error) {
		if value == nil {
			return defaultValue, nil
		}
		result, err := intstr.GetScaledValueFromIntOrPercent(value, replicas, roundUp)
		if err != nil {
			return 0, err
		}
		if result < 0 {
			return 0, fmt.Errorf("rollout limits must not be negative")
		}
		return result, nil
	}
	var err error
	if limits.partition, err = resolve(strategy.Partition, 0, false); err != nil {
		return limits, err
	}
	if limits.maxSurge, err = resolve(strategy.MaxSurge, 0, true); err != nil {
		return limits, err
	}
	if limits.maxUnavailable, err = resolve(strategy.MaxUnavailable, 1, false); err != nil {
		return limits, err
	}
	limits.partition = min(limits.partition, replicas)
	limits.maxUnavailable = min(limits.maxUnavailable, replicas)
	if limits.maxSurge == 0 && limits.maxUnavailable == 0 {
		limits.maxUnavailable = 1
	}
	return limits, nil
}

func groupSetOrdinal(set *workloadsv1alpha2.RoleBasedGroupSet, child *workloadsv1alpha2.RoleBasedGroup) (int, bool) {
	index, err := strconv.Atoi(child.Labels[constants.GroupSetIndexLabelKey])
	return index, err == nil && index >= 0 && child.Name == fmt.Sprintf("%s-%d", set.Name, index)
}

func groupSetChildReady(child *workloadsv1alpha2.RoleBasedGroup) bool {
	return child.DeletionTimestamp.IsZero() && child.Status.ObservedGeneration >= child.Generation &&
		meta.IsStatusConditionTrue(child.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupReady))
}

func (r *RoleBasedGroupSetReconciler) listRollingGroupSetChildren(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet,
) (*workloadsv1alpha2.RoleBasedGroupList, error) {
	children := &workloadsv1alpha2.RoleBasedGroupList{}
	// Availability decisions must see previous writes even when the informer cache is behind.
	if err := r.apiReader.List(ctx, children, client.InNamespace(set.Namespace),
		client.MatchingLabels{constants.GroupSetNameLabelKey: set.Name}); err != nil {
		return nil, err
	}
	owned := children.Items[:0]
	for _, child := range children.Items {
		if metav1.IsControlledBy(&child, set) {
			owned = append(owned, child)
		}
	}
	children.Items = owned
	return children, nil
}

func (r *RoleBasedGroupSetReconciler) reconcileRollingGroupSet(ctx context.Context, key types.NamespacedName) (ctrl.Result, error) {
	set := &workloadsv1alpha2.RoleBasedGroupSet{}
	if err := r.apiReader.Get(ctx, key, set); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !set.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if set.Spec.RolloutStrategy == nil {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	limits, err := resolveGroupSetRollout(set)
	if err != nil {
		return ctrl.Result{}, err
	}
	children, err := r.listRollingGroupSetChildren(ctx, set)
	if err != nil {
		return ctrl.Result{}, err
	}
	current, update, err := r.groupSetRevisions(ctx, set, children)
	if err != nil {
		return ctrl.Result{}, err
	}
	if set.Status.CurrentRevision == "" {
		delete(set.Annotations, groupSetReplicasAnnotation)
	}

	// Persist the old template reference before deleting the last child that carries it.
	if err := r.updateRollingGroupSetStatus(ctx, set, children, current, update, limits); err != nil {
		return ctrl.Result{}, err
	}
	changed, reconcileErr := r.syncRollingGroupSet(ctx, set, children, current, update, limits)
	children, err = r.listRollingGroupSetChildren(ctx, set)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.updateRollingGroupSetStatus(ctx, set, children, current, update, limits); err != nil {
		return ctrl.Result{}, err
	}
	if reconcileErr != nil {
		return ctrl.Result{}, reconcileErr
	}
	if err := r.pruneGroupSetRevisions(ctx, set, children); err != nil {
		return ctrl.Result{}, err
	}
	if changed || set.Status.Replicas != *set.Spec.Replicas || set.Status.ReadyReplicas != *set.Spec.Replicas ||
		!r.groupSetPartitionReady(set, children, update, limits.partition) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func (r *RoleBasedGroupSetReconciler) syncRollingGroupSet(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet, children *workloadsv1alpha2.RoleBasedGroupList,
	current, update *groupSetRevision, limits groupSetRolloutLimits,
) (bool, error) {
	replicas := int(*set.Spec.Replicas)
	existing, ready, changed, err := r.deleteOutOfRangeGroupSetChildren(ctx, set, children, replicas, limits)
	if err != nil || changed {
		return changed, err
	}

	capacity := replicas + limits.maxSurge - len(children.Items)
	changed, err = r.fillMissingGroupSetBaseOrdinals(ctx, set, existing, replicas, limits, current, update, capacity)
	if err != nil || changed {
		return changed, err
	}

	outdated, outdatedBase, changed, err := r.classifyGroupSetChildrenForRollout(ctx, existing, replicas, limits, update)
	if err != nil || changed {
		return changed, err
	}

	changed, err = r.warmUpGroupSetSurge(ctx, set, existing, replicas, limits, update, outdatedBase, capacity)
	if err != nil {
		return changed, err
	}

	deleted, err := r.deleteGroupSetOutdatedChildren(ctx, set, children, existing, outdated, ready, replicas, limits, update)
	return changed || deleted, err
}

// deleteOutOfRangeGroupSetChildren removes children with a broken ordinal label or an ordinal at
// or above replicas+maxSurge, and returns the remaining children by ordinal plus the ready count
// the delete budget below is measured against.
func (r *RoleBasedGroupSetReconciler) deleteOutOfRangeGroupSetChildren(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet, children *workloadsv1alpha2.RoleBasedGroupList,
	replicas int, limits groupSetRolloutLimits,
) (map[int]*workloadsv1alpha2.RoleBasedGroup, int, bool, error) {
	minimumReady := max(0, replicas-limits.maxUnavailable)
	existing := make(map[int]*workloadsv1alpha2.RoleBasedGroup, len(children.Items))
	ready := 0
	for i := range children.Items {
		if groupSetChildReady(&children.Items[i]) {
			ready++
		}
	}
	changed := false
	for i := range children.Items {
		child := &children.Items[i]
		index, valid := groupSetOrdinal(set, child)
		if !valid || index >= replicas+limits.maxSurge {
			isReady := groupSetChildReady(child)
			if child.DeletionTimestamp.IsZero() && (!isReady || ready > minimumReady) {
				if err := r.deleteRollingGroupSetChild(ctx, child); err != nil {
					return existing, ready, changed, err
				}
				if isReady {
					ready--
				}
				changed = true
			}
			continue
		}
		existing[index] = child
	}
	return existing, ready, changed, nil
}

// fillMissingGroupSetBaseOrdinals creates the base groups whose ordinals are not currently
// occupied. An ordinal below the partition that already existed is rebuilt at the current
// revision; anything new — scale-out, or a gap above the partition — is built at the update
// revision. The observed-replicas checkpoint is what tells the two apart.
func (r *RoleBasedGroupSetReconciler) fillMissingGroupSetBaseOrdinals(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet,
	existing map[int]*workloadsv1alpha2.RoleBasedGroup, replicas int, limits groupSetRolloutLimits,
	current, update *groupSetRevision, capacity int,
) (bool, error) {
	observedReplicas, err := strconv.Atoi(set.Annotations[groupSetReplicasAnnotation])
	if err != nil {
		observedReplicas = 0
		for index := range existing {
			if index < replicas {
				observedReplicas = max(observedReplicas, index+1)
			}
		}
		if err := r.recordGroupSetReplicas(ctx, set, observedReplicas); err != nil {
			return false, err
		}
	}
	changed := false
	baseCreated := true
	for index := 0; index < replicas; index++ {
		if existing[index] != nil {
			continue
		}
		if capacity <= 0 {
			baseCreated = false
			continue
		}
		revision := update
		if index < limits.partition && index < observedReplicas {
			revision = current
		}
		if err := r.createRollingGroupSetChild(ctx, set, newRBGForSetRevision(set, index, revision)); err != nil {
			return changed, err
		}
		capacity--
		changed = true
	}
	if baseCreated {
		if err := r.recordGroupSetReplicas(ctx, set, replicas); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// classifyGroupSetChildrenForRollout splits the in-scope children into up-to-date, scale-only,
// and outdated. A scale-only child differs from the update revision only in role replicas, which
// is applied in place here; anything else is recreated by the caller under the budget.
func (r *RoleBasedGroupSetReconciler) classifyGroupSetChildrenForRollout(
	ctx context.Context, existing map[int]*workloadsv1alpha2.RoleBasedGroup, replicas int,
	limits groupSetRolloutLimits, update *groupSetRevision,
) ([]*workloadsv1alpha2.RoleBasedGroup, int, bool, error) {
	var outdated []*workloadsv1alpha2.RoleBasedGroup
	outdatedBase := 0
	changed := false
	for index, child := range existing {
		if index < limits.partition || !child.DeletionTimestamp.IsZero() || r.groupSetMatchesRevision(child, update) {
			continue
		}
		if groupSetOnlyReplicasChanged(child, update.template) {
			updated := child.DeepCopy()
			updated.Spec = *update.template.Spec.DeepCopy()
			updated.Labels[constants.GroupSetRevisionLabelKey] = update.name
			if err := r.client.Update(ctx, updated); err != nil {
				return nil, 0, changed, err
			}
			changed = true
			continue
		}
		outdated = append(outdated, child)
		if index < replicas {
			outdatedBase++
		}
	}
	return outdated, outdatedBase, changed, nil
}

func (r *RoleBasedGroupSetReconciler) warmUpGroupSetSurge(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet,
	existing map[int]*workloadsv1alpha2.RoleBasedGroup, replicas int, limits groupSetRolloutLimits,
	update *groupSetRevision, outdatedBase, capacity int,
) (bool, error) {
	// Surge only needs to cover remaining replacements beyond the unavailable budget.
	needed := min(limits.maxSurge, max(0, outdatedBase-limits.maxUnavailable))
	for index := range existing {
		if index >= replicas {
			needed--
		}
	}
	capacity = min(capacity, needed)
	changed := false
	for index := replicas; index < replicas+limits.maxSurge && capacity > 0; index++ {
		if existing[index] != nil {
			continue
		}
		if err := r.createRollingGroupSetChild(ctx, set, newRBGForSetRevision(set, index, update)); err != nil {
			return changed, err
		}
		capacity--
		changed = true
	}
	return changed, nil
}

// deleteGroupSetOutdatedChildren recreates the outdated children the budget allows, highest
// ordinal first, and reclaims surge capacity once the partition-scoped rollout is done.
func (r *RoleBasedGroupSetReconciler) deleteGroupSetOutdatedChildren(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet, children *workloadsv1alpha2.RoleBasedGroupList,
	existing map[int]*workloadsv1alpha2.RoleBasedGroup, outdated []*workloadsv1alpha2.RoleBasedGroup,
	ready, replicas int, limits groupSetRolloutLimits, update *groupSetRevision,
) (bool, error) {
	minimumReady := max(0, replicas-limits.maxUnavailable)
	if r.groupSetPartitionReady(set, children, update, limits.partition) {
		outdated = nil
		for index, child := range existing {
			if index >= replicas && child.DeletionTimestamp.IsZero() {
				outdated = append(outdated, child)
			}
		}
	}
	sort.Slice(outdated, func(i, j int) bool {
		left, _ := groupSetOrdinal(set, outdated[i])
		right, _ := groupSetOrdinal(set, outdated[j])
		return left > right
	})
	changed := false
	for _, child := range outdated {
		isReady := groupSetChildReady(child)
		if isReady && ready <= minimumReady {
			continue
		}
		if err := r.deleteRollingGroupSetChild(ctx, child); err != nil {
			return changed, err
		}
		if isReady {
			ready--
		}
		changed = true
	}
	return changed, nil
}

func (r *RoleBasedGroupSetReconciler) createRollingGroupSetChild(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet, child *workloadsv1alpha2.RoleBasedGroup,
) error {
	if err := controllerutil.SetControllerReference(set, child, r.scheme); err != nil {
		return err
	}
	if err := r.client.Create(ctx, child); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing := &workloadsv1alpha2.RoleBasedGroup{}
		if err := r.apiReader.Get(ctx, client.ObjectKeyFromObject(child), existing); err != nil {
			return err
		}
		if !metav1.IsControlledBy(existing, set) {
			return fmt.Errorf("RoleBasedGroup %s already exists and is not owned by RoleBasedGroupSet %s", child.Name, set.Name)
		}
	}
	return nil
}

func (r *RoleBasedGroupSetReconciler) deleteRollingGroupSetChild(ctx context.Context, child *workloadsv1alpha2.RoleBasedGroup) error {
	return client.IgnoreNotFound(r.client.Delete(ctx, child,
		client.PropagationPolicy(metav1.DeletePropagationForeground),
		client.Preconditions{UID: &child.UID, ResourceVersion: &child.ResourceVersion}))
}

func (r *RoleBasedGroupSetReconciler) recordGroupSetReplicas(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet, replicas int,
) error {
	value := strconv.Itoa(replicas)
	if set.Annotations[groupSetReplicasAnnotation] == value {
		return nil
	}
	// This checkpoint distinguishes scale-out from replacement below a newly increased partition.
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &workloadsv1alpha2.RoleBasedGroupSet{}
		if err := r.apiReader.Get(ctx, client.ObjectKeyFromObject(set), latest); err != nil {
			return err
		}
		if latest.UID != set.UID || latest.Generation != set.Generation {
			return fmt.Errorf("RoleBasedGroupSet %s changed during reconciliation", set.Name)
		}
		base := latest.DeepCopy()
		if latest.Annotations == nil {
			latest.Annotations = make(map[string]string)
		}
		latest.Annotations[groupSetReplicasAnnotation] = value
		if err := r.client.Patch(ctx, latest, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		set.Annotations = latest.Annotations
		return nil
	})
}

func (r *RoleBasedGroupSetReconciler) groupSetPartitionReady(
	set *workloadsv1alpha2.RoleBasedGroupSet, children *workloadsv1alpha2.RoleBasedGroupList,
	update *groupSetRevision, partition int,
) bool {
	ready := 0
	for i := range children.Items {
		child := &children.Items[i]
		index, valid := groupSetOrdinal(set, child)
		if valid && index >= partition && index < int(*set.Spec.Replicas) &&
			r.groupSetMatchesRevision(child, update) && groupSetChildReady(child) {
			ready++
		}
	}
	return ready == int(*set.Spec.Replicas)-partition
}

func (r *RoleBasedGroupSetReconciler) updateRollingGroupSetStatus(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet, children *workloadsv1alpha2.RoleBasedGroupList,
	current, update *groupSetRevision, limits groupSetRolloutLimits,
) error {
	status := *set.Status.DeepCopy()
	status.ObservedGeneration = set.Generation
	status.CurrentRevision = current.name
	status.UpdateRevision = update.name
	status.Replicas = int32(len(children.Items))
	status.ReadyReplicas = 0
	status.CurrentReplicas = 0
	status.UpdatedReplicas = 0
	status.UpdatedReadyReplicas = 0
	status.ExpectedUpdatedReplicas = *set.Spec.Replicas - int32(limits.partition)
	for i := range children.Items {
		child := &children.Items[i]
		ready := groupSetChildReady(child)
		if ready {
			status.ReadyReplicas++
		}
		index, valid := groupSetOrdinal(set, child)
		if !valid || index >= int(*set.Spec.Replicas) || !child.DeletionTimestamp.IsZero() {
			continue
		}
		if r.groupSetMatchesRevision(child, update) {
			status.UpdatedReplicas++
			if ready {
				status.UpdatedReadyReplicas++
			}
		} else {
			status.CurrentReplicas++
		}
	}
	readyCondition := metav1.Condition{
		Type: string(workloadsv1alpha2.RoleBasedGroupSetReady), ObservedGeneration: set.Generation,
		Status: metav1.ConditionFalse, Reason: "ReplicasNotReady",
		Message: fmt.Sprintf("Waiting for replicas to be ready (%d/%d)", status.ReadyReplicas, *set.Spec.Replicas),
	}
	if status.ReadyReplicas >= *set.Spec.Replicas {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "AllReplicasReady"
		readyCondition.Message = "All RoleBasedGroup replicas are ready."
	}
	meta.SetStatusCondition(&status.Conditions, readyCondition)

	complete := status.UpdatedReplicas == *set.Spec.Replicas && status.ReadyReplicas == *set.Spec.Replicas &&
		status.Replicas == *set.Spec.Replicas
	rolling := metav1.Condition{
		Type: string(workloadsv1alpha2.RoleBasedGroupSetRolling), ObservedGeneration: set.Generation,
		Status: metav1.ConditionTrue, Reason: "RolloutInProgress",
		Message: fmt.Sprintf("Updating RoleBasedGroups to revision %s", update.name),
	}
	previous := meta.FindStatusCondition(set.Status.Conditions, rolling.Type)
	sameTemplate := set.Status.UpdateRevision == update.name
	switch {
	case complete:
		status.CurrentRevision = update.name
		rolling.Status = metav1.ConditionFalse
		rolling.Reason = "RolloutComplete"
		rolling.Message = "All RoleBasedGroups have completed the rollout."
	case sameTemplate && previous != nil && previous.Status == metav1.ConditionFalse &&
		(previous.Reason == "RolloutComplete" ||
			(previous.Reason == "PartitionComplete" && set.Status.ExpectedUpdatedReplicas >= status.ExpectedUpdatedReplicas)):
		// Readiness loss alone must not reopen a completed rollout.
		rolling.Status = previous.Status
		rolling.Reason = previous.Reason
		rolling.Message = previous.Message
	case limits.partition > 0 && status.Replicas == *set.Spec.Replicas &&
		r.groupSetPartitionReady(set, children, update, limits.partition):
		rolling.Status = metav1.ConditionFalse
		rolling.Reason = "PartitionComplete"
		rolling.Message = "All RoleBasedGroups at or above the partition have completed the rollout."
	}
	meta.SetStatusCondition(&status.Conditions, rolling)
	if reflect.DeepEqual(set.Status, status) {
		return nil
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &workloadsv1alpha2.RoleBasedGroupSet{}
		if err := r.apiReader.Get(ctx, client.ObjectKeyFromObject(set), latest); err != nil {
			return err
		}
		if latest.UID != set.UID || latest.Generation != set.Generation {
			return fmt.Errorf("RoleBasedGroupSet %s changed during reconciliation", set.Name)
		}
		latest.Status = status
		return r.client.Status().Update(ctx, latest)
	}); err != nil {
		return err
	}
	set.Status = status
	current.name = status.CurrentRevision
	if current.name == update.name {
		current.template = update.template
	}
	return nil
}

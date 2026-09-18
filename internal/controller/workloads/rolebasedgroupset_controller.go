/*
Copyright 2025 The RBG Authors.

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
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
	utilclient "sigs.k8s.io/rbgs/pkg/utils/client"
)

// RoleBasedGroupSetReconciler reconciles a RoleBasedGroupSet object
type RoleBasedGroupSetReconciler struct {
	client    client.Client
	apiReader client.Reader
	scheme    *runtime.Scheme
	recorder  record.EventRecorder
}

func NewRoleBasedGroupSetReconciler(mgr ctrl.Manager) *RoleBasedGroupSetReconciler {
	return &RoleBasedGroupSetReconciler{
		client:    utilclient.NewClientWithUserAgent(mgr, "rolebasedgroupset"),
		apiReader: mgr.GetAPIReader(),
		scheme:    mgr.GetScheme(),
		recorder:  mgr.GetEventRecorderFor("rbgset-controller"),
	}
}

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupsets/finalizers,verbs=update
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=clusterengineruntimeprofiles,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=clusterengineruntimeprofiles/status,verbs=get;update;patch

// Reconcile is the main reconciliation logic for RoleBasedGroupSet
func (r *RoleBasedGroupSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("rbgset", req.NamespacedName)
	ctx = ctrl.LoggerInto(ctx, logger)
	logger.Info("Start to reconcile rbgset")

	// 1. Fetch the RoleBasedGroupSet instance.
	rbgset := &workloadsv1alpha2.RoleBasedGroupSet{}
	if err := r.client.Get(ctx, req.NamespacedName, rbgset); err != nil {
		// Ignore not-found errors, which can happen after an object has been deleted.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !rbgset.DeletionTimestamp.IsZero() {
		logger.Info("rbgset is deleting, skip reconcile")
		return ctrl.Result{}, nil
	}

	// 2. List all child RoleBasedGroup instances currently associated with this RoleBasedGroupSet.
	var rbglist workloadsv1alpha2.RoleBasedGroupList
	selector, _ := labels.Parse(fmt.Sprintf("%s=%s", constants.GroupSetNameLabelKey, rbgset.Name))
	if err := r.client.List(
		ctx, &rbglist, client.InNamespace(rbgset.Namespace), client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		logger.Error(err, "Failed to list child RoleBasedGroups")
		return ctrl.Result{}, err
	}

	// 3. Propagate the template according to the rollout strategy. Without one, every
	// outdated group is updated in place within this single reconcile; with one, outdated
	// groups are recreated under a paced rolling update.
	replicas := int(*rbgset.Spec.Replicas)
	children := classifyChildren(&rbglist, replicas)

	var err error
	if rbgset.Spec.RolloutStrategy == nil {
		err = r.reconcileStatic(ctx, rbgset, replicas, children)
	} else {
		err = r.reconcileRolling(ctx, rbgset, replicas, children, resolveRollingParams(rbgset))
	}
	if err != nil {
		logger.Error(err, "Failed to reconcile rbgset")
		return ctrl.Result{}, err
	}

	// 4. Update the status after all operations are complete.
	// After scaling, re-list the children to ensure the status is accurate.
	if err := r.client.List(
		ctx, &rbglist, client.InNamespace(rbgset.Namespace), client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		logger.Error(err, "Failed to re-list child RoleBasedGroups for status update")
		return ctrl.Result{}, err
	}
	if err := r.updateStatus(ctx, rbgset, &rbglist); err != nil {
		logger.Error(err, "Failed to update RoleBasedGroupSet status")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled rbgset")
	return ctrl.Result{}, nil
}

// classifiedChildren splits the children of a RoleBasedGroupSet by their ordinal.
// base holds the ordinals in [0, Replicas), surge holds the ordinals at or above Replicas,
// and invalid collects children whose index label is missing or unparseable.
type classifiedChildren struct {
	base    map[int]*workloadsv1alpha2.RoleBasedGroup
	surge   map[int]*workloadsv1alpha2.RoleBasedGroup
	invalid []*workloadsv1alpha2.RoleBasedGroup
}

func classifyChildren(list *workloadsv1alpha2.RoleBasedGroupList, replicas int) classifiedChildren {
	children := classifiedChildren{
		base:  make(map[int]*workloadsv1alpha2.RoleBasedGroup, len(list.Items)),
		surge: make(map[int]*workloadsv1alpha2.RoleBasedGroup),
	}
	for i := range list.Items {
		rbg := &list.Items[i]
		ordinal, ok := parseGroupOrdinal(rbg)
		if !ok {
			children.invalid = append(children.invalid, rbg)
			continue
		}
		if ordinal >= replicas {
			children.surge[ordinal] = rbg
		} else {
			children.base[ordinal] = rbg
		}
	}
	return children
}

func parseGroupOrdinal(rbg *workloadsv1alpha2.RoleBasedGroup) (int, bool) {
	indexStr, ok := rbg.Labels[constants.GroupSetIndexLabelKey]
	if !ok {
		return 0, false
	}
	index, err := strconv.Atoi(indexStr)
	if err != nil || index < 0 {
		return 0, false
	}
	return index, true
}

// ordinalOf returns the parsed index label of a child RoleBasedGroup. Callers only pass
// children that classifyChildren accepted, so the label is present and valid.
func ordinalOf(rbg *workloadsv1alpha2.RoleBasedGroup) int {
	ordinal, _ := parseGroupOrdinal(rbg)
	return ordinal
}

// rollingParams carries the resolved rolling update parameters of a RoleBasedGroupSet.
type rollingParams struct {
	partition      int
	maxUnavailable int
	maxSurge       int
	paused         bool
}

// resolveRollingParams resolves the IntOrString fields of spec.rolloutStrategy
// against spec.replicas. Invalid values fall back to the defaults; admission normally rejects
// them before they reach the controller. A resolved maxUnavailable of 0 is only reachable
// together with a surge budget, where the ready surge groups supply the capacity that lets the
// rollout advance without ever dropping a base group, so it is kept as 0. Flooring to 1 applies
// only when there is no surge budget, because otherwise the rollout could not make progress at all.
func resolveRollingParams(rbgset *workloadsv1alpha2.RoleBasedGroupSet) rollingParams {
	params := rollingParams{maxUnavailable: 1}
	if rbgset.Spec.RolloutStrategy == nil {
		return params
	}
	ru := rbgset.Spec.RolloutStrategy
	replicas := int(*rbgset.Spec.Replicas)

	if ru.MaxSurge != nil {
		if v, err := intstr.GetScaledValueFromIntOrPercent(ru.MaxSurge, replicas, true); err == nil {
			params.maxSurge = v
		}
	}
	if ru.MaxUnavailable != nil {
		if v, err := intstr.GetScaledValueFromIntOrPercent(ru.MaxUnavailable, replicas, params.maxSurge == 0); err == nil {
			params.maxUnavailable = v
		}
	}
	if params.maxSurge == 0 && params.maxUnavailable < 1 {
		params.maxUnavailable = 1
	}
	if ru.Partition != nil {
		if v, err := intstr.GetScaledValueFromIntOrPercent(ru.Partition, replicas, false); err == nil {
			params.partition = v
		}
	}
	if params.partition < 0 {
		params.partition = 0
	}
	if params.partition > replicas {
		params.partition = replicas
	}
	params.paused = ru.Paused
	return params
}

// reconcileStatic keeps the propagation semantics used when spec.rolloutStrategy is unset:
// every outdated RoleBasedGroup is updated in place within a single reconcile, with no
// ordering and no availability gating. Ordinals at or above Replicas are scale-down leftovers.
func (r *RoleBasedGroupSetReconciler) reconcileStatic(
	ctx context.Context, rbgset *workloadsv1alpha2.RoleBasedGroupSet, replicas int, children classifiedChildren,
) error {
	logger := log.FromContext(ctx)

	rbgsToDelete := append([]*workloadsv1alpha2.RoleBasedGroup{}, children.invalid...)
	for _, rbg := range children.surge {
		rbgsToDelete = append(rbgsToDelete, rbg)
	}

	var rbgsToUpdate []*workloadsv1alpha2.RoleBasedGroup
	for _, rbg := range children.base {
		if r.needsUpdate(rbgset, rbg) {
			rbgsToUpdate = append(rbgsToUpdate, rbg)
		}
	}

	var rbgsToCreate []*workloadsv1alpha2.RoleBasedGroup
	for i := 0; i < replicas; i++ {
		if _, exists := children.base[i]; !exists {
			rbgsToCreate = append(rbgsToCreate, newRBGForSet(rbgset, i))
		}
	}

	// Scale down first to avoid updating RoleBasedGroups that are about to be deleted.
	if len(rbgsToDelete) > 0 {
		logger.Info("Scaling down RoleBasedGroups", "count", len(rbgsToDelete))
		if err := r.scaleDown(ctx, rbgsToDelete); err != nil {
			logger.Error(err, "Failed to scale down")
			return err
		}
	}
	if len(rbgsToUpdate) > 0 {
		logger.Info("Updating existing RoleBasedGroups", "count", len(rbgsToUpdate))
		if err := r.updateExistingRBGs(ctx, rbgset, rbgsToUpdate); err != nil {
			logger.Error(err, "Failed to update existing RoleBasedGroups")
			return err
		}
	}
	if len(rbgsToCreate) > 0 {
		logger.Info("Scaling up RoleBasedGroups", "count", len(rbgsToCreate))
		if err := r.scaleUp(ctx, rbgset, rbgsToCreate); err != nil {
			logger.Error(err, "Failed to scale up")
			// Returning an error will trigger a requeue.
			return err
		}
	}
	return nil
}

// reconcileRolling propagates GroupTemplate changes by deleting and recreating outdated
// RoleBasedGroups within an unavailability budget, from the highest ordinal down to Partition.
// A diff limited to role replicas is a scale operation and is applied in place without
// recreation. Ordinals in [Replicas, Replicas+MaxSurge) hold surge groups that provide extra
// capacity while the rollout is in flight.
//
// The children passed in come from the informer cache and are only used as a cheap pre-filter
// and to drive the non-mutating steps. The decision to delete is re-taken from an authoritative
// read in recreateOutdatedGroups, so a reconcile that changes nothing costs no direct API call.
func (r *RoleBasedGroupSetReconciler) reconcileRolling(
	ctx context.Context, rbgset *workloadsv1alpha2.RoleBasedGroupSet, replicas int,
	children classifiedChildren, params rollingParams,
) error {
	logger := log.FromContext(ctx)

	outdated, scaleOnly, metadataDrift := r.classifyRolloutChildren(rbgset, children)
	complete := r.rolloutComplete(rbgset, children, replicas, params.partition)

	// 1. Drop children whose index label is missing or broken.
	if len(children.invalid) > 0 {
		logger.Info("Deleting RoleBasedGroups with an invalid index label", "count", len(children.invalid))
		if err := r.scaleDown(ctx, children.invalid); err != nil {
			logger.Error(err, "Failed to delete invalid RoleBasedGroups")
			return err
		}
	}

	// 2. Reclaim surge groups that exceed the surge budget or outlived the rollout.
	keptSurge, err := r.reclaimSurgeGroups(ctx, replicas, children, params, complete)
	if err != nil {
		return err
	}

	// 3. Fill missing base ordinals. Scaling is never paced by the rollout, and it keeps
	// happening while the rollout is paused.
	if err := r.fillMissingBaseOrdinals(ctx, rbgset, replicas, children); err != nil {
		return err
	}

	// 4. Warm up surge capacity while the rollout is pending.
	if !params.paused && len(outdated) > 0 && len(keptSurge) < params.maxSurge {
		if err := r.warmUpSurgeCapacity(ctx, rbgset, replicas, keptSurge, params); err != nil {
			return err
		}
	}

	// 5. Apply scale-only diffs in place. They are scaling, not rollout, so they are not
	// paced and they still happen while the rollout is paused.
	if len(scaleOnly) > 0 {
		logger.Info("Scaling RoleBasedGroups in place, roles differ only in replicas", "count", len(scaleOnly))
		if err := r.updateExistingRBGs(ctx, rbgset, scaleOnly); err != nil {
			logger.Error(err, "Failed to scale RoleBasedGroups in place")
			return err
		}
	}
	// Template metadata is synced in place as well, but only while the rollout runs: a
	// paused rollout freezes template propagation.
	if !params.paused && len(metadataDrift) > 0 {
		logger.Info("Syncing groupTemplate metadata to RoleBasedGroups", "count", len(metadataDrift))
		if err := r.syncChildrenMetadata(ctx, rbgset, metadataDrift); err != nil {
			logger.Error(err, "Failed to sync RoleBasedGroup metadata")
			return err
		}
	}

	// 6. Recreate outdated groups within the unavailability budget, highest ordinal first.
	if params.paused {
		logger.Info("Rollout is paused, skipping template updates and keeping surge groups")
		return nil
	}
	if len(outdated) == 0 {
		return nil
	}
	return r.recreateOutdatedGroups(ctx, rbgset, replicas, children, outdated, params)
}

// classifyRolloutChildren buckets every base child by how its roles compare to the current
// groupTemplate: up-to-date children may only need template metadata synced, scale-only
// children differ from the template only in role replicas and are scaled in place, and
// outdated children are recreated under the unavailability budget.
func (r *RoleBasedGroupSetReconciler) classifyRolloutChildren(
	rbgset *workloadsv1alpha2.RoleBasedGroupSet, children classifiedChildren,
) (outdated, scaleOnly, metadataDrift []*workloadsv1alpha2.RoleBasedGroup) {
	template := rbgset.Spec.GroupTemplate.Spec.Roles
	for _, rbg := range children.base {
		switch {
		case r.rolesEqual(rbg.Spec.Roles, template):
			if r.needsTemplateLabelUpdate(rbgset, rbg) || r.needsTemplateAnnotationUpdate(rbgset, rbg) {
				metadataDrift = append(metadataDrift, rbg)
			}
		case r.onlyReplicasChanged(rbg.Spec.Roles, template):
			scaleOnly = append(scaleOnly, rbg)
		default:
			outdated = append(outdated, rbg)
		}
	}
	return outdated, scaleOnly, metadataDrift
}

// reclaimSurgeGroups deletes the surge groups that exceed the surge budget or outlived the
// rollout, and returns the ones still serving rollout capacity.
func (r *RoleBasedGroupSetReconciler) reclaimSurgeGroups(
	ctx context.Context, replicas int, children classifiedChildren, params rollingParams, complete bool,
) (map[int]*workloadsv1alpha2.RoleBasedGroup, error) {
	logger := log.FromContext(ctx)
	keptSurge := make(map[int]*workloadsv1alpha2.RoleBasedGroup, len(children.surge))
	var surgeToDelete []*workloadsv1alpha2.RoleBasedGroup
	for ordinal, rbg := range children.surge {
		if ordinal >= replicas+params.maxSurge || complete {
			surgeToDelete = append(surgeToDelete, rbg)
			continue
		}
		if rbg.DeletionTimestamp.IsZero() {
			keptSurge[ordinal] = rbg
		}
	}
	if len(surgeToDelete) == 0 {
		return keptSurge, nil
	}
	logger.Info("Reclaiming surge RoleBasedGroups", "count", len(surgeToDelete))
	if err := r.scaleDown(ctx, surgeToDelete); err != nil {
		logger.Error(err, "Failed to reclaim surge RoleBasedGroups")
		return nil, err
	}
	return keptSurge, nil
}

// fillMissingBaseOrdinals creates the base groups whose ordinals are not currently occupied.
func (r *RoleBasedGroupSetReconciler) fillMissingBaseOrdinals(
	ctx context.Context, rbgset *workloadsv1alpha2.RoleBasedGroupSet, replicas int, children classifiedChildren,
) error {
	logger := log.FromContext(ctx)
	var rbgsToCreate []*workloadsv1alpha2.RoleBasedGroup
	for i := 0; i < replicas; i++ {
		if _, exists := children.base[i]; !exists {
			rbgsToCreate = append(rbgsToCreate, newRBGForSet(rbgset, i))
		}
	}
	if len(rbgsToCreate) == 0 {
		return nil
	}
	logger.Info("Scaling up RoleBasedGroups", "count", len(rbgsToCreate))
	if err := r.scaleUp(ctx, rbgset, rbgsToCreate); err != nil {
		logger.Error(err, "Failed to scale up")
		return err
	}
	return nil
}

// warmUpSurgeCapacity creates the surge groups that are missing within the surge budget
// while a rollout is pending.
func (r *RoleBasedGroupSetReconciler) warmUpSurgeCapacity(
	ctx context.Context, rbgset *workloadsv1alpha2.RoleBasedGroupSet, replicas int,
	keptSurge map[int]*workloadsv1alpha2.RoleBasedGroup, params rollingParams,
) error {
	logger := log.FromContext(ctx)
	var surgeToCreate []*workloadsv1alpha2.RoleBasedGroup
	for ordinal := replicas; ordinal < replicas+params.maxSurge; ordinal++ {
		if _, exists := keptSurge[ordinal]; exists {
			continue
		}
		surgeToCreate = append(surgeToCreate, newRBGForSet(rbgset, ordinal))
	}
	if len(surgeToCreate) == 0 {
		return nil
	}
	logger.Info("Creating surge RoleBasedGroups", "count", len(surgeToCreate))
	if err := r.scaleUp(ctx, rbgset, surgeToCreate); err != nil {
		logger.Error(err, "Failed to create surge RoleBasedGroups")
		return err
	}
	return nil
}

// recreateOutdatedGroups deletes the outdated groups that the unavailability budget allows,
// highest ordinal first, and leaves their recreation to the next reconcile's scale-up step.
//
// The delete uses foreground propagation, which is what makes the pacing hold. A group is
// rebuilt under the same deterministic name, and with the default background propagation the
// RoleBasedGroup object disappears at once while its RoleInstanceSet, RoleInstances and Pods are
// still being garbage collected. The replacement created in that window adopts those leftover
// objects and reports Ready from them, which frees the budget for a second group before the
// first one really exists. Foreground propagation keeps the group visible and terminating until
// the whole chain is gone, so it stays counted as unavailable and its ordinal is not refilled
// early.
//
// The classification this reconcile already made is passed in rather than recomputed. What
// makes acting on a possibly stale snapshot safe is the UID precondition on every delete:
// Delete locates its target by name alone, so without it a snapshot that lagged across a whole
// delete-recreate-ready cycle would delete the replacement instead of the group it reasoned
// about. A mismatch comes back as a conflict and is dropped, because the deletion in flight
// already re-queues this set and the next pass decides with the replacement in view.
//
// Descending ordinal order is what makes a lagging snapshot harmless even without that: the
// group whose deletion has not been observed yet is the highest ordinal, so it is the first
// candidate and the budget is spent re-deleting it, which is a no-op, instead of moving on to a
// lower ordinal that is still serving.
func (r *RoleBasedGroupSetReconciler) recreateOutdatedGroups(
	ctx context.Context, rbgset *workloadsv1alpha2.RoleBasedGroupSet, replicas int,
	children classifiedChildren, outdated []*workloadsv1alpha2.RoleBasedGroup, params rollingParams,
) error {
	logger := log.FromContext(ctx)

	// Missing base ordinals are in-flight capacity just like not-ready groups: a group deleted
	// in an earlier reconcile and not yet recreated is capacity that is not there.
	unavailableBase := replicas - len(children.base)
	for _, rbg := range children.base {
		if !r.isServing(rbg) {
			unavailableBase++
		}
	}

	// Ready surge groups widen the budget because they serve capacity while a base group is
	// being rebuilt.
	readySurge := 0
	for _, rbg := range children.surge {
		if r.isServing(rbg) {
			readySurge++
		}
	}

	sort.Slice(outdated, func(i, j int) bool { return ordinalOf(outdated[i]) > ordinalOf(outdated[j]) })
	budget := params.maxUnavailable + readySurge
	for _, rbg := range outdated {
		if ordinalOf(rbg) < params.partition {
			// Held back on the previous template; the remaining candidates have even
			// lower ordinals.
			break
		}
		if !rbg.DeletionTimestamp.IsZero() {
			// Already on its way out and already counted as unavailable.
			continue
		}
		serving := r.isServing(rbg)
		if serving && unavailableBase >= budget {
			logger.Info(
				"Unavailability budget exhausted, waiting before recreating more RoleBasedGroups",
				"unavailable", unavailableBase, "budget", budget,
			)
			break
		}
		logger.Info("Recreating outdated RoleBasedGroup", "name", rbg.Name)
		r.recorder.Eventf(rbgset, corev1.EventTypeNormal, "RecreatingGroup",
			"Recreating outdated RoleBasedGroup %s to match the current groupTemplate", rbg.Name)
		uid := rbg.UID
		if err := r.client.Delete(
			ctx, rbg,
			client.PropagationPolicy(metav1.DeletePropagationForeground),
			client.Preconditions{UID: &uid},
		); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return fmt.Errorf("failed to delete RoleBasedGroup %s: %w", rbg.Name, err)
		}
		if serving {
			unavailableBase++
		}
	}
	return nil
}

// rolloutComplete reports whether the rollout has finished: every base ordinal exists and is
// serving, and every in-scope ordinal (at or above the partition) is on the current template.
// Ordinals below the partition are held back on the previous template by design, so they must
// not count toward completeness; serving is still required of every base group so surge capacity
// is not withdrawn while any base group is down. Readiness is part of the test because a group
// that was just recreated matches the template at once but is not serving yet, and withdrawing
// the surge groups at that moment would leave fewer than replicas-maxUnavailable groups ready
// for as long as the new one takes to come up.
func (r *RoleBasedGroupSetReconciler) rolloutComplete(
	rbgset *workloadsv1alpha2.RoleBasedGroupSet, children classifiedChildren, replicas, partition int,
) bool {
	if len(children.base) != replicas {
		return false
	}
	template := rbgset.Spec.GroupTemplate.Spec.Roles
	for ordinal, rbg := range children.base {
		if !r.isServing(rbg) {
			return false
		}
		if ordinal >= partition && !r.rolesEqual(rbg.Spec.Roles, template) {
			return false
		}
	}
	return true
}

// isReady reports whether a child RoleBasedGroup has a true Ready condition.
func (r *RoleBasedGroupSetReconciler) isReady(rbg *workloadsv1alpha2.RoleBasedGroup) bool {
	return meta.IsStatusConditionTrue(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupReady))
}

// isServing reports whether a child RoleBasedGroup actually serves traffic. A group that is
// being deleted keeps its Ready condition true while its Pods work through their preStop hooks
// and the termination grace period, so counting it as ready overstates availability for that
// whole window. Every readiness decision in this controller goes through here, which keeps the
// unavailability budget, the surge lifetime and the reported status from drifting apart.
func (r *RoleBasedGroupSetReconciler) isServing(rbg *workloadsv1alpha2.RoleBasedGroup) bool {
	return r.isReady(rbg) && rbg.DeletionTimestamp.IsZero()
}

// onlyReplicasChanged reports whether child roles differ from the template only in role
// replicas: the same set of role names and identical fields except Replicas. Such a diff is
// a scale operation and is applied in place instead of recreating the group.
func (r *RoleBasedGroupSetReconciler) onlyReplicasChanged(
	child, template []workloadsv1alpha2.RoleSpec,
) bool {
	if len(child) != len(template) {
		return false
	}
	byName := make(map[string]*workloadsv1alpha2.RoleSpec, len(child))
	for i := range child {
		byName[child[i].Name] = &child[i]
	}
	for i := range template {
		c, ok := byName[template[i].Name]
		if !ok {
			return false
		}
		// Compare with Replicas masked out; sharing the pointer makes the field equal.
		cc := *c
		cc.Replicas = template[i].Replicas
		if !reflect.DeepEqual(cc, template[i]) {
			return false
		}
	}
	return true
}

// scaleUp concurrently creates a given set of RoleBasedGroup instances.
func (r *RoleBasedGroupSetReconciler) scaleUp(
	ctx context.Context, rbgset *workloadsv1alpha2.RoleBasedGroupSet, rbgsToCreate []*workloadsv1alpha2.RoleBasedGroup,
) error {
	logger := log.FromContext(ctx)
	// TODO: we need to enhance it by following the way:
	// https://github.com/openkruise/kruise/blob/master/pkg/controller/statefulset/stateful_set_control.go#L478
	allErrs := make([]error, 0, len(rbgsToCreate))
	for _, rbg := range rbgsToCreate {
		// Set the owner reference.
		if err := controllerutil.SetControllerReference(rbgset, rbg, r.scheme); err != nil {
			allErrs = append(allErrs, fmt.Errorf("failed to set controller reference for rbg %s: %w", rbg.Name, err))
			continue
		}

		// Already created not need to continue
		got := &workloadsv1alpha2.RoleBasedGroup{}
		if err := r.client.Get(
			ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, got,
		); err == nil {
			continue
		}

		if err := r.client.Create(ctx, rbg); err != nil {
			// If it already exists, ignore the error. This ensures idempotency,
			// e.g., if the previous reconcile was interrupted after a successful creation.
			if !apierrors.IsAlreadyExists(err) {
				allErrs = append(allErrs, fmt.Errorf("failed to create RoleBasedGroup %s: %w", rbg.Name, err))
			} else {
				logger.V(1).Info("RoleBasedGroup has been created", "name", rbg.Name)
			}
		} else {
			logger.Info("Successfully created RoleBasedGroup", "name", rbg.Name)
		}

	}

	// Aggregate all errors.
	return utilerrors.NewAggregate(allErrs)
}

// scaleDown deletes a given set of RoleBasedGroup instances. Every delete carries the UID of the
// object the caller classified, for the same reason recreateOutdatedGroups does: an ordinal
// dropped here is refilled by name in a later step, so a stale snapshot must not delete whatever
// took its place in the meantime.
func (r *RoleBasedGroupSetReconciler) scaleDown(
	ctx context.Context, rbgsToDelete []*workloadsv1alpha2.RoleBasedGroup,
) error {
	logger := log.FromContext(ctx)
	allErrs := make([]error, 0, len(rbgsToDelete))
	for _, rbg := range rbgsToDelete {
		uid := rbg.UID
		err := r.client.Delete(ctx, rbg, client.Preconditions{UID: &uid})
		switch {
		case err == nil:
			logger.Info("Successfully deleted RoleBasedGroup", "name", rbg.Name)
		case apierrors.IsNotFound(err), apierrors.IsConflict(err):
			// Already gone, or already replaced by an object this reconcile had not seen.
			// Either way there is nothing left to delete, and keeping this a success is
			// what makes the whole step idempotent.
		default:
			allErrs = append(allErrs, fmt.Errorf("failed to delete RoleBasedGroup %s: %w", rbg.Name, err))
		}
	}

	return utilerrors.NewAggregate(allErrs)
}

// updateStatus updates the status of the RoleBasedGroupSet. The replica counters count only
// the base children, the ones at ordinals below spec.replicas; surge groups are transient
// rollout capacity and stay out of the counters so they remain comparable to spec.replicas.
func (r *RoleBasedGroupSetReconciler) updateStatus(
	ctx context.Context, rbgset *workloadsv1alpha2.RoleBasedGroupSet, rbglist *workloadsv1alpha2.RoleBasedGroupList,
) error {
	logger := log.FromContext(ctx)

	replicas := int(*rbgset.Spec.Replicas)
	template := rbgset.Spec.GroupTemplate.Spec.Roles

	// Only ordinals at or above the partition take part in the rollout, so only they count as
	// still on the previous template. Ordinals held back below the partition keep the old
	// template by design and must not keep the rollout from reporting complete.
	partition := 0
	if rbgset.Spec.RolloutStrategy != nil {
		partition = resolveRollingParams(rbgset).partition
	}

	var baseTotal, readyBase, current, updated, updatedReady int32
	for i := range rbglist.Items {
		rbg := &rbglist.Items[i]
		ordinal, ok := parseGroupOrdinal(rbg)
		if !ok || ordinal >= replicas {
			continue
		}
		baseTotal++
		ready := r.isServing(rbg)
		if ready {
			readyBase++
		}
		if r.rolesEqual(rbg.Spec.Roles, template) {
			updated++
			if ready {
				updatedReady++
			}
		} else if ordinal >= partition {
			current++
		}
	}

	newStatus := *rbgset.Status.DeepCopy()
	newStatus.ObservedGeneration = rbgset.Generation
	newStatus.Replicas = baseTotal
	newStatus.ReadyReplicas = readyBase
	newStatus.CurrentReplicas = current
	newStatus.UpdatedReplicas = updated
	newStatus.UpdatedReadyReplicas = updatedReady

	if readyBase >= int32(replicas) {
		meta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
			Type:    string(workloadsv1alpha2.RoleBasedGroupSetReady),
			Status:  metav1.ConditionTrue,
			Reason:  "AllReplicasReady",
			Message: "All RoleBasedGroup replicas are ready.",
		})
	} else {
		meta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
			Type:   string(workloadsv1alpha2.RoleBasedGroupSetReady),
			Status: metav1.ConditionFalse,
			Reason: "ReplicasNotReady",
			Message: fmt.Sprintf(
				"Waiting for replicas to be ready (%d/%d)", readyBase, replicas,
			),
		})
	}

	if rbgset.Spec.RolloutStrategy == nil {
		newStatus.ExpectedUpdatedReplicas = 0
		meta.RemoveStatusCondition(&newStatus.Conditions, string(workloadsv1alpha2.RoleBasedGroupSetRolling))
		meta.RemoveStatusCondition(&newStatus.Conditions, string(workloadsv1alpha2.RoleBasedGroupSetPaused))
	} else {
		params := resolveRollingParams(rbgset)
		newStatus.ExpectedUpdatedReplicas = int32(replicas - params.partition)

		// A rollout counts as complete only once every base ordinal is on the current
		// template and serving. A group that was just recreated matches the template at
		// once, so testing template equality alone would report RolloutComplete while that
		// group is still coming up, contradicting the Ready condition right next to it.
		switch {
		case current > 0:
			meta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
				Type:    string(workloadsv1alpha2.RoleBasedGroupSetRolling),
				Status:  metav1.ConditionTrue,
				Reason:  "RolloutInProgress",
				Message: fmt.Sprintf("%d RoleBasedGroup(s) are still on the previous template", current),
			})
		case baseTotal < int32(replicas) || updatedReady < updated:
			meta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
				Type:   string(workloadsv1alpha2.RoleBasedGroupSetRolling),
				Status: metav1.ConditionTrue,
				Reason: "RolloutInProgress",
				Message: fmt.Sprintf(
					"Waiting for the RoleBasedGroups on the current groupTemplate to become ready (%d/%d)",
					updatedReady, replicas,
				),
			})
		default:
			meta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
				Type:    string(workloadsv1alpha2.RoleBasedGroupSetRolling),
				Status:  metav1.ConditionFalse,
				Reason:  "RolloutComplete",
				Message: "All RoleBasedGroups match the current groupTemplate and are ready",
			})
		}

		if params.paused && current > 0 {
			meta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
				Type:    string(workloadsv1alpha2.RoleBasedGroupSetPaused),
				Status:  metav1.ConditionTrue,
				Reason:  "RolloutPaused",
				Message: "The rollout is paused by spec.rolloutStrategy.paused",
			})
		} else {
			meta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
				Type:    string(workloadsv1alpha2.RoleBasedGroupSetPaused),
				Status:  metav1.ConditionFalse,
				Reason:  "RolloutNotPaused",
				Message: "The rollout is not paused",
			})
		}
	}

	// Only update the status if it has changed to avoid unnecessary API calls.
	if reflect.DeepEqual(rbgset.Status, newStatus) {
		return nil
	}

	// Use RetryOnConflict to handle potential conflicts during status updates.
	return retry.RetryOnConflict(
		retry.DefaultRetry, func() error {
			// On each retry, get the latest version of the rbgset object.
			latestRBGSet := &workloadsv1alpha2.RoleBasedGroupSet{}
			if err := r.client.Get(
				ctx, types.NamespacedName{Name: rbgset.Name, Namespace: rbgset.Namespace}, latestRBGSet,
			); err != nil {
				return err
			}

			// Apply the status changes to the latest object.
			latestRBGSet.Status = newStatus

			err := r.client.Status().Update(ctx, latestRBGSet)
			if err == nil {
				logger.Info(
					"Successfully updated RoleBasedGroupSet status",
					"replicas", newStatus.Replicas, "readyReplicas", newStatus.ReadyReplicas,
					"updatedReplicas", newStatus.UpdatedReplicas,
				)
			}
			return err
		},
	)
}

// rolesEqual compares two role slices by sorting them by name first.
func (r *RoleBasedGroupSetReconciler) rolesEqual(
	roles1, roles2 []workloadsv1alpha2.RoleSpec,
) bool {
	if len(roles1) != len(roles2) {
		return false
	}

	// Deep copies keep the caller's roles untouched: `copy` alone would still share
	// the *RolloutStrategy/*RollingUpdate pointers, and normalizing through them would
	// mutate the cached RoleBasedGroup/RoleBasedGroupSet objects being compared.
	sortedRoles1 := deepCopyRoles(roles1)
	sortedRoles2 := deepCopyRoles(roles2)

	// Sort both slices by role name
	sort.Slice(
		sortedRoles1, func(i, j int) bool {
			return sortedRoles1[i].Name < sortedRoles1[j].Name
		},
	)
	sort.Slice(
		sortedRoles2, func(i, j int) bool {
			return sortedRoles2[i].Name < sortedRoles2[j].Name
		},
	)

	// Normalize legacy update-strategy type values before comparing. The RBGS
	// defaulter only runs on RoleBasedGroupSet spec writes, so a stored template can
	// keep the v1alpha1 "Recreate" spelling while children carry the
	// webhook-normalized "RecreatePod"; comparing the normalized forms keeps such a
	// spelling-only delta from being read as a real change, which would re-issue
	// child updates forever.
	normalizeRolloutUpdateTypes(sortedRoles1)
	normalizeRolloutUpdateTypes(sortedRoles2)

	// Compare the sorted slices
	return reflect.DeepEqual(sortedRoles1, sortedRoles2)
}

// deepCopyRoles returns a deep copy of roles so callers can sort, normalize and
// compare without mutating the source RoleSpecs (which share *RolloutStrategy /
// *RollingUpdate pointers with the informer-cache objects they came from).
func deepCopyRoles(roles []workloadsv1alpha2.RoleSpec) []workloadsv1alpha2.RoleSpec {
	out := make([]workloadsv1alpha2.RoleSpec, len(roles))
	for i := range roles {
		roles[i].DeepCopyInto(&out[i])
	}
	return out
}

// normalizeRolloutUpdateTypes rewrites legacy and empty update-strategy type
// values on each role in place. Callers must pass a slice they own (rolesEqual and
// normalizedGroupTemplateRoles both operate on deep copies), so no caller's input
// object is mutated.
func normalizeRolloutUpdateTypes(roles []workloadsv1alpha2.RoleSpec) {
	for i := range roles {
		ru := roles[i].RolloutStrategy
		if ru == nil || ru.RollingUpdate == nil {
			continue
		}
		roles[i].RolloutStrategy.RollingUpdate.Type = workloadsv1alpha2.NormalizeUpdateStrategyType(
			ru.RollingUpdate.Type,
		)
	}
}

// normalizedGroupTemplateRoles returns a deep copy of the set's GroupTemplate roles
// with legacy update-strategy type values normalized. Children must be created and
// updated from this form: the RBGS defaulter only fires on RoleBasedGroupSet spec
// writes, so a legacy value stored before the enum was introduced is never healed on
// the parent itself, and copying it verbatim into a child would fail CRD enum
// validation on clusters with webhooks disabled, or leave the child's normalized
// value diverging from the parent forever where webhooks are enabled.
func normalizedGroupTemplateRoles(rbgset *workloadsv1alpha2.RoleBasedGroupSet) []workloadsv1alpha2.RoleSpec {
	roles := deepCopyRoles(rbgset.Spec.GroupTemplate.Spec.Roles)
	normalizeRolloutUpdateTypes(roles)
	return roles
}

// needsUpdate checks if a child RBG needs to be updated based on changes in the parent RBGSet.
func (r *RoleBasedGroupSetReconciler) needsUpdate(
	rbgset *workloadsv1alpha2.RoleBasedGroupSet, rbg *workloadsv1alpha2.RoleBasedGroup,
) bool {
	// Check if the template spec has changed using order-insensitive comparison
	if !r.rolesEqual(rbg.Spec.Roles, rbgset.Spec.GroupTemplate.Spec.Roles) {
		return true
	}

	// Check if labels from the template need to be propagated
	if r.needsTemplateLabelUpdate(rbgset, rbg) {
		return true
	}

	// Check if annotations from the template need to be propagated
	return r.needsTemplateAnnotationUpdate(rbgset, rbg)
}

// needsTemplateLabelUpdate checks if the RBG labels need to be updated to match Template.Labels.
// Keys under the project prefix are owned by the controllers, not by the user, so they never
// count as drift.
func (r *RoleBasedGroupSetReconciler) needsTemplateLabelUpdate(
	rbgset *workloadsv1alpha2.RoleBasedGroupSet, rbg *workloadsv1alpha2.RoleBasedGroup,
) bool {
	templateLabels := rbgset.Spec.GroupTemplate.Labels
	for k, v := range templateLabels {
		if rbg.Labels[k] != v {
			return true
		}
	}
	// Check if any template label was removed from the template but still exists on RBG
	for k := range rbg.Labels {
		if isSystemManagedMetadataKey(k) {
			continue
		}
		if _, exists := templateLabels[k]; !exists {
			return true
		}
	}
	return false
}

// needsTemplateAnnotationUpdate checks if the RBG annotations need to be updated to match
// Template.Annotations. Keys under the project prefix are owned by the controllers, so they
// never count as drift: the RoleBasedGroup controller records discovery-config-mode on its own
// object, and treating that as drift makes the two controllers revert each other forever.
func (r *RoleBasedGroupSetReconciler) needsTemplateAnnotationUpdate(
	rbgset *workloadsv1alpha2.RoleBasedGroupSet, rbg *workloadsv1alpha2.RoleBasedGroup,
) bool {
	templateAnnotations := rbgset.Spec.GroupTemplate.Annotations
	for k, v := range templateAnnotations {
		if rbg.Annotations[k] != v {
			return true
		}
	}
	// Check if any template annotation was removed from the template but still exists on RBG
	for k := range rbg.Annotations {
		if isSystemManagedMetadataKey(k) {
			continue
		}
		if _, exists := templateAnnotations[k]; !exists {
			return true
		}
	}
	return false
}

// isSystemManagedMetadataKey reports whether a label or annotation key belongs to this project's
// controllers rather than to the user. Template syncing carries those keys over from the child
// instead of rebuilding them from the template, which is what keeps two controllers from
// fighting over the same object.
func isSystemManagedMetadataKey(key string) bool {
	return strings.HasPrefix(key, constants.RBGPrefix)
}

// updateExistingRBGs updates existing RoleBasedGroup instances to match the current template.
func (r *RoleBasedGroupSetReconciler) updateExistingRBGs(
	ctx context.Context, rbgset *workloadsv1alpha2.RoleBasedGroupSet, rbgsToUpdate []*workloadsv1alpha2.RoleBasedGroup,
) error {
	logger := log.FromContext(ctx)
	allErrs := make([]error, 0, len(rbgsToUpdate))

	for _, rbg := range rbgsToUpdate {

		// Use retry mechanism to handle potential conflicts
		err := retry.RetryOnConflict(
			retry.DefaultRetry, func() error {
				// Get the latest version of the RBG
				latestRBG := &workloadsv1alpha2.RoleBasedGroup{}
				if err := r.client.Get(
					ctx, types.NamespacedName{
						Name:      rbg.Name,
						Namespace: rbg.Namespace,
					}, latestRBG,
				); err != nil {
					return err
				}

				// Update the spec from template, normalizing legacy update-strategy type
				// values so a pre-webhook legacy template cannot poison the child.
				latestRBG.Spec.Roles = normalizedGroupTemplateRoles(rbgset)

				// Sync labels and annotations from the template
				r.syncRBGMetadata(rbgset, latestRBG)

				// Perform the update
				return r.client.Update(ctx, latestRBG)
			},
		)

		if err != nil {
			allErrs = append(allErrs,
				fmt.Errorf("failed to update RoleBasedGroup %s: %w", rbg.Name, err))
		} else {
			logger.Info("Successfully updated RoleBasedGroup", "name", rbg.Name)
		}

	}

	// Aggregate all concurrent errors
	return utilerrors.NewAggregate(allErrs)
}

// syncChildrenMetadata re-applies the GroupTemplate labels and annotations to children whose
// spec already matches the template, without touching their spec.
func (r *RoleBasedGroupSetReconciler) syncChildrenMetadata(
	ctx context.Context, rbgset *workloadsv1alpha2.RoleBasedGroupSet, rbgs []*workloadsv1alpha2.RoleBasedGroup,
) error {
	logger := log.FromContext(ctx)
	allErrs := make([]error, 0, len(rbgs))

	for _, rbg := range rbgs {
		err := retry.RetryOnConflict(
			retry.DefaultRetry, func() error {
				latestRBG := &workloadsv1alpha2.RoleBasedGroup{}
				if err := r.client.Get(
					ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, latestRBG,
				); err != nil {
					return err
				}

				r.syncRBGMetadata(rbgset, latestRBG)

				return r.client.Update(ctx, latestRBG)
			},
		)

		if err != nil {
			allErrs = append(allErrs,
				fmt.Errorf("failed to sync metadata of RoleBasedGroup %s: %w", rbg.Name, err))
		} else {
			logger.Info("Successfully synced RoleBasedGroup metadata", "name", rbg.Name)
		}
	}

	return utilerrors.NewAggregate(allErrs)
}

// syncRBGMetadata syncs the labels and annotations from Template to the child RBG.
//
// Keys under the project prefix are carried over from the child rather than rebuilt from the
// template, because they belong to the controllers. Replacing the whole map deletes what the
// RoleBasedGroup controller recorded there, which makes it write it again, which makes this
// controller see drift again, and the two never settle.
func (r *RoleBasedGroupSetReconciler) syncRBGMetadata(
	rbgset *workloadsv1alpha2.RoleBasedGroupSet, rbg *workloadsv1alpha2.RoleBasedGroup,
) {
	newLabels := make(map[string]string, len(rbg.Labels)+len(rbgset.Spec.GroupTemplate.Labels))
	for k, v := range rbg.Labels {
		if isSystemManagedMetadataKey(k) {
			newLabels[k] = v
		}
	}
	// Template labels come next and the identity labels last, so a template can never override
	// which set a child belongs to or which ordinal it holds.
	for k, v := range rbgset.Spec.GroupTemplate.Labels {
		newLabels[k] = v
	}
	newLabels[constants.GroupSetNameLabelKey] = rbgset.Name
	newLabels[constants.GroupSetIndexLabelKey] = rbg.Labels[constants.GroupSetIndexLabelKey]
	rbg.Labels = newLabels

	newAnnotations := make(map[string]string, len(rbg.Annotations)+len(rbgset.Spec.GroupTemplate.Annotations))
	for k, v := range rbg.Annotations {
		if isSystemManagedMetadataKey(k) {
			newAnnotations[k] = v
		}
	}
	for k, v := range rbgset.Spec.GroupTemplate.Annotations {
		newAnnotations[k] = v
	}
	if len(newAnnotations) == 0 {
		rbg.Annotations = nil
	} else {
		rbg.Annotations = newAnnotations
	}
}

// newRBGForSet creates a new RoleBasedGroup object based on the set's template.
func newRBGForSet(rbgset *workloadsv1alpha2.RoleBasedGroupSet, index int) *workloadsv1alpha2.RoleBasedGroup {
	// Merge template labels first, then overwrite with system-managed labels to ensure
	// system labels cannot be overridden by template labels.
	rbgLabels := make(map[string]string, len(rbgset.Spec.GroupTemplate.Labels)+2)
	for k, v := range rbgset.Spec.GroupTemplate.Labels {
		rbgLabels[k] = v
	}
	rbgLabels[constants.GroupSetNameLabelKey] = rbgset.Name
	rbgLabels[constants.GroupSetIndexLabelKey] = fmt.Sprintf("%d", index)

	// Copy annotations from the template.
	var rbgAnnotations map[string]string
	if len(rbgset.Spec.GroupTemplate.Annotations) > 0 {
		rbgAnnotations = make(map[string]string, len(rbgset.Spec.GroupTemplate.Annotations))
		for k, v := range rbgset.Spec.GroupTemplate.Annotations {
			rbgAnnotations[k] = v
		}
	}

	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   rbgset.Namespace,
			Name:        fmt.Sprintf("%s-%d", rbgset.Name, index),
			Labels:      rbgLabels,
			Annotations: rbgAnnotations,
			// The OwnerReference will be set in the scaleUp function.
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			// Normalize legacy update-strategy type values the same way updateExistingRBGs
			// does, so a fresh child never carries a value the CRD enum rejects.
			Roles: normalizedGroupTemplateRoles(rbgset),
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *RoleBasedGroupSetReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&workloadsv1alpha2.RoleBasedGroupSet{}).
		Owns(&workloadsv1alpha2.RoleBasedGroup{}).
		Named("rbgset-controller").
		Complete(r)
}

// CheckCrdExists checks if the specified Custom Resource Definition (CRD) exists in the Kubernetes cluster.
func (r *RoleBasedGroupSetReconciler) CheckCrdExists() error {
	return utils.CheckCrdExists(r.apiReader, "rolebasedgroupsets.workloads.x-k8s.io")
}

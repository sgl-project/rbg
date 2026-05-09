/*
Copyright 2026 The RBG Authors.
Copyright 2019 The Kruise Authors.
Copyright 2016 The Kubernetes Authors.

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
	"math"
	"sort"
	"time"

	apps "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/history"
	"k8s.io/utils/ptr"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instanceinplace "sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
	instancelifecycle "sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/utils"
	historyutil "sigs.k8s.io/rbgs/pkg/utils/history"
)

const (
	// MaxMinReadySeconds is the max value of MinReadySeconds
	MaxMinReadySeconds = 300

	// Realistic value for maximum in-flight requests when processing in parallel mode.
	MaxBatchSize = 500

	// stableUnhealthyDuration is the minimum CONSECUTIVE time an instance must
	// be observed unhealthy before progressUpdate is allowed to treat it as
	// "free" (cleanup-unhealthy semantics — i.e. delete it without consuming
	// the maxUnavailable budget).
	//
	// Without this gate, a transient cache view that briefly reports a ready
	// base instance as not-ready could let progressUpdate wrongly free-delete
	// it. Combined with surge that has just become available, this can
	// cascade into wiping out healthy ready replicas mid-rollout.
	//
	// The gate is enforced by tracking, per RoleInstance UID, the first time
	// THIS controller observed the instance as unhealthy (in
	// instanceUnhealthySince). The timer resets whenever the same UID is
	// subsequently observed as healthy — so flapping cache (Ready toggling on
	// the order of milliseconds) can never accumulate enough continuous time
	// to satisfy the gate. A genuinely broken instance is observed unhealthy
	// continuously and becomes eligible after this window.
	//
	// 10s is well above the typical informer cache lag (sub-second) while
	// keeping the recovery delay for genuinely broken pods small enough to
	// avoid wasting GPU resources for too long.
	stableUnhealthyDuration = 10 * time.Second
)

// observeInstanceHealth maintains the instanceUnhealthySince map: for each
// instance, record the first observation time when unhealthy, and clear the
// entry when observed healthy. Called once at the top of every reconcile in
// Phase A so the timestamps reflect the current cache snapshot.
//
// Also opportunistically removes entries for UIDs that no longer appear in
// the supplied instances slice, preventing the map from growing unbounded as
// instances are recreated (each delete-and-recreate produces a new UID).
func observeInstanceHealth(instances []*workloadsv1alpha2.RoleInstance) {
	now := time.Now()
	live := make(map[types.UID]struct{}, len(instances))
	for _, inst := range instances {
		if inst == nil || inst.UID == "" {
			continue
		}
		live[inst.UID] = struct{}{}
		if isHealthy(inst) {
			instanceUnhealthySince.Delete(inst.UID)
			continue
		}
		// Unhealthy: record first-observed time if not already recorded.
		instanceUnhealthySince.LoadOrStore(inst.UID, now)
	}
	// Drop entries for instances that have disappeared (different UID after
	// recreate, or fully deleted).
	instanceUnhealthySince.Range(func(key, _ interface{}) bool {
		uid, _ := key.(types.UID)
		if _, alive := live[uid]; !alive {
			instanceUnhealthySince.Delete(uid)
		}
		return true
	})
}

// isStablyUnhealthy returns true when the controller has observed `inst` as
// unhealthy for at least stableUnhealthyDuration of CONSECUTIVE time. This is
// the only signal progressUpdate uses to decide whether a base instance is
// "free" to update without consuming maxUnavailable budget. See
// stableUnhealthyDuration for the rationale.
func isStablyUnhealthy(inst *workloadsv1alpha2.RoleInstance) bool {
	if inst == nil || inst.UID == "" {
		return false
	}
	v, ok := instanceUnhealthySince.Load(inst.UID)
	if !ok {
		return false
	}
	first, ok := v.(time.Time)
	if !ok {
		return false
	}
	return time.Since(first) >= stableUnhealthyDuration
}

// StatefulInstanceSetControlInterface implements the control logic for updating InstanceSets managing Instances
type StatefulInstanceSetControlInterface interface {
	// UpdateStatefulInstanceSet implements the control logic for Instance creation, update, and deletion
	UpdateStatefulInstanceSet(ctx context.Context, set *workloadsv1alpha2.RoleInstanceSet, instances []*workloadsv1alpha2.RoleInstance) error
	// ListRevisions returns a array of the ControllerRevisions that represent the revisions of set.
	ListRevisions(set *workloadsv1alpha2.RoleInstanceSet) ([]*apps.ControllerRevision, error)
	// AdoptOrphanRevisions adopts any orphaned ControllerRevisions that match set's Selector.
	AdoptOrphanRevisions(set *workloadsv1alpha2.RoleInstanceSet, revisions []*apps.ControllerRevision) error
}

// NewDefaultStatefulInstanceSetControl returns a new instance of the default implementation
func NewDefaultStatefulInstanceSetControl(
	instanceControl *StatefulInstanceControl,
	inplaceControl instanceinplace.Interface,
	lifecycleControl instancelifecycle.Interface,
	statusUpdater StatusUpdaterInterface,
	controllerHistory history.Interface,
	recorder record.EventRecorder) StatefulInstanceSetControlInterface {
	return &defaultStatefulInstanceSetControl{
		instanceControl,
		statusUpdater,
		controllerHistory,
		recorder,
		inplaceControl,
		lifecycleControl,
	}
}

// defaultStatefulInstanceSetControl implements ControlInterface
var _ StatefulInstanceSetControlInterface = &defaultStatefulInstanceSetControl{}

type defaultStatefulInstanceSetControl struct {
	instanceControl   *StatefulInstanceControl
	statusUpdater     StatusUpdaterInterface
	controllerHistory history.Interface
	recorder          record.EventRecorder
	inplaceControl    instanceinplace.Interface
	lifecycleControl  instancelifecycle.Interface
}

// UpdateStatefulInstanceSet executes the core logic loop for a stateful set managing instances
func (ssc *defaultStatefulInstanceSetControl) UpdateStatefulInstanceSet(ctx context.Context, set *workloadsv1alpha2.RoleInstanceSet, instances []*workloadsv1alpha2.RoleInstance) error {
	set = set.DeepCopy()

	// list all revisions and sort them
	revisions, err := ssc.ListRevisions(set)
	if err != nil {
		return err
	}
	history.SortControllerRevisions(revisions)

	currentRevision, updateRevision, err := ssc.performUpdate(ctx, set, instances, revisions)
	if err != nil {
		return utilerrors.NewAggregate([]error{err, ssc.truncateHistory(set, instances, revisions, currentRevision, updateRevision)})
	}

	// maintain the set's revision history limit
	return ssc.truncateHistory(set, instances, revisions, currentRevision, updateRevision)
}

func (ssc *defaultStatefulInstanceSetControl) performUpdate(
	ctx context.Context, set *workloadsv1alpha2.RoleInstanceSet, instances []*workloadsv1alpha2.RoleInstance, revisions []*apps.ControllerRevision) (*apps.ControllerRevision, *apps.ControllerRevision, error) {
	var currentStatus *workloadsv1alpha2.RoleInstanceSetStatus
	// get the current, and update revisions
	currentRevision, updateRevision, collisionCount, err := ssc.getInstanceSetRevisions(set, revisions)
	if err != nil {
		return currentRevision, updateRevision, err
	}

	// Refresh update expectations
	for _, instance := range instances {
		updateExpectations.ObserveUpdated(getInstanceSetKey(set), updateRevision.Name, instance)
	}

	// perform the main update function and get the status
	currentStatus, getStatusErr := ssc.updateStatefulInstanceSet(ctx, set, currentRevision, updateRevision, collisionCount, instances, revisions)
	if getStatusErr != nil && currentStatus == nil {
		return currentRevision, updateRevision, getStatusErr
	}

	// make sure to update the latest status even if there is an error with non-nil currentStatus
	updateStatusErr := ssc.updateInstanceSetStatus(ctx, set, currentStatus)
	if updateStatusErr == nil {
		klog.V(4).InfoS("Updated status", "instanceSet", klog.KObj(set),
			"replicas", currentStatus.Replicas,
			"readyReplicas", currentStatus.ReadyReplicas,
			"updatedReplicas", currentStatus.UpdatedReplicas)
	}

	switch {
	case getStatusErr != nil && updateStatusErr != nil:
		klog.ErrorS(updateStatusErr, "Could not update status", "instanceSet", klog.KObj(set))
		return currentRevision, updateRevision, getStatusErr
	case getStatusErr != nil:
		return currentRevision, updateRevision, getStatusErr
	case updateStatusErr != nil:
		return currentRevision, updateRevision, updateStatusErr
	}

	klog.V(4).InfoS("InstanceSet revisions", "instanceSet", klog.KObj(set),
		"currentRevision", currentStatus.CurrentRevision,
		"updateRevision", currentStatus.UpdateRevision)

	return currentRevision, updateRevision, nil
}

func (ssc *defaultStatefulInstanceSetControl) ListRevisions(set *workloadsv1alpha2.RoleInstanceSet) ([]*apps.ControllerRevision, error) {
	selector, err := utils.ValidatedLabelSelectorAsSelector(set.Spec.Selector)
	if err != nil {
		return nil, err
	}
	return ssc.controllerHistory.ListControllerRevisions(set, selector)
}

func (ssc *defaultStatefulInstanceSetControl) AdoptOrphanRevisions(
	set *workloadsv1alpha2.RoleInstanceSet,
	revisions []*apps.ControllerRevision) error {
	for i := range revisions {
		adopted, err := ssc.controllerHistory.AdoptControllerRevision(set, controllerKind, revisions[i])
		if err != nil {
			return err
		}
		revisions[i] = adopted
	}
	return nil
}

// truncateHistory truncates any non-live ControllerRevisions in revisions from set's history
func (ssc *defaultStatefulInstanceSetControl) truncateHistory(
	set *workloadsv1alpha2.RoleInstanceSet,
	instances []*workloadsv1alpha2.RoleInstance,
	revisions []*apps.ControllerRevision,
	current *apps.ControllerRevision,
	update *apps.ControllerRevision) error {
	live := map[string]bool{}
	for i := range instances {
		live[getInstanceRevision(instances[i])] = true
	}

	historyLimit := workloadsv1alpha2.DefaultRoleInstanceSetRevisionHistoryLimit
	if set.Spec.RevisionHistoryLimit != nil {
		historyLimit = int(*set.Spec.RevisionHistoryLimit)
	}

	return historyutil.TruncateControllerRevisions(
		revisions,
		current,
		update,
		historyLimit,
		func(revisionName string) bool {
			return live[revisionName]
		},
		ssc.controllerHistory.DeleteControllerRevision,
	)
}

// getInstanceSetRevisions returns the current and update ControllerRevisions for set
func (ssc *defaultStatefulInstanceSetControl) getInstanceSetRevisions(
	set *workloadsv1alpha2.RoleInstanceSet,
	revisions []*apps.ControllerRevision) (*apps.ControllerRevision, *apps.ControllerRevision, int32, error) {
	var currentRevision, updateRevision *apps.ControllerRevision

	revisionCount := len(revisions)
	history.SortControllerRevisions(revisions)

	// Use a local copy of set.Status.CollisionCount to avoid modifying set.Status directly.
	var collisionCount int32
	if set.Status.CollisionCount != nil {
		collisionCount = *set.Status.CollisionCount
	}

	// create a new revision from the current set
	updateRevision, err := newRevision(set, nextRevision(revisions), &collisionCount)
	if err != nil {
		return nil, nil, collisionCount, err
	}

	// find any equivalent revisions
	equalRevisions := history.FindEqualRevisions(revisions, updateRevision)
	equalCount := len(equalRevisions)

	if equalCount > 0 {
		if history.EqualRevision(revisions[revisionCount-1], equalRevisions[equalCount-1]) {
			// if the equivalent revision is immediately prior the update revision has not changed
			updateRevision = revisions[revisionCount-1]
		} else {
			// if the equivalent revision is not immediately prior we will roll back by incrementing the
			// Revision of the equivalent revision
			updateRevision, err = ssc.controllerHistory.UpdateControllerRevision(
				equalRevisions[equalCount-1],
				updateRevision.Revision)
			if err != nil {
				return nil, nil, collisionCount, err
			}
		}
	} else {
		// if there is no equivalent revision we create a new one
		updateRevision, err = ssc.controllerHistory.CreateControllerRevision(set, updateRevision, &collisionCount)
		if err != nil {
			return nil, nil, collisionCount, err
		}
	}

	// attempt to find the revision that corresponds to the current revision
	for i := range revisions {
		if revisions[i].Name == set.Status.CurrentRevision {
			currentRevision = revisions[i]
			break
		}
	}

	// if the current revision is nil we initialize the history by setting it to the update revision
	if currentRevision == nil {
		currentRevision = updateRevision
	}

	return currentRevision, updateRevision, collisionCount, nil
}

// updateStatefulInstanceSet drives a single reconcile pass over the
// RoleInstanceSet, in four ordered phases that each have a clear contract:
//
//	Phase A — observe         compute revisions & topology, build the
//	                          desired status skeleton.
//	Phase B — scale & identity ensure ord [start, end) is fully populated:
//	                          condemn out-of-range, create missing slots,
//	                          process replicas (creation) & condemned (delete).
//	Phase C — progress update for in-rollout sets, drive rolling update with
//	                          a free-vs-costly maxUnavailable budget.
//	Phase D — status & advance recompute counts, decide whether to advance
//	                          status.CurrentRevision (multi-layer guard).
//
// The four phases share the topology computed in Phase A as the single source
// of truth for ordinal range, surge sizing, partition, and budget — so there
// is no second place that recomputes "where surge ends".
func (ssc *defaultStatefulInstanceSetControl) updateStatefulInstanceSet(
	ctx context.Context,
	set *workloadsv1alpha2.RoleInstanceSet,
	currentRevision *apps.ControllerRevision,
	updateRevision *apps.ControllerRevision,
	collisionCount int32,
	instances []*workloadsv1alpha2.RoleInstance,
	revisions []*apps.ControllerRevision) (*workloadsv1alpha2.RoleInstanceSetStatus, error) {

	// =====================  Phase A — observe  =====================
	selector, err := utils.ValidatedLabelSelectorAsSelector(set.Spec.Selector)
	if err != nil {
		return set.Status.DeepCopy(), err
	}
	currentSet, err := ApplyRevision(set, currentRevision)
	if err != nil {
		return set.Status.DeepCopy(), err
	}
	updateSet, err := ApplyRevision(set, updateRevision)
	if err != nil {
		return set.Status.DeepCopy(), err
	}
	topo, err := computeTopology(set, instances, currentRevision.Name, updateRevision.Name)
	if err != nil {
		return set.Status.DeepCopy(), err
	}

	// Update the per-UID "first observed unhealthy" timestamps so the time
	// gate (isStablyUnhealthy) reflects the cache snapshot we are about to
	// reason over. Healthy instances clear their entry; unhealthy instances
	// either start a new timer or carry forward the prior one. See
	// stableUnhealthyDuration for why this gate is the bug-3 defense.
	observeInstanceHealth(instances)

	minReadySeconds := getMinReadySeconds(set)
	monotonic := !allowsBurst(set)

	status := workloadsv1alpha2.RoleInstanceSetStatus{}
	status.ObservedGeneration = set.Generation
	status.CurrentRevision = currentRevision.Name
	status.UpdateRevision = updateRevision.Name
	status.CollisionCount = ptr.To[int32](collisionCount)
	status.LabelSelector = selector.String()
	status.ExpectedUpdatedReplicas = int32(topo.replicas - topo.partition)
	updateStatus(&status, minReadySeconds, currentRevision, updateRevision, instances)

	// ===============  Phase B — scale & identity  ==================
	// Partition instances into in-range slots and condemned (out-of-range).
	// Anything beyond endOrdinal — including stale-rev surge from a prior
	// rollout — falls into condemned and gets deleted by processCondemned.
	replicas := make([]*workloadsv1alpha2.RoleInstance, topo.endOrdinal-topo.startOrdinal)
	condemned := make([]*workloadsv1alpha2.RoleInstance, 0, len(instances))
	for i := range instances {
		ord := getOrdinal(instances[i])
		if instanceInOrdinalRangeWithParams(instances[i], topo.startOrdinal, topo.endOrdinal, topo.reserveOrdinals) {
			replicas[ord-topo.startOrdinal] = instances[i]
		} else if ord >= 0 {
			condemned = append(condemned, instances[i])
		}
	}
	// Fill empty in-range slots. ord < partition uses currentRev (per
	// newVersionedInstance), the rest get updateRev — including surge slots.
	for ord := topo.startOrdinal; ord < topo.endOrdinal; ord++ {
		if topo.reserveOrdinals.Has(ord) {
			continue
		}
		idx := ord - topo.startOrdinal
		if replicas[idx] == nil {
			replicas[idx] = newVersionedInstance(currentSet, updateSet, currentRevision.Name, updateRevision.Name, ord, replicas)
		}
	}
	sort.Sort(descendingOrdinal(condemned))
	_, firstUnhealthyInstance := countUnhealthy(replicas, condemned)

	if set.DeletionTimestamp != nil {
		return &status, nil
	}

	// Refresh in-place readiness condition for all created instances. Without
	// this, freshly created instances would carry InPlaceUpdateReady=False
	// from the instance controller and could deadlock processReplica in
	// OrderedReady mode (which gates on isHealthy).
	for i := range replicas {
		if replicas[i] == nil || !isCreated(replicas[i]) {
			continue
		}
		if _, duration, err := ssc.refreshInstanceState(set, replicas[i], updateRevision.Name); err != nil {
			return &status, err
		} else if duration > 0 {
			durationStore.Push(getInstanceSetKey(set), duration)
		}
	}

	scaleMaxUnavailable, err := getScaleMaxUnavailable(set)
	if err != nil {
		return &status, err
	}
	processReplicaFn := func(i int) (bool, bool, error) {
		return ssc.processReplica(ctx, set, updateSet, monotonic, replicas, i, &status, scaleMaxUnavailable)
	}
	if shouldExit, err := runForAllWithBreak(replicas, processReplicaFn, monotonic); shouldExit || err != nil {
		updateStatus(&status, minReadySeconds, currentRevision, updateRevision, replicas, condemned)
		return &status, err
	}
	processCondemnedFn := func(i int) (bool, error) {
		return ssc.processCondemned(ctx, set, firstUnhealthyInstance, monotonic, condemned, i)
	}
	if shouldExit, err := runForAll(condemned, processCondemnedFn, monotonic); shouldExit || err != nil {
		updateStatus(&status, minReadySeconds, currentRevision, updateRevision, replicas, condemned)
		return &status, err
	}

	// ===============  Phase C — progress rolling update  ===========
	if topo.inRollout {
		if _, err := ssc.progressUpdate(set, &status, currentRevision, updateRevision, revisions, instances, replicas, minReadySeconds, topo); err != nil {
			updateStatus(&status, minReadySeconds, currentRevision, updateRevision, replicas, condemned)
			return &status, err
		}
	}

	// ===============  Phase D — final status & advance  ============
	updateStatus(&status, minReadySeconds, currentRevision, updateRevision, replicas, condemned)
	if shouldAdvanceCurrentRevision(set, replicas, updateRevision.Name, topo) {
		klog.V(2).InfoS("Advancing CurrentRevision", "instanceSet", klog.KObj(set), "from", status.CurrentRevision, "to", updateRevision.Name)
		status.CurrentRevision = updateRevision.Name
	}
	return &status, nil
}

// countUnhealthy returns the count of unhealthy instances across the in-range
// replicas slice and the condemned list, plus the lowest-ordinal unhealthy
// instance (used by processCondemned in OrderedReady mode).
func countUnhealthy(replicas, condemned []*workloadsv1alpha2.RoleInstance) (int, *workloadsv1alpha2.RoleInstance) {
	unhealthy := 0
	firstUnhealthyOrdinal := math.MaxInt32
	var firstUnhealthyInstance *workloadsv1alpha2.RoleInstance
	for i := range replicas {
		if replicas[i] == nil {
			continue
		}
		if !isHealthy(replicas[i]) {
			unhealthy++
			if ord := getOrdinal(replicas[i]); ord < firstUnhealthyOrdinal {
				firstUnhealthyOrdinal = ord
				firstUnhealthyInstance = replicas[i]
			}
		}
	}
	for i := len(condemned) - 1; i >= 0; i-- {
		if !isHealthy(condemned[i]) {
			unhealthy++
			if ord := getOrdinal(condemned[i]); ord < firstUnhealthyOrdinal {
				firstUnhealthyOrdinal = ord
				firstUnhealthyInstance = condemned[i]
			}
		}
	}
	if unhealthy > 0 && firstUnhealthyInstance != nil {
		klog.V(4).InfoS("InstanceSet has unhealthy Instances", "unhealthyReplicas", unhealthy, "instance", klog.KObj(firstUnhealthyInstance))
	}
	return unhealthy, firstUnhealthyInstance
}

// progressUpdate is Phase C: drive the rolling update by replacing base
// instances at non-update revision with their updateRev counterparts (in-place
// or delete-and-recreate).
//
// Budget model:
//
//	effectiveBudget = maxUnavailable + availableSurge
//
// where availableSurge counts surge ords [replicas, endOrdinal) that already
// hold a healthy, non-terminating instance at updateRev — only those provide a
// real availability buffer for base unavailability.
//
// Each target is classified:
//
//   - free   = target is not currently contributing to availability (already
//     unhealthy or terminating). Updating it does not violate the
//     maxUnavailable contract; it does not consume budget. This is the
//     k8s Deployment cleanupUnhealthyReplicas semantics.
//   - costly = target is currently healthy. Updating consumes 1 unit of
//     budget. The combined check
//     newlyUnavail >= maxUnavailable
//     OR (initialBaseUnavail+newlyUnavail >= effectiveBudget AND target not
//     already counted as unavailable)
//     blocks further costly updates this loop.
//
// The "costly" path is what protects ready instances from being deleted faster
// than maxUnavailable allows. The "free" path lets us recover from already-
// degraded states (broken rollout, mid-rollout config change with stale base).
func (ssc *defaultStatefulInstanceSetControl) progressUpdate(
	set *workloadsv1alpha2.RoleInstanceSet,
	status *workloadsv1alpha2.RoleInstanceSetStatus,
	currentRevision *apps.ControllerRevision,
	updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision,
	instances []*workloadsv1alpha2.RoleInstance,
	replicas []*workloadsv1alpha2.RoleInstance,
	minReadySeconds int32,
	topo topology,
) (*workloadsv1alpha2.RoleInstanceSetStatus, error) {
	if updateSatisfied, _, dirty := updateExpectations.SatisfiedExpectations(getInstanceSetKey(set), updateRevision.Name); !updateSatisfied {
		klog.InfoS("Update expectations not satisfied, skipping update",
			"instanceSet", klog.KObj(set), "dirty", dirty, "updateRevision", updateRevision.Name)
		return status, nil
	}
	if modified, err := ssc.refreshAllInstanceStates(set, instances, updateRevision.Name); err != nil {
		return status, err
	} else if modified {
		return status, nil
	}

	availableSurge := countAvailableSurge(replicas, topo, updateRevision.Name)
	effectiveBudget := topo.maxUnavailable + availableSurge
	baseUnavail := ssc.collectBaseUnavailable(set, replicas, topo, minReadySeconds)
	initialBaseUnavail := len(baseUnavail)
	targets := buildUpdateTargets(replicas, topo, updateRevision.Name)

	klog.InfoS("Progress rolling update", "instanceSet", klog.KObj(set),
		"targets", targets, "maxUnavailable", topo.maxUnavailable,
		"maxSurge", topo.maxSurge, "activeSurge", topo.activeSurge,
		"availableSurge", availableSurge, "effectiveBudget", effectiveBudget,
		"initialBaseUnavail", initialBaseUnavail)

	newlyUnavail := 0
	for _, idx := range targets {
		target := replicas[idx]
		// "free" = updating this target does not violate the maxUnavailable
		// contract on BASE ordinals. Three sources of free:
		//   - surge slot: surge is extra capacity, not part of the base
		//     availability contract; replacing it never affects base availability.
		//   - target already terminating: nothing more to do this loop.
		//   - target stably unhealthy (Ready=False observed CONSECUTIVELY for
		//     >= stableUnhealthyDuration): cleanupUnhealthy semantics. The
		//     time gate prevents transient cache flap from triggering a
		//     wrongful free-delete on a base instance that is actually ready —
		//     millisecond-level mislabeling cannot accumulate enough continuous
		//     unhealthy time to satisfy it, while a genuinely broken instance
		//     accrues quickly.
		isSurgeSlot := getOrdinal(target) >= topo.replicas
		isFree := isSurgeSlot || isTerminating(target) || isStablyUnhealthy(target)

		if !isFree && initialBaseUnavail+newlyUnavail >= effectiveBudget {
			klog.InfoS("Rolling update budget exhausted",
				"instanceSet", klog.KObj(set), "instance", klog.KObj(target),
				"initialBaseUnavail", initialBaseUnavail, "newlyUnavail", newlyUnavail,
				"effectiveBudget", effectiveBudget)
			return status, nil
		}
		if isTerminating(target) {
			continue
		}

		decrement, err := ssc.applyTargetUpdate(set, target, currentRevision, updateRevision, revisions, isSurgeSlot, isFree)
		if err != nil {
			return status, err
		}
		if !isFree {
			baseUnavail.Insert(target.Name)
			newlyUnavail++
		}
		if decrement {
			status.CurrentReplicas--
		}
	}
	return status, nil
}

// countAvailableSurge counts surge slots [replicas, endOrdinal) whose instance
// is at updateRev AND healthy AND not terminating. Only those provide a real
// availability buffer — stale-rev or unhealthy surge does not.
func countAvailableSurge(
	replicas []*workloadsv1alpha2.RoleInstance,
	topo topology,
	updateRev string,
) int {
	out := 0
	for ord := topo.surgeStart; ord < topo.endOrdinal; ord++ {
		idx := ord - topo.startOrdinal
		if idx < 0 || idx >= len(replicas) {
			continue
		}
		inst := replicas[idx]
		if inst != nil && isCreated(inst) && isHealthy(inst) && !isTerminating(inst) &&
			getInstanceRevision(inst) == updateRev {
			out++
		}
	}
	return out
}

// collectBaseUnavailable returns the set of base ord [start, replicas) instance
// names that are currently NOT contributing to availability (unhealthy,
// in-place-update incomplete, or not-yet-available under minReadySeconds).
// These are "already unavailable" for budget purposes — updating them in
// progressUpdate is free. Also pushes any minReadySeconds wait into
// durationStore so the controller is requeued when those windows expire.
func (ssc *defaultStatefulInstanceSetControl) collectBaseUnavailable(
	set *workloadsv1alpha2.RoleInstanceSet,
	replicas []*workloadsv1alpha2.RoleInstance,
	topo topology,
	minReadySeconds int32,
) sets.Set[string] {
	out := sets.New[string]()
	opts := instanceinplace.SetOptionsDefaults(&instanceinplace.UpdateOptions{})
	minWaitTime := MaxMinReadySeconds * time.Second

	for ord := topo.startOrdinal; ord < topo.replicas; ord++ {
		idx := ord - topo.startOrdinal
		if idx < 0 || idx >= len(replicas) {
			continue
		}
		inst := replicas[idx]
		if inst == nil {
			continue
		}
		if !isHealthy(inst) || opts.CheckRoleInstanceUpdateCompleted(inst) != nil {
			out.Insert(inst.Name)
			continue
		}
		isAvailable, waitTime := isInstanceRunningAndAvailable(inst, minReadySeconds)
		if isAvailable {
			continue
		}
		out.Insert(inst.Name)
		if waitTime != 0 && waitTime <= minWaitTime {
			minWaitTime = waitTime
			durationStore.Push(getInstanceSetKey(set), waitTime)
		}
	}
	return out
}

// buildUpdateTargets returns indices into replicas[] of in-range ords
// [partition, endOrdinal) holding an instance whose revision != updateRev,
// sorted by descending ordinal. The range deliberately includes surge ords
// [replicas, endOrdinal): a surge slot may hold a stale-rev instance from a
// superseded rollout, and Phase B cannot condemn it (the slot is still
// in-range for the current rollout's surge needs), so progressUpdate must
// recycle it — otherwise stale-rev surge persists forever.
func buildUpdateTargets(
	replicas []*workloadsv1alpha2.RoleInstance,
	topo topology,
	updateRev string,
) []int {
	out := make([]int, 0, topo.endOrdinal-topo.partition)
	for ord := topo.partition; ord < topo.endOrdinal; ord++ {
		idx := ord - topo.startOrdinal
		if idx < 0 || idx >= len(replicas) {
			continue
		}
		if replicas[idx] == nil {
			continue
		}
		if getInstanceRevision(replicas[idx]) == updateRev {
			continue
		}
		out = append(out, idx)
	}
	// Highest ordinal first (statefulset convention; also means surge slots
	// are recycled before base, so we re-establish fresh surge at updateRev
	// before chipping at base).
	sort.Slice(out, func(i, j int) bool {
		return getOrdinal(replicas[out[i]]) > getOrdinal(replicas[out[j]])
	})
	return out
}

// applyTargetUpdate performs a single target's update — in-place when possible,
// otherwise delete-and-recreate via Phase B on the next reconcile. Returns
// `decrementCurrent=true` when the operation actually transitioned the target
// away from currentRevision (so caller can decrement status.CurrentReplicas).
func (ssc *defaultStatefulInstanceSetControl) applyTargetUpdate(
	set *workloadsv1alpha2.RoleInstanceSet,
	target *workloadsv1alpha2.RoleInstance,
	currentRevision *apps.ControllerRevision,
	updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision,
	isSurgeSlot bool,
	isFree bool,
) (bool, error) {
	klog.InfoS("Updating instance", "instanceSet", klog.KObj(set), "instance", klog.KObj(target),
		"from", getInstanceRevision(target), "to", updateRevision.Name,
		"isSurge", isSurgeSlot, "free", isFree)
	inplacing, err := ssc.inPlaceUpdateInstance(set, target, updateRevision, revisions)
	if err != nil {
		return false, err
	}
	transitioned := inplacing
	if !inplacing {
		_, actualDeleting, err := ssc.deleteInstance(set, target)
		if err != nil {
			return false, err
		}
		transitioned = actualDeleting
	}
	return transitioned && getInstanceRevision(target) == currentRevision.Name, nil
}

// shouldAdvanceCurrentRevision decides whether status.CurrentRevision can be
// advanced to updateRev this reconcile. Multi-layer guard:
//
//	① We are actually in a rollout (currentRev != updateRev, !Paused).
//	② Partition has been fully consumed (partition == 0). When partition > 0
//	   the ords below partition are pinned at the "old" revision, so
//	   advancing CurrentRevision to updateRev would lie about their state and
//	   prevent later partition decreases from being recognized as
//	   "currentRev != updateRev → in rollout" — exactly the bug behind
//	   "lower partition does not progress".
//	③ The PRIOR persisted status already named updateRev as UpdateRevision
//	   AND counted UpdatedReplicas >= replicas - partition. This forces the
//	   "all base updated" observation to have survived at least one full
//	   reconcile cycle, so a transient cache lag in the current cycle cannot
//	   cause an erroneous advance. This is the central defense against the
//	   "rollout right after creation deletes everything" cascade.
//	④ updateExpectations are satisfied (no in-place updates we are still
//	   waiting for the API to acknowledge).
//	⑤ The observed in-memory snapshot for every base ord [partition,
//	   replicas) shows: at updateRev, isCreated, isHealthy, !isTerminating,
//	   and ObservedGeneration synced.
//
// We never write to the in-memory rev label after an in-place update (see
// inPlaceUpdateInstance), so the snapshot is the unmodified Phase-A read.
func shouldAdvanceCurrentRevision(
	set *workloadsv1alpha2.RoleInstanceSet,
	replicas []*workloadsv1alpha2.RoleInstance,
	updateRev string,
	topo topology,
) bool {
	if !topo.inRollout {
		return false
	}
	// ② only advance once partition is fully consumed
	if topo.partition > 0 {
		return false
	}
	// ③ prior persisted concurrence
	if set.Status.UpdateRevision != updateRev {
		return false
	}
	if set.Status.UpdatedReplicas < int32(topo.replicas-topo.partition) {
		return false
	}
	// ④ no in-flight in-place updates
	if satisfied, _, _ := updateExpectations.SatisfiedExpectations(getInstanceSetKey(set), updateRev); !satisfied {
		return false
	}
	// ⑤ observed snapshot all green
	for ord := topo.partition; ord < topo.replicas; ord++ {
		idx := ord - topo.startOrdinal
		if idx < 0 || idx >= len(replicas) {
			return false
		}
		inst := replicas[idx]
		if inst == nil || !isCreated(inst) || isTerminating(inst) {
			return false
		}
		if getInstanceRevision(inst) != updateRev {
			return false
		}
		if !isHealthy(inst) {
			return false
		}
		if inst.Status.ObservedGeneration < inst.Generation {
			return false
		}
	}
	return true
}

// refreshAllInstanceStates refreshes states for all instances and returns if any was modified
func (ssc *defaultStatefulInstanceSetControl) refreshAllInstanceStates(
	set *workloadsv1alpha2.RoleInstanceSet,
	instances []*workloadsv1alpha2.RoleInstance,
	updateRevisionName string,
) (bool, error) {
	var modified bool
	for _, instance := range instances {
		if instance == nil {
			continue
		}
		refreshed, duration, err := ssc.refreshInstanceState(set, instance, updateRevisionName)
		if err != nil {
			return false, err
		}
		if duration > 0 {
			durationStore.Push(getInstanceSetKey(set), duration)
		}
		if refreshed {
			modified = true
		}
	}
	return modified, nil
}

// processReplica processes a single replica instance.
// When monotonic is true (OrderedReady policy), instances are created one at a time:
// the loop exits after each creation and waits for the instance to become healthy
// before proceeding to the next.
// When monotonic is false (Parallel policy), all pending instances are created in a
// single reconcile pass and the loop continues without waiting.
func (ssc *defaultStatefulInstanceSetControl) processReplica(
	ctx context.Context,
	set *workloadsv1alpha2.RoleInstanceSet,
	_ *workloadsv1alpha2.RoleInstanceSet,
	monotonic bool,
	replicas []*workloadsv1alpha2.RoleInstance,
	i int,
	status *workloadsv1alpha2.RoleInstanceSetStatus,
	_ int) (bool, bool, error) {

	// if the Instance is in pending create it
	if !isCreated(replicas[i]) {
		if err := ssc.instanceControl.CreateStatefulInstance(ctx, set, replicas[i]); err != nil {
			return false, false, err
		}
		// Increment CurrentReplicas and Replicas
		status.Replicas++
		if getInstanceRevision(replicas[i]) == set.Status.CurrentRevision {
			status.CurrentReplicas++
		}
		// In monotonic (OrderedReady) mode, exit after each creation and wait for
		// the instance to become healthy before creating the next one.
		// In non-monotonic (Parallel) mode, continue to create all replicas at once.
		if monotonic {
			return true, false, nil
		}
		return false, true, nil
	}

	// If we find an Instance that has not become healthy yet, block in monotonic
	// mode so that the next replica is not processed until this one is ready.
	if !isHealthy(replicas[i]) {
		klog.V(4).InfoS("InstanceSet waiting for unhealthy Instance to become healthy", "instanceSet", klog.KObj(set), "instance", klog.KObj(replicas[i]))
		return monotonic, false, nil
	}

	return false, false, nil
}

// processCondemned processes a condemned instance for deletion
func (ssc *defaultStatefulInstanceSetControl) processCondemned(
	_ context.Context,
	set *workloadsv1alpha2.RoleInstanceSet,
	firstUnhealthyInstance *workloadsv1alpha2.RoleInstance,
	monotonic bool,
	condemned []*workloadsv1alpha2.RoleInstance,
	i int) (bool, error) {

	if !isCreated(condemned[i]) {
		return false, nil
	}

	if monotonic && condemned[i] != firstUnhealthyInstance && !isHealthy(condemned[i]) {
		return true, nil
	}

	klog.V(2).InfoS("InstanceSet terminating condemned Instance", "instanceSet", klog.KObj(set), "instance", klog.KObj(condemned[i]))
	if err := ssc.instanceControl.DeleteStatefulInstance(set, condemned[i]); err != nil {
		return monotonic, err
	}

	return false, nil
}

func (ssc *defaultStatefulInstanceSetControl) updateInstanceSetStatus(
	ctx context.Context,
	set *workloadsv1alpha2.RoleInstanceSet,
	status *workloadsv1alpha2.RoleInstanceSetStatus) error {
	return ssc.statusUpdater.UpdateInstanceSetStatus(ctx, set, status)
}

// Helper types and functions

type descendingOrdinal []*workloadsv1alpha2.RoleInstance

func (o descendingOrdinal) Len() int {
	return len(o)
}

func (o descendingOrdinal) Swap(i, j int) {
	o[i], o[j] = o[j], o[i]
}

func (o descendingOrdinal) Less(i, j int) bool {
	return getOrdinal(o[i]) > getOrdinal(o[j])
}

// refreshInstanceState refreshes the state of an instance and returns if state was refreshed
func (ssc *defaultStatefulInstanceSetControl) refreshInstanceState(
	set *workloadsv1alpha2.RoleInstanceSet,
	instance *workloadsv1alpha2.RoleInstance,
	_ string,
) (bool, time.Duration, error) {
	opts := &instanceinplace.UpdateOptions{}
	opts = instanceinplace.SetOptionsDefaults(opts)

	// if instance is updating and completed, refresh its state
	if opts.CheckRoleInstanceUpdateCompleted(instance) == nil {
		result := ssc.inplaceControl.Refresh(instance, opts)
		if result.RefreshErr != nil {
			return false, 0, result.RefreshErr
		}
		// Only return modified=true when Refresh has remaining work (grace period delay).
		// Do NOT use getInstanceInPlaceUpdateDuration here, as it returns time.Since(conditionSetTime)
		// which is always > 0 for completed updates, causing perpetual modified=true and
		// permanently blocking the update loop from reaching remaining replicas.
		if result.DelayDuration > 0 {
			klog.V(2).InfoS("InstanceSet refreshed Instance state after in-place update",
				"instanceSet", klog.KObj(set), "instance", klog.KObj(instance),
				"delayDuration", result.DelayDuration)
			return true, result.DelayDuration, nil
		}
	}
	return false, 0, nil
}

// inPlaceUpdateInstance performs in-place update on an instance
func (ssc *defaultStatefulInstanceSetControl) inPlaceUpdateInstance(
	set *workloadsv1alpha2.RoleInstanceSet,
	instance *workloadsv1alpha2.RoleInstance,
	updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision,
) (bool, error) {
	opts := &instanceinplace.UpdateOptions{}
	opts = instanceinplace.SetOptionsDefaults(opts)

	if set.Spec.UpdateStrategy.InPlaceUpdateStrategy != nil {
		opts.GracePeriodSeconds = set.Spec.UpdateStrategy.InPlaceUpdateStrategy.GracePeriodSeconds
	}

	// Find old revision for this instance
	currentRevisionName := getInstanceRevision(instance)
	var currentRevision *apps.ControllerRevision
	for _, rev := range revisions {
		if rev.Name == currentRevisionName {
			currentRevision = rev
			break
		}
	}
	if currentRevision == nil {
		// If no current revision found, cannot perform in-place update
		return false, nil
	}

	// Check if can update in place
	if !ssc.inplaceControl.CanUpdateInPlace(currentRevision, updateRevision, opts) {
		return false, nil
	}

	// Perform in-place update
	result := ssc.inplaceControl.Update(instance, currentRevision, updateRevision, opts)
	if result.UpdateErr != nil {
		return false, result.UpdateErr
	}

	if result.InPlaceUpdate {
		klog.V(2).InfoS("InstanceSet in-place updating Instance",
			"instanceSet", klog.KObj(set), "instance", klog.KObj(instance))
		updateExpectations.ExpectUpdated(getInstanceSetKey(set), updateRevision.Name, instance)

		// NOTE: Do NOT mutate instance.Labels in memory here. inplaceControl.Update
		// patched the API server (label + spec + condition Ready=False), but the
		// in-memory object we hold reflects the snapshot from the start of this
		// reconcile. If we forced the rev label here, shouldAdvanceCurrentRevision
		// would see "rev=updateRev AND isHealthy=true (stale Ready)" and could
		// erroneously advance status.CurrentRevision, which is exactly the
		// cascade behind the "rollout right after creation deletes everything"
		// bug. Next reconcile reads fresh state and sees both new rev and
		// updated condition, at which point advance can fire safely.
	}

	return result.InPlaceUpdate, nil
}

// deleteInstance deletes an instance and returns if it was actually deleted
func (ssc *defaultStatefulInstanceSetControl) deleteInstance(
	set *workloadsv1alpha2.RoleInstanceSet,
	instance *workloadsv1alpha2.RoleInstance,
) (bool, bool, error) {
	if isTerminating(instance) {
		return false, false, nil
	}

	err := ssc.instanceControl.DeleteStatefulInstance(set, instance)
	if err != nil {
		return false, false, err
	}

	return true, true, nil
}

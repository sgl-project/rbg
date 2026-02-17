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
	"fmt"
	"math"
	"sort"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/history"
	"k8s.io/utils/ptr"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	instanceinplace "sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
	instancelifecycle "sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/utils"
)

const (
	// MaxMinReadySeconds is the max value of MinReadySeconds
	MaxMinReadySeconds = 300

	// Realistic value for maximum in-flight requests when processing in parallel mode.
	MaxBatchSize = 500
)

// StatefulInstanceSetControlInterface implements the control logic for updating InstanceSets managing Instances
type StatefulInstanceSetControlInterface interface {
	// UpdateStatefulInstanceSet implements the control logic for Instance creation, update, and deletion
	UpdateStatefulInstanceSet(ctx context.Context, set *workloadsv1alpha1.InstanceSet, instances []*workloadsv1alpha1.Instance) error
	// ListRevisions returns a array of the ControllerRevisions that represent the revisions of set.
	ListRevisions(set *workloadsv1alpha1.InstanceSet) ([]*apps.ControllerRevision, error)
	// AdoptOrphanRevisions adopts any orphaned ControllerRevisions that match set's Selector.
	AdoptOrphanRevisions(set *workloadsv1alpha1.InstanceSet, revisions []*apps.ControllerRevision) error
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
func (ssc *defaultStatefulInstanceSetControl) UpdateStatefulInstanceSet(ctx context.Context, set *workloadsv1alpha1.InstanceSet, instances []*workloadsv1alpha1.Instance) error {
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
	ctx context.Context, set *workloadsv1alpha1.InstanceSet, instances []*workloadsv1alpha1.Instance, revisions []*apps.ControllerRevision) (*apps.ControllerRevision, *apps.ControllerRevision, error) {
	var currentStatus *workloadsv1alpha1.InstanceSetStatus
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

func (ssc *defaultStatefulInstanceSetControl) ListRevisions(set *workloadsv1alpha1.InstanceSet) ([]*apps.ControllerRevision, error) {
	selector, err := utils.ValidatedLabelSelectorAsSelector(set.Spec.Selector)
	if err != nil {
		return nil, err
	}
	return ssc.controllerHistory.ListControllerRevisions(set, selector)
}

func (ssc *defaultStatefulInstanceSetControl) AdoptOrphanRevisions(
	set *workloadsv1alpha1.InstanceSet,
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
	set *workloadsv1alpha1.InstanceSet,
	instances []*workloadsv1alpha1.Instance,
	revisions []*apps.ControllerRevision,
	current *apps.ControllerRevision,
	update *apps.ControllerRevision) error {
	history := make([]*apps.ControllerRevision, 0, len(revisions))
	// mark all live revisions
	live := map[string]bool{}
	if current != nil {
		live[current.Name] = true
	}
	if update != nil {
		live[update.Name] = true
	}
	for i := range instances {
		live[getInstanceRevision(instances[i])] = true
	}
	// collect live revisions and historic revisions
	for i := range revisions {
		if !live[revisions[i].Name] {
			history = append(history, revisions[i])
		}
	}
	historyLen := len(history)
	// Check if RevisionHistoryLimit is set
	if set.Spec.RevisionHistoryLimit == nil {
		return nil
	}
	historyLimit := int(*set.Spec.RevisionHistoryLimit)
	if historyLen <= historyLimit {
		return nil
	}
	// delete any non-live history to maintain the revision limit.
	history = history[:(historyLen - historyLimit)]
	for i := 0; i < len(history); i++ {
		if err := ssc.controllerHistory.DeleteControllerRevision(history[i]); err != nil {
			return err
		}
	}
	return nil
}

// getInstanceSetRevisions returns the current and update ControllerRevisions for set
func (ssc *defaultStatefulInstanceSetControl) getInstanceSetRevisions(
	set *workloadsv1alpha1.InstanceSet,
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

// updateStatefulInstanceSet performs the update function for a InstanceSet managing instances
func (ssc *defaultStatefulInstanceSetControl) updateStatefulInstanceSet(
	ctx context.Context,
	set *workloadsv1alpha1.InstanceSet,
	currentRevision *apps.ControllerRevision,
	updateRevision *apps.ControllerRevision,
	collisionCount int32,
	instances []*workloadsv1alpha1.Instance,
	revisions []*apps.ControllerRevision) (*workloadsv1alpha1.InstanceSetStatus, error) {
	selector, err := utils.ValidatedLabelSelectorAsSelector(set.Spec.Selector)
	if err != nil {
		return set.Status.DeepCopy(), err
	}

	// get the current and update revisions of the set.
	currentSet, err := ApplyRevision(set, currentRevision)
	if err != nil {
		return set.Status.DeepCopy(), err
	}
	updateSet, err := ApplyRevision(set, updateRevision)
	if err != nil {
		return set.Status.DeepCopy(), err
	}

	// set the generation, and revisions in the returned status
	status := workloadsv1alpha1.InstanceSetStatus{}
	status.ObservedGeneration = set.Generation
	status.CurrentRevision = currentRevision.Name
	status.UpdateRevision = updateRevision.Name
	status.CollisionCount = ptr.To[int32](collisionCount)
	status.LabelSelector = selector.String()
	minReadySeconds := getMinReadySeconds(set)

	updateStatus(&status, minReadySeconds, currentRevision, updateRevision, instances)

	startOrdinal, endOrdinal, reserveOrdinals := getInstanceSetReplicasRange(set)
	// slice that will contain all Instances such that startOrdinal <= getOrdinal(instance) < endOrdinal
	replicas := make([]*workloadsv1alpha1.Instance, endOrdinal-startOrdinal)
	// slice that will contain all Instances such that getOrdinal(instance) < startOrdinal or getOrdinal(instance) >= endOrdinal
	condemned := make([]*workloadsv1alpha1.Instance, 0, len(instances))
	unhealthy := 0
	firstUnhealthyOrdinal := math.MaxInt32
	var firstUnhealthyInstance *workloadsv1alpha1.Instance
	monotonic := !allowsBurst(set)

	// First we partition instances into two lists valid replicas and condemned Instances
	for i := range instances {
		if ord := getOrdinal(instances[i]); instanceInOrdinalRangeWithParams(instances[i], startOrdinal, endOrdinal, reserveOrdinals) {
			// if the ordinal of the instance is within the range of the current number of replicas
			// insert it at the indirection of its ordinal
			replicas[ord-startOrdinal] = instances[i]

		} else if ord >= 0 {
			// if the ordinal is valid, but not within the range
			// add it to the condemned list
			condemned = append(condemned, instances[i])
		}
	}

	// for any empty indices in the sequence [startOrdinal,endOrdinal) create a new Instance at the correct revision
	for ord := startOrdinal; ord < endOrdinal; ord++ {
		if reserveOrdinals.Has(ord) {
			continue
		}
		replicaIdx := ord - startOrdinal
		if replicas[replicaIdx] == nil {
			replicas[replicaIdx] = newVersionedInstance(
				currentSet,
				updateSet,
				currentRevision.Name,
				updateRevision.Name, ord, replicas)
		}
	}

	// sort the condemned Instances by their ordinals
	sort.Sort(descendingOrdinal(condemned))

	// find the first unhealthy Instance
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

	// or the first unhealthy condemned Instance (condemned are sorted in descending order for ease of use)
	for i := len(condemned) - 1; i >= 0; i-- {
		if !isHealthy(condemned[i]) {
			unhealthy++
			if ord := getOrdinal(condemned[i]); ord < firstUnhealthyOrdinal {
				firstUnhealthyOrdinal = ord
				firstUnhealthyInstance = condemned[i]
			}
		}
	}

	if unhealthy > 0 {
		klog.V(4).InfoS("InstanceSet has unhealthy Instances", "instanceSet", klog.KObj(set), "unhealthyReplicas", unhealthy, "instance", klog.KObj(firstUnhealthyInstance))
	}

	// If the InstanceSet is being deleted, don't do anything other than updating status.
	if set.DeletionTimestamp != nil {
		return &status, nil
	}

	// Refresh states for all created instances to ensure InstanceInPlaceUpdateReady condition is up-to-date.
	// Newly created instances default to InstanceInPlaceUpdateReady=False at the Instance controller level,
	// but they don't actually need an in-place update. Without this refresh, these instances would remain
	// unhealthy and block the processReplica loop in monotonic (OrderedReady) mode, creating a deadlock
	// where processReplica waits for healthy instances, but refreshInstanceState (which sets the condition
	// to True) is only called in rollingUpdateInstances which is never reached.
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

	// First, process each living replica. Exit if we run into an error or something blocking in monotonic mode.
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

	// Process condemned instances
	processCondemnedFn := func(i int) (bool, error) {
		return ssc.processCondemned(ctx, set, firstUnhealthyInstance, monotonic, condemned, i)
	}
	if shouldExit, err := runForAll(condemned, processCondemnedFn, monotonic); shouldExit || err != nil {
		updateStatus(&status, minReadySeconds, currentRevision, updateRevision, replicas, condemned)
		return &status, err
	}
	updateStatus(&status, minReadySeconds, currentRevision, updateRevision, replicas, condemned)

	//TODO: support OnDelete
	//for the OnDelete strategy we short circuit.
	//if set.Spec.UpdateStrategy.Type == apps.OnDeleteStatefulSetStrategyType {
	//	return &status, nil
	//}

	return ssc.rollingUpdateInstances(
		set, &status, currentRevision, updateRevision, revisions, instances, replicas, minReadySeconds,
	)
}

// rollingUpdateInstances implements the rolling update logic for instances
func (ssc *defaultStatefulInstanceSetControl) rollingUpdateInstances(
	set *workloadsv1alpha1.InstanceSet,
	status *workloadsv1alpha1.InstanceSetStatus,
	currentRevision *apps.ControllerRevision,
	updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision,
	instances []*workloadsv1alpha1.Instance,
	replicas []*workloadsv1alpha1.Instance,
	minReadySeconds int32,
) (*workloadsv1alpha1.InstanceSetStatus, error) {

	// If update expectations have not satisfied yet, skip updating instances
	if updateSatisfied, _, updateDirtyInstances := updateExpectations.SatisfiedExpectations(getInstanceSetKey(set), updateRevision.Name); !updateSatisfied {
		klog.InfoS("Update expectations not satisfied, skipping update", "instanceSet", klog.KObj(set), "updateDirtyInstances", updateDirtyInstances, "updateRevision", updateRevision.Name)
		return status, nil
	}
	klog.InfoS("Update expectations satisfied, proceeding with update", "instanceSet", klog.KObj(set), "updateRevision", updateRevision.Name)

	// refresh states for all instances
	var modified bool
	for _, instance := range instances {
		if instance == nil {
			continue
		}
		refreshed, duration, err := ssc.refreshInstanceState(set, instance, updateRevision.Name)
		if err != nil {
			return status, err
		} else if duration > 0 {
			durationStore.Push(getInstanceSetKey(set), duration)
		}
		if refreshed {
			modified = true
		}
	}
	if modified {
		return status, nil
	}

	var err error
	// compute the minimum ordinal of the target sequence for a destructive update based on the strategy
	maxUnavailable := 1
	if set.Spec.UpdateStrategy.Paused {
		return status, nil
	}

	if set.Spec.UpdateStrategy.MaxUnavailable != nil {
		if set.Spec.Replicas == nil {
			return status, fmt.Errorf("Replicas is nil")
		}
		maxUnavailable, err = intstrutil.GetValueFromIntOrPercent(
			set.Spec.UpdateStrategy.MaxUnavailable,
			int(*set.Spec.Replicas), false)
		if err != nil {
			return status, err
		}
	}
	// maxUnavailable should not be less than 1
	if maxUnavailable < 1 {
		maxUnavailable = 1
	}

	minWaitTime := MaxMinReadySeconds * time.Second
	unavailableInstances := sets.NewString()
	opts := &instanceinplace.UpdateOptions{}
	opts = instanceinplace.SetOptionsDefaults(opts)
	// counts any targets in the replicas that are unhealthy for checking maxUnavailable limit
	for target := range replicas {
		if replicas[target] == nil {
			continue
		}
		if !isHealthy(replicas[target]) {
			// count instance as unavailable if it's unhealthy
			unavailableInstances.Insert(replicas[target].Name)
		} else if completedErr := opts.CheckInstanceUpdateCompleted(replicas[target]); completedErr != nil {
			// count instance as unavailable if it's in-place updating and not ready
			klog.V(4).ErrorS(completedErr, "InstanceSet found Instance in-place update not-ready",
				"instanceSet", klog.KObj(set), "instance", klog.KObj(replicas[target]))
			unavailableInstances.Insert(replicas[target].Name)
		} else if isAvailable, waitTime := isInstanceRunningAndAvailable(replicas[target], minReadySeconds); !isAvailable {
			// count instance as unavailable if it's not available yet given minReadySeconds
			unavailableInstances.Insert(replicas[target].Name)
			if waitTime != 0 && waitTime <= minWaitTime {
				minWaitTime = waitTime
				durationStore.Push(getInstanceSetKey(set), waitTime)
			}
		}
	}

	// Sort instances to update
	if set.Spec.Replicas == nil {
		return status, fmt.Errorf("Replicas is nil")
	}
	updateIndexes := sortInstancesToUpdate(&set.Spec.UpdateStrategy, updateRevision.Name, *set.Spec.Replicas, replicas)
	// Log detailed instance revisions for debugging
	for i, instance := range replicas {
		if instance != nil {
			klog.InfoS("Instance revision status", "instanceSet", klog.KObj(set), "index", i, "instance", klog.KObj(instance), "currentRevision", getInstanceRevision(instance), "updateRevision", updateRevision.Name, "needsUpdate", getInstanceRevision(instance) != updateRevision.Name)
		}
	}
	klog.InfoS("Prepare to update instance indexes for InstanceSet", "instanceSet", klog.KObj(set), "instanceIndexes", updateIndexes, "maxUnavailable", maxUnavailable, "unavailableInstances", unavailableInstances.List())

	// Track instances updated in this reconcile loop
	updatedInThisLoop := 0

	// update instances in sequence
	for _, target := range updateIndexes {
		// the target is already up-to-date, go to next
		if getInstanceRevision(replicas[target]) == updateRevision.Name {
			continue
		}

		// Block if:
		// 1. We already triggered maxUnavailable updates in this reconcile loop
		// 2. OR there are already maxUnavailable instances that are unavailable (not counting current target)
		if updatedInThisLoop >= maxUnavailable || (len(unavailableInstances) >= maxUnavailable && !unavailableInstances.Has(replicas[target].Name)) {
			klog.InfoS("InstanceSet was waiting for unavailable Instances to update, blocked instance",
				"instanceSet", klog.KObj(set), "unavailableInstances", unavailableInstances.List(), "blockedInstance", klog.KObj(replicas[target]), "updatedInThisLoop", updatedInThisLoop, "maxUnavailable", maxUnavailable)
			return status, nil
		}

		// delete or inplace update the Instance if it does not match the update revision
		if !isTerminating(replicas[target]) {
			klog.InfoS("InstanceSet attempting to update Instance", "instanceSet", klog.KObj(set), "instance", klog.KObj(replicas[target]), "currentRevision", getInstanceRevision(replicas[target]), "targetRevision", updateRevision.Name)
			inplacing, inplaceUpdateErr := ssc.inPlaceUpdateInstance(set, replicas[target], updateRevision, revisions)
			if inplaceUpdateErr != nil {
				return status, inplaceUpdateErr
			}
			klog.InfoS("InstanceSet update Instance result", "instanceSet", klog.KObj(set), "instance", klog.KObj(replicas[target]), "inplacing", inplacing)
			// if instance is inplacing or actual deleting, update status
			revisionNeedDecrease := inplacing
			if !inplacing {
				klog.V(2).InfoS("InstanceSet terminating Instance for update", "instanceSet", klog.KObj(set), "instance", klog.KObj(replicas[target]))
				if _, actualDeleting, err := ssc.deleteInstance(set, replicas[target]); err != nil {
					return status, err
				} else {
					revisionNeedDecrease = actualDeleting
				}
			}
			// Mark target as unavailable for this reconcile loop
			unavailableInstances.Insert(replicas[target].Name)
			updatedInThisLoop++

			if revisionNeedDecrease && getInstanceRevision(replicas[target]) == currentRevision.Name {
				status.CurrentReplicas--
			}
		}
	}

	return status, nil
}

// processReplica processes a single replica instance
func (ssc *defaultStatefulInstanceSetControl) processReplica(
	ctx context.Context,
	set *workloadsv1alpha1.InstanceSet,
	updateSet *workloadsv1alpha1.InstanceSet,
	monotonic bool,
	replicas []*workloadsv1alpha1.Instance,
	i int,
	status *workloadsv1alpha1.InstanceSetStatus,
	scaleMaxUnavailable int) (bool, bool, error) {

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
		// if the set does not allow bursting, return immediately
		if monotonic {
			return true, false, nil
		}
		return false, true, nil
	}

	// If we find a Instance that has not been updated, return immediately
	if !isHealthy(replicas[i]) {
		klog.V(4).InfoS("InstanceSet waiting for unhealthy Instance to become healthy", "instanceSet", klog.KObj(set), "instance", klog.KObj(replicas[i]))
		return monotonic, false, nil
	}

	// If the Instance is in UpdateStrategy.RollingUpdate and is not at the update revision, attempt update
	if getPodRevision(replicas[i]) != set.Status.UpdateRevision {
		// Update logic would go here
		// For minimal viable version, skip update
	}

	return false, false, nil
}

// processCondemned processes a condemned instance for deletion
func (ssc *defaultStatefulInstanceSetControl) processCondemned(
	ctx context.Context,
	set *workloadsv1alpha1.InstanceSet,
	firstUnhealthyInstance *workloadsv1alpha1.Instance,
	monotonic bool,
	condemned []*workloadsv1alpha1.Instance,
	i int) (bool, error) {

	if !isCreated(condemned[i]) {
		return false, nil
	}

	if monotonic && condemned[i] != firstUnhealthyInstance && isHealthy(condemned[i]) {
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
	set *workloadsv1alpha1.InstanceSet,
	status *workloadsv1alpha1.InstanceSetStatus) error {
	return ssc.statusUpdater.UpdateInstanceSetStatus(ctx, set, status)
}

// Helper types and functions

type descendingOrdinal []*workloadsv1alpha1.Instance

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
	set *workloadsv1alpha1.InstanceSet,
	instance *workloadsv1alpha1.Instance,
	updateRevision string,
) (bool, time.Duration, error) {
	opts := &instanceinplace.UpdateOptions{}
	opts = instanceinplace.SetOptionsDefaults(opts)

	// if instance is updating and completed, refresh its state
	if opts.CheckInstanceUpdateCompleted(instance) == nil {
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

// getInstanceInPlaceUpdateDuration calculates duration from in-place update timestamp
func getInstanceInPlaceUpdateDuration(instance *workloadsv1alpha1.Instance) (time.Duration, error) {
	for _, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha1.InstanceInPlaceUpdateReady {
			if cond.Status == v1.ConditionTrue {
				return time.Since(cond.LastTransitionTime.Time), nil
			}
		}
	}
	return 0, nil
}

// refreshInstanceStateAfterInplaceUpdate refreshes instance state after inplace update is done
func (ssc *defaultStatefulInstanceSetControl) refreshInstanceStateAfterInplaceUpdate(instance *workloadsv1alpha1.Instance) (bool, error) {
	opts := &instanceinplace.UpdateOptions{}
	opts = instanceinplace.SetOptionsDefaults(opts)

	if opts.CheckInstanceUpdateCompleted(instance) != nil {
		return false, nil
	}

	result := ssc.inplaceControl.Refresh(instance, opts)
	return result.RefreshErr == nil, result.RefreshErr
}

// inPlaceUpdateInstance performs in-place update on an instance
func (ssc *defaultStatefulInstanceSetControl) inPlaceUpdateInstance(
	set *workloadsv1alpha1.InstanceSet,
	instance *workloadsv1alpha1.Instance,
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

		// Update the instance's revision label in memory to reflect the update
		// This prevents the controller from repeatedly triggering updates
		if instance.Labels == nil {
			instance.Labels = make(map[string]string)
		}
		instance.Labels[apps.ControllerRevisionHashLabelKey] = updateRevision.Name
	}

	return result.InPlaceUpdate, nil
}

// deleteInstance deletes an instance and returns if it was actually deleted
func (ssc *defaultStatefulInstanceSetControl) deleteInstance(
	set *workloadsv1alpha1.InstanceSet,
	instance *workloadsv1alpha1.Instance,
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

// sortInstancesToUpdate returns the indexes of instances in the order they should be updated
func sortInstancesToUpdate(
	rollingUpdate *workloadsv1alpha1.InstanceSetUpdateStrategy,
	updateRevision string,
	replicas int32,
	instances []*workloadsv1alpha1.Instance,
) []int {
	var partition int32 = 0
	if rollingUpdate != nil && rollingUpdate.Partition != nil {
		// Convert IntOrString to int32
		partitionValue, err := intstrutil.GetValueFromIntOrPercent(
			rollingUpdate.Partition,
			int(replicas),
			false,
		)
		if err == nil {
			partition = int32(partitionValue)
		}
	}

	var updateIndexes []int
	for i := range instances {
		if instances[i] == nil {
			continue
		}
		ordinal := getOrdinal(instances[i])
		if ordinal >= int(partition) && getInstanceRevision(instances[i]) != updateRevision {
			updateIndexes = append(updateIndexes, i)
		}
	}

	// sort by ordinal in reverse order (start from the largest ordinal)
	sort.Slice(updateIndexes, func(i, j int) bool {
		return getOrdinal(instances[updateIndexes[i]]) > getOrdinal(instances[updateIndexes[j]])
	})

	return updateIndexes
}

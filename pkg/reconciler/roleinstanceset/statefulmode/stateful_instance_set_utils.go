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
	"fmt"
	"regexp"
	"strconv"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	portallocator "sigs.k8s.io/rbgs/pkg/port-allocator"
)

// statefulInstanceRegex is a regular expression that extracts the parent InstanceSet and ordinal from the Name of an Instance
var statefulInstanceRegex = regexp.MustCompile("(.*)-([0-9]+)$")

// getParentNameAndOrdinal gets the name of instance's parent InstanceSet and instance's ordinal as extracted from its Name.
func getParentNameAndOrdinal(instance *workloadsv1alpha2.RoleInstance) (string, int) {
	parent := ""
	ordinal := -1
	subMatches := statefulInstanceRegex.FindStringSubmatch(instance.Name)
	if len(subMatches) < 3 {
		return parent, ordinal
	}
	parent = subMatches[1]
	if i, err := strconv.ParseInt(subMatches[2], 10, 32); err == nil {
		ordinal = int(i)
	}
	return parent, ordinal
}

// getParentName gets the name of instance's parent InstanceSet.
func getParentName(instance *workloadsv1alpha2.RoleInstance) string {
	parent, _ := getParentNameAndOrdinal(instance)
	return parent
}

// getOrdinal gets instance's ordinal. If instance has no ordinal, -1 is returned.
func getOrdinal(instance *workloadsv1alpha2.RoleInstance) int {
	_, ordinal := getParentNameAndOrdinal(instance)
	return ordinal
}

func instanceInOrdinalRangeWithParams(instance *workloadsv1alpha2.RoleInstance, startOrdinal, endOrdinal int, reserveOrdinals sets.Set[int]) bool {
	ordinal := getOrdinal(instance)
	return ordinal >= startOrdinal && ordinal < endOrdinal &&
		!reserveOrdinals.Has(ordinal)
}

// getInstanceName gets the name of set's child Instance with an ordinal index of ordinal
func getInstanceName(set *workloadsv1alpha2.RoleInstanceSet, ordinal int) string {
	return fmt.Sprintf("%s-%d", set.Name, ordinal)
}

// isMemberOf tests if instance is a member of set.
func isMemberOf(set *workloadsv1alpha2.RoleInstanceSet, instance *workloadsv1alpha2.RoleInstance) bool {
	return getParentName(instance) == set.Name
}

// identityMatches returns true if instance has a valid identity for a member of set.
func identityMatches(set *workloadsv1alpha2.RoleInstanceSet, instance *workloadsv1alpha2.RoleInstance) bool {
	parent, ordinal := getParentNameAndOrdinal(instance)
	return ordinal >= 0 &&
		set.Name == parent &&
		instance.Name == getInstanceName(set, ordinal) &&
		instance.Namespace == set.Namespace &&
		instance.Labels[apps.StatefulSetPodNameLabel] == instance.Name
}

// updateIdentity updates instance's name and labels to conform to set's name
func updateIdentity(set *workloadsv1alpha2.RoleInstanceSet, instance *workloadsv1alpha2.RoleInstance) {
	ordinal := getOrdinal(instance)
	instance.Name = getInstanceName(set, ordinal)
	instance.Namespace = set.Namespace
	if instance.Labels == nil {
		instance.Labels = make(map[string]string)
	}
	identityLabels := map[string]string{
		apps.StatefulSetPodNameLabel:        instance.Name,
		constants.RoleInstanceIndexLabelKey: strconv.Itoa(ordinal),
	}
	for k, v := range identityLabels {
		instance.Labels[k] = v
	}
	injectIdentityLabelsIntoComponents(instance, identityLabels)
}

// injectIdentityLabelsIntoComponents writes the given identity labels into each
// component's pod template metadata, so that pods created from the template
// also carry the instance's ordinal identity labels.
func injectIdentityLabelsIntoComponents(instance *workloadsv1alpha2.RoleInstance, identityLabels map[string]string) {
	for i := range instance.Spec.Components {
		if instance.Spec.Components[i].Template.Labels == nil {
			instance.Spec.Components[i].Template.Labels = make(map[string]string)
		}
		for k, v := range identityLabels {
			instance.Spec.Components[i].Template.Labels[k] = v
		}
	}
}

// getInstanceSetKey returns the key for a InstanceSet
func getInstanceSetKey(set *workloadsv1alpha2.RoleInstanceSet) string {
	return fmt.Sprintf("%s/%s", set.Namespace, set.Name)
}

// getInstanceRevision gets the revision of Instance by inspecting the controller revision label.
func getInstanceRevision(instance *workloadsv1alpha2.RoleInstance) string {
	if instance.Labels == nil {
		return ""
	}
	return instance.Labels[apps.ControllerRevisionHashLabelKey]
}

// newVersionedInstance creates a new Instance for set with specific revision
func newVersionedInstance(
	currentSet, updateSet *workloadsv1alpha2.RoleInstanceSet,
	currentRevision, updateRevision string,
	ordinal int,
	_ []*workloadsv1alpha2.RoleInstance) *workloadsv1alpha2.RoleInstance {

	// Determine which set to use based on ordinal and update strategy
	useUpdateRevision := false
	setToUse := currentSet
	// Default partition is 0, meaning all instances should be updated
	var partition int32 = 0
	if currentSet.Spec.UpdateStrategy.Partition != nil && currentSet.Spec.Replicas != nil {
		// Convert IntOrString to int32
		partitionValue, err := intstrutil.GetValueFromIntOrPercent(
			currentSet.Spec.UpdateStrategy.Partition,
			int(*currentSet.Spec.Replicas),
			false,
		)
		if err == nil {
			partition = int32(partitionValue)
		}
	}
	// If ordinal >= partition, use update revision
	if ordinal >= int(partition) {
		useUpdateRevision = true
		setToUse = updateSet
	}

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
		},
		// Copy the RoleInstanceSpec from RoleInstanceTemplate
		Spec: setToUse.Spec.RoleInstanceTemplate.RoleInstanceSpec,
	}

	// Set basic identity
	instance.Name = getInstanceName(currentSet, ordinal)
	instance.Namespace = currentSet.Namespace

	// Copy all labels from InstanceSet to Instance (consistent with statelessmode)
	for k, v := range currentSet.Labels {
		instance.Labels[k] = v
	}
	// Also copy Selector.MatchLabels for backward compatibility
	if currentSet.Spec.Selector != nil && currentSet.Spec.Selector.MatchLabels != nil {
		for k, v := range currentSet.Spec.Selector.MatchLabels {
			instance.Labels[k] = v
		}
	}
	// Set revision label
	if useUpdateRevision {
		instance.Labels[apps.ControllerRevisionHashLabelKey] = updateRevision
	} else {
		instance.Labels[apps.ControllerRevisionHashLabelKey] = currentRevision
	}

	// Set identity labels and propagate them into each component's pod template.
	identityLabels := map[string]string{
		apps.StatefulSetPodNameLabel:        instance.Name,
		constants.RoleInstanceIndexLabelKey: strconv.Itoa(ordinal),
	}
	for k, v := range identityLabels {
		instance.Labels[k] = v
	}
	injectIdentityLabelsIntoComponents(instance, identityLabels)

	// Set owner reference
	instance.OwnerReferences = []metav1.OwnerReference{
		*metav1.NewControllerRef(currentSet, controllerKind),
	}

	// Propagate gang-scheduling annotation from RoleInstanceSet to RoleInstance so that
	// the instance controller can enforce gang-scheduling constraints without access to the RBG.
	if v, ok := currentSet.Annotations[constants.RoleInstanceGangSchedulingAnnotationKey]; ok {
		instance.Annotations[constants.RoleInstanceGangSchedulingAnnotationKey] = v
	}

	// Propagate in-place scheduling annotation from RoleInstanceSet to RoleInstance so that
	// the instance controller can inject nodeAffinity when recreating pods.
	if v, ok := currentSet.Annotations[constants.RoleInplaceSchedulingAnnotationKey]; ok {
		instance.Annotations[constants.RoleInplaceSchedulingAnnotationKey] = v
	}
	// Propagate in-place scheduling granularity annotation (Pod vs Component).
	if v, ok := currentSet.Annotations[constants.RoleInplaceSchedulingGranularityAnnotationKey]; ok {
		instance.Annotations[constants.RoleInplaceSchedulingGranularityAnnotationKey] = v
	}
	// Propagate in-place scheduling avoid annotation (hard-exclude nodes carrying a specific label).
	if v, ok := currentSet.Annotations[constants.RoleInplaceSchedulingAvoidAnnotationKey]; ok {
		instance.Annotations[constants.RoleInplaceSchedulingAvoidAnnotationKey] = v
	}

	portallocator.AllocatePortsForInstance(instance, currentSet)

	return instance
}

// topology captures all the rolling-update sizing decisions for a single
// reconcile pass. It is computed once at the top of updateStatefulInstanceSet
// and consulted by every later phase, so there is exactly one place that
// decides the in-range ordinal window.
type topology struct {
	// startOrdinal, endOrdinal define the [start, end) ordinal range that is
	// considered "in-range" this reconcile. Anything outside is condemned.
	// During a rollout endOrdinal may exceed replicas to provide a surge
	// buffer; outside of a rollout it equals replicas.
	startOrdinal int
	endOrdinal   int
	// reserveOrdinals are ordinals within [start, end) that should be skipped
	// when allocating slots. Always empty in the current implementation but
	// kept for future support of ordinal reservation.
	reserveOrdinals sets.Set[int]

	// replicas is the desired replica count from spec.
	replicas int
	// partition: ordinals < partition stay on currentRevision, ordinals >=
	// partition are eligible for rolling update.
	partition int
	// maxUnavailable is the budget for available -> unavailable transitions
	// during rolling update. Resolved from spec (default 10%, rounded down
	// when maxSurge > 0).
	maxUnavailable int
	// maxSurge is the upper bound on extra ordinals beyond replicas during a
	// rollout. Resolved from spec (default 0, rounded up).
	maxSurge int

	// activeSurge is the number of surge slots actually allocated this
	// reconcile. Always 0 when not in rollout. During rollout we size it as
	//   activeSurge = min(maxSurge, max(surgeNeeded, existingValidSurge))
	// where surgeNeeded buffers healthy-old base instances beyond
	// maxUnavailable, and existingValidSurge keeps already-created surge at
	// the current updateRev alive (sticky). Stale-revision surge does NOT
	// count toward existingValidSurge — it falls outside endOrdinal and gets
	// condemned in Phase B.
	activeSurge int
	// surgeStart equals replicas: surge ords occupy [surgeStart, endOrdinal).
	surgeStart int

	// inRollout is true when currentRev != updateRev and the rollout is not
	// paused. Used to gate surge allocation and to decide whether Phase C
	// runs at all.
	inRollout bool
}

// computeMaxSurge resolves spec.UpdateStrategy.MaxSurge against the desired
// replica count using the round-up convention (matching k8s Deployment).
// Returns 0 when MaxSurge is unset or Replicas is nil.
func computeMaxSurge(set *workloadsv1alpha2.RoleInstanceSet) (int, error) {
	if set.Spec.UpdateStrategy.MaxSurge == nil || set.Spec.Replicas == nil {
		return 0, nil
	}
	maxSurge, err := intstrutil.GetScaledValueFromIntOrPercent(
		set.Spec.UpdateStrategy.MaxSurge, int(*set.Spec.Replicas), true)
	if err != nil {
		return 0, err
	}
	if maxSurge < 0 {
		return 0, nil
	}
	return maxSurge, nil
}

// computeMaxUnavailable resolves spec.UpdateStrategy.MaxUnavailable against
// the desired replica count. When MaxSurge > 0 we round down (matching k8s
// Deployment when maxSurge is in play); otherwise we round up.
//
// As with k8s Deployment, when maxSurge == 0 the resolved maxUnavailable is
// floored to 1, otherwise the rolling update could not make progress.
func computeMaxUnavailable(set *workloadsv1alpha2.RoleInstanceSet, maxSurge int) (int, error) {
	if set.Spec.Replicas == nil {
		if set.Spec.UpdateStrategy.MaxUnavailable != nil {
			return 0, fmt.Errorf("spec.Replicas is nil")
		}
		return 0, nil
	}
	replicas := int(*set.Spec.Replicas)
	if set.Spec.UpdateStrategy.MaxUnavailable == nil {
		// Default: 10% of replicas. Match Deployment's rounding rule.
		def := intstrutil.FromString(workloadsv1alpha2.DefaultRoleInstanceSetMaxUnavailable)
		raw, err := intstrutil.GetScaledValueFromIntOrPercent(&def, replicas, maxSurge == 0)
		if err != nil {
			return 0, err
		}
		if maxSurge == 0 && raw < 1 {
			raw = 1
		}
		if raw < 0 {
			raw = 0
		}
		return raw, nil
	}
	raw, err := intstrutil.GetScaledValueFromIntOrPercent(
		set.Spec.UpdateStrategy.MaxUnavailable, replicas, maxSurge == 0)
	if err != nil {
		return 0, err
	}
	if maxSurge == 0 && raw < 1 {
		raw = 1
	}
	if raw < 0 {
		raw = 0
	}
	return raw, nil
}

// computePartition resolves spec.UpdateStrategy.Partition. Default 0 means
// "update all instances".
func computePartition(set *workloadsv1alpha2.RoleInstanceSet) (int, error) {
	if set.Spec.UpdateStrategy.Partition == nil || set.Spec.Replicas == nil {
		return 0, nil
	}
	p, err := intstrutil.GetValueFromIntOrPercent(
		set.Spec.UpdateStrategy.Partition, int(*set.Spec.Replicas), false)
	if err != nil {
		return 0, err
	}
	if p < 0 {
		return 0, nil
	}
	if p > int(*set.Spec.Replicas) {
		p = int(*set.Spec.Replicas)
	}
	return p, nil
}

// countHealthyOldInBase counts ordinals within [partition, replicas) where
// the instance is at the OLD revision (i.e. != updateRevision) AND currently
// healthy AND not terminating. These are the instances that need a surge
// buffer to be replaced without dropping availability — they are currently
// providing service and we must not delete them faster than maxUnavailable
// allows.
//
// Already-unavailable instances (unhealthy / terminating / missing) do NOT
// need a surge buffer because they are not contributing to availability;
// replacing them does not reduce availability further. This is the same
// observation behind k8s Deployment's cleanupUnhealthyReplicas.
func countHealthyOldInBase(
	instances []*workloadsv1alpha2.RoleInstance,
	partition int,
	replicas int,
	updateRevision string,
) int {
	if replicas <= partition {
		return 0
	}
	count := 0
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		ord := getOrdinal(inst)
		if ord < partition || ord >= replicas {
			continue
		}
		if getInstanceRevision(inst) == updateRevision {
			continue
		}
		if !isCreated(inst) || !isHealthy(inst) || isTerminating(inst) {
			continue
		}
		count++
	}
	return count
}

// countExistingValidSurge counts ordinals in [replicas, replicas+maxSurge)
// that hold an instance already at the CURRENT updateRevision and not
// terminating. We use this as a stickiness floor when sizing activeSurge —
// surge slots that have already been allocated for the current rollout
// should stay alive even if surgeNeeded shrinks (e.g. as healthy old base
// instances get updated).
//
// Critically: surge instances at a STALE revision (e.g. left over from a
// prior rollout that the user superseded mid-flight) are NOT counted here.
// They will fall outside endOrdinal and Phase B will condemn them — which is
// what fixes the "mid-rollout config change does not progress" bug.
func countExistingValidSurge(
	instances []*workloadsv1alpha2.RoleInstance,
	replicas int,
	maxSurge int,
	updateRevision string,
) int {
	if maxSurge <= 0 {
		return 0
	}
	count := 0
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		ord := getOrdinal(inst)
		if ord < replicas || ord >= replicas+maxSurge {
			continue
		}
		if getInstanceRevision(inst) != updateRevision {
			continue
		}
		if isTerminating(inst) {
			continue
		}
		count++
	}
	return count
}

// allBaseAtUpdateRevHealthy reports whether every ord in [partition, replicas)
// is fully done with respect to updateRevision: an instance exists, is at
// updateRev, isCreated + isHealthy + !isTerminating, and its
// ObservedGeneration has caught up to its Generation. Used as a "rollout
// effectively complete" signal for surge sticky-floor and (combined with a
// partition==0 guard) for advancing CurrentRevision.
func allBaseAtUpdateRevHealthy(
	instances []*workloadsv1alpha2.RoleInstance,
	partition, replicas int,
	updateRevision string,
) bool {
	if replicas <= partition {
		return true
	}
	seen := sets.New[int]()
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		ord := getOrdinal(inst)
		if ord < partition || ord >= replicas {
			continue
		}
		if getInstanceRevision(inst) != updateRevision {
			return false
		}
		if !isCreated(inst) || !isHealthy(inst) || isTerminating(inst) {
			return false
		}
		if inst.Status.ObservedGeneration < inst.Generation {
			return false
		}
		seen.Insert(ord)
	}
	// Every ord in [partition, replicas) must be present.
	return seen.Len() == replicas-partition
}

// computeTopology is the single source of truth for ordinal range sizing
// during a reconcile. See topology doc for field semantics. Errors only
// occur on malformed spec (negative percentages etc.).
//
// Sizing rules in one place:
//
//   - Outside a rollout (currentRev == updateRev): activeSurge = 0,
//     endOrdinal = replicas. Stale surge (if any from a finished rollout)
//     falls out of range and gets condemned.
//
//   - Paused mid-rollout (Paused=true && currentRev != updateRev): freeze the
//     existing surge in place. activeSurge = existingValidSurge (clamped to
//     maxSurge). New surge is NOT allocated (no surgeNeeded delta), but
//     in-flight surge slots stay inside endOrdinal so Phase B does not condemn
//     them. Pod startup is expensive; throwing away surge on pause and
//     re-creating it on unpause would waste minutes of GPU time.
//
//   - Inside a rollout, with work still pending in [partition, replicas):
//     activeSurge = min(maxSurge, max(surgeNeeded, existingValidSurge)).
//     surgeNeeded = max(0, healthyOldInBase - maxUnavailable). The max with
//     existingValidSurge keeps already-allocated surge sticky so we don't
//     thrash by creating surge → letting healthy-old shrink → condemning surge.
//
//   - Inside a rollout, but all base ords [partition, replicas) are already at
//     updateRev healthy: surge stickiness is dropped and activeSurge collapses
//     to surgeNeeded (which is 0 in that state). Surge slots fall out of
//     endOrdinal and Phase B condemns them. This matters when partition > 0
//     because CurrentRevision will not advance (per the partition>0 guard in
//     shouldAdvanceCurrentRevision), so the next-reconcile "inRollout=false →
//     drop surge" path would otherwise never trigger.
func computeTopology(
	set *workloadsv1alpha2.RoleInstanceSet,
	instances []*workloadsv1alpha2.RoleInstance,
	currentRevision, updateRevision string,
) (topology, error) {
	t := topology{
		startOrdinal:    0,
		reserveOrdinals: sets.New[int](),
	}
	if set.Spec.Replicas != nil {
		t.replicas = int(*set.Spec.Replicas)
	}
	t.surgeStart = t.replicas
	t.endOrdinal = t.replicas

	maxSurge, err := computeMaxSurge(set)
	if err != nil {
		return t, err
	}
	t.maxSurge = maxSurge

	maxUnav, err := computeMaxUnavailable(set, maxSurge)
	if err != nil {
		return t, err
	}
	t.maxUnavailable = maxUnav

	partition, err := computePartition(set)
	if err != nil {
		return t, err
	}
	t.partition = partition

	t.inRollout = currentRevision != updateRevision && !set.Spec.UpdateStrategy.Paused
	if maxSurge == 0 {
		return t, nil
	}
	// Paused mid-rollout: preserve existing surge slots so Phase B does not
	// condemn them. We do NOT allocate new surge here — surgeNeeded is only
	// considered when actively rolling.
	if !t.inRollout {
		if set.Spec.UpdateStrategy.Paused && currentRevision != updateRevision {
			existing := countExistingValidSurge(instances, t.replicas, maxSurge, updateRevision)
			if existing > maxSurge {
				existing = maxSurge
			}
			t.activeSurge = existing
			t.endOrdinal = t.replicas + existing
		}
		return t, nil
	}

	healthyOld := countHealthyOldInBase(instances, partition, t.replicas, updateRevision)
	surgeNeeded := healthyOld - maxUnav
	if surgeNeeded < 0 {
		surgeNeeded = 0
	}

	active := surgeNeeded
	// Stickiness only applies while there is still work to do in
	// [partition, replicas). Once that range is fully at updateRev healthy,
	// drop the existing-surge floor so surge ramps back down.
	if !allBaseAtUpdateRevHealthy(instances, partition, t.replicas, updateRevision) {
		existing := countExistingValidSurge(instances, t.replicas, maxSurge, updateRevision)
		if existing > active {
			active = existing
		}
	}
	if active > maxSurge {
		active = maxSurge
	}
	t.activeSurge = active
	t.endOrdinal = t.replicas + active
	return t, nil
}

// isHealthy returns true if instance is healthy
func isHealthy(instance *workloadsv1alpha2.RoleInstance) bool {
	// Check if instance is ready
	for _, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

// isCreated returns true if instance has been created
func isCreated(instance *workloadsv1alpha2.RoleInstance) bool {
	return instance != nil && instance.UID != ""
}

// isTerminating returns true if instance is being terminated
func isTerminating(instance *workloadsv1alpha2.RoleInstance) bool {
	return instance != nil && instance.DeletionTimestamp != nil
}

// isRunningAndReady returns true if instance is running and ready
func isRunningAndReady(instance *workloadsv1alpha2.RoleInstance) bool {
	return isHealthy(instance) && !isTerminating(instance)
}

// isInstanceRunningAndAvailable returns true if instance is running and available, and the duration since it became available
func isInstanceRunningAndAvailable(instance *workloadsv1alpha2.RoleInstance, minReadySeconds int32) (bool, time.Duration) {
	if !isRunningAndReady(instance) {
		return false, 0
	}

	for _, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == v1.ConditionTrue {
			availableFor := metav1.Now().Sub(cond.LastTransitionTime.Time)
			minDuration := time.Duration(minReadySeconds) * time.Second
			if availableFor < minDuration {
				return false, minDuration - availableFor
			}
			return true, 0
		}
	}

	return false, 0
}

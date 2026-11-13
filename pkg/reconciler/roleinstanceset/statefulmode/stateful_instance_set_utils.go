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
	"sort"
	"strconv"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/api/workloads/ordinals"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	inplaceutil "sigs.k8s.io/rbgs/pkg/inplace/instance"
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
	inplaceutil.InjectIdentityLabelsIntoComponents(instance)
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
func newVersionedInstance(currentSet, updateSet *workloadsv1alpha2.RoleInstanceSet, currentRevision, updateRevision string, ordinal, position int) *workloadsv1alpha2.RoleInstance {

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
	// Partition selects positions in committed membership, not numeric ordinals.
	if position >= int(partition) {
		useUpdateRevision = true
		setToUse = updateSet
	}

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
		},
		// Deep copied, not assigned: a struct assignment shares the Components slice
		// with the set's template, so the identity labels written into one instance's
		// pod template below would land in every other instance built from the same
		// template in this reconcile, and the highest ordinal would win.
		Spec: *setToUse.Spec.RoleInstanceTemplate.RoleInstanceSpec.DeepCopy(),
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
	inplaceutil.InjectIdentityLabelsIntoComponents(instance)

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
	// Propagate in-place update grace period seconds so the RoleInstance controller
	// can honour the configured drain delay when updating Pods.
	if v, ok := currentSet.Annotations[constants.RoleInplaceUpdateGracePeriodSecondsKey]; ok {
		instance.Annotations[constants.RoleInplaceUpdateGracePeriodSecondsKey] = v
	}

	portallocator.AllocatePortsForInstance(instance, currentSet)

	return instance
}

// topology captures all the rolling-update sizing decisions for a single
// reconcile pass. It is computed once at the top of updateStatefulInstanceSet
// and consulted by every later phase, so there is exactly one place that
// decides the in-range ordinal window.
type topology struct {
	// All range fields below are compact slot positions. ordinals maps each
	// position to its real identity; memory never depends on the largest ordinal.
	ordinals []int
	// startOrdinal, endOrdinal define the [start, end) slot positions that are
	// considered in range this reconcile. Unlisted identities are condemned.
	// During a rollout endOrdinal may exceed replicas to provide a surge
	// buffer; outside of a rollout it equals replicas.
	startOrdinal int
	endOrdinal   int

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

	// inRollout is true when update work remains and the rollout is not paused.
	// Update work includes a revision mismatch, a stale base instance, or an
	// in-flight early-rollback replacement. Used to gate surge allocation and to
	// decide whether Phase C runs at all.
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
	members []int,
) int {
	if replicas <= partition {
		return 0
	}
	count := 0
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		ord := memberPosition(getOrdinal(inst), members)
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

// existingValidSurgeOrdinals identifies created, nonterminating update-revision
// surge without expanding a candidate window. Exclusions can put a valid surge
// arbitrarily far above the base. Retain observed identities even after E is
// edited or removed, with the count still bounded by maxSurge.
func existingValidSurgeOrdinals(
	instances []*workloadsv1alpha2.RoleInstance,
	replicas, maxSurge int,
	updateRevision string,
	skips ordinals.Ranges,
) []int {
	if maxSurge <= 0 {
		return nil
	}
	existing := sets.New[int]()
	for _, inst := range instances {
		if !isCreated(inst) || isTerminating(inst) || getInstanceRevision(inst) != updateRevision {
			continue
		}
		ordinal := getOrdinal(inst)
		position, allowed := skips.Position(ordinal)
		if allowed && position >= replicas {
			existing.Insert(ordinal)
		}
	}
	result := sets.List(existing)
	return result[:min(len(result), maxSurge)]
}

// materializeSurge keeps selected existing identities before allocating only
// the additional slots needed. The sorted layout is compact regardless of the
// configured maximum or the size of ordinal gaps.
func (t *topology) materializeSurge(skips, exclusions ordinals.Ranges, existing []int) error {
	if t.activeSurge == 0 {
		return nil
	}
	if int64(t.replicas)+int64(t.activeSurge) > int64(ordinals.MaxOrdinal)+1 {
		return fmt.Errorf("active replica and surge count exceeds ordinal space")
	}
	surge := append([]int(nil), existing[:min(len(existing), t.activeSurge)]...)
	used := sets.New(surge...)
	next := 0
	if t.replicas > 0 {
		next = t.ordinals[t.replicas-1] + 1
	}
	for len(surge) < t.activeSurge {
		ordinal, err := nextSurgeOrdinal(skips, exclusions, next)
		if err != nil {
			return err
		}
		next = ordinal + 1
		if used.Has(ordinal) {
			continue
		}
		used.Insert(ordinal)
		surge = append(surge, ordinal)
	}
	sort.Ints(surge)
	t.ordinals = append(t.ordinals, surge...)
	t.endOrdinal = t.replicas + t.activeSurge
	return nil
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
	members []int,
) bool {
	if replicas <= partition {
		return true
	}
	seen := sets.New[int]()
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		ord := memberPosition(getOrdinal(inst), members)
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

// hasStaleBaseInstance reports whether a base instance in the update range is
// still at a revision other than updateRevision. Terminating instances are
// included so an early rollback remains pending while the stale instance is
// being deleted. This catches A -> B -> A, where CurrentRevision and
// UpdateRevision can have the same name while the live instances are still at B.
func hasStaleBaseInstance(
	instances []*workloadsv1alpha2.RoleInstance,
	partition, replicas int,
	updateRevision string,
	members []int,
) bool {
	if replicas <= partition {
		return false
	}
	for _, inst := range instances {
		if inst == nil || !isCreated(inst) {
			continue
		}
		ord := memberPosition(getOrdinal(inst), members)
		if ord < partition || ord >= replicas {
			continue
		}
		if getInstanceRevision(inst) != updateRevision {
			return true
		}
	}
	return false
}

// nextSurgeOrdinal jumps exclusion intervals without materializing their union.
func nextSurgeOrdinal(skips, exclusions ordinals.Ranges, start int) (int, error) {
	for {
		candidate, err := skips.Next(start)
		if err != nil {
			return 0, err
		}
		next, err := exclusions.Next(candidate)
		if err != nil {
			return 0, err
		}
		if next == candidate {
			return candidate, nil
		}
		start = next
	}
}

// rolloutReplacementStateActive conservatively checks the whole current update
// range while replacement intent for this revision is persisted. The recorded
// positional bounds cannot identify the original members after gap filling:
// a saved position may now refer to a different, healthy instance. Keeping the
// current range protected can retain surge longer, but cannot release it while
// a desired replacement is missing or unready. No new status fields are needed.
// This function does not mutate the informer snapshot or status.
func rolloutReplacementStateActive(
	set *workloadsv1alpha2.RoleInstanceSet,
	instances []*workloadsv1alpha2.RoleInstance,
	partition, replicas int,
	updateRevision string,
) bool {
	if set == nil || set.Status.RolloutReplacementState == nil {
		return false
	}
	state := set.Status.RolloutReplacementState
	if state.UpdateRevision != updateRevision {
		return false
	}
	members, err := set.CommittedOrdinals()
	if err != nil {
		return true
	} // computeTopology reports the invalid record before acting.
	return !allBaseAtUpdateRevHealthy(instances, partition, replicas, updateRevision, members)
}

// computeTopology is the single source of truth for ordinal range sizing
// during a reconcile. See topology doc for field semantics. Errors only
// occur on malformed spec (negative percentages etc.). The caller supplies
// whether early-rollback replacement work is active so this function remains
// free of global-map side effects.
//
// Sizing rules in one place:
//
//   - Outside a rollout (no revision work, stale base, or in-flight early
//     rollback replacement): activeSurge = 0, endOrdinal = replicas. Stale
//     surge (if any from a finished rollout) falls out of range and gets
//     condemned.
//
//   - Paused mid-rollout (Paused=true && update work remains): freeze the
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
	earlyRollbackReplacement bool,
) (topology, error) {
	t := topology{}
	if set.Spec.Replicas != nil {
		t.replicas = int(*set.Spec.Replicas)
	}
	t.surgeStart = t.replicas
	t.endOrdinal = t.replicas
	skips, err := ordinals.Parse(set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
	if err != nil {
		return t, fmt.Errorf("invalid committed skip ordinals: %w", err)
	}
	t.ordinals, err = skips.Members(t.replicas)
	if err != nil {
		return t, err
	}

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

	staleBase := hasStaleBaseInstance(instances, t.partition, t.replicas, updateRevision, t.ordinals)
	updatePending := currentRevision != updateRevision || staleBase || earlyRollbackReplacement
	t.inRollout = updatePending && !set.Spec.UpdateStrategy.Paused
	if maxSurge == 0 || !updatePending {
		return t, nil
	}
	exclusions, err := ordinals.Parse(set.Annotations[constants.ExcludedOrdinalsAnnotationKey])
	if err != nil {
		return t, fmt.Errorf("invalid surge exclusions: %w", err)
	}
	existing := existingValidSurgeOrdinals(instances, t.replicas, maxSurge, updateRevision, skips)
	// Pause preserves observed identities only; it never allocates new slots.
	if !t.inRollout {
		t.activeSurge = len(existing)
		err = t.materializeSurge(skips, exclusions, existing)
		return t, err
	}

	healthyOld := countHealthyOldInBase(instances, partition, t.replicas, updateRevision, t.ordinals)
	active := max(0, healthyOld-maxUnav)
	// Retain existing surge only while an eligible base member is unfinished.
	if !allBaseAtUpdateRevHealthy(instances, partition, t.replicas, updateRevision, t.ordinals) {
		active = max(active, len(existing))
	}
	t.activeSurge = min(active, maxSurge)
	err = t.materializeSurge(skips, exclusions, existing)
	return t, err
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

// memberPosition returns -1 for identities not in a supplied layout. Dense
// helpers can use a nil layout; production topology supplies it explicitly.
func memberPosition(ordinal int, members []int) int {
	if members == nil {
		return ordinal
	}
	i := sort.SearchInts(members, ordinal)
	if i < len(members) && members[i] == ordinal {
		return i
	}
	return -1
}

func (t topology) position(ordinal int) int { return memberPosition(ordinal, t.ordinals) }
func (t topology) ordinal(position int) int {
	if t.ordinals == nil {
		return position
	}
	return t.ordinals[position]
}

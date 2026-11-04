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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestSparseMembershipRecoveryAndPartition(t *testing.T) {
	h := newRollbackTestHarness(t, "sparse-recovery", 3, 2, 0)
	h.set.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1,3"}
	zero := h.instance(0, h.current.Name, true)
	four := h.instance(4, h.current.Name, true)
	// Missing desired member 2 is recovered, even though its actual ordinal is
	// numerically equal to partition. Its position 1 keeps it on currentRevision.
	status, err := h.control.updateStatefulInstanceSet(context.Background(), h.set, h.current, h.intermediate, 0, []*workloadsv1alpha2.RoleInstance{zero, four}, []*apps.ControllerRevision{h.current, h.intermediate})
	require.NoError(t, err)
	require.Equal(t, []string{"sparse-recovery-2"}, h.objects.created)
	require.Equal(t, int32(1), status.ExpectedUpdatedReplicas)
	currentSet, err := ApplyRevision(h.set, h.current)
	require.NoError(t, err)
	updateSet, err := ApplyRevision(h.set, h.intermediate)
	require.NoError(t, err)
	restored := newVersionedInstance(currentSet, updateSet, h.current.Name, h.intermediate.Name, 2, 1)
	require.Equal(t, h.current.Name, getInstanceRevision(restored))
	newest := newVersionedInstance(currentSet, updateSet, h.current.Name, h.intermediate.Name, 4, 2)
	require.Equal(t, h.intermediate.Name, getInstanceRevision(newest))
	require.Equal(t, "4", newest.Labels[constants.RoleInstanceIndexLabelKey])
}

func TestSparseScaleDownRetainsWarmSurvivors(t *testing.T) {
	h := newRollbackTestHarness(t, "sparse-scale", 3, 0, 0)
	h.set.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1,3"}
	var instances []*workloadsv1alpha2.RoleInstance
	for i := 0; i < 5; i++ {
		instances = append(instances, h.instance(i, h.current.Name, true))
	}
	h.reconcile(t, instances...)
	h.assertDeleted(t, "sparse-scale-3", "sparse-scale-1")
	require.Empty(t, h.objects.created, "must preserve the accepted high-ordinal survivor")
}

func TestSparseSurgeBudgetPauseAndCompletion(t *testing.T) {
	h := newRollbackTestHarness(t, "sparse-rollout", 3, 2, 1)
	h.set.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1,3"}
	zero := h.instance(0, h.current.Name, true)
	two := h.instance(2, h.current.Name, true)
	four := h.instance(4, h.current.Name, true)
	instances := []*workloadsv1alpha2.RoleInstance{zero, two, four}
	topo, err := computeTopology(h.set, instances, h.current.Name, h.intermediate.Name, false)
	require.NoError(t, err)
	require.Equal(t, 5, topo.ordinal(3))
	require.Equal(t, []int{2}, buildUpdateTargets(instances, topo, h.intermediate.Name))
	// Actual ordinal 4 is a base member, not free surge. Without a ready surge,
	// maxUnavailable=0 must keep it alive.
	h.set.Spec.UpdateStrategy.MaxSurge = ptr.To(intstr.FromInt32(0))
	// Use the previously computed budget (maxUnavailable=0) to avoid the existing
	// maxSurge=0 default floor of 1, which is intentionally unchanged.
	_, err = h.control.progressUpdate(context.Background(), h.set, &h.set.Status, h.current, h.intermediate, []*apps.ControllerRevision{h.current, h.intermediate}, instances, instances, 0, topo)
	require.NoError(t, err)
	h.assertDeleted(t)
	h.set.Spec.UpdateStrategy.MaxSurge = ptr.To(intstr.FromInt32(1))
	surge := h.instance(5, h.intermediate.Name, true)
	withSurge := append(instances, surge)
	_, err = h.control.progressUpdate(context.Background(), h.set, &h.set.Status, h.current, h.intermediate, []*apps.ControllerRevision{h.current, h.intermediate}, withSurge, withSurge, 0, topo)
	require.NoError(t, err)
	h.assertDeleted(t, four.Name)
	// Pausing must preserve an existing legal surge identity without creating one.
	h.set.Spec.UpdateStrategy.Paused = true
	paused, err := computeTopology(h.set, withSurge, h.current.Name, h.intermediate.Name, false)
	require.NoError(t, err)
	require.Equal(t, 1, paused.activeSurge)
	require.Equal(t, 5, paused.ordinal(3))
	require.False(t, paused.inRollout)
	// Fully updated eligible base members collapse surge, while protected 0 and 2
	// stay at the old revision and CurrentRevision must not advance.
	h.set.Spec.UpdateStrategy.Paused = false
	four.Labels[apps.ControllerRevisionHashLabelKey] = h.intermediate.Name
	done, err := computeTopology(h.set, withSurge, h.current.Name, h.intermediate.Name, false)
	require.NoError(t, err)
	require.Zero(t, done.activeSurge)
	require.False(t, shouldAdvanceCurrentRevision(h.set, instances, h.intermediate.Name, done))
}

func TestSparseEarlyRollbackRetainsReplacementScope(t *testing.T) {
	h := newRollbackTestHarness(t, "sparse-rollback", 3, 2, 1)
	h.set.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1,3"}
	instances := []*workloadsv1alpha2.RoleInstance{h.instance(0, h.current.Name, true), h.instance(2, h.current.Name, true), h.instance(4, h.intermediate.Name, true)}
	h.reconcile(t, instances...)
	require.Equal(t, []string{"sparse-rollback-5"}, h.objects.created)
	require.Empty(t, h.objects.deleted)
	require.NoError(t, h.control.persistRolloutReplacementState(context.Background(), h.set, &h.set.Status, h.current.Name, 4))
	require.Equal(t, int32(2), h.set.Status.RolloutReplacementState.Partition)
	// Missing high ordinal 4 remains a replacement in logical position 2.
	topo, err := computeTopologyForReconcile(h.set, append(instances[:2:2], h.instance(5, h.current.Name, true)), h.current.Name, h.current.Name)
	require.NoError(t, err)
	require.True(t, topo.inRollout)
	require.Equal(t, 1, topo.activeSurge)
}

func TestSparseLargeGapsDoNotAllocateByOrdinal(t *testing.T) {
	set := buildSet("sparse-large", 2, nil, nil)
	set.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "0-2147483645"}
	topo, err := computeTopology(set, nil, "rev", "rev", false)
	require.NoError(t, err)
	require.Len(t, topo.ordinals, 2)
	require.Equal(t, 2147483646, topo.ordinal(0))
	require.Equal(t, 2147483647, topo.ordinal(1))
	set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] = "0-2147483647"
	_, err = computeTopology(set, nil, "rev", "rev", false)
	require.Error(t, err)
}

func TestSparseSurgeWaitsForMinReadySeconds(t *testing.T) {
	h := newRollbackTestHarness(t, "sparse-min-ready", 3, 2, 1)
	h.set.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1,3"}
	h.set.Spec.MinReadySeconds = 30
	var instances []*workloadsv1alpha2.RoleInstance
	for _, ordinal := range []int{0, 2, 4} {
		inst := h.instance(ordinal, h.current.Name, true)
		inst.Status.Conditions[0].LastTransitionTime = metav1.NewTime(time.Now().Add(-time.Minute))
		instances = append(instances, inst)
	}
	surge := h.instance(5, h.intermediate.Name, true)
	instances = append(instances, surge)
	topo, err := computeTopology(h.set, instances, h.current.Name, h.intermediate.Name, false)
	require.NoError(t, err)
	_, err = h.control.progressUpdate(context.Background(), h.set, &h.set.Status, h.current, h.intermediate, []*apps.ControllerRevision{h.current, h.intermediate}, instances, instances, 30, topo)
	require.NoError(t, err)
	h.assertDeleted(t)
	require.Positive(t, durationStore.Pop(getInstanceSetKey(h.set)))
	surge.Status.Conditions[0].LastTransitionTime = metav1.NewTime(time.Now().Add(-time.Minute))
	_, err = h.control.progressUpdate(context.Background(), h.set, &h.set.Status, h.current, h.intermediate, []*apps.ControllerRevision{h.current, h.intermediate}, instances, instances, 30, topo)
	require.NoError(t, err)
	h.assertDeleted(t, h.set.Name+"-4")
}

func TestSparsePausePreservesNonPrefixSurgeIdentity(t *testing.T) {
	h := newRollbackTestHarness(t, "sparse-paused-gap", 3, 0, 2)
	h.set.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1,3"}
	h.set.Spec.UpdateStrategy.Paused = true
	instances := []*workloadsv1alpha2.RoleInstance{h.instance(0, h.current.Name, true), h.instance(2, h.current.Name, true), h.instance(4, h.current.Name, true), h.instance(6, h.intermediate.Name, true)}
	topo, err := computeTopology(h.set, instances, h.current.Name, h.intermediate.Name, false)
	require.NoError(t, err)
	require.Equal(t, 1, topo.activeSurge)
	require.Equal(t, 6, topo.ordinal(3))
	_, err = h.control.updateStatefulInstanceSet(context.Background(), h.set, h.current, h.intermediate, 0, instances, []*apps.ControllerRevision{h.current, h.intermediate})
	require.NoError(t, err)
	require.Empty(t, h.objects.created, "pause cannot fill the missing surge ordinal 5")
	h.assertDeleted(t)
}

func TestSparseMaxOrdinalUnhealthyOrdering(t *testing.T) {
	instance := buildInst("max-ordinal", 2147483647, "revision", false, true)
	count, first := countUnhealthy([]*workloadsv1alpha2.RoleInstance{instance}, nil)
	require.Equal(t, 1, count)
	require.Same(t, instance, first, "the largest valid ordinal must still be an OrderedReady blocker")
}

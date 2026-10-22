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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// rollbackTestHarness runs the real reconcile phases with A as both the
// current revision and the rollback target, and B as the interrupted rollout.
// Only API writes are replaced with recording fakes.
type rollbackTestHarness struct {
	set                   *workloadsv1alpha2.RoleInstanceSet
	current, intermediate *apps.ControllerRevision
	control               *defaultStatefulInstanceSetControl
	objects               *fakeInstanceObjectManager
	updater               *fakeStatefulStatusUpdater
}

func newRollbackTestHarness(t *testing.T, name string, replicas, partition, surge int32) *rollbackTestHarness {
	t.Helper()
	resetInstanceUnhealthySince()
	t.Cleanup(resetInstanceUnhealthySince)
	set := newRevisionTestSet("nginx:1.0")
	set.Name, set.UID = name, types.UID(name)
	set.Spec.Replicas = ptr.To(replicas)
	set.Spec.PodManagementPolicy = constants.ParallelPodManagement
	set.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}
	set.Spec.UpdateStrategy.Type = workloadsv1alpha2.RecreatePodUpdateStrategyType
	set.Spec.UpdateStrategy.Partition = ptr.To(intstr.FromInt32(partition))
	set.Spec.UpdateStrategy.MaxSurge = ptr.To(intstr.FromInt32(surge))
	set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(0))
	current, err := newRevision(set, 1, ptr.To[int32](0))
	if err != nil {
		t.Fatal(err)
	}
	set.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Containers[0].Image = "nginx:2.0"
	intermediate, err := newRevision(set, 2, ptr.To[int32](0))
	if err != nil {
		t.Fatal(err)
	}
	set.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Containers[0].Image = "nginx:1.0"
	set.Status.CurrentRevision, set.Status.UpdateRevision = current.Name, intermediate.Name
	objects := &fakeInstanceObjectManager{}
	updater := &fakeStatefulStatusUpdater{}
	recorder := record.NewFakeRecorder(64)
	control := &defaultStatefulInstanceSetControl{
		instanceControl: NewStatefulInstanceControlFromManager(objects, recorder),
		inplaceControl:  &fakeInplaceControl{},
		statusUpdater:   updater,
		recorder:        recorder,
	}
	key := getInstanceSetKey(set)
	durationStore.Pop(key)
	t.Cleanup(func() {
		durationStore.Pop(key)
		updateExpectations.DeleteExpectations(key)
	})
	return &rollbackTestHarness{set: set, current: current, intermediate: intermediate,
		control: control, objects: objects, updater: updater}
}

func (h *rollbackTestHarness) instance(ordinal int, revision string, ready bool) *workloadsv1alpha2.RoleInstance {
	return buildInst(h.set.Name, ordinal, revision, ready, true)
}

func (h *rollbackTestHarness) reconcile(t *testing.T, instances ...*workloadsv1alpha2.RoleInstance) {
	t.Helper()
	h.objects.created, h.objects.deleted = nil, nil
	status, err := h.control.updateStatefulInstanceSet(context.Background(), h.set, h.current, h.current, 0,
		instances, []*apps.ControllerRevision{h.current, h.intermediate})
	if err != nil {
		t.Fatalf("updateStatefulInstanceSet() error = %v", err)
	}
	h.set.Status = *status.DeepCopy()
}

func (h *rollbackTestHarness) assertDeleted(t *testing.T, names ...string) {
	t.Helper()
	if !sets.New(h.objects.deleted...).Equal(sets.New(names...)) {
		t.Fatalf("deleted = %v, want %v", h.objects.deleted, names)
	}
}

func TestEarlyRollbackPersistsBeforeInPlaceUpdate(t *testing.T) {
	for _, tt := range []struct {
		name       string
		persistErr error
	}{
		{name: "persist before in-place update"},
		{name: "failed persistence prevents in-place update", persistErr: errors.New("status write failed")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "rollback-in-place", 2, 1, 1)
			h.set.Spec.UpdateStrategy.Type = workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType
			h.updater.persistErr = tt.persistErr
			target := h.instance(1, h.intermediate.Name, true)
			status := &workloadsv1alpha2.RoleInstanceSetStatus{}
			expected := &workloadsv1alpha2.RolloutReplacementState{
				UpdateRevision: h.current.Name, Replicas: 2, Partition: 1,
			}
			inplace := &fakeInplaceControl{
				canUpdateInPlace: true,
				beforeUpdate: func() {
					require.Equal(t, 1, h.updater.persistCalls, "intent must be written before the in-place API call")
					require.Equal(t, expected, h.set.Status.RolloutReplacementState)
				},
			}
			h.control.inplaceControl = inplace

			_, err := h.control.applyTargetUpdate(context.Background(), h.set, status, target,
				h.current, h.current, []*apps.ControllerRevision{h.current, h.intermediate}, false, false)
			require.ErrorIs(t, err, tt.persistErr)
			require.Equal(t, 1, h.updater.persistCalls)
			h.assertDeleted(t)
			if tt.persistErr != nil {
				require.Zero(t, inplace.canUpdateCalls)
				require.Zero(t, inplace.updateCalls, "failed persistence must leave the instance untouched")
				require.Nil(t, h.set.Status.RolloutReplacementState)
				require.Nil(t, status.RolloutReplacementState)
				return
			}
			require.Equal(t, 1, inplace.canUpdateCalls)
			require.Equal(t, 1, inplace.updateCalls, "the test must execute the in-place update path")
			require.Equal(t, expected, status.RolloutReplacementState)
		})
	}
}

func TestEarlyRollbackKeepsSurgeAfterPartitionDecreases(t *testing.T) {
	h := newRollbackTestHarness(t, "rollback-partition", 2, 1, 1)
	base0 := h.instance(0, h.current.Name, true)
	base1 := h.instance(1, h.intermediate.Name, true)
	surge := h.instance(2, h.current.Name, true)
	h.reconcile(t, base0, base1, surge)
	h.assertDeleted(t, base1.Name)

	// A coordinated rollout may lower partition while the old ordinal 1
	// replacement remains unready. Its overlap with the new range is protected.
	h.set.Spec.UpdateStrategy.Partition = ptr.To(intstr.FromInt32(0))
	h.set.Generation++
	replacement := h.instance(1, h.current.Name, false)
	h.reconcile(t, base0, replacement, surge)
	h.assertDeleted(t)
	if h.set.Status.RolloutReplacementState == nil {
		t.Fatal("partition decrease discarded an unfinished replacement")
	}

	replacement.Status.Conditions[0].Status = v1.ConditionTrue
	h.reconcile(t, base0, replacement, surge)
	h.assertDeleted(t, surge.Name)
	if h.set.Status.RolloutReplacementState != nil {
		t.Fatal("completed replacement retained its protection")
	}
}

func TestEarlyRollbackRetainsFormerBaseAsSurgeAfterScaleDown(t *testing.T) {
	h := newRollbackTestHarness(t, "rollback-scale-down", 3, 0, 1)
	base0 := h.instance(0, h.current.Name, true)
	base1 := h.instance(1, h.intermediate.Name, true)
	base2 := h.instance(2, h.current.Name, true)
	surge := h.instance(3, h.current.Name, true)
	h.reconcile(t, base0, base1, base2, surge)
	h.assertDeleted(t, base1.Name)

	h.set.Spec.Replicas = ptr.To[int32](2)
	h.set.Generation++
	replacement := h.instance(1, h.current.Name, false)
	h.reconcile(t, base0, replacement, base2, surge)
	// Ordinal 2 is now within the permitted surge slot; only ordinal 3 is
	// beyond the new replicas + maxSurge bound and can be removed safely.
	h.assertDeleted(t, surge.Name)
	if h.set.Status.RolloutReplacementState == nil {
		t.Fatal("scale-down discarded an unfinished in-range replacement")
	}

	replacement.Status.Conditions[0].Status = v1.ConditionTrue
	h.reconcile(t, base0, replacement, base2)
	h.assertDeleted(t, base2.Name)
	if h.set.Status.RolloutReplacementState != nil {
		t.Fatal("completed replacement retained the former base")
	}
}

func TestEarlyRollbackProtectsNewReplacementsAfterPartitionDecreases(t *testing.T) {
	h := newRollbackTestHarness(t, "rollback-new-range", 2, 1, 2)
	base0 := h.instance(0, h.intermediate.Name, true)
	base1 := h.instance(1, h.intermediate.Name, true)
	surge2 := h.instance(2, h.current.Name, true)
	surge3 := h.instance(3, h.current.Name, true)
	h.reconcile(t, base0, base1, surge2, surge3)
	h.assertDeleted(t, base1.Name)

	// Two ready surge instances leave enough budget to start another
	// replacement in the newly opened range before ordinal 1 becomes ready.
	h.set.Spec.UpdateStrategy.Partition = ptr.To(intstr.FromInt32(0))
	h.set.Generation++
	replacement1 := h.instance(1, h.current.Name, false)
	h.reconcile(t, base0, replacement1, surge2, surge3)
	h.assertDeleted(t, base0.Name)

	// Finishing the original scope must not release the surge while the
	// replacement that started after partition changed is still unready.
	replacement0 := h.instance(0, h.current.Name, false)
	replacement1.Status.Conditions[0].Status = v1.ConditionTrue
	h.reconcile(t, replacement0, replacement1, surge2, surge3)
	h.assertDeleted(t)
	if h.set.Status.RolloutReplacementState == nil {
		t.Fatal("new replacement was not included in persisted protection")
	}

	replacement0.Status.Conditions[0].Status = v1.ConditionTrue
	h.reconcile(t, replacement0, replacement1, surge2, surge3)
	h.assertDeleted(t, surge2.Name, surge3.Name)
}

func TestEarlyRollbackProtectsNewReplacementsAfterScaleUp(t *testing.T) {
	h := newRollbackTestHarness(t, "rollback-scale-up", 2, 1, 2)
	// Recover an unfinished ordinal 1 replacement from the previously persisted
	// scope. Scale-up alone does not extend it; replacing ordinal 2 must do so.
	h.set.Status.UpdateRevision = h.current.Name
	h.set.Status.RolloutReplacementState = &workloadsv1alpha2.RolloutReplacementState{
		UpdateRevision: h.current.Name, Replicas: 2, Partition: 1,
	}
	h.set.Spec.Replicas = ptr.To[int32](3)
	h.set.Generation++
	base0 := h.instance(0, h.current.Name, true)
	replacement1 := h.instance(1, h.current.Name, false)
	base2 := h.instance(2, h.intermediate.Name, true)
	surge3 := h.instance(3, h.current.Name, true)
	surge4 := h.instance(4, h.current.Name, true)
	expected := &workloadsv1alpha2.RolloutReplacementState{
		UpdateRevision: h.current.Name, Replicas: 3, Partition: 1,
	}
	h.objects.beforeDelete = func(instance *workloadsv1alpha2.RoleInstance) error {
		if instance.Name == base2.Name {
			require.Equal(t, 1, h.updater.persistCalls)
			require.Equal(t, expected, h.set.Status.RolloutReplacementState,
				"the expanded scope must be persisted before replacing the new base ordinal")
		}
		return nil
	}
	h.reconcile(t, base0, replacement1, base2, surge3, surge4)
	h.assertDeleted(t, base2.Name)
	require.Equal(t, 1, h.updater.persistCalls)
	require.Equal(t, expected, h.set.Status.RolloutReplacementState)

	// Completing the original scope must not release surge while the newly
	// recorded ordinal 2 is still unready at the rollback revision.
	replacement1.Status.Conditions[0].Status = v1.ConditionTrue
	replacement2 := h.instance(2, h.current.Name, false)
	replacement2.Generation = 1
	replacement2.Status.ObservedGeneration = 1
	h.reconcile(t, base0, replacement1, replacement2, surge3, surge4)
	h.assertDeleted(t)
	require.Equal(t, expected, h.set.Status.RolloutReplacementState)

	// Ready from an older generation is not completion of the replacement.
	replacement2.Status.Conditions[0].Status = v1.ConditionTrue
	replacement2.Generation++
	h.reconcile(t, base0, replacement1, replacement2, surge3, surge4)
	h.assertDeleted(t)
	require.Equal(t, expected, h.set.Status.RolloutReplacementState)

	replacement2.Status.ObservedGeneration = replacement2.Generation
	h.reconcile(t, base0, replacement1, replacement2, surge3, surge4)
	h.assertDeleted(t, surge3.Name, surge4.Name)
	require.Nil(t, h.set.Status.RolloutReplacementState)
}

func TestEarlyRollbackWithoutSurgeSkipsReplacementStateWrite(t *testing.T) {
	for _, terminating := range []bool{false, true} {
		name := "controller replaces base"
		if terminating {
			name = "observes terminating base"
		}
		t.Run(name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "rollback-no-surge", 1, 0, 0)
			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
			h.updater.persistErr = errors.New("replacement status field was pruned")
			base := h.instance(0, h.intermediate.Name, true)
			if terminating {
				withTerminating(base)
			}
			h.reconcile(t, base)
			if h.updater.persistCalls != 0 {
				t.Fatalf("persist calls = %d, want none without surge", h.updater.persistCalls)
			}
			if h.set.Status.RolloutReplacementState != nil {
				t.Fatal("rollback without surge persisted unnecessary replacement state")
			}
			if terminating {
				h.assertDeleted(t)
				h.reconcile(t)
				if len(h.objects.created) != 1 || h.objects.created[0] != base.Name {
					t.Fatalf("created = %v, want replacement %s", h.objects.created, base.Name)
				}
			} else {
				h.assertDeleted(t, base.Name)
			}
		})
	}
}

func TestEarlyRollbackPersistenceFailureEvents(t *testing.T) {
	for _, tt := range []struct {
		name        string
		persistErr  error
		wantWarning bool
	}{
		{name: "failed status write", persistErr: errors.New("upgrade the RoleInstanceSet CRD"), wantWarning: true},
		{name: "spec changed", persistErr: errRolloutSpecChanged},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "rollback-events", 1, 0, 1)
			h.updater.persistErr = tt.persistErr
			target := h.instance(0, h.intermediate.Name, true)
			_, err := h.control.applyTargetUpdate(context.Background(), h.set,
				&workloadsv1alpha2.RoleInstanceSetStatus{}, target, h.current, h.current,
				[]*apps.ControllerRevision{h.current, h.intermediate}, false, false)
			if !errors.Is(err, tt.persistErr) {
				t.Fatalf("error = %v, want %v", err, tt.persistErr)
			}
			h.assertDeleted(t)
			recorder := h.control.recorder.(*record.FakeRecorder)
			select {
			case event := <-recorder.Events:
				if !tt.wantWarning || !strings.Contains(event, "Warning FailedPersistRolloutReplacementState") ||
					!strings.Contains(event, tt.persistErr.Error()) {
					t.Fatalf("unexpected persistence event: %s", event)
				}
			default:
				if tt.wantWarning {
					t.Fatal("failed persistence did not emit a Warning event")
				}
			}
		})
	}
}

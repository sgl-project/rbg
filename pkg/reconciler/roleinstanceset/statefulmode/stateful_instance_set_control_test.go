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
	"fmt"
	"testing"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instanceinplace "sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
)

func TestTruncateHistoryKeepsLiveInstanceRevision(t *testing.T) {
	limit := int32(0)
	set := &workloadsv1alpha2.RoleInstanceSet{
		Spec: workloadsv1alpha2.RoleInstanceSetSpec{
			RevisionHistoryLimit: &limit,
		},
	}
	revisions := []*apps.ControllerRevision{
		newStatefulRevision("rev-1", 1),
		newStatefulRevision("rev-2", 2),
		newStatefulRevision("rev-3", 3),
	}
	instances := []*workloadsv1alpha2.RoleInstance{
		{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					apps.ControllerRevisionHashLabelKey: "rev-1",
				},
			},
		},
	}
	history := &fakeStatefulHistory{}
	control := &defaultStatefulInstanceSetControl{controllerHistory: history}

	err := control.truncateHistory(set, instances, revisions, revisions[1], revisions[2])
	if err != nil {
		t.Fatalf("truncateHistory() error = %v", err)
	}

	if len(history.deleted) != 0 {
		t.Fatalf("deleted revisions = %v, want none", history.deleted)
	}
}

func TestTruncateHistoryUsesDefaultLimitWhenRevisionHistoryLimitIsNil(t *testing.T) {
	set := &workloadsv1alpha2.RoleInstanceSet{}
	revisions := make([]*apps.ControllerRevision, 0, 13)
	for i := 1; i <= 13; i++ {
		revisions = append(revisions, newStatefulRevision(fmt.Sprintf("rev-%d", i), int64(i)))
	}

	history := &fakeStatefulHistory{}
	control := &defaultStatefulInstanceSetControl{controllerHistory: history}

	err := control.truncateHistory(set, nil, revisions, revisions[11], revisions[12])
	if err != nil {
		t.Fatalf("truncateHistory() error = %v", err)
	}

	want := []string{"rev-1"}
	if len(history.deleted) != len(want) {
		t.Fatalf("deleted revisions = %v, want %v", history.deleted, want)
	}
	for i := range want {
		if history.deleted[i] != want[i] {
			t.Fatalf("deleted[%d] = %q, want %q", i, history.deleted[i], want[i])
		}
	}
}

func newStatefulRevision(name string, revision int64) *apps.ControllerRevision {
	return &apps.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Revision:   revision,
	}
}

type fakeStatefulHistory struct {
	deleted []string
}

func (f *fakeStatefulHistory) ListControllerRevisions(metav1.Object, labels.Selector) ([]*apps.ControllerRevision, error) {
	return nil, nil
}

func (f *fakeStatefulHistory) CreateControllerRevision(metav1.Object, *apps.ControllerRevision, *int32) (*apps.ControllerRevision, error) {
	return nil, nil
}

func (f *fakeStatefulHistory) DeleteControllerRevision(revision *apps.ControllerRevision) error {
	f.deleted = append(f.deleted, revision.Name)
	return nil
}

func (f *fakeStatefulHistory) UpdateControllerRevision(*apps.ControllerRevision, int64) (*apps.ControllerRevision, error) {
	return nil, nil
}

func (f *fakeStatefulHistory) AdoptControllerRevision(metav1.Object, schema.GroupVersionKind, *apps.ControllerRevision) (*apps.ControllerRevision, error) {
	return nil, nil
}

func (f *fakeStatefulHistory) ReleaseControllerRevision(metav1.Object, *apps.ControllerRevision) (*apps.ControllerRevision, error) {
	return nil, nil
}

const (
	testOldRev    = "old-rev"
	testUpdateRev = "update-rev"
)

// buildSet constructs a minimal RoleInstanceSet useful for topology /
// progressUpdate tests.
func buildSet(name string, replicas int32, surge, unavail *intstrutil.IntOrString) *workloadsv1alpha2.RoleInstanceSet {
	set := &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: workloadsv1alpha2.RoleInstanceSetSpec{
			Replicas: ptr.To(replicas),
		},
	}
	if surge != nil {
		set.Spec.UpdateStrategy.MaxSurge = surge
	}
	if unavail != nil {
		set.Spec.UpdateStrategy.MaxUnavailable = unavail
	}
	return set
}

// buildInst returns a RoleInstance at given ord, revision, and Ready cond.
// `ready=true` sets RoleInstanceReady=True; otherwise condition is False.
// `created=true` sets a UID so isCreated returns true.
func buildInst(setName string, ord int, rev string, ready, created bool) *workloadsv1alpha2.RoleInstance {
	inst := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", setName, ord),
			Namespace: "default",
			Labels: map[string]string{
				apps.ControllerRevisionHashLabelKey: rev,
			},
		},
	}
	if created {
		// Unique per (setName, ord) so the time-gate map (keyed on UID) does
		// not collapse all test instances onto a single entry.
		inst.UID = types.UID(fmt.Sprintf("uid-%s-%d", setName, ord))
	}
	cond := workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceReady,
		Status:             v1.ConditionFalse,
		LastTransitionTime: metav1.Now(),
	}
	if ready {
		cond.Status = v1.ConditionTrue
	}
	inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{cond}
	return inst
}

// withTerminating marks the instance as terminating (DeletionTimestamp set).
func withTerminating(inst *workloadsv1alpha2.RoleInstance) *workloadsv1alpha2.RoleInstance {
	now := metav1.Now()
	inst.DeletionTimestamp = &now
	return inst
}

func TestComputeTopology(t *testing.T) {
	tests := []struct {
		name              string
		set               *workloadsv1alpha2.RoleInstanceSet
		instances         []*workloadsv1alpha2.RoleInstance
		currentRev        string
		updateRev         string
		paused            bool
		expectedActSurge  int
		expectedEndOrd    int
		expectedPartition int
		expectedMaxUnav   int
		expectedMaxSurge  int
		expectedInRollout bool
	}{
		{
			name:              "no rollout: currentRev == updateRev",
			set:               buildSet("s", 4, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(0))),
			instances:         []*workloadsv1alpha2.RoleInstance{buildInst("s", 0, testUpdateRev, true, true), buildInst("s", 1, testUpdateRev, true, true), buildInst("s", 2, testUpdateRev, true, true), buildInst("s", 3, testUpdateRev, true, true)},
			currentRev:        testUpdateRev,
			updateRev:         testUpdateRev,
			expectedActSurge:  0,
			expectedEndOrd:    4,
			expectedMaxSurge:  2,
			expectedMaxUnav:   0,
			expectedInRollout: false,
		},
		{
			name: "paused with no existing surge: do not allocate new surge",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				s := buildSet("s", 4, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(0)))
				s.Spec.UpdateStrategy.Paused = true
				return s
			}(),
			instances:  []*workloadsv1alpha2.RoleInstance{buildInst("s", 0, testOldRev, true, true), buildInst("s", 1, testOldRev, true, true), buildInst("s", 2, testOldRev, true, true), buildInst("s", 3, testOldRev, true, true)},
			currentRev: testOldRev, updateRev: testUpdateRev,
			expectedActSurge: 0, expectedEndOrd: 4,
			expectedMaxSurge: 2, expectedMaxUnav: 0,
			expectedInRollout: false,
		},
		{
			// Paused mid-rollout while surge already exists: preserve the
			// surge so Phase B does not condemn it. Pod startup is expensive,
			// throwing away surge on pause and re-creating on unpause wastes
			// minutes of GPU time.
			name: "paused mid-rollout: preserve existing surge at updateRev",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				s := buildSet("s", 4, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(0)))
				s.Spec.UpdateStrategy.Paused = true
				return s
			}(),
			instances: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true), buildInst("s", 1, testOldRev, true, true),
				buildInst("s", 2, testOldRev, true, true), buildInst("s", 3, testOldRev, true, true),
				buildInst("s", 4, testUpdateRev, true, true), buildInst("s", 5, testUpdateRev, true, true),
			},
			currentRev: testOldRev, updateRev: testUpdateRev,
			expectedActSurge: 2, expectedEndOrd: 6,
			expectedMaxSurge: 2, expectedMaxUnav: 0,
			expectedInRollout: false, // paused → rollout phases skipped
		},
		{
			// Paused with stale-rev surge (from a superseded rollout): those
			// surge slots are at a rev that is neither currentRev nor the new
			// updateRev. They are NOT counted by countExistingValidSurge, so
			// activeSurge stays 0 and they get condemned. This is the right
			// call: stale surge is not contributing toward the current target.
			name: "paused mid-rollout: stale-rev surge is NOT preserved",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				s := buildSet("s", 4, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(0)))
				s.Spec.UpdateStrategy.Paused = true
				return s
			}(),
			instances: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true), buildInst("s", 1, testOldRev, true, true),
				buildInst("s", 2, testOldRev, true, true), buildInst("s", 3, testOldRev, true, true),
				buildInst("s", 4, "stale-rev", true, true), buildInst("s", 5, "stale-rev", true, true),
			},
			currentRev: testOldRev, updateRev: testUpdateRev,
			expectedActSurge: 0, expectedEndOrd: 4,
			expectedMaxSurge: 2, expectedMaxUnav: 0,
			expectedInRollout: false,
		},
		{
			name: "rollout, all healthy old: surge=min(maxSurge, healthyOld-maxUnav)",
			set:  buildSet("s", 4, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(0))),
			instances: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true), buildInst("s", 1, testOldRev, true, true),
				buildInst("s", 2, testOldRev, true, true), buildInst("s", 3, testOldRev, true, true),
			},
			currentRev: testOldRev, updateRev: testUpdateRev,
			expectedActSurge: 2, expectedEndOrd: 6,
			expectedMaxSurge: 2, expectedMaxUnav: 0,
			expectedInRollout: true,
		},
		{
			name: "all unhealthy old → no surge needed (broken instances aren't contributing to availability)",
			set:  buildSet("s", 2, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(0))),
			instances: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, false, true), buildInst("s", 1, testOldRev, false, true),
			},
			currentRev: testOldRev, updateRev: testUpdateRev,
			expectedActSurge: 0, expectedEndOrd: 2,
			expectedMaxSurge: 2, expectedMaxUnav: 0,
			expectedInRollout: true,
		},
		{
			name: "maxUnav covers all: surge=2, unav=2, healthyOld=2 → surge=0",
			set:  buildSet("s", 4, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(2))),
			instances: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true), buildInst("s", 1, testOldRev, true, true),
				buildInst("s", 2, testOldRev, true, true), buildInst("s", 3, testOldRev, true, true),
			},
			currentRev: testOldRev, updateRev: testUpdateRev,
			expectedActSurge: 2, expectedEndOrd: 6, // healthyOld=4, surgeNeeded=4-2=2
			expectedMaxSurge: 2, expectedMaxUnav: 2,
			expectedInRollout: true,
		},
		{
			name: "partition=2: only [2,4) update; healthyOld counted in range only",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				s := buildSet("s", 4, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(0)))
				s.Spec.UpdateStrategy.Partition = ptr.To(intstrutil.FromInt32(2))
				return s
			}(),
			instances:         []*workloadsv1alpha2.RoleInstance{buildInst("s", 0, testOldRev, true, true), buildInst("s", 1, testOldRev, true, true), buildInst("s", 2, testOldRev, true, true), buildInst("s", 3, testOldRev, true, true)},
			currentRev:        testOldRev,
			updateRev:         testUpdateRev,
			expectedActSurge:  2, // healthyOld in [2,4) = 2
			expectedEndOrd:    6,
			expectedPartition: 2,
			expectedMaxSurge:  2,
			expectedMaxUnav:   0,
			expectedInRollout: true,
		},
		{
			name: "chained rollout: stale-rev surge condemned (existingValidSurge counts updateRev only)",
			// Chained rollout: a previous rollout left base + surge at rev
			// "mid". User pushes a new rev "update" before "mid" finished.
			// existingValidSurge for "update" = 0 (the in-flight surge is at
			// the wrong rev). With healthyOldInBase = 0 here, activeSurge
			// collapses to 0 and the stale surge falls outside endOrdinal so
			// Phase B condemns it.
			set: buildSet("s", 2, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(0))),
			instances: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, "mid-rev", false, true), buildInst("s", 1, "mid-rev", false, true),
				buildInst("s", 2, "mid-rev", true, true), buildInst("s", 3, "mid-rev", true, true),
			},
			currentRev: testOldRev, updateRev: testUpdateRev,
			expectedActSurge: 0, expectedEndOrd: 2, // surge condemned, ords 2,3 will be condemned in Phase B
			expectedMaxSurge: 2, expectedMaxUnav: 0,
			expectedInRollout: true,
		},
		{
			// Partitioned rollout completed: ords [partition, replicas) at
			// updateRev healthy. surgeNeeded=0 and stickiness MUST drop
			// (allBaseAtUpdateRevHealthy=true), otherwise surge would persist
			// forever — currentRevision never advances under partition>0,
			// so the "inRollout=false next reconcile" cleanup path never
			// triggers either.
			name: "partition rollout completed: drop sticky surge so it can be condemned",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				s := buildSet("s", 4, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(0)))
				s.Spec.UpdateStrategy.Partition = ptr.To(intstrutil.FromInt32(2))
				return s
			}(),
			instances: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true), // below partition, untouched
				buildInst("s", 1, testOldRev, true, true), // below partition, untouched
				func() *workloadsv1alpha2.RoleInstance {
					i := buildInst("s", 2, testUpdateRev, true, true)
					i.Generation = 1
					i.Status.ObservedGeneration = 1
					return i
				}(), // updated, ObservedGen synced
				func() *workloadsv1alpha2.RoleInstance {
					i := buildInst("s", 3, testUpdateRev, true, true)
					i.Generation = 1
					i.Status.ObservedGeneration = 1
					return i
				}(),
				buildInst("s", 4, testUpdateRev, true, true), // surge slot still exists from rollout
				buildInst("s", 5, testUpdateRev, true, true),
			},
			currentRev:        testOldRev,
			updateRev:         testUpdateRev,
			expectedActSurge:  0, // stickiness dropped
			expectedEndOrd:    4,
			expectedPartition: 2,
			expectedMaxSurge:  2,
			expectedMaxUnav:   0,
			expectedInRollout: true,
		},
		{
			// Bug: partition decrease should re-trigger work. After a
			// partition=2 rollout we have ords [0,2) at oldRev, ords [2,4)
			// at updateRev. CurrentRevision stays at oldRev (partition>0
			// guard). User now lowers partition to 1: topology must see ord
			// 1 as old-rev work and allocate surge for it.
			name: "partition decrease: ord at non-updateRev in newly-exposed range triggers surge",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				s := buildSet("s", 4, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(0)))
				s.Spec.UpdateStrategy.Partition = ptr.To(intstrutil.FromInt32(1))
				return s
			}(),
			instances:         []*workloadsv1alpha2.RoleInstance{buildInst("s", 0, testOldRev, true, true), buildInst("s", 1, testOldRev, true, true), buildInst("s", 2, testUpdateRev, true, true), buildInst("s", 3, testUpdateRev, true, true)},
			currentRev:        testOldRev, // not advanced because previous rollout had partition>0
			updateRev:         testUpdateRev,
			expectedActSurge:  1, // healthyOld in [1,4) = 1 (only ord 1)
			expectedEndOrd:    5,
			expectedPartition: 1,
			expectedMaxSurge:  2,
			expectedMaxUnav:   0,
			expectedInRollout: true,
		},
		{
			name: "stickiness: existingValidSurge keeps surge alive even as healthyOld shrinks",
			// 2 base at oldRev (one healthy, one becoming unhealthy mid-rollout) +
			// 2 surge at updateRev healthy. healthyOld = 1. surgeNeeded = 1-0 = 1.
			// existingValidSurge = 2 (healthy at updateRev). activeSurge =
			// max(1, 2) = 2. So we keep surge = 2 even though "need" dropped.
			set: buildSet("s", 2, ptr.To(intstrutil.FromInt32(2)), ptr.To(intstrutil.FromInt32(0))),
			instances: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true), buildInst("s", 1, testOldRev, false, true),
				buildInst("s", 2, testUpdateRev, true, true), buildInst("s", 3, testUpdateRev, true, true),
			},
			currentRev: testOldRev, updateRev: testUpdateRev,
			expectedActSurge: 2, expectedEndOrd: 4,
			expectedMaxSurge: 2, expectedMaxUnav: 0,
			expectedInRollout: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := computeTopology(tt.set, tt.instances, tt.currentRev, tt.updateRev)
			if err != nil {
				t.Fatalf("computeTopology() unexpected error: %v", err)
			}
			if got.activeSurge != tt.expectedActSurge {
				t.Errorf("activeSurge = %d, want %d", got.activeSurge, tt.expectedActSurge)
			}
			if got.endOrdinal != tt.expectedEndOrd {
				t.Errorf("endOrdinal = %d, want %d", got.endOrdinal, tt.expectedEndOrd)
			}
			if got.partition != tt.expectedPartition {
				t.Errorf("partition = %d, want %d", got.partition, tt.expectedPartition)
			}
			if got.maxSurge != tt.expectedMaxSurge {
				t.Errorf("maxSurge = %d, want %d", got.maxSurge, tt.expectedMaxSurge)
			}
			if got.maxUnavailable != tt.expectedMaxUnav {
				t.Errorf("maxUnavailable = %d, want %d", got.maxUnavailable, tt.expectedMaxUnav)
			}
			if got.inRollout != tt.expectedInRollout {
				t.Errorf("inRollout = %v, want %v", got.inRollout, tt.expectedInRollout)
			}
		})
	}
}

// fakeInstanceObjectManager records create/delete calls without API.
type fakeInstanceObjectManager struct {
	created []string
	deleted []string
}

func (f *fakeInstanceObjectManager) CreateInstance(_ context.Context, instance *workloadsv1alpha2.RoleInstance) error {
	f.created = append(f.created, instance.Name)
	return nil
}
func (f *fakeInstanceObjectManager) GetInstance(_, _ string) (*workloadsv1alpha2.RoleInstance, error) {
	return nil, nil
}
func (f *fakeInstanceObjectManager) UpdateInstance(_ *workloadsv1alpha2.RoleInstance) error {
	return nil
}
func (f *fakeInstanceObjectManager) DeleteInstance(instance *workloadsv1alpha2.RoleInstance) error {
	f.deleted = append(f.deleted, instance.Name)
	return nil
}

// fakeInplaceControl returns CanUpdateInPlace=false by default so progressUpdate
// falls through to delete-and-recreate when the strategy allows fallback.
type fakeInplaceControl struct {
	canUpdateCalls int
	updateCalls    int
}

func (f *fakeInplaceControl) CanUpdateInPlace(_, _ *apps.ControllerRevision, _ *instanceinplace.UpdateOptions) bool {
	f.canUpdateCalls++
	return false
}
func (f *fakeInplaceControl) Update(_ *workloadsv1alpha2.RoleInstance, _, _ *apps.ControllerRevision, _ *instanceinplace.UpdateOptions) instanceinplace.UpdateResult {
	f.updateCalls++
	return instanceinplace.UpdateResult{InPlaceUpdate: false}
}
func (f *fakeInplaceControl) Refresh(_ *workloadsv1alpha2.RoleInstance, _ *instanceinplace.UpdateOptions) instanceinplace.RefreshResult {
	return instanceinplace.RefreshResult{}
}

func TestApplyTargetUpdateRecreatePodSkipsInPlace(t *testing.T) {
	set := buildSet("s", 1, nil, nil)
	set.Spec.UpdateStrategy.Type = workloadsv1alpha2.RecreatePodUpdateStrategyType
	target := buildInst("s", 0, testOldRev, true, true)
	objectManager := &fakeInstanceObjectManager{}
	inplaceControl := &fakeInplaceControl{}
	control := &defaultStatefulInstanceSetControl{
		instanceControl: NewStatefulInstanceControlFromManager(objectManager, record.NewFakeRecorder(10)),
		inplaceControl:  inplaceControl,
	}

	transitioned, err := control.applyTargetUpdate(
		set,
		target,
		&apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: testOldRev}},
		&apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: testUpdateRev}},
		[]*apps.ControllerRevision{{ObjectMeta: metav1.ObjectMeta{Name: testOldRev}}},
		false,
		false,
	)
	if err != nil {
		t.Fatalf("applyTargetUpdate() error = %v", err)
	}
	if !transitioned {
		t.Fatal("expected target to transition via recreate")
	}
	if inplaceControl.canUpdateCalls != 0 || inplaceControl.updateCalls != 0 {
		t.Fatalf("expected in-place control not called, got canUpdate=%d update=%d", inplaceControl.canUpdateCalls, inplaceControl.updateCalls)
	}
	if len(objectManager.deleted) != 1 || objectManager.deleted[0] != target.Name {
		t.Fatalf("expected target deleted, got %v", objectManager.deleted)
	}
}

func TestProgressUpdateBudget(t *testing.T) {
	tests := []struct {
		name     string
		replicas []*workloadsv1alpha2.RoleInstance
		topo     topology
		// markStablyUnhealthy, when true, seeds instanceUnhealthySince with a
		// past timestamp for every unhealthy replica in `replicas`, so they
		// satisfy the time gate without the test having to sleep. Tests that
		// want to exercise the cleanup-unhealthy free-delete path set this;
		// tests that exercise the cache-lag protection (transiently mislabeled
		// ready instances must NOT be free-deleted) leave it false so those
		// instances fail the gate and stay protected.
		markStablyUnhealthy bool
		expectedDels        []string // names that MUST be deleted (subset)
		expectNoDelet       []string // names that MUST NOT be deleted
	}{
		{
			// All base healthy old, surge already healthy at updateRev → can update
			// up to maxUnav + availSurge.
			name: "healthy old base, surge ready: budget = maxUnav + availSurge",
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true),
				buildInst("s", 1, testOldRev, true, true),
				buildInst("s", 2, testUpdateRev, true, true),
				buildInst("s", 3, testUpdateRev, true, true),
			},
			topo: topology{
				startOrdinal: 0, endOrdinal: 4, surgeStart: 2,
				replicas: 2, partition: 0, maxUnavailable: 0, maxSurge: 2,
				activeSurge: 2, inRollout: true,
			},
			expectedDels: []string{"s-1", "s-0"},
		},
		{
			// All base STABLY unhealthy at oldRev → free-delete eligible
			// (cleanup-unhealthy path; broken instances are not contributing
			// to availability so deleting them does not consume budget).
			name: "stably unhealthy base free-deletes without budget",
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, false, true),
				buildInst("s", 1, testOldRev, false, true),
			},
			topo: topology{
				startOrdinal: 0, endOrdinal: 2, surgeStart: 2,
				replicas: 2, partition: 0, maxUnavailable: 0, maxSurge: 2,
				activeSurge: 0, inRollout: true,
			},
			markStablyUnhealthy: true,
			expectedDels:        []string{"s-0", "s-1"},
		},
		{
			// Same shape as the prior case but the unhealthy state is FRESH
			// (not stably observed for stableUnhealthyDuration). Time gate
			// must deny the free path; the cost path then blocks on budget=0.
			// Net effect: protect brand-new flapping instances from being
			// deleted.
			name: "freshly unhealthy (not stably): time gate denies free, cost blocks",
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, false, true),
				buildInst("s", 1, testOldRev, false, true),
			},
			topo: topology{
				startOrdinal: 0, endOrdinal: 2, surgeStart: 2,
				replicas: 2, partition: 0, maxUnavailable: 0, maxSurge: 2,
				activeSurge: 0, inRollout: true,
			},
			markStablyUnhealthy: false,
			expectNoDelet:       []string{"s-0", "s-1"},
		},
		{
			// All healthy old base, no surge, maxUnav=0 → completely blocked.
			name: "maxUnav=0 with no surge buffer blocks all healthy updates",
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true),
				buildInst("s", 1, testOldRev, true, true),
			},
			topo: topology{
				startOrdinal: 0, endOrdinal: 2, surgeStart: 2,
				replicas: 2, partition: 0, maxUnavailable: 0, maxSurge: 0,
				activeSurge: 0, inRollout: true,
			},
			expectNoDelet: []string{"s-0", "s-1"},
		},
		{
			// 1 healthy + 1 stably unhealthy base, maxUnav=0, no surge.
			// Stably unhealthy is free; healthy is cost-blocked.
			name: "mixed: stably unhealthy proceeds free, healthy stays",
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true),
				buildInst("s", 1, testOldRev, false, true),
			},
			topo: topology{
				startOrdinal: 0, endOrdinal: 2, surgeStart: 2,
				replicas: 2, partition: 0, maxUnavailable: 0, maxSurge: 0,
				activeSurge: 0, inRollout: true,
			},
			markStablyUnhealthy: true,
			expectedDels:        []string{"s-1"},
			expectNoDelet:       []string{"s-0"},
		},
		{
			// Mid-rollout, base instances at a superseded revision are
			// stably unhealthy (e.g. stuck in CrashLoopBackOff). Free path
			// recovers them so a new updateRev push can make progress
			// without waiting for the stuck rev to "complete".
			name: "stably unhealthy stale-rev base recovers free",
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, "mid-rev", false, true),
				buildInst("s", 1, "mid-rev", false, true),
			},
			topo: topology{
				startOrdinal: 0, endOrdinal: 2, surgeStart: 2,
				replicas: 2, partition: 0, maxUnavailable: 0, maxSurge: 2,
				activeSurge: 0, inRollout: true,
			},
			markStablyUnhealthy: true,
			expectedDels:        []string{"s-0", "s-1"},
		},
		{
			// Chained rolling update. replicas=4, surge=2. Initial rollout
			// to rev2 created 2 surge at rev2 (ord 4, 5). Before they ready,
			// user pushes rev3. activeSurge stays 2 (need surge for rev3),
			// endOrdinal=6, so the rev2 surge slots are NOT condemned in
			// Phase B. progressUpdate must recycle them by including ord
			// [replicas, endOrdinal) in targets and treating surge updates as
			// free. Otherwise they would stay at rev2 forever.
			name: "chained rollout: stale-rev surge in [replicas, endOrdinal) gets recycled (free)",
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true),
				buildInst("s", 1, testOldRev, true, true),
				buildInst("s", 2, testOldRev, true, true),
				buildInst("s", 3, testOldRev, true, true),
				buildInst("s", 4, "mid-rev", false, true), // stale surge from prior rollout, still creating
				buildInst("s", 5, "mid-rev", false, true),
			},
			topo: topology{
				startOrdinal: 0, endOrdinal: 6, surgeStart: 4,
				replicas: 4, partition: 0, maxUnavailable: 0, maxSurge: 2,
				activeSurge: 2, inRollout: true,
			},
			// Stale surge gets deleted (free). Base blocked by budget=0 since
			// no rev3 surge healthy yet. Next reconcile creates fresh surge
			// at the current updateRev.
			expectedDels:  []string{"s-4", "s-5"},
			expectNoDelet: []string{"s-0", "s-1", "s-2", "s-3"},
		},
		{
			// Sibling of above: stale surge healthy. Still gets recycled.
			// Surge updates do not count toward effectiveBudget so even
			// maxUnav=0 + zero rev3-surge does not block them.
			name: "chained rollout: stale-rev surge HEALTHY also gets recycled",
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true),
				buildInst("s", 1, testOldRev, true, true),
				buildInst("s", 2, testOldRev, true, true),
				buildInst("s", 3, testOldRev, true, true),
				buildInst("s", 4, "mid-rev", true, true), // healthy stale surge
				buildInst("s", 5, "mid-rev", true, true),
			},
			topo: topology{
				startOrdinal: 0, endOrdinal: 6, surgeStart: 4,
				replicas: 4, partition: 0, maxUnavailable: 0, maxSurge: 2,
				activeSurge: 2, inRollout: true,
			},
			expectedDels:  []string{"s-4", "s-5"},
			expectNoDelet: []string{"s-0", "s-1", "s-2", "s-3"},
		},
		{
			// Cache-lag mislabels a fresh-flapping ready base as unhealthy.
			// markStablyUnhealthy is FALSE so the time gate is not satisfied
			// → free-delete denied → cost path blocks on budget=0. Net
			// effect: ord 1 (which cache says is unhealthy but really just
			// flapped) is NOT deleted.
			name: "fresh cache-lag unhealthy base must NOT free-delete",
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true),
				buildInst("s", 1, testOldRev, false, true), // cache says not ready, but flap is fresh
			},
			topo: topology{
				startOrdinal: 0, endOrdinal: 2, surgeStart: 2,
				replicas: 2, partition: 0, maxUnavailable: 0, maxSurge: 0,
				activeSurge: 0, inRollout: true,
			},
			markStablyUnhealthy: false,
			expectNoDelet:       []string{"s-0", "s-1"},
		},
		{
			// Same protection extends to the case where surge is in flight:
			// surge updates remain free (always), but flapping base ord 1
			// must not be free-deleted just because cache says so.
			name: "fresh cache-lag protected even with surge in flight",
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true),
				buildInst("s", 1, testOldRev, false, true),
				buildInst("s", 2, testUpdateRev, false, true),
				buildInst("s", 3, testUpdateRev, false, true),
			},
			topo: topology{
				startOrdinal: 0, endOrdinal: 4, surgeStart: 2,
				replicas: 2, partition: 0, maxUnavailable: 0, maxSurge: 2,
				activeSurge: 2, inRollout: true,
			},
			markStablyUnhealthy: false,
			expectNoDelet:       []string{"s-0", "s-1"},
		},
		{
			// Partition=2: ord 0,1 below partition stay; only ord 2,3 update.
			name: "partition: instances below partition stay untouched",
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true),
				buildInst("s", 1, testOldRev, true, true),
				buildInst("s", 2, testOldRev, true, true),
				buildInst("s", 3, testOldRev, true, true),
			},
			topo: topology{
				startOrdinal: 0, endOrdinal: 4, surgeStart: 4,
				replicas: 4, partition: 2, maxUnavailable: 1, maxSurge: 0,
				activeSurge: 0, inRollout: true,
			},
			expectedDels:  []string{"s-3"}, // budget=1 → only highest ord
			expectNoDelet: []string{"s-0", "s-1", "s-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset the cross-test global time-gate map so the prior test's
			// observations cannot leak in. Tests opt in to "stably unhealthy"
			// via markStablyUnhealthy below.
			instanceUnhealthySince.Range(func(k, _ interface{}) bool {
				instanceUnhealthySince.Delete(k)
				return true
			})
			if tt.markStablyUnhealthy {
				past := time.Now().Add(-2 * stableUnhealthyDuration)
				for _, inst := range tt.replicas {
					if inst != nil && inst.UID != "" && !isHealthy(inst) {
						instanceUnhealthySince.Store(inst.UID, past)
					}
				}
			}

			set := buildSet("s", int32(tt.topo.replicas), nil, nil)
			fakeOM := &fakeInstanceObjectManager{}
			recorder := record.NewFakeRecorder(64)
			ssc := &defaultStatefulInstanceSetControl{
				instanceControl: NewStatefulInstanceControlFromManager(fakeOM, recorder),
				inplaceControl:  &fakeInplaceControl{},
				recorder:        recorder,
			}

			currentRev := newStatefulRevision(testOldRev, 1)
			updateRev := newStatefulRevision(testUpdateRev, 2)
			revisions := []*apps.ControllerRevision{currentRev, updateRev}
			status := &workloadsv1alpha2.RoleInstanceSetStatus{}

			_, err := ssc.progressUpdate(set, status, currentRev, updateRev, revisions, tt.replicas, tt.replicas, 0, tt.topo)
			if err != nil {
				t.Fatalf("progressUpdate() unexpected error: %v", err)
			}

			deleted := sets.New(fakeOM.deleted...)
			for _, name := range tt.expectedDels {
				if !deleted.Has(name) {
					t.Errorf("expected %q to be deleted, deleted=%v", name, fakeOM.deleted)
				}
			}
			for _, name := range tt.expectNoDelet {
				if deleted.Has(name) {
					t.Errorf("did NOT expect %q to be deleted, deleted=%v", name, fakeOM.deleted)
				}
			}
		})
	}
}

func TestShouldAdvanceCurrentRevision(t *testing.T) {
	// helper: build set with given prior status fields
	priorStatus := func(updateRev string, updatedReplicas int32) *workloadsv1alpha2.RoleInstanceSet {
		s := buildSet("s", 2, nil, nil)
		s.Status.UpdateRevision = updateRev
		s.Status.UpdatedReplicas = updatedReplicas
		return s
	}
	baseTopo := topology{
		startOrdinal: 0, endOrdinal: 2, surgeStart: 2,
		replicas: 2, partition: 0, inRollout: true,
	}

	tests := []struct {
		name     string
		set      *workloadsv1alpha2.RoleInstanceSet
		replicas []*workloadsv1alpha2.RoleInstance
		topo     topology
		expected bool
	}{
		{
			name:     "happy path: prior status concurrence + observed all green",
			set:      priorStatus(testUpdateRev, 2),
			replicas: []*workloadsv1alpha2.RoleInstance{buildInst("s", 0, testUpdateRev, true, true), buildInst("s", 1, testUpdateRev, true, true)},
			topo:     baseTopo,
			expected: true,
		},
		{
			name:     "prior status had not yet caught up (UpdatedReplicas=0) → don't advance",
			set:      priorStatus(testUpdateRev, 0),
			replicas: []*workloadsv1alpha2.RoleInstance{buildInst("s", 0, testUpdateRev, true, true), buildInst("s", 1, testUpdateRev, true, true)},
			topo:     baseTopo,
			expected: false,
		},
		{
			name:     "prior status had different UpdateRevision → don't advance",
			set:      priorStatus("some-other-rev", 2),
			replicas: []*workloadsv1alpha2.RoleInstance{buildInst("s", 0, testUpdateRev, true, true), buildInst("s", 1, testUpdateRev, true, true)},
			topo:     baseTopo,
			expected: false,
		},
		{
			name:     "observed: one base still at oldRev → don't advance",
			set:      priorStatus(testUpdateRev, 2),
			replicas: []*workloadsv1alpha2.RoleInstance{buildInst("s", 0, testOldRev, true, true), buildInst("s", 1, testUpdateRev, true, true)},
			topo:     baseTopo,
			expected: false,
		},
		{
			name:     "observed: one base unhealthy → don't advance",
			set:      priorStatus(testUpdateRev, 2),
			replicas: []*workloadsv1alpha2.RoleInstance{buildInst("s", 0, testUpdateRev, true, true), buildInst("s", 1, testUpdateRev, false, true)},
			topo:     baseTopo,
			expected: false,
		},
		{
			name: "observed: ObservedGeneration not synced → don't advance (in-place midflight)",
			set:  priorStatus(testUpdateRev, 2),
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testUpdateRev, true, true),
				func() *workloadsv1alpha2.RoleInstance {
					inst := buildInst("s", 1, testUpdateRev, true, true)
					inst.Generation = 2
					inst.Status.ObservedGeneration = 1
					return inst
				}(),
			},
			topo:     baseTopo,
			expected: false,
		},
		{
			name:     "observed: instance terminating → don't advance",
			set:      priorStatus(testUpdateRev, 2),
			replicas: []*workloadsv1alpha2.RoleInstance{buildInst("s", 0, testUpdateRev, true, true), withTerminating(buildInst("s", 1, testUpdateRev, true, true))},
			topo:     baseTopo,
			expected: false,
		},
		{
			name: "not in rollout: never advance (no-op)",
			set:  priorStatus(testUpdateRev, 2),
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testUpdateRev, true, true),
				buildInst("s", 1, testUpdateRev, true, true),
			},
			topo:     topology{startOrdinal: 0, endOrdinal: 2, surgeStart: 2, replicas: 2, partition: 0, inRollout: false},
			expected: false,
		},
		{
			// With partition > 0, the rev semantics is: currentRev = rev of
			// ords below partition (still old), updateRev = rev of ords above.
			// Advancing CurrentRevision to updateRev here would lie about ord
			// 0 — and break later partition decreases (currentRev would equal
			// updateRev so "in rollout" detection would miss them).
			name: "partition > 0: do not advance even when ords above partition are all at updateRev",
			set:  priorStatus(testUpdateRev, 1),
			replicas: []*workloadsv1alpha2.RoleInstance{
				buildInst("s", 0, testOldRev, true, true),
				buildInst("s", 1, testUpdateRev, true, true),
			},
			topo:     topology{startOrdinal: 0, endOrdinal: 2, surgeStart: 2, replicas: 2, partition: 1, inRollout: true},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldAdvanceCurrentRevision(tt.set, tt.replicas, testUpdateRev, tt.topo)
			if got != tt.expected {
				t.Errorf("shouldAdvanceCurrentRevision() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// resetInstanceUnhealthySince clears the global time-gate map so each test
// starts from a known empty state.
func resetInstanceUnhealthySince() {
	instanceUnhealthySince.Range(func(k, _ interface{}) bool {
		instanceUnhealthySince.Delete(k)
		return true
	})
}

func TestObserveInstanceHealthAndIsStablyUnhealthy(t *testing.T) {
	resetInstanceUnhealthySince()
	defer resetInstanceUnhealthySince()

	healthy := buildInst("s", 0, testOldRev, true, true)
	unhealthy := buildInst("s", 1, testOldRev, false, true)

	// First observation: nothing is "stably" unhealthy yet — even the
	// unhealthy instance just had its timer started.
	observeInstanceHealth([]*workloadsv1alpha2.RoleInstance{healthy, unhealthy})
	if isStablyUnhealthy(unhealthy) {
		t.Errorf("just-observed unhealthy must not be stably unhealthy yet")
	}
	if isStablyUnhealthy(healthy) {
		t.Errorf("healthy instance must never be stably unhealthy")
	}

	// Backdate the unhealthy timer past the threshold; gate should now fire.
	instanceUnhealthySince.Store(unhealthy.UID, time.Now().Add(-2*stableUnhealthyDuration))
	if !isStablyUnhealthy(unhealthy) {
		t.Errorf("unhealthy past threshold must be stably unhealthy")
	}

	// Observe instance flipping back to healthy: timer must reset.
	healthyAgain := buildInst("s", 1, testOldRev, true, true)
	healthyAgain.UID = unhealthy.UID
	observeInstanceHealth([]*workloadsv1alpha2.RoleInstance{healthyAgain})
	if isStablyUnhealthy(healthyAgain) {
		t.Errorf("after observing healthy, gate must reset")
	}
	// Even if a later flip-to-unhealthy comes in, the timer is fresh.
	stillUnhealthy := buildInst("s", 1, testOldRev, false, true)
	stillUnhealthy.UID = unhealthy.UID
	observeInstanceHealth([]*workloadsv1alpha2.RoleInstance{stillUnhealthy})
	if isStablyUnhealthy(stillUnhealthy) {
		t.Errorf("freshly re-observed unhealthy must not satisfy gate")
	}

	// Disappeared UIDs are garbage-collected from the map.
	observeInstanceHealth(nil)
	if _, ok := instanceUnhealthySince.Load(stillUnhealthy.UID); ok {
		t.Errorf("UID no longer present in instances slice should be GC'd from the map")
	}
}

func TestIsStablyUnhealthyEdgeCases(t *testing.T) {
	resetInstanceUnhealthySince()
	defer resetInstanceUnhealthySince()

	if isStablyUnhealthy(nil) {
		t.Errorf("nil instance must return false")
	}
	noUID := buildInst("s", 0, testOldRev, false, false) // created=false → no UID
	if isStablyUnhealthy(noUID) {
		t.Errorf("instance without UID must return false")
	}
	notInMap := buildInst("s", 1, testOldRev, false, true)
	if isStablyUnhealthy(notInMap) {
		t.Errorf("instance not yet in the map must return false")
	}
}

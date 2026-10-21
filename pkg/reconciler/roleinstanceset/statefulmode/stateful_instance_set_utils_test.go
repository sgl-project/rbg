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
	"reflect"
	"strconv"
	"testing"

	apps "k8s.io/api/apps/v1"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// TestNewVersionedInstanceIdentityIsPerOrdinal covers the instances of one set being
// built in a single reconcile: each must carry its own ordinal in its pod template,
// and the set's template must come out of it with no identity at all.
//
// Every instance is built before anything is asserted. Checking one right after
// constructing it would pass even on a shared template, which is only clobbered once
// the next ordinal is built.
func TestNewVersionedInstanceIdentityIsPerOrdinal(t *testing.T) {
	set := newRevisionTestSet("nginx:1.0")

	const replicas = 2
	instances := make([]*workloadsv1alpha2.RoleInstance, replicas)
	for ordinal := range instances {
		instances[ordinal] = newVersionedInstance(set, set, "current-rev", "update-rev", ordinal, nil)
	}

	for ordinal, instance := range instances {
		labels := instance.Spec.Components[0].Template.Labels

		wantName := "test-set-" + strconv.Itoa(ordinal)
		if got := labels[apps.StatefulSetPodNameLabel]; got != wantName {
			t.Errorf("ordinal %d: pod template %s is %q, expected %q",
				ordinal, apps.StatefulSetPodNameLabel, got, wantName)
		}
		wantIndex := strconv.Itoa(ordinal)
		if got := labels[constants.RoleInstanceIndexLabelKey]; got != wantIndex {
			t.Errorf("ordinal %d: pod template %s is %q, expected %q",
				ordinal, constants.RoleInstanceIndexLabelKey, got, wantIndex)
		}
	}

	setLabels := set.Spec.RoleInstanceTemplate.Components[0].Template.Labels
	for _, key := range []string{apps.StatefulSetPodNameLabel, constants.RoleInstanceIndexLabelKey} {
		if value, found := setLabels[key]; found {
			t.Errorf("the set's shared template was given %s=%q, which belongs to one ordinal only",
				key, value)
		}
	}
}

func TestRolloutReplacementStateActive(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*workloadsv1alpha2.RoleInstanceSet, *workloadsv1alpha2.RoleInstance)
		active bool
	}{
		{name: "unready replacement", active: true},
		{name: "old revision", active: true, change: func(_ *workloadsv1alpha2.RoleInstanceSet, inst *workloadsv1alpha2.RoleInstance) {
			inst.Labels[apps.ControllerRevisionHashLabelKey] = testOldRev
		}},
		{name: "terminating replacement", active: true, change: func(_ *workloadsv1alpha2.RoleInstanceSet, inst *workloadsv1alpha2.RoleInstance) {
			withTerminating(inst)
		}},
		{name: "generation not observed", active: true, change: func(_ *workloadsv1alpha2.RoleInstanceSet, inst *workloadsv1alpha2.RoleInstance) {
			inst.Status.Conditions[0].Status = "True"
			inst.Generation = 2
		}},
		{name: "replacement converged", change: func(_ *workloadsv1alpha2.RoleInstanceSet, inst *workloadsv1alpha2.RoleInstance) {
			inst.Status.Conditions[0].Status = "True"
			inst.Generation = 2
			inst.Status.ObservedGeneration = 2
		}},
		{name: "revision superseded", change: func(set *workloadsv1alpha2.RoleInstanceSet, _ *workloadsv1alpha2.RoleInstance) {
			set.Status.RolloutReplacementState.UpdateRevision = testOldRev
		}},
		{name: "scale up preserves protection", active: true, change: func(set *workloadsv1alpha2.RoleInstanceSet, _ *workloadsv1alpha2.RoleInstance) {
			set.Spec.Replicas = ptr.To[int32](2)
		}},
		{name: "partition excludes replacement", change: func(set *workloadsv1alpha2.RoleInstanceSet, _ *workloadsv1alpha2.RoleInstance) {
			set.Spec.UpdateStrategy.Partition = ptr.To(intstrutil.FromInt32(1))
		}},
		{name: "pause preserves protection", active: true, change: func(set *workloadsv1alpha2.RoleInstanceSet, _ *workloadsv1alpha2.RoleInstance) {
			set.Spec.UpdateStrategy.Paused = true
			set.Generation++
		}},
		{name: "no state on recreated set", change: func(set *workloadsv1alpha2.RoleInstanceSet, _ *workloadsv1alpha2.RoleInstance) {
			set.UID = "recreated-set"
			set.Status = workloadsv1alpha2.RoleInstanceSetStatus{}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			set := buildSet("replacement", 1, nil, nil)
			set.Status.RolloutReplacementState = &workloadsv1alpha2.RolloutReplacementState{UpdateRevision: testUpdateRev, Replicas: 1}
			instance := buildInst(set.Name, 0, testUpdateRev, false, true)
			if tt.change != nil {
				tt.change(set, instance)
			}
			partition, err := computePartition(set)
			if err != nil {
				t.Fatal(err)
			}
			before := set.DeepCopy()
			if got := rolloutReplacementStateActive(set, []*workloadsv1alpha2.RoleInstance{instance}, partition, int(*set.Spec.Replicas), testUpdateRev); got != tt.active {
				t.Fatalf("active=%v, want %v", got, tt.active)
			}
			if !reflect.DeepEqual(set, before) {
				t.Fatal("observation mutated the informer snapshot")
			}
		})
	}
}

func TestRolloutReplacementStateRangeChanges(t *testing.T) {
	for _, tt := range []struct {
		name                   string
		oldPartition, oldCount int32
		partition, replicas    int
		unreadyOrdinal         int
		missing                bool
		active                 bool
	}{
		{name: "lower partition protects overlapping replacement", oldPartition: 1, oldCount: 3, partition: 0, replicas: 3, unreadyOrdinal: 1, active: true},
		{name: "lower partition does not expand recorded scope", oldPartition: 1, oldCount: 3, partition: 0, replicas: 3, unreadyOrdinal: 0},
		{name: "raise partition protects overlapping replacement", oldCount: 3, partition: 1, replicas: 3, unreadyOrdinal: 2, active: true},
		{name: "raise partition excludes completed update range", oldCount: 3, partition: 1, replicas: 3, unreadyOrdinal: 0},
		{name: "scale down protects remaining replacement", oldCount: 3, replicas: 2, unreadyOrdinal: 1, active: true},
		{name: "scale down protects missing replacement", oldCount: 3, replicas: 2, unreadyOrdinal: 1, missing: true, active: true},
		{name: "scale down excludes removed replacement", oldCount: 3, replicas: 2, unreadyOrdinal: 2},
		{name: "scale up protects original replacement", oldCount: 2, replicas: 3, unreadyOrdinal: 1, active: true},
		{name: "scale up does not expand recorded scope", oldCount: 2, replicas: 3, unreadyOrdinal: 2},
		{name: "partition above recorded scope ends protection", oldCount: 2, partition: 2, replicas: 3, unreadyOrdinal: 2},
		{name: "scale down below recorded scope ends protection", oldPartition: 2, oldCount: 3, replicas: 2, unreadyOrdinal: 0},
		{name: "scale to zero ends protection", oldCount: 3, unreadyOrdinal: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			set := buildSet("scope", int32(tt.replicas), nil, nil)
			set.Status.RolloutReplacementState = &workloadsv1alpha2.RolloutReplacementState{
				UpdateRevision: testUpdateRev,
				Partition:      tt.oldPartition,
				Replicas:       tt.oldCount,
			}
			before := set.DeepCopy()
			instances := make([]*workloadsv1alpha2.RoleInstance, max(tt.replicas, int(tt.oldCount)))
			for ordinal := range instances {
				instances[ordinal] = buildInst(set.Name, ordinal, testUpdateRev, ordinal != tt.unreadyOrdinal, true)
			}
			if tt.missing {
				instances[tt.unreadyOrdinal] = nil
			}
			if got := rolloutReplacementStateActive(set, instances, tt.partition, tt.replicas, testUpdateRev); got != tt.active {
				t.Fatalf("active=%v, want %v", got, tt.active)
			}
			if !reflect.DeepEqual(set, before) {
				t.Fatal("observation changed the persisted replacement scope")
			}
		})
	}
}

func TestReplacementScopeRetainsFormerBaseAsSurgeAfterScaleDown(t *testing.T) {
	set := buildSet("scale-down", 2, ptr.To(intstrutil.FromInt32(1)), ptr.To(intstrutil.FromInt32(0)))
	set.Status.RolloutReplacementState = &workloadsv1alpha2.RolloutReplacementState{UpdateRevision: testUpdateRev, Replicas: 3}
	instances := []*workloadsv1alpha2.RoleInstance{
		buildInst(set.Name, 0, testUpdateRev, true, true),
		buildInst(set.Name, 1, testUpdateRev, false, true),
		buildInst(set.Name, 2, testUpdateRev, true, true),
		buildInst(set.Name, 3, testUpdateRev, true, true),
	}
	active := rolloutReplacementStateActive(set, instances, 0, 2, testUpdateRev)
	topo, err := computeTopology(set, instances, testUpdateRev, testUpdateRev, active)
	if err != nil {
		t.Fatal(err)
	}
	if !topo.inRollout || topo.endOrdinal != 3 {
		t.Fatalf("topology=%+v, want ordinal 2 retained as surge and ordinal 3 condemned", topo)
	}
	instances[1].Status.Conditions[0].Status = "True"
	active = rolloutReplacementStateActive(set, instances, 0, 2, testUpdateRev)
	topo, err = computeTopology(set, instances, testUpdateRev, testUpdateRev, active)
	if err != nil {
		t.Fatal(err)
	}
	if topo.inRollout || topo.endOrdinal != 2 {
		t.Fatalf("topology=%+v, want surge released after remaining replacement is ready", topo)
	}
}

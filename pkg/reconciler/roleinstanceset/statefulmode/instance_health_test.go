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
	"fmt"
	"maps"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancelisters "sigs.k8s.io/rbgs/client-go/listers/workloads/v1alpha2"
)

func TestObserveInstanceHealthSnapshot(t *testing.T) {
	resetInstanceUnhealthySince()
	t.Cleanup(resetInstanceUnhealthySince)
	set := buildSet("snapshot", 3, nil, nil)
	set.UID = "original-set"
	key := types.NamespacedName{Namespace: set.Namespace, Name: set.Name}
	first := time.Now().Add(-2 * stableUnhealthyDuration)
	instances := make([]*workloadsv1alpha2.RoleInstance, 0, 3)
	for ordinal := 0; ordinal < 3; ordinal++ {
		instance := buildInst(set.Name, ordinal, testOldRev, false, true)
		instances = append(instances, instance)
		setUnhealthySince(set, instance, first)
	}
	value, _ := instanceUnhealthySince.Load(key)
	previous := value.(*instanceHealthState)
	before := maps.Clone(previous.since)
	instances[1].Status.Conditions[0].Status = v1.ConditionTrue
	newInstance := buildInst(set.Name, 3, testOldRev, false, true)
	noUID := buildInst(set.Name, 4, testOldRev, false, false)

	observeInstanceHealth(set, []*workloadsv1alpha2.RoleInstance{instances[0], instances[1], newInstance, nil, noUID})

	value, _ = instanceUnhealthySince.Load(key)
	current := value.(*instanceHealthState)
	if current == previous || !maps.Equal(previous.since, before) {
		t.Fatal("observation mutated the previously published snapshot")
	}
	if len(current.since) != 2 || current.since[instances[0].UID] != first {
		t.Fatalf("observation must retain only current unhealthy instances and preserve their first timestamp: %v", current.since)
	}
	if got, ok := current.since[newInstance.UID]; !ok || !got.After(first) {
		t.Fatalf("new instance must start a fresh unhealthy window: %v", got)
	}

	for _, inputs := range [][]*workloadsv1alpha2.RoleInstance{nil, {instances[1]}, {nil, noUID}} {
		setUnhealthySince(set, instances[0], first)
		observeInstanceHealth(set, inputs)
		if _, ok := instanceUnhealthySince.Load(key); ok {
			t.Fatal("observation retained an empty health snapshot")
		}
		observeInstanceHealth(set, inputs)
		if _, ok := instanceUnhealthySince.Load(key); ok {
			t.Fatal("observation created an empty health snapshot")
		}
	}
}

func TestPruneInstanceHealthPreservesConcurrentObservation(t *testing.T) {
	for _, state := range []string{"deleted", "recreated"} {
		t.Run(state, func(t *testing.T) {
			resetInstanceUnhealthySince()
			t.Cleanup(resetInstanceUnhealthySince)
			resetEarlyRollbackReplacementSignals()
			t.Cleanup(resetEarlyRollbackReplacementSignals)
			set := buildSet("recreated", 1, nil, nil)
			set.UID = "old-set"
			instance := buildInst(set.Name, 0, testOldRev, false, true)
			setUnhealthySince(set, instance, time.Now().Add(-2*stableUnhealthyDuration))
			replacement := set.DeepCopy()
			replacement.UID = "new-set"
			indexer := &healthSnapshotIndexer{
				Indexer: cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{}),
				entered: make(chan struct{}),
				release: make(chan struct{}),
			}
			if state == "recreated" {
				if err := indexer.Add(replacement); err != nil {
					t.Fatal(err)
				}
			}
			controller := &ReconcileStatefulInstanceSet{roleInstanceSetLister: instancelisters.NewRoleInstanceSetLister(indexer)}
			done := make(chan struct{})
			go func() {
				defer close(done)
				controller.pruneInstanceHealth(time.Now())
			}()
			t.Cleanup(func() {
				close(indexer.release)
				<-done
			})
			select {
			case <-indexer.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("pruning did not reach the cache lookup")
			}

			// Publish the replacement after the sweep has captured the old snapshot.
			observeInstanceHealth(replacement, []*workloadsv1alpha2.RoleInstance{instance})
			first, ok := getUnhealthySince(replacement, instance)
			if !ok || isStablyUnhealthy(replacement, instance) {
				t.Fatal("replacement must start a fresh unhealthy window")
			}
			indexer.release <- struct{}{}
			<-done
			if got, ok := getUnhealthySince(replacement, instance); !ok || got != first {
				t.Fatal("pruning an old snapshot removed the concurrent observation")
			}
		})
	}
}

type healthSnapshotIndexer struct {
	cache.Indexer
	entered chan struct{}
	release chan struct{}
}

func (i *healthSnapshotIndexer) GetByKey(key string) (interface{}, bool, error) {
	item, exists, err := i.Indexer.GetByKey(key)
	close(i.entered)
	<-i.release
	return item, exists, err
}

func TestInstanceHealthConcurrentSets(t *testing.T) {
	resetInstanceUnhealthySince()
	t.Cleanup(resetInstanceUnhealthySince)
	resetEarlyRollbackReplacementSignals()
	t.Cleanup(resetEarlyRollbackReplacementSignals)
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	controller := &ReconcileStatefulInstanceSet{roleInstanceSetLister: instancelisters.NewRoleInstanceSetLister(indexer)}
	var workers sync.WaitGroup
	for ordinal := 0; ordinal < 4; ordinal++ {
		set := buildSet(fmt.Sprintf("set-%d", ordinal), 1, nil, nil)
		set.UID = types.UID(set.Name)
		if err := indexer.Add(set); err != nil {
			t.Fatal(err)
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			instance := buildInst(set.Name, 0, testOldRev, false, true)
			first := time.Now().Add(-2 * stableUnhealthyDuration)
			setUnhealthySince(set, instance, first)
			now := time.Now()
			for iteration := 0; iteration < 100; iteration++ {
				observeInstanceHealth(set, []*workloadsv1alpha2.RoleInstance{instance})
				controller.pruneInstanceHealth(now.Add(time.Duration(iteration) * instanceHealthPruneInterval))
				if got, ok := getUnhealthySince(set, instance); !ok || got != first {
					t.Errorf("concurrent observations changed %s's timer: %v", set.Name, got)
					return
				}
			}
		}()
	}
	workers.Wait()
}

func TestPruneInstanceHealthLooksUpEachSetOnce(t *testing.T) {
	resetInstanceUnhealthySince()
	t.Cleanup(resetInstanceUnhealthySince)
	resetEarlyRollbackReplacementSignals()
	t.Cleanup(resetEarlyRollbackReplacementSignals)
	indexer := &healthCountingIndexer{Indexer: cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})}
	for _, namespace := range []string{"first", "second"} {
		set := buildSet("shared-name", 3, nil, nil)
		set.Namespace = namespace
		set.UID = types.UID(namespace)
		if err := indexer.Add(set); err != nil {
			t.Fatal(err)
		}
		var instances []*workloadsv1alpha2.RoleInstance
		for ordinal := 0; ordinal < 3; ordinal++ {
			instances = append(instances, buildInst(set.Name, ordinal, testOldRev, false, true))
		}
		observeInstanceHealth(set, instances)
	}
	controller := &ReconcileStatefulInstanceSet{roleInstanceSetLister: instancelisters.NewRoleInstanceSetLister(indexer)}

	controller.pruneInstanceHealth(time.Now())

	if got := indexer.lookups.Load(); got != 2 {
		t.Fatalf("pruning looked up sets %d times, want one lookup per set (2)", got)
	}
}

// Seed timers without mutating a snapshot that concurrent readers may hold.
func setUnhealthySince(set *workloadsv1alpha2.RoleInstanceSet, instance *workloadsv1alpha2.RoleInstance, first time.Time) {
	key := types.NamespacedName{Namespace: set.Namespace, Name: set.Name}
	state := &instanceHealthState{setUID: set.UID, since: make(map[types.UID]time.Time)}
	if value, ok := instanceUnhealthySince.Load(key); ok {
		previous := value.(*instanceHealthState)
		if previous.setUID == set.UID {
			maps.Copy(state.since, previous.since)
		}
	}
	state.since[instance.UID] = first
	instanceUnhealthySince.Store(key, state)
}

func BenchmarkObserveInstanceHealth(b *testing.B) {
	for _, scenario := range []struct {
		current int
		other   int
		healthy bool
	}{
		{current: 10},
		{current: 10, other: 100},
		{current: 10, other: 10000},
		{current: 10000},
		{current: 10000, healthy: true},
	} {
		b.Run(fmt.Sprintf("current=%d/other=%d/healthy=%t", scenario.current, scenario.other, scenario.healthy), func(b *testing.B) {
			resetInstanceUnhealthySince()
			b.Cleanup(resetInstanceUnhealthySince)
			for ordinal := 0; ordinal < scenario.other; ordinal++ {
				set := buildSet(fmt.Sprintf("other-%d", ordinal), 1, nil, nil)
				instance := buildInst(set.Name, 0, testOldRev, false, true)
				observeInstanceHealth(set, []*workloadsv1alpha2.RoleInstance{instance})
			}
			set := buildSet("current", int32(scenario.current), nil, nil)
			instances := make([]*workloadsv1alpha2.RoleInstance, 0, scenario.current)
			for ordinal := 0; ordinal < scenario.current; ordinal++ {
				instances = append(instances, buildInst(set.Name, ordinal, testOldRev, scenario.healthy, true))
			}
			observeInstanceHealth(set, instances)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				observeInstanceHealth(set, instances)
			}
		})
	}
}

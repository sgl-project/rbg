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
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

type rollbackTest struct {
	set       *workloadsv1alpha2.RoleInstanceSet
	revisions []*apps.ControllerRevision
	objects   *fakeInstanceObjectManager
	control   *defaultStatefulInstanceSetControl
}

func newRollbackTest(t *testing.T, maxSurge, maxUnavailable int32) *rollbackTest {
	t.Helper()
	resetInstanceUnhealthySince()
	t.Cleanup(resetInstanceUnhealthySince)

	set := newRevisionTestSet("nginx:1.0")
	set.Spec.Replicas = ptr.To[int32](1)
	set.Spec.PodManagementPolicy = constants.ParallelPodManagement
	set.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": set.Name}}
	set.Spec.UpdateStrategy.MaxSurge = ptr.To(intstrutil.FromInt32(maxSurge))
	set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstrutil.FromInt32(maxUnavailable))
	revisions := make([]*apps.ControllerRevision, 0, 3)
	for i, image := range []string{"nginx:1.0", "nginx:2.0", "nginx:1.0"} {
		set.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Containers[0].Image = image
		revision, err := newRevision(set, int64(i+1), ptr.To[int32](0))
		if err != nil {
			t.Fatal(err)
		}
		revisions = append(revisions, revision)
	}
	if revisions[0].Name != revisions[2].Name || !bytes.Equal(revisions[0].Data.Raw, revisions[2].Data.Raw) {
		t.Fatal("rollback did not recreate revision A")
	}
	if revisions[0].Name == revisions[1].Name {
		t.Fatal("revision B must differ from revision A")
	}
	set.Status.CurrentRevision = revisions[0].Name
	set.Status.UpdateRevision = revisions[1].Name
	key := getInstanceSetKey(set)
	durationStore.Pop(key)
	updateExpectations.DeleteExpectations(key)
	t.Cleanup(func() {
		durationStore.Pop(key)
		updateExpectations.DeleteExpectations(key)
	})
	objects := &fakeInstanceObjectManager{}
	recorder := record.NewFakeRecorder(64)
	return &rollbackTest{
		set: set, revisions: revisions, objects: objects,
		control: &defaultStatefulInstanceSetControl{
			instanceControl: NewStatefulInstanceControlFromManager(objects, recorder),
			inplaceControl:  &fakeInplaceControl{},
			recorder:        recorder,
		},
	}
}

func (r *rollbackTest) reconcile(t *testing.T, instances ...*workloadsv1alpha2.RoleInstance) time.Duration {
	t.Helper()
	r.objects.created = nil
	r.objects.deleted = nil
	status, err := r.control.updateStatefulInstanceSet(context.Background(), r.set,
		r.revisions[0], r.revisions[2], 0, instances, r.revisions[1:])
	if err != nil {
		t.Fatal(err)
	}
	r.set.Status = *status
	return durationStore.Pop(getInstanceSetKey(r.set))
}

func TestEarlyRollbackReplacesHealthyBase(t *testing.T) {
	r := newRollbackTest(t, 0, 1)
	base := buildInst(r.set.Name, 0, r.revisions[1].Name, true, true)
	r.reconcile(t, base)
	if !slices.Equal(r.objects.deleted, []string{base.Name}) {
		t.Fatalf("deleted = %v, want stale base %s", r.objects.deleted, base.Name)
	}
	if len(r.objects.created) != 0 {
		t.Fatalf("created = %v, want no surge", r.objects.created)
	}
}

func TestEarlyRollbackRetriesUnhealthyBase(t *testing.T) {
	r := newRollbackTest(t, 0, 1)
	base := buildInst(r.set.Name, 0, r.revisions[1].Name, false, true)
	if wait := r.reconcile(t, base); wait <= 0 || wait > stableUnhealthyDuration {
		t.Fatalf("retry = %v, want a positive health-window delay", wait)
	}
	if len(r.objects.deleted) != 0 {
		t.Fatalf("deleted before the health window: %v", r.objects.deleted)
	}
	instanceUnhealthySince.Store(base.UID, time.Now().Add(-2*stableUnhealthyDuration))
	if wait := r.reconcile(t, base); wait != 0 {
		t.Fatalf("retry = %v after the health window", wait)
	}
	if !slices.Equal(r.objects.deleted, []string{base.Name}) {
		t.Fatalf("deleted = %v, want stale base %s", r.objects.deleted, base.Name)
	}
}

func TestEarlyRollbackKeepsSurgeUntilBaseIsReady(t *testing.T) {
	r := newRollbackTest(t, 1, 0)
	base := buildInst(r.set.Name, 0, r.revisions[1].Name, true, true)
	surge := buildInst(r.set.Name, 1, r.revisions[2].Name, true, true)
	r.reconcile(t, base)
	if !slices.Equal(r.objects.created, []string{surge.Name}) || len(r.objects.deleted) != 0 {
		t.Fatalf("created = %v, deleted = %v; want only surge %s created", r.objects.created, r.objects.deleted, surge.Name)
	}

	// Ready surge supplies the budget to replace B with A.
	r.reconcile(t, base, surge)
	if !slices.Equal(r.objects.deleted, []string{base.Name}) {
		t.Fatalf("deleted = %v, want only stale base %s", r.objects.deleted, base.Name)
	}
	withTerminating(base)
	r.reconcile(t, base, surge)
	if len(r.objects.deleted) != 0 {
		t.Fatalf("deleted surge while the base still terminates: %v", r.objects.deleted)
	}

	r.reconcile(t, surge)
	if !slices.Equal(r.objects.created, []string{base.Name}) || len(r.objects.deleted) != 0 {
		t.Fatalf("created = %v, deleted = %v; want only base %s recreated", r.objects.created, r.objects.deleted, base.Name)
	}
	base = buildInst(r.set.Name, 0, r.revisions[2].Name, false, true)
	base.UID = "recreated-base"
	base.Generation = 2
	r.reconcile(t, base, surge)
	if len(r.objects.deleted) != 0 {
		t.Fatalf("deleted surge before the new base is ready: %v", r.objects.deleted)
	}

	base.Status.Conditions[0].Status = v1.ConditionTrue
	base.Status.ObservedGeneration = 1
	r.reconcile(t, base, surge)
	if len(r.objects.deleted) != 0 {
		t.Fatalf("deleted surge before the base observed its generation: %v", r.objects.deleted)
	}
	base.Status.ObservedGeneration = base.Generation
	r.reconcile(t, base, surge)
	if !slices.Equal(r.objects.deleted, []string{surge.Name}) {
		t.Fatalf("deleted = %v, want only surplus instance %s", r.objects.deleted, surge.Name)
	}
}

func TestEarlyRollbackPauseKeepsExistingSurge(t *testing.T) {
	r := newRollbackTest(t, 1, 0)
	r.set.Spec.UpdateStrategy.Paused = true
	base := buildInst(r.set.Name, 0, r.revisions[1].Name, true, true)
	r.reconcile(t, base)
	if len(r.objects.created) != 0 || len(r.objects.deleted) != 0 {
		t.Fatalf("paused rollback changed instances: created %v, deleted %v", r.objects.created, r.objects.deleted)
	}
	surge := buildInst(r.set.Name, 1, r.revisions[2].Name, true, true)
	r.reconcile(t, base, surge)
	if len(r.objects.created) != 0 || len(r.objects.deleted) != 0 {
		t.Fatalf("paused rollback changed existing surge: created %v, deleted %v", r.objects.created, r.objects.deleted)
	}
	base = buildInst(r.set.Name, 0, r.revisions[2].Name, false, true)
	r.reconcile(t, base, surge)
	if len(r.objects.deleted) != 0 {
		t.Fatalf("paused rollback deleted surge before the base is ready: %v", r.objects.deleted)
	}
}

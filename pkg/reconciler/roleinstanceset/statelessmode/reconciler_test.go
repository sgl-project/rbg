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

package statelessmode

import (
	"fmt"
	"testing"

	apps "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestTruncateHistoryKeepsLiveShortHashRevision(t *testing.T) {
	limit := int32(0)
	set := &workloadsv1alpha2.RoleInstanceSet{
		Spec: workloadsv1alpha2.RoleInstanceSetSpec{
			RevisionHistoryLimit: &limit,
		},
	}
	revisions := []*apps.ControllerRevision{
		newStatelessRevision("instanceset-aaa111", 1),
		newStatelessRevision("instanceset-bbb222", 2),
		newStatelessRevision("instanceset-ccc333", 3),
	}
	instances := []*workloadsv1alpha2.RoleInstance{
		{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					apps.ControllerRevisionHashLabelKey: "aaa111",
				},
			},
		},
	}
	reconciler := &ReconcileInstanceSet{
		controllerHistory: &fakeStatelessHistory{},
	}

	err := reconciler.truncateHistory(set, instances, revisions, revisions[1], revisions[2])
	if err != nil {
		t.Fatalf("truncateHistory() error = %v", err)
	}

	history := reconciler.controllerHistory.(*fakeStatelessHistory)
	if len(history.deleted) != 0 {
		t.Fatalf("deleted revisions = %v, want none", history.deleted)
	}
}

func TestTruncateHistoryUsesDefaultLimitWhenRevisionHistoryLimitIsNil(t *testing.T) {
	set := &workloadsv1alpha2.RoleInstanceSet{}
	revisions := make([]*apps.ControllerRevision, 0, 13)
	for i := 1; i <= 13; i++ {
		revisions = append(revisions, newStatelessRevision(fmt.Sprintf("instanceset-rev-%d", i), int64(i)))
	}

	reconciler := &ReconcileInstanceSet{
		controllerHistory: &fakeStatelessHistory{},
	}

	err := reconciler.truncateHistory(set, nil, revisions, revisions[11], revisions[12])
	if err != nil {
		t.Fatalf("truncateHistory() error = %v", err)
	}

	history := reconciler.controllerHistory.(*fakeStatelessHistory)
	want := []string{"instanceset-rev-1"}
	if len(history.deleted) != len(want) {
		t.Fatalf("deleted revisions = %v, want %v", history.deleted, want)
	}
	for i := range want {
		if history.deleted[i] != want[i] {
			t.Fatalf("deleted[%d] = %q, want %q", i, history.deleted[i], want[i])
		}
	}
}

func newStatelessRevision(name string, revision int64) *apps.ControllerRevision {
	return &apps.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Revision:   revision,
	}
}

type fakeStatelessHistory struct {
	deleted []string
}

func (f *fakeStatelessHistory) ListControllerRevisions(metav1.Object, labels.Selector) ([]*apps.ControllerRevision, error) {
	return nil, nil
}

func (f *fakeStatelessHistory) CreateControllerRevision(metav1.Object, *apps.ControllerRevision, *int32) (*apps.ControllerRevision, error) {
	return nil, nil
}

func (f *fakeStatelessHistory) DeleteControllerRevision(revision *apps.ControllerRevision) error {
	f.deleted = append(f.deleted, revision.Name)
	return nil
}

func (f *fakeStatelessHistory) UpdateControllerRevision(*apps.ControllerRevision, int64) (*apps.ControllerRevision, error) {
	return nil, nil
}

func (f *fakeStatelessHistory) AdoptControllerRevision(metav1.Object, schema.GroupVersionKind, *apps.ControllerRevision) (*apps.ControllerRevision, error) {
	return nil, nil
}

func (f *fakeStatelessHistory) ReleaseControllerRevision(metav1.Object, *apps.ControllerRevision) (*apps.ControllerRevision, error) {
	return nil, nil
}

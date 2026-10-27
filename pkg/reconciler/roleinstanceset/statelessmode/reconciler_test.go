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
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestStatelessStatusPreservesRevisionAlgorithmAndExternalConditions(t *testing.T) {
	set := &workloadsv1alpha2.RoleInstanceSet{ObjectMeta: metav1.ObjectMeta{Name: "set", Namespace: "default", Generation: 2}}
	set.Spec.Replicas = ptr.To[int32](1)
	set.Status.CurrentRevision = "set-old"
	instance := &workloadsv1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{apps.ControllerRevisionHashLabelKey: "new"}, Generation: 2}}
	instance.Status.ObservedGeneration = 1
	instance.Status.CurrentRevision, instance.Status.UpdateRevision = "ri-rev", "ri-rev"
	instance.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue}}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	live := set.DeepCopy()
	live.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{Type: "External", Status: corev1.ConditionTrue, Reason: "LatestExternalReason"}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set).WithObjects(live).Build()
	writes := 0
	c := interceptor.NewClient(base, interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cli client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
		writes++
		return cli.SubResource(sub).Update(ctx, obj, opts...)
	}})
	updater := &realStatusUpdater{Client: c}
	status := &workloadsv1alpha2.RoleInstanceSetStatus{ObservedGeneration: 2, CurrentRevision: "set-old", UpdateRevision: "set-new"}
	updater.calculateStatus(set, status, []*workloadsv1alpha2.RoleInstance{instance})
	require.Equal(t, "set-new", status.CurrentRevision)
	require.Equal(t, workloadsv1alpha2.RoleInstanceSetPhaseUpdating, status.Phase)
	require.Equal(t, corev1.ConditionFalse, status.Conditions[0].Status)
	require.NoError(t, updater.updateStatus(set, status))
	got := &workloadsv1alpha2.RoleInstanceSet{}
	require.NoError(t, base.Get(context.Background(), client.ObjectKeyFromObject(set), got))
	require.Len(t, got.Status.Conditions, 3)
	require.Equal(t, "LatestExternalReason", got.Status.Conditions[0].Reason)
	require.NoError(t, updater.updateStatus(set, status))
	require.Equal(t, 1, writes)
	changed := got.Status.DeepCopy()
	changed.Phase = workloadsv1alpha2.RoleInstanceSetPhaseRunning
	require.True(t, updater.inconsistentStatus(got, changed))
	changed = got.Status.DeepCopy()
	changed.Conditions[1].Reason = "Changed"
	require.True(t, updater.inconsistentStatus(got, changed))
}

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

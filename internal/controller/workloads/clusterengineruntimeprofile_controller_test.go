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

package workloads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// controllerHarness builds the reference-tracking reconciler over a fake client
// with the status subresource enabled, plus a drained event recorder. The
// fake client does not maintain metadata.generation, so tests that need a
// generation drift set the field explicitly — the reconciler only reads it.
func controllerHarness(t *testing.T, objs ...client.Object) (
	*ClusterEngineRuntimeProfileReconciler, *record.FakeRecorder, client.Client,
) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.ClusterEngineRuntimeProfile{}).
		WithObjects(objs...).Build()
	recorder := record.NewFakeRecorder(32)
	return &ClusterEngineRuntimeProfileReconciler{Client: c, Recorder: recorder}, recorder, c
}

func cerpFor(name string) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
	return &workloadsv1alpha2.ClusterEngineRuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec:       workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{UpdateStrategy: "NoUpdate"},
	}
}

func controllerRBG(name, namespace, profileName string) *workloadsv1alpha2.RoleBasedGroup {
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{{
			Name:           "worker",
			EngineRuntimes: []workloadsv1alpha2.EngineRuntime{{ProfileName: profileName}},
		}}},
	}
}

func reconcileProfile(t *testing.T, ctx context.Context, r *ClusterEngineRuntimeProfileReconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	require.NoError(t, err)
}

func getProfile(t *testing.T, ctx context.Context, c client.Client, name string) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
	t.Helper()
	profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name}, profile))
	return profile
}

func frozenCondition(t *testing.T, profile *workloadsv1alpha2.ClusterEngineRuntimeProfile) *metav1.Condition {
	t.Helper()
	for i := range profile.Status.Conditions {
		if profile.Status.Conditions[i].Type == workloadsv1alpha2.FrozenConditionType {
			return &profile.Status.Conditions[i]
		}
	}
	return nil
}

func drainControllerEvents(recorder *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case ev := <-recorder.Events:
			events = append(events, ev)
		default:
			return events
		}
	}
}

// TestClusterEngineRuntimeProfileTracking pins the KEP-531 reference
// tracking: the none-to-referenced transition records the baseline
// generation and the Frozen condition, the baseline never refreshes while
// referenced, a live generation drifting from it flips the condition's
// reason and fires the bypass-detection warning once (not per reconcile),
// and the last reference leaving restores the unreferenced shape — count
// zero, baseline cleared, condition gone.
func TestClusterEngineRuntimeProfileTracking(t *testing.T) {
	ctx := context.Background()

	t.Run("an unreferenced profile carries no tracking state", func(t *testing.T) {
		r, recorder, c := controllerHarness(t, cerpFor("ik"))
		reconcileProfile(t, ctx, r, "ik")
		profile := getProfile(t, ctx, c, "ik")
		assert.Equal(t, int32(0), profile.Status.ReferenceCount)
		assert.Equal(t, int64(0), profile.Status.ReferencedGeneration)
		assert.Nil(t, frozenCondition(t, profile))
		assert.Empty(t, drainControllerEvents(recorder))
	})

	t.Run("the none-to-referenced transition records the baseline and freezes", func(t *testing.T) {
		r, recorder, c := controllerHarness(t,
			cerpFor("ik"),
			controllerRBG("grp", "ns-a", "ik"),
			controllerRBG("grp-two", "ns-b", "ik"),
		)
		reconcileProfile(t, ctx, r, "ik")
		profile := getProfile(t, ctx, c, "ik")
		assert.Equal(t, int32(2), profile.Status.ReferenceCount)
		assert.Equal(t, int64(1), profile.Status.ReferencedGeneration,
			"the baseline records metadata.generation when the profile becomes referenced")
		cond := frozenCondition(t, profile)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, workloadsv1alpha2.ReasonReferenced, cond.Reason)
		assert.Empty(t, drainControllerEvents(recorder), "a clean freeze is no bypass")
	})

	t.Run("generation drift flips the reason and warns once", func(t *testing.T) {
		r, recorder, c := controllerHarness(t,
			cerpFor("ik"),
			controllerRBG("grp", "ns-a", "ik"),
		)
		reconcileProfile(t, ctx, r, "ik")

		// a spec edit the webhook did not block: generation moves past the baseline
		profile := getProfile(t, ctx, c, "ik")
		profile.Generation = 2
		require.NoError(t, c.Update(ctx, profile))

		reconcileProfile(t, ctx, r, "ik")
		profile = getProfile(t, ctx, c, "ik")
		assert.Equal(t, int64(1), profile.Status.ReferencedGeneration,
			"the baseline never refreshes while referenced")
		cond := frozenCondition(t, profile)
		require.NotNil(t, cond)
		assert.Equal(t, workloadsv1alpha2.ReasonSpecChangedWhileReferenced, cond.Reason)
		events := drainControllerEvents(recorder)
		require.Len(t, events, 1, "the bypass warning fires once per drift")
		assert.Contains(t, events[0], ReasonEngineRuntimeProfileMutatedWhileReferenced)

		// the next reconcile re-observes the same drift and stays silent
		reconcileProfile(t, ctx, r, "ik")
		assert.Empty(t, drainControllerEvents(recorder),
			"the event is deduped on the condition's reason flip, not re-fired per reconcile")
	})

	t.Run("a new reference added to an already-frozen profile keeps the baseline", func(t *testing.T) {
		r, _, c := controllerHarness(t,
			cerpFor("ik"),
			controllerRBG("grp", "ns-a", "ik"),
		)
		reconcileProfile(t, ctx, r, "ik")

		profile := getProfile(t, ctx, c, "ik")
		profile.Generation = 3 // the profile drifted before this reference landed
		require.NoError(t, c.Update(ctx, profile))
		require.NoError(t, c.Create(ctx, controllerRBG("grp-two", "ns-b", "ik")))

		reconcileProfile(t, ctx, r, "ik")
		profile = getProfile(t, ctx, c, "ik")
		assert.Equal(t, int32(2), profile.Status.ReferenceCount)
		assert.Equal(t, int64(1), profile.Status.ReferencedGeneration,
			"a later reference never re-records the baseline")
	})

	t.Run("the last reference leaving clears the tracking state", func(t *testing.T) {
		r, _, c := controllerHarness(t,
			cerpFor("ik"),
			controllerRBG("grp", "ns-a", "ik"),
		)
		reconcileProfile(t, ctx, r, "ik")
		require.NoError(t, c.Delete(ctx, controllerRBG("grp", "ns-a", "ik")))
		reconcileProfile(t, ctx, r, "ik")
		profile := getProfile(t, ctx, c, "ik")
		assert.Equal(t, int32(0), profile.Status.ReferenceCount)
		assert.Equal(t, int64(0), profile.Status.ReferencedGeneration)
		assert.Nil(t, frozenCondition(t, profile))
	})

	t.Run("a profile mid-deletion is left alone", func(t *testing.T) {
		doomed := cerpFor("ik")
		now := metav1.Now()
		doomed.DeletionTimestamp = &now
		doomed.Finalizers = []string{"test.example.com/keep"} // the fake client refuses a deletionTimestamp without one
		r, recorder, c := controllerHarness(t, doomed)
		reconcileProfile(t, ctx, r, "ik")
		profile := getProfile(t, ctx, c, "ik")
		assert.Equal(t, int32(0), profile.Status.ReferenceCount,
			"tracking never races the delete")
		assert.Empty(t, drainControllerEvents(recorder))
	})
}

// TestProfilesReferencedBy pins the watch mapping: an RBG or RBGSet enqueue
// exactly the profiles its roles name, deduped, and a set's group template
// counts even at zero replicas.
func TestProfilesReferencedBy(t *testing.T) {
	spec := workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{
		{Name: "a", EngineRuntimes: []workloadsv1alpha2.EngineRuntime{
			{ProfileName: "ik"}, {ProfileName: "ik"}, {ProfileName: "torch"},
		}},
		{Name: "b", EngineRuntimes: []workloadsv1alpha2.EngineRuntime{{ProfileName: ""}}},
	}}

	requests := profilesReferencedBy(&spec)
	names := make([]string, len(requests))
	for i := range requests {
		names[i] = requests[i].Name
	}
	assert.Equal(t, []string{"ik", "torch"}, names,
		"deduped, blank profile names skipped, order preserved")

	assert.Empty(t, profilesReferencedBy(&workloadsv1alpha2.RoleBasedGroupSpec{}))
	assert.Nil(t, profilesReferencedByRBG(context.Background(), &workloadsv1alpha2.RoleBasedGroupSet{}))
	assert.Nil(t, profilesReferencedByRBGSet(context.Background(), &workloadsv1alpha2.RoleBasedGroup{}))
}

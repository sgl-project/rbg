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
	"errors"
	"strings"
	"sync"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"sigs.k8s.io/rbgs/api/workloads/constants"
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
	// The label-convergence gate is process-global; every harness starts
	// unconverged so the backfill scan actually runs.
	workloadsv1alpha2.SetEngineRuntimeLabelConverged(false)
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

func controllerRBGSet(name, namespace, profileName string) *workloadsv1alpha2.RoleBasedGroupSet {
	return &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{{
					Name:           "worker",
					EngineRuntimes: []workloadsv1alpha2.EngineRuntime{{ProfileName: profileName}},
				}}},
			},
			RolloutStrategy: &workloadsv1alpha2.GroupSetRolloutStrategy{},
		},
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

func TestEngineRuntimeLabelDriftRequeuesReferencedProfiles(t *testing.T) {
	ctx := context.Background()
	workloadsv1alpha2.SetEngineRuntimeLabelConverged(true)
	t.Cleanup(func() { workloadsv1alpha2.SetEngineRuntimeLabelConverged(false) })

	unlabeled := controllerRBG("grp-old", "ns-a", "ik")
	delete(unlabeled.Labels, constants.HasEngineRuntimesLabelKey)
	requests := profilesReferencedByRBG(ctx, unlabeled)
	require.Len(t, requests, 1)
	assert.Equal(t, "ik", requests[0].Name)
	assert.False(t, workloadsv1alpha2.EngineRuntimeLabelConverged(),
		"a label-only bypassed write must un-converge the gate before the queued reconcile runs")

	old := controllerRBG("grp", "ns-a", "ik")
	old.Labels = map[string]string{constants.HasEngineRuntimesLabelKey: constants.HasEngineRuntimesLabelValue}
	newObj := old.DeepCopy()
	delete(newObj.Labels, constants.HasEngineRuntimesLabelKey)
	assert.True(t, engineRuntimeLabelOrGenerationChanged().Update(event.UpdateEvent{
		ObjectOld: old,
		ObjectNew: newObj,
	}), "a label-only update must pass the watch predicate")
}

// TestClusterEngineRuntimeProfileLabelBackfill pins the upgrade path of the
// webhook's label-scoped reference lookup (#597): objects written before the
// has-engine-runtimes label shipped — or past the admission defaulters —
// get the label converged by the profile controller, in both directions.
func TestClusterEngineRuntimeProfileLabelBackfill(t *testing.T) {
	ctx := context.Background()
	referencing := controllerRBG("grp-old", "ns-a", "ik")
	stale := controllerRBG("grp-stale", "ns-a", "ik")
	stale.Spec.Roles[0].EngineRuntimes = nil
	stale.Labels = map[string]string{constants.HasEngineRuntimesLabelKey: "true"}

	r, _, c := controllerHarness(t, referencing, stale, cerpFor("ik"))
	reconcileProfile(t, ctx, r, "ik")

	got := &workloadsv1alpha2.RoleBasedGroup{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "grp-old"}, got))
	assert.Equal(t, "true", got.Labels[constants.HasEngineRuntimesLabelKey],
		"a referencing object written before the label must be backfilled")

	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "grp-stale"}, got))
	assert.NotContains(t, got.Labels, constants.HasEngineRuntimesLabelKey,
		"a stale label on an object that no longer references any profile must be removed")
}

func TestClusterEngineRuntimeProfileDriftWarningSurvivesBackfillFailure(t *testing.T) {
	ctx := context.Background()
	workloadsv1alpha2.SetEngineRuntimeLabelConverged(false)
	t.Cleanup(func() { workloadsv1alpha2.SetEngineRuntimeLabelConverged(false) })
	profile := cerpFor("ik")
	profile.Generation = 2
	profile.Status = workloadsv1alpha2.ClusterEngineRuntimeProfileStatus{
		ReferenceCount:       1,
		ReferencedGeneration: 1,
		Conditions: []metav1.Condition{{
			Type: workloadsv1alpha2.FrozenConditionType, Status: metav1.ConditionTrue,
			Reason: workloadsv1alpha2.ReasonReferenced, ObservedGeneration: 1,
			LastTransitionTime: metav1.Now(),
		}},
	}
	unlabeled := controllerRBG("grp-old", "ns-a", "ik")

	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	failures := 1
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.ClusterEngineRuntimeProfile{}).
		WithObjects(profile, unlabeled).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*workloadsv1alpha2.RoleBasedGroup); ok && failures > 0 {
					failures--
					return errors.New("failed calling webhook: no endpoints available")
				}
				return c.Patch(ctx, obj, p, opts...)
			},
		}).Build()
	recorder := record.NewFakeRecorder(32)
	r := &ClusterEngineRuntimeProfileReconciler{Client: c, Recorder: recorder}

	warnings := 0
	for i := 1; i <= 3; i++ {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "ik"}})
		if i == 1 {
			require.Error(t, err, "the failed backfill still returns an error for retry")
		} else {
			require.NoError(t, err)
		}
		for _, event := range drainControllerEvents(recorder) {
			if strings.Contains(event, ReasonEngineRuntimeProfileMutatedWhileReferenced) {
				warnings++
			}
		}
	}
	assert.Equal(t, 1, warnings,
		"the drift warning must survive a failed label backfill")
}

func TestClusterEngineRuntimeProfileLabelBackfillContinuesAfterPatchFailure(t *testing.T) {
	ctx := context.Background()
	bad := controllerRBG("grp-bad", "ns-a", "ik")
	good := controllerRBG("grp-good", "ns-a", "ik")

	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(bad, good).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
				if obj.GetName() == "grp-bad" {
					return errors.New("webhook rejected the patch")
				}
				return c.Patch(ctx, obj, p, opts...)
			},
		}).Build()
	workloadsv1alpha2.SetEngineRuntimeLabelConverged(false)
	t.Cleanup(func() { workloadsv1alpha2.SetEngineRuntimeLabelConverged(false) })
	r := &ClusterEngineRuntimeProfileReconciler{Client: c, Recorder: record.NewFakeRecorder(32)}

	err := r.backfillHasEngineRuntimesLabels(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "grp-bad")

	got := &workloadsv1alpha2.RoleBasedGroup{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "grp-good"}, got))
	assert.Equal(t, constants.HasEngineRuntimesLabelValue, got.Labels[constants.HasEngineRuntimesLabelKey],
		"a patch failure on one object must not stop the rest of the backfill scan")
	assert.False(t, workloadsv1alpha2.EngineRuntimeLabelConverged(),
		"convergence must wait until every mismatching object is patched")
}

func TestClusterEngineRuntimeProfileLabelBackfillSkipsLegacyStrategyObjects(t *testing.T) {
	ctx := context.Background()
	legacy := controllerRBG("grp-legacy", "ns-a", "ik")
	legacy.Spec.Roles[0].RolloutStrategy = &workloadsv1alpha2.RolloutStrategy{
		Type:          workloadsv1alpha2.RollingUpdateStrategyType,
		RollingUpdate: &workloadsv1alpha2.RollingUpdate{},
	}

	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	patches := 0
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(legacy).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
				patches++
				return c.Patch(ctx, obj, p, opts...)
			},
		}).Build()
	recorder := record.NewFakeRecorder(32)
	workloadsv1alpha2.SetEngineRuntimeLabelConverged(false)
	t.Cleanup(func() { workloadsv1alpha2.SetEngineRuntimeLabelConverged(false) })
	r := &ClusterEngineRuntimeProfileReconciler{Client: c, Recorder: recorder}

	require.NoError(t, r.backfillHasEngineRuntimesLabels(ctx))
	assert.Equal(t, 0, patches,
		"the backfill must not patch an object whose stored strategy the mutating webhook would heal")
	got := &workloadsv1alpha2.RoleBasedGroup{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "grp-legacy"}, got))
	assert.NotContains(t, got.Labels, constants.HasEngineRuntimesLabelKey,
		"a skipped object must stay unlabeled so admission keeps the unscoped lookup")
	assert.False(t, workloadsv1alpha2.EngineRuntimeLabelConverged(),
		"the gate must not converge past a skipped referencing object")

	events := drainControllerEvents(recorder)
	assert.Len(t, events, 1)
	assert.Contains(t, events[0], ReasonEngineRuntimeLabelBackfillSkipped)
}

func TestClusterEngineRuntimeProfileLabelBackfillSkipsLegacyStrategySets(t *testing.T) {
	ctx := context.Background()
	legacy := controllerRBGSet("set-legacy", "ns-a", "ik")

	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	patches := 0
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(legacy).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
				patches++
				return c.Patch(ctx, obj, p, opts...)
			},
		}).Build()
	recorder := record.NewFakeRecorder(32)
	workloadsv1alpha2.SetEngineRuntimeLabelConverged(false)
	t.Cleanup(func() { workloadsv1alpha2.SetEngineRuntimeLabelConverged(false) })
	r := &ClusterEngineRuntimeProfileReconciler{Client: c, Recorder: recorder}

	require.NoError(t, r.backfillHasEngineRuntimesLabels(ctx))
	assert.Equal(t, 0, patches,
		"the backfill must not patch a set whose stored strategy the mutating webhook would heal")
	got := &workloadsv1alpha2.RoleBasedGroupSet{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "set-legacy"}, got))
	assert.NotContains(t, got.Labels, constants.HasEngineRuntimesLabelKey,
		"a skipped set must stay unlabeled so admission keeps the unscoped lookup")
	assert.False(t, workloadsv1alpha2.EngineRuntimeLabelConverged(),
		"the gate must not converge past a skipped referencing set")

	events := drainControllerEvents(recorder)
	assert.Len(t, events, 1)
	assert.Contains(t, events[0], ReasonEngineRuntimeLabelBackfillSkipped)
}

func TestClusterEngineRuntimeProfileLabelBackfillWarningIsConcurrencySafe(t *testing.T) {
	ctx := context.Background()
	profiles := []*workloadsv1alpha2.ClusterEngineRuntimeProfile{cerpFor("ik-a"), cerpFor("ik-b")}
	legacy := controllerRBG("grp-legacy", "ns-a", "ik-a")
	legacy.Spec.Roles[0].EngineRuntimes[0].ProfileName = "ik-b"
	legacy.Spec.Roles[0].RolloutStrategy = &workloadsv1alpha2.RolloutStrategy{
		Type:          workloadsv1alpha2.RollingUpdateStrategyType,
		RollingUpdate: &workloadsv1alpha2.RollingUpdate{},
	}

	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.ClusterEngineRuntimeProfile{}).
		WithObjects(profiles[0], profiles[1], legacy).Build()
	recorder := record.NewFakeRecorder(64)
	workloadsv1alpha2.SetEngineRuntimeLabelConverged(false)
	t.Cleanup(func() { workloadsv1alpha2.SetEngineRuntimeLabelConverged(false) })
	r := &ClusterEngineRuntimeProfileReconciler{Client: c, Recorder: recorder}

	const workers = 2
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: profiles[i%len(profiles)].Name}})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	assert.Equal(t, 1, len(drainControllerEvents(recorder)),
		"concurrent profile reconciles must emit one warning for the skipped object")
}

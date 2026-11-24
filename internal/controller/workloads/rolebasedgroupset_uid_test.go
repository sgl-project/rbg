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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func TestGroupSetForOrphanRBG(t *testing.T) {
	for _, tt := range []struct {
		name        string
		setName     string
		owner       *metav1.OwnerReference
		terminating bool
		wantRequest bool
	}{
		{name: "orphan", setName: "demo", wantRequest: true},
		{name: "terminating orphan", setName: "demo", terminating: true, wantRequest: true},
		{name: "missing set label"},
		{name: "non-controller owner", setName: "demo", owner: &metav1.OwnerReference{
			APIVersion: "v1", Kind: "ConfigMap", Name: "other", UID: "other", Controller: ptr.To(false),
		}, wantRequest: true},
		{name: "owned by set", setName: "demo", owner: &metav1.OwnerReference{
			APIVersion: workloadsv1alpha2.GroupVersion.String(), Kind: "RoleBasedGroupSet", Name: "demo", UID: "set", Controller: ptr.To(true),
		}},
		{name: "foreign controller", setName: "demo", owner: &metav1.OwnerReference{
			APIVersion: "apps/v1", Kind: "StatefulSet", Name: "other", UID: "other", Controller: ptr.To(true),
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			child := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{
				Name: "demo-1", Namespace: "test-namespace",
			}}
			if tt.setName != "" {
				child.Labels = map[string]string{constants.GroupSetNameLabelKey: tt.setName}
			}
			if tt.owner != nil {
				child.OwnerReferences = []metav1.OwnerReference{*tt.owner}
			}
			if tt.terminating {
				now := metav1.Now()
				child.DeletionTimestamp = &now
			}
			var expected []ctrl.Request
			if tt.wantRequest {
				expected = []ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: child.Namespace, Name: tt.setName}}}
			}
			assert.Equal(t, expected, groupSetForOrphanRBG(context.Background(), child))
		})
	}
}

func TestRoleBasedGroupSetReconciler_ForeignChildUID(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))

	role := wrappersv2.BuildStandaloneRole("worker").Obj()
	set := &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", UID: "new-set-uid"},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			Replicas:      ptr.To[int32](1),
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{role}}},
		},
	}

	t.Run("foreign child is not counted as ready", func(t *testing.T) {
		oldSet := set.DeepCopy()
		oldSet.UID = "old-set-uid"
		child := newRBGForSet(set, 0)
		child.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(oldSet, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroupSet"))}
		child.Status.Conditions = []metav1.Condition{{Type: string(workloadsv1alpha2.RoleBasedGroupReady), Status: metav1.ConditionTrue, Reason: "Ready"}}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set, child).WithObjects(set, child).Build()
		r := &RoleBasedGroupSetReconciler{client: c, scheme: scheme}

		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(set)})
		// The reconcile succeeds — it simply skips the foreign child during listing and scaleUp.
		require.NoError(t, err)

		after := &workloadsv1alpha2.RoleBasedGroupSet{}
		require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(set), after))
		assert.Equal(t, int32(0), after.Status.ReadyReplicas, "foreign child should not be counted as ready")
	})

	t.Run("foreign child is not deleted on scale-down", func(t *testing.T) {
		oldSet := set.DeepCopy()
		oldSet.UID = "old-set-uid"
		scaledDown := set.DeepCopy()
		scaledDown.Spec.Replicas = ptr.To[int32](0)
		child := newRBGForSet(set, 0)
		child.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(oldSet, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroupSet"))}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(scaledDown, child).WithObjects(scaledDown, child).Build()
		r := &RoleBasedGroupSetReconciler{client: c, scheme: scheme}

		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(scaledDown)})
		require.NoError(t, err)

		err = c.Get(context.Background(), client.ObjectKeyFromObject(child), &workloadsv1alpha2.RoleBasedGroup{})
		assert.NoError(t, err, "foreign child should not be deleted")
	})

	t.Run("orphan with correct label is adopted", func(t *testing.T) {
		child := newRBGForSet(set, 0)
		child.OwnerReferences = nil

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set, child).WithObjects(set, child).Build()
		r := &RoleBasedGroupSetReconciler{client: c, apiReader: c, scheme: scheme}

		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(set)})
		require.NoError(t, err)

		after := &workloadsv1alpha2.RoleBasedGroup{}
		require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(child), after))
		assert.True(t, metav1.IsControlledBy(after, set), "orphan child should be adopted by the Set")
	})

	t.Run("own child is managed normally", func(t *testing.T) {
		child := newRBGForSet(set, 0)
		child.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(set, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroupSet"))}

		assert.True(t, (&RoleBasedGroupSetReconciler{}).isOwnedOrAdoptable(set, child), "own child should be managed")
	})
}

// A rolling set must claim the orphans a cascade=orphan deletion left behind, exactly like the
// legacy path does. Filtering them out hides an occupied ordinal, so the create for it collides
// and aborts the fill loop before the ordinals above it are ever created.
func TestRollingGroupSetAdoptsOrphansAndFillsHigherOrdinals(t *testing.T) {
	ctx := context.Background()
	template := revisionTestTemplate(nil)
	set := revisionTestRollingSet(3, &groupSetRevision{template: template})
	objects := []client.Object{set}
	for index := 0; index < 2; index++ {
		orphan := revisionTestReadyChild(set, index, &groupSetRevision{template: template})
		orphan.OwnerReferences = nil
		orphan.Labels[constants.GroupSetRevisionLabelKey] = "rbgs-deleted-predecessor"
		objects = append(objects, orphan)
	}
	r, _ := revisionTestRollingReconciler(t, objects...)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))

	result, err := r.reconcileRollingGroupSet(ctx, client.ObjectKeyFromObject(set))
	require.NoError(t, err)
	assert.Positive(t, result.RequeueAfter, "the adopted orphans still have to roll")

	for index := 0; index < 2; index++ {
		orphan := revisionTestGetChild(t, r, set, index)
		assert.True(t, metav1.IsControlledBy(orphan, set), "ordinal %d must be adopted", index)
		assert.Equal(t, types.UID(fmt.Sprintf("original-%s", orphan.Name)), orphan.UID,
			"adoption must not recreate the child")
		assert.Equal(t, "rbgs-deleted-predecessor", orphan.Labels[constants.GroupSetRevisionLabelKey],
			"adoption must not apply the new template outside the rollout budget")
	}
	assert.True(t, metav1.IsControlledBy(revisionTestGetChild(t, r, set, 2), set),
		"the ordinal above the orphans must still be filled")

	stored := &workloadsv1alpha2.RoleBasedGroupSet{}
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), stored))
	assert.Equal(t, int32(3), stored.Status.Replicas)
	assert.Equal(t, int32(2), stored.Status.ReadyReplicas)
}

// A leftover of a same-named predecessor belongs to that UID; adopting it would steal another
// set's child, so the collision stays an error instead.
func TestRollingGroupSetDoesNotAdoptForeignChildren(t *testing.T) {
	ctx := context.Background()
	template := revisionTestTemplate(nil)
	set := revisionTestRollingSet(1, &groupSetRevision{template: template})
	predecessor := set.DeepCopy()
	predecessor.UID = "deleted-set-uid"
	foreign := revisionTestReadyChild(set, 0, &groupSetRevision{template: template})
	foreign.OwnerReferences = []metav1.OwnerReference{
		*metav1.NewControllerRef(predecessor, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroupSet")),
	}
	r, _ := revisionTestRollingReconciler(t, set, foreign)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))

	_, err := r.reconcileRollingGroupSet(ctx, client.ObjectKeyFromObject(set))
	require.ErrorContains(t, err, "not owned")

	after := revisionTestGetChild(t, r, set, 0)
	assert.Equal(t, foreign.UID, after.UID)
	assert.Equal(t, types.UID("deleted-set-uid"), metav1.GetControllerOfNoCopy(after).UID)
}

// An orphan created between the listing and the fill is only visible to the create collision, so
// that path adopts it too rather than reporting a child the set is supposed to own.
func TestCreateRollingGroupSetChildAdoptsOrphan(t *testing.T) {
	ctx := context.Background()
	template := revisionTestTemplate(nil)
	set := revisionTestRollingSet(1, &groupSetRevision{template: template})
	orphan := revisionTestReadyChild(set, 0, &groupSetRevision{template: template})
	orphan.OwnerReferences = nil
	r, _ := revisionTestRollingReconciler(t, set, orphan)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))
	revision, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
	require.NoError(t, err)

	require.NoError(t, r.createRollingGroupSetChild(ctx, set, newRBGForSetRevision(set, 0, revision)))

	adopted := revisionTestGetChild(t, r, set, 0)
	assert.Equal(t, orphan.UID, adopted.UID, "adoption must reuse the orphan")
	assert.True(t, metav1.IsControlledBy(adopted, set))
	assert.Empty(t, adopted.Labels[constants.GroupSetRevisionLabelKey],
		"adoption must not apply the new template outside the rollout budget")
}

// The create collision re-reads the child but not the set, so it must not adopt an orphan for a
// set the API server already reports as deleting.
func TestCreateRollingGroupSetChildDoesNotAdoptForDeletingSet(t *testing.T) {
	ctx := context.Background()
	template := revisionTestTemplate(nil)
	set := revisionTestRollingSet(1, &groupSetRevision{template: template})
	// The API server adds this finalizer for an orphan delete; the fake client does not.
	set.Finalizers = []string{metav1.FinalizerOrphanDependents}
	orphan := revisionTestReadyChild(set, 0, &groupSetRevision{template: template})
	orphan.OwnerReferences = nil
	r, _ := revisionTestRollingReconciler(t, set, orphan)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))
	revision, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
	require.NoError(t, err)
	require.NoError(t, r.client.Delete(ctx, set.DeepCopy(), client.PropagationPolicy(metav1.DeletePropagationOrphan)))

	err = r.createRollingGroupSetChild(ctx, set, newRBGForSetRevision(set, 0, revision))
	assert.ErrorContains(t, err, "cannot adopt RoleBasedGroups")
	assert.Nil(t, metav1.GetControllerOfNoCopy(revisionTestGetChild(t, r, set, 0)),
		"a deleting set must not adopt the orphan")
}

// A cascade=orphan delete marks the set deleting before the owner references of its children are
// stripped. A reconcile that read the set before the delete and lists the children after the strip
// must not adopt them back, or the same-named replacement can never claim them.
func TestGroupSetDoesNotReadoptChildrenReleasedByItsOrphanDelete(t *testing.T) {
	for _, rolling := range []bool{true, false} {
		t.Run(fmt.Sprintf("rolling=%t", rolling), func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			require.NoError(t, appsv1.AddToScheme(scheme))
			require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
			template := revisionTestTemplate(nil)
			newSet := func(uid types.UID) *workloadsv1alpha2.RoleBasedGroupSet {
				set := revisionTestRollingSet(2, &groupSetRevision{template: template})
				set.UID = uid
				if !rolling {
					set.Spec.RolloutStrategy = nil
				}
				return set
			}
			predecessor := newSet("deleted-set-uid")
			// The API server adds this finalizer for an orphan delete; the fake client does not.
			predecessor.Finalizers = []string{metav1.FinalizerOrphanDependents}
			objects := []client.Object{predecessor}
			for index := 0; index < 2; index++ {
				objects = append(objects, revisionTestReadyChild(predecessor, index, &groupSetRevision{template: template}))
			}
			key := client.ObjectKeyFromObject(predecessor)

			deleteBeforeListing := true
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
				WithStatusSubresource(&workloadsv1alpha2.RoleBasedGroup{}, &workloadsv1alpha2.RoleBasedGroupSet{}).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, inner client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, ok := list.(*workloadsv1alpha2.RoleBasedGroupList); ok && deleteBeforeListing {
							deleteBeforeListing = false
							// The orphan delete lands after the reconcile read the set: the set is
							// marked deleting, then the garbage collector releases its children.
							set := &workloadsv1alpha2.RoleBasedGroupSet{}
							require.NoError(t, inner.Get(ctx, key, set))
							require.NoError(t, inner.Delete(ctx, set, client.PropagationPolicy(metav1.DeletePropagationOrphan)))
							for index := 0; index < 2; index++ {
								child := &workloadsv1alpha2.RoleBasedGroup{}
								require.NoError(t, inner.Get(ctx, client.ObjectKey{
									Namespace: key.Namespace, Name: fmt.Sprintf("%s-%d", key.Name, index),
								}, child))
								child.OwnerReferences = nil
								require.NoError(t, inner.Update(ctx, child))
							}
						}
						return inner.List(ctx, list, opts...)
					},
				}).Build()
			r := &RoleBasedGroupSetReconciler{client: c, apiReader: c, scheme: scheme}

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			assert.ErrorContains(t, err, "cannot adopt RoleBasedGroups")
			for index := 0; index < 2; index++ {
				assert.Nil(t, metav1.GetControllerOfNoCopy(revisionTestGetChild(t, r, predecessor, index)),
					"the deleting set must not adopt ordinal %d back", index)
			}

			deleting := &workloadsv1alpha2.RoleBasedGroupSet{}
			require.NoError(t, c.Get(ctx, key, deleting))
			deleting.Finalizers = nil
			require.NoError(t, c.Update(ctx, deleting))
			replacement := newSet("replacement-set-uid")
			require.NoError(t, c.Create(ctx, replacement))

			_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			require.NoError(t, err)
			for index := 0; index < 2; index++ {
				child := revisionTestGetChild(t, r, replacement, index)
				assert.True(t, metav1.IsControlledBy(child, replacement), "the replacement must adopt ordinal %d", index)
				assert.Equal(t, types.UID("original-"+child.Name), child.UID, "adoption must not recreate ordinal %d", index)
			}
		})
	}
}

// An orphan derives the legacy discovery config mode while it has no controller, and
// the metadata sync keeps the mode a child carries, so the adoption is the one place
// that can undo it (#603). An orphan adopted before that derivation has no mode, and
// an owner reference change alone does not enqueue the RoleBasedGroup, so the
// adoption pins that one too. A mode the template declares is stamped by the same
// write. A legacy that was set rather than derived, here by the template of the set
// that owned the orphan before, cannot be told apart and is replaced too; the
// adopting set has to declare legacy to keep it. Both paths adopt through
// claimGroupSetOrphan.
func TestGroupSetAdoptionPinsRefineDiscoveryMode(t *testing.T) {
	modeKey := constants.DiscoveryConfigModeAnnotationKey
	legacy := string(constants.LegacyDiscoveryConfigMode)
	refine := string(constants.RefineDiscoveryConfigMode)

	tests := []struct {
		name         string
		templateMode string
		// previousTemplateMode builds the orphan from the template of the set that
		// owned it before, so its mode was set by that template, not derived.
		previousTemplateMode string
		orphanMode           string
		wantMode             string
		wantPresent          bool
	}{
		{name: "legacy becomes refine when the template declares no mode", orphanMode: legacy, wantMode: refine, wantPresent: true},
		{name: "legacy the previous set's template declared becomes refine when the template declares no mode", previousTemplateMode: legacy, wantMode: refine, wantPresent: true},
		{name: "legacy the previous set's template declared is kept when the template declares it too", templateMode: legacy, previousTemplateMode: legacy, wantMode: legacy, wantPresent: true},
		{name: "legacy declared by the template is kept", templateMode: legacy, orphanMode: legacy, wantMode: legacy, wantPresent: true},
		{name: "refine on the orphan is kept", orphanMode: refine, wantMode: refine, wantPresent: true},
		// The claim is the only write: nothing else would make the RoleBasedGroup
		// controller look at an orphan that otherwise matches the template.
		{name: "an orphan without a mode is pinned to refine", wantMode: refine, wantPresent: true},
		// Stamped by the claim, not left to the metadata sync: the rolling subtest
		// runs the claim alone.
		{name: "an orphan without a mode gets the legacy its template declares", templateMode: legacy, wantMode: legacy, wantPresent: true},
		{name: "refine on the orphan gives way to the legacy its template declares", templateMode: legacy, orphanMode: refine, wantMode: legacy, wantPresent: true},
	}

	for _, tt := range tests {
		t.Run("static/"+tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
			role := wrappersv2.BuildStandaloneRole("worker").Obj()
			set := &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", UID: "set-uid"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					Replicas: ptr.To[int32](1),
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Annotations: map[string]string{"team": "a"},
						Spec:        workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{role}},
					},
				},
			}
			if tt.templateMode != "" {
				set.Spec.GroupTemplate.Annotations[modeKey] = tt.templateMode
			}
			orphan := newRBGForSet(set, 0)
			delete(orphan.Annotations, modeKey)
			if tt.previousTemplateMode != "" {
				previous := set.DeepCopy()
				previous.UID = "previous-set-uid"
				previous.Spec.GroupTemplate.Annotations[modeKey] = tt.previousTemplateMode
				orphan = newRBGForSet(previous, 0)
				require.Equal(t, tt.previousTemplateMode, orphan.Annotations[modeKey])
			}
			orphan.OwnerReferences = nil
			if tt.orphanMode != "" {
				orphan.Annotations[modeKey] = tt.orphanMode
			}

			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set, orphan).WithObjects(set, orphan).Build()
			r := &RoleBasedGroupSetReconciler{client: c, apiReader: c, scheme: scheme}
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(set)})
			require.NoError(t, err)

			adopted := &workloadsv1alpha2.RoleBasedGroup{}
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(orphan), adopted))
			require.True(t, metav1.IsControlledBy(adopted, set), "the orphan must be adopted")
			mode, present := adopted.Annotations[modeKey]
			assert.Equal(t, tt.wantPresent, present)
			assert.Equal(t, tt.wantMode, mode)
			assert.Equal(t, "a", adopted.Annotations["team"], "other annotations must survive the adoption")
		})

		t.Run("rolling/"+tt.name, func(t *testing.T) {
			ctx := context.Background()
			template := revisionTestTemplate(nil)
			if tt.templateMode != "" {
				template.Annotations = map[string]string{modeKey: tt.templateMode}
			}
			set := revisionTestRollingSet(1, &groupSetRevision{template: template})
			orphan := revisionTestReadyChild(set, 0, &groupSetRevision{template: template})
			delete(orphan.Annotations, modeKey)
			if tt.previousTemplateMode != "" {
				previous := revisionTestTemplate(nil)
				previous.Annotations = map[string]string{modeKey: tt.previousTemplateMode}
				orphan = revisionTestReadyChild(set, 0, &groupSetRevision{template: previous})
				require.Equal(t, tt.previousTemplateMode, orphan.Annotations[modeKey])
			}
			orphan.OwnerReferences = nil
			if tt.orphanMode != "" {
				if orphan.Annotations == nil {
					orphan.Annotations = map[string]string{}
				}
				orphan.Annotations[modeKey] = tt.orphanMode
			}
			r, _ := revisionTestRollingReconciler(t, set, orphan)
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(orphan), orphan))

			require.NoError(t, r.adoptGroupSetChild(ctx, set, orphan))

			adopted := revisionTestGetChild(t, r, set, 0)
			require.True(t, metav1.IsControlledBy(adopted, set), "the orphan must be adopted")
			mode, present := adopted.Annotations[modeKey]
			assert.Equal(t, tt.wantPresent, present)
			assert.Equal(t, tt.wantMode, mode)
		})
	}
}

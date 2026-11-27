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

// The set a create collision is handed can predate the set's own deletion, so the set must be
// re-read before the orphan the collision found is adopted.
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

// The rolling path never claims a terminating orphan, so such an orphan must not trigger the set
// re-read. Here that re-read would fail the reconcile, because the set is being deleted.
func TestAdoptGroupSetOrphansIgnoresTerminatingOrphans(t *testing.T) {
	ctx := context.Background()
	template := revisionTestTemplate(nil)
	set := revisionTestRollingSet(1, &groupSetRevision{template: template})
	// The API server adds this finalizer for an orphan delete; the fake client does not.
	set.Finalizers = []string{metav1.FinalizerOrphanDependents}
	orphan := revisionTestReadyChild(set, 0, &groupSetRevision{template: template})
	orphan.OwnerReferences = nil
	orphan.Finalizers = []string{"test.io/hold"}
	r, _ := revisionTestRollingReconciler(t, set, orphan)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))
	require.NoError(t, r.client.Delete(ctx, orphan.DeepCopy()))
	require.NoError(t, r.client.Delete(ctx, set.DeepCopy(), client.PropagationPolicy(metav1.DeletePropagationOrphan)))
	children, err := r.listRollingGroupSetChildren(ctx, set)
	require.NoError(t, err)
	require.Len(t, children.Items, 1)
	require.False(t, children.Items[0].DeletionTimestamp.IsZero())

	adopted, err := r.adoptGroupSetOrphans(ctx, set, children)
	require.NoError(t, err)
	assert.False(t, adopted)
	assert.Nil(t, metav1.GetControllerOfNoCopy(revisionTestGetChild(t, r, set, 0)))
}

// A cascade=orphan delete marks the set deleting before the owner references of its children are
// stripped. A reconcile that read the set before the delete and lists the children after the strip
// must leave them alone: adopting them back keeps the same-named replacement from ever claiming
// them, and the legacy path would delete one whose ordinal label is missing.
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
			unlabeled := revisionTestReadyChild(predecessor, 2, &groupSetRevision{template: template})
			delete(unlabeled.Labels, constants.GroupSetIndexLabelKey)
			objects = append(objects, unlabeled)
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
							for index := 0; index < 3; index++ {
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
			for index := 0; index < 3; index++ {
				assert.Nil(t, metav1.GetControllerOfNoCopy(revisionTestGetChild(t, r, predecessor, index)),
					"the deleting set must leave released child %d alone", index)
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

// Adoption stamps the template-owned discovery-config-version in the same write as
// ownership. An owner reference alone does not enqueue the RoleBasedGroup
// controller, so a matching orphan's ConfigMap could otherwise stay stale (#603).
// The retired mode annotation is compatibility metadata and must not select behavior.
func TestGroupSetAdoptionStampsDiscoveryConfigVersion(t *testing.T) {
	versionKey := constants.DiscoveryConfigVersionAnnotationKey
	v1alpha1 := string(constants.DiscoveryConfigVersionV1Alpha1)
	v1 := string(constants.DiscoveryConfigVersionV1)

	tests := []struct {
		name            string
		templateVersion string
		orphanVersion   string
		wantVersion     string
	}{
		{name: "implicit historical content is stamped", wantVersion: v1alpha1},
		{name: "template version wins", templateVersion: v1, wantVersion: v1},
		{name: "orphan version converges to the template", templateVersion: v1, orphanVersion: v1alpha1, wantVersion: v1},
		{name: "missing template returns the orphan to historical content", orphanVersion: v1, wantVersion: v1alpha1},
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
			if tt.templateVersion != "" {
				set.Spec.GroupTemplate.Annotations[versionKey] = tt.templateVersion
			}
			orphan := newRBGForSet(set, 0)
			delete(orphan.Annotations, versionKey)
			orphan.OwnerReferences = nil
			if tt.orphanVersion != "" {
				orphan.Annotations[versionKey] = tt.orphanVersion
			}
			orphan.Annotations[constants.DiscoveryConfigModeAnnotationKey] = "legacy"

			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set, orphan).WithObjects(set, orphan).Build()
			r := &RoleBasedGroupSetReconciler{client: c, apiReader: c, scheme: scheme}
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(set)})
			require.NoError(t, err)

			adopted := &workloadsv1alpha2.RoleBasedGroup{}
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(orphan), adopted))
			require.True(t, metav1.IsControlledBy(adopted, set), "the orphan must be adopted")
			assert.Equal(t, tt.wantVersion, adopted.Annotations[versionKey])
			assert.Equal(t, "legacy", adopted.Annotations[constants.DiscoveryConfigModeAnnotationKey],
				"the retired mode annotation must remain inert compatibility metadata")
			assert.Equal(t, "a", adopted.Annotations["team"], "other annotations must survive the adoption")
		})

		t.Run("rolling/"+tt.name, func(t *testing.T) {
			ctx := context.Background()
			template := revisionTestTemplate(nil)
			if tt.templateVersion != "" {
				template.Annotations = map[string]string{versionKey: tt.templateVersion}
			}
			set := revisionTestRollingSet(1, &groupSetRevision{template: template})
			orphan := revisionTestReadyChild(set, 0, &groupSetRevision{template: template})
			delete(orphan.Annotations, versionKey)
			orphan.OwnerReferences = nil
			if tt.orphanVersion != "" {
				if orphan.Annotations == nil {
					orphan.Annotations = map[string]string{}
				}
				orphan.Annotations[versionKey] = tt.orphanVersion
			}
			r, _ := revisionTestRollingReconciler(t, set, orphan)
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(orphan), orphan))

			require.NoError(t, r.adoptGroupSetChild(ctx, set, orphan))

			adopted := revisionTestGetChild(t, r, set, 0)
			require.True(t, metav1.IsControlledBy(adopted, set), "the orphan must be adopted")
			assert.Equal(t, tt.wantVersion, adopted.Annotations[versionKey])
		})
	}
}

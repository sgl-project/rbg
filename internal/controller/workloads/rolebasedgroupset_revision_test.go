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
	"encoding/json"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
)

// revisionTestTemplate builds the template side the way ensureGroupSetRevision stores it.
func revisionTestTemplate(annotations map[string]string) *workloadsv1alpha2.RoleBasedGroupTemplateSpec {
	return normalizedGroupSetTemplate(&workloadsv1alpha2.RoleBasedGroupTemplateSpec{
		Annotations: annotations,
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
		},
	})
}

// revisionTestChild builds a child carrying the same roles plus whatever annotations the RBG
// controller or the user wrote on it.
func revisionTestChild(annotations map[string]string) *workloadsv1alpha2.RoleBasedGroup {
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-0",
			Annotations: annotations,
			Labels: map[string]string{
				constants.GroupSetNameLabelKey:  "test",
				constants.GroupSetIndexLabelKey: "0",
			},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
		},
	}
}

func TestPruneGroupSetRevisionsAdoptsOrphans(t *testing.T) {
	ctx := context.Background()
	set := revisionTestRollingSet(1, &groupSetRevision{template: revisionTestTemplate(nil)})
	set.Status.CurrentRevision, set.Status.UpdateRevision = "current", "update"
	data, err := canonicalGroupSetTemplateJSON(&set.Spec.GroupTemplate)
	require.NoError(t, err)
	newRevision := func(name string, number int64) *appsv1.ControllerRevision {
		return &appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: set.Namespace, UID: types.UID(name),
				Labels:          map[string]string{constants.GroupSetNameLabelKey: set.Name},
				Annotations:     map[string]string{"kept": "value"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "other", UID: "other"}},
			},
			Data: runtime.RawExtension{Raw: data}, Revision: number,
		}
	}
	objects := []client.Object{set}
	var adopted []*appsv1.ControllerRevision
	for i := 0; i < utils.DefaultRevisionHistoryLimit+3; i++ {
		revision := newRevision(fmt.Sprintf("old-%02d", i), int64(i))
		objects = append(objects, revision)
		if i >= 3 {
			adopted = append(adopted, revision)
		}
	}
	for _, name := range []string{"current", "update", "child-held"} {
		revision := newRevision(name, -1)
		objects = append(objects, revision)
		adopted = append(adopted, revision)
	}
	foreign := newRevision("foreign", -2)
	foreign.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "foreign", UID: "foreign", Controller: ptr.To(true)}}
	predecessor := newRevision("predecessor-owned", -2)
	predecessor.OwnerReferences = []metav1.OwnerReference{{APIVersion: workloadsv1alpha2.GroupVersion.String(), Kind: "RoleBasedGroupSet", Name: set.Name, UID: "previous-uid", Controller: ptr.To(true)}}
	mismatch := newRevision("wrong-label", -2)
	mismatch.Labels[constants.GroupSetNameLabelKey] = "another-set"
	otherNamespace := newRevision("wrong-namespace", -2)
	otherNamespace.Namespace = "other"
	terminating := newRevision("terminating", -2)
	now := metav1.Now()
	terminating.DeletionTimestamp, terminating.Finalizers = &now, []string{"test.io/hold"}
	untouched := []*appsv1.ControllerRevision{foreign, predecessor, mismatch, otherNamespace, terminating}
	for _, revision := range untouched {
		objects = append(objects, revision)
	}
	r, _ := revisionTestRollingReconciler(t, objects...)
	children := &workloadsv1alpha2.RoleBasedGroupList{Items: []workloadsv1alpha2.RoleBasedGroup{{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{constants.GroupSetRevisionLabelKey: "child-held"}},
	}}}
	require.NoError(t, r.pruneGroupSetRevisions(ctx, set, children))
	for i := 0; i < 3; i++ {
		err := r.client.Get(ctx, client.ObjectKey{Namespace: set.Namespace, Name: fmt.Sprintf("old-%02d", i)}, &appsv1.ControllerRevision{})
		assert.True(t, apierrors.IsNotFound(err), "old unused revision %d should be pruned", i)
	}
	for _, original := range adopted {
		got := &appsv1.ControllerRevision{}
		require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(original), got))
		assert.True(t, metav1.IsControlledBy(got, set), got.Name)
		assert.Contains(t, got.OwnerReferences, original.OwnerReferences[0])
		assert.Equal(t, original.UID, got.UID)
		assert.Equal(t, original.Data, got.Data)
		assert.Equal(t, original.Revision, got.Revision)
		assert.Equal(t, original.Labels, got.Labels)
		assert.Equal(t, original.Annotations, got.Annotations)
	}
	for _, original := range untouched {
		got := &appsv1.ControllerRevision{}
		require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(original), got))
		assert.Equal(t, original.OwnerReferences, got.OwnerReferences)
		assert.Equal(t, original.Data, got.Data)
	}
	require.NoError(t, r.pruneGroupSetRevisions(ctx, set, children))
	for _, original := range adopted {
		require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(original), &appsv1.ControllerRevision{}))
	}
}

func TestPruneGroupSetRevisionsRetainsNewestAcrossParents(t *testing.T) {
	ctx := context.Background()
	set := revisionTestRollingSet(0, &groupSetRevision{template: revisionTestTemplate(nil)})
	set.Status.CurrentRevision, set.Status.UpdateRevision = "new-3", "new-3"
	objects := []client.Object{set}
	for i := 0; i < utils.DefaultRevisionHistoryLimit; i++ {
		objects = append(objects, &appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("old-%d", i), Namespace: set.Namespace,
				Labels:            map[string]string{constants.GroupSetNameLabelKey: set.Name},
				CreationTimestamp: metav1.NewTime(time.Unix(int64(i), 0)),
			},
			Revision: int64(96 + i),
		})
	}
	for i := 1; i <= 3; i++ {
		objects = append(objects, &appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("new-%d", i), Namespace: set.Namespace,
				Labels:            map[string]string{constants.GroupSetNameLabelKey: set.Name},
				CreationTimestamp: metav1.NewTime(time.Unix(int64(100+i), 0)),
				OwnerReferences:   []metav1.OwnerReference{*metav1.NewControllerRef(set, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroupSet"))},
			},
			Revision: int64(i),
		})
	}
	r, _ := revisionTestRollingReconciler(t, objects...)
	require.NoError(t, r.pruneGroupSetRevisions(ctx, set, &workloadsv1alpha2.RoleBasedGroupList{}))
	for i := 0; i < 2; i++ {
		err := r.client.Get(ctx, client.ObjectKey{Namespace: set.Namespace, Name: fmt.Sprintf("old-%d", i)}, &appsv1.ControllerRevision{})
		assert.True(t, apierrors.IsNotFound(err))
	}
	for _, original := range objects[3:] {
		got := &appsv1.ControllerRevision{}
		require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(original), got))
		assert.True(t, metav1.IsControlledBy(got, set))
		assert.Equal(t, original.(*appsv1.ControllerRevision).Revision, got.Revision)
	}
}

func TestPruneGroupSetRevisionsChecksLiveParent(t *testing.T) {
	for _, state := range []string{"missing", "replaced", "deleting", "read error", "already owned"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			set := revisionTestRollingSet(0, &groupSetRevision{template: revisionTestTemplate(nil)})
			revision := &appsv1.ControllerRevision{ObjectMeta: metav1.ObjectMeta{
				Name: "old", Namespace: set.Namespace, UID: "old-revision",
				Labels: map[string]string{constants.GroupSetNameLabelKey: set.Name},
			}}
			if state == "already owned" {
				revision.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(set, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroupSet"))}
			}
			r, _ := revisionTestRollingReconciler(t, set, revision)
			objects := []client.Object{revision}
			live := set.DeepCopy()
			switch state {
			case "replaced":
				live.UID = "new-parent"
				objects = append(objects, live)
			case "deleting":
				now := metav1.Now()
				live.DeletionTimestamp, live.Finalizers = &now, []string{"test.io/hold"}
				objects = append(objects, live)
			}
			parentReads := 0
			r.apiReader = fake.NewClientBuilder().WithScheme(r.scheme).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*workloadsv1alpha2.RoleBasedGroupSet); ok {
						parentReads++
						if state == "read error" {
							return fmt.Errorf("live parent read failed")
						}
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}).Build()
			err := r.pruneGroupSetRevisions(ctx, set, &workloadsv1alpha2.RoleBasedGroupList{})
			if state == "already owned" {
				require.NoError(t, err)
				assert.Zero(t, parentReads)
			} else {
				require.Error(t, err)
				assert.Equal(t, 1, parentReads)
			}
			got := &appsv1.ControllerRevision{}
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(revision), got))
			assert.Equal(t, revision.OwnerReferences, got.OwnerReferences)
		})
	}
}

func TestPruneGroupSetRevisionsAdoptionRaces(t *testing.T) {
	for _, state := range []string{"owner changed", "label changed", "deleted"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			set := revisionTestRollingSet(0, &groupSetRevision{template: revisionTestTemplate(nil)})
			revision := &appsv1.ControllerRevision{ObjectMeta: metav1.ObjectMeta{
				Name: "old", Namespace: set.Namespace, UID: "old-revision",
				Labels: map[string]string{constants.GroupSetNameLabelKey: set.Name},
			}}
			r, _ := revisionTestRollingReconciler(t, set, revision)
			r.client = fake.NewClientBuilder().WithScheme(r.scheme).WithObjects(set, revision).WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					live := &appsv1.ControllerRevision{}
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), live))
					switch state {
					case "owner changed":
						live.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "foreign", UID: "foreign", Controller: ptr.To(true)}}
					case "label changed":
						live.Labels[constants.GroupSetNameLabelKey] = "foreign"
					}
					if state == "deleted" {
						require.NoError(t, c.Delete(ctx, live))
					} else {
						require.NoError(t, c.Update(ctx, live))
					}
					return c.Patch(ctx, obj, patch, opts...)
				},
			}).Build()
			r.apiReader = r.client
			err := r.pruneGroupSetRevisions(ctx, set, &workloadsv1alpha2.RoleBasedGroupList{})
			if state == "deleted" {
				require.NoError(t, err)
				return
			}
			require.True(t, apierrors.IsConflict(err), "%v", err)
			got := &appsv1.ControllerRevision{}
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(revision), got))
			assert.False(t, metav1.IsControlledBy(got, set))
			if state == "owner changed" {
				assert.Equal(t, types.UID("foreign"), metav1.GetControllerOf(got).UID)
			} else {
				assert.Equal(t, "foreign", got.Labels[constants.GroupSetNameLabelKey])
			}
			require.NoError(t, r.pruneGroupSetRevisions(ctx, set, &workloadsv1alpha2.RoleBasedGroupList{}))
		})
	}
}

func TestPruneGroupSetRevisionsRejectsChangedDeleteTarget(t *testing.T) {
	ctx := context.Background()
	set := revisionTestRollingSet(0, &groupSetRevision{template: revisionTestTemplate(nil)})
	objects := []client.Object{set}
	for i := 0; i <= utils.DefaultRevisionHistoryLimit; i++ {
		objects = append(objects, &appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("old-%d", i), Namespace: set.Namespace, UID: types.UID(fmt.Sprintf("revision-%d", i)),
				Labels:          map[string]string{constants.GroupSetNameLabelKey: set.Name},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(set, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroupSet"))},
			},
			Revision: int64(i),
		})
	}
	r, _ := revisionTestRollingReconciler(t, objects...)
	r.client = fake.NewClientBuilder().WithScheme(r.scheme).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			live := &appsv1.ControllerRevision{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), live))
			live.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "foreign", UID: "foreign", Controller: ptr.To(true)}}
			require.NoError(t, c.Update(ctx, live))
			return c.Delete(ctx, obj, opts...)
		},
	}).Build()
	r.apiReader = r.client
	err := r.pruneGroupSetRevisions(ctx, set, &workloadsv1alpha2.RoleBasedGroupList{})
	require.True(t, apierrors.IsConflict(err), "%v", err)
	got := &appsv1.ControllerRevision{}
	require.NoError(t, r.client.Get(ctx, client.ObjectKey{Namespace: set.Namespace, Name: "old-0"}, got))
	assert.Equal(t, types.UID("foreign"), metav1.GetControllerOf(got).UID)
}

// TestGroupSetMatchesRevision_IgnoresControllerOwnedAnnotations pins the recreate-loop
// regression: a child carrying discovery-config-mode must still match a revision whose template
// has no annotations, or the group gets recreated every time the RBG controller rewrites it.
func TestGroupSetMatchesRevision_IgnoresControllerOwnedAnnotations(t *testing.T) {
	r := &RoleBasedGroupSetReconciler{}
	revision := &groupSetRevision{name: "rbgs-x", template: revisionTestTemplate(nil)}

	assert.True(
		t,
		r.groupSetMatchesRevision(
			revisionTestChild(map[string]string{constants.DiscoveryConfigModeAnnotationKey: "refine"}), revision,
		),
		"a controller-owned annotation must not read as template drift",
	)
	assert.True(
		t,
		r.groupSetMatchesRevision(revisionTestChild(nil), revision),
	)

	// A user annotation the template lacks is still drift, so the exclusion cannot go blanket.
	assert.False(
		t,
		r.groupSetMatchesRevision(
			revisionTestChild(
				map[string]string{
					constants.DiscoveryConfigModeAnnotationKey: "refine",
					"user.io/note": "x",
				},
			), revision,
		),
		"a user annotation the template lacks must still read as drift",
	)
}

// TestGroupSetOnlyReplicasChanged_IgnoresControllerOwnedAnnotations keeps the scale path on the
// same rule: a replicas-only change must route to the in-place update even when the child carries
// discovery-config-mode.
func TestGroupSetOnlyReplicasChanged_IgnoresControllerOwnedAnnotations(t *testing.T) {
	template := revisionTestTemplate(nil)
	template.Spec.Roles[0].Replicas = ptr.To(int32(3))
	child := revisionTestChild(map[string]string{constants.DiscoveryConfigModeAnnotationKey: "legacy"})
	assert.True(t, groupSetOnlyReplicasChanged(child, template))
}

// TestNormalizedGroupSetTemplate_DropsControllerOwnedKeys checks the revision identity itself:
// system-managed metadata on either side must not change the revision name.
func TestNormalizedGroupSetTemplate_DropsControllerOwnedKeys(t *testing.T) {
	template := &workloadsv1alpha2.RoleBasedGroupTemplateSpec{
		Labels: map[string]string{
			constants.GroupSetNameLabelKey:     "test",
			constants.GroupSetIndexLabelKey:    "0",
			constants.GroupSetRevisionLabelKey: "rbgs-x",
			"tier":                             "backend",
		},
		Annotations: map[string]string{
			constants.DiscoveryConfigModeAnnotationKey: "refine",
			"app.io/env": "prod",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
		},
	}
	result := normalizedGroupSetTemplate(template)

	assert.Equal(t, map[string]string{"tier": "backend"}, result.Labels)
	assert.Equal(t, map[string]string{"app.io/env": "prod"}, result.Annotations)

	bare := normalizedGroupSetTemplate(&workloadsv1alpha2.RoleBasedGroupTemplateSpec{
		Labels:      map[string]string{"tier": "backend"},
		Annotations: map[string]string{"app.io/env": "prod"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
		},
	})
	assert.True(
		t, groupSetTemplatesEqual(result, bare),
		"system-managed metadata must not change the revision identity",
	)
}

func TestGroupSetConversionAnnotationsExcludedFromRevision(t *testing.T) {
	for _, key := range []string{
		constants.ConversionV1alpha1CoordinationAnnotationKey,
		constants.ConversionV1alpha1PodGroupPolicyAnnotationKey,
		constants.ConversionV1alpha2RolloutStrategyAnnotationKey,
		constants.ConversionV1alpha2RolloutStatusAnnotationKey,
	} {
		t.Run(key, func(t *testing.T) {
			ctx := context.Background()
			set := revisionTestRollingSet(1, &groupSetRevision{template: revisionTestTemplate(nil)})
			r, _ := revisionTestRollingReconciler(t, set)
			require.NoError(t, appsv1.AddToScheme(r.scheme))
			base, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
			require.NoError(t, err)
			for _, value := range []string{"first", "changed", ""} {
				template := set.Spec.GroupTemplate.DeepCopy()
				template.Annotations = map[string]string{key: value}
				revision, err := r.ensureGroupSetRevision(ctx, set, template)
				require.NoError(t, err)
				assert.Equal(t, base.name, revision.name)
				assert.Equal(t, value, template.Annotations[key], "normalization must not mutate the source")
				child := revisionTestChild(map[string]string{key: value})
				assert.True(t, r.groupSetMatchesRevision(child, base))
				assert.True(t, groupSetOnlyReplicasChanged(child, base.template))
			}
			revisions := &appsv1.ControllerRevisionList{}
			require.NoError(t, r.client.List(ctx, revisions))
			require.Len(t, revisions.Items, 1)
			assert.NotContains(t, string(revisions.Items[0].Data.Raw), key)
			set.Spec.GroupTemplate.Annotations = map[string]string{"user.io/version": "new"}
			userRevision, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
			require.NoError(t, err)
			assert.NotEqual(t, base.name, userRevision.name, "ordinary metadata must still change revision identity")
		})
	}
}

func TestGroupSetTemplateAnnotationDirection(t *testing.T) {
	for _, key := range []string{
		constants.DiscoveryConfigModeAnnotationKey,
		constants.ConversionV1alpha1CoordinationAnnotationKey,
		constants.ConversionV1alpha1PodGroupPolicyAnnotationKey,
		constants.ConversionV1alpha2RolloutStrategyAnnotationKey,
		constants.ConversionV1alpha2RolloutStatusAnnotationKey,
	} {
		for _, tt := range []struct {
			name     string
			template map[string]string
			child    map[string]string
			want     map[string]string
			changed  bool
		}{
			{name: "child-only stash", child: map[string]string{key: "child"}, want: map[string]string{key: "child"}},
			{name: "missing", template: map[string]string{key: "template"}, want: map[string]string{key: "template"}, changed: true},
			{name: "changed", template: map[string]string{key: "template"}, child: map[string]string{key: "child"}, want: map[string]string{key: "template"}, changed: true},
			{name: "matching", template: map[string]string{key: "same"}, child: map[string]string{key: "same"}, want: map[string]string{key: "same"}},
			{name: "explicit empty", template: map[string]string{key: ""}, want: map[string]string{key: ""}, changed: true},
			{name: "remove user annotation", child: map[string]string{key: "child", "user.io/removed": "old"}, want: map[string]string{key: "child"}, changed: true},
		} {
			t.Run(key+"/"+tt.name, func(t *testing.T) {
				r := &RoleBasedGroupSetReconciler{}
				set := &workloadsv1alpha2.RoleBasedGroupSet{Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{Annotations: tt.template},
				}}
				child := revisionTestChild(tt.child)
				assert.Equal(t, tt.changed, r.needsTemplateAnnotationUpdate(set, child))
				r.syncRBGMetadata(set, child)
				assert.Equal(t, tt.want, child.Annotations)
				assert.False(t, r.needsTemplateAnnotationUpdate(set, child), "sync must converge")
			})
		}
	}
}

func TestNewRBGForSetRevision_SystemAnnotations(t *testing.T) {
	key := constants.ConversionV1alpha1CoordinationAnnotationKey
	revision := &groupSetRevision{name: "old", template: revisionTestTemplate(map[string]string{"version": "old"})}
	set := revisionTestRollingSet(1, revision)
	set.Spec.GroupTemplate.Annotations = map[string]string{"version": "new", key: "latest-coordination"}
	child := newRBGForSetRevision(set, 0, revision)
	assert.Equal(t, map[string]string{"version": "old", key: "latest-coordination"}, child.Annotations)
	assert.Equal(t, revision.template.Spec, child.Spec)
	assert.Equal(t, revision.name, child.Labels[constants.GroupSetRevisionLabelKey])
	child.Annotations[key] = "mutated"
	assert.Equal(t, "latest-coordination", set.Spec.GroupTemplate.Annotations[key])
	assert.Equal(t, map[string]string{"version": "old"}, revision.template.Annotations)
}

func TestSyncRollingGroupSet_SystemAnnotations(t *testing.T) {
	for _, strategy := range []workloadsv1alpha2.GroupUpdateStrategyType{
		workloadsv1alpha2.InPlaceUpdateStrategyType, workloadsv1alpha2.RecreateStrategyType,
	} {
		for _, partition := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/partition=%d", strategy, partition), func(t *testing.T) {
				key := constants.ConversionV1alpha1CoordinationAnnotationKey
				stash := constants.ConversionV1alpha1PodGroupPolicyAnnotationKey
				update := &groupSetRevision{name: "latest", template: revisionTestTemplate(map[string]string{"version": "new"})}
				current := update
				if partition > 0 {
					current = &groupSetRevision{name: "held", template: revisionTestTemplate(map[string]string{"version": "old"})}
				}
				set := revisionTestRollingSet(2, update)
				set.Spec.RolloutStrategy.Type = strategy
				set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt(partition))
				first, second := revisionTestReadyChild(set, 0, current), revisionTestReadyChild(set, 1, update)
				first.Annotations[stash] = "child-only-stash"
				second.Annotations[key] = "stale-coordination"
				r, updates := revisionTestRollingReconciler(t, set, first, second)
				for _, value := range []string{"initial-coordination", "changed-coordination", ""} {
					set.Spec.GroupTemplate.Annotations[key] = value
					changed, children := revisionTestSync(t, r, set, current, update)
					require.True(t, changed)
					require.Len(t, children.Items, 2)
					for index, original := range []*workloadsv1alpha2.RoleBasedGroup{first, second} {
						child := revisionTestGetChild(t, r, set, index)
						assert.Equal(t, value, child.Annotations[key])
						assert.Contains(t, child.Annotations, key)
						assert.Equal(t, original.UID, child.UID)
						assert.Equal(t, original.Spec, child.Spec)
						assert.Equal(t, original.Labels, child.Labels)
						assert.Equal(t, original.Generation, child.Generation)
						assert.Equal(t, original.Status, child.Status)
						assert.Equal(t, original.Annotations["version"], child.Annotations["version"], "held user metadata must not advance")
						assert.Equal(t, original.Annotations[stash], child.Annotations[stash])
					}
					beforeUpdates := len(*updates)
					changed, _ = revisionTestSync(t, r, set, current, update)
					assert.False(t, changed, "annotation-only propagation must converge")
					assert.Len(t, *updates, beforeUpdates)
				}
				delete(set.Spec.GroupTemplate.Annotations, key)
				changed, _ := revisionTestSync(t, r, set, current, update)
				assert.False(t, changed, "template absence must not erase child-side compatibility data")
			})
		}
	}
}

func TestSyncRollingGroupSet_SystemAnnotationsReachRetainedSurge(t *testing.T) {
	key := constants.ConversionV1alpha1CoordinationAnnotationKey
	revision := &groupSetRevision{name: "current", template: revisionTestTemplate(nil)}
	set := revisionTestRollingSet(2, revision)
	first, second := revisionTestReadyChild(set, 0, revision), revisionTestReadyChild(set, 1, revision)
	first.Status.Conditions[0].Status = metav1.ConditionFalse
	second.Status.Conditions[0].Status = metav1.ConditionFalse
	surge := revisionTestReadyChild(set, 2, revision)
	terminating := revisionTestReadyChild(set, 3, revision)
	terminating.DeletionTimestamp = ptr.To(metav1.Now().Rfc3339Copy())
	terminating.Finalizers = []string{"test.io/hold-deletion"}
	set.Spec.GroupTemplate.Annotations = map[string]string{key: "latest-coordination"}
	r, _ := revisionTestRollingReconciler(t, set, first, second, surge, terminating)
	changed, children := revisionTestSync(t, r, set, revision, revision)
	require.True(t, changed)
	require.Len(t, children.Items, 4)
	for index := 0; index < 3; index++ {
		child := revisionTestGetChild(t, r, set, index)
		assert.Equal(t, "latest-coordination", child.Annotations[key])
		assert.Equal(t, types.UID("original-"+child.Name), child.UID)
	}
	assert.NotContains(t, revisionTestGetChild(t, r, set, 3).Annotations, key)
	changed, _ = revisionTestSync(t, r, set, revision, revision)
	assert.False(t, changed)
}

func TestSyncRollingGroupSet_InPlacePreservesConversionStash(t *testing.T) {
	current := &groupSetRevision{name: "old", template: revisionTestTemplate(map[string]string{"version": "old"})}
	update := &groupSetRevision{name: "new", template: revisionTestTemplate(map[string]string{"version": "new"})}
	set := revisionTestRollingSet(1, update)
	original := revisionTestReadyChild(set, 0, current)
	stash := map[string]string{
		constants.ConversionV1alpha1CoordinationAnnotationKey:    "coordination",
		constants.ConversionV1alpha1PodGroupPolicyAnnotationKey:  "pod-group-policy",
		constants.ConversionV1alpha2RolloutStrategyAnnotationKey: "strategy",
		constants.ConversionV1alpha2RolloutStatusAnnotationKey:   "status",
	}
	for key, value := range stash {
		original.Annotations[key] = value
	}
	r, _ := revisionTestRollingReconciler(t, set, original)
	changed, _ := revisionTestSync(t, r, set, current, update)
	require.True(t, changed)
	child := revisionTestGetChild(t, r, set, 0)
	assert.Equal(t, original.UID, child.UID)
	assert.Equal(t, "new", child.Annotations["version"])
	for key, value := range stash {
		assert.Equal(t, value, child.Annotations[key])
	}
	changed, _ = revisionTestSync(t, r, set, current, update)
	assert.False(t, changed, "child-only stashes must not cause another rollout")
}

func TestWarmUpGroupSetSurge_BoundedByRemainingWork(t *testing.T) {
	tests := []struct {
		name            string
		maxSurge        int
		maxUnavailable  int
		servingOutdated int
		readyBase       int
		existingSurge   []int
		wantCreated     int
	}{
		{name: "surge exceeds replicas", maxSurge: 5, servingOutdated: 4, readyBase: 4, wantCreated: 4},
		{name: "surge remains the upper bound", maxSurge: 2, servingOutdated: 4, readyBase: 4, wantCreated: 2},
		{name: "only partition batch needs surge", maxSurge: 5, servingOutdated: 2, readyBase: 4, wantCreated: 2},
		{name: "unavailable budget covers part of batch", maxSurge: 5, maxUnavailable: 1, servingOutdated: 4, readyBase: 4, wantCreated: 3},
		{name: "unavailable budget covers whole batch", maxSurge: 5, maxUnavailable: 2, servingOutdated: 2, readyBase: 4},
		{name: "no remaining work", maxSurge: 5, readyBase: 4},
		{name: "surge disabled", servingOutdated: 4, readyBase: 4},
		{name: "existing surge counts toward target", maxSurge: 5, servingOutdated: 4, readyBase: 4, existingSurge: []int{4, 5}, wantCreated: 2},
		{name: "higher ordinal surge already covers remaining work", maxSurge: 5, servingOutdated: 1, readyBase: 4, existingSurge: []int{7}},
		{name: "surge with gaps only fills shortfall", maxSurge: 5, servingOutdated: 3, readyBase: 4, existingSurge: []int{6, 8}, wantCreated: 1},
		{name: "existing surge exceeds remaining work", maxSurge: 5, servingOutdated: 1, readyBase: 4, existingSurge: []int{4, 5}},
		// A group that is down has already spent part of the unavailable budget, and a group held
		// back below the partition is never touched by the rollout. Surge must still be created from
		// the remaining headroom, otherwise the rollout waits for that group to recover by itself.
		{name: "failure below partition consumes the unavailable budget", maxSurge: 1, maxUnavailable: 1, servingOutdated: 1, readyBase: 3, wantCreated: 1},
		{name: "failure leaves partial headroom", maxSurge: 2, maxUnavailable: 2, servingOutdated: 3, readyBase: 3, wantCreated: 2},
		{name: "failure covers the whole batch", maxSurge: 2, maxUnavailable: 2, servingOutdated: 1, readyBase: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
			set := &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-set"},
				Spec:       workloadsv1alpha2.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(4))},
			}
			update := &groupSetRevision{name: "update", template: revisionTestTemplate(nil)}
			existing := make(map[int]*workloadsv1alpha2.RoleBasedGroup)
			objects := []client.Object{set}
			for _, index := range tt.existingSurge {
				child := newRBGForSetRevision(set, index, update)
				existing[index] = child
				objects = append(objects, child)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			r := &RoleBasedGroupSetReconciler{client: c, apiReader: c, scheme: scheme}
			limits := groupSetRolloutLimits{maxSurge: tt.maxSurge, maxUnavailable: tt.maxUnavailable}
			accounting := groupSetSurgeAccounting{servingOutdated: tt.servingOutdated, readyBase: tt.readyBase}
			changed, err := r.warmUpGroupSetSurge(ctx, set, existing, 4, limits, update,
				accounting, tt.maxSurge-len(tt.existingSurge))
			require.NoError(t, err)
			assert.Equal(t, tt.wantCreated > 0, changed)
			children := &workloadsv1alpha2.RoleBasedGroupList{}
			require.NoError(t, c.List(ctx, children, client.InNamespace(set.Namespace)))
			assert.Len(t, children.Items, len(tt.existingSurge)+tt.wantCreated)
		})
	}
}

// A failure in a group held back below the partition spends the declared unavailable budget. Surge
// exists precisely so the rollout does not wait for that group to recover on its own, so surge must
// still be warmed up; and because the surge is not ready yet, the in-scope group is only replaced
// after it is, keeping ready capacity at the floor the whole time.
func TestSyncRollingGroupSet_HeldBackFailureStillWarmsSurge(t *testing.T) {
	ctx := context.Background()
	current := &groupSetRevision{name: "v1", template: revisionTestTemplate(nil)}
	update := &groupSetRevision{name: "v2", template: revisionTestTemplate(map[string]string{"user.io/version": "v2"})}
	set := revisionTestRollingSet(4, update)
	set.Spec.RolloutStrategy.Type = workloadsv1alpha2.RecreateStrategyType
	set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt32(3))
	set.Spec.RolloutStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	set.Spec.RolloutStrategy.MaxSurge = ptr.To(intstr.FromInt32(1))

	objects := []client.Object{set}
	for index := 0; index < 4; index++ {
		child := revisionTestReadyChild(set, index, current)
		if index == 0 {
			child.Status.Conditions[0].Status = metav1.ConditionFalse
		}
		objects = append(objects, child)
	}
	r, updates := revisionTestRollingReconciler(t, objects...)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))

	// maxUnavailable=1 is already consumed by the held-back ordinal 0, so no base group can be
	// taken down; the surge slot has to come from maxSurge instead.
	changed, _ := revisionTestSync(t, r, set, current, update)
	require.True(t, changed, "the spent unavailable budget must not prevent surge creation")
	surge := revisionTestGetChild(t, r, set, 4)
	assert.Equal(t, "v2", surge.Labels[constants.GroupSetRevisionLabelKey], "surge is built at the update revision")
	assert.Equal(t, "v1", revisionTestGetChild(t, r, set, 3).Labels[constants.GroupSetRevisionLabelKey])
	assert.Empty(t, *updates, "the in-scope group waits for the surge to become ready")

	// Repeated passes create no second surge and still take no base group down.
	for pass := 0; pass < 4; pass++ {
		changed, _ = revisionTestSync(t, r, set, current, update)
		assert.False(t, changed, "pass %d has no work left", pass)
		assert.Empty(t, *updates)
	}

	revisionTestMarkReady(t, r, surge)
	for pass := 0; pass < 6; pass++ {
		revisionTestSync(t, r, set, current, update)
		// Recreate replaces ordinal 3 once the surge covers it; the rebuilt group carries v2.
		rolled := &workloadsv1alpha2.RoleBasedGroup{}
		if err := r.client.Get(ctx, client.ObjectKey{Namespace: set.Namespace, Name: "test-3"}, rolled); err == nil &&
			rolled.Labels[constants.GroupSetRevisionLabelKey] == "v2" {
			return
		}
	}
	t.Error("ordinal 3 never rolled after the surge became ready")
}

func TestSyncRollingGroupSet_OversizedSurgeConverges(t *testing.T) {
	for _, partition := range []int{0, 2} {
		t.Run(fmt.Sprintf("partition=%d", partition), func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
			set := &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test", Namespace: "default", UID: "test-set",
					Annotations: map[string]string{groupSetReplicasAnnotation: "4"},
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(4))},
			}
			current := &groupSetRevision{name: "current", template: revisionTestTemplate(map[string]string{"version": "v1"})}
			update := &groupSetRevision{name: "update", template: revisionTestTemplate(map[string]string{"version": "v2"})}
			readyStatus := workloadsv1alpha2.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{{Type: string(workloadsv1alpha2.RoleBasedGroupReady), Status: metav1.ConditionTrue}},
			}
			objects := []client.Object{set}
			for index := 0; index < 4; index++ {
				child := newRBGForSetRevision(set, index, current)
				child.Status = readyStatus
				objects = append(objects, child)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
				WithStatusSubresource(&workloadsv1alpha2.RoleBasedGroup{}).Build()
			r := &RoleBasedGroupSetReconciler{client: c, apiReader: c, scheme: scheme}
			limits := groupSetRolloutLimits{partition: partition, maxSurge: 5}
			peakSurge := 0
			children := &workloadsv1alpha2.RoleBasedGroupList{}
			for step := 0; step < 12; step++ {
				require.NoError(t, c.List(ctx, children, client.InNamespace(set.Namespace)))
				_, err := r.syncRollingGroupSet(ctx, set, children, current, update, limits)
				require.NoError(t, err)
				require.NoError(t, c.List(ctx, children, client.InNamespace(set.Namespace)))
				ready, surge := 0, 0
				for i := range children.Items {
					child := &children.Items[i]
					if groupSetChildReady(child) {
						ready++
					}
					index, valid := groupSetOrdinal(set, child)
					require.True(t, valid)
					if index >= 4 {
						surge++
					}
					if index < partition {
						require.Equal(t, current.name, child.Labels[constants.GroupSetRevisionLabelKey])
					}
					child.Status = readyStatus
					child.Status.ObservedGeneration = child.Generation
					require.NoError(t, c.Status().Update(ctx, child))
				}
				peakSurge = max(peakSurge, surge)
				require.LessOrEqual(t, surge, 4-partition, "step %d created unnecessary surge", step)
				require.GreaterOrEqual(t, ready, 4, "step %d violated maxUnavailable=0", step)
			}
			assert.Equal(t, 4-partition, peakSurge)
			assert.Len(t, children.Items, 4)
			assert.True(t, r.groupSetPartitionReady(set, children, update, partition))
		})
	}
}

func TestResolveGroupSetRollout(t *testing.T) {
	for _, strategy := range []workloadsv1alpha2.GroupUpdateStrategyType{
		"", workloadsv1alpha2.RecreateStrategyType, workloadsv1alpha2.InPlaceUpdateStrategyType,
	} {
		for _, tt := range []struct {
			name                          string
			partition, surge, unavailable *intstr.IntOrString
			wantRecreate, wantInPlace     groupSetRolloutLimits
			recreateError                 bool
		}{
			{
				name: "defaults", wantRecreate: groupSetRolloutLimits{maxUnavailable: 1},
				wantInPlace: groupSetRolloutLimits{maxUnavailable: 1},
			},
			{
				name: "percentage rounding", partition: ptr.To(intstr.FromString("39%")),
				surge: ptr.To(intstr.FromString("21%")), unavailable: ptr.To(intstr.FromString("39%")),
				wantRecreate: groupSetRolloutLimits{partition: 1, maxSurge: 2, maxUnavailable: 1},
				wantInPlace:  groupSetRolloutLimits{partition: 1, maxUnavailable: 1},
			},
			{
				name: "clamp to replicas except recreate surge", partition: ptr.To(intstr.FromInt(9)),
				surge: ptr.To(intstr.FromInt(9)), unavailable: ptr.To(intstr.FromInt(9)),
				wantRecreate: groupSetRolloutLimits{partition: 5, maxSurge: 9, maxUnavailable: 5},
				wantInPlace:  groupSetRolloutLimits{partition: 5, maxUnavailable: 5},
			},
			{
				name: "zero budgets make progress", surge: ptr.To(intstr.FromInt(0)), unavailable: ptr.To(intstr.FromInt(0)),
				wantRecreate: groupSetRolloutLimits{maxUnavailable: 1},
				wantInPlace:  groupSetRolloutLimits{maxUnavailable: 1},
			},
			{
				name: "zero percentage budgets make progress", surge: ptr.To(intstr.FromString("0%")), unavailable: ptr.To(intstr.FromString("0%")),
				wantRecreate: groupSetRolloutLimits{maxUnavailable: 1},
				wantInPlace:  groupSetRolloutLimits{maxUnavailable: 1},
			},
			{
				name: "only recreate surge permits zero unavailable", surge: ptr.To(intstr.FromInt(1)), unavailable: ptr.To(intstr.FromInt(0)),
				wantRecreate: groupSetRolloutLimits{maxSurge: 1},
				wantInPlace:  groupSetRolloutLimits{maxUnavailable: 1},
			},
			{
				name: "positive unavailable percentage rounds down to zero", surge: ptr.To(intstr.FromInt(1)), unavailable: ptr.To(intstr.FromString("19%")),
				wantRecreate: groupSetRolloutLimits{maxSurge: 1},
				wantInPlace:  groupSetRolloutLimits{maxUnavailable: 1},
			},
			{
				name: "stored oversized surge", surge: ptr.To(intstr.FromInt(99)), unavailable: ptr.To(intstr.FromInt(0)),
				wantRecreate: groupSetRolloutLimits{maxSurge: 99},
				wantInPlace:  groupSetRolloutLimits{maxUnavailable: 1},
			},
			{
				name: "stored percentage surge with zero percentage unavailable", surge: ptr.To(intstr.FromString("99%")), unavailable: ptr.To(intstr.FromString("0%")),
				wantRecreate: groupSetRolloutLimits{maxSurge: 5},
				wantInPlace:  groupSetRolloutLimits{maxUnavailable: 1},
			},
			{
				name: "stored malformed surge", surge: ptr.To(intstr.FromString("invalid")),
				recreateError: true, wantInPlace: groupSetRolloutLimits{maxUnavailable: 1},
			},
			{
				name: "stored negative surge", surge: ptr.To(intstr.FromInt(-1)),
				recreateError: true, wantInPlace: groupSetRolloutLimits{maxUnavailable: 1},
			},
			{
				name: "stored negative percentage surge", surge: ptr.To(intstr.FromString("-99%")),
				recreateError: true, wantInPlace: groupSetRolloutLimits{maxUnavailable: 1},
			},
			{
				name: "stored invalid surge type", surge: &intstr.IntOrString{Type: intstr.Type(99)},
				recreateError: true, wantInPlace: groupSetRolloutLimits{maxUnavailable: 1},
			},
		} {
			strategyName := string(strategy)
			if strategyName == "" {
				strategyName = "default"
			}
			t.Run(strategyName+"/"+tt.name, func(t *testing.T) {
				set := &workloadsv1alpha2.RoleBasedGroupSet{Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(5)),
					RolloutStrategy: &workloadsv1alpha2.GroupSetRolloutStrategy{
						Type: strategy, Partition: tt.partition, MaxSurge: tt.surge, MaxUnavailable: tt.unavailable,
					},
				}}
				limits, err := resolveGroupSetRollout(set)
				if strategy == workloadsv1alpha2.RecreateStrategyType && tt.recreateError {
					require.Error(t, err, "Recreate must still parse and validate maxSurge")
					return
				}
				require.NoError(t, err)
				want := tt.wantRecreate
				if strategy != workloadsv1alpha2.RecreateStrategyType {
					want = tt.wantInPlace
					want.inPlaceUpdate = true
				}
				assert.Equal(t, want, limits)
			})
		}
	}
	t.Run("unknown strategy", func(t *testing.T) {
		set := &workloadsv1alpha2.RoleBasedGroupSet{Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			Replicas:        ptr.To(int32(5)),
			RolloutStrategy: &workloadsv1alpha2.GroupSetRolloutStrategy{Type: workloadsv1alpha2.GroupUpdateStrategyType("Unknown")},
		}}
		_, err := resolveGroupSetRollout(set)
		require.ErrorContains(t, err, "unsupported RoleBasedGroupSet rollout strategy")
		assert.ErrorContains(t, err, "Unknown")
	})
}

func revisionTestRollingSet(replicas int32, update *groupSetRevision) *workloadsv1alpha2.RoleBasedGroupSet {
	return &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test", Namespace: "default", UID: "test-set",
			Annotations: map[string]string{groupSetReplicasAnnotation: fmt.Sprint(replicas)},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			Replicas: ptr.To(replicas), GroupTemplate: *update.template.DeepCopy(),
			RolloutStrategy: &workloadsv1alpha2.GroupSetRolloutStrategy{Type: workloadsv1alpha2.InPlaceUpdateStrategyType},
		},
	}
}

func revisionTestReadyChild(set *workloadsv1alpha2.RoleBasedGroupSet, index int, revision *groupSetRevision) *workloadsv1alpha2.RoleBasedGroup {
	child := newRBGForSetRevision(set, index, revision)
	child.UID = types.UID("original-" + child.Name)
	child.Generation = 1
	child.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(set, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroupSet"))}
	child.Status = workloadsv1alpha2.RoleBasedGroupStatus{
		ObservedGeneration: 1,
		Conditions: []metav1.Condition{{
			Type: string(workloadsv1alpha2.RoleBasedGroupReady), Status: metav1.ConditionTrue, ObservedGeneration: 1,
		}},
	}
	return child
}

// fake preserves status but does not implement the apiserver's generation semantics.
// Record successful child Updates as well, so ordering assertions do not depend on List order.
func revisionTestRollingReconciler(t *testing.T, objects ...client.Object) (*RoleBasedGroupSetReconciler, *[]string) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	var updates []string
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
		WithStatusSubresource(&workloadsv1alpha2.RoleBasedGroup{}, &workloadsv1alpha2.RoleBasedGroupSet{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if child, ok := obj.(*workloadsv1alpha2.RoleBasedGroup); ok {
					child.Generation = 1
					child.UID = types.UID("created-" + child.Name)
				}
				return c.Create(ctx, obj, opts...)
			},
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				child, ok := obj.(*workloadsv1alpha2.RoleBasedGroup)
				if !ok {
					return c.Update(ctx, obj, opts...)
				}
				stored := &workloadsv1alpha2.RoleBasedGroup{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(child), stored); err != nil {
					return err
				}
				child.Generation = stored.Generation
				if !apiequality.Semantic.DeepEqual(stored.Spec, child.Spec) {
					child.Generation++
				}
				if err := c.Update(ctx, child, opts...); err != nil {
					return err
				}
				updates = append(updates, child.Name)
				return nil
			},
		}).Build()
	return &RoleBasedGroupSetReconciler{client: c, apiReader: c, scheme: scheme}, &updates
}

func revisionTestSync(t *testing.T, r *RoleBasedGroupSetReconciler, set *workloadsv1alpha2.RoleBasedGroupSet,
	current, update *groupSetRevision,
) (bool, *workloadsv1alpha2.RoleBasedGroupList) {
	t.Helper()
	ctx := context.Background()
	limits, err := resolveGroupSetRollout(set)
	require.NoError(t, err)
	children, err := r.listRollingGroupSetChildren(ctx, set)
	require.NoError(t, err)
	changed, err := r.syncRollingGroupSet(ctx, set, children, current, update, limits)
	require.NoError(t, err)
	children, err = r.listRollingGroupSetChildren(ctx, set)
	require.NoError(t, err)
	return changed, children
}

func revisionTestGetChild(t *testing.T, r *RoleBasedGroupSetReconciler, set *workloadsv1alpha2.RoleBasedGroupSet, index int) *workloadsv1alpha2.RoleBasedGroup {
	t.Helper()
	child := &workloadsv1alpha2.RoleBasedGroup{}
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKey{Namespace: set.Namespace, Name: fmt.Sprintf("%s-%d", set.Name, index)}, child))
	return child
}

// revisionTestMarkReady stands in for the RoleBasedGroup controller finishing a rollout, which
// includes acknowledging the set's last rollout request.
func revisionTestMarkReady(t *testing.T, r *RoleBasedGroupSetReconciler, child *workloadsv1alpha2.RoleBasedGroup) {
	t.Helper()
	child.Status.ObservedGeneration = child.Generation
	child.Status.ObservedRolloutRequest = child.Annotations[constants.RolloutRequestAnnotationKey]
	child.Status.Conditions = []metav1.Condition{{
		Type: string(workloadsv1alpha2.RoleBasedGroupReady), Status: metav1.ConditionTrue, ObservedGeneration: child.Generation,
	}}
	require.NoError(t, r.client.Status().Update(context.Background(), child))
}

func TestSyncRollingGroupSet_InPlaceUpdateBudgetAndOrder(t *testing.T) {
	for _, mode := range []struct {
		strategy     workloadsv1alpha2.GroupUpdateStrategyType
		replicasOnly bool
	}{
		{strategy: workloadsv1alpha2.InPlaceUpdateStrategyType},
		{strategy: workloadsv1alpha2.InPlaceUpdateStrategyType, replicasOnly: true},
		{strategy: workloadsv1alpha2.RecreateStrategyType, replicasOnly: true},
	} {
		replicasOnly := mode.replicasOnly
		for _, tt := range []struct {
			name                                  string
			unavailable, partition                int
			terminating, unready, staleGeneration bool
			wantUpdates                           []string
		}{
			{name: "one at a time", unavailable: 1, wantUpdates: []string{"test-3"}},
			{name: "descending batch", unavailable: 2, wantUpdates: []string{"test-3", "test-2"}},
			{name: "partition held back", unavailable: 4, partition: 2, wantUpdates: []string{"test-3", "test-2"}},
			{name: "fully partitioned", unavailable: 4, partition: 4},
			{name: "terminating skipped and consumes budget", unavailable: 2, terminating: true, wantUpdates: []string{"test-2"}},
			{name: "unready repaired with no remaining budget", unavailable: 1, unready: true, wantUpdates: []string{"test-1"}},
			{name: "stale generation repaired with no remaining budget", unavailable: 1, staleGeneration: true, wantUpdates: []string{"test-1"}},
			{name: "unready below partition held back", unavailable: 1, partition: 2, unready: true},
		} {
			t.Run(fmt.Sprintf("%s/replicasOnly=%t/%s", mode.strategy, replicasOnly, tt.name), func(t *testing.T) {
				current := &groupSetRevision{name: "v1", template: revisionTestTemplate(nil)}
				update := &groupSetRevision{name: "v2", template: revisionTestTemplate(nil)}
				update.template.Spec.Roles[0].Replicas = ptr.To(int32(2))
				if !replicasOnly {
					update.template.Spec.Roles[0].StandalonePattern = &workloadsv1alpha2.StandalonePattern{
						TemplateSource: workloadsv1alpha2.TemplateSource{Template: &corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "worker:v2"}}},
						}},
					}
				}
				set := revisionTestRollingSet(4, update)
				set.Spec.RolloutStrategy.Type = mode.strategy
				set.Spec.RolloutStrategy.MaxUnavailable = ptr.To(intstr.FromInt(tt.unavailable))
				set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt(tt.partition))
				objects := []client.Object{set}
				original := make(map[string]*workloadsv1alpha2.RoleBasedGroup)
				for index := 0; index < 4; index++ {
					child := revisionTestReadyChild(set, index, current)
					if tt.terminating && index == 3 {
						child.DeletionTimestamp = ptr.To(metav1.Now().Rfc3339Copy())
						child.Finalizers = []string{"test.io/hold-deletion"}
					}
					if tt.unready && index == 1 {
						child.Status.Conditions[0].Status = metav1.ConditionFalse
					}
					if tt.staleGeneration && index == 1 {
						child.Generation = 2
					}
					require.Equal(t, replicasOnly, groupSetOnlyReplicasChanged(child, update.template))
					original[child.Name] = child.DeepCopy()
					objects = append(objects, child)
				}
				r, updates := revisionTestRollingReconciler(t, objects...)
				changed, children := revisionTestSync(t, r, set, current, update)
				assert.Equal(t, len(tt.wantUpdates) > 0, changed)
				assert.Equal(t, tt.wantUpdates, *updates)
				require.Len(t, children.Items, 4, "in-place rollout must not delete or create base groups")
				for i := range children.Items {
					child := &children.Items[i]
					before := original[child.Name]
					require.NotNil(t, before)
					assert.Equal(t, before.UID, child.UID)
					assert.Equal(t, before.OwnerReferences, child.OwnerReferences)
					assert.Equal(t, before.Finalizers, child.Finalizers)
					assert.True(t, before.DeletionTimestamp.Equal(child.DeletionTimestamp),
						"in-place writes must not change the deletion timestamp")
					assert.Equal(t, before.Status, child.Status, "spec writes must leave readiness stale")
					shouldUpdate := false
					for _, name := range tt.wantUpdates {
						shouldUpdate = shouldUpdate || child.Name == name
					}
					if shouldUpdate {
						assert.Equal(t, update.template.Spec, child.Spec)
						assert.Equal(t, update.name, child.Labels[constants.GroupSetRevisionLabelKey])
						assert.Equal(t, before.Generation+1, child.Generation)
						assert.False(t, groupSetChildReady(child))
					} else {
						assert.Equal(t, before.Spec, child.Spec)
						assert.Equal(t, before.Labels, child.Labels)
						assert.Equal(t, before.Generation, child.Generation)
					}
				}
				changed, _ = revisionTestSync(t, r, set, current, update)
				assert.False(t, changed, "a second reconcile cannot spend the same readiness budget")
				assert.Equal(t, tt.wantUpdates, *updates)
			})
		}
	}
}

func TestRolloutRequestCannotBeOverriddenByTemplate(t *testing.T) {
	revision := &groupSetRevision{name: "v1", template: revisionTestTemplate(nil)}
	set := revisionTestRollingSet(1, revision)
	set.Spec.GroupTemplate.Annotations = map[string]string{constants.RolloutRequestAnnotationKey: "0"}
	child := revisionTestReadyChild(set, 0, revision)
	child.Annotations = map[string]string{constants.RolloutRequestAnnotationKey: "2"}
	child.Status.ObservedRolloutRequest = "2"
	r := &RoleBasedGroupSetReconciler{}
	assert.False(t, r.needsTemplateAnnotationUpdate(set, child))
	r.syncRBGMetadata(set, child)
	assert.Equal(t, "2", child.Annotations[constants.RolloutRequestAnnotationKey])
	requestGroupSetChildRollout(child)
	assert.Equal(t, "3", child.Annotations[constants.RolloutRequestAnnotationKey])
	assert.False(t, groupSetChildReady(child))
	assert.NotContains(t, newRBGForSet(set, 0).Annotations, constants.RolloutRequestAnnotationKey)
	assert.NotContains(t, normalizedGroupSetTemplate(set.Spec.GroupTemplate.DeepCopy()).Annotations, constants.RolloutRequestAnnotationKey)
}

func TestGroupSetChildReady_RolloutRequest(t *testing.T) {
	for _, tt := range []struct {
		name             string
		request, ack     string
		hasRequest, want bool
	}{
		{name: "never requested child bootstraps as ready", want: true},
		{name: "unacknowledged first request", hasRequest: true, request: "1"},
		{name: "older acknowledgement", hasRequest: true, request: "2", ack: "1"},
		{name: "acknowledged request", hasRequest: true, request: "2", ack: "2", want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			revision := &groupSetRevision{name: "v1", template: revisionTestTemplate(nil)}
			child := revisionTestReadyChild(revisionTestRollingSet(1, revision), 0, revision)
			if tt.hasRequest {
				child.Annotations = map[string]string{constants.RolloutRequestAnnotationKey: tt.request}
			}
			child.Status.ObservedRolloutRequest = tt.ack
			assert.Equal(t, tt.want, groupSetChildReady(child))
		})
	}
}

// A metadata-only write keeps the child's generation, so its old Ready status stays current. The
// slot it spends must stay spent until the child acknowledges the write, not just for one pass.
func TestSyncRollingGroupSet_MetadataOnlyUpdateHoldsBudgetUntilAcknowledged(t *testing.T) {
	current := &groupSetRevision{name: "v1", template: revisionTestTemplate(nil)}
	update := &groupSetRevision{name: "v2", template: revisionTestTemplate(map[string]string{
		constants.GroupExclusiveTopologyKey: "kubernetes.io/hostname",
	})}
	set := revisionTestRollingSet(3, update)
	r, updates := revisionTestRollingReconciler(t, set,
		revisionTestReadyChild(set, 0, current), revisionTestReadyChild(set, 1, current), revisionTestReadyChild(set, 2, current))
	var wantUpdates []string
	for index := 2; index >= 0; index-- {
		changed, _ := revisionTestSync(t, r, set, current, update)
		require.True(t, changed)
		child := revisionTestGetChild(t, r, set, index)
		wantUpdates = append(wantUpdates, child.Name)
		require.Equal(t, wantUpdates, *updates)
		assert.Equal(t, "kubernetes.io/hostname", child.Annotations[constants.GroupExclusiveTopologyKey])
		assert.Equal(t, int64(1), child.Generation)
		assert.Equal(t, child.Generation, child.Status.ObservedGeneration)
		assert.True(t, meta.IsStatusConditionTrue(child.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupReady)))
		assert.False(t, groupSetChildReady(child))

		for attempt := 0; attempt < 2; attempt++ {
			changed, _ = revisionTestSync(t, r, set, current, update)
			assert.False(t, changed, "an unacknowledged metadata-only write must keep holding its slot")
			assert.Equal(t, wantUpdates, *updates)
		}
		// The child controller has seen the write and its workloads are still rolling.
		child.Status.Conditions = append(child.Status.Conditions, metav1.Condition{
			Type: string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress), Status: metav1.ConditionTrue,
			ObservedGeneration: child.Generation, Reason: "RolesUpdating", LastTransitionTime: metav1.Now(),
		})
		require.NoError(t, r.client.Status().Update(context.Background(), child))
		changed, _ = revisionTestSync(t, r, set, current, update)
		assert.False(t, changed)
		assert.Equal(t, wantUpdates, *updates)

		revisionTestMarkReady(t, r, child)
	}
	changed, children := revisionTestSync(t, r, set, current, update)
	assert.False(t, changed)
	assert.True(t, r.groupSetPartitionReady(set, children, update, 0))
	assert.Equal(t, []string{"test-2", "test-1", "test-0"}, *updates)
}

func TestSyncRollingGroupSet_ReplicaUpdatesWaitForCompletion(t *testing.T) {
	for _, strategy := range []workloadsv1alpha2.GroupUpdateStrategyType{
		workloadsv1alpha2.InPlaceUpdateStrategyType, workloadsv1alpha2.RecreateStrategyType,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			current := &groupSetRevision{name: "v1", template: revisionTestTemplate(nil)}
			update := &groupSetRevision{name: "v2", template: revisionTestTemplate(nil)}
			update.template.Spec.Roles[0].Replicas = ptr.To(int32(2))
			set := revisionTestRollingSet(2, update)
			set.Spec.RolloutStrategy.Type = strategy
			r, updates := revisionTestRollingReconciler(t, set,
				revisionTestReadyChild(set, 0, current), revisionTestReadyChild(set, 1, current))
			var wantUpdates []string
			for index := 1; index >= 0; index-- {
				changed, children := revisionTestSync(t, r, set, current, update)
				require.True(t, changed)
				require.Len(t, children.Items, 2)
				child := revisionTestGetChild(t, r, set, index)
				wantUpdates = append(wantUpdates, child.Name)
				assert.Equal(t, wantUpdates, *updates)
				assert.Equal(t, types.UID("original-"+child.Name), child.UID)
				assert.Equal(t, int64(2), child.Generation)
				assert.Equal(t, int64(1), child.Status.ObservedGeneration)
				assert.Equal(t, metav1.ConditionTrue, child.Status.Conditions[0].Status)
				for attempt := 0; attempt < 2; attempt++ {
					changed, _ = revisionTestSync(t, r, set, current, update)
					assert.False(t, changed, "old observedGeneration plus Ready must not release the next ordinal")
					assert.Equal(t, wantUpdates, *updates)
				}
				child.Status.ObservedGeneration = child.Generation
				child.Status.ObservedRolloutRequest = child.Annotations[constants.RolloutRequestAnnotationKey]
				for _, rolling := range []metav1.Condition{
					{Status: metav1.ConditionFalse, ObservedGeneration: child.Generation - 1},
					{Status: metav1.ConditionTrue, ObservedGeneration: child.Generation},
					{Status: metav1.ConditionUnknown, ObservedGeneration: child.Generation},
				} {
					rolling.Type = string(workloadsv1alpha2.RoleBasedGroupRollingUpdateInProgress)
					child.Status.Conditions = append(child.Status.Conditions[:1], rolling)
					require.NoError(t, r.client.Status().Update(context.Background(), child))
					changed, _ = revisionTestSync(t, r, set, current, update)
					assert.False(t, changed)
					assert.Equal(t, wantUpdates, *updates)
				}
				child.Status.Conditions[1].Status = metav1.ConditionFalse
				child.Status.Conditions[0].Status = metav1.ConditionFalse
				require.NoError(t, r.client.Status().Update(context.Background(), child))
				changed, _ = revisionTestSync(t, r, set, current, update)
				assert.False(t, changed, "the desired capacity must be Ready even after the workload is updated")
				assert.Equal(t, wantUpdates, *updates)
				revisionTestMarkReady(t, r, child)
			}
			changed, children := revisionTestSync(t, r, set, current, update)
			assert.False(t, changed)
			assert.True(t, r.groupSetPartitionReady(set, children, update, 0))
			assert.Equal(t, []string{"test-1", "test-0"}, *updates)
		})
	}
}

func TestSyncRollingGroupSet_RecreateReplicaUpdateUsesReadySurge(t *testing.T) {
	current := &groupSetRevision{name: "v1", template: revisionTestTemplate(nil)}
	update := &groupSetRevision{name: "v2", template: revisionTestTemplate(nil)}
	update.template.Spec.Roles[0].Replicas = ptr.To(int32(2))
	set := revisionTestRollingSet(2, update)
	set.Spec.RolloutStrategy.Type = workloadsv1alpha2.RecreateStrategyType
	set.Spec.RolloutStrategy.MaxUnavailable = ptr.To(intstr.FromInt(0))
	set.Spec.RolloutStrategy.MaxSurge = ptr.To(intstr.FromInt(1))
	r, updates := revisionTestRollingReconciler(t, set,
		revisionTestReadyChild(set, 0, current), revisionTestReadyChild(set, 1, current))
	changed, children := revisionTestSync(t, r, set, current, update)
	require.True(t, changed)
	require.Len(t, children.Items, 3)
	assert.Empty(t, *updates, "unready surge cannot back a zero-unavailable scale update")
	changed, _ = revisionTestSync(t, r, set, current, update)
	assert.False(t, changed)
	revisionTestMarkReady(t, r, revisionTestGetChild(t, r, set, 2))

	for index := 1; index >= 0; index-- {
		changed, children = revisionTestSync(t, r, set, current, update)
		require.True(t, changed)
		child := revisionTestGetChild(t, r, set, index)
		assert.Equal(t, types.UID("original-"+child.Name), child.UID)
		assert.Equal(t, int32(2), *child.Spec.Roles[0].Replicas)
		ready := 0
		for i := range children.Items {
			if groupSetChildReady(&children.Items[i]) {
				ready++
			}
		}
		assert.Equal(t, 2, ready, "ready surge must preserve the floor while a base group scales")
		changed, _ = revisionTestSync(t, r, set, current, update)
		assert.False(t, changed)
		revisionTestMarkReady(t, r, child)
	}
	changed, children = revisionTestSync(t, r, set, current, update)
	require.True(t, changed, "finished scaling must reclaim surplus surge")
	assert.Len(t, children.Items, 2)
	assert.Equal(t, []string{"test-1", "test-0"}, *updates)
	assert.True(t, r.groupSetPartitionReady(set, children, update, 0))
}

func TestSyncRollingGroupSet_InPlaceUpdateFullSpecAndMetadata(t *testing.T) {
	current := &groupSetRevision{name: "v1", template: revisionTestTemplate(map[string]string{"changed": "old", "removed": "old"})}
	current.template.Labels = map[string]string{"changed": "old", "removed": "old"}
	current.template.Spec.RoleTemplates = []workloadsv1alpha2.RoleTemplate{{Name: "obsolete"}}
	current.template.Spec.Roles = append(current.template.Spec.Roles, workloadsv1alpha2.RoleSpec{Name: "retired", Replicas: ptr.To(int32(1))})
	update := &groupSetRevision{name: "v2", template: revisionTestTemplate(map[string]string{"changed": "new", "added": "new"})}
	update.template.Labels = map[string]string{"changed": "new", "added": "new"}
	update.template.Spec.RoleTemplates = []workloadsv1alpha2.RoleTemplate{{
		Name: "shared", Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "worker:v2"}}},
		},
	}}
	update.template.Spec.Roles[0].Replicas = ptr.To(int32(3))
	update.template.Spec.Roles[0].StandalonePattern = &workloadsv1alpha2.StandalonePattern{
		TemplateSource: workloadsv1alpha2.TemplateSource{TemplateRef: &workloadsv1alpha2.TemplateRef{Name: "shared"}},
	}
	update.template.Spec.Roles = append(update.template.Spec.Roles, workloadsv1alpha2.RoleSpec{
		Name: "helper", Replicas: ptr.To(int32(1)),
		Pattern: workloadsv1alpha2.Pattern{StandalonePattern: update.template.Spec.Roles[0].StandalonePattern.DeepCopy()},
	})
	update.template = normalizedGroupSetTemplate(update.template)
	set := revisionTestRollingSet(1, update)
	// Even if reserved labels leak into the raw template, child identity wins.
	for _, key := range []string{constants.GroupSetNameLabelKey, constants.GroupSetIndexLabelKey, constants.GroupSetRevisionLabelKey} {
		set.Spec.GroupTemplate.Labels[key] = "must-not-win"
	}
	original := revisionTestReadyChild(set, 0, current)
	original.Annotations[constants.DiscoveryConfigModeAnnotationKey] = "refine"
	original.CreationTimestamp = metav1.Now().Rfc3339Copy()
	original.Finalizers = []string{"test.io/preserve"}
	r, updates := revisionTestRollingReconciler(t, set, original)

	changed, _ := revisionTestSync(t, r, set, current, update)
	require.True(t, changed)
	child := revisionTestGetChild(t, r, set, 0)
	assert.Equal(t, update.template.Spec, child.Spec, "replace the whole spec, including roles and roleTemplates")
	assert.Equal(t, map[string]string{
		"changed": "new", "added": "new",
		constants.GroupSetNameLabelKey: set.Name, constants.GroupSetIndexLabelKey: "0", constants.GroupSetRevisionLabelKey: update.name,
	}, child.Labels)
	assert.Equal(t, map[string]string{
		"changed": "new", "added": "new", constants.DiscoveryConfigModeAnnotationKey: "refine",
		constants.RolloutRequestAnnotationKey: "1",
	}, child.Annotations)
	assert.Equal(t, original.Name, child.Name)
	assert.Equal(t, original.Namespace, child.Namespace)
	assert.Equal(t, original.UID, child.UID)
	assert.Equal(t, original.OwnerReferences, child.OwnerReferences)
	assert.True(t, original.CreationTimestamp.Equal(&child.CreationTimestamp),
		"in-place writes must not change the creation timestamp")
	assert.Equal(t, original.Finalizers, child.Finalizers)
	assert.Equal(t, original.Status, child.Status)
	assert.Equal(t, int64(2), child.Generation)
	assert.True(t, r.groupSetMatchesRevision(child, update))
	revisionTestMarkReady(t, r, child)
	changed, _ = revisionTestSync(t, r, set, current, update)
	assert.False(t, changed, "controller-owned annotations must not trigger another rollout")

	// Removing all user metadata still preserves system annotations and identity.
	cleared := &groupSetRevision{name: "v3", template: update.template.DeepCopy()}
	cleared.template.Labels = nil
	cleared.template.Annotations = nil
	set.Spec.GroupTemplate = *cleared.template.DeepCopy()
	changed, _ = revisionTestSync(t, r, set, current, cleared)
	require.True(t, changed)
	child = revisionTestGetChild(t, r, set, 0)
	assert.Equal(t, map[string]string{
		constants.GroupSetNameLabelKey: set.Name, constants.GroupSetIndexLabelKey: "0", constants.GroupSetRevisionLabelKey: cleared.name,
	}, child.Labels)
	assert.Equal(t, map[string]string{
		constants.DiscoveryConfigModeAnnotationKey: "refine", constants.RolloutRequestAnnotationKey: "2",
	}, child.Annotations)
	assert.Equal(t, cleared.template.Spec, child.Spec)
	assert.Equal(t, original.UID, child.UID)
	assert.Equal(t, original.OwnerReferences, child.OwnerReferences)
	assert.Equal(t, original.Finalizers, child.Finalizers)
	assert.Equal(t, int64(2), child.Generation, "metadata-only Updates must not bump generation")
	changed, children := revisionTestSync(t, r, set, current, cleared)
	assert.False(t, changed)
	assert.False(t, r.groupSetPartitionReady(set, children, cleared, 0),
		"the unchanged generation must not let the stale Ready status complete a metadata-only update")
	revisionTestMarkReady(t, r, child)
	changed, children = revisionTestSync(t, r, set, current, cleared)
	assert.False(t, changed)
	assert.True(t, r.groupSetPartitionReady(set, children, cleared, 0))
	assert.Equal(t, []string{"test-0", "test-0"}, *updates)
}

func TestSyncRollingGroupSet_InPlaceUpdateIgnoresStoredSurge(t *testing.T) {
	for _, surge := range []intstr.IntOrString{
		intstr.FromInt(99), intstr.FromString("99%"), intstr.FromString("invalid"), intstr.FromInt(-1),
	} {
		for _, unavailable := range []intstr.IntOrString{intstr.FromInt(0), intstr.FromString("0%"), intstr.FromString("19%")} {
			for _, partition := range []int{0, 1} {
				t.Run(fmt.Sprintf("surge=%s/unavailable=%s/partition=%d", surge.String(), unavailable.String(), partition), func(t *testing.T) {
					current := &groupSetRevision{name: "v1", template: revisionTestTemplate(nil)}
					update := &groupSetRevision{name: "v2", template: revisionTestTemplate(nil)}
					update.template.Spec.Roles[0].Replicas = ptr.To(int32(2))
					set := revisionTestRollingSet(3, update)
					set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt(partition))
					set.Spec.RolloutStrategy.MaxSurge = ptr.To(surge)
					set.Spec.RolloutStrategy.MaxUnavailable = ptr.To(unavailable)
					r, updates := revisionTestRollingReconciler(t, set,
						revisionTestReadyChild(set, 0, current), revisionTestReadyChild(set, 1, current), revisionTestReadyChild(set, 2, current))
					limits, err := resolveGroupSetRollout(set)
					require.NoError(t, err, "stored surge must not be parsed for InPlaceUpdate")
					require.Equal(t, groupSetRolloutLimits{partition: partition, maxUnavailable: 1, inPlaceUpdate: true}, limits)
					checkCounts := func(children *workloadsv1alpha2.RoleBasedGroupList, ready, updated, updatedReady int32) {
						t.Helper()
						require.Len(t, children.Items, 3, "ignored surge must never create extra children")
						for i := range children.Items {
							child := &children.Items[i]
							index, valid := groupSetOrdinal(set, child)
							require.True(t, valid)
							assert.Less(t, index, 3, "only base ordinals may exist")
							assert.Equal(t, types.UID("original-"+child.Name), child.UID)
						}
						require.NoError(t, r.updateRollingGroupSetStatus(context.Background(), set, children, current, update, limits))
						assert.Equal(t, int32(3), set.Status.Replicas)
						assert.Equal(t, ready, set.Status.ReadyReplicas)
						assert.Equal(t, int32(3)-updated, set.Status.CurrentReplicas)
						assert.Equal(t, updated, set.Status.UpdatedReplicas)
						assert.Equal(t, updatedReady, set.Status.UpdatedReadyReplicas)
						assert.Equal(t, int32(3-partition), set.Status.ExpectedUpdatedReplicas)
					}

					var wantUpdates []string
					for index := 2; index >= partition; index-- {
						changed, children := revisionTestSync(t, r, set, current, update)
						require.True(t, changed, "zero or rounded-down unavailable must fall back to one")
						child := revisionTestGetChild(t, r, set, index)
						wantUpdates = append(wantUpdates, child.Name)
						assert.Equal(t, wantUpdates, *updates, "update exactly one group in descending ordinal order")
						assert.Equal(t, update.template.Spec, child.Spec)
						assert.Equal(t, update.name, child.Labels[constants.GroupSetRevisionLabelKey])
						assert.Equal(t, int64(2), child.Generation)
						assert.Equal(t, int64(1), child.Status.ObservedGeneration)
						assert.Equal(t, metav1.ConditionTrue, child.Status.Conditions[0].Status)
						checkCounts(children, 2, int32(3-index), int32(2-index))
						for lower := 0; lower < index; lower++ {
							pending := revisionTestGetChild(t, r, set, lower)
							assert.Equal(t, current.template.Spec, pending.Spec, "lower ordinals must not advance early")
							assert.Equal(t, current.name, pending.Labels[constants.GroupSetRevisionLabelKey])
							assert.Equal(t, int64(1), pending.Generation)
						}
						for attempt := 0; attempt < 2; attempt++ {
							changed, children = revisionTestSync(t, r, set, current, update)
							assert.False(t, changed, "wait for the in-flight group, without creating surge")
							assert.Equal(t, wantUpdates, *updates)
							checkCounts(children, 2, int32(3-index), int32(2-index))
							assert.False(t, r.groupSetPartitionReady(set, children, update, partition))
						}
						revisionTestMarkReady(t, r, child)
					}

					changed, children := revisionTestSync(t, r, set, current, update)
					assert.False(t, changed, "no surge needs reclaiming after an in-place rollout")
					assert.Equal(t, wantUpdates, *updates)
					checkCounts(children, 3, int32(3-partition), int32(3-partition))
					assert.True(t, r.groupSetPartitionReady(set, children, update, partition))
				})
			}
		}
	}
}

func TestSyncRollingGroupSet_EmptyStrategyUpdatesInPlace(t *testing.T) {
	for _, allUnavailable := range []bool{false, true} {
		t.Run(fmt.Sprintf("allUnavailable=%t", allUnavailable), func(t *testing.T) {
			current := &groupSetRevision{name: "v1", template: revisionTestTemplate(nil)}
			update := &groupSetRevision{name: "v2", template: revisionTestTemplate(nil)}
			update.template.Spec.Roles[0].StandalonePattern = &workloadsv1alpha2.StandalonePattern{
				TemplateSource: workloadsv1alpha2.TemplateSource{Template: &corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "worker:v2"}}},
				}},
			}
			set := revisionTestRollingSet(3, update)
			set.Spec.RolloutStrategy = &workloadsv1alpha2.GroupSetRolloutStrategy{}
			objects := []client.Object{set}
			for index := 0; index < 3; index++ {
				child := revisionTestReadyChild(set, index, current)
				if allUnavailable {
					child.Status.Conditions[0].Status = metav1.ConditionFalse
				}
				require.False(t, groupSetOnlyReplicasChanged(child, update.template), "exercise the strategy, not Recreate's replicas-only shortcut")
				objects = append(objects, child)
			}
			r, updates := revisionTestRollingReconciler(t, objects...)
			batches := [][]int{{2}, {1}, {0}}
			if allUnavailable {
				batches = [][]int{{2, 1, 0}}
			}
			var wantUpdates []string
			for _, batch := range batches {
				changed, children := revisionTestSync(t, r, set, current, update)
				require.True(t, changed, "even zero available groups must not freeze repairs")
				require.Len(t, children.Items, 3, "an empty strategy must update rather than recreate")
				for _, index := range batch {
					wantUpdates = append(wantUpdates, fmt.Sprintf("test-%d", index))
				}
				assert.Equal(t, wantUpdates, *updates)
				for index := 0; index < 3; index++ {
					child := revisionTestGetChild(t, r, set, index)
					assert.Equal(t, types.UID("original-"+child.Name), child.UID)
					assert.True(t, metav1.IsControlledBy(child, set))
					assert.Nil(t, child.DeletionTimestamp)
					if index >= batch[len(batch)-1] {
						assert.Equal(t, update.template.Spec, child.Spec)
						assert.Equal(t, update.name, child.Labels[constants.GroupSetRevisionLabelKey])
						assert.Equal(t, int64(2), child.Generation)
					} else {
						assert.Equal(t, current.template.Spec, child.Spec)
						assert.Equal(t, current.name, child.Labels[constants.GroupSetRevisionLabelKey])
						assert.Equal(t, int64(1), child.Generation)
					}
				}
				changed, children = revisionTestSync(t, r, set, current, update)
				assert.False(t, changed, "do not advance until the updated groups are Ready")
				require.Len(t, children.Items, 3)
				assert.Equal(t, wantUpdates, *updates)
				for _, index := range batch {
					revisionTestMarkReady(t, r, revisionTestGetChild(t, r, set, index))
				}
			}
			changed, children := revisionTestSync(t, r, set, current, update)
			assert.False(t, changed)
			assert.True(t, r.groupSetPartitionReady(set, children, update, 0))
			assert.Equal(t, []string{"test-2", "test-1", "test-0"}, *updates)
		})
	}
}

func TestSyncRollingGroupSet_RecreateToInPlaceUpdateReclaimsStoredSurgeWithinBudget(t *testing.T) {
	ctx := context.Background()
	current := &groupSetRevision{name: "v1", template: revisionTestTemplate(map[string]string{"version": "v1"})}
	update := &groupSetRevision{name: "v2", template: revisionTestTemplate(map[string]string{"version": "v2"})}
	update.template.Spec.Roles[0].Replicas = ptr.To(int32(2))
	set := revisionTestRollingSet(3, update)
	set.Spec.RolloutStrategy.Type = workloadsv1alpha2.RecreateStrategyType
	set.Spec.RolloutStrategy.MaxSurge = ptr.To(intstr.FromInt(99))
	set.Spec.RolloutStrategy.MaxUnavailable = ptr.To(intstr.FromInt(0))
	r, updates := revisionTestRollingReconciler(t, set,
		revisionTestReadyChild(set, 0, current), revisionTestReadyChild(set, 1, current), revisionTestReadyChild(set, 2, current))
	changed, children := revisionTestSync(t, r, set, current, update)
	require.True(t, changed)
	require.Len(t, children.Items, 6, "Recreate can still warm up surge")
	require.Empty(t, *updates)
	for index := 3; index < 6; index++ {
		revisionTestMarkReady(t, r, revisionTestGetChild(t, r, set, index))
	}
	// Two base groups lose readiness before the strategy switch, so not all Ready
	// surge can be deleted without crossing replicas-maxUnavailable (3-1=2).
	for _, index := range []int{1, 2} {
		child := revisionTestGetChild(t, r, set, index)
		child.Status.Conditions[0].Status = metav1.ConditionFalse
		require.NoError(t, r.client.Status().Update(ctx, child))
	}
	set.Spec.RolloutStrategy.Type = workloadsv1alpha2.InPlaceUpdateStrategyType
	require.NoError(t, r.client.Update(ctx, set))
	changed, children = revisionTestSync(t, r, set, current, update)
	require.True(t, changed, "reclaim out-of-range surge before continuing the rollout")
	require.Len(t, children.Items, 4, "delete only the two Ready surge permitted by the shrink budget")
	assert.Empty(t, *updates)
	var remainingSurge *workloadsv1alpha2.RoleBasedGroup
	ready := 0
	for i := range children.Items {
		child := &children.Items[i]
		if groupSetChildReady(child) {
			ready++
		}
		index, valid := groupSetOrdinal(set, child)
		require.True(t, valid)
		if index >= 3 {
			require.Nil(t, remainingSurge)
			remainingSurge = child.DeepCopy()
		}
	}
	require.Equal(t, 2, ready)
	require.NotNil(t, remainingSurge)

	changed, children = revisionTestSync(t, r, set, current, update)
	require.True(t, changed, "repair unavailable base groups without spending more Ready budget")
	require.Len(t, children.Items, 4, "do not replace reclaimed surge despite stored maxSurge=99")
	assert.Equal(t, []string{"test-2", "test-1"}, *updates)
	for attempt := 0; attempt < 2; attempt++ {
		changed, children = revisionTestSync(t, r, set, current, update)
		assert.False(t, changed, "preserve the last Ready surge while shrink budget is exhausted")
		require.Len(t, children.Items, 4)
		assert.Equal(t, []string{"test-2", "test-1"}, *updates)
	}
	revisionTestMarkReady(t, r, revisionTestGetChild(t, r, set, 2))
	changed, children = revisionTestSync(t, r, set, current, update)
	require.True(t, changed)
	require.Len(t, children.Items, 3)
	assert.True(t, apierrors.IsNotFound(r.client.Get(ctx, client.ObjectKeyFromObject(remainingSurge), &workloadsv1alpha2.RoleBasedGroup{})))
	assert.Equal(t, []string{"test-2", "test-1"}, *updates)
	changed, _ = revisionTestSync(t, r, set, current, update)
	assert.False(t, changed, "the last base group still needs the unavailable group to recover")
	revisionTestMarkReady(t, r, revisionTestGetChild(t, r, set, 1))
	changed, children = revisionTestSync(t, r, set, current, update)
	require.True(t, changed)
	require.Len(t, children.Items, 3)
	assert.Equal(t, []string{"test-2", "test-1", "test-0"}, *updates)
	revisionTestMarkReady(t, r, revisionTestGetChild(t, r, set, 0))
	changed, children = revisionTestSync(t, r, set, current, update)
	assert.False(t, changed)
	require.Len(t, children.Items, 3)
	assert.True(t, r.groupSetPartitionReady(set, children, update, 0))
	for i := range children.Items {
		child := &children.Items[i]
		assert.Equal(t, types.UID("original-"+child.Name), child.UID, "base groups must not be recreated after the switch")
		assert.Equal(t, int64(2), child.Generation)
		assert.Equal(t, update.template.Spec, child.Spec)
	}
}

func revisionTestUserAnnotations(annotations map[string]string) map[string]string {
	result := maps.Clone(annotations)
	delete(result, constants.RolloutRequestAnnotationKey)
	return result
}

func TestSyncRollingGroupSet_InPlaceUpdateRapidTemplateChangeAndRollback(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%t", rollback), func(t *testing.T) {
			current := &groupSetRevision{name: "v1", template: revisionTestTemplate(map[string]string{"version": "v1"})}
			update := &groupSetRevision{name: "v2", template: revisionTestTemplate(map[string]string{"version": "v2"})}
			update.template.Spec.Roles[0].Replicas = ptr.To(int32(2))
			latest := &groupSetRevision{name: "v3", template: revisionTestTemplate(map[string]string{"version": "v3"})}
			latest.template.Spec.Roles[0].Replicas = ptr.To(int32(3))
			if rollback {
				latest = current
			}
			set := revisionTestRollingSet(3, update)
			r, updates := revisionTestRollingReconciler(t, set,
				revisionTestReadyChild(set, 0, current), revisionTestReadyChild(set, 1, current), revisionTestReadyChild(set, 2, current))
			changed, _ := revisionTestSync(t, r, set, current, update)
			require.True(t, changed)
			assert.Equal(t, []string{"test-2"}, *updates)
			inFlight := revisionTestGetChild(t, r, set, 2)
			assert.Equal(t, update.template.Spec, inFlight.Spec)
			assert.Equal(t, int64(2), inFlight.Generation)
			assert.False(t, groupSetChildReady(inFlight))

			// Change the target before v2 ever reports Ready. Repair the unavailable
			// group immediately, without waiting on v2 or taking down another group.
			set.Spec.GroupTemplate = *latest.template.DeepCopy()
			changed, children := revisionTestSync(t, r, set, current, latest)
			require.True(t, changed)
			require.Len(t, children.Items, 3)
			inFlight = revisionTestGetChild(t, r, set, 2)
			assert.Equal(t, latest.template.Spec, inFlight.Spec)
			assert.Equal(t, latest.name, inFlight.Labels[constants.GroupSetRevisionLabelKey])
			assert.Equal(t, latest.template.Annotations, revisionTestUserAnnotations(inFlight.Annotations))
			assert.Equal(t, types.UID("original-test-2"), inFlight.UID)
			assert.Equal(t, int64(3), inFlight.Generation)
			assert.Equal(t, int64(1), inFlight.Status.ObservedGeneration)
			assert.Equal(t, []string{"test-2", "test-2"}, *updates)
			for attempt := 0; attempt < 2; attempt++ {
				changed, _ = revisionTestSync(t, r, set, current, latest)
				assert.False(t, changed)
				assert.Equal(t, []string{"test-2", "test-2"}, *updates)
			}
			revisionTestMarkReady(t, r, inFlight)
			wantUpdates := []string{"test-2", "test-2"}
			if !rollback {
				for index := 1; index >= 0; index-- {
					changed, _ = revisionTestSync(t, r, set, current, latest)
					require.True(t, changed)
					child := revisionTestGetChild(t, r, set, index)
					wantUpdates = append(wantUpdates, child.Name)
					assert.Equal(t, wantUpdates, *updates)
					assert.Equal(t, latest.template.Spec, child.Spec, "remaining groups must skip the superseded template")
					assert.Equal(t, int64(2), child.Generation)
					revisionTestMarkReady(t, r, child)
				}
			}
			changed, children = revisionTestSync(t, r, set, current, latest)
			assert.False(t, changed)
			require.Len(t, children.Items, 3)
			assert.True(t, r.groupSetPartitionReady(set, children, latest, 0))
			assert.Equal(t, wantUpdates, *updates)
			for i := range children.Items {
				child := &children.Items[i]
				assert.Equal(t, types.UID("original-"+child.Name), child.UID)
				assert.Equal(t, latest.template.Spec, child.Spec)
				assert.Equal(t, latest.template.Annotations, revisionTestUserAnnotations(child.Annotations))
				assert.Equal(t, latest.name, child.Labels[constants.GroupSetRevisionLabelKey])
				if rollback && child.Name != "test-2" {
					assert.Equal(t, int64(1), child.Generation, "already matching groups must not be rewritten during rollback")
				}
			}
		})
	}
}

func TestSyncRollingGroupSet_StrategiesRecoverMissingOrdinals(t *testing.T) {
	for _, strategy := range []workloadsv1alpha2.GroupUpdateStrategyType{
		workloadsv1alpha2.RecreateStrategyType, workloadsv1alpha2.InPlaceUpdateStrategyType,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			current := &groupSetRevision{name: "v1", template: revisionTestTemplate(map[string]string{"version": "v1"})}
			update := &groupSetRevision{name: "v2", template: revisionTestTemplate(map[string]string{"version": "v2"})}
			set := revisionTestRollingSet(3, update)
			set.Spec.RolloutStrategy.Type = strategy
			set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt(3))
			set.Annotations[groupSetReplicasAnnotation] = "2"
			// Ordinal 0 disappeared; ordinal 2 is new scale-out. Both are below
			// partition, but only the previously existing one uses current revision.
			r, updates := revisionTestRollingReconciler(t, set, revisionTestReadyChild(set, 1, current))
			changed, children := revisionTestSync(t, r, set, current, update)
			require.True(t, changed)
			require.Len(t, children.Items, 3)
			assert.Empty(t, *updates)
			for index, revision := range []*groupSetRevision{current, current, update} {
				child := revisionTestGetChild(t, r, set, index)
				assert.True(t, r.groupSetMatchesRevision(child, revision))
				assert.True(t, metav1.IsControlledBy(child, set))
				assert.Equal(t, int64(1), child.Generation)
				assert.Equal(t, index == 1, groupSetChildReady(child))
			}
			storedSet := &workloadsv1alpha2.RoleBasedGroupSet{}
			require.NoError(t, r.client.Get(context.Background(), client.ObjectKeyFromObject(set), storedSet))
			assert.Equal(t, "3", storedSet.Annotations[groupSetReplicasAnnotation])
			limits, err := resolveGroupSetRollout(set)
			require.NoError(t, err)
			require.NoError(t, r.updateRollingGroupSetStatus(context.Background(), set, children, current, update, limits))
			assert.Equal(t, int32(3), set.Status.Replicas)
			assert.Equal(t, int32(1), set.Status.ReadyReplicas)
			assert.Equal(t, int32(2), set.Status.CurrentReplicas)
			assert.Equal(t, int32(1), set.Status.UpdatedReplicas)
			assert.Zero(t, set.Status.UpdatedReadyReplicas)
			changed, _ = revisionTestSync(t, r, set, current, update)
			assert.False(t, changed)
			assert.Empty(t, *updates)
		})
	}
}

func revisionTestPodGroupPolicyTemplate(timeout int) *workloadsv1alpha2.RoleBasedGroupTemplateSpec {
	template := revisionTestTemplate(map[string]string{
		constants.GangSchedulingAnnotationKey:             "true",
		constants.GangSchedulingScheduleTimeoutSecondsKey: fmt.Sprint(timeout),
	})
	// Add the raw conversion stash after normalization, just as conversion does on the hub template.
	template.Annotations[constants.ConversionV1alpha1PodGroupPolicyAnnotationKey] =
		fmt.Sprintf(`{"kubeScheduling":{"scheduleTimeoutSeconds":%d}}`, timeout)
	return template
}

func revisionTestPolicySnapshot(t *testing.T, r *RoleBasedGroupSetReconciler,
	set *workloadsv1alpha2.RoleBasedGroupSet, revision *groupSetRevision,
) *appsv1.ControllerRevision {
	t.Helper()
	snapshot := &appsv1.ControllerRevision{}
	require.NoError(t, r.client.Get(context.Background(), client.ObjectKey{Namespace: set.Namespace, Name: revision.name}, snapshot))
	require.True(t, metav1.IsControlledBy(snapshot, set))
	key := constants.ConversionV1alpha1PodGroupPolicyAnnotationKey
	want, wantPresent := revision.template.Annotations[key]
	got, gotPresent := snapshot.Annotations[key]
	assert.Equal(t, wantPresent, gotPresent)
	assert.Equal(t, want, got)
	assert.NotContains(t, string(snapshot.Data.Raw), key, "raw compatibility data must not enter revision identity")
	data, err := json.Marshal(normalizedGroupSetTemplate(revision.template))
	require.NoError(t, err)
	assert.JSONEq(t, string(data), string(snapshot.Data.Raw))
	return snapshot
}

// Seed stable child UIDs before attaching revision identities produced by the real persistence path.
func revisionTestPolicyFixture(t *testing.T, replicas int32, template *workloadsv1alpha2.RoleBasedGroupTemplateSpec,
) (*RoleBasedGroupSetReconciler, *workloadsv1alpha2.RoleBasedGroupSet, *groupSetRevision) {
	t.Helper()
	ctx := context.Background()
	seed := &groupSetRevision{template: template}
	set := revisionTestRollingSet(replicas, seed)
	objects := []client.Object{set}
	for index := 0; index < int(replicas); index++ {
		objects = append(objects, revisionTestReadyChild(set, index, seed))
	}
	r, _ := revisionTestRollingReconciler(t, objects...)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))
	source := set.Spec.GroupTemplate.DeepCopy()
	current, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
	require.NoError(t, err)
	assert.Equal(t, source, &set.Spec.GroupTemplate, "ensure must not normalize the source in place")
	revisionTestPolicySnapshot(t, r, set, current)
	for index := 0; index < int(replicas); index++ {
		child := revisionTestGetChild(t, r, set, index)
		child.Labels[constants.GroupSetRevisionLabelKey] = current.name
		require.NoError(t, r.client.Update(ctx, child))
	}
	set.Status.CurrentRevision, set.Status.UpdateRevision = current.name, current.name
	require.NoError(t, r.client.Status().Update(ctx, set))
	return r, set, current
}

// Assert the delete/create boundary explicitly so an in-place update cannot hide a replacement.
func revisionTestRollPolicyChild(t *testing.T, r *RoleBasedGroupSetReconciler,
	set *workloadsv1alpha2.RoleBasedGroupSet, current, update *groupSetRevision, index int,
) *workloadsv1alpha2.RoleBasedGroup {
	t.Helper()
	before := revisionTestGetChild(t, r, set, index)
	source, oldTemplate, newTemplate := set.DeepCopy(), current.template.DeepCopy(), update.template.DeepCopy()
	changed, _ := revisionTestSync(t, r, set, current, update)
	require.True(t, changed)
	if set.Spec.RolloutStrategy.Type == workloadsv1alpha2.RecreateStrategyType {
		require.True(t, apierrors.IsNotFound(r.client.Get(context.Background(), client.ObjectKeyFromObject(before), &workloadsv1alpha2.RoleBasedGroup{})))
		changed, _ = revisionTestSync(t, r, set, current, update)
		require.True(t, changed)
	}
	child := revisionTestGetChild(t, r, set, index)
	require.Equal(t, update.name, child.Labels[constants.GroupSetRevisionLabelKey])
	assert.True(t, r.groupSetMatchesRevision(child, update))
	for _, key := range []string{
		constants.ConversionV1alpha1PodGroupPolicyAnnotationKey,
		constants.GangSchedulingAnnotationKey, constants.GangSchedulingScheduleTimeoutSecondsKey,
	} {
		want, wantPresent := update.template.Annotations[key]
		got, gotPresent := child.Annotations[key]
		assert.Equal(t, wantPresent, gotPresent, "annotation presence: %s", key)
		assert.Equal(t, want, got, "annotation value: %s", key)
	}
	if set.Spec.RolloutStrategy.Type == workloadsv1alpha2.InPlaceUpdateStrategyType {
		assert.Equal(t, before.UID, child.UID)
		assert.Equal(t, before.Generation, child.Generation, "policy annotations alone must not change generation")
		assert.Equal(t, before.Status, child.Status)
	} else {
		assert.Equal(t, int64(1), child.Generation)
		assert.False(t, groupSetChildReady(child))
	}
	assert.Equal(t, before.Spec, child.Spec)
	assert.True(t, metav1.IsControlledBy(child, set))
	assert.Equal(t, source, set)
	assert.Equal(t, oldTemplate, current.template)
	assert.Equal(t, newTemplate, update.template)
	revisionTestMarkReady(t, r, child)
	return revisionTestGetChild(t, r, set, index)
}

func TestSyncRollingGroupSet_PodGroupPolicyLifecycle(t *testing.T) {
	for _, strategy := range []workloadsv1alpha2.GroupUpdateStrategyType{
		workloadsv1alpha2.InPlaceUpdateStrategyType, workloadsv1alpha2.RecreateStrategyType,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx := context.Background()
			key := constants.ConversionV1alpha1PodGroupPolicyAnnotationKey
			coordination := constants.ConversionV1alpha1CoordinationAnnotationKey
			r, set, current := revisionTestPolicyFixture(t, 2, revisionTestPodGroupPolicyTemplate(120))
			oldSnapshot := revisionTestPolicySnapshot(t, r, set, current).DeepCopy()
			oldTemplate := current.template.DeepCopy()
			first, second := revisionTestGetChild(t, r, set, 0), revisionTestGetChild(t, r, set, 1)
			set.Spec.RolloutStrategy.Type = strategy
			set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt(1))
			set.Spec.GroupTemplate = *revisionTestPodGroupPolicyTemplate(180)
			set.Spec.GroupTemplate.Annotations[coordination] = "live-coordination"
			require.NoError(t, r.client.Update(ctx, set))
			source := set.DeepCopy()
			update, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
			require.NoError(t, err)
			require.NotEqual(t, current.name, update.name, "the derived timeout is revisioned")
			assert.Equal(t, source, set)
			newSnapshot := revisionTestPolicySnapshot(t, r, set, update).DeepCopy()
			assert.Equal(t, "180", update.template.Annotations[constants.GangSchedulingScheduleTimeoutSecondsKey])

			assertHeld := func(before *workloadsv1alpha2.RoleBasedGroup, revision *groupSetRevision) {
				t.Helper()
				child := revisionTestGetChild(t, r, set, 0)
				assert.Equal(t, before.UID, child.UID)
				assert.Equal(t, before.Generation, child.Generation)
				assert.Equal(t, before.Spec, child.Spec)
				assert.Equal(t, before.Status, child.Status)
				assert.Equal(t, revision.name, child.Labels[constants.GroupSetRevisionLabelKey])
				assert.Equal(t, revision.template.Annotations[key], child.Annotations[key], "partition must hold the raw policy")
				assert.Equal(t, revision.template.Annotations[constants.GangSchedulingAnnotationKey], child.Annotations[constants.GangSchedulingAnnotationKey])
				assert.Equal(t, revision.template.Annotations[constants.GangSchedulingScheduleTimeoutSecondsKey], child.Annotations[constants.GangSchedulingScheduleTimeoutSecondsKey])
				assert.Equal(t, "live-coordination", child.Annotations[coordination], "other system metadata remains live")
			}
			// The all-child metadata pass may propagate coordination, but neither policy may advance yet.
			changed, _ := revisionTestSync(t, r, set, current, update)
			require.True(t, changed)
			assertHeld(first, current)
			pending := revisionTestGetChild(t, r, set, 1)
			assert.Equal(t, second.UID, pending.UID)
			assert.Equal(t, second.Generation, pending.Generation)
			assert.Equal(t, current.name, pending.Labels[constants.GroupSetRevisionLabelKey])
			assert.Equal(t, current.template.Annotations[key], pending.Annotations[key])
			assert.Equal(t, "120", pending.Annotations[constants.GangSchedulingScheduleTimeoutSecondsKey])

			second = revisionTestRollPolicyChild(t, r, set, current, update, 1)
			assertHeld(first, current)
			if strategy == workloadsv1alpha2.RecreateStrategyType {
				assert.NotEqual(t, types.UID("original-test-1"), second.UID)
			}
			changed, _ = revisionTestSync(t, r, set, current, update)
			assert.False(t, changed, "partition completion must not advance ordinal zero")

			// Even Recreate repairs compatibility metadata in place when the effective revision matches.
			for _, missing := range []bool{true, false} {
				stale := revisionTestGetChild(t, r, set, 1)
				if missing {
					delete(stale.Annotations, key)
				} else {
					stale.Annotations[key] = current.template.Annotations[key]
				}
				require.NoError(t, r.client.Update(ctx, stale))
				changed, _ = revisionTestSync(t, r, set, current, update)
				require.True(t, changed)
				repaired := revisionTestGetChild(t, r, set, 1)
				assert.Equal(t, second.UID, repaired.UID)
				assert.Equal(t, second.Generation, repaired.Generation)
				assert.Equal(t, second.Spec, repaired.Spec)
				assert.Equal(t, second.Status, repaired.Status)
				assert.Equal(t, second.Labels, repaired.Labels)
				assert.Equal(t, update.template.Annotations[key], repaired.Annotations[key])
				assertHeld(first, current)
				changed, _ = revisionTestSync(t, r, set, current, update)
				assert.False(t, changed)
			}

			// Lose the last 120 child, then reload revisions from persisted status and snapshots.
			require.NoError(t, r.client.Delete(ctx, revisionTestGetChild(t, r, set, 0)))
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))
			children, err := r.listRollingGroupSetChildren(ctx, set)
			require.NoError(t, err)
			require.Len(t, children.Items, 1)
			current, update, err = r.groupSetRevisions(ctx, set, children)
			require.NoError(t, err)
			assert.Equal(t, oldSnapshot.Name, current.name)
			assert.Equal(t, oldTemplate, current.template)
			assert.Equal(t, newSnapshot.Name, update.name)
			changed, _ = revisionTestSync(t, r, set, current, update)
			require.True(t, changed)
			rebuilt := revisionTestGetChild(t, r, set, 0)
			assert.NotEqual(t, first.UID, rebuilt.UID)
			assert.Equal(t, int64(1), rebuilt.Generation)
			assertHeld(rebuilt, current)
			assert.NotEqual(t, update.template.Annotations[key], rebuilt.Annotations[key], "recovery must not borrow live 180 policy")
			assert.True(t, metav1.IsControlledBy(rebuilt, set))
			revisionTestMarkReady(t, r, rebuilt)
			rebuilt = revisionTestGetChild(t, r, set, 0)
			changed, _ = revisionTestSync(t, r, set, current, update)
			assert.False(t, changed)
			assertHeld(rebuilt, current)

			set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt(0))
			require.NoError(t, r.client.Update(ctx, set))
			settledSecond := revisionTestGetChild(t, r, set, 1)
			first = revisionTestRollPolicyChild(t, r, set, current, update, 0)
			assert.Equal(t, settledSecond, revisionTestGetChild(t, r, set, 1), "lifting partition must not rewrite the settled child")
			changed, _ = revisionTestSync(t, r, set, current, update)
			assert.False(t, changed)

			// Policy removal must respect partition just like other effective configuration changes.
			set.Status.CurrentRevision = update.name
			require.NoError(t, r.client.Status().Update(ctx, set))
			set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt(1))
			delete(set.Spec.GroupTemplate.Annotations, key)
			delete(set.Spec.GroupTemplate.Annotations, constants.GangSchedulingAnnotationKey)
			delete(set.Spec.GroupTemplate.Annotations, constants.GangSchedulingScheduleTimeoutSecondsKey)
			require.NoError(t, r.client.Update(ctx, set))
			children, err = r.listRollingGroupSetChildren(ctx, set)
			require.NoError(t, err)
			current, update, err = r.groupSetRevisions(ctx, set, children)
			require.NoError(t, err)
			require.Equal(t, newSnapshot.Name, current.name)
			require.NotEqual(t, current.name, update.name)
			revisionTestPolicySnapshot(t, r, set, update)
			revisionTestRollPolicyChild(t, r, set, current, update, 1)
			assertHeld(first, current)
			changed, _ = revisionTestSync(t, r, set, current, update)
			assert.False(t, changed)
			set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt(0))
			require.NoError(t, r.client.Update(ctx, set))
			revisionTestRollPolicyChild(t, r, set, current, update, 0)
			changed, children = revisionTestSync(t, r, set, current, update)
			assert.False(t, changed)
			require.Len(t, children.Items, 2)
			for _, child := range children.Items {
				assert.NotContains(t, child.Annotations, key)
				assert.NotContains(t, child.Annotations, constants.GangSchedulingAnnotationKey)
				assert.NotContains(t, child.Annotations, constants.GangSchedulingScheduleTimeoutSecondsKey)
				assert.Equal(t, "live-coordination", child.Annotations[coordination])
			}
			assert.Equal(t, oldSnapshot, revisionTestPolicySnapshot(t, r, set, &groupSetRevision{name: oldSnapshot.Name, template: oldTemplate}))
			assert.Equal(t, newSnapshot, revisionTestPolicySnapshot(t, r, set, current), "removal must not erase historical snapshot policy")
		})
	}
}

func TestEnsureGroupSetRevision_PodGroupPolicyRawFormatting(t *testing.T) {
	for _, strategy := range []workloadsv1alpha2.GroupUpdateStrategyType{
		workloadsv1alpha2.InPlaceUpdateStrategyType, workloadsv1alpha2.RecreateStrategyType,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx := context.Background()
			key := constants.ConversionV1alpha1PodGroupPolicyAnnotationKey
			r, set, current := revisionTestPolicyFixture(t, 1, revisionTestPodGroupPolicyTemplate(120))
			before := revisionTestGetChild(t, r, set, 0)
			snapshot := revisionTestPolicySnapshot(t, r, set, current).DeepCopy()
			oldTemplate := current.template.DeepCopy()
			set.Spec.RolloutStrategy.Type = strategy
			set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt(1))
			set.Spec.GroupTemplate.Annotations[key] = "{\n  \"kubeScheduling\": { \"scheduleTimeoutSeconds\": 120 }\n}"
			require.NoError(t, r.client.Update(ctx, set))
			source := set.DeepCopy()
			update, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
			require.NoError(t, err)
			require.Equal(t, current.name, update.name)
			assert.JSONEq(t, current.template.Annotations[key], update.template.Annotations[key])
			assert.Equal(t, set.Spec.GroupTemplate.Annotations[key], update.template.Annotations[key])
			assert.Equal(t, source, set)
			assert.Equal(t, oldTemplate, current.template)
			after := revisionTestPolicySnapshot(t, r, set, update)
			assert.Equal(t, snapshot.Data.Raw, after.Data.Raw)
			assert.Equal(t, snapshot.UID, after.UID)
			assert.Equal(t, snapshot.Revision, after.Revision)
			assert.Equal(t, snapshot.OwnerReferences, after.OwnerReferences)
			assert.NotEqual(t, snapshot.ResourceVersion, after.ResourceVersion, "same hash still persists the new raw stash")
			children, err := r.listRollingGroupSetChildren(ctx, set)
			require.NoError(t, err)
			loaded, loadedUpdate, err := r.groupSetRevisions(ctx, set, children)
			require.NoError(t, err)
			assert.Equal(t, update.template, loaded.template)
			assert.Equal(t, update.template, loadedUpdate.template)
			changed, _ := revisionTestSync(t, r, set, loaded, loadedUpdate)
			require.True(t, changed, "a matching revision can repair raw metadata even below partition")
			child := revisionTestGetChild(t, r, set, 0)
			assert.Equal(t, before.UID, child.UID)
			assert.Equal(t, before.Generation, child.Generation)
			assert.Equal(t, before.Spec, child.Spec)
			assert.Equal(t, before.Labels, child.Labels)
			assert.Equal(t, before.Status, child.Status)
			assert.Equal(t, set.Spec.GroupTemplate.Annotations[key], child.Annotations[key])
			changed, _ = revisionTestSync(t, r, set, loaded, loadedUpdate)
			assert.False(t, changed)
			revisions := &appsv1.ControllerRevisionList{}
			require.NoError(t, r.client.List(ctx, revisions))
			require.Len(t, revisions.Items, 1)
			for annotation, value := range map[string]string{
				constants.GangSchedulingAnnotationKey: "false", constants.GangSchedulingScheduleTimeoutSecondsKey: "240",
			} {
				template := set.Spec.GroupTemplate.DeepCopy()
				template.Annotations[annotation] = value
				original := template.DeepCopy()
				userRevision, err := r.ensureGroupSetRevision(ctx, set, template)
				require.NoError(t, err)
				assert.NotEqual(t, update.name, userRevision.name, "user gang configuration must remain revisioned: %s", annotation)
				assert.Equal(t, original, template)
				revisionTestPolicySnapshot(t, r, set, userRevision)
			}
			assert.Equal(t, source, set)
			assert.Equal(t, oldTemplate, current.template)
		})
	}
}

func TestSyncRollingGroupSet_PodGroupPolicyChildOnlyStash(t *testing.T) {
	for _, strategy := range []workloadsv1alpha2.GroupUpdateStrategyType{
		workloadsv1alpha2.InPlaceUpdateStrategyType, workloadsv1alpha2.RecreateStrategyType,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx := context.Background()
			key := constants.ConversionV1alpha1PodGroupPolicyAnnotationKey
			r, set, revision := revisionTestPolicyFixture(t, 1, revisionTestTemplate(nil))
			child := revisionTestGetChild(t, r, set, 0)
			child.Annotations = map[string]string{key: revisionTestPodGroupPolicyTemplate(120).Annotations[key]}
			require.NoError(t, r.client.Update(ctx, child))
			before := child.DeepCopy()
			set.Spec.RolloutStrategy.Type = strategy
			set.Spec.GroupTemplate.Annotations = map[string]string{constants.DiscoveryConfigModeAnnotationKey: "refine"}
			require.NoError(t, r.client.Update(ctx, set))
			update, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
			require.NoError(t, err)
			require.Equal(t, revision.name, update.name)
			snapshot := revisionTestPolicySnapshot(t, r, set, update).DeepCopy()
			assert.NotContains(t, snapshot.Annotations, key, "the parent never declared this child-only policy")
			changed, _ := revisionTestSync(t, r, set, revision, update)
			require.True(t, changed, "exercise the all-child metadata pass")
			child = revisionTestGetChild(t, r, set, 0)
			assert.Equal(t, before.Annotations[key], child.Annotations[key])
			assert.Equal(t, "refine", child.Annotations[constants.DiscoveryConfigModeAnnotationKey])
			assert.Equal(t, before.UID, child.UID)
			assert.Equal(t, before.Generation, child.Generation)
			assert.Equal(t, before.Spec, child.Spec)
			assert.Equal(t, before.Labels, child.Labels)
			assert.Equal(t, before.Status, child.Status)
			changed, _ = revisionTestSync(t, r, set, revision, update)
			assert.False(t, changed)
			assert.Equal(t, snapshot, revisionTestPolicySnapshot(t, r, set, update))
		})
	}
}

func TestSyncRollingGroupSet_EmptyPodGroupPolicyRemoval(t *testing.T) {
	for _, strategy := range []workloadsv1alpha2.GroupUpdateStrategyType{
		workloadsv1alpha2.InPlaceUpdateStrategyType, workloadsv1alpha2.RecreateStrategyType,
	} {
		for _, replicasOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replicasOnly=%t", strategy, replicasOnly), func(t *testing.T) {
				ctx := context.Background()
				key := constants.ConversionV1alpha1PodGroupPolicyAnnotationKey
				template := revisionTestTemplate(nil)
				template.Annotations = map[string]string{key: "{}"}
				r, set, current := revisionTestPolicyFixture(t, 1, template)
				before := revisionTestGetChild(t, r, set, 0)
				set.Spec.RolloutStrategy.Type = strategy
				delete(set.Spec.GroupTemplate.Annotations, key)
				if replicasOnly {
					set.Spec.GroupTemplate.Spec.Roles[0].Replicas = ptr.To(int32(2))
				}
				require.NoError(t, r.client.Update(ctx, set))
				update, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
				require.NoError(t, err)
				assert.Equal(t, !replicasOnly, current.name == update.name)
				changed, _ := revisionTestSync(t, r, set, current, update)
				require.True(t, changed)
				child := revisionTestGetChild(t, r, set, 0)
				assert.Equal(t, before.UID, child.UID, "Recreate's replicas-only path must still update in place")
				assert.NotContains(t, child.Annotations, key)
				assert.Equal(t, update.name, child.Labels[constants.GroupSetRevisionLabelKey])
				revisionTestMarkReady(t, r, child)
				update, err = r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
				require.NoError(t, err)
				revisionTestPolicySnapshot(t, r, set, update)
				changed, _ = revisionTestSync(t, r, set, current, update)
				assert.False(t, changed)

				child = revisionTestGetChild(t, r, set, 0)
				if child.Annotations == nil {
					child.Annotations = make(map[string]string)
				}
				child.Annotations[key] = "child-only"
				require.NoError(t, r.client.Update(ctx, child))
				changed, _ = revisionTestSync(t, r, set, current, update)
				assert.False(t, changed, "a cleared snapshot must not authorize deleting a later child-only stash")
				assert.Equal(t, "child-only", revisionTestGetChild(t, r, set, 0).Annotations[key])

				set.Status.CurrentRevision = update.name
				require.NoError(t, r.client.Status().Update(ctx, set))
				set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt(1))
				set.Spec.GroupTemplate.Spec.Roles[0].Replicas = ptr.To(int32(3))
				require.NoError(t, r.client.Update(ctx, set))
				require.NoError(t, r.client.Delete(ctx, revisionTestGetChild(t, r, set, 0)))
				children, err := r.listRollingGroupSetChildren(ctx, set)
				require.NoError(t, err)
				loaded, latest, err := r.groupSetRevisions(ctx, set, children)
				require.NoError(t, err)
				assert.NotContains(t, loaded.template.Annotations, key, "removed policy must not reappear in a held revision")
				changed, _ = revisionTestSync(t, r, set, loaded, latest)
				require.True(t, changed)
				assert.NotContains(t, revisionTestGetChild(t, r, set, 0).Annotations, key)
			})
		}
	}
}

func TestGroupSetRevisions_BootstrapPodGroupPolicySnapshot(t *testing.T) {
	ctx := context.Background()
	key := constants.ConversionV1alpha1PodGroupPolicyAnnotationKey
	set := revisionTestRollingSet(1, &groupSetRevision{template: revisionTestPodGroupPolicyTemplate(180)})
	legacy := revisionTestReadyChild(set, 0, &groupSetRevision{template: revisionTestPodGroupPolicyTemplate(120)})
	delete(legacy.Labels, constants.GroupSetRevisionLabelKey)
	r, _ := revisionTestRollingReconciler(t, set, legacy)
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))
	children, err := r.listRollingGroupSetChildren(ctx, set)
	require.NoError(t, err)
	setSource, childrenSource := set.DeepCopy(), children.DeepCopy()
	current, update, err := r.groupSetRevisions(ctx, set, children)
	require.NoError(t, err)
	require.NotEqual(t, current.name, update.name)
	assert.Equal(t, legacy.Annotations[key], current.template.Annotations[key])
	assert.Equal(t, "120", current.template.Annotations[constants.GangSchedulingScheduleTimeoutSecondsKey])
	assert.Equal(t, set.Spec.GroupTemplate.Annotations[key], update.template.Annotations[key])
	assert.Equal(t, "180", update.template.Annotations[constants.GangSchedulingScheduleTimeoutSecondsKey])
	oldSnapshot := revisionTestPolicySnapshot(t, r, set, current).DeepCopy()
	revisionTestPolicySnapshot(t, r, set, update)
	assert.Equal(t, setSource, set)
	assert.Equal(t, childrenSource, children)
	assert.Equal(t, legacy.UID, revisionTestGetChild(t, r, set, 0).UID)
	assert.NotContains(t, revisionTestGetChild(t, r, set, 0).Labels, constants.GroupSetRevisionLabelKey)

	set.Status.CurrentRevision, set.Status.UpdateRevision = current.name, update.name
	require.NoError(t, r.client.Status().Update(ctx, set))
	require.NoError(t, r.client.Delete(ctx, revisionTestGetChild(t, r, set, 0)))
	require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))
	children, err = r.listRollingGroupSetChildren(ctx, set)
	require.NoError(t, err)
	require.Empty(t, children.Items)
	loaded, _, err := r.groupSetRevisions(ctx, set, children)
	require.NoError(t, err)
	assert.Equal(t, current.name, loaded.name)
	assert.True(t, groupSetTemplatesEqual(current.template, loaded.template))
	assert.Equal(t, current.template.Annotations, loaded.template.Annotations)
	assert.Equal(t, oldSnapshot, revisionTestPolicySnapshot(t, r, set, loaded))
	set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt(1))
	require.NoError(t, r.client.Update(ctx, set))
	changed, _ := revisionTestSync(t, r, set, loaded, update)
	require.True(t, changed)
	rebuilt := revisionTestGetChild(t, r, set, 0)
	assert.Equal(t, legacy.Annotations[key], rebuilt.Annotations[key])
	assert.Equal(t, "120", rebuilt.Annotations[constants.GangSchedulingScheduleTimeoutSecondsKey])
	assert.Equal(t, loaded.name, rebuilt.Labels[constants.GroupSetRevisionLabelKey])
	assert.NotEqual(t, legacy.UID, rebuilt.UID)
	assert.Equal(t, int64(1), rebuilt.Generation)
}

func TestSyncRollingGroupSet_PodGroupPolicyRemovalUsesPreviousOwnedSnapshot(t *testing.T) {
	for _, tt := range []struct {
		name                       string
		snapshotted, foreign, lost bool
		wantRemoved                bool
	}{
		{name: "parent-owned policy", snapshotted: true, wantRemoved: true},
		{name: "child-only policy"},
		{name: "foreign snapshot cannot authorize removal", snapshotted: true, foreign: true},
		{name: "missing snapshot cannot authorize removal", snapshotted: true, lost: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			key := constants.ConversionV1alpha1PodGroupPolicyAnnotationKey
			raw := revisionTestPodGroupPolicyTemplate(120).Annotations[key]
			oldTemplate := revisionTestTemplate(map[string]string{"version": "old"})
			if tt.snapshotted {
				oldTemplate.Annotations[key] = raw
			}
			set := revisionTestRollingSet(1, &groupSetRevision{template: oldTemplate})
			r, _ := revisionTestRollingReconciler(t, set)
			require.NoError(t, r.client.Get(ctx, client.ObjectKeyFromObject(set), set))
			owner := set.DeepCopy()
			if tt.foreign {
				owner.UID = "another-set"
			}
			current, err := r.ensureGroupSetRevision(ctx, owner, oldTemplate)
			require.NoError(t, err)
			snapshot := revisionTestPolicySnapshot(t, r, owner, current).DeepCopy()
			child := revisionTestReadyChild(set, 0, current)
			child.Annotations[key] = raw
			require.NoError(t, r.client.Create(ctx, child))
			before := revisionTestGetChild(t, r, set, 0)
			if tt.lost {
				require.NoError(t, r.client.Delete(ctx, snapshot))
			}
			set.Spec.GroupTemplate = *revisionTestTemplate(map[string]string{"version": "new"})
			set.Spec.GroupTemplate.Spec.Roles[0].Replicas = ptr.To(int32(2))
			require.NoError(t, r.client.Update(ctx, set))
			update, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
			require.NoError(t, err)
			require.NotEqual(t, current.name, update.name)
			revisionTestPolicySnapshot(t, r, set, update)
			source, oldSource, newSource := set.DeepCopy(), current.template.DeepCopy(), update.template.DeepCopy()
			changed, _ := revisionTestSync(t, r, set, current, update)
			require.True(t, changed)
			child = revisionTestGetChild(t, r, set, 0)
			if tt.wantRemoved {
				assert.NotContains(t, child.Annotations, key, "remove using the old label, before advancing spec and revision identity")
			} else {
				assert.Equal(t, raw, child.Annotations[key])
			}
			assert.Equal(t, before.UID, child.UID)
			assert.Equal(t, before.Generation+1, child.Generation, "only the spec change increments generation")
			assert.Equal(t, before.Status, child.Status)
			assert.Equal(t, update.template.Spec, child.Spec)
			assert.Equal(t, update.name, child.Labels[constants.GroupSetRevisionLabelKey])
			assert.Equal(t, source, set)
			assert.Equal(t, oldSource, current.template)
			assert.Equal(t, newSource, update.template)
			if !tt.lost {
				assert.Equal(t, snapshot, revisionTestPolicySnapshot(t, r, owner, current))
			}
			revisionTestMarkReady(t, r, child)
			changed, _ = revisionTestSync(t, r, set, current, update)
			assert.False(t, changed)
		})
	}
}

// ControllerRevision data is immutable and compared byte-wise, while a strategic-merge patch
// re-serializes the whole object with sorted keys. Garbage collection orphans built-in dependents
// through such a patch, so the stored payload must already be canonical: otherwise the API server
// rejects the merge and the orphan finalizer never clears.
func TestEnsureGroupSetRevision_DataSurvivesStrategicMergePatch(t *testing.T) {
	ctx := context.Background()
	template := revisionTestTemplate(map[string]string{"user.io/probe": "a<b>&c"})
	template.Labels = map[string]string{"user.io/tier": "cache"}
	template.Spec.Roles[0].Pattern = workloadsv1alpha2.Pattern{StandalonePattern: &workloadsv1alpha2.StandalonePattern{
		TemplateSource: workloadsv1alpha2.TemplateSource{Template: &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "worker", Image: "busybox", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
			}}},
		}},
	}}
	set := revisionTestRollingSet(1, &groupSetRevision{template: template})
	r, _ := revisionTestRollingReconciler(t, set)
	revision, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
	require.NoError(t, err)
	stored := &appsv1.ControllerRevision{}
	require.NoError(t, r.client.Get(ctx, client.ObjectKey{Namespace: set.Namespace, Name: revision.name}, stored))
	assert.Contains(t, string(stored.Data.Raw), `a\u003cb\u003e\u0026c`, "escaping must match what the API server re-emits")
	typed, err := json.Marshal(normalizedGroupSetTemplate(&set.Spec.GroupTemplate))
	require.NoError(t, err)
	assert.NotEqual(t, string(typed), string(stored.Data.Raw),
		"the fixture must store a payload whose struct field order is not already sorted")

	original, err := json.Marshal(stored)
	require.NoError(t, err)
	patched, err := strategicpatch.StrategicMergePatch(original,
		[]byte(`{"metadata":{"finalizers":["rbg.io/probe"]}}`), &appsv1.ControllerRevision{})
	require.NoError(t, err)
	merged := &appsv1.ControllerRevision{}
	require.NoError(t, json.Unmarshal(patched, merged))
	assert.Equal(t, string(stored.Data.Raw), string(merged.Data.Raw), "merging must not rewrite the immutable payload")
	assert.Equal(t, []string{"rbg.io/probe"}, merged.Finalizers)
}

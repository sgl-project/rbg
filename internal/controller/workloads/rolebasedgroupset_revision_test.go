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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
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

func TestWarmUpGroupSetSurge_BoundedByRemainingWork(t *testing.T) {
	tests := []struct {
		name           string
		maxSurge       int
		maxUnavailable int
		outdatedBase   int
		existingSurge  []int
		wantCreated    int
	}{
		{name: "surge exceeds replicas", maxSurge: 5, outdatedBase: 4, wantCreated: 4},
		{name: "surge remains the upper bound", maxSurge: 2, outdatedBase: 4, wantCreated: 2},
		{name: "only partition batch needs surge", maxSurge: 5, outdatedBase: 2, wantCreated: 2},
		{name: "unavailable budget covers part of batch", maxSurge: 5, maxUnavailable: 1, outdatedBase: 4, wantCreated: 3},
		{name: "unavailable budget covers whole batch", maxSurge: 5, maxUnavailable: 2, outdatedBase: 2},
		{name: "no remaining work", maxSurge: 5},
		{name: "surge disabled", outdatedBase: 4},
		{name: "existing surge counts toward target", maxSurge: 5, outdatedBase: 4, existingSurge: []int{4, 5}, wantCreated: 2},
		{name: "higher ordinal surge already covers remaining work", maxSurge: 5, outdatedBase: 1, existingSurge: []int{7}},
		{name: "surge with gaps only fills shortfall", maxSurge: 5, outdatedBase: 3, existingSurge: []int{6, 8}, wantCreated: 1},
		{name: "existing surge exceeds remaining work", maxSurge: 5, outdatedBase: 1, existingSurge: []int{4, 5}},
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
			changed, err := r.warmUpGroupSetSurge(ctx, set, existing, 4, limits, update,
				tt.outdatedBase, tt.maxSurge-len(tt.existingSurge))
			require.NoError(t, err)
			assert.Equal(t, tt.wantCreated > 0, changed)
			children := &workloadsv1alpha2.RoleBasedGroupList{}
			require.NoError(t, c.List(ctx, children, client.InNamespace(set.Namespace)))
			assert.Len(t, children.Items, len(tt.existingSurge)+tt.wantCreated)
		})
	}
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

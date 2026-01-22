/*
Copyright 2025.

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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestRoleBasedGroupSetReconciler_scaleUp tests the scaleUp function.
func TestRoleBasedGroupSetReconciler_scaleUp(t *testing.T) {
	// Setup test scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)

	// Create a RoleBasedGroupSet for testing
	rbgset := &workloadsv1alpha1.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbgset",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
			Template: workloadsv1alpha1.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha1.RoleSpec{
					{Name: "role-1"},
					{Name: "role-2"},
				},
			},
		},
	}

	tests := []struct {
		name        string
		count       int
		expectError bool
	}{
		{
			name:        "Create 3 RBGs",
			count:       3,
			expectError: false,
		},
		{
			name:        "Create 0 RBGs",
			count:       0,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Create test reconciler with a fake client
				r := &RoleBasedGroupSetReconciler{
					client: fake.NewClientBuilder().WithScheme(scheme).Build(),
					scheme: scheme,
				}

				// The new scaleUp function expects a list of objects to create.
				// We generate this list based on the test case count.
				var rbgsToCreate []*workloadsv1alpha1.RoleBasedGroup
				for i := 0; i < tt.count; i++ {
					rbgsToCreate = append(rbgsToCreate, newRBGForSet(rbgset, i))
				}

				err := r.scaleUp(context.Background(), rbgset, rbgsToCreate)
				if tt.expectError {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}

				// Verify the result by listing the created objects.
				var rbglist workloadsv1alpha1.RoleBasedGroupList
				opts := []client.ListOption{
					client.InNamespace(rbgset.Namespace),
					client.MatchingLabels{workloadsv1alpha1.SetRBGSetNameLabelKey: rbgset.Name},
				}
				err = r.client.List(context.Background(), &rbglist, opts...)
				assert.NoError(t, err)
				assert.Equal(t, tt.count, len(rbglist.Items))
			},
		)
	}
}

// TestRoleBasedGroupSetReconciler_scaleDown tests the scaleDown function.
func TestRoleBasedGroupSetReconciler_scaleDown(t *testing.T) {
	// Setup test scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)

	rbgBase := []workloadsv1alpha1.RoleBasedGroup{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rbg-0",
				Namespace: "default",
				Labels: map[string]string{
					workloadsv1alpha1.SetRBGSetNameLabelKey: "rbgs-test",
					workloadsv1alpha1.SetRBGIndexLabelKey:   "0",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rbg-1",
				Namespace: "default",
				Labels: map[string]string{
					workloadsv1alpha1.SetRBGSetNameLabelKey: "rbgs-test",
					workloadsv1alpha1.SetRBGIndexLabelKey:   "1",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rbg-2",
				Namespace: "default",
				Labels: map[string]string{
					workloadsv1alpha1.SetRBGSetNameLabelKey: "rbgs-test",
					workloadsv1alpha1.SetRBGIndexLabelKey:   "2",
				},
			},
		},
	}

	tests := []struct {
		name                string
		initialRBGs         []workloadsv1alpha1.RoleBasedGroup
		rbgsToDeleteIndices []int // Indices from initialRBGs to delete
		expectedNamesLeft   []string
	}{
		{
			name:                "Delete 2 out of 3 RBGs",
			initialRBGs:         rbgBase,
			rbgsToDeleteIndices: []int{0, 2}, // Delete rbg-1 and rbg-3
			expectedNamesLeft:   []string{"rbg-1"},
		},
		{
			name:                "Delete all RBGs",
			initialRBGs:         rbgBase,
			rbgsToDeleteIndices: []int{0, 1, 2},
			expectedNamesLeft:   []string{},
		},
		{
			name:                "Delete 0 items",
			initialRBGs:         rbgBase,
			rbgsToDeleteIndices: []int{},
			expectedNamesLeft:   []string{"rbg-0", "rbg-1", "rbg-2"},
		},
		{
			name:                "Delete from an empty list",
			initialRBGs:         []workloadsv1alpha1.RoleBasedGroup{},
			rbgsToDeleteIndices: []int{},
			expectedNamesLeft:   []string{},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Prepare initial objects for the fake client
				objs := make([]runtime.Object, len(tt.initialRBGs))
				for i := range tt.initialRBGs {
					objs[i] = &tt.initialRBGs[i]
				}
				r := &RoleBasedGroupSetReconciler{
					client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build(),
				}

				// The new scaleDown function expects an explicit list of objects to delete.
				var rbgsToDelete []*workloadsv1alpha1.RoleBasedGroup
				for _, index := range tt.rbgsToDeleteIndices {
					// We need to pass pointers to copies to avoid issues with loop variables.
					rbgCopy := tt.initialRBGs[index].DeepCopy()
					rbgsToDelete = append(rbgsToDelete, rbgCopy)
				}

				err := r.scaleDown(context.Background(), rbgsToDelete)
				assert.NoError(t, err)

				// Verify the result by listing the remaining objects.
				var leftRbgList workloadsv1alpha1.RoleBasedGroupList
				opts := []client.ListOption{
					client.InNamespace("default"),
					client.MatchingLabels{workloadsv1alpha1.SetRBGSetNameLabelKey: "rbgs-test"},
				}
				err = r.client.List(context.Background(), &leftRbgList, opts...)
				assert.NoError(t, err)
				assert.Equal(t, len(tt.expectedNamesLeft), len(leftRbgList.Items))

				// Check if the correct items are left.
				remainingNames := make(map[string]bool)
				for _, rbg := range leftRbgList.Items {
					remainingNames[rbg.Name] = true
				}
				for _, expectedName := range tt.expectedNamesLeft {
					assert.True(
						t, remainingNames[expectedName],
						fmt.Sprintf("Expected RBG %s to remain, but it was deleted", expectedName),
					)
				}
			},
		)
	}
}

// TestRoleBasedGroupSetReconciler_needsUpdate tests the needsUpdate method.
func TestRoleBasedGroupSetReconciler_needsUpdate(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)

	tests := []struct {
		name           string
		rbgset         *workloadsv1alpha1.RoleBasedGroupSet
		rbg            *workloadsv1alpha1.RoleBasedGroup
		expectedUpdate bool
	}{
		{
			name: "RBG needs update - different roles",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbgset",
					Namespace: "default",
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
						Roles: []workloadsv1alpha1.RoleSpec{
							{Name: "new-role"},
						},
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbgset",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{Name: "old-role"},
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG needs update - missing annotation",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						workloadsv1alpha1.ExclusiveKeyAnnotationKey: "test-exclusive",
					},
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
						Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG needs update - different CoordinationRequirements",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
						Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
						CoordinationRequirements: []workloadsv1alpha1.Coordination{
							{
								Name:  "new-coordination",
								Roles: []string{"role-1", "role-2"},
							},
						},
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Name:  "old-coordination",
							Roles: []string{"role-1"},
						},
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG needs update - CoordinationRequirements added",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
						Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
						CoordinationRequirements: []workloadsv1alpha1.Coordination{
							{
								Name:  "coordination",
								Roles: []string{"role-1"},
							},
						},
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles:                    []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
					CoordinationRequirements: nil,
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG needs update - different PodGroupPolicy",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
						Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
						PodGroupPolicy: &workloadsv1alpha1.PodGroupPolicy{
							PodGroupPolicySource: workloadsv1alpha1.PodGroupPolicySource{
								KubeScheduling: &workloadsv1alpha1.KubeSchedulingPodGroupPolicySource{
									ScheduleTimeoutSeconds: ptr.To(int32(120)),
								},
							},
						},
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
					PodGroupPolicy: &workloadsv1alpha1.PodGroupPolicy{
						PodGroupPolicySource: workloadsv1alpha1.PodGroupPolicySource{
							KubeScheduling: &workloadsv1alpha1.KubeSchedulingPodGroupPolicySource{
								ScheduleTimeoutSeconds: ptr.To(int32(60)),
							},
						},
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG needs update - PodGroupPolicy added",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
						Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
						PodGroupPolicy: &workloadsv1alpha1.PodGroupPolicy{
							PodGroupPolicySource: workloadsv1alpha1.PodGroupPolicySource{
								KubeScheduling: &workloadsv1alpha1.KubeSchedulingPodGroupPolicySource{
									ScheduleTimeoutSeconds: ptr.To(int32(60)),
								},
							},
						},
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles:          []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
					PodGroupPolicy: nil,
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG no update needed - same CoordinationRequirements and PodGroupPolicy",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
						Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
						CoordinationRequirements: []workloadsv1alpha1.Coordination{
							{
								Name:  "coordination",
								Roles: []string{"role-1"},
							},
						},
						PodGroupPolicy: &workloadsv1alpha1.PodGroupPolicy{
							PodGroupPolicySource: workloadsv1alpha1.PodGroupPolicySource{
								KubeScheduling: &workloadsv1alpha1.KubeSchedulingPodGroupPolicySource{
									ScheduleTimeoutSeconds: ptr.To(int32(60)),
								},
							},
						},
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Name:  "coordination",
							Roles: []string{"role-1"},
						},
					},
					PodGroupPolicy: &workloadsv1alpha1.PodGroupPolicy{
						PodGroupPolicySource: workloadsv1alpha1.PodGroupPolicySource{
							KubeScheduling: &workloadsv1alpha1.KubeSchedulingPodGroupPolicySource{
								ScheduleTimeoutSeconds: ptr.To(int32(60)),
							},
						},
					},
				},
			},
			expectedUpdate: false,
		},
		{
			name: "RBG no update needed",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
						Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{{Name: "role-1"}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &RoleBasedGroupSetReconciler{}
				result := r.needsUpdate(tt.rbgset, tt.rbg)
				assert.Equal(t, tt.expectedUpdate, result)
			},
		)
	}
}

// TestRoleBasedGroupSetReconciler_needsAnnotationUpdate tests the needsAnnotationUpdate method.
func TestRoleBasedGroupSetReconciler_needsAnnotationUpdate(t *testing.T) {
	tests := []struct {
		name           string
		rbgset         *workloadsv1alpha1.RoleBasedGroupSet
		rbg            *workloadsv1alpha1.RoleBasedGroup
		expectedUpdate bool
	}{
		{
			name:   "RBG has annotation, RBGSet doesn't",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						workloadsv1alpha1.ExclusiveKeyAnnotationKey: "topology.kubernetes.io/zone",
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBGSet has annotation, RBG doesn't",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						workloadsv1alpha1.ExclusiveKeyAnnotationKey: "test-key",
					},
				},
			},
			rbg:            &workloadsv1alpha1.RoleBasedGroup{},
			expectedUpdate: true,
		},
		{
			name: "Different annotation values",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						workloadsv1alpha1.ExclusiveKeyAnnotationKey: "new-value",
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						workloadsv1alpha1.ExclusiveKeyAnnotationKey: "old-value",
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "Same annotation values",
			rbgset: &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						workloadsv1alpha1.ExclusiveKeyAnnotationKey: "same-value",
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						workloadsv1alpha1.ExclusiveKeyAnnotationKey: "same-value",
					},
				},
			},
			expectedUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &RoleBasedGroupSetReconciler{}
				result := r.needsAnnotationUpdate(tt.rbgset, tt.rbg)
				assert.Equal(t, tt.expectedUpdate, result)
			},
		)
	}
}

// TestRoleBasedGroupSetReconciler_Reconcile_OptimizedOrder tests the optimized operation order.
// This test verifies that when both role changes and replica reduction occur,
// the controller deletes excess RBGs first, then updates remaining ones.
func TestRoleBasedGroupSetReconciler_Reconcile_OptimizedOrder(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)

	// Setup: 4 RBGs with old role, scale down to 2 with new role
	initialRBGSet := &workloadsv1alpha1.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbgset",
			Namespace: "default",
			Annotations: map[string]string{
				workloadsv1alpha1.ExclusiveKeyAnnotationKey: "new-exclusive",
			},
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(2)), // Reduced from 4 to 2
			Template: workloadsv1alpha1.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha1.RoleSpec{{Name: "new-role"}},
			},
		},
	}

	// Create 4 existing RBGs with old role
	existingRBGs := []runtime.Object{initialRBGSet}
	for i := 0; i < 4; i++ {
		rbg := &workloadsv1alpha1.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("test-rbgset-%d", i),
				Namespace: "default",
				Labels: map[string]string{
					workloadsv1alpha1.SetRBGSetNameLabelKey: "test-rbgset",
					workloadsv1alpha1.SetRBGIndexLabelKey:   fmt.Sprintf("%d", i),
				},
				Annotations: map[string]string{
					workloadsv1alpha1.ExclusiveKeyAnnotationKey: "old-exclusive",
				},
			},
			Spec: workloadsv1alpha1.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha1.RoleSpec{{Name: "old-role"}},
			},
		}
		existingRBGs = append(existingRBGs, rbg)
	}

	r := &RoleBasedGroupSetReconciler{
		client: fake.NewClientBuilder().WithScheme(scheme).
			WithRuntimeObjects(existingRBGs...).
			WithStatusSubresource(&workloadsv1alpha1.RoleBasedGroupSet{}).Build(),
		scheme: scheme,
	}

	// Run reconcile
	_, err := r.Reconcile(
		context.TODO(), ctrl.Request{
			NamespacedName: types.NamespacedName{
				Namespace: "default",
				Name:      "test-rbgset",
			},
		},
	)
	assert.NoError(t, err)

	// Verify results: should have exactly 2 RBGs
	var rbgList workloadsv1alpha1.RoleBasedGroupList
	err = r.client.List(
		context.Background(), &rbgList,
		client.InNamespace("default"),
		client.MatchingLabels{workloadsv1alpha1.SetRBGSetNameLabelKey: "test-rbgset"},
	)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(rbgList.Items))

	// Verify remaining RBGs have updated roles and annotations
	for _, rbg := range rbgList.Items {
		assert.Equal(t, "new-role", rbg.Spec.Roles[0].Name)
		assert.Equal(t, "new-exclusive", rbg.Annotations[workloadsv1alpha1.ExclusiveKeyAnnotationKey])
		index := rbg.Labels[workloadsv1alpha1.SetRBGIndexLabelKey]
		assert.True(t, index == "0" || index == "1")
	}
}

// TestRoleBasedGroupSetReconciler_Reconcile_StatusUpdate tests the status update logic within the Reconcile loop.
func TestRoleBasedGroupSetReconciler_Reconcile_StatusUpdate(t *testing.T) {
	// Setup test scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)

	tests := []struct {
		name                string
		initialRBGSet       *workloadsv1alpha1.RoleBasedGroupSet
		rbgList             []workloadsv1alpha1.RoleBasedGroup
		expectReady         bool
		expectReplicas      int32
		expectReadyReplicas int32
		expectedReason      string
		expectedMessagePart string
	}{
		{
			name: "All RBGs ready, replicas match spec",
			initialRBGSet: &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       workloadsv1alpha1.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(2))},
			},
			rbgList: []workloadsv1alpha1.RoleBasedGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rbg-0",
						Namespace: "default",
						Labels: map[string]string{
							workloadsv1alpha1.SetRBGSetNameLabelKey: "test-rbgset",
							workloadsv1alpha1.SetRBGIndexLabelKey:   "0",
						},
					},
					Status: workloadsv1alpha1.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha1.RoleBasedGroupReady),
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rbg-1",
						Namespace: "default",
						Labels: map[string]string{
							workloadsv1alpha1.SetRBGSetNameLabelKey: "test-rbgset",
							workloadsv1alpha1.SetRBGIndexLabelKey:   "1",
						},
					},
					Status: workloadsv1alpha1.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha1.RoleBasedGroupReady),
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			},
			expectReady:         true,
			expectReplicas:      2,
			expectReadyReplicas: 2,
			expectedReason:      "AllReplicasReady",
			expectedMessagePart: "All RoleBasedGroup replicas are ready.",
		},
		{
			name: "Partial RBGs ready",
			initialRBGSet: &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       workloadsv1alpha1.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(2))},
			},
			rbgList: []workloadsv1alpha1.RoleBasedGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rbg-0",
						Namespace: "default",
						Labels: map[string]string{
							workloadsv1alpha1.SetRBGSetNameLabelKey: "test-rbgset",
							workloadsv1alpha1.SetRBGIndexLabelKey:   "0",
						},
					},
					Status: workloadsv1alpha1.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha1.RoleBasedGroupReady),
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rbg-1",
						Namespace: "default",
						Labels: map[string]string{
							workloadsv1alpha1.SetRBGSetNameLabelKey: "test-rbgset",
							workloadsv1alpha1.SetRBGIndexLabelKey:   "1",
						},
					},
					Status: workloadsv1alpha1.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha1.RoleBasedGroupReady),
								Status: metav1.ConditionFalse,
							},
						},
					},
				},
			},
			expectReady:         false,
			expectReplicas:      2,
			expectReadyReplicas: 1,
			expectedReason:      "ReplicasNotReady",
			expectedMessagePart: "Waiting for replicas to be ready (1/2)",
		},
		{
			name: "No RBGs ready",
			initialRBGSet: &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       workloadsv1alpha1.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(1))},
			},
			rbgList: []workloadsv1alpha1.RoleBasedGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rbg-0",
						Namespace: "default",
						Labels: map[string]string{
							workloadsv1alpha1.SetRBGSetNameLabelKey: "test-rbgset",
							workloadsv1alpha1.SetRBGIndexLabelKey:   "0",
						},
					},
					Status: workloadsv1alpha1.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha1.RoleBasedGroupReady),
								Status: metav1.ConditionFalse,
							},
						},
					},
				},
			},
			expectReady:         false,
			expectReplicas:      1,
			expectReadyReplicas: 0,
			expectedReason:      "ReplicasNotReady",
			expectedMessagePart: "Waiting for replicas to be ready (0/1)",
		},
		{
			name: "Empty RBG list with zero replicas spec",
			initialRBGSet: &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       workloadsv1alpha1.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(0))},
			},
			rbgList:             []workloadsv1alpha1.RoleBasedGroup{},
			expectReady:         true, // 0 ready >= 0 desired, so it's considered ready.
			expectReplicas:      0,
			expectReadyReplicas: 0,
			expectedReason:      "AllReplicasReady",
			expectedMessagePart: "All RoleBasedGroup replicas are ready.",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Prepare all objects for the fake client.
				objs := []runtime.Object{tt.initialRBGSet}
				for i := range tt.rbgList {
					objs = append(objs, &tt.rbgList[i])
				}

				// Configure the fake client to provide a status subresource for the CRD.
				r := &RoleBasedGroupSetReconciler{
					client: fake.NewClientBuilder().WithScheme(scheme).
						WithRuntimeObjects(objs...).
						WithStatusSubresource(&workloadsv1alpha1.RoleBasedGroupSet{}).Build(),
					scheme: scheme,
				}

				// Run the full reconcile loop. Since replicas in spec match the number of existing
				// objects, no scaling will occur, and it will proceed to status update.
				_, err := r.Reconcile(
					context.TODO(), ctrl.Request{
						NamespacedName: types.NamespacedName{
							Namespace: tt.initialRBGSet.Namespace,
							Name:      tt.initialRBGSet.Name,
						},
					},
				)
				// We expect a RequeueAfter, so we don't assert a nil error, just no *real* error.
				assert.True(t, err == nil || err.Error() == "", "Reconcile returned an unexpected error: %v", err)

				// Fetch the updated RBGSet to check its status.
				updatedRBGSet := &workloadsv1alpha1.RoleBasedGroupSet{}
				err = r.client.Get(
					context.Background(), types.NamespacedName{
						Name:      tt.initialRBGSet.Name,
						Namespace: tt.initialRBGSet.Namespace,
					}, updatedRBGSet,
				)
				assert.NoError(t, err)

				// Verify status fields
				assert.Equal(t, tt.expectReplicas, updatedRBGSet.Status.Replicas)
				assert.Equal(t, tt.expectReadyReplicas, updatedRBGSet.Status.ReadyReplicas)

				// Verify condition
				assert.NotEmpty(t, updatedRBGSet.Status.Conditions, "Status conditions should not be empty")
				condition := updatedRBGSet.Status.Conditions[0]
				assert.Equal(t, string(workloadsv1alpha1.RoleBasedGroupSetReady), condition.Type)
				assert.Equal(t, tt.expectedReason, condition.Reason)
				assert.Contains(t, condition.Message, tt.expectedMessagePart)

				if tt.expectReady {
					assert.Equal(t, metav1.ConditionTrue, condition.Status)
				} else {
					assert.Equal(t, metav1.ConditionFalse, condition.Status)
				}
			},
		)
	}
}

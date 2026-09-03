/*
Copyright 2025 The RBG Authors.

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
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// TestRoleBasedGroupSetReconciler_scaleUp tests the scaleUp function.
func TestRoleBasedGroupSetReconciler_scaleUp(t *testing.T) {
	// Setup test scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)

	// Create a RoleBasedGroupSet for testing
	rbgset := &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbgset",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{Name: "role-1"},
						{Name: "role-2"},
					},
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
				var rbgsToCreate []*workloadsv1alpha2.RoleBasedGroup
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
				var rbglist workloadsv1alpha2.RoleBasedGroupList
				opts := []client.ListOption{
					client.InNamespace(rbgset.Namespace),
					client.MatchingLabels{constants.GroupSetNameLabelKey: rbgset.Name},
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
	_ = workloadsv1alpha2.AddToScheme(scheme)

	rbgBase := []workloadsv1alpha2.RoleBasedGroup{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rbg-0",
				Namespace: "default",
				Labels: map[string]string{
					constants.GroupSetNameLabelKey:  "rbgs-test",
					constants.GroupSetIndexLabelKey: "0",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rbg-1",
				Namespace: "default",
				Labels: map[string]string{
					constants.GroupSetNameLabelKey:  "rbgs-test",
					constants.GroupSetIndexLabelKey: "1",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rbg-2",
				Namespace: "default",
				Labels: map[string]string{
					constants.GroupSetNameLabelKey:  "rbgs-test",
					constants.GroupSetIndexLabelKey: "2",
				},
			},
		},
	}

	tests := []struct {
		name                string
		initialRBGs         []workloadsv1alpha2.RoleBasedGroup
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
			initialRBGs:         []workloadsv1alpha2.RoleBasedGroup{},
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
				var rbgsToDelete []*workloadsv1alpha2.RoleBasedGroup
				for _, index := range tt.rbgsToDeleteIndices {
					// We need to pass pointers to copies to avoid issues with loop variables.
					rbgCopy := tt.initialRBGs[index].DeepCopy()
					rbgsToDelete = append(rbgsToDelete, rbgCopy)
				}

				err := r.scaleDown(context.Background(), rbgsToDelete)
				assert.NoError(t, err)

				// Verify the result by listing the remaining objects.
				var leftRbgList workloadsv1alpha2.RoleBasedGroupList
				opts := []client.ListOption{
					client.InNamespace("default"),
					client.MatchingLabels{constants.GroupSetNameLabelKey: "rbgs-test"},
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

// TestRolesEqual_DoesNotMutateInputs proves the normalized comparison keeps the
// caller's RoleSpecs untouched. rolesEqual previously normalized in place through
// the shared *RolloutStrategy/*RollingUpdate pointers, which reached into the
// informer-cache objects being compared; the deep copy added here must prevent that.
func TestRolesEqual_DoesNotMutateInputs(t *testing.T) {
	parent := []workloadsv1alpha2.RoleSpec{
		{
			Name: "role-1",
			RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
				RollingUpdate: &workloadsv1alpha2.RollingUpdate{
					Type: workloadsv1alpha2.LegacyRecreateUpdateStrategyType,
				},
			},
		},
	}
	child := []workloadsv1alpha2.RoleSpec{
		{
			Name: "role-1",
			RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
				RollingUpdate: &workloadsv1alpha2.RollingUpdate{
					Type: workloadsv1alpha2.RecreatePodUpdateStrategyType,
				},
			},
		},
	}

	parentBefore := parent[0].RolloutStrategy.RollingUpdate.Type
	childBefore := child[0].RolloutStrategy.RollingUpdate.Type

	r := &RoleBasedGroupSetReconciler{}
	if !r.rolesEqual(parent, child) {
		t.Fatal("legacy vs normalized spellings should compare equal")
	}

	if got := parent[0].RolloutStrategy.RollingUpdate.Type; got != parentBefore {
		t.Errorf("rolesEqual mutated the parent input: got %q, want %q", got, parentBefore)
	}
	if got := child[0].RolloutStrategy.RollingUpdate.Type; got != childBefore {
		t.Errorf("rolesEqual mutated the child input: got %q, want %q", got, childBefore)
	}
}

// TestRoleBasedGroupSetReconciler_needsUpdate tests the needsUpdate method.
func TestRoleBasedGroupSetReconciler_needsUpdate(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)

	tests := []struct {
		name           string
		rbgset         *workloadsv1alpha2.RoleBasedGroupSet
		rbg            *workloadsv1alpha2.RoleBasedGroup
		expectedUpdate bool
	}{
		{
			name: "RBG needs update - different roles",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbgset",
					Namespace: "default",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{
								{Name: "new-role"},
							},
						},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbgset",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{Name: "old-role"},
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG needs update - template annotation added",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Annotations: map[string]string{
							"app.io/env": "prod",
						},
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
						},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG needs update - template annotation removed",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
						},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{"app.io/env": "prod"},
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG needs update - template label added",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Labels: map[string]string{"tier": "backend"},
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
						},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.GroupSetNameLabelKey:  "test-rbgset",
						constants.GroupSetIndexLabelKey: "0",
					},
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG needs update - template label removed",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
						},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.GroupSetNameLabelKey:  "test-rbgset",
						constants.GroupSetIndexLabelKey: "0",
						"tier":                          "backend", // extra label not in template
					},
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG no update needed - roles and metadata all match",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Labels:      map[string]string{"tier": "backend"},
						Annotations: map[string]string{"app.io/env": "prod"},
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
						},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.GroupSetNameLabelKey:  "test-rbgset",
						constants.GroupSetIndexLabelKey: "0",
						"tier":                          "backend",
					},
					Annotations: map[string]string{"app.io/env": "prod"},
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
				},
			},
			expectedUpdate: false,
		},
		{
			name: "RBG no update needed - empty template metadata",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
						},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
				},
			},
			expectedUpdate: false,
		},

		{
			name: "RBG needs update - strategy type really differs after normalization",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{
								{
									Name: "role-1",
									RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
										RollingUpdate: &workloadsv1alpha2.RollingUpdate{
											Type: workloadsv1alpha2.RecreatePodUpdateStrategyType,
										},
									},
								},
							},
						},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name: "role-1",
							RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
								RollingUpdate: &workloadsv1alpha2.RollingUpdate{
									Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
								},
							},
						},
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "RBG no update needed - legacy strategy spelling differs only",
			// A pre-webhook RoleBasedGroupSet template can keep the v1alpha1 "Recreate"
			// spelling while its children carry the webhook-normalized "RecreatePod".
			// The normalized comparison must treat that spelling-only delta as equal, or
			// the controller would re-issue child updates forever.
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{
								{
									Name: "role-1",
									RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
										RollingUpdate: &workloadsv1alpha2.RollingUpdate{
											Type: workloadsv1alpha2.LegacyRecreateUpdateStrategyType,
										},
									},
								},
							},
						},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name: "role-1",
							RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
								RollingUpdate: &workloadsv1alpha2.RollingUpdate{
									Type: workloadsv1alpha2.RecreatePodUpdateStrategyType,
								},
							},
						},
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
				result := r.needsUpdate(tt.rbgset, tt.rbg)
				assert.Equal(t, tt.expectedUpdate, result)
			},
		)
	}
}

// TestRoleBasedGroupSetReconciler_needsTemplateAnnotationUpdate tests the needsTemplateAnnotationUpdate method.
func TestRoleBasedGroupSetReconciler_needsTemplateAnnotationUpdate(t *testing.T) {
	tests := []struct {
		name           string
		rbgset         *workloadsv1alpha2.RoleBasedGroupSet
		rbg            *workloadsv1alpha2.RoleBasedGroup
		expectedUpdate bool
	}{
		{
			name: "RBG has annotation, template doesn't",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{"app.io/env": "prod"},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "Template has annotation, RBG doesn't",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Annotations: map[string]string{"app.io/env": "prod"},
					},
				},
			},
			rbg:            &workloadsv1alpha2.RoleBasedGroup{},
			expectedUpdate: true,
		},
		{
			name: "Different annotation values",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Annotations: map[string]string{"app.io/env": "prod"},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{"app.io/env": "staging"},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "Same annotation values",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Annotations: map[string]string{"app.io/env": "prod"},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{"app.io/env": "prod"},
				},
			},
			expectedUpdate: false,
		},
		{
			name: "Both empty",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{},
				},
			},
			rbg:            &workloadsv1alpha2.RoleBasedGroup{},
			expectedUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &RoleBasedGroupSetReconciler{}
				result := r.needsTemplateAnnotationUpdate(tt.rbgset, tt.rbg)
				assert.Equal(t, tt.expectedUpdate, result)
			},
		)
	}
}

// TestRoleBasedGroupSetReconciler_needsTemplateLabelUpdate tests the needsTemplateLabelUpdate method.
func TestRoleBasedGroupSetReconciler_needsTemplateLabelUpdate(t *testing.T) {
	tests := []struct {
		name           string
		rbgset         *workloadsv1alpha2.RoleBasedGroupSet
		rbg            *workloadsv1alpha2.RoleBasedGroup
		expectedUpdate bool
	}{
		{
			name: "RBG has extra non-system label not in template",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.GroupSetNameLabelKey:  "test-rbgset",
						constants.GroupSetIndexLabelKey: "0",
						"tier":                          "backend",
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "Template has label, RBG doesn't",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Labels: map[string]string{"tier": "backend"},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.GroupSetNameLabelKey:  "test-rbgset",
						constants.GroupSetIndexLabelKey: "0",
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "Different label values",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Labels: map[string]string{"tier": "frontend"},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.GroupSetNameLabelKey:  "test-rbgset",
						constants.GroupSetIndexLabelKey: "0",
						"tier":                          "backend",
					},
				},
			},
			expectedUpdate: true,
		},
		{
			name: "Labels match, system labels ignored in comparison",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Labels: map[string]string{"tier": "backend"},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.GroupSetNameLabelKey:  "test-rbgset",
						constants.GroupSetIndexLabelKey: "0",
						"tier":                          "backend",
					},
				},
			},
			expectedUpdate: false,
		},
		{
			name: "System labels only, no template labels",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.GroupSetNameLabelKey:  "test-rbgset",
						constants.GroupSetIndexLabelKey: "0",
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
				result := r.needsTemplateLabelUpdate(tt.rbgset, tt.rbg)
				assert.Equal(t, tt.expectedUpdate, result)
			},
		)
	}
}

// TestNewRBGForSet_MetadataPropagation tests that newRBGForSet correctly propagates
// groupTemplate.labels and groupTemplate.annotations to the child RBG.
func TestNewRBGForSet_MetadataPropagation(t *testing.T) {
	tests := []struct {
		name                string
		rbgset              *workloadsv1alpha2.RoleBasedGroupSet
		index               int
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name: "Template labels and annotations are propagated",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Labels:      map[string]string{"tier": "backend", "env": "prod"},
						Annotations: map[string]string{"app.io/config": "v1"},
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
						},
					},
				},
			},
			index: 2,
			expectedLabels: map[string]string{
				constants.GroupSetNameLabelKey:  "test-rbgset",
				constants.GroupSetIndexLabelKey: "2",
				"tier":                          "backend",
				"env":                           "prod",
			},
			expectedAnnotations: map[string]string{"app.io/config": "v1"},
		},
		{
			name: "Empty template metadata produces only system labels and no annotations",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
						},
					},
				},
			},
			index: 0,
			expectedLabels: map[string]string{
				constants.GroupSetNameLabelKey:  "test-rbgset",
				constants.GroupSetIndexLabelKey: "0",
			},
			expectedAnnotations: nil,
		},
		{
			name: "Template label does not override system-managed labels",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						// Attempting to override a system label via template is ignored
						// because system labels are written after template labels.
						Labels: map[string]string{
							constants.GroupSetIndexLabelKey: "99",
							"tier":                          "backend",
						},
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{{Name: "role-1"}},
						},
					},
				},
			},
			index: 1,
			expectedLabels: map[string]string{
				constants.GroupSetNameLabelKey:  "test-rbgset",
				constants.GroupSetIndexLabelKey: "1", // system label wins, index is 1 not 99
				"tier":                          "backend",
			},
			expectedAnnotations: nil,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				rbg := newRBGForSet(tt.rbgset, tt.index)
				assert.Equal(t, tt.expectedLabels, rbg.Labels)
				assert.Equal(t, tt.expectedAnnotations, rbg.Annotations)
				assert.Equal(
					t,
					fmt.Sprintf("%s-%d", tt.rbgset.Name, tt.index),
					rbg.Name,
				)
				assert.Equal(t, tt.rbgset.Namespace, rbg.Namespace)
			},
		)
	}
}

// TestNewRBGForSet_NormalizesLegacyStrategyType proves a child RBG built from a
// legacy template never carries a value the v1alpha2 CRD enum rejects, even when
// webhooks are disabled (and therefore the RBGS defaulter never ran on the stored
// template).
func TestNewRBGForSet_NormalizesLegacyStrategyType(t *testing.T) {
	rbgset := &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name: "role-1",
							RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
								RollingUpdate: &workloadsv1alpha2.RollingUpdate{
									Type: workloadsv1alpha2.LegacyRecreateUpdateStrategyType,
								},
							},
						},
						{
							Name: "role-2",
							RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
								RollingUpdate: &workloadsv1alpha2.RollingUpdate{
									Type: workloadsv1alpha2.UpdateStrategyType(""),
								},
							},
						},
						{
							Name: "role-3",
							RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
								RollingUpdate: &workloadsv1alpha2.RollingUpdate{
									Type: workloadsv1alpha2.InPlaceOnlyUpdateStrategyType,
								},
							},
						},
					},
				},
			},
		},
	}

	rbg := newRBGForSet(rbgset, 0)
	got := rbg.Spec.Roles
	if len(got) != 3 {
		t.Fatalf("expected 3 roles, got %d", len(got))
	}
	if got[0].RolloutStrategy.RollingUpdate.Type != workloadsv1alpha2.RecreatePodUpdateStrategyType {
		t.Errorf("legacy Recreate not normalized: got %q", got[0].RolloutStrategy.RollingUpdate.Type)
	}
	if got[1].RolloutStrategy.RollingUpdate.Type != workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType {
		t.Errorf("empty type not defaulted: got %q", got[1].RolloutStrategy.RollingUpdate.Type)
	}
	if got[2].RolloutStrategy.RollingUpdate.Type != workloadsv1alpha2.InPlaceOnlyUpdateStrategyType {
		t.Errorf("valid type was changed: got %q", got[2].RolloutStrategy.RollingUpdate.Type)
	}

	// Building the child must not mutate the parent template.
	if parentType := rbgset.Spec.GroupTemplate.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type; parentType != workloadsv1alpha2.LegacyRecreateUpdateStrategyType {
		t.Errorf("parent template was mutated: got %q", parentType)
	}
	if parentType := rbgset.Spec.GroupTemplate.Spec.Roles[1].RolloutStrategy.RollingUpdate.Type; parentType != workloadsv1alpha2.UpdateStrategyType("") {
		t.Errorf("parent template was mutated: got %q", parentType)
	}
}

// TestRoleBasedGroupSetReconciler_updateExistingRBGs_NormalizesLegacyStrategy
// drives the update path (not just newRBGForSet) with a legacy template and proves
// the child is stored with the normalized strategy type, so a pre-webhook template
// cannot poison an updated child even when webhooks are disabled.
func TestRoleBasedGroupSetReconciler_updateExistingRBGs_NormalizesLegacyStrategy(t *testing.T) {
	s := runtime.NewScheme()
	if err := workloadsv1alpha2.AddToScheme(s); err != nil {
		t.Fatal(err)
	}

	const (
		ns        = "default"
		setName   = "test-rbgset"
		childName = "test-rbgset-0"
	)
	rbgset := &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: setName, Namespace: ns},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name: "role-1",
							RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
								RollingUpdate: &workloadsv1alpha2.RollingUpdate{
									Type: workloadsv1alpha2.LegacyRecreateUpdateStrategyType,
								},
							},
						},
					},
				},
			},
		},
	}
	child := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      childName,
			Namespace: ns,
			Labels: map[string]string{
				constants.GroupSetNameLabelKey:  setName,
				constants.GroupSetIndexLabelKey: "0",
			},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{
					Name: "role-1",
					RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
						RollingUpdate: &workloadsv1alpha2.RollingUpdate{
							Type: workloadsv1alpha2.LegacyRecreateUpdateStrategyType,
						},
					},
				},
			},
		},
	}

	r := &RoleBasedGroupSetReconciler{
		client: fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(child).Build(),
	}
	if err := r.updateExistingRBGs(context.Background(), rbgset, []*workloadsv1alpha2.RoleBasedGroup{child}); err != nil {
		t.Fatalf("updateExistingRBGs: %v", err)
	}

	stored := &workloadsv1alpha2.RoleBasedGroup{}
	if err := r.client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: childName}, stored); err != nil {
		t.Fatalf("get child: %v", err)
	}
	if got := stored.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type; got != workloadsv1alpha2.RecreatePodUpdateStrategyType {
		t.Errorf("child stored with legacy type: got %q, want %q", got, workloadsv1alpha2.RecreatePodUpdateStrategyType)
	}
	// The parent template must be left untouched.
	if got := rbgset.Spec.GroupTemplate.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type; got != workloadsv1alpha2.LegacyRecreateUpdateStrategyType {
		t.Errorf("parent template was mutated: got %q", got)
	}
}

// TestSyncRBGMetadata tests the syncRBGMetadata method.
func TestSyncRBGMetadata(t *testing.T) {
	tests := []struct {
		name                string
		rbgset              *workloadsv1alpha2.RoleBasedGroupSet
		rbg                 *workloadsv1alpha2.RoleBasedGroup
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name: "Syncs template labels and annotations, preserves system labels",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Labels:      map[string]string{"tier": "backend"},
						Annotations: map[string]string{"app.io/env": "prod"},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.GroupSetNameLabelKey:  "test-rbgset",
						constants.GroupSetIndexLabelKey: "1",
						"old-label":                     "old-value",
					},
					Annotations: map[string]string{"old-annotation": "old-value"},
				},
			},
			expectedLabels: map[string]string{
				constants.GroupSetNameLabelKey:  "test-rbgset",
				constants.GroupSetIndexLabelKey: "1",
				"tier":                          "backend",
			},
			expectedAnnotations: map[string]string{"app.io/env": "prod"},
		},
		{
			name: "Clears annotations when template has none",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.GroupSetNameLabelKey:  "test-rbgset",
						constants.GroupSetIndexLabelKey: "0",
					},
					Annotations: map[string]string{"stale-annotation": "value"},
				},
			},
			expectedLabels: map[string]string{
				constants.GroupSetNameLabelKey:  "test-rbgset",
				constants.GroupSetIndexLabelKey: "0",
			},
			expectedAnnotations: nil,
		},
		{
			name: "Removes extra non-system labels not in template",
			rbgset: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset"},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Labels: map[string]string{"env": "prod"},
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.GroupSetNameLabelKey:  "test-rbgset",
						constants.GroupSetIndexLabelKey: "0",
						"stale-label":                   "old",
					},
				},
			},
			expectedLabels: map[string]string{
				constants.GroupSetNameLabelKey:  "test-rbgset",
				constants.GroupSetIndexLabelKey: "0",
				"env":                           "prod",
			},
			expectedAnnotations: nil,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &RoleBasedGroupSetReconciler{}
				r.syncRBGMetadata(tt.rbgset, tt.rbg)
				assert.Equal(t, tt.expectedLabels, tt.rbg.Labels)
				assert.Equal(t, tt.expectedAnnotations, tt.rbg.Annotations)
			},
		)
	}
}

// TestRoleBasedGroupSetReconciler_Reconcile_OptimizedOrder tests the optimized operation order.
// This test verifies that when both role changes and replica reduction occur,
// the controller deletes excess RBGs first, then updates remaining ones.
func TestRoleBasedGroupSetReconciler_Reconcile_OptimizedOrder(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)

	// Setup: 4 RBGs with old role, scale down to 2 with new role and updated metadata
	initialRBGSet := &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbgset",
			Namespace: "default",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(2)), // Reduced from 4 to 2
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
				Labels:      map[string]string{"env": "prod"},
				Annotations: map[string]string{constants.GroupExclusiveTopologyKey: "new-exclusive"},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{Name: "new-role"}},
				},
			},
		},
	}

	// Create 4 existing RBGs with old role and stale metadata
	existingRBGs := []runtime.Object{initialRBGSet}
	for i := 0; i < 4; i++ {
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("test-rbgset-%d", i),
				Namespace: "default",
				Labels: map[string]string{
					constants.GroupSetNameLabelKey:  "test-rbgset",
					constants.GroupSetIndexLabelKey: fmt.Sprintf("%d", i),
				},
				Annotations: map[string]string{
					constants.GroupExclusiveTopologyKey: "old-exclusive",
				},
			},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{Name: "old-role"}},
			},
		}
		existingRBGs = append(existingRBGs, rbg)
	}

	r := &RoleBasedGroupSetReconciler{
		client: fake.NewClientBuilder().WithScheme(scheme).
			WithRuntimeObjects(existingRBGs...).
			WithStatusSubresource(&workloadsv1alpha2.RoleBasedGroupSet{}).Build(),
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
	var rbgList workloadsv1alpha2.RoleBasedGroupList
	err = r.client.List(
		context.Background(), &rbgList,
		client.InNamespace("default"),
		client.MatchingLabels{constants.GroupSetNameLabelKey: "test-rbgset"},
	)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(rbgList.Items))

	// Verify remaining RBGs have updated roles, labels, and annotations from groupTemplate
	for _, rbg := range rbgList.Items {
		assert.Equal(t, "new-role", rbg.Spec.Roles[0].Name)
		// Template annotation should be propagated
		assert.Equal(t, "new-exclusive", rbg.Annotations[constants.GroupExclusiveTopologyKey])
		// Template label should be propagated
		assert.Equal(t, "prod", rbg.Labels["env"])
		// System labels must be preserved
		assert.Equal(t, "test-rbgset", rbg.Labels[constants.GroupSetNameLabelKey])
		index := rbg.Labels[constants.GroupSetIndexLabelKey]
		assert.True(t, index == "0" || index == "1")
	}
}

// TestRoleBasedGroupSetReconciler_Reconcile_StatusUpdate tests the status update logic within the Reconcile loop.
func TestRoleBasedGroupSetReconciler_Reconcile_StatusUpdate(t *testing.T) {
	// Setup test scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)

	tests := []struct {
		name                string
		initialRBGSet       *workloadsv1alpha2.RoleBasedGroupSet
		rbgList             []workloadsv1alpha2.RoleBasedGroup
		expectReady         bool
		expectReplicas      int32
		expectReadyReplicas int32
		expectedReason      string
		expectedMessagePart string
	}{
		{
			name: "All RBGs ready, replicas match spec",
			initialRBGSet: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       workloadsv1alpha2.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(2))},
			},
			rbgList: []workloadsv1alpha2.RoleBasedGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rbg-0",
						Namespace: "default",
						Labels: map[string]string{
							constants.GroupSetNameLabelKey:  "test-rbgset",
							constants.GroupSetIndexLabelKey: "0",
						},
					},
					Status: workloadsv1alpha2.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha2.RoleBasedGroupReady),
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
							constants.GroupSetNameLabelKey:  "test-rbgset",
							constants.GroupSetIndexLabelKey: "1",
						},
					},
					Status: workloadsv1alpha2.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha2.RoleBasedGroupReady),
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
			initialRBGSet: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       workloadsv1alpha2.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(2))},
			},
			rbgList: []workloadsv1alpha2.RoleBasedGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rbg-0",
						Namespace: "default",
						Labels: map[string]string{
							constants.GroupSetNameLabelKey:  "test-rbgset",
							constants.GroupSetIndexLabelKey: "0",
						},
					},
					Status: workloadsv1alpha2.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha2.RoleBasedGroupReady),
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
							constants.GroupSetNameLabelKey:  "test-rbgset",
							constants.GroupSetIndexLabelKey: "1",
						},
					},
					Status: workloadsv1alpha2.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha2.RoleBasedGroupReady),
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
			initialRBGSet: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       workloadsv1alpha2.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(1))},
			},
			rbgList: []workloadsv1alpha2.RoleBasedGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rbg-0",
						Namespace: "default",
						Labels: map[string]string{
							constants.GroupSetNameLabelKey:  "test-rbgset",
							constants.GroupSetIndexLabelKey: "0",
						},
					},
					Status: workloadsv1alpha2.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha2.RoleBasedGroupReady),
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
			initialRBGSet: &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       workloadsv1alpha2.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(0))},
			},
			rbgList:             []workloadsv1alpha2.RoleBasedGroup{},
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
						WithStatusSubresource(&workloadsv1alpha2.RoleBasedGroupSet{}).Build(),
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
				updatedRBGSet := &workloadsv1alpha2.RoleBasedGroupSet{}
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
				assert.Equal(t, string(workloadsv1alpha2.RoleBasedGroupSetReady), condition.Type)
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

// --- Rolling update tests ---

func rollingTestSet(
	name string, replicas int32, roles []workloadsv1alpha2.RoleSpec, ru *workloadsv1alpha2.GroupSetRolloutStrategy,
) *workloadsv1alpha2.RoleBasedGroupSet {
	return &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			Replicas: ptr.To(replicas),
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: roles},
			},
			RolloutStrategy: ru,
		},
	}
}

func rollingTestChild(
	setName string, ordinal int, roles []workloadsv1alpha2.RoleSpec, ready bool,
) *workloadsv1alpha2.RoleBasedGroup {
	readyStatus := metav1.ConditionFalse
	if ready {
		readyStatus = metav1.ConditionTrue
	}
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", setName, ordinal),
			Namespace: "default",
			Labels: map[string]string{
				constants.GroupSetNameLabelKey:  setName,
				constants.GroupSetIndexLabelKey: fmt.Sprintf("%d", ordinal),
			},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: roles},
		Status: workloadsv1alpha2.RoleBasedGroupStatus{
			Conditions: []metav1.Condition{
				{
					Type:               string(workloadsv1alpha2.RoleBasedGroupReady),
					Status:             readyStatus,
					Reason:             "Test",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
}

func newRollingTestReconciler(scheme *runtime.Scheme, objs ...runtime.Object) *RoleBasedGroupSetReconciler {
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&workloadsv1alpha2.RoleBasedGroupSet{}).Build()
	return &RoleBasedGroupSetReconciler{
		client: c,
		// Reconcile never reads through apiReader; only CheckCrdExists does, at startup.
		scheme:   scheme,
		recorder: record.NewFakeRecorder(100),
	}
}

func reconcileSet(t *testing.T, r *RoleBasedGroupSetReconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(
		context.TODO(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}},
	)
	assert.NoError(t, err)
}

func listChildren(t *testing.T, r *RoleBasedGroupSetReconciler, setName string) []workloadsv1alpha2.RoleBasedGroup {
	t.Helper()
	var list workloadsv1alpha2.RoleBasedGroupList
	err := r.client.List(
		context.Background(), &list,
		client.InNamespace("default"),
		client.MatchingLabels{constants.GroupSetNameLabelKey: setName},
	)
	assert.NoError(t, err)
	return list.Items
}

func getChild(t *testing.T, r *RoleBasedGroupSetReconciler, name string) *workloadsv1alpha2.RoleBasedGroup {
	t.Helper()
	rbg := &workloadsv1alpha2.RoleBasedGroup{}
	err := r.client.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, rbg)
	assert.NoError(t, err)
	return rbg
}

func markChildReady(t *testing.T, r *RoleBasedGroupSetReconciler, name string) {
	t.Helper()
	rbg := getChild(t, r, name)
	rbg.Status.Conditions = []metav1.Condition{
		{
			Type:               string(workloadsv1alpha2.RoleBasedGroupReady),
			Status:             metav1.ConditionTrue,
			Reason:             "TestReady",
			LastTransitionTime: metav1.Now(),
		},
	}
	assert.NoError(t, r.client.Update(context.Background(), rbg))
}

// getSet re-reads the RoleBasedGroupSet so a test sees the status the reconcile just wrote.
func getSet(t *testing.T, r *RoleBasedGroupSetReconciler, name string) *workloadsv1alpha2.RoleBasedGroupSet {
	t.Helper()
	set := &workloadsv1alpha2.RoleBasedGroupSet{}
	assert.NoError(t, r.client.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, set))
	return set
}

func rbgsTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	assert.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	return scheme
}

// TestRollingUpdate_PacedRecreate verifies the recreate rolling update: one outdated group is
// deleted per budget slot, recreated from the new template, and the rollout waits for it to
// become ready before touching the next one.
func TestRollingUpdate_PacedRecreate(t *testing.T) {
	scheme := rbgsTestScheme(t)

	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	set := rollingTestSet("s", 3, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, oldRoles, true),
		rollingTestChild("s", 1, oldRoles, true),
		rollingTestChild("s", 2, oldRoles, true),
	)

	// First pass deletes only the highest-ordinal outdated group (maxUnavailable defaults to 1).
	reconcileSet(t, r, "s")
	assert.Len(t, listChildren(t, r, "s"), 2)

	// Second pass recreates it from the new template, but it is not ready yet, so the
	// budget stays exhausted and no further group is deleted.
	reconcileSet(t, r, "s")
	children := listChildren(t, r, "s")
	assert.Len(t, children, 3)
	assert.Equal(t, newRoles, getChild(t, r, "s-2").Spec.Roles)

	// Once the recreated group turns ready, the next outdated group is deleted.
	markChildReady(t, r, "s-2")
	reconcileSet(t, r, "s")
	children = listChildren(t, r, "s")
	assert.Len(t, children, 2)
	names := map[string]bool{children[0].Name: true, children[1].Name: true}
	assert.True(t, names["s-0"] && names["s-2"], "expected s-1 to be deleted, got %v", names)
}

// TestRollingUpdate_TemplateFlipFlopConverges verifies the A->B->A rollback: a template change
// to B followed by a flip back to A must not strand a group on B. This holds because version
// detection is content comparison, not revision history: whatever the current template content
// is, a group not matching it is outdated and rolls toward it, in either direction. A
// revision-history-based controller would treat B as "newer" and never return to A; this test
// pins the content-comparison contract.
func TestRollingUpdate_TemplateFlipFlopConverges(t *testing.T) {
	scheme := rbgsTestScheme(t)

	rolesA := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	rolesB := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	// Start fully on A, then the template moves to B.
	set := rollingTestSet("s", 2, rolesB, &workloadsv1alpha2.GroupSetRolloutStrategy{})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, rolesA, true),
		rollingTestChild("s", 1, rolesA, true),
	)

	// Roll toward B: the highest ordinal is deleted and recreated onto B, not yet ready.
	reconcileSet(t, r, "s")
	reconcileSet(t, r, "s")
	assert.Equal(t, rolesB, getChild(t, r, "s-1").Spec.Roles, "s-1 should have rolled onto B")

	// Flip the template back to A while s-1 is still coming up on B.
	set = getSet(t, r, "s")
	set.Spec.GroupTemplate.Spec.Roles = rolesA
	assert.NoError(t, r.client.Update(context.Background(), set))

	// s-1 is on B and not yet serving, so it removes no serving capacity and consumes no
	// budget: it is deleted and rebuilt onto A at once, instead of first finishing its
	// startup on the superseded content and serving it briefly.
	reconcileSet(t, r, "s") // delete s-1, which is on B and not serving
	reconcileSet(t, r, "s") // recreate s-1 onto A

	assert.Equal(t, rolesA, getChild(t, r, "s-0").Spec.Roles)
	assert.Equal(t, rolesA, getChild(t, r, "s-1").Spec.Roles,
		"a group mid-startup on the superseded template must be rebuilt onto A, not finish starting on B")
}

// TestRollingUpdate_NotServingOutdatedBypassesBudgetOnly pins the two halves of the budget
// rule together: a group that is outdated but not serving is replaced for free, while a group
// that is outdated and serving still waits for a budget slot. Removing the second half would let
// a rollback tear down serving capacity faster than maxUnavailable allows.
func TestRollingUpdate_NotServingOutdatedBypassesBudgetOnly(t *testing.T) {
	scheme := rbgsTestScheme(t)

	rolesA := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	rolesB := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}
	rolesC := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 20}}

	// Template is C. s-2 is on B and not ready (outdated, not serving); s-1 is on A and ready
	// (outdated, serving); s-0 is on C and ready (up to date).
	set := rollingTestSet("s", 3, rolesC, &workloadsv1alpha2.GroupSetRolloutStrategy{})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, rolesC, true),
		rollingTestChild("s", 1, rolesA, true),
		rollingTestChild("s", 2, rolesB, false),
	)

	reconcileSet(t, r, "s")

	children := listChildren(t, r, "s")
	assert.Len(t, children, 2, "s-2 (outdated, not serving) is replaced for free; s-1 (outdated, serving) waits")
	names := map[string]bool{children[0].Name: true, children[1].Name: true}
	assert.True(t, names["s-0"] && names["s-1"],
		"the serving-but-outdated s-1 must wait for a budget slot; got %v", names)
}

// TestRecreateDeleteCarriesUIDPrecondition verifies that a recreate delete names the exact
// object this reconcile classified, not merely its name. Delete locates its target by name
// alone, so without the precondition a snapshot that lagged across a whole
// delete-recreate-ready cycle would delete the replacement instead. The fake client does not
// enforce UID preconditions, so what is asserted here is what we pass; turning a mismatch into
// a 409 is the API server's contract.
func TestRecreateDeleteCarriesUIDPrecondition(t *testing.T) {
	scheme := rbgsTestScheme(t)

	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	set := rollingTestSet("s", 1, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{})
	outdated := rollingTestChild("s", 0, oldRoles, true)
	outdated.UID = "uid-this-reconcile-classified"

	var seenUIDs []types.UID
	var seenPolicies []metav1.DeletionPropagation
	base := fake.NewClientBuilder().WithScheme(scheme).
		WithRuntimeObjects(set, outdated).
		WithStatusSubresource(&workloadsv1alpha2.RoleBasedGroupSet{}).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Delete: func(
			ctx context.Context, inner client.WithWatch, obj client.Object, opts ...client.DeleteOption,
		) error {
			do := client.DeleteOptions{}
			do.ApplyOptions(opts)
			if do.Preconditions != nil && do.Preconditions.UID != nil {
				seenUIDs = append(seenUIDs, *do.Preconditions.UID)
			}
			if do.PropagationPolicy != nil {
				seenPolicies = append(seenPolicies, *do.PropagationPolicy)
			}
			return inner.Delete(ctx, obj, opts...)
		},
	})
	r := &RoleBasedGroupSetReconciler{client: c, scheme: scheme, recorder: record.NewFakeRecorder(100)}

	reconcileSet(t, r, "s")

	assert.Equal(t, []types.UID{"uid-this-reconcile-classified"}, seenUIDs,
		"the recreate delete must pin the UID of the group it reasoned about")
	assert.Equal(t, []metav1.DeletionPropagation{metav1.DeletePropagationForeground}, seenPolicies,
		"the recreate delete must wait for the whole dependent chain")
}

// TestRollingUpdate_ReplicasOnlyChangeIsScaledInPlace verifies the exemption: when the only
// template diff is role replicas, groups are scaled in place instead of recreated.
func TestRollingUpdate_ReplicasOnlyChangeIsScaledInPlace(t *testing.T) {
	scheme := rbgsTestScheme(t)

	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(4))}}

	set := rollingTestSet("s", 2, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, oldRoles, true),
		rollingTestChild("s", 1, oldRoles, true),
	)

	reconcileSet(t, r, "s")

	// No group was deleted and both carry the new replica count.
	children := listChildren(t, r, "s")
	assert.Len(t, children, 2)
	for _, child := range children {
		assert.Equal(t, ptr.To(int32(4)), child.Spec.Roles[0].Replicas)
	}
}

// TestRollingUpdate_PartitionHoldsBackLowerOrdinals verifies that ordinals below the
// partition keep the previous template.
func TestRollingUpdate_PartitionHoldsBackLowerOrdinals(t *testing.T) {
	scheme := rbgsTestScheme(t)

	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	set := rollingTestSet("s", 3, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{
		Partition: ptr.To(intstr.FromInt32(2)),
	})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, oldRoles, true),
		rollingTestChild("s", 1, oldRoles, true),
		rollingTestChild("s", 2, oldRoles, true),
	)

	// Only ordinal 2 takes part in the rollout.
	reconcileSet(t, r, "s")
	assert.Len(t, listChildren(t, r, "s"), 2)

	// After the recreation is ready, the held-back ordinals are still untouched.
	reconcileSet(t, r, "s")
	markChildReady(t, r, "s-2")
	reconcileSet(t, r, "s")
	children := listChildren(t, r, "s")
	assert.Len(t, children, 3)
	assert.Equal(t, oldRoles, getChild(t, r, "s-0").Spec.Roles)
	assert.Equal(t, oldRoles, getChild(t, r, "s-1").Spec.Roles)
	assert.Equal(t, newRoles, getChild(t, r, "s-2").Spec.Roles)
}

// TestRollingUpdate_PartitionRolloutReportsComplete verifies that with a partition the rollout
// reports complete once every in-scope group is updated and serving. The held-back groups below
// the partition are on the previous template by design; counting them as not-updated would wedge
// the Rolling condition at InProgress forever and never reclaim surge capacity.
func TestRollingUpdate_PartitionRolloutReportsComplete(t *testing.T) {
	scheme := rbgsTestScheme(t)

	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	// partition=1 holds ordinal 0 back on the previous template; only ordinal 1 rolls.
	set := rollingTestSet("s", 2, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{
		Partition: ptr.To(intstr.FromInt32(1)),
	})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, oldRoles, true),
		rollingTestChild("s", 1, oldRoles, true),
	)

	reconcileSet(t, r, "s") // delete s-1, the only in-scope group
	reconcileSet(t, r, "s") // recreate s-1 onto the new template, not ready yet
	markChildReady(t, r, "s-1")
	reconcileSet(t, r, "s")

	assert.Equal(t, oldRoles, getChild(t, r, "s-0").Spec.Roles, "s-0 is held back below the partition")
	assert.Equal(t, newRoles, getChild(t, r, "s-1").Spec.Roles, "s-1 rolled onto the new template")

	got := getSet(t, r, "s")
	assert.Equal(t, int32(1), got.Status.ExpectedUpdatedReplicas)
	assert.Equal(t, int32(1), got.Status.UpdatedReadyReplicas)
	rolling := meta.FindStatusCondition(got.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupSetRolling))
	if assert.NotNil(t, rolling) {
		assert.Equal(t, metav1.ConditionFalse, rolling.Status,
			"the in-scope rollout is done, so Rolling must be Complete; held-back groups must not count, got %q: %s",
			rolling.Reason, rolling.Message)
	}
}

// TestRollingUpdate_PartitionReleasesSurgeAfterInScopeRollout pins the steady state of
// partition>0 combined with maxSurge>0. Once every in-scope ordinal is on the template and
// serving, the rollout is done for surge purposes: the surge group is reclaimed and — this
// is what the original churn bug violated — it is not re-created by the next reconcile's
// warm-up just because held-back ordinals still differ from the template.
func TestRollingUpdate_PartitionReleasesSurgeAfterInScopeRollout(t *testing.T) {
	scheme := rbgsTestScheme(t)

	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	// partition=1 holds ordinal 0 back; ordinal 1 is already rolled; the surge group exists.
	set := rollingTestSet("s", 2, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{
		MaxSurge:  ptr.To(intstr.FromInt32(1)),
		Partition: ptr.To(intstr.FromInt32(1)),
	})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, oldRoles, true), // held back below the partition
		rollingTestChild("s", 1, newRoles, true), // in-scope, already rolled
		rollingTestChild("s", 2, newRoles, true), // surge
	)

	reconcileSet(t, r, "s")
	assert.Len(t, listChildren(t, r, "s"), 2, "the in-scope rollout is done, so the surge group is reclaimed")

	// Repeated reconciles must not bring it back: held-back ordinals are not rollout work,
	// so they must not re-arm the surge warm-up.
	for i := 0; i < 3; i++ {
		reconcileSet(t, r, "s")
		assert.Len(t, listChildren(t, r, "s"), 2, "the set must settle at replicas, pass %d", i)
		assert.Equal(t, oldRoles, getChild(t, r, "s-0").Spec.Roles, "the held-back ordinal must not roll")
	}
}

// TestRollingUpdate_StandingCanaryKeepsSurge pins the one configuration where a finished
// rollout must not release the surge group: partition == replicas. The rollout scope is
// empty, so partition-scoped completion is vacuously true, and reclaiming on it would
// delete the canary the user asked maxSurge to hold. The surge group keeps its identity
// until the template itself converges. The UID is what proves stability: a reclaim
// followed by a warm-up re-create inside one reconcile keeps the child count unchanged,
// so counting children cannot see churn — only identity can.
func TestRollingUpdate_StandingCanaryKeepsSurge(t *testing.T) {
	scheme := rbgsTestScheme(t)

	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	// partition == replicas freezes both base groups on the old template; the surge group
	// is the standing canary on the new one.
	set := rollingTestSet("s", 2, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{
		MaxSurge:  ptr.To(intstr.FromInt32(1)),
		Partition: ptr.To(intstr.FromInt32(2)),
	})
	surge := rollingTestChild("s", 2, newRoles, true)
	surge.UID = types.UID("canary-uid")
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, oldRoles, true),
		rollingTestChild("s", 1, oldRoles, true),
		surge,
	)

	for i := 0; i < 3; i++ {
		reconcileSet(t, r, "s")
		assert.Len(t, listChildren(t, r, "s"), 3, "the canary must survive, pass %d", i)
		assert.Equal(t, types.UID("canary-uid"), getChild(t, r, "s-2").UID,
			"the canary must not be reclaimed and re-created, pass %d", i)
		assert.Equal(t, oldRoles, getChild(t, r, "s-0").Spec.Roles, "the frozen ordinals must not roll")
	}

	// Reverting the template converges the set, and only then is the surge group reclaimed.
	set = getSet(t, r, "s")
	set.Spec.GroupTemplate.Spec.Roles = oldRoles
	assert.NoError(t, r.client.Update(context.Background(), set))
	reconcileSet(t, r, "s")
	assert.Len(t, listChildren(t, r, "s"), 2, "surge is reclaimed once the whole set converges")
}

// TestRollingUpdate_SurgeGroupsCreated verifies that surge groups are created at the ordinals
// above spec.replicas while a rollout is in flight.
func TestRollingUpdate_SurgeGroupsCreated(t *testing.T) {
	scheme := rbgsTestScheme(t)

	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	set := rollingTestSet("s", 2, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{
		MaxSurge: ptr.To(intstr.FromInt32(1)),
	})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, oldRoles, true),
		rollingTestChild("s", 1, oldRoles, true),
	)

	reconcileSet(t, r, "s")

	surge := getChild(t, r, "s-2")
	assert.Equal(t, "2", surge.Labels[constants.GroupSetIndexLabelKey])
	assert.Equal(t, newRoles, surge.Spec.Roles)
	// The unavailability budget still allows one base deletion alongside the not-yet-ready
	// surge group.
	children := listChildren(t, r, "s")
	assert.Len(t, children, 2) // s-0 and the surge group; s-1 was deleted
}

// TestRollingUpdate_SurgeNotCreatedWhenMaxUnavailableCoversWork pins the waste the warm-up
// gate exists to prevent: when maxUnavailable alone covers the in-scope work, the delete
// budget never needs surge capacity, so creating surge groups would be pure churn — they
// would be created at the start of the rollout and reclaimed at the end without ever
// gating anything.
func TestRollingUpdate_SurgeNotCreatedWhenMaxUnavailableCoversWork(t *testing.T) {
	scheme := rbgsTestScheme(t)

	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	// replicas-partition = 2 = maxUnavailable: both in-scope groups may be deleted at once
	// without any surge capacity.
	set := rollingTestSet("s", 4, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{
		MaxUnavailable: ptr.To(intstr.FromInt32(2)),
		MaxSurge:       ptr.To(intstr.FromInt32(2)),
		Partition:      ptr.To(intstr.FromInt32(2)),
	})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, oldRoles, true),
		rollingTestChild("s", 1, oldRoles, true),
		rollingTestChild("s", 2, oldRoles, true),
		rollingTestChild("s", 3, oldRoles, true),
	)

	assertNoSurgeOrdinals := func() {
		t.Helper()
		for _, child := range listChildren(t, r, "s") {
			ordinal, err := strconv.Atoi(child.Labels[constants.GroupSetIndexLabelKey])
			assert.NoError(t, err)
			assert.Less(t, ordinal, 4, "no surge group may be created, found %s", child.Name)
		}
	}

	reconcileSet(t, r, "s") // deletes s-3 and s-2 under the budget, creates no surge
	assert.Len(t, listChildren(t, r, "s"), 2)
	assertNoSurgeOrdinals()

	reconcileSet(t, r, "s") // recreates s-2 and s-3 onto the new template, still no surge
	assert.Len(t, listChildren(t, r, "s"), 4)
	assertNoSurgeOrdinals()

	markChildReady(t, r, "s-2")
	markChildReady(t, r, "s-3")
	reconcileSet(t, r, "s")
	assertNoSurgeOrdinals()
	assert.Equal(t, newRoles, getChild(t, r, "s-2").Spec.Roles)
	assert.Equal(t, oldRoles, getChild(t, r, "s-0").Spec.Roles, "the held-back ordinals stay on the old template")
}

// TestRollingUpdate_SurgeKeptUntilRecreatedGroupIsReady verifies that surge capacity survives
// until every base group is serving. A recreated group matches the template the moment it
// exists, so gating reclamation on template equality alone withdraws the surge groups exactly
// when availability is at its lowest and drops the set below replicas-maxUnavailable.
func TestRollingUpdate_SurgeKeptUntilRecreatedGroupIsReady(t *testing.T) {
	scheme := rbgsTestScheme(t)

	roles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}

	set := rollingTestSet("s", 2, roles, &workloadsv1alpha2.GroupSetRolloutStrategy{
		MaxSurge: ptr.To(intstr.FromInt32(1)),
	})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, roles, false), // recreated, on the new template, not serving yet
		rollingTestChild("s", 1, roles, true),
		rollingTestChild("s", 2, roles, true), // surge
	)

	reconcileSet(t, r, "s")
	assert.Len(t, listChildren(t, r, "s"), 3, "surge must keep serving while s-0 comes up")

	markChildReady(t, r, "s-0")
	reconcileSet(t, r, "s")
	assert.Len(t, listChildren(t, r, "s"), 2, "surge is reclaimed once every base group is ready")
}

// TestRollingUpdate_RolloutCompleteRequiresReady verifies that the Rolling condition keeps
// reporting RolloutInProgress while a group on the new template is still not serving. Without
// the readiness check it would claim RolloutComplete next to a Ready condition saying the
// opposite.
func TestRollingUpdate_RolloutCompleteRequiresReady(t *testing.T) {
	scheme := rbgsTestScheme(t)

	roles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}

	set := rollingTestSet("s", 2, roles, &workloadsv1alpha2.GroupSetRolloutStrategy{})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, roles, false),
		rollingTestChild("s", 1, roles, true),
	)

	rollingCondition := func() *metav1.Condition {
		return meta.FindStatusCondition(
			getSet(t, r, "s").Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupSetRolling),
		)
	}

	reconcileSet(t, r, "s")
	cond := rollingCondition()
	assert.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "RolloutInProgress", cond.Reason)

	markChildReady(t, r, "s-0")
	reconcileSet(t, r, "s")
	cond = rollingCondition()
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "RolloutComplete", cond.Reason)
}

// TestRoleBasedGroupSetReconciler_TerminatingChildIsNotCountedReady verifies that a group being
// deleted stays out of readyReplicas and the Ready condition. Its own Ready condition remains
// true while its Pods work through preStop hooks and the termination grace period, so counting
// it would report full availability for that whole window even though it is going away.
func TestRoleBasedGroupSetReconciler_TerminatingChildIsNotCountedReady(t *testing.T) {
	scheme := rbgsTestScheme(t)

	roles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	set := rollingTestSet("s", 2, roles, &workloadsv1alpha2.GroupSetRolloutStrategy{})

	terminating := rollingTestChild("s", 1, roles, true)
	// The finalizer is what keeps an object with a deletionTimestamp alive in the fake client,
	// which is exactly the state foreground propagation produces while the Pods terminate.
	terminating.Finalizers = []string{"test/keep-alive"}
	now := metav1.Now()
	terminating.DeletionTimestamp = &now

	r := newRollingTestReconciler(scheme, set, rollingTestChild("s", 0, roles, true), terminating)
	reconcileSet(t, r, "s")

	status := getSet(t, r, "s").Status
	assert.Equal(t, int32(2), status.Replicas)
	assert.Equal(t, int32(1), status.ReadyReplicas, "the terminating group must not count as ready")

	ready := meta.FindStatusCondition(status.Conditions, string(workloadsv1alpha2.RoleBasedGroupSetReady))
	assert.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, "ReplicasNotReady", ready.Reason)
}

// TestStatus_CapacityCountersIncludeSurge pins the capacity/progress split of the status
// counters: replicas and readyReplicas count every child including surge groups (capacity
// truth, matching Deployment/CloneSet), while the updated counter family counts base ordinals
// only (progress truth). Paused freezes the rollout mechanics so the counters can be observed
// without the controller deleting or creating anything.
func TestStatus_CapacityCountersIncludeSurge(t *testing.T) {
	scheme := rbgsTestScheme(t)
	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	t.Run("surge window counts surge in replicas and readyReplicas", func(t *testing.T) {
		set := rollingTestSet("s", 2, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{
			MaxSurge: ptr.To(intstr.FromInt32(1)),
			Paused:   true,
		})
		r := newRollingTestReconciler(scheme, set,
			rollingTestChild("s", 0, oldRoles, true),
			rollingTestChild("s", 1, oldRoles, true),
			rollingTestChild("s", 2, newRoles, true), // surge
		)
		reconcileSet(t, r, "s")

		status := getSet(t, r, "s").Status
		assert.Equal(t, int32(3), status.Replicas, "capacity counts surge, so it exceeds spec.replicas in a surge window")
		assert.Equal(t, int32(3), status.ReadyReplicas)
		assert.Equal(t, int32(0), status.UpdatedReplicas, "progress counts base ordinals only")
		assert.Equal(t, int32(2), status.CurrentReplicas)
		assert.Equal(t, int32(2), status.ExpectedUpdatedReplicas)

		ready := meta.FindStatusCondition(status.Conditions, string(workloadsv1alpha2.RoleBasedGroupSetReady))
		if assert.NotNil(t, ready) {
			assert.Equal(t, metav1.ConditionTrue, ready.Status)
		}
	})

	t.Run("a ready surge group keeps Ready true while a base group is down", func(t *testing.T) {
		set := rollingTestSet("s", 3, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{
			MaxUnavailable: ptr.To(intstr.FromInt32(0)),
			MaxSurge:       ptr.To(intstr.FromInt32(1)),
			Paused:         true,
		})
		r := newRollingTestReconciler(scheme, set,
			rollingTestChild("s", 0, newRoles, true),
			rollingTestChild("s", 1, newRoles, true),
			rollingTestChild("s", 2, oldRoles, false), // base group not serving
			rollingTestChild("s", 3, newRoles, true),  // surge serving in its place
		)
		reconcileSet(t, r, "s")

		status := getSet(t, r, "s").Status
		assert.Equal(t, int32(4), status.Replicas)
		assert.Equal(t, int32(3), status.ReadyReplicas)
		assert.Equal(t, int32(2), status.UpdatedReplicas)
		assert.Equal(t, int32(2), status.UpdatedReadyReplicas)
		assert.Equal(t, int32(1), status.CurrentReplicas)

		ready := meta.FindStatusCondition(status.Conditions, string(workloadsv1alpha2.RoleBasedGroupSetReady))
		if assert.NotNil(t, ready) {
			assert.Equal(t, metav1.ConditionTrue, ready.Status,
				"maxUnavailable 0 means capacity must not drop; the serving surge group delivers exactly that")
		}
	})
}

// TestRollingUpdate_PausedFreezesRollout verifies that a paused rollout deletes nothing.
func TestRollingUpdate_PausedFreezesRollout(t *testing.T) {
	scheme := rbgsTestScheme(t)

	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	set := rollingTestSet("s", 2, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{
		Paused: true,
	})
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, oldRoles, true),
		rollingTestChild("s", 1, oldRoles, true),
	)

	reconcileSet(t, r, "s")

	children := listChildren(t, r, "s")
	assert.Len(t, children, 2)
	for _, child := range children {
		assert.Equal(t, oldRoles, child.Spec.Roles)
	}
}

// TestStaticUpdate_AllGroupsUpdatedInOnePass verifies the behavior for sets without a rollout
// strategy (including every set created before the feature existed): every outdated group is
// updated in place within a single reconcile, nothing is deleted.
func TestStaticUpdate_AllGroupsUpdatedInOnePass(t *testing.T) {
	scheme := rbgsTestScheme(t)

	oldRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
	newRoles := []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1)), MinReadySeconds: 10}}

	set := rollingTestSet("s", 3, newRoles, &workloadsv1alpha2.GroupSetRolloutStrategy{})
	set.Spec.RolloutStrategy = nil
	r := newRollingTestReconciler(scheme, set,
		rollingTestChild("s", 0, oldRoles, true),
		rollingTestChild("s", 1, oldRoles, true),
		rollingTestChild("s", 2, oldRoles, true),
	)

	reconcileSet(t, r, "s")

	children := listChildren(t, r, "s")
	assert.Len(t, children, 3)
	for _, child := range children {
		assert.Equal(t, newRoles, child.Spec.Roles)
	}
}

func TestOnlyReplicasChanged(t *testing.T) {
	r := &RoleBasedGroupSetReconciler{}
	role := func(name string, replicas int32) workloadsv1alpha2.RoleSpec {
		return workloadsv1alpha2.RoleSpec{Name: name, Replicas: ptr.To(replicas)}
	}

	// Only replicas differ, order-insensitive.
	assert.True(t, r.onlyReplicasChanged(
		[]workloadsv1alpha2.RoleSpec{role("a", 1), role("b", 2)},
		[]workloadsv1alpha2.RoleSpec{role("b", 4), role("a", 3)},
	))
	// Identical roles vacuously qualify; callers gate on rolesEqual first.
	assert.True(t, r.onlyReplicasChanged(
		[]workloadsv1alpha2.RoleSpec{role("a", 1)},
		[]workloadsv1alpha2.RoleSpec{role("a", 1)},
	))
	// Role count differs.
	assert.False(t, r.onlyReplicasChanged(
		[]workloadsv1alpha2.RoleSpec{role("a", 1)},
		[]workloadsv1alpha2.RoleSpec{role("a", 1), role("b", 1)},
	))
	// Role renamed.
	assert.False(t, r.onlyReplicasChanged(
		[]workloadsv1alpha2.RoleSpec{role("a", 1)},
		[]workloadsv1alpha2.RoleSpec{role("z", 1)},
	))
	// Another field changed alongside replicas.
	other := role("a", 3)
	other.MinReadySeconds = 5
	assert.False(t, r.onlyReplicasChanged(
		[]workloadsv1alpha2.RoleSpec{role("a", 1)},
		[]workloadsv1alpha2.RoleSpec{other},
	))
	// A legacy-spelled strategy type on the stored template must not read as a real change:
	// children carry the webhook-normalized spelling, so the remaining diff is replicas-only
	// and must scale in place rather than be misrouted to recreate.
	withStrategy := func(rs workloadsv1alpha2.RoleSpec, t workloadsv1alpha2.UpdateStrategyType) workloadsv1alpha2.RoleSpec {
		rs.RolloutStrategy = &workloadsv1alpha2.RolloutStrategy{
			Type:          workloadsv1alpha2.RollingUpdateStrategyType,
			RollingUpdate: &workloadsv1alpha2.RollingUpdate{Type: t},
		}
		return rs
	}
	assert.True(t, r.onlyReplicasChanged(
		[]workloadsv1alpha2.RoleSpec{withStrategy(role("a", 1), workloadsv1alpha2.RecreatePodUpdateStrategyType)},
		[]workloadsv1alpha2.RoleSpec{withStrategy(role("a", 3), workloadsv1alpha2.LegacyRecreateUpdateStrategyType)},
	))
}

func TestResolveRollingParams(t *testing.T) {
	// No rollout strategy at all.
	set := &workloadsv1alpha2.RoleBasedGroupSet{
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(3))},
	}
	assert.Equal(t, rollingParams{maxUnavailable: 1}, resolveRollingParams(set))

	// Strategy present but no rollingUpdate block.
	set = rollingTestSet("s", 3, nil, nil)
	assert.Equal(t, rollingParams{maxUnavailable: 1}, resolveRollingParams(set))

	// Percentages: maxSurge rounds up, maxUnavailable rounds down while surge > 0,
	// partition rounds down.
	set = rollingTestSet("s", 10, nil, &workloadsv1alpha2.GroupSetRolloutStrategy{
		MaxSurge:       ptr.To(intstr.FromString("20%")),
		MaxUnavailable: ptr.To(intstr.FromString("25%")),
		Partition:      ptr.To(intstr.FromString("30%")),
	})
	assert.Equal(t, rollingParams{partition: 3, maxUnavailable: 2, maxSurge: 2}, resolveRollingParams(set))

	// maxUnavailable rounds up when maxSurge is 0.
	set = rollingTestSet("s", 10, nil, &workloadsv1alpha2.GroupSetRolloutStrategy{
		MaxUnavailable: ptr.To(intstr.FromString("21%")),
	})
	assert.Equal(t, rollingParams{maxUnavailable: 3}, resolveRollingParams(set))

	// A resolved maxUnavailable of 0 is kept when a surge budget backs it: the ready surge
	// groups supply the capacity, so flooring to 1 would let the rollout drop a base group
	// the user explicitly asked to keep.
	set = rollingTestSet("s", 10, nil, &workloadsv1alpha2.GroupSetRolloutStrategy{
		MaxSurge:       ptr.To(intstr.FromInt32(1)),
		MaxUnavailable: ptr.To(intstr.FromString("1%")),
	})
	assert.Equal(t, rollingParams{maxUnavailable: 0, maxSurge: 1}, resolveRollingParams(set))

	// Without a surge budget the same resolved 0 is floored to 1, otherwise no group could
	// ever be recreated and the rollout would stall.
	set = rollingTestSet("s", 10, nil, &workloadsv1alpha2.GroupSetRolloutStrategy{
		MaxUnavailable: ptr.To(intstr.FromString("1%")),
	})
	assert.Equal(t, rollingParams{maxUnavailable: 1}, resolveRollingParams(set))

	// Partition is clamped to replicas (admission normally rejects larger values).
	set = rollingTestSet("s", 2, nil, &workloadsv1alpha2.GroupSetRolloutStrategy{
		Partition: ptr.To(intstr.FromInt32(5)),
	})
	assert.Equal(t, rollingParams{partition: 2, maxUnavailable: 1}, resolveRollingParams(set))
}

// TestSyncRBGMetadataKeepsSystemKeys covers the loop two controllers got into. The
// RoleBasedGroup controller records discovery-config-mode on its own object; a template sync
// that rebuilt the whole annotation map deleted it, so that controller wrote it again, so this
// one saw drift again, and the child was updated forever while its downstream workloads
// re-hashed and its readiness flickered.
func TestSyncRBGMetadataKeepsSystemKeys(t *testing.T) {
	r := &RoleBasedGroupSetReconciler{}
	rbgset := &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
	}
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "s-0",
			Namespace: "default",
			Labels: map[string]string{
				constants.GroupSetNameLabelKey:  "s",
				constants.GroupSetIndexLabelKey: "0",
			},
			Annotations: map[string]string{
				constants.DiscoveryConfigModeAnnotationKey: "refine",
				"example.com/dropped":                      "gone",
			},
		},
	}

	r.syncRBGMetadata(rbgset, rbg)

	assert.Equal(t, "refine", rbg.Annotations[constants.DiscoveryConfigModeAnnotationKey],
		"an annotation another controller owns must survive a template sync")
	assert.NotContains(t, rbg.Annotations, "example.com/dropped",
		"a user annotation the template no longer specifies is still dropped")
	assert.Equal(t, "s", rbg.Labels[constants.GroupSetNameLabelKey])
	assert.Equal(t, "0", rbg.Labels[constants.GroupSetIndexLabelKey])

	// The point of the whole exercise: after one sync the predicates agree, so the next
	// reconcile has nothing to do and the two controllers settle.
	assert.False(t, r.needsTemplateAnnotationUpdate(rbgset, rbg))
	assert.False(t, r.needsTemplateLabelUpdate(rbgset, rbg))
}

// TestSyncRBGMetadataPropagatesUserFacingPrefixKeys pins the other half of the ownership
// split: only the enumerated controller-owned keys are special. A user-facing key under the
// project prefix — group-exclusive-topology is the documented example — must propagate from
// the template exactly like any other key: added and updated when the template carries it,
// removed when the template drops it. A prefix-wide ownership match would silently keep the
// child's value in all three cases.
func TestSyncRBGMetadataPropagatesUserFacingPrefixKeys(t *testing.T) {
	r := &RoleBasedGroupSetReconciler{}

	t.Run("removal from the template removes the key from the child", func(t *testing.T) {
		rbgset := &workloadsv1alpha2.RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
		}
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "s-0",
				Namespace: "default",
				Labels: map[string]string{
					constants.GroupSetNameLabelKey:  "s",
					constants.GroupSetIndexLabelKey: "0",
				},
				Annotations: map[string]string{
					constants.GroupExclusiveTopologyKey:        "topology.kubernetes.io/zone",
					constants.DiscoveryConfigModeAnnotationKey: "refine",
				},
			},
		}

		assert.True(t, r.needsTemplateAnnotationUpdate(rbgset, rbg),
			"a user-facing prefix key the template no longer specifies must count as drift")
		r.syncRBGMetadata(rbgset, rbg)
		assert.NotContains(t, rbg.Annotations, constants.GroupExclusiveTopologyKey)
		assert.Equal(t, "refine", rbg.Annotations[constants.DiscoveryConfigModeAnnotationKey],
			"the genuinely controller-owned key is still preserved")
	})

	t.Run("add and update propagate from the template", func(t *testing.T) {
		rbgset := &workloadsv1alpha2.RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
			Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
				GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
					Annotations: map[string]string{constants.GroupExclusiveTopologyKey: "topology.kubernetes.io/zone"},
				},
			},
		}
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "s-0",
				Namespace:   "default",
				Labels:      map[string]string{constants.GroupSetNameLabelKey: "s", constants.GroupSetIndexLabelKey: "0"},
				Annotations: map[string]string{constants.GroupExclusiveTopologyKey: "kubernetes.io/hostname"},
			},
		}

		assert.True(t, r.needsTemplateAnnotationUpdate(rbgset, rbg))
		r.syncRBGMetadata(rbgset, rbg)
		assert.Equal(t, "topology.kubernetes.io/zone", rbg.Annotations[constants.GroupExclusiveTopologyKey])
	})
}

func TestClassifyChildren(t *testing.T) {
	list := &workloadsv1alpha2.RoleBasedGroupList{
		Items: []workloadsv1alpha2.RoleBasedGroup{
			*rollingTestChild("s", 0, nil, true),
			*rollingTestChild("s", 2, nil, true),
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "bad-index",
					Labels: map[string]string{constants.GroupSetIndexLabelKey: "abc"},
				},
			},
			{ObjectMeta: metav1.ObjectMeta{Name: "no-index"}},
		},
	}

	children := classifyChildren(list, 2)
	assert.Len(t, children.base, 1)
	assert.Contains(t, children.base, 0)
	assert.Len(t, children.surge, 1)
	assert.Contains(t, children.surge, 2)
	assert.Len(t, children.invalid, 2)
}

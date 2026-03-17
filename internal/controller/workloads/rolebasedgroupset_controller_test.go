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
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

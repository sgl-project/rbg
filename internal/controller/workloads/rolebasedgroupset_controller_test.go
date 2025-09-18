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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRoleBasedGroupSetReconciler_CheckCrdExists(t *testing.T) {
	// Setup test scheme with required types
	testScheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(testScheme)
	_ = apiextensionsv1.AddToScheme(testScheme)

	// Target CRD name following Kubernetes naming convention
	targetCRDName := "rolebasedgroupsets.workloads.x-k8s.io"

	type fields struct {
		apiReader client.Reader
	}

	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "CRD exists and established",
			fields: fields{
				apiReader: fake.NewClientBuilder().
					WithScheme(testScheme).
					WithObjects(
						&apiextensionsv1.CustomResourceDefinition{
							ObjectMeta: metav1.ObjectMeta{Name: targetCRDName},
							Status: apiextensionsv1.CustomResourceDefinitionStatus{
								Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
									{
										Type:   apiextensionsv1.Established,
										Status: apiextensionsv1.ConditionTrue,
									},
								},
							},
						},
					).
					Build(),
			},
			wantErr: false,
		},
		{
			name: "CRD not found in cluster",
			fields: fields{
				apiReader: fake.NewClientBuilder().
					WithScheme(testScheme).
					Build(), // Empty client
			},
			wantErr: true,
		},
		{
			name: "CRD exists but not established",
			fields: fields{
				apiReader: fake.NewClientBuilder().
					WithScheme(testScheme).
					WithObjects(
						&apiextensionsv1.CustomResourceDefinition{
							ObjectMeta: metav1.ObjectMeta{Name: targetCRDName},
							Status:     apiextensionsv1.CustomResourceDefinitionStatus{
								// No established condition
							},
						},
					).
					Build(),
			},
			wantErr: true,
		},
		{
			name: "API server connection error",
			fields: fields{
				apiReader: fake.NewClientBuilder().WithScheme(testScheme).WithInterceptorFuncs(
					interceptor.Funcs{
						Get: func(
							ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object,
							opts ...client.GetOption,
						) error {
							return fmt.Errorf("API server connection error")
						},
					},
				).Build(),
			},
			wantErr: true,
		},
		{
			name: "RBAC permission denied",
			fields: fields{
				apiReader: fake.NewClientBuilder().WithScheme(testScheme).WithInterceptorFuncs(
					interceptor.Funcs{
						Get: func(
							ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object,
							opts ...client.GetOption,
						) error {
							return apierrors.NewForbidden(
								schema.GroupResource{
									Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions",
								},
								targetCRDName,
								fmt.Errorf("requires 'get' permission"),
							)
						},
					},
				).Build(),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &RoleBasedGroupSetReconciler{
					apiReader: tt.fields.apiReader, // Focus on API reader component
				}

				// Execute the check operation
				err := r.CheckCrdExists()

				// Validate error expectations
				if (err != nil) != tt.wantErr {
					t.Errorf("CheckCrdExists() error = %v, wantErr %v", err, tt.wantErr)
				}

				// Additional error type assertions
				if tt.wantErr {
					switch tt.name {
					case "RBAC permission denied":
						if !apierrors.IsForbidden(err) {
							t.Error("Expected Forbidden error not received")
						}
					case "CRD not found in cluster":
						if !apierrors.IsNotFound(err) {
							t.Error("Expected NotFound error not received")
						}
					}
				}
			},
		)
	}
}

// TestRoleBasedGroupSetReconciler_scaleUp tests the scaleUp function.
func TestRoleBasedGroupSetReconciler_scaleUp(t *testing.T) {
	// Setup test scheme
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)

	// Create a RoleBasedGroupSet for testing
	rbgset := &v1alpha1.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbgset",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: v1alpha1.RoleBasedGroupSetSpec{
			Template: v1alpha1.RoleBasedGroupSpec{
				Roles: []v1alpha1.RoleSpec{
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
				var rbgsToCreate []*v1alpha1.RoleBasedGroup
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
				var rbglist v1alpha1.RoleBasedGroupList
				opts := []client.ListOption{
					client.InNamespace(rbgset.Namespace),
					client.MatchingLabels{v1alpha1.SetRBGSetNameLabelKey: rbgset.Name},
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
	_ = v1alpha1.AddToScheme(scheme)

	rbgBase := []v1alpha1.RoleBasedGroup{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rbg-0",
				Namespace: "default",
				Labels: map[string]string{
					v1alpha1.SetRBGSetNameLabelKey: "rbgs-test",
					v1alpha1.SetRBGIndexLabelKey:   "0",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rbg-1",
				Namespace: "default",
				Labels: map[string]string{
					v1alpha1.SetRBGSetNameLabelKey: "rbgs-test",
					v1alpha1.SetRBGIndexLabelKey:   "1",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "rbg-2",
				Namespace: "default",
				Labels: map[string]string{
					v1alpha1.SetRBGSetNameLabelKey: "rbgs-test",
					v1alpha1.SetRBGIndexLabelKey:   "2",
				},
			},
		},
	}

	tests := []struct {
		name                string
		initialRBGs         []v1alpha1.RoleBasedGroup
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
			initialRBGs:         []v1alpha1.RoleBasedGroup{},
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
				var rbgsToDelete []*v1alpha1.RoleBasedGroup
				for _, index := range tt.rbgsToDeleteIndices {
					// We need to pass pointers to copies to avoid issues with loop variables.
					rbgCopy := tt.initialRBGs[index].DeepCopy()
					rbgsToDelete = append(rbgsToDelete, rbgCopy)
				}

				err := r.scaleDown(context.Background(), rbgsToDelete)
				assert.NoError(t, err)

				// Verify the result by listing the remaining objects.
				var leftRbgList v1alpha1.RoleBasedGroupList
				opts := []client.ListOption{
					client.InNamespace("default"),
					client.MatchingLabels{v1alpha1.SetRBGSetNameLabelKey: "rbgs-test"},
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

// TestRoleBasedGroupSetReconciler_Reconcile_StatusUpdate tests the status update logic within the Reconcile loop.
func TestRoleBasedGroupSetReconciler_Reconcile_StatusUpdate(t *testing.T) {
	// Setup test scheme
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)

	tests := []struct {
		name                string
		initialRBGSet       *v1alpha1.RoleBasedGroupSet
		rbgList             []v1alpha1.RoleBasedGroup
		expectReady         bool
		expectReplicas      int32
		expectReadyReplicas int32
		expectedReason      string
		expectedMessagePart string
	}{
		{
			name: "All RBGs ready, replicas match spec",
			initialRBGSet: &v1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       v1alpha1.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(2))},
			},
			rbgList: []v1alpha1.RoleBasedGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rbg-0",
						Namespace: "default",
						Labels: map[string]string{
							v1alpha1.SetRBGSetNameLabelKey: "test-rbgset",
							v1alpha1.SetRBGIndexLabelKey:   "0",
						},
					},
					Status: v1alpha1.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(v1alpha1.RoleBasedGroupReady),
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
							v1alpha1.SetRBGSetNameLabelKey: "test-rbgset",
							v1alpha1.SetRBGIndexLabelKey:   "1",
						},
					},
					Status: v1alpha1.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(v1alpha1.RoleBasedGroupReady),
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
			initialRBGSet: &v1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       v1alpha1.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(2))},
			},
			rbgList: []v1alpha1.RoleBasedGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rbg-0",
						Namespace: "default",
						Labels: map[string]string{
							v1alpha1.SetRBGSetNameLabelKey: "test-rbgset",
							v1alpha1.SetRBGIndexLabelKey:   "0",
						},
					},
					Status: v1alpha1.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(v1alpha1.RoleBasedGroupReady),
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
							v1alpha1.SetRBGSetNameLabelKey: "test-rbgset",
							v1alpha1.SetRBGIndexLabelKey:   "1",
						},
					},
					Status: v1alpha1.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(v1alpha1.RoleBasedGroupReady),
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
			initialRBGSet: &v1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       v1alpha1.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(1))},
			},
			rbgList: []v1alpha1.RoleBasedGroup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rbg-0",
						Namespace: "default",
						Labels: map[string]string{
							v1alpha1.SetRBGSetNameLabelKey: "test-rbgset",
							v1alpha1.SetRBGIndexLabelKey:   "0",
						},
					},
					Status: v1alpha1.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(v1alpha1.RoleBasedGroupReady),
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
			initialRBGSet: &v1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgset", Namespace: "default"},
				Spec:       v1alpha1.RoleBasedGroupSetSpec{Replicas: ptr.To(int32(0))},
			},
			rbgList:             []v1alpha1.RoleBasedGroup{},
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
						WithStatusSubresource(&v1alpha1.RoleBasedGroupSet{}).Build(),
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
				updatedRBGSet := &v1alpha1.RoleBasedGroupSet{}
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
				assert.Equal(t, string(v1alpha1.RoleBasedGroupSetReady), condition.Type)
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

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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"

	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/scale"
	"sigs.k8s.io/rbgs/pkg/utils"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func TestRoleBasedGroupReconciler_CheckCrdExists_Partial(t *testing.T) {
	testScheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(testScheme)
	_ = apiextensionsv1.AddToScheme(testScheme)

	// Target CRD name according to Kubernetes naming convention

	type fields struct {
		apiReader client.Reader
	}

	tests := []struct {
		name    string
		fields  fields
		wantErr bool
		errType string
	}{
		{
			name: "RBG CRD missing while Runtime CRD exists",
			fields: fields{
				apiReader: fake.NewClientBuilder().
					WithScheme(testScheme).
					WithObjects(
						&apiextensionsv1.CustomResourceDefinition{
							ObjectMeta: metav1.ObjectMeta{Name: utils.RuntimeCRDName},
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
			wantErr: true,
			errType: "not found",
		},
		{
			name: "Runtime CRD missing while RBG CRD exists",
			fields: fields{
				apiReader: fake.NewClientBuilder().
					WithScheme(testScheme).
					WithObjects(
						&apiextensionsv1.CustomResourceDefinition{
							ObjectMeta: metav1.ObjectMeta{Name: utils.RbgCRDName},
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
			wantErr: true,
			errType: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &RoleBasedGroupReconciler{
					apiReader: tt.fields.apiReader,
				}

				err := r.CheckCrdExists()

				if (err != nil) != tt.wantErr {
					t.Errorf("CheckCrdExists() error = %v, wantErr %v", err, tt.wantErr)
				}

				if tt.wantErr && tt.errType == "not found" {
					if !apierrors.IsNotFound(err) {
						t.Errorf("Expected NotFound error, got %T", err)
					}
				}
			},
		)
	}
}

func Test_hasValidOwnerRef(t *testing.T) {
	// Define the specific target GVK
	targetGVK := schema.GroupVersionKind{
		Group:   "workloads.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "RoleBasedGroup",
	}

	type args struct {
		obj       client.Object
		targetGVK schema.GroupVersionKind
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		// --------------------------
		// Basic Scenarios
		// --------------------------
		{
			name: "no owner references",
			args: args{
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						OwnerReferences: nil, // Explicit nil
					},
				},
				targetGVK: targetGVK,
			},
			want: false,
		},
		{
			name: "empty owner references",
			args: args{
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						OwnerReferences: []metav1.OwnerReference{}, // Empty slice
					},
				},
				targetGVK: targetGVK,
			},
			want: false,
		},
		{
			name: "owner reference exists but does not match",
			args: args{
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",    // Group mismatch
								Kind:       "Deployment", // Kind mismatch
							},
						},
					},
				},
				targetGVK: targetGVK,
			},
			want: false,
		},
		{
			name: "owner reference matches exactly",
			args: args{
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "workloads.x-k8s.io/v1alpha1", // Group/Version matches
								Kind:       "RoleBasedGroup",              // Kind matches
							},
						},
					},
				},
				targetGVK: targetGVK,
			},
			want: true,
		},

		// --------------------------
		// Edge Cases
		// --------------------------
		{
			name: "partial match in multiple owner references",
			args: args{
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "batch/v1",
								Kind:       "Job",
							},
							{
								APIVersion: "workloads.x-k8s.io/v1alpha1", // Matches
								Kind:       "RoleBasedGroup",
							},
						},
					},
				},
				targetGVK: targetGVK,
			},
			want: true, // Returns true if at least one matches
		},
		{
			name: "correct group/version but wrong kind",
			args: args{
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "workloads.x-k8s.io/v1alpha1",
								Kind:       "WrongKind", // Kind mismatch
							},
						},
					},
				},
				targetGVK: targetGVK,
			},
			want: false,
		},
		{
			name: "correct kind but wrong group",
			args: args{
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "wrong-group/v1alpha1", // Group mismatch
								Kind:       "RoleBasedGroup",       // Kind matches
							},
						},
					},
				},
				targetGVK: targetGVK,
			},
			want: false,
		},
		{
			name: "invalid apiVersion format",
			args: args{
				obj: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "workloads.x-k8s.io/v1alpha1/beta", // Invalid format
								Kind:       "RoleBasedGroup",
							},
						},
					},
				},
				targetGVK: targetGVK,
			},
			want: false, // Parsing fails → treated as non-match
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				if got := hasValidOwnerRef(tt.args.obj, tt.args.targetGVK); got != tt.want {
					t.Errorf("hasValidOwnerRef() = %v, want %v", got, tt.want)
				}
			},
		)
	}
}

func TestRoleBasedGroupReconciler_Reconcile(t *testing.T) {
	testScheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(testScheme)
	_ = workloadsv1alpha1.AddToScheme(testScheme)

	fakeRecorder := record.NewFakeRecorder(10)

	tests := []struct {
		name         string
		obj          []client.Object
		request      reconcile.Request
		wantErr      bool
		errorMessage string
	}{
		{
			name: "RBG not found",
			obj:  []client.Object{},
			request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "non-existent",
					Namespace: "default",
				},
			},
			wantErr: false, // Should not return error for not found
		},
		{
			name: "normal RBG",
			obj: []client.Object{
				wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
			},
			request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-rbg",
					Namespace: "default",
				},
			},
			wantErr: false,
		},
		{
			name: "RBG with orphaned roles",
			obj: []client.Object{
				wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
				&appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg-orphaned-role",
						Namespace: "default",
						Labels: map[string]string{
							workloadsv1alpha1.SetNameLabelKey: "test-rbg",
							workloadsv1alpha1.SetRoleLabelKey: "orphaned-role",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "workloads.x-k8s.io/v1alpha1",
								Kind:       "RoleBasedGroup",
								Name:       "test-rbg",
								Controller: ptr.To(true),
								UID:        "rbg-test-uid",
							},
						},
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: ptr.To(int32(2)),
						Template: wrappers.BuildBasicPodTemplateSpec().Obj(),
					},
				},
			},
			request: reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-rbg",
					Namespace: "default",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				fakeClient := fake.NewClientBuilder().
					WithScheme(testScheme).
					WithObjects(tt.obj...).
					Build()

				r := &RoleBasedGroupReconciler{
					client:    fakeClient,
					apiReader: fakeClient,
					scheme:    testScheme,
					recorder:  fakeRecorder,
				}

				logger := zap.New().WithValues("env", "unit-test")
				ctx := ctrl.LoggerInto(context.TODO(), logger)

				_, err := r.Reconcile(ctx, tt.request)

				if (err != nil) != tt.wantErr {
					t.Errorf("Reconcile() error = %v, wantErr %v", err, tt.wantErr)
				}

				if tt.wantErr && tt.errorMessage != "" {
					if err == nil || !strings.Contains(err.Error(), tt.errorMessage) {
						t.Errorf("Expected error containing '%s', got %v", tt.errorMessage, err)
					}
				}
			},
		)
	}
}

func TestRoleBasedGroupReconciler_ReconcileScalingAdapter(t *testing.T) {
	testScheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(testScheme)
	_ = workloadsv1alpha1.AddToScheme(testScheme)

	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			UID:       "test-uid",
		},
	}

	roleSpec := &workloadsv1alpha1.RoleSpec{
		Name: "test-role",
		ScalingAdapter: &workloadsv1alpha1.ScalingAdapter{
			Enable: true,
		},
	}

	tests := []struct {
		name         string
		obj          []client.Object
		wantErr      bool
		expectCreate bool
		expectDelete bool
	}{
		{
			name:         "Create scaling adapter when not exists",
			obj:          []client.Object{rbg},
			wantErr:      false,
			expectCreate: true,
		},
		{
			name: "Delete scaling adapter when disabled",
			obj: []client.Object{
				rbg,
				&workloadsv1alpha1.RoleBasedGroupScalingAdapter{
					ObjectMeta: metav1.ObjectMeta{
						Name:      scale.GenerateScalingAdapterName(rbg.Name, roleSpec.Name),
						Namespace: rbg.Namespace,
					},
				},
			},
			wantErr:      false,
			expectDelete: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				fakeClient := fake.NewClientBuilder().
					WithScheme(testScheme).
					WithObjects(tt.obj...).
					Build()

				r := &RoleBasedGroupReconciler{
					client:    fakeClient,
					apiReader: fakeClient,
					scheme:    testScheme,
				}

				roleSpec.ScalingAdapter.Enable = !tt.expectDelete

				err := r.ReconcileScalingAdapter(context.Background(), rbg, roleSpec)

				if (err != nil) != tt.wantErr {
					t.Errorf("ReconcileScalingAdapter() error = %v, wantErr %v", err, tt.wantErr)
				}

				// Verify creation/deletion
				adapterName := scale.GenerateScalingAdapterName(rbg.Name, roleSpec.Name)
				adapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{}
				err = fakeClient.Get(
					context.Background(), types.NamespacedName{
						Name:      adapterName,
						Namespace: rbg.Namespace,
					}, adapter,
				)

				if tt.expectCreate && err != nil {
					t.Errorf("Expected scaling adapter to be created, but got error: %v", err)
				}

				if tt.expectDelete && err == nil {
					t.Errorf("Expected scaling adapter to be deleted, but it still exists")
				}
			},
		)
	}
}

func TestRoleBasedGroupReconciler_CleanupOrphanedScalingAdapters(t *testing.T) {
	testScheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(testScheme)
	_ = workloadsv1alpha1.AddToScheme(testScheme)

	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()

	tests := []struct {
		name        string
		obj         []client.Object
		expectExist bool
	}{
		{
			name: "Delete orphaned scaling adapter",
			obj: []client.Object{
				rbg,
				&workloadsv1alpha1.RoleBasedGroupScalingAdapter{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg-scaling-role2-scaling-adapter",
						Namespace: "default",
						Labels: map[string]string{
							workloadsv1alpha1.SetRoleLabelKey: "role2",
							workloadsv1alpha1.SetNameLabelKey: "test-rbg",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "workloads.x-k8s.io/v1alpha1",
								Kind:       "RoleBasedGroup",
								Name:       "test-rbg",
								Controller: ptr.To[bool](true),
								UID:        "rbg-test-uid",
							},
						},
					},
					Spec: workloadsv1alpha1.RoleBasedGroupScalingAdapterSpec{
						ScaleTargetRef: &workloadsv1alpha1.AdapterScaleTargetRef{
							Name: "test-rbg",
							Role: "role2", // This role doesn't exist in RBG spec
						},
					},
				},
			},
			expectExist: false,
		},
		{
			name: "Keep valid scaling adapter",
			obj: []client.Object{
				rbg,
				&workloadsv1alpha1.RoleBasedGroupScalingAdapter{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg-test-role-scaling-adapter",
						Namespace: "default",
						Labels: map[string]string{
							workloadsv1alpha1.SetNameLabelKey: "test-rbg",
							workloadsv1alpha1.SetRoleLabelKey: "test-role",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "workloads.x-k8s.io/v1alpha1",
								Kind:       "RoleBasedGroup",
								Name:       "test-rbg",
								Controller: ptr.To[bool](true),
								UID:        "rbg-test-uid",
							},
						},
					},
					Spec: workloadsv1alpha1.RoleBasedGroupScalingAdapterSpec{
						ScaleTargetRef: &workloadsv1alpha1.AdapterScaleTargetRef{
							Name: "test-rbg",
							Role: "test-role", // This role exists in RBG spec
						},
					},
				},
			},
			expectExist: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				fakeClient := fake.NewClientBuilder().
					WithScheme(testScheme).
					WithObjects(tt.obj...).
					Build()

				r := &RoleBasedGroupReconciler{
					client:    fakeClient,
					apiReader: fakeClient,
					scheme:    testScheme,
				}

				err := r.CleanupOrphanedScalingAdapters(context.Background(), rbg)
				if err != nil {
					t.Fatalf("CleanupOrphanedScalingAdapters() error = %v", err)
				}

				// Check if adapter still exists
				adapters := &workloadsv1alpha1.RoleBasedGroupScalingAdapterList{}
				listErr := fakeClient.List(
					context.Background(), adapters, &client.ListOptions{
						Namespace: "default",
					},
				)
				if listErr != nil {
					t.Fatalf("Failed to list scaling adapters: %v", listErr)
				}

				adapterExists := false
				for _, adapter := range adapters.Items {
					if strings.Contains(adapter.Name, "scaling-adapter") {
						adapterExists = true
						break
					}
				}

				if tt.expectExist && !adapterExists {
					t.Errorf("Expected scaling adapter to exist, but it was deleted")
				}

				if !tt.expectExist && adapterExists {
					t.Errorf("Expected scaling adapter to be deleted, but it still exists")
				}
			},
		)
	}
}

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
	"math"
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
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
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

func Test_CalculateNextRollingTarget_WithNormalCases(t *testing.T) {
	type testCase struct {
		name            string
		maxSkew         string
		roles           sets.Set[string]
		desiredReplicas map[string]int32
		updatedReplicas map[string]int32
	}

	testCases := []testCase{
		{
			name:    "(200p, 100d): maxSkew=1%",
			maxSkew: "1%",
			roles:   sets.New[string]("prefill", "decode"),
			desiredReplicas: map[string]int32{
				"prefill": 200,
				"decode":  100,
			},
			updatedReplicas: map[string]int32{
				"prefill": 20,
				"decode":  0,
			},
		},
		{
			name:    "(200p, 100d): maxSkew=10%",
			maxSkew: "10%",
			roles:   sets.New[string]("prefill", "decode"),
			desiredReplicas: map[string]int32{
				"prefill": 200,
				"decode":  100,
			},
			updatedReplicas: map[string]int32{
				"prefill": 71,
				"decode":  53,
			},
		},
		{
			name:    "(3p, 1d): maxSkew=1%",
			maxSkew: "1%",
			roles:   sets.New[string]("prefill", "decode"),
			desiredReplicas: map[string]int32{
				"prefill": 3,
				"decode":  1,
			},
			updatedReplicas: map[string]int32{
				"prefill": 1,
				"decode":  1,
			},
		},
		{
			name:    "(7p, 5d): maxSkew=1%",
			maxSkew: "1%",
			roles:   sets.New[string]("prefill", "decode"),
			desiredReplicas: map[string]int32{
				"prefill": 7,
				"decode":  5,
			},
			updatedReplicas: map[string]int32{
				"prefill": 2,
				"decode":  1,
			},
		},
		{
			name:    "(1p, 1d): maxSkew=1%",
			maxSkew: "1%",
			roles:   sets.New[string]("prefill", "decode"),
			desiredReplicas: map[string]int32{
				"prefill": 1,
				"decode":  1,
			},
			updatedReplicas: map[string]int32{
				"prefill": 0,
				"decode":  0,
			},
		},
		{
			name:    "(10p, 10d): maxSkew=1%",
			maxSkew: "1%",
			roles:   sets.New[string]("prefill", "decode"),
			desiredReplicas: map[string]int32{
				"prefill": 10,
				"decode":  10,
			},
			updatedReplicas: map[string]int32{
				"prefill": 2,
				"decode":  0,
			},
		},
		{
			name:    "(10p, 10d, 10e): maxSkew=1%",
			maxSkew: "1%",
			roles:   sets.New[string]("prefill", "decode", "encoder"),
			desiredReplicas: map[string]int32{
				"prefill": 10,
				"decode":  10,
				"encoder": 10,
			},
			updatedReplicas: map[string]int32{
				"prefill": 1,
				"decode":  2,
				"encoder": 3,
			},
		},
		{
			name:    "(7p, 5d, 3e): maxSkew=1%",
			maxSkew: "1%",
			roles:   sets.New[string]("prefill", "decode", "encoder"),
			desiredReplicas: map[string]int32{
				"prefill": 7,
				"decode":  5,
				"encoder": 3,
			},
			updatedReplicas: map[string]int32{
				"prefill": 3,
				"decode":  1,
				"encoder": 1,
			},
		},
		{
			name:    "(7p, 5d, 3e): maxSkew=10%",
			maxSkew: "10%",
			roles:   sets.New[string]("prefill", "decode", "encoder"),
			desiredReplicas: map[string]int32{
				"prefill": 7,
				"decode":  5,
				"encoder": 3,
			},
			updatedReplicas: map[string]int32{
				"prefill": 1,
				"decode":  1,
				"encoder": 1,
			},
		},
		{
			name:    "(10p, 2d): maxSkew=10%",
			maxSkew: "10%",
			roles:   sets.New[string]("prefill", "decode"),
			desiredReplicas: map[string]int32{
				"prefill": 10,
				"decode":  2,
			},
			updatedReplicas: map[string]int32{
				"prefill": 0,
				"decode":  0,
			},
		},
	}

	GetSkewAllowedBias := func(desired map[string]int32) int {
		skewAllowedBias := 0
		for _, replicas := range desired {
			skewAllowed := int(math.Ceil(10000.0 / float64(replicas)))
			if skewAllowed > skewAllowedBias {
				skewAllowedBias = skewAllowed
			}
		}
		return skewAllowedBias
	}

	GetCurrentSkew := func(fastUpdated, slowUpdated, fastDesired, slowDesired int32) int {
		fastRatio := float64(fastUpdated) / float64(fastDesired)
		slowRatio := float64(slowUpdated) / float64(slowDesired)
		return int(math.Ceil(10000.0 * (fastRatio - slowRatio)))
	}

	CheckSkewSatisfied := func(fastUpdated, slowUpdated, fastDesired, slowDesired int32, maxSkew, skewAllowedBias int) bool {
		currentSkew := GetCurrentSkew(fastUpdated, slowUpdated, fastDesired, slowDesired)
		return currentSkew <= skewAllowedBias+maxSkew
	}

	CheckRollingSatisfied := func(updated, desired map[string]int32) bool {
		for role, replicas := range desired {
			if updated[role] < replicas {
				return false
			}
		}
		return true
	}

	for _, ts := range testCases {
		t.Run(ts.name, func(t *testing.T) {
			skewAllowedBias := GetSkewAllowedBias(ts.desiredReplicas)
			for {
				nextRollingTarget := calculateNextRollingTarget(&ts.maxSkew, ts.roles, ts.desiredReplicas, ts.updatedReplicas, ts.desiredReplicas)
				t.Logf("%s calculated next rolling target: %v", ts.name, nextRollingTarget)
				for role, newUpdated := range nextRollingTarget {
					ts.updatedReplicas[role] = newUpdated
				}

				maxSkew, _ := utils.ParseIntStrAsNonZero(intstr.FromString(ts.maxSkew), 10000)
				fast, slow := getFastestAndSlowestRole(ts.roles, ts.desiredReplicas, ts.updatedReplicas)
				if !CheckSkewSatisfied(ts.updatedReplicas[fast], ts.updatedReplicas[slow], ts.desiredReplicas[fast], ts.desiredReplicas[slow], int(maxSkew), skewAllowedBias) {
					t.Fatal("Skew is out of MaxSkew")
				}
				if CheckRollingSatisfied(ts.updatedReplicas, ts.desiredReplicas) {
					break
				}
			}
		})
	}

	for prefill := 1; prefill < 100; prefill++ {
		for decode := 1; decode < 100; decode++ {
			ts := testCase{
				name:    fmt.Sprintf("(%d p, %d d): maxSkew=1%%", prefill, decode),
				maxSkew: "1%",
				roles:   sets.New[string]("prefill", "decode"),
				desiredReplicas: map[string]int32{
					"prefill": int32(prefill),
					"decode":  int32(decode),
				},
				updatedReplicas: map[string]int32{
					"prefill": 0,
					"decode":  0,
				},
			}
			t.Run(ts.name, func(t *testing.T) {
				skewAllowedBias := GetSkewAllowedBias(ts.desiredReplicas)
				for {
					nextRollingTarget := calculateNextRollingTarget(&ts.maxSkew, ts.roles, ts.desiredReplicas, ts.updatedReplicas, ts.desiredReplicas)
					t.Logf("%s calculated next rolling target: %v", ts.name, nextRollingTarget)
					for role, newUpdated := range nextRollingTarget {
						ts.updatedReplicas[role] = newUpdated
					}

					maxSkew, _ := utils.ParseIntStrAsNonZero(intstr.FromString(ts.maxSkew), 10000)
					fast, slow := getFastestAndSlowestRole(ts.roles, ts.desiredReplicas, ts.updatedReplicas)
					if !CheckSkewSatisfied(ts.updatedReplicas[fast], ts.updatedReplicas[slow], ts.desiredReplicas[fast], ts.desiredReplicas[slow], int(maxSkew), skewAllowedBias) {
						t.Fatal("Skew is out of MaxSkew")
					}
					if CheckRollingSatisfied(ts.updatedReplicas, ts.desiredReplicas) {
						break
					}
				}
			})
		}
	}

	for prefill := 1; prefill < 20; prefill++ {
		for decode := 1; decode < 20; decode++ {
			for prefillUpdated := 1; prefillUpdated <= prefill; prefillUpdated++ {
				for decodeUpdated := 1; decodeUpdated <= decode; decodeUpdated++ {
					ts := testCase{
						name:    fmt.Sprintf("(%d p, %d d): maxSkew=1%%", prefill, decode),
						maxSkew: "1%",
						roles:   sets.New[string]("prefill", "decode"),
						desiredReplicas: map[string]int32{
							"prefill": int32(prefill),
							"decode":  int32(decode),
						},
						updatedReplicas: map[string]int32{
							"prefill": int32(prefillUpdated),
							"decode":  int32(decodeUpdated),
						},
					}
					t.Run(ts.name, func(t *testing.T) {
						skewAllowedBias := GetSkewAllowedBias(ts.desiredReplicas)
						for {
							nextRollingTarget := calculateNextRollingTarget(&ts.maxSkew, ts.roles, ts.desiredReplicas, ts.updatedReplicas, ts.desiredReplicas)
							t.Logf("%s calculated next rolling target: %v", ts.name, nextRollingTarget)
							for role, newUpdated := range nextRollingTarget {
								ts.updatedReplicas[role] = newUpdated
							}

							maxSkew, _ := utils.ParseIntStrAsNonZero(intstr.FromString(ts.maxSkew), 10000)
							fast, slow := getFastestAndSlowestRole(ts.roles, ts.desiredReplicas, ts.updatedReplicas)
							if !CheckSkewSatisfied(ts.updatedReplicas[fast], ts.updatedReplicas[slow], ts.desiredReplicas[fast], ts.desiredReplicas[slow], int(maxSkew), skewAllowedBias) {
								t.Fatal("Skew is out of MaxSkew")
							}
							if CheckRollingSatisfied(ts.updatedReplicas, ts.desiredReplicas) {
								break
							}
						}
					})
				}
			}
		}
	}
}

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

func Test_getFastestAndSlowestRole(t *testing.T) {
	tests := []struct {
		name              string
		coordinationRoles sets.Set[string]
		desiredReplicas   map[string]int32
		updatedReplicas   map[string]int32
		expectedFastest   string
		expectedSlowest   string
	}{
		{
			name:              "two roles with different ratios",
			coordinationRoles: sets.New[string]("role1", "role2"),
			desiredReplicas: map[string]int32{
				"role1": 100,
				"role2": 100,
			},
			updatedReplicas: map[string]int32{
				"role1": 50, // 50%
				"role2": 10, // 10%
			},
			expectedFastest: "role1",
			expectedSlowest: "role2",
		},
		{
			name:              "two roles with same ratio, different desired",
			coordinationRoles: sets.New[string]("role1", "role2"),
			desiredReplicas: map[string]int32{
				"role1": 200,
				"role2": 100,
			},
			updatedReplicas: map[string]int32{
				"role1": 100, // 50%
				"role2": 50,  // 50%
			},
			expectedFastest: "role2", // Same ratio, but role1 has more desired replicas
			expectedSlowest: "role1",
		},
		{
			name:              "three roles with different ratios",
			coordinationRoles: sets.New[string]("role1", "role2", "role3"),
			desiredReplicas: map[string]int32{
				"role1": 100,
				"role2": 100,
				"role3": 100,
			},
			updatedReplicas: map[string]int32{
				"role1": 80, // 80%
				"role2": 50, // 50%
				"role3": 20, // 20%
			},
			expectedFastest: "role1",
			expectedSlowest: "role3",
		},
		{
			name:              "small replicas with different ratios",
			coordinationRoles: sets.New[string]("role1", "role2"),
			desiredReplicas: map[string]int32{
				"role1": 10,
				"role2": 10,
			},
			updatedReplicas: map[string]int32{
				"role1": 7, // 70%
				"role2": 3, // 30%
			},
			expectedFastest: "role1",
			expectedSlowest: "role2",
		},
		{
			name:              "unequal desired replicas",
			coordinationRoles: sets.New[string]("role1", "role2"),
			desiredReplicas: map[string]int32{
				"role1": 200,
				"role2": 100,
			},
			updatedReplicas: map[string]int32{
				"role1": 20, // 10%
				"role2": 15, // 15%
			},
			expectedFastest: "role2",
			expectedSlowest: "role1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fastest, slowest := getFastestAndSlowestRole(tt.coordinationRoles, tt.desiredReplicas, tt.updatedReplicas)
			if fastest != tt.expectedFastest {
				t.Errorf("getFastestAndSlowestRole() fastest = %v, want %v", fastest, tt.expectedFastest)
			}
			if slowest != tt.expectedSlowest {
				t.Errorf("getFastestAndSlowestRole() slowest = %v, want %v", slowest, tt.expectedSlowest)
			}
		},
		)
	}
}

func Test_getFastestAndSlowestRole_EdgeCases(t *testing.T) {
	tests := []struct {
		name              string
		coordinationRoles sets.Set[string]
		desiredReplicas   map[string]int32
		updatedReplicas   map[string]int32
		expectedFastest   string
		expectedSlowest   string
	}{
		{
			name:              "single role",
			coordinationRoles: sets.New[string]("role1"),
			desiredReplicas: map[string]int32{
				"role1": 100,
			},
			updatedReplicas: map[string]int32{
				"role1": 50,
			},
			expectedFastest: "", // Should return empty for single role
			expectedSlowest: "",
		},
		{
			name:              "empty roles",
			coordinationRoles: sets.New[string](),
			desiredReplicas:   map[string]int32{},
			updatedReplicas:   map[string]int32{},
			expectedFastest:   "",
			expectedSlowest:   "",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				fastest, slowest := getFastestAndSlowestRole(tt.coordinationRoles, tt.desiredReplicas, tt.updatedReplicas)
				if fastest != tt.expectedFastest {
					t.Errorf("getFastestAndSlowestRole() fastest = %v, want %v", fastest, tt.expectedFastest)
				}
				if slowest != tt.expectedSlowest {
					t.Errorf("getFastestAndSlowestRole() slowest = %v, want %v", slowest, tt.expectedSlowest)
				}
			},
		)
	}
}

func Test_mergeStrategyRollingUpdate(t *testing.T) {
	tests := []struct {
		name        string
		strategiesA map[string]workloadsv1alpha1.RollingUpdate
		strategiesB map[string]workloadsv1alpha1.RollingUpdate
		expected    map[string]workloadsv1alpha1.RollingUpdate
	}{
		{
			name:        "merge empty maps",
			strategiesA: map[string]workloadsv1alpha1.RollingUpdate{},
			strategiesB: map[string]workloadsv1alpha1.RollingUpdate{},
			expected:    map[string]workloadsv1alpha1.RollingUpdate{},
		},
		{
			name: "merge with no overlap",
			strategiesA: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(5),
					Partition:      ptr.To(int32(10)),
				},
			},
			strategiesB: map[string]workloadsv1alpha1.RollingUpdate{
				"role2": {
					MaxUnavailable: intstr.FromInt(3),
					Partition:      ptr.To(int32(5)),
				},
			},
			expected: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(5),
					Partition:      ptr.To(int32(10)),
				},
				"role2": {
					MaxUnavailable: intstr.FromInt(3),
					Partition:      ptr.To(int32(5)),
				},
			},
		},
		{
			name: "merge with overlap, B has smaller maxUnavailable",
			strategiesA: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(10),
					Partition:      ptr.To(int32(5)),
				},
			},
			strategiesB: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(5),
					Partition:      ptr.To(int32(3)),
				},
			},
			expected: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(5), // Smaller value
					Partition:      ptr.To(int32(5)),  // Larger partition
				},
			},
		},
		{
			name: "merge with overlap, B has larger partition",
			strategiesA: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(5),
					Partition:      ptr.To(int32(3)),
				},
			},
			strategiesB: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(5),
					Partition:      ptr.To(int32(10)),
				},
			},
			expected: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(5),
					Partition:      ptr.To(int32(10)), // Larger partition
				},
			},
		},
		{
			name: "merge with percentage maxUnavailable",
			strategiesA: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromString("20%"),
					Partition:      ptr.To(int32(5)),
				},
			},
			strategiesB: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromString("10%"),
					Partition:      ptr.To(int32(3)),
				},
			},
			expected: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromString("10%"), // Smaller value
					Partition:      ptr.To(int32(5)),         // Larger partition
				},
			},
		},
		{
			name: "merge with nil partition",
			strategiesA: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(5),
					Partition:      nil,
				},
			},
			strategiesB: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(3),
					Partition:      ptr.To(int32(10)),
				},
			},
			expected: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(3),
					Partition:      ptr.To(int32(10)), // B's partition
				},
			},
		},
		{
			name: "merge multiple roles",
			strategiesA: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(10),
					Partition:      ptr.To(int32(5)),
				},
				"role2": {
					MaxUnavailable: intstr.FromInt(5),
					Partition:      ptr.To(int32(3)),
				},
			},
			strategiesB: map[string]workloadsv1alpha1.RollingUpdate{
				"role2": {
					MaxUnavailable: intstr.FromInt(3),
					Partition:      ptr.To(int32(10)),
				},
				"role3": {
					MaxUnavailable: intstr.FromInt(7),
					Partition:      ptr.To(int32(8)),
				},
			},
			expected: map[string]workloadsv1alpha1.RollingUpdate{
				"role1": {
					MaxUnavailable: intstr.FromInt(10),
					Partition:      ptr.To(int32(5)),
				},
				"role2": {
					MaxUnavailable: intstr.FromInt(3),
					Partition:      ptr.To(int32(10)),
				},
				"role3": {
					MaxUnavailable: intstr.FromInt(7),
					Partition:      ptr.To(int32(8)),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := mergeStrategyRollingUpdate(tt.strategiesA, tt.strategiesB)
				if len(result) != len(tt.expected) {
					t.Errorf("mergeStrategyRollingUpdate() result length = %v, want %v", len(result), len(tt.expected))
					return
				}
				for role, expectedStrategy := range tt.expected {
					actualStrategy, ok := result[role]
					if !ok {
						t.Errorf("mergeStrategyRollingUpdate() missing role %v", role)
						continue
					}
					if actualStrategy.MaxUnavailable.String() != expectedStrategy.MaxUnavailable.String() {
						t.Errorf("mergeStrategyRollingUpdate() MaxUnavailable for %v = %v, want %v", role, actualStrategy.MaxUnavailable, expectedStrategy.MaxUnavailable)
					}
					if (actualStrategy.Partition == nil) != (expectedStrategy.Partition == nil) {
						t.Errorf("mergeStrategyRollingUpdate() Partition nil for %v = %v, want %v", role, actualStrategy.Partition == nil, expectedStrategy.Partition == nil)
						continue
					}
					if actualStrategy.Partition != nil && expectedStrategy.Partition != nil {
						if *actualStrategy.Partition != *expectedStrategy.Partition {
							t.Errorf("mergeStrategyRollingUpdate() Partition for %v = %v, want %v", role, *actualStrategy.Partition, *expectedStrategy.Partition)
						}
					}
				}
			},
		)
	}
}

func Test_calculateCoordinationUpdatedReplicasBound(t *testing.T) {
	tests := []struct {
		name           string
		maxSkew        *intstr.IntOrString
		refUpdated     int32
		refDesired     int32
		requestDesired int32
		expectedLower  int32
		expectedUpper  int32
	}{
		{
			name:           "normal case with 10% maxSkew",
			maxSkew:        ptrToIntStr(intstr.FromString("10%")),
			refUpdated:     50,
			refDesired:     100,
			requestDesired: 100,
			expectedLower:  40, // (100*50*100 - 10*100*100) / (100*100) = 40
			expectedUpper:  60, // (10*100*100 + 100*50*100) / (100*100) = 60
		},
		{
			name:           "normal case with 1% maxSkew",
			maxSkew:        ptrToIntStr(intstr.FromString("1%")),
			refUpdated:     20,
			refDesired:     200,
			requestDesired: 100,
			expectedLower:  9,  // floor calculation
			expectedUpper:  11, // ceil calculation
		},
		{
			name:           "different desired replicas",
			maxSkew:        ptrToIntStr(intstr.FromString("5%")),
			refUpdated:     50,
			refDesired:     100,
			requestDesired: 200,
			expectedLower:  90,  // (100*50*200 - 5*100*200) / (100*100) = 90
			expectedUpper:  110, // (5*100*200 + 100*50*200) / (100*100) = 110
		},
		{
			name:           "zero refDesired",
			maxSkew:        ptrToIntStr(intstr.FromString("10%")),
			refUpdated:     0,
			refDesired:     0,
			requestDesired: 100,
			expectedLower:  0,
			expectedUpper:  0,
		},
		{
			name:           "small values",
			maxSkew:        ptrToIntStr(intstr.FromString("10%")),
			refUpdated:     1,
			refDesired:     10,
			requestDesired: 10,
			expectedLower:  0, // floor calculation
			expectedUpper:  2, // ceil calculation
		},
		{
			name:           "large maxSkew",
			maxSkew:        ptrToIntStr(intstr.FromString("50%")),
			refUpdated:     50,
			refDesired:     100,
			requestDesired: 100,
			expectedLower:  0,   // (100*50*100 - 50*100*100) / (100*100) = 0
			expectedUpper:  100, // (50*100*100 + 100*50*100) / (100*100) = 100
		},
		{
			name:           "refUpdated equals refDesired",
			maxSkew:        ptrToIntStr(intstr.FromString("10%")),
			refUpdated:     100,
			refDesired:     100,
			requestDesired: 100,
			expectedLower:  90,  // (100*100*100 - 10*100*100) / (100*100) = 90
			expectedUpper:  110, // (10*100*100 + 100*100*100) / (100*100) = 110
		},
		{
			name:           "zero refUpdated",
			maxSkew:        ptrToIntStr(intstr.FromString("10%")),
			refUpdated:     0,
			refDesired:     100,
			requestDesired: 100,
			expectedLower:  0,  // (100*0*100 - 10*100*100) / (100*100) = -10, max with 0 = 0
			expectedUpper:  10, // (10*100*100 + 100*0*100) / (100*100) = 10
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				lower, upper := calculateCoordinationUpdatedReplicasBound(*tt.maxSkew, tt.refUpdated, tt.refDesired, tt.requestDesired)
				if lower != tt.expectedLower {
					t.Errorf("calculateCoordinationUpdatedReplicasBound() lower = %v, want %v", lower, tt.expectedLower)
				}
				if upper != tt.expectedUpper {
					t.Errorf("calculateCoordinationUpdatedReplicasBound() upper = %v, want %v", upper, tt.expectedUpper)
				}
			},
		)
	}
}

// Helper function to create intstr.IntOrString pointer
func ptrToIntStr(v intstr.IntOrString) *intstr.IntOrString {
	return &v
}

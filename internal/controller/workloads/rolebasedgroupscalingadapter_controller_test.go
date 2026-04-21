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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	applyconfiguration "sigs.k8s.io/rbgs/client-go/applyconfiguration/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/wrappers"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func TestRoleBasedGroupScalingAdapterReconciler_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	rbgsa := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-adapter",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "workloads.x-k8s.io/v1alpha2",
					Kind:               "RoleBasedGroup",
					Name:               "test-rbg",
					UID:                "rbg-test-uid",
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			// scale replicas from 1 to 3
			Replicas: ptr.To(int32(3)),
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "test-rbg",
				Role: "test-role",
			},
		},
	}

	boundedRbgSA := rbgsa.DeepCopy()
	boundedRbgSA.Status.Phase = constants.AdapterPhaseBound

	// Create RBG with StatefulSet workload type to match the test StatefulSet
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("test-role").WithWorkload("apps/v1", "StatefulSet").Obj(),
		}).Obj()
	rbg.Status.RoleStatuses = []workloadsv1alpha2.RoleStatus{
		{Name: "test-role", ReadyReplicas: 2, Replicas: 3},
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-test-role",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "workloads.x-k8s.io/v1alpha2",
					Kind:               "RoleBasedGroup",
					Name:               "test-rbg",
					UID:                "rbg-test-uid",
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.GroupNameLabelKey: "test-rbg",
					constants.RoleNameLabelKey:  "test-role",
				},
			},
			Template: wrappers.BuildBasicPodTemplateSpec().Obj(),
		},
	}

	// Create an adapter referencing a role that doesn't exist in the RBG, to test the unbound (10s) requeue path
	unboundRbgSA := rbgsa.DeepCopy()
	unboundRbgSA.Spec.ScaleTargetRef.Role = "non-existent-role"

	rbgNoRoleStatuses := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("test-role").WithWorkload("apps/v1", "StatefulSet").Obj(),
		}).Obj()
	// no roleStatuses set

	unboundAdapterForNoRoleStatus := rbgsa.DeepCopy()

	// Define test cases
	tests := []struct {
		name                   string
		client                 client.Client
		expectError            bool
		expectRequeue          bool
		expectedRequeueAfter   time.Duration
		expectedPhase          constants.AdapterPhase
		expectedReplicas       *int32
		expectedReadyReplicas  *int32
		expectNilReadyReplicas bool
	}{
		{
			name:                  "adapter bound successfully",
			client:                fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbgsa, rbg, sts).Build(),
			expectError:           false,
			expectRequeue:         true,
			expectedRequeueAfter:  1 * time.Second,
			expectedPhase:         constants.AdapterPhaseBound,
			expectedReadyReplicas: ptr.To(int32(2)),
		},
		{
			name:                  "adapter bound without roleStatus initializes readyReplicas to 0",
			client:                fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(unboundAdapterForNoRoleStatus, rbgNoRoleStatuses, sts).Build(),
			expectError:           false,
			expectRequeue:         true,
			expectedRequeueAfter:  1 * time.Second,
			expectedPhase:         constants.AdapterPhaseBound,
			expectedReadyReplicas: ptr.To(int32(0)),
		},
		{
			name:                 "unbound adapter with missing role requeues after 10s",
			client:               fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(unboundRbgSA, rbg, sts).Build(),
			expectError:          false,
			expectRequeue:        true,
			expectedRequeueAfter: 10 * time.Second,
		},
		{
			name: "bounded adapter scales role replicas",
			client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(
				boundedRbgSA, rbg, sts,
			).Build(),
			expectError:   false,
			expectRequeue: false,
		},
		{
			// rbgsa can only be enabled by role.scalingAdapter.enable=True, therefore ignore non-existent rbg
			name:          "adapter with non-existent RBG should not return error",
			client:        fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbgsa).Build(),
			expectError:   false,
			expectRequeue: false,
		},
		{
			name: "mock client error",
			client: fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(
				interceptor.Funcs{
					Get: func(
						ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object,
						opts ...client.GetOption,
					) error {
						return apierrors.NewInternalError(fmt.Errorf("fake client get error"))
					},
				},
			).WithObjects(rbg, rbgsa, sts).Build(),
			expectError:   true,
			expectRequeue: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				reconciler := &RoleBasedGroupScalingAdapterReconciler{
					client:   tt.client,
					recorder: record.NewFakeRecorder(100),
				}
				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      rbgsa.Name,
						Namespace: rbgsa.Namespace,
					},
				}

				// Reconcile
				logger := zap.New().WithValues("env", "unit-test")
				ctx := ctrl.LoggerInto(context.TODO(), logger)
				result, err := reconciler.Reconcile(ctx, req)

				if (err != nil) != tt.expectError {
					t.Errorf("Reconcile() error = %v, expectedError %v", err, tt.expectError)
				}

				if tt.expectRequeue {
					assert.True(t, result.RequeueAfter > 0)
					if tt.expectedRequeueAfter > 0 {
						assert.Equal(t, tt.expectedRequeueAfter, result.RequeueAfter,
							"expected RequeueAfter to be %v, got %v", tt.expectedRequeueAfter, result.RequeueAfter)
					}
				} else {
					assert.False(t, result.RequeueAfter > 0)
					assert.Equal(t, int64(0), int64(result.RequeueAfter))
				}

				// Check status if expected
				if tt.expectedPhase != "" {
					updatedAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
					err := tt.client.Get(
						context.TODO(), types.NamespacedName{
							Name:      rbgsa.Name,
							Namespace: rbgsa.Namespace,
						}, updatedAdapter,
					)
					require.NoError(t, err)
					assert.Equal(t, tt.expectedPhase, updatedAdapter.Status.Phase)
				}

				if tt.expectedReplicas != nil {
					updatedAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
					err := tt.client.Get(
						context.TODO(), types.NamespacedName{
							Name:      rbgsa.Name,
							Namespace: rbgsa.Namespace,
						}, updatedAdapter,
					)
					require.NoError(t, err)
					assert.Equal(t, tt.expectedReplicas, updatedAdapter.Status.Replicas)
				}

				if tt.expectedReadyReplicas != nil {
					updatedAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
					err := tt.client.Get(
						context.TODO(), types.NamespacedName{
							Name:      rbgsa.Name,
							Namespace: rbgsa.Namespace,
						}, updatedAdapter,
					)
					require.NoError(t, err)
					assert.Equal(t, tt.expectedReadyReplicas, updatedAdapter.Status.ReadyReplicas)
				}

				if tt.expectNilReadyReplicas {
					updatedAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
					err := tt.client.Get(
						context.TODO(), types.NamespacedName{
							Name:      rbgsa.Name,
							Namespace: rbgsa.Namespace,
						}, updatedAdapter,
					)
					require.NoError(t, err)
					assert.Nil(t, updatedAdapter.Status.ReadyReplicas)
				}
			},
		)
	}
}

func TestRoleBasedGroupScalingAdapterReconciler_GetTargetRbgFromAdapter(t *testing.T) {
	// Create scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name        string
		adapter     *workloadsv1alpha2.RoleBasedGroupScalingAdapter
		rbg         *workloadsv1alpha2.RoleBasedGroup
		expectError bool
	}{
		{
			name: "adapter with valid target RBG should return RBG",
			adapter: &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-adapter",
					Namespace: "default",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
					ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
						Name: "test-rbg",
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
			},
		},
		{
			name: "adapter with invalid target RBG should return error",
			adapter: &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-adapter",
					Namespace: "default",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
					ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
						Name: "invalid-rbg",
					},
				},
			},
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Create fake client
				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(tt.adapter, tt.rbg).
					Build()

				// Create reconciler
				reconciler := &RoleBasedGroupScalingAdapterReconciler{
					client: fakeClient,
				}
				// Test GetTargetRbgFromAdapter
				result, err := reconciler.GetTargetRbgFromAdapter(context.TODO(), tt.adapter)
				if tt.expectError != (err != nil) {
					t.Errorf("Expected error: %v, got: %v", tt.expectError, err)
				}
				if err == nil {
					assert.Equal(t, "test-rbg", result.Name)
					assert.Equal(t, "default", result.Namespace)
				}
			},
		)
	}
}

func TestRoleBasedGroupScalingAdapterReconciler_UpdateAdapterOwnerReference(t *testing.T) {
	// Create scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Create test objects
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()

	adapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-adapter",
			Namespace: "default",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "test-rbg",
				Role: "worker",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(adapter).
		Build()

	reconciler := &RoleBasedGroupScalingAdapterReconciler{
		client: fakeClient,
	}

	// Test UpdateAdapterOwnerReference
	err := reconciler.UpdateAdapterOwnerReference(context.TODO(), adapter, rbg)
	require.NoError(t, err)

	// Check if owner reference was added
	updatedAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
	err = fakeClient.Get(
		context.TODO(), types.NamespacedName{
			Name:      "test-adapter",
			Namespace: "default",
		}, updatedAdapter,
	)
	require.NoError(t, err)

	assert.Len(t, updatedAdapter.OwnerReferences, 1)
	assert.Equal(t, "test-rbg", updatedAdapter.OwnerReferences[0].Name)
	assert.Equal(t, "RoleBasedGroup", updatedAdapter.OwnerReferences[0].Kind)
	assert.Equal(t, "workloads.x-k8s.io/v1alpha2", updatedAdapter.OwnerReferences[0].APIVersion)
	assert.Equal(t, "rbg-test-uid", string(updatedAdapter.OwnerReferences[0].UID))
}

func TestRBGScalingAdapterPredicate(t *testing.T) {
	predicate := RBGScalingAdapterPredicate()

	// Test CreateFunc
	createEvent := event.CreateEvent{
		Object: &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
		},
	}
	assert.True(t, predicate.CreateFunc(createEvent))

	// Test UpdateFunc with spec change
	oldAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "rbg1",
			},
		},
	}

	newAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "rbg2",
			},
		},
	}

	updateEvent := event.UpdateEvent{
		ObjectOld: oldAdapter,
		ObjectNew: newAdapter,
	}
	assert.True(t, predicate.UpdateFunc(updateEvent))

	// Test UpdateFunc without spec change
	newAdapter2 := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{Name: "test", ResourceVersion: "2"},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "rbg1",
			},
		},
	}

	updateEvent2 := event.UpdateEvent{
		ObjectOld: oldAdapter,
		ObjectNew: newAdapter2,
	}
	assert.False(t, predicate.UpdateFunc(updateEvent2))

	// Test DeleteFunc
	deleteEvent := event.DeleteEvent{
		Object: &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
		},
	}
	assert.True(t, predicate.DeleteFunc(deleteEvent))

	// Test GenericFunc
	genericEvent := event.GenericEvent{
		Object: &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
		},
	}
	assert.False(t, predicate.GenericFunc(genericEvent))
}

func TestRoleBasedGroupScalingAdapterReconciler_ReadyReplicasSync(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("test-role").WithWorkload("apps/v1", "StatefulSet").Obj(),
		}).Obj()
	rbg.Status.RoleStatuses = []workloadsv1alpha2.RoleStatus{
		{Name: "test-role", ReadyReplicas: 3, Replicas: 5},
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-test-role",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "workloads.x-k8s.io/v1alpha2",
					Kind:               "RoleBasedGroup",
					Name:               "test-rbg",
					UID:                "rbg-test-uid",
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(5)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.GroupNameLabelKey: "test-rbg",
					constants.RoleNameLabelKey:  "test-role",
				},
			},
			Template: wrappers.BuildBasicPodTemplateSpec().Obj(),
		},
	}

	baseBoundAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-adapter",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "workloads.x-k8s.io/v1alpha2",
					Kind:               "RoleBasedGroup",
					Name:               "test-rbg",
					UID:                "rbg-test-uid",
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			Replicas: ptr.To(int32(1)), // match role replicas to avoid triggering scale path
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "test-rbg",
				Role: "test-role",
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupScalingAdapterStatus{
			Phase: constants.AdapterPhaseBound,
		},
	}

	tests := []struct {
		name                   string
		adapterReadyReplicas   *int32
		expectedReadyReplicas  int32
		expectNilReadyReplicas bool
		clearRoleStatuses      bool
		clientOverride         client.Client
		expectError            bool
	}{
		{
			name:                  "sync readyReplicas from nil to 3",
			adapterReadyReplicas:  nil,
			expectedReadyReplicas: 3,
		},
		{
			name:                  "update readyReplicas from 1 to 3",
			adapterReadyReplicas:  ptr.To(int32(1)),
			expectedReadyReplicas: 3,
		},
		{
			name:                  "no update when readyReplicas already 3",
			adapterReadyReplicas:  ptr.To(int32(3)),
			expectedReadyReplicas: 3,
		},
		{
			name:                   "roleStatus not found skips readyReplicas update",
			adapterReadyReplicas:   ptr.To(int32(5)),
			expectNilReadyReplicas: false,
			expectedReadyReplicas:  5,
			clearRoleStatuses:      true,
		},
		{
			name:                 "patch error on readyReplicas sync returns error",
			adapterReadyReplicas: nil,
			expectError:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := baseBoundAdapter.DeepCopy()
			adapter.Status.ReadyReplicas = tt.adapterReadyReplicas

			// Use a RBG with no roleStatus for the clearRoleStatuses test
			testRBG := rbg
			if tt.clearRoleStatuses {
				noStatusRBG := rbg.DeepCopy()
				noStatusRBG.Status.RoleStatuses = nil
				testRBG = noStatusRBG
			}

			var fakeClient client.Client
			if tt.clientOverride != nil {
				fakeClient = tt.clientOverride
			} else if tt.expectError {
				fakeClient = fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(adapter, testRBG, sts).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							return fmt.Errorf("simulated patch error")
						},
					}).
					Build()
			} else {
				fakeClient = fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(adapter, testRBG, sts).
					Build()
			}

			reconciler := &RoleBasedGroupScalingAdapterReconciler{
				client:   fakeClient,
				recorder: record.NewFakeRecorder(100),
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      adapter.Name,
					Namespace: adapter.Namespace,
				},
			}

			logger := zap.New().WithValues("env", "unit-test")
			ctx := ctrl.LoggerInto(context.TODO(), logger)
			_, err := reconciler.Reconcile(ctx, req)

			if tt.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			updatedAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      adapter.Name,
				Namespace: adapter.Namespace,
			}, updatedAdapter)
			require.NoError(t, err)

			if tt.expectNilReadyReplicas {
				assert.Nil(t, updatedAdapter.Status.ReadyReplicas)
			} else {
				require.NotNil(t, updatedAdapter.Status.ReadyReplicas)
				assert.Equal(t, tt.expectedReadyReplicas, *updatedAdapter.Status.ReadyReplicas)
			}
		})
	}
}

func TestReadyReplicasSyncWithScale(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("test-role").WithWorkload("apps/v1", "StatefulSet").Obj(),
		}).Obj()
	rbg.Status.RoleStatuses = []workloadsv1alpha2.RoleStatus{
		{Name: "test-role", ReadyReplicas: 3, Replicas: 1},
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-test-role",
			Namespace: "default",
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.GroupNameLabelKey: "test-rbg",
					constants.RoleNameLabelKey:  "test-role",
				},
			},
			Template: wrappers.BuildBasicPodTemplateSpec().Obj(),
		},
	}

	adapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-adapter",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "workloads.x-k8s.io/v1alpha2",
					Kind:               "RoleBasedGroup",
					Name:               "test-rbg",
					UID:                "rbg-test-uid",
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			Replicas: ptr.To(int32(5)), // differs from role replicas=1 → triggers scale
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "test-rbg",
				Role: "test-role",
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupScalingAdapterStatus{
			Phase: constants.AdapterPhaseBound,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(adapter, rbg, sts).
		Build()

	reconciler := &RoleBasedGroupScalingAdapterReconciler{
		client:   fakeClient,
		recorder: record.NewFakeRecorder(100),
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      adapter.Name,
			Namespace: adapter.Namespace,
		},
	}

	logger := zap.New().WithValues("env", "unit-test")
	ctx := ctrl.LoggerInto(context.TODO(), logger)
	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	updatedAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      adapter.Name,
		Namespace: adapter.Namespace,
	}, updatedAdapter)
	require.NoError(t, err)

	// Both readyReplicas synced AND scale path executed
	require.NotNil(t, updatedAdapter.Status.ReadyReplicas, "readyReplicas should be preserved after scale")
	assert.Equal(t, int32(3), *updatedAdapter.Status.ReadyReplicas)
	require.NotNil(t, updatedAdapter.Status.Replicas, "status.Replicas should reflect scaled value")
	assert.Equal(t, int32(5), *updatedAdapter.Status.Replicas)
	assert.Equal(t, constants.AdapterPhaseBound, updatedAdapter.Status.Phase, "phase should be preserved after readyReplicas sync")
}

func TestScaleStatusPatchPreservesReadyReplicasFromLatestAdapter(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("test-role").WithWorkload("apps/v1", "StatefulSet").Obj(),
		}).Obj()
	// No roleStatuses: this forces reconcile to rely on the adapter's current status
	// when building the scale status patch.

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-test-role",
			Namespace: "default",
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.GroupNameLabelKey: "test-rbg",
					constants.RoleNameLabelKey:  "test-role",
				},
			},
			Template: wrappers.BuildBasicPodTemplateSpec().Obj(),
		},
	}

	latestAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-adapter",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "workloads.x-k8s.io/v1alpha2",
					Kind:               "RoleBasedGroup",
					Name:               "test-rbg",
					UID:                "rbg-test-uid",
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			Replicas: ptr.To(int32(5)),
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "test-rbg",
				Role: "test-role",
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupScalingAdapterStatus{
			Phase:         constants.AdapterPhaseBound,
			Replicas:      ptr.To(int32(1)),
			ReadyReplicas: ptr.To(int32(1)),
			Selector:      "app=test",
		},
	}

	staleAdapter := latestAdapter.DeepCopy()
	staleAdapter.Status.ReadyReplicas = nil

	var patchedReadyReplicas *int32
	cachedClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(latestAdapter.DeepCopy(), rbg.DeepCopy(), sts.DeepCopy()).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				adapter, ok := obj.(*workloadsv1alpha2.RoleBasedGroupScalingAdapter)
				if ok && key.Name == staleAdapter.Name && key.Namespace == staleAdapter.Namespace {
					staleAdapter.DeepCopyInto(adapter)
					return nil
				}
				return c.Get(ctx, key, obj, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if subResourceName == "status" {
					if obj.GetName() == latestAdapter.Name && obj.GetNamespace() == latestAdapter.Namespace {
						patchData, err := patch.Data(obj)
						require.NoError(t, err)

						var payload map[string]any
						require.NoError(t, json.Unmarshal(patchData, &payload))

						statusPayload, ok := payload["status"].(map[string]any)
						require.True(t, ok, "expected status payload in apply patch")

						rawReadyReplicas, ok := statusPayload["readyReplicas"]
						require.True(t, ok, "expected readyReplicas to be preserved in status patch")

						readyReplicasValue, ok := rawReadyReplicas.(float64)
						require.True(t, ok, "expected numeric readyReplicas in status patch")
						patchedReadyReplicas = ptr.To(int32(readyReplicasValue))
					}
				}
				return nil
			},
		}).
		Build()

	apiReader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(latestAdapter.DeepCopy(), rbg.DeepCopy(), sts.DeepCopy()).
		Build()

	reconciler := &RoleBasedGroupScalingAdapterReconciler{
		client:    cachedClient,
		apiReader: apiReader,
		recorder:  record.NewFakeRecorder(100),
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      latestAdapter.Name,
			Namespace: latestAdapter.Namespace,
		},
	}

	logger := zap.New().WithValues("env", "unit-test")
	ctx := ctrl.LoggerInto(context.TODO(), logger)
	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NotNil(t, patchedReadyReplicas, "expected scale status patch to preserve readyReplicas")
	assert.Equal(t, int32(1), *patchedReadyReplicas)
}

func TestToRoleBasedGroupScalingAdapterStatusPatchApplyConfiguration_ExcludesSpec(t *testing.T) {
	adapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-adapter",
			Namespace: "default",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			Replicas: ptr.To(int32(5)),
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "test-rbg",
				Role: "test-role",
			},
		},
	}

	applyConfig := ToRoleBasedGroupScalingAdapterStatusPatchApplyConfiguration(adapter).
		WithStatus(applyconfiguration.RoleBasedGroupScalingAdapterStatus().WithPhase(constants.AdapterPhaseBound))

	unstructuredConfig, err := runtime.DefaultUnstructuredConverter.ToUnstructured(applyConfig)
	require.NoError(t, err)

	_, hasSpec := unstructuredConfig["spec"]
	assert.False(t, hasSpec, "status patch apply configuration should not include spec")
}

func TestMapRBGToScalingAdapters(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()

	matchingAdapter1 := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "adapter-1",
			Namespace: "default",
			Labels:    map[string]string{constants.GroupNameLabelKey: "test-rbg"},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "test-rbg",
				Role: "role-a",
			},
		},
	}

	matchingAdapter2 := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "adapter-2",
			Namespace: "default",
			Labels:    map[string]string{constants.GroupNameLabelKey: "test-rbg"},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "test-rbg",
				Role: "role-b",
			},
		},
	}

	nonMatchingAdapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "adapter-other",
			Namespace: "default",
			Labels:    map[string]string{constants.GroupNameLabelKey: "other-rbg"},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha2.AdapterScaleTargetRef{
				Name: "other-rbg",
				Role: "role-x",
			},
		},
	}

	tests := []struct {
		name             string
		obj              client.Object
		clientBuilder    func() client.Client
		expectedRequests int
	}{
		{
			name: "maps RBG to matching adapters",
			obj:  rbg,
			clientBuilder: func() client.Client {
				return fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(rbg, matchingAdapter1, matchingAdapter2, nonMatchingAdapter).
					Build()
			},
			expectedRequests: 2,
		},
		{
			name: "returns nil for non-RBG object",
			obj:  &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a-pod", Namespace: "default"}},
			clientBuilder: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedRequests: 0,
		},
		{
			name: "returns nil on List error",
			obj:  rbg,
			clientBuilder: func() client.Client {
				return fake.NewClientBuilder().
					WithScheme(scheme).
					WithInterceptorFuncs(interceptor.Funcs{
						List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
							return fmt.Errorf("simulated list error")
						},
					}).
					Build()
			},
			expectedRequests: 0,
		},
		{
			name: "returns nil when no adapters exist",
			obj:  rbg,
			clientBuilder: func() client.Client {
				return fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(rbg).
					Build()
			},
			expectedRequests: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := tt.clientBuilder()
			reconciler := &RoleBasedGroupScalingAdapterReconciler{
				client: fakeClient,
			}

			logger := zap.New().WithValues("env", "unit-test")
			ctx := ctrl.LoggerInto(context.TODO(), logger)
			requests := reconciler.mapRBGToScalingAdapters(ctx, tt.obj)

			assert.Len(t, requests, tt.expectedRequests)
		})
	}
}

func TestRBGRoleStatusPredicate(t *testing.T) {
	pred := RBGRoleStatusPredicate()

	t.Run("UpdateFunc", func(t *testing.T) {
		tests := []struct {
			name     string
			event    event.UpdateEvent
			expected bool
		}{
			{
				name: "returns true when RoleStatuses change",
				event: event.UpdateEvent{
					ObjectOld: &workloadsv1alpha2.RoleBasedGroup{
						Status: workloadsv1alpha2.RoleBasedGroupStatus{
							RoleStatuses: []workloadsv1alpha2.RoleStatus{
								{Name: "role-a", ReadyReplicas: 2},
							},
						},
					},
					ObjectNew: &workloadsv1alpha2.RoleBasedGroup{
						Status: workloadsv1alpha2.RoleBasedGroupStatus{
							RoleStatuses: []workloadsv1alpha2.RoleStatus{
								{Name: "role-a", ReadyReplicas: 3},
							},
						},
					},
				},
				expected: true,
			},
			{
				name: "returns false when RoleStatuses unchanged",
				event: event.UpdateEvent{
					ObjectOld: &workloadsv1alpha2.RoleBasedGroup{
						Status: workloadsv1alpha2.RoleBasedGroupStatus{
							RoleStatuses: []workloadsv1alpha2.RoleStatus{
								{Name: "role-a", ReadyReplicas: 2},
							},
						},
					},
					ObjectNew: &workloadsv1alpha2.RoleBasedGroup{
						Status: workloadsv1alpha2.RoleBasedGroupStatus{
							RoleStatuses: []workloadsv1alpha2.RoleStatus{
								{Name: "role-a", ReadyReplicas: 2},
							},
						},
					},
				},
				expected: false,
			},
			{
				name: "returns false for non-RBG types",
				event: event.UpdateEvent{
					ObjectOld: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old"}},
					ObjectNew: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "new"}},
				},
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assert.Equal(t, tt.expected, pred.UpdateFunc(tt.event))
			})
		}
	})

	t.Run("CreateFunc", func(t *testing.T) {
		assert.False(t, pred.CreateFunc(event.CreateEvent{
			Object: &workloadsv1alpha2.RoleBasedGroup{},
		}))
	})

	t.Run("DeleteFunc", func(t *testing.T) {
		assert.False(t, pred.DeleteFunc(event.DeleteEvent{
			Object: &workloadsv1alpha2.RoleBasedGroup{},
		}))
	})

	t.Run("GenericFunc", func(t *testing.T) {
		assert.False(t, pred.GenericFunc(event.GenericEvent{
			Object: &workloadsv1alpha2.RoleBasedGroup{},
		}))
	})
}

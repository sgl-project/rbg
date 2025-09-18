package workloads

import (
	"context"
	"fmt"
	"testing"

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
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func TestRoleBasedGroupScalingAdapterReconciler_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	rbgsa := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-adapter",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "workloads.x-k8s.io/v1alpha1",
					Kind:               "RoleBasedGroup",
					Name:               "test-rbg",
					UID:                "rbg-test-uid",
					BlockOwnerDeletion: ptr.To(true),
				},
			},
		},
		Spec: workloadsv1alpha1.RoleBasedGroupScalingAdapterSpec{
			// scale replicas from 1 to 3
			Replicas: ptr.To(int32(3)),
			ScaleTargetRef: &workloadsv1alpha1.AdapterScaleTargetRef{
				Name: "test-rbg",
				Role: "test-role",
			},
		},
	}

	boundedRbgSA := rbgsa.DeepCopy()
	boundedRbgSA.Status.Phase = workloadsv1alpha1.AdapterPhaseBound

	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-test-role",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "workloads.x-k8s.io/v1alpha1",
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
					workloadsv1alpha1.SetNameLabelKey: "test-rbg",
					workloadsv1alpha1.SetRoleLabelKey: "test-role",
				},
			},
			Template: wrappers.BuildBasicPodTemplateSpec().Obj(),
		},
	}

	// Define test cases
	tests := []struct {
		name             string
		client           client.Client
		expectError      bool
		expectRequeue    bool
		expectedPhase    workloadsv1alpha1.AdapterPhase
		expectedReplicas *int32
	}{
		{
			name:          "adapter bound successfully",
			client:        fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbgsa, rbg, sts).Build(),
			expectError:   false,
			expectRequeue: true,
			expectedPhase: workloadsv1alpha1.AdapterPhaseBound,
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
				} else {
					assert.False(t, result.RequeueAfter > 0)
					assert.Equal(t, int64(0), int64(result.RequeueAfter))
				}

				// Check status if expected
				if tt.expectedPhase != "" {
					updatedAdapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{}
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
					updatedAdapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{}
					err := tt.client.Get(
						context.TODO(), types.NamespacedName{
							Name:      rbgsa.Name,
							Namespace: rbgsa.Namespace,
						}, updatedAdapter,
					)
					require.NoError(t, err)
					assert.Equal(t, tt.expectedReplicas, updatedAdapter.Status.Replicas)
				}
			},
		)
	}
}

func TestRoleBasedGroupScalingAdapterReconciler_GetTargetRbgFromAdapter(t *testing.T) {
	// Create scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name        string
		adapter     *workloadsv1alpha1.RoleBasedGroupScalingAdapter
		rbg         *workloadsv1alpha1.RoleBasedGroup
		expectError bool
	}{
		{
			name: "adapter with valid target RBG should return RBG",
			adapter: &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-adapter",
					Namespace: "default",
				},
				Spec: workloadsv1alpha1.RoleBasedGroupScalingAdapterSpec{
					ScaleTargetRef: &workloadsv1alpha1.AdapterScaleTargetRef{
						Name: "test-rbg",
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
			},
		},
		{
			name: "adapter with invalid target RBG should return error",
			adapter: &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-adapter",
					Namespace: "default",
				},
				Spec: workloadsv1alpha1.RoleBasedGroupScalingAdapterSpec{
					ScaleTargetRef: &workloadsv1alpha1.AdapterScaleTargetRef{
						Name: "invalid-rbg",
					},
				},
			},
			rbg: &workloadsv1alpha1.RoleBasedGroup{
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
	_ = workloadsv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Create test objects
	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()

	adapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-adapter",
			Namespace: "default",
		},
		Spec: workloadsv1alpha1.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha1.AdapterScaleTargetRef{
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
	updatedAdapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{}
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
	assert.Equal(t, "workloads.x-k8s.io/v1alpha1", updatedAdapter.OwnerReferences[0].APIVersion)
	assert.Equal(t, "rbg-test-uid", string(updatedAdapter.OwnerReferences[0].UID))
}

func TestRBGScalingAdapterPredicate(t *testing.T) {
	predicate := RBGScalingAdapterPredicate()

	// Test CreateFunc
	createEvent := event.CreateEvent{
		Object: &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
		},
	}
	assert.True(t, predicate.CreateFunc(createEvent))

	// Test UpdateFunc with spec change
	oldAdapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: workloadsv1alpha1.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha1.AdapterScaleTargetRef{
				Name: "rbg1",
			},
		},
	}

	newAdapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: workloadsv1alpha1.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha1.AdapterScaleTargetRef{
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
	newAdapter2 := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{Name: "test", ResourceVersion: "2"},
		Spec: workloadsv1alpha1.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha1.AdapterScaleTargetRef{
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
		Object: &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
		},
	}
	assert.True(t, predicate.DeleteFunc(deleteEvent))

	// Test GenericFunc
	genericEvent := event.GenericEvent{
		Object: &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
		},
	}
	assert.False(t, predicate.GenericFunc(genericEvent))
}

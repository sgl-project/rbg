package reconciler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestConstructRoleStatue(t *testing.T) {
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
		Status: workloadsv1alpha1.RoleBasedGroupStatus{
			RoleStatuses: []workloadsv1alpha1.RoleStatus{
				{
					Name:            "test-role",
					Replicas:        3,
					ReadyReplicas:   2,
					UpdatedReplicas: 1,
				},
			},
		},
	}

	role := &workloadsv1alpha1.RoleSpec{
		Name:     "test-role",
		Replicas: ptr.To(int32(3)),
	}

	tests := []struct {
		name             string
		currentReplicas  int32
		currentReady     int32
		updatedReplicas  int32
		expectedUpdate   bool
		expectedReplicas int32
		expectedReady    int32
		expectedUpdated  int32
	}{
		{
			name:             "status unchanged",
			currentReplicas:  3,
			currentReady:     2,
			updatedReplicas:  1,
			expectedUpdate:   false,
			expectedReplicas: 3,
			expectedReady:    2,
			expectedUpdated:  1,
		},
		{
			name:             "status changed",
			currentReplicas:  5,
			currentReady:     4,
			updatedReplicas:  3,
			expectedUpdate:   true,
			expectedReplicas: 5,
			expectedReady:    4,
			expectedUpdated:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, updateStatus := ConstructRoleStatue(rbg, role, tt.currentReplicas, tt.currentReady, tt.updatedReplicas)

			assert.Equal(t, tt.expectedUpdate, updateStatus)
			assert.Equal(t, role.Name, status.Name)
			assert.Equal(t, tt.expectedReplicas, status.Replicas)
			assert.Equal(t, tt.expectedReady, status.ReadyReplicas)
			assert.Equal(t, tt.expectedUpdated, status.UpdatedReplicas)
		})
	}
}

func TestCleanupOrphanedObjs(t *testing.T) {
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			UID:       "rbg-uid",
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				{
					Name: "valid-role",
					Workload: workloadsv1alpha1.WorkloadSpec{
						APIVersion: "workloads.x-k8s.io/v1alpha1",
						Kind:       "InstanceSet",
					},
				},
			},
		},
	}

	// Create valid object (should not be deleted)
	validObj := &unstructured.Unstructured{}
	validObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "workloads.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "InstanceSet",
	})
	validObj.SetName("test-rbg-valid-role")
	validObj.SetNamespace("default")
	validObj.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: "workloads.x-k8s.io/v1alpha1",
			Kind:       "RoleBasedGroup",
			Name:       "test-rbg",
			UID:        "rbg-uid",
			Controller: ptr.To(true),
		},
	})
	validObj.SetLabels(map[string]string{
		"rolebasedgroup.workloads.x-k8s.io/name": "test-rbg",
	})

	orphanedObj := &unstructured.Unstructured{}
	orphanedObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "workloads.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "InstanceSet",
	})
	orphanedObj.SetName("test-rbg-orphaned-role")
	orphanedObj.SetNamespace("default")
	orphanedObj.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion: "workloads.x-k8s.io/v1alpha1",
			Kind:       "RoleBasedGroup",
			Name:       "test-rbg",
			UID:        "rbg-uid",
			Controller: ptr.To(true),
		},
	})
	orphanedObj.SetLabels(map[string]string{
		"rolebasedgroup.workloads.x-k8s.io/name": "test-rbg",
	})

	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(validObj, orphanedObj).
		Build()

	gvk := schema.GroupVersionKind{
		Group:   "workloads.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "InstanceSet",
	}

	// Execute cleanup
	err := CleanupOrphanedObjs(context.Background(), fakeClient, rbg, gvk)
	assert.NoError(t, err)

	// Verify valid object still exists
	existingValidObj := &unstructured.Unstructured{}
	existingValidObj.SetGroupVersionKind(gvk)
	err = fakeClient.Get(context.Background(),
		client.ObjectKey{Name: "test-rbg-valid-role", Namespace: "default"},
		existingValidObj)
	assert.NoError(t, err)

	// Verify orphaned object was deleted
	deletedObj := &unstructured.Unstructured{}
	deletedObj.SetGroupVersionKind(gvk)
	err = fakeClient.Get(context.Background(),
		client.ObjectKey{Name: "test-rbg-orphaned-role", Namespace: "default"},
		deletedObj)
	assert.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err))
}

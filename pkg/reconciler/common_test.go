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

package reconciler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func TestConstructRoleStatue(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
		Status: workloadsv1alpha2.RoleBasedGroupStatus{
			RoleStatuses: []workloadsv1alpha2.RoleStatus{
				{
					Name:            "test-role",
					Replicas:        3,
					ReadyReplicas:   2,
					UpdatedReplicas: 1,
				},
			},
		},
	}

	role := &workloadsv1alpha2.RoleSpec{
		Name:     "test-role",
		Replicas: ptr.To(int32(3)),
	}

	tests := []struct {
		name             string
		currentReplicas  int32
		currentReady     int32
		updatedReplicas  int32
		expectedReplicas int32
		expectedReady    int32
		expectedUpdated  int32
	}{
		{
			name:             "status unchanged",
			currentReplicas:  3,
			currentReady:     2,
			updatedReplicas:  1,
			expectedReplicas: 3,
			expectedReady:    2,
			expectedUpdated:  1,
		},
		{
			name:             "status changed",
			currentReplicas:  5,
			currentReady:     4,
			updatedReplicas:  3,
			expectedReplicas: 5,
			expectedReady:    4,
			expectedUpdated:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := ConstructRoleStatue(rbg, role, tt.currentReplicas, tt.currentReady, tt.updatedReplicas)

			assert.Equal(t, role.Name, status.Name)
			assert.Equal(t, tt.expectedReplicas, status.Replicas)
			assert.Equal(t, tt.expectedReady, status.ReadyReplicas)
			assert.Equal(t, tt.expectedUpdated, status.UpdatedReplicas)
		})
	}
}

func TestCleanupOrphanedObjs(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			UID:       "rbg-uid",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{
					Name: "valid-role",
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: "workloads.x-k8s.io/v1alpha1/InstanceSet",
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
		constants.GroupNameLabelKey: "test-rbg",
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
		constants.GroupNameLabelKey: "test-rbg",
	})

	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)

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

const (
	claimTestPreviousUID = types.UID("previous-rbg-uid")
	claimTestCurrentUID  = types.UID("current-rbg-uid")
)

// claimTestWorkload builds one workload kind at the name its role maps to, already fully ready.
type claimTestWorkload struct {
	kind       string
	role       workloadsv1alpha2.RoleSpec
	object     func(meta metav1.ObjectMeta) client.Object
	empty      func() client.Object
	reconciler func(s *runtime.Scheme, c client.Client) WorkloadReconciler
}

func claimTestWorkloads() []claimTestWorkload {
	return []claimTestWorkload{
		{
			kind: "RoleInstanceSet",
			role: wrappersv2.BuildStandaloneRole("worker").Obj(),
			object: func(meta metav1.ObjectMeta) client.Object {
				return &workloadsv1alpha2.RoleInstanceSet{
					ObjectMeta: meta,
					Spec:       workloadsv1alpha2.RoleInstanceSetSpec{Replicas: ptr.To(int32(1))},
					Status: workloadsv1alpha2.RoleInstanceSetStatus{
						ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1,
					},
				}
			},
			empty: func() client.Object { return &workloadsv1alpha2.RoleInstanceSet{} },
			reconciler: func(s *runtime.Scheme, c client.Client) WorkloadReconciler {
				return NewRoleInstanceSetReconciler(s, c)
			},
		},
		{
			kind: "Deployment",
			role: wrappersv2.BuildStandaloneRole("worker").WithWorkload("apps/v1", "Deployment").Obj(),
			object: func(meta metav1.ObjectMeta) client.Object {
				return &appsv1.Deployment{
					ObjectMeta: meta,
					Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(1))},
					Status: appsv1.DeploymentStatus{
						ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1,
					},
				}
			},
			empty: func() client.Object { return &appsv1.Deployment{} },
			reconciler: func(s *runtime.Scheme, c client.Client) WorkloadReconciler {
				return NewDeploymentReconciler(s, c)
			},
		},
		{
			kind: "StatefulSet",
			role: wrappersv2.BuildStandaloneRole("worker").WithWorkload("apps/v1", "StatefulSet").Obj(),
			object: func(meta metav1.ObjectMeta) client.Object {
				return &appsv1.StatefulSet{
					ObjectMeta: meta,
					Spec:       appsv1.StatefulSetSpec{Replicas: ptr.To(int32(1))},
					Status: appsv1.StatefulSetStatus{
						ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1,
					},
				}
			},
			empty: func() client.Object { return &appsv1.StatefulSet{} },
			reconciler: func(s *runtime.Scheme, c client.Client) WorkloadReconciler {
				return NewStatefulSetReconciler(s, c)
			},
		},
		{
			kind: "LeaderWorkerSet",
			role: wrappersv2.BuildLeaderWorkerRole("worker").Obj(),
			object: func(meta metav1.ObjectMeta) client.Object {
				return &lwsv1.LeaderWorkerSet{
					ObjectMeta: meta,
					Spec:       lwsv1.LeaderWorkerSetSpec{Replicas: ptr.To(int32(1))},
					Status:     lwsv1.LeaderWorkerSetStatus{Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1},
				}
			},
			empty: func() client.Object { return &lwsv1.LeaderWorkerSet{} },
			reconciler: func(s *runtime.Scheme, c client.Client) WorkloadReconciler {
				return NewLeaderWorkerSetReconciler(s, c)
			},
		},
	}
}

func claimTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, lwsv1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	return scheme
}

func claimTestControllerRef(rbg *workloadsv1alpha2.RoleBasedGroup, uid types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         workloadsv1alpha2.GroupVersion.String(),
		Kind:               "RoleBasedGroup",
		Name:               rbg.Name,
		UID:                uid,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}

// claimTestOwnedBy places a pre-created workload under rbg's control, for fixtures that stand in
// for a workload the RBG already manages.
func claimTestOwnedBy(rbg *workloadsv1alpha2.RoleBasedGroup) []metav1.OwnerReference {
	return []metav1.OwnerReference{claimTestControllerRef(rbg, rbg.UID)}
}

// TestWorkloadReconcilers_DoNotClaimWorkloadTheyDoNotControl covers a workload that exists under
// the name a role maps to but belongs to someone else: one left behind by a same-named RBG
// deleted in the background, or one that no controller owns at all.
func TestWorkloadReconcilers_DoNotClaimWorkloadTheyDoNotControl(t *testing.T) {
	scheme := claimTestScheme(t)

	for _, wl := range claimTestWorkloads() {
		cases := []struct {
			name   string
			owners func(rbg *workloadsv1alpha2.RoleBasedGroup) []metav1.OwnerReference
		}{
			{
				name: "left behind by a previous RBG of the same name",
				owners: func(rbg *workloadsv1alpha2.RoleBasedGroup) []metav1.OwnerReference {
					return []metav1.OwnerReference{claimTestControllerRef(rbg, claimTestPreviousUID)}
				},
			},
			{
				name:   "orphaned, with no controller at all",
				owners: func(*workloadsv1alpha2.RoleBasedGroup) []metav1.OwnerReference { return nil },
			},
		}

		for _, tc := range cases {
			t.Run(wl.kind+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				role := wl.role
				rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
					WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
				rbg.UID = claimTestCurrentUID

				meta := metav1.ObjectMeta{
					Name:            rbg.GetWorkloadName(&role),
					Namespace:       rbg.Namespace,
					Generation:      1,
					OwnerReferences: tc.owners(rbg),
				}
				c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(wl.object(meta)).Build()
				rec := wl.reconciler(scheme, c)

				status, err := rec.ConstructRoleStatus(ctx, rbg, &role)
				require.NoError(t, err)
				assert.Zero(t, status.ReadyReplicas, "readiness must not be read from a workload the RBG does not control")
				assert.Zero(t, status.Replicas)

				ready, err := rec.CheckWorkloadReady(ctx, rbg, &role)
				require.NoError(t, err)
				assert.False(t, ready, "dependents must not start on a workload the RBG does not control")

				assert.Error(t, rec.Reconciler(ctx, rbg, &role, nil, expectedRevisionHash),
					"the role must wait for the workload to be released instead of taking it over")

				got := wl.empty()
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(wl.object(meta)), got))
				assert.Equal(t, tc.owners(rbg), got.GetOwnerReferences(), "the workload must not be adopted")
			})
		}
	}
}

// TestWorkloadReconcilers_ManageTheirOwnWorkload is the counterpart guard: a workload the RBG does
// control is read and applied as usual, so the ownership check cannot degrade into refusing
// everything.
func TestWorkloadReconcilers_ManageTheirOwnWorkload(t *testing.T) {
	scheme := claimTestScheme(t)

	for _, wl := range claimTestWorkloads() {
		t.Run(wl.kind, func(t *testing.T) {
			ctx := context.Background()
			role := wl.role
			rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
			rbg.UID = claimTestCurrentUID

			owned := wl.object(metav1.ObjectMeta{
				Name:            rbg.GetWorkloadName(&role),
				Namespace:       rbg.Namespace,
				Generation:      1,
				OwnerReferences: []metav1.OwnerReference{claimTestControllerRef(rbg, claimTestCurrentUID)},
			})
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owned).Build()
			rec := wl.reconciler(scheme, c)

			status, err := rec.ConstructRoleStatus(ctx, rbg, &role)
			require.NoError(t, err)
			assert.Equal(t, int32(1), status.ReadyReplicas)

			ready, err := rec.CheckWorkloadReady(ctx, rbg, &role)
			require.NoError(t, err)
			assert.True(t, ready)

			require.NoError(t, rec.Reconciler(ctx, rbg, &role, nil, expectedRevisionHash))

			got := wl.empty()
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(owned), got))
			controller := metav1.GetControllerOf(got)
			require.NotNil(t, controller)
			assert.Equal(t, claimTestCurrentUID, controller.UID)
		})
	}
}

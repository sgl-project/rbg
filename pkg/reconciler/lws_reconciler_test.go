package reconciler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/wrappers"
)

// TestLeaderWorkerSetReconciler_Reconciler tests the Reconciler method
func TestLeaderWorkerSetReconciler_Reconciler(t *testing.T) {
	// Create a scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = lwsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	// Create test objects
	lwsRole := wrappers.BuildLwsRole("test-role").Obj()
	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha1.RoleSpec{wrappers.BuildLwsRole("test-role").Obj()}).Obj()

	// Create a fake client with initial objects
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Create reconciler
	reconciler := NewLeaderWorkerSetReconciler(scheme, fakeClient)

	// Test successful reconciliation
	ctx := context.Background()
	expectedRevisionHash := "revision-hash-value"
	err := reconciler.Reconciler(ctx, rbg, &lwsRole, nil, expectedRevisionHash)
	assert.NoError(t, err)

	// Verify LWS was created
	lws := &lwsv1.LeaderWorkerSet{}
	err = fakeClient.Get(
		ctx, types.NamespacedName{
			Name:      rbg.GetWorkloadName(&lwsRole),
			Namespace: rbg.Namespace,
		}, lws,
	)
	assert.NoError(t, err)
	assert.Equal(t, int32(1), *lws.Spec.Replicas)
	assert.Equal(t, int32(2), *lws.Spec.LeaderWorkerTemplate.Size)
	assert.Equal(t, expectedRevisionHash, lws.Labels[fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, lwsRole.Name)])
}

// TestLeaderWorkerSetReconciler_ConstructRoleStatus tests the ConstructRoleStatus method
func TestLeaderWorkerSetReconciler_ConstructRoleStatus(t *testing.T) {
	// Create a scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = lwsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	// Create test objects
	lwsRole := wrappers.BuildLwsRole("test-role").Obj()
	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha1.RoleSpec{lwsRole}).Obj()

	// Create LWS with status
	lws := &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.GetWorkloadName(&lwsRole),
			Namespace: rbg.Namespace,
		},
		Status: lwsv1.LeaderWorkerSetStatus{
			Replicas:      5,
			ReadyReplicas: 3,
		},
	}

	// Create a fake client with initial objects
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(lws).Build()

	// Create reconciler
	reconciler := NewLeaderWorkerSetReconciler(scheme, fakeClient)

	// Test status construction
	ctx := context.Background()
	status, updateStatus, err := reconciler.ConstructRoleStatus(ctx, rbg, &lwsRole)
	assert.NoError(t, err)
	assert.True(t, updateStatus) // Should be true since there was no previous status
	assert.Equal(t, "test-role", status.Name)
	assert.Equal(t, int32(5), status.Replicas)
	assert.Equal(t, int32(3), status.ReadyReplicas)

	// Add status to RBG and test again
	rbg.Status = workloadsv1alpha1.RoleBasedGroupStatus{
		RoleStatuses: []workloadsv1alpha1.RoleStatus{status},
	}

	// Test when status is the same (should not need update)
	status2, updateStatus2, err := reconciler.ConstructRoleStatus(ctx, rbg, &lwsRole)
	assert.NoError(t, err)
	assert.False(t, updateStatus2) // Should be false since status is the same
	assert.Equal(t, status, status2)
}

// TestLeaderWorkerSetReconciler_CheckWorkloadReady tests the CheckWorkloadReady method
func TestLeaderWorkerSetReconciler_CheckWorkloadReady(t *testing.T) {
	// Create a scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = lwsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	// Create test objects
	lwsRole := wrappers.BuildLwsRole("test-role").Obj()
	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha1.RoleSpec{lwsRole}).Obj()

	tests := []struct {
		name        string
		lwsStatus   lwsv1.LeaderWorkerSetStatus
		expected    bool
		expectError bool
	}{
		{
			name: "All replicas ready",
			lwsStatus: lwsv1.LeaderWorkerSetStatus{
				Replicas:      5,
				ReadyReplicas: 5,
			},
			expected:    true,
			expectError: false,
		},
		{
			name: "Not all replicas ready",
			lwsStatus: lwsv1.LeaderWorkerSetStatus{
				Replicas:      5,
				ReadyReplicas: 3,
			},
			expected:    false,
			expectError: false,
		},
		{
			name:        "LWS not found",
			lwsStatus:   lwsv1.LeaderWorkerSetStatus{},
			expected:    false,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Create a fake client
				builder := fake.NewClientBuilder().WithScheme(scheme)

				// Add LWS only if it should exist
				if !tt.expectError || tt.lwsStatus.Replicas > 0 || tt.lwsStatus.ReadyReplicas > 0 {
					lws := &lwsv1.LeaderWorkerSet{
						ObjectMeta: metav1.ObjectMeta{
							Name:      rbg.GetWorkloadName(&lwsRole),
							Namespace: rbg.Namespace,
						},
						Status: tt.lwsStatus,
					}
					builder = builder.WithObjects(lws)
				}

				fakeClient := builder.Build()

				// Create reconciler
				reconciler := NewLeaderWorkerSetReconciler(scheme, fakeClient)

				// Test ready check
				ctx := context.Background()
				ready, err := reconciler.CheckWorkloadReady(ctx, rbg, &lwsRole)

				if tt.expectError {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
					assert.Equal(t, tt.expected, ready)
				}
			},
		)
	}
}

// TestLeaderWorkerSetReconciler_CleanupOrphanedWorkloads tests the CleanupOrphanedWorkloads method
func TestLeaderWorkerSetReconciler_CleanupOrphanedWorkloads(t *testing.T) {
	// Create a scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = lwsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)

	// Create test RBG
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			UID:       "rbg-uid-1",
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				{
					Name:     "role1",
					Replicas: ptr.To(int32(2)),
					Workload: workloadsv1alpha1.WorkloadSpec{
						APIVersion: "leaderworkerset.x-k8s.io/v1",
						Kind:       "LeaderWorkerSet",
					},
					Template: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "nginx:latest",
								},
							},
						},
					},
					LeaderWorkerSet: &workloadsv1alpha1.LeaderWorkerTemplate{
						Size: ptr.To(int32(3)),
					},
				},
			},
		},
	}

	// Create LWS objects
	ownedLWS := &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-role1",
			Namespace: rbg.Namespace,
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: rbg.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: rbg.APIVersion,
					Kind:       rbg.Kind,
					Name:       rbg.Name,
					UID:        rbg.UID,
					Controller: ptr.To[bool](true),
				},
			},
		},
	}

	orphanedLWS := &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "orphaned-lws",
			Namespace: rbg.Namespace,
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: rbg.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: rbg.APIVersion,
					Kind:       rbg.Kind,
					Name:       rbg.Name,
					UID:        rbg.UID,
					Controller: ptr.To[bool](true),
				},
			},
		},
	}

	unrelatedLWS := &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-lws",
			Namespace: rbg.Namespace,
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "other-rbg",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: rbg.APIVersion,
					Kind:       rbg.Kind,
					Name:       "other-rbg",
					UID:        "other-rbg-uid",
					Controller: ptr.To[bool](true),
				},
			},
		},
	}

	// lws crd
	lwsCRD := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "leaderworkersets.leaderworkerset.x-k8s.io",
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
				{
					Type:   apiextensionsv1.Established,
					Status: apiextensionsv1.ConditionTrue,
				},
			},
		},
	}

	// Create a fake client with initial objects
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(ownedLWS, orphanedLWS, unrelatedLWS, lwsCRD).Build()
	reconciler := NewLeaderWorkerSetReconciler(scheme, fakeClient)

	// Test cleanup
	ctx := log.IntoContext(context.Background(), zap.New().WithValues("env", "test"))
	err := reconciler.CleanupOrphanedWorkloads(ctx, rbg)
	assert.NoError(t, err)

	// Verify owned LWS still exists
	existingLWS := &lwsv1.LeaderWorkerSet{}
	err = fakeClient.Get(ctx, types.NamespacedName{Name: ownedLWS.Name, Namespace: ownedLWS.Namespace}, existingLWS)
	assert.NoError(t, err)

	// Verify orphaned LWS was deleted
	deletedLWS := &lwsv1.LeaderWorkerSet{}
	err = fakeClient.Get(
		ctx, types.NamespacedName{Name: orphanedLWS.Name, Namespace: orphanedLWS.Namespace}, deletedLWS,
	)
	assert.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err))

	// Verify unrelated LWS still exists
	otherLWS := &lwsv1.LeaderWorkerSet{}
	err = fakeClient.Get(
		ctx, types.NamespacedName{Name: unrelatedLWS.Name, Namespace: unrelatedLWS.Namespace}, otherLWS,
	)
	assert.NoError(t, err)
}

// TestLeaderWorkerSetReconciler_RecreateWorkload tests the RecreateWorkload method
func TestLeaderWorkerSetReconciler_RecreateWorkload(t *testing.T) {
	// Create a scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = lwsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	lwsRole := wrappers.BuildLwsRole("test-role").Obj()
	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha1.RoleSpec{lwsRole}).Obj()

	lws := &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.GetWorkloadName(&lwsRole),
			Namespace: rbg.Namespace,
			UID:       "lws-uid-1",
		},
	}

	tests := []struct {
		name          string
		rbg           *workloadsv1alpha1.RoleBasedGroup
		role          *workloadsv1alpha1.RoleSpec
		lws           *lwsv1.LeaderWorkerSet
		mockReconcile bool
		wantErr       bool
	}{
		{
			name:          "lws recreation",
			rbg:           rbg,
			role:          &lwsRole,
			lws:           lws,
			mockReconcile: true,
			wantErr:       false,
		},
		{
			name: "non-existing LWS",
			rbg:  rbg,
			role: &lwsRole,
			lws: &lwsv1.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg-lws-2",
					Namespace: rbg.Namespace,
					UID:       "lws-uid-2",
				},
			},
			mockReconcile: true,
			wantErr:       false,
		},
		{
			name:          "rbg nil",
			rbg:           nil,
			role:          &lwsRole,
			lws:           lws,
			mockReconcile: false,
			wantErr:       false,
		},
		{
			name:          "role nil",
			rbg:           rbg,
			role:          nil,
			lws:           lws,
			mockReconcile: false,
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.lws).Build()
				r := NewLeaderWorkerSetReconciler(scheme, fakeClient)
				if tt.mockReconcile {
					// mock rbg controller reconcile
					go func() {
						for i := 0; i < 60; i++ {
							newlws := &lwsv1.LeaderWorkerSet{
								ObjectMeta: metav1.ObjectMeta{
									Name:      rbg.GetWorkloadName(&lwsRole),
									Namespace: "default",
								},
							}
							err := r.client.Create(context.TODO(), newlws)
							if err != nil && !apierrors.IsAlreadyExists(err) {
								t.Logf("create failed: %v", err)
							}
							time.Sleep(5 * time.Second)
						}
					}()
				}
				err := r.RecreateWorkload(context.TODO(), tt.rbg, tt.role)
				if (err != nil) != tt.wantErr {
					t.Errorf("RecreateWorkload() error = %v, wantErr %v", err, tt.wantErr)
				}
			},
		)

	}
}

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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// TestLeaderWorkerSetReconciler_Reconciler tests the Reconciler method
func TestLeaderWorkerSetReconciler_Reconciler(t *testing.T) {
	// Create a scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = lwsv1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	// Create test objects
	lwsRole := wrappersv2.BuildLeaderWorkerRole("test-role").Obj()
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{wrappersv2.BuildLeaderWorkerRole("test-role").Obj()}).Obj()

	// Create a fake client with initial objects
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Create reconciler
	reconciler := NewLeaderWorkerSetReconciler(scheme, fakeClient)

	// Test successful reconciliation
	ctx := context.Background()
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
	assert.Equal(t, expectedRevisionHash, lws.Labels[fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, lwsRole.Name)])
}

// TestLeaderWorkerSetReconciler_ConstructRoleStatus tests the ConstructRoleStatus method
func TestLeaderWorkerSetReconciler_ConstructRoleStatus(t *testing.T) {
	// Create a scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = lwsv1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	// Create test objects
	lwsRole := wrappersv2.BuildLeaderWorkerRole("test-role").Obj()
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{lwsRole}).Obj()

	// Create LWS with status
	lws := &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            rbg.GetWorkloadName(&lwsRole),
			Namespace:       rbg.Namespace,
			OwnerReferences: claimTestOwnedBy(rbg),
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
	status, err := reconciler.ConstructRoleStatus(ctx, rbg, &lwsRole)
	assert.NoError(t, err)
	assert.Equal(t, "test-role", status.Name)
	assert.Equal(t, int32(5), status.Replicas)
	assert.Equal(t, int32(3), status.ReadyReplicas)

	// Add status to RBG and test again
	rbg.Status = workloadsv1alpha2.RoleBasedGroupStatus{
		RoleStatuses: []workloadsv1alpha2.RoleStatus{status},
	}

	// Test when status is the same (should not need update)
	status2, err := reconciler.ConstructRoleStatus(ctx, rbg, &lwsRole)
	assert.NoError(t, err)
	assert.Equal(t, status, status2)
}

// TestLeaderWorkerSetReconciler_CheckWorkloadReady tests the CheckWorkloadReady method
func TestLeaderWorkerSetReconciler_CheckWorkloadReady(t *testing.T) {
	// Create a scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = lwsv1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	// Create test objects
	lwsRole := wrappersv2.BuildLeaderWorkerRole("test-role").Obj()
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{lwsRole}).Obj()

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
							Name:            rbg.GetWorkloadName(&lwsRole),
							Namespace:       rbg.Namespace,
							OwnerReferences: claimTestOwnedBy(rbg),
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
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)

	// Create test RBG
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
			UID:       "rbg-uid-1",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{
					Name:     "role1",
					Replicas: ptr.To(int32(2)),
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet",
					},
					Pattern: workloadsv1alpha2.Pattern{
						LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
							Size: ptr.To(int32(3)),
							TemplateSource: workloadsv1alpha2.TemplateSource{
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
							},
						},
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
				constants.GroupNameLabelKey: rbg.Name,
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
				constants.GroupNameLabelKey: rbg.Name,
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
				constants.GroupNameLabelKey: "other-rbg",
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

// TestLwsSpecEqual_RolloutStrategyDiff tests that lwsSpecEqual detects RolloutStrategy
// differences, which is necessary for triggering reconciliation on partition changes (PR #151 fix)
func TestLwsSpecEqual_RolloutStrategyDiff(t *testing.T) {
	baseSpec := func() lwsv1.LeaderWorkerSetSpec {
		return lwsv1.LeaderWorkerSetSpec{
			Replicas: ptr.To(int32(3)),
			LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
				Size: ptr.To(int32(2)),
				LeaderTemplate: &corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "test", Image: "nginx"}},
					},
				},
				WorkerTemplate: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "test", Image: "nginx"}},
					},
				},
			},
		}
	}

	// Test: different partition values should be detected as not equal
	spec1 := baseSpec()
	spec1.RolloutStrategy = lwsv1.RolloutStrategy{
		Type: lwsv1.RollingUpdateStrategyType,
		RollingUpdateConfiguration: &lwsv1.RollingUpdateConfiguration{
			Partition: ptr.To(int32(5)),
		},
	}

	spec2 := baseSpec()
	spec2.RolloutStrategy = lwsv1.RolloutStrategy{
		Type: lwsv1.RollingUpdateStrategyType,
		RollingUpdateConfiguration: &lwsv1.RollingUpdateConfiguration{
			Partition: ptr.To(int32(8)),
		},
	}

	equal, _ := lwsSpecEqual(spec1, spec2)
	assert.False(t, equal, "lwsSpecEqual should detect partition difference")
}

// TestConstructLWSApplyConfiguration_CoordinationRollingUpdate verifies that coordination-level
// rolling update parameters are applied even when role-level RolloutStrategy is nil.
func TestConstructLWSApplyConfiguration_CoordinationRollingUpdate(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := NewLeaderWorkerSetReconciler(scheme, fakeClient)

	role := &workloadsv1alpha2.RoleSpec{
		Name:            "test-role",
		Replicas:        ptr.To(int32(3)),
		RolloutStrategy: nil, // no role-level rolling update
	}

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{*role},
		},
	}

	coordinationRollingUpdate := &workloadsv1alpha2.RollingUpdate{
		Partition: ptr.To(intstr.FromInt32(2)),
	}

	result, err := reconciler.constructLWSApplyConfiguration(
		context.Background(),
		rbg,
		role,
		coordinationRollingUpdate,
		expectedRevisionHash,
	)

	assert.NoError(t, err)
	assert.NotNil(t, result.Spec.RolloutStrategy)
	assert.NotNil(t, result.Spec.RolloutStrategy.RollingUpdateConfiguration)
	assert.Equal(t, int32(2), *result.Spec.RolloutStrategy.RollingUpdateConfiguration.Partition)
}

func TestConstructLWSApplyConfiguration_LabelsAndAnnotations(t *testing.T) {
	role := &workloadsv1alpha2.RoleSpec{
		Name:     "test-role",
		Replicas: ptr.To(int32(3)),
	}

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{*role},
		},
	}

	tests := []struct {
		name                string
		roleLabels          map[string]string
		roleAnnotations     map[string]string
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name: "test role labels and annotations",
			roleLabels: map[string]string{
				"app":          "my-app",
				"version":      "v1.0",
				"custom-label": "role-value",
			},
			expectedLabels: map[string]string{
				"app":                       "my-app",
				"version":                   "v1.0",
				"custom-label":              "role-value",
				constants.RoleNameLabelKey:  role.Name,
				constants.GroupNameLabelKey: rbg.Name,
				constants.GroupUIDLabelKey:  rbg.GenGroupUniqueKey(),
				fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name): expectedRevisionHash,
			},
			roleAnnotations: map[string]string{
				"description":       "custom description",
				"custom-annotation": "role-value",
			},
			expectedAnnotations: map[string]string{
				"description":                   "custom description",
				"custom-annotation":             "role-value",
				constants.RoleSizeAnnotationKey: "3",
			},
		},
		{
			name: "test role labels and annotations with priority",
			roleLabels: map[string]string{
				"app":                       "my-app",
				"version":                   "v1.0",
				"custom-label":              "role-value",
				constants.RoleNameLabelKey:  "custom",
				constants.GroupNameLabelKey: "custom",
				constants.GroupUIDLabelKey:  "custom",
				fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name): "custom",
			},
			expectedLabels: map[string]string{
				"app":                       "my-app",
				"version":                   "v1.0",
				"custom-label":              "role-value",
				constants.RoleNameLabelKey:  role.Name,
				constants.GroupNameLabelKey: rbg.Name,
				constants.GroupUIDLabelKey:  rbg.GenGroupUniqueKey(),
				fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name): expectedRevisionHash,
			},
			roleAnnotations: map[string]string{
				"description":                   "custom description",
				"custom-annotation":             "role-value",
				constants.RoleSizeAnnotationKey: "custom",
			},
			expectedAnnotations: map[string]string{
				"description":                   "custom description",
				"custom-annotation":             "role-value",
				constants.RoleSizeAnnotationKey: "3",
			},
		},
		{
			name:            "no role labels or annotations",
			roleLabels:      nil,
			roleAnnotations: nil,
			expectedLabels: map[string]string{
				constants.RoleNameLabelKey:                                role.Name,
				constants.GroupNameLabelKey:                               rbg.Name,
				constants.GroupUIDLabelKey:                                rbg.GenGroupUniqueKey(),
				fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name): expectedRevisionHash,
			},
			expectedAnnotations: map[string]string{
				constants.RoleSizeAnnotationKey: "3",
			},
		},
		{
			name:            "empty role labels and annotations",
			roleLabels:      map[string]string{},
			roleAnnotations: map[string]string{},
			expectedLabels: map[string]string{
				constants.RoleNameLabelKey:                                role.Name,
				constants.GroupNameLabelKey:                               rbg.Name,
				constants.GroupUIDLabelKey:                                rbg.GenGroupUniqueKey(),
				fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name): expectedRevisionHash,
			},
			expectedAnnotations: map[string]string{
				constants.RoleSizeAnnotationKey: "3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = appsv1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			_ = workloadsv1alpha2.AddToScheme(scheme)

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			reconciler := NewLeaderWorkerSetReconciler(scheme, fakeClient)

			role.Labels = tt.roleLabels
			role.Annotations = tt.roleAnnotations

			result, err := reconciler.constructLWSApplyConfiguration(
				context.Background(),
				rbg,
				role,
				nil,
				expectedRevisionHash,
			)

			if err != nil {
				t.Fatalf("constructLWSApplyConfiguration() error = %v", err)
			}

			assert.Equal(t, tt.expectedLabels, result.Labels)
			assert.Equal(t, tt.expectedAnnotations, result.Annotations)
		})
	}
}

// TestReview_LWSNonLWPPatternFragmentRouting pins the render-path contract
// of the grandfathered LWS × non-leaderWorkerPattern shape (KEP-531
// compatibility guarantee #1 — references that set only profileName render
// identically to main): a LeaderWorkerSet-typed role whose pattern is
// standalonePattern keeps main's published LWS behavior, where the leader
// template is injected unconditionally and the worker template is not. The
// profile's sidecar and volume land in the leader template only, the worker
// template stays as the role wrote it, with no warning and no error. A
// vocabulary keyed by the pattern (the standalone role name) would miss the
// leader/worker positions this construct path renders and silently drop
// every engine-runtime fragment.
func TestReview_LWSNonLWPPatternFragmentRouting(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "ik"},
		Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
			Volumes: []corev1.Volume{{
				Name:         "ik-vol",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}},
			Containers: []corev1.Container{{Name: "ik-sidecar", Image: "ik:latest"}},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile).Build()
	reconciler := NewLeaderWorkerSetReconciler(scheme, fakeClient)
	recorder := record.NewFakeRecorder(16)
	reconciler.SetRecorder(recorder)

	tmpl := corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "engine", Image: "engine:latest"}},
	}}
	role := &workloadsv1alpha2.RoleSpec{
		Name:     "sgl",
		Replicas: ptr.To(int32(2)),
		Pattern: workloadsv1alpha2.Pattern{
			StandalonePattern: &workloadsv1alpha2.StandalonePattern{
				TemplateSource: workloadsv1alpha2.TemplateSource{Template: &tmpl},
			},
		},
		EngineRuntimes: []workloadsv1alpha2.EngineRuntime{{ProfileName: "ik"}},
	}
	// the grandfathered shape: the role's workload-type annotation routes
	// it through the LWS render path while its pattern stays standalone
	role.Annotations = map[string]string{
		constants.RoleWorkloadTypeAnnotationKey: constants.LeaderWorkerSetWorkloadType,
	}
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "lws-rbg", Namespace: "default"},
		Spec:       workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{*role}},
	}

	result, err := reconciler.constructLWSApplyConfiguration(
		context.Background(), rbg, role, nil, expectedRevisionHash)
	require.NoError(t, err)

	names := func(spec *coreapplyv1.PodSpecApplyConfiguration) map[string]bool {
		found := map[string]bool{}
		if spec == nil {
			return found
		}
		for _, c := range spec.Containers {
			if c.Name != nil {
				found[*c.Name] = true
			}
		}
		return found
	}
	volumes := func(spec *coreapplyv1.PodSpecApplyConfiguration) map[string]bool {
		found := map[string]bool{}
		if spec == nil {
			return found
		}
		for _, v := range spec.Volumes {
			if v.Name != nil {
				found[*v.Name] = true
			}
		}
		return found
	}

	lwt := result.Spec.LeaderWorkerTemplate
	require.NotNil(t, lwt.LeaderTemplate)
	require.NotNil(t, lwt.WorkerTemplate)

	// main's behavior: the leader template carries the fragments
	assert.True(t, names(lwt.LeaderTemplate.Spec)["ik-sidecar"],
		"the profile's sidecar lands in the leader template")
	assert.True(t, volumes(lwt.LeaderTemplate.Spec)["ik-vol"],
		"the profile's volume lands in the leader template")

	// the worker template is skipped, as on main
	assert.False(t, names(lwt.WorkerTemplate.Spec)["ik-sidecar"],
		"the worker template stays without the profile's sidecar")
	assert.False(t, volumes(lwt.WorkerTemplate.Spec)["ik-vol"],
		"the worker template stays without the profile's volume")

	assert.Empty(t, len(recorder.Events), "no warning events — the render is main's behavior")
}

package reconciler

import (
	"context"
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
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha1client "sigs.k8s.io/rbgs/client-go/applyconfiguration/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func TestInstanceSetReconciler_Validate(t *testing.T) {
	tests := []struct {
		name        string
		role        *workloadsv1alpha1.RoleSpec
		expectError bool
	}{
		{
			name: "valid components without template or leaderWorkerSet",
			role: &workloadsv1alpha1.RoleSpec{
				Labels: map[string]string{
					workloadsv1alpha1.RBGInstancePatternLabelKey: string(workloadsv1alpha1.DeploymentInstancePattern),
				},
				Components: []workloadsv1alpha1.InstanceComponent{
					{
						Name: "test-component",
						Size: ptr.To(int32(1)),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "test-container",
										Image: "nginx",
									},
								},
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "invalid components with template",
			role: &workloadsv1alpha1.RoleSpec{
				Labels: map[string]string{
					workloadsv1alpha1.RBGInstancePatternLabelKey: string(workloadsv1alpha1.DeploymentInstancePattern),
				},
				Components: []workloadsv1alpha1.InstanceComponent{
					{
						Name: "test-component",
						Size: ptr.To(int32(1)),
					},
				},
				TemplateSource: workloadsv1alpha1.TemplateSource{
					Template: &corev1.PodTemplateSpec{},
				},
			},
			expectError: true,
		},
		{
			name: "invalid components with leaderWorkerSet",
			role: &workloadsv1alpha1.RoleSpec{
				Labels: map[string]string{
					workloadsv1alpha1.RBGInstancePatternLabelKey: string(workloadsv1alpha1.DeploymentInstancePattern),
				},
				Components: []workloadsv1alpha1.InstanceComponent{
					{
						Name: "test-component",
						Size: ptr.To(int32(1)),
					},
				},
				LeaderWorkerSet: &workloadsv1alpha1.LeaderWorkerTemplate{},
			},
			expectError: true,
		},
		{
			name: "valid template with Deployment pattern",
			role: &workloadsv1alpha1.RoleSpec{
				Labels: map[string]string{
					workloadsv1alpha1.RBGInstancePatternLabelKey: string(workloadsv1alpha1.DeploymentInstancePattern),
				},
				TemplateSource: workloadsv1alpha1.TemplateSource{
					Template: &corev1.PodTemplateSpec{},
				},
			},
			expectError: false,
		},
		{
			name: "valid template without instance pattern",
			role: &workloadsv1alpha1.RoleSpec{
				TemplateSource: workloadsv1alpha1.TemplateSource{
					Template: &corev1.PodTemplateSpec{},
				},
			},
			expectError: true,
		},
		{
			name: "valid template with invalid pattern",
			role: &workloadsv1alpha1.RoleSpec{
				Labels: map[string]string{
					workloadsv1alpha1.RBGInstancePatternLabelKey: string(workloadsv1alpha1.StatefulSetInstancePattern),
				},
				TemplateSource: workloadsv1alpha1.TemplateSource{
					Template: &corev1.PodTemplateSpec{},
				},
			},
			expectError: true,
		},
		{
			name: "valid leaderWorkerSet without components or template",
			role: &workloadsv1alpha1.RoleSpec{
				Labels: map[string]string{
					workloadsv1alpha1.RBGInstancePatternLabelKey: string(workloadsv1alpha1.DeploymentInstancePattern),
				},
				LeaderWorkerSet: &workloadsv1alpha1.LeaderWorkerTemplate{
					PatchLeaderTemplate: &runtime.RawExtension{},
					PatchWorkerTemplate: &runtime.RawExtension{},
				},
			},
			expectError: false,
		},
		{
			name:        "invalid neither template nor leaderWorkerSet",
			role:        &workloadsv1alpha1.RoleSpec{},
			expectError: true,
		},
		{
			name: "invalid leaderWorkerSet without patchLeaderTemplate",
			role: &workloadsv1alpha1.RoleSpec{
				Labels: map[string]string{
					workloadsv1alpha1.RBGInstancePatternLabelKey: string(workloadsv1alpha1.DeploymentInstancePattern),
				},
				LeaderWorkerSet: &workloadsv1alpha1.LeaderWorkerTemplate{
					PatchWorkerTemplate: &runtime.RawExtension{},
				},
			},
			expectError: true,
		},
		{
			name: "invalid leaderWorkerSet without patchWorkerTemplate",
			role: &workloadsv1alpha1.RoleSpec{
				Labels: map[string]string{
					workloadsv1alpha1.RBGInstancePatternLabelKey: string(workloadsv1alpha1.DeploymentInstancePattern),
				},
				LeaderWorkerSet: &workloadsv1alpha1.LeaderWorkerTemplate{
					PatchLeaderTemplate: &runtime.RawExtension{},
				},
			},
			expectError: true,
		},
		{
			name: "invalid leaderWorkerSet with neither patch templates",
			role: &workloadsv1alpha1.RoleSpec{
				Labels: map[string]string{
					workloadsv1alpha1.RBGInstancePatternLabelKey: string(workloadsv1alpha1.DeploymentInstancePattern),
				},
				LeaderWorkerSet: &workloadsv1alpha1.LeaderWorkerTemplate{},
			},
			expectError: true,
		},
	}

	scheme := runtime.NewScheme()
	instanceSetReconciler := NewInstanceSetReconciler(scheme, nil)
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := instanceSetReconciler.Validate(ctx, tt.role)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestInstanceSetReconciler_constructInstanceSetApplyConfiguration(t *testing.T) {
	// Setup test environment
	s := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))

	// Create test objects
	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()

	// Setup fake client
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(rbg).Build()

	// Create InstanceSetReconciler
	instanceSetReconciler := NewInstanceSetReconciler(s, cl)

	ctx := context.Background()
	revisionKey := "test-revision-key"

	t.Run("with components configuration", func(t *testing.T) {
		role := &workloadsv1alpha1.RoleSpec{
			Name:            "test-role",
			Replicas:        ptr.To(int32(3)),
			MinReadySeconds: int32(10),
			Components: []workloadsv1alpha1.InstanceComponent{
				{
					Name: "component-1",
					Size: ptr.To(int32(2)),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "container-1",
									Image: "nginx:latest",
								},
							},
						},
					},
				},
				{
					Name: "component-2",
					Size: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "container-2",
									Image: "redis:latest",
								},
							},
						},
					},
				},
			},
		}

		rbg.Spec.Roles = []workloadsv1alpha1.RoleSpec{*role}

		config, err := instanceSetReconciler.constructInstanceSetApplyConfiguration(
			ctx, rbg, role, nil, revisionKey)

		assert.NoError(t, err)
		assert.NotNil(t, config)

		// Check basic properties
		assert.Equal(t, rbg.GetWorkloadName(role), *config.Name)
		assert.Equal(t, rbg.Namespace, *config.Namespace)
		assert.Equal(t, *role.Replicas, *config.Spec.Replicas)
		assert.Equal(t, role.MinReadySeconds, *config.Spec.MinReadySeconds)

		// Check labels
		expectedLabelKey := fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, role.Name)
		assert.Equal(t, revisionKey, config.Labels[expectedLabelKey])

		// Check components
		assert.Len(t, config.Spec.InstanceTemplate.Components, 2)

		// Check first component
		component1 := config.Spec.InstanceTemplate.Components[0]
		assert.Equal(t, "component-1", *component1.Name)
		assert.Equal(t, int32(2), *component1.Size)
		assert.NotNil(t, component1.Template)

		// Check second component
		component2 := config.Spec.InstanceTemplate.Components[1]
		assert.Equal(t, "component-2", *component2.Name)
		assert.Equal(t, int32(3), *component2.Size)
		assert.NotNil(t, component2.Template)
	})

	t.Run("with template configuration", func(t *testing.T) {
		role := &workloadsv1alpha1.RoleSpec{
			Name:            "test-role-template",
			Replicas:        ptr.To(int32(2)),
			MinReadySeconds: int32(5),
			TemplateSource: workloadsv1alpha1.TemplateSource{
				Template: &corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "main-container",
								Image: "nginx:alpine",
							},
						},
					},
				},
			},
		}
		rbg.Spec.Roles = []workloadsv1alpha1.RoleSpec{*role}

		config, err := instanceSetReconciler.constructInstanceSetApplyConfiguration(
			ctx, rbg, role, nil, revisionKey)

		assert.NoError(t, err)
		assert.NotNil(t, config)

		// Check basic properties
		assert.Equal(t, rbg.GetWorkloadName(role), *config.Name)
		assert.Equal(t, rbg.Namespace, *config.Namespace)
		assert.Equal(t, *role.Replicas, *config.Spec.Replicas)
		assert.Equal(t, role.MinReadySeconds, *config.Spec.MinReadySeconds)

		// Check labels
		expectedLabelKey := "rolebasedgroup.workloads.x-k8s.io/role-revision-hash-test-role-template"
		assert.Equal(t, revisionKey, config.Labels[expectedLabelKey])

		// Check components - should have one component based on the template
		assert.Len(t, config.Spec.InstanceTemplate.Components, 1)

		component := config.Spec.InstanceTemplate.Components[0]
		assert.Equal(t, role.Name, *component.Name)
		assert.Equal(t, int32(1), *component.Size) // Default size for template-based component
		assert.NotNil(t, component.Template)
	})

	t.Run("with leaderWorkerSet configuration", func(t *testing.T) {
		role := &workloadsv1alpha1.RoleSpec{
			Name:            "test-role-lws",
			Replicas:        ptr.To(int32(1)),
			MinReadySeconds: int32(15),
			LeaderWorkerSet: &workloadsv1alpha1.LeaderWorkerTemplate{
				Size: ptr.To(int32(3)),
				PatchLeaderTemplate: &runtime.RawExtension{
					Raw: []byte(`{"spec":{"containers":[{"name":"leader","image":"nginx:latest"}]}}`),
				},
				PatchWorkerTemplate: &runtime.RawExtension{
					Raw: []byte(`{"spec":{"containers":[{"name":"worker","image":"nginx:latest"}]}}`),
				},
			},
		}
		rbg.Spec.Roles = []workloadsv1alpha1.RoleSpec{*role}

		config, err := instanceSetReconciler.constructInstanceSetApplyConfiguration(
			ctx, rbg, role, nil, revisionKey)

		assert.NoError(t, err)
		assert.NotNil(t, config)

		// Check basic properties
		assert.Equal(t, rbg.GetWorkloadName(role), *config.Name)
		assert.Equal(t, rbg.Namespace, *config.Namespace)
		assert.Equal(t, *role.Replicas, *config.Spec.Replicas)
		assert.Equal(t, role.MinReadySeconds, *config.Spec.MinReadySeconds)

		// Check labels
		expectedLabelKey := fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, role.Name)
		assert.Equal(t, revisionKey, config.Labels[expectedLabelKey])

		// Check components - should have leader and worker components
		assert.Len(t, config.Spec.InstanceTemplate.Components, 2)

		// Find leader and worker components
		var leaderComponent, workerComponent *workloadsv1alpha1client.InstanceComponentApplyConfiguration
		for _, comp := range config.Spec.InstanceTemplate.Components {
			if *comp.Name == "leader" {
				leaderComponent = &comp
			} else if *comp.Name == "worker" {
				workerComponent = &comp
			}
		}

		// Check leader component
		assert.NotNil(t, leaderComponent)
		assert.Equal(t, "leader", *leaderComponent.Name)
		assert.Equal(t, int32(1), *leaderComponent.Size)
		assert.NotNil(t, leaderComponent.Template)

		// Check worker component
		assert.NotNil(t, workerComponent)
		assert.Equal(t, "worker", *workerComponent.Name)
		assert.Equal(t, int32(2), *workerComponent.Size) // Should match LeaderWorkerSet.Size
		assert.NotNil(t, workerComponent.Template)
	})

	t.Run("with rollout strategy", func(t *testing.T) {
		rollingUpdateStrategy := &workloadsv1alpha1.RollingUpdate{
			Partition:      ptr.To(intstr.FromInt32(1)),
			MaxUnavailable: ptr.To(intstr.FromInt32(1)),
		}

		role := &workloadsv1alpha1.RoleSpec{
			Name:     "test-role-strategy",
			Replicas: ptr.To(int32(3)),
			TemplateSource: workloadsv1alpha1.TemplateSource{
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
			RolloutStrategy: &workloadsv1alpha1.RolloutStrategy{
				Type: workloadsv1alpha1.RollingUpdateStrategyType,
				RollingUpdate: &workloadsv1alpha1.RollingUpdate{
					Partition:      ptr.To(intstr.FromInt32(2)),
					MaxUnavailable: ptr.To(intstr.FromInt32(2)),
				},
			},
		}
		rbg.Spec.Roles = []workloadsv1alpha1.RoleSpec{*role}

		config, err := instanceSetReconciler.constructInstanceSetApplyConfiguration(
			ctx, rbg, role, rollingUpdateStrategy, revisionKey)

		assert.NoError(t, err)
		assert.NotNil(t, config)

		// Check that the rollout strategy is applied
		assert.NotNil(t, config.Spec.UpdateStrategy)
		assert.Equal(t, intstr.FromInt32(1), *config.Spec.UpdateStrategy.Partition)      // Should use the override strategy
		assert.Equal(t, intstr.FromInt32(1), *config.Spec.UpdateStrategy.MaxUnavailable) // Default value
	})

	t.Run("with invalid patch template", func(t *testing.T) {
		role := &workloadsv1alpha1.RoleSpec{
			Name:     "test-role-invalid",
			Replicas: ptr.To(int32(1)),
			LeaderWorkerSet: &workloadsv1alpha1.LeaderWorkerTemplate{
				Size: ptr.To(int32(1)),
				PatchLeaderTemplate: &runtime.RawExtension{
					Raw: []byte(`{invalid-json}`),
				},
				PatchWorkerTemplate: &runtime.RawExtension{
					Raw: []byte(`{"spec":{"containers":[{"name":"worker","image":"nginx:latest"}]}}`),
				},
			},
		}
		rbg.Spec.Roles = []workloadsv1alpha1.RoleSpec{*role}

		config, err := instanceSetReconciler.constructInstanceSetApplyConfiguration(
			ctx, rbg, role, nil, revisionKey)

		// Should fail due to invalid JSON in patch
		assert.Error(t, err)
		assert.Nil(t, config)
	})
}

func TestInstanceSetReconciler_Reconciler(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	role := wrappers.BuildBasicRole("test-role").WithWorkload(workloadsv1alpha1.InstanceSetWorkloadType).Obj()
	rollingRole := wrappers.BuildBasicRole("test-role").WithReplicas(4).
		WithWorkload(workloadsv1alpha1.InstanceSetWorkloadType).
		WithRollingUpdate(
			workloadsv1alpha1.RollingUpdate{
				MaxUnavailable: ptr.To(intstr.FromInt32(2)),
				MaxSurge:       ptr.To(intstr.FromInt32(2)),
				Partition:      ptr.To(intstr.FromInt32(1)),
			},
		).Obj()

	tests := []struct {
		name      string
		rbg       *workloadsv1alpha1.RoleBasedGroup
		role      *workloadsv1alpha1.RoleSpec
		expectErr bool
	}{
		{
			name:      "normal",
			rbg:       rbg,
			role:      &role,
			expectErr: false,
		},
		{
			name:      "role with rollingUpdate",
			rbg:       rbg,
			role:      &rollingRole,
			expectErr: false,
		},
		{
			name:      "rbg name start with numeric",
			rbg:       wrappers.BuildBasicRoleBasedGroup("123-rbg", "default").Obj(),
			role:      &role,
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				client := fake.NewClientBuilder().WithScheme(scheme).Build()

				r := &InstanceSetReconciler{
					scheme: scheme,
					client: client,
				}
				err := r.Reconciler(context.Background(), tt.rbg, tt.role, nil, expectedRevisionHash)
				if tt.expectErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)

					// Check if instanceSet was created
					instanceSet := &workloadsv1alpha1.InstanceSet{}
					err = client.Get(
						context.Background(), types.NamespacedName{
							Name:      tt.rbg.GetWorkloadName(tt.role),
							Namespace: tt.rbg.Namespace,
						}, instanceSet,
					)
					assert.NoError(t, err)
					assert.Equal(t, tt.rbg.GetWorkloadName(tt.role), instanceSet.Name)
					assert.Equal(t, expectedRevisionHash, instanceSet.Labels[fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, tt.role.Name)])

					// Check if Service was created
					svc := &corev1.Service{}
					err = client.Get(
						context.Background(), types.NamespacedName{
							Name:      tt.rbg.GetServiceName(tt.role),
							Namespace: tt.rbg.Namespace,
						}, svc,
					)
					assert.NoError(t, err)
					assert.Equal(t, tt.rbg.GetServiceName(tt.role), svc.Name)
				}
			},
		)
	}
}

func TestInstanceSetReconciler_CheckWorkloadReady(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	role := wrappers.BuildBasicRole("test-role").WithWorkload(workloadsv1alpha1.InstanceSetWorkloadType).Obj()

	tests := []struct {
		name        string
		rbg         *workloadsv1alpha1.RoleBasedGroup
		role        *workloadsv1alpha1.RoleSpec
		instanceSet *workloadsv1alpha1.InstanceSet
		expectReady bool
		expectErr   bool
	}{
		{
			name: "workload ready",
			rbg:  rbg,
			role: &role,
			instanceSet: &workloadsv1alpha1.InstanceSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg-test-role",
					Namespace: "default",
				},
				Spec: workloadsv1alpha1.InstanceSetSpec{
					Replicas: ptr.To[int32](3),
				},
				Status: workloadsv1alpha1.InstanceSetStatus{
					ReadyReplicas: 3,
					Replicas:      3,
				},
			},
			expectReady: true,
			expectErr:   false,
		},
		{
			name: "workload not ready",
			rbg:  rbg,
			role: &role,
			instanceSet: &workloadsv1alpha1.InstanceSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg-test-role",
					Namespace: "default",
				},
				Spec: workloadsv1alpha1.InstanceSetSpec{
					Replicas: ptr.To[int32](3),
				},
				Status: workloadsv1alpha1.InstanceSetStatus{
					ReadyReplicas: 2,
					Replicas:      2,
				},
			},
			expectReady: false,
			expectErr:   false,
		},
		{
			name:        "workload not found",
			rbg:         rbg,
			role:        &role,
			instanceSet: nil,
			expectReady: false,
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				clientBuilder := fake.NewClientBuilder().WithScheme(scheme)
				if tt.instanceSet != nil {
					clientBuilder = clientBuilder.WithObjects(tt.instanceSet)
				}

				r := &InstanceSetReconciler{
					scheme: scheme,
					client: clientBuilder.Build(),
				}

				ready, err := r.CheckWorkloadReady(context.Background(), tt.rbg, tt.role)
				if tt.expectErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
					assert.Equal(t, tt.expectReady, ready)
				}
			},
		)
	}
}

func TestInstanceSetReconciler_CleanupOrphanedWorkloads(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()

	instanceSetOwned := &workloadsv1alpha1.InstanceSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-worker",
			Namespace: "default",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: workloadsv1alpha1.GroupVersion.String(),
					Kind:       "RoleBasedGroup",
					Name:       "test-rbg",
					UID:        rbg.UID,
					Controller: ptr.To[bool](true),
				},
			},
		},
	}

	instanceSetOrphaned := &workloadsv1alpha1.InstanceSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-orphaned",
			Namespace: "default",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: workloadsv1alpha1.GroupVersion.String(),
					Kind:       "RoleBasedGroup",
					Name:       "test-rbg",
					UID:        rbg.UID,
					Controller: ptr.To[bool](true),
				},
			},
		},
	}

	instanceSetDifferentOwner := &workloadsv1alpha1.InstanceSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-rbg-worker",
			Namespace: "default",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "other-rbg",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: workloadsv1alpha1.GroupVersion.String(),
					Kind:       "RoleBasedGroup",
					Name:       "other-rbg",
					UID:        "other-uid",
					Controller: ptr.To[bool](true),
				},
			},
		},
	}

	tests := []struct {
		name          string
		rbg           *workloadsv1alpha1.RoleBasedGroup
		existingObjs  []runtime.Object
		expectDeleted []string
		expectErr     bool
	}{
		{
			name: "cleanup orphaned workloads",
			rbg:  rbg,
			existingObjs: []runtime.Object{
				instanceSetOwned,
				instanceSetOrphaned,
				instanceSetDifferentOwner,
			},
			expectDeleted: []string{"test-rbg-orphaned"},
			expectErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				client := fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(tt.existingObjs...).
					Build()

				r := &InstanceSetReconciler{
					scheme: scheme,
					client: client,
				}

				err := r.CleanupOrphanedWorkloads(context.Background(), tt.rbg)
				if tt.expectErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)

					// Check that orphaned workloads were deleted
					for _, name := range tt.expectDeleted {
						instanceSet := &workloadsv1alpha1.InstanceSet{}
						err = client.Get(
							context.Background(), types.NamespacedName{
								Name:      name,
								Namespace: tt.rbg.Namespace,
							}, instanceSet,
						)
						assert.True(t, apierrors.IsNotFound(err), "Expected %s to be deleted", name)
					}
				}
			},
		)
	}
}

func TestInstanceSetReconciler_RecreateWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	role := wrappers.BuildBasicRole("test-role").WithWorkload(workloadsv1alpha1.InstanceSetWorkloadType).Obj()
	instanceSet := &workloadsv1alpha1.InstanceSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-test-role",
			Namespace: "default",
			UID:       "instanceset-uid",
		},
	}

	tests := []struct {
		name          string
		client        client.Client
		rbg           *workloadsv1alpha1.RoleBasedGroup
		role          *workloadsv1alpha1.RoleSpec
		mockReconcile bool
		expectErr     bool
	}{
		{
			name:          "recreate existing workload",
			client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(instanceSet).Build(),
			rbg:           rbg,
			role:          &role,
			mockReconcile: true,
			expectErr:     false,
		},
		{
			name:          "recreate non-existing workload",
			client:        fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg:           rbg,
			role:          &role,
			mockReconcile: true,
			expectErr:     false,
		},
		{
			name:          "nil rbg",
			client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(instanceSet).Build(),
			rbg:           nil,
			role:          &role,
			mockReconcile: false,
			expectErr:     false,
		},
		{
			name:          "nil role",
			client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(instanceSet).Build(),
			rbg:           rbg,
			role:          nil,
			mockReconcile: false,
			expectErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &InstanceSetReconciler{
					scheme: scheme,
					client: tt.client,
				}

				ctx := log.IntoContext(context.TODO(), zap.New().WithValues("env", "test"))

				if tt.mockReconcile {
					// mock rbg controller reconcile
					go func() {
						for i := 0; i < 60; i++ {
							newInstanceSet := &workloadsv1alpha1.InstanceSet{
								ObjectMeta: metav1.ObjectMeta{
									Name:      rbg.GetWorkloadName(&role),
									Namespace: "default",
								},
							}
							err := r.client.Create(ctx, newInstanceSet)
							if err != nil && !apierrors.IsAlreadyExists(err) {
								t.Logf("create failed: %v", err)
							}
							time.Sleep(5 * time.Second)
						}
					}()
				}

				err := r.RecreateWorkload(ctx, tt.rbg, tt.role)
				if (err != nil) != tt.expectErr {
					t.Errorf("InstanceSetReconciler.RecreateWorkload() error = %v, expectError %v", err, tt.expectErr)
				}
			},
		)
	}
}

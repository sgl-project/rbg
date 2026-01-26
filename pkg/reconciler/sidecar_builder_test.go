package reconciler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestSidecarBuilder_Build(t *testing.T) {
	// Define test scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)

	tests := []struct {
		name          string
		client        client.Client
		rbg           *workloadsv1alpha.RoleBasedGroup
		role          *workloadsv1alpha.RoleSpec
		podSpec       *v1.PodTemplateSpec
		expectedError bool
	}{
		{
			name:   "role not found",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg: &workloadsv1alpha.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-rbg",
				},
				Spec: workloadsv1alpha.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha.RoleSpec{
						{
							Name: "existing-role",
						},
					},
				},
			},
			role: &workloadsv1alpha.RoleSpec{
				Name: "non-existent-role",
			},
			podSpec:       &v1.PodTemplateSpec{},
			expectedError: true,
		},
		{
			name:   "no engine runtimes",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg: &workloadsv1alpha.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-rbg",
				},
				Spec: workloadsv1alpha.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha.RoleSpec{
						{
							Name:           "test-role",
							EngineRuntimes: []workloadsv1alpha.EngineRuntime{},
						},
					},
				},
			},
			role: &workloadsv1alpha.RoleSpec{
				Name: "test-role",
			},
			podSpec:       &v1.PodTemplateSpec{},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				roleData := &RoleData{
					Spec: tt.role,
					OwnerInfo: OwnerInfo{
						Name:      tt.rbg.Name,
						Namespace: tt.rbg.Namespace,
						UID:       tt.rbg.UID,
					},
				}

				b := &SidecarBuilder{
					RoleData: roleData,
					Client:   tt.client,
				}

				err := b.Build(context.TODO(), tt.podSpec)

				if (err != nil) != tt.expectedError {
					t.Errorf("SidecarBuilder.Build() error = %v, expectedError %v", err, tt.expectedError)
				}
			},
		)
	}
}

func TestSidecarBuilder_injectRuntime(t *testing.T) {
	// Define test scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)

	// Create test ClusterEngineRuntimeProfile
	engineRuntime := &workloadsv1alpha.ClusterEngineRuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-engine",
		},
		Spec: workloadsv1alpha.ClusterEngineRuntimeProfileSpec{
			InitContainers: []v1.Container{
				{
					Name:  "init-container",
					Image: "init-image:latest",
				},
			},
			Containers: []v1.Container{
				{
					Name:  "sidecar-container",
					Image: "sidecar-image:latest",
				},
			},
			Volumes: []v1.Volume{
				{
					Name: "test-volume",
					VolumeSource: v1.VolumeSource{
						EmptyDir: &v1.EmptyDirVolumeSource{},
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(engineRuntime).
		Build()

	tests := []struct {
		name          string
		client        client.Client
		rbg           *workloadsv1alpha.RoleBasedGroup
		role          *workloadsv1alpha.RoleSpec
		podSpec       *v1.PodTemplateSpec
		runtime       workloadsv1alpha.EngineRuntime
		expectedError bool
		verifyFunc    func(*testing.T, *v1.PodTemplateSpec)
	}{
		{
			name:   "inject runtime successfully",
			client: fakeClient,
			rbg: &workloadsv1alpha.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-rbg",
				},
			},
			role: &workloadsv1alpha.RoleSpec{
				Name: "test-role",
			},
			podSpec: &v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					InitContainers: []v1.Container{},
					Containers: []v1.Container{
						{
							Name:  "main-container",
							Image: "main-image:latest",
						},
					},
					Volumes: []v1.Volume{},
				},
			},
			runtime: workloadsv1alpha.EngineRuntime{
				ProfileName: "test-engine",
				Containers:  []v1.Container{},
			},
			expectedError: false,
			verifyFunc: func(t *testing.T, podSpec *v1.PodTemplateSpec) {
				// Check init container was added
				if len(podSpec.Spec.InitContainers) != 1 {
					t.Errorf("Expected 1 init container, got %d", len(podSpec.Spec.InitContainers))
				}

				// Check sidecar container was added
				if len(podSpec.Spec.Containers) != 2 {
					t.Errorf("Expected 2 containers, got %d", len(podSpec.Spec.Containers))
				}

				// Check volume was added
				if len(podSpec.Spec.Volumes) != 1 {
					t.Errorf("Expected 1 volume, got %d", len(podSpec.Spec.Volumes))
				}
			},
		},
		{
			name:   "runtime not found",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg: &workloadsv1alpha.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-rbg",
				},
			},
			role: &workloadsv1alpha.RoleSpec{
				Name: "test-role",
			},
			podSpec: &v1.PodTemplateSpec{},
			runtime: workloadsv1alpha.EngineRuntime{
				ProfileName: "non-existent-engine",
			},
			expectedError: true,
			verifyFunc:    nil,
		},
		{
			name:   "override container env and args",
			client: fakeClient,
			rbg: &workloadsv1alpha.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-rbg",
				},
			},
			role: &workloadsv1alpha.RoleSpec{
				Name: "test-role",
			},
			podSpec: &v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "sidecar-container",
							Image: "sidecar-image:latest",
						},
					},
				},
			},
			runtime: workloadsv1alpha.EngineRuntime{
				ProfileName: "test-engine",
				Containers: []v1.Container{
					{
						Name: "sidecar-container",
						Env: []v1.EnvVar{
							{
								Name:  "TEST_ENV",
								Value: "test-value",
							},
						},
						Args: []string{"--test-arg"},
					},
				},
			},
			expectedError: false,
			verifyFunc: func(t *testing.T, podSpec *v1.PodTemplateSpec) {
				// Check container was updated with env and args
				found := false
				for _, container := range podSpec.Spec.Containers {
					if container.Name == "sidecar-container" {
						found = true
						// Check env was added
						if len(container.Env) != 1 || container.Env[0].Name != "TEST_ENV" {
							t.Errorf("Expected TEST_ENV env var in container")
						}
						// Check args were added
						if len(container.Args) != 1 || container.Args[0] != "--test-arg" {
							t.Errorf("Expected --test-arg in container args")
						}
						break
					}
				}
				if !found {
					t.Errorf("Expected sidecar-container not found")
				}
			},
		},
		{
			name:   "override non-existent container",
			client: fakeClient,
			rbg: &workloadsv1alpha.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-rbg",
				},
			},
			role: &workloadsv1alpha.RoleSpec{
				Name: "test-role",
			},
			podSpec: &v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "main-container",
							Image: "main-image:latest",
						},
					},
				},
			},
			runtime: workloadsv1alpha.EngineRuntime{
				ProfileName: "test-engine",
				Containers: []v1.Container{
					{
						Name: "non-existent-container",
						Env: []v1.EnvVar{
							{
								Name:  "TEST_ENV",
								Value: "test-value",
							},
						},
					},
				},
			},
			expectedError: false,
			verifyFunc: func(t *testing.T, podSpec *v1.PodTemplateSpec) {
				// Should not have added the env to any container
				for _, container := range podSpec.Spec.Containers {
					if container.Name == "non-existent-container" {
						t.Errorf("Should not have added non-existent-container")
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				roleData := &RoleData{
					Spec: tt.role,
					OwnerInfo: OwnerInfo{
						Name:      tt.rbg.Name,
						Namespace: tt.rbg.Namespace,
						UID:       tt.rbg.UID,
					},
				}

				b := &SidecarBuilder{
					RoleData: roleData,
					Client:   tt.client,
				}

				ctx := log.IntoContext(context.TODO(), klog.NewKlogr())
				err := b.injectRuntime(ctx, tt.podSpec, tt.runtime)

				if (err != nil) != tt.expectedError {
					t.Errorf("SidecarBuilder.injectRuntime() error = %v, expectedError %v", err, tt.expectedError)
				}

				if tt.verifyFunc != nil {
					tt.verifyFunc(t, tt.podSpec)
				}
			},
		)
	}
}

func TestNewSidecarBuilder(t *testing.T) {
	// Define test scheme
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	rbg := &workloadsv1alpha.RoleBasedGroup{}
	role := &workloadsv1alpha.RoleSpec{}

	roleData := &RoleData{
		Spec: role,
		OwnerInfo: OwnerInfo{
			Name:      rbg.Name,
			Namespace: rbg.Namespace,
			UID:       rbg.UID,
		},
	}

	builder := NewSidecarBuilder(client, roleData)
	assert.NotEmpty(t, builder, "NewSidecarBuilder() should not return nil")
	assert.Equal(t, client, builder.Client)
	assert.Equal(t, roleData, builder.RoleData)
}

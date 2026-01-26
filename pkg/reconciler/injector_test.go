package reconciler

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDefaultInjector_InjectSidecar(t *testing.T) {
	// Initialize test scheme with required types
	testScheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(testScheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithRuntimeObjects(
			&workloadsv1alpha1.ClusterEngineRuntimeProfile{
				ObjectMeta: metav1.ObjectMeta{Name: "patio-runtime"},
				Spec: workloadsv1alpha1.ClusterEngineRuntimeProfileSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init-patio-runtime",
							Image: "init-container-image",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "patio-runtime",
							Image: "sidecar-image",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "patio-runtime-volume",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		).Build()

	rbg := &workloadsv1alpha1.RoleBasedGroup{
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{
				{
					Name: "test",
					EngineRuntimes: []workloadsv1alpha1.EngineRuntime{
						{
							ProfileName: "patio-runtime",
							Containers: []corev1.Container{
								{
									Name: "patio-runtime",
									Args: []string{"--foo=bar"},
									Env: []corev1.EnvVar{
										{
											Name:  "INFERENCE_ENGINE",
											Value: "SGLang",
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

	tests := []struct {
		name    string
		podSpec *corev1.PodTemplateSpec
		want    *corev1.PodTemplateSpec
	}{
		{
			name: "Add init & sidecar & volume to pod",
			podSpec: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test",
							Image: "test-image",
						},
					},
				},
			},
			want: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init-patio-runtime",
							Image: "init-container-image",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "test",
							Image: "test-image",
						},
						{
							Name:  "patio-runtime",
							Image: "sidecar-image",
							Args:  []string{"--foo=bar"},
							Env: []corev1.EnvVar{
								{
									Name:  "INFERENCE_ENGINE",
									Value: "SGLang",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "patio-runtime-volume",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
		{
			name: "Add duplicated init & sidecar & volume to pod",
			podSpec: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init-patio-runtime",
							Image: "init-container-image",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "test",
							Image: "test-image",
						},
						{
							Name:  "patio-runtime",
							Image: "sidecar-image",
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "patio-runtime-volume",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
			want: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "init-patio-runtime",
							Image: "init-container-image",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "test",
							Image: "test-image",
						},
						{
							Name:  "patio-runtime",
							Image: "sidecar-image",
							Args:  []string{"--foo=bar"},
							Env: []corev1.EnvVar{
								{
									Name:  "INFERENCE_ENGINE",
									Value: "SGLang",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "patio-runtime-volume",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				role, _ := rbg.GetRole("test")
				// Create RoleData
				roleData := &RoleData{
					Spec: role,
					OwnerInfo: OwnerInfo{
						Name:      rbg.Name,
						Namespace: rbg.Namespace,
					},
				}
				b := NewSidecarBuilder(fakeClient, roleData)
				err := b.Build(context.TODO(), tt.podSpec)
				if err != nil {
					t.Errorf("build error: %s", err.Error())
				}
				if !reflect.DeepEqual(tt.podSpec, tt.want) {
					t.Errorf("Build expect err, want %v, got %v", tt.want, tt.podSpec)
				}

			},
		)
	}
}

func TestDefaultInjector_InjectEnv(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name            string
		rbg             *workloadsv1alpha1.RoleBasedGroup
		role            *workloadsv1alpha1.RoleSpec
		initialPodSpec  *corev1.PodTemplateSpec
		expectedEnvVars []corev1.EnvVar
	}{
		{
			name: "Inject environment variables when they don't exist",
			// RBG with role that will generate some environment variables
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(3)),
						},
					},
				},
			},
			role: &workloadsv1alpha1.RoleSpec{
				Name:     "worker",
				Replicas: ptr.To(int32(3)),
			},
			// Container without the expected environment variables
			initialPodSpec: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main",
							Image: "test-image",
						},
					},
				},
			},
			// Expected environment variables to be added (sorted by name)
			expectedEnvVars: []corev1.EnvVar{
				{
					Name:  "GROUP_NAME",
					Value: "test-rbg",
				},
				{
					Name:  "ROLE_NAME",
					Value: "worker",
				},
			},
		},
		{
			name: "Merge environment variables preserving existing ones",
			// RBG with role that will generate some environment variables
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(3)),
						},
					},
				},
			},
			role: &workloadsv1alpha1.RoleSpec{
				Name:     "worker",
				Replicas: ptr.To(int32(3)),
			},
			// Container with existing environment variables
			initialPodSpec: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main",
							Image: "test-image",
							Env: []corev1.EnvVar{
								{
									Name:  "EXISTING_VAR",
									Value: "existing-value",
								},
								// This variable should be overwritten
								{
									Name:  "ROLE_NAME",
									Value: "old-value",
								},
							},
						},
					},
				},
			},
			// Expected environment variables - existing ones preserved, RBG ones overwritten
			expectedEnvVars: []corev1.EnvVar{
				{
					Name:  "EXISTING_VAR",
					Value: "existing-value",
				},
				{
					Name:  "GROUP_NAME",
					Value: "test-rbg",
				},
				{
					Name:  "ROLE_NAME",
					Value: "worker",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
				injector := NewDefaultInjector(scheme, fakeClient)

				// Create RoleData
				roleData := &RoleData{
					Spec: tt.role,
					OwnerInfo: OwnerInfo{
						Name:      tt.rbg.Name,
						Namespace: tt.rbg.Namespace,
					},
				}

				// Execute InjectEnv method
				err := injector.InjectEnv(context.Background(), tt.initialPodSpec, roleData)
				if err != nil {
					t.Fatalf("InjectEnv() error = %v", err)
				}

				// Check if environment variables are correctly injected and sorted
				if len(tt.initialPodSpec.Spec.Containers) > 0 {
					if diff := cmp.Diff(tt.expectedEnvVars, tt.initialPodSpec.Spec.Containers[0].Env); diff != "" {
						t.Errorf("Environment variables mismatch (-want +got):\n%s", diff)
					}
				}
			},
		)
	}
}

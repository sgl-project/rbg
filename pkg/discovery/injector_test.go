package discovery

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/wrappers"

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
				b := NewSidecarBuilder(fakeClient, rbg, role)
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

func TestDefaultInjector_InjectConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name            string
		rbg             *workloadsv1alpha1.RoleBasedGroup
		initialPodSpec  *corev1.PodTemplateSpec
		expectedVolumes []corev1.Volume
		expectedMounts  []corev1.VolumeMount
	}{
		{
			name: "Inject config volume and mount when they don't exist",
			rbg:  wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
			// Pod spec without config volume or mount
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
			// Expected volume to be added
			expectedVolumes: []corev1.Volume{
				{
					Name: "rbg-cluster-config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-rbg-test-role",
							},
							Items: []corev1.KeyToPath{
								{
									Key:  "config.yaml",
									Path: "config.yaml",
								},
							},
						},
					},
				},
			},
			// Expected mount to be added to container
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      "rbg-cluster-config",
					MountPath: "/etc/rbg",
					ReadOnly:  true,
				},
			},
		},
		{
			name: "Skip injection when volume and mount already exist",
			rbg:  wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
			// Pod spec that already has the config volume and mount
			initialPodSpec: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "rbg-cluster-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "test-rbg-test-role",
									},
									Items: []corev1.KeyToPath{
										{
											Key:  "config.yaml",
											Path: "config.yaml",
										},
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "main",
							Image: "test-image",
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "rbg-cluster-config",
									MountPath: "/etc/rbg",
									ReadOnly:  true,
								},
							},
						},
					},
				},
			},
			// Should remain unchanged
			expectedVolumes: []corev1.Volume{
				{
					Name: "rbg-cluster-config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-rbg-test-role",
							},
							Items: []corev1.KeyToPath{
								{
									Key:  "config.yaml",
									Path: "config.yaml",
								},
							},
						},
					},
				},
			},
			// Should remain unchanged
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      "rbg-cluster-config",
					MountPath: "/etc/rbg",
					ReadOnly:  true,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
				injector := NewDefaultInjector(scheme, fakeClient)

				// Execute InjectConfig method
				err := injector.InjectConfig(context.TODO(), tt.initialPodSpec, tt.rbg, &tt.rbg.Spec.Roles[0])
				if err != nil {
					t.Fatalf("InjectConfig() error = %v", err)
				}

				// Check if volumes are correctly injected
				if diff := cmp.Diff(
					tt.expectedVolumes, tt.initialPodSpec.Spec.Volumes,
					cmpopts.IgnoreFields(corev1.ConfigMapVolumeSource{}, "DefaultMode"),
				); diff != "" {
					t.Errorf("Volumes mismatch (-want +got):\n%s", diff)
				}

				// Check if volume mounts are correctly injected
				if len(tt.initialPodSpec.Spec.Containers) > 0 {
					if diff := cmp.Diff(
						tt.expectedMounts, tt.initialPodSpec.Spec.Containers[0].VolumeMounts,
					); diff != "" {
						t.Errorf("VolumeMounts mismatch (-want +got):\n%s", diff)
					}
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

				// Execute InjectEnv method
				err := injector.InjectEnv(context.Background(), tt.initialPodSpec, tt.rbg, tt.role)
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

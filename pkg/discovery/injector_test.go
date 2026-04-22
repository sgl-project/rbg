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

package discovery

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDefaultInjector_InjectSidecar(t *testing.T) {
	// Initialize test scheme with required types
	testScheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(testScheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithRuntimeObjects(
			&workloadsv1alpha2.ClusterEngineRuntimeProfile{
				ObjectMeta: metav1.ObjectMeta{Name: "patio-runtime"},
				Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
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

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{
					Name: "test",
					EngineRuntimes: []workloadsv1alpha2.EngineRuntime{
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
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name            string
		rbg             *workloadsv1alpha2.RoleBasedGroup
		initialPodSpec  *corev1.PodTemplateSpec
		expectedVolumes []corev1.Volume
		expectedMounts  []corev1.VolumeMount
	}{
		{
			name: "Inject config volume and mount when they don't exist",
			rbg:  wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
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
								Name: "test-rbg",
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
			rbg:  wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
			// Pod spec that already has the config volume and mount
			initialPodSpec: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "rbg-cluster-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "test-rbg",
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
								Name: "test-rbg",
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
		{
			name: "Refine mode mounts shared configmap for stateful role",
			rbg: func() *workloadsv1alpha2.RoleBasedGroup {
				rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
				rbg.SetDiscoveryConfigMode(constants.RefineDiscoveryConfigMode)
				return rbg
			}(),
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
			expectedVolumes: []corev1.Volume{
				{
					Name: "rbg-cluster-config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-rbg",
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
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      "rbg-cluster-config",
					MountPath: "/etc/rbg",
					ReadOnly:  true,
				},
			},
		},
		{
			name: "Refine mode skips config injection for stateless role",
			rbg: func() *workloadsv1alpha2.RoleBasedGroup {
				rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
				rbg.SetDiscoveryConfigMode(constants.RefineDiscoveryConfigMode)
				if rbg.Spec.Roles[0].Annotations == nil {
					rbg.Spec.Roles[0].Annotations = make(map[string]string)
				}
				rbg.Spec.Roles[0].Annotations[constants.RoleWorkloadTypeAnnotationKey] = "apps/v1/Deployment"
				return rbg
			}(),
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
			expectedVolumes: nil,
			expectedMounts:  nil,
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
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name            string
		rbg             *workloadsv1alpha2.RoleBasedGroup
		role            *workloadsv1alpha2.RoleSpec
		initialPodSpec  *corev1.PodTemplateSpec
		expectedEnvVars []corev1.EnvVar
	}{
		{
			name: "Inject environment variables when they don't exist",
			// RBG with role that will generate some environment variables
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(3)),
							Annotations: map[string]string{
								constants.RoleWorkloadTypeAnnotationKey: "apps/v1/StatefulSet",
							},
						},
					},
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name:     "worker",
				Replicas: ptr.To(int32(3)),
				Annotations: map[string]string{
					constants.RoleWorkloadTypeAnnotationKey: "apps/v1/StatefulSet",
				},
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
					Name:  "RBG_GROUP_NAME",
					Value: "test-rbg",
				},
				{
					Name: "RBG_ROLE_INDEX",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['apps.kubernetes.io/pod-index']",
						},
					},
				},
				{
					Name:  "RBG_ROLE_NAME",
					Value: "worker",
				},
			},
		},
		{
			name: "Merge environment variables preserving existing ones",
			// RBG with role that will generate some environment variables
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg",
					Namespace: "default",
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(3)),
							Annotations: map[string]string{
								constants.RoleWorkloadTypeAnnotationKey: "apps/v1/StatefulSet",
							},
						},
					},
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name:     "worker",
				Replicas: ptr.To(int32(3)),
				Annotations: map[string]string{
					constants.RoleWorkloadTypeAnnotationKey: "apps/v1/StatefulSet",
				},
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
									Name:  "RBG_ROLE_NAME",
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
					Name:  "RBG_GROUP_NAME",
					Value: "test-rbg",
				},
				{
					Name: "RBG_ROLE_INDEX",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['apps.kubernetes.io/pod-index']",
						},
					},
				},
				{
					Name:  "RBG_ROLE_NAME",
					Value: "worker",
				},
				{
					Name:  "EXISTING_VAR",
					Value: "existing-value",
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

func TestDefaultInjector_InjectEnv_UserEnvReferenceRBGVarsOrder(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{
					Name:     "worker",
					Replicas: ptr.To(int32(3)),
				},
			},
		},
	}
	role := &workloadsv1alpha2.RoleSpec{
		Name:     "worker",
		Replicas: ptr.To(int32(3)),
	}
	podSpec := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "main",
					Image: "test-image",
					Env: []corev1.EnvVar{
						{
							Name:  "DP_ADDRESS",
							Value: "$(RBG_ROLE_INSTANCE_NAME)-0.s-rbg.default",
						},
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	injector := NewDefaultInjector(scheme, fakeClient)

	if err := injector.InjectEnv(context.Background(), podSpec, rbg, role); err != nil {
		t.Fatalf("InjectEnv() error = %v", err)
	}

	rbgRoleInstanceNameIndex := -1
	dpAddressIndex := -1
	for i, envVar := range podSpec.Spec.Containers[0].Env {
		switch envVar.Name {
		case constants.EnvRBGRoleInstanceName:
			rbgRoleInstanceNameIndex = i
		case "DP_ADDRESS":
			dpAddressIndex = i
		}
	}

	if rbgRoleInstanceNameIndex == -1 {
		t.Fatalf("%s not found in merged env vars", constants.EnvRBGRoleInstanceName)
	}
	if dpAddressIndex == -1 {
		t.Fatal("DP_ADDRESS not found in merged env vars")
	}
	if dpAddressIndex <= rbgRoleInstanceNameIndex {
		t.Fatalf("DP_ADDRESS must be after %s for Kubernetes env expansion, got DP_ADDRESS index %d and %s index %d",
			constants.EnvRBGRoleInstanceName, dpAddressIndex, constants.EnvRBGRoleInstanceName, rbgRoleInstanceNameIndex)
	}
}

func TestDefaultInjector_InjectLeaderWorkerSetEnv(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "serving-v2hbfa5wai",
			Namespace: "project-infra",
		},
	}
	role := &workloadsv1alpha2.RoleSpec{
		Name: "integrate",
		Annotations: map[string]string{
			constants.RoleWorkloadTypeAnnotationKey: "workloads.x-k8s.io/v1alpha2/RoleInstanceSet",
		},
	}
	podSpec := &corev1.PodTemplateSpec{
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
						{
							Name:  constants.EnvRBGLeaderAddress,
							Value: "stale-leader-address",
						},
						{
							Name:  constants.EnvRBGRoleInstanceName,
							Value: "stale-instance-name",
						},
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	injector := NewDefaultInjector(scheme, fakeClient)

	if err := injector.InjectEnv(context.Background(), podSpec, rbg, role); err != nil {
		t.Fatalf("InjectEnv() error = %v", err)
	}
	if err := injector.InjectLeaderWorkerSetEnv(context.Background(), podSpec, rbg, role); err != nil {
		t.Fatalf("InjectLeaderWorkerSetEnv() error = %v", err)
	}

	expectedEnvVars := []corev1.EnvVar{
		{
			Name: constants.EnvRBGComponentIndex,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.labels['%s']", constants.ComponentIDLabelKey),
				},
			},
		},
		{
			Name: constants.EnvRBGComponentName,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.labels['%s']", constants.ComponentNameLabelKey),
				},
			},
		},
		{
			Name:  constants.EnvRBGGroupName,
			Value: rbg.Name,
		},
		{
			Name: constants.EnvRBGRoleIndex,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.labels['%s']", constants.RoleInstanceIndexLabelKey),
				},
			},
		},
		{
			Name: constants.EnvRBGRoleInstanceName,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.labels['%s']", constants.RoleInstanceNameLabelKey),
				},
			},
		},
		{
			Name:  constants.EnvRBGRoleName,
			Value: role.Name,
		},
		{
			Name:  constants.EnvRBGLeaderAddress,
			Value: fmt.Sprintf("$(%s)-0.%s.%s", constants.EnvRBGRoleInstanceName, rbg.GetServiceName(role), rbg.Namespace),
		},
		{
			Name: constants.EnvRBGIndex,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.labels['%s']", constants.ComponentIndexLabelKey),
				},
			},
		},
		{
			Name: constants.EnvRBGSize,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.labels['%s']", constants.ComponentSizeLabelKey),
				},
			},
		},
		{
			Name:  "EXISTING_VAR",
			Value: "existing-value",
		},
	}
	roleInstanceNameIndex := -1
	leaderAddressIndex := -1
	for i, envVar := range podSpec.Spec.Containers[0].Env {
		switch envVar.Name {
		case constants.EnvRBGRoleInstanceName:
			roleInstanceNameIndex = i
		case constants.EnvRBGLeaderAddress:
			leaderAddressIndex = i
		}
	}
	if roleInstanceNameIndex == -1 || leaderAddressIndex == -1 {
		t.Fatalf("expected both %s and %s in env, got: %+v", constants.EnvRBGRoleInstanceName, constants.EnvRBGLeaderAddress, podSpec.Spec.Containers[0].Env)
	}
	if roleInstanceNameIndex >= leaderAddressIndex {
		t.Fatalf("expected %s to appear before %s for Kubernetes env expansion, got indexes %d and %d", constants.EnvRBGRoleInstanceName, constants.EnvRBGLeaderAddress, roleInstanceNameIndex, leaderAddressIndex)
	}
	if diff := cmp.Diff(expectedEnvVars, podSpec.Spec.Containers[0].Env); diff != "" {
		t.Fatalf("Environment variables mismatch (-want +got):\n%s", diff)
	}
}

func TestMergeEnvVars(t *testing.T) {
	tests := []struct {
		name     string
		existing []corev1.EnvVar
		injected []corev1.EnvVar
		expected []corev1.EnvVar
	}{
		{
			name:     "both empty",
			existing: nil,
			injected: nil,
			expected: []corev1.EnvVar{},
		},
		{
			name:     "existing empty, injected has RBG vars",
			existing: nil,
			injected: []corev1.EnvVar{
				{Name: "RBG_GROUP_NAME", Value: "test"},
			},
			expected: []corev1.EnvVar{
				{Name: "RBG_GROUP_NAME", Value: "test"},
			},
		},
		{
			name: "injected empty, existing has user vars",
			existing: []corev1.EnvVar{
				{Name: "MY_VAR", Value: "my-value"},
			},
			injected: nil,
			expected: []corev1.EnvVar{
				{Name: "MY_VAR", Value: "my-value"},
			},
		},
		{
			name: "injected overrides existing RBG var",
			existing: []corev1.EnvVar{
				{Name: "RBG_GROUP_NAME", Value: "old"},
			},
			injected: []corev1.EnvVar{
				{Name: "RBG_GROUP_NAME", Value: "new"},
			},
			expected: []corev1.EnvVar{
				{Name: "RBG_GROUP_NAME", Value: "new"},
			},
		},
		{
			name: "injected overrides existing user var with same name",
			existing: []corev1.EnvVar{
				{Name: "MY_VAR", Value: "old"},
			},
			injected: []corev1.EnvVar{
				{Name: "MY_VAR", Value: "new"},
			},
			expected: []corev1.EnvVar{
				{Name: "MY_VAR", Value: "new"},
			},
		},
		{
			name: "base RBG vars before injected LWP vars before user vars",
			existing: []corev1.EnvVar{
				{Name: "USER_VAR", Value: "user"},
				{Name: "RBG_GROUP_NAME", Value: "my-rbg"},
				{Name: "RBG_ROLE_NAME", Value: "worker"},
			},
			injected: []corev1.EnvVar{
				{Name: "RBG_LWP_LEADER_ADDRESS", Value: "$(RBG_ROLE_INSTANCE_NAME)-0.svc.ns"},
				{Name: "RBG_LWP_GROUP_SIZE", Value: "3"},
			},
			expected: []corev1.EnvVar{
				{Name: "RBG_GROUP_NAME", Value: "my-rbg"},
				{Name: "RBG_ROLE_NAME", Value: "worker"},
				{Name: "RBG_LWP_LEADER_ADDRESS", Value: "$(RBG_ROLE_INSTANCE_NAME)-0.svc.ns"},
				{Name: "RBG_LWP_GROUP_SIZE", Value: "3"},
				{Name: "USER_VAR", Value: "user"},
			},
		},
		{
			name: "existing LWP vars placed after injected vars when not overridden",
			existing: []corev1.EnvVar{
				{Name: "RBG_LWP_LEADER_ADDRESS", Value: "stale-address"},
				{Name: "RBG_GROUP_NAME", Value: "my-rbg"},
				{Name: "USER_VAR", Value: "user"},
			},
			injected: []corev1.EnvVar{
				{Name: "RBG_LWP_GROUP_SIZE", Value: "3"},
			},
			expected: []corev1.EnvVar{
				{Name: "RBG_GROUP_NAME", Value: "my-rbg"},
				{Name: "RBG_LWP_GROUP_SIZE", Value: "3"},
				{Name: "RBG_LWP_LEADER_ADDRESS", Value: "stale-address"},
				{Name: "USER_VAR", Value: "user"},
			},
		},
		{
			name: "two-phase injection: base RBG before LWP so $(VAR) expansion works",
			existing: []corev1.EnvVar{
				{Name: "RBG_ROLE_INSTANCE_NAME", Value: "instance-0"},
				{Name: "RBG_GROUP_NAME", Value: "my-rbg"},
				{Name: "USER_VAR", Value: "user"},
			},
			injected: []corev1.EnvVar{
				{Name: "RBG_LWP_LEADER_ADDRESS", Value: "$(RBG_ROLE_INSTANCE_NAME)-0.svc.ns"},
			},
			expected: []corev1.EnvVar{
				{Name: "RBG_ROLE_INSTANCE_NAME", Value: "instance-0"},
				{Name: "RBG_GROUP_NAME", Value: "my-rbg"},
				{Name: "RBG_LWP_LEADER_ADDRESS", Value: "$(RBG_ROLE_INSTANCE_NAME)-0.svc.ns"},
				{Name: "USER_VAR", Value: "user"},
			},
		},
		{
			name: "injected overrides existing LWP var while keeping other existing LWP vars",
			existing: []corev1.EnvVar{
				{Name: "RBG_LWP_LEADER_ADDRESS", Value: "stale"},
				{Name: "RBG_LWP_GROUP_SIZE", Value: "2"},
				{Name: "RBG_ROLE_NAME", Value: "worker"},
				{Name: "USER_VAR", Value: "user"},
			},
			injected: []corev1.EnvVar{
				{Name: "RBG_LWP_LEADER_ADDRESS", Value: "$(RBG_ROLE_INSTANCE_NAME)-0.svc.ns"},
			},
			expected: []corev1.EnvVar{
				{Name: "RBG_ROLE_NAME", Value: "worker"},
				{Name: "RBG_LWP_LEADER_ADDRESS", Value: "$(RBG_ROLE_INSTANCE_NAME)-0.svc.ns"},
				{Name: "RBG_LWP_GROUP_SIZE", Value: "2"},
				{Name: "USER_VAR", Value: "user"},
			},
		},
		{
			name: "user var referencing RBG base var is placed after it",
			existing: []corev1.EnvVar{
				{Name: "DP_ADDRESS", Value: "$(RBG_ROLE_INSTANCE_NAME)-0.svc.ns"},
			},
			injected: []corev1.EnvVar{
				{Name: "RBG_ROLE_INSTANCE_NAME", Value: "instance-0"},
				{Name: "RBG_GROUP_NAME", Value: "my-rbg"},
			},
			expected: []corev1.EnvVar{
				{Name: "RBG_ROLE_INSTANCE_NAME", Value: "instance-0"},
				{Name: "RBG_GROUP_NAME", Value: "my-rbg"},
				{Name: "DP_ADDRESS", Value: "$(RBG_ROLE_INSTANCE_NAME)-0.svc.ns"},
			},
		},
		{
			name: "user var referencing LWP var is placed after it",
			existing: []corev1.EnvVar{
				{Name: "MY_LEADER", Value: "$(RBG_LWP_LEADER_ADDRESS)/path"},
			},
			injected: []corev1.EnvVar{
				{Name: "RBG_LWP_LEADER_ADDRESS", Value: "leader-0.svc.ns"},
			},
			expected: []corev1.EnvVar{
				{Name: "RBG_LWP_LEADER_ADDRESS", Value: "leader-0.svc.ns"},
				{Name: "MY_LEADER", Value: "$(RBG_LWP_LEADER_ADDRESS)/path"},
			},
		},
		{
			name: "duplicate in existing is deduplicated preserving last",
			existing: []corev1.EnvVar{
				{Name: "USER_VAR", Value: "first"},
				{Name: "USER_VAR", Value: "second"},
			},
			injected: []corev1.EnvVar{
				{Name: "RBG_GROUP_NAME", Value: "my-rbg"},
			},
			expected: []corev1.EnvVar{
				{Name: "RBG_GROUP_NAME", Value: "my-rbg"},
				{Name: "USER_VAR", Value: "second"},
			},
		},
		{
			name: "duplicate in injected is deduplicated preserving last",
			existing: []corev1.EnvVar{
				{Name: "USER_VAR", Value: "user"},
			},
			injected: []corev1.EnvVar{
				{Name: "RBG_GROUP_NAME", Value: "first"},
				{Name: "RBG_GROUP_NAME", Value: "second"},
			},
			expected: []corev1.EnvVar{
				{Name: "RBG_GROUP_NAME", Value: "second"},
				{Name: "USER_VAR", Value: "user"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeEnvVars(tt.existing, tt.injected)
			if diff := cmp.Diff(tt.expected, result); diff != "" {
				t.Errorf("mergeEnvVars mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIsRBGInjectedEnvVar(t *testing.T) {
	tests := []struct {
		name     string
		envName  string
		expected bool
	}{
		{name: "RBG prefix", envName: "RBG_GROUP_NAME", expected: true},
		{name: "RBG_LWP prefix", envName: "RBG_LWP_LEADER_ADDRESS", expected: true},
		{name: "short name less than 4 chars", envName: "RBG", expected: false},
		{name: "non-RBG prefix", envName: "MY_VAR", expected: false},
		{name: "empty string", envName: "", expected: false},
		{name: "similar but not RBG prefix", envName: "XRBG_VAR", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRBGInjectedEnvVar(tt.envName); got != tt.expected {
				t.Errorf("isRBGInjectedEnvVar(%q) = %v, want %v", tt.envName, got, tt.expected)
			}
		})
	}
}

func TestIsRBGLWPEnvVar(t *testing.T) {
	tests := []struct {
		name     string
		envName  string
		expected bool
	}{
		{name: "RBG_LWP prefix", envName: "RBG_LWP_LEADER_ADDRESS", expected: true},
		{name: "RBG_LWP another", envName: "RBG_LWP_GROUP_SIZE", expected: true},
		{name: "RBG prefix only", envName: "RBG_GROUP_NAME", expected: false},
		{name: "short name less than 8 chars", envName: "RBG_LWP", expected: false},
		{name: "non-RBG prefix", envName: "MY_VAR", expected: false},
		{name: "empty string", envName: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRBGLWPEnvVar(tt.envName); got != tt.expected {
				t.Errorf("isRBGLWPEnvVar(%q) = %v, want %v", tt.envName, got, tt.expected)
			}
		})
	}
}

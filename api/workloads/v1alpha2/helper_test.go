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

package v1alpha2

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// TestRoleSpec_GetResolvedTemplate tests the GetResolvedTemplate method
// which resolves base templates for both templateRef and inline template modes.
func TestRoleSpec_GetResolvedTemplate(t *testing.T) {
	// Create a base RoleTemplate
	baseTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "nginx",
					Image: "nginx:latest",
					Env: []corev1.EnvVar{
						{Name: "BASE_ENV", Value: "base_value"},
					},
				},
			},
		},
	}

	// Create template patch
	patchData := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []map[string]interface{}{
				{
					"name": "nginx",
					"env": []map[string]string{
						{"name": "PATCHED_ENV", "value": "patched_value"},
					},
				},
			},
		},
	}
	patchBytes, _ := json.Marshal(patchData)
	templatePatch := &runtime.RawExtension{Raw: patchBytes}

	emptyPatch := &runtime.RawExtension{Raw: []byte("{}")}

	tests := []struct {
		name           string
		useTemplateRef bool
		templatePatch  *runtime.RawExtension
		expectError    bool
		errorMsg       string
		verifyEnv      func(t *testing.T, envs []corev1.EnvVar)
	}{
		{
			name:           "inline template mode",
			useTemplateRef: false,
			expectError:    false,
			verifyEnv: func(t *testing.T, envs []corev1.EnvVar) {
				assert.Len(t, envs, 1)
				assert.Equal(t, "BASE_ENV", envs[0].Name)
			},
		},
		{
			name:           "templateRef mode with patch",
			useTemplateRef: true,
			templatePatch:  templatePatch,
			expectError:    false,
			verifyEnv: func(t *testing.T, envs []corev1.EnvVar) {
				// Strategic merge patch merges env vars by name
				assert.Len(t, envs, 2)
				// Order may vary based on patch application
				envNames := make([]string, len(envs))
				for i, env := range envs {
					envNames[i] = env.Name
				}
				assert.Contains(t, envNames, "BASE_ENV")
				assert.Contains(t, envNames, "PATCHED_ENV")
			},
		},
		{
			name:           "templateRef mode with empty patch",
			useTemplateRef: true,
			templatePatch:  emptyPatch,
			expectError:    false,
			verifyEnv: func(t *testing.T, envs []corev1.EnvVar) {
				// Should only have base env
				assert.Len(t, envs, 1)
				assert.Equal(t, "BASE_ENV", envs[0].Name)
			},
		},
		{
			name:           "templateRef mode with nil patch",
			useTemplateRef: true,
			templatePatch:  nil,
			expectError:    false,
			verifyEnv: func(t *testing.T, envs []corev1.EnvVar) {
				// Should only have base env (no patch applied)
				assert.Len(t, envs, 1)
				assert.Equal(t, "BASE_ENV", envs[0].Name)
			},
		},
		{
			name:           "templateRef mode with non-existent template",
			useTemplateRef: true,
			templatePatch:  templatePatch,
			expectError:    true,
			errorMsg:       "not found in spec.roleTemplates",
			verifyEnv:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build role
			role := &RoleSpec{
				Name:     "test-role",
				Replicas: ptr.To(int32(1)),
			}

			// Build RBG
			rbg := &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					RoleTemplates: []RoleTemplate{
						{
							Name:     "test-template",
							Template: baseTemplate,
						},
					},
				},
			}

			if tt.useTemplateRef {
				templateRefName := "test-template"
				if tt.expectError {
					templateRefName = "non-existent-template"
				}
				role.Pattern = Pattern{
					StandalonePattern: &StandalonePattern{
						TemplateSource: TemplateSource{
							TemplateRef: &TemplateRef{
								Name:  templateRefName,
								Patch: tt.templatePatch,
							},
						},
					},
				}
			} else {
				inlineTemplate := baseTemplate.DeepCopy()
				role.Pattern = Pattern{
					StandalonePattern: &StandalonePattern{
						TemplateSource: TemplateSource{
							Template: inlineTemplate,
						},
					},
				}
			}

			// Call GetResolvedTemplate
			result, err := role.GetResolvedTemplate(rbg)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, result)

			// Verify the result is a deep copy (not the same pointer)
			if tt.useTemplateRef {
				assert.NotSame(t, &baseTemplate, &result)
			}

			// Verify env vars
			if tt.verifyEnv != nil {
				tt.verifyEnv(t, result.Spec.Containers[0].Env)
			}
		})
	}
}

// TestRoleSpec_GetResolvedTemplate_DeepCopyIsolation verifies that
// GetResolvedTemplate returns a deep copy that doesn't affect the source.
func TestRoleSpec_GetResolvedTemplate_DeepCopyIsolation(t *testing.T) {
	// Create a base RoleTemplate
	baseTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "nginx",
					Image: "nginx:latest",
					Env: []corev1.EnvVar{
						{Name: "ORIGINAL", Value: "value"},
					},
				},
			},
		},
	}

	roleTemplate := RoleTemplate{
		Name:     "isolation-test",
		Template: baseTemplate,
	}

	role := &RoleSpec{
		Name:     "test-role",
		Replicas: ptr.To(int32(1)),
		Pattern: Pattern{
			StandalonePattern: &StandalonePattern{
				TemplateSource: TemplateSource{
					TemplateRef: &TemplateRef{
						Name:  "isolation-test",
						Patch: &runtime.RawExtension{Raw: []byte("{}")},
					},
				},
			},
		},
	}

	rbg := &RoleBasedGroup{
		Spec: RoleBasedGroupSpec{
			RoleTemplates: []RoleTemplate{roleTemplate},
		},
	}

	// Get resolved template
	result, err := role.GetResolvedTemplate(rbg)
	assert.NoError(t, err)

	// Mutate the result
	result.Spec.Containers[0].Env = append(result.Spec.Containers[0].Env, corev1.EnvVar{
		Name:  "MUTATED",
		Value: "mutated_value",
	})

	// Verify the original RoleTemplate was not affected
	assert.Len(t, rbg.Spec.RoleTemplates[0].Template.Spec.Containers[0].Env, 1)
	assert.Equal(t, "ORIGINAL", rbg.Spec.RoleTemplates[0].Template.Spec.Containers[0].Env[0].Name)
}

// TestRoleSpec_GetResolvedTemplate_NoTemplateError tests that
// GetResolvedTemplate returns an error when no template is set.
func TestRoleSpec_GetResolvedTemplate_NoTemplateError(t *testing.T) {
	role := &RoleSpec{
		Name:     "test-role",
		Replicas: ptr.To(int32(1)),
		// No template set
	}

	rbg := &RoleBasedGroup{}

	_, err := role.GetResolvedTemplate(rbg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "has no template or templateRef set")
}

// Test_applyStrategicMergePatch tests the applyStrategicMergePatch helper function.
func Test_applyStrategicMergePatch(t *testing.T) {
	base := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "nginx",
					Image: "nginx:latest",
					Env: []corev1.EnvVar{
						{Name: "EXISTING", Value: "existing_value"},
					},
				},
			},
		},
	}

	patchData := map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []map[string]interface{}{
				{
					"name": "nginx",
					"env": []map[string]string{
						{"name": "NEW", "value": "new_value"},
					},
				},
			},
		},
	}
	patchBytes, _ := json.Marshal(patchData)

	tests := []struct {
		name        string
		patch       *runtime.RawExtension
		expectError bool
		verifyEnv   func(t *testing.T, envs []corev1.EnvVar)
	}{
		{
			name:        "nil patch",
			patch:       nil,
			expectError: false,
			verifyEnv: func(t *testing.T, envs []corev1.EnvVar) {
				assert.Len(t, envs, 1)
				assert.Equal(t, "EXISTING", envs[0].Name)
			},
		},
		{
			name:        "empty patch",
			patch:       &runtime.RawExtension{Raw: []byte("{}")},
			expectError: false,
			verifyEnv: func(t *testing.T, envs []corev1.EnvVar) {
				assert.Len(t, envs, 1)
				assert.Equal(t, "EXISTING", envs[0].Name)
			},
		},
		{
			name:        "valid patch",
			patch:       &runtime.RawExtension{Raw: patchBytes},
			expectError: false,
			verifyEnv: func(t *testing.T, envs []corev1.EnvVar) {
				// Strategic merge patch merges env vars by name
				assert.Len(t, envs, 2)
				// Order may vary based on patch application
				envNames := make([]string, len(envs))
				for i, env := range envs {
					envNames[i] = env.Name
				}
				assert.Contains(t, envNames, "EXISTING")
				assert.Contains(t, envNames, "NEW")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := applyStrategicMergePatch(base, tt.patch)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			// Verify result is a deep copy
			assert.NotSame(t, &base, &result)

			if tt.verifyEnv != nil {
				tt.verifyEnv(t, result.Spec.Containers[0].Env)
			}
		})
	}
}

// TestRoleSpec_GetWorkloadType tests the GetWorkloadType method
// which returns the workload type from annotation or defaults to RoleInstanceSet.
func TestRoleSpec_GetWorkloadType(t *testing.T) {
	tests := []struct {
		name         string
		annotations  map[string]string
		expectedType string
	}{
		{
			name:         "no annotation - defaults to RoleInstanceSet",
			annotations:  nil,
			expectedType: constants.RoleInstanceSetWorkloadType,
		},
		{
			name:         "empty annotations - defaults to RoleInstanceSet",
			annotations:  map[string]string{},
			expectedType: constants.RoleInstanceSetWorkloadType,
		},
		{
			name: "annotation set to StatefulSet",
			annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "apps/v1/StatefulSet",
			},
			expectedType: "apps/v1/StatefulSet",
		},
		{
			name: "annotation set to Deployment",
			annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "apps/v1/Deployment",
			},
			expectedType: "apps/v1/Deployment",
		},
		{
			name: "annotation set to LeaderWorkerSet",
			annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet",
			},
			expectedType: "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet",
		},
		{
			name: "annotation set to RoleInstanceSet",
			annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "workloads.x-k8s.io/v1alpha2/RoleInstanceSet",
			},
			expectedType: "workloads.x-k8s.io/v1alpha2/RoleInstanceSet",
		},
		{
			name: "other annotation present but not workload type - defaults to RoleInstanceSet",
			annotations: map[string]string{
				"other-annotation": "some-value",
			},
			expectedType: constants.RoleInstanceSetWorkloadType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role := &RoleSpec{
				Name:        "test-role",
				Annotations: tt.annotations,
			}

			result := role.GetWorkloadType()
			assert.Equal(t, tt.expectedType, result)
		})
	}
}

// TestRoleSpec_GetWorkloadSpec tests the GetWorkloadSpec method
// which parses the workload type annotation and returns WorkloadSpec.
func TestRoleSpec_GetWorkloadSpec(t *testing.T) {
	tests := []struct {
		name               string
		annotations        map[string]string
		expectedAPIVersion string
		expectedKind       string
	}{
		{
			name:               "no annotation - defaults to RoleInstanceSet",
			annotations:        nil,
			expectedAPIVersion: "workloads.x-k8s.io/v1alpha2",
			expectedKind:       "RoleInstanceSet",
		},
		{
			name: "annotation set to StatefulSet",
			annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "apps/v1/StatefulSet",
			},
			expectedAPIVersion: "apps/v1",
			expectedKind:       "StatefulSet",
		},
		{
			name: "annotation set to Deployment",
			annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "apps/v1/Deployment",
			},
			expectedAPIVersion: "apps/v1",
			expectedKind:       "Deployment",
		},
		{
			name: "annotation set to LeaderWorkerSet",
			annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet",
			},
			expectedAPIVersion: "leaderworkerset.x-k8s.io/v1",
			expectedKind:       "LeaderWorkerSet",
		},
		{
			name: "annotation set to RoleInstanceSet",
			annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "workloads.x-k8s.io/v1alpha2/RoleInstanceSet",
			},
			expectedAPIVersion: "workloads.x-k8s.io/v1alpha2",
			expectedKind:       "RoleInstanceSet",
		},
		{
			name: "malformed annotation without slash - returns RoleInstanceSet defaults",
			annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "malformed-value",
			},
			expectedAPIVersion: "workloads.x-k8s.io/v1alpha2",
			expectedKind:       "RoleInstanceSet",
		},
		{
			name: "empty annotation value - returns RoleInstanceSet defaults",
			annotations: map[string]string{
				constants.RoleWorkloadTypeAnnotationKey: "",
			},
			expectedAPIVersion: "workloads.x-k8s.io/v1alpha2",
			expectedKind:       "RoleInstanceSet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role := &RoleSpec{
				Name:        "test-role",
				Annotations: tt.annotations,
			}

			result := role.GetWorkloadSpec()
			assert.Equal(t, tt.expectedAPIVersion, result.APIVersion)
			assert.Equal(t, tt.expectedKind, result.Kind)
		})
	}
}

func TestValidateRollingUpdate_AllowsReplicasZero(t *testing.T) {
	rbg := &RoleBasedGroup{
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{
					Name:     "worker",
					Replicas: ptr.To[int32](0),
					RolloutStrategy: &RolloutStrategy{
						Type: RollingUpdateStrategyType,
						RollingUpdate: &RollingUpdate{
							Partition: ptr.To(intstr.FromInt32(0)),
						},
					},
				},
			},
		},
	}

	assert.NoError(t, ValidateRollingUpdate(rbg))
}

func TestRoleBasedGroupValidator_ValidateCreateName(t *testing.T) {
	tests := []struct {
		name        string
		rbgName     string
		wantErr     bool
		errContains string
	}{
		{
			name:    "allows DNS label name",
			rbgName: "test-rbg",
		},
		{
			name:        "rejects DNS subdomain name with dot",
			rbgName:     "test.rbg",
			wantErr:     true,
			errContains: "metadata.name: \"test.rbg\" is not a valid DNS label",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validator := &RoleBasedGroupValidator{}
			rbg := &RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: tt.rbgName, Namespace: "default"},
			}

			_, err := validator.ValidateCreate(context.Background(), rbg)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestRoleBasedGroupValidator_ValidateUpdateScalingAdapterReplicas(t *testing.T) {
	const (
		rbgName   = "test-rbg"
		namespace = "default"
		roleName  = "engine"
	)

	makeRBG := func(replicas int32, enableScalingAdapter bool) *RoleBasedGroup {
		role := RoleSpec{
			Name:     roleName,
			Replicas: ptr.To(replicas),
		}
		if enableScalingAdapter {
			role.ScalingAdapter = &ScalingAdapter{Enable: true}
		}
		return &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: rbgName, Namespace: namespace},
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{role},
			},
		}
	}
	makeAdapter := func(replicas *int32) *RoleBasedGroupScalingAdapter {
		return &RoleBasedGroupScalingAdapter{
			ObjectMeta: metav1.ObjectMeta{Name: rbgName + "-" + roleName, Namespace: namespace},
			Spec: RoleBasedGroupScalingAdapterSpec{
				Replicas: replicas,
			},
		}
	}

	tests := []struct {
		name        string
		oldRBG      *RoleBasedGroup
		newRBG      *RoleBasedGroup
		adapter     *RoleBasedGroupScalingAdapter
		wantErr     bool
		errContains string
	}{
		{
			name:        "rejects manual replicas change when scaling adapter has different replicas",
			oldRBG:      makeRBG(2, true),
			newRBG:      makeRBG(3, true),
			adapter:     makeAdapter(ptr.To[int32](2)),
			wantErr:     true,
			errContains: "cannot be changed to 3 while scalingAdapter.enable is true",
		},
		{
			name:    "allows replicas change when scaling adapter already has same replicas",
			oldRBG:  makeRBG(2, true),
			newRBG:  makeRBG(3, true),
			adapter: makeAdapter(ptr.To[int32](3)),
		},
		{
			name:    "allows replicas change when scaling adapter is disabled",
			oldRBG:  makeRBG(2, false),
			newRBG:  makeRBG(3, false),
			adapter: makeAdapter(ptr.To[int32](2)),
		},
		{
			name:    "allows unchanged replicas even when adapter differs",
			oldRBG:  makeRBG(2, true),
			newRBG:  makeRBG(2, true),
			adapter: makeAdapter(ptr.To[int32](4)),
		},
		{
			name:   "allows replicas change before adapter is created",
			oldRBG: makeRBG(2, true),
			newRBG: makeRBG(3, true),
		},
		{
			name:    "allows replicas change before adapter replicas is set",
			oldRBG:  makeRBG(2, true),
			newRBG:  makeRBG(3, true),
			adapter: makeAdapter(nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			assert.NoError(t, AddToScheme(scheme))

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.adapter != nil {
				builder = builder.WithObjects(tt.adapter)
			}
			validator := &RoleBasedGroupValidator{Client: builder.Build()}

			_, err := validator.ValidateUpdate(context.Background(), tt.oldRBG, tt.newRBG)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			assert.NoError(t, err)
		})
	}
}

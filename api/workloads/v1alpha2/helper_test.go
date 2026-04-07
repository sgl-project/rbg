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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
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

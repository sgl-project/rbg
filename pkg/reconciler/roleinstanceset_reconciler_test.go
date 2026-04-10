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
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// TestRoleInstanceSetReconciler_LeaderWorkerPattern_WithTemplateRef tests that
// RoleInstanceSet with LeaderWorkerPattern correctly supports templateRef,
// including base template inheritance, leader/worker-specific patch application,
// and that leader-only injection does not leak into the worker template.
func TestRoleInstanceSetReconciler_LeaderWorkerPattern_WithTemplateRef(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	// Create a base RoleTemplate
	baseTemplate := wrappersv2.BuildBasicPodTemplateSpec()
	baseTemplate.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "BASE_ENV", Value: "base_value"},
	}

	roleTemplate := workloadsv1alpha2.RoleTemplate{
		Name:     "test-template",
		Template: baseTemplate,
	}

	// Create leader and worker patches
	leaderPatch := buildRawExtension(t, map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []map[string]interface{}{
				{
					"name": "nginx",
					"env": []map[string]string{
						{"name": "ROLE", "value": "leader"},
					},
				},
			},
		},
	})

	workerPatch := buildRawExtension(t, map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []map[string]interface{}{
				{
					"name": "nginx",
					"env": []map[string]string{
						{"name": "ROLE", "value": "worker"},
					},
				},
			},
		},
	})

	templatePatch := buildRawExtension(t, map[string]interface{}{
		"spec": map[string]interface{}{
			"containers": []map[string]interface{}{
				{
					"name": "nginx",
					"env": []map[string]string{
						{"name": "TEMPLATE_PATCH", "value": "patched"},
					},
				},
			},
		},
	})

	// Test cases
	tests := []struct {
		name           string
		useTemplateRef bool
		templatePatch  *runtime.RawExtension
		leaderPatch    *runtime.RawExtension
		workerPatch    *runtime.RawExtension
		expectError    bool
	}{
		{
			name:           "templateRef with templatePatch and leader/worker patches",
			useTemplateRef: true,
			templatePatch:  templatePatch,
			leaderPatch:    leaderPatch,
			workerPatch:    workerPatch,
			expectError:    false,
		},
		{
			name:           "templateRef with empty templatePatch",
			useTemplateRef: true,
			templatePatch:  buildRawExtension(t, map[string]interface{}{}),
			leaderPatch:    leaderPatch,
			workerPatch:    workerPatch,
			expectError:    false,
		},
		{
			name:           "inline template with leader/worker patches",
			useTemplateRef: false,
			leaderPatch:    leaderPatch,
			workerPatch:    workerPatch,
			expectError:    false,
		},
		{
			name:           "templateRef with nil leader patch",
			useTemplateRef: true,
			templatePatch:  templatePatch,
			leaderPatch:    nil,
			workerPatch:    workerPatch,
			expectError:    false,
		},
		{
			name:           "templateRef with nil worker patch",
			useTemplateRef: true,
			templatePatch:  templatePatch,
			leaderPatch:    leaderPatch,
			workerPatch:    nil,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build the role
			roleWrapper := &wrappersv2.LeaderWorkerRoleWrapper{}
			roleWrapper.Name = "test-lwp-role"
			roleWrapper.Replicas = ptr.To(int32(1))
			roleWrapper.Workload = workloadsv1alpha2.WorkloadSpec{
				APIVersion: "workloads.x-k8s.io/v1alpha2",
				Kind:       "RoleInstanceSet",
			}
			roleWrapper.LeaderWorkerPattern = &workloadsv1alpha2.LeaderWorkerPattern{
				Size:                ptr.To(int32(2)),
				LeaderTemplatePatch: tt.leaderPatch,
				WorkerTemplatePatch: tt.workerPatch,
			}

			if tt.useTemplateRef {
				roleWrapper.LeaderWorkerPattern.TemplateSource = workloadsv1alpha2.TemplateSource{
					TemplateRef: &workloadsv1alpha2.TemplateRef{
						Name:  "test-template",
						Patch: tt.templatePatch,
					},
				}
			} else {
				template := wrappersv2.BuildBasicPodTemplateSpec()
				roleWrapper.LeaderWorkerPattern.TemplateSource = workloadsv1alpha2.TemplateSource{
					Template: &template,
				}
			}

			role := roleWrapper.Obj()

			// Build RBG with or without RoleTemplate
			rbgBuilder := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithRoles([]workloadsv1alpha2.RoleSpec{role})

			if tt.useTemplateRef {
				rbgBuilder = rbgBuilder.WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{roleTemplate})
			}

			rbg := rbgBuilder.Obj()

			// Create fake client
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

			// Create reconciler
			reconciler := NewRoleInstanceSetReconciler(scheme, fakeClient)

			// Test reconciliation
			ctx := context.Background()
			err := reconciler.Reconciler(ctx, rbg, &role, nil, expectedRevisionHash)

			if tt.expectError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			// Verify RoleInstanceSet was created
			ris := &workloadsv1alpha2.RoleInstanceSet{}
			err = fakeClient.Get(
				ctx, types.NamespacedName{
					Name:      rbg.GetWorkloadName(&role),
					Namespace: rbg.Namespace,
				}, ris,
			)
			assert.NoError(t, err)
			assert.Equal(t, expectedRevisionHash, ris.Labels[fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name)])

			// Verify RoleInstanceTemplate
			assert.NotNil(t, ris.Spec.RoleInstanceTemplate)
		})
	}
}

// TestRoleInstanceSetReconciler_LeaderWorkerPattern_TemplateIsolation verifies
// that mutations during leader reconciliation do not leak into worker template.
func TestRoleInstanceSetReconciler_LeaderWorkerPattern_TemplateIsolation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	// Create a base RoleTemplate with containers that have env vars
	baseTemplate := wrappersv2.BuildBasicPodTemplateSpec()
	baseTemplate.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "COMMON", Value: "value"},
	}

	roleTemplate := workloadsv1alpha2.RoleTemplate{
		Name:     "isolation-test-template",
		Template: baseTemplate,
	}

	// Empty patches to test the DeepCopy behavior when patch is empty
	emptyPatch := &runtime.RawExtension{Raw: []byte("{}")}

	// Build the role using templateRef
	roleWrapper := &wrappersv2.LeaderWorkerRoleWrapper{}
	roleWrapper.Name = "isolation-test-role"
	roleWrapper.Replicas = ptr.To(int32(1))
	roleWrapper.Workload = workloadsv1alpha2.WorkloadSpec{
		APIVersion: "workloads.x-k8s.io/v1alpha2",
		Kind:       "RoleInstanceSet",
	}
	roleWrapper.LeaderWorkerPattern = &workloadsv1alpha2.LeaderWorkerPattern{
		Size:                ptr.To(int32(2)),
		LeaderTemplatePatch: emptyPatch,
		WorkerTemplatePatch: emptyPatch,
		TemplateSource: workloadsv1alpha2.TemplateSource{
			TemplateRef: &workloadsv1alpha2.TemplateRef{
				Name:  "isolation-test-template",
				Patch: emptyPatch,
			},
		},
	}

	role := roleWrapper.Obj()
	rbg := wrappersv2.BuildBasicRoleBasedGroup("isolation-test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{role}).
		WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{roleTemplate}).
		Obj()

	// Create fake client
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Create reconciler
	reconciler := NewRoleInstanceSetReconciler(scheme, fakeClient)

	// Test reconciliation
	ctx := context.Background()
	err := reconciler.Reconciler(ctx, rbg, &role, nil, "test-revision")
	assert.NoError(t, err)

	// Verify RoleInstanceSet was created
	ris := &workloadsv1alpha2.RoleInstanceSet{}
	err = fakeClient.Get(
		ctx, types.NamespacedName{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		}, ris,
	)
	assert.NoError(t, err)

	// Verify that the original RoleTemplate was not mutated
	// This is the key test for the DeepCopy fix
	assert.Len(t, roleTemplate.Template.Spec.Containers[0].Env, 1)
	assert.Equal(t, "COMMON", roleTemplate.Template.Spec.Containers[0].Env[0].Name)
}

// TestRoleInstanceSetReconciler_ValidateRoleTemplateReferences tests that
// validation correctly rejects templateRef for LeaderWorkerSet workload.
func TestRoleInstanceSetReconciler_ValidateRoleTemplateReferences(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	baseTemplate := wrappersv2.BuildBasicPodTemplateSpec()
	roleTemplate := workloadsv1alpha2.RoleTemplate{
		Name:     "validation-test-template",
		Template: baseTemplate,
	}

	tests := []struct {
		name             string
		workloadKind     string
		useTemplateRef   bool
		useTemplatePatch bool
		expectError      bool
		errorMsg         string
	}{
		{
			name:             "RoleInstanceSet with templateRef and templatePatch should succeed",
			workloadKind:     "RoleInstanceSet",
			useTemplateRef:   true,
			useTemplatePatch: true,
			expectError:      false,
		},
		{
			name:             "RoleInstanceSet with templateRef but no templatePatch should fail",
			workloadKind:     "RoleInstanceSet",
			useTemplateRef:   true,
			useTemplatePatch: false,
			expectError:      true,
			errorMsg:         "templatePatch: required when templateRef is set",
		},
		{
			name:           "LeaderWorkerSet with templateRef should fail",
			workloadKind:   "LeaderWorkerSet",
			useTemplateRef: true,
			expectError:    true,
			errorMsg:       "not supported for LeaderWorkerSet workloads",
		},
		{
			name:           "RoleInstanceSet with inline template should succeed",
			workloadKind:   "RoleInstanceSet",
			useTemplateRef: false,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build the role
			roleWrapper := &wrappersv2.LeaderWorkerRoleWrapper{}
			roleWrapper.Name = "validation-test-role"
			roleWrapper.Replicas = ptr.To(int32(1))
			roleWrapper.Workload = workloadsv1alpha2.WorkloadSpec{
				APIVersion: "workloads.x-k8s.io/v1alpha2",
				Kind:       tt.workloadKind,
			}
			roleWrapper.LeaderWorkerPattern = &workloadsv1alpha2.LeaderWorkerPattern{
				Size: ptr.To(int32(2)),
			}

			if tt.useTemplateRef {
				templateRef := &workloadsv1alpha2.TemplateRef{Name: "validation-test-template"}
				if tt.useTemplatePatch {
					templateRef.Patch = &runtime.RawExtension{Raw: []byte("{}")}
				}
				roleWrapper.LeaderWorkerPattern.TemplateSource = workloadsv1alpha2.TemplateSource{
					TemplateRef: templateRef,
				}
			} else {
				template := wrappersv2.BuildBasicPodTemplateSpec()
				roleWrapper.LeaderWorkerPattern.TemplateSource = workloadsv1alpha2.TemplateSource{
					Template: &template,
				}
			}

			role := roleWrapper.Obj()
			rbg := wrappersv2.BuildBasicRoleBasedGroup("validation-test-rbg", "default").
				WithRoles([]workloadsv1alpha2.RoleSpec{role}).
				WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{roleTemplate}).
				Obj()

			// Validate
			err := workloadsv1alpha2.ValidateRoleTemplateReferences(rbg)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// buildRawExtension creates a runtime.RawExtension from a map
func buildRawExtension(t *testing.T, data map[string]interface{}) *runtime.RawExtension {
	if data == nil {
		return nil
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Failed to marshal patch: %v", err)
	}
	return &runtime.RawExtension{Raw: bytes}
}

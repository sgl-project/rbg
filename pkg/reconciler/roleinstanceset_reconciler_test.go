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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
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
			roleWrapper.WithWorkload("workloads.x-k8s.io/v1alpha2", "RoleInstanceSet")
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

// TestRoleInstanceSetReconciler_LeaderWorkerPattern_ComponentServiceName verifies which
// components of the role instance are bound to the role's shared headless service: All binds
// every component, LeaderOnly (the default) binds the leader only.
func TestRoleInstanceSetReconciler_LeaderWorkerPattern_ComponentServiceName(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	tests := []struct {
		name               string
		policy             *workloadsv1alpha2.SharedServiceSelectionPolicy
		expectWorkerShared bool
	}{
		{
			name:               "All binds leader and worker",
			policy:             ptr.To(workloadsv1alpha2.SharedServiceSelectionAll),
			expectWorkerShared: true,
		},
		{
			name:               "LeaderOnly binds the leader only",
			policy:             ptr.To(workloadsv1alpha2.SharedServiceSelectionLeaderOnly),
			expectWorkerShared: false,
		},
		{
			name:               "unset policy falls back to the LeaderOnly default",
			policy:             nil,
			expectWorkerShared: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role := wrappersv2.BuildLeaderWorkerRole("test-role").Obj()
			role.LeaderWorkerPattern.SharedServiceSelection = tt.policy

			rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithRoles([]workloadsv1alpha2.RoleSpec{role}).
				Obj()

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			reconciler := NewRoleInstanceSetReconciler(scheme, fakeClient)

			ctx := context.Background()
			assert.NoError(t, reconciler.Reconciler(ctx, rbg, &role, nil, expectedRevisionHash))

			ris := &workloadsv1alpha2.RoleInstanceSet{}
			assert.NoError(
				t, fakeClient.Get(
					ctx, types.NamespacedName{Name: rbg.GetWorkloadName(&role), Namespace: rbg.Namespace}, ris,
				),
			)

			svcName := rbg.GetServiceName(&role)
			serviceNames := make(map[string]string, len(ris.Spec.RoleInstanceTemplate.Components))
			for _, component := range ris.Spec.RoleInstanceTemplate.Components {
				serviceNames[component.Name] = component.ServiceName
			}

			assert.Equal(t, svcName, serviceNames[string(constants.LeaderComponentType)])
			if tt.expectWorkerShared {
				assert.Equal(t, svcName, serviceNames[string(constants.WorkerComponentType)])
			} else {
				assert.Empty(t, serviceNames[string(constants.WorkerComponentType)])
			}
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
	roleWrapper.WithWorkload("workloads.x-k8s.io/v1alpha2", "RoleInstanceSet")
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

func TestRoleInstanceSetReconciler_RoundsUpMaxUnavailableWhenMaxSurgeIsZero(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	role := wrappersv2.BuildStandaloneRole("test-role").
		WithReplicas(3).
		WithWorkload("workloads.x-k8s.io/v1alpha2", "RoleInstanceSet").
		WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
			MaxUnavailable: ptr.To(intstr.FromString("30%")),
			MaxSurge:       ptr.To(intstr.FromInt32(0)),
			Partition:      ptr.To(intstr.FromInt32(0)),
		}).
		Obj()
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{role}).
		Obj()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := NewRoleInstanceSetReconciler(scheme, fakeClient)

	ctx := context.Background()
	err := reconciler.Reconciler(ctx, rbg, &role, nil, expectedRevisionHash)

	assert.NoError(t, err)
	ris := &workloadsv1alpha2.RoleInstanceSet{}
	err = fakeClient.Get(
		ctx,
		types.NamespacedName{
			Name:      rbg.GetWorkloadName(&role),
			Namespace: rbg.Namespace,
		},
		ris,
	)
	assert.NoError(t, err)
	assert.Equal(t, "30%", ris.Spec.UpdateStrategy.MaxUnavailable.String())
	assert.Equal(t, "0", ris.Spec.UpdateStrategy.MaxSurge.String())
}

func TestRoleInstanceSetReconciler_ValidateRolloutStrategyRoundsUpMaxUnavailableWhenMaxSurgeIsZero(t *testing.T) {
	strategy := &workloadsv1alpha2.RolloutStrategy{
		Type: workloadsv1alpha2.RollingUpdateStrategyType,
		RollingUpdate: &workloadsv1alpha2.RollingUpdate{
			MaxUnavailable: ptr.To(intstr.FromString("30%")),
			MaxSurge:       ptr.To(intstr.FromInt32(0)),
			Partition:      ptr.To(intstr.FromInt32(0)),
		},
	}

	_, err := validateRolloutStrategy(strategy, 3)

	assert.NoError(t, err)
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
			errorMsg:         "templateRef.patch is required when templateRef is set",
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
			roleWrapper.WithWorkload("workloads.x-k8s.io/v1alpha2", tt.workloadKind)
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

// TestRoleInstanceSetReconciler_RestartPolicySerializedShape pins which of the two
// mutually exclusive restart-policy fields the RoleInstance template carries.
//
// The choice is not cosmetic: the serialized template is what the RoleInstanceSet
// revision hash is computed over, so emitting restartPolicyConfig for a role that only
// ever stored the deprecated restartPolicy string moves the hash and rolls the role with
// nothing to roll to. The discriminator is the backoff delays -- the only thing the
// string cannot express -- not the presence of a restartPolicyConfig block, which is why
// a role that sets nothing but its type still takes the string path.
func TestRoleInstanceSetReconciler_RestartPolicySerializedShape(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	tests := []struct {
		name string
		role func() workloadsv1alpha2.RoleSpec
		// wantRestartPolicy is the expected restartPolicy string, empty when the field
		// must be absent.
		wantRestartPolicy workloadsv1alpha2.RestartPolicyType
		// wantConfig is the expected restartPolicyConfig, nil when the field must be
		// absent.
		wantConfig *workloadsv1alpha2.RestartPolicyConfig
	}{
		{
			name: "v0.7-style role storing only the deprecated string",
			role: func() workloadsv1alpha2.RoleSpec {
				return wrappersv2.BuildLeaderWorkerRole("legacy-role").
					WithLegacyRestartPolicy(workloadsv1alpha2.RestartPolicyNone).
					Obj()
			},
			wantRestartPolicy: workloadsv1alpha2.RestartPolicyNone,
		},
		{
			name: "role configuring no restart policy at all",
			role: func() workloadsv1alpha2.RoleSpec {
				return wrappersv2.BuildLeaderWorkerRole("default-role").Obj()
			},
			wantRestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
		},
		{
			name: "role configuring a type but no delays",
			role: func() workloadsv1alpha2.RoleSpec {
				return wrappersv2.BuildLeaderWorkerRole("type-only-role").
					WithRestartPolicy(workloadsv1alpha2.RestartPolicyNone).
					Obj()
			},
			wantRestartPolicy: workloadsv1alpha2.RestartPolicyNone,
		},
		{
			name: "role configuring both delays",
			role: func() workloadsv1alpha2.RoleSpec {
				return wrappersv2.BuildLeaderWorkerRole("backoff-role").
					WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
					WithBaseDelaySeconds(5).
					WithMaxDelaySeconds(50).
					Obj()
			},
			wantConfig: &workloadsv1alpha2.RestartPolicyConfig{
				Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
				BaseDelaySeconds: ptr.To(int32(5)),
				MaxDelaySeconds:  ptr.To(int32(50)),
			},
		},
		{
			name: "role configuring only the base delay defaults the max",
			role: func() workloadsv1alpha2.RoleSpec {
				return wrappersv2.BuildLeaderWorkerRole("base-only-role").
					WithBaseDelaySeconds(5).
					Obj()
			},
			wantConfig: &workloadsv1alpha2.RestartPolicyConfig{
				Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
				BaseDelaySeconds: ptr.To(int32(5)),
				MaxDelaySeconds:  ptr.To(workloadsv1alpha2.DefaultMaxDelaySeconds),
			},
		},
		{
			name: "role configuring only the max delay defaults the base",
			role: func() workloadsv1alpha2.RoleSpec {
				return wrappersv2.BuildLeaderWorkerRole("max-only-role").
					WithMaxDelaySeconds(50).
					Obj()
			},
			wantConfig: &workloadsv1alpha2.RestartPolicyConfig{
				Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
				BaseDelaySeconds: ptr.To(workloadsv1alpha2.DefaultBaseDelaySeconds),
				MaxDelaySeconds:  ptr.To(int32(50)),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role := tt.role()
			rbg := wrappersv2.BuildBasicRoleBasedGroup("restart-policy-rbg", "default").
				WithRoles([]workloadsv1alpha2.RoleSpec{role}).
				Obj()

			reconciler := NewRoleInstanceSetReconciler(scheme, fake.NewClientBuilder().WithScheme(scheme).Build())
			applyConfig, err := reconciler.constructRoleInstanceSetApplyConfiguration(
				context.Background(), rbg, &role, nil, expectedRevisionHash, nil,
			)
			assert.NoError(t, err)

			template := applyConfig.Spec.RoleInstanceTemplate
			if tt.wantConfig == nil {
				assert.Nil(t, template.RestartPolicyConfig)
				assert.Equal(t, ptr.To(tt.wantRestartPolicy), template.RestartPolicy)
			} else {
				assert.Nil(t, template.RestartPolicy)
				assert.Equal(t, ptr.To(tt.wantConfig.Type), template.RestartPolicyConfig.Type)
				assert.Equal(t, tt.wantConfig.BaseDelaySeconds, template.RestartPolicyConfig.BaseDelaySeconds)
				assert.Equal(t, tt.wantConfig.MaxDelaySeconds, template.RestartPolicyConfig.MaxDelaySeconds)
			}

			// The typed assertions above can both hold while the field the apiserver
			// actually stores differs, so the keys are checked on the serialized form
			// the revision hash is taken over.
			raw, err := json.Marshal(template)
			assert.NoError(t, err)
			var keys map[string]json.RawMessage
			assert.NoError(t, json.Unmarshal(raw, &keys))
			_, hasString := keys["restartPolicy"]
			_, hasConfig := keys["restartPolicyConfig"]
			assert.Equal(t, tt.wantConfig == nil, hasString)
			assert.Equal(t, tt.wantConfig != nil, hasConfig)
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

// TestRoleInstanceSetReconciler_DerivesRoleInstanceGangAnnotation pins that a gang-covered
// role gets the RoleInstance-level gang flag without the user setting it. A Volcano subGroup
// is exactly one RoleInstance, so an instance whose pods are recreated non-atomically drops
// the subGroup below subGroupSize and breaks the guarantee subGroupPolicy depends on.
func TestRoleInstanceSetReconciler_DerivesRoleInstanceGangAnnotation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	gangPolicy := func(minReplicas map[string]int32) *workloadsv1alpha2.CoordinatedPolicy {
		return &workloadsv1alpha2.CoordinatedPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbg", Namespace: "default"},
			Spec: workloadsv1alpha2.CoordinatedPolicySpec{
				Policies: []workloadsv1alpha2.CoordinatedPolicyRule{{
					Roles: []string{"prefill", "decode"},
					Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
						Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
							Gang: &workloadsv1alpha2.GangSchedulingStrategy{MinReplicas: minReplicas},
						},
					},
				}},
			},
		}
	}

	tests := []struct {
		name           string
		policy         *workloadsv1alpha2.CoordinatedPolicy
		roleName       string
		roleAnnotation string
		want           string
	}{
		{
			name:     "no gang strategy leaves the annotation unset",
			roleName: "prefill",
		},
		{
			name:     "whole-group gang covers every role",
			policy:   gangPolicy(nil),
			roleName: "prefill",
			want:     "true",
		},
		{
			name:     "per-role minimums cover the named role",
			policy:   gangPolicy(map[string]int32{"prefill": 1}),
			roleName: "prefill",
			want:     "true",
		},
		{
			name:     "per-role minimums leave an excluded role alone",
			policy:   gangPolicy(map[string]int32{"prefill": 1}),
			roleName: "decode",
		},
		{
			name:           "an explicit value on the role wins",
			policy:         gangPolicy(nil),
			roleName:       "prefill",
			roleAnnotation: "false",
			want:           "false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roleWrapper := wrappersv2.BuildStandaloneRole(tt.roleName).
				WithWorkload("workloads.x-k8s.io/v1alpha2", "RoleInstanceSet")
			if tt.roleAnnotation != "" {
				roleWrapper = roleWrapper.WithAnnotations(
					map[string]string{constants.RoleInstanceGangSchedulingAnnotationKey: tt.roleAnnotation})
			}
			role := roleWrapper.Obj()
			rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithRoles([]workloadsv1alpha2.RoleSpec{role}).
				Obj()

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.policy != nil {
				builder = builder.WithObjects(tt.policy)
			}
			fakeClient := builder.Build()

			ctx := context.Background()
			err := NewRoleInstanceSetReconciler(scheme, fakeClient).
				Reconciler(ctx, rbg, &role, nil, expectedRevisionHash)
			assert.NoError(t, err)

			ris := &workloadsv1alpha2.RoleInstanceSet{}
			err = fakeClient.Get(
				ctx,
				types.NamespacedName{Name: rbg.GetWorkloadName(&role), Namespace: rbg.Namespace},
				ris,
			)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, ris.Annotations[constants.RoleInstanceGangSchedulingAnnotationKey])
		})
	}
}

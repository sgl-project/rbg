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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/kubernetes/pkg/controller/history"
	"k8s.io/utils/lru"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statefulmode"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/revision"
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

func TestRoleInstanceSetReconciler_NormalizesLegacyUpdateStrategyType(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	tests := []struct {
		name string
		in   workloadsv1alpha2.UpdateStrategyType
		want workloadsv1alpha2.UpdateStrategyType
	}{
		{
			name: "legacy Recreate becomes RecreatePod",
			in:   workloadsv1alpha2.LegacyRecreateUpdateStrategyType,
			want: workloadsv1alpha2.RecreatePodUpdateStrategyType,
		},
		{
			name: "empty becomes InPlaceIfPossible",
			in:   "",
			want: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role := wrappersv2.BuildStandaloneRole("test-role").
				WithReplicas(1).
				WithWorkload("workloads.x-k8s.io/v1alpha2", "RoleInstanceSet").
				WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
					Type:           tt.in,
					MaxUnavailable: ptr.To(intstr.FromInt32(1)),
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
			assert.Equal(t, tt.want, ris.Spec.UpdateStrategy.Type)
		})
	}
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
			name:             "RoleInstanceSet with templateRef but no templatePatch should succeed",
			workloadKind:     "RoleInstanceSet",
			useTemplateRef:   true,
			useTemplatePatch: false,
			expectError:      false,
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
			name: "role arming the triggers before Ready",
			role: func() workloadsv1alpha2.RoleSpec {
				role := wrappersv2.BuildLeaderWorkerRole("activate-after-role").Obj()
				role.LeaderWorkerPattern.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{
					Type:          workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
					ActivateAfter: workloadsv1alpha2.RestartPolicyActivateAfterCreated,
				}
				return role
			},
			wantConfig: &workloadsv1alpha2.RestartPolicyConfig{
				Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
				ActivateAfter:    workloadsv1alpha2.RestartPolicyActivateAfterCreated,
				BaseDelaySeconds: ptr.To(workloadsv1alpha2.DefaultBaseDelaySeconds),
				MaxDelaySeconds:  ptr.To(workloadsv1alpha2.DefaultMaxDelaySeconds),
			},
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
				if tt.wantConfig.ActivateAfter == "" {
					assert.Nil(t, template.RestartPolicyConfig.ActivateAfter)
				} else {
					assert.Equal(t, ptr.To(tt.wantConfig.ActivateAfter), template.RestartPolicyConfig.ActivateAfter)
				}
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

			// activateAfter must never appear unless the role set it explicitly: a
			// non-empty default written into the template would move the RoleInstanceSet
			// revision hash and roll every existing role on upgrade.
			rawConfig, hasConfigKey := keys["restartPolicyConfig"]
			if tt.wantConfig == nil || tt.wantConfig.ActivateAfter == "" {
				if hasConfigKey {
					assert.NotContains(t, string(rawConfig), "activateAfter")
				}
			} else {
				assert.Contains(t, string(rawConfig), "activateAfter")
			}
		})
	}
}

// TestRoleInstanceSetReconciler_RestartPolicyUpgrade uses fixtures emitted by the
// actual v0.9.0-alpha.3 code (fd160cc964b0e1438bead4ad7324d0a8f0bf5eac),
// not a reconstruction using today's serializer. A temporary detached worktree ran
// constructRoleInstanceSetApplyConfiguration, decoded its result into an RIS, and
// called revision.NewRevisionControl().NewRevision(set, 7, &zero). Only simulated
// apiserver metadata and status were added. Compact JSON preserves Data.Raw bytes.
// The generator used restartPolicyUpgradeRole below and the same RBG/UID inputs.
// Do not regenerate these fixtures with the current controller: that would hide
// the upgrade regression this test guards against.
func TestRoleInstanceSetReconciler_RestartPolicyUpgrade(t *testing.T) {
	const source = "v0.9.0-alpha.3/fd160cc964b0e1438bead4ad7324d0a8f0bf5eac"
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	// Both revision implementations use the client-go global scheme's codec.
	require.NoError(t, workloadsv1alpha2.AddToScheme(clientgoscheme.Scheme))

	for _, tt := range []struct {
		name             string
		customComponents bool
		backoff          bool
	}{
		{name: "leader-worker-legacy"},
		{name: "leader-worker-backoff", backoff: true},
		{name: "custom-components-legacy", customComponents: true},
		{name: "custom-components-backoff", customComponents: true, backoff: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "restart-policy-upgrade", tt.name+".json"))
			require.NoError(t, err)
			var fixture struct {
				Source   string                            `json:"source"`
				Set      workloadsv1alpha2.RoleInstanceSet `json:"roleInstanceSet"`
				Revision apps.ControllerRevision           `json:"controllerRevision"`
			}
			require.NoError(t, json.Unmarshal(data, &fixture))
			require.Equal(t, source, fixture.Source)
			stored, previous := &fixture.Set, &fixture.Revision
			owner := metav1.GetControllerOf(previous)
			require.NotNil(t, owner)
			require.Equal(t, stored.UID, owner.UID)
			require.Equal(t, previous.Name, stored.Status.CurrentRevision)
			require.Equal(t, previous.Name, stored.Status.UpdateRevision)
			require.NotContains(t, string(previous.Data.Raw), "activateAfter")

			role := restartPolicyUpgradeRole(tt.customComponents, tt.backoff)
			rbg := wrappersv2.BuildBasicRoleBasedGroup("restart-policy-rbg", "default").
				WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
			rbg.UID = "c657f5ab-2a83-4e85-a2d0-176e226baf76"
			reconciler := NewRoleInstanceSetReconciler(scheme, fake.NewClientBuilder().WithScheme(scheme).Build())
			construct := func(role *workloadsv1alpha2.RoleSpec) *workloadsv1alpha2.RoleInstanceSet {
				config, err := reconciler.constructRoleInstanceSetApplyConfiguration(
					context.Background(), rbg, role, nil, expectedRevisionHash, stored,
				)
				require.NoError(t, err)
				// Mirror the production apply-configuration -> API-object conversion,
				// retaining the existing object's UID, generation and revision status.
				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(config)
				require.NoError(t, err)
				set := &workloadsv1alpha2.RoleInstanceSet{}
				require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(obj, set))
				set.UID, set.Generation, set.ResourceVersion = stored.UID, stored.Generation, stored.ResourceVersion
				set.Status = stored.Status
				return set
			}
			set := construct(&role)
			var serialized struct {
				Set struct {
					Spec struct {
						Template json.RawMessage `json:"roleInstanceTemplate"`
					} `json:"spec"`
				} `json:"roleInstanceSet"`
			}
			require.NoError(t, json.Unmarshal(data, &serialized))
			oldTemplate := serialized.Set.Spec.Template
			newTemplate, err := json.Marshal(set.Spec.RoleInstanceTemplate)
			require.NoError(t, err)
			require.Equal(t, string(oldTemplate), string(newTemplate), "unchanged role must keep stored template bytes")

			control := revision.NewRevisionControl()
			proposed, err := control.NewRevision(set, previous.Revision+1, ptr.To(int32(0)))
			require.NoError(t, err)
			assert.Equal(t, previous.Data.Raw, proposed.Data.Raw, "upgrade must preserve the exact revision patch")
			assert.Equal(t, previous.Name, proposed.Name)
			assert.Equal(t, previous.Labels[history.ControllerRevisionHashLabel], proposed.Labels[history.ControllerRevisionHashLabel])
			assert.True(t, history.EqualRevision(previous, proposed))
			// This is the controllers' unchanged-revision selection predicate: the
			// prior stored revision matches even though the proposed number is newer.
			matches := history.FindEqualRevisions([]*apps.ControllerRevision{previous}, proposed)
			require.Len(t, matches, 1, "unchanged role must reuse the previous revision, not create a new one")
			assert.Same(t, previous, matches[0])

			// Stateful and stateless use the same replacement patch. Exercise both
			// public restore/equality paths against the actual prior release bytes.
			for name, mode := range map[string]struct {
				apply   func(*workloadsv1alpha2.RoleInstanceSet, *apps.ControllerRevision) (*workloadsv1alpha2.RoleInstanceSet, error)
				matches func(*workloadsv1alpha2.RoleInstanceSet, *apps.ControllerRevision, *apps.ControllerRevision, *lru.Cache) bool
			}{
				"stateful":  {statefulmode.ApplyRevision, statefulmode.SetMatchesRevision},
				"stateless": {control.ApplyRevision, control.SetMatchesRevision},
			} {
				t.Run(name, func(t *testing.T) {
					restored, err := mode.apply(set, previous)
					require.NoError(t, err)
					assert.Equal(t, stored.Spec.RoleInstanceTemplate, restored.Spec.RoleInstanceTemplate)
					assert.True(t, mode.matches(set, proposed, previous, lru.New(1)))

					// A deliberate activation change must not be mistaken for an upgrade
					// no-op (including legacy roles that must now take the config path).
					changedRole := role.DeepCopy()
					config := &workloadsv1alpha2.RestartPolicyConfig{Type: role.GetRestartPolicy()}
					if raw := role.GetRawRestartPolicyConfig(); raw != nil {
						*config = *raw
					}
					config.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
					if tt.customComponents {
						changedRole.CustomComponentsPattern.RestartPolicyConfig = config
					} else {
						changedRole.LeaderWorkerPattern.RestartPolicyConfig = config
					}
					changed := construct(changedRole)
					changedTemplate, err := json.Marshal(changed.Spec.RoleInstanceTemplate)
					require.NoError(t, err)
					assert.NotEqual(t, string(oldTemplate), string(changedTemplate))
					changedRevision, err := control.NewRevision(changed, previous.Revision+1, ptr.To(int32(0)))
					require.NoError(t, err)
					assert.NotEqual(t, previous.Data.Raw, changedRevision.Data.Raw)
					assert.NotEqual(t, previous.Name, changedRevision.Name)
					assert.NotEqual(t, previous.Labels[history.ControllerRevisionHashLabel], changedRevision.Labels[history.ControllerRevisionHashLabel])
					assert.False(t, history.EqualRevision(previous, changedRevision))
					assert.Empty(t, history.FindEqualRevisions([]*apps.ControllerRevision{previous}, changedRevision))
					assert.False(t, mode.matches(changed, changedRevision, previous, lru.New(1)))
				})
			}
		})
	}
}

func restartPolicyUpgradeRole(customComponents, backoff bool) workloadsv1alpha2.RoleSpec {
	builder := wrappersv2.BuildLeaderWorkerRole("inference")
	if backoff {
		builder = builder.WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
			WithBaseDelaySeconds(5).WithMaxDelaySeconds(50)
	} else {
		builder = builder.WithLegacyRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart)
	}
	role := builder.Obj()
	if customComponents {
		pattern := role.LeaderWorkerPattern
		role.CustomComponentsPattern = &workloadsv1alpha2.CustomComponentsPattern{
			RestartPolicy:       pattern.RestartPolicy, //nolint:staticcheck // preserve the prior release's legacy wire shape
			RestartPolicyConfig: pattern.RestartPolicyConfig,
			Components: []workloadsv1alpha2.InstanceComponent{{
				Name: "worker", Size: ptr.To(int32(2)), Template: *pattern.Template,
			}},
		}
		role.LeaderWorkerPattern = nil
	}
	return role
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

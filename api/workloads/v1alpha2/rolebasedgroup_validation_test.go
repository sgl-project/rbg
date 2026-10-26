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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// newFakeValidatorClient returns a client that knows the v1alpha2 types, which the
// validator needs in order to look up a role's RoleBasedGroupScalingAdapter.
func newFakeValidatorClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func TestValidateNoDeprecatedWorkloadTypes(t *testing.T) {
	tests := []struct {
		name        string
		roles       []RoleSpec
		expectError bool
		errContains string
		// fieldPath defaults to "spec.roles" when empty.
		fieldPath string
	}{
		{
			name: "RoleInstanceSet only - no error",
			roles: []RoleSpec{
				{Name: "worker"},
			},
			expectError: false,
		},
		{
			// A v1alpha1 role that names RoleInstanceSet explicitly converts to this
			// annotation, which is how v1alpha1 stays usable with the deprecated types off.
			name: "explicit RoleInstanceSet annotation - no error",
			roles: []RoleSpec{
				{
					Name:        "worker",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.RoleInstanceSetWorkloadType},
				},
			},
			expectError: false,
		},
		{
			name: "Deployment workload type - error",
			roles: []RoleSpec{
				{
					Name:        "worker",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
				},
			},
			expectError: true,
			errContains: "apps/v1/Deployment",
		},
		{
			name: "StatefulSet workload type - error",
			roles: []RoleSpec{
				{
					Name:        "worker",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
				},
			},
			expectError: true,
			errContains: "apps/v1/StatefulSet",
		},
		{
			name: "LeaderWorkerSet workload type - error",
			roles: []RoleSpec{
				{
					Name:        "worker",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.LeaderWorkerSetWorkloadType},
				},
			},
			expectError: true,
			errContains: "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet",
		},
		{
			name: "mixed roles with one deprecated - error",
			roles: []RoleSpec{
				{Name: "role-a"},
				{
					Name:        "role-b",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
				},
			},
			expectError: true,
			errContains: "role-b",
		},
		{
			name: "multiple deprecated roles - error with all",
			roles: []RoleSpec{
				{
					Name:        "role-a",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
				},
				{
					Name:        "role-b",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
				},
			},
			expectError: true,
			errContains: "role-a",
		},
		{
			name:        "empty roles - no error",
			roles:       []RoleSpec{},
			expectError: false,
		},
		{
			name: "field path reflects where the roles live",
			roles: []RoleSpec{
				{
					Name:        "worker",
					Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
				},
			},
			expectError: true,
			errContains: "spec.groupTemplate.spec.roles[0]",
			fieldPath:   "spec.groupTemplate.spec.roles",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fieldPath := tt.fieldPath
			if fieldPath == "" {
				fieldPath = "spec.roles"
			}
			err := validateNoDeprecatedWorkloadTypes(fieldPath, tt.roles)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				// The v1alpha1 schema defaults the workload field, so the error must
				// explain why a user who never set one still lands here, and name the
				// two ways out.
				assert.Contains(t, err.Error(), "the v1alpha1 schema defaults spec.roles[].workload")
				assert.Contains(t, err.Error(), "workload.kind=RoleInstanceSet")
				assert.Contains(t, err.Error(), "controller.deprecatedWorkloadTypes.enabled=true")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateRoleDependencies(t *testing.T) {
	tests := []struct {
		name        string
		roles       []RoleSpec
		wantErr     bool
		errContains string
	}{
		{
			name: "valid dependency",
			roles: []RoleSpec{
				{Name: "head"},
				{Name: "worker", Dependencies: []string{"head"}},
			},
		},
		{
			name: "dependency references unknown role",
			roles: []RoleSpec{
				{Name: "worker", Dependencies: []string{"ghost"}},
			},
			wantErr:     true,
			errContains: `role "worker" depends on unknown role "ghost"`,
		},
		{
			name: "self dependency",
			roles: []RoleSpec{
				{Name: "worker", Dependencies: []string{"worker"}},
			},
			wantErr:     true,
			errContains: `role "worker" cannot depend on itself`,
		},
		{
			name: "cyclic dependency",
			roles: []RoleSpec{
				{Name: "head", Dependencies: []string{"worker"}},
				{Name: "worker", Dependencies: []string{"head"}},
			},
			wantErr:     true,
			errContains: "dependency cycle detected: head -> worker -> head",
		},
		{
			name: "cyclic dependency starting mid path",
			roles: []RoleSpec{
				{Name: "head", Dependencies: []string{"worker"}},
				{Name: "worker", Dependencies: []string{"sidecar"}},
				{Name: "sidecar", Dependencies: []string{"worker"}},
			},
			wantErr:     true,
			errContains: "dependency cycle detected: worker -> sidecar -> worker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rbg := &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: tt.roles,
				},
			}

			err := ValidateRoleDependencies(rbg)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestRoleBasedGroupValidator_ValidateCreateRoleDependencies(t *testing.T) {
	rbg := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{Name: "worker", Replicas: ptr.To(int32(1)), Dependencies: []string{"ghost"}},
			},
		},
	}
	v := &RoleBasedGroupValidator{EnableDeprecatedWorkloadTypes: true}

	_, err := v.ValidateCreate(context.Background(), rbg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `role "worker" depends on unknown role "ghost"`)
}

func TestRoleBasedGroupValidator_ValidateUpdateRoleDependencies(t *testing.T) {
	oldRBG := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{Name: "head", Replicas: ptr.To(int32(1))},
				{Name: "worker", Replicas: ptr.To(int32(1))},
			},
		},
	}
	newRBG := oldRBG.DeepCopy()
	newRBG.Spec.Roles[0].Dependencies = []string{"worker"}
	newRBG.Spec.Roles[1].Dependencies = []string{"head"}
	v := &RoleBasedGroupValidator{
		Client:                        fake.NewClientBuilder().Build(),
		EnableDeprecatedWorkloadTypes: true,
	}

	_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dependency cycle detected: head -> worker -> head")
}

func TestRoleBasedGroupSetValidator_ValidateCreateRoleDependencies(t *testing.T) {
	rbgs := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
		Spec: RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(1)),
			GroupTemplate: RoleBasedGroupTemplateSpec{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{Name: "worker", Replicas: ptr.To(int32(1)), Dependencies: []string{"ghost"}},
					},
				},
			},
		},
	}
	v := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}

	_, err := v.ValidateCreate(context.Background(), rbgs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `spec.groupTemplate.spec.roles[0].dependencies[0]`)
	assert.Contains(t, err.Error(), `role "worker" depends on unknown role "ghost"`)
}

func TestRoleBasedGroupSetValidator_ValidateUpdateRoleDependencies(t *testing.T) {
	oldRBGS := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
		Spec: RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(1)),
			GroupTemplate: RoleBasedGroupTemplateSpec{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{Name: "head", Replicas: ptr.To(int32(1))},
						{Name: "worker", Replicas: ptr.To(int32(1))},
					},
				},
			},
		},
	}
	newRBGS := oldRBGS.DeepCopy()
	newRBGS.Spec.GroupTemplate.Spec.Roles[0].Dependencies = []string{"worker"}
	newRBGS.Spec.GroupTemplate.Spec.Roles[1].Dependencies = []string{"head"}
	v := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}

	_, err := v.ValidateUpdate(context.Background(), oldRBGS, newRBGS)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.groupTemplate.spec.roles: dependency cycle detected: head -> worker -> head")
}

func TestRoleBasedGroupValidator_ValidateCreate_DeprecatedWorkloadTypesDisabled(t *testing.T) {
	tests := []struct {
		name                          string
		enableDeprecatedWorkloadTypes bool
		rbg                           *RoleBasedGroup
		expectError                   bool
	}{
		{
			name:                          "deprecated workload types disabled, RoleInstanceSet - no error",
			enableDeprecatedWorkloadTypes: false,
			rbg: &RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{Name: "worker", Replicas: ptr.To(int32(1))},
					},
				},
			},
			expectError: false,
		},
		{
			name:                          "deprecated workload types disabled, StatefulSet - error",
			enableDeprecatedWorkloadTypes: false,
			rbg: &RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{
							Name:        "worker",
							Replicas:    ptr.To(int32(1)),
							Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
						},
					},
				},
			},
			expectError: true,
		},
		{
			name:                          "deprecated workload types enabled, StatefulSet - error",
			enableDeprecatedWorkloadTypes: true,
			rbg: &RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{
							Name:        "worker",
							Replicas:    ptr.To(int32(1)),
							Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
						},
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &RoleBasedGroupValidator{
				Client:                        newFakeValidatorClient(t),
				EnableDeprecatedWorkloadTypes: tt.enableDeprecatedWorkloadTypes,
			}
			_, err := v.ValidateCreate(context.Background(), tt.rbg)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRoleBasedGroupValidator_ValidateUpdate_DeprecatedWorkloadTypesDisabled(t *testing.T) {
	oldRBG := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{Name: "worker", Replicas: ptr.To(int32(1))},
			},
		},
	}

	t.Run("deprecated workload types disabled, update to deprecated workload - error", func(t *testing.T) {
		newRBG := &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{
					{
						Name:        "worker",
						Replicas:    ptr.To(int32(1)),
						Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
					},
				},
			},
		}
		v := &RoleBasedGroupValidator{
			Client:                        newFakeValidatorClient(t),
			EnableDeprecatedWorkloadTypes: false,
		}
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.Error(t, err)
	})

	t.Run("deprecated workload types disabled, update to RoleInstanceSet - no error", func(t *testing.T) {
		newRBG := &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{
					{Name: "worker", Replicas: ptr.To(int32(1))},
				},
			},
		}
		v := &RoleBasedGroupValidator{
			Client:                        newFakeValidatorClient(t),
			EnableDeprecatedWorkloadTypes: false,
		}
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.NoError(t, err)
	})

	t.Run("deprecated workload types enabled, update to deprecated workload - error", func(t *testing.T) {
		newRBG := &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{
					{
						Name:        "worker",
						Replicas:    ptr.To(int32(1)),
						Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
					},
				},
			},
		}
		v := &RoleBasedGroupValidator{
			Client:                        newFakeValidatorClient(t),
			EnableDeprecatedWorkloadTypes: true,
		}
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.Error(t, err)
	})

	t.Run("deprecated workload types enabled, existing role remains accepted", func(t *testing.T) {
		oldDeprecated := &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType,
					},
				}},
			},
		}
		newDeprecated := oldDeprecated.DeepCopy()
		newDeprecated.Spec.Roles[0].Replicas = ptr.To(int32(3))
		v := &RoleBasedGroupValidator{
			Client:                        newFakeValidatorClient(t),
			EnableDeprecatedWorkloadTypes: true,
		}
		_, err := v.ValidateUpdate(context.Background(), oldDeprecated, newDeprecated)
		assert.NoError(t, err)
	})
}

// TestRoleBasedGroupValidator_ValidateUpdate_RejectsDeprecatedRole pins the strict
// behaviour: with the toggle off, an object carrying a deprecated workload type is
// rejected on every update, not just when the update introduces one. A cluster in
// this mode grants no RBAC for those types and watches none of them, so an admitted
// write would produce an object nothing can reconcile. These same shapes are how the
// controllers write the main resource, so a group in this state is deliberately
// unusable rather than half-working.
func TestRoleBasedGroupValidator_ValidateUpdate_RejectsDeprecatedRole(t *testing.T) {
	statefulSetRole := func(replicas int32) RoleSpec {
		return RoleSpec{
			Name:        "worker",
			Replicas:    ptr.To(replicas),
			Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
		}
	}
	oldRBG := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbg"},
		Spec:       RoleBasedGroupSpec{Roles: []RoleSpec{statefulSetRole(1)}},
	}
	v := &RoleBasedGroupValidator{
		Client:                        newFakeValidatorClient(t),
		EnableDeprecatedWorkloadTypes: false,
	}

	t.Run("idempotent role rewrite - error", func(t *testing.T) {
		newRBG := oldRBG.DeepCopy()
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "controller.deprecatedWorkloadTypes.enabled=true")
	})

	t.Run("scale path - error", func(t *testing.T) {
		newRBG := oldRBG.DeepCopy()
		newRBG.Spec.Roles = []RoleSpec{statefulSetRole(5)}
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.Error(t, err)
	})

	t.Run("removing the deprecated role - no error", func(t *testing.T) {
		newRBG := oldRBG.DeepCopy()
		newRBG.Spec.Roles = []RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}}
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.NoError(t, err)
	})
}

func TestRoleBasedGroupSetValidator_ValidateCreate_DeprecatedWorkloadTypesDisabled(t *testing.T) {
	tests := []struct {
		name                          string
		enableDeprecatedWorkloadTypes bool
		rbgs                          *RoleBasedGroupSet
		expectError                   bool
	}{
		{
			name:                          "deprecated workload types disabled, RoleInstanceSet - no error",
			enableDeprecatedWorkloadTypes: false,
			rbgs: &RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
				Spec: RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(1)),
					GroupTemplate: RoleBasedGroupTemplateSpec{
						Spec: RoleBasedGroupSpec{
							Roles: []RoleSpec{
								{Name: "worker", Replicas: ptr.To(int32(1))},
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name:                          "deprecated workload types disabled, Deployment - error",
			enableDeprecatedWorkloadTypes: false,
			rbgs: &RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
				Spec: RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(1)),
					GroupTemplate: RoleBasedGroupTemplateSpec{
						Spec: RoleBasedGroupSpec{
							Roles: []RoleSpec{
								{
									Name:        "worker",
									Replicas:    ptr.To(int32(1)),
									Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
								},
							},
						},
					},
				},
			},
			expectError: true,
		},
		{
			name:                          "deprecated workload types enabled, Deployment - error",
			enableDeprecatedWorkloadTypes: true,
			rbgs: &RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
				Spec: RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(1)),
					GroupTemplate: RoleBasedGroupTemplateSpec{
						Spec: RoleBasedGroupSpec{
							Roles: []RoleSpec{
								{
									Name:        "worker",
									Replicas:    ptr.To(int32(1)),
									Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType},
								},
							},
						},
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &RoleBasedGroupSetValidator{
				EnableDeprecatedWorkloadTypes: tt.enableDeprecatedWorkloadTypes,
			}
			_, err := v.ValidateCreate(context.Background(), tt.rbgs)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRoleBasedGroupSetValidator_ValidateUpdate_DeprecatedWorkloadTypesDisabled(t *testing.T) {
	oldRBGS := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
		Spec: RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(1)),
			GroupTemplate: RoleBasedGroupTemplateSpec{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{Name: "worker", Replicas: ptr.To(int32(1))},
					},
				},
			},
		},
	}

	t.Run("deprecated workload types disabled, update to deprecated workload - error", func(t *testing.T) {
		newRBGS := &RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
			Spec: RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				GroupTemplate: RoleBasedGroupTemplateSpec{
					Spec: RoleBasedGroupSpec{
						Roles: []RoleSpec{
							{
								Name:        "worker",
								Replicas:    ptr.To(int32(1)),
								Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.LeaderWorkerSetWorkloadType},
							},
						},
					},
				},
			},
		}
		v := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: false}
		_, err := v.ValidateUpdate(context.Background(), oldRBGS, newRBGS)
		assert.Error(t, err)
	})

	t.Run("deprecated workload types enabled, update to deprecated workload - error", func(t *testing.T) {
		newRBGS := &RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
			Spec: RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				GroupTemplate: RoleBasedGroupTemplateSpec{
					Spec: RoleBasedGroupSpec{
						Roles: []RoleSpec{
							{
								Name:        "worker",
								Replicas:    ptr.To(int32(1)),
								Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.LeaderWorkerSetWorkloadType},
							},
						},
					},
				},
			},
		}
		v := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}
		_, err := v.ValidateUpdate(context.Background(), oldRBGS, newRBGS)
		assert.Error(t, err)
	})

	t.Run("deprecated workload types enabled, existing template remains accepted", func(t *testing.T) {
		oldDeprecated := &RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
			Spec: RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				GroupTemplate: RoleBasedGroupTemplateSpec{
					Spec: RoleBasedGroupSpec{
						Roles: []RoleSpec{{
							Name:     "worker",
							Replicas: ptr.To(int32(1)),
							Annotations: map[string]string{
								constants.RoleWorkloadTypeAnnotationKey: constants.LeaderWorkerSetWorkloadType,
							},
						}},
					},
				},
			},
		}
		newDeprecated := oldDeprecated.DeepCopy()
		newDeprecated.Spec.Replicas = ptr.To(int32(3))
		v := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}
		_, err := v.ValidateUpdate(context.Background(), oldDeprecated, newDeprecated)
		assert.NoError(t, err)
	})

	t.Run("deprecated workload types disabled, pre-existing deprecated template - error", func(t *testing.T) {
		deprecatedRBGS := &RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs"},
			Spec: RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				GroupTemplate: RoleBasedGroupTemplateSpec{
					Spec: RoleBasedGroupSpec{
						Roles: []RoleSpec{
							{
								Name:        "worker",
								Replicas:    ptr.To(int32(1)),
								Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.LeaderWorkerSetWorkloadType},
							},
						},
					},
				},
			},
		}
		// Scaling is rejected at the parent rather than silently failing on the
		// child creates it would trigger, which the cluster has no RBAC to reconcile.
		newRBGS := deprecatedRBGS.DeepCopy()
		newRBGS.Spec.Replicas = ptr.To(int32(3))

		v := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: false}
		_, err := v.ValidateUpdate(context.Background(), deprecatedRBGS, newRBGS)
		assert.Error(t, err)
	})
}

// TestRoleBasedGroupValidator_ValidateCreate_LegacySetReplacement pins the
// compatibility-mode exemption for the RoleBasedGroupSet controller: a child
// re-created from a set template that already carried the deprecated workload
// type is not a new use, while unrelated creates (no owner, a foreign owner, or
// a mismatched template) stay rejected. The parent lookup verifies the owner
// reference against the actual RoleBasedGroupSet UID and template.
func TestRoleBasedGroupValidator_ValidateCreate_LegacySetReplacement(t *testing.T) {
	statefulRole := func(name string) RoleSpec {
		return RoleSpec{
			Name:        name,
			Replicas:    ptr.To(int32(1)),
			Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
		}
	}
	newSet := func(name string, uid types.UID, roles ...RoleSpec) *RoleBasedGroupSet {
		return &RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: uid},
			Spec: RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				GroupTemplate: RoleBasedGroupTemplateSpec{
					Spec: RoleBasedGroupSpec{Roles: roles},
				},
			},
		}
	}
	newChild := func(owner *metav1.OwnerReference, roles ...RoleSpec) *RoleBasedGroup {
		child := &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "set-1", Namespace: "default"},
			Spec:       RoleBasedGroupSpec{Roles: roles},
		}
		if owner != nil {
			child.OwnerReferences = []metav1.OwnerReference{*owner}
		}
		return child
	}
	controllerRef := func(set *RoleBasedGroupSet, uid types.UID) *metav1.OwnerReference {
		return &metav1.OwnerReference{
			APIVersion: GroupVersion.String(),
			Kind:       "RoleBasedGroupSet",
			Name:       set.Name,
			UID:        uid,
			Controller: ptr.To(true),
		}
	}

	parent := newSet("legacy-set", "parent-uid", statefulRole("worker"))
	deploymentParent := newSet("deployment-set", "deployment-uid", func() RoleSpec {
		role := statefulRole("worker")
		role.Annotations[constants.RoleWorkloadTypeAnnotationKey] = constants.DeploymentWorkloadType
		return role
	}())

	t.Run("replacement child inherits the legacy template role", func(t *testing.T) {
		v := &RoleBasedGroupValidator{
			Client:                        fake.NewClientBuilder().WithScheme(validatorScheme(t)).WithObjects(parent).Build(),
			EnableDeprecatedWorkloadTypes: true,
		}
		_, err := v.ValidateCreate(context.Background(), newChild(controllerRef(parent, "parent-uid"), statefulRole("worker")))
		require.NoError(t, err)
	})

	t.Run("create without an owner reference is still rejected", func(t *testing.T) {
		v := &RoleBasedGroupValidator{
			Client:                        fake.NewClientBuilder().WithScheme(validatorScheme(t)).WithObjects(parent).Build(),
			EnableDeprecatedWorkloadTypes: true,
		}
		_, err := v.ValidateCreate(context.Background(), newChild(nil, statefulRole("worker")))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "new uses are not accepted")
	})

	t.Run("a forged controller reference does not match the parent UID", func(t *testing.T) {
		v := &RoleBasedGroupValidator{
			Client:                        fake.NewClientBuilder().WithScheme(validatorScheme(t)).WithObjects(parent).Build(),
			EnableDeprecatedWorkloadTypes: true,
		}
		_, err := v.ValidateCreate(context.Background(), newChild(controllerRef(parent, "forged-uid"), statefulRole("worker")))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "new uses are not accepted")
	})

	t.Run("a missing parent grants no exemption", func(t *testing.T) {
		v := &RoleBasedGroupValidator{
			Client:                        fake.NewClientBuilder().WithScheme(validatorScheme(t)).Build(),
			EnableDeprecatedWorkloadTypes: true,
		}
		_, err := v.ValidateCreate(context.Background(), newChild(controllerRef(parent, "parent-uid"), statefulRole("worker")))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "new uses are not accepted")
	})

	t.Run("a template role of a different deprecated type does not exempt the child", func(t *testing.T) {
		v := &RoleBasedGroupValidator{
			Client:                        fake.NewClientBuilder().WithScheme(validatorScheme(t)).WithObjects(deploymentParent).Build(),
			EnableDeprecatedWorkloadTypes: true,
		}
		_, err := v.ValidateCreate(context.Background(), newChild(controllerRef(deploymentParent, "deployment-uid"), statefulRole("worker")))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "new uses are not accepted")
	})

	t.Run("final cutoff mode rejects even a verified replacement child", func(t *testing.T) {
		v := &RoleBasedGroupValidator{
			Client:                        fake.NewClientBuilder().WithScheme(validatorScheme(t)).WithObjects(parent).Build(),
			EnableDeprecatedWorkloadTypes: false,
		}
		_, err := v.ValidateCreate(context.Background(), newChild(controllerRef(parent, "parent-uid"), statefulRole("worker")))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deprecated and not enabled on this cluster")
	})
}

// validatorScheme builds the scheme used by the admission validator fake clients.
func validatorScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))
	return scheme
}

// TestRoleBasedGroupValidator_ValidateCreate_PrefersLiveParentOverCache pins the
// security property behind the APIReader wiring: the replacement-child exemption
// must be decided against the live parent, not a stale cached copy. A parent
// that has been migrated or replaced can no longer authorize a deprecated child
// even when the cache still holds the old qualifying object.
func TestRoleBasedGroupValidator_ValidateCreate_PrefersLiveParentOverCache(t *testing.T) {
	statefulRole := func() RoleSpec {
		return RoleSpec{
			Name:        "worker",
			Replicas:    ptr.To(int32(1)),
			Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType},
		}
	}
	migratedRole := func() RoleSpec {
		role := statefulRole()
		role.Annotations = nil
		return role
	}
	newSet := func(uid types.UID, roles ...RoleSpec) *RoleBasedGroupSet {
		return &RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy-set", Namespace: "default", UID: uid},
			Spec: RoleBasedGroupSetSpec{
				Replicas:      ptr.To(int32(1)),
				GroupTemplate: RoleBasedGroupTemplateSpec{Spec: RoleBasedGroupSpec{Roles: roles}},
			},
		}
	}
	childOf := func(uid types.UID) *RoleBasedGroup {
		return &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "legacy-set-1",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: GroupVersion.String(),
					Kind:       "RoleBasedGroupSet",
					Name:       "legacy-set",
					UID:        uid,
					Controller: ptr.To(true),
				}},
			},
			Spec: RoleBasedGroupSpec{Roles: []RoleSpec{statefulRole()}},
		}
	}

	t.Run("a cached legacy parent cannot exempt a child after the live set migrated", func(t *testing.T) {
		cached := newSet("parent-uid", statefulRole())
		live := newSet("parent-uid", migratedRole())
		v := &RoleBasedGroupValidator{
			Client:                        fake.NewClientBuilder().WithScheme(validatorScheme(t)).WithObjects(cached).Build(),
			APIReader:                     fake.NewClientBuilder().WithScheme(validatorScheme(t)).WithObjects(live).Build(),
			EnableDeprecatedWorkloadTypes: true,
		}
		_, err := v.ValidateCreate(context.Background(), childOf("parent-uid"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "new uses are not accepted")
	})

	t.Run("a cached parent UID cannot exempt a child after the live set was replaced", func(t *testing.T) {
		cached := newSet("old-uid", statefulRole())
		live := newSet("new-uid", statefulRole())
		v := &RoleBasedGroupValidator{
			Client:                        fake.NewClientBuilder().WithScheme(validatorScheme(t)).WithObjects(cached).Build(),
			APIReader:                     fake.NewClientBuilder().WithScheme(validatorScheme(t)).WithObjects(live).Build(),
			EnableDeprecatedWorkloadTypes: true,
		}
		_, err := v.ValidateCreate(context.Background(), childOf("old-uid"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "new uses are not accepted")
	})

	t.Run("the live parent still grants the replacement-child exemption", func(t *testing.T) {
		live := newSet("parent-uid", statefulRole())
		v := &RoleBasedGroupValidator{
			Client:                        fake.NewClientBuilder().WithScheme(validatorScheme(t)).Build(),
			APIReader:                     fake.NewClientBuilder().WithScheme(validatorScheme(t)).WithObjects(live).Build(),
			EnableDeprecatedWorkloadTypes: true,
		}
		_, err := v.ValidateCreate(context.Background(), childOf("parent-uid"))
		require.NoError(t, err)
	})
}

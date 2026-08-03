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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

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
			name:                          "deprecated workload types enabled, StatefulSet - no error",
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
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &RoleBasedGroupValidator{
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
			Client:                        fake.NewClientBuilder().Build(),
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
			Client:                        fake.NewClientBuilder().Build(),
			EnableDeprecatedWorkloadTypes: false,
		}
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.NoError(t, err)
	})

	t.Run("deprecated workload types enabled, update to deprecated workload - no error", func(t *testing.T) {
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
			Client:                        fake.NewClientBuilder().Build(),
			EnableDeprecatedWorkloadTypes: true,
		}
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.NoError(t, err)
	})
}

// TestValidateNoNewDeprecatedWorkloadTypes covers the update-time delta check: a
// role keeps the deprecated workload type it already had, so that the controllers'
// own writes to pre-existing groups are not denied, while newly introduced or
// changed deprecated types still are.
func TestValidateNoNewDeprecatedWorkloadTypes(t *testing.T) {
	deprecatedRole := func(name, workloadType string, replicas int32) RoleSpec {
		return RoleSpec{
			Name:        name,
			Replicas:    ptr.To(replicas),
			Annotations: map[string]string{constants.RoleWorkloadTypeAnnotationKey: workloadType},
		}
	}

	tests := []struct {
		name        string
		oldRoles    []RoleSpec
		newRoles    []RoleSpec
		expectError bool
		errContains string
	}{
		{
			// The RoleBasedGroupSet template sync and the discovery-mode annotation
			// patch both rewrite the roles unchanged.
			name:     "identical deprecated roles - no error",
			oldRoles: []RoleSpec{deprecatedRole("worker", constants.StatefulSetWorkloadType, 1)},
			newRoles: []RoleSpec{deprecatedRole("worker", constants.StatefulSetWorkloadType, 1)},
		},
		{
			// The ScalingAdapter (HPA) path only changes replicas.
			name:     "deprecated role scaled - no error",
			oldRoles: []RoleSpec{deprecatedRole("worker", constants.StatefulSetWorkloadType, 1)},
			newRoles: []RoleSpec{deprecatedRole("worker", constants.StatefulSetWorkloadType, 5)},
		},
		{
			name:     "deprecated role removed - no error",
			oldRoles: []RoleSpec{deprecatedRole("worker", constants.StatefulSetWorkloadType, 1)},
			newRoles: []RoleSpec{{Name: "worker-v2", Replicas: ptr.To(int32(1))}},
		},
		{
			name:        "deprecated role added - error",
			oldRoles:    []RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
			newRoles:    []RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}, deprecatedRole("router", constants.DeploymentWorkloadType, 1)},
			expectError: true,
			errContains: "newly added role",
		},
		{
			name:        "role switched to a deprecated type - error",
			oldRoles:    []RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
			newRoles:    []RoleSpec{deprecatedRole("worker", constants.StatefulSetWorkloadType, 1)},
			expectError: true,
			errContains: "cannot be changed",
		},
		{
			// Exempting one deprecated type must not exempt the others.
			name:        "deprecated type swapped for another - error",
			oldRoles:    []RoleSpec{deprecatedRole("worker", constants.StatefulSetWorkloadType, 1)},
			newRoles:    []RoleSpec{deprecatedRole("worker", constants.DeploymentWorkloadType, 1)},
			expectError: true,
			errContains: `from "apps/v1/StatefulSet" to "apps/v1/Deployment"`,
		},
		{
			// Roles are matched by name, so a rename counts as a new role.
			name:        "deprecated role renamed - error",
			oldRoles:    []RoleSpec{deprecatedRole("worker", constants.StatefulSetWorkloadType, 1)},
			newRoles:    []RoleSpec{deprecatedRole("worker-renamed", constants.StatefulSetWorkloadType, 1)},
			expectError: true,
			errContains: "newly added role",
		},
		{
			name:     "no deprecated types at all - no error",
			oldRoles: []RoleSpec{{Name: "worker"}},
			newRoles: []RoleSpec{{Name: "worker"}, {Name: "router"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNoNewDeprecatedWorkloadTypes("spec.roles", tt.oldRoles, tt.newRoles)
			if !tt.expectError {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
			// The hint must explain that existing roles are exempt, otherwise
			// the error reads as if the whole object were rejected.
			assert.Contains(t, err.Error(), "already use a deprecated workload type keep working")
			assert.Contains(t, err.Error(), "controller.deprecatedWorkloadTypes.enabled=true")
		})
	}
}

// TestRoleBasedGroupValidator_ValidateUpdate_AllowsExistingDeprecatedRole asserts the
// three controller write shapes that the update check must not deny.
func TestRoleBasedGroupValidator_ValidateUpdate_AllowsExistingDeprecatedRole(t *testing.T) {
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
		Client:                        fake.NewClientBuilder().Build(),
		EnableDeprecatedWorkloadTypes: false,
	}

	t.Run("discovery-mode annotation patch - no error", func(t *testing.T) {
		newRBG := oldRBG.DeepCopy()
		newRBG.SetDiscoveryConfigMode(constants.RefineDiscoveryConfigMode)
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.NoError(t, err)
	})

	t.Run("scale path - no error", func(t *testing.T) {
		newRBG := oldRBG.DeepCopy()
		newRBG.Spec.Roles = []RoleSpec{statefulSetRole(5)}
		_, err := v.ValidateUpdate(context.Background(), oldRBG, newRBG)
		assert.NoError(t, err)
	})

	t.Run("idempotent role rewrite - no error", func(t *testing.T) {
		newRBG := oldRBG.DeepCopy()
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
			name:                          "deprecated workload types enabled, Deployment - no error",
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
			expectError: false,
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

	t.Run("deprecated workload types enabled, update to deprecated workload - no error", func(t *testing.T) {
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
		assert.NoError(t, err)
	})

	t.Run("deprecated workload types disabled, pre-existing deprecated template - no error", func(t *testing.T) {
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
		// Scaling an RBGSet whose template already used a deprecated workload type
		// must stay possible, otherwise the set error-loops on its own child sync.
		newRBGS := deprecatedRBGS.DeepCopy()
		newRBGS.Spec.Replicas = ptr.To(int32(3))

		v := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: false}
		_, err := v.ValidateUpdate(context.Background(), deprecatedRBGS, newRBGS)
		assert.NoError(t, err)
	})
}

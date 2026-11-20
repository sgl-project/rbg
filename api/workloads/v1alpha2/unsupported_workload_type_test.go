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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// Workload types the controller has no reconciler for and that the deprecated-type
// checks do not cover (#624). The last one has no "/", so it is not apiVersion/kind.
var unsupportedWorkloadTypes = []string{
	"workloads.x-k8s.io/v1alpha2/InstanceSet",
	"apps/v1/DaemonSet",
	"apps/v1/Statefulset",
	constants.InstanceWorkloadType,
	"RoleInstanceSet",
}

func roleWithWorkloadType(name, workloadType string) RoleSpec {
	role := RoleSpec{Name: name, Replicas: ptr.To(int32(1))}
	if workloadType != "" {
		role.Annotations = map[string]string{constants.RoleWorkloadTypeAnnotationKey: workloadType}
	}
	return role
}

func rbgWithRoles(roles ...RoleSpec) *RoleBasedGroup {
	return &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: "default"},
		Spec:       RoleBasedGroupSpec{Roles: roles},
	}
}

func rbgSetWithRoles(roles ...RoleSpec) *RoleBasedGroupSet {
	return &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: "default", UID: "set-uid"},
		Spec: RoleBasedGroupSetSpec{
			Replicas:      ptr.To(int32(1)),
			GroupTemplate: RoleBasedGroupTemplateSpec{Spec: RoleBasedGroupSpec{Roles: roles}},
		},
	}
}

func forBothDeprecatedModes(t *testing.T, run func(t *testing.T, enableDeprecated bool)) {
	t.Helper()
	for _, enableDeprecated := range []bool{true, false} {
		name := "strict"
		if enableDeprecated {
			name = "compatibility"
		}
		t.Run(name, func(t *testing.T) { run(t, enableDeprecated) })
	}
}

func TestIsReconcilableWorkloadType(t *testing.T) {
	for _, wt := range []string{
		constants.RoleInstanceSetWorkloadType,
		constants.DeploymentWorkloadType,
		constants.StatefulSetWorkloadType,
		constants.LeaderWorkerSetWorkloadType,
	} {
		assert.True(t, IsReconcilableWorkloadType(wt), wt)
		assert.False(t, isUnsupportedWorkloadType(wt), wt)
	}
	for _, wt := range unsupportedWorkloadTypes {
		assert.False(t, IsReconcilableWorkloadType(wt), wt)
		assert.True(t, isUnsupportedWorkloadType(wt), wt)
	}
	// The v1alpha1 InstanceSet has no reconciler either, but the deprecated-type
	// checks answer for it with their own migration hint (#593).
	assert.False(t, IsReconcilableWorkloadType(constants.InstanceSetWorkloadType))
	assert.False(t, isUnsupportedWorkloadType(constants.InstanceSetWorkloadType))
}

func TestRoleBasedGroupValidator_UnsupportedWorkloadType(t *testing.T) {
	forBothDeprecatedModes(t, func(t *testing.T, enableDeprecated bool) {
		newValidator := func() *RoleBasedGroupValidator {
			return &RoleBasedGroupValidator{Client: newFakeValidatorClient(t), EnableDeprecatedWorkloadTypes: enableDeprecated}
		}
		ctx := context.Background()

		for _, wt := range unsupportedWorkloadTypes {
			t.Run("create is rejected for "+wt, func(t *testing.T) {
				_, err := newValidator().ValidateCreate(ctx, rbgWithRoles(roleWithWorkloadType("worker", wt)))
				require.Error(t, err)
				assert.Contains(t, err.Error(), `spec.roles[0] (role "worker"): workload type "`+wt+`" is not supported`)
				assert.Contains(t, err.Error(), constants.RoleInstanceSetWorkloadType)
			})
		}

		t.Run("create is admitted for RoleInstanceSet, named or by default", func(t *testing.T) {
			_, err := newValidator().ValidateCreate(ctx, rbgWithRoles(
				roleWithWorkloadType("named", constants.RoleInstanceSetWorkloadType),
				roleWithWorkloadType("defaulted", ""),
			))
			require.NoError(t, err)
		})

		t.Run("update that changes a role to an unsupported type is rejected", func(t *testing.T) {
			oldRBG := rbgWithRoles(roleWithWorkloadType("worker", constants.RoleInstanceSetWorkloadType))
			newRBG := rbgWithRoles(roleWithWorkloadType("worker", "apps/v1/DaemonSet"))
			_, err := newValidator().ValidateUpdate(ctx, oldRBG, newRBG)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `workload type "apps/v1/DaemonSet" is not supported`)
		})

		t.Run("update that adds a role with an unsupported type is rejected", func(t *testing.T) {
			oldRBG := rbgWithRoles(roleWithWorkloadType("worker", ""))
			newRBG := rbgWithRoles(roleWithWorkloadType("worker", ""), roleWithWorkloadType("extra", "apps/v1/Statefulset"))
			_, err := newValidator().ValidateUpdate(ctx, oldRBG, newRBG)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `spec.roles[1] (role "extra")`)
		})

		t.Run("update that swaps one unsupported type for another is rejected", func(t *testing.T) {
			oldRBG := rbgWithRoles(roleWithWorkloadType("worker", "apps/v1/DaemonSet"))
			newRBG := rbgWithRoles(roleWithWorkloadType("worker", "apps/v1/Statefulset"))
			_, err := newValidator().ValidateUpdate(ctx, oldRBG, newRBG)
			require.Error(t, err)
		})

		t.Run("an object that already stores the type can still be updated", func(t *testing.T) {
			oldRBG := rbgWithRoles(roleWithWorkloadType("worker", "apps/v1/DaemonSet"))
			newRBG := rbgWithRoles(roleWithWorkloadType("worker", "apps/v1/DaemonSet"))
			newRBG.Labels = map[string]string{"touched": "true"}
			_, err := newValidator().ValidateUpdate(ctx, oldRBG, newRBG)
			require.NoError(t, err)
		})

		t.Run("an object that already stores the type can be fixed", func(t *testing.T) {
			oldRBG := rbgWithRoles(roleWithWorkloadType("worker", "apps/v1/DaemonSet"))
			newRBG := rbgWithRoles(roleWithWorkloadType("worker", constants.RoleInstanceSetWorkloadType))
			_, err := newValidator().ValidateUpdate(ctx, oldRBG, newRBG)
			require.NoError(t, err)
		})
	})
}

func TestRoleBasedGroupSetValidator_UnsupportedWorkloadType(t *testing.T) {
	forBothDeprecatedModes(t, func(t *testing.T, enableDeprecated bool) {
		v := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: enableDeprecated}
		ctx := context.Background()

		for _, wt := range unsupportedWorkloadTypes {
			t.Run("create is rejected for "+wt, func(t *testing.T) {
				_, err := v.ValidateCreate(ctx, rbgSetWithRoles(roleWithWorkloadType("worker", wt)))
				require.Error(t, err)
				assert.Contains(t, err.Error(),
					`spec.groupTemplate.spec.roles[0] (role "worker"): workload type "`+wt+`" is not supported`)
			})
		}

		t.Run("create is admitted for RoleInstanceSet", func(t *testing.T) {
			_, err := v.ValidateCreate(ctx, rbgSetWithRoles(roleWithWorkloadType("worker", "")))
			require.NoError(t, err)
		})

		t.Run("update that changes a template role to an unsupported type is rejected", func(t *testing.T) {
			oldSet := rbgSetWithRoles(roleWithWorkloadType("worker", ""))
			newSet := rbgSetWithRoles(roleWithWorkloadType("worker", "apps/v1/DaemonSet"))
			_, err := v.ValidateUpdate(ctx, oldSet, newSet)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `workload type "apps/v1/DaemonSet" is not supported`)
		})

		t.Run("a template that already stores the type can still be updated", func(t *testing.T) {
			oldSet := rbgSetWithRoles(roleWithWorkloadType("worker", "apps/v1/DaemonSet"))
			newSet := rbgSetWithRoles(roleWithWorkloadType("worker", "apps/v1/DaemonSet"))
			newSet.Spec.Replicas = ptr.To(int32(2))
			_, err := v.ValidateUpdate(ctx, oldSet, newSet)
			require.NoError(t, err)
		})
	})
}

// A RoleBasedGroupSet whose template already stores an unsupported type keeps
// creating its children: they are copies of a stored value, not new uses. The
// exemption needs the same verified owner as the deprecated-type one.
func TestRoleBasedGroupValidator_UnsupportedWorkloadType_SetChild(t *testing.T) {
	forBothDeprecatedModes(t, func(t *testing.T, enableDeprecated bool) {
		ctx := context.Background()
		parent := rbgSetWithRoles(roleWithWorkloadType("worker", "apps/v1/DaemonSet"))
		newValidator := func() *RoleBasedGroupValidator {
			return &RoleBasedGroupValidator{
				Client:                        fake.NewClientBuilder().WithScheme(validatorScheme(t)).WithObjects(parent).Build(),
				EnableDeprecatedWorkloadTypes: enableDeprecated,
			}
		}
		child := func(ownerUID types.UID, workloadType string) *RoleBasedGroup {
			rbg := rbgWithRoles(roleWithWorkloadType("worker", workloadType))
			rbg.Name = parent.Name + "-0"
			if ownerUID != "" {
				rbg.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: GroupVersion.String(),
					Kind:       "RoleBasedGroupSet",
					Name:       parent.Name,
					UID:        ownerUID,
					Controller: ptr.To(true),
				}}
			}
			return rbg
		}

		t.Run("a child of the set inherits the stored type", func(t *testing.T) {
			_, err := newValidator().ValidateCreate(ctx, child(parent.UID, "apps/v1/DaemonSet"))
			require.NoError(t, err)
		})

		t.Run("a child with another unsupported type is rejected", func(t *testing.T) {
			_, err := newValidator().ValidateCreate(ctx, child(parent.UID, "apps/v1/Statefulset"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is not supported")
		})

		t.Run("a forged owner UID grants nothing", func(t *testing.T) {
			_, err := newValidator().ValidateCreate(ctx, child("forged-uid", "apps/v1/DaemonSet"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is not supported")
		})

		t.Run("a create without an owner is rejected", func(t *testing.T) {
			_, err := newValidator().ValidateCreate(ctx, child("", "apps/v1/DaemonSet"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is not supported")
		})
	})
}

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

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// roleWithPattern returns a RoleInstanceSet role with the given instance pattern
// annotation; an empty pattern leaves the annotation unset.
func roleWithPattern(name, pattern string) RoleSpec {
	role := RoleSpec{Name: name, Replicas: ptr.To(int32(2))}
	if pattern != "" {
		role.Annotations = map[string]string{constants.RoleInstancePatternKey: pattern}
	}
	return role
}

const (
	stateful  = string(constants.StatefulPattern)
	stateless = string(constants.StatelessPattern)
)

// patternChangeCases are the role updates both validators see. warnFrom and warnTo
// are empty when the update must not warn.
var patternChangeCases = []struct {
	name             string
	oldRole, newRole RoleSpec
	warnFrom, warnTo string
}{
	{name: "unset to Stateless", oldRole: roleWithPattern("worker", ""), newRole: roleWithPattern("worker", stateless), warnFrom: stateful, warnTo: stateless},
	{name: "Stateful to Stateless", oldRole: roleWithPattern("worker", stateful), newRole: roleWithPattern("worker", stateless), warnFrom: stateful, warnTo: stateless},
	{name: "Stateless to Stateful", oldRole: roleWithPattern("worker", stateless), newRole: roleWithPattern("worker", stateful), warnFrom: stateless, warnTo: stateful},
	{name: "Stateless to unset", oldRole: roleWithPattern("worker", stateless), newRole: roleWithPattern("worker", ""), warnFrom: stateless, warnTo: stateful},
	{name: "unset to Stateful keeps the pattern", oldRole: roleWithPattern("worker", ""), newRole: roleWithPattern("worker", stateful)},
	{name: "Stateful to unset keeps the pattern", oldRole: roleWithPattern("worker", stateful), newRole: roleWithPattern("worker", "")},
	{
		name:    "a Stateless role that only scales",
		oldRole: roleWithPattern("worker", stateless),
		newRole: func() RoleSpec { r := roleWithPattern("worker", stateless); r.Replicas = ptr.To(int32(5)); return r }(),
	},
	{
		name:    "a role migrated from a Deployment that picks Stateless",
		oldRole: roleWithWorkloadType("worker", constants.DeploymentWorkloadType),
		newRole: roleWithPattern("worker", stateless),
	},
	{name: "an unknown pattern", oldRole: roleWithPattern("worker", ""), newRole: roleWithPattern("worker", "stateless")},
}

func TestRoleBasedGroupValidator_WarnsOnRoleInstancePatternChange(t *testing.T) {
	ctx := context.Background()
	for _, tc := range patternChangeCases {
		t.Run(tc.name, func(t *testing.T) {
			v := &RoleBasedGroupValidator{Client: newFakeValidatorClient(t), EnableDeprecatedWorkloadTypes: true}
			warnings, err := v.ValidateUpdate(ctx, rbgWithRoles(tc.oldRole), rbgWithRoles(tc.newRole))
			require.NoError(t, err, "the switch is still accepted")
			if tc.warnFrom == "" {
				assert.Empty(t, warnings)
				return
			}
			require.Len(t, warnings, 1)
			assert.Equal(t,
				`spec.roles[0] (role "worker"): changing rbg.workloads.x-k8s.io/role-instance-pattern from `+
					tc.warnFrom+` to `+tc.warnTo+` deletes all RoleInstances of this role at once and creates new ones; `+
					`a later release will reject this change, so to switch patterns delete the role and add it again`,
				warnings[0])
		})
	}

	t.Run("only the role that switches is named", func(t *testing.T) {
		v := &RoleBasedGroupValidator{Client: newFakeValidatorClient(t), EnableDeprecatedWorkloadTypes: true}
		oldRBG := rbgWithRoles(roleWithPattern("prefill", ""), roleWithPattern("decode", ""))
		newRBG := rbgWithRoles(roleWithPattern("prefill", ""), roleWithPattern("decode", stateless), roleWithPattern("router", stateless))
		warnings, err := v.ValidateUpdate(ctx, oldRBG, newRBG)
		require.NoError(t, err)
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], `spec.roles[1] (role "decode")`)
	})

	t.Run("a deleting group is not warned about", func(t *testing.T) {
		v := &RoleBasedGroupValidator{Client: newFakeValidatorClient(t), EnableDeprecatedWorkloadTypes: true}
		oldRBG := rbgWithRoles(roleWithPattern("worker", ""))
		newRBG := rbgWithRoles(roleWithPattern("worker", stateless))
		newRBG.DeletionTimestamp = ptr.To(metav1.Now())
		warnings, err := v.ValidateUpdate(ctx, oldRBG, newRBG)
		require.NoError(t, err)
		assert.Empty(t, warnings)
	})

	t.Run("the warning comes with a rejection of the same update", func(t *testing.T) {
		v := &RoleBasedGroupValidator{Client: newFakeValidatorClient(t), EnableDeprecatedWorkloadTypes: true}
		oldRBG := rbgWithRoles(roleWithPattern("worker", ""))
		newRBG := rbgWithRoles(roleWithPattern("worker", stateless))
		newRBG.Spec.Roles[0].Dependencies = []string{"ghost"}
		warnings, err := v.ValidateUpdate(ctx, oldRBG, newRBG)
		require.Error(t, err)
		assert.Len(t, warnings, 1)
	})
}

func TestRoleBasedGroupSetValidator_WarnsOnRoleInstancePatternChange(t *testing.T) {
	ctx := context.Background()
	for _, tc := range patternChangeCases {
		t.Run(tc.name, func(t *testing.T) {
			v := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}
			warnings, err := v.ValidateUpdate(ctx, rbgSetWithRoles(tc.oldRole), rbgSetWithRoles(tc.newRole))
			require.NoError(t, err, "the switch is still accepted")
			if tc.warnFrom == "" {
				assert.Empty(t, warnings)
				return
			}
			require.Len(t, warnings, 1)
			assert.Equal(t,
				`spec.groupTemplate.spec.roles[0] (role "worker"): changing rbg.workloads.x-k8s.io/role-instance-pattern from `+
					tc.warnFrom+` to `+tc.warnTo+` deletes all RoleInstances of this role at once in each child RoleBasedGroup `+
					`the set updates in place, and creates new ones; to switch patterns, delete the role and add it again`,
				warnings[0])
		})
	}

	// rbgSetWithRoles has no rollout strategy, so the cases above use the legacy
	// path, which updates every child in place.
	for _, tc := range []struct {
		name     string
		strategy GroupUpdateStrategyType
		warns    bool
	}{
		{name: "empty type", strategy: "", warns: true},
		{name: "InPlaceUpdate", strategy: InPlaceUpdateStrategyType, warns: true},
		{name: "Recreate", strategy: RecreateStrategyType, warns: false},
	} {
		t.Run("rollout strategy "+tc.name, func(t *testing.T) {
			v := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}
			oldRBGS := rbgSetWithRoles(roleWithPattern("worker", ""))
			newRBGS := rbgSetWithRoles(roleWithPattern("worker", stateless))
			newRBGS.Spec.RolloutStrategy = &GroupSetRolloutStrategy{Type: tc.strategy}
			warnings, err := v.ValidateUpdate(ctx, oldRBGS, newRBGS)
			require.NoError(t, err)
			if tc.warns {
				assert.Len(t, warnings, 1)
			} else {
				assert.Empty(t, warnings, "a Recreate set rebuilds the child anyway")
			}
		})
	}

	t.Run("a deleting set is not warned about", func(t *testing.T) {
		v := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}
		oldRBGS := rbgSetWithRoles(roleWithPattern("worker", ""))
		newRBGS := rbgSetWithRoles(roleWithPattern("worker", stateless))
		newRBGS.DeletionTimestamp = ptr.To(metav1.Now())
		warnings, err := v.ValidateUpdate(ctx, oldRBGS, newRBGS)
		require.NoError(t, err)
		assert.Empty(t, warnings)
	})
}

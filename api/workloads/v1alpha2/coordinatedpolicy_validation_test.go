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
)

func gangPolicy(roles []string, minReplicas map[string]int32) *CoordinatedPolicy {
	return &CoordinatedPolicy{
		Spec: CoordinatedPolicySpec{
			Policies: []CoordinatedPolicyRule{
				{
					Roles: roles,
					Strategy: CoordinatedPolicyStrategy{
						Scheduling: &SchedulingCoordinationStrategy{
							Gang: &GangSchedulingStrategy{MinReplicas: minReplicas},
						},
					},
				},
			},
		},
	}
}

func TestValidateCoordinatedPolicyGang(t *testing.T) {
	tests := []struct {
		name                     string
		policy                   *CoordinatedPolicy
		perRoleMinimumsSupported bool
		errContains              string
	}{
		{
			name:                     "no scheduling strategy",
			policy:                   &CoordinatedPolicy{Spec: CoordinatedPolicySpec{Policies: []CoordinatedPolicyRule{{Roles: []string{"prefill"}}}}},
			perRoleMinimumsSupported: true,
		},
		{
			name:                     "basic gang without minReplicas is allowed on scheduler-plugins",
			policy:                   gangPolicy([]string{"prefill"}, nil),
			perRoleMinimumsSupported: false,
		},
		{
			name:                     "valid per-role minimums",
			policy:                   gangPolicy([]string{"prefill", "decode"}, map[string]int32{"prefill": 4, "decode": 1}),
			perRoleMinimumsSupported: true,
		},
		{
			name:                     "minReplicas rejected when the scheduler cannot honor them",
			policy:                   gangPolicy([]string{"prefill"}, map[string]int32{"prefill": 2}),
			perRoleMinimumsSupported: false,
			errContains:              "require --scheduler-name=volcano",
		},
		{
			name:                     "role outside the rule scope",
			policy:                   gangPolicy([]string{"prefill"}, map[string]int32{"decode": 1}),
			perRoleMinimumsSupported: true,
			errContains:              "is not listed in spec.policies[0].roles",
		},
		{
			name:                     "minReplicas below one",
			policy:                   gangPolicy([]string{"prefill"}, map[string]int32{"prefill": 0}),
			perRoleMinimumsSupported: true,
			errContains:              "must be at least 1",
		},
		{
			// Whether the role exists and whether it has enough replicas depends on the
			// RoleBasedGroup, so both are enforced when the PodGroup is built instead.
			name:                     "role existence and replica bounds are left to the reconciler",
			policy:                   gangPolicy([]string{"router", "decode"}, map[string]int32{"router": 1, "decode": 99}),
			perRoleMinimumsSupported: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCoordinatedPolicyGang(tt.policy, tt.perRoleMinimumsSupported)
			if tt.errContains == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.errContains)
		})
	}
}

func namedGangPolicy(minReplicas map[string]int32) *CoordinatedPolicy {
	policy := gangPolicy([]string{"prefill", "decode"}, minReplicas)
	policy.Name = "rbg"
	policy.Namespace = "default"
	return policy
}

func TestCoordinatedPolicyValidator(t *testing.T) {
	// The validator reads nothing: a policy that no RoleBasedGroup can satisfy yet is
	// admitted, and the reconciler reports it as GangConfigured=False.
	t.Run("replica bounds are not enforced at admission", func(t *testing.T) {
		v := &CoordinatedPolicyValidator{PerRoleGangMinimumsSupported: true}
		policy := namedGangPolicy(map[string]int32{"decode": 99})

		_, err := v.ValidateCreate(context.Background(), policy)
		assert.NoError(t, err)

		_, err = v.ValidateUpdate(context.Background(), policy, policy)
		assert.NoError(t, err)
	})

	t.Run("scheduler capability is enforced", func(t *testing.T) {
		v := &CoordinatedPolicyValidator{PerRoleGangMinimumsSupported: false}
		_, err := v.ValidateCreate(context.Background(), namedGangPolicy(map[string]int32{"decode": 1}))
		assert.ErrorContains(t, err, "require --scheduler-name=volcano")
	})
}

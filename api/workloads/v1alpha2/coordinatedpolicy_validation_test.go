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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

func gangRBG() *RoleBasedGroup {
	return &RoleBasedGroup{
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{Name: "prefill", Replicas: ptr.To[int32](4)},
				{Name: "decode", Replicas: ptr.To[int32](2)},
			},
		},
	}
}

func TestValidateCoordinatedPolicyGang(t *testing.T) {
	tests := []struct {
		name                     string
		policy                   *CoordinatedPolicy
		rbg                      *RoleBasedGroup
		perRoleMinimumsSupported bool
		errContains              string
	}{
		{
			name:                     "no scheduling strategy",
			policy:                   &CoordinatedPolicy{Spec: CoordinatedPolicySpec{Policies: []CoordinatedPolicyRule{{Roles: []string{"prefill"}}}}},
			rbg:                      gangRBG(),
			perRoleMinimumsSupported: true,
		},
		{
			name:                     "basic gang without minReplicas is allowed on scheduler-plugins",
			policy:                   gangPolicy([]string{"prefill"}, nil),
			rbg:                      gangRBG(),
			perRoleMinimumsSupported: false,
		},
		{
			name:                     "valid per-role minimums",
			policy:                   gangPolicy([]string{"prefill", "decode"}, map[string]int32{"prefill": 4, "decode": 1}),
			rbg:                      gangRBG(),
			perRoleMinimumsSupported: true,
		},
		{
			name:                     "minReplicas rejected when the scheduler cannot honor them",
			policy:                   gangPolicy([]string{"prefill"}, map[string]int32{"prefill": 2}),
			rbg:                      gangRBG(),
			perRoleMinimumsSupported: false,
			errContains:              "require --scheduler-name=volcano",
		},
		{
			name:                     "role outside the rule scope",
			policy:                   gangPolicy([]string{"prefill"}, map[string]int32{"decode": 1}),
			rbg:                      gangRBG(),
			perRoleMinimumsSupported: true,
			errContains:              "is not listed in spec.policies[0].roles",
		},
		{
			name:                     "minReplicas below one",
			policy:                   gangPolicy([]string{"prefill"}, map[string]int32{"prefill": 0}),
			rbg:                      gangRBG(),
			perRoleMinimumsSupported: true,
			errContains:              "must be at least 1",
		},
		{
			name:                     "unknown role",
			policy:                   gangPolicy([]string{"router"}, map[string]int32{"router": 1}),
			rbg:                      gangRBG(),
			perRoleMinimumsSupported: true,
			errContains:              "no such role in RoleBasedGroup",
		},
		{
			name:                     "minReplicas above the role replicas",
			policy:                   gangPolicy([]string{"decode"}, map[string]int32{"decode": 3}),
			rbg:                      gangRBG(),
			perRoleMinimumsSupported: true,
			errContains:              "must not exceed the role's 2 replicas",
		},
		{
			name:                     "replica bounds are skipped when the RBG does not exist yet",
			policy:                   gangPolicy([]string{"decode"}, map[string]int32{"decode": 3}),
			rbg:                      nil,
			perRoleMinimumsSupported: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCoordinatedPolicyGang(tt.policy, tt.rbg, tt.perRoleMinimumsSupported)
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

func namedGangRBG() *RoleBasedGroup {
	rbg := gangRBG()
	rbg.Name = "rbg"
	rbg.Namespace = "default"
	return rbg
}

func TestCoordinatedPolicyValidator(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))

	t.Run("replica bounds are checked against the cross-read RBG", func(t *testing.T) {
		v := &CoordinatedPolicyValidator{
			Reader:                       fake.NewClientBuilder().WithScheme(scheme).WithObjects(namedGangRBG()).Build(),
			PerRoleGangMinimumsSupported: true,
		}
		policy := namedGangPolicy(map[string]int32{"decode": 3})

		_, err := v.ValidateCreate(context.Background(), policy)
		assert.ErrorContains(t, err, "must not exceed the role's 2 replicas")

		_, err = v.ValidateUpdate(context.Background(), policy, policy)
		assert.ErrorContains(t, err, "must not exceed the role's 2 replicas")
	})

	t.Run("missing RBG defers the replica bounds", func(t *testing.T) {
		v := &CoordinatedPolicyValidator{
			Reader:                       fake.NewClientBuilder().WithScheme(scheme).Build(),
			PerRoleGangMinimumsSupported: true,
		}
		_, err := v.ValidateCreate(context.Background(), namedGangPolicy(map[string]int32{"decode": 3}))
		assert.NoError(t, err)
	})

	// A read failure must not reject every policy write: the RoleBasedGroup validator
	// repeats the same check, and the reconcile path repeats it again.
	t.Run("read failure fails open", func(t *testing.T) {
		reader := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return errors.New("boom")
			},
		}).Build()
		v := &CoordinatedPolicyValidator{Reader: reader, PerRoleGangMinimumsSupported: true}
		_, err := v.ValidateCreate(context.Background(), namedGangPolicy(map[string]int32{"decode": 3}))
		assert.NoError(t, err)
	})

	// The scheduler capability check needs no cross-read, so it still applies.
	t.Run("scheduler capability is enforced without the RBG", func(t *testing.T) {
		v := &CoordinatedPolicyValidator{
			Reader:                       fake.NewClientBuilder().WithScheme(scheme).Build(),
			PerRoleGangMinimumsSupported: false,
		}
		_, err := v.ValidateCreate(context.Background(), namedGangPolicy(map[string]int32{"decode": 1}))
		assert.ErrorContains(t, err, "require --scheduler-name=volcano")
	})
}

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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

// TestRestartPolicy_DecodeV070Wire guards the upgrade path from v0.7.0, where
// restartPolicy was serialized as a bare string. Decoding those stored objects
// must keep working: a failure here breaks the typed informer for the whole
// resource type, not just the offending object.
func TestRestartPolicy_DecodeV070Wire(t *testing.T) {
	t.Run("leaderWorkerPattern", func(t *testing.T) {
		var role RoleSpec
		require.NoError(t, json.Unmarshal([]byte(`{
			"name": "role",
			"leaderWorkerPattern": {"size": 2, "restartPolicy": "RecreateRoleInstanceOnPodRestart"}
		}`), &role))

		assert.Equal(t, RecreateRoleInstanceOnPodRestart, role.LeaderWorkerPattern.RestartPolicy)
		assert.Nil(t, role.LeaderWorkerPattern.RestartPolicyConfig)
		assert.Equal(t, RecreateRoleInstanceOnPodRestart, role.GetRestartPolicy())
	})

	t.Run("customComponentsPattern", func(t *testing.T) {
		var role RoleSpec
		require.NoError(t, json.Unmarshal([]byte(`{
			"name": "role",
			"customComponentsPattern": {"restartPolicy": "None"}
		}`), &role))

		assert.Equal(t, RestartPolicyNone, role.GetRestartPolicy())
	})

	t.Run("roleInstanceSpec", func(t *testing.T) {
		var spec RoleInstanceSpec
		require.NoError(t, json.Unmarshal([]byte(`{
			"components": [],
			"restartPolicy": "RecreateRoleInstanceOnPodRestart"
		}`), &spec))

		assert.Equal(t, RecreateRoleInstanceOnPodRestart, spec.GetRestartPolicy())
		// v0.7.0 carried no backoff config, so the defaults apply.
		assert.Equal(t, DefaultBaseDelaySeconds, spec.GetBaseDelaySeconds())
		assert.Equal(t, DefaultMaxDelaySeconds, spec.GetMaxDelaySeconds())
	})
}

func TestRoleSpec_GetRestartPolicyConfig(t *testing.T) {
	lwpRole := func(legacy RestartPolicyType, cfg *RestartPolicyConfig) *RoleSpec {
		return &RoleSpec{
			Name: "role",
			Pattern: Pattern{
				LeaderWorkerPattern: &LeaderWorkerPattern{
					RestartPolicy:       legacy,
					RestartPolicyConfig: cfg,
				},
			},
		}
	}

	tests := []struct {
		name         string
		role         *RoleSpec
		expectedType RestartPolicyType
	}{
		{
			name:         "nil role resolves to None",
			role:         nil,
			expectedType: RestartPolicyNone,
		},
		{
			name:         "standalone pattern resolves to None",
			role:         &RoleSpec{Name: "role", Pattern: Pattern{StandalonePattern: &StandalonePattern{}}},
			expectedType: RestartPolicyNone,
		},
		{
			name:         "config type wins over legacy field",
			role:         lwpRole(RecreateRoleInstanceOnPodRestart, &RestartPolicyConfig{Type: RestartPolicyNone}),
			expectedType: RestartPolicyNone,
		},
		{
			name:         "legacy field used when config carries no type",
			role:         lwpRole(RestartPolicyNone, &RestartPolicyConfig{BaseDelaySeconds: ptr.To(int32(5))}),
			expectedType: RestartPolicyNone,
		},
		{
			name:         "legacy field used when config is absent",
			role:         lwpRole(RestartPolicyNone, nil),
			expectedType: RestartPolicyNone,
		},
		{
			name:         "leaderWorkerPattern defaults to recreate when neither is set",
			role:         lwpRole("", nil),
			expectedType: RecreateRoleInstanceOnPodRestart,
		},
		{
			name: "customComponentsPattern defaults to recreate when neither is set",
			role: &RoleSpec{
				Name:    "role",
				Pattern: Pattern{CustomComponentsPattern: &CustomComponentsPattern{}},
			},
			expectedType: RecreateRoleInstanceOnPodRestart,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedType, tt.role.GetRestartPolicyConfig().Type)
			assert.Equal(t, tt.expectedType, tt.role.GetRestartPolicy())
		})
	}
}

func TestRoleSpec_GetRestartPolicyConfig_PreservesBackoff(t *testing.T) {
	role := &RoleSpec{
		Name: "role",
		Pattern: Pattern{
			LeaderWorkerPattern: &LeaderWorkerPattern{
				RestartPolicy: RecreateRoleInstanceOnPodRestart,
				RestartPolicyConfig: &RestartPolicyConfig{
					BaseDelaySeconds: ptr.To(int32(15)),
					MaxDelaySeconds:  ptr.To(int32(120)),
				},
			},
		},
	}

	assert.Equal(t, RecreateRoleInstanceOnPodRestart, role.GetRestartPolicy())
	assert.Equal(t, int32(15), role.GetBaseDelaySeconds())
	assert.Equal(t, int32(120), role.GetMaxDelaySeconds())
}

func TestRoleInstanceSpec_GetRestartPolicy_NoSyntheticDefault(t *testing.T) {
	// Unlike RoleSpec, an unset policy on a RoleInstance must stay empty:
	// RecreateRoleInstanceOnPodRestart would start recreating instances of
	// patterns that never opted in.
	spec := &RoleInstanceSpec{}
	assert.Equal(t, RestartPolicyType(""), spec.GetRestartPolicy())

	spec.RestartPolicy = RecreateRoleInstanceOnPodRestart
	assert.Equal(t, RecreateRoleInstanceOnPodRestart, spec.GetRestartPolicy())

	spec.RestartPolicyConfig = &RestartPolicyConfig{Type: RestartPolicyNone}
	assert.Equal(t, RestartPolicyNone, spec.GetRestartPolicy())
}

func TestRoleBasedGroupDefaulter_Default(t *testing.T) {
	rbg := &RoleBasedGroup{
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{
					Name: "legacy-only",
					Pattern: Pattern{
						LeaderWorkerPattern: &LeaderWorkerPattern{RestartPolicy: RestartPolicyNone},
					},
				},
				{
					Name:    "unset",
					Pattern: Pattern{CustomComponentsPattern: &CustomComponentsPattern{}},
				},
				{
					Name: "explicit backoff",
					Pattern: Pattern{
						LeaderWorkerPattern: &LeaderWorkerPattern{
							RestartPolicyConfig: &RestartPolicyConfig{
								Type:             RecreateRoleInstanceOnPodRestart,
								BaseDelaySeconds: ptr.To(int32(15)),
							},
						},
					},
				},
				{
					Name:    "standalone",
					Pattern: Pattern{StandalonePattern: &StandalonePattern{}},
				},
			},
		},
	}

	require.NoError(t, (&RoleBasedGroupDefaulter{}).Default(context.Background(), rbg))

	legacyOnly := rbg.Spec.Roles[0].LeaderWorkerPattern
	assert.Equal(t, RestartPolicyNone, legacyOnly.RestartPolicyConfig.Type)
	assert.Equal(t, RestartPolicyNone, legacyOnly.RestartPolicy, "deprecated field must not be cleared")

	unset := rbg.Spec.Roles[1].CustomComponentsPattern
	assert.Equal(t, RecreateRoleInstanceOnPodRestart, unset.RestartPolicyConfig.Type)

	explicit := rbg.Spec.Roles[2].LeaderWorkerPattern
	assert.Equal(t, RecreateRoleInstanceOnPodRestart, explicit.RestartPolicyConfig.Type)
	assert.Equal(t, ptr.To(int32(15)), explicit.RestartPolicyConfig.BaseDelaySeconds)

	assert.Equal(t, RestartPolicyNone, rbg.Spec.Roles[3].GetRestartPolicy(),
		"standalone pattern carries no restart policy to default")
}

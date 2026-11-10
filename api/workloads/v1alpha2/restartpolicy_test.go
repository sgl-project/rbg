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

func TestRestartPolicy_FromCreationOptInResolution(t *testing.T) {
	tests := []struct {
		name         string
		legacy       RestartPolicyType
		config       *RestartPolicyConfig
		expectedRole RestartPolicyType
		expectedSpec RestartPolicyType
	}{
		{name: "absent policy does not opt in", expectedRole: RecreateRoleInstanceOnPodRestart},
		{name: "empty config does not opt in", config: &RestartPolicyConfig{}, expectedRole: RecreateRoleInstanceOnPodRestart},
		{name: "backoff alone does not opt in", config: &RestartPolicyConfig{BaseDelaySeconds: ptr.To(int32(7))}, expectedRole: RecreateRoleInstanceOnPodRestart},
		{name: "legacy recreate stays legacy", legacy: RecreateRoleInstanceOnPodRestart, expectedRole: RecreateRoleInstanceOnPodRestart, expectedSpec: RecreateRoleInstanceOnPodRestart},
		{name: "legacy field explicitly opts in", legacy: RecreateRoleInstanceOnPodRestartFromCreation, expectedRole: RecreateRoleInstanceOnPodRestartFromCreation, expectedSpec: RecreateRoleInstanceOnPodRestartFromCreation},
		{name: "empty config preserves legacy opt in", legacy: RecreateRoleInstanceOnPodRestartFromCreation, config: &RestartPolicyConfig{}, expectedRole: RecreateRoleInstanceOnPodRestartFromCreation, expectedSpec: RecreateRoleInstanceOnPodRestartFromCreation},
		{name: "backoff config preserves legacy opt in", legacy: RecreateRoleInstanceOnPodRestartFromCreation, config: &RestartPolicyConfig{BaseDelaySeconds: ptr.To(int32(7))}, expectedRole: RecreateRoleInstanceOnPodRestartFromCreation, expectedSpec: RecreateRoleInstanceOnPodRestartFromCreation},
		{name: "config explicitly opts in", config: &RestartPolicyConfig{Type: RecreateRoleInstanceOnPodRestartFromCreation}, expectedRole: RecreateRoleInstanceOnPodRestartFromCreation, expectedSpec: RecreateRoleInstanceOnPodRestartFromCreation},
		{name: "config opt in overrides legacy None", legacy: RestartPolicyNone, config: &RestartPolicyConfig{Type: RecreateRoleInstanceOnPodRestartFromCreation}, expectedRole: RecreateRoleInstanceOnPodRestartFromCreation, expectedSpec: RecreateRoleInstanceOnPodRestartFromCreation},
		{name: "config None overrides legacy opt in", legacy: RecreateRoleInstanceOnPodRestartFromCreation, config: &RestartPolicyConfig{Type: RestartPolicyNone}, expectedRole: RestartPolicyNone, expectedSpec: RestartPolicyNone},
		{name: "config legacy policy overrides legacy opt in", legacy: RecreateRoleInstanceOnPodRestartFromCreation, config: &RestartPolicyConfig{Type: RecreateRoleInstanceOnPodRestart}, expectedRole: RecreateRoleInstanceOnPodRestart, expectedSpec: RecreateRoleInstanceOnPodRestart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patterns := map[string]Pattern{
				"leaderWorker": {LeaderWorkerPattern: &LeaderWorkerPattern{RestartPolicy: tt.legacy, RestartPolicyConfig: tt.config}},
				"components":   {CustomComponentsPattern: &CustomComponentsPattern{RestartPolicy: tt.legacy, RestartPolicyConfig: tt.config}},
			}
			for name, pattern := range patterns {
				t.Run(name, func(t *testing.T) {
					role := &RoleSpec{Pattern: pattern}
					before := role.DeepCopy()
					assert.Equal(t, tt.expectedRole, role.GetRestartPolicy())
					assert.Equal(t, tt.expectedRole, role.GetRestartPolicyConfig().Type)
					if tt.config != nil {
						assert.Equal(t, tt.config.BaseDelaySeconds, role.GetRestartPolicyConfig().BaseDelaySeconds)
					}
					assert.Equal(t, before, role, "resolution must not mutate the stored policy")
				})
			}
			spec := &RoleInstanceSpec{RestartPolicy: tt.legacy, RestartPolicyConfig: tt.config}
			before := spec.DeepCopy()
			assert.Equal(t, tt.expectedSpec, spec.GetRestartPolicy())
			assert.Equal(t, tt.expectedSpec, spec.GetRestartPolicyConfig().Type)
			assert.Equal(t, before, spec, "resolution must not mutate the stored policy")
		})
	}
}

func TestRestartPolicy_DecodeFromCreationWire(t *testing.T) {
	for _, pattern := range []string{"leaderWorkerPattern", "customComponentsPattern"} {
		for _, policyJSON := range []string{
			`"restartPolicy":"RecreateRoleInstanceOnPodRestartFromCreation"`,
			`"restartPolicyConfig":{"type":"RecreateRoleInstanceOnPodRestartFromCreation"}`,
		} {
			t.Run(pattern+"/"+policyJSON, func(t *testing.T) {
				var role RoleSpec
				require.NoError(t, json.Unmarshal([]byte(`{"`+pattern+`":{`+policyJSON+`}}`), &role))
				assert.Equal(t, RecreateRoleInstanceOnPodRestartFromCreation, role.GetRestartPolicy())
				var spec RoleInstanceSpec
				require.NoError(t, json.Unmarshal([]byte(`{`+policyJSON+`}`), &spec))
				assert.Equal(t, RecreateRoleInstanceOnPodRestartFromCreation, spec.GetRestartPolicy())
			})
		}
	}
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

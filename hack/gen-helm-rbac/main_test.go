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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
)

func TestRenderKeepsDeprecatedWorkloadRBACUnconditional(t *testing.T) {
	rules := []ruleBlock{{
		rule: rbacv1.PolicyRule{
			APIGroups: []string{"apps"},
			Resources: []string{"deployments", "statefulsets"},
			Verbs:     []string{"get"},
		},
	}}
	out, err := render(rules)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "if $deprecatedEnabled")
	assert.Contains(t, string(out), "deployments")
	assert.Contains(t, string(out), "statefulsets")
}

// TestParseRole covers inputs that used to be accepted and then silently lose
// permissions: a misspelled key left its rule empty, a kind other than ClusterRole
// was rendered as the chart ClusterRole anyway, and both exited 0.
func TestParseRole(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		errContains string
	}{
		{
			name: "well-formed ClusterRole",
			raw: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: controller-role
rules:
- apiGroups: ["apps"]
  resources: ["statefulsets"]
  verbs: ["get"]
`,
		},
		{
			name: "misspelled key",
			raw: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: controller-role
rules:
- apiGroups: ["apps"]
  resource: ["statefulsets"]
  verbs: ["get"]
`,
			errContains: "parse",
		},
		{
			name: "namespaced Role",
			raw: `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: controller-role
rules:
- apiGroups: ["apps"]
  resources: ["statefulsets"]
  verbs: ["get"]
`,
			errContains: `want ClusterRole`,
		},
		{
			name: "no rules",
			raw: `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: controller-role
`,
			errContains: "declares no rules",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role, err := parseRole([]byte(tt.raw))
			if tt.errContains == "" {
				require.NoError(t, err)
				assert.Len(t, role.Rules, 1)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

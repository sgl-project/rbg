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

// TestDeprecatedResources pins the gate derived from the workload type constants.
// Changing or adding a constant, or hitting a kind the naive pluralisation gets
// wrong, breaks this test rather than silently emitting the rule outside the
// conditional, where it would stay granted with the toggle off.
func TestDeprecatedResources(t *testing.T) {
	assert.Equal(t, map[string]map[string]bool{
		"apps": {
			"deployments":  true,
			"statefulsets": true,
		},
		"leaderworkerset.x-k8s.io": {
			"leaderworkersets": true,
		},
	}, deprecatedResources)
}

func TestSplitRules(t *testing.T) {
	tests := []struct {
		name  string
		rules []rbacv1.PolicyRule
		want  []ruleBlock
	}{
		{
			name: "a rule of only deprecated resources is gated whole",
			rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"apps"},
				Resources: []string{"deployments", "statefulsets"},
				Verbs:     []string{"get"},
			}},
			want: []ruleBlock{{
				rule: rbacv1.PolicyRule{
					APIGroups: []string{"apps"},
					Resources: []string{"deployments", "statefulsets"},
					Verbs:     []string{"get"},
				},
				deprecated: true,
			}},
		},
		{
			name: "a mixed rule is split, and controllerrevisions stay granted",
			rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"apps"},
				Resources: []string{"controllerrevisions", "statefulsets"},
				Verbs:     []string{"get"},
			}},
			want: []ruleBlock{
				{rule: rbacv1.PolicyRule{
					APIGroups: []string{"apps"},
					Resources: []string{"controllerrevisions"},
					Verbs:     []string{"get"},
				}},
				{
					rule: rbacv1.PolicyRule{
						APIGroups: []string{"apps"},
						Resources: []string{"statefulsets"},
						Verbs:     []string{"get"},
					},
					deprecated: true,
				},
			},
		},
		{
			name: "subresources follow their base resource",
			rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"apps"},
				Resources: []string{"deployments/status", "deployments/finalizers"},
				Verbs:     []string{"update"},
			}},
			want: []ruleBlock{{
				rule: rbacv1.PolicyRule{
					APIGroups: []string{"apps"},
					Resources: []string{"deployments/status", "deployments/finalizers"},
					Verbs:     []string{"update"},
				},
				deprecated: true,
			}},
		},
		{
			name: "a resource outside the gate is granted unconditionally",
			rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"workloads.x-k8s.io"},
				Resources: []string{"roleinstancesets"},
				Verbs:     []string{"get"},
			}},
			want: []ruleBlock{{rule: rbacv1.PolicyRule{
				APIGroups: []string{"workloads.x-k8s.io"},
				Resources: []string{"roleinstancesets"},
				Verbs:     []string{"get"},
			}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitRules(tt.rules)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestSplitRulesRejectsAmbiguousGroups covers a rule whose apiGroups disagree on
// whether a resource is deprecated: it cannot be split by resource alone, so it must
// error rather than land on one side of the conditional.
func TestSplitRulesRejectsAmbiguousGroups(t *testing.T) {
	_, err := splitRules([]rbacv1.PolicyRule{{
		APIGroups: []string{"apps", "workloads.x-k8s.io"},
		Resources: []string{"statefulsets"},
		Verbs:     []string{"get"},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "split the kubebuilder marker")
}

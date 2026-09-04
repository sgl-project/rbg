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
	"fmt"
	"sort"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

// ValidateCoordinatedPolicyGang validates every scheduling.gang strategy in the policy.
//
// Only self-contained rules are checked here. Whether a role exists and whether its
// replica count can satisfy the minimum depend on the RoleBasedGroup, and a policy is
// allowed to describe a workload that does not exist yet or that is temporarily too
// small; those rules are enforced when the PodGroup is built, and surface as a
// GangConfigured=False condition on the RoleBasedGroup.
//
// perRoleMinimumsSupported reports whether the configured scheduler can honor
// minReplicas at all. Only Volcano implements it, via the PodGroup subGroupPolicy
// field, so rejecting it here surfaces a wrong --scheduler-name immediately instead
// of at reconcile time. It does not cover every failure: a Volcano too old to have
// subGroupPolicy, or a role not backed by a RoleInstanceSet, is only detected when
// the PodGroup is built.
func ValidateCoordinatedPolicyGang(
	policy *CoordinatedPolicy,
	perRoleMinimumsSupported bool,
) error {
	var allErrs []error

	for i := range policy.Spec.Policies {
		rule := &policy.Spec.Policies[i]
		if rule.Strategy.Scheduling == nil || rule.Strategy.Scheduling.Gang == nil {
			continue
		}
		gang := rule.Strategy.Scheduling.Gang
		if len(gang.MinReplicas) == 0 {
			continue
		}

		path := fmt.Sprintf("spec.policies[%d].strategy.scheduling.gang.minReplicas", i)
		if !perRoleMinimumsSupported {
			allErrs = append(allErrs, fmt.Errorf(
				"%s: per-role gang minimums require --scheduler-name=volcano with Volcano >= 1.14; "+
					"omit minReplicas for basic whole-group gang scheduling", path))
			continue
		}

		scope := make(map[string]struct{}, len(rule.Roles))
		for _, roleName := range rule.Roles {
			scope[roleName] = struct{}{}
		}

		for _, roleName := range sortedKeys(gang.MinReplicas) {
			minReplicas := gang.MinReplicas[roleName]
			if _, inScope := scope[roleName]; !inScope {
				allErrs = append(allErrs, fmt.Errorf(
					"%s[%s]: role is not listed in spec.policies[%d].roles %v, so the minimum would be silently ignored",
					path, roleName, i, rule.Roles))
				continue
			}
			if minReplicas < 1 {
				allErrs = append(allErrs, fmt.Errorf(
					"%s[%s]: must be at least 1, got %d", path, roleName, minReplicas))
			}
		}
	}

	return utilerrors.NewAggregate(allErrs)
}

// sortedKeys returns the map keys in a stable order so that admission errors for
// a multi-role minReplicas map do not shuffle between requests.
func sortedKeys(m map[string]int32) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

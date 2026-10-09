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
	"slices"
	"sort"
	"strings"

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

// ValidateCoordinatedPolicyTopology validates syntax and rule-local overlap for
// topology constraints. Role existence and level semantics are checked during
// reconcile, because a CoordinatedPolicy may be created before its RBG.
func ValidateCoordinatedPolicyTopology(policy *CoordinatedPolicy) error {
	var allErrs []error
	seenRoles := map[string]string{}
	seenRuleNames := map[string]struct{}{}
	for i := range policy.Spec.Policies {
		rule := &policy.Spec.Policies[i]
		if rule.Strategy.Scheduling == nil || rule.Strategy.Scheduling.TopologyConstraint == nil {
			continue
		}
		path := fmt.Sprintf("spec.policies[%d].strategy.scheduling.topologyConstraint", i)
		if err := ValidateTopologyConstraint(path, rule.Strategy.Scheduling.TopologyConstraint); err != nil {
			allErrs = append(allErrs, err)
		}
		if _, duplicate := seenRuleNames[rule.Name]; duplicate {
			allErrs = append(allErrs, fmt.Errorf(
				"%s: duplicate topology rule name %q; topology-bearing rule names must be unique",
				path, rule.Name))
		} else {
			seenRuleNames[rule.Name] = struct{}{}
		}
		for _, roleName := range rule.Roles {
			if existing, exists := seenRoles[roleName]; exists {
				allErrs = append(allErrs, fmt.Errorf(
					"%s: role %q is also covered by topology rule %q; a role may appear in at most one topology-bearing rule",
					path, roleName, existing))
				continue
			}
			seenRoles[roleName] = rule.Name
		}
	}
	return utilerrors.NewAggregate(allErrs)
}

// ValidateCoordinatedPolicyTopologyImmutability rejects changes to the complete set of
// topology-bearing policy rules. Topology is a launch-time placement contract, so the
// rule set cannot be added to, removed from, or changed in place.
func ValidateCoordinatedPolicyTopologyImmutability(oldPolicy, newPolicy *CoordinatedPolicy) error {
	// Compare the complete topology-bearing rule set rather than a name-keyed map. The
	// CRD does not require unique policy names, so duplicate names must not hide one of
	// the rules from removal detection.
	if !topologyRuleIdentitiesEqual(
		topologyRuleIdentities(oldPolicy),
		topologyRuleIdentities(newPolicy),
	) {
		return fmt.Errorf(
			"spec.policies topology rules are immutable for the workload lifecycle; delete and recreate the affected workload to change them")
	}
	return nil
}

type topologyRuleIdentity struct {
	Name     string
	Roles    []string
	Topology *TopologyConstraint
}

func topologyRuleIdentities(policy *CoordinatedPolicy) []topologyRuleIdentity {
	if policy == nil {
		return nil
	}
	identities := make([]topologyRuleIdentity, 0)
	for i := range policy.Spec.Policies {
		rule := &policy.Spec.Policies[i]
		if rule.Strategy.Scheduling == nil || rule.Strategy.Scheduling.TopologyConstraint == nil {
			continue
		}
		identities = append(identities, topologyRuleIdentity{
			Name:     rule.Name,
			Roles:    canonicalRoleNames(rule.Roles),
			Topology: rule.Strategy.Scheduling.TopologyConstraint,
		})
	}
	sort.Slice(identities, func(i, j int) bool {
		if identities[i].Name != identities[j].Name {
			return identities[i].Name < identities[j].Name
		}
		return strings.Join(identities[i].Roles, "\x00") < strings.Join(identities[j].Roles, "\x00")
	})
	return identities
}

func canonicalRoleNames(roleNames []string) []string {
	canonical := slices.Clone(roleNames)
	sort.Strings(canonical)
	return canonical
}

func topologyRuleIdentitiesEqual(left, right []topologyRuleIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Name != right[i].Name ||
			!slices.Equal(left[i].Roles, right[i].Roles) ||
			!TopologyConstraintsEqual(left[i].Topology, right[i].Topology) {
			return false
		}
	}
	return true
}

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

package common

import (
	"context"
	"fmt"
	"maps"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// GangStrategy is the gang configuration resolved for one RoleBasedGroup.
//
// It is not the API type: a CoordinatedPolicy scopes each rule with
// spec.policies[].roles, and an all-or-nothing rule has no minReplicas map to record
// that scope in, so the resolved form carries the covered roles explicitly.
type GangStrategy struct {
	// Roles are the names of the roles the gang covers. Empty means every role in the
	// group, which is what the legacy annotation resolves to since it has no way to
	// name roles.
	Roles sets.Set[string]

	// MinReplicas is the minimum number of replicas of a covered role that must be
	// schedulable before the gang is dispatched. Empty means every covered role
	// participates in full, an all-or-nothing gang over Roles. When set, its keys are
	// a subset of Roles: a covered role absent from the map came from an
	// all-or-nothing rule and participates in full, while the per-role minimums the
	// other rules declared still apply to the roles they name.
	MinReplicas map[string]int32
}

// gangStrategyContextKey keys the gang strategy resolved for the reconcile that
// the context belongs to.
type gangStrategyContextKey struct{}

// resolvedGangStrategy carries a resolved strategy together with the RBG it was
// resolved for, so a context that outlives its reconcile can never hand back
// another object's strategy.
type resolvedGangStrategy struct {
	key      client.ObjectKey
	strategy *GangStrategy
}

// ResolveGangStrategy resolves the strategy for rbg once and returns a context
// carrying the result, so that every consumer within the same reconcile (the
// PodGroup spec and each role's pod template) observes one value. Resolving per
// role instead would let a CoordinatedPolicy update land mid-reconcile and leave
// the PodGroup and the pod templates disagreeing about the gang.
func ResolveGangStrategy(
	ctx context.Context,
	c client.Reader,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) (context.Context, *GangStrategy, error) {
	strategy, err := getGangStrategy(ctx, c, rbg)
	if err != nil {
		return ctx, nil, err
	}
	resolved := resolvedGangStrategy{key: client.ObjectKeyFromObject(rbg), strategy: strategy}
	return context.WithValue(ctx, gangStrategyContextKey{}, resolved), strategy, nil
}

// GetGangStrategy returns the gang scheduling strategy for the given RBG,
// reusing the value ResolveGangStrategy put on the context when it is present
// and reading the CoordinatedPolicy otherwise.
//
// It first checks the CoordinatedPolicy (same name/namespace as the RBG) for
// Scheduling.Gang strategies. If none is found, it falls back to the legacy
// annotation compat path: when the rbg has the gang-scheduling annotation set
// to "true", a whole-group GangStrategy is returned so that callers can treat all
// gang-enabling paths uniformly via a non-nil pointer.
//
// Returns (nil, nil) when gang scheduling is not enabled. A read error on the
// CoordinatedPolicy is returned rather than treated as absence, so that a
// transient API failure or an RBAC denial cannot silently downgrade a
// scheduling guarantee.
func GetGangStrategy(
	ctx context.Context,
	c client.Reader,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) (*GangStrategy, error) {
	resolved, ok := ctx.Value(gangStrategyContextKey{}).(resolvedGangStrategy)
	if ok && resolved.key == client.ObjectKeyFromObject(rbg) {
		return resolved.strategy, nil
	}
	return getGangStrategy(ctx, c, rbg)
}

func getGangStrategy(
	ctx context.Context,
	c client.Reader,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) (*GangStrategy, error) {
	// 1. Check CoordinatedPolicy for explicit gang strategies.
	coordinatedPolicy := &workloadsv1alpha2.CoordinatedPolicy{}
	err := c.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, coordinatedPolicy)
	switch {
	case err == nil:
		strategy, mergeErr := MergeGangStrategies(coordinatedPolicy)
		if mergeErr != nil {
			return nil, mergeErr
		}
		if strategy != nil {
			return strategy, nil
		}
	case !apierrors.IsNotFound(err):
		return nil, fmt.Errorf("get CoordinatedPolicy %s/%s: %w", rbg.Namespace, rbg.Name, err)
	}

	// 2. Fall back to the legacy annotation compat path.
	if rbg.Annotations[constants.GangSchedulingAnnotationKey] == "true" {
		return &GangStrategy{}, nil
	}

	return nil, nil
}

// RoleInGang reports whether the role participates in the gang described by strategy.
func RoleInGang(
	role *workloadsv1alpha2.RoleSpec,
	strategy *GangStrategy,
) bool {
	if strategy == nil || role == nil {
		return false
	}
	if len(strategy.Roles) == 0 {
		return true
	}
	return strategy.Roles.Has(role.Name)
}

// GangSize returns the number of pods an all-or-nothing gang covers, which is the
// PodGroup minMember when no per-role minimum is configured.
//
// Only the roles the gang covers are counted. A CoordinatedPolicy rule scopes itself
// with spec.policies[].roles, so a group may legitimately run roles the gang leaves
// out; counting those would demand pods the gang never enrolls and leave the whole
// group Pending.
func GangSize(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	strategy *GangStrategy,
) (int32, error) {
	if unknown := UnknownGangRoles(rbg, strategy); len(unknown) > 0 {
		return 0, NewIncompatibleGangConfigError(
			"gang scheduling covers roles that do not exist in the RoleBasedGroup: %v; "+
				"fix the role names in CoordinatedPolicy %s/%s",
			unknown, rbg.Namespace, rbg.Name)
	}

	var size int32
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		if !RoleInGang(role, strategy) {
			continue
		}
		size += workloadsv1alpha2.ComputeSubGroupSize(role) * ptr.Deref(role.Replicas, 1)
	}

	if size == 0 {
		return 0, NewIncompatibleGangConfigError(
			"gang scheduling resolved to minMember 0, which would provide no gang guarantee; "+
				"the roles it covers in CoordinatedPolicy %s/%s are all scaled to zero",
			rbg.Namespace, rbg.Name)
	}
	return size, nil
}

// GangMinimumReplicas returns, per covered role, the replica count the gang needs
// before it can be dispatched. A covered role with a configured minimum needs exactly
// that many replicas; a covered role without one (an all-or-nothing role) needs every
// replica.
//
// This is what other controllers must not hold a role below: the PodGroup counts the
// pods of those replicas in minMember, so a role parked underneath its gang minimum
// keeps the whole gang unschedulable.
func GangMinimumReplicas(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	strategy *GangStrategy,
) map[string]int32 {
	if strategy == nil {
		return nil
	}

	minimums := make(map[string]int32, len(rbg.Spec.Roles))
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		if !RoleInGang(role, strategy) {
			continue
		}
		if minReplicas, ok := strategy.MinReplicas[role.Name]; ok {
			minimums[role.Name] = minReplicas
			continue
		}
		minimums[role.Name] = ptr.Deref(role.Replicas, 1)
	}
	return minimums
}

// UnknownGangRoles returns the covered role names that name no role in the RBG, sorted.
func UnknownGangRoles(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	strategy *GangStrategy,
) []string {
	if strategy == nil || len(strategy.Roles) == 0 {
		return nil
	}

	known := make(sets.Set[string], len(rbg.Spec.Roles))
	for i := range rbg.Spec.Roles {
		known.Insert(rbg.Spec.Roles[i].Name)
	}
	return sets.List(strategy.Roles.Difference(known))
}

// MergeGangStrategies collapses every Scheduling.Gang across all policy rules
// into a single strategy, or returns nil when no rule declares one.
//
// The gang covers the union of the roles named by every rule that declares one, which
// is what makes spec.policies[].roles a meaningful scoping field rather than
// decoration. Per-role minimums are scoped by the declaring rule too: a key is only
// honored when the rule that declares it also lists that role. When the same role
// carries a minimum in more than one rule, the largest wins, because that is the only
// merge that satisfies every rule simultaneously.
//
// A gang rule with an empty minReplicas map requests all-or-nothing gang over the
// roles it names. Such a role participates in full regardless of what other rules
// declare for it: holding it to a per-role minimum would be weaker than the
// all-or-nothing rule asked for, so the minimum is dropped and the role stays
// covered. The resulting MinReplicas map is therefore a subset of Roles: keys are
// roles that only per-role rules cover, and covered roles absent from the map
// participate in full.
//
// It returns an IncompatibleGangConfigError when per-role minimums were declared
// but every one of them was scoped away, since neither an all-or-nothing gang nor nil
// would describe what the policy asked for.
func MergeGangStrategies(
	coordinatedPolicy *workloadsv1alpha2.CoordinatedPolicy,
) (*GangStrategy, error) {
	allOrNothingRoles := sets.New[string]()
	merged := map[string]int32{}
	found := false

	for i := range coordinatedPolicy.Spec.Policies {
		policy := &coordinatedPolicy.Spec.Policies[i]
		if policy.Strategy.Scheduling == nil || policy.Strategy.Scheduling.Gang == nil {
			continue
		}
		found = true

		gang := policy.Strategy.Scheduling.Gang
		if len(gang.MinReplicas) == 0 {
			allOrNothingRoles.Insert(policy.Roles...)
			continue
		}

		for roleName, minReplicas := range gang.MinReplicas {
			if !slices.Contains(policy.Roles, roleName) {
				continue
			}
			if existing, ok := merged[roleName]; !ok || minReplicas > existing {
				merged[roleName] = minReplicas
			}
		}
	}

	if !found {
		return nil, nil
	}
	// An all-or-nothing rule covers its roles in full, so any per-role minimum for
	// the same role is subsumed.
	maps.DeleteFunc(merged, func(roleName string, _ int32) bool {
		return allOrNothingRoles.Has(roleName)
	})
	if len(merged) == 0 {
		if allOrNothingRoles.Len() > 0 {
			return &GangStrategy{Roles: allOrNothingRoles}, nil
		}
		// Every gang rule declared per-role minimums yet none survived scoping, so
		// every key named a role outside its own rule. The admission webhook rejects
		// that, so reaching here means the policy predates the webhook or was written
		// with admission disabled. Falling back to an all-or-nothing gang would be a
		// stricter constraint than was asked for, so report the configuration instead
		// of silently widening it.
		return nil, NewIncompatibleGangConfigError(
			"gang scheduling minReplicas in CoordinatedPolicy %s/%s names only roles outside "+
				"their own policy rule's roles list, so no per-role minimum applies; "+
				"list those roles in the rule or remove the minimums",
			coordinatedPolicy.Namespace, coordinatedPolicy.Name)
	}
	return &GangStrategy{
		Roles:       allOrNothingRoles.Union(sets.KeySet(merged)),
		MinReplicas: merged,
	}, nil
}

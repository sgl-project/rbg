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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// gangStrategyContextKey keys the gang strategy resolved for the reconcile that
// the context belongs to.
type gangStrategyContextKey struct{}

// resolvedGangStrategy carries a resolved strategy together with the RBG it was
// resolved for, so a context that outlives its reconcile can never hand back
// another object's strategy.
type resolvedGangStrategy struct {
	key      client.ObjectKey
	strategy *workloadsv1alpha2.GangSchedulingStrategy
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
) (context.Context, *workloadsv1alpha2.GangSchedulingStrategy, error) {
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
// to "true", a default (empty) GangSchedulingStrategy is returned so that
// callers can treat all gang-enabling paths uniformly via a non-nil pointer.
//
// Returns (nil, nil) when gang scheduling is not enabled. A read error on the
// CoordinatedPolicy is returned rather than treated as absence, so that a
// transient API failure or an RBAC denial cannot silently downgrade a
// scheduling guarantee.
func GetGangStrategy(
	ctx context.Context,
	c client.Reader,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) (*workloadsv1alpha2.GangSchedulingStrategy, error) {
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
) (*workloadsv1alpha2.GangSchedulingStrategy, error) {
	// 1. Check CoordinatedPolicy for explicit gang strategies.
	coordinatedPolicy := &workloadsv1alpha2.CoordinatedPolicy{}
	err := c.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, coordinatedPolicy)
	switch {
	case err == nil:
		if strategy := MergeGangStrategies(coordinatedPolicy); strategy != nil {
			return strategy, nil
		}
	case !apierrors.IsNotFound(err):
		return nil, fmt.Errorf("get CoordinatedPolicy %s/%s: %w", rbg.Namespace, rbg.Name, err)
	}

	// 2. Fall back to the legacy annotation compat path.
	if rbg.Annotations[constants.GangSchedulingAnnotationKey] == "true" {
		return &workloadsv1alpha2.GangSchedulingStrategy{}, nil
	}

	return nil, nil
}

// RoleInGang reports whether the role participates in the gang described by strategy.
// An empty minReplicas map means basic all-or-nothing gang over every role.
func RoleInGang(
	role *workloadsv1alpha2.RoleSpec,
	strategy *workloadsv1alpha2.GangSchedulingStrategy,
) bool {
	if strategy == nil || role == nil {
		return false
	}
	if len(strategy.MinReplicas) == 0 {
		return true
	}
	_, ok := strategy.MinReplicas[role.Name]
	return ok
}

// MergeGangStrategies collapses every Scheduling.Gang across all policy rules
// into a single strategy, or returns nil when no rule declares one.
//
// Per-role minimums are scoped by the declaring rule: a key is only honored
// when the rule that declares it also lists that role in its Roles field, which
// is what makes Roles a meaningful scoping field rather than decoration. When
// the same role carries a minimum in more than one rule, the largest wins,
// because that is the only merge that satisfies every rule simultaneously.
//
// A gang rule with an empty minReplicas map requests basic all-or-nothing gang
// over the whole group, which subsumes any per-role minimum, so it short
// circuits to the empty strategy.
func MergeGangStrategies(
	coordinatedPolicy *workloadsv1alpha2.CoordinatedPolicy,
) *workloadsv1alpha2.GangSchedulingStrategy {
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
			return &workloadsv1alpha2.GangSchedulingStrategy{}
		}

		scope := make(map[string]struct{}, len(policy.Roles))
		for _, roleName := range policy.Roles {
			scope[roleName] = struct{}{}
		}
		for roleName, minReplicas := range gang.MinReplicas {
			if _, inScope := scope[roleName]; !inScope {
				continue
			}
			if existing, ok := merged[roleName]; !ok || minReplicas > existing {
				merged[roleName] = minReplicas
			}
		}
	}

	if !found {
		return nil
	}
	if len(merged) == 0 {
		return &workloadsv1alpha2.GangSchedulingStrategy{}
	}
	return &workloadsv1alpha2.GangSchedulingStrategy{MinReplicas: merged}
}

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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const (
	// PartitionByNone means all members of a scope form one placement group.
	PartitionByNone = ""
	// PartitionByRoleInstance means one placement group is produced per RoleInstance.
	PartitionByRoleInstance = "role-instance-name"
)

// PlacementPlan is the scheduler-independent result of resolving gang and topology
// intents. The scheduler compiler is the only component allowed to turn this plan
// into physical PodGroups and pod bindings.
type PlacementPlan struct {
	// Root is nil when neither gang nor topology is enabled.
	Root *PlacementGroup
}

// PlacementScope defines the members a placement constraint applies to and how those
// members are partitioned into concrete groups.
type PlacementScope struct {
	// Roles contains sorted, unique RBG role names.
	Roles []string

	// PartitionBy is empty for a whole-scope group and PartitionByRoleInstance for
	// per-RoleInstance groups.
	PartitionBy string
}

// PlacementGroup is one logical placement node. Gang and topology are attributes of
// the node; neither attribute owns physical PodGroup layout.
type PlacementGroup struct {
	ID string

	// Name is a short, human-readable identity derived from the declaring Role or
	// CoordinatedPolicy rule. The physical PodGroup name uses it together with the
	// RBG identity so names remain readable without relying on a long scope hash.
	Name string

	Scope PlacementScope

	Gang *GangStrategy

	Topology *workloadsv1alpha2.TopologyConstraint

	Children []*PlacementGroup
}

// HasTopology reports whether any node in the plan carries a topology constraint.
func (p *PlacementPlan) HasTopology() bool {
	return p != nil && groupHasTopology(p.Root)
}

func groupHasTopology(group *PlacementGroup) bool {
	if group == nil {
		return false
	}
	if group.Topology != nil {
		return true
	}
	for _, child := range group.Children {
		if groupHasTopology(child) {
			return true
		}
	}
	return false
}

// TopLevelGroups returns the concrete top-level logical groups. A synthetic root is
// used only to hold disjoint top-level groups and is not itself rendered.
func (p *PlacementPlan) TopLevelGroups() []*PlacementGroup {
	if p == nil || p.Root == nil {
		return nil
	}
	if p.Root.ID == "" {
		return p.Root.Children
	}
	return []*PlacementGroup{p.Root}
}

// ResolvePlacementPlan reads the CoordinatedPolicy, combines it with Role-level
// instance constraints, and returns one logical placement plan. gangStrategy must be
// the result of ResolveGangStrategy so gang composition remains exactly KEP-430.
func ResolvePlacementPlan(
	ctx context.Context,
	c client.Reader,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	gangStrategy *GangStrategy,
) (*PlacementPlan, error) {
	if rbg == nil {
		return nil, nil
	}

	policy := &workloadsv1alpha2.CoordinatedPolicy{}
	err := c.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, policy)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get CoordinatedPolicy %s/%s: %w", rbg.Namespace, rbg.Name, err)
	}
	if apierrors.IsNotFound(err) {
		policy = nil
	}

	groups, err := buildPlacementGroups(rbg, policy, gangStrategy)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, nil
	}
	if len(groups) == 1 {
		return &PlacementPlan{Root: groups[0]}, nil
	}
	return &PlacementPlan{Root: &PlacementGroup{Children: groups}}, nil
}

func buildPlacementGroups(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	policy *workloadsv1alpha2.CoordinatedPolicy,
	gangStrategy *GangStrategy,
) ([]*PlacementGroup, error) {
	knownRoles := sets.New[string]()
	for i := range rbg.Spec.Roles {
		knownRoles.Insert(rbg.Spec.Roles[i].Name)
	}

	var nodes []*PlacementGroup
	if gangStrategy != nil {
		// Legacy whole-group gang strategies carry an empty Roles set, meaning every
		// role in the RBG. Expand it here so the logical root has a real scope; a
		// scope with no roles would otherwise be mistaken for a synthetic root.
		gangRoles := sets.List(gangStrategy.Roles)
		if len(gangRoles) == 0 {
			gangRoles = sets.List(knownRoles)
		}
		scope := newPlacementScope(gangRoles, PartitionByNone)
		nodes = append(nodes, &PlacementGroup{
			ID:       scopeID(scope),
			Scope:    scope,
			Gang:     gangStrategy,
			Topology: nil,
		})
	}

	// Cross-role topology rules. A role may participate in at most one topology rule;
	// membership is a partition and a pod cannot belong to two physical PodGroups.
	topologyRoles := sets.New[string]()
	if policy != nil {
		for i := range policy.Spec.Policies {
			rule := &policy.Spec.Policies[i]
			if rule.Strategy.Scheduling == nil || rule.Strategy.Scheduling.TopologyConstraint == nil {
				continue
			}
			roles := canonicalRoles(rule.Roles)
			for _, roleName := range roles {
				if !knownRoles.Has(roleName) {
					return nil, NewTopologyTranslationError(
						"topology rule %d in CoordinatedPolicy %s/%s references unknown role %q",
						i, policy.Namespace, policy.Name, roleName)
				}
				if topologyRoles.Has(roleName) {
					return nil, NewIncompatiblePlacementGroupsError(
						"role %q appears in more than one topology-bearing rule in CoordinatedPolicy %s/%s",
						roleName, policy.Namespace, policy.Name)
				}
				topologyRoles.Insert(roleName)
			}
			nodes = append(nodes, &PlacementGroup{
				ID:       scopeID(newPlacementScope(roles, PartitionByNone)),
				Name:     "p-" + rule.Name,
				Scope:    newPlacementScope(roles, PartitionByNone),
				Gang:     nil,
				Topology: rule.Strategy.Scheduling.TopologyConstraint,
			})
		}
	}

	// Role-level constraints always partition by RoleInstance. They may refine a
	// cross-role or gang parent, or stand alone when no parent covers the role.
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		if role.InstanceTopologyConstraint == nil {
			continue
		}
		scope := newPlacementScope([]string{role.Name}, PartitionByRoleInstance)
		nodes = append(nodes, &PlacementGroup{
			ID:       scopeID(scope),
			Name:     "r-" + role.Name,
			Scope:    scope,
			Gang:     nil,
			Topology: role.InstanceTopologyConstraint,
		})
	}

	if err := validateTopologyIdentity(nodes); err != nil {
		return nil, err
	}

	var roots []*PlacementGroup
	for _, node := range nodes {
		roots = insertPlacementGroup(roots, node)
		if roots == nil {
			return nil, NewIncompatiblePlacementGroupsError(
				"placement scopes partially overlap; neither scope contains the other")
		}
	}
	return roots, nil
}

func newPlacementScope(roles []string, partitionBy string) PlacementScope {
	return PlacementScope{Roles: canonicalRoles(roles), PartitionBy: partitionBy}
}

func canonicalRoles(roles []string) []string {
	unique := sets.New(roles...).UnsortedList()
	sort.Strings(unique)
	return unique
}

func scopeID(scope PlacementScope) string {
	// A simple dash-joined role list is ambiguous: ["a-b", "c"] and ["a", "b-c"]
	// both produce "a-b-c". Hash the canonical scope instead so disjoint scopes cannot
	// collide in generated PodGroup names. The 128-bit prefix is long enough to make
	// accidental collisions negligible while keeping object names bounded.
	canonical := make([]byte, 0, len(scope.Roles)+len(scope.PartitionBy)+len(scope.Roles)+1)
	canonical = append(canonical, scope.PartitionBy...)
	canonical = append(canonical, 0)
	for _, role := range scope.Roles {
		canonical = append(canonical, role...)
		canonical = append(canonical, 0)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:16])
}

// insertPlacementGroup adds node to the top-level forest. It returns nil when node
// partially overlaps an existing scope; a nil result is the caller's error marker.
func insertPlacementGroup(roots []*PlacementGroup, node *PlacementGroup) []*PlacementGroup {
	if node == nil {
		return roots
	}

	next := make([]*PlacementGroup, 0, len(roots)+1)
	nodeInserted := false
	for _, root := range roots {
		relation := scopeRelation(root.Scope, node.Scope)
		switch relation {
		case scopeEqual:
			mergePlacementGroups(root, node)
			return roots
		case scopeDisjoint:
			next = append(next, root)
		case scopeContains:
			// root contains node; insert node under root.
			root.Children = insertPlacementGroup(root.Children, node)
			if root.Children == nil {
				return nil
			}
			return roots
		case scopeContainedBy:
			// node contains root; absorb root and continue in case node contains
			// more than one existing root.
			node.Children = append(node.Children, root)
			if node.Name == "" {
				node.Name = root.Name
			}
			if !nodeInserted {
				nodeInserted = true
			}
		default:
			return nil
		}
	}
	if nodeInserted {
		next = append(next, node)
		return next
	}
	return append(next, node)
}
func mergePlacementGroups(dst, src *PlacementGroup) {
	if dst.Gang == nil {
		dst.Gang = src.Gang
	}
	if dst.Topology == nil {
		dst.Topology = src.Topology
	}
	if dst.Name == "" {
		dst.Name = src.Name
	}
	dst.Children = append(dst.Children, src.Children...)
}

type scopeRelationKind int

const (
	scopeEqual scopeRelationKind = iota
	scopeDisjoint
	scopeContains
	scopeContainedBy
	scopePartialOverlap
)

func scopeRelation(parent, child PlacementScope) scopeRelationKind {
	if slices.Equal(parent.Roles, child.Roles) && parent.PartitionBy == child.PartitionBy {
		return scopeEqual
	}

	parentSet := sets.New(parent.Roles...)
	childSet := sets.New(child.Roles...)
	switch {
	case parentSet.Len() == 0 && childSet.Len() == 0:
		if partitionRank(parent.PartitionBy) == partitionRank(child.PartitionBy) {
			return scopeEqual
		}
		if partitionRank(parent.PartitionBy) < partitionRank(child.PartitionBy) {
			return scopeContains
		}
		return scopeContainedBy
	case parentSet.Len() == 0:
		return scopeContains
	case childSet.Len() == 0:
		return scopeContainedBy
	case childSet.Intersection(parentSet).Len() == 0:
		return scopeDisjoint
	case childSet.Equal(parentSet):
		if partitionRank(parent.PartitionBy) <= partitionRank(child.PartitionBy) {
			return scopeContains
		}
		return scopeContainedBy
	case childSet.IsSuperset(parentSet):
		return scopeContainedBy
	case parentSet.IsSuperset(childSet):
		if partitionRank(parent.PartitionBy) <= partitionRank(child.PartitionBy) {
			return scopeContains
		}
		return scopePartialOverlap
	default:
		return scopePartialOverlap
	}
}

func partitionRank(partitionBy string) int {
	switch partitionBy {
	case "":
		return 0
	case PartitionByRoleInstance:
		return 1
	default:
		return -1
	}
}

func validateTopologyIdentity(groups []*PlacementGroup) error {
	seen := sets.New[string]()
	var visit func(group *PlacementGroup) error
	visit = func(group *PlacementGroup) error {
		if group == nil {
			return nil
		}
		if group.Topology != nil && group.Topology.TopologyName != nil && *group.Topology.TopologyName != "" {
			seen.Insert(*group.Topology.TopologyName)
		}
		if seen.Len() > 1 {
			return NewIncompatiblePlacementGroupsError(
				"topology constraints resolve to different topologyName values %v", sets.List(seen))
		}
		for _, child := range group.Children {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	for _, group := range groups {
		if err := visit(group); err != nil {
			return err
		}
	}
	return nil
}

// IncompatiblePlacementGroupsError reports a placement plan that cannot be represented
// by the target scheduler because its scopes partially overlap.
type IncompatiblePlacementGroupsError struct {
	message string
}

func NewIncompatiblePlacementGroupsError(format string, args ...any) *IncompatiblePlacementGroupsError {
	return &IncompatiblePlacementGroupsError{message: fmt.Sprintf(format, args...)}
}

func (e *IncompatiblePlacementGroupsError) Error() string {
	return e.message
}

func IsIncompatiblePlacementGroups(err error) bool {
	var target *IncompatiblePlacementGroupsError
	return errors.As(err, &target)
}

type placementPlanContextKey struct{}

type resolvedPlacementPlan struct {
	key  types.NamespacedName
	plan *PlacementPlan
}

// WithPlacementPlan carries the plan resolved once per reconcile so the PodGroup
// compiler and every pod template observe the same logical membership.
func WithPlacementPlan(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, plan *PlacementPlan) context.Context {
	if rbg == nil {
		return ctx
	}
	return context.WithValue(ctx, placementPlanContextKey{}, resolvedPlacementPlan{
		key:  client.ObjectKeyFromObject(rbg),
		plan: plan,
	})
}

// GetPlacementPlan returns the plan stored by WithPlacementPlan, resolving it from
// the RBG and CoordinatedPolicy when a reconciler calls without the controller's
// context.
func GetPlacementPlan(
	ctx context.Context,
	reader client.Reader,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) (*PlacementPlan, error) {
	if rbg == nil {
		return nil, nil
	}
	if resolved, ok := ctx.Value(placementPlanContextKey{}).(resolvedPlacementPlan); ok {
		if resolved.key == client.ObjectKeyFromObject(rbg) {
			return resolved.plan, nil
		}
	}
	gangStrategy, err := GetGangStrategy(ctx, reader, rbg)
	if err != nil {
		return nil, err
	}
	return ResolvePlacementPlan(ctx, reader, rbg, gangStrategy)
}

// SchedulerUnsupportedError reports a valid logical plan that the active scheduler
// dialect cannot compile without semantic loss.
type SchedulerUnsupportedError struct {
	message string
}

func NewSchedulerUnsupportedError(format string, args ...any) *SchedulerUnsupportedError {
	return &SchedulerUnsupportedError{message: fmt.Sprintf(format, args...)}
}

func (e *SchedulerUnsupportedError) Error() string {
	return e.message
}

func IsSchedulerUnsupported(err error) bool {
	var target *SchedulerUnsupportedError
	return errors.As(err, &target)
}

// TopologyTranslationError reports a topology value that cannot be translated for
// the active scheduler, such as an unknown level or invalid parent/child ordering.
type TopologyTranslationError struct {
	message string
}

func NewTopologyTranslationError(format string, args ...any) *TopologyTranslationError {
	return &TopologyTranslationError{message: fmt.Sprintf(format, args...)}
}

func (e *TopologyTranslationError) Error() string {
	return e.message
}

func IsTopologyTranslationError(err error) bool {
	var target *TopologyTranslationError
	return errors.As(err, &target)
}

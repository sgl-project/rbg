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
	"fmt"
	"reflect"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func ValidateRoleBasedGroupName(rbg *RoleBasedGroup) error {
	if errs := validation.IsDNS1123Label(rbg.Name); len(errs) > 0 {
		return fmt.Errorf("metadata.name: %q is not a valid DNS label: %s", rbg.Name, errs[0])
	}
	return nil
}

// ValidateRollingUpdate validates each role's rolloutStrategy when replicas is greater than 0:
//   - maxSurge and maxUnavailable cannot both resolve to 0 (would block any rollout progress).
//   - partition must be strictly less than replicas (otherwise no replica would ever be updated).
func ValidateRollingUpdate(rbg *RoleBasedGroup) error {
	var allErrs []error

	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		if role.RolloutStrategy == nil || role.RolloutStrategy.RollingUpdate == nil || role.Replicas == nil || *role.Replicas == 0 {
			continue
		}
		if err := validateRoleRollingUpdate(i, role.RolloutStrategy.RollingUpdate, *role.Replicas); err != nil {
			allErrs = append(allErrs, err...)
		}
	}
	return utilerrors.NewAggregate(allErrs)
}

// ValidateRoleDependencies validates that every role dependency references an
// existing role, no role depends on itself, and the role dependency graph is acyclic.
func ValidateRoleDependencies(rbg *RoleBasedGroup) error {
	return validateRoleDependencies("spec.roles", rbg.Spec.Roles)
}

func validateRoleDependencies(fieldPath string, roles []RoleSpec) error {
	roleNames := make(map[string]struct{}, len(roles))
	var allErrs []error
	for i := range roles {
		roleNames[roles[i].Name] = struct{}{}
	}

	graph := make(map[string][]string, len(roles))
	for i := range roles {
		role := &roles[i]
		for j, dependency := range role.Dependencies {
			if dependency == role.Name {
				allErrs = append(allErrs, fmt.Errorf(
					"%s[%d].dependencies[%d]: role %q cannot depend on itself",
					fieldPath, i, j, role.Name,
				))
				continue
			}
			if _, ok := roleNames[dependency]; !ok {
				allErrs = append(allErrs, fmt.Errorf(
					"%s[%d].dependencies[%d]: role %q depends on unknown role %q",
					fieldPath, i, j, role.Name, dependency,
				))
				continue
			}
			graph[role.Name] = append(graph[role.Name], dependency)
		}
	}
	if err := validateNoRoleDependencyCycle(fieldPath, roles, graph); err != nil {
		allErrs = append(allErrs, err)
	}

	return utilerrors.NewAggregate(allErrs)
}

func validateNoRoleDependencyCycle(fieldPath string, roles []RoleSpec, graph map[string][]string) error {
	const (
		unvisited = iota
		visiting
		visited
	)

	state := make(map[string]int, len(roles))
	var visit func(roleName string, path []string) error
	visit = func(roleName string, path []string) error {
		switch state[roleName] {
		case visiting:
			// Keep the reported cycle minimal when a later edge points back into
			// the active DFS path, e.g. a -> b -> c -> b reports b -> c -> b.
			cycle := appendCycle(path, roleName)
			return fmt.Errorf("%s: dependency cycle detected: %s", fieldPath, strings.Join(cycle, " -> "))
		case visited:
			return nil
		}

		state[roleName] = visiting
		path = append(path, roleName)
		for _, dependency := range graph[roleName] {
			if err := visit(dependency, path); err != nil {
				return err
			}
		}
		state[roleName] = visited
		return nil
	}

	for i := range roles {
		if state[roles[i].Name] == unvisited {
			if err := visit(roles[i].Name, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func appendCycle(path []string, roleName string) []string {
	for i := range path {
		if path[i] == roleName {
			cycle := append([]string{}, path[i:]...)
			return append(cycle, roleName)
		}
	}
	return append(append([]string{}, path...), roleName)
}

func validateRoleRollingUpdate(index int, ru *RollingUpdate, replicas int32) []error {
	var errs []error

	maxSurge, surgeErr := scaledIntOrPercent(ru.MaxSurge, replicas, true, 0)
	if surgeErr != nil {
		errs = append(errs, fmt.Errorf(
			"spec.roles[%d].rolloutStrategy.rollingUpdate.maxSurge: invalid value %q",
			index, ru.MaxSurge.String(),
		))
	}

	maxUnavailable, unavailErr := scaledIntOrPercent(ru.MaxUnavailable, replicas, false, 1)
	if unavailErr != nil {
		errs = append(errs, fmt.Errorf(
			"spec.roles[%d].rolloutStrategy.rollingUpdate.maxUnavailable: invalid value %q",
			index, ru.MaxUnavailable.String(),
		))
	}

	if surgeErr == nil && unavailErr == nil && maxSurge == 0 && maxUnavailable == 0 {
		errs = append(errs, fmt.Errorf(
			"spec.roles[%d].rolloutStrategy.rollingUpdate: maxSurge and maxUnavailable cannot both be 0",
			index,
		))
	}

	if ru.Partition != nil {
		partition, err := intstr.GetScaledValueFromIntOrPercent(ru.Partition, int(replicas), true)
		if err != nil {
			errs = append(errs, fmt.Errorf(
				"spec.roles[%d].rolloutStrategy.rollingUpdate.partition: invalid value %q",
				index, ru.Partition.String(),
			))
		} else if int32(partition) >= replicas {
			errs = append(errs, fmt.Errorf(
				"spec.roles[%d].rolloutStrategy.rollingUpdate.partition: %d must be less than replicas %d",
				index, partition, replicas,
			))
		}
	}

	return errs
}

func ValidateScalingAdapterReplicas(ctx context.Context, reader client.Reader, oldRBG, newRBG *RoleBasedGroup) error {
	if reader == nil {
		return fmt.Errorf("RoleBasedGroup validator requires a Kubernetes client")
	}

	oldRoles := make(map[string]*RoleSpec, len(oldRBG.Spec.Roles))
	for i := range oldRBG.Spec.Roles {
		role := &oldRBG.Spec.Roles[i]
		oldRoles[role.Name] = role
	}

	allErrs := make([]error, 0)
	for i := range newRBG.Spec.Roles {
		newRole := &newRBG.Spec.Roles[i]
		if newRole.ScalingAdapter == nil || !newRole.ScalingAdapter.Enable {
			continue
		}
		oldRole, ok := oldRoles[newRole.Name]
		if !ok || roleReplicasEqual(oldRole.Replicas, newRole.Replicas) {
			continue
		}

		adapterName := GenerateScalingAdapterName(newRBG.Name, newRole.Name)
		adapter := &RoleBasedGroupScalingAdapter{}
		if err := reader.Get(ctx, types.NamespacedName{Namespace: newRBG.Namespace, Name: adapterName}, adapter); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			allErrs = append(allErrs, fmt.Errorf("failed to get ScalingAdapter %s/%s: %w", newRBG.Namespace, adapterName, err))
			continue
		}
		if adapter.Spec.Replicas == nil || newRole.Replicas == nil || *adapter.Spec.Replicas == *newRole.Replicas {
			continue
		}

		allErrs = append(allErrs, fmt.Errorf(
			"spec.roles[%d].replicas (role %q): cannot be changed to %d while scalingAdapter.enable is true and ScalingAdapter %q has spec.replicas %d",
			i, newRole.Name, *newRole.Replicas, adapterName, *adapter.Spec.Replicas,
		))
	}
	return utilerrors.NewAggregate(allErrs)
}

func roleReplicasEqual(left, right *int32) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

// scaledIntOrPercent resolves an *intstr.IntOrString against replicas. When the
// pointer is nil, defaultVal is returned with no error so callers can apply the
// CRD-level defaults uniformly.
func scaledIntOrPercent(v *intstr.IntOrString, replicas int32, roundUp bool, defaultVal int) (int, error) {
	if v == nil {
		return defaultVal, nil
	}
	return intstr.GetScaledValueFromIntOrPercent(v, int(replicas), roundUp)
}

// deprecatedWorkloadTypeHint explains a non-obvious consequence of turning off the
// deprecated workload types: a role can carry one even when the user never wrote
// it. The v1alpha1 schema defaults spec.roles[].workload to apps/v1 StatefulSet,
// and the conversion webhook records that defaulted value in the
// role-workload-type annotation this validator reads. A v1alpha1 role must
// therefore name RoleInstanceSet explicitly to be accepted, so the error points at
// that rather than leaving the user looking for a field they never set.
const deprecatedWorkloadTypeHint = "note: the v1alpha1 schema defaults spec.roles[].workload to apps/v1 StatefulSet, " +
	"so a role submitted via the v1alpha1 API carries a deprecated workload type even if you never set one; " +
	"fix: set workload.apiVersion=workloads.x-k8s.io/v1alpha2 and workload.kind=RoleInstanceSet on the role " +
	"(or submit the object as v1alpha2, where RoleInstanceSet is the default), or re-enable the deprecated " +
	"workload types (Helm: controller.deprecatedWorkloadTypes.enabled=true, " +
	"controller: --enable-deprecated-workload-types=true)"

// isDeprecatedWorkloadType reports whether wt is one of the workload types
// superseded by RoleInstanceSet.
func isDeprecatedWorkloadType(wt string) bool {
	switch wt {
	case constants.DeploymentWorkloadType, constants.StatefulSetWorkloadType, constants.LeaderWorkerSetWorkloadType:
		return true
	default:
		return false
	}
}

// validateNoDeprecatedWorkloadTypes checks that no role uses a deprecated workload
// type (Deployment, StatefulSet, or LeaderWorkerSet). fieldPath is the JSON path of
// the role slice being validated, since roles live under different paths in a
// RoleBasedGroup and a RoleBasedGroupSet. Returns an aggregated error listing all
// offending roles, suffixed with deprecatedWorkloadTypeHint.
//
// The whole object is checked on both create and update. A cluster with the toggle
// off never reconciles these workload types at all — it grants no RBAC for them and
// watches none of them — so admitting a write that carries one would leave an object
// nothing can act on.
func validateNoDeprecatedWorkloadTypes(fieldPath string, roles []RoleSpec) error {
	var allErrs []error
	for i := range roles {
		role := &roles[i]
		wt := role.GetWorkloadType()
		if isDeprecatedWorkloadType(wt) {
			allErrs = append(allErrs, fmt.Errorf(
				"%s[%d] (role %q): workload type %q is deprecated and not enabled on this cluster",
				fieldPath, i, role.Name, wt,
			))
		}
	}
	if len(allErrs) == 0 {
		return nil
	}
	// Wrap once instead of repeating the hint per role: with several offending
	// roles the hint is identical and would otherwise dominate the message.
	return fmt.Errorf("%w; %s", utilerrors.NewAggregate(allErrs), deprecatedWorkloadTypeHint)
}

// ValidateRoleTopologyConstraints performs self-contained syntax validation on every
// role-level instance topology constraint. Level existence and parent/child ordering
// are scheduler-dialect-specific and are validated during reconcile.
func ValidateRoleTopologyConstraints(rbg *RoleBasedGroup) error {
	return validateRoleTopologyConstraints("spec.roles", rbg.Spec.Roles)
}

func validateRoleTopologyConstraints(fieldPath string, roles []RoleSpec) error {
	var allErrs []error
	for i := range roles {
		role := &roles[i]
		if role.InstanceTopologyConstraint == nil {
			continue
		}
		if err := ValidateTopologyConstraint(
			fmt.Sprintf("%s[%d].instanceTopologyConstraint", fieldPath, i),
			role.InstanceTopologyConstraint,
		); err != nil {
			allErrs = append(allErrs, err)
		}
	}
	return utilerrors.NewAggregate(allErrs)
}

// ValidateTopologyConstraint validates the non-dialect-specific parts of a topology
// constraint. A non-nil constraint must define a pack level; otherwise it would be
// treated as topology-bearing without conveying a schedulable requirement. Empty
// strings are rejected because they are almost always typos, while a missing object
// means the constraint is disabled.
func ValidateTopologyConstraint(path string, constraint *TopologyConstraint) error {
	if constraint == nil {
		return nil
	}
	var errs []error
	if constraint.Pack == nil || (constraint.Pack.Required == nil && constraint.Pack.Preferred == nil) {
		errs = append(errs, fmt.Errorf(
			"%s.pack must specify at least one of required or preferred", path))
	}
	if constraint.TopologyName != nil {
		if err := validateTopologyIdentifier(path+".topologyName", *constraint.TopologyName); err != nil {
			errs = append(errs, err)
		}
	}
	if constraint.Pack != nil {
		if constraint.Pack.Required != nil {
			if err := validateTopologyIdentifier(path+".pack.required", *constraint.Pack.Required); err != nil {
				errs = append(errs, err)
			}
		}
		if constraint.Pack.Preferred != nil {
			if err := validateTopologyIdentifier(path+".pack.preferred", *constraint.Pack.Preferred); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return utilerrors.NewAggregate(errs)
}

func validateTopologyIdentifier(path, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", path)
	}
	if len(value) > 253 {
		return fmt.Errorf("%s must be at most 253 characters, got %d", path, len(value))
	}
	return nil
}

// ValidateRoleTopologyImmutability rejects changes to the set of role-level topology
// declarations. Topology is a launch-time placement contract: adding, removing, or
// changing a declaration requires deleting and recreating the RBG. Ordinary changes
// to roles that never carry a topology constraint remain allowed.
func ValidateRoleTopologyImmutability(oldRBG, newRBG *RoleBasedGroup) error {
	oldTopologies := roleTopologyDeclarations(oldRBG.Spec.Roles)
	newTopologies := roleTopologyDeclarations(newRBG.Spec.Roles)
	if len(oldTopologies) != len(newTopologies) {
		return fmt.Errorf(
			"spec.roles topology constraints are immutable for the RoleBasedGroup lifecycle; delete and recreate the workload to add or remove one")
	}
	for roleName, oldConstraint := range oldTopologies {
		newConstraint, exists := newTopologies[roleName]
		if !exists || !TopologyConstraintsEqual(oldConstraint, newConstraint) {
			return fmt.Errorf(
				"spec.roles[%s].instanceTopologyConstraint is immutable for the RoleBasedGroup lifecycle; delete and recreate the workload to change it",
				roleName,
			)
		}
	}
	return nil
}

func roleTopologyDeclarations(roles []RoleSpec) map[string]*TopologyConstraint {
	declarations := make(map[string]*TopologyConstraint, len(roles))
	for i := range roles {
		if roles[i].InstanceTopologyConstraint != nil {
			declarations[roles[i].Name] = roles[i].InstanceTopologyConstraint
		}
	}
	return declarations
}

func TopologyConstraintsEqual(left, right *TopologyConstraint) bool {
	return reflect.DeepEqual(left, right)
}

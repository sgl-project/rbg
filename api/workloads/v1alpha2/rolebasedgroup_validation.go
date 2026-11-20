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
	"fmt"
	"reflect"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
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

// ValidateRestartPolicyWorkloadCompatibility rejects policies that the backing
// workload cannot implement faithfully.
func ValidateRestartPolicyWorkloadCompatibility(fieldPath string, roles []RoleSpec) error {
	var allErrs []error
	for i := range roles {
		role := &roles[i]
		cfg := role.GetRawRestartPolicyConfig()
		if cfg != nil && cfg.MaxGroupRestarts != nil {
			if *cfg.MaxGroupRestarts < 0 || cfg.Type != RecreateRoleInstanceOnPodRestart {
				allErrs = append(allErrs, fmt.Errorf("%s[%d] (role %q): maxGroupRestarts must be non-negative and requires a whole-RoleInstance recreate policy", fieldPath, i, role.Name))
			}
			if role.GetWorkloadType() != constants.RoleInstanceSetWorkloadType {
				allErrs = append(allErrs, fmt.Errorf("%s[%d] (role %q): maxGroupRestarts is only supported by native RoleInstanceSet workloads", fieldPath, i, role.Name))
			}
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
	for i := range roles {
		roleNames[roles[i].Name] = struct{}{}
	}

	var allErrs []error
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

// maxEngineRuntimeValidationErrors caps how many engine-runtime validation
// errors one role's aggregate carries. The engineRuntimes list is uncapped by
// API contract, so a pathological object can violate the presence, scope,
// vocabulary or env rules once per reference — tens of thousands of errors.
// The webhook path renders the aggregate with err.Error(), whose += build is
// quadratic in the message length: 5k presence violations (~260 KiB of JSON)
// took 4.4 s and answered with a 618 KiB error, and 10k hit the webhook's
// 10 s timeout, hiding the violations behind a context deadline. The resolver
// re-runs this validation on every reconcile, so the cap bounds the recurring
// cost too. Every violation is still counted — the aggregate ends with an
// "and N more" line — but only the first maxEngineRuntimeValidationErrors
// carry a message.
const maxEngineRuntimeValidationErrors = 20

// ValidateEngineRuntimes enforces the role-side engine-runtime invariants
// the role's own object can answer (KEP-531 Validation Layer Split):
// targetContainers requiring component except under standalonePattern,
// same-profile effective-component-set overlap (D6, with the
// compatibility-shape exemption), a component outside the pattern's
// vocabulary (standalone's must-equal-the-role's-name as the special
// case), duplicate env-name creation within one targetContainers entry's
// extraPatches, and the new fields present on a role whose workload type
// routes through a deprecated surface (Deployment/StatefulSet/LWS). The
// RoleBasedGroup and RoleBasedGroupSet webhooks invoke it at create and
// update, re-validating the whole object on each — so flipping a written
// role's workload-type annotation to a deprecated surface is itself a
// rejected spec update. The render-time resolver re-runs it as the
// authoritative gate for writes stored with the webhook absent: an
// uninstalled webhook configuration validates nothing, and objects written
// during an old-controller window reach the new renderer by reconcile, not
// by admission, failing there with EngineRuntimeRenderFailed. The presence
// rule is webhook-enforced rather than CEL: the apiserver's CEL cost
// budget rejects a comprehension over the uncapped engineRuntimes list
// by more than 100x, the KEP-531 cost-budget remedy. Errors are capped at
// maxEngineRuntimeValidationErrors plus one "and N more" line.
func ValidateEngineRuntimes(role *RoleSpec) error {
	if role == nil || len(role.EngineRuntimes) == 0 {
		return nil
	}
	allErrs := validateEngineRuntimeTargetContainersPresence(role)
	allErrs = append(allErrs, validateEngineRuntimeScopeOwnership(role)...)
	allErrs = append(allErrs, validateEngineRuntimeComponentVocabulary(role)...)
	allErrs = append(allErrs, validateEngineRuntimeEnvCreation(role)...)
	allErrs = append(allErrs, validateEngineRuntimeNewFieldsOnDeprecatedSurface(role)...)
	if len(allErrs) > maxEngineRuntimeValidationErrors {
		excess := len(allErrs) - maxEngineRuntimeValidationErrors
		allErrs = append(allErrs[:maxEngineRuntimeValidationErrors:maxEngineRuntimeValidationErrors],
			fmt.Errorf("and %d more engine runtime validation errors", excess))
	}
	return utilerrors.NewAggregate(allErrs)
}

// engineRuntimeRoleSurface is the slice of RoleSpec that
// ValidateEngineRuntimes reads: the references themselves, the role's
// name, the three pattern fields, and the workload type. An update that
// leaves all of these untouched cannot have changed any engine-runtime
// validation outcome for that role.
type engineRuntimeRoleSurface struct {
	name                    string
	workloadType            string
	engineRuntimes          []EngineRuntime
	standalonePattern       *StandalonePattern
	leaderWorkerPattern     *LeaderWorkerPattern
	customComponentsPattern *CustomComponentsPattern
}

func engineRuntimeRoleSurfaceOf(role *RoleSpec) engineRuntimeRoleSurface {
	return engineRuntimeRoleSurface{
		name:                    role.Name,
		workloadType:            role.GetWorkloadType(),
		engineRuntimes:          role.EngineRuntimes,
		standalonePattern:       role.StandalonePattern,
		leaderWorkerPattern:     role.LeaderWorkerPattern,
		customComponentsPattern: role.CustomComponentsPattern,
	}
}

// validateNoNewEngineRuntimeViolations runs the shared engine-runtime
// validation over the roles of a write, exempting roles whose
// engine-runtime surface is unchanged from oldRoles — the old-vs-new
// exemption style of validateNoNewDeprecatedWorkloadTypes, so an update
// rejects only what it newly introduces. Without it, an object written
// while the webhook was absent (--enable-webhooks=none, or the
// old-controller window with the new CRDs) carrying e.g.
// targetContainers without component would wedge: every update, even a
// label-only one, would be rejected until a corrective spec write. The
// render-time resolver remains the authoritative gate for the stored
// violation. Roles are keyed by name, mirroring
// validateNoNewDeprecatedWorkloadTypes; a nil oldRoles slice validates
// every role, which is the create path.
func validateNoNewEngineRuntimeViolations(roles, oldRoles []RoleSpec) error {
	oldSurfaces := make(map[string]engineRuntimeRoleSurface, len(oldRoles))
	for i := range oldRoles {
		oldSurfaces[oldRoles[i].Name] = engineRuntimeRoleSurfaceOf(&oldRoles[i])
	}

	var allErrs []error
	for i := range roles {
		role := &roles[i]
		if old, existed := oldSurfaces[role.Name]; existed &&
			reflect.DeepEqual(old, engineRuntimeRoleSurfaceOf(role)) {
			continue
		}
		if err := ValidateEngineRuntimes(role); err != nil {
			allErrs = append(allErrs, err)
		}
	}
	return utilerrors.NewAggregate(allErrs)
}

// validateEngineRuntimeTargetContainersPresence enforces the
// cross-field presence rule: a reference entry carrying targetContainers
// must name a component, except under standalonePattern where the entry
// is scoped to the role's own name. CEL cannot carry the rule — a
// comprehension over the uncapped engineRuntimes list at the uncapped
// roles[].scope yields an O(n^2) cost estimate the apiserver rejects —
// so this is its write-time home.
func validateEngineRuntimeTargetContainersPresence(role *RoleSpec) []error {
	if role.StandalonePattern != nil {
		return nil
	}
	var allErrs []error
	for i := range role.EngineRuntimes {
		er := &role.EngineRuntimes[i]
		if len(er.TargetContainers) > 0 && er.Component == "" {
			allErrs = append(allErrs, fmt.Errorf(
				"role %q reference %q (engineRuntimes[%d]): targetContainers requires component to be set, except under standalonePattern",
				role.Name, er.ProfileName, i))
		}
	}
	return allErrs
}

// validateEngineRuntimeScopeOwnership enforces D6: for one profileName,
// any two references whose effective component sets intersect are an
// error, except the pure compatibility shape — any number of references
// all lacking component and carrying no targetContainers — which keeps
// main's duplicate-reference semantics verbatim.
//
// Both the engineRuntimes list and a customComponentsPattern's components
// list are uncapped by API contract, so the check records each reference
// once, not once per (reference, component) of its effective set: a
// component-less compat reference under customComponentsPattern would
// otherwise be recorded under every declared component — n × C entries,
// which OOMKilled a serving replica at n = C = 6000 on nothing but a
// server-side dry run. Compat references never conflict with each other,
// so they are recorded once per profile as an index list; every other
// admissible reference covers exactly one component (a component-less
// reference carrying targetContainers is admitted only under
// standalonePattern, whose effective set is the singleton {role.Name},
// and the presence rule rejects it on every other pattern in the same
// validation pass, so its scope overlaps are moot), so it is recorded
// under that one component. A one-component reference overlaps the
// compat references of its profile exactly when its component belongs to
// the pattern's default set, which is computed once. The pairwise walk
// over one component's references still stops at
// maxScopeOwnershipErrors, and only the first
// maxScopeOwnershipErrors overlaps carry a message.
func validateEngineRuntimeScopeOwnership(role *RoleSpec) []error {
	const maxScopeOwnershipErrors = 20

	// The pattern's default component set, computed once: the effective
	// set of every compat reference of the role, and therefore the overlap
	// condition between a compat reference and a one-component reference.
	defaultSet := engineRuntimeEffectiveComponents(role, &EngineRuntime{})

	// coverage: profileName → component → reference indices in list order,
	// one entry per non-compat reference. compat: profileName → reference
	// indices in list order, one entry per compat reference.
	coverage := make(map[string]map[string][]int)
	compat := make(map[string][]int)
	record := func(profileName, component string, index int) {
		components, ok := coverage[profileName]
		if !ok {
			components = make(map[string][]int, 1)
			coverage[profileName] = components
		}
		components[component] = append(components[component], index)
	}
	for i := range role.EngineRuntimes {
		er := &role.EngineRuntimes[i]
		switch {
		case er.Component == "" && len(er.TargetContainers) == 0:
			compat[er.ProfileName] = append(compat[er.ProfileName], i)
		case er.Component != "":
			record(er.ProfileName, er.Component, i)
		case role.StandalonePattern != nil:
			// component-less with targetContainers: legal only under
			// standalonePattern, where the effective set is {role.Name}
			record(er.ProfileName, role.Name, i)
		default:
			// component-less with targetContainers on another pattern:
			// rejected by the presence rule in this same validation pass
		}
	}

	// Walk the profile and component keys in sorted order so the reported
	// pairs — and which pairs survive the maxScopeOwnershipErrors cap — are
	// deterministic despite the maps.
	profileNames := make([]string, 0, len(coverage))
	for profileName := range coverage {
		profileNames = append(profileNames, profileName)
	}
	sort.Strings(profileNames)

	var allErrs []error
	for _, profileName := range profileNames {
		components := coverage[profileName]
		componentNames := make([]string, 0, len(components))
		for component := range components {
			componentNames = append(componentNames, component)
		}
		sort.Strings(componentNames)
		compatRefs := compat[profileName]
		for _, component := range componentNames {
			nonCompat := components[component]
			if len(nonCompat) < 2 && len(compatRefs) == 0 {
				continue
			}
			report := func(i, j int) {
				if len(allErrs) >= maxScopeOwnershipErrors {
					return
				}
				if i > j {
					i, j = j, i
				}
				allErrs = append(allErrs, fmt.Errorf(
					"role %q reference %q: component %q is covered by both engineRuntimes[%d] and engineRuntimes[%d] — same-profile references must not have overlapping effective component sets",
					role.Name, profileName, component, i, j))
			}
			for a := 0; a < len(nonCompat); a++ {
				for b := a + 1; b < len(nonCompat); b++ {
					if len(allErrs) >= maxScopeOwnershipErrors {
						return allErrs
					}
					report(nonCompat[a], nonCompat[b])
				}
			}
			// The compat references cover the pattern's default set, so a
			// one-component reference overlaps them exactly when its
			// component is in that set.
			if _, inDefaults := defaultSet[component]; !inDefaults {
				continue
			}
			for _, i := range nonCompat {
				for _, j := range compatRefs {
					if len(allErrs) >= maxScopeOwnershipErrors {
						return allErrs
					}
					report(i, j)
				}
			}
		}
	}
	return allErrs
}

// engineRuntimeEffectiveComponents expands one reference to its effective
// component set (KEP-531 Effective Component Set): the named component
// when set, else the pattern's default set. It is a function of the
// pattern alone — no profile content — so the webhook can compute it.
// This is the write-time twin of the resolver's positionVocabulary
// (pkg/discovery/runtime_resolver.go); the two are an intentional mirror,
// not a shared helper: the KEP's validation layer split keeps them in
// separate packages, and they diverge by design — the resolver
// additionally collapses the vocabulary for legacy Deployment/StatefulSet
// renders (the single role template), while this side always uses the
// pattern shape (D6 overlap is judged on pattern semantics regardless of
// render path). Both sides are pinned by tests
// (TestValidateEngineRuntimes, TestResolveEngineRuntimesVocabulary).
func engineRuntimeEffectiveComponents(role *RoleSpec, er *EngineRuntime) map[string]struct{} {
	if er.Component != "" {
		return map[string]struct{}{er.Component: {}}
	}
	switch {
	case role.LeaderWorkerPattern != nil:
		// main's default reaches the leader only: the worker template's
		// injector chain omits the sidecar stage.
		return map[string]struct{}{string(constants.LeaderComponentType): {}}
	case role.CustomComponentsPattern != nil:
		set := make(map[string]struct{}, len(role.CustomComponentsPattern.Components))
		for _, c := range role.CustomComponentsPattern.Components {
			set[c.Name] = struct{}{}
		}
		return set
	case role.StandalonePattern != nil:
		// The standalone component is named after the role (KEP-173).
		return map[string]struct{}{role.Name: {}}
	}
	return nil
}

// validateEngineRuntimeComponentVocabulary checks every named component
// against the pattern's vocabulary (KEP-531 Plan-Time Vocabulary Check):
// `leader`/`worker` under leaderWorkerPattern, a declared component name
// under customComponentsPattern, the role's own name under
// standalonePattern. An unknown name would otherwise create an unused
// plan key and silently inject nothing, so it is rejected at write time
// in every pattern, with the standalone row's wording kept as its
// special case.
func validateEngineRuntimeComponentVocabulary(role *RoleSpec) []error {
	vocabulary := make(map[string]struct{})
	switch {
	case role.LeaderWorkerPattern != nil:
		vocabulary[string(constants.LeaderComponentType)] = struct{}{}
		vocabulary[string(constants.WorkerComponentType)] = struct{}{}
	case role.CustomComponentsPattern != nil:
		for _, c := range role.CustomComponentsPattern.Components {
			vocabulary[c.Name] = struct{}{}
		}
	case role.StandalonePattern != nil:
		vocabulary[role.Name] = struct{}{}
	default:
		vocabulary[role.Name] = struct{}{}
	}
	var allErrs []error
	for i := range role.EngineRuntimes {
		er := &role.EngineRuntimes[i]
		if er.Component == "" {
			continue
		}
		if _, ok := vocabulary[er.Component]; ok {
			continue
		}
		if role.StandalonePattern != nil {
			allErrs = append(allErrs, fmt.Errorf(
				"role %q reference %q (engineRuntimes[%d]): component %q must equal the role's name %q under standalonePattern",
				role.Name, er.ProfileName, i, er.Component, role.Name))
			continue
		}
		allErrs = append(allErrs, fmt.Errorf(
			"role %q reference %q (engineRuntimes[%d]): component %q is not defined by the role's pattern",
			role.Name, er.ProfileName, i, er.Component))
	}
	return allErrs
}

// validateEngineRuntimeNewFieldsOnDeprecatedSurface rejects the KEP-531
// fields (component, targetContainers) on a role whose workload type
// routes through a deprecated render surface: the new fields' semantics
// are defined for the RoleBasedGroup → RoleInstanceSet render path only.
// Unlike validateNoDeprecatedWorkloadTypes this fires regardless of the
// deprecated-workload-types toggle, and a reference carrying neither new
// field keeps main's behavior on the deprecated path verbatim.
func validateEngineRuntimeNewFieldsOnDeprecatedSurface(role *RoleSpec) []error {
	if !isDeprecatedWorkloadType(role.GetWorkloadType()) {
		return nil
	}
	var allErrs []error
	for i := range role.EngineRuntimes {
		er := &role.EngineRuntimes[i]
		if er.Component == "" && len(er.TargetContainers) == 0 {
			continue
		}
		allErrs = append(allErrs, fmt.Errorf(
			"role %q reference %q (engineRuntimes[%d]): component and targetContainers are only defined for the RoleInstanceSet render path, but the role's workload type is %q",
			role.Name, er.ProfileName, i, role.GetWorkloadType()))
	}
	return allErrs
}

// validateEngineRuntimeEnvCreation enforces the intra-list env rule
// (KEP-531 Patch Grammar): two entries in one targetContainers entry's
// extraPatches creating the same env name are an error — the earlier one
// is a dead letter, so a duplicate is always a mistake. The check needs
// the value unmarshalled, so it rides this shared function rather than
// CEL. The profile-side half fires at render instead (the CERP has no
// write-time home for it).
func validateEngineRuntimeEnvCreation(role *RoleSpec) []error {
	var allErrs []error
	for i := range role.EngineRuntimes {
		er := &role.EngineRuntimes[i]
		for j := range er.TargetContainers {
			tc := &er.TargetContainers[j]
			seen := make(map[string]struct{}, len(tc.ExtraPatches))
			for k := range tc.ExtraPatches {
				name, ok := engineRuntimeCreatedEnvName(&tc.ExtraPatches[k])
				if !ok {
					continue
				}
				if _, dup := seen[name]; dup {
					allErrs = append(allErrs, fmt.Errorf(
						"role %q reference %q targetContainers[%q].extraPatches[%d]: duplicate env name %q created within one extraPatches list",
						role.Name, er.ProfileName, tc.Name, k, name))
					continue
				}
				seen[name] = struct{}{}
			}
		}
	}
	return allErrs
}

// engineRuntimeCreatedEnvName reports the env name an extraPatches entry
// creates, or ok=false when the entry does not address /env or its value
// does not unmarshal into an env var (a shape mismatch the render pass
// reports with attribution).
func engineRuntimeCreatedEnvName(p *ContainerPatch) (name string, ok bool) {
	if p.JSONPatch == nil || !strings.HasPrefix(p.JSONPatch.Path, "/env/") {
		return "", false
	}
	var env corev1.EnvVar
	if err := json.Unmarshal(p.JSONPatch.Value.Raw, &env); err != nil || env.Name == "" {
		return "", false
	}
	return env.Name, true
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

// deprecatedWorkloadTypeCompatHint is the compatibility-mode counterpart of
// deprecatedWorkloadTypeHint. The flag is already enabled in this mode, so the
// remedy is to migrate the offending role; advising the operator to re-enable
// the flag would be a no-op.
const deprecatedWorkloadTypeCompatHint = "note: the v1alpha1 schema defaults spec.roles[].workload to apps/v1 StatefulSet, " +
	"so a role submitted via the v1alpha1 API carries a deprecated workload type even if you never set one; " +
	"fix: migrate this role to RoleInstanceSet (set workload.apiVersion=workloads.x-k8s.io/v1alpha2 and " +
	"workload.kind=RoleInstanceSet, or submit the object as v1alpha2, where RoleInstanceSet is the default); " +
	"roles that already used the deprecated workload type before the upgrade remain accepted while the " +
	"compatibility flag is enabled (Helm: controller.deprecatedWorkloadTypes.enabled=true, " +
	"controller: --enable-deprecated-workload-types=true)"

// isDeprecatedWorkloadType reports whether wt is one of the workload types
// superseded by RoleInstanceSet.
func isDeprecatedWorkloadType(wt string) bool {
	switch wt {
	case constants.DeploymentWorkloadType, constants.StatefulSetWorkloadType,
		constants.LeaderWorkerSetWorkloadType, constants.InstanceSetWorkloadType:
		return true
	default:
		return false
	}
}

// validateDiscoveryConfigVersion checks the KEP-576 content-version
// annotation. A missing value is the historical v1alpha1 content. The compact
// v1 format supports the native RoleInstanceSet backend only; deprecated
// stateful roles must stay on v1alpha1 until they are migrated.
func validateDiscoveryConfigVersion(roles []RoleSpec, version constants.DiscoveryConfigVersion, fieldPath string) error {
	switch version {
	case "", constants.DiscoveryConfigVersionV1Alpha1:
		return nil
	case constants.DiscoveryConfigVersionV1:
		for i := range roles {
			role := &roles[i]
			if role.GetWorkloadType() == constants.RoleInstanceSetWorkloadType {
				continue
			}
			if IsStatefulRole(role) {
				return fmt.Errorf(
					"%s: v1 is not supported for deprecated stateful workload type %s; keep v1alpha1 or migrate role %s to RoleInstanceSet",
					fieldPath, role.GetWorkloadType(), role.Name,
				)
			}
		}
		return nil
	default:
		return fmt.Errorf("%s: unsupported value %q; supported values are v1alpha1 and v1", fieldPath, version)
	}
}

// hasDeprecatedStatefulRole reports whether any role uses a stateful workload
// backend other than the native RoleInstanceSet. Such roles keep the
// v1alpha1 discovery content.
func hasDeprecatedStatefulRole(roles []RoleSpec) bool {
	for i := range roles {
		role := &roles[i]
		if role.GetWorkloadType() != constants.RoleInstanceSetWorkloadType && IsStatefulRole(role) {
			return true
		}
	}
	return false
}

// SupportsCompactDiscoveryContent reports whether the role set can serialize
// the KEP-576 compact discovery content. Deprecated stateful backends must
// keep the v1alpha1 enumeration.
func SupportsCompactDiscoveryContent(roles []RoleSpec) bool {
	return !hasDeprecatedStatefulRole(roles)
}

// ValidateDiscoveryConfigVersion validates the RBG discovery ConfigMap
// content-version annotation.
func ValidateDiscoveryConfigVersion(rbg *RoleBasedGroup) error {
	return validateDiscoveryConfigVersion(
		rbg.Spec.Roles,
		rbg.GetDiscoveryConfigVersion(),
		"metadata.annotations["+constants.DiscoveryConfigVersionAnnotationKey+"]",
	)
}

// validateNoDeprecatedWorkloadTypes checks that no role uses a deprecated workload
// type. This is the final cutoff mode: --enable-deprecated-workload-types=false is
// set only after every role has migrated to RoleInstanceSet.
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
	return fmt.Errorf("%w; %s", utilerrors.NewAggregate(allErrs), deprecatedWorkloadTypeHint)
}

// validateNoNewDeprecatedWorkloadTypes checks that a write does not introduce a
// deprecated workload type (Deployment, StatefulSet, LeaderWorkerSet, or the
// v1alpha1 InstanceSet) into a role that did not already use one. fieldPath is
// the JSON path of the role slice
// being validated, since roles live under different paths in a RoleBasedGroup and a
// RoleBasedGroupSet. Returns an aggregated error listing all offending roles,
// suffixed with deprecatedWorkloadTypeCompatHint.
//
// An existing role keeps its exact deprecated workload type: the exemption is
// keyed on both the role name and the type. Switching between deprecated types
// is a new use -- the reconciler is chosen per type, so a switch provisions a
// brand-new Deployment or LeaderWorkerSet (and, for LWS, a new dependency on
// the LWS CRD) while the old workload is deleted. That grows deprecated usage
// during a window meant to stop it. Migrate the role to RoleInstanceSet instead.
// When the flag is false, the strict validator above rejects every remaining use.
func validateNoNewDeprecatedWorkloadTypes(fieldPath string, roles, oldRoles []RoleSpec) error {
	oldWorkloadTypes := make(map[string]string, len(oldRoles))
	for i := range oldRoles {
		oldWorkloadTypes[oldRoles[i].Name] = oldRoles[i].GetWorkloadType()
	}

	var allErrs []error
	for i := range roles {
		role := &roles[i]
		wt := role.GetWorkloadType()
		if !isDeprecatedWorkloadType(wt) {
			continue
		}
		if oldType, existed := oldWorkloadTypes[role.Name]; !existed || oldType != wt {
			allErrs = append(allErrs, fmt.Errorf(
				"%s[%d] (role %q): workload type %q is deprecated; new uses are not accepted",
				fieldPath, i, role.Name, wt,
			))
		}
	}
	if len(allErrs) == 0 {
		return nil
	}
	// Wrap once instead of repeating the hint per role: with several offending
	// roles the hint is identical and would otherwise dominate the message.
	return fmt.Errorf("%w; %s", utilerrors.NewAggregate(allErrs), deprecatedWorkloadTypeCompatHint)
}

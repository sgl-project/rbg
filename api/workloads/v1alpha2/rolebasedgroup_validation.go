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

// deprecatedWorkloadTypeUpdateHint is the counterpart of deprecatedWorkloadTypeHint
// for updates. A role keeps the deprecated workload type it already has, so the hint
// has to explain what does count as introducing one — including the
// v1alpha1 defaulting trap, since re-applying a v1alpha1 manifest that omits
// spec.roles[].workload silently changes the role to apps/v1 StatefulSet.
const deprecatedWorkloadTypeUpdateHint = "note: roles that already use a deprecated workload type keep working; " +
	"only adding a role with one, or changing a role to a different one, is rejected. " +
	"The v1alpha1 schema defaults spec.roles[].workload to apps/v1 StatefulSet, so re-applying a v1alpha1 " +
	"manifest that omits workload counts as such a change; " +
	"fix: keep the role's workload type unchanged, set workload.kind=RoleInstanceSet " +
	"(apiVersion workloads.x-k8s.io/v1alpha2), or re-enable the deprecated workload types " +
	"(Helm: controller.deprecatedWorkloadTypes.enabled=true, " +
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
// This is the create-time check: updates go through
// validateNoNewDeprecatedWorkloadTypes so that pre-existing roles stay writable.
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

// validateNoNewDeprecatedWorkloadTypes checks that an update does not introduce a
// deprecated workload type, rather than rejecting every object that still carries
// one. A role keeps its existing deprecated type; adding a role that uses one, or
// switching a role to a different one, is rejected.
//
// Rejecting the whole object on update would also deny the controllers' own writes
// to pre-existing groups — the discovery-mode annotation patch, the RoleBasedGroupSet
// template sync, and the ScalingAdapter replica update all rewrite roles they do not
// change — leaving those controllers retrying forever with no sign on the object.
// Requiring the type to be unchanged (rather than merely "was deprecated before")
// additionally prevents swapping one deprecated type for another.
func validateNoNewDeprecatedWorkloadTypes(fieldPath string, oldRoles, newRoles []RoleSpec) error {
	previousTypes := make(map[string]string, len(oldRoles))
	for i := range oldRoles {
		role := &oldRoles[i]
		previousTypes[role.Name] = role.GetWorkloadType()
	}

	var allErrs []error
	for i := range newRoles {
		role := &newRoles[i]
		wt := role.GetWorkloadType()
		if !isDeprecatedWorkloadType(wt) {
			continue
		}
		previousType, existed := previousTypes[role.Name]
		switch {
		case !existed:
			allErrs = append(allErrs, fmt.Errorf(
				"%s[%d] (role %q): workload type %q is deprecated and not enabled on this cluster, "+
					"so it cannot be used by a newly added role",
				fieldPath, i, role.Name, wt,
			))
		case previousType != wt:
			allErrs = append(allErrs, fmt.Errorf(
				"%s[%d] (role %q): workload type cannot be changed from %q to %q, "+
					"because %q is deprecated and not enabled on this cluster",
				fieldPath, i, role.Name, previousType, wt, wt,
			))
		}
	}
	if len(allErrs) == 0 {
		return nil
	}
	return fmt.Errorf("%w; %s", utilerrors.NewAggregate(allErrs), deprecatedWorkloadTypeUpdateHint)
}

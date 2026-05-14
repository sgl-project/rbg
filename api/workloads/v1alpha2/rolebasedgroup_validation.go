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

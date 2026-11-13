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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/api/workloads/ordinals"
)

// ValidateExcludedOrdinals validates content and placement, not caller identity.
// It is also called at reconciliation when admission is disabled.
func ValidateExcludedOrdinals(rbg *RoleBasedGroup) error {
	return validateExcludedOrdinals(rbg, isRBGSetManaged(rbg))
}

// ValidateRoleExcludedOrdinals scopes the reconciliation backstop to one role.
// Admission still validates the complete object. Preserve the original role
// index in errors and retain the parent's managed-placement restriction.
func ValidateRoleExcludedOrdinals(rbg *RoleBasedGroup, role *RoleSpec) error {
	index := 0
	for i := range rbg.Spec.Roles {
		if rbg.Spec.Roles[i].Name == role.Name {
			index = i
			break
		}
	}
	errs := validateExcludedOrdinalsRole(role, field.NewPath("spec", "roles").Index(index), isRBGSetManaged(rbg))
	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(GroupVersion.WithKind(roleBasedGroupKind).GroupKind(), rbg.Name, errs)
}

func isRBGSetManaged(rbg *RoleBasedGroup) bool {
	for _, owner := range rbg.OwnerReferences {
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		if err == nil && gv.Group == GroupVersion.Group && owner.Kind == roleBasedGroupSetKind {
			return true
		}
	}
	return false
}

func validateExcludedOrdinals(rbg *RoleBasedGroup, managed bool) error {
	errs := validateExcludedOrdinalsRoles(rbg.Spec.Roles, field.NewPath("spec", "roles"), managed)
	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(GroupVersion.WithKind(roleBasedGroupKind).GroupKind(), rbg.Name, errs)
}

func validateExcludedOrdinalsRoles(roles []RoleSpec, path *field.Path, managed bool) field.ErrorList {
	errs := make(field.ErrorList, 0, len(roles))
	for i := range roles {
		errs = append(errs, validateExcludedOrdinalsRole(&roles[i], path.Index(i), managed)...)
	}
	return errs
}

func validateExcludedOrdinalsRole(role *RoleSpec, path *field.Path, managed bool) field.ErrorList {
	var errs field.ErrorList
	annotations := path.Child("annotations")
	if _, ok := role.Annotations[constants.CommittedSkipOrdinalsAnnotationKey]; ok {
		errs = append(errs, field.Forbidden(annotations.Key(constants.CommittedSkipOrdinalsAnnotationKey), "reserved for controller-managed child RIS membership"))
	}
	value, ok := role.Annotations[constants.ExcludedOrdinalsAnnotationKey]
	if !ok {
		return errs
	}
	key := annotations.Key(constants.ExcludedOrdinalsAnnotationKey)
	if managed {
		errs = append(errs, field.Forbidden(key, "not supported on RBGSet templates or RBGSet-managed RBGs"))
	}
	pattern := role.Annotations[constants.RoleInstancePatternKey]
	if role.GetWorkloadType() != constants.RoleInstanceSetWorkloadType || (pattern != "" && pattern != string(constants.StatefulPattern)) {
		errs = append(errs, field.Forbidden(key, "requires the native Stateful RoleInstanceSet backend"))
	}
	if _, err := ordinals.Parse(value); err != nil {
		errs = append(errs, field.Invalid(key, value, err.Error()))
	}
	return errs
}

func validateRBGSetExcludedOrdinals(rbgs *RoleBasedGroupSet) error {
	errs := validateExcludedOrdinalsRoles(rbgs.Spec.GroupTemplate.Spec.Roles, field.NewPath("spec", "groupTemplate", "spec", "roles"), true)
	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(GroupVersion.WithKind(roleBasedGroupSetKind).GroupKind(), rbgs.Name, errs)
}

// CommittedOrdinals reads desired membership, independently of live instances.
func (set *RoleInstanceSet) CommittedOrdinals() ([]int, error) {
	skips, err := ordinals.Parse(set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", constants.CommittedSkipOrdinalsAnnotationKey, err)
	}
	n := 0
	if set.Spec.Replicas != nil {
		n = int(*set.Spec.Replicas)
	}
	return skips.Members(n)
}

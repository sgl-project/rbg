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
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// RoleBasedGroupSetValidator implements admission.CustomValidator for RoleBasedGroupSet.
//
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha2-rolebasedgroupset,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroupsets,verbs=create;update,versions=v1alpha2,name=vrolebasedgroupset.kb.io,admissionReviewVersions=v1
// +kubebuilder:object:generate=false
type RoleBasedGroupSetValidator struct {
	// EnableDeprecatedWorkloadTypes reports whether the deprecated workload types
	// (Deployment, StatefulSet, LeaderWorkerSet) are still accepted. When false,
	// RBGSets whose template uses them are rejected.
	EnableDeprecatedWorkloadTypes bool
}

var _ admission.CustomValidator = &RoleBasedGroupSetValidator{}

// ValidateCreate validates a RoleBasedGroupSet on creation.
func (v *RoleBasedGroupSetValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rbgs, ok := obj.(*RoleBasedGroupSet)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroupSet but got %T", obj)
	}
	klog.V(4).InfoS("validating RoleBasedGroupSet on create", "name", rbgs.Name, "namespace", rbgs.Namespace)

	var allErrs []error
	if err := validateGroupSetRolloutStrategy(&rbgs.Spec); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := validateGroupSetScalingAdapters(nil, rbgs.Spec.GroupTemplate.Spec.Roles); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := validateRoleDependencies("spec.groupTemplate.spec.roles", rbgs.Spec.GroupTemplate.Spec.Roles); err != nil {
		allErrs = append(allErrs, err)
	}
	if !v.EnableDeprecatedWorkloadTypes {
		if err := validateNoDeprecatedWorkloadTypes("spec.groupTemplate.spec.roles", rbgs.Spec.GroupTemplate.Spec.Roles); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	return nil, utilerrors.NewAggregate(allErrs)
}

// ValidateUpdate validates a RoleBasedGroupSet on update.
func (v *RoleBasedGroupSetValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldRBGS, ok := oldObj.(*RoleBasedGroupSet)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroupSet but got %T", oldObj)
	}
	rbgs, ok := newObj.(*RoleBasedGroupSet)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroupSet but got %T", newObj)
	}
	klog.V(4).InfoS("validating RoleBasedGroupSet on update", "name", rbgs.Name, "namespace", rbgs.Namespace)

	var allErrs []error
	if err := validateGroupSetRolloutStrategy(&rbgs.Spec); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := validateGroupSetScalingAdapters(oldRBGS.Spec.GroupTemplate.Spec.Roles, rbgs.Spec.GroupTemplate.Spec.Roles); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := validateRoleDependencies("spec.groupTemplate.spec.roles", rbgs.Spec.GroupTemplate.Spec.Roles); err != nil {
		allErrs = append(allErrs, err)
	}
	if !v.EnableDeprecatedWorkloadTypes {
		if err := validateNoDeprecatedWorkloadTypes(
			"spec.groupTemplate.spec.roles",
			rbgs.Spec.GroupTemplate.Spec.Roles,
		); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	return nil, utilerrors.NewAggregate(allErrs)
}

// ValidateDelete just implements admission.CustomValidator. This verb is currently no-op.
func (v *RoleBasedGroupSetValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateGroupSetRolloutStrategy(spec *RoleBasedGroupSetSpec) error {
	strategy := spec.RolloutStrategy
	if strategy == nil {
		return nil
	}

	var allErrs []error
	if strategy.Type != "" && strategy.Type != RecreateStrategyType {
		allErrs = append(allErrs, fmt.Errorf("spec.rolloutStrategy.type: unsupported value %q; only Recreate is supported", strategy.Type))
	}

	maxUnavailable, unavailablePercent, unavailableErr := parseGroupSetIntOrPercent(strategy.MaxUnavailable, 1)
	if unavailableErr != nil {
		allErrs = append(allErrs, fmt.Errorf("spec.rolloutStrategy.maxUnavailable: %w", unavailableErr))
	}
	maxSurge, surgePercent, surgeErr := parseGroupSetIntOrPercent(strategy.MaxSurge, 0)
	if surgeErr != nil {
		allErrs = append(allErrs, fmt.Errorf("spec.rolloutStrategy.maxSurge: %w", surgeErr))
	}
	if unavailableErr == nil && surgeErr == nil && !unavailablePercent && !surgePercent && maxUnavailable == 0 && maxSurge == 0 {
		allErrs = append(allErrs, fmt.Errorf("spec.rolloutStrategy: maxUnavailable and maxSurge cannot both be integer 0"))
	}

	replicas := int64(1)
	if spec.Replicas != nil {
		replicas = int64(*spec.Replicas)
	}
	partition, partitionPercent, partitionErr := parseGroupSetIntOrPercent(strategy.Partition, 0)
	if partitionErr != nil {
		allErrs = append(allErrs, fmt.Errorf("spec.rolloutStrategy.partition: %w", partitionErr))
	} else if partitionPercent && partition > 100 {
		allErrs = append(allErrs, fmt.Errorf("spec.rolloutStrategy.partition: percentage must be between 0%% and 100%%"))
	} else {
		if partitionPercent {
			partition = partition * replicas / 100
		}
		if partition < 0 || partition > replicas {
			allErrs = append(allErrs, fmt.Errorf("spec.rolloutStrategy.partition: must be between 0 and replicas (%d)", replicas))
		}
	}
	return utilerrors.NewAggregate(allErrs)
}

func parseGroupSetIntOrPercent(value *intstr.IntOrString, defaultValue int64) (int64, bool, error) {
	if value == nil {
		return defaultValue, false, nil
	}
	switch value.Type {
	case intstr.Int:
		if value.IntVal >= 0 {
			return int64(value.IntVal), false, nil
		}
	case intstr.String:
		if strings.HasSuffix(value.StrVal, "%") {
			digits := strings.TrimSuffix(value.StrVal, "%")
			if len(digits) == 0 {
				break
			}
			for _, digit := range digits {
				if digit < '0' || digit > '9' {
					return 0, false, fmt.Errorf("invalid value %q: must be a non-negative integer or percentage", value.String())
				}
			}
			percentage, err := strconv.ParseInt(digits, 10, 64)
			if err != nil {
				return 0, false, fmt.Errorf("invalid percentage %q: %w", value.StrVal, err)
			}
			return percentage, true, nil
		}
	}
	return 0, false, fmt.Errorf("invalid value %q: must be a non-negative integer or percentage", value.String())
}

func validateGroupSetScalingAdapters(oldRoles, newRoles []RoleSpec) error {
	oldEnabled := make(map[string]bool, len(oldRoles))
	for _, role := range oldRoles {
		oldEnabled[role.Name] = role.ScalingAdapter != nil && role.ScalingAdapter.Enable
	}

	var allErrs []error
	for i, role := range newRoles {
		if role.ScalingAdapter != nil && role.ScalingAdapter.Enable && !oldEnabled[role.Name] {
			allErrs = append(allErrs, fmt.Errorf("spec.groupTemplate.spec.roles[%d].scalingAdapter.enable (role %q): cannot enable scaling adapter in a RoleBasedGroupSet", i, role.Name))
		}
	}
	return utilerrors.NewAggregate(allErrs)
}

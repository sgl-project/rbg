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

	return nil, utilerrors.NewAggregate(v.validateSpec(rbgs))
}

// ValidateUpdate validates a RoleBasedGroupSet on update.
func (v *RoleBasedGroupSetValidator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	rbgs, ok := newObj.(*RoleBasedGroupSet)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroupSet but got %T", newObj)
	}
	klog.V(4).InfoS("validating RoleBasedGroupSet on update", "name", rbgs.Name, "namespace", rbgs.Namespace)

	return nil, utilerrors.NewAggregate(v.validateSpec(rbgs))
}

// groupSetRolesFieldPath is the spec path of the role list carried by the group template,
// shared by every role level validation so the reported field paths stay consistent.
const groupSetRolesFieldPath = "spec.groupTemplate.spec.roles"

// validateSpec runs the validations that apply identically on create and on update.
func (v *RoleBasedGroupSetValidator) validateSpec(rbgs *RoleBasedGroupSet) []error {
	roles := rbgs.Spec.GroupTemplate.Spec.Roles

	var allErrs []error
	if err := validateRoleDependencies("spec.groupTemplate.spec.roles", rbgs.Spec.GroupTemplate.Spec.Roles); err != nil {
		allErrs = append(allErrs, err)
	}
	if !v.EnableDeprecatedWorkloadTypes {
		if err := validateNoDeprecatedWorkloadTypes(groupSetRolesFieldPath, roles); err != nil {
			allErrs = append(allErrs, err)
		}
	}
	if err := validateNoRoleScalingAdapter(groupSetRolesFieldPath, roles); err != nil {
		allErrs = append(allErrs, err)
	}
	return append(allErrs, validateGroupSetRollout(rbgs)...)
}

// scalingAdapterInGroupSetHint explains why enabling a ScalingAdapter from a group template
// is rejected. It is appended once per object rather than once per offending role.
const scalingAdapterInGroupSetHint = "a RoleBasedGroupSet owns spec.roles of every child RoleBasedGroup and keeps it " +
	"consistent with spec.groupTemplate; suggest deploying a standalone RoleBasedGroup that needs role-level autoscaling"

// validateNoRoleScalingAdapter rejects roles that enable a ScalingAdapter inside a group
// template. The RoleBasedGroupSet controller propagates the whole role list to its children,
// which conflicts with the ScalingAdapter controller writing role replicas back into a child
// RoleBasedGroup: the two keep overwriting each other and the RoleBasedGroup validator rejects
// the propagation, so the set never converges. Autoscaling a role therefore requires a
// standalone RoleBasedGroup.
func validateNoRoleScalingAdapter(fieldPath string, roles []RoleSpec) error {
	var allErrs []error
	for i := range roles {
		role := &roles[i]
		if role.ScalingAdapter == nil || !role.ScalingAdapter.Enable {
			continue
		}
		allErrs = append(allErrs, fmt.Errorf(
			"%s[%d] (role %q): scalingAdapter.enable is not supported in a RoleBasedGroupSet groupTemplate",
			fieldPath, i, role.Name,
		))
	}
	if len(allErrs) == 0 {
		return nil
	}
	return fmt.Errorf("%w; %s", utilerrors.NewAggregate(allErrs), scalingAdapterInGroupSetHint)
}

// ValidateDelete just implements admission.CustomValidator. This verb is currently no-op.
func (v *RoleBasedGroupSetValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// validateGroupSetRollout validates spec.rolloutStrategy against spec.replicas:
//   - maxSurge and maxUnavailable cannot both resolve to 0, which would block any progress.
//   - partition must not be greater than replicas. Unlike the role level rule, partition
//     equal to replicas is allowed: it freezes every existing RoleBasedGroup so that
//     maxSurge can hold a standing canary.
//
// maxUnavailable follows the RoleInstanceSet rounding rule, rounding up when maxSurge is 0
// and down otherwise, so that a resolved 0 is only reachable together with a real surge
// budget. Flooring that 0 to 1 is left to the controller, as ValidateRollingUpdate does.
func validateGroupSetRollout(rbgs *RoleBasedGroupSet) []error {
	if rbgs.Spec.RolloutStrategy == nil {
		return nil
	}
	// CRD defaulting runs before validation, so Replicas is normally set here.
	if rbgs.Spec.Replicas == nil || *rbgs.Spec.Replicas == 0 {
		return nil
	}
	replicas := *rbgs.Spec.Replicas
	ru := rbgs.Spec.RolloutStrategy

	var errs []error
	maxSurge, surgeErr := scaledIntOrPercent(ru.MaxSurge, replicas, true, 0)
	if surgeErr != nil {
		errs = append(errs, fmt.Errorf(
			"spec.rolloutStrategy.maxSurge: invalid value %q", ru.MaxSurge.String(),
		))
	}

	maxUnavailable, unavailErr := scaledIntOrPercent(ru.MaxUnavailable, replicas, maxSurge == 0, 1)
	if unavailErr != nil {
		errs = append(errs, fmt.Errorf(
			"spec.rolloutStrategy.maxUnavailable: invalid value %q", ru.MaxUnavailable.String(),
		))
	}

	if surgeErr == nil && unavailErr == nil && maxSurge == 0 && maxUnavailable == 0 {
		errs = append(errs, fmt.Errorf(
			"spec.rolloutStrategy: maxSurge and maxUnavailable cannot both be 0",
		))
	}

	if ru.Partition != nil {
		partition, err := intstr.GetScaledValueFromIntOrPercent(ru.Partition, int(replicas), false)
		if err != nil {
			errs = append(errs, fmt.Errorf(
				"spec.rolloutStrategy.partition: invalid value %q", ru.Partition.String(),
			))
		} else if partition > int(replicas) {
			errs = append(errs, fmt.Errorf(
				"spec.rolloutStrategy.partition: %d must not be greater than replicas %d",
				partition, replicas,
			))
		}
	}

	return errs
}

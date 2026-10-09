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
	if err := validateRoleDependencies("spec.groupTemplate.spec.roles", rbgs.Spec.GroupTemplate.Spec.Roles); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := validateRoleTopologyConstraints("spec.groupTemplate.spec.roles", rbgs.Spec.GroupTemplate.Spec.Roles); err != nil {
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
	if err := validateRoleDependencies("spec.groupTemplate.spec.roles", rbgs.Spec.GroupTemplate.Spec.Roles); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := validateRoleTopologyConstraints("spec.groupTemplate.spec.roles", rbgs.Spec.GroupTemplate.Spec.Roles); err != nil {
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
	if err := validateTopologyImmutability(oldRBGS, rbgs); err != nil {
		allErrs = append(allErrs, err)
	}

	return nil, utilerrors.NewAggregate(allErrs)
}

// validateTopologyImmutability prevents an RBGSet template update from changing its
// topology contract. The parent comparison is self-contained, so it needs no
// cross-resource child lookup.
func validateTopologyImmutability(oldRBGS, newRBGS *RoleBasedGroupSet) error {
	oldTopologies := roleTopologyDeclarations(oldRBGS.Spec.GroupTemplate.Spec.Roles)
	newTopologies := roleTopologyDeclarations(newRBGS.Spec.GroupTemplate.Spec.Roles)
	if len(oldTopologies) != len(newTopologies) {
		return fmt.Errorf(
			"spec.groupTemplate.spec.roles topology constraints are immutable for the RoleBasedGroupSet lifecycle; delete and recreate it to add or remove one")
	}
	for roleName, oldConstraint := range oldTopologies {
		newConstraint, exists := newTopologies[roleName]
		if !exists || !TopologyConstraintsEqual(oldConstraint, newConstraint) {
			return fmt.Errorf(
				"spec.groupTemplate.spec.roles[%s].instanceTopologyConstraint is immutable for the RoleBasedGroupSet lifecycle; delete and recreate it to change it",
				roleName,
			)
		}
	}
	return nil
}

// ValidateDelete just implements admission.CustomValidator. This verb is currently no-op.
func (v *RoleBasedGroupSetValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

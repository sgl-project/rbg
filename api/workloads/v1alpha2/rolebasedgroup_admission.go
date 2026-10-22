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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// RoleBasedGroupValidator implements admission.CustomValidator for RoleBasedGroup.
// Wired into the manager by SetupWebhookWithManager.
//
// +kubebuilder:object:generate=false
type RoleBasedGroupValidator struct {
	Client client.Client
	// EnableDeprecatedWorkloadTypes reports whether existing deprecated workload
	// types (Deployment, StatefulSet, LeaderWorkerSet) remain supported during
	// migration. New uses are rejected in both modes; when false, existing uses are
	// rejected as well.
	EnableDeprecatedWorkloadTypes bool
}

var _ admission.CustomValidator = &RoleBasedGroupValidator{}

// ValidateCreate validates a RoleBasedGroup on creation.
func (v *RoleBasedGroupValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	rbg, ok := obj.(*RoleBasedGroup)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroup but got %T", obj)
	}
	klog.V(4).InfoS("validating RoleBasedGroup on create", "name", rbg.Name, "namespace", rbg.Namespace)

	var allErrs []error
	if err := ValidateRoleBasedGroupName(rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := ValidateRollingUpdate(rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := ValidateRoleDependencies(rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if v.EnableDeprecatedWorkloadTypes {
		if err := validateNoNewDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles, nil); err != nil {
			allErrs = append(allErrs, err)
		}
	} else {
		if err := validateNoDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	return nil, utilerrors.NewAggregate(allErrs)
}

// ValidateUpdate validates a RoleBasedGroup on update.
func (v *RoleBasedGroupValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldRBG, ok := oldObj.(*RoleBasedGroup)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroup but got %T", oldObj)
	}
	rbg, ok := newObj.(*RoleBasedGroup)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroup but got %T", newObj)
	}
	klog.V(4).InfoS("validating RoleBasedGroup on update", "name", rbg.Name, "namespace", rbg.Namespace)

	var allErrs []error
	if err := ValidateRollingUpdate(rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := ValidateRoleDependencies(rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := ValidateScalingAdapterReplicas(ctx, v.Client, oldRBG, rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if v.EnableDeprecatedWorkloadTypes {
		if err := validateNoNewDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles, oldRBG.Spec.Roles); err != nil {
			allErrs = append(allErrs, err)
		}
	} else {
		if err := validateNoDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	return nil, utilerrors.NewAggregate(allErrs)
}

// ValidateDelete just implements admission.CustomValidator. This verb is currently no-op.
func (v *RoleBasedGroupValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

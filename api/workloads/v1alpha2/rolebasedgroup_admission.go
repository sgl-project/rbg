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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
func (v *RoleBasedGroupValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
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
		// A RoleBasedGroupSet controller replaces deleted children by re-creating
		// them from its own template. Those children are not new user-facing uses:
		// the deprecated workload type already exists in the set's template, so the
		// replacement inherits it. Look up the verified parent template and use it
		// as the pre-existing role set, without exempting unrelated creates.
		oldRoles, err := v.legacySetTemplateRoles(ctx, rbg)
		if err != nil {
			allErrs = append(allErrs, err)
		} else if err := validateNoNewDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles, oldRoles); err != nil {
			allErrs = append(allErrs, err)
		}
	} else {
		if err := validateNoDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	return nil, utilerrors.NewAggregate(allErrs)
}

// legacySetTemplateRoles returns the role list from the owning
// RoleBasedGroupSet's group template when the object being created carries a
// deprecated workload type and is controlled by that set. It returns nil for
// every other create, so unrelated deprecated uses stay rejected.
//
// The exemption is owner-reference aware but does not trust the owner reference
// alone: the parent is fetched by name, its UID must match the reference, and
// only template roles whose name and workload type match a deprecated role in
// the new child are returned. A forged or stale owner reference therefore grants
// nothing.
func (v *RoleBasedGroupValidator) legacySetTemplateRoles(
	ctx context.Context, rbg *RoleBasedGroup,
) ([]RoleSpec, error) {
	if v.Client == nil || !containsDeprecatedWorkloadType(rbg.Spec.Roles) {
		return nil, nil
	}

	owner := metav1.GetControllerOfNoCopy(rbg)
	if owner == nil || owner.Kind != "RoleBasedGroupSet" || owner.APIVersion != GroupVersion.String() {
		return nil, nil
	}

	parent := &RoleBasedGroupSet{}
	if err := v.Client.Get(ctx, types.NamespacedName{Namespace: rbg.Namespace, Name: owner.Name}, parent); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("unable to verify RoleBasedGroupSet owner %s/%s: %w", rbg.Namespace, owner.Name, err)
	}
	if parent.UID != owner.UID {
		return nil, nil
	}

	templateRoles := parent.Spec.GroupTemplate.Spec.Roles
	if len(templateRoles) == 0 {
		return nil, nil
	}

	wanted := make(map[string]string, len(rbg.Spec.Roles))
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		wt := role.GetWorkloadType()
		if isDeprecatedWorkloadType(wt) {
			wanted[role.Name] = wt
		}
	}

	exempt := make([]RoleSpec, 0, len(templateRoles))
	for i := range templateRoles {
		role := &templateRoles[i]
		if wt, ok := wanted[role.Name]; ok && wt == role.GetWorkloadType() {
			exempt = append(exempt, *role.DeepCopy())
		}
	}
	if len(exempt) == 0 {
		return nil, nil
	}
	return exempt, nil
}

// containsDeprecatedWorkloadType reports whether any role in the slice uses one
// of the deprecated workload types. It gates the parent lookup so ordinary
// creates never pay for an extra read.
func containsDeprecatedWorkloadType(roles []RoleSpec) bool {
	for i := range roles {
		if isDeprecatedWorkloadType(roles[i].GetWorkloadType()) {
			return true
		}
	}
	return false
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

package v1alpha1

import (
	"fmt"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
)

// ValidateRoleTemplates validates roleTemplates array for uniqueness and completeness.
func ValidateRoleTemplates(rbg *RoleBasedGroup) error {
	templateNames := make(map[string]bool)
	var allErrs []error

	for i, rt := range rbg.Spec.RoleTemplates {
		// Validate DNS label format first
		if errs := validation.IsDNS1123Label(rt.Name); len(errs) > 0 {
			allErrs = append(allErrs, fmt.Errorf(
				"spec.roleTemplates[%d].name: %q is not a valid DNS label: %s",
				i, rt.Name, errs[0],
			))
		}

		// Check for duplicate names
		if templateNames[rt.Name] {
			allErrs = append(allErrs, fmt.Errorf(
				"spec.roleTemplates[%d]: duplicate template name %q",
				i, rt.Name,
			))
		}
		templateNames[rt.Name] = true

		// Validate template has at least one container
		if len(rt.Template.Spec.Containers) == 0 {
			allErrs = append(allErrs, fmt.Errorf(
				"spec.roleTemplates[%d].template.spec.containers: must have at least one container",
				i,
			))
		}
	}

	return utilerrors.NewAggregate(allErrs)
}

// ValidateRoleTemplateReferences validates template references in roles.
func ValidateRoleTemplateReferences(rbg *RoleBasedGroup) error {
	templateNames := make(map[string]bool)
	for _, rt := range rbg.Spec.RoleTemplates {
		templateNames[rt.Name] = true
	}

	var allErrs []error
	for i := range rbg.Spec.Roles {
		if err := validateRoleTemplateFields(i, &rbg.Spec.Roles[i], templateNames); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	return utilerrors.NewAggregate(allErrs)
}

// validateRoleTemplateFields validates template-related fields at the controller layer.
// Note: Some structural rules are also enforced by CRD XValidation. We keep a subset of checks here as
// a defense-in-depth fallback for clusters where CRDs may not yet be updated.
func validateRoleTemplateFields(
	index int,
	role *RoleSpec,
	validTemplateNames map[string]bool,
) error {
	hasTemplateRef := role.TemplateSource.TemplateRef != nil
	hasTemplatePatch := len(role.TemplatePatch.Raw) > 0
	hasTemplate := role.TemplateSource.Template != nil

	if hasTemplateRef {
		// Defense-in-depth: CRD validates this, but controller validates as well.
		if hasTemplate {
			return fmt.Errorf(
				"spec.roles[%d]: templateRef and template are mutually exclusive, only one can be set",
				index,
			)
		}

		// Defense-in-depth: CRD validates this, but controller validates as well.
		if role.Workload.Kind == "InstanceSet" {
			return fmt.Errorf(
				"spec.roles[%d].templateRef: not supported for InstanceSet workloads",
				index,
			)
		}

		// LWS workload does not support templateRef to avoid three-layer merge complexity.
		// Users should use template + patchLeaderTemplate/patchWorkerTemplate instead.
		if role.Workload.Kind == "LeaderWorkerSet" {
			return fmt.Errorf(
				"spec.roles[%d].templateRef: not supported for LeaderWorkerSet workloads (use template with patchLeaderTemplate/patchWorkerTemplate instead)",
				index,
			)
		}

		// Cross-resource check: referenced template must exist.
		if !validTemplateNames[role.TemplateSource.TemplateRef.Name] {
			return fmt.Errorf(
				"spec.roles[%d].templateRef.name: template %q not found in spec.roleTemplates",
				index, role.TemplateSource.TemplateRef.Name,
			)
		}

		// RawExtension cannot be inspected by CEL, so validate in controller.
		if !hasTemplatePatch {
			return fmt.Errorf(
				"spec.roles[%d].templatePatch: required when templateRef is set (if no overrides needed, use empty object: templatePatch: {})",
				index,
			)
		}
		return nil
	}

	// templateRef is not set: templatePatch must not be set.
	if hasTemplatePatch {
		return fmt.Errorf(
			"spec.roles[%d].templatePatch: only valid when templateRef is set",
			index,
		)
	}

	// Defense-in-depth: reconcilers also validate template presence for supported workloads.
	if !hasTemplate && role.Workload.Kind != "InstanceSet" {
		return fmt.Errorf(
			"spec.roles[%d].template: required when templateRef is not set",
			index,
		)
	}

	return nil
}

package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ValidateRoleTemplates validates roleTemplates array for uniqueness and completeness.
func ValidateRoleTemplates(rbg *RoleBasedGroup) error {
	templateNames := make(map[string]bool)

	for i, rt := range rbg.Spec.RoleTemplates {
		if templateNames[rt.Name] {
			return fmt.Errorf(
				"spec.roleTemplates[%d]: duplicate template name %q",
				i, rt.Name,
			)
		}
		templateNames[rt.Name] = true

		if len(rt.Template.Spec.Containers) == 0 {
			return fmt.Errorf(
				"spec.roleTemplates[%d].template.spec.containers: must have at least one container",
				i,
			)
		}

		if errs := validation.IsDNS1123Label(rt.Name); len(errs) > 0 {
			return fmt.Errorf(
				"spec.roleTemplates[%d].name: %q is not a valid DNS label: %s",
				i, rt.Name, errs[0],
			)
		}
	}

	return nil
}

// ValidateRoleTemplateReferences validates template references in roles.
func ValidateRoleTemplateReferences(rbg *RoleBasedGroup) error {
	templateNames := make(map[string]bool)
	for _, rt := range rbg.Spec.RoleTemplates {
		templateNames[rt.Name] = true
	}

	for i := range rbg.Spec.Roles {
		if err := validateRoleTemplateFields(i, &rbg.Spec.Roles[i], templateNames); err != nil {
			return err
		}
	}

	return nil
}

// validateRoleTemplateFields validates template field mutual exclusivity.
func validateRoleTemplateFields(
	index int,
	role *RoleSpec,
	validTemplateNames map[string]bool,
) error {
	hasTemplateRef := role.TemplateRef != nil
	hasTemplate := role.Template != nil
	hasTemplatePatch := len(role.TemplatePatch.Raw) > 0

	// Strict mutual exclusivity: templateRef and template cannot both be set
	if hasTemplateRef && hasTemplate {
		return fmt.Errorf(
			"spec.roles[%d]: templateRef and template are mutually exclusive, only one can be set",
			index,
		)
	}

	if hasTemplateRef {
		// TemplateRef mode: use referenced template with patch
		if !validTemplateNames[role.TemplateRef.Name] {
			return fmt.Errorf(
				"spec.roles[%d].templateRef.name: template %q not found in spec.roleTemplates",
				index, role.TemplateRef.Name,
			)
		}

		if !hasTemplatePatch {
			return fmt.Errorf(
				"spec.roles[%d].templatePatch: required when templateRef is set (if no overrides needed, use empty object: templatePatch: {})",
				index,
			)
		}
	} else {
		// Traditional mode: template field is required
		if hasTemplatePatch {
			return fmt.Errorf(
				"spec.roles[%d].templatePatch: only valid when templateRef is set",
				index,
			)
		}

		if !hasTemplate {
			return fmt.Errorf(
				"spec.roles[%d].template: required when templateRef is not set",
				index,
			)
		}
	}

	return nil
}


package v1alpha1

import (
	"fmt"
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

		if !isDNSLabel(rt.Name) {
			return fmt.Errorf(
				"spec.roleTemplates[%d].name: %q is not a valid DNS label (must be lowercase alphanumeric, may contain hyphens, cannot start or end with hyphen)",
				i, rt.Name,
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

	for i, role := range rbg.Spec.Roles {
		if err := validateRoleTemplateFields(i, &role, templateNames); err != nil {
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
	hasTemplatePatch := role.TemplatePatch.Raw != nil && len(role.TemplatePatch.Raw) > 0

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

// isDNSLabel validates if string is a valid DNS label (RFC 1123).
func isDNSLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}

	for i, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}

		if i == 0 && c == '-' {
			return false
		}

		if i == len(s)-1 && c == '-' {
			return false
		}
	}

	return true
}

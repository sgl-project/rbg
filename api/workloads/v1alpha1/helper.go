package v1alpha1

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

func (rbg *RoleBasedGroup) GetCommonLabelsFromRole(role *RoleSpec) map[string]string {
	// Be careful to change these labels.
	// They are used as sts.spec.selector which can not be updated. If changed, may cause all exist rbgs failed.
	return map[string]string{
		SetNameLabelKey:            rbg.Name,
		SetRoleLabelKey:            role.Name,
		SetGroupUniqueHashLabelKey: rbg.GenGroupUniqueKey(),
	}
}

func (rbg *RoleBasedGroup) GetCommonAnnotationsFromRole(role *RoleSpec) map[string]string {
	return map[string]string{
		RoleSizeAnnotationKey: fmt.Sprintf("%d", *role.Replicas),
	}
}

func (rbg *RoleBasedGroup) GetGroupSize() int {
	ret := 0
	for _, role := range rbg.Spec.Roles {
		if role.Workload.String() == LeaderWorkerSetWorkloadType {
			if role.LeaderWorkerSet == nil {
				ret += 1 * int(*role.Replicas)
				continue
			}
			ret += int(*role.LeaderWorkerSet.Size) * int(*role.Replicas)
		} else {
			ret += int(*role.Replicas)
		}
	}
	return ret
}

func (rbg *RoleBasedGroup) GetWorkloadName(role *RoleSpec) string {
	if rbg == nil {
		return ""
	}

	workloadName := fmt.Sprintf("%s-%s", rbg.Name, role.Name)

	// Kubernetes name length is limited to 63 characters
	if len(workloadName) > 63 {
		workloadName = workloadName[:63]
		workloadName = strings.TrimRight(workloadName, "-")
	}
	return workloadName
}

// GetServiceName Because ServiceName needs to follow DNS naming conventions,
// which do not allow names to start with a number. Therefore, the s- prefix
// is added to the service name to meet this requirement.
func (rbg *RoleBasedGroup) GetServiceName(role *RoleSpec) string {
	svcName := fmt.Sprintf("s-%s-%s", rbg.Name, role.Name)
	if len(svcName) > 63 {
		svcName = svcName[:63]
		// After truncation, trim trailing hyphens (and ensure the name ends with an alphanumeric)
		// to maintain DNS-1123/DNS-1035 validity.
		svcName = strings.TrimRight(svcName, "-")
	}
	return svcName
}

func (rbg *RoleBasedGroup) GetRole(roleName string) (*RoleSpec, error) {
	if roleName == "" {
		return nil, errors.New("roleName cannot be empty")
	}

	for i := range rbg.Spec.Roles {
		if rbg.Spec.Roles[i].Name == roleName {
			return &rbg.Spec.Roles[i], nil
		}
	}
	return nil, fmt.Errorf("role %q not found", roleName)
}

func (rbg *RoleBasedGroup) GetRoleStatus(roleName string) (status RoleStatus, found bool) {
	if roleName == "" {
		return
	}

	for i := range rbg.Status.RoleStatuses {
		if rbg.Status.RoleStatuses[i].Name == roleName {
			status = rbg.Status.RoleStatuses[i]
			found = true
			break
		}
	}
	return
}

func (rbg *RoleBasedGroup) GetExclusiveKey() (topologyKey string, found bool) {
	topologyKey, found = rbg.Annotations[ExclusiveKeyAnnotationKey]
	return
}

func (rbg *RoleBasedGroup) GenGroupUniqueKey() string {
	return sha1Hash(fmt.Sprintf("%s/%s", rbg.GetNamespace(), rbg.GetName()))
}

// sha1Hash accepts an input string and returns the 40 character SHA1 hash digest of the input string.
func sha1Hash(s string) string {
	h := sha1.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

func (rbg *RoleBasedGroup) EnableGangScheduling() bool {
	if rbg.IsKubeGangScheduling() || rbg.IsVolcanoGangScheduling() {
		return true
	}
	return false
}

func (rbg *RoleBasedGroup) IsVolcanoGangScheduling() bool {
	if rbg.Spec.PodGroupPolicy != nil && rbg.Spec.PodGroupPolicy.PodGroupPolicySource.VolcanoScheduling != nil {
		return true
	}
	return false
}

func (rbg *RoleBasedGroup) IsKubeGangScheduling() bool {
	if rbg.Spec.PodGroupPolicy != nil && rbg.Spec.PodGroupPolicy.PodGroupPolicySource.KubeScheduling != nil {
		return true
	}
	return false
}

func (rbgsa *RoleBasedGroupScalingAdapter) ContainsRBGOwner(rbg *RoleBasedGroup) bool {
	for _, owner := range rbgsa.OwnerReferences {
		if owner.UID == rbg.UID {
			return true
		}
	}
	return false
}

func (p *PodGroupPolicy) EnableGangScheduling() bool {
	return p.IsKubeGangScheduling() || p.IsVolcanoGangScheduling()
}

func (p *PodGroupPolicy) IsVolcanoGangScheduling() bool {
	return p != nil && p.PodGroupPolicySource.VolcanoScheduling != nil
}

func (p *PodGroupPolicy) IsKubeGangScheduling() bool {
	return p != nil && p.PodGroupPolicySource.KubeScheduling != nil
}

func (instance *Instance) GetInstancePattern() InstancePatternType {
	return InstancePatternType(instance.Labels[RBGInstancePatternLabelKey])
}

func (instance *Instance) GetRoleTemplateType() RBGRoleTemplateType {
	return RBGRoleTemplateType(instance.Labels[RBGRoleTemplateTypeLabelKey])
}

// FindRoleTemplate finds a RoleTemplate by name in the RoleBasedGroup's spec.
// Returns the template if found, or an error if not found.
func (rbg *RoleBasedGroup) FindRoleTemplate(name string) (*RoleTemplate, error) {
	if name == "" {
		return nil, errors.New("template name cannot be empty")
	}

	for i := range rbg.Spec.RoleTemplates {
		if rbg.Spec.RoleTemplates[i].Name == name {
			return &rbg.Spec.RoleTemplates[i], nil
		}
	}
	return nil, fmt.Errorf("roleTemplate %q not found in spec.roleTemplates", name)
}

// UsesRoleTemplate returns true if the role uses a RoleTemplate (has templateRef set).
func (r *RoleSpec) UsesRoleTemplate() bool {
	return r.TemplateRef != nil
}

// GetEffectiveTemplateName returns the name of the template this role uses.
// Returns empty string if the role doesn't use a template.
func (r *RoleSpec) GetEffectiveTemplateName() string {
	if r.TemplateRef != nil {
		return r.TemplateRef.Name
	}
	return ""
}

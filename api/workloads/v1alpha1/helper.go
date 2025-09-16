package v1alpha1

import (
	"errors"
	"fmt"
)

func (rbg *RoleBasedGroup) GetCommonLabelsFromRole(role *RoleSpec) map[string]string {
	// Be careful to change these labels.
	// They are used as sts.spec.selector which can not be updated. If changed, may cause all exist rbgs failed.
	return map[string]string{
		SetNameLabelKey: rbg.Name,
		SetRoleLabelKey: role.Name,
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
			ret += int(*role.LeaderWorkerSet.Size) * int(*role.Replicas)
		} else {
			ret += int(*role.Replicas)
		}
	}
	return ret
}

func (rbg *RoleBasedGroup) GetWorkloadName(role *RoleSpec) string {
	return fmt.Sprintf("%s-%s", rbg.Name, role.Name)
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

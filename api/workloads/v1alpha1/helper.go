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

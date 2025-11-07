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

func (p *PodGroupPolicy) EnableGangScheduling() bool {
	return p.IsKubeGangScheduling() || p.IsVolcanoGangScheduling()
}

func (p *PodGroupPolicy) IsVolcanoGangScheduling() bool {
	return p != nil && p.PodGroupPolicySource.VolcanoScheduling != nil
}

func (p *PodGroupPolicy) IsKubeGangScheduling() bool {
	return p != nil && p.PodGroupPolicySource.KubeScheduling != nil
}

// GetCoordination returns the coordination with the given name.
func (rbg *RoleBasedGroup) GetCoordination(name string) (*Coordination, error) {
	if name == "" {
		return nil, errors.New("coordination name cannot be empty")
	}

	for i := range rbg.Spec.Coordination {
		if rbg.Spec.Coordination[i].Name == name {
			return &rbg.Spec.Coordination[i], nil
		}
	}
	return nil, fmt.Errorf("coordination %q not found", name)
}

// ValidateCoordination validates the coordination configuration.
func (c *Coordination) ValidateCoordination(rbg *RoleBasedGroup) error {
	// Validate that all roles in coordination exist
	for _, roleName := range c.Roles {
		if _, err := rbg.GetRole(roleName); err != nil {
			return fmt.Errorf("coordination %q references non-existent role %q: %w", c.Name, roleName, err)
		}
	}

	// Validate strategy based on type
	switch c.Type {
	case RollingUpdateCoordination:
		if c.Strategy.RollingUpdate == nil {
			return fmt.Errorf("coordination %q of type RollingUpdate must have rollingUpdate strategy defined", c.Name)
		}
		// Additional validation for rolling update strategy can be added here
	case AffinitySchedulingCoordination:
		if c.Strategy.AffinityScheduling == nil {
			return fmt.Errorf("coordination %q of type AffinityScheduling must have affinityScheduling strategy defined", c.Name)
		}
		if err := c.Strategy.AffinityScheduling.Validate(rbg, c.Roles); err != nil {
			return fmt.Errorf("invalid affinityScheduling strategy for coordination %q: %w", c.Name, err)
		}
	default:
		return fmt.Errorf("coordination %q has unsupported type %q", c.Name, c.Type)
	}

	return nil
}

// Validate validates the AffinitySchedulingStrategy.
func (a *AffinitySchedulingStrategy) Validate(rbg *RoleBasedGroup, coordinatedRoles []string) error {
	if a.TopologyKey == "" {
		return errors.New("topologyKey cannot be empty")
	}

	if len(a.Ratios) == 0 {
		return errors.New("ratios cannot be empty")
	}

	// Ensure all coordinated roles have ratios defined
	roleSet := make(map[string]bool)
	for _, roleName := range coordinatedRoles {
		roleSet[roleName] = true
	}

	for roleName := range a.Ratios {
		if !roleSet[roleName] {
			return fmt.Errorf("ratio defined for role %q which is not in coordinated roles", roleName)
		}
		delete(roleSet, roleName)
	}

	if len(roleSet) > 0 {
		var missing []string
		for roleName := range roleSet {
			missing = append(missing, roleName)
		}
		return fmt.Errorf("missing ratios for coordinated roles: %v", missing)
	}

	// Validate that ratios are positive
	for roleName, ratio := range a.Ratios {
		if ratio <= 0 {
			return fmt.Errorf("ratio for role %q must be positive, got %d", roleName, ratio)
		}
	}

	// Validate that the total replicas for each role is divisible by its ratio
	for roleName, ratio := range a.Ratios {
		role, err := rbg.GetRole(roleName)
		if err != nil {
			return err
		}
		if *role.Replicas%ratio != 0 {
			return fmt.Errorf("role %q has %d replicas which is not divisible by ratio %d", roleName, *role.Replicas, ratio)
		}
	}

	return nil
}

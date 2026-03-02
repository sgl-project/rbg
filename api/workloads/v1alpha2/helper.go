/*
Copyright 2025.

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
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// GetCommonLabelsFromRole returns common labels for a role.
func (rbg *RoleBasedGroup) GetCommonLabelsFromRole(role *RoleSpec) map[string]string {
	// Be careful to change these labels.
	// They are used as sts.spec.selector which can not be updated. If changed, may cause all exist rbgs failed.
	return map[string]string{
		SetNameLabelKey:            rbg.Name,
		SetRoleLabelKey:            role.Name,
		SetGroupUniqueHashLabelKey: rbg.GenGroupUniqueKey(),
	}
}

// GetCommonAnnotationsFromRole returns common annotations for a role.
func (rbg *RoleBasedGroup) GetCommonAnnotationsFromRole(role *RoleSpec) map[string]string {
	return map[string]string{
		RoleSizeAnnotationKey: fmt.Sprintf("%d", *role.Replicas),
	}
}

// GetGroupSize returns the total number of pods in the group.
func (rbg *RoleBasedGroup) GetGroupSize() int {
	ret := 0
	for _, role := range rbg.Spec.Roles {
		if role.IsLeaderWorkerPattern() {
			lwp := role.GetLeaderWorkerPattern()
			if lwp == nil || lwp.Size == nil {
				ret += 1 * int(*role.Replicas)
				continue
			}
			ret += int(*lwp.Size) * int(*role.Replicas)
		} else {
			ret += int(*role.Replicas)
		}
	}
	return ret
}

// GetWorkloadName returns the workload name for a role.
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

// GetServiceName returns the service name for a role.
// Because ServiceName needs to follow DNS naming conventions,
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

// GetRole returns the RoleSpec for a given role name.
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

// GetRoleStatus returns the RoleStatus for a given role name.
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

// GetExclusiveKey returns the exclusive key from annotations.
func (rbg *RoleBasedGroup) GetExclusiveKey() (topologyKey string, found bool) {
	topologyKey, found = rbg.Annotations[ExclusiveKeyAnnotationKey]
	return
}

// GenGroupUniqueKey generates a unique key for the group.
func (rbg *RoleBasedGroup) GenGroupUniqueKey() string {
	return sha1Hash(fmt.Sprintf("%s/%s", rbg.GetNamespace(), rbg.GetName()))
}

// sha1Hash accepts an input string and returns the 40 character SHA1 hash digest of the input string.
func sha1Hash(s string) string {
	h := sha1.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// EnableGangScheduling returns true if gang scheduling is enabled.
func (rbg *RoleBasedGroup) EnableGangScheduling() bool {
	return rbg.IsKubeGangScheduling() || rbg.IsVolcanoGangScheduling()
}

// IsVolcanoGangScheduling returns true if volcano gang scheduling is enabled.
func (rbg *RoleBasedGroup) IsVolcanoGangScheduling() bool {
	return rbg.Spec.PodGroupPolicy != nil && rbg.Spec.PodGroupPolicy.PodGroupPolicySource.VolcanoScheduling != nil
}

// IsKubeGangScheduling returns true if kube gang scheduling is enabled.
func (rbg *RoleBasedGroup) IsKubeGangScheduling() bool {
	return rbg.Spec.PodGroupPolicy != nil && rbg.Spec.PodGroupPolicy.PodGroupPolicySource.KubeScheduling != nil
}

// EnableGangScheduling returns true if gang scheduling is enabled.
func (p *PodGroupPolicy) EnableGangScheduling() bool {
	return p.IsKubeGangScheduling() || p.IsVolcanoGangScheduling()
}

// IsVolcanoGangScheduling returns true if volcano gang scheduling is enabled.
func (p *PodGroupPolicy) IsVolcanoGangScheduling() bool {
	return p != nil && p.PodGroupPolicySource.VolcanoScheduling != nil
}

// IsKubeGangScheduling returns true if kube gang scheduling is enabled.
func (p *PodGroupPolicy) IsKubeGangScheduling() bool {
	return p != nil && p.PodGroupPolicySource.KubeScheduling != nil
}

// FindRoleTemplate finds a RoleTemplate by name in the RoleBasedGroup's spec.
// Returns a deep copy of the template if found, or an error if not found.
func (rbg *RoleBasedGroup) FindRoleTemplate(name string) (*RoleTemplate, error) {
	if name == "" {
		return nil, errors.New("template name cannot be empty")
	}

	for i := range rbg.Spec.RoleTemplates {
		if rbg.Spec.RoleTemplates[i].Name == name {
			return rbg.Spec.RoleTemplates[i].DeepCopy(), nil
		}
	}
	return nil, fmt.Errorf("roleTemplate %q not found in spec.roleTemplates", name)
}

// GetKey returns the namespaced name key for the RoleBasedGroup.
func (rbg *RoleBasedGroup) GetKey() string {
	return fmt.Sprintf("%s/%s", rbg.Namespace, rbg.Name)
}

// ========== RoleSpec Pattern Helper Methods ==========

// IsStandalonePattern returns true if the role uses the standalone pattern.
func (r *RoleSpec) IsStandalonePattern() bool {
	return r.StandalonePattern != nil
}

// IsLeaderWorkerPattern returns true if the role uses the leader-worker pattern.
func (r *RoleSpec) IsLeaderWorkerPattern() bool {
	return r.LeaderWorkerPattern != nil
}

// GetStandalonePattern returns the StandalonePattern if set, nil otherwise.
func (r *RoleSpec) GetStandalonePattern() *StandalonePattern {
	return r.StandalonePattern
}

// GetLeaderWorkerPattern returns the LeaderWorkerPattern if set, nil otherwise.
func (r *RoleSpec) GetLeaderWorkerPattern() *LeaderWorkerPattern {
	return r.LeaderWorkerPattern
}

// GetTemplate returns the PodTemplateSpec for this role.
// It checks both StandalonePattern and LeaderWorkerPattern.
func (r *RoleSpec) GetTemplate() *corev1.PodTemplateSpec {
	if r.StandalonePattern != nil {
		return r.StandalonePattern.TemplateSource.Template
	}
	if r.LeaderWorkerPattern != nil {
		return r.LeaderWorkerPattern.TemplateSource.Template
	}
	return nil
}

// GetTemplateRef returns the TemplateRef for this role.
// It checks both StandalonePattern and LeaderWorkerPattern.
func (r *RoleSpec) GetTemplateRef() *TemplateRef {
	if r.StandalonePattern != nil {
		return r.StandalonePattern.TemplateSource.TemplateRef
	}
	if r.LeaderWorkerPattern != nil {
		return r.LeaderWorkerPattern.TemplateSource.TemplateRef
	}
	return nil
}

// UsesRoleTemplate returns true if the role uses a RoleTemplate (has templateRef set).
func (r *RoleSpec) UsesRoleTemplate() bool {
	return r.GetTemplateRef() != nil
}

// GetEffectiveTemplateName returns the name of the template this role uses.
// Returns empty string if the role doesn't use a template.
func (r *RoleSpec) GetEffectiveTemplateName() string {
	if ref := r.GetTemplateRef(); ref != nil {
		return ref.Name
	}
	return ""
}

// GetTemplatePatch returns the template patch for this role.
// It checks TemplateRef.Patch in both StandalonePattern and LeaderWorkerPattern.
func (r *RoleSpec) GetTemplatePatch() *runtime.RawExtension {
	if r.StandalonePattern != nil && r.StandalonePattern.TemplateSource.TemplateRef != nil {
		return r.StandalonePattern.TemplateSource.TemplateRef.Patch
	}
	if r.LeaderWorkerPattern != nil && r.LeaderWorkerPattern.TemplateSource.TemplateRef != nil {
		return r.LeaderWorkerPattern.TemplateSource.TemplateRef.Patch
	}
	return nil
}

// GetLeaderTemplatePatch returns the leader template patch if using LeaderWorkerPattern.
func (r *RoleSpec) GetLeaderTemplatePatch() *runtime.RawExtension {
	if r.LeaderWorkerPattern == nil {
		return nil
	}
	return r.LeaderWorkerPattern.LeaderTemplatePatch
}

// GetWorkerTemplatePatch returns the worker template patch if using LeaderWorkerPattern.
func (r *RoleSpec) GetWorkerTemplatePatch() *runtime.RawExtension {
	if r.LeaderWorkerPattern == nil {
		return nil
	}
	return r.LeaderWorkerPattern.WorkerTemplatePatch
}

// GetLeaderWorkerSize returns the size of the leader-worker group.
// Returns nil if not using LeaderWorkerPattern.
func (r *RoleSpec) GetLeaderWorkerSize() *int32 {
	if r.LeaderWorkerPattern == nil {
		return nil
	}
	return r.LeaderWorkerPattern.Size
}

// HasTemplate returns true if the role has either an inline template or a template reference.
func (r *RoleSpec) HasTemplate() bool {
	return r.GetTemplate() != nil || r.GetTemplateRef() != nil
}

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

package v1alpha1

import (
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// ConvertTo converts this RoleBasedGroup (v1alpha1) to the Hub version (v1alpha2).
func (src *RoleBasedGroup) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*v1alpha2.RoleBasedGroup)

	// Convert metadata
	dst.ObjectMeta = src.ObjectMeta

	// Convert spec
	dst.Spec = convertSpecToV1alpha2(src.Spec)

	// Convert status
	dst.Status = convertStatusToV1alpha2(src.Status)

	return nil
}

// ConvertFrom converts from the Hub version (v1alpha2) to this RoleBasedGroup (v1alpha1).
func (dst *RoleBasedGroup) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*v1alpha2.RoleBasedGroup)

	// Convert metadata
	dst.ObjectMeta = src.ObjectMeta

	// Convert spec
	dst.Spec = convertSpecFromV1alpha2(src.Spec)

	// Convert status
	dst.Status = convertStatusFromV1alpha2(src.Status)

	return nil
}

// convertSpecToV1alpha2 converts RoleBasedGroupSpec from v1alpha1 to v1alpha2.
func convertSpecToV1alpha2(src RoleBasedGroupSpec) v1alpha2.RoleBasedGroupSpec {
	dst := v1alpha2.RoleBasedGroupSpec{
		RoleTemplates:            convertRoleTemplatesToV1alpha2(src.RoleTemplates),
		PodGroupPolicy:           convertPodGroupPolicyToV1alpha2(src.PodGroupPolicy),
		CoordinationRequirements: convertCoordinationsToV1alpha2(src.CoordinationRequirements),
	}

	// Convert roles
	dst.Roles = make([]v1alpha2.RoleSpec, len(src.Roles))
	for i, role := range src.Roles {
		dst.Roles[i] = convertRoleSpecToV1alpha2(role)
	}

	return dst
}

// convertRoleSpecToV1alpha2 converts RoleSpec from v1alpha1 to v1alpha2.
func convertRoleSpecToV1alpha2(src RoleSpec) v1alpha2.RoleSpec {
	dst := v1alpha2.RoleSpec{
		Name:            src.Name,
		Labels:          src.Labels,
		Annotations:     src.Annotations,
		Replicas:        src.Replicas,
		RolloutStrategy: convertRolloutStrategyToV1alpha2(src.RolloutStrategy),
		RestartPolicy:   v1alpha2.RestartPolicyType(src.RestartPolicy),
		Dependencies:    src.Dependencies,
		Workload: v1alpha2.WorkloadSpec{
			APIVersion: src.Workload.APIVersion,
			Kind:       src.Workload.Kind,
		},
		TemplatePatch:   src.TemplatePatch,
		Components:      convertComponentsToV1alpha2(src.Components),
		ServicePorts:    src.ServicePorts,
		EngineRuntimes:  convertEngineRuntimesToV1alpha2(src.EngineRuntimes),
		ScalingAdapter:  convertScalingAdapterToV1alpha2(src.ScalingAdapter),
		MinReadySeconds: src.MinReadySeconds,
	}

	// Convert template structure to Pattern
	// If LeaderWorkerSet is set, convert to LeaderWorkerPattern
	// Otherwise, convert Template/TemplateRef to StandalonePattern
	if src.LeaderWorkerSet != nil {
		dst.Pattern = &v1alpha2.Pattern{
			LeaderWorkerPattern: &v1alpha2.LeaderWorkerPattern{
				Size: src.LeaderWorkerSet.Size,
				TemplateSource: v1alpha2.TemplateSource{
					Template:    src.TemplateSource.Template,
					TemplateRef: convertTemplateRefToV1alpha2(src.TemplateSource.TemplateRef),
				},
				LeaderTemplatePatch: src.LeaderWorkerSet.PatchLeaderTemplate,
				WorkerTemplatePatch: src.LeaderWorkerSet.PatchWorkerTemplate,
			},
		}
	} else if src.TemplateSource.Template != nil || src.TemplateSource.TemplateRef != nil {
		dst.Pattern = &v1alpha2.Pattern{
			StandalonePattern: &v1alpha2.StandalonePattern{
				TemplateSource: v1alpha2.TemplateSource{
					Template:    src.TemplateSource.Template,
					TemplateRef: convertTemplateRefToV1alpha2(src.TemplateSource.TemplateRef),
				},
			},
		}
	}

	return dst
}

// convertSpecFromV1alpha2 converts RoleBasedGroupSpec from v1alpha2 to v1alpha1.
func convertSpecFromV1alpha2(src v1alpha2.RoleBasedGroupSpec) RoleBasedGroupSpec {
	dst := RoleBasedGroupSpec{
		RoleTemplates:            convertRoleTemplatesFromV1alpha2(src.RoleTemplates),
		PodGroupPolicy:           convertPodGroupPolicyFromV1alpha2(src.PodGroupPolicy),
		CoordinationRequirements: convertCoordinationsFromV1alpha2(src.CoordinationRequirements),
	}

	// Convert roles
	dst.Roles = make([]RoleSpec, len(src.Roles))
	for i, role := range src.Roles {
		dst.Roles[i] = convertRoleSpecFromV1alpha2(role)
	}

	return dst
}

// convertRoleSpecFromV1alpha2 converts RoleSpec from v1alpha2 to v1alpha1.
func convertRoleSpecFromV1alpha2(src v1alpha2.RoleSpec) RoleSpec {
	dst := RoleSpec{
		Name:            src.Name,
		Labels:          src.Labels,
		Annotations:     src.Annotations,
		Replicas:        src.Replicas,
		RolloutStrategy: convertRolloutStrategyFromV1alpha2(src.RolloutStrategy),
		RestartPolicy:   RestartPolicyType(src.RestartPolicy),
		Dependencies:    src.Dependencies,
		Workload: WorkloadSpec{
			APIVersion: src.Workload.APIVersion,
			Kind:       src.Workload.Kind,
		},
		TemplatePatch:   src.TemplatePatch,
		Components:      convertComponentsFromV1alpha2(src.Components),
		ServicePorts:    src.ServicePorts,
		EngineRuntimes:  convertEngineRuntimesFromV1alpha2(src.EngineRuntimes),
		ScalingAdapter:  convertScalingAdapterFromV1alpha2(src.ScalingAdapter),
		MinReadySeconds: src.MinReadySeconds,
	}

	// Convert Pattern back to v1alpha1 structure
	if src.Pattern != nil {
		if src.Pattern.LeaderWorkerPattern != nil {
			lwp := src.Pattern.LeaderWorkerPattern
			dst.LeaderWorkerSet = &LeaderWorkerTemplate{
				Size:                lwp.Size,
				PatchLeaderTemplate: lwp.LeaderTemplatePatch,
				PatchWorkerTemplate: lwp.WorkerTemplatePatch,
			}
			// Template goes to TemplateSource
			dst.TemplateSource = TemplateSource{
				Template:    lwp.TemplateSource.Template,
				TemplateRef: convertTemplateRefFromV1alpha2(lwp.TemplateSource.TemplateRef),
			}
		} else if src.Pattern.StandalonePattern != nil {
			sp := src.Pattern.StandalonePattern
			dst.TemplateSource = TemplateSource{
				Template:    sp.TemplateSource.Template,
				TemplateRef: convertTemplateRefFromV1alpha2(sp.TemplateSource.TemplateRef),
			}
		}
	}

	return dst
}

// convertStatusToV1alpha2 converts RoleBasedGroupStatus from v1alpha1 to v1alpha2.
func convertStatusToV1alpha2(src RoleBasedGroupStatus) v1alpha2.RoleBasedGroupStatus {
	dst := v1alpha2.RoleBasedGroupStatus{
		ObservedGeneration: src.ObservedGeneration,
		Conditions:         src.Conditions,
	}

	dst.RoleStatuses = make([]v1alpha2.RoleStatus, len(src.RoleStatuses))
	for i, rs := range src.RoleStatuses {
		dst.RoleStatuses[i] = v1alpha2.RoleStatus{
			Name:            rs.Name,
			ReadyReplicas:   rs.ReadyReplicas,
			Replicas:        rs.Replicas,
			UpdatedReplicas: rs.UpdatedReplicas,
		}
	}

	return dst
}

// convertStatusFromV1alpha2 converts RoleBasedGroupStatus from v1alpha2 to v1alpha1.
func convertStatusFromV1alpha2(src v1alpha2.RoleBasedGroupStatus) RoleBasedGroupStatus {
	dst := RoleBasedGroupStatus{
		ObservedGeneration: src.ObservedGeneration,
		Conditions:         src.Conditions,
	}

	dst.RoleStatuses = make([]RoleStatus, len(src.RoleStatuses))
	for i, rs := range src.RoleStatuses {
		dst.RoleStatuses[i] = RoleStatus{
			Name:            rs.Name,
			ReadyReplicas:   rs.ReadyReplicas,
			Replicas:        rs.Replicas,
			UpdatedReplicas: rs.UpdatedReplicas,
		}
	}

	return dst
}

// Helper conversion functions

func convertRoleTemplatesToV1alpha2(src []RoleTemplate) []v1alpha2.RoleTemplate {
	if src == nil {
		return nil
	}
	dst := make([]v1alpha2.RoleTemplate, len(src))
	for i, rt := range src {
		dst[i] = v1alpha2.RoleTemplate{
			Name:     rt.Name,
			Template: rt.Template,
		}
	}
	return dst
}

func convertRoleTemplatesFromV1alpha2(src []v1alpha2.RoleTemplate) []RoleTemplate {
	if src == nil {
		return nil
	}
	dst := make([]RoleTemplate, len(src))
	for i, rt := range src {
		dst[i] = RoleTemplate{
			Name:     rt.Name,
			Template: rt.Template,
		}
	}
	return dst
}

func convertTemplateRefToV1alpha2(src *TemplateRef) *v1alpha2.TemplateRef {
	if src == nil {
		return nil
	}
	return &v1alpha2.TemplateRef{
		Name: src.Name,
	}
}

func convertTemplateRefFromV1alpha2(src *v1alpha2.TemplateRef) *TemplateRef {
	if src == nil {
		return nil
	}
	return &TemplateRef{
		Name: src.Name,
	}
}

func convertPodGroupPolicyToV1alpha2(src *PodGroupPolicy) *v1alpha2.PodGroupPolicy {
	if src == nil {
		return nil
	}
	dst := &v1alpha2.PodGroupPolicy{}
	if src.KubeScheduling != nil {
		dst.KubeScheduling = &v1alpha2.KubeSchedulingPodGroupPolicySource{
			ScheduleTimeoutSeconds: src.KubeScheduling.ScheduleTimeoutSeconds,
		}
	}
	if src.VolcanoScheduling != nil {
		dst.VolcanoScheduling = &v1alpha2.VolcanoSchedulingPodGroupPolicySource{
			PriorityClassName: src.VolcanoScheduling.PriorityClassName,
			Queue:             src.VolcanoScheduling.Queue,
		}
	}
	return dst
}

func convertPodGroupPolicyFromV1alpha2(src *v1alpha2.PodGroupPolicy) *PodGroupPolicy {
	if src == nil {
		return nil
	}
	dst := &PodGroupPolicy{}
	if src.KubeScheduling != nil {
		dst.KubeScheduling = &KubeSchedulingPodGroupPolicySource{
			ScheduleTimeoutSeconds: src.KubeScheduling.ScheduleTimeoutSeconds,
		}
	}
	if src.VolcanoScheduling != nil {
		dst.VolcanoScheduling = &VolcanoSchedulingPodGroupPolicySource{
			PriorityClassName: src.VolcanoScheduling.PriorityClassName,
			Queue:             src.VolcanoScheduling.Queue,
		}
	}
	return dst
}

func convertCoordinationsToV1alpha2(src []Coordination) []v1alpha2.Coordination {
	if src == nil {
		return nil
	}
	dst := make([]v1alpha2.Coordination, len(src))
	for i, c := range src {
		dst[i] = v1alpha2.Coordination{
			Name:     c.Name,
			Roles:    c.Roles,
			Strategy: convertCoordinationStrategyToV1alpha2(c.Strategy),
		}
	}
	return dst
}

func convertCoordinationsFromV1alpha2(src []v1alpha2.Coordination) []Coordination {
	if src == nil {
		return nil
	}
	dst := make([]Coordination, len(src))
	for i, c := range src {
		dst[i] = Coordination{
			Name:     c.Name,
			Roles:    c.Roles,
			Strategy: convertCoordinationStrategyFromV1alpha2(c.Strategy),
		}
	}
	return dst
}

func convertCoordinationStrategyToV1alpha2(src *CoordinationStrategy) *v1alpha2.CoordinationStrategy {
	if src == nil {
		return nil
	}
	dst := &v1alpha2.CoordinationStrategy{}
	if src.RollingUpdate != nil {
		dst.RollingUpdate = &v1alpha2.CoordinationRollingUpdate{
			MaxSkew:        src.RollingUpdate.MaxSkew,
			Partition:      src.RollingUpdate.Partition,
			MaxUnavailable: src.RollingUpdate.MaxUnavailable,
		}
	}
	if src.Scaling != nil {
		dst.Scaling = &v1alpha2.CoordinationScaling{
			MaxSkew:     src.Scaling.MaxSkew,
			Progression: (*v1alpha2.ProgressionType)(src.Scaling.Progression),
		}
	}
	return dst
}

func convertCoordinationStrategyFromV1alpha2(src *v1alpha2.CoordinationStrategy) *CoordinationStrategy {
	if src == nil {
		return nil
	}
	dst := &CoordinationStrategy{}
	if src.RollingUpdate != nil {
		dst.RollingUpdate = &CoordinationRollingUpdate{
			MaxSkew:        src.RollingUpdate.MaxSkew,
			Partition:      src.RollingUpdate.Partition,
			MaxUnavailable: src.RollingUpdate.MaxUnavailable,
		}
	}
	if src.Scaling != nil {
		dst.Scaling = &CoordinationScaling{
			MaxSkew:     src.Scaling.MaxSkew,
			Progression: (*ProgressionType)(src.Scaling.Progression),
		}
	}
	return dst
}

func convertRolloutStrategyToV1alpha2(src *RolloutStrategy) *v1alpha2.RolloutStrategy {
	if src == nil {
		return nil
	}
	dst := &v1alpha2.RolloutStrategy{
		Type: v1alpha2.RolloutStrategyType(src.Type),
	}
	if src.RollingUpdate != nil {
		dst.RollingUpdate = &v1alpha2.RollingUpdate{
			Type:           v1alpha2.UpdateStrategyType(src.RollingUpdate.Type),
			Partition:      src.RollingUpdate.Partition,
			MaxUnavailable: src.RollingUpdate.MaxUnavailable,
			MaxSurge:       src.RollingUpdate.MaxSurge,
			Paused:         src.RollingUpdate.Paused,
		}
		if src.RollingUpdate.InPlaceUpdateStrategy != nil {
			dst.RollingUpdate.InPlaceUpdateStrategy = &v1alpha2.InPlaceUpdateStrategy{
				GracePeriodSeconds: src.RollingUpdate.InPlaceUpdateStrategy.GracePeriodSeconds,
			}
		}
	}
	return dst
}

func convertRolloutStrategyFromV1alpha2(src *v1alpha2.RolloutStrategy) *RolloutStrategy {
	if src == nil {
		return nil
	}
	dst := &RolloutStrategy{
		Type: RolloutStrategyType(src.Type),
	}
	if src.RollingUpdate != nil {
		dst.RollingUpdate = &RollingUpdate{
			Type:           UpdateStrategyType(src.RollingUpdate.Type),
			Partition:      src.RollingUpdate.Partition,
			MaxUnavailable: src.RollingUpdate.MaxUnavailable,
			MaxSurge:       src.RollingUpdate.MaxSurge,
			Paused:         src.RollingUpdate.Paused,
		}
		if src.RollingUpdate.InPlaceUpdateStrategy != nil {
			dst.RollingUpdate.InPlaceUpdateStrategy = &InPlaceUpdateStrategy{
				GracePeriodSeconds: src.RollingUpdate.InPlaceUpdateStrategy.GracePeriodSeconds,
			}
		}
	}
	return dst
}

func convertComponentsToV1alpha2(src []InstanceComponent) []v1alpha2.InstanceComponent {
	if src == nil {
		return nil
	}
	dst := make([]v1alpha2.InstanceComponent, len(src))
	for i, c := range src {
		dst[i] = v1alpha2.InstanceComponent{
			Name:        c.Name,
			Size:        c.Size,
			ServiceName: c.ServiceName,
			Template:    c.Template,
		}
	}
	return dst
}

func convertComponentsFromV1alpha2(src []v1alpha2.InstanceComponent) []InstanceComponent {
	if src == nil {
		return nil
	}
	dst := make([]InstanceComponent, len(src))
	for i, c := range src {
		dst[i] = InstanceComponent{
			Name:        c.Name,
			Size:        c.Size,
			ServiceName: c.ServiceName,
			Template:    c.Template,
		}
	}
	return dst
}

func convertEngineRuntimesToV1alpha2(src []EngineRuntime) []v1alpha2.EngineRuntime {
	if src == nil {
		return nil
	}
	dst := make([]v1alpha2.EngineRuntime, len(src))
	for i, er := range src {
		dst[i] = v1alpha2.EngineRuntime{
			ProfileName:      er.ProfileName,
			InjectContainers: er.InjectContainers,
			Containers:       er.Containers,
		}
	}
	return dst
}

func convertEngineRuntimesFromV1alpha2(src []v1alpha2.EngineRuntime) []EngineRuntime {
	if src == nil {
		return nil
	}
	dst := make([]EngineRuntime, len(src))
	for i, er := range src {
		dst[i] = EngineRuntime{
			ProfileName:      er.ProfileName,
			InjectContainers: er.InjectContainers,
			Containers:       er.Containers,
		}
	}
	return dst
}

func convertScalingAdapterToV1alpha2(src *ScalingAdapter) *v1alpha2.ScalingAdapter {
	if src == nil {
		return nil
	}
	return &v1alpha2.ScalingAdapter{
		Enable: src.Enable,
	}
}

func convertScalingAdapterFromV1alpha2(src *v1alpha2.ScalingAdapter) *ScalingAdapter {
	if src == nil {
		return nil
	}
	return &ScalingAdapter{
		Enable: src.Enable,
	}
}

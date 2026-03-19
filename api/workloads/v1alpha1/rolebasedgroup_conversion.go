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
	"encoding/json"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	v2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const (
	// annotationV1alpha1PodGroupPolicy stores the serialized v1alpha1 PodGroupPolicy
	// on the hub (v1alpha2) object so it can be round-tripped back.
	annotationV1alpha1PodGroupPolicy = "conversion.workloads.x-k8s.io/v1alpha1-pod-group-policy"

	// annotationV1alpha1Coordination stores the serialized v1alpha1 CoordinationRequirements.
	annotationV1alpha1Coordination = "conversion.workloads.x-k8s.io/v1alpha1-coordination"
)

// ConvertTo converts this RoleBasedGroup (v1alpha1) to the Hub version (v1alpha2).
func (src *RoleBasedGroup) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v2.RoleBasedGroup)
	if !ok {
		return fmt.Errorf("expected *v1alpha2.RoleBasedGroup, got %T", dstRaw)
	}

	// ObjectMeta
	dst.ObjectMeta = src.ObjectMeta

	// Convert Spec
	if err := convertSpecV1alpha1ToV2(&src.Spec, &dst.Spec); err != nil {
		return err
	}

	// Convert Status
	convertStatusV1alpha1ToV2(&src.Status, &dst.Status)

	// Preserve lossy fields in annotations on the hub object.
	if err := preserveV1alpha1Fields(src, dst); err != nil {
		return err
	}

	return nil
}

// ConvertFrom converts from the Hub version (v1alpha2) back to this RoleBasedGroup (v1alpha1).
func (dst *RoleBasedGroup) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v2.RoleBasedGroup)
	if !ok {
		return fmt.Errorf("expected *v1alpha2.RoleBasedGroup, got %T", srcRaw)
	}

	// ObjectMeta
	dst.ObjectMeta = src.ObjectMeta

	// Convert Spec
	if err := convertSpecV2ToV1alpha1(&src.Spec, &dst.Spec); err != nil {
		return err
	}

	// Convert Status
	convertStatusV2ToV1alpha1(&src.Status, &dst.Status)

	// Restore lossy fields that were preserved in annotations.
	if err := restoreV1alpha1Fields(src, dst); err != nil {
		return err
	}

	// Remove conversion-only annotations from the v1alpha1 object.
	removeConversionAnnotations(dst.Annotations)

	return nil
}

// ---------------------------------------------------------------------------
// Spec conversion helpers
// ---------------------------------------------------------------------------

func convertSpecV1alpha1ToV2(src *RoleBasedGroupSpec, dst *v2.RoleBasedGroupSpec) error {
	// RoleTemplates – identical structure
	if len(src.RoleTemplates) > 0 {
		dst.RoleTemplates = make([]v2.RoleTemplate, len(src.RoleTemplates))
		for i, rt := range src.RoleTemplates {
			dst.RoleTemplates[i] = v2.RoleTemplate{
				Name:     rt.Name,
				Template: *rt.Template.DeepCopy(),
			}
		}
	}

	// Roles
	dst.Roles = make([]v2.RoleSpec, len(src.Roles))
	for i, role := range src.Roles {
		if err := convertRoleV1alpha1ToV2(&role, &dst.Roles[i]); err != nil {
			return err
		}
	}

	// PodGroupPolicy and CoordinationRequirements are lossy – preserved via annotation (see preserveV1alpha1Fields).

	return nil
}

func convertRoleV1alpha1ToV2(src *RoleSpec, dst *v2.RoleSpec) error {
	dst.Name = src.Name
	dst.Labels = src.Labels
	dst.Annotations = src.Annotations
	dst.Replicas = src.Replicas
	dst.RestartPolicy = v2.RestartPolicyType(src.RestartPolicy)
	dst.Dependencies = src.Dependencies
	dst.ServicePorts = src.ServicePorts
	dst.MinReadySeconds = src.MinReadySeconds
	dst.ScalingAdapter = convertScalingAdapterV1alpha1ToV2(src.ScalingAdapter)
	dst.EngineRuntimes = convertEngineRuntimesV1alpha1ToV2(src.EngineRuntimes)

	// Workload – deprecated in v2 but carry it over; v2 defaults differ so we set explicitly.
	dst.Workload = v2.WorkloadSpec{
		APIVersion: src.Workload.APIVersion,
		Kind:       src.Workload.Kind,
	}

	// RolloutStrategy
	if src.RolloutStrategy != nil {
		dst.RolloutStrategy = convertRolloutStrategyV1alpha1ToV2(src.RolloutStrategy)
	}

	// Pattern: determine from v1alpha1 workload kind and fields.
	switch {
	case src.LeaderWorkerSet != nil:
		// LWS → LeaderWorkerPattern
		lwp := &v2.LeaderWorkerPattern{
			Size:                src.LeaderWorkerSet.Size,
			LeaderTemplatePatch: src.LeaderWorkerSet.PatchLeaderTemplate,
			WorkerTemplatePatch: src.LeaderWorkerSet.PatchWorkerTemplate,
		}
		// Template source
		if src.TemplateSource.Template != nil || src.TemplateSource.TemplateRef != nil {
			if src.TemplateSource.Template != nil {
				tmp := src.TemplateSource.Template.DeepCopy()
				lwp.TemplateSource = v2.TemplateSource{Template: tmp}
			} else if src.TemplateSource.TemplateRef != nil {
				tref := convertTemplateRefV1alpha1ToV2(src.TemplateSource.TemplateRef, &src.TemplatePatch)
				lwp.TemplateSource = v2.TemplateSource{TemplateRef: tref}
			}
		}
		dst.Pattern = v2.Pattern{LeaderWorkerPattern: lwp}

	case len(src.Components) > 0:
		// Components → CustomComponentsPattern
		components := make([]v2.InstanceComponent, len(src.Components))
		for i, c := range src.Components {
			components[i] = v2.InstanceComponent{
				Name:        c.Name,
				Size:        c.Size,
				ServiceName: c.ServiceName,
				Template:    *c.Template.DeepCopy(),
			}
		}
		dst.Pattern = v2.Pattern{
			CustomComponentsPattern: &v2.CustomComponentsPattern{Components: components},
		}

	default:
		// StandalonePattern (InstanceSet / StatefulSet / Deployment)
		sp := &v2.StandalonePattern{}
		if src.TemplateSource.Template != nil {
			tmp := src.TemplateSource.Template.DeepCopy()
			sp.TemplateSource = v2.TemplateSource{Template: tmp}
		} else if src.TemplateSource.TemplateRef != nil {
			tref := convertTemplateRefV1alpha1ToV2(src.TemplateSource.TemplateRef, &src.TemplatePatch)
			sp.TemplateSource = v2.TemplateSource{TemplateRef: tref}
		}
		dst.Pattern = v2.Pattern{StandalonePattern: sp}
	}

	return nil
}

// convertTemplateRefV1alpha1ToV2 converts a v1alpha1 TemplateRef (+patch) to a v1alpha2 TemplateRef.
func convertTemplateRefV1alpha1ToV2(ref *TemplateRef, patch *runtime.RawExtension) *v2.TemplateRef {
	if ref == nil {
		return nil
	}
	r := &v2.TemplateRef{Name: ref.Name}
	if patch != nil && len(patch.Raw) > 0 {
		p := patch.DeepCopy()
		r.Patch = p
	}
	return r
}

func convertRolloutStrategyV1alpha1ToV2(src *RolloutStrategy) *v2.RolloutStrategy {
	if src == nil {
		return nil
	}
	dst := &v2.RolloutStrategy{
		Type: v2.RolloutStrategyType(src.Type),
	}
	if src.RollingUpdate != nil {
		dst.RollingUpdate = &v2.RollingUpdate{
			Type:           v2.UpdateStrategyType(src.RollingUpdate.Type),
			Partition:      src.RollingUpdate.Partition,
			MaxUnavailable: src.RollingUpdate.MaxUnavailable,
			MaxSurge:       src.RollingUpdate.MaxSurge,
			Paused:         src.RollingUpdate.Paused,
		}
		if src.RollingUpdate.InPlaceUpdateStrategy != nil {
			dst.RollingUpdate.InPlaceUpdateStrategy = &v2.InPlaceUpdateStrategy{
				GracePeriodSeconds: src.RollingUpdate.InPlaceUpdateStrategy.GracePeriodSeconds,
			}
		}
	}
	return dst
}

func convertScalingAdapterV1alpha1ToV2(src *ScalingAdapter) *v2.ScalingAdapter {
	if src == nil {
		return nil
	}
	return &v2.ScalingAdapter{Enable: src.Enable}
}

func convertEngineRuntimesV1alpha1ToV2(src []EngineRuntime) []v2.EngineRuntime {
	if len(src) == 0 {
		return nil
	}
	dst := make([]v2.EngineRuntime, len(src))
	for i, er := range src {
		dst[i] = v2.EngineRuntime{
			ProfileName:      er.ProfileName,
			InjectContainers: er.InjectContainers,
			Containers:       er.Containers,
		}
	}
	return dst
}

func convertStatusV1alpha1ToV2(src *RoleBasedGroupStatus, dst *v2.RoleBasedGroupStatus) {
	dst.ObservedGeneration = src.ObservedGeneration
	dst.Conditions = src.Conditions
	if len(src.RoleStatuses) > 0 {
		dst.RoleStatuses = make([]v2.RoleStatus, len(src.RoleStatuses))
		for i, rs := range src.RoleStatuses {
			dst.RoleStatuses[i] = v2.RoleStatus{
				Name:            rs.Name,
				ReadyReplicas:   rs.ReadyReplicas,
				Replicas:        rs.Replicas,
				UpdatedReplicas: rs.UpdatedReplicas,
			}
		}
	}
}

// ---------------------------------------------------------------------------
// v1alpha2 → v1alpha1 conversion helpers
// ---------------------------------------------------------------------------

func convertSpecV2ToV1alpha1(src *v2.RoleBasedGroupSpec, dst *RoleBasedGroupSpec) error {
	// RoleTemplates
	if len(src.RoleTemplates) > 0 {
		dst.RoleTemplates = make([]RoleTemplate, len(src.RoleTemplates))
		for i, rt := range src.RoleTemplates {
			dst.RoleTemplates[i] = RoleTemplate{
				Name:     rt.Name,
				Template: *rt.Template.DeepCopy(),
			}
		}
	}

	// Roles
	dst.Roles = make([]RoleSpec, len(src.Roles))
	for i, role := range src.Roles {
		if err := convertRoleV2ToV1alpha1(&role, &dst.Roles[i]); err != nil {
			return err
		}
	}

	return nil
}

func convertRoleV2ToV1alpha1(src *v2.RoleSpec, dst *RoleSpec) error {
	dst.Name = src.Name
	dst.Labels = src.Labels
	dst.Annotations = src.Annotations
	dst.Replicas = src.Replicas
	dst.RestartPolicy = RestartPolicyType(src.RestartPolicy)
	dst.Dependencies = src.Dependencies
	dst.ServicePorts = src.ServicePorts
	dst.MinReadySeconds = src.MinReadySeconds
	dst.ScalingAdapter = convertScalingAdapterV2ToV1alpha1(src.ScalingAdapter)
	dst.EngineRuntimes = convertEngineRuntimesV2ToV1alpha1(src.EngineRuntimes)

	// Workload
	dst.Workload = WorkloadSpec{
		APIVersion: src.Workload.APIVersion,
		Kind:       src.Workload.Kind,
	}

	// RolloutStrategy
	if src.RolloutStrategy != nil {
		dst.RolloutStrategy = convertRolloutStrategyV2ToV1alpha1(src.RolloutStrategy)
	}

	// Pattern → v1alpha1 fields
	switch {
	case src.Pattern.LeaderWorkerPattern != nil:
		lwp := src.Pattern.LeaderWorkerPattern
		dst.LeaderWorkerSet = &LeaderWorkerTemplate{
			Size:                lwp.Size,
			PatchLeaderTemplate: lwp.LeaderTemplatePatch,
			PatchWorkerTemplate: lwp.WorkerTemplatePatch,
		}
		dst.TemplateSource, dst.TemplatePatch = convertTemplateSrcV2ToV1alpha1(&lwp.TemplateSource)

	case src.Pattern.CustomComponentsPattern != nil:
		ccp := src.Pattern.CustomComponentsPattern
		dst.Components = make([]InstanceComponent, len(ccp.Components))
		for i, c := range ccp.Components {
			dst.Components[i] = InstanceComponent{
				Name:        c.Name,
				Size:        c.Size,
				ServiceName: c.ServiceName,
				Template:    *c.Template.DeepCopy(),
			}
		}

	case src.Pattern.StandalonePattern != nil:
		dst.TemplateSource, dst.TemplatePatch = convertTemplateSrcV2ToV1alpha1(&src.Pattern.StandalonePattern.TemplateSource)

	default:
		// No pattern set – nothing to populate.
	}

	return nil
}

// convertTemplateSrcV2ToV1alpha1 converts a v1alpha2 TemplateSource back to v1alpha1 TemplateSource + TemplatePatch.
func convertTemplateSrcV2ToV1alpha1(src *v2.TemplateSource) (TemplateSource, runtime.RawExtension) {
	var ts TemplateSource
	var patch runtime.RawExtension

	if src == nil {
		return ts, patch
	}

	if src.Template != nil {
		ts.Template = src.Template.DeepCopy()
	} else if src.TemplateRef != nil {
		ts.TemplateRef = &TemplateRef{Name: src.TemplateRef.Name}
		if src.TemplateRef.Patch != nil {
			patch = *src.TemplateRef.Patch.DeepCopy()
		}
	}
	return ts, patch
}

func convertRolloutStrategyV2ToV1alpha1(src *v2.RolloutStrategy) *RolloutStrategy {
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

func convertScalingAdapterV2ToV1alpha1(src *v2.ScalingAdapter) *ScalingAdapter {
	if src == nil {
		return nil
	}
	return &ScalingAdapter{Enable: src.Enable}
}

func convertEngineRuntimesV2ToV1alpha1(src []v2.EngineRuntime) []EngineRuntime {
	if len(src) == 0 {
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

func convertStatusV2ToV1alpha1(src *v2.RoleBasedGroupStatus, dst *RoleBasedGroupStatus) {
	dst.ObservedGeneration = src.ObservedGeneration
	dst.Conditions = src.Conditions
	if len(src.RoleStatuses) > 0 {
		dst.RoleStatuses = make([]RoleStatus, len(src.RoleStatuses))
		for i, rs := range src.RoleStatuses {
			dst.RoleStatuses[i] = RoleStatus{
				Name:            rs.Name,
				ReadyReplicas:   rs.ReadyReplicas,
				Replicas:        rs.Replicas,
				UpdatedReplicas: rs.UpdatedReplicas,
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Lossy-field preservation via annotations
// ---------------------------------------------------------------------------

// preserveV1alpha1Fields serializes v1alpha1-only fields (podGroupPolicy, coordination)
// into annotations on the v1alpha2 hub object so they survive round-trips.
// Note: WorkloadSpec (apiVersion/kind) is preserved directly via the Workload field
// which exists in both versions, so no annotation is needed for it.
//
// Regarding spec.coordination: in v1alpha2 this concept was extracted into a standalone
// CoordinatedPolicy resource. The conversion webhook cannot create or delete sibling objects,
// so coordination data is round-tripped via annotation. A one-time migration controller
// (or migration job run during upgrade) is responsible for:
//  1. Reading spec.coordination from existing v1alpha1 RoleBasedGroups.
//  2. Creating the corresponding CoordinatedPolicy objects.
//  3. Removing the preservation annotation once migration is complete.
func preserveV1alpha1Fields(src *RoleBasedGroup, dst *v2.RoleBasedGroup) error {
	if dst.Annotations == nil {
		dst.Annotations = map[string]string{}
	}

	// Translate v1alpha1 annotation keys to their controller-readable equivalents.
	// The controller always uses the constants package keys, never the v1alpha1 ones.
	if val, ok := dst.Annotations[ExclusiveKeyAnnotationKey]; ok {
		dst.Annotations[constants.GroupExclusiveTopologyKey] = val
	}

	// PodGroupPolicy: preserve raw for round-trip AND translate to controller-readable annotations.
	if src.Spec.PodGroupPolicy != nil {
		data, err := json.Marshal(src.Spec.PodGroupPolicy)
		if err != nil {
			return fmt.Errorf("marshalling PodGroupPolicy: %w", err)
		}
		dst.Annotations[annotationV1alpha1PodGroupPolicy] = string(data)

		// Translate to the annotation-based gang scheduling that the controller reads.
		pgp := src.Spec.PodGroupPolicy
		if pgp.KubeScheduling != nil {
			dst.Annotations[constants.GangSchedulingAnnotationKey] = "true"
			if pgp.KubeScheduling.ScheduleTimeoutSeconds != nil {
				dst.Annotations[constants.GangSchedulingScheduleTimeoutSecondsKey] =
					strconv.Itoa(int(*pgp.KubeScheduling.ScheduleTimeoutSeconds))
			}
		} else if pgp.VolcanoScheduling != nil {
			dst.Annotations[constants.GangSchedulingAnnotationKey] = "true"
			if pgp.VolcanoScheduling.Queue != "" {
				dst.Annotations[constants.GangSchedulingVolcanoQueueKey] = pgp.VolcanoScheduling.Queue
			}
			if pgp.VolcanoScheduling.PriorityClassName != "" {
				dst.Annotations[constants.GangSchedulingVolcanoPriorityClassKey] = pgp.VolcanoScheduling.PriorityClassName
			}
		}
	}

	// CoordinationRequirements
	if len(src.Spec.CoordinationRequirements) > 0 {
		data, err := json.Marshal(src.Spec.CoordinationRequirements)
		if err != nil {
			return fmt.Errorf("marshalling CoordinationRequirements: %w", err)
		}
		dst.Annotations[annotationV1alpha1Coordination] = string(data)
	}

	return nil
}

// restoreV1alpha1Fields reads previously preserved annotations from the hub object
// and populates the corresponding v1alpha1 fields.
func restoreV1alpha1Fields(src *v2.RoleBasedGroup, dst *RoleBasedGroup) error {
	annotations := src.Annotations

	// PodGroupPolicy
	if raw, ok := annotations[annotationV1alpha1PodGroupPolicy]; ok && raw != "" {
		var pgp PodGroupPolicy
		if err := json.Unmarshal([]byte(raw), &pgp); err != nil {
			return fmt.Errorf("unmarshalling PodGroupPolicy: %w", err)
		}
		dst.Spec.PodGroupPolicy = &pgp
	}

	// CoordinationRequirements
	if raw, ok := annotations[annotationV1alpha1Coordination]; ok && raw != "" {
		var coord []Coordination
		if err := json.Unmarshal([]byte(raw), &coord); err != nil {
			return fmt.Errorf("unmarshalling CoordinationRequirements: %w", err)
		}
		dst.Spec.CoordinationRequirements = coord
	}

	return nil
}

// removeConversionAnnotations deletes conversion-only annotations from the map in-place.
func removeConversionAnnotations(annotations map[string]string) {
	if annotations == nil {
		return
	}
	delete(annotations, annotationV1alpha1PodGroupPolicy)
	delete(annotations, annotationV1alpha1Coordination)
	// Also remove the translated gang-scheduling annotations that were injected by ConvertTo.
	delete(annotations, constants.GangSchedulingAnnotationKey)
	delete(annotations, constants.GangSchedulingScheduleTimeoutSecondsKey)
	delete(annotations, constants.GangSchedulingVolcanoQueueKey)
	delete(annotations, constants.GangSchedulingVolcanoPriorityClassKey)
	// Also remove the translated exclusive-topology annotation.
	delete(annotations, constants.GroupExclusiveTopologyKey)
}

// copyAnnotations returns a shallow copy of the given annotation map, never nil.
func copyAnnotations(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

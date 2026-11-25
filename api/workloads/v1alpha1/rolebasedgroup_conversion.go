/*
Copyright 2026 The RBG Authors.

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
	"strings"

	apimachineryvalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	v2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const (
	// annotationV1alpha1PodGroupPolicy stores the serialized v1alpha1 PodGroupPolicy
	// on the hub (v1alpha2) object so it can be round-tripped back.
	annotationV1alpha1PodGroupPolicy = constants.ConversionV1alpha1PodGroupPolicyAnnotationKey

	// annotationV1alpha1Coordination stores the serialized v1alpha1 CoordinationRequirements.
	annotationV1alpha1Coordination = constants.ConversionV1alpha1CoordinationAnnotationKey

	// annotationV2RoleProvenance carries, in the v1alpha1 object's metadata
	// annotations, a map keyed by role name. The map preserves v1alpha2 role
	// provenance that v1alpha1 cannot express. It is not a new API field.
	annotationV2RoleProvenance = constants.ConversionV2RoleProvenanceAnnotationKey
)

// oversizedProvenanceMarker replaces a marker that does not fit within the API
// server's annotation size limit. ConvertTo rejects a write that carries it. A
// write that strips the annotation follows the fresh-write path, as documented
// for the conversion marker.
const oversizedProvenanceMarker = "oversized"

// ConvertTo converts this RoleBasedGroup (v1alpha1) to the Hub version (v1alpha2).
func (src *RoleBasedGroup) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v2.RoleBasedGroup)
	if !ok {
		return fmt.Errorf("expected *v1alpha2.RoleBasedGroup, got %T", dstRaw)
	}

	// ObjectMeta
	dst.ObjectMeta = *src.ObjectMeta.DeepCopy()

	markers, roundTrip, err := restoreV2RoleProvenanceMarkers(src.Annotations)
	if err != nil {
		return err
	}
	if roundTrip {
		delete(dst.Annotations, annotationV2RoleProvenance)
	}

	// Convert Spec
	if err := convertSpecV1alpha1ToV2(&src.Spec, &dst.Spec, markers, roundTrip); err != nil {
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
	dst.ObjectMeta = *src.ObjectMeta.DeepCopy()

	// Convert Spec
	if err := convertSpecV2ToV1alpha1(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	if err := preserveV2RoleProvenanceMarkers(src, dst); err != nil {
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
	if err := dropOversizedProvenanceMarker(dst.Annotations); err != nil {
		return err
	}

	return nil
}

// dropOversizedProvenanceMarker replaces the round-trip marker when it would
// push the object's annotations over the API server's size limit: the API server
// rejects such a conversion response, which would make the object unreadable
// through v1alpha1. If even the replacement does not fit, conversion fails
// closed rather than emitting a markerless object whose next write would
// silently follow the fresh-write path.
func dropOversizedProvenanceMarker(annotations map[string]string) error {
	if _, ok := annotations[annotationV2RoleProvenance]; !ok {
		return nil
	}
	if apimachineryvalidation.ValidateAnnotationsSize(annotations) == nil {
		return nil
	}
	annotations[annotationV2RoleProvenance] = oversizedProvenanceMarker
	if apimachineryvalidation.ValidateAnnotationsSize(annotations) != nil {
		return fmt.Errorf("the v1alpha2-only role fields of this object cannot be represented in a v1alpha1 round trip; update it through workloads.x-k8s.io/v1alpha2")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Spec conversion helpers
// ---------------------------------------------------------------------------

func convertSpecV1alpha1ToV2(src *RoleBasedGroupSpec, dst *v2.RoleBasedGroupSpec, markers map[string]v2RoleProvenanceMarker, roundTrip bool) error {
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
		// A role the read did not emit (one added by the writer) has no
		// provenance to restore, so it converts like a fresh write.
		marker, marked := markers[role.Name]
		if err := convertRoleV1alpha1ToV2(&role, &dst.Roles[i], marker, roundTrip && marked); err != nil {
			return err
		}
	}

	// PodGroupPolicy and CoordinationRequirements are lossy – preserved via annotation (see preserveV1alpha1Fields).

	return nil
}

func convertRoleV1alpha1ToV2(src *RoleSpec, dst *v2.RoleSpec, marker v2RoleProvenanceMarker, roundTrip bool) error {
	dst.Name = src.Name
	dst.Labels = src.Labels
	// Deep-copy annotations to avoid mutating the apiserver-provided source object
	// when writing conversion-only keys (e.g. RoleWorkloadTypeAnnotationKey).
	dst.Annotations = copyAnnotations(src.Annotations)
	dst.Replicas = src.Replicas
	dst.Dependencies = src.Dependencies
	dst.ServicePorts = src.ServicePorts
	dst.MinReadySeconds = src.MinReadySeconds
	dst.ScalingAdapter = convertScalingAdapterV1alpha1ToV2(src.ScalingAdapter)
	dst.EngineRuntimes = convertEngineRuntimesV1alpha1ToV2(src.EngineRuntimes)

	// ConvertFrom marks every role it emits (see preserveV2RoleProvenanceMarkers),
	// so the marker tells a read-and-write round trip from a fresh v1alpha1
	// write: a client-built object, a full-object replace without the marker, or
	// the first apply of a manifest. A fresh write must keep the mapping that
	// v0.8.0 and later stored, or re-applying an unchanged manifest rewrites and
	// rolls every object those releases stored. A round trip restores what
	// v1alpha1 cannot express, and nothing else (#580).
	// Workload – preserved via role annotation instead of field in v1alpha2.
	// This annotation is for conversion compatibility only. It is filtered from
	// common child annotations, but the RoleInstanceSet selective-membership path
	// intentionally retains it on child workload metadata.
	// New v1alpha2 RBGs should NOT set this annotation.
	//
	// The workload field is authoritative. An unchanged workload restores the
	// annotation exactly as it was read, including an explicitly empty value.
	restoreRoleWorkloadAnnotation(src, dst, marker, roundTrip)
	if len(dst.Annotations) == 0 {
		// A role whose object-level marker entry restores no role
		// annotations must not round trip to an empty-but-present map.
		dst.Annotations = nil
	}

	// Restart policy. A fresh write keeps the mapping every release since
	// #424 stored. A round trip distinguishes a deliberate type edit from an
	// unchanged write by comparing the wire value with the type ConvertFrom
	// emitted: an edit's wire type wins (keeping the preserved delays and
	// activateAfter, dropping the deprecated string), an unchanged write
	// restores both raw fields verbatim — config, delays, activateAfter, and
	// the deprecated pattern-level string — so neither is folded or dropped
	// (#580).
	wireRestartPolicy := convertRestartPolicyV1alpha1ToV2(src.RestartPolicy)
	var restartPolicyConfig *v2.RestartPolicyConfig
	var legacyRestartPolicy v2.RestartPolicyType
	switch {
	case !roundTrip:
		restartPolicyConfig = &v2.RestartPolicyConfig{Type: wireRestartPolicy}
	case wireRestartPolicy != emittedRestartPolicyType(marker):
		// A deliberate edit of the type, including clearing it.
		restartPolicyConfig = marker.Config
		if restartPolicyConfig == nil {
			restartPolicyConfig = &v2.RestartPolicyConfig{}
		}
		restartPolicyConfig.Type = wireRestartPolicy
		if restartPolicyConfig.MaxGroupRestarts != nil {
			if wireRestartPolicy == v2.RestartPolicyNone {
				// None cannot carry a budget: dropping it matches the
				// v1alpha2 admission rule instead of making the edit fail.
				restartPolicyConfig.MaxGroupRestarts = nil
			} else if wireRestartPolicy == "" && (src.LeaderWorkerSet != nil || len(src.Components) > 0) {
				// These patterns resolve an empty type to Recreate. Keep the
				// untouched budget, but write the type explicitly so it is
				// valid without relying on defaulting.
				restartPolicyConfig.Type = v2.RecreateRoleInstanceOnPodRestart
			}
		}
	default:
		// An unchanged round trip: restore the raw shape.
		restartPolicyConfig = marker.Config
		legacyRestartPolicy = marker.LegacyType
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
			RestartPolicy:       legacyRestartPolicy, //nolint:staticcheck // the deprecated string is preserved verbatim for round-trip losslessness
			RestartPolicyConfig: restartPolicyConfig,
		}
		// Template source
		if src.Template != nil || src.TemplateRef != nil {
			if src.Template != nil {
				tmp := src.Template.DeepCopy()
				lwp.TemplateSource = v2.TemplateSource{Template: tmp}
			} else if src.TemplateRef != nil {
				tref := convertTemplateRefV1alpha1ToV2(src.TemplateRef, &src.TemplatePatch)
				lwp.TemplateSource = v2.TemplateSource{TemplateRef: tref}
			}
		}
		dst.Pattern = v2.Pattern{LeaderWorkerPattern: lwp}

	case len(src.Components) > 0:
		// Components → CustomComponentsPattern. A role-level templatePatch
		// with no template or templateRef is ignored here, matching the
		// pre-existing v1alpha1 compatibility behavior.
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
			CustomComponentsPattern: &v2.CustomComponentsPattern{
				Components: components,
				//nolint:staticcheck // the deprecated string is preserved verbatim for round-trip losslessness
				RestartPolicy:       legacyRestartPolicy,
				RestartPolicyConfig: restartPolicyConfig,
			},
		}

	default:
		// StandalonePattern (InstanceSet / StatefulSet / Deployment)
		sp := &v2.StandalonePattern{}
		if src.Template != nil {
			tmp := src.Template.DeepCopy()
			sp.TemplateSource = v2.TemplateSource{Template: tmp}
		} else if src.TemplateRef != nil {
			tref := convertTemplateRefV1alpha1ToV2(src.TemplateRef, &src.TemplatePatch)
			sp.TemplateSource = v2.TemplateSource{TemplateRef: tref}
		}
		dst.Pattern = v2.Pattern{StandalonePattern: sp}
	}

	if roundTrip {
		restoreV2OnlyRoleFields(dst, marker)
	}

	return nil
}

func restoreRoleWorkloadAnnotation(src *RoleSpec, dst *v2.RoleSpec, marker v2RoleProvenanceMarker, roundTrip bool) {
	if src.Workload.APIVersion == "" || src.Workload.Kind == "" {
		return
	}
	workloadType := fmt.Sprintf("%s/%s", src.Workload.APIVersion, src.Workload.Kind)
	if roundTrip && workloadType == emittedWorkloadType(ptr.Deref(marker.WorkloadType, "")) {
		// An unchanged workload restores the annotation as it was read. The wire
		// role's annotations cannot tell: a merge patch (client-side kubectl
		// apply) replaces the roles with the manifest's.
		if marker.WorkloadType == nil {
			delete(dst.Annotations, constants.RoleWorkloadTypeAnnotationKey)
			return
		}
		if dst.Annotations == nil {
			dst.Annotations = make(map[string]string)
		}
		dst.Annotations[constants.RoleWorkloadTypeAnnotationKey] = *marker.WorkloadType
		return
	}
	if dst.Annotations == nil {
		dst.Annotations = make(map[string]string)
	}
	dst.Annotations[constants.RoleWorkloadTypeAnnotationKey] = workloadType
}

// restoreV2OnlyRoleFields restores, on a round trip of a role the read emitted,
// the role fields v1alpha1 cannot express. A fresh write cannot see them and
// leaves them unset, as every release since v0.8.0 did.
func restoreV2OnlyRoleFields(dst *v2.RoleSpec, marker v2RoleProvenanceMarker) {
	dst.PodManagementPolicy = marker.PodManagementPolicy
	if marker.ScalingAdapterLabels != nil && dst.ScalingAdapter != nil {
		dst.ScalingAdapter.Labels = marker.ScalingAdapterLabels
	}
	if lwp := dst.LeaderWorkerPattern; lwp != nil {
		if marker.SharedServiceSelection != nil {
			selection := *marker.SharedServiceSelection
			lwp.SharedServiceSelection = &selection
		}
	}
	if ccp := dst.CustomComponentsPattern; ccp != nil && marker.Components != nil {
		for i := range ccp.Components {
			m, ok := marker.Components[ccp.Components[i].Name]
			if !ok {
				continue
			}
			ccp.Components[i].Labels = m.Labels
			ccp.Components[i].Annotations = copyAnnotations(m.Annotations)
		}
	}
}

// convertRestartPolicyV1alpha1ToV2 converts a v1alpha1 RestartPolicyType to v1alpha2.
// An empty value is preserved as-is so that the v1alpha2 getter applies the
// pattern-aware default (Recreate for LeaderWorkerPattern/CustomComponentsPattern,
// None for StandalonePattern) at runtime — matching v1alpha1's documented behavior.
func convertRestartPolicyV1alpha1ToV2(src RestartPolicyType) v2.RestartPolicyType {
	return v2.RestartPolicyType(src)
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

// convertUpdateStrategyTypeV1alpha1ToV2 converts a v1alpha1 UpdateStrategyType to v1alpha2.
// The v1alpha1 "Recreate" spelling is mapped to the v1alpha2 "RecreatePod" name.
// An empty value is preserved as-is (matching convertRestartPolicyV1alpha1ToV2) so
// that the v1alpha2 defaulting (mutating webhook / reconciler) applies the documented
// InPlaceIfPossible default instead of baking it into stored objects.
func convertUpdateStrategyTypeV1alpha1ToV2(src UpdateStrategyType) v2.UpdateStrategyType {
	if src == RecreateUpdateStrategyType {
		return v2.RecreatePodUpdateStrategyType
	}
	return v2.UpdateStrategyType(src)
}

// convertUpdateStrategyTypeV2ToV1alpha1 converts a v1alpha2 UpdateStrategyType to v1alpha1.
// It is the inverse of convertUpdateStrategyTypeV1alpha1ToV2: RecreatePod maps back to
// the v1alpha1 "Recreate" spelling; all other values pass through unchanged.
func convertUpdateStrategyTypeV2ToV1alpha1(src v2.UpdateStrategyType) UpdateStrategyType {
	if src == v2.RecreatePodUpdateStrategyType {
		return RecreateUpdateStrategyType
	}
	return UpdateStrategyType(src)
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
			Type:           convertUpdateStrategyTypeV1alpha1ToV2(src.RollingUpdate.Type),
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
			Component:        er.Component,
			TargetContainers: convertEngineRuntimeTargetContainersV1alpha1ToV2(er.TargetContainers),
			InjectContainers: er.InjectContainers,
			Containers:       er.Containers,
		}
	}
	return dst
}

func convertEngineRuntimeTargetContainersV1alpha1ToV2(
	in []EngineRuntimeTargetContainer,
) []v2.EngineRuntimeTargetContainer {
	if in == nil {
		return nil
	}
	dst := make([]v2.EngineRuntimeTargetContainer, len(in))
	for i, tc := range in {
		dst[i] = v2.EngineRuntimeTargetContainer{Name: tc.Name}
		dst[i].ExtraPatches = convertContainerPatchesV1alpha1ToV2(tc.ExtraPatches)
	}
	return dst
}

func convertContainerPatchesV1alpha1ToV2(in []ContainerPatch) []v2.ContainerPatch {
	if in == nil {
		return nil
	}
	dst := make([]v2.ContainerPatch, len(in))
	for i, p := range in {
		dst[i].JSONPatch = convertContainerJSONPatchV1alpha1ToV2(p.JSONPatch)
	}
	return dst
}

func convertContainerJSONPatchV1alpha1ToV2(in *ContainerJSONPatch) *v2.ContainerJSONPatch {
	if in == nil {
		return nil
	}
	return &v2.ContainerJSONPatch{
		Op:    in.Op,
		Path:  in.Path,
		Value: *in.Value.DeepCopy(),
	}
}

func convertStatusV1alpha1ToV2(src *RoleBasedGroupStatus, dst *v2.RoleBasedGroupStatus) {
	dst.ObservedGeneration = src.ObservedGeneration
	dst.ObservedRolloutRequest = src.ObservedRolloutRequest
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
	// Copy instead of aliasing: the conversion below may add compatibility
	// annotations, and the hub object from the apiserver must never be mutated.
	dst.Annotations = copyAnnotations(src.Annotations)
	dst.Replicas = src.Replicas
	dst.RestartPolicy = RestartPolicyType(src.GetRawRestartPolicyType())
	dst.Dependencies = src.Dependencies
	dst.ServicePorts = src.ServicePorts
	dst.MinReadySeconds = src.MinReadySeconds
	dst.ScalingAdapter = convertScalingAdapterV2ToV1alpha1(src.ScalingAdapter)
	dst.EngineRuntimes = convertEngineRuntimesV2ToV1alpha1(src.EngineRuntimes)

	// Workload – restore from annotation if present, otherwise default from the
	// v1alpha2 role so v1alpha1 always has workload apiVersion/kind populated.
	workloadSet := false
	if src.Annotations != nil {
		workloadType := src.Annotations[constants.RoleWorkloadTypeAnnotationKey]
		if workloadType != "" {
			// Parse "apiVersion/kind" format. Use LastIndex because apiVersion
			// itself contains "/" (e.g. "leaderworkerset.x-k8s.io/v1").
			idx := strings.LastIndex(workloadType, "/")
			if idx > 0 {
				dst.Workload.APIVersion = workloadType[:idx]
				dst.Workload.Kind = workloadType[idx+1:]
				workloadSet = true
			}
		}
	}
	if !workloadSet {
		ws := src.GetWorkloadSpec()
		dst.Workload.APIVersion = ws.APIVersion
		dst.Workload.Kind = ws.Kind
	}

	// RolloutStrategy
	if src.RolloutStrategy != nil {
		dst.RolloutStrategy = convertRolloutStrategyV2ToV1alpha1(src.RolloutStrategy)
	}

	// Pattern → v1alpha1 fields
	switch {
	case src.LeaderWorkerPattern != nil:
		lwp := src.LeaderWorkerPattern
		dst.LeaderWorkerSet = &LeaderWorkerTemplate{
			Size:                lwp.Size,
			PatchLeaderTemplate: lwp.LeaderTemplatePatch,
			PatchWorkerTemplate: lwp.WorkerTemplatePatch,
		}
		dst.TemplateSource, dst.TemplatePatch = convertTemplateSrcV2ToV1alpha1(&lwp.TemplateSource)

	case src.CustomComponentsPattern != nil:
		ccp := src.CustomComponentsPattern
		dst.Components = make([]InstanceComponent, len(ccp.Components))
		for i, c := range ccp.Components {
			dst.Components[i] = InstanceComponent{
				Name:        c.Name,
				Size:        c.Size,
				ServiceName: c.ServiceName,
				Template:    *c.Template.DeepCopy(),
			}
		}

	case src.StandalonePattern != nil:
		dst.TemplateSource, dst.TemplatePatch = convertTemplateSrcV2ToV1alpha1(&src.StandalonePattern.TemplateSource)

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
			Type:           convertUpdateStrategyTypeV2ToV1alpha1(src.RollingUpdate.Type),
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
			Component:        er.Component,
			TargetContainers: convertEngineRuntimeTargetContainersV2ToV1alpha1(er.TargetContainers),
			InjectContainers: er.InjectContainers,
			Containers:       er.Containers,
		}
	}
	return dst
}

func convertEngineRuntimeTargetContainersV2ToV1alpha1(
	in []v2.EngineRuntimeTargetContainer,
) []EngineRuntimeTargetContainer {
	if in == nil {
		return nil
	}
	dst := make([]EngineRuntimeTargetContainer, len(in))
	for i, tc := range in {
		dst[i] = EngineRuntimeTargetContainer{Name: tc.Name}
		dst[i].ExtraPatches = convertContainerPatchesV2ToV1alpha1(tc.ExtraPatches)
	}
	return dst
}

func convertContainerPatchesV2ToV1alpha1(in []v2.ContainerPatch) []ContainerPatch {
	if in == nil {
		return nil
	}
	dst := make([]ContainerPatch, len(in))
	for i, p := range in {
		dst[i].JSONPatch = convertContainerJSONPatchV2ToV1alpha1(p.JSONPatch)
	}
	return dst
}

func convertContainerJSONPatchV2ToV1alpha1(in *v2.ContainerJSONPatch) *ContainerJSONPatch {
	if in == nil {
		return nil
	}
	return &ContainerJSONPatch{
		Op:    in.Op,
		Path:  in.Path,
		Value: *in.Value.DeepCopy(),
	}
}

func convertStatusV2ToV1alpha1(src *v2.RoleBasedGroupStatus, dst *RoleBasedGroupStatus) {
	dst.ObservedGeneration = src.ObservedGeneration
	dst.ObservedRolloutRequest = src.ObservedRolloutRequest
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
//  3. Continuing to treat the preservation annotation as the source of truth until
//     v1alpha1 support is fully removed, at which point both the annotation and
//     its associated migration logic can be safely deleted.
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

// copyAnnotations returns a shallow copy of the given annotation map. An
// empty (or nil) source maps to nil so a no-op round trip stays lossless
// in memory as well as on the wire.
func copyAnnotations(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// v2RoleProvenanceMarker is the JSON the conversion annotation carries.
type v2RoleProvenanceMarker struct {
	// Config is the role's raw v1alpha2 restart policy config, nil when the
	// role sets none.
	Config *v2.RestartPolicyConfig `json:"config,omitempty"`
	// LegacyType is the deprecated pattern-level restartPolicy string the
	// role carries alongside the config, empty when the role sets none.
	LegacyType v2.RestartPolicyType `json:"legacyType,omitempty"`
	// WorkloadType is the role's raw role-workload-type annotation, nil when
	// the role carries none; an explicitly empty value is preserved.
	WorkloadType *string `json:"workloadType,omitempty"`

	// The v1alpha2-only role fields v1alpha1 has no representation for. They
	// cannot be edited through v1alpha1, so a round trip restores them as read.
	PodManagementPolicy    constants.PodManagementPolicyType `json:"podManagementPolicy,omitempty"`
	SharedServiceSelection *v2.SharedServiceSelectionPolicy  `json:"sharedServiceSelection,omitempty"`
	ScalingAdapterLabels   map[string]string                 `json:"scalingAdapterLabels,omitempty"`
	// Components carries each custom component's labels and annotations,
	// keyed by component name.
	Components map[string]v2ComponentMetadata `json:"components,omitempty"`
}

// v2ComponentMetadata is a custom component's v1alpha2-only metadata.
type v2ComponentMetadata struct {
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// emittedRestartPolicyType derives the wire RestartPolicy value ConvertFrom
// emitted from the preserved raw fields. Deriving it prevents a hand-edited
// marker from claiming an emitted type that disagrees with its config and
// legacy string.
func emittedRestartPolicyType(marker v2RoleProvenanceMarker) v2.RestartPolicyType {
	if marker.Config != nil && marker.Config.Type != "" {
		return marker.Config.Type
	}
	return marker.LegacyType
}

// emittedWorkloadType returns the workload ConvertFrom emitted for a role whose
// raw role-workload-type annotation is raw: the annotation when it parses as
// apiVersion/kind, otherwise the RoleInstanceSet default.
func emittedWorkloadType(raw string) string {
	if strings.LastIndex(raw, "/") > 0 {
		return raw
	}
	return constants.RoleInstanceSetWorkloadType
}

// preserveV2RoleProvenanceMarkers marks the v1alpha1 wire object as read from the
// hub, so that ConvertTo can tell a read-and-write round trip from a fresh
// v1alpha1 write, and carries the role's restart policy provenance in the
// markers: v1alpha1 can express only the policy type, so the config's delays and
// activateAfter, and the deprecated pattern-level string, would otherwise be
// dropped or folded by a round trip (#580). Every role is marked.
func preserveV2RoleProvenanceMarkers(src *v2.RoleBasedGroup, dst *RoleBasedGroup) error {
	markers := make(map[string]v2RoleProvenanceMarker, len(src.Spec.Roles))
	for i := range src.Spec.Roles {
		role := &src.Spec.Roles[i]
		marker := v2RoleProvenanceMarker{
			Config:              role.GetRawRestartPolicyConfig(),
			PodManagementPolicy: role.PodManagementPolicy,
		}
		if wt, ok := role.Annotations[constants.RoleWorkloadTypeAnnotationKey]; ok {
			marker.WorkloadType = &wt
		}
		if role.ScalingAdapter != nil {
			marker.ScalingAdapterLabels = role.ScalingAdapter.Labels
		}
		if lwp := role.LeaderWorkerPattern; lwp != nil {
			marker.LegacyType = lwp.RestartPolicy //nolint:staticcheck // the deprecated string is part of the round-trip provenance
			marker.SharedServiceSelection = lwp.SharedServiceSelection
		} else if ccp := role.CustomComponentsPattern; ccp != nil {
			marker.LegacyType = ccp.RestartPolicy //nolint:staticcheck // the deprecated string is part of the round-trip provenance
			for _, c := range ccp.Components {
				if len(c.Labels) == 0 && len(c.Annotations) == 0 {
					continue
				}
				if marker.Components == nil {
					marker.Components = make(map[string]v2ComponentMetadata)
				}
				marker.Components[c.Name] = v2ComponentMetadata{Labels: c.Labels, Annotations: c.Annotations}
			}
		}
		markers[role.Name] = marker
	}

	data, err := json.Marshal(markers)
	if err != nil {
		return fmt.Errorf("marshalling role provenance markers: %w", err)
	}
	if dst.Annotations == nil {
		dst.Annotations = make(map[string]string, 1)
	}
	dst.Annotations[annotationV2RoleProvenance] = string(data)
	return nil
}

func restoreV2RoleProvenanceMarkers(annotations map[string]string) (map[string]v2RoleProvenanceMarker, bool, error) {
	var markers map[string]v2RoleProvenanceMarker
	raw, ok := annotations[annotationV2RoleProvenance]
	if !ok {
		return markers, false, nil
	}
	if raw == "" {
		return markers, true, nil
	}
	if raw == oversizedProvenanceMarker {
		return markers, true, fmt.Errorf("the v1alpha2-only role fields of this object do not fit in a v1alpha1 round trip; update it through workloads.x-k8s.io/v1alpha2")
	}
	if err := json.Unmarshal([]byte(raw), &markers); err != nil {
		return markers, true, fmt.Errorf("unmarshalling preserved role provenance markers: %w", err)
	}
	return markers, true, nil
}

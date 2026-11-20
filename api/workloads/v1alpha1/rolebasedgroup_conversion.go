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

	"k8s.io/apimachinery/pkg/runtime"
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

	// annotationV2RestartPolicyConfig marks every role of a v1alpha1 wire
	// object read from the hub and stores the role's serialized v1alpha2
	// RestartPolicyConfig (empty when it sets none). v1alpha1 can express only
	// the restart policy type: the delays and activateAfter are preserved
	// through this annotation, and its presence makes a v1alpha1 write a round
	// trip rather than a fresh write. It is consumed and stripped when the
	// object converts back to the hub, and never persists on a stored object.
	annotationV2RestartPolicyConfig = constants.ConversionV2RestartPolicyConfigAnnotationKey
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
	// Deep-copy annotations to avoid mutating the apiserver-provided source object
	// when writing conversion-only keys (e.g. RoleWorkloadTypeAnnotationKey).
	dst.Annotations = copyAnnotations(src.Annotations)
	dst.Replicas = src.Replicas
	dst.Dependencies = src.Dependencies
	dst.ServicePorts = src.ServicePorts
	dst.MinReadySeconds = src.MinReadySeconds
	dst.ScalingAdapter = convertScalingAdapterV1alpha1ToV2(src.ScalingAdapter)
	dst.EngineRuntimes = convertEngineRuntimesV1alpha1ToV2(src.EngineRuntimes)

	// ConvertFrom marks every role it emits (see preserveV2RestartPolicyConfig),
	// so the marker tells a read-and-write round trip from a fresh v1alpha1
	// write: a manifest applied with `kubectl apply` or `kubectl replace -f`,
	// or an object a client builds itself. A fresh write must keep the mapping
	// that v0.8.0 and later stored, or the next apply of an unchanged manifest
	// rewrites, and rolls, every object those releases stored. A round trip
	// must restore what v1alpha1 cannot express, and nothing else (#580).
	// The marker is consumed here and never persists on the hub object.
	marker, roundTrip, err := restoreV2RestartPolicyConfig(dst.Annotations)
	if err != nil {
		return err
	}
	if roundTrip {
		delete(dst.Annotations, annotationV2RestartPolicyConfig)
	}

	// Workload – preserved via role annotation instead of field in v1alpha2.
	// This annotation is for conversion compatibility only; it is not propagated
	// to downstream workloads (filtered out by GetCommonAnnotationsFromRole).
	// New v1alpha2 RBGs should NOT set this annotation.
	//
	// The workload field is authoritative. The one exception keeps a round
	// trip lossless: a role read without the annotation, whose workload still
	// names the default that ConvertFrom filled in, stays without it.
	if src.Workload.APIVersion != "" && src.Workload.Kind != "" {
		workloadType := fmt.Sprintf("%s/%s", src.Workload.APIVersion, src.Workload.Kind)
		unannotatedDefault := roundTrip &&
			dst.Annotations[constants.RoleWorkloadTypeAnnotationKey] == "" &&
			workloadType == constants.RoleInstanceSetWorkloadType
		if !unannotatedDefault {
			if dst.Annotations == nil {
				dst.Annotations = make(map[string]string)
			}
			dst.Annotations[constants.RoleWorkloadTypeAnnotationKey] = workloadType
		}
	}
	if len(dst.Annotations) == 0 {
		// A role that carried only the marker must not round trip to an
		// empty-but-present annotations map.
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
	case wireRestartPolicy != marker.EmittedType:
		// A deliberate edit of the type, including clearing it.
		restartPolicyConfig = marker.Config
		if restartPolicyConfig == nil {
			restartPolicyConfig = &v2.RestartPolicyConfig{}
		}
		restartPolicyConfig.Type = wireRestartPolicy
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

	return nil
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
	// Copy instead of aliasing: the v1alpha1 wire object below carries the
	// conversion-only round-trip marker, and the hub object from the
	// apiserver must never be mutated.
	dst.Annotations = copyAnnotations(src.Annotations)
	dst.Replicas = src.Replicas
	dst.RestartPolicy = RestartPolicyType(src.GetRawRestartPolicyType())
	if err := preserveV2RestartPolicyConfig(src, dst); err != nil {
		return err
	}
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

// v2RestartPolicyMarker is the JSON the conversion annotation carries.
type v2RestartPolicyMarker struct {
	// Config is the role's raw v1alpha2 restart policy config, nil when the
	// role sets none.
	Config *v2.RestartPolicyConfig `json:"config,omitempty"`
	// LegacyType is the deprecated pattern-level restartPolicy string the
	// role carries alongside the config, empty when the role sets none.
	LegacyType v2.RestartPolicyType `json:"legacyType,omitempty"`
	// EmittedType is the wire RestartPolicy value ConvertFrom emitted for
	// this role — the effective type folded from the config or the legacy
	// string. ConvertTo compares the wire value against it to tell a
	// deliberate type edit from an unchanged round trip.
	EmittedType v2.RestartPolicyType `json:"emittedType,omitempty"`
}

// preserveV2RestartPolicyConfig marks the v1alpha1 wire role as read from the
// hub, so that ConvertTo can tell a read-and-write round trip from a fresh
// v1alpha1 write, and carries the role's restart policy provenance in the
// marker: v1alpha1 can express only the policy type, so the config's delays and
// activateAfter, and the deprecated pattern-level string, would otherwise be
// dropped or folded by a round trip (#580). Every role is marked, so a stale
// value copied from the hub's own annotations is always overwritten.
func preserveV2RestartPolicyConfig(src *v2.RoleSpec, dst *RoleSpec) error {
	marker := v2RestartPolicyMarker{
		Config:      src.GetRawRestartPolicyConfig(),
		EmittedType: src.GetRawRestartPolicyType(),
	}
	if lwp := src.LeaderWorkerPattern; lwp != nil {
		marker.LegacyType = lwp.RestartPolicy //nolint:staticcheck // the deprecated string is part of the round-trip provenance
	} else if ccp := src.CustomComponentsPattern; ccp != nil {
		marker.LegacyType = ccp.RestartPolicy //nolint:staticcheck // the deprecated string is part of the round-trip provenance
	}
	value := ""
	if marker != (v2RestartPolicyMarker{}) {
		data, err := json.Marshal(marker)
		if err != nil {
			// The marker is plain scalars so this cannot fail in practice, but a
			// dropped marker would corrupt the round trip; fail the conversion.
			return fmt.Errorf("marshalling restart policy marker for role %q: %w", src.Name, err)
		}
		value = string(data)
	}
	if dst.Annotations == nil {
		dst.Annotations = make(map[string]string, 1)
	}
	dst.Annotations[annotationV2RestartPolicyConfig] = value
	return nil
}

// restoreV2RestartPolicyConfig returns the restart policy configuration
// preserved by preserveV2RestartPolicyConfig. found reports whether the marker
// is present at all, which makes the write a round trip (an empty value means
// the role set no config); err is non-nil when the marker is present but does
// not unmarshal into the config.
// restoreV2RestartPolicyConfig returns the restart policy provenance
// preserved by preserveV2RestartPolicyConfig. found reports whether the marker
// is present at all, which makes the write a round trip; err is non-nil when
// the marker is present but does not unmarshal, or fails the semantic
// validation the v1alpha2 CRD applies to a config written through v1alpha2.
func restoreV2RestartPolicyConfig(annotations map[string]string) (v2RestartPolicyMarker, bool, error) {
	var marker v2RestartPolicyMarker
	raw, ok := annotations[annotationV2RestartPolicyConfig]
	if !ok {
		return marker, false, nil
	}
	if raw == "" {
		return marker, true, nil
	}
	if err := json.Unmarshal([]byte(raw), &marker); err != nil {
		return marker, true, fmt.Errorf("unmarshalling preserved restart policy marker: %w", err)
	}
	if err := validateV2RestartPolicyMarker(marker); err != nil {
		return marker, true, err
	}
	return marker, true, nil
}

// validateV2RestartPolicyMarker validates a restored marker the way the
// v1alpha2 schema validates a restart policy config: the type and
// activateAfter enums, non-negative delays, and maxDelaySeconds >=
// baseDelaySeconds. A hand-edited marker that would not validate through the
// v1alpha2 API must not be stored through v1alpha1 either.
func validateV2RestartPolicyMarker(marker v2RestartPolicyMarker) error {
	for name, value := range map[string]v2.RestartPolicyType{
		"legacyType":  marker.LegacyType,
		"emittedType": marker.EmittedType,
	} {
		switch value {
		case "", v2.RestartPolicyNone, v2.RecreateRoleInstanceOnPodRestart:
		default:
			return fmt.Errorf("restart policy marker %s %q is not a valid restart policy type", name, value)
		}
	}
	if marker.Config == nil {
		return nil
	}
	config := marker.Config
	switch config.Type {
	case "", v2.RestartPolicyNone, v2.RecreateRoleInstanceOnPodRestart:
	default:
		return fmt.Errorf("restart policy config type %q is not a valid restart policy type", config.Type)
	}
	switch config.ActivateAfter {
	case "", v2.RestartPolicyActivateAfterReady, v2.RestartPolicyActivateAfterCreated:
	default:
		return fmt.Errorf("restart policy config activateAfter %q is not a valid activateAfter", config.ActivateAfter)
	}
	if config.BaseDelaySeconds != nil && *config.BaseDelaySeconds < 0 {
		return fmt.Errorf("restart policy config baseDelaySeconds %d is negative", *config.BaseDelaySeconds)
	}
	if config.MaxDelaySeconds != nil && *config.MaxDelaySeconds < 0 {
		return fmt.Errorf("restart policy config maxDelaySeconds %d is negative", *config.MaxDelaySeconds)
	}
	if config.BaseDelaySeconds != nil && config.MaxDelaySeconds != nil &&
		*config.MaxDelaySeconds < *config.BaseDelaySeconds {
		return fmt.Errorf("restart policy config maxDelaySeconds %d is less than baseDelaySeconds %d",
			*config.MaxDelaySeconds, *config.BaseDelaySeconds)
	}
	return nil
}

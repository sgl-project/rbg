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

	"sigs.k8s.io/controller-runtime/pkg/conversion"

	v2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const (
	// annotationV1alpha2RolloutStrategy stores the serialized v1alpha2 spec.rolloutStrategy on
	// the v1alpha1 object, so a full-object write through v1alpha1 round-trips it back to the
	// hub object instead of silently reverting the set to the legacy static path.
	annotationV1alpha2RolloutStrategy = "conversion.workloads.x-k8s.io/v1alpha2-rollout-strategy"

	// annotationV1alpha2RolloutStatus stores the v1alpha2-only rollout status fields on the
	// v1alpha1 object for the same round trip.
	annotationV1alpha2RolloutStatus = "conversion.workloads.x-k8s.io/v1alpha2-rollout-status"
)

// groupSetRolloutStatusFields are the RoleBasedGroupSet status fields that exist only in
// v1alpha2 and therefore cannot live on the v1alpha1 object itself.
type groupSetRolloutStatusFields struct {
	CurrentReplicas         int32  `json:"currentReplicas,omitempty"`
	UpdatedReplicas         int32  `json:"updatedReplicas,omitempty"`
	UpdatedReadyReplicas    int32  `json:"updatedReadyReplicas,omitempty"`
	ExpectedUpdatedReplicas int32  `json:"expectedUpdatedReplicas,omitempty"`
	CurrentRevision         string `json:"currentRevision,omitempty"`
	UpdateRevision          string `json:"updateRevision,omitempty"`
}

func groupSetRolloutStatusFromV2(status *v2.RoleBasedGroupSetStatus) groupSetRolloutStatusFields {
	return groupSetRolloutStatusFields{
		CurrentReplicas:         status.CurrentReplicas,
		UpdatedReplicas:         status.UpdatedReplicas,
		UpdatedReadyReplicas:    status.UpdatedReadyReplicas,
		ExpectedUpdatedReplicas: status.ExpectedUpdatedReplicas,
		CurrentRevision:         status.CurrentRevision,
		UpdateRevision:          status.UpdateRevision,
	}
}

// ConvertTo converts this RoleBasedGroupSet (v1alpha1) to the Hub version (v1alpha2).
func (src *RoleBasedGroupSet) ConvertTo(dstRaw conversion.Hub) error {
	dst, ok := dstRaw.(*v2.RoleBasedGroupSet)
	if !ok {
		return fmt.Errorf("expected *v1alpha2.RoleBasedGroupSet, got %T", dstRaw)
	}

	dst.ObjectMeta = src.ObjectMeta
	dst.Spec.Replicas = src.Spec.Replicas

	if err := convertSpecV1alpha1ToV2(&src.Spec.Template, &dst.Spec.GroupTemplate.Spec); err != nil {
		return err
	}

	// Preserve lossy v1alpha1 fields (PodGroupPolicy, CoordinationRequirements) into
	// dst.Annotations. dst.Annotations is already populated from dst.ObjectMeta = src.ObjectMeta,
	// so we pass it directly as the destination annotation map.
	// A synthetic RoleBasedGroup wrapper is used because preserveV1alpha1Fields operates on
	// RoleBasedGroup types; only the Spec fields are read, so Annotations on the wrapper are unused.
	syntheticSrc := &RoleBasedGroup{Spec: src.Spec.Template}
	syntheticDst := &v2.RoleBasedGroup{}
	syntheticDst.Annotations = copyAnnotations(dst.Annotations)
	if err := preserveV1alpha1Fields(syntheticSrc, syntheticDst); err != nil {
		return err
	}
	dst.Annotations = syntheticDst.Annotations

	// Also copy the coordination annotation into GroupTemplate.Annotations so that
	// newRBGForSet (which copies Spec.GroupTemplate.Annotations) propagates it to
	// each child RoleBasedGroup, making them visible to EnsureV1alpha1CoordinatedPolicy.
	if v, ok := dst.Annotations[annotationV1alpha1Coordination]; ok && v != "" {
		if dst.Spec.GroupTemplate.Annotations == nil {
			dst.Spec.GroupTemplate.Annotations = make(map[string]string)
		}
		dst.Spec.GroupTemplate.Annotations[annotationV1alpha1Coordination] = v
	}
	dst.Status = v2.RoleBasedGroupSetStatus{
		ObservedGeneration: src.Status.ObservedGeneration,
		Replicas:           src.Status.Replicas,
		ReadyReplicas:      src.Status.ReadyReplicas,
		Conditions:         src.Status.Conditions,
	}

	if raw := src.Annotations[annotationV1alpha2RolloutStrategy]; raw != "" {
		strategy := &v2.GroupSetRolloutStrategy{}
		if err := json.Unmarshal([]byte(raw), strategy); err != nil {
			return fmt.Errorf("annotation %s: %w", annotationV1alpha2RolloutStrategy, err)
		}
		dst.Spec.RolloutStrategy = strategy
	}
	if raw := src.Annotations[annotationV1alpha2RolloutStatus]; raw != "" {
		extras := &groupSetRolloutStatusFields{}
		if err := json.Unmarshal([]byte(raw), extras); err != nil {
			return fmt.Errorf("annotation %s: %w", annotationV1alpha2RolloutStatus, err)
		}
		dst.Status.CurrentReplicas = extras.CurrentReplicas
		dst.Status.UpdatedReplicas = extras.UpdatedReplicas
		dst.Status.UpdatedReadyReplicas = extras.UpdatedReadyReplicas
		dst.Status.ExpectedUpdatedReplicas = extras.ExpectedUpdatedReplicas
		dst.Status.CurrentRevision = extras.CurrentRevision
		dst.Status.UpdateRevision = extras.UpdateRevision
	}
	// The stash exists only on the v1alpha1 view; the hub object keeps these values in their
	// native fields, so the conversion-only keys must not leak into storage.
	delete(dst.Annotations, annotationV1alpha2RolloutStrategy)
	delete(dst.Annotations, annotationV1alpha2RolloutStatus)

	return nil
}

// ConvertFrom converts from the Hub version (v1alpha2) back to this RoleBasedGroupSet (v1alpha1).
func (dst *RoleBasedGroupSet) ConvertFrom(srcRaw conversion.Hub) error {
	src, ok := srcRaw.(*v2.RoleBasedGroupSet)
	if !ok {
		return fmt.Errorf("expected *v1alpha2.RoleBasedGroupSet, got %T", srcRaw)
	}

	dst.ObjectMeta = src.ObjectMeta
	dst.Spec.Replicas = src.Spec.Replicas

	if err := convertSpecV2ToV1alpha1(&src.Spec.GroupTemplate.Spec, &dst.Spec.Template); err != nil {
		return err
	}

	// Restore lossy fields.
	syntheticSrc := &v2.RoleBasedGroup{Spec: src.Spec.GroupTemplate.Spec}
	syntheticSrc.Annotations = src.Annotations
	syntheticDst := &RoleBasedGroup{Spec: dst.Spec.Template}
	syntheticDst.Annotations = dst.Annotations
	if err := restoreV1alpha1Fields(syntheticSrc, syntheticDst); err != nil {
		return err
	}
	dst.Spec.Template = syntheticDst.Spec

	removeConversionAnnotations(dst.Annotations)

	// Status
	dst.Status = RoleBasedGroupSetStatus{
		ObservedGeneration: src.Status.ObservedGeneration,
		Replicas:           src.Status.Replicas,
		ReadyReplicas:      src.Status.ReadyReplicas,
		Conditions:         src.Status.Conditions,
	}

	// Stash v1alpha2-only fields on the v1alpha1 view, so a full-object write through
	// v1alpha1 carries them back instead of dropping them. removeConversionAnnotations only
	// strips v1alpha1 stash keys, so these must be added after it; clear any stale copies first.
	delete(dst.Annotations, annotationV1alpha2RolloutStrategy)
	delete(dst.Annotations, annotationV1alpha2RolloutStatus)
	if src.Spec.RolloutStrategy != nil {
		data, err := json.Marshal(src.Spec.RolloutStrategy)
		if err != nil {
			return err
		}
		if dst.Annotations == nil {
			dst.Annotations = make(map[string]string)
		}
		dst.Annotations[annotationV1alpha2RolloutStrategy] = string(data)
	}
	if extras := groupSetRolloutStatusFromV2(&src.Status); extras != (groupSetRolloutStatusFields{}) {
		data, err := json.Marshal(extras)
		if err != nil {
			return err
		}
		if dst.Annotations == nil {
			dst.Annotations = make(map[string]string)
		}
		dst.Annotations[annotationV1alpha2RolloutStatus] = string(data)
	}

	return nil
}

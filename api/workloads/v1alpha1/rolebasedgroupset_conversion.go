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
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/conversion"

	v2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

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

	return nil
}

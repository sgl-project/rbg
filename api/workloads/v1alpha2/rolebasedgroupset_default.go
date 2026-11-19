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

package v1alpha2

import (
	"context"
	"encoding/json"
	"fmt"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// RoleBasedGroupSetDefaulter normalizes legacy update-strategy type values in
// the GroupTemplate of RoleBasedGroupSet writes. The controller already normalizes
// before copying the template into child RBGs, but that only heals the children:
// this defaulter heals the stored template itself, so the set stays writable and
// the template stops diverging from the normalized children it produces.
// It also defaults an empty group-set strategy type without enabling rolling updates
// for sets whose rolloutStrategy is unset.
//
// +kubebuilder:object:generate=false
type RoleBasedGroupSetDefaulter struct{}

var _ admission.CustomDefaulter = &RoleBasedGroupSetDefaulter{}

// Default implements admission.CustomDefaulter.
func (d *RoleBasedGroupSetDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	rbgset, ok := obj.(*RoleBasedGroupSet)
	if !ok {
		return fmt.Errorf("expected *RoleBasedGroupSet but got %T", obj)
	}
	// KEP-576: default only CREATE (decided from the admission operation), and
	// only inside the template that child RBGs inherit their annotations from.
	// A PUT that rebuilds the set and omits the template key carries the stored
	// value forward instead of rolling the set and all of its children back to
	// v1alpha1. A template with deprecated stateful roles cannot serialize
	// compact content and defaults to v1alpha1 so the set controller can still
	// recreate its children.
	if isCreateAdmission(ctx) {
		template := &rbgset.Spec.GroupTemplate
		if template.Annotations == nil {
			template.Annotations = make(map[string]string, 1)
		}
		if template.Annotations[constants.DiscoveryConfigVersionAnnotationKey] == "" {
			version := constants.DiscoveryConfigVersionV1
			if hasDeprecatedStatefulRole(rbgset.Spec.GroupTemplate.Spec.Roles) {
				version = constants.DiscoveryConfigVersionV1Alpha1
			}
			template.Annotations[constants.DiscoveryConfigVersionAnnotationKey] = string(version)
		}
	} else if template := &rbgset.Spec.GroupTemplate; template.Annotations[constants.DiscoveryConfigVersionAnnotationKey] == "" {
		if version := storedRoleBasedGroupSetDiscoveryConfigVersion(ctx); version != "" {
			if template.Annotations == nil {
				template.Annotations = make(map[string]string, 1)
			}
			template.Annotations[constants.DiscoveryConfigVersionAnnotationKey] = version
		}
	}
	if strategy := rbgset.Spec.RolloutStrategy; strategy != nil && strategy.Type == "" {
		strategy.Type = InPlaceUpdateStrategyType
	}
	for i := range rbgset.Spec.GroupTemplate.Spec.Roles {
		ru := rbgset.Spec.GroupTemplate.Spec.Roles[i].RolloutStrategy
		if ru == nil || ru.RollingUpdate == nil {
			continue
		}
		ru.RollingUpdate.Type = NormalizeUpdateStrategyType(ru.RollingUpdate.Type)
	}
	// Keep the engine-runtime reference marker selectable on the wire: the
	// ClusterEngineRuntimeProfile admission webhook scopes its live
	// reference lookup to this label (#597).
	SetRoleBasedGroupSetHasEngineRuntimesLabel(rbgset)
	return nil
}

func storedRoleBasedGroupSetDiscoveryConfigVersion(ctx context.Context) string {
	request, err := admission.RequestFromContext(ctx)
	if err != nil || request.Operation != admissionv1.Update || len(request.OldObject.Raw) == 0 {
		return ""
	}
	old := &RoleBasedGroupSet{}
	if err := json.Unmarshal(request.OldObject.Raw, old); err != nil {
		return ""
	}
	return old.Spec.GroupTemplate.Annotations[constants.DiscoveryConfigVersionAnnotationKey]
}

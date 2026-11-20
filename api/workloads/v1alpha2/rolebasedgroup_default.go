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
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// RoleBasedGroupDefaulter normalizes legacy update-strategy type values on
// RoleBasedGroup writes. Objects stored before the rollingUpdate.type enum was
// introduced can carry "" or the v1alpha1 "Recreate" spelling; normalizing them
// here keeps such objects writable instead of failing schema validation.
//
// +kubebuilder:object:generate=false
type RoleBasedGroupDefaulter struct{}

var _ admission.CustomDefaulter = &RoleBasedGroupDefaulter{}

// Default implements admission.CustomDefaulter.
func (d *RoleBasedGroupDefaulter) Default(_ context.Context, obj runtime.Object) error {
	rbg, ok := obj.(*RoleBasedGroup)
	if !ok {
		return fmt.Errorf("expected *RoleBasedGroup but got %T", obj)
	}
	// KEP-576: default only CREATE. Existing objects without the annotation
	// keep the v1alpha1 content they were written with; a missing annotation on
	// UPDATE must never be reinterpreted as v1. Deprecated stateful backends
	// cannot serialize the compact format, so their default stays v1alpha1.
	if rbg.CreationTimestamp.IsZero() && rbg.GetDiscoveryConfigVersion() == "" {
		version := constants.DiscoveryConfigVersionV1
		if hasDeprecatedStatefulRole(rbg.Spec.Roles) {
			version = constants.DiscoveryConfigVersionV1Alpha1
		}
		rbg.SetDiscoveryConfigVersion(version)
	}
	for i := range rbg.Spec.Roles {
		ru := rbg.Spec.Roles[i].RolloutStrategy
		if ru == nil || ru.RollingUpdate == nil {
			continue
		}
		ru.RollingUpdate.Type = NormalizeUpdateStrategyType(ru.RollingUpdate.Type)
	}
	return nil
}

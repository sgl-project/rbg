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

// RoleBasedGroupDefaulter normalizes legacy update-strategy type values on
// RoleBasedGroup writes. Objects stored before the rollingUpdate.type enum was
// introduced can carry "" or the v1alpha1 "Recreate" spelling; normalizing them
// here keeps such objects writable instead of failing schema validation.
//
// +kubebuilder:object:generate=false
type RoleBasedGroupDefaulter struct{}

var _ admission.CustomDefaulter = &RoleBasedGroupDefaulter{}

// Default implements admission.CustomDefaulter.
func (d *RoleBasedGroupDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	rbg, ok := obj.(*RoleBasedGroup)
	if !ok {
		return fmt.Errorf("expected *RoleBasedGroup but got %T", obj)
	}
	// KEP-576: default only CREATE, decided from the admission operation so a
	// decoded UPDATE object can never be mistaken for a create. A PUT that
	// rebuilds the object and omits the annotation carries the stored value
	// forward instead of silently rolling a new RBG back to v1alpha1.
	// Deprecated stateful backends cannot serialize the compact format, so
	// their default stays v1alpha1.
	if isCreateAdmission(ctx) && rbg.GetDiscoveryConfigVersion() == "" {
		version := constants.DiscoveryConfigVersionV1
		if hasDeprecatedStatefulRole(rbg.Spec.Roles) {
			version = constants.DiscoveryConfigVersionV1Alpha1
		}
		rbg.SetDiscoveryConfigVersion(version)
	} else if rbg.GetDiscoveryConfigVersion() == "" {
		if version := storedRoleBasedGroupDiscoveryConfigVersion(ctx); version != "" {
			rbg.SetDiscoveryConfigVersion(version)
		}
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

// isCreateAdmission reports whether the defaulter runs for an admission CREATE.
// A context without an admission request never triggers CREATE-only defaulting.
func isCreateAdmission(ctx context.Context) bool {
	request, err := admission.RequestFromContext(ctx)
	return err == nil && request.Operation == admissionv1.Create
}

func storedRoleBasedGroupDiscoveryConfigVersion(ctx context.Context) constants.DiscoveryConfigVersion {
	request, err := admission.RequestFromContext(ctx)
	if err != nil || request.Operation != admissionv1.Update || len(request.OldObject.Raw) == 0 {
		return ""
	}
	old := &RoleBasedGroup{}
	if err := json.Unmarshal(request.OldObject.Raw, old); err != nil {
		return ""
	}
	return old.GetDiscoveryConfigVersion()
}

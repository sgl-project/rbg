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
)

// RoleBasedGroupSetDefaulter normalizes legacy update-strategy type values in
// the GroupTemplate of RoleBasedGroupSet writes. The RBGS controller copies
// these roles verbatim into child RBGs, so a legacy value stored here would
// otherwise fail validation on every generated RBG.
//
// +kubebuilder:webhook:path=/mutate-workloads-x-k8s-io-v1alpha2-rolebasedgroupset,mutating=true,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroupsets,verbs=create;update,versions=v1alpha2,name=mrolebasedgroupset.kb.io,admissionReviewVersions=v1
// +kubebuilder:object:generate=false
type RoleBasedGroupSetDefaulter struct{}

var _ admission.CustomDefaulter = &RoleBasedGroupSetDefaulter{}

// Default implements admission.CustomDefaulter.
func (d *RoleBasedGroupSetDefaulter) Default(_ context.Context, obj runtime.Object) error {
	rbgset, ok := obj.(*RoleBasedGroupSet)
	if !ok {
		return fmt.Errorf("expected *RoleBasedGroupSet but got %T", obj)
	}
	for i := range rbgset.Spec.GroupTemplate.Spec.Roles {
		ru := rbgset.Spec.GroupTemplate.Spec.Roles[i].RolloutStrategy
		if ru == nil || ru.RollingUpdate == nil {
			continue
		}
		ru.RollingUpdate.Type = NormalizeUpdateStrategyType(ru.RollingUpdate.Type)
	}
	return nil
}

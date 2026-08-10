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
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// RoleBasedGroupDefaulter implements admission.CustomDefaulter for RoleBasedGroup.
// Wired into the manager by SetupWebhookWithManager.
//
// +kubebuilder:webhook:path=/mutate-workloads-x-k8s-io-v1alpha2-rolebasedgroup,mutating=true,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroups,verbs=create;update,versions=v1alpha2,name=mrolebasedgroup.kb.io,admissionReviewVersions=v1
// +kubebuilder:object:generate=false
type RoleBasedGroupDefaulter struct{}

var _ admission.CustomDefaulter = &RoleBasedGroupDefaulter{}

// Default materializes the resolved restart policy into RestartPolicyConfig so that
// the effective value is visible on the stored object.
//
// The deprecated RestartPolicy string is deliberately left untouched: clearing it
// would fight Server-Side Apply clients that own the field, producing an endless
// apply/mutate loop.
func (d *RoleBasedGroupDefaulter) Default(_ context.Context, obj runtime.Object) error {
	rbg, ok := obj.(*RoleBasedGroup)
	if !ok {
		return fmt.Errorf("expected *RoleBasedGroup but got %T", obj)
	}
	klog.V(4).InfoS("defaulting RoleBasedGroup", "name", rbg.Name, "namespace", rbg.Namespace)

	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		resolved := role.GetRestartPolicyConfig()
		switch {
		case role.LeaderWorkerPattern != nil:
			role.LeaderWorkerPattern.RestartPolicyConfig = &resolved
		case role.CustomComponentsPattern != nil:
			role.CustomComponentsPattern.RestartPolicyConfig = &resolved
		}
	}

	return nil
}

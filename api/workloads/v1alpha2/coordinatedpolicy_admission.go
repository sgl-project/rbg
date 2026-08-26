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

// CoordinatedPolicyValidator implements admission.CustomValidator for CoordinatedPolicy.
// Wired into the manager by SetupWebhookWithManager.
//
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha2-coordinatedpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=coordinatedpolicies,verbs=create;update,versions=v1alpha2,name=vcoordinatedpolicy.kb.io,admissionReviewVersions=v1
// +kubebuilder:object:generate=false
type CoordinatedPolicyValidator struct {
	// PerRoleGangMinimumsSupported reports whether the configured scheduler can honor
	// scheduling.gang.minReplicas. Only Volcano implements the PodGroup subGroupPolicy
	// field that per-role minimums are built on.
	PerRoleGangMinimumsSupported bool
}

var _ admission.CustomValidator = &CoordinatedPolicyValidator{}

// ValidateCreate validates a CoordinatedPolicy on creation.
func (v *CoordinatedPolicyValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, v.validate(obj)
}

// ValidateUpdate validates a CoordinatedPolicy on update.
func (v *CoordinatedPolicyValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	return nil, v.validate(newObj)
}

// ValidateDelete just implements admission.CustomValidator. This verb is currently no-op.
func (v *CoordinatedPolicyValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *CoordinatedPolicyValidator) validate(obj runtime.Object) error {
	policy, ok := obj.(*CoordinatedPolicy)
	if !ok {
		return fmt.Errorf("expected *CoordinatedPolicy but got %T", obj)
	}
	klog.V(4).InfoS("validating CoordinatedPolicy", "name", policy.Name, "namespace", policy.Namespace)

	return ValidateCoordinatedPolicyGang(policy, v.PerRoleGangMinimumsSupported)
}

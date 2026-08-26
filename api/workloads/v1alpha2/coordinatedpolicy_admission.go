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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// CoordinatedPolicyValidator implements admission.CustomValidator for CoordinatedPolicy.
// Wired into the manager by SetupWebhookWithManager.
//
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha2-coordinatedpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=coordinatedpolicies,verbs=create;update,versions=v1alpha2,name=vcoordinatedpolicy.kb.io,admissionReviewVersions=v1
// +kubebuilder:object:generate=false
type CoordinatedPolicyValidator struct {
	// Reader is an uncached reader used for the RoleBasedGroup cross-read, which
	// happens before the manager's informer cache is started.
	Reader client.Reader
	// PerRoleGangMinimumsSupported reports whether the configured scheduler can honor
	// scheduling.gang.minReplicas. Only Volcano implements the PodGroup subGroupPolicy
	// field that per-role minimums are built on.
	PerRoleGangMinimumsSupported bool
}

var _ admission.CustomValidator = &CoordinatedPolicyValidator{}

// ValidateCreate validates a CoordinatedPolicy on creation.
func (v *CoordinatedPolicyValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, v.validate(ctx, obj)
}

// ValidateUpdate validates a CoordinatedPolicy on update.
func (v *CoordinatedPolicyValidator) ValidateUpdate(ctx context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	return nil, v.validate(ctx, newObj)
}

// ValidateDelete just implements admission.CustomValidator. This verb is currently no-op.
func (v *CoordinatedPolicyValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *CoordinatedPolicyValidator) validate(ctx context.Context, obj runtime.Object) error {
	policy, ok := obj.(*CoordinatedPolicy)
	if !ok {
		return fmt.Errorf("expected *CoordinatedPolicy but got %T", obj)
	}
	klog.V(4).InfoS("validating CoordinatedPolicy", "name", policy.Name, "namespace", policy.Namespace)

	// A CoordinatedPolicy targets the RBG with the same namespace/name. It may be
	// created before that RBG exists, and the read itself may fail on a missing CRD
	// or an RBAC gap. In either case the replica-bound checks are deferred to the
	// RoleBasedGroup validator rather than rejecting the policy.
	var rbg *RoleBasedGroup
	if v.Reader != nil {
		fetched := &RoleBasedGroup{}
		err := v.Reader.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}, fetched)
		switch {
		case err == nil:
			rbg = fetched
		case !apierrors.IsNotFound(err):
			klog.V(2).InfoS("validating CoordinatedPolicy without its RoleBasedGroup: read failed",
				"namespace", policy.Namespace, "name", policy.Name, "err", err)
		}
	}

	return ValidateCoordinatedPolicyGang(policy, rbg, v.PerRoleGangMinimumsSupported)
}

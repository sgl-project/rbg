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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// RoleInstanceValidator implements admission.CustomValidator for
// RoleInstance. It enforces the static KEP-527 lifecycle rules on
// spec.lifecycle — the same validation RBG, RBGSet, and RoleInstanceSet
// admission applies to the same struct — and the preDelete deletion guard: a
// Foreground or Orphan delete of a preDelete-armed RoleInstance bypasses the
// reserved finalizer (GC cascades Foreground down to the member Pods while
// the hook is still draining), so those propagation policies are rejected
// with a pointer to retry with background propagation.
type RoleInstanceValidator struct{}

var _ admission.CustomValidator = &RoleInstanceValidator{}

// SetupWebhookWithManager registers the RoleInstance deletion guard. Only
// DELETE is intercepted, so creation and updates of controller-managed
// RoleInstances never depend on webhook availability.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha2-roleinstance,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=roleinstances,verbs=delete,versions=v1alpha2,name=vroleinstance.kb.io,admissionReviewVersions=v1

func (r *RoleInstance) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&RoleInstanceValidator{}).
		Complete()
}

// ValidateCreate validates the static lifecycle rules on the snapshot the
// runtime executes. RoleInstances are normally controller-managed, but a
// reserved-key signal would overwrite controller-owned state in EnterHook, so
// this surface stays consistent with the parent resources.
func (v *RoleInstanceValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	return validateRoleInstanceSpecLifecycle(obj)
}

// ValidateUpdate validates the static lifecycle rules on the updated
// snapshot the runtime executes.
func (v *RoleInstanceValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	return validateRoleInstanceSpecLifecycle(newObj)
}

func validateRoleInstanceSpecLifecycle(obj runtime.Object) (admission.Warnings, error) {
	instance, ok := obj.(*RoleInstance)
	if !ok {
		return nil, fmt.Errorf("expected *RoleInstance but got %T", obj)
	}
	return nil, ValidateRoleInstanceLifecycle("spec.lifecycle", instance.Spec.Lifecycle, nil)
}

// ValidateDelete rejects Foreground and Orphan propagation for RoleInstances
// whose effective lifecycle configures preDelete, including RIs that are
// already being deleted: a Foreground delete re-arms the GC cascade even on
// an object already in Background deletion.
func (v *RoleInstanceValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	instance, ok := obj.(*RoleInstance)
	if !ok {
		return nil, fmt.Errorf("expected *RoleInstance but got %T", obj)
	}
	if instance.Spec.Lifecycle == nil || instance.Spec.Lifecycle.PreDelete == nil {
		return nil, nil
	}
	request, err := admission.RequestFromContext(ctx)
	if err != nil {
		// Without the admission request the DeleteOptions are unknown; fail
		// closed for armed instances rather than guess the propagation.
		return nil, fmt.Errorf("validating RoleInstance deletion: %w", err)
	}
	deleteOptions := &metav1.DeleteOptions{}
	if len(request.Options.Raw) > 0 {
		if err := json.Unmarshal(request.Options.Raw, deleteOptions); err != nil {
			return nil, fmt.Errorf("parsing DeleteOptions for RoleInstance deletion: %w", err)
		}
	}
	if orphanDependents := deleteOptions.OrphanDependents; orphanDependents != nil && *orphanDependents { //nolint:staticcheck // the deprecated field must still be rejected for old clients
		return nil, fmt.Errorf(
			"RoleInstance %s holds a pending preDelete hook: orphan propagation strips member Pod owner references while the hook drains; retry with background propagation",
			instance.Name)
	}
	switch {
	case deleteOptions.PropagationPolicy == nil,
		*deleteOptions.PropagationPolicy == metav1.DeletePropagationBackground:
		return nil, nil
	case *deleteOptions.PropagationPolicy == metav1.DeletePropagationOrphan:
		return nil, fmt.Errorf(
			"RoleInstance %s holds a pending preDelete hook: orphan propagation strips member Pod owner references while the hook drains; retry with background propagation",
			instance.Name)
	case *deleteOptions.PropagationPolicy == metav1.DeletePropagationForeground:
		return nil, fmt.Errorf(
			"RoleInstance %s holds a pending preDelete hook: foreground propagation deletes member Pods before the hook drains; retry with background propagation",
			instance.Name)
	default:
		return nil, fmt.Errorf(
			"RoleInstance %s holds a pending preDelete hook: propagation policy %q is unsupported; retry with background propagation",
			instance.Name, *deleteOptions.PropagationPolicy)
	}
}

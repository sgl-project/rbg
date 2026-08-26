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
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// RoleBasedGroupValidator implements admission.CustomValidator for RoleBasedGroup.
// Wired into the manager by SetupWebhookWithManager.
//
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha2-rolebasedgroup,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroups,verbs=create;update,versions=v1alpha2,name=vrolebasedgroup.kb.io,admissionReviewVersions=v1
// +kubebuilder:object:generate=false
type RoleBasedGroupValidator struct {
	Client client.Client
	// Reader is an uncached reader used for the CoordinatedPolicy cross-read, which
	// happens before the manager's informer cache is started.
	Reader client.Reader
	// EnableDeprecatedWorkloadTypes reports whether the deprecated workload types
	// (Deployment, StatefulSet, LeaderWorkerSet) are still accepted. When false,
	// RBGs whose roles use them are rejected.
	EnableDeprecatedWorkloadTypes bool
	// PerRoleGangMinimumsSupported reports whether the configured scheduler can honor
	// CoordinatedPolicy scheduling.gang.minReplicas.
	PerRoleGangMinimumsSupported bool
}

var _ admission.CustomValidator = &RoleBasedGroupValidator{}

// ValidateCreate validates a RoleBasedGroup on creation.
func (v *RoleBasedGroupValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	rbg, ok := obj.(*RoleBasedGroup)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroup but got %T", obj)
	}
	klog.V(4).InfoS("validating RoleBasedGroup on create", "name", rbg.Name, "namespace", rbg.Namespace)

	var allErrs []error
	if err := ValidateRoleBasedGroupName(rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := ValidateRollingUpdate(rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if !v.EnableDeprecatedWorkloadTypes {
		if err := validateNoDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles); err != nil {
			allErrs = append(allErrs, err)
		}
	}
	if err := v.validateAgainstCoordinatedPolicy(ctx, rbg); err != nil {
		allErrs = append(allErrs, err)
	}

	return nil, utilerrors.NewAggregate(allErrs)
}

// ValidateUpdate validates a RoleBasedGroup on update.
func (v *RoleBasedGroupValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldRBG, ok := oldObj.(*RoleBasedGroup)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroup but got %T", oldObj)
	}
	rbg, ok := newObj.(*RoleBasedGroup)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroup but got %T", newObj)
	}
	klog.V(4).InfoS("validating RoleBasedGroup on update", "name", rbg.Name, "namespace", rbg.Namespace)

	var allErrs []error
	if err := ValidateRollingUpdate(rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := ValidateScalingAdapterReplicas(ctx, v.Client, oldRBG, rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if !v.EnableDeprecatedWorkloadTypes {
		if err := validateNoDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles); err != nil {
			allErrs = append(allErrs, err)
		}
	}
	if err := v.validateAgainstCoordinatedPolicy(ctx, rbg); err != nil {
		allErrs = append(allErrs, err)
	}

	return nil, utilerrors.NewAggregate(allErrs)
}

// validateAgainstCoordinatedPolicy rechecks the gang strategy of the CoordinatedPolicy
// that targets this RBG. The CoordinatedPolicy webhook cannot do this alone: the policy
// may be admitted before the RBG exists, and renaming or scaling down a role afterwards
// would otherwise leave an already-admitted minReplicas permanently unsatisfiable.
//
// The read fails open. A missing CRD, an RBAC gap or an apiserver hiccup must not block
// every RBG write, and the reconcile path revalidates the same constraints before it
// builds the PodGroup.
func (v *RoleBasedGroupValidator) validateAgainstCoordinatedPolicy(ctx context.Context, rbg *RoleBasedGroup) error {
	if v.Reader == nil {
		return nil
	}
	policy := &CoordinatedPolicy{}
	if err := v.Reader.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, policy); err != nil {
		if !apierrors.IsNotFound(err) {
			klog.V(2).InfoS("skipping CoordinatedPolicy gang validation: read failed",
				"namespace", rbg.Namespace, "name", rbg.Name, "err", err)
		}
		return nil
	}
	return ValidateCoordinatedPolicyGang(policy, rbg, v.PerRoleGangMinimumsSupported)
}

// ValidateDelete just implements admission.CustomValidator. This verb is currently no-op.
func (v *RoleBasedGroupValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

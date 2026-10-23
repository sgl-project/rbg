/*
Copyright 2025 The RBG Authors.

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
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupWebhookWithManager sets up the conversion, validating and defaulting
// webhooks for RoleBasedGroup with the Manager.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha2-rolebasedgroup,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroups,verbs=create;update,versions=v1alpha2,name=vrolebasedgroup.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/mutate-workloads-x-k8s-io-v1alpha2-rolebasedgroup,mutating=true,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroups,verbs=create;update,versions=v1alpha2,name=mrolebasedgroup.kb.io,admissionReviewVersions=v1

func (r *RoleBasedGroup) SetupWebhookWithManager(
	mgr ctrl.Manager,
	enableDeprecatedWorkloadTypes bool,
) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&RoleBasedGroupValidator{
			Client:                        mgr.GetClient(),
			EnableDeprecatedWorkloadTypes: enableDeprecatedWorkloadTypes,
		}).
		WithDefaulter(&RoleBasedGroupDefaulter{}).
		Complete()
}

// SetupWebhookWithManager sets up the validating webhook for CoordinatedPolicy
// with the Manager.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha2-coordinatedpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=coordinatedpolicies,verbs=create;update,versions=v1alpha2,name=vcoordinatedpolicy.kb.io,admissionReviewVersions=v1

func (r *CoordinatedPolicy) SetupWebhookWithManager(mgr ctrl.Manager, perRoleGangMinimumsSupported bool) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&CoordinatedPolicyValidator{
			PerRoleGangMinimumsSupported: perRoleGangMinimumsSupported,
		}).
		Complete()
}

// SetupWebhookWithManager sets up the conversion, validating and defaulting
// webhooks for RoleBasedGroupSet with the Manager.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha2-rolebasedgroupset,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroupsets,verbs=create;update,versions=v1alpha2,name=vrolebasedgroupset.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/mutate-workloads-x-k8s-io-v1alpha2-rolebasedgroupset,mutating=true,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroupsets,verbs=create;update,versions=v1alpha2,name=mrolebasedgroupset.kb.io,admissionReviewVersions=v1

func (r *RoleBasedGroupSet) SetupWebhookWithManager(mgr ctrl.Manager, enableDeprecatedWorkloadTypes bool) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&RoleBasedGroupSetValidator{
			EnableDeprecatedWorkloadTypes: enableDeprecatedWorkloadTypes,
		}).
		WithDefaulter(&RoleBasedGroupSetDefaulter{}).
		Complete()
}

// SetupWebhookWithManager sets up the defaulting webhook for RoleInstanceSet
// with the Manager.
// +kubebuilder:webhook:path=/mutate-workloads-x-k8s-io-v1alpha2-roleinstanceset,mutating=true,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=roleinstancesets,verbs=create;update,versions=v1alpha2,name=mroleinstanceset.kb.io,admissionReviewVersions=v1

func (r *RoleInstanceSet) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithDefaulter(&RoleInstanceSetDefaulter{}).
		Complete()
}

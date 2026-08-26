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

// SetupWebhookWithManager sets up the conversion and validating webhooks for
// RoleBasedGroup with the Manager.
//
// The validator cross-reads CoordinatedPolicy, so it gets the uncached API reader:
// the manager starts webhooks before the informer cache, and a cached read at that
// point fails with ErrCacheNotStarted.
func (r *RoleBasedGroup) SetupWebhookWithManager(
	mgr ctrl.Manager,
	enableDeprecatedWorkloadTypes bool,
	perRoleGangMinimumsSupported bool,
) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&RoleBasedGroupValidator{
			Client:                        mgr.GetClient(),
			Reader:                        mgr.GetAPIReader(),
			EnableDeprecatedWorkloadTypes: enableDeprecatedWorkloadTypes,
			PerRoleGangMinimumsSupported:  perRoleGangMinimumsSupported,
		}).
		Complete()
}

// SetupWebhookWithManager sets up the validating webhook for CoordinatedPolicy
// with the Manager.
func (r *CoordinatedPolicy) SetupWebhookWithManager(mgr ctrl.Manager, perRoleGangMinimumsSupported bool) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&CoordinatedPolicyValidator{
			Reader:                       mgr.GetAPIReader(),
			PerRoleGangMinimumsSupported: perRoleGangMinimumsSupported,
		}).
		Complete()
}

// SetupWebhookWithManager sets up the conversion and validating webhooks for
// RoleBasedGroupSet with the Manager.
func (r *RoleBasedGroupSet) SetupWebhookWithManager(mgr ctrl.Manager, enableDeprecatedWorkloadTypes bool) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&RoleBasedGroupSetValidator{
			EnableDeprecatedWorkloadTypes: enableDeprecatedWorkloadTypes,
		}).
		Complete()
}

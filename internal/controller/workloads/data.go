/*
Copyright 2025.

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

package workloads

import (
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/dependency"
	"sigs.k8s.io/rbgs/pkg/reconciler"
)

// ReconcileData holds the data required during the reconciliation process of a RoleBasedGroup.
// It encapsulates both input (read-only) and intermediate (mutable) state used across reconciliation steps.
type ReconcileData struct {
	// readOnly
	rbg               *workloadsv1alpha1.RoleBasedGroup
	dependencyManager *dependency.DefaultDependencyManager

	// Mutable intermediate results computed and updated during reconciliation.
	expectedRolesRevisionHash map[string]string
	sortedRoles               [][]*workloadsv1alpha1.RoleSpec
	roleStatuses              []workloadsv1alpha1.RoleStatus
	// ScalingTargets            map[string]int32
	rollingUpdateStrategies map[string]workloadsv1alpha1.RollingUpdate
	// needUpdateStatus          *bool
	roleReconciler map[string]reconciler.WorkloadReconciler
}

// NewReconcileData creates and initializes a new ReconcileData instance.
// It takes a RoleBasedGroup (rbg) and a dependency manager (dm) as required inputs.
func NewReconcileData(rbg *workloadsv1alpha1.RoleBasedGroup, dm *dependency.DefaultDependencyManager) *ReconcileData {
	return &ReconcileData{
		rbg:               rbg,
		dependencyManager: dm,
	}
}

// GetRBG returns the RoleBasedGroup being reconciled.
// The returned pointer should not be mutated.
func (rd *ReconcileData) GetRBG() *workloadsv1alpha1.RoleBasedGroup {
	return rd.rbg
}

// GetDependencyManager returns the dependency manager used for external interactions.
func (rd *ReconcileData) GetDependencyManager() *dependency.DefaultDependencyManager {
	return rd.dependencyManager
}

// GetSortedRoles returns the current grouping of roles into ordered batches.
// The structure enables phased or dependency-aware reconciliation.
func (rd *ReconcileData) GetSortedRoles() [][]*workloadsv1alpha1.RoleSpec {
	return rd.sortedRoles
}

// SetSortedRoles sets the sortedRoles field, typically after dependency analysis or topological sorting.
func (rd *ReconcileData) SetSortedRoles(sortedRoles [][]*workloadsv1alpha1.RoleSpec) {
	rd.sortedRoles = sortedRoles
}

// SetRoleStatuses updates the roleStatuses slice with the latest reconciliation outcomes.
func (rd *ReconcileData) SetRoleStatuses(roleStatuses []workloadsv1alpha1.RoleStatus) {
	rd.roleStatuses = roleStatuses
}

// GetRoleStatuses returns the current list of role statuses, intended for status update.
func (rd *ReconcileData) GetRoleStatuses() []workloadsv1alpha1.RoleStatus {
	return rd.roleStatuses
}

// GetRollingUpdateStrategies returns the map of rolling update strategies keyed by role name.
func (rd *ReconcileData) GetRollingUpdateStrategies() map[string]workloadsv1alpha1.RollingUpdate {
	return rd.rollingUpdateStrategies
}

// SetRollingUpdateStrategies sets the rolling update strategies for each role.
func (rd *ReconcileData) SetRollingUpdateStrategies(rollingUpdateStrategies map[string]workloadsv1alpha1.RollingUpdate) {
	rd.rollingUpdateStrategies = rollingUpdateStrategies
}

func (rd *ReconcileData) SetExpectedRolesRevisionHash(hash map[string]string) {
	rd.expectedRolesRevisionHash = hash
}

func (rd *ReconcileData) GetExpectedRolesRevisionHash() map[string]string {
	return rd.expectedRolesRevisionHash
}

func (rd *ReconcileData) SetRoleReconcilers(reconcilers map[string]reconciler.WorkloadReconciler) {
	rd.roleReconciler = reconcilers
}

func (rd *ReconcileData) GetRoleReconcilers() map[string]reconciler.WorkloadReconciler {
	return rd.roleReconciler
}

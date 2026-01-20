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

package rbg

import (
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// RoleData holds the data for a single role in RoleBasedGroup reconciliation.
// It aggregates all role-related data to avoid scattered map lookups.
type RoleData struct {
	// Spec is the role specification from RBG.
	Spec *workloadsv1alpha1.RoleSpec

	// Status is the computed status of this role.
	Status *workloadsv1alpha1.RoleStatus

	// ExpectedRevisionHash is the expected revision hash for this role.
	ExpectedRevisionHash string

	// RollingUpdateStrategy is the calculated rolling update strategy.
	RollingUpdateStrategy *workloadsv1alpha1.RollingUpdate

	// DependencyLevel indicates the dependency order level (0 = no dependencies, higher = depends on lower levels).
	DependencyLevel int
}

// RBGReconcileState holds the state for RoleBasedGroup reconciliation.
// It serves as the input and output for each reconciliation step, enabling
// a modular and extensible reconcile flow.
type RBGReconcileState struct {
	// name is the name of the RoleBasedGroup being reconciled.
	name string

	// namespace is the namespace of the RoleBasedGroup.
	namespace string

	// --- Intermediate state computed by steps ---

	// roles is a list of RoleData sorted by dependency order.
	// All role-related data (spec, status, hash, strategy) is aggregated here.
	roles []*RoleData
}

// NewRBGReconcileData creates a new RBGReconcileState for reconciliation.
func NewRBGReconcileData(name, namespace string) *RBGReconcileState {
	return &RBGReconcileState{
		name:      name,
		namespace: namespace,
		roles:     make([]*RoleData, 0),
	}
}

// Name returns the RoleBasedGroup name.
func (s *RBGReconcileState) Name() string {
	return s.name
}

// Namespace returns the RoleBasedGroup namespace.
func (s *RBGReconcileState) Namespace() string {
	return s.namespace
}

// SetRoles sets the role data list.
func (s *RBGReconcileState) SetRoles(roles []*RoleData) {
	s.roles = roles
}

// GetRoles returns all role data.
func (s *RBGReconcileState) GetRoles() []*RoleData {
	return s.roles
}

// AddRole appends a role data to the list.
func (s *RBGReconcileState) AddRole(role *RoleData) {
	s.roles = append(s.roles, role)
}

// GetRole returns the role data by name.
func (s *RBGReconcileState) GetRole(roleName string) *RoleData {
	for _, role := range s.roles {
		if role.Spec != nil && role.Spec.Name == roleName {
			return role
		}
	}
	return nil
}

// GetRoleStatuses collects and returns all role statuses from role data.
func (s *RBGReconcileState) GetRoleStatuses() []workloadsv1alpha1.RoleStatus {
	statuses := make([]workloadsv1alpha1.RoleStatus, 0, len(s.roles))
	for _, role := range s.roles {
		if role.Status != nil {
			statuses = append(statuses, *role.Status)
		}
	}
	return statuses
}

// GetRolesByDependencyLevel returns roles grouped by their dependency level.
// Each inner slice contains roles that can be processed in parallel.
func (s *RBGReconcileState) GetRolesByDependencyLevel() [][]*RoleData {
	if len(s.roles) == 0 {
		return nil
	}

	// Find max level
	maxLevel := 0
	for _, role := range s.roles {
		if role.DependencyLevel > maxLevel {
			maxLevel = role.DependencyLevel
		}
	}

	// Group by level
	result := make([][]*RoleData, maxLevel+1)
	for i := range result {
		result[i] = make([]*RoleData, 0)
	}
	for _, role := range s.roles {
		result[role.DependencyLevel] = append(result[role.DependencyLevel], role)
	}

	return result
}

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

package reconciler

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

const (
	// EventTypeNormal is the normal event type
	EventTypeNormal = "Normal"
	// EventTypeWarning is the warning event type
	EventTypeWarning = "Warning"
)

// Metadata holds the basic metadata for RoleBasedGroup reconciliation.
// It contains only immutable identifiers, avoiding the need to pass the full RBG object.
type Metadata struct {
	// Name is the name of the RoleBasedGroup
	Name string

	// Namespace is the namespace of the RoleBasedGroup
	Namespace string

	// UID is the unique identifier of the RoleBasedGroup
	UID types.UID
}

// GetWorkloadName returns the workload name for a role.
// This is a helper method that follows the same naming convention as RBG.GetWorkloadName.
func (m Metadata) GetWorkloadName(role *workloadsv1alpha1.RoleSpec) string {
	workloadName := fmt.Sprintf("%s-%s", m.Name, role.Name)

	// Kubernetes name length is limited to 63 characters
	if len(workloadName) > 63 {
		workloadName = workloadName[:63]
		workloadName = strings.TrimRight(workloadName, "-")
	}
	return workloadName
}

// GetServiceName returns the service name for a role.
// This is a helper method that follows the same naming convention as RBG.GetServiceName.
func (m Metadata) GetServiceName(role *workloadsv1alpha1.RoleSpec) string {
	svcName := fmt.Sprintf("s-%s-%s", m.Name, role.Name)
	if len(svcName) > 63 {
		svcName = svcName[:63]
		svcName = strings.TrimRight(svcName, "-")
	}
	return svcName
}

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

	// --- Computed fields for convenience ---

	// WorkloadName is the computed workload name for this role.
	WorkloadName string

	// OwnerInfo contains the owner reference information.
	OwnerInfo OwnerInfo

	// ExclusiveTopologyKey is the exclusive topology key from RBG annotations
	ExclusiveTopologyKey string

	// GroupUniqueHash is the unique hash for the RBG group
	GroupUniqueHash string

	// PodGroupKey is the key used to identify the pod group (typically the RBG name)
	PodGroupKey string

	// PodGroupLabelKey is the label/annotation key for pod group scheduling
	// - "pod-group.scheduling.sigs.k8s.io/name" for KubeScheduling
	// - "scheduling.k8s.io/group-name" for VolcanoScheduling
	// - empty string if no pod group policy is set
	PodGroupLabelKey string

	// APIVersion and Kind for the RBG (needed for owner references)
	APIVersion string
	Kind       string
}

// OwnerInfo holds the owner reference information for creating resources.
type OwnerInfo struct {
	Name      string
	Namespace string
	UID       types.UID
}

// EventFunc is a function type for emitting Kubernetes events.
// It encapsulates event recording without exposing the underlying implementation.
type EventFunc func(eventType, reason, message string)

// RBGReconcileData holds the state for RoleBasedGroup reconciliation.
// It serves as the input and output for each reconciliation step, enabling
// a modular and extensible reconcile flow.
type RBGReconcileData struct {
	// metadata contains the basic identifiers of the RoleBasedGroup.
	metadata Metadata

	// eventFunc is the function for emitting events.
	// This is injected during initialization to avoid coupling with specific event recorder.
	eventFunc EventFunc

	// --- RBG Spec fields needed for reconciliation ---

	// podGroupPolicy defines the pod group scheduling policy
	podGroupPolicy *workloadsv1alpha1.PodGroupPolicy

	// coordinationRequirements defines the coordination requirements for multi-role updates
	coordinationRequirements []workloadsv1alpha1.Coordination

	// --- Intermediate state computed by steps ---

	// roles is a list of RoleData sorted by dependency order.
	// All role-related data (spec, status, hash, strategy) is aggregated here.
	roles []*RoleData
}

// NewRBGReconcileData creates a new RBGReconcileData for reconciliation.
func NewRBGReconcileData(rbg *workloadsv1alpha1.RoleBasedGroup, eventFunc EventFunc) *RBGReconcileData {
	return &RBGReconcileData{
		metadata: Metadata{
			Name:      rbg.Name,
			Namespace: rbg.Namespace,
			UID:       rbg.UID,
		},
		eventFunc:                eventFunc,
		podGroupPolicy:           rbg.Spec.PodGroupPolicy,
		coordinationRequirements: rbg.Spec.CoordinationRequirements,
		roles:                    make([]*RoleData, 0),
	}
}

// Metadata returns the RoleBasedGroup metadata.
func (d *RBGReconcileData) Metadata() Metadata {
	return d.metadata
}

// PodGroupPolicy returns the pod group policy.
func (d *RBGReconcileData) PodGroupPolicy() *workloadsv1alpha1.PodGroupPolicy {
	return d.podGroupPolicy
}

// IsKubeGangScheduling returns true if using Kubernetes gang scheduling.
func (d *RBGReconcileData) IsKubeGangScheduling() bool {
	if d.podGroupPolicy == nil {
		return false
	}
	return d.podGroupPolicy.KubeScheduling != nil
}

// IsVolcanoGangScheduling returns true if using Volcano gang scheduling.
func (d *RBGReconcileData) IsVolcanoGangScheduling() bool {
	if d.podGroupPolicy == nil {
		return false
	}
	return d.podGroupPolicy.VolcanoScheduling != nil
}

// GetGroupSize calculates the total number of replicas across all roles.
func (d *RBGReconcileData) GetGroupSize() int {
	var size int32 = 0
	for _, role := range d.GetRoles() {
		if role.Spec.Replicas != nil {
			size += *role.Spec.Replicas
		}
	}
	return int(size)
}

// SetRoles sets the role data list.
func (s *RBGReconcileData) SetRoles(roles []*RoleData) {
	s.roles = roles
}

// GetRoles returns all role data.
func (s *RBGReconcileData) GetRoles() []*RoleData {
	return s.roles
}

// AddRole appends a role data to the list.
func (s *RBGReconcileData) AddRole(role *RoleData) {
	s.roles = append(s.roles, role)
}

// GetRole returns the role data by name.
func (s *RBGReconcileData) GetRole(roleName string) *RoleData {
	for _, role := range s.roles {
		if role.Spec != nil && role.Spec.Name == roleName {
			return role
		}
	}
	return nil
}

// GetRoleStatuses collects and returns all role statuses from role data.
func (s *RBGReconcileData) GetRoleStatuses() []workloadsv1alpha1.RoleStatus {
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
func (s *RBGReconcileData) GetRolesByDependencyLevel() [][]*RoleData {
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

// Event emits a Kubernetes event.
// This encapsulates event recording, avoiding tight coupling with event recorder.
func (d *RBGReconcileData) Event(eventType, reason, message string) {
	if d.eventFunc != nil {
		d.eventFunc(eventType, reason, message)
	}
}

// Eventf emits a formatted Kubernetes event.
func (d *RBGReconcileData) Eventf(eventType, reason, messageFmt string, args ...interface{}) {
	if d.eventFunc != nil {
		d.eventFunc(eventType, reason, fmt.Sprintf(messageFmt, args...))
	}
}

// NormalEvent is a convenience method for emitting normal events.
func (d *RBGReconcileData) NormalEvent(reason, message string) {
	d.Event(EventTypeNormal, reason, message)
}

// WarningEvent is a convenience method for emitting warning events.
func (d *RBGReconcileData) WarningEvent(reason, message string) {
	d.Event(EventTypeWarning, reason, message)
}

// WarningEventf is a convenience method for emitting formatted warning events.
func (d *RBGReconcileData) WarningEventf(reason, messageFmt string, args ...interface{}) {
	d.Eventf(EventTypeWarning, reason, messageFmt, args...)
}

// RoleInMaxSkewCoordination checks if the role is in a max skew coordination.
func (d *RBGReconcileData) RoleInMaxSkewCoordination(roleName string) bool {
	for _, coordination := range d.coordinationRequirements {
		for _, role := range coordination.Roles {
			if role == roleName {
				if coordination.Strategy != nil &&
					coordination.Strategy.RollingUpdate != nil &&
					coordination.Strategy.RollingUpdate.MaxSkew != nil {
					return true
				}
			}
		}
	}
	return false
}

// GetRoleSpecs returns the role specifications as pointers.
func (d *RBGReconcileData) GetRoleSpecs(roleName string) int32 {
	for _, role := range d.roles {
		if role.Spec != nil && role.Spec.Name == roleName {
			return *role.Spec.Replicas
		}
	}
	return 0
}

// GetCompatibleHeadlessServiceNameFromRoleData returns the compatible headless service name from roleData.
// It checks if the old service name (workloadName) exists, otherwise uses the new service name.
func GetCompatibleHeadlessServiceNameFromRoleData(
	ctx context.Context, kclient client.Client, roleData *RoleData,
) (string, error) {
	svc := &corev1.Service{}
	err := kclient.Get(ctx, types.NamespacedName{Name: roleData.WorkloadName, Namespace: roleData.OwnerInfo.Namespace}, svc)
	if err == nil {
		// if oldService exists, use old ServiceName (workloadName)
		return roleData.WorkloadName, nil
	} else {
		// if oldService not exists, use new ServiceName
		if apierrors.IsNotFound(err) {
			metadata := Metadata{
				Name:      roleData.OwnerInfo.Name,
				Namespace: roleData.OwnerInfo.Namespace,
				UID:       roleData.OwnerInfo.UID,
			}
			return metadata.GetServiceName(roleData.Spec), nil
		}
	}
	return "", err
}

// GetCoordinationRequirements returns the coordination requirements.
func (d *RBGReconcileData) GetCoordinationRequirements() []workloadsv1alpha1.Coordination {
	return d.coordinationRequirements
}

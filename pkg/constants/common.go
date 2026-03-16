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

package constants

import (
	"strings"
)

// Unified prefix
const (
	ControllerName = "rbg-controller"
	RBGPrefix      = "rbg.workloads.x-k8s.io/"
)

// ========== Enum Types ==========

// InstancePatternType defines supported organization patterns
type InstancePatternType string

const (
	// StatelessPattern represents stateless (unordered) topology pattern
	StatelessPattern InstancePatternType = "Stateless"

	// StatefulPattern represents stateful (ordered) topology pattern
	StatefulPattern InstancePatternType = "Stateful"
)

// PodManagementPolicyType controls how pods are created during initial scale-up,
// when replacing pods on nodes, or when scaling down.
// +kubebuilder:validation:Enum={OrderedReady,Parallel}
type PodManagementPolicyType string

const (
	// OrderedReadyPodManagement creates and deletes pods in order, waiting for each
	// to be ready before creating the next one.
	OrderedReadyPodManagement PodManagementPolicyType = "OrderedReady"

	// ParallelPodManagement creates and deletes pods simultaneously without waiting
	// for pods to be ready. This is the default policy.
	ParallelPodManagement PodManagementPolicyType = "Parallel"
)

// RoleTemplateType defines supported role template types
type RoleTemplateType string

const (
	// ComponentsTemplateType represents template is constructed from role.components field
	ComponentsTemplateType RoleTemplateType = "Components"

	// LeaderWorkerSetTemplateType represents template is constructed from role.leaderWorkerSet field
	LeaderWorkerSetTemplateType RoleTemplateType = "LeaderWorkerSet"

	// PodTemplateTemplateType represents template is constructed from role.template field
	PodTemplateTemplateType RoleTemplateType = "PodTemplate"
)

// LifecycleState defines lifecycle states
type LifecycleState string

const (
	LifecycleNormal          LifecycleState = "Normal"
	LifecyclePreparingUpdate LifecycleState = "PreparingUpdate"
	LifecycleUpdating        LifecycleState = "Updating"
	LifecycleUpdated         LifecycleState = "Updated"
	LifecyclePreparingDelete LifecycleState = "PreparingDelete"
)

type ComponentType string

const (
	LeaderComponentType ComponentType = "Leader"
	WorkerComponentType ComponentType = "Worker"
)

// ========== Compatibility Helper Functions ==========

// GetLabelValue retrieves label value with backward compatibility support for old keys
func GetLabelValue(labels map[string]string, newKey, oldKey string) string {
	if v, ok := labels[newKey]; ok {
		return v
	}
	return labels[oldKey]
}

// GetAnnotationValue retrieves annotation value with backward compatibility support for old keys
func GetAnnotationValue(annotations map[string]string, newKey, oldKey string) string {
	if v, ok := annotations[newKey]; ok {
		return v
	}
	return annotations[oldKey]
}

// ========== Discovery Config Mode ==========

// AdapterPhase defines the phase of a ScalingAdapter
type AdapterPhase string

const (
	AdapterPhaseNone     AdapterPhase = ""
	AdapterPhaseNotBound AdapterPhase = "NotBound"
	AdapterPhaseBound    AdapterPhase = "Bound"
)

// DiscoveryConfigMode defines the mode for discovery config
type DiscoveryConfigMode string

const (
	// LegacyDiscoveryConfigMode uses legacy shared ConfigMap behavior
	LegacyDiscoveryConfigMode DiscoveryConfigMode = "legacy"

	// RefineDiscoveryConfigMode enables refined shared ConfigMap behavior
	RefineDiscoveryConfigMode DiscoveryConfigMode = "refine"
)

// IsStatefulRole checks if a role is stateful (uses InstanceSet/RoleInstanceSet or StatefulSet workload)
func IsStatefulRole(workloadKind string) bool {
	kind := strings.ToLower(workloadKind)
	return kind == "instanceset" || kind == "roleinstanceset" || kind == "statefulset"
}

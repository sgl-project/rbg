package constants

import v1 "k8s.io/api/core/v1"

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

// RoleTemplateType defines supported role template types
type RoleTemplateType string

const (
	// ComponentsTemplate represents template is constructed from role.components field
	ComponentsTemplate RoleTemplateType = "Components"

	// LeaderWorkerTemplate represents template is constructed from role.leaderWorkerSet field
	LeaderWorkerTemplate RoleTemplateType = "LeaderWorkerSet"

	// PodTemplateTemplate represents template is constructed from role.template field
	PodTemplateTemplate RoleTemplateType = "PodTemplate"
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

// ========== External System Constants ==========

// LeaderWorkerSet labels and annotations
const (
	LeaderWorkerSetPrefix = "leaderworkerset.sigs.k8s.io/"

	// LwsWorkerIndexLabelKey identifies the worker index in LeaderWorkerSet
	LwsWorkerIndexLabelKey = LeaderWorkerSetPrefix + "worker-index"
)

const (
	// InstancePodReadyConditionType corresponding condition status was set to "False" by multiple writers.
	InstancePodReadyConditionType v1.PodConditionType = "InstancePodReady"
)

type LwsComponentType string

const (
	LeaderLwsComponentType LwsComponentType = "Leader"
	WorkerLwsComponentType LwsComponentType = "Worker"
)

const (
	DeploymentWorkloadType      string = "apps/v1/Deployment"
	StatefulSetWorkloadType     string = "apps/v1/StatefulSet"
	InstanceSetWorkloadType     string = "workloads.x-k8s.io/v1alpha1/InstanceSet"
	LeaderWorkerSetWorkloadType string = "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet"
)

type AdapterPhase string

const (
	AdapterPhaseNone     AdapterPhase = ""
	AdapterPhaseNotBound AdapterPhase = "NotBound"
	AdapterPhaseBound    AdapterPhase = "Bound"
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

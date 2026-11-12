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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RoleInstanceSpec defines the desired state of RoleInstance
type RoleInstanceSpec struct {
	// Components is a list of components, each of which specifies a component and the number of replicas and template for RoleInstance that match the component. Component names must be unique.
	// +listType=map
	// +listMapKey=name
	Components []RoleInstanceComponent `json:"components" patchStrategy:"merge" patchMergeKey:"name"`

	// RoleInstanceReadyPolicy specifies the policy for determining if the RoleInstance is ready.
	// Defaults to `AllPodReady`
	// +kubebuilder:default=AllPodReady
	ReadyPolicy RoleInstanceReadyPolicyType `json:"readyPolicy,omitempty"`

	// RestartPolicy defines the restart policy for all pods within the RoleInstance.
	//
	// Deprecated: use RestartPolicyConfig instead. Kept as a string for wire
	// compatibility with v0.7.0, where this field carried the policy directly.
	// +optional
	// +kubebuilder:validation:Enum={None,RecreateRoleInstanceOnPodRestart}
	RestartPolicy RestartPolicyType `json:"restartPolicy,omitempty"`

	// RestartPolicyConfig defines the restart policy and backoff configuration for
	// all pods within the RoleInstance. Its type takes precedence over the
	// deprecated RestartPolicy field.
	// +optional
	RestartPolicyConfig *RestartPolicyConfig `json:"restartPolicyConfig,omitempty"`

	// ReadinessGates is an optional list of PodReadinessGates for the whole RoleInstance.
	ReadinessGates []RoleInstanceReadinessGate `json:"readinessGates,omitempty"`
}

// RoleInstanceReadinessGate contains the reference to a RoleInstance condition
type RoleInstanceReadinessGate struct {
	// ConditionType refers to a condition in the pod's condition list with matching type.
	ConditionType RoleInstanceConditionType `json:"conditionType"`
}

// GetRestartPolicyConfig returns the restart policy configuration, folding the
// deprecated RestartPolicy string in when RestartPolicyConfig carries no type.
// No default type is applied: an empty type means no instance-level restart, and
// synthesizing RecreateRoleInstanceOnPodRestart here would start recreating the
// RoleInstances of patterns that never opted in.
func (s *RoleInstanceSpec) GetRestartPolicyConfig() RestartPolicyConfig {
	if s == nil {
		return RestartPolicyConfig{}
	}
	return resolveRestartPolicyConfig(s.RestartPolicyConfig, s.RestartPolicy, "")
}

// GetRestartPolicy returns the effective restart policy type.
func (s *RoleInstanceSpec) GetRestartPolicy() RestartPolicyType {
	return s.GetRestartPolicyConfig().Type
}

// GetBaseDelaySeconds returns the configured base delay or the default (30).
func (s *RoleInstanceSpec) GetBaseDelaySeconds() int32 {
	if s == nil {
		return DefaultBaseDelaySeconds
	}
	if cfg := s.GetRestartPolicyConfig(); cfg.BaseDelaySeconds != nil {
		return *cfg.BaseDelaySeconds
	}
	return DefaultBaseDelaySeconds
}

// GetMaxDelaySeconds returns the configured max delay or the default (600).
func (s *RoleInstanceSpec) GetMaxDelaySeconds() int32 {
	if s == nil {
		return DefaultMaxDelaySeconds
	}
	if cfg := s.GetRestartPolicyConfig(); cfg.MaxDelaySeconds != nil {
		return *cfg.MaxDelaySeconds
	}
	return DefaultMaxDelaySeconds
}

type RoleInstanceReadyPolicyType string

const (
	// RoleInstanceReadyOnAllPodReady means all Pods in the RoleInstance must be ready when RoleInstance Ready
	RoleInstanceReadyOnAllPodReady RoleInstanceReadyPolicyType = "AllPodReady"

	// RoleInstanceReadyPolicyTypeNone means do nothing for Pods
	RoleInstanceReadyPolicyTypeNone RoleInstanceReadyPolicyType = "None"
)

type RoleInstanceComponent struct {
	// Name is the type name of the component.
	Name string `json:"name"`

	// Size is the number of replicas for Pods that match the PodRule.
	Size *int32 `json:"size,omitempty"`

	// ServiceName is the name of the service that governs this RoleInstance Component.
	// This service must exist before the RoleInstance, and is responsible for
	// the network identity of the set. Pods get DNS/hostnames that follow the
	// pattern: pod-specific-string.serviceName.default.svc.cluster.local
	// where "pod-specific-string" is managed by the RoleInstance controller.
	ServiceName string `json:"serviceName,omitempty"`

	// Labels are additional labels merged into every pod of this component at creation time.
	// These are copied from InstanceComponent.Labels during reconciliation and are merged
	// with (and take precedence over) any labels already present in Template.Metadata.Labels.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations are additional annotations merged into every pod of this component at
	// creation time.  These are copied from InstanceComponent.Annotations during reconciliation
	// and are merged with (and take precedence over) any annotations already present in
	// Template.Metadata.Annotations.
	// Controller-directive annotations (component-depends-on, port-allocator,
	// component-discovery) live here.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Template is the template for the component pods.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Template corev1.PodTemplateSpec `json:"template"`
}

// RoleInstanceStatus defines the observed state of RoleInstance
type RoleInstanceStatus struct {
	// ObservedGeneration is the most recent generation observed for this RoleInstance. It corresponds to the
	// RoleInstance's generation, which is updated on mutation by the API Server.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is the optional instance-level lifecycle phase. It records the stage the
	// instance has entered, is not a readiness or success verdict, is not copied by
	// parent controllers, and is not used as the lifecycle-hook protocol.
	// +optional
	Phase RoleInstancePhase `json:"phase,omitempty"`

	// Conditions track the condition of the RoleInstance
	Conditions []RoleInstanceCondition `json:"conditions,omitempty"`

	// ComponentStatuses is a list of RoleInstanceComponentStatus, each of which specifies the status of a component.
	ComponentStatuses []RoleInstanceComponentStatus `json:"componentStatuses,omitempty"`

	// LabelSelector of a RoleInstance is a label query over Pods that should match the RoleInstance.
	LabelSelector string `json:"labelSelector,omitempty"`

	// CurrentRevision is a hash value that changes when the spec is changed.
	CurrentRevision string `json:"currentRevision,omitempty"`

	// UpdateRevision is a hash value that changes when the spec is changed.
	UpdateRevision string `json:"updateRevision,omitempty"`

	// CollisionCount is the count of hash collisions for the RoleInstanceSet. The RoleInstanceSet controller
	// uses this field as a collision avoidance mechanism when it needs to create the name for the
	// newest ControllerRevision.
	CollisionCount *int32 `json:"collisionCount,omitempty"`

	// InPlaceUpdateContainerBaselines records the pre-update baseline for
	// containers that were in-place updated. Outer key is pod name, inner key
	// is container name.
	// Set by the Update flow when an in-place update is performed.
	// Baselines for deleted pods are garbage-collected when all replicas converge.
	// Baselines for existing pods are retained and naturally overwritten on next update.
	// Used by shouldRecreateInstance to distinguish expected container restarts
	// (from in-place image changes) from real crashes.
	// +optional
	InPlaceUpdateContainerBaselines map[string]map[string]ContainerUpdateBaseline `json:"inPlaceUpdateContainerBaselines,omitempty"`

	// RestartCount tracks the number of times the restart policy has triggered
	// a full RoleInstance recreation.
	// +optional
	RestartCount int32 `json:"restartCount,omitempty"`

	// LastRestartTime is the timestamp of the most recent restart-policy-triggered
	// recreation. Used to compute exponential backoff between restart attempts.
	// +optional
	LastRestartTime *metav1.Time `json:"lastRestartTime,omitempty"`

	// LastReadyGeneration is the metadata.generation at which the instance was last
	// observed Ready. It persists "the instance was stable on the spec it is running"
	// independently of the current Ready condition, which a fault flips to False
	// before the restart signal becomes visible.
	//
	// Starting at 0 means "never Ready at any recorded generation", so upgraded
	// objects keep today's behavior until their next Ready.
	// +optional
	LastReadyGeneration int64 `json:"lastReadyGeneration,omitempty"`
}

// ContainerUpdateBaseline records the pre-update state of a container for
// distinguishing expected restarts (from in-place image changes) from real crashes.
type ContainerUpdateBaseline struct {
	// RestartCount is the container's RestartCount before the in-place update.
	RestartCount int32 `json:"restartCount"`
	// ImageID is the container's ImageID before the in-place update.
	// Used to determine if the image actually changed for this container.
	ImageID string `json:"imageID,omitempty"`
}

type RoleInstanceComponentStatus struct {
	// Name is the type name of the component.
	Name string `json:"name"`

	// Size is the number of Pod for RoleInstance that match the component.
	Size int32 `json:"size"`

	// ReadyReplicas is the number of ready Pod for RoleInstance that match the component.
	ReadyReplicas int32 `json:"readyReplicas"`

	// UpdatedReplicas is the number of updated Pod for RoleInstance that match the component.
	UpdatedReplicas int32 `json:"updatedReplicas"`

	// ScheduledReplicas is the number of scheduled Pod for RoleInstance that match the component.
	ScheduledReplicas int32 `json:"scheduledReplicas"`

	// AvailableReplicas is the number of available Pod for RoleInstance that match the component.
	AvailableReplicas int32 `json:"availableReplicas"`

	// UpdatedReadyReplicas is the number of updated and ready Pod for RoleInstance that match the component.
	UpdatedReadyReplicas int32 `json:"updatedReadyReplicas"`
}

// RoleInstanceConditionType is type for RoleInstance conditions.
type RoleInstanceConditionType string

const (
	// RoleInstanceReady corresponding condition status was set to "False" by multiple writers,
	// the condition status will be considered as "True" only when all these writers
	// set it to "True".
	RoleInstanceReady RoleInstanceConditionType = "RoleInstanceReady"

	// RoleInstanceInPlaceUpdateReady indicates RoleInstance inplace update
	RoleInstanceInPlaceUpdateReady RoleInstanceConditionType = "RoleInstanceInPlaceUpdateReady"

	// RoleInstanceCustomReady indicates the expectation of customized ready state.
	RoleInstanceCustomReady RoleInstanceConditionType = "RoleInstanceCustomReady"

	// RoleInstanceAllPodsReady indicates all pods in the RoleInstance are ready.
	RoleInstanceAllPodsReady RoleInstanceConditionType = "RoleInstanceAllPodsReady"

	// RoleInstanceFailedScale indicates RoleInstance controller failed to create or delete pods.
	RoleInstanceFailedScale RoleInstanceConditionType = "FailedScale"

	// RoleInstanceFailedUpdate indicates RoleInstance controller failed to update pods.
	RoleInstanceFailedUpdate RoleInstanceConditionType = "FailedUpdate"

	// RoleInstanceRestarting indicates the instance is being recreated due to restart policy.
	// While this condition is True, further restart-policy recreations are suppressed to
	// prevent cascading restart loops.
	RoleInstanceRestarting RoleInstanceConditionType = "Restarting"

	// RoleInstanceProgressing reports current progress toward the instance target or the
	// current blocker. It is not a readiness gate.
	RoleInstanceProgressing RoleInstanceConditionType = "Progressing"
)

// RoleInstancePhase is the instance-level lifecycle phase of a RoleInstance.
// +kubebuilder:validation:Enum={Creating,Running,Updating,Recovering,Terminating}
type RoleInstancePhase string

const (
	// RoleInstancePhaseCreating means the instance is initially establishing the target
	// Pods for each component and waiting for readyPolicy to be satisfied. External
	// readiness gates do not gate this phase.
	RoleInstancePhaseCreating RoleInstancePhase = "Creating"

	// RoleInstancePhaseRunning means the instance-level target is steady after startup,
	// an update, or recovery completes. Readiness may still be false.
	RoleInstancePhaseRunning RoleInstancePhase = "Running"

	// RoleInstancePhaseUpdating means an identified component spec, template, or revision
	// update is in progress, including in-place updates and Pod replacements.
	RoleInstancePhaseUpdating RoleInstancePhase = "Updating"

	// RoleInstancePhaseRecovering means independent recreation or replacement of required
	// Pods is in progress outside an update.
	RoleInstancePhaseRecovering RoleInstancePhase = "Recovering"

	// RoleInstancePhaseTerminating means the RoleInstance itself has a deletionTimestamp.
	RoleInstancePhaseTerminating RoleInstancePhase = "Terminating"
)

// RoleInstance Progressing reasons. Reason strings are scoped to the object that owns
// the condition; RIS and RBG map these families to their canonical reasons.
const (
	// ReasonInstanceConverged reports the instance target is complete.
	ReasonInstanceConverged = "Converged"

	// ReasonInstanceCreating reports initial creation is in progress.
	ReasonInstanceCreating = "Creating"

	// ReasonInstanceUpdating reports an instance update is in progress.
	ReasonInstanceUpdating = "Updating"

	// ReasonInstanceRecovering reports independent recovery is in progress.
	ReasonInstanceRecovering = "Recovering"

	// ReasonInstancePodsNotReady reports no active workflow while target Pods are not ready.
	ReasonInstancePodsNotReady = "PodsNotReady"

	// ReasonPodCreateQuotaExceeded reports Pod creation is blocked by resource quota.
	ReasonPodCreateQuotaExceeded = "PodCreateQuotaExceeded"

	// ReasonPodUpdateQuotaExceeded reports Pod updates are blocked by resource quota.
	ReasonPodUpdateQuotaExceeded = "PodUpdateQuotaExceeded"

	// ReasonPodCreateForbidden reports Pod creation is blocked by RBAC.
	ReasonPodCreateForbidden = "PodCreateForbidden"

	// ReasonPodUpdateForbidden reports Pod updates are blocked by RBAC.
	ReasonPodUpdateForbidden = "PodUpdateForbidden"

	// ReasonPodAdmissionRejected reports a Pod operation is rejected by admission.
	ReasonPodAdmissionRejected = "PodAdmissionRejected"

	// ReasonInstanceInsufficientResources reports insufficient resources for Pod scheduling.
	ReasonInstanceInsufficientResources = "InsufficientResources"

	// ReasonPodOperationUnclassified reports an operation failure whose family cannot be
	// determined yet.
	ReasonPodOperationUnclassified = "PodOperationUnclassified"
)

// RoleInstanceCondition describes the state of a RoleInstance at a certain point.
type RoleInstanceCondition struct {
	// Type of RoleInstance condition.
	Type RoleInstanceConditionType `json:"type"`

	// Status of the condition, one of True, False, Unknown.
	Status corev1.ConditionStatus `json:"status"`

	// Last time the condition transitioned from one status to another.
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`

	// The reason for the condition's last transition.
	Reason string `json:"reason,omitempty"`

	// A human readable message indicating details about the transition.
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the generation of this RoleInstance to which the fact applies.
	// Its absence does not confirm the current generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient
// +k8s:openapi-gen=true
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rins,path=roleinstances,scope=Namespaced
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='RoleInstanceReady')].status",description="Overall readiness status"
// +kubebuilder:printcolumn:name="PHASE",type="string",JSONPath=".status.phase",description="Instance-level lifecycle phase"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp",description="CreationTimestamp is a timestamp representing the server time when this object was created. It is not guaranteed to be set in happens-before order across separate operations. Clients may not set this value. It is represented in RFC3339 form and is in UTC."

// RoleInstance is the Schema for the roleinstances API
type RoleInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RoleInstanceSpec   `json:"spec,omitempty"`
	Status RoleInstanceStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true

// RoleInstanceList contains a list of RoleInstance
type RoleInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RoleInstance `json:"items"`
}

type RoleInstanceTemplate struct {
	RoleInstanceSpec `json:",inline"`
}

func init() {
	SchemeBuilder.Register(&RoleInstance{}, &RoleInstanceList{})
}

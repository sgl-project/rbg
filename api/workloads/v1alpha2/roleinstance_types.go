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
	// Components is a list of components, each of which specifies a component and the number of replicas and template for RoleInstance that match the component.
	Components []RoleInstanceComponent `json:"components" patchStrategy:"merge" patchMergeKey:"name"`

	// RoleInstanceReadyPolicy specifies the policy for determining if the RoleInstance is ready.
	// Defaults to `AllPodReady`
	// +kubebuilder:default=AllPodReady
	ReadyPolicy RoleInstanceReadyPolicyType `json:"readyPolicy,omitempty"`

	// RestartPolicy defines the restart policy for all pods within the RoleInstance.
	RestartPolicy RestartPolicyType `json:"restartPolicy,omitempty"`

	// ReadinessGates is an optional list of PodReadinessGates for the whole RoleInstance.
	ReadinessGates []RoleInstanceReadinessGate `json:"readinessGates,omitempty"`

	// BaseDelaySeconds is the base delay between restart attempts (seconds).
	// Subsequent attempts use exponential backoff: delay = min(base * 2^restartCount, maxDelaySeconds).
	// The first recreation after a crash is immediate (no backoff) because LastRestartTime is nil.
	// +optional
	// +kubebuilder:validation:Minimum=0
	BaseDelaySeconds *int32 `json:"baseDelaySeconds,omitempty"`

	// MaxDelaySeconds caps the exponential backoff delay (seconds).
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxDelaySeconds *int32 `json:"maxDelaySeconds,omitempty"`
}

// RoleInstanceReadinessGate contains the reference to a RoleInstance condition
type RoleInstanceReadinessGate struct {
	// ConditionType refers to a condition in the pod's condition list with matching type.
	ConditionType RoleInstanceConditionType `json:"conditionType"`
}

// GetBaseDelaySeconds returns the configured base delay or the default (30).
func (s *RoleInstanceSpec) GetBaseDelaySeconds() int32 {
	if s == nil {
		return DefaultBaseDelaySeconds
	}
	if s.BaseDelaySeconds != nil {
		return *s.BaseDelaySeconds
	}
	return DefaultBaseDelaySeconds
}

// GetMaxDelaySeconds returns the configured max delay or the default (600).
func (s *RoleInstanceSpec) GetMaxDelaySeconds() int32 {
	if s == nil {
		return DefaultMaxDelaySeconds
	}
	if s.MaxDelaySeconds != nil {
		return *s.MaxDelaySeconds
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
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient
// +k8s:openapi-gen=true
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=rins,path=roleinstances,scope=Namespaced
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='RoleInstanceReady')].status",description="Overall readiness status"
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

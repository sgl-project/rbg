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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstanceSpec defines the desired state ofInstance
type InstanceSpec struct {
	// Components is a list of components, each of which specifies a component and the number of replicas and template for Instance that match the component.
	Components []InstanceComponent `json:"components" patchStrategy:"merge" patchMergeKey:"name"`

	// Selector is a label query over Pods that should match the Instance.
	Selector *metav1.LabelSelector `json:"selector"`

	// MinReadySeconds is the minimum number of seconds for which a newly created Pod should be ready without any of its containers crashing, for it to be considered available.
	// Configuration for the Instance to enable gang-scheduling via supported plugins.
	PodGroupPolicy *PodGroupPolicy `json:"podGroupPolicy,omitempty"`

	// InstanceReadyPolicy specifies the policy for determining if the Instance is ready.
	// Defaults to `InstanceReadyOnAllPodReady`
	PodReadyPolicy RestartPolicyType `json:"readyPolicy,omitempty"`

	// RestartPolicy defines the restart policy for all pods within the Instance.
	PodRestartPolicy InstanceReadyPolicyType `json:"restartPolicy,omitempty"`

	// ReadinessGates is an optional list of PodReadinessGates for the whole Instance.
	ReadinessGates []InstanceReadinessGate `json:"readinessGates,omitempty"`
}

// InstanceReadinessGate contains the reference to a Instance condition
type InstanceReadinessGate struct {
	// ConditionType refers to a condition in the pod's condition list with matching type.
	ConditionType InstanceConditionType `json:"conditionType"`
}

type InstanceReadyPolicyType string

const (
	// InstanceReadyPolicyTypeAll means all Pods in the Instance must be ready when Instance Ready
	InstanceReadyPolicyTypeAll InstanceReadyPolicyType = "InstanceReadyOnAllPodReady"

	// InstanceReadyPolicyTypeNone means do nothing for Pods
	InstanceReadyPolicyTypeNone InstanceReadyPolicyType = "None"
)

type InstanceComponent struct {
	// Name is the type name of the component.
	Name string `json:"name"`

	// Size is the number of replicas for Pods that match the PodRule.
	Size *int32 `json:"size,omitempty"`

	// serviceName is the name of the service that governs this Instance Component.
	// This service must exist before the Instance, and is responsible for
	// the network identity of the set. Pods get DNS/hostnames that follow the
	// pattern: pod-specific-string.serviceName.default.svc.cluster.local
	// where "pod-specific-string" is managed by the Instance controller.
	ServiceName string `json:"serviceName,omitempty"`

	// Template is the template for the component pods.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Template corev1.PodTemplateSpec `json:"template"`
}

// InstanceStatus defines the observed state ofInstance
type InstanceStatus struct {
	// ObservedGeneration is the most recent generation observed for this Instance. It corresponds to the
	// Instance's generation, which is updated on mutation by the API Server.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions track the condition of the Instance
	Conditions []InstanceCondition `json:"conditions,omitempty"`

	// ComponentStatuses is a list of ComponentStatus, each of which specifies the status of a component.
	ComponentStatuses ComponentStatus `json:"componentStatuses,omitempty"`

	// LabelSelector of an Instance is a label query over Pods that should match the Instance.
	LabelSelector string `json:"labelSelector,omitempty"`

	// CurrentRevision is a hash value that changes when the spec is changed.
	CurrentRevision string `json:"currentRevision,omitempty"`

	// UpdateRevision is a hash value that changes when the spec is changed.
	UpdateRevision string `json:"updateRevision,omitempty"`

	// CollisionCount is the count of hash collisions for the CloneSet. The CloneSet controller
	// uses this field as a collision avoidance mechanism when it needs to create the name for the
	// newest ControllerRevision.
	CollisionCount *int32 `json:"collisionCount,omitempty"`
}

type ComponentStatus struct {
	// Name is the type name of the component.
	Name string `json:"name"`

	// Size is the number of Pod for Instance that match the component.
	Replicas int32 `json:"replicas"`

	// ReadyReplicas is the number of ready Pod for Instance that match the component.
	ReadyReplicas int32 `json:"readyReplicas"`

	// UpdatedReplicas is the number of updated Pod for Instance that match the component.
	UpdatedReplicas int32 `json:"updatedReplicas"`

	// ScheduledReplicas is the number of scheduled Pod for Instance that match the component.
	ScheduledReplicas int32 `json:"scheduledReplicas"`

	// AvailableReplicas is the number of available Pod for Instance that match the component.
	AvailableReplicas int32 `json:"availableReplicas"`

	// UpdatedReadyReplicas is the number of updated and ready Pod for Instance that match the component.
	UpdatedReadyReplicas int32 `json:"updatedReadyReplicas"`
}

// InstanceConditionType is type for Instance conditions.
type InstanceConditionType string

const (
	// InstanceReady corresponding condition status was set to "False" by multiple writers,
	// the condition status will be considered as "True" only when all these writers
	// set it to "True".
	InstanceReady InstanceConditionType = "InstanceReady"

	// InstanceInPlaceUpdateReady indicates Instance inplace update
	InstanceInPlaceUpdateReady InstanceConditionType = "InstanceInPlaceUpdateReady"

	// InstanceAllPodsReady indicates pods ready condition
	InstanceAllPodsReady InstanceConditionType = "InstanceAllPodsReady"

	// InstanceFailedScale indicates Instance controller failed to create or delete pods.
	InstanceFailedScale InstanceConditionType = "FailedScale"

	// InstanceFailedUpdate indicates Instance controller failed to update pods.
	InstanceFailedUpdate InstanceConditionType = "FailedUpdate"
)

// InstanceCondition describes the state of a Instance at a certain point.
type InstanceCondition struct {
	// Type of Instance condition.
	Type InstanceConditionType `json:"type"`

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
// +kubebuilder:resource:shortName=ins,path=Instances,scope=Namespaced
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='InstanceReady')].status",description="Overall readiness status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp",description="CreationTimestamp is a timestamp representing the server time when this object was created. It is not guaranteed to be set in happens-before order across separate operations. Clients may not set this value. It is represented in RFC3339 form and is in UTC."

// Instance is the Schema for the duplicatesets API
type Instance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceSpec   `json:"spec,omitempty"`
	Status InstanceStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true

// InstanceList contains a list ofInstance
type InstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Instance `json:"items"`
}

type InstanceTemplate struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              InstanceSpec `json:"spec,omitempty"`
}

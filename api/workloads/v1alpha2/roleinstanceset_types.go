/*
Copyright 2026.

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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

const (
	DefaultRoleInstanceSetMaxUnavailable       = "10%"
	DefaultRoleInstanceSetRevisionHistoryLimit = 10
)

// RoleInstanceSetSpec defines the desired state of RoleInstanceSet
type RoleInstanceSetSpec struct {
	// Replicas is the desired number of replicas of the given Template.
	// These are replicas in the sense that they are instantiations of the
	// same Template.
	// If unspecified, defaults to 1.
	Replicas *int32 `json:"replicas,omitempty"`

	// Selector is a label query over role instances that should match the replica count.
	// It must match the role instance template's labels.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// RoleInstanceTemplate describes the data a role instance should have when created from a template
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	RoleInstanceTemplate RoleInstanceTemplate `json:"roleInstanceTemplate"`

	// PodManagementPolicy controls how pods are created during the initial scale-up.
	// Parallel (default) creates all instances simultaneously.
	// OrderedReady creates instances one by one, waiting for each to become
	// ready before creating the next.
	// +optional
	// +kubebuilder:default=Parallel
	PodManagementPolicy constants.PodManagementPolicyType `json:"podManagementPolicy,omitempty"`

	// ScaleStrategy indicates the ScaleStrategy that will be employed to
	// create and delete RoleInstances in the RoleInstanceSet.
	// +optional
	ScaleStrategy RoleInstanceSetScaleStrategy `json:"scaleStrategy,omitempty"`

	// UpdateStrategy indicates the UpdateStrategy that will be employed to
	// update RoleInstances in the RoleInstanceSet when a revision is made to Template.
	// +optional
	UpdateStrategy RoleInstanceSetUpdateStrategy `json:"updateStrategy,omitempty"`

	// RevisionHistoryLimit is the maximum number of revisions that will
	// be maintained in the RoleInstanceSet's revision history. The revision history
	// consists of all revisions not represented by a currently applied
	// RoleInstanceSetSpec version. The default value is 10.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=0
	// +optional
	RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty"`

	// Minimum number of seconds for which a newly created RoleInstances should be ready
	// without any of its container crashing, for it to be considered available.
	// Defaults to 0 (RoleInstances will be considered available as soon as it is ready)
	// +optional
	MinReadySeconds int32 `json:"minReadySeconds,omitempty"`

	// Lifecycle defines the lifecycle hooks for RoleInstances pre-delete, in-place update.
	Lifecycle *RoleInstanceSetLifecycle `json:"lifecycle,omitempty"`
}

// RoleInstanceSetLifecycle contains the hooks for RoleInstance lifecycle.
type RoleInstanceSetLifecycle struct {
	// PreDelete is the hook before RoleInstance to be deleted.
	PreDelete *RoleInstanceSetLifecycleHook `json:"preDelete,omitempty"`
	// InPlaceUpdate is the hook before RoleInstance to update and after RoleInstance has been updated.
	InPlaceUpdate *RoleInstanceSetLifecycleHook `json:"inPlaceUpdate,omitempty"`
}

type RoleInstanceSetLifecycleHook struct {
	LabelsHandler     map[string]string `json:"labelsHandler,omitempty"`
	FinalizersHandler []string          `json:"finalizersHandler,omitempty"`
	// MarkNotReady = true means:
	// - RoleInstance will be set to 'NotReady' at preparingDelete/preparingUpdate state.
	// - RoleInstance will be restored to 'Ready' at Updated state if it was set to 'NotReady' at preparingUpdate state.
	// Default to false.
	MarkNotReady bool `json:"markPodNotReady,omitempty"`
}

// RoleInstanceSetScaleStrategy defines strategies for RoleInstances scale.
type RoleInstanceSetScaleStrategy struct {
	// RoleInstanceToDelete is the names of RoleInstance should be deleted.
	// Note that this list will be truncated for non-existing role instance names.
	RoleInstanceToDelete []string `json:"roleInstanceToDelete,omitempty"`

	// The maximum number of RoleInstances that can be unavailable for scaled RoleInstances.
	// This field can control the changes rate of replicas for RoleInstanceSet so as to minimize the impact for users' service.
	// The scale will fail if the number of unavailable RoleInstances were greater than this MaxUnavailable at scaling up.
	// MaxUnavailable works only when scaling up.
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`
}

// RoleInstanceSetUpdateStrategy defines strategies for RoleInstances update.
type RoleInstanceSetUpdateStrategy struct {
	// Type indicates the type of the RoleInstanceSetUpdateStrategy.
	// Default is InPlaceIfPossible.
	Type UpdateStrategyType `json:"type,omitempty"`

	// Partition is the desired number of RoleInstances in old revisions.
	// Value can be an absolute number (ex: 5) or a percentage of desired RoleInstances (ex: 10%).
	// Absolute number is calculated from percentage by rounding up by default.
	// It means when partition is set during RoleInstances updating, (replicas - partition value) number of RoleInstances will be updated.
	// Default value is 0.
	Partition *intstr.IntOrString `json:"partition,omitempty"`

	// The maximum number of RoleInstances that can be unavailable during update or scale.
	// Value can be an absolute number (ex: 5) or a percentage of desired RoleInstances (ex: 10%).
	// Absolute number is calculated from percentage by rounding up by default.
	// When maxSurge > 0, absolute number is calculated from percentage by rounding down.
	// Defaults to 10%.
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`

	// The maximum number of RoleInstances that can be scheduled above the desired replicas during update or specified delete.
	// Value can be an absolute number (ex: 5) or a percentage of desired RoleInstances (ex: 10%).
	// Absolute number is calculated from percentage by rounding up.
	// Defaults to 0.
	MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`

	// Paused indicates that the RoleInstanceSet is paused.
	// Default value is false
	Paused bool `json:"paused,omitempty"`

	// InPlaceUpdateStrategy contains strategies for in-place update.
	InPlaceUpdateStrategy *RoleInstanceSetInPlaceUpdateStrategy `json:"inPlaceUpdateStrategy,omitempty"`
}

// RoleInstanceSetInPlaceUpdateStrategy defines the strategies for in-place update.
type RoleInstanceSetInPlaceUpdateStrategy struct {
	// GracePeriodSeconds is the timespan between set Pod status to not-ready and update images in Pod spec
	// when in-place update a Pod.
	GracePeriodSeconds int32 `json:"gracePeriodSeconds,omitempty"`
}

// RoleInstanceSetStatus defines the observed state of RoleInstanceSet
type RoleInstanceSetStatus struct {
	// ObservedGeneration is the most recent generation observed for this RoleInstanceSet. It corresponds to the
	// RoleInstanceSet's generation, which is updated on mutation by the API Server.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Replicas is the number of RoleInstances created by the RoleInstanceSet controller.
	Replicas int32 `json:"replicas"`

	// ReadyReplicas is the number of RoleInstances created by the RoleInstanceSet controller that have a Ready Condition.
	ReadyReplicas int32 `json:"readyReplicas"`

	// AvailableReplicas is the number of RoleInstances created by the RoleInstanceSet controller that have a Ready Condition for at least minReadySeconds.
	AvailableReplicas int32 `json:"availableReplicas"`

	// CurrentReplicas is the number of RoleInstances created by the RoleInstanceSet controller from the RoleInstanceSet version
	// indicated by currentRevision.
	CurrentReplicas int32 `json:"currentReplicas"`

	// UpdatedReplicas is the number of RoleInstances created by the RoleInstanceSet controller from the RoleInstanceSet version
	// indicated by updateRevision.
	UpdatedReplicas int32 `json:"updatedReplicas"`

	// UpdatedReadyReplicas is the number of RoleInstances created by the RoleInstanceSet controller from the RoleInstanceSet version
	// indicated by updateRevision and have a Ready Condition.
	UpdatedReadyReplicas int32 `json:"updatedReadyReplicas"`

	// ExpectedUpdatedReplicas is the number of RoleInstances that should be updated by RoleInstanceSet controller.
	// This field is calculated via Replicas - Partition.
	ExpectedUpdatedReplicas int32 `json:"expectedUpdatedReplicas,omitempty"`

	// UpdateRevision, if not empty, indicates the latest revision of the RoleInstanceSet.
	UpdateRevision string `json:"updateRevision,omitempty"`

	// CurrentRevision, if not empty, indicates the current revision version of the RoleInstanceSet.
	CurrentRevision string `json:"currentRevision,omitempty"`

	// CollisionCount is the count of hash collisions for the RoleInstanceSet. The RoleInstanceSet controller
	// uses this field as a collision avoidance mechanism when it needs to create the name for the
	// newest ControllerRevision.
	CollisionCount *int32 `json:"collisionCount,omitempty"`

	// Conditions represents the latest available observations of a RoleInstanceSet's current state.
	Conditions []RoleInstanceSetCondition `json:"conditions,omitempty"`

	// LabelSelector is label selectors for query over RoleInstances that should match the replica count used by HPA.
	LabelSelector string `json:"labelSelector,omitempty"`
}

// RoleInstanceSetConditionType is type for RoleInstanceSet conditions.
type RoleInstanceSetConditionType string

const (
	// RoleInstanceSetConditionFailedScale indicates RoleInstanceSet controller failed to create or delete RoleInstances.
	RoleInstanceSetConditionFailedScale RoleInstanceSetConditionType = "FailedScale"

	// RoleInstanceSetConditionFailedUpdate indicates RoleInstanceSet controller failed to update RoleInstances.
	RoleInstanceSetConditionFailedUpdate RoleInstanceSetConditionType = "FailedUpdate"
)

// RoleInstanceSetCondition describes the state of a RoleInstanceSet at a certain point.
type RoleInstanceSetCondition struct {
	// Type of RoleInstanceSet condition.
	Type RoleInstanceSetConditionType `json:"type"`

	// Status of the condition, one of True, False, Unknown.
	Status v1.ConditionStatus `json:"status"`

	// Last time the condition transitioned from one status to another.
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`

	// The reason for the condition's last transition.
	Reason string `json:"reason,omitempty"`

	// A human-readable message indicating details about the transition.
	Message string `json:"message,omitempty"`
}

// +genclient
// +genclient:method=GetScale,verb=get,subresource=scale,result=k8s.io/api/autoscaling/v1.Scale
// +genclient:method=UpdateScale,verb=update,subresource=scale,input=k8s.io/api/autoscaling/v1.Scale,result=k8s.io/api/autoscaling/v1.Scale
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ris,path=roleinstancesets,scope=Namespaced
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.labelSelector
// +kubebuilder:printcolumn:name="DESIRED",type="integer",JSONPath=".spec.replicas",description="The desired number of RoleInstances."
// +kubebuilder:printcolumn:name="UPDATED",type="integer",JSONPath=".status.updatedReplicas",description="The number of RoleInstances updated."
// +kubebuilder:printcolumn:name="UPDATED_READY",type="integer",JSONPath=".status.updatedReadyReplicas",description="The number of RoleInstances updated and ready."
// +kubebuilder:printcolumn:name="READY",type="integer",JSONPath=".status.readyReplicas",description="The number of RoleInstances ready."
// +kubebuilder:printcolumn:name="TOTAL",type="integer",JSONPath=".status.replicas",description="The number of currently all RoleInstances."
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp",description="CreationTimestamp is a timestamp representing the server time when this object was created. It is not guaranteed to be set in happens-before order across separate operations. Clients may not set this value. It is represented in RFC3339 form and is in UTC."
// +kubebuilder:printcolumn:name="SELECTOR",type="string",priority=1,JSONPath=".status.labelSelector",description="The selector of currently RoleInstanceSet."

// RoleInstanceSet is the Schema for the RoleInstanceSet API
type RoleInstanceSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RoleInstanceSetSpec   `json:"spec,omitempty"`
	Status RoleInstanceSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RoleInstanceSetList contains a list of RoleInstanceSet
type RoleInstanceSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RoleInstanceSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RoleInstanceSet{}, &RoleInstanceSetList{})
}

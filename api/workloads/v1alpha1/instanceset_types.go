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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	DefaultInstanceSetMaxUnavailable = "10%"
)

// InstanceSetSpec defines the desired state of InstanceSet
type InstanceSetSpec struct {
	// Replicas is the desired number of replicas of the given Template.
	// These are replicas in the sense that they are instantiations of the
	// same Template.
	// If unspecified, defaults to 1.
	Replicas *int32 `json:"replicas,omitempty"`

	// Selector is a label query over instances that should match the replica count.
	// It must match the instance template's labels.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// InstanceTemplate describes the data an instance should have when created from a template
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	InstanceTemplate InstanceTemplate `json:"instanceTemplate"`

	// ScaleStrategy indicates the ScaleStrategy that will be employed to
	// create and delete Instances in the InstanceSet.
	// +optional
	ScaleStrategy InstanceSetScaleStrategy `json:"scaleStrategy,omitempty"`

	// UpdateStrategy indicates the UpdateStrategy that will be employed to
	// update Instances in the InstanceSet when a revision is made to Template.
	// +optional
	UpdateStrategy InstanceSetUpdateStrategy `json:"updateStrategy,omitempty"`

	// RevisionHistoryLimit is the maximum number of revisions that will
	// be maintained in the InstanceSet's revision history. The revision history
	// consists of all revisions not represented by a currently applied
	// InstanceSetSpec version. The default value is 10.
	RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty"`

	// Minimum number of seconds for which a newly created Instances should be ready
	// without any of its container crashing, for it to be considered available.
	// Defaults to 0 (Instances will be considered available as soon as it is ready)
	// +optional
	MinReadySeconds int32 `json:"minReadySeconds,omitempty"`

	// Lifecycle defines the lifecycle hooks for Instances pre-delete, in-place update.
	Lifecycle *Lifecycle `json:"lifecycle,omitempty"`
}

const (
	LifecycleStateKey     = "lifecycle.workloads.x-k8s.io/state"
	LifecycleTimestampKey = "lifecycle.workloads.x-k8s.io/timestamp"

	LifecycleStateNormal          LifecycleStateType = "Normal"
	LifecycleStatePreparingUpdate LifecycleStateType = "PreparingUpdate"
	LifecycleStateUpdating        LifecycleStateType = "Updating"
	LifecycleStateUpdated         LifecycleStateType = "Updated"
	LifecycleStatePreparingDelete LifecycleStateType = "PreparingDelete"
)

type LifecycleStateType string

// Lifecycle contains the hooks for Instance lifecycle.
type Lifecycle struct {
	// PreDelete is the hook before Instance to be deleted.
	PreDelete *LifecycleHook `json:"preDelete,omitempty"`
	// InPlaceUpdate is the hook before Instance to update and after Instance has been updated.
	InPlaceUpdate *LifecycleHook `json:"inPlaceUpdate,omitempty"`
}

type LifecycleHook struct {
	LabelsHandler     map[string]string `json:"labelsHandler,omitempty"`
	FinalizersHandler []string          `json:"finalizersHandler,omitempty"`
	// MarkNotReady = true means:
	// - Instance will be set to 'NotReady' at preparingDelete/preparingUpdate state.
	// - Instance will be restored to 'Ready' at Updated state if it was set to 'NotReady' at preparingUpdate state.
	// Default to false.
	MarkNotReady bool `json:"markPodNotReady,omitempty"`
}

// InstanceSetScaleStrategy defines strategies for Instances scale.
type InstanceSetScaleStrategy struct {
	// InstanceToDelete is the names of Instance should be deleted.
	// Note that this list will be truncated for non-existing instance names.
	InstanceToDelete []string `json:"instanceToDelete,omitempty"`

	// The maximum number of Instances that can be unavailable for scaled Instances.
	// This field can control the changes rate of replicas for InstanceSet so as to minimize the impact for users' service.
	// The scale will fail if the number of unavailable Instances were greater than this MaxUnavailable at scaling up.
	// MaxUnavailable works only when scaling up.
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`
}

// InstanceSetUpdateStrategy defines strategies for Instances update.
type InstanceSetUpdateStrategy struct {
	// Type indicates the type of the InstanceSetUpdateStrategy.
	// Default is ReCreate.
	Type UpdateStrategyType `json:"type,omitempty"`

	// Partition is the desired number of Instances in old revisions.
	// Value can be an absolute number (ex: 5) or a percentage of desired Instances (ex: 10%).
	// Absolute number is calculated from percentage by rounding up by default.
	// It means when partition is set during Instances updating, (replicas - partition value) number of Instances will be updated.
	// Default value is 0.
	Partition *intstr.IntOrString `json:"partition,omitempty"`

	// The maximum number of Instances that can be unavailable during update or scale.
	// Value can be an absolute number (ex: 5) or a percentage of desired Instances (ex: 10%).
	// Absolute number is calculated from percentage by rounding up by default.
	// When maxSurge > 0, absolute number is calculated from percentage by rounding down.
	// Defaults to 20%.
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`

	// The maximum number of Instances that can be scheduled above the desired replicas during update or specified delete.
	// Value can be an absolute number (ex: 5) or a percentage of desired Instances (ex: 10%).
	// Absolute number is calculated from percentage by rounding up.
	// Defaults to 0.
	MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`

	// Paused indicates that the InstanceSet is paused.
	// Default value is false
	Paused bool `json:"paused,omitempty"`

	// InPlaceUpdateStrategy contains strategies for in-place update.
	InPlaceUpdateStrategy *InPlaceUpdateStrategy `json:"inPlaceUpdateStrategy,omitempty"`
}

// InPlaceUpdateStrategy defines the strategies for in-place update.
type InPlaceUpdateStrategy struct {
	// GracePeriodSeconds is the timespan between set Pod status to not-ready and update images in Pod spec
	// when in-place update a Pod.
	GracePeriodSeconds int32 `json:"gracePeriodSeconds,omitempty"`
}

// InstanceSetStatus defines the observed state of InstanceSet
type InstanceSetStatus struct {
	// ObservedGeneration is the most recent generation observed for this InstanceSet. It corresponds to the
	// InstanceSet's generation, which is updated on mutation by the API Server.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Replicas is the number of Instances created by the InstanceSet controller.
	Replicas int32 `json:"replicas"`

	// ReadyReplicas is the number of Instances created by the InstanceSet controller that have a Ready Condition.
	ReadyReplicas int32 `json:"readyReplicas"`

	// AvailableReplicas is the number of Instances created by the InstanceSet controller that have a Ready Condition for at least minReadySeconds.
	AvailableReplicas int32 `json:"availableReplicas"`

	// CurrentReplicas is the number of Instances created by the InstanceSet controller from the InstanceSet version
	// indicated by currentRevision.
	CurrentReplicas int32 `json:"currentReplicas"`

	// UpdatedReplicas is the number of Instances created by the InstanceSet controller from the InstanceSet version
	// indicated by updateRevision.
	UpdatedReplicas int32 `json:"updatedReplicas"`

	// UpdatedReadyReplicas is the number of Instances created by the InstanceSet controller from the InstanceSet version
	// indicated by updateRevision and have a Ready Condition.
	UpdatedReadyReplicas int32 `json:"updatedReadyReplicas"`

	// ExpectedUpdatedReplicas is the number of Instances that should be updated by InstanceSet controller.
	// This field is calculated via Replicas - Partition.
	ExpectedUpdatedReplicas int32 `json:"expectedUpdatedReplicas,omitempty"`

	// UpdateRevision, if not empty, indicates the latest revision of the InstanceSet.
	UpdateRevision string `json:"updateRevision,omitempty"`

	// currentRevision, if not empty, indicates the current revision version of the InstanceSet.
	CurrentRevision string `json:"currentRevision,omitempty"`

	// CollisionCount is the count of hash collisions for the InstanceSet. The InstanceSet controller
	// uses this field as a collision avoidance mechanism when it needs to create the name for the
	// newest ControllerRevision.
	CollisionCount *int32 `json:"collisionCount,omitempty"`

	// Conditions represents the latest available observations of a InstanceSet's current state.
	Conditions []InstanceSetCondition `json:"conditions,omitempty"`

	// LabelSelector is label selectors for query over Instances that should match the replica count used by HPA.
	LabelSelector string `json:"labelSelector,omitempty"`
}

// InstanceSetConditionType is type for InstanceSet conditions.
type InstanceSetConditionType string

const (
	// InstanceSetConditionFailedScale indicates InstanceSet controller failed to create or delete Instances.
	InstanceSetConditionFailedScale InstanceSetConditionType = "FailedScale"

	// InstanceSetConditionFailedUpdate indicates InstanceSet controller failed to update Instances.
	InstanceSetConditionFailedUpdate InstanceSetConditionType = "FailedUpdate"
)

// InstanceSetCondition describes the state of a InstanceSet at a certain point.
type InstanceSetCondition struct {
	// Type of InstanceSet condition.
	Type InstanceSetConditionType `json:"type"`

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
// +kubebuilder:resource:shortName=is,path=instancesets,scope=Namespaced
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.labelSelector
// +kubebuilder:printcolumn:name="DESIRED",type="integer",JSONPath=".spec.replicas",description="The desired number of Instances."
// +kubebuilder:printcolumn:name="UPDATED",type="integer",JSONPath=".status.updatedReplicas",description="The number of Instances updated."
// +kubebuilder:printcolumn:name="UPDATED_READY",type="integer",JSONPath=".status.updatedReadyReplicas",description="The number of Instances updated and ready."
// +kubebuilder:printcolumn:name="READY",type="integer",JSONPath=".status.readyReplicas",description="The number of Instances ready."
// +kubebuilder:printcolumn:name="TOTAL",type="integer",JSONPath=".status.replicas",description="The number of currently all Instances."
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp",description="CreationTimestamp is a timestamp representing the server time when this object was created. It is not guaranteed to be set in happens-before order across separate operations. Clients may not set this value. It is represented in RFC3339 form and is in UTC."
// +kubebuilder:printcolumn:name="SELECTOR",type="string",priority=1,JSONPath=".status.labelSelector",description="The selector of currently InstanceSet."

// InstanceSet is the Schema for the InstanceSet API
type InstanceSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceSetSpec   `json:"spec,omitempty"`
	Status InstanceSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// InstanceSetList contains a list of InstanceSet
type InstanceSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InstanceSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InstanceSet{}, &InstanceSetList{})
}

/*
Copyright 2025 The RBG Authors.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// RoleBasedGroupTemplateSpec describes the data a RoleBasedGroup should have when created from a template.
type RoleBasedGroupTemplateSpec struct {
	// Map of string keys and values that can be used to organize and categorize objects.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations is an unstructured key value map stored with a resource.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Spec defines the desired behavior of the RoleBasedGroup.
	// +optional
	Spec RoleBasedGroupSpec `json:"spec"`
}

// RoleBasedGroupSetSpec defines the desired state of RoleBasedGroupSet.
type RoleBasedGroupSetSpec struct {
	// Replicas is the number of RoleBasedGroup that will be created.
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas,omitempty"`

	// GroupTemplate describes the RoleBasedGroup that will be created.
	GroupTemplate RoleBasedGroupTemplateSpec `json:"groupTemplate"`

	// RolloutStrategy controls how a change to GroupTemplate is propagated to the child
	// RoleBasedGroups. When it is unset every outdated RoleBasedGroup is updated in place
	// within a single reconcile, with no ordering and no availability gating.
	// +optional
	RolloutStrategy *GroupSetRolloutStrategy `json:"rolloutStrategy,omitempty"`
}

// GroupUpdateStrategyType defines the strategy type for updating one child RoleBasedGroup.
type GroupUpdateStrategyType string

const (
	// RecreateStrategyType - Delete the RoleBasedGroup and recreate it from
	// GroupTemplate under the same name and ordinal. Every downstream workload and Pod is
	// rebuilt, so the group restarts as a whole and gang consistency is preserved.
	// The role level rolloutStrategy of the RoleBasedGroup has no effect in this mode.
	RecreateStrategyType GroupUpdateStrategyType = "Recreate"

	// InPlaceUpdateStrategyType - Update the RoleBasedGroup object in place,
	// keeping its name and UID, and let each downstream workload decide how its Pods move
	// to the new template according to its own role level rolloutStrategy.
	// the current controller only implements Recreate.
	InPlaceUpdateStrategyType GroupUpdateStrategyType = "InPlaceUpdate"
)

// GroupSetRolloutStrategy defines the strategy that the RoleBasedGroupSet controller will
// use to perform updates of its child RoleBasedGroups.
type GroupSetRolloutStrategy struct {
	// Type indicates how one child RoleBasedGroup is moved onto the new template.
	// +kubebuilder:validation:Enum={Recreate}
	// +kubebuilder:default=Recreate
	Type GroupUpdateStrategyType `json:"type,omitempty"`

	// Partition is the number of lowest ordinal RoleBasedGroups that are held back on the
	// previous template. Only the ordinals in [Partition, Replicas) take part in the
	// rollout. Value can be an absolute number (ex: 2) or a percentage of Replicas
	// (ex: 25%). Absolute number is calculated from percentage by rounding down.
	// It must not be greater than Replicas.
	//
	// +optional
	// +kubebuilder:default=0
	Partition *intstr.IntOrString `json:"partition,omitempty"`

	// MaxUnavailable is the maximum number of RoleBasedGroups that can be unavailable
	// during the update. Value can be an absolute number (ex: 1) or a percentage of
	// Replicas (ex: 25%). Absolute number is calculated from percentage by rounding up
	// when MaxSurge is 0, and by rounding down when MaxSurge is greater than 0, where a
	// resolved value of 0 is allowed.
	//
	// +kubebuilder:validation:XIntOrString
	// +kubebuilder:default=1
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`

	// MaxSurge is the maximum number of RoleBasedGroups that can be created above Replicas
	// during the update. They occupy the ordinals in [Replicas, Replicas+MaxSurge) and take
	// traffic exactly like the base groups do. Value can be an absolute number (ex: 1) or a
	// percentage of Replicas (ex: 25%).
	// Absolute number is calculated from percentage by rounding up.
	//
	// +kubebuilder:validation:XIntOrString
	// +kubebuilder:default=0
	MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`

	// Paused freezes the rollout, so outdated RoleBasedGroups are neither recreated nor
	// updated. Reconciling Replicas still happens while paused.
	// Surge groups that already exist are kept.
	//
	// +optional
	Paused bool `json:"paused,omitempty"`
}

type RoleBasedGroupSetConditionType string

const (
	RoleBasedGroupSetReady RoleBasedGroupSetConditionType = "Ready"

	// RoleBasedGroupSetRolling means a GroupTemplate change is being propagated to the
	// child RoleBasedGroups.
	RoleBasedGroupSetRolling RoleBasedGroupSetConditionType = "Rolling"

	// RoleBasedGroupSetPaused means spec.rolloutStrategy.paused is holding the rollout back.
	RoleBasedGroupSetPaused RoleBasedGroupSetConditionType = "Paused"
)

// RoleBasedGroupSetStatus defines the observed state of RoleBasedGroupSet.
type RoleBasedGroupSetStatus struct {
	// The generation observed by the deployment controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty" protobuf:"varint,1,opt,name=observedGeneration"`

	// +optional
	Replicas int32 `json:"replicas,omitempty" protobuf:"varint,2,opt,name=replicas"`

	// +optional
	ReadyReplicas int32 `json:"readyReplicas" protobuf:"varint,3,opt,name=readyReplicas"`

	// CurrentReplicas is the number of child RoleBasedGroups at ordinals below Replicas
	// whose spec.roles do not match the current GroupTemplate, i.e. the groups the rollout
	// has not moved onto the new template yet. Surge groups are not counted.
	// +optional
	CurrentReplicas int32 `json:"currentReplicas,omitempty"`

	// UpdatedReplicas is the number of child RoleBasedGroups at ordinals below Replicas
	// whose spec.roles match the current GroupTemplate, whether they are ready or not.
	// Surge groups are not counted.
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`

	// UpdatedReadyReplicas is the number of child RoleBasedGroups at ordinals below
	// Replicas whose spec.roles match the current GroupTemplate and are also ready.
	// Surge groups are not counted.
	// +optional
	UpdatedReadyReplicas int32 `json:"updatedReadyReplicas,omitempty"`

	// ExpectedUpdatedReplicas is the number of RoleBasedGroups the rollout is expected to
	// bring onto the current GroupTemplate. It is calculated as Replicas - Partition.
	// +optional
	ExpectedUpdatedReplicas int32 `json:"expectedUpdatedReplicas,omitempty"`

	// Conditions track the condition of the rbgs
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="DESIRED",type="string",JSONPath=".status.replicas",description="desired replicas"
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.readyReplicas",description="ready replicas"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:shortName={rbgs}

// RoleBasedGroupSet is the Schema for the rolebasedgroupsets API.
type RoleBasedGroupSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RoleBasedGroupSetSpec   `json:"spec,omitempty"`
	Status RoleBasedGroupSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RoleBasedGroupSetList contains a list of RoleBasedGroupSet.
type RoleBasedGroupSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RoleBasedGroupSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RoleBasedGroupSet{}, &RoleBasedGroupSetList{})
}

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RoleBasedGroupTemplateSpec describes the data a RoleBasedGroup should have when created from a template.
type RoleBasedGroupTemplateSpec struct {
	// Standard object's metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired behavior of the RoleBasedGroup.
	// +optional
	Spec RoleBasedGroupSpec `json:"spec,omitempty"`
}

// RoleBasedGroupSetSpec defines the desired state of RoleBasedGroupSet.
type RoleBasedGroupSetSpec struct {
	// Replicas is the number of RoleBasedGroup that will be created.
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas,omitempty"`

	// GroupTemplate describes the RoleBasedGroup that will be created.
	GroupTemplate RoleBasedGroupTemplateSpec `json:"groupTemplate"`
}

type RoleBasedGroupSetConditionType string

const (
	RoleBasedGroupSetReady RoleBasedGroupSetConditionType = "Ready"
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

	// Conditions track the condition of the rbgs
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas
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

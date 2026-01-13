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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RoleBasedGroupScalingAdapterSpec defines the desired state of RoleBasedGroupScalingAdapter.
type RoleBasedGroupScalingAdapterSpec struct {
	// Replicas is the number of RoleBasedGroupRole that will be scaled.
	Replicas *int32 `json:"replicas,omitempty"`

	// ScaleTargetRef is a reference to the target resource that should be scaled.
	ScaleTargetRef *AdapterScaleTargetRef `json:"scaleTargetRef"`
}

// RoleBasedGroupScalingAdapterStatus shows the current state of a RoleBasedGroupScalingAdapter.
type RoleBasedGroupScalingAdapterStatus struct {
	// Phase indicates the current phase of the RoleBasedGroupScalingAdapter.
	Phase AdapterPhase `json:"phase,omitempty"`

	// Replicas is the current effective number of target RoleBasedGroupRole.
	Replicas *int32 `json:"replicas,omitempty"`

	// Selector is a label query used to filter and identify a set of resources targeted for metrics collection.
	Selector string `json:"selector,omitempty"`

	// LastScaleTime is the last time the RoleBasedGroupScalingAdapter scaled the number of pods,
	LastScaleTime *metav1.Time `json:"lastScaleTime,omitempty"`
}

type AdapterScaleTargetRef struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:resource:shortName={rbgsa}
// +kubebuilder:printcolumn:name="PHASE",type="string",JSONPath=".status.phase",description="The current phase of the adapter"
// +kubebuilder:printcolumn:name="REPLICAS",type="integer",JSONPath=".status.replicas",description="The current number of replicas"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp",description="Time since creation."

// RoleBasedGroupScalingAdapter is the Schema for the rolebasedgroupscalingadapters API.
type RoleBasedGroupScalingAdapter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RoleBasedGroupScalingAdapterSpec   `json:"spec,omitempty"`
	Status RoleBasedGroupScalingAdapterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RoleBasedGroupScalingAdapterList contains a list of RoleBasedGroupScalingAdapter.
type RoleBasedGroupScalingAdapterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RoleBasedGroupScalingAdapter `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RoleBasedGroupScalingAdapter{}, &RoleBasedGroupScalingAdapterList{})
}

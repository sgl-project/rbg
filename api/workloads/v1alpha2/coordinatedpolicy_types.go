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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// CoordinatedPolicySpec defines the desired state of CoordinatedPolicy.
type CoordinatedPolicySpec struct {
	// Policies define the coordination policies for roles.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:Required
	// +listType=map
	// +listMapKey=name
	Policies []CoordinatedPolicyRule `json:"policies"`
}

// CoordinatedPolicyRule defines the coordination policy rule for a set of roles.
type CoordinatedPolicyRule struct {
	// Name specifies the name of this policy rule.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Roles specifies the names of the roles that this policy applies to.
	// TODO: Add validation to detect conflicts when the same role appears in multiple policies.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:Required
	Roles []string `json:"roles"`

	// Strategy defines the coordinated strategies for the roles.
	// +kubebuilder:validation:Required
	Strategy CoordinatedPolicyStrategy `json:"strategy"`
}

// CoordinatedPolicyStrategy defines the strategy for coordinated roles.
type CoordinatedPolicyStrategy struct {
	// RollingUpdate defines the coordinated strategy for rolling updates.
	// +optional
	RollingUpdate *RollingUpdateCoordinationStrategy `json:"rollingUpdate,omitempty"`

	// Scaling defines the coordinated strategy for scaling operations.
	// +optional
	Scaling *ScalingCoordinationStrategy `json:"scaling,omitempty"`
}

// RollingUpdateCoordinationStrategy defines the coordination parameters for rolling updates.
type RollingUpdateCoordinationStrategy struct {
	// MaxSkew is the maximum allowed skew between the update progress of different roles.
	// Can be an absolute number (e.g., 5) or a percentage (e.g., "10%").
	// +optional
	// +kubebuilder:validation:XIntOrString
	MaxSkew *intstr.IntOrString `json:"maxSkew,omitempty"`

	// Partition indicates the ordinal at which the roles should be partitioned for updates.
	// Can be an absolute number or a percentage.
	// +optional
	// +kubebuilder:validation:XIntOrString
	Partition *intstr.IntOrString `json:"partition,omitempty"`

	// MaxUnavailable is the maximum number of replicas that can be unavailable during the update.
	// Can be an absolute number or a percentage.
	// +optional
	// +kubebuilder:validation:XIntOrString
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`
}

// ScalingCoordinationStrategy defines the coordination parameters for scaling.
type ScalingCoordinationStrategy struct {
	// MaxSkew is the maximum allowed skew between the scaling progress of different roles.
	// Can be an absolute number (e.g., 5) or a percentage (e.g., "10%").
	// +optional
	// +kubebuilder:validation:XIntOrString
	MaxSkew *intstr.IntOrString `json:"maxSkew,omitempty"`

	// Progression defines the order in which replicas are scheduled during scaling.
	// +optional
	// +kubebuilder:validation:Enum={OrderScheduled,OrderReady}
	Progression ScalingProgression `json:"progression,omitempty"`
}

// ScalingProgression defines the progression type for scaling.
type ScalingProgression string

const (
	// OrderScheduledProgression scales replicas in order based on scheduling status.
	OrderScheduledProgression ScalingProgression = "OrderScheduled"

	// OrderReadyProgression scales replicas in order based on readiness.
	OrderReadyProgression ScalingProgression = "OrderReady"
)

// CoordinatedPolicyStatus defines the observed state of CoordinatedPolicy.
type CoordinatedPolicyStatus struct {
	// ObservedGeneration is the generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions track the condition of the CoordinatedPolicy.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:shortName={cpolicy}

// CoordinatedPolicy is the Schema for the coordinatedpolicies API.
// It defines coordination policies for rolling updates and scaling across multiple roles.
type CoordinatedPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CoordinatedPolicySpec   `json:"spec,omitempty"`
	Status CoordinatedPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CoordinatedPolicyList contains a list of CoordinatedPolicy.
type CoordinatedPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CoordinatedPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CoordinatedPolicy{}, &CoordinatedPolicyList{})
}

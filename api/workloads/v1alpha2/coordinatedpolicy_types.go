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

	// Scheduling defines the coordinated strategy for scheduling.
	// Currently supports gang scheduling via the `gang` sub-field.
	// Future scheduling coordination strategies may be added.
	// +optional
	Scheduling *SchedulingCoordinationStrategy `json:"scheduling,omitempty"`
}

// SchedulingCoordinationStrategy defines scheduling coordination for roles.
// This is a domain that can hold multiple scheduling coordination strategies.
type SchedulingCoordinationStrategy struct {
	// Gang defines the gang scheduling coordination for roles.
	// When present, gang scheduling is enabled for the RoleBasedGroup. Which roles
	// are constrained depends on minReplicas: an empty map covers every role in the
	// group, a non-empty one covers only the roles it names.
	//
	// Several policy rules may each declare a gang strategy. Their minReplicas
	// maps are merged across all rules, taking the maximum when the same role
	// appears more than once. A rule may only name roles listed in its own
	// spec.policies[].roles.
	//
	// When scheduling.gang is not configured (or CoordinatedPolicy does not
	// exist), the controller falls back to checking the legacy annotation
	// rbg.workloads.x-k8s.io/group-gang-scheduling=true for backward compatibility.
	// When neither is set, gang scheduling is disabled.
	//
	// +optional
	Gang *GangSchedulingStrategy `json:"gang,omitempty"`
}

// GangSchedulingStrategy defines gang scheduling parameters per role.
type GangSchedulingStrategy struct {
	// MinReplicas defines the minimum number of replicas per role
	// that must be scheduled together as part of the gang.
	//
	// Keys must name roles that are listed in the enclosing policy rule's
	// `roles` field, and each value must fall within [1, role.replicas].
	// Both are enforced by the CoordinatedPolicy validating webhook rather than by
	// the CRD schema: this field sits inside the unbounded spec.policies array, where
	// a CEL rule's estimated cost exceeds the apiserver budget and would make the
	// entire CRD unloadable.
	//
	// When non-empty, only the roles listed in this map participate in
	// the gang with their respective minimums. Roles absent from this
	// map are excluded from the gang constraint, but their pods still get
	// the gang scheduler's schedulerName so the whole group is placed by
	// one scheduler.
	//
	// Per-role minimums are implemented with the Volcano PodGroup
	// `subGroupPolicy` field and therefore require --scheduler-name=volcano
	// with Volcano >= 1.14. scheduler-plugins supports basic gang only.
	//
	// When the gang field is present but minReplicas is empty (nil),
	// ALL roles participate and minMember equals GetGroupSize()
	// (basic all-or-nothing gang).
	//
	// +optional
	// +kubebuilder:validation:Type=object
	MinReplicas map[string]int32 `json:"minReplicas,omitempty"`
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
	// +listType=map
	// +listMapKey=type
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

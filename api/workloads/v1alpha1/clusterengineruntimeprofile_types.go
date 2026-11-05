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

package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	NoUpdateStrategy      = "NoUpdate"
	RollingUpdateStrategy = "RollingUpdate"
)

// ClusterEngineRuntimeProfileSpec defines the desired state of ClusterEngineRuntimeProfile.
type ClusterEngineRuntimeProfileSpec struct {
	// +optional
	InitContainers []v1.Container `json:"initContainers,omitempty"`
	// +optional
	Containers []v1.Container `json:"containers,omitempty"`
	// +optional
	Volumes []v1.Volume `json:"volumes,omitempty"`
	// +kubebuilder:validation:Enum=NoUpdate;RollingUpdate
	// +kubebuilder:default=NoUpdate
	// Deprecated: updateStrategy was never consumed by the controller and
	// is removed at the next API version boundary alongside
	// injectContainers. Profile revisions are published as new profiles
	// and references migrate one at a time — see Profile Mutability in
	// KEP-531.
	UpdateStrategy string `json:"updateStrategy"`

	// ContainerPatches mirrors the v1alpha2 field with identical shape:
	// ClusterEngineRuntimeProfile uses a None conversion strategy, so a
	// v1alpha1 write without this schema entry would prune the field
	// from the stored object.
	//
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +listType=atomic
	ContainerPatches []ContainerPatch `json:"containerPatches,omitempty"`
}

// ClusterEngineRuntimeProfileStatus mirrors the v1alpha2 status fields:
// display and detection only, never authoritative for rendering.
type ClusterEngineRuntimeProfileStatus struct {
	// ReferenceCount is the count of live references to this profile:
	// RoleBasedGroups plus the group templates of RoleBasedGroupSets,
	// a set's template counting even at zero replicas.
	// +optional
	ReferenceCount int32 `json:"referenceCount,omitempty"`

	// ReferencedGeneration is the profile's metadata.generation recorded
	// at the none-to-referenced transition and cleared when the last
	// reference leaves; it is never refreshed while referenced.
	// +optional
	ReferencedGeneration int64 `json:"referencedGeneration,omitempty"`

	// Conditions carries the Frozen condition: True with reason
	// Referenced while a live reference exists, flipping to reason
	// SpecChangedWhileReferenced on a generation mismatch.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:deprecatedversion:warning="workloads.x-k8s.io/v1alpha1 ClusterEngineRuntimeProfile is deprecated and will be removed in 0.10.0; use workloads.x-k8s.io/v1alpha2 ClusterEngineRuntimeProfile"

// ClusterEngineRuntimeProfile is the Schema for the clusterengineruntimeprofiles API.
type ClusterEngineRuntimeProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterEngineRuntimeProfileSpec   `json:"spec,omitempty"`
	Status ClusterEngineRuntimeProfileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterEngineRuntimeProfileList contains a list of ClusterEngineRuntimeProfile.
type ClusterEngineRuntimeProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterEngineRuntimeProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterEngineRuntimeProfile{}, &ClusterEngineRuntimeProfileList{})
}

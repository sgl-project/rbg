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
	UpdateStrategy string `json:"updateStrategy"`
}

// ClusterEngineRuntimeProfileStatus defines the observed state of ClusterEngineRuntimeProfile.
type ClusterEngineRuntimeProfileStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

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

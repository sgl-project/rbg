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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type WarmupJobPhase string

const (
	WarmupJobPhaseNone      WarmupJobPhase = ""
	WarmupJobPhaseRunning   WarmupJobPhase = "Running"
	WarmupJobPhasePaused    WarmupJobPhase = "Paused"
	WarmupJobPhaseCompleted WarmupJobPhase = "Completed"
	WarmupJobPhaseFailed    WarmupJobPhase = "Failed"
)

type ImagePreloadAction struct {
	// +kubebuilder:validation:MinItems=1
	// +required
	Items []string `json:"items"`

	// +optional
	PullSecrets []corev1.LocalObjectReference `json:"pullSecrets,omitempty"`
}

type CustomizedAction struct {
	// +kubebuilder:validation:MinItems=1
	// +required
	Containers []corev1.Container `json:"containers"`

	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`
}

type WarmupActions struct {
	// +optional
	ImagePreload *ImagePreloadAction `json:"imagePreload,omitempty"`

	// +optional
	CustomizedAction *CustomizedAction `json:"customizedAction,omitempty"`
}

type TargetNodes struct {
	NodeNames []string `json:"nodeNames,omitempty"`

	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	WarmupActions `json:",inline"`
}

type TargetRoleBasedGroup struct {
	Name string `json:"name"`

	Roles map[string]WarmupActions `json:"roles"`
}

// RoleBasedGroupWarmupSpec defines the desired state of RoleBasedGroupWarmup
type RoleBasedGroupWarmupSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// The following markers will use OpenAPI v3 schema to validate the value
	// More info: https://book.kubebuilder.io/reference/markers/crd-validation.html

	// TargetNodes specifies explicit nodes and warmup actions to perform on them.
	// +optional
	TargetNodes *TargetNodes `json:"targetNodes,omitempty"`

	// TargetRoleBasedGroup specifies a RoleBasedGroup resource to discover nodes from.
	// The controller will find all Pods belonging to the RoleBasedGroup, extract their nodes,
	// and perform role-specific warmup actions. Multiple warmup actions for the same node
	// will be merged into a single warmup Pod.
	// +optional
	TargetRoleBasedGroup *TargetRoleBasedGroup `json:"targetRoleBasedGroup,omitempty"`
}

// RoleBasedGroupWarmupStatus defines the observed state of RoleBasedGroupWarmup.
type RoleBasedGroupWarmupStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	StartTime metav1.Time `json:"startTime,omitempty"`

	CompletionTime metav1.Time `json:"completionTime,omitempty"`

	Desired int32 `json:"desired,omitempty"`

	Succeeded int32 `json:"succeeded,omitempty"`

	Failed int32 `json:"failed,omitempty"`

	Active int32 `json:"active,omitempty"`

	Ready int32 `json:"ready,omitempty"`

	Phase WarmupJobPhase `json:"phase"`

	// conditions represent the current state of the RoleBasedGroupWarmup resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName={rbgwarmup}

// RoleBasedGroupWarmup is the Schema for the rolebasedgroupwarmups API
type RoleBasedGroupWarmup struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of RoleBasedGroupWarmup
	// +required
	Spec RoleBasedGroupWarmupSpec `json:"spec"`

	// status defines the observed state of RoleBasedGroupWarmup
	// +optional
	Status RoleBasedGroupWarmupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RoleBasedGroupWarmupList contains a list of RoleBasedGroupWarmup
type RoleBasedGroupWarmupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RoleBasedGroupWarmup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RoleBasedGroupWarmup{}, &RoleBasedGroupWarmupList{})
}

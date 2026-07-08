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

type WarmupJobPhase string

const (
	WarmupJobPhaseNone      WarmupJobPhase = ""
	WarmupJobPhaseRunning   WarmupJobPhase = "Running"
	WarmupJobPhasePaused    WarmupJobPhase = "Paused"
	WarmupJobPhaseCompleted WarmupJobPhase = "Completed"
	WarmupJobPhaseFailed    WarmupJobPhase = "Failed"
)

type ImagePreloadAction struct {
	// Images specifies the container images to be preloaded onto target nodes.
	// Each entry must be a valid image reference (e.g., "registry.example.com/app:v1.0").
	// +kubebuilder:validation:MinItems=1
	// +required
	Images []string `json:"images"`

	// PullSecrets is a list of references to secrets used for pulling any of the images above.
	// +optional
	PullSecrets []corev1.LocalObjectReference `json:"pullSecrets,omitempty"`
}

// CustomizedAction defines user-provided containers to run custom warmup logic
// (e.g., CUDA kernel compilation).
type CustomizedAction struct {
	// Containers to run as part of the warmup Pod. Each container runs alongside
	// image preload containers. The warmup is considered complete when all containers
	// finish successfully.
	// +kubebuilder:validation:MinItems=1
	// +required
	Containers []corev1.Container `json:"containers"`

	// Volumes to mount into the customized containers.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`
}

// WarmupActions defines what warmup operations to perform on a node.
// At least one of imagePreload or customizedAction must be specified.
// NOTE: update this rule when adding new action types.
// +kubebuilder:validation:XValidation:rule="has(self.imagePreload) || has(self.customizedAction)",message="at least one of imagePreload or customizedAction must be specified"
type WarmupActions struct {
	// ImagePreload pulls container images onto the node ahead of time.
	// +optional
	ImagePreload *ImagePreloadAction `json:"imagePreload,omitempty"`

	// CustomizedAction runs user-defined containers for custom warmup logic.
	// +optional
	CustomizedAction *CustomizedAction `json:"customizedAction,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.nodeNames) || has(self.nodeSelector)",message="either nodeNames or nodeSelector must be specified"
// +kubebuilder:validation:XValidation:rule="!(has(self.nodeNames) && has(self.nodeSelector))",message="nodeNames and nodeSelector are mutually exclusive"

// TargetNodes specifies a set of nodes and the warmup actions to perform on them.
type TargetNodes struct {
	// NodeNames is an explicit list of node names to warm up.
	// Mutually exclusive with NodeSelector.
	// +optional
	// +kubebuilder:validation:MinItems=1
	NodeNames []string `json:"nodeNames,omitempty"`

	// NodeSelector selects nodes by their labels.
	// Mutually exclusive with NodeNames.
	// +optional
	// +kubebuilder:validation:MinProperties=1
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	WarmupActions `json:",inline"`
}

// TargetRoleBasedGroup discovers target nodes from an existing RoleBasedGroup resource.
// The controller finds all scheduled Pods in the RBG, groups them by role, and applies
// the corresponding warmup actions to each node.
type TargetRoleBasedGroup struct {
	// Name of the RoleBasedGroup resource in the same namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Roles maps role names to their warmup actions. Only roles listed here will be warmed up.
	// +kubebuilder:validation:MinProperties=1
	Roles map[string]WarmupActions `json:"roles"`
}

// WarmupPolicies controls the execution behavior of the warmup job.
// +kubebuilder:validation:XValidation:rule="!has(self.maxFailedNodes) || has(self.backoffLimitPerNode)",message="backoffLimitPerNode must be set when maxFailedNodes is specified"
type WarmupPolicies struct {
	// Parallelism limits how many nodes are warmed up concurrently.
	// If not set, all nodes are warmed up in parallel.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Parallelism *int32 `json:"parallelism,omitempty"`

	// BackoffLimitPerNode specifies the maximum number of retries for each node
	// before marking that node as permanently failed.
	// If not set (nil), nodes are retried indefinitely.
	// If set to 0, no retries are attempted (fail on first failure).
	// +optional
	// +kubebuilder:validation:Minimum=0
	BackoffLimitPerNode *int32 `json:"backoffLimitPerNode,omitempty"`

	// MaxFailedNodes specifies the maximum number of permanently-failed nodes
	// that can be tolerated before the entire warmup job is marked as Failed.
	// A node is permanently failed when its failure count exceeds BackoffLimitPerNode.
	// If not set (nil), any number of node failures is tolerated and the job
	// will still be marked Completed once all nodes reach a terminal state.
	// If set to 0, the job fails immediately when any node permanently fails.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxFailedNodes *int32 `json:"maxFailedNodes,omitempty"`

	// GlobalTimeoutSeconds specifies the overall timeout for the warmup job,
	// measured from the time the first Pod is created (status.startTime).
	// When the timeout is exceeded, all active Pods are deleted and the job
	// is marked as Failed regardless of other settings.
	// If not set (nil), no timeout is applied.
	// +optional
	// +kubebuilder:validation:Minimum=1
	GlobalTimeoutSeconds *int64 `json:"globalTimeoutSeconds,omitempty"`

	// TTLSecondsAfterFinished is the time-to-live in seconds after the warmup job
	// reaches a terminal phase (Completed or Failed). When the TTL expires, the
	// RoleBasedGroupWarmup resource and its owned Pods are automatically deleted.
	// If not set, the resource is NOT auto-deleted.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// RoleBasedGroupWarmupSpec defines the desired state of RoleBasedGroupWarmup.
// Exactly one of targetNodes or targetRoleBasedGroup must be specified.
// +kubebuilder:validation:XValidation:rule="has(self.targetNodes) || has(self.targetRoleBasedGroup)",message="either targetNodes or targetRoleBasedGroup must be specified"
// +kubebuilder:validation:XValidation:rule="!(has(self.targetNodes) && has(self.targetRoleBasedGroup))",message="targetNodes and targetRoleBasedGroup are mutually exclusive"
type RoleBasedGroupWarmupSpec struct {
	// Paused suspends the warmup job. When set to true, no new warmup Pods will be created,
	// but existing Pods are not affected.
	// +optional
	Paused *bool `json:"paused,omitempty"`

	// Policies controls parallelism, TTL and other execution behavior.
	// +optional
	Policies *WarmupPolicies `json:"policies,omitempty"`

	// TargetNodes specifies explicit nodes and warmup actions to perform on them.
	// +optional
	TargetNodes *TargetNodes `json:"targetNodes,omitempty"`

	// TargetRoleBasedGroup specifies a RoleBasedGroup resource to discover nodes from.
	// The controller will find all Pods belonging to the RoleBasedGroup, extract their nodes,
	// and perform role-specific warmup actions. Multiple warmup actions for the same node
	// will be merged into a single warmup Pod.
	// +optional
	TargetRoleBasedGroup *TargetRoleBasedGroup `json:"targetRoleBasedGroup,omitempty"`

	// Tolerations allow warmup Pods to be scheduled on tainted nodes (e.g., GPU nodes).
	// If not specified, warmup Pods will not tolerate any taints.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// RoleBasedGroupWarmupStatus defines the observed state of RoleBasedGroupWarmup.
type RoleBasedGroupWarmupStatus struct {
	// StartTime is the time when the first warmup Pod was created.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is the time when all warmup Pods reached a terminal state.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Phase is the current phase of the warmup job.
	Phase WarmupJobPhase `json:"phase"`

	// Desired is the total number of nodes that need to be warmed up.
	Desired int32 `json:"desired"`

	// Active is the number of nodes with warmup Pods currently running.
	Active int32 `json:"active"`

	// Succeeded is the number of nodes whose warmup Pod completed successfully.
	Succeeded int32 `json:"succeeded"`

	// Failed is the number of nodes whose warmup Pod failed.
	Failed int32 `json:"failed"`

	// Conditions represent the latest observations of the warmup job's state.
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

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

package v1alpha2

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// RoleTemplate defines a reusable Pod template that can be referenced by roles.
type RoleTemplate struct {
	// Name is the unique identifier for this template.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Template defines the Pod template specification.
	// +kubebuilder:validation:Required
	Template corev1.PodTemplateSpec `json:"template"`
}

// TemplateRef references a RoleTemplate defined in spec.roleTemplates
// with optional customizations via strategic merge patch.
type TemplateRef struct {
	// Name of the RoleTemplate to reference.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Patch specifies modifications to apply to the referenced template.
	// Uses strategic merge patch semantics.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Patch *runtime.RawExtension `json:"patch,omitempty"`
}

// RoleBasedGroupSpec defines the desired state of RoleBasedGroup.
type RoleBasedGroupSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:Required
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	Roles []RoleSpec `json:"roles" patchStrategy:"merge" patchMergeKey:"name"`

	// RoleTemplates defines reusable Pod templates that can be referenced by roles.
	// +optional
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	RoleTemplates []RoleTemplate `json:"roleTemplates,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
}

// RolloutStrategy defines the strategy that the rbg controller
// will use to perform replica updates of role.
type RolloutStrategy struct {
	// Type defines the rollout strategy, it can only be "RollingUpdate" for now.
	// +kubebuilder:validation:Enum={RollingUpdate}
	// +kubebuilder:default=RollingUpdate
	Type RolloutStrategyType `json:"type"`

	// RollingUpdate defines the parameters to be used when type is RollingUpdateStrategyType.
	// +optional
	RollingUpdate *RollingUpdate `json:"rollingUpdate,omitempty"`
}

// RolloutStrategyType defines the strategy type for rollout.
type RolloutStrategyType string

const (
	// RollingUpdateStrategyType - Replace pods one by one.
	RollingUpdateStrategyType RolloutStrategyType = "RollingUpdate"
)

// UpdateStrategyType defines the strategy type for in-place update.
type UpdateStrategyType string

const (
	// RecreatePodUpdateStrategyType - Recreate pods during update.
	RecreatePodUpdateStrategyType UpdateStrategyType = "RecreatePod"

	// InPlaceIfPossibleUpdateStrategyType - Try in-place update first, recreate if not possible.
	InPlaceIfPossibleUpdateStrategyType UpdateStrategyType = "InPlaceIfPossible"

	// InPlaceOnlyUpdateStrategyType - Only use in-place update.
	InPlaceOnlyUpdateStrategyType UpdateStrategyType = "InPlaceOnly"
)

// RollingUpdate defines the parameters to be used for RollingUpdateStrategyType.
type RollingUpdate struct {
	// Type indicates the type of the InstanceSetUpdateStrategy.
	// Default is InPlaceIfPossible.
	Type UpdateStrategyType `json:"type,omitempty"`

	// Partition indicates the ordinal at which the role should be partitioned for updates.
	// +optional
	// +kubebuilder:default=0
	Partition *intstr.IntOrString `json:"partition,omitempty"`

	// The maximum number of replicas that can be unavailable during the update.
	// +kubebuilder:validation:XIntOrString
	// +kubebuilder:default=1
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`

	// The maximum number of replicas that can be scheduled above the original number of replicas.
	// +kubebuilder:validation:XIntOrString
	// +kubebuilder:default=0
	MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`

	// Paused indicates that the InstanceSet is paused.
	Paused bool `json:"paused,omitempty"`

	// InPlaceUpdateStrategy contains strategies for in-place update.
	InPlaceUpdateStrategy *InPlaceUpdateStrategy `json:"inPlaceUpdateStrategy,omitempty"`
}

// InPlaceUpdateStrategy defines strategies for in-place update.
type InPlaceUpdateStrategy struct {
	// GracePeriodSeconds is the timespan between set Pod status to not-ready and update images in Pod spec.
	GracePeriodSeconds int32 `json:"gracePeriodSeconds,omitempty"`
}

// RestartPolicyType defines the restart policy type.
type RestartPolicyType string

const (
	// RestartPolicyNone - No restart policy.
	RestartPolicyNone RestartPolicyType = "None"

	// RecreateRBGOnPodRestart - Recreate the entire RBG on pod restart.
	RecreateRBGOnPodRestart RestartPolicyType = "RecreateRBGOnPodRestart"

	// RecreateRoleInstanceOnPodRestart - Recreate the role instance on pod restart.
	RecreateRoleInstanceOnPodRestart RestartPolicyType = "RecreateRoleInstanceOnPodRestart"
)

// RoleSpec defines the specification for a role in the group
// +kubebuilder:validation:XValidation:rule="!(has(self.standalonePattern) && has(self.leaderWorkerPattern))",message="standalonePattern and leaderWorkerPattern are mutually exclusive"
type RoleSpec struct {
	// Unique identifier for the role
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Map of string keys and values that can be used to organize and categorize objects.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations is an unstructured key value map stored with a resource.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas"`

	// RolloutStrategy defines the strategy that will be applied to update replicas.
	// +optional
	RolloutStrategy *RolloutStrategy `json:"rolloutStrategy,omitempty"`

	// RestartPolicy defines the restart policy when pod failures happen.
	// +kubebuilder:validation:Enum={None,RecreateRBGOnPodRestart,RecreateRoleInstanceOnPodRestart}
	// +optional
	RestartPolicy RestartPolicyType `json:"restartPolicy,omitempty"`

	// Dependencies of the role
	// +optional
	Dependencies []string `json:"dependencies,omitempty"`

	// Workload type specification
	// Deprecated: This field is deprecated and will be removed in future versions.
	// The underlying workload will use InstanceSet.
	// +kubebuilder:default={apiVersion:"workloads.x-k8s.io/v1alpha2", kind:"RoleInstanceSet"}
	// +optional
	Workload WorkloadSpec `json:"workload,omitempty"`

	// Pattern defines the deployment pattern for this role (inline).
	// Either standalonePattern or leaderWorkerPattern can be specified, not both.
	// +optional
	Pattern `json:",inline"`

	// Components describe the components that will be created.
	// +optional
	Components []InstanceComponent `json:"components,omitempty"`

	// +optional
	ServicePorts []corev1.ServicePort `json:"servicePorts,omitempty"`

	// +optional
	EngineRuntimes []EngineRuntime `json:"engineRuntimes,omitempty"`

	// +optional
	ScalingAdapter *ScalingAdapter `json:"scalingAdapter,omitempty"`

	// MinReadySeconds is the minimum number of seconds for which a newly created pod/instance should be ready.
	// +optional
	// +kubebuilder:default=0
	MinReadySeconds int32 `json:"minReadySeconds,omitempty" protobuf:"varint,9,opt,name=minReadySeconds"`
}

// Pattern defines the deployment pattern for a role.
// Only one of standalonePattern or leaderWorkerPattern can be specified.
type Pattern struct {
	// StandalonePattern defines a single-pod pattern.
	// +optional
	StandalonePattern *StandalonePattern `json:"standalonePattern,omitempty"`

	// LeaderWorkerPattern defines a multi-pod pattern with leader and workers.
	// +optional
	LeaderWorkerPattern *LeaderWorkerPattern `json:"leaderWorkerPattern,omitempty"`
}

// TemplateSource defines either an inline template or a reference to a RoleTemplate.
// Only one of its members may be specified.
// +kubebuilder:validation:XValidation:rule="!(has(self.template) && has(self.templateRef))",message="template and templateRef are mutually exclusive"
type TemplateSource struct {
	// Template defines the Pod template specification inline.
	// +optional
	Template *corev1.PodTemplateSpec `json:"template,omitempty"`

	// TemplateRef references a RoleTemplate from spec.roleTemplates with optional patch.
	// +optional
	TemplateRef *TemplateRef `json:"templateRef,omitempty"`
}

// StandalonePattern defines the standalone deployment pattern (single pod per replica).
type StandalonePattern struct {
	// TemplateSource defines the Pod template source, either inline or via reference.
	// +optional
	TemplateSource `json:",inline"`
}

// LeaderWorkerPattern defines the leader-worker deployment pattern (multiple pods per replica).
type LeaderWorkerPattern struct {
	// Size is the total number of pods in each group.
	// The minimum is 1 which represents the leader.
	// +optional
	// +kubebuilder:default=1
	Size *int32 `json:"size,omitempty"`

	// TemplateSource defines the Pod template source, either inline or via reference.
	// +optional
	TemplateSource `json:",inline"`

	// LeaderTemplatePatch indicates patching for the leader template.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	LeaderTemplatePatch *runtime.RawExtension `json:"leaderTemplatePatch,omitempty"`

	// WorkerTemplatePatch indicates patching for the worker template.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	WorkerTemplatePatch *runtime.RawExtension `json:"workerTemplatePatch,omitempty"`
}

type WorkloadSpec struct {
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/v[0-9]+((alpha|beta)[0-9]+)?$`
	// +kubebuilder:default="workloads.x-k8s.io/v1alpha1"
	APIVersion string `json:"apiVersion"`

	// +optional
	// +kubebuilder:default="InstanceSet"
	Kind string `json:"kind"`
}

func (w *WorkloadSpec) String() string {
	return fmt.Sprintf("%s/%s", w.APIVersion, w.Kind)
}

type EngineRuntime struct {
	// ProfileName specifies the name of the engine runtime profile to be used
	ProfileName string `json:"profileName"`

	// InjectContainers specifies the containers to be injected with the engine runtime
	// +optional
	InjectContainers []string `json:"injectContainers,omitempty"`

	// Containers specifies the engine runtime containers to be overridden
	Containers []corev1.Container `json:"containers,omitempty"`
}

type InstanceComponent struct {
	// Name is the type name of the component.
	Name string `json:"name"`

	// Size is the number of replicas for Pods that match the PodRule.
	Size *int32 `json:"size,omitempty"`

	// ServiceName is the name of the service that governs this Instance Component.
	ServiceName string `json:"serviceName,omitempty"`

	// Template is the template for the component pods.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Template corev1.PodTemplateSpec `json:"template"`
}

type ScalingAdapter struct {
	// Enable indicates whether the ScalingAdapter is enabled for the Role.
	// +optional
	// +kubebuilder:default=false
	Enable bool `json:"enable,omitempty"`
}

// RoleBasedGroupStatus defines the observed state of RoleBasedGroup.
type RoleBasedGroupStatus struct {
	// The generation observed by the controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions track the condition of the RBG
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Status of individual roles
	RoleStatuses []RoleStatus `json:"roleStatuses"`
}

// RoleStatus shows the current state of a specific role
type RoleStatus struct {
	// Name of the role
	Name string `json:"name"`

	// Number of ready replicas
	ReadyReplicas int32 `json:"readyReplicas"`

	// Total number of desired replicas
	Replicas int32 `json:"replicas"`

	// Total number of updated replicas
	UpdatedReplicas int32 `json:"updatedReplicas"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:shortName={rbg}

// RoleBasedGroup is the Schema for the rolebasedgroups API.
type RoleBasedGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RoleBasedGroupSpec   `json:"spec,omitempty"`
	Status RoleBasedGroupStatus `json:"status,omitempty"`
}

type RoleBasedGroupConditionType string

// These are built-in conditions of a RBG.
const (
	// RoleBasedGroupReady means the rbg is available.
	RoleBasedGroupReady RoleBasedGroupConditionType = "Ready"

	// RoleBasedGroupProgressing means rbg is progressing.
	RoleBasedGroupProgressing RoleBasedGroupConditionType = "Progressing"

	// RoleBasedGroupRollingUpdateInProgress means rbg is performing a rolling update.
	RoleBasedGroupRollingUpdateInProgress RoleBasedGroupConditionType = "RollingUpdateInProgress"

	// RoleBasedGroupRestartInProgress means rbg is restarting.
	RoleBasedGroupRestartInProgress RoleBasedGroupConditionType = "RestartInProgress"
)

// +kubebuilder:object:root=true

// RoleBasedGroupList contains a list of RoleBasedGroup.
type RoleBasedGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RoleBasedGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RoleBasedGroup{}, &RoleBasedGroupList{})
}

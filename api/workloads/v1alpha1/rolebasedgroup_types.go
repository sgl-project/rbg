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

// TemplateRef references a RoleTemplate defined in spec.roleTemplates.
type TemplateRef struct {
	// Name of the RoleTemplate to reference.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// TemplateSource defines either an inline template or a reference to a RoleTemplate.
// Only one of its members may be specified.
// +kubebuilder:validation:XValidation:rule="!(has(self.template) && has(self.templateRef))",message="template and templateRef are mutually exclusive"
type TemplateSource struct {
	// Template defines the Pod template specification inline.
	// Required when templateRef is not set.
	// +optional
	Template *corev1.PodTemplateSpec `json:"template,omitempty"`

	// TemplateRef references a RoleTemplate from spec.roleTemplates.
	// When set, the Pod template is derived by merging the referenced template with templatePatch.
	// Cannot be used together with template field.
	// +optional
	TemplateRef *TemplateRef `json:"templateRef,omitempty"`
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

	// Configuration for the PodGroup to enable gang-scheduling via supported plugins.
	PodGroupPolicy *PodGroupPolicy `json:"podGroupPolicy,omitempty"`

	// CoordinationRequirements describes the requirements of coordination strategies for some specified roles.
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	CoordinationRequirements []Coordination `json:"coordination,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
}

// Coordination describes the requirements of coordination strategies for roles.
type Coordination struct {
	// Name of the coordination.
	Name string `json:"name"`

	// Roles that should be constrained by this coordination.
	Roles []string `json:"roles"`

	// RolloutStrategy describes the coordination strategies.
	Strategy *CoordinationStrategy `json:"strategy,omitempty"`
}

type CoordinationStrategy struct {
	// RollingUpdate defines the coordination strategies about rolling update.
	RollingUpdate *CoordinationRollingUpdate `json:"rollingUpdate,omitempty"`
}

// CoordinationRollingUpdate describes the rolling update coordination strategy.
type CoordinationRollingUpdate struct {
	// MaxSkew defines the max skew requirement about updated replicas between the roles when rolling update.
	// For example, one RoleBasedGroup with (200 prefills, 100 decodes) will have the
	// constraint `abs(updated_prefills/200 - updated_decodes/100) <= MaxSkew`.
	// Only support percentage value, and defaults to nil.
	//
	// +kubebuilder:validation:Pattern=`^([0-9]|[1-9][0-9]|100)%$`
	MaxSkew *string `json:"maxSkew,omitempty"`

	// Partition indicates the replicas at which the role should be partitioned for rolling update.
	// If Partition is not nil, the Partition of the roles' rolloutStrategy will be overridden by this field.
	// Only support percentage value, and defaults to nil.
	//
	// +kubebuilder:validation:Pattern=`^([0-9]|[1-9][0-9]|100)%$`
	Partition *string `json:"partition,omitempty"`

	// MaxUnavailable defines the updating step during rolling. If MaxUnavailable is not nil,
	// the MaxUnavailable of the roles' rolloutStrategy will be overridden by this field.
	// Only support percentage value, and defaults to nil.
	//
	// +kubebuilder:validation:Pattern=`^([0-9]|[1-9][0-9]|100)%$`
	MaxUnavailable *string `json:"maxUnavailable,omitempty"`
}

// PodGroupPolicy represents a PodGroup configuration for gang-scheduling.
type PodGroupPolicy struct {
	// Configuration for gang-scheduling using various plugins.
	PodGroupPolicySource `json:",inline"`
}

// PodGroupPolicySource represents supported plugins for gang-scheduling.
// Only one of its members may be specified.
type PodGroupPolicySource struct {
	// KubeScheduling plugin from the Kubernetes scheduler-plugins for gang-scheduling.
	KubeScheduling *KubeSchedulingPodGroupPolicySource `json:"kubeScheduling,omitempty"`

	VolcanoScheduling *VolcanoSchedulingPodGroupPolicySource `json:"volcanoScheduling,omitempty"`
}

// KubeSchedulingPodGroupPolicySource represents configuration for  Kubernetes scheduling plugin.
// The number of min members in the PodGroupSpec is always equal to the number of rbg pods.
type KubeSchedulingPodGroupPolicySource struct {
	// Time threshold to schedule PodGroup for gang-scheduling.
	// If the scheduling timeout is equal to 0, the default value is used.
	// Defaults to 60 seconds.
	// +kubebuilder:default=60
	ScheduleTimeoutSeconds *int32 `json:"scheduleTimeoutSeconds,omitempty"`
}

// VolcanoSchedulingPodGroupPolicySource represents configuration for volcano podgroup scheduling plugin
type VolcanoSchedulingPodGroupPolicySource struct {
	// If specified, indicates the PodGroup's priority. "system-node-critical" and
	// "system-cluster-critical" are two special keywords which indicate the
	// highest priorities with the former being the highest priority. Any other
	// name must be defined by creating a PriorityClass object with that name.
	// If not specified, the PodGroup priority will be default or zero if there is no
	// default.
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// Queue defines the queue to allocate resource for PodGroup; if queue does not exist,
	// the PodGroup will not be scheduled. Defaults to `default` Queue with the lowest weight.
	// +optional
	Queue string `json:"queue,omitempty"`
}

// RolloutStrategy defines the strategy that the rbg controller
// will use to perform replica updates of role.
type RolloutStrategy struct {
	// Type defines the rollout strategy, it can only be “RollingUpdate” for now.
	//
	// +kubebuilder:validation:Enum={RollingUpdate}
	// +kubebuilder:default=RollingUpdate
	Type RolloutStrategyType `json:"type"`

	// RollingUpdate defines the parameters to be used when type is RollingUpdateStrategyType.
	// +optional
	RollingUpdate *RollingUpdate `json:"rollingUpdate,omitempty"`
}

// RollingUpdate defines the parameters to be used for RollingUpdateStrategyType.
type RollingUpdate struct {
	// Type indicates the type of the InstanceSetUpdateStrategy.
	// Default is InPlaceIfPossible.
	Type UpdateStrategyType `json:"type,omitempty"`

	// Partition indicates the ordinal at which the role should be partitioned for updates.
	// During a rolling update, all the groups from ordinal Partition to Replicas-1 will be updated.
	// The groups from 0 to Partition-1 will not be updated.
	// This is helpful in incremental rollout strategies like canary deployments
	// or interactive rollout strategies for multiple replicas like xPyD deployments.
	// Once partition field and maxSurge field both set, the bursted replicas will keep remaining
	// until the rolling update is completely done and the partition field is reset to 0.
	// This is as expected to reduce the reconciling complexity.
	// The default value is 0.
	//
	// +optional
	// +kubebuilder:default=0
	Partition *intstr.IntOrString `json:"partition,omitempty"`

	// The maximum number of replicas that can be unavailable during the update.
	// Value can be an absolute number (ex: 5) or a percentage of total replicas at the start of update (ex: 10%).
	// Absolute number is calculated from percentage by rounding down.
	// This can not be 0 if MaxSurge is 0.
	// By default, a fixed value of 1 is used.
	// Example: when this is set to 30%, the old replicas can be scaled down by 30%
	// immediately when the rolling update starts. Once new replicas are ready, old replicas
	// can be scaled down further, followed by scaling up the new replicas, ensuring
	// that at least 70% of original number of replicas are available at all times
	// during the update.
	//
	// +kubebuilder:validation:XIntOrString
	// +kubebuilder:default=1
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`

	// The maximum number of replicas that can be scheduled above the original number of
	// replicas.
	// Value can be an absolute number (ex: 5) or a percentage of total replicas at
	// the start of the update (ex: 10%).
	// Absolute number is calculated from percentage by rounding up.
	// By default, a value of 0 is used.
	// Example: when this is set to 30%, the new replicas can be scaled up by 30%
	// immediately when the rolling update starts. Once old replicas have been deleted,
	// new replicas can be scaled up further, ensuring that total number of replicas running
	// at any time during the update is at most 130% of original replicas.
	// When rolling update completes, replicas will fall back to the original replicas.
	//
	// +kubebuilder:validation:XIntOrString
	// +kubebuilder:default=0
	MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`

	// Paused indicates that the InstanceSet is paused.
	// Default value is false
	Paused bool `json:"paused,omitempty"`

	// InPlaceUpdateStrategy contains strategies for in-place update.
	InPlaceUpdateStrategy *InPlaceUpdateStrategy `json:"inPlaceUpdateStrategy,omitempty"`
}

// RoleSpec defines the specification for a role in the group
// +kubebuilder:validation:XValidation:rule="!has(self.templateRef) || !has(self.workload) || self.workload.kind != 'InstanceSet'",message="templateRef is not supported for InstanceSet workloads"
// Note: "templatePatch is only valid when templateRef is set" validation is done in controller
// because templatePatch is runtime.RawExtension (x-kubernetes-preserve-unknown-fields) which CEL cannot inspect
type RoleSpec struct {
	// Unique identifier for the role
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Map of string keys and values that can be used to organize and categorize
	// (scope and select) objects. May match selectors of replication controllers
	// and services.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations is an unstructured key value map stored with a resource that may be
	// set by external tools to store and retrieve arbitrary metadata. They are not
	// queryable and should be preserved when modifying objects.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas"`

	// RolloutStrategy defines the strategy that will be applied to update replicas
	// when a revision is made to the leaderWorkerTemplate.
	// +optional
	RolloutStrategy *RolloutStrategy `json:"rolloutStrategy,omitempty"`

	// RestartPolicy defines the restart policy when pod failures happen.
	// The default value is RecreateRoleInstanceOnPodRestart for LWS and None for STS & Deploy. Therefore, no default value is set.
	// +kubebuilder:validation:Enum={None,RecreateRBGOnPodRestart,RecreateRoleInstanceOnPodRestart}
	// +optional
	RestartPolicy RestartPolicyType `json:"restartPolicy,omitempty"`

	// Dependencies of the role
	// +optional
	Dependencies []string `json:"dependencies,omitempty"`

	// Workload type specification
	// +kubebuilder:default={apiVersion:"apps/v1", kind:"StatefulSet"}
	// +optional
	Workload WorkloadSpec `json:"workload,omitempty"`

	// TemplateSource defines the Pod template source, either inline or via reference.
	// +optional
	TemplateSource `json:",inline"`

	// TemplatePatch specifies modifications to apply to the referenced template.
	// Uses strategic merge patch semantics.
	// Required when templateRef is set, use empty object ({}) for no modifications.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	TemplatePatch runtime.RawExtension `json:"templatePatch,omitempty"`

	// LeaderWorkerSet template
	// +optional
	LeaderWorkerSet *LeaderWorkerTemplate `json:"leaderWorkerSet,omitempty"`

	// Components describe the components that will be created.
	// +optional
	Components []InstanceComponent `json:"components,omitempty"`

	// +optional
	ServicePorts []corev1.ServicePort `json:"servicePorts,omitempty"`

	// +optional
	EngineRuntimes []EngineRuntime `json:"engineRuntimes,omitempty"`

	// +optional
	ScalingAdapter *ScalingAdapter `json:"scalingAdapter,omitempty"`

	// MinReadySeconds is the minimum number of seconds for which a newly created pod/instance should be ready
	// without any of its container crashing for it to be considered available.
	// Defaults to 0 (pod will be considered available as soon as it is ready)
	// +optional
	// +kubebuilder:default=0
	MinReadySeconds int32 `json:"minReadySeconds,omitempty" protobuf:"varint,9,opt,name=minReadySeconds"`
}

type WorkloadSpec struct {
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/v[0-9]+((alpha|beta)[0-9]+)?$`
	// +kubebuilder:default="apps/v1"
	APIVersion string `json:"apiVersion"`

	// +optional
	// +kubebuilder:default="StatefulSet"
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

	// Containers specifies the engine runtime containers to be overridden, only support command,args overridden
	Containers []corev1.Container `json:"containers,omitempty"`
}

type LeaderWorkerTemplate struct {
	// Number of pods to create. It is the total number of pods in each group.
	// The minimum is 1 which represent the leader. When set to 1, the leader
	// pod is created for each group as well as a 0-replica StatefulSet for the workers.
	// Default to 1.
	//
	// +optional
	// +kubebuilder:default=1
	Size *int32 `json:"size,omitempty"`

	// PatchLeaderTemplate indicates patching LeaderTemplate.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	PatchLeaderTemplate *runtime.RawExtension `json:"patchLeaderTemplate,omitempty"`

	// PatchWorkerTemplate indicates patching WorkerTemplate.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	PatchWorkerTemplate *runtime.RawExtension `json:"patchWorkerTemplate,omitempty"`
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
	// RoleBasedGroupReady means the rbg is available, ie, at least the
	// minimum available groups are up and running.
	RoleBasedGroupReady RoleBasedGroupConditionType = "Ready"

	// RoleBasedGroupProgressing means rbg is progressing. Progress for a
	// rbg replica is considered when a new group is created, and when new pods
	// scale up and down. Before a group has all its pods ready, the group itself
	// will be in progressing state. And any group in progress will make
	// the rbg as progressing state.
	RoleBasedGroupProgressing RoleBasedGroupConditionType = "Progressing"

	// RoleBasedGroupRollingUpdateInProgress means rbg is performing a rolling update. UpdateInProgress
	// is true when the rbg is in upgrade process after the (leader/worker) template is updated. If only replicas is modified, it will
	// not be considered as UpdateInProgress.
	RoleBasedGroupRollingUpdateInProgress RoleBasedGroupConditionType = "RollingUpdateInProgress"

	// RoleBasedGroupRestartInProgress means rbg is restarting. RestartInProgress
	// is true when the rbg is in restart process after the pod is deleted or the container is restarted.
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

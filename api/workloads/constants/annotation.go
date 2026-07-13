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

package constants

// ========== Annotations ==========

// Group level annotations
const (
	// GroupExclusiveTopologyKey declares the topology domain (e.g. kubernetes.io/hostname)
	// for 1:1 exclusive scheduling.
	GroupExclusiveTopologyKey = RBGPrefix + "group-exclusive-topology"

	// DisableExclusiveKeyAnnotationKey can be set to "true" on a Pod template
	// to skip exclusive-topology affinity injection for that pod.
	DisableExclusiveKeyAnnotationKey = RBGPrefix + "role-disable-exclusive"

	// GangSchedulingAnnotationKey enables gang scheduling for a RoleBasedGroup when set to "true".
	// When enabled, the controller will create a PodGroup CR managed by the scheduler
	// configured via --scheduler-name flag (scheduler-plugins or volcano).
	// Setting this annotation automatically derives RoleInstanceGangSchedulingAnnotationKey
	// for each role's RoleInstanceSet, so they must NOT be set simultaneously.
	// Example: rbg.workloads.x-k8s.io/group-gang-scheduling: "true"
	GangSchedulingAnnotationKey = RBGPrefix + "group-gang-scheduling"

	// GangSchedulingScheduleTimeoutSecondsKey specifies the schedule timeout seconds for
	// scheduler-plugins based gang scheduling. Defaults to 60 seconds if not set.
	// Example: rbg.workloads.x-k8s.io/group-gang-scheduling-timeout: "120"
	GangSchedulingScheduleTimeoutSecondsKey = RBGPrefix + "group-gang-scheduling-timeout"

	// GangSchedulingVolcanoPriorityClassKey specifies the PriorityClassName for volcano gang scheduling.
	// Example: rbg.workloads.x-k8s.io/group-gang-scheduling-volcano-priority: "system-node-critical"
	GangSchedulingVolcanoPriorityClassKey = RBGPrefix + "group-gang-scheduling-volcano-priority"

	// GangSchedulingVolcanoQueueKey specifies the Queue for volcano gang scheduling.
	// Example: rbg.workloads.x-k8s.io/group-gang-scheduling-volcano-queue: "default"
	GangSchedulingVolcanoQueueKey = RBGPrefix + "group-gang-scheduling-volcano-queue"
)

// Role level annotations
const (
	// RoleSizeAnnotationKey identifies the role replica size
	RoleSizeAnnotationKey = RBGPrefix + "role-size"

	// RoleDisableExclusiveKey can be set to "true" on a Role template
	// to skip exclusive-topology affinity injection for that role.
	RoleDisableExclusiveKey = RBGPrefix + "role-disable-exclusive"

	// RoleWorkloadTypeAnnotationKey specifies the workload type for a role.
	// This is primarily used by the conversion webhook when converting v1alpha1
	// RoleBasedGroups that had workload field set. New v1alpha2 RBGs should
	// not use this annotation as the default (RoleInstanceSet) is appropriate.
	// Format: "apiVersion/kind" e.g., "apps/v1/StatefulSet"
	// Example: rbg.workloads.x-k8s.io/role-workload-type: "apps/v1/StatefulSet"
	// WARNING: apiVersion may contain "/" (e.g. "leaderworkerset.x-k8s.io/v1"),
	// so parsing must use strings.LastIndex to correctly split at the last "/"
	// as the apiVersion/kind delimiter. Do NOT use strings.Split.
	RoleWorkloadTypeAnnotationKey = RBGPrefix + "role-workload-type"
)

// SystemManagedRoleAnnotations is the set of role-level annotations that are
// managed by the control plane and must not be propagated to downstream
// workload/Pod metadata or overridden by user-provided values.
var SystemManagedRoleAnnotations = map[string]struct{}{
	RoleSizeAnnotationKey:         {},
	RoleWorkloadTypeAnnotationKey: {},
}

// IsSystemManagedRoleAnnotation reports whether the given annotation key is
// a system-managed role annotation that should be filtered out when copying
// role annotations to downstream resources.
func IsSystemManagedRoleAnnotation(key string) bool {
	_, ok := SystemManagedRoleAnnotations[key]
	return ok
}

// RoleInstance level annotations
const (
	// RoleInstancePatternKey identifies the RoleInstance organization pattern (Stateful/Stateless)
	RoleInstancePatternKey = RBGPrefix + "role-instance-pattern"

	// RoleInstanceGangSchedulingAnnotationKey enables gang-scheduling aware behavior at the
	// RoleInstance level when set to "true". It is derived automatically from the RBG-level
	// GangSchedulingAnnotationKey annotation during RoleInstanceSet reconciliation, but users
	// can also set it explicitly in role.Annotations within the RBG spec.
	//
	// NOTE: This annotation must NOT be set on the RBG object (metadata.annotations) directly
	// when GangSchedulingAnnotationKey is already set, as they are mutually exclusive at the
	// RBG level. Use either GangSchedulingAnnotationKey (group-level) or set
	// RoleInstanceGangSchedulingAnnotationKey per role via role.Annotations, not both.
	//
	// When enabled, the RoleInstance controller enforces gang-scheduling constraints:
	//   1. If any orphan pod (not yet GC'd) exists, pod creation fails immediately instead
	//      of silently skipping — preventing partial group startup.
	//   2. If an in-place update cannot be applied to a pod, all pods of the instance are
	//      recreated atomically so the PodGroup minimum member requirement is met.
	//
	// Example: rbg.workloads.x-k8s.io/role-instance-gang-scheduling: "true"
	RoleInstanceGangSchedulingAnnotationKey = RBGPrefix + "role-instance-gang-scheduling"

	// DiscoveryConfigModeAnnotationKey identifies discovery config handling mode.
	DiscoveryConfigModeAnnotationKey = RBGPrefix + "discovery-config-mode"
)

// Lifecycle management annotations
const (
	// RoleInstanceSetLifecycleStateKey identifies the lifecycle state of a RoleInstance (stored in labels)
	RoleInstanceSetLifecycleStateKey = RBGPrefix + "role-instance-lifecycle-state"

	// RoleInstanceSetLifecycleTimestampKey identifies the lifecycle state change timestamp of a RoleInstance
	RoleInstanceSetLifecycleTimestampKey = RBGPrefix + "role-instance-lifecycle-timestamp"
)

// InPlace update annotations
const (
	// InPlaceUpdateStateKey identifies the in-place update state
	InPlaceUpdateStateKey = RBGPrefix + "inplace-update-state"

	// InPlaceUpdateGraceKey identifies the in-place update grace period configuration
	InPlaceUpdateGraceKey = RBGPrefix + "inplace-update-grace"

	// RoleInplaceUpdateGracePeriodSecondsKey propagates the in-place update grace
	// period (seconds) from RoleInstanceSet to RoleInstance, so the RoleInstance
	// controller can honor the configured delay.
	// Example: rbg.workloads.x-k8s.io/inplace-update-grace-period-seconds: "30"
	RoleInplaceUpdateGracePeriodSecondsKey = RBGPrefix + "inplace-update-grace-period-seconds"

	// RuntimeContainerMetaKey is a key in pod annotations. Some inplace update scene should report the
	// states of runtime containers into its value, which is a structure JSON of RuntimeContainerMetaSet type.
	RuntimeContainerMetaKey = "workloads.x-k8s.io/runtime-containers-meta"
)

// Component level annotations
const (
	// RestartTriggerPolicyAnnotationKey specifies whether a component's
	// pod restart/failure events should trigger the role's restart policy
	// (RecreateRoleInstanceOnPodRestart).
	// Valid values are:
	//   - "Inherit" (or empty): Pod events from this component will follow the role's restart policy.
	//   - "Ignore": Pod events from this component will NOT trigger restart policy.
	// This is useful for auxiliary components (e.g., monitoring, logging sidecars) whose
	// failures should not affect the main workload.
	// Example: rbg.workloads.x-k8s.io/restart-trigger-policy: "Ignore"
	RestartTriggerPolicyAnnotationKey = RBGPrefix + "restart-trigger-policy"
)

// Restart trigger policy values
const (
	// RestartTriggerPolicyInherit means the component's pod events will
	// follow the role's restart policy configuration. This is the default behavior
	// when the annotation is not set or set to an unrecognized value.
	RestartTriggerPolicyInherit = "Inherit"

	// RestartTriggerPolicyIgnore means the component's pod events will
	// NOT trigger the role's restart policy. Use this for auxiliary components
	// whose failures should not cascade to the main workload.
	RestartTriggerPolicyIgnore = "Ignore"
)

// Inplace scheduling annotations and values.
const (
	// RoleInplaceSchedulingAnnotationKey enables in-place scheduling for a role.
	// When set, recreated Pods (due to rolling upgrade or failure recovery) are
	// injected with nodeAffinity to prefer or require scheduling to nodes where
	// the same component type has previously run, enabling reuse of node-local
	// cached resources.
	// Valid values:
	//   - "Preferred": inject preferredDuringSchedulingIgnoredDuringExecution with weight 100.
	//   - "Required": inject requiredDuringSchedulingIgnoredDuringExecution.
	// When not set, no in-place scheduling affinity is injected.
	//
	// Note: when adding to an existing RBG, old RoleInstances lack the label so
	// they are unaffected. New RoleInstances start recording bindings,
	// but no affinity is injected on the first recreation (empty binding).
	// Affinity takes effect on subsequent recreations.
	//
	// Example: rbg.workloads.x-k8s.io/role-inplace-scheduling: "Preferred"
	RoleInplaceSchedulingAnnotationKey = RBGPrefix + "role-inplace-scheduling"

	// InplaceSchedulingPreferred injects preferredDuringSchedulingIgnoredDuringExecution
	// nodeAffinity with weight 100, steering recreated Pods toward historical nodes
	// while allowing fallback to any available node.
	InplaceSchedulingPreferred = "Preferred"

	// InplaceSchedulingRequired injects requiredDuringSchedulingIgnoredDuringExecution
	// nodeAffinity, requiring recreated Pods to land on a historical node.
	// If a binding exists but no historical node is healthy, the Pod remains Pending.
	// On cold start (empty binding store, e.g. after controller restart), no
	// affinity is injected until RecordNodeBindings reseeds the store.
	InplaceSchedulingRequired = "Required"

	// RoleInplaceSchedulingGranularityAnnotationKey controls the binding granularity
	// for in-place scheduling. When not set, the default is auto-detected:
	//   - Stateful mode → Pod
	//   - Stateless mode → Component
	// Example: rbg.workloads.x-k8s.io/role-inplace-scheduling-granularity: "Component"
	RoleInplaceSchedulingGranularityAnnotationKey = RBGPrefix + "role-inplace-scheduling-granularity"

	// InplaceSchedulingGranularityPod uses per-Pod binding: each Pod returns to
	// its own historical node. Key: {rbgUID}/{podName} → single node.
	InplaceSchedulingGranularityPod = "Pod"

	// InplaceSchedulingGranularityComponent uses component-level binding: Pod
	// prefers any node that has hosted the same component type.
	// Key: {rbgUID}/{roleName}-{componentName} → node set.
	InplaceSchedulingGranularityComponent = "Component"

	// RoleInplaceSchedulingAvoidAnnotationKey specifies one or more node label
	// keys. When set, a RequiredDuringSchedulingIgnoredDuringExecution term
	// with DoesNotExist operator is injected for each key, hard-excluding nodes
	// that carry any of these labels.
	// The annotation value is a single label key or a comma-separated list.
	// Example: rbg.workloads.x-k8s.io/role-inplace-scheduling-avoid: "key1,key2"
	RoleInplaceSchedulingAvoidAnnotationKey = RBGPrefix + "role-inplace-scheduling-avoid"
)

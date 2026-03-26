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
)

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

	// RuntimeContainerMetaKey is a key in pod annotations. Some inplace update scene should report the
	// states of runtime containers into its value, which is a structure JSON of RuntimeContainerMetaSet type.
	RuntimeContainerMetaKey = "workloads.x-k8s.io/runtime-containers-meta"
)

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

	// DiscoveryConfigModeAnnotationKey identifies discovery config handling mode.
	DiscoveryConfigModeAnnotationKey = RBGPrefix + "discovery-config-mode"
)

// Lifecycle management annotations
const (
	// LifecycleStateKey identifies the lifecycle state
	LifecycleStateKey = RBGPrefix + "lifecycle-state"

	// LifecycleTimestampKey identifies the lifecycle state change timestamp
	LifecycleTimestampKey = RBGPrefix + "lifecycle-timestamp"
)

// InPlace update annotations
const (
	// InPlaceUpdateStateKey identifies the in-place update state
	InPlaceUpdateStateKey = RBGPrefix + "inplace-state"

	// InPlaceUpdateGraceKey identifies the in-place update grace period configuration
	InPlaceUpdateGraceKey = RBGPrefix + "inplace-grace"
)

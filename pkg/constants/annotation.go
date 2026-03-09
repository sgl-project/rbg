package constants

// ========== Annotations ==========

// Group level annotations
const (
	// GroupExclusiveTopologyKey declares the topology domain (e.g. kubernetes.io/hostname)
	// for 1:1 exclusive scheduling.
	GroupExclusiveTopologyKey = RBGPrefix + "group-exclusive-topology"
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

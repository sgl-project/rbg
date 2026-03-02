package v1alpha2

import v1 "k8s.io/api/core/v1"

const (
	ControllerName = "rolebasedgroup-controller"

	// RBGPrefix Domain prefix for all labels/annotations to avoid conflicts
	RBGPrefix = "rolebasedgroup.workloads.x-k8s.io/"

	// SetNameLabelKey identifies resources belonging to a specific RoleBasedGroup
	// Value: RoleBasedGroup.metadata.name
	SetNameLabelKey = RBGPrefix + "name"

	// SetRoleLabelKey identifies resources belonging to a specific role
	// Value: RoleSpec.name from RoleBasedGroup.spec.roles[]
	SetRoleLabelKey = RBGPrefix + "role"

	// SetGroupUniqueHashLabelKey is set on every Pod and carries a short hash
	// that identifies all Pods belonging to the same RoleBasedGroup instance.
	// Used as the match label for topology affinity.
	SetGroupUniqueHashLabelKey = RBGPrefix + "group-unique-key"

	// ExclusiveKeyAnnotationKey declares the topology domain (e.g. kubernetes.io/hostname)
	// for 1:1 exclusive scheduling.
	ExclusiveKeyAnnotationKey = RBGPrefix + "exclusive-topology"

	// DisableExclusiveKeyAnnotationKey can be set to "true" on a Role template
	// to skip exclusive-topology affinity injection for that role.
	DisableExclusiveKeyAnnotationKey = RBGPrefix + "disable-exclusive-topology"

	// RevisionLabelKey is the labels key used to store the revision hash of the
	// RoleBasedGroup Roles's template.
	RevisionLabelKey = RBGPrefix + "controller-revision-hash"

	// RoleRevisionLabelKeyFmt is the labels key used to store the revision hash of
	// a specific Role template.
	RoleRevisionLabelKeyFmt = RBGPrefix + "role-revision-hash-%s"

	RoleSizeAnnotationKey string = RBGPrefix + "role-size"

	// RBGSetPrefix rbgs prefix for all rbgs
	RBGSetPrefix = "rolebasedgroupset.workloads.x-k8s.io/"

	// SetRBGSetNameLabelKey identifies resources belonging to a specific RoleBasedGroupSet
	SetRBGSetNameLabelKey = RBGSetPrefix + "name"

	// SetRBGIndexLabelKey SetRBGIndex identifies the index of the rbg within the rbgset
	SetRBGIndexLabelKey = RBGSetPrefix + "rbg-index"
)

const ComponentLabelPrefix = "component." + RBGPrefix

// Generic component label keys
const (
	// RBGComponentNameLabelKey identifies the component name (e.g., leader/worker/coordinator)
	RBGComponentNameLabelKey = ComponentLabelPrefix + "name"

	// RBGComponentIndexLabelKey identifies the component instance index
	RBGComponentIndexLabelKey = ComponentLabelPrefix + "index"

	// RBGComponentSizeLabelKey identifies the component size
	RBGComponentSizeLabelKey = ComponentLabelPrefix + "size"
)

const InstanceLabelPrefix = "instance." + RBGPrefix

const InstanceAnnotationPrefix = "instance." + RBGPrefix

// Generic instance annotation keys
const (
	// RBGInstancePatternAnnotationKey identifies the instance organization pattern (Stateful/Stateless)
	RBGInstancePatternAnnotationKey = InstanceAnnotationPrefix + "pattern"
)

// InstancePatternType defines supported organization patterns
type InstancePatternType string

const (
	// StatelessInstancePattern represents stateless (unordered) topology pattern
	StatelessInstancePattern InstancePatternType = "Stateless"

	// StatefulInstancePattern represents stateful (ordered) topology pattern
	StatefulInstancePattern InstancePatternType = "Stateful"
)

const RoleLabelPrefix = "role." + RBGPrefix

// Generic role label keys
const (
	// RBGRoleTemplateTypeLabelKey identifies the role organization pattern
	RBGRoleTemplateTypeLabelKey = RoleLabelPrefix + "template-type"
)

// RBGRoleTemplateType defines supported organization patterns
type RBGRoleTemplateType string

const (
	// ComponentsTemplateType represents template is constructed from role.components field
	ComponentsTemplateType RBGRoleTemplateType = "Components"

	// LeaderWorkerPatternTemplateType represents template is constructed from role.leaderWorkerPattern field
	LeaderWorkerPatternTemplateType RBGRoleTemplateType = "LeaderWorkerPattern"

	// StandalonePatternTemplateType represents template is constructed from role.pattern.standalonePattern field
	StandalonePatternTemplateType RBGRoleTemplateType = "StandalonePattern"

	// PodTemplateTemplateType represents template is constructed from role.template field (deprecated)
	PodTemplateTemplateType RBGRoleTemplateType = "PodTemplate"
)

// LeaderWorkerSet labels and annotations
const (
	LeaderWorkerSetPrefix = "leaderworkerset.sigs.k8s.io/"

	// LwsWorkerIndexLabelKey identifies the worker index in LeaderWorkerSet
	LwsWorkerIndexLabelKey = LeaderWorkerSetPrefix + "worker-index"
)

// InstanceSet labels and annotations
const (
	InstanceSetPrefix = "instanceset.workloads.x-k8s.io/"

	// SetInstanceOwnerLabelKey identifies resources belonging to a specific InstanceSet.
	SetInstanceOwnerLabelKey = InstanceSetPrefix + "owner-uid"

	// SetInstanceIDLabelKey is a unique id for Instance and its Pods.
	SetInstanceIDLabelKey = InstanceSetPrefix + "instance-id"

	// SetInstanceNameLabelKey is the name of the Instance.
	SetInstanceNameLabelKey = InstanceSetPrefix + "instance-name"

	// SetInstanceComponentID is a unique id for Instance component and its Pods.
	SetInstanceComponentID = InstanceSetPrefix + "instance-component-id"

	// SetInstanceComponentName is the name of the Instance component.
	SetInstanceComponentName = InstanceSetPrefix + "instance-component-name"

	// SpecifiedDeleteKey is a label used to mark that the Instance should be deleted.
	SpecifiedDeleteKey = InstanceSetPrefix + "specified-delete"
)

// Instance labels and annotations
const (
	InstancePrefix = "instance.workloads.x-k8s.io/"

	// InstanceNameLabelKey is the name of the Instance
	InstanceNameLabelKey = InstancePrefix + "instance-name"

	// InstanceComponentNameKey is the name of the Component
	InstanceComponentNameKey = InstancePrefix + "component-name"

	// InstanceComponentIDKey is the id of the Component
	InstanceComponentIDKey = InstancePrefix + "component-id"
)

const (
	// InstancePodReadyConditionType corresponding condition status was set to "False" by multiple writers.
	InstancePodReadyConditionType v1.PodConditionType = "InstancePodReady"
)

type LwsComponentType string

const (
	LeaderLwsComponentType LwsComponentType = "Leader"
	WorkerLwsComponentType LwsComponentType = "Worker"
)

const (
	DeploymentWorkloadType      string = "apps/v1/Deployment"
	StatefulSetWorkloadType     string = "apps/v1/StatefulSet"
	InstanceSetWorkloadType     string = "workloads.x-k8s.io/v1alpha1/InstanceSet"
	LeaderWorkerSetWorkloadType string = "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet"
)

type AdapterPhase string

const (
	AdapterPhaseNone     AdapterPhase = ""
	AdapterPhaseNotBound AdapterPhase = "NotBound"
	AdapterPhaseBound    AdapterPhase = "Bound"
)

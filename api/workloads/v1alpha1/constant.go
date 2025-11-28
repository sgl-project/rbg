package v1alpha1

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
	// for 1:1 exclusive scheduling.  When present on RoleBasedGroup or Role template,
	// all affected Pods are required to land on the same topology domain and block
	// any other group from using that domain.
	ExclusiveKeyAnnotationKey = RBGPrefix + "exclusive-topology"

	// DisableExclusiveKeyAnnotationKey can be set to "true" on a Role template
	// to skip exclusive-topology affinity injection for that role.
	DisableExclusiveKeyAnnotationKey = RBGPrefix + "disable-exclusive-topology"

	// RevisionLabelKey is the labels key used to store the revision hash of the
	// RoleBasedGroup Roles's template. Place it in the rbg controllerrevision label.
	RevisionLabelKey = RBGPrefix + "controller-revision-hash"

	// RoleRevisionLabelKeyFmt is the labels key used to store the revision hash of
	// a specific Role template. Placed on the rbg controllerrevision and role workload labels
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
	// Under InstanceSet/StatefulSet pattern, RBGComponentIndex = InstanceComponentID
	// Under LeaderWorkerSet pattern:
	// - leader's RBGComponentIndex = "0"
	// - worker's RBGComponentIndex = InstanceComponentIDKey.value + 1
	RBGComponentIndexLabelKey = ComponentLabelPrefix + "index"

	// RBGComponentSizeLabelKey identifies the component size
	RBGComponentSizeLabelKey = ComponentLabelPrefix + "size"
)

const InstanceLabelPrefix = "instance." + RBGPrefix

// Generic instance label keys
const (
	// RBGInstancePatternLabelKey identifies the instance organization pattern
	RBGInstancePatternLabelKey = InstanceLabelPrefix + "pattern"
)

// InstancePatternType defines supported organization patterns
type InstancePatternType string

const (
	// DeploymentInstancePattern represents Deployment ordered topology pattern
	DeploymentInstancePattern InstancePatternType = "Deployment"

	// StatefulSetInstancePattern represents StatefulSet ordered topology pattern
	StatefulSetInstancePattern InstancePatternType = "StatefulSet"
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

	// LeaderWorkerSetTemplateType represents template is constructed from role.leaderWorkerSet field
	LeaderWorkerSetTemplateType RBGRoleTemplateType = "LeaderWorkerSet"

	// PodTemplateTemplateType represents template is constructed from role.template field
	PodTemplateTemplateType RBGRoleTemplateType = "PodTemplate"
)

// InstanceSet labels and annotations
const (
	InstanceSetPrefix = "instanceset.workloads.x-k8s.io/"

	// SetInstanceOwnerLabelKey identifies resources belonging to a specific InstanceSet.
	// This label is used for default selector for InstanceSet.
	SetInstanceOwnerLabelKey = InstanceSetPrefix + "owner-uid"

	// SetInstanceIDLabelKey is a unique id for Instance and its Pods.
	// Each Instance and the Pods it owns have the same instance-id.
	SetInstanceIDLabelKey = InstanceSetPrefix + "instance-id"

	// SetInstanceNameLabelKey is the name of the Instance.
	// Each Instance and the Pods it owns have the same instance-name.
	SetInstanceNameLabelKey = InstanceSetPrefix + "instance-name"

	// SetInstanceComponentID is a unique id for Instance component and its Pods.
	// Each Instance component and the Pods it owns have the same instance-component-id.
	SetInstanceComponentID = InstanceSetPrefix + "instance-component-id"

	// SetInstanceComponentName is the name of the Instance component.
	// Each Instance component and the Pods it owns have the same instance-component-name.
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
	// InstancePodReadyConditionType corresponding condition status was set to "False" by multiple writers,
	// the condition status will be considered as "True" only when all these writers set it to "True".
	InstancePodReadyConditionType v1.PodConditionType = "InstancePodReady"
)

type RolloutStrategyType string

const (
	// RollingUpdateStrategyType indicates that replicas will be updated one by one(defined
	// by RollingUpdateConfiguration), the latter one will not start the update until the
	// former role is ready.
	RollingUpdateStrategyType RolloutStrategyType = "RollingUpdate"
)

type RestartPolicyType string

const (
	// None will follow the same behavior as the StatefulSet/Deployment where only the failed pod
	// will be restarted on failure and other pods in the group will not be impacted.
	NoneRestartPolicy RestartPolicyType = "None"

	// RecreateRBGOnPodRestart will recreate all the pods in the rbg if
	// 1. Any individual pod in the rbg is recreated; 2. Any containers/init-containers
	// in a pod is restarted. This is to ensure all pods/containers in the group will be
	// started in the same time.
	RecreateRBGOnPodRestart RestartPolicyType = "RecreateRBGOnPodRestart"

	// RecreateRoleInstanceOnPodRestart will recreate an instance of role. If role's workload is lws, it means when a pod
	// failed, we will recreate only one lws instance, not all lws instances.
	// It equals to RecreateGroupOnPodRestart in lws.spec.LeaderWorkerTemplate.RestartPolicyType
	RecreateRoleInstanceOnPodRestart RestartPolicyType = "RecreateRoleInstanceOnPodRestart"
)

// UpdateStrategyType defines strategies for Instances in-place update.
type UpdateStrategyType string

const (
	// RecreateUpdateStrategyType indicates that we always delete Instances and create new Instances
	// during Instances update.
	RecreateUpdateStrategyType UpdateStrategyType = "Recreate"

	// InPlaceIfPossibleUpdateStrategyType indicates that we try to in-place update Instances instead of
	// recreating Instances when possible. Currently, all field but size update of Instances spec is allowed.
	// Size changes to the Instances spec will fall back to ReCreate UpdateStrategyType where Instances will be recreated.
	// Note that if InPlaceIfPossibleUpdateStrategyType was set, the Pods owned by the Instances will also be updated in-place when possible.
	// Due to the constraints of the Kubernetes APIServer on Pod update operations, a Pod can only be upgraded in-place when there are changes to its Metadata or Image.
	// Any other modifications will trigger a rebuild-based upgrade.
	// InPlaceIfPossibleUpdateStrategyType is the default behavior
	InPlaceIfPossibleUpdateStrategyType UpdateStrategyType = "InPlaceIfPossible"
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

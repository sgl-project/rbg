package workloads

// rbg-controller events
const (
	FailedGetRBG                      = "FailedGetRBG"
	InvalidRoleTemplates              = "InvalidRoleTemplates"
	InvalidTemplateRef                = "InvalidTemplateRef"
	InvalidRoleDependency             = "InvalidRoleDependency"
	FailedCheckRoleDependency         = "FailedCheckRoleDependency"
	DependencyNotMet                  = "DependencyNotMet"
	FailedReconcileWorkload           = "FailedReconcileWorkload"
	FailedCreateScalingAdapter        = "FailedCreateScalingAdapter"
	FailedCalculateScaling            = "FailedCalculateScaling"
	Succeed                           = "Succeed"
	FailedUpdateStatus                = "FailedUpdateStatus"
	FailedCreatePodGroup              = "FailedCreatePodGroup"
	FailedReconcilePodGroup           = "FailedReconcilePodGroup"
	FailedCreateRevision              = "FailedCreateRevision"
	FailedReconcileDiscoveryConfigMap = "FailedReconcileDiscoveryConfigMap"
	SucceedCreateRevision             = "SucceedCreateRevision"
	// InvalidGangSchedulingAnnotations is emitted when group-gang-scheduling and
	// role-instance-gang-scheduling annotations are set simultaneously on the same RBG.
	InvalidGangSchedulingAnnotations = "InvalidGangSchedulingAnnotations"
)

// rbg-scaling-adapter events
const (
	SuccessfulBound            = "SuccessfulBound"
	SuccessfulScale            = "SuccessfulScale"
	FailedScale                = "FailedScale"
	FailedGetRBGRole           = "FailedGetRBGRole"
	FailedGetRBGScalingAdapter = "FailedGetRBGScalingAdapter"
)

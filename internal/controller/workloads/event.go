package workloads

// rbg-controller events
const (
	FailedGetRBG               = "FailedGetRBG"
	InvalidRoleDependency      = "InvalidRoleDependency"
	FailedCheckRoleDependency  = "FailedCheckRoleDependency"
	FailedReconcileWorkload    = "FailedReconcileWorkload"
	FailedCreateScalingAdapter = "FailedCreateScalingAdapter"
	Succeed                    = "Succeed"
	FailedUpdateStatus         = "FailedUpdateStatus"
	FailedCreatePodGroup       = "FailedCreatePodGroup"
	FailedCreateRevision       = "FailedCreateRevision"
	SucceedCreateRevision      = "SucceedCreateRevision"
)

// rbg-scaling-adapter events
const (
	SuccessfulBound            = "SuccessfulBound"
	SuccessfulScale            = "SuccessfulScale"
	FailedScale                = "FailedScale"
	FailedGetRBGRole           = "FailedGetRBGRole"
	FailedGetRBGScalingAdapter = "FailedGetRBGScalingAdapter"
)

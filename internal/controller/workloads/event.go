/*
Copyright 2026 The RBG Authors.

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

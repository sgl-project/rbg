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

import v1 "k8s.io/api/core/v1"

// ========== External System Constants ==========

// LeaderWorkerSet labels and annotations
const (
	LeaderWorkerSetPrefix = "leaderworkerset.sigs.k8s.io/"

	// LwsWorkerIndexLabelKey identifies the worker index in LeaderWorkerSet
	LwsWorkerIndexLabelKey = LeaderWorkerSetPrefix + "worker-index"
)

const (
	// InstancePodReadyConditionType corresponding condition status was set to "False" by multiple writers.
	InstancePodReadyConditionType v1.PodConditionType = "InstancePodReady"

	// InPlaceUpdateReady must be added into template.spec.readinessGates when pod podUpdatePolicy
	// is InPlaceIfPossible or InPlaceOnly. The condition in podStatus will be updated to False before in-place
	// updating and updated to True after the update is finished. This ensures pod to remain at NotReady state while
	// in-place update is happening.
	InPlaceUpdateReady v1.PodConditionType = "InPlaceUpdateReady"
)

const (
	DeploymentWorkloadType  string = "apps/v1/Deployment"
	StatefulSetWorkloadType string = "apps/v1/StatefulSet"
	// InstanceSetWorkloadType is the v1alpha1-only workload superseded by
	// RoleInstanceSet. No reconciler exists for it since v0.7.0, so the v1alpha2
	// admission gate treats it as deprecated to keep new uses out.
	InstanceSetWorkloadType string = "workloads.x-k8s.io/v1alpha1/InstanceSet"
	// InstanceWorkloadType is the v1alpha1-only Instance superseded by RoleInstance.
	// It is never a role workload selector and is not matched by the admission
	// gate; it exists so the Helm RBAC generator can gate the dead grants for the
	// legacy resource behind the deprecated-workload-types toggle.
	InstanceWorkloadType        string = "workloads.x-k8s.io/v1alpha1/Instance"
	RoleInstanceSetWorkloadType string = "workloads.x-k8s.io/v1alpha2/RoleInstanceSet"
	LeaderWorkerSetWorkloadType string = "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet"
)

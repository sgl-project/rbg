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
)

const (
	DeploymentWorkloadType      string = "apps/v1/Deployment"
	StatefulSetWorkloadType     string = "apps/v1/StatefulSet"
	InstanceSetWorkloadType     string = "workloads.x-k8s.io/v1alpha1/InstanceSet"
	LeaderWorkerSetWorkloadType string = "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet"
)

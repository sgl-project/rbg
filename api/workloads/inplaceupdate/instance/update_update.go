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

package groupinplace

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// InPlaceUpdateReady must be added into template.spec.readinessGates when InstanceSet updateStrategy
	// is InPlaceIfPossible. The condition in Instance and its Pods will be updated to False before in-place
	// updating and updated to True after the update is finished. This ensures Instance and its Pods to remain
	// at NotReady state while in-place update is happening.
	InPlaceUpdateReady v1.PodConditionType = "InPlaceUpdateReady"

	// InPlaceUpdateStateKey records the state of inplace-update of the Instance.
	// The value of annotation is InPlaceUpdateState.
	InPlaceUpdateStateKey string = "workloads.x-k8s.io/inplace-update-state"

	// InPlaceUpdateGraceKey records the spec that the Instance should be updated when
	// grace period ends.
	InPlaceUpdateGraceKey string = "workloads.x-k8s.io/inplace-update-grace"
)

// InPlaceUpdateState records latest inplace-update state, including old statuses of containers.
type InPlaceUpdateState struct {
	// Revision is the updated revision hash.
	Revision string `json:"revision"`

	// UpdateTimestamp is the start time when the in-place update happens.
	UpdateTimestamp metav1.Time `json:"updateTimestamp"`
}

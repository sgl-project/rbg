/*
Copyright 2020 The Kruise Authors.

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

package inplace_update

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InPlaceUpdateState records latest inplace-update state, including old statuses of containers.
type InPlaceUpdateState struct {
	// Revision is the updated revision hash.
	Revision string `json:"revision"`

	// UpdateTimestamp is the time when the in-place update happens.
	UpdateTimestamp metav1.Time `json:"updateTimestamp"`

	// LastContainerStatuses records the before-in-place-update container statuses. It is a map from ContainerName
	// to InPlaceUpdateContainerStatus
	LastContainerStatuses map[string]InPlaceUpdateContainerStatus `json:"lastContainerStatuses"`
}

// InPlaceUpdateContainerStatus records the statuses of the container that are mainly used
// to determine whether the InPlaceUpdate is completed.
type InPlaceUpdateContainerStatus struct {
	ImageID string `json:"imageID,omitempty"`
}

// RuntimeContainerMetaSet contains all the containers' meta of the Pod.
type RuntimeContainerMetaSet struct {
	Containers []RuntimeContainerMeta `json:"containers"`
}

// RuntimeContainerMeta contains the meta data of a runtime container.
type RuntimeContainerMeta struct {
	Name         string                 `json:"name"`
	ContainerID  string                 `json:"containerID"`
	RestartCount int32                  `json:"restartCount"`
	Hashes       RuntimeContainerHashes `json:"hashes"`
}

// RuntimeContainerHashes contains the hashes of such container.
type RuntimeContainerHashes struct {
	// PlainHash is the hash that directly calculated from pod.spec.container[x].
	// Usually it is calculated by Kubelet and will be in annotation of each runtime container.
	PlainHash uint64 `json:"plainHash"`
	// TODO: add ConvertEnvHash here to support inplace update for env from annotation/label
}

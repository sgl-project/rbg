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

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const AnnotationCustomizedActionContainers = "workloads.x-k8s.io/customized-action-containers"

type customizedActionContainerMapping struct {
	PodContainerName string   `json:"podContainerName"`
	ContainerNames   []string `json:"containerNames"`
}

func minimumCustomizedActionTimeout(actions []workloadsv1alpha2.WarmupActions) *int64 {
	var minimum *int64
	for i := range actions {
		customized := actions[i].CustomizedAction
		if customized == nil || customized.TimeoutSeconds == nil {
			continue
		}
		if minimum == nil || *customized.TimeoutSeconds < *minimum {
			value := *customized.TimeoutSeconds
			minimum = &value
		}
	}
	return minimum
}

func customizedActionMappingsFromPod(pod *corev1.Pod) ([]customizedActionContainerMapping, error) {
	raw := pod.Annotations[AnnotationCustomizedActionContainers]
	if raw == "" {
		return nil, nil
	}
	var mappings []customizedActionContainerMapping
	if err := json.Unmarshal([]byte(raw), &mappings); err != nil {
		return nil, fmt.Errorf("decode customized action mappings: %w", err)
	}
	return mappings, nil
}

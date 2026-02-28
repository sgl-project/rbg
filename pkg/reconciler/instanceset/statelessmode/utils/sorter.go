/*
Copyright 2021 The Kruise Authors.

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

package utils

import (
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

type Ranker interface {
	GetRank(instance *appsv1alpha1.Instance) float64
}

const (
	// InstanceDeletionCost can be used to set to an int32 that represent the cost of deleting
	// a podGroup compared to other PodGroups belonging to the same GroupSet. PodGroups with lower
	// deletion cost are preferred to be deleted before PodGroups with higher deletion cost.
	InstanceDeletionCost = "controller.kubernetes.io/instance-deletion-cost"
)

// ActiveInstancesAvailableRank type allows custom sorting of podGroups so a controller can pick the best ones to delete.
type ActiveInstancesAvailableRank struct {
	Instances     []*appsv1alpha1.Instance
	AvailableFunc func(*appsv1alpha1.Instance) bool
}

func (s ActiveInstancesAvailableRank) Len() int { return len(s.Instances) }
func (s ActiveInstancesAvailableRank) Swap(i, j int) {
	s.Instances[i], s.Instances[j] = s.Instances[j], s.Instances[i]
}

func (s ActiveInstancesAvailableRank) Less(i, j int) bool {
	// 1. Unassigned < assigned
	if len(s.Instances[i].Status.ComponentStatuses) != len(s.Instances[j].Status.ComponentStatuses) {
		return len(s.Instances[i].Status.ComponentStatuses) < len(s.Instances[j].Status.ComponentStatuses)
	}

	// 2. Group ready
	if IsRunningAndReady(s.Instances[i]) != IsRunningAndReady(s.Instances[j]) {
		return !IsRunningAndReady(s.Instances[i])
	}

	// 3. Group available
	if s.AvailableFunc(s.Instances[i]) != s.AvailableFunc(s.Instances[j]) {
		return !s.AvailableFunc(s.Instances[i])
	}

	// 4. Deletion cost
	pi, _ := getDeletionCostFromAnnotations(s.Instances[i].Annotations)
	pj, _ := getDeletionCostFromAnnotations(s.Instances[j].Annotations)
	if pi != pj {
		return pi < pj
	}

	// 5. Newer podGroups
	if !s.Instances[i].CreationTimestamp.Equal(&s.Instances[j].CreationTimestamp) {
		return afterOrZero(&s.Instances[i].CreationTimestamp, &s.Instances[j].CreationTimestamp)
	}
	return false
}

// afterOrZero checks if time t1 is after time t2; if one of them
// is zero, the zero time is seen as after non-zero time.
func afterOrZero(t1, t2 *metav1.Time) bool {
	if t1.Time.IsZero() || t2.Time.IsZero() {
		return t1.Time.IsZero()
	}
	return t1.After(t2.Time)
}

// getDeletionCostFromAnnotations returns the integer value of podgroup-deletion-cost. Returns 0
// if not set or the value is invalid.
func getDeletionCostFromAnnotations(annotations map[string]string) (int32, error) {
	if value, exist := annotations[InstanceDeletionCost]; exist {
		// values that start with plus sign (e.g, "+10") or leading zeros (e.g., "008") are not valid.
		if !validFirstDigit(value) {
			return 0, fmt.Errorf("invalid value %q", value)
		}

		i, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			// make sure we default to 0 on error.
			return 0, err
		}
		return int32(i), nil
	}
	return 0, nil
}

func validFirstDigit(str string) bool {
	if len(str) == 0 {
		return false
	}
	return str[0] == '-' || (str[0] == '0' && str == "0") || (str[0] >= '1' && str[0] <= '9')
}

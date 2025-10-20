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

package pod

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	inplaceapi "sigs.k8s.io/rbgs/api/workloads/inplaceupdate/pod"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// InjectInPlaceReadinessGate injects InPlaceUpdateReady into pod.spec.readinessGates
func InjectInPlaceReadinessGate(pod *v1.Pod) {
	for _, r := range pod.Spec.ReadinessGates {
		if r.ConditionType == inplaceapi.InPlaceUpdateReady {
			return
		}
	}
	pod.Spec.ReadinessGates = append(pod.Spec.ReadinessGates, v1.PodReadinessGate{ConditionType: inplaceapi.InPlaceUpdateReady})
}

func InjectInstancePodReadinessGate(pod *v1.Pod) {
	for _, r := range pod.Spec.ReadinessGates {
		if r.ConditionType == v1alpha1.InstancePodReadyConditionType {
			return
		}
	}
	pod.Spec.ReadinessGates = append(pod.Spec.ReadinessGates, v1.PodReadinessGate{ConditionType: v1alpha1.InstancePodReadyConditionType})
}

// ContainsInPlaceReadinessGate checks whether pod.spec.readinessGates contains InPlaceUpdateReady
func ContainsInPlaceReadinessGate(pod *v1.Pod) bool {
	for _, r := range pod.Spec.ReadinessGates {
		if r.ConditionType == inplaceapi.InPlaceUpdateReady {
			return true
		}
	}
	return false
}

// GetInPlaceCondition returns the InPlaceUpdateReady condition in Pod.
func GetInPlaceCondition(pod *v1.Pod) *v1.PodCondition {
	return GetPodCondition(pod, inplaceapi.InPlaceUpdateReady)
}

func GetPodCondition(pod *v1.Pod, cType v1.PodConditionType) *v1.PodCondition {
	for _, c := range pod.Status.Conditions {
		if c.Type == cType {
			return &c
		}
	}
	return nil
}

func SetPodCondition(pod *v1.Pod, condition v1.PodCondition) {
	for i, c := range pod.Status.Conditions {
		if c.Type == condition.Type {
			if c.Status != condition.Status {
				pod.Status.Conditions[i] = condition
			}
			return
		}
	}
	pod.Status.Conditions = append(pod.Status.Conditions, condition)
}

func UpdatePodReadyCondition(pod *v1.Pod) {
	podReady := GetPodCondition(pod, v1.PodReady)
	if podReady == nil {
		return
	}

	containersReady := GetPodCondition(pod, v1.ContainersReady)
	if containersReady == nil || containersReady.Status != v1.ConditionTrue {
		return
	}

	var unreadyMessages []string
	for _, rg := range pod.Spec.ReadinessGates {
		c := GetPodCondition(pod, rg.ConditionType)
		if c == nil {
			unreadyMessages = append(unreadyMessages, fmt.Sprintf("corresponding condition of pod readiness gate %q does not exist.", string(rg.ConditionType)))
		} else if c.Status != v1.ConditionTrue {
			unreadyMessages = append(unreadyMessages, fmt.Sprintf("the status of pod readiness gate %q is not \"True\", but %v", string(rg.ConditionType), c.Status))
		}
	}

	newPodReady := v1.PodCondition{
		Type:               v1.PodReady,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
	}
	// Set "Ready" condition to "False" if any readiness gate is not ready.
	if len(unreadyMessages) != 0 {
		unreadyMessage := strings.Join(unreadyMessages, ", ")
		newPodReady = v1.PodCondition{
			Type:    v1.PodReady,
			Status:  v1.ConditionFalse,
			Reason:  "ReadinessGatesNotReady",
			Message: unreadyMessage,
		}
	}

	SetPodCondition(pod, newPodReady)
}

func RoundupSeconds(d time.Duration) time.Duration {
	if d%time.Second == 0 {
		return d
	}
	return (d/time.Second + 1) * time.Second
}

// DumpJSON returns the JSON encoding
func DumpJSON(o interface{}) string {
	j, _ := json.Marshal(o)
	return string(j)
}

// IsJSONObjectEqual checks if two objects are equal after encoding json
func IsJSONObjectEqual(o1, o2 interface{}) bool {
	if reflect.DeepEqual(o1, o2) {
		return true
	}

	oj1, _ := json.Marshal(o1)
	oj2, _ := json.Marshal(o2)
	os1 := string(oj1)
	os2 := string(oj2)
	if os1 == os2 {
		return true
	}

	om1 := make(map[string]interface{})
	om2 := make(map[string]interface{})
	_ = json.Unmarshal(oj1, &om1)
	_ = json.Unmarshal(oj2, &om2)

	return reflect.DeepEqual(om1, om2)
}

func GetShortHash(hash string) string {
	// This makes sure the real hash must be the last '-' substring of revision name
	// vendor/k8s.io/kubernetes/pkg/controller/history/controller_history.go#82
	list := strings.Split(hash, "-")
	return list[len(list)-1]
}

func ContainsInstanceReadinessGate(pod *v1.Pod) bool {
	for _, r := range pod.Spec.ReadinessGates {
		if string(r.ConditionType) == string(v1alpha1.InstanceInPlaceUpdateReady) {
			return true
		}
	}
	return false
}

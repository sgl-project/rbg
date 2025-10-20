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

package instance

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// GetInstanceName returns a set of instance names
func GetInstanceName(instances ...*appsv1alpha1.Instance) sets.Set[string] {
	names := sets.New[string]()
	for _, instance := range instances {
		names.Insert(instance.Name)

	}
	return names
}

// MatchComponent checks whether the pod belongs to the component
func MatchComponent(pod *corev1.Pod, componentName string) bool {
	if pod.Labels == nil {
		return false
	}
	return pod.Labels[appsv1alpha1.InstanceComponentNameKey] == componentName
}

// IsInstanceReady checks whether the instance is ready
func IsInstanceReady(instance *appsv1alpha1.Instance) bool {
	condition := GetInstanceCondition(instance, appsv1alpha1.InstanceReady)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// GetInstanceCondition returns the condition with the provided type.
func GetInstanceCondition(instance *appsv1alpha1.Instance, cType appsv1alpha1.InstanceConditionType) *appsv1alpha1.InstanceCondition {
	for i := range instance.Status.Conditions {
		if instance.Status.Conditions[i].Type == cType {
			return &instance.Status.Conditions[i]
		}
	}
	return nil
}

// InjectInstanceReadinessGate injects InPlaceUpdateReady into instance.spec.readinessGates
func InjectInstanceReadinessGate(instance *appsv1alpha1.Instance) {
	for _, r := range instance.Spec.ReadinessGates {
		if r.ConditionType == appsv1alpha1.InstanceInPlaceUpdateReady {
			return
		}
	}
	instance.Spec.ReadinessGates = append(instance.Spec.ReadinessGates, appsv1alpha1.InstanceReadinessGate{ConditionType: appsv1alpha1.InstanceInPlaceUpdateReady})
}

// SetInstanceCondition sets the condition in the instance status. If the condition that has the same type already exists, it will be updated.
func SetInstanceCondition(instance *appsv1alpha1.Instance, condition appsv1alpha1.InstanceCondition) {
	for i, c := range instance.Status.Conditions {
		if c.Type == condition.Type {
			if c.Status != condition.Status {
				instance.Status.Conditions[i] = condition
			}
			return
		}
	}
	instance.Status.Conditions = append(instance.Status.Conditions, condition)
}

// SetInstanceNotReadyCondition sets the "Ready" condition to "False" if any readiness gate is not ready.
func SetInstanceNotReadyCondition(instance *appsv1alpha1.Instance) {
	podReady := GetInstanceCondition(instance, appsv1alpha1.InstanceReady)
	if podReady == nil || podReady.Status == corev1.ConditionFalse {
		return
	}

	var unreadyMessages []string
	for _, rg := range instance.Spec.ReadinessGates {
		c := GetInstanceCondition(instance, rg.ConditionType)
		if c == nil {
			unreadyMessages = append(unreadyMessages, fmt.Sprintf("corresponding condition of instance readiness gate %q does not exist.", string(rg.ConditionType)))
		} else if c.Status != corev1.ConditionTrue {
			unreadyMessages = append(unreadyMessages, fmt.Sprintf("the status of instance readiness gate %q is not \"True\", but %v", string(rg.ConditionType), c.Status))
		}
	}

	// Set "Ready" condition to "False" if any readiness gate is not ready.
	if len(unreadyMessages) != 0 {
		unreadyMessage := strings.Join(unreadyMessages, ", ")
		newPodReady := appsv1alpha1.InstanceCondition{
			Type:    appsv1alpha1.InstanceReady,
			Status:  corev1.ConditionFalse,
			Reason:  "ReadinessGatesNotReady",
			Message: unreadyMessage,
		}
		SetInstanceCondition(instance, newPodReady)
	}
}

// GetComponentSize returns the size of the component. If the component is nil, it returns 0. If the size is nil, it returns 1.
func GetComponentSize(component *appsv1alpha1.InstanceComponent) int32 {
	if component == nil {
		return 0
	}
	if component.Size == nil {
		return 1
	}
	return *component.Size
}

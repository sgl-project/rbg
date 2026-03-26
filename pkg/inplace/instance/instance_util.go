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

package instance

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// GetRoleInstanceName returns a set of role instance names
func GetRoleInstanceName(instances ...*workloadsv1alpha2.RoleInstance) sets.Set[string] {
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
	return pod.Labels[constants.ComponentNameLabelKey] == componentName
}

// IsRoleInstanceReady checks whether the role instance is ready
func IsRoleInstanceReady(instance *workloadsv1alpha2.RoleInstance) bool {
	condition := GetRoleInstanceCondition(instance, workloadsv1alpha2.RoleInstanceReady)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// GetRoleInstanceCondition returns the condition with the provided type.
func GetRoleInstanceCondition(instance *workloadsv1alpha2.RoleInstance, cType workloadsv1alpha2.RoleInstanceConditionType) *workloadsv1alpha2.RoleInstanceCondition {
	for i := range instance.Status.Conditions {
		if instance.Status.Conditions[i].Type == cType {
			return &instance.Status.Conditions[i]
		}
	}
	return nil
}

// InjectRoleInstanceReadinessGate injects InPlaceUpdateReady into instance.spec.readinessGates
func InjectRoleInstanceReadinessGate(instance *workloadsv1alpha2.RoleInstance) {
	for _, r := range instance.Spec.ReadinessGates {
		if r.ConditionType == workloadsv1alpha2.RoleInstanceInPlaceUpdateReady {
			return
		}
	}
	instance.Spec.ReadinessGates = append(instance.Spec.ReadinessGates, workloadsv1alpha2.RoleInstanceReadinessGate{ConditionType: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady})
}

// SetRoleInstanceCondition sets the condition in the role instance status. If the condition that has the same type already exists, it will be updated.
func SetRoleInstanceCondition(instance *workloadsv1alpha2.RoleInstance, condition workloadsv1alpha2.RoleInstanceCondition) {
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

// SetRoleInstanceNotReadyCondition sets the "Ready" condition to "False" if any readiness gate is not ready.
func SetRoleInstanceNotReadyCondition(instance *workloadsv1alpha2.RoleInstance) {
	podReady := GetRoleInstanceCondition(instance, workloadsv1alpha2.RoleInstanceReady)
	if podReady == nil || podReady.Status == corev1.ConditionFalse {
		return
	}

	var unreadyMessages []string
	for _, rg := range instance.Spec.ReadinessGates {
		c := GetRoleInstanceCondition(instance, rg.ConditionType)
		if c == nil {
			unreadyMessages = append(unreadyMessages, fmt.Sprintf("corresponding condition of role instance readiness gate %q does not exist.", string(rg.ConditionType)))
		} else if c.Status != corev1.ConditionTrue {
			unreadyMessages = append(unreadyMessages, fmt.Sprintf("the status of role instance readiness gate %q is not \"True\", but %v", string(rg.ConditionType), c.Status))
		}
	}

	// Set "Ready" condition to "False" if any readiness gate is not ready.
	if len(unreadyMessages) != 0 {
		unreadyMessage := strings.Join(unreadyMessages, ", ")
		newPodReady := workloadsv1alpha2.RoleInstanceCondition{
			Type:    workloadsv1alpha2.RoleInstanceReady,
			Status:  corev1.ConditionFalse,
			Reason:  "ReadinessGatesNotReady",
			Message: unreadyMessage,
		}
		SetRoleInstanceCondition(instance, newPodReady)
	}
}

// GetComponentSize returns the size of the component. If the component is nil, it returns 0. If the size is nil, it returns 1.
func GetComponentSize(component *workloadsv1alpha2.RoleInstanceComponent) int32 {
	if component == nil {
		return 0
	}
	if component.Size == nil {
		return 1
	}
	return *component.Size
}

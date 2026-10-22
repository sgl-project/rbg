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

	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// identityLabelKeys are the labels that say which ordinal a RoleInstance, and every
// pod built from it, belongs to.
var identityLabelKeys = []string{
	apps.StatefulSetPodNameLabel,
	constants.RoleInstanceIndexLabelKey,
}

// InjectIdentityLabelsIntoComponents copies the instance's ordinal identity labels
// into every component's pod template, so pods built from that template carry the
// ordinal they belong to.
//
// The instance's own labels are the only source: they are written per instance, while
// the RoleInstanceSet template the components come from is shared by every ordinal and
// therefore cannot hold an identity. Taking the labels as an argument instead is how
// one ordinal's identity ends up on another's pods.
//
// A stateless instance has no ordinal and so no identity labels to copy; it is left
// alone.
func InjectIdentityLabelsIntoComponents(instance *workloadsv1alpha2.RoleInstance) {
	identity := make(map[string]string, len(identityLabelKeys))
	for _, key := range identityLabelKeys {
		if value, ok := instance.Labels[key]; ok {
			identity[key] = value
		}
	}
	if len(identity) == 0 {
		return
	}
	for i := range instance.Spec.Components {
		if instance.Spec.Components[i].Template.Labels == nil {
			instance.Spec.Components[i].Template.Labels = make(map[string]string, len(identity))
		}
		for key, value := range identity {
			instance.Spec.Components[i].Template.Labels[key] = value
		}
	}
}

// IsRoleInstanceReady checks whether the role instance is ready
func IsRoleInstanceReady(instance *workloadsv1alpha2.RoleInstance) bool {
	condition := GetRoleInstanceCondition(instance, workloadsv1alpha2.RoleInstanceReady)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

// GetRoleInstanceControlReadiness 保留重启和 Stateful 控制原有的健康判断。
// 自定义 gate 只影响聚合 Ready；操作判断仍使用 Pod 与原地更新条件。
// 不写入新状态；旧对象尚无基础条件时沿用其 Ready，未报告过 Ready 的对象不能放行。
func GetRoleInstanceControlReadiness(instance *workloadsv1alpha2.RoleInstance) (bool, metav1.Time) {
	condition := GetRoleInstanceCondition(instance, workloadsv1alpha2.RoleInstanceReady)
	if condition == nil {
		return false, metav1.Time{}
	}
	additionalGate := false
	for _, gate := range instance.Spec.ReadinessGates {
		if gate.ConditionType != workloadsv1alpha2.RoleInstanceInPlaceUpdateReady {
			additionalGate = true
			break
		}
	}
	if !additionalGate {
		return condition.Status == corev1.ConditionTrue, condition.LastTransitionTime
	}

	readySince := condition.LastTransitionTime
	if instance.Spec.ReadyPolicy == workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone {
		// 无 Pod 门槛时以创建时间为起点，再由原地更新条件推进；外部 gate 不参与计时。
		if !instance.CreationTimestamp.IsZero() {
			readySince = instance.CreationTimestamp
		}
	} else {
		pods := GetRoleInstanceCondition(instance, workloadsv1alpha2.RoleInstanceAllPodsReady)
		if pods == nil {
			if condition.Status != corev1.ConditionTrue {
				return false, readySince
			}
		} else {
			if pods.Status != corev1.ConditionTrue {
				return false, pods.LastTransitionTime
			}
			readySince = pods.LastTransitionTime
		}
	}
	for _, gate := range instance.Spec.ReadinessGates {
		if gate.ConditionType != workloadsv1alpha2.RoleInstanceInPlaceUpdateReady {
			continue
		}
		inplace := GetRoleInstanceCondition(instance, gate.ConditionType)
		if inplace == nil || inplace.Status != corev1.ConditionTrue {
			return false, readySince
		}
		// 可用等待只由基础条件推进，不被外部 gate 的变化重新计时。
		if inplace.LastTransitionTime.After(readySince.Time) {
			readySince = inplace.LastTransitionTime
		}
	}
	return true, readySince
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
	if old := GetRoleInstanceCondition(instance, condition.Type); old != nil && old.Status == condition.Status && !old.LastTransitionTime.IsZero() {
		condition.LastTransitionTime = old.LastTransitionTime
	}
	if condition.LastTransitionTime.IsZero() {
		condition.LastTransitionTime = metav1.Now()
	}
	conditions := make([]workloadsv1alpha2.RoleInstanceCondition, 0, len(instance.Status.Conditions)+1)
	found := false
	for _, c := range instance.Status.Conditions {
		if c.Type == condition.Type {
			if !found {
				conditions = append(conditions, condition)
				found = true
			}
			continue
		}
		conditions = append(conditions, c)
	}
	if !found {
		conditions = append(conditions, condition)
	}
	instance.Status.Conditions = conditions
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
			Reason:  workloadsv1alpha2.RoleInstanceReasonReadinessGatesNotReady,
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

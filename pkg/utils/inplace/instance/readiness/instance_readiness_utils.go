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

package readiness

import (
	"encoding/json"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	inplaceutil "sigs.k8s.io/rbgs/pkg/utils/inplace/instance"
	"sigs.k8s.io/rbgs/pkg/utils/inplace/instance/clientdapter"
)

func addNotReadyKey(adp clientdapter.Adapter, instance *appsv1alpha1.Instance, msg Message, condType appsv1alpha1.InstanceConditionType) error {
	if alreadyHasKey(instance, msg, condType) {
		return nil
	}

	if !containsReadinessGate(instance, condType) {
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		newInstance, err := adp.GetInstance(instance.Namespace, instance.Name)
		if err != nil {
			return err
		}

		condition := getReadinessCondition(newInstance, condType)
		if condition == nil {
			_, messages := addMessage("", msg)
			newInstance.Status.Conditions = append(newInstance.Status.Conditions, appsv1alpha1.InstanceCondition{
				Type:               appsv1alpha1.InstanceCustomReady,
				Message:            messages.dump(),
				LastTransitionTime: metav1.Now(),
			})
		} else {
			changed, messages := addMessage(condition.Message, msg)
			if !changed {
				return nil
			}
			condition.Status = v1.ConditionFalse
			condition.Message = messages.dump()
			condition.LastTransitionTime = metav1.Now()
		}

		// set instance ready condition to "False"
		inplaceutil.SetInstanceNotReadyCondition(newInstance)
		return adp.UpdateInstanceStatus(newInstance)
	})
	return err
}

func removeNotReadyKey(adp clientdapter.Adapter, instance *appsv1alpha1.Instance, msg Message, condType appsv1alpha1.InstanceConditionType) error {
	if !containsReadinessGate(instance, condType) {
		return nil
	}

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		newInstance, err := adp.GetInstance(instance.Namespace, instance.Name)
		if err != nil {
			return err
		}

		condition := getReadinessCondition(newInstance, condType)
		if condition == nil {
			return nil
		}
		changed, messages := removeMessage(condition.Message, msg)
		if !changed {
			return nil
		}
		if len(messages) == 0 {
			condition.Status = v1.ConditionTrue
		}
		condition.Message = messages.dump()
		condition.LastTransitionTime = metav1.Now()
		return adp.UpdateInstanceStatus(newInstance)
	})
}

func addMessage(base string, msg Message) (bool, messageList) {
	messages := messageList{}
	if base != "" {
		_ = json.Unmarshal([]byte(base), &messages)
	}
	for _, m := range messages {
		if m.UserAgent == msg.UserAgent && m.Key == msg.Key {
			return false, messages
		}
	}
	messages = append(messages, msg)
	return true, messages
}

func removeMessage(base string, msg Message) (bool, messageList) {
	messages := messageList{}
	if base != "" {
		_ = json.Unmarshal([]byte(base), &messages)
	}
	var removed bool
	newMessages := messageList{}
	for _, m := range messages {
		if m.UserAgent == msg.UserAgent && m.Key == msg.Key {
			removed = true
			continue
		}
		newMessages = append(newMessages, m)
	}
	return removed, newMessages
}

func GetReadinessCondition(instance *appsv1alpha1.Instance) *appsv1alpha1.InstanceCondition {
	return getReadinessCondition(instance, appsv1alpha1.InstanceCustomReady)
}

func ContainsReadinessGate(instance *appsv1alpha1.Instance) bool {
	return containsReadinessGate(instance, appsv1alpha1.InstanceCustomReady)
}

func getReadinessCondition(instance *appsv1alpha1.Instance, condType appsv1alpha1.InstanceConditionType) *appsv1alpha1.InstanceCondition {
	if instance == nil {
		return nil
	}
	for i := range instance.Status.Conditions {
		c := &instance.Status.Conditions[i]
		if c.Type == condType {
			return c
		}
	}
	return nil
}

func containsReadinessGate(instance *appsv1alpha1.Instance, condType appsv1alpha1.InstanceConditionType) bool {
	for _, g := range instance.Spec.ReadinessGates {
		if g.ConditionType == condType {
			return true
		}
	}
	return false
}

func alreadyHasKey(instance *appsv1alpha1.Instance, msg Message, condType appsv1alpha1.InstanceConditionType) bool {
	condition := getReadinessCondition(instance, condType)
	if condition == nil {
		return false
	}
	if condition.Status == v1.ConditionTrue || condition.Message == "" {
		return false
	}
	messages := messageList{}
	_ = json.Unmarshal([]byte(condition.Message), &messages)
	for _, m := range messages {
		if m.UserAgent == msg.UserAgent && m.Key == msg.Key {
			return true
		}
	}
	return false
}

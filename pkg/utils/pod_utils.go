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

package utils

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	kubecontroller "k8s.io/kubernetes/pkg/controller"
)

// PodRunningAndReady checks if the pod condition is running and marked as ready.
func PodRunningAndReady(pod corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodRunning && podReady(pod)
}

func podReady(pod corev1.Pod) bool {
	return podReadyConditionTrue(pod.Status)
}

func podReadyConditionTrue(status corev1.PodStatus) bool {
	condition := getPodReadyCondition(status)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

func getPodReadyCondition(status corev1.PodStatus) *corev1.PodCondition {
	_, condition := getPodCondition(&status, corev1.PodReady)
	return condition
}
func getPodCondition(status *corev1.PodStatus, conditionType corev1.PodConditionType) (int, *corev1.PodCondition) {
	if status == nil {
		return -1, nil
	}
	return getPodConditionFromList(status.Conditions, conditionType)
}

func getPodConditionFromList(conditions []corev1.PodCondition, conditionType corev1.PodConditionType) (
	int, *corev1.PodCondition,
) {
	if conditions == nil {
		return -1, nil
	}
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return i, &conditions[i]
		}
	}
	return -1, nil
}

// ContainerRestarted return true when there is any container in the pod that gets restarted
func ContainerRestarted(pod *corev1.Pod) bool {
	// if pod is nil, no containers restarted.
	if pod == nil {
		return false
	}
	if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
		for j := range pod.Status.InitContainerStatuses {
			stat := pod.Status.InitContainerStatuses[j]
			if stat.RestartCount > 0 {
				return true
			}
		}
		for j := range pod.Status.ContainerStatuses {
			// if engine runtime restart, do not need to recreate rbg.
			if pod.Status.ContainerStatuses[j].Name == "patio-runtime" {
				continue
			}
			stat := pod.Status.ContainerStatuses[j]
			if stat.RestartCount > 0 {
				return true
			}
		}
	}
	return false
}

// PodDeleted checks if the worker pod has been deleted
func PodDeleted(pod *corev1.Pod) bool {
	return pod == nil || pod.DeletionTimestamp != nil
}

// PodBecameInactive checks if a pod transitioned from active to inactive state.
// This is used in predicates to capture state transitions and avoid duplicate triggers.
// Uses the native Kubernetes IsPodActive function for consistency.
func PodBecameInactive(oldPod, newPod *corev1.Pod) bool {
	if oldPod == nil || newPod == nil {
		return false
	}
	wasActive := kubecontroller.IsPodActive(oldPod)
	nowInactive := !kubecontroller.IsPodActive(newPod)
	return wasActive && nowInactive
}

// IsPodEvicted checks if a pod was evicted due to resource pressure.
// Evicted pods have Phase=Failed and status.reason="Evicted".
// Also supports DisruptionTarget condition (Kubernetes 1.22+).
func IsPodEvicted(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	if !podutil.IsPodTerminal(pod) {
		return false
	}
	if pod.Status.Phase != corev1.PodFailed {
		return false
	}
	// Check DisruptionTarget condition (K8s 1.22+)
	// Native reasons: PreemptionByScheduler, TerminationByKubelet
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.DisruptionTarget {
			return true
		}
	}
	// Traditional detection: status.reason = "Evicted"
	return pod.Status.Reason == "Evicted"
}

// IsPodUnexpectedAdmissionError checks if a pod failed due to admission issues.
func IsPodUnexpectedAdmissionError(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	if pod.Status.Phase != corev1.PodFailed {
		return false
	}
	return pod.Status.Reason == "UnexpectedAdmissionError"
}

// IsPodFailedSchedule checks if pod scheduling failed (PodScheduled=False).
// Uses native constants: PodReasonUnschedulable, PodReasonSchedulerError.
func IsPodFailedSchedule(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
			return cond.Reason == corev1.PodReasonUnschedulable ||
				cond.Reason == corev1.PodReasonSchedulerError
		}
	}
	return false
}

// GetPodInactiveReason returns a human-readable reason for pod being inactive.
// Based on podutil.IsPodTerminal and native constants.
func GetPodInactiveReason(pod *corev1.Pod) string {
	if pod == nil {
		return "PodNotFound"
	}
	if pod.DeletionTimestamp != nil {
		if podutil.IsPodTerminal(pod) {
			return "PodTerminatingTerminal"
		}
		return "PodTerminating"
	}
	if pod.Status.Phase == corev1.PodSucceeded {
		return "PodSucceeded"
	}
	if pod.Status.Phase == corev1.PodFailed {
		if IsPodEvicted(pod) {
			return "PodEvicted"
		}
		if IsPodUnexpectedAdmissionError(pod) {
			return "UnexpectedAdmissionError"
		}
		// Check for eviction-like message patterns
		if strings.Contains(pod.Status.Message, "Evicted") ||
			strings.Contains(pod.Status.Message, "evicted") {
			return "PodEvicted"
		}
		return "PodFailed"
	}
	if IsPodFailedSchedule(pod) {
		return "PodUnschedulable"
	}
	return "Unknown"
}

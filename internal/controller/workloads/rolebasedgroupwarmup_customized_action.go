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
	"sort"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const (
	AnnotationCustomizedActionContainers = "workloads.x-k8s.io/customized-action-containers"

	CustomizedActionReasonCompleted            = "Completed"
	CustomizedActionReasonContainerExitCode    = "ContainerExitCode"
	CustomizedActionReasonContainerStartFailed = "ContainerStartFailed"
	CustomizedActionReasonImagePullFailed      = "ImagePullFailed"
	CustomizedActionReasonTimeout              = "Timeout"
	CustomizedActionReasonNodeNotSchedulable   = "NodeNotSchedulable"
	CustomizedActionReasonPodFailed            = "PodFailed"
	CustomizedActionReasonGlobalTimeout        = "GlobalTimeoutExceeded"

	ConditionCustomizedActionComplete = "CustomizedActionComplete"
	ConditionCustomizedActionFailed   = "CustomizedActionFailed"

	customizedActionTerminationMessageLimit = 1024
)

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

func evaluateCustomizedActionPod(pod *corev1.Pod) workloadsv1alpha2.CustomizedActionResult {
	result := workloadsv1alpha2.CustomizedActionResult{
		NodeName:       pod.Labels[LabelNodeName],
		PodName:        pod.Name,
		TimeoutSeconds: pod.Spec.ActiveDeadlineSeconds,
	}

	mappings, mappingErr := customizedActionMappingsFromPod(pod)
	if mappingErr != nil || len(mappings) == 0 {
		mappings = fallbackCustomizedActionMappings(pod)
	}

	statuses := make(map[string]corev1.ContainerStatus, len(pod.Status.ContainerStatuses))
	for i := range pod.Status.ContainerStatuses {
		status := pod.Status.ContainerStatuses[i]
		statuses[status.Name] = status
	}

	for _, mapping := range mappings {
		result.Containers = append(result.Containers, evaluateCustomizedActionContainer(mapping, statuses)...)
	}
	result.State, result.Reason, result.Message = aggregateCustomizedActionState(pod, mappings, result.Containers, statuses)
	if result.Message == "" && mappingErr != nil {
		result.Message = mappingErr.Error()
	}
	return result
}

func fallbackCustomizedActionMappings(pod *corev1.Pod) []customizedActionContainerMapping {
	mappings := make([]customizedActionContainerMapping, 0)
	for i := range pod.Spec.Containers {
		name := pod.Spec.Containers[i].Name
		if !strings.HasPrefix(name, "custom-") {
			continue
		}
		mappings = append(mappings, customizedActionContainerMapping{
			PodContainerName: name,
			ContainerNames:   []string{name},
		})
	}
	return mappings
}

func evaluateCustomizedActionContainer(
	mapping customizedActionContainerMapping,
	statuses map[string]corev1.ContainerStatus,
) []workloadsv1alpha2.CustomizedActionContainerResult {
	state := workloadsv1alpha2.CustomizedActionContainerStateWaiting
	var exitCode *int32
	var terminationReason, terminationMessage string
	if status, exists := statuses[mapping.PodContainerName]; exists {
		switch {
		case status.State.Running != nil:
			state = workloadsv1alpha2.CustomizedActionContainerStateRunning
		case status.State.Terminated != nil:
			terminated := status.State.Terminated
			exitCode = new(int32)
			*exitCode = terminated.ExitCode
			terminationReason = terminated.Reason
			terminationMessage = truncateCustomizedActionMessage(terminated.Message)
			if terminated.ExitCode == 0 {
				state = workloadsv1alpha2.CustomizedActionContainerStateSucceeded
			} else {
				state = workloadsv1alpha2.CustomizedActionContainerStateFailed
			}
		}
	}

	results := make([]workloadsv1alpha2.CustomizedActionContainerResult, 0, len(mapping.ContainerNames))
	for _, originalName := range mapping.ContainerNames {
		results = append(results, workloadsv1alpha2.CustomizedActionContainerResult{
			ContainerName:      originalName,
			PodContainerName:   mapping.PodContainerName,
			State:              state,
			ExitCode:           exitCode,
			TerminationReason:  terminationReason,
			TerminationMessage: terminationMessage,
		})
	}
	return results
}

func aggregateCustomizedActionState(
	pod *corev1.Pod,
	mappings []customizedActionContainerMapping,
	containers []workloadsv1alpha2.CustomizedActionContainerResult,
	statuses map[string]corev1.ContainerStatus,
) (workloadsv1alpha2.CustomizedActionState, string, string) {
	if pod.Status.Reason == "DeadlineExceeded" {
		return workloadsv1alpha2.CustomizedActionStateFailed, CustomizedActionReasonTimeout, pod.Status.Message
	}
	for i := range pod.Status.Conditions {
		condition := pod.Status.Conditions[i]
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == corev1.PodReasonUnschedulable {
			return workloadsv1alpha2.CustomizedActionStatePending, CustomizedActionReasonNodeNotSchedulable, condition.Message
		}
	}

	for _, mapping := range mappings {
		status, exists := statuses[mapping.PodContainerName]
		if !exists {
			continue
		}
		if waiting := status.State.Waiting; waiting != nil {
			switch waiting.Reason {
			case "ErrImagePull", "ImagePullBackOff":
				return workloadsv1alpha2.CustomizedActionStatePending, CustomizedActionReasonImagePullFailed, waiting.Message
			case "CreateContainerConfigError", "CreateContainerError", "RunContainerError":
				return workloadsv1alpha2.CustomizedActionStatePending, CustomizedActionReasonContainerStartFailed, waiting.Message
			}
		}
		if terminated := status.State.Terminated; terminated != nil {
			switch terminated.Reason {
			case "ContainerCannotRun", "StartError":
				return workloadsv1alpha2.CustomizedActionStateFailed, CustomizedActionReasonContainerStartFailed, terminated.Message
			}
		}
	}

	allSucceeded := len(containers) > 0
	anyRunning := false
	for i := range containers {
		switch containers[i].State {
		case workloadsv1alpha2.CustomizedActionContainerStateFailed:
			return workloadsv1alpha2.CustomizedActionStateFailed, CustomizedActionReasonContainerExitCode, containers[i].TerminationMessage
		case workloadsv1alpha2.CustomizedActionContainerStateSucceeded:
		case workloadsv1alpha2.CustomizedActionContainerStateRunning:
			allSucceeded = false
			anyRunning = true
		default:
			allSucceeded = false
		}
	}
	if allSucceeded {
		return workloadsv1alpha2.CustomizedActionStateSucceeded, CustomizedActionReasonCompleted, ""
	}
	if pod.Status.Phase == corev1.PodFailed {
		message := pod.Status.Message
		if message == "" {
			message = pod.Status.Reason
		}
		return workloadsv1alpha2.CustomizedActionStateFailed, CustomizedActionReasonPodFailed, message
	}
	if anyRunning {
		return workloadsv1alpha2.CustomizedActionStateRunning, "", ""
	}
	return workloadsv1alpha2.CustomizedActionStatePending, "", ""
}

func evaluateCustomizedActionResults(
	desiredNodes map[string][]workloadsv1alpha2.WarmupActions,
	pods []*corev1.Pod,
) []workloadsv1alpha2.CustomizedActionResult {
	customizedNodes := make(map[string]bool)
	for nodeName, actions := range desiredNodes {
		for i := range actions {
			if actions[i].CustomizedAction != nil {
				customizedNodes[nodeName] = true
				break
			}
		}
	}

	latestPods := make(map[string]*corev1.Pod)
	for _, pod := range pods {
		nodeName := pod.Labels[LabelNodeName]
		if !customizedNodes[nodeName] || !pod.DeletionTimestamp.IsZero() {
			continue
		}
		current := latestPods[nodeName]
		if current == nil || pod.CreationTimestamp.After(current.CreationTimestamp.Time) ||
			(pod.CreationTimestamp.Equal(&current.CreationTimestamp) && pod.Name > current.Name) {
			latestPods[nodeName] = pod
		}
	}

	results := make([]workloadsv1alpha2.CustomizedActionResult, 0, len(latestPods))
	for _, pod := range latestPods {
		results = append(results, evaluateCustomizedActionPod(pod))
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].NodeName < results[j].NodeName
	})
	return results
}

func truncateCustomizedActionMessage(message string) string {
	if len(message) <= customizedActionTerminationMessageLimit {
		return message
	}
	const suffix = "…"
	limit := customizedActionTerminationMessageLimit - len(suffix)
	var builder strings.Builder
	for _, r := range message {
		size := utf8.RuneLen(r)
		if builder.Len()+size > limit {
			break
		}
		builder.WriteRune(r)
	}
	builder.WriteString(suffix)
	return builder.String()
}

func updateCustomizedActionConditions(
	conditions *[]metav1.Condition,
	generation int64,
	results []workloadsv1alpha2.CustomizedActionResult,
	permanentlyFailedNodes map[string]bool,
	globallyTimedOut bool,
) {
	if len(results) == 0 {
		apimeta.RemoveStatusCondition(conditions, ConditionCustomizedActionComplete)
		apimeta.RemoveStatusCondition(conditions, ConditionCustomizedActionFailed)
		return
	}

	var terminalFailure *workloadsv1alpha2.CustomizedActionResult
	for i := range results {
		if permanentlyFailedNodes[results[i].NodeName] && results[i].State == workloadsv1alpha2.CustomizedActionStateFailed {
			terminalFailure = &results[i]
			break
		}
	}
	if globallyTimedOut || terminalFailure != nil {
		apimeta.RemoveStatusCondition(conditions, ConditionCustomizedActionComplete)
		reason := CustomizedActionReasonGlobalTimeout
		message := "Customized action execution was terminated by the global timeout"
		if terminalFailure != nil && !globallyTimedOut {
			reason = terminalFailure.Reason
			message = terminalFailure.Message
			if message == "" {
				message = fmt.Sprintf("Customized action permanently failed on node %s", terminalFailure.NodeName)
			}
		}
		apimeta.SetStatusCondition(conditions, metav1.Condition{
			Type:               ConditionCustomizedActionFailed,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: generation,
			Reason:             reason,
			Message:            message,
		})
		return
	}

	allSucceeded := true
	for i := range results {
		if results[i].State != workloadsv1alpha2.CustomizedActionStateSucceeded {
			allSucceeded = false
			break
		}
	}
	if allSucceeded {
		apimeta.RemoveStatusCondition(conditions, ConditionCustomizedActionFailed)
		apimeta.SetStatusCondition(conditions, metav1.Condition{
			Type:               ConditionCustomizedActionComplete,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: generation,
			Reason:             "AllActionsSucceeded",
			Message:            "All customized actions completed successfully",
		})
		return
	}

	apimeta.RemoveStatusCondition(conditions, ConditionCustomizedActionComplete)
	apimeta.RemoveStatusCondition(conditions, ConditionCustomizedActionFailed)
}

func recordCustomizedActionEvents(
	recorder record.EventRecorder,
	warmup *workloadsv1alpha2.RoleBasedGroupWarmup,
	oldResults, newResults []workloadsv1alpha2.CustomizedActionResult,
) {
	oldByNode := make(map[string]workloadsv1alpha2.CustomizedActionResult, len(oldResults))
	for i := range oldResults {
		oldByNode[oldResults[i].NodeName] = oldResults[i]
	}
	for i := range newResults {
		result := newResults[i]
		if old, exists := oldByNode[result.NodeName]; exists &&
			old.PodName == result.PodName && old.State == result.State && old.Reason == result.Reason {
			continue
		}

		message := result.Message
		if message == "" {
			message = fmt.Sprintf("Customized action on node %s is %s", result.NodeName, result.State)
		}
		switch {
		case result.State == workloadsv1alpha2.CustomizedActionStateSucceeded:
			recorder.Eventf(warmup, corev1.EventTypeNormal, "CustomizedActionCompleted",
				"node=%s, pod=%s: %s", result.NodeName, result.PodName, message)
		case result.State == workloadsv1alpha2.CustomizedActionStateFailed,
			result.Reason == CustomizedActionReasonImagePullFailed,
			result.Reason == CustomizedActionReasonContainerStartFailed,
			result.Reason == CustomizedActionReasonNodeNotSchedulable:
			reason := result.Reason
			if reason == "" {
				reason = CustomizedActionReasonPodFailed
			}
			recorder.Eventf(warmup, corev1.EventTypeWarning, reason,
				"node=%s, pod=%s: %s", result.NodeName, result.PodName, message)
		}
	}
}

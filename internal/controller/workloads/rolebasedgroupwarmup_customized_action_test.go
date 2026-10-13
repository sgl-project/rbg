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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func mappedWarmupPod(nodeName, podName string, phase corev1.PodPhase, statuses ...corev1.ContainerStatus) *corev1.Pod {
	mappings := make([]customizedActionContainerMapping, 0, len(statuses))
	containers := make([]corev1.Container, 0, len(statuses))
	for _, status := range statuses {
		mappings = append(mappings, customizedActionContainerMapping{
			PodContainerName: status.Name,
			ContainerNames:   []string{"node-check"},
		})
		containers = append(containers, corev1.Container{Name: status.Name, Image: "busybox"})
	}
	raw, err := json.Marshal(mappings)
	if err != nil {
		panic(err)
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				LabelNodeName: nodeName,
			},
			Annotations: map[string]string{AnnotationCustomizedActionContainers: string(raw)},
		},
		Spec: corev1.PodSpec{Containers: containers},
		Status: corev1.PodStatus{
			Phase:             phase,
			ContainerStatuses: statuses,
		},
	}
}

func terminatedStatus(name string, exitCode int32, reason, message string) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name: name,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: exitCode,
			Reason:   reason,
			Message:  message,
		}},
	}
}

func waitingStatus(name, reason, message string) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:  name,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}},
	}
}

func TestBuildWarmupPodCustomizedActionMetadata(t *testing.T) {
	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
	}
	actions := []workloadsv1alpha2.WarmupActions{
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			TimeoutSeconds: ptr.To(int64(120)),
			Containers: []corev1.Container{
				{Name: "prefill-check", Image: "busybox", Command: []string{"true"}},
			},
		}},
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			TimeoutSeconds: ptr.To(int64(30)),
			Containers: []corev1.Container{
				{Name: "decode-check", Image: "alpine", Command: []string{"true"}},
			},
		}},
	}

	pod, conflict := r.buildWarmupPod(warmup, "node-1", actions)
	if conflict {
		t.Fatal("unexpected volume conflict")
	}
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 30 {
		t.Fatalf("expected minimum deadline 30, got %v", pod.Spec.ActiveDeadlineSeconds)
	}

	mappings, err := customizedActionMappingsFromPod(pod)
	if err != nil {
		t.Fatalf("decode mappings: %v", err)
	}
	want := []customizedActionContainerMapping{
		{PodContainerName: "custom-0", ContainerNames: []string{"prefill-check"}},
		{PodContainerName: "custom-1", ContainerNames: []string{"decode-check"}},
	}
	if !apiequality.Semantic.DeepEqual(want, mappings) {
		t.Fatalf("expected mappings %#v, got %#v", want, mappings)
	}
}

func TestBuildWarmupPodCustomizedActionMetadataDeduplicatesIdentities(t *testing.T) {
	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
	}
	actions := []workloadsv1alpha2.WarmupActions{
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{Containers: []corev1.Container{
			{Name: "zeta-check", Image: "busybox", Command: []string{"true"}},
		}}},
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{Containers: []corev1.Container{
			{Name: "alpha-check", Image: "busybox", Command: []string{"true"}},
		}}},
	}

	pod, _ := r.buildWarmupPod(warmup, "node-1", actions)
	if pod.Spec.ActiveDeadlineSeconds != nil {
		t.Fatalf("expected no deadline, got %d", *pod.Spec.ActiveDeadlineSeconds)
	}
	mappings, err := customizedActionMappingsFromPod(pod)
	if err != nil {
		t.Fatalf("decode mappings: %v", err)
	}
	want := []customizedActionContainerMapping{{
		PodContainerName: "custom-0",
		ContainerNames:   []string{"alpha-check", "zeta-check"},
	}}
	if !apiequality.Semantic.DeepEqual(want, mappings) {
		t.Fatalf("expected mappings %#v, got %#v", want, mappings)
	}
}

func TestBuildWarmupPodCustomizedActionMetadataMarshalFailureFallsBack(t *testing.T) {
	originalMarshal := marshalCustomizedActionMappings
	marshalCustomizedActionMappings = func(any) ([]byte, error) {
		return nil, errors.New("marshal failed")
	}
	t.Cleanup(func() { marshalCustomizedActionMappings = originalMarshal })

	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
	}
	actions := []workloadsv1alpha2.WarmupActions{{
		CustomizedAction: &workloadsv1alpha2.CustomizedAction{Containers: []corev1.Container{{
			Name: "node-check", Image: "busybox",
		}}},
	}}

	pod, _ := r.buildWarmupPod(warmup, "node-1", actions)
	if pod.Annotations[AnnotationCustomizedActionContainers] != "" {
		t.Fatalf("expected mapping annotation to be omitted, got %#v", pod.Annotations)
	}
	recorder := r.Recorder.(*record.FakeRecorder)
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "CustomizedActionMappingError") {
			t.Fatalf("unexpected warning event: %q", event)
		}
	default:
		t.Fatal("expected mapping marshal failure event")
	}
}

func TestCustomizedActionMappingsFromPodRejectsMalformedJSON(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		AnnotationCustomizedActionContainers: "not-json",
	}}}
	if _, err := customizedActionMappingsFromPod(pod); err == nil {
		t.Fatal("expected malformed mapping annotation to fail")
	}
}

func TestEvaluateCustomizedActionPod(t *testing.T) {
	longMessage := strings.Repeat("界", customizedActionTerminationMessageLimit)
	tests := []struct {
		name       string
		pod        *corev1.Pod
		wantState  workloadsv1alpha2.CustomizedActionState
		wantReason string
		wantExit   *int32
		wantDetail string
	}{
		{
			name:       "succeeded",
			pod:        mappedWarmupPod("node-1", "success", corev1.PodSucceeded, terminatedStatus("custom-0", 0, "Completed", "")),
			wantState:  workloadsv1alpha2.CustomizedActionStateSucceeded,
			wantReason: CustomizedActionReasonCompleted,
			wantExit:   ptr.To(int32(0)),
		},
		{
			name:       "non-zero exit",
			pod:        mappedWarmupPod("node-1", "failed", corev1.PodFailed, terminatedStatus("custom-0", 17, "Error", "GPU check failed")),
			wantState:  workloadsv1alpha2.CustomizedActionStateFailed,
			wantReason: CustomizedActionReasonContainerExitCode,
			wantExit:   ptr.To(int32(17)),
			wantDetail: "GPU check failed",
		},
		{
			name: "deadline exceeded",
			pod: func() *corev1.Pod {
				pod := mappedWarmupPod("node-1", "timeout", corev1.PodFailed, terminatedStatus("custom-0", 137, "Error", "terminated"))
				pod.Status.Reason = "DeadlineExceeded"
				pod.Spec.ActiveDeadlineSeconds = ptr.To(int64(30))
				return pod
			}(),
			wantState:  workloadsv1alpha2.CustomizedActionStateFailed,
			wantReason: CustomizedActionReasonTimeout,
			wantExit:   ptr.To(int32(137)),
		},
		{
			name: "deadline takes priority over container start failure",
			pod: func() *corev1.Pod {
				pod := mappedWarmupPod("node-1", "start-timeout", corev1.PodFailed, terminatedStatus("custom-0", 127, "ContainerCannotRun", "executable not found"))
				pod.Status.Reason = "DeadlineExceeded"
				pod.Status.Message = "Pod was active longer than specified deadline"
				return pod
			}(),
			wantState:  workloadsv1alpha2.CustomizedActionStateFailed,
			wantReason: CustomizedActionReasonTimeout,
			wantExit:   ptr.To(int32(127)),
		},
		{
			name: "customized action succeeds before co-located preload deadline",
			pod: func() *corev1.Pod {
				pod := mappedWarmupPod("node-1", "preload-timeout", corev1.PodFailed, terminatedStatus("custom-0", 0, "Completed", ""))
				pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "image-preload-1", Image: "large-image"})
				pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, waitingStatus("image-preload-1", "ContainerCreating", "pulling image"))
				pod.Status.Reason = "DeadlineExceeded"
				pod.Status.Message = "Pod was active longer than specified deadline"
				return pod
			}(),
			wantState:  workloadsv1alpha2.CustomizedActionStateSucceeded,
			wantReason: CustomizedActionReasonCompleted,
			wantExit:   ptr.To(int32(0)),
		},
		{
			name:       "image pull backoff",
			pod:        mappedWarmupPod("node-1", "pull", corev1.PodPending, waitingStatus("custom-0", "ImagePullBackOff", "back-off pulling image")),
			wantState:  workloadsv1alpha2.CustomizedActionStatePending,
			wantReason: CustomizedActionReasonImagePullFailed,
			wantDetail: "back-off pulling image",
		},
		{
			name:       "container start waiting failure",
			pod:        mappedWarmupPod("node-1", "start", corev1.PodPending, waitingStatus("custom-0", "CreateContainerConfigError", "secret missing")),
			wantState:  workloadsv1alpha2.CustomizedActionStatePending,
			wantReason: CustomizedActionReasonContainerStartFailed,
			wantDetail: "secret missing",
		},
		{
			name:       "failed pod preserves image pull diagnostic as terminal",
			pod:        mappedWarmupPod("node-1", "pull-terminal", corev1.PodFailed, waitingStatus("custom-0", "ImagePullBackOff", "back-off pulling image")),
			wantState:  workloadsv1alpha2.CustomizedActionStateFailed,
			wantReason: CustomizedActionReasonImagePullFailed,
			wantDetail: "back-off pulling image",
		},
		{
			name:       "failed pod preserves container start diagnostic as terminal",
			pod:        mappedWarmupPod("node-1", "start-waiting-terminal", corev1.PodFailed, waitingStatus("custom-0", "CreateContainerConfigError", "secret missing")),
			wantState:  workloadsv1alpha2.CustomizedActionStateFailed,
			wantReason: CustomizedActionReasonContainerStartFailed,
			wantDetail: "secret missing",
		},
		{
			name:       "container cannot run",
			pod:        mappedWarmupPod("node-1", "start-terminal", corev1.PodFailed, terminatedStatus("custom-0", 127, "ContainerCannotRun", "executable file not found")),
			wantState:  workloadsv1alpha2.CustomizedActionStateFailed,
			wantReason: CustomizedActionReasonContainerStartFailed,
			wantExit:   ptr.To(int32(127)),
			wantDetail: "executable file not found",
		},
		{
			name: "unschedulable",
			pod: func() *corev1.Pod {
				pod := mappedWarmupPod("node-1", "pending", corev1.PodPending, waitingStatus("custom-0", "ContainerCreating", ""))
				pod.Status.Conditions = []corev1.PodCondition{{
					Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
					Reason: corev1.PodReasonUnschedulable, Message: "node did not match",
				}}
				return pod
			}(),
			wantState:  workloadsv1alpha2.CustomizedActionStatePending,
			wantReason: CustomizedActionReasonNodeNotSchedulable,
			wantDetail: "node did not match",
		},
		{
			name:       "running",
			pod:        mappedWarmupPod("node-1", "running", corev1.PodRunning, corev1.ContainerStatus{Name: "custom-0", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}),
			wantState:  workloadsv1alpha2.CustomizedActionStateRunning,
			wantReason: "",
		},
		{
			name: "missing status",
			pod: func() *corev1.Pod {
				pod := mappedWarmupPod("node-1", "waiting", corev1.PodPending, waitingStatus("custom-0", "ContainerCreating", ""))
				pod.Status.ContainerStatuses = nil
				return pod
			}(),
			wantState:  workloadsv1alpha2.CustomizedActionStatePending,
			wantReason: "",
		},
		{
			name:       "termination message is truncated",
			pod:        mappedWarmupPod("node-1", "long", corev1.PodFailed, terminatedStatus("custom-0", 1, "Error", longMessage)),
			wantState:  workloadsv1alpha2.CustomizedActionStateFailed,
			wantReason: CustomizedActionReasonContainerExitCode,
			wantExit:   ptr.To(int32(1)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateCustomizedActionPod(tt.pod)
			if got.NodeName != "node-1" || got.PodName != tt.pod.Name {
				t.Fatalf("unexpected identity: %#v", got)
			}
			if got.State != tt.wantState || got.Reason != tt.wantReason {
				t.Fatalf("expected state/reason %s/%s, got %s/%s", tt.wantState, tt.wantReason, got.State, got.Reason)
			}
			if len(got.Containers) != 1 {
				t.Fatalf("expected one logical container result, got %#v", got.Containers)
			}
			container := got.Containers[0]
			if container.ContainerName != "node-check" || container.PodContainerName != "custom-0" {
				t.Fatalf("unexpected container identity: %#v", container)
			}
			if !apiequality.Semantic.DeepEqual(container.ExitCode, tt.wantExit) {
				t.Fatalf("expected exit code %v, got %v", tt.wantExit, container.ExitCode)
			}
			if tt.wantDetail != "" && !strings.Contains(got.Message+container.TerminationMessage, tt.wantDetail) {
				t.Fatalf("expected detail %q in result %#v", tt.wantDetail, got)
			}
			if len(container.TerminationMessage) > customizedActionTerminationMessageLimit {
				t.Fatalf("termination message exceeded limit: %d", len(container.TerminationMessage))
			}
		})
	}
}

func TestEvaluateCustomizedActionPodBoundsAggregateMessages(t *testing.T) {
	longMessage := strings.Repeat("界", customizedActionTerminationMessageLimit)
	tests := []struct {
		name string
		pod  *corev1.Pod
	}{
		{
			name: "deadline",
			pod: func() *corev1.Pod {
				pod := mappedWarmupPod("node-1", "deadline", corev1.PodFailed, terminatedStatus("custom-0", 137, "Error", "terminated"))
				pod.Status.Reason = "DeadlineExceeded"
				pod.Status.Message = longMessage
				return pod
			}(),
		},
		{
			name: "container start",
			pod:  mappedWarmupPod("node-1", "start", corev1.PodFailed, terminatedStatus("custom-0", 127, "ContainerCannotRun", longMessage)),
		},
		{
			name: "waiting",
			pod:  mappedWarmupPod("node-1", "pull", corev1.PodPending, waitingStatus("custom-0", "ImagePullBackOff", longMessage)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateCustomizedActionPod(tt.pod)
			if len(got.Message) > customizedActionTerminationMessageLimit {
				t.Fatalf("aggregate message exceeded %d bytes: %d", customizedActionTerminationMessageLimit, len(got.Message))
			}
		})
	}
}

func TestEvaluateCustomizedActionPodUsesOnlyMappedContainers(t *testing.T) {
	pod := mappedWarmupPod("node-1", "mixed", corev1.PodFailed, terminatedStatus("custom-0", 0, "Completed", ""))
	pod.Spec.Containers = append([]corev1.Container{{Name: "image-preload-0", Image: "missing"}}, pod.Spec.Containers...)
	pod.Status.ContainerStatuses = append([]corev1.ContainerStatus{terminatedStatus("image-preload-0", 1, "Error", "pull failed")}, pod.Status.ContainerStatuses...)

	got := evaluateCustomizedActionPod(pod)
	if got.State != workloadsv1alpha2.CustomizedActionStateSucceeded || got.Reason != CustomizedActionReasonCompleted {
		t.Fatalf("preload failure must not contaminate customized action result: %#v", got)
	}
}

func TestEvaluateCustomizedActionPodTerminalFailureBeatsWaitingError(t *testing.T) {
	pod := mappedWarmupPod(
		"node-1",
		"mixed-failure",
		corev1.PodFailed,
		waitingStatus("custom-0", "ImagePullBackOff", "back-off pulling image"),
		terminatedStatus("custom-1", 17, "Error", "GPU check failed"),
	)

	got := evaluateCustomizedActionPod(pod)
	if got.State != workloadsv1alpha2.CustomizedActionStateFailed || got.Reason != CustomizedActionReasonContainerExitCode {
		t.Fatalf("terminal failure must beat waiting error, got %#v", got)
	}
}

func TestEvaluateCustomizedActionPodWaitingMessageIncludesContainer(t *testing.T) {
	pod := mappedWarmupPod(
		"node-1",
		"pull-failure",
		corev1.PodPending,
		waitingStatus("custom-0", "ImagePullBackOff", "back-off pulling image"),
	)

	got := evaluateCustomizedActionPod(pod)
	if !strings.Contains(got.Message, "container=node-check") {
		t.Fatalf("expected original container identity in aggregate message, got %q", got.Message)
	}
}

func TestEvaluateCustomizedActionPodPreservesWaitingFailureDetails(t *testing.T) {
	pod := mappedWarmupPod(
		"node-1",
		"pull-failure",
		corev1.PodPending,
		waitingStatus("custom-0", "ImagePullBackOff", "back-off pulling image"),
	)

	got := evaluateCustomizedActionPod(pod)
	if len(got.Containers) != 1 {
		t.Fatalf("expected one container result, got %#v", got.Containers)
	}
	container := got.Containers[0]
	if container.ContainerName != "node-check" || container.PodContainerName != "custom-0" {
		t.Fatalf("unexpected failing container identity: %#v", container)
	}
	if container.TerminationReason != "ImagePullBackOff" || container.TerminationMessage != "back-off pulling image" {
		t.Fatalf("expected waiting failure details, got %#v", container)
	}
}

func TestEvaluateCustomizedActionPodFallsBackToGeneratedNames(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "legacy", Labels: map[string]string{LabelNodeName: "node-1"},
			Annotations: map[string]string{AnnotationCustomizedActionContainers: "not-json"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "custom-0", Image: "busybox"}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodSucceeded,
			ContainerStatuses: []corev1.ContainerStatus{terminatedStatus("custom-0", 0, "Completed", "")},
		},
	}

	got := evaluateCustomizedActionPod(pod)
	if len(got.Containers) != 1 || got.Containers[0].ContainerName != "custom-0" || got.Containers[0].PodContainerName != "custom-0" {
		t.Fatalf("expected generated-name fallback, got %#v", got)
	}
}

func TestEvaluateCustomizedActionResultsSelectsLatestAttemptDeterministically(t *testing.T) {
	base := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	old := mappedWarmupPod("node-b", "attempt-old", corev1.PodFailed, terminatedStatus("custom-0", 1, "Error", "old"))
	old.CreationTimestamp = metav1.NewTime(base)
	latestByName := mappedWarmupPod("node-b", "attempt-z", corev1.PodSucceeded, terminatedStatus("custom-0", 0, "Completed", ""))
	latestByName.CreationTimestamp = metav1.NewTime(base.Add(time.Minute))
	tiedButEarlierName := mappedWarmupPod("node-b", "attempt-a", corev1.PodFailed, terminatedStatus("custom-0", 2, "Error", "tied"))
	tiedButEarlierName.CreationTimestamp = latestByName.CreationTimestamp
	nodeA := mappedWarmupPod("node-a", "attempt-a", corev1.PodSucceeded, terminatedStatus("custom-0", 0, "Completed", ""))
	nodeA.CreationTimestamp = metav1.NewTime(base)
	preloadOnly := mappedWarmupPod("node-c", "preload", corev1.PodSucceeded, terminatedStatus("custom-0", 0, "Completed", ""))

	desired := map[string][]workloadsv1alpha2.WarmupActions{
		"node-c": {{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"busybox"}}}},
		"node-b": {{CustomizedAction: &workloadsv1alpha2.CustomizedAction{Containers: []corev1.Container{{Name: "check", Image: "busybox"}}}}},
		"node-a": {{CustomizedAction: &workloadsv1alpha2.CustomizedAction{Containers: []corev1.Container{{Name: "check", Image: "busybox"}}}}},
	}
	got := evaluateCustomizedActionResults(desired, []*corev1.Pod{latestByName, nodeA, old, preloadOnly, tiedButEarlierName})
	if len(got) != 2 {
		t.Fatalf("expected two customized action results, got %#v", got)
	}
	if got[0].NodeName != "node-a" || got[1].NodeName != "node-b" || got[1].PodName != "attempt-z" {
		t.Fatalf("unexpected ordering/latest selection: %#v", got)
	}
}

func TestEvaluateCustomizedActionResultsPrefersMonotonicAttempt(t *testing.T) {
	created := metav1.NewTime(time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC))
	olderByAttempt := mappedWarmupPod("node-1", "attempt-z", corev1.PodFailed, terminatedStatus("custom-0", 1, "Error", "old"))
	olderByAttempt.CreationTimestamp = created
	olderByAttempt.Labels["workloads.x-k8s.io/warmup-attempt"] = "1"
	newerByAttempt := mappedWarmupPod("node-1", "attempt-a", corev1.PodSucceeded, terminatedStatus("custom-0", 0, "Completed", ""))
	newerByAttempt.CreationTimestamp = created
	newerByAttempt.Labels["workloads.x-k8s.io/warmup-attempt"] = "2"
	desired := map[string][]workloadsv1alpha2.WarmupActions{
		"node-1": {{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "check", Image: "busybox"}},
		}}},
	}

	got := evaluateCustomizedActionResults(desired, []*corev1.Pod{olderByAttempt, newerByAttempt})
	if len(got) != 1 || got[0].PodName != "attempt-a" || got[0].State != workloadsv1alpha2.CustomizedActionStateSucceeded {
		t.Fatalf("expected monotonic attempt 2 to win, got %#v", got)
	}
}

func TestEvaluateCustomizedActionResultsReturnsNilWithoutLatestPods(t *testing.T) {
	desired := map[string][]workloadsv1alpha2.WarmupActions{
		"node-1": {{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "check", Image: "busybox"}},
		}}},
	}

	if got := evaluateCustomizedActionResults(desired, nil); got != nil {
		t.Fatalf("expected nil results without pods, got %#v", got)
	}
}

func TestUpdateStatusIncludesCustomizedActionResults(t *testing.T) {
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1", Generation: 3},
	}
	pod := mappedWarmupPod("node-1", "attempt-1", corev1.PodSucceeded, terminatedStatus("custom-0", 0, "Completed", ""))
	desired := map[string][]workloadsv1alpha2.WarmupActions{
		"node-1": {{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "node-check", Image: "busybox"}},
		}}},
	}
	r := newWarmupReconciler(warmup)

	if err := r.updateStatus(context.Background(), warmup, nil, []*corev1.Pod{pod}, nil, desired, map[string]bool{}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get updated warmup: %v", err)
	}
	if len(updated.Status.CustomizedActionResults) != 1 || updated.Status.CustomizedActionResults[0].State != workloadsv1alpha2.CustomizedActionStateSucceeded {
		t.Fatalf("unexpected customized action results: %#v", updated.Status.CustomizedActionResults)
	}
	if len(updated.Status.CustomizedActionResults[0].Containers) != 1 {
		t.Fatalf("small successful result lost container detail: %#v", updated.Status.CustomizedActionResults[0])
	}
	if updated.Status.CustomizedActionResultsTruncated {
		t.Fatal("small successful result must not be marked truncated")
	}
	complete := apimeta.FindStatusCondition(updated.Status.Conditions, ConditionCustomizedActionComplete)
	if complete == nil || complete.Status != metav1.ConditionTrue {
		t.Fatalf("expected customized action complete condition, got %#v", updated.Status.Conditions)
	}
	if failed := apimeta.FindStatusCondition(updated.Status.Conditions, ConditionCustomizedActionFailed); failed != nil {
		t.Fatalf("did not expect customized action failed condition: %#v", failed)
	}
}

func TestUpdateStatusPreservesLastCustomizedActionResultBetweenAttempts(t *testing.T) {
	previous := workloadsv1alpha2.CustomizedActionResult{
		NodeName: "node-1", PodName: "attempt-1", State: workloadsv1alpha2.CustomizedActionStateFailed,
		Reason: CustomizedActionReasonContainerExitCode,
	}
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase:                   workloadsv1alpha2.WarmupJobPhaseRunning,
			CustomizedActionResults: []workloadsv1alpha2.CustomizedActionResult{previous},
		},
	}
	desired := map[string][]workloadsv1alpha2.WarmupActions{
		"node-1": {{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "node-check", Image: "busybox"}},
		}}},
	}
	r := newWarmupReconciler(warmup)

	if err := r.updateStatus(context.Background(), warmup, nil, nil, nil, desired, map[string]bool{}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get updated warmup: %v", err)
	}
	if !apiequality.Semantic.DeepEqual(updated.Status.CustomizedActionResults, []workloadsv1alpha2.CustomizedActionResult{previous}) {
		t.Fatalf("expected previous result to remain visible between attempts, got %#v", updated.Status.CustomizedActionResults)
	}
}

func TestUpdateStatusPreservesTruncationFlagWithCompactedResult(t *testing.T) {
	previous := workloadsv1alpha2.CustomizedActionResult{
		NodeName: "node-1", PodName: "attempt-1", State: workloadsv1alpha2.CustomizedActionStateFailed,
		Reason: CustomizedActionReasonContainerExitCode,
	}
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase:                            workloadsv1alpha2.WarmupJobPhaseRunning,
			CustomizedActionResults:          []workloadsv1alpha2.CustomizedActionResult{previous},
			CustomizedActionResultsTruncated: true,
		},
	}
	desired := map[string][]workloadsv1alpha2.WarmupActions{
		"node-1": {{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "node-check", Image: "busybox"}},
		}}},
	}
	r := newWarmupReconciler(warmup)

	if err := r.updateStatus(context.Background(), warmup, nil, nil, nil, desired, map[string]bool{}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get updated warmup: %v", err)
	}
	if !updated.Status.CustomizedActionResultsTruncated {
		t.Fatal("expected preserved compacted result to retain truncation flag")
	}
}

func TestUpdateStatusDoesNotRegressToOlderAttempt(t *testing.T) {
	previous := workloadsv1alpha2.CustomizedActionResult{
		NodeName: "node-1", PodName: "attempt-2", State: workloadsv1alpha2.CustomizedActionStateRunning,
	}
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase:                            workloadsv1alpha2.WarmupJobPhaseRunning,
			CustomizedActionResults:          []workloadsv1alpha2.CustomizedActionResult{previous},
			CustomizedActionResultsTruncated: true,
		},
	}
	older := mappedWarmupPod("node-1", "attempt-1", corev1.PodFailed, terminatedStatus("custom-0", 1, "Error", "old failure"))
	older.Labels[LabelWarmupAttempt] = "1"
	desired := map[string][]workloadsv1alpha2.WarmupActions{
		"node-1": {{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "node-check", Image: "busybox"}},
		}}},
	}
	r := newWarmupReconciler(warmup)

	if err := r.updateStatus(context.Background(), warmup, nil, nil, []*corev1.Pod{older}, desired, map[string]bool{}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get updated warmup: %v", err)
	}
	if len(updated.Status.CustomizedActionResults) != 1 || updated.Status.CustomizedActionResults[0].PodName != previous.PodName {
		t.Fatalf("expected latest preserved attempt, got %#v", updated.Status.CustomizedActionResults)
	}
	if !updated.Status.CustomizedActionResultsTruncated {
		t.Fatal("expected preserved latest attempt to retain truncation provenance")
	}
}

func TestUpdateStatusAcceptsNewerFailedAttempt(t *testing.T) {
	previous := workloadsv1alpha2.CustomizedActionResult{
		NodeName: "node-1", PodName: "attempt-1", State: workloadsv1alpha2.CustomizedActionStateRunning,
	}
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase: workloadsv1alpha2.WarmupJobPhaseRunning, CustomizedActionResults: []workloadsv1alpha2.CustomizedActionResult{previous},
		},
	}
	oldPod := mappedWarmupPod("node-1", "attempt-1", corev1.PodFailed, terminatedStatus("custom-0", 1, "Error", "old failure"))
	oldPod.Labels[LabelWarmupAttempt] = "1"
	newPod := mappedWarmupPod("node-1", "attempt-2", corev1.PodFailed, terminatedStatus("custom-0", 2, "Error", "new failure"))
	newPod.Labels[LabelWarmupAttempt] = "2"
	desired := map[string][]workloadsv1alpha2.WarmupActions{
		"node-1": {{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "node-check", Image: "busybox"}},
		}}},
	}
	r := newWarmupReconciler(warmup)

	if err := r.updateStatus(context.Background(), warmup, nil, nil, []*corev1.Pod{oldPod, newPod}, desired, map[string]bool{}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get updated warmup: %v", err)
	}
	if len(updated.Status.CustomizedActionResults) != 1 || updated.Status.CustomizedActionResults[0].PodName != newPod.Name {
		t.Fatalf("expected newer failed attempt, got %#v", updated.Status.CustomizedActionResults)
	}
}

func TestUpdateStatusPreservesMissingLatestTerminalAttempt(t *testing.T) {
	previous := workloadsv1alpha2.CustomizedActionResult{
		NodeName: "node-1", PodName: "attempt-2", State: workloadsv1alpha2.CustomizedActionStateFailed,
		Reason: CustomizedActionReasonContainerExitCode, Message: "latest failure",
	}
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase: workloadsv1alpha2.WarmupJobPhaseRunning, CustomizedActionResults: []workloadsv1alpha2.CustomizedActionResult{previous},
		},
	}
	older := mappedWarmupPod("node-1", "attempt-1", corev1.PodFailed, terminatedStatus("custom-0", 1, "Error", "old failure"))
	older.Labels[LabelWarmupAttempt] = "1"
	desired := map[string][]workloadsv1alpha2.WarmupActions{
		"node-1": {{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "node-check", Image: "busybox"}},
		}}},
	}
	r := newWarmupReconciler(warmup)

	if err := r.updateStatus(context.Background(), warmup, nil, nil, []*corev1.Pod{older}, desired, map[string]bool{}); err != nil {
		t.Fatalf("update status: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get updated warmup: %v", err)
	}
	if len(updated.Status.CustomizedActionResults) != 1 || updated.Status.CustomizedActionResults[0].PodName != previous.PodName {
		t.Fatalf("expected missing latest terminal attempt to be preserved, got %#v", updated.Status.CustomizedActionResults)
	}
}

func TestUpdateStatusBoundsCustomizedActionResultsAtScale(t *testing.T) {
	const nodeCount = 1000
	longMessage := strings.Repeat("x", customizedActionTerminationMessageLimit)
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "scale", Namespace: "default", UID: "uid-scale"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			Policies: &workloadsv1alpha2.WarmupPolicies{MaxFailedNodes: ptr.To(int32(0))},
		},
	}
	desired := make(map[string][]workloadsv1alpha2.WarmupActions, nodeCount)
	succeededPods := make([]*corev1.Pod, 0, nodeCount-1)
	failedPods := make([]*corev1.Pod, 0, nodeCount/2)
	permanentlyFailed := make(map[string]bool, nodeCount/2)
	for i := 0; i < nodeCount; i++ {
		nodeName := fmt.Sprintf("node-%04d", i)
		desired[nodeName] = []workloadsv1alpha2.WarmupActions{{
			CustomizedAction: &workloadsv1alpha2.CustomizedAction{
				Containers: []corev1.Container{{Name: "check", Image: "busybox"}},
			},
		}}
		statuses := []corev1.ContainerStatus{
			terminatedStatus("custom-0", 0, "Completed", longMessage),
			terminatedStatus("custom-1", 0, "Completed", longMessage),
			terminatedStatus("custom-2", 0, "Completed", longMessage),
		}
		phase := corev1.PodSucceeded
		if i >= nodeCount/2 {
			phase = corev1.PodFailed
			statuses[0] = terminatedStatus("custom-0", 1, "Error", longMessage)
			permanentlyFailed[nodeName] = true
		}
		pod := mappedWarmupPod(nodeName, "attempt-"+nodeName, phase, statuses...)
		if phase == corev1.PodFailed {
			failedPods = append(failedPods, pod)
		} else {
			succeededPods = append(succeededPods, pod)
		}
	}
	r := newWarmupReconciler(warmup)
	r.Recorder = record.NewFakeRecorder(nodeCount + 10)

	if err := r.updateStatus(context.Background(), warmup, nil, succeededPods, failedPods, desired, permanentlyFailed); err != nil {
		t.Fatalf("update status: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get updated warmup: %v", err)
	}
	serialized, err := json.Marshal(updated.Status.CustomizedActionResults)
	if err != nil {
		t.Fatalf("marshal results: %v", err)
	}
	if len(serialized) > customizedActionResultsStatusBudget {
		t.Fatalf("customized action results exceed status budget: got %d bytes, want <= %d", len(serialized), customizedActionResultsStatusBudget)
	}
	t.Logf("bounded %d node results to %d bytes", nodeCount, len(serialized))
	if len(updated.Status.CustomizedActionResults) != nodeCount {
		t.Fatalf("expected all node summaries to fit, got %d", len(updated.Status.CustomizedActionResults))
	}
	if !updated.Status.CustomizedActionResultsTruncated {
		t.Fatal("expected status to report compacted result details")
	}
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseFailed {
		t.Fatalf("expected full results to drive the terminal phase, got %q", updated.Status.Phase)
	}
	if condition := apimeta.FindStatusCondition(updated.Status.Conditions, ConditionCustomizedActionFailed); condition == nil {
		t.Fatalf("expected full results to drive the failure condition, got %#v", updated.Status.Conditions)
	}
	foundFailure := false
	foundDetailedFailure := false
	for i := range updated.Status.CustomizedActionResults {
		result := updated.Status.CustomizedActionResults[i]
		if result.NodeName == "node-0999" {
			foundFailure = true
		}
		if result.State == workloadsv1alpha2.CustomizedActionStateFailed && len(result.Containers) != 0 {
			foundDetailedFailure = true
		}
		if result.State == workloadsv1alpha2.CustomizedActionStateSucceeded && len(result.Containers) != 0 {
			t.Fatalf("successful result retained container details: %#v", result)
		}
	}
	if !foundFailure {
		t.Fatal("expected the failed node to be retained ahead of successful results")
	}
	if !foundDetailedFailure {
		t.Fatal("expected remaining budget to retain details for at least one failed node")
	}
}

func TestLimitCustomizedActionResultsPrioritizesFailuresAtMaxItems(t *testing.T) {
	results := make([]workloadsv1alpha2.CustomizedActionResult, 0, customizedActionResultsMaxItems+1)
	for i := 0; i < customizedActionResultsMaxItems; i++ {
		results = append(results, workloadsv1alpha2.CustomizedActionResult{
			NodeName: fmt.Sprintf("success-%04d", i), State: workloadsv1alpha2.CustomizedActionStateSucceeded,
			Containers: []workloadsv1alpha2.CustomizedActionContainerResult{{ContainerName: "check"}},
		})
	}
	results = append(results, workloadsv1alpha2.CustomizedActionResult{
		NodeName: "zz-failed", State: workloadsv1alpha2.CustomizedActionStateFailed,
		Reason: CustomizedActionReasonContainerExitCode,
	})

	limited, truncated := limitCustomizedActionResults(results)
	if !truncated {
		t.Fatal("expected max-items truncation")
	}
	if len(limited) != customizedActionResultsMaxItems {
		t.Fatalf("expected %d results, got %d", customizedActionResultsMaxItems, len(limited))
	}
	foundFailure := false
	for i := range limited {
		if limited[i].NodeName == "zz-failed" {
			foundFailure = true
		}
	}
	if !foundFailure {
		t.Fatal("failure was displaced by successful results")
	}
}

func TestUpdateCustomizedActionConditions(t *testing.T) {
	succeeded := workloadsv1alpha2.CustomizedActionResult{
		NodeName: "node-1", PodName: "success", State: workloadsv1alpha2.CustomizedActionStateSucceeded,
		Reason: CustomizedActionReasonCompleted,
	}
	failed := workloadsv1alpha2.CustomizedActionResult{
		NodeName: "node-1", PodName: "failed", State: workloadsv1alpha2.CustomizedActionStateFailed,
		Reason: CustomizedActionReasonContainerExitCode,
	}

	t.Run("retryable failure has no terminal condition", func(t *testing.T) {
		var conditions []metav1.Condition
		updateCustomizedActionConditions(&conditions, 1, []workloadsv1alpha2.CustomizedActionResult{failed}, map[string]bool{}, false, 1)
		if len(conditions) != 0 {
			t.Fatalf("expected no terminal conditions, got %#v", conditions)
		}
	})

	t.Run("permanent failure sets only failed", func(t *testing.T) {
		conditions := []metav1.Condition{{Type: ConditionCustomizedActionComplete, Status: metav1.ConditionTrue}}
		updateCustomizedActionConditions(&conditions, 2, []workloadsv1alpha2.CustomizedActionResult{failed}, map[string]bool{"node-1": true}, false, 1)
		if apimeta.FindStatusCondition(conditions, ConditionCustomizedActionComplete) != nil {
			t.Fatalf("complete condition must be removed: %#v", conditions)
		}
		condition := apimeta.FindStatusCondition(conditions, ConditionCustomizedActionFailed)
		if condition == nil || condition.Reason != CustomizedActionReasonContainerExitCode {
			t.Fatalf("expected failed condition, got %#v", conditions)
		}
	})

	t.Run("success replaces failed", func(t *testing.T) {
		conditions := []metav1.Condition{{Type: ConditionCustomizedActionFailed, Status: metav1.ConditionTrue}}
		updateCustomizedActionConditions(&conditions, 3, []workloadsv1alpha2.CustomizedActionResult{succeeded}, map[string]bool{}, false, 1)
		if apimeta.FindStatusCondition(conditions, ConditionCustomizedActionFailed) != nil {
			t.Fatalf("failed condition must be removed: %#v", conditions)
		}
		condition := apimeta.FindStatusCondition(conditions, ConditionCustomizedActionComplete)
		if condition == nil || condition.Reason != "AllActionsSucceeded" {
			t.Fatalf("expected complete condition, got %#v", conditions)
		}
	})

	t.Run("preload failure does not turn successful custom action into failure", func(t *testing.T) {
		var conditions []metav1.Condition
		updateCustomizedActionConditions(&conditions, 4, []workloadsv1alpha2.CustomizedActionResult{succeeded}, map[string]bool{"node-1": true}, false, 1)
		if apimeta.FindStatusCondition(conditions, ConditionCustomizedActionFailed) != nil {
			t.Fatalf("unexpected failed condition: %#v", conditions)
		}
	})

	t.Run("partial success does not complete while another node has no result", func(t *testing.T) {
		var conditions []metav1.Condition
		updateCustomizedActionConditions(&conditions, 5, []workloadsv1alpha2.CustomizedActionResult{succeeded}, map[string]bool{}, false, 2)
		if apimeta.FindStatusCondition(conditions, ConditionCustomizedActionComplete) != nil {
			t.Fatalf("partial results must not complete all actions: %#v", conditions)
		}
	})

	t.Run("global timeout sets failure before any result exists", func(t *testing.T) {
		var conditions []metav1.Condition
		updateCustomizedActionConditions(&conditions, 6, nil, map[string]bool{}, true, 1)
		condition := apimeta.FindStatusCondition(conditions, ConditionCustomizedActionFailed)
		if condition == nil || condition.Reason != CustomizedActionReasonGlobalTimeout {
			t.Fatalf("expected global-timeout failure condition, got %#v", conditions)
		}
	})

	t.Run("global timeout preserves completed customized actions", func(t *testing.T) {
		conditions := []metav1.Condition{{Type: ConditionCustomizedActionFailed, Status: metav1.ConditionTrue}}
		updateCustomizedActionConditions(&conditions, 7, []workloadsv1alpha2.CustomizedActionResult{succeeded}, map[string]bool{}, true, 1)
		if failedCondition := apimeta.FindStatusCondition(conditions, ConditionCustomizedActionFailed); failedCondition != nil {
			t.Fatalf("completed customized action must not be marked failed: %#v", conditions)
		}
		complete := apimeta.FindStatusCondition(conditions, ConditionCustomizedActionComplete)
		if complete == nil || complete.Status != metav1.ConditionTrue {
			t.Fatalf("expected completed customized action condition, got %#v", conditions)
		}
	})
}

func TestFailWarmupJobPreservesCustomizedActionDiagnosticsWithoutDesiredNodes(t *testing.T) {
	result := workloadsv1alpha2.CustomizedActionResult{
		NodeName: "node-1",
		PodName:  "attempt-1",
		State:    workloadsv1alpha2.CustomizedActionStateSucceeded,
		Reason:   CustomizedActionReasonCompleted,
	}
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1", Generation: 2},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			CustomizedActionResults: []workloadsv1alpha2.CustomizedActionResult{result},
			Conditions: []metav1.Condition{{
				Type:               ConditionCustomizedActionComplete,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: 1,
				Reason:             "AllActionsSucceeded",
				Message:            "All customized actions completed successfully",
			}},
		},
	}
	r := newWarmupReconciler(warmup)

	if err := r.failWarmupJob(
		context.Background(), warmup, nil, nil, nil, nil, "InvalidWarmupSpec", "invalid spec",
	); err != nil {
		t.Fatalf("fail warmup job: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get updated warmup: %v", err)
	}
	if !apiequality.Semantic.DeepEqual(updated.Status.CustomizedActionResults, []workloadsv1alpha2.CustomizedActionResult{result}) {
		t.Fatalf("expected customized action results to be preserved, got %#v", updated.Status.CustomizedActionResults)
	}
	if condition := apimeta.FindStatusCondition(updated.Status.Conditions, ConditionCustomizedActionComplete); condition == nil {
		t.Fatalf("expected customized action completion condition to be preserved, got %#v", updated.Status.Conditions)
	}
}

func TestFailWarmupJobFinalizesNonTerminalCustomizedActionResults(t *testing.T) {
	tests := []struct {
		name        string
		result      workloadsv1alpha2.CustomizedActionResult
		jobReason   string
		jobMessage  string
		wantReason  string
		wantMessage string
	}{
		{
			name: "preserves specific diagnostic",
			result: workloadsv1alpha2.CustomizedActionResult{
				NodeName: "node-1", PodName: "attempt-1", State: workloadsv1alpha2.CustomizedActionStatePending,
				Reason: CustomizedActionReasonImagePullFailed, Message: "container=node-check: back-off pulling image",
			},
			jobReason:   CustomizedActionReasonGlobalTimeout,
			jobMessage:  "warmup timed out",
			wantReason:  CustomizedActionReasonImagePullFailed,
			wantMessage: "container=node-check: back-off pulling image",
		},
		{
			name: "fills missing diagnostic from job failure",
			result: workloadsv1alpha2.CustomizedActionResult{
				NodeName: "node-1", PodName: "attempt-1", State: workloadsv1alpha2.CustomizedActionStateRunning,
			},
			jobReason:   "InvalidWarmupSpec",
			jobMessage:  "invalid spec",
			wantReason:  "InvalidWarmupSpec",
			wantMessage: "invalid spec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
				Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
					Phase:                   workloadsv1alpha2.WarmupJobPhaseRunning,
					CustomizedActionResults: []workloadsv1alpha2.CustomizedActionResult{tt.result},
				},
			}
			r := newWarmupReconciler(warmup)

			if err := r.failWarmupJob(context.Background(), warmup, nil, nil, nil, nil, tt.jobReason, tt.jobMessage); err != nil {
				t.Fatalf("fail warmup job: %v", err)
			}
			updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
				t.Fatalf("get updated warmup: %v", err)
			}
			if len(updated.Status.CustomizedActionResults) != 1 {
				t.Fatalf("expected one result, got %#v", updated.Status.CustomizedActionResults)
			}
			got := updated.Status.CustomizedActionResults[0]
			if got.State != workloadsv1alpha2.CustomizedActionStateFailed || got.Reason != tt.wantReason || got.Message != tt.wantMessage {
				t.Fatalf("unexpected finalized result: %#v", got)
			}
		})
	}
}

func TestFailWarmupJobEmitsEventForFinalizedCustomizedAction(t *testing.T) {
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase: workloadsv1alpha2.WarmupJobPhaseRunning,
			CustomizedActionResults: []workloadsv1alpha2.CustomizedActionResult{{
				NodeName: "node-1", PodName: "attempt-1", State: workloadsv1alpha2.CustomizedActionStateRunning,
			}},
		},
	}
	r := newWarmupReconciler(warmup)
	recorder := r.Recorder.(*record.FakeRecorder)

	if err := r.failWarmupJob(
		context.Background(), warmup, nil, nil, nil, nil, "InvalidTarget", "target disappeared",
	); err != nil {
		t.Fatalf("fail warmup job: %v", err)
	}

	foundCustomizedActionEvent := false
	for len(recorder.Events) > 0 {
		if event := <-recorder.Events; strings.Contains(event, "node=node-1") && strings.Contains(event, "InvalidTarget") {
			foundCustomizedActionEvent = true
		}
	}
	if !foundCustomizedActionEvent {
		t.Fatal("expected finalized customized action to emit a per-node warning event")
	}
}

func TestFailWarmupJobBoundsFallbackDiagnostics(t *testing.T) {
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase: workloadsv1alpha2.WarmupJobPhaseRunning,
			CustomizedActionResults: []workloadsv1alpha2.CustomizedActionResult{{
				NodeName: "node-1", PodName: "attempt-1", State: workloadsv1alpha2.CustomizedActionStateRunning,
			}},
		},
	}
	r := newWarmupReconciler(warmup)

	if err := r.failWarmupJob(
		context.Background(), warmup, nil, nil, nil, nil,
		strings.Repeat("r", customizedActionTerminationReasonLimit+1),
		strings.Repeat("界", customizedActionTerminationMessageLimit),
	); err != nil {
		t.Fatalf("fail warmup job: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get updated warmup: %v", err)
	}
	got := updated.Status.CustomizedActionResults[0]
	if len(got.Reason) > customizedActionTerminationReasonLimit || len(got.Message) > customizedActionTerminationMessageLimit {
		t.Fatalf("fallback diagnostics exceed API bounds: reason=%d message=%d", len(got.Reason), len(got.Message))
	}
}

func TestReconcileDoesNotBlockRetriesWhenStatusIsAtMaxItems(t *testing.T) {
	const resultCount = customizedActionResultsMaxItems + 1
	desired := make([]string, 0, resultCount)
	previous := make([]workloadsv1alpha2.CustomizedActionResult, 0, customizedActionResultsMaxItems)
	for i := 0; i < resultCount; i++ {
		nodeName := fmt.Sprintf("node-%04d", i)
		desired = append(desired, nodeName)
		if i > 0 {
			previous = append(previous, workloadsv1alpha2.CustomizedActionResult{
				NodeName: nodeName, PodName: "old-" + nodeName, State: workloadsv1alpha2.CustomizedActionStateFailed,
				Reason: CustomizedActionReasonContainerExitCode,
			})
		}
	}
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			Policies: &workloadsv1alpha2.WarmupPolicies{
				BackoffLimitPerNode: ptr.To(int32(2)), Parallelism: ptr.To(int32(1)),
			},
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames: desired,
				WarmupActions: workloadsv1alpha2.WarmupActions{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
					Containers: []corev1.Container{{Name: "node-check", Image: "busybox"}},
				}},
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase: workloadsv1alpha2.WarmupJobPhaseRunning, CustomizedActionResults: previous,
		},
	}
	failed := mappedWarmupPod("node-0000", "attempt-1", corev1.PodFailed, terminatedStatus("custom-0", 17, "Error", "failed"))
	failed.Namespace = warmup.Namespace
	failed.Labels[LabelWarmupName] = warmup.Name
	failed.Labels[LabelWarmupUID] = string(warmup.UID)
	failed.Labels[LabelWarmupAttempt] = "1"
	r := newWarmupReconciler(warmup, failed)
	r.Recorder = record.NewFakeRecorder(resultCount + 10)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	pods := &corev1.PodList{}
	if err := r.List(context.Background(), pods, client.InNamespace(warmup.Namespace)); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	for i := range pods.Items {
		if pods.Items[i].Status.Phase != corev1.PodFailed {
			return
		}
	}
	t.Fatal("expected retry creation to proceed even when result status is at maxItems")
}

func TestFailWarmupJobPreservesDiagnosticsFromDeletingPod(t *testing.T) {
	previous := workloadsv1alpha2.CustomizedActionResult{
		NodeName: "node-1", PodName: "attempt-1", State: workloadsv1alpha2.CustomizedActionStatePending,
		Reason: CustomizedActionReasonImagePullFailed, Message: "container=node-check: back-off pulling image",
		Containers: []workloadsv1alpha2.CustomizedActionContainerResult{{
			ContainerName: "node-check", PodContainerName: "custom-0",
			State:             workloadsv1alpha2.CustomizedActionContainerStateWaiting,
			TerminationReason: "ImagePullBackOff",
		}},
	}
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase: workloadsv1alpha2.WarmupJobPhaseRunning, CustomizedActionResults: []workloadsv1alpha2.CustomizedActionResult{previous},
		},
	}
	pod := mappedWarmupPod("node-1", "attempt-1", corev1.PodRunning, waitingStatus("custom-0", "ImagePullBackOff", "back-off pulling image"))
	now := metav1.Now()
	pod.DeletionTimestamp = &now
	desired := map[string][]workloadsv1alpha2.WarmupActions{
		"node-1": {{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "node-check", Image: "busybox"}},
		}}},
	}
	r := newWarmupReconciler(warmup)

	if err := r.failWarmupJob(
		context.Background(), warmup, []*corev1.Pod{pod}, nil, nil, desired,
		CustomizedActionReasonGlobalTimeout, "warmup timed out",
	); err != nil {
		t.Fatalf("fail warmup job: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get updated warmup: %v", err)
	}
	if len(updated.Status.CustomizedActionResults) != 1 {
		t.Fatalf("expected one preserved result, got %#v", updated.Status.CustomizedActionResults)
	}
	got := updated.Status.CustomizedActionResults[0]
	if got.State != workloadsv1alpha2.CustomizedActionStateFailed || got.Reason != previous.Reason || got.Message != previous.Message || len(got.Containers) != 1 {
		t.Fatalf("expected preserved diagnostics to be finalized, got %#v", got)
	}
}

func TestRecordCustomizedActionEventsOnlyForTransitions(t *testing.T) {
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"}}
	recorder := record.NewFakeRecorder(10)
	result := workloadsv1alpha2.CustomizedActionResult{
		NodeName: "node-1", PodName: "attempt-1", State: workloadsv1alpha2.CustomizedActionStateFailed,
		Reason: CustomizedActionReasonContainerExitCode, Message: "exit code 1",
	}

	recordCustomizedActionEvents(recorder, warmup, nil, []workloadsv1alpha2.CustomizedActionResult{result})
	recordCustomizedActionEvents(recorder, warmup, []workloadsv1alpha2.CustomizedActionResult{result}, []workloadsv1alpha2.CustomizedActionResult{result})
	first := <-recorder.Events
	if !strings.Contains(first, CustomizedActionReasonContainerExitCode) {
		t.Fatalf("unexpected first event: %q", first)
	}
	select {
	case event := <-recorder.Events:
		t.Fatalf("unchanged result emitted duplicate event: %q", event)
	default:
	}

	retry := result
	retry.PodName = "attempt-2"
	recordCustomizedActionEvents(recorder, warmup, []workloadsv1alpha2.CustomizedActionResult{result}, []workloadsv1alpha2.CustomizedActionResult{retry})
	select {
	case <-recorder.Events:
	default:
		t.Fatal("new retry attempt must emit a new event")
	}
}

func TestRecordCustomizedActionEventsBoundsEventMessage(t *testing.T) {
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"}}
	recorder := record.NewFakeRecorder(1)
	result := workloadsv1alpha2.CustomizedActionResult{
		NodeName: "node-1", PodName: "attempt-1", State: workloadsv1alpha2.CustomizedActionStateFailed,
		Reason: CustomizedActionReasonContainerExitCode, Message: strings.Repeat("界", customizedActionTerminationMessageLimit),
	}

	recordCustomizedActionEvents(recorder, warmup, nil, []workloadsv1alpha2.CustomizedActionResult{result})
	event := <-recorder.Events
	parts := strings.SplitN(event, " ", 3)
	if len(parts) != 3 {
		t.Fatalf("unexpected event format: %q", event)
	}
	if len(parts[2]) > 1024 {
		t.Fatalf("event message exceeded 1024 bytes: %d", len(parts[2]))
	}
}

func TestReconcileReportsFailedAttemptBeforeCreatingRetry(t *testing.T) {
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			Policies: &workloadsv1alpha2.WarmupPolicies{BackoffLimitPerNode: ptr.To(int32(2))},
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames: []string{"node-1"},
				WarmupActions: workloadsv1alpha2.WarmupActions{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
					Containers: []corev1.Container{{Name: "node-check", Image: "busybox"}},
				}},
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase: workloadsv1alpha2.WarmupJobPhaseRunning,
			CustomizedActionResults: []workloadsv1alpha2.CustomizedActionResult{{
				NodeName: "node-1", PodName: "attempt-1", State: workloadsv1alpha2.CustomizedActionStateRunning,
			}},
		},
	}
	failed := mappedWarmupPod("node-1", "attempt-1", corev1.PodFailed, terminatedStatus("custom-0", 17, "Error", "GPU check failed"))
	failed.Namespace = warmup.Namespace
	failed.Labels[LabelWarmupName] = warmup.Name
	failed.Labels[LabelWarmupUID] = string(warmup.UID)
	failed.Labels[LabelWarmupAttempt] = "1"
	r := newWarmupReconciler(warmup, failed)
	recorder := r.Recorder.(*record.FakeRecorder)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	foundFailureEvent := false
	for len(recorder.Events) > 0 {
		if event := <-recorder.Events; strings.Contains(event, CustomizedActionReasonContainerExitCode) && strings.Contains(event, "attempt-1") {
			foundFailureEvent = true
		}
	}
	if !foundFailureEvent {
		t.Fatal("expected failed attempt to emit a warning event before retry replaces it")
	}
}

func TestReconcileReportsDisplacedFailureAfterPartialRetryCreation(t *testing.T) {
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			Policies: &workloadsv1alpha2.WarmupPolicies{BackoffLimitPerNode: ptr.To(int32(2))},
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames: []string{"node-1", "node-2"},
				WarmupActions: workloadsv1alpha2.WarmupActions{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
					Containers: []corev1.Container{{Name: "node-check", Image: "busybox"}},
				}},
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase: workloadsv1alpha2.WarmupJobPhaseRunning,
			CustomizedActionResults: []workloadsv1alpha2.CustomizedActionResult{
				{NodeName: "node-1", PodName: "attempt-1-node-1", State: workloadsv1alpha2.CustomizedActionStateRunning},
				{NodeName: "node-2", PodName: "attempt-1-node-2", State: workloadsv1alpha2.CustomizedActionStateRunning},
			},
		},
	}
	failedPods := make([]runtime.Object, 0, 2)
	for _, nodeName := range []string{"node-1", "node-2"} {
		pod := mappedWarmupPod(nodeName, "attempt-1-"+nodeName, corev1.PodFailed, terminatedStatus("custom-0", 17, "Error", "failed"))
		pod.Namespace = warmup.Namespace
		pod.Labels[LabelWarmupName] = warmup.Name
		pod.Labels[LabelWarmupUID] = string(warmup.UID)
		pod.Labels[LabelWarmupAttempt] = "1"
		failedPods = append(failedPods, pod)
	}
	scheme := newWarmupTestScheme()
	objects := append([]runtime.Object{warmup}, failedPods...)
	createCount := 0
	createErr := errors.New("injected second retry create failure")
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objects...).
		WithStatusSubresource(&workloadsv1alpha2.RoleBasedGroupWarmup{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*corev1.Pod); ok {
					createCount++
					if createCount == 2 {
						return createErr
					}
				}
				return c.Create(ctx, obj, opts...)
			},
		}).Build()
	recorder := record.NewFakeRecorder(20)
	r := &RoleBasedGroupWarmupReconciler{Client: fakeClient, Scheme: scheme, Recorder: recorder}

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)})
	if !errors.Is(err, createErr) {
		t.Fatalf("expected retry creation error, got %v", err)
	}
	foundDisplacedFailure := false
	for len(recorder.Events) > 0 {
		if event := <-recorder.Events; strings.Contains(event, CustomizedActionReasonContainerExitCode) && strings.Contains(event, "node=node-1") {
			foundDisplacedFailure = true
		}
	}
	if !foundDisplacedFailure {
		t.Fatal("expected successfully displaced failure to emit an event before returning create error")
	}
}

func TestReconcileGlobalTimeoutPreservesCustomizedActionDiagnostics(t *testing.T) {
	startTime := metav1.NewTime(time.Now().Add(-2 * time.Minute))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1", Generation: 2},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			Policies: &workloadsv1alpha2.WarmupPolicies{GlobalTimeoutSeconds: ptr.To(int64(1))},
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames: []string{"node-1"},
				WarmupActions: workloadsv1alpha2.WarmupActions{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
					Containers: []corev1.Container{{Name: "node-check", Image: "busybox"}},
				}},
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase:     workloadsv1alpha2.WarmupJobPhaseRunning,
			StartTime: &startTime,
		},
	}
	pod := mappedWarmupPod("node-1", "attempt-1", corev1.PodRunning, corev1.ContainerStatus{
		Name: "custom-0", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	})
	pod.Namespace = warmup.Namespace
	pod.Labels[LabelWarmupName] = warmup.Name
	pod.Labels[LabelWarmupUID] = string(warmup.UID)
	r := newWarmupReconciler(warmup, pod)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get updated warmup: %v", err)
	}
	if len(updated.Status.CustomizedActionResults) != 1 {
		t.Fatalf("expected one preserved result, got %#v", updated.Status.CustomizedActionResults)
	}
	result := updated.Status.CustomizedActionResults[0]
	if result.State != workloadsv1alpha2.CustomizedActionStateFailed || result.Reason != CustomizedActionReasonGlobalTimeout {
		t.Fatalf("expected global-timeout result, got %#v", result)
	}
	condition := apimeta.FindStatusCondition(updated.Status.Conditions, ConditionCustomizedActionFailed)
	if condition == nil || condition.Reason != CustomizedActionReasonGlobalTimeout {
		t.Fatalf("expected global-timeout customized action condition, got %#v", updated.Status.Conditions)
	}
}

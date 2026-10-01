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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

func TestEvaluateCustomizedActionPodUsesOnlyMappedContainers(t *testing.T) {
	pod := mappedWarmupPod("node-1", "mixed", corev1.PodFailed, terminatedStatus("custom-0", 0, "Completed", ""))
	pod.Spec.Containers = append([]corev1.Container{{Name: "image-preload-0", Image: "missing"}}, pod.Spec.Containers...)
	pod.Status.ContainerStatuses = append([]corev1.ContainerStatus{terminatedStatus("image-preload-0", 1, "Error", "pull failed")}, pod.Status.ContainerStatuses...)

	got := evaluateCustomizedActionPod(pod)
	if got.State != workloadsv1alpha2.CustomizedActionStateSucceeded || got.Reason != CustomizedActionReasonCompleted {
		t.Fatalf("preload failure must not contaminate customized action result: %#v", got)
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
	complete := apimeta.FindStatusCondition(updated.Status.Conditions, ConditionCustomizedActionComplete)
	if complete == nil || complete.Status != metav1.ConditionTrue {
		t.Fatalf("expected customized action complete condition, got %#v", updated.Status.Conditions)
	}
	if failed := apimeta.FindStatusCondition(updated.Status.Conditions, ConditionCustomizedActionFailed); failed != nil {
		t.Fatalf("did not expect customized action failed condition: %#v", failed)
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
		updateCustomizedActionConditions(&conditions, 1, []workloadsv1alpha2.CustomizedActionResult{failed}, map[string]bool{}, false)
		if len(conditions) != 0 {
			t.Fatalf("expected no terminal conditions, got %#v", conditions)
		}
	})

	t.Run("permanent failure sets only failed", func(t *testing.T) {
		conditions := []metav1.Condition{{Type: ConditionCustomizedActionComplete, Status: metav1.ConditionTrue}}
		updateCustomizedActionConditions(&conditions, 2, []workloadsv1alpha2.CustomizedActionResult{failed}, map[string]bool{"node-1": true}, false)
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
		updateCustomizedActionConditions(&conditions, 3, []workloadsv1alpha2.CustomizedActionResult{succeeded}, map[string]bool{}, false)
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
		updateCustomizedActionConditions(&conditions, 4, []workloadsv1alpha2.CustomizedActionResult{succeeded}, map[string]bool{"node-1": true}, false)
		if apimeta.FindStatusCondition(conditions, ConditionCustomizedActionFailed) != nil {
			t.Fatalf("unexpected failed condition: %#v", conditions)
		}
	})
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

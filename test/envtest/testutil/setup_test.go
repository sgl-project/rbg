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

package testutil

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetPodRunningAndReadyPreservesControllerConditions(t *testing.T) {
	controllerOwned := corev1.PodCondition{
		Type:               "test.rbg.io/controller-owned",
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute)),
	}
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase:      corev1.PodPending,
			Conditions: []corev1.PodCondition{controllerOwned},
		},
	}

	SetPodRunningAndReady(pod)

	if pod.Status.Phase != corev1.PodRunning {
		t.Errorf("phase = %q, want Running", pod.Status.Phase)
	}
	// The controller-owned condition must survive with its transition time.
	var gate corev1.PodCondition
	for _, cond := range pod.Status.Conditions {
		if cond.Type == controllerOwned.Type {
			gate = cond
		}
	}
	if gate.Type == "" {
		t.Fatalf("controller-owned condition %q was dropped", controllerOwned.Type)
	}
	if gate.Status != controllerOwned.Status || !gate.LastTransitionTime.Equal(&controllerOwned.LastTransitionTime) {
		t.Errorf("controller-owned condition changed: %+v, want %+v", gate, controllerOwned)
	}
	for _, want := range []corev1.PodConditionType{corev1.PodReady, corev1.ContainersReady} {
		found := false
		for _, cond := range pod.Status.Conditions {
			if cond.Type == want {
				found = true
				if cond.Status != corev1.ConditionTrue {
					t.Errorf("condition %s = %s, want True", want, cond.Status)
				}
			}
		}
		if !found {
			t.Errorf("condition %s missing", want)
		}
	}
}

func TestSetPodConditionUpsert(t *testing.T) {
	oldTransition := metav1.NewTime(time.Now().Add(-time.Hour))
	pod := &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: oldTransition},
	}}}

	SetPodCondition(pod, corev1.PodReady, corev1.ConditionTrue)

	if len(pod.Status.Conditions) != 1 {
		t.Fatalf("got %d conditions, want 1 (upsert must not append): %+v", len(pod.Status.Conditions), pod.Status.Conditions)
	}
	cond := pod.Status.Conditions[0]
	if cond.Status != corev1.ConditionTrue {
		t.Errorf("status = %s, want True", cond.Status)
	}
	if cond.LastTransitionTime.Equal(&oldTransition) {
		t.Error("LastTransitionTime must move when the status flips")
	}

	// Same-status write is idempotent: no duplicate, transition time pinned.
	pinned := cond.LastTransitionTime
	SetPodCondition(pod, corev1.PodReady, corev1.ConditionTrue)
	if len(pod.Status.Conditions) != 1 {
		t.Fatalf("got %d conditions, want 1: %+v", len(pod.Status.Conditions), pod.Status.Conditions)
	}
	if !pod.Status.Conditions[0].LastTransitionTime.Equal(&pinned) {
		t.Error("LastTransitionTime must not move when the status is unchanged")
	}

	// A new condition type is appended, not overwritten.
	SetPodCondition(pod, "test.rbg.io/gate", corev1.ConditionFalse)
	if diff := cmp.Diff([]corev1.PodConditionType{corev1.PodReady, "test.rbg.io/gate"}, conditionTypes(pod)); diff != "" {
		t.Errorf("condition types mismatch (-want +got):\n%s", diff)
	}
}

func conditionTypes(pod *corev1.Pod) []corev1.PodConditionType {
	types := make([]corev1.PodConditionType, 0, len(pod.Status.Conditions))
	for _, cond := range pod.Status.Conditions {
		types = append(types, cond.Type)
	}
	return types
}

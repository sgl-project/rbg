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

package readiness

import (
	"fmt"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	podadapter "sigs.k8s.io/rbgs/pkg/inplace/pod/clientadapter"
)

// fakeAdapter is a minimal in-memory Adapter for unit tests.
type fakeAdapter struct {
	pod *v1.Pod
}

func (f *fakeAdapter) GetPod(namespace, name string) (*v1.Pod, error) {
	if f.pod == nil || f.pod.Namespace != namespace || f.pod.Name != name {
		return nil, fmt.Errorf("pod %s/%s not found", namespace, name)
	}
	return f.pod.DeepCopy(), nil
}

func (f *fakeAdapter) UpdatePod(pod *v1.Pod) error {
	f.pod = pod.DeepCopy()
	return nil
}

func (f *fakeAdapter) UpdatePodStatus(pod *v1.Pod) error {
	f.pod = pod.DeepCopy()
	return nil
}

var _ podadapter.Adapter = &fakeAdapter{}

// newReadyPod creates a pod that simulates the state after KWOK's pod-ready
// stage fires: all conditions True, both readiness gates present and True.
func newReadyPod() *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "test-ns",
			Name:      "test-pod",
		},
		Spec: v1.PodSpec{
			ReadinessGates: []v1.PodReadinessGate{
				{ConditionType: constants.InstancePodReadyConditionType},
				{ConditionType: "InPlaceUpdateReady"},
			},
		},
		Status: v1.PodStatus{
			Conditions: []v1.PodCondition{
				{
					Type:               v1.PodReady,
					Status:             v1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               v1.ContainersReady,
					Status:             v1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               constants.InstancePodReadyConditionType,
					Status:             v1.ConditionTrue,
					Message:            "[]",
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               "InPlaceUpdateReady",
					Status:             v1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               v1.PodScheduled,
					Status:             v1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
}

func getPodConditionStatus(pod *v1.Pod, condType v1.PodConditionType) v1.ConditionStatus {
	for _, c := range pod.Status.Conditions {
		if c.Type == condType {
			return c.Status
		}
	}
	return ""
}

// TestRemoveNotReadyKey_UpdatesPodReadyCondition reproduces the bug where
// removeNotReadyKey did not re-evaluate the pod's Ready condition after
// setting a readiness gate back to True. This caused pods to stay
// Ready=False permanently in KWOK environments.
//
// The sequence:
//  1. KWOK sets pod Ready=True with all readiness gates True
//  2. addNotReadyKey sets InstancePodReady=False → Ready becomes False
//  3. removeNotReadyKey sets InstancePodReady=True → Ready must become True
//
// Before the fix, step 3 left Ready=False because UpdatePodReadyCondition
// was not called.
func TestRemoveNotReadyKey_UpdatesPodReadyCondition(t *testing.T) {
	pod := newReadyPod()
	adp := &fakeAdapter{pod: pod}

	msg := Message{UserAgent: "Lifecycle", Key: "InstanceReady"}
	condType := constants.InstancePodReadyConditionType

	// Step 1: Verify initial state — pod is fully ready (simulates KWOK).
	if s := getPodConditionStatus(adp.pod, v1.PodReady); s != v1.ConditionTrue {
		t.Fatalf("precondition: expected Ready=True, got %s", s)
	}

	// Step 2: addNotReadyKey — sets InstancePodReady=False and Ready=False.
	modified, err := addNotReadyKey(adp, adp.pod, msg, condType)
	if err != nil {
		t.Fatalf("addNotReadyKey failed: %v", err)
	}
	if !modified {
		t.Fatal("addNotReadyKey should have modified the pod")
	}
	if s := getPodConditionStatus(adp.pod, condType); s != v1.ConditionFalse {
		t.Fatalf("after addNotReadyKey: expected InstancePodReady=False, got %s", s)
	}
	if s := getPodConditionStatus(adp.pod, v1.PodReady); s != v1.ConditionFalse {
		t.Fatalf("after addNotReadyKey: expected Ready=False, got %s", s)
	}

	// Step 3: removeNotReadyKey — sets InstancePodReady=True.
	// This is the critical assertion: Ready must also become True.
	modified, err = removeNotReadyKey(adp, adp.pod, msg, condType)
	if err != nil {
		t.Fatalf("removeNotReadyKey failed: %v", err)
	}
	if !modified {
		t.Fatal("removeNotReadyKey should have modified the pod")
	}
	if s := getPodConditionStatus(adp.pod, condType); s != v1.ConditionTrue {
		t.Fatalf("after removeNotReadyKey: expected InstancePodReady=True, got %s", s)
	}
	if s := getPodConditionStatus(adp.pod, v1.PodReady); s != v1.ConditionTrue {
		t.Fatalf("after removeNotReadyKey: expected Ready=True, got %s (bug: UpdatePodReadyCondition not called)", s)
	}
}

// TestAddRemoveNotReadyKey_MultipleKeys verifies that Ready only becomes True
// once all not-ready keys are removed, not just one.
func TestAddRemoveNotReadyKey_MultipleKeys(t *testing.T) {
	pod := newReadyPod()
	adp := &fakeAdapter{pod: pod}

	msg1 := Message{UserAgent: "Lifecycle", Key: "InstanceReady"}
	msg2 := Message{UserAgent: "InPlaceUpdate", Key: "Updating"}
	condType := constants.InstancePodReadyConditionType

	// Add two not-ready keys.
	if _, err := addNotReadyKey(adp, adp.pod, msg1, condType); err != nil {
		t.Fatalf("addNotReadyKey(msg1) failed: %v", err)
	}
	if _, err := addNotReadyKey(adp, adp.pod, msg2, condType); err != nil {
		t.Fatalf("addNotReadyKey(msg2) failed: %v", err)
	}
	if s := getPodConditionStatus(adp.pod, v1.PodReady); s != v1.ConditionFalse {
		t.Fatalf("after adding two keys: expected Ready=False, got %s", s)
	}

	// Remove first key — Ready should remain False (one key still present).
	if _, err := removeNotReadyKey(adp, adp.pod, msg1, condType); err != nil {
		t.Fatalf("removeNotReadyKey(msg1) failed: %v", err)
	}
	if s := getPodConditionStatus(adp.pod, condType); s != v1.ConditionFalse {
		t.Fatalf("after removing one key: expected InstancePodReady=False, got %s", s)
	}
	if s := getPodConditionStatus(adp.pod, v1.PodReady); s != v1.ConditionFalse {
		t.Fatalf("after removing one key: expected Ready=False, got %s", s)
	}

	// Remove second key — now Ready should become True.
	if _, err := removeNotReadyKey(adp, adp.pod, msg2, condType); err != nil {
		t.Fatalf("removeNotReadyKey(msg2) failed: %v", err)
	}
	if s := getPodConditionStatus(adp.pod, condType); s != v1.ConditionTrue {
		t.Fatalf("after removing all keys: expected InstancePodReady=True, got %s", s)
	}
	if s := getPodConditionStatus(adp.pod, v1.PodReady); s != v1.ConditionTrue {
		t.Fatalf("after removing all keys: expected Ready=True, got %s", s)
	}
}

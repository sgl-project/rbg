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
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	podadapter "sigs.k8s.io/rbgs/pkg/inplace/pod/clientadapter"
)

var gateCondType = constants.InstancePodReadyConditionType

// recordingAdapter is an in-memory Adapter that records the exact pod object
// passed to every UpdatePodStatus call. Deliberately, it does nothing else.
//
// A previous version of this fake additionally recomputed the Ready condition
// the way kubelet would, immediately after every write. That made every
// assertion on Ready converge to the same value whether or not the controller
// itself had written Ready, so the tests passed both with and without the
// write they were meant to guard against. Here, any Ready value observed in a
// recorded write is a value the controller put there itself.
type recordingAdapter struct {
	pod    *v1.Pod
	writes []*v1.Pod
}

var _ podadapter.Adapter = &recordingAdapter{}

func (f *recordingAdapter) GetPod(namespace, name string) (*v1.Pod, error) {
	if f.pod == nil || f.pod.Namespace != namespace || f.pod.Name != name {
		return nil, fmt.Errorf("pod %s/%s not found", namespace, name)
	}
	return f.pod.DeepCopy(), nil
}

func (f *recordingAdapter) UpdatePod(pod *v1.Pod) error {
	f.pod = pod.DeepCopy()
	return nil
}

func (f *recordingAdapter) UpdatePodStatus(pod *v1.Pod) error {
	f.writes = append(f.writes, pod.DeepCopy())
	f.pod = pod.DeepCopy()
	return nil
}

// kubeletManagedReady returns a deliberately distinctive Ready condition.
// Ready is kubelet-owned: kubelet derives it from ContainersReady plus the
// readiness gates, and a controller that also writes it makes kubelet's
// status cache diverge from the API server, so kubelet's next 10s tick
// republishes a stale value (the observed True->False->True flap). The
// marker Reason/Message make any controller write to Ready show up in a
// DeepEqual even when the Status it writes happens to equal the current one.
func kubeletManagedReady(status v1.ConditionStatus) v1.PodCondition {
	return v1.PodCondition{
		Type:               v1.PodReady,
		Status:             status,
		Reason:             "KubeletManaged",
		Message:            "owned by kubelet; the controller must not rewrite this condition",
		LastTransitionTime: metav1.NewTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)),
	}
}

// servingPod is a pod that has fully come up: containers ready, both gates
// satisfied, and kubelet has published Ready=True.
func servingPod() *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "test-ns",
			Name:      "test-pod",
		},
		Spec: v1.PodSpec{
			ReadinessGates: []v1.PodReadinessGate{
				{ConditionType: constants.InstancePodReadyConditionType},
				{ConditionType: constants.InPlaceUpdateReady},
			},
		},
		Status: v1.PodStatus{
			Conditions: []v1.PodCondition{
				kubeletManagedReady(v1.ConditionTrue),
				{Type: v1.ContainersReady, Status: v1.ConditionTrue},
				{Type: constants.InPlaceUpdateReady, Status: v1.ConditionTrue},
				{Type: gateCondType, Status: v1.ConditionTrue, Message: "[]"},
			},
		},
	}
}

// gatedOutPod is a serving pod taken out of rotation: the gate is False
// carrying the given not-ready keys, and kubelet has already reacted to the
// gate by publishing Ready=False.
func gatedOutPod(msgs ...Message) *v1.Pod {
	pod := servingPod()
	setCondition(pod, kubeletManagedReady(v1.ConditionFalse))
	setGateCondition(pod, v1.ConditionFalse, messageList(msgs).dump())
	return pod
}

func setCondition(pod *v1.Pod, condition v1.PodCondition) {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == condition.Type {
			pod.Status.Conditions[i] = condition
			return
		}
	}
	pod.Status.Conditions = append(pod.Status.Conditions, condition)
}

func setGateCondition(pod *v1.Pod, status v1.ConditionStatus, message string) {
	setCondition(pod, v1.PodCondition{
		Type:               gateCondType,
		Status:             status,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

func dropConditionOfType(pod *v1.Pod, condType v1.PodConditionType) {
	kept := pod.Status.Conditions[:0]
	for _, c := range pod.Status.Conditions {
		if c.Type != condType {
			kept = append(kept, c)
		}
	}
	pod.Status.Conditions = kept
}

func mustCondition(t *testing.T, pod *v1.Pod, condType v1.PodConditionType) v1.PodCondition {
	t.Helper()
	for _, c := range pod.Status.Conditions {
		if c.Type == condType {
			return c
		}
	}
	t.Fatalf("condition %s not found on pod", condType)
	return v1.PodCondition{}
}

// assertOnlyGateConditionMayChange fails if written differs from before in
// any condition other than the readiness gate: the gate is the only condition
// the controller owns. Ready is the one that matters — writing it is the
// defect these tests guard against — but ContainersReady & co. are equally
// not ours to touch.
func assertOnlyGateConditionMayChange(t *testing.T, before, written *v1.Pod) {
	t.Helper()
	beforeByType := make(map[v1.PodConditionType]v1.PodCondition, len(before.Status.Conditions))
	for _, c := range before.Status.Conditions {
		beforeByType[c.Type] = c
	}
	seen := make(map[v1.PodConditionType]bool, len(written.Status.Conditions))
	for _, c := range written.Status.Conditions {
		seen[c.Type] = true
		if c.Type == gateCondType {
			continue
		}
		b, ok := beforeByType[c.Type]
		if !ok {
			t.Errorf("controller write added unexpected condition %s: %+v", c.Type, c)
			continue
		}
		if !reflect.DeepEqual(b, c) {
			t.Errorf("controller write modified condition %s, which it does not own:\n  before: %+v\n  after:  %+v", c.Type, b, c)
		}
	}
	for condType := range beforeByType {
		if !seen[condType] {
			t.Errorf("controller write dropped condition %s", condType)
		}
	}
}

func assertGateMessages(t *testing.T, gate v1.PodCondition, want []Message) {
	t.Helper()
	var got []Message
	if gate.Message != "" {
		if err := json.Unmarshal([]byte(gate.Message), &got); err != nil {
			t.Fatalf("gate message %q is not valid JSON: %v", gate.Message, err)
		}
	}
	gotSet := make(map[Message]bool, len(got))
	for _, m := range got {
		if gotSet[m] {
			t.Errorf("gate message %q contains duplicate %+v", gate.Message, m)
		}
		gotSet[m] = true
	}
	if len(got) != len(want) {
		t.Errorf("gate messages = %v, want %v", got, want)
		return
	}
	for _, m := range want {
		if !gotSet[m] {
			t.Errorf("gate messages = %v, want %v", got, want)
			return
		}
	}
}

// TestAddNotReadyKey_LeavesReadyConditionToKubelet guards the ownership
// contract on the take-out-of-rotation path: the write flips the readiness
// gate to False and nothing else. Kubelet observes the gate and publishes
// Ready=False itself, within tens of milliseconds on a settled pod, so
// writing Ready here gains nothing and breaks the single-writer rule.
func TestAddNotReadyKey_LeavesReadyConditionToKubelet(t *testing.T) {
	pod := servingPod()
	adp := &recordingAdapter{pod: pod}
	before := pod.DeepCopy()

	msg := Message{UserAgent: "Lifecycle", Key: "InstanceReady"}
	modified, err := addNotReadyKey(adp, pod, msg, gateCondType)
	if err != nil {
		t.Fatalf("addNotReadyKey failed: %v", err)
	}
	if !modified {
		t.Fatal("addNotReadyKey should have modified the pod")
	}
	if len(adp.writes) != 1 {
		t.Fatalf("expected exactly 1 status write, got %d", len(adp.writes))
	}
	written := adp.writes[0]

	gate := mustCondition(t, written, gateCondType)
	if gate.Status != v1.ConditionFalse {
		t.Errorf("gate status = %s, want False", gate.Status)
	}
	assertGateMessages(t, gate, []Message{msg})

	assertOnlyGateConditionMayChange(t, before, written)
	if got := mustCondition(t, written, v1.PodReady); got.Status != v1.ConditionTrue {
		t.Errorf("Ready must be left untouched at True; controller wrote %s", got.Status)
	}
}

// TestRemoveNotReadyKey_LeavesReadyConditionToKubelet guards the ownership
// contract on the return-to-rotation path: the write flips the readiness
// gate back to True and nothing else. Ready stays False in the write on
// purpose — flipping it is kubelet's job. The old behavior of writing
// Ready=True here is what made kubelet's next 10s sync republish its stale
// Ready=False over ours.
func TestRemoveNotReadyKey_LeavesReadyConditionToKubelet(t *testing.T) {
	msg := Message{UserAgent: "Lifecycle", Key: "InstanceReady"}
	pod := gatedOutPod(msg)
	adp := &recordingAdapter{pod: pod}
	before := pod.DeepCopy()

	modified, err := removeNotReadyKey(adp, pod, msg, gateCondType)
	if err != nil {
		t.Fatalf("removeNotReadyKey failed: %v", err)
	}
	if !modified {
		t.Fatal("removeNotReadyKey should have modified the pod")
	}
	if len(adp.writes) != 1 {
		t.Fatalf("expected exactly 1 status write, got %d", len(adp.writes))
	}
	written := adp.writes[0]

	gate := mustCondition(t, written, gateCondType)
	if gate.Status != v1.ConditionTrue {
		t.Errorf("gate status = %s, want True", gate.Status)
	}
	assertGateMessages(t, gate, nil)

	assertOnlyGateConditionMayChange(t, before, written)
	if got := mustCondition(t, written, v1.PodReady); got.Status != v1.ConditionFalse {
		t.Errorf("Ready must be left untouched at False for kubelet to flip; controller wrote %s", got.Status)
	}
}

// TestAddNotReadyKey_GateCondition pins the gate-condition behavior the fix
// must preserve: when the gate is written, which keys it carries, and when
// no write happens at all. Redundant writes are worth pinning because every
// status write is what wakes kubelet's pod-readiness reconciliation.
func TestAddNotReadyKey_GateCondition(t *testing.T) {
	msg := Message{UserAgent: "Lifecycle", Key: "InstanceReady"}

	cases := []struct {
		name         string
		prepare      func(pod *v1.Pod)
		wantModified bool
		wantWrites   int
		wantStatus   v1.ConditionStatus
		wantMessages []Message
	}{
		{
			name: "gate condition absent is created as False with the key",
			prepare: func(pod *v1.Pod) {
				dropConditionOfType(pod, gateCondType)
			},
			wantModified: true,
			wantWrites:   1,
			wantStatus:   v1.ConditionFalse,
			wantMessages: []Message{msg},
		},
		{
			name:         "gate condition True gains the key and flips to False",
			prepare:      func(pod *v1.Pod) {},
			wantModified: true,
			wantWrites:   1,
			wantStatus:   v1.ConditionFalse,
			wantMessages: []Message{msg},
		},
		{
			name: "key already present is a no-op with no status write",
			prepare: func(pod *v1.Pod) {
				setGateCondition(pod, v1.ConditionFalse, messageList{msg}.dump())
			},
			wantModified: false,
			wantWrites:   0,
		},
		{
			name: "pod without the readiness gate is left alone",
			prepare: func(pod *v1.Pod) {
				pod.Spec.ReadinessGates = nil
			},
			wantModified: false,
			wantWrites:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := servingPod()
			tc.prepare(pod)
			adp := &recordingAdapter{pod: pod}
			before := pod.DeepCopy()

			modified, err := addNotReadyKey(adp, pod, msg, gateCondType)
			if err != nil {
				t.Fatalf("addNotReadyKey failed: %v", err)
			}
			if modified != tc.wantModified {
				t.Errorf("modified = %v, want %v", modified, tc.wantModified)
			}
			if len(adp.writes) != tc.wantWrites {
				t.Fatalf("expected %d status writes, got %d", tc.wantWrites, len(adp.writes))
			}
			if tc.wantWrites == 0 {
				return
			}

			written := adp.writes[0]
			gate := mustCondition(t, written, gateCondType)
			if gate.Status != tc.wantStatus {
				t.Errorf("gate status = %s, want %s", gate.Status, tc.wantStatus)
			}
			assertGateMessages(t, gate, tc.wantMessages)
			assertOnlyGateConditionMayChange(t, before, written)
		})
	}
}

// TestRemoveNotReadyKey_GateCondition pins the gate-condition behavior the
// fix must preserve: the gate goes back to True only once the last not-ready
// key is removed, and keys the caller does not own are left untouched.
func TestRemoveNotReadyKey_GateCondition(t *testing.T) {
	msg1 := Message{UserAgent: "Lifecycle", Key: "InstanceReady"}
	msg2 := Message{UserAgent: "InPlaceUpdate", Key: "Updating"}

	cases := []struct {
		name         string
		prepare      func(pod *v1.Pod)
		remove       Message
		wantModified bool
		wantWrites   int
		wantStatus   v1.ConditionStatus
		wantMessages []Message
	}{
		{
			name: "removing the last key flips the gate back to True",
			prepare: func(pod *v1.Pod) {
				setGateCondition(pod, v1.ConditionFalse, messageList{msg1}.dump())
			},
			remove:       msg1,
			wantModified: true,
			wantWrites:   1,
			wantStatus:   v1.ConditionTrue,
			wantMessages: nil,
		},
		{
			name: "removing one of two keys keeps the gate False",
			prepare: func(pod *v1.Pod) {
				setGateCondition(pod, v1.ConditionFalse, messageList{msg1, msg2}.dump())
			},
			remove:       msg1,
			wantModified: true,
			wantWrites:   1,
			wantStatus:   v1.ConditionFalse,
			wantMessages: []Message{msg2},
		},
		{
			name: "unknown key is a no-op with no status write",
			prepare: func(pod *v1.Pod) {
				setGateCondition(pod, v1.ConditionFalse, messageList{msg2}.dump())
			},
			remove:       msg1,
			wantModified: false,
			wantWrites:   0,
		},
		{
			name: "gate condition absent is a no-op with no status write",
			prepare: func(pod *v1.Pod) {
				dropConditionOfType(pod, gateCondType)
			},
			remove:       msg1,
			wantModified: false,
			wantWrites:   0,
		},
		{
			name: "pod without the readiness gate is left alone",
			prepare: func(pod *v1.Pod) {
				pod.Spec.ReadinessGates = nil
			},
			remove:       msg1,
			wantModified: false,
			wantWrites:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := gatedOutPod(msg1)
			tc.prepare(pod)
			adp := &recordingAdapter{pod: pod}
			before := pod.DeepCopy()

			modified, err := removeNotReadyKey(adp, pod, tc.remove, gateCondType)
			if err != nil {
				t.Fatalf("removeNotReadyKey failed: %v", err)
			}
			if modified != tc.wantModified {
				t.Errorf("modified = %v, want %v", modified, tc.wantModified)
			}
			if len(adp.writes) != tc.wantWrites {
				t.Fatalf("expected %d status writes, got %d", tc.wantWrites, len(adp.writes))
			}
			if tc.wantWrites == 0 {
				return
			}

			written := adp.writes[0]
			gate := mustCondition(t, written, gateCondType)
			if gate.Status != tc.wantStatus {
				t.Errorf("gate status = %s, want %s", gate.Status, tc.wantStatus)
			}
			assertGateMessages(t, gate, tc.wantMessages)
			assertOnlyGateConditionMayChange(t, before, written)
		})
	}
}

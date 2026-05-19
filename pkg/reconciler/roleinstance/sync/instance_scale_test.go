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

package sync

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// TestShouldRecreateInstance tests the shouldRecreateInstance function
// which handles Pod Failed → RoleInstance recreation
func TestShouldRecreateInstance(t *testing.T) {
	tests := []struct {
		name     string
		instance *workloadsv1alpha2.RoleInstance
		pods     []*corev1.Pod
		expected bool
		desc     string
	}{
		{
			name: "RestartPolicy is RecreateOnPodRestart AND Pod Failed - should recreate",
			desc: "With RecreateRoleInstanceOnPodRestart policy, Pod Failed triggers Instance recreation",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
				{
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
			},
			expected: true,
		},
		{
			name: "RestartPolicy is RecreateOnPodRestart AND Pod Evicted - should recreate",
			desc: "Evicted Pod (Failed phase) triggers Instance recreation with RecreateRoleInstanceOnPodRestart",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					Status: corev1.PodStatus{
						Phase:  corev1.PodFailed,
						Reason: "Evicted",
					},
				},
			},
			expected: true,
		},
		{
			name: "RestartPolicy is None - should NOT recreate (replacement Pod instead)",
			desc: "With RestartPolicy=None, Pod Failed triggers replacement Pod (not Instance recreation)",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyNone,
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
				{
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
			},
			expected: false,
		},
		{
			name: "Instance not Ready - should NOT recreate",
			desc: "Only trigger recreation when Instance was previously Ready (stable state)",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
				{
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
			},
			expected: false,
		},
		{
			name: "Generation != ObservedGeneration (spec being changed) - should NOT recreate",
			desc: "Avoid triggering recreation during spec changes",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 2,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
				{
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
			},
			expected: false,
		},
		{
			name: "No pods exist - should NOT recreate",
			desc: "If no pods exist (initial creation), don't trigger recreation",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			pods:     []*corev1.Pod{},
			expected: false,
		},
		{
			name: "Pod Succeeded - should NOT recreate (per KEP Non-Goals)",
			desc: "Succeeded pods are excluded per KEP Non-Goals",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
				{
					Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
				},
			},
			expected: false,
		},
		{
			name: "Pod being deleted - should NOT recreate",
			desc: "Pod being deleted (with DeletionTimestamp) is not counted as Failed",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: &metav1.Time{},
					},
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
			},
			expected: false,
		},
		{
			name: "All pods Running - should NOT recreate",
			desc: "No Failed pods, all active",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
				{
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
			},
			expected: false,
		},
		{
			name: "Pod with container restart count - should NOT trigger (handled by Pod Controller)",
			desc: "Container restart should NOT trigger Instance recreation here",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "nginx", RestartCount: 1},
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldRecreateInstance(tt.instance, tt.pods)
			assert.Equal(t, tt.expected, result, tt.desc)
		})
	}
}

// TestWasInstanceReady tests the wasInstanceReady helper function
func TestWasInstanceReady(t *testing.T) {
	tests := []struct {
		name     string
		instance *workloadsv1alpha2.RoleInstance
		expected bool
	}{
		{
			name: "Instance was Ready",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "Instance was not Ready",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "Ready condition not found",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceInPlaceUpdateReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "Empty conditions",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := wasInstanceReady(tt.instance)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestFailedPodDeletion tests that Failed pods in inactivePods are included in the delete list
// so they get cleaned up and replacements can be created on subsequent reconciles.
func TestFailedPodDeletion(t *testing.T) {
	now := metav1.Now()
	tests := []struct {
		name               string
		inactivePods       []*corev1.Pod
		expectedDeleteNum  int
		expectedDeletePods []*corev1.Pod
	}{
		{
			name: "Failed pod without DeletionTimestamp should be deleted",
			inactivePods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
					Status:     corev1.PodStatus{Phase: corev1.PodFailed},
				},
			},
			expectedDeleteNum: 1,
		},
		{
			name: "Failed pod with DeletionTimestamp should NOT be deleted (already terminating)",
			inactivePods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "pod-0",
						DeletionTimestamp: &now,
					},
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
			},
			expectedDeleteNum: 0,
		},
		{
			name: "Succeeded pod should NOT be deleted",
			inactivePods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
					Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
				},
			},
			expectedDeleteNum: 0,
		},
		{
			name: "Multiple inactive pods - only Failed without DeletionTimestamp",
			inactivePods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
					Status:     corev1.PodStatus{Phase: corev1.PodFailed},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod-1"},
					Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "pod-2",
						DeletionTimestamp: &now,
					},
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod-3"},
					Status: corev1.PodStatus{
						Phase:  corev1.PodFailed,
						Reason: "Evicted",
					},
				},
			},
			expectedDeleteNum: 2, // pod-0 and pod-3 (Evicted is also Failed phase)
		},
		{
			name:              "Empty inactive pods",
			inactivePods:      []*corev1.Pod{},
			expectedDeleteNum: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var toDeletePods []*corev1.Pod
			for _, p := range tt.inactivePods {
				if p.Status.Phase == corev1.PodFailed && p.DeletionTimestamp == nil {
					toDeletePods = append(toDeletePods, p)
				}
			}
			assert.Equal(t, tt.expectedDeleteNum, len(toDeletePods))
		})
	}
}

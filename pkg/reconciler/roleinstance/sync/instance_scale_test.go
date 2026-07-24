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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// TestShouldRecreateInstance tests the shouldRecreateInstance function
// which handles Pod Failed → RoleInstance recreation
func TestShouldRecreateInstance(t *testing.T) {
	tests := []struct {
		name      string
		instance  *workloadsv1alpha2.RoleInstance
		pods      []*corev1.Pod
		baselines map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline
		expected  bool
		desc      string
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
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RestartPolicyNone},
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
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
			name: "Pod with container restart count - should trigger recreation",
			desc: "Container restart triggers Instance recreation with RecreateRoleInstanceOnPodRestart",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
			expected: true,
		},
		{
			name: "Container restarted with Ignore annotation - should NOT recreate",
			desc: "Pod with restart-trigger-policy=Ignore annotation should not trigger Instance recreation on container restart",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
						Annotations: map[string]string{
							constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "nginx", RestartCount: 3},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "Pod Failed with Ignore annotation - should NOT recreate",
			desc: "Pod with restart-trigger-policy=Ignore annotation should not trigger Instance recreation",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
						},
					},
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
			},
			expected: false,
		},
		{
			name: "Mixed: ignored pod Failed + normal pod Failed - should recreate",
			desc: "Normal pod without Ignore annotation still triggers recreation",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](3)},
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
						Annotations: map[string]string{
							constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
						},
					},
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
				{
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
				{
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
			},
			expected: true,
		},
		{
			name: "All Failed pods have Ignore annotation - should NOT recreate",
			desc: "When all Failed pods are ignored, no recreation is triggered",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
						},
					},
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
						},
					},
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
			},
			expected: false,
		},
		{
			name: "CurrentRevision != UpdateRevision (rolling update in progress) - should NOT recreate",
			desc: "During a rolling update, container restarts should not trigger recreation",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-1",
					UpdateRevision:     "rev-2",
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
							{Name: "main", RestartCount: 1},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "Pod with InPlaceUpdateReady=False (in-place update in progress) - should NOT recreate",
			desc: "During an in-place update, container restart is expected and should not trigger recreation",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
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
						Name: "pod-0",
					},
					Spec: corev1.PodSpec{
						ReadinessGates: []corev1.PodReadinessGate{
							{ConditionType: constants.InPlaceUpdateReady},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{
								Type:   constants.InPlaceUpdateReady,
								Status: corev1.ConditionFalse,
							},
						},
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "Pod in-place updating but sibling pod Failed - should recreate",
			desc: "isPodInPlaceUpdating skips only that pod; a sibling PodFailed still triggers recreation",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
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
						Name: "pod-0",
					},
					Spec: corev1.PodSpec{
						ReadinessGates: []corev1.PodReadinessGate{
							{ConditionType: constants.InPlaceUpdateReady},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{
								Type:   constants.InPlaceUpdateReady,
								Status: corev1.ConditionFalse,
							},
						},
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pod-1",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodFailed,
					},
				},
			},
			expected: true,
		},
		{
			name: "Pod in-place updating but sibling container crashed - should recreate",
			desc: "isPodInPlaceUpdating skips only that pod; a sibling's unexpected container restart still triggers recreation",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
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
						Name: "pod-0",
					},
					Spec: corev1.PodSpec{
						ReadinessGates: []corev1.PodReadinessGate{
							{ConditionType: constants.InPlaceUpdateReady},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{
								Type:   constants.InPlaceUpdateReady,
								Status: corev1.ConditionFalse,
							},
						},
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pod-1",
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 2},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "All pods in-place updating with restarts - should NOT recreate",
			desc: "When all non-ignored pods are in-place updating, their restarts are expected",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
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
						Name: "pod-0",
					},
					Spec: corev1.PodSpec{
						ReadinessGates: []corev1.PodReadinessGate{
							{ConditionType: constants.InPlaceUpdateReady},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{
								Type:   constants.InPlaceUpdateReady,
								Status: corev1.ConditionFalse,
							},
						},
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pod-1",
					},
					Spec: corev1.PodSpec{
						ReadinessGates: []corev1.PodReadinessGate{
							{ConditionType: constants.InPlaceUpdateReady},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{
								Type:   constants.InPlaceUpdateReady,
								Status: corev1.ConditionFalse,
							},
						},
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "Container restart expected from in-place update (RestartCount = baseline+1) - should NOT recreate",
			desc: "After in-place update completes, a single restart per updated container is expected",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
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
					ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1, ImageID: "img-v2"},
						},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0, ImageID: "img-v1"}},
			},
			expected: false,
		},
		{
			name: "Container restart exceeds expected from in-place update (RestartCount > baseline+1) - should recreate",
			desc: "After in-place update, additional restarts beyond the expected one indicate a real crash",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
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
					ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 3, ImageID: "img-v2"},
						},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0, ImageID: "img-v1"}},
			},
			expected: true,
		},
		{
			name: "Container not in-place updated but restarted - should recreate",
			desc: "Container not tracked in baselines but has restarts indicates a crash",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
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
					ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1, ImageID: "img-v2"},
							{Name: "sidecar", RestartCount: 2},
						},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0, ImageID: "img-v1"}},
			},
			expected: true,
		},
		{
			name: "Multi-container: updated container expected restart, non-updated container crash - should recreate",
			desc: "Container A was in-place updated (restart expected), but container B crashed independently",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
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
					ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "inference", RestartCount: 1, ImageID: "img-inf-v2"}, // expected from in-place update
							{Name: "monitor", RestartCount: 1},                          // NOT in baselines → crash
						},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"inference": {RestartCount: 0, ImageID: "img-inf-v1"}},
			},
			expected: true,
		},
		{
			name: "Multi-container: all containers in-place updated, all expected restarts - should NOT recreate",
			desc: "Both containers were in-place updated and restarted within expected range",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
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
					ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "inference", RestartCount: 3, ImageID: "img-inf-v2"},
							{Name: "router", RestartCount: 1, ImageID: "img-rtr-v2"},
						},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"inference": {RestartCount: 2, ImageID: "img-inf-v1"}, "router": {RestartCount: 0, ImageID: "img-rtr-v1"}},
			},
			expected: false,
		},
		{
			name: "No baselines (nil map) with container restart - should recreate",
			desc: "Backward compatibility: no baselines means no in-place update protection",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
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
					ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
			},
			baselines: nil,
			expected:  true,
		},
		{
			name: "Baselines for different pod name - should recreate",
			desc: "Baselines exist but for a different pod; the restarted pod has no baseline protection",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
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
					ObjectMeta: metav1.ObjectMeta{Name: "pod-1"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			expected: true,
		},
		// ---- Multi-component and complex scenario tests ----
		{
			name: "Multi-component (inference+router): inference in-place updating, router pod crashed - should recreate",
			desc: "Different named components: inference is updating (skip), router crashed (detect). Tests cross-component fault detection.",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Name: "inference", Size: ptr.To[int32](1)},
						{Name: "router", Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-inference-0"},
					Spec: corev1.PodSpec{
						ReadinessGates: []corev1.PodReadinessGate{
							{ConditionType: constants.InPlaceUpdateReady},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse},
						},
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "inference", RestartCount: 1},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-router-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "router", RestartCount: 2},
						},
					},
				},
			},
			baselines: nil,
			expected:  true,
		},
		{
			name: "Multi-component: all components simultaneously in-place updating with baselines - should NOT recreate",
			desc: "Both inference and router are in-place updating, their restarts are expected",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Name: "inference", Size: ptr.To[int32](1)},
						{Name: "router", Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-inference-0"},
					Spec: corev1.PodSpec{
						ReadinessGates: []corev1.PodReadinessGate{
							{ConditionType: constants.InPlaceUpdateReady},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse},
						},
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "inference", RestartCount: 1},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-router-0"},
					Spec: corev1.PodSpec{
						ReadinessGates: []corev1.PodReadinessGate{
							{ConditionType: constants.InPlaceUpdateReady},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse},
						},
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "router", RestartCount: 1},
						},
					},
				},
			},
			baselines: nil,
			expected:  false,
		},
		{
			name: "LeaderWorker: leader in-place updating, worker PodFailed - should recreate",
			desc: "In LeaderWorker pattern, leader is updating but worker has failed",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Name: "leader", Size: ptr.To[int32](1)},
						{Name: "worker", Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-0"},
					Spec: corev1.PodSpec{
						ReadinessGates: []corev1.PodReadinessGate{
							{ConditionType: constants.InPlaceUpdateReady},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse},
						},
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-1"},
					Status: corev1.PodStatus{
						Phase: corev1.PodFailed,
					},
				},
			},
			baselines: nil,
			expected:  true,
		},
		{
			name: "Non-zero baseline: RestartCount at baseline+1 is expected - should NOT recreate",
			desc: "Pod had RestartCount=2 before update, now RestartCount=3 is expected (2+1)",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "nginx", RestartCount: 3},
						},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"nginx": {RestartCount: 2, ImageID: "img-old"}},
			},
			expected: false,
		},
		{
			name: "Non-zero baseline: RestartCount at baseline+2 is crash - should recreate",
			desc: "Pod had RestartCount=2 before update, now RestartCount=4 exceeds expected (2+1)",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "nginx", RestartCount: 4},
						},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"nginx": {RestartCount: 2, ImageID: "img-old"}},
			},
			expected: true,
		},
		{
			name: "Ignore pod in-place updating + non-ignore pod crashed - should recreate",
			desc: "Ignored pod's in-place update state doesn't matter; non-ignored pod's crash triggers recreation",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Name: "monitor", Size: ptr.To[int32](1)},
						{Name: "inference", Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test-instance-monitor-0",
						Annotations: map[string]string{constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore},
					},
					Spec: corev1.PodSpec{
						ReadinessGates: []corev1.PodReadinessGate{
							{ConditionType: constants.InPlaceUpdateReady},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse},
						},
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "monitor", RestartCount: 1},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-inference-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "inference", RestartCount: 2},
						},
					},
				},
			},
			baselines: nil,
			expected:  true,
		},
		{
			name: "Ignore pod Failed + non-ignore pod in-place updating - should NOT recreate",
			desc: "Ignored pod failure is skipped, non-ignored pod is in-place updating so also skipped",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Name: "monitor", Size: ptr.To[int32](1)},
						{Name: "inference", Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test-instance-monitor-0",
						Annotations: map[string]string{constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodFailed,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-inference-0"},
					Spec: corev1.PodSpec{
						ReadinessGates: []corev1.PodReadinessGate{
							{ConditionType: constants.InPlaceUpdateReady},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						Conditions: []corev1.PodCondition{
							{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse},
						},
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "inference", RestartCount: 1},
						},
					},
				},
			},
			baselines: nil,
			expected:  false,
		},
		{
			name: "Multi-pod multi-component: baselines protect updated pods, non-updated pod crash detected - should recreate",
			desc: "2 inference pods + 1 router; inference pods have baselines (updated), router crashed without baselines",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Name: "inference", Size: ptr.To[int32](2)},
						{Name: "router", Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-inference-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-inference-1"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-router-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"test-instance-inference-0": {"main": {RestartCount: 0, ImageID: "img-old"}},
				"test-instance-inference-1": {"main": {RestartCount: 0, ImageID: "img-old"}},
				// router has no baseline → its restart is a real crash
			},
			expected: true,
		},
		{
			name: "Multi-pod multi-component: all pods have baselines, all restarts expected - should NOT recreate",
			desc: "All pods in all components were in-place updated and restarted within expected range",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Name: "inference", Size: ptr.To[int32](2)},
						{Name: "router", Size: ptr.To[int32](1)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
					CurrentRevision:    "rev-2",
					UpdateRevision:     "rev-2",
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
					},
				},
			},
			pods: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-inference-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-inference-1"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "test-instance-router-0"},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "main", RestartCount: 1},
						},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"test-instance-inference-0": {"main": {RestartCount: 0, ImageID: "img-old"}},
				"test-instance-inference-1": {"main": {RestartCount: 0, ImageID: "img-old"}},
				"test-instance-router-0":    {"main": {RestartCount: 0, ImageID: "img-old"}},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldRecreateInstance(tt.instance, tt.pods, tt.baselines)
			assert.Equal(t, tt.expected, result, tt.desc)
		})
	}
}

// TestRestartingCachePreventsRecreation tests that the in-memory restarting cache
// prevents shouldRecreateInstanceGuarded from triggering when the instance is already restarting.
func TestRestartingCachePreventsRecreation(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-instance",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
	}
	pods := []*corev1.Pod{
		{
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "main", RestartCount: 1},
				},
			},
		},
	}

	// Without cache: shouldRecreateInstance returns true
	assert.True(t, shouldRecreateInstance(instance, pods, nil))

	// Set the in-memory cache to mark instance as restarting
	restartingCache.Store(instanceKey(instance), true)
	defer restartingCache.Delete(instanceKey(instance))

	// The guarded version with a nil apiReader (cache hit means no API call needed)
	ctrl := &realControl{}
	result := ctrl.shouldRecreateInstanceGuarded(context.Background(), instance, pods, nil)
	assert.False(t, result, "should not recreate when instance is in restarting cache")
}

// TestIsInstanceRestarting tests the isInstanceRestarting helper function
func TestIsInstanceRestarting(t *testing.T) {
	tests := []struct {
		name     string
		instance *workloadsv1alpha2.RoleInstance
		expected bool
	}{
		{
			name: "Restarting condition True",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: corev1.ConditionTrue},
					},
				},
			},
			expected: true,
		},
		{
			name: "Restarting condition False",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: corev1.ConditionFalse},
					},
				},
			},
			expected: false,
		},
		{
			name: "No Restarting condition",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isInstanceRestarting(tt.instance)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestSetRestartingCondition tests the setRestartingCondition helper function
func TestSetRestartingCondition(t *testing.T) {
	t.Run("sets condition when not present", func(t *testing.T) {
		instance := &workloadsv1alpha2.RoleInstance{}
		setRestartingCondition(instance)
		assert.True(t, isInstanceRestarting(instance))
	})

	t.Run("updates existing condition", func(t *testing.T) {
		instance := &workloadsv1alpha2.RoleInstance{
			Status: workloadsv1alpha2.RoleInstanceStatus{
				Conditions: []workloadsv1alpha2.RoleInstanceCondition{
					{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: corev1.ConditionFalse},
				},
			},
		}
		setRestartingCondition(instance)
		assert.True(t, isInstanceRestarting(instance))
	})
}

// TestClearRestarting tests that ClearRestarting removes the instance from the
// in-memory cache, preventing stale entries from blocking new instances.
func TestClearRestarting(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "test-instance"},
	}
	ctrl := &realControl{}

	// Seed the cache
	restartingCache.Store(instanceKey(instance), true)
	_, ok := restartingCache.Load(instanceKey(instance))
	assert.True(t, ok, "cache should have entry before clear")

	ctrl.ClearRestarting(instance)

	_, ok = restartingCache.Load(instanceKey(instance))
	assert.False(t, ok, "cache should not have entry after clear")
}

// TestClearRestartingPreventsStaleBlock verifies that after ClearRestarting,
// isAlreadyRestarting does not return true from a stale cache entry.
func TestClearRestartingPreventsStaleBlock(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "test-instance"},
	}
	ctrl := &realControl{}

	// Simulate restart trigger then deletion cleanup
	setRestartingCondition(instance)
	ctrl.ClearRestarting(instance)

	// With nil apiReader, isAlreadyRestarting should return false
	assert.False(t, ctrl.isAlreadyRestarting(context.Background(), instance),
		"should not be restarting after cache cleared")
}

// TestHasRecentRestart tests that hasRecentRestart correctly identifies whether
// the last restart falls within the stable threshold.
func TestHasRecentRestart(t *testing.T) {
	oldRestartTime := metav1.NewTime(time.Now().Add(-20 * time.Minute))
	recentRestartTime := metav1.NewTime(time.Now().Add(-1 * time.Minute))

	tests := []struct {
		name     string
		instance *workloadsv1alpha2.RoleInstance
		expected bool
	}{
		{
			name: "nil LastRestartTime: false",
			instance: &workloadsv1alpha2.RoleInstance{
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
				},
			},
			expected: false,
		},
		{
			name: "recent restart (within default threshold): true",
			instance: &workloadsv1alpha2.RoleInstance{
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					LastRestartTime: &recentRestartTime,
				},
			},
			expected: true,
		},
		{
			name: "old restart (beyond default threshold 20min < 10min min): false",
			instance: &workloadsv1alpha2.RoleInstance{
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					LastRestartTime: &oldRestartTime,
				},
			},
			expected: false,
		},
		{
			name: "old restart with custom maxDelay=60 (threshold=10min): false",
			instance: &workloadsv1alpha2.RoleInstance{
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{
						Type:            workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
						MaxDelaySeconds: ptr.To(int32(60)),
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					LastRestartTime: &oldRestartTime,
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, hasRecentRestart(tt.instance))
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

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func makeComponentStatus(name string, size, readyReplicas int32) workloadsv1alpha2.RoleInstanceComponentStatus {
	return workloadsv1alpha2.RoleInstanceComponentStatus{
		Name:          name,
		Size:          size,
		ReadyReplicas: readyReplicas,
	}
}

// ---------------------------------------------------------------------------
// allNamedComponentsReady
// ---------------------------------------------------------------------------

func TestAllNamedComponentsReady_EmptyDeps(t *testing.T) {
	// No dependencies — always ready, even when componentStatuses is empty.
	statuses := []workloadsv1alpha2.RoleInstanceComponentStatus{}
	assert.True(t, allNamedComponentsReady(nil, statuses))
	assert.True(t, allNamedComponentsReady([]string{}, statuses))
}

func TestAllNamedComponentsReady_AllSatisfied(t *testing.T) {
	// Both leader and worker are fully Ready.
	statuses := []workloadsv1alpha2.RoleInstanceComponentStatus{
		makeComponentStatus("leader", 1, 1),
		makeComponentStatus("worker", 2, 2),
		makeComponentStatus("router", 0, 0), // router not yet created — size=0
	}
	assert.True(t, allNamedComponentsReady([]string{"leader", "worker"}, statuses))
}

func TestAllNamedComponentsReady_OneNotReady(t *testing.T) {
	// worker is Running but readyReplicas < size.
	statuses := []workloadsv1alpha2.RoleInstanceComponentStatus{
		makeComponentStatus("leader", 1, 1),
		makeComponentStatus("worker", 2, 1), // only 1 of 2 ready
	}
	assert.False(t, allNamedComponentsReady([]string{"leader", "worker"}, statuses))
}

func TestAllNamedComponentsReady_SizeZero(t *testing.T) {
	// A dep entry with size=0 means its pods haven't been created yet — not ready.
	statuses := []workloadsv1alpha2.RoleInstanceComponentStatus{
		makeComponentStatus("leader", 0, 0),
	}
	assert.False(t, allNamedComponentsReady([]string{"leader"}, statuses))
}

func TestAllNamedComponentsReady_MissingDep(t *testing.T) {
	// Referenced component has no status entry at all — treat as not ready.
	statuses := []workloadsv1alpha2.RoleInstanceComponentStatus{
		makeComponentStatus("leader", 1, 1),
	}
	assert.False(t, allNamedComponentsReady([]string{"leader", "worker"}, statuses))
}

func TestAllNamedComponentsReady_EmptyStatuses(t *testing.T) {
	// Status slice is nil but deps are non-empty — not ready.
	assert.False(t, allNamedComponentsReady([]string{"leader"}, nil))
}

func TestAllNamedComponentsReady_ReadyReplicasLessThanSize(t *testing.T) {
	// readyReplicas=0 even though size>0.
	statuses := []workloadsv1alpha2.RoleInstanceComponentStatus{
		makeComponentStatus("leader", 1, 0),
	}
	assert.False(t, allNamedComponentsReady([]string{"leader"}, statuses))
}

func TestAllNamedComponentsReady_SingleDepFullyReady(t *testing.T) {
	statuses := []workloadsv1alpha2.RoleInstanceComponentStatus{
		makeComponentStatus("leader", 3, 3),
	}
	assert.True(t, allNamedComponentsReady([]string{"leader"}, statuses))
}

func TestAllNamedComponentsReady_PartialSetSatisfied(t *testing.T) {
	// router only depends on leader, and leader is ready — worker unrelated.
	statuses := []workloadsv1alpha2.RoleInstanceComponentStatus{
		makeComponentStatus("leader", 1, 1),
		makeComponentStatus("worker", 2, 1), // not fully ready, but not depended on
	}
	assert.True(t, allNamedComponentsReady([]string{"leader"}, statuses))
}

func TestHasTriggerPolicyIgnore(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "Pod with Ignore annotation",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
					},
				},
			},
			expected: true,
		},
		{
			name: "Pod with Inherit annotation",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyInherit,
					},
				},
			},
			expected: false,
		},
		{
			name: "Pod with no annotations",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expected: false,
		},
		{
			name: "Pod with nil annotations map",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: nil,
				},
			},
			expected: false,
		},
		{
			name: "Pod with unrecognized annotation value",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						constants.RestartTriggerPolicyAnnotationKey: "unknown",
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasTriggerPolicyIgnore(tt.pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestContainerRestarted(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "Container with RestartCount > 0",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", RestartCount: 1},
					},
				},
			},
			expected: true,
		},
		{
			name: "Container with RestartCount = 0",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", RestartCount: 0},
					},
				},
			},
			expected: false,
		},
		{
			name: "Multiple containers, one restarted",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", RestartCount: 0},
						{Name: "sidecar", RestartCount: 2},
					},
				},
			},
			expected: true,
		},
		{
			name: "No container statuses",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containerRestarted(tt.pod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsContainerRestartExpected(t *testing.T) {
	tests := []struct {
		name      string
		pod       *corev1.Pod
		baselines map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline
		expected  bool
	}{
		{
			name: "nil baselines",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", RestartCount: 1},
					},
				},
			},
			baselines: nil,
			expected:  false,
		},
		{
			name: "pod not in baselines",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-1"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", RestartCount: 1},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			expected: false,
		},
		{
			name: "restart within expected range (baseline+1)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", RestartCount: 1, ImageID: "img-v2"},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0, ImageID: "img-v1"}},
			},
			expected: true,
		},
		{
			name: "restart exceeds expected range",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", RestartCount: 3, ImageID: "img-v2"},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0, ImageID: "img-v1"}},
			},
			expected: false,
		},
		{
			name: "container not in baselines but restarted",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "sidecar", RestartCount: 1},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0, ImageID: "img-v1"}},
			},
			expected: false,
		},
		{
			name: "container with RestartCount=0 is skipped",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", RestartCount: 0},
						{Name: "sidecar", RestartCount: 0},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			expected: true,
		},
		{
			name: "non-zero baseline: restart within range",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", RestartCount: 6, ImageID: "img-v2"},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 5, ImageID: "img-v1"}},
			},
			expected: true,
		},
		{
			name: "multi-container: all within range",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "inference", RestartCount: 3, ImageID: "img-inf-v2"},
						{Name: "router", RestartCount: 1, ImageID: "img-rtr-v2"},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"inference": {RestartCount: 2, ImageID: "img-inf-v1"}, "router": {RestartCount: 0, ImageID: "img-rtr-v1"}},
			},
			expected: true,
		},
		{
			name: "multi-container: one exceeds range",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "inference", RestartCount: 3, ImageID: "img-inf-v2"},
						{Name: "router", RestartCount: 5, ImageID: "img-rtr-v2"},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"inference": {RestartCount: 2, ImageID: "img-inf-v1"}, "router": {RestartCount: 0, ImageID: "img-rtr-v1"}},
			},
			expected: false,
		},
		{
			name: "stale baseline after pod recreation (RestartCount < baseline)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", RestartCount: 1, ImageID: "img-v2"},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 5, ImageID: "img-v1"}},
			},
			expected: false,
		},
		{
			name: "same ImageID means no restart allowance",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", RestartCount: 1, ImageID: "img-v1"},
					},
				},
			},
			baselines: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0, ImageID: "img-v1"}},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isContainerRestartExpected(tt.pod, tt.baselines)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCalculateRestartDelay(t *testing.T) {
	tests := []struct {
		name          string
		baseDelay     int32
		maxDelay      int32
		restartCount  int32
		expectedDelay int32
	}{
		{name: "base=0 returns 0", baseDelay: 0, maxDelay: 600, restartCount: 0, expectedDelay: 0},
		{name: "restartCount=0 returns 0", baseDelay: 30, maxDelay: 600, restartCount: 0, expectedDelay: 0},
		{name: "round 1: base", baseDelay: 30, maxDelay: 600, restartCount: 1, expectedDelay: 30},
		{name: "round 2: 2x", baseDelay: 30, maxDelay: 600, restartCount: 2, expectedDelay: 60},
		{name: "round 3: 4x", baseDelay: 30, maxDelay: 600, restartCount: 3, expectedDelay: 120},
		{name: "round 4: 8x", baseDelay: 30, maxDelay: 600, restartCount: 4, expectedDelay: 240},
		{name: "round 5: 16x", baseDelay: 30, maxDelay: 600, restartCount: 5, expectedDelay: 480},
		{name: "round 6: capped at max", baseDelay: 30, maxDelay: 600, restartCount: 6, expectedDelay: 600},
		{name: "round 10: capped at max", baseDelay: 30, maxDelay: 600, restartCount: 10, expectedDelay: 600},
		{name: "overflow boundary (27): capped at max", baseDelay: 30, maxDelay: 600, restartCount: 27, expectedDelay: 600},
		{name: "large restart count: no overflow", baseDelay: 30, maxDelay: 600, restartCount: 50, expectedDelay: 600},
		{name: "int64 overflow at 59: capped at max", baseDelay: 30, maxDelay: 600, restartCount: 59, expectedDelay: 600},
		{name: "int64 overflow at 60: capped at max", baseDelay: 30, maxDelay: 600, restartCount: 60, expectedDelay: 600},
		{name: "int64 overflow at 61: capped at max", baseDelay: 30, maxDelay: 600, restartCount: 61, expectedDelay: 600},
		{name: "int64 overflow at 62: capped at max", baseDelay: 30, maxDelay: 600, restartCount: 62, expectedDelay: 600},
		{name: "int64 overflow uncapped at 60", baseDelay: 10, maxDelay: 0, restartCount: 60, expectedDelay: 0x7FFFFFFF},
		{name: "maxDelay=0: no cap", baseDelay: 10, maxDelay: 0, restartCount: 5, expectedDelay: 160},
		{name: "negative baseDelay returns 0", baseDelay: -1, maxDelay: 600, restartCount: 3, expectedDelay: 0},
		{name: "negative baseDelay with negative maxDelay returns 0", baseDelay: -30, maxDelay: -1, restartCount: 2, expectedDelay: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateRestartDelay(tt.baseDelay, tt.maxDelay, tt.restartCount)
			assert.Equal(t, tt.expectedDelay, result)
		})
	}
}

func TestCheckRestartBackoff(t *testing.T) {
	// Helper to create a realControl with a fake client
	c := &realControl{}

	// Create an instance that would trigger recreation
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
			Components: []workloadsv1alpha2.RoleInstanceComponent{
				{Name: "main", Size: ptr.To(int32(1))},
			},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{
			ObservedGeneration: 1,
			CurrentRevision:    "rev-1",
			UpdateRevision:     "rev-1",
			Conditions: []workloadsv1alpha2.RoleInstanceCondition{
				{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
			},
		},
	}
	// Set Generation to match ObservedGeneration
	instance.Generation = 1

	failedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed},
	}

	t.Run("RestartPolicy=None: no backoff", func(t *testing.T) {
		inst := instance.DeepCopy()
		inst.Spec.RestartPolicy = workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RestartPolicyNone}
		result := c.checkRestartBackoff(inst, nil, []*corev1.Pod{failedPod}, nil)
		assert.Equal(t, time.Duration(0), result)
	})

	t.Run("no crash detected: no backoff", func(t *testing.T) {
		inst := instance.DeepCopy()
		runningPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
		result := c.checkRestartBackoff(inst, nil, []*corev1.Pod{runningPod}, nil)
		assert.Equal(t, time.Duration(0), result)
	})

	t.Run("no LastRestartTime: no backoff (first restart)", func(t *testing.T) {
		inst := instance.DeepCopy()
		result := c.checkRestartBackoff(inst, nil, []*corev1.Pod{failedPod}, nil)
		assert.Equal(t, time.Duration(0), result)
	})

	t.Run("delay not elapsed: returns remaining duration", func(t *testing.T) {
		inst := instance.DeepCopy()
		now := metav1.Now()
		inst.Status.LastRestartTime = &now
		inst.Status.RestartCount = 1
		inst.Spec.RestartPolicy.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicy.MaxDelaySeconds = ptr.To(int32(600))
		result := c.checkRestartBackoff(inst, nil, []*corev1.Pod{failedPod}, nil)
		assert.Greater(t, result, time.Duration(0))
		assert.LessOrEqual(t, result, 30*time.Second)
	})

	t.Run("delay elapsed: no backoff", func(t *testing.T) {
		inst := instance.DeepCopy()
		past := metav1.NewTime(metav1.Now().Add(-60 * time.Second))
		inst.Status.LastRestartTime = &past
		inst.Status.RestartCount = 1
		inst.Spec.RestartPolicy.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicy.MaxDelaySeconds = ptr.To(int32(600))
		result := c.checkRestartBackoff(inst, nil, []*corev1.Pod{failedPod}, nil)
		assert.Equal(t, time.Duration(0), result)
	})
}

// ---------------------------------------------------------------------------
// updateRestartTracking
// ---------------------------------------------------------------------------

func TestUpdateRestartTracking(t *testing.T) {
	tests := []struct {
		name            string
		restartCount    int32
		lastRestartTime *metav1.Time
		maxDelaySeconds *int32
		expectedCount   int32
		desc            string
	}{
		{
			name:            "first restart: count 0→1, LastRestartTime nil",
			restartCount:    0,
			lastRestartTime: nil,
			maxDelaySeconds: nil,
			expectedCount:   1,
			desc:            "When LastRestartTime is nil, RestartCount increments from 0 to 1",
		},
		{
			name:            "recent restart: count 5→6 (no reset, within stable period)",
			restartCount:    5,
			lastRestartTime: ptr.To(metav1.NewTime(metav1.Now().Add(-1 * time.Minute))),
			maxDelaySeconds: nil, // default 600 → threshold = max(1200s, 600s) = 20min
			expectedCount:   6,
			desc:            "1 minute ago is within 20min threshold, no reset, count increments to 6",
		},
		{
			name:            "old restart with default maxDelay: count 5→1 (reset after stable period)",
			restartCount:    5,
			lastRestartTime: ptr.To(metav1.NewTime(metav1.Now().Add(-25 * time.Minute))),
			maxDelaySeconds: nil, // default 600 → threshold = max(1200s, 600s) = 20min
			expectedCount:   1,
			desc:            "25 minutes > 20min threshold, count resets to 0 then increments to 1",
		},
		{
			name:            "within stable period with default maxDelay: count 5→6 (15min < 20min)",
			restartCount:    5,
			lastRestartTime: ptr.To(metav1.NewTime(metav1.Now().Add(-15 * time.Minute))),
			maxDelaySeconds: nil,
			expectedCount:   6,
			desc:            "15 minutes < 20min threshold, no reset, count increments to 6",
		},
		{
			name:            "small maxDelay: threshold = max(20s, 10min) = 10min, old restart resets",
			restartCount:    5,
			lastRestartTime: ptr.To(metav1.NewTime(metav1.Now().Add(-11 * time.Minute))),
			maxDelaySeconds: ptr.To(int32(10)),
			expectedCount:   1,
			desc:            "maxDelay=10 → threshold=max(20s,10min)=10min, 11min > 10min, count resets to 1",
		},
		{
			name:            "small maxDelay: threshold = 10min, within period no reset",
			restartCount:    5,
			lastRestartTime: ptr.To(metav1.NewTime(metav1.Now().Add(-5 * time.Minute))),
			maxDelaySeconds: ptr.To(int32(10)),
			expectedCount:   6,
			desc:            "maxDelay=10 → threshold=10min, 5min < 10min, no reset, count increments to 6",
		},
		{
			name:            "maxDelay=0: threshold = max(0, 10min) = 10min, old restart resets",
			restartCount:    5,
			lastRestartTime: ptr.To(metav1.NewTime(metav1.Now().Add(-15 * time.Minute))),
			maxDelaySeconds: ptr.To(int32(0)),
			expectedCount:   1,
			desc:            "maxDelay=0 → threshold=max(0,10min)=10min, 15min > 10min, count resets to 1",
		},
		{
			name:            "maxDelay=0: threshold = 10min, within period no reset",
			restartCount:    3,
			lastRestartTime: ptr.To(metav1.NewTime(metav1.Now().Add(-5 * time.Minute))),
			maxDelaySeconds: ptr.To(int32(0)),
			expectedCount:   4,
			desc:            "maxDelay=0 → threshold=10min, 5min < 10min, no reset, count increments to 4",
		},
		{
			name:            "large maxDelay: threshold = max(2000s, 600s) = 2000s ≈ 33min",
			restartCount:    10,
			lastRestartTime: ptr.To(metav1.NewTime(metav1.Now().Add(-30 * time.Minute))),
			maxDelaySeconds: ptr.To(int32(1000)),
			expectedCount:   11,
			desc:            "maxDelay=1000 → threshold=max(2000s,600s)=2000s≈33min, 30min < 33min, no reset",
		},
		{
			name:            "large maxDelay: beyond threshold resets",
			restartCount:    10,
			lastRestartTime: ptr.To(metav1.NewTime(metav1.Now().Add(-35 * time.Minute))),
			maxDelaySeconds: ptr.To(int32(1000)),
			expectedCount:   1,
			desc:            "maxDelay=1000 → threshold≈33min, 35min > 33min, count resets to 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{
						Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
						MaxDelaySeconds:  tt.maxDelaySeconds,
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					RestartCount:    tt.restartCount,
					LastRestartTime: tt.lastRestartTime,
				},
			}

			updateRestartTracking(instance)

			assert.Equal(t, tt.expectedCount, instance.Status.RestartCount, tt.desc)
			assert.NotNil(t, instance.Status.LastRestartTime, "LastRestartTime should always be set after updateRestartTracking")
		})
	}
}

// ---------------------------------------------------------------------------
// syncRestartTrackingFromAPI
// ---------------------------------------------------------------------------

func TestSyncRestartTrackingFromAPI(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(scheme)

	now := metav1.NewTime(time.Now().Truncate(time.Second))
	earlier := metav1.NewTime(now.Add(-2 * time.Minute))
	later := metav1.NewTime(now.Add(2 * time.Minute))

	// assertTimeEqual compares metav1.Time values using time.Time.Equal which
	// ignores the monotonic clock reading stripped by fake client serialization.
	assertTimeEqual := func(t *testing.T, expected, actual *metav1.Time, msgAndArgs ...interface{}) {
		t.Helper()
		if expected == nil {
			assert.Nil(t, actual, msgAndArgs...)
			return
		}
		if assert.NotNil(t, actual, msgAndArgs...) {
			assert.True(t, actual.Time.Equal(expected.Time), msgAndArgs...)
		}
	}

	t.Run("nil apiReader: returns nil, instance unchanged", func(t *testing.T) {
		ctrl := &realControl{}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    3,
				LastRestartTime: &now,
			},
		}
		result := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.Nil(t, result)
		assert.Equal(t, int32(3), instance.Status.RestartCount)
		assertTimeEqual(t, &now, instance.Status.LastRestartTime)
	})

	t.Run("API not found: returns nil, instance unchanged", func(t *testing.T) {
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		ctrl := &realControl{apiReader: fakeClient}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    3,
				LastRestartTime: &now,
			},
		}
		result := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.Nil(t, result)
		assert.Equal(t, int32(3), instance.Status.RestartCount)
	})

	t.Run("fresh timestamp newer, count lower (reset): adopt both", func(t *testing.T) {
		freshOnAPI := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    1,
				LastRestartTime: &now,
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(freshOnAPI).Build()
		ctrl := &realControl{apiReader: fakeClient}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    5,
				LastRestartTime: &earlier,
			},
		}
		result := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NotNil(t, result)
		assert.Equal(t, int32(1), instance.Status.RestartCount, "should adopt fresh count (5→1 reset)")
		assertTimeEqual(t, &now, instance.Status.LastRestartTime, "should adopt fresh timestamp")
	})

	t.Run("fresh timestamp newer, count higher: adopt both", func(t *testing.T) {
		freshOnAPI := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    10,
				LastRestartTime: &now,
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(freshOnAPI).Build()
		ctrl := &realControl{apiReader: fakeClient}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    3,
				LastRestartTime: &earlier,
			},
		}
		result := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NotNil(t, result)
		assert.Equal(t, int32(10), instance.Status.RestartCount)
		assertTimeEqual(t, &now, instance.Status.LastRestartTime)
	})

	t.Run("fresh timestamp older: do not adopt timestamp, adopt count if higher", func(t *testing.T) {
		freshOnAPI := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    10,
				LastRestartTime: &earlier,
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(freshOnAPI).Build()
		ctrl := &realControl{apiReader: fakeClient}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    3,
				LastRestartTime: &now,
			},
		}
		result := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NotNil(t, result)
		assert.Equal(t, int32(10), instance.Status.RestartCount, "should adopt higher count even when timestamp is older")
		assertTimeEqual(t, &now, instance.Status.LastRestartTime)
	})

	t.Run("instance LastRestartTime nil, fresh has one: adopt both", func(t *testing.T) {
		freshOnAPI := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    2,
				LastRestartTime: &now,
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(freshOnAPI).Build()
		ctrl := &realControl{apiReader: fakeClient}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    0,
				LastRestartTime: nil,
			},
		}
		result := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NotNil(t, result)
		assert.Equal(t, int32(2), instance.Status.RestartCount)
		assertTimeEqual(t, &now, instance.Status.LastRestartTime)
	})

	t.Run("fresh LastRestartTime nil: do not adopt timestamp, adopt count if higher", func(t *testing.T) {
		freshOnAPI := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    7,
				LastRestartTime: nil,
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(freshOnAPI).Build()
		ctrl := &realControl{apiReader: fakeClient}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    3,
				LastRestartTime: &now,
			},
		}
		result := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NotNil(t, result)
		assert.Equal(t, int32(7), instance.Status.RestartCount, "should adopt higher count even without timestamp")
		assertTimeEqual(t, &now, instance.Status.LastRestartTime, "should NOT change timestamp when fresh is nil")
	})

	t.Run("fresh LastRestartTime nil, count not higher: no change", func(t *testing.T) {
		freshOnAPI := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    2,
				LastRestartTime: nil,
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(freshOnAPI).Build()
		ctrl := &realControl{apiReader: fakeClient}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    5,
				LastRestartTime: &now,
			},
		}
		result := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NotNil(t, result)
		assert.Equal(t, int32(5), instance.Status.RestartCount)
		assertTimeEqual(t, &now, instance.Status.LastRestartTime)
	})

	t.Run("same timestamp, fresh count higher: adopt count only", func(t *testing.T) {
		freshOnAPI := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    8,
				LastRestartTime: &now,
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(freshOnAPI).Build()
		ctrl := &realControl{apiReader: fakeClient}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    3,
				LastRestartTime: &now,
			},
		}
		result := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NotNil(t, result)
		assert.Equal(t, int32(8), instance.Status.RestartCount, "should adopt higher count with same timestamp")
		assertTimeEqual(t, &now, instance.Status.LastRestartTime)
	})

	t.Run("same timestamp, same count: no change", func(t *testing.T) {
		freshOnAPI := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    3,
				LastRestartTime: &now,
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(freshOnAPI).Build()
		ctrl := &realControl{apiReader: fakeClient}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    3,
				LastRestartTime: &now,
			},
		}
		result := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NotNil(t, result)
		assert.Equal(t, int32(3), instance.Status.RestartCount)
		assertTimeEqual(t, &now, instance.Status.LastRestartTime)
	})

	t.Run("fresh timestamp newer (later): adopt both even if count same", func(t *testing.T) {
		freshOnAPI := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    3,
				LastRestartTime: &later,
			},
		}
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(freshOnAPI).Build()
		ctrl := &realControl{apiReader: fakeClient}
		instance := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default"},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				RestartCount:    3,
				LastRestartTime: &now,
			},
		}
		result := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NotNil(t, result)
		assert.Equal(t, int32(3), instance.Status.RestartCount)
		assertTimeEqual(t, &later, instance.Status.LastRestartTime, "should adopt newer timestamp even if count is same")
	})
}

// ---------------------------------------------------------------------------
// shouldRecreateInstance bypass (LastRestartTime set + not Ready)
// ---------------------------------------------------------------------------

func TestShouldRecreateInstanceBypass(t *testing.T) {
	someTime := metav1.NewTime(metav1.Now().Add(-1 * time.Minute))

	baseInstance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-instance",
			Generation: 1,
		},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
			Components: []workloadsv1alpha2.RoleInstanceComponent{
				{Size: ptr.To[int32](1)},
			},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{
			ObservedGeneration: 1,
			CurrentRevision:    "rev-1",
			UpdateRevision:     "rev-1",
		},
	}

	failedPod := []*corev1.Pod{
		{Status: corev1.PodStatus{Phase: corev1.PodFailed}},
	}
	runningPod := []*corev1.Pod{
		{Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	}
	restartedPod := []*corev1.Pod{
		{
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: "main", RestartCount: 1},
				},
			},
		},
	}

	tests := []struct {
		name     string
		modify   func(*workloadsv1alpha2.RoleInstance)
		pods     []*corev1.Pod
		expected bool
		desc     string
	}{
		{
			name: "not Ready + LastRestartTime nil + PodFailed: bypass does NOT apply",
			modify: func(inst *workloadsv1alpha2.RoleInstance) {
				inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
					{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionFalse},
				}
				inst.Status.LastRestartTime = nil
			},
			pods:     failedPod,
			expected: false,
			desc:     "Without LastRestartTime, wasInstanceReady=False blocks recreation (no bypass)",
		},
		{
			name: "not Ready + LastRestartTime set + PodFailed: bypass applies",
			modify: func(inst *workloadsv1alpha2.RoleInstance) {
				inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
					{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionFalse},
				}
				inst.Status.LastRestartTime = &someTime
			},
			pods:     failedPod,
			expected: true,
			desc:     "LastRestartTime set bypasses wasInstanceReady=False check, PodFailed triggers recreate",
		},
		{
			name: "not Ready + LastRestartTime set + container restart: bypass applies",
			modify: func(inst *workloadsv1alpha2.RoleInstance) {
				inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
					{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionFalse},
				}
				inst.Status.LastRestartTime = &someTime
			},
			pods:     restartedPod,
			expected: true,
			desc:     "Bypass applies, container restart triggers recreate",
		},
		{
			name: "not Ready + LastRestartTime set + all running: no crash to detect",
			modify: func(inst *workloadsv1alpha2.RoleInstance) {
				inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
					{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionFalse},
				}
				inst.Status.LastRestartTime = &someTime
			},
			pods:     runningPod,
			expected: false,
			desc:     "Bypass applies but no PodFailed or container restart, so no recreate",
		},
		{
			name: "not Ready + LastRestartTime set + Generation mismatch: Generation check still blocks",
			modify: func(inst *workloadsv1alpha2.RoleInstance) {
				inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
					{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionFalse},
				}
				inst.Status.LastRestartTime = &someTime
				inst.Generation = 2
				inst.Status.ObservedGeneration = 1
			},
			pods:     failedPod,
			expected: false,
			desc:     "Even with bypass, Generation != ObservedGeneration blocks recreation",
		},
		{
			name: "not Ready + LastRestartTime set + revision mismatch: revision check still blocks",
			modify: func(inst *workloadsv1alpha2.RoleInstance) {
				inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
					{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionFalse},
				}
				inst.Status.LastRestartTime = &someTime
				inst.Status.CurrentRevision = "rev-1"
				inst.Status.UpdateRevision = "rev-2"
			},
			pods:     failedPod,
			expected: false,
			desc:     "Even with bypass, CurrentRevision != UpdateRevision blocks recreation",
		},
		{
			name: "Ready + LastRestartTime nil + PodFailed: normal case (no bypass needed)",
			modify: func(inst *workloadsv1alpha2.RoleInstance) {
				inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
					{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
				}
				inst.Status.LastRestartTime = nil
			},
			pods:     failedPod,
			expected: true,
			desc:     "wasInstanceReady=True, normal recreation triggered by PodFailed",
		},
		{
			name: "Ready + LastRestartTime set + PodFailed: both conditions satisfied",
			modify: func(inst *workloadsv1alpha2.RoleInstance) {
				inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
					{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
				}
				inst.Status.LastRestartTime = &someTime
			},
			pods:     failedPod,
			expected: true,
			desc:     "Ready=True and LastRestartTime set, PodFailed triggers recreate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := baseInstance.DeepCopy()
			tt.modify(inst)
			result := shouldRecreateInstance(inst, tt.pods, nil)
			assert.Equal(t, tt.expected, result, tt.desc)
		})
	}
}

// ---------------------------------------------------------------------------
// checkRestartBackoff branches: fresh != nil, restarting == true, baseDelay == 0
// ---------------------------------------------------------------------------

func TestCheckRestartBackoff_Branches(t *testing.T) {
	c := &realControl{}

	baseInstance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "test-instance", Namespace: "default", Generation: 1},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
			Components: []workloadsv1alpha2.RoleInstanceComponent{
				{Name: "main", Size: ptr.To(int32(1))},
			},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{
			ObservedGeneration: 1,
			CurrentRevision:    "rev-1",
			UpdateRevision:     "rev-1",
			Conditions: []workloadsv1alpha2.RoleInstanceCondition{
				{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
			},
		},
	}

	failedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed},
	}

	t.Run("restarting == true (from fresh): returns 0", func(t *testing.T) {
		inst := baseInstance.DeepCopy()
		now := metav1.Now()
		inst.Status.LastRestartTime = &now
		inst.Status.RestartCount = 1
		inst.Spec.RestartPolicy.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicy.MaxDelaySeconds = ptr.To(int32(600))

		fresh := inst.DeepCopy()
		fresh.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
			{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: corev1.ConditionTrue},
		}

		result := c.checkRestartBackoff(inst, fresh, []*corev1.Pod{failedPod}, nil)
		assert.Equal(t, time.Duration(0), result, "fresh Restarting=True should skip backoff")
	})

	t.Run("restarting == true (from informer, fresh == nil): returns 0", func(t *testing.T) {
		inst := baseInstance.DeepCopy()
		now := metav1.Now()
		inst.Status.LastRestartTime = &now
		inst.Status.RestartCount = 1
		inst.Spec.RestartPolicy.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicy.MaxDelaySeconds = ptr.To(int32(600))
		inst.Status.Conditions = append(inst.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{
			Type:   workloadsv1alpha2.RoleInstanceRestarting,
			Status: corev1.ConditionTrue,
		})

		result := c.checkRestartBackoff(inst, nil, []*corev1.Pod{failedPod}, nil)
		assert.Equal(t, time.Duration(0), result, "informer Restarting=True should skip backoff")
	})

	t.Run("restarting == true from informer overridden by fresh == false: proceeds with backoff", func(t *testing.T) {
		inst := baseInstance.DeepCopy()
		now := metav1.Now()
		inst.Status.LastRestartTime = &now
		inst.Status.RestartCount = 1
		inst.Spec.RestartPolicy.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicy.MaxDelaySeconds = ptr.To(int32(600))
		inst.Status.Conditions = append(inst.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{
			Type:   workloadsv1alpha2.RoleInstanceRestarting,
			Status: corev1.ConditionTrue,
		})

		fresh := inst.DeepCopy()
		fresh.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
			{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
		}

		result := c.checkRestartBackoff(inst, fresh, []*corev1.Pod{failedPod}, nil)
		assert.Greater(t, result, time.Duration(0), "fresh Restarting=False should override stale informer Restarting=True")
	})

	t.Run("baseDelay == 0 && maxDelay == 0: returns 0 (disabled)", func(t *testing.T) {
		inst := baseInstance.DeepCopy()
		now := metav1.Now()
		inst.Status.LastRestartTime = &now
		inst.Status.RestartCount = 5
		inst.Spec.RestartPolicy.BaseDelaySeconds = ptr.To(int32(0))
		inst.Spec.RestartPolicy.MaxDelaySeconds = ptr.To(int32(0))

		result := c.checkRestartBackoff(inst, nil, []*corev1.Pod{failedPod}, nil)
		assert.Equal(t, time.Duration(0), result, "backoff disabled when baseDelay=0 and maxDelay=0")
	})

	t.Run("fresh != nil: uses fresh RestartCount for delay calculation", func(t *testing.T) {
		inst := baseInstance.DeepCopy()
		now := metav1.Now()
		inst.Status.LastRestartTime = &now
		inst.Status.RestartCount = 5
		inst.Spec.RestartPolicy.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicy.MaxDelaySeconds = ptr.To(int32(600))

		fresh := inst.DeepCopy()
		fresh.Status.RestartCount = 1

		result := c.checkRestartBackoff(inst, fresh, []*corev1.Pod{failedPod}, nil)
		assert.Greater(t, result, time.Duration(0))
		assert.LessOrEqual(t, result, 30*time.Second,
			"with fresh RestartCount=1, delay should be ~30s (not 480s from informer's count=5)")
	})

	t.Run("fresh != nil with nil LastRestartTime: returns 0", func(t *testing.T) {
		inst := baseInstance.DeepCopy()
		now := metav1.Now()
		inst.Status.LastRestartTime = &now
		inst.Status.RestartCount = 1
		inst.Spec.RestartPolicy.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicy.MaxDelaySeconds = ptr.To(int32(600))

		fresh := inst.DeepCopy()
		fresh.Status.LastRestartTime = nil

		result := c.checkRestartBackoff(inst, fresh, []*corev1.Pod{failedPod}, nil)
		assert.Equal(t, time.Duration(0), result, "fresh LastRestartTime=nil means no backoff")
	})

	t.Run("Generation mismatch with failed pods: returns 0 (no backoff freeze)", func(t *testing.T) {
		inst := baseInstance.DeepCopy()
		now := metav1.Now()
		inst.Status.LastRestartTime = &now
		inst.Status.RestartCount = 3
		inst.Spec.RestartPolicy.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicy.MaxDelaySeconds = ptr.To(int32(600))
		// Simulate spec change: Generation > ObservedGeneration
		inst.Generation = 2
		inst.Status.ObservedGeneration = 1
		// Not Ready so shouldRecreateInstance returns false
		inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{}

		result := c.checkRestartBackoff(inst, nil, []*corev1.Pod{failedPod}, nil)
		assert.Equal(t, time.Duration(0), result,
			"backoff should not freeze updates when Generation != ObservedGeneration")
	})

	t.Run("CurrentRevision != UpdateRevision with failed pods: returns 0 (no backoff freeze)", func(t *testing.T) {
		inst := baseInstance.DeepCopy()
		now := metav1.Now()
		inst.Status.LastRestartTime = &now
		inst.Status.RestartCount = 3
		inst.Spec.RestartPolicy.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicy.MaxDelaySeconds = ptr.To(int32(600))
		// Simulate rolling update: revisions differ
		inst.Status.CurrentRevision = "rev-1"
		inst.Status.UpdateRevision = "rev-2"
		// Not Ready so shouldRecreateInstance returns false
		inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{}

		result := c.checkRestartBackoff(inst, nil, []*corev1.Pod{failedPod}, nil)
		assert.Equal(t, time.Duration(0), result,
			"backoff should not freeze rolling updates when CurrentRevision != UpdateRevision")
	})
}

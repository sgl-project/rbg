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
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RestartPolicyNone},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
			name: "Pod being deleted - should recreate",
			desc: "A Terminating desired member is member loss under the default policy (#542)",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Name: "worker", Size: ptr.To[int32](1)},
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
						Name:              "test-instance-worker-0",
						DeletionTimestamp: &metav1.Time{},
					},
					Status: corev1.PodStatus{Phase: corev1.PodFailed},
				},
			},
			expected: true,
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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

// TestRestartWaveGuardScopesCascadingRecreation tests that the restart wave marker
// blocks recreation only while the pre-restart Pods are still being deleted.
func TestRestartWaveGuardScopesCascadingRecreation(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-instance",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
			Components: []workloadsv1alpha2.RoleInstanceComponent{
				{Name: "main", Size: ptr.To[int32](2)},
			},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{
			ObservedGeneration:  1,
			LastReadyGeneration: 1,
			// The instance is NOT Ready: the rebuilt generation has not become
			// Ready yet, which is the #563 scenario the guard must tolerate.
			Conditions: []workloadsv1alpha2.RoleInstanceCondition{
				{
					Type:   workloadsv1alpha2.RoleInstanceReady,
					Status: corev1.ConditionFalse,
				},
			},
		},
	}
	pods := []*corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-instance-0",
				UID:  "old-pod-uid",
				Labels: map[string]string{
					constants.ComponentNameLabelKey: "main",
					constants.ComponentIDLabelKey:   "0",
				},
			},
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

	// A wave is in progress and the pre-restart Pod is still present: the
	// deletion trigger it produces must not start another wave.
	rememberRestartWave(instance, pods)
	defer restartingCache.Delete(instanceKey(instance))

	// The guarded version with a nil apiReader (cache hit means no API call needed)
	ctrl := &realControl{}
	result := ctrl.shouldRecreateInstanceGuarded(context.Background(), instance, pods, nil, nil)
	assert.False(t, result, "should not recreate while the pre-restart Pod is still present")

	// The pre-restart Pod is gone and only a replacement Pod (new UID, same name)
	// remains: the wave is complete and a new failure must be allowed to recreate
	// again, even though the instance is not Ready (#563).
	replacement := pods[0].DeepCopy()
	replacement.UID = "new-pod-uid"
	result = ctrl.shouldRecreateInstanceGuarded(context.Background(), instance, []*corev1.Pod{replacement}, nil, nil)
	assert.True(t, result, "should recreate again once the restart wave completed")
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
	oldPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-0", UID: "old-uid"}}
	t.Cleanup(func() { restartingCache.Delete(instanceKey(instance)) })

	// Simulate a restart trigger (wave marker + Restarting=True), then the
	// Ready/deletion cleanup path.
	rememberRestartWave(instance, []*corev1.Pod{oldPod})
	setRestartingCondition(instance)
	ctrl.ClearRestarting(instance)
	completeRestartingCondition(instance)

	// With nil apiReader and a completed wave, isAlreadyRestarting should return false
	assert.False(t, ctrl.isAlreadyRestarting(context.Background(), instance, []*corev1.Pod{oldPod}, nil),
		"should not be restarting after the cache entry was cleared")
}

// TestReleaseCompletedRestartWave verifies that the wave guard is released as soon
// as every pre-restart Pod is gone, flipping Restarting to False (RestartCompleted)
// so the same reconcile observes a completed wave in both backoff and recreation
// decisions.
func TestReleaseCompletedRestartWave(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "test-instance"},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			Components: []workloadsv1alpha2.RoleInstanceComponent{
				{Name: "main", Size: ptr.To[int32](1)},
			},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{
			LastRestartTime: ptr.To(metav1.NewTime(time.Now().Add(-5 * time.Second))),
			RestartCount:    1,
			Conditions: []workloadsv1alpha2.RoleInstanceCondition{
				{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: corev1.ConditionTrue},
			},
		},
	}
	podLabels := map[string]string{
		constants.ComponentNameLabelKey: "main",
		constants.ComponentIDLabelKey:   "0",
	}
	oldPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-0", UID: "old-uid", Labels: podLabels}}
	newPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-0", UID: "new-uid", Labels: podLabels}}
	ctrl := &realControl{}
	t.Cleanup(func() { restartingCache.Delete(instanceKey(instance)) })

	// Wave still in progress: the pre-restart Pod is present.
	rememberRestartWave(instance, []*corev1.Pod{oldPod})
	ctrl.releaseCompletedRestartWave(instance, nil, []*corev1.Pod{oldPod})
	_, ok := restartingCache.Load(instanceKey(instance))
	assert.True(t, ok, "wave marker must remain while a pre-restart Pod is present")
	assert.True(t, isInstanceRestarting(instance), "Restarting must remain True while the wave is active")

	// Wave complete: only the replacement Pod is present.
	fresh := instance.DeepCopy()
	ctrl.releaseCompletedRestartWave(instance, fresh, []*corev1.Pod{newPod})
	_, ok = restartingCache.Load(instanceKey(instance))
	assert.False(t, ok, "wave marker must be removed once every pre-restart Pod is gone")
	assert.False(t, isInstanceRestarting(instance), "Restarting must flip to False on completion")
	assert.False(t, isInstanceRestarting(fresh), "fresh copy must also observe completion")
	cond := findCondition(instance, workloadsv1alpha2.RoleInstanceRestarting)
	require.NotNil(t, cond)
	assert.Equal(t, "RestartCompleted", cond.Reason)

	// A completed wave allows a second recreation and applies backoff.
	instance.Generation = 1
	instance.Status.ObservedGeneration = 1
	instance.Status.CurrentRevision = "rev-1"
	instance.Status.UpdateRevision = "rev-1"
	instance.Status.LastReadyGeneration = 1
	instance.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{
		Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
		BaseDelaySeconds: ptr.To(int32(30)),
		MaxDelaySeconds:  ptr.To(int32(600)),
	}
	failedReplacement := newPod.DeepCopy()
	failedReplacement.Status.Phase = corev1.PodFailed
	assert.True(t, ctrl.shouldRecreateInstanceGuarded(context.Background(), instance, []*corev1.Pod{failedReplacement}, nil, fresh))
	assert.Greater(t, ctrl.checkRestartBackoff(instance, fresh, []*corev1.Pod{failedReplacement}, nil), time.Duration(0),
		"backoff must bound the second recreation after wave completion")
}

// TestIsAlreadyRestartingPersistedWaveFallback covers the marker-less fallback
// (controller restarted mid-wave): a persisted Restarting condition blocks only
// while a Pod from the pre-restart generation is still present.
func TestIsAlreadyRestartingPersistedWaveFallback(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))

	lastRestart := metav1.NewTime(time.Now().Add(-30 * time.Second))
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "test-instance"},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			Components: []workloadsv1alpha2.RoleInstanceComponent{
				{Name: "main", Size: ptr.To[int32](1)},
			},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{
			LastRestartTime: &lastRestart,
			Conditions: []workloadsv1alpha2.RoleInstanceCondition{
				{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: corev1.ConditionTrue},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()
	ctrl := &realControl{apiReader: c}

	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pod-0",
			CreationTimestamp: metav1.NewTime(lastRestart.Add(-time.Minute)),
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "main",
				constants.ComponentIDLabelKey:   "0",
			},
		},
	}
	newPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "pod-0",
			CreationTimestamp: metav1.NewTime(lastRestart.Add(time.Minute)),
			Labels: map[string]string{
				constants.ComponentNameLabelKey: "main",
				constants.ComponentIDLabelKey:   "0",
			},
		},
	}
	assert.True(t, ctrl.isAlreadyRestarting(context.Background(), instance, []*corev1.Pod{oldPod}, nil),
		"persisted wave with a pre-restart Pod still blocks")
	assert.False(t, ctrl.isAlreadyRestarting(context.Background(), instance, []*corev1.Pod{newPod}, nil),
		"persisted wave without pre-restart Pods no longer blocks")
}

func findCondition(instance *workloadsv1alpha2.RoleInstance, condType workloadsv1alpha2.RoleInstanceConditionType) *workloadsv1alpha2.RoleInstanceCondition {
	for i := range instance.Status.Conditions {
		if instance.Status.Conditions[i].Type == condType {
			return &instance.Status.Conditions[i]
		}
	}
	return nil
}

// TestHasRecentRestart tests that hasRecentRestart correctly identifies whether
// the last restart falls within the stable threshold.
func TestHasRecentRestart(t *testing.T) {
	oldRestartTime := metav1.NewTime(time.Now().Add(-21 * time.Minute))
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
				},
			},
			expected: false,
		},
		{
			name: "recent restart (within default threshold): true",
			instance: &workloadsv1alpha2.RoleInstance{
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					LastRestartTime: &recentRestartTime,
				},
			},
			expected: true,
		},
		{
			name: "old restart (beyond default 20min threshold): false",
			instance: &workloadsv1alpha2.RoleInstance{
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
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
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
		inst.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RestartPolicyNone}
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
		inst.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(600))
		result := c.checkRestartBackoff(inst, nil, []*corev1.Pod{failedPod}, nil)
		assert.Greater(t, result, time.Duration(0))
		assert.LessOrEqual(t, result, 30*time.Second)
	})

	t.Run("delay elapsed: no backoff", func(t *testing.T) {
		inst := instance.DeepCopy()
		past := metav1.NewTime(metav1.Now().Add(-60 * time.Second))
		inst.Status.LastRestartTime = &past
		inst.Status.RestartCount = 1
		inst.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(600))
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
						Type:            workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
						MaxDelaySeconds: tt.maxDelaySeconds,
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
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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
		inst.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(600))

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
		inst.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(600))
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
		inst.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(600))
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
		inst.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(0))
		inst.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(0))

		result := c.checkRestartBackoff(inst, nil, []*corev1.Pod{failedPod}, nil)
		assert.Equal(t, time.Duration(0), result, "backoff disabled when baseDelay=0 and maxDelay=0")
	})

	t.Run("fresh != nil: uses fresh RestartCount for delay calculation", func(t *testing.T) {
		inst := baseInstance.DeepCopy()
		now := metav1.Now()
		inst.Status.LastRestartTime = &now
		inst.Status.RestartCount = 5
		inst.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(600))

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
		inst.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(600))

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
		inst.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(600))
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
		inst.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(30))
		inst.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(600))
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

func TestScaleErrorsAndCreatesAfterOrphanPodGC(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	assert.NoError(t, corev1.AddToScheme(scheme))
	assert.NoError(t, workloadsv1alpha2.AddToScheme(scheme))

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "role-0",
			Namespace: "default",
			UID:       "new-role-instance",
			Labels: map[string]string{
				constants.RoleTypeLabelKey: string(constants.PodTemplateTemplateType),
			},
		},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			Components: []workloadsv1alpha2.RoleInstanceComponent{{
				Name: "role",
				Size: ptr.To(int32(1)),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "main", Image: "rbg:latest"}},
					},
				},
			}},
		},
	}

	// A StatefulSet-to-RoleInstanceSet migration can leave a same-name Pod owned
	// by the removed legacy workload while the new RoleInstance already exists.
	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
				Name:       "role",
				UID:        "old-statefulset",
				Controller: ptr.To(true),
			}},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oldPod).
		Build()
	recorder := record.NewFakeRecorder(10)
	ctrl := &realControl{
		Client:   fakeClient,
		recorder: recorder,
	}
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}

	scaling, requeue, err := ctrl.Scale(ctx, instance, revision, revision, nil, nil, nil)
	assert.ErrorContains(t, err, "is not controlled by RoleInstance")
	assert.False(t, scaling)
	assert.Zero(t, requeue,
		"controller-runtime supplies exponential backoff for the returned error; no fixed requeue interval is needed")
	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "FailedCreate")
		assert.Contains(t, event, "is not controlled by RoleInstance")
	default:
		assert.Fail(t, "expected a warning event for the blocked pod creation")
	}

	podList := &corev1.PodList{}
	assert.NoError(t, fakeClient.List(ctx, podList, client.InNamespace(instance.Namespace)))
	assert.Len(t, podList.Items, 1, "the foreign same-name Pod must not be adopted or replaced before GC")

	assert.NoError(t, fakeClient.Delete(ctx, oldPod))
	scaling, requeue, err = ctrl.Scale(ctx, instance, revision, revision, nil, nil, nil)
	assert.NoError(t, err)
	assert.True(t, scaling)
	assert.Zero(t, requeue)

	assert.NoError(t, fakeClient.List(ctx, podList, client.InNamespace(instance.Namespace)))
	assert.Len(t, podList.Items, 1, "the replacement Pod must be created after the old Pod is removed")
	assert.Equal(t, instance.Name, podList.Items[0].Name)
	assert.True(t, metav1.IsControlledBy(&podList.Items[0], instance))
}

func TestScaleCreatesNonCollidingSiblingsDuringNameCollision(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	assert.NoError(t, corev1.AddToScheme(scheme))
	assert.NoError(t, workloadsv1alpha2.AddToScheme(scheme))

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "role-0",
			Namespace: "default",
			UID:       "new-role-instance",
			Labels: map[string]string{
				constants.RoleTypeLabelKey: string(constants.ComponentsTemplateType),
			},
		},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			Components: []workloadsv1alpha2.RoleInstanceComponent{{
				Name: "role",
				Size: ptr.To(int32(2)),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "main", Image: "rbg:latest"}},
					},
				},
			}},
		},
	}

	oldPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + "-role-0",
			Namespace: instance.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
				Name:       "role",
				UID:        "old-statefulset",
				Controller: ptr.To(true),
			}},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oldPod).
		Build()
	ctrl := &realControl{
		Client:   fakeClient,
		recorder: record.NewFakeRecorder(10),
	}
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}

	scaling, _, err := ctrl.Scale(ctx, instance, revision, revision, nil, nil, nil)
	assert.ErrorContains(t, err, "is not controlled by RoleInstance")
	assert.True(t, scaling, "non-colliding sibling pods should still be created")

	podList := &corev1.PodList{}
	assert.NoError(t, fakeClient.List(ctx, podList, client.InNamespace(instance.Namespace)))
	assert.Len(t, podList.Items, 2)
	var replacement *corev1.Pod
	for i := range podList.Items {
		if podList.Items[i].Name == "role-0-role-1" {
			replacement = &podList.Items[i]
		}
	}
	if assert.NotNil(t, replacement, "the non-colliding sibling should be created") {
		assert.True(t, metav1.IsControlledBy(replacement, instance))
	}

	assert.NoError(t, fakeClient.Delete(ctx, oldPod))
	_, _, err = ctrl.Scale(ctx, instance, revision, revision, nil, nil, nil)
	assert.NoError(t, err)

	assert.NoError(t, fakeClient.List(ctx, podList, client.InNamespace(instance.Namespace)))
	assert.Len(t, podList.Items, 2)
	for i := range podList.Items {
		assert.True(t, metav1.IsControlledBy(&podList.Items[i], instance),
			"pod %s should be controlled by the new RoleInstance", podList.Items[i].Name)
	}
}

// restartScaleFixture builds a two-member native RoleInstance whose revision has
// converged, which is the state the restart triggers evaluate.
func restartScaleFixture() (*workloadsv1alpha2.RoleInstance, []*corev1.Pod) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "restart-scale", Namespace: "default", UID: "restart-scale-uid", Generation: 1},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
			Components:          []workloadsv1alpha2.RoleInstanceComponent{{Name: "worker", Size: ptr.To[int32](2)}},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 1, CurrentRevision: "revision-1", UpdateRevision: "revision-1"},
	}
	pods := make([]*corev1.Pod, 0, 2)
	for i := 0; i < 2; i++ {
		pods = append(pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("restart-scale-worker-%d", i),
				Namespace: instance.Namespace,
				UID:       types.UID(fmt.Sprintf("pod-%d", i)),
				Labels:    map[string]string{constants.ComponentNameLabelKey: "worker", constants.ComponentIDLabelKey: strconv.Itoa(i)},
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(instance, workloadsv1alpha2.SchemeGroupVersion.WithKind("RoleInstance")),
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		})
	}
	return instance, pods
}

// restartScaleClient serves the fixture plus the given Pods through a fake client and
// returns the control bound to it.
func restartScaleClient(t *testing.T, instance *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod, funcs interceptor.Funcs) (client.Client, *realControl) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	objects := make([]client.Object, 0, 1+len(pods))
	objects = append(objects, instance)
	for _, pod := range pods {
		objects = append(objects, pod)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(instance).
		WithObjects(objects...).WithInterceptorFuncs(funcs).Build()
	return c, &realControl{Client: c, apiReader: c, recorder: record.NewFakeRecorder(10)}
}

func TestScaleDoesNotRecreateForTerminatingScaleInPod(t *testing.T) {
	for _, phase := range []corev1.PodPhase{corev1.PodRunning, corev1.PodFailed} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			instance, pods := restartScaleFixture()
			instance.Spec.Components[0].Size = ptr.To[int32](1)
			instance.Status.LastReadyGeneration = instance.Generation
			pods[1].DeletionTimestamp = ptr.To(metav1.Now())
			pods[1].Finalizers = []string{"test.rbg.io/hold"}
			pods[1].Status.Phase = phase
			pods[1].Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}
			c, control := restartScaleClient(t, instance, pods, interceptor.Funcs{})
			t.Cleanup(func() { control.ClearRestarting(instance) })
			revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}
			handled, _, err := control.Scale(ctx, instance, revision, revision, nil, pods[:1], pods[1:])
			require.NoError(t, err)
			require.False(t, handled)
			require.Zero(t, instance.Status.RestartCount)
			survivor := &corev1.Pod{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[0]), survivor))
			require.Equal(t, pods[0].UID, survivor.UID)
			require.Nil(t, survivor.DeletionTimestamp, "a scale-in shutdown must not delete a healthy desired member")
		})
	}
}

// TestScaleRetriesPendingWaveDeletions covers a transient DELETE failure during
// a restart wave: the recorded pre-restart Pods must be deleted again without
// starting a new wave or incrementing RestartCount, otherwise the wave guard
// would block recreation forever while normal scaling leaves the still-running
// desired members untouched.
func TestScaleRetriesPendingWaveDeletions(t *testing.T) {
	ctx := context.Background()
	instance, pods := restartScaleFixture()
	instance.Status.LastReadyGeneration = instance.Generation
	instance.Status.RestartCount = 1
	now := metav1.Now()
	instance.Status.LastRestartTime = &now
	instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{
		Type:   workloadsv1alpha2.RoleInstanceRestarting,
		Status: corev1.ConditionTrue,
	})
	// pods[1] is already terminating (its deletion is in progress); pods[0] is
	// still running because its DELETE failed.
	pods[1].DeletionTimestamp = ptr.To(metav1.Now())
	pods[1].Finalizers = []string{"test.rbg.io/hold"}

	// The wave recorded both Pods before the deletion was attempted.
	rememberRestartWave(instance, pods)
	t.Cleanup(func() { restartingCache.Delete(instanceKey(instance)) })

	failFirstDelete := true
	retriedDeletes := 0
	c, control := restartScaleClient(t, instance, pods, interceptor.Funcs{
		Delete: func(ctx context.Context, cc client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if obj.GetName() == pods[0].Name && failFirstDelete {
				failFirstDelete = false
				return apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, pods[0].Name, fmt.Errorf("transient conflict"))
			}
			retriedDeletes++
			return cc.Delete(ctx, obj, opts...)
		},
	})
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}

	// First attempt: the DELETE fails transiently. The wave stays armed and the
	// instance keeps its restart tracking.
	scaled, _, err := control.Scale(ctx, instance, revision, revision, nil, pods, nil)
	require.ErrorContains(t, err, "transient conflict")
	require.False(t, scaled, "the failed deletion is not handled; the reconcile requeues with the error")
	require.Equal(t, int32(1), instance.Status.RestartCount, "a failed deletion must not start a new wave")
	survivor := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[0]), survivor))
	require.Nil(t, survivor.DeletionTimestamp)

	// Retry: the still-running wave member is deleted again. This is the same
	// deletion, not a new wave: RestartCount is not incremented again.
	scaled, _, err = control.Scale(ctx, instance, revision, revision, nil, pods, nil)
	require.NoError(t, err)
	require.True(t, scaled)
	require.Equal(t, 1, retriedDeletes, "the pending wave deletion must be retried")
	require.Equal(t, int32(1), instance.Status.RestartCount, "the retry must not increment RestartCount")
	require.Error(t, c.Get(ctx, client.ObjectKeyFromObject(pods[0]), &corev1.Pod{}),
		"the retried deletion must remove the wave member")
}

// TestScaleRetriesPendingWaveDeletionsAfterControllerRestart covers the
// marker-less fallback: the controller restarted mid-wave, so only the
// persisted wave state (Restarting=True + LastRestartTime) is available. The
// non-terminating pre-restart member must still be deleted, while a replacement
// created after the restart time must be left alone and RestartCount must not
// grow.
func TestScaleRetriesPendingWaveDeletionsAfterControllerRestart(t *testing.T) {
	ctx := context.Background()
	instance, pods := restartScaleFixture()
	instance.Status.LastReadyGeneration = instance.Generation
	instance.Status.RestartCount = 1
	restartTime := metav1.NewTime(time.Now().Add(-30 * time.Second))
	instance.Status.LastRestartTime = &restartTime
	instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{
		Type:   workloadsv1alpha2.RoleInstanceRestarting,
		Status: corev1.ConditionTrue,
	})
	// pods[0] is the pre-restart member whose DELETE failed; pods[1] is a
	// replacement created after the wave started. No in-memory marker exists.
	pods[0].CreationTimestamp = metav1.NewTime(restartTime.Add(-time.Minute))
	pods[1].CreationTimestamp = metav1.NewTime(restartTime.Add(time.Minute))
	_, ok := restartingCache.Load(instanceKey(instance))
	require.False(t, ok, "fixture must model a lost wave marker")

	deletes := 0
	c, control := restartScaleClient(t, instance, pods, interceptor.Funcs{
		Delete: func(ctx context.Context, cc client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			deletes++
			return cc.Delete(ctx, obj, opts...)
		},
	})
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}

	scaled, _, err := control.Scale(ctx, instance, revision, revision, nil, pods, nil)
	require.NoError(t, err)
	require.True(t, scaled)
	require.Equal(t, 1, deletes, "only the pre-restart member must be deleted")
	require.Error(t, c.Get(ctx, client.ObjectKeyFromObject(pods[0]), &corev1.Pod{}),
		"the pre-restart member must be removed by the retried deletion")
	replacement := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[1]), replacement))
	require.Nil(t, replacement.DeletionTimestamp,
		"a replacement created after the restart time must not be deleted")
	require.Equal(t, int32(1), instance.Status.RestartCount,
		"the fallback retry must not increment RestartCount")
}

// TestWaveCompletionIgnoresNonDesiredMembers covers a scale-in leftover stuck
// Terminating on a finalizer: it is recorded by the wave but neither produces
// triggers nor blocks readiness, so it must not keep the wave open (which would
// silently disable the restart policy for an otherwise healthy instance).
func TestWaveCompletionIgnoresNonDesiredMembers(t *testing.T) {
	instance, pods := restartScaleFixture()
	instance.Spec.Components[0].Size = ptr.To[int32](1) // pods[1] becomes a scale-in leftover
	pods[1].DeletionTimestamp = ptr.To(metav1.Now())
	pods[1].Finalizers = []string{"test.rbg.io/hold"}
	restartTime := metav1.NewTime(time.Now().Add(-30 * time.Second))
	instance.Status.LastRestartTime = &restartTime
	setRestartingCondition(instance)
	rememberRestartWave(instance, pods)
	t.Cleanup(func() {
		restartingCache.Delete(instanceKey(instance))
		waveDeletesIssued.Delete(instanceKey(instance))
	})
	ctrl := &realControl{}

	// The desired member (pods[0]) is gone; only the stuck non-desired
	// leftover (pods[1]) remains. The wave must complete.
	ctrl.releaseCompletedRestartWave(instance, nil, []*corev1.Pod{pods[1]})
	_, ok := restartingCache.Load(instanceKey(instance))
	assert.False(t, ok, "a stuck non-desired member must not keep the wave open")
	assert.False(t, isInstanceRestarting(instance), "the wave must be marked complete")
	assert.False(t, ctrl.isAlreadyRestarting(context.Background(), instance, []*corev1.Pod{pods[1]}, nil),
		"the guard must not block recreation because of a non-desired leftover")

	// The marker-less fallback heuristic must agree: a non-desired pre-restart
	// Pod does not count as an active wave either.
	assert.False(t, hasPreRestartPods(instance, []*corev1.Pod{pods[1]}),
		"the persisted fallback must ignore non-desired members")
}

// TestWaveDeletionNotReissuedForLaggingInformer verifies that a DELETE already
// accepted by the API server is not re-issued when the pod informer still shows
// the recorded Pods without a DeletionTimestamp right after the wave started.
func TestWaveDeletionNotReissuedForLaggingInformer(t *testing.T) {
	ctx := context.Background()
	instance, pods := restartScaleFixture()
	instance.Status.LastReadyGeneration = instance.Generation
	// Container-restart trigger while the Pods are Running/Ready.
	pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}

	deletes := 0
	_, control := restartScaleClient(t, instance, pods, interceptor.Funcs{
		Delete: func(ctx context.Context, cc client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			deletes++
			return cc.Delete(ctx, obj, opts...)
		},
	})
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}
	t.Cleanup(func() { control.ClearRestarting(instance) })

	// Wave starts: both pre-restart Pods are deleted.
	scaled, _, err := control.Scale(ctx, instance, revision, revision, nil, pods, nil)
	require.NoError(t, err)
	require.True(t, scaled)
	require.Equal(t, 2, deletes)

	// The next reconcile still sees the stale informer view of both Pods (no
	// DeletionTimestamp) even though the DELETEs already went through.
	_, _, err = control.Scale(ctx, instance, revision, revision, nil, pods, nil)
	require.NoError(t, err)
	require.Equal(t, 2, deletes, "an already-accepted DELETE must not be re-issued")
}

func TestDeletePodsTreatsGoneAndTerminatingMembersAsHandled(t *testing.T) {
	for _, state := range []string{"gone", "terminating", "present"} {
		t.Run(state, func(t *testing.T) {
			instance, pods := restartScaleFixture()
			var livePods []*corev1.Pod
			if state != "gone" {
				if state == "terminating" {
					pods[0].DeletionTimestamp = ptr.To(metav1.Now())
					pods[0].Finalizers = []string{"test.rbg.io/hold"}
				}
				livePods = pods[:1]
			}
			deletes := 0
			_, control := restartScaleClient(t, instance, livePods, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deletes++
					return c.Delete(ctx, obj, opts...)
				},
			})
			handled, err := control.deletePods(context.Background(), instance, pods[:1])
			require.NoError(t, err)
			require.True(t, handled, "already deleting or absent members still count as handled")
			if state == "terminating" {
				require.Zero(t, deletes, "do not reissue Delete for a terminating member")
			} else {
				require.Equal(t, 1, deletes)
			}
			select {
			case event := <-control.recorder.(*record.FakeRecorder).Events:
				require.Equal(t, "present", state, "do not report an API deletion that was not performed")
				require.Contains(t, event, "SuccessfulDelete")
			default:
				require.NotEqual(t, "present", state, "report an actual successful deletion")
			}
		})
	}
}

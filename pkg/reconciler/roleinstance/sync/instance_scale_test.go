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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
			name: "Instance not Ready - explicit failure should recreate",
			desc: "An explicit failure triggers recreation even before the first Ready state",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
			expected: true,
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
			name: "Pod Succeeded without deletionTimestamp - should NOT recreate",
			desc: "A terminal phase reached while the Pod is alive is normal completion per KEP Non-Goals (only reachable when restartPolicy is not Always)",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
			name: "Succeeded member being deleted under restartPolicy Always - should recreate",
			desc: "An observed deletion is a trigger whatever the phase; kubelet assigns Succeeded to a RestartPolicy=Always Pod only while it terminates",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
				},
			},
			pods: []*corev1.Pod{
				{
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: &metav1.Time{},
					},
					Spec:   corev1.PodSpec{RestartPolicy: corev1.RestartPolicyAlways},
					Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
				},
			},
			expected: true,
		},
		{
			name: "Succeeded one-shot member being deleted - should recreate",
			desc: "The trigger does not consult the Pod-level restartPolicy: a deletion is a deletion, so a completed one-shot member being cleaned up also rebuilds the group",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{Size: ptr.To[int32](2)},
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					ObservedGeneration: 1,
				},
			},
			pods: []*corev1.Pod{
				{
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						DeletionTimestamp: &metav1.Time{},
					},
					Spec:   corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever},
					Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
				},
			},
			expected: true,
		},
		{
			name: "Desired Pod being deleted - should recreate",
			desc: "DeletionTimestamp on a desired member triggers recreation",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-instance",
					Generation: 1,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
			normalizeOriginalRestartFixtures(tt.instance, tt.pods)
			result := shouldRecreateInstance(tt.instance, tt.pods, tt.baselines)
			assert.Equal(t, tt.expected, result, tt.desc)
		})
	}
}

// TestSetRestartingCondition tests the setRestartingCondition helper function.
// The condition is a projection of status.retiringPods, so only the condition
// itself is asserted here.
func TestSetRestartingCondition(t *testing.T) {
	hasRestarting := func(instance *workloadsv1alpha2.RoleInstance) bool {
		for _, cond := range instance.Status.Conditions {
			if cond.Type == workloadsv1alpha2.RoleInstanceRestarting && cond.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}

	t.Run("sets condition when not present", func(t *testing.T) {
		instance := &workloadsv1alpha2.RoleInstance{}
		setRestartingCondition(instance)
		assert.True(t, hasRestarting(instance))
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
		assert.True(t, hasRestarting(instance))
		assert.Len(t, instance.Status.Conditions, 1, "an existing condition must be updated in place")
	})
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

// normalizeOriginalRestartFixtures supplies identity omitted by the original
// predicate tables, without changing Pod names or their baseline lookup keys.
// Only legacy fixtures use this helper; malformed-member regression cases do not.
func normalizeOriginalRestartFixtures(instance *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) {
	if len(instance.Spec.Components) == 0 {
		instance.Spec.Components = []workloadsv1alpha2.RoleInstanceComponent{{Name: "worker", Size: ptr.To(int32(len(pods)))}}
	}
	componentIndex, ordinal := 0, int32(0)
	for i := range instance.Spec.Components {
		if instance.Spec.Components[i].Name == "" {
			instance.Spec.Components[i].Name = "worker"
		}
	}
	for _, pod := range pods {
		for componentIndex < len(instance.Spec.Components)-1 && ordinal >= *instance.Spec.Components[componentIndex].Size {
			componentIndex++
			ordinal = 0
		}
		if pod.Labels == nil {
			pod.Labels = make(map[string]string)
		}
		if pod.Labels[constants.ComponentNameLabelKey] == "" {
			pod.Labels[constants.ComponentNameLabelKey] = instance.Spec.Components[componentIndex].Name
		}
		if pod.Labels[constants.ComponentIDLabelKey] == "" {
			pod.Labels[constants.ComponentIDLabelKey] = strconv.Itoa(int(ordinal))
		}
		ordinal++
	}
}

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
			name: "Init container restarted only",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses:     []corev1.ContainerStatus{{Name: "main", RestartCount: 0}},
					InitContainerStatuses: []corev1.ContainerStatus{{Name: "sidecar", RestartCount: 3}},
				},
			},
			expected: false,
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
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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

	normalizeOriginalRestartFixtures(instance, []*corev1.Pod{failedPod})

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
						Type:            workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation,
						MaxDelaySeconds: tt.maxDelaySeconds,
					},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					RestartCount:    tt.restartCount,
					LastRestartTime: tt.lastRestartTime,
				},
			}

			require.NoError(t, updateRestartTracking(instance))

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
		result, err := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NoError(t, err)
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
		result, err := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.Error(t, err)
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
		result, err := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NoError(t, err)
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
		result, err := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NoError(t, err)
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
		result, err := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, int32(10), instance.Status.RestartCount, "should adopt the authoritative API count even when timestamp is older")
		assertTimeEqual(t, &earlier, instance.Status.LastRestartTime)
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
		result, err := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NoError(t, err)
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
		result, err := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, int32(7), instance.Status.RestartCount, "should adopt higher count even without timestamp")
		assertTimeEqual(t, nil, instance.Status.LastRestartTime, "should clear stale timestamp when fresh is nil")
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
		result, err := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, int32(2), instance.Status.RestartCount)
		assertTimeEqual(t, nil, instance.Status.LastRestartTime)
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
		result, err := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NoError(t, err)
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
		result, err := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NoError(t, err)
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
		result, err := ctrl.syncRestartTrackingFromAPI(context.Background(), instance)
		assert.NoError(t, err)
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
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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
			name: "not Ready + LastRestartTime nil + PodFailed: explicit failure triggers",
			modify: func(inst *workloadsv1alpha2.RoleInstance) {
				inst.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
					{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionFalse},
				}
				inst.Status.LastRestartTime = nil
			},
			pods:     failedPod,
			expected: true,
			desc:     "Neither prior Ready nor restart history is required for an explicit failure",
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
			normalizeOriginalRestartFixtures(inst, tt.pods)
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
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation},
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

	normalizeOriginalRestartFixtures(baseInstance, []*corev1.Pod{failedPod})

	t.Run("legacy Restarting=True (from fresh): still applies backoff", func(t *testing.T) {
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
		assert.Greater(t, result, time.Duration(0), "legacy fresh Restarting=True alone must not skip backoff")
	})

	t.Run("legacy Restarting=True (from informer, fresh == nil): still applies backoff", func(t *testing.T) {
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
		assert.Greater(t, result, time.Duration(0), "legacy informer Restarting=True alone must not skip backoff")
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

	for _, useFresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("RetiringPods alone skips backoff, fresh=%t", useFresh), func(t *testing.T) {
			inst := baseInstance.DeepCopy()
			inst.Status.LastRestartTime = ptr.To(metav1.Now())
			inst.Status.RestartCount = 1
			inst.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(30))
			inst.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(600))
			inst.Status.RetiringPods = []workloadsv1alpha2.RoleInstancePodReference{{Name: failedPod.Name, UID: "old"}}
			var fresh *workloadsv1alpha2.RoleInstance
			if useFresh {
				fresh = inst.DeepCopy()
				inst.Status.RetiringPods = nil
			}
			assert.Zero(t, c.checkRestartBackoff(inst, fresh, []*corev1.Pod{failedPod}, nil))
			if useFresh {
				inst.Status.RetiringPods = fresh.Status.RetiringPods
				fresh.Status.RetiringPods = nil
				assert.Positive(t, c.checkRestartBackoff(inst, fresh, []*corev1.Pod{failedPod}, nil), "fresh empty list overrides a stale informer list")
			}
		})
	}

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
		assert.Equal(t, 2*time.Second, result,
			"generation mismatch should poll for acknowledgment instead of applying restart backoff")
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

// restartScaleFixture uses real identities and the same component labels as Pods
// created by the existing Scale path. It deliberately has no Ready history.
func restartScaleFixture() (*workloadsv1alpha2.RoleInstance, []*corev1.Pod) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name: "recovery-0", Namespace: "default", UID: "recovery-instance", Generation: 1,
			Labels: map[string]string{constants.RoleTypeLabelKey: string(constants.ComponentsTemplateType)},
		},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
				Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation,
				BaseDelaySeconds: ptr.To(int32(0)), MaxDelaySeconds: ptr.To(int32(0)),
			},
			Components: []workloadsv1alpha2.RoleInstanceComponent{{
				Name: "worker", Size: ptr.To(int32(2)),
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "main", Image: "rbg:latest"}},
				}},
			}},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{
			ObservedGeneration: 1, CurrentRevision: "revision-1", UpdateRevision: "revision-1",
		},
	}
	pods := make([]*corev1.Pod, 2)
	for i := range pods {
		pods[i] = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-worker-%d", instance.Name, i), Namespace: instance.Namespace,
				UID: types.UID(fmt.Sprintf("old-%d", i)),
				Labels: map[string]string{
					constants.ComponentNameLabelKey: "worker", constants.ComponentIDLabelKey: strconv.Itoa(i),
					constants.RoleInstanceNameLabelKey: instance.Name,
				},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(instance, workloadsv1alpha2.GroupVersion.WithKind("RoleInstance"))},
			},
			Spec:   instance.Spec.Components[0].Template.Spec,
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
	}
	return instance, pods
}

func restartScaleClient(t *testing.T, instance *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod, funcs interceptor.Funcs) (client.WithWatch, *realControl) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	objects := make([]client.Object, 1, 1+len(pods))
	objects[0] = instance
	for _, pod := range pods {
		objects = append(objects, pod)
	}
	// The fake API does not allocate UIDs. Allocate a distinct incarnation for
	// each Pod created by ordinary Scale, including repeated same-name creates.
	var created atomic.Int32
	if funcs.Create == nil {
		funcs.Create = func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.Pod); ok && obj.GetUID() == "" {
				obj.SetUID(types.UID(fmt.Sprintf("new-%d", created.Add(1))))
			}
			return c.Create(ctx, obj, opts...)
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(instance, &corev1.Pod{}).WithObjects(objects...).
		WithInterceptorFuncs(funcs).Build()
	return c, &realControl{Client: c, apiReader: c, recorder: record.NewFakeRecorder(100)}
}

func restartScaleStep(t *testing.T, ctrl *realControl, key client.ObjectKey) (*workloadsv1alpha2.RoleInstance, bool, time.Duration, error) {
	t.Helper()
	ctx := context.Background()
	instance := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, ctrl.Get(ctx, key, instance))
	pods, inactivePods, err := instanceutil.GetActiveAndInactivePods(ctx, ctrl.Client, &client.ListOptions{Namespace: key.Namespace})
	require.NoError(t, err)
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}
	scaled, requeue, err := ctrl.Scale(ctx, instance, revision, revision, nil, pods, inactivePods)
	return instance, scaled, requeue, err
}

func TestRestartRecordBeforeDelete(t *testing.T) {
	for _, failPatch := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistence failure=%t", failPatch), func(t *testing.T) {
			instance, pods := restartScaleFixture()
			pods[1].Status.Phase = corev1.PodFailed
			instance.Status.InPlaceUpdateContainerBaselines = map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				pods[0].Name: {"main": {RestartCount: 0, ImageID: "old"}},
				pods[1].Name: {"main": {RestartCount: 0, ImageID: "old"}},
				"unrelated":  {"main": {RestartCount: 2}},
			}
			expected := []workloadsv1alpha2.RoleInstancePodReference{{Name: pods[0].Name, UID: pods[0].UID}, {Name: pods[1].Name, UID: pods[1].UID}}
			patches, deletes := 0, 0
			c, ctrl := restartScaleClient(t, instance, pods, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					patches++
					data, err := patch.Data(obj)
					require.NoError(t, err)
					assert.Contains(t, string(data), `"resourceVersion"`, "status persistence must be optimistic")
					if failPatch {
						return fmt.Errorf("status persistence failed")
					}
					return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
				},
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deletes++
					persisted := &workloadsv1alpha2.RoleInstance{}
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), persisted))
					assert.ElementsMatch(t, expected, persisted.Status.RetiringPods, "all old UIDs must be saved before the first Delete")
					assert.Equal(t, int32(1), persisted.Status.RestartCount)
					assert.NotNil(t, persisted.Status.LastRestartTime)
					assert.Empty(t, persisted.Status.InPlaceUpdateContainerBaselines, "a group rebuild invalidates allowances for every old slot")
					return c.Delete(ctx, obj, opts...)
				},
			})
			_, _, _, err := restartScaleStep(t, ctrl, client.ObjectKeyFromObject(instance))
			assert.Equal(t, 1, patches)
			if failPatch {
				assert.ErrorContains(t, err, "status persistence failed")
				assert.Zero(t, deletes, "a failed status write must not delete any Pod")
				persisted := &workloadsv1alpha2.RoleInstance{}
				require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), persisted))
				assert.Empty(t, persisted.Status.RetiringPods)
				assert.Zero(t, persisted.Status.RestartCount)
			} else {
				require.NoError(t, err)
				assert.Equal(t, 2, deletes)
			}
		})
	}
}

func TestRestartFinalizerCountsOnceAndBlocksReplacement(t *testing.T) {
	ctx := context.Background()
	instance, pods := restartScaleFixture()
	pods[1].Status.Phase = corev1.PodFailed
	pods[1].Finalizers = []string{"test.example/hold"}
	c, ctrl := restartScaleClient(t, instance, pods, interceptor.Funcs{})
	key := client.ObjectKeyFromObject(instance)
	started, scaled, requeue, err := restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	assert.True(t, scaled)
	assert.Equal(t, drainSafetyRequeue, requeue, "a drain only needs a safety requeue; Pod events drive progress")
	require.Equal(t, int32(1), started.Status.RestartCount)
	changed := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, key, changed))
	changed.Spec.RestartPolicyConfig.Type = workloadsv1alpha2.RestartPolicyNone
	changed.Generation++ // an accepted deletion also survives unacknowledged spec changes
	require.NoError(t, c.Update(ctx, changed))
	for i := 0; i < 3; i++ {
		// Reconstruct control to model process loss; the status list alone resumes.
		ctrl = &realControl{Client: c, apiReader: c, recorder: record.NewFakeRecorder(100)}
		resumed, _, requeue, err := restartScaleStep(t, ctrl, key)
		require.NoError(t, err)
		assert.Equal(t, drainSafetyRequeue, requeue, "terminating UIDs still requeue even with base delay zero")
		assert.Equal(t, int32(1), resumed.Status.RestartCount)
		assert.Equal(t, started.Status.LastRestartTime, resumed.Status.LastRestartTime)
		assert.Len(t, resumed.Status.RetiringPods, 2)
		list := &corev1.PodList{}
		require.NoError(t, c.List(ctx, list))
		require.Len(t, list.Items, 1, "no replacement may be created while an old UID remains")
		assert.Equal(t, pods[1].UID, list.Items[0].UID)
	}
	held := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[1]), held))
	held.Finalizers = nil
	require.NoError(t, c.Update(ctx, held))
	cleared, scaled, requeue, err := restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	assert.Empty(t, cleared.Status.RetiringPods)
	assert.True(t, scaled, "clearing the saved deletion is progress through deletePods")
	assert.Equal(t, drainSafetyRequeue, requeue, "clearing the barrier polls once before ordinary creation")
	_, scaled, _, err = restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	assert.True(t, scaled)
	list := &corev1.PodList{}
	require.NoError(t, c.List(ctx, list))
	assert.Len(t, list.Items, 2)
}

func TestRestartBarrierUsesOldUIDNotReusedName(t *testing.T) {
	ctx := context.Background()
	instance, pods := restartScaleFixture()
	pods[1].Status.Phase = corev1.PodFailed
	pods[1].Finalizers = []string{"test.example/hold"}
	c, ctrl := restartScaleClient(t, instance, pods, interceptor.Funcs{})
	key := client.ObjectKeyFromObject(instance)
	_, _, _, err := restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	newPod := pods[0].DeepCopy()
	newPod.UID = "new-same-name"
	newPod.ResourceVersion = ""
	require.NoError(t, c.Create(ctx, newPod))
	_, _, requeue, err := restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	assert.Positive(t, requeue)
	check := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(newPod), check))
	assert.Equal(t, newPod.UID, check.UID, "resumption must never delete the newer same-name Pod")
	held := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[1]), held))
	held.Finalizers = nil
	require.NoError(t, c.Update(ctx, held))
	cleared, scaled, requeue, err := restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	assert.Empty(t, cleared.Status.RetiringPods, "the surviving new UID must not keep the old barrier alive")
	assert.Equal(t, int32(1), cleared.Status.RestartCount)
	assert.True(t, scaled)
	assert.Positive(t, requeue)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(newPod), check))
	assert.Equal(t, newPod.UID, check.UID)
}

func isReady(instance *workloadsv1alpha2.RoleInstance) bool {
	for _, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func TestRestartRepeatedAttemptsWithoutReady(t *testing.T) {
	ctx := context.Background()
	instance, pods := restartScaleFixture()
	c, ctrl := restartScaleClient(t, instance, pods, interceptor.Funcs{})
	key := client.ObjectKeyFromObject(instance)
	for attempt := int32(1); attempt <= 3; attempt++ {
		failed := &corev1.Pod{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[1]), failed))
		failed.Status.Phase = corev1.PodFailed
		require.NoError(t, c.Status().Update(ctx, failed))
		started, _, _, err := restartScaleStep(t, ctrl, key)
		require.NoError(t, err)
		assert.Equal(t, attempt, started.Status.RestartCount)
		assert.False(t, isReady(started), "recovery must not depend on the instance being Ready")
		cleared, _, requeue, err := restartScaleStep(t, ctrl, key)
		require.NoError(t, err)
		assert.Empty(t, cleared.Status.RetiringPods)
		assert.Positive(t, requeue)
		replaced, scaled, _, err := restartScaleStep(t, ctrl, key)
		require.NoError(t, err)
		assert.True(t, scaled)
		assert.Equal(t, attempt, replaced.Status.RestartCount)
		assert.False(t, isReady(replaced), "a second round must not wait for readiness")
		current := &corev1.Pod{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(failed), current))
		assert.NotEqual(t, failed.UID, current.UID)
	}
}

func TestMissedDeletionScalesNormallyThenExplicitFailureDrainsBoth(t *testing.T) {
	ctx := context.Background()
	instance, pods := restartScaleFixture()
	// B disappeared entirely between observations. Missing history must not
	// manufacture an instance restart: the existing Scale path creates B2.
	c, ctrl := restartScaleClient(t, instance, pods, interceptor.Funcs{})
	require.NoError(t, c.Delete(ctx, pods[1]))
	key := client.ObjectKeyFromObject(instance)
	ordinary, scaled, _, err := restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	assert.True(t, scaled)
	assert.Empty(t, ordinary.Status.RetiringPods)
	assert.Zero(t, ordinary.Status.RestartCount)
	b2 := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[1]), b2))
	assert.NotEqual(t, pods[1].UID, b2.UID)
	assert.True(t, metav1.IsControlledBy(b2, instance))
	b2.Status.Phase = corev1.PodFailed
	require.NoError(t, c.Status().Update(ctx, b2))
	started, _, _, err := restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	assert.ElementsMatch(t, []workloadsv1alpha2.RoleInstancePodReference{
		{Name: pods[0].Name, UID: pods[0].UID}, {Name: b2.Name, UID: b2.UID},
	}, started.Status.RetiringPods)
	assert.Equal(t, int32(1), started.Status.RestartCount)
	list := &corev1.PodList{}
	require.NoError(t, c.List(ctx, list))
	assert.Empty(t, list.Items, "an explicit B2 failure must drain both B2 and its surviving sibling")
}

func TestRestartClearsAbsentMemberBaseline(t *testing.T) {
	ctx := context.Background()
	instance, pods := restartScaleFixture()
	baselines := map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
		pods[1].Name: {"main": {RestartCount: 0, ImageID: "old-image"}},
	}
	instance.Status.InPlaceUpdateContainerBaselines = baselines
	// B is already absent when A explicitly fails, so only A can be retired.
	pods[0].Status.Phase = corev1.PodFailed
	c, ctrl := restartScaleClient(t, instance, pods[:1], interceptor.Funcs{})
	key := client.ObjectKeyFromObject(instance)
	started, _, _, err := restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	assert.Equal(t, []workloadsv1alpha2.RoleInstancePodReference{{Name: pods[0].Name, UID: pods[0].UID}}, started.Status.RetiringPods)
	assert.Equal(t, int32(1), started.Status.RestartCount)
	persisted := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, key, persisted))
	assert.Empty(t, persisted.Status.InPlaceUpdateContainerBaselines, "the absent member's old allowance must also be persisted as cleared")

	cleared, _, _, err := restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	assert.Empty(t, cleared.Status.RetiringPods)
	_, scaled, _, err := restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	assert.True(t, scaled)
	list := &corev1.PodList{}
	require.NoError(t, c.List(ctx, list))
	require.Len(t, list.Items, 2)
	for i := range list.Items {
		assert.NotEqual(t, pods[0].UID, list.Items[i].UID)
		assert.NotEqual(t, pods[1].UID, list.Items[i].UID)
		assert.True(t, metav1.IsControlledBy(&list.Items[i], instance))
	}
	freshB := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[1]), freshB))
	freshB.Status.Phase = corev1.PodRunning
	freshB.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1, ImageID: "new-image"}}
	assert.True(t, isContainerRestartExpected(freshB, baselines), "the stale name-keyed allowance would hide this genuine restart")
	require.NoError(t, c.Status().Update(ctx, freshB))
	restarted, _, _, err := restartScaleStep(t, ctrl, key)
	require.NoError(t, err)
	assert.Equal(t, int32(2), restarted.Status.RestartCount)
	assert.Len(t, restarted.Status.RetiringPods, 2)
	require.NoError(t, c.List(ctx, list))
	assert.Empty(t, list.Items, "the first unexpected restart on fresh B must drain the replacement group")
}

func TestUpdateRestartTrackingRejectsUnverifiedPod(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*corev1.Pod)
	}{
		{"missing UID", func(p *corev1.Pod) { p.UID = "" }},
		{"foreign owner", func(p *corev1.Pod) { p.OwnerReferences[0].UID = "other-instance" }},
		{"non-controller owner", func(p *corev1.Pod) { p.OwnerReferences[0].Controller = ptr.To(false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance, pods := restartScaleFixture()
			instance.Status.InPlaceUpdateContainerBaselines = map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				pods[0].Name: {"main": {RestartCount: 0, ImageID: "old-image"}},
				pods[1].Name: {"main": {RestartCount: 0, ImageID: "old-image"}},
			}
			before := instance.DeepCopy()
			tc.modify(pods[1]) // Validate a good Pod before encountering the invalid one.
			require.ErrorContains(t, updateRestartTracking(instance, pods...), "without a verified owner and UID")
			assert.Equal(t, before.Status.InPlaceUpdateContainerBaselines, instance.Status.InPlaceUpdateContainerBaselines)
			assert.Equal(t, before.Status.RestartCount, instance.Status.RestartCount)
			assert.Equal(t, before.Status.LastRestartTime, instance.Status.LastRestartTime)
		})
	}
}

func TestDesiredMemberTriggersAndInitRestartAllowance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*workloadsv1alpha2.RoleInstance, *corev1.Pod)
		want   bool
	}{
		{"desired deletion", func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) { p.DeletionTimestamp = ptr.To(metav1.Now()) }, true},
		{"unknown component", func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Labels[constants.ComponentNameLabelKey] = "removed"
			p.Status.Phase = corev1.PodFailed
		}, false},
		{"missing component label uses existing name fallback", func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			delete(p.Labels, constants.ComponentNameLabelKey)
			p.Status.Phase = corev1.PodFailed
		}, true},
		{"missing component label with unknown name", func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			delete(p.Labels, constants.ComponentNameLabelKey)
			p.Name = "removed"
			p.Status.Phase = corev1.PodFailed
		}, false},
		{"scaled down ordinal", func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Labels[constants.ComponentIDLabelKey] = "2"
			p.Status.Phase = corev1.PodFailed
		}, false},
		{"negative ordinal", func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Labels[constants.ComponentIDLabelKey] = "-1"
			p.Status.Phase = corev1.PodFailed
		}, false},
		{"malformed ordinal", func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Labels[constants.ComponentIDLabelKey] = "invalid"
			p.Status.Phase = corev1.PodFailed
		}, false},
		{"missing sibling is not a trigger", func(_ *workloadsv1alpha2.RoleInstance, _ *corev1.Pod) {}, false},
		{"init container restart is not a trigger", func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "sidecar", RestartCount: 3}}
		}, false},
		{"init restart does not consume the ordinary allowance", func(inst *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			inst.Status.InPlaceUpdateContainerBaselines = map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{p.Name: {"main": {RestartCount: 0, ImageID: "old"}}}
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1, ImageID: "new"}}
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "sidecar", RestartCount: 2, ImageID: "x"}}
		}, false},
		{"ordinary restart beyond the allowance still triggers", func(inst *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			inst.Status.InPlaceUpdateContainerBaselines = map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{p.Name: {"main": {RestartCount: 0, ImageID: "old"}}}
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 2, ImageID: "new"}}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance, pods := restartScaleFixture()
			tc.modify(instance, pods[0])
			assert.Equal(t, tc.want, shouldRecreateInstance(instance, pods[:1], instance.Status.InPlaceUpdateContainerBaselines))
			instance.Status.LastRestartTime = ptr.To(metav1.Now())
			instance.Status.RestartCount = 1
			instance.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(30))
			instance.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(600))
			delay := (&realControl{}).checkRestartBackoff(instance, nil, pods[:1], nil)
			if tc.want {
				assert.Positive(t, delay)
			} else {
				assert.Zero(t, delay, "false predicates must skip the obsolete failed-Pod slow path")
			}
		})
	}
}

func TestRestartAcceptanceUsesCurrentPodIncarnations(t *testing.T) {
	for _, tc := range []struct {
		name         string
		wantRecreate bool
	}{
		{name: "old UIDs are gone"},
		{name: "same-name replacements are healthy"},
		{name: "same UID is no longer failed"},
		{name: "current Pod ignores restart triggers"},
		{name: "same-name Pod is now foreign"},
		{name: "new incarnation has a genuine failure", wantRecreate: true},
		{name: "current sibling is absent from the informer", wantRecreate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			instance, cachedPods := restartScaleFixture()
			instance.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To[int32](30)
			instance.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To[int32](600)
			instance.Status.RestartCount = 1
			previous := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
			instance.Status.LastRestartTime = &previous
			cachedPods[0].Status.Phase = corev1.PodFailed
			livePods := []*corev1.Pod{cachedPods[0].DeepCopy(), cachedPods[1].DeepCopy()}
			for _, pod := range livePods {
				pod.Status.Phase = corev1.PodRunning
			}
			switch tc.name {
			case "old UIDs are gone":
				livePods = nil
			case "same-name replacements are healthy":
				livePods[0].UID = "new-0"
				livePods[1].UID = "new-1"
			case "current Pod ignores restart triggers":
				livePods[0].Status.Phase = corev1.PodFailed
				livePods[0].Annotations = map[string]string{constants.RestartTriggerPolicyAnnotationKey: "Ignore"}
			case "same-name Pod is now foreign":
				livePods[0].Status.Phase = corev1.PodFailed
				livePods[0].OwnerReferences[0].UID = "another-instance"
			case "new incarnation has a genuine failure":
				livePods[0].UID = "new-0"
				livePods[1].UID = "new-1"
				livePods[0].Status.Phase = corev1.PodFailed
			case "current sibling is absent from the informer":
				livePods[0].Status.Phase = corev1.PodFailed
				livePods[1].UID = "new-1"
				cachedPods = cachedPods[:1]
			}
			patches := 0
			c, control := restartScaleClient(t, instance, livePods, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					patches++
					return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
				},
			})
			accepted, err := control.shouldRecreateInstanceGuarded(ctx, instance, cachedPods, nil, "revision-1", "revision-1")
			require.NoError(t, err)
			assert.Equal(t, tc.wantRecreate, accepted)
			fresh := &workloadsv1alpha2.RoleInstance{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), fresh))
			if !tc.wantRecreate {
				assert.Zero(t, patches, "obsolete Pod evidence must not write restart tracking")
				assert.Equal(t, int32(1), fresh.Status.RestartCount)
				assert.Equal(t, previous, *fresh.Status.LastRestartTime)
				assert.Empty(t, fresh.Status.RetiringPods)
				return
			}
			assert.Equal(t, 1, patches)
			assert.Equal(t, int32(2), fresh.Status.RestartCount, "a genuine current fault charges exactly once")
			expected := make([]workloadsv1alpha2.RoleInstancePodReference, 0, len(livePods))
			for _, pod := range livePods {
				expected = append(expected, workloadsv1alpha2.RoleInstancePodReference{Name: pod.Name, UID: pod.UID})
			}
			assert.ElementsMatch(t, expected, fresh.Status.RetiringPods, "retire current members, including uncached siblings, not stale UIDs")
		})
	}
}

func TestRestartAcceptanceScopesLivePodListToInstance(t *testing.T) {
	instance, pods := restartScaleFixture()
	pods[0].Status.Phase = corev1.PodFailed
	otherInstance := pods[1].DeepCopy()
	otherInstance.Name = "other-instance-worker-0"
	otherInstance.UID = "other-member"
	otherInstance.Labels[constants.RoleInstanceNameLabelKey] = "other-instance"
	otherInstance.OwnerReferences[0].UID = "other-instance-uid"
	otherNamespace := pods[1].DeepCopy()
	otherNamespace.Namespace = "other-namespace"
	otherNamespace.UID = "other-namespace-member"
	previousInstance := pods[1].DeepCopy()
	previousInstance.Name = "previous-instance-worker-0"
	previousInstance.UID = "previous-member"
	previousInstance.OwnerReferences[0].UID = "previous-instance-uid"
	allPods := append(pods, otherInstance, otherNamespace, previousInstance)
	lists := 0
	c, control := restartScaleClient(t, instance, allPods, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			lists++
			options := (&client.ListOptions{}).ApplyOptions(opts)
			assert.Equal(t, instance.Namespace, options.Namespace)
			require.NotNil(t, options.LabelSelector, "live Pod reads must not list the whole namespace")
			assert.Equal(t, fmt.Sprintf("%s=%s", constants.RoleInstanceNameLabelKey, instance.Name), options.LabelSelector.String())
			if options.FieldSelector != nil {
				assert.True(t, options.FieldSelector.Empty(), "the API server does not support the owner UID cache index")
			}
			if err := c.List(ctx, list, opts...); err != nil {
				return err
			}
			result, ok := list.(*corev1.PodList)
			require.True(t, ok)
			assert.Len(t, result.Items, 3, "only same-namespace Pods with this instance label may be returned")
			return nil
		},
	})
	accepted, err := control.shouldRecreateInstanceGuarded(context.Background(), instance, pods, nil, "revision-1", "revision-1")
	require.NoError(t, err)
	require.True(t, accepted)
	assert.Equal(t, 1, lists)
	fresh := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), fresh))
	assert.ElementsMatch(t, []workloadsv1alpha2.RoleInstancePodReference{
		{Name: pods[0].Name, UID: pods[0].UID},
		{Name: pods[1].Name, UID: pods[1].UID},
	}, fresh.Status.RetiringPods, "a matching label alone must not authorize retiring a foreign Pod")
}

func TestRestartEventNamesTriggerBeforeBaselinesAreCleared(t *testing.T) {
	instance, pods := restartScaleFixture()
	pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1, ImageID: "new-image"}}
	instance.Status.InPlaceUpdateContainerBaselines = map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
		pods[0].Name: {"main": {RestartCount: 0, ImageID: "old-image"}},
	}
	pods[1].Status.Phase = corev1.PodFailed
	_, control := restartScaleClient(t, instance, pods, interceptor.Funcs{})
	accepted, err := control.shouldRecreateInstanceGuarded(context.Background(), instance, pods, instance.Status.InPlaceUpdateContainerBaselines, "revision-1", "revision-1")
	require.NoError(t, err)
	require.True(t, accepted)
	assert.Empty(t, instance.Status.InPlaceUpdateContainerBaselines)
	select {
	case event := <-control.recorder.(*record.FakeRecorder).Events:
		assert.Contains(t, event, "Normal ReCreateInstance")
		assert.Contains(t, event, fmt.Sprintf("trigger: member %s is Failed", pods[1].Name))
		assert.NotContains(t, event, fmt.Sprintf("trigger: member %s", pods[0].Name))
	default:
		t.Fatal("accepted restart must report its actual live trigger")
	}
}

func TestGenerationAcknowledgementPreservesRemainingRestartBackoff(t *testing.T) {
	instance, pods := restartScaleFixture()
	pods[0].Status.Phase = corev1.PodFailed
	instance.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To(int32(60))
	instance.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To(int32(60))
	instance.Status.RestartCount = 1
	instance.Status.LastRestartTime = ptr.To(metav1.NewTime(time.Now().Add(-10 * time.Second)))
	instance.Generation++ // metadata/spec edit without a Pod-template revision change
	control := &realControl{}
	assert.Equal(t, restartRequeue, control.checkRestartBackoff(instance, instance.DeepCopy(), pods, nil, "revision-1", "revision-1"))
	instance.Status.ObservedGeneration = instance.Generation
	remaining := control.checkRestartBackoff(instance, instance.DeepCopy(), pods, nil, "revision-1", "revision-1")
	assert.Greater(t, remaining, 45*time.Second, "generation acknowledgement is not a backoff bypass")
	assert.LessOrEqual(t, remaining, 50*time.Second)
}

func TestExplicitLossDuringInPlaceUpdateKeepsLegacySemantics(t *testing.T) {
	for _, signal := range []string{"Failed", "deleting", "restart"} {
		for _, policy := range []workloadsv1alpha2.RestartPolicyType{workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation, workloadsv1alpha2.RecreateRoleInstanceOnPodRestart} {
			t.Run(fmt.Sprintf("%s/%s", policy, signal), func(t *testing.T) {
				instance, pods := restartScaleFixture()
				instance.Spec.RestartPolicyConfig.Type = policy
				instance.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue}}
				pods[0].Status.Conditions = []corev1.PodCondition{{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse}}
				switch signal {
				case "Failed":
					pods[0].Status.Phase = corev1.PodFailed
				case "deleting":
					pods[0].DeletionTimestamp = ptr.To(metav1.Now())
				case "restart":
					pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}
				}
				want := policy == workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation && signal != "restart"
				assert.Equal(t, want, shouldRecreateInstance(instance, pods, nil))
				if signal == "deleting" && policy == workloadsv1alpha2.RecreateRoleInstanceOnPodRestart {
					pods[0].Status.Conditions = nil
					assert.False(t, shouldRecreateInstance(instance, pods, nil), "legacy never treats deletion alone as a trigger")
				}
			})
		}
	}
}

func TestRestartAcceptanceReadsPodsWithoutInformerCache(t *testing.T) {
	instance, cachedPods := restartScaleFixture()
	cachedPods[0].Status.Phase = corev1.PodFailed
	livePods := []*corev1.Pod{cachedPods[0].DeepCopy(), cachedPods[1].DeepCopy()}
	livePods[0].Status.Phase = corev1.PodRunning
	c, control := restartScaleClient(t, instance, livePods, interceptor.Funcs{})
	control.Client = interceptor.NewClient(c, interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			t.Fatal("restart acceptance must use the API reader, not the informer client")
			return nil
		},
	})
	accepted, err := control.shouldRecreateInstanceGuarded(context.Background(), instance, cachedPods, nil, "revision-1", "revision-1")
	require.NoError(t, err)
	assert.False(t, accepted)
	select {
	case event := <-control.recorder.(*record.FakeRecorder).Events:
		assert.Contains(t, event, "Warning RestartSignalDropped")
	default:
		t.Fatal("a disappearing cached trigger must not be silently downgraded")
	}
}

func TestRestartAcceptanceRejectsStaleContainerRestart(t *testing.T) {
	instance, cachedPods := restartScaleFixture()
	cachedPods[0].Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}
	livePods := []*corev1.Pod{cachedPods[0].DeepCopy(), cachedPods[1].DeepCopy()}
	livePods[0].Status.ContainerStatuses[0].RestartCount = 0
	c, control := restartScaleClient(t, instance, livePods, interceptor.Funcs{})
	accepted, err := control.shouldRecreateInstanceGuarded(context.Background(), instance, cachedPods, nil, "revision-1", "revision-1")
	require.NoError(t, err)
	assert.False(t, accepted)
	fresh := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), fresh))
	assert.Zero(t, fresh.Status.RestartCount)
	assert.Nil(t, fresh.Status.LastRestartTime)
	assert.Empty(t, fresh.Status.RetiringPods)
}

func TestRestartAcceptanceFiltersNonControllerReferences(t *testing.T) {
	instance, pods := restartScaleFixture()
	pods[0].Status.Phase = corev1.PodFailed
	auxiliary := pods[1].DeepCopy()
	auxiliary.Name = "auxiliary-pod"
	auxiliary.UID = "auxiliary-uid"
	auxiliary.OwnerReferences[0].Controller = ptr.To(false)
	allPods := append(pods, auxiliary)
	c, control := restartScaleClient(t, instance, allPods, interceptor.Funcs{})
	accepted, err := control.shouldRecreateInstanceGuarded(context.Background(), instance, allPods, nil, "revision-1", "revision-1")
	require.NoError(t, err)
	require.True(t, accepted, "an auxiliary owner reference cannot veto a genuine member failure")
	fresh := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), fresh))
	assert.Len(t, fresh.Status.RetiringPods, 2)
	assert.NotContains(t, fresh.Status.RetiringPods, workloadsv1alpha2.RoleInstancePodReference{Name: auxiliary.Name, UID: auxiliary.UID})
	_, err = control.deletePods(context.Background(), instance, allPods)
	require.NoError(t, err)
	current := &corev1.Pod{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(auxiliary), current))
	assert.Equal(t, auxiliary.UID, current.UID, "non-controller-owned Pods must not be deleted")
}

func TestRestartAcceptancePodReadErrorDoesNotCharge(t *testing.T) {
	instance, pods := restartScaleFixture()
	pods[0].Status.Phase = corev1.PodFailed
	c, control := restartScaleClient(t, instance, pods, interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return fmt.Errorf("Pod API read failed")
		},
	})
	accepted, err := control.shouldRecreateInstanceGuarded(context.Background(), instance, pods, nil, "revision-1", "revision-1")
	require.ErrorContains(t, err, "Pod API read failed")
	assert.False(t, accepted)
	fresh := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), fresh))
	assert.Zero(t, fresh.Status.RestartCount)
	assert.Nil(t, fresh.Status.LastRestartTime)
	assert.Empty(t, fresh.Status.RetiringPods)
	for _, pod := range pods {
		current := &corev1.Pod{}
		require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pod), current))
		assert.Equal(t, pod.UID, current.UID)
	}
}

func TestRestartResumeDoesNotRequireNewPodEvidence(t *testing.T) {
	instance, pods := restartScaleFixture()
	instance.Status.RestartCount = 1
	instance.Status.RetiringPods = []workloadsv1alpha2.RoleInstancePodReference{{Name: pods[0].Name, UID: pods[0].UID}}
	_, control := restartScaleClient(t, instance, pods, interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			t.Fatal("an already accepted deletion list must not revalidate its trigger")
			return nil
		},
	})
	accepted, err := control.shouldRecreateInstanceGuarded(context.Background(), instance, nil, nil, "revision-1", "revision-1")
	require.NoError(t, err)
	assert.True(t, accepted)
	assert.Equal(t, int32(1), instance.Status.RestartCount)
}

// TestNeedsFreshRestartStatus guards the condition that decides whether a
// reconcile pays for an uncached RoleInstance read.
func TestNeedsFreshRestartStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   workloadsv1alpha2.RestartPolicyType
		retiring []workloadsv1alpha2.RoleInstancePodReference
		want     bool
	}{
		{name: "policy None, nothing to drain", policy: workloadsv1alpha2.RestartPolicyNone},
		{name: "policy unset, nothing to drain"},
		{
			name:   "policy None but a deletion list is still recorded",
			policy: workloadsv1alpha2.RestartPolicyNone,
			retiring: []workloadsv1alpha2.RoleInstancePodReference{
				{Name: "member-0", UID: "uid-0"},
			},
		},
		{name: "restart policy, nothing to drain", policy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation, want: true},
		{
			name:   "restart policy while draining",
			policy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation,
			retiring: []workloadsv1alpha2.RoleInstancePodReference{
				{Name: "member-0", UID: "uid-0"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance := &workloadsv1alpha2.RoleInstance{
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: tc.policy},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{RetiringPods: tc.retiring},
			}
			assert.Equal(t, tc.want, needsFreshRestartStatus(instance))
		})
	}
}

// scaleWithRoleInstanceGets runs one Scale pass and returns the number of uncached
// RoleInstance reads it issued.
func scaleWithRoleInstanceGets(t *testing.T, policy workloadsv1alpha2.RestartPolicyType, failRead bool) (int, error) {
	t.Helper()
	ctx := context.Background()
	instance, pods := restartScaleFixture()
	instance.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{Type: policy}
	var (
		riGets atomic.Int32
		fail   atomic.Bool
	)
	c, ctrl := restartScaleClient(t, instance, pods, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*workloadsv1alpha2.RoleInstance); ok {
				riGets.Add(1)
				if fail.Load() {
					return fmt.Errorf("RoleInstance API read failed")
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	// Load the object and the Pods the way the reconciler does, then count only
	// the reads performed inside Scale.
	loaded := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), loaded))
	pods_, inactivePods, err := instanceutil.GetActiveAndInactivePods(ctx, c, &client.ListOptions{Namespace: instance.Namespace})
	require.NoError(t, err)
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}
	fail.Store(failRead)
	riGets.Store(0)
	_, _, err = ctrl.Scale(ctx, loaded, revision, revision, nil, pods_, inactivePods)
	return int(riGets.Load()), err
}

// TestScaleSkipsFreshReadWhenUnnecessary pins cheyang's review note: the
// uncached RoleInstance read is only needed for RecreateRoleInstanceOnPodRestart
// or while a deletion list is recorded.
func TestScaleSkipsFreshReadWhenUnnecessary(t *testing.T) {
	gets, err := scaleWithRoleInstanceGets(t, workloadsv1alpha2.RestartPolicyNone, false)
	require.NoError(t, err)
	assert.Zero(t, gets, "restartPolicy=None must not pay for an uncached RoleInstance read")

	gets, err = scaleWithRoleInstanceGets(t, workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation, false)
	require.NoError(t, err)
	assert.Equal(t, 1, gets, "the recreate policy needs the authoritative restart bookkeeping")
}

// TestScaleUncachedErrorDoesNotBlockNonePolicy pins the second half of the same
// review note: for restartPolicy=None a failed read cannot block ordinary
// scaling, because no read is issued at all.
func TestScaleUncachedErrorDoesNotBlockNonePolicy(t *testing.T) {
	_, err := scaleWithRoleInstanceGets(t, workloadsv1alpha2.RestartPolicyNone, true)
	require.NoError(t, err, "a read that is not needed must not fail the scale pass")
}

// TestDrainQueriesAPIOnlyWhenItWouldComplete pins the read budget of a drain: while
// the recorded members are still visible, the informer view is enough. The API is
// only consulted once the cache reports every old UID gone, because that answer is
// what releases creation.
func TestHeldRetirementHasNoReadsOrWritesAndWarnsOnlyWhenStalled(t *testing.T) {
	for _, budget := range []bool{false, true} {
		for _, stalled := range []bool{false, true} {
			t.Run(fmt.Sprintf("budget=%t/stalled=%t", budget, stalled), func(t *testing.T) {
				ctx := context.Background()
				instance, pods := restartScaleFixture()
				*instance.Spec.Components[0].Size = 8
				instance.Status.RestartCount = 1
				for i := 0; i < 8; i++ {
					instance.Status.RetiringPods = append(instance.Status.RetiringPods, workloadsv1alpha2.RoleInstancePodReference{
						Name: fmt.Sprintf("%s-worker-%d", instance.Name, i), UID: types.UID(fmt.Sprintf("old-%d", i)),
					})
				}
				if budget {
					instance.Spec.RestartPolicyConfig.MaxGroupRestarts = ptr.To(int32(1))
					instance.Status.GroupRestartCount = 1
					instance.Status.GroupRestartRevision = groupRestartRevision(instance)
					instance.Finalizers = []string{constants.GroupRestartBudgetCleanupFinalizer}
				}
				age := time.Minute
				if stalled {
					age = 6 * time.Minute
				}
				pods[0].DeletionTimestamp = ptr.To(metav1.NewTime(time.Now().Add(-age)))
				pods[0].Finalizers = []string{"test.example/hold"}
				gets, writes := 0, 0
				c, control := restartScaleClient(t, instance, pods[:1], interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						gets++
						return c.Get(ctx, key, obj, opts...)
					},
					SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						writes++
						return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
					},
					SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						writes++
						return c.SubResource(sub).Update(ctx, obj, opts...)
					},
				})
				recorder := record.NewFakeRecorder(10)
				control.recorder = recorder
				revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}
				for i := 0; i < 3; i++ {
					_, requeue, err := control.Scale(ctx, instance, revision, revision, nil, pods[:1], nil)
					require.NoError(t, err)
					assert.Equal(t, drainSafetyRequeue, requeue)
					assert.Len(t, instance.Status.RetiringPods, 8)
					select {
					case event := <-recorder.Events:
						require.True(t, stalled, "no warning before the threshold: %s", event)
						assert.Contains(t, event, "Warning RetirementStalled")
						assert.Contains(t, event, pods[0].Name)
					default:
						require.False(t, stalled, "old terminating member must emit a warning")
					}
				}
				assert.Zero(t, gets, "including the budget path, held drains use only the cached UID list")
				assert.Zero(t, writes, "neither warnings nor polling may rewrite status")
				// Remove the finalizer normally; only confirmed UID absence releases
				// the barrier. No force-delete or timeout-ledger clearing is allowed.
				held := &corev1.Pod{}
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[0]), held))
				held.Finalizers = nil
				require.NoError(t, c.Update(ctx, held))
				gets, writes = 0, 0
				_, _, err := control.Scale(ctx, instance, revision, revision, nil, nil, nil)
				require.NoError(t, err)
				assert.Equal(t, 9, gets, "one live RI and eight saved UID confirmations")
				assert.Equal(t, 1, writes, "only confirmed completion clears the ledger")
				assert.Empty(t, instance.Status.RetiringPods)
			})
		}
	}
}

func TestDrainQueriesAPIOnlyWhenItWouldComplete(t *testing.T) {
	ctx := context.Background()
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}

	// scaleOnce loads like the reconciler and counts the reads Scale performs.
	scaleOnce := func(t *testing.T, c client.WithWatch, control *realControl, instance *workloadsv1alpha2.RoleInstance,
		podGets, riGets *atomic.Int32) {
		t.Helper()
		loaded := &workloadsv1alpha2.RoleInstance{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), loaded))
		active, inactive, err := instanceutil.GetActiveAndInactivePods(ctx, c, &client.ListOptions{Namespace: instance.Namespace})
		require.NoError(t, err)
		podGets.Store(0)
		riGets.Store(0)
		_, _, err = control.Scale(ctx, loaded, revision, revision, nil, active, inactive)
		require.NoError(t, err)
	}
	countReads := func(podGets, riGets *atomic.Int32) interceptor.Funcs {
		return interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				switch obj.(type) {
				case *corev1.Pod:
					podGets.Add(1)
				case *workloadsv1alpha2.RoleInstance:
					riGets.Add(1)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}
	}

	t.Run("a member still visible is deleted without any uncached read", func(t *testing.T) {
		instance, pods := restartScaleFixture()
		old := metav1.NewTime(time.Now().Add(-time.Minute))
		instance.Status.RestartCount = 1
		instance.Status.LastRestartTime = &old
		instance.Status.RetiringPods = []workloadsv1alpha2.RoleInstancePodReference{{Name: pods[0].Name, UID: pods[0].UID}}
		pods[0].Finalizers = []string{"test.example/hold"}
		var podGets, riGets atomic.Int32
		c, control := restartScaleClient(t, instance, pods, countReads(&podGets, &riGets))

		scaleOnce(t, c, control, instance, &podGets, &riGets)

		assert.Zero(t, podGets.Load(), "a cached member must not cost an uncached Pod read")
		assert.Zero(t, riGets.Load(), "an active drain needs no fresh RI read")
		persisted := &workloadsv1alpha2.RoleInstance{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), persisted))
		assert.Len(t, persisted.Status.RetiringPods, 1, "the drain is not finished while the member exists")
	})

	t.Run("completion is confirmed against the API", func(t *testing.T) {
		instance, pods := restartScaleFixture()
		old := metav1.NewTime(time.Now().Add(-time.Minute))
		instance.Status.RestartCount = 1
		instance.Status.LastRestartTime = &old
		instance.Status.RetiringPods = []workloadsv1alpha2.RoleInstancePodReference{{Name: pods[0].Name, UID: pods[0].UID}}
		// The recorded member is gone from the API and from the informer.
		var podGets, riGets atomic.Int32
		c, control := restartScaleClient(t, instance, pods[1:], countReads(&podGets, &riGets))

		scaleOnce(t, c, control, instance, &podGets, &riGets)

		assert.Equal(t, int32(1), podGets.Load(), "the completed UID is confirmed by a Pod read")
		assert.Equal(t, int32(1), riGets.Load(), "only the drain confirmation reads the RI")
		persisted := &workloadsv1alpha2.RoleInstance{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), persisted))
		assert.Empty(t, persisted.Status.RetiringPods, "the list is cleared once the old UID is gone")
	})
}

func TestVanishBeforeConfirmationIsReported(t *testing.T) {
	instance, pods := restartScaleFixture()
	terminating := pods[0].DeepCopy()
	terminating.DeletionTimestamp = ptr.To(metav1.Now())
	healthy := pods[1].DeepCopy()
	c, control := restartScaleClient(t, instance, []*corev1.Pod{healthy}, interceptor.Funcs{})
	recorder := record.NewFakeRecorder(10)
	control.recorder = recorder
	accepted, err := control.shouldRecreateInstanceGuarded(context.Background(), instance,
		[]*corev1.Pod{terminating, healthy}, nil, "revision-1", "revision-1")
	require.NoError(t, err)
	assert.False(t, accepted, "a vanished deletion trigger must not charge a group rebuild")
	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "RestartSignalDropped")
	case <-time.After(time.Second):
		t.Fatal("no event reported for a dropped deletion signal")
	}
	fresh := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), fresh))
	assert.Zero(t, fresh.Status.RestartCount)
	assert.Zero(t, fresh.Status.GroupRestartCount)
	assert.Empty(t, fresh.Status.RetiringPods)
}

func TestReCreateInstanceEventNamesTheTrigger(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*corev1.Pod)
		want   string
	}{
		{name: "failed member", mutate: func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed }, want: "is Failed"},
		{name: "deleting member", mutate: func(p *corev1.Pod) {
			p.DeletionTimestamp = ptr.To(metav1.Now())
			p.Finalizers = []string{"test.example/hold"}
		}, want: "is being deleted"},
		{name: "restarting member", mutate: func(p *corev1.Pod) {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}
		}, want: "restarted unexpectedly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance, pods := restartScaleFixture()
			tc.mutate(pods[0])
			_, control := restartScaleClient(t, instance, pods, interceptor.Funcs{})
			recorder := record.NewFakeRecorder(10)
			control.recorder = recorder
			accepted, err := control.shouldRecreateInstanceGuarded(context.Background(), instance, pods, nil, "revision-1", "revision-1")
			require.NoError(t, err)
			require.True(t, accepted)
			var events []string
		drain:
			for {
				select {
				case event := <-recorder.Events:
					events = append(events, event)
				default:
					break drain
				}
			}
			joined := strings.Join(events, "\n")
			assert.Contains(t, joined, "ReCreateInstance")
			assert.Contains(t, joined, pods[0].Name)
			assert.Contains(t, joined, tc.want)
		})
	}
}

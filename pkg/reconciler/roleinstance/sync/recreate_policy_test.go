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

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func newRecreateTestInstance(activateAfter workloadsv1alpha2.RestartPolicyActivateAfter) *workloadsv1alpha2.RoleInstance {
	return &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
				Type:          workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
				ActivateAfter: activateAfter,
			},
			Components: []workloadsv1alpha2.RoleInstanceComponent{{Name: "worker", Size: ptr.To[int32](1)}},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{
			ObservedGeneration: 1,
			CurrentRevision:    "r1",
			UpdateRevision:     "r1",
		},
	}
}

func newRecreateTestPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-worker-0",
			Labels: map[string]string{constants.ComponentNameLabelKey: "worker", constants.ComponentIDLabelKey: "0"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodFailed},
	}
}

func markReady(instance *workloadsv1alpha2.RoleInstance) {
	instance.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
		{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue},
	}
}

func TestTerminatingScaleInPodDoesNotTriggerRecreation(t *testing.T) {
	for _, removedSlot := range []string{"ordinal", "component"} {
		for _, phase := range []corev1.PodPhase{corev1.PodRunning, corev1.PodFailed, corev1.PodSucceeded} {
			t.Run(removedSlot+"/"+string(phase), func(t *testing.T) {
				instance := newRecreateTestInstance(workloadsv1alpha2.RestartPolicyActivateAfterReady)
				instance.Status.LastReadyGeneration = instance.Generation
				extra := newRecreateTestPod()
				extra.Name = "removed-member"
				if removedSlot == "ordinal" {
					extra.Labels[constants.ComponentIDLabelKey] = "1"
				} else {
					extra.Labels[constants.ComponentNameLabelKey] = "removed"
				}
				extra.DeletionTimestamp = ptr.To(metav1.Now())
				extra.Status.Phase = phase
				extra.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}
				require.False(t, shouldRecreateInstance(instance, []*corev1.Pod{extra}, nil),
					"a non-desired terminating member must not fall through to Failed or restart triggers")
				reason, name := recreateTriggerReason(instance, []*corev1.Pod{extra}, nil)
				require.Equal(t, "Unknown", reason)
				require.Empty(t, name)

				// A real sibling fault must still trigger and be named in the event.
				member := newRecreateTestPod()
				member.Status.Phase = corev1.PodRunning
				member.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}
				pods := []*corev1.Pod{extra, member}
				require.True(t, shouldRecreateInstance(instance, pods, nil))
				reason, name = recreateTriggerReason(instance, pods, nil)
				require.Equal(t, "ContainerRestarted", reason)
				require.Equal(t, member.Name, name)
			})
		}
	}
}

// TestShouldRecreateInstanceTriggers covers the trigger set of the recreate policy and
// the arming rules of restartPolicyConfig.activateAfter.
func TestShouldRecreateInstanceTriggers(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*workloadsv1alpha2.RoleInstance, []*corev1.Pod) []*corev1.Pod
		want   bool
	}{
		{
			name: "default Ready arming: fault before first Ready does not fire",
		},
		{
			name: "default Ready arming: fires once Ready at this generation",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				markReady(i)
				return pods
			},
			want: true,
		},
		{
			name: "default Ready arming: lastReadyGeneration keeps triggers armed after Ready flips",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Status.LastReadyGeneration = i.Generation
				return pods
			},
			want: true,
		},
		{
			name: "lastReadyGeneration from an older generation does not arm",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Status.LastReadyGeneration = i.Generation - 1
				return pods
			},
		},
		{
			name: "activateAfter=Created fires before the first Ready",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				return pods
			},
			want: true,
		},
		{
			name: "activateAfter=Created still honours the generation guard",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				i.Generation++
				return pods
			},
		},
		{
			name: "observed deletion is member loss and fires before Ready",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				pods[0].Status.Phase = corev1.PodRunning
				pods[0].DeletionTimestamp = ptr.To(metav1.Now())
				return pods
			},
			want: true,
		},
		{
			name: "Terminating member beyond the component size is scale-in, not member loss",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				pods[0].Status.Phase = corev1.PodRunning
				pods[0].Labels[constants.ComponentIDLabelKey] = "1"
				pods[0].DeletionTimestamp = ptr.To(metav1.Now())
				return pods
			},
		},
		{
			name: "Terminating member of a removed component is scale-in, not member loss",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				pods[0].Status.Phase = corev1.PodRunning
				pods[0].Labels[constants.ComponentNameLabelKey] = "removed"
				pods[0].DeletionTimestamp = ptr.To(metav1.Now())
				return pods
			},
		},
		{
			name: "Terminating Ignore member is not member loss",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				pods[0].Status.Phase = corev1.PodRunning
				pods[0].Annotations = map[string]string{constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore}
				pods[0].DeletionTimestamp = ptr.To(metav1.Now())
				return pods
			},
		},
		{
			name: "ordinary container crash fires under Created",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				pods[0].Status.Phase = corev1.PodRunning
				pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}
				return pods
			},
			want: true,
		},
		{
			name: "init restart alone is not a trigger",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				pods[0].Status.Phase = corev1.PodRunning
				pods[0].Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", RestartCount: 1}}
				return pods
			},
		},
		{
			name: "Failed while in-place updating still triggers",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				pods[0].Status.Conditions = []corev1.PodCondition{{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse}}
				return pods
			},
			want: true,
		},
		{
			name: "in-place restart alone remains exempt",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				pods[0].Status.Phase = corev1.PodRunning
				pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}
				pods[0].Status.Conditions = []corev1.PodCondition{{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse}}
				return pods
			},
		},
		{
			name: "normal completion is not a trigger",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				pods[0].Status.Phase = corev1.PodSucceeded
				return pods
			},
		},
		{
			name: "Ignore exempts the trigger",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				pods[0].Annotations = map[string]string{constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore}
				return pods
			},
		},
		{
			name: "scale down is not member loss",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				markReady(i)
				i.Spec.Components[0].Size = ptr.To[int32](0)
				pods[0].Status.Phase = corev1.PodRunning
				return pods
			},
		},
		{
			name: "rolling update is not a trigger",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
				i.Status.UpdateRevision = "new"
				return pods
			},
		},
		{
			name: "restart policy None never fires",
			mutate: func(i *workloadsv1alpha2.RoleInstance, pods []*corev1.Pod) []*corev1.Pod {
				i.Spec.RestartPolicyConfig.Type = workloadsv1alpha2.RestartPolicyNone
				return pods
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instance := newRecreateTestInstance(workloadsv1alpha2.RestartPolicyActivateAfterReady)
			pods := []*corev1.Pod{newRecreateTestPod()}
			if tt.mutate != nil {
				pods = tt.mutate(instance, pods)
			}
			require.Equal(t, tt.want, shouldRecreateInstance(instance, pods, nil))
		})
	}
}

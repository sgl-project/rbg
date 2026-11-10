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

func TestRecreateRoleInstanceOnPodRestartFromCreation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		policy workloadsv1alpha2.RestartPolicyType
		mutate func(*workloadsv1alpha2.RoleInstance, *corev1.Pod)
		want   bool
	}{
		{name: "new policy recovers before first Ready", want: true},
		{name: "legacy policy still requires Ready", policy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
		{name: "legacy policy works after Ready", policy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart, mutate: func(i *workloadsv1alpha2.RoleInstance, _ *corev1.Pod) {
			i.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue}}
		}, want: true},
		{name: "observed deletion before Ready", mutate: func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
			now := metav1.Now()
			p.DeletionTimestamp = &now
		}, want: true},
		{name: "ordinary container crash before Ready", mutate: func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}
		}, want: true},
		{name: "init restart alone is not a trigger", mutate: func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", RestartCount: 1}}
		}},
		{name: "native sidecar restart alone is not a trigger", mutate: func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
			p.Spec.InitContainers = []corev1.Container{{Name: "sidecar", RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways)}}
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "sidecar", RestartCount: 1}}
		}},
		{name: "Failed while in-place updating still triggers", mutate: func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse}}
		}, want: true},
		{name: "deletion while in-place updating still triggers", mutate: func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
			p.DeletionTimestamp = ptr.To(metav1.Now())
			p.Status.Conditions = []corev1.PodCondition{{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse}}
		}, want: true},
		{name: "in-place restart alone remains exempt", mutate: func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}
			p.Status.Conditions = []corev1.PodCondition{{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionFalse}}
		}},
		{name: "normal completion is not a trigger", mutate: func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }},
		{name: "Ignore exempts trigger", mutate: func(_ *workloadsv1alpha2.RoleInstance, p *corev1.Pod) {
			p.Annotations = map[string]string{constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore}
		}},
		{name: "scale down is not member loss", mutate: func(i *workloadsv1alpha2.RoleInstance, _ *corev1.Pod) { i.Spec.Components[0].Size = ptr.To[int32](0) }},
		{name: "unobserved generation is not a trigger", mutate: func(i *workloadsv1alpha2.RoleInstance, _ *corev1.Pod) { i.Generation++ }},
		{name: "rolling update is not a trigger", mutate: func(i *workloadsv1alpha2.RoleInstance, _ *corev1.Pod) { i.Status.UpdateRevision = "new" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			policy := tt.policy
			if policy == "" {
				policy = workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation
			}
			instance := &workloadsv1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{Generation: 1}, Spec: workloadsv1alpha2.RoleInstanceSpec{RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: policy}, Components: []workloadsv1alpha2.RoleInstanceComponent{{Name: "worker", Size: ptr.To[int32](1)}}}, Status: workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 1}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-worker-0", Labels: map[string]string{constants.ComponentNameLabelKey: "worker", constants.ComponentIDLabelKey: "0"}}, Status: corev1.PodStatus{Phase: corev1.PodFailed}}
			if tt.mutate != nil {
				tt.mutate(instance, pod)
			}
			require.Equal(t, tt.want, shouldRecreateInstance(instance, []*corev1.Pod{pod}, nil))
		})
	}
}

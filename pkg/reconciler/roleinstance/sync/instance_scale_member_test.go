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
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

// memberTestInstance returns an instance that was Ready at its current generation
// and has since become NotReady, which is the state a member fault leaves behind.
func memberTestInstance(name string) *workloadsv1alpha2.RoleInstance {
	return &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			UID:        "member-test-uid",
			Generation: 2,
			Labels: map[string]string{
				constants.RoleTypeLabelKey: string(constants.ComponentsTemplateType),
			},
		},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
			Components: []workloadsv1alpha2.RoleInstanceComponent{
				{Name: "leader", Size: ptr.To[int32](1)},
				{Name: "worker", Size: ptr.To[int32](2)},
			},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{
			ObservedGeneration:  2,
			CurrentRevision:     "rev-1",
			UpdateRevision:      "rev-1",
			LastReadyGeneration: 2,
			Conditions: []workloadsv1alpha2.RoleInstanceCondition{
				{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionFalse},
			},
		},
	}
}

func memberTestPod(instance *workloadsv1alpha2.RoleInstance, component string, id int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceutil.FormatComponentPodName(instance.Name, component, id, instance.GetRoleTemplateType()),
			Namespace: instance.Namespace,
			Labels:    instanceutil.InitComponentPodLabels(instance.Name, component, id, instance.GetRoleTemplateType()),
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// memberTestPods returns one running Pod per member slot, skipping the named ones.
func memberTestPods(instance *workloadsv1alpha2.RoleInstance, skip ...string) []*corev1.Pod {
	var pods []*corev1.Pod
	for _, component := range instance.Spec.Components {
		for id := int32(0); id < *component.Size; id++ {
			pod := memberTestPod(instance, component.Name, id)
			if !slices.Contains(skip, pod.Name) {
				pods = append(pods, pod)
			}
		}
	}
	return pods
}

func setReady(instance *workloadsv1alpha2.RoleInstance, status corev1.ConditionStatus) {
	instance.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
		{Type: workloadsv1alpha2.RoleInstanceReady, Status: status},
	}
}

func markTerminating(pod *corev1.Pod) *corev1.Pod {
	now := metav1.Now()
	pod.DeletionTimestamp = &now
	return pod
}

func TestRecreateTriggerLostMember(t *testing.T) {
	const name = "group-0"
	worker1 := name + "-worker-1"
	withPod := func(mutate func(*corev1.Pod), podName string) func(*workloadsv1alpha2.RoleInstance) []*corev1.Pod {
		return func(instance *workloadsv1alpha2.RoleInstance) []*corev1.Pod {
			pods := memberTestPods(instance)
			for _, pod := range pods {
				if pod.Name == podName {
					mutate(pod)
				}
			}
			return pods
		}
	}
	withoutWorker1 := func(instance *workloadsv1alpha2.RoleInstance) []*corev1.Pod {
		return memberTestPods(instance, worker1)
	}
	terminatingWorker1 := withPod(func(pod *corev1.Pod) { markTerminating(pod) }, worker1)
	notLatchedButReady := func(instance *workloadsv1alpha2.RoleInstance) {
		instance.Status.LastReadyGeneration = 0
		setReady(instance, corev1.ConditionTrue)
	}

	tests := []struct {
		name       string
		mutate     func(*workloadsv1alpha2.RoleInstance)
		pods       func(*workloadsv1alpha2.RoleInstance) []*corev1.Pod
		want       bool
		wantReason string
	}{
		{
			name: "all members present",
			pods: func(instance *workloadsv1alpha2.RoleInstance) []*corev1.Pod { return memberTestPods(instance) },
		},
		{
			name:       "member being deleted after the instance went NotReady",
			pods:       terminatingWorker1,
			want:       true,
			wantReason: "member pod " + worker1 + " is being deleted",
		},
		{
			name:       "member being deleted while Ready, before the readiness latch exists",
			mutate:     notLatchedButReady,
			pods:       terminatingWorker1,
			want:       true,
			wantReason: "member pod " + worker1 + " is being deleted",
		},
		{
			name:       "member missing after the instance was Ready at this generation",
			pods:       withoutWorker1,
			want:       true,
			wantReason: "member pod " + worker1 + " is missing",
		},
		{
			name:   "missing member is not evidence of loss without the readiness latch",
			mutate: notLatchedButReady,
			pods:   withoutWorker1,
		},
		{
			name: "missing member is ignored with readyPolicy None",
			mutate: func(instance *workloadsv1alpha2.RoleInstance) {
				instance.Spec.ReadyPolicy = workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone
			},
			pods: withoutWorker1,
		},
		{
			name: "missing member during initial creation",
			mutate: func(instance *workloadsv1alpha2.RoleInstance) {
				instance.Status.LastReadyGeneration = 0
			},
			pods: withoutWorker1,
		},
		{
			name: "missing member after a spec change that has not been Ready yet",
			mutate: func(instance *workloadsv1alpha2.RoleInstance) {
				instance.Status.LastReadyGeneration = 1
			},
			pods: withoutWorker1,
		},
		{
			name: "missing member before the new generation is observed",
			mutate: func(instance *workloadsv1alpha2.RoleInstance) {
				instance.Generation = 3
			},
			pods: withoutWorker1,
		},
		{
			name: "member being deleted during a rolling update",
			mutate: func(instance *workloadsv1alpha2.RoleInstance) {
				instance.Status.UpdateRevision = "rev-2"
			},
			pods: terminatingWorker1,
		},
		{
			name: "scale-in leftover being deleted is not a member",
			pods: func(instance *workloadsv1alpha2.RoleInstance) []*corev1.Pod {
				return append(memberTestPods(instance), markTerminating(memberTestPod(instance, "worker", 2)))
			},
		},
		{
			name: "terminating member that opts out with the Ignore annotation",
			pods: withPod(func(pod *corev1.Pod) {
				markTerminating(pod)
				pod.Annotations = map[string]string{
					constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
				}
			}, worker1),
		},
		{
			name: "missing member of a component whose template opts out",
			mutate: func(instance *workloadsv1alpha2.RoleInstance) {
				instance.Spec.Components[1].Template.Annotations = map[string]string{
					constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
				}
			},
			pods: withoutWorker1,
		},
		{
			name: "missing member of a component that opts out with a component annotation",
			mutate: func(instance *workloadsv1alpha2.RoleInstance) {
				instance.Spec.Components[1].Annotations = map[string]string{
					constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
				}
			},
			pods: withoutWorker1,
		},
		{
			name: "component annotation takes precedence over the template annotation",
			mutate: func(instance *workloadsv1alpha2.RoleInstance) {
				instance.Spec.Components[1].Template.Annotations = map[string]string{
					constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
				}
				instance.Spec.Components[1].Annotations = map[string]string{
					constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyInherit,
				}
			},
			pods:       withoutWorker1,
			want:       true,
			wantReason: "member pod " + worker1 + " is missing",
		},
		{
			name: "restart policy None never recreates",
			mutate: func(instance *workloadsv1alpha2.RoleInstance) {
				instance.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RestartPolicyNone}
			},
			pods: terminatingWorker1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := memberTestInstance(name)
			if tt.mutate != nil {
				tt.mutate(instance)
			}
			reason, got := recreateTrigger(instance, tt.pods(instance), nil)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantReason, reason)
			assert.Equal(t, tt.want, shouldRecreateInstance(instance, tt.pods(instance), nil))
		})
	}
}

// TestRecreateTriggerAfterReadyAtCurrentGeneration covers crash signals that arrive
// after the fault already made the instance NotReady.
func TestRecreateTriggerAfterReadyAtCurrentGeneration(t *testing.T) {
	const name = "group-1"
	failedWorker := func(instance *workloadsv1alpha2.RoleInstance) []*corev1.Pod {
		pods := memberTestPods(instance)
		pods[1].Status.Phase = corev1.PodFailed
		return pods
	}
	restartedLeader := func(instance *workloadsv1alpha2.RoleInstance) []*corev1.Pod {
		pods := memberTestPods(instance)
		pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", RestartCount: 1}}
		return pods
	}

	tests := []struct {
		name       string
		mutate     func(*workloadsv1alpha2.RoleInstance)
		pods       func(*workloadsv1alpha2.RoleInstance) []*corev1.Pod
		want       bool
		wantReason string
	}{
		{
			name:       "failed pod after the instance went NotReady",
			pods:       failedWorker,
			want:       true,
			wantReason: "pod " + name + "-worker-0 failed",
		},
		{
			name:       "container restart after the instance went NotReady",
			pods:       restartedLeader,
			want:       true,
			wantReason: "a container of pod " + name + "-leader-0 restarted",
		},
		{
			name: "container restart while starting up on a new spec",
			mutate: func(instance *workloadsv1alpha2.RoleInstance) {
				instance.Generation = 3
				instance.Status.ObservedGeneration = 3
			},
			pods: restartedLeader,
		},
		{
			name: "zero-value generation and latch do not count as Ready",
			mutate: func(instance *workloadsv1alpha2.RoleInstance) {
				instance.Generation = 0
				instance.Status.ObservedGeneration = 0
				instance.Status.LastReadyGeneration = 0
			},
			pods: failedWorker,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := memberTestInstance(name)
			if tt.mutate != nil {
				tt.mutate(instance)
			}
			reason, got := recreateTrigger(instance, tt.pods(instance), nil)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantReason, reason)
		})
	}
}

func TestCalculateDiffsRecreatesInstanceOnLostMember(t *testing.T) {
	instance := memberTestInstance("group-lost-member")
	defer restartingCache.Delete(instanceKey(instance))
	pods := memberTestPods(instance, "group-lost-member-worker-1")
	recorder := record.NewFakeRecorder(10)
	ctrl := &realControl{recorder: recorder}
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "rev-1"}}

	diff, err := ctrl.calculateDiffsWithExpectation(context.Background(), instance, revision, revision, nil, pods, nil)
	require.NoError(t, err)
	assert.Equal(t, len(pods), diff.toDeleteNum, "all surviving members must be deleted")
	assert.ElementsMatch(t, pods, diff.toDeletePod)
	assert.Zero(t, diff.toScaleNum, "the missing member must not be replaced on its own")
	assert.Equal(t, int32(1), instance.Status.RestartCount)
	assert.True(t, isInstanceRestarting(instance))

	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "ReCreateInstance")
		assert.Contains(t, event, "member pod group-lost-member-worker-1 is missing")
	default:
		assert.Fail(t, "expected a ReCreateInstance event")
	}

	// On the next reconcile the leader has been re-created while the workers are
	// still missing. The restart guard must keep the instance from being recreated
	// again, so the missing workers are created normally.
	pods = memberTestPods(instance, "group-lost-member-worker-0", "group-lost-member-worker-1")
	diff, err = ctrl.calculateDiffsWithExpectation(context.Background(), instance, revision, revision, nil, pods, nil)
	require.NoError(t, err)
	assert.Zero(t, diff.toDeleteNum)
	assert.Equal(t, 2, diff.toScaleNum)
	assert.Equal(t, int32(1), instance.Status.RestartCount)
}

func TestDeletePodsHandlesTerminatingAndGonePods(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))

	instance := memberTestInstance("group-delete")
	live := memberTestPod(instance, "leader", 0)
	terminating := markTerminating(memberTestPod(instance, "worker", 0))
	gone := memberTestPod(instance, "worker", 1)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(live).Build()
	recorder := record.NewFakeRecorder(10)
	ctrl := &realControl{Client: fakeClient, recorder: recorder}

	modified, err := ctrl.deletePods(ctx, instance, []*corev1.Pod{live, terminating, gone})
	require.NoError(t, err, "a Pod that is already gone must not fail the recreation")
	assert.True(t, modified)
	assert.True(t, apierrors.IsNotFound(fakeClient.Get(ctx, client.ObjectKeyFromObject(live), &corev1.Pod{})))

	var events []string
	for len(recorder.Events) > 0 {
		events = append(events, <-recorder.Events)
	}
	require.Len(t, events, 1, "only the live Pod needs a delete call")
	assert.True(t, strings.Contains(events[0], "SuccessfulDelete"), events[0])

	modified, err = ctrl.deletePods(ctx, instance, []*corev1.Pod{terminating, gone})
	require.NoError(t, err)
	assert.True(t, modified, "a recreation whose Pods are all terminating or gone must still be persisted")
}

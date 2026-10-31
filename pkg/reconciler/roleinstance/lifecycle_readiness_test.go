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

package roleinstance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	api "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancelifecycle "sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	instancereadiness "sigs.k8s.io/rbgs/pkg/inplace/instance/readiness"
)

func TestLifecycleCustomReadinessControlsInstanceAndPods(t *testing.T) {
	for _, status := range []corev1.ConditionStatus{corev1.ConditionFalse, corev1.ConditionUnknown, "", corev1.ConditionTrue} {
		for _, policy := range []api.RoleInstanceReadyPolicyType{api.RoleInstanceReadyOnAllPodReady, api.RoleInstanceReadyPolicyTypeNone} {
			t.Run(string(status)+"/"+string(policy), func(t *testing.T) {
				instance := newReadinessTestInstance()
				instance.Spec.ReadyPolicy = policy
				instance.Spec.ReadinessGates = append(instance.Spec.ReadinessGates, api.RoleInstanceReadinessGate{ConditionType: api.RoleInstanceCustomReady})
				if status != "" {
					instance.Status.Conditions = append(instance.Status.Conditions, api.RoleInstanceCondition{Type: api.RoleInstanceCustomReady, Status: status})
				}
				pods := []*corev1.Pod{newReadinessTestPod(instance, "router"), newReadinessTestPod(instance, "sidecar")}
				objects := []client.Object{instance}
				for _, pod := range pods {
					pod.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: constants.InstancePodReadyConditionType}}
					pod.Status.Conditions = append(pod.Status.Conditions,
						corev1.PodCondition{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
						corev1.PodCondition{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionTrue},
						corev1.PodCondition{Type: constants.InstancePodReadyConditionType, Status: corev1.ConditionTrue})
					objects = append(objects, pod)
				}
				c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithObjects(objects...).WithStatusSubresource(objects...).Build()
				updater := newStatusUpdater(c).(*realStatusUpdater)
				want := corev1.ConditionFalse
				if status == corev1.ConditionTrue {
					want = corev1.ConditionTrue
				}
				for range 2 {
					ready := getInstanceReadyCondition(instance, &api.RoleInstanceCondition{Type: api.RoleInstanceAllPodsReady, Status: corev1.ConditionTrue}, &api.RoleInstanceCondition{Type: api.RoleInstanceInPlaceUpdateReady, Status: corev1.ConditionTrue})
					require.Equal(t, want, ready.Status, "Ready recalculation must honor the declared gate")
					require.NoError(t, updater.updatePodsLifeCycle(context.Background(), instance, pods))
					for i, pod := range pods {
						got := &corev1.Pod{}
						require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pod), got))
						var found bool
						for _, condition := range got.Status.Conditions {
							if condition.Type == constants.InstancePodReadyConditionType {
								found = true
								require.Equal(t, want, condition.Status)
							}
						}
						require.True(t, found)
						pods[i] = got
					}
				}
			})
		}
	}
}

func TestLifecycleReadinessReleasePreservesOtherPodBlockers(t *testing.T) {
	instance := newReadinessTestInstance()
	pod := newReadinessTestPod(instance, "router")
	pod.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: constants.InstancePodReadyConditionType}}
	pod.Status.Conditions = append(pod.Status.Conditions,
		corev1.PodCondition{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
		corev1.PodCondition{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionTrue},
		corev1.PodCondition{Type: constants.InstancePodReadyConditionType, Status: corev1.ConditionFalse, Message: `[{"userAgent":"Lifecycle","key":"InstanceReady"},{"userAgent":"Other","key":"hold"}]`})
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithObjects(instance, pod).WithStatusSubresource(instance, pod).Build()
	updater := newStatusUpdater(c).(*realStatusUpdater)
	require.NoError(t, updater.updatePodsLifeCycle(context.Background(), instance, []*corev1.Pod{pod}))
	got := &corev1.Pod{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pod), got))
	for _, condition := range got.Status.Conditions {
		if condition.Type == constants.InstancePodReadyConditionType {
			require.Equal(t, corev1.ConditionFalse, condition.Status)
			require.Contains(t, condition.Message, "Other")
			require.NotContains(t, condition.Message, "Lifecycle")
		}
	}
}

func TestLifecycleReadinessRejectsStaleStatusCalculation(t *testing.T) {
	instance := newReadinessTestInstance()
	pod := newReadinessTestPod(instance, "router")
	pod.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: constants.InstancePodReadyConditionType}}
	pod.Status.Conditions = append(pod.Status.Conditions,
		corev1.PodCondition{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
		corev1.PodCondition{Type: constants.InPlaceUpdateReady, Status: corev1.ConditionTrue},
		corev1.PodCondition{Type: constants.InstancePodReadyConditionType, Status: corev1.ConditionFalse, Message: `[{"userAgent":"Lifecycle","key":"InstanceReady"}]`})
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithObjects(instance, pod).WithStatusSubresource(instance, pod).Build()
	stale := instance.DeepCopy()
	_, _, err := instancelifecycle.New(c).UpdateRoleInstanceLifecycle(instance, constants.RoleInstanceLifecycleStatePreparingDelete, true)
	require.NoError(t, err)
	updater := newStatusUpdater(c)
	// Force a status write calculated from an informer snapshot taken before
	// the lifecycle writer persisted its custom gate and condition.
	err = updater.UpdateInstanceStatus(context.Background(), stale, &api.RoleInstanceStatus{ObservedGeneration: 1}, []*corev1.Pod{pod})
	require.Error(t, err, "requeue instead of overwriting the lifecycle writer")
	got := &api.RoleInstance{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), got))
	require.Equal(t, corev1.ConditionFalse, instancereadiness.GetReadinessCondition(got).Status)
	gotPod := &corev1.Pod{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pod), gotPod))
	for _, condition := range gotPod.Status.Conditions {
		if condition.Type == constants.InstancePodReadyConditionType {
			require.Equal(t, corev1.ConditionFalse, condition.Status, "do not clear the Pod block using a stale RI")
		}
	}
}

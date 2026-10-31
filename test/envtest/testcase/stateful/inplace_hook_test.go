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

package stateful

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	api "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("Stateful InPlaceUpdate lifecycle hook", func() {
	const timeout = 30 * time.Second
	const interval = 200 * time.Millisecond
	const label = "example.com/inplace-protection"
	var namespace string
	BeforeEach(func() {
		namespace = fmt.Sprintf("test-inplace-hook-%d", time.Now().UnixNano())
		testutil.CreateNamespace(namespace)
	})
	AfterEach(func() { testutil.DeleteNamespace(namespace) })
	DescribeTable("waits for release, updates in place, and completes the hook lifecycle", func(policy constants.PodManagementPolicyType) {
		set := newRolloutRecoverySet("hooked-update", namespace)
		set.Spec.PodManagementPolicy = policy
		set.Spec.UpdateStrategy.Type = api.InPlaceIfPossibleUpdateStrategyType
		set.Spec.Lifecycle = &api.RoleInstanceSetLifecycle{InPlaceUpdate: &api.RoleInstanceSetLifecycleHook{LabelsHandler: map[string]string{label: "hold"}}}
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())
		setKey := client.ObjectKeyFromObject(set)
		instanceKey := types.NamespacedName{Namespace: namespace, Name: set.Name + "-0"}
		podKey := types.NamespacedName{Namespace: namespace, Name: set.Name + "-0-worker-0"}
		// Envtest has no kubelet. Supply container runtime status explicitly;
		// real RI/RIS controllers still perform all template and lifecycle writes.
		setRuntimeStatus := func() error {
			pod := &corev1.Pod{}
			if err := testutil.K8sClient.Get(testutil.Ctx, podKey, pod); err != nil {
				return err
			}
			testutil.SetPodRunningAndReady(pod)
			for i := range pod.Status.ContainerStatuses {
				pod.Status.ContainerStatuses[i].ImageID = "test://" + pod.Spec.Containers[i].Image
				pod.Status.ContainerStatuses[i].ContainerID = "test://container-" + pod.Spec.Containers[i].Image
			}
			return testutil.K8sClient.Status().Update(testutil.Ctx, pod)
		}
		Eventually(setRuntimeStatus, timeout, interval).Should(Succeed())
		awaitRolloutSet(setKey, func(got *api.RoleInstanceSet) bool { return got.Status.ReadyReplicas == 1 })
		instance := &api.RoleInstance{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance)).To(Succeed())
		instanceUID := instance.UID
		before := instance.DeepCopy()
		instance.Labels[label] = "hold"
		Expect(testutil.K8sClient.Patch(testutil.Ctx, instance, client.MergeFrom(before))).To(Succeed())
		pod := &corev1.Pod{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, podKey, pod)).To(Succeed())
		podUID := pod.UID
		updateRolloutSpec(setKey, func(got *api.RoleInstanceSet) {
			got.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Containers[0].Image = "nginx:2.0"
			got.Spec.UpdateStrategy.Partition = ptr.To(intstr.FromInt32(1))
		})
		awaitRolloutSet(setKey, func(got *api.RoleInstanceSet) bool { return got.Status.ObservedGeneration >= got.Generation })
		Consistently(func() (string, error) {
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return "", err
			}
			return instance.Spec.Components[0].Template.Spec.Containers[0].Image, nil
		}, 2*time.Second, interval).Should(Equal("nginx:1.0"), "partition must still protect this ordinal")
		updateRolloutSpec(setKey, func(got *api.RoleInstanceSet) { got.Spec.UpdateStrategy.Partition = ptr.To(intstr.FromInt32(0)) })
		Eventually(func() (string, error) {
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return "", err
			}
			return instance.Labels[constants.RoleInstanceSetLifecycleStateKey], nil
		}, timeout, interval).Should(Equal(string(constants.RoleInstanceLifecycleStatePreparingUpdate)))
		Consistently(func() (bool, error) {
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return false, err
			}
			if err := testutil.K8sClient.Get(testutil.Ctx, podKey, pod); err != nil {
				return false, err
			}
			return instance.UID == instanceUID && instance.DeletionTimestamp == nil && pod.UID == podUID &&
				instance.Spec.Components[0].Template.Spec.Containers[0].Image == "nginx:1.0" && pod.Spec.Containers[0].Image == "nginx:1.0", nil
		}, 2*time.Second, interval).Should(BeTrue())
		updateRolloutSpec(setKey, func(got *api.RoleInstanceSet) { got.Spec.UpdateStrategy.Paused = true })
		awaitRolloutSet(setKey, func(got *api.RoleInstanceSet) bool { return got.Status.ObservedGeneration >= got.Generation })
		before = instance.DeepCopy()
		delete(instance.Labels, label)
		Expect(testutil.K8sClient.Patch(testutil.Ctx, instance, client.MergeFrom(before))).To(Succeed())
		Consistently(func() (string, error) {
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return "", err
			}
			return instance.Spec.Components[0].Template.Spec.Containers[0].Image, nil
		}, 2*time.Second, interval).Should(Equal("nginx:1.0"), "release must not bypass a paused rollout")
		updateRolloutSpec(setKey, func(got *api.RoleInstanceSet) { got.Spec.UpdateStrategy.Paused = false })
		Eventually(func() (bool, error) {
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return false, err
			}
			if err := testutil.K8sClient.Get(testutil.Ctx, podKey, pod); err != nil {
				return false, err
			}
			// Updating can be transient: completion uses the existing component
			// revision checks, independently of our simulated kubelet callback.
			state := instance.Labels[constants.RoleInstanceSetLifecycleStateKey]
			return (state == string(constants.RoleInstanceLifecycleStateUpdating) || state == string(constants.RoleInstanceLifecycleStateUpdated)) &&
				pod.Spec.Containers[0].Image == "nginx:2.0", nil
		}, timeout, interval).Should(BeTrue())
		Expect(instance.UID).To(Equal(instanceUID))
		Expect(pod.UID).To(Equal(podUID))
		Eventually(setRuntimeStatus, timeout, interval).Should(Succeed())
		Eventually(func() (string, error) {
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return "", err
			}
			return instance.Labels[constants.RoleInstanceSetLifecycleStateKey], nil
		}, timeout, interval).Should(Equal(string(constants.RoleInstanceLifecycleStateUpdated)))
		before = instance.DeepCopy()
		instance.Labels[label] = "hold"
		Expect(testutil.K8sClient.Patch(testutil.Ctx, instance, client.MergeFrom(before))).To(Succeed())
		Eventually(func() (string, error) {
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return "", err
			}
			return instance.Labels[constants.RoleInstanceSetLifecycleStateKey], nil
		}, timeout, interval).Should(Equal(string(constants.RoleInstanceLifecycleStateNormal)))
		Expect(instance.UID).To(Equal(instanceUID))
		Expect(testutil.K8sClient.Get(testutil.Ctx, podKey, pod)).To(Succeed())
		Expect(pod.UID).To(Equal(podUID))
	}, Entry("Parallel", constants.ParallelPodManagement), Entry("OrderedReady", constants.OrderedReadyPodManagement))
})

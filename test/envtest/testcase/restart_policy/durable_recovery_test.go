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

package restart_policy

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

const recoveryHoldFinalizer = "test.rbg.io/recovery-hold"

// These tests exercise a single RI independently of RIS rollout. envtest has no
// kubelet: Pending/NotReady replacement Pods are intentional, not a test failure.
var _ = Describe("Durable RoleInstance recovery", func() {
	var namespace string
	var instance *workloadsv1alpha2.RoleInstance

	BeforeEach(func() {
		namespace = fmt.Sprintf("test-durable-recovery-%d", time.Now().UnixNano())
		testutil.CreateNamespace(namespace)
		instance = &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name: "rank-group", Namespace: namespace,
				Labels: map[string]string{constants.RoleTypeLabelKey: string(constants.ComponentsTemplateType)},
			},
			Spec: workloadsv1alpha2.RoleInstanceSpec{
				ReadyPolicy: workloadsv1alpha2.RoleInstanceReadyOnAllPodReady,
				RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
					Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation,
					BaseDelaySeconds: ptr.To[int32](1), MaxDelaySeconds: ptr.To[int32](2),
				},
				Components: []workloadsv1alpha2.RoleInstanceComponent{{
					Name: "rank", Size: ptr.To[int32](2),
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "main", Image: "nginx:1.27"}},
					}},
				}},
			},
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, instance)).To(Succeed())
	})

	AfterEach(func() {
		// A failed assertion must not leave a test finalizer blocking teardown.
		pods := &corev1.PodList{}
		Expect(testutil.K8sClient.List(testutil.Ctx, pods, client.InNamespace(namespace))).To(Succeed())
		for i := range pods.Items {
			key := client.ObjectKeyFromObject(&pods.Items[i])
			Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				pod := &corev1.Pod{}
				if err := testutil.K8sClient.Get(testutil.Ctx, key, pod); err != nil {
					return client.IgnoreNotFound(err)
				}
				pod.Finalizers = nil
				return testutil.K8sClient.Update(testutil.Ctx, pod)
			})).To(Succeed())
		}
		testutil.DeleteNamespace(namespace)
	})

	listOwned := func() []corev1.Pod {
		list := &corev1.PodList{}
		ExpectWithOffset(1, testutil.K8sClient.List(testutil.Ctx, list, client.InNamespace(namespace))).To(Succeed())
		var owned []corev1.Pod
		for i := range list.Items {
			if metav1.IsControlledBy(&list.Items[i], instance) {
				owned = append(owned, list.Items[i])
			}
		}
		return owned
	}
	readInstance := func() *workloadsv1alpha2.RoleInstance {
		fresh := &workloadsv1alpha2.RoleInstance{}
		ExpectWithOffset(1, testutil.K8sClient.Get(testutil.Ctx, client.ObjectKeyFromObject(instance), fresh)).To(Succeed())
		ExpectWithOffset(1, fresh.UID).To(Equal(instance.UID), "recovery must retain the RI object")
		return fresh
	}
	memberPods := func(previous map[string]types.UID) map[string]types.UID {
		var result map[string]types.UID
		EventuallyWithOffset(1, func() bool {
			pods := listOwned()
			if len(pods) != 2 {
				return false
			}
			result = make(map[string]types.UID, 2)
			for _, pod := range pods {
				if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodFailed {
					return false
				}
				if previous != nil && previous[pod.Name] == pod.UID {
					return false
				}
				result[pod.Name] = pod.UID
			}
			return true
		}, timeout, interval).Should(BeTrue(), "every member must belong to the replacement member Pods")
		return result
	}
	setStatus := func(key client.ObjectKey, ready bool, restarts int32) {
		ExpectWithOffset(1, retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			pod := &corev1.Pod{}
			if err := testutil.K8sClient.Get(testutil.Ctx, key, pod); err != nil {
				return err
			}
			testutil.SetPodRunningAndReady(pod)
			pod.Status.ContainerStatuses[0].RestartCount = restarts
			if !ready {
				for i := range pod.Status.Conditions {
					pod.Status.Conditions[i].Status = corev1.ConditionFalse
				}
				pod.Status.ContainerStatuses[0].Ready = false
			}
			return testutil.K8sClient.Status().Update(testutil.Ctx, pod)
		})).To(Succeed())
	}
	isReady := func(ri *workloadsv1alpha2.RoleInstance) bool {
		for _, condition := range ri.Status.Conditions {
			if condition.Type == workloadsv1alpha2.RoleInstanceReady {
				return condition.Status == corev1.ConditionTrue
			}
		}
		return false
	}
	makeReady := func() {
		for _, pod := range listOwned() {
			setStatus(client.ObjectKeyFromObject(&pod), true, 0)
		}
		EventuallyWithOffset(1, func() bool { return isReady(readInstance()) }, timeout, interval).Should(BeTrue())
	}

	It("does not recreate ordinary Pending members before first Ready", func() {
		original := memberPods(nil)
		Consistently(func() map[string]types.UID {
			actual := make(map[string]types.UID)
			for _, pod := range listOwned() {
				actual[pod.Name] = pod.UID
			}
			return actual
		}, 3*time.Second, interval).Should(Equal(original))
		Expect(readInstance().Status.RestartCount).To(BeZero())
	})

	It("does not delete a new UID under an old list, then retries its explicit failure", func() {
		original := memberPods(nil)
		pods := listOwned()
		heldKey := client.ObjectKeyFromObject(&pods[0])
		replacementTemplate := pods[1].DeepCopy()
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			pod := &corev1.Pod{}
			if err := testutil.K8sClient.Get(testutil.Ctx, heldKey, pod); err != nil {
				return err
			}
			pod.Finalizers = append(pod.Finalizers, recoveryHoldFinalizer)
			return testutil.K8sClient.Update(testutil.Ctx, pod)
		})).To(Succeed())
		setStatus(heldKey, false, 1)
		Eventually(func() bool {
			fresh := readInstance()
			return len(fresh.Status.RetiringPods) > 0 && fresh.Status.RestartCount == 1
		}, timeout, interval).Should(BeTrue())
		Eventually(func() bool {
			remaining := listOwned()
			return len(remaining) == 1 && remaining[0].Name == heldKey.Name && remaining[0].DeletionTimestamp != nil
		}, timeout, interval).Should(BeTrue())
		external := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: replacementTemplate.Name, Namespace: namespace,
			Labels: replacementTemplate.Labels, Annotations: replacementTemplate.Annotations,
			OwnerReferences: replacementTemplate.OwnerReferences,
		}, Spec: replacementTemplate.Spec}
		Expect(testutil.K8sClient.Create(testutil.Ctx, external)).To(Succeed())
		Consistently(func() bool {
			fresh := readInstance()
			current := &corev1.Pod{}
			if err := testutil.K8sClient.Get(testutil.Ctx, client.ObjectKeyFromObject(external), current); err != nil {
				return false
			}
			return len(fresh.Status.RetiringPods) > 0 && fresh.Status.RestartCount == 1 &&
				current.UID == external.UID && current.DeletionTimestamp == nil
		}, 2*time.Second, interval).Should(BeTrue(), "saved old UIDs must not authorize deleting a replacement")
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			pod := &corev1.Pod{}
			if err := testutil.K8sClient.Get(testutil.Ctx, heldKey, pod); err != nil {
				return err
			}
			pod.Finalizers = nil
			return testutil.K8sClient.Update(testutil.Ctx, pod)
		})).To(Succeed())
		Eventually(func() bool {
			current := listOwned()
			if len(current) != 2 || len(readInstance().Status.RetiringPods) != 0 {
				return false
			}
			for _, pod := range current {
				if pod.Name == external.Name && pod.UID != external.UID {
					return false
				}
				if pod.Name == heldKey.Name && (pod.UID == original[pod.Name] || pod.DeletionTimestamp != nil) {
					return false
				}
			}
			return true
		}, timeout, interval).Should(BeTrue())
		Expect(readInstance().Status.RestartCount).To(Equal(int32(1)))
		beforeFailure := memberPods(nil)
		setStatus(client.ObjectKeyFromObject(external), false, 1)
		memberPods(beforeFailure)
		Eventually(func() int32 { return readInstance().Status.RestartCount }, timeout, interval).Should(Equal(int32(2)))
	})

	It("retries three fresh member-Pod sets before first Ready without changing the RI UID", func() {
		previous := memberPods(nil)
		for attempt := int32(1); attempt <= 3; attempt++ {
			Expect(isReady(readInstance())).To(BeFalse())
			pods := listOwned()
			setStatus(client.ObjectKeyFromObject(&pods[0]), false, 1)
			previous = memberPods(previous)
			Eventually(func() int32 { return readInstance().Status.RestartCount }, timeout, interval).Should(Equal(attempt))
			Eventually(func() bool { return len(readInstance().Status.RetiringPods) == 0 }, timeout, interval).Should(BeTrue(),
				"cleanup of old member Pods must finish without waiting for business readiness")
			Expect(isReady(readInstance())).To(BeFalse())
		}
		makeReady()
		Expect(readInstance().Status.RestartCount).To(Equal(int32(3)))
	})

	It("restarts the whole instance after an individually repaired member fails", func() {
		setPolicy := func(policy workloadsv1alpha2.RestartPolicyType) {
			Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				fresh := readInstance()
				fresh.Spec.RestartPolicyConfig.Type = policy
				return testutil.K8sClient.Update(testutil.Ctx, fresh)
			})).To(Succeed())
			Eventually(func() bool {
				fresh := readInstance()
				return fresh.Status.ObservedGeneration == fresh.Generation
			}, timeout, interval).Should(BeTrue())
		}
		original := memberPods(nil)
		setPolicy(workloadsv1alpha2.RestartPolicyNone)
		pods := listOwned()
		deleted := pods[0]
		Expect(testutil.K8sClient.Delete(testutil.Ctx, &deleted)).To(Succeed())
		Eventually(func() bool {
			current := listOwned()
			if len(current) != 2 {
				return false
			}
			for _, pod := range current {
				if pod.Name == deleted.Name && pod.UID == deleted.UID {
					return false
				}
				if pod.Name != deleted.Name && pod.UID != original[pod.Name] {
					return false
				}
			}
			return true
		}, timeout, interval).Should(BeTrue(), "ordinary Scale replaces only the missing member")
		Expect(readInstance().Status.RestartCount).To(BeZero())
		setPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation)
		beforeFailure := memberPods(nil)
		setStatus(client.ObjectKeyFromObject(&deleted), false, 1)
		memberPods(beforeFailure)
		Eventually(func() int32 { return readInstance().Status.RestartCount }, timeout, interval).Should(Equal(int32(1)))
	})

	It("[restart-budget] charges an all-or-nothing drain once, then exhausts before first Ready", func() {
		original := memberPods(nil)
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			fresh := readInstance()
			fresh.Spec.RestartPolicyConfig.MaxGroupRestarts = ptr.To[int32](1)
			fresh.Spec.RestartPolicyConfig.BaseDelaySeconds = ptr.To[int32](30)
			fresh.Spec.RestartPolicyConfig.MaxDelaySeconds = ptr.To[int32](600)
			return testutil.K8sClient.Update(testutil.Ctx, fresh)
		})).To(Succeed())
		Eventually(func() bool {
			fresh := readInstance()
			return fresh.Status.ObservedGeneration == fresh.Generation
		}, timeout, interval).Should(BeTrue())
		pods := listOwned()
		heldKey := client.ObjectKeyFromObject(&pods[0])
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			pod := &corev1.Pod{}
			if err := testutil.K8sClient.Get(testutil.Ctx, heldKey, pod); err != nil {
				return err
			}
			pod.Finalizers = append(pod.Finalizers, recoveryHoldFinalizer)
			return testutil.K8sClient.Update(testutil.Ctx, pod)
		})).To(Succeed())
		setStatus(heldKey, false, 1)
		Eventually(func() bool {
			fresh := readInstance()
			return fresh.Status.GroupRestartCount == 1 && len(fresh.Status.RetiringPods) > 0
		}, timeout, interval).Should(BeTrue())
		Consistently(func() bool {
			fresh := readInstance()
			for _, pod := range listOwned() {
				if original[pod.Name] != pod.UID {
					return false
				}
			}
			return !fresh.Status.RestartBudgetExhausted && fresh.Status.GroupRestartCount == 1 && len(fresh.Status.RetiringPods) > 0
		}, 2*time.Second, interval).Should(BeTrue(), "the accepted drain cannot consume its final budget twice or create partial replacements")
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			pod := &corev1.Pod{}
			if err := testutil.K8sClient.Get(testutil.Ctx, heldKey, pod); err != nil {
				return err
			}
			pod.Finalizers = nil
			return testutil.K8sClient.Update(testutil.Ctx, pod)
		})).To(Succeed())
		replacements := memberPods(original)
		Eventually(func() bool { return len(readInstance().Status.RetiringPods) == 0 }, timeout, interval).Should(BeTrue())
		Expect(isReady(readInstance())).To(BeFalse())
		pods = listOwned()
		setStatus(client.ObjectKeyFromObject(&pods[0]), false, 1)
		Eventually(func() bool { return readInstance().Status.RestartBudgetExhausted }, 15*time.Second, interval).Should(BeTrue(), "exhaustion must not wait out the 30s restart backoff")
		Eventually(func() bool {
			pods := listOwned()
			if len(pods) != 2 {
				return false
			}
			for _, pod := range pods {
				if pod.UID != replacements[pod.Name] || pod.DeletionTimestamp == nil {
					return false
				}
			}
			return true
		}, timeout, interval).Should(BeTrue())
		fresh := readInstance()
		Expect(fresh.Status.GroupRestartCount).To(Equal(int32(1)))
		Expect(fresh.Status.RestartCount).To(Equal(int32(1)), "suppression is not another recreation")
		Expect(fresh.Status.RetiringPods).To(BeEmpty())
		Expect(isReady(fresh)).To(BeFalse())
		Expect(testutil.K8sClient.Delete(testutil.Ctx, fresh)).To(Succeed())
		Eventually(func() int { return len(listOwned()) }, timeout, interval).Should(BeZero(), "RI teardown releases retained Pod finalizers")
	})

	It("drains a terminating member once and resumes only after its finalizer is released", func() {
		original := memberPods(nil)
		makeReady()
		pods := listOwned()
		heldKey := client.ObjectKeyFromObject(&pods[0])
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			pod := &corev1.Pod{}
			if err := testutil.K8sClient.Get(testutil.Ctx, heldKey, pod); err != nil {
				return err
			}
			pod.Finalizers = append(pod.Finalizers, recoveryHoldFinalizer)
			return testutil.K8sClient.Update(testutil.Ctx, pod)
		})).To(Succeed())
		setStatus(heldKey, false, 0)
		Eventually(func() bool { return isReady(readInstance()) }, timeout, interval).Should(BeFalse())
		held := &corev1.Pod{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, heldKey, held)).To(Succeed())
		Expect(testutil.K8sClient.Delete(testutil.Ctx, held)).To(Succeed())
		Eventually(func() int32 { return readInstance().Status.RestartCount }, timeout, interval).Should(Equal(int32(1)))
		Consistently(func() bool {
			for _, pod := range listOwned() {
				if original[pod.Name] != pod.UID {
					return false
				}
			}
			return readInstance().Status.RestartCount == 1 && len(readInstance().Status.RetiringPods) > 0
		}, 3*time.Second, interval).Should(BeTrue(), "no replacements or duplicate restarts while old member Pods are draining")
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			pod := &corev1.Pod{}
			if err := testutil.K8sClient.Get(testutil.Ctx, heldKey, pod); err != nil {
				return err
			}
			pod.Finalizers = nil
			return testutil.K8sClient.Update(testutil.Ctx, pod)
		})).To(Succeed())
		memberPods(original)
		Eventually(func() bool { return len(readInstance().Status.RetiringPods) == 0 }, timeout, interval).Should(BeTrue())
		Expect(isReady(readInstance())).To(BeFalse())
		Expect(readInstance().Status.RestartCount).To(Equal(int32(1)))
	})
})

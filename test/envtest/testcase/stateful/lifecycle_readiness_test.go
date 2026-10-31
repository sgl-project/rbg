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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	api "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancelifecycle "sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("RoleInstance lifecycle readiness", func() {
	const timeout = 25 * time.Second
	const interval = 200 * time.Millisecond
	var namespace string
	BeforeEach(func() {
		namespace = fmt.Sprintf("test-lifecycle-readiness-%d", time.Now().UnixNano())
		testutil.CreateNamespace(namespace)
	})
	AfterEach(func() { testutil.DeleteNamespace(namespace) })

	DescribeTable("propagates a lifecycle block to every component Pod without recreating them",
		func(state constants.RoleInstanceLifecycleStateType, policy api.RoleInstanceReadyPolicyType) {
			// Exercise the shared lifecycle contract directly. Stateful PreDelete
			// entry-point enforcement is covered by its separate fix.
			template := newRolloutRecoverySet("readiness", namespace).Spec.RoleInstanceTemplate.RoleInstanceSpec
			template.ReadyPolicy = policy
			extra := *template.Components[0].DeepCopy()
			extra.Name = "sidecar"
			template.Components = append(template.Components, extra)
			instance := &api.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Name: "readiness-0", Namespace: namespace, Labels: map[string]string{
					constants.RoleTypeLabelKey: string(constants.ComponentsTemplateType),
				}},
				Spec: template,
			}
			Expect(testutil.K8sClient.Create(testutil.Ctx, instance)).To(Succeed())
			key := client.ObjectKeyFromObject(instance)
			podKeys := []types.NamespacedName{
				{Namespace: namespace, Name: "readiness-0-worker-0"},
				{Namespace: namespace, Name: "readiness-0-sidecar-0"},
			}
			podUIDs := make(map[types.NamespacedName]types.UID)
			for _, podKey := range podKeys {
				Eventually(func() error { return setPodReady(podKey, true) }, timeout, interval).Should(Succeed())
				pod := &corev1.Pod{}
				Expect(testutil.K8sClient.Get(testutil.Ctx, podKey, pod)).To(Succeed())
				Expect(pod.Spec.ReadinessGates).To(ContainElement(corev1.PodReadinessGate{ConditionType: constants.InstancePodReadyConditionType}))
				podUIDs[podKey] = pod.UID
			}
			readyState := func(want corev1.ConditionStatus) error {
				got := &api.RoleInstance{}
				if err := testutil.K8sClient.Get(testutil.Ctx, key, got); err != nil {
					return err
				}
				found := false
				for _, condition := range got.Status.Conditions {
					if condition.Type == api.RoleInstanceReady {
						found = true
						if condition.Status != want {
							return fmt.Errorf("RI Ready=%s, want %s", condition.Status, want)
						}
					}
				}
				if !found {
					return fmt.Errorf("RI Ready missing")
				}
				for _, podKey := range podKeys {
					pod := &corev1.Pod{}
					if err := testutil.K8sClient.Get(testutil.Ctx, podKey, pod); err != nil {
						return err
					}
					if pod.UID != podUIDs[podKey] {
						return fmt.Errorf("Pod %s was replaced", podKey.Name)
					}
					found := false
					for _, condition := range pod.Status.Conditions {
						if condition.Type == constants.InstancePodReadyConditionType {
							found = true
							if condition.Status != want {
								return fmt.Errorf("Pod %s gate=%s, want %s", pod.Name, condition.Status, want)
							}
						}
					}
					if !found {
						return fmt.Errorf("Pod %s gate missing", pod.Name)
					}
				}
				return nil
			}
			Eventually(func() error { return readyState(corev1.ConditionTrue) }, timeout, interval).Should(Succeed())
			control := instancelifecycle.New(testutil.K8sClient)
			Eventually(func() error {
				if err := testutil.K8sClient.Get(testutil.Ctx, key, instance); err != nil {
					return err
				}
				_, _, err := control.UpdateRoleInstanceLifecycle(instance, state, true)
				return err
			}, timeout, interval).Should(Succeed())
			Eventually(func() error { return readyState(corev1.ConditionFalse) }, timeout, interval).Should(Succeed())
			Consistently(func() error {
				if err := testutil.K8sClient.Get(testutil.Ctx, key, instance); err != nil {
					return err
				}
				// Repeated lifecycle calls and resulting controller reconciles
				// must not clear the block or overwrite another gate.
				if _, _, err := control.UpdateRoleInstanceLifecycle(instance, state, true); err != nil {
					return err
				}
				return readyState(corev1.ConditionFalse)
			}, 2*time.Second, interval).Should(Succeed())
			if state == constants.RoleInstanceLifecycleStatePreparingUpdate {
				Eventually(func() error {
					if err := testutil.K8sClient.Get(testutil.Ctx, key, instance); err != nil {
						return err
					}
					_, _, err := control.UpdateRoleInstanceLifecycle(instance, constants.RoleInstanceLifecycleStateNormal, true)
					return err
				}, timeout, interval).Should(Succeed())
				Eventually(func() error { return readyState(corev1.ConditionTrue) }, timeout, interval).Should(Succeed())
			}
		},
		Entry("PreDelete / AllPodReady", constants.RoleInstanceLifecycleStatePreparingDelete, api.RoleInstanceReadyOnAllPodReady),
		Entry("PreDelete / None", constants.RoleInstanceLifecycleStatePreparingDelete, api.RoleInstanceReadyPolicyTypeNone),
		Entry("PreparingUpdate / AllPodReady", constants.RoleInstanceLifecycleStatePreparingUpdate, api.RoleInstanceReadyOnAllPodReady),
		Entry("PreparingUpdate / None", constants.RoleInstanceLifecycleStatePreparingUpdate, api.RoleInstanceReadyPolicyTypeNone),
	)
})

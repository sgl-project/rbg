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
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("Restart budget lifecycle", func() {
	It("[restart-budget] retains complete groups, rejects implicit recovery and cleans up on RI deletion", func() {
		namespace := fmt.Sprintf("budget-%d", time.Now().UnixNano())
		testutil.CreateNamespace(namespace)
		DeferCleanup(func() { testutil.DeleteNamespace(namespace) })
		cli, ctx := testutil.K8sClient, testutil.Ctx
		instance := &workloadsv1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "bounded", Namespace: namespace, Annotations: map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}}, Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation, MaxGroupRestarts: ptr.To[int32](0)},
			Components:          []workloadsv1alpha2.RoleInstanceComponent{{Name: "worker", Size: ptr.To[int32](2), Template: v1.PodTemplateSpec{Spec: v1.PodSpec{Containers: []v1.Container{{Name: "worker", Image: "nginx:latest"}}}}}},
		}}
		key := client.ObjectKeyFromObject(instance)
		getInstance := func() *workloadsv1alpha2.RoleInstance {
			live := &workloadsv1alpha2.RoleInstance{}
			Expect(cli.Get(ctx, key, live)).To(Succeed())
			return live
		}
		listPods := func() []v1.Pod {
			list := &v1.PodList{}
			Expect(cli.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
			return list.Items
		}
		Expect(cli.Create(ctx, instance)).To(Succeed())
		Eventually(func() int { return len(listPods()) }, timeout, interval).Should(Equal(2))
		Eventually(func() bool {
			live := getInstance()
			return live.Status.ObservedGeneration == live.Generation && live.Status.CurrentRevision == live.Status.UpdateRevision
		}, timeout, interval).Should(BeTrue())
		Expect(getInstance().Status.RestartBudgetExhausted).To(BeFalse(), "max zero must not freeze healthy creation")
		originalUIDs := map[types.UID]bool{}
		for _, p := range listPods() {
			originalUIDs[p.UID] = true
		}
		failPod := func() {
			pods := listPods()
			Expect(pods).To(HaveLen(2))
			Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				pod := &v1.Pod{}
				if err := cli.Get(ctx, client.ObjectKeyFromObject(&pods[0]), pod); err != nil {
					return err
				}
				pod.Status.Phase = v1.PodFailed
				return cli.Status().Update(ctx, pod)
			})).To(Succeed())
		}
		failPod()
		Eventually(func() bool { return getInstance().Status.RestartBudgetExhausted }, timeout, interval).Should(BeTrue())
		Eventually(func() bool {
			pods := listPods()
			if len(pods) != 2 {
				return false
			}
			for _, pod := range pods {
				if pod.DeletionTimestamp == nil {
					return false
				}
				found := false
				for _, f := range pod.Finalizers {
					if f == constants.GroupRestartBudgetCleanupFinalizer {
						found = true
					}
				}
				if !found {
					return false
				}
			}
			return true
		}, timeout, interval).Should(BeTrue())
		live := getInstance()
		Expect(live.Status.GroupRestartCount).To(BeZero())
		Expect(live.Annotations).NotTo(HaveKey(constants.RoleInstanceRecoverAnnotationKey))
		Expect(live.Status.Conditions).To(ContainElement(And(HaveField("Type", workloadsv1alpha2.RoleInstanceDegraded), HaveField("Status", v1.ConditionTrue), HaveField("Reason", "GroupRestartBudgetExceeded"))))
		Expect(live.Status.Conditions).To(ContainElement(And(HaveField("Type", workloadsv1alpha2.RoleInstanceReady), HaveField("Status", v1.ConditionFalse))))
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			live := getInstance()
			live.Spec.RestartPolicyConfig.MaxGroupRestarts = ptr.To[int32](10)
			return cli.Update(ctx, live)
		})).To(Succeed())
		Consistently(func() bool { return getInstance().Status.RestartBudgetExhausted }, 3*time.Second, interval).Should(BeTrue())
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			live := getInstance()
			live.Spec.RestartPolicyConfig.MaxGroupRestarts = nil
			live.Spec.RestartPolicyConfig.Type = workloadsv1alpha2.RestartPolicyNone
			return cli.Update(ctx, live)
		})).To(Succeed())
		Consistently(func() bool { return getInstance().Status.RestartBudgetExhausted }, 3*time.Second, interval).Should(BeTrue())
		// Configure max zero again, but only the annotation is allowed to recover.
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			live := getInstance()
			live.Spec.RestartPolicyConfig.Type = workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation
			live.Spec.RestartPolicyConfig.MaxGroupRestarts = ptr.To[int32](0)
			if live.Annotations == nil {
				live.Annotations = map[string]string{}
			}
			live.Annotations[constants.RoleInstanceRecoverAnnotationKey] = "true"
			return cli.Update(ctx, live)
		})).To(Succeed())
		Eventually(func() bool {
			if getInstance().Status.RestartBudgetExhausted {
				return false
			}
			pods := listPods()
			if len(pods) != 2 {
				return false
			}
			for _, pod := range pods {
				if originalUIDs[pod.UID] || pod.DeletionTimestamp != nil {
					return false
				}
			}
			return true
		}, timeout, interval).Should(BeTrue())
		Eventually(func() bool {
			live := getInstance()
			return live.Generation == live.Status.ObservedGeneration && live.Status.CurrentRevision == live.Status.UpdateRevision
		}, timeout, interval).Should(BeTrue())
		failPod()
		Eventually(func() bool { return getInstance().Status.RestartBudgetExhausted }, timeout, interval).Should(BeTrue())
		Eventually(func() bool {
			pods := listPods()
			if len(pods) != 2 {
				return false
			}
			for _, pod := range pods {
				if pod.DeletionTimestamp == nil {
					return false
				}
			}
			return true
		}, timeout, interval).Should(BeTrue())
		live = getInstance()
		Expect(cli.Delete(ctx, live)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(cli.Get(ctx, key, &workloadsv1alpha2.RoleInstance{})) }, timeout, interval).Should(BeTrue())
		Eventually(func() int { return len(listPods()) }, timeout, interval).Should(BeZero(), "deletion must not strand budget finalizers")
	})

	It("[restart-budget] rejects negative limits and budgets without a recreate policy via CEL", func() {
		namespace := fmt.Sprintf("budget-schema-%d", time.Now().UnixNano())
		testutil.CreateNamespace(namespace)
		DeferCleanup(func() { testutil.DeleteNamespace(namespace) })
		for _, config := range []workloadsv1alpha2.RestartPolicyConfig{{Type: workloadsv1alpha2.RestartPolicyNone, MaxGroupRestarts: ptr.To[int32](0)}, {Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation, MaxGroupRestarts: ptr.To[int32](-1)}, {MaxGroupRestarts: ptr.To[int32](1)}} {
			instance := &workloadsv1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{GenerateName: "invalid-", Namespace: namespace}, Spec: workloadsv1alpha2.RoleInstanceSpec{RestartPolicyConfig: &config, Components: []workloadsv1alpha2.RoleInstanceComponent{}}}
			Expect(apierrors.IsInvalid(testutil.K8sClient.Create(testutil.Ctx, instance))).To(BeTrue())
		}
	})
})

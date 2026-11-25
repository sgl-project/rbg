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
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	v2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("LWS-compatible restart budget revisions", func() {
	DescribeTable("[restart-budget-resize] starts a fresh size revision but preserves surviving replica budgets",
		func(pattern constants.InstancePatternType, strategy v2.UpdateStrategyType, exhausted bool) {
			ctx, cli := testutil.Ctx, testutil.K8sClient
			namespace := fmt.Sprintf("budget-resize-%d", time.Now().UnixNano())
			testutil.CreateNamespace(namespace)
			DeferCleanup(func() { testutil.DeleteNamespace(namespace) })
			limit := int32(2)
			if exhausted {
				limit = 0
			}
			group := &v2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "resize", Namespace: namespace}, Spec: v2.RoleBasedGroupSpec{Roles: []v2.RoleSpec{{
				Name: "worker", Replicas: ptr.To[int32](2),
				Annotations: map[string]string{constants.RoleInstancePatternKey: string(pattern)},
				RolloutStrategy: &v2.RolloutStrategy{Type: v2.RollingUpdateStrategyType, RollingUpdate: &v2.RollingUpdate{Type: strategy,
					MaxSurge: ptr.To(intstr.FromInt32(1)), MaxUnavailable: ptr.To(intstr.FromInt32(0))}},
				Pattern: v2.Pattern{LeaderWorkerPattern: &v2.LeaderWorkerPattern{
					Size: ptr.To[int32](2),
					RestartPolicyConfig: &v2.RestartPolicyConfig{Type: v2.RecreateRoleInstanceOnPodRestart,
						ActivateAfter: v2.RestartPolicyActivateAfterCreated, MaxGroupRestarts: ptr.To(limit),
						BaseDelaySeconds: ptr.To[int32](0), MaxDelaySeconds: ptr.To[int32](0)},
					TemplateSource: v2.TemplateSource{Template: &v1.PodTemplateSpec{Spec: v1.PodSpec{Containers: []v1.Container{{Name: "main", Image: "nginx:latest"}}}}},
				}},
			}}}}
			Expect(cli.Create(ctx, group)).To(Succeed())
			instances := func() []v2.RoleInstance {
				list := &v2.RoleInstanceList{}
				Expect(cli.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
				return list.Items
			}
			pods := func() []v1.Pod {
				list := &v1.PodList{}
				Expect(cli.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
				return list.Items
			}
			makeReady := func() error {
				for _, snapshot := range pods() {
					// Envtest has no GC. Remove only Pods whose exact RI owner is
					// absent, leaving all rollout and budget decisions to controllers.
					if owner := metav1.GetControllerOf(&snapshot); owner != nil && owner.Kind == "RoleInstance" {
						instance := &v2.RoleInstance{}
						err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: owner.Name}, instance)
						if apierrors.IsNotFound(err) || (err == nil && instance.UID != owner.UID) {
							if err := client.IgnoreNotFound(cli.Delete(ctx, &snapshot, client.Preconditions{UID: &snapshot.UID})); err != nil {
								return err
							}
							continue
						}
						if err != nil {
							return err
						}
					}
					if testutil.ShouldSkipPodStatusUpdate(&snapshot) {
						continue
					}
					// SetPodRunningAndReady upserts the kubelet conditions and
					// already preserves the controller-written gate conditions
					// (see testutil); the former manual re-append duplicated
					// them on every poll until the status write exceeded
					// etcd's request limit. The conflict-safe write rereads
					// the Pod on every retry instead of reusing the snapshot.
					// The guards are re-checked on the freshly read object: a
					// Pod that started terminating or already failed after the
					// List snapshot must not be marked Running/Ready.
					if err := testutil.UpdatePodStatusOnConflict(ctx, &snapshot, func(pod *v1.Pod) bool {
						if testutil.ShouldSkipPodStatusUpdate(pod) {
							return true
						}
						testutil.SetPodRunningAndReady(pod)
						return false
					}); err != nil {
						if apierrors.IsNotFound(err) || testutil.IsRetryable(err) {
							continue
						}
						return err
					}
				}
				return nil
			}
			readyInstances := func() int {
				Expect(makeReady()).To(Succeed())
				ready := 0
				for _, instance := range instances() {
					for _, condition := range instance.Status.Conditions {
						if condition.Type == v2.RoleInstanceReady && condition.Status == v1.ConditionTrue {
							ready++
						}
					}
				}
				return ready
			}
			failMember := func(instance v2.RoleInstance) {
				var key client.ObjectKey
				for _, pod := range pods() {
					if metav1.IsControlledBy(&pod, &instance) {
						key = client.ObjectKeyFromObject(&pod)
						break
					}
				}
				Expect(key.Name).NotTo(BeEmpty())
				Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					pod := &v1.Pod{}
					if err := cli.Get(ctx, key, pod); err != nil {
						return err
					}
					pod.Status.Phase = v1.PodFailed
					return cli.Status().Update(ctx, pod)
				})).To(Succeed())
			}
			updateGroup := func(mutate func(*v2.RoleBasedGroup)) {
				Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					live := &v2.RoleBasedGroup{}
					if err := cli.Get(ctx, client.ObjectKeyFromObject(group), live); err != nil {
						return err
					}
					mutate(live)
					return cli.Update(ctx, live)
				})).To(Succeed())
			}
			Eventually(readyInstances, timeout, interval).Should(Equal(2))
			Expect(pods()).To(HaveLen(4))
			target := instances()[0]
			failMember(target)
			Eventually(func() bool {
				live := &v2.RoleInstance{}
				Expect(cli.Get(ctx, client.ObjectKeyFromObject(&target), live)).To(Succeed())
				if exhausted {
					return live.Status.RestartBudgetExhausted
				}
				return live.Status.GroupRestartCount == 1
			}, timeout, interval).Should(BeTrue())
			if exhausted {
				instanceProgressingReason := func() string {
					live := &v2.RoleInstance{}
					if err := cli.Get(ctx, client.ObjectKeyFromObject(&target), live); err != nil {
						return ""
					}
					for _, condition := range live.Status.Conditions {
						if condition.Type == v2.RoleInstanceProgressing && condition.Status == v1.ConditionTrue {
							return condition.Reason
						}
					}
					return ""
				}
				setProgressingReason := func() string {
					list := &v2.RoleInstanceSetList{}
					if err := cli.List(ctx, list, client.InNamespace(namespace)); err != nil || len(list.Items) != 1 {
						return ""
					}
					for _, condition := range list.Items[0].Status.Conditions {
						if condition.Type == v2.RoleInstanceSetProgressing && condition.Status == v1.ConditionTrue {
							return condition.Reason
						}
					}
					return ""
				}
				groupProgressingReason := func() string {
					live := &v2.RoleBasedGroup{}
					if err := cli.Get(ctx, client.ObjectKeyFromObject(group), live); err != nil {
						return ""
					}
					for _, condition := range live.Status.Conditions {
						if condition.Type == string(v2.RoleBasedGroupProgressing) && condition.Status == metav1.ConditionTrue {
							return condition.Reason
						}
					}
					return ""
				}
				Eventually(instanceProgressingReason, timeout, interval).Should(Equal(v2.ReasonGroupRestartBudgetExceeded))
				Eventually(setProgressingReason, timeout, interval).Should(Equal(constants.ProgressingReasonRestartBudgetExceeded))
				Eventually(groupProgressingReason, timeout, interval).Should(Equal(constants.ProgressingReasonRestartBudgetExceeded))

				Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					live := &v2.RoleInstance{}
					if err := cli.Get(ctx, client.ObjectKeyFromObject(&target), live); err != nil {
						return err
					}
					if live.Annotations == nil {
						live.Annotations = map[string]string{}
					}
					live.Annotations[constants.RoleInstanceRecoverAnnotationKey] = "true"
					return cli.Update(ctx, live)
				})).To(Succeed())
				Eventually(func() bool {
					if readyInstances() != 2 {
						return false
					}
					live := &v2.RoleInstance{}
					if err := cli.Get(ctx, client.ObjectKeyFromObject(&target), live); err != nil {
						return false
					}
					if live.Status.RestartBudgetExhausted ||
						live.Annotations[constants.RoleInstanceRecoverAnnotationKey] != "" ||
						instanceProgressingReason() == v2.ReasonGroupRestartBudgetExceeded ||
						setProgressingReason() == constants.ProgressingReasonRestartBudgetExceeded ||
						groupProgressingReason() == constants.ProgressingReasonRestartBudgetExceeded {
						return false
					}
					return true
				}, timeout, interval).Should(BeTrue(), "recover=true must clear budget exhaustion at RI, RIS, and RBG")
				return
			}
			if !exhausted {
				Eventually(readyInstances, timeout, interval).Should(Equal(2))
			}
			oldInstances := map[types.UID]bool{}
			for _, instance := range instances() {
				oldInstances[instance.UID] = true
			}
			oldPods := map[types.UID]bool{}
			for _, pod := range pods() {
				oldPods[pod.UID] = true
			}
			updateGroup(func(live *v2.RoleBasedGroup) { live.Spec.Roles[0].LeaderWorkerPattern.Size = ptr.To[int32](3) })
			Eventually(func() bool {
				Expect(makeReady()).To(Succeed())
				liveInstances := instances()
				if len(liveInstances) != 2 {
					return false
				}
				for _, instance := range liveInstances {
					if oldInstances[instance.UID] || ptr.Deref(instance.Spec.Components[1].Size, 0) != 2 ||
						instance.Status.GroupRestartCount != 0 || instance.Status.RestartBudgetExhausted {
						return false
					}
				}
				livePods := pods()
				if len(livePods) != 6 {
					return false
				}
				for _, pod := range livePods {
					if oldPods[pod.UID] || pod.DeletionTimestamp != nil {
						return false
					}
				}
				return readyInstances() == 2
			}, timeout, interval).Should(BeTrue(), "new size/topology revision receives fresh per-group budgets, as in LWS")
			if exhausted {
				return
			}

			// Unlike size, replicas is outside the topology revision: add one
			// group without resetting the surviving groups' accepted-failure count.
			target = instances()[0]
			failMember(target)
			Eventually(func() int32 {
				live := &v2.RoleInstance{}
				Expect(cli.Get(ctx, client.ObjectKeyFromObject(&target), live)).To(Succeed())
				return live.Status.GroupRestartCount
			}, timeout, interval).Should(Equal(int32(1)))
			Eventually(readyInstances, timeout, interval).Should(Equal(2))
			survivors := map[string]v2.RoleInstance{}
			for _, instance := range instances() {
				survivors[instance.Name] = instance
			}
			updateGroup(func(live *v2.RoleBasedGroup) { live.Spec.Roles[0].Replicas = ptr.To[int32](3) })
			Eventually(func() int { return readyInstances() }, timeout, interval).Should(Equal(3))
			for name, original := range survivors {
				live := &v2.RoleInstance{}
				Expect(cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, live)).To(Succeed())
				Expect(live.UID).To(Equal(original.UID))
				Expect(live.Status.GroupRestartCount).To(Equal(original.Status.GroupRestartCount))
				Expect(live.Status.GroupRestartRevision).To(Equal(original.Status.GroupRestartRevision))
			}
		},
		Entry("stateful in-place", constants.StatefulPattern, v2.InPlaceIfPossibleUpdateStrategyType, false),
		Entry("stateful recreate", constants.StatefulPattern, v2.RecreatePodUpdateStrategyType, false),
		Entry("stateless in-place with surge", constants.StatelessPattern, v2.InPlaceIfPossibleUpdateStrategyType, false),
		Entry("stateless recreate with surge", constants.StatelessPattern, v2.RecreatePodUpdateStrategyType, false),
		Entry("exhausted stateful recreate", constants.StatefulPattern, v2.RecreatePodUpdateStrategyType, true),
		Entry("exhausted stateless recreate", constants.StatelessPattern, v2.RecreatePodUpdateStrategyType, true),
	)
})

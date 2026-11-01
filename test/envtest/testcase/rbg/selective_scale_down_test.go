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

package rbg

import (
	"fmt"
	"sort"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apps "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappers "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

var _ = Describe("Stateful selective scale-down", func() {
	const timeout = 30 * time.Second
	const interval = 200 * time.Millisecond
	var namespace string
	BeforeEach(func() {
		namespace = fmt.Sprintf("selective-%d", time.Now().UnixNano())
		testutil.CreateNamespace(namespace)
	})
	AfterEach(func() { testutil.DeleteNamespace(namespace) })
	update := func(key client.ObjectKey, mutate func(*workloadsv1alpha2.RoleBasedGroup)) {
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latest := &workloadsv1alpha2.RoleBasedGroup{}
			if err := testutil.K8sClient.Get(testutil.Ctx, key, latest); err != nil {
				return err
			}
			mutate(latest)
			return testutil.K8sClient.Update(testutil.Ctx, latest)
		})).To(Succeed())
	}
	getMembers := func(key client.ObjectKey) []int {
		set := &workloadsv1alpha2.RoleInstanceSet{}
		if testutil.K8sClient.Get(testutil.Ctx, key, set) != nil {
			return nil
		}
		members, err := set.CommittedOrdinals()
		if err != nil {
			return nil
		}
		return members
	}
	instances := func(parentName string) map[string]types.UID {
		list := &workloadsv1alpha2.RoleInstanceList{}
		if testutil.K8sClient.List(testutil.Ctx, list, client.InNamespace(namespace), client.MatchingLabels{constants.GroupNameLabelKey: parentName}) != nil {
			return nil
		}
		result := map[string]types.UID{}
		for _, instance := range list.Items {
			if instance.DeletionTimestamp == nil {
				result[instance.Name] = instance.UID
			}
		}
		return result
	}
	names := func(values map[string]types.UID) []string {
		result := []string{}
		for name := range values {
			result = append(result, name)
		}
		sort.Strings(result)
		return result
	}

	DescribeTable("selects whole instances and keeps survivor identities", func(leaderWorker bool) {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(5).Obj()
		if leaderWorker {
			role = wrappers.BuildLeaderWorkerRole("worker").WithReplicas(5).WithSize(2).Obj()
		}
		group := wrappers.BuildBasicRoleBasedGroup("group", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())
		key := client.ObjectKeyFromObject(group)
		setKey := client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&role)}
		Eventually(func() int { return len(instances(group.Name)) }, timeout, interval).Should(Equal(5))
		original := instances(group.Name)
		initialSet := &workloadsv1alpha2.RoleInstanceSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, initialSet)).To(Succeed())
		hashKey := fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name)
		hash := initialSet.Labels[hashKey]
		revisionNames := func() []string {
			list := &apps.ControllerRevisionList{}
			Expect(testutil.K8sClient.List(testutil.Ctx, list, client.InNamespace(namespace))).To(Succeed())
			values := []string{}
			for _, item := range list.Items {
				values = append(values, item.Name)
			}
			sort.Strings(values)
			return values
		}
		Eventually(revisionNames, timeout, interval).ShouldNot(BeEmpty())
		revisions := revisionNames()
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) {
			g.Spec.Roles[0].Replicas = ptr.To[int32](3)
			g.Spec.Roles[0].Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1,3"}
		})
		Eventually(func() []int { return getMembers(setKey) }, timeout, interval).Should(Equal([]int{0, 2, 4}))
		Eventually(func() []string { return names(instances(group.Name)) }, timeout, interval).Should(Equal([]string{setKey.Name + "-0", setKey.Name + "-2", setKey.Name + "-4"}))
		for _, ordinal := range []int{0, 2, 4} {
			name := fmt.Sprintf("%s-%d", setKey.Name, ordinal)
			Expect(instances(group.Name)[name]).To(Equal(original[name]))
		}
		// Fixed-count E changes and clearing E leave membership and template revisions intact.
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) {
			g.Spec.Roles[0].Annotations[constants.ExcludedOrdinalsAnnotationKey] = "0,2,4"
		})
		Consistently(func() []int { return getMembers(setKey) }, time.Second, interval).Should(Equal([]int{0, 2, 4}))
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) {
			delete(g.Spec.Roles[0].Annotations, constants.ExcludedOrdinalsAnnotationKey)
		})
		Consistently(func() []int { return getMembers(setKey) }, time.Second, interval).Should(Equal([]int{0, 2, 4}))
		current := &workloadsv1alpha2.RoleInstanceSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, current)).To(Succeed())
		Expect(current.Labels[hashKey]).To(Equal(hash))
		Expect(revisionNames()).To(Equal(revisions))
		// Later scale-out can fill a released gap; it cannot evict a warm survivor.
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) { g.Spec.Roles[0].Replicas = ptr.To[int32](4) })
		Eventually(func() []int { return getMembers(setKey) }, timeout, interval).Should(Equal([]int{0, 1, 2, 4}))
		for _, ordinal := range []int{0, 2, 4} {
			name := fmt.Sprintf("%s-%d", setKey.Name, ordinal)
			Expect(instances(group.Name)[name]).To(Equal(original[name]))
		}
	}, Entry("standalone", false), Entry("leader and all workers", true))

	It("reports invalid bypassed input and never falls back to default scale-down", func() {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(3).Obj()
		group := wrappers.BuildBasicRoleBasedGroup("invalid", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())
		key := client.ObjectKeyFromObject(group)
		Eventually(func() int { return len(instances(group.Name)) }, timeout, interval).Should(Equal(3))
		original := instances(group.Name)
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) {
			g.Spec.Roles[0].Replicas = ptr.To[int32](2)
			g.Spec.Roles[0].Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "2-1"}
		})
		Eventually(func() string {
			latest := &workloadsv1alpha2.RoleBasedGroup{}
			if testutil.K8sClient.Get(testutil.Ctx, key, latest) != nil {
				return ""
			}
			condition := apimeta.FindStatusCondition(latest.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupReady))
			if condition == nil {
				return ""
			}
			return condition.Reason
		}, timeout, interval).Should(Equal("InvalidExcludedOrdinals"))
		Consistently(func() map[string]types.UID { return instances(group.Name) }, time.Second, interval).Should(Equal(original))
		set := &workloadsv1alpha2.RoleInstanceSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&role)}, set)).To(Succeed())
		Expect(*set.Spec.Replicas).To(Equal(int32(3)))
	})

	It("creates initial sparse members and recovers a missing member at its committed ordinal", func() {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(3).Obj()
		role.Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1,3"}
		group := wrappers.BuildBasicRoleBasedGroup("recover", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())
		setKey := client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&role)}
		Eventually(func() []string { return names(instances(group.Name)) }, timeout, interval).Should(Equal([]string{setKey.Name + "-0", setKey.Name + "-2", setKey.Name + "-4"}))
		old := &workloadsv1alpha2.RoleInstance{}
		instanceKey := client.ObjectKey{Namespace: namespace, Name: setKey.Name + "-4"}
		Expect(testutil.K8sClient.Get(testutil.Ctx, instanceKey, old)).To(Succeed())
		pods := &corev1.PodList{}
		Expect(testutil.K8sClient.List(testutil.Ctx, pods, client.InNamespace(namespace), client.MatchingLabels{constants.RoleInstanceNameLabelKey: old.Name})).To(Succeed())
		Expect(testutil.K8sClient.Delete(testutil.Ctx, old, client.Preconditions{UID: &old.UID})).To(Succeed())
		// envtest has no garbage collector; explicitly remove old-owner Pods only.
		for i := range pods.Items {
			pod := &pods.Items[i]
			err := testutil.K8sClient.Delete(testutil.Ctx, pod, client.Preconditions{UID: &pod.UID})
			Expect(client.IgnoreNotFound(err)).To(Succeed())
		}
		Eventually(func() (types.UID, error) {
			replacement := &workloadsv1alpha2.RoleInstance{}
			err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, replacement)
			if apierrors.IsNotFound(err) {
				return old.UID, nil
			}
			return replacement.UID, err
		}, timeout, interval).ShouldNot(Equal(old.UID))
		Expect(getMembers(setKey)).To(Equal([]int{0, 2, 4}))
		// Internal keys and user E must never leak into Pod metadata.
		allPods := &corev1.PodList{}
		Expect(testutil.K8sClient.List(testutil.Ctx, allPods, client.InNamespace(namespace))).To(Succeed())
		for _, pod := range allPods.Items {
			Expect(pod.Annotations).NotTo(HaveKey(constants.ExcludedOrdinalsAnnotationKey))
			Expect(pod.Annotations).NotTo(HaveKey(constants.CommittedSkipOrdinalsAnnotationKey))
		}
	})
	It("uses the Scaling Adapter scale API without changing replica ownership", func() {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(3).Obj()
		role.ScalingAdapter = &workloadsv1alpha2.ScalingAdapter{Enable: true}
		group := wrappers.BuildBasicRoleBasedGroup("autoscale", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())
		key := client.ObjectKeyFromObject(group)
		setKey := client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&role)}
		Eventually(func() []int { return getMembers(setKey) }, timeout, interval).Should(Equal([]int{0, 1, 2}))
		adapterKey := client.ObjectKey{Namespace: namespace, Name: workloadsv1alpha2.GenerateScalingAdapterName(group.Name, role.Name)}
		adapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
		Eventually(func() (constants.AdapterPhase, error) {
			err := testutil.K8sClient.Get(testutil.Ctx, adapterKey, adapter)
			return adapter.Status.Phase, err
		}, timeout, interval).Should(Equal(constants.AdapterPhaseBound))
		// Keep exclusions as an independent role input; the autoscaler owns count.
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) {
			g.Spec.Roles[0].Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1"}
		})
		scaleTo := func(count int32) {
			Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
				current := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
				if err := testutil.K8sClient.Get(testutil.Ctx, adapterKey, current); err != nil {
					return err
				}
				body := &autoscalingv1.Scale{ObjectMeta: metav1.ObjectMeta{Name: current.Name, Namespace: namespace, ResourceVersion: current.ResourceVersion}, Spec: autoscalingv1.ScaleSpec{Replicas: count}}
				return testutil.K8sClient.SubResource("scale").Update(testutil.Ctx, current, client.WithSubResourceBody(body))
			})).To(Succeed())
		}
		scaleTo(2)
		Eventually(func() []int { return getMembers(setKey) }, timeout, interval).Should(Equal([]int{0, 2}))
		// Only commitment, not the old instance's physical retirement, is needed.
		scaleTo(3)
		Eventually(func() []int { return getMembers(setKey) }, timeout, interval).Should(Equal([]int{0, 2, 3}))
	})

	It("pauses and resumes a sparse canary using a ready surge before retiring a base member", func() {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(3).Obj()
		role.Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1,3"}
		role.RolloutStrategy.RollingUpdate = &workloadsv1alpha2.RollingUpdate{Type: workloadsv1alpha2.RecreatePodUpdateStrategyType, Partition: ptr.To(intstr.FromInt32(2)), MaxSurge: ptr.To(intstr.FromInt32(1)), MaxUnavailable: ptr.To(intstr.FromInt32(0))}
		group := wrappers.BuildBasicRoleBasedGroup("canary", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())
		key := client.ObjectKeyFromObject(group)
		setKey := client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&role)}
		// envtest has neither kubelet nor GC. Supply runtime readiness and delete
		// only Pods whose RI owner UID has disappeared, leaving controller decisions real.
		markPodsReady := func() error {
			pods := &corev1.PodList{}
			if err := testutil.K8sClient.List(testutil.Ctx, pods, client.InNamespace(namespace), client.MatchingLabels{constants.GroupNameLabelKey: group.Name}); err != nil {
				return err
			}
			for i := range pods.Items {
				pod := &pods.Items[i]
				if owner := metav1.GetControllerOf(pod); owner != nil && owner.Kind == "RoleInstance" {
					instance := &workloadsv1alpha2.RoleInstance{}
					err := testutil.K8sClient.Get(testutil.Ctx, client.ObjectKey{Namespace: namespace, Name: owner.Name}, instance)
					if apierrors.IsNotFound(err) || (err == nil && instance.UID != owner.UID) {
						if err := client.IgnoreNotFound(testutil.K8sClient.Delete(testutil.Ctx, pod, client.Preconditions{UID: &pod.UID})); err != nil {
							return err
						}
						continue
					}
					if err != nil {
						return err
					}
				}
				// Preserve lifecycle/update readiness gate conditions the controllers write.
				conditions := pod.Status.Conditions
				testutil.SetPodRunningAndReady(pod)
				for _, condition := range conditions {
					if condition.Type != corev1.PodReady && condition.Type != corev1.ContainersReady {
						pod.Status.Conditions = append(pod.Status.Conditions, condition)
					}
				}
				if err := testutil.K8sClient.Status().Update(testutil.Ctx, pod); err != nil {
					return err
				}
			}
			return nil
		}
		Eventually(func() (int32, error) {
			if err := markPodsReady(); err != nil {
				return 0, err
			}
			set := &workloadsv1alpha2.RoleInstanceSet{}
			err := testutil.K8sClient.Get(testutil.Ctx, setKey, set)
			return set.Status.ReadyReplicas, err
		}, timeout, interval).Should(Equal(int32(3)))
		original := instances(group.Name)
		oldRevision := ""
		set := &workloadsv1alpha2.RoleInstanceSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, set)).To(Succeed())
		oldRevision = set.Status.CurrentRevision
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) {
			g.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Image = "nginx:2.0"
		})
		Eventually(func() bool { _, exists := instances(group.Name)[setKey.Name+"-5"]; return exists }, timeout, interval).Should(BeTrue())
		Consistently(func() types.UID { return instances(group.Name)[setKey.Name+"-4"] }, time.Second, interval).Should(Equal(original[setKey.Name+"-4"]))
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) { g.Spec.Roles[0].RolloutStrategy.RollingUpdate.Paused = true })
		Eventually(func() bool {
			current := &workloadsv1alpha2.RoleInstanceSet{}
			if testutil.K8sClient.Get(testutil.Ctx, setKey, current) != nil {
				return false
			}
			return current.Spec.UpdateStrategy.Paused
		}, timeout, interval).Should(BeTrue())
		Expect(markPodsReady()).To(Succeed())
		Consistently(func() types.UID { return instances(group.Name)[setKey.Name+"-4"] }, time.Second, interval).Should(Equal(original[setKey.Name+"-4"]))
		Expect(instances(group.Name)).To(HaveKey(setKey.Name + "-5"))
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) {
			g.Spec.Roles[0].RolloutStrategy.RollingUpdate.Paused = false
		})
		Eventually(func() (bool, error) {
			if err := markPodsReady(); err != nil {
				return false, err
			}
			current := &workloadsv1alpha2.RoleInstanceSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, current); err != nil {
				return false, err
			}
			candidate := &workloadsv1alpha2.RoleInstance{}
			if err := testutil.K8sClient.Get(testutil.Ctx, client.ObjectKey{Namespace: namespace, Name: setKey.Name + "-4"}, candidate); err != nil {
				return false, err
			}
			return candidate.UID != original[candidate.Name] && candidate.Labels[apps.ControllerRevisionHashLabelKey] == current.Status.UpdateRevision && current.Status.ReadyReplicas == 3 && len(instances(group.Name)) == 3, nil
		}, timeout, interval).Should(BeTrue())
		for _, ordinal := range []int{0, 2} {
			name := fmt.Sprintf("%s-%d", setKey.Name, ordinal)
			Expect(instances(group.Name)[name]).To(Equal(original[name]))
			protected := &workloadsv1alpha2.RoleInstance{}
			Expect(testutil.K8sClient.Get(testutil.Ctx, client.ObjectKey{Namespace: namespace, Name: name}, protected)).To(Succeed())
			Expect(protected.Labels[apps.ControllerRevisionHashLabelKey]).To(Equal(oldRevision))
		}
		Expect(getMembers(setKey)).To(Equal([]int{0, 2, 4}))
	})

})

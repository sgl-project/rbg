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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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
	"sigs.k8s.io/rbgs/pkg/discovery"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappers "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
	"sigs.k8s.io/yaml"
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
		result := make([]string, 0, len(values))
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
			values := make([]string, 0, len(list.Items))
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
		// Status SSA in the real controller must not turn an unchanged spec
		// into a failed membership commit and a workqueue warning.
		Consistently(func() []string {
			events := &corev1.EventList{}
			Expect(testutil.K8sClient.List(testutil.Ctx, events, client.InNamespace(namespace))).To(Succeed())
			warnings := make([]string, 0, len(events.Items))
			for _, event := range events.Items {
				if event.InvolvedObject.UID == group.UID && event.Reason == "FailedReconcileWorkload" &&
					strings.Contains(event.Message, "role configuration changed before membership commitment") {
					warnings = append(warnings, event.Message)
				}
			}
			return warnings
		}, time.Second, interval).Should(BeEmpty())
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

	It("adopts a dense orphan through refined discovery after recreating its RBG", func() {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(5).Obj()
		group := wrappers.BuildBasicRoleBasedGroup("orphan", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())
		key := client.ObjectKeyFromObject(group)
		setKey := client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&role)}
		Eventually(func() int { return len(instances(group.Name)) }, timeout, interval).Should(Equal(5))
		original := instances(group.Name)
		dense := &workloadsv1alpha2.RoleInstanceSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, dense)).To(Succeed())
		Expect(dense.Annotations[constants.CommittedSkipOrdinalsAnnotationKey]).To(BeEmpty())
		oldUID := group.UID
		Expect(testutil.K8sClient.Delete(testutil.Ctx, group)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(testutil.K8sClient.Get(testutil.Ctx, key, &workloadsv1alpha2.RoleBasedGroup{}))
		}, timeout, interval).Should(BeTrue())
		// Envtest has no garbage collector. After the parent is gone, model its
		// orphan policy by removing only references to that parent's UID.
		orphan := func(obj client.Object) {
			refs := obj.GetOwnerReferences()
			kept := make([]metav1.OwnerReference, 0, len(refs))
			for _, ref := range refs {
				if ref.UID != oldUID {
					kept = append(kept, ref)
				}
			}
			before := obj.DeepCopyObject().(client.Object)
			obj.SetOwnerReferences(kept)
			Expect(testutil.K8sClient.Patch(testutil.Ctx, obj, client.MergeFrom(before))).To(Succeed())
		}
		Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, dense)).To(Succeed())
		orphan(dense)
		services := &corev1.ServiceList{}
		Expect(testutil.K8sClient.List(testutil.Ctx, services, client.InNamespace(namespace))).To(Succeed())
		for i := range services.Items {
			orphan(&services.Items[i])
		}
		configMap := &corev1.ConfigMap{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, key, configMap)).To(Succeed())
		orphan(configMap)
		revisions := &apps.ControllerRevisionList{}
		Expect(testutil.K8sClient.List(testutil.Ctx, revisions, client.InNamespace(namespace))).To(Succeed())
		for i := range revisions.Items {
			orphan(&revisions.Items[i])
		}
		role.Replicas = ptr.To[int32](3)
		role.Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1,3"}
		group = wrappers.BuildBasicRoleBasedGroup("orphan", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())
		Expect(group.UID).NotTo(Equal(oldUID))
		Eventually(func(g Gomega) {
			adopted := &workloadsv1alpha2.RoleInstanceSet{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, adopted)).To(Succeed())
			g.Expect(adopted.UID).To(Equal(dense.UID))
			owner := metav1.GetControllerOf(adopted)
			g.Expect(owner).NotTo(BeNil())
			g.Expect(owner.UID).To(Equal(group.UID))
			g.Expect(getMembers(setKey)).To(Equal([]int{0, 2, 4}))
		}, timeout, interval).Should(Succeed())
		Eventually(func() []string { return names(instances(group.Name)) }, timeout, interval).Should(Equal([]string{setKey.Name + "-0", setKey.Name + "-2", setKey.Name + "-4"}))
		for _, ordinal := range []int{0, 2, 4} {
			name := fmt.Sprintf("%s-%d", setKey.Name, ordinal)
			Expect(instances(group.Name)[name]).To(Equal(original[name]))
		}
		Eventually(func(g Gomega) {
			latest := &workloadsv1alpha2.RoleBasedGroup{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, latest)).To(Succeed())
			g.Expect(latest.GetDiscoveryConfigMode()).To(Equal(constants.RefineDiscoveryConfigMode))
			cm := &corev1.ConfigMap{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, cm)).To(Succeed())
			owner := metav1.GetControllerOf(cm)
			g.Expect(owner).NotTo(BeNil())
			g.Expect(owner.UID).To(Equal(group.UID))
			var config discovery.ClusterConfig
			g.Expect(yaml.UnmarshalStrict([]byte(cm.Data["config.yaml"]), &config)).To(Succeed())
			g.Expect(config.Roles["worker"].Instances).To(Equal([]discovery.Instance{
				{Address: setKey.Name + "-0.s-" + setKey.Name},
				{Address: setKey.Name + "-2.s-" + setKey.Name},
				{Address: setKey.Name + "-4.s-" + setKey.Name},
			}))
		}, timeout, interval).Should(Succeed())
	})

	It("isolates invalid bypassed input while reconciling other roles and cleaning removed roles", func() {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(3).Obj()
		healthy := wrappers.BuildStandaloneRole("healthy").WithReplicas(1).Obj()
		removed := wrappers.BuildStandaloneRole("obsolete").WithReplicas(1).Obj()
		group := wrappers.BuildBasicRoleBasedGroup("invalid", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role, healthy, removed}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())
		key := client.ObjectKeyFromObject(group)
		setKey := client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&role)}
		healthyKey := client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&healthy)}
		removedKey := client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&removed)}
		Eventually(func() int { return len(instances(group.Name)) }, timeout, interval).Should(Equal(5))
		original := instances(group.Name)
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) {
			g.Spec.Roles = g.Spec.Roles[:2]
			g.Spec.Roles[0].Replicas = ptr.To[int32](2)
			g.Spec.Roles[0].Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "2-1"}
			g.Spec.Roles[1].Replicas = ptr.To[int32](2)
			g.Spec.Roles[1].Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1"}
			fresh := wrappers.BuildStandaloneRole("fresh").WithReplicas(1).Obj()
			g.Spec.Roles = append(g.Spec.Roles, fresh)
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
		Eventually(func() []int { return getMembers(healthyKey) }, timeout, interval).Should(Equal([]int{0, 2}))
		freshKey := client.ObjectKey{Namespace: namespace, Name: group.Name + "-fresh"}
		Eventually(func() []int { return getMembers(freshKey) }, timeout, interval).Should(Equal([]int{0}))
		Eventually(func() bool {
			return apierrors.IsNotFound(testutil.K8sClient.Get(testutil.Ctx, removedKey, &workloadsv1alpha2.RoleInstanceSet{}))
		}, timeout, interval).Should(BeTrue())
		Consistently(func(g Gomega) {
			g.Expect(getMembers(setKey)).To(Equal([]int{0, 1, 2}))
			for _, ordinal := range []int{0, 1, 2} {
				name := fmt.Sprintf("%s-%d", setKey.Name, ordinal)
				g.Expect(instances(group.Name)[name]).To(Equal(original[name]))
			}
		}, time.Second, interval).Should(Succeed())
		Eventually(func(g Gomega) {
			cm := &corev1.ConfigMap{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, cm)).To(Succeed())
			var config discovery.ClusterConfig
			g.Expect(yaml.UnmarshalStrict([]byte(cm.Data["config.yaml"]), &config)).To(Succeed())
			g.Expect(config.Roles["worker"].Size).To(Equal(3), "invalid input must retain committed discovery")
			g.Expect(config.Roles["healthy"].Size).To(Equal(2))
			g.Expect(config.Roles).NotTo(HaveKey("obsolete"))
			g.Expect(config.Roles["fresh"].Size).To(Equal(1))
		}, timeout, interval).Should(Succeed())
	})

	It("does not create an invalid initial role and recovers after its input is fixed", func() {
		invalid := wrappers.BuildStandaloneRole("invalid").WithReplicas(2).Obj()
		invalid.Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "2-1"}
		valid := wrappers.BuildStandaloneRole("valid").WithReplicas(1).Obj()
		group := wrappers.BuildBasicRoleBasedGroup("initial-invalid", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{invalid, valid}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())
		key := client.ObjectKeyFromObject(group)
		invalidKey := client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&invalid)}
		validKey := client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&valid)}
		Eventually(func() []int { return getMembers(validKey) }, timeout, interval).Should(Equal([]int{0}))
		Consistently(func() bool {
			return apierrors.IsNotFound(testutil.K8sClient.Get(testutil.Ctx, invalidKey, &workloadsv1alpha2.RoleInstanceSet{}))
		}, time.Second, interval).Should(BeTrue())
		Eventually(func(g Gomega) {
			cm := &corev1.ConfigMap{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, cm)).To(Succeed())
			var config discovery.ClusterConfig
			g.Expect(yaml.UnmarshalStrict([]byte(cm.Data["config.yaml"]), &config)).To(Succeed())
			g.Expect(config.Roles["invalid"].Size).To(BeZero())
			g.Expect(config.Roles["valid"].Size).To(Equal(1))
		}, timeout, interval).Should(Succeed())
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) {
			g.Spec.Roles[0].Annotations[constants.ExcludedOrdinalsAnnotationKey] = "1"
		})
		Eventually(func() []int { return getMembers(invalidKey) }, timeout, interval).Should(Equal([]int{0, 2}))
		Eventually(func(g Gomega) {
			latest := &workloadsv1alpha2.RoleBasedGroup{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, latest)).To(Succeed())
			condition := apimeta.FindStatusCondition(latest.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupReady))
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.ObservedGeneration).To(Equal(latest.Generation))
			g.Expect(condition.Reason).NotTo(Equal("InvalidExcludedOrdinals"))
		}, timeout, interval).Should(Succeed())
	})

	DescribeTable("recovers a corrupt record only after an explicit guarded operator repair", func(sparse bool) {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(3).Obj()
		members := []int{0, 1, 2}
		record := ""
		if sparse {
			role.Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1,3"}
			members, record = []int{0, 2, 4}, "1,3"
		}
		group := wrappers.BuildBasicRoleBasedGroup("repair", namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())
		key := client.ObjectKeyFromObject(group)
		setKey := client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&role)}
		Eventually(func() []int { return getMembers(setKey) }, timeout, interval).Should(Equal(members))
		Eventually(func() int { return len(instances(group.Name)) }, timeout, interval).Should(Equal(3))
		original := instances(group.Name)
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			set := &workloadsv1alpha2.RoleInstanceSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, set); err != nil {
				return err
			}
			before := set.DeepCopy()
			if set.Annotations == nil {
				set.Annotations = map[string]string{}
			}
			set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] = "3-1"
			return testutil.K8sClient.Patch(testutil.Ctx, set, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
		})).To(Succeed())
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) {
			delete(g.Spec.Roles[0].Annotations, constants.ExcludedOrdinalsAnnotationKey)
		})
		Eventually(func() bool {
			set := &workloadsv1alpha2.RoleInstanceSet{}
			if testutil.K8sClient.Get(testutil.Ctx, setKey, set) != nil {
				return false
			}
			for _, condition := range set.Status.Conditions {
				// KEP-502 reports the configuration-family blocker through the
				// Progressing condition instead of the legacy FailedScale marker.
				if condition.Type == workloadsv1alpha2.RoleInstanceSetProgressing &&
					condition.Status == corev1.ConditionTrue &&
					condition.Reason == constants.ProgressingReasonInvalidSpec {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())
		Consistently(func() map[string]types.UID { return instances(group.Name) }, time.Second, interval).Should(Equal(original))
		// The runbook's guards must fail closed without changing the corrupt record.
		for _, guard := range []map[string]interface{}{
			{"op": "test", "path": "/metadata/uid", "value": "wrong-uid"},
			{"op": "test", "path": "/metadata/resourceVersion", "value": "stale"},
			{"op": "test", "path": "/spec/replicas", "value": 4},
		} {
			set := &workloadsv1alpha2.RoleInstanceSet{}
			Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, set)).To(Succeed())
			raw, err := json.Marshal([]map[string]interface{}{guard, {"op": "remove", "path": "/metadata/annotations/rbg.workloads.x-k8s.io~1committed-skip-ordinals"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(testutil.K8sClient.Patch(testutil.Ctx, set, client.RawPatch(types.JSONPatchType, raw))).NotTo(Succeed())
			Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, set)).To(Succeed())
			Expect(set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey]).To(Equal("3-1"))
		}
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			set := &workloadsv1alpha2.RoleInstanceSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, set); err != nil {
				return err
			}
			repair := map[string]interface{}{"op": "remove", "path": "/metadata/annotations/rbg.workloads.x-k8s.io~1committed-skip-ordinals"}
			if sparse {
				repair["op"], repair["value"] = "replace", record
			}
			raw, err := json.Marshal([]map[string]interface{}{
				{"op": "test", "path": "/metadata/uid", "value": set.UID},
				{"op": "test", "path": "/metadata/resourceVersion", "value": set.ResourceVersion},
				{"op": "test", "path": "/spec/replicas", "value": 3},
				repair,
			})
			if err != nil {
				return err
			}
			return testutil.K8sClient.Patch(testutil.Ctx, set, client.RawPatch(types.JSONPatchType, raw))
		})).To(Succeed())
		Eventually(func(g Gomega) {
			set := &workloadsv1alpha2.RoleInstanceSet{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, set)).To(Succeed())
			g.Expect(getMembers(setKey)).To(Equal(members))
			g.Expect(set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey]).To(Equal(record))
			g.Expect(set.Annotations).NotTo(HaveKey(constants.ExcludedOrdinalsAnnotationKey))
			for _, condition := range set.Status.Conditions {
				g.Expect(condition.Reason).NotTo(Equal("InvalidCommittedOrdinals"))
				g.Expect(condition.Reason).NotTo(Equal(constants.ProgressingReasonInvalidSpec))
			}
			g.Expect(instances(group.Name)).To(Equal(original))
			cm := &corev1.ConfigMap{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, cm)).To(Succeed())
			var config discovery.ClusterConfig
			g.Expect(yaml.UnmarshalStrict([]byte(cm.Data["config.yaml"]), &config)).To(Succeed())
			addresses := make([]discovery.Instance, 0, len(members))
			for _, ordinal := range members {
				addresses = append(addresses, discovery.Instance{Address: fmt.Sprintf("%s-%d.s-%s", setKey.Name, ordinal, setKey.Name)})
			}
			g.Expect(config.Roles["worker"].Instances).To(Equal(addresses))
		}, timeout, interval).Should(Succeed())
	}, Entry("restore a verified sparse target", true), Entry("remove the record for a verified dense target", false))

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

	It("keeps surge off excluded ordinals and preserves its identity across pause and input edits", func() {
		role := wrappers.BuildStandaloneRole("worker").WithReplicas(3).Obj()
		role.Annotations = map[string]string{constants.ExcludedOrdinalsAnnotationKey: "1,3,5"}
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
		Eventually(func() bool { _, exists := instances(group.Name)[setKey.Name+"-6"]; return exists }, timeout, interval).Should(BeTrue())
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
		Expect(instances(group.Name)).To(HaveKey(setKey.Name + "-6"))
		Expect(instances(group.Name)).NotTo(HaveKey(setKey.Name + "-5"))
		surgeUID := instances(group.Name)[setKey.Name+"-6"]
		update(key, func(g *workloadsv1alpha2.RoleBasedGroup) {
			g.Spec.Roles[0].Annotations[constants.ExcludedOrdinalsAnnotationKey] = "1,3,5,6"
		})
		Eventually(func() string {
			current := &workloadsv1alpha2.RoleInstanceSet{}
			if testutil.K8sClient.Get(testutil.Ctx, setKey, current) != nil {
				return ""
			}
			return current.Annotations[constants.ExcludedOrdinalsAnnotationKey]
		}, timeout, interval).Should(Equal("1,3,5,6"))
		Consistently(func() types.UID { return instances(group.Name)[setKey.Name+"-6"] }, time.Second, interval).Should(Equal(surgeUID))
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

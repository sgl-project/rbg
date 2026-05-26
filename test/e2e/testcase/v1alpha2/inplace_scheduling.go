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

package v1alpha2

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// RunInplaceSchedulingTestCases verifies that the in-place scheduling feature
// correctly injects nodeAffinity into Pods after a rolling upgrade.
//
// This test uses two roles in a single RBG:
//   - role-pod:      Stateful (Standalone) → Pod-level granularity (default)
//   - role-component: Stateless (Standalone with pattern=Stateless) → Component-level granularity (default)
//
// Both roles use Preferred mode with maxSurge=0, maxUnavailable=1 (delete-first).
// After the rolling upgrade completes, the test asserts that:
//  1. Pods of both roles carry preferredDuringScheduling nodeAffinity
//  2. The affinity references kubernetes.io/hostname with the correct node name
//  3. Both roles reference the same node (single-node cluster)
func RunInplaceSchedulingTestCases(f *framework.Framework) {
	ginkgo.Describe("inplace-scheduling", func() {

		ginkgo.It("injects nodeAffinity after rolling upgrade for both Pod and Component granularity", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-scheduling", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					// Role 1: Stateful → Pod-level granularity (smart default)
					wrappersv2.BuildStandaloneRole("role-pod").
						WithReplicas(2).
						WithAnnotations(map[string]string{
							constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingPreferred,
						}).
						WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
							MaxSurge:       ptr.To(intstr.FromInt32(0)),
							MaxUnavailable: ptr.To(intstr.FromInt32(1)),
						}).Obj(),

					// Role 2: Stateless → Component-level granularity (smart default)
					wrappersv2.BuildStandaloneRole("role-component").
						WithReplicas(2).
						WithAnnotations(map[string]string{
							constants.RoleInstancePatternKey:             string(constants.StatelessPattern),
							constants.RoleInplaceSchedulingAnnotationKey: constants.InplaceSchedulingPreferred,
						}).
						WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
							MaxSurge:       ptr.To(intstr.FromInt32(0)),
							MaxUnavailable: ptr.To(intstr.FromInt32(1)),
						}).Obj(),
				}).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Both roles should have 2 Ready pods initially.
			gomega.Expect(getPodUIDsForRole(f, rbg, "role-pod")).Should(gomega.HaveLen(2))
			gomega.Expect(getPodUIDsForRole(f, rbg, "role-component")).Should(gomega.HaveLen(2))

			// Record initial pod UIDs for both roles.
			initialPodUIDs := getPodUIDsForRole(f, rbg, "role-pod")
			initialComponentPodUIDs := getPodUIDsForRole(f, rbg, "role-component")

			// Trigger rolling upgrade by adding an env var to both roles.
			// Env var changes cannot be applied in-place, forcing pod recreation.
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				for i := range rbg.Spec.Roles {
					rbg.Spec.Roles[i].StandalonePattern.Template.Spec.Containers[0].Env = []corev1.EnvVar{
						{Name: "INPLACE_E2E", Value: "v2"},
					}
				}
			})

			// Wait for rolling upgrade to complete.
			f.ExpectRbgV2Equal(rbg)

			// Verify pods were recreated (UIDs changed).
			var updatedPodUIDs map[string]types.UID
			gomega.Eventually(func() bool {
				updatedPodUIDs = getPodUIDsForRole(f, rbg, "role-pod")
				if len(updatedPodUIDs) != 2 {
					return false
				}
				for name := range updatedPodUIDs {
					if initialPodUIDs[name] == updatedPodUIDs[name] {
						return false
					}
				}
				return true
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"role-pod pods should be recreated with new UIDs")

			gomega.Eventually(func() bool {
				componentUIDs := getPodUIDsForRole(f, rbg, "role-component")
				if len(componentUIDs) != 2 {
					return false
				}
				for name := range componentUIDs {
					if initialComponentPodUIDs[name] == componentUIDs[name] {
						return false
					}
				}
				return true
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"role-component pods should be recreated with new UIDs")

			// ---------------------------------------------------------------
			// Assert nodeAffinity injection
			// ---------------------------------------------------------------

			// Get pods for both roles.
			rolePods := listPodsForRole(f, rbg, "role-pod")
			componentPods := listPodsForRole(f, rbg, "role-component")

			gomega.Expect(rolePods).Should(gomega.HaveLen(2))
			gomega.Expect(componentPods).Should(gomega.HaveLen(2))

			// Verify role-pod (Pod-level granularity): each Pod has preferred affinity
			// and the affinity includes the node it's actually running on.
			for _, pod := range rolePods {
				gomega.Expect(pod.Spec.NodeName).ShouldNot(gomega.BeEmpty(),
					"pod %s should be assigned to a node", pod.Name)
				assertAffinityContainsNode(pod, pod.Spec.NodeName, "role-pod")
			}

			// Verify role-component (Component-level granularity): each Pod has preferred affinity
			// and the affinity includes the node it's actually running on.
			for _, pod := range componentPods {
				gomega.Expect(pod.Spec.NodeName).ShouldNot(gomega.BeEmpty(),
					"pod %s should be assigned to a node", pod.Name)
				assertAffinityContainsNode(pod, pod.Spec.NodeName, "role-component")
			}
		})

		ginkgo.It("injects avoid anti-affinity and preferred affinity after pod deletion", func() {
			const avoidLabelKey = "workloads.x-k8s.io/heavy-tenant"

			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-avoid", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-avoid").
						WithReplicas(1).
						WithAnnotations(map[string]string{
							constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingPreferred,
							constants.RoleInplaceSchedulingAvoidAnnotationKey: avoidLabelKey,
						}).Obj(),
				}).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Should have 1 Ready pod.
			initialUIDs := getPodUIDsForRole(f, rbg, "role-avoid")
			gomega.Expect(initialUIDs).Should(gomega.HaveLen(1))

			// Delete the pod to trigger recreation.
			pods := listPodsForRole(f, rbg, "role-avoid")
			gomega.Expect(pods).Should(gomega.HaveLen(1))
			gomega.Expect(f.Client.Delete(f.Ctx, &pods[0])).Should(gomega.Succeed())

			// Wait for the new pod to be created with a different UID.
			gomega.Eventually(func() bool {
				newUIDs := getPodUIDsForRole(f, rbg, "role-avoid")
				if len(newUIDs) != 1 {
					return false
				}
				for name, uid := range newUIDs {
					if initialUIDs[name] == uid {
						return false
					}
				}
				return true
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"pod should be recreated with a new UID after deletion")

			// Wait for the new pod to be Running+Ready.
			f.ExpectRbgV2Equal(rbg)

			// ---------------------------------------------------------------
			// Assert affinity injection on the new pod
			// ---------------------------------------------------------------
			newPods := listPodsForRole(f, rbg, "role-avoid")
			gomega.Expect(newPods).Should(gomega.HaveLen(1))
			newPod := newPods[0]

			gomega.Expect(newPod.Spec.NodeName).ShouldNot(gomega.BeEmpty())

			na := newPod.Spec.Affinity.NodeAffinity
			gomega.Expect(na).ShouldNot(gomega.BeNil(),
				"pod %s should have nodeAffinity injected", newPod.Name)

			// 1. Verify preferred affinity (from node binding)
			assertAffinityContainsNode(newPod, newPod.Spec.NodeName, "role-avoid")

			// 2. Verify avoid anti-affinity (required + DoesNotExist)
			req := na.RequiredDuringSchedulingIgnoredDuringExecution
			gomega.Expect(req).ShouldNot(gomega.BeNil(),
				"pod %s should have required anti-affinity injected", newPod.Name)

			var avoidFound bool
			for _, term := range req.NodeSelectorTerms {
				for _, expr := range term.MatchExpressions {
					if expr.Key == avoidLabelKey &&
						expr.Operator == corev1.NodeSelectorOpDoesNotExist {
						avoidFound = true
						break
					}
				}
				if avoidFound {
					break
				}
			}
			gomega.Expect(avoidFound).Should(gomega.BeTrue(),
				"pod %s should have required anti-affinity with key=%s and operator=DoesNotExist, got: %+v",
				newPod.Name, avoidLabelKey, req.NodeSelectorTerms)
		})

		ginkgo.It("Required mode with avoid: pod stays Pending when historical node carries avoid label", func() {
			const avoidLabelKey = "workloads.x-k8s.io/heavy-tenant"

			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-required-avoid", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-req-avoid").
						WithReplicas(1).
						WithAnnotations(map[string]string{
							constants.RoleInplaceSchedulingAnnotationKey:      constants.InplaceSchedulingRequired,
							constants.RoleInplaceSchedulingAvoidAnnotationKey: avoidLabelKey,
						}).Obj(),
				}).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Should have 1 Ready pod.
			initialUIDs := getPodUIDsForRole(f, rbg, "role-req-avoid")
			gomega.Expect(initialUIDs).Should(gomega.HaveLen(1))

			// Record the node name where the pod is running.
			pods := listPodsForRole(f, rbg, "role-req-avoid")
			gomega.Expect(pods).Should(gomega.HaveLen(1))
			originalNode := pods[0].Spec.NodeName
			gomega.Expect(originalNode).ShouldNot(gomega.BeEmpty())

			// Label the original node with the avoid key so the DoesNotExist
			// constraint will reject it.
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: originalNode}}
			gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(node), node)).Should(gomega.Succeed())
			mergeFrom := client.MergeFrom(node.DeepCopy())
			if node.Labels == nil {
				node.Labels = make(map[string]string)
			}
			node.Labels[avoidLabelKey] = "true"
			gomega.Expect(f.Client.Patch(f.Ctx, node, mergeFrom)).Should(gomega.Succeed())

			// Ensure the label is removed after the test regardless of outcome.
			// Use DeferCleanup instead of defer so it runs in Ginkgo's cleanup
			// phase, which is more robust against panics and context cancellation.
			ginkgo.DeferCleanup(func(ctx context.Context) {
				freshNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: originalNode}}
				if err := f.Client.Get(ctx, client.ObjectKeyFromObject(freshNode), freshNode); err == nil {
					cleanMerge := client.MergeFrom(freshNode.DeepCopy())
					delete(freshNode.Labels, avoidLabelKey)
					_ = f.Client.Patch(ctx, freshNode, cleanMerge)
				}
			})

			// Delete the pod to trigger recreation.
			gomega.Expect(f.Client.Delete(f.Ctx, &pods[0])).Should(gomega.Succeed())

			// Wait for the new pod to be created with a different UID.
			gomega.Eventually(func() bool {
				newUIDs := getPodUIDsForRole(f, rbg, "role-req-avoid")
				for name, uid := range newUIDs {
					if initialUIDs[name] != uid {
						return true
					}
				}
				return false
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"a new pod with a different UID should be created")

			// ---------------------------------------------------------------
			// Assert: the new pod has merged Required affinity AND stays Pending
			// ---------------------------------------------------------------
			gomega.Eventually(func() bool {
				newPods := listPodsForRole(f, rbg, "role-req-avoid")
				for _, p := range newPods {
					if initialUIDs[p.Name] == p.UID {
						continue // skip the old pod being terminated
					}
					if p.Spec.Affinity == nil || p.Spec.Affinity.NodeAffinity == nil {
						return false
					}
					return true
				}
				return false
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"new pod should have affinity injected")

			newPods := listPodsForRole(f, rbg, "role-req-avoid")
			var newPod corev1.Pod
			for _, p := range newPods {
				if initialUIDs[p.Name] != p.UID {
					newPod = p
					break
				}
			}

			na := newPod.Spec.Affinity.NodeAffinity
			gomega.Expect(na).ShouldNot(gomega.BeNil())

			// Verify the merged Required term (hostname + avoid in one term).
			assertMergedRequiredAvoidTerm(na, originalNode, avoidLabelKey)

			// No preferred terms in Required mode.
			gomega.Expect(na.PreferredDuringSchedulingIgnoredDuringExecution).Should(gomega.BeEmpty())

			// Scheduling outcome: the pod must either be Pending (single-node
			// cluster) OR scheduled to a node DIFFERENT from the original.
			assertPodNotScheduledOnOriginalNode(f, newPod, originalNode)
		})
	})
}

// assertMergedRequiredAvoidTerm verifies that the NodeAffinity has exactly ONE
// NodeSelectorTerm containing TWO MatchExpressions (hostname In + avoid
// DoesNotExist) — AND semantics within a single term.
func assertMergedRequiredAvoidTerm(na *corev1.NodeAffinity, originalNode, avoidLabelKey string) {
	ginkgo.GinkgoHelper()

	req := na.RequiredDuringSchedulingIgnoredDuringExecution
	gomega.Expect(req).ShouldNot(gomega.BeNil())
	gomega.Expect(req.NodeSelectorTerms).Should(gomega.HaveLen(1),
		"Required mode + avoid must produce a single merged term (AND semantics)")

	exprs := req.NodeSelectorTerms[0].MatchExpressions
	gomega.Expect(exprs).Should(gomega.HaveLen(2))

	// hostname In [originalNode]
	gomega.Expect(exprs[0].Key).Should(gomega.Equal("kubernetes.io/hostname"))
	gomega.Expect(exprs[0].Operator).Should(gomega.Equal(corev1.NodeSelectorOpIn))
	gomega.Expect(exprs[0].Values).Should(gomega.ContainElement(originalNode))

	// avoid key DoesNotExist
	gomega.Expect(exprs[1].Key).Should(gomega.Equal(avoidLabelKey))
	gomega.Expect(exprs[1].Operator).Should(gomega.Equal(corev1.NodeSelectorOpDoesNotExist))
}

// assertPodNotScheduledOnOriginalNode waits for a stable period then verifies
// that the pod is either Pending or Running on a node different from
// originalNode.
func assertPodNotScheduledOnOriginalNode(f *framework.Framework, pod corev1.Pod, originalNode string) {
	ginkgo.GinkgoHelper()

	gomega.Consistently(func() bool {
		live := &corev1.Pod{}
		if err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(&pod), live); err != nil {
			return false
		}
		switch live.Status.Phase {
		case corev1.PodRunning:
			return live.Spec.NodeName != originalNode
		case corev1.PodPending:
			return true
		default:
			return true
		}
	}, "12s", "2s").Should(gomega.BeTrue(),
		"pod must either stay Pending or run on a node != %s", originalNode)

	live := &corev1.Pod{}
	gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(&pod), live)).Should(gomega.Succeed())
	if live.Status.Phase == corev1.PodRunning {
		gomega.Expect(live.Spec.NodeName).ShouldNot(gomega.Equal(originalNode),
			"pod must NOT be scheduled back to the original node which carries the avoid label")
	} else {
		gomega.Expect(live.Status.Phase).Should(gomega.Equal(corev1.PodPending),
			"pod should be either Pending or Running on a different node")
	}
}

// listPodsForRole returns the list of non-deleted Pods belonging to a role.
func listPodsForRole(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) []corev1.Pod {
	podList := &corev1.PodList{}
	err := f.Client.List(f.Ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  roleName,
		},
	)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	var result []corev1.Pod
	for _, pod := range podList.Items {
		if pod.DeletionTimestamp == nil {
			result = append(result, pod)
		}
	}
	return result
}

// assertAffinityContainsNode verifies that the Pod has a preferredDuringScheduling
// nodeAffinity term with weight=100, key=kubernetes.io/hostname, and that
// the specified node is present in the values list.
func assertAffinityContainsNode(pod corev1.Pod, expectedNode, roleName string) {
	ginkgo.GinkgoHelper()

	gomega.Expect(pod.Spec.Affinity).ShouldNot(gomega.BeNil(),
		"[%s] pod %s should have affinity injected", roleName, pod.Name)
	gomega.Expect(pod.Spec.Affinity.NodeAffinity).ShouldNot(gomega.BeNil(),
		"[%s] pod %s should have nodeAffinity injected", roleName, pod.Name)

	terms := pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	gomega.Expect(terms).ShouldNot(gomega.BeEmpty(),
		"[%s] pod %s should have preferred scheduling terms", roleName, pod.Name)

	// Find the in-place scheduling term (weight=100, key=kubernetes.io/hostname).
	var found bool
	for _, term := range terms {
		if term.Weight != 100 {
			continue
		}
		for _, expr := range term.Preference.MatchExpressions {
			if expr.Key == "kubernetes.io/hostname" &&
				expr.Operator == corev1.NodeSelectorOpIn {
				for _, v := range expr.Values {
					if v == expectedNode {
						found = true
						break
					}
				}
			}
			if found {
				break
			}
		}
		if found {
			break
		}
	}
	gomega.Expect(found).Should(gomega.BeTrue(),
		"[%s] pod %s should have preferred affinity including node %s, got: %+v",
		roleName, pod.Name, expectedNode, terms)
}

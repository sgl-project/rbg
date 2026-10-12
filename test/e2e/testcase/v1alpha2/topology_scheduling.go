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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

const (
	volcanoSchedulerName      = "volcano"
	topologyHyperNodeTierName = "rack"
)

// RunTopologySchedulingTestCases covers the KEP-473 happy path against a real
// Volcano scheduler. Unlike unit and envtest coverage, this spec exercises the
// deployed CRDs, controller RBAC, Volcano PodGroup schema, HyperNode informer,
// and the final pod-to-scheduler binding.
func RunTopologySchedulingTestCases(f *framework.Framework) {
	ginkgo.Describe("topology-aware scheduling [volcano]", ginkgo.Label("volcano"), func() {
		ginkgo.It("renders a topology constraint and schedules covered pods with Volcano", func() {
			rbgName := "e2e-topology-pd"
			ruleName := "topology-pd"
			hyperNodeName := "rbgs-e2e-topology-rack"

			nodes := &corev1.NodeList{}
			gomega.Expect(f.Client.List(f.Ctx, nodes)).Should(gomega.Succeed())
			gomega.Expect(nodes.Items).ShouldNot(gomega.BeEmpty())

			hyperNode := topologyHyperNode(hyperNodeName, nodes)
			gomega.Expect(f.Client.Create(f.Ctx, hyperNode)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				_ = f.Client.Delete(f.Ctx, hyperNode)
			})

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("prefill").WithReplicas(1).Obj(),
					wrappersv2.BuildStandaloneRole("decode").WithReplicas(1).Obj(),
				},
			).Obj()
			cpolicy := topologyPolicy(rbgName, f.Namespace, ruleName, []string{"prefill", "decode"})

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			ginkgo.By("waiting for the topology plan to translate successfully")
			gomega.Eventually(func() bool {
				current := &workloadsv1alpha2.RoleBasedGroup{}
				if err := f.Client.Get(
					f.Ctx, client.ObjectKey{Name: rbgName, Namespace: f.Namespace}, current,
				); err != nil {
					return false
				}
				condition := apimeta.FindStatusCondition(
					current.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupTopologyTranslated))
				return condition != nil &&
					condition.Status == metav1.ConditionTrue &&
					condition.Reason == "TopologyTranslated"
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"TopologyTranslated should become True after the plan is rendered")

			ginkgo.By("verifying the rendered Volcano PodGroup")
			podGroup := &unstructured.Unstructured{}
			gomega.Eventually(func() error {
				var err error
				podGroup, err = getTopologyPodGroup(f, f.Namespace, ruleName)
				return err
			}, utils.Timeout, utils.Interval).Should(gomega.Succeed())

			mode, _, _ := unstructured.NestedString(
				podGroup.Object, "spec", "networkTopology", "mode")
			highestTierName, _, _ := unstructured.NestedString(
				podGroup.Object, "spec", "networkTopology", "highestTierName")
			gomega.Expect(mode).Should(gomega.Equal("hard"))
			gomega.Expect(highestTierName).Should(gomega.Equal(topologyHyperNodeTierName))
			gomega.Expect(podGroup.GetLabels()[constants.PlacementGroupIDLabelKey]).
				ShouldNot(gomega.BeEmpty())
			gomega.Expect(podGroup.GetLabels()[constants.PlacementGroupSourceLabelKey]).
				Should(gomega.Equal(ruleName))

			ginkgo.By("waiting for the topology-constrained workloads to become ready")
			f.ExpectRbgV2Equal(rbg)

			ginkgo.By("verifying pods are bound to the rendered PodGroup and Volcano scheduler")
			for _, role := range rbg.Spec.Roles {
				gomega.Eventually(func() bool {
					pods := &corev1.PodList{}
					if err := f.Client.List(f.Ctx, pods,
						client.InNamespace(rbg.Namespace),
						client.MatchingLabels{
							constants.GroupNameLabelKey: rbg.Name,
							constants.RoleNameLabelKey:  role.Name,
						},
					); err != nil {
						return false
					}
					if len(pods.Items) != 1 {
						return false
					}
					pod := pods.Items[0]
					return pod.Spec.SchedulerName == volcanoSchedulerName &&
						pod.Annotations[scheduler.VolcanoPodGroupAnnotationKey] == podGroup.GetName() &&
						pod.Spec.NodeName != ""
				}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
					"pods for role %s should use Volcano and the topology PodGroup", role.Name)
			}
		})
	})
}

func topologyPolicy(name, namespace, ruleName string, roles []string) *workloadsv1alpha2.CoordinatedPolicy {
	return &workloadsv1alpha2.CoordinatedPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadsv1alpha2.CoordinatedPolicySpec{
			Policies: []workloadsv1alpha2.CoordinatedPolicyRule{{
				Name:  ruleName,
				Roles: roles,
				Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
					Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
						TopologyConstraint: &workloadsv1alpha2.TopologyConstraint{
							Pack: &workloadsv1alpha2.TopologyPackConstraint{
								Required: ptr.To(topologyHyperNodeTierName),
							},
						},
					},
				},
			}},
		},
	}
}

func topologyHyperNode(name string, nodes *corev1.NodeList) *unstructured.Unstructured {
	members := make([]interface{}, 0, len(nodes.Items))
	for i := range nodes.Items {
		members = append(members, map[string]interface{}{
			"type": "Node",
			"selector": map[string]interface{}{
				"exactMatch": map[string]interface{}{"name": nodes.Items[i].Name},
			},
		})
	}

	hyperNode := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "topology.volcano.sh/v1alpha1",
		"kind":       "HyperNode",
		"metadata":   map[string]interface{}{"name": name},
		"spec": map[string]interface{}{
			"tier":     int64(1),
			"tierName": topologyHyperNodeTierName,
			"members":  members,
		},
	}}
	hyperNode.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "topology.volcano.sh",
		Version: "v1alpha1",
		Kind:    "HyperNode",
	})
	return hyperNode
}

func getTopologyPodGroup(
	f *framework.Framework, namespace, sourceName string,
) (*unstructured.Unstructured, error) {
	podGroups := &unstructured.UnstructuredList{}
	podGroups.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "scheduling.volcano.sh",
		Version: "v1beta1",
		Kind:    "PodGroupList",
	})
	err := f.Client.List(f.Ctx, podGroups,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.PlacementGroupSourceLabelKey: sourceName},
	)
	if err != nil {
		return nil, err
	}
	if len(podGroups.Items) != 1 {
		return nil, fmt.Errorf("expected one PodGroup for source %q, got %d", sourceName, len(podGroups.Items))
	}
	return &podGroups.Items[0], nil
}

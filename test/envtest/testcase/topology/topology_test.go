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

package topology

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("Topology-aware scheduling lifecycle", func() {
	const timeout = 10 * time.Second
	const interval = 100 * time.Millisecond

	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-topology-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	It("protects an active topology policy and supports delete-and-recreate", func() {
		rbgName := "infer"
		key := types.NamespacedName{Name: rbgName, Namespace: testNs}

		Expect(testutil.K8sClient.Create(testutil.Ctx, topologyRBG(rbgName, testNs))).To(Succeed())
		Expect(testutil.K8sClient.Create(testutil.Ctx, topologyPolicy(rbgName, testNs, "block"))).To(Succeed())

		policy := &workloadsv1alpha2.CoordinatedPolicy{}
		Eventually(func() []string {
			Expect(testutil.K8sClient.Get(testutil.Ctx, key, policy)).To(Succeed())
			return policy.Finalizers
		}, timeout, interval).Should(ContainElement("workloads.x-k8s.io/topology-policy-protection"))

		rbg := &workloadsv1alpha2.RoleBasedGroup{}
		Eventually(func() *metav1.Condition {
			Expect(testutil.K8sClient.Get(testutil.Ctx, key, rbg)).To(Succeed())
			return apimeta.FindStatusCondition(
				rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupTopologyTranslated))
		}, timeout, interval).ShouldNot(BeNil())

		Expect(testutil.K8sClient.Delete(testutil.Ctx, policy)).To(Succeed())
		Expect(testutil.K8sClient.Get(testutil.Ctx, key, policy)).To(Succeed())
		Expect(policy.DeletionTimestamp.IsZero()).To(BeFalse())

		Expect(testutil.K8sClient.Delete(testutil.Ctx, topologyRBG(rbgName, testNs))).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(
				testutil.K8sClient.Get(testutil.Ctx, key, &workloadsv1alpha2.CoordinatedPolicy{}))
		}, timeout, interval).Should(BeTrue())

		// Recreating the same RBG name starts from clean revision history; the
		// existing controller does not garbage-collect old revisions when the RBG
		// object itself is deleted.
		revisions := &appsv1.ControllerRevisionList{}
		Expect(testutil.K8sClient.List(
			testutil.Ctx, revisions, client.InNamespace(testNs))).To(Succeed())
		for i := range revisions.Items {
			Expect(testutil.K8sClient.Delete(testutil.Ctx, &revisions.Items[i])).To(Succeed())
		}

		recreatedRBG := topologyRBG(rbgName, testNs)
		Expect(testutil.K8sClient.Create(testutil.Ctx, recreatedRBG)).To(Succeed())
		Expect(testutil.K8sClient.Create(testutil.Ctx, topologyPolicy(rbgName, testNs, "rack"))).To(Succeed())

		Eventually(func() []string {
			recreated := &workloadsv1alpha2.CoordinatedPolicy{}
			if err := testutil.K8sClient.Get(testutil.Ctx, key, recreated); err != nil {
				return nil
			}
			return recreated.Finalizers
		}, timeout, interval).Should(ContainElement("workloads.x-k8s.io/topology-policy-protection"))
	})
})

func topologyRBG(name, namespace string) *workloadsv1alpha2.RoleBasedGroup {
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{
			{Name: "prefill", Replicas: ptrInt32(1)},
		}},
	}
}

func topologyPolicy(name, namespace, level string) *workloadsv1alpha2.CoordinatedPolicy {
	return &workloadsv1alpha2.CoordinatedPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadsv1alpha2.CoordinatedPolicySpec{Policies: []workloadsv1alpha2.CoordinatedPolicyRule{{
			Name:  "pd",
			Roles: []string{"prefill"},
			Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
				Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
					TopologyConstraint: &workloadsv1alpha2.TopologyConstraint{
						Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: &level},
					},
				},
			},
		}}},
	}
}

func ptrInt32(value int32) *int32 { return &value }

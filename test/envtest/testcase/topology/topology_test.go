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
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/constants"
	apiworkloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
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

	It("rejects a topology policy that arrives after the RBG accepted no topology", func() {
		rbgName := "infer-late"
		key := types.NamespacedName{Name: rbgName, Namespace: testNs}

		Expect(testutil.K8sClient.Create(testutil.Ctx, topologyRBG(rbgName, testNs))).To(Succeed())
		Eventually(func() string {
			rbg := &apiworkloadsv1alpha2.RoleBasedGroup{}
			if err := testutil.K8sClient.Get(testutil.Ctx, key, rbg); err != nil {
				return ""
			}
			return rbg.Annotations[workloadsv1alpha2.RBGTopologyContractAnnotation]
		}, timeout, interval).Should(Equal("none"))

		Expect(testutil.K8sClient.Create(
			testutil.Ctx, topologyPolicy(rbgName, testNs, "block"))).To(Succeed())

		// Trigger reconcile through the normal event path with a spec change. The RBG
		// update predicate compares Spec, so an annotation-only update would not enqueue.
		// Retry the update: the controller records the topology contract concurrently.
		Eventually(func() error {
			rbg := &apiworkloadsv1alpha2.RoleBasedGroup{}
			if err := testutil.K8sClient.Get(testutil.Ctx, key, rbg); err != nil {
				return err
			}
			rbg.Spec.Roles[0].Replicas = ptrInt32(2)
			return testutil.K8sClient.Update(testutil.Ctx, rbg)
		}, timeout, interval).Should(Succeed())

		Eventually(func() *metav1.Condition {
			fresh := &apiworkloadsv1alpha2.RoleBasedGroup{}
			if err := testutil.K8sClient.Get(testutil.Ctx, key, fresh); err != nil {
				return nil
			}
			return apimeta.FindStatusCondition(
				fresh.Status.Conditions, string(apiworkloadsv1alpha2.RoleBasedGroupTopologyTranslated))
		}, timeout, interval).Should(And(
			Not(BeNil()),
			WithTransform(func(condition *metav1.Condition) string {
				return string(condition.Status) + "/" + condition.Reason
			}, Equal("False/TopologyPolicyLate")),
		))
	})

	It("protects an active topology policy and supports delete-and-recreate", func() {
		rbgName := "infer"
		key := types.NamespacedName{Name: rbgName, Namespace: testNs}

		Expect(testutil.K8sClient.Create(testutil.Ctx, topologyRBG(rbgName, testNs))).To(Succeed())
		Expect(testutil.K8sClient.Create(testutil.Ctx, topologyPolicy(rbgName, testNs, "block"))).To(Succeed())

		policy := &apiworkloadsv1alpha2.CoordinatedPolicy{}
		Eventually(func() []string {
			Expect(testutil.K8sClient.Get(testutil.Ctx, key, policy)).To(Succeed())
			return policy.Finalizers
		}, timeout, interval).Should(ContainElement("workloads.x-k8s.io/topology-policy-protection"))

		rbg := &apiworkloadsv1alpha2.RoleBasedGroup{}
		Eventually(func() *metav1.Condition {
			Expect(testutil.K8sClient.Get(testutil.Ctx, key, rbg)).To(Succeed())
			return apimeta.FindStatusCondition(
				rbg.Status.Conditions, string(apiworkloadsv1alpha2.RoleBasedGroupTopologyTranslated))
		}, timeout, interval).ShouldNot(BeNil())

		Expect(testutil.K8sClient.Delete(testutil.Ctx, policy)).To(Succeed())
		Expect(testutil.K8sClient.Get(testutil.Ctx, key, policy)).To(Succeed())
		Expect(policy.DeletionTimestamp.IsZero()).To(BeFalse())

		Expect(testutil.K8sClient.Delete(testutil.Ctx, topologyRBG(rbgName, testNs))).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(
				testutil.K8sClient.Get(testutil.Ctx, key, &apiworkloadsv1alpha2.CoordinatedPolicy{}))
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
			recreated := &apiworkloadsv1alpha2.CoordinatedPolicy{}
			if err := testutil.K8sClient.Get(testutil.Ctx, key, recreated); err != nil {
				return nil
			}
			return recreated.Finalizers
		}, timeout, interval).Should(ContainElement("workloads.x-k8s.io/topology-policy-protection"))
	})
})

func topologyRBG(name, namespace string) *apiworkloadsv1alpha2.RoleBasedGroup {
	return &apiworkloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: apiworkloadsv1alpha2.RoleBasedGroupSpec{Roles: []apiworkloadsv1alpha2.RoleSpec{
			{Name: "prefill", Replicas: ptrInt32(1)},
		}},
	}
}

func topologyPolicy(name, namespace, level string) *apiworkloadsv1alpha2.CoordinatedPolicy {
	return &apiworkloadsv1alpha2.CoordinatedPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: apiworkloadsv1alpha2.CoordinatedPolicySpec{Policies: []apiworkloadsv1alpha2.CoordinatedPolicyRule{{
			Name:  "pd",
			Roles: []string{"prefill"},
			Strategy: apiworkloadsv1alpha2.CoordinatedPolicyStrategy{
				Scheduling: &apiworkloadsv1alpha2.SchedulingCoordinationStrategy{
					TopologyConstraint: &apiworkloadsv1alpha2.TopologyConstraint{
						Pack: &apiworkloadsv1alpha2.TopologyPackConstraint{Required: &level},
					},
				},
			},
		}}},
	}
}

func ptrInt32(value int32) *int32 { return &value }

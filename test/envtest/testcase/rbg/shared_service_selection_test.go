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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

var _ = Describe("SharedServiceSelection", func() {
	const (
		sssTimeout  = time.Second * 60
		sssInterval = time.Millisecond * 500
	)

	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-sss-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	// createRbg creates an RBG holding a single leaderWorkerPattern role and returns the
	// object as persisted by the API server, so that defaulted fields are visible.
	createRbg := func(name string, role workloadsv1alpha2.RoleSpec) *workloadsv1alpha2.RoleBasedGroup {
		rbg := wrappersv2.BuildBasicRoleBasedGroup(name, testNs).
			WithRoles([]workloadsv1alpha2.RoleSpec{role}).
			Obj()
		ExpectWithOffset(1, testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

		created := &workloadsv1alpha2.RoleBasedGroup{}
		ExpectWithOffset(1, testutil.K8sClient.Get(
			testutil.Ctx, types.NamespacedName{Name: name, Namespace: testNs}, created,
		)).Should(Succeed())
		return created
	}

	// updateRbg re-reads the RBG and applies mutate, retrying on conflict because the controller
	// writes to the same object.
	updateRbg := func(name string, mutate func(*workloadsv1alpha2.RoleBasedGroup)) {
		ExpectWithOffset(1, retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latest := &workloadsv1alpha2.RoleBasedGroup{}
			if err := testutil.K8sClient.Get(
				testutil.Ctx, types.NamespacedName{Name: name, Namespace: testNs}, latest,
			); err != nil {
				return err
			}
			mutate(latest)
			return testutil.K8sClient.Update(testutil.Ctx, latest)
		})).Should(Succeed())
	}

	// componentServiceNames waits for the RoleInstanceSet of the role and returns its
	// component name -> serviceName mapping.
	componentServiceNames := func(
		rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
	) map[string]string {
		ris := &workloadsv1alpha2.RoleInstanceSet{}
		EventuallyWithOffset(1, func() error {
			return testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{
				Name: rbg.GetWorkloadName(role), Namespace: testNs,
			}, ris)
		}, sssTimeout, sssInterval).Should(Succeed())

		serviceNames := map[string]string{}
		for _, component := range ris.Spec.RoleInstanceTemplate.Components {
			serviceNames[component.Name] = component.ServiceName
		}
		return serviceNames
	}

	serviceSelector := func(
		rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
	) map[string]string {
		svc := &corev1.Service{}
		EventuallyWithOffset(1, func() error {
			return testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{
				Name: rbg.GetServiceName(role), Namespace: testNs,
			}, svc)
		}, sssTimeout, sssInterval).Should(Succeed())
		return svc.Spec.Selector
	}

	// podsByComponent waits until every pod of the role is created and indexes them by
	// component name.
	podsByComponent := func(rbgName, roleName string, count int) map[string]corev1.Pod {
		pods := map[string]corev1.Pod{}
		EventuallyWithOffset(1, func() int {
			podList := &corev1.PodList{}
			if err := testutil.K8sClient.List(testutil.Ctx, podList,
				client.InNamespace(testNs),
				client.MatchingLabels{
					constants.GroupNameLabelKey: rbgName,
					constants.RoleNameLabelKey:  roleName,
				},
			); err != nil {
				return 0
			}
			pods = map[string]corev1.Pod{}
			for i := range podList.Items {
				pod := podList.Items[i]
				if pod.DeletionTimestamp != nil {
					continue
				}
				pods[pod.Labels[constants.ComponentNameLabelKey]] = pod
			}
			return len(pods)
		}, sssTimeout, sssInterval).Should(Equal(count))
		return pods
	}

	Context("When the policy is not set", func() {
		It("Should fall back to LeaderOnly and only give the leader component a serviceName", func() {
			role := wrappersv2.BuildLeaderWorkerRole("role-1").Obj()
			role.LeaderWorkerPattern.SharedServiceSelection = nil

			rbg := createRbg("rbg-default", role)

			// The default lives in the controller, not in the CRD, so the stored field stays unset.
			By("verifying the API server does not populate the field")
			Expect(rbg.Spec.Roles[0].LeaderWorkerPattern.SharedServiceSelection).Should(BeNil())

			By("verifying only the leader component is bound to the shared service")
			serviceNames := componentServiceNames(rbg, &rbg.Spec.Roles[0])
			Expect(serviceNames[string(constants.LeaderComponentType)]).
				Should(Equal(rbg.GetServiceName(&rbg.Spec.Roles[0])))
			Expect(serviceNames[string(constants.WorkerComponentType)]).Should(BeEmpty())

			By("verifying the shared service only selects leader pods")
			Expect(serviceSelector(rbg, &rbg.Spec.Roles[0])).
				Should(HaveKeyWithValue(constants.ComponentNameLabelKey, string(constants.LeaderComponentType)))

			By("verifying only the leader pod has a network identity")
			pods := podsByComponent(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			leader := pods[string(constants.LeaderComponentType)]
			Expect(leader.Spec.Hostname).Should(Equal(leader.Name))
			Expect(leader.Spec.Subdomain).Should(Equal(rbg.GetServiceName(&rbg.Spec.Roles[0])))
			Expect(pods[string(constants.WorkerComponentType)].Spec.Subdomain).Should(BeEmpty())
		})
	})

	Context("When the policy is All", func() {
		It("Should give every component a serviceName and a pod network identity", func() {
			role := wrappersv2.BuildLeaderWorkerRole("role-1").Obj()
			role.LeaderWorkerPattern.SharedServiceSelection = ptr.To(
				workloadsv1alpha2.SharedServiceSelectionAll,
			)

			rbg := createRbg("rbg-all", role)
			svcName := rbg.GetServiceName(&rbg.Spec.Roles[0])

			By("verifying both components are bound to the shared service")
			serviceNames := componentServiceNames(rbg, &rbg.Spec.Roles[0])
			Expect(serviceNames[string(constants.LeaderComponentType)]).Should(Equal(svcName))
			Expect(serviceNames[string(constants.WorkerComponentType)]).Should(Equal(svcName))

			By("verifying the shared service selects every pod of the role")
			Expect(serviceSelector(rbg, &rbg.Spec.Roles[0])).
				ShouldNot(HaveKey(constants.ComponentNameLabelKey))

			By("verifying leader and worker pods both have a network identity")
			pods := podsByComponent(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			for _, component := range []string{
				string(constants.LeaderComponentType), string(constants.WorkerComponentType),
			} {
				pod := pods[component]
				Expect(pod.Spec.Hostname).Should(Equal(pod.Name), "component %s", component)
				Expect(pod.Spec.Subdomain).Should(Equal(svcName), "component %s", component)
			}
		})
	})

	// Switching All -> LeaderOnly drops WithServiceName from the worker apply configuration.
	// ServiceName is a plain string with omitempty, so there is no explicit-empty value to send:
	// the field only disappears because server-side apply retires a field the rbg field manager
	// previously owned. That is worth pinning down rather than inferring from the code.
	Context("When the policy changes from All to LeaderOnly", func() {
		It("Should clear the worker component serviceName and narrow the shared service in place", func() {
			role := wrappersv2.BuildLeaderWorkerRole("role-1").Obj()
			role.LeaderWorkerPattern.SharedServiceSelection = ptr.To(
				workloadsv1alpha2.SharedServiceSelectionAll,
			)

			rbg := createRbg("rbg-all-to-leaderonly", role)
			roleRef := &rbg.Spec.Roles[0]
			svcName := rbg.GetServiceName(roleRef)

			By("verifying both components start out bound to the shared service")
			Eventually(func() string {
				return componentServiceNames(rbg, roleRef)[string(constants.WorkerComponentType)]
			}, sssTimeout, sssInterval).Should(Equal(svcName))

			svc := &corev1.Service{}
			Expect(testutil.K8sClient.Get(
				testutil.Ctx, types.NamespacedName{Name: svcName, Namespace: testNs}, svc,
			)).Should(Succeed())
			originalSvcUID := svc.UID

			By("switching the policy to LeaderOnly")
			updateRbg(rbg.Name, func(latest *workloadsv1alpha2.RoleBasedGroup) {
				latest.Spec.Roles[0].LeaderWorkerPattern.SharedServiceSelection = ptr.To(
					workloadsv1alpha2.SharedServiceSelectionLeaderOnly,
				)
			})

			By("verifying the worker component's serviceName is removed")
			Eventually(func() string {
				return componentServiceNames(rbg, roleRef)[string(constants.WorkerComponentType)]
			}, sssTimeout, sssInterval).Should(BeEmpty())

			By("verifying the leader component stays bound to the shared service")
			Expect(componentServiceNames(rbg, roleRef)[string(constants.LeaderComponentType)]).
				Should(Equal(svcName))

			By("verifying the shared service selector narrows to leader pods")
			Eventually(func() map[string]string {
				return serviceSelector(rbg, roleRef)
			}, sssTimeout, sssInterval).Should(
				HaveKeyWithValue(constants.ComponentNameLabelKey, string(constants.LeaderComponentType)),
			)

			By("verifying the shared service was updated in place")
			Expect(testutil.K8sClient.Get(
				testutil.Ctx, types.NamespacedName{Name: svcName, Namespace: testNs}, svc,
			)).Should(Succeed())
			Expect(svc.UID).Should(Equal(originalSvcUID))
		})
	})

	Context("When the role runs on a LeaderWorkerSet workload", func() {
		// The policy only drives the shared headless service that RBG manages itself, which
		// LeaderWorkerSet roles do not have, so LeaderOnly is rejected at admission there. Leaving
		// the field unset must stay valid: that is why the LeaderOnly default is applied by the
		// controller instead of by a CRD default, which would populate the field on every role.
		It("Should accept an unset policy and reject an explicit LeaderOnly", func() {
			role := wrappersv2.BuildLeaderWorkerRole("role-1").
				WithWorkload("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet").
				Obj()
			role.LeaderWorkerPattern.SharedServiceSelection = nil

			rbg := createRbg("rbg-lws", role)
			Expect(rbg.Spec.Roles[0].LeaderWorkerPattern.SharedServiceSelection).Should(BeNil())

			By("verifying an explicit LeaderOnly is rejected on this workload type")
			rejected := wrappersv2.BuildLeaderWorkerRole("role-1").
				WithWorkload("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet").
				Obj()
			rejected.LeaderWorkerPattern.SharedServiceSelection = ptr.To(
				workloadsv1alpha2.SharedServiceSelectionLeaderOnly,
			)
			rejectedRbg := wrappersv2.BuildBasicRoleBasedGroup("rbg-lws-leaderonly", testNs).
				WithRoles([]workloadsv1alpha2.RoleSpec{rejected}).
				Obj()
			err := testutil.K8sClient.Create(testutil.Ctx, rejectedRbg)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("only supported for RoleInstanceSet"))
		})
	})
})

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

// Envtest reproductions for suspected bugs raised in the review of PR #394.
// Run against a real kube-apiserver (envtest); no kubelet, so a pod status
// update to PodFailed sticks and reliably triggers the restart path.
//
// These specs encode the INTENDED behavior. On the PR head, B2 is expected to
// FAIL (bug reproduced) and B4 is expected to show the apiserver accepting a
// negative delay on RoleInstance while rejecting it on RoleBasedGroup.
// See docs/verification/pr394-restart-backoff/README.md.

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

var _ = Describe("PR394 Restart Backoff Bug Verification", func() {
	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-pr394-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})
	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	// --- self-contained helpers (mirroring restart_policy_test.go closures) ---

	listRolePods := func(rbgName, roleName string) *corev1.PodList {
		podList := &corev1.PodList{}
		ExpectWithOffset(1, testutil.K8sClient.List(testutil.Ctx, podList,
			client.InNamespace(testNs),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbgName,
				constants.RoleNameLabelKey:  roleName,
			})).Should(Succeed())
		return podList
	}

	activePods := func(pl *corev1.PodList) []*corev1.Pod {
		var out []*corev1.Pod
		for i := range pl.Items {
			p := &pl.Items[i]
			if p.DeletionTimestamp == nil &&
				p.Status.Phase != corev1.PodFailed &&
				p.Status.Phase != corev1.PodSucceeded {
				out = append(out, p)
			}
		}
		return out
	}

	makePodsReady := func(rbgName, roleName string) {
		Eventually(func() bool {
			for i := range listRolePods(rbgName, roleName).Items {
				p := listRolePods(rbgName, roleName).Items[i]
				if p.Status.Phase == corev1.PodRunning {
					continue
				}
				fresh := &corev1.Pod{}
				if err := testutil.K8sClient.Get(testutil.Ctx, client.ObjectKeyFromObject(&p), fresh); err != nil {
					return false
				}
				testutil.SetPodRunningAndReady(fresh)
				if err := testutil.K8sClient.Status().Update(testutil.Ctx, fresh); err != nil {
					return false
				}
			}
			return true
		}, timeout, interval).Should(BeTrue())
	}

	waitForPods := func(rbgName, roleName string, count int) {
		Eventually(func() int {
			return len(activePods(listRolePods(rbgName, roleName)))
		}, timeout, interval).Should(Equal(count))
	}

	getRoleInstance := func(rbgName, roleName string) *workloadsv1alpha2.RoleInstance {
		riList := &workloadsv1alpha2.RoleInstanceList{}
		ExpectWithOffset(1, testutil.K8sClient.List(testutil.Ctx, riList,
			client.InNamespace(testNs),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbgName,
				constants.RoleNameLabelKey:  roleName,
			})).Should(Succeed())
		if len(riList.Items) == 0 {
			return nil
		}
		return &riList.Items[0]
	}

	waitForRoleInstanceReady := func(rbgName, roleName string) {
		Eventually(func() bool {
			ri := getRoleInstance(rbgName, roleName)
			if ri == nil {
				return false
			}
			for _, c := range ri.Status.Conditions {
				if c.Type == workloadsv1alpha2.RoleInstanceReady {
					return c.Status == corev1.ConditionTrue
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())
	}

	// ---------------------------------------------------------------------
	// B2: stable-period RestartCount reset is clobbered by the monotonic
	// preserve in updateStatus once the count has climbed above 1.
	// ---------------------------------------------------------------------
	It("B2: RestartCount>1 must reset to 1 after a stable period (expect FAIL: stays high)", func() {
		rbgName := "b2-reset"
		roleName := defaultRoleName

		rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole(roleName).
					WithReplicas(1).
					WithSize(1).
					WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
					WithBaseDelaySeconds(1).
					WithMaxDelaySeconds(2).
					Obj(),
			}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

		waitForPods(rbgName, roleName, 1)
		makePodsReady(rbgName, roleName)
		waitForRoleInstanceReady(rbgName, roleName)

		// Seed a high restart history: count=5, last restart 11 minutes ago
		// (past the reset threshold max(maxDelay*2, 10min) = 10min).
		Eventually(func() error {
			ri := getRoleInstance(rbgName, roleName)
			if ri == nil {
				return fmt.Errorf("no roleinstance yet")
			}
			ri.Status.RestartCount = 5
			t := metav1.NewTime(time.Now().Add(-11 * time.Minute))
			ri.Status.LastRestartTime = &t
			return testutil.K8sClient.Status().Update(testutil.Ctx, ri)
		}, timeout, interval).Should(Succeed())

		// Trigger a fresh crash.
		pods := listRolePods(rbgName, roleName)
		Expect(pods.Items).ShouldNot(BeEmpty())
		originalUID := pods.Items[0].UID
		fresh := &corev1.Pod{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, client.ObjectKeyFromObject(&pods.Items[0]), fresh)).Should(Succeed())
		fresh.Status.Phase = corev1.PodFailed
		fresh.Status.Reason = "Error"
		Expect(testutil.K8sClient.Status().Update(testutil.Ctx, fresh)).Should(Succeed())

		// Wait for recreation (old pod gone).
		Eventually(func() bool {
			for _, p := range activePods(listRolePods(rbgName, roleName)) {
				if p.UID == originalUID {
					return false
				}
			}
			return len(activePods(listRolePods(rbgName, roleName))) >= 1
		}, timeout, interval).Should(BeTrue())

		makePodsReady(rbgName, roleName)

		// INTENDED: after a stable period the counter resets, so the fresh
		// crash records RestartCount=1. On the PR head the monotonic preserve
		// keeps the old value (5/6), so this assertion FAILS => bug reproduced.
		Eventually(func() int32 {
			ri := getRoleInstance(rbgName, roleName)
			if ri == nil {
				return -1
			}
			GinkgoWriter.Printf("B2 observed RestartCount=%d\n", ri.Status.RestartCount)
			return ri.Status.RestartCount
		}, timeout, interval).Should(Equal(int32(1)),
			"RestartCount should reset to 1 after a long stable period, not continue from 5")
	})

	// ---------------------------------------------------------------------
	// B4: RoleInstanceSpec lacks Minimum=0 validation, so the apiserver
	// accepts a negative baseDelaySeconds — while the RoleBasedGroup pattern
	// (which has Minimum=0) rejects it. This asymmetry lets a negative delay
	// through, which the backoff math then turns into "no wait".
	// ---------------------------------------------------------------------
	It("B4: apiserver must reject negative baseDelaySeconds on both RBG and RoleInstance", func() {
		// (a) RBG pattern field HAS Minimum=0 -> expect rejection.
		badRBG := wrappersv2.BuildBasicRoleBasedGroup("b4-rbg", testNs).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole(defaultRoleName).
					WithReplicas(1).WithSize(1).
					WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
					WithBaseDelaySeconds(-30).
					Obj(),
			}).Obj()
		errRBG := testutil.K8sClient.Create(testutil.Ctx, badRBG)
		GinkgoWriter.Printf("B4 create RBG(baseDelaySeconds=-30) err = %v\n", errRBG)
		Expect(errRBG).To(HaveOccurred(), "RBG has minimum:0, negative should be rejected")

		// (b) RoleInstance spec field has NO Minimum -> apiserver accepts it.
		// The INTENDED behavior is that this is ALSO rejected. On the PR head
		// the create SUCCEEDS => validation gap reproduced.
		badRI := &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "b4-ri", Namespace: testNs},
			Spec: workloadsv1alpha2.RoleInstanceSpec{
				RestartPolicy:    workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
				BaseDelaySeconds: ptr.To(int32(-30)),
				MaxDelaySeconds:  ptr.To(int32(600)),
				Components: []workloadsv1alpha2.RoleInstanceComponent{
					{
						Name: "main",
						Size: ptr.To(int32(1)),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{Name: "c", Image: "registry.cn-hangzhou.aliyuncs.com/acs-sample/nginx:latest"},
								},
							},
						},
					},
				},
			},
		}
		errRI := testutil.K8sClient.Create(testutil.Ctx, badRI)
		GinkgoWriter.Printf("B4 create RoleInstance(baseDelaySeconds=-30) err = %v\n", errRI)
		Expect(errRI).To(HaveOccurred(),
			"RoleInstance baseDelaySeconds should also be validated (minimum:0), but it is not")
	})
})

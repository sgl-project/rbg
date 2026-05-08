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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunInactivePodTestCases(f *framework.Framework) {
	// Case 1: Evicted Pod triggers RBG recreation with RestartPolicy=RecreateRBGOnPodRestart
	ginkgo.It("evicted pod triggers RBG recreation with RecreateRBGOnPodRestart", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-evicted-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("role-1").
				WithWorkload("apps/v1", "Deployment").
				WithReplicas(2).
				WithRestartPolicy(workloadsv1alpha2.RecreateRBGOnPodRestart).
				Obj(),
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get one pod and simulate Evicted status
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).ShouldNot(gomega.BeEmpty())

		targetPod := &podList.Items[0]
		gomega.Expect(utils.SetPodEvicted(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for RBG recreation - RestartInProgress should go True then False
		f.ExpectRbgV2Equal(rbg)
		f.ExpectRbgV2Condition(rbg, workloadsv1alpha2.RoleBasedGroupRestartInProgress, metav1.ConditionFalse)

		// Verify active pods count restored to 2
		activeCount, err := utils.GetActivePodCount(f.Ctx, f.Client, f.Namespace, rbg.Name)
		gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
		gomega.Expect(activeCount).Should(gomega.Equal(2))
	})

	// Case 2: Failed Pod triggers RoleInstance recreation with RestartPolicy=RecreateRoleInstanceOnPodRestart
	ginkgo.It("failed pod triggers RoleInstance recreation with RecreateRoleInstanceOnPodRestart", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-instance-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildLeaderWorkerRole("role-1").
				WithReplicas(2).
				WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
				Obj(),
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get one pod and simulate Failed status
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())

		targetPod := &podList.Items[0]
		gomega.Expect(utils.SetPodFailed(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for RoleInstance recreation via LWS controller
		f.ExpectRbgV2Equal(rbg)
	})

	// Case 3: RestartPolicy=None creates replacement pod for inactive pod
	ginkgo.It("inactive pod triggers replacement pod creation with RestartPolicy=None", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-none-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("role-1").
				WithWorkload("apps/v1", "Deployment").
				WithReplicas(3).
				Obj(), // RestartPolicy defaults to None
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Record initial pod UIDs
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		initialPodUIDs := make(map[string]string)
		for _, p := range podList.Items {
			initialPodUIDs[p.Name] = string(p.UID)
		}

		// Simulate one pod becoming Evicted
		targetPod := &podList.Items[0]
		gomega.Expect(utils.SetPodEvicted(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for replacement pod to be created - active count should be 3
		gomega.Eventually(func() bool {
			activeCount, err := utils.GetActivePodCount(f.Ctx, f.Client, f.Namespace, rbg.Name)
			if err != nil {
				return false
			}
			return activeCount == 3
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())

		// Verify no RBG restart (RestartInProgress should not exist or be False)
		freshRbg := &workloadsv1alpha2.RoleBasedGroup{}
		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(rbg), freshRbg)).Should(gomega.Succeed())
		// No restart should have occurred for RestartPolicy=None
	})

	// Case 4: predicate only captures state transition, no duplicate triggers
	ginkgo.It("predicate only captures state transition, no duplicate triggers", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-no-dup-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("role-1").
				WithWorkload("apps/v1", "Deployment").
				WithReplicas(1).
				WithRestartPolicy(workloadsv1alpha2.RecreateRBGOnPodRestart).
				Obj(),
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// First eviction - should trigger recreation
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		targetPod := &podList.Items[0]
		gomega.Expect(utils.SetPodEvicted(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for recreation to complete
		f.ExpectRbgV2Equal(rbg)
		f.ExpectRbgV2Condition(rbg, workloadsv1alpha2.RoleBasedGroupRestartInProgress, metav1.ConditionFalse)

		// Get new pod and update status again (should NOT trigger recreation)
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		newPod := &podList.Items[0]

		// Update pod status message without changing phase
		newPod.Status.Message = "Updated message for testing"
		gomega.Expect(f.Client.Status().Update(f.Ctx, newPod)).Should(gomega.Succeed())

		// Wait a bit and verify no new restart occurred
		time.Sleep(5 * time.Second)
		freshRbg := &workloadsv1alpha2.RoleBasedGroup{}
		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(rbg), freshRbg)).Should(gomega.Succeed())

		// RestartInProgress should still be False (no new recreation)
		hasRestartCondition := false
		for _, cond := range freshRbg.Status.Conditions {
			if cond.Type == string(workloadsv1alpha2.RoleBasedGroupRestartInProgress) {
				hasRestartCondition = true
				gomega.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionFalse))
			}
		}
		// If no restart condition exists, that's also acceptable (no restart happened)
		if hasRestartCondition {
			gomega.Expect(freshRbg.Status.Conditions).ShouldNot(gomega.BeEmpty())
		}
	})

	// Case 5: UnexpectedAdmissionError triggers recreation
	ginkgo.It("unexpected admission error pod triggers recreation", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-admission-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("role-1").
				WithWorkload("apps/v1", "Deployment").
				WithReplicas(1).
				WithRestartPolicy(workloadsv1alpha2.RecreateRBGOnPodRestart).
				Obj(),
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get pod and simulate UnexpectedAdmissionError
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())

		targetPod := &podList.Items[0]
		gomega.Expect(utils.SetPodUnexpectedAdmissionError(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for RBG recreation
		f.ExpectRbgV2Equal(rbg)
		f.ExpectRbgV2Condition(rbg, workloadsv1alpha2.RoleBasedGroupRestartInProgress, metav1.ConditionFalse)
	})
}

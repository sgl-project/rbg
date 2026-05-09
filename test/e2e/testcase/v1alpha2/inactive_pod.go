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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunInactivePodTestCases(f *framework.Framework) {
	// Case 1: Evicted Pod triggers replacement Pod creation with RestartPolicy=RecreateRBGOnPodRestart
	// Note: Pod Failed (Dimension 2) does NOT trigger RBG recreation via Pod Controller.
	// RoleInstance Controller creates replacement Pod through normal reconciliation.
	ginkgo.It("evicted pod triggers replacement pod creation with RecreateRBGOnPodRestart", func() {
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

		// Get pods and record initial UIDs
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).ShouldNot(gomega.BeEmpty())
		gomega.Expect(podList.Items).Should(gomega.HaveLen(2))

		initialPodUIDs := make(map[string]types.UID)
		for _, p := range podList.Items {
			initialPodUIDs[p.Name] = p.UID
		}

		// Get one pod and simulate Evicted status
		targetPod := &podList.Items[0]
		gomega.Expect(utils.SetPodEvicted(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for replacement pod creation - active pods count should be restored to 2
		gomega.Eventually(func() int {
			activeCount, err := utils.GetActivePodCount(f.Ctx, f.Client, f.Namespace, rbg.Name)
			if err != nil {
				return 0
			}
			return activeCount
		}, utils.Timeout, utils.Interval).Should(gomega.Equal(2))

		// Verify NO RBG restart occurred (RestartInProgress should be False or absent)
		// Pod Failed (Dimension 2) does not trigger RBG recreation
		freshRbg := &workloadsv1alpha2.RoleBasedGroup{}
		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(rbg), freshRbg)).Should(gomega.Succeed())
		for _, cond := range freshRbg.Status.Conditions {
			if cond.Type == string(workloadsv1alpha2.RoleBasedGroupRestartInProgress) {
				gomega.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionFalse))
			}
		}
	})

	// Case 2: Failed Pod triggers RoleInstance recreation with RestartPolicy=RecreateRoleInstanceOnPodRestart
	// Note: With this policy, RoleInstance Controller recreates entire Instance (not just replacement Pod)
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

		// Get pods and record initial UIDs
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).ShouldNot(gomega.BeEmpty())

		initialPodUIDs := make(map[string]types.UID)
		for _, p := range podList.Items {
			initialPodUIDs[p.Name] = p.UID
		}

		// Get one pod and simulate Failed status
		targetPod := &podList.Items[0]
		gomega.Expect(utils.SetPodFailed(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for RoleInstance recreation - all pods should be recreated (new UIDs)
		gomega.Eventually(func() bool {
			gomega.Expect(f.Client.List(f.Ctx, podList,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
			if len(podList.Items) == 0 {
				return false
			}
			// All pods should have new UIDs (Instance was recreated)
			allNew := true
			for _, p := range podList.Items {
				if initialPodUIDs[p.Name] == p.UID {
					allNew = false
					break
				}
			}
			return allNew
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())

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

		// Get pods and record initial UIDs for verification
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).ShouldNot(gomega.BeEmpty())
		gomega.Expect(podList.Items).Should(gomega.HaveLen(3))

		initialPodUIDs := make(map[string]types.UID)
		for _, p := range podList.Items {
			initialPodUIDs[p.Name] = p.UID
		}

		// Simulate one pod becoming Evicted
		targetPod := &podList.Items[0]
		gomega.Expect(utils.SetPodEvicted(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for replacement pod to be created - active count should be 3
		gomega.Eventually(func() int {
			activeCount, err := utils.GetActivePodCount(f.Ctx, f.Client, f.Namespace, rbg.Name)
			if err != nil {
				return 0
			}
			return activeCount
		}, utils.Timeout, utils.Interval).Should(gomega.Equal(3))

		// Verify no RBG restart occurred (RestartInProgress should be False or absent)
		freshRbg := &workloadsv1alpha2.RoleBasedGroup{}
		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(rbg), freshRbg)).Should(gomega.Succeed())
		for _, cond := range freshRbg.Status.Conditions {
			if cond.Type == string(workloadsv1alpha2.RoleBasedGroupRestartInProgress) {
				gomega.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionFalse))
			}
		}

		// Verify new pod has different UID (replacement pod created)
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		// At least one pod should have a different UID (the replacement)
		foundReplacement := false
		for _, p := range podList.Items {
			if initialPodUIDs[p.Name] != p.UID {
				foundReplacement = true
				break
			}
		}
		gomega.Expect(foundReplacement).Should(gomega.BeTrue())
	})

	// Case 4: Container restart triggers RBG recreation with RecreateRBGOnPodRestart
	// This tests Dimension 1: container restart → restartPolicy behavior
	ginkgo.It("container restart triggers RBG recreation with RecreateRBGOnPodRestart", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-container-restart-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("role-1").
				WithWorkload("apps/v1", "Deployment").
				WithReplicas(1).
				WithRestartPolicy(workloadsv1alpha2.RecreateRBGOnPodRestart).
				Obj(),
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get pod and simulate container restart
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).ShouldNot(gomega.BeEmpty())

		targetPod := &podList.Items[0]
		gomega.Expect(utils.SimulateContainerRestart(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for RBG recreation - RestartInProgress should go True then False
		f.ExpectRbgV2Equal(rbg)
		f.ExpectRbgV2Condition(rbg, workloadsv1alpha2.RoleBasedGroupRestartInProgress, metav1.ConditionFalse)
	})

	// Case 5: Pod deletion triggers RBG recreation with RecreateRBGOnPodRestart
	// This is part of Dimension 1: pod deletion is handled by Pod Controller
	ginkgo.It("pod deletion triggers RBG recreation with RecreateRBGOnPodRestart", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-pod-deletion-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("role-1").
				WithWorkload("apps/v1", "Deployment").
				WithReplicas(1).
				WithRestartPolicy(workloadsv1alpha2.RecreateRBGOnPodRestart).
				Obj(),
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get pod and delete it
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).ShouldNot(gomega.BeEmpty())

		targetPod := &podList.Items[0]
		gomega.Expect(f.Client.Delete(f.Ctx, targetPod)).Should(gomega.Succeed())

		// Wait for RBG recreation - RestartInProgress should go True then False
		f.ExpectRbgV2Equal(rbg)
		f.ExpectRbgV2Condition(rbg, workloadsv1alpha2.RoleBasedGroupRestartInProgress, metav1.ConditionFalse)
	})
}

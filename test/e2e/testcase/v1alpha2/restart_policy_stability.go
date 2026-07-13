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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunRestartPolicyStabilityTestCases(f *framework.Framework) {
	ginkgo.Describe("restart policy stability", func() {
		runRestartPolicyRecreateTest(f)
		runRestartPolicyNoneReplaceTest(f)
		runRestartBackoffDelayTest(f)
		runRestartBackoffPropagationTest(f)
	})
}

// runRestartPolicyRecreateTest verifies that RecreateRoleInstanceOnPodRestart
// only recreates the affected instance and is stable after recreation.
func runRestartPolicyRecreateTest(f *framework.Framework) {
	ginkgo.It("RecreateRoleInstanceOnPodRestart only recreates affected instance and is stable after recreation", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithReplicas(2).
					WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get all pods and group by instance
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbg.Name,
				constants.RoleNameLabelKey:  "role-1",
			})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).ShouldNot(gomega.BeEmpty())

		// Group pods by instance name and record UIDs
		instancePods := make(map[string]map[string]types.UID)
		for _, pod := range podList.Items {
			instanceName := pod.Labels[constants.RoleInstanceNameLabelKey]
			if instancePods[instanceName] == nil {
				instancePods[instanceName] = make(map[string]types.UID)
			}
			instancePods[instanceName][pod.Name] = pod.UID
		}
		gomega.Expect(instancePods).Should(gomega.HaveLen(2), "should have 2 instances")

		// Pick one instance to fail, record the other as unaffected
		var targetInstance, unaffectedInstance string
		for name := range instancePods {
			if targetInstance == "" {
				targetInstance = name
			} else {
				unaffectedInstance = name
			}
		}
		unaffectedPodUIDs := instancePods[unaffectedInstance]

		// Wait for target RoleInstance to be Ready (required by shouldRecreateInstance)
		gomega.Eventually(func() bool {
			ri := &workloadsv1alpha2.RoleInstance{}
			if err := f.Client.Get(f.Ctx, client.ObjectKey{
				Namespace: f.Namespace,
				Name:      targetInstance,
			}, ri); err != nil {
				return false
			}
			for _, cond := range ri.Status.Conditions {
				if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == corev1.ConditionTrue {
					return true
				}
			}
			return false
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
			"RoleInstance should be Ready before triggering failure")

		// Re-fetch pods from the target instance to get latest resourceVersion
		targetPodList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, targetPodList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey:        rbg.Name,
				constants.RoleInstanceNameLabelKey: targetInstance,
			})).Should(gomega.Succeed())
		gomega.Expect(targetPodList.Items).ShouldNot(gomega.BeEmpty())
		targetPod := &targetPodList.Items[0]
		gomega.Expect(utils.SetPodFailed(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for the target instance's pods to be fully recreated (new UIDs)
		expectedPodCount := len(instancePods[targetInstance])
		gomega.Eventually(func() bool {
			pods := &corev1.PodList{}
			if err := f.Client.List(f.Ctx, pods,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{
					constants.GroupNameLabelKey:        rbg.Name,
					constants.RoleInstanceNameLabelKey: targetInstance,
				}); err != nil {
				return false
			}
			activePods := filterActivePods(pods.Items)
			if len(activePods) != expectedPodCount {
				return false
			}
			// All pods in the affected instance should have new UIDs
			for _, p := range activePods {
				if instancePods[targetInstance][p.Name] == p.UID {
					return false
				}
			}
			return true
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
			"target instance pods should be recreated with new UIDs")

		// Verify unaffected instance pods are completely untouched
		currentUnaffectedPods := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, currentUnaffectedPods,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey:        rbg.Name,
				constants.RoleInstanceNameLabelKey: unaffectedInstance,
			})).Should(gomega.Succeed())
		for _, pod := range currentUnaffectedPods.Items {
			if pod.DeletionTimestamp == nil {
				gomega.Expect(unaffectedPodUIDs).Should(gomega.HaveKeyWithValue(pod.Name, pod.UID),
					"unaffected instance pod %s should not be recreated", pod.Name)
			}
		}

		// Wait for RBG to be fully ready again
		f.ExpectRbgV2Equal(rbg)

		// Verify recreated instance is stable (no repeated restarts)
		recreatedPodUIDs := getInstancePodUIDs(f, rbg, targetInstance)
		gomega.Consistently(func() map[string]types.UID {
			return getInstancePodUIDs(f, rbg, targetInstance)
		}, 20, 2).Should(gomega.Equal(recreatedPodUIDs),
			"recreated instance should be stable after recreation (no repeated restarts)")
	})
}

// runRestartPolicyNoneReplaceTest verifies that RestartPolicy=None only
// replaces the failed pod without affecting other pods.
func runRestartPolicyNoneReplaceTest(f *framework.Framework) {
	ginkgo.It("RestartPolicy=None on RoleInstanceSet replaces only failed pod and is stable after replacement", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithReplicas(3).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Record initial pod UIDs
		initialPodUIDs := getPodUIDsForRole(f, rbg, "role-1")
		gomega.Expect(initialPodUIDs).Should(gomega.HaveLen(3))

		// Pick one pod to evict
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbg.Name,
				constants.RoleNameLabelKey:  "role-1",
			})).Should(gomega.Succeed())
		targetPod := &podList.Items[0]
		targetPodName := targetPod.Name

		gomega.Expect(utils.SetPodEvicted(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for active pod count to be restored to 3
		gomega.Eventually(func() int {
			count, err := utils.GetActivePodCount(f.Ctx, f.Client, f.Namespace, rbg.Name)
			if err != nil {
				return 0
			}
			return count
		}, utils.Timeout, utils.Interval).Should(gomega.Equal(3))

		// Verify unaffected pods kept their UIDs
		currentPodUIDs := getPodUIDsForRole(f, rbg, "role-1")
		for name, uid := range initialPodUIDs {
			if name == targetPodName {
				continue // skip the evicted pod
			}
			gomega.Expect(currentPodUIDs).Should(gomega.HaveKeyWithValue(name, uid),
				"unaffected pod %s should not be recreated", name)
		}

		// Wait for full readiness
		f.ExpectRbgV2Equal(rbg)

		// Verify stability after replacement (no further restarts)
		stablePodUIDs := getActivePodUIDsForRole(f, rbg, "role-1")
		gomega.Consistently(func() map[string]types.UID {
			return getActivePodUIDsForRole(f, rbg, "role-1")
		}, 20, 2).Should(gomega.Equal(stablePodUIDs),
			"pods should be stable after replacement (no repeated restarts)")
	})
}

// runRestartBackoffDelayTest verifies that exponential backoff delays
// the second recreation after a restart-policy-triggered recreation.
func runRestartBackoffDelayTest(f *framework.Framework) {
	ginkgo.It("RecreateRoleInstanceOnPodRestart with backoff delays second recreation", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-backoff-test", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithReplicas(1).
					WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
					WithBaseDelaySeconds(5).
					WithMaxDelaySeconds(30).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get pods and find the target instance
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbg.Name,
				constants.RoleNameLabelKey:  "role-1",
			})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).ShouldNot(gomega.BeEmpty())

		targetInstanceName := podList.Items[0].Labels[constants.RoleInstanceNameLabelKey]

		// Wait for RoleInstance Ready
		waitForInstanceReady(f, targetInstanceName)

		// --- First crash: no backoff (first restart) ---
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey:        rbg.Name,
				constants.RoleInstanceNameLabelKey: targetInstanceName,
			})).Should(gomega.Succeed())
		firstUIDs := make(map[string]types.UID)
		for _, p := range podList.Items {
			firstUIDs[p.Name] = p.UID
		}

		targetPod := &corev1.Pod{}
		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKey{
			Namespace: f.Namespace,
			Name:      podList.Items[0].Name,
		}, targetPod)).Should(gomega.Succeed())
		gomega.Expect(utils.SetPodFailed(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for first recreation (no backoff on first crash)
		waitForInstancePodsRecreated(f, rbg, targetInstanceName, firstUIDs)

		// Wait for full recovery
		f.ExpectRbgV2Equal(rbg)

		// Wait for RoleInstance to be fully recovered: Ready=True AND
		// Restarting condition cleared. The Restarting condition persists in
		// status during the creation phase (scaling=true) until pods become
		// Running+Ready, which causes checkRestartBackoff to skip backoff on
		// the next crash. We must wait for it to be cleared to ensure the
		// backoff mechanism works correctly for the second crash.
		waitForInstanceFullyRecovered(f, targetInstanceName)

		// --- Second crash: backoff should delay recreation ---
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey:        rbg.Name,
				constants.RoleInstanceNameLabelKey: targetInstanceName,
			})).Should(gomega.Succeed())
		secondUIDs := make(map[string]types.UID)
		for _, p := range podList.Items {
			secondUIDs[p.Name] = p.UID
		}

		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKey{
			Namespace: f.Namespace,
			Name:      podList.Items[0].Name,
		}, targetPod)).Should(gomega.Succeed())
		gomega.Expect(utils.SetPodFailed(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// During backoff window (5s), pods should NOT be recreated immediately
		gomega.Consistently(func() bool {
			pods := &corev1.PodList{}
			if err := f.Client.List(f.Ctx, pods,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{
					constants.GroupNameLabelKey:        rbg.Name,
					constants.RoleInstanceNameLabelKey: targetInstanceName,
				}); err != nil {
				return false
			}
			// Check if any active pod still has the old UID (backoff working)
			for _, p := range filterActivePods(pods.Items) {
				if secondUIDs[p.Name] == p.UID {
					return true // Original pod still exists — backoff is working
				}
			}
			return false
		}, 4, 1).Should(gomega.BeTrue(),
			"pod should NOT be recreated immediately during backoff window")

		// After backoff elapses, recreation should proceed
		waitForInstancePodsRecreated(f, rbg, targetInstanceName, secondUIDs)

		// Wait for full recovery and verify stability
		f.ExpectRbgV2Equal(rbg)
		recoveredUIDs := getInstancePodUIDs(f, rbg, targetInstanceName)
		gomega.Consistently(func() map[string]types.UID {
			return getInstancePodUIDs(f, rbg, targetInstanceName)
		}, 20, 2).Should(gomega.Equal(recoveredUIDs),
			"instance should be stable after backoff recovery (no infinite restart loop)")
	})
}

// runRestartBackoffPropagationTest verifies that BaseDelaySeconds and
// MaxDelaySeconds propagate from the RBG spec to the RoleInstance spec.
func runRestartBackoffPropagationTest(f *framework.Framework) {
	ginkgo.It("Restart backoff config propagates from RBG to RoleInstance", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-backoff-propagate", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithReplicas(1).
					WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
					WithBaseDelaySeconds(10).
					WithMaxDelaySeconds(120).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Find the RoleInstance and verify delay config propagation
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbg.Name,
				constants.RoleNameLabelKey:  "role-1",
			})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).ShouldNot(gomega.BeEmpty())

		targetInstanceName := podList.Items[0].Labels[constants.RoleInstanceNameLabelKey]

		gomega.Eventually(func() bool {
			ri := &workloadsv1alpha2.RoleInstance{}
			if err := f.Client.Get(f.Ctx, client.ObjectKey{
				Namespace: f.Namespace,
				Name:      targetInstanceName,
			}, ri); err != nil {
				return false
			}
			return ri.Spec.BaseDelaySeconds != nil && *ri.Spec.BaseDelaySeconds == 10 &&
				ri.Spec.MaxDelaySeconds != nil && *ri.Spec.MaxDelaySeconds == 120
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
			"BaseDelaySeconds=10 and MaxDelaySeconds=120 should propagate from RBG to RoleInstance")
	})
}

// waitForInstanceReady waits until the RoleInstance has a Ready=True condition.
func waitForInstanceReady(f *framework.Framework, instanceName string) {
	gomega.Eventually(func() bool {
		ri := &workloadsv1alpha2.RoleInstance{}
		if err := f.Client.Get(f.Ctx, client.ObjectKey{
			Namespace: f.Namespace,
			Name:      instanceName,
		}, ri); err != nil {
			return false
		}
		for _, cond := range ri.Status.Conditions {
			if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
		"RoleInstance should be Ready before triggering failure")
}

// waitForInstancePodsRecreated waits until all active pods of the instance have new UIDs.
func waitForInstancePodsRecreated(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, instanceName string, oldUIDs map[string]types.UID) {
	gomega.Eventually(func() bool {
		pods := &corev1.PodList{}
		if err := f.Client.List(f.Ctx, pods,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey:        rbg.Name,
				constants.RoleInstanceNameLabelKey: instanceName,
			}); err != nil {
			return false
		}
		activePods := filterActivePods(pods.Items)
		if len(activePods) == 0 {
			return false
		}
		for _, p := range activePods {
			if oldUIDs[p.Name] == p.UID {
				return false
			}
		}
		return true
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
		"pods should be recreated with new UIDs")
}

// waitForInstanceFullyRecovered waits until the RoleInstance has Ready=True and
// the Restarting condition is absent or False. This ensures the controller has
// fully converged: pods are Running, status is stable, and the in-memory
// restarting cache has been cleared. Without this wait, a subsequent crash
// might occur while the Restarting condition is still True in status (set
// during the creation phase), causing checkRestartBackoff to skip the backoff
// delay and allowing immediate recreation.
func waitForInstanceFullyRecovered(f *framework.Framework, instanceName string) {
	gomega.Eventually(func() bool {
		ri := &workloadsv1alpha2.RoleInstance{}
		if err := f.Client.Get(f.Ctx, client.ObjectKey{
			Namespace: f.Namespace,
			Name:      instanceName,
		}, ri); err != nil {
			return false
		}
		readyFound := false
		for _, cond := range ri.Status.Conditions {
			if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == corev1.ConditionTrue {
				readyFound = true
			}
			// Restarting condition must be absent or explicitly False
			if cond.Type == workloadsv1alpha2.RoleInstanceRestarting && cond.Status == corev1.ConditionTrue {
				return false
			}
		}
		return readyFound
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
		"RoleInstance should be fully recovered (Ready=True, Restarting cleared)")
}

// filterActivePods returns pods that are not terminating and not in terminal phase.
func filterActivePods(pods []corev1.Pod) []corev1.Pod {
	var active []corev1.Pod
	for i := range pods {
		if pods[i].DeletionTimestamp == nil &&
			pods[i].Status.Phase != corev1.PodFailed &&
			pods[i].Status.Phase != corev1.PodSucceeded {
			active = append(active, pods[i])
		}
	}
	return active
}

// getInstancePodUIDs returns pod UIDs for a specific RoleInstance.
func getInstancePodUIDs(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, instanceName string) map[string]types.UID {
	podList := &corev1.PodList{}
	err := f.Client.List(f.Ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey:        rbg.Name,
			constants.RoleInstanceNameLabelKey: instanceName,
		},
	)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	result := make(map[string]types.UID)
	for _, pod := range podList.Items {
		if pod.DeletionTimestamp == nil && pod.Status.Phase != corev1.PodFailed && pod.Status.Phase != corev1.PodSucceeded {
			result[pod.Name] = pod.UID
		}
	}
	return result
}

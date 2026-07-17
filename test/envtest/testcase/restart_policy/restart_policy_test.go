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

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

const (
	timeout         = time.Second * 60
	interval        = time.Millisecond * 500
	defaultRoleName = "role-1"
)

var _ = Describe("RestartPolicy Controller Integration", func() {
	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-restart-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	// Helper: list pods owned by a specific RBG role
	listRolePods := func(rbgName, roleName string) *corev1.PodList {
		podList := &corev1.PodList{}
		ExpectWithOffset(1, testutil.K8sClient.List(testutil.Ctx, podList,
			client.InNamespace(testNs),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbgName,
				constants.RoleNameLabelKey:  roleName,
			},
		)).Should(Succeed())
		return podList
	}

	// Helper: get active pods (not Failed/Succeeded, not deleting)
	getActivePods := func(podList *corev1.PodList) []*corev1.Pod {
		var active []*corev1.Pod
		for i := range podList.Items {
			p := &podList.Items[i]
			if p.DeletionTimestamp != nil {
				continue
			}
			if p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded {
				continue
			}
			active = append(active, p)
		}
		return active
	}

	// Helper: collect pod UIDs from a pod list
	getPodUIDs := func(podList *corev1.PodList) map[string]types.UID {
		uids := make(map[string]types.UID)
		for i := range podList.Items {
			uids[podList.Items[i].Name] = podList.Items[i].UID
		}
		return uids
	}

	// Helper: set all pods in the list to Running and Ready
	makePodsReady := func(rbgName, roleName string) {
		Eventually(func() error {
			podList := listRolePods(rbgName, roleName)
			for i := range podList.Items {
				pod := &podList.Items[i]
				if pod.Status.Phase == corev1.PodRunning {
					continue
				}
				// Re-fetch to avoid resourceVersion conflicts
				freshPod := &corev1.Pod{}
				if err := testutil.K8sClient.Get(testutil.Ctx,
					client.ObjectKeyFromObject(pod), freshPod); err != nil {
					return err
				}
				testutil.SetPodRunningAndReady(freshPod)
				if err := testutil.K8sClient.Status().Update(testutil.Ctx, freshPod); err != nil {
					return err
				}
			}
			return nil
		}, timeout, interval).Should(Succeed())
	}

	// Helper: wait for pods to exist for a role
	waitForPods := func(rbgName, roleName string, count int) {
		Eventually(func() int {
			podList := listRolePods(rbgName, roleName)
			active := getActivePods(podList)
			return len(active)
		}, timeout, interval).Should(Equal(count))
	}

	// Helper: get RoleInstance for a role
	getRoleInstance := func(rbgName, roleName string) *workloadsv1alpha2.RoleInstance {
		riList := &workloadsv1alpha2.RoleInstanceList{}
		ExpectWithOffset(1, testutil.K8sClient.List(testutil.Ctx, riList,
			client.InNamespace(testNs),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbgName,
				constants.RoleNameLabelKey:  roleName,
			},
		)).Should(Succeed())
		if len(riList.Items) == 0 {
			return nil
		}
		return &riList.Items[0]
	}

	// Helper: wait for RoleInstance to reach Ready condition
	waitForRoleInstanceReady := func(rbgName, roleName string) {
		Eventually(func() bool {
			ri := getRoleInstance(rbgName, roleName)
			if ri == nil {
				return false
			}
			for _, cond := range ri.Status.Conditions {
				if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == corev1.ConditionTrue {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())
	}

	Context("Controller chain diagnostic", func() {
		It("should verify the full controller chain creates all objects", func() {
			rbgName := "test-diag"
			roleName := defaultRoleName

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(1).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Step 1: Check RBG is created
			createdRBG := &workloadsv1alpha2.RoleBasedGroup{}
			Eventually(func() error {
				return testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: rbgName, Namespace: testNs}, createdRBG)
			}, timeout, interval).Should(Succeed(), "RBG should be created")
			GinkgoWriter.Printf("RBG created: %s/%s, UID: %s\n", createdRBG.Namespace, createdRBG.Name, createdRBG.UID)

			// Step 2: Check RoleInstanceSet is created
			risList := &workloadsv1alpha2.RoleInstanceSetList{}
			Eventually(func() int {
				if err := testutil.K8sClient.List(testutil.Ctx, risList,
					client.InNamespace(testNs)); err != nil {
					GinkgoWriter.Printf("Error listing RIS: %v\n", err)
					return 0
				}
				return len(risList.Items)
			}, timeout, interval).Should(BeNumerically(">=", 1), "RoleInstanceSet should be created")
			for _, ris := range risList.Items {
				GinkgoWriter.Printf("RIS found: %s, labels: %v, replicas: %v\n",
					ris.Name, ris.Labels, ris.Spec.Replicas)
			}

			// Step 3: Check RoleInstance is created
			riList := &workloadsv1alpha2.RoleInstanceList{}
			Eventually(func() int {
				if err := testutil.K8sClient.List(testutil.Ctx, riList,
					client.InNamespace(testNs)); err != nil {
					GinkgoWriter.Printf("Error listing RI: %v\n", err)
					return 0
				}
				return len(riList.Items)
			}, timeout, interval).Should(BeNumerically(">=", 1), "RoleInstance should be created")
			for _, ri := range riList.Items {
				GinkgoWriter.Printf("RI found: %s, labels: %v\n", ri.Name, ri.Labels)
			}

			// Step 4: Check Pods are created
			allPods := &corev1.PodList{}
			Eventually(func() int {
				if err := testutil.K8sClient.List(testutil.Ctx, allPods,
					client.InNamespace(testNs)); err != nil {
					GinkgoWriter.Printf("Error listing pods: %v\n", err)
					return 0
				}
				for _, p := range allPods.Items {
					GinkgoWriter.Printf("Pod found: %s, labels: %v, phase: %s\n",
						p.Name, p.Labels, p.Status.Phase)
				}
				return len(allPods.Items)
			}, timeout, interval).Should(BeNumerically(">=", 1), "Pods should be created")

			// Verify labels match what we expect
			podList := listRolePods(rbgName, roleName)
			Expect(podList.Items).Should(HaveLen(1),
				"Should find 1 pod with GroupName+RoleName labels")
		})
	})

	Context("RecreateRoleInstanceOnPodRestart", func() {
		It("should recreate pod when it fails with single-pod LeaderWorker pattern", func() {
			rbgName := "test-recreate-standalone"
			roleName := defaultRoleName

			// Use LeaderWorker with Size=1 (single pod per instance) to test
			// restart policy. StandalonePattern does not support RestartPolicy.
			// The multi-pod "recreate ALL" scenario is covered by the LeaderWorker test below.
			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(1).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Wait for 1 pod to be created
			waitForPods(rbgName, roleName, 1)

			// Make pod Ready
			makePodsReady(rbgName, roleName)

			// Wait for RoleInstance to be Ready
			waitForRoleInstanceReady(rbgName, roleName)

			// Record original pod UID
			podList := listRolePods(rbgName, roleName)
			Expect(podList.Items).Should(HaveLen(1))
			originalUID := podList.Items[0].UID

			// Fail the pod
			freshPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx, client.ObjectKeyFromObject(&podList.Items[0]), freshPod)).Should(Succeed())
			freshPod.Status.Phase = corev1.PodFailed
			freshPod.Status.Reason = "Error"
			freshPod.Status.Message = "Container exited with error"
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// Verify the pod is recreated with a new UID
			Eventually(func() bool {
				podList := listRolePods(rbgName, roleName)
				active := getActivePods(podList)
				if len(active) == 0 {
					return false
				}
				// The replacement pod should have a different UID
				for _, p := range active {
					if p.UID == originalUID {
						return false // Original pod still exists
					}
				}
				return true
			}, timeout, interval).Should(BeTrue(),
				"restart policy should trigger recreation of the failed pod")

			// Make replacement pod Ready and verify recovery
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)
		})

		It("should recreate all component pods when worker fails in LeaderWorker pattern", func() {
			rbgName := "test-recreate-lw"
			roleName := defaultRoleName

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(1).
						WithSize(2).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Wait for leader + worker pods (size=2 means 1 leader + 1 worker = 2 pods)
			waitForPods(rbgName, roleName, 2)

			// Make all pods Ready
			makePodsReady(rbgName, roleName)

			// Wait for RoleInstance Ready
			waitForRoleInstanceReady(rbgName, roleName)

			// Record original UIDs
			originalUIDs := getPodUIDs(listRolePods(rbgName, roleName))
			Expect(originalUIDs).Should(HaveLen(2))

			// Fail the worker pod (second pod)
			podList := listRolePods(rbgName, roleName)
			// Find the worker pod (higher index)
			var workerPod *corev1.Pod
			for i := range podList.Items {
				if podList.Items[i].Labels[constants.ComponentNameLabelKey] == "worker" {
					workerPod = &podList.Items[i]
					break
				}
			}
			if workerPod == nil {
				// Fallback: just pick the second pod
				workerPod = &podList.Items[1]
			}

			freshPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx, client.ObjectKeyFromObject(workerPod), freshPod)).Should(Succeed())
			freshPod.Status.Phase = corev1.PodFailed
			freshPod.Status.Reason = "Error"
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// Verify ALL pods are recreated (both leader and worker), not just the worker
			Eventually(func() bool {
				podList := listRolePods(rbgName, roleName)
				active := getActivePods(podList)
				for _, p := range active {
					if uid, ok := originalUIDs[p.Name]; ok && uid == p.UID {
						return false // Original pod still exists
					}
				}
				return len(active) >= 1
			}, timeout, interval).Should(BeTrue(),
				"restart policy should recreate ALL pods including leader when worker fails")

			// Recovery
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)
		})

		It("should set and clear Restarting condition during recreation cycle", func() {
			rbgName := "test-restarting-cond"
			roleName := defaultRoleName

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(1).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			waitForPods(rbgName, roleName, 1)
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)

			// Trigger failure
			podList := listRolePods(rbgName, roleName)
			freshPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), freshPod)).Should(Succeed())
			freshPod.Status.Phase = corev1.PodFailed
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// Verify Restarting condition is set
			Eventually(func() bool {
				ri := getRoleInstance(rbgName, roleName)
				if ri == nil {
					return false
				}
				for _, cond := range ri.Status.Conditions {
					if cond.Type == workloadsv1alpha2.RoleInstanceRestarting && cond.Status == corev1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue(),
				"Restarting condition should be set when restart policy triggers")

			// Wait for replacement pods and make them Ready
			waitForPods(rbgName, roleName, 1)
			makePodsReady(rbgName, roleName)

			// Verify Restarting condition is cleared when instance becomes Ready again
			Eventually(func() bool {
				ri := getRoleInstance(rbgName, roleName)
				if ri == nil {
					return false
				}
				for _, cond := range ri.Status.Conditions {
					if cond.Type == workloadsv1alpha2.RoleInstanceRestarting && cond.Status == corev1.ConditionTrue {
						return false // Still restarting
					}
				}
				// Also check that the instance is Ready
				for _, cond := range ri.Status.Conditions {
					if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == corev1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue(),
				"Restarting condition should be cleared when instance becomes Ready")
		})

		It("should not recreate when container restart is on Ignore-annotated pod", func() {
			rbgName := "test-ignore-policy"
			roleName := defaultRoleName

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(2).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			waitForPods(rbgName, roleName, 2)
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)

			// Record pod UIDs
			originalUIDs := getPodUIDs(listRolePods(rbgName, roleName))
			Expect(originalUIDs).Should(HaveLen(2))

			// Add Ignore annotation to one pod and simulate container restart on it
			podList := listRolePods(rbgName, roleName)
			ignorePod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), ignorePod)).Should(Succeed())

			// Add the Ignore annotation
			if ignorePod.Annotations == nil {
				ignorePod.Annotations = make(map[string]string)
			}
			ignorePod.Annotations[constants.RestartTriggerPolicyAnnotationKey] = constants.RestartTriggerPolicyIgnore
			Expect(testutil.K8sClient.Update(testutil.Ctx, ignorePod)).Should(Succeed())

			// Now simulate container restart on the ignored pod
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(ignorePod), ignorePod)).Should(Succeed())
			testutil.SetPodContainerRestarted(ignorePod, "nginx", 1)
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, ignorePod)).Should(Succeed())

			// Verify pods are NOT recreated (Ignore prevents recreation)
			Consistently(func() map[string]types.UID {
				return getPodUIDs(listRolePods(rbgName, roleName))
			}, 10*time.Second, interval).Should(Equal(originalUIDs),
				"pods should NOT be recreated when only Ignore-annotated pods have failures")
		})
	})

	Context("RestartPolicy=None", func() {
		It("should only replace the failed pod, not recreate all pods", func() {
			rbgName := "test-none-policy"
			roleName := defaultRoleName

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(2).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RestartPolicyNone).Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			waitForPods(rbgName, roleName, 2)
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)

			// Record UIDs
			originalUIDs := getPodUIDs(listRolePods(rbgName, roleName))
			Expect(originalUIDs).Should(HaveLen(2))

			// Fail one pod
			podList := listRolePods(rbgName, roleName)
			survivorName := podList.Items[1].Name
			survivorUID := podList.Items[1].UID

			failPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), failPod)).Should(Succeed())
			failPod.Status.Phase = corev1.PodFailed
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, failPod)).Should(Succeed())

			// Verify the surviving pod is NOT deleted
			Consistently(func() types.UID {
				pod := &corev1.Pod{}
				if err := testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: survivorName, Namespace: testNs}, pod); err != nil {
					return ""
				}
				return pod.UID
			}, 10*time.Second, interval).Should(Equal(survivorUID),
				"with RestartPolicy=None, the surviving pod should NOT be deleted")
		})
	})

	Context("StandalonePattern defaults to RestartPolicy=None", func() {
		It("should not trigger instance recreation when pod fails", func() {
			rbgName := "test-standalone-none"
			roleName := defaultRoleName

			// StandalonePattern does not support RestartPolicy field;
			// it always defaults to None. Verify that pod failure does NOT
			// trigger RoleInstance-level recreation.
			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole(roleName).
						WithReplicas(2).Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			waitForPods(rbgName, roleName, 2)
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)

			// Record UIDs
			originalUIDs := getPodUIDs(listRolePods(rbgName, roleName))
			Expect(originalUIDs).Should(HaveLen(2))

			// Fail one pod
			podList := listRolePods(rbgName, roleName)
			survivorName := podList.Items[1].Name
			survivorUID := podList.Items[1].UID

			failPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), failPod)).Should(Succeed())
			failPod.Status.Phase = corev1.PodFailed
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, failPod)).Should(Succeed())

			// Verify the surviving pod is NOT deleted (no instance-level recreation)
			Consistently(func() types.UID {
				pod := &corev1.Pod{}
				if err := testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: survivorName, Namespace: testNs}, pod); err != nil {
					return ""
				}
				return pod.UID
			}, 10*time.Second, interval).Should(Equal(survivorUID),
				"standalone pattern should default to RestartPolicy=None, surviving pod should NOT be deleted")

			// Verify Restarting condition is NOT set
			Consistently(func() bool {
				ri := getRoleInstance(rbgName, roleName)
				if ri == nil {
					return false
				}
				for _, cond := range ri.Status.Conditions {
					if cond.Type == workloadsv1alpha2.RoleInstanceRestarting && cond.Status == corev1.ConditionTrue {
						return true // Restarting condition found — unexpected
					}
				}
				return false
			}, 10*time.Second, interval).Should(BeFalse(),
				"standalone pattern should not set Restarting condition")
		})
	})

	Context("Container restart (not PodFailed)", func() {
		It("should recreate all pods when container RestartCount > 0", func() {
			rbgName := "test-container-restart"
			roleName := defaultRoleName

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(1).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			waitForPods(rbgName, roleName, 1)
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)

			// Record original UID
			podList := listRolePods(rbgName, roleName)
			originalUID := podList.Items[0].UID

			// Simulate container restart (pod still Running but RestartCount > 0)
			freshPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), freshPod)).Should(Succeed())
			testutil.SetPodContainerRestarted(freshPod, "nginx", 1)
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// Verify pod gets recreated
			Eventually(func() bool {
				podList := listRolePods(rbgName, roleName)
				active := getActivePods(podList)
				for _, p := range active {
					if p.UID == originalUID {
						return false // Original pod still exists
					}
				}
				return len(active) >= 1
			}, timeout, interval).Should(BeTrue(),
				"pod should be recreated when container RestartCount > 0 with RecreateRoleInstanceOnPodRestart")
		})
	})

	Context("Guard conditions", func() {
		It("should not recreate during initial creation (instance not yet Ready)", func() {
			rbgName := "test-guard-init"
			roleName := defaultRoleName

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(2).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Wait for pods to be created but DON'T make them Ready
			waitForPods(rbgName, roleName, 2)

			// Fail one pod while instance is NOT Ready yet
			podList := listRolePods(rbgName, roleName)
			freshPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), freshPod)).Should(Succeed())
			freshPod.Status.Phase = corev1.PodFailed
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// The other pod should NOT be deleted (restart policy should not trigger
			// because instance was never Ready)
			otherPod := &podList.Items[1]
			otherUID := otherPod.UID
			Consistently(func() types.UID {
				pod := &corev1.Pod{}
				if err := testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: otherPod.Name, Namespace: testNs}, pod); err != nil {
					return ""
				}
				return pod.UID
			}, 10*time.Second, interval).Should(Equal(otherUID),
				"restart policy should NOT trigger during initial creation (instance not Ready yet)")
		})
	})

	Context("Multi-role RBG", func() {
		It("should only recreate the affected role's instance, not other roles", func() {
			rbgName := "test-multi-role"

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole("role-1").
						WithReplicas(1).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).Obj(),
					wrappersv2.BuildLeaderWorkerRole("role-2").
						WithReplicas(1).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Wait for both roles' pods
			waitForPods(rbgName, "role-1", 1)
			waitForPods(rbgName, "role-2", 1)

			// Make all pods Ready
			makePodsReady(rbgName, "role-1")
			makePodsReady(rbgName, "role-2")

			waitForRoleInstanceReady(rbgName, "role-1")
			waitForRoleInstanceReady(rbgName, "role-2")

			// Record role-2's pod UID
			role2Pods := listRolePods(rbgName, "role-2")
			role2UID := role2Pods.Items[0].UID

			// Fail role-1's pod
			role1Pods := listRolePods(rbgName, "role-1")
			freshPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&role1Pods.Items[0]), freshPod)).Should(Succeed())
			freshPod.Status.Phase = corev1.PodFailed
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// Verify role-2's pod is NOT affected
			Consistently(func() types.UID {
				pod := &corev1.Pod{}
				if err := testutil.K8sClient.Get(testutil.Ctx,
					types.NamespacedName{Name: role2Pods.Items[0].Name, Namespace: testNs}, pod); err != nil {
					return ""
				}
				return pod.UID
			}, 10*time.Second, interval).Should(Equal(role2UID),
				"role-2's pods should NOT be affected when role-1 triggers restart policy")
		})
	})

	Context("Restarting guard prevents cascading recreations", func() {
		It("should not trigger recreation again during an active restart cycle", func() {
			rbgName := "test-cascade-guard"
			roleName := defaultRoleName

			// Use LeaderWorker (replicas=1, size=2) so we have 1 RoleInstance with
			// 2 pods. The Restarting guard is implicitly tested: during the restart
			// cycle, replacement pods are Pending (not Ready), and the guard prevents
			// the controller from triggering yet another recreation.
			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(1).
						WithSize(2).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			waitForPods(rbgName, roleName, 2)
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)

			// Record original UIDs
			originalUIDs := getPodUIDs(listRolePods(rbgName, roleName))

			// Fail one pod to trigger recreation
			podList := listRolePods(rbgName, roleName)
			freshPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), freshPod)).Should(Succeed())
			freshPod.Status.Phase = corev1.PodFailed
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// Wait for Restarting condition to be set
			Eventually(func() bool {
				ri := getRoleInstance(rbgName, roleName)
				if ri == nil {
					return false
				}
				for _, cond := range ri.Status.Conditions {
					if cond.Type == workloadsv1alpha2.RoleInstanceRestarting && cond.Status == corev1.ConditionTrue {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue(),
				"Restarting condition should be set when restart policy triggers")

			// Wait for replacement pods to appear (all original pods should be gone)
			Eventually(func() bool {
				podList := listRolePods(rbgName, roleName)
				active := getActivePods(podList)
				if len(active) < 2 {
					return false
				}
				for _, p := range active {
					if uid, ok := originalUIDs[p.Name]; ok && uid == p.UID {
						return false // Original pod still around
					}
				}
				return true
			}, timeout, interval).Should(BeTrue(),
				"replacement pods should appear after restart policy recreation")

			// Make replacement pods Ready to recover
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)

			// Verify instance is stable — should stay Ready without further cascading
			Consistently(func() bool {
				ri := getRoleInstance(rbgName, roleName)
				if ri == nil {
					return false
				}
				for _, cond := range ri.Status.Conditions {
					if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == corev1.ConditionTrue {
						return true
					}
				}
				return false
			}, 10*time.Second, interval).Should(BeTrue(),
				"instance should be stable after recovery from restart")
		})
	})

	Context("Exponential Backoff", func() {
		It("should track RestartCount and LastRestartTime after recreation", func() {
			rbgName := "test-backoff-tracking"
			roleName := defaultRoleName

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(1).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
						WithBaseDelaySeconds(2).
						WithMaxDelaySeconds(10).
						Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			waitForPods(rbgName, roleName, 1)
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)

			// Verify initial state: RestartCount=0, LastRestartTime=nil
			ri := getRoleInstance(rbgName, roleName)
			Expect(ri).NotTo(BeNil())
			Expect(ri.Status.RestartCount).Should(Equal(int32(0)))
			Expect(ri.Status.LastRestartTime).Should(BeNil())

			// Fail the pod to trigger recreation
			podList := listRolePods(rbgName, roleName)
			freshPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), freshPod)).Should(Succeed())
			freshPod.Status.Phase = corev1.PodFailed
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// Wait for recreation to happen (pods get new UIDs)
			originalUID := podList.Items[0].UID
			Eventually(func() bool {
				podList := listRolePods(rbgName, roleName)
				active := getActivePods(podList)
				for _, p := range active {
					if p.UID == originalUID {
						return false
					}
				}
				return len(active) >= 1
			}, timeout, interval).Should(BeTrue(),
				"pod should be recreated after failure")

			// Verify RestartCount incremented and LastRestartTime set
			Eventually(func() int32 {
				ri := getRoleInstance(rbgName, roleName)
				if ri == nil {
					return 0
				}
				return ri.Status.RestartCount
			}, timeout, interval).Should(Equal(int32(1)),
				"RestartCount should be 1 after first recreation")

			ri = getRoleInstance(rbgName, roleName)
			Expect(ri.Status.LastRestartTime).ShouldNot(BeNil(),
				"LastRestartTime should be set after recreation")
		})

		It("should delay second recreation due to backoff", func() {
			rbgName := "test-backoff-delay"
			roleName := defaultRoleName
			// Use baseDelay=10 so backoff=20s, long enough to not expire during test setup
			baseDelay := int32(10)

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(1).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
						WithBaseDelaySeconds(baseDelay).
						WithMaxDelaySeconds(60).
						Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			waitForPods(rbgName, roleName, 1)
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)

			// --- First crash: no backoff (RestartCount=0, LastRestartTime=nil) ---
			podList := listRolePods(rbgName, roleName)
			firstUID := podList.Items[0].UID
			freshPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), freshPod)).Should(Succeed())
			freshPod.Status.Phase = corev1.PodFailed
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// Wait for first recreation
			Eventually(func() bool {
				podList := listRolePods(rbgName, roleName)
				active := getActivePods(podList)
				for _, p := range active {
					if p.UID == firstUID {
						return false
					}
				}
				return len(active) >= 1
			}, timeout, interval).Should(BeTrue(),
				"first recreation should happen without backoff")

			// Recover: make replacement pods Ready
			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)

			// Verify RestartCount=1
			Eventually(func() int32 {
				ri := getRoleInstance(rbgName, roleName)
				if ri == nil {
					return 0
				}
				return ri.Status.RestartCount
			}, timeout, interval).Should(Equal(int32(1)))

			// --- Second crash: backoff should delay recreation ---
			podList = listRolePods(rbgName, roleName)
			secondUID := podList.Items[0].UID
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), freshPod)).Should(Succeed())
			freshPod.Status.Phase = corev1.PodFailed
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// During backoff window, the original pod should NOT be deleted.
			// backoff delay = baseDelay * 2^restartCount = 10 * 2^1 = 20 seconds
			// The backoff is measured from LastRestartTime (set during first crash),
			// not from the second crash. So we use a fixed Consistently duration
			// that's shorter than the remaining backoff time.
			// We check by UID because getActivePods filters out Failed pods,
			// but during backoff the failed pod should still exist (not deleted).
			Consistently(func() bool {
				podList := listRolePods(rbgName, roleName)
				for i := range podList.Items {
					if podList.Items[i].UID == secondUID {
						return true // Original pod still exists — backoff working
					}
				}
				return false
			}, 5*time.Second, interval).Should(BeTrue(),
				"pod should NOT be recreated during backoff window")

			// After backoff elapses, recreation should proceed
			Eventually(func() bool {
				podList := listRolePods(rbgName, roleName)
				for i := range podList.Items {
					if podList.Items[i].UID == secondUID {
						return false
					}
				}
				// Original pod gone; verify a replacement exists
				active := getActivePods(podList)
				return len(active) >= 1
			}, timeout, interval).Should(BeTrue(),
				"pod should be recreated after backoff elapses")

			// Verify RestartCount=2
			Eventually(func() int32 {
				ri := getRoleInstance(rbgName, roleName)
				if ri == nil {
					return 0
				}
				return ri.Status.RestartCount
			}, timeout, interval).Should(Equal(int32(2)),
				"RestartCount should be 2 after second recreation")
		})

		It("should propagate BaseDelaySeconds and MaxDelaySeconds from RBG to RoleInstance", func() {
			rbgName := "test-backoff-propagate"
			roleName := defaultRoleName

			rbg := wrappersv2.BuildBasicRoleBasedGroup(rbgName, testNs).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildLeaderWorkerRole(roleName).
						WithReplicas(1).
						WithSize(1).
						WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
						WithBaseDelaySeconds(15).
						WithMaxDelaySeconds(120).
						Obj(),
				}).Obj()

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Wait for RoleInstance to be created and verify delay config propagation
			Eventually(func() bool {
				ri := getRoleInstance(rbgName, roleName)
				if ri == nil {
					return false
				}
				return ri.Spec.BaseDelaySeconds != nil && *ri.Spec.BaseDelaySeconds == 15 &&
					ri.Spec.MaxDelaySeconds != nil && *ri.Spec.MaxDelaySeconds == 120
			}, timeout, interval).Should(BeTrue(),
				"BaseDelaySeconds and MaxDelaySeconds should propagate from RBG to RoleInstance")
		})

		It("should reset RestartCount after stable period", func() {
			rbgName := "test-backoff-reset"
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

			// Trigger first recreation to set RestartCount=1
			podList := listRolePods(rbgName, roleName)
			originalUID := podList.Items[0].UID
			freshPod := &corev1.Pod{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), freshPod)).Should(Succeed())
			freshPod.Status.Phase = corev1.PodFailed
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// Wait for recreation
			Eventually(func() bool {
				podList := listRolePods(rbgName, roleName)
				active := getActivePods(podList)
				for _, p := range active {
					if p.UID == originalUID {
						return false
					}
				}
				return len(active) >= 1
			}, timeout, interval).Should(BeTrue())

			makePodsReady(rbgName, roleName)
			waitForRoleInstanceReady(rbgName, roleName)

			// Verify RestartCount=1
			Eventually(func() int32 {
				ri := getRoleInstance(rbgName, roleName)
				if ri == nil {
					return 0
				}
				return ri.Status.RestartCount
			}, timeout, interval).Should(Equal(int32(1)))

			// Simulate a long stable period by backdating LastRestartTime to 11 minutes ago.
			// The reset threshold is max(maxDelaySeconds*2, 10min) = max(4, 600) = 600s = 10min.
			// Setting LastRestartTime to 11 minutes ago exceeds this threshold.
			ri := getRoleInstance(rbgName, roleName)
			Expect(ri).NotTo(BeNil())
			ri.Status.LastRestartTime.Time = time.Now().Add(-11 * time.Minute)
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, ri)).Should(Succeed())

			// Trigger another recreation
			podList = listRolePods(rbgName, roleName)
			secondUID := podList.Items[0].UID
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				client.ObjectKeyFromObject(&podList.Items[0]), freshPod)).Should(Succeed())
			freshPod.Status.Phase = corev1.PodFailed
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, freshPod)).Should(Succeed())

			// Wait for recreation
			Eventually(func() bool {
				podList := listRolePods(rbgName, roleName)
				active := getActivePods(podList)
				for _, p := range active {
					if p.UID == secondUID {
						return false
					}
				}
				return len(active) >= 1
			}, timeout, interval).Should(BeTrue())

			// RestartCount should be reset to 1 (not 2), because the stable period
			// caused the counter to reset before incrementing.
			Eventually(func() int32 {
				ri := getRoleInstance(rbgName, roleName)
				if ri == nil {
					return 0
				}
				return ri.Status.RestartCount
			}, timeout, interval).Should(Equal(int32(1)),
				"RestartCount should reset to 1 after long stable period (not continue from previous value)")
		})
	})
})

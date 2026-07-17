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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunInactivePodTestCases(f *framework.Framework) {
	runEvictedPodTest(f)
	runFailedPodRecreationTest(f)
	runIgnoredComponentTest(f)
	runNonIgnoredComponentTest(f)
	runRestartingConditionTest(f)
	runRestartPolicyNoneTest(f)
}

// Case 1: Evicted Pod triggers replacement Pod creation
// RoleInstance Controller creates replacement Pod through normal reconciliation.
func runEvictedPodTest(f *framework.Framework) {
	ginkgo.It("evicted pod triggers replacement pod creation", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-evicted-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("role-1").
				WithWorkload("apps/v1", "Deployment").
				WithReplicas(2).
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
	})
}

// Case 2: Failed Pod triggers RoleInstance recreation with RestartPolicy=RecreateRoleInstanceOnPodRestart
// Note: With this policy, RoleInstance Controller recreates the entire affected Instance (not just replacement Pod).
// Only the RoleInstance containing the Failed pod is recreated; other RoleInstances are unaffected.
func runFailedPodRecreationTest(f *framework.Framework) {
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

		// Get one pod and find the target instance
		targetPod := &podList.Items[0]
		targetInstanceName := targetPod.Labels[constants.RoleInstanceNameLabelKey]

		// Record the expected pod count for the target Instance before triggering failure
		instancePodList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, instancePodList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey:        rbg.Name,
				constants.RoleInstanceNameLabelKey: targetInstanceName,
			})).Should(gomega.Succeed())
		expectedPodCount := len(instancePodList.Items)

		// Wait for target RoleInstance to be Ready (required by shouldRecreateInstance)
		gomega.Eventually(func() bool {
			ri := &workloadsv1alpha2.RoleInstance{}
			if err := f.Client.Get(f.Ctx, client.ObjectKey{
				Namespace: f.Namespace,
				Name:      targetInstanceName,
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

		// Re-fetch target pod to get latest resourceVersion
		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKey{
			Namespace: f.Namespace,
			Name:      targetPod.Name,
		}, targetPod)).Should(gomega.Succeed())

		gomega.Expect(utils.SetPodFailed(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Wait for the affected RoleInstance's pods to be fully recreated (new UIDs, correct count)
		// Only the RoleInstance containing the Failed pod should be recreated
		gomega.Eventually(func() bool {
			gomega.Expect(f.Client.List(f.Ctx, podList,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{
					constants.GroupNameLabelKey:        rbg.Name,
					constants.RoleInstanceNameLabelKey: targetInstanceName,
				})).Should(gomega.Succeed())
			if len(podList.Items) != expectedPodCount {
				return false
			}
			// All pods in the affected Instance should have new UIDs
			for _, p := range podList.Items {
				if initialPodUIDs[p.Name] == p.UID {
					return false
				}
			}
			return true
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())

		f.ExpectRbgV2Equal(rbg)
	})
}

// Case 3: Pod Failed with RestartTriggerPolicy=Ignore does NOT trigger RoleInstance recreation
// When a component has the restart-trigger-policy=Ignore annotation, its pod failures
// should not trigger the restart policy for the entire RoleInstance.
func runIgnoredComponentTest(f *framework.Framework) {
	ginkgo.It("ignored component pod failure does NOT trigger RoleInstance recreation", func() {
		// Build a customComponentsPattern RBG with two components:
		// - "main": the primary component (no annotation)
		// - "monitor": auxiliary component with Ignore annotation
		mainTemplate := wrappersv2.BuildBasicPodTemplateSpec()
		monitorTemplate := wrappersv2.BuildBasicPodTemplateSpec()
		monitorTemplate.ObjectMeta = metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
			},
		}

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-ignore-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			{
				Name:     "role-1",
				Replicas: ptr.To(int32(1)),
				Pattern: workloadsv1alpha2.Pattern{
					CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
						RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
						Components: []workloadsv1alpha2.InstanceComponent{
							{
								Name:     "main",
								Size:     ptr.To(int32(1)),
								Template: mainTemplate,
							},
							{
								Name:     "monitor",
								Size:     ptr.To(int32(1)),
								Template: monitorTemplate,
							},
						},
					},
				},
			},
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get all pods and record initial UIDs
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).Should(gomega.HaveLen(2))

		initialPodUIDs := make(map[string]types.UID)
		for _, p := range podList.Items {
			initialPodUIDs[p.Name] = p.UID
		}

		// Find the instance name from pods
		ignoreInstanceName := podList.Items[0].Labels[constants.RoleInstanceNameLabelKey]

		// Wait for target RoleInstance to be Ready (required for shouldRecreateInstance to evaluate)
		gomega.Eventually(func() bool {
			ri := &workloadsv1alpha2.RoleInstance{}
			if err := f.Client.Get(f.Ctx, client.ObjectKey{
				Namespace: f.Namespace,
				Name:      ignoreInstanceName,
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

		// Re-fetch pods to get latest resourceVersion
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())

		// Find the monitor pod (the one with the Ignore annotation)
		var monitorPod *corev1.Pod
		for i := range podList.Items {
			if podList.Items[i].Annotations[constants.RestartTriggerPolicyAnnotationKey] == constants.RestartTriggerPolicyIgnore {
				monitorPod = &podList.Items[i]
				break
			}
		}
		gomega.Expect(monitorPod).ShouldNot(gomega.BeNil(), "monitor pod with Ignore annotation should exist")

		// Simulate monitor pod failure
		gomega.Expect(utils.SetPodFailed(f.Ctx, f.Client, monitorPod)).Should(gomega.Succeed())

		// The main pod should NOT be recreated since the failed pod has Ignore annotation.
		// Wait a bit and verify UIDs of the main pod haven't changed.
		gomega.Consistently(func() bool {
			updatedPodList := &corev1.PodList{}
			if err := f.Client.List(f.Ctx, updatedPodList,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name}); err != nil {
				return false
			}
			for _, p := range updatedPodList.Items {
				// Skip the failed monitor pod itself
				if p.Annotations[constants.RestartTriggerPolicyAnnotationKey] == constants.RestartTriggerPolicyIgnore {
					continue
				}
				// Main pod UID should remain unchanged
				if oldUID, ok := initialPodUIDs[p.Name]; ok && oldUID != p.UID {
					return false
				}
			}
			return true
		}, 15, 2).Should(gomega.BeTrue(), "main pod should NOT be recreated when ignored component fails")
	})
}

// Case 4: Non-ignored component pod failure DOES trigger RoleInstance recreation
func runNonIgnoredComponentTest(f *framework.Framework) {
	ginkgo.It("non-ignored component pod failure triggers RoleInstance recreation", func() {
		mainTemplate := wrappersv2.BuildBasicPodTemplateSpec()
		monitorTemplate := wrappersv2.BuildBasicPodTemplateSpec()
		monitorTemplate.ObjectMeta = metav1.ObjectMeta{
			Annotations: map[string]string{
				constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore,
			},
		}

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-noignore-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			{
				Name:     "role-1",
				Replicas: ptr.To(int32(1)),
				Pattern: workloadsv1alpha2.Pattern{
					CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
						RestartPolicy: workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
						Components: []workloadsv1alpha2.InstanceComponent{
							{
								Name:     "main",
								Size:     ptr.To(int32(1)),
								Template: mainTemplate,
							},
							{
								Name:     "monitor",
								Size:     ptr.To(int32(1)),
								Template: monitorTemplate,
							},
						},
					},
				},
			},
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get all pods and record initial UIDs
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).Should(gomega.HaveLen(2))

		initialPodUIDs := make(map[string]types.UID)
		for _, p := range podList.Items {
			initialPodUIDs[p.Name] = p.UID
		}

		// Find the instance name from pods
		targetInstanceName := podList.Items[0].Labels[constants.RoleInstanceNameLabelKey]

		// Wait for target RoleInstance to be Ready (required by shouldRecreateInstance)
		gomega.Eventually(func() bool {
			ri := &workloadsv1alpha2.RoleInstance{}
			if err := f.Client.Get(f.Ctx, client.ObjectKey{
				Namespace: f.Namespace,
				Name:      targetInstanceName,
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

		// Re-fetch pods to get latest resourceVersion
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())

		// Find the main pod (the one WITHOUT the Ignore annotation)
		var mainPod *corev1.Pod
		for i := range podList.Items {
			if podList.Items[i].Annotations[constants.RestartTriggerPolicyAnnotationKey] != constants.RestartTriggerPolicyIgnore {
				mainPod = &podList.Items[i]
				break
			}
		}
		gomega.Expect(mainPod).ShouldNot(gomega.BeNil(), "main pod without Ignore annotation should exist")

		// Simulate main pod failure
		gomega.Expect(utils.SetPodFailed(f.Ctx, f.Client, mainPod)).Should(gomega.Succeed())

		// The entire RoleInstance should be recreated (all pods get new UIDs)
		gomega.Eventually(func() bool {
			updatedPodList := &corev1.PodList{}
			if err := f.Client.List(f.Ctx, updatedPodList,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name}); err != nil {
				return false
			}
			if len(updatedPodList.Items) != 2 {
				return false
			}
			// All pods should have new UIDs (instance was recreated)
			for _, p := range updatedPodList.Items {
				if oldUID, ok := initialPodUIDs[p.Name]; ok && oldUID == p.UID {
					return false
				}
			}
			return true
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
			"all pods should be recreated when non-ignored component fails")
	})
}

// Case 6: RestartPolicy=None creates replacement pod for inactive pod
func runRestartPolicyNoneTest(f *framework.Framework) {
	ginkgo.It("inactive pod triggers replacement pod creation with RestartPolicy=None", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-none-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("role-1").
				WithWorkload("apps/v1", "Deployment").
				WithReplicas(3).
				Obj(),
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
}

// runRestartingConditionTest verifies that the Restarting condition prevents cascading
// restart-policy recreations. After a recreation triggers, Restarting=True is set;
// it is cleared once the instance becomes Ready again, and no further recreation occurs.
func runRestartingConditionTest(f *framework.Framework) {
	ginkgo.It("restarting condition prevents cascading restart-policy recreations", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-restarting-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildLeaderWorkerRole("role-1").
				WithReplicas(1).
				WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
				Obj(),
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get pods and find the target instance
		podList := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		gomega.Expect(podList.Items).ShouldNot(gomega.BeEmpty())

		targetInstanceName := podList.Items[0].Labels[constants.RoleInstanceNameLabelKey]

		// Wait for the RoleInstance to be Ready (stable state required by shouldRecreateInstance)
		gomega.Eventually(func() bool {
			ri := &workloadsv1alpha2.RoleInstance{}
			if err := f.Client.Get(f.Ctx, client.ObjectKey{
				Namespace: f.Namespace,
				Name:      targetInstanceName,
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
			"RoleInstance should be Ready before we trigger the failure")

		// Record initial pod UIDs to detect recreation
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey:        rbg.Name,
				constants.RoleInstanceNameLabelKey: targetInstanceName,
			})).Should(gomega.Succeed())
		initialPodUIDs := make(map[string]types.UID)
		for _, p := range podList.Items {
			initialPodUIDs[p.Name] = p.UID
		}

		// Re-fetch the target pod to get latest resource version
		targetPod := &corev1.Pod{}
		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKey{
			Namespace: f.Namespace,
			Name:      podList.Items[0].Name,
		}, targetPod)).Should(gomega.Succeed())

		// Simulate pod failure to trigger restart-policy recreation
		gomega.Expect(utils.SetPodFailed(f.Ctx, f.Client, targetPod)).Should(gomega.Succeed())

		// Verify pods are recreated (UIDs change) — this proves the restart policy triggered
		gomega.Eventually(func() bool {
			if err := f.Client.List(f.Ctx, podList,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{
					constants.GroupNameLabelKey:        rbg.Name,
					constants.RoleInstanceNameLabelKey: targetInstanceName,
				}); err != nil {
				return false
			}
			// All pods should have different UIDs from before (full instance recreation)
			for _, p := range podList.Items {
				if oldUID, ok := initialPodUIDs[p.Name]; ok && oldUID == p.UID {
					return false
				}
			}
			return len(podList.Items) > 0
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
			"pods should be recreated with new UIDs after restart-policy triggers")

		// Wait for the instance to become Ready again (pods are recreated and healthy)
		gomega.Eventually(func() bool {
			ri := &workloadsv1alpha2.RoleInstance{}
			if err := f.Client.Get(f.Ctx, client.ObjectKey{
				Namespace: f.Namespace,
				Name:      targetInstanceName,
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
			"RoleInstance should become Ready again after recreation")

		// Once Ready, the Restarting condition should be cleared (not present or False)
		ri := &workloadsv1alpha2.RoleInstance{}
		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKey{
			Namespace: f.Namespace,
			Name:      targetInstanceName,
		}, ri)).Should(gomega.Succeed())

		for _, cond := range ri.Status.Conditions {
			if cond.Type == workloadsv1alpha2.RoleInstanceRestarting {
				gomega.Expect(cond.Status).ShouldNot(gomega.Equal(corev1.ConditionTrue),
					"Restarting condition should not be True after instance becomes Ready")
			}
		}

		// Verify no cascading restarts: pods should remain stable after recovery
		recoveredPodUIDs := getInstancePodUIDs(f, rbg, targetInstanceName)

		// Pods should remain stable (no further recreation cycles)
		gomega.Consistently(func() map[string]types.UID {
			return getInstancePodUIDs(f, rbg, targetInstanceName)
		}, 15, 2).Should(gomega.Equal(recoveredPodUIDs),
			"pods should remain stable after recovery - no cascading restarts")
	})
}

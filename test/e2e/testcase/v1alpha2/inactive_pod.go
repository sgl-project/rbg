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
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunInactivePodTestCases(f *framework.Framework) {
	runInactivePodReplacementTest(f)
	runRestartedPodRecreationTest(f)
	runIgnoredComponentTest(f)
	runNonIgnoredComponentTest(f)
	runRestartingConditionTest(f)
	runRestartPolicyNoneTest(f)
}

// Case 1: a real terminal Failed Pod is replaced through normal reconciliation.
func runInactivePodReplacementTest(f *framework.Framework) {
	ginkgo.It("failed pod triggers replacement pod creation", ginkgo.Label("update", "serial"), func() {
		template := buildFailingPodTemplate()
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inactive-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("role-1").
				WithTemplate(&template).
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

		// Exit a restartPolicy=Never container so kubelet reports PodFailed.
		targetPod := &podList.Items[0]
		failPod(f, targetPod)
		expectOnlyFailedPodReplaced(f, rbg, podList.Items, targetPod)
		f.ExpectRbgV2Equal(rbg)
	})
}

// Case 2: a container restart triggers RoleInstance recreation with RestartPolicy=RecreateRoleInstanceOnPodRestart
// Note: With this policy, RoleInstance Controller recreates the entire affected Instance (not just replacement Pod).
// Only the RoleInstance containing the restarted container is recreated; other RoleInstances are unaffected.
func runRestartedPodRecreationTest(f *framework.Framework) {
	ginkgo.It("container restart triggers RoleInstance recreation with RecreateRoleInstanceOnPodRestart", ginkgo.Label("update", "serial"), func() {
		role := wrappersv2.BuildLeaderWorkerRole("role-1").
			WithReplicas(2).
			WithRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
			Obj()
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-instance-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			role,
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
		gomega.Expect(expectedPodCount).Should(gomega.Equal(2))
		unaffectedPodUIDs := make(map[string]types.UID)
		for _, pod := range podList.Items {
			if pod.Labels[constants.RoleInstanceNameLabelKey] != targetInstanceName {
				unaffectedPodUIDs[pod.Name] = pod.UID
			}
		}
		gomega.Expect(unaffectedPodUIDs).Should(gomega.HaveLen(2))

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

		gomega.Expect(utils.RestartNginxContainer(f.Ctx, targetPod)).Should(gomega.Succeed())

		// Wait for the affected RoleInstance's pods to be fully recreated (new UIDs, correct count)
		// Only the RoleInstance containing the restarted container should be recreated
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
		gomega.Expect(f.Client.List(f.Ctx, podList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
		currentUnaffectedPodUIDs := make(map[string]types.UID)
		for _, pod := range podList.Items {
			if pod.Labels[constants.RoleInstanceNameLabelKey] != targetInstanceName {
				currentUnaffectedPodUIDs[pod.Name] = pod.UID
			}
		}
		gomega.Expect(currentUnaffectedPodUIDs).Should(gomega.Equal(unaffectedPodUIDs),
			"pods in other RoleInstances should not be recreated")
	})
}

// Case 3: a container restart with RestartTriggerPolicy=Ignore does NOT trigger
// RoleInstance recreation, even after the kubelet has restored readiness.
func runIgnoredComponentTest(f *framework.Framework) {
	ginkgo.It("ignored component container restart does NOT trigger RoleInstance recreation", ginkgo.Label("update", "serial"), func() {
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
						RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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

		// Confirm a durable restart on the original Pod. A terminal failure can
		// make the instance unready before the restart-policy check, masking
		// whether Ignore itself prevented instance recreation.
		waitForNginxContainerRestart(f, monitorPod)
		waitForInstanceFullyRecovered(f, ignoreInstanceName)

		// Neither the restarted monitor nor the main Pod should be recreated.
		gomega.Consistently(func() map[string]types.UID {
			return getInstancePodUIDs(f, rbg, ignoreInstanceName)
		}, 15, 2).Should(gomega.Equal(initialPodUIDs), "all Pods should keep their UIDs when an ignored component restarts")
		f.ExpectRbgV2Equal(rbg)
	})
}

// Case 4: a non-ignored component container restart DOES trigger RoleInstance recreation
func runNonIgnoredComponentTest(f *framework.Framework) {
	ginkgo.It("non-ignored component container restart triggers RoleInstance recreation", ginkgo.Label("update", "serial"), func() {
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
						RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart},
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

		gomega.Expect(utils.RestartNginxContainer(f.Ctx, mainPod)).Should(gomega.Succeed())

		// The entire RoleInstance should be recreated (all pods get new UIDs)
		waitForInstancePodsRecreated(f, rbg, targetInstanceName, initialPodUIDs)
		f.ExpectRbgV2Equal(rbg)
	})
}

// Case 6: RestartPolicy=None creates replacement pod for inactive pod
func runRestartPolicyNoneTest(f *framework.Framework) {
	ginkgo.It("inactive pod triggers replacement pod creation with RestartPolicy=None", ginkgo.Label("update", "serial"), func() {
		template := buildFailingPodTemplate()
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-none-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("role-1").
				WithTemplate(&template).
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

		targetPod := &podList.Items[0]
		failPod(f, targetPod)
		expectOnlyFailedPodReplaced(f, rbg, podList.Items, targetPod)
		f.ExpectRbgV2Equal(rbg)
	})
}

// runRestartingConditionTest verifies that the Restarting condition prevents cascading
// restart-policy recreations. After a recreation triggers, Restarting=True is set;
// it is cleared once the instance becomes Ready again, and no further recreation occurs.
func runRestartingConditionTest(f *framework.Framework) {
	ginkgo.It("restarting condition prevents cascading restart-policy recreations", ginkgo.Label("update", "serial"), func() {
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

		// A kubelet-owned container restart triggers restart-policy recreation.
		gomega.Expect(utils.RestartNginxContainer(f.Ctx, targetPod)).Should(gomega.Succeed())

		// Verify pods are recreated (UIDs change) — this proves the restart policy triggered
		waitForInstancePodsRecreated(f, rbg, targetInstanceName, initialPodUIDs)

		// Wait for the instance to become Ready again (pods are recreated and healthy)
		waitForInstanceFullyRecovered(f, targetInstanceName)

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

const podFailureMarker = "/tmp/rbg-e2e-fail"

// buildFailingPodTemplate stays healthy until failPod requests a non-zero exit.
// RestartPolicyNever makes the resulting PodFailed phase a kubelet-owned terminal
// state, so these specs exercise inactive Pod handling rather than container restarts.
func buildFailingPodTemplate() corev1.PodTemplateSpec {
	template := wrappersv2.BuildBasicPodTemplateSpec()
	template.Spec.RestartPolicy = corev1.RestartPolicyNever
	template.Spec.Containers[0].Command = []string{"sh", "-c",
		"trap 'exit 0' TERM INT; while [ ! -f " + podFailureMarker + " ]; do sleep 1; done; exit 1"}
	return template
}

// failPod starts watching before injection so a fast controller cannot delete the
// Failed Pod before the test observes its terminal state.
func failPod(f *framework.Framework, pod *corev1.Pod) {
	ginkgo.GinkgoHelper()
	current := &corev1.Pod{}
	gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(pod), current)).Should(gomega.Succeed())
	gomega.Expect(current.UID).Should(gomega.Equal(pod.UID))
	gomega.Expect(current.Spec.RestartPolicy).Should(gomega.Equal(corev1.RestartPolicyNever))

	ctx, cancel := context.WithTimeout(f.Ctx, utils.Timeout)
	defer cancel()
	podWatch, err := f.Clientset.CoreV1().Pods(pod.Namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector:   fields.OneTermEqualSelector("metadata.name", pod.Name).String(),
		ResourceVersion: current.ResourceVersion,
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer podWatch.Stop()

	gomega.Expect(utils.ExecInPod(ctx, current, "nginx", "touch", podFailureMarker)).Should(gomega.Succeed())
	_, err = watchtools.UntilWithoutRetry(ctx, podWatch, func(event watch.Event) (bool, error) {
		if event.Type == watch.Error {
			return false, fmt.Errorf("pod failure watch: %v", event.Object)
		}
		observed, ok := event.Object.(*corev1.Pod)
		if !ok || observed.UID != pod.UID {
			return false, nil
		}
		if observed.Status.Phase == corev1.PodFailed {
			for _, status := range observed.Status.ContainerStatuses {
				if status.Name == "nginx" && status.State.Terminated != nil && status.State.Terminated.ExitCode == 1 {
					return true, nil
				}
			}
		}
		if event.Type == watch.Deleted {
			return false, fmt.Errorf("pod %s/%s was deleted before its non-zero exit was observed", pod.Namespace, pod.Name)
		}
		return false, nil
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "kubelet should report a terminal Failed Pod after the container exits non-zero")
}

func expectOnlyFailedPodReplaced(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, originalPods []corev1.Pod, failedPod *corev1.Pod) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func() bool {
		pods := &corev1.PodList{}
		if err := f.Client.List(f.Ctx, pods,
			client.InNamespace(rbg.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name}); err != nil {
			return false
		}
		active := filterActivePods(pods.Items)
		if len(active) != len(originalPods) {
			return false
		}
		currentUIDs := make(map[string]types.UID, len(active))
		for _, pod := range active {
			currentUIDs[pod.Name] = pod.UID
		}
		for _, pod := range originalPods {
			uid, exists := currentUIDs[pod.Name]
			if !exists || (pod.UID == failedPod.UID && uid == pod.UID) || (pod.UID != failedPod.UID && uid != pod.UID) {
				return false
			}
		}
		return true
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(), "only the failed Pod should be replaced, with all siblings preserved")
}

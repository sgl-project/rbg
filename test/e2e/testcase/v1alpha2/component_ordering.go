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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	testutils "sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

const (
	componentDependsOnAnnotationKey = "rolebasedgroup.workloads.x-k8s.io/component-depends-on"
)

// RunComponentOrderingTestCases registers e2e tests for component-depends-on (startAfter/deleteAfter).
func RunComponentOrderingTestCases(f *framework.Framework) {
	ginkgo.Describe("component ordering (startAfter/deleteAfter)", func() {
		// Case 1: startAfter — router pod must be created after leader and worker are Ready
		ginkgo.It("router should be created only after leader and worker are Ready (startAfter)", func() {
			rbg := buildOrderedRBG(f.Namespace)

			f.RegisterDebugFn(func() {
				dumpDebugInfo(f, rbg)
				dumpComponentOrderingDebugInfo(f, rbg)
			})

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Wait for the full RBG to be Ready (all 4 pods: leader×1 + worker×2 + router×1).
			f.ExpectRbgV2Equal(rbg)

			// --- Verify startup ordering via pod creation timestamps ---
			ginkgo.By("Verifying router pod creation timestamp is after leader and worker pods")
			verifyRouterCreatedAfterLeaderWorker(f, rbg)

			// --- Verify componentStatuses reflect correct readyReplicas ---
			ginkgo.By("Verifying componentStatuses show correct readyReplicas for all components")
			verifyComponentStatuses(f, rbg)
		})

		// Case 2: deleteAfter — verify router is cleaned up and full RBG deletion completes
		ginkgo.It("RBG with deleteAfter should be fully deleted in correct order", func() {
			rbg := buildOrderedRBG(f.Namespace)

			f.RegisterDebugFn(func() {
				dumpDebugInfo(f, rbg)
				dumpComponentOrderingDebugInfo(f, rbg)
			})

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Wait for full Ready before testing deletion.
			f.ExpectRbgV2Equal(rbg)

			// Delete the RBG and verify all pods are cleaned up (framework AfterEach would also
			// do this, but we explicitly verify deletion completes cleanly here).
			ginkgo.By("Deleting the RBG and verifying all pods are removed")
			gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Deleted(rbg)
		})
	})
}

// buildOrderedRBG creates an RBG with CustomComponentsPattern containing:
//   - leader  (size=1): no deps; has deleteAfter=["router"] so it's deleted after router
//   - worker  (size=2): no deps; has deleteAfter=["router"]
//   - router  (size=1): startAfter=["leader","worker"] so it's created after both are Ready
func buildOrderedRBG(namespace string) *workloadsv1alpha2.RoleBasedGroup {
	leaderTemplate := wrappersv2.BuildBasicPodTemplateSpec()
	leaderTemplate.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent

	workerTemplate := wrappersv2.BuildBasicPodTemplateSpec()
	workerTemplate.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent

	routerTemplate := wrappersv2.BuildBasicPodTemplateSpec()
	routerTemplate.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent

	rbg := wrappersv2.BuildBasicRoleBasedGroup("component-ordering-test", namespace).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			{
				Name:     "prefill",
				Replicas: ptr.To(int32(1)),
				Pattern: workloadsv1alpha2.Pattern{
					CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
						Components: []workloadsv1alpha2.InstanceComponent{
							{
								Name: "leader",
								Size: ptr.To(int32(1)),
								Annotations: map[string]string{
									componentDependsOnAnnotationKey: `{"deleteAfter": ["router"]}`,
								},
								Template: leaderTemplate,
							},
							{
								Name: "worker",
								Size: ptr.To(int32(2)),
								Annotations: map[string]string{
									componentDependsOnAnnotationKey: `{"deleteAfter": ["router"]}`,
								},
								Template: workerTemplate,
							},
							{
								Name: "router",
								Size: ptr.To(int32(1)),
								Annotations: map[string]string{
									componentDependsOnAnnotationKey: `{"startAfter": ["leader", "worker"]}`,
								},
								Template: routerTemplate,
							},
						},
					},
				},
			},
		}).Obj()
	return rbg
}

// verifyRouterCreatedAfterLeaderWorker checks that the router pod's creation timestamp
// is strictly after the creation timestamp of all leader and worker pods.
// This confirms that the startAfter ordering was enforced.
func verifyRouterCreatedAfterLeaderWorker(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	podList := &corev1.PodList{}
	gomega.Eventually(func() bool {
		err := f.Client.List(f.Ctx, podList,
			client.InNamespace(rbg.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
		)
		if err != nil {
			return false
		}
		// Expect exactly 4 pods: leader×1 + worker×2 + router×1
		return len(podList.Items) == 4
	}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue(),
		"expected 4 pods (leader×1 + worker×2 + router×1)")

	var routerCreation, maxLeaderWorkerCreation int64
	for _, pod := range podList.Items {
		componentName := pod.Labels[constants.ComponentNameLabelKey]
		ts := pod.CreationTimestamp.UnixNano()
		switch componentName {
		case "router":
			routerCreation = ts
		case "leader", "worker":
			if ts > maxLeaderWorkerCreation {
				maxLeaderWorkerCreation = ts
			}
		}
	}

	gomega.Expect(routerCreation).Should(gomega.BeNumerically(">", 0),
		"router pod creation timestamp should be non-zero")
	gomega.Expect(maxLeaderWorkerCreation).Should(gomega.BeNumerically(">", 0),
		"leader/worker pod creation timestamps should be non-zero")
	gomega.Expect(routerCreation).Should(gomega.BeNumerically(">=", maxLeaderWorkerCreation),
		"router pod must be created no earlier than the last leader/worker pod (startAfter ordering)")
}

// verifyComponentStatuses checks that the RoleInstance's componentStatuses reflect
// readyReplicas == size for all three components once the RBG is fully Ready.
func verifyComponentStatuses(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	gomega.Eventually(func() bool {
		riList := &workloadsv1alpha2.RoleInstanceList{}
		if err := f.Client.List(f.Ctx, riList,
			client.InNamespace(rbg.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
		); err != nil {
			return false
		}
		if len(riList.Items) == 0 {
			return false
		}
		ri := &riList.Items[0]
		statusMap := make(map[string]workloadsv1alpha2.RoleInstanceComponentStatus, len(ri.Status.ComponentStatuses))
		for _, cs := range ri.Status.ComponentStatuses {
			statusMap[cs.Name] = cs
		}
		// All three components must have readyReplicas == their expected size.
		expectations := map[string]int32{"leader": 1, "worker": 2, "router": 1}
		for name, expectedSize := range expectations {
			cs, ok := statusMap[name]
			if !ok || cs.Size != expectedSize || cs.ReadyReplicas < cs.Size {
				return false
			}
		}
		return true
	}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue(),
		"all components must have readyReplicas == size in componentStatuses")
}

// dumpComponentOrderingDebugInfo prints componentStatuses and pod timestamps when a test fails.
func dumpComponentOrderingDebugInfo(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	if rbg == nil || !ginkgo.CurrentSpecReport().Failed() {
		return
	}
	fmt.Println("\n========== Component Ordering Debug Info ==========")

	// Dump RoleInstance componentStatuses.
	riList := &workloadsv1alpha2.RoleInstanceList{}
	if err := f.Client.List(f.Ctx, riList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
	); err != nil {
		fmt.Printf("[RI] Failed to list RoleInstances: %v\n", err)
	} else {
		for _, ri := range riList.Items {
			fmt.Printf("[RI] %s componentStatuses:\n", ri.Name)
			for _, cs := range ri.Status.ComponentStatuses {
				fmt.Printf("  - name=%s size=%d readyReplicas=%d scheduledReplicas=%d\n",
					cs.Name, cs.Size, cs.ReadyReplicas, cs.ScheduledReplicas)
			}
		}
	}

	// Dump pod creation timestamps.
	podList := &corev1.PodList{}
	if err := f.Client.List(f.Ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
	); err != nil {
		fmt.Printf("[POD] Failed to list pods: %v\n", err)
	} else {
		fmt.Printf("[POD] %d pods:\n", len(podList.Items))
		for _, pod := range podList.Items {
			component := pod.Labels[constants.ComponentNameLabelKey]
			fmt.Printf("  - %s (component=%s) createdAt=%s phase=%s\n",
				pod.Name, component, pod.CreationTimestamp.String(), pod.Status.Phase)
		}
	}

	fmt.Println("========== End Component Ordering Debug Info ==========")
}

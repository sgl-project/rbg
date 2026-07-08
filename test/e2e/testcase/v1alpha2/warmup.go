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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

const (
	LabelWarmupName = "workloads.x-k8s.io/warmup-name"
	LabelWarmupUID  = "workloads.x-k8s.io/warmup-uid"
	LabelNodeName   = "workloads.x-k8s.io/node-name"
)

// RunWarmupTestCases runs all warmup e2e test cases.
func RunWarmupTestCases(f *framework.Framework) {
	ginkgo.Describe("warmup", func() {

		ginkgo.It("should complete warmup job with targetNodes mode", func() {
			ginkgo.By("Getting a node name")
			nodeName := getFirstAvailableNodeName(f)

			ginkgo.By("Creating warmup CR with targetNodes mode")
			warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "warmup-targetnodes-test",
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{
						TTLSecondsAfterFinished: ptr.To(int32(60)),
					},
					TargetNodes: &workloadsv1alpha2.TargetNodes{
						NodeNames: []string{nodeName},
						WarmupActions: workloadsv1alpha2.WarmupActions{
							ImagePreload: &workloadsv1alpha2.ImagePreloadAction{
								Images: []string{utils.DefaultImage},
							},
						},
					},
				},
			}

			f.RegisterDebugFn(func() {
				dumpWarmupDebugInfo(f, warmup)
			})

			gomega.Expect(f.Client.Create(f.Ctx, warmup)).Should(gomega.Succeed())

			// Wait for warmup to complete
			gomega.Eventually(func() bool {
				err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(warmup), warmup)
				if err != nil {
					return false
				}
				return warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseCompleted
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(), "warmup job should complete")

			// Verify status
			gomega.Expect(warmup.Status.Desired).To(gomega.Equal(int32(1)))
			gomega.Expect(warmup.Status.Succeeded).To(gomega.Equal(int32(1)))
			gomega.Expect(warmup.Status.Failed).To(gomega.Equal(int32(0)))
			gomega.Expect(warmup.Status.CompletionTime).ToNot(gomega.BeNil())

			// Verify warmup Pod was created
			podList := &corev1.PodList{}
			err := f.Client.List(f.Ctx, podList,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{LabelWarmupName: warmup.Name})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(podList.Items).To(gomega.HaveLen(1))
			gomega.Expect(podList.Items[0].Status.Phase).To(gomega.Equal(corev1.PodSucceeded))
		})

		ginkgo.It("should complete warmup job with targetRoleBasedGroup mode and merge multi-role actions", func() {
			ginkgo.By("Creating RBG with 2 roles")
			podTemplate := buildWarmupPodTemplate()
			rbg := wrappersv2.BuildBasicRoleBasedGroup("warmup-rbg-test", f.Namespace).
				WithRoles([]workloadsv1alpha2.RoleSpec{
					{
						Name: "prefill",
						Pattern: workloadsv1alpha2.Pattern{
							StandalonePattern: &workloadsv1alpha2.StandalonePattern{
								TemplateSource: workloadsv1alpha2.TemplateSource{
									Template: &podTemplate,
								},
							},
						},
					},
					{
						Name: "decode",
						Pattern: workloadsv1alpha2.Pattern{
							StandalonePattern: &workloadsv1alpha2.StandalonePattern{
								TemplateSource: workloadsv1alpha2.TemplateSource{
									Template: &podTemplate,
								},
							},
						},
					},
				}).Obj()
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Wait for RBG pods to be scheduled
			ginkgo.By("Waiting for RBG pods to be scheduled")
			gomega.Eventually(func() bool {
				podList := &corev1.PodList{}
				err := f.Client.List(f.Ctx, podList,
					client.InNamespace(f.Namespace),
					client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})
				if err != nil || len(podList.Items) < 2 {
					return false
				}
				for _, p := range podList.Items {
					if p.Spec.NodeName == "" {
						return false
					}
				}
				return true
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(), "RBG pods should be scheduled")

			// Get the node where both pods are running
			ginkgo.By("Getting node name from RBG pods")
			podList := &corev1.PodList{}
			gomega.Expect(f.Client.List(f.Ctx, podList,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).Should(gomega.Succeed())
			nodeName := podList.Items[0].Spec.NodeName

			ginkgo.By("Creating warmup CR with targetRoleBasedGroup mode")
			warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "warmup-targetrbg-test",
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{
						TTLSecondsAfterFinished: ptr.To(int32(60)),
					},
					TargetRoleBasedGroup: &workloadsv1alpha2.TargetRoleBasedGroup{
						Name: rbg.Name,
						Roles: map[string]workloadsv1alpha2.WarmupActions{
							"prefill": {
								ImagePreload: &workloadsv1alpha2.ImagePreloadAction{
									Images: []string{utils.DefaultImage},
								},
							},
							"decode": {
								CustomizedAction: &workloadsv1alpha2.CustomizedAction{
									Containers: []corev1.Container{
										{
											Name:            "decode-task",
											Image:           utils.DefaultImage,
											ImagePullPolicy: corev1.PullIfNotPresent,
											Command:         []string{"sh", "-c", "exit 0"},
										},
									},
								},
							},
						},
					},
				},
			}

			f.RegisterDebugFn(func() {
				dumpWarmupDebugInfo(f, warmup)
			})

			gomega.Expect(f.Client.Create(f.Ctx, warmup)).Should(gomega.Succeed())

			// Wait for warmup to complete
			gomega.Eventually(func() bool {
				err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(warmup), warmup)
				if err != nil {
					return false
				}
				return warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseCompleted
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(), "warmup job should complete")

			// Verify warmup Pod was created for the node
			warmupPodList := &corev1.PodList{}
			err := f.Client.List(f.Ctx, warmupPodList,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{LabelWarmupName: warmup.Name})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			// Should have 1 Pod per unique node (both RBG pods on same node → 1 warmup Pod with 2 containers)
			gomega.Expect(warmupPodList.Items).To(gomega.HaveLen(1))
			warmupPod := warmupPodList.Items[0]
			gomega.Expect(warmupPod.Labels[LabelNodeName]).To(gomega.Equal(nodeName))

			// Verify 2 containers (1 image-preload from prefill + 1 customized action from decode)
			gomega.Expect(warmupPod.Spec.Containers).To(gomega.HaveLen(2))

			// Verify status
			gomega.Expect(warmup.Status.Desired).To(gomega.Equal(int32(1)))
			gomega.Expect(warmup.Status.Succeeded).To(gomega.Equal(int32(1)))
			gomega.Expect(warmup.Status.CompletionTime).ToNot(gomega.BeNil())
		})

		ginkgo.It("auto-delete warmup CR after TTL expires", func() {
			ginkgo.By("Getting a node name")
			nodeName := getFirstAvailableNodeName(f)

			ginkgo.By("Creating warmup CR with short TTL")
			warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "warmup-ttl-test",
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{
						TTLSecondsAfterFinished: ptr.To(int32(10)),
					},
					TargetNodes: &workloadsv1alpha2.TargetNodes{
						NodeNames: []string{nodeName},
						WarmupActions: workloadsv1alpha2.WarmupActions{
							ImagePreload: &workloadsv1alpha2.ImagePreloadAction{
								Images: []string{utils.DefaultImage},
							},
						},
					},
				},
			}

			f.RegisterDebugFn(func() {
				dumpWarmupDebugInfo(f, warmup)
			})

			gomega.Expect(f.Client.Create(f.Ctx, warmup)).Should(gomega.Succeed())

			// Wait for warmup to complete
			gomega.Eventually(func() bool {
				err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(warmup), warmup)
				if err != nil {
					return false
				}
				return warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseCompleted
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(), "warmup job should complete")

			// Wait for TTL to expire and CR to be deleted
			ginkgo.By("Waiting for TTL to expire and CR to be deleted")
			gomega.Eventually(func() bool {
				err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(warmup), warmup)
				return client.IgnoreNotFound(err) == nil && err != nil
			}, 30*time.Second, utils.Interval).Should(gomega.BeTrue(), "warmup CR should be deleted after TTL")

			// Verify owned Pods are also deleted (cascade)
			gomega.Eventually(func() int {
				podList := &corev1.PodList{}
				err := f.Client.List(f.Ctx, podList,
					client.InNamespace(f.Namespace),
					client.MatchingLabels{LabelWarmupName: warmup.Name})
				if err != nil {
					return -1
				}
				return len(podList.Items)
			}, utils.Timeout, utils.Interval).Should(gomega.Equal(0), "warmup Pods should be cascade deleted")
		})

		ginkgo.It("fail warmup job when global timeout is exceeded", func() {
			ginkgo.By("Getting a node name")
			nodeName := getFirstAvailableNodeName(f)

			ginkgo.By("Creating warmup CR with global timeout and non-existent image")
			warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "warmup-timeout-test",
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{
						GlobalTimeoutSeconds: ptr.To(int64(30)),
					},
					TargetNodes: &workloadsv1alpha2.TargetNodes{
						NodeNames: []string{nodeName},
						WarmupActions: workloadsv1alpha2.WarmupActions{
							ImagePreload: &workloadsv1alpha2.ImagePreloadAction{
								Images: []string{"nonexistent-registry.example.com/fake-image:v999"},
							},
						},
					},
				},
			}

			f.RegisterDebugFn(func() {
				dumpWarmupDebugInfo(f, warmup)
			})

			gomega.Expect(f.Client.Create(f.Ctx, warmup)).Should(gomega.Succeed())

			// Wait for warmup to fail due to timeout
			ginkgo.By("Waiting for global timeout to trigger")
			gomega.Eventually(func() bool {
				err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(warmup), warmup)
				if err != nil {
					return false
				}
				return warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseFailed
			}, 60*time.Second, utils.Interval).Should(gomega.BeTrue(), "warmup job should fail due to timeout")

			// Verify Failed condition
			gomega.Expect(warmup.Status.CompletionTime).ToNot(gomega.BeNil())
			var foundCondition bool
			for _, c := range warmup.Status.Conditions {
				if c.Type == "Failed" && c.Status == metav1.ConditionTrue {
					foundCondition = true
					gomega.Expect(c.Reason).To(gomega.Equal("GlobalTimeoutExceeded"))
				}
			}
			gomega.Expect(foundCondition).To(gomega.BeTrue(), "should have Failed condition with GlobalTimeoutExceeded reason")
		})

		ginkgo.It("fail warmup job when retry limit is reached", func() {
			ginkgo.By("Getting a node name")
			nodeName := getFirstAvailableNodeName(f)

			ginkgo.By("Creating warmup CR with backoffLimitPerNode=1 and maxFailedNodes=0")
			warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "warmup-retry-test",
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{
						BackoffLimitPerNode: ptr.To(int32(1)),
						MaxFailedNodes:      ptr.To(int32(0)),
					},
					TargetNodes: &workloadsv1alpha2.TargetNodes{
						NodeNames: []string{nodeName},
						WarmupActions: workloadsv1alpha2.WarmupActions{
							CustomizedAction: &workloadsv1alpha2.CustomizedAction{
								Containers: []corev1.Container{
									{
										Name:    "failing-task",
										Image:   utils.DefaultImage,
										Command: []string{"sh", "-c", "sleep 3; exit 1"},
									},
								},
							},
						},
					},
				},
			}

			f.RegisterDebugFn(func() {
				dumpWarmupDebugInfo(f, warmup)
			})

			gomega.Expect(f.Client.Create(f.Ctx, warmup)).Should(gomega.Succeed())

			// Wait for warmup to fail: 2 failures (initial + 1 retry) → permanent failure → maxFailedNodes=0 → job fails
			ginkgo.By("Waiting for retry limit and maxFailedNodes to trigger")
			gomega.Eventually(func() bool {
				err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(warmup), warmup)
				if err != nil {
					return false
				}
				return warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseFailed
			}, 60*time.Second, utils.Interval).Should(gomega.BeTrue(), "warmup job should fail after retry limit")

			// Verify status
			gomega.Expect(warmup.Status.Failed).To(gomega.Equal(int32(1)))
			gomega.Expect(warmup.Status.CompletionTime).ToNot(gomega.BeNil())

			// Verify Failed condition
			var foundCondition bool
			for _, c := range warmup.Status.Conditions {
				if c.Type == "Failed" && c.Status == metav1.ConditionTrue {
					foundCondition = true
					gomega.Expect(c.Reason).To(gomega.Equal("MaxFailedNodesExceeded"))
				}
			}
			gomega.Expect(foundCondition).To(gomega.BeTrue(), "should have Failed condition with MaxFailedNodesExceeded reason")
		})

		ginkgo.It("should complete immediately when no nodes match the target", func() {
			ginkgo.By("Creating warmup CR with nodeSelector that matches no nodes")
			warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "warmup-no-nodes-test",
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{
						TTLSecondsAfterFinished: ptr.To(int32(60)),
					},
					TargetNodes: &workloadsv1alpha2.TargetNodes{
						NodeSelector: map[string]string{
							"nonexistent-label-key": "nonexistent-value",
						},
						WarmupActions: workloadsv1alpha2.WarmupActions{
							ImagePreload: &workloadsv1alpha2.ImagePreloadAction{
								Images: []string{utils.DefaultImage},
							},
						},
					},
				},
			}

			f.RegisterDebugFn(func() {
				dumpWarmupDebugInfo(f, warmup)
			})

			gomega.Expect(f.Client.Create(f.Ctx, warmup)).Should(gomega.Succeed())

			// Wait for warmup to complete with NoNodesMatched
			ginkgo.By("Waiting for warmup to complete with NoNodesMatched")
			gomega.Eventually(func() bool {
				err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(warmup), warmup)
				if err != nil {
					return false
				}
				return warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseCompleted
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(), "warmup job should complete immediately")

			// Verify status
			gomega.Expect(warmup.Status.Desired).To(gomega.Equal(int32(0)))
			gomega.Expect(warmup.Status.Succeeded).To(gomega.Equal(int32(0)))
			gomega.Expect(warmup.Status.Failed).To(gomega.Equal(int32(0)))
			gomega.Expect(warmup.Status.CompletionTime).ToNot(gomega.BeNil())

			// Verify Complete condition with NoNodesMatched reason
			var foundCondition bool
			for _, c := range warmup.Status.Conditions {
				if c.Type == "Complete" && c.Status == metav1.ConditionTrue {
					foundCondition = true
					gomega.Expect(c.Reason).To(gomega.Equal("NoNodesMatched"))
				}
			}
			gomega.Expect(foundCondition).To(gomega.BeTrue(), "should have Complete condition with NoNodesMatched reason")
		})
	})
}

// getFirstAvailableNodeName returns the name of the first available node.
func getFirstAvailableNodeName(f *framework.Framework) string {
	nodeList := &corev1.NodeList{}
	gomega.Expect(f.Client.List(f.Ctx, nodeList)).Should(gomega.Succeed())

	// First try to find a worker node
	for _, node := range nodeList.Items {
		if _, isControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]; isControlPlane {
			continue
		}
		return node.Name
	}
	// Fallback: use any available node
	if len(nodeList.Items) > 0 {
		return nodeList.Items[0].Name
	}
	ginkgo.Fail("no nodes found in the cluster")
	return ""
}

// buildWarmupPodTemplate creates a basic pod template for RBG roles.
func buildWarmupPodTemplate() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{
				{
					Name:            "engine",
					Image:           utils.DefaultImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{"sleep", "3600"},
				},
			},
		},
	}
}

// dumpWarmupDebugInfo logs debug information for warmup tests.
func dumpWarmupDebugInfo(f *framework.Framework, warmup *workloadsv1alpha2.RoleBasedGroupWarmup) {
	logger := log.FromContext(f.Ctx)

	if err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(warmup), warmup); err != nil {
		logger.Error(err, "failed to get warmup for debug info")
		return
	}

	logger.Info("Warmup debug info",
		"name", warmup.Name,
		"phase", warmup.Status.Phase,
		"desired", warmup.Status.Desired,
		"active", warmup.Status.Active,
		"succeeded", warmup.Status.Succeeded,
		"failed", warmup.Status.Failed,
		"conditions", fmt.Sprintf("%+v", warmup.Status.Conditions))

	// List warmup pods
	podList := &corev1.PodList{}
	if err := f.Client.List(f.Ctx, podList,
		client.InNamespace(f.Namespace),
		client.MatchingLabels{LabelWarmupName: warmup.Name}); err != nil {
		logger.Error(err, "failed to list warmup pods")
	} else {
		for _, pod := range podList.Items {
			logger.Info("Warmup pod",
				"name", pod.Name,
				"phase", pod.Status.Phase,
				"node", pod.Spec.NodeName)
		}
	}
}

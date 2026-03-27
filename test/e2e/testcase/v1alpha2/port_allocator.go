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
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

const (
	portAllocatorAnnotationKey = "rolebasedgroup.workloads.x-k8s.io/port-allocator"
)

func RunPortAllocatorTestCases(f *framework.Framework) {
	ginkgo.Describe("port allocator", func() {
		ginkgo.It("should allocate ports and store in annotations", func() {
			ginkgo.By("Creating RBG with port-allocator annotation")
			rbg := buildPortAllocatorTestRBG(f.Namespace)

			f.RegisterDebugFn(func() {
				dumpDebugInfo(f, rbg)
				dumpPortAllocatorDebugInfo(f, rbg)
			})

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Wait for RBG to be ready
			f.ExpectRbgV2Equal(rbg)

			// Step 1: Verify RoleInstanceSet has RoleScoped port annotation
			ginkgo.By("Verifying RoleInstanceSet has RoleScoped port annotation")
			verifyRoleInstanceSetHasRoleScopedPort(f, rbg, "leader.leader-port-static")

			// Step 2: Verify RoleInstance has both RoleScoped and PodScoped port annotations
			ginkgo.By("Verifying RoleInstance has port annotations")
			verifyRoleInstanceHasPortAnnotations(f, rbg)

			// Step 3: Verify leader pod has env vars and annotations
			ginkgo.By("Verifying leader pod has env vars and annotations")
			verifyLeaderPodHasPorts(f, rbg)

			// Step 4: Verify worker pod has reference env var
			ginkgo.By("Verifying worker pod has reference env var")
			verifyWorkerPodHasReferencePort(f, rbg)
		})
	})
}

func buildPortAllocatorTestRBG(namespace string) *workloadsv1alpha2.RoleBasedGroup {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("port-allocator-test", namespace).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			{
				Name: "prefill",
				Workload: workloadsv1alpha2.WorkloadSpec{
					APIVersion: "workloads.x-k8s.io/v1alpha2",
					Kind:       "RoleInstanceSet",
				},
				Pattern: workloadsv1alpha2.Pattern{
					CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
						Components: []workloadsv1alpha2.InstanceComponent{
							buildLeaderComponent(),
							buildWorkerComponent(),
						},
					},
				},
			},
		}).Obj()
	return rbg
}

func buildLeaderComponent() workloadsv1alpha2.InstanceComponent {
	template := wrappersv2.BuildBasicPodTemplateSpec()
	template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	template.Annotations = map[string]string{
		portAllocatorAnnotationKey: `{
			"allocations": [
				{
					"name": "leader-port",
					"env": "LEADER_PORT",
					"annotationKey": "test/grpc-port",
					"scope": "PodScoped"
				},
				{
					"name": "leader-port-static",
					"env": "LEADER_PORT_STATIC",
					"annotationKey": "test/grpc-port-static",
					"scope": "RoleScoped"
				}
			]
		}`,
	}

	return workloadsv1alpha2.InstanceComponent{
		Name:     "leader",
		Size:     ptr.To(int32(1)),
		Template: template,
	}
}

func buildWorkerComponent() workloadsv1alpha2.InstanceComponent {
	template := wrappersv2.BuildBasicPodTemplateSpec()
	template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	template.Annotations = map[string]string{
		portAllocatorAnnotationKey: `{
			"references": [
				{
					"env": "LEADER_ADDR_PORT",
					"from": "prefill.leader.leader-port"
				}
			]
		}`,
	}

	return workloadsv1alpha2.InstanceComponent{
		Name:     "worker",
		Size:     ptr.To(int32(1)),
		Template: template,
	}
}

func verifyRoleInstanceSetHasRoleScopedPort(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, expectedKey string) {
	logger := log.FromContext(f.Ctx)

	gomega.Eventually(func() bool {
		risList := &workloadsv1alpha2.RoleInstanceSetList{}
		err := f.Client.List(f.Ctx, risList,
			client.InNamespace(rbg.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbg.Name,
			},
		)
		if err != nil {
			logger.Error(err, "Failed to list RoleInstanceSets")
			return false
		}

		for i := range risList.Items {
			item := &risList.Items[i]
			if item.Annotations[expectedKey] != "" {
				logger.V(1).Info("Found RoleScoped port annotation in RoleInstanceSet",
					"name", item.Name,
					"key", expectedKey,
					"value", item.Annotations[expectedKey])
				return true
			}
		}

		logger.V(1).Info("Waiting for RoleInstanceSet to have RoleScoped port annotation", "key", expectedKey)
		return false
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

func verifyRoleInstanceHasPortAnnotations(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	logger := log.FromContext(f.Ctx)

	gomega.Eventually(func() bool {
		riList := &workloadsv1alpha2.RoleInstanceList{}
		err := f.Client.List(f.Ctx, riList,
			client.InNamespace(rbg.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbg.Name,
			},
		)
		if err != nil {
			logger.Error(err, "Failed to list RoleInstances")
			return false
		}

		for _, ri := range riList.Items {
			// Check for RoleScoped port annotation (leader.leader-port-static)
			roleScopedValue := ri.Annotations["leader.leader-port-static"]
			if roleScopedValue == "" {
				logger.V(1).Info("RoleInstance missing RoleScoped port annotation", "name", ri.Name)
				continue
			}

			// Check for PodScoped port annotation (pod-name.leader-port)
			// Find any key that matches pattern *.leader-port
			foundPodScoped := false
			for key, value := range ri.Annotations {
				if len(key) > len(".leader-port") && key[len(key)-len(".leader-port"):] == ".leader-port" && value != "" {
					logger.V(1).Info("Found PodScoped port annotation in RoleInstance",
						"name", ri.Name,
						"key", key,
						"value", value)
					foundPodScoped = true
					break
				}
			}

			if !foundPodScoped {
				logger.V(1).Info("RoleInstance missing PodScoped port annotation", "name", ri.Name)
				continue
			}

			logger.V(1).Info("RoleInstance has all expected port annotations", "name", ri.Name)
			return true
		}

		logger.V(1).Info("Waiting for RoleInstance to have port annotations")
		return false
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

func verifyLeaderPodHasPorts(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	logger := log.FromContext(f.Ctx)

	gomega.Eventually(func() bool {
		podList := &corev1.PodList{}
		err := f.Client.List(f.Ctx, podList,
			client.InNamespace(rbg.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbg.Name,
			},
		)
		if err != nil {
			logger.Error(err, "Failed to list pods")
			return false
		}

		for _, pod := range podList.Items {
			if containsLeader(pod.Name) {
				// Check annotations
				grpcPort := pod.Annotations["test/grpc-port"]
				grpcPortStatic := pod.Annotations["test/grpc-port-static"]
				if grpcPort == "" || grpcPortStatic == "" {
					logger.V(1).Info("Leader pod missing port annotations",
						"pod", pod.Name,
						"test/grpc-port", grpcPort,
						"test/grpc-port-static", grpcPortStatic)
					return false
				}

				// Check env vars
				foundLeaderPort := false
				foundLeaderPortStatic := false
				for _, container := range pod.Spec.Containers {
					for _, envVar := range container.Env {
						if envVar.Name == "LEADER_PORT" && envVar.Value != "" {
							foundLeaderPort = true
							logger.V(1).Info("Found LEADER_PORT env var", "pod", pod.Name, "value", envVar.Value)
						}
						if envVar.Name == "LEADER_PORT_STATIC" && envVar.Value != "" {
							foundLeaderPortStatic = true
							logger.V(1).Info("Found LEADER_PORT_STATIC env var", "pod", pod.Name, "value", envVar.Value)
						}
					}
				}

				if !foundLeaderPort || !foundLeaderPortStatic {
					logger.V(1).Info("Leader pod missing env vars",
						"pod", pod.Name,
						"foundLeaderPort", foundLeaderPort,
						"foundLeaderPortStatic", foundLeaderPortStatic)
					return false
				}

				logger.V(1).Info("Leader pod has all expected ports", "pod", pod.Name)
				return true
			}
		}

		logger.V(1).Info("Leader pod not found")
		return false
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

func verifyWorkerPodHasReferencePort(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	logger := log.FromContext(f.Ctx)

	gomega.Eventually(func() bool {
		podList := &corev1.PodList{}
		err := f.Client.List(f.Ctx, podList,
			client.InNamespace(rbg.Namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey: rbg.Name,
			},
		)
		if err != nil {
			logger.Error(err, "Failed to list pods")
			return false
		}

		for _, pod := range podList.Items {
			if containsWorker(pod.Name) {
				// Check env var for reference
				for _, container := range pod.Spec.Containers {
					for _, envVar := range container.Env {
						if envVar.Name == "LEADER_ADDR_PORT" && envVar.Value != "" {
							logger.V(1).Info("Found LEADER_ADDR_PORT env var in worker pod",
								"pod", pod.Name,
								"value", envVar.Value)
							return true
						}
					}
				}

				logger.V(1).Info("Worker pod missing LEADER_ADDR_PORT env var", "pod", pod.Name)
				return false
			}
		}

		logger.V(1).Info("Worker pod not found")
		return false
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

func dumpPortAllocatorDebugInfo(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	if rbg == nil {
		return
	}
	if !ginkgo.CurrentSpecReport().Failed() {
		return
	}

	fmt.Println("\n========== Port Allocator Debug Info ==========")

	// Dump RoleInstanceSets
	risList := &workloadsv1alpha2.RoleInstanceSetList{}
	if err := f.Client.List(f.Ctx, risList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
		},
	); err != nil {
		fmt.Printf("[RIS] Failed to list RoleInstanceSets: %v\n", err)
	} else {
		fmt.Printf("[RIS] Found %d RoleInstanceSets\n", len(risList.Items))
		for _, ris := range risList.Items {
			fmt.Printf("  - Name: %s\n", ris.Name)
			fmt.Printf("    Annotations:\n")
			for k, v := range ris.Annotations {
				fmt.Printf("      %s: %s\n", k, v)
			}
		}
	}

	// Dump RoleInstances
	riList := &workloadsv1alpha2.RoleInstanceList{}
	if err := f.Client.List(f.Ctx, riList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
		},
	); err != nil {
		fmt.Printf("[RI] Failed to list RoleInstances: %v\n", err)
	} else {
		fmt.Printf("[RI] Found %d RoleInstances\n", len(riList.Items))
		for _, ri := range riList.Items {
			fmt.Printf("  - Name: %s\n", ri.Name)
			fmt.Printf("    Annotations:\n")
			for k, v := range ri.Annotations {
				fmt.Printf("      %s: %s\n", k, v)
			}
		}
	}

	// Dump Pods with port info
	podList := &corev1.PodList{}
	if err := f.Client.List(f.Ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
		},
	); err != nil {
		fmt.Printf("[POD] Failed to list pods: %v\n", err)
	} else {
		fmt.Printf("[POD] Found %d pods\n", len(podList.Items))
		for _, pod := range podList.Items {
			fmt.Printf("  - Name: %s\n", pod.Name)
			fmt.Printf("    Annotations:\n")
			for k, v := range pod.Annotations {
				fmt.Printf("      %s: %s\n", k, v)
			}
			fmt.Printf("    Env vars:\n")
			for _, container := range pod.Spec.Containers {
				for _, env := range container.Env {
					fmt.Printf("      %s = %s\n", env.Name, env.Value)
				}
			}
		}
	}

	fmt.Println("========== End Port Allocator Debug Info ==========")
}

func containsLeader(podName string) bool {
	return containsSubstring(podName, "-leader-")
}

func containsWorker(podName string) bool {
	return containsSubstring(podName, "-worker-")
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

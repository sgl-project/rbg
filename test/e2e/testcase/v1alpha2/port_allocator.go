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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	portallocator "sigs.k8s.io/rbgs/pkg/port-allocator"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

const (
	portAllocatorAnnotationKey = "rolebasedgroup.workloads.x-k8s.io/port-allocator"
)

func RunPortAllocatorTestCases(f *framework.Framework) {
	ginkgo.Describe("port allocator", func() {
		ginkgo.It("should allocate ports when annotation is added to existing RBG", func() {
			rbg := buildPortAllocatorTestRBG(f.Namespace, false)

			f.RegisterDebugFn(func() {
				dumpDebugInfo(f, rbg)
				dumpPortAllocatorDebugInfo(f, rbg)
			})

			// Step 1: Create RBG without port-allocator annotation
			ginkgo.By("Creating RBG without port-allocator annotation")
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Wait for RBG to be ready
			f.ExpectRbgV2Equal(rbg)

			// Step 2: Verify no port ConfigMaps exist
			ginkgo.By("Verifying no port ConfigMaps exist")
			gomega.Consistently(func() bool {
				cmList := &corev1.ConfigMapList{}
				err := f.Client.List(f.Ctx, cmList,
					client.InNamespace(f.Namespace),
					client.MatchingLabels{
						portallocator.PortConfigMapLabelKey: portallocator.PortConfigMapLabelValue,
					},
				)
				if err != nil {
					return false
				}
				return len(cmList.Items) == 0
			}, 5*time.Second, utils.Interval).Should(gomega.BeTrue())

			// Step 3: Update RBG to add port-allocator annotation
			ginkgo.By("Updating RBG with port-allocator annotation")
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].Pattern.CustomComponentsPattern = &workloadsv1alpha2.CustomComponentsPattern{
					Components: []workloadsv1alpha2.InstanceComponent{
						buildLeaderComponentWithPortAllocator(true),
						buildWorkerComponentWithPortAllocator(true),
					},
				}
			})

			// Step 4: Wait for RBG to be ready after update
			ginkgo.By("Waiting for RBG to be ready after update")
			f.ExpectRbgV2Equal(rbg)

			// Step 5: Verify ConfigMaps are created
			ginkgo.By("Verifying port ConfigMaps are created")

			// Get the actual instance name from pods
			instanceCMName := verifyInstancePortConfigMapExists(f)
			instanceSetCMName := verifyInstanceSetPortConfigMapExists(f)

			// Step 6: Verify pods have environment variables injected
			ginkgo.By("Verifying pods have environment variables injected")
			verifyPodsHavePortEnvVars(f, rbg)

			// Step 7: Verify ConfigMap data contains expected ports
			ginkgo.By("Verifying ConfigMap data contains expected ports")
			verifyConfigMapData(f, instanceCMName, instanceSetCMName)
		})

		ginkgo.It("should remove RoleScoped port when annotation is updated to remove RoleScoped allocation", func() {
			// Step 1: Create RBG with port-allocator annotation (including RoleScoped)
			ginkgo.By("Creating RBG with port-allocator annotation")
			rbg := buildPortAllocatorTestRBG(f.Namespace, true)

			f.RegisterDebugFn(func() {
				dumpDebugInfo(f, rbg)
				dumpPortAllocatorDebugInfo(f, rbg)
			})

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Wait for RBG to be ready
			f.ExpectRbgV2Equal(rbg)

			// Step 2: Verify ConfigMaps are created and contain expected ports
			ginkgo.By("Verifying ConfigMaps are created with RoleScoped port")
			_ = verifyInstancePortConfigMapExists(f)
			instanceSetCMName := verifyInstanceSetPortConfigMapExists(f)

			// Step 3: Verify leader pod has both PodScoped and RoleScoped env vars
			ginkgo.By("Verifying leader pod has both PodScoped and RoleScoped env vars")
			verifyLeaderPodHasEnvVars(f, rbg, []string{"LEADER_PORT", "LEADER_PORT_STATIC"})

			// Step 4: Verify InstanceSet ConfigMap has RoleScoped port
			ginkgo.By("Verifying InstanceSet ConfigMap has RoleScoped port")
			verifyInstanceSetConfigMapHasKey(f, instanceSetCMName, "leader.leader-port-static")

			// Step 5: Update RBG to remove RoleScoped allocation from leader
			ginkgo.By("Updating RBG to remove RoleScoped allocation from leader")
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].Pattern.CustomComponentsPattern = &workloadsv1alpha2.CustomComponentsPattern{
					Components: []workloadsv1alpha2.InstanceComponent{
						buildLeaderComponentWithOnlyPodScoped(),
						buildWorkerComponentWithPortAllocator(true),
					},
				}
			})

			// Step 6: Wait for RBG to be ready after update
			ginkgo.By("Waiting for RBG to be ready after update")
			f.ExpectRbgV2Equal(rbg)

			// Step 7: Verify InstanceSet ConfigMap no longer has RoleScoped port
			ginkgo.By("Verifying InstanceSet ConfigMap no longer has RoleScoped port")
			verifyInstanceSetConfigMapDoesNotHaveKey(f, instanceSetCMName, "leader.leader-port-static")

			// Step 8: Verify leader pod no longer has RoleScoped env var but still has PodScoped
			ginkgo.By("Verifying leader pod has only PodScoped env var")
			verifyLeaderPodHasEnvVars(f, rbg, []string{"LEADER_PORT"})
			verifyLeaderPodDoesNotHaveEnvVars(f, rbg, []string{"LEADER_PORT_STATIC"})
		})
	})
}

// buildPortAllocatorTestRBG builds a RoleBasedGroup for port allocator testing.
// If withAnnotation is true, the port-allocator annotation is included.
func buildPortAllocatorTestRBG(namespace string, withAnnotation bool) *workloadsv1alpha2.RoleBasedGroup {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("port-allocator-test", namespace).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			buildPortAllocatorRole(withAnnotation),
		}).Obj()
	return rbg
}

func buildPortAllocatorRole(withAnnotation bool) workloadsv1alpha2.RoleSpec {
	role := workloadsv1alpha2.RoleSpec{
		Name: "prefill",
		Workload: workloadsv1alpha2.WorkloadSpec{
			APIVersion: "workloads.x-k8s.io/v1alpha2",
			Kind:       "RoleInstanceSet",
		},
		Pattern: workloadsv1alpha2.Pattern{
			CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
				Components: []workloadsv1alpha2.InstanceComponent{
					buildLeaderComponentWithPortAllocator(withAnnotation),
					buildWorkerComponentWithPortAllocator(withAnnotation),
				},
			},
		},
	}
	return role
}

func buildLeaderComponentWithPortAllocator(withAnnotation bool) workloadsv1alpha2.InstanceComponent {
	template := wrappersv2.BuildBasicPodTemplateSpec()
	template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	if withAnnotation {
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
					"annotationKey": "test/grpc-port2",
					"scope": "RoleScoped"
				}
			]
		}`,
		}
	}

	return workloadsv1alpha2.InstanceComponent{
		Name:     "leader",
		Size:     ptr.To(int32(1)),
		Template: template,
	}
}

func buildWorkerComponentWithPortAllocator(withAnnotation bool) workloadsv1alpha2.InstanceComponent {
	template := wrappersv2.BuildBasicPodTemplateSpec()
	template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	if withAnnotation {
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
	}

	return workloadsv1alpha2.InstanceComponent{
		Name:     "worker",
		Size:     ptr.To(int32(1)),
		Template: template,
	}
}

func verifyInstancePortConfigMapExists(f *framework.Framework) string {
	logger := log.FromContext(f.Ctx)

	var cmName string
	gomega.Eventually(func() bool {
		cmList := &corev1.ConfigMapList{}
		err := f.Client.List(f.Ctx, cmList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				portallocator.PortConfigMapLabelKey: portallocator.PortConfigMapLabelValue,
			},
		)
		if err != nil {
			logger.Error(err, "Failed to list ConfigMaps")
			return false
		}

		// Find instance-level ConfigMap (starts with "instance-")
		for _, cm := range cmList.Items {
			if len(cm.Name) > 9 && cm.Name[:9] == "instance-" {
				cmName = cm.Name
				logger.V(1).Info("Found instance port ConfigMap", "name", cmName)
				return true
			}
		}

		logger.V(1).Info("Waiting for instance port ConfigMap to be created")
		return false
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())

	return cmName
}

func verifyInstanceSetPortConfigMapExists(f *framework.Framework) string {
	logger := log.FromContext(f.Ctx)

	var cmName string
	gomega.Eventually(func() bool {
		cmList := &corev1.ConfigMapList{}
		err := f.Client.List(f.Ctx, cmList,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{
				portallocator.PortConfigMapLabelKey: portallocator.PortConfigMapLabelValue,
			},
		)
		if err != nil {
			logger.Error(err, "Failed to list ConfigMaps")
			return false
		}

		for _, cm := range cmList.Items {
			if len(cm.Name) > 12 && cm.Name[:12] == "instanceset-" {
				cmName = cm.Name
				logger.V(1).Info("Found instanceset port ConfigMap", "name", cmName)
				return true
			}
		}

		logger.V(1).Info("Waiting for instanceset port ConfigMap to be created")
		return false
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())

	return cmName
}

func verifyPodsHavePortEnvVars(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
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

		foundPortEnvVar := false
		for _, pod := range podList.Items {
			// Check if pod has port-related annotations (for leader pods)
			if pod.Annotations["test/grpc-port"] != "" || pod.Annotations["test/grpc-port2"] != "" {
				logger.V(1).Info("Pod has port annotation", "pod", pod.Name, "annotations", pod.Annotations)
			}

			// Check containers for environment variables
			for _, container := range pod.Spec.Containers {
				for _, envVar := range container.Env {
					if envVar.ValueFrom != nil && envVar.ValueFrom.ConfigMapKeyRef != nil {
						logger.V(1).Info("Container has ConfigMap env var", "pod", pod.Name, "container", container.Name, "env", envVar.Name, "cm", envVar.ValueFrom.ConfigMapKeyRef.Name)
						foundPortEnvVar = true
					}
				}
			}
		}

		if !foundPortEnvVar {
			logger.V(1).Info("Waiting for pods to have port environment variables")
		}
		return foundPortEnvVar
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

func verifyConfigMapData(f *framework.Framework, instanceCMName, instanceSetCMName string) {
	logger := log.FromContext(f.Ctx)

	if instanceCMName != "" {
		gomega.Eventually(func() bool {
			cm := &corev1.ConfigMap{}
			err := f.Client.Get(f.Ctx, client.ObjectKey{Name: instanceCMName, Namespace: f.Namespace}, cm)
			if err != nil {
				logger.Error(err, "Failed to get instance ConfigMap")
				return false
			}

			if len(cm.Data) == 0 {
				logger.V(1).Info("Waiting for instance ConfigMap to have data")
				return false
			}

			logger.V(1).Info("Instance ConfigMap data", "data", cm.Data)
			return true
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
	}

	if instanceSetCMName != "" {
		gomega.Eventually(func() bool {
			cm := &corev1.ConfigMap{}
			err := f.Client.Get(f.Ctx, client.ObjectKey{Name: instanceSetCMName, Namespace: f.Namespace}, cm)
			if err != nil {
				logger.Error(err, "Failed to get instanceset ConfigMap")
				return false
			}

			if len(cm.Data) == 0 {
				logger.V(1).Info("Waiting for instanceset ConfigMap to have data")
				return false
			}

			logger.V(1).Info("InstanceSet ConfigMap data", "data", cm.Data)
			return true
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
	}
}

func dumpPortAllocatorDebugInfo(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	if rbg == nil {
		return
	}
	if !ginkgo.CurrentSpecReport().Failed() {
		return
	}

	fmt.Println("\n========== Port Allocator Debug Info ==========")

	// Dump ConfigMaps
	cmList := &corev1.ConfigMapList{}
	if err := f.Client.List(f.Ctx, cmList,
		client.InNamespace(f.Namespace),
		client.MatchingLabels{
			portallocator.PortConfigMapLabelKey: "rbgs-controller-manager",
		},
	); err != nil {
		fmt.Printf("[CM] Failed to list ConfigMaps: %v\n", err)
	} else {
		fmt.Printf("[CM] Found %d port ConfigMaps\n", len(cmList.Items))
		for _, cm := range cmList.Items {
			fmt.Printf("  - Name: %s, Data keys: %v\n", cm.Name, getMapKeys(cm.Data))
		}
	}

	// Dump Pods with port info
	podList := &corev1.PodList{}
	if err := f.Client.List(f.Ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			"rbg.workloads.x-k8s.io/group-name": rbg.Name,
		},
	); err != nil {
		fmt.Printf("[POD] Failed to list pods: %v\n", err)
	} else {
		fmt.Printf("[POD] Found %d pods\n", len(podList.Items))
		for _, pod := range podList.Items {
			fmt.Printf("  - Name: %s\n", pod.Name)
			fmt.Printf("    Annotations: %v\n", pod.Annotations)
			for _, container := range pod.Spec.Containers {
				for _, env := range container.Env {
					if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil {
						fmt.Printf("    Env: %s -> ConfigMap/%s/%s\n", env.Name, env.ValueFrom.ConfigMapKeyRef.Name, env.ValueFrom.ConfigMapKeyRef.Key)
					}
				}
			}
		}
	}

	fmt.Println("========== End Port Allocator Debug Info ==========")
}

func getMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// buildLeaderComponentWithOnlyPodScoped builds a leader component with only PodScoped allocation
func buildLeaderComponentWithOnlyPodScoped() workloadsv1alpha2.InstanceComponent {
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

// verifyLeaderPodHasEnvVars verifies that leader pod has the specified environment variables
func verifyLeaderPodHasEnvVars(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, expectedEnvVars []string) {
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

		// Find leader pod
		for _, pod := range podList.Items {
			if containsLeader(pod.Name) {
				// Check if pod has all expected env vars
				foundEnvVars := make(map[string]bool)
				for _, container := range pod.Spec.Containers {
					for _, envVar := range container.Env {
						for _, expected := range expectedEnvVars {
							if envVar.Name == expected {
								foundEnvVars[expected] = true
								logger.V(1).Info("Found env var in leader pod", "pod", pod.Name, "env", envVar.Name)
							}
						}
					}
				}

				// Check if all expected env vars are found
				allFound := true
				for _, expected := range expectedEnvVars {
					if !foundEnvVars[expected] {
						logger.V(1).Info("Missing expected env var in leader pod", "pod", pod.Name, "env", expected)
						allFound = false
					}
				}
				return allFound
			}
		}

		logger.V(1).Info("Leader pod not found")
		return false
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

// verifyLeaderPodDoesNotHaveEnvVars verifies that leader pod does NOT have the specified environment variables
func verifyLeaderPodDoesNotHaveEnvVars(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, unexpectedEnvVars []string) {
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

		// Find leader pod
		for _, pod := range podList.Items {
			if containsLeader(pod.Name) {
				// Check if pod has any unexpected env vars
				for _, container := range pod.Spec.Containers {
					for _, envVar := range container.Env {
						for _, unexpected := range unexpectedEnvVars {
							if envVar.Name == unexpected {
								logger.V(1).Info("Found unexpected env var in leader pod", "pod", pod.Name, "env", envVar.Name)
								return false
							}
						}
					}
				}
				logger.V(1).Info("Leader pod does not have unexpected env vars", "pod", pod.Name)
				return true
			}
		}

		logger.V(1).Info("Leader pod not found")
		return false
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

// verifyInstanceSetConfigMapHasKey verifies that InstanceSet ConfigMap has the specified key
func verifyInstanceSetConfigMapHasKey(f *framework.Framework, cmName, expectedKey string) {
	logger := log.FromContext(f.Ctx)

	gomega.Eventually(func() bool {
		cm := &corev1.ConfigMap{}
		err := f.Client.Get(f.Ctx, client.ObjectKey{Name: cmName, Namespace: f.Namespace}, cm)
		if err != nil {
			logger.Error(err, "Failed to get instanceset ConfigMap", "name", cmName)
			return false
		}

		for key := range cm.Data {
			if key == expectedKey {
				logger.V(1).Info("Found expected key in InstanceSet ConfigMap", "key", expectedKey, "value", cm.Data[key])
				return true
			}
		}

		logger.V(1).Info("Key not found in InstanceSet ConfigMap", "key", expectedKey, "data", cm.Data)
		return false
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

// verifyInstanceSetConfigMapDoesNotHaveKey verifies that InstanceSet ConfigMap does NOT have the specified key
func verifyInstanceSetConfigMapDoesNotHaveKey(f *framework.Framework, cmName, unexpectedKey string) {
	logger := log.FromContext(f.Ctx)

	gomega.Eventually(func() bool {
		cm := &corev1.ConfigMap{}
		err := f.Client.Get(f.Ctx, client.ObjectKey{Name: cmName, Namespace: f.Namespace}, cm)
		if err != nil {
			logger.Error(err, "Failed to get instanceset ConfigMap", "name", cmName)
			return false
		}

		for key := range cm.Data {
			if key == unexpectedKey {
				logger.V(1).Info("Unexpected key still found in InstanceSet ConfigMap", "key", unexpectedKey, "value", cm.Data[key])
				return false
			}
		}

		logger.V(1).Info("Key removed from InstanceSet ConfigMap as expected", "key", unexpectedKey)
		return true
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

// containsLeader checks if pod name contains "leader"
func containsLeader(podName string) bool {
	return len(podName) >= 6 && (podName[len(podName)-6:] == "leader" ||
		(len(podName) > 7 && podName[len(podName)-7:len(podName)-2] == "leader") ||
		containsSubstring(podName, "-leader-"))
}

// containsSubstring checks if s contains substr
func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

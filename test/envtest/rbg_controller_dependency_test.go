/*
Copyright 2025.

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

package envtest

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

var _ = Describe("RoleBasedGroup Controller - Dependency Management", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("When creating RBG with role dependencies", func() {
		var (
			testNs string
		)

		BeforeEach(func() {
			testNs = fmt.Sprintf("test-rbg-dep-%d", time.Now().UnixNano())
			createNamespace(testNs)
		})

		AfterEach(func() {
			deleteNamespace(testNs)
		})

		It("Should respect role dependencies - role B depends on role A", func() {
			rbgName := "test-rbg-simple-dep"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "backend",
							Replicas: ptr.To(int32(2)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "redis",
											Image: "redis:7.0",
										},
									},
								},
							},
						},
						{
							Name:         "frontend",
							Replicas:     ptr.To(int32(3)),
							Dependencies: []string{"backend"}, // frontend depends on backend
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "nginx",
											Image: "nginx:latest",
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// Verify backend StatefulSet is created first
			backendLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "backend"),
				Namespace: testNs,
			}
			backendSTS := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, backendLookupKey, backendSTS)
			}, timeout, interval).Should(Succeed())

			// Update backend status to ready
			Eventually(func() error {
				if err := k8sClient.Get(ctx, backendLookupKey, backendSTS); err != nil {
					return err
				}
				backendSTS.Status.ObservedGeneration = int64(backendSTS.Generation)
				backendSTS.Status.Replicas = 2
				backendSTS.Status.ReadyReplicas = 2
				return k8sClient.Status().Update(ctx, backendSTS)
			}, timeout, interval).Should(Succeed())

			// Now frontend Deployment should be created (after backend is ready)
			frontendLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "frontend"),
				Namespace: testNs,
			}
			frontendDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, frontendLookupKey, frontendDeploy)
			}, timeout, interval).Should(Succeed())

			// Update frontend status
			Eventually(func() error {
				if err := k8sClient.Get(ctx, frontendLookupKey, frontendDeploy); err != nil {
					return err
				}
				frontendDeploy.Status.ObservedGeneration = frontendDeploy.Generation
				frontendDeploy.Status.Replicas = 3
				frontendDeploy.Status.ReadyReplicas = 3
				return k8sClient.Status().Update(ctx, frontendDeploy)
			}, timeout, interval).Should(Succeed())

			// Verify RBG status
			rbgLookupKey := types.NamespacedName{Name: rbgName, Namespace: testNs}
			createdRBG := &workloadsv1alpha1.RoleBasedGroup{}
			Eventually(func() int {
				err := k8sClient.Get(ctx, rbgLookupKey, createdRBG)
				if err != nil {
					return 0
				}
				return len(createdRBG.Status.RoleStatuses)
			}, timeout, interval).Should(Equal(2))

			// Verify both roles are in status
			roleStatusMap := make(map[string]workloadsv1alpha1.RoleStatus)
			for _, rs := range createdRBG.Status.RoleStatuses {
				roleStatusMap[rs.Name] = rs
			}
			Expect(roleStatusMap).To(HaveKey("backend"))
			Expect(roleStatusMap).To(HaveKey("frontend"))
		})

		It("Should handle multi-level dependencies - A -> B -> C", func() {
			rbgName := "test-rbg-multi-level-dep"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "database",
							Replicas: ptr.To(int32(1)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "postgres",
											Image: "postgres:14",
										},
									},
								},
							},
						},
						{
							Name:         "backend",
							Replicas:     ptr.To(int32(2)),
							Dependencies: []string{"database"}, // backend depends on database
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "api",
											Image: "api:latest",
										},
									},
								},
							},
						},
						{
							Name:         "frontend",
							Replicas:     ptr.To(int32(3)),
							Dependencies: []string{"backend"}, // frontend depends on backend
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "nginx",
											Image: "nginx:latest",
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// Verify database is created first
			databaseLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "database"),
				Namespace: testNs,
			}
			databaseSTS := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, databaseLookupKey, databaseSTS)
			}, timeout, interval).Should(Succeed())

			// Make database ready
			Eventually(func() error {
				if err := k8sClient.Get(ctx, databaseLookupKey, databaseSTS); err != nil {
					return err
				}
				databaseSTS.Status.ObservedGeneration = int64(databaseSTS.Generation)
				databaseSTS.Status.Replicas = 1
				databaseSTS.Status.ReadyReplicas = 1
				return k8sClient.Status().Update(ctx, databaseSTS)
			}, timeout, interval).Should(Succeed())

			// Verify backend is created after database
			backendLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "backend"),
				Namespace: testNs,
			}
			backendDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, backendLookupKey, backendDeploy)
			}, timeout, interval).Should(Succeed())

			// Make backend ready
			Eventually(func() error {
				if err := k8sClient.Get(ctx, backendLookupKey, backendDeploy); err != nil {
					return err
				}
				backendDeploy.Status.ObservedGeneration = backendDeploy.Generation
				backendDeploy.Status.Replicas = 2
				backendDeploy.Status.ReadyReplicas = 2
				return k8sClient.Status().Update(ctx, backendDeploy)
			}, timeout, interval).Should(Succeed())

			// Verify frontend is created last
			frontendLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "frontend"),
				Namespace: testNs,
			}
			frontendDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, frontendLookupKey, frontendDeploy)
			}, timeout, interval).Should(Succeed())

			// Verify all three roles exist
			rbgLookupKey := types.NamespacedName{Name: rbgName, Namespace: testNs}
			createdRBG := &workloadsv1alpha1.RoleBasedGroup{}
			Eventually(func() int {
				err := k8sClient.Get(ctx, rbgLookupKey, createdRBG)
				if err != nil {
					return 0
				}
				return len(createdRBG.Status.RoleStatuses)
			}, timeout, interval).Should(Equal(3))
		})

		It("Should reject cyclic dependencies", func() {
			rbgName := "test-rbg-cyclic-dep"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:         "role-a",
							Replicas:     ptr.To(int32(1)),
							Dependencies: []string{"role-b"}, // A depends on B
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "container-a",
											Image: "nginx:latest",
										},
									},
								},
							},
						},
						{
							Name:         "role-b",
							Replicas:     ptr.To(int32(1)),
							Dependencies: []string{"role-a"}, // B depends on A - creates cycle!
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "container-b",
											Image: "nginx:latest",
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// The controller should detect the cycle and emit an event
			// Check for warning event about cycle detection
			rbgLookupKey := types.NamespacedName{Name: rbgName, Namespace: testNs}
			createdRBG := &workloadsv1alpha1.RoleBasedGroup{}

			// RBG should exist but reconciliation should fail
			Eventually(func() error {
				return k8sClient.Get(ctx, rbgLookupKey, createdRBG)
			}, timeout, interval).Should(Succeed())

			// Verify that workloads are NOT created due to cycle
			roleALookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "role-a"),
				Namespace: testNs,
			}
			deployA := &appsv1.Deployment{}

			// Should not be able to create deployment due to cycle
			Consistently(func() error {
				return k8sClient.Get(ctx, roleALookupKey, deployA)
			}, time.Second*2, interval).ShouldNot(Succeed())
		})

		It("Should reject non-existent dependencies", func() {
			rbgName := "test-rbg-invalid-dep"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:         "frontend",
							Replicas:     ptr.To(int32(2)),
							Dependencies: []string{"non-existent-role"}, // Invalid dependency
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "nginx",
											Image: "nginx:latest",
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// The controller should reject this configuration
			rbgLookupKey := types.NamespacedName{Name: rbgName, Namespace: testNs}
			createdRBG := &workloadsv1alpha1.RoleBasedGroup{}
			Eventually(func() error {
				return k8sClient.Get(ctx, rbgLookupKey, createdRBG)
			}, timeout, interval).Should(Succeed())

			// Verify that workload is NOT created
			frontendLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "frontend"),
				Namespace: testNs,
			}
			frontendDeploy := &appsv1.Deployment{}

			Consistently(func() error {
				return k8sClient.Get(ctx, frontendLookupKey, frontendDeploy)
			}, time.Second*2, interval).ShouldNot(Succeed())
		})

		It("Should handle parallel roles without dependencies", func() {
			rbgName := "test-rbg-parallel"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker-1",
							Replicas: ptr.To(int32(2)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "worker",
											Image: "worker:v1",
										},
									},
								},
							},
						},
						{
							Name:     "worker-2",
							Replicas: ptr.To(int32(2)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "worker",
											Image: "worker:v2",
										},
									},
								},
							},
						},
						{
							Name:     "worker-3",
							Replicas: ptr.To(int32(2)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "worker",
											Image: "worker:v3",
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// All three deployments should be created in parallel (same order level)
			worker1Key := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "worker-1"),
				Namespace: testNs,
			}
			worker2Key := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "worker-2"),
				Namespace: testNs,
			}
			worker3Key := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "worker-3"),
				Namespace: testNs,
			}

			deploy1 := &appsv1.Deployment{}
			deploy2 := &appsv1.Deployment{}
			deploy3 := &appsv1.Deployment{}

			// All should be created
			Eventually(func() error {
				return k8sClient.Get(ctx, worker1Key, deploy1)
			}, timeout, interval).Should(Succeed())

			// Update deploy1 status
			Eventually(func() error {
				if err := k8sClient.Get(ctx, worker1Key, deploy1); err != nil {
					return err
				}
				deploy1.Status.ObservedGeneration = deploy1.Generation
				deploy1.Status.Replicas = 2
				deploy1.Status.ReadyReplicas = 2
				return k8sClient.Status().Update(ctx, deploy1)
			}, timeout, interval).Should(Succeed())

			Eventually(func() error {
				return k8sClient.Get(ctx, worker2Key, deploy2)
			}, timeout, interval).Should(Succeed())

			// Update deploy2 status
			Eventually(func() error {
				if err := k8sClient.Get(ctx, worker2Key, deploy2); err != nil {
					return err
				}
				deploy2.Status.ObservedGeneration = deploy2.Generation
				deploy2.Status.Replicas = 2
				deploy2.Status.ReadyReplicas = 2
				return k8sClient.Status().Update(ctx, deploy2)
			}, timeout, interval).Should(Succeed())

			Eventually(func() error {
				return k8sClient.Get(ctx, worker3Key, deploy3)
			}, timeout, interval).Should(Succeed())

			// Update deploy3 status
			Eventually(func() error {
				if err := k8sClient.Get(ctx, worker3Key, deploy3); err != nil {
					return err
				}
				deploy3.Status.ObservedGeneration = deploy3.Generation
				deploy3.Status.Replicas = 2
				deploy3.Status.ReadyReplicas = 2
				return k8sClient.Status().Update(ctx, deploy3)
			}, timeout, interval).Should(Succeed())

			// Verify RBG has all three roles in status
			rbgLookupKey := types.NamespacedName{Name: rbgName, Namespace: testNs}
			createdRBG := &workloadsv1alpha1.RoleBasedGroup{}
			Eventually(func() int {
				err := k8sClient.Get(ctx, rbgLookupKey, createdRBG)
				if err != nil {
					return 0
				}
				return len(createdRBG.Status.RoleStatuses)
			}, timeout, interval).Should(Equal(3))
		})
	})
})

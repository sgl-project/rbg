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
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

var _ = Describe("RoleBasedGroup Controller - Basic Functionality", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("When creating RBG with single role", func() {
		var (
			testNs string
		)

		BeforeEach(func() {
			// Generate unique namespace name with timestamp to avoid conflicts
			testNs = fmt.Sprintf("test-rbg-single-%d", time.Now().UnixNano())
			createNamespace(testNs)
		})

		AfterEach(func() {
			deleteNamespace(testNs)
		})

		It("Should create RBG with single Deployment role successfully", func() {
			rbgName := "test-rbg-single-deployment"

			// Create RBG with single Deployment role
			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(2)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								ObjectMeta: metav1.ObjectMeta{
									Labels: map[string]string{
										"app": "test-worker",
									},
								},
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "nginx",
											Image: "nginx:1.14.2",
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// Verify RBG is created
			rbgLookupKey := types.NamespacedName{Name: rbgName, Namespace: testNs}
			createdRBG := &workloadsv1alpha1.RoleBasedGroup{}
			Eventually(func() error {
				return k8sClient.Get(ctx, rbgLookupKey, createdRBG)
			}, timeout, interval).Should(Succeed())

			// Verify Deployment is created
			deployLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "worker"),
				Namespace: testNs,
			}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, deployLookupKey, createdDeploy)
			}, timeout, interval).Should(Succeed())

			// Verify Deployment spec
			Expect(*createdDeploy.Spec.Replicas).To(Equal(int32(2)))
			Expect(createdDeploy.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(createdDeploy.Spec.Template.Spec.Containers[0].Name).To(Equal("nginx"))

			// In envtest, manually set Deployment status to simulate reconciliation
			Eventually(func() error {
				if err := k8sClient.Get(ctx, deployLookupKey, createdDeploy); err != nil {
					return err
				}
				createdDeploy.Status.ObservedGeneration = createdDeploy.Generation
				createdDeploy.Status.Replicas = 2
				createdDeploy.Status.ReadyReplicas = 2
				return k8sClient.Status().Update(ctx, createdDeploy)
			}, timeout, interval).Should(Succeed())

			// Verify ControllerRevision is created
			Eventually(func() bool {
				revisionList := &appsv1.ControllerRevisionList{}
				err := k8sClient.List(ctx, revisionList,
					&client.ListOptions{Namespace: testNs})
				if err != nil {
					return false
				}
				return len(revisionList.Items) > 0
			}, timeout, interval).Should(BeTrue())

			// Update Deployment status to simulate pods running
			createdDeploy.Status.Replicas = 2
			createdDeploy.Status.ReadyReplicas = 2
			Expect(k8sClient.Status().Update(ctx, createdDeploy)).Should(Succeed())

			// Verify RBG status is updated
			Eventually(func() bool {
				err := k8sClient.Get(ctx, rbgLookupKey, createdRBG)
				if err != nil {
					return false
				}
				if len(createdRBG.Status.RoleStatuses) == 0 {
					return false
				}
				roleStatus := createdRBG.Status.RoleStatuses[0]
				return roleStatus.Name == "worker" &&
					roleStatus.Replicas == 2 &&
					roleStatus.ReadyReplicas == 2
			}, timeout, interval).Should(BeTrue())

			// Verify RBG Ready condition
			Eventually(func() bool {
				err := k8sClient.Get(ctx, rbgLookupKey, createdRBG)
				if err != nil {
					return false
				}
				for _, cond := range createdRBG.Status.Conditions {
					if cond.Type == string(workloadsv1alpha1.RoleBasedGroupReady) {
						return cond.Status == metav1.ConditionTrue
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})

		It("Should create RBG with single StatefulSet role successfully", func() {
			rbgName := "test-rbg-single-sts"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "master",
							Replicas: ptr.To(int32(3)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
							},
							Template: &corev1.PodTemplateSpec{
								ObjectMeta: metav1.ObjectMeta{
									Labels: map[string]string{
										"app": "test-master",
									},
								},
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
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// Verify StatefulSet is created
			stsLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "master"),
				Namespace: testNs,
			}
			createdSTS := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, stsLookupKey, createdSTS)
			}, timeout, interval).Should(Succeed())

			Expect(*createdSTS.Spec.Replicas).To(Equal(int32(3)))
		})

		It("Should create RBG with InstanceSet role successfully", func() {
			rbgName := "test-rbg-instanceset"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(2)),
							// Required label for InstanceSet workload
							Labels: map[string]string{
								workloadsv1alpha1.RBGInstancePatternLabelKey: string(workloadsv1alpha1.DeploymentInstancePattern),
							},
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "workloads.x-k8s.io/v1alpha1",
								Kind:       "InstanceSet",
							},
							Components: []workloadsv1alpha1.InstanceComponent{
								{
									Name: "main",
									Size: ptr.To(int32(1)),
									Template: corev1.PodTemplateSpec{
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
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// Verify InstanceSet is created
			instanceSetLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "worker"),
				Namespace: testNs,
			}
			createdInstanceSet := &workloadsv1alpha1.InstanceSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, instanceSetLookupKey, createdInstanceSet)
			}, timeout, interval).Should(Succeed())

			Expect(*createdInstanceSet.Spec.Replicas).To(Equal(int32(2)))
		})
	})

	Context("When creating RBG with multiple roles", func() {
		var (
			testNs string
		)

		BeforeEach(func() {
			// Generate unique namespace name with timestamp to avoid conflicts
			testNs = fmt.Sprintf("test-rbg-multi-%d", time.Now().UnixNano())
			createNamespace(testNs)
		})

		AfterEach(func() {
			deleteNamespace(testNs)
		})

		It("Should create RBG with multiple roles (Deployment + StatefulSet)", func() {
			rbgName := "test-rbg-multi"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "frontend",
							Replicas: ptr.To(int32(2)),
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
						{
							Name:     "backend",
							Replicas: ptr.To(int32(3)),
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
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// Verify Deployment is created
			deployLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "frontend"),
				Namespace: testNs,
			}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, deployLookupKey, createdDeploy)
			}, timeout, interval).Should(Succeed())

			// Manually update Deployment status in envtest
			Eventually(func() error {
				if err := k8sClient.Get(ctx, deployLookupKey, createdDeploy); err != nil {
					return err
				}
				createdDeploy.Status.ObservedGeneration = createdDeploy.Generation
				createdDeploy.Status.Replicas = 2
				createdDeploy.Status.ReadyReplicas = 2
				return k8sClient.Status().Update(ctx, createdDeploy)
			}, timeout, interval).Should(Succeed())

			stsLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "backend"),
				Namespace: testNs,
			}
			createdSTS := &appsv1.StatefulSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, stsLookupKey, createdSTS)
			}, timeout, interval).Should(Succeed())

			// Manually update StatefulSet status in envtest
			Eventually(func() error {
				if err := k8sClient.Get(ctx, stsLookupKey, createdSTS); err != nil {
					return err
				}
				createdSTS.Status.ObservedGeneration = int64(createdSTS.Generation)
				createdSTS.Status.Replicas = 3
				createdSTS.Status.ReadyReplicas = 3
				return k8sClient.Status().Update(ctx, createdSTS)
			}, timeout, interval).Should(Succeed())

			// Verify RBG status contains both role statuses
			rbgLookupKey := types.NamespacedName{Name: rbgName, Namespace: testNs}
			createdRBG := &workloadsv1alpha1.RoleBasedGroup{}
			Eventually(func() int {
				err := k8sClient.Get(ctx, rbgLookupKey, createdRBG)
				if err != nil {
					return 0
				}
				return len(createdRBG.Status.RoleStatuses)
			}, timeout, interval).Should(Equal(2))

			// Verify role statuses are correct
			roleStatusMap := make(map[string]workloadsv1alpha1.RoleStatus)
			for _, rs := range createdRBG.Status.RoleStatuses {
				roleStatusMap[rs.Name] = rs
			}
			Expect(roleStatusMap).To(HaveKey("frontend"))
			Expect(roleStatusMap).To(HaveKey("backend"))
		})

		It("Should handle role status updates correctly", func() {
			rbgName := "test-rbg-status"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(3)),
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

			deployLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "worker"),
				Namespace: testNs,
			}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, deployLookupKey, createdDeploy)
			}, timeout, interval).Should(Succeed())

			// In envtest, Deployment status won't update automatically
			// We need to manually update status to simulate pods running
			// First, update the Deployment status to set Generation and ObservedGeneration
			Eventually(func() error {
				if err := k8sClient.Get(ctx, deployLookupKey, createdDeploy); err != nil {
					return err
				}
				createdDeploy.Status.ObservedGeneration = createdDeploy.Generation
				createdDeploy.Status.Replicas = 3
				createdDeploy.Status.ReadyReplicas = 2 // Only 2 out of 3 ready
				return k8sClient.Status().Update(ctx, createdDeploy)
			}, timeout, interval).Should(Succeed())

			// Verify RBG status reflects partial ready
			rbgLookupKey := types.NamespacedName{Name: rbgName, Namespace: testNs}
			createdRBG := &workloadsv1alpha1.RoleBasedGroup{}
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, rbgLookupKey, createdRBG)
				if err != nil || len(createdRBG.Status.RoleStatuses) == 0 {
					return 0
				}
				return createdRBG.Status.RoleStatuses[0].ReadyReplicas
			}, timeout, interval).Should(Equal(int32(2)))

			// Verify RBG is not ready yet
			Expect(createdRBG.Status.RoleStatuses[0].Replicas).To(Equal(int32(3)))
			Expect(createdRBG.Status.RoleStatuses[0].ReadyReplicas).To(Equal(int32(2)))

			// Simulate all ready
			Eventually(func() error {
				if err := k8sClient.Get(ctx, deployLookupKey, createdDeploy); err != nil {
					return err
				}
				createdDeploy.Status.ReadyReplicas = 3
				return k8sClient.Status().Update(ctx, createdDeploy)
			}, timeout, interval).Should(Succeed())

			// Verify RBG status is fully ready now
			Eventually(func() bool {
				err := k8sClient.Get(ctx, rbgLookupKey, createdRBG)
				if err != nil || len(createdRBG.Status.RoleStatuses) == 0 {
					return false
				}
				return createdRBG.Status.RoleStatuses[0].ReadyReplicas == 3
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When handling ControllerRevision", func() {
		var (
			testNs string
		)

		BeforeEach(func() {
			// Generate unique namespace name with timestamp to avoid conflicts
			testNs = fmt.Sprintf("test-rbg-rev-%d", time.Now().UnixNano())
			createNamespace(testNs)
		})

		AfterEach(func() {
			deleteNamespace(testNs)
		})

		It("Should create ControllerRevision when RBG is created", func() {
			rbgName := "test-rbg-with-revision"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(1)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "nginx",
											Image: "nginx:1.14.2",
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// Verify ControllerRevision is created
			Eventually(func() int {
				revisionList := &appsv1.ControllerRevisionList{}
				err := k8sClient.List(ctx, revisionList,
					&client.ListOptions{Namespace: testNs})
				if err != nil {
					return 0
				}
				return len(revisionList.Items)
			}, timeout, interval).Should(BeNumerically(">", 0))
		})

		It("Should create new revision when RBG spec changes", func() {
			rbgName := "test-rbg-revision-update"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(1)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "nginx",
											Image: "nginx:1.14.2",
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// Wait for initial revision
			Eventually(func() int {
				revisionList := &appsv1.ControllerRevisionList{}
				err := k8sClient.List(ctx, revisionList,
					&client.ListOptions{Namespace: testNs})
				if err != nil {
					return 0
				}
				return len(revisionList.Items)
			}, timeout, interval).Should(Equal(1))

			initialRevisionCount := 1

			// Update RBG spec (change image)
			rbgLookupKey := types.NamespacedName{Name: rbgName, Namespace: testNs}
			updatedRBG := &workloadsv1alpha1.RoleBasedGroup{}
			Eventually(func() error {
				if err := k8sClient.Get(ctx, rbgLookupKey, updatedRBG); err != nil {
					return err
				}
				updatedRBG.Spec.Roles[0].Template.Spec.Containers[0].Image = "nginx:1.19.0"
				return k8sClient.Update(ctx, updatedRBG)
			}, timeout, interval).Should(Succeed())

			// Verify new revision is created
			Eventually(func() int {
				revisionList := &appsv1.ControllerRevisionList{}
				err := k8sClient.List(ctx, revisionList,
					&client.ListOptions{Namespace: testNs})
				if err != nil {
					return initialRevisionCount
				}
				return len(revisionList.Items)
			}, timeout, interval).Should(BeNumerically(">", initialRevisionCount))
		})
	})
})

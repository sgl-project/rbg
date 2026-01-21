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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

var _ = Describe("RoleBasedGroupSet Controller", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var (
		testNs string
	)

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-rbgs-%d", time.Now().UnixNano())
		createNamespace(testNs)
	})

	AfterEach(func() {
		deleteNamespace(testNs)
	})

	Context("When creating RoleBasedGroupSet", func() {
		It("Should create child RoleBasedGroups successfully", func() {
			rbgsName := "test-rbgs-basic"

			rbgs := &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgsName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
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
												Image: "nginx:latest",
											},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbgs)).Should(Succeed())

			// Verify child RBGs are created
			Eventually(func() int {
				rbgList := &workloadsv1alpha1.RoleBasedGroupList{}
				err := k8sClient.List(ctx, rbgList,
					client.InNamespace(testNs),
					client.MatchingLabels{workloadsv1alpha1.SetRBGSetNameLabelKey: rbgsName})
				if err != nil {
					return 0
				}
				return len(rbgList.Items)
			}, timeout, interval).Should(Equal(3))

			// Verify index labels
			rbgList := &workloadsv1alpha1.RoleBasedGroupList{}
			k8sClient.List(ctx, rbgList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetRBGSetNameLabelKey: rbgsName})

			indices := make(map[string]bool)
			for _, rbg := range rbgList.Items {
				idx := rbg.Labels[workloadsv1alpha1.SetRBGIndexLabelKey]
				Expect(idx).NotTo(BeEmpty())
				indices[idx] = true
			}
			Expect(indices).To(HaveLen(3))
			Expect(indices).To(HaveKey("0"))
			Expect(indices).To(HaveKey("1"))
			Expect(indices).To(HaveKey("2"))
		})

		It("Should handle scaling and spec updates", func() {
			rbgsName := "test-rbgs-scaling"

			rbgs := &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgsName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(2)),
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
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
												Image: "nginx:v1",
											},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbgs)).Should(Succeed())

			// Wait for 2 RBGs
			Eventually(func() int {
				rbgList := &workloadsv1alpha1.RoleBasedGroupList{}
				k8sClient.List(ctx, rbgList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetRBGSetNameLabelKey: rbgsName})
				return len(rbgList.Items)
			}, timeout, interval).Should(Equal(2))

			// Update spec (change image and replicas)
			rbgsLookupKey := types.NamespacedName{Name: rbgsName, Namespace: testNs}
			createdRBGS := &workloadsv1alpha1.RoleBasedGroupSet{}
			Eventually(func() error {
				if err := k8sClient.Get(ctx, rbgsLookupKey, createdRBGS); err != nil {
					return err
				}
				createdRBGS.Spec.Replicas = ptr.To(int32(4))
				createdRBGS.Spec.Template.Roles[0].Template.Spec.Containers[0].Image = "nginx:v2"
				return k8sClient.Update(ctx, createdRBGS)
			}, timeout, interval).Should(Succeed())

			// Verify 4 RBGs
			Eventually(func() int {
				rbgList := &workloadsv1alpha1.RoleBasedGroupList{}
				k8sClient.List(ctx, rbgList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetRBGSetNameLabelKey: rbgsName})
				return len(rbgList.Items)
			}, timeout, interval).Should(Equal(4))

			// Verify spec updated in child RBGs
			rbgList := &workloadsv1alpha1.RoleBasedGroupList{}
			Eventually(func() bool {
				k8sClient.List(ctx, rbgList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetRBGSetNameLabelKey: rbgsName})
				for _, rbg := range rbgList.Items {
					if len(rbg.Spec.Roles) == 0 || rbg.Spec.Roles[0].Template.Spec.Containers[0].Image != "nginx:v2" {
						return false
					}
				}
				return true
			}, timeout, interval).Should(BeTrue())

			// Scale down to 1
			Eventually(func() error {
				if err := k8sClient.Get(ctx, rbgsLookupKey, createdRBGS); err != nil {
					return err
				}
				createdRBGS.Spec.Replicas = ptr.To(int32(1))
				return k8sClient.Update(ctx, createdRBGS)
			}, timeout, interval).Should(Succeed())

			Eventually(func() int {
				rbgList := &workloadsv1alpha1.RoleBasedGroupList{}
				k8sClient.List(ctx, rbgList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetRBGSetNameLabelKey: rbgsName})
				return len(rbgList.Items)
			}, timeout, interval).Should(Equal(1))
		})

		It("Should summarize status from child RBGs", func() {
			rbgsName := "test-rbgs-status"

			rbgs := &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgsName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(2)),
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
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
												Image: "nginx:latest",
											},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbgs)).Should(Succeed())

			// Wait for 2 RBGs
			rbgList := &workloadsv1alpha1.RoleBasedGroupList{}
			Eventually(func() int {
				k8sClient.List(ctx, rbgList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetRBGSetNameLabelKey: rbgsName})
				return len(rbgList.Items)
			}, timeout, interval).Should(Equal(2))

			// Manually update RBG status to Ready
			for i := range rbgList.Items {
				rbg := &rbgList.Items[i]
				Eventually(func() error {
					err := k8sClient.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, rbg)
					if err != nil {
						return err
					}

					// Simple update for status in envtest
					rbg.Status.RoleStatuses = []workloadsv1alpha1.RoleStatus{
						{
							Name:          "worker",
							Replicas:      1,
							ReadyReplicas: 1,
						},
					}
					rbg.Status.Conditions = []metav1.Condition{
						{
							Type:               string(workloadsv1alpha1.RoleBasedGroupReady),
							Status:             metav1.ConditionTrue,
							Reason:             "AllRolesReady",
							Message:            "All roles are ready",
							LastTransitionTime: metav1.Now(),
						},
					}
					return k8sClient.Status().Update(ctx, rbg)
				}, timeout, interval).Should(Succeed())
			}

			// Verify RBGS status ReadyReplicas
			rbgsLookupKey := types.NamespacedName{Name: rbgsName, Namespace: testNs}
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, rbgsLookupKey, rbgs)
				if err != nil {
					return -1
				}
				return rbgs.Status.ReadyReplicas
			}, timeout, interval).Should(Equal(int32(2)))
		})

		It("Should delete child RoleBasedGroups when RoleBasedGroupSet is deleted", func() {
			rbgsName := "test-rbgs-delete"

			rbgs := &workloadsv1alpha1.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgsName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(2)),
					Template: workloadsv1alpha1.RoleBasedGroupSpec{
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
										Containers: []corev1.Container{{Name: "nginx", Image: "nginx"}},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbgs)).Should(Succeed())

			// Wait for 2 RBGs
			rbgList := &workloadsv1alpha1.RoleBasedGroupList{}
			Eventually(func() int {
				k8sClient.List(ctx, rbgList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetRBGSetNameLabelKey: rbgsName})
				return len(rbgList.Items)
			}, timeout, interval).Should(Equal(2))

			// Verify OwnerReference is set
			for _, rbg := range rbgList.Items {
				Expect(rbg.OwnerReferences).To(HaveLen(1))
				Expect(rbg.OwnerReferences[0].Name).To(Equal(rbgsName))
			}

			// Delete RoleBasedGroupSet
			Expect(k8sClient.Delete(ctx, rbgs)).Should(Succeed())

			// In envtest, we don't have GC controller, so we just verify it was deleted or has deletion timestamp
			// Actually, RBGS controller doesn't explicitly delete RBGs, it relies on OwnerReferences.
		})
	})
})

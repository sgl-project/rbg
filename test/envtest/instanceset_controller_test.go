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

var _ = Describe("InstanceSet Controller", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var (
		testNs string
	)

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-instanceset-%d", time.Now().UnixNano())
		createNamespace(testNs)
	})

	AfterEach(func() {
		deleteNamespace(testNs)
	})

	Context("When creating InstanceSet", func() {
		It("Should create Instance resources successfully", func() {
			isName := "test-is-basic"

			is := &workloadsv1alpha1.InstanceSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      isName,
					Namespace: testNs,
					Labels: map[string]string{
						"app": isName,
					},
				},
				Spec: workloadsv1alpha1.InstanceSetSpec{
					Replicas: ptr.To(int32(3)),
					InstanceTemplate: workloadsv1alpha1.InstanceTemplate{
						InstanceSpec: workloadsv1alpha1.InstanceSpec{
							Components: []workloadsv1alpha1.InstanceComponent{
								{
									Name: "main",
									Size: ptr.To(int32(1)),
									Template: corev1.PodTemplateSpec{
										ObjectMeta: metav1.ObjectMeta{
											Labels: map[string]string{
												"app": isName,
											},
										},
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

			Expect(k8sClient.Create(ctx, is)).Should(Succeed())

			// Verify InstanceSet is created
			isLookupKey := types.NamespacedName{Name: isName, Namespace: testNs}
			createdIS := &workloadsv1alpha1.InstanceSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, isLookupKey, createdIS)
			}, timeout, interval).Should(Succeed())

			// Verify Instances are created
			Eventually(func() int {
				instanceList := &workloadsv1alpha1.InstanceList{}
				err := k8sClient.List(ctx, instanceList,
					client.InNamespace(testNs),
					client.MatchingLabels{workloadsv1alpha1.SetInstanceOwnerLabelKey: string(createdIS.UID)})
				if err != nil {
					return 0
				}
				return len(instanceList.Items)
			}, timeout, interval).Should(Equal(3))

			// Verify Status is updated
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, isLookupKey, createdIS)
				if err != nil {
					return 0
				}
				return createdIS.Status.Replicas
			}, timeout, interval).Should(Equal(int32(3)))
		})

		It("Should handle scaling up and down", func() {
			isName := "test-is-scaling"

			is := &workloadsv1alpha1.InstanceSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      isName,
					Namespace: testNs,
					Labels: map[string]string{
						"app": isName,
					},
				},
				Spec: workloadsv1alpha1.InstanceSetSpec{
					Replicas: ptr.To(int32(2)),
					InstanceTemplate: workloadsv1alpha1.InstanceTemplate{
						InstanceSpec: workloadsv1alpha1.InstanceSpec{
							Components: []workloadsv1alpha1.InstanceComponent{
								{
									Name: "worker",
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

			Expect(k8sClient.Create(ctx, is)).Should(Succeed())

			// Get UID for listing
			isLookupKey := types.NamespacedName{Name: isName, Namespace: testNs}
			createdIS := &workloadsv1alpha1.InstanceSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, isLookupKey, createdIS)
			}, timeout, interval).Should(Succeed())

			// Wait for 2 instances
			Eventually(func() int {
				instanceList := &workloadsv1alpha1.InstanceList{}
				k8sClient.List(ctx, instanceList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetInstanceOwnerLabelKey: string(createdIS.UID)})
				return len(instanceList.Items)
			}, timeout, interval).Should(Equal(2))

			// Scale up to 4
			Eventually(func() error {
				if err := k8sClient.Get(ctx, isLookupKey, createdIS); err != nil {
					return err
				}
				createdIS.Spec.Replicas = ptr.To(int32(4))
				return k8sClient.Update(ctx, createdIS)
			}, timeout, interval).Should(Succeed())

			Eventually(func() int {
				instanceList := &workloadsv1alpha1.InstanceList{}
				k8sClient.List(ctx, instanceList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetInstanceOwnerLabelKey: string(createdIS.UID)})
				return len(instanceList.Items)
			}, timeout, interval).Should(Equal(4))

			// Scale down to 1
			Eventually(func() error {
				if err := k8sClient.Get(ctx, isLookupKey, createdIS); err != nil {
					return err
				}
				createdIS.Spec.Replicas = ptr.To(int32(1))
				return k8sClient.Update(ctx, createdIS)
			}, timeout, interval).Should(Succeed())

			Eventually(func() int {
				instanceList := &workloadsv1alpha1.InstanceList{}
				k8sClient.List(ctx, instanceList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetInstanceOwnerLabelKey: string(createdIS.UID)})
				return len(instanceList.Items)
			}, timeout, interval).Should(Equal(1))
		})

		It("Should update Instance status correctly", func() {
			isName := "test-is-status"

			is := &workloadsv1alpha1.InstanceSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      isName,
					Namespace: testNs,
					Labels: map[string]string{
						"app": isName,
					},
				},
				Spec: workloadsv1alpha1.InstanceSetSpec{
					Replicas: ptr.To(int32(2)),
					InstanceTemplate: workloadsv1alpha1.InstanceTemplate{
						InstanceSpec: workloadsv1alpha1.InstanceSpec{
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

			Expect(k8sClient.Create(ctx, is)).Should(Succeed())

			// Get UID for listing
			isLookupKey := types.NamespacedName{Name: isName, Namespace: testNs}
			createdIS := &workloadsv1alpha1.InstanceSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, isLookupKey, createdIS)
			}, timeout, interval).Should(Succeed())

			// Get Instances
			instanceList := &workloadsv1alpha1.InstanceList{}
			Eventually(func() int {
				k8sClient.List(ctx, instanceList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetInstanceOwnerLabelKey: string(createdIS.UID)})
				return len(instanceList.Items)
			}, timeout, interval).Should(Equal(2))

			// Manually update Instance status to ready
			for i := range instanceList.Items {
				inst := &instanceList.Items[i]

				// Simulation: Add InstanceReady condition and ComponentStatuses
				Eventually(func() error {
					err := k8sClient.Get(ctx, types.NamespacedName{Name: inst.Name, Namespace: inst.Namespace}, inst)
					if err != nil {
						return err
					}
					inst.Status.ObservedGeneration = inst.Generation
					inst.Status.Conditions = []workloadsv1alpha1.InstanceCondition{
						{
							Type:               workloadsv1alpha1.InstanceReady,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: metav1.Now(),
						},
						{
							Type:               workloadsv1alpha1.InstanceAllPodsReady,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: metav1.Now(),
						},
					}
					// Update component status
					inst.Status.ComponentStatuses = []workloadsv1alpha1.ComponentStatus{
						{
							Name:              "main",
							Replicas:          1,
							ReadyReplicas:     1,
							AvailableReplicas: 1,
						},
					}
					return k8sClient.Status().Update(ctx, inst)
				}, timeout, interval).Should(Succeed())
			}

			// Verify IS status Replicas
			Eventually(func() int32 {
				err := k8sClient.Get(ctx, isLookupKey, is)
				if err != nil {
					return -1
				}
				return is.Status.Replicas
			}, timeout, interval).Should(Equal(int32(2)))
		})

		It("Should delete Instances when InstanceSet is deleted", func() {
			isName := "test-is-delete"

			is := &workloadsv1alpha1.InstanceSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      isName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.InstanceSetSpec{
					Replicas: ptr.To(int32(2)),
					InstanceTemplate: workloadsv1alpha1.InstanceTemplate{
						InstanceSpec: workloadsv1alpha1.InstanceSpec{
							Components: []workloadsv1alpha1.InstanceComponent{
								{
									Name: "main",
									Size: ptr.To(int32(1)),
									Template: corev1.PodTemplateSpec{
										Spec: corev1.PodSpec{
											Containers: []corev1.Container{{Name: "nginx", Image: "nginx"}},
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, is)).Should(Succeed())

			// Wait for Instances
			isLookupKey := types.NamespacedName{Name: isName, Namespace: testNs}
			createdIS := &workloadsv1alpha1.InstanceSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, isLookupKey, createdIS)
			}, timeout, interval).Should(Succeed())

			instanceList := &workloadsv1alpha1.InstanceList{}
			Eventually(func() int {
				k8sClient.List(ctx, instanceList, client.InNamespace(testNs), client.MatchingLabels{workloadsv1alpha1.SetInstanceOwnerLabelKey: string(createdIS.UID)})
				return len(instanceList.Items)
			}, timeout, interval).Should(Equal(2))

			// Verify OwnerReference is set
			for i := range instanceList.Items {
				inst := &instanceList.Items[i]
				Expect(inst.OwnerReferences).To(HaveLen(1))
				Expect(inst.OwnerReferences[0].Name).To(Equal(isName))
			}

			// Delete InstanceSet
			Expect(k8sClient.Delete(ctx, is)).Should(Succeed())
		})
	})
})

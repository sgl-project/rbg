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

var _ = Describe("Instance Controller", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var (
		testNs string
	)

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-instance-%d", time.Now().UnixNano())
		createNamespace(testNs)
	})

	AfterEach(func() {
		deleteNamespace(testNs)
	})

	Context("When creating Instance", func() {
		It("Should create Pods for each component successfully", func() {
			instanceName := "test-instance-pods"
			instance := &workloadsv1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      instanceName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.InstanceSpec{
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
						{
							Name: "sidecar",
							Size: ptr.To(int32(1)),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "fluentd",
											Image: "fluentd:latest",
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, instance)).Should(Succeed())

			// Verify Pods are created
			Eventually(func() int {
				podList := &corev1.PodList{}
				err := k8sClient.List(ctx, podList, client.InNamespace(testNs))
				if err != nil {
					return 0
				}
				return len(podList.Items)
			}, timeout, interval).Should(Equal(2))

			// Verify Pod owner
			podList := &corev1.PodList{}
			Expect(k8sClient.List(ctx, podList, client.InNamespace(testNs))).Should(Succeed())
			for _, pod := range podList.Items {
				Expect(pod.OwnerReferences).To(HaveLen(1))
				Expect(pod.OwnerReferences[0].Name).To(Equal(instanceName))
			}
		})

		It("Should update Instance status when Pods are ready", func() {
			instanceName := "test-instance-status"
			instance := &workloadsv1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      instanceName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.InstanceSpec{
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
			}

			Expect(k8sClient.Create(ctx, instance)).Should(Succeed())

			// Get the Pod
			podList := &corev1.PodList{}
			Eventually(func() int {
				k8sClient.List(ctx, podList, client.InNamespace(testNs))
				return len(podList.Items)
			}, timeout, interval).Should(Equal(1))

			pod := &podList.Items[0]

			// Update Pod status to Ready in envtest
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, pod)
				if err != nil {
					return err
				}
				pod.Status.Phase = corev1.PodRunning
				pod.Status.Conditions = []corev1.PodCondition{
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionTrue,
					},
				}
				return k8sClient.Status().Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			// Verify Instance status is updated
			Eventually(func() bool {
				createdInstance := &workloadsv1alpha1.Instance{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: testNs}, createdInstance)
				if err != nil {
					return false
				}

				// Check for InstanceReady condition
				ready := false
				for _, cond := range createdInstance.Status.Conditions {
					if cond.Type == workloadsv1alpha1.InstanceReady && cond.Status == corev1.ConditionTrue {
						ready = true
						break
					}
				}

				if !ready {
					return false
				}

				// Check component status
				if len(createdInstance.Status.ComponentStatuses) == 0 {
					return false
				}
				return createdInstance.Status.ComponentStatuses[0].ReadyReplicas == 1
			}, timeout, interval).Should(BeTrue())
		})

		It("Should delete Pods when Instance is deleted", func() {
			instanceName := "test-instance-delete"
			instance := &workloadsv1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      instanceName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.InstanceSpec{
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
			}

			Expect(k8sClient.Create(ctx, instance)).Should(Succeed())

			// Wait for Pod
			Eventually(func() int {
				podList := &corev1.PodList{}
				k8sClient.List(ctx, podList, client.InNamespace(testNs))
				return len(podList.Items)
			}, timeout, interval).Should(Equal(1))

			// Delete Instance
			Expect(k8sClient.Delete(ctx, instance)).Should(Succeed())

			// Verify Pods have OwnerReference set to Instance
			podList := &corev1.PodList{}
			Expect(k8sClient.List(ctx, podList, client.InNamespace(testNs))).Should(Succeed())
			for _, pod := range podList.Items {
				Expect(pod.OwnerReferences).To(HaveLen(1))
				Expect(pod.OwnerReferences[0].Name).To(Equal(instanceName))
			}
		})
	})
})

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

var _ = Describe("Hello World Test - Basic Envtest Framework Validation", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When testing basic Kubernetes operations", func() {
		It("Should successfully create and retrieve a Namespace", func() {
			testNs := fmt.Sprintf("test-namespace-hello-%d", time.Now().UnixNano())
			ns := createNamespace(testNs)
			Expect(ns).NotTo(BeNil())
			Expect(ns.Name).To(Equal(testNs))

			// Verify we can retrieve it
			retrievedNs := &corev1.Namespace{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: testNs}, retrievedNs)
			}, timeout, interval).Should(Succeed())

			Expect(retrievedNs.Name).To(Equal(testNs))

			// Cleanup
			deleteNamespace(testNs)
		})

		It("Should successfully create and retrieve a RoleBasedGroup", func() {
			testNs := fmt.Sprintf("test-rbg-hello-%d", time.Now().UnixNano())
			createNamespace(testNs)
			defer deleteNamespace(testNs)

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hello-rbg",
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

			// Verify we can retrieve it
			retrievedRBG := &workloadsv1alpha1.RoleBasedGroup{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "hello-rbg",
					Namespace: testNs,
				}, retrievedRBG)
			}, timeout, interval).Should(Succeed())

			Expect(retrievedRBG.Name).To(Equal("hello-rbg"))
			Expect(retrievedRBG.Spec.Roles).To(HaveLen(1))
			Expect(retrievedRBG.Spec.Roles[0].Name).To(Equal("worker"))
		})

		It("Should successfully create a Deployment", func() {
			testNs := fmt.Sprintf("test-deployment-hello-%d", time.Now().UnixNano())
			createNamespace(testNs)
			defer deleteNamespace(testNs)

			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hello-deployment",
					Namespace: testNs,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "hello",
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": "hello",
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
			}

			Expect(k8sClient.Create(ctx, deployment)).Should(Succeed())

			// Verify we can retrieve it
			retrievedDeployment := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "hello-deployment",
					Namespace: testNs,
				}, retrievedDeployment)
			}, timeout, interval).Should(Succeed())

			Expect(retrievedDeployment.Name).To(Equal("hello-deployment"))
			Expect(*retrievedDeployment.Spec.Replicas).To(Equal(int32(1)))
		})

		It("Should successfully create an InstanceSet", func() {
			testNs := fmt.Sprintf("test-instanceset-hello-%d", time.Now().UnixNano())
			createNamespace(testNs)
			defer deleteNamespace(testNs)

			instanceSet := &workloadsv1alpha1.InstanceSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "hello-instanceset",
					Namespace: testNs,
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
										ObjectMeta: metav1.ObjectMeta{
											Labels: map[string]string{
												"app": "hello-instance",
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

			Expect(k8sClient.Create(ctx, instanceSet)).Should(Succeed())

			// Verify we can retrieve it
			retrievedInstanceSet := &workloadsv1alpha1.InstanceSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "hello-instanceset",
					Namespace: testNs,
				}, retrievedInstanceSet)
			}, timeout, interval).Should(Succeed())

			Expect(retrievedInstanceSet.Name).To(Equal("hello-instanceset"))
			Expect(*retrievedInstanceSet.Spec.Replicas).To(Equal(int32(2)))
		})
	})

	Context("When testing CRD validation", func() {
		It("Should validate RoleBasedGroup CRD is loaded", func() {
			// Verify that we can create RBG objects, which implies CRD is loaded
			testNs := fmt.Sprintf("test-crd-validation-%d", time.Now().UnixNano())
			createNamespace(testNs)
			defer deleteNamespace(testNs)

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "crd-validation-rbg",
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "test-role",
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
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())
		})
	})
})

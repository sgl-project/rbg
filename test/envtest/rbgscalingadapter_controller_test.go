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

var _ = Describe("RoleBasedGroupScalingAdapter Controller", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var (
		testNs string
	)

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-scaling-adapter-%d", time.Now().UnixNano())
		createNamespace(testNs)
	})

	AfterEach(func() {
		deleteNamespace(testNs)
	})

	Context("When managing RBG with ScalingAdapter", func() {
		It("Should automatically create ScalingAdapter when enabled", func() {
			rbgName := "test-rbg-with-scaling"
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
							ScalingAdapter: &workloadsv1alpha1.ScalingAdapter{
								Enable: true,
							},
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

			// Simulate Deployment status update in envtest
			deployLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "worker"),
				Namespace: testNs,
			}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				if err := k8sClient.Get(ctx, deployLookupKey, createdDeploy); err != nil {
					return err
				}
				createdDeploy.Status.ObservedGeneration = createdDeploy.Generation
				createdDeploy.Status.Replicas = 2
				createdDeploy.Status.ReadyReplicas = 2
				return k8sClient.Status().Update(ctx, createdDeploy)
			}, timeout, interval).Should(Succeed())

			// Verify ScalingAdapter is created
			// The name follows the pattern: {rbgName}-{roleName}
			adapterName := fmt.Sprintf("%s-%s", rbgName, "worker")
			adapterLookupKey := types.NamespacedName{Name: adapterName, Namespace: testNs}
			createdAdapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{}

			Eventually(func() error {
				return k8sClient.Get(ctx, adapterLookupKey, createdAdapter)
			}, timeout, interval).Should(Succeed())

			Expect(createdAdapter.Spec.ScaleTargetRef.Name).To(Equal(rbgName))
			Expect(createdAdapter.Spec.ScaleTargetRef.Role).To(Equal("worker"))
		})

		It("Should delete ScalingAdapter when disabled", func() {
			rbgName := "test-rbg-disable-scaling"
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
							ScalingAdapter: &workloadsv1alpha1.ScalingAdapter{
								Enable: true,
							},
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

			// Simulate Deployment status update in envtest
			deployLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "worker"),
				Namespace: testNs,
			}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				if err := k8sClient.Get(ctx, deployLookupKey, createdDeploy); err != nil {
					return err
				}
				createdDeploy.Status.ObservedGeneration = createdDeploy.Generation
				createdDeploy.Status.Replicas = 2
				createdDeploy.Status.ReadyReplicas = 2
				return k8sClient.Status().Update(ctx, createdDeploy)
			}, timeout, interval).Should(Succeed())

			adapterName := fmt.Sprintf("%s-%s", rbgName, "worker")
			adapterLookupKey := types.NamespacedName{Name: adapterName, Namespace: testNs}
			createdAdapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{}

			// Ensure it exists first
			Eventually(func() error {
				return k8sClient.Get(ctx, adapterLookupKey, createdAdapter)
			}, timeout, interval).Should(Succeed())

			// Disable scaling adapter
			Eventually(func() error {
				latestRBG := &workloadsv1alpha1.RoleBasedGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: rbgName, Namespace: testNs}, latestRBG)
				if err != nil {
					return err
				}
				latestRBG.Spec.Roles[0].ScalingAdapter.Enable = false
				return k8sClient.Update(ctx, latestRBG)
			}, timeout, interval).Should(Succeed())

			// Verify ScalingAdapter is deleted
			Eventually(func() bool {
				err := k8sClient.Get(ctx, adapterLookupKey, createdAdapter)
				return client.IgnoreNotFound(err) == nil && (err != nil)
			}, timeout, interval).Should(BeTrue())
		})
	})
})

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

var _ = Describe("RoleBasedGroup Controller - Advanced Features", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var (
		testNs string
	)

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-rbg-adv-%d", time.Now().UnixNano())
		createNamespace(testNs)
	})

	AfterEach(func() {
		deleteNamespace(testNs)
	})

	Context("When using Exclusive Topology", func() {
		It("Should inject pod affinity and anti-affinity", func() {
			rbgName := "test-rbg-exclusive"
			topologyKey := "kubernetes.io/hostname"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
					Annotations: map[string]string{
						workloadsv1alpha1.ExclusiveKeyAnnotationKey: topologyKey,
					},
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

			// Verify Deployment has affinity injected
			deployLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "worker"),
				Namespace: testNs,
			}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() bool {
				err := k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				if err != nil {
					return false
				}
				affinity := createdDeploy.Spec.Template.Spec.Affinity
				if affinity == nil || affinity.PodAffinity == nil || affinity.PodAntiAffinity == nil {
					return false
				}

				// Check PodAffinity
				hasAffinity := false
				for _, term := range affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
					if term.TopologyKey == topologyKey {
						hasAffinity = true
						break
					}
				}

				// Check PodAntiAffinity
				hasAntiAffinity := false
				for _, term := range affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
					if term.TopologyKey == topologyKey {
						hasAntiAffinity = true
						break
					}
				}

				return hasAffinity && hasAntiAffinity
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("When using Gang Scheduling", func() {
		It("Should inject Kube Scheduling labels", func() {
			rbgName := "test-rbg-kube-gang"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					PodGroupPolicy: &workloadsv1alpha1.PodGroupPolicy{
						PodGroupPolicySource: workloadsv1alpha1.PodGroupPolicySource{
							KubeScheduling: &workloadsv1alpha1.KubeSchedulingPodGroupPolicySource{
								ScheduleTimeoutSeconds: ptr.To(int32(60)),
							},
						},
					},
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

			// Verify Deployment has labels injected
			deployLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "worker"),
				Namespace: testNs,
			}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() string {
				k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return createdDeploy.Spec.Template.Labels["pod-group.scheduling.sigs.k8s.io/name"]
			}, timeout, interval).Should(Equal(rbgName))
		})

		It("Should inject Volcano Scheduling annotations", func() {
			rbgName := "test-rbg-volcano-gang"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					PodGroupPolicy: &workloadsv1alpha1.PodGroupPolicy{
						PodGroupPolicySource: workloadsv1alpha1.PodGroupPolicySource{
							VolcanoScheduling: &workloadsv1alpha1.VolcanoSchedulingPodGroupPolicySource{
								Queue: "default",
							},
						},
					},
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

			// Verify Deployment has annotations injected
			deployLookupKey := types.NamespacedName{
				Name:      fmt.Sprintf("%s-%s", rbgName, "worker"),
				Namespace: testNs,
			}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() string {
				k8sClient.Get(ctx, deployLookupKey, createdDeploy)
				return createdDeploy.Spec.Template.Annotations["scheduling.k8s.io/group-name"]
			}, timeout, interval).Should(Equal(rbgName))
		})
	})

	Context("When role is removed from RBG", func() {
		It("Should delete orphan workloads", func() {
			rbgName := "test-rbg-orphan"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "role1",
							Replicas: ptr.To(int32(1)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "n1", Image: "nginx"}},
								},
							},
						},
						{
							Name:     "role2",
							Replicas: ptr.To(int32(1)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "n2", Image: "nginx"}},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// Verify both Deployments are created
			Eventually(func() int {
				deployList := &appsv1.DeploymentList{}
				k8sClient.List(ctx, deployList, client.InNamespace(testNs))
				return len(deployList.Items)
			}, timeout, interval).Should(Equal(2))

			// Remove role2 from RBG
			Eventually(func() error {
				createdRBG := &workloadsv1alpha1.RoleBasedGroup{}
				k8sClient.Get(ctx, types.NamespacedName{Name: rbgName, Namespace: testNs}, createdRBG)
				createdRBG.Spec.Roles = []workloadsv1alpha1.RoleSpec{createdRBG.Spec.Roles[0]}
				return k8sClient.Update(ctx, createdRBG)
			}, timeout, interval).Should(Succeed())

			// Verify role2 Deployment is deleted
			Eventually(func() int {
				deployList := &appsv1.DeploymentList{}
				k8sClient.List(ctx, deployList, client.InNamespace(testNs))
				return len(deployList.Items)
			}, timeout, interval).Should(Equal(1))

			deployList := &appsv1.DeploymentList{}
			k8sClient.List(ctx, deployList, client.InNamespace(testNs))
			Expect(deployList.Items[0].Name).To(Equal(fmt.Sprintf("%s-%s", rbgName, "role1")))
		})
	})
})

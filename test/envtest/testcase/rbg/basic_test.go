/*
Copyright 2025 The RBG Authors.

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

package rbg

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("RoleBasedGroup Controller", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var (
		testNs string
	)

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-rbg-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	Context("When creating a RoleBasedGroup", func() {
		It("Should create child Deployment for each role", func() {
			rbgName := "test-rbg-basic"

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
							TemplateSource: workloadsv1alpha1.TemplateSource{
								Template: ptr.To(corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{
										Containers: []corev1.Container{
											{
												Name:  "nginx",
												Image: "nginx:latest",
											},
										},
									},
								}),
							},
						},
					},
				},
			}

			// Create RoleBasedGroup
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Verify RoleBasedGroup is created
			rbgLookupKey := types.NamespacedName{Name: rbgName, Namespace: testNs}
			createdRBG := &workloadsv1alpha1.RoleBasedGroup{}

			Eventually(func() error {
				return testutil.K8sClient.Get(testutil.Ctx, rbgLookupKey, createdRBG)
			}, timeout, interval).Should(Succeed())

			Expect(createdRBG.Spec.Roles).To(HaveLen(1))
			Expect(createdRBG.Spec.Roles[0].Name).To(Equal("worker"))
		})

		It("Should reject v1alpha2 custom components with duplicate names", func() {
			rbg := &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-v1alpha2-components",
					Namespace: testNs,
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(1)),
							Pattern: workloadsv1alpha2.Pattern{
								CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
									Components: []workloadsv1alpha2.InstanceComponent{
										{
											Name:     "worker",
											Template: nginxPodTemplate(),
										},
										{
											Name:     "worker",
											Template: nginxPodTemplate(),
										},
									},
								},
							},
						},
					},
				},
			}

			expectInvalidCreate(rbg)
		})

		It("Should reject v1alpha1 components with duplicate names", func() {
			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-v1alpha1-components",
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(1)),
							Components: []workloadsv1alpha1.InstanceComponent{
								{
									Name:     "worker",
									Template: nginxPodTemplate(),
								},
								{
									Name:     "worker",
									Template: nginxPodTemplate(),
								},
							},
						},
					},
				},
			}

			expectInvalidCreate(rbg)
		})

		It("Should reject RoleInstance components with duplicate names", func() {
			instance := &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-roleinstance-components",
					Namespace: testNs,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{
							Name:     "worker",
							Template: nginxPodTemplate(),
						},
						{
							Name:     "worker",
							Template: nginxPodTemplate(),
						},
					},
				},
			}

			expectInvalidCreate(instance)
		})
	})
})

func nginxPodTemplate() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "nginx",
					Image: "nginx:latest",
				},
			},
		},
	}
}

func expectInvalidCreate(obj client.Object) {
	err := testutil.K8sClient.Create(testutil.Ctx, obj)
	Expect(err).To(HaveOccurred())
	Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
}

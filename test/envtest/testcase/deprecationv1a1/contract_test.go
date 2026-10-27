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

package deprecationv1a1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappersv1 "sigs.k8s.io/rbgs/test/wrappers/v1alpha1"
)

var _ = Describe("v1alpha1 deprecation contract", Ordered, func() {
	BeforeEach(func() {
		testutil.ContractWarnings.Reset()
	})

	It("returns the on-the-wire deprecation warning for v1alpha1 creates", func() {
		rbg := buildV1alpha1RBG("contract-create")
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
		Expect(testutil.ContractWarnings.Containing("v1alpha1 is deprecated")).NotTo(BeEmpty())
		Expect(testutil.ContractWarnings.Containing("removed in 0.10.0")).NotTo(BeEmpty())
	})

	It("returns the on-the-wire deprecation warning for v1alpha1 updates", func() {
		rbg := buildV1alpha1RBG("contract-update")
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
		testutil.ContractWarnings.Reset()

		rbg.Spec.Roles[0].Replicas = ptr.To(int32(2))
		Expect(testutil.K8sClient.Update(testutil.Ctx, rbg)).To(Succeed())
		Expect(testutil.ContractWarnings.Containing("v1alpha1 is deprecated")).NotTo(BeEmpty())
	})

	It("returns the on-the-wire warning for a warning-only kind create", func() {
		instanceSet := &workloadsv1alpha1.InstanceSet{
			ObjectMeta: metav1.ObjectMeta{Name: "contract-instanceset", Namespace: contractNamespace},
			Spec: workloadsv1alpha1.InstanceSetSpec{
				Replicas: ptr.To(int32(1)),
				InstanceTemplate: workloadsv1alpha1.InstanceTemplate{
					InstanceSpec: workloadsv1alpha1.InstanceSpec{
						Components: []workloadsv1alpha1.InstanceComponent{{
							Name: "worker",
							Size: ptr.To(int32(1)),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "nginx", Image: "nginx:latest"}},
								},
							},
						}},
					},
				},
			},
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, instanceSet)).To(Succeed())
		Expect(testutil.ContractWarnings.Containing("v1alpha1 is deprecated")).NotTo(BeEmpty())
	})

	It("rejects a new deprecated workload use through the v1alpha1 API", func() {
		rbg := buildV1alpha1RBG("contract-reject")
		rbg.Spec.Roles[0].Workload = workloadsv1alpha1.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		}
		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new uses are not accepted"))
	})

	It("rejects a new deprecated workload use in a v1alpha1 set template", func() {
		set := wrappersv1.BuildBasicRoleBasedGroupSet("contract-set", contractNamespace).Obj()
		set.Spec.Template.Roles = []workloadsv1alpha1.RoleSpec{
			func() workloadsv1alpha1.RoleSpec {
				role := wrappersv1.BuildBasicRole("member").Obj()
				role.Workload = workloadsv1alpha1.WorkloadSpec{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
				}
				return role
			}(),
		}
		err := testutil.K8sClient.Create(testutil.Ctx, set)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new uses are not accepted"))
	})

	It("does not warn on v1alpha2 writes through Exact match policy", func() {
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "contract-v1a2", Namespace: contractNamespace},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					Pattern: workloadsv1alpha2.Pattern{
						StandalonePattern: &workloadsv1alpha2.StandalonePattern{
							TemplateSource: workloadsv1alpha2.TemplateSource{
								Template: ptr.To(corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{
										Containers: []corev1.Container{{Name: "nginx", Image: "nginx:latest"}},
									},
								}),
							},
						},
					},
				}},
			},
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
		Expect(testutil.ContractWarnings.All()).To(BeEmpty(),
			"a v1alpha2 write must not route to the v1alpha1 Exact-match webhooks")
	})

	It("leaves v1alpha1 deletes ungated by the deprecation webhooks", func() {
		rbg := buildV1alpha1RBG("contract-delete")
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

		stored := &workloadsv1alpha1.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(
			testutil.Ctx, types.NamespacedName{Name: rbg.Name, Namespace: contractNamespace}, stored,
		)).To(Succeed())
		Expect(testutil.K8sClient.Delete(testutil.Ctx, stored)).To(Succeed())
	})
})

func buildV1alpha1RBG(name string) *workloadsv1alpha1.RoleBasedGroup {
	return wrappersv1.BuildBasicRoleBasedGroup(name, contractNamespace).
		WithRoles([]workloadsv1alpha1.RoleSpec{
			wrappersv1.BuildBasicRole("worker").Obj(),
		}).Obj()
}

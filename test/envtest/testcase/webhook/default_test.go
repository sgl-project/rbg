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

package webhook

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
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappersv1 "sigs.k8s.io/rbgs/test/wrappers/v1alpha1"
)

var _ = Describe("Mutating webhook defaulters", func() {
	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-webhook-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	Context("RoleBasedGroup", func() {
		It("heals the v1alpha1 Recreate spelling to RecreatePod on create", func() {
			rbg := buildRBG("heal-recreate", testNs, workloadsv1alpha2.LegacyRecreateUpdateStrategyType)
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

			stored := getRBG(rbg.Name, testNs)
			Expect(stored.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type).To(
				Equal(workloadsv1alpha2.RecreatePodUpdateStrategyType),
			)
		})

		It("defaults an explicit empty type to InPlaceIfPossible on create", func() {
			rbg := buildRBG("default-empty", testNs, workloadsv1alpha2.UpdateStrategyType(""))
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

			stored := getRBG(rbg.Name, testNs)
			Expect(stored.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type).To(
				Equal(workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType),
			)
		})

		It("heals a legacy type on update, not only on create", func() {
			rbg := buildRBG("heal-update", testNs, workloadsv1alpha2.LegacyRecreateUpdateStrategyType)
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

			// Re-write the same object; the mutating webhook must normalize it again.
			stored := getRBG(rbg.Name, testNs)
			Expect(stored.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type).To(
				Equal(workloadsv1alpha2.RecreatePodUpdateStrategyType),
			)
			Expect(testutil.K8sClient.Update(testutil.Ctx, stored)).To(Succeed())

			after := getRBG(rbg.Name, testNs)
			Expect(after.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type).To(
				Equal(workloadsv1alpha2.RecreatePodUpdateStrategyType),
			)
		})

		It("still passes valid types through unchanged", func() {
			rbg := buildRBG("valid-type", testNs, workloadsv1alpha2.InPlaceOnlyUpdateStrategyType)
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

			stored := getRBG(rbg.Name, testNs)
			Expect(stored.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type).To(
				Equal(workloadsv1alpha2.InPlaceOnlyUpdateStrategyType),
			)
		})
	})

	Context("RoleBasedGroupSet", func() {
		It("heals the template's legacy Recreate spelling to RecreatePod on create", func() {
			rbgset := &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "heal-set", Namespace: testNs},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(1)),
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Spec: workloadsv1alpha2.RoleBasedGroupSpec{
							Roles: []workloadsv1alpha2.RoleSpec{
								*legacyStrategyRole("member"),
							},
						},
					},
				},
			}
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbgset)).To(Succeed())

			stored := &workloadsv1alpha2.RoleBasedGroupSet{}
			Expect(testutil.K8sClient.Get(
				testutil.Ctx, types.NamespacedName{Name: "heal-set", Namespace: testNs}, stored,
			)).To(Succeed())
			Expect(stored.Spec.GroupTemplate.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type).To(
				Equal(workloadsv1alpha2.RecreatePodUpdateStrategyType),
			)
		})
	})

	Context("RoleInstanceSet", func() {
		It("heals the v1alpha1 Recreate spelling to RecreatePod on create", func() {
			ris := buildRIS("heal-ris", testNs, workloadsv1alpha2.LegacyRecreateUpdateStrategyType)
			Expect(testutil.K8sClient.Create(testutil.Ctx, ris)).To(Succeed())

			stored := getRIS(ris.Name, testNs)
			Expect(stored.Spec.UpdateStrategy.Type).To(
				Equal(workloadsv1alpha2.RecreatePodUpdateStrategyType),
			)
		})

		It("defaults an explicit empty type to InPlaceIfPossible on create", func() {
			ris := buildRIS("default-ris", testNs, workloadsv1alpha2.UpdateStrategyType(""))
			Expect(testutil.K8sClient.Create(testutil.Ctx, ris)).To(Succeed())

			stored := getRIS(ris.Name, testNs)
			Expect(stored.Spec.UpdateStrategy.Type).To(
				Equal(workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType),
			)
		})
	})

	Context("v1alpha1 write", func() {
		It("heals the legacy Recreate spelling before the object is stored", func() {
			rbg := wrappersv1.BuildBasicRoleBasedGroup("v1a1-heal", testNs).
				WithRoles([]workloadsv1alpha1.RoleSpec{
					func() workloadsv1alpha1.RoleSpec {
						role := wrappersv1.BuildBasicRole("worker").Obj()
						role.RolloutStrategy = &workloadsv1alpha1.RolloutStrategy{
							Type: workloadsv1alpha1.RollingUpdateStrategyType,
							RollingUpdate: &workloadsv1alpha1.RollingUpdate{
								Type: workloadsv1alpha1.UpdateStrategyType("Recreate"),
							},
						}
						return role
					}(),
				}).Obj()
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

			stored := &workloadsv1alpha1.RoleBasedGroup{}
			Expect(testutil.K8sClient.Get(
				testutil.Ctx, types.NamespacedName{Name: "v1a1-heal", Namespace: testNs}, stored,
			)).To(Succeed())
			Expect(stored.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type).To(
				Equal(workloadsv1alpha1.UpdateStrategyType("RecreatePod")),
			)
		})
	})
})

func buildRBG(name, ns string, strategyType workloadsv1alpha2.UpdateStrategyType) *workloadsv1alpha2.RoleBasedGroup {
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
						Type: workloadsv1alpha2.RollingUpdateStrategyType,
						RollingUpdate: &workloadsv1alpha2.RollingUpdate{
							Type: strategyType,
						},
					},
					Pattern: workloadsv1alpha2.Pattern{
						StandalonePattern: &workloadsv1alpha2.StandalonePattern{
							TemplateSource: workloadsv1alpha2.TemplateSource{
								Template: ptr.To(webhookPodTemplate()),
							},
						},
					},
				},
			},
		},
	}
}

func legacyStrategyRole(name string) *workloadsv1alpha2.RoleSpec {
	return &workloadsv1alpha2.RoleSpec{
		Name:     name,
		Replicas: ptr.To(int32(1)),
		RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
			Type: workloadsv1alpha2.RollingUpdateStrategyType,
			RollingUpdate: &workloadsv1alpha2.RollingUpdate{
				Type: workloadsv1alpha2.LegacyRecreateUpdateStrategyType,
			},
		},
		Pattern: workloadsv1alpha2.Pattern{
			StandalonePattern: &workloadsv1alpha2.StandalonePattern{
				TemplateSource: workloadsv1alpha2.TemplateSource{
					Template: ptr.To(webhookPodTemplate()),
				},
			},
		},
	}
}

func buildRIS(name, ns string, strategyType workloadsv1alpha2.UpdateStrategyType) *workloadsv1alpha2.RoleInstanceSet {
	return &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: workloadsv1alpha2.RoleInstanceSetSpec{
			Replicas: ptr.To(int32(1)),
			RoleInstanceTemplate: workloadsv1alpha2.RoleInstanceTemplate{
				RoleInstanceSpec: workloadsv1alpha2.RoleInstanceSpec{
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{
							Name:     "worker",
							Size:     ptr.To(int32(1)),
							Template: webhookPodTemplate(),
						},
					},
				},
			},
			UpdateStrategy: workloadsv1alpha2.RoleInstanceSetUpdateStrategy{
				Type: strategyType,
			},
		},
	}
}

func getRBG(name, ns string) *workloadsv1alpha2.RoleBasedGroup {
	rbg := &workloadsv1alpha2.RoleBasedGroup{}
	Expect(testutil.K8sClient.Get(
		testutil.Ctx, client.ObjectKey{Namespace: ns, Name: name}, rbg,
	)).To(Succeed())
	return rbg
}

func getRIS(name, ns string) *workloadsv1alpha2.RoleInstanceSet {
	ris := &workloadsv1alpha2.RoleInstanceSet{}
	Expect(testutil.K8sClient.Get(
		testutil.Ctx, client.ObjectKey{Namespace: ns, Name: name}, ris,
	)).To(Succeed())
	return ris
}

func webhookPodTemplate() corev1.PodTemplateSpec {
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

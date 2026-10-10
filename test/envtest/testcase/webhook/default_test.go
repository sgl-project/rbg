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
	"k8s.io/apimachinery/pkg/util/intstr"
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
			rbg := buildRBG("heal-update", testNs, workloadsv1alpha2.RecreatePodUpdateStrategyType)
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

			// Send a legacy value through UPDATE: a stored object can only carry the
			// pre-enum spelling if it was written before the upgrade, but flipping the
			// field back to the legacy spelling is the closest in-envtest stand-in for
			// that stored state. The mutating webhook must heal it before validation.
			stored := getRBG(rbg.Name, testNs)
			stored.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type = workloadsv1alpha2.LegacyRecreateUpdateStrategyType
			Expect(testutil.K8sClient.Update(testutil.Ctx, stored)).To(Succeed())

			after := getRBG(rbg.Name, testNs)
			Expect(after.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type).To(
				Equal(workloadsv1alpha2.RecreatePodUpdateStrategyType),
				"the mutating webhook did not normalize the legacy type sent through update",
			)
		})

		It("still passes valid types through unchanged", func() {
			// Deprecated but still a valid enum value.
			rbg := buildRBG("valid-type", testNs, workloadsv1alpha2.UpdateStrategyType("InPlaceOnly"))
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

			stored := getRBG(rbg.Name, testNs)
			Expect(stored.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type).To(
				Equal(workloadsv1alpha2.UpdateStrategyType("InPlaceOnly")),
			)
		})
	})

	Context("RoleBasedGroupSet", func() {
		DescribeTable("validates rollout budgets on create and update through admission",
			func(strategy workloadsv1alpha2.GroupUpdateStrategyType, unavailable, surge intstr.IntOrString, wantErr string) {
				set := &workloadsv1alpha2.RoleBasedGroupSet{
					ObjectMeta: metav1.ObjectMeta{Name: "budgets", Namespace: testNs},
					Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
						Replicas: ptr.To(int32(3)),
						GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
							Spec: buildRBG("unused", testNs, workloadsv1alpha2.RecreatePodUpdateStrategyType).Spec,
						},
					},
				}
				Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())
				Expect(set.Spec.RolloutStrategy).To(BeNil())
				set.Spec.RolloutStrategy = &workloadsv1alpha2.GroupSetRolloutStrategy{
					Type: strategy, MaxUnavailable: &unavailable, MaxSurge: &surge,
				}
				fresh := set.DeepCopy()
				fresh.Name = "budgets-create"
				fresh.ResourceVersion, fresh.UID = "", ""
				for _, err := range []error{
					testutil.K8sClient.Create(testutil.Ctx, fresh),
					testutil.K8sClient.Update(testutil.Ctx, set),
				} {
					if wantErr != "" {
						Expect(err).To(HaveOccurred())
						Expect(err.Error()).To(ContainSubstring(wantErr))
					} else {
						Expect(err).NotTo(HaveOccurred())
					}
				}
				if wantErr == "" && strategy == "" {
					Expect(fresh.Spec.RolloutStrategy.Type).To(Equal(workloadsv1alpha2.InPlaceUpdateStrategyType))
					Expect(set.Spec.RolloutStrategy.Type).To(Equal(workloadsv1alpha2.InPlaceUpdateStrategyType))
				}
			},
			Entry("default strategy allows positive rounding-to-zero percentage", workloadsv1alpha2.GroupUpdateStrategyType(""), intstr.FromString("1%"), intstr.FromString("0%"), ""),
			Entry("default strategy rejects surge", workloadsv1alpha2.GroupUpdateStrategyType(""), intstr.FromInt(1), intstr.FromInt(1), "maxSurge"),
			Entry("in-place rejects zero percent unavailable", workloadsv1alpha2.InPlaceUpdateStrategyType, intstr.FromString("0%"), intstr.FromInt(0), "maxUnavailable"),
			Entry("in-place rejects integer zero unavailable", workloadsv1alpha2.InPlaceUpdateStrategyType, intstr.FromInt(0), intstr.FromString("0%"), "maxUnavailable"),
			Entry("in-place rejects positive percentage surge", workloadsv1alpha2.InPlaceUpdateStrategyType, intstr.FromInt(1), intstr.FromString("1%"), "maxSurge"),
			Entry("recreate rejects mixed zero budgets", workloadsv1alpha2.RecreateStrategyType, intstr.FromInt(0), intstr.FromString("0%"), "maxUnavailable"),
			Entry("recreate rejects zero percent budgets", workloadsv1alpha2.RecreateStrategyType, intstr.FromString("0%"), intstr.FromString("0%"), "maxUnavailable"),
			Entry("recreate allows zero unavailable with surge", workloadsv1alpha2.RecreateStrategyType, intstr.FromInt(0), intstr.FromInt(1), ""),
		)

		It("enforces the CRD percentage ceiling through the API server", func() {
			set := &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "percentages", Namespace: testNs},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(3)),
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Spec: buildRBG("unused", testNs, workloadsv1alpha2.RecreatePodUpdateStrategyType).Spec,
					},
					RolloutStrategy: &workloadsv1alpha2.GroupSetRolloutStrategy{
						Type:           workloadsv1alpha2.RecreateStrategyType,
						Partition:      ptr.To(intstr.FromString("100%")),
						MaxUnavailable: ptr.To(intstr.FromString("100%")),
						MaxSurge:       ptr.To(intstr.FromString("100%")),
					},
				},
			}
			Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())
			Expect(set.Spec.RolloutStrategy.Partition.String()).To(Equal("100%"))

			fields := []*intstr.IntOrString{
				set.Spec.RolloutStrategy.Partition,
				set.Spec.RolloutStrategy.MaxUnavailable,
				set.Spec.RolloutStrategy.MaxSurge,
			}
			for i, value := range []*intstr.IntOrString{
				ptr.To(intstr.FromString("101%")),
				ptr.To(intstr.FromInt(-1)),
			} {
				for _, field := range fields {
					*field = *value
				}
				Expect(testutil.K8sClient.Update(testutil.Ctx, set)).To(MatchError(ContainSubstring("spec.rolloutStrategy")), "update %d", i)
				for _, field := range fields {
					*field = intstr.FromString("100%")
				}
			}
			Expect(testutil.K8sClient.Update(testutil.Ctx, set)).To(Succeed())
		})

		It("validates switches by the target rollout strategy", func() {
			set := &workloadsv1alpha2.RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{Name: "switch", Namespace: testNs},
				Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(3)),
					GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
						Spec: buildRBG("unused", testNs, workloadsv1alpha2.RecreatePodUpdateStrategyType).Spec,
					},
					RolloutStrategy: &workloadsv1alpha2.GroupSetRolloutStrategy{
						Type: workloadsv1alpha2.RecreateStrategyType, MaxUnavailable: ptr.To(intstr.FromInt(1)), MaxSurge: ptr.To(intstr.FromInt(1)),
					},
				},
			}
			Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())
			set.Spec.RolloutStrategy.Type = workloadsv1alpha2.InPlaceUpdateStrategyType
			Expect(testutil.K8sClient.Update(testutil.Ctx, set)).To(MatchError(ContainSubstring("maxSurge")))
			set.Spec.RolloutStrategy.MaxSurge = ptr.To(intstr.FromString("0%"))
			Expect(testutil.K8sClient.Update(testutil.Ctx, set)).To(Succeed())
			set.Spec.RolloutStrategy.Type = workloadsv1alpha2.RecreateStrategyType
			set.Spec.RolloutStrategy.MaxUnavailable = ptr.To(intstr.FromString("0%"))
			Expect(testutil.K8sClient.Update(testutil.Ctx, set)).To(MatchError(ContainSubstring("maxUnavailable")))
			set.Spec.RolloutStrategy.MaxSurge = ptr.To(intstr.FromString("1%"))
			Expect(testutil.K8sClient.Update(testutil.Ctx, set)).To(Succeed())
		})

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
			// envtest installs the CRDs from config/crd/bases, whose conversion strategy
			// is None, so no conversion webhook runs and a v1alpha1 object is stored and
			// read back as-is. The v1alpha2 mutating rule below still matches this
			// request because the webhook config omits matchPolicy (default Equivalent)
			// and both versions are served by the same CRD -- so a v1alpha1 write with
			// the legacy spelling exercises the same admission heal a production
			// v1alpha1 write goes through. In production, with the conversion webhook,
			// a v1alpha1 read of the stored value round-trips to "Recreate" again; the
			// value this spec sees here (RecreatePod) is the envtest view of the healed
			// storage.
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

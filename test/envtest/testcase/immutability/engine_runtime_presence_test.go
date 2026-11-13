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

package immutability

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

// The D7 presence rule — targetContainers requires component, except under
// standalonePattern — is enforced here by the RBG/RBGSet validating webhooks,
// which this suite installs. It cannot ride CEL: a comprehension over the
// uncapped engineRuntimes list at the uncapped roles[].scope yields an O(n^2)
// cost estimate the apiserver's CEL budget rejects by more than 100x on every
// supported version — the KEP-531 cost-budget remedy.

var _ = Describe("engine runtime targetContainers presence rule", func() {
	bareTargetContainers := func() []workloadsv1alpha2.EngineRuntimeTargetContainer {
		return []workloadsv1alpha2.EngineRuntimeTargetContainer{{Name: "engine"}}
	}

	lwRBG := func(name string, rt workloadsv1alpha2.EngineRuntime) *workloadsv1alpha2.RoleBasedGroup {
		return &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNs},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{{
				Name:     "worker",
				Replicas: ptr.To(int32(1)),
				Pattern: workloadsv1alpha2.Pattern{LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
					Size: ptr.To(int32(1)),
					TemplateSource: workloadsv1alpha2.TemplateSource{
						Template: ptr.To(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
							Name:  "engine",
							Image: "nginx:latest",
						}}}}),
					},
				}},
				EngineRuntimes: []workloadsv1alpha2.EngineRuntime{rt},
			}}},
		}
	}

	standaloneRBG := func(name string, rt workloadsv1alpha2.EngineRuntime) *workloadsv1alpha2.RoleBasedGroup {
		return &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNs},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{{
				Name:     "worker",
				Replicas: ptr.To(int32(1)),
				Pattern: workloadsv1alpha2.Pattern{StandalonePattern: &workloadsv1alpha2.StandalonePattern{
					TemplateSource: workloadsv1alpha2.TemplateSource{
						Template: ptr.To(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
							Name:  "engine",
							Image: "nginx:latest",
						}}}}),
					},
				}},
				EngineRuntimes: []workloadsv1alpha2.EngineRuntime{rt},
			}}},
		}
	}

	It("rejects bare targetContainers under leaderWorkerPattern", func() {
		err := testutil.K8sClient.Create(testutil.Ctx, lwRBG("presence-lw", workloadsv1alpha2.EngineRuntime{
			ProfileName:      "p",
			TargetContainers: bareTargetContainers(),
		}))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("targetContainers requires component to be set"))
	})

	It("rejects bare targetContainers under customComponentsPattern", func() {
		rbg := standaloneRBG("presence-custom-template", workloadsv1alpha2.EngineRuntime{ProfileName: "p"})
		rbg.Spec.Roles[0].Pattern = workloadsv1alpha2.Pattern{CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
			Components: []workloadsv1alpha2.InstanceComponent{{
				Name: "comp-a",
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "engine",
					Image: "nginx:latest",
				}}}},
			}},
		}}
		rbg.Spec.Roles[0].EngineRuntimes = []workloadsv1alpha2.EngineRuntime{{
			ProfileName:      "p",
			TargetContainers: bareTargetContainers(),
		}}

		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("targetContainers requires component to be set"))
	})

	It("accepts bare targetContainers under standalonePattern (the exemption)", func() {
		Expect(testutil.K8sClient.Create(testutil.Ctx, standaloneRBG("presence-standalone", workloadsv1alpha2.EngineRuntime{
			ProfileName:      "p",
			TargetContainers: bareTargetContainers(),
		}))).To(Succeed())
	})

	It("accepts targetContainers with component under leaderWorkerPattern", func() {
		Expect(testutil.K8sClient.Create(testutil.Ctx, lwRBG("presence-lw-ok", workloadsv1alpha2.EngineRuntime{
			ProfileName: "p", Component: "worker",
			TargetContainers: bareTargetContainers(),
		}))).To(Succeed())
	})

	It("rejects bare targetContainers in an RBGSet group template under leaderWorkerPattern", func() {
		rbgs := &workloadsv1alpha2.RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "presence-set", Namespace: testNs},
			Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(0)),
				GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{{
						Name:     "worker",
						Replicas: ptr.To(int32(1)),
						Pattern: workloadsv1alpha2.Pattern{LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
							Size: ptr.To(int32(1)),
							TemplateSource: workloadsv1alpha2.TemplateSource{
								Template: ptr.To(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
									Name:  "engine",
									Image: "nginx:latest",
								}}}}),
							},
						}},
						EngineRuntimes: []workloadsv1alpha2.EngineRuntime{{
							ProfileName:      "p",
							TargetContainers: bareTargetContainers(),
						}},
					}},
				}},
			},
		}

		err := testutil.K8sClient.Create(testutil.Ctx, rbgs)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("targetContainers requires component to be set"))
	})

	Context("v1alpha1 writes through Equivalent matchPolicy", func() {
		// The webhook config registers v1alpha2 rules with the default
		// matchPolicy=Equivalent, so a v1alpha1 write is admitted through the
		// same validator with the object converted to the v1alpha2 shape —
		// where the conversion's pattern inference decides the exemption.

		v1a1RBG := func(name string, rt workloadsv1alpha1.EngineRuntime, pattern func() workloadsv1alpha1.RoleSpec) *workloadsv1alpha1.RoleBasedGroup {
			role := pattern()
			role.EngineRuntimes = []workloadsv1alpha1.EngineRuntime{rt}
			return &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNs},
				Spec:       workloadsv1alpha1.RoleBasedGroupSpec{Roles: []workloadsv1alpha1.RoleSpec{role}},
			}
		}

		bareV1a1 := func() []workloadsv1alpha1.EngineRuntimeTargetContainer {
			return []workloadsv1alpha1.EngineRuntimeTargetContainer{{Name: "engine"}}
		}

		template := func() workloadsv1alpha1.RoleSpec {
			return workloadsv1alpha1.RoleSpec{
				Name:     "worker",
				Replicas: ptr.To(int32(1)),
				// Workload is required-with-default; server-side defaulting
				// does not fill it before validation, so the fixture sets it.
				// RoleInstanceSet is the explicit opt-in to the non-deprecated
				// render path — with the schema default (StatefulSet) the
				// fourth invariant correctly rejects the new fields there.
				Workload: workloadsv1alpha1.WorkloadSpec{APIVersion: "workloads.x-k8s.io/v1alpha2", Kind: "RoleInstanceSet"},
				TemplateSource: workloadsv1alpha1.TemplateSource{
					Template: ptr.To(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name:  "engine",
						Image: "nginx:latest",
					}}}}),
				},
			}
		}

		It("accepts the pattern-unset shape (conversion infers standalone)", func() {
			Expect(testutil.K8sClient.Create(testutil.Ctx, v1a1RBG(
				"presence-v1a1-standalone",
				workloadsv1alpha1.EngineRuntime{ProfileName: "p", TargetContainers: bareV1a1()},
				template,
			))).To(Succeed())
		})

		It("accepts the empty components edge (a present-but-empty list is pattern-unset)", func() {
			role := template()
			role.Components = []workloadsv1alpha1.InstanceComponent{}
			Expect(testutil.K8sClient.Create(testutil.Ctx, v1a1RBG(
				"presence-v1a1-empty-components",
				workloadsv1alpha1.EngineRuntime{ProfileName: "p", TargetContainers: bareV1a1()},
				func() workloadsv1alpha1.RoleSpec { return role },
			))).To(Succeed())
		})

		It("rejects the leaderWorkerSet shape", func() {
			role := template()
			// The role's own TemplateSource carries the worker template;
			// the LWS template only contributes size here.
			role.LeaderWorkerSet = &workloadsv1alpha1.LeaderWorkerTemplate{
				Size: ptr.To(int32(1)),
			}
			err := testutil.K8sClient.Create(testutil.Ctx, v1a1RBG(
				"presence-v1a1-lws",
				workloadsv1alpha1.EngineRuntime{ProfileName: "p", TargetContainers: bareV1a1()},
				func() workloadsv1alpha1.RoleSpec { return role },
			))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("targetContainers requires component to be set"))
		})
	})
})

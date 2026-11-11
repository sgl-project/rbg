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

package v1a1gate

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

func buildV1a1RBG(name string) *workloadsv1alpha1.RoleBasedGroup {
	return &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gateNamespace},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha1.RoleSpec{{
				Name:     "worker",
				Replicas: ptr.To(int32(1)),
				Workload: workloadsv1alpha1.WorkloadSpec{
					APIVersion: "workloads.x-k8s.io/v1alpha2",
					Kind:       "RoleInstanceSet",
				},
				TemplateSource: workloadsv1alpha1.TemplateSource{
					Template: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "nginx", Image: "nginx:latest"}},
						},
					},
				},
			}},
		},
	}
}

var _ = Describe("v1alpha1 request path through the conversion chain", Ordered, func() {

	It("rejects a new deprecated use submitted via v1alpha1 (conversion -> v1alpha2 gate)", func() {
		rbg := buildV1a1RBG("gate-reject")
		// Explicitly name a deprecated workload type; the v1alpha2 gate
		// (reached through conversion with matchPolicy: Equivalent) rejects it.
		rbg.Spec.Roles[0].Workload = workloadsv1alpha1.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		}
		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new uses are not accepted"))
	})

	It("accepts a v1alpha1 create with explicit RoleInstanceSet", func() {
		rbg := buildV1a1RBG("gate-accept")
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
	})

	It("rejects a v1alpha1 RBGSet whose template uses a deprecated type", func() {
		set := &workloadsv1alpha1.RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "gate-set-reject", Namespace: gateNamespace},
			Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				Template: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{{
						Name:     "worker",
						Replicas: ptr.To(int32(1)),
						Workload: workloadsv1alpha1.WorkloadSpec{
							APIVersion: "apps/v1",
							Kind:       "StatefulSet",
						},
						TemplateSource: workloadsv1alpha1.TemplateSource{
							Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "nginx", Image: "nginx:latest"}},
								},
							},
						},
					}},
				},
			},
		}
		err := testutil.K8sClient.Create(testutil.Ctx, set)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new uses are not accepted"))
	})

	It("leaves v1alpha1 deletes ungated", func() {
		rbg := buildV1a1RBG("gate-delete")
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
		Expect(testutil.K8sClient.Delete(testutil.Ctx, rbg)).To(Succeed())
	})
})

const gateNamespace = "v1a1-gate-test"

var _ = Describe("v1alpha1 wire contract", Ordered, func() {

	BeforeEach(func() {
		testutil.GateWarnings.Reset()
	})

	It("returns exactly one v1alpha1 deprecation warning carrying the 0.10.0 timeline", func() {
		rbg := buildV1a1RBG("warn-single")
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

		warnings := testutil.GateWarnings.All()
		Expect(warnings).To(HaveLen(1),
			"v1alpha1 write must return exactly one warning (the CRD-level deprecationWarning), not %d", len(warnings))
		Expect(warnings[0]).To(ContainSubstring("v1alpha1"))
		Expect(warnings[0]).To(ContainSubstring("deprecated"))
		Expect(warnings[0]).To(ContainSubstring("0.10.0"))
	})

	It("does not warn on v1alpha2 writes", func() {
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "warn-v1a2-none", Namespace: gateNamespace},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					Pattern: workloadsv1alpha2.Pattern{
						StandalonePattern: &workloadsv1alpha2.StandalonePattern{
							TemplateSource: workloadsv1alpha2.TemplateSource{},
						},
					},
				}},
			},
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
		Expect(testutil.GateWarnings.All()).To(BeEmpty(),
			"a v1alpha2 write must not carry a v1alpha1 deprecation warning")
	})

	It("rejects a v1alpha1 create with a present-but-empty workload", func() {
		// A Go zero-value WorkloadSpec marshals as
		// workload:{"apiVersion":"","kind":""} — the key is present with
		// empty values, not omitted. The CRD schema requires apiVersion
		// to match an apiVersion pattern, so schema validation rejects
		// the empty string before any admission webhook. A genuinely
		// absent key (kubectl-style YAML) would trigger the CRD top-level
		// default of apps/v1 StatefulSet, which the v1alpha2 gate then
		// rejects as a new use; that path is not covered in-repo.
		rbg := &workloadsv1alpha1.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "warn-defaulted", Namespace: gateNamespace},
			Spec: workloadsv1alpha1.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha1.RoleSpec{{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					// Zero-value workload: present with empty apiVersion
					// and kind; schema validation catches the pattern
					// mismatch.
					Workload: workloadsv1alpha1.WorkloadSpec{},
					TemplateSource: workloadsv1alpha1.TemplateSource{
						Template: &corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "nginx", Image: "nginx:latest"}},
							},
						},
					},
				}},
			},
		}
		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("workload.apiVersion"))
	})

	// v1alpha1-path grandfathering (same-type update through the conversion
	// chain) and type-exact switch (StatefulSet -> Deployment) have no in-repo
	// coverage: the v1alpha2 deprecation suite covers them only through the
	// v1alpha2 API, not via v1alpha1 conversion. In this suite the gate is
	// already active, so the mutating webhook defaults every create to
	// RoleInstanceSet; testing grandfathering would require the two-phase
	// (seed-then-gate) setup, which belongs in the deprecation suite's
	// architecture. Track as a follow-up before 0.10.0 if needed.
})

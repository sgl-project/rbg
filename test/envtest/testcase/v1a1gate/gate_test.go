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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

	It("rejects a v1alpha1 create whose role omits the workload key", func() {
		// The CRD schema defaults an absent workload key to apps/v1
		// StatefulSet; conversion then hands the deprecated type to the
		// v1alpha2 gate. The typed Go client cannot produce this request —
		// a zero WorkloadSpec marshals "workload":{"apiVersion":"","kind":""}
		// — so submit an unstructured object with the key omitted, the way
		// kubectl-style YAML does.
		obj := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "workloads.x-k8s.io/v1alpha1",
			"kind":       "RoleBasedGroup",
			"metadata": map[string]interface{}{
				"name":      "gate-absent-key",
				"namespace": gateNamespace,
			},
			"spec": map[string]interface{}{
				"roles": []interface{}{map[string]interface{}{
					"name":     "worker",
					"replicas": int64(1),
					"template": map[string]interface{}{
						"spec": map[string]interface{}{
							"containers": []interface{}{map[string]interface{}{
								"name":  "nginx",
								"image": "nginx:latest",
							}},
						},
					},
				}},
			},
		}}
		obj.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "workloads.x-k8s.io",
			Version: "v1alpha1",
			Kind:    "RoleBasedGroup",
		})
		err := testutil.K8sClient.Create(testutil.Ctx, obj)
		Expect(err).To(HaveOccurred())
		// Pin the actual default, not just any deprecated type: the CRD
		// schema defaults the absent workload key to apps/v1 StatefulSet.
		Expect(err.Error()).To(ContainSubstring(
			`workload type "apps/v1/StatefulSet"`))
		Expect(err.Error()).To(ContainSubstring("new uses are not accepted"))
	})

	It("leaves v1alpha1 deletes ungated", func() {
		rbg := buildV1a1RBG("gate-delete")
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
		Expect(testutil.K8sClient.Delete(testutil.Ctx, rbg)).To(Succeed())
	})
})

const gateNamespace = "v1a1-gate-test"

var _ = Describe("v1alpha1 grandfathering through conversion", Ordered, func() {
	const seededName = "gate-grandfather"

	BeforeAll(func() {
		// Seed an object whose role predates the gate: with the gate
		// offline, create it through v1alpha1 with the deprecated
		// apps/v1 StatefulSet type, then bring the gate back. Registering
		// the restore defer before disabling guarantees the gate is
		// re-enabled even if the seed fails.
		defer testutil.EnableV1a2Gate()
		testutil.DisableV1a2Gate()

		rbg := buildV1a1RBG(seededName)
		rbg.Spec.Roles[0].Workload = workloadsv1alpha1.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
	})

	getSeeded := func() *workloadsv1alpha1.RoleBasedGroup {
		fresh := &workloadsv1alpha1.RoleBasedGroup{}
		ExpectWithOffset(1, testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKey{Namespace: gateNamespace, Name: seededName}, fresh)).To(Succeed())
		return fresh
	}

	It("accepts a same-type v1alpha1 update of the pre-gate StatefulSet role", func() {
		// The admission request converts both the new and the old object
		// to v1alpha2; the type-exact exemption keeps a pre-gate
		// deprecated role writable through that chain.
		fresh := getSeeded()
		*fresh.Spec.Roles[0].Replicas = 2
		Expect(testutil.K8sClient.Update(testutil.Ctx, fresh)).To(Succeed())
	})

	It("rejects a v1alpha1 switch from StatefulSet to Deployment", func() {
		fresh := getSeeded()
		fresh.Spec.Roles[0].Workload.Kind = "Deployment"
		err := testutil.K8sClient.Update(testutil.Ctx, fresh)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new uses are not accepted"))
	})

	It("rejects a role rename through a v1alpha1 update", func() {
		// The type-exact exemption is keyed on role name and workload type:
		// a renamed role is a new use even with the same deprecated type.
		fresh := getSeeded()
		fresh.Spec.Roles[0].Name = "worker-renamed"
		err := testutil.K8sClient.Update(testutil.Ctx, fresh)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new uses are not accepted"))
	})

	It("rejects adding a new deprecated role through a v1alpha1 update", func() {
		fresh := getSeeded()
		newRole := buildV1a1RBG(seededName).Spec.Roles[0]
		newRole.Name = "worker-extra"
		newRole.Workload = workloadsv1alpha1.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		}
		fresh.Spec.Roles = append(fresh.Spec.Roles, newRole)
		err := testutil.K8sClient.Update(testutil.Ctx, fresh)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new uses are not accepted"))
	})
})

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
		// the empty string before any admission webhook. The genuinely
		// absent-key path (kubectl-style YAML, defaulted to apps/v1
		// StatefulSet and rejected by the gate) is covered above.
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
		// A schema rejection is an Invalid (422) error; a webhook denial
		// would surface as Forbidden (403). The field path pins the
		// rejection to the empty workload.apiVersion specifically, so an
		// unrelated validation error cannot satisfy this spec.
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("spec.roles[0].workload.apiVersion"))
	})

	// v1alpha1-path grandfathering, type switching, and key-absent workload
	// defaulting are covered by the sibling Describes above (see #579). The
	// v1alpha2 validator unit tests cover the analogous type-exact
	// grandfathering and switch semantics; key-absent defaulting is specific
	// to the v1alpha1 CRD schema.
})

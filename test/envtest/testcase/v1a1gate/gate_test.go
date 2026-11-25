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
	"io"
	"os"
	"path/filepath"

	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/rbgs/api/workloads/constants"
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

func readerString(reader io.Reader) string {
	data, err := io.ReadAll(reader)
	Expect(err).NotTo(HaveOccurred())
	return string(data)
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

var _ = Describe("v1alpha1 role shape validation through the CEL rule", Ordered, func() {
	It("rejects a RoleInstanceSet role with no template, templateRef, components, or leaderWorkerSet", func() {
		// The RoleInstanceSet exemption exists so customComponentsPattern
		// roles (no template, but components) survive the round trip; a
		// role with neither a template source nor a pattern must not be
		// admitted, because it converts to a standalone role with no
		// template source that can never reconcile.
		rbg := buildV1a1RBG("gate-empty-ris")
		rbg.Spec.Roles[0].TemplateSource = workloadsv1alpha1.TemplateSource{}
		rbg.Spec.Roles[0].Workload = workloadsv1alpha1.WorkloadSpec{
			APIVersion: "workloads.x-k8s.io/v1alpha2",
			Kind:       "RoleInstanceSet",
		}
		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})
})

var _ = Describe("v1alpha1 role provenance marker validation", Ordered, func() {
	It("rejects malformed markers through the real API", func() {
		rbg := buildV1a1RBG("gate-malformed-restart-marker")
		rbg.Annotations = map[string]string{
			constants.ConversionV2RoleProvenanceAnnotationKey: "not-json",
		}

		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unmarshalling preserved role provenance markers"))
	})

	It("rejects an edit that introduces an invalid restart config through the marker", func() {
		rbg := buildV1a1RBG("gate-edited-marker")
		rbg.Spec.Roles[0].LeaderWorkerSet = &workloadsv1alpha1.LeaderWorkerTemplate{
			Size: ptr.To(int32(2)),
		}
		rbg.Annotations = map[string]string{
			constants.ConversionV2RoleProvenanceAnnotationKey: `{"worker":{"config":{"type":"RecreateRoleInstanceOnPodRestart","activateAfter":"Created","baseDelaySeconds":10,"maxDelaySeconds":300}}}`,
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

		live := &workloadsv1alpha1.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(rbg), live)).To(Succeed())
		live.Annotations[constants.ConversionV2RoleProvenanceAnnotationKey] =
			`{"worker":{"config":{"type":"RecreateRoleInstanceOnPodRestart","activateAfter":"Created","baseDelaySeconds":-5,"maxDelaySeconds":-10}}}`

		err := testutil.K8sClient.Update(testutil.Ctx, live)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("restartPolicyConfig.baseDelaySeconds -5 is negative"))
	})

	It("rejects an invalid legacy restart policy masked by a valid config type", func() {
		rbg := buildV1a1RBG("gate-masked-legacy-marker")
		rbg.Spec.Roles[0].LeaderWorkerSet = &workloadsv1alpha1.LeaderWorkerTemplate{
			Size: ptr.To(int32(2)),
		}
		rbg.Spec.Roles[0].RestartPolicy = workloadsv1alpha1.RecreateRoleInstanceOnPodRestart
		rbg.Annotations = map[string]string{
			constants.ConversionV2RoleProvenanceAnnotationKey: `{"worker":{"config":{"type":"RecreateRoleInstanceOnPodRestart"},"legacyType":"Bogus"}}`,
		}

		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`restartPolicy "Bogus" is not a supported restart policy`))
	})

	It("rejects LeaderOnly with an explicitly empty workload annotation restored by the marker", func() {
		rbg := buildV1a1RBG("gate-leader-only-empty-workload")
		rbg.Spec.Roles[0].LeaderWorkerSet = &workloadsv1alpha1.LeaderWorkerTemplate{
			Size: ptr.To(int32(2)),
		}
		rbg.Spec.Roles[0].RestartPolicy = workloadsv1alpha1.RecreateRoleInstanceOnPodRestart
		rbg.Annotations = map[string]string{
			constants.ConversionV2RoleProvenanceAnnotationKey: `{"worker":{"workloadType":"","sharedServiceSelection":"LeaderOnly"}}`,
		}

		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(
			"sharedServiceSelection LeaderOnly is only supported for RoleInstanceSet workloads"))
	})
})

var _ = Describe("v1alpha1 no-op round trip", Ordered, func() {
	It("leaves a stored v1alpha2 RoleBasedGroup unchanged", func() {
		// A customComponentsPattern role has neither template nor
		// templateRef: without the RoleInstanceSet exemption in the
		// v1alpha1 CEL rule, its round trip is rejected outright; with it,
		// the write-back must leave the stored v1alpha2 object untouched.
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "roundtrip-components", Namespace: gateNamespace},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "engine",
					Replicas: ptr.To(int32(1)),
					Pattern: workloadsv1alpha2.Pattern{CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
						Components: []workloadsv1alpha2.InstanceComponent{{
							Name: "main", Size: ptr.To(int32(1)),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "nginx:latest"}}},
							},
						}},
					}},
				}},
			},
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

		stored := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(rbg), stored)).To(Succeed())
		storedSpec := stored.Spec.DeepCopy()

		v1 := &workloadsv1alpha1.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(rbg), v1)).To(Succeed())
		Expect(v1.Spec.Roles[0].Components).To(HaveLen(1))
		Expect(testutil.K8sClient.Update(testutil.Ctx, v1)).To(Succeed())

		after := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(rbg), after)).To(Succeed())
		Expect(after.Spec).To(Equal(*storedSpec),
			"a no-op v1alpha1 read-and-write must not rewrite the stored v1alpha2 object")
		Expect(after.Annotations).NotTo(HaveKey(constants.ConversionV2RoleProvenanceAnnotationKey),
			"the conversion marker is wire-only for same-version conversion and must not persist on the stored RBG")
	})

	It("preserves non-default restart settings through the write-back", func() {
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "roundtrip-restart", Namespace: gateNamespace},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "engine",
					Replicas: ptr.To(int32(1)),
					Pattern: workloadsv1alpha2.Pattern{LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
						Size: ptr.To(int32(2)),
						RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
							Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
							ActivateAfter:    workloadsv1alpha2.RestartPolicyActivateAfterCreated,
							BaseDelaySeconds: ptr.To(int32(10)),
							MaxDelaySeconds:  ptr.To(int32(300)),
						},
						TemplateSource: workloadsv1alpha2.TemplateSource{Template: &corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "nginx:latest"}}},
						}},
					}},
				}},
			},
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

		v1 := &workloadsv1alpha1.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(rbg), v1)).To(Succeed())
		Expect(v1.Spec.Roles[0].RestartPolicy).To(Equal(workloadsv1alpha1.RecreateRoleInstanceOnPodRestart))
		Expect(testutil.K8sClient.Update(testutil.Ctx, v1)).To(Succeed())

		after := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(rbg), after)).To(Succeed())
		config := after.Spec.Roles[0].LeaderWorkerPattern.RestartPolicyConfig
		Expect(config).NotTo(BeNil())
		Expect(config.ActivateAfter).To(Equal(workloadsv1alpha2.RestartPolicyActivateAfterCreated))
		Expect(*config.BaseDelaySeconds).To(Equal(int32(10)))
		Expect(*config.MaxDelaySeconds).To(Equal(int32(300)))
	})

	It("leaves an object stored by an earlier fresh v1alpha1 write unchanged when its manifest is written again", func() {
		// `kubectl apply` and `kubectl replace -f` with a fresh manifest send its
		// roles without the object-level round-trip marker. Such a write must convert exactly as
		// v0.8.0 through v0.9.0 stored the object, or re-applying an unchanged
		// manifest rewrites it and rolls its roles.
		tmpl := func() *corev1.PodTemplateSpec {
			return &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "nginx:latest"}}},
			}
		}
		// The shape those releases stored for the manifest below.
		stored := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "rewrite-manifest", Namespace: gateNamespace},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "engine",
					Replicas: ptr.To(int32(1)),
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: constants.RoleInstanceSetWorkloadType,
					},
					Pattern: workloadsv1alpha2.Pattern{LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
						Size:                ptr.To(int32(2)),
						RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{},
						TemplateSource:      workloadsv1alpha2.TemplateSource{Template: tmpl()},
					}},
				}},
			},
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, stored)).To(Succeed())
		before := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(stored), before)).To(Succeed())

		live := &workloadsv1alpha1.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(stored), live)).To(Succeed())
		manifest := &workloadsv1alpha1.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name: live.Name, Namespace: live.Namespace, ResourceVersion: live.ResourceVersion,
			},
			Spec: workloadsv1alpha1.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha1.RoleSpec{{
					Name:     "engine",
					Replicas: ptr.To(int32(1)),
					Workload: workloadsv1alpha1.WorkloadSpec{
						APIVersion: "workloads.x-k8s.io/v1alpha2",
						Kind:       "RoleInstanceSet",
					},
					LeaderWorkerSet: &workloadsv1alpha1.LeaderWorkerTemplate{Size: ptr.To(int32(2))},
					TemplateSource:  workloadsv1alpha1.TemplateSource{Template: tmpl()},
				}},
			},
		}
		Expect(testutil.K8sClient.Update(testutil.Ctx, manifest)).To(Succeed())

		after := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(stored), after)).To(Succeed())
		Expect(after.Spec).To(Equal(before.Spec),
			"a fresh v1alpha1 write of an unchanged manifest must not rewrite the stored object")
	})

	It("leaves a stored object unchanged when kubectl client-side apply runs twice", func() {
		stored := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "client-side-apply", Namespace: gateNamespace},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "engine",
					Replicas: ptr.To(int32(1)),
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: constants.RoleInstanceSetWorkloadType,
					},
					Pattern: workloadsv1alpha2.Pattern{LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
						Size: ptr.To(int32(2)),
						RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
							BaseDelaySeconds: ptr.To(int32(30)),
							MaxDelaySeconds:  ptr.To(int32(600)),
						},
						TemplateSource: workloadsv1alpha2.TemplateSource{Template: &corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "nginx:latest"}}},
						}},
					}},
				}},
			},
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, stored)).To(Succeed())

		before := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(stored), before)).To(Succeed())

		manifest := `apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: client-side-apply
  namespace: ` + gateNamespace + `
spec:
  roles:
    - name: engine
      replicas: 1
      workload:
        apiVersion: workloads.x-k8s.io/v1alpha2
        kind: RoleInstanceSet
      leaderWorkerSet:
        size: 2
      template:
        spec:
          containers:
            - name: main
              image: nginx:latest
`
		path := filepath.Join(GinkgoT().TempDir(), "rolebasedgroup.yaml")
		Expect(os.WriteFile(path, []byte(manifest), 0o600)).To(Succeed())

		user, err := testutil.TestEnv.AddUser(envtest.User{
			Name:   "gate-kubectl",
			Groups: []string{"system:masters"},
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		kubectl, err := user.Kubectl()
		Expect(err).NotTo(HaveOccurred())
		for i := 0; i < 2; i++ {
			_, stderr, runErr := kubectl.Run("apply", "-f", path)
			Expect(runErr).NotTo(HaveOccurred(), "apply attempt %d: %s", i+1, readerString(stderr))
		}

		after := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(stored), after)).To(Succeed())
		Expect(after.Spec).To(Equal(before.Spec),
			"an unchanged manifest applied by client-side kubectl apply must not rewrite the stored object")
	})

	It("leaves a stored object unchanged when kubectl server-side apply runs twice", func() {
		stored := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "server-side-apply", Namespace: gateNamespace},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "engine",
					Replicas: ptr.To(int32(1)),
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: constants.RoleInstanceSetWorkloadType,
					},
					Pattern: workloadsv1alpha2.Pattern{LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
						Size: ptr.To(int32(2)),
						RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
							BaseDelaySeconds: ptr.To(int32(30)),
							MaxDelaySeconds:  ptr.To(int32(600)),
						},
						TemplateSource: workloadsv1alpha2.TemplateSource{Template: &corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "nginx:latest"}}},
						}},
					}},
				}},
			},
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, stored)).To(Succeed())

		before := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(stored), before)).To(Succeed())

		manifest := `apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: server-side-apply
  namespace: ` + gateNamespace + `
spec:
  roles:
    - name: engine
      replicas: 1
      workload:
        apiVersion: workloads.x-k8s.io/v1alpha2
        kind: RoleInstanceSet
      leaderWorkerSet:
        size: 2
      template:
        spec:
          containers:
            - name: main
              image: nginx:latest
`
		path := filepath.Join(GinkgoT().TempDir(), "rolebasedgroup.yaml")
		Expect(os.WriteFile(path, []byte(manifest), 0o600)).To(Succeed())

		user, err := testutil.TestEnv.AddUser(envtest.User{
			Name:   "gate-kubectl-server-side",
			Groups: []string{"system:masters"},
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		kubectl, err := user.Kubectl()
		Expect(err).NotTo(HaveOccurred())
		for i := 0; i < 2; i++ {
			_, stderr, runErr := kubectl.Run("apply", "--server-side", "-f", path)
			Expect(runErr).NotTo(HaveOccurred(), "apply attempt %d: %s", i+1, readerString(stderr))
		}

		after := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(stored), after)).To(Succeed())
		Expect(after.Spec).To(Equal(before.Spec),
			"an unchanged manifest applied by server-side kubectl apply must not rewrite the stored object")
	})
})

var _ = Describe("v1alpha1 RoleBasedGroupSet round trip", Ordered, func() {
	const setName = "gate-set-roundtrip"

	buildStoredSet := func() *workloadsv1alpha2.RoleBasedGroupSet {
		return &workloadsv1alpha2.RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: setName, Namespace: gateNamespace},
			Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
					Labels: map[string]string{"team": "inference"},
					Annotations: map[string]string{
						"example.org/owner":                 "ai",
						constants.GroupExclusiveTopologyKey: "kubernetes.io/hostname",
					},
					Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{{
						Name:     "engine",
						Replicas: ptr.To(int32(1)),
						Pattern: workloadsv1alpha2.Pattern{CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
							Components: []workloadsv1alpha2.InstanceComponent{{
								Name: "main", Size: ptr.To(int32(1)),
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "nginx:latest"}}},
								},
							}},
							RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
								Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
								ActivateAfter:    workloadsv1alpha2.RestartPolicyActivateAfterCreated,
								BaseDelaySeconds: ptr.To(int32(10)),
								MaxDelaySeconds:  ptr.To(int32(300)),
							},
						}},
					}}},
				},
			},
		}
	}

	runSetApplyCase := func(name string, serverSide bool) {
		stored := &workloadsv1alpha2.RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gateNamespace},
			Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
					Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{{
						Name: "engine", Replicas: ptr.To(int32(1)),
						PodManagementPolicy: constants.OrderedReadyPodManagement,
						Annotations: map[string]string{
							constants.RoleWorkloadTypeAnnotationKey: constants.RoleInstanceSetWorkloadType,
						},
						ScalingAdapter: &workloadsv1alpha2.ScalingAdapter{
							Enable: false, Labels: map[string]string{"team": "inference"},
						},
						Pattern: workloadsv1alpha2.Pattern{LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
							Size:                   ptr.To(int32(2)),
							SharedServiceSelection: ptr.To(workloadsv1alpha2.SharedServiceSelectionAll),
							RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
								Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
								ActivateAfter:    workloadsv1alpha2.RestartPolicyActivateAfterCreated,
								BaseDelaySeconds: ptr.To(int32(10)),
								MaxDelaySeconds:  ptr.To(int32(300)),
							},
							TemplateSource: workloadsv1alpha2.TemplateSource{Template: &corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "nginx:latest"}}},
							}},
						}},
					}}},
				},
			},
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, stored)).To(Succeed())
		before := &workloadsv1alpha2.RoleBasedGroupSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(stored), before)).To(Succeed())

		manifest := `apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroupSet
metadata:
  name: ` + name + `
  namespace: ` + gateNamespace + `
spec:
  replicas: 1
  template:
    roles:
      - name: engine
        replicas: 1
        restartPolicy: RecreateRoleInstanceOnPodRestart
        workload:
          apiVersion: workloads.x-k8s.io/v1alpha2
          kind: RoleInstanceSet
        scalingAdapter: {}
        leaderWorkerSet:
          size: 2
        template:
          spec:
            containers:
              - name: main
                image: nginx:latest
`
		path := filepath.Join(GinkgoT().TempDir(), "rolebasedgroupset.yaml")
		Expect(os.WriteFile(path, []byte(manifest), 0o600)).To(Succeed())

		userName := "gate-set-kubectl"
		if serverSide {
			userName += "-server-side"
		}
		user, err := testutil.TestEnv.AddUser(envtest.User{
			Name:   userName,
			Groups: []string{"system:masters"},
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		kubectl, err := user.Kubectl()
		Expect(err).NotTo(HaveOccurred())
		args := []string{"apply"}
		if serverSide {
			args = append(args, "--server-side")
		}
		args = append(args, "-f", path)
		for i := 0; i < 2; i++ {
			_, stderr, runErr := kubectl.Run(args...)
			Expect(runErr).NotTo(HaveOccurred(), "apply attempt %d: %s", i+1, readerString(stderr))
		}

		after := &workloadsv1alpha2.RoleBasedGroupSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(stored), after)).To(Succeed())
		Expect(cmp.Diff(before.Spec, after.Spec)).To(BeEmpty(),
			"an unchanged set manifest applied by kubectl must not rewrite the stored set: %s", cmp.Diff(before.Spec, after.Spec))
		Expect(after.Annotations).NotTo(HaveKey(constants.ConversionV2RoleProvenanceAnnotationKey),
			"the conversion marker is wire-only for same-version conversion and must not persist on the stored RBGSet")
	}

	It("leaves a stored v1alpha2 RoleBasedGroupSet unchanged after a no-op v1alpha1 write-back", func() {
		set := buildStoredSet()
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())
		before := set.Spec.DeepCopy()

		v1 := &workloadsv1alpha1.RoleBasedGroupSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(set), v1)).To(Succeed())
		Expect(v1.Spec.Template.Roles[0].Components).To(HaveLen(1))
		Expect(testutil.K8sClient.Update(testutil.Ctx, v1)).To(Succeed())

		after := &workloadsv1alpha2.RoleBasedGroupSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(set), after)).To(Succeed())
		Expect(after.Spec).To(Equal(*before),
			"a no-op v1alpha1 read-and-write must not rewrite the stored set")
		Expect(after.Annotations).NotTo(HaveKey(constants.ConversionV2RoleProvenanceAnnotationKey),
			"the conversion marker is wire-only for same-version conversion and must not persist on the stored RBGSet")
	})

	It("rejects an invalid marker through the real RoleBasedGroupSet API", func() {
		set := &workloadsv1alpha1.RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gate-set-invalid-marker", Namespace: gateNamespace,
				Annotations: map[string]string{
					constants.ConversionV2RoleProvenanceAnnotationKey: `{"worker":{"config":{"type":"RecreateRoleInstanceOnPodRestart"},"legacyType":"Bogus"}}`,
				},
			},
			Spec: workloadsv1alpha1.RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				Template: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{{
						Name: "worker", Replicas: ptr.To(int32(1)),
						RestartPolicy: workloadsv1alpha1.RecreateRoleInstanceOnPodRestart,
						Workload: workloadsv1alpha1.WorkloadSpec{
							APIVersion: "workloads.x-k8s.io/v1alpha2",
							Kind:       "RoleInstanceSet",
						},
						Components: []workloadsv1alpha1.InstanceComponent{{
							Name: "rank", Size: ptr.To(int32(2)),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "rank", Image: "nginx:latest"}}},
							},
						}},
					}},
				},
			},
		}

		err := testutil.K8sClient.Create(testutil.Ctx, set)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`restartPolicy "Bogus" is not a supported restart policy`))
	})

	It("leaves a stored set unchanged when kubectl client-side apply runs twice", func() {
		runSetApplyCase("gate-set-client-side-apply", false)
	})

	It("leaves a stored set unchanged when kubectl server-side apply runs twice", func() {
		runSetApplyCase("gate-set-server-side-apply", true)
	})

	It("keeps an object stored by an earlier fresh v1alpha1 write unchanged when its manifest is written again", func() {
		set := buildStoredSet()
		set.Name = setName + "-fresh"
		// A fresh v1alpha1 manifest cannot express groupTemplate metadata.
		set.Spec.GroupTemplate.Labels = nil
		set.Spec.GroupTemplate.Annotations = nil
		// The shape a fresh v1alpha1 write stored on v0.8.0 through v0.9.0:
		// the role carries the workload annotation and a type-only
		// restartPolicyConfig whose delays the CRD defaulted (30/600). A
		// fresh write cannot express activateAfter or non-default delays.
		storedRole := &set.Spec.GroupTemplate.Spec.Roles[0]
		storedRole.Annotations = map[string]string{
			constants.RoleWorkloadTypeAnnotationKey: constants.RoleInstanceSetWorkloadType,
		}
		storedRole.CustomComponentsPattern.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{
			Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
			BaseDelaySeconds: ptr.To(int32(30)),
			MaxDelaySeconds:  ptr.To(int32(600)),
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())
		before := set.Spec.DeepCopy()

		live := &workloadsv1alpha1.RoleBasedGroupSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(set), live)).To(Succeed())
		// A fresh manifest has no object-level round-trip marker, exactly
		// like a `kubectl apply` or `kubectl replace -f` write.
		delete(live.Annotations, constants.ConversionV2RoleProvenanceAnnotationKey)
		Expect(testutil.K8sClient.Update(testutil.Ctx, live)).To(Succeed())

		after := &workloadsv1alpha2.RoleBasedGroupSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			client.ObjectKeyFromObject(set), after)).To(Succeed())
		Expect(after.Spec).To(Equal(*before),
			"a fresh v1alpha1 write of an unchanged manifest must not rewrite the stored set")
	})
})

var _ = Describe("v1alpha1 RoleInstanceSet exemption shape validation", Ordered, func() {
	It("rejects an empty components list claiming the RoleInstanceSet exemption", func() {
		// The typed client omits an empty components slice, and the CEL rule
		// requires a non-empty list; an unstructured create carries the empty
		// list explicitly and must be rejected the same way.
		rbg := buildV1a1RBG("gate-empty-components")
		rbg.Spec.Roles[0].TemplateSource = workloadsv1alpha1.TemplateSource{}
		rbg.Spec.Roles[0].Workload = workloadsv1alpha1.WorkloadSpec{
			APIVersion: "workloads.x-k8s.io/v1alpha2",
			Kind:       "RoleInstanceSet",
		}
		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())

		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "workloads.x-k8s.io/v1alpha1", "kind": "RoleBasedGroup",
			"metadata": map[string]interface{}{"name": "gate-empty-components-u", "namespace": gateNamespace},
			"spec": map[string]interface{}{"roles": []interface{}{map[string]interface{}{
				"name": "worker", "replicas": int64(1),
				"workload":   map[string]interface{}{"apiVersion": "workloads.x-k8s.io/v1alpha2", "kind": "RoleInstanceSet"},
				"components": []interface{}{},
			}}},
		}}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: "workloads.x-k8s.io", Version: "v1alpha1", Kind: "RoleBasedGroup"})
		uerr := testutil.K8sClient.Create(testutil.Ctx, u)
		Expect(uerr).To(HaveOccurred())
		Expect(apierrors.IsInvalid(uerr)).To(BeTrue())
	})

	It("rejects a leaderWorkerSet without a template claiming the exemption", func() {
		rbg := buildV1a1RBG("gate-bare-lws")
		rbg.Spec.Roles[0].TemplateSource = workloadsv1alpha1.TemplateSource{}
		rbg.Spec.Roles[0].LeaderWorkerSet = &workloadsv1alpha1.LeaderWorkerTemplate{Size: ptr.To(int32(2))}
		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	It("rejects a mixed components and leaderWorkerSet role claiming the exemption", func() {
		// Conversion prioritizes leaderWorkerSet, which would silently drop the
		// components and store a LeaderWorkerPattern with no template source.
		rbg := buildV1a1RBG("gate-mixed-components-lws")
		rbg.Spec.Roles[0].TemplateSource = workloadsv1alpha1.TemplateSource{}
		rbg.Spec.Roles[0].LeaderWorkerSet = &workloadsv1alpha1.LeaderWorkerTemplate{Size: ptr.To(int32(2))}
		rbg.Spec.Roles[0].Components = []workloadsv1alpha1.InstanceComponent{{
			Name: "rank", Size: ptr.To(int32(2)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "rank", Image: "nginx:latest"}}},
			},
		}}
		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	It("accepts a mixed components and leaderWorkerSet role when a template is set", func() {
		rbg := buildV1a1RBG("gate-mixed-components-lws-template")
		rbg.Spec.Roles[0].LeaderWorkerSet = &workloadsv1alpha1.LeaderWorkerTemplate{Size: ptr.To(int32(2))}
		rbg.Spec.Roles[0].Components = []workloadsv1alpha1.InstanceComponent{{
			Name: "rank", Size: ptr.To(int32(2)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "rank", Image: "nginx:latest"}}},
			},
		}}
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
	})

	It("accepts a pure components role for a RoleInstanceSet workload", func() {
		rbg := buildV1a1RBG("gate-components-role-instance-set")
		rbg.Spec.Roles[0].TemplateSource = workloadsv1alpha1.TemplateSource{}
		rbg.Spec.Roles[0].Components = []workloadsv1alpha1.InstanceComponent{{
			Name: "rank", Size: ptr.To(int32(2)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "rank", Image: "nginx:latest"}}},
			},
		}}
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
	})

	It("accepts components combined with a role-level template", func() {
		rbg := buildV1a1RBG("gate-components-template")
		rbg.Spec.Roles[0].Components = []workloadsv1alpha1.InstanceComponent{{
			Name: "rank", Size: ptr.To(int32(2)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "rank", Image: "nginx:latest"}}},
			},
		}}
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
	})

	It("accepts components combined with a role-level templateRef", func() {
		rbg := buildV1a1RBG("gate-components-template-ref")
		rbg.Spec.Roles[0].TemplateSource = workloadsv1alpha1.TemplateSource{
			TemplateRef: &workloadsv1alpha1.TemplateRef{Name: "base"},
		}
		rbg.Spec.Roles[0].TemplatePatch = runtime.RawExtension{Raw: []byte(`{}`)}
		rbg.Spec.Roles[0].Components = []workloadsv1alpha1.InstanceComponent{{
			Name: "rank", Size: ptr.To(int32(2)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "rank", Image: "nginx:latest"}}},
			},
		}}
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
	})

	It("accepts components combined with a templatePatch for a RoleInstanceSet workload", func() {
		rbg := buildV1a1RBG("gate-components-template-patch")
		rbg.Spec.Roles[0].TemplateSource = workloadsv1alpha1.TemplateSource{}
		rbg.Spec.Roles[0].Workload = workloadsv1alpha1.WorkloadSpec{
			APIVersion: "workloads.x-k8s.io/v1alpha2",
			Kind:       "RoleInstanceSet",
		}
		rbg.Spec.Roles[0].TemplatePatch = runtime.RawExtension{Raw: []byte(`{"spec":{"containers":[{"name":"engine"}]}}`)}
		rbg.Spec.Roles[0].Components = []workloadsv1alpha1.InstanceComponent{{
			Name: "worker", Size: ptr.To(int32(2)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "nginx:latest"}}},
			},
		}}

		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
	})
})

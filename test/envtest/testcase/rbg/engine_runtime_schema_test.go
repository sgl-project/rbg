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

package rbg

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("RoleBasedGroup engine runtime schema", func() {
	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-erts-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})
	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	makeSchemaRBG := func(name string, rt workloadsv1alpha2.EngineRuntime) *workloadsv1alpha2.RoleBasedGroup {
		return &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNs},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name: "worker",
					Pattern: workloadsv1alpha2.Pattern{
						StandalonePattern: &workloadsv1alpha2.StandalonePattern{
							TemplateSource: workloadsv1alpha2.TemplateSource{
								Template: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
									Name:  "engine",
									Image: "nginx:latest",
								}}}},
							},
						},
					},
					EngineRuntimes: []workloadsv1alpha2.EngineRuntime{rt},
				}},
			},
		}
	}

	makeSchemaRBGLW := func(name string, rt workloadsv1alpha2.EngineRuntime) *workloadsv1alpha2.RoleBasedGroup {
		return &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNs},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name: "worker",
					Pattern: workloadsv1alpha2.Pattern{
						LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
							Size: ptr.To(int32(1)),
							TemplateSource: workloadsv1alpha2.TemplateSource{
								Template: &corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
									Name:  "engine",
									Image: "nginx:latest",
								}}}},
							},
						},
					},
					EngineRuntimes: []workloadsv1alpha2.EngineRuntime{rt},
				}},
			},
		}
	}

	expectAccepted := func(name string, rt workloadsv1alpha2.EngineRuntime) {
		ExpectWithOffset(1, testutil.K8sClient.Create(testutil.Ctx, makeSchemaRBG(name, rt))).To(Succeed())
	}

	expectRejectedLW := func(name string, rt workloadsv1alpha2.EngineRuntime, substring string) {
		Expect(testutil.K8sClient.Create(testutil.Ctx, makeSchemaRBGLW(name, rt))).
			NotTo(Succeed(), "%s must be rejected at write time", name)
		// re-run to capture the error message
		err := testutil.K8sClient.Create(testutil.Ctx, makeSchemaRBGLW(name, rt))
		ExpectWithOffset(1, err).To(HaveOccurred())
		ExpectWithOffset(1, err.Error()).To(ContainSubstring(substring))
	}

	expectAcceptedLW := func(name string, rt workloadsv1alpha2.EngineRuntime) {
		ExpectWithOffset(1, testutil.K8sClient.Create(testutil.Ctx, makeSchemaRBGLW(name, rt))).To(Succeed())
	}

	It("accepts a component outside the DNS-label grammar (render-time vocabulary gate)", func() {
		// Component is deliberately unmarked beyond MinLength: the source
		// vocabulary it references (RoleSpec.Name, InstanceComponent.Name)
		// is unmarked too, so the render-time position gate resolves the
		// name, not the schema.
		expectAcceptedLW("odd-component", workloadsv1alpha2.EngineRuntime{
			ProfileName: "p", Component: "Bad_Name",
		})
	})

	It("accepts a legal component", func() {
		expectAcceptedLW("component-ok", workloadsv1alpha2.EngineRuntime{
			ProfileName: "p", Component: "worker",
		})
	})

	It("rejects a targetContainers name that is not a DNS label", func() {
		expectRejectedLW("bad-tc", workloadsv1alpha2.EngineRuntime{
			ProfileName: "p", Component: "worker",
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
				{Name: "Bad_Shape"},
			},
		}, "should match '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$'")
	})

	It("rejects duplicate targetContainers names (map list)", func() {
		expectRejectedLW("dup-tc", workloadsv1alpha2.EngineRuntime{
			ProfileName: "p", Component: "worker",
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
				{Name: "engine"}, {Name: "engine"},
			},
		}, "Duplicate value")
	})

	It("rejects more than 32 targetContainers entries", func() {
		entries := make([]workloadsv1alpha2.EngineRuntimeTargetContainer, 33)
		for i := range entries {
			entries[i] = workloadsv1alpha2.EngineRuntimeTargetContainer{Name: fmt.Sprintf("e%d", i)}
		}
		expectRejectedLW("33-tc", workloadsv1alpha2.EngineRuntime{
			ProfileName: "p", Component: "worker", TargetContainers: entries,
		}, "must have at most 32 items")
	})

	It("rejects a role-side extraPatches entry without jsonPatch", func() {
		expectRejectedLW("no-op", workloadsv1alpha2.EngineRuntime{
			ProfileName: "p", Component: "worker",
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{
				Name:         "sidecar",
				ExtraPatches: []workloadsv1alpha2.ContainerPatch{{}},
			}},
		}, "Required value")
	})

	It("rejects a role-side extraPatches path outside the allowlist", func() {
		expectRejectedLW("bad-path", workloadsv1alpha2.EngineRuntime{
			ProfileName: "p", Component: "worker",
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{
				Name: "sidecar",
				ExtraPatches: []workloadsv1alpha2.ContainerPatch{{
					JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{Op: "add", Path: "/image",
						Value: runtime.RawExtension{Raw: []byte(`"x"`)}},
				}},
			}},
		}, "path must be an element pointer under /command, /args, /env, /volumeMounts or /securityContext/capabilities/add")
	})

	It("D7: accepts targetContainers with component (scenario 3)", func() {
		expectAcceptedLW("d7-ok", workloadsv1alpha2.EngineRuntime{
			ProfileName: "p", Component: "worker",
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{
				Name: "sidecar",
				ExtraPatches: []workloadsv1alpha2.ContainerPatch{{
					JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{Op: "add", Path: "/env/-",
						Value: runtime.RawExtension{Raw: []byte(`{"name":"A","value":"b"}`)}},
				}},
			}},
		})
	})

	// The D7 presence rule (targetContainers requires component, except
	// under standalonePattern) is webhook-enforced, not CEL — the apiserver's
	// CEL cost budget rejects a comprehension over the uncapped
	// engineRuntimes list by more than 100x (KEP-531 cost-budget remedy).
	// The write-time rejection matrix lives in the immutability suite,
	// which installs the validating webhooks; render-time rejection is
	// pinned in pkg/discovery resolver tests.
	It("D7: the schema accepts the standalone exemption shape (component-less targetContainers under standalonePattern)", func() {
		expectAccepted("d7-standalone", workloadsv1alpha2.EngineRuntime{
			ProfileName: "p",
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
				{Name: "engine"},
			},
		})
	})

	It("scenario 1: accepts a profileName-only reference", func() {
		expectAccepted("compat-only", workloadsv1alpha2.EngineRuntime{ProfileName: "p"})
	})

	It("injectContainers stays accepted with arbitrary values (inert field)", func() {
		expectAccepted("inert-ok", workloadsv1alpha2.EngineRuntime{
			ProfileName: "p", InjectContainers: []string{"whatever/shape", "Bad_Shape"},
		})
	})
})

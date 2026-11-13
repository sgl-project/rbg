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
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/discovery"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

var _ = Describe("RoleBasedGroup container patch injection", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-patch-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})
	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	// addOp builds an `add` jsonPatch arm, mirroring how the apiserver
	// delivers the authored YAML JSON inside RawExtension.
	addOp := func(path string, value any) workloadsv1alpha2.ContainerPatch {
		raw, err := json.Marshal(value)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		return workloadsv1alpha2.ContainerPatch{
			JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{
				Op: "add", Path: path, Value: runtime.RawExtension{Raw: raw},
			},
		}
	}

	makeProfile := func(name string, patches []workloadsv1alpha2.ContainerPatch) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
		return &workloadsv1alpha2.ClusterEngineRuntimeProfile{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
				// updateStrategy carries no omitempty: leaving the zero value
				// marshals spec.updateStrategy as "" and the apiserver enum
				// validation rejects the profile, so the fixture sets it.
				UpdateStrategy: "NoUpdate",
				Volumes: []corev1.Volume{{
					Name: name + "-vol",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				}},
				ContainerPatches: patches,
			},
		}
	}

	// makeRBG builds a v1alpha2 RBG whose worker role is a Deployment
	// workload. The storage version is v1alpha2 and this suite wires no
	// conversion webhooks, so a v1alpha1 RBG would lose its role pattern in
	// the apiserver's structural version conversion ("no valid pattern found").
	// The role-workload-type annotation below is exactly the one the v1alpha1
	// conversion writes (rolebasedgroup_conversion.go), so the v1alpha1
	// entrypoint and this fixture dispatch to the same Deployment reconciler.
	makeRBG := func(name string, runtimes []workloadsv1alpha2.EngineRuntime) *workloadsv1alpha2.RoleBasedGroup {
		return &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNs},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType,
					},
					Pattern: workloadsv1alpha2.Pattern{
						StandalonePattern: &workloadsv1alpha2.StandalonePattern{
							TemplateSource: workloadsv1alpha2.TemplateSource{
								Template: ptr.To(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
									Name:    "engine",
									Image:   "nginx:latest",
									Command: []string{"nginx"},
									Env:     []corev1.EnvVar{{Name: "PYTHONPATH", Value: "/usr/lib/python"}},
								}}}}),
							},
						},
					},
					EngineRuntimes: runtimes,
				}},
			},
		}
	}

	listDeployments := func() []appsv1.Deployment {
		list := &appsv1.DeploymentList{}
		ExpectWithOffset(1, testutil.K8sClient.List(testutil.Ctx, list, client.InNamespace(testNs))).To(Succeed())
		return list.Items
	}

	// makeLeaderWorkerRBG creates a v1alpha2 RBG whose single role is a
	// leaderWorkerPattern role (the RoleInstanceSet path) referencing the
	// given engine runtimes. The consent fields require a named component
	// (D7), and a standalonePattern role has no component vocabulary to
	// name (KEP-531 Effective Component Set), so the specs that drive the profile's
	// patches render through this fixture. BuildLeaderWorkerRole declares
	// an `nginx` container the fixture renames to `engine` — the
	// role-declared container the profiles below patch.
	makeLeaderWorkerRBG := func(name string, runtimes ...workloadsv1alpha2.EngineRuntime) *workloadsv1alpha2.RoleBasedGroup {
		role := wrappersv2.BuildLeaderWorkerRole("worker").Obj()
		base := role.LeaderWorkerPattern.Template
		base.Spec.Containers[0].Name = "engine"
		base.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "PYTHONPATH", Value: "/usr/lib/python"}}
		role.EngineRuntimes = runtimes

		rbg := wrappersv2.BuildBasicRoleBasedGroup(name, testNs).
			WithRoles([]workloadsv1alpha2.RoleSpec{role}).
			Obj()
		ExpectWithOffset(1, testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

		created := &workloadsv1alpha2.RoleBasedGroup{}
		ExpectWithOffset(1, testutil.K8sClient.Get(
			testutil.Ctx, types.NamespacedName{Name: name, Namespace: testNs}, created,
		)).To(Succeed())
		return created
	}

	// roleInstanceSet waits until the role's RoleInstanceSet child exists
	// and returns it — the rendered child the patch specs assert against.
	roleInstanceSet := func(rbg *workloadsv1alpha2.RoleBasedGroup) *workloadsv1alpha2.RoleInstanceSet {
		risName := rbg.GetWorkloadName(&rbg.Spec.Roles[0])
		ris := &workloadsv1alpha2.RoleInstanceSet{}
		EventuallyWithOffset(1, func() error {
			return testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{
				Name: risName, Namespace: testNs,
			}, ris)
		}, timeout, interval).Should(Succeed())
		return ris
	}

	// componentTemplate returns the named component's rendered pod
	// template from the RoleInstanceSet, if present.
	componentTemplate := func(
		ris *workloadsv1alpha2.RoleInstanceSet, component string,
	) *corev1.PodTemplateSpec {
		for i := range ris.Spec.RoleInstanceTemplate.Components {
			c := &ris.Spec.RoleInstanceTemplate.Components[i]
			if c.Name == component {
				return &c.Template
			}
		}
		return nil
	}

	// noRoleInstanceSet pins that the role's child workload was never
	// created — a render error must leave no partial child behind.
	noRoleInstanceSet := func(rbg *workloadsv1alpha2.RoleBasedGroup) {
		risName := rbg.GetWorkloadName(&rbg.Spec.Roles[0])
		ConsistentlyWithOffset(1, func(g Gomega) {
			err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{
				Name: risName, Namespace: testNs,
			}, &workloadsv1alpha2.RoleInstanceSet{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "RoleInstanceSet %s must not exist", risName)
		}, 5*time.Second, interval).Should(Succeed())
	}

	// hasEvent reports whether a warning event with the given reason was
	// recorded in the test namespace — matched on the reason only; the
	// message wording is not contract, and reconcile loops legitimately
	// repeat events, so no count is ever asserted.
	hasEvent := func(reason string) bool {
		events := &corev1.EventList{}
		ExpectWithOffset(1, testutil.K8sClient.List(testutil.Ctx, events, client.InNamespace(testNs))).To(Succeed())
		for _, ev := range events.Items {
			if ev.Reason == reason {
				return true
			}
		}
		return false
	}

	It("renders the consented patches into the child workload", func() {
		// Scenario 3 consent: targetContainers fires the profile's patches on
		// the named role-declared container of the named component. The
		// standalonePattern fixture above has no component vocabulary
		// (KEP-531 Effective Component Set), so the consent path renders through a
		// leaderWorkerPattern role's RoleInstanceSet child.
		profile := makeProfile("ik-golden", []workloadsv1alpha2.ContainerPatch{
			addOp("/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
			addOp("/volumeMounts/-", corev1.VolumeMount{
				Name: "ik-golden-vol", MountPath: "/opt/ik", ReadOnly: true,
			}),
		})
		Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
		rbg := makeLeaderWorkerRBG("patched", workloadsv1alpha2.EngineRuntime{
			ProfileName: "ik-golden",
			Component:   string(constants.WorkerComponentType),
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
				{Name: "engine"},
			},
		})

		ris := roleInstanceSet(rbg)
		worker := componentTemplate(ris, string(constants.WorkerComponentType))
		Expect(worker).NotTo(BeNil(), "worker component template not found")
		c := findContainer(worker.Spec.Containers, "engine")
		Expect(c).NotTo(BeNil())
		// Looked up by name, not index: the common-env injector prepends
		// the RBG_* discovery vars to the container's env list.
		val, ok := envValue(c, "IK_SITE")
		Expect(ok).To(BeTrue(), "IK_SITE env var not found")
		Expect(val).To(Equal("/opt/ik/site"))
		Expect(c.VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name: "ik-golden-vol", MountPath: "/opt/ik", ReadOnly: true,
		}))
		Expect(hasVolume(worker.Spec.Volumes, "ik-golden-vol")).
			To(BeTrue(), "expected the profile volume ik-golden-vol in the rendered pod spec")

		By("verifying the consent stays scoped to the named component")
		leader := componentTemplate(ris, string(constants.LeaderComponentType))
		Expect(leader).NotTo(BeNil(), "leader component template not found")
		leaderEngine := findContainer(leader.Spec.Containers, "engine")
		Expect(leaderEngine).NotTo(BeNil())
		_, ok = envValue(leaderEngine, "IK_SITE")
		Expect(ok).To(BeFalse(), "leader: the patches must not fire outside the named component")
	})

	It("unaimed patch: fragments inject, patch does not fire, warning event", func() {
		profile := makeProfile("ik-unaimed", []workloadsv1alpha2.ContainerPatch{
			addOp("/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
		})
		Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
		Expect(testutil.K8sClient.Create(testutil.Ctx, makeRBG("unaimed", []workloadsv1alpha2.EngineRuntime{{
			ProfileName: "ik-unaimed", // no targetContainers consent
		}}))).To(Succeed())

		Eventually(func(g Gomega) {
			deps := listDeployments()
			g.Expect(deps).To(HaveLen(1))
			c := findContainer(deps[0].Spec.Template.Spec.Containers, "engine")
			g.Expect(c).NotTo(BeNil())
			// profile volume injected... (compared by name: the live volume
			// carries the profile's emptyDir source, which defeats
			// ContainElement's deep equality)
			g.Expect(hasVolume(deps[0].Spec.Template.Spec.Volumes, "ik-unaimed-vol")).
				To(BeTrue(), "expected the profile volume ik-unaimed-vol in the rendered pod spec")
			// ...but the patch did not fire
			_, added := envValue(c, "IK_SITE")
			g.Expect(added).To(BeFalse(), "the patch must not fire without targetContainers consent")
			val, ok := envValue(c, "PYTHONPATH")
			g.Expect(ok).To(BeTrue(), "PYTHONPATH env var not found")
			g.Expect(val).To(Equal("/usr/lib/python"))
			events := &corev1.EventList{}
			g.Expect(testutil.K8sClient.List(testutil.Ctx, events, client.InNamespace(testNs))).To(Succeed())
			var found bool
			for _, ev := range events.Items {
				if ev.Reason == discovery.ReasonUnaimedContainerPatch {
					found = true
				}
			}
			g.Expect(found).To(BeTrue(), "expected an UnaimedContainerPatch warning event")
		}, timeout, interval).Should(Succeed())
	})

	It("legacy single-template render: a scenario-1 reference on a Deployment-annotated leaderWorkerPattern role injects into the role template", func() {
		// KEP-531 compatibility: the legacy Deployment/StatefulSet render
		// produces one template — the role template — whatever the role's
		// pattern, and main's pod reconciler ran the sidecar injector into
		// it unconditionally. A scenario-1 reference (profileName only —
		// the new fields are rejected on deprecated surfaces) must therefore
		// inject into that single template even under leaderWorkerPattern,
		// whose RoleInstanceSet vocabulary would otherwise default the
		// reference to the leader position.
		profile := makeProfile("ik-legacy-lwp", nil)
		Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())

		role := wrappersv2.BuildLeaderWorkerRole("worker").Obj()
		base := role.LeaderWorkerPattern.Template
		base.Spec.Containers[0].Name = "engine"
		role.Annotations = map[string]string{
			constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType,
		}
		role.EngineRuntimes = []workloadsv1alpha2.EngineRuntime{{ProfileName: "ik-legacy-lwp"}}
		rbg := wrappersv2.BuildBasicRoleBasedGroup("legacy-lwp", testNs).
			WithRoles([]workloadsv1alpha2.RoleSpec{role}).
			Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

		Eventually(func(g Gomega) {
			deps := listDeployments()
			g.Expect(deps).To(HaveLen(1))
			g.Expect(hasVolume(deps[0].Spec.Template.Spec.Volumes, "ik-legacy-lwp-vol")).
				To(BeTrue(), "expected the profile volume in the single role template")
		}, timeout, interval).Should(Succeed())
	})

	It("render error: no child workload is created and a warning event fires", func() {
		// A patch target the role's own template does not declare is a
		// render error: the profile's patches address role-declared
		// spec.containers members only.
		profile := makeProfile("ik-broken", []workloadsv1alpha2.ContainerPatch{
			addOp("/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
		})
		Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
		rbg := makeLeaderWorkerRBG("broken", workloadsv1alpha2.EngineRuntime{
			ProfileName: "ik-broken",
			Component:   string(constants.WorkerComponentType),
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
				{Name: "ghost"}, // no such container
			},
		})

		By("verifying no RoleInstanceSet is created for the role")
		noRoleInstanceSet(rbg)

		By("verifying the render-failed warning event fires")
		Eventually(func(g Gomega) {
			g.Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeTrue())
		}, timeout, interval).Should(Succeed())
	})

	It("schema: op replace is rejected at write time", func() {
		profile := makeProfile("ik-replace-op", []workloadsv1alpha2.ContainerPatch{
			{JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{
				Op: "replace", Path: "/command/0",
				Value: runtime.RawExtension{Raw: []byte(`"x"`)},
			}},
		})
		err := testutil.K8sClient.Create(testutil.Ctx, profile)
		ExpectWithOffset(1, err).To(HaveOccurred())
		ExpectWithOffset(1, err.Error()).To(ContainSubstring("Unsupported value"))
	})

	It("schema: an element without a jsonPatch is rejected at write time", func() {
		profile := makeProfile("ik-no-field", []workloadsv1alpha2.ContainerPatch{{}})
		err := testutil.K8sClient.Create(testutil.Ctx, profile)
		ExpectWithOffset(1, err).To(HaveOccurred())
		ExpectWithOffset(1, err.Error()).To(ContainSubstring("jsonPatch"))
		ExpectWithOffset(1, err.Error()).To(ContainSubstring("Required value"))
	})

	It("schema: an explicit jsonPatch: null is rejected at write time", func() {
		// The typed client drops nil patch fields (omitempty), so pin the
		// explicit-null shape through unstructured: the apiserver prunes the
		// null and the required-field rule rejects the element exactly like
		// the no-field case.
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "workloads.x-k8s.io/v1alpha2",
			"kind":       "ClusterEngineRuntimeProfile",
			"metadata":   map[string]any{"name": "ik-null-arm"},
			"spec": map[string]any{
				"containerPatches": []any{
					map[string]any{"jsonPatch": nil},
				},
			},
		}}
		err := testutil.K8sClient.Create(testutil.Ctx, u)
		ExpectWithOffset(1, err).To(HaveOccurred())
		ExpectWithOffset(1, err.Error()).To(ContainSubstring("jsonPatch"))
		ExpectWithOffset(1, err.Error()).To(ContainSubstring("Required value"))
	})

	It("schema: a path outside the allowlist is rejected at write time", func() {
		profile := makeProfile("ik-bad-path", []workloadsv1alpha2.ContainerPatch{
			addOp("/image", "x"),
		})
		err := testutil.K8sClient.Create(testutil.Ctx, profile)
		ExpectWithOffset(1, err).To(HaveOccurred())
		ExpectWithOffset(1, err.Error()).To(ContainSubstring("path must be an element pointer"))
	})

	It("schema: a segment-boundary bypass (/commando) is rejected at write time", func() {
		profile := makeProfile("ik-commando", []workloadsv1alpha2.ContainerPatch{
			addOp("/commando/0", "x"),
		})
		err := testutil.K8sClient.Create(testutil.Ctx, profile)
		ExpectWithOffset(1, err).To(HaveOccurred())
		ExpectWithOffset(1, err.Error()).To(ContainSubstring("path must be an element pointer"))
	})

	It("schema: more than 32 patches is rejected at write time", func() {
		patches := make([]workloadsv1alpha2.ContainerPatch, 33)
		for i := range patches {
			patches[i] = addOp("/env/-", corev1.EnvVar{Name: fmt.Sprintf("E%d", i), Value: "v"})
		}
		err := testutil.K8sClient.Create(testutil.Ctx, makeProfile("ik-33-patches", patches))
		ExpectWithOffset(1, err).To(HaveOccurred())
		ExpectWithOffset(1, err.Error()).To(ContainSubstring("32"))
	})

	It("render error: a patch adding an env var the container already declares is rejected", func() {
		// `add` cannot join values: PYTHONPATH is already declared by the role
		// template, so this profile cannot render against this role shape.
		profile := makeProfile("ik-env-join", []workloadsv1alpha2.ContainerPatch{
			addOp("/env/-", corev1.EnvVar{Name: "PYTHONPATH", Value: "/opt/ik/site"}),
		})
		Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
		rbg := makeLeaderWorkerRBG("env-join", workloadsv1alpha2.EngineRuntime{
			ProfileName: "ik-env-join",
			Component:   string(constants.WorkerComponentType),
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
				{Name: "engine"},
			},
		})

		By("verifying no RoleInstanceSet is created for the role")
		noRoleInstanceSet(rbg)

		By("verifying the render-failed warning event fires")
		Eventually(func(g Gomega) {
			g.Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeTrue())
		}, timeout, interval).Should(Succeed())
	})

	It("profile change rides the next reconcile: update then touch the RBG", func() {
		profile := makeProfile("ik-lazy", []workloadsv1alpha2.ContainerPatch{
			addOp("/env/-", corev1.EnvVar{Name: "IK_VERSION", Value: "/opt/ik/v1"}),
		})
		Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
		rbg := makeLeaderWorkerRBG("lazy", workloadsv1alpha2.EngineRuntime{
			ProfileName: "ik-lazy",
			Component:   string(constants.WorkerComponentType),
			TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
				{Name: "engine"},
			},
		})
		risName := types.NamespacedName{Name: rbg.GetWorkloadName(&rbg.Spec.Roles[0]), Namespace: testNs}

		// workerEngineVersion reports the IK_VERSION value rendered into
		// the worker component's engine container, and whether the child
		// has rendered that far yet — absent pieces are "keep polling",
		// not failures.
		workerEngineVersion := func(ris *workloadsv1alpha2.RoleInstanceSet) (string, bool) {
			worker := componentTemplate(ris, string(constants.WorkerComponentType))
			if worker == nil {
				return "", false
			}
			c := findContainer(worker.Spec.Containers, "engine")
			if c == nil {
				return "", false
			}
			return envValue(c, "IK_VERSION")
		}

		// The Get is asserted inside the poll: a func(g Gomega) attempt
		// with no failed g assertion counts as success, so swallowing the
		// NotFound would make Eventually pass on the first try and the
		// phase vacuous.
		Eventually(func(g Gomega) {
			ris := &workloadsv1alpha2.RoleInstanceSet{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, risName, ris)).To(Succeed())
			val, ok := workerEngineVersion(ris)
			g.Expect(ok).To(BeTrue(), "worker: IK_VERSION env var not found")
			g.Expect(val).To(Equal("/opt/ik/v1"))
		}, timeout, interval).Should(Succeed())

		profile.Spec.ContainerPatches[0] = addOp("/env/-", corev1.EnvVar{Name: "IK_VERSION", Value: "/opt/ik/v2"})
		Expect(testutil.K8sClient.Update(testutil.Ctx, profile)).To(Succeed())

		// Lazy propagation: the profile change rides the RBG's next
		// reconcile (KEP Update Propagation) — force one with a spec change;
		// RBGPredicate enqueues only when the spec differs, so an
		// object-metadata annotation would not trigger anything.
		key := types.NamespacedName{Name: "lazy", Namespace: testNs}
		current := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, key, current)).To(Succeed())
		if current.Spec.Roles[0].Annotations == nil {
			current.Spec.Roles[0].Annotations = map[string]string{}
		}
		current.Spec.Roles[0].Annotations["test-patch-touch"] = fmt.Sprintf("%d", time.Now().UnixNano())
		Expect(testutil.K8sClient.Update(testutil.Ctx, current)).To(Succeed())

		Eventually(func(g Gomega) {
			ris := &workloadsv1alpha2.RoleInstanceSet{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, risName, ris)).To(Succeed())
			val, ok := workerEngineVersion(ris)
			g.Expect(ok).To(BeTrue(), "worker: IK_VERSION env var not found")
			g.Expect(val).To(Equal("/opt/ik/v2"))
		}, timeout, interval).Should(Succeed())
	})
})

func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

func hasVolume(volumes []corev1.Volume, name string) bool {
	for _, v := range volumes {
		if v.Name == name {
			return true
		}
	}
	return false
}

func envValue(container *corev1.Container, name string) (string, bool) {
	for _, env := range container.Env {
		if env.Name == name {
			return env.Value, true
		}
	}
	return "", false
}

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
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// The KEP-531 targeting semantics (keps/531-engine-runtime-profile-injection/README.md):
// an absent component expands to the pattern's default component set —
// main's published behavior, which under leaderWorkerPattern is the leader
// ONLY: the worker template's injector chain omits the sidecar stage
// outright, so a profileName-only reference never injects into the worker
// (D2, scenario 1); a named component scopes fragments and legacy merge to
// that one component — component: worker is the new capability that reaches
// a component main's default never touches (scenario 2); targetContainers
// consents the profile's patches and carries the role's own extraPatches,
// both within the named component (scenario 3).
var _ = Describe("RoleBasedGroup engine runtime targeting", func() {
	const (
		targetingTimeout  = time.Second * 60
		targetingInterval = time.Millisecond * 500
	)

	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-targeting-%d", time.Now().UnixNano())
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

	// roleAddOp builds one role-side targetContainers entry: the same
	// `add` jsonPatch arm as addOp, wrapped in the role-side shape that
	// names its own target container.
	roleAddOp := func(container, path string, value any) workloadsv1alpha2.EngineRuntimeTargetContainer {
		raw, err := json.Marshal(value)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		return workloadsv1alpha2.EngineRuntimeTargetContainer{
			Name: container,
			ExtraPatches: []workloadsv1alpha2.ContainerPatch{{
				JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{
					Op: "add", Path: path, Value: runtime.RawExtension{Raw: raw},
				},
			}},
		}
	}

	// makeTargetingProfile builds a profile carrying one emptyDir volume
	// (a pod-level fragment) plus the given container patches. The
	// updateStrategy carries no omitempty: leaving the zero value would
	// marshal spec.updateStrategy as "" and fail the apiserver enum
	// validation, so the fixture sets it.
	makeTargetingProfile := func(name string, patches ...workloadsv1alpha2.ContainerPatch) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
		return &workloadsv1alpha2.ClusterEngineRuntimeProfile{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
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

	// withSidecar declares one profile-injected sidecar container — the
	// fragment container a role-side targetContainers entry addresses.
	withSidecar := func(
		profile *workloadsv1alpha2.ClusterEngineRuntimeProfile,
		name string, env ...corev1.EnvVar,
	) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
		profile.Spec.Containers = append(profile.Spec.Containers, corev1.Container{
			Name: name, Image: name + ":latest", Env: env,
		})
		return profile
	}

	// withInitContainer declares one profile-injected init container with
	// the given args — role-side patches address profile init containers
	// too, and an /args add requires the target to declare its args.
	withInitContainer := func(
		profile *workloadsv1alpha2.ClusterEngineRuntimeProfile,
		name string, args ...string,
	) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
		profile.Spec.InitContainers = append(profile.Spec.InitContainers, corev1.Container{
			Name: name, Image: name + ":latest", Args: args,
		})
		return profile
	}

	// leaderWorkerRole builds the single leaderWorkerPattern role the
	// targeting fixtures render (the RoleInstanceSet path) referencing the
	// given engine runtimes. BuildLeaderWorkerRole declares an `nginx`
	// container in the shared base template; the targeting semantics
	// address it as `engine`, so the fixture renames it and gives it a
	// PYTHONPATH env the profile must not collide with. Returned by value
	// so callers can still mutate the shared base template through the
	// embedded pointers (e.g. to declare a role volume for the D9 cases).
	leaderWorkerRole := func(runtimes ...workloadsv1alpha2.EngineRuntime) workloadsv1alpha2.RoleSpec {
		role := wrappersv2.BuildLeaderWorkerRole("worker").Obj()
		base := role.LeaderWorkerPattern.Template
		base.Spec.Containers[0].Name = "engine"
		base.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "PYTHONPATH", Value: "/usr/lib/python"}}
		role.EngineRuntimes = runtimes
		return role
	}

	// createLeaderWorkerRBG creates a v1alpha2 RBG carrying one
	// already-built leaderWorkerPattern role — the entrypoint for fixtures
	// that need to shape the role beyond the runtimes list.
	createLeaderWorkerRBG := func(name string, role workloadsv1alpha2.RoleSpec) *workloadsv1alpha2.RoleBasedGroup {
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

	// leaderWorkerRBG creates a v1alpha2 RBG whose single role is a
	// leaderWorkerPattern role referencing the given engine runtimes.
	leaderWorkerRBG := func(name string, runtimes ...workloadsv1alpha2.EngineRuntime) *workloadsv1alpha2.RoleBasedGroup {
		return createLeaderWorkerRBG(name, leaderWorkerRole(runtimes...))
	}

	// standaloneRBG creates a v1alpha2 RBG whose named role renders through
	// the RoleInstanceSet path — the default workload type, and the only
	// render path the new reference fields are defined on: a role routed to
	// a deprecated surface (Deployment/StatefulSet/LWS) cannot carry
	// component or targetContainers at all (fourth shared invariant).
	// roleName doubles as the standalone component's name (KEP-173: the
	// component is named after the role), so the mixed-case vocabulary the
	// KEP requires is writable here.
	standaloneRBG := func(name, roleName string, runtime workloadsv1alpha2.EngineRuntime) *workloadsv1alpha2.RoleBasedGroup {
		return &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNs},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:     roleName,
					Replicas: ptr.To(int32(1)),
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
					EngineRuntimes: []workloadsv1alpha2.EngineRuntime{runtime},
				}},
			},
		}
	}

	// rolePods waits until every pod of the role exists and indexes them by
	// component name, so the leader and worker components are addressable
	// separately.
	rolePods := func(rbgName, roleName string, count int) map[string]corev1.Pod {
		pods := map[string]corev1.Pod{}
		EventuallyWithOffset(1, func() int {
			podList := &corev1.PodList{}
			if err := testutil.K8sClient.List(testutil.Ctx, podList,
				client.InNamespace(testNs),
				client.MatchingLabels{
					constants.GroupNameLabelKey: rbgName,
					constants.RoleNameLabelKey:  roleName,
				},
			); err != nil {
				return 0
			}
			pods = map[string]corev1.Pod{}
			for i := range podList.Items {
				pod := podList.Items[i]
				if pod.DeletionTimestamp != nil {
					continue
				}
				pods[pod.Labels[constants.ComponentNameLabelKey]] = pod
			}
			return len(pods)
		}, targetingTimeout, targetingInterval).Should(Equal(count))
		return pods
	}

	// listRoleInstanceSets lists the RoleInstanceSets of the test namespace —
	// the child workload shape a standalonePattern role renders through the
	// default RoleInstanceSet path.
	listRoleInstanceSets := func() []workloadsv1alpha2.RoleInstanceSet {
		list := &workloadsv1alpha2.RoleInstanceSetList{}
		ExpectWithOffset(1, testutil.K8sClient.List(testutil.Ctx, list, client.InNamespace(testNs))).To(Succeed())
		return list.Items
	}

	// hasEvent reports whether a warning event with the given reason was
	// recorded in the test namespace. Assertions match on the reason only —
	// the message wording is not contract, and reconcile loops legitimately
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

	// noEngineRuntimeEvents reports whether no engine-runtime event of any
	// known reason fired — the byte-for-byte guarantee of scenario 1.
	noEngineRuntimeEvents := func() bool {
		return !hasEvent(discovery.ReasonUnaimedContainerPatch) &&
			!hasEvent(discovery.ReasonEngineRuntimeRenderFailed) &&
			!hasEvent(discovery.ReasonEngineRuntimeWrapShadowed)
	}

	hasMountName := func(mounts []corev1.VolumeMount, name string) bool {
		for _, m := range mounts {
			if m.Name == name {
				return true
			}
		}
		return false
	}

	// noRoleInstanceSet pins that the role's child workload was never
	// created — the render error must leave no partial child behind.
	noRoleInstanceSet := func(rbg *workloadsv1alpha2.RoleBasedGroup) {
		risName := rbg.GetWorkloadName(&rbg.Spec.Roles[0])
		ConsistentlyWithOffset(1, func(g Gomega) {
			err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{
				Name: risName, Namespace: testNs,
			}, &workloadsv1alpha2.RoleInstanceSet{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "RoleInstanceSet %s must not exist", risName)
		}, 5*time.Second, targetingInterval).Should(Succeed())
	}

	// roleInstanceSet waits until the role's RoleInstanceSet child exists
	// and returns it — the rendered template the residue assertions read.
	roleInstanceSet := func(rbg *workloadsv1alpha2.RoleBasedGroup) *workloadsv1alpha2.RoleInstanceSet {
		risName := rbg.GetWorkloadName(&rbg.Spec.Roles[0])
		ris := &workloadsv1alpha2.RoleInstanceSet{}
		EventuallyWithOffset(1, func() error {
			return testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{
				Name: risName, Namespace: testNs,
			}, ris)
		}, targetingTimeout, targetingInterval).Should(Succeed())
		return ris
	}

	// componentTemplate returns the named component's rendered pod template
	// from the RoleInstanceSet, if present.
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

	Context("scenario 1: profileName-only references stay byte-for-byte compatible", func() {
		It("delivers the fragments to the leader only — the worker sidecar stage is skipped", func() {
			// D2: absent component expands to the pattern's default
			// component set, which under leaderWorkerPattern is the leader
			// ONLY — main's published behavior. The worker template's
			// injector chain omits the sidecar stage outright, so no
			// fragment and no legacy merge may reach it.
			profile := makeTargetingProfile("ik-compat")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-compat", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-compat",
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			leader := findContainer(pods[string(constants.LeaderComponentType)].Spec.Containers, "engine")
			Expect(leader).NotTo(BeNil(), "leader: engine container not found")
			Expect(hasVolume(pods[string(constants.LeaderComponentType)].Spec.Volumes, "ik-compat-vol")).
				To(BeTrue(), "leader: expected the profile volume in the rendered pod spec")
			// No patch dimension is expressed by this reference, so no
			// patch env var may appear on the engine.
			_, patched := envValue(leader, "IK_SITE")
			Expect(patched).To(BeFalse(), "leader: no patch env may fire on a profileName-only reference")

			By("verifying the worker template skips the sidecar stage entirely")
			worker := findContainer(pods[string(constants.WorkerComponentType)].Spec.Containers, "engine")
			Expect(worker).NotTo(BeNil(), "worker: the role's own engine container still renders")
			Expect(hasVolume(pods[string(constants.WorkerComponentType)].Spec.Volumes, "ik-compat-vol")).
				To(BeFalse(), "worker: the absent-component default is leader-only — no fragments may reach the worker template")

			By("verifying no engine-runtime event of any reason fires")
			Consistently(func(g Gomega) {
				g.Expect(noEngineRuntimeEvents()).To(BeTrue())
			}, 3*time.Second, targetingInterval).Should(Succeed())
		})

		It("withholds the patches of a patch-carrying profile and warns", func() {
			// A profile that later gains containerPatches must not change a
			// pre-existing reference's rendered output: the fragments still
			// land in the default component set (the leader only under
			// leaderWorkerPattern), the patches do not fire, and the
			// unaimed-patch warning event is emitted (asserted by presence —
			// reconcile loops legitimately repeat it).
			profile := makeTargetingProfile("ik-compat-patches",
				addOp("/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
				addOp("/volumeMounts/-", corev1.VolumeMount{
					Name: "ik-compat-patches-vol", MountPath: "/opt/ik", ReadOnly: true,
				}),
			)
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-compat-patches", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-compat-patches",
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			leader := findContainer(pods[string(constants.LeaderComponentType)].Spec.Containers, "engine")
			Expect(leader).NotTo(BeNil(), "leader: engine container not found")
			Expect(hasVolume(pods[string(constants.LeaderComponentType)].Spec.Volumes, "ik-compat-patches-vol")).
				To(BeTrue(), "leader: the fragments must keep landing in the default component set")
			_, patched := envValue(leader, "IK_SITE")
			Expect(patched).To(BeFalse(), "leader: the patches must not fire without targetContainers consent")

			By("verifying the worker template skips the sidecar stage entirely")
			worker := findContainer(pods[string(constants.WorkerComponentType)].Spec.Containers, "engine")
			Expect(worker).NotTo(BeNil(), "worker: the role's own engine container still renders")
			Expect(hasVolume(pods[string(constants.WorkerComponentType)].Spec.Volumes, "ik-compat-patches-vol")).
				To(BeFalse(), "worker: the absent-component default is leader-only — no fragments may reach the worker template")

			By("verifying the unaimed-patch warning event fires")
			Eventually(func(g Gomega) {
				g.Expect(hasEvent(discovery.ReasonUnaimedContainerPatch)).To(BeTrue())
			}, targetingTimeout, targetingInterval).Should(Succeed())
		})

		It("removing the reference leaves no template residue", func() {
			// One artifact, one revert: the reference carries the fragments,
			// the legacy overrides and the patches together, so deleting it
			// must leave the role template exactly as it was — no injected
			// container, no volume, the role's own env intact.
			profile := withSidecar(makeTargetingProfile("ik-remove"), "ik-sidecar")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-remove", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-remove",
			})

			ris := roleInstanceSet(rbg)
			leader := componentTemplate(ris, string(constants.LeaderComponentType))
			Expect(leader).NotTo(BeNil(), "leader component template not found")
			Expect(findContainer(leader.Spec.Containers, "ik-sidecar")).
				NotTo(BeNil(), "leader: the profile sidecar must render while the reference stands")
			Expect(hasVolume(leader.Spec.Volumes, "ik-remove-vol")).
				To(BeTrue(), "leader: the profile volume must render while the reference stands")

			By("removing the engineRuntimes entry from the role")
			key := types.NamespacedName{Name: rbg.Name, Namespace: testNs}
			current := &workloadsv1alpha2.RoleBasedGroup{}
			Expect(testutil.K8sClient.Get(testutil.Ctx, key, current)).To(Succeed())
			current.Spec.Roles[0].EngineRuntimes = nil
			Expect(testutil.K8sClient.Update(testutil.Ctx, current)).To(Succeed())

			By("verifying the re-rendered template carries no fragment of the removed reference")
			Eventually(func(g Gomega) {
				ris := &workloadsv1alpha2.RoleInstanceSet{}
				g.Expect(testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{
					Name: rbg.GetWorkloadName(&current.Spec.Roles[0]), Namespace: testNs,
				}, ris)).To(Succeed())
				leader := componentTemplate(ris, string(constants.LeaderComponentType))
				g.Expect(leader).NotTo(BeNil(), "leader component template not found")
				g.Expect(findContainer(leader.Spec.Containers, "ik-sidecar")).
					To(BeNil(), "leader: the profile sidecar must vanish with the reference")
				g.Expect(hasVolume(leader.Spec.Volumes, "ik-remove-vol")).
					To(BeFalse(), "leader: the profile volume must vanish with the reference")
				engine := findContainer(leader.Spec.Containers, "engine")
				g.Expect(engine).NotTo(BeNil(), "leader: the role's own engine container must stay")
				val, ok := envValue(engine, "PYTHONPATH")
				g.Expect(ok).To(BeTrue(), "leader: the role's own env must stay")
				g.Expect(val).To(Equal("/usr/lib/python"))
			}, targetingTimeout, targetingInterval).Should(Succeed())
		})
	})

	Context("scenario 2: a named component narrows the fragments", func() {
		It("delivers the fragments to the worker only", func() {
			profile := makeTargetingProfile("ik-narrow")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-narrow", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-narrow",
				Component:   string(constants.WorkerComponentType),
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			Expect(hasVolume(pods[string(constants.WorkerComponentType)].Spec.Volumes, "ik-narrow-vol")).
				To(BeTrue(), "worker: expected the profile volume in the rendered pod spec")
			Expect(hasVolume(pods[string(constants.LeaderComponentType)].Spec.Volumes, "ik-narrow-vol")).
				To(BeFalse(), "leader: the fragments must not deliver outside the named component")

			By("verifying no engine-runtime event fires")
			Expect(noEngineRuntimeEvents()).To(BeTrue())
		})

		It("component: worker delivers pod-level fragments to the worker template (new capability)", func() {
			// D2: naming component: worker reaches a component main's
			// default never touches — main's worker injector chain omits the
			// sidecar stage outright, so this is scope EXTENSION, the first
			// surface to make worker-side pod-level injection reachable.
			// Every fragment kind lands: init container, sidecar and volume.
			profile := withInitContainer(
				withSidecar(makeTargetingProfile("ik-worker-cap"), "ik-sidecar"), "ik-init",
			)
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-worker-cap", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-worker-cap",
				Component:   string(constants.WorkerComponentType),
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			workerSpec := pods[string(constants.WorkerComponentType)].Spec
			Expect(findContainer(workerSpec.Containers, "ik-sidecar")).
				NotTo(BeNil(), "worker: the profile sidecar must render")
			Expect(findInitContainer(workerSpec.InitContainers, "ik-init")).
				NotTo(BeNil(), "worker: the profile init container must render")
			Expect(hasVolume(workerSpec.Volumes, "ik-worker-cap-vol")).
				To(BeTrue(), "worker: the profile volume must render")

			By("verifying the leader template stays untouched")
			leaderSpec := pods[string(constants.LeaderComponentType)].Spec
			Expect(findContainer(leaderSpec.Containers, "ik-sidecar")).
				To(BeNil(), "leader: the fragments must not deliver outside the named component")
			Expect(findInitContainer(leaderSpec.InitContainers, "ik-init")).
				To(BeNil(), "leader: the fragments must not deliver outside the named component")
			Expect(hasVolume(leaderSpec.Volumes, "ik-worker-cap-vol")).
				To(BeFalse(), "leader: the fragments must not deliver outside the named component")

			By("verifying no engine-runtime event fires")
			Expect(noEngineRuntimeEvents()).To(BeTrue())
		})
	})

	Context("scenario 3: consented patches within the named component", func() {
		It("profile-side: targetContainers fires the profile's patches on the named role container", func() {
			profile := makeTargetingProfile("ik-consent",
				addOp("/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
				addOp("/volumeMounts/-", corev1.VolumeMount{
					Name: "ik-consent-vol", MountPath: "/opt/ik", ReadOnly: true,
				}),
			)
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-consent", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-consent",
				Component:   string(constants.WorkerComponentType),
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					{Name: "engine"},
				},
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			worker := findContainer(pods[string(constants.WorkerComponentType)].Spec.Containers, "engine")
			leader := findContainer(pods[string(constants.LeaderComponentType)].Spec.Containers, "engine")
			Expect(worker).NotTo(BeNil())
			Expect(leader).NotTo(BeNil())

			By("verifying the worker engine gains both the env var and the mount")
			val, ok := envValue(worker, "IK_SITE")
			Expect(ok).To(BeTrue(), "worker: IK_SITE env var not found")
			Expect(val).To(Equal("/opt/ik/site"))
			Expect(worker.VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name: "ik-consent-vol", MountPath: "/opt/ik", ReadOnly: true,
			}))
			Expect(hasVolume(pods[string(constants.WorkerComponentType)].Spec.Volumes, "ik-consent-vol")).
				To(BeTrue(), "worker: the fragments render within the named component")

			By("verifying the leader engine gains neither")
			_, ok = envValue(leader, "IK_SITE")
			Expect(ok).To(BeFalse(), "leader: the patches must not fire outside the named component")
			Expect(hasMountName(leader.VolumeMounts, "ik-consent-vol")).
				To(BeFalse(), "leader: the profile volume must not be mounted")

			By("verifying no warning event fires — the reference consented")
			Expect(hasEvent(discovery.ReasonUnaimedContainerPatch)).To(BeFalse())
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})

		It("role-side: extraPatches fire the role's patches into the profile's sidecar", func() {
			profile := withSidecar(makeTargetingProfile("ik-rolepatch"), "ik-sidecar")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-rolepatch", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-rolepatch",
				Component:   string(constants.WorkerComponentType),
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					roleAddOp("ik-sidecar", "/env/-", corev1.EnvVar{Name: "ROLE_FLAG", Value: "on"}),
				},
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			workerSidecar := findContainer(pods[string(constants.WorkerComponentType)].Spec.Containers, "ik-sidecar")
			Expect(workerSidecar).NotTo(BeNil(), "worker: the profile sidecar must render in the named component")
			val, ok := envValue(workerSidecar, "ROLE_FLAG")
			Expect(ok).To(BeTrue(), "worker: ROLE_FLAG env var not found on the sidecar")
			Expect(val).To(Equal("on"))

			By("verifying the sidecar renders in the worker only")
			Expect(findContainer(pods[string(constants.LeaderComponentType)].Spec.Containers, "ik-sidecar")).
				To(BeNil(), "leader: the fragments must not deliver outside the named component")

			By("verifying no warning event fires — writing the rules is the consent")
			Expect(hasEvent(discovery.ReasonUnaimedContainerPatch)).To(BeFalse())
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})

		It("role-side: extraPatches address profile init containers too", func() {
			profile := withInitContainer(makeTargetingProfile("ik-init-patch"), "ik-init", "--seed")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-init", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-init-patch",
				Component:   string(constants.WorkerComponentType),
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					roleAddOp("ik-init", "/args/-", "--ik-warm"),
				},
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			workerInit := findInitContainer(pods[string(constants.WorkerComponentType)].Spec.InitContainers, "ik-init")
			Expect(workerInit).NotTo(BeNil(), "worker: the profile init container must render in the named component")
			Expect(workerInit.Args).To(Equal([]string{"--seed", "--ik-warm"}),
				"worker: the role-side patch must append its arg to the profile init container")

			By("verifying the init container renders in the worker only")
			Expect(findInitContainer(pods[string(constants.LeaderComponentType)].Spec.InitContainers, "ik-init")).
				To(BeNil(), "leader: the fragments must not deliver outside the named component")

			By("verifying no warning event fires")
			Expect(hasEvent(discovery.ReasonUnaimedContainerPatch)).To(BeFalse())
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})

		It("bidirectional: an entry naming a profile-declared container receives only the role-side pass", func() {
			// D8 consent split: the profile's containerPatches fire only at
			// role-declared entries — the engine uniformly — while a
			// profile-declared entry (the injected sidecar) is addressable
			// but takes no profile-side patches; the entry's own
			// extraPatches still land on it. Role content flowing into a
			// profile container is the role author's decision, never a
			// render error.
			profile := withSidecar(makeTargetingProfile("ik-bidir",
				addOp("/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
			), "ik-sidecar")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-bidir", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-bidir",
				Component:   string(constants.WorkerComponentType),
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					{Name: "engine"},
					roleAddOp("ik-sidecar", "/env/-", corev1.EnvVar{Name: "ROLE_FLAG", Value: "on"}),
				},
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			workerSpec := pods[string(constants.WorkerComponentType)].Spec
			engine := findContainer(workerSpec.Containers, "engine")
			Expect(engine).NotTo(BeNil(), "worker: engine container not found")
			val, ok := envValue(engine, "IK_SITE")
			Expect(ok).To(BeTrue(), "worker: the profile patch must fire at the role-declared entry")
			Expect(val).To(Equal("/opt/ik/site"))

			By("verifying the profile patch does not fire at the profile-declared entry — consent is role-declared only")
			sidecar := findContainer(workerSpec.Containers, "ik-sidecar")
			Expect(sidecar).NotTo(BeNil(), "worker: the profile sidecar must render in the named component")
			_, ok = envValue(sidecar, "IK_SITE")
			Expect(ok).To(BeFalse(), "worker: a profile-declared entry takes no profile-side patches (D8 consent)")

			By("verifying the role's extraPatches land on the profile-declared target")
			val, ok = envValue(sidecar, "ROLE_FLAG")
			Expect(ok).To(BeTrue(), "worker: the role patch must land on the profile sidecar (D9)")
			Expect(val).To(Equal("on"))

			By("verifying no warning event fires — a role-declared consent address is in the mix")
			Expect(hasEvent(discovery.ReasonUnaimedContainerPatch)).To(BeFalse())
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})

		It("role-side: a same-name append onto the profile's env is the override gesture", func() {
			// The profile sidecar declares FROM_PROFILE; the role-side patch
			// appends the same name — no error, because role patches land
			// last and win positionally: the same override the legacy
			// containers merge performs on main.
			profile := withSidecar(makeTargetingProfile("ik-override"), "ik-sidecar",
				corev1.EnvVar{Name: "FROM_PROFILE", Value: "profile"},
			)
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-override", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-override",
				Component:   string(constants.WorkerComponentType),
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					roleAddOp("ik-sidecar", "/env/-", corev1.EnvVar{Name: "FROM_PROFILE", Value: "role"}),
				},
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			sidecar := findContainer(pods[string(constants.WorkerComponentType)].Spec.Containers, "ik-sidecar")
			Expect(sidecar).NotTo(BeNil(), "worker: the profile sidecar must render in the named component")
			idxs := envIndexes(sidecar, "FROM_PROFILE")
			Expect(idxs).To(HaveLen(2), "worker: the override gesture appends, never replaces")
			Expect(sidecar.Env[idxs[0]].Value).To(Equal("profile"),
				"worker: the profile's entry stays first")
			Expect(sidecar.Env[idxs[1]].Value).To(Equal("role"),
				"worker: the role's entry lands last and wins positionally")

			By("verifying the render succeeded — no render-failed event fires")
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})

		It("role-side: a role-declared volume mounts into a profile container (D9)", func() {
			// One-way declarations bind the profile's own spec only; the
			// converse — role content flowing into a profile container — is
			// the role author's decision: the role template declares
			// role-vol and the role-side patch mounts it into the profile's
			// sidecar, the one channel that holds both halves.
			profile := withSidecar(makeTargetingProfile("ik-role-vol"), "ik-sidecar")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			role := leaderWorkerRole(workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-role-vol",
				Component:   string(constants.WorkerComponentType),
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					roleAddOp("ik-sidecar", "/volumeMounts/-", corev1.VolumeMount{
						Name: "role-vol", MountPath: "/mnt/role",
					}),
				},
			})
			base := role.LeaderWorkerPattern.Template
			base.Spec.Volumes = append(base.Spec.Volumes, corev1.Volume{
				Name:         "role-vol",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			})
			rbg := createLeaderWorkerRBG("targeting-role-vol", role)

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			sidecar := findContainer(pods[string(constants.WorkerComponentType)].Spec.Containers, "ik-sidecar")
			Expect(sidecar).NotTo(BeNil(), "worker: the profile sidecar must render in the named component")
			Expect(hasMountName(sidecar.VolumeMounts, "role-vol")).
				To(BeTrue(), "worker: the role-declared volume must mount into the profile container")

			By("verifying the render succeeded — no render-failed event fires")
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})

		It("role-side: a secretKeyRef env lands in a profile container (D9)", func() {
			// A consuming-namespace secret handed to the publisher's image:
			// the patch author, the secret owner and the consent-giver are
			// the same party — the same trust decision as running any
			// third-party image with that secret, reviewable in the role's
			// spec.
			profile := withSidecar(makeTargetingProfile("ik-role-secret"), "ik-sidecar")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-role-secret", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-role-secret",
				Component:   string(constants.WorkerComponentType),
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					roleAddOp("ik-sidecar", "/env/-", corev1.EnvVar{
						Name: "ROLE_SECRET",
						ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "role-secret"},
							Key:                  "token",
						}},
					}),
				},
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			sidecar := findContainer(pods[string(constants.WorkerComponentType)].Spec.Containers, "ik-sidecar")
			Expect(sidecar).NotTo(BeNil(), "worker: the profile sidecar must render in the named component")
			Expect(sidecar.Env).To(ContainElement(corev1.EnvVar{
				Name: "ROLE_SECRET",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "role-secret"},
					Key:                  "token",
				}},
			}), "worker: the secretKeyRef env must land on the profile container")

			By("verifying the render succeeded — no render-failed event fires")
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})
	})

	Context("render errors", func() {
		It("D6: two same-profile references naming one component create no child", func() {
			profile := makeTargetingProfile("ik-d6")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-d6",
				workloadsv1alpha2.EngineRuntime{ProfileName: "ik-d6", Component: string(constants.LeaderComponentType)},
				workloadsv1alpha2.EngineRuntime{ProfileName: "ik-d6", Component: string(constants.LeaderComponentType)},
			)

			By("verifying no RoleInstanceSet is created for the role")
			noRoleInstanceSet(rbg)

			By("verifying the render-failed warning event fires")
			Eventually(func(g Gomega) {
				g.Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeTrue())
			}, targetingTimeout, targetingInterval).Should(Succeed())
		})

		It("a reference naming an unknown component creates no child", func() {
			profile := makeTargetingProfile("ik-bogus")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-bogus", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-bogus",
				Component:   "bogus", // no such component under leaderWorkerPattern
			})

			By("verifying no RoleInstanceSet is created for the role")
			noRoleInstanceSet(rbg)

			By("verifying the render-failed warning event fires")
			Eventually(func(g Gomega) {
				g.Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeTrue())
			}, targetingTimeout, targetingInterval).Should(Succeed())
		})

		It("an explicit component on a standalone role creates no child", func() {
			// The standalone component is named after the role (KEP-173), so
			// `component` must equal the role's own name — here "worker" —
			// and any other name fails the render vocabulary check.
			profile := makeTargetingProfile("ik-standalone-pods")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			Expect(testutil.K8sClient.Create(testutil.Ctx, standaloneRBG("targeting-standalone", "worker", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-standalone-pods",
				Component:   string(constants.LeaderComponentType), // not this role's name
			}))).To(Succeed())

			By("verifying no RoleInstanceSet child is created")
			Consistently(func(g Gomega) {
				g.Expect(listRoleInstanceSets()).To(BeEmpty())
			}, 5*time.Second, targetingInterval).Should(Succeed())

			By("verifying the render-failed warning event fires")
			Eventually(func(g Gomega) {
				g.Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeTrue())
			}, targetingTimeout, targetingInterval).Should(Succeed())
		})

		It("a targetContainers entry naming a container another reference injected creates no child", func() {
			// An entry resolves against the role-declared names plus the
			// container names this reference's own profile declares —
			// anything else is a render error. `other-sidecar` is declared
			// by ik-other, not by the referencing profile ik-err, so the
			// entry has no addressable target.
			otherProfile := withSidecar(makeTargetingProfile("ik-other"), "other-sidecar")
			errProfile := makeTargetingProfile("ik-err",
				addOp("/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
			)
			Expect(testutil.K8sClient.Create(testutil.Ctx, otherProfile)).To(Succeed())
			Expect(testutil.K8sClient.Create(testutil.Ctx, errProfile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-foreign",
				workloadsv1alpha2.EngineRuntime{ProfileName: "ik-other"},
				workloadsv1alpha2.EngineRuntime{
					ProfileName: "ik-err",
					Component:   string(constants.WorkerComponentType),
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
						{Name: "other-sidecar"}, // injected by the other reference's profile
					},
				},
			)

			By("verifying no RoleInstanceSet is created for the role")
			noRoleInstanceSet(rbg)

			By("verifying the render-failed warning event fires")
			Eventually(func(g Gomega) {
				g.Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeTrue())
			}, targetingTimeout, targetingInterval).Should(Succeed())
		})

		It("D6: a compatibility-shape reference overlapping a component-scoped one creates no child", func() {
			// The compat reference implicitly covers {leader} under
			// leaderWorkerPattern, which the scoped reference also names —
			// the effective sets intersect, so scope ownership refuses the
			// pair. Only the pure compat pair (both references lack
			// component and targetContainers) is exempt.
			profile := makeTargetingProfile("ik-mixed-err")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-mixed-err",
				workloadsv1alpha2.EngineRuntime{ProfileName: "ik-mixed-err"},
				workloadsv1alpha2.EngineRuntime{
					ProfileName: "ik-mixed-err",
					Component:   string(constants.LeaderComponentType),
				},
			)

			By("verifying no RoleInstanceSet is created for the role")
			noRoleInstanceSet(rbg)

			By("verifying the render-failed warning event fires")
			Eventually(func(g Gomega) {
				g.Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeTrue())
			}, targetingTimeout, targetingInterval).Should(Succeed())
		})

		It("a profile deleted mid-life dangles the reference and fails the next render", func() {
			// Deletion interleaving: this suite installs no admission
			// webhooks, so the freeze that would otherwise hold a
			// referenced profile in place is absent and a live reference
			// can dangle — the one window the webhook-enabled suite
			// cannot reach. A dangling reference is a render error exactly
			// like a missing-from-birth profile: the next render fails
			// with EngineRuntimeRenderFailed, and no NEW child appears —
			// the child the first successful render created stands with
			// its last successful content, untouched.
			profile := makeTargetingProfile("ik-dangling")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := standaloneRBG("targeting-dangling", "worker", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-dangling", // component-less: unscoped, renders initially
			})
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

			ris := roleInstanceSet(rbg)
			template := componentTemplate(ris, rbg.Spec.Roles[0].Name)
			Expect(template).NotTo(BeNil(), "role-named component template not found")
			Expect(hasVolume(template.Spec.Volumes, "ik-dangling-vol")).
				To(BeTrue(), "the first render must deliver the profile's volume")

			By("deleting the profile — the freeze webhook is absent, so the live reference dangles")
			Expect(testutil.K8sClient.Delete(testutil.Ctx, profile)).To(Succeed())

			By("forcing a re-render with a role spec change")
			// RBGPredicate enqueues only when the spec differs, so the
			// touch annotation (not object metadata) rides the reconcile.
			// Each retry writes a fresh value: a touch landing before the
			// controller's profile informer has observed the delete renders
			// against the stale cache and the failure needs a second
			// enqueue. The render-failure phase later proves the failed
			// re-render rewrote nothing, so retries cannot skew it.
			key := types.NamespacedName{Name: rbg.Name, Namespace: testNs}
			touchRole := func() {
				current := &workloadsv1alpha2.RoleBasedGroup{}
				Expect(testutil.K8sClient.Get(testutil.Ctx, key, current)).To(Succeed())
				if current.Spec.Roles[0].Annotations == nil {
					current.Spec.Roles[0].Annotations = map[string]string{}
				}
				current.Spec.Roles[0].Annotations["test-targeting-touch"] = fmt.Sprintf("%d", time.Now().UnixNano())
				Expect(testutil.K8sClient.Update(testutil.Ctx, current)).To(Succeed())
			}

			By("verifying the render-failed warning event fires for the dangling reference")
			Eventually(func(g Gomega) {
				touchRole()
				g.Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeTrue())
			}, targetingTimeout, targetingInterval).Should(Succeed())

			By("verifying no new RoleInstanceSet child appears — the standing child is untouched")
			Consistently(func(g Gomega) {
				g.Expect(listRoleInstanceSets()).To(HaveLen(1),
					"the failed re-render must leave exactly the one standing child")
				ris := &workloadsv1alpha2.RoleInstanceSet{}
				g.Expect(testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{
					Name: rbg.GetWorkloadName(&rbg.Spec.Roles[0]), Namespace: testNs,
				}, ris)).To(Succeed())
				template := componentTemplate(ris, rbg.Spec.Roles[0].Name)
				g.Expect(template).NotTo(BeNil(), "role-named component template not found")
				g.Expect(hasVolume(template.Spec.Volumes, "ik-dangling-vol")).To(BeTrue(),
					"the standing child keeps its last successful render — the failed re-render replaces nothing")
			}, 5*time.Second, targetingInterval).Should(Succeed())
		})

		It("a same-name recreate resolves the dangling reference and the re-render succeeds", func() {
			// The watch's blind spot: a CREATE under a referenced profile's
			// name is not observable as a reference-relevant change, and
			// the reference resolves by name at render time, never by UID —
			// so after the same-name recreate the next render succeeds with
			// the RECREATED spec and no failure event fires. The recreated
			// profile declares a sidecar the original never had, so the
			// re-rendered child carrying it proves the resolution read the
			// new object, not a stale one.
			profile := makeTargetingProfile("ik-recreated")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := standaloneRBG("targeting-recreated", "worker", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-recreated",
			})
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

			ris := roleInstanceSet(rbg)
			template := componentTemplate(ris, rbg.Spec.Roles[0].Name)
			Expect(template).NotTo(BeNil(), "role-named component template not found")
			Expect(hasVolume(template.Spec.Volumes, "ik-recreated-vol")).
				To(BeTrue(), "the first render must deliver the profile's volume")
			Expect(findContainer(template.Spec.Containers, "ik-recreated-sidecar")).
				To(BeNil(), "the original profile declares no sidecar to deliver")

			By("deleting the profile and recreating it under the same name with a different spec")
			Expect(testutil.K8sClient.Delete(testutil.Ctx, profile)).To(Succeed())
			recreated := withSidecar(makeTargetingProfile("ik-recreated"), "ik-recreated-sidecar")
			Expect(testutil.K8sClient.Create(testutil.Ctx, recreated)).To(Succeed())

			By("forcing a re-render with a role spec change")
			// Same touch pattern as the dangling spec: each retry carries a
			// fresh annotation, so a touch racing the informer's
			// observation of the recreate self-heals instead of flaking.
			key := types.NamespacedName{Name: rbg.Name, Namespace: testNs}
			touchRole := func() {
				current := &workloadsv1alpha2.RoleBasedGroup{}
				Expect(testutil.K8sClient.Get(testutil.Ctx, key, current)).To(Succeed())
				if current.Spec.Roles[0].Annotations == nil {
					current.Spec.Roles[0].Annotations = map[string]string{}
				}
				current.Spec.Roles[0].Annotations["test-targeting-touch"] = fmt.Sprintf("%d", time.Now().UnixNano())
				Expect(testutil.K8sClient.Update(testutil.Ctx, current)).To(Succeed())
			}

			By("verifying the re-render resolves against the recreated profile's spec")
			Eventually(func(g Gomega) {
				touchRole()
				ris := &workloadsv1alpha2.RoleInstanceSet{}
				g.Expect(testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{
					Name: rbg.GetWorkloadName(&rbg.Spec.Roles[0]), Namespace: testNs,
				}, ris)).To(Succeed())
				template := componentTemplate(ris, rbg.Spec.Roles[0].Name)
				g.Expect(template).NotTo(BeNil(), "role-named component template not found")
				g.Expect(findContainer(template.Spec.Containers, "ik-recreated-sidecar")).NotTo(BeNil(),
					"the re-render must deliver the recreated profile's sidecar — the reference resolved by name")
				g.Expect(hasVolume(template.Spec.Volumes, "ik-recreated-vol")).
					To(BeTrue(), "the recreated profile's volume must render")
			}, targetingTimeout, targetingInterval).Should(Succeed())

			By("verifying no render-failed event fires once the recreate is resolved")
			// A retry that raced the informer lag may have failed a render
			// before the resolution landed — a harness artifact, not the
			// semantics under test. The guarantee is that the resolved
			// reference renders cleanly from here on.
			resolved := time.Now()
			Consistently(func(g Gomega) {
				events := &corev1.EventList{}
				g.Expect(testutil.K8sClient.List(
					testutil.Ctx, events, client.InNamespace(testNs))).To(Succeed())
				for _, ev := range events.Items {
					if ev.Reason == discovery.ReasonEngineRuntimeRenderFailed &&
						ev.LastTimestamp.After(resolved) {
						g.Expect(ev.Reason).To(Equal(discovery.ReasonUnaimedContainerPatch),
							"a render failure after the recreate resolved means the reference read a stale profile")
					}
				}
			}, 5*time.Second, targetingInterval).Should(Succeed())
		})
	})

	Context("scope ownership exemptions and coexistence", func() {
		It("duplicate profileName-only references render as on main (D6 exemption)", func() {
			// A pure compatibility reference owns no component, so two of
			// them to the same profile stay legal — exactly what main
			// accepted. Fragment injection is name-keyed and idempotent, and
			// the absent-component default stays leader-only: neither
			// reference reaches the worker template.
			profile := makeTargetingProfile("ik-dup")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-dup",
				workloadsv1alpha2.EngineRuntime{ProfileName: "ik-dup"},
				workloadsv1alpha2.EngineRuntime{ProfileName: "ik-dup"},
			)

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			Expect(hasVolume(pods[string(constants.LeaderComponentType)].Spec.Volumes, "ik-dup-vol")).
				To(BeTrue(), "leader: expected the profile volume rendered in the pod spec")
			Expect(hasVolume(pods[string(constants.WorkerComponentType)].Spec.Volumes, "ik-dup-vol")).
				To(BeFalse(), "worker: the absent-component default is leader-only even for duplicate references")

			By("verifying no engine-runtime event fires")
			Expect(noEngineRuntimeEvents()).To(BeTrue())
		})

		It("the disjoint mix (compat + component: worker) renders byte-identically to its all-scoped spelling", func() {
			// The one legal mix under leaderWorkerPattern: the compat
			// reference implicitly covers {leader} and component: worker
			// is disjoint from it, so both positions receive the
			// fragments — the same output two scoped references would
			// render.
			profile := makeTargetingProfile("ik-mixed")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-mixed",
				workloadsv1alpha2.EngineRuntime{ProfileName: "ik-mixed"},
				workloadsv1alpha2.EngineRuntime{
					ProfileName: "ik-mixed",
					Component:   string(constants.WorkerComponentType),
				},
			)

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			Expect(hasVolume(pods[string(constants.LeaderComponentType)].Spec.Volumes, "ik-mixed-vol")).
				To(BeTrue(), "leader: the compat reference's fragments reach the default component set")
			Expect(hasVolume(pods[string(constants.WorkerComponentType)].Spec.Volumes, "ik-mixed-vol")).
				To(BeTrue(), "worker: the component-scoped reference delivers here — the disjoint mix's own grant")

			By("verifying no engine-runtime event fires")
			Expect(noEngineRuntimeEvents()).To(BeTrue())
		})

		It("legacy containers merge and role-side patches coexist on one sidecar", func() {
			// §5.5 order: fragments and the legacy containers merge land
			// first, then the role-side patches — so all three env names
			// must be present on the rendered sidecar.
			profile := withSidecar(makeTargetingProfile("ik-coexist"), "ik-sidecar",
				corev1.EnvVar{Name: "FROM_PROFILE", Value: "profile"},
			)
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-coexist", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-coexist",
				Component:   string(constants.WorkerComponentType),
				Containers: []corev1.Container{{
					Name: "ik-sidecar",
					Env:  []corev1.EnvVar{{Name: "FROM_LEGACY", Value: "legacy"}},
				}},
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					roleAddOp("ik-sidecar", "/env/-", corev1.EnvVar{Name: "FROM_ROLE", Value: "role"}),
				},
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			sidecar := findContainer(pods[string(constants.WorkerComponentType)].Spec.Containers, "ik-sidecar")
			Expect(sidecar).NotTo(BeNil(), "worker: the profile sidecar must render in the named component")
			for _, name := range []string{"FROM_PROFILE", "FROM_LEGACY", "FROM_ROLE"} {
				_, ok := envValue(sidecar, name)
				Expect(ok).To(BeTrue(), "worker: env var %s not found on the sidecar", name)
			}

			By("verifying the coexistence stays inside the named component")
			Expect(findContainer(pods[string(constants.LeaderComponentType)].Spec.Containers, "ik-sidecar")).
				To(BeNil(), "leader: the fragments must not deliver outside the named component")

			By("verifying no warning event fires")
			Expect(hasEvent(discovery.ReasonUnaimedContainerPatch)).To(BeFalse())
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})

		It("the legacy containers merge appends a same-named override verbatim", func() {
			// The legacy merge keeps main's behavior exactly: a plain
			// append with no conflict check — the profile sidecar declares
			// FROM_PROFILE and the override appends the same name, so the
			// role's entry lands after the profile's and wins positionally.
			// The built-in env injector then dedupes by name preserving
			// the last occurrence (main's published pipeline), so the
			// rendered template carries one entry holding the role's
			// value — the override, silently. Legacy-only duplicates
			// never error.
			profile := withSidecar(makeTargetingProfile("ik-legacy-append"), "ik-sidecar",
				corev1.EnvVar{Name: "FROM_PROFILE", Value: "profile"},
			)
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-legacy-append", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-legacy-append",
				Component:   string(constants.WorkerComponentType),
				Containers: []corev1.Container{{
					Name: "ik-sidecar",
					Env:  []corev1.EnvVar{{Name: "FROM_PROFILE", Value: "role"}},
				}},
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			sidecar := findContainer(pods[string(constants.WorkerComponentType)].Spec.Containers, "ik-sidecar")
			Expect(sidecar).NotTo(BeNil(), "worker: the profile sidecar must render in the named component")
			idxs := envIndexes(sidecar, "FROM_PROFILE")
			Expect(idxs).To(HaveLen(1), "worker: the env injector dedupes the legacy same-name append to the last occurrence")
			Expect(sidecar.Env[idxs[0]].Value).To(Equal("role"),
				"worker: the override lands last, so the dedupe keeps the role's value")

			By("verifying the render succeeded — no render-failed event fires")
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})

		It("duplicate references append their legacy override once per reference", func() {
			// The exempt compatibility pair keeps main's duplicate-reference
			// output: pod-level delivery de-duplicates (one sidecar), the
			// legacy merge appends once per covering reference, and the
			// env injector's last-wins dedupe collapses the same-named
			// results to one entry — exactly what main rendered.
			profile := withSidecar(makeTargetingProfile("ik-legacy-dup"), "ik-sidecar")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			override := workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-legacy-dup",
				Containers: []corev1.Container{{
					Name: "ik-sidecar",
					Env:  []corev1.EnvVar{{Name: "SHARED", Value: "v"}},
				}},
			}
			rbg := leaderWorkerRBG("targeting-legacy-dup", override, override)

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			sidecars := 0
			for _, c := range pods[string(constants.LeaderComponentType)].Spec.Containers {
				if c.Name == "ik-sidecar" {
					sidecars++
				}
			}
			Expect(sidecars).To(Equal(1), "leader: pod-level delivery de-duplicates across references")
			sidecar := findContainer(pods[string(constants.LeaderComponentType)].Spec.Containers, "ik-sidecar")
			Expect(envIndexes(sidecar, "SHARED")).To(HaveLen(1),
				"leader: the legacy merge's once-per-reference appends collapse to one entry")

			By("verifying the render succeeded — no render-failed event fires")
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})

		It("D9 permitted direction: a role-side patch mounting the profile's own volume into the sidecar renders", func() {
			// The one-way bound blocks role content only. The profile's own
			// volume mounted into its own container is the profile
			// consuming profile content, so the render succeeds — the
			// guard must not over-refuse.
			profile := withSidecar(makeTargetingProfile("ik-d9-ok"), "ik-sidecar")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := leaderWorkerRBG("targeting-d9-ok", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-d9-ok",
				Component:   string(constants.WorkerComponentType),
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					roleAddOp("ik-sidecar", "/volumeMounts/-", corev1.VolumeMount{
						Name: "ik-d9-ok-vol", MountPath: "/opt/ik", ReadOnly: true,
					}),
				},
			})

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			sidecar := findContainer(pods[string(constants.WorkerComponentType)].Spec.Containers, "ik-sidecar")
			Expect(sidecar).NotTo(BeNil(), "worker: the profile sidecar must render in the named component")
			Expect(hasMountName(sidecar.VolumeMounts, "ik-d9-ok-vol")).
				To(BeTrue(), "worker: the profile's own volume may mount into its own container")

			By("verifying no render-failed event fires")
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})
	})

	Context("standalonePattern component vocabulary", func() {
		It("component: <role name> selects the standalone component and renders", func() {
			// The standalone component is named after the role (KEP-173),
			// so component: worker addresses it — fragments and the consented
			// patches both render into the single role-named component.
			profile := makeTargetingProfile("ik-standalone-ok",
				addOp("/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
			)
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			Expect(testutil.K8sClient.Create(testutil.Ctx, standaloneRBG("targeting-standalone-ok", "worker", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-standalone-ok",
				Component:   "worker",
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					{Name: "engine"},
				},
			}))).To(Succeed())

			// The standalone component is named after the role (KEP-173),
			// so component: worker addresses it — fragments and the consented
			// patches both render into the single role-named component.
			pods := rolePods("targeting-standalone-ok", "worker", 1)
			podSpec := pods["worker"].Spec
			Expect(hasVolume(podSpec.Volumes, "ik-standalone-ok-vol")).
				To(BeTrue(), "expected the profile volume in the rendered pod spec")
			engine := findContainer(podSpec.Containers, "engine")
			Expect(engine).NotTo(BeNil())
			val, ok := envValue(engine, "IK_SITE")
			Expect(ok).To(BeTrue(), "the consented patch must fire in the role-named component")
			Expect(val).To(Equal("/opt/ik/site"))

			By("verifying no engine-runtime event fires")
			Expect(noEngineRuntimeEvents()).To(BeTrue())
		})

		It("a mixed-case role name writes with component: <role name>", func() {
			// `component` carries no lexical pattern (RoleSpec.Name does not
			// either): a standalone role named MyRole writes today, so
			// component: MyRole must write, and the render vocabulary gate
			// must resolve it — the role-named component (KEP-173). Whether
			// the mixed-case name can name a child workload is a pre-existing
			// main property independent of engine runtimes, asserted here only
			// through its absence: no engine-runtime render failure fires,
			// because the component name resolves.
			profile := makeTargetingProfile("ik-mixedcase")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			Expect(testutil.K8sClient.Create(testutil.Ctx, standaloneRBG("targeting-mixedcase", "MyRole", workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-mixedcase",
				Component:   "MyRole",
			}))).To(Succeed())

			By("verifying the render vocabulary gate accepts the role-named component")
			Consistently(func(g Gomega) {
				g.Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse(),
					"component: MyRole must resolve — the standalone component is named after the role")
			}, 5*time.Second, targetingInterval).Should(Succeed())
		})
	})

	Context("application order and wrap shadowing (D10)", func() {
		It("a role-side wrap ahead of a profile-side one warns and still renders", func() {
			// D10: the profile-side pass applies before the role-side pass,
			// so what the role writes lands where it is written — its
			// /command/0 wrap lands ahead of the profile's. The render
			// succeeds, and an EngineRuntimeWrapShadowed warning event is
			// emitted so the shadowing is never silent. The report is
			// command-root-only: the container must declare its command for
			// a wrap to be addressable at all.
			profile := makeTargetingProfile("ik-shadow",
				addOp("/command/0", "profile-wrap"),
			)
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			role := leaderWorkerRole(workloadsv1alpha2.EngineRuntime{
				ProfileName: "ik-shadow",
				Component:   string(constants.WorkerComponentType),
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					roleAddOp("engine", "/command/0", "role-wrap"),
				},
			})
			role.LeaderWorkerPattern.Template.Spec.Containers[0].Command = []string{"python"}
			rbg := createLeaderWorkerRBG("targeting-shadow", role)

			pods := rolePods(rbg.Name, rbg.Spec.Roles[0].Name, 2)
			engine := findContainer(pods[string(constants.WorkerComponentType)].Spec.Containers, "engine")
			Expect(engine).NotTo(BeNil(), "worker: engine container not found")
			Expect(engine.Command).To(Equal([]string{"role-wrap", "profile-wrap", "python"}),
				"the role-side pass runs last, so its wrap lands outermost")

			By("verifying the wrap-shadowed warning event fires")
			Eventually(func(g Gomega) {
				g.Expect(hasEvent(discovery.ReasonEngineRuntimeWrapShadowed)).To(BeTrue())
			}, targetingTimeout, targetingInterval).Should(Succeed())

			By("verifying the render succeeded — no render-failed event fires")
			Expect(hasEvent(discovery.ReasonEngineRuntimeRenderFailed)).To(BeFalse())
		})
	})

})

// findInitContainer returns the named spec.initContainers member, if present.
func findInitContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

// envIndexes returns the positions of every env entry with the given name —
// the override-gesture material: a same-name append must land after, never
// replace, the existing entry.
func envIndexes(container *corev1.Container, name string) []int {
	var idxs []int
	for i := range container.Env {
		if container.Env[i].Name == name {
			idxs = append(idxs, i)
		}
	}
	return idxs
}

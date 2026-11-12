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

package v1alpha2

import (
	"os"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// The KEP-531 ecosystem driver scenarios: the engine-runtime delivery
// shapes the inference drivers (patio, oss-connector, sglang plugin,
// inference-kit) publish through ClusterEngineRuntimeProfiles. Fixtures and
// expected outcomes originate from the manual KEP-531 verification cases;
// only the observation moved from kubectl to ginkgo.
//
// They cannot run on the kind CI clusters: the carrier and engine images
// live in the private kube-ai-registry and the data-level scenario reads a
// real OSS bucket through real credentials. They register behind
// RBGS_E2E_ECOSYSTEM=1 instead — the registration-time gate means CI (and
// its dry-run matrix-coverage audit) never sees these specs, the same
// all-or-nothing posture as the RBGS_DRILLS gate. Within the set, the real
// OSS scenario needs one more opt-in, RBGS_E2E_ECOSYSTEM_DATA=1, plus its
// environment (defaults in the spec).

const (
	// ecosystemRegistry is the private registry hosting the carrier,
	// connector and engine images — what the kind CI clusters cannot pull.
	ecosystemRegistry = "kube-ai-registry.cn-shanghai.cr.aliyuncs.com/kube-ai-dev"

	ecosystemPythonImage = ecosystemRegistry + "/python:3.11-slim"
	ecosystemSglangImage = ecosystemRegistry + "/sglang:v0.5.21"

	// osscDataLeaderScript is case 65's leader command: read the connector's
	// virtual-mounted config and weights head/tail, print content hashes
	// with fetch timings, then stay alive.
	osscDataLeaderScript = `mkdir -p /models
# The connector master must stay in the entrypoint process (its daemon
# threads own the UDS listener), so python runs as a child instead of
# exec-ing over sh; the library flips it to slave role via env.
python -u - <<'PYEOF'
import hashlib, time
t = time.time()
d = open("/models/config.json", "rb").read()
print("CONFIG", len(d), hashlib.sha256(d).hexdigest(),
      "%.2fs" % (time.time() - t), flush=True)
t = time.time()
f = open("/models/model.safetensors-00001-of-00001.safetensors", "rb")
head = f.read(16 * 1024 * 1024)
f.seek(-16 * 1024 * 1024, 2)
tail = f.read(16 * 1024 * 1024)
f.close()
print("WEIGHTS", hashlib.sha256(head).hexdigest(),
      hashlib.sha256(tail).hexdigest(),
      "%.2fs" % (time.time() - t), flush=True)
import time as _t; _t.sleep(3600)
PYEOF`
)

// ecosystemSleeperArgs keeps the engine alive after render: the scenarios
// assert startup, never exit.
var ecosystemSleeperArgs = []string{"-c", "import time; time.sleep(3600)"}

// RunEngineRuntimeEcosystemTestCases registers the env-gated KEP-531
// ecosystem driver-scenario specs. CI never sets the gate, so nothing
// registers there; a private-registry cluster opts in with
// RBGS_E2E_ECOSYSTEM=1.
func RunEngineRuntimeEcosystemTestCases(f *framework.Framework) {
	if os.Getenv("RBGS_E2E_ECOSYSTEM") != "1" {
		return
	}
	ginkgo.Describe("engine runtime ecosystem driver scenarios (KEP-531)", ginkgo.Label("ecosystem"), func() {
		// The one-role leaderWorkerPattern fixture every scenario builds:
		// the shared engine container the profile patches address, optional
		// per-component template patches (the role's own divergent data),
		// and the engine runtime references.
		ecosystemLwpRbg := func(
			name string, engine corev1.Container,
			leaderPatch, workerPatch *runtime.RawExtension,
			runtimes ...workloadsv1alpha2.EngineRuntime,
		) *workloadsv1alpha2.RoleBasedGroup {
			role := wrappersv2.BuildLeaderWorkerRole("worker").Obj()
			tpl := role.LeaderWorkerPattern.Template
			tpl.Spec.Containers = []corev1.Container{engine}
			role.LeaderWorkerPattern.LeaderTemplatePatch = leaderPatch
			role.LeaderWorkerPattern.WorkerTemplatePatch = workerPatch
			role.EngineRuntimes = runtimes
			return wrappersv2.BuildBasicRoleBasedGroup(name, f.Namespace).
				WithRoles([]workloadsv1alpha2.RoleSpec{role}).
				Obj()
		}

		// engineEnvPatch is the role's per-component template patch shape:
		// env (and optionally a command override) on the engine container.
		engineEnvPatch := func(env []corev1.EnvVar, command []string) *runtime.RawExtension {
			container := map[string]any{"name": engineContainerName, "env": env}
			if command != nil {
				container["command"] = command
			}
			return &runtime.RawExtension{Raw: jsonPatchValue(map[string]any{
				"spec": map[string]any{"containers": []any{container}},
			}).Raw}
		}

		// broadcastReferences is the scenarios' reference shape: one
		// component-scoped reference per component (D1), each consenting at
		// the engine container; the shared entries' extraPatches carry the
		// role-side rules, if any.
		broadcastReferences := func(
			profileName string, extraPatches ...workloadsv1alpha2.ContainerPatch,
		) []workloadsv1alpha2.EngineRuntime {
			entry := workloadsv1alpha2.EngineRuntimeTargetContainer{
				Name: engineContainerName, ExtraPatches: extraPatches,
			}
			return []workloadsv1alpha2.EngineRuntime{
				{
					ProfileName: profileName, Component: string(constants.LeaderComponentType),
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{entry},
				},
				{
					ProfileName: profileName, Component: string(constants.WorkerComponentType),
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{entry},
				},
			}
		}

		// assertBroadcastRender asserts the shared render shape once —
		// injected initContainers, volumes, engine mounts and env, in both
		// components — and returns the two templates for the scenario's own
		// divergences.
		assertBroadcastRender := func(
			rbgName string, initContainers, volumes []string,
			mounts, env map[string]string,
		) (leader, worker *corev1.PodTemplateSpec) {
			gomega.Eventually(func(g gomega.Gomega) {
				var err error
				leader, err = engineRuntimeComponent(f, rbgName, string(constants.LeaderComponentType))
				g.Expect(err).ToNot(gomega.HaveOccurred())
				worker, err = engineRuntimeComponent(f, rbgName, string(constants.WorkerComponentType))
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}, utils.Timeout, utils.Interval).Should(gomega.Succeed(), "%s never rendered", rbgName)

			for _, tpl := range []*corev1.PodTemplateSpec{leader, worker} {
				for _, name := range initContainers {
					found := false
					for i := range tpl.Spec.InitContainers {
						if tpl.Spec.InitContainers[i].Name == name {
							found = true
						}
					}
					gomega.Expect(found).To(gomega.BeTrue(),
						"%s: the %s initContainer must be injected", rbgName, name)
				}
				for _, name := range volumes {
					gomega.Expect(engineRuntimeFindVolume(tpl, name)).To(gomega.BeTrue(),
						"%s: the %s volume must be injected", rbgName, name)
				}
				engine := engineRuntimeFindContainer(tpl, engineContainerName)
				for volume, mountPath := range mounts {
					mount := engineRuntimeFindMount(engine, volume)
					gomega.Expect(mount).ToNot(gomega.BeNil(),
						"%s: the %s volume must be mounted on the engine", rbgName, volume)
					gomega.Expect(mount.MountPath).To(gomega.Equal(mountPath))
				}
				for name, value := range env {
					v, found := engineRuntimeFindEnv(engine, name)
					gomega.Expect(found).To(gomega.BeTrue(), "%s: env %s missing", rbgName, name)
					gomega.Expect(v).To(gomega.Equal(value))
				}
			}
			return leader, worker
		}

		// ecosystemPod returns the component's pod of the one-role fixture:
		// exactly one per component at size 2, identified through the
		// controller's own labels rather than a name scheme.
		ecosystemPod := func(rbgName string, component constants.ComponentType) *corev1.Pod {
			pods := &corev1.PodList{}
			gomega.Eventually(func(g gomega.Gomega) int {
				g.Expect(f.Client.List(f.Ctx, pods,
					client.InNamespace(f.Namespace),
					client.MatchingLabels{
						constants.GroupNameLabelKey:     rbgName,
						constants.RoleNameLabelKey:      "worker",
						constants.ComponentNameLabelKey: string(component),
					})).To(gomega.Succeed())
				return len(pods.Items)
			}, utils.Timeout, utils.Interval).Should(gomega.Equal(1),
				"exactly one %s pod of %s must exist", component, rbgName)
			return pods.Items[0].DeepCopy()
		}

		expectInitContainerCompleted := func(pod *corev1.Pod, name string) {
			gomega.Eventually(func(g gomega.Gomega) bool {
				fresh := &corev1.Pod{}
				g.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(pod), fresh)).To(gomega.Succeed())
				for i := range fresh.Status.InitContainerStatuses {
					s := fresh.Status.InitContainerStatuses[i]
					if s.Name == name {
						return s.State.Terminated != nil && s.State.Terminated.ExitCode == 0
					}
				}
				return false
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"the %s initContainer of pod %s must complete successfully", name, pod.Name)
		}

		// expectPodLog waits until the pod's log contains want. The engine is
		// every fixture's first (and usually only) container, which is what
		// the log reader defaults to.
		expectPodLog := func(pod *corev1.Pod, want string) {
			gomega.Eventually(func(g gomega.Gomega) string {
				logs, err := f.GetPodLogs(pod.Namespace, pod.Name)
				g.Expect(err).ToNot(gomega.HaveOccurred())
				return logs
			}, utils.Timeout, utils.Interval).Should(gomega.ContainSubstring(want))
		}

		ginkgo.It("delivers the patio sidecar to the leader component only", func() {
			profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{
				ObjectMeta: metav1.ObjectMeta{Name: "kep531-ecosystem-patio"},
				Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
					UpdateStrategy: workloadsv1alpha2.NoUpdateStrategy,
					Containers: []corev1.Container{{
						Name:            "patio-agent",
						Image:           ecosystemRegistry + "/inference-engine-runtime:c6bddfc",
						ImagePullPolicy: corev1.PullIfNotPresent,
						VolumeMounts:    []corev1.VolumeMount{{Name: "patio-sock", MountPath: "/tmp/patio-sock"}},
					}},
					Volumes: []corev1.Volume{{
						Name: "patio-sock", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					}},
				},
			}
			gomega.Expect(f.Client.Create(f.Ctx, profile)).To(gomega.Succeed())

			rbg := ecosystemLwpRbg("ecosystem-patio",
				corev1.Container{
					Name:            engineContainerName,
					Image:           ecosystemPythonImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{"python"},
					Args:            ecosystemSleeperArgs,
				},
				nil, nil,
				workloadsv1alpha2.EngineRuntime{
					ProfileName: profile.Name, Component: string(constants.LeaderComponentType),
				})
			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			leader, err := engineRuntimeComponent(f, rbg.Name, string(constants.LeaderComponentType))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			worker, err := engineRuntimeComponent(f, rbg.Name, string(constants.WorkerComponentType))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(engineRuntimeFindContainer(leader, "patio-agent")).ToNot(gomega.BeNil(),
				"the fragments must render into the leader component")
			gomega.Expect(engineRuntimeFindVolume(leader, "patio-sock")).To(gomega.BeTrue())
			gomega.Expect(engineRuntimeFindContainer(worker, "patio-agent")).To(gomega.BeNil(),
				"the worker component must render byte-identically to its own template")
			gomega.Expect(engineRuntimeFindVolume(worker, "patio-sock")).To(gomega.BeFalse())

			// The sidecar reaches Running in the leader pod; the worker pod
			// has no patio container at all (the render assertion above).
			leaderPod := ecosystemPod(rbg.Name, constants.LeaderComponentType)
			gomega.Eventually(func(g gomega.Gomega) bool {
				fresh := &corev1.Pod{}
				g.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(leaderPod), fresh)).To(gomega.Succeed())
				for i := range fresh.Status.ContainerStatuses {
					if fresh.Status.ContainerStatuses[i].Name == "patio-agent" {
						return fresh.Status.ContainerStatuses[i].State.Running != nil
					}
				}
				return false
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"the patio-agent sidecar must reach Running in the leader pod")

			// The profile declares no containerPatches and the reference no
			// targetContainers, so no warning event may fire.
			gomega.Consistently(func() bool {
				found, _ := engineRuntimeEvent(f, rbg.Name, "UnaimedContainerPatch")
				return !found
			}, 15*time.Second, utils.Interval).Should(gomega.BeTrue(),
				"a fragments-only reference must not warn")
		})

		ginkgo.It("interposes the oss-connector library on both components (transport level)", func() {
			profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{
				ObjectMeta: metav1.ObjectMeta{Name: "kep531-ecosystem-ossc"},
				Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
					UpdateStrategy: workloadsv1alpha2.NoUpdateStrategy,
					Volumes: []corev1.Volume{{
						Name: "oss-connector-lib", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					}},
					InitContainers: []corev1.Container{{
						Name:  "oss-connector-install",
						Image: ecosystemRegistry + "/oss-connector:1.2.0",
						Command: []string{"sh", "-c",
							"mkdir -p /opt/ossc/lib64 && cp /usr/local/lib/libossc_preload.so /opt/ossc/lib64/ && " +
								"cp /etc/oss-connector/config.json /opt/ossc/config.json"},
						VolumeMounts: []corev1.VolumeMount{{Name: "oss-connector-lib", MountPath: "/opt/ossc"}},
					}},
					ContainerPatches: []workloadsv1alpha2.ContainerPatch{
						erPatch("add", "/volumeMounts/-", corev1.VolumeMount{
							Name: "oss-connector-lib", MountPath: "/opt/ossc", ReadOnly: true,
						}),
						erPatch("add", "/env/-", corev1.EnvVar{
							Name: "LD_PRELOAD", Value: "/opt/ossc/lib64/libossc_preload.so",
						}),
						erPatch("add", "/env/-", corev1.EnvVar{
							Name: "CONNECTOR_CONFIG_PATH", Value: "/opt/ossc/config.json",
						}),
					},
				},
			}
			gomega.Expect(f.Client.Create(f.Ctx, profile)).To(gomega.Succeed())

			// The connector's launch validation requires all four startup
			// placeholders; without credentials the library loads and stays
			// inert — this scenario asserts render and startup only, never
			// real OSS data (that is the data-level spec below).
			rbg := ecosystemLwpRbg("ecosystem-ossc",
				corev1.Container{
					Name:    engineContainerName,
					Image:   ecosystemSglangImage,
					Command: []string{"python"},
					Args:    ecosystemSleeperArgs,
					Env: []corev1.EnvVar{
						{Name: "MODEL_DIR", Value: "/tmp/models"},
						{Name: "OSS_PATH", Value: "oss://placeholder-bucket/placeholder"},
						{Name: "OSS_ENDPOINT", Value: "oss-cn-shanghai.aliyuncs.com"},
						{Name: "OSS_REGION", Value: "cn-shanghai"},
					},
				},
				// ENABLE_CONNECTOR diverges per component: the leader's
				// master loads from OSS and distributes, the worker receives
				// from the master.
				engineEnvPatch([]corev1.EnvVar{{Name: "ENABLE_CONNECTOR", Value: "1"}}, nil),
				engineEnvPatch([]corev1.EnvVar{{Name: "ENABLE_CONNECTOR", Value: "2"}}, nil),
				broadcastReferences(profile.Name)...)
			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			leader, worker := assertBroadcastRender(rbg.Name,
				[]string{"oss-connector-install"},
				[]string{"oss-connector-lib"},
				map[string]string{"oss-connector-lib": "/opt/ossc"},
				map[string]string{
					"LD_PRELOAD":            "/opt/ossc/lib64/libossc_preload.so",
					"CONNECTOR_CONFIG_PATH": "/opt/ossc/config.json",
				})
			leaderEnv, _ := engineRuntimeFindEnv(engineRuntimeFindContainer(leader, engineContainerName), "ENABLE_CONNECTOR")
			workerEnv, _ := engineRuntimeFindEnv(engineRuntimeFindContainer(worker, engineContainerName), "ENABLE_CONNECTOR")
			gomega.Expect(leaderEnv).To(gomega.Equal("1"))
			gomega.Expect(workerEnv).To(gomega.Equal("2"))

			expectInitContainerCompleted(ecosystemPod(rbg.Name, constants.LeaderComponentType), "oss-connector-install")
			expectInitContainerCompleted(ecosystemPod(rbg.Name, constants.WorkerComponentType), "oss-connector-install")
		})

		ginkgo.It("installs and discovers the sglang plugin wheel in both components", func() {
			profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{
				ObjectMeta: metav1.ObjectMeta{Name: "kep531-ecosystem-sglang"},
				Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
					UpdateStrategy: workloadsv1alpha2.NoUpdateStrategy,
					Volumes: []corev1.Volume{
						{Name: "sglang-plugins", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "sglang-vit-wheel", VolumeSource: corev1.VolumeSource{
							Image: &corev1.ImageVolumeSource{
								Reference:  ecosystemRegistry + "/sglang-vit-batch-plugin:0.1.0",
								PullPolicy: corev1.PullIfNotPresent,
							},
						}},
					},
					InitContainers: []corev1.Container{{
						Name:  "sglang-plugin-install",
						Image: ecosystemPythonImage,
						Command: []string{"pip", "install", "--no-deps", "-t", "/plugins",
							"/carrier/wheel/sglang_vit_batch_plugin-0.1.0-py3-none-any.whl"},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "sglang-plugins", MountPath: "/plugins"},
							{Name: "sglang-vit-wheel", MountPath: "/carrier", ReadOnly: true},
						},
					}},
					ContainerPatches: []workloadsv1alpha2.ContainerPatch{
						erPatch("add", "/volumeMounts/-", corev1.VolumeMount{
							Name: "sglang-plugins", MountPath: "/plugins", ReadOnly: true,
						}),
						erPatch("add", "/env/-", corev1.EnvVar{Name: "PYTHONPATH", Value: "/plugins"}),
					},
				},
			}
			gomega.Expect(f.Client.Create(f.Ctx, profile)).To(gomega.Succeed())

			// The role's only own data is the plugin's A/B gate env,
			// diverging per component. The engine's argv performs the
			// discovery check the case expects — entry-point metadata
			// resolved along the mounted PYTHONPATH — then sleeps.
			rbg := ecosystemLwpRbg("ecosystem-sglang",
				corev1.Container{
					Name:    engineContainerName,
					Image:   ecosystemSglangImage,
					Command: []string{"python"},
					Args: []string{"-c", "import importlib.metadata as m; " +
						"print([e.name for e in m.entry_points(group='sglang.srt.plugins')], flush=True); " +
						"import time; time.sleep(3600)"},
				},
				engineEnvPatch([]corev1.EnvVar{{Name: "SGLANG_VIT_CROSS_REQ_BATCH", Value: "1"}}, nil),
				engineEnvPatch([]corev1.EnvVar{{Name: "SGLANG_VIT_CROSS_REQ_BATCH", Value: "2"}}, nil),
				broadcastReferences(profile.Name)...)
			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			leader, worker := assertBroadcastRender(rbg.Name,
				[]string{"sglang-plugin-install"},
				[]string{"sglang-plugins", "sglang-vit-wheel"},
				map[string]string{"sglang-plugins": "/plugins"},
				map[string]string{"PYTHONPATH": "/plugins"})
			leaderGate, _ := engineRuntimeFindEnv(engineRuntimeFindContainer(leader, engineContainerName), "SGLANG_VIT_CROSS_REQ_BATCH")
			workerGate, _ := engineRuntimeFindEnv(engineRuntimeFindContainer(worker, engineContainerName), "SGLANG_VIT_CROSS_REQ_BATCH")
			gomega.Expect(leaderGate).To(gomega.Equal("1"))
			gomega.Expect(workerGate).To(gomega.Equal("2"))

			expectInitContainerCompleted(ecosystemPod(rbg.Name, constants.LeaderComponentType), "sglang-plugin-install")
			expectInitContainerCompleted(ecosystemPod(rbg.Name, constants.WorkerComponentType), "sglang-plugin-install")

			expectPodLog(ecosystemPod(rbg.Name, constants.LeaderComponentType), "vit_cross_request_batch")
			expectPodLog(ecosystemPod(rbg.Name, constants.WorkerComponentType), "vit_cross_request_batch")
		})

		ginkgo.It("wraps the engine argv with the inference-kit launcher in both components", func() {
			profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{
				ObjectMeta: metav1.ObjectMeta{Name: "kep531-ecosystem-ik"},
				Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
					// RollingUpdate is accepted but never consumed in this
					// feature; a changed profile reaches running pods only
					// through a workload update or re-render.
					UpdateStrategy: workloadsv1alpha2.RollingUpdateStrategy,
					Volumes: []corev1.Volume{
						{Name: "inference-kit", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "inference-kit-wheel", VolumeSource: corev1.VolumeSource{
							Image: &corev1.ImageVolumeSource{
								Reference:  ecosystemRegistry + "/inference-kit:sha-5dd19905cfd3acf211b9ce127ac4531b1bfd78a0",
								PullPolicy: corev1.PullIfNotPresent,
							},
						}},
					},
					InitContainers: []corev1.Container{{
						Name:  "inference-kit-install",
						Image: ecosystemPythonImage,
						Command: []string{"pip", "install", "--no-deps", "-t", "/opt/inference-kit",
							"/carrier/wheel/inference_kit-0.1.0-py3-none-any.whl"},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "inference-kit", MountPath: "/opt/inference-kit"},
							{Name: "inference-kit-wheel", MountPath: "/carrier", ReadOnly: true},
						},
					}},
					// Head-inserts stack with the later rule landing leftmost,
					// hence the reverse-order listing: the engine command
					// [python, -c, …] becomes [python, -m, inference_kit.cli,
					// python, -c, …], so the interpreter exec's the launcher
					// with the original argv untouched. The launcher passes
					// unrecognized commands through byte-identically, so the
					// plain sleeper keeps the wrapped engine alive.
					ContainerPatches: []workloadsv1alpha2.ContainerPatch{
						erPatch("add", "/volumeMounts/-", corev1.VolumeMount{
							Name: "inference-kit", MountPath: "/opt/inference-kit", ReadOnly: true,
						}),
						erPatch("add", "/env/-", corev1.EnvVar{Name: "PYTHONPATH", Value: "/opt/inference-kit"}),
						erPatch("add", "/command/0", "inference_kit.cli"),
						erPatch("add", "/command/0", "-m"),
						erPatch("add", "/command/0", "python"),
					},
				},
			}
			gomega.Expect(f.Client.Create(f.Ctx, profile)).To(gomega.Succeed())

			// The role's only own data is IK_ALL=0 (feature level 0 =
			// pass-through) and the empty capabilities list the role-side
			// SYS_RESOURCE append lands in — capabilities are a
			// role-side-only root (D11), so the append rides the entries'
			// extraPatches, applied after the profile's patches.
			rbg := ecosystemLwpRbg("ecosystem-ik",
				corev1.Container{
					Name:            engineContainerName,
					Image:           ecosystemSglangImage,
					Command:         []string{"python"},
					Args:            ecosystemSleeperArgs,
					Env:             []corev1.EnvVar{{Name: "IK_ALL", Value: "0"}},
					SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{}}},
				},
				nil, nil,
				broadcastReferences(profile.Name,
					erPatch("add", "/securityContext/capabilities/add/-", "SYS_RESOURCE"))...)
			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			leader, worker := assertBroadcastRender(rbg.Name,
				[]string{"inference-kit-install"},
				[]string{"inference-kit", "inference-kit-wheel"},
				map[string]string{"inference-kit": "/opt/inference-kit"},
				map[string]string{"PYTHONPATH": "/opt/inference-kit"})
			for _, tpl := range []*corev1.PodTemplateSpec{leader, worker} {
				engine := engineRuntimeFindContainer(tpl, engineContainerName)
				gomega.Expect(engine.Command).To(gomega.Equal(
					[]string{"python", "-m", "inference_kit.cli", "python"}),
					"the launcher wrap must head-insert in reverse order")
				gomega.Expect(engine.Args).To(gomega.Equal(ecosystemSleeperArgs),
					"the original argv must stay untouched after the wrap")
				gomega.Expect(engine.SecurityContext.Capabilities.Add).To(gomega.ContainElement(
					corev1.Capability("SYS_RESOURCE")))
			}

			expectInitContainerCompleted(ecosystemPod(rbg.Name, constants.LeaderComponentType), "inference-kit-install")
			expectInitContainerCompleted(ecosystemPod(rbg.Name, constants.WorkerComponentType), "inference-kit-install")
		})

		ginkgo.It("reads real model bytes through the connector from OSS", func() {
			if os.Getenv("RBGS_E2E_ECOSYSTEM_DATA") != "1" {
				ginkgo.Skip("requires RBGS_E2E_ECOSYSTEM_DATA=1: reads a real OSS bucket through real credentials")
			}
			ossPath := envOrDefault("RBGS_E2E_OSS_PATH", "oss://ik-models/Qwen/Qwen3.5-0.8B/")
			ossEndpoint := envOrDefault("RBGS_E2E_OSS_ENDPOINT", "http://oss-cn-shanghai-internal.aliyuncs.com")
			ossRegion := envOrDefault("RBGS_E2E_OSS_REGION", "cn-shanghai")
			ossSecret := envOrDefault("RBGS_E2E_OSS_SECRET", "oss-chubo")

			// The profile is byte-identical to the transport scenario's.
			profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{
				ObjectMeta: metav1.ObjectMeta{Name: "kep531-ecosystem-ossc-data"},
				Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
					UpdateStrategy: workloadsv1alpha2.NoUpdateStrategy,
					Volumes: []corev1.Volume{{
						Name: "oss-connector-lib", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					}},
					InitContainers: []corev1.Container{{
						Name:  "oss-connector-install",
						Image: ecosystemRegistry + "/oss-connector:1.2.0",
						Command: []string{"sh", "-c",
							"mkdir -p /opt/ossc/lib64 && cp /usr/local/lib/libossc_preload.so /opt/ossc/lib64/ && " +
								"cp /etc/oss-connector/config.json /opt/ossc/config.json"},
						VolumeMounts: []corev1.VolumeMount{{Name: "oss-connector-lib", MountPath: "/opt/ossc"}},
					}},
					ContainerPatches: []workloadsv1alpha2.ContainerPatch{
						erPatch("add", "/volumeMounts/-", corev1.VolumeMount{
							Name: "oss-connector-lib", MountPath: "/opt/ossc", ReadOnly: true,
						}),
						erPatch("add", "/env/-", corev1.EnvVar{
							Name: "LD_PRELOAD", Value: "/opt/ossc/lib64/libossc_preload.so",
						}),
						erPatch("add", "/env/-", corev1.EnvVar{
							Name: "CONNECTOR_CONFIG_PATH", Value: "/opt/ossc/config.json",
						}),
					},
				},
			}
			gomega.Expect(f.Client.Create(f.Ctx, profile)).To(gomega.Succeed())

			// Everything real belongs to the role's own templates, per the
			// zero-workload-data rule: MODEL_DIR (the connector's virtual
			// mount point), OSS_PATH, OSS_ENDPOINT, OSS_REGION and the
			// credentials via secretKeyRef — never plaintext. The leader
			// divergence overrides the engine's command with the hash script;
			// the worker stays a plain sleeper, since receiving from the
			// master is the worker's data tier, not this scenario's
			// assertion.
			secretEnv := func(name, key string) corev1.EnvVar {
				return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: ossSecret}, Key: key,
					},
				}}
			}
			rbg := ecosystemLwpRbg("ecosystem-ossc-data",
				corev1.Container{
					Name:    engineContainerName,
					Image:   ecosystemPythonImage,
					Command: []string{"python"},
					Args:    ecosystemSleeperArgs,
					Env: []corev1.EnvVar{
						{Name: "MODEL_DIR", Value: "/models"},
						{Name: "OSS_PATH", Value: ossPath},
						{Name: "OSS_ENDPOINT", Value: ossEndpoint},
						{Name: "OSS_REGION", Value: ossRegion},
						secretEnv("OSS_ACCESS_KEY_ID", "akId"),
						secretEnv("OSS_ACCESS_KEY_SECRET", "akSecret"),
					},
				},
				engineEnvPatch([]corev1.EnvVar{{Name: "ENABLE_CONNECTOR", Value: "1"}},
					[]string{"sh", "-c", osscDataLeaderScript}),
				engineEnvPatch([]corev1.EnvVar{{Name: "ENABLE_CONNECTOR", Value: "2"}}, nil),
				broadcastReferences(profile.Name)...)
			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			assertBroadcastRender(rbg.Name,
				[]string{"oss-connector-install"},
				[]string{"oss-connector-lib"},
				map[string]string{"oss-connector-lib": "/opt/ossc"},
				map[string]string{
					"LD_PRELOAD":            "/opt/ossc/lib64/libossc_preload.so",
					"CONNECTOR_CONFIG_PATH": "/opt/ossc/config.json",
				})

			expectInitContainerCompleted(ecosystemPod(rbg.Name, constants.LeaderComponentType), "oss-connector-install")
			expectInitContainerCompleted(ecosystemPod(rbg.Name, constants.WorkerComponentType), "oss-connector-install")

			// The leader's engine prints the content hashes of the objects
			// the connector fetched from OSS; presence and timing are the
			// assertion — matching them against the bucket is the
			// operator's manual check, never the suite's.
			expectPodLog(ecosystemPod(rbg.Name, constants.LeaderComponentType), "CONFIG")
			expectPodLog(ecosystemPod(rbg.Name, constants.LeaderComponentType), "WEIGHTS")
		})
	})
}

// envOrDefault reads an environment variable with a fallback — the data
// scenario's configuration knobs.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

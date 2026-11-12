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
	"encoding/json"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// The KEP-531 engine runtime render semantics, as steady-state e2e specs.
// Every fixture asserts on the rendered child RoleInstanceSet templates,
// the warning events on the RBG, and the pods actually running, with
// images limited to the registries the kind CI clusters can pull. The
// ecosystem driver-scenario fixtures (private-registry images, real OSS
// data) live in engine_runtime_ecosystem.go behind RBGS_E2E_ECOSYSTEM=1.

const (
	// engineRuntimeImage runs a shell and sleep: the fixtures need a
	// declared command (the /command root's precondition) that keeps the
	// pod alive after the patch passes transform the argv. The suite's
	// default image (acs-sample nginx) carries sh and sleep and is pullable
	// by both the kind CI clusters and the China-region clusters the manual
	// runs use.
	engineRuntimeImage = utils.DefaultImage

	engineContainerName = "engine"
	roleEnvMarker       = "PYTHONPATH"
)

// jsonPatchValue marshals v once, mirroring how the apiserver delivers
// authored YAML inside RawExtension.
func jsonPatchValue(v any) runtime.RawExtension {
	raw, err := json.Marshal(v)
	gomega.ExpectWithOffset(1, err).ToNot(gomega.HaveOccurred())
	return runtime.RawExtension{Raw: raw}
}

// erPatch builds one profile-side or extraPatches entry.
func erPatch(op, path string, value any) workloadsv1alpha2.ContainerPatch {
	return workloadsv1alpha2.ContainerPatch{
		JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{
			Op: op, Path: path, Value: jsonPatchValue(value),
		},
	}
}

// engineRuntimeProfile builds a ClusterEngineRuntimeProfile with optional
// fragment containers, volumes and profile-side container patches.
func engineRuntimeProfile(
	name string,
	containers []corev1.Container,
	volumes []corev1.Volume,
	patches []workloadsv1alpha2.ContainerPatch,
) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
	return &workloadsv1alpha2.ClusterEngineRuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
			UpdateStrategy:   workloadsv1alpha2.NoUpdateStrategy,
			Containers:       containers,
			Volumes:          volumes,
			ContainerPatches: patches,
		},
	}
}

// sleeperContainer builds a busybox sleeper — the fragment container shape.
func sleeperContainer(name string) corev1.Container {
	return corev1.Container{
		Name:            name,
		Image:           engineRuntimeImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sh", "-c", "sleep 3600"},
	}
}

// engineRuntimeLwpRbg builds a one-role leaderWorkerPattern RBG whose shared
// template declares the busybox engine container the patches target, with
// the PYTHONPATH marker env the assertions read back.
func engineRuntimeLwpRbg(
	namespace, name string, runtimes ...workloadsv1alpha2.EngineRuntime,
) *workloadsv1alpha2.RoleBasedGroup {
	role := wrappersv2.BuildLeaderWorkerRole("worker").Obj()
	tpl := role.LeaderWorkerPattern.Template
	tpl.Spec.Containers = []corev1.Container{{
		Name:            engineContainerName,
		Image:           engineRuntimeImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sh"},
		Args:            []string{"-c", "sleep 3600"},
		Env:             []corev1.EnvVar{{Name: roleEnvMarker, Value: "/usr/lib/python"}},
	}}
	role.EngineRuntimes = runtimes
	return wrappersv2.BuildBasicRoleBasedGroup(name, namespace).
		WithRoles([]workloadsv1alpha2.RoleSpec{role}).
		Obj()
}

// engineRuntimeStandaloneRbg builds a one-role standalonePattern RBG. The
// role's own name is the rendered component's name; no workload-type
// annotation — the RoleInstanceSet default routes through the current
// surface.
func engineRuntimeStandaloneRbg(
	namespace, name string, runtimes ...workloadsv1alpha2.EngineRuntime,
) *workloadsv1alpha2.RoleBasedGroup {
	role := wrappersv2.BuildStandaloneRole("solo").Obj()
	role.StandalonePattern.Template.Spec.Containers = []corev1.Container{{
		Name:            engineContainerName,
		Image:           engineRuntimeImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sh", "-c", "sleep 3600"},
		Env:             []corev1.EnvVar{{Name: roleEnvMarker, Value: "/usr/lib/python"}},
	}}
	role.EngineRuntimes = runtimes
	return wrappersv2.BuildBasicRoleBasedGroup(name, namespace).
		WithRoles([]workloadsv1alpha2.RoleSpec{role}).
		Obj()
}

// engineRuntimeComponent reads one component's rendered pod template from
// the child RoleInstanceSet of the RBG's single role.
func engineRuntimeComponent(
	f *framework.Framework, rbgName, component string,
) (*corev1.PodTemplateSpec, error) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Namespace: f.Namespace, Name: rbgName}, rbg); err != nil {
		return nil, err
	}
	ris := &workloadsv1alpha2.RoleInstanceSet{}
	key := client.ObjectKey{Namespace: f.Namespace, Name: rbg.GetWorkloadName(&rbg.Spec.Roles[0])}
	if err := f.Client.Get(f.Ctx, key, ris); err != nil {
		return nil, err
	}
	for i := range ris.Spec.RoleInstanceTemplate.Components {
		c := &ris.Spec.RoleInstanceTemplate.Components[i]
		if c.Name == component {
			return &c.Template, nil
		}
	}
	return nil, fmt.Errorf("%s has no %s component rendered", rbgName, component)
}

// engineRuntimeEvent reports whether a warning event with the reason was
// recorded for the object, and its message. Events repeat on every reconcile
// by design, so presence — never a count — is the assertion surface.
func engineRuntimeEvent(f *framework.Framework, objName, reason string) (bool, string) {
	events := &corev1.EventList{}
	if err := f.Client.List(f.Ctx, events, client.InNamespace(f.Namespace)); err != nil {
		return false, ""
	}
	for i := range events.Items {
		ev := &events.Items[i]
		if ev.InvolvedObject.Name == objName && ev.Reason == reason {
			return true, ev.Message
		}
	}
	return false, ""
}

// engineRuntimeCleanupProfile defers deletion of a cluster-scoped profile.
// DeferCleanup callbacks run after the framework AfterEach has removed the
// RBGs, so the references are gone and a frozen profile has unfrozen by
// then; the controller may take a moment to drop ReferenceCount, hence the
// retry.
func engineRuntimeCleanupProfile(f *framework.Framework, name string) {
	ginkgo.DeferCleanup(func() {
		profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
		gomega.Eventually(func() bool {
			err := f.Client.Delete(f.Ctx, profile)
			return err == nil || apierrors.IsNotFound(err)
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
			"profile %s must be deletable once its references are gone", name)
	})
}

func engineRuntimeFindEnv(c *corev1.Container, name string) (string, bool) {
	for _, ev := range c.Env {
		if ev.Name == name {
			return ev.Value, true
		}
	}
	return "", false
}

func engineRuntimeEnvIndex(c *corev1.Container, name string) (int, bool) {
	for i, ev := range c.Env {
		if ev.Name == name {
			return i, true
		}
	}
	return -1, false
}

func engineRuntimeFindMount(c *corev1.Container, name string) *corev1.VolumeMount {
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Name == name {
			return &c.VolumeMounts[i]
		}
	}
	return nil
}

func engineRuntimeFindVolume(tpl *corev1.PodTemplateSpec, name string) bool {
	for i := range tpl.Spec.Volumes {
		if tpl.Spec.Volumes[i].Name == name {
			return true
		}
	}
	return false
}

func engineRuntimeFindContainer(tpl *corev1.PodTemplateSpec, name string) *corev1.Container {
	for i := range tpl.Spec.Containers {
		if tpl.Spec.Containers[i].Name == name {
			return &tpl.Spec.Containers[i]
		}
	}
	return nil
}

// RunEngineRuntimeTestCases registers the KEP-531 render-semantics e2e
// specs: the consent model, the five-root patch grammar, render failure,
// multi-profile stacking determinism, split delivery and profile
// immutability.
func RunEngineRuntimeTestCases(f *framework.Framework) {
	ginkgo.Describe("engine runtime profile injection (KEP-531)", ginkgo.Label("feature"), func() {
		ginkgo.It("renders the full five-root bundle into the consented component only", func() {
			profile := engineRuntimeProfile("kep531-e2e-full",
				[]corev1.Container{sleeperContainer("ik-sidecar")},
				[]corev1.Volume{{Name: "ik-full-vol", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				[]workloadsv1alpha2.ContainerPatch{
					erPatch("add", "/command/0", "env"),
					erPatch("add", "/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
					erPatch("add", "/volumeMounts/-", corev1.VolumeMount{
						Name: "ik-full-vol", MountPath: "/opt/ik", ReadOnly: true,
					}),
				})
			gomega.Expect(f.Client.Create(f.Ctx, profile)).To(gomega.Succeed())
			engineRuntimeCleanupProfile(f, profile.Name)

			rbg := engineRuntimeLwpRbg(f.Namespace, "kep531-full", workloadsv1alpha2.EngineRuntime{
				ProfileName: profile.Name,
				Component:   string(constants.WorkerComponentType),
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{
					Name: engineContainerName,
					ExtraPatches: []workloadsv1alpha2.ContainerPatch{
						erPatch("add", "/args/0", "-u"),
						erPatch("add", "/securityContext/capabilities/add/-", "SYS_NICE"),
					},
				}},
			})
			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			var worker *corev1.PodTemplateSpec
			gomega.Eventually(func(g gomega.Gomega) {
				var err error
				worker, err = engineRuntimeComponent(f, rbg.Name, string(constants.WorkerComponentType))
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}, utils.Timeout, utils.Interval).Should(gomega.Succeed())

			// The fragments: the profile's sidecar and volume landed in the
			// worker component.
			gomega.Expect(engineRuntimeFindContainer(worker, "ik-sidecar")).ToNot(gomega.BeNil(),
				"the profile's sidecar container must render into the referenced component")
			gomega.Expect(engineRuntimeFindVolume(worker, "ik-full-vol")).To(gomega.BeTrue(),
				"the profile's volume must render into the referenced component")

			// The profile-side patches, in list order: command head-insert,
			// env append, volumeMount append.
			engine := engineRuntimeFindContainer(worker, engineContainerName)
			gomega.Expect(engine.Command).To(gomega.Equal([]string{"env", "sh"}),
				"the /command/0 head-insert must land leftmost")
			gomega.Expect(engine.Args).To(gomega.Equal([]string{"-u", "-c", "sleep 3600"}),
				"the entry's /args/0 head-insert must land before the role's args")
			value, found := engineRuntimeFindEnv(engine, "IK_SITE")
			gomega.Expect(found).To(gomega.BeTrue(), "the profile's /env/- append must land")
			gomega.Expect(value).To(gomega.Equal("/opt/ik/site"))
			marker, _ := engineRuntimeFindEnv(engine, roleEnvMarker)
			gomega.Expect(marker).To(gomega.Equal("/usr/lib/python"),
				"the role's own env must survive the patch pass")
			mount := engineRuntimeFindMount(engine, "ik-full-vol")
			gomega.Expect(mount).ToNot(gomega.BeNil(), "the /volumeMounts/- append must land")
			gomega.Expect(mount.MountPath).To(gomega.Equal("/opt/ik"))
			gomega.Expect(mount.ReadOnly).To(gomega.BeTrue())

			// The role-side extraPatches, applied after the profile's: the
			// capability append.
			gomega.Expect(engine.SecurityContext).ToNot(gomega.BeNil())
			gomega.Expect(engine.SecurityContext.Capabilities).ToNot(gomega.BeNil())
			gomega.Expect(engine.SecurityContext.Capabilities.Add).To(gomega.ContainElement(corev1.Capability("SYS_NICE")))

			// The component scope: the leader renders byte-identically to
			// its own template — no fragments, no patches.
			leader, err := engineRuntimeComponent(f, rbg.Name, string(constants.LeaderComponentType))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(engineRuntimeFindContainer(leader, "ik-sidecar")).To(gomega.BeNil())
			gomega.Expect(engineRuntimeFindVolume(leader, "ik-full-vol")).To(gomega.BeFalse())
			leaderEngine := engineRuntimeFindContainer(leader, engineContainerName)
			gomega.Expect(leaderEngine.Command).To(gomega.Equal([]string{"sh"}))
			_, hasSite := engineRuntimeFindEnv(leaderEngine, "IK_SITE")
			gomega.Expect(hasSite).To(gomega.BeFalse())
			gomega.Expect(leaderEngine.SecurityContext).To(gomega.BeNil())
		})

		ginkgo.It("leaves a patch-carrying profile unaimed without targetContainers and warns", func() {
			profile := engineRuntimeProfile("kep531-e2e-unaimed",
				[]corev1.Container{sleeperContainer("ik-sidecar")},
				[]corev1.Volume{{Name: "ik-unaimed-vol", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				[]workloadsv1alpha2.ContainerPatch{
					erPatch("add", "/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
				})
			gomega.Expect(f.Client.Create(f.Ctx, profile)).To(gomega.Succeed())
			engineRuntimeCleanupProfile(f, profile.Name)

			// The compatibility shape: profileName only, no component, no
			// targetContainers — a valid state a profile gaining
			// containerPatches cannot break.
			rbg := engineRuntimeStandaloneRbg(f.Namespace, "kep531-unaimed",
				workloadsv1alpha2.EngineRuntime{ProfileName: profile.Name})
			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			var tpl *corev1.PodTemplateSpec
			gomega.Eventually(func(g gomega.Gomega) {
				var err error
				tpl, err = engineRuntimeComponent(f, rbg.Name, "solo")
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}, utils.Timeout, utils.Interval).Should(gomega.Succeed())

			// The fragments still deliver; the patches do not fire.
			gomega.Expect(engineRuntimeFindContainer(tpl, "ik-sidecar")).ToNot(gomega.BeNil(),
				"the fragments must still deliver on the compatibility-shape reference")
			engine := engineRuntimeFindContainer(tpl, engineContainerName)
			_, hasSite := engineRuntimeFindEnv(engine, "IK_SITE")
			gomega.Expect(hasSite).To(gomega.BeFalse(),
				"the patches must not fire without a targetContainers consent")
			marker, _ := engineRuntimeFindEnv(engine, roleEnvMarker)
			gomega.Expect(marker).To(gomega.Equal("/usr/lib/python"))

			gomega.Eventually(func() bool {
				found, _ := engineRuntimeEvent(f, rbg.Name, "UnaimedContainerPatch")
				return found
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"an UnaimedContainerPatch warning event must be recorded on the RBG")
		})

		ginkgo.It("fails the render loudly when a targetContainers entry names an unknown container", func() {
			profile := engineRuntimeProfile("kep531-e2e-broken", nil, nil,
				[]workloadsv1alpha2.ContainerPatch{
					erPatch("add", "/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
				})
			gomega.Expect(f.Client.Create(f.Ctx, profile)).To(gomega.Succeed())
			engineRuntimeCleanupProfile(f, profile.Name)

			rbg := engineRuntimeLwpRbg(f.Namespace, "kep531-broken", workloadsv1alpha2.EngineRuntime{
				ProfileName: profile.Name,
				Component:   string(constants.WorkerComponentType),
				TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
					{Name: "ghost"},
				},
			})
			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
			// The write is admitted: the ghost entry is a render error, not
			// an admission error — objects written through an
			// admission-disabled window must fail the same way at render.
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())

			// No child workload is created, and the failure is a warning
			// event on the RBG — render outcomes are events, never status
			// conditions.
			risKey := client.ObjectKey{
				Namespace: f.Namespace,
				Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
			}
			gomega.Consistently(func() bool {
				return apierrors.IsNotFound(f.Client.Get(f.Ctx, risKey, &workloadsv1alpha2.RoleInstanceSet{}))
			}, 30*time.Second, utils.Interval).Should(gomega.BeTrue(),
				"a failed render must leave no child workload behind")
			gomega.Eventually(func() bool {
				found, _ := engineRuntimeEvent(f, rbg.Name, "EngineRuntimeRenderFailed")
				return found
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"an EngineRuntimeRenderFailed warning event must be recorded on the RBG")
		})

		ginkgo.It("stacks two profiles deterministically with the later reference landing leftmost", func() {
			accel := engineRuntimeProfile("kep531-e2e-accel", nil,
				[]corev1.Volume{{Name: "ik-accel-vol", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				[]workloadsv1alpha2.ContainerPatch{
					erPatch("add", "/volumeMounts/-", corev1.VolumeMount{Name: "ik-accel-vol", MountPath: "/opt/accel"}),
					erPatch("add", "/env/0", corev1.EnvVar{Name: "ACCEL_SITE", Value: "/opt/accel"}),
				})
			tele := engineRuntimeProfile("kep531-e2e-tele", nil,
				[]corev1.Volume{{Name: "ik-tele-vol", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				[]workloadsv1alpha2.ContainerPatch{
					erPatch("add", "/volumeMounts/-", corev1.VolumeMount{Name: "ik-tele-vol", MountPath: "/opt/tele"}),
					erPatch("add", "/env/0", corev1.EnvVar{Name: "TELE_SITE", Value: "/opt/tele"}),
				})
			gomega.Expect(f.Client.Create(f.Ctx, accel)).To(gomega.Succeed())
			engineRuntimeCleanupProfile(f, accel.Name)
			gomega.Expect(f.Client.Create(f.Ctx, tele)).To(gomega.Succeed())
			engineRuntimeCleanupProfile(f, tele.Name)

			// Two references may name the same component because they carry
			// different profileNames (D6); engineRuntimes order [accel, tele].
			rbg := engineRuntimeLwpRbg(f.Namespace, "kep531-stacked",
				workloadsv1alpha2.EngineRuntime{
					ProfileName:      accel.Name,
					Component:        string(constants.WorkerComponentType),
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{Name: engineContainerName}},
				},
				workloadsv1alpha2.EngineRuntime{
					ProfileName:      tele.Name,
					Component:        string(constants.WorkerComponentType),
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{Name: engineContainerName}},
				})
			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			var worker *corev1.PodTemplateSpec
			gomega.Eventually(func(g gomega.Gomega) {
				var err error
				worker, err = engineRuntimeComponent(f, rbg.Name, string(constants.WorkerComponentType))
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}, utils.Timeout, utils.Interval).Should(gomega.Succeed())

			// Head-inserts stack with the LATER reference landing leftmost:
			// env order = [TELE_SITE, ACCEL_SITE, ..., PYTHONPATH]. The
			// controller injects RBG_* env vars after the patch roots, so
			// assert relative order instead of absolute layout.
			engine := engineRuntimeFindContainer(worker, engineContainerName)
			teleVal, ok := engineRuntimeFindEnv(engine, "TELE_SITE")
			gomega.Expect(ok).To(gomega.BeTrue(), "TELE_SITE must be present")
			gomega.Expect(teleVal).To(gomega.Equal("/opt/tele"))
			accelVal, ok := engineRuntimeFindEnv(engine, "ACCEL_SITE")
			gomega.Expect(ok).To(gomega.BeTrue(), "ACCEL_SITE must be present")
			gomega.Expect(accelVal).To(gomega.Equal("/opt/accel"))
			teleIdx, _ := engineRuntimeEnvIndex(engine, "TELE_SITE")
			accelIdx, _ := engineRuntimeEnvIndex(engine, "ACCEL_SITE")
			markerIdx, _ := engineRuntimeEnvIndex(engine, roleEnvMarker)
			gomega.Expect(teleIdx).To(gomega.BeNumerically("<", accelIdx),
				"the later reference must land leftmost")
			gomega.Expect(accelIdx).To(gomega.BeNumerically("<", markerIdx),
				"profile patches must land before the role's own env")
			gomega.Expect(engineRuntimeFindVolume(worker, "ik-accel-vol")).To(gomega.BeTrue())
			gomega.Expect(engineRuntimeFindVolume(worker, "ik-tele-vol")).To(gomega.BeTrue())

			leader, err := engineRuntimeComponent(f, rbg.Name, string(constants.LeaderComponentType))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(engineRuntimeFindVolume(leader, "ik-accel-vol")).To(gomega.BeFalse())
			gomega.Expect(engineRuntimeFindVolume(leader, "ik-tele-vol")).To(gomega.BeFalse())
		})

		ginkgo.It("delivers fragments and consent per component through two references to one profile", func() {
			profile := engineRuntimeProfile("kep531-e2e-split", nil,
				[]corev1.Volume{{Name: "ik-split-vol", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				[]workloadsv1alpha2.ContainerPatch{
					erPatch("add", "/env/-", corev1.EnvVar{Name: "IK_SITE", Value: "/opt/ik/site"}),
				})
			gomega.Expect(f.Client.Create(f.Ctx, profile)).To(gomega.Succeed())
			engineRuntimeCleanupProfile(f, profile.Name)

			// The legal two-reference shape (D1/D6): one profile, one
			// reference per component, disjoint components.
			rbg := engineRuntimeLwpRbg(f.Namespace, "kep531-split",
				workloadsv1alpha2.EngineRuntime{
					ProfileName: profile.Name,
					Component:   string(constants.LeaderComponentType),
				},
				workloadsv1alpha2.EngineRuntime{
					ProfileName: profile.Name,
					Component:   string(constants.WorkerComponentType),
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
						{Name: engineContainerName},
					},
				})
			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			var leader, worker *corev1.PodTemplateSpec
			gomega.Eventually(func(g gomega.Gomega) {
				var err error
				leader, err = engineRuntimeComponent(f, rbg.Name, string(constants.LeaderComponentType))
				g.Expect(err).ToNot(gomega.HaveOccurred())
				worker, err = engineRuntimeComponent(f, rbg.Name, string(constants.WorkerComponentType))
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}, utils.Timeout, utils.Interval).Should(gomega.Succeed())

			// The leader reference delivers the fragments only: the volume
			// is there, the patch is not (no consent).
			gomega.Expect(engineRuntimeFindVolume(leader, "ik-split-vol")).To(gomega.BeTrue())
			_, leaderSite := engineRuntimeFindEnv(engineRuntimeFindContainer(leader, engineContainerName), "IK_SITE")
			gomega.Expect(leaderSite).To(gomega.BeFalse(),
				"the leader reference lists no target container, so the patch must not fire there")

			// The worker reference delivers the fragments along with its
			// consent.
			gomega.Expect(engineRuntimeFindVolume(worker, "ik-split-vol")).To(gomega.BeTrue())
			workerSite, found := engineRuntimeFindEnv(engineRuntimeFindContainer(worker, engineContainerName), "IK_SITE")
			gomega.Expect(found).To(gomega.BeTrue())
			gomega.Expect(workerSite).To(gomega.Equal("/opt/ik/site"))

			// Neither engine mounts the volume (the profile declares no
			// volumeMounts patch), so both pods run — asserted by
			// ExpectRbgV2Equal above.
			gomega.Eventually(func() bool {
				found, _ := engineRuntimeEvent(f, rbg.Name, "UnaimedContainerPatch")
				return found
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"the leader reference lists no targetContainers while the profile declares patches, so one warning must fire")
		})

		ginkgo.It("freezes a referenced profile and restores it when the reference leaves", func() {
			profile := engineRuntimeProfile("kep531-e2e-immutable", nil,
				[]corev1.Volume{{Name: "immutable-vol", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				nil)
			gomega.Expect(f.Client.Create(f.Ctx, profile)).To(gomega.Succeed())
			engineRuntimeCleanupProfile(f, profile.Name)

			rbg := engineRuntimeStandaloneRbg(f.Namespace, "kep531-immutable",
				workloadsv1alpha2.EngineRuntime{ProfileName: profile.Name})
			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// The reference-tracking controller records the live reference
			// and the Frozen condition.
			gomega.Eventually(func(g gomega.Gomega) {
				stored := &workloadsv1alpha2.ClusterEngineRuntimeProfile{}
				g.Expect(f.Client.Get(f.Ctx, types.NamespacedName{Name: profile.Name}, stored)).To(gomega.Succeed())
				g.Expect(stored.Status.ReferenceCount).To(gomega.BeNumerically(">=", 1))
				frozen := false
				for _, c := range stored.Status.Conditions {
					if c.Type == workloadsv1alpha2.FrozenConditionType {
						frozen = c.Status == metav1.ConditionTrue && c.Reason == workloadsv1alpha2.ReasonReferenced
					}
				}
				g.Expect(frozen).To(gomega.BeTrue(), "the Frozen condition must be True/Referenced while referenced")
			}, utils.Timeout, utils.Interval).Should(gomega.Succeed())

			// Spec edits and deletes are rejected; metadata stays editable.
			stored := &workloadsv1alpha2.ClusterEngineRuntimeProfile{}
			gomega.Expect(f.Client.Get(f.Ctx, types.NamespacedName{Name: profile.Name}, stored)).To(gomega.Succeed())
			stored.Spec.Volumes = append(stored.Spec.Volumes, corev1.Volume{
				Name: "immutable-extra", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			})
			err := f.Client.Update(f.Ctx, stored)
			gomega.Expect(err).To(gomega.HaveOccurred(), "a spec edit of a referenced profile must be rejected")
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("frozen"))
			gomega.Expect(f.Client.Delete(f.Ctx, stored)).ToNot(gomega.Succeed(),
				"deleting a referenced profile must be rejected")

			// The failed update leaves the local copy carrying the rejected
			// spec change; re-fetch so the metadata edit carries no spec delta.
			stored = &workloadsv1alpha2.ClusterEngineRuntimeProfile{}
			gomega.Expect(f.Client.Get(f.Ctx, types.NamespacedName{Name: profile.Name}, stored)).To(gomega.Succeed())
			stored.Annotations = map[string]string{"e2e": "label-edit"}
			gomega.Expect(f.Client.Update(f.Ctx, stored)).To(gomega.Succeed(),
				"metadata edits stay writable while the spec is frozen")

			// The last reference leaving restores editability.
			gomega.Expect(f.Client.Delete(f.Ctx, rbg)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				after := &workloadsv1alpha2.ClusterEngineRuntimeProfile{}
				g.Expect(f.Client.Get(f.Ctx, types.NamespacedName{Name: profile.Name}, after)).To(gomega.Succeed())
				g.Expect(after.Status.ReferenceCount).To(gomega.Equal(int32(0)))
				after.Spec.Volumes = append(after.Spec.Volumes, corev1.Volume{
					Name: "immutable-unfrozen", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				})
				g.Expect(f.Client.Update(f.Ctx, after)).To(gomega.Succeed(),
					"the spec edit must go through once no reference remains")
			}, utils.Timeout, utils.Interval).Should(gomega.Succeed())
		})
	})
}

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

package drills

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/discovery"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/e2e/upgrade"
	"sigs.k8s.io/rbgs/test/utils"
)

// Fixture and profile names. The profiles are cluster-scoped, so they are
// prefixed and removed in AfterSuite; the fixtures live in the framework's
// own namespace. The RBG names double as prefix material for the pods the
// fixtures run, so they are kept short.
const (
	// profRuntime is created before the upgrade and carries only the
	// pre-KEP profile surface: one sidecar container. The strict and prune
	// probes and the leader-worker fixtures all reference it.
	profRuntime = "drill-runtime"
	// profPatches is created after the upgrade and additionally carries the
	// new profile-side containerPatches surface (drill 3).
	profPatches = "drill-patches"
	// profFrozen is the profile the frozen-admission drill (drill 5) keeps
	// referenced from the moment it exists.
	profFrozen = "drill-frozen"
	// profDoomed is the referenced profile the admission-disabled window
	// deletes (drill 5).
	profDoomed = "drill-doomed"

	fxStrictProbe    = "drill-strict"
	fxPruned         = "drill-pruned"
	fxStrippedWorker = "drill-stripped"
	fxStandalone     = "drill-solo"
	fxCleanup        = "drill-cleanup"
	fxFrozen         = "drill-frozen"
	fxDoomed         = "drill-doomed"

	runtimeSidecar  = "drill-runtime-sidecar"
	patchesSidecar  = "drill-patches-sidecar"
	frozenSidecar   = "drill-frozen-sidecar"
	doomedSidecar   = "drill-doomed-sidecar"
	profilePatchEnv = "DRILL_PROFILE_PATCH"
	rolePatchEnv    = "DRILL_ROLE_PATCH"

	lwpRoleName  = "app"
	soloRoleName = "solo"
	cleanRole    = "clean"

	// settleDuration mirrors the upgrade suite: two samples this far apart
	// that agree are the evidence a controller has looked at these objects
	// and left them alone, not that it has not looked yet.
	settleDuration = 30 * time.Second
)

// RunDrillSpecs is the body of the Ordered container. The specs are phases of
// one experiment on one set of fixtures: pre-upgrade writes against the old
// CRDs, the chart two-pass upgrade (controller image first, CRD second, with
// the safe old/old rollback drilled between the passes), the new-controller
// render semantics, the admission-disabled window with the controller image
// rolled back, and the re-upgrade with the webhook configuration re-applied by
// hand. None of them may run in isolation, and the order is the drill.
func RunDrillSpecs(f *framework.Framework) {
	// before captures renders and child state at the phases later phases
	// compare against.
	var before struct {
		// preUpgrade is the pre-upgrade render of both leader-worker
		// fixtures, keyed <rbg>/<component>: what the objects written before
		// the CRD upgrade must still render as under the new controller
		// (drills 1, 2 and 4).
		preUpgrade map[string]corev1.PodTemplateSpec
		// patched is the post-upgrade render of the standalone
		// targetContainers fixture with the profile-side patch fired
		// (drill 3).
		patched corev1.PodTemplateSpec
		// windowGeneration and windowSpec are the prune fixture's
		// RoleInstanceSet generation and spec entering the window: the
		// render-failure phase asserts the pair stored through the window
		// leaves exactly this child behind.
		windowGeneration int64
		windowSpec       workloadsv1alpha2.RoleInstanceSetSpec
	}

	// ---------------------------------------------------------------------------
	// Phase 1: writes against the pre-upgrade CRDs and controller.
	// ---------------------------------------------------------------------------

	ginkgo.It(fmt.Sprintf("[drill 1] rejects a strict write of the KEP-531 fields against the %s CRDs", drillFromRef()),
		func() {
			probe := lwpRbg(f.Namespace, fxStrictProbe,
				workloadsv1alpha2.EngineRuntime{
					ProfileName: profRuntime,
					Component:   string(constants.WorkerComponentType),
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
						{Name: "nginx"},
					},
				})

			// Strict decoding fails at the apiserver, before admission, so
			// the from-release webhooks are not even consulted: the old
			// CRD schema is the whole story.
			err := f.Client.Create(f.Ctx, probe, client.FieldValidation("Strict"))
			gomega.Expect(err).To(gomega.HaveOccurred(),
				"the %s CRDs accepted engineRuntimes[].component; the strict write would not be a probe of anything",
				drillFromRef())
			for _, unknown := range []string{
				"unknown field",
				"engineRuntimes[0].component",
				"engineRuntimes[0].targetContainers",
			} {
				gomega.Expect(err.Error()).To(gomega.ContainSubstring(unknown),
					"the strict rejection should name %q", unknown)
			}

			err = f.Client.Get(f.Ctx, client.ObjectKey{
				Namespace: f.Namespace, Name: fxStrictProbe,
			}, &workloadsv1alpha2.RoleBasedGroup{})
			gomega.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(),
				"a rejected strict write must not leave an object behind")
		})

	ginkgo.It("[drill 1+2] prunes the new fields out of a non-strict write and stores an active unscoped reference",
		func() {
			// Two fixtures in one spec: drill 1's write carries both new
			// fields, drill 2's carries the worker-scoped component whose
			// loss is the surprising one. Both writes go out with the new
			// fields set, both are pruned to the same stored shape, and
			// the difference between them is nothing at all — which is the
			// silent data loss the drill pins.
			ginkgo.By("creating " + profRuntime + " with the pre-KEP profile surface")
			gomega.Expect(f.Client.Create(f.Ctx, buildProfile(profRuntime, runtimeSidecar))).To(gomega.Succeed())

			pruned := lwpRbg(f.Namespace, fxPruned,
				workloadsv1alpha2.EngineRuntime{
					ProfileName: profRuntime,
					Component:   string(constants.LeaderComponentType),
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
						{Name: "nginx"},
					},
				})
			gomega.Expect(
				f.Client.Create(f.Ctx, pruned, client.FieldValidation("Ignore")),
			).To(gomega.Succeed(), "a non-strict write against the old CRDs must be admitted")

			stripped := lwpRbg(f.Namespace, fxStrippedWorker,
				workloadsv1alpha2.EngineRuntime{
					ProfileName:      profRuntime,
					Component:        string(constants.WorkerComponentType),
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{Name: "nginx"}},
				})
			gomega.Expect(
				f.Client.Create(f.Ctx, stripped, client.FieldValidation("Ignore")),
			).To(gomega.Succeed(), "a non-strict write against the old CRDs must be admitted")

			// The stored shape is the prune: profileName only, both new
			// fields silently gone — including the worker intent of the
			// second fixture.
			for _, name := range []string{fxPruned, fxStrippedWorker} {
				overlay := fmt.Sprintf("the pruned write of %s", name)
				stored, err := storedEngineRuntime(f, name, 0)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(stored).To(gomega.HaveKeyWithValue("profileName", profRuntime), overlay)
				gomega.Expect(stored).ToNot(gomega.HaveKey("component"),
					"%s: component must have been pruned", overlay)
				gomega.Expect(stored).ToNot(gomega.HaveKey("targetContainers"),
					"%s: targetContainers must have been pruned", overlay)
			}

			for _, name := range []string{fxPruned, fxStrippedWorker} {
				ginkgo.By("waiting for " + name + " to become ready on the old controller")
				waitRoleReady(f, name)
			}

			rendered := map[string]corev1.PodTemplateSpec{}
			for _, name := range []string{fxPruned, fxStrippedWorker} {
				ris := waitRoleRendered(f, name)

				leader := componentTemplate(ris, string(constants.LeaderComponentType))
				worker := componentTemplate(ris, string(constants.WorkerComponentType))
				gomega.Expect(leader).ToNot(gomega.BeNil(), "%s: no leader component rendered", name)
				gomega.Expect(worker).ToNot(gomega.BeNil(), "%s: no worker component rendered", name)

				// The unscoped reference is active: the default component
				// set under leaderWorkerPattern is the leader only
				// (main's published behavior), so the sidecar is on the
				// leader and the worker template is untouched — for the
				// stripped fixture even though the write asked for the
				// worker (drill 2).
				overlay := fmt.Sprintf("%s on %s", drillFromRef(), name)
				gomega.Expect(hasContainer(leader, runtimeSidecar)).To(gomega.BeTrue(),
					"%s: the pruned unscoped reference is inactive", overlay)
				gomega.Expect(hasContainer(worker, runtimeSidecar)).To(gomega.BeFalse(),
					"%s: the absent-component default must reach the leader only", overlay)

				rendered[name+"/"+string(constants.LeaderComponentType)] = *leader.DeepCopy()
				rendered[name+"/"+string(constants.WorkerComponentType)] = *worker.DeepCopy()
			}
			before.preUpgrade = rendered
		})

	// ---------------------------------------------------------------------------
	// Phase 2: the chart two-pass upgrade (KEP-531: controller first, CRD
	// second). The chart's default order is the reverse — the CRD upgrader
	// Job runs as a pre-upgrade hook — so a plain helm upgrade would land
	// the CRDs while the old controller is still serving, the dangerous
	// old-controller/new-CRD row. The two passes traverse old/old ->
	// new/old -> new/new and never the dangerous row; the rollback between
	// the pins is included precisely because it is the safe one.
	// ---------------------------------------------------------------------------

	ginkgo.It("[pass 1] swaps the controller image only (crdUpgrade.enabled=false)", func() {
		upgrade.RunHelmUpgradeWith(f, "--set", "crdUpgrade.enabled=false")

		ginkgo.By("waiting for the new controller to roll out and serve")
		upgrade.WaitControllerRolledOut(f, upgrade.ToTag())
		// The pre-upgrade hook is the chart's CRD path and pass one just
		// disabled it, but the gates below prove only the controller side.
		// The marker is asserted directly so the row the window specs pin
		// really is the new-controller/old-CRD one.
		present, err := kep531MarkerPresent(f)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(present).To(gomega.BeFalse(),
			"pass one replaced the CRD bundle anyway, so the two-pass order collapsed into the chart default")
		// The conversion CRDs were not replaced, so their caBundle
		// survived untouched. The webhook configurations WERE re-rendered
		// by the chart with an empty caBundle; the running controller
		// patches them back.
		upgrade.WaitValidatingWebhookCABundle(f)
		upgrade.WaitMutatingWebhookCABundle(f)
		upgrade.WaitConversionActuallyWorks(f, fxPruned)
	})

	ginkgo.It("[pass 1] keeps rejecting the KEP-531 fields and renders the pre-upgrade objects as main",
		func() {
			// The strict probe, repeated inside the window: the old CRD is
			// still what serves the write, so the strict rejection is the
			// row's own contract — a strict client gets `unknown field`,
			// never a stored object.
			probe := lwpRbg(f.Namespace, fxStrictProbe,
				workloadsv1alpha2.EngineRuntime{
					ProfileName: profRuntime,
					Component:   string(constants.WorkerComponentType),
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
						{Name: "nginx"},
					},
				})
			err := f.Client.Create(f.Ctx, probe, client.FieldValidation("Strict"))
			gomega.Expect(err).To(gomega.HaveOccurred(),
				"the old CRDs still serve this write; the new-controller/old-CRD row must keep rejecting "+
					"the KEP-531 fields at the apiserver")
			gomega.Expect(err.Error()).To(gomega.ContainSubstring("unknown field"))

			// Two samples settleDuration apart, then both compared against
			// the pre-upgrade render: a controller that only looks quiet
			// because it has not reconciled yet would fail the second
			// sample, and one that rewrites the stored specs would fail
			// both. This is the row's other half: every reference renders
			// exactly as on main.
			first := sampleLWPRenders(f)
			time.Sleep(settleDuration)
			second := sampleLWPRenders(f)

			for key, tpl := range before.preUpgrade {
				gomega.Expect(first[key]).To(gomega.Equal(tpl),
					"%s: the new-controller/old-CRD row renders a different template than %s did",
					key, drillFromRef())
				gomega.Expect(second[key]).To(gomega.Equal(tpl),
					"%s: the render did not settle within %s", key, settleDuration)
			}

			// The new controller must not rewrite the stored specs either:
			// the references are still the pruned shape the old CRDs
			// stored, and the row leaves them that way.
			for _, name := range []string{fxPruned, fxStrippedWorker} {
				stored, err := storedEngineRuntime(f, name, 0)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(stored).ToNot(gomega.HaveKey("component"),
					"%s: the new controller wrote a component into the stored spec", name)
				gomega.Expect(stored).ToNot(gomega.HaveKey("targetContainers"),
					"%s: the new controller wrote targetContainers into the stored spec", name)
			}
		})

	ginkgo.It("[window] rolls the controller image back between the passes (the old/old row)", func() {
		// The rollback the two-pass order makes safe: the CRDs are still the
		// old ones, so rolling the controller image back lands in old/old —
		// main exactly — instead of the dangerous old-controller/new-CRD
		// row a rollback after pass two would land in.
		swapControllerImage(f, drillFromTag(), fxPruned)

		stored := sampleLWPRenders(f)
		for key, tpl := range before.preUpgrade {
			gomega.Expect(stored[key]).To(gomega.Equal(tpl),
				"%s: the old/old row must render as main, but the template changed", key)
		}

		// Roll forward before pass two: the pre-upgrade hook applies the
		// new CRDs while whichever controller is running, so the sequence
		// must not hand it the old one — that combination is the row the
		// whole two-pass order exists to avoid.
		swapControllerImage(f, upgrade.ToTag(), fxPruned)
	})

	ginkgo.It("[pass 2] applies the new CRD without restarting the controller", func() {
		// The controller image is unchanged from pass one, so Helm has no
		// reason to touch the Deployment: the only change this pass makes
		// is the CRD replacement. The pod names are the evidence — a
		// restart would replace every one of them.
		podsBefore, err := upgrade.ManagerPodNames(f)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		upgrade.RunHelmUpgrade(f)

		podsAfter, err := upgrade.ManagerPodNames(f)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(podsAfter).To(gomega.Equal(podsBefore),
			"pass two restarted the controller pods; the CRD-only pass must leave them alone")

		upgrade.WaitForUpgradeReady(f, fxPruned,
			"the KEP-531 fields on the new CRD bundle (engineRuntimes[].component and CERP containerPatches)",
			kep531MarkerPresent)

		// The full upgrade leaves the pre-upgrade objects as they were:
		// the new/new row renders them as main still, and neither pass
		// rewrote the stored specs.
		after := sampleLWPRenders(f)
		for key, tpl := range before.preUpgrade {
			gomega.Expect(after[key]).To(gomega.Equal(tpl),
				"%s: the completed upgrade renders a different template than %s did", key, drillFromRef())
		}
		for _, name := range []string{fxPruned, fxStrippedWorker} {
			stored, err := storedEngineRuntime(f, name, 0)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(stored).ToNot(gomega.HaveKey("component"),
				"%s: the upgrade wrote a component into the stored spec", name)
			gomega.Expect(stored).ToNot(gomega.HaveKey("targetContainers"),
				"%s: the upgrade wrote targetContainers into the stored spec", name)
		}
	})

	// ---------------------------------------------------------------------------
	// Phase 3: the new controller's render semantics. The byte-for-byte
	// render guarantees moved into the pass specs: the settle-and-compare
	// runs inside the new-controller/old-CRD window (the row whose contract
	// it is), and the completed-upgrade re-check closes pass two.
	// ---------------------------------------------------------------------------

	ginkgo.It("[drill 3] renders the profile-side patches of a standalone targetContainers reference",
		func() {
			profile := buildProfile(profPatches, patchesSidecar,
				envPatch(corev1.EnvVar{Name: profilePatchEnv, Value: "1"}))
			gomega.Expect(f.Client.Create(f.Ctx, profile)).To(gomega.Succeed())

			// No component: under standalonePattern the pattern's one
			// component is the only possible target, so the presence rule
			// exempts the write and the patch dimensions operate on the
			// role-named component.
			solo := standaloneRbg(f.Namespace, fxStandalone, soloRoleName,
				workloadsv1alpha2.EngineRuntime{
					ProfileName: profPatches,
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{
						{Name: "nginx"},
					},
				})
			ginkgo.By("creating " + fxStandalone + " through the version-under-test webhooks")
			gomega.Expect(f.Client.Create(f.Ctx, solo)).To(gomega.Succeed())
			waitRoleReady(f, fxStandalone)

			tpl := componentTemplate(waitRoleRendered(f, fxStandalone), soloRoleName)
			gomega.Expect(tpl).ToNot(gomega.BeNil(), "no %s component rendered", soloRoleName)
			gomega.Expect(hasContainer(tpl, patchesSidecar)).To(gomega.BeTrue(),
				"the fragments of the profile must still land on the standalone component")
			nginx := findContainer(tpl, "nginx")
			gomega.Expect(nginx).ToNot(gomega.BeNil())
			value, found := envValue(nginx, profilePatchEnv)
			gomega.Expect(found).To(gomega.BeTrue(),
				"the profile's containerPatches must fire at the role-declared container listed in targetContainers")
			gomega.Expect(value).To(gomega.Equal("1"))

			before.patched = *tpl.DeepCopy()
		})

	ginkgo.It("[drill 4] renders the remaining spec byte-for-byte as on main once the new-field reference is removed",
		func() {
			clean := standaloneRbg(f.Namespace, fxCleanup, cleanRole,
				workloadsv1alpha2.EngineRuntime{ProfileName: profRuntime})
			gomega.Expect(f.Client.Create(f.Ctx, clean)).To(gomega.Succeed())
			waitRoleReady(f, fxCleanup)

			baseline := componentTemplate(waitRoleRendered(f, fxCleanup), cleanRole)
			gomega.Expect(baseline).ToNot(gomega.BeNil(), "no %s component rendered", cleanRole)
			gomega.Expect(hasContainer(baseline, runtimeSidecar)).To(gomega.BeTrue(),
				"the plain profileName-only reference must render the profile sidecar")

			// The new-field shape: the role's own extraPatches at the
			// role-declared container, an observable effect only the new
			// field surfaces produce.
			ginkgo.By("adding a targetContainers entry with a role-side patch")
			err := updateFirstEngineRuntime(f, fxCleanup, func(er *workloadsv1alpha2.EngineRuntime) {
				er.TargetContainers = []workloadsv1alpha2.EngineRuntimeTargetContainer{{
					Name: "nginx",
					ExtraPatches: []workloadsv1alpha2.ContainerPatch{
						envPatch(corev1.EnvVar{Name: rolePatchEnv, Value: "cleanup"}),
					},
				}}
			})
			gomega.Expect(err).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) string {
				tpl, err := tryComponentTemplate(f, fxCleanup, cleanRole)
				g.Expect(err).ToNot(gomega.HaveOccurred())
				value, _ := envValue(findContainer(tpl, "nginx"), rolePatchEnv)
				return value
			}, renderTimeout, renderInterval).Should(gomega.Equal("cleanup"),
				"the role-side patch of the targetContainers entry must fire while the reference is set")

			// The cleanup write: the new-field reference leaves the spec,
			// and whatever remains renders byte-for-byte as it did before —
			// the new fields leave no residue in the render.
			ginkgo.By("removing the new-field reference from the spec")
			err = updateFirstEngineRuntime(f, fxCleanup, func(er *workloadsv1alpha2.EngineRuntime) {
				er.TargetContainers = nil
			})
			gomega.Expect(err).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) corev1.PodTemplateSpec {
				tpl, err := tryComponentTemplate(f, fxCleanup, cleanRole)
				g.Expect(err).ToNot(gomega.HaveOccurred())
				return *tpl
			}, renderTimeout, renderInterval).Should(gomega.Equal(*baseline),
				"after the new-field reference is removed, the remaining spec must render exactly as before")
		})

	ginkgo.It("[drill 5] freezes a referenced profile at admission", func() {
		gomega.Expect(f.Client.Create(f.Ctx, buildProfile(profFrozen, frozenSidecar))).To(gomega.Succeed())
		frozen := standaloneRbg(f.Namespace, fxFrozen, soloRoleName,
			workloadsv1alpha2.EngineRuntime{ProfileName: profFrozen})
		gomega.Expect(f.Client.Create(f.Ctx, frozen)).To(gomega.Succeed())
		waitRoleReady(f, fxFrozen)

		// The controller's status surface is the evidence the reference is
		// live, before the rejections are read as anything else.
		gomega.Eventually(func(g gomega.Gomega) int32 {
			profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{}
			g.Expect(f.Client.Get(f.Ctx, client.ObjectKey{Name: profFrozen}, profile)).To(gomega.Succeed())
			return profile.Status.ReferenceCount
		}, renderTimeout, renderInterval).Should(gomega.BeNumerically(">=", 1),
			"the controller never counted the live reference to %s", profFrozen)

		err := updateProfileSpec(f, profFrozen, func(spec *workloadsv1alpha2.ClusterEngineRuntimeProfileSpec) {
			spec.Containers = append(spec.Containers, corev1.Container{
				Name: frozenSidecar + "-window", Image: utils.DefaultImage,
			})
		})
		gomega.Expect(err).To(gomega.HaveOccurred(), "a spec edit of a referenced profile must be rejected at admission")
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("is frozen"))

		err = f.Client.Delete(f.Ctx, cerpObject(profFrozen))
		gomega.Expect(err).To(gomega.HaveOccurred(), "deleting a referenced profile must be rejected at admission")
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("is frozen"))

		// The window's delete target: a second referenced profile, so drill
		// 5 can delete one without disturbing the frozen one it keeps
		// asserting against.
		gomega.Expect(f.Client.Create(f.Ctx, buildProfile(profDoomed, doomedSidecar))).To(gomega.Succeed())
		doomed := standaloneRbg(f.Namespace, fxDoomed, soloRoleName,
			workloadsv1alpha2.EngineRuntime{ProfileName: profDoomed})
		gomega.Expect(f.Client.Create(f.Ctx, doomed)).To(gomega.Succeed())
		waitRoleReady(f, fxDoomed)

		err = f.Client.Delete(f.Ctx, cerpObject(profDoomed))
		gomega.Expect(err).To(gomega.HaveOccurred(), "deleting a referenced profile must be rejected at admission")
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("is frozen"))
	})

	// ---------------------------------------------------------------------------
	// Phase 4: the admission-disabled window.
	// ---------------------------------------------------------------------------

	ginkgo.It("[drill 5] removes the CERP entry from the ValidatingWebhookConfiguration", func() {
		removeCERPWebhookEntry(f)
	})

	ginkgo.It(fmt.Sprintf("[window] rolls the controller image back to %s", drillFromRef()), func() {
		swapControllerImage(f, drillFromTag(), fxPruned)

		// The old controller's render of the prune fixture, sampled after
		// its rollout gates: the pair drill 1 writes during the window is
		// admitted by this controller, and the render-failure phase later
		// asserts the new controller's failure left exactly this child.
		ris := roleInstanceSet(f, fxPruned)
		before.windowGeneration = ris.Generation
		before.windowSpec = *ris.Spec.DeepCopy()
	})

	ginkgo.It("[drill 5] admits CERP updates and deletes with no webhook in the path", func() {
		err := updateProfileSpec(f, profFrozen, func(spec *workloadsv1alpha2.ClusterEngineRuntimeProfileSpec) {
			spec.Containers = append(spec.Containers, corev1.Container{
				Name: frozenSidecar + "-drift", Image: utils.DefaultImage,
			})
		})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(),
			"a spec edit of a referenced profile must be admitted with the CERP entry removed")

		gomega.Expect(f.Client.Delete(f.Ctx, cerpObject(profDoomed))).To(gomega.Succeed(),
			"deleting a referenced profile must be admitted with the CERP entry removed")

		// The referencing RoleBasedGroup is removed too: it would render
		// against a profile that no longer exists for the rest of the
		// suite, which is not part of this drill.
		doomed := getRBG(f, fxDoomed)
		gomega.Expect(f.Client.Delete(f.Ctx, doomed)).To(gomega.Succeed())
		gomega.Eventually(func() bool {
			err := f.Client.Get(f.Ctx, client.ObjectKey{
				Namespace: f.Namespace, Name: fxDoomed,
			}, &workloadsv1alpha2.RoleBasedGroup{})
			return apierrors.IsNotFound(err)
		}, renderTimeout, renderInterval).Should(gomega.BeTrue())
	})

	ginkgo.It("[drill 3] renders the stored targetContainers reference as main under the old controller", func() {
		// The old controller does not know targetContainers, so its render
		// drops the patch dimension while keeping everything the pure
		// compat shape produces: the sidecar stays, the profile's patch
		// silently stops firing. This is the silent feature loss the drill
		// pins — asserted through what IS observable, on the rendered
		// child.
		expected := before.patched.DeepCopy()
		nginx := findContainer(expected, "nginx")
		filtered := nginx.Env[:0]
		for _, ev := range nginx.Env {
			if ev.Name != profilePatchEnv {
				filtered = append(filtered, ev)
			}
		}
		nginx.Env = filtered

		gomega.Eventually(func(g gomega.Gomega) corev1.PodTemplateSpec {
			tpl, err := tryComponentTemplate(f, fxStandalone, soloRoleName)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			return *tpl
		}, renderTimeout, renderInterval).Should(gomega.Equal(*expected),
			"the old controller must render the stored reference as main: fragments delivered, patches gone")

		tpl := componentTemplate(roleInstanceSet(f, fxStandalone), soloRoleName)
		gomega.Expect(hasContainer(tpl, patchesSidecar)).To(gomega.BeTrue(),
			"the old controller must still deliver the profile's sidecar")

		// Nothing pruned the stored spec: the CRDs are still the new ones,
		// so the reference survives the window exactly as written — the
		// loss is in the old controller's render, not in storage.
		stored, err := storedEngineRuntime(f, fxStandalone, 0)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(stored).To(gomega.HaveKey("targetContainers"),
			"the stored spec must keep targetContainers through the window")
		gomega.Expect(stored).ToNot(gomega.HaveKey("component"))
	})

	ginkgo.It("[drill 1] stores an overlapping same-profile pair through the old-controller window", func() {
		// The one pruning shape that surfaces (KEP-531): the pre-upgrade
		// prune left an unscoped reference — effective set {leader} under
		// leaderWorkerPattern — and the old controller's webhook has no
		// scope-ownership rule, so a scoped reference to the leader of the
		// same profile can be written through the window. The new
		// controller's render must reject the pair; that is the next
		// phase's assertion.
		err := appendEngineRuntime(f, fxPruned, workloadsv1alpha2.EngineRuntime{
			ProfileName: profRuntime,
			Component:   string(constants.LeaderComponentType),
		})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(),
			"the old controller's webhook must admit the overlapping same-profile pair; the new "+
				"validation is what would reject it, and it is out of the path in this window")

		stored, err := storedEngineRuntimes(f, fxPruned)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(stored).To(gomega.HaveLen(2), "expected the pair to be stored")

		gomega.Expect(stored[0]).To(gomega.HaveKeyWithValue("profileName", profRuntime))
		gomega.Expect(stored[0]).ToNot(gomega.HaveKey("component"),
			"the pruned reference must stay unscoped in storage")
		gomega.Expect(stored[1]).To(gomega.HaveKeyWithValue("profileName", profRuntime))
		gomega.Expect(stored[1]).To(gomega.HaveKeyWithValue("component", string(constants.LeaderComponentType)),
			"the scoped reference must keep its component through the old-controller window")
	})

	// ---------------------------------------------------------------------------
	// Phase 5: the re-upgrade and the re-applied webhook configuration.
	// ---------------------------------------------------------------------------

	ginkgo.It("[window] re-upgrades the controller image to the version under test", func() {
		swapControllerImage(f, upgrade.ToTag(), fxPruned)
	})

	ginkgo.It("[drill 5] proves the re-upgraded image alone does not re-enable admission", func() {
		// The controller never creates ValidatingWebhookConfigurations —
		// it only patches caBundle — so a re-upgraded image with the entry
		// still missing must leave CERP writes unvalidated.
		err := updateProfileSpec(f, profFrozen, func(spec *workloadsv1alpha2.ClusterEngineRuntimeProfileSpec) {
			spec.Containers = append(spec.Containers, corev1.Container{
				Name: frozenSidecar + "-window2", Image: utils.DefaultImage,
			})
		})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(),
			"the re-upgraded controller must not re-enable CERP admission by itself")
	})

	ginkgo.It("[drill 5] re-applies the ValidatingWebhookConfiguration from the release artifacts", func() {
		reapplyValidatingWebhookConfiguration(f)

		// The chart ships the entries with an empty caBundle; the running
		// controller's WebhookCertReconciler watches this object by name
		// and re-patches it on this very update. Until every entry carries
		// the CA, admission calls fail closed (failurePolicy: Fail).
		upgrade.WaitValidatingWebhookCABundle(f)
	})

	ginkgo.It("[drill 5] asserts enforcement returned", func() {
		err := updateProfileSpec(f, profFrozen, func(spec *workloadsv1alpha2.ClusterEngineRuntimeProfileSpec) {
			spec.Containers = append(spec.Containers, corev1.Container{
				Name: frozenSidecar + "-post", Image: utils.DefaultImage,
			})
		})
		gomega.Expect(err).To(gomega.HaveOccurred(),
			"a spec edit of a referenced profile must be rejected once the configuration is re-applied")
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("is frozen"))

		err = f.Client.Delete(f.Ctx, cerpObject(profFrozen))
		gomega.Expect(err).To(gomega.HaveOccurred(),
			"deleting a referenced profile must be rejected once the configuration is re-applied")
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("is frozen"))
	})

	ginkgo.It("[drill 1] fails the overlapping pair at render with EngineRuntimeRenderFailed", func() {
		// The webhook-absent backstop: the pair was stored while the new
		// validation was out of the path, and the render-time re-run of the
		// shared validation is the authoritative gate that catches it.
		gomega.Eventually(func(g gomega.Gomega) string {
			found, msg := engineRuntimeEvent(f, fxPruned, discovery.ReasonEngineRuntimeRenderFailed)
			g.Expect(found).To(gomega.BeTrue(),
				"no %s event on %s yet", discovery.ReasonEngineRuntimeRenderFailed, fxPruned)
			return msg
		}, renderTimeout, renderInterval).Should(gomega.ContainSubstring("covered by both"),
			"the new controller must fail the overlapping same-profile pair at render")

		// The failed re-render leaves the child exactly as the window
		// left it: no partial template, no new generation. The pair the
		// window wrote never changed the child either — its overlapping
		// scopes fail the same shared validation at render on both the old
		// and the new controller, so the child the window sampled is the
		// one the failed re-render must leave.
		ris := roleInstanceSet(f, fxPruned)
		gomega.Expect(ris.Generation).To(gomega.Equal(before.windowGeneration),
			"a failed render must not rewrite the RoleInstanceSet")
		gomega.Expect(ris.Spec).To(gomega.Equal(before.windowSpec),
			"a failed render must leave the RoleInstanceSet spec untouched")
	})

	ginkgo.It("[drill 3] fires the profile-side patches again under the new controller", func() {
		gomega.Eventually(func(g gomega.Gomega) corev1.PodTemplateSpec {
			tpl, err := tryComponentTemplate(f, fxStandalone, soloRoleName)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			return *tpl
		}, renderTimeout, renderInterval).Should(gomega.Equal(before.patched),
			"the re-upgraded controller must render the stored reference with its patches firing again")
	})
}

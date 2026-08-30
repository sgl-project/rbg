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

package upgrade

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// settleDuration is how far apart the two samples that have to agree are taken, and how
// long the controller is given to act after a write elsewhere in the suite.
//
// This is the single most important detail in the suite. Without it, a regression
// that rolls pods would still pass whenever the sample happened to land in the gap
// between "deployment is ready" and "the new controller finished its first
// reconcile of the existing objects". Two samples that agree, taken this far apart,
// is the evidence that the controller has looked at these objects and left them
// alone -- not that it had not looked yet.
const settleDuration = 30 * time.Second

// RunUpgradeSpecs is the body of the Ordered container. The specs are phases of one
// experiment and share fixtures, so they must run in order and none of them may be
// run in isolation.
func RunUpgradeSpecs(f *framework.Framework) {
	var (
		rbgs   []*workloadsv1alpha2.RoleBasedGroup
		rbgSet *workloadsv1alpha2.RoleBasedGroupSet
		legacy *workloadsv1alpha1.RoleBasedGroup
		// pending never becomes ready and legacySet is written through v1alpha1. Both
		// are kept out of the rbgs list because phase 1 treats that list uniformly and
		// neither of these is a plain ready-by-the-end RoleBasedGroup.
		pending   *workloadsv1alpha2.RoleBasedGroup
		legacySet *workloadsv1alpha1.RoleBasedGroupSet

		// before is the pre-upgrade snapshot every phase 3 assertion compares against.
		before map[string]RBGSnapshot
		// preUpgradeMark bounds the event search: only pod-removal events recorded
		// after this point can have been caused by the upgrade.
		preUpgradeMark metav1.Time
		// mutated collects RBGs whose churn a spec asked for -- phase 2's mid-rollout
		// fixture and the objects phase 4 disturbs -- so the "everything else is still
		// untouched" checks can exclude them.
		mutated []string
		// midRollPodsBefore are the mid-rollout fixture's pods as they were before phase 2
		// started rolling it. Every one of them must be gone once the partition is lifted
		// and the rollout converges, which is the only way to tell a finished rollout from
		// a stalled one.
		midRollPodsBefore map[string]PodFacts
		// midRollPodsAtPartition are the same pods once the rollout has halted at the
		// partition, immediately before helm runs: the instances still at the old revision
		// plus the ones already replaced. The upgrade must leave all of them alone, which
		// is a claim midRollPodsBefore cannot express because part of that set is meant to
		// have been replaced by then.
		midRollPodsAtPartition map[string]PodFacts
	)

	ginkgo.BeforeAll(
		func() {
			rbgs = buildFixtures(f.Namespace)
			rbgSet = buildSetFixture(f.Namespace)
			legacy = buildV1alpha1Fixture(f.Namespace)
			pending = buildPendingFixture(f.Namespace)
			legacySet = buildV1alpha1SetFixture(f.Namespace)
		},
	)

	// The debug dump is wired here rather than through f.RegisterDebugFn, which only
	// runs from f.AfterEach() -- and this suite calls that once in AfterSuite, where
	// the spec report is no longer the failed spec's.
	ginkgo.AfterEach(
		func() {
			if ginkgo.CurrentSpecReport().Failed() {
				dumpUpgradeDebugInfo(f, &before)
			}
		},
	)

	// ---------------------------------------------------------------------------
	// Phase 1: build the "already running on v0.7.0" world.
	// ---------------------------------------------------------------------------

	ginkgo.It(
		"[phase 1] rejects fields the v0.7.0 CRDs do not know", func() {
			// Positive proof that the fixtures really are being written against the
			// old schema. The preflight infers this from the absent warmup CRD; this
			// observes the pruning itself, which is the property phase 3 depends on:
			// if a v0.8.0-only field survived here, the fixtures would not be
			// v0.7.0-shaped and the whole comparison would be measuring nothing.
			probe := wrappersv2.BuildBasicRoleBasedGroup("up-prune-probe", f.Namespace).
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildLeaderWorkerRole("probe").
							WithReplicas(1).
							WithSize(2).
							WithRestartPolicy(workloadsv1alpha2.RestartPolicyNone).
							WithBaseDelaySeconds(7).
							Obj(),
					},
				).Obj()

			gomega.Expect(f.Client.Create(f.Ctx, probe)).To(gomega.Succeed())
			ginkgo.DeferCleanup(
				func() {
					gomega.Expect(client.IgnoreNotFound(f.Client.Delete(f.Ctx, probe))).To(gomega.Succeed())
				},
			)

			stored := &workloadsv1alpha2.RoleBasedGroup{}
			gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(probe), stored)).To(gomega.Succeed())
			gomega.Expect(stored.Spec.Roles).To(gomega.HaveLen(1))
			gomega.Expect(stored.Spec.Roles[0].LeaderWorkerPattern).ToNot(gomega.BeNil())
			gomega.Expect(stored.Spec.Roles[0].LeaderWorkerPattern.RestartPolicyConfig).To(
				gomega.BeNil(),
				"restartPolicyConfig survived, so this cluster is not running the v0.7.0 CRDs and "+
					"the fixtures would not be v0.7.0-shaped",
			)
		},
	)

	ginkgo.It(
		"[phase 1] creates RoleBasedGroups on v0.7.0 and waits for every role to be ready", func() {
			for _, rbg := range rbgs {
				ginkgo.By("creating " + rbg.Name)
				gomega.Expect(f.Client.Create(f.Ctx, rbg)).To(gomega.Succeed())
			}
			gomega.Expect(f.Client.Create(f.Ctx, rbgSet)).To(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, legacy)).To(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, legacySet)).To(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, pending)).To(gomega.Succeed())

			for _, rbg := range rbgs {
				ginkgo.By("waiting for " + rbg.Name + " to be ready")
				f.ExpectRbgV2Equal(rbg)
			}
			f.ExpectRbgV2ScalingAdapterEqual(findFixture(rbgs, fxScaling))
			f.ExpectRbgSetV2Equal(rbgSet)
			f.ExpectRbgEqual(legacy)
			f.ExpectRbgSetEqual(legacySet)

			// The two ExpectRbgSet*Equal helpers only wait for the child RBGs to exist,
			// so without this the snapshot could be taken while a child's pods are still
			// starting -- and a baseline of a half-started world proves nothing.
			ginkgo.By("waiting for every RoleBasedGroup in the namespace to be ready")
			waitAllRBGsReady(f, fxPending)

			// And the gate above is not enough on its own, because an RBG's Ready
			// condition is only as good as the workload status it is derived from. This
			// waits on the pods themselves: all of them present, all of them Running.
			ginkgo.By("waiting for every pod the snapshot will record to be Running")
			waitAllPodsRunning(f, fxPending)

			// The never-ready fixture is still required to have got as far as a Pending
			// pod. Without that it would contribute nothing to compare, and "the upgrade
			// left the Pending pod alone" would be a claim about an empty set.
			ginkgo.By("waiting for " + fxPending + " to have a Pending pod")
			waitForPendingPod(f, pending)
		},
	)

	ginkgo.It(
		"[phase 1] records the running pods", func() {
			// Marked before the snapshot, not after, so no removal event can slip
			// into the untracked window between the two.
			preUpgradeMark = metav1.Now()
			before = captureAll(gomega.Default, f)

			gomega.Expect(before).ToNot(gomega.BeEmpty(), "no RoleBasedGroups were captured")

			// Without this, "nothing churned" would also hold for a namespace whose
			// pods never started, which is the way this suite could most easily pass
			// while proving nothing.
			var problems []string
			for name, snap := range before {
				// The never-ready fixture is exempt: its pod cannot be scheduled by
				// construction. It stays in the snapshot, because a Pending pod is state
				// the upgrade must leave alone too -- it just cannot be Running.
				if name == fxPending {
					continue
				}
				for role, pods := range snap.Roles {
					running := 0
					for _, facts := range pods {
						if facts.Phase == corev1.PodRunning {
							running++
						}
					}
					if running == 0 {
						problems = append(
							problems, fmt.Sprintf(
								"%s/%s has no Running pod (%d pods total)", name, role, len(pods),
							),
						)
					}
				}
			}
			reportProblems("some roles have nothing running, so there is nothing to observe", problems)

			ginkgo.By(fmt.Sprintf("captured %d RoleBasedGroups before the upgrade", len(before)))
			printSnapshots(before)
		},
	)

	// ---------------------------------------------------------------------------
	// Phase 2: perform the upgrade.
	// ---------------------------------------------------------------------------

	ginkgo.It(
		"[phase 2] upgrades the release to the version under test", func() {
			// The upgrade deliberately lands on a cluster that is not at rest. Every other
			// fixture is converged and quiet by now, which is the one state a real upgrade
			// is least likely to arrive in, and a controller that mishandles a half-rolled
			// workload would leave it stuck rather than churn anything else.
			//
			// The rollout is started and then allowed to halt at its partition before helm
			// runs, so the upgrade is guaranteed to land on mixed revisions. Racing a
			// deliberately slow rollout against helm was the earlier approach and it could
			// only report the overlap afterwards, because how long helm takes is not
			// something this suite controls.
			target := findFixture(rbgs, fxMidRoll)
			mutated = append(mutated, target.Name)
			midRollPodsBefore = listPodFactsForRole(gomega.Default, f, target, midRollRole)
			gomega.Expect(midRollPodsBefore).To(gomega.HaveLen(midRollReplicas))

			ginkgo.By("starting a rollout of " + target.Name)
			startMidRollout(f, target)

			ginkgo.By(fmt.Sprintf("waiting for the rollout to halt with %d instances still at the old revision", midRollPartition))
			gomega.Eventually(
				func(g gomega.Gomega) {
					pods := listPodFactsForRole(g, f, target, midRollRole)
					g.Expect(pods).To(gomega.HaveLen(midRollReplicas))
					// Counted by UID, not by name: a RoleInstanceSet gives its pods stable
					// names, so a replaced pod comes back under the name it had and
					// counting names would report a rollout that never started.
					g.Expect(countSurvivors(midRollPodsBefore, pods)).To(
						gomega.Equal(midRollPartition),
						"the rollout did not halt at the partition",
					)
					for name, facts := range pods {
						g.Expect(facts.Phase).To(gomega.Equal(corev1.PodRunning), "pod %s is %s", name, facts.Phase)
					}
				}, utils.Timeout, utils.Interval,
			).Should(gomega.Succeed())
			midRollPodsAtPartition = listPodFactsForRole(gomega.Default, f, target, midRollRole)

			runHelmUpgrade(f)

			// Asserted rather than reported: the partition holds the fixture here for as
			// long as it takes, so the upgrade cannot have missed the half-rolled state.
			//
			// This also doubles as the check that the halt was real. The wait above can be
			// satisfied by a rollout that is merely between two instances at the moment it
			// samples, whereas this spans the whole helm upgrade, which is far longer than
			// replacing one nginx pod. So a count that has moved means one of two things,
			// and the failure cannot say which: the upgrade rolled instances the partition
			// had pinned, or the partition never pinned them.
			gomega.Expect(
				countSurvivors(midRollPodsBefore, listPodFactsForRole(gomega.Default, f, target, midRollRole)),
			).To(
				gomega.Equal(midRollPartition),
				"instances that partition %d should have pinned at the old revision were rolled, either by "+
					"the upgrade or because the partition never held", midRollPartition,
			)

			// The conversion round-trip gate needs an object that already exists and
			// is reachable through both API versions; the v1alpha1 fixture is it.
			waitForUpgradeReady(f, fxV1alpha1)
		},
	)

	// ---------------------------------------------------------------------------
	// Phase 3: the actual claim.
	// ---------------------------------------------------------------------------

	ginkgo.It(
		"[phase 3] leaves every pod that was already running untouched", func() {
			// The comparison below is only meaningful once the controller has stopped
			// changing things: until then a difference against `before` could just as
			// well be the sampling moment as the upgrade. waitQuiesced establishes that
			// and hands back the sample it established it with.
			//
			// The mid-rollout fixture is excluded from the quiescence check: it is
			// legitimately moving, so leaving it in would never let the wait succeed.
			ginkgo.By(fmt.Sprintf("waiting for two samples %s apart to agree", settleDuration))
			after := waitQuiesced(f, mutated...)

			fs := &findings{}
			runDetectors(fs, f, before, after, preUpgradeMark, upgradeRewrites.acrossStarts(controllerStarts), mutated)
			fs.report()
		},
	)

	ginkgo.It(
		"[phase 3] leaves the stored spec of v0.7.0 objects as it was written", func() {
			// The new CRDs added restartPolicyConfig alongside the deprecated
			// restartPolicy string. Because restartPolicyConfig itself carries no
			// default, an object that never set it gets nothing defaulted onto it --
			// so the stored spec must still be exactly the v0.7.0 shape. A rewrite
			// here would be the mechanism behind a revision-hash change, and it
			// would show up in this assertion before the pods finished moving.
			lwp := &workloadsv1alpha2.RoleBasedGroup{}
			gomega.Expect(
				f.Client.Get(f.Ctx, client.ObjectKey{Namespace: f.Namespace, Name: fxLwp}, lwp),
			).To(gomega.Succeed())
			gomega.Expect(lwp.Spec.Roles).To(gomega.HaveLen(1))
			pattern := lwp.Spec.Roles[0].LeaderWorkerPattern
			gomega.Expect(pattern).ToNot(gomega.BeNil())
			gomega.Expect(pattern.RestartPolicy).To( //nolint:staticcheck // asserting the v0.7.0 wire shape
				gomega.Equal(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart),
				"the deprecated restartPolicy string was rewritten",
			)
			gomega.Expect(pattern.RestartPolicyConfig).To(
				gomega.BeNil(), "restartPolicyConfig was written onto an object that never had it",
			)

			cc := &workloadsv1alpha2.RoleBasedGroup{}
			gomega.Expect(
				f.Client.Get(f.Ctx, client.ObjectKey{Namespace: f.Namespace, Name: fxCustom}, cc),
			).To(gomega.Succeed())
			gomega.Expect(cc.Spec.Roles).To(gomega.HaveLen(1))
			ccPattern := cc.Spec.Roles[0].CustomComponentsPattern
			gomega.Expect(ccPattern).ToNot(gomega.BeNil())
			gomega.Expect(ccPattern.RestartPolicy).To( //nolint:staticcheck // asserting the v0.7.0 wire shape
				gomega.Equal(workloadsv1alpha2.RestartPolicyNone),
				"the deprecated restartPolicy string was rewritten",
			)
			gomega.Expect(ccPattern.RestartPolicyConfig).To(
				gomega.BeNil(), "restartPolicyConfig was written onto an object that never had it",
			)
		},
	)

	ginkgo.It(
		"[phase 3] leaves a half-rolled workload alone and still finishes its rollout", func() {
			// Two separate claims about the fixture the upgrade caught at mixed revisions.
			//
			// First, that the upgrade did not touch it. The partition had it stopped, so
			// "stopped" is the state that has to survive: neither the instances pinned at
			// the old revision nor the ones already replaced may move. That is the same
			// pod-level standard every quiet fixture is held to -- churn, restarts and
			// metadata, not merely a count of survivors -- and it is only reachable
			// because the rollout is halted rather than racing helm. It cannot be folded
			// into the comparisons against `before`, which predates the deliberate churn.
			//
			// The object-level detectors are deliberately not run here: setting the
			// partition rewrote the RoleInstanceSet spec on purpose, so its generation and
			// revisions moved for a reason that has nothing to do with the upgrade.
			//
			// Second, that lifting the partition still converges. A controller that dropped
			// the rollout on the floor -- or advanced currentRevision while the partition
			// was still in force, so the remaining instances no longer look out of date --
			// passes the first claim and fails this one.
			target := findFixture(rbgs, fxMidRoll)

			ginkgo.By("checking the upgrade left the half-rolled pods untouched")
			fs := &findings{}
			atPartition := roleSnapshot(target.Name, midRollRole, midRollPodsAtPartition)
			nowSnap := roleSnapshot(
				target.Name, midRollRole, listPodFactsForRole(gomega.Default, f, target, midRollRole),
			)
			checkNoPodChurn(fs, atPartition, nowSnap)
			checkNoRestarts(fs, atPartition, nowSnap)
			checkPodMetadataStable(fs, atPartition, nowSnap)
			fs.report()

			ginkgo.By("lifting the partition so the rollout can finish")
			releaseMidRollout(f, target)

			// Replacement is checked by UID under the same name, because a RoleInstanceSet
			// brings a replaced pod back under the name it had.
			gomega.Eventually(
				func(g gomega.Gomega) {
					pods := listPodFactsForRole(g, f, target, midRollRole)
					g.Expect(pods).To(gomega.HaveLen(midRollReplicas))
					g.Expect(countSurvivors(midRollPodsBefore, pods)).To(
						gomega.BeZero(), "the rollout is stuck part-way through",
					)
					for name, facts := range pods {
						g.Expect(facts.Phase).To(gomega.Equal(corev1.PodRunning), "pod %s is %s", name, facts.Phase)
					}
				}, utils.Timeout, utils.Interval,
			).Should(gomega.Succeed())

			f.ExpectRbgV2Equal(target)
		},
	)

	ginkgo.It(
		"[phase 3] changes nothing when the upgraded controller starts from cold", func() {
			// The controller process that upgraded these objects has been running since
			// before it last reconciled them, so everything it holds in memory -- informer
			// caches, the revision hashes it computed -- was built while the upgrade was
			// happening. A fresh process rebuilds all of it from what is stored, which is
			// what every later restart of the Deployment does. A rewrite that only happens
			// on a cold cache is invisible until then.
			//
			// The baseline is taken now rather than reusing `before`, and only what a
			// controller start is itself known to rewrite is tolerated. The upgrade's own
			// changes do not apply: the shared Service selector was already narrowed by
			// the hop, so narrowing it again here would be a finding.
			mark := metav1.Now()
			baseline := captureAll(gomega.Default, f)

			restartController(f, fxV1alpha1)

			ginkgo.By(fmt.Sprintf("letting the restarted controller settle for %s", settleDuration))
			time.Sleep(settleDuration)

			fs := &findings{}
			runDetectors(fs, f, baseline, captureAll(gomega.Default, f), mark, controllerStartRewrites.acrossStarts(1), nil)
			fs.report()
		},
	)

	ginkgo.It(
		"[phase 3] changes nothing when the same helm upgrade is run again", func() {
			// A release is upgraded more than once over its life, and the second run of the
			// same chart has to be a no-op. It also separates two explanations of the clean
			// result above that otherwise look identical: that the new controller does not
			// rewrite these objects, and that the rewrite already happened during the first
			// upgrade and simply cannot happen twice.
			//
			// This re-runs the crd-upgrade hook as well, which is the part that drops the
			// conversion caBundle while it replaces the CRDs.
			mark := metav1.Now()
			baseline := captureAll(gomega.Default, f)
			startsBefore := controllerStarts

			runHelmUpgrade(f)
			waitForUpgradeReady(f, fxV1alpha1)

			ginkgo.By(fmt.Sprintf("letting the controller settle for %s", settleDuration))
			time.Sleep(settleDuration)

			fs := &findings{}
			// Identical values leave the controller Deployment alone, so this normally
			// spans no controller start at all and tolerates nothing. Taking the delta
			// rather than asserting zero keeps the spec measuring what happened: if the
			// chart does replace the pods, the per-start rewrite is expected and the rest
			// of the interval is still held to changing nothing.
			runDetectors(
				fs, f, baseline, captureAll(gomega.Default, f), mark,
				controllerStartRewrites.acrossStarts(controllerStarts-startsBefore), nil,
			)
			fs.report()
		},
	)

	// ---------------------------------------------------------------------------
	// Phase 4: the upgraded controller is not merely quiet -- it still works.
	//
	// "Nothing churned" is also true of a controller that crashed on startup or
	// silently stopped reconciling these objects, which would be a worse outcome
	// than a rollout. These specs rule that out. Each one disturbs an RBG on
	// purpose, so that RBG is added to `mutated` and excluded from the later
	// "everything else is still untouched" check.
	// ---------------------------------------------------------------------------

	ginkgo.It(
		"[phase 4] still recreates a pod that is deleted after the upgrade", func() {
			target := findFixture(rbgs, fxDeploy)
			roleName := target.Spec.Roles[0].Name
			mutated = append(mutated, target.Name)

			podsBefore := listPodFactsForRole(gomega.Default, f, target, roleName)
			gomega.Expect(podsBefore).ToNot(gomega.BeEmpty())

			var victim string
			for name := range podsBefore {
				victim = name
				break
			}
			ginkgo.By("deleting pod " + victim)
			gomega.Expect(
				f.Client.Delete(
					f.Ctx, &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{Namespace: f.Namespace, Name: victim},
					},
				),
			).To(gomega.Succeed())

			// The victim is checked by name rather than the whole set, because a
			// replacement legitimately arrives either under a fresh generated name or
			// under the same stable name with a new UID, and the surviving replicas of
			// a multi-replica role must still compare equal.
			gomega.Eventually(
				func(g gomega.Gomega) {
					pods := listPodFactsForRole(g, f, target, roleName)
					g.Expect(pods).To(gomega.HaveLen(len(podsBefore)))
					current, stillThere := pods[victim]
					g.Expect(stillThere && current.UID == podsBefore[victim].UID).To(
						gomega.BeFalse(), "pod %s is still the deleted one", victim,
					)
				}, utils.Timeout, utils.Interval,
			).Should(gomega.Succeed(), "the deleted pod was not replaced")

			f.ExpectRbgV2Equal(target)
		},
	)

	ginkgo.It(
		"[phase 4] still scales a role that v0.7.0 created", func() {
			target := findFixture(rbgs, fxStandalone)
			roleName := target.Spec.Roles[0].Name
			mutated = append(mutated, target.Name)

			ginkgo.By("scaling " + roleName + " to 3")
			scaleRole(f, target, roleName, 3)
			gomega.Eventually(
				func(g gomega.Gomega) {
					g.Expect(listPodFactsForRole(g, f, target, roleName)).To(gomega.HaveLen(3))
				}, utils.Timeout, utils.Interval,
			).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(target)

			ginkgo.By("scaling " + roleName + " back to 2")
			scaleRole(f, target, roleName, 2)
			gomega.Eventually(
				func(g gomega.Gomega) {
					g.Expect(listPodFactsForRole(g, f, target, roleName)).To(gomega.HaveLen(2))
				}, utils.Timeout, utils.Interval,
			).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(target)
		},
	)

	ginkgo.It(
		"[phase 4] rolls only the role whose template actually changed", func() {
			// This is the counterpart to phase 3: it shows the clean result there was
			// not simply an upgraded controller that never rolls anything. The change
			// goes into the shared roleTemplate, which only the dependent role
			// references, so the sibling role must not move.
			target := findFixture(rbgs, fxDeps)
			mutated = append(mutated, target.Name)

			beforeA := listPodFactsForRole(gomega.Default, f, target, roleA)
			beforeB := listPodFactsForRole(gomega.Default, f, target, roleB)
			gomega.Expect(beforeA).ToNot(gomega.BeEmpty())
			gomega.Expect(beforeB).ToNot(gomega.BeEmpty())

			ginkgo.By("changing the container env of roleTemplate " + sharedTemplateName)
			gomega.Expect(
				retry.RetryOnConflict(
					retry.DefaultRetry, func() error {
						live := &workloadsv1alpha2.RoleBasedGroup{}
						if err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(target), live); err != nil {
							return err
						}
						for i := range live.Spec.RoleTemplates {
							if live.Spec.RoleTemplates[i].Name != sharedTemplateName {
								continue
							}
							containers := live.Spec.RoleTemplates[i].Template.Spec.Containers
							gomega.Expect(containers).ToNot(gomega.BeEmpty())
							containers[0].Env = append(
								containers[0].Env,
								corev1.EnvVar{Name: "UPGRADE_E2E_ROLL", Value: "1"},
							)
						}
						return f.Client.Update(f.Ctx, live)
					},
				),
			).To(gomega.Succeed())

			gomega.Eventually(
				func(g gomega.Gomega) {
					pods := listPodFactsForRole(g, f, target, roleB)
					g.Expect(pods).To(gomega.HaveLen(len(beforeB)))
					for name, facts := range pods {
						old, existed := beforeB[name]
						g.Expect(existed && old.UID == facts.UID).To(
							gomega.BeFalse(), "pod %s of role %s has not been replaced yet", name, roleB,
						)
					}
				}, utils.Timeout, utils.Interval,
			).Should(gomega.Succeed(), "role %s did not roll after its template changed", roleB)

			fs := &findings{}

			ginkgo.By("checking the sibling role did not move")
			checkNoPodChurn(
				fs,
				roleSnapshot(target.Name, roleA, beforeA),
				roleSnapshot(target.Name, roleA, listPodFactsForRole(gomega.Default, f, target, roleA)),
			)

			ginkgo.By("checking every other RoleBasedGroup is still untouched")
			after := exclude(captureAll(gomega.Default, f), mutated...)
			baseline := exclude(before, mutated...)
			checkSameRBGSet(fs, baseline, after)
			checkNoPodChurn(fs, baseline, after)
			checkNoRestarts(fs, baseline, after)
			checkOwnersStable(fs, baseline, after, upgradeRewrites.acrossStarts(controllerStarts).generationBumps)
			fs.report()
		},
	)

	ginkgo.It(
		"[phase 4] does not disturb a v1alpha1 object updated through the v1alpha1 API", func() {
			// Reading the stored v1alpha2 object exercises conversion one way; this
			// spec exercises the write direction, which is where the changed conversion
			// code can actually rewrite what is stored. A user's first post-upgrade
			// `kubectl edit` on a v1alpha1 RBG takes exactly this path: v1alpha2 stored
			// -> v1alpha1 read -> v1alpha1 write -> v1alpha2 stored. The edit itself is
			// metadata-only, so any spec difference that comes back is the webhook's.
			mutated = append(mutated, fxV1alpha1)

			key := client.ObjectKey{Namespace: f.Namespace, Name: fxV1alpha1}
			storedBefore := &workloadsv1alpha2.RoleBasedGroup{}
			gomega.Expect(f.Client.Get(f.Ctx, key, storedBefore)).To(gomega.Succeed())

			// The baseline is taken here rather than reused from `before`: the interval
			// since the pre-upgrade snapshot has already been attributed by phase 3, and
			// comparing across it again would report anything it found as something this
			// round-trip did.
			baseline := only(captureAll(gomega.Default, f), fxV1alpha1)
			gomega.Expect(baseline[fxV1alpha1].Roles[v1alpha1LwsRole]).ToNot(
				gomega.BeEmpty(), "role %s of %s has no pods to watch", v1alpha1LwsRole, fxV1alpha1,
			)

			ginkgo.By("annotating " + fxV1alpha1 + " through the v1alpha1 API")
			gomega.Expect(
				retry.RetryOnConflict(
					retry.DefaultRetry, func() error {
						live := &workloadsv1alpha1.RoleBasedGroup{}
						if err := f.Client.Get(f.Ctx, key, live); err != nil {
							return err
						}
						if live.Annotations == nil {
							live.Annotations = map[string]string{}
						}
						live.Annotations["upgrade-e2e/roundtrip"] = "1"
						return f.Client.Update(f.Ctx, live)
					},
				),
			).To(gomega.Succeed())

			fs := &findings{}

			ginkgo.By("checking the round-trip did not rewrite the stored spec")
			storedAfter := &workloadsv1alpha2.RoleBasedGroup{}
			gomega.Expect(f.Client.Get(f.Ctx, key, storedAfter)).To(gomega.Succeed())
			// Collected rather than asserted: what a rewrite does to the running pods is
			// the question this spec exists to answer, and failing here would skip the
			// checks below that answer it.
			if diff := storedSpecDiff(storedBefore.Spec, storedAfter.Spec); diff != "" {
				fs.add(
					"the v1alpha1 write path rewrote the stored v1alpha2 spec, which changes what the "+
						"controller hashes",
					[]string{diff},
				)
			}

			// A rewrite would take a moment to reach the pods, so the pod checks are
			// given the settle window rather than being read straight back.
			ginkgo.By(fmt.Sprintf("letting the controller settle for %s", settleDuration))
			time.Sleep(settleDuration)

			settled := only(captureAll(gomega.Default, f), fxV1alpha1)
			checkNoPodChurn(fs, baseline, settled)
			checkNoRestarts(fs, baseline, settled)
			checkPodMetadataStable(fs, baseline, settled)
			fs.report()
			f.ExpectRbgEqual(legacy)
		},
	)

	ginkgo.It(
		"[phase 4] does not disturb a v1alpha1 RoleBasedGroupSet updated through the v1alpha1 API", func() {
			// RoleBasedGroupSet.ConvertTo runs the same convertSpecV1alpha1ToV2 the
			// RoleBasedGroup conversion runs, one level down. A rewrite therefore lands in
			// the set's groupTemplate, and from there in every child RoleBasedGroup the set
			// stamps out -- so it reaches more pods than the RoleBasedGroup case does, and
			// it was the half of that shared path nothing covered.
			children := childRBGNames(f, fxV1alpha1Set)
			gomega.Expect(children).ToNot(
				gomega.BeEmpty(), "the set has no child RoleBasedGroup, so there is nothing to observe",
			)
			mutated = append(mutated, fxV1alpha1Set)
			mutated = append(mutated, children...)

			key := client.ObjectKey{Namespace: f.Namespace, Name: fxV1alpha1Set}
			storedBefore := &workloadsv1alpha2.RoleBasedGroupSet{}
			gomega.Expect(f.Client.Get(f.Ctx, key, storedBefore)).To(gomega.Succeed())

			// Taken here rather than reused from `before`, for the same reason as the
			// RoleBasedGroup spec above: phase 3 already accounted for the upgrade, and
			// this spec is only allowed to speak about the write it performs.
			baseline := only(captureAll(gomega.Default, f), children...)

			ginkgo.By("annotating " + fxV1alpha1Set + " through the v1alpha1 API")
			gomega.Expect(
				retry.RetryOnConflict(
					retry.DefaultRetry, func() error {
						live := &workloadsv1alpha1.RoleBasedGroupSet{}
						if err := f.Client.Get(f.Ctx, key, live); err != nil {
							return err
						}
						if live.Annotations == nil {
							live.Annotations = map[string]string{}
						}
						live.Annotations["upgrade-e2e/roundtrip"] = "1"
						return f.Client.Update(f.Ctx, live)
					},
				),
			).To(gomega.Succeed())

			fs := &findings{}

			ginkgo.By("checking the round-trip did not rewrite the stored spec")
			storedAfter := &workloadsv1alpha2.RoleBasedGroupSet{}
			gomega.Expect(f.Client.Get(f.Ctx, key, storedAfter)).To(gomega.Succeed())
			// Collected rather than asserted, for the same reason as the RoleBasedGroup
			// spec above: what a rewrite does to the running pods is the question, and
			// failing here would skip the checks that answer it.
			if diff := storedSpecDiff(storedBefore.Spec, storedAfter.Spec); diff != "" {
				fs.add(
					"the v1alpha1 write path rewrote the stored v1alpha2 RoleBasedGroupSet spec, which the "+
						"set copies into every child RoleBasedGroup",
					[]string{diff},
				)
			}

			ginkgo.By(fmt.Sprintf("letting the controller settle for %s", settleDuration))
			time.Sleep(settleDuration)

			settled := only(captureAll(gomega.Default, f), children...)
			checkNoPodChurn(fs, baseline, settled)
			checkNoRestarts(fs, baseline, settled)
			checkPodMetadataStable(fs, baseline, settled)
			fs.report()
			f.ExpectRbgSetEqual(legacySet)
		},
	)

	ginkgo.It(
		"[phase 4] accepts the RoleBasedGroupWarmup API the upgrade introduced", func() {
			// A shallow check on purpose: the point is that the new API surface is
			// reachable on an upgraded cluster, not that warmup works -- the main e2e
			// suite owns that.
			target := findFixture(rbgs, fxSts)
			warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
				ObjectMeta: metav1.ObjectMeta{Namespace: f.Namespace, Name: "up-warmup"},
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{
						TTLSecondsAfterFinished: ptr.To(int32(60)),
					},
					TargetRoleBasedGroup: &workloadsv1alpha2.TargetRoleBasedGroup{
						Name: target.Name,
						Roles: map[string]workloadsv1alpha2.WarmupActions{
							target.Spec.Roles[0].Name: {
								ImagePreload: &workloadsv1alpha2.ImagePreloadAction{
									Images: []string{utils.DefaultImage},
								},
							},
						},
					},
				},
			}
			gomega.Expect(f.Client.Create(f.Ctx, warmup)).To(gomega.Succeed())
			ginkgo.DeferCleanup(
				func() {
					gomega.Expect(client.IgnoreNotFound(f.Client.Delete(f.Ctx, warmup))).To(gomega.Succeed())
				},
			)

			gomega.Eventually(
				func(g gomega.Gomega) {
					live := &workloadsv1alpha2.RoleBasedGroupWarmup{}
					g.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(warmup), live)).To(gomega.Succeed())
					g.Expect(live.Status.Phase).ToNot(gomega.BeEmpty(), "the warmup was never reconciled")
				}, utils.Timeout, utils.Interval,
			).Should(gomega.Succeed())
		},
	)
}

// findFixture returns the fixture with the given name, failing rather than returning
// nil so a renamed fixture surfaces as a clear message instead of a nil dereference.
func findFixture(rbgs []*workloadsv1alpha2.RoleBasedGroup, name string) *workloadsv1alpha2.RoleBasedGroup {
	for _, rbg := range rbgs {
		if rbg.Name == name {
			return rbg
		}
	}
	ginkgo.Fail(fmt.Sprintf("fixture %q is not in the fixture list", name))
	return nil
}

// scaleRole sets one role's replicas on the cluster and mirrors the change onto the
// local fixture, so the ExpectRbg* helpers keep comparing against the intended spec.
func scaleRole(
	f *framework.Framework,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	roleName string,
	replicas int32,
) {
	gomega.Expect(
		retry.RetryOnConflict(
			retry.DefaultRetry, func() error {
				live := &workloadsv1alpha2.RoleBasedGroup{}
				if err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(rbg), live); err != nil {
					return err
				}
				for i := range live.Spec.Roles {
					if live.Spec.Roles[i].Name == roleName {
						live.Spec.Roles[i].Replicas = ptr.To(replicas)
					}
				}
				return f.Client.Update(f.Ctx, live)
			},
		),
	).To(gomega.Succeed())

	for i := range rbg.Spec.Roles {
		if rbg.Spec.Roles[i].Name == roleName {
			rbg.Spec.Roles[i].Replicas = ptr.To(replicas)
		}
	}
}

// only returns a copy of snaps holding just the named entries. A missing name fails
// rather than being skipped, which would leave a comparison against an empty map that
// compares equal to anything.
func only(snaps map[string]RBGSnapshot, names ...string) map[string]RBGSnapshot {
	out := make(map[string]RBGSnapshot, len(names))
	for _, name := range names {
		snap, found := snaps[name]
		if !found {
			ginkgo.Fail(fmt.Sprintf("RoleBasedGroup %q is not in the snapshot", name))
		}
		out[name] = snap
	}
	return out
}

// exclude returns a copy of snaps without the named entries.
func exclude(snaps map[string]RBGSnapshot, names ...string) map[string]RBGSnapshot {
	skip := make(map[string]bool, len(names))
	for _, name := range names {
		skip[name] = true
	}
	out := make(map[string]RBGSnapshot, len(snaps))
	for name, snap := range snaps {
		if !skip[name] {
			out[name] = snap
		}
	}
	return out
}

// waitAllRBGsReady waits until every RoleBasedGroup in the namespace reports Ready,
// except the named ones.
//
// It lists rather than taking a fixture list because the RBGs the set fixtures create are
// not in any list here, and they are exactly the ones the ExpectRbgSet*Equal helpers do
// not wait for: those only require the children to exist.
func waitAllRBGsReady(f *framework.Framework, except ...string) {
	skip := make(map[string]bool, len(except))
	for _, name := range except {
		skip[name] = true
	}

	gomega.Eventually(
		func(g gomega.Gomega) {
			list := &workloadsv1alpha2.RoleBasedGroupList{}
			g.Expect(f.Client.List(f.Ctx, list, client.InNamespace(f.Namespace))).To(gomega.Succeed())
			g.Expect(list.Items).ToNot(gomega.BeEmpty())

			for i := range list.Items {
				rbg := &list.Items[i]
				if skip[rbg.Name] {
					continue
				}
				// A terminating object never reaches Ready, so waiting on one would
				// burn the whole timeout. The prune probe of the first phase 1 spec is
				// deleted in a DeferCleanup and can still be here.
				if rbg.DeletionTimestamp != nil {
					continue
				}
				ready := false
				for _, cond := range rbg.Status.Conditions {
					if cond.Type == string(workloadsv1alpha2.RoleBasedGroupReady) {
						ready = cond.Status == metav1.ConditionTrue
					}
				}
				g.Expect(ready).To(gomega.BeTrue(), "RoleBasedGroup %s is not Ready", rbg.Name)
			}
		}, utils.Timeout, utils.Interval,
	).Should(gomega.Succeed())
}

// waitAllPodsRunning waits until every role of every RoleBasedGroup in the namespace has
// its full expected pod count and every one of those pods is Running, except for the
// named RBGs.
//
// The Ready gate above cannot establish this, because an RBG's Ready condition inherits
// whatever the workload underneath it calls ready. For a LeaderWorkerSet role that is
// LWS's group accounting, and LWS v0.7.0 counts a group ready when the leader pod is
// Running and Ready and the worker StatefulSet merely has its pods *created*: its
// StatefulsetReady compares Spec.Replicas against Status.Replicas, not against
// Status.ReadyReplicas. A Pending worker beside a Running leader therefore makes the
// whole RBG report Ready, which is how up-v1a1-lw-0-1 reached the baseline still Pending
// and started a minute later.
//
// That matters because `before` is what every later phase compares against: a Pending pod
// captured into it becomes a Pending -> Running difference in phase 3, attributed to an
// upgrade that had nothing to do with it. Teaching checkStillReady to tolerate that would
// instead give up seeing an upgrade that really did restart a pod.
//
// The pod count is the same guard one level lower, and no workload has been observed
// opening the Ready gate with pods missing outright -- the condition also requires
// `*role.Replicas == status.Replicas`. It is here because it does not depend on any
// workload's definition of ready, so it still holds whatever a later version decides that
// word means.
//
// Terminating objects are skipped for the same reason waitAllRBGsReady skips them: the
// prune probe of the first phase 1 spec can still be here, and it will never have pods
// again.
func waitAllPodsRunning(f *framework.Framework, except ...string) {
	skip := make(map[string]bool, len(except))
	for _, name := range except {
		skip[name] = true
	}

	gomega.Eventually(
		func(g gomega.Gomega) {
			list := &workloadsv1alpha2.RoleBasedGroupList{}
			g.Expect(f.Client.List(f.Ctx, list, client.InNamespace(f.Namespace))).To(gomega.Succeed())
			g.Expect(list.Items).ToNot(gomega.BeEmpty())

			var problems []string
			for i := range list.Items {
				rbg := &list.Items[i]
				if skip[rbg.Name] || rbg.DeletionTimestamp != nil {
					continue
				}
				for j := range rbg.Spec.Roles {
					role := &rbg.Spec.Roles[j]
					// The same helper captureRBG uses, so this waits on exactly the pods
					// the snapshot will record.
					pods := listPodFactsForRole(g, f, rbg, role.Name)
					if want, err := expectedPodsForRole(role); err != nil {
						problems = append(problems, fmt.Sprintf("%s/%s: %s", rbg.Name, role.Name, err))
					} else if len(pods) != want {
						problems = append(
							problems,
							fmt.Sprintf("%s/%s: has %d of %d pods", rbg.Name, role.Name, len(pods), want),
						)
					}
					for pod, facts := range pods {
						if facts.Phase != corev1.PodRunning {
							problems = append(
								problems,
								fmt.Sprintf("%s/%s: pod %s is %s", rbg.Name, role.Name, pod, facts.Phase),
							)
						}
					}
				}
			}
			sort.Strings(problems)
			g.Expect(problems).To(
				gomega.BeEmpty(),
				"the namespace has not converged, so the baseline would be a half-started world:\n  - %s",
				strings.Join(problems, "\n  - "),
			)
		}, utils.Timeout, utils.Interval,
	).Should(gomega.Succeed())
}

// expectedPodsForRole is how many pods a converged role has: one per replica, times the
// pods each replica is made of.
//
// An unrecognised pattern is an error rather than a guess of one pod per replica, because
// a guess would silently under-count a role a later version adds and quietly weaken the
// gate above -- the failure mode this whole suite exists to avoid.
func expectedPodsForRole(role *workloadsv1alpha2.RoleSpec) (int, error) {
	replicas := ptr.Deref(role.Replicas, 1)

	switch {
	case role.IsStandalonePattern():
		return int(replicas), nil
	case role.IsLeaderWorkerPattern():
		// Size counts the leader, so it is the number of pods per replica outright.
		return int(replicas * ptr.Deref(role.GetLeaderWorkerSize(), 1)), nil
	case role.GetCustomComponentsPattern() != nil:
		perReplica := int32(0)
		for _, component := range role.GetCustomComponentsPattern().Components {
			perReplica += ptr.Deref(component.Size, 1)
		}
		return int(replicas * perReplica), nil
	}
	return 0, fmt.Errorf("no pattern this gate can count pods for")
}

// waitForPendingPod waits until the given RBG's only role has a pod that exists and is
// Pending. Existence is the point: a fixture that never got as far as a pod would make
// every later comparison about it a comparison of two empty sets.
func waitForPendingPod(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	gomega.Expect(rbg.Spec.Roles).To(gomega.HaveLen(1))
	roleName := rbg.Spec.Roles[0].Name

	gomega.Eventually(
		func(g gomega.Gomega) {
			pods := listPodFactsForRole(g, f, rbg, roleName)
			g.Expect(pods).To(gomega.HaveLen(1))
			for name, facts := range pods {
				g.Expect(facts.Phase).To(
					gomega.Equal(corev1.PodPending),
					"pod %s is %s, so it was scheduled after all and no node carries %s",
					name, facts.Phase, unschedulableNodeLabel,
				)
			}
		}, utils.Timeout, utils.Interval,
	).Should(gomega.Succeed())
}

// childRBGNames returns the sorted names of the RoleBasedGroups a RoleBasedGroupSet owns.
func childRBGNames(f *framework.Framework, setName string) []string {
	list := &workloadsv1alpha2.RoleBasedGroupList{}
	gomega.Expect(
		f.Client.List(
			f.Ctx, list,
			client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.GroupSetNameLabelKey: setName},
		),
	).To(gomega.Succeed())

	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].Name)
	}
	sort.Strings(names)
	return names
}

// startMidRollout changes the mid-rollout fixture's pod template and pins its lower
// ordinals with a partition, which together start a rollout that halts part-way, and
// mirrors both onto the local fixture so ExpectRbgV2Equal keeps comparing against the
// intended spec.
//
// Both go in one update on purpose. Written separately, the controller could observe the
// new template while the partition was still zero and roll all of the instances before the
// partition arrived, which is the state this fixture exists to avoid.
func startMidRollout(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	env := corev1.EnvVar{Name: "UPGRADE_E2E_MIDROLL", Value: "1"}
	partition := ptr.To(intstr.FromInt32(midRollPartition))

	gomega.Expect(
		retry.RetryOnConflict(
			retry.DefaultRetry, func() error {
				live := &workloadsv1alpha2.RoleBasedGroup{}
				if err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(rbg), live); err != nil {
					return err
				}
				containers := midRollTemplate(live).Spec.Containers
				containers[0].Env = append(containers[0].Env, env)
				midRollRollingUpdate(live).Partition = partition
				return f.Client.Update(f.Ctx, live)
			},
		),
	).To(gomega.Succeed())

	containers := midRollTemplate(rbg).Spec.Containers
	containers[0].Env = append(containers[0].Env, env)
	midRollRollingUpdate(rbg).Partition = partition
}

// releaseMidRollout lowers the mid-rollout fixture's partition to zero, which is what lets
// the halted rollout converge, and mirrors the change onto the local fixture so
// ExpectRbgV2Equal keeps comparing against the intended spec.
func releaseMidRollout(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	released := ptr.To(intstr.FromInt32(0))

	gomega.Expect(
		retry.RetryOnConflict(
			retry.DefaultRetry, func() error {
				live := &workloadsv1alpha2.RoleBasedGroup{}
				if err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(rbg), live); err != nil {
					return err
				}
				midRollRollingUpdate(live).Partition = released
				return f.Client.Update(f.Ctx, live)
			},
		),
	).To(gomega.Succeed())

	midRollRollingUpdate(rbg).Partition = released
}

// midRollTemplate returns the pod template of the mid-rollout fixture's only role.
func midRollTemplate(rbg *workloadsv1alpha2.RoleBasedGroup) *corev1.PodTemplateSpec {
	gomega.Expect(rbg.Spec.Roles).To(gomega.HaveLen(1))
	pattern := rbg.Spec.Roles[0].StandalonePattern
	gomega.Expect(pattern).ToNot(gomega.BeNil())
	gomega.Expect(pattern.Template).ToNot(gomega.BeNil())
	gomega.Expect(pattern.Template.Spec.Containers).ToNot(gomega.BeEmpty())
	return pattern.Template
}

// midRollRollingUpdate returns the rolling update strategy of the mid-rollout fixture's
// only role, which is where its partition lives.
func midRollRollingUpdate(rbg *workloadsv1alpha2.RoleBasedGroup) *workloadsv1alpha2.RollingUpdate {
	gomega.Expect(rbg.Spec.Roles).To(gomega.HaveLen(1))
	strategy := rbg.Spec.Roles[0].RolloutStrategy
	gomega.Expect(strategy).ToNot(gomega.BeNil())
	gomega.Expect(strategy.RollingUpdate).ToNot(gomega.BeNil())
	return strategy.RollingUpdate
}

// countSurvivors returns how many of the pods in before are still the same pod in after.
//
// Identity is the name plus the UID, not the name alone: a RoleInstanceSet gives its pods
// stable names, so a replaced pod comes back under the name it had and counting names
// would report a finished rollout as one that never started.
func countSurvivors(before, after map[string]PodFacts) int {
	survivors := 0
	for name, old := range before {
		if current, found := after[name]; found && current.UID == old.UID {
			survivors++
		}
	}
	return survivors
}

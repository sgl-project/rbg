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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// rolloutMarker is added to the container env of the group template to drive a recreate
// rolling update.
const (
	rolloutMarkerName  = "RBGSET_ROLLOUT_MARKER"
	rolloutMarkerValue = "rolled"
)

// hasRolloutMarker reports whether the role carries the marker env var, i.e. whether it has
// been moved onto the updated group template.
func hasRolloutMarker(role workloadsv1alpha2.RoleSpec) bool {
	if role.StandalonePattern == nil || role.StandalonePattern.Template == nil {
		return false
	}
	for _, container := range role.StandalonePattern.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == rolloutMarkerName && env.Value == rolloutMarkerValue {
				return true
			}
		}
	}
	return false
}

// triggerRbgSetRollout adds the marker env var to the group template, which is the change every
// rolling update case below rolls out.
func triggerRbgSetRollout(f *framework.Framework, rbgset *workloadsv1alpha2.RoleBasedGroupSet) {
	updateRbgSetV2(
		f, rbgset, func(rs *workloadsv1alpha2.RoleBasedGroupSet) {
			container := &rs.Spec.GroupTemplate.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0]
			container.Env = append(
				container.Env,
				corev1.EnvVar{Name: rolloutMarkerName, Value: rolloutMarkerValue},
			)
		},
	)
}

// baseRolloutComplete reports whether the rollout has reached its end state: every base ordinal
// exists, carries the new template and is serving, and the surge groups have been reclaimed. It
// returns the base UIDs so a caller can show that they replaced the original ones.
func baseRolloutComplete(
	children []workloadsv1alpha2.RoleBasedGroup, replicas int,
) (map[string]types.UID, bool) {
	base, surge := framework.SplitRbgSetV2Children(children, replicas)
	if len(base) != replicas || len(surge) != 0 {
		return nil, false
	}
	if framework.CountRbgSetV2ReadyChildren(base) != replicas {
		return nil, false
	}
	for i := range base {
		if !hasRolloutMarker(base[i].Spec.Roles[0]) {
			return nil, false
		}
	}
	return framework.RbgSetV2ChildUIDs(base), true
}

// expectGroupsRecreated asserts that the named groups kept their deterministic names but changed
// UID, which is what a recreate rollout leaves behind.
func expectGroupsRecreated(before, after map[string]types.UID, names ...string) {
	for _, name := range names {
		gomega.Expect(after).To(gomega.HaveKey(name))
		gomega.Expect(after[name]).NotTo(
			gomega.Equal(before[name]), "group %s should have been deleted and recreated", name,
		)
	}
}

// expectGroupsUntouched asserts that the named groups were neither recreated nor updated in
// place, which is how a held-back ordinal proves the controller left it alone.
func expectGroupsUntouched(before, after map[string]types.UID, names ...string) {
	for _, name := range names {
		gomega.Expect(after).To(gomega.HaveKey(name))
		gomega.Expect(after[name]).To(
			gomega.Equal(before[name]), "group %s should not have been touched", name,
		)
	}
}

// countRbgV2ReadyPods counts the ready, non-terminating Pods of the named child RoleBasedGroup.
// The DeletionTimestamp filter is what makes an in-place scale distinguishable from a
// delete-recreate: a recreated group's old Pods linger as terminating for a while.
func countRbgV2ReadyPods(f *framework.Framework, namespace, rbgName string) int {
	podList := &corev1.PodList{}
	gomega.Expect(f.Client.List(
		f.Ctx, podList, client.InNamespace(namespace),
		client.MatchingLabels{constants.GroupNameLabelKey: rbgName},
	)).Should(gomega.Succeed())

	ready := 0
	for i := range podList.Items {
		pod := &podList.Items[i]
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				ready++
				break
			}
		}
	}
	return ready
}

func RunRbgSetControllerTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"rbgset controller", func() {
			ginkgo.It(
				"create & delete rbgset", func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgSetV2Equal(rbgset)

					// delete rbgset
					gomega.Expect(f.Client.Delete(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgSetV2Deleted(rbgset)
				},
			)

			ginkgo.It(
				"scaling rbgset", func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).WithReplicas(1).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgSetV2Equal(rbgset)

					// replicas 1 to 2
					updateRbgSetV2(
						f, rbgset, func(rs *workloadsv1alpha2.RoleBasedGroupSet) {
							rs.Spec.Replicas = ptr.To(int32(2))
						},
					)
					f.ExpectRbgSetV2Equal(rbgset)

					// replicas 2 to 1
					updateRbgSetV2(
						f, rbgset, func(rs *workloadsv1alpha2.RoleBasedGroupSet) {
							rs.Spec.Replicas = ptr.To(int32(1))
						},
					)
					f.ExpectRbgSetV2Equal(rbgset)
				},
			)

			ginkgo.It(
				"role replicas change scales groups in place without recreating them", func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).
						WithReplicas(2).
						WithRolloutStrategy(wrappersv2.BuildRecreateRolloutStrategy(1, 0, 0)).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					initialUIDs := f.ExpectRbgSetV2AllReady(rbgset)
					gomega.Expect(initialUIDs).Should(gomega.HaveLen(2))
					f.ExpectRbgSetV2ChildrenStable(rbgset, initialUIDs)

					// A replicas-only diff inside groupTemplate never falls back to recreate:
					// the rbg groups keep their identity and the extra pods are added in place.
					updateRbgSetV2(
						f, rbgset, func(rs *workloadsv1alpha2.RoleBasedGroupSet) {
							rs.Spec.GroupTemplate.Spec.Roles[0].Replicas = ptr.To(int32(2))
						},
					)

					var scaledUIDs map[string]types.UID
					gomega.Eventually(
						func() bool {
							children := f.ListRbgSetV2Children(rbgset)
							if len(children) != 2 || framework.CountRbgSetV2ReadyChildren(children) != 2 {
								return false
							}
							uids := framework.RbgSetV2ChildUIDs(children)
							for name, uid := range initialUIDs {
								gomega.Expect(uids[name]).To(
									gomega.Equal(uid),
									"a replicas-only change must not delete the group %s", name,
								)
							}
							for _, child := range children {
								if countRbgV2ReadyPods(f, child.Namespace, child.Name) != 2 {
									return false
								}
							}
							scaledUIDs = uids
							return true
						}, utils.Timeout, utils.Interval,
					).Should(gomega.BeTrue())

					// The scaled-up shape holds: no group is reclaimed afterwards.
					f.ExpectRbgSetV2ChildrenStable(rbgset, scaledUIDs)
				},
			)

			ginkgo.It(
				"exclusive-topology annotation propagates from groupTemplate and is removed with it", func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).
						WithReplicas(1).Obj()
					rbgset.Spec.GroupTemplate.Annotations = map[string]string{
						constants.GroupExclusiveTopologyKey: "topology.kubernetes.io/zone",
					}

					f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())

					// The user-facing rbg.* annotation propagates from groupTemplate to the child.
					gomega.Eventually(
						func() bool {
							children := f.ListRbgSetV2Children(rbgset)
							return len(children) == 1 &&
								children[0].Annotations[constants.GroupExclusiveTopologyKey] == "topology.kubernetes.io/zone"
						}, utils.Timeout, utils.Interval,
					).Should(gomega.BeTrue())

					// Removing it from the template removes it from the child too: the key is
					// user-facing, not controller-owned, so the sync must not keep it behind.
					updateRbgSetV2(
						f, rbgset, func(rs *workloadsv1alpha2.RoleBasedGroupSet) {
							delete(rs.Spec.GroupTemplate.Annotations, constants.GroupExclusiveTopologyKey)
						},
					)
					gomega.Eventually(
						func() bool {
							children := f.ListRbgSetV2Children(rbgset)
							if len(children) != 1 {
								return false
							}
							_, exists := children[0].Annotations[constants.GroupExclusiveTopologyKey]
							return !exists
						}, utils.Timeout, utils.Interval,
					).Should(gomega.BeTrue())
				},
			)

			ginkgo.It(
				"recreate rolling update replaces one group at a time", func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).
						WithReplicas(2).
						WithRolloutStrategy(wrappersv2.BuildRecreateRolloutStrategy(1, 0, 0)).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())

					// Creation settles: both groups come up and then hold still, so the
					// steady state is recorded before anything is changed.
					initialUIDs := f.ExpectRbgSetV2AllReady(rbgset)
					gomega.Expect(initialUIDs).Should(gomega.HaveLen(2))
					f.ExpectRbgSetV2ChildrenStable(rbgset, initialUIDs)

					// The marker env var is not a replicas-only diff, so it drives a recreate.
					triggerRbgSetRollout(f, rbgset)

					// The pacing invariant is asserted on every poll, so it covers the whole
					// rollout rather than a fixed window. maxSurge=0 means never more groups than
					// replicas, and maxUnavailable=1 out of 2 means never fewer than one ready
					// group. Together those are exactly what "delete one, then create one" looks
					// like from the outside, and a violation fails the spec immediately.
					var rolledUIDs map[string]types.UID
					gomega.Eventually(
						func() bool {
							children := f.ListRbgSetV2Children(rbgset)
							gomega.Expect(len(children)).To(
								gomega.BeNumerically("<=", 2),
								"maxSurge is 0, so no group beyond spec.replicas may appear",
							)
							ready := framework.CountRbgSetV2ReadyChildren(children)
							gomega.Expect(ready).To(
								gomega.BeNumerically(">=", 1),
								"maxUnavailable is 1 of 2, so one group must keep serving",
							)

							if len(children) != 2 || ready != 2 {
								return false
							}
							uids := framework.RbgSetV2ChildUIDs(children)
							for name, uid := range initialUIDs {
								if uids[name] == uid {
									return false
								}
							}
							rolledUIDs = uids
							return true
						}, utils.Timeout, utils.Interval,
					).Should(gomega.BeTrue())

					// Both groups were recreated: the deterministic names are kept, the UIDs are not.
					expectGroupsRecreated(initialUIDs, rolledUIDs, "test-0", "test-1")

					// The set settles again on the new template and stays there.
					f.ExpectRbgSetV2ChildrenStable(rbgset, rolledUIDs)
				},
			)

			ginkgo.It(
				"surge backs a zero maxUnavailable so ready groups never drop below replicas", func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).
						WithReplicas(2).
						WithRolloutStrategy(wrappersv2.BuildRecreateRolloutStrategy(0, 1, 0)).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					initialUIDs := f.ExpectRbgSetV2AllReady(rbgset)
					f.ExpectRbgSetV2ChildrenStable(rbgset, initialUIDs)

					triggerRbgSetRollout(f, rbgset)

					// maxUnavailable is 0, so the entire delete budget comes from the surge group:
					// a base group may only be recreated once the surge group is serving. Two groups
					// therefore have to be ready at every point of the rollout. Both invariants are
					// asserted on every poll, which covers the whole window rather than a sample.
					var rolledUIDs map[string]types.UID
					gomega.Eventually(
						func() bool {
							children := f.ListRbgSetV2Children(rbgset)
							gomega.Expect(framework.CountRbgSetV2ReadyChildren(children)).To(
								gomega.BeNumerically(">=", 2),
								"maxUnavailable is 0, so both groups must keep serving throughout",
							)
							gomega.Expect(len(children)).To(
								gomega.BeNumerically("<=", 3),
								"maxSurge is 1, so at most one group above spec.replicas may exist",
							)

							uids, complete := baseRolloutComplete(children, 2)
							if !complete {
								return false
							}
							rolledUIDs = uids
							return true
						}, utils.Timeout, utils.Interval,
					).Should(gomega.BeTrue())

					expectGroupsRecreated(initialUIDs, rolledUIDs, "test-0", "test-1")
					f.ExpectRbgSetV2ChildrenStable(rbgset, rolledUIDs)
				},
			)

			ginkgo.It(
				"surge group survives until the rollout completes", func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).
						WithReplicas(2).
						WithRolloutStrategy(wrappersv2.BuildRecreateRolloutStrategy(1, 1, 0)).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					initialUIDs := f.ExpectRbgSetV2AllReady(rbgset)
					f.ExpectRbgSetV2ChildrenStable(rbgset, initialUIDs)

					triggerRbgSetRollout(f, rbgset)

					// one group should keep serving on its own and one surge group is extra capacity on top.
					var rolledUIDs map[string]types.UID
					sawSurge := false
					gomega.Eventually(
						func() bool {
							children := f.ListRbgSetV2Children(rbgset)
							gomega.Expect(framework.CountRbgSetV2ReadyChildren(children)).To(
								gomega.BeNumerically(">=", 1),
								"maxUnavailable is 1 of 2, so one group must keep serving",
							)
							gomega.Expect(len(children)).To(
								gomega.BeNumerically("<=", 3),
								"maxSurge is 1, so at most one group above spec.replicas may exist",
							)

							_, surge := framework.SplitRbgSetV2Children(children, 2)
							if len(surge) > 0 {
								sawSurge = true
							}

							uids, complete := baseRolloutComplete(children, 2)
							if !complete {
								// Only enforced once the surge group has been sighted, so the poll
								// that lands before the first reconcile does not fail the case.
								if sawSurge {
									gomega.Expect(surge).To(
										gomega.HaveLen(1),
										"the surge group must not be reclaimed before the rollout completes",
									)
								}
								return false
							}
							rolledUIDs = uids
							return true
						}, utils.Timeout, utils.Interval,
					).Should(gomega.BeTrue())

					gomega.Expect(sawSurge).To(
						gomega.BeTrue(), "no surge group ever appeared, so the case proved nothing",
					)
					expectGroupsRecreated(initialUIDs, rolledUIDs, "test-0", "test-1")
					f.ExpectRbgSetV2ChildrenStable(rbgset, rolledUIDs)
				},
			)

			runRbgSetPartitionRolloutCases(f)

			ginkgo.It(
				"paused holds the rollout still until it is lifted", func() {
					strategy := wrappersv2.BuildRecreateRolloutStrategy(1, 0, 0)
					strategy.Paused = true
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).
						WithReplicas(1).
						WithRolloutStrategy(strategy).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					// Scaling is never frozen by paused, so the single group still comes up.
					initialUIDs := f.ExpectRbgSetV2AllReady(rbgset)

					triggerRbgSetRollout(f, rbgset)

					// Paused: the template changed but nothing may move. Eight seconds spans many
					// reconciles, each of which has to decide to do nothing. Returning nil on any
					// sighting of the marker, or of a changed UID, fails the window immediately.
					gomega.Consistently(
						func() map[string]types.UID {
							children := f.ListRbgSetV2Children(rbgset)
							if len(children) != 1 || hasRolloutMarker(children[0].Spec.Roles[0]) {
								return nil
							}
							return framework.RbgSetV2ChildUIDs(children)
						}, 8, 1,
					).Should(gomega.Equal(initialUIDs))

					updateRbgSetV2(
						f, rbgset, func(rs *workloadsv1alpha2.RoleBasedGroupSet) {
							rs.Spec.RolloutStrategy.Paused = false
						},
					)

					var rolledUIDs map[string]types.UID
					gomega.Eventually(
						func() bool {
							uids, complete := baseRolloutComplete(f.ListRbgSetV2Children(rbgset), 1)
							if !complete {
								return false
							}
							rolledUIDs = uids
							return true
						}, utils.Timeout, utils.Interval,
					).Should(gomega.BeTrue())

					expectGroupsRecreated(initialUIDs, rolledUIDs, "test-0")
					f.ExpectRbgSetV2ChildrenStable(rbgset, rolledUIDs)
				},
			)
		},
	)
}

// runRbgSetPartitionRolloutCases registers the partition-scoped rollout specs. gocyclo
// attributes nested closures to the enclosing function, so the partition specs live in
// their own registrar to keep RunRbgSetControllerTestCases under the complexity limit.
func runRbgSetPartitionRolloutCases(f *framework.Framework) {
	ginkgo.It(
		"partition rolls the upper ordinals first and releases the rest when lowered", func() {
			rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).
				WithReplicas(4).
				WithRolloutStrategy(wrappersv2.BuildRecreateRolloutStrategy(2, 0, 2)).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

			gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
			initialUIDs := f.ExpectRbgSetV2AllReady(rbgset)
			gomega.Expect(initialUIDs).Should(gomega.HaveLen(4))
			f.ExpectRbgSetV2ChildrenStable(rbgset, initialUIDs)

			triggerRbgSetRollout(f, rbgset)

			// partition is 2, so only ordinals 2 and 3 take part, and maxUnavailable is 2,
			// so both of them may be rebuilt in parallel rather than one at a time.
			var phase1UIDs map[string]types.UID
			gomega.Eventually(
				func() bool {
					children := f.ListRbgSetV2Children(rbgset)
					if len(children) != 4 || framework.CountRbgSetV2ReadyChildren(children) != 4 {
						return false
					}
					for _, ordinal := range []int{2, 3} {
						rbg := framework.RbgSetV2ChildByOrdinal(children, ordinal)
						if rbg == nil || !hasRolloutMarker(rbg.Spec.Roles[0]) {
							return false
						}
					}
					for _, ordinal := range []int{0, 1} {
						rbg := framework.RbgSetV2ChildByOrdinal(children, ordinal)
						if rbg == nil || hasRolloutMarker(rbg.Spec.Roles[0]) {
							return false
						}
					}
					phase1UIDs = framework.RbgSetV2ChildUIDs(children)
					return true
				}, utils.Timeout, utils.Interval,
			).Should(gomega.BeTrue())

			expectGroupsRecreated(initialUIDs, phase1UIDs, "test-2", "test-3")
			expectGroupsUntouched(initialUIDs, phase1UIDs, "test-0", "test-1")

			// The held-back ordinals stay held: with nothing left above partition the
			// controller has no work, so it must not churn them.
			f.ExpectRbgSetV2ChildrenStable(rbgset, phase1UIDs)

			updateRbgSetV2(
				f, rbgset, func(rs *workloadsv1alpha2.RoleBasedGroupSet) {
					rs.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt32(0))
				},
			)

			var finalUIDs map[string]types.UID
			gomega.Eventually(
				func() bool {
					uids, complete := baseRolloutComplete(f.ListRbgSetV2Children(rbgset), 4)
					if !complete {
						return false
					}
					finalUIDs = uids
					return true
				}, utils.Timeout, utils.Interval,
			).Should(gomega.BeTrue())

			expectGroupsRecreated(phase1UIDs, finalUIDs, "test-0", "test-1")
			expectGroupsUntouched(phase1UIDs, finalUIDs, "test-2", "test-3")
			f.ExpectRbgSetV2ChildrenStable(rbgset, finalUIDs)
		},
	)

	ginkgo.It(
		"surge-backed partition rollout releases surge when the batch completes", func() {
			rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).
				WithReplicas(4).
				WithRolloutStrategy(wrappersv2.BuildRecreateRolloutStrategy(0, 2, 2)).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

			gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
			initialUIDs := f.ExpectRbgSetV2AllReady(rbgset)
			gomega.Expect(initialUIDs).Should(gomega.HaveLen(4))
			f.ExpectRbgSetV2ChildrenStable(rbgset, initialUIDs)

			triggerRbgSetRollout(f, rbgset)

			// maxUnavailable is 0, so every delete must be backed by ready surge capacity:
			// ready groups (surge included, surge serves exactly like base) may never drop
			// below replicas, and no more than replicas+maxSurge groups may exist. The
			// held-back ordinals must keep their identity throughout. All three are
			// invariants, asserted on every poll so a violation fails at first sighting.
			sawSurge := false
			var rolledUIDs map[string]types.UID
			gomega.Eventually(
				func() bool {
					children := f.ListRbgSetV2Children(rbgset)
					gomega.Expect(len(children)).To(
						gomega.BeNumerically("<=", 6),
						"maxSurge is 2, so at most 4+2 groups may exist",
					)
					gomega.Expect(framework.CountRbgSetV2ReadyChildren(children)).To(
						gomega.BeNumerically(">=", 4),
						"maxUnavailable is 0, so ready groups (surge included) must stay at replicas",
					)
					uids := framework.RbgSetV2ChildUIDs(children)
					for _, name := range []string{"test-0", "test-1"} {
						gomega.Expect(uids[name]).To(
							gomega.Equal(initialUIDs[name]),
							"partition holds %s back, so it must never be recreated", name,
						)
					}

					base, surge := framework.SplitRbgSetV2Children(children, 4)
					if len(surge) > 0 {
						sawSurge = true
					}
					if len(base) != 4 || len(surge) != 0 || framework.CountRbgSetV2ReadyChildren(base) != 4 {
						return false
					}
					for _, ordinal := range []int{2, 3} {
						rbg := framework.RbgSetV2ChildByOrdinal(children, ordinal)
						if rbg == nil || !hasRolloutMarker(rbg.Spec.Roles[0]) {
							return false
						}
					}
					rolledUIDs = uids
					return true
				}, utils.Timeout, utils.Interval,
			).Should(gomega.BeTrue())

			gomega.Expect(sawSurge).To(
				gomega.BeTrue(), "maxUnavailable is 0, so the rollout could only advance on surge capacity",
			)
			expectGroupsRecreated(initialUIDs, rolledUIDs, "test-2", "test-3")
			expectGroupsUntouched(initialUIDs, rolledUIDs, "test-0", "test-1")

			// The batch is done, so the surge groups are gone for good: this window fails
			// if a re-warmed surge group or a churned UID ever shows up.
			f.ExpectRbgSetV2ChildrenStable(rbgset, rolledUIDs)
		},
	)

	ginkgo.It(
		"maxUnavailable covering the batch rolls without ever creating surge", func() {
			rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).
				WithReplicas(4).
				WithRolloutStrategy(wrappersv2.BuildRecreateRolloutStrategy(2, 2, 2)).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

			gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
			initialUIDs := f.ExpectRbgSetV2AllReady(rbgset)
			gomega.Expect(initialUIDs).Should(gomega.HaveLen(4))
			f.ExpectRbgSetV2ChildrenStable(rbgset, initialUIDs)

			triggerRbgSetRollout(f, rbgset)

			// maxUnavailable alone covers the two in-scope ordinals, so the delete budget
			// never needs surge capacity: a surge group appearing at any point is the
			// create-then-reclaim waste this case exists to forbid. Ready groups may dip
			// by at most maxUnavailable.
			var rolledUIDs map[string]types.UID
			gomega.Eventually(
				func() bool {
					children := f.ListRbgSetV2Children(rbgset)
					gomega.Expect(len(children)).To(
						gomega.BeNumerically("<=", 4),
						"maxUnavailable covers the in-scope work, so no surge group may ever be created",
					)
					gomega.Expect(framework.CountRbgSetV2ReadyChildren(children)).To(
						gomega.BeNumerically(">=", 2),
						"maxUnavailable is 2 of 4, so at least two groups must keep serving",
					)
					uids := framework.RbgSetV2ChildUIDs(children)
					for _, name := range []string{"test-0", "test-1"} {
						gomega.Expect(uids[name]).To(
							gomega.Equal(initialUIDs[name]),
							"partition holds %s back, so it must never be recreated", name,
						)
					}

					if len(children) != 4 || framework.CountRbgSetV2ReadyChildren(children) != 4 {
						return false
					}
					for _, ordinal := range []int{2, 3} {
						rbg := framework.RbgSetV2ChildByOrdinal(children, ordinal)
						if rbg == nil || !hasRolloutMarker(rbg.Spec.Roles[0]) {
							return false
						}
					}
					rolledUIDs = uids
					return true
				}, utils.Timeout, utils.Interval,
			).Should(gomega.BeTrue())

			expectGroupsRecreated(initialUIDs, rolledUIDs, "test-2", "test-3")
			expectGroupsUntouched(initialUIDs, rolledUIDs, "test-0", "test-1")
			f.ExpectRbgSetV2ChildrenStable(rbgset, rolledUIDs)
		},
	)
}

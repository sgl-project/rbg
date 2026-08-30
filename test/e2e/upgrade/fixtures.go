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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv1 "sigs.k8s.io/rbgs/test/wrappers/v1alpha1"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// Fixture names. Kept short because they prefix generated pod names, which have a
// 63-character limit once role, instance and component suffixes are appended.
const (
	fxStandalone = "up-standalone"
	fxDeploy     = "up-deploy"
	fxSts        = "up-sts"
	fxLws        = "up-lws"
	fxLwp        = "up-lwp"
	fxDeps       = "up-deps"
	fxScaling    = "up-sa"
	fxCustom     = "up-cc"
	fxSet        = "up-set"
	fxV1alpha1   = "up-v1a1"
	// fxPending never becomes ready, and fxMidRoll is half-rolled when the upgrade
	// lands. Both exist because every other fixture is converged and quiet by the
	// time the upgrade starts, which is the one cluster state an upgrade is least
	// likely to arrive in.
	fxPending = "up-pend"
	fxMidRoll = "up-mid"
	// fxV1alpha1Set is a RoleBasedGroupSet written through v1alpha1. Its conversion
	// shares convertSpecV1alpha1ToV2 with RoleBasedGroup, so the shape the webhook
	// writes is the same code and only the RoleBasedGroup half was covered.
	fxV1alpha1Set = "up-v1a1s"

	sharedTemplateName = "shared-tpl"

	// unschedulableNodeLabel is a node label no node carries, which is how fxPending
	// is kept Pending. A resource request large enough to not fit would do the same,
	// but only until someone runs this on a bigger cluster.
	unschedulableNodeLabel = "upgrade-e2e.rbgs.x-k8s.io/no-such-node"

	midRollReplicas = 3
	midRollRole     = "roller"
	// midRollPartition is how many of fxMidRoll's instances phase 2 pins at the old
	// revision when it changes the template. Rolling the remaining
	// midRollReplicas-midRollPartition and then halting is what holds the fixture
	// half-rolled across the upgrade, however long helm takes.
	midRollPartition = 2

	// roleA and roleB are the two roles of the fxDeps fixture. Phase 4 mutates roleB
	// and requires that only roleB's pods move.
	roleA = "lead"
	roleB = "follow"

	// lwpRole is the role of the fxLwp fixture. Named because upgradeRewrites has to
	// name the shared Service in front of it.
	lwpRole = "lwp"

	// v1alpha1LwsRole is the role of the v1alpha1 fixture that converts to a
	// LeaderWorkerPattern, which is the branch the conversion changes touch.
	v1alpha1LwsRole = "lw"
)

// The fixtures below are created against the v0.7.0 CRDs using the current Go types.
// The apiserver prunes any field the v0.7.0 schema does not know, silently. So no
// fixture may use a field introduced after v0.7.0, or it would be dropped and the
// spec under test would not be the spec that was written.
//
// Two concrete bans:
//   - RestartPolicyConfig. Use WithLegacyRestartPolicy, which writes the deprecated
//     restartPolicy string, the shape v0.7.0 actually stored. Setting the config
//     struct would be pruned, and then phase 3 could not distinguish "defaulting
//     added the field" from "we wrote it ourselves".
//   - RoleBasedGroupWarmup. Its CRD does not exist in v0.7.0 at all. Phase 4 creates
//     one after the upgrade instead.
//
// Gang scheduling is excluded on purpose. Its pods stay Pending until the whole gang
// can be placed, which on a single-node Kind cluster is the top source of flakes, and
// a Pending pod turns "nothing churned" into a claim about nothing. The main e2e
// suite covers gang scheduling on a freshly installed controller.
//
// Sizes are kept small (at most 3 replicas, roughly 25 pods in total, all nginx) so a
// single-node Kind cluster is not the thing under test.

// buildFixtures returns the RoleBasedGroups to create before the upgrade.
//
// The never-ready fixture is not in here: phase 1 waits for readiness on everything
// this returns, and that fixture exists precisely because it never gets there.
func buildFixtures(ns string) []*workloadsv1alpha2.RoleBasedGroup {
	return []*workloadsv1alpha2.RoleBasedGroup{
		buildStandaloneFixture(ns),
		buildDeploymentFixture(ns),
		buildStatefulSetFixture(ns),
		buildLWSFixture(ns),
		buildLeaderWorkerPatternFixture(ns),
		buildDependenciesFixture(ns),
		buildScalingAdapterFixture(ns),
		buildCustomComponentsFixture(ns),
		buildMidRolloutFixture(ns),
	}
}

// buildStandaloneFixture is the baseline: default RoleInstanceSet workload, an
// explicit rolling update strategy and role annotations, two replicas.
func buildStandaloneFixture(ns string) *workloadsv1alpha2.RoleBasedGroup {
	return wrappersv2.BuildBasicRoleBasedGroup(fxStandalone, ns).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("worker").
				WithReplicas(2).
				WithRollingUpdate(wrappersv2.BuildRollingUpdate(1, 1)).
				WithAnnotations(map[string]string{"upgrade-e2e/kept": "annotation"}).
				Obj(),
		}).Obj()
}

// buildDeploymentFixture covers the deprecated Deployment workload type, which the
// current chart still enables by default.
func buildDeploymentFixture(ns string) *workloadsv1alpha2.RoleBasedGroup {
	return wrappersv2.BuildBasicRoleBasedGroup(fxDeploy, ns).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("web").
				WithWorkload("apps/v1", "Deployment").
				WithReplicas(1).
				Obj(),
		}).Obj()
}

// buildStatefulSetFixture exists for its stable pod names. A StatefulSet recreating
// a pod reuses the name, so this is the fixture that catches a replacement that
// checkNoPodChurn could only see through the UID.
func buildStatefulSetFixture(ns string) *workloadsv1alpha2.RoleBasedGroup {
	return wrappersv2.BuildBasicRoleBasedGroup(fxSts, ns).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("db").
				WithWorkload("apps/v1", "StatefulSet").
				WithReplicas(1).
				Obj(),
		}).Obj()
}

// buildLWSFixture covers the deprecated LeaderWorkerSet workload type, which also
// pulls in the external LWS CRD and controller.
func buildLWSFixture(ns string) *workloadsv1alpha2.RoleBasedGroup {
	return wrappersv2.BuildBasicRoleBasedGroup(fxLws, ns).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildLeaderWorkerRole("lws").
				WithWorkload("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet").
				WithReplicas(1).
				WithSize(2).
				Obj(),
		}).Obj()
}

// buildLeaderWorkerPatternFixture pins the deprecated restartPolicy string, which is
// the shape v0.7.0 stored. The new CRD adds restartPolicyConfig alongside it, so this
// is the fixture that would surface a rollout if reading the old shape through the new
// schema perturbed the spec the controller hashes. Phase 3 asserts restartPolicyConfig
// stays nil -- it carries no parent-level default, so the apiserver has nothing to
// materialize -- while the churn assertions confirm no pod moved.
func buildLeaderWorkerPatternFixture(ns string) *workloadsv1alpha2.RoleBasedGroup {
	return wrappersv2.BuildBasicRoleBasedGroup(fxLwp, ns).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildLeaderWorkerRole(lwpRole).
				WithReplicas(1).
				WithSize(2).
				WithLegacyRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
				Obj(),
		}).Obj()
}

// sharedServiceName is the name of the headless Service in front of a role. It goes
// through the production helper so this suite cannot disagree with the controller about
// what a Service is called.
func sharedServiceName(rbgName, roleName string) string {
	rbg := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: rbgName}}
	return rbg.GetServiceName(&workloadsv1alpha2.RoleSpec{Name: roleName})
}

// buildDependenciesFixture covers role ordering plus roleTemplates and templateRef,
// so the upgrade is also exercised against a role whose pod template is resolved
// indirectly. Phase 4 reuses it to prove the churn radius of a real spec change.
func buildDependenciesFixture(ns string) *workloadsv1alpha2.RoleBasedGroup {
	return wrappersv2.BuildBasicRoleBasedGroup(fxDeps, ns).
		WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{
			{
				Name:     sharedTemplateName,
				Template: wrappersv2.BuildBasicPodTemplateSpec(),
			},
		}).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole(roleA).
				WithReplicas(1).
				Obj(),
			wrappersv2.BuildStandaloneRole(roleB).
				WithReplicas(1).
				WithDependencies([]string{roleA}).
				// A templateRef without a patch is rejected by the controller's
				// preCheck, so the empty object is required rather than cosmetic.
				WithPatchRef(sharedTemplateName, &runtime.RawExtension{Raw: []byte("{}")}).
				Obj(),
		}).Obj()
}

// buildScalingAdapterFixture covers the scaling adapter object and engine runtime
// injection, both of which add reconcilers that could rewrite the role on upgrade.
func buildScalingAdapterFixture(ns string) *workloadsv1alpha2.RoleBasedGroup {
	return wrappersv2.BuildBasicRoleBasedGroup(fxScaling, ns).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("api").
				WithReplicas(1).
				WithScalingAdapter(true).
				WithEngineRuntime([]workloadsv1alpha2.EngineRuntime{
					{ProfileName: utils.DefaultEngineRuntimeProfileName},
				}).
				Obj(),
		}).Obj()
}

// buildCustomComponentsFixture is the pattern with the largest defaulting surface in
// the new CRDs. It is hand-built rather than wrapped because there is no wrapper for
// CustomComponentsPattern.
//
// Two replicas, not one: each replica is a RoleInstance carrying its own copy of both
// component pod templates, and state accidentally shared between ordinals only shows up
// once a second ordinal exists to leak into. At one replica the highest ordinal is also
// the only ordinal, so such a leak is invisible.
//
// It deliberately carries no port-allocator annotations: their payload schema is not
// something this suite has established is identical across the two versions, and
// port allocation is not the claim being tested here.
func buildCustomComponentsFixture(ns string) *workloadsv1alpha2.RoleBasedGroup {
	return wrappersv2.BuildBasicRoleBasedGroup(fxCustom, ns).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			{
				Name:     "prefill",
				Replicas: ptr.To(int32(2)),
				Pattern: workloadsv1alpha2.Pattern{
					CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
						// Only the deprecated string field, for the same reason as
						// buildLeaderWorkerPatternFixture.
						RestartPolicy: workloadsv1alpha2.RestartPolicyNone, //nolint:staticcheck // v0.7.0 wire shape on purpose
						Components: []workloadsv1alpha2.InstanceComponent{
							buildComponent("leader"),
							buildComponent("worker"),
						},
					},
				},
			},
		}).Obj()
}

// buildMidRolloutFixture is the fixture that is half-rolled when the upgrade lands.
// Phase 2 changes its template and introduces a partition in the same update.
//
// The partition is what makes that state hold. It pins the ordinals below it at the old
// revision, so the ordinals above it roll and the rollout then stops, leaving the fixture
// at mixed revisions until phase 3 lifts it. Stretching a readiness probe until the
// rollout outlasted helm was the earlier approach, and it was a race the suite could not
// win: how long a helm upgrade takes is not something a test controls, so the overlap
// could only be reported afterwards, never relied on.
//
// It is not set here, only in phase 2. newVersionedInstance builds every ordinal below the
// partition from the *current* revision, which during initial creation is not yet the
// revision this fixture is being created at, so a partition present from the start would
// put phase 1's readiness wait at the mercy of that path.
//
// The halt is also what lets this fixture be held to the same standard as the quiet ones.
// A rollout that is genuinely moving has to be excluded from every churn comparison; one
// that is stopped must not move either, so phase 3 can require that the upgrade left even
// this non-converged workload's pods alone, and then that lifting the partition still
// finishes the rollout the old controller started.
func buildMidRolloutFixture(ns string) *workloadsv1alpha2.RoleBasedGroup {
	template := wrappersv2.BuildBasicPodTemplateSpec()

	return wrappersv2.BuildBasicRoleBasedGroup(fxMidRoll, ns).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole(midRollRole).
				WithReplicas(midRollReplicas).
				// One at a time and no surge, so the instances above the partition roll
				// in sequence rather than all at once.
				WithRollingUpdate(wrappersv2.BuildRollingUpdate(1, 0)).
				WithTemplate(&template).
				Obj(),
		}).Obj()
}

// buildPendingFixture never becomes ready: its pod cannot be scheduled anywhere.
//
// A Pending pod is state the upgrade must leave alone just as much as a Running one, and
// it is the case every other fixture excludes by construction. It also answers a
// different question from the rest of the suite: a controller that only copes with
// converged objects would fail on this one while every other assertion still passed.
//
// A node label nothing carries is used rather than an unsatisfiable resource request,
// which stops being unsatisfiable on a larger cluster.
func buildPendingFixture(ns string) *workloadsv1alpha2.RoleBasedGroup {
	template := wrappersv2.BuildBasicPodTemplateSpec()
	template.Spec.NodeSelector = map[string]string{unschedulableNodeLabel: "true"}

	return wrappersv2.BuildBasicRoleBasedGroup(fxPending, ns).
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("stuck").
				WithReplicas(1).
				WithTemplate(&template).
				Obj(),
		}).Obj()
}

func buildComponent(name string) workloadsv1alpha2.InstanceComponent {
	template := wrappersv2.BuildBasicPodTemplateSpec()
	template.Spec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	return workloadsv1alpha2.InstanceComponent{
		Name:     name,
		Size:     ptr.To(int32(1)),
		Template: template,
	}
}

// buildSetFixture covers the RoleBasedGroupSet -> RoleBasedGroup owner chain. Its
// child RBG is picked up by captureAll like any other, since captureAll lists the
// namespace rather than taking a fixture list.
func buildSetFixture(ns string) *workloadsv1alpha2.RoleBasedGroupSet {
	set := wrappersv2.BuildBasicRoleBasedGroupSet(fxSet, ns).WithReplicas(1).Obj()
	set.Spec.GroupTemplate.Spec.Roles = []workloadsv1alpha2.RoleSpec{
		wrappersv2.BuildStandaloneRole("member").WithReplicas(1).Obj(),
	}
	return set
}

// buildV1alpha1Fixture is created through the v1alpha1 API, so writing it and every
// later read of it goes through the conversion webhook. It is stored as v1alpha2
// either way, so its pods are labelled like any other RBG's and captureAll sees it.
//
// The second role exists because the v1alpha1 conversion changes in this release are on
// the LeaderWorkerPattern and CustomComponents branches of convertRoleV1alpha1ToV2; a
// standalone role reaches neither. Its restartPolicy is deliberately left unset: the two
// releases disagree only about the empty value, so an unset field is the one input that
// can convert to a different v1alpha2 shape than v0.7.0 stored.
//
// The CustomComponents branch is left to the conversion unit tests, because no v1alpha1
// role that reaches it can run on either version. v1alpha1's workload field defaults to
// apps/v1 StatefulSet at the apiserver, the conversion turns that default into the
// role-workload-type annotation, and a role the StatefulSet reconciler is handed with a
// components pattern has no role-level template to build a pod from. So there are no
// pods of such a role for this suite to watch across the upgrade.
func buildV1alpha1Fixture(ns string) *workloadsv1alpha1.RoleBasedGroup {
	return wrappersv1.BuildBasicRoleBasedGroup(fxV1alpha1, ns).
		WithRoles([]workloadsv1alpha1.RoleSpec{
			wrappersv1.BuildBasicRole("legacy").WithReplicas(1).Obj(),
			wrappersv1.BuildLwsRole(v1alpha1LwsRole).WithReplicas(1).Obj(),
		}).Obj()
}

// buildV1alpha1SetFixture is a RoleBasedGroupSet created through the v1alpha1 API.
//
// RoleBasedGroupSet.ConvertTo delegates to convertSpecV1alpha1ToV2, the same function
// the RoleBasedGroup conversion uses, so anything the webhook rewrites on a
// RoleBasedGroup it also rewrites here -- and only the RoleBasedGroup half of that
// shared path was covered. Its template holds an LWS role for the same reason as the
// second role of buildV1alpha1Fixture: that is the branch the conversion changes touch.
//
// The child RoleBasedGroup it produces is stored as v1alpha2 and labelled like any
// other, so captureAll picks it up without being told about it.
func buildV1alpha1SetFixture(ns string) *workloadsv1alpha1.RoleBasedGroupSet {
	set := wrappersv1.BuildBasicRoleBasedGroupSet(fxV1alpha1Set, ns).WithReplicas(1).Obj()
	set.Spec.Template.Roles = []workloadsv1alpha1.RoleSpec{
		wrappersv1.BuildLwsRole(v1alpha1LwsRole).WithReplicas(1).Obj(),
	}
	return set
}

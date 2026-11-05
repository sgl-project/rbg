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

package discovery

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// EngineRuntimeFragment is one reference's pod-level fragments routed to one
// position: the fetched profile plus the role-side Containers override.
type EngineRuntimeFragment struct {
	Profile *workloadsv1alpha2.ClusterEngineRuntimeProfile
	Runtime workloadsv1alpha2.EngineRuntime
}

// EngineRuntimePatch is one reference's profile-side patch rules routed to
// one position. Targets holds the names of the reference's targetContainers
// entries verbatim, in authored order, all within the reference's named
// component — each listed name is the consent (D8), and the profile-side
// entries fire at every one. DeclaredContainers is the profile-declared
// name set (init containers and sidecars), captured at resolve time; the
// render pass resolves each listed name against the role-declared names
// first, then this set — the profile-declared half of the target
// vocabulary (D8).
type EngineRuntimePatch struct {
	ProfileName        string
	Patches            []workloadsv1alpha2.ContainerPatch
	Targets            []string
	DeclaredContainers map[string]struct{}
}

// EngineRuntimeRolePatch is one reference's role-side patches routed to one
// position. Targets holds the reference's targetContainers entries verbatim;
// each entry's extraPatches land at that entry's container, after the
// profile-side pass (D10). DeclaredContainers is the profile-declared name
// set (init containers and sidecars), captured at resolve time; the render
// pass resolves each entry's target against the role-declared names first,
// then this set — the profile-declared half of the target vocabulary (D8: a
// targetContainers entry may name a role-declared container or a container
// this reference's own profile declares).
type EngineRuntimeRolePatch struct {
	ProfileName        string
	Targets            []workloadsv1alpha2.EngineRuntimeTargetContainer
	DeclaredContainers map[string]struct{}
}

// EngineRuntimePlan is the resolver's per-role output: the three routing
// artifacts the render passes consume, keyed by position. FragmentApplications
// feeds the fragment pass — pod-level fragments and the legacy containers
// merge follow the reference's component scope (its effective set);
// append-only, consent-free, idempotent. PatchApplications feeds the
// profile-side patch pass (the profile's patches at the consented addresses
// of the named component); RolePatchApplications feeds the role-side patch
// pass (the reference's patches into role-declared or profile-declared
// containers of the named component, per D8). All preserve role-list order
// within a position. Under standalonePattern the single position is the
// role-named component — the plan keys by role.Name, exactly the component
// main's renderer builds (KEP-173: the standalone component is named after
// the role, so `component: <role name>` selects it).
type EngineRuntimePlan struct {
	FragmentApplications  map[string][]EngineRuntimeFragment
	PatchApplications     map[string][]EngineRuntimePatch
	RolePatchApplications map[string][]EngineRuntimeRolePatch
	// Vocabulary lists the positions the role's pattern defines, in authored
	// order (leader/worker; component names; the role's own name under
	// standalonePattern) — the only positions the applications above may
	// key by. It is recorded only when the role references engine runtimes;
	// a plan that resolved none records no vocabulary and accepts any
	// position.
	Vocabulary []string
	// RoleName is the role the plan was resolved for, used to attribute
	// role-side patch errors at render time.
	RoleName string

	// unaimedWarned dedupes the render-side half of the unaimed-patch
	// warning — entries naming only profile-declared containers — so it
	// fires once per reference per resolve even when several templates
	// run a stage-3 pass. Keyed by profileName.
	unaimedWarned map[string]struct{}
}

// ShouldWarnUnaimed reports whether the render pass should emit the
// unaimed-patch warning for this profile, marking it warned so only the
// first caller per resolve returns true.
func (p *EngineRuntimePlan) ShouldWarnUnaimed(profileName string) bool {
	if p.unaimedWarned == nil {
		p.unaimedWarned = make(map[string]struct{}, 1)
	}
	if _, warned := p.unaimedWarned[profileName]; warned {
		return false
	}
	p.unaimedWarned[profileName] = struct{}{}
	return true
}

// HasPosition reports whether name is a position the role's pattern defines.
// A plan that recorded no vocabulary (the role references no engine runtimes)
// accepts every position: nothing is routed, so there is nothing to mis-key.
func (p *EngineRuntimePlan) HasPosition(name string) bool {
	if len(p.Vocabulary) == 0 {
		return true
	}
	for _, position := range p.Vocabulary {
		if position == name {
			return true
		}
	}
	return false
}

// ResolveEngineRuntimes runs once per role render, before any per-template
// pass. It re-runs the shared role-side validation function
// (workloadsv1alpha2.ValidateEngineRuntimes — the same function the
// RoleBasedGroup webhook invokes at write time) as the authoritative gate
// for writes stored with the webhook bypassed, then computes each
// reference's effective component set, routes its fragments and both patch
// dimensions, and validates what no per-template pass can see whole:
// unknown components and targetContainers without a component (the D7
// re-check). Role-side patch targets are deliberately not validated here —
// D8 resolves them at render time against the role-declared names plus the
// reference's own profile declarations, which only the per-template pass
// can see. The unaimed-patch warning is emitted here,
// once per reference per render — a per-template pass would duplicate it
// under leaderWorkerPattern once both templates run a pass.
func ResolveEngineRuntimes(
	ctx context.Context,
	k8sClient client.Client,
	recorder record.EventRecorder,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
) (*EngineRuntimePlan, error) {
	plan := &EngineRuntimePlan{
		FragmentApplications:  map[string][]EngineRuntimeFragment{},
		PatchApplications:     map[string][]EngineRuntimePatch{},
		RolePatchApplications: map[string][]EngineRuntimeRolePatch{},
		unaimedWarned:         map[string]struct{}{},
	}
	if len(role.EngineRuntimes) == 0 {
		return plan, nil
	}
	plan.RoleName = role.Name

	// The render-time re-run of the shared validation function (KEP-531
	// Validation Layer Split): the webhook reports these at write time —
	// including a workload-type annotation flip to a deprecated surface,
	// since the webhooks re-validate the whole object on CREATE and UPDATE —
	// and this re-run is the authoritative gate for the webhook-absent
	// window: an uninstalled webhook configuration validates nothing, and
	// objects written during an old-controller window reach the renderer by
	// reconcile, not by admission. The rules: targetContainers requiring
	// component except under standalonePattern, same-profile
	// effective-component-set overlap (D6, with the compatibility-pair
	// exemption), the standalonePattern component rule, duplicate env-name
	// creation within one entry's extraPatches, and the new fields on a
	// role routed to a deprecated surface.
	if err := workloadsv1alpha2.ValidateEngineRuntimes(role); err != nil {
		emitEngineRuntimeWarning(ctx, recorder, rbg, ReasonEngineRuntimeRenderFailed,
			fmt.Sprintf("engine runtime render failed: role %q: %s", role.Name, err.Error()))
		return plan, NewRenderError(err)
	}

	vocabulary, positions, defaults := positionVocabulary(role)
	plan.Vocabulary = positions

	for _, runtime := range role.EngineRuntimes {
		profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: runtime.ProfileName}, profile); err != nil {
			err = fmt.Errorf("get engine runtime %s, error: %s", runtime.ProfileName, err.Error())
			return plan, failRef(ctx, recorder, rbg, runtime, err)
		}

		effective, err := effectivePositions(role, runtime, vocabulary, defaults)
		if err != nil {
			return plan, failRef(ctx, recorder, rbg, runtime, err)
		}

		for _, position := range effective {
			plan.FragmentApplications[position] = append(
				plan.FragmentApplications[position],
				EngineRuntimeFragment{Profile: profile, Runtime: runtime},
			)
		}

		if len(runtime.TargetContainers) > 0 {
			// The presence rule's standalonePattern exemption: the pattern
			// has exactly one component, so a component-less reference
			// carrying targetContainers is exactly as scoped as
			// `component: <role name>` — same effective set, same
			// ownership (KEP-531 Scope Ownership). The non-standalone
			// rejection is ValidateEngineRuntimes' — the re-run above is
			// the render-time authoritative gate for the presence rule.
			// The list is +listType=map keyed by name, so the apiserver
			// rejects a container listed twice at write time; this
			// defensive re-check covers writes stored with the webhook and
			// the schema both bypassed.
			seen := make(map[string]struct{}, len(runtime.TargetContainers))
			for _, tc := range runtime.TargetContainers {
				if _, dup := seen[tc.Name]; dup {
					return plan, failRef(ctx, recorder, rbg, runtime, fmt.Errorf(
						"role %q reference %q lists container %q twice in targetContainers",
						role.Name, runtime.ProfileName, tc.Name))
				}
				seen[tc.Name] = struct{}{}
			}
		}

		// The position both patch dimensions key by: the named component,
		// or — under the standalonePattern exemption — the role-named
		// single component.
		patchPosition := runtime.Component
		if patchPosition == "" {
			patchPosition = role.Name
		}

		if len(runtime.TargetContainers) == 0 {
			if len(profile.Spec.ContainerPatches) > 0 {
				emitEngineRuntimeWarning(ctx, recorder, rbg, ReasonUnaimedContainerPatch, fmt.Sprintf(
					"profile %q declares containerPatches but the reference lists no targetContainers; "+
						"the patches do not fire — set component and list the containers in targetContainers to opt in",
					runtime.ProfileName))
			}
		} else {
			// The profile-declared half of this reference's target
			// vocabulary: a targetContainers entry may name a role-declared
			// container or one this reference's own profile declares, so
			// the resolver routes the names unvalidated — the render pass
			// resolves each against the role-declared names snapshotted
			// before injection plus this set.
			declared := make(map[string]struct{},
				len(profile.Spec.InitContainers)+len(profile.Spec.Containers))
			for _, c := range profile.Spec.InitContainers {
				declared[c.Name] = struct{}{}
			}
			for _, c := range profile.Spec.Containers {
				declared[c.Name] = struct{}{}
			}
			targets := make([]string, len(runtime.TargetContainers))
			for i, tc := range runtime.TargetContainers {
				targets[i] = tc.Name
			}
			if len(profile.Spec.ContainerPatches) > 0 {
				plan.PatchApplications[patchPosition] = append(
					plan.PatchApplications[patchPosition],
					EngineRuntimePatch{
						ProfileName:        runtime.ProfileName,
						Patches:            profile.Spec.ContainerPatches,
						Targets:            targets,
						DeclaredContainers: declared,
					})
			}
			hasExtraPatches := false
			for _, tc := range runtime.TargetContainers {
				if len(tc.ExtraPatches) > 0 {
					hasExtraPatches = true
					break
				}
			}
			if hasExtraPatches {
				for _, tc := range runtime.TargetContainers {
					for i := range tc.ExtraPatches {
						if tc.ExtraPatches[i].JSONPatch == nil {
							return plan, failRef(ctx, recorder, rbg, runtime, fmt.Errorf(
								"role %q reference %q targetContainers[%q].extraPatches[%d]: no patch field is set; exactly one must be",
								role.Name, runtime.ProfileName, tc.Name, i))
						}
					}
				}
				plan.RolePatchApplications[patchPosition] = append(
					plan.RolePatchApplications[patchPosition],
					EngineRuntimeRolePatch{
						ProfileName:        runtime.ProfileName,
						Targets:            runtime.TargetContainers,
						DeclaredContainers: declared,
					})
			}
		}
	}
	return plan, nil
}

// failRef emits the render-failed warning for a reference-scoped error and
// returns the error marked as a render failure (same message) — render
// outcomes are warning events on the referencing RBG, never conditions
// (KEP-531 events-not-conditions split).
func failRef(
	ctx context.Context,
	recorder record.EventRecorder,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	runtime workloadsv1alpha2.EngineRuntime,
	err error,
) error {
	emitEngineRuntimeWarning(ctx, recorder, rbg, ReasonEngineRuntimeRenderFailed,
		fmt.Sprintf("engine runtime %q render failed: %s", runtime.ProfileName, err.Error()))
	return NewRenderError(err)
}

// effectivePositions computes a reference's effective position set: the
// single named component when component is set, else the pattern's default
// set (per-pattern per D2 — leader-only under leaderWorkerPattern, every
// declared component under customComponentsPattern, the single
// role-named component under standalonePattern). The explicit form is
// validated against the vocabulary; under standalonePattern that
// vocabulary is exactly {role.Name} — the standalone component is named
// after the role (KEP-173), so `component: <role name>` selects it and any
// other name fails the render vocabulary check.
func effectivePositions(
	role *workloadsv1alpha2.RoleSpec,
	runtime workloadsv1alpha2.EngineRuntime,
	vocabulary map[string]struct{},
	defaults []string,
) ([]string, error) {
	if runtime.Component == "" {
		return defaults, nil
	}
	if _, legal := vocabulary[runtime.Component]; !legal {
		if role.GetStandalonePattern() != nil {
			return nil, fmt.Errorf(
				"role %q reference %q names component %q, but a standalonePattern role has exactly one "+
					"component, named after the role — component must equal %q or stay absent",
				role.Name, runtime.ProfileName, runtime.Component, role.Name)
		}
		return nil, fmt.Errorf(
			"role %q reference %q names component %q, which the role's pattern does not define",
			role.Name, runtime.ProfileName, runtime.Component)
	}
	return []string{runtime.Component}, nil
}

// positionVocabulary returns the legal position names of the role's pattern,
// the render vocabulary (the ordered positions a plan's applications may
// key by), and the default effective set used when component is absent — the
// pattern's default component set per D2 (main's published behavior:
// leader-only under leaderWorkerPattern, every declared component under
// customComponentsPattern, the single role-named component under
// standalonePattern). This is the render-time twin of the write-time
// mapping in api/workloads/v1alpha2/rolebasedgroup_validation.go
// (validateEngineRuntimeComponentVocabulary, engineRuntimeEffectiveComponents);
// the two are an intentional mirror, not a shared helper — see the
// divergence note in the legacy collapse below and the twin's comment.
func positionVocabulary(role *workloadsv1alpha2.RoleSpec) (map[string]struct{}, []string, []string) {
	// A legacy Deployment/StatefulSet render produces one template — the role
	// template — whatever the role's pattern, and main's pod reconciler ran
	// the sidecar injector into it unconditionally. KEP-531 compatibility
	// keeps that behavior verbatim: the vocabulary collapses to the single
	// role-named position, so scenario-1 references (the only shape those
	// surfaces can carry — the new fields are rejected at write time and
	// re-rejected by the render-time validation re-run) key by role.Name,
	// which is the position the Deployment/StatefulSet reconcilers render.
	// The write-time mapping deliberately does NOT collapse here: D6 overlap
	// is judged on pattern semantics regardless of render path. LWS is not
	// collapsed either: its render is a two-template (leader/worker) path,
	// so the pattern vocabulary applies.
	switch role.GetWorkloadType() {
	case constants.DeploymentWorkloadType, constants.StatefulSetWorkloadType:
		vocabulary := map[string]struct{}{role.Name: {}}
		return vocabulary, []string{role.Name}, []string{role.Name}
	}
	switch {
	case role.GetStandalonePattern() != nil:
		// The standalone component is named after the role — main's renderer
		// builds it WithName(role.Name) — so the plan keys by the role's
		// own name and `component: <role name>` resolves to it.
		vocabulary := map[string]struct{}{role.Name: {}}
		return vocabulary, []string{role.Name}, []string{role.Name}
	case role.GetLeaderWorkerPattern() != nil:
		leader := string(constants.LeaderComponentType)
		worker := string(constants.WorkerComponentType)
		// D2: the absent-component default is main's published behavior — the
		// worker template runs no sidecar stage, so only the leader position
		// is in the default set. component: worker is the new capability that
		// reaches it.
		return map[string]struct{}{leader: {}, worker: {}}, []string{leader, worker}, []string{leader}
	case role.GetCustomComponentsPattern() != nil:
		components := role.GetCustomComponentsPattern().Components
		vocabulary := make(map[string]struct{}, len(components))
		names := make([]string, 0, len(components))
		for _, component := range components {
			vocabulary[component.Name] = struct{}{}
			names = append(names, component.Name)
		}
		return vocabulary, names, names
	default:
		// The construct dispatch rejects a pattern-less role before the
		// resolver runs; the role-named single position is the least
		// surprising fallback for direct callers.
		vocabulary := map[string]struct{}{role.Name: {}}
		return vocabulary, []string{role.Name}, []string{role.Name}
	}
}

// emitEngineRuntimeWarning logs the warning and, when a recorder is set,
// emits a v1.EventTypeWarning event on the referencing RBG. Nil recorder is
// tolerated (log-only).
func emitEngineRuntimeWarning(
	ctx context.Context, recorder record.EventRecorder,
	rbg *workloadsv1alpha2.RoleBasedGroup, reason, message string,
) {
	log.FromContext(ctx).Info(message)
	if recorder != nil {
		recorder.Event(rbg, v1.EventTypeWarning, reason, message)
	}
}

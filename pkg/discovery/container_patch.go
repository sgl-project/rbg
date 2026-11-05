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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// Warning-event reasons emitted by the engine runtime resolver and the
// render passes on the referencing RBG.
const (
	// ReasonUnaimedContainerPatch: a patch-carrying profile is referenced but
	// the role lists no targetContainers — the patches do not fire.
	ReasonUnaimedContainerPatch = "UnaimedContainerPatch"
	// ReasonEngineRuntimeRenderFailed: emitted alongside a render error for
	// timeline observability.
	ReasonEngineRuntimeRenderFailed = "EngineRuntimeRenderFailed"
	// ReasonEngineRuntimeWrapShadowed: a role-side /command/0 insert
	// landed ahead of a profile-side wrap on the same container, so the
	// role's wrap runs outermost (KEP-531 non-goals: no structural
	// ordering guarantee; role-side /command/0 lands outermost). The
	// render succeeds — the event exists so the shadowing is never
	// silent. Env overlaps never fire it: they are never adjudicated
	// and surface through EngineRuntimeEnvCollision instead.
	ReasonEngineRuntimeWrapShadowed = "EngineRuntimeWrapShadowed"
	// ReasonEngineRuntimeNameCollision: a cross-profile container or volume
	// name collision skipped the later profile's same-named fragment — the
	// earlier reference wins, and the operator should know a fragment they
	// believe present was not delivered.
	ReasonEngineRuntimeNameCollision = "EngineRuntimeNameCollision"
	// ReasonEngineRuntimeEnvCollision: a patch pass created an env entry
	// whose name already exists in the container's rendered list at that
	// point — from the role template, another reference, or the other
	// side. The render lands every entry verbatim and adds no winner rule
	// of its own: kubelet resolves same-name entries by last occurrence
	// and $(VAR) expands against earlier entries only, so the event names
	// the container, the env name, both sources and the later entry. The
	// legacy containers merge's duplicates never fire it.
	ReasonEngineRuntimeEnvCollision = "EngineRuntimeEnvCollision"
	// ReasonEngineRuntimeCapabilityFormHazard: a capability patch value not
	// in the canonical uppercase form — lowercase works on both runtimes but
	// a CAP_-prefixed form is silently dropped by containerd, so the
	// non-canonical spelling gets a warning rather than silence. Attributed
	// to the role: capabilities are a role-side-only root (KEP-531 D11).
	ReasonEngineRuntimeCapabilityFormHazard = "EngineRuntimeCapabilityFormHazard"
)

// reservedConfigVolumeName is the controller-injected discovery ConfigMap
// volume (stateful roles' config mount). It is a third ownership class —
// neither role nor profile content — so a patch entry on either side adding
// a volumeMount naming it is a render error (KEP-531 Application Order).
const reservedConfigVolumeName = "rbg-cluster-config"

// The patch grammar's finite vocabulary: the one implemented op and the five
// container-relative roots (KEP-531 Patch Grammar).
const (
	patchOpAdd = "add"

	patchRootCommand      = "command"
	patchRootArgs         = "args"
	patchRootEnv          = "env"
	patchRootVolumeMounts = "volumeMounts"
	patchRootCapabilities = "capabilities"
)

// canonicalCapabilityForm matches the canonical uppercase Linux capability
// spelling, `^[A-Z]+(_[A-Z]+)*$`. A name not matching it (lowercase), or one
// carrying the CAP_ macro prefix — which matches the shape but is silently
// dropped by containerd — emits a warning event, not an error: lowercase
// works on both containerd and CRI-O, and the prefixed form works on CRI-O —
// only the runtime can judge the name's validity, so the renderer holds no
// capability list (KEP-531 Patch Grammar).
var canonicalCapabilityForm = regexp.MustCompile(`^[A-Z]+(_[A-Z]+)*$`)

// RenderError marks an engine runtime render failure (resolver or render-pass
// error) so a caller that needs to tell render outcomes apart from
// infrastructure errors can, with isRenderError. The RBG controller instead
// relies on the construction site: every path that returns a RenderError has
// already emitted the ReasonEngineRuntimeRenderFailed warning event — render
// outcomes surface as events, never as status conditions (a condition is
// future work). Wrapping preserves the underlying error's message and unwrap
// chain, so error strings and errors.Is/As behavior are unchanged.
type RenderError struct {
	err error
}

// NewRenderError wraps err as an engine runtime render failure. A nil err
// returns nil so callers can wrap unconditionally.
func NewRenderError(err error) *RenderError {
	if err == nil {
		return nil
	}
	return &RenderError{err: err}
}

func (e *RenderError) Error() string { return e.err.Error() }

func (e *RenderError) Unwrap() error { return e.err }

// isRenderError reports whether err — or any error it wraps or joins — is an
// engine runtime render failure.
func isRenderError(err error) bool {
	var renderErr *RenderError
	return errors.As(err, &renderErr)
}

// positionalInsertRecorder accumulates the container names that took a
// positional insert (a numeric-index, non-"-" element pointer) during one
// patch pass. Both passes thread it through applyJSONPatch: the profile-side
// pass's record feeds the role-side pass's D10 positional-shadowing report.
// A nil pointer records nothing.
type positionalInsertRecorder struct {
	containers map[string]struct{}
}

// record notes one positional insert on containerName. A "-" (append)
// pointer never records: an append lands after all existing content and
// shadows nothing.
func (p *positionalInsertRecorder) record(containerName, seg string) {
	if p == nil || seg == "-" {
		return
	}
	if p.containers == nil {
		p.containers = make(map[string]struct{})
	}
	p.containers[containerName] = struct{}{}
}

// envCreationTracker records, per rendered container, the env names the
// profile-side pass created during one template render (stage 3 — shared
// across every covering reference, so cross-profile same names resolve
// positionally per the Patch Grammar). It is the profile side's share of
// the role-ownership boundary: a name the container already carries is
// role-owned unless a profile-side insert of this render created it, in
// which case a later profile's same-named entry resolves positionally
// instead of tripping the role-owned rule.
type envCreationTracker struct {
	created map[string]map[string]struct{}
}

func (t *envCreationTracker) record(containerName, envName string) {
	if t == nil {
		return
	}
	if t.created == nil {
		t.created = make(map[string]map[string]struct{})
	}
	names := t.created[containerName]
	if names == nil {
		names = make(map[string]struct{})
		t.created[containerName] = names
	}
	names[envName] = struct{}{}
}

func (t *envCreationTracker) has(containerName, envName string) bool {
	if t == nil {
		return false
	}
	_, ok := t.created[containerName][envName]
	return ok
}

// patchSource names the author of a patch list so render errors attribute
// to the side that wrote the rules, in the KEP-531 attribution shapes:
// the role side names the referencing field, the profile side the
// patch-carrying field.
type patchSource struct {
	kind      string // "profile" or "role"
	name      string // profile name; role name on the role side
	ref       string // the reference's profileName, role side only
	container string // the targetContainers entry name, role side only
}

func profilePatchSource(profileName string) patchSource {
	return patchSource{kind: "profile", name: profileName}
}

func rolePatchSource(roleName, profileName, containerName string) patchSource {
	return patchSource{kind: "role", name: roleName, ref: profileName, container: containerName}
}

func (s patchSource) describe() string {
	if s.kind == "role" {
		return fmt.Sprintf("role %q engineRuntimes[%q].targetContainers[%q].extraPatches", s.name, s.ref, s.container)
	}
	return fmt.Sprintf("profile %q containerPatches", s.name)
}

// renderedVolumeNames collects the volume names visible at patch time (the
// rendered spec's volumes already include the position's fragment volumes).
func renderedVolumeNames(podSpec *v1.PodTemplateSpec) map[string]struct{} {
	volumeNames := make(map[string]struct{}, len(podSpec.Spec.Volumes))
	for _, vol := range podSpec.Spec.Volumes {
		volumeNames[vol.Name] = struct{}{}
	}
	return volumeNames
}

// applyContainerPatch applies one reference's profile-side containerPatches
// to the rendered pod spec, firing every entry at every listed
// role-declared targetContainers address (KEP-531 Per-Dimension Gates), in
// list order: profile patches are written for the consumer's engine
// containers, so an entry naming a profile-declared container is consent
// only for its own extraPatches, which the stage-4 pass delivers — a
// publisher's launcher wrap can never land around its own sidecar's
// command. Targets resolve
// per D8 against the role-declared names (spec.containers and
// spec.initContainers, snapshotted before any injection) plus the container
// names this reference's own profile declares; anything else — a name
// injected only by a different reference or profile — is a render error.
// A same-named role declaration always wins over a profile declaration.
// The profile-side root allowlist (D11) is enforced by validatePatchList:
// only /env, /volumeMounts and /command/0 are addressable.
//
// positional accumulates the containers that took a command positional
// insert — the profile-side half of the wrap-shadowing report.
// appendedEnv — shared across every reference of this template render —
// records the env names the profile side created, separating role-owned
// names (a "-" append onto one is a render error; a /env/0 head insert
// is the sanctioned default) from cross-profile positional resolution
// (legal either gesture). warn carries the advisory render signals
// (EngineRuntimeEnvCollision); a nil warn is tolerated.
func applyContainerPatch(
	podSpec *v1.PodTemplateSpec,
	roleContainerNames map[string]struct{},
	roleInitContainerNames map[string]struct{},
	roleName string,
	targets []string,
	patches []workloadsv1alpha2.ContainerPatch,
	profileName string,
	declaredContainers map[string]struct{},
	positional *positionalInsertRecorder,
	appendedEnv *envCreationTracker,
	warn func(reason, message string),
) error {
	if len(patches) == 0 || len(targets) == 0 {
		return nil
	}
	source := profilePatchSource(profileName)
	if err := validatePatchList(patches, source, true); err != nil {
		return err
	}
	volumeNames := renderedVolumeNames(podSpec)

	for _, target := range targets {
		container := resolvePatchTarget(
			podSpec, roleContainerNames, roleInitContainerNames, declaredContainers,
			target,
		)
		if container == nil {
			return fmt.Errorf(
				"role %q engineRuntimes[%q].targetContainers[%q]: the entry's container is neither role-declared "+
					"nor declared by profile %q — a name injected only by a different reference or profile is not addressable",
				roleName, profileName, target, profileName)
		}
		if !isRoleDeclaredTarget(roleContainerNames, roleInitContainerNames, target) {
			// A profile-declared target: consent covers only the entry's
			// own extraPatches, which the stage-4 pass delivers.
			continue
		}
		pc := &patchContext{
			volumeNames:   volumeNames,
			appendedEnv:   appendedEnv,
			positional:    positional,
			source:        source,
			containerName: target,
			roleSide:      false,
			warn:          warn,
		}
		for i, rule := range patches {
			if err := applyJSONPatch(pc, container, *rule.JSONPatch, i); err != nil {
				return err
			}
		}
	}
	return nil
}

// isRoleDeclaredTarget reports whether a targetContainers entry names a
// container the role's own template declares — the consent address class
// the profile's containerPatches fire at (spec.containers and
// spec.initContainers alike; the grammar distinguishes targets by name,
// not by kind).
func isRoleDeclaredTarget(
	roleContainerNames, roleInitContainerNames map[string]struct{},
	target string,
) bool {
	_, inContainers := roleContainerNames[target]
	_, inInit := roleInitContainerNames[target]
	return inContainers || inInit
}

// ApplyEngineRuntimePatches is the stage-3 render-time pass for one
// position's template: it feeds each per-reference entry to
// ApplyContainerPatch in engineRuntimes list order, so the profile-side
// passes of every covering reference complete before any role-side pass
// begins (KEP-531 Application Order — the passes are phased by author, not
// by reference). The env creation tracker is shared across the entries, so
// cross-profile same-named env entries resolve positionally. It returns the
// container names that took a command positional insert — the
// profile-side half of the wrap-shadowing report, which the
// role-side pass consumes.
//
// The render-side half of the unaimed-patch warning fires here: when a
// patch-carrying profile's consent addresses name no role-declared
// container of this template — every entry profile-declared — the patches
// fire nowhere and the render succeeds; plan dedupes the warning per
// reference so it fires once per resolve, not once per template.
// recorder and rbg carry the warning events; a nil recorder is tolerated
// (log-only).
func ApplyEngineRuntimePatches(
	ctx context.Context,
	podSpec *v1.PodTemplateSpec,
	roleContainerNames map[string]struct{},
	roleInitContainerNames map[string]struct{},
	roleName string,
	entries []EngineRuntimePatch,
	plan *EngineRuntimePlan,
	recorder record.EventRecorder,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) (map[string]struct{}, error) {
	var positional positionalInsertRecorder
	appendedEnv := &envCreationTracker{}
	warn := func(reason, message string) {
		emitEngineRuntimeWarning(ctx, recorder, rbg, reason, message)
	}
	for _, entry := range entries {
		if err := applyContainerPatch(
			podSpec, roleContainerNames, roleInitContainerNames, roleName,
			entry.Targets, entry.Patches, entry.ProfileName, entry.DeclaredContainers,
			&positional, appendedEnv, warn,
		); err != nil {
			return nil, err
		}
		if !hasRoleDeclaredTarget(roleContainerNames, roleInitContainerNames, entry.Targets) &&
			plan != nil && plan.ShouldWarnUnaimed(entry.ProfileName) {
			emitEngineRuntimeWarning(ctx, recorder, rbg, ReasonUnaimedContainerPatch, fmt.Sprintf(
				"profile %q declares containerPatches but no targetContainers entry of the reference names a "+
					"role-declared container; the patches do not fire — list the role-declared containers in "+
					"targetContainers to opt in",
				entry.ProfileName))
		}
	}
	return positional.containers, nil
}

// hasRoleDeclaredTarget reports whether any of the listed targets names a
// container the role's own template declares.
func hasRoleDeclaredTarget(
	roleContainerNames, roleInitContainerNames map[string]struct{},
	targets []string,
) bool {
	for _, target := range targets {
		if isRoleDeclaredTarget(roleContainerNames, roleInitContainerNames, target) {
			return true
		}
	}
	return false
}

// ApplyRoleEngineRuntimePatches is the stage-4 render-time pass for one
// position's template: the role-side extraPatches of one position's
// references, in reference order then list order — the KEP-531 application
// order, after the fragment/legacy-merge pass, the built-in injector chain
// and the profile-side pass: role-side patches land last (D10), so what the
// role writes lands where it is written. Each entry's target resolves per
// D8 against the role-declared names captured by the caller before any
// profile fragment injection plus the container names this reference's own
// profile declares; anything else — a name injected only by a different
// reference or profile — is a render error. Role content flowing into a
// profile-declared container is the role author's explicit decision (D9):
// a role-declared volume mounted into a profile container, a secretKeyRef
// env value handed to the publisher's image — all legal, reviewable in the
// role spec. The role side addresses all five roots at any index (D11),
// and a same-named env append is the override gesture — positional win,
// never an error.
//
// profilePositionalInserts is the profile-side pass's positional-insert
// record for this template (ApplyEngineRuntimePatches's return value); the
// returned map is the D10 positional-shadowing report — the containers
// where this pass's positional inserts landed ahead of the profile-side
// pass's. Shadowing never fails the render; the caller emits a warning
// event so it is never silent. recorder and rbg carry the capability
// form-hazard warning (attributed to the role); a nil recorder is
// tolerated (log-only).
func ApplyRoleEngineRuntimePatches(
	ctx context.Context,
	podSpec *v1.PodTemplateSpec,
	roleContainerNames map[string]struct{},
	roleInitContainerNames map[string]struct{},
	entries []EngineRuntimeRolePatch,
	roleName string,
	profilePositionalInserts map[string]struct{},
	recorder record.EventRecorder,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) (map[string]struct{}, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	volumeNames := renderedVolumeNames(podSpec)
	var positional positionalInsertRecorder
	for _, entry := range entries {
		for _, tc := range entry.Targets {
			if len(tc.ExtraPatches) == 0 {
				continue
			}
			source := rolePatchSource(roleName, entry.ProfileName, tc.Name)
			// The intra-list duplicate checks re-run at render: the
			// resolver re-runs the shared validation function as the
			// authoritative gate, and this pass is the per-template
			// backstop for writes stored with the webhook bypassed.
			if err := validatePatchList(tc.ExtraPatches, source, false); err != nil {
				return nil, err
			}
			for i, patch := range tc.ExtraPatches {
				container := resolvePatchTarget(
					podSpec, roleContainerNames, roleInitContainerNames, entry.DeclaredContainers,
					tc.Name,
				)
				if container == nil {
					return nil, fmt.Errorf(
						"%s[%d] targets container %q, which is neither role-declared nor declared by profile %q",
						source.describe(), i, tc.Name, entry.ProfileName)
				}
				pc := &patchContext{
					volumeNames:   volumeNames,
					positional:    &positional,
					source:        source,
					containerName: tc.Name,
					roleSide:      true,
					warn: func(reason, message string) {
						emitEngineRuntimeWarning(ctx, recorder, rbg, reason, message)
					},
				}
				if err := applyJSONPatch(pc, container, *patch.JSONPatch, i); err != nil {
					return nil, err
				}
			}
		}
	}
	return shadowedContainers(positional.containers, profilePositionalInserts), nil
}

// shadowedContainers computes the KEP-531 D10 positional-shadowing report:
// the containers where the role-side pass made a positional insert over a
// container the profile-side pass also positionally inserted. Because the
// role-side pass runs last, its insert lands at the written index and shifts
// profile-side content at or after it — a profile whose wrap must be
// outermost is thereby shadowed. The report is per container: it cannot say
// which specific elements moved, only that both sides claimed absolute
// positions on the same container, so it errs toward reporting rather than
// staying silent.
func shadowedContainers(rolePositional, profilePositionalInserts map[string]struct{}) map[string]struct{} {
	if len(rolePositional) == 0 || len(profilePositionalInserts) == 0 {
		return nil
	}
	shadowed := make(map[string]struct{}, len(rolePositional))
	for container := range rolePositional {
		if _, ok := profilePositionalInserts[container]; ok {
			shadowed[container] = struct{}{}
		}
	}
	return shadowed
}

// resolvePatchTarget resolves one targetContainers entry name to its
// rendered container, per the KEP-531 D8 precedence shared by both patch
// passes: (1) a name the role's own template declares in spec.containers —
// the role-owned member; (2) a name it declares in spec.initContainers —
// the role-owned init container (the legacy containers merge cannot reach
// init containers; the patch grammar closes that gap); (3) a name this
// reference's own profile declares, located by name in the rendered pod —
// init containers included, profile-declared targets span both lists;
// (4) anything else — a name injected only by a different reference or
// profile — returns nil and the caller renders the attributed error. A
// name the role's own template also declares resolves to the role-owned
// container — the same precedence injection applies.
func resolvePatchTarget(
	podSpec *v1.PodTemplateSpec,
	roleContainerNames map[string]struct{},
	roleInitContainerNames map[string]struct{},
	declaredContainers map[string]struct{},
	name string,
) *v1.Container {
	if _, ok := roleContainerNames[name]; ok {
		if container := findRenderedContainer(podSpec, name); container != nil {
			return container
		}
	}
	if _, ok := roleInitContainerNames[name]; ok {
		if container := findRenderedInitContainer(podSpec, name); container != nil {
			return container
		}
	}
	if _, ok := declaredContainers[name]; ok {
		return findRenderedProfileContainer(podSpec, name)
	}
	return nil
}

// findRenderedInitContainer finds an init container by name in the rendered
// spec's spec.initContainers — the locator for a role-declared init
// container target (role-owned under the append-if-missing merge).
func findRenderedInitContainer(podSpec *v1.PodTemplateSpec, name string) *v1.Container {
	for i := range podSpec.Spec.InitContainers {
		if podSpec.Spec.InitContainers[i].Name == name {
			return &podSpec.Spec.InitContainers[i]
		}
	}
	return nil
}

// findRenderedProfileContainer finds a container by name in the rendered
// spec — init containers first, then sidecars — because profile-declared
// targets include init containers and sidecars alike.
func findRenderedProfileContainer(podSpec *v1.PodTemplateSpec, name string) *v1.Container {
	for i := range podSpec.Spec.InitContainers {
		if podSpec.Spec.InitContainers[i].Name == name {
			return &podSpec.Spec.InitContainers[i]
		}
	}
	return findRenderedContainer(podSpec, name)
}

// validatePatchList enforces the patch list's list-scoped grammar once per
// list, before any per-target application, so the error fires exactly once
// per offending entry regardless of target count: presence (exactly one
// patch format set — today jsonPatch), op (add only), the path grammar
// (element pointer under the five roots), the profile-side root allowlist
// (D11: /env, /volumeMounts and /command/0 only — /args at any element,
// /command/<i> with i > 0, and /securityContext/capabilities/add are
// role-side-only, and a profile-side entry addressing one is a render error
// attributed to the profile), and the intra-list duplicate rules: two
// entries creating the same env name, and two entries creating the same
// (name, mountPath) volumeMount key — the earlier one is a dead letter, so
// a duplicate is always a mistake (the reference-side env half rides the
// shared validation function at write time and the resolver at render;
// this pass re-checks both halves as the render-time backstop).
func validatePatchList(
	patches []workloadsv1alpha2.ContainerPatch,
	source patchSource,
	profileSide bool,
) error {
	seenEnv := make(map[string]struct{}, len(patches))
	seenMounts := make(map[string]struct{}, len(patches))
	for i, rule := range patches {
		if rule.JSONPatch == nil {
			return fmt.Errorf("%s[%d]: no patch field is set; exactly one must be", source.describe(), i)
		}
		if rule.JSONPatch.Op != patchOpAdd {
			return fmt.Errorf(
				"%s[%d]: op %q is not supported; only add is implemented",
				source.describe(), i, rule.JSONPatch.Op)
		}
		if len(rule.JSONPatch.Value.Raw) == 0 {
			return fmt.Errorf("%s[%d]: op add requires a value", source.describe(), i)
		}
		root, seg, err := parsePatchPath(rule.JSONPatch.Path)
		if err != nil {
			return fmt.Errorf("%s[%d]: %w", source.describe(), i, err)
		}
		if profileSide {
			if err := checkProfileSideRoot(root, seg, source, i, rule.JSONPatch.Path); err != nil {
				return err
			}
		}
		switch root {
		case patchRootEnv:
			var env v1.EnvVar
			if err := decodePatchStruct(rule.JSONPatch.Value.Raw, &env); err != nil {
				continue // the per-target pass reports shape mismatches with path context
			}
			if env.Name == "" {
				continue
			}
			if _, dup := seenEnv[env.Name]; dup {
				return fmt.Errorf(
					"%s[%d]: duplicate env name %q created within one patch list — the earlier entry is a dead letter",
					source.describe(), i, env.Name)
			}
			seenEnv[env.Name] = struct{}{}
		case patchRootVolumeMounts:
			var mount v1.VolumeMount
			if err := decodePatchStruct(rule.JSONPatch.Value.Raw, &mount); err != nil {
				continue
			}
			if mount.MountPath == "" {
				continue
			}
			key := mount.Name + "\x00" + mount.MountPath
			if _, dup := seenMounts[key]; dup {
				return fmt.Errorf(
					"%s[%d]: duplicate volumeMount (name %q, mountPath %q) created within one patch list — "+
						"the earlier entry is a dead letter",
					source.describe(), i, mount.Name, mount.MountPath)
			}
			seenMounts[key] = struct{}{}
		}
	}
	return nil
}

// checkProfileSideRoot enforces the D11 profile-side root allowlist: env and
// volumeMounts address at any element; command addresses /command/0 only;
// args and capabilities are role-side-only.
func checkProfileSideRoot(root, seg string, source patchSource, idx int, path string) error {
	switch root {
	case patchRootEnv, patchRootVolumeMounts:
		return nil
	case patchRootCommand:
		if seg == "0" {
			return nil
		}
		return fmt.Errorf(
			"%s[%d] path %q: the profile side may address /command/0 only; /command/<i> with i > 0 is role-side-only",
			source.describe(), idx, path)
	default:
		return fmt.Errorf(
			"%s[%d] path %q: the profile side may address /env, /volumeMounts and /command/0 only; "+
				"/args and /securityContext/capabilities/add are role-side-only",
			source.describe(), idx, path)
	}
}

// patchContext carries one patch pass's shared state through applyJSONPatch:
// the volumes visible at patch time, the profile-side env creation tracker,
// the positional-insert recorder, the attribution source, whether the pass
// is role-side, and the warn closure for warning-class render signals (the
// capability form hazard).
type patchContext struct {
	volumeNames   map[string]struct{}
	appendedEnv   *envCreationTracker
	positional    *positionalInsertRecorder
	source        patchSource
	containerName string
	roleSide      bool
	warn          func(reason, message string)
}

// applyJSONPatch applies one ContainerJSONPatch to one container. Value
// checks run against the container's current state, which by construction
// contains only the mutations of earlier rules — so a violation fires only
// when the patch introduces it, never for a duplicate the role itself
// declared. positional, when non-nil, records the container each successful
// positional insert touched — the D10 shadowing report's raw material.
func applyJSONPatch(
	pc *patchContext,
	container *v1.Container,
	rule workloadsv1alpha2.ContainerJSONPatch,
	idx int,
) error {
	source := pc.source
	if rule.Op != patchOpAdd {
		return fmt.Errorf(
			"%s[%d]: op %q is not supported; only add is implemented",
			source.describe(), idx, rule.Op,
		)
	}
	if len(rule.Value.Raw) == 0 {
		return fmt.Errorf("%s[%d]: op add requires a value", source.describe(), idx)
	}

	pos, seg, err := parsePatchPath(rule.Path)
	if err != nil {
		return fmt.Errorf("%s[%d]: %w", source.describe(), idx, err)
	}
	if !pc.roleSide {
		if err := checkProfileSideRoot(pos, seg, source, idx, rule.Path); err != nil {
			return err
		}
	}

	switch pos {
	case patchRootCommand:
		value, err := decodePatchString(rule.Value.Raw)
		if err != nil {
			return patchPathError(source, idx, rule.Path, err)
		}
		if len(container.Command) == 0 {
			// Creating command is always an error: it would replace the
			// image ENTRYPOINT whatever else is set (KEP-531 Patch
			// Grammar, the Kubernetes entrypoint merge rule).
			return fmt.Errorf(
				"%s[%d] path %q targets container %q, which declares no command; "+
					"inserting would create one and silently discard the image ENTRYPOINT — declare the command to opt in",
				source.describe(), idx, rule.Path, pc.containerName,
			)
		}
		list, err := insertElement(container.Command, seg, value)
		if err != nil {
			return patchPathError(source, idx, rule.Path, err)
		}
		container.Command = list
		pc.positional.record(pc.containerName, seg)
	case patchRootArgs:
		value, err := decodePatchString(rule.Value.Raw)
		if err != nil {
			return patchPathError(source, idx, rule.Path, err)
		}
		if len(container.Command) == 0 && len(container.Args) == 0 {
			// Creating args discards nothing when command is set — the image
			// ENTRYPOINT and CMD are already ignored (the containerd
			// WithProcessArgs rule) — so the error fires only when the
			// container declares neither, where the created args would
			// replace the image CMD. The emptiness predicate is len == 0:
			// omitempty serialization and the CRI repeated-field protocol
			// do not distinguish nil from an explicitly written empty
			// array, so `command: []` counts as unset.
			return fmt.Errorf(
				"%s[%d] path %q targets container %q, which declares neither command nor args; "+
					"inserting args would create them and silently discard the image CMD — "+
					"declare command or args to opt in",
				source.describe(), idx, rule.Path, pc.containerName,
			)
		}
		list, err := insertElement(container.Args, seg, value)
		if err != nil {
			return patchPathError(source, idx, rule.Path, err)
		}
		container.Args = list
	case patchRootEnv:
		return applyEnvPatch(pc, container, rule, idx, seg)
	case patchRootVolumeMounts:
		var mount v1.VolumeMount
		if err := decodePatchStruct(rule.Value.Raw, &mount); err != nil {
			return patchPathError(source, idx, rule.Path, err)
		}
		if mount.MountPath == "" {
			return fmt.Errorf(
				"%s[%d] path %q: a volumeMount requires a mountPath", source.describe(), idx, rule.Path,
			)
		}
		if mount.Name == reservedConfigVolumeName {
			// The controller-injected rbg-cluster-config volume is a third
			// ownership class: a patch entry on either side mounting it is
			// a render error — neither author steers publisher or role code
			// at the controller's own config surface.
			return fmt.Errorf(
				"%s[%d] path %q adds a volumeMount naming %q, the controller's own config volume — "+
					"the name is reserved and not addressable from either side",
				source.describe(), idx, rule.Path, mount.Name,
			)
		}
		if err := checkVolumeMount(container, mount, pc.volumeNames, source, pc.containerName, idx, rule.Path); err != nil {
			return err
		}
		if hasEqualVolumeMount(container.VolumeMounts, mount) {
			// Full-field semantic equality — an idempotent no-op (KEP-531
			// Patch Grammar): the key matches and every other field is
			// equal, an absent field and an explicit zero counting as
			// equal.
			return nil
		}
		list, err := insertElement(container.VolumeMounts, seg, mount)
		if err != nil {
			return patchPathError(source, idx, rule.Path, err)
		}
		container.VolumeMounts = list
	case patchRootCapabilities:
		return applyCapabilitiesPatch(pc, container, rule, idx, seg)
	default:
		// Unreachable: parsePatchPath rejects other roots.
		return fmt.Errorf("%s[%d] path %q: unsupported target", source.describe(), idx, rule.Path)
	}
	return nil
}

// applyEnvPatch handles the /env/<i> root of applyJSONPatch: name presence,
// the RBG_ prefix reservation, the profile-side default-only gesture rule,
// the same-name advisory event, and the insert.
func applyEnvPatch(
	pc *patchContext,
	container *v1.Container,
	rule workloadsv1alpha2.ContainerJSONPatch,
	idx int,
	seg string,
) error {
	source := pc.source
	var env v1.EnvVar
	if err := decodePatchStruct(rule.Value.Raw, &env); err != nil {
		return patchPathError(source, idx, rule.Path, err)
	}
	if env.Name == "" {
		return fmt.Errorf(
			"%s[%d] path %q: an env var requires a name", source.describe(), idx, rule.Path,
		)
	}
	if strings.HasPrefix(env.Name, constants.EnvRBGPrefix) {
		// The RBG_ prefix belongs to the controller's own injection — a
		// third ownership class a patch entry on either side may not
		// create (KEP-531 Patch Grammar).
		return fmt.Errorf(
			"%s[%d] path %q adds env var %q: the RBG_ prefix is reserved for the controller's own injection",
			source.describe(), idx, rule.Path, env.Name,
		)
	}
	if !pc.roleSide {
		// Profile-side rules may only default. A name the container
		// already carries — role template, legacy merge or built-in
		// content — must be landed ahead of the role-owned entry:
		// /env/0 head insert, so the role's later entry wins the
		// positional contest. A "-" append (or any non-zero insert)
		// onto a role-owned name is a render error: profile content
		// never wins a same-name contest with role content. A name a
		// profile-side insert of this render created belongs to
		// cross-profile positional resolution — any gesture is legal.
		if hasEnvName(container.Env, env.Name) &&
			!pc.appendedEnv.has(pc.containerName, env.Name) &&
			seg != "0" {
			return fmt.Errorf(
				"%s[%d] path %q adds env var %q to container %q, which already carries the name; "+
					"the profile side may only default a role-owned name — head-insert at /env/0 so the "+
					"role's later entry wins, or publish a profile variant",
				source.describe(), idx, rule.Path, env.Name, pc.containerName,
			)
		}
		pc.appendedEnv.record(pc.containerName, env.Name)
	}
	// The role side needs no same-name check: role patches land last, so
	// a same-named append is the override gesture — the positional win
	// the legacy containers[].env merge performs on main.
	if hasEnvName(container.Env, env.Name) && pc.warn != nil {
		// Advisory only — every landing gesture here is legal (the
		// role-side append is the override; the profile-side head
		// insert is the default). The render lands every entry
		// verbatim and adds no winner rule of its own (KEP-531 Patch
		// Grammar): kubelet's last-occurrence resolution decides, and
		// $(VAR) consumers expand against earlier entries only — so the
		// collision is surfaced rather than left positional. The legacy
		// containers merge never fires it: it lands outside the patch
		// grammar as a plain append.
		existingSource := "the container's earlier rendered entries (role template, legacy merge, built-in injectors or an earlier patch pass)"
		if pc.appendedEnv != nil && pc.appendedEnv.has(pc.containerName, env.Name) {
			existingSource = "a profile-side insert of this render"
		}
		pc.warn(ReasonEngineRuntimeEnvCollision, fmt.Sprintf(
			"container %q: %s[%d] adds env var %q already present from %s; "+
				"kubelet resolves same-name entries by last occurrence and $(VAR) expands against earlier entries only — "+
				"re-declare consumers after the winning entry if alignment matters",
			pc.containerName, source.describe(), idx, env.Name, existingSource))
	}
	list, err := insertElement(container.Env, seg, env)
	if err != nil {
		return patchPathError(source, idx, rule.Path, err)
	}
	container.Env = list
	return nil
}

// applyCapabilitiesPatch handles the /securityContext/capabilities/add/<i>
// root of applyJSONPatch: name presence, the form-hazard advisory event, and
// the insert.
func applyCapabilitiesPatch(
	pc *patchContext,
	container *v1.Container,
	rule workloadsv1alpha2.ContainerJSONPatch,
	idx int,
	seg string,
) error {
	source := pc.source
	capability, err := decodePatchString(rule.Value.Raw)
	if err != nil {
		return patchPathError(source, idx, rule.Path, err)
	}
	if capability == "" {
		return fmt.Errorf(
			"%s[%d] path %q: a capability requires a name", source.describe(), idx, rule.Path,
		)
	}
	// Names pass through verbatim — no normalization, no list check:
	// the capability set drifts across kernel versions and the runtimes
	// diverge (containerd matches case-insensitively but silently drops
	// CAP_-prefixed and unknown names; CRI-O normalizes and fails on
	// unknown names), so the runtime is the only judge — exactly as for
	// hand-written pod specs. Two form hazards get a render-time
	// signal, warning events attributed to the role, never errors: a
	// name outside the canonical uppercase form, and a CAP_-prefixed
	// name — lowercase works on both runtimes, and the prefixed form
	// works on CRI-O but containerd silently drops it, the
	// wrote-it-but-it-never-took-effect trap the event surfaces.
	if (!canonicalCapabilityForm.MatchString(capability) ||
		(len(capability) >= 4 && strings.EqualFold(capability[:4], "CAP_"))) && pc.warn != nil {
		pc.warn(ReasonEngineRuntimeCapabilityFormHazard, fmt.Sprintf(
			"role %q adds capability %q (path %q, container %q) in a non-canonical form; "+
				"the canonical form is ^[A-Z]+(_[A-Z]+)*$ — containerd silently drops CAP_-prefixed "+
				"and unknown names, so the capability may never take effect",
			source.name, capability, rule.Path, pc.containerName))
	}
	if container.SecurityContext == nil {
		container.SecurityContext = &v1.SecurityContext{}
	}
	if container.SecurityContext.Capabilities == nil {
		container.SecurityContext.Capabilities = &v1.Capabilities{}
	}
	if hasCapability(container.SecurityContext.Capabilities.Add, capability) {
		// A capability already present is an idempotent no-op — verbatim
		// compare, no normalization: a capability name has no
		// secondary fields to diverge.
		return nil
	}
	list, err := insertElement(container.SecurityContext.Capabilities.Add, seg, v1.Capability(capability))
	if err != nil {
		return patchPathError(source, idx, rule.Path, err)
	}
	container.SecurityContext.Capabilities.Add = list
	return nil
}

// parsePatchPath validates the container-relative RFC 6901 pointer grammar
// against the patch vocabulary: an element pointer (index or "-") under one
// of the finite roots. It returns the target identifier and the element
// segment. Whole-list roots ("/command", "/volumeMounts") are rejected —
// RFC 6902 add onto an existing location replaces it, which would overwrite
// user values.
func parsePatchPath(path string) (target, seg string, err error) {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch {
	case len(segments) == 2 && segments[0] == patchRootCommand,
		len(segments) == 2 && segments[0] == patchRootArgs,
		len(segments) == 2 && segments[0] == patchRootEnv,
		len(segments) == 2 && segments[0] == patchRootVolumeMounts:
		target, seg = segments[0], segments[1]
	case len(segments) == 4 && segments[0] == "securityContext" &&
		segments[1] == patchRootCapabilities && segments[2] == patchOpAdd:
		target, seg = patchRootCapabilities, segments[3]
	default:
		return "", "", fmt.Errorf("path %q is not an element pointer under /command, /args, /env, /volumeMounts or /securityContext/capabilities/add", path)
	}
	if seg != "-" {
		for _, r := range seg {
			if r < '0' || r > '9' {
				return "", "", fmt.Errorf("path %q: element segment %q is neither an index nor \"-\"", path, seg)
			}
		}
	}
	return target, seg, nil
}

// insertElement performs the RFC 6902 add insertion for one list element:
// "-" appends (index len); an explicit index must not exceed the list
// length (RFC 6902: adding beyond the array end does not extend it).
func insertElement[T any](list []T, seg string, value T) ([]T, error) {
	index, err := elementIndex(seg, len(list))
	if err != nil {
		return nil, err
	}
	list = append(list, value)
	copy(list[index+1:], list[index:])
	list[index] = value
	return list, nil
}

func elementIndex(seg string, length int) (int, error) {
	if seg == "-" {
		return length, nil
	}
	// strconv rejects non-decimal input and indices beyond int range; a
	// manual digit accumulation would silently wrap negative, bypass the
	// length check below, and panic in insertElement's copy.
	index, err := strconv.Atoi(seg)
	if err != nil {
		return 0, fmt.Errorf("invalid index %q: %w", seg, err)
	}
	if index > length {
		return 0, fmt.Errorf("index %s exceeds the list length %d", seg, length)
	}
	return index, nil
}

// hasEnvName reports whether the env list carries the given name.
func hasEnvName(envs []v1.EnvVar, name string) bool {
	for _, env := range envs {
		if env.Name == name {
			return true
		}
	}
	return false
}

// hasEqualVolumeMount reports whether the container already carries a mount
// equal to mount under the Patch Grammar's full semantic equality: same
// (name, mountPath) key and every other field — readOnly, subPath,
// subPathExpr, mountPropagationMode, recursiveReadOnly — equal after an
// absent field and an explicit zero value are canonicalized to the same
// shape. Such a re-add is an idempotent no-op; a key match with any other
// field differing is a render error checkVolumeMount reports.
func hasEqualVolumeMount(mounts []v1.VolumeMount, mount v1.VolumeMount) bool {
	for _, existing := range mounts {
		if existing.Name == mount.Name && existing.MountPath == mount.MountPath &&
			volumeMountsEqual(existing, mount) {
			return true
		}
	}
	return false
}

// volumeMountsEqual compares two volumeMounts for full semantic equality.
// MountPropagationMode and RecursiveReadOnly are pointer fields: a nil
// pointer and a pointer to the zero value canonicalize to the same shape,
// so an absent field and an explicit zero compare equal (KEP-531 Patch
// Grammar).
func volumeMountsEqual(a, b v1.VolumeMount) bool {
	canon := func(m v1.VolumeMount) v1.VolumeMount {
		if m.MountPropagation == nil {
			zero := v1.MountPropagationMode("")
			m.MountPropagation = &zero
		}
		if m.RecursiveReadOnly == nil {
			zero := v1.RecursiveReadOnlyMode("")
			m.RecursiveReadOnly = &zero
		}
		return m
	}
	return reflect.DeepEqual(canon(a), canon(b))
}

// hasCapability reports verbatim whether the capability name is already
// present — no normalization, no case folding: the renderer is neither
// stricter nor more magical than the target surface (KEP-531 Patch
// Grammar).
func hasCapability(add []v1.Capability, capability string) bool {
	for _, existing := range add {
		if string(existing) == capability {
			return true
		}
	}
	return false
}

// checkVolumeMount enforces the volumeMount merge invariants: every mount is
// validated against the volumes visible at patch time (the rendered spec
// volumes already include the position's fragment volumes, applied by the
// fragment pass before this one), a dangling reference fails fast, a
// mountPath already used by a different volume name is a render error —
// the kubelet resolves overlapping mount paths last-wins, a shadowing the
// merge refuses to create — and a (name, mountPath) key match whose other
// fields differ is a render error attributed to the patch author's object:
// an add op cannot modify the existing entry, so the only renderable
// output would be a duplicate mount entry, and core validation rejects
// duplicate mount paths at pod creation far from the cause. The same
// volume name at a different mountPath — a legitimate second mount of one
// volume — passes.
func checkVolumeMount(
	container *v1.Container, mount v1.VolumeMount,
	volumeNames map[string]struct{},
	source patchSource, containerName string, idx int, path string,
) error {
	if _, ok := volumeNames[mount.Name]; !ok {
		return fmt.Errorf(
			"%s[%d] path %q references volume %q (container %q), which is declared by "+
				"neither the rendered spec nor the profile",
			source.describe(), idx, path, mount.Name, containerName,
		)
	}
	for _, existing := range container.VolumeMounts {
		if existing.MountPath == mount.MountPath && existing.Name != mount.Name {
			return fmt.Errorf(
				"%s[%d] path %q mounts volume %q at path %q (container %q), already used by "+
					"a different volume; the kubelet resolves overlapping mount paths last-wins",
				source.describe(), idx, path, mount.Name, mount.MountPath, containerName,
			)
		}
		if existing.Name == mount.Name && existing.MountPath == mount.MountPath &&
			!volumeMountsEqual(existing, mount) {
			return fmt.Errorf(
				"%s[%d] path %q re-adds volumeMount (name %q, mountPath %q, container %q) with differing "+
					"fields; an add op cannot modify the existing entry and core validation rejects a "+
					"duplicate mount path — a re-add must be a full semantic match to be an idempotent no-op",
				source.describe(), idx, path, mount.Name, mount.MountPath, containerName,
			)
		}
	}
	return nil
}

// decodePatchString unmarshals a scalar patch value. RawExtension.Raw holds
// the submitted JSON bytes verbatim (preserve-unknown-fields), so the decode
// sees exactly what the author wrote.
func decodePatchString(raw []byte) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("value must be a JSON string: %w", err)
	}
	return value, nil
}

// decodePatchStruct unmarshals an object patch value strictly: a mistyped
// field name fails here, attributed to the patch author, instead of silently
// producing a half-initialized struct.
func decodePatchStruct(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("value must match the target struct: %w", err)
	}
	return nil
}

func patchPathError(source patchSource, idx int, path string, err error) error {
	return fmt.Errorf("%s[%d] path %q: %w", source.describe(), idx, path, err)
}

func findRenderedContainer(podSpec *v1.PodTemplateSpec, name string) *v1.Container {
	for i := range podSpec.Spec.Containers {
		if podSpec.Spec.Containers[i].Name == name {
			return &podSpec.Spec.Containers[i]
		}
	}
	return nil
}

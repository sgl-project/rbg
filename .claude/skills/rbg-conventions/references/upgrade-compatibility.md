# Upgrade compatibility: keep existing workloads still

By default, upgrading the controller (and its CRDs) must be a no-op for every object that does not
use a new feature: no new ControllerRevision, no recreated or restarted Pod, no rewritten Service
selector. Inference roles are expensive to restart (model reload, GPU rescheduling), so a change
that rolls every existing role on upgrade is a release blocker even if every Pod comes back healthy.

A deliberate migration is allowed only if all of the following hold. Any churn that isn't recorded
this way is a blocker.

- maintainers explicitly agreed to it
- it is scoped to one release hop and happens once
- it is documented in the upgrade notes
- it is recorded in `test/e2e/upgrade/profile.go`, as the `sharedServiceSelection` narrowing and
  the legacy strategy healing are today

## How revisions are computed on `main`

| Level | Built by | Hashed input | Effect of a new hash |
|---|---|---|---|
| RoleBasedGroup | `NewRevision` → `getRBGPatch` (`pkg/utils/revision_utils.go`) | typed `spec.roles` + `spec.roleTemplates`, replicas stripped | new RBG ControllerRevision |
| Role | `GetRolesRevisionHash` (same file) | fnv32a of each role's JSON plus the referenced roleTemplate's `template` | rewrites the `rbg.workloads.x-k8s.io/role-revision-<role>` label on the workload's metadata (not on the pod template). On the deprecated StatefulSet path, a hash change alone is treated as the start of a rolling update (partition reset, may add `maxSurge` replicas) even when the pod template is unchanged |
| RoleInstanceSet | `getPatch` (`roleinstanceset/statelessmode/revision/revision.go`, `statefulmode/stateful_instance_set_helper.go`) | `spec.roleInstanceTemplate` only | new update revision; RoleInstances roll to it according to the update strategy and partition |
| RoleInstance | `buildPatch` (`pkg/reconciler/roleinstance/revision/instance_revision.go`) | `spec.components` minus `size` | Pods recreated or updated in place |
| Deprecated Deployment / StatefulSet / LWS | native controllers | pod template | native rollout |

The RoleInstanceSet template is rendered from the role on every reconcile by
`constructRoleInstanceSetApplyConfiguration` (`pkg/reconciler/roleinstanceset_reconciler.go`). It is
assembled fresh by each controller release, so a release that renders different bytes for an
unchanged role rolls that role.

Only `spec.roleInstanceTemplate` enters the RoleInstanceSet revision. The RoleInstanceSet's own
labels, annotations, replicas and update strategy don't. Everything injected into the nested pod
templates does (`pkg/reconciler/pod_reconciler.go`):

- discovery config and sidecars
- common and leader/worker env vars
- exclusive topology
- gang-scheduling labels and annotations

### The serializer-drift guards are not a safety net

`SetMatchesRevision` (RBG, RoleInstanceSet and RoleInstance levels) re-applies the stored revision and
re-serializes it, so pure serialization drift of identical content (for example
`creationTimestamp: null` vs omitted after a client-go bump) does not cut a new revision. It does
**not** help with:

- **Value changes**: a CRD default, a newly rendered field, or a changed controller default is real
  content, so a new revision is created.
- **Deferred churn**: drift it absorbed resurfaces later. The next legitimate revision is cut from the
  new serialization, so every role whose stored serialization differs gets a new hash and a
  relabeled workload, even roles the user didn't edit. Drift that affects every template, such as
  the `creationTimestamp` case, hits every role. This was verified with
  `testutils.WithLegacyCreationTimestamp`: editing one of three roles changed all three role hashes.

## How an upgrade disturbs running workloads

The package doc of `test/e2e/upgrade/preflight.go` lists four causes in this codebase. Reviews added two
more.

1. **Hash inputs change.** The controller renders a different template for an unchanged role. For
   example, the RoleInstanceSet template gained `restartPolicyConfig` in v0.8.0 next to the old
   `restartPolicy` string. The controller writes the new field only when a role sets backoff delays;
   see the worked example below.
2. **A new CRD default.** The API server applies structural defaults
   [when reading from etcd](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/#defaulting)
   to any field whose parent object is present. The controller then sees a changed spec that nobody
   wrote. The default is not persisted until the object is next written.
3. **Conversion changes shape.** The v1alpha1 conversion webhook writes a different v1alpha2 shape
   than the previous release did. This hits every object created or updated through v1alpha1.
4. **A controller-side default changes.** How an *unset* field is interpreted changes. Example: v0.7.0
   read an unset `sharedServiceSelection` as `All`, while later releases resolve it to `LeaderOnly`
   and patch the shared Service selector in place. No Pod restarts, but traffic does change.
5. **Templates get stamped.** The controller adds labels, annotations or env vars to existing pod or
   RoleInstanceSet templates. Stamp Pods at creation time instead, never the template (#530, KEP-284).
6. **Rendering reads a live referenced object.** A profile or policy that is neither watched nor part
   of the hash is re-rendered on a later reconcile or controller restart, so every referencing RBG
   rolls at an unpredictable time (#548, ClusterEngineRuntimeProfile).

## What moves a hash

| Change | Result | Do instead |
|---|---|---|
| New optional `*T` field with `json:"x,omitempty"`, left unset | No change | This is the pattern |
| New field without `omitempty`, or a non-pointer struct (encodes as `{}` even with `omitempty`), under an already-stored parent | Drift. Absorbed at upgrade, then new hashes for every affected role at the next revision | Pointer + `omitempty` |
| `+kubebuilder:default` on a new field whose parent exists in stored objects | Changes every such object, on read. Defaults nested under an absent optional parent are not materialized | No schema default; default in a Go getter |
| Controller renders new content into `spec.roleInstanceTemplate` (fields, labels, annotations, env, sidecars, injected scheduling fields) for every role | New RoleInstanceSet revision; instances roll | Render it only when the user set it |
| Re-ordering rendered list items (env, containers, volumes) | Rolls Pods | Keep render order stable; de-duplicate deterministically (#548) |
| Changing how an unset field is interpreted | Rewrites derived objects | Keep the old interpretation for existing objects; make the new one opt-in |
| Renaming a JSON key or changing a field's type | Changes every object | Add a new field and deprecate the old one, keeping its wire shape |
| Re-ordering Go struct fields | No change (revision input is re-marshaled from maps with sorted keys) | n/a |

## Safe pattern for a new knob: the `restartPolicyConfig` example

1. **API shape.** The new parent is a pointer: `RestartPolicyConfig *RestartPolicyConfig` with
   `json:"restartPolicyConfig,omitempty"` and `+optional`. The deprecated `RestartPolicy` string stays
   "for wire compatibility with v0.7.0" behind a `Deprecated:` comment. Fields *inside* the new
   struct can be value types and carry `+kubebuilder:default` markers, because those defaults only
   apply once a user sets the parent.
2. **Defaults in Go, not in storage.** `GetRestartPolicyConfig`, `GetBaseDelaySeconds` and
   `GetMaxDelaySeconds` (`api/workloads/v1alpha2/helper.go`) fold in the deprecated string and default
   missing values at the point of use.
3. **Raw getters.** `GetRawRestartPolicyType` and `GetRawRestartBackoff` return only what the user set,
   so callers can tell "configured" from "inherits the default".
4. **Render only when set.** `constructRoleInstanceSetApplyConfiguration` writes `restartPolicyConfig`
   into the RoleInstanceSet template only when the role configures backoff delays. Otherwise it keeps
   writing the deprecated string. Writing it unconditionally "would rewrite the stored template of
   every role a v0.7.0 install created, moving the RoleInstanceSet revision hash and rolling the role
   on upgrade" (comment in that function).
5. **Conversion, where it is only partly done.** Converting v1alpha2 to v1alpha1
   (`api/workloads/v1alpha1/rolebasedgroup_conversion.go`) uses `GetRawRestartPolicyType`, so
   defaults are not materialized. The other direction, v1alpha1 to v1alpha2, still materializes
   `restartPolicyConfig` for leader/worker and custom-components roles, and the API server then
   defaults the delays inside it.
   - The upgrade suite tolerates this only because its v1alpha1 round-trip fixtures are reconciled
     into LeaderWorkerSets (`foldRestartPolicyShape` in `snapshot.go`).
   - On the RoleInstanceSet path the same flip would change the rendered template and roll the
     role.
   - For any new field, test the full v1alpha2 → v1alpha1 → v1alpha2 round trip.
6. **Guard with a test.** `test/e2e/upgrade/specs.go` asserts that the deprecated string was not
   rewritten and that `restartPolicyConfig` is still nil on objects that never had it.

## Related compatibility rules

- **Opt-in by default.** With a new field or flag unset, behavior must match the previous release.
  Tighten in stages, warn first and deny later. Default-on host ports could leave Pods Pending (#532).
  Cache scoping stays opt-in in alpha (#530). Flipping a chart default needs an upgrade guard
  (#524, #552).
- **Referenced content is pinned.** Anything read from another object that affects rendering, such as
  profiles or policies, must be one of:
  - snapshotted into the revision,
  - folded into the role hash, or
  - immutable while referenced.

  Never re-render it silently from the live object (#548, #473). StorageClass is the Kubernetes
  analogue: editing it does not touch bound PVCs.
- **Canonical bytes before release.** Anything hashed or stored immutably (ControllerRevision `data`)
  must be canonical JSON: unmarshal to `map[string]any`, then marshal again. Fixing the bytes after a
  release renames every revision, which is a mass rollout (#474/#491).
- **Version skew works in both directions.**
  - A new controller on old CRDs silently drops new status fields (#508).
  - After a downgrade, the old controller's whole-object `Status().Update` wipes new fields (#491).
  - An old controller ignores new spec fields; for example it injects into the leader (#548).
  - An old image returns 404 for a new webhook path (#552).
  - The manager must start even when an optional CRD is absent (#478).
  - Write all of this in the KEP's *Upgrade / Downgrade Strategy* section.
- **Pre-existing objects.** State what the first reconcile after the upgrade does to objects created
  earlier, and put it in the release note or squash message. For example, a timeout measured from
  `creationTimestamp` fails already-running objects immediately (#478).
- **Deprecated surfaces.** v1alpha1 and Deployment/StatefulSet/LWS roles get fixes only, no new
  features. Upgrades must still leave existing deprecated objects untouched; they are gated by
  `--enable-deprecated-workload-types` (#524).

## How to verify

1. **Unit test.** Build fixtures in the previous release's stored shape and compare
   `NewRevision` / `GetRolesRevisionHash` and the rendered RoleInstanceSet template between `main`
   and your branch. They must be byte-identical for objects that don't use the feature.
2. **envtest.** Install the new CRDs over objects created in the old shape, then read them back. This
   is how CRD defaults applied on read and conversion changes show up.
3. **Binary swap on a cluster.** Run `main` with one RBG per pattern (standalone, leader/worker, custom
   components) and per deprecated workload type, then switch the controller image to your build. Pod
   UIDs, restart counts, owner generations and ControllerRevision names must all stay the same.
4. **`make test-e2e-upgrade`.** This standalone suite needs a cluster with no rbgs install; it
   installs the from-release itself and deletes the CRDs on teardown.
   - `RBGS_FROM_GIT_TAG` picks the hop: `v0.7.0` (default) or `v0.8.0`.
   - By default it upgrades to the chart's *published* images. To test your build:
     - Set `RBGS_TO_REPO` and `RBGS_TO_TAG` for the controller.
     - If the CRDs or the CRD upgrader changed, also set `RBGS_CRD_UPGRADE_REPO`. It does not fall
       back to `RBGS_TO_REPO`. Set `RBGS_CRD_UPGRADE_TAG` too if its tag differs; otherwise it falls
       back to `RBGS_TO_TAG`.
   - `PodFacts` in `snapshot.go` lists what must survive the upgrade.
   - An intentional rewrite is recorded per hop in `profile.go`. Never loosen a detector to make a
     finding go away.
5. **`/release-test`.** A maintainer comment runs the same suite in CI against the chart's default
   images. It validates a release, not unreleased controller code.
6. **Know what the suite does not prove** (see the package doc in `preflight.go`):
   - downgrade, or the old controller reading objects after the new CRDs land
   - releases older than v0.7.0, and multi-hop upgrades
   - storage-version migration
   - multiple nodes, leader-election hand-off, and real load
   - regressions that only show up on a later periodic resync
   - field combinations outside `fixtures.go`

   When your field changes rendering, add a fixture or assertion to `test/e2e/upgrade`.

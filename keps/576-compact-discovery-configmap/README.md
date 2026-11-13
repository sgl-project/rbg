# KEP-576: Compact Discovery ConfigMap

<!-- toc -->
- [Release Signoff Checklist](#release-signoff-checklist)
- [Summary](#summary)
- [Motivation](#motivation)
    - [Shipped Baseline](#shipped-baseline)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [User Stories (Optional)](#user-stories-optional)
        - [Story 1](#story-1)
        - [Story 2](#story-2)
    - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
    - [ConfigMap Injection and Mode](#configmap-injection-and-mode)
    - [Compact Format](#compact-format)
    - [Address Templates](#address-templates)
    - [Component Indexing](#component-indexing)
    - [Committed Membership](#committed-membership)
    - [Compatibility Rendering Layer](#compatibility-rendering-layer)
    - [Snapshot Contract](#snapshot-contract)
    - [Size Guardrail](#size-guardrail)
    - [Rollout, Migration, and Downgrade](#rollout-migration-and-downgrade)
    - [Phase 1 Scope](#phase-1-scope)
    - [Test Plan](#test-plan)
        - [Prerequisite testing updates](#prerequisite-testing-updates)
        - [Unit tests](#unit-tests)
        - [Integration tests](#integration-tests)
        - [e2e tests](#e2e-tests)
    - [Graduation Criteria](#graduation-criteria)
    - [Upgrade / Downgrade Strategy](#upgrade--downgrade-strategy)
    - [Version Skew Strategy](#version-skew-strategy)
- [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
    - [Feature Enablement and Rollback](#feature-enablement-and-rollback)
    - [Rollout, Upgrade and Rollback Planning](#rollout-upgrade-and-rollback-planning)
    - [Monitoring Requirements](#monitoring-requirements)
    - [Dependencies](#dependencies)
    - [Scalability](#scalability)
    - [Troubleshooting](#troubleshooting)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure Needed (Optional)](#infrastructure-needed-optional)
<!-- /toc -->

## Release Signoff Checklist

Items marked as (R) are required prior to targeting a milestone / release.

- [ ] (R) Enhancement issue in release milestone, which links to this KEP
      (https://github.com/sgl-project/rbg/issues/576)
- [ ] (R) KEP approvers have approved the KEP status as `implementable`
- [ ] (R) Design details are appropriately documented
- [ ] (R) Test plan is in place, giving consideration to SIG Architecture and SIG Testing input
    - [ ] e2e tests for all Beta API operations
    - [ ] (R) Ensure GA e2e tests meet requirements for conformance tests
    - [ ] (R) Minimum two week window for GA e2e tests to prove flake free
- [ ] (R) Graduation criteria is in place
- [ ] (R) Production readiness review completed
- [ ] (R) Production readiness review approved
- [ ] "Implementation History" section is up-to-date for milestone
- [ ] User-facing documentation has been created
- [ ] Supporting documentation—e.g., additional design documents, links to mailing
      list discussions, relevant PRs/issues, release notes—has been created

## Summary

This KEP proposes a **compact, declarative source format** for the RBG service
discovery ConfigMap, together with a **compatibility rendering layer** that
expands it inside the Pod into the address list consumers parse today.

The shipped `refine` mode (see [Shipped Baseline](#shipped-baseline)) stores a
full enumeration of every instance address in a single RBG-level ConfigMap.
Measured at roughly 43 bytes per pod, a 4000-replica role with 5 pods per
instance crosses the 1 MiB ConfigMap limit, and every scale event rewrites the
whole object to every node that mounts it.

The compact source stores a **controller-computed address template plus
declarative parameters** per role/component, so its size is
`O(roles x components)`, independent of replica count. Stored content size
grows only with the skip-ordinal list during drains, which KEP-499 bounds.

Scope: the compact format targets **RoleInstanceSet** roles only. The
deprecated workload types (Deployment, StatefulSet, LeaderWorkerSet) are out
of scope: new uses are rejected at admission, and those populations stay on
`legacy`/`refine` behavior.

This KEP replaces KEP-133, whose compact-format proposal was never implemented
and whose text no longer describes what shipped.

## Motivation

### Shipped Baseline

This is the state on `main` today, which any new design must model accurately:

1. **Two discovery modes exist.** The
   `rolebasedgroup.workloads.x-k8s.io/discovery-config-mode` annotation pins
   each RBG to `legacy` or `refine` on first reconciliation
   (`internal/controller/workloads/rolebasedgroup_controller.go`,
   `ensureDiscoveryConfigMode`). New RBGs get `refine` since PR #134
   (released in v0.7.0/v0.8.0).
2. **`legacy`** RBGs (pre-#134) keep per-role ConfigMaps.
3. **`refine`** RBGs get a single RBG-level ConfigMap named after the RBG,
   rewritten on every reconcile (`reconcileRefinedDiscoveryConfigMap`), holding
   the **full enumeration**: one `address` entry per pod plus per-entry copies
   of the role's `servicePorts`.
4. **Addresses are relative**: `<workload>-<ordinal>...s-<rbg>-<role>` —
   no namespace, no `svc.cluster.local` suffix (`pkg/discovery/config_builder.go`,
   `buildInstances`). They resolve via the pod's DNS search path.
5. **Membership is the committed child target, not the user exclusion input**:
   `ConfigBuilder.memberOrdinals` publishes `RoleInstanceSet.CommittedOrdinals()`
   when it exists, which can include ordinals `>= replicas` (KEP-499 example:
   replicas 3, exclusions [1,3] → committed members [0,2,4]). Invalid
   exclusion input freezes the last committed target rather than dropping it.
6. **CustomComponentsPattern components are enumerated in spec declaration
   order**, and roles in spec order.
7. **The ConfigMap is mounted as a whole volume (no `subPath`) at `/etc/rbg`**,
   so kubelet's ConfigMap sync propagates content updates (scale-outs,
   KEP-499 drains) to running pods within the sync period.

### Size problem

Per serialized pod entry (relative address line plus the copied `ports` map),
the shipped format costs roughly 40-120 bytes depending on name length and
port count; with the example names used in this KEP, one entry is ~118 bytes.
A 4000-replica role with 5 pods per instance is therefore ~2.4 MB — beyond the
1 MiB ConfigMap hard limit. Below the hard limit the cost is still
proportional: every reconcile rewrites the whole object, and kubelet's
ConfigMap sync (default period ~1 minute) redistributes the full object to
every node with a mounting pod — a 1 MB object on 100 such nodes moves ~100
MB of data per rewrite.

The obvious fix — publishing naming conventions and letting clients derive
addresses — does not hold: the controller must compute the templates, because
workload and Service names are truncated at 63 characters, a compatibility
path keeps legacy Service names, CustomComponentsPattern components may
override their `ServiceName`, and three patterns use different pod-naming
schemes.

### Goals

1. Stored ConfigMap size `O(roles x components)`, independent of replica count
   (bounded growth from skip ordinals only).
2. File-format compatibility for consumers that read `/etc/rbg/config.yaml`
   once at startup: the rendering layer produces an address list that is
   **semantically equal (field-by-field, order-sensitive)** to today's,
   including relative addresses, spec ordering, and committed membership.
3. A truthful rollout model for the three real populations: pre-#134 `legacy`,
   post-#134 `refine`, and new RBGs.
4. An explicit, honest account of the one behavior change (live ConfigMap
   refresh) rather than a false equivalence claim.
5. Fail fast and visibly when the stored object would exceed its limit.
6. Scope the new format to the non-deprecated RoleInstanceSet workload type.

### Non-Goals

1. Reflecting runtime readiness or liveness. The discovery file is a
   startup-time declarative snapshot; runtime membership changes are the job
   of DNS/EndpointSlice.
2. Preserving live ConfigMap refresh for running pods (see
   [Snapshot Contract](#snapshot-contract); this is a disclosed behavior
   change, not an accident).
3. Traffic draining, data migration, or PVC cleanup (inherited KEP-499
   non-goals).
4. Dynamic port allocator ports. RoleScoped/PodScoped allocated ports remain
   delivered through Pod environment variables and annotations.
5. Supporting Deployment-type (stateless, random pod names) workloads.
6. In-place controller downgrade for `refine-compact` RBGs (see the downgrade
   matrix; recreate the RBG instead).
7. Supporting deprecated workload types (Deployment, StatefulSet,
   LeaderWorkerSet) in the compact format. New uses of those types are
   rejected at admission; existing populations stay on `legacy`/`refine`
   behavior, and opt-in migration for RBGs containing them is rejected.

## Proposal

Store a compact declarative description in a separate `<rbg>-source` ConfigMap
for `refine-compact` RBGs and render the address list inside the Pod:

```text
RBG controller
  └─ ConfigMap <rbg-name>-source      (compact source, format: compact)
       │ mounted read-only by init container
       ▼
Pod init container (RBG-owned renderer)
  └─ expand templates + committed membership
       └─ /etc/rbg/config.yaml       (address list, emptyDir volume)
             ▲
             └─ main containers mount as before (read at startup)
```

### User Stories (Optional)

#### Story 1

As a cluster operator, I run disaggregated inference RBGs with thousands of
instances per role. The discovery ConfigMap must not grow with replica count,
must stay far below the 1 MiB limit, and must remain a single small object
that kubelet can sync cheaply.

#### Story 2

As an inference engine developer, my engine parses `/etc/rbg/config.yaml`
once at startup. Controller upgrades must not change the file format my
engine sees, without requiring any engine change.

### Notes/Constraints/Caveats (Optional)

- The rendered file is a **startup snapshot**: the init container runs once,
  so later ConfigMap edits are not reflected until Pod recreation. This is a
  deliberate behavior change relative to the shipped baseline; see
  [Snapshot Contract](#snapshot-contract).
- Deployment-type workloads have non-deterministic pod names, so no discovery
  ConfigMap is created or mounted for them.
- Only stateful roles are included, matching the shipped behavior.
- The compact format covers the RoleInstanceSet template types only
  (Standalone, CustomComponents, LeaderWorker). This deliberately excludes
  the deprecated LWS-compatible naming path, whose builder output has not
  been verified against actual pod names.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Renderer output diverges from the shipped address list | e2e asserts field-by-field, order-sensitive equality (relative addresses, spec ordering, committed membership) against the current builder output; unit tests cover truncation, compatible names, and all patterns |
| Long RBG/role names hit 63-char truncation | `base` is computed by the controller, which owns truncation and compatibility logic; clients never derive names |
| Live refresh loss affects file-polling consumers | Disclosed as a behavior change with consumer guidance; not claimed as equivalent |
| Migration rollout stalls | The legacy `<rbg>` enumeration object is retained (stale but valid) until rollout completion; no reader ever sees a wrong-format file |
| Downgrade of `refine-compact` RBGs corrupts discovery | Downgrade matrix documents that in-place downgrade is unsupported; recreate the RBG |
| Renderer init container adds a moving part | Tiny deterministic binary, no network, no API access, fails fast on malformed input |

## Design Details

### ConfigMap Injection and Mode

The `discovery-config-mode` annotation gains a third value:

| Mode | Population | ConfigMap content | Delivery |
|---|---|---|---|
| `legacy` | pre-#134 RBGs | per-role ConfigMaps, full enumeration | direct mount (unchanged) |
| `refine` | post-#134 RBGs | ConfigMap `<rbg>`, full enumeration | direct mount (unchanged) |
| `refine-compact` | new RBGs (and opt-in migration) | ConfigMap `<rbg>-source`, compact source | rendering init container |

The compact source lives in a **separate object** named `<rbg>-source` (key
`source.yaml`). The controller reconciles it the same way it reconciles the
`refine` ConfigMap today (before roles, every reconcile). The legacy `<rbg>`
enumeration object of a migrated RBG is never rewritten; it is deleted only
after the migration rollout completes (see
[Rollout, Migration, and Downgrade](#rollout-migration-and-downgrade)).
`legacy` and `refine` RBGs are untouched.

Opt-in migration has one precondition: the RBG must not contain stateful
roles of a deprecated workload type; otherwise the controller rejects the
mode change with a Warning Event and keeps the RBG on `refine`.

### Compact Format

```yaml
format: compact
namespace: default
group: pd-inference

roles:                            # array, spec declaration order
- name: prefill                  # unique per RBG (CRD-validated)
  replicas: 500                   # committed replica count; see below
  skipOrdinals: [3, 17]           # committed skip ordinals; see below
  components:                     # array, spec declaration order
  - name: leader
    size: 1                       # pods of this component per instance
    base: "pd-inference-prefill-{role_index}-leader-{component_index}.s-pd-inference-prefill"
    ports:                        # declared servicePorts, component-level
      http: 8080
      grpc: 50000
  - name: worker
    size: 4
    base: "pd-inference-prefill-{role_index}-worker-{component_index}.s-pd-inference-prefill"
    ports:
      http: 8080
```

Field reference:

| Field | Type | Description |
|---|---|---|
| `format` | string | Must be `compact`; lets renderers and future revisions detect the schema |
| `namespace` / `group` | string | Namespace and RBG name |
| `roles` | array | Role entries in **spec declaration order** (rendering preserves the shipped enumeration order) |
| `roles[].name` | string | Role name |
| `roles[].replicas` | int | Committed replica count (see [Committed Membership](#committed-membership)) |
| `roles[].skipOrdinals` | int[] | Optional, sorted, deduplicated committed skip ordinals |
| `roles[].components` | array | Component entries in spec declaration order |
| `roles[].components[].name` | string | Component name |
| `roles[].components[].size` | int | Pod count of the component within one instance |
| `roles[].components[].offset` | int | Optional, default `0`; first `component_index` of the component |
| `roles[].components[].base` | string | Relative address template containing `{role_index}` and/or `{component_index}` |
| `roles[].components[].ports` | map | Statically declared `servicePorts`; currently identical across components of a role |

### Address Templates

`base` is the **relative address** (`<pod>.<service>`, no namespace, no
cluster domain) with placeholders, exactly matching the shipped builder
output:

- `{role_index}`: zero-based instance ordinal within the role.
- `{component_index}`: pod ordinal within the role instance, computed as
  `component.offset + j` (see [Component Indexing](#component-indexing)).

Rules:

1. Substitution is strict string replacement
   (regex `\{(role_index|component_index)\}`); no expressions, no Python
   `format`, no Go templates. The controller pre-computes everything else
   (truncation, compatible Service names, component `ServiceName` overrides).
2. A placeholder not applicable to the pattern is simply absent.
3. An unknown or missing required placeholder is a controller bug; unit tests
   assert the exact placeholder set per pattern.

Pattern mapping:

| Pattern | base example (placeholders) |
|---|---|
| Standalone (no pattern) | `<rbg>-<role>-{role_index}.s-<rbg>-<role>` (single implicit component, size 1) |
| CustomComponentsPattern | `<rbg>-<role>-{role_index}-<component>-{component_index}.<svc>`, `offset: 0` |
| LeaderWorkerPattern (RoleInstanceSet) | `<rbg>-<role>-{role_index}-{component_index}.<svc>`; leader `offset: 0, size: 1`, worker `offset: 1, size: size-1` |

### Component Indexing

The renderer computes `component_index` for each pod `j` in
`[0, component.size)` of each committed `role_index`:

`component_index = component.offset + j`

This single rule covers all in-scope RoleInstanceSet template types:
Standalone (single implicit component, `offset: 0`, `size: 1`),
CustomComponentsPattern (each component `offset: 0`, base contains the
component name), and LeaderWorkerPattern (leader `offset: 0, size: 1`,
worker `offset: 1, size: size-1`). All three match the actual pod naming
(`FormatComponentPodName`).

The deprecated LWS-compatible naming (globally numbered pods) is intentionally
not modeled. RBG creates genuine vendor LeaderWorkerSet objects for that
path, whose pod names follow the vendor convention
(`<set>-<groupIndex>`, `<set>-<groupIndex>-<workerIndex>`), which the shipped
builder does not reproduce. Since that workload type is deprecated and its
population stays on `refine` enumeration, the compact format does not carry
the discrepancy forward.

### Committed Membership

The compact format does **not** project the user-facing
`rbg.workloads.x-k8s.io/excluded-ordinals` annotation. It encodes the
**committed child target**, which is what the shipped builder publishes:

- `replicas`: the committed replica count.
- `skipOrdinals`: the committed skip ordinals (KEP-499's
  `CommittedSkipOrdinals` semantics).

Rendering rule: emit the first `replicas` ordinals that are **not** in
`skipOrdinals`. This reproduces the shipped `memberOrdinals` behavior,
including members with ordinal `>= replicas` (replicas 3, skips [1,3] →
members [0,2,4]) and the freeze-on-invalid-input behavior (the controller
keeps publishing the last committed target and never plans new membership
from invalid input).

Source of truth, in order (mirroring `memberOrdinals`):

1. the RoleInstanceSet's committed target when it exists
   (`CommittedOrdinals()` / committed skip-ordinal annotation);
2. the same initial dense planner the controller uses before child creation
   (dense `[0, replicas)` when no exclusion input is present);
3. invalid input freezes the last published values.

This also answers which replica value is written when coordination scaling or
scaling adapters move effective membership away from `spec.replicas`: the
committed target, not the spec field.

### Compatibility Rendering Layer

1. The injector mounts the source ConfigMap `<rbg>-source` (key
   `source.yaml`) read-only as volume `rbg-discovery-source` at
   `/etc/rbg/source`, and an `emptyDir` volume `rbg-discovery-rendered`
   (with `sizeLimit`, see below) at `/etc/rbg`.
2. An RBG-owned init container reads `/etc/rbg/source/source.yaml`, computes
   committed membership, expands templates, and writes the address list to
   `/etc/rbg/config.yaml`, preserving role/component declaration order.
3. Main containers mount `/etc/rbg/config.yaml` as today.
4. The renderer is deterministic, performs no network or API-server access,
   and fails fast (non-zero exit) on malformed input — including a `format`
   value it does not understand — so Pods surface the error instead of
   silently consuming an incomplete topology.

New consumers may skip the rendering layer and parse the compact source
directly.

### Snapshot Contract

This is a **behavior change relative to the shipped baseline** and is stated
as such:

- Shipped: the ConfigMap is mounted whole (no `subPath`), so kubelet sync
  propagates discovery updates to **running** pods; a consumer re-reading
  `/etc/rbg/config.yaml` observes scale-outs and KEP-499 drains.
- Compact: the renderer writes the file once at Pod startup; running pods do
  not observe later ConfigMap edits until recreation.

Affected consumers: any workload that polls the discovery file at runtime.
Guidance: such consumers should treat the file as a startup snapshot and use
DNS/EndpointSlice for runtime membership, or accept Pod recreation on
topology change. The e2e and documentation work must call this out; the
acceptance criterion is file-format compatibility for startup readers, not
behavioral equivalence for pollers.

### Size Guardrail

The guardrail applies to the **serialized compact ConfigMap object**
(the thing that lives in etcd and has the 1 MiB limit), including the
`skipOrdinals` array:

- Threshold: 512 KiB (headroom below the 1 MiB limit).
- On breach with an existing object: keep the last good content and emit a
  Warning Event.
- On breach at first creation: do not create the object, emit a Warning
  Event; Pods then fail visibly on the missing volume rather than silently
  starting with incomplete discovery.

The rendered output lives in a Pod-local `emptyDir` and is not subject to the
etcd limit; the rendered volume declares an `emptyDir` `sizeLimit` (default
4 MiB), and the renderer pre-estimates its output size and fails fast before
writing when the estimate exceeds the limit. Skip ordinals grow only during
drains and are bounded by KEP-499 membership (a full 5000-member drain is
~25 KB).

### Rollout, Migration, and Downgrade

| Population | Mode | Behavior under the new controller |
|---|---|---|
| pre-#134 | `legacy` | Untouched: per-role ConfigMaps, direct mount |
| post-#134 | `refine` | Untouched by default: enumeration content, direct mount. **Opt-in migration** via the sequence below |
| new | `refine-compact` (default) | `<rbg>-source` ConfigMap + renderer from creation |

Opt-in migration sequence — designed so that **no running pod ever reads a
wrong-format file**:

1. The operator edits the mode annotation to `refine-compact`.
2. The controller creates/updates the `<rbg>-source` object and injects the
   renderer into the pod templates, which triggers a normal rollout. The
   legacy `<rbg>` enumeration object is left byte-for-byte untouched, so
   running pods (direct mount, live kubelet sync) keep reading a valid
   enumeration until they are replaced; at worst it is stale, which is the
   shipped behavior anyway. Crash-restarting containers on old pods re-read
   the same valid enumeration.
3. Once the rollout completes and no pod mounts the legacy volume, the
   controller deletes the `<rbg>` enumeration object. A stalled rollout
   merely retains the stale object; it never breaks a reader.

Downgrade matrix (to a v0.7/v0.8 controller):

| Mode at downgrade | Outcome |
|---|---|
| `legacy` | Supported (native mode of old controller) |
| `refine` | Supported (native mode of old controller) |
| `refine-compact` | **Unsupported, recreate the RBG.** The old controller re-pins the unknown mode annotation: to `legacy` when the RBG has status (the common case), which touches neither the `<rbg>` nor the `<rbg>-source` object, or to `refine` when the RBG has no status yet, in which case it (re)creates the `<rbg>` enumeration object. Pods the old controller recreates then either direct-mount a missing `<rbg>` object (post-migration or new RBGs) and fail visibly on the volume, or mount a valid enumeration (status-less RBGs) and keep working while the RBG stays pinned to `refine`. No path silently serves the compact source in the wrong format. |

In-place downgrade for `refine-compact` RBGs is deliberately out of scope for
phase 1 (a dual-write scheme is a possible future extension and is recorded
under Alternatives).

### Phase 1 Scope

In scope:

- `refine-compact` mode with the compact format, committed membership, and
  the rendering init container.
- Opt-in migration for `refine` RBGs.
- Stored-object size guardrail.
- The snapshot-contract disclosure.

Out of scope (new KEP required):

- live refresh of the rendered file;
- in-place downgrade support;
- dynamic (port-allocator) ports in the ConfigMap.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

##### Prerequisite testing updates

None.

##### Unit tests

- `pkg/discovery`: compact builder produces the exact field set; roles and
  components preserve spec declaration order across reorderings.
- Template correctness per pattern: Standalone, CustomComponents (including
  component `ServiceName` overrides), LeaderWorkerPattern (RoleInstanceSet),
  including 63-character truncation and legacy compatible Service names;
  relative (not FQDN) address output.
- Committed membership: dense fallback, committed-target source, skips
  producing members `>= replicas`, freeze-on-invalid-input.
- Renderer: placeholder substitution, offset arithmetic, fail-fast on
  malformed input, order-preserving output equal to the current enumeration.
  Fail-fast on an unknown `format` value (renderer/controller skew).
- Size guardrail: threshold behavior on update and first create.

##### Integration tests

- Mode gating: new RBGs get `refine-compact`; `legacy` and `refine` RBGs are
  not rewritten.
- Opt-in migration: `refine` → `refine-compact` annotation edit creates the
  `<rbg>-source` object and injects the renderer on the next rollout; the
  legacy `<rbg>` object stays untouched until rollout completion, then is
  deleted.
- Downgrade drill: a `refine-compact` RBG under an old controller is not
  silently corrupted without operator action; document the re-pin behavior.

##### e2e tests

- Extend `test/e2e/testcase/v1alpha2/discovery_configmap.go`: the rendered
  file is **field-by-field and order-sensitive** equal to the enumeration the
  current builder produces for multi-pod patterns and StandalonePattern,
  including relative address strings.
- Committed-membership scenarios: KEP-499 drain keeps committed members
  (including ordinals `>= replicas`) in the rendered file; invalid exclusion
  input freezes the last published target.
- Renderer failure surfacing on malformed input.
- Migration path: v0.8-shaped `refine` RBG → new controller stays on
  enumeration until opt-in; during and after opt-in, every pod (old and new)
  always reads either the legacy enumeration or a valid rendered file — no
  window serves the wrong format; the legacy object is deleted only after
  rollout completion.

### Graduation Criteria

#### Alpha

- Mode gating, compact format, renderer, and guardrail implemented; new RBGs
  default to `refine-compact`.
- Unit, integration, and e2e coverage as above.
- A large-scale (2000+ instance) scale test showing the stored ConfigMap
  stays well under the guardrail and reconciliation does not fail.

#### Beta

- Renderer image published and versioned; docs for direct compact-source
  consumption and the snapshot contract.
- No reports of rendered-output divergence.

#### GA

- Two releases of stable behavior; `legacy` and `refine` populations still
  untouched.

#### Deprecation

- N/A. No user-facing API is removed; `legacy` and `refine` modes remain
  supported.

### Upgrade / Downgrade Strategy

See [Rollout, Migration, and Downgrade](#rollout-migration-and-downgrade).
Upgrades only change new RBGs by default; existing populations are untouched
unless an operator opts in. In-place downgrade of `refine-compact` RBGs is
unsupported: recreate the RBG.

### Version Skew Strategy

The `format: compact` marker lets any renderer detect the schema. The compact
source lives in the separate `<rbg>-source` object, which older controllers
never read or write (they only manage `legacy` per-role ConfigMaps and the
`refine` `<rbg>` object).

Renderer image versioning: the image reference is a controller-level setting
(e.g. a `--discovery-renderer-image` flag, defaulting to the image released
with the controller version). The controller only emits schema versions its
bundled renderer supports; a renderer that meets an unknown `format` value
fails fast, so the mismatch surfaces as a Pod stuck in Init with an explicit
log line rather than silent wrong behavior. A controller/renderer version
skew is operator-visible, never silent.

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

###### How can this feature be enabled / disabled in a live cluster?

Enabled automatically for newly created RBGs after the controller upgrade;
existing RBGs keep their pinned mode. Opt-in migration for `refine` RBGs is
an annotation edit.

###### Does enabling the feature change any default behavior?

Only for new RBGs (and opted-in ones): compact source plus the rendering init
container. Running pods of other populations are unaffected.

###### Can the feature be disabled once it has been enabled (i.e. can we roll back the enablement)?

Roll back the controller image. In-place downgrade for `refine-compact` RBGs
is unsupported; recreate them (documented in the downgrade matrix).

###### What happens if we reenable the feature if it was previously rolled back?

The controller rebuilds the compact source deterministically from spec and
the committed child target.

###### Are there any tests for feature enablement/disablement?

Yes: integration tests for mode gating, opt-in migration, and the downgrade
drill.

### Rollout, Upgrade and Rollback Planning

###### How can a rollout or rollback fail? Can it impact already running workloads?

A rollout failure is a reconcile failure on the ConfigMap (guarded by the size
guardrail) and surfaces as Warning Events. Already running workloads are not
impacted; only newly created Pods consume the rendered output.

###### What specific metrics should inform a rollback?

Elevated RBG controller reconcile error rates and guardrail-warning events.

###### Were upgrade and rollback tested? Was the upgrade->downgrade->upgrade path tested?

Covered by the integration downgrade drill and the migration-path e2e test.

###### Is the rollout accompanied by any deprecations and/or removals of features, APIs, fields of API types, flags, etc.?

No. `legacy` and `refine` modes remain supported.

### Monitoring Requirements

###### How can an operator determine if the feature is in use by workloads?

`kubectl get rbg -o yaml` shows `discovery-config-mode: refine-compact`; the
`<rbg>-source` ConfigMap carries `format: compact`.

###### How can someone using this feature know that it is working for their instance?

The rendered `/etc/rbg/config.yaml` exists and is non-empty; the init
container exits 0.

###### What are the reasonable SLOs (Service Level Objectives) for the enhancement?

No new SLOs; ConfigMap reconciliation is part of the existing RBG reconcile
loop.

###### What are the SLIs (Service Level Indicators) an operator can use to determine the health of the service?

RBG controller reconcile error rate and Warning Events
(`FailedReconcileDiscoveryConfigMap`, guardrail warnings).

###### Are there any missing metrics that would be useful to have to improve observability of this feature?

None identified at alpha; a counter for guardrail breaches can be added if
events prove insufficient.

### Dependencies

###### Does this feature depend on any specific services running in the cluster?

No. The renderer init container performs no network or API-server access.

### Scalability

###### Will enabling / using this feature result in any new API calls?

No additional calls beyond the existing ConfigMap reconciliation.

###### Will enabling / using this feature result in introducing new API types?

No.

###### Will enabling / using this feature result in any new calls to the cloud provider?

No.

###### Will enabling / using this feature result in increasing size or count of the existing API objects?

The opposite for new RBGs: the stored ConfigMap is `O(roles x components)`
instead of `O(instances)`, with bounded growth from committed skip ordinals.
Opt-in migration shrinks existing objects the same way.

###### Will enabling / using this feature result in increasing time taken by any operations covered by existing SLIs/SLOs?

No; building the compact source is cheaper than enumerating every pod.

###### Will enabling / using this feature result in non-negligible increase of resource usage (CPU, RAM, disk, IO, ...) in any components?

Pods gain a small init container and an emptyDir volume for the rendered
file, bounded by the volume's `sizeLimit` (default 4 MiB).

###### Can enabling / using this feature result in resource exhaustion of some node resources (PIDs, sockets, inodes, etc.)?

No; one extra init container process per Pod and a small file.

### Troubleshooting

###### How does this feature react if the API server and/or etcd is unavailable?

Reconciliation fails and retries with the existing backoff; running pods are
unaffected because the rendered file is local.

###### What are other known failure modes?

- Renderer fails on malformed input (controller bug):
  - Detection: init container non-zero exit; Pod stuck in Init.
  - Mitigation: no partial file is written; the error surfaces immediately.
  - Diagnostics: init container log shows the parse/substitution error.
  - Testing: renderer unit tests assert fail-fast behavior.
- Stored object would exceed the guardrail:
  - Detection: Warning Event; last-good content kept (or object not created
    on first reconcile).
  - Mitigation: reconciliation continues with previous content; new pods
    fail visibly on the missing volume.
  - Testing: guardrail unit tests.

###### What steps should be taken if SLOs are not being met to determine the problem?

Inspect RBG Warning Events and controller logs, then
`kubectl get cm <rbg> -o yaml` and the init container logs of an affected Pod.

## Implementation History

- 2026-11-13: initial KEP. Replaces KEP-133 (whose compact proposal was
  never implemented; #134 shipped the RBG-level enumeration ConfigMap).
  Incorporates the review findings from the PR #570 design round: accurate
  shipped baseline, relative address templates, committed-membership
  encoding, spec-order rendering, snapshot-contract disclosure, stored-object
  guardrail, three-population migration and downgrade matrix.
- 2026-11-13 (revision): design-review fixes — the compact source moved to a
  separate `<rbg>-source` object so opt-in migration never rewrites the
  legacy object under running pods (no wrong-format window; legacy object
  deleted only after rollout completion); unified the equality gate to
  semantic (field-by-field, order-sensitive) equality; corrected the
  downgrade mechanics; specified renderer image versioning and fail-fast on
  unknown `format`; bounded the rendered volume with `emptyDir` `sizeLimit`;
  and completed the size/distribution quantification.

## Drawbacks

- Adds a rendering init container to every Pod that consumes the discovery
  file.
- Running pods lose live discovery updates (a disclosed behavior change; see
  [Snapshot Contract](#snapshot-contract)).
- Two content forms (compact source, rendered list) coexist indefinitely.
- In-place controller downgrade for `refine-compact` RBGs is unsupported.

## Alternatives

1. **Rewrite KEP-133 in place**: rejected. It would overwrite the historical
   proposal record, and the shipped `refine` behavior (the migration source)
   has no accurate KEP description today; this KEP owns both the baseline
   description and the target design.
2. **Keep the shipped enumeration and only merge per-role ConfigMaps**:
   already shipped; does not address the `O(instances)` size problem.
3. **Client-side FQDN derivation from naming conventions**: rejected
   (63-char truncation, compatible Service names, component overrides, three
   naming patterns).
4. **Per-role ConfigMap sharding**: fails for huge single roles and keeps
   many objects.
5. **Runtime discovery (DNS SRV / EndpointSlice watch)**: answers
   readiness-style questions but not ordinal identity, and conflicts with
   RBG's static-injection philosophy; complement, not replacement.
6. **Dual-write (enumeration + compact) for in-place downgrade support**:
   deferred; doubles storage and reconcile cost to serve a rollback path
   that can instead be handled by RBG recreation.
7. **active/draining membership fields**: rejected. The file is static and
   declarative; committed membership (KEP-499) already expresses the drain
   window correctly, and readiness is EndpointSlice's job.
8. **In-place rewrite of the shared `<rbg>` ConfigMap for migration**:
   rejected — it would swap the file format under running pods that
   direct-mount the object (live kubelet sync), including containers that
   re-read the file after a crash restart, creating a breaking window for
   the entire rollout duration. The separate `<rbg>-source` object avoids
   the window entirely.

## Infrastructure Needed (Optional)

- Release pipeline support for publishing the renderer init container image
  alongside the controller image, and a controller flag for the renderer
  image reference (defaulting to the image matching the controller version).

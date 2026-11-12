# KEP-133: ConfigMap Refine for RoleBasedGroup (RBG)

<!-- toc -->
- [Release Signoff Checklist](#release-signoff-checklist)
- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [User Stories (Optional)](#user-stories-optional)
        - [Story 1](#story-1)
        - [Story 2](#story-2)
    - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
    - [ConfigMap Injection](#configmap-injection)
    - [Refine Format](#refine-format)
    - [Address Templates](#address-templates)
    - [Indexing Modes](#indexing-modes)
    - [Excluded Ordinals](#excluded-ordinals)
    - [Compatibility Rendering Layer](#compatibility-rendering-layer)
    - [Size Guardrail](#size-guardrail)
    - [Rollout and Compatibility](#rollout-and-compatibility)
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

<!--
**ACTION REQUIRED:** In order to merge code into a release, there must be an
issue referencing this KEP and targeting a release milestone **before** the
corresponding freeze of the targeted release.

Items marked as (R) are required *prior to targeting to a milestone / release*.

Check these off as they are completed. These checklist items _must_ be updated
for the enhancement to be released.
-->

- [ ] (R) Enhancement issue in release milestone, which links to this KEP
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

[kubernetes.io]: https://kubernetes.io/
[kubernetes/enhancements]: https://git.k8s.io/enhancements
[kubernetes/kubernetes]: https://git.k8s.io/kubernetes
[kubernetes/website]: https://git.k8s.io/website

## Summary

This KEP proposes the **refine mode** for the RBG service discovery ConfigMap
(`/etc/rbg/config.yaml`).

Today every role of an RBG gets its own ConfigMap whose content is a full
enumeration of every instance FQDN, and the same content is duplicated across
all roles of the group. The refine mode replaces this with:

1. a **single RBG-level ConfigMap** (named after the RBG) that contains a
   **compact, declarative description** of the group topology instead of the
   expanded address list, and
2. a **compatibility rendering layer** that expands the compact description
   into the full address list inside the Pod, so consumers keep seeing the file
   format they parse today.

The stored ConfigMap size becomes `O(roles x components)`, independent of
replica count, instead of `O(replicas x components x component size)`.

The refine mode is strictly **static and declarative**: the only state-like
field is `excludedOrdinals`, whose semantics are inherited verbatim from KEP-499
(stateful selective scale-down).

## Motivation

Today the controller creates one ConfigMap per role, while the discovery
content is identical for every role of the same RBG: one RBG with N roles
stores N copies of the same topology.

The content itself does not scale. It lists one entry per pod:

```yaml
group:
  name: pd-inference
  size: 4
  roles: [router, prefill, decode]
roles:
  prefill:
    size: 2500
    instances:
    - address: pd-inference-prefill-0-leader-0.s-pd-inference-prefill.default.svc.cluster.local
      ports: {http: 8080, grpc: 50000}
    - address: pd-inference-prefill-0-worker-0.s-pd-inference-prefill.default.svc.cluster.local
      ports: {http: 8080, grpc: 50000}
    # ... one entry per pod: replicas x components x component size
```

Each entry costs ~120-200 bytes, so a role with 500 instances x (1 leader + 4
workers) is already ~500 KB; with a 2000-instance sibling role the ConfigMap
exceeds the 1 MiB etcd/ConfigMap hard limit and reconciliation fails. Beyond
the hard limit, kubelet re-syncs the whole object to every node that mounts it,
and every scale event rewrites the whole object.

The content is also internally redundant: `ports` is role-level data copied
into every instance entry, and full FQDNs repeat a common pattern that differs
only in indices.

Finally, the obvious fix of publishing only naming conventions and letting
clients derive FQDNs does not hold, because the controller must compute address
templates: workload and Service names are truncated at 63 characters with
trailing-hyphen trimming (`GetWorkloadName`/`GetServiceName`), a compatibility
path keeps legacy Service names when they pre-exist
(`GetCompatibleHeadlessServiceName`), CustomComponentsPattern components may
override their `ServiceName`, and three patterns (Standalone, LeaderWorker,
CustomComponents) use different pod-naming schemes.

### Goals

1. One ConfigMap per RBG instead of one per role.
2. Stored content size `O(roles x components)`, independent of replica count.
3. Preserve the existing consumer contract: a file at `/etc/rbg/config.yaml`
   with the full address list, produced by a rendering layer.
4. Represent KEP-499 excluded ordinals in the discovery data.
5. Existing RBGs are never migrated automatically (`discovery-config-mode`
   annotation gate).
6. Fail fast and visibly when the rendered output would exceed the ConfigMap
   limit.

### Non-Goals

1. Reflecting runtime readiness or liveness. The ConfigMap is a startup-time
   declarative snapshot; runtime membership changes are the job of DNS/EndpointSlice.
2. Traffic draining, data migration, or PVC cleanup (inherited KEP-499 non-goals).
3. Dynamic port allocator ports. RoleScoped/PodScoped allocated ports remain
   delivered through Pod environment variables and annotations; the ConfigMap
   only carries statically declared `servicePorts`.
4. Speeding up ConfigMap propagation or update notification.
5. Supporting Deployment-type (stateless, random pod names) workloads. The
   controller neither creates nor mounts discovery ConfigMaps for those roles,
   same as today.

## Proposal

Store a compact declarative description in a single RBG-level ConfigMap and
render the full address list inside the Pod:

```text
RBG controller
  └─ ConfigMap <rbg-name>            (compact refine format, O(roles x components))
       │ mounted read-only by init container
       ▼
Pod init container (RBG-owned renderer)
  └─ expand templates + skip excluded ordinals
       └─ /etc/rbg/config.yaml       (full address list, emptyDir volume)
             ▲
             └─ main containers mount as before; zero consumer changes
```

### User Stories (Optional)

#### Story 1

As a cluster operator, I run disaggregated inference RBGs with thousands of
instances per role. The discovery ConfigMap must not grow with replica count,
must stay far below the 1 MiB limit, and must remain a single small object that
kubelet can sync cheaply.

#### Story 2

As an inference engine developer, my engine already parses `/etc/rbg/config.yaml`
as a full address list. RBG upgrades must not require any change to my engine,
while new engines may consume the compact source directly.

### Notes/Constraints/Caveats (Optional)

- The rendered file is a **startup snapshot**: the init container runs once, so
  later ConfigMap edits are not reflected until Pod recreation. This matches the
  effective contract today, where consumers reload at their own discretion.
  Runtime membership is DNS/EndpointSlice's job.
- Deployment-type workloads have non-deterministic pod names
  (`getOrGenInstanceID`), so no ConfigMap is created or mounted for them; the
  refine mode only covers patterns with deterministic names.
- Only stateful roles are included in the ConfigMap, matching the current
  behavior.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Renderer produces output that differs from today's address list | e2e asserts field-by-field equality against the current enumeration output; unit tests cover truncation, compatible names, and all patterns |
| Long RBG/role names hit 63-char truncation, breaking naive templates | `base` is always computed by the controller, which owns truncation and compatibility logic; clients never derive names |
| Excluded ordinals interpreted as "safe to drop traffic now" | Semantics copied verbatim from KEP-499: early-drain signal, not a guarantee; documented in the format reference |
| ConfigMap updates after startup are not visible in the rendered file | Documented: the rendered file is a startup snapshot; runtime membership is DNS/EndpointSlice |
| Renderer init container adds a moving part | Renderer is a tiny deterministic binary owned by RBG, no network, no API access, fails fast on malformed input |

## Design Details

### ConfigMap Injection

The controller creates and continuously reconciles a **single RBG-level
ConfigMap**, named after the RBG, in the RBG reconcile loop. It is reconciled
before roles, so the ConfigMap always exists before any workload that mounts
it is created.

The injector only mounts; it no longer creates per-role ConfigMaps. Only
stateful roles mount the discovery ConfigMap.

### Refine Format

```yaml
format: refine
namespace: default
group: pd-inference

roles:                              # array, sorted by name (stable output)
- name: prefill                    # unique per RBG (CRD-validated)
  replicas: 500                     # instance count; role_index in [0, replicas)
  excludedOrdinals: [3, 17]         # optional; see "Excluded Ordinals"
  components:                       # array, sorted by name
  - name: leader
    size: 1                         # pods of this component per instance
    base: "pd-inference-prefill-{role_index}-leader-{component_index}.s-pd-inference-prefill.default.svc.cluster.local"
    ports:                          # declared servicePorts, component-level
      http: 8080
      grpc: 50000
  - name: worker
    size: 4
    base: "pd-inference-prefill-{role_index}-worker-{component_index}.s-pd-inference-prefill.default.svc.cluster.local"
    ports:
      http: 8080
```

Field reference:

| Field | Type | Description |
|---|---|---|
| `format` | string | Must be `refine`; lets renderers and future revisions detect the schema |
| `namespace` / `group` | string | Namespace and RBG name |
| `roles` | array | Role entries, **sorted by `name`** so spec reordering never changes the serialized object |
| `roles[].name` | string | Role name |
| `roles[].replicas` | int | Declared instance count |
| `roles[].excludedOrdinals` | int[] | Optional, sorted, deduplicated, subset of `[0, replicas)` |
| `roles[].instanceSize` | int | Optional; required only with `indexing: grouped` (see below) |
| `roles[].components[].name` | string | Component name |
| `roles[].components[].size` | int | Pod count of the component within one instance |
| `roles[].components[].offset` | int | Optional, default `0`; first `component_index` of the component |
| `roles[].components[].indexing` | enum | `perComponent` (default) or `grouped`; see "Indexing Modes" |
| `roles[].components[].base` | string | Full address template containing `{role_index}` and/or `{component_index}` |
| `roles[].components[].ports` | map | Statically declared `servicePorts`; currently identical across components of a role, kept at component level to allow future divergence |

### Address Templates

`base` is the complete address (pod name + service DNS suffix) with placeholders:

- `{role_index}`: zero-based instance ordinal within the role.
- `{component_index}`: pod ordinal, interpreted per the component's indexing mode.

Rules:

1. Substitution is strict string replacement (regex `\{(role_index|component_index)\}`),
   not Python `format` or Go templates. No expressions are allowed in templates;
   the controller pre-computes everything else (truncation, compatible Service
   names, component `ServiceName` overrides, namespace suffix).
2. A placeholder that is not applicable to the pattern (e.g. Standalone roles have
   only `{role_index}`) is simply absent.
3. An unknown placeholder or a missing required placeholder is a controller bug;
   unit tests assert the exact placeholder set per pattern.

Pattern mapping:

| Pattern | base example (placeholders) |
|---|---|
| Standalone / StatefulSet | `<rbg>-<role>-{role_index}.s-<rbg>-<role>.<ns>.svc.cluster.local` (single implicit component, size 1) |
| CustomComponentsPattern | `<rbg>-<role>-{role_index}-<component>-{component_index}.<svc>.<ns>.svc.cluster.local`, `offset: 0`, `indexing: perComponent` |
| LeaderWorkerPattern (RoleInstanceSet) | `<rbg>-<role>-{role_index}-{component_index}.<svc>.<ns>.svc.cluster.local`; leader `offset: 0, size: 1`, worker `offset: 1, size: size-1` |
| LeaderWorkerPattern (LWS-compatible workload type) | `<rbg>-<role>-{component_index}.<svc>.<ns>.svc.cluster.local`, `indexing: grouped`, `instanceSize: <size>` |

### Indexing Modes

The renderer computes `component_index` as follows for each pod `j` in
`[0, component.size)` of each non-excluded `role_index`:

- `perComponent` (default): `component_index = component.offset + j`.
- `grouped`: `component_index = role_index * instanceSize + component.offset + j`.
  Used only by the LWS-compatible workload type, whose pod names are globally
  numbered within the role and do not contain the component name. The
  multiplication is performed by the RBG-owned renderer; it is never embedded in
  the template string.

### Excluded Ordinals

`excludedOrdinals` is projected from the KEP-499 role annotation
`rbg.workloads.x-k8s.io/excluded-ordinals` (which accepts the compact form
`"1,3,8-10"`). Semantics are inherited from KEP-499 unchanged:

- Ordinals listed here are not declared members: the renderer skips them, so no
  address is emitted for them.
- The signal is an **early-drain hint, not a guarantee** that consumers have
  stopped using the address. Consumers own the transition window.

Controller-side validation:

1. Entries must be within `[0, replicas)`; out-of-range entries are dropped and
   reported via a Warning Event on the RBG.
2. The rendered array is sorted and deduplicated so SSA semantic comparison does
   not produce spurious diffs.

Deliberately there is **no** active/draining split: the ConfigMap is static and
declarative; distinguishing "draining" from "not yet created" is EndpointSlice's
job, not the ConfigMap's.

### Compatibility Rendering Layer

1. The injector mounts the refine ConfigMap read-only (e.g. at
   `/etc/rbg/source`).
2. An RBG-owned init container reads the source, expands templates, skips
   excluded ordinals, and writes the full address list to an `emptyDir` volume
   at `/etc/rbg/config.yaml`.
3. Main containers mount `/etc/rbg/config.yaml` exactly as today; **zero changes
   for existing consumers**.
4. The renderer is deterministic, performs no network or API-server access, and
   fails fast (non-zero exit) on malformed input so Pods surface the error
   immediately instead of silently consuming an incomplete topology.

New consumers may skip the rendering layer and parse the compact source
directly.

### Size Guardrail

The controller estimates the fully rendered size when building the compact
source. If the estimate exceeds **512 KiB** (headroom below the 1 MiB limit for
exclusion-list growth during rollouts), the controller:

1. Does not write the new content (keeps the last good object), and
2. Emits a Warning Event on the RBG explaining that the discovery snapshot
   exceeds the guardrail.

The stored compact source itself is `O(roles x components)` and in practice a
few hundred bytes; only the rendered output can grow, and it grows at the
consumer, not in etcd/kubelet paths.

### Rollout and Compatibility

The `rolebasedgroup.workloads.x-k8s.io/discovery-config-mode` annotation
distinguishes the two modes:

| Mode | Behavior |
|---|---|
| `legacy` | Objects reconciled by older controllers: per-role ConfigMaps with full enumeration content; untouched by this feature |
| `refine` | Single RBG-level ConfigMap with the compact format plus the rendering init container |

Rules:

1. Mode is fixed on first reconciliation (existing behavior) and **never
   migrated automatically**, so existing RBGs keep their per-role ConfigMaps and
   mounted content unchanged.
2. New RBGs created after the controller upgrade get `refine`.
3. Downgrade: `refine` RBGs keep their RBG-level ConfigMap, which the older
   controller will not overwrite (older controllers only manage per-role
   ConfigMaps).

### Phase 1 Scope

In scope:

- Single RBG-level ConfigMap with the compact format (roles array, templates,
  component-level ports).
- `excludedOrdinals` projection from KEP-499.
- Rendering init container producing the full address list.
- Size guardrail and mode gating.

Explicitly out of scope (and requiring a new KEP if ever needed):

- active/draining readiness-style states;
- dynamic (port-allocator) ports in the ConfigMap;
- runtime refresh of the rendered file.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

##### Prerequisite testing updates

None.

##### Unit tests

- `pkg/discovery`: refine builder produces the exact field set and stable
  ordering (roles/components sorted by name) across spec reorderings.
- Template correctness per pattern: Standalone, CustomComponents (including
  component `ServiceName` overrides), LeaderWorkerPattern (RoleInstanceSet and
  LWS-compatible `grouped` indexing), including 63-character truncation and
  legacy compatible Service names.
- `excludedOrdinals`: range expansion, dedup/sort, out-of-range dropped with Event.
- Renderer: placeholder substitution, grouped index arithmetic, fail-fast on
  malformed input, and equality with the current enumeration output.
- Size guardrail threshold behavior.

##### Integration tests

- Reconcile an RBG in `refine` mode: verify ConfigMap content, injected init
  container, volumes, and the rendered file inside a test Pod.

##### e2e tests

- Extend `test/e2e/testcase/v1alpha2/discovery_configmap.go`: assert the rendered
  file is field-by-field equal to the enumeration the current builder produces
  for multi-pod patterns and StandalonePattern.
- Verify excluded ordinals (KEP-499 annotation) are absent from the rendered file.
- Verify failure surfacing when the renderer input is malformed.

### Graduation Criteria

#### Alpha

- Feature implemented behind the mode annotation; new RBGs default to `refine`.
- Unit, integration, and e2e coverage as above.
- At least one large-scale (2000+ instance) scale test showing the stored ConfigMap
  stays under 1 KiB and reconciliation does not hit the guardrail.

#### Beta

- Renderer image published and versioned; docs for direct compact-source
  consumption.
- No reports of rendered-output divergence from the current address list.

#### GA

- Two releases of stable behavior; legacy-mode RBGs still untouched.

#### Deprecation

- N/A. The refine mode does not remove any user-facing API; per-role ConfigMaps
  remain supported for `legacy`-mode RBGs.

### Upgrade / Downgrade Strategy

See [Rollout and Compatibility](#rollout-and-compatibility). Upgrades only
affect newly created RBGs; existing `legacy` objects keep their ConfigMaps and
mounts untouched.

Downgrade: roll back the controller image. `refine` RBGs keep their RBG-level
ConfigMap; the older controller does not manage that object and does not
corrupt it. Pods recreated by the older controller lose the rendering init
container and, since their per-role ConfigMaps no longer exist, such RBGs
should be recreated rather than rolled back (documented operational guidance).

### Version Skew Strategy

The `format: refine` marker lets any renderer detect the schema version. The
RBG-level ConfigMap is only written by controllers that understand `refine`
mode; older controllers ignore it (they only manage per-role ConfigMaps).

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

###### How can this feature be enabled / disabled in a live cluster?

It is enabled automatically for newly created RBGs after the controller
upgrade; existing RBGs keep their pinned `discovery-config-mode`. There is no
feature gate; the mode is recorded per RBG.

###### Does enabling the feature change any default behavior?

Only for new RBGs: they get one RBG-level ConfigMap with the compact format and
the rendering init container. Existing RBGs are untouched.

###### Can the feature be disabled once it has been enabled (i.e. can we roll back the enablement)?

Roll back the controller image; see the Upgrade / Downgrade Strategy for the
operational guidance on `refine`-mode RBGs.

###### What happens if we reenable the feature if it was previously rolled back?

The controller resumes reconciling the RBG-level ConfigMap; the compact source
is deterministic, so content is rebuilt identically.

###### Are there any tests for feature enablement/disablement?

Yes: unit and integration tests cover mode selection on first reconciliation
and that `legacy` RBGs are never migrated.

### Rollout, Upgrade and Rollback Planning

###### How can a rollout or rollback fail? Can it impact already running workloads?

A rollout failure is a reconcile failure on the new ConfigMap (guarded by the
size guardrail) and surfaces as Warning Events. Already running workloads are
not impacted: they keep their mounted file; only newly created Pods consume the
rendered output.

###### What specific metrics should inform a rollback?

Elevated reconcile error rates for the RBG controller and guardrail-warning
events.

###### Were upgrade and rollback tested? Was the upgrade->downgrade->upgrade path tested?

Covered by the integration tests for mode gating; upgrade->downgrade->upgrade
is exercised for ConfigMap content stability (deterministic rebuild).

###### Is the rollout accompanied by any deprecations and/or removals of features, APIs, fields of API types, flags, etc.?

No.

### Monitoring Requirements

###### How can an operator determine if the feature is in use by workloads?

`kubectl get rbg -o yaml` shows the `discovery-config-mode: refine` annotation;
the RBG-level ConfigMap exists with `format: refine`.

###### How can someone using this feature know that it is working for their instance?

The rendered `/etc/rbg/config.yaml` exists and is non-empty in the Pod; init
container exit code 0.

###### What are the reasonable SLOs (Service Level Objectives) for the enhancement?

No new SLOs: ConfigMap reconciliation is part of the existing RBG reconcile
loop.

###### What are the SLIs (Service Level Indicators) an operator can use to determine the health of the service?

RBG controller reconcile error rate and Warning Events
(`FailedReconcileDiscoveryConfigMap`, guardrail warnings).

###### Are there any missing metrics that would be useful to have to improve observability of this feature?

None identified at alpha; a counter for guardrail breaches can be added if the
events prove insufficient.

### Dependencies

###### Does this feature depend on any specific services running in the cluster?

No. The renderer init container performs no network or API-server access; it
only reads the mounted ConfigMap.

### Scalability

###### Will enabling / using this feature result in any new API calls?

No additional API calls beyond the existing ConfigMap reconciliation, which is
reduced (one object per RBG instead of one per role).

###### Will enabling / using this feature result in introducing new API types?

No.

###### Will enabling / using this feature result in any new calls to the cloud provider?

No.

###### Will enabling / using this feature result in increasing size or count of the existing API objects?

The opposite: fewer ConfigMaps (one per RBG instead of one per role) and a
stored size of `O(roles x components)` instead of `O(instances)`.

###### Will enabling / using this feature result in increasing time taken by any operations covered by existing SLIs/SLOs?

No; building the compact source is cheaper than enumerating every instance.

###### Will enabling / using this feature result in non-negligible increase of resource usage (CPU, RAM, disk, IO, ...) in any components?

Pods gain a small init container and an emptyDir volume for the rendered file.
The rendered file is bounded by the 512 KiB guardrail.

###### Can enabling / using this feature result in resource exhaustion of some node resources (PIDs, sockets, inodes, etc.)?

No; one extra init container process per Pod and a small emptyDir file.

### Troubleshooting

###### How does this feature react if the API server and/or etcd is unavailable?

Reconciliation fails and retries with the existing backoff; running Pods are
unaffected because the rendered file is local.

###### What are other known failure modes?

- Renderer fails on malformed input (controller bug):
  - Detection: init container non-zero exit; Pod stuck in Init state.
  - Mitigation: no partial file is written; the Pod surfaces the error immediately.
  - Diagnostics: init container log shows the parse/substitution error verbatim.
  - Testing: renderer unit tests assert fail-fast behavior.
- Rendered output would exceed the guardrail:
  - Detection: Warning Event on the RBG; last-good ConfigMap is kept.
  - Mitigation: reconciliation continues with the previous content.
  - Testing: size guardrail unit tests.

###### What steps should be taken if SLOs are not being met to determine the problem?

Inspect RBG Warning Events and controller logs, then `kubectl get cm <rbg> -o
yaml` and the init container logs of an affected Pod.

## Implementation History

- 2025-12-29: initial KEP (compact template proposal, per-role to shared
  ConfigMap direction).
- 2026-11-12: consolidated design: single RBG-level ConfigMap carrying a
  compact declarative format (roles array, controller-computed `base`
  templates, component-level ports, KEP-499 `excludedOrdinals`), a
  compatibility rendering init container, a size guardrail, and mode gating
  between `legacy` and `refine`.

## Drawbacks

- Adds a rendering init container to every Pod that consumes the discovery file.
- The rendered file is a startup snapshot; ConfigMap updates after startup are not
  reflected until Pod recreation.
- Two content forms (compact source, rendered address list) coexist during the
  transition.

## Alternatives

1. **Single RBG-level ConfigMap with full enumeration**: removes the per-role
   duplication but keeps the `O(instances)` size problem and the 1 MiB risk;
   superseded by the compact format.
2. **Client-side FQDN derivation from naming conventions**: rejected.
   63-character truncation, legacy compatible Service names, component
   `ServiceName` overrides, and three naming patterns make the naming rules a
   fragile public contract; the controller must compute addresses.
3. **Range enumeration only** (`instances: [{from: 0, to: 499}]`): smaller
   reduction and still requires an address template; superseded by the full
   template approach.
4. **Per-role ConfigMap sharding**: reduces the limit from group-wide to
   per-role scale but keeps many objects and fails for huge single roles;
   rejected.
5. **Runtime discovery (DNS SRV / EndpointSlice watch)**: answers readiness-style
   questions but not ordinal identity ("which index am I", "who is my leader"),
   and violates RBG's static-injection philosophy; kept as a complement, not a
   replacement.
6. **active/draining membership fields**: rejected. The ConfigMap is static and
   declarative; distinguishing draining from not-yet-created is EndpointSlice's
   job. The single KEP-499 `excludedOrdinals` field suffices.

## Infrastructure Needed (Optional)

- Release pipeline support for publishing the renderer init container image
  alongside the controller image.

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
    - [ConfigMap Publication](#configmap-publication)
    - [Source Object Naming](#source-object-naming)
    - [Compact Format](#compact-format)
    - [Address Templates](#address-templates)
    - [Component Indexing](#component-indexing)
    - [Membership Encoding](#membership-encoding)
    - [Source Publication](#source-publication)
    - [Size Guardrail](#size-guardrail)
    - [Rollout and Downgrade](#rollout-and-downgrade)
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
discovery ConfigMap.

The shipped `refine` mode (see [Shipped Baseline](#shipped-baseline)) stores a
full enumeration of every instance address in a single RBG-level ConfigMap.
With the example names used in this KEP, one serialized pod entry is roughly
118 bytes, so a 4000-replica role with 5 pods per instance is about 2.4 MB and
crosses the 1 MiB ConfigMap limit. Every scale event rewrites the whole object
to every node that mounts it.

The compact source stores a **controller-computed address template plus
declarative parameters** per role/component, so its size is
`O(roles x components)`, independent of replica count. Stored content size
grows only with the number of normalized skip ranges during drains, which
KEP-499 bounds.

The compact source is additive. The controller publishes it in a separate
ConfigMap, and the mounted `<rbg>` ConfigMap keeps its current format and
behavior. Pod-level delivery and consumer migration are follow-up work.

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
   `rbg.workloads.x-k8s.io/discovery-config-mode` annotation pins
   each RBG to `legacy` or `refine` on first reconciliation
   (`internal/controller/workloads/rolebasedgroup_controller.go`,
   `ensureDiscoveryConfigMode`). New RBGs get `refine` since PR #134
   (released in v0.7.0/v0.8.0).
2. **`legacy`** RBGs (pre-#134) were created by an older controller that
   managed per-role ConfigMaps. Since #188, the injector is mode-unaware and
   mounts the RBG-level `<rbg>` ConfigMap for every stateful role, while only
   `refine` creates that object. A recreated legacy Pod can therefore reference
   a missing ConfigMap. This is a pre-existing product gap, not a behavior this
   KEP changes.
3. **`refine`** RBGs get a single RBG-level ConfigMap named after the RBG,
   rewritten on every reconcile (`reconcileRefinedDiscoveryConfigMap`), holding
   the **full enumeration**: one `address` entry per pod plus per-entry copies
   of the role's `servicePorts`.
4. **Addresses are relative `<pod>.<service>` names** — for example
   `<rbg>-<role>-0.s-<rbg>-<role>` — with no namespace or
   `svc.cluster.local` suffix (`pkg/discovery/config_builder.go`,
   `buildInstances`). They resolve via the pod's DNS search path.
5. **Membership selection is conditional.** `ConfigBuilder.memberOrdinals`
   does not unconditionally read the child. Before child creation, it uses
   dense `[0, replicas)` when no exclusion input is present, projects valid
   exclusions when they are present, and returns no members for invalid input.
   Once the child exists, it reads `CommittedOrdinals()` only when exclusion
   input is present or invalid, or when the child carries a committed skip
   record; otherwise it uses dense `[0, role.replicas)`. In the routine
   scale-up window (role spec 3, committed child 2, no exclusion input or
   record), today publishes three members. Invalid input with a child freezes
   the last committed target.
6. **Ordering is split across the output shape.** `group.roles` is spec
   declaration order, and each role's `instances` array preserves builder
   order. The `roles:` mapping itself is a Go map serialized through
   `sigs.k8s.io/yaml`, so its keys are alphabetically sorted.
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
   (bounded growth from normalized skip ranges only).
2. A stable, independently versioned source schema that future consumers and
   migration tooling can rely on.
3. Keep the mounted `<rbg>` ConfigMap and the existing `legacy` / `refine`
   populations unchanged.
4. Leave room for a later consumer migration that does not rewrite the legacy
   object under running pods.
5. Fail fast and visibly when the stored object would exceed its limit.
6. Scope the new format to the non-deprecated RoleInstanceSet workload type.

### Non-Goals

1. Reflecting runtime readiness or liveness. Runtime membership changes are
   the job of DNS/EndpointSlice.
2. Pod-level delivery, consumer translation, and migration rollout.
3. Traffic draining, data migration, or PVC cleanup (inherited KEP-499
   non-goals).
4. Dynamic port allocator ports. RoleScoped/PodScoped allocated ports remain
   delivered through Pod environment variables and annotations.
5. Supporting Deployment-type (stateless, random pod names) workloads.
6. Changing the mounted ConfigMap or Pod templates.
7. Supporting deprecated workload types (Deployment, StatefulSet,
   LeaderWorkerSet) in the compact format. New uses of those types are
   rejected at admission; existing populations stay on `legacy`/`refine`
   behavior.

## Proposal

Store a compact declarative description in a separate
`<rbg>-source-<uid-hash>` ConfigMap:

```text
RBG controller
  └─ ConfigMap <rbg-name>-source-<uid-hash>  (compact source, format: compact)

The mounted `<rbg>` ConfigMap continues to serve current consumers.
```

### User Stories (Optional)

#### Story 1

As a cluster operator, I run disaggregated inference RBGs with thousands of
instances per role. The discovery ConfigMap must not grow with replica count,
must stay far below the 1 MiB limit, and must remain a single small object
that kubelet can sync cheaply.

#### Story 2

As a tooling author, I need a stable compact source schema that does not grow
with replica count, so I can build a consumer path later without changing the
controller's storage format.

### Notes/Constraints/Caveats (Optional)

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
| Compact source encodes a different address set than the shipped builder | Unit tests expand the source with a test-only helper and compare it structurally with the current builder output |
| Long RBG/role names hit 63-char truncation | `base` is computed by the controller, which owns truncation and compatibility logic; clients never derive names |
| Future consumer migration changes behavior under running pods | This KEP keeps the compact source additive; migration needs its own rollout design |

## Design Details

### ConfigMap Publication

The compact source is additive. A controller that implements this KEP
publishes the compact source in addition to the existing `<rbg>` ConfigMap.
The existing discovery mode annotation and mounted volume remain unchanged.

| Population | Existing behavior | Compact source |
|---|---|---|
| pre-#134 `legacy` | historical per-role objects; v0.7+ injectors mount `<rbg>` even though only `refine` creates it | not published in this KEP |
| post-#134 `refine` | ConfigMap `<rbg>`, full enumeration, direct mount | published |
| new RBGs | ConfigMap `<rbg>`, full enumeration, direct mount | published |

The compact source lives in a **separate object** derived from the RBG name and
UID (see [Source Object Naming](#source-object-naming)). The controller
reconciles it before roles, on every reconcile. The mounted `<rbg>` object is
never rewritten or deleted by this KEP.

### Source Object Naming

The source ConfigMap name is:

```text
<rbg>-source-<uid-hash>
```

`<uid-hash>` is the first 16 bytes of `sha256(rbg.uid)`, encoded as lowercase
hex. If the full derived name would exceed Kubernetes' 253-character name
limit, the controller truncates the RBG-name prefix before appending
`-source-<uid-hash>`.

The UID suffix keeps the generated name distinct across RBGs whose names would
otherwise produce the same source name. The name is stable for the RBG's
lifetime because the UID is stable. Before any create, update, or delete, the
controller verifies that the existing object's controller owner UID matches
the RBG; on mismatch it emits a Warning Event and refuses to write.

### Compact Format

```yaml
format: compact
namespace: default
group: pd-inference

roles:                            # array, spec declaration order
- name: prefill                  # unique per RBG (CRD-validated)
  members: 500                    # member count; see below
  skipOrdinals: "3,17"            # committed skip ranges; see below
  components:                     # array, spec declaration order
  - name: leader
    size: 1                       # pods of this component per instance
    base: "pd-inference-prefill-{role_index}-leader-{component_index}.s-pd-inference-prefill"
    ports:                        # role servicePorts copied to each component
      http: 8080
      grpc: 50000
  - name: worker
    size: 4
    base: "pd-inference-prefill-{role_index}-worker-{component_index}.s-pd-inference-prefill"
    ports:
      http: 8080
      grpc: 50000
```

Field reference:

| Field | Type | Description |
|---|---|---|
| `format` | string | Must be `compact`; lets consumers and future revisions detect the schema |
| `namespace` / `group` | string | Namespace and RBG name |
| `roles` | array | Role entries in **spec declaration order**; `group.roles` preserves this order |
| `roles[].name` | string | Role name |
| `roles[].members` | int | Number of encoded members (see [Membership Encoding](#membership-encoding)) |
| `roles[].skipOrdinals` | string | Optional, normalized comma-separated inclusive ranges (`3`, `3,17`, `0-99999`); never expanded into one element per ordinal |
| `roles[].components` | array | Component entries in spec declaration order |
| `roles[].components[].name` | string | Component name |
| `roles[].components[].size` | int | Pod count of the component within one instance |
| `roles[].components[].offset` | int | Optional, default `0`; first `component_index` of the component |
| `roles[].components[].base` | string | Relative address template containing `{role_index}` and/or `{component_index}` |
| `roles[].components[].ports` | map | The role's `servicePorts`, copied to every component |

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

The source encodes `component_index` for each pod `j` in
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

### Membership Encoding

The compact format does **not** project the user-facing
`rbg.workloads.x-k8s.io/excluded-ordinals` annotation directly. It encodes the
membership selected by the shipped `memberOrdinals` algorithm, which is
conditional:

1. Before the child RoleInstanceSet exists:
   - no exclusion input → dense `[0, role.replicas)`;
   - valid exclusion input → `ordinals.Select(nil, role.replicas, excluded)`;
   - invalid input → empty membership.
2. After the child exists:
   - if exclusion input is present or invalid, or the child carries a
     committed skip record, use `CommittedOrdinals()`;
   - otherwise use dense `[0, role.replicas)`.

In the steady state, the controller evaluates that algorithm and encodes the
selected member count and holes without expanding the member list:

- `members` is the number of selected ordinals;
- `skipOrdinals` is `ordinals.Record(members)`, the normalized inclusive
  ranges for the holes below the last member.

A consumer reconstructs membership with range jumping: emit the first
`members` ordinals not contained in `skipOrdinals`, using the same `Next`
semantics as `ordinals.Ranges.Members`. It never expands the skip ranges.
After a reconcile converges, this reproduces the shipped behavior, including
members with ordinal `>= members` (members 3, skips `1,3` → members
`[0,2,4]`) and freeze-on-invalid-input when a child already exists.

### Source Publication

The controller attempts to publish the compact source before role
reconciliation. A source write failure emits a Warning Event and requeues the
RBG; it does not block role reconciliation because no Pod consumes the source
in this KEP.

### Size Guardrail

The guardrail applies to the **serialized compact ConfigMap object**
(the thing that lives in etcd and has the 1 MiB limit), including the
`skipOrdinals` range string:

- Threshold: 512 KiB (headroom below the 1 MiB limit).
- On breach with an existing object: keep the last good content and emit a
  Warning Event when the guardrail state changes.
- On breach at first creation: do not create the object and emit one Warning
  Event.

A guardrail breach does not block role reconciliation.

Skip ranges are stored without expansion: a contiguous 5000-member drain is a
few bytes, and disjoint holes cost one short range entry each. The source never
becomes `O(replicas)` merely because a role has a large ordinal or a long skip
range.

### Rollout and Downgrade

The compact source is additive:

| Controller | Existing behavior | Compact source |
|---|---|---|
| without compact-source support | unchanged | not published |
| with compact-source support | unchanged | published in addition to `<rbg>` |

No Pod template, volume, or mount changes. A later KEP can define consumer
delivery and migration.

Downgrade to a controller without this feature is safe: the old controller
ignores the source object, and the RBG owner reference eventually garbage
collects it with the RBG.

### Phase 1 Scope

In scope:

- The compact source schema.
- Additive source object publication.
- Conditional membership encoding.
- Stored-object size guardrail.

Out of scope (new KEP required):

- consumer delivery and translation;
- consumer migration and cleanup;
- live refresh;
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
- Membership encoding: dense fallback, conditional child-target selection,
  projected pre-child exclusions, invalid input without a child, skips
  producing members `>= members`, and freeze-on-invalid-input with a child.
- Size guardrail: threshold behavior on update and first create.
- Long skip ranges: a one-member source with skip range `0-99999` stays far
  below the stored-object guardrail and encodes member `100000`.
- Source naming: derived name length, UID-hash stability, and refusal to
  write an object owned by another RBG.
- Structural equivalence: a test-only helper expands the compact source and
  compares it with the current builder output.

##### Integration tests

- Additive behavior: the mounted `<rbg>` ConfigMap and Pod templates are
  unchanged when the source is published.
- Source-publication failure: a write failure emits a Warning Event and
  requeues, but does not block role reconciliation.
- Name collision: RBGs `foo` and `foo-source` coexist without one controller
  overwriting the other's object.
- Downgrade drill: an older controller ignores the source object and the
  mounted ConfigMap remains usable.

##### e2e tests

- Extend `test/e2e/testcase/v1alpha2/discovery_configmap.go`: the compact
  source exists, the mounted `<rbg>` ConfigMap is unchanged, and no Pod
  template changes.
- Membership scenarios: KEP-499 drain and invalid exclusion input are encoded
  as specified.
- Downgrade drill: an older controller ignores the source object.

### Graduation Criteria

#### Alpha

- Compact source publication, format, and guardrail implemented.
- Unit, integration, and e2e coverage as above.
- A large-scale (2000+ instance) scale test showing the stored ConfigMap
  stays well under the guardrail and reconciliation does not fail.

#### Beta

- Docs for direct compact-source consumption.
- No reports of compact-source divergence.

#### GA

- Two releases of stable behavior; `legacy` and `refine` populations still
  untouched.

#### Deprecation

- N/A. No user-facing API is removed.

### Upgrade / Downgrade Strategy

See [Rollout and Downgrade](#rollout-and-downgrade). The feature only adds a
source object; it does not change Pod templates or mounted volumes.

### Version Skew Strategy

The `format: compact` marker lets future consumers detect the schema. The compact
source lives in the separate `<rbg>-source-<uid-hash>` object, which older
controllers never read or write.

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

###### How can this feature be enabled / disabled in a live cluster?

Install a controller release that implements this KEP. The controller then
publishes the compact source in addition to the existing ConfigMap.

###### Does enabling the feature change any default behavior?

Stored objects only. Pod templates, volumes, and mounted ConfigMaps are
unchanged.

###### Can the feature be disabled once it has been enabled (i.e. can we roll back the enablement)?

Roll back to a controller release without compact-source support. Workloads
are unaffected. The source object remains until the RBG is deleted, and an
operator may delete it manually.

###### What happens if we reinstall the feature after rolling it back?

The controller rebuilds the compact source deterministically from the same
conditional membership algorithm as the shipped builder.

###### Are there any tests for enablement/disablement?

Yes: integration and e2e tests for additive publication and the downgrade
drill.

### Rollout, Upgrade and Rollback Planning

###### How can a rollout or rollback fail? Can it impact already running workloads?

A rollout failure is a reconcile failure on the ConfigMap (guarded by the size
guardrail) and surfaces as Warning Events. Already running workloads are not
impacted.

###### What specific metrics should inform a rollback?

Elevated RBG controller reconcile error rates and guardrail-warning events.

###### Were upgrade and rollback tested? Was the upgrade->downgrade->upgrade path tested?

Covered by the integration downgrade drill and the e2e source-publication test.

###### Is the rollout accompanied by any deprecations and/or removals of features, APIs, fields of API types, flags, etc.?

No.

### Monitoring Requirements

###### How can an operator determine if the feature is in use by workloads?

The `<rbg>-source-<uid-hash>` ConfigMap exists and carries `format: compact`.

###### How can someone using this feature know that it is working for their instance?

The source object exists and its content matches the RBG's current roles,
components, and membership encoding.

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

No.

### Scalability

###### Will enabling / using this feature result in any new API calls?

No additional calls beyond the existing ConfigMap reconciliation.

###### Will enabling / using this feature result in introducing new API types?

No.

###### Will enabling / using this feature result in any new calls to the cloud provider?

No.

###### Will enabling / using this feature result in increasing size or count of the existing API objects?

One additional ConfigMap per RBG. The source is `O(roles x components)`, with
bounded growth from normalized skip ranges.

###### Will enabling / using this feature result in increasing time taken by any operations covered by existing SLIs/SLOs?

No; building the compact source is cheaper than enumerating every pod.

###### Will enabling / using this feature result in non-negligible increase of resource usage (CPU, RAM, disk, IO, ...) in any components?

No Pod resource changes.

###### Can enabling / using this feature result in resource exhaustion of some node resources (PIDs, sockets, inodes, etc.)?

No.

### Troubleshooting

###### How does this feature react if the API server and/or etcd is unavailable?

Reconciliation fails and retries with the existing backoff; running pods are
unaffected.

###### What are other known failure modes?

- Stored object would exceed the guardrail:
  - Detection: Warning Event; last-good content kept (or object not created
    on first reconcile).
  - Mitigation: role reconciliation continues.
  - Testing: guardrail unit tests.

###### What steps should be taken if SLOs are not being met to determine the problem?

Inspect RBG Warning Events and controller logs, then
`kubectl get cm <rbg> <rbg>-source-<uid-hash> -o yaml`.

## Implementation History

- 2026-11-13: initial KEP. Replaces KEP-133.
- 2026-11-17: scoped the KEP to the compact source schema and additive
  controller publication. Corrected the shipped baseline, conditional
  membership selection, role-map ordering, and Summary arithmetic. Skip
  ordinals use normalized ranges; the source name is collision-resistant.
  Added the stored-object guardrail.

Merge-order note: PR #577, which marks KEP-133 replaced by this KEP, must not
merge before this KEP is accepted. Merge this KEP first, or land both
together.

## Drawbacks

- Adds one ConfigMap per RBG.
- The compact source and the existing enumeration coexist until a consumer
  migration is designed.

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
6. **Compress the existing enumeration**: gzip keeps the content smaller, but
   the stored object still changes on every membership event and consumers
   must decompress it before parsing.
7. **active/draining membership fields**: rejected. The source is static and
   declarative; KEP-499 membership encoding already expresses the drain
   window correctly, and readiness is EndpointSlice's job.
8. **In-place rewrite of the shared `<rbg>` ConfigMap**: rejected. It changes
   the file under running pods; a later consumer migration can use the
   separate source object instead.

## Infrastructure Needed (Optional)

None.

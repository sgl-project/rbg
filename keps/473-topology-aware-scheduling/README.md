# KEP-NNNN: Topology-Aware Scheduling

## Table of Contents

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [User Stories](#user-stories)
        - [Story 1: Multi-Node Instance Packing](#story-1-multi-node-instance-packing)
        - [Story 2: Prefill–Decode Co-Location](#story-2-prefilldecode-co-location)
        - [Story 3: Fleet Uniformity Within a Scheduler Family](#story-3-fleet-uniformity-within-a-scheduler-family)
    - [Design Overview](#design-overview)
    - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
    - [Background: Scheduler Topology Dialects](#background-scheduler-topology-dialects)
    - [Level Identifiers: Pass-Through Semantics](#level-identifiers-pass-through-semantics)
    - [Core API: TopologyConstraint](#core-api-topologyconstraint)
    - [Attachment Points](#attachment-points)
    - [Scheduler Translation](#scheduler-translation)
        - [Translation Channels](#translation-channels)
        - [Translation Matrix](#translation-matrix)
        - [Translation Examples](#translation-examples)
    - [Observability](#observability)
    - [Dependencies](#dependencies)
    - [Implementation Phases](#implementation-phases)
    - [Test Plan](#test-plan)
        - [Unit tests](#unit-tests)
        - [Integration tests](#integration-tests)
        - [e2e tests](#e2e-tests)
    - [Graduation Criteria](#graduation-criteria)
    - [Upgrade / Downgrade Strategy](#upgrade--downgrade-strategy)
    - [Version Skew Strategy](#version-skew-strategy)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

RBG orchestrates role-based, multi-node inference workloads whose pods communicate intensely: tensor/pipeline parallelism spans machines within a single serving instance, and prefill–decode disaggregation streams KV cache across roles. The usable bandwidth and latency of that communication depend strongly on physical network locality — pods inside the same rack/block enjoy far better fabric than pods crossing spine-level, oversubscribed links.

This KEP introduces topology-aware scheduling for RBG with two pieces:

1. **`TopologyConstraint`** — a group-granularity API (`pack.required` / `pack.preferred`) attached at the RBG level (co-locate the whole group) and at the role level (pack each RoleInstance's pods). The values are **level identifiers in the configured scheduler's own vocabulary, passed through verbatim**: a Volcano `tierName`, a Koordinator `topologyLayer`, a KAI level alias, or a node label key for Kueue/native.
2. **A translation layer** that compiles the unified declaration into the dialect of the configured scheduler — Volcano `networkTopology` (PodGroup and subGroup level), Koordinator `gatherStrategy`, KAI placement constraints, or native pod affinity — validating levels against the scheduler's own topology objects, with explicit, observable failure when a level or a scheduler cannot honor the constraint.

## Motivation

Two production scenarios drive this work:

1. **Very large models require multi-node deployment.** Models such as Qwen3.8-Max no longer fit a single 8-GPU node and must run cross-machine TP/PP. Cross-machine traffic rides RDMA (IB/RoCE), where achievable bandwidth and latency are dictated by physical network position: communication inside one ToR/Block is fast; crossing the Spine degrades sharply. Every pod of one inference instance must therefore be packed into a single high-performance network domain.
2. **Prefill–decode (PD) disaggregated inference.** Prefill and decode transfer large, latency-critical KV cache volumes that directly determine TTFT. Co-locating paired prefill and decode pods inside the same network domain yields deterministic inference performance.

The native scheduler cannot express either need:

- Node labels form **flat** topology domains (`kubernetes.io/hostname`, `topology.kubernetes.io/zone`, custom keys) with no hierarchy or distance semantics — "same rack beats same block beats cross-spine" is inexpressible.
- Scheduling decisions are **per-pod and greedy**; there is no atomic, group-granularity placement for a set of pods that must land together.

AI schedulers (Volcano, Koordinator, KAI, Kueue) each built topology models that re-interpret node topology as a **domain tree plus group-granularity placement**, but each speaks a different dialect: Volcano uses integer tiers (or tier names) on a `HyperNode` tree, Koordinator uses named `topologyLayer`s, KAI uses level aliases, Kueue and the native scheduler use label keys. RBG, as the workload API, must let users state topology intent once and have it honored on any of these schedulers.

### Goals

1. **Declarative group–domain affinity.** Express topology intent on the RBG API with group-granularity semantics — the whole pod group is the atomic placement unit and a common performance domain is selected on the topology tree. Cover both scenarios (RoleInstance packing, RBG-level co-location) with hard (`required`) and soft (`preferred`) constraint semantics.
2. **Propagation along the resource chain.** Constraints are declared at the RBG/Role level and propagated along RBG → Role → RoleInstanceSet → RoleInstance → Pods, surfacing finally as pod scheduling constraints; users never configure pods individually.
3. **Uniform API shape, honest about the single scheduler.** One declaration structure translates into the downstream dialect of the configured scheduler (Volcano `networkTopology`, Koordinator `gatherStrategy`, KAI placement constraints, native affinity); the values are level identifiers in that scheduler's own vocabulary — no abstraction layer pretending cross-scheduler portability. Schedulers without topology capability report "unsupported" explicitly or degrade hard constraints to soft ones by configuration — behavior is always predictable.
4. **Lifecycle coverage.** Pods created by scale-out, rolling update, or failure recovery still satisfy the declared constraints (no constraint drift).
5. **Observable and diagnosable.** Actual placement, constraint satisfaction, and degradation/failure reasons (insufficient domain capacity, unsupported level, missing scheduler capability) are queryable from RBG/RoleInstance status and events.

### Non-Goals

1. **No anti-affinity.** Spreading replicas across topology domains (disaster-tolerance dispersion) is out of scope for this KEP.
2. **No segment affinity.** Packing cross-role *segments* (e.g., a fixed recipe of N prefill + M decode instances per segment) is designed in a separate KEP.
3. **No network QoS / bandwidth isolation.** Topology constraints decide *where* pods land; they do not guarantee bandwidth.
4. **No placement algorithms.** Domain selection, bin-packing, and capacity accounting belong to downstream schedulers; RBG only declares and translates constraints.
5. **No topology discovery ownership.** The topology tree (HyperNode/Topology/ClusterNetworkTopology CRs and node labels) is maintained by administrators or discovery tools (UFM, topograph); RBG consumes it.

## Proposal

### User Stories

#### Story 1: Multi-Node Instance Packing

As an inference platform engineer deploying very large models with multi-node TP/PP, I need every pod of one serving instance (e.g., 4 machines of a prefill instance) to be packed inside a single high-performance network domain, so that cross-machine RDMA traffic never crosses oversubscribed spine links. I declare a hard instance-level constraint (`pack.required: rack`) once on the role; every instance created by scale-out, rolling update, or recovery keeps honoring it.

#### Story 2: Prefill–Decode Co-Location

As an inference application developer running PD-disaggregated serving, I need prefill and decode pods of the same group to land in the same network block so that KV-cache transfer latency (TTFT) stays deterministic. I declare `pack.required: block` as the hard upper bound and `pack.preferred: rack` as the soft target at the RBG level: the scheduler gathers the group into one rack when capacity allows, and may fall back to block otherwise — but never crosses the block boundary.

### Design Overview

Topology-aware scheduling is built in two layers:

**User layer — `TopologyConstraint` on RBG and Role.** Users declare `pack.required` / `pack.preferred` with the level identifiers of the cluster's scheduler — the same strings administrators already maintain on Volcano `tierName`, Koordinator `topologyLayer`, or KAI aliases. Attachment points:

- **RBG level** (`spec.topologyConstraint`): constraint group = all pods of the RBG (PD co-location).
- **Role level** (`spec.role[].leaderWorkerPattern.topologyConstraint`): constraint group = one RoleInstance's pods; every instance picks its domain independently (instance packing).

**Translation layer.** Per scheduler, a translator compiles (constraint group, level identifier, hard/soft) into the scheduler dialect — mostly verbatim pass-through — riding the gang-scheduling channel (KEP-430): the Volcano `PodGroup`/`subGroupPolicy` is reused as the carrier, so topology composes with gang semantics on the same objects. Levels are validated at reconcile against the scheduler's own topology objects (HyperNode tierName set, ClusterNetworkTopology layers, Topology CR aliases); failures (unknown level, incapable scheduler) are explicit: status conditions plus events, never silent degradation.

### Notes/Constraints/Caveats

- **Topology constraints ride the gang channel.** Group objects (PodGroup) are the only carrier the schedulers offer for group-granularity placement. An RBG/role with topology constraints therefore gets a PodGroup even when gang scheduling is not configured; carrying `networkTopology` does not by itself impose gang semantics (`minMember` behavior is unchanged). On the native kube-scheduler there is no group object at all, so translation degrades to per-pod affinity — a documented approximation (per-pod greedy, no atomic group placement).
- **Gather-only semantics.** The API models "gather into a common domain" (聚). "Spread across domains" (散) is deliberately excluded.
- **Soft semantics are approximate on Volcano.** Volcano cannot anchor a soft target at a middle tier; in hard mode its scoring naturally prefers lower tiers, so "tighten within the required bound" is absorbed by scoring rather than declared.
- **Volcano requires tier names.** Constraints reference `HyperNode.spec.tierName` only; integer `highestTierAllowed` is never rendered. Clusters whose HyperNodes predate `tierName` must adopt it (otherwise they are unsupported).

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Volcano **silently ignores** an unknown `highestTierName` (constraint dropped, pods schedule unconstrained) | Translation layer lists cluster HyperNodes and verifies the tier name exists before rendering; on a miss it refuses to render and reports `TopologyTranslated=False` (reason `LevelUnresolved`) plus a warning event |
| Users mistype a level identifier (no schema can catch a wrong string) | Admission checks syntax only; reconcile-time existence validation against the scheduler's topology objects surfaces `TopologyTranslated=False` on the declaring object (RBG/RoleInstance), naming the offending value before any pod group is rendered |
| Scheduler has no topology capability (e.g., native kube-scheduler) | Hard constraints report unsupported explicitly or degrade to soft per configuration; always surfaced in status — never silently dropped |
| Scheduler topology drift (a level renamed/removed while workloads reference it) | Referencing RBGs are re-validated on topology-object change (watch); missing levels flip their `TopologyTranslated` conditions naming the exact identifier |
| Volcano version too old or tier names unmaintained (no `tierName`, no subGroup-level `networkTopology`) | Runtime CRD schema detection (same pattern as KEP-430's `hasSubGroupPolicy` check) plus HyperNode tierName existence validation; explicit unsupported |
| Constraints guaranteed only for newly created pods; already-running pods may drift from the constraint after external rescheduling | Constraints are re-rendered on every reconcile, so scale-out/rollout/recovery pods always comply; drift of *running* pods is left to the scheduler's own mechanisms and documented |

## Design Details

### Background: Scheduler Topology Dialects

All target schedulers model the data-center network as a **domain tree** — Cluster → Region/Zone → Spine → Block/Leaf → Rack/ToR → Node → Device — where a lower lowest-common-ancestor means higher bandwidth and lower latency, and higher links are typically oversubscribed. They differ in how levels are named and how constraints are declared:

| Scheduler | Topology model | Level coordinate | Constraint declaration |
|---|---|---|---|
| Volcano | `HyperNode` CRD tree | integer `spec.tier`, optional `spec.tierName` | `PodGroup.spec.networkTopology`: `mode: hard` + `highestTierAllowed`/`highestTierName` (mutually exclusive); `mode: soft` carries no threshold; `subGroupPolicy[]` entries accept their own `networkTopology` |
| Koordinator | `ClusterNetworkTopology` CRD | named `topologyLayer` (e.g., `BlockLayer`) + node `labelKey` | PodGroup/GangGroup annotation `gang.scheduling.koordinator.sh/network-topology-spec` with per-layer `gatherStrategy`: `MustGather` / `PreferGather` |
| KAI | `Topology` CRD (`spec.levels`) | level `alias` + `nodeLabel` | PodGroup `topologyConstraint.requiredTopologyLevel` / `preferredTopologyLevel` (also as annotations with label keys); subGroups support their own constraint |

Key structural facts the design exploits:

- Every dialect can be keyed by a **string**: Volcano `tierName`, Koordinator `topologyLayer`, KAI `alias` are names; Kueue/native use label keys, which are strings too. A single pass-through string field therefore covers all dialects without type branching or a mapping layer.
- Volcano's hard mode plus its tier-preferring scoring subsumes a separate soft field; Koordinator and KAI natively support two-tier (required + preferred) declarations.
- Volcano's `subGroupPolicy` (used by KEP-430 gang translation, partitioned per RoleInstance via `matchLabelKeys`) accepts per-subGroup `networkTopology`, giving instance-granularity constraints for free.

### Level Identifiers

Constraint types `required` and `preferred` are supported:

| Scheduler | `required`/`preferred` value | Rendered into |
|---|---|---|
| Volcano | HyperNode `spec.tierName` (e.g., `rack`) — **HyperNodes must maintain tierName** | `highestTierName` |
| Koordinator | `topologyLayer` name (e.g., `BlockLayer`) | gatherStrategy `layer` |
| KAI | level `alias` of a Topology CR (e.g., `block`) | `requiredTopologyLevel` / `preferredTopologyLevel` |
| Kueue / native | node label key (e.g., `topology.cloud.com/rack`) | TAS label key / podAffinity `topologyKey` |

**Validation runs at reconcile** (KEP-430 precedent: webhooks avoid cross-resource reads — the informer cache is not started when the webhook serves; admission checks syntax only):

| Check | Source | Notes |
|---|---|---|
| Level existence | Active scheduler's topology objects (HyperNode set / ClusterNetworkTopology / Topology CR) | Unknown identifier → `TopologyTranslated=False` + event, never silent degradation |
| `preferred` not higher than `required` | Ordering from the same objects (Volcano tier integers, CR levels array order) | Native label keys have no ordered source — **the ordering check is skipped on the native dialect** (documented limitation) |
| Dialect capability | Runtime CRD schema inspection (same pattern as KEP-430's `hasSubGroupPolicy`) | Older Volcano without `tierName`/subGroup `networkTopology`, or HyperNodes not maintaining tierName → explicit unsupported; no integer-tier fallback |

**Portability boundary.** RBG manifests are reusable verbatim across clusters running the same scheduler family; migrating to another scheduler rewrites only the `required`/`preferred` strings — the manifest structure is unchanged. Cross-scheduler-transparent manifests would require a vocabulary indirection, which is rejected for now (see Alternatives).

### Core API: TopologyConstraint

```go
// TopologyConstraint defines topology placement requirements.
type TopologyConstraint struct {
    // Pack specifies topology packing constraints for each replica of the resource.
	// +optional
    Pack *TopologyPackConstraint `json:"pack"`
}

type TopologyPackConstraint struct {
	// Required defines a topology constraint that must be satisfied as a hard requirement. The workload will not be
	// scheduled if this constraint cannot be satisfied. Generally, it is easier for the scheduler to satisfy constraints
	// on topology domains with larger compute capacity, (e.g. zone or datacenter), than smaller domains, (e.g. host or
	// numa).
	// +optional
	Required *string `json:"required,omitempty"`

	// Preferred defines best-effort topology constraint. Topology domains that provide the most optimized performance
	// with dense packing are typically used as preferred constraints for topology packing. Since it is preferred
	// constraint, it is therefore not binding on the scheduler to mandatorily satisfy this packing constraint. Scheduler
	// can fall back to higher topology levels (upto Required constraint) if preferred cannot be satisfied.
	// +optional
	Preferred *string `json:"preferred,omitempty"`
}
```

### Attachment Points

```yaml
# RoleBasedGroup spec
spec:
  topologyConstraint:                # RBG-level co-location: the whole group within one block,
    pack:                            # tightened to one rack when possible
      preferred: rack
      required: block
  roles:
  - name: prefill
    replicas: 2
    leaderWorkerPattern:             # each instance spans 4 machines (multi-node deployment)
      size: 4
      topologyConstraint:            # instance packing: the 4 pods of each prefill instance
        pack:                        # converge into a single rack
          required: rack
```

`TopologyConstraint` attaches at two places: the RBG spec (group = all pods of the RBG) and the role's instance pattern (group = one RoleInstance's pods, each instance choosing its domain independently). The example values (`rack`, `block`) are shown in Volcano/KAI dialect; on Koordinator write `RackLayer`/`BlockLayer`.

### Scheduler Translation

The translation layer compiles the unified declaration (constraint group + level identifier + hard/soft) into the downstream dialect.

#### Translation Channels

- **RoleInstance packing** — one constraint group per instance: Volcano uses the subGroup-level `networkTopology` (sharing the `subGroupPolicy` channel with instance-level gang; `matchLabelKeys` already partitions subGroups per instance); KAI uses subGroup constraints; **Koordinator has no instance-level form** (`network-topology-spec` must be uniform across the whole GangGroup) — role-level constraints report unsupported on that dialect.
- **RBG-level co-location** — one constraint group per RBG: Volcano uses `PodGroup.spec.networkTopology`; Koordinator uses GangGroup / PodGroup annotation.

#### Translation Matrix

| RBG API | Volcano | Koordinator gatherStrategy | KAI |
|---|---|---|---|
| `required: R` | `mode: hard` + `highestTierName: R` | `[{R, MustGather}]` | `requiredTopologyLevel: R` |
| `required: R` + `preferred: P` | `mode: hard` + `highestTierName: R` (P absorbed by scoring) | `[{R, MustGather}, {P, PreferGather}]` | `requiredTopologyLevel: R` + `preferredTopologyLevel: P` |
| `preferred: P` only | `mode: soft` (no tier threshold) | `[{P, PreferGather}]` | `preferredTopologyLevel: P` |
| Level validation fails (e.g., unknown tierName) | Translation fails; status condition / event; no silent degradation | same | same |

#### Translation Examples

Input — the RBG from [Attachment Points](#attachment-points): RBG-level `required: block` + `preferred: rack`, prefill instance-level `required: rack` (2 instances × 4 pods, instances `infer-0-prefill-0/1`).

**Volcano** — both levels land on a single PodGroup: RBG-level at `spec.networkTopology`, instance-level at `subGroupPolicy[].networkTopology` (`matchLabelKeys` already partitions subGroups per instance, so the constraint applies per instance):

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: infer-0
  namespace: default
  ownerReferences:
  - apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroup
    name: infer-0
    controller: true
spec:
  minMember: 8                     # inherited from gang computation: prefill 2 instances × 4 pods
  networkTopology:                 # RBG-level: required=block; preferred=rack absorbed by scoring
    mode: hard
    highestTierName: block
  subGroupPolicy:
  - name: prefill
    labelSelector:
      matchLabels:
        rbg.workloads.x-k8s.io/group-name: infer-0
        rbg.workloads.x-k8s.io/role-name: prefill
    matchLabelKeys:
    - rbg.workloads.x-k8s.io/role-instance-name
    minSubGroups: 2
    subGroupSize: 4
    networkTopology:               # instance packing: each subGroup (= each instance) packs independently
      mode: hard
      highestTierName: rack
```

Cluster HyperNodes must maintain `spec.tierName`; if the referenced name is found on no HyperNode (including clusters not maintaining tierName at all), validation fails explicitly — there is no integer `highestTierAllowed` fallback.

**Koordinator** — RBG-level lands on the GangGroup annotations: every member PodGroup of a GangGroup must carry **identical** `groups` and `network-topology-spec` annotations, and the gather algorithm runs over all member pods of the group:

```yaml
apiVersion: scheduling.sigs.k8s.io/v1alpha1
kind: PodGroup
metadata:
  name: infer-0-prefill-0        # one GangGroup member (2 total, one per instance); all members carry identical annotations
  annotations:
    gang.scheduling.koordinator.sh/groups: '["default/infer-0-prefill-0", "default/infer-0-prefill-1"]'
    # required=block → MustGather@BlockLayer; preferred=rack → PreferGather@RackLayer
    gang.scheduling.koordinator.sh/network-topology-spec: |
      { "gatherStrategy": [
          {"layer": "BlockLayer", "strategy": "MustGather"},
          {"layer": "RackLayer",  "strategy": "PreferGather"} ]}
spec:
  minMember: 4
```

Instance-level constraints **cannot be expressed** on Koordinator: `network-topology-spec` has no per-subGroup form (unlike Volcano's `subGroupPolicy`), and divergent member specs are invalid; role-level constraints report unsupported (`SchedulerUnsupported`).

**KAI** — RBG-level lands on the PodGroup `topologyConstraint`, instance-level on subGroups (isomorphic to Volcano's subGroupPolicy, subGroups aligned with instance boundaries):

```yaml
apiVersion: scheduling.run.ai/v2alpha2
kind: PodGroup
metadata:
  name: infer-0
spec:
  topologyConstraint:              # RBG-level
    topology: cluster-topology
    requiredTopologyLevel: block   # alias pass-through
    preferredTopologyLevel: rack
  subGroups:
  - name: prefill
    minMember: 4
    topologyConstraint:            # instance-level
      topology: cluster-topology
      requiredTopologyLevel: rack
```

### Observability

- **RBG / RoleInstance conditions**: `TopologyTranslated` — `False` with reasons such as `LevelUnresolved` (identifier absent from the scheduler's topology objects, e.g., a Volcano tier name found on no HyperNode), `InvalidLevelOrder` (preferred higher than required), `SchedulerUnsupported` (scheduler lacks topology capability). Each condition transition emits one warning event (edge-triggered, per the KEP-430 event-spam analysis).
- Placement outcomes themselves (which domain a group landed in) remain owned by the scheduler (e.g., Volcano's allocated-HyperNode bookkeeping); RBG status links failure *reasons* back to the declaring object.

### Dependencies

- **A scheduler with topology-aware scheduling support.** Phase 1 targets Volcano with `tierName`/subGroup-level `networkTopology` support; Koordinator, KAI, and native translation land in later phases. Runtime CRD schema inspection detects field availability (same pattern as KEP-430).
- **Cluster topology objects and node labels**, maintained by administrators or discovery tools (UFM, RoCE, topograph). For label-backed dialects, nodes must carry the label keys referenced by constraints. These objects are also the reconcile-time validation source for level existence and ordering.
- **The gang channel (KEP-430)** as the constraint carrier: PodGroup/subGroupPolicy objects and the pod label conventions (`group-name`, `role-name`, `role-instance-name`).

### Implementation Phases

- **Phase 1**: `TopologyConstraint` API (RBG + role attachment), translation layer interface + **Volcano** implementation, reconcile-time validation against scheduler topology objects, status conditions/events, tests.
- **Phase 2**: **Koordinator** translators.
- **Phase 3**: **KAI** translator; validation hardening (drift heuristics, richer diagnostics).

### Test Plan

[x] I/we understand the owners of the involved components may require updates to existing tests to make this code solid enough prior to committing the changes necessary to implement this enhancement.

#### Unit tests

1. Pass-through rendering: per-dialect rendering of the identifier string; Volcano rejects names found on no HyperNode (no integer fallback).
2. Translation matrix: every API combination (`required`, `required+preferred`, `preferred`-only) × every dialect renders the expected dialect object.
3. Reconcile-time semantic validation: unknown level identifier, ordering violation → correct condition reasons; recovery when the topology object or the RBG is fixed.
4. Volcano runtime detection: CRD schema inspection for `tierName` / subGroup `networkTopology`; unmaintained-tierName and unsupported paths.

#### Integration tests

1. envtest: create scheduler topology fixtures + RBG with both attachment points, assert the rendered PodGroup (`networkTopology`, `subGroupPolicy[].networkTopology`) field-by-field.
2. Topology-object change triggers re-validation of referencing RBGs; level removal flips their conditions.
3. Deleting the constraint removes rendered dialect fields on the next reconcile.

#### e2e tests

1. Volcano cluster with labeled HyperNodes (tier names aligned): deploy the example RBG, verify each instance's pods land within one rack-tier HyperNode and the group within one block-tier HyperNode.
2. Insufficient domain capacity: hard constraint → pods stay Pending, condition/event explains; soft constraint → cross-domain placement allowed.
3. Unknown tier name: explicit error surfaced, pods not silently unconstrained.

### Graduation Criteria

- [ ] `TopologyConstraint` API defined (RBG spec + role instance pattern attachment)
- [ ] Translation layer interface + Volcano implementation (PodGroup + subGroup networkTopology, tier-name verification)
- [ ] Admission + reconcile validation (syntax at admission; level existence and ordering against scheduler topology objects at reconcile)
- [ ] Status conditions and edge-triggered events on RBG/RoleInstance
- [ ] Unit test coverage
- [ ] Integration test coverage
- [ ] e2e tests (Volcano environment)

### Upgrade / Downgrade Strategy

- **Upgrade**: all new fields are optional; existing RBGs without `topologyConstraint` behave unchanged.
- **Downgrade**: removing `topologyConstraint` removes the rendered dialect fields on the next reconcile; already-scheduled pods are unaffected. Rolling the controller back to a version without this feature leaves rendered PodGroup fields in place but unmaintained — they remain valid scheduler configuration and can be cleaned up manually.

### Version Skew Strategy

`TopologyConstraint` is an additive, optional API. Older controllers ignore the new fields (constraints simply unenforced); newer controllers reading objects written by older versions see no constraints and render nothing. No control-plane/node coordination is involved beyond the scheduler's own version requirements, which are detected at runtime via CRD schema inspection.

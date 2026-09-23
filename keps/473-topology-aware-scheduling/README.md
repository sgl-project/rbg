# KEP-473: Topology-Aware Scheduling

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
        - [Story 3: Co-Location Over a Role Subset](#story-3-co-location-over-a-role-subset)
    - [Design Overview](#design-overview)
    - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
    - [Background: Scheduler Topology Dialects](#background-scheduler-topology-dialects)
    - [Level Identifiers: Pass-Through Semantics](#level-identifiers-pass-through-semantics)
    - [Core API: TopologyConstraint](#core-api-topologyconstraint)
    - [Attachment Points](#attachment-points)
    - [Mutability and Update Semantics](#mutability-and-update-semantics)
    - [Placement Planning](#placement-planning)
        - [Placement Group Model](#placement-group-model)
        - [Scope Composition](#scope-composition)
        - [Reconcile Flow and Failure Gating](#reconcile-flow-and-failure-gating)
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

This KEP introduces topology-aware scheduling for RBG with three pieces:

1. **`TopologyConstraint`** — a group-granularity API (`pack.required` / `pack.preferred`), exposed on RoleSpec as **`InstanceTopologyConstraint`** (pack each RoleInstance's pods) and, for cross-role co-location, in **CoordinatedPolicy** rules that name an explicit set of roles (e.g., the prefill and decode roles of a PD-disaggregated deployment). The values are **level identifiers in the configured scheduler's own vocabulary, passed through verbatim**: a Volcano `tierName`, a Koordinator `topologyLayer`, or a KAI `nodeLabel` key. A `topologyName` field selects the scheduler topology resource on dialects that need one, currently KAI.
2. **A scheduler-independent placement planner.** Gang (KEP-430) and topology no longer each decide how physical PodGroups are partitioned. The planner first resolves one logical **PlacementGroup tree** from all placement intents; gang and topology are attributes of the same logical group. A pod can therefore have exactly one placement membership, and incompatible scope overlaps are rejected before workloads are created.
3. **A scheduler compiler.** One compiler per dialect (Volcano, Koordinator, KAI) is the only component that renders physical PodGroups and pod membership. If a dialect cannot compile a valid logical tree without semantic loss, it reports `SchedulerUnsupported` rather than approximating it.

## Motivation

Two production scenarios drive this work:

1. **Very large models require multi-node deployment.** Models such as Qwen3.8-Max no longer fit a single 8-GPU node and must run cross-machine TP/PP. Cross-machine traffic rides RDMA (IB/RoCE), where achievable bandwidth and latency are dictated by physical network position: communication inside one ToR/Block is fast; crossing the Spine degrades sharply. Every pod of one inference instance must therefore be packed into a single high-performance network domain.
2. **Prefill–decode (PD) disaggregated inference.** Prefill and decode transfer large, latency-critical KV cache volumes that directly determine TTFT. Co-locating prefill and decode pods inside the same network domain yields deterministic inference performance. Such deployments typically include further roles without latency-critical cross-role traffic, so co-location is a property of a **role subset**, not of the whole RBG.

The native kube-scheduler cannot express either need:

- Node labels form **flat** topology domains (`kubernetes.io/hostname`, `topology.kubernetes.io/zone`, custom keys) with no hierarchy or distance semantics — "same rack beats same block beats cross-spine" is inexpressible.
- Scheduling decisions are **per-pod and greedy**; there is no atomic, group-granularity placement for a set of pods that must land together.

Schedulers (Volcano, Koordinator, KAI) each built topology models that re-interpret node topology as a **domain tree plus group-granularity placement**, but each speaks a different dialect: Volcano uses tiers (and names) on a `HyperNode` tree, Koordinator uses named `topologyLayer`s, KAI uses levels backed by node-label keys. RBG, as the workload API, must let users state topology intent once and have it honored on any of these schedulers.

RBG also already has gang scheduling (KEP-430). Gang and topology are different dimensions of the same placement problem:

- **Gang answers when a logical group may be dispatched** (`minMember`, `minSubGroups`).
- **Topology answers where the members of that group must land** (`networkTopology`, gather strategies).

Because a Volcano/Koordinator/KAI pod can belong to only one physical PodGroup, gang and topology must not independently choose PodGroup membership. This KEP therefore places membership selection in a scheduler-independent planner, and physical rendering in the scheduler compiler.

### Goals

1. **Declarative group–domain affinity.** Express topology intent with group-granularity semantics — the pod group of one RoleInstance, or of an explicit role set, is the atomic placement unit and a common performance domain is selected on the topology tree. Cover both scenarios (RoleInstance packing, cross-role co-location over selected roles) with hard (`required`) and soft (`preferred`) constraint semantics.
2. **Composability with gang scheduling.** Gang and topology intents with the same scope compose on one placement group. Disjoint scopes remain independent. Containment scopes form a parent/child placement tree when a backend can render it. Partially overlapping scopes are rejected explicitly, before pod creation, because no target scheduler can represent a pod with two parents.
3. **Propagation along the resource chain.** Constraints are declared at the Role level (instance packing) and in CoordinatedPolicy rules (cross-role co-location), then propagated along RBG → Role → RoleInstanceSet → RoleInstance → Pods, surfacing finally as pod scheduling constraints; users never configure pods individually.
4. **Uniform API shape, honest about the single scheduler.** One declaration structure translates into the downstream dialect of the configured scheduler (Volcano `networkTopology`, Koordinator `gatherStrategy`, KAI topology constraints); the values are level identifiers in that scheduler's own vocabulary — no abstraction layer pretending cross-scheduler portability. Schedulers without topology capability report "unsupported" explicitly.
5. **Lifecycle coverage.** Pods created by scale-out, rolling update, or failure recovery still satisfy the declared constraints (no constraint drift).
6. **Observable and diagnosable.** Placement-plan validity, translation fidelity, constraint satisfaction, and failure reasons (insufficient domain capacity, unsupported level, missing scheduler capability, incompatible scopes) are queryable from the declaring object's status and events (CoordinatedPolicy / RBG / RoleInstance).

### Non-Goals

1. **No anti-affinity.** Spreading replicas across topology domains (disaster-tolerance dispersion) is out of scope for this KEP.
2. **No placement algorithms.** Domain selection, bin-packing, and capacity accounting belong to downstream schedulers; RBG only declares constraints, plans logical membership, and compiles scheduler objects.
3. **No topology discovery ownership.** The topology tree (HyperNode/Topology/ClusterNetworkTopology CRs and node labels) is maintained by administrators or discovery tools (UFM, topograph); RBG consumes it.
4. **No native kube-scheduler translation.** Native affinity is a per-pod approximation and cannot preserve group-granularity or gang semantics. A native scheduler receives an explicit `SchedulerUnsupported` condition in this KEP's delivery scope.

## Proposal

### User Stories

#### Story 1: Multi-Node Instance Packing

As an inference platform engineer deploying very large models with multi-node TP/PP, I need every pod of one serving instance (e.g., 4 machines of a prefill instance) to be packed inside a single high-performance network domain, so that cross-machine RDMA traffic never crosses oversubscribed spine links. I declare a hard instance-level constraint (`pack.required: rack`) once on the role; every instance created by scale-out, rolling update, or recovery keeps honoring it, and each instance independently chooses a domain that satisfies it.

#### Story 2: Prefill–Decode Co-Location

As an inference application developer running PD-disaggregated serving, I need prefill and decode pods to land in the same network block so that KV-cache transfer latency (TTFT) stays deterministic. I declare `pack.required: block` as the hard upper bound and `pack.preferred: rack` as the soft target in a CoordinatedPolicy rule over `roles: [prefill, decode]`: the scheduler gathers the rule's prefill+decode pods into one rack when capacity allows, and may fall back to block otherwise — but never crosses the block boundary.

#### Story 3: Co-Location Over a Role Subset

As an inference application developer, I need the topology constraint to cover exactly the roles that exchange latency-critical traffic — and nothing else. A wider group than necessary wastes scarce in-domain capacity and can render the constraint unsatisfiable. The CoordinatedPolicy rule's `roles` list states the constraint group explicitly: I name the roles to co-locate (e.g., `prefill` and `decode`); roles left out are simply outside the cross-role domain, while their own instance-level constraints still apply. Listing all roles of the RBG recovers whole-group co-location without a dedicated RBG-level field.

### Design Overview

Topology-aware scheduling is built in three layers:

**User layer — `TopologyConstraint` on Role and CoordinatedPolicy.** Users declare `pack.required` / `pack.preferred` with the level identifiers of the cluster's scheduler — the same strings administrators already maintain on Volcano `tierName`, Koordinator `topologyLayer`, or KAI node-label keys. All *inter-role* coordination, topology included, lives in CoordinatedPolicy (the object that already carries coordinated rolling update, scaling, and gang); *intra-role* packing stays on the role itself. Attachment points:

- **Role level** (`spec.roles[].instanceTopologyConstraint`): constraint group = one RoleInstance's pods; every instance picks its domain independently (instance packing). The field is pattern-independent and applies equally to `LeaderWorkerPattern` and `CustomComponentsPattern` roles.
- **Cross-role level** (`spec.policies[].strategy.scheduling.topologyConstraint` on CoordinatedPolicy): constraint group = all pods of the roles named in the rule's `roles` list (cross-role co-location). Roles not listed stay outside the cross-role domain (their own instance-level constraints still apply). Listing every role of the RBG recovers whole-group co-location, so no dedicated RBG-level field exists.

**Placement planning layer.** Before any scheduler object is created, the controller resolves gang intents (KEP-430 semantics are unchanged), topology intents, and role-instance partitioning into one **PlacementPlan**. The planner is the only owner of logical membership: equal scopes merge gang and topology attributes; disjoint scopes produce sibling groups; containment scopes produce a tree; partially overlapping scopes are rejected. This removes order dependence between gang and topology translation and prevents a pod from being assigned two different physical PodGroups.

**Scheduler compiler.** Per scheduler, a compiler consumes the valid PlacementPlan and renders the dialect — Volcano `networkTopology` at PodGroup/subGroup level, Koordinator `gatherStrategy`, KAI topology constraints — while preserving KEP-430 gang behavior when no topology is configured. Levels are validated at reconcile against the scheduler's own topology objects (HyperNode tierName set, ClusterNetworkTopology layers, KAI Topology CR); failures (unknown level, incapable scheduler, incompatible placement scopes) are explicit: status conditions plus events, never silent degradation.

### Notes/Constraints/Caveats

- **Group objects are the physical carrier, but not the semantic model.** Schedulers only offer group-granularity placement through PodGroup-like objects. A role or policy rule with topology constraints therefore gets group objects even when gang scheduling is not configured. Topology-only groups carry **no gang requirement**: on Volcano they render `minMember: 0` and omit gang threshold fields such as `minSubGroups`; on KAI they render `minMember: 0` / `minSubGroup: 0`. Gang fields render only for roles covered by gang-configured rules.
- **One membership owner.** Gang and topology resolvers declare intents only; they do not create PodGroups, choose PodGroup names, or inject `scheduling.k8s.io/group-name`. Only the scheduler compiler outputs objects and the unique pod bindings derived from the PlacementPlan.
- **Gather-only semantics.** The API models "gather into a common domain" (聚). "Spread across domains" (散) is deliberately excluded.
- **The constraint group is exactly the rule's role set.** Referenced roles join one common domain; unreferenced roles stay outside it (their own instance-level constraints still apply). A role may appear in at most one topology-bearing rule because placement membership partitions pods; overlap is rejected by the CoordinatedPolicy validating webhook, following the `gang.minReplicas` validation pattern. Gang and topology scopes may additionally be equal, disjoint, or nested, but not partially overlapping.
- **Soft semantics are approximate on Volcano.** Volcano cannot anchor a soft target at a middle tier. `required: R` plus `preferred: P` renders `mode: hard` with `highestTierName: R`; the preferred target is not represented as a tier threshold. This is an explicit fidelity loss reported as `PreferredAbsorbed`, not a claim that scoring is equivalent to anchoring at `P`.
- **Volcano requires tier names.** Constraints reference `HyperNode.spec.tierName` only; integer `highestTierAllowed` is never rendered. Clusters whose HyperNodes predate `tierName` must adopt it (otherwise they are unsupported).
- **KAI uses label-backed levels.** KAI `TopologyLevel` is a node-label key, not an abstract alias. The KAI PodGroup also names the `Topology` CR to use; RBG derives that name from controller configuration and validates that the object exists (see Level Identifiers).

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Users mistype a level identifier (no schema can catch a wrong string) | Admission checks syntax only; reconcile-time existence validation against the scheduler's topology objects surfaces `TopologyTranslated=False` on the declaring object (CoordinatedPolicy for rules, RBG/RoleInstance for instance-level packing), naming the offending value before any pod group is rendered |
| Gang and topology rules have partially overlapping scopes, so no backend can preserve both guarantees | Placement planning normalizes scopes before rendering and rejects partial overlap with `PlacementPlanReady=False` / `IncompatiblePlacementGroups`; role create/update is gated |
| Scheduler has no topology capability (including the native kube-scheduler) | Hard constraints report `SchedulerUnsupported` explicitly; always surfaced in status — never silently dropped or approximated by per-pod affinity |
| Scheduler topology drift (a level renamed/removed while workloads reference it) | Referencing RBGs are re-validated on topology-object change (watch); missing levels flip their `TopologyTranslated` conditions naming the exact identifier |
| Volcano version too old or tier names unmaintained (no `tierName`, no subGroup-level `networkTopology`) | Runtime CRD schema detection (same pattern as KEP-430's `hasSubGroupPolicy` check) plus HyperNode tierName existence validation; explicit unsupported |
| Constraints guaranteed only for newly created pods; already-running pods may drift from the constraint after external rescheduling | Placement validation gates role create/update, so new/updated pods never run unconstrained. Reconcile re-renders objects for scale-out/rollout/recovery. Running pods are not evicted; drift of *running* pods is left to the scheduler's own mechanisms and documented |
| Topology fields are changed after pods already exist | Admission and reconcile reject in-place updates; changing a topology constraint requires delete/recreate or a rollout to a new PlacementPlan generation |
| KAI topology identity is missing or inconsistent across one RBG | `topologyName` is validated at reconcile; a missing or mismatched name reports `TopologyResourceUnresolved` / `IncompatibleTopologyNames` and blocks rendering |
| A topology rule names a role absent from the RBG, or a role appears in two topology-bearing rules | Admission checks rule-internal syntax (same webhook pattern as `gang.minReplicas`); role existence is verified at reconcile against the RBG (a policy may be written before the RBG exists) and reported as `TopologyTranslated=False` (`RoleUnresolved`) naming the offending role |

## Design Details

### Background: Scheduler Topology Dialects

All target schedulers model the data-center network as a **domain tree** — Cluster → Region/Zone → Spine → Block/Leaf → Rack/ToR → Node → Device — where a lower lowest-common-ancestor means higher bandwidth and lower latency, and higher links are typically oversubscribed. They differ in how levels are named and how constraints are declared:

| Scheduler | Topology model | Level coordinate | Constraint declaration |
|---|---|---|---|
| Volcano | `HyperNode` CRD tree | integer `spec.tier`, optional `spec.tierName` | `PodGroup.spec.networkTopology`: `mode: hard` + `highestTierAllowed`/`highestTierName` (mutually exclusive); `mode: soft` carries no threshold; `subGroupPolicy[]` entries accept their own `networkTopology` |
| Koordinator | `ClusterNetworkTopology` CRD | named `topologyLayer` (e.g., `BlockLayer`) + node `labelKey` | PodGroup/GangGroup annotation `gang.scheduling.koordinator.sh/network-topology-spec` with per-layer `gatherStrategy`: `MustGather` / `PreferGather` |
| KAI | `Topology` CRD (`spec.levels`) | level `nodeLabel` (e.g., `topology.rack`) | PodGroup `topologyConstraint.requiredTopologyLevel` / `preferredTopologyLevel`; the constraint also names the `Topology` CR; subGroups nest arbitrarily and support their own constraints |

Key structural facts the design exploits:

- Every dialect can be keyed by a **string**: Volcano `tierName`, Koordinator `topologyLayer`, and KAI `nodeLabel` are names. A single pass-through string field therefore covers all dialects without type branching or a mapping layer.
- Koordinator and KAI natively support two-tier (required + preferred) declarations. Volcano hard mode has no middle-tier soft anchor; preferred is reported as absorbed rather than translated (see Translation Matrix).
- Volcano's `subGroupPolicy` (used by KEP-430 gang translation, partitioned per RoleInstance via `matchLabelKeys`) accepts per-subGroup `networkTopology`, giving instance-granularity constraints for this dialect.

### Level Identifiers: Pass-Through Semantics

Constraint types `required` and `preferred` are supported:

| Scheduler | `required`/`preferred` value | Rendered into |
|---|---|---|
| Volcano | HyperNode `spec.tierName` (e.g., `rack`) — **HyperNodes must maintain tierName** | `highestTierName` |
| Koordinator | `topologyLayer` name (e.g., `BlockLayer`) | gatherStrategy `layer` |
| KAI | Topology level's `nodeLabel` (e.g., `topology.rack`) | `requiredTopologyLevel` / `preferredTopologyLevel` |

`TopologyConstraint.topologyName` selects the scheduler's topology resource when a dialect has one. Today that is KAI's `Topology` CR; Volcano and Koordinator infer topology from their cluster-wide objects and do not consume the field. On KAI, a set `topologyName` wins over the controller default (`--kai-topology-name`); if neither is set, reconciliation reports `TopologyResourceUnresolved`. All topology constraints in one PlacementPlan must resolve to the same topology name, otherwise the plan is rejected as `IncompatibleTopologyNames`.

**Validation runs at reconcile** (KEP-430 precedent: webhooks avoid cross-resource reads — the informer cache is not started when the webhook serves; admission checks syntax only):

| Check | Source | Notes |
|---|---|---|
| Level existence | Active scheduler's topology objects (HyperNode set / ClusterNetworkTopology / the configured KAI Topology CR) | Unknown identifier → `TopologyTranslated=False` + event, never silent degradation |
| `preferred` not higher than `required` | Ordering from the same objects (Volcano tier integers, CR levels array order) | The pair is invalid if preferred is broader (higher in the tree) than required |
| Child level equal to or narrower than parent | Ordering from the same objects | A child placement group must not require a broader domain than its parent; reversed nesting is rejected |
| Dialect capability | Runtime CRD schema inspection (same pattern as KEP-430's `hasSubGroupPolicy`) | Older Volcano without `tierName`/subGroup `networkTopology`, or HyperNodes not maintaining tierName → explicit unsupported; no integer-tier fallback |
| Topology identity | `TopologyConstraint.topologyName` on the API, with controller default for dialects that need one | KAI: the named `Topology` CR must exist; all constraints in one PlacementPlan must resolve to the same name. Missing or mismatched → `TopologyResourceUnresolved` / `IncompatibleTopologyNames`; no rendering |

**Portability boundary.** RBG manifests are reusable verbatim across clusters running the same scheduler family; migrating to another scheduler rewrites only the `required`/`preferred` strings — the manifest structure is unchanged. Cross-scheduler-transparent manifests would require a vocabulary indirection, which is rejected for now (see Alternatives).

### Core API: TopologyConstraint

```go
// TopologyConstraint defines topology placement requirements.
type TopologyConstraint struct {
	// TopologyName names the scheduler's topology resource when the active
	// dialect has one. Today this is the KAI Topology CR name. Volcano and
	// Koordinator do not consume the field. If omitted, the controller uses its
	// configured default for that dialect.
	// +optional
	TopologyName *string `json:"topologyName,omitempty"`

	// Pack specifies topology packing constraints for each replica of the resource.
	// +optional
	Pack *TopologyPackConstraint `json:"pack,omitempty"`
}

type TopologyPackConstraint struct {
	// Required defines a topology constraint that must be satisfied as a hard requirement. The workload will not be
	// scheduled if this constraint cannot be satisfied. Generally, it is easier for the scheduler to satisfy constraints
	// on topology domains with larger compute capacity, (e.g., zone or datacenter), than smaller domains, (e.g., host or
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

Instance-level constraints are pattern-independent:

```go
// RoleSpec gains an instance topology member.
type RoleSpec struct {
	// ...existing fields...

	// InstanceTopologyConstraint packs the pods of each RoleInstance of this
	// role into one topology domain. It applies to every pattern that can produce
	// multiple pods per RoleInstance, including LeaderWorkerPattern and
	// CustomComponentsPattern.
	// +optional
	InstanceTopologyConstraint *TopologyConstraint `json:"instanceTopologyConstraint,omitempty"`
}
```

Cross-role co-location is expressed by reusing the same struct inside CoordinatedPolicy — the constraint group is the enclosing policy rule's `roles` list:

```go
// SchedulingCoordinationStrategy gains a topology member.
type SchedulingCoordinationStrategy struct {
	// Gang defines the gang scheduling coordination for roles. (unchanged)
	// +optional
	Gang *GangSchedulingStrategy `json:"gang,omitempty"`

	// TopologyConstraint defines topology co-location for the roles listed in
	// the enclosing policy rule's `roles` field. Roles not listed are
	// unconstrained by this rule; list every role of the RoleBasedGroup for
	// whole-group co-location.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}
```

### Attachment Points

```yaml
# RoleBasedGroup spec
spec:
  roles:
  - name: router
    replicas: 1
  - name: prefill
    replicas: 2
    leaderWorkerPattern:             # each instance spans 4 machines (multi-node deployment)
      size: 4
    instanceTopologyConstraint:      # instance-level, pattern-independent: the pods of each
      pack:                          # prefill instance converge into a single rack
        required: rack
  - name: decode
    replicas: 2
    leaderWorkerPattern:
      size: 2
---
# CoordinatedPolicy (same name and namespace as the RoleBasedGroup)
spec:
  policies:
  - name: pd-colocation
    roles: [prefill, decode]         # cross-role co-location: the rule's pods share one block,
    strategy:                        # tightened to one rack when possible; roles outside the
      scheduling:                    # rule are unconstrained by this rule
        topologyConstraint:
          pack:
            preferred: rack
            required: block
```

`InstanceTopologyConstraint` attaches to the Role (group = one RoleInstance's pods, each instance choosing its domain independently); `TopologyConstraint` attaches to a CoordinatedPolicy rule (group = all pods of the rule's `roles`; list every role to co-locate the whole RBG). The example values (`rack`, `block`) are shown in the Volcano dialect; on Koordinator write `RackLayer`/`BlockLayer`; on KAI write the level's node-label key (e.g., `topology.rack`) and set `topologyName` to the `Topology` CR name (e.g., `cluster-topology`). Volcano and Koordinator leave `topologyName` unset.

### Mutability and Update Semantics

Topology constraints are mutable only before the PlacementPlan has been rendered and the first pod has been created. Once placement becomes active, `topologyName`, `pack.required`, and `pack.preferred` are immutable on the declaring Role or CoordinatedPolicy rule.

The controller records placement activity in status, for example `TopologyConstraintActive=True`, so the validating webhook can enforce immutability without cross-resource reads. Reconcile applies the same guard if the webhook is bypassed.

To change a topology constraint after placement, delete and recreate the affected workload. Rolling updates that change topology constraints are out of scope for this KEP. Running pods are never migrated or evicted by this controller.

If the referenced topology resource is deleted, reconcile reports `TopologyResourceUnresolved`; if a referenced level is removed or renamed, reconcile reports `LevelUnresolved`. In both cases new pod creation is gated, and already-running pods are not evicted or migrated.

### Placement Planning

#### Placement Group Model

Placement planning is scheduler-independent and happens before any PodGroup is rendered:

```go
type PlacementPlan struct {
	Root *PlacementGroup
}

// PlacementScope is the logical scope a placement constraint applies to.
type PlacementScope struct {
	// Roles are the RBG roles covered by this scope.
	Roles []string

	// PartitionBy says how members are partitioned into concrete placement
	// groups. Empty means all members form one group. For instance topology,
	// the planner sets the RBG role-instance-name label, producing one group per
	// RoleInstance.
	PartitionBy []string
}

type PlacementGroup struct {
	// ID is stable for the lifetime of the logical group.
	ID string

	// Scope is the logical scope this placement group covers.
	Scope PlacementScope

	// Gang carries KEP-430 dispatch semantics; it never decides physical layout.
	Gang *GangConstraint

	// Topology carries where the group's members must be gathered.
	Topology *TopologyConstraint

	// Children are strictly contained sub-groups.
	Children []*PlacementGroup
}
```

The important distinction is `Scope.PartitionBy`. Instance topology is not "all pods of a role share one domain":

```text
roles=[prefill], partitionBy=[role-instance-name], required=rack

prefill-0 -> rack A
prefill-1 -> rack B
```

Each RoleInstance independently selects a domain. In the current API, `InstanceTopologyConstraint` always produces `PartitionBy=[role-instance-name]`, while a CoordinatedPolicy topology rule produces `PartitionBy=none`.

#### Scope Composition

The planner canonicalizes each intent's scope as `(sorted Roles, PartitionBy)`, not as a role set alone. Two scopes are comparable only through the following four relations:

| Relation | Formal condition | Example | Result |
|---|---|---|---|
| Equal | Same `Roles` and same `PartitionBy` | gang `{Roles=[P,D], PartitionBy=none}`, topology `{Roles=[P,D], PartitionBy=none}` | Merge into one PlacementGroup; gang and topology are attributes of the same group |
| Disjoint | `Roles` have no intersection | gang `{Roles=[P,D], PartitionBy=none}`, topology `{Roles=[R], PartitionBy=none}` | Two independent groups |
| Containment | Child `Roles` ⊆ parent `Roles`, and child `PartitionBy` is equal to or finer than parent `PartitionBy` | topology `{Roles=[P,D,R], PartitionBy=none}` containing gang `{Roles=[P,D], PartitionBy=none}` | Parent/child placement tree, if the backend can render it |
| Partial overlap | `Roles` overlap without containment, or the partition relation is incompatible | gang `{Roles=[P,R], PartitionBy=none}`, topology `{Roles=[P,D], PartitionBy=none}` | **Reject before workload creation** |

`PartitionBy=none` is the coarsest partition. `role-instance-name` is finer than `none`.

Partial overlap is not a naming problem: it is a hypergraph that the target PodGroup models cannot represent, because a pod has one physical membership. All alternatives are wrong:

- create `{P,R}` and `{P,D}`: P cannot belong to two PodGroups;
- merge into `{P,D,R}`: the topology constraint incorrectly covers D;
- split into three atomic groups: neither cross-group guarantee can be expressed;
- keep one constraint: the other guarantee is silently lost.

The rejection therefore reports both declaring rules:

```text
PlacementPlanReady=False
Reason=IncompatiblePlacementGroups
Message="gang roles [prefill,router] partially overlap topology roles [prefill,decode]; neither scope contains the other"
```

Gang composition rules themselves are unchanged from KEP-430: gang-bearing rules are merged into one GangStrategy, covered roles are unioned, overlapping per-role minima take the maximum, and all-or-nothing rules cover their full role set. When no topology intent exists, the plan must compile to exactly the current single-PodGroup behavior (golden tests lock this equivalence).

#### Reconcile Flow and Failure Gating

```text
1. Read RBG + CoordinatedPolicy
2. Resolve gang intents (KEP-430 rules unchanged)
3. Resolve topology intents and role-instance partitions
4. Build and validate the PlacementPlan:
   - role existence
   - scope relation over `(Roles, PartitionBy)` (equal/disjoint/containment only)
   - topology identity consistency (`topologyName` resolves to one resource)
   - level existence and parent/child ordering
5. Compile the plan with the configured scheduler backend
6. Reconcile the rendered group objects
7. Only after rendered objects are ready, create/update Roles
8. Inject the compiler-provided pod bindings
9. Delete group objects no longer present in the rendered plan
```

If steps 2–5 fail:

- no PodGroup is created or updated;
- Role create/update is paused (reusing the KEP-430 `IncompatibleGangConfig` gating path), so new pods cannot schedule without their declared hard constraints;
- already-running pods are not evicted;
- cleanup of roles deleted by the user remains allowed;
- the declaring object gets a condition and one edge-triggered event.

### Scheduler Translation

The scheduler compiler consumes the validated PlacementPlan and renders the downstream dialect. It is the only component that creates physical group objects and emits pod bindings.

#### Translation Channels

- **RoleInstance packing** — the planner's per-instance partition renders at the subGroup level beneath the role's group: Volcano uses subGroup-level `networkTopology` with `matchLabelKeys` partitioning subGroups per instance; KAI uses per-instance subGroups nested under the rule's subGroup; **Koordinator has no instance-level form** (`network-topology-spec` must be uniform across the whole GangGroup) — instance-level constraints report unsupported on that dialect.
- **Cross-role co-location** — a PlacementGroup with topology renders at the group level: Volcano uses the PodGroup's `spec.networkTopology`; Koordinator scopes the GangGroup to the member PodGroups of the group's roles; KAI renders a subGroup that nests the group's roles and carries its own `topologyConstraint`.
- **Gang plus topology on the same scope** — both attributes are rendered on one group object. For Volcano this is one PodGroup with `minMember`/subGroup thresholds plus `networkTopology`; for KAI it is one subGroup tree. No second topology-only PodGroup is created for the same members.
- **Containment trees** — KAI can render nested subGroups. Volcano's `subGroupPolicy` is essentially flat plus per-instance partitioning, so it supports the common parent topology + per-instance child topology case, but not arbitrary cross-role containment; such plans return `SchedulerUnsupported` instead of approximating the tree.
- **Topology-only groups** — Volcano renders `minMember: 0` and omits gang threshold fields; KAI renders `minMember: 0` / `minSubGroup: 0`. The compiler may still render the label/identity fields needed to form topology subgroups.

#### Translation Matrix

| `pack` API (instance-level or cross-role) | Volcano | Koordinator gatherStrategy | KAI |
|---|---|---|---|
| `required: R` | `mode: hard` + `highestTierName: R` | `[{R, MustGather}]` | `requiredTopologyLevel: R` |
| `required: R` + `preferred: P` | `mode: hard` + `highestTierName: R`; P is **not anchored**; emit `PreferredAbsorbed` condition/warning | `[{R, MustGather}, {P, PreferGather}]` | `requiredTopologyLevel: R` + `preferredTopologyLevel: P` |
| `preferred: P` only | `mode: soft` (no tier threshold); P is **not anchored**; emit `PreferredAbsorbed` | `[{P, PreferGather}]` | `preferredTopologyLevel: P` |
| Level validation fails | Translation fails; condition/event; role update gated; no rendering | same | same |
| Scheduler lacks required capability | `SchedulerUnsupported`; no approximation | same | same |

Volcano's generic lower-tier scoring is an implementation heuristic, not a translation of the user's preferred level. It remains valuable, but the KEP does not claim equivalent semantics; scheduler-level tests document the actual scoring behavior separately from the API contract.

#### Translation Examples

Input — the objects from [Attachment Points](#attachment-points): a `pd-colocation` rule over prefill+decode with `required: block` + `preferred: rack`, prefill instance-level `required: rack`, and no gang configured (router 1 pod outside the rule; prefill 2 instances × 4 pods; decode 2 instances × 2 pods).

**Volcano** — the rule's PlacementGroup becomes one topology-bearing PodGroup. This example is topology-only, so `minMember` is zero and gang thresholds are omitted:

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: infer-0-pd-colocation    # derived from the PlacementGroup, not the gang translator
  namespace: default
  ownerReferences:
  - apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroup
    name: infer-0
    controller: true
spec:
  minMember: 0                   # topology-only: no gang requirement
  networkTopology:               # cross-role: required=block
    mode: hard
    highestTierName: block       # preferred=rack is not anchored; PreferredAbsorbed is reported
  subGroupPolicy:
  - name: prefill
    labelSelector:
      matchLabels:
        rbg.workloads.x-k8s.io/group-name: infer-0
        rbg.workloads.x-k8s.io/role-name: prefill
    matchLabelKeys:
    - rbg.workloads.x-k8s.io/role-instance-name
    networkTopology:             # each subGroup (= each instance) packs independently
      mode: hard
      highestTierName: rack
```

The router input declares no topology constraint, so it is not bound to any PodGroup. If a role outside every rule declares an instance constraint, the compiler creates a separate PodGroup for it and uses the same subGroup pattern.

Cluster HyperNodes must maintain `spec.tierName`; if the referenced name is found on no HyperNode (including clusters not maintaining tierName at all), validation fails explicitly — there is no integer `highestTierAllowed` fallback.

**Koordinator** — the cross-role rule lands on the GangGroup annotations, with the GangGroup scoped to the referenced roles' member PodGroups (PodGroups of roles outside the rule join no topology GangGroup): every member PodGroup of a GangGroup must carry **identical** `groups` and `network-topology-spec` annotations, and the gather algorithm runs over all member pods of the group:

```yaml
apiVersion: scheduling.sigs.k8s.io/v1alpha1
kind: PodGroup
metadata:
  name: infer-0-prefill-0        # one GangGroup member (4 total: prefill-0/1 + decode-0/1); all members carry identical annotations
  annotations:
    gang.scheduling.koordinator.sh/groups: '["default/infer-0-prefill-0", "default/infer-0-prefill-1", "default/infer-0-decode-0", "default/infer-0-decode-1"]'
    # required=block → MustGather@BlockLayer; preferred=rack → PreferGather@RackLayer
    gang.scheduling.koordinator.sh/network-topology-spec: |
      { "gatherStrategy": [
          {"layer": "BlockLayer", "strategy": "MustGather"},
          {"layer": "RackLayer",  "strategy": "PreferGather"} ]}
spec:
  minMember: 0                   # topology-only: no gang requirement
```

Instance-level constraints **cannot be expressed** on Koordinator: `network-topology-spec` has no per-subGroup form (unlike Volcano's `subGroupPolicy`), and divergent member specs are invalid; instance-level constraints report unsupported (`SchedulerUnsupported`).

**KAI** — subGroups nest arbitrarily, so the plan lands in a single PodGroup: the rule becomes a nested subGroup spanning prefill+decode with its own `topologyConstraint`, per-instance subGroups carry instance packing, and uncovered roles only appear as sibling subGroups when they declare their own instance-level constraints. KAI levels are node-label keys, and `topology` names the selected KAI Topology CR (`TopologyConstraint.topologyName`, or the controller default when omitted):

```yaml
apiVersion: scheduling.run.ai/v2alpha2
kind: PodGroup
metadata:
  name: infer-0
spec:
  minSubGroup: 0                 # root: topology-only — router never blocks pd-colocation
  subGroups:
  - name: pd-colocation          # the CoordinatedPolicy rule over prefill+decode
    minSubGroup: 0               # topology-only: no gang requirement
    topologyConstraint:          # cross-role
      topology: cluster-topology    # from TopologyConstraint.topologyName or controller default
      requiredTopologyLevel: topology.block
      preferredTopologyLevel: topology.rack
  - name: prefill-0              # per-instance leaf subGroups carry instance packing
    parent: pd-colocation
    minMember: 0
    topologyConstraint:
      topology: cluster-topology
      requiredTopologyLevel: topology.rack
  - name: prefill-1
    parent: pd-colocation
    minMember: 0
    topologyConstraint:
      topology: cluster-topology
      requiredTopologyLevel: topology.rack
  - name: decode-0
    parent: pd-colocation
    minMember: 0
  - name: decode-1
    parent: pd-colocation
    minMember: 0
```

`minMember` / `minSubGroup` render only where gang is configured. With a gang rule on the same scope, its KEP-430 computation fills the corresponding threshold while topology remains an attribute of the same subGroup tree.

### Observability

- **CoordinatedPolicy / RBG / RoleInstance conditions**:
  - `PlacementPlanReady` — `False` with reasons such as `IncompatiblePlacementGroups` (partially overlapping gang/topology scopes), `IncompatibleTopologyNames`, `RoleUnresolved`, `InvalidLevelOrder` (child broader than parent, or preferred broader than required).
  - `TopologyTranslated` — `False` with reasons such as `LevelUnresolved`, `TopologyResourceUnresolved` (KAI), `SchedulerUnsupported`.
  - `PreferredAbsorbed` — `True` when Volcano cannot anchor the preferred level and generic scoring is used instead.
  - `TopologyConstraintActive` — `True` after the PlacementPlan has been rendered and the first pod has been created; admission uses this marker to enforce topology immutability.
  - Each condition transition emits one warning event (edge-triggered, per the KEP-430 event-spam analysis).
- **Placement gating is explicit.** While `PlacementPlanReady` or `TopologyTranslated` is false, Role create/update is paused; status explains why no new pod was created.
- Placement outcomes themselves (which domain a group landed in) remain owned by the scheduler (e.g., Volcano's allocated-HyperNode bookkeeping); RBG status links failure *reasons* back to the declaring object.

### Dependencies

- **A scheduler with topology-aware scheduling support.** Phase 1 targets Volcano with `tierName`/subGroup-level `networkTopology` support; Koordinator and KAI land in later phases. Runtime CRD schema inspection detects field availability (same pattern as KEP-430).
- **Cluster topology objects and node labels**, maintained by administrators or discovery tools (UFM, RoCE, topograph). For label-backed dialects, nodes must carry the label keys referenced by constraints. These objects are also the reconcile-time validation source for level existence and ordering.
- **KEP-430 gang semantics and PodGroup conventions**: the placement planner consumes `ResolveGangStrategy`; the compiler renders the PodGroup/subGroup objects and pod label conventions (`group-name`, `role-name`, `role-instance-name`). No second translator may modify those resources or pod annotations.
- **CoordinatedPolicy (KEP-30)** as the cross-role coordination surface: the policy binds to its RoleBasedGroup by identical name and namespace, so a topology rule's role set always resolves against exactly one RBG.
- **KAI controller default topology name** when the KAI backend is enabled; an explicit `TopologyConstraint.topologyName` overrides it.

### Implementation Phases

- **Phase 1**: `TopologyConstraint` API (Role `instanceTopologyConstraint` + CoordinatedPolicy `scheduling.topologyConstraint`), `PlacementPlan` resolver/planner with KEP-430-equivalence golden tests, scheduler compiler interface + **Volcano** implementation, reconcile-time validation, failure gating, status conditions/events, tests.
- **Phase 2**: **Koordinator** compiler.
- **Phase 3**: **KAI** compiler; validation hardening (drift heuristics, richer diagnostics).

### Test Plan

[x] I/we understand the owners of the involved components may require updates to existing tests to make this code solid enough prior to committing the changes necessary to implement this enhancement.

#### Unit tests

1. Placement planning: equal scopes merge gang and topology; disjoint scopes stay independent; containment builds a tree; partial overlap is rejected with both declaring rules in the message. Scope comparison must include `PartitionBy`, not only `Roles`.
2. KEP-430 equivalence: gang-only inputs produce byte-for-byte the current single PodGroup rendering before topology features are enabled.
3. Pass-through rendering: per-dialect rendering of the identifier string; Volcano rejects names found on no HyperNode (no integer fallback); KAI levels use node-label keys, and `topologyName` selects the Topology CR.
4. Translation matrix: every API combination (`required`, `required+preferred`, `preferred`-only) × every dialect renders the expected dialect object; Volcano preferred cases report `PreferredAbsorbed`.
5. Reconcile-time semantic validation: unknown level identifier, ordering violation, reversed parent/child nesting, missing or inconsistent `topologyName` → correct condition reasons; recovery when the topology object or the RBG is fixed.
6. Topology-only groups: Volcano `minMember: 0`, KAI `minSubGroup: 0`, and gang threshold fields omitted unless gang is configured.
7. Mutability: topology fields can be updated before any pod exists and are rejected after the first pod is created.
8. Volcano runtime detection: CRD schema inspection for `tierName` / subGroup `networkTopology`; unmaintained-tierName and unsupported paths.

#### Integration tests

1. envtest: create scheduler topology fixtures + RBG with both attachment points, assert the rendered PodGroups (PlacementGroup-derived `spec.networkTopology`, `subGroupPolicy[].networkTopology`, and any PodGroup created for rule-external instance constraints) field-by-field.
2. Failure gating: an incompatible plan or unknown level prevents Role create/update and Pod creation; cleanup of deleted roles still works; running pods are not evicted.
3. Topology-object change triggers re-validation of referencing RBGs; level removal flips their conditions.
4. Deleting the constraint removes rendered dialect fields on the next reconcile.

#### e2e tests

1. Volcano cluster with labeled HyperNodes (tier names aligned): deploy the example RBG, verify each prefill instance's pods land within one rack-tier HyperNode and the rule's prefill+decode pods within one block-tier HyperNode.
2. Insufficient domain capacity: hard constraint → pods stay Pending, condition/event explains; soft constraint → cross-domain placement allowed.
3. Unknown tier name: explicit error surfaced, roles gated, pods not silently unconstrained.

### Graduation Criteria

- [ ] `TopologyConstraint` API defined (Role `instanceTopologyConstraint` + CoordinatedPolicy `scheduling.topologyConstraint`)
- [ ] `PlacementPlan` resolver/planner implemented, including `(Roles, PartitionBy)` scope-composition validation and KEP-430 equivalence golden tests
- [ ] `TopologyConstraint.topologyName` implemented with KAI topology-resource validation
- [ ] Topology mutability policy enforced before and after first pod creation
- [ ] Scheduler compiler interface + Volcano implementation (PodGroup + subGroup networkTopology, tier-name verification, topology-only `minMember: 0`)
- [ ] Failure gating for invalid plans and translations
- [ ] Admission + reconcile validation (syntax at admission; level existence and parent/child ordering against scheduler topology objects at reconcile)
- [ ] Status conditions and edge-triggered events on RBG/RoleInstance/CoordinatedPolicy
- [ ] Unit test coverage
- [ ] Integration test coverage
- [ ] e2e tests (Volcano environment)

### Upgrade / Downgrade Strategy

- **Upgrade**: all new fields are optional; existing RBGs and CoordinatedPolicies without topology configuration produce the same PlacementPlan and the same PodGroup rendering as KEP-430.
- **Downgrade**: removing the topology fields removes the rendered dialect fields on the next reconcile; already-scheduled pods are unaffected. Rolling the controller back to a version without this feature leaves rendered PodGroup fields in place but unmaintained — they remain valid scheduler configuration and can be cleaned up manually.

### Version Skew Strategy

`TopologyConstraint` is an additive, optional API. Older controllers ignore the new fields (constraints simply unenforced); newer controllers reading objects written by older versions see no constraints and render nothing. No control-plane/node coordination is involved beyond the scheduler's own version requirements, which are detected at runtime via CRD schema inspection.

## Drawbacks

- Cross-role co-location requires a second object: users declare instance packing on the RBG but must create a CoordinatedPolicy (same name/namespace) for PD co-location, even when no other coordination strategy is needed. Gang scheduling already established this pattern, and keeping every inter-role behavior in one object avoids two sources of truth.
- The planning layer is more explicit than letting each translator create resources directly. The compensating benefit is that gang, topology, and other placement intents have one membership owner; composition no longer depends on translator call order.
- A topology rule over a role set gathers all current and future pods of that scope into one domain. This can become a hard capacity ceiling; the limitation is explicit in this KEP.
- Volcano cannot express arbitrary nested cross-role placement trees or anchor a preferred middle tier. Those cases are reported as unsupported/degraded rather than approximated.

## Alternatives

- **Independent gang and topology translators.** Rejected: KEP-430 would create one RBG-level gang PodGroup while topology rules partition their own PodGroups. A pod can carry only one `group-name`, so overlapping scopes either lose the gang guarantee, over-constrain topology, or produce order-dependent results. Placement membership needs one owner.
- **RBG-level `spec.topologyConstraint` field.** Rejected: the constraint group "all pods of the RBG" is the wrong granularity for the driving scenario — PD-disaggregated deployments need prefill+decode co-located while roles without latency-critical cross-role traffic stay outside the domain, and an over-broad group wastes scarce in-domain capacity and can make the constraint unsatisfiable. Cross-role grouping is by definition inter-role coordination, which is exactly CoordinatedPolicy's domain. Whole-RBG co-location remains expressible by listing every role, so no expressiveness is lost.
- **Per-role topology with pairwise role references** (prefill declares "co-locate with decode"). Rejected: pairwise declarations create N² relationships, make the constraint group implicit, and complicate conflict validation; a single rule naming the full role set states the group explicitly, once.
- **Vocabulary indirection for cross-scheduler portability** (abstract level names mapped per scheduler). Rejected for now: the strings are the scheduler's own identifiers and manifests port verbatim within a scheduler family.
- **Approximating unsupported plans** (merging overlapping scopes into the union, dropping one constraint, or rendering native pod affinities). Rejected: silent semantic changes are worse than an explicit `IncompatiblePlacementGroups` or `SchedulerUnsupported` condition.

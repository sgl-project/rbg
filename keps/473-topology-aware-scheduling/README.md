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
    - [Portable Topology Vocabulary: ClusterNetworkTopology CR](#portable-topology-vocabulary-clusternetworktopology-cr)
        - [API](#api)
        - [Mode Support Matrix](#mode-support-matrix)
        - [Validation and Admission](#validation-and-admission)
        - [Controller Materialization](#controller-materialization)
        - [Status and Level Contract](#status-and-level-contract)
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
- [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
    - [Feature Enablement and Rollback](#feature-enablement-and-rollback)
    - [Rollout, Upgrade and Rollback Planning](#rollout-upgrade-and-rollback-planning)
    - [Monitoring Requirements](#monitoring-requirements)
    - [Dependencies](#dependencies)
    - [Scalability](#scalability)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

RBG orchestrates role-based, multi-node inference workloads whose pods communicate intensely: tensor/pipeline parallelism spans machines within a single serving instance, and prefill–decode disaggregation streams KV cache across roles. The usable bandwidth and latency of that communication depend strongly on physical network locality — pods inside the same rack/block enjoy far better fabric than pods crossing spine-level, oversubscribed links.

This KEP introduces topology-aware scheduling for RBG with four pieces:

1. **`ClusterNetworkTopology`** — a scheduler-independent CR that defines the topology vocabulary used by RBG workloads. In **Managed** mode, RBG declares ordered levels and materializes the scheduler-native resources (KAI `Topology`, Volcano `HyperNode` set). In **Imported** mode, RBG binds an existing scheduler topology resource and exposes its levels through status. Workloads never name a backend GVK.
2. **`TopologyConstraint`** — a group-granularity API (`pack.required` / `pack.preferred`), exposed on RoleSpec as **`InstanceTopologyConstraint`** (pack each RoleInstance's pods) and, for cross-role co-location, in **CoordinatedPolicy** rules that name an explicit set of roles (e.g., the prefill and decode roles of a PD-disaggregated deployment). `topologyName` references the RBG `ClusterNetworkTopology`; `required` and `preferred` reference levels in that CR's `status.levels`.
3. **A scheduler-independent placement planner.** Gang (KEP-430) and topology no longer each decide how physical PodGroups are partitioned. The planner first resolves one logical **PlacementGroup tree** from all placement intents; gang and topology are attributes of the same logical group. A pod can therefore have exactly one placement membership, and incompatible scope overlaps are rejected before workloads are created.
4. **A scheduler compiler.** One compiler per dialect (Volcano, Koordinator, KAI) is the only component that renders physical PodGroups and pod membership. If a dialect cannot compile a valid logical tree without semantic loss, it reports `SchedulerUnsupported` rather than approximating it.

## Motivation

Two production scenarios drive this work:

1. **Very large models require multi-node deployment.** Models such as Qwen3.8-Max no longer fit a single 8-GPU node and must run cross-machine TP/PP. Cross-machine traffic rides RDMA (IB/RoCE), where achievable bandwidth and latency are dictated by physical network position: communication inside one ToR/Block is fast; crossing the Spine degrades sharply. Every pod of one inference instance must therefore be packed into a single high-performance network domain.
2. **Prefill–decode (PD) disaggregated inference.** Prefill and decode transfer large, latency-critical KV cache volumes that directly determine TTFT. Co-locating prefill and decode pods inside the same network domain yields deterministic inference performance. Such deployments typically include further roles without latency-critical cross-role traffic, so co-location is a property of a **role subset**, not of the whole RBG.

The native kube-scheduler cannot express either need:

- Node labels form **flat** topology domains (`kubernetes.io/hostname`, `topology.kubernetes.io/zone`, custom keys) with no hierarchy or distance semantics — "same rack beats same block beats cross-spine" is inexpressible.
- Scheduling decisions are **per-pod and greedy**; there is no atomic, group-granularity placement for a set of pods that must land together.

Schedulers (Volcano, Koordinator, KAI) each built topology models that re-interpret node topology as a **domain tree plus group-granularity placement**, but each speaks a different dialect: Volcano uses tiers (and names) on a `HyperNode` tree, Koordinator uses named `topologyLayer`s, KAI uses levels backed by node-label keys, optionally referenced through `TopologyLevel.alias`. RBG, as the workload API, must let users state topology intent once and have it honored on any of these schedulers.

RBG also already has gang scheduling (KEP-430). Gang and topology are different dimensions of the same placement problem:

- **Gang answers when a logical group may be dispatched** (`minMember`, `minSubGroups`).
- **Topology answers where the members of that group must land** (`networkTopology`, gather strategies).

Because a Volcano/Koordinator/KAI pod can belong to only one physical PodGroup, gang and topology must not independently choose PodGroup membership. This KEP therefore places membership selection in a scheduler-independent planner, and physical rendering in the scheduler compiler.

### Goals

1. **Declarative group–domain affinity.** Express topology intent with group-granularity semantics — the pod group of one RoleInstance, or of an explicit role set, is the atomic placement unit and a common performance domain is selected on the topology tree. Cover both scenarios (RoleInstance packing, cross-role co-location over selected roles) with hard (`required`) and soft (`preferred`) constraint semantics.
2. **Composability with gang scheduling.** Gang and topology intents with the same scope compose on one placement group. Disjoint scopes remain independent. Containment scopes form a parent/child placement tree when a backend can render it. Partially overlapping scopes are rejected explicitly, before pod creation, because no target scheduler can represent a pod with two parents.
3. **Propagation along the resource chain.** Constraints are declared at the Role level (instance packing) and in CoordinatedPolicy rules (cross-role co-location), then propagated along RBG → Role → RoleInstanceSet → RoleInstance → Pods, surfacing finally as pod scheduling constraints; users never configure pods individually.
4. **Portable topology vocabulary.** RBG workloads reference `ClusterNetworkTopology` and its status levels, not Volcano `tierName`, Koordinator `topologyLayer`, or KAI `nodeLabel`/`alias` resources. Managed topology definitions are scheduler-independent; imported topology is an explicit compatibility boundary whose levels are surfaced through RBG status. Schedulers without topology capability report "unsupported" explicitly.
5. **Lifecycle coverage.** Pods created by scale-out, rolling update, or failure recovery still satisfy the declared constraints (no constraint drift).
6. **Observable and diagnosable.** Placement-plan validity, translation fidelity, constraint satisfaction, and failure reasons (insufficient domain capacity, unsupported level, missing scheduler capability, incompatible scopes) are queryable from the RBG's status and events, which name the declaring CoordinatedPolicy rule, RoleSpec, or RoleInstance.

### Non-Goals

1. **No anti-affinity.** Spreading replicas across topology domains (disaster-tolerance dispersion) is out of scope for this KEP.
2. **No placement algorithms.** Domain selection, bin-packing, and capacity accounting belong to downstream schedulers; RBG only declares constraints, plans logical membership, and compiles scheduler objects.
3. **No topology discovery ownership.** RBG does not discover physical topology or assign node labels. Administrators or discovery tools (UFM, RoCE topology exporters, topograph) maintain node labels and, for Imported mode, the native scheduler CR. In Managed mode RBG owns only the scheduler resources it generates from those labels.
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

Topology-aware scheduling is built in four layers:

**Topology vocabulary layer — `ClusterNetworkTopology`.** A cluster-scoped RBG CR defines the topology names that workload manifests are allowed to use. It has two mutually exclusive modes:

- **Managed**: the user declares an ordered list of levels (`zone`, `block`, `rack`, `host`) and the label key that partitions nodes at each level. The controller materializes the active scheduler's native resources.
- **Imported**: the user binds an existing native topology resource by name. The controller reads it, validates the mode/name constraints, and writes the available levels into status.

A mutating webhook stamps the active scheduler into `spec.scheduler` on first create; the field is immutable. This prevents a topology from silently changing meaning when the cluster's active scheduler changes. A validating webhook enforces mode exclusivity and immutability. The controller adds a finalizer, reconciles backend resources or bindings, and publishes `Ready`, `NativeTopologyDrift`, and the level list in status.

**User layer — `TopologyConstraint` on Role and CoordinatedPolicy.** Users declare `pack.required` / `pack.preferred` using names from `status.levels` of the referenced `ClusterNetworkTopology`. All *inter-role* coordination, topology included, lives in CoordinatedPolicy (the object that already carries coordinated rolling update, scaling, and gang); *intra-role* packing stays on the role itself. Attachment points:

- **Role level** (`spec.roles[].instanceTopologyConstraint`): constraint group = one RoleInstance's pods; every instance picks its domain independently (instance packing). The field is pattern-independent and applies equally to `LeaderWorkerPattern` and `CustomComponentsPattern` roles.
- **Cross-role level** (`spec.policies[].strategy.scheduling.topologyConstraint` on CoordinatedPolicy): constraint group = all pods of the roles named in the rule's `roles` list (cross-role co-location). Roles not listed stay outside the cross-role domain (their own instance-level constraints still apply). Listing every role of the RBG recovers whole-group co-location, so no dedicated RBG-level field exists.

**Placement planning layer.** Before any scheduler object is created, the controller resolves gang intents (KEP-430 semantics are unchanged), topology intents, and role-instance partitioning into one **PlacementPlan**. The planner is the only owner of logical membership: equal scopes merge gang and topology attributes; disjoint scopes produce sibling groups; containment scopes produce a tree; partially overlapping scopes are rejected. This removes order dependence between gang and topology translation and prevents a pod from being assigned two different physical PodGroups.

**Scheduler compiler.** Per scheduler, a compiler consumes the valid PlacementPlan plus the resolved `ClusterNetworkTopology` status and renders the dialect — Volcano `networkTopology` at PodGroup/subGroup level, Koordinator `gatherStrategy`, KAI topology constraints — while preserving KEP-430 gang behavior when no topology is configured. Levels are validated against the RBG topology CR's status and native resource state; failures (unknown level, topology not ready, incapable scheduler, incompatible placement scopes) are explicit: status conditions plus events, never silent degradation.

### Notes/Constraints/Caveats

- **Group objects are the physical carrier, but not the semantic model.** Schedulers only offer group-granularity placement through PodGroup-like objects. A role or policy rule with topology constraints therefore gets group objects even when gang scheduling is not configured. Topology-only groups carry **no gang requirement**: on Volcano they render `minMember: 0` and omit gang threshold fields such as `minSubGroups`; on KAI they render `minMember: 0` / `minSubGroup: 0`. Gang fields render only for roles covered by gang-configured rules.
- **One membership owner.** Gang and topology resolvers declare intents only; they do not create PodGroups, choose PodGroup names, or inject `scheduling.k8s.io/group-name`. Only the scheduler compiler outputs objects and the unique pod bindings derived from the PlacementPlan.
- **Gather-only semantics.** The API models "gather into a common domain" (聚). "Spread across domains" (散) is deliberately excluded.
- **The constraint group is exactly the rule's role set.** Referenced roles join one common domain; unreferenced roles stay outside it (their own instance-level constraints still apply). A role may appear in at most one topology-bearing rule because placement membership partitions pods; overlap is rejected by the CoordinatedPolicy validating webhook, following the `gang.minReplicas` validation pattern. Gang and topology scopes may additionally be equal, disjoint, or nested, but not partially overlapping.
- **Soft semantics are approximate on Volcano.** Volcano cannot anchor a soft target at a middle tier. `required: R` plus `preferred: P` renders `mode: hard` with `highestTierName: R`; the preferred target is not represented as a tier threshold. This is an explicit fidelity loss reported as `PreferredAbsorbed`, not a claim that scoring is equivalent to anchoring at `P`.
- **Volcano is materialized, not imported.** RBG Managed mode creates a set of HyperNodes from node labels; Imported mode is unsupported because there is no single topology resource to bind. `HyperNode.spec.tierName` is always the RBG level name, and integer `highestTierAllowed` is never rendered.
- **Koordinator is import-only.** Koordinator's scheduler reads only the `ClusterNetworkTopology` named `default`; RBG Managed mode could conflict with cluster-administrator ownership, so it is unsupported. A topology binding must reference `default`.
- **KAI uses Topology-backed levels.** In Managed mode, RBG writes `TopologyLevel.alias = ClusterNetworkTopology.spec.levels[].name`. In Imported mode, RBG reads the external Topology and exposes each level's alias when present, otherwise its `nodeLabel`, through status. KAI alias/nodeLabel drift is surfaced rather than silently remapped.
- **All topology CRs are pinned to one scheduler.** `spec.scheduler` is stamped on create and immutable. If the active scheduler later differs, the controller skips backend reconcile and reports `SchedulerMismatch`; it does not silently reinterpret the same native resource name under a new scheduler.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Users mistype a level identifier (no schema can catch a wrong string) | The workload controller resolves `topologyName` to a Ready `ClusterNetworkTopology` and validates `required`/`preferred` against `status.levels`; failures surface `TopologyTranslated=False` and name the declaring CoordinatedPolicy rule or RoleSpec before any pod group is rendered |
| Gang and topology rules have partially overlapping scopes, so no backend can preserve both guarantees | Placement planning normalizes scopes before rendering and rejects partial overlap with `TopologyTranslated=False` / `IncompatiblePlacementGroups`; role create/update is gated |
| Scheduler has no topology capability (including the native kube-scheduler) | Hard constraints report `SchedulerUnsupported` explicitly; always surfaced in status — never silently dropped or approximated by per-pod affinity |
| Imported topology drift (a level renamed/removed, or a native CR deleted and recreated with the same name) | The ClusterNetworkTopology controller watches its bound resource and UID, updates `status.levels`, and reports `NativeTopologyDrift`/`NativeTopologyNotFound`; workload reconcile gates on `Ready` and names the invalid level |
| Managed topology resources are externally modified | The controller owns generated resources and restores the desired state; while a mismatch exists it reports `NativeTopologyDrift` and does not mark the topology Ready |
| Active scheduler changes after a topology CR is created | `spec.scheduler` is immutable and was stamped at create; reconcile reports `SchedulerMismatch` and skips backend reconciliation instead of reinterpreting the topology under a new scheduler |
| Volcano version too old or its topology CRD is unavailable (no `tierName`, no subGroup-level `networkTopology`) | Runtime CRD schema detection (same pattern as KEP-430's `hasSubGroupPolicy` check); explicit unsupported |
| Constraints guaranteed only for newly created pods; already-running pods may drift from the constraint after external rescheduling | Placement validation gates role create/update, so new/updated pods never run unconstrained. Reconcile re-renders objects for scale-out/rollout/recovery. Running pods are not evicted; drift of *running* pods is left to the scheduler's own mechanisms and documented |
| Topology declarations are changed after the workload contract is accepted | V1alpha2 validating admission rejects in-place additions, removals, and changes; changing topology requires deleting and recreating the workload |
| A user bypasses v1alpha2 validating admission (for example, through a v1alpha1 conversion round trip or by disabling webhooks) | This is outside the controller's enforcement boundary; the user is responsible for using the v1alpha2 API with validating webhooks enabled |
| Topology identity is missing, not Ready, or inconsistent across one RBG | `TopologyConstraint.topologyName` must reference one Ready RBG `ClusterNetworkTopology`; missing/not-ready/mismatched names report `TopologyUnresolved` / `TopologyNotReady` / `IncompatibleTopologyNames` and block rendering |
| A topology rule names a role absent from the RBG, or a role appears in two topology-bearing rules | Admission checks rule-internal syntax (same webhook pattern as `gang.minReplicas`); role existence is verified at reconcile against the RBG (a policy may be written before the RBG exists) and reported as `TopologyTranslated=False` (`RoleUnresolved`) naming the offending role |

## Design Details

### Background: Scheduler Topology Dialects

All target schedulers model the data-center network as a **domain tree** — Cluster → Region/Zone → Spine → Block/Leaf → Rack/ToR → Node → Device — where a lower lowest-common-ancestor means higher bandwidth and lower latency, and higher links are typically oversubscribed. They differ in how levels are named and how constraints are declared:

| Scheduler | Topology model | Level coordinate | Constraint declaration |
|---|---|---|---|
| Volcano | `HyperNode` CRD tree | integer `spec.tier`, optional `spec.tierName` | `PodGroup.spec.networkTopology`: `mode: hard` + `highestTierAllowed`/`highestTierName` (mutually exclusive); `mode: soft` carries no threshold; `subGroupPolicy[]` entries accept their own `networkTopology` |
| Koordinator | `ClusterNetworkTopology` CRD | named `topologyLayer` (e.g., `BlockLayer`) + node `labelKey` | PodGroup/GangGroup annotation `gang.scheduling.koordinator.sh/network-topology-spec` with per-layer `gatherStrategy`: `MustGather` / `PreferGather` |
| KAI | `Topology` CRD (`spec.levels`) | level `nodeLabel` (e.g., `topology.rack`) or `TopologyLevel.alias` | PodGroup `topologyConstraint.requiredTopologyLevel` / `preferredTopologyLevel`; the constraint also names the `Topology` CR; subGroups nest arbitrarily and support their own constraints |

Key structural facts the design exploits:

- Every dialect can expose an ordered list of **string level names**: Volcano `tierName`, Koordinator `topologyLayer`, and KAI alias/nodeLabel. RBG can therefore define a portable vocabulary in `ClusterNetworkTopology` and map it to each scheduler without exposing backend GVKs in workload manifests.
- Koordinator and KAI natively support two-tier (required + preferred) declarations. Volcano hard mode has no middle-tier soft anchor; preferred is reported as absorbed rather than translated (see Translation Matrix).
- Volcano's `subGroupPolicy` (used by KEP-430 gang translation, partitioned per RoleInstance via `matchLabelKeys`) accepts per-subGroup `networkTopology`, giving instance-granularity constraints for this dialect.

### Portable Topology Vocabulary: ClusterNetworkTopology CR

RBG workloads must not name Volcano `HyperNode.tierName`, Koordinator `ClusterNetworkTopology.topologyLayer`, or a KAI `nodeLabel`/`alias` as their API contract. Those identifiers are scheduler vocabulary and make the workload API change meaning when the active scheduler changes. The mapping instead lives in a new RBG CR:

```text
RBG workload
  -> ClusterNetworkTopology topologyName + status.levels
     -> controller materializes or binds scheduler-native topology resources
        -> scheduler compiler renders dialect-specific PodGroup constraints
```

The CR is cluster-scoped. Its kind intentionally has the same name as Koordinator's CR but a different API group (`topology.x-k8s.io`); tools and examples must always display the full `apiVersion` to avoid ambiguity.

#### API

Managed mode — RBG owns the generated scheduler resources:

```yaml
apiVersion: topology.x-k8s.io/v1alpha1
kind: ClusterNetworkTopology
metadata:
  name: gpu-fabric
spec:
  # Written by RBG's mutating webhook from the controller's active-scheduler
  # configuration on first create; immutable afterwards. Users do not set it.
  scheduler: KAI
  levels:
    - name: zone
      nodeLabelKey: topology.kubernetes.io/zone
    - name: block
      nodeLabelKey: topology.example.com/block
    - name: rack
      nodeLabelKey: topology.example.com/rack
    - name: host
      nodeLabelKey: kubernetes.io/hostname
```

`levels` is ordered from broadest to narrowest. The array order is the only depth representation; there is no separate depth field.

Imported mode — RBG binds an externally maintained resource and never modifies it:

```yaml
apiVersion: topology.x-k8s.io/v1alpha1
kind: ClusterNetworkTopology
metadata:
  name: koordinator-default
spec:
  scheduler: Koordinator
  topologyBinding:
    topologyReference: default
```

`topologyReference` is only a name. It does not contain a GVK, so users do not need to understand scheduler resource types. The controller resolves it according to the immutable `spec.scheduler` and records the actual GVK, name, and UID in status.

```go
type ClusterNetworkTopologySpec struct {
	// Scheduler is stamped from the controller's active-scheduler
	// configuration by a mutating webhook on create and is immutable.
	// +kubebuilder:validation:Enum=KAI;Koordinator;Volcano
	Scheduler SchedulerType `json:"scheduler"`

	// Levels declares a managed topology. It is mutually exclusive with
	// TopologyBinding and immutable after create.
	// +optional
	// +listType=map
	// +listMapKey=name
	Levels []TopologyLevelSpec `json:"levels,omitempty"`

	// TopologyBinding references an externally managed scheduler topology
	// resource. It is mutually exclusive with Levels and immutable after create.
	// +optional
	TopologyBinding *TopologyBindingSpec `json:"topologyBinding,omitempty"`
}

type TopologyLevelSpec struct {
	// Name is the portable level name used by RBG workloads.
	// +required
	Name string `json:"name"`

	// NodeLabelKey partitions nodes at this level. A node participates in a
	// domain only when it has a non-empty value for this key.
	// +required
	NodeLabelKey string `json:"nodeLabelKey"`
}

type TopologyBindingSpec struct {
	// TopologyReference is the native resource name, interpreted according to
	// spec.scheduler. Koordinator accepts only "default".
	// +required
	TopologyReference string `json:"topologyReference"`
}
```

#### Mode Support Matrix

| Scheduler | Managed | Imported | Binding name | Reason |
|---|---|---|---|---|
| KAI | Yes | Yes | any `Topology` CR name | KAI supports multiple Topology CRs and workload-level topology selection |
| Koordinator | No | Yes | must be `default` | the scheduler reads only the `ClusterNetworkTopology` named `default`; RBG must not take ownership of an administrator's global resource |
| Volcano | Yes | No | n/a | topology is a set of HyperNodes, not a single bindable resource |

Managed topology definitions are portable across schedulers as long as the same `levels[].name` values are declared. Imported topology is deliberately an explicit compatibility boundary: its status levels are read from the external CR, but the workload API still references only the RBG CR and status, never a backend GVK.

#### Validation and Admission

A mutating webhook is required for topology CR creation:

1. read the active scheduler from controller configuration;
2. if `spec.scheduler` is empty, write the active scheduler;
3. if a user-provided value differs from the active scheduler, reject the create;
4. reject creation if the active scheduler is unknown.

A validating webhook enforces:

- `spec.levels` and `spec.topologyBinding` are mutually exclusive and exactly one must be set;
- `spec.scheduler`, `spec.levels`, and `spec.topologyBinding` are immutable;
- managed level names and node label keys are non-empty and unique;
- a level name must not equal any node label key in the same topology (KAI alias validation);
- `kubernetes.io/hostname` may only be the last level;
- the number of levels is within backend limits (KAI's current limit is 16);
- Koordinator imported topology must reference exactly `default`;
- Volcano imported topology is rejected as `ModeUnsupported`.

Validation that requires reading Nodes or backend CRs happens in reconcile, not admission. This keeps admission self-contained and allows topology CRs to be created before all nodes are labeled.

#### Controller Materialization

The controller adds a finalizer to every `ClusterNetworkTopology`. It removes the finalizer only after workload references are gone and, for Managed mode, generated backend resources are deleted.

**KAI Managed** creates or updates one KAI `Topology` CR:

```yaml
apiVersion: kai.scheduler/v1alpha1
kind: Topology
metadata:
  name: gpu-fabric
  ownerReferences:
  - apiVersion: topology.x-k8s.io/v1alpha1
    kind: ClusterNetworkTopology
    name: gpu-fabric
    controller: true
spec:
  levels:
    - nodeLabel: topology.kubernetes.io/zone
      alias: zone
    - nodeLabel: topology.example.com/block
      alias: block
    - nodeLabel: topology.example.com/rack
      alias: rack
    - nodeLabel: kubernetes.io/hostname
      alias: host
```

The native resource normally uses the RBG CR name. If that name is already occupied by an unowned resource, reconcile reports `NativeTopologyConflict` and does not overwrite it.

**KAI Imported** reads the referenced `Topology` and reverse-maps each level:

```text
status.level = alias if non-empty, otherwise nodeLabel
```

The bound resource is read-only. If an alias changes, `status.levels` changes and the CR reports drift; workloads that reference the old name fail validation instead of being silently remapped. If the native CR is deleted and recreated with the same name, the recorded UID mismatch reports drift/not-found semantics rather than treating it as the same object.

**Koordinator Imported** reads only the `scheduling.koordinator.sh/v1alpha1 ClusterNetworkTopology` named `default` and writes each `networkTopologySpec[].topologyLayer` into `status.levels`. Koordinator's per-layer `labelKey` array is not part of the workload contract. Its any-of semantics remain owned by Koordinator; users who need to diagnose it inspect the native CR. Managed mode is unsupported to avoid contending for the global `default` object.

**Volcano Managed** materializes a forest of HyperNodes from Node labels:

1. list Nodes and read the label value for every non-host level;
2. a Node participates only when every non-host level label is non-empty; excluded Nodes emit a warning event;
3. build the domain path from broadest to narrowest, for example `zone-a/block-b/rack-r1`;
4. create one HyperNode per unique path, with:
   - `spec.tierName = spec.levels[].name`;
   - integer `tier` derived from level order (leaf is tier 1, each broader level increments by one);
   - leaf members of type `Node`;
   - parent members of type `HyperNode` referencing the child domain;
   - an ownerReference and RBG labels on the ClusterNetworkTopology;
5. name HyperNodes deterministically from the topology name, level name, and a hash of the full domain path, so the same rack name under different zones never collapses;
6. watch Nodes and generated HyperNodes:
   - label changes update memberships;
   - stale domains are deleted;
   - external edits are restored;
   - tier/tierName collisions with non-owned HyperNodes report `NativeTopologyConflict`.

Volcano Imported mode is unsupported because there is no single resource whose GVK/name/UID can represent the whole tree.

#### Status and Level Contract

`status` is the contract used by workload reconciliation. It is deliberately minimal and has no `phase`; `Ready` plus `reason` is richer and avoids a second source of truth.

```go
type ClusterNetworkTopologyStatus struct {
	// ObservedGeneration is the ClusterNetworkTopology generation processed
	// by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Mode is Managed or Imported.
	Mode TopologyMode `json:"mode,omitempty"`

	// ActiveScheduler is the scheduler currently configured in the RBG
	// controller. It is compared with spec.scheduler during reconcile.
	ActiveScheduler SchedulerType `json:"activeScheduler,omitempty"`

	// Levels is the ordered workload contract, broadest first.
	Levels []string `json:"levels,omitempty"`

	// Resources records the bound or generated native resource(s).
	Resources []NativeTopologyResourceStatus `json:"resources,omitempty"`

	// Conditions includes Ready and NativeTopologyDrift.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type NativeTopologyResourceStatus struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	UID        types.UID `json:"uid,omitempty"`
	Managed    bool `json:"managed,omitempty"`
	NativeGeneration int64 `json:"nativeGeneration,omitempty"`

	// Volcano aggregate fields; unused by single-resource backends.
	NamePrefix string `json:"namePrefix,omitempty"`
	Count      int32 `json:"count,omitempty"`
}
```

```yaml
status:
  observedGeneration: 1
  mode: Managed
  activeScheduler: KAI

  # Ordered from broadest to narrowest. Array order is the depth.
  levels:
    - zone
    - block
    - rack
    - host

  resources:
    - apiVersion: kai.scheduler/v1alpha1
      kind: Topology
      name: gpu-fabric
      managed: true
      nativeGeneration: 1

  conditions:
    - type: Ready
      status: "True"
      reason: TranslationSucceeded
      message: "Topology has been translated to kai.scheduler/v1alpha1 Topology gpu-fabric"
      lastTransitionTime: "2026-10-15T08:00:00Z"
```

Imported resources additionally record `uid` so a same-name recreation cannot be mistaken for the original object. Volcano records an aggregate entry rather than listing every HyperNode:

```yaml
resources:
  - apiVersion: topology.volcano.sh/v1alpha1
    kind: HyperNode
    managed: true
    namePrefix: rbg-gpu-fabric-
    count: 538
```

`status.levels` contains only strings. It intentionally does not expose `nodeLabelKeys`, backend level types, or domain counts. If diagnostics are needed later, they belong in a separate optional `levelDetails` field rather than in the workload contract.

The controller maintains these conditions:

| Condition | Reason | Meaning |
|---|---|---|
| `Ready=True` | `TranslationSucceeded` | managed resources exist and match spec |
| `Ready=True` | `NativeTopologyBound` | imported resource exists and its levels are valid |
| `Ready=True` + `NativeTopologyDrift=True` | `NativeResourceModified` | imported resource changed but remains valid; status.levels was remapped, and workloads referencing removed names fail validation |
| `Ready=False` | `InvalidSpec` | admission contract violated or backend topology is structurally invalid |
| `Ready=False` | `ModeUnsupported` | scheduler does not support Managed/Imported for this CR |
| `Ready=False` | `SchedulerMismatch` | immutable `spec.scheduler` differs from active scheduler; backend reconcile is skipped |
| `Ready=False` | `NativeTopologyNameInvalid` | Koordinator binding is not `default` |
| `Ready=False` | `NativeTopologyNotFound` | bound native resource does not exist |
| `Ready=False` | `NativeTopologyConflict` | native name/tierName is owned externally or by another RBG topology |
| `Ready=False` | `NativeTopologyInvalid` | native levels are empty, duplicated, unordered, or exceed backend limits |
| `Ready=False` | `NativeTopologyDrift` | generated or imported topology no longer matches the status contract |
| `Ready=False` | `TopologyInUse` | deletion is blocked while workloads reference the topology |
| `NativeTopologyDrift=True` | `NativeResourceModified` / `ManagedResourceModified` | imported resource changed or managed resource was externally edited |

Workload reconciliation requires `Ready=True` and validates `TopologyConstraint.pack.required` / `preferred` against `status.levels`. The array order is used for required/preferred and parent/child ordering checks. A workload is never rendered against a native scheduler CR directly.

### Core API: TopologyConstraint

```go
// TopologyConstraint defines topology placement requirements.
type TopologyConstraint struct {
	// TopologyName references the RBG ClusterNetworkTopology CR that owns the
	// level vocabulary. It is not a KAI/Volcano/Koordinator resource name.
	// A non-nil TopologyConstraint must set it.
	// +required
	TopologyName *string `json:"topologyName"`

	// Pack specifies topology packing constraints for each replica of the resource.
	// A non-nil TopologyConstraint must set at least one of Pack.required or
	// Pack.preferred.
	// +optional
	Pack *TopologyPackConstraint `json:"pack,omitempty"`
}

type TopologyPackConstraint struct {
	// Required names a level in the referenced ClusterNetworkTopology's
	// status.levels. The workload will not be scheduled if this constraint
	// cannot be satisfied. Generally, it is easier for the scheduler to satisfy
	// constraints on topology domains with larger compute capacity (for example
	// zone or block) than smaller domains (for example rack or host).
	// +optional
	Required *string `json:"required,omitempty"`

	// Preferred names a best-effort level in the referenced
	// ClusterNetworkTopology's status.levels. The scheduler may fall back to
	// Required when the preferred domain cannot be satisfied.
	// +optional
	Preferred *string `json:"preferred,omitempty"`
}
```

Admission validates that a non-nil `TopologyConstraint` sets `topologyName` and at least one of `pack.required` or `pack.preferred`; `{}`, `topologyName` only, and `pack: {}` are rejected. `topologyName` is a cluster-scoped RBG CR name and is validated at reconcile against `ClusterNetworkTopology.status`.

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
	// the enclosing policy rule's roles field. Roles not listed are
	// unconstrained by this rule; list every role of the RoleBasedGroup for
	// whole-group co-location.
	// +optional
	TopologyConstraint *TopologyConstraint `json:"topologyConstraint,omitempty"`
}
```

### Attachment Points

First create the topology vocabulary. This example is Managed, so the same `zone/block/rack/host` names can be materialized for KAI or Volcano without changing workload manifests:

```yaml
apiVersion: topology.x-k8s.io/v1alpha1
kind: ClusterNetworkTopology
metadata:
  name: gpu-fabric
spec:
  scheduler: KAI
  levels:
    - name: zone
      nodeLabelKey: topology.kubernetes.io/zone
    - name: block
      nodeLabelKey: topology.example.com/block
    - name: rack
      nodeLabelKey: topology.example.com/rack
    - name: host
      nodeLabelKey: kubernetes.io/hostname
```

Then reference it from the workload:

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
      topologyName: gpu-fabric       # prefill instance converge into a single rack
      pack:
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
          topologyName: gpu-fabric
          pack:
            preferred: rack
            required: block
```

`InstanceTopologyConstraint` attaches to the Role (group = one RoleInstance's pods, each instance choosing its domain independently); `TopologyConstraint` attaches to a CoordinatedPolicy rule (group = all pods of the rule's `roles`; list every role to co-locate the whole RBG). The example values (`rack`, `block`) are the level names in `ClusterNetworkTopology.status.levels`, not scheduler dialect fields.

All topology constraints in one PlacementPlan must resolve to the same `ClusterNetworkTopology` name. Mixing multiple topology CRs in one plan is rejected as `IncompatibleTopologyNames` in this KEP.

### Mutability and Update Semantics

`ClusterNetworkTopology` is also a launch-time contract:

- `spec.scheduler`, `spec.levels`, and `spec.topologyBinding` are immutable;
- the mutating/validating webhooks enforce that contract;
- the controller adds a finalizer to every topology CR;
- while any workload still references the CR, deletion reports `TopologyInUse`;
- deleting a Managed topology first removes generated backend resources;
- deleting an Imported topology removes only the RBG CR and never the external native resource.

Changing a topology requires creating a new `ClusterNetworkTopology` and a new workload lifecycle. The active workload cannot be silently moved to a changed topology vocabulary.

The workload topology declaration is immutable for the RBG lifecycle: topology cannot be added, removed, or changed in place on a Role or CoordinatedPolicy rule. Ordinary changes to roles that do not carry topology remain allowed.

Admission compares the old and new topology declarations directly. An RBGSet template likewise compares its old and new topology declarations, so parent validation is self-contained and requires no cross-resource child lookup.

**Contract freeze and late policy creation.** The combined RBG/CoordinatedPolicy topology contract freezes when the controller first accepts a placement plan for the RBG, before it creates or updates Roles. On that first acceptance the controller records the accepted topology contract (a canonical identifier of the topology declarations, including `none`) on the RBG.

The freeze-time policy lookup must not rely on the informer cache. Ordinary policy reconciliation may continue to use the cache-backed client, but the first contract resolution performs a direct API-server read with the controller's uncached `apiReader`. A read error prevents the freeze and requeues without creating or updating Roles. If the direct read returns `NotFound` while the RBG is still within a 60-second creation settling window, the controller also requeues and does not record `none`; the RBG reports `TopologyTranslated=False` with reason `TopologyPolicySettling`. Only after the settling window has elapsed and a second direct read still returns `NotFound` may the controller freeze the `none` contract. A policy create event during the window re-enqueues immediately and is resolved through the same uncached path.

With this guard, a CoordinatedPolicy persisted before or during the settling window is normally accepted before the RBG records its contract. The settling window is intentionally bounded: it reduces same-batch GitOps races but is not a global ordering guarantee. If a topology-bearing policy first becomes visible after the RBG has already accepted `none`, or if the recorded topology differs from the currently resolved declarations, reconcile reports `TopologyTranslated=False` with reason `TopologyPolicyLate`, creates no PodGroup, and gates Role create/update. The late policy is not applied to running pods; deleting and recreating the RBG (with the policy already present) starts a new lifecycle. Operators that require strict launch ordering should create and wait for the topology-bearing CoordinatedPolicy before creating the RBG. Removing all topology after a non-empty contract is treated the same way. Role-level constraints cannot arrive late because RBG admission makes the Role declaration immutable from create onward.

Topology immutability is enforced by v1alpha2 validating admission. Delete-and-recreate of a topology-bearing CoordinatedPolicy cannot bypass that contract while its matching RBG is active: the controller protects the active policy with a finalizer. Release is keyed to the matching RBG's **UID**, not merely its name, so a GitOps delete-and-recreate of an RBG with the same name cannot satisfy the release condition for the old lifecycle. A terminating policy whose recorded UID still matches the active RBG remains authoritative because validating admission continues to reject topology changes on update requests (Kubernetes itself permits updates to deleting objects; it only prohibits adding finalizers). If a terminating policy's recorded UID no longer matches the RBG (for example after a same-name recreate), the planner ignores it, the replacement RBG never inherits the old contract, and the controller releases the finalizer once that recorded UID is deleted. The finalizer otherwise remains until the UID-recorded RBG is deleted.

Deployments that disable validating webhooks explicitly opt out of this guarantee; the controller then renders the latest desired topology instead of reconstructing historical admission state. Clusters that use topology constraints should run with validating webhooks enabled. V1alpha1 does not model the new topology fields, so a v1alpha1 read-then-write round trip that removes or changes them is also outside this enforcement boundary and is a user error.

Escape hatch and rollback: the finalizer can be removed manually with `kubectl patch --type=merge -p '{"metadata":{"finalizers":[]}}'` on a policy that is stuck after an operator error or controller rollback. This is documented as bypassing the topology immutability guarantee and requires the operator to confirm that the RBG is no longer governed by the old policy.

To change topology, delete and recreate the affected workload (and, when needed, its topology-bearing CoordinatedPolicy). Rolling updates that change topology constraints are out of scope for this KEP. Running pods are never migrated or evicted by this controller.

If the referenced `ClusterNetworkTopology` is missing, not Ready, or its status level list has changed so that a workload level no longer exists, workload reconcile reports `TopologyUnresolved`, `TopologyNotReady`, or `LevelUnresolved`. In all cases new pod creation is gated, and already-running pods are not evicted or migrated. The topology controller watches its bound/imported resource and generated resources; workload-driven reconcile still revalidates `Ready` and `status.levels` before rendering.

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
TopologyTranslated=False
Reason=IncompatiblePlacementGroups
Message="gang roles [prefill,router] partially overlap topology roles [prefill,decode]; neither scope contains the other"
```

Gang composition rules themselves are unchanged from KEP-430: gang-bearing rules are merged into one GangStrategy, covered roles are unioned, overlapping per-role minima take the maximum, and all-or-nothing rules cover their full role set. When no topology intent exists, the plan must compile to exactly the current single-PodGroup behavior (golden tests lock this equivalence).

#### Reconcile Flow and Failure Gating

```text
1. Read RBG + CoordinatedPolicy
2. Resolve all TopologyConstraint.topologyName values to ClusterNetworkTopology CRs
3. Require every referenced topology to be Ready
4. Resolve gang intents (KEP-430 rules unchanged)
5. Resolve topology intents and role-instance partitions
6. Build and validate the PlacementPlan:
   - role existence
   - all constraints resolve to one topology name
   - level existence against status.levels
   - scope relation over (Roles, PartitionBy) (equal/disjoint/containment only)
   - preferred/required ordering and parent/child ordering from status.levels order
7. Compile the plan with the active scheduler backend
8. Reconcile the rendered group objects
9. Only after rendered objects are ready, construct the Role templates and inject the compiler-provided pod bindings into those templates
10. Create/update the already-bound Roles
11. Delete group objects no longer present in the rendered plan
```

If steps 2–7 fail:

- no PodGroup is created or updated;
- Role create/update is paused (reusing the KEP-430 `IncompatibleGangConfig` gating path), so new pods cannot schedule without their declared hard constraints;
- already-running pods are not evicted;
- cleanup of roles deleted by the user remains allowed;
- the RBG gets a condition and one edge-triggered event naming the declaring rule or role.

### Scheduler Translation

The scheduler compiler consumes the validated PlacementPlan, the referenced `ClusterNetworkTopology.status.levels`, and the native-resource reference recorded in topology status. It is the only component that creates physical group objects and emits pod bindings. The compiler maps portable level names to the active scheduler's dialect; workload objects never contain a backend GVK.

#### Translation Channels

- **RoleInstance packing** — the planner's per-instance partition renders at the subGroup level beneath the role's group: Volcano uses subGroup-level `networkTopology` with `matchLabelKeys` partitioning subGroups per instance; KAI uses per-instance subGroups nested under the rule's subGroup; **Koordinator has no instance-level form** (`network-topology-spec` must be uniform across the whole GangGroup) — instance-level constraints report unsupported on that dialect. Topology declares placement locality; it does not change RoleInstanceSet rollout or recovery granularity. Newly created or recreated pods join their declared placement membership and are scheduled under its constraint, while component replacement may still follow the normal update strategy. Atomic, all-or-nothing instance replacement is gang-scheduling semantics; users that need it should enable gang scheduling explicitly.
- **Cross-role co-location** — a PlacementGroup with topology renders at the group level: Volcano uses the PodGroup's `spec.networkTopology`; Koordinator scopes the GangGroup to the member PodGroups of the group's roles; KAI renders a subGroup that nests the group's roles and carries its own `topologyConstraint`.
- **Gang plus topology on the same scope** — both attributes are rendered on one group object. For Volcano this is one PodGroup with `minMember`/subGroup thresholds plus `networkTopology`; for KAI it is one subGroup tree. No second topology-only PodGroup is created for the same members.
- **Containment trees** — KAI can render nested subGroups. Volcano's `subGroupPolicy` is flat, but it supports two containment shapes: the common parent topology + per-instance child topology case, and a gang parent with a cross-role topology child (the standard PD shape: whole-group gang over `{prefill, decode, router}` plus a topology child over `{prefill, decode}`). The latter renders as a `subGroupPolicy` entry whose `labelSelector` selects the child roles (for example, `role-name In [prefill, decode]`) and whose `networkTopology` carries the child constraint. Volcano still does not support arbitrary multi-level cross-role containment trees; those plans return `SchedulerUnsupported` instead of approximating the tree.
- **Topology-only groups** — Volcano renders `minMember: 0` and omits gang threshold fields; KAI renders `minMember: 0` / `minSubGroup: 0`. The compiler may still render the label/identity fields needed to form topology subgroups. Koordinator's deployed PodGroup CRD (`scheduling.sigs.k8s.io`) permits `minMember: 0`; the upstream scheduler-plugins CRD (`scheduling.x-k8s.io`, the dialect targeted by the repo's k8s-scheduler-plugin backend) requires `minMember >= 1`. The Koordinator compiler detects the installed PodGroup CRD dialect at runtime. If only the upstream CRD is installed, topology-only rendering returns `SchedulerUnsupported` rather than fabricating a gang threshold.

#### Generated Group Names

Legacy gang-only PodGroups keep the KEP-430 name `rbg.Name`. Topology-bearing PodGroups use a readable, collision-safe form:

```text
<truncated-rbg-name>-<placement-name>-<placement-hash>
```

- The RBG prefix is at most 26 characters.
- The placement name is at most 22 characters:
  - `r-<role.Name>` for a RoleSpec instance constraint;
  - `p-<policyRule.Name>` for a CoordinatedPolicy topology rule.
- Role names and topology-bearing policy-rule names are validated at admission as DNS-1123 labels. Source names therefore never inject characters that are invalid in generated Kubernetes object names; the compiler truncates the source portion to 20 readable characters when rendering the placement name.
- The placement hash is the first six bytes of SHA-256 over the RBG UID and the full `PlacementGroup.ID`, rendered as twelve hexadecimal characters. Including the full logical-group identity — not only the RBG UID — makes the name collision-safe within one RBG, including when two policy rules share their first 20 characters.
- The total is at most 62 characters, below the Kubernetes DNS-label limit.

Per-dialect naming rules:

- **Volcano**: a gang-only single PodGroup keeps the KEP-430 name `rbg.Name` (equivalence is golden-tested). A topology-bearing PodGroup uses the placement form above. In the containment case where a gang-bearing parent has topology-bearing subGroups, the gang parent keeps `rbg.Name`; each topology-bearing subGroup uses the placement form.
- **Koordinator**: there is no rendered root group object. GangGroup members are the existing per-role-instance (or per-role, when `PartitionBy=none`) PodGroups named `<rbg.Name>-<role.Name>-<instance-index>` / `<rbg.Name>-<role.Name>`; the GangGroup identity is the annotation, not a new object name.
- **KAI**: the root PodGroup is named `rbg.Name`. SubGroup names are unique across the entire subGroup tree, not merely among siblings, and use kind-prefixed, length-bounded namespaces:

  ```text
  policy-<source[0:20]>-<hash12>
  instance-<role[0:20]>-<instance-index>-<hash12>
  role-<source[0:20]>-<hash12>
  ```

  Each `hash12` is the first six bytes of SHA-256 over the RBG UID, the full `PlacementGroup.ID`, and the KAI subgroup kind. The readable source portion is truncated to 20 characters before concatenation. The resulting names are valid DNS-1123 labels and remain below the 63-character KAI `SubGroup.Name` limit even when the source role or rule name is already 63 characters. The kind prefix and full-identity hash also prevent a rule named `prefill-0` from colliding with an instance subGroup for role `prefill`. Parent references and pod bindings use exactly these generated names; a compiler never emits a raw source name directly. A role is split per instance only when it carries an instance constraint.

#### Translation Matrix

In the matrix below, `R` and `P` are names from `ClusterNetworkTopology.status.levels`. The compiler is responsible for mapping them to Volcano `tierName`, Koordinator `topologyLayer`, or KAI alias/nodeLabel.

| `pack` API (instance-level or cross-role) | Volcano | Koordinator gatherStrategy | KAI |
|---|---|---|---|
| `required: R` | `mode: hard` + `highestTierName: R` | `[{R, MustGather}]` | `requiredTopologyLevel: R` |
| `required: R` + `preferred: P` | `mode: hard` + `highestTierName: R`; P is **not anchored**; set `TopologyTranslated=True` with reason `PreferredAbsorbed` and emit a warning event | `[{R, MustGather}, {P, PreferGather}]` | `requiredTopologyLevel: R` + `preferredTopologyLevel: P` |
| `preferred: P` only | `mode: soft` (no tier threshold); P is **not anchored**; set `TopologyTranslated=True` with reason `PreferredAbsorbed` | `[{P, PreferGather}]` | `preferredTopologyLevel: P` |
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
  name: infer-0-p-pd-colocation-a1b2c3d4e5f6  # readable placement name + PlacementGroup-ID hash
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
    subGroupSize: 4              # ComputeSubGroupSize(prefill): one instance contains 4 pods
    networkTopology:             # each subGroup (= each instance) packs independently
      mode: hard
      highestTierName: rack
```

The router input declares no topology constraint, so it is not bound to any PodGroup. If a role outside every rule declares an instance constraint, the compiler creates a separate PodGroup for it and uses the same subGroup pattern. Every instance-level Volcano subGroup entry sets `subGroupSize` from `ComputeSubGroupSize(role)`; omitting it would let Volcano default to one pod per subGroup even when an instance contains multiple pods. `minSubGroups` remains omitted for topology-only behavior.

RBG-generated HyperNodes always maintain `spec.tierName`; if the referenced portable level is not present in `ClusterNetworkTopology.status.levels`, validation fails explicitly before rendering — there is no integer `highestTierAllowed` fallback.

**Koordinator** — the full example input is **not renderable** on Koordinator: prefill declares an instance-level constraint, `network-topology-spec` has no per-subGroup form (unlike Volcano's `subGroupPolicy`), and divergent member specs are invalid, so translation returns `SchedulerUnsupported` and no PodGroup is created or updated.

For the cross-role-only variant — that is, the input with prefill's unsupported instance constraint removed — no topology intent supplies `PartitionBy=role-instance-name`. The rule has `PartitionBy=none`, so both roles contribute role-level members (`infer-0-prefill` and `infer-0-decode`), not per-instance members. This KEP defines no hidden non-topology partition source. Every member PodGroup carries **identical** `groups` and `network-topology-spec` annotations, and the gather algorithm runs over all member pods of the group:

```yaml
apiVersion: scheduling.sigs.k8s.io/v1alpha1
kind: PodGroup
metadata:
  name: infer-0-prefill          # one role-level GangGroup member (2 total: prefill + decode); all members carry identical annotations
  annotations:
    gang.scheduling.koordinator.sh/groups: '["default/infer-0-prefill", "default/infer-0-decode"]'
    # required=block → MustGather@block; preferred=rack → PreferGather@rack
    # block/rack are the topologyLayer names exposed in ClusterNetworkTopology.status.levels
    gang.scheduling.koordinator.sh/network-topology-spec: |
      { "gatherStrategy": [
          {"layer": "block", "strategy": "MustGather"},
          {"layer": "rack",  "strategy": "PreferGather"} ]}
spec:
  minMember: 0                   # topology-only: no gang requirement; Koordinator CRD dialect
```

The `scheduling.sigs.k8s.io` API group and `minMember: 0` are Koordinator's deployed PodGroup CRD dialect. If the runtime-installed CRD is the upstream scheduler-plugins dialect (`scheduling.x-k8s.io`, `minMember >= 1`), the compiler reports `SchedulerUnsupported` for topology-only groups instead of inventing a threshold.

**KAI** — subGroups nest arbitrarily, so the plan lands in a single PodGroup: the rule becomes a nested subGroup spanning prefill+decode with its own `topologyConstraint`, per-instance subGroups carry instance packing, and uncovered roles only appear as sibling subGroups when they declare their own instance-level constraints. The root PodGroup is named `rbg.Name`; subGroups use the kind-prefixed naming rule so names are unique across the whole tree. Decode has no instance constraint, so it appears as one role-level child of `policy-pd-colocation-<hash12>` and is not split into `decode-0`/`decode-1`. KAI levels are the portable names in `ClusterNetworkTopology.status.levels`; `topology` names the KAI `Topology` CR recorded in `status.resources`, not `TopologyConstraint.topologyName`:

```yaml
apiVersion: scheduling.run.ai/v2alpha2
kind: PodGroup
metadata:
  name: infer-0
spec:
  minSubGroup: 0                 # root: topology-only — router never blocks pd-colocation
  subGroups:
  - name: policy-pd-colocation-<hash12> # kind-prefixed, length-bounded rule subGroup
    minSubGroup: 0               # topology-only: no gang requirement
    topologyConstraint:          # cross-role
      topology: gpu-fabric          # native resource from ClusterNetworkTopology.status.resources
      requiredTopologyLevel: block
      preferredTopologyLevel: rack
  - name: instance-prefill-0-<hash12> # per-instance leaf subGroups carry instance packing
    parent: policy-pd-colocation-<hash12>
    minMember: 0
    topologyConstraint:
      topology: gpu-fabric
      requiredTopologyLevel: rack
  - name: instance-prefill-1-<hash12>
    parent: policy-pd-colocation-<hash12>
    minMember: 0
    topologyConstraint:
      topology: gpu-fabric
      requiredTopologyLevel: rack
  - name: role-decode-<hash12>   # no instance constraint: one role-level child, not per-instance
    parent: policy-pd-colocation-<hash12>
    minMember: 0
```

KAI always renders `minMember` and `minSubGroup` on topology-bearing subGroups so topology-only groups remain elastic: their zero values explicitly mean "no gang threshold." Only **nonzero** gang thresholds are conditional on gang configuration. With a gang rule on the same scope, its KEP-430 computation fills the corresponding threshold while topology remains an attribute of the same subGroup tree.

### Observability

- **ClusterNetworkTopology status**:
  - `Ready` — whether the topology is usable by workloads. Reasons distinguish `TranslationSucceeded`, `NativeTopologyBound`, `ModeUnsupported`, `SchedulerMismatch`, `NativeTopologyNameInvalid`, `NativeTopologyNotFound`, `NativeTopologyConflict`, `NativeTopologyInvalid`, and `TopologyInUse`.
  - `NativeTopologyDrift` — imported resource changes or external edits to generated resources.
  - `status.levels` — the only level vocabulary exposed to workloads.
  - `status.resources` — the actual native GVK/name/UID or Volcano aggregate; this is diagnostic and not a workload input.
  - No `phase` field is used; `Ready` plus `reason` avoids duplicate and coarser state.
- **RBG conditions**:
  - `GangConfigured` — retained from KEP-430 for gang-only failures.
  - `TopologyTranslated` — present when topology is configured or while the policy-freeze settling window is active; `False` with reasons such as `TopologyUnresolved`, `TopologyNotReady`, `IncompatiblePlacementGroups`, `IncompatibleTopologyNames`, `RoleUnresolved`, `InvalidLevelOrder`, `LevelUnresolved`, `SchedulerUnsupported`, `TopologyPolicySettling`, `TopologyPolicyLate`, or `FeatureDisabled`. On success it is `True`; when Volcano cannot anchor a preferred level, the reason is `PreferredAbsorbed` and a warning event is emitted.
  - Condition transitions emit warning events (edge-triggered, per the KEP-430 event-spam analysis).
- **Placement gating is explicit.** While `GangConfigured` or `TopologyTranslated` is false, Role create/update is paused; status explains why no new pod was created.
- Placement outcomes themselves (which domain a group landed in) remain owned by the scheduler (for example Volcano's allocated-HyperNode bookkeeping); RBG status links failure *reasons* back to the declaring object.

### Dependencies

- **`ClusterNetworkTopology` CRD and webhooks.** Creation requires the mutating webhook that stamps `spec.scheduler`; the validating webhook enforces mode exclusivity and immutability. If the active scheduler is unknown, topology creation fails rather than guessing.
- **A scheduler with topology-aware scheduling support.** Phase 1 targets Volcano with `tierName` and subGroup-level `networkTopology`; Koordinator Imported and KAI Managed/Imported land in later phases. Runtime CRD schema inspection detects field availability (same pattern as KEP-430).
- **Node labels maintained by administrators or discovery tools** (UFM, RoCE topology exporters, topograph). For Managed label-backed modes, nodes must carry the configured label keys. RBG does not discover or assign these labels.
- **Native topology CRDs** for the selected scheduler: KAI `Topology`, Koordinator `ClusterNetworkTopology` named `default`, or Volcano `HyperNode`. Imported mode additionally requires the external resource to exist before the RBG topology can become Ready.
- **Koordinator PodGroup CRD dialect**: the Koordinator compiler targets Koordinator's deployed CRD (`scheduling.sigs.k8s.io`, `minMember: 0` allowed) and detects the installed dialect at runtime. The upstream scheduler-plugins CRD (`scheduling.x-k8s.io`, `minMember >= 1`) is not assumed to support topology-only groups.
- **KEP-430 gang semantics and PodGroup conventions**: the placement planner consumes `ResolveGangStrategy`; the compiler renders the PodGroup/subGroup objects and pod label conventions (`group-name`, `role-name`, `role-instance-name`). No second translator may modify those resources or pod annotations.
- **CoordinatedPolicy (KEP-30)** as the cross-role coordination surface: the policy binds to its RoleBasedGroup by identical name and namespace, so a topology rule's role set always resolves against exactly one RBG.

### Implementation Phases

- **Phase 1**: `ClusterNetworkTopology` CRD, mutating/validating webhooks, controller skeleton, finalizer, status contract, `TopologyConstraint` API (Role `instanceTopologyConstraint` + CoordinatedPolicy `scheduling.topologyConstraint`), PlacementPlan resolver/planner with KEP-430-equivalence golden tests, scheduler compiler interface, and **Volcano Managed materialization**.
- **Phase 2**: **Koordinator Imported binding** plus the Koordinator PodGroup compiler.
- **Phase 3**: **KAI Managed and Imported bindings** plus the KAI compiler; drift hardening and richer diagnostics.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to existing tests to make this code solid enough prior to committing the changes necessary to implement this enhancement.

#### Unit tests

1. ClusterNetworkTopology admission: the mutating webhook stamps the active scheduler; unknown scheduler rejects create; `levels` and `topologyBinding` are mutually exclusive; level names/keys are unique; `kubernetes.io/hostname` is last; Koordinator bindings are `default`; Volcano Imported and Koordinator Managed are rejected.
2. ClusterNetworkTopology immutability: scheduler, levels, and topologyBinding cannot be updated in place.
3. Managed KAI translation: `TopologyLevel.alias` equals the RBG level name, nodeLabels come from spec, ownerReferences are set, and external name conflicts report `NativeTopologyConflict`.
4. Imported KAI reverse mapping: alias is preferred over nodeLabel; missing alias falls back to nodeLabel; alias changes update status and report drift; same-name recreation changes the bound UID.
5. Imported Koordinator: only `default` is accepted; `networkTopologySpec[].topologyLayer` becomes the ordered `status.levels`; non-default names report `NativeTopologyNameInvalid`.
6. Volcano materialization: unique domain paths produce distinct HyperNodes even when rack names repeat under different zones; tierName is the RBG level name; integer tiers follow level order; leaf members are Nodes and parents are HyperNodes; stale domains are deleted; external membership edits are restored; non-owned tierName conflicts report `NativeTopologyConflict`.
7. Placement planning: equal scopes merge gang and topology; disjoint scopes stay independent; containment builds a tree; partial overlap is rejected with both declaring rules in the message. Scope comparison must include `PartitionBy`, not only `Roles`.
8. KEP-430 equivalence: gang-only inputs produce byte-for-byte the current single PodGroup rendering before topology features are enabled.
9. Translation matrix: every API combination (`required`, `required+preferred`, `preferred`-only) × every dialect renders the expected dialect object; Volcano preferred cases report `PreferredAbsorbed`.
10. Workload semantic validation: missing topology CR, topology not Ready, unknown status level, `preferred` broader than `required`, reversed parent/child nesting, and inconsistent topology names produce the documented condition reasons; recovery is tested after fixing the topology or RBG.
11. Topology-only groups: Volcano `minMember: 0`, KAI `minSubGroup: 0`, and nonzero gang thresholds omitted unless gang is configured. Volcano instance subGroups set `subGroupSize` from `ComputeSubGroupSize(role)` for LeaderWorkerPattern and CustomComponentsPattern.
12. Generated names: two placement groups in the same RBG whose source names share their first 20 characters produce distinct names because the hash includes the full `PlacementGroup.ID`; per-dialect naming rules (Volcano gang-only vs topology-bearing, Koordinator member names, KAI root/subGroup names) are golden-tested. Admission rejects non-DNS-1123 source names, and KAI cross-kind collisions (a rule named like a role instance) produce distinct kind-prefixed subGroup names with matching parent references and bindings. KAI tests use 63-character role/rule names and assert that every generated subGroup name remains a DNS-1123 label of at most 63 characters.
13. Runtime detection: Volcano CRD schema inspection for `tierName` / subGroup `networkTopology`; Koordinator PodGroup CRD dialect and `minMember` schema; unsupported paths return `SchedulerUnsupported`.
14. Finalizer lifecycle: workload references block deletion (`TopologyInUse`); Managed deletion removes generated resources; Imported deletion leaves the native resource untouched; CoordinatedPolicy UID-keyed release behavior remains as documented.

#### Integration tests

1. envtest ClusterNetworkTopology lifecycle: create the CR through admission, wait for Ready/status.levels/resources, update Nodes for Volcano materialization, and assert the generated scheduler objects field-by-field.
2. envtest workload rendering: create a Ready topology plus RBG with both attachment points, assert the rendered PodGroups (PlacementGroup-derived topology fields and any PodGroup created for rule-external instance constraints) field-by-field.
3. Failure gating: a missing/not-ready topology, incompatible plan, or unknown level prevents Role create/update and Pod creation; cleanup of deleted roles still works; running pods are not evicted.
4. Imported drift: modifying the native resource or deleting/recreating it with the same name updates ClusterNetworkTopology status, reports drift or not-found, and gates workload rendering.
5. Managed drift: externally edit a generated KAI Topology or Volcano HyperNode and verify the controller restores it while reporting `NativeTopologyDrift`.
6. Scheduler mismatch: change the controller's active scheduler configuration after a topology is created; reconcile reports `SchedulerMismatch` and does not reinterpret or mutate the old backend resource.
7. Lifecycle immutability: in-place topology removal or change is rejected by admission; deleting an active topology-bearing CoordinatedPolicy is held by its finalizer until the UID-recorded RBG is deleted; a same-name replacement RBG never inherits the old contract.
8. Creation order: a topology-bearing policy created before the RBG is accepted and rendered; the same policy created after the RBG has accepted `none` reports `TopologyPolicyLate` and gates Role create/update without rendering a PodGroup. The test triggers reconcile with an RBG spec change (replicas) rather than a metadata-only update. A cache-only policy informer is seeded without the policy while the uncached fake API reader returns it, proving the freeze uses the direct read. A policy appearing inside the settling window is accepted, while a policy appearing after the window reports `TopologyPolicyLate`.
9. Binding-before-role ordering: a compiler fake fails if Role create/update is called before pod bindings are injected; the integration test asserts every persisted Role pod template contains its placement membership at first create.

#### e2e tests

1. Volcano cluster with labeled Nodes: create a Managed ClusterNetworkTopology, verify generated HyperNodes/tierNames, deploy the example RBG, and verify each prefill instance's pods land within one rack-tier HyperNode and the rule's prefill+decode pods within one block-tier HyperNode.
2. Insufficient domain capacity: hard constraint → pods stay Pending, condition/event explains; soft constraint → cross-domain placement allowed.
3. Node label drift: adding/removing/changing a topology label updates HyperNode membership without silently changing workload constraints.
4. Unknown status level: explicit error surfaced, roles gated, pods not silently unconstrained.

### Graduation Criteria

- [ ] `ClusterNetworkTopology` CRD defined with Managed/Imported mode, immutable scheduler/levels/binding, and minimal status contract
- [ ] Mutating webhook stamps the active scheduler; validating webhook enforces mode exclusivity, immutability, and structural level validation
- [ ] ClusterNetworkTopology controller implemented with finalizer, Ready/NativeTopologyDrift conditions, status.levels, and status.resources
- [ ] Volcano Managed materialization implemented, including Node/HyperNode watches, path-safe naming, tier/tierName assignment, drift restoration, and conflict handling
- [ ] `TopologyConstraint` API defined (Role `instanceTopologyConstraint` + CoordinatedPolicy `scheduling.topologyConstraint`) with `topologyName` referencing the RBG topology CR
- [ ] `PlacementPlan` resolver/planner implemented, including `(Roles, PartitionBy)` scope-composition validation and KEP-430 equivalence golden tests
- [ ] Topology lifecycle immutability enforced by admission and the controller, including UID-keyed CoordinatedPolicy finalizer release and the documented rollback escape hatch
- [ ] Scheduler compiler interface + Volcano implementation (PodGroup + subGroup networkTopology, topology-only `minMember: 0`, collision-safe PlacementGroup-ID hashing)
- [ ] Failure gating for invalid topology, invalid plans, and translations
- [ ] Admission + reconcile validation (syntax at admission; Ready/status-level existence, preferred/required ordering, and parent/child ordering at reconcile)
- [ ] TopologyTranslated status and edge-triggered events on RBG
- [ ] Unit test coverage
- [ ] Integration test coverage
- [ ] e2e tests (Volcano environment)

### Upgrade / Downgrade Strategy

- **Upgrade**: `ClusterNetworkTopology`, `TopologyConstraint`, and their fields are additive. Existing RBGs and CoordinatedPolicies without topology configuration produce the same PlacementPlan and the same PodGroup rendering as KEP-430. No default topology CR is created unless a user explicitly creates one.
- **Downgrade**: topology fields are immutable in place. A workload that no longer needs topology must be deleted and recreated without topology so its rendered dialect fields are removed for the new lifecycle; already-scheduled pods from the old lifecycle are not migrated.
- **Topology CR rollback**: rolling the controller back to a version without this feature leaves `ClusterNetworkTopology` CRs, generated backend resources, and finalizers in place. Imported native resources remain valid scheduler configuration and can continue to be used directly. Managed generated resources can be cleaned up manually only after workload references are removed. A topology CR may carry a finalizer that the older controller does not understand; use the documented finalizer-removal escape hatch only after confirming no workload still references it.
- **CoordinatedPolicy rollback**: a topology-bearing CoordinatedPolicy may carry the topology finalizer that the older controller does not know; delete the matching RBG first, or use the documented finalizer-removal escape hatch, before recreating the policy.

### Version Skew Strategy

`ClusterNetworkTopology` and `TopologyConstraint` are additive, optional APIs. Older controllers ignore the new workload fields (constraints simply unenforced) and do not reconcile topology CRs. Newer controllers reading objects written by older versions see no constraints and render nothing. No control-plane/node coordination is involved beyond scheduler CRD versions and webhook availability, which are detected at runtime. Clusters using Managed/Imported topology must run the RBG controller version that owns the corresponding CRD, webhooks, and finalizer.

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

###### How can this feature be enabled / disabled in a live cluster?

Use the controller feature gate `RBGTopologyAwareScheduling`:

- Gate name: `RBGTopologyAwareScheduling`
- Component: `rbgs`
- Alpha default: `false`

Enabling or disabling requires restarting the RBG controller; it does not require API-server downtime, node downtime, or node reprovisioning. The controller must also run with the topology CRD, webhooks, and a topology-capable scheduler backend enabled.

###### Does enabling the feature change any default behavior?

No. RBGs and CoordinatedPolicies without topology declarations continue through the existing KEP-430-compatible path and render the same objects. Existing gang-only behavior is golden-tested for equivalence.

###### Can the feature be disabled once it has been enabled?

Yes. Set `RBGTopologyAwareScheduling=false` and restart `rbgs`. Unconstrained workloads continue normally. For topology-bearing RBGs, the controller stops rendering or updating topology groups, reports `TopologyTranslated=False` with reason `FeatureDisabled`, and gates further Role create/update so new pods are not created without their declared hard placement constraint. Already-running pods are not evicted or migrated. Existing scheduler group objects and topology finalizers are retained; delete the corresponding workloads (or use the documented finalizer escape hatch after confirming no workload still references them) before manually cleaning up generated resources.

###### What happens if we reenable the feature if it was previously rolled back?

The controller revalidates the recorded contract, `ClusterNetworkTopology` readiness and level drift, scheduler capability, and rendered objects. If those checks pass, topology translation and Role updates resume. If drift or a late policy is detected, the existing `TopologyPolicyLate`, `LevelUnresolved`, or `SchedulerUnsupported` paths gate the workload instead of approximating the constraint.

###### Are there any tests for feature enablement/disablement?

Yes. Unit and integration tests cover the gate default, admission with the gate disabled, rendering with the gate enabled, disabling after a plan has been accepted (Role updates gated, running pods unchanged), and reenablement after a rollback. Conversion and API round-trip tests ensure old objects remain readable when the gate is disabled.

### Rollout, Upgrade and Rollback Planning

###### How can a rollout or rollback fail? Can it impact already running workloads?

Rollout can fail if the topology CRD/webhooks are unavailable, the active scheduler differs from a topology's immutable scheduler, or the selected scheduler lacks the required CRD capability. In those cases no topology group is written and topology-bearing Role create/update is gated. Already-running pods are not evicted. Rollback to a controller without the feature leaves topology CRs, finalizers, and generated scheduler objects in place; the operator must remove workload references before cleanup, as described in Upgrade / Downgrade Strategy.

###### What specific metrics should inform a rollback?

Monitor failed topology translation conditions/events, `rbg_topology_translation_duration_seconds`, and `rbg_topology_native_resource_ready` (0/1 by topology CR). A sustained increase in translation failures or a topology that is not Ready supports disabling the feature gate and rolling back.

### Monitoring Requirements

The controller emits edge-triggered events and RBG/`ClusterNetworkTopology` conditions for unresolved topology, invalid levels, unsupported schedulers, drift, policy settling/late arrival, and feature-disabled states. The metrics above expose translation latency and topology readiness. No new node-level metrics are required.

### Dependencies

The alpha feature depends on the RBG controller, its webhooks, the `ClusterNetworkTopology` CRD, and the selected scheduler backend (Volcano, Koordinator, or KAI) including that backend's topology and PodGroup CRDs. Scheduler capability is detected at runtime and unsupported combinations fail explicitly.

### Scalability

Placement planning is bounded by the number of roles, CoordinatedPolicy rules, and RoleInstances, not by the number of nodes. Generated names are bounded and hashed, PodGroup/subGroup ownership is garbage-collected, and watches are scoped to RBG-referenced objects. Managed Volcano mode can create HyperNodes for participating nodes; the controller must respect API list/write limits and batch materialization. No per-pod custom resources are introduced.

## Drawbacks

- Cross-role co-location requires a second object: users declare instance packing on the RBG but must create a CoordinatedPolicy (same name/namespace) for PD co-location, even when no other coordination strategy is needed. Gang scheduling already established this pattern, and keeping every inter-role behavior in one object avoids two sources of truth.
- The planning layer is more explicit than letting each translator create resources directly. The compensating benefit is that gang, topology, and other placement intents have one membership owner; composition no longer depends on translator call order.
- A topology rule over a role set gathers all current and future pods of that scope into one domain. This can become a hard capacity ceiling; the limitation is explicit in this KEP.
- Volcano cannot express arbitrary nested cross-role placement trees or anchor a preferred middle tier. Those cases are reported as unsupported/degraded rather than approximated.
- ClusterNetworkTopology adds a new CR and controller lifecycle. The compensating benefit is that workload manifests reference a stable RBG vocabulary instead of a scheduler GVK or dialect field, and imported/managed ownership is explicit.

## Alternatives

- **Independent gang and topology translators.** Rejected: KEP-430 would create one RBG-level gang PodGroup while topology rules partition their own PodGroups. A pod can carry only one `group-name`, so overlapping scopes either lose the gang guarantee, over-constrain topology, or produce order-dependent results. Placement membership needs one owner.
- **RBG-level `spec.topologyConstraint` field.** Rejected: the constraint group "all pods of the RBG" is the wrong granularity for the driving scenario — PD-disaggregated deployments need prefill+decode co-located while roles without latency-critical cross-role traffic stay outside the domain, and an over-broad group wastes scarce in-domain capacity and can make the constraint unsatisfiable. Cross-role grouping is by definition inter-role coordination, which is exactly CoordinatedPolicy's domain. Whole-RBG co-location remains expressible by listing every role, so no expressiveness is lost.
- **Per-role topology with pairwise role references** (prefill declares "co-locate with decode"). Rejected: pairwise declarations create N² relationships, make the constraint group implicit, and complicate conflict validation; a single rule naming the full role set states the group explicitly, once.
- **Direct pass-through of scheduler level identifiers.** Rejected: the same RBG field would mean a Volcano `tierName`, Koordinator `topologyLayer`, or KAI `nodeLabel`/alias depending on the active scheduler. The schema would be stable but the semantic contract would not be. `ClusterNetworkTopology` makes the vocabulary and backend ownership explicit.
- **Putting a backend GVK in `topologyBinding`.** Rejected: users would need to understand each scheduler's CRD, and a GVK inconsistent with the active scheduler would require additional conflict semantics. A name plus the immutable scheduler stamp is sufficient; status records the actual GVK, name, and UID.
- **Koordinator Managed mode.** Rejected: Koordinator reads only the global `default` ClusterNetworkTopology, so RBG ownership could conflict with cluster administrators. Imported `default` makes external ownership explicit.
- **Approximating unsupported plans** (merging overlapping scopes into the union, dropping one constraint, or rendering native pod affinities). Rejected: silent semantic changes are worse than an explicit `IncompatiblePlacementGroups` or `SchedulerUnsupported` condition.

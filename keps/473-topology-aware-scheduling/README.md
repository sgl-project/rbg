# KEP-473: Topology-Aware Scheduling

## Table of Contents

<!-- toc -->
- [KEP-473: Topology-Aware Scheduling](#kep-473-topology-aware-scheduling)
  - [Table of Contents](#table-of-contents)
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
      - [Topology Retention and Deletion](#topology-retention-and-deletion)
      - [Status and Level Contract](#status-and-level-contract)
    - [Core API: TopologyConstraint](#core-api-topologyconstraint)
    - [Attachment Points](#attachment-points)
    - [Mutability and Update Semantics](#mutability-and-update-semantics)
    - [Placement Planning](#placement-planning)
      - [Placement Group Model](#placement-group-model)
      - [Scope Composition](#scope-composition)
      - [Reconcile Flow and Failure Gating](#reconcile-flow-and-failure-gating)
      - [Instance Binding Lifecycle](#instance-binding-lifecycle)
      - [Binding Status Field Ownership](#binding-status-field-ownership)
      - [RBG Deletion and Drain Protocol](#rbg-deletion-and-drain-protocol)
    - [Scheduler Translation](#scheduler-translation)
      - [Translation Channels](#translation-channels)
      - [Backend Capability Gates](#backend-capability-gates)
      - [Generated Group Names](#generated-group-names)
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
      - [How can this feature be enabled / disabled in a live cluster?](#how-can-this-feature-be-enabled--disabled-in-a-live-cluster)
      - [Does enabling the feature change any default behavior?](#does-enabling-the-feature-change-any-default-behavior)
      - [Can the feature be disabled once it has been enabled?](#can-the-feature-be-disabled-once-it-has-been-enabled)
      - [What happens if we reenable the feature if it was previously rolled back?](#what-happens-if-we-reenable-the-feature-if-it-was-previously-rolled-back)
      - [Are there any tests for feature enablement/disablement?](#are-there-any-tests-for-feature-enablementdisablement)
    - [Rollout, Upgrade and Rollback Planning](#rollout-upgrade-and-rollback-planning)
      - [How can a rollout or rollback fail? Can it impact already running workloads?](#how-can-a-rollout-or-rollback-fail-can-it-impact-already-running-workloads)
      - [What specific metrics should inform a rollback?](#what-specific-metrics-should-inform-a-rollback)
    - [Monitoring Requirements](#monitoring-requirements)
    - [Runtime Dependencies](#runtime-dependencies)
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
4. **A scheduler compiler.** One compiler per dialect (Volcano, Koordinator, KAI) is the only component that renders physical PodGroups and pod membership. It preserves hard topology and declared gang guarantees or reports `SchedulerUnsupported`. The two supported Volcano compatibility differences are explicit: an unanchored preferred level (`PreferredAbsorbed`) and an instance-local dispatch threshold imposed by `subGroupSize` (`ImplicitInstanceGang`). Neither permits dropping a hard constraint or a declared per-role gang minimum. Volcano additionally rejects BestEffort topology members and conflicting startup dependencies, and propagates parent topology into subgroup policies so scheduler restart can recover the common domain. Koordinator workload translation requires verified cross-batch enforcement; KAI requires a verified backend with unambiguous domain-path encoding. The inspected Koordinator baseline and unpatched KAI v0.18.6 remain unsupported for workload topology; importing their vocabulary alone does not enable placement.

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
5. **Lifecycle coverage.** Scale-out, rolling update, and recovery preserve the accepted placement membership. Hard placement is enforced by the configured scheduler. Invalid desired plans pause upper-level updates but do not globally suspend downstream controllers; recovery under persisted bindings and the limits of that guarantee are specified below.
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

- **Role level** (`spec.roles[].instanceTopologyConstraint`): constraint group = one RoleInstance's pods; every instance picks its domain independently (instance packing). The field is pattern-independent **within the RoleInstanceSet backing** and applies equally to `LeaderWorkerPattern` and `CustomComponentsPattern` roles. It is rejected for deprecated Deployment, StatefulSet, and LeaderWorkerSet role backings because those backings do not provide the required instance identity label.
- **Cross-role level** (`spec.policies[].strategy.scheduling.topologyConstraint` on CoordinatedPolicy): constraint group = all pods of the roles named in the rule's `roles` list (cross-role co-location). Roles not listed stay outside the cross-role domain (their own instance-level constraints still apply). Listing every role of the RBG recovers whole-group co-location, so no dedicated RBG-level field exists.

**Placement planning layer.** Before any scheduler object is created, the controller resolves gang intents (KEP-430 semantics are unchanged), topology intents, and role-instance partitioning into one **PlacementPlan**. The planner is the only owner of logical membership: equal scopes merge gang and topology attributes; disjoint scopes produce sibling groups; containment scopes produce a tree; partially overlapping scopes are rejected. This removes order dependence between gang and topology translation and prevents a pod from being assigned two different physical PodGroups.

**Scheduler compiler.** Per scheduler, a compiler consumes the valid PlacementPlan plus the resolved `ClusterNetworkTopology` status and renders the dialect — Volcano `networkTopology` at PodGroup/subGroup level, Koordinator `gatherStrategy`, KAI topology constraints — while preserving KEP-430 gang behavior when no topology is configured. Levels are validated against the RBG topology CR's status and native resource state; failures (unknown level, topology not ready, incapable scheduler, incompatible placement scopes) are explicit: status conditions plus events, never silent degradation.

### Notes/Constraints/Caveats

- **Group objects are the physical carrier, but not the semantic model.** A role or policy rule with topology constraints gets group objects even without a gang declaration. Topology-only means **no declared cross-instance gang threshold**, not universally gang-free execution: Volcano uses `minMember: 0` and omits `minSubGroups`, but an instance subgroup's `subGroupSize` still imposes an instance-local dispatch threshold. KAI topology-only groups use zero `minMember` / `minSubGroup`. Declared gang minima are rendered only for covered roles; Volcano's instance-local threshold is separately reported as `ImplicitInstanceGang` when not already requested by gang.
- **One membership owner.** Gang and topology resolvers declare intents only; they do not create PodGroups, choose PodGroup names, or inject `scheduling.k8s.io/group-name`. Only the scheduler compiler outputs objects and the unique pod bindings derived from the PlacementPlan.
- **Gather-only semantics.** The API models "gather into a common domain". "Spread across domains" is deliberately excluded.
- **The constraint group is exactly the rule's role set.** Referenced roles join one common domain; unreferenced roles stay outside it (their own instance-level constraints still apply). A role may appear in at most one topology-bearing rule because placement membership partitions pods; overlap is rejected by the CoordinatedPolicy validating webhook, following the `gang.minReplicas` validation pattern. Gang and topology scopes may additionally be equal, disjoint, or nested, but not partially overlapping.
- **Soft semantics are approximate on Volcano.** Volcano cannot anchor a soft target at a middle tier. `required: R` plus `preferred: P` renders `mode: hard` with `highestTierName: R`; the preferred target is not represented as a tier threshold. This is an explicit fidelity loss reported as `PreferredAbsorbed`, not a claim that scoring is equivalent to anchoring at `P`.
- **Volcano is materialized, not imported.** RBG Managed mode creates a set of HyperNodes from node labels; Imported mode is unsupported because there is no single topology resource to bind. `HyperNode.spec.tierName` is always the RBG level name, and integer `highestTierAllowed` is never rendered.
- **Koordinator is import-only, with workload translation gated.** Koordinator's scheduler reads only the `ClusterNetworkTopology` named `default`; RBG Managed mode could conflict with cluster-administrator ownership, so it is unsupported. A topology binding must reference `default`. A valid Imported binding does not establish lifecycle placement support: the inspected scheduler baseline reports `SchedulerUnsupported` for workload topology, including cross-role-only rules, as specified in [Backend Capability Gates](#backend-capability-gates).
- **KAI uses Topology-backed levels.** In Managed mode, RBG writes `TopologyLevel.alias = ClusterNetworkTopology.spec.levels[].name`. Imported mode requires every native level to have a non-empty, unique alias satisfying RBG's portable level-name contract. Status exposes those aliases; there is no nodeLabel fallback. Missing or invalid aliases report `NativeTopologyInvalid`, with the offending level and the administrator action required. RBG never modifies an imported Topology. Workload translation is separately gated on collision-free backend domain encoding; unpatched v0.18.6 does not pass that gate.
- **All topology CRs are pinned to one scheduler.** `spec.scheduler` is stamped on create and immutable. If the active scheduler later differs, the controller skips normal materialization and reports `SchedulerMismatch`; it does not silently reinterpret the same native resource name under a new scheduler. Deletion cleanup continues using the original scheduler identity and retention protocol.

### Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| Users mistype a level identifier (no schema can catch a wrong string) | The workload controller resolves `topologyName` to a Ready `ClusterNetworkTopology` and validates `required`/`preferred` against `status.levels`; failures surface `TopologyTranslated=False` and name the declaring CoordinatedPolicy rule or RoleSpec before any pod group is rendered |
| Gang and topology rules have partially overlapping scopes, so no backend can preserve both guarantees | Placement planning normalizes scopes before rendering and rejects partial overlap with `TopologyTranslated=False` / `IncompatiblePlacementGroups`; role create/update is gated |
| Scheduler has no topology capability (including the native kube-scheduler) | Hard constraints report `SchedulerUnsupported` explicitly; always surfaced in status — never silently dropped or approximated by per-pod affinity |
| Imported topology drift, including valid alias swaps, level renames, or same-name native CR recreation | Track the native UID and complete ordered mapping; KAI renders native constraints using accepted nodeLabels. Valid alias drift may update the vocabulary for new workloads, but existing RBGs report `TopologyMappingChanged` and retain their original mappings and bindings without reinterpreting hard constraints |
| Managed topology resources are externally modified | The controller owns generated resources and restores the desired state; while a mismatch exists it reports `NativeTopologyDrift` and does not mark the topology Ready |
| Active scheduler changes after a topology CR is created | `spec.scheduler` is immutable and was stamped at create; reconcile reports `SchedulerMismatch` and skips normal materialization instead of reinterpreting the topology under a new scheduler. Reference-drained cleanup still uses the original backend identity |
| Volcano version too old, topology CRD unavailable, or required scheduler plugins disabled | Runtime CRD schema detection checks field availability; deployment preflight verifies the tested scheduler version and enabled topology/gang plugins. A compatible schema alone is not proof of scheduling capability |
| Invalid desired topology while downstream controllers still have persisted work | Pause RBG Role create/update and retain accepted bindings and scheduler groups. Existing controllers may still replace Pods or finish already-issued scale/rollout work. This is not a global Pod-creation gate; hard enforcement depends on the retained native configuration and active scheduler |
| Topology declarations are changed after the workload contract is accepted | V1alpha2 validating admission rejects in-place additions, removals, and changes; changing topology requires deleting and recreating the workload |
| A user bypasses v1alpha2 validating admission (for example, through a v1alpha1 conversion round trip or by disabling webhooks) | Unsupported operation; a new controller still rejects conflicts with a persisted accepted contract, but cannot reconstruct an unrecorded historical contract or protect against an older controller |
| Topology identity is missing, not Ready, or inconsistent across one RBG | `TopologyConstraint.topologyName` must reference one Ready RBG `ClusterNetworkTopology`; missing/not-ready/mismatched names report `TopologyUnresolved` / `TopologyNotReady` / `IncompatibleTopologyNames` and block rendering |
| A topology rule names a role absent from the RBG, or a role appears in two topology-bearing rules | Admission checks rule-internal syntax (same webhook pattern as `gang.minReplicas`); role existence is verified at reconcile against the RBG (a policy may be written before the RBG exists) and reported as `TopologyTranslated=False` (`RoleUnresolved`) naming the offending role |
| A gang update changes physical membership without changing topology declarations | Compare the compiled layout with the accepted layout before any scheduler write; report `PlacementLayoutChanged` and retain the old groups and bindings |
| Volcano topology-only instance packing is mistaken for fully elastic dispatch | Document and report `ImplicitInstanceGang`; test `subGroupSize` with partial resource availability and staggered Pod arrival |
| Koordinator schedules later batches without retaining the domain of bound members | Keep workload topology unsupported on the inspected baseline; Phase 2 requires a verified backend that constrains pending members using existing bound members across scale-out, replacement, and restart |
| Volcano BestEffort/backfill bypasses the hard topology path | Reject topology-covered Pods whose effective requests enter the resource-empty/BestEffort path; validate all effective component templates, not only GPU containers, before workload writes |
| Component `startAfter` waits for Ready while an implicit instance gang waits for all component Pods | Reject non-empty startup dependencies on Pod-producing components of a Volcano instance subgroup before acceptance or template updates; do not lower `subGroupSize` or discard the dependency |
| A manual finalizer patch uses an index that shifted after the read | In one atomic JSON Patch, test the policy UID, resourceVersion, and exact finalizer value before removing that index; reread on any failed test |
| RI finalizer waits for Pods while background GC waits for the RI to disappear | Add a terminating drain branch to the RI controller that actively deletes all Pods identified by owner UID; retire the subgroup and release the finalizer only after drain completes |
| RBG or PodGroup disappears before its dependents, losing recovery state across restarts | Install the RBG drain finalizer before acceptance and the group-protection finalizer at PodGroup creation; use the persisted accepted record to drain in workload → Pod → group order |
| Ordinary RI status updates overwrite binding acknowledgements | The placement reconciler exclusively owns the binding payload and `PlacementBound`; each writer reads the latest object on conflict retries and preserves fields owned by other writers |
| Foreground deletion of a topology CR deletes native resources while RBGs still use them | Managed native topology resources use topology-UID labels and explicit cleanup, not ownerReferences; consumer reservations prevent cleanup from racing first workload acceptance |
| Volcano loses the parent domain after scheduler restart when gang subgroups have no topology | Compile an effective topology on every explicit subgroup beneath a topology-bearing parent, retaining stronger child constraints and all gang thresholds |
| KAI concatenates distinct label-value paths into the same domain ID | Require a tested backend with unambiguous path encoding; reject the unpatched baseline rather than certifying it from a collision-free Node snapshot |

## Design Details

### Background: Scheduler Topology Dialects

All target schedulers model the data-center network as a **domain tree** — Cluster → Region/Zone → Spine → Block/Leaf → Rack/ToR → Node → Device — where a lower lowest-common-ancestor means higher bandwidth and lower latency, and higher links are typically oversubscribed. They differ in how levels are named and how constraints are declared:

| Scheduler | Topology model | Level coordinate | Constraint declaration |
| --- | --- | --- | --- |
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

`levels` is ordered from broadest to narrowest. The array order is the only depth representation; there is no separate depth field. The list is atomic for server-side apply; a map-list would give unordered merge/equality semantics to an order-sensitive contract. Admission checks name/key uniqueness separately and rejects reordering as an immutable-spec change.

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
 // +listType=atomic
 Levels []TopologyLevelSpec `json:"levels,omitempty"`

 // TopologyBinding references an externally managed scheduler topology
 // resource. It is mutually exclusive with Levels and immutable after create.
 // +optional
 TopologyBinding *TopologyBindingSpec `json:"topologyBinding,omitempty"`
}

type TopologyLevelSpec struct {
 // Name is the portable level name used by RBG workloads. It is a DNS-1123
 // label and is deliberately capped below the Kubernetes limit so generated
 // native object names always have room for a hash.
 // +required
 // +kubebuilder:validation:MinLength=1
 // +kubebuilder:validation:MaxLength=32
 // +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
 Name string `json:"name"`

 // NodeLabelKey partitions nodes at this level. A node participates in a
 // domain only when it has a non-empty value for this key. Admission applies
 // Kubernetes qualified-name validation, including the 63-character name
 // segment limit; the schema MaxLength is only a coarse total-length bound.
 // +required
 // +kubebuilder:validation:MinLength=1
 // +kubebuilder:validation:MaxLength=253
 NodeLabelKey string `json:"nodeLabelKey"`
}

type TopologyBindingSpec struct {
 // TopologyReference is the native resource name, interpreted according to
 // spec.scheduler. KAI accepts a valid Kubernetes object name (DNS subdomain);
 // Koordinator admission additionally narrows the value to "default".
 // +required
 // +kubebuilder:validation:MinLength=1
 // +kubebuilder:validation:MaxLength=253
 // +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
 TopologyReference string `json:"topologyReference"`
}
```

#### Mode Support Matrix

| Scheduler | Managed | Imported | Binding name | Reason |
| --- | --- | --- | --- | --- |
| KAI | Yes, vocabulary materialization | Yes, vocabulary binding with portable aliases on every level | any valid Kubernetes `Topology` CR name (DNS subdomain) | workload translation additionally requires the Phase 3 domain-encoding gate; unpatched v0.18.6 is unsupported |
| Koordinator | No | Yes, vocabulary binding only until the Phase 2 capability gate passes | must be `default` | the scheduler reads only `default`; RBG must not take ownership of it, and a valid binding does not certify cross-batch placement |
| Volcano | Yes | No | n/a | topology is a set of HyperNodes, not a single bindable resource |

This matrix describes topology materialization and vocabulary binding, not authorization to render a workload. `Ready=True` on a topology CR is necessary but not sufficient: workload compilation must also pass the [Backend Capability Gates](#backend-capability-gates). Managed topology definitions are portable across supported schedulers as long as the same `levels[].name` values are declared. Imported topology is deliberately an explicit compatibility boundary: its status levels are read from the external CR, but the workload API still references only the RBG CR and status, never a backend GVK.

#### Validation and Admission

A mutating webhook is required for topology CR creation:

1. read the active scheduler from controller configuration;
2. if `spec.scheduler` is empty, write the active scheduler;
3. if a user-provided value differs from the active scheduler, reject the create;
4. reject creation if the active scheduler is unknown or is not one of `KAI`, `Koordinator`, and `Volcano`, including a recognized native kube-scheduler configuration. Return an admission error identifying the unsupported scheduler; do not add an enum value merely to persist a failed topology CR. A workload referencing an existing topology under an unsupported scheduler still reports `SchedulerUnsupported`.

A validating webhook enforces:

- `spec.levels` and `spec.topologyBinding` are mutually exclusive and exactly one must be set;
- `spec.scheduler`, `spec.levels`, and `spec.topologyBinding` are immutable;
- managed level names are unique DNS-1123 labels of at most 32 characters;
- managed node label keys are unique and satisfy Kubernetes qualified-name validation (`validation.IsQualifiedName`), including a name segment of at most 63 characters; the CRD `MaxLength=253` is a coarse bound and the validating webhook is authoritative;
- `topologyReference` is a DNS-1123 subdomain of at most 253 characters; Koordinator additionally requires `default`;
- a level name must not equal any node label key in the same topology (KAI alias validation);
- `kubernetes.io/hostname` may only be the last level;
- the number of levels is within backend limits (KAI's current limit is 16);
- Koordinator imported topology must reference exactly `default`;
- Volcano imported topology is rejected as `ModeUnsupported`.

Validation that requires reading Nodes or backend CRs happens in reconcile, not admission. This keeps admission self-contained and allows topology CRs to be created before all nodes are labeled.

#### Controller Materialization

The controller persists `topology.x-k8s.io/topology-protection` on every non-terminating `ClusterNetworkTopology` before publishing Ready or creating native resources. Managed native resources carry `topology.x-k8s.io/topology-uid` with the RBG topology CR's UID and have **no ownerReferences**. Their lifecycle is controller-managed, not Kubernetes garbage collection: a finalizer on an owner cannot stop foreground GC from deleting its dependents, and a child finalizer alone cannot prevent those dependents from becoming terminating. The [Topology Retention and Deletion](#topology-retention-and-deletion) protocol preserves usable native objects until consumers drain.

**KAI Managed** creates or updates one KAI `Topology` CR:

```yaml
apiVersion: kai.scheduler/v1alpha1
kind: Topology
metadata:
  name: gpu-fabric
  labels:
    topology.x-k8s.io/topology-uid: "<rbg-topology-uid>"
  # No ownerReferences: the topology controller performs reference-drained cleanup.
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

The native resource normally uses the RBG CR name. Ownership requires the expected topology-UID label and, after read-back, the recorded native UID; an unrelated same-name resource reports `NativeTopologyConflict` and is never adopted. The example's `<rbg-topology-uid>` is the actual RBG topology CR UID, not its name or the native resource UID. Managed native resources must remain free of ownerReferences before Ready can be published.

**KAI Imported** reads the referenced `Topology` and preserves its broadest-to-narrowest order. Every native level must have a non-empty alias that is a DNS-1123 label of at most 32 characters; aliases must be unique and satisfy KAI's native alias validation. Only after validating the complete list does the controller publish `status.levels = spec.levels[].alias`.

There is **no nodeLabel fallback**: ordinary keys such as `topology.example.com/rack` and `kubernetes.io/hostname` are not portable DNS labels. A valid KAI Topology without portable aliases is therefore not yet importable by RBG. Reconcile reports `Ready=False/NativeTopologyInvalid`, naming the level and requesting an administrator-provided alias; it neither sanitizes keys into potentially colliding names nor edits the native CR. Administrators can add aliases to the existing native Topology before retrying the binding.

The bound native resource remains read-only. Valid alias changes may update `status.levels` and report drift for new RBGs that have not yet accepted a contract; missing, invalid, or duplicate aliases do not publish a new usable contract. Same-name native CR recreation is still treated as a UID mismatch and must not be rebound as the original object.

**KAI mapping freeze.** [KAI v0.18.6](https://github.com/kai-scheduler/KAI-scheduler/blob/v0.18.6/pkg/apis/kai/v1alpha1/topology_types.go) allows alias edits but forbids changes to the ordered `nodeLabel` structure. Both Managed and Imported compilation persist the native GVK/name/UID and the complete ordered `{alias, nodeLabel, depth}` mapping at first acceptance; `depth` starts at 0 for the broadest level. This is internal data in the RBG accepted record, not a new workload input. `requiredTopologyLevel` / `preferredTopologyLevel` always render the `nodeLabel` from that snapshot, never the mutable alias. Inputs must still be portable names from `status.levels`; users cannot bypass alias validation by submitting nodeLabels directly.

Subsequent valid mapping changes, including a `block`/`rack` alias swap that preserves the set of names, report `TopologyTranslated=False/TopologyMappingChanged` and pause Role updates and new explicit binding acknowledgements without overwriting the accepted mapping or existing native fields. Pod recovery under existing bindings continues to use the original nodeLabels, so an alias swap cannot turn a hard rack constraint into a hard block constraint even before the drift watch is processed. Restoring the original mapping allows reconciliation to resume; adopting a new mapping requires a new RBG lifecycle. Native UID changes and invalid aliases still follow the identity and readiness failure paths, respectively. Mapping freeze does not protect against node label value changes or native resource deletion.

**Koordinator Imported** reads only the `scheduling.koordinator.sh/v1alpha1 ClusterNetworkTopology` named `default`. It does **not** infer depth from the order of `networkTopologySpec` because Koordinator expresses depth through `parentTopologyLayer`:

1. index entries by `topologyLayer` and reject duplicate layer names;
2. require exactly one `NodeTopologyLayer`;
3. start at `NodeTopologyLayer` and follow the singular `parentTopologyLayer` chain upward until an empty parent or the built-in `ClusterTopologyLayer` root is reached;
4. reverse that chain and publish it in `status.levels` as broadest-to-narrowest;
5. reject cycles, unknown/broken parents, a missing `NodeTopologyLayer`, and any spec entry not reached by the chain (which indicates a second root or disconnected chain).

Koordinator native layer names are not copied verbatim. The controller publishes deterministic portable aliases: `NodeTopologyLayer` maps to `host`; an explicit `ClusterTopologyLayer` maps to `cluster` (the implicit root is not published); other layer names are converted to lowercase kebab-case at CamelCase boundaries, with `_` and `.` replaced by `-`. An alias that is empty, longer than 32 characters, invalid as a DNS-1123 label, or collides with another alias reports `Ready=False` with reason `NativeTopologyInvalid`. The compiler keeps the alias-to-native-layer mapping for this topology; once the Phase 2 capability gate passes, it renders the original Koordinator layer names in `network-topology-spec`. Koordinator's per-layer `labelKey` array is not a public workload input; its any-of semantics remain owned by Koordinator. It is nevertheless part of the internal semantic identity, together with `topologyLayer`, `parentTopologyLayer`, and the derived alias/depth mapping. Managed mode is unsupported to avoid contending for the global `default` object.

**Koordinator semantic stability (Phase 2 prerequisite).** Native UID pinning alone cannot prevent a same-UID `labelKey` or parent-chain edit from changing the meaning of `required: rack`. Before enabling workload compilation, deployment preflight must verify fail-closed native admission that makes the complete `networkTopologySpec` immutable on update for `default`, installed before any acceptance and retained throughout every consumer lifecycle. This is an administrator-managed prerequisite, not permission for RBG to edit an Imported resource. Native deletion/recreation and bypassing this guard while consumers remain are unsupported and must be excluded by deployment permissions. The compiler reads the native object through `apiReader`, verifies Ready UID/generation consistency, and persists the complete ordered semantic mapping in the accepted RBG record and placement hash. Metadata-only updates are excluded. A changed mapping reports `TopologyMappingChanged` before candidate writes and retains the accepted state. This detection is defense in depth, not protection against the scheduler observing an unguarded edit first; a watch or snapshot alone does not satisfy the gate. A changed native specification requires draining all consumers and creating a new topology/workload lifecycle. The inspected baseline remains unsupported regardless of this additional guard.

**Volcano Managed** materializes a forest of HyperNodes from Node labels:

1. list Nodes and read every configured level label, including `kubernetes.io/hostname` when present. The host level is identified by this key, not by whether its portable name is literally `host`;
2. a Node participates only when every configured label has a non-empty value; excluded Nodes emit an edge-triggered warning. If the host level is configured, hostname values must be globally unique among participating Nodes. Duplicate values report `Ready=False/NativeTopologyInvalid` before any backend mutation; the controller must not merge two Nodes into one host domain;
3. build the full ordered domain path, for example `zone-a/block-b/rack-r1/host-h1`, and create a domain for every distinct prefix;
4. create one HyperNode per domain prefix, with:
   - `spec.tierName = spec.levels[].name`;
   - integer `tier` derived from level order (the last configured level is tier 1, each broader level increments by one);
   - when the last level is hostname, one leaf HyperNode per participating Node, containing exactly that Node through a `Node` member with `selector.exactMatch.name = Node.metadata.name`; hostname is the domain value and need not equal the Node name;
   - without a hostname level, each leaf contains all participating Nodes in that narrowest domain, each selected by exact Node name;
   - parent members of type `HyperNode` referencing the child domains;
   - the `topology.x-k8s.io/topology-uid` ownership label and no ownerReferences; parent/child HyperNode membership is expressed only through `spec.members`, not a GC ownership chain;
5. name HyperNodes with the bounded form `r-<topology-prefix>-<level[0:16]>-<hash12>`. Compute `topology-prefix` by replacing every `.` in the DNS-subdomain topology name with `-`, then taking the first 16 characters; do not truncate before sanitizing. A trailing `-` in either readable portion is legal inside the final name because the hexadecimal suffix ends it. `hash12` is the first six bytes of SHA-256, rendered as twelve lowercase hexadecimal characters, over an unambiguous canonical tuple of the full topology UID, full level name, and full ordered domain-path components; sanitization/truncation never changes those hash inputs. The final name is at most 48 characters and passes DNS-1123 label validation. Names such as `abcdefghijklmno.example.com` must not leave a dot at the truncation boundary. The full-domain hash distinguishes repeated rack names under different zones; parent references use exactly these generated names;
6. watch Nodes and generated HyperNodes:
   - label changes update memberships;
   - stale domains are deleted;
   - external edits are restored;
   - tier/tierName collisions with non-owned HyperNodes report `NativeTopologyConflict`.

A topology containing only the hostname level is valid and creates one tier-1 HyperNode per participating Node. Missing hostname labels exclude Nodes rather than falling back to Node names. No hostname level is synthesized when the user omits it. An empty participating set reports `Ready=False/NativeTopologyInvalid`; correcting labels triggers reconciliation and recovery.

Volcano Imported mode is unsupported because there is no single resource whose GVK/name/UID can represent the whole tree.

#### Topology Retention and Deletion

The topology controller owns cleanup of Managed native resources. Keeping the RBG topology CR does not by itself preserve an owner-linked native object during foreground deletion; omitting native ownerReferences is intentional. This differs from PodGroups, which retain RBG ownerReferences plus `group-protection` because RBG deletion actively drains their workloads. Deleting a topology CR does **not** delete its consuming RBGs.

1. **Reserve before acceptance.** After installing `rbg-drain`, the placement reconciler registers `{namespace, name, uid}` for the RBG in the topology's controller-owned `status.consumers`, keyed by RBG UID. Use an uncached read and resourceVersion-guarded status update on the same topology UID; require Ready, `topology-protection`, and no deletionTimestamp on every retry. No accepted contract, PodGroup, Role, or explicit binding may be published before this reservation succeeds. The accepted RBG record includes the RBG topology CR name/UID as well as native identity. A failed registration blocks creation; a stale cached Ready is insufficient.
2. **Close acquisition on deletion.** The API-server deletionTimestamp is the acquisition barrier: a stale registration conflicts with the deletion's resourceVersion, and retries cannot register on a terminating topology. Reservations committed before deletion remain retention roots, even if the RBG accepted-status write is still in flight. Set `Ready=False/TopologyInUse` while any reservation remains; pause normal RBG updates and new acknowledgements. Existing static or acknowledged bindings may recover against the retained native resources under the existing recovery contract. Do not delete, recreate, or rebind native resources merely because their RBG topology CR is terminating.
3. **Release only after consumer drain.** A reservation persists until the same-UID RBG is terminating with `PlacementDrained=True`, or that UID is absent and uncached checks confirm that no creators, Pods, or scheduler groups from that lifecycle remain. Active zero-replica RBGs retain reservations. An interrupted acceptance with no workload writes still retains its reservation until that RBG is deleted and its empty lifecycle drain completes; do not expire reservations by time or release them from an old failed reconcile. Unaccepted declarations without a reservation cannot publish bindings and do not retain native resources. RBG drain does not wait for topology deletion or reservation removal, so there is no circular wait.
4. **Delete native resources last.** Once all reservations have been released, confirm through uncached cross-namespace reference checks that no accepted record with incomplete drain, binding-capable template, Pod, or scheduler group still refers to the topology UID. An unexpected unregistered reference blocks cleanup rather than being ignored. Enumerate Managed resources using the immutable scheduler and topology-UID label, verify recorded native UIDs where present, issue UID-preconditioned deletes, and wait for actual disappearance. For Volcano, clean the complete UID-owned HyperNode set, not a name prefix or stale status count; remove parent membership edges before deleting obsolete children. Imported mode never edits or deletes the native resource. Finally release only `topology-protection`, preserving other finalizers.
5. **Recover without GC assumptions.** Topology, consumer-RBG, and native-resource watches plus five-second retries drive cleanup after controller restart, feature disablement, or scheduler configuration changes. Cleanup uses the original scheduler identity even when normal materialization reports `SchedulerMismatch`. API/CRD/permission errors are not NotFound. A missing native object while consumers exist is an error, not permission to adopt a replacement UID. Normal stale-domain cleanup for an active Volcano topology still follows Node membership reconciliation; topology deletion instead retains the remaining native set until consumer drain.

`status.consumers` is lifecycle bookkeeping, not a workload input. Placement writers add reservations; the topology cleanup writer removes only drained entries. Every writer, including ordinary Ready/status reconciliation, rereads and merges live fields on conflict, preserving other consumers and status fields. This protocol handles both background and foreground deletion of the RBG topology CR without marking Managed native resources terminating early. Direct administrator deletion of a native resource, adding GC ownerReferences, or tampering with ownership labels/reservations is outside this guarantee and must be excluded by deployment permissions; retaining a parent CR cannot repair such intervention. Rollback must complete explicit cleanup with a supporting controller because native resources are not GC dependents.

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
 // +listType=atomic
 Levels []string `json:"levels,omitempty"`

 // Resources records the bound or generated native resource(s).
 Resources []NativeTopologyResourceStatus `json:"resources,omitempty"`

 // Consumers reserves topology retention for RBG lifecycles before acceptance.
 // +listType=map
 // +listMapKey=uid
 Consumers []TopologyConsumerStatus `json:"consumers,omitempty"`

 // Conditions includes Ready and NativeTopologyDrift.
 Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type TopologyConsumerStatus struct {
 Namespace string `json:"namespace"`
 Name      string `json:"name"`
 UID       types.UID `json:"uid"`
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
      uid: "<native-topology-uid>"
      managed: true
      nativeGeneration: 1

  conditions:
    - type: Ready
      status: "True"
      reason: TranslationSucceeded
      message: "Topology has been translated to kai.scheduler/v1alpha1 Topology gpu-fabric"
      lastTransitionTime: "2026-10-15T08:00:00Z"
```

KAI Managed/Imported and Koordinator Imported both record the native `uid`, so a same-name recreation cannot be treated as the original object; `<native-topology-uid>` in the example is populated from an actual API read-back. Volcano records an aggregate entry rather than listing every HyperNode:

```yaml
resources:
  - apiVersion: topology.volcano.sh/v1alpha1
    kind: HyperNode
    managed: true
    namePrefix: r-gpu-fabric-
    count: 538
```

`status.levels` contains only strings. It intentionally does not expose `nodeLabelKeys`, backend level types, or domain counts. If diagnostics are needed later, they belong in a separate optional `levelDetails` field rather than in the workload contract. KAI's accepted alias→nodeLabel/depth snapshot is stored in controller-owned RBG status solely for compilation, drift comparison, and restart recovery; it does not change this string vocabulary.

The controller maintains these conditions:

| Condition | Reason | Meaning |
| --- | --- | --- |
| `Ready=True` | `TranslationSucceeded` | managed resources exist and match spec |
| `Ready=True` | `NativeTopologyBound` | imported resource exists and its levels are valid |
| `Ready=True` + `NativeTopologyDrift=True` | `NativeResourceModified` | imported resource remains valid after a change and `status.levels` has been updated; this only makes a new contract available and does not authorize changing an existing RBG's accepted mapping |
| `Ready=False` | `InvalidSpec` | admission contract violated or backend topology is structurally invalid |
| `Ready=False` | `ModeUnsupported` | scheduler does not support Managed/Imported for this CR |
| `Ready=False` | `SchedulerMismatch` | immutable `spec.scheduler` differs from active scheduler; normal materialization is skipped, but reference-drained cleanup remains active |
| `Ready=False` | `NativeTopologyNameInvalid` | Koordinator binding is not `default` |
| `Ready=False` | `NativeTopologyNotFound` | bound native resource does not exist |
| `Ready=False` | `NativeTopologyConflict` | native name/tierName is owned externally or by another RBG topology |
| `Ready=False` | `NativeTopologyInvalid` | native levels are empty, duplicated, unordered, invalid, oversized, or exceed backend limits |
| `Ready=False` | `NativeTopologyDrift` | generated or imported topology no longer matches the status contract |
| `Ready=False` | `TopologyInUse` | deletion is blocked by consumer reservations or remaining workload references, including interrupted first acceptance |
| `NativeTopologyDrift=True` | `NativeResourceModified` / `ManagedResourceModified` | imported resource changed or managed resource was externally edited |

Workload reconciliation requires `Ready=True` and validates `TopologyConstraint.pack.required` / `preferred` against `status.levels`. Every published `status.levels` entry must also satisfy the portable level-name contract (non-empty, unique, DNS-1123 label, at most 32 characters). For Koordinator, the published entry is the deterministic portable alias, not the raw native layer name. For KAI Imported, it is the validated mandatory native alias, never the nodeLabel. Imported resources whose levels cannot meet these rules report `Ready=False` with reason `NativeTopologyInvalid`. The array order is used for required/preferred and parent/child ordering checks. A workload is never rendered against a native scheduler CR directly.

The first KAI compilation must use `apiReader` to read the native Topology referenced by status, verify that its UID, `NativeGeneration`, and complete alias order match the current Ready status, and then generate the mapping snapshot from that same object. If status has not caught up with the native generation, report `TopologyNotReady` and retry rather than combining an old vocabulary with a new mapping. Subsequent compilations first verify that this snapshot matches the accepted mapping, then use the accepted nodeLabels. resourceVersion/generation are used for read-consistency checks and are excluded from the semantic hash; metadata-only updates do not constitute mapping changes.

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

An explicit launch dependency is separate from placement constraints:

```go
type RoleBasedGroupSpec struct {
    // ...existing fields...

    // Wait for topology rules from the same-name CoordinatedPolicy; immutable after creation.
    // +optional
    // +kubebuilder:default=false
    TopologyPolicyRequired bool `json:"topologyPolicyRequired,omitempty"`
}
```

Admission compares the effective boolean value so omitted and explicit `false` are equivalent. The field does not change policy binding by name/namespace; it only determines whether initial acceptance may proceed without a topology-bearing policy.

Instance-level constraints are pattern-independent within the RoleInstanceSet backing:

```go
// RoleSpec gains an instance topology member.
type RoleSpec struct {
 // ...existing fields...

 // InstanceTopologyConstraint packs the pods of each RoleInstance of this
 // role into one topology domain. It applies to every RoleInstanceSet pattern
 // that can produce multiple pods per RoleInstance, including
 // LeaderWorkerPattern and CustomComponentsPattern.
 // +optional
 InstanceTopologyConstraint *TopologyConstraint `json:"instanceTopologyConstraint,omitempty"`
}
```

`InstanceTopologyConstraint` is **backing-dependent**. Its planner partition is the `rbg.workloads.x-k8s.io/role-instance-name` label, which is currently emitted only by RoleInstanceSet. Admission therefore accepts the field only when the role's effective workload type is `workloads.x-k8s.io/v1alpha2/RoleInstanceSet` (including the v1alpha2 default). If `rbg.workloads.x-k8s.io/role-workload-type` selects `apps/v1/Deployment`, `apps/v1/StatefulSet`, or `leaderworkerset.x-k8s.io/LeaderWorkerSet`, admission rejects the role with `InstanceTopologyUnsupportedWorkload`. Reconcile repeats this check as a runtime safety net and gates Role create/update; it never emits a Volcano `matchLabelKeys` partition or a KAI per-instance subGroup for a backing that cannot label its pods. Cross-role topology rules use `PartitionBy=none` and remain independent of this restriction.

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
  topologyPolicyRequired: true
  roles:
  - name: router
    replicas: 1
  - name: prefill
    replicas: 2
    leaderWorkerPattern:             # each instance spans 4 machines (multi-node deployment)
      size: 4
    instanceTopologyConstraint:      # instance-level, RoleInstanceSet-backed: the pods of each
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
- the controller installs `topology-protection` before native materialization or Ready;
- consumer reservations serialize first acceptance against topology deletion; while consumers remain, deletion reports `TopologyInUse`;
- deleting a Managed topology waits for consumer drain, explicitly deletes UID-owned native resources, then releases its finalizer; native resources have no ownerReferences;
- deleting an Imported topology removes only the RBG CR and never the external native resource.

Changing a topology requires creating a new `ClusterNetworkTopology` and a new workload lifecycle. The active workload cannot be silently moved to a changed topology vocabulary.

The workload topology declaration is immutable for the RBG lifecycle: topology cannot be added, removed, or changed in place on a Role or CoordinatedPolicy rule. Ordinary changes remain allowed only when they preserve the accepted physical membership layout. Gang-only RBGs retain KEP-430 mutability; the additional layout freeze applies once a non-empty topology contract has been accepted.

Admission compares the old and new topology declarations directly. An RBGSet template likewise compares its old and new topology declarations, so parent validation is self-contained and requires no cross-resource child lookup.

**Explicit policy dependency.** RBG gains the optional boolean `spec.topologyPolicyRequired`, default `false`, immutable from creation. RBGSet exposes it through its RBG template and propagates it to children; template admission also enforces immutability. With `true`, first placement acceptance requires the same-name, same-namespace CoordinatedPolicy to contain at least one topology-bearing rule. Missing policy or missing topology rules reports `TopologyTranslated=False/TopologyPolicyPending` and requeues without creating Roles. There is no elapsed-time fallback. Policy create/update/delete events enqueue the matching RBG; while pending, reconciliation also retries every five seconds. With `false`, a missing policy does not delay topology-free or instance-only workloads; a policy already present is still included. Admission rejects `true` while the feature gate is disabled.

**Upgrade bootstrap and creation provenance.** Absence of an accepted record is not evidence that an RBG has never launched. On CREATE, the supporting RBG mutating webhook stamps the reserved, create-only annotation `topology.x-k8s.io/contract-version: v1:<handover-uuid>`; validation rejects malformed/unsupported values and any later addition, removal, or change. The UUID is a controller/webhook configuration value, generated once per handover from an older controller, persisted by the deployment, and shared by all supporting replicas. CREATE admission always writes the current configured value, never trusts a client-supplied epoch, and fails closed if that configuration is unavailable. Existing objects are never backfilled with this marker. RBGSet templates cannot supply it; each child receives it through RBG CREATE admission. The marker is internal provenance, not a user opt-in to topology. It may be issued only after the upgrade handover has stopped all controllers that can write workloads without first persisting an accepted record. All supporting controllers, including with the topology feature gate disabled, persist `none` before topology-free group/Role writes. Admission for the marker is fail-closed, and conversions preserve it.

For a non-terminating RBG without an accepted record, use an uncached UID-matching read and optimistic concurrency to classify it before resolving candidate topology or writing workloads. With no marker or a supported `v1` marker from an earlier handover epoch, persist a legacy `none` record for that UID, regardless of current policy, replica count, Pods, revisions, or retained backing templates; read/write errors block upper-level reconciliation. This deliberately includes old RBGs that never launched: adopting topology requires a new RBG UID. Existing gang behavior and resources are not reconstructed or rewritten by bootstrap. A topology-bearing policy already present before the new controller's first reconcile is late relative to this legacy contract and reports `TopologyPolicyLate`, without acquiring topology reservations/finalizers or changing bindings. With the valid current-epoch marker, normal first acceptance below applies. An unknown marker format/version or unavailable epoch configuration reports `TopologyBootstrapBlocked` and blocks reconciliation rather than granting fresh acceptance. Ordinary supporting-controller restarts, gate toggles, and supporting-version upgrades reuse the epoch; every return from a controller that lacks accepted-record persistence requires a new epoch, so surviving markers cannot falsely certify work created during a downgrade. A pre-existing accepted record always takes precedence; none of these branches overwrite it. Removing controller-owned status or forging provenance by bypassing admission is unsupported.

**Contract freeze and late policy creation.** After bootstrap classification, before fresh first acceptance, read the policy through the uncached `apiReader`; any read error other than `NotFound` blocks acceptance. After plan validation and compilation, a non-empty topology contract first requires persisting the `topology.x-k8s.io/rbg-drain` finalizer on the UID-matching RBG that is not yet terminating, then acquiring the topology consumer reservation, protecting the topology-bearing policy with its UID-keyed finalizer, and finally writing controller-owned RBG status using optimistic concurrency. Failure at any step prohibits scheduler group or Role writes; do not attempt to add finalizers after deletion has begun.

The accepted record contains topology declarations (including `none`); a non-empty contract also includes the normalized physical layout: RBG UID, RBG topology CR name/UID, native topology identity, scheduler/profile, logical group IDs, parent relations, role scopes, partition rules, physical group GVK/names, binding templates, and canonical hashes. KAI additionally records the complete ordered alias→nodeLabel/depth mapping; the gated Koordinator compiler records the complete native semantic mapping described above. Both include their mapping in the placement hash. The record describes placement only, excluding ordinary rollout fields such as image and command. Retries and restarts reuse the persisted record; during RBG deletion, the drain finalizer retains it until the deletion protocol below completes. Topology-free workloads do not acquire these additional topology finalizers or retroactive startup waits; first acceptance of `none` must still persist the declaration before Role writes so that later policy arrival can be detected.

An RBG with `topologyPolicyRequired: false` may accept its role-only or `none` contract immediately after an uncached `NotFound`. A topology-bearing policy appearing later reports `TopologyPolicyLate` and is not applied to that lifecycle. The same applies when the resolved declarations differ from the accepted declarations, including topology removal. Users requiring order-independent GitOps creation set `topologyPolicyRequired: true`; users not setting it must persist the complete policy before the RBG. No fixed settling window is used.

A rejected late policy that was never selected for acceptance acquires no `topology.x-k8s.io/policy` finalizer or topology consumer reservation merely because its name matches an RBG. Removing that policy restores the previously accepted role-only or `none` declarations and allows reconciliation to resume after validation; it does not require recreating the existing RBG just to continue without the rejected topology. Applying the rejected topology does require a new RBG lifecycle. By contrast, a policy already protected during an interrupted first acceptance remains subject to the retention protocol; it is not treated as an unselected late policy. `TopologyPolicyLate` also covers changed or removed accepted declarations, and its message must identify which mismatch occurred.

**Physical layout freeze.** KAI and the gated Koordinator compiler check their accepted mappings first; valid mapping changes use the distinct `TopologyMappingChanged` reason and must not be treated as harmless updates to the same declaration. After mapping and backend capability validation, compare the candidate compiled layout with the accepted layout before any backend write. A compilable gang change that reparents a topology group, changes a role's physical PodGroup, adds/removes a partition rule, or changes scheduler/profile reports `TopologyTranslated=False/PlacementLayoutChanged`; it does not mutate groups or Role templates. A candidate that already fails backend capability checks reports `SchedulerUnsupported` first, with the same retention behavior. Threshold-only gang updates are allowed if all groups, partition rules, bindings, and hard topology remain unchanged. Desired replica counts and concrete instance identities are not part of the layout hash, so normal scale-out, scale-in, and surge can instantiate or retire members of an already-accepted partition. Removing a role does not authorize repurposing its group; cleanup follows the reference-drain rules below. Changing the layout otherwise requires a new RBG lifecycle.

**Group retention.** A successful Role template patch is not evidence that old bindings have disappeared. Groups are retained while referenced by a live Pod, RoleInstance, or persisted workload template that can still create Pods, including a paused rollout's old instances. Scale-in retires a concrete instance group only after its creator is terminating/deleted, dependent Pods are gone, and it cannot create another Pod under that binding. Whole-layout migration is not supported; retained old/new groups are not a substitute for a protocol that would keep both in one domain. Retention also covers RBG deletion: every physical PodGroup carrying a topology plan has the `topology.x-k8s.io/group-protection` finalizer from creation. Neither the RBG ownerReference nor the RBG finalizer alone can prevent foreground GC; the release order is specified in the deletion protocol below.

Topology immutability is enforced by v1alpha2 validating admission. An active topology-bearing CoordinatedPolicy is protected by the `topology.x-k8s.io/policy` finalizer, keyed to the RBG **UID**, not merely its name. While the UID matches and deletion drain is incomplete, a terminating policy remains the authoritative contract; admission continues to reject topology updates, so delete-and-recreate cannot bypass immutability.

The policy finalizer may be released when the recorded RBG UID no longer exists, or when the RBG with that UID is terminating and has persisted `PlacementDrained=True`. The latter marks completion of the deletion protocol and is not equivalent to zero replicas. The policy controller does not require the RBG to disappear from the API first, and RBG drain does not wait for the policy to disappear, avoiding circular waits along foreground ownerReference chains. A replacement RBG with the same name but a different UID ignores the old terminating policy and does not inherit its contract; the old policy finalizer may be released once the old UID is confirmed absent.

Clusters using topology must enable v1alpha2 validating webhooks. Disabling admission or using a v1alpha1 round trip to drop topology fields is unsupported, but neither authorizes overwriting an existing accepted record: controller reconciliation still rejects a topology/layout mismatch and retains the previous bindings. The controller cannot reconstruct an unrecorded historical contract or enforce immutability against an older controller. It never deliberately renders a conflicting latest topology over an accepted layout.

Escape hatch and rollback: a policy stuck after an operator error or controller rollback may have only `topology.x-k8s.io/policy` removed. Read the policy once and obtain its UID, resourceVersion, and the exact finalizer's zero-based array index from that same response. Replace the placeholders below with those observed values. Submit all three `test` operations and the single `remove` in one atomic JSON Patch; a read followed by an unguarded index removal is unsafe because another controller can change the array. Do **not** use a merge patch that sets `metadata.finalizers` to `[]`, because that clears unrelated controller and foreground-deletion finalizers:

```sh
kubectl get coordinatedpolicy <name> -n <namespace> -o json
# Copy <policy-uid>, <resource-version>, and <index> from the same response.
# Verify that <index> contains exactly topology.x-k8s.io/policy.
kubectl patch --type=json -n <namespace> coordinatedpolicy <name> \
  -p '[
    {"op":"test","path":"/metadata/uid","value":"<policy-uid>"},
    {"op":"test","path":"/metadata/resourceVersion","value":"<resource-version>"},
    {"op":"test","path":"/metadata/finalizers/<index>","value":"topology.x-k8s.io/policy"},
    {"op":"remove","path":"/metadata/finalizers/<index>"}
  ]'
```

If any test fails or the request conflicts, no removal is applied. Reread and recheck the policy's identity, current finalizers, and eligibility before constructing a new patch; never retry an unguarded removal at the old index. If the target finalizer is already absent, no patch is needed. This bypasses the topology immutability guarantee and requires the operator to confirm that the RBG is no longer governed by the old policy. All other finalizers must remain untouched.

To change topology, delete and recreate the affected workload (and, when needed, its topology-bearing CoordinatedPolicy). Rolling updates that change topology constraints are out of scope for this KEP. Topology changes alone do not trigger migration or eviction of running Pods; user-initiated workload deletion, normal scale-in, and instance replacement still terminate Pods under their respective deletion protocols.

If the referenced `ClusterNetworkTopology` is missing, not Ready, or lacks a level at first acceptance, workload reconciliation reports `TopologyUnresolved`, `TopologyNotReady`, or `LevelUnresolved` and pauses RBG Role create/update. For an existing KAI accepted record, valid mapping drift in a Ready vocabulary reports `TopologyMappingChanged` first, even if an old alias has been removed; the old declaration must not first be reinterpreted against the new vocabulary. Before first acceptance this prevents creating managed workloads and their Pods. After acceptance, existing workload controllers may still replace Pods or finish previously issued scale/rollout work using persisted bindings; RBG does not globally stop Pod creation or cancel an in-progress rollout. It retains the accepted scheduler groups and never strips constraints to unblock recovery. Successful hard enforcement during such recovery depends on the native topology and scheduler still honoring those bindings; a missing or externally corrupted backend is not made safe by an RBG condition. Running Pods are not evicted or migrated by this feature.

Topology and native-resource changes enqueue referencing RBGs; RoleInstance events also drive concrete binding reconciliation. Recovery revalidates the accepted contract and layout before resuming updates. A backend resource must never be silently rebound to a new native UID or a different scheduler.

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

Each RoleInstance independently selects a domain. In the current API, `InstanceTopologyConstraint` produces `PartitionBy=[role-instance-name]` only after admission has verified the RoleInstanceSet backing, while a CoordinatedPolicy topology rule produces `PartitionBy=none`.

#### Scope Composition

The planner canonicalizes each intent's scope as `(sorted Roles, PartitionBy)`, not as a role set alone. Two scopes are comparable only through the following four relations:

| Relation | Formal condition | Example | Result |
| --- | --- | --- | --- |
| Equal | Same `Roles` and same `PartitionBy` | gang `{Roles=[P,D], PartitionBy=none}`, topology `{Roles=[P,D], PartitionBy=none}` | Merge into one PlacementGroup; gang and topology are attributes of the same group |
| Disjoint | `Roles` have no intersection | gang `{Roles=[P,D], PartitionBy=none}`, topology `{Roles=[R], PartitionBy=none}` | Two independent groups |
| Containment | Child `Roles` ⊆ parent `Roles`, and child `PartitionBy` is equal to or finer than parent `PartitionBy` | topology `{Roles=[P,D,R], PartitionBy=none}` containing gang `{Roles=[P,D], PartitionBy=none}` | Parent/child placement tree, if the backend can render it |
| Partial overlap | `Roles` overlap without containment, or the partition relation is incompatible | gang `{Roles=[P,R], PartitionBy=none}`, topology `{Roles=[P,D], PartitionBy=none}` | **Reject before workload creation** |

`PartitionBy=none` is the coarsest partition. `role-instance-name` is finer than `none`.

Partial overlap is not a naming problem: it is a hypergraph that the target PodGroup models cannot represent, because a pod has one physical membership. All alternatives are wrong:

- create `{P,R}` and `{P,D}`: P cannot belong to two PodGroups;
- merge into `{P,D,R}`: the topology constraint incorrectly covers R;
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
0. Read the RBG; terminating objects enter drain before feature/readiness/policy gates
   - For non-terminating objects without an accepted record, classify creation provenance and persist legacy none before candidate topology resolution
1. Read CoordinatedPolicy for a non-terminating RBG; compare declarations with any accepted record before resolving candidate topology
2. Resolve all TopologyConstraint.topologyName values to ClusterNetworkTopology CRs
3. Require a live, non-terminating topology with the expected UID and Ready status; KAI verifies native UID/generation and checks accepted mapping drift first
4. Resolve gang intents (KEP-430 rules unchanged)
5. Resolve topology intents and role-instance partitions
6. Build and validate the PlacementPlan:
   - role existence
   - all constraints resolve to one topology name
   - level existence against status.levels
   - scope relation over (Roles, PartitionBy) (equal/disjoint/containment only)
   - preferred/required ordering and parent/child ordering from status.levels order
7. Build effective templates without writes; compile effective subgroup topology and check all backend capability gates
8. Install RBG drain protection and reserve the topology UID before accepting a non-empty contract; persist or validate the contract, mapping, and layout
9. Reconcile protected static groups and concrete groups for observed RoleInstances
10. Confirm group ownership/spec by API read-back; do not wait for a scheduler phase that requires Pods
11. Inject static pod bindings or the accepted per-instance binding descriptor into Role templates
12. Create/update Roles; per-instance explicit bindings complete through Instance Binding Lifecycle
13. Retire groups only after their creators, Pods, and binding-capable templates have drained
```

If bootstrap, policy resolution, or steps 2–8 fail:

- no candidate PodGroup is created or updated, and accepted groups are not garbage-collected because they are absent from an invalid candidate plan;
- RBG Role create/update is paused using the KEP-430 gating path. For fresh admission-marked lifecycles, no managed Roles exist before first acceptance. Legacy lifecycles may already have workloads and are bootstrapped to `none` before this path; persisted downstream work and Pod replacement may continue under their existing configuration;
- no fallback to an unbound/default-scheduler Pod template is permitted;
- already-running Pods are not evicted by this feature, and normal downstream rollout/recovery behavior is not globally suspended;
- cleanup of deleted roles remains allowed, but group retirement waits for reference drain;
- the RBG gets a condition and edge-triggered events naming the declaring rule or role.

Step 7 uses the same template resolution, defaulting, patching, and runtime-injection inputs as workload rendering. It runs before first acceptance and on every candidate update, including request or component-annotation changes that leave the placement hash unchanged. Backend eligibility is not inferred from a previous successful acceptance. A failed capability check reports `TopologyTranslated=False/SchedulerUnsupported` with the scheduler baseline and offending role/component or dependency; it is not a successful translation with only `ImplicitInstanceGang`. Existing accepted groups and templates remain intact, and deletion/drain remains allowed.

Failures while writing or reading back groups in steps 9–10 also stop Role updates. Partially created owned groups are retained for idempotent retry; scheduler `Pending` alone is not a readiness failure because Pods may not exist yet.

#### Instance Binding Lifecycle

Volcano's label partition can be installed before instances exist. KAI's explicit instance subgroups cannot: RoleInstanceSet has a shared template, Stateless instance identities are allocated downstream, and Stateful rollouts may create surge instances beyond the desired replica range. The compiler therefore has two outputs: static bindings and an instance binding descriptor identifying the accepted layout hash, role, logical partition, and parent group. A descriptor is not a concrete subgroup name and may never be copied into a Pod as one.

For explicit instance bindings, the lifecycle is:

1. RoleInstanceSet creates an RI with its actual name/UID and the accepted descriptor, which includes the RBG UID. Stateful ordinals, random Stateless identities, and surge instances all follow the same flow; identities are never guessed from desired replicas.
2. The RI controller persists the `topology.x-k8s.io/instance-binding` finalizer before creating any Pod. Pod creation is denied by default without a complete, valid acknowledgement; an absent condition does not authorize creation. The RI controller does not write placement-owned status; the placement reconciler writes the initial `PlacementBound=False/PlacementBindingPending`.
3. RI events enqueue the RBG placement reconciler. It confirms that the RI finalizer exists, the RI and owning RBG UIDs match, and neither object is terminating, then compiles the subgroup from the accepted descriptor, serially merges it into the shared PodGroup, and reads back ownership/spec through the API. Topology failure, mapping or layout mismatch, and feature disablement do not authorize new bindings.
4. The placement reconciler persists `status.placementBinding` and `PlacementBound=True` together. The payload contains the RBG UID, accepted layout hash, RI UID, physical PodGroup name/UID, and subgroup name. The RI controller overlays the compiled result onto all component Pod templates only when the payload is complete, UID/hash values match, and the condition is True. Before each Pod creation, it must also verify that the RI/RBG are not terminating and the original PodGroup UID still exists and is not terminating; a stale cached True alone cannot authorize creation.
5. New RIs from scaling or rollout repeat this handshake; shared PodGroup updates must preserve all live sibling subgroups. A PodGroup UID change invalidates the old acknowledgement and blocks new Pod creation until reconfirmation; an object owned by someone else must not be adopted merely because its name matches. Active, already-bound instances continue to replace Pods under their accepted bindings without selecting new groups.
6. A terminating RI enters a **new active drain branch**, rather than taking the current `DeletionTimestamp` early return or waiting for background GC to delete Pods first. The RI controller stops replacement/restart/in-place Pod creation, uses an uncached list to find all dependent Pods by RI owner UID (including inactive, terminal, and terminating Pods), issues deletes with each Pod's UID precondition, and waits for actual NotFound responses. It must not check only `filteredPods`, determine drain completion by RI name, or remove other Pod finalizers. This branch continues to run when the feature is disabled, topology is NotReady, or the RBG is terminating.
7. After Pods have drained, the placement reconciler retires the subgroup and reads it back, then releases only the `instance-binding` finalizer; a new RI with the same name must not inherit the old binding. If interrupted after writing the group but before acknowledgement, identify the subgroup from the RI UID, descriptor, and accepted record and read the actual group; cleanup must not depend on an existing payload or a previous `PlacementBound=True`. If the original PodGroup UID is gone, release the finalizer after drain without recreating the group. If the PodGroup itself is terminating, the subgroup may remain until the entire group is deleted; release the RI finalizer after Pods have drained and RI creation is blocked, so progress does not require updating a deleting group. The parent group must still wait for its remaining child/static references to drain. Restart recovery uses the protected RBG accepted record, RI payload, and actual owner UIDs, not in-memory counts.

This handshake gates previously unbound explicit instances; it is not a global NotReady gate on already-bound instances or on Deployment/StatefulSet/LeaderWorkerSet recovery. Cross-role-only roles with those backings use static bindings and follow the persisted-template recovery contract.

#### Binding Status Field Ownership

- The placement reconciler exclusively owns `RoleInstance.status.placementBinding` and the `PlacementBound` condition, including Pending, acknowledgement, invalidation, and `PlacementDraining` states; the RI controller only consumes them. The payload and condition are written in the same status update guarded by resourceVersion; separate writes must never expose `True` with an empty payload.
- The ordinary RI status updater retains ownership of existing fields such as readiness, revision, and restart, but every conflict retry must use `apiReader` to fetch the latest object with the same UID, preserve live `placementBinding` and `PlacementBound`, and merge its own fields. The current path that rebuilds `newStatus` and replaces the whole status with `clone.Status = *newStatus` must be updated accordingly; preserving only custom conditions is insufficient, and the payload must not be copied from the stale object read at the start of reconciliation.
- The placement writer likewise starts from the latest object, updates only its payload/condition, and preserves other writers' readiness, restart, and custom conditions. After a conflict, it rereads and revalidates RI/RBG UIDs, termination state, the accepted hash, and the original PodGroup identity; it must not unconditionally restore the acknowledgement from before the retry. Conditions are merged by type, never by replacing the entire array and overwriting other writers' entries.
- When binding identity/hash becomes invalid, set `PlacementBound` to False but retain the last payload to locate resources during drain. Topology NotReady, alias drift, or feature disablement alone does not revoke an existing acknowledgement with a valid identity, preserving the documented recovery path. Termination sets `False/PlacementDraining` and never restores True. Readers validate both the condition and complete payload; a preserved condition alone does not make a binding usable. Normal retirement does not require clearing the payload beforehand; it disappears with the RI.
- The same ownership rule applies to the RBG: the placement/drain reconciler owns the accepted record, hash, and `PlacementDrained`; ordinary RBG status updates must merge and preserve these fields so deletion recovery state is not lost.

#### RBG Deletion and Drain Protocol

This protocol covers every non-empty topology contract, including static bindings and KAI explicit instance bindings. The RBG finalizer protects only the RBG itself and cannot prevent foreground GC from deleting its PodGroups first; object retention and controller-driven drain must therefore be implemented together.

1. **Install protection before publishing workloads.** Install `topology.x-k8s.io/rbg-drain` before first acceptance. Every PodGroup managed by the topology plan carries `topology.x-k8s.io/group-protection` from creation and retains its existing RBG ownerReference; an existing owned group must also be protected and read back before use. Groups, managed workloads, RIs, and their Pod templates/Pods carry `topology.x-k8s.io/rbg-uid`, which combines with owner UID chains to identify the entire lifecycle; references must not be discovered solely by RBG name or latest spec. KAI subgroups have no separate Kubernetes finalizer and are protected by their physical PodGroup.
2. **Deletion takes precedence over normal gates.** RBG reconciliation enters drain before the current terminating early return, preserves accepted status, and stops normal Role create/update and new acknowledgements. This path does not recompile the latest spec or wait for policy existence, topology Ready, an active-scheduler match, or feature-gate enablement. Identify original backend objects from the accepted group GVK/name and RBG owner UID, read back the actual group UID, and perform operations with UID preconditions; do not infer object types from the current scheduler configuration. Related cleanup controllers, watches, and permissions remain in place after feature disablement. Missing CRDs, read failures, and permission failures are retryable errors, not evidence that objects are gone.
3. **Drain creators and Pods first.** Enumerate existing backing workloads using the accepted record, UID labels, and owner chains, covering roles removed from the latest spec, old revisions, paused rollouts, Stateless instances, and surge instances. Issue foreground deletes with UID preconditions for owned backing workloads and track their descendants. RIs carrying the `instance-binding` finalizer actively delete Pods as described above; other backings/RIs rely on their respective controllers/GC to delete children, rather than merely setting replicas to zero. Continue driving deletion when remaining owned descendants are observed. All persisted workload/instance/template owners capable of creating Pods must disappear, as must every Pod object; Ready=0, terminal Pods, successful Delete requests, or empty cached lists do not prove drain completion.
4. **Then retire scheduler groups.** Retain every still-referenced group during drain, even if GC has marked it terminating. Retire subgroups and release RI finalizers according to instance drain results. Only after uncached read-back confirms that all creators and Pods are absent may the corresponding group's `group-protection` be removed and the group deleted with a UID precondition; wait for the original UID to actually disappear. Preserve all other controllers' finalizers. Failure at any stage retains the RBG finalizer, accepted record, and groups still needed. Retaining a terminating group keeps cleanup and recovery state readable; it does not guarantee that the scheduler continues accepting new scheduling work for that group.
5. **Persist completion before release.** Once managed creators, Pods, RI binding finalizers, and owned scheduler groups have drained, write `PlacementDrained=True` for the same RBG UID; release `rbg-drain` only after that write succeeds. The policy controller may use this marker to release the corresponding `policy` finalizer without waiting for RBG NotFound. RBG drain itself does not wait for the policy, supporting foreground deletion even when the policy has a blocking ownerReference. An active RBG does not set this marker or release lifecycle protection merely because it scales to zero. `ClusterNetworkTopology` reference accounting still includes accepted records of terminating RBGs whose drain is incomplete. Its cleanup reconciler may release this RBG UID's consumer reservation only after the drain marker; RBG deletion does not wait for reservation removal or topology deletion.
6. **Recover from interruption.** Every step is idempotent; Pod/workload/group events and five-second retries for incomplete drains jointly drive progress. After restart, continue from persisted UIDs/records and live API state. If interrupted after installing the RBG finalizer but before writing the accepted record, no group or Role may have been created; after confirming that no owned descendants or groups exist, complete drain for the empty lifecycle. If protected resources exist without a record, retain the finalizer and report `PlacementDrainBlocked` rather than guessing a new layout. Both background and foreground RBG deletion follow this protocol; orphan deletion with reusable bindings is not offered as a safe migration or rollback procedure.

The current early-return/status paths in `roleinstance/instance_reconciler.go`, `roleinstance/instance_status.go`, and `internal/controller/workloads/rolebasedgroup_controller.go` require the changes described in this section; these are integration tasks for this KEP, not existing controller behavior. Successful persistence of protection finalizers, status field preservation, and deletion recovery tests are prerequisites for enabling the corresponding capabilities.

### Scheduler Translation

The scheduler compiler consumes the validated PlacementPlan, the referenced `ClusterNetworkTopology.status.levels`, and the native-resource reference recorded in topology status. It is the only component that creates physical group objects and emits pod bindings. The compiler maps portable level names to the active scheduler's dialect; workload objects never contain a backend GVK. KAI uses the accepted alias→nodeLabel/depth mapping to render stable nodeLabels; it must not reinterpret the same alias against new native semantics on each reconciliation.

Pod `schedulerName` is part of those pod bindings, but it is not topology membership. After all backend capability gates pass for a PlacementPlan containing topology intent, the compiler injects one scheduler name into every pod template in the RBG — including roles outside every topology rule and roles with no physical PodGroup, such as the example router — before Roles are created. This prevents one RBG from being split between the cluster default scheduler and a topology-aware scheduler. Rejected plans produce no template writes. Dialect rules:

- **Volcano**: every pod receives `schedulerName: volcano`.
- **Koordinator (gated Phase 2 target)**: every pod receives the configured Koordinator scheduler profile name; when the profile name is empty, the field is left unset only under the documented assumption that the cluster default scheduler is Koordinator.
- **KAI**: every pod receives the configured KAI scheduler profile name; when the profile name is empty, the field is left unset only under the documented assumption that the cluster default scheduler is KAI.

Topology-free RBGs retain KEP-430 behavior and receive no additional scheduler-name injection from this feature.

#### Translation Channels

- **RoleInstance packing** — Volcano uses subGroup-level `networkTopology` with `matchLabelKeys=[role-instance-name]`; KAI uses explicitly acknowledged per-instance subgroups; **Koordinator has no instance-level form** and reports `SchedulerUnsupported`. Volcano's `subGroupSize = ComputeSubGroupSize(role)` is both a dispatch threshold and part of instance scheduling behavior, not merely a group identity hint. On the supported gang-plugin path, `minMember: 0` / omitted `minSubGroups` do not cancel that threshold. RBG supports this backend difference only after the resource-request and startup-dependency gates pass, with `ImplicitInstanceGang` when no declared gang already requests it. This does not change component deletion/replacement granularity or guarantee simultaneous Pod restarts. Gang scheduling controls dispatch, not atomic controller-side replacement.
- **Cross-role co-location** — a PlacementGroup with topology renders at the group level: Volcano uses the PodGroup's `spec.networkTopology` and propagates its effective constraint into every existing explicit subgroup policy as specified below; Koordinator's planned Phase 2 translation scopes the GangGroup to the member PodGroups of the group's roles, but remains disabled on the inspected baseline; KAI renders a subGroup that nests the group's roles and carries its own `topologyConstraint`.
- **Gang plus topology on the same scope** — both attributes are rendered on one group object. For Volcano this is one PodGroup with `minMember`/subGroup thresholds plus `networkTopology`; for KAI it is one subGroup tree. No second topology-only PodGroup is created for the same members.
- **Containment trees** — KAI supports nested subgroups, subject to preserving every gang minimum. Volcano supports a topology-bearing PodGroup with disjoint per-instance subgroup partitions, optionally carrying gang thresholds on those same partitions. A gang parent over `{prefill, decode, router}` with a cross-role topology child over `{prefill, decode}` is **unsupported in Phase 1**, even though the logical tree is valid. The compiler must not flatten away per-role minima, widen the topology scope to router, or emit overlapping subgroup policies.
- **Topology-only groups** — eligible Volcano workloads render `minMember: 0`, omit `minSubGroups`, and retain any required instance `subGroupSize` threshold; KAI renders zero `minMember` / `minSubGroup`. Koordinator's deployed PodGroup CRD (`scheduling.sigs.k8s.io`) permits `minMember: 0`, but its inspected scheduler baseline does not satisfy this KEP's lifecycle contract, so no topology workload is rendered. A future verified Phase 2 backend must also pass CRD dialect checks: the upstream scheduler-plugins CRD (`scheduling.x-k8s.io`) requires `minMember >= 1` and remains unsupported for topology-only groups. RBG never invents a threshold to bypass either gate.

The Volcano Phase 1 support matrix applies in addition to logical scope validation and the mandatory resource-request and startup-dependency checks in [Backend Capability Gates](#backend-capability-gates). A supported shape alone does not authorize rendering:

| Topology shape | Gang shape | Result |
| --- | --- | --- |
| Cross-role group, with or without instance topology children | None | One PodGroup; instance partitions retain local dispatch thresholds |
| Instance topology without cross-role topology | None | One PodGroup per constrained role, with label-partitioned instances and local dispatch thresholds |
| Cross-role group with optional instance topology children | Exactly the same role scope | One PodGroup; keep parent topology and propagate effective constraints into all gang partitions, retaining stronger child constraints and every `minReplicas` |
| Instance topology without cross-role topology | Gang covers those roles, possibly with additional roles | Gang PodGroup plus the same instance partitions; do not add cross-role topology |
| Disjoint topology and gang scopes | Any valid KEP-430 minima | Independent physical groups |
| Cross-role topology child inside a wider gang scope | All-or-nothing or per-role minima | `SchedulerUnsupported` in Phase 1 |
| Cross-role topology parent containing a narrower cross-role gang scope | All-or-nothing or per-role minima | `SchedulerUnsupported` in Phase 1; not enabled merely because scopes are nested |
| Any shape requiring two matching subgroup policies for one Pod | Any | `SchedulerUnsupported` |
| Any topology-covered Pod using the resource-empty/BestEffort path | Any | `SchedulerUnsupported`; no request injection or backfill workaround |
| Instance subgroup with a Pod-producing component declaring non-empty `startAfter` | With or without declared gang | `SchedulerUnsupported`; reject before workload creation or template update |

Every supported Volcano subgroup policy has a non-empty `matchLabelKeys` result and selectors disjoint from other policies. In the v1.14.4 source baseline, `getOrCreateSubJob` uses the first policy with non-empty match values; selector-only policies do not create the intended subgroup. Future cross-role subgroup support would need a shared compiler-injected group label plus a proven way to retain per-role gang minima. It is not part of the current compiler.

`subGroupSize` populates `SubJobInfo.MinAvailable`; the gang plugin's subgroup readiness checks and allocate action can discard an incomplete subgroup allocation. A four-Pod instance with resources for only three therefore cannot start those three merely because the parent `minMember` is zero. These behaviors are covered by scheduler-running tests, not only YAML golden tests. Reference baseline: [Volcano subgroup construction](https://github.com/volcano-sh/volcano/blob/v1.14.4/pkg/scheduler/api/job_info.go), [subgroup readiness](https://github.com/volcano-sh/volcano/blob/v1.14.4/pkg/scheduler/api/sub_job_info.go), and [gang plugin](https://github.com/volcano-sh/volcano/blob/v1.14.4/pkg/scheduler/plugins/gang/gang.go).

#### Backend Capability Gates

**Volcano: preserve the parent domain across scheduler restart.** In v1.14.4, [policy-matched subjobs](https://github.com/volcano-sh/volcano/blob/v1.14.4/pkg/scheduler/api/job_info.go#L1262-L1295) do not inherit the PodGroup topology automatically; only the default subjob does. [Domain recovery](https://github.com/volcano-sh/volcano/blob/v1.14.4/pkg/scheduler/framework/session.go#L354-L441) skips subjobs without their own topology, then reconstructs the parent's domain from recovered subjobs. A parent-only hard constraint with topology-free gang partitions can therefore lose its domain anchor after restart.

Before layout hashing or rendering, compute effective topology for every explicit subgroup policy beneath a topology-bearing PodGroup:

- Keep the parent's `networkTopology` on the PodGroup; subgroup copies do not replace the common-domain constraint across instances.
- If a subgroup has no topology, copy the parent's rendered constraint into that same policy. Do not add a second matching policy or change selectors, names, `subGroupSize`, `minSubGroups`, or `minMember`.
- For a hard parent, every child must have a hard bound at the same or a narrower level. Keep an existing stronger child hard bound. A preferred-only child receives the parent's hard bound and retains the `PreferredAbsorbed` diagnostic for its unanchored preference; it must not remain soft and bypass the recovery requirement. Existing invalid parent/child ordering is still rejected.
- For a soft parent, retain an existing child constraint; otherwise copy the parent's soft constraint. For a parent without topology, do not add a constraint to unrelated gang partitions. Pods in the default subjob keep the backend's verified parent inheritance; no artificial subgroup is needed.

These redundant inherited bounds add no new logical scope or gang threshold. They ensure that every surviving member contributes to domain recovery, including when only roles without an instance constraint survive. Canonical effective fields are part of the accepted layout, and group read-back verifies them. A backend unable to preserve this behavior reports `SchedulerUnsupported`. Real-scheduler restart, replacement, and scale-out tests are mandatory; retaining a PodGroup or freezing its name alone is insufficient.

**KAI: require unambiguous domain-path encoding.** In v0.18.6, [`calcDomainId`](https://github.com/kai-scheduler/KAI-scheduler/blob/v0.18.6/pkg/scheduler/plugins/topology/topology_structs.go#L96-L103) joins label values with `.` without escaping. Paths `["a.b", "c"]` and `["a", "b.c"]` both become `a.b.c`; the [topology builder](https://github.com/kai-scheduler/KAI-scheduler/blob/v0.18.6/pkg/scheduler/plugins/topology/topology_plugin.go#L92-L108) then merges nodes from distinct domains. These are legal Kubernetes label values, and alias validation or an accepted nodeLabel mapping cannot prevent the collision.

Phase 3 requires a fixed backend with an injective encoding of ordered label-value paths, used consistently for tree construction, domain lookup, and recovery. Unpatched v0.18.6 and unverified versions report `TopologyTranslated=False/SchedulerUnsupported` for all workload topology, including preferred-only rules and single-level inputs; Managed/Imported vocabulary handling may still work. No fixed version is certified here. Enable a deployment only after recording its verified image digest/version/profile and passing collision, nested-level, incremental-arrival, and restart tests together with the other KAI lifecycle tests. A one-time scan of current Nodes cannot certify the unpatched backend because later Node additions may introduce collisions before RBG reconciliation observes them. This KEP chooses the backend-fix gate rather than introducing a cluster-wide Node label-value admission policy or rewriting administrator labels. Existing accepted groups are retained if a deployment later fails the gate, but changing the active scheduler to an unpatched backend is an unsupported downgrade, not made safe by an RBG condition.

**Koordinator: vocabulary binding is not continuous placement.** The inspected baseline is commit `81e013476f441ae7f331789de8d2aa4890b2c641`. Its [topology workflow](https://github.com/koordinator-sh/koordinator/blob/81e013476f441ae7f331789de8d2aa4890b2c641/pkg/scheduler/plugins/coscheduling/core/network_topology_workflow.go#L42-L45) explicitly limits support to jobs with no Bound members and total membership equal to the minimum. The default `OnceSatisfied` path can [return before constructing topology context](https://github.com/koordinator-sh/koordinator/blob/81e013476f441ae7f331789de8d2aa4890b2c641/pkg/scheduler/plugins/coscheduling/core/core.go#L316-L346), and the topology solver plans pending Pods without pinning them to the domain of already-bound members. Initial placement in block A therefore does not constrain a later batch to A. Identical annotations, an accepted `minMember: 0`, or changing the gang match policy alone do not fix this.

On this baseline, all workload topology translation, including cross-role-only and preferred-only rules, reports `TopologyTranslated=False/SchedulerUnsupported` before group or Role writes. RBG does not silently accept a preference that the backend skips for later batches. Imported vocabulary may still become `Ready=True/NativeTopologyBound`; this does not bypass the workload gate. Phase 2 must identify a fixed scheduler version/profile and pass real-scheduler tests showing that hard constraints account for bound members across incremental arrivals, scale-out, replacement, and scheduler restart, while preferred constraints remain evaluated and declared gang minima remain intact. Topology-only support additionally requires genuine zero-threshold dispatch. No fixed version is certified by this KEP yet; unknown versions remain unsupported rather than being accepted solely because their CRD schema matches. The gate also requires the fail-closed native semantic-immutability guard and accepted mapping checks described under Koordinator Imported; continuous placement alone does not prevent same-UID `labelKey` or parent-chain drift. The Koordinator mappings below describe that gated target, not a supported baseline deployment.

**Volcano: reject the BestEffort/backfill path.** In v1.14.4, [allocate skips tasks with empty resource requests](https://github.com/volcano-sh/volcano/blob/v1.14.4/pkg/scheduler/actions/allocate/allocate.go#L259-L264), while [backfill selects from the full node list and directly allocates](https://github.com/volcano-sh/volcano/blob/v1.14.4/pkg/scheduler/actions/backfill/backfill.go#L64-L105). Hard topology filtering is registered through [job/subjob HyperNode gradient callbacks](https://github.com/volcano-sh/volcano/blob/v1.14.4/pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go#L278-L307), not an equivalent backfill predicate. Enabling the topology and gang plugins alone does not close this path.

Phase 1 requires every Pod covered by a topology group or instance partition to have non-empty effective resource requests under the tested scheduler's accounting and to avoid its BestEffort classification. Inspect every effective Pod template, including non-GPU components, after template references, patches, defaulting, and runtime injection are resolved; checking only a GPU container, the raw RoleSpec, or the Kubernetes QoS name is insufficient. Apply the check even when the role currently has zero replicas, because it may scale out later. Do not rely on a future LimitRange or admission sidecar to add otherwise missing requests. If effective requests cannot be established before workload writes, fail the capability check. The supported deployment must preserve those requests through Pod admission; request-removing external mutation is unsupported and must be excluded by deployment preflight/admission policy. RBG does not add artificial requests, drop topology, or disable backfill to claim support; disabling backfill alone leaves these tasks unschedulable. Unconstrained Pods, such as the example router, are not subject to this topology-specific check. Supporting BestEffort members later requires a backend fix and separate hard-domain tests covering backfill.

**Volcano: reject startup dependencies within an instance dispatch group.** `CustomComponentsPattern` may declare `rolebasedgroup.workloads.x-k8s.io/component-depends-on` with `startAfter`. The [RI creation path](../../pkg/reconciler/roleinstance/sync/instance_scale.go) waits for all named components to be Ready before creating dependent Pods. With A and B each producing one Pod and B starting after A, `subGroupSize: 2` prevents A from dispatching until B exists, while B cannot be created until A is Ready. `ImplicitInstanceGang` is a diagnostic, not permission to accept this deadlock.

For each Volcano instance subgroup in a topology-bearing plan, reject a non-empty `startAfter` on any Pod-producing component, whether the dispatch threshold is implicit or also requested by gang. Reuse the [component dependency parser](../../pkg/component-discovery/component_lifecycle.go) on the effective component definitions, including top-level annotation precedence and the Pod-template annotation fallback, rather than inspecting only one annotation location. Parse/validation failures must block compilation, not be interpreted as absent dependencies. This conservative gate applies before first acceptance and on every template/dependency update, including when current Pods happen to be Ready; a later cold start must remain possible. Empty/absent `startAfter` and `deleteAfter`-only configuration do not trigger this startup gate. Neither reducing `subGroupSize`, splitting the instance, nor deleting dependency annotations is an allowed fallback. This gate does not change topology-free KEP-430 behavior or claim to solve arbitrary application-level readiness cycles.

#### Generated Group Names

Legacy gang-only PodGroups keep the KEP-430 name `rbg.Name`. Topology-bearing PodGroups use a readable, collision-safe form:

```text
<truncated-rbg-name>-<placement-name>-<placement-hash>
```

- The RBG prefix is at most 26 characters.
- The placement name is at most 22 characters:
  - `r-<role.Name>` for a RoleSpec instance constraint;
  - `p-<policyRule.Name>` for a CoordinatedPolicy topology rule.
- Topology-bearing policy-rule names and Role names used as sources by a topology-bearing compiled plan must be DNS-1123 labels. RBG admission validates roles when its own declarations request topology; policy admission validates its topology-bearing rule names, and reconciliation checks all remaining compiler source roles once both resources are available, including gang partitions in the same plan. This adds no stricter naming admission to topology-free RBGs or gang-only policies and never renames existing KEP-430 groups. The compiler truncates source portions to 20 readable characters when rendering the placement name.
- The placement hash is the first six bytes of SHA-256 over the RBG UID and the full `PlacementGroup.ID`, rendered as twelve hexadecimal characters. Including the full logical-group identity — not only the RBG UID — makes the name collision-safe within one RBG, including when two policy rules share their first 20 characters.
- The total is at most 62 characters, below the Kubernetes DNS-label limit.

Per-dialect naming rules:

- **Volcano**: a gang-only single PodGroup keeps the KEP-430 name `rbg.Name` (equivalence is golden-tested). A cross-role topology-bearing PodGroup uses the placement form above, including when gang has exactly the same scope. A gang parent carrying only per-instance topology retains `rbg.Name` and the existing per-role policy names/partitions. Cross-role topology children under a wider gang parent are rejected, not assigned a subgroup name. Once accepted, names and partitions are frozen for that lifecycle.
- **Koordinator (gated Phase 2 target)**: there is no rendered root group object. After capability verification, topology-bearing GangGroup members use the bounded form:

  ```text
  k-<rbg[0:14]>-<role[0:14]>[-i<instance-index>]-<hash12>
  ```

  For `PartitionBy=none`, omit the instance segment. `hash12` is the first six bytes of SHA-256 over the RBG UID, the full role name, the instance index (or `none`), and the full `PlacementGroup.ID`. The instance index is rendered in decimal and is bounded by the RBG replica type, so the generated name remains below the 63-character limit even when the RBG and role names are each 63 characters. Gang-only rendering preserves the legacy KEP-430 name; this bounded form applies to topology-bearing member groups and their association annotations.
- **KAI**: the root PodGroup is named `rbg.Name`. SubGroup names are unique across the entire subGroup tree, not merely among siblings, and use kind-prefixed, length-bounded namespaces:

  ```text
  policy-<source[0:20]>-<hash12>
  instance-<role[0:20]>-<instance-hash12>
  role-<source[0:20]>-<hash12>
  ```

  Policy/role `hash12` is the first six bytes of SHA-256 over the RBG UID, full `PlacementGroup.ID`, and subgroup kind. `instance-hash12` additionally includes the actual RoleInstance UID; it is allocated only after that instance exists, not from a presumed integer index. Recreating a same-name instance produces a new subgroup identity. Readable source portions are truncated to 20 characters; names stay below 63 characters. Kind prefixes prevent policy/instance collisions. Parent references and Pod bindings use exactly the acknowledged compiler-generated names. Topology requires per-instance splitting only for roles carrying an instance constraint; declared gang may independently require per-instance partitions to preserve KEP-430 minima.

#### Translation Matrix

In the table below, `R` and `P` are portable names from `ClusterNetworkTopology.status.levels`. The compiler maps them to Volcano `tierName`, Koordinator `topologyLayer`, or stable nodeLabels in the KAI accepted snapshot; `K(R)` / `K(P)` denote those KAI mapping results, not alias strings.

All rendering entries require the capability gates above. Koordinator's inspected baseline and unpatched KAI v0.18.6 return `SchedulerUnsupported` for every `pack` combination. Their mappings describe gated Phase 2/3 targets, not supported output on those baselines. Volcano group-level hard fields must also propagate into explicit subgroup policies as described above.

| `pack` API (instance-level or cross-role) | Volcano | Koordinator | KAI (verified Phase 3 target) |
| --- | --- | --- | --- |
| `required: R` | `mode: hard` + `highestTierName: R` | Unsupported baseline; gated cross-role target: `[{R, MustGather}]` | `requiredTopologyLevel: K(R)` |
| `required: R` + `preferred: P` | `mode: hard` + `highestTierName: R`; P is **not anchored**; set `TopologyTranslated=True` with reason `PreferredAbsorbed` and emit a warning event | Unsupported baseline; gated cross-role target: `[{R, MustGather}, {P, PreferGather}]` | `requiredTopologyLevel: K(R)` + `preferredTopologyLevel: K(P)` |
| `preferred: P` only | `mode: soft` (no tier threshold); P is **not anchored**; set `TopologyTranslated=True` with reason `PreferredAbsorbed` | Unsupported baseline; gated cross-role target: `[{P, PreferGather}]` | `preferredTopologyLevel: K(P)` |
| Level validation fails | Translation fails; condition/event; role update gated; no rendering | same | same |
| Scheduler lacks required capability | `SchedulerUnsupported`; no approximation | same | same |
| Covered Pod uses the resource-empty/BestEffort path, or instance startup dependency gate fails | `SchedulerUnsupported`; retain accepted state on updates | Baseline already unsupported | No additional Volcano-specific restriction |

Volcano's generic lower-tier scoring is an implementation heuristic, not a translation of the user's preferred level. The compiler returns a set of diagnostic codes so `PreferredAbsorbed` and `ImplicitInstanceGang` can coexist. For successful translation, the condition reason is `PreferredAbsorbed` when present, otherwise `ImplicitInstanceGang` when present, otherwise `TranslationSucceeded`; the condition message lists all diagnostics and affected roles/rules. Each diagnostic emits its own edge-triggered warning event. Scheduler-running tests verify preferred scoring and instance-local dispatch independently of YAML translation.

#### Translation Examples

The examples below focus on scheduling fields and omit common ownership metadata. Every actual PodGroup generated for a topology plan must include an RBG ownerReference, the `topology.x-k8s.io/rbg-uid` label, and the `topology.x-k8s.io/group-protection` finalizer; bindings may be published only after successful read-back.

Input — the objects from [Attachment Points](#attachment-points): a `pd-colocation` rule over prefill+decode with `required: block` + `preferred: rack`, prefill instance-level `required: rack`, and no gang configured (router 1 pod outside the rule; prefill 2 instances × 4 pods; decode 2 instances × 2 pods). Pod templates are omitted from that placement fragment. The Volcano example assumes every prefill/decode Pod has non-empty effective requests and avoids the BestEffort path, and no Pod-producing component in an instance subgroup declares a non-empty `startAfter`; a real manifest must pass both checks before this output is produced.

**Volcano** — the rule's PlacementGroup becomes one topology-bearing PodGroup. No cross-instance gang is declared: `minMember` is zero and `minSubGroups` is omitted. Prefill's `subGroupSize: 4` still imposes an instance-local dispatch threshold; translation reports both `PreferredAbsorbed` and `ImplicitInstanceGang`:

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
  minMember: 0                   # no cross-instance gang threshold; subGroupSize still sets the instance-local threshold
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

The router declares no topology constraint and is not bound to a PodGroup. A rule-external role with instance topology gets a separate PodGroup using the same partition pattern. Every instance-level policy sets `subGroupSize` from `ComputeSubGroupSize(role)`. Group identity comes from the label values, not this size: omitting the size defaults the readiness threshold to one, rather than creating one subgroup per Pod. Keeping size four deliberately requires the subgroup's four members to satisfy dispatch readiness. Omitting `minSubGroups` removes the minimum number of ready instances, not the local four-member requirement.

RBG-generated HyperNodes always maintain `spec.tierName`; if the referenced portable level is not present in `ClusterNetworkTopology.status.levels`, validation fails explicitly before rendering — there is no integer `highestTierAllowed` fallback.

If gang is added on exactly the example's prefill+decode scope, keep the parent hard block constraint and all per-role minima. Prefill's policy keeps its stronger hard rack constraint; decode's gang policy must explicitly carry `networkTopology: {mode: hard, highestTierName: block}` even though decode declares no instance constraint. With no instance topology on either role, both gang policies inherit hard block. If a child declares only a preferred rack, it also receives hard block and reports `PreferredAbsorbed`. This is one effective constraint per existing partition, not a second topology membership. In the topology-only example above, decode instead uses the default subjob, which inherits the parent constraint in the verified Volcano baseline.

**Koordinator** — both the full example and its cross-role-only variant are **unsupported on the inspected baseline** and return `SchedulerUnsupported` before any PodGroup or Role write. The full example also requires an instance-level form that Koordinator does not expose; removing that instance constraint does not solve the backend's cross-batch limitation.

The following is a **future Phase 2 dialect illustration, not runnable supported output**. It assumes a scheduler that has passed the continuous-placement and zero-threshold tests in [Backend Capability Gates](#backend-capability-gates), with the native semantic-immutability guard installed and verified. Remove prefill's instance constraint and reference Imported `koordinator-default` with `required: block-layer` and `preferred: rack-layer`. With `PartitionBy=none` and no gang-derived partition source, the intended members are role-level `k-infer-0-prefill-<hash12>` and `k-infer-0-decode-<hash12>`. They carry identical `groups` and `network-topology-spec` annotations, but those annotations alone do not prove that existing and future members remain in a common domain:

```yaml
# Phase 2 target only; not emitted for the inspected unsupported scheduler baseline.
apiVersion: scheduling.sigs.k8s.io/v1alpha1
kind: PodGroup
metadata:
  name: k-infer-0-prefill-<hash12> # one bounded role-level GangGroup member (2 total: prefill + decode)
  annotations:
    gang.scheduling.koordinator.sh/groups: '["default/k-infer-0-prefill-<hash12>", "default/k-infer-0-decode-<hash12>"]'
    # required=block-layer → MustGather@BlockLayer; preferred=rack-layer → PreferGather@RackLayer
    # Assumes native levels BlockLayer/RackLayer and portable names block-layer/rack-layer
    gang.scheduling.koordinator.sh/network-topology-spec: |
      { "gatherStrategy": [
          {"layer": "BlockLayer", "strategy": "MustGather"},
          {"layer": "RackLayer",  "strategy": "PreferGather"} ]}
spec:
  minMember: 0                   # topology-only: no gang requirement; Koordinator CRD dialect
```

For this Imported example, the native layers `BlockLayer` and `RackLayer` publish portable aliases `block-layer` and `rack-layer`. The input constraint must use those aliases, and the compiler maps them back to native layer names as shown; Managed `block`/`rack` names are not automatically interchangeable with Imported aliases.

The `scheduling.sigs.k8s.io` API group and `minMember: 0` describe only Koordinator's deployed PodGroup CRD dialect, not a supported scheduling lifecycle. Even after the continuous-placement gate is satisfied, the upstream scheduler-plugins dialect (`scheduling.x-k8s.io`, `minMember >= 1`) remains unsupported for topology-only groups; the compiler must not invent a threshold.

**KAI** — this is a gated Phase 3 illustration for a backend that passes the domain-encoding and lifecycle checks, not output supported on unpatched v0.18.6. It shows a steady-state snapshot after actual RoleInstances exist and their concrete bindings have been acknowledged, not a prerequisite for creating the shared Role template. SubGroups nest, so the plan lands in a single PodGroup: the rule becomes a nested subGroup spanning prefill+decode with its own `topologyConstraint`, per-instance subGroups carry instance packing, and uncovered roles only appear as sibling subGroups when they declare their own instance-level constraints. The root PodGroup is named `rbg.Name`; subGroups use the kind-prefixed naming rule so names are unique across the whole tree. Decode has no instance constraint, so it appears as one role-level child of `policy-pd-colocation-<hash12>` and is not split into `decode-0`/`decode-1`. KAI native level fields use nodeLabels from the accepted mapping; in this example, portable `block` / `rack` map to `topology.example.com/block` / `topology.example.com/rack`, respectively. `topology` still uses the KAI `Topology` name recorded in `status.resources`, not `TopologyConstraint.topologyName`:

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
      requiredTopologyLevel: topology.example.com/block
      preferredTopologyLevel: topology.example.com/rack
  - name: instance-prefill-<instance0-hash12> # per-instance leaf subGroups carry instance packing
    parent: policy-pd-colocation-<hash12>
    minMember: 0
    topologyConstraint:
      topology: gpu-fabric
      requiredTopologyLevel: topology.example.com/rack
  - name: instance-prefill-<instance1-hash12>
    parent: policy-pd-colocation-<hash12>
    minMember: 0
    topologyConstraint:
      topology: gpu-fabric
      requiredTopologyLevel: topology.example.com/rack
  - name: role-decode-<hash12>   # no instance constraint: one role-level child, not per-instance
    parent: policy-pd-colocation-<hash12>
    minMember: 0
```

KAI explicitly renders `minMember: 0` on topology-only leaf subgroups and `minSubGroup: 0` on topology-only parent groups. Nonzero thresholds come only from gang configuration. With gang on the same scope, the compiler must preserve every per-role minimum and instance completeness requirement, adding role/instance structure where needed rather than merely filling a root total. The example's `instance0-hash12` / `instance1-hash12` denote hashes of two observed RoleInstance UIDs, not ordinal-based identities.

### Observability

- **ClusterNetworkTopology status**:
  - `Ready` — whether the topology resource and vocabulary are ready for workload resolution; per-workload backend capability checks remain mandatory. Reasons distinguish `TranslationSucceeded`, `NativeTopologyBound`, `ModeUnsupported`, `SchedulerMismatch`, `NativeTopologyNameInvalid`, `NativeTopologyNotFound`, `NativeTopologyConflict`, `NativeTopologyInvalid`, and `TopologyInUse`.
  - `NativeTopologyDrift` — imported resource changes or external edits to generated resources.
  - `status.levels` — the only level vocabulary exposed to workloads.
  - `status.resources` — the actual native GVK/name/UID or Volcano aggregate; this is diagnostic and not a workload input.
  - `status.consumers` — UID-keyed retention reservations, including incomplete first acceptance. `TopologyInUse` identifies blocking consumer UIDs; cleanup errors identify the native object or failed API operation.
  - No `phase` field is used; `Ready` plus `reason` avoids duplicate and coarser state.
- **RBG conditions**:
  - `GangConfigured` — retained from KEP-430 for gang-only failures.
  - `TopologyTranslated` — present when topology is configured, an explicitly required policy is pending, or provenance bootstrap is blocked; `False` reasons include `TopologyUnresolved`, `TopologyNotReady`, `IncompatiblePlacementGroups`, `IncompatibleTopologyNames`, `RoleUnresolved`, `InvalidLevelOrder`, `LevelUnresolved`, `InstanceTopologyUnsupportedWorkload`, `SchedulerUnsupported`, `TopologyPolicyPending`, `TopologyPolicyLate`, `TopologyBootstrapBlocked`, `TopologyMappingChanged`, `PlacementLayoutChanged`, and `FeatureDisabled`. Successful translations report all compiler diagnostics; `PreferredAbsorbed` takes condition-reason precedence over `ImplicitInstanceGang`, without suppressing either warning event.
  - `SchedulerUnsupported` messages distinguish unavailable continuous-placement support, unsafe KAI domain encoding, unavailable Volcano restart-domain recovery, resource-empty/BestEffort members, and component startup dependencies conflicting with instance dispatch. Include the scheduler version/profile and relevant role/component and `startAfter` edge; topology `Ready=True` does not suppress these failures.
  - The accepted topology/layout record and hash identify the frozen contract; KAI and the gated Koordinator compiler retain their complete semantic mappings rather than reconstructing historical semantics from the latest desired spec. Legacy bootstrap records `none` with the RBG UID; late-policy diagnostics distinguish legacy bootstrap from fresh acceptance.
  - `PlacementDrained` — records deletion progress only while the RBG is terminating: `False/PlacementDraining` indicates drain in progress; the message for `False/PlacementDrainBlocked` identifies the blocking object UID or API error; `True/PlacementDrained` indicates that the deletion protocol has completed. This condition is not topology readiness and cannot be inferred from scale-to-zero.
- **RoleInstance condition**: `PlacementBound` and `status.placementBinding` are both owned by the placement reconciler; `PlacementBindingPending` means creation is not yet authorized, and `PlacementDraining` means deletion drain is in progress. Only True with a complete, UID/hash-matching payload constitutes a valid acknowledgement; the condition alone cannot authorize Pod creation.
- **Events**: failure transitions and compiler diagnostics emit edge-triggered warnings, following the KEP-430 event-spam analysis.
- **Placement gating is explicit.** A false `GangConfigured` or `TopologyTranslated` pauses RBG Role create/update. It does not imply that existing downstream controllers cannot replace Pods. An unacknowledged explicit instance binding separately gates that instance's Pod creation.
- Placement outcomes themselves (which domain a group landed in) remain owned by the scheduler (for example Volcano's allocated-HyperNode bookkeeping); RBG status links failure *reasons* back to the declaring object.

### Dependencies

- **`ClusterNetworkTopology` CRD and webhooks.** Creation requires the mutating webhook that stamps `spec.scheduler`; the validating webhook enforces mode exclusivity and immutability. Unknown or known-but-unsupported active schedulers reject topology creation. Supporting RBG admission also stamps and preserves creation provenance after the coordinated upgrade handover; accepted-record persistence remains active with the topology gate disabled.
- **A scheduler with topology-aware scheduling support.** Phase 1 uses Volcano v1.14.4 as the source/test baseline and requires `tierName`, subGroup-level `networkTopology`, and the configured topology-aware and gang plugins, plus effective subgroup inheritance, restart-domain recovery, resource-request, and startup-dependency checks above. Schema inspection detects missing fields, not enabled scheduler behavior. The deployment preflight/runbook must record the tested scheduler image version, profile/plugin/action configuration, and Pod-admission resource assumptions; unsupported or unverified combinations are not advertised as supported. The inspected Koordinator baseline is explicitly unsupported for workload topology; Phase 2 requires a verified continuous-placement backend. KAI requires a verified domain-encoding fix plus scheduler-running capability tests before enablement; unpatched v0.18.6 is not a supported workload backend.
- **Node labels maintained by administrators or discovery tools** (UFM, RoCE topology exporters, topograph). For Managed label-backed modes, nodes must carry the configured label keys. RBG does not discover or assign these labels.
- **Native topology CRDs** for the selected scheduler: KAI `Topology`, Koordinator `ClusterNetworkTopology` named `default`, or Volcano `HyperNode`. Imported mode additionally requires the external resource to exist before the RBG topology can become Ready. Managed lifecycle requires topology-UID resource watches, explicit deletion permissions, and cross-namespace consumer checks; controller permissions and cleanup handlers remain available after feature disablement.
- **Koordinator PodGroup CRD dialect**: the gated Phase 2 compiler targets Koordinator's deployed CRD (`scheduling.sigs.k8s.io`, `minMember: 0` allowed) and detects the installed dialect at runtime. This check is additional to continuous-placement verification, never a replacement for it. The upstream scheduler-plugins CRD (`scheduling.x-k8s.io`, `minMember >= 1`) does not support the topology-only output in this KEP.
- **KEP-430 gang semantics and PodGroup conventions**: the placement planner consumes `ResolveGangStrategy`; the compiler renders the PodGroup/subGroup objects and pod label conventions (`group-name`, `role-name`, `role-instance-name`). No second translator may modify those resources or pod annotations.
- **CoordinatedPolicy (KEP-30)** as the cross-role coordination surface: the policy binds to its RoleBasedGroup by identical name and namespace, so a topology rule's role set always resolves against exactly one RBG.

The required RBAC surface is listed below. These are controller service-account permissions, not tenant grants; existing KEP-430 permissions remain in effect. Native permissions are granted only for installed/configured backends, but permissions for an original backend must remain until its cleanup is complete.

| Resource / scope | Required verbs and purpose |
| --- | --- |
| `topology.x-k8s.io/clusternetworktopologies` (cluster) | `get`, `list`, `watch`, `patch`, `update` for observation and protection metadata; `/status`: `get`, `patch`, `update` for Ready/resources/consumers; `/finalizers`: `update` |
| `workloads.x-k8s.io/rolebasedgroups`, `roleinstances`, `coordinatedpolicies` (all managed namespaces) | `get`, `list`, `watch`, `patch`, `update` for contracts, identity, and finalizers; RBG/RI `/status`: `get`, `patch`, `update`; relevant `/finalizers`: `update` |
| Backing `roleinstancesets`, RIs, `apps/deployments`, `apps/statefulsets`, `apps/replicasets`, `apps/controllerrevisions`, `leaderworkerset.x-k8s.io/leaderworkersets`, and core `pods` (all managed namespaces) | `get`, `list`, `watch` for uncached UID/owner-chain drain checks; `delete` for owned creators/descendants in active drain, in addition to existing workload create/update permissions |
| Scheduler PodGroups: `scheduling.volcano.sh`, `scheduling.sigs.k8s.io` (gated Koordinator), `scheduling.run.ai` (KAI) | `get`, `list`, `watch`, `create`, `patch`, `update`, `delete`; `/finalizers`: `update` for protection and retirement; no scheduler-owned `/status` writes |
| Managed `kai.scheduler/topologies` or `topology.volcano.sh/hypernodes` (cluster) | `get`, `list`, `watch`, `create`, `patch`, `update`, `delete` for materialization, UID ownership checks, and explicit cleanup; no native `/status` writes |
| Imported `kai.scheduler/topologies` or `scheduling.koordinator.sh/clusternetworktopologies` (cluster) | `get`, `list`, `watch` only; no mutation/deletion permission is required by the import path |
| Core `nodes`; `apiextensions.k8s.io/customresourcedefinitions` (cluster) | `get`, `list`, `watch` for topology labels and capability/schema discovery; no mutation |
| Core `events` (managed namespaces) | `create`, `patch` for edge-triggered diagnostics |

Cross-namespace topology retention requires visibility into every namespace allowed to consume the cluster-scoped topology, including old backend resources. A deployment using a restricted namespace scope must reject consumers outside that scope; a label-filtered cache is not sufficient evidence of drain. RBAC cannot restrict mutation by ownership label or by Managed/Imported use of the same native GVR: when KAI Managed and Imported coexist, controller UID/mode checks enforce read-only Imported behavior despite broader service-account permissions. RBAC changes alone do not implement Koordinator semantic immutability; that requires the separate fail-closed native admission guard. Missing access blocks acquisition or cleanup, and feature disablement never removes cleanup permissions.

### Implementation Phases

- **Phase 1**: `ClusterNetworkTopology` CRD/webhooks/controller, atomic level list, hostname materialization, `TopologyConstraint` API, explicit `topologyPolicyRequired` dependency, persisted accepted layout, reference-drained cleanup, PlacementPlan resolver, and **Volcano Managed materialization plus the restricted compiler**. Deliver KEP-430-equivalence tests, effective-template resource and startup-dependency validation, and scheduler-running tests for subgroup thresholds, BestEffort/backfill exclusion, dependency-deadlock rejection, containment rejection, recovery, and layout freeze together. RBG drain, PodGroup protection, accepted status preservation, topology consumer reservations and explicit native cleanup, and background/foreground deletion recovery must also ship in this phase. Deliver effective subgroup inheritance and scheduler-restart domain tests before enabling same-scope gang/topology composition; shared lifecycle protection cannot wait until the KAI phase. The admission provenance marker, conservative legacy bootstrap, gate-independent accepted-record persistence, upgrade handover, and RBAC/drain checks also ship in Phase 1.
- **Phase 2**: **Koordinator Imported vocabulary binding**, followed by workload compilation only after a fixed scheduler version/profile passes continuous-placement, zero-threshold, preferred-behavior, gang-preservation, and native semantic-immutability/mapping-drift tests. The inspected baseline and unknown versions remain `SchedulerUnsupported` for workload topology. No currently verified fixed version is identified here; the PodGroup compiler and illustrative mappings must not be enabled on CRD compatibility alone.
- **Phase 3**: **KAI Managed and alias-required Imported bindings**, stable nodeLabel compilation and accepted mapping freeze, UID-based explicit instance handshake, active RI deletion and multi-writer status merging, and Stateful/Stateless/surge lifecycle tests. KAI v0.18.6 is only the source baseline for alias/nodeLabel semantics and the domain-encoding defect; unpatched versions remain `SchedulerUnsupported` for workload topology. Enable KAI workload translation only with a verified unambiguous-encoding backend and passing collision, handshake, native-retention, deletion, alias-swap, and acknowledgement-preservation tests.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to existing tests to make this code solid enough prior to committing the changes necessary to implement this enhancement.

#### Unit tests

1. ClusterNetworkTopology admission: the mutating webhook stamps the active scheduler; unknown and known-but-unsupported schedulers (including native kube-scheduler) reject create without expanding the enum; `levels` and `topologyBinding` are mutually exclusive; level names/keys are unique; `kubernetes.io/hostname` is last; Koordinator bindings are `default`; Volcano Imported and Koordinator Managed are rejected.
2. ClusterNetworkTopology immutability: scheduler, levels, and topologyBinding cannot be updated in place; generated schemas use atomic level lists, order is preserved through server-side apply, and reordering/duplicate levels are rejected.
3. Managed KAI translation: `TopologyLevel.alias` equals the RBG level name, nodeLabels come from spec, the topology-UID label is set, ownerReferences are absent, and external name/UID conflicts report `NativeTopologyConflict`.
4. Imported KAI aliases: valid native aliases become ordered status levels; missing, duplicate, oversized, or invalid aliases report `NativeTopologyInvalid` with no nodeLabel fallback. Include `topology.example.com/rack` and `kubernetes.io/hostname` without aliases, then add valid native aliases and verify recovery without RBG editing the native CR. Valid alias drift is reported; same-name native recreation does not silently rebind the recorded UID.
5. Imported Koordinator: only `default` is accepted; parent-chain order and portable aliases form `status.levels`; non-default names report `NativeTopologyNameInvalid`. Test alias-to-native-layer mapping as a pure Phase 2 rendering fixture, separately from the runtime gate that rejects workload topology on the inspected baseline even when the Imported CR is Ready.
6. Volcano materialization: unique domain prefixes remain distinct when rack names repeat under different zones; tiers include the configured hostname level. Test host-only topology, hostname different from Node name, missing hostname exclusion, duplicate hostname rejection before writes, no-host multi-Node leaves, empty participating set, exact Node membership, label correction recovery, stale-domain deletion, ownership conflicts, and drift restoration.
7. Placement planning: equal scopes merge gang and topology; disjoint scopes stay independent; containment builds a tree; partial overlap is rejected with both declaring rules in the message. Scope comparison must include `PartitionBy`, not only `Roles`.
8. KEP-430 equivalence: gang-only inputs produce byte-for-byte the current single PodGroup rendering before topology features are enabled.
9. Translation matrix: every API combination (`required`, `required+preferred`, `preferred`-only) × every dialect produces the expected supported rendering or `SchedulerUnsupported`. The inspected Koordinator baseline and unpatched KAI v0.18.6 reject all combinations; hypothetical capable-backend fixtures are not proof of runtime support. Eligible Volcano preferred cases report `PreferredAbsorbed`.
10. Workload semantic validation: missing topology CR, topology not Ready, unknown status level, `preferred` broader than `required`, reversed parent/child nesting, and inconsistent topology names produce the documented condition reasons; recovery is tested after fixing the topology or RBG.
11. Topology-only groups: Volcano omits cross-instance minima but retains `subGroupSize` for instance-local dispatch; KAI emits zero thresholds. Validate `ImplicitInstanceGang` for multi-Pod instance packing without declared gang, coexistence with `PreferredAbsorbed`, and edge-triggered diagnostics. Cover LeaderWorkerPattern and CustomComponentsPattern.
12. Generated names: two placement groups in the same RBG whose source names share their first 20 characters produce distinct names because the hash includes the full `PlacementGroup.ID`; per-dialect naming rules (Volcano gang-only vs topology-bearing, Koordinator member names, KAI root/subGroup names) are golden-tested. Admission/reconcile reject non-DNS-1123 topology compiler source names without tightening topology-free KEP-430 validation, and KAI cross-kind collisions (a rule named like a role instance) produce distinct kind-prefixed subGroup names with matching parent references and bindings. KAI and Koordinator tests use 63-character RBG/role/rule names and assert that every generated subgroup/member name remains a DNS-1123 label of at most 63 characters. Volcano tests use maximal topology and level names, dotted names (including `abcdefghijklmno.example.com` with a dot at the 16-character boundary), repeated path components, and prefixes that sanitize identically; assert bounded DNS-1123 HyperNode names, distinct identity hashes, and matching parent references. Admission tests reject oversized or malformed level names, node label keys, topology references, and instance topology on Deployment/StatefulSet/LeaderWorkerSet backings.
13. Runtime detection: Volcano field/schema checks and Koordinator PodGroup dialect checks reject missing capabilities. Preflight fixtures also cover compatible CRDs with missing topology/gang plugins; do not equate schema presence with runtime enforcement.
14. Finalizer lifecycle: workload references block deletion (`TopologyInUse`); Managed deletion removes generated resources; Imported deletion leaves the native resource untouched; CoordinatedPolicy UID-keyed release behavior remains as documented.
15. Feature-gate lifecycle: the gate defaults to false; admission rejects topology fields while disabled; rendering succeeds while enabled; disabling after acceptance gates Role updates without evicting running Pods; reenablement revalidates and resumes; and v1alpha1/v1alpha2 conversion plus API round-trips preserve old topology-free objects while disabled.
16. Koordinator Imported ordering and aliases: a child-first `networkTopologySpec` still produces parent-to-child `status.levels`; `NodeTopologyLayer` maps to `host`; CamelCase custom layers map to kebab-case aliases; cycles, broken parent links, disconnected chains, duplicate layers, and alias collisions report `NativeTopologyInvalid`.
17. Identifier validation: Kubernetes qualified label keys accept uppercase, `_`, `.`, and single-label prefixes (for example `example/Key` and `topology.example.com/Rack_1`) while rejecting name segments over 63 characters; KAI binding accepts `gpu-fabric.example.com` as a DNS subdomain and Koordinator binding accepts only `default`.
18. Scheduler-name ownership: after capability gates pass, Volcano, the future verified Koordinator compiler, and KAI inject their configured scheduler name into constrained, unconstrained, and unbound roles uniformly. The unsupported Koordinator baseline and unpatched KAI v0.18.6 write no templates; topology-free RBGs preserve KEP-430 behavior.
19. Volcano scope/threshold matrix: equal-scope composition preserves P>=2 instances and D>=1 instance independently; topology parent plus disjoint instance children compiles; wider/narrower cross-role containment is rejected. No supported policy has empty match values or overlaps another policy. A total pod minimum is not accepted as a substitute for per-role minima.
20. Layout freeze: gang threshold-only changes with identical layout are accepted; reparenting, partition changes, scheduler/profile changes, and physical group renames return `PlacementLayoutChanged` before writes. Replica count, Stateful ordinal, Stateless identity, and surge membership do not change the partition layout hash.
21. Explicit policy dependency: default false has no delay; true waits for topology-bearing policy without time-based fallback. Admission enforces RBG/RBGSet immutability and disabled-gate rejection. Late policy after role-only/none acceptance is rejected.
22. Explicit instance bindings: hashes include actual instance UID, distinguish same-name recreation, and cover random Stateless names and surge. Pending/incorrect UID/hash acknowledgement never authorizes a Pod; sibling bindings survive one-instance updates; retirement requires creator/Pod drain.
23. KAI mapping freeze: first acceptance stores the complete ordered alias/nodeLabel/depth mapping and native UID; required, preferred, and nested constraints all render nodeLabels. Cover valid `block`/`rack` alias swaps, `required: rack` alone, alias renames, and metadata-only updates to verify the mapping hash, `TopologyMappingChanged`, and absence of false positives. Stale topology status must not be accepted together with a different native generation.
24. RI deletion: enter drain before the terminating early return; delete all Pods by owner UID, including inactive/terminal/terminating Pods. Cover Pod UID preconditions, blocking finalizers, absent or terminating groups, groups written without an acknowledgement, feature disablement, and new same-name RIs; never create replacements or clean up sibling bindings.
25. Status ownership: ordinary RI reconciliation and Pod readiness updates after acknowledgement do not clear the payload; stale caches, conflict retries, and concurrent restart tracking/custom condition updates preserve both writers' fields. Old True values must not overwrite invalidation or termination; True without a payload does not authorize creation. RBG status updates likewise preserve the accepted record and drain marker.
26. RBG deletion state machine: protection precedes group/Role writes; group-protection and ownerReference coexist; drain follows creators/Pods → groups → drain marker → RBG finalizer. The policy may be released when the same-UID RBG is terminating with marker=True, without waiting for the RBG to disappear; zero replicas do not satisfy this condition. Cover acceptance interruption, UID mismatch, and the distinction between NotFound and API/CRD errors.
27. Volcano effective requests: reject a covered Pod that enters the resource-empty/BestEffort path, including a non-GPU component among GPU components, explicit zero requests, and request-removing template patches. Cover defaulted requests, runtime-injected containers, template references, zero-replica roles, and unresolved effective requests. Do not infer eligibility solely from QoS or presumed future admission defaults; unconstrained roles remain unaffected.
28. Volcano startup dependencies: A and B each produce one Pod, B has `startAfter: [A]`, and the instance has topology but no declared gang. Expect `SchedulerUnsupported` before acceptance, not `ImplicitInstanceGang` success. Cover explicit gang on the same subgroup, top-level versus template annotation precedence, malformed dependency data, positive component sizes, and updates after existing Pods are Ready. Absent/empty `startAfter` and `deleteAfter`-only data do not trigger this gate; rejected candidates never lower thresholds or rewrite dependencies.
29. Koordinator capability gating: Ready Imported topology plus either PodGroup CRD dialect is insufficient. Reject the inspected baseline and unknown versions for cross-role-only, required-only, required+preferred, and preferred-only rules, with or without gang. Keep vocabulary readiness and workload capability status separate.
30. Topology retention: install `topology-protection` before native writes/Ready; native resources have topology-UID labels and no ownerReferences. Reservation acquisition conflicts with deletion, preserves concurrent consumers, and precedes acceptance. Cover interruption before accepted status, active zero replicas, same-name/new-UID RBGs, cross-namespace references, and release after `PlacementDrained=True`; no TTL-based release. Unexpected unregistered references block cleanup. Only after consumers drain may Managed native UIDs be deleted; Imported resources remain untouched.
31. Volcano effective subgroup topology: hard parent plus topology-free gang children copies the parent into every policy without changing minima or partitions; stronger child hard bounds survive, preferred-only children receive the parent hard bound with `PreferredAbsorbed`, and a soft parent introduces no hard constraint. Include no parent topology, default-subjob inheritance, mixed constrained/unconstrained roles, and canonical layout/read-back checks.
32. KAI encoding gate: reject unpatched v0.18.6 and unverified backends even with Ready vocabulary, a single level, or currently collision-free Nodes. Fixed-backend fixtures distinguish `["a.b", "c"]` from `["a", "b.c"]` and preserve genuinely identical paths, repeated names under distinct parents, nested constraints, and portable alias mappings. No label rewriting or controller-only Node scan bypasses the gate.
33. Upgrade bootstrap: CREATE stamps the configured `v1:<handover-uuid>` marker, UPDATE cannot add/remove/change it, RBGSet children are independently stamped, and conversion preserves it. Recordless old RBGs (missing/prior-epoch marker) persist `none` even with an already-present topology policy, zero replicas, no Pods, or retained templates. Include never-launched old objects, existing accepted records, unknown marker formats/versions, unavailable epoch configuration, failed status writes, UID races, and restarts. Supporting controllers persist `none` with the gate disabled; ordinary restarts reuse the epoch and reupgrade after an older controller rotates it.
34. Late-policy recovery: an unselected rejected policy acquires no topology finalizer or reservation; deleting it resumes the existing `none`/role-only contract after validation. An interrupted protected first acceptance retains its protection until the documented drain, and cannot use this recovery shortcut.
35. Koordinator semantic identity: complete native layer names, `labelKey` arrays, parent links, and derived alias/depth mapping enter the accepted hash. Same-UID key swaps and parent-chain changes are semantic changes even with unchanged `status.levels`; metadata-only changes are not. A deployment without the native immutability guard fails the Phase 2 gate.

#### Integration tests

1. envtest ClusterNetworkTopology lifecycle: create the CR through admission, wait for Ready/status.levels/resources, update Nodes for Volcano materialization, and assert the generated scheduler objects field-by-field.
2. envtest workload rendering: create a Ready topology plus RBG with both attachment points, assert the rendered PodGroups (PlacementGroup-derived topology fields and any PodGroup created for rule-external instance constraints) field-by-field.
3. Failure gating: before initial acceptance, invalid topology/plan prevents Role and managed Pod creation. After acceptance, make topology NotReady and delete a Pod: downstream replacement may proceed with the original membership while RBG Role updates remain paused. Also cover already-issued scale/rollout work and static Deployment/StatefulSet/LeaderWorkerSet bindings; no unbound fallback or accepted-group deletion is allowed.
4. Imported drift: modifying the native resource or deleting/recreating it with the same name updates ClusterNetworkTopology status, reports drift or not-found, and gates workload rendering.
5. Managed drift: externally edit a generated KAI Topology or Volcano HyperNode and verify the controller restores it while reporting `NativeTopologyDrift`.
6. Scheduler mismatch: change the controller's active scheduler configuration after a topology is created; normal reconcile reports `SchedulerMismatch` and does not reinterpret or mutate the old backend resource. Then delete and drain consumers and the topology CR; explicit cleanup must still use the original scheduler identity, preserve Imported resources, and release finalizers only after the retention protocol completes.
7. Lifecycle immutability: in-place topology removal or change is rejected by admission; the active policy's finalizer remains until the RBG with the recorded UID disappears or its terminating deletion flow persists `PlacementDrained=True`; a same-name replacement RBG does not inherit the old contract, and scale-to-zero does not release the policy early.
8. Creation order: with `topologyPolicyRequired: true`, create the policy after the RBG and advance time beyond 60 seconds; the RBG remains pending until a topology-bearing policy exists, then accepts it. With false and no policy, role-only/none acceptance is immediate; a later topology policy reports `TopologyPolicyLate`. Test policy event enqueueing without an RBG spec edit, RBGSet propagation, uncached-read/cache disagreement, and API read errors.
9. Binding ordering: static bindings are injected before Role creation. Explicit instance templates initially contain only the accepted descriptor; a newly created RoleInstance produces no Pods until the compiler persists its concrete UID/hash-matching binding. Do not wait for PodGroup scheduling readiness before Pods exist. Test read-back failure, PodGroup UID replacement, controller restart, Stateless allocation, Stateful surge, and same-name instance recreation.
10. Controller rollback safety: create a topology-bearing RBG, drain/delete it, wait for managed workloads and Pods to disappear, release topology finalizers, then run an older-controller fake that ignores topology fields and assert it has no topology-bearing Role to create or update. A parallel test documents that leaving the RBG active is rejected by the preflight/runbook check.
11. Feature-gate lifecycle in envtest: gate default, disabled admission, enabled rendering, disable-after-acceptance, and reenablement; include v1alpha1/v1alpha2 conversion and API round-trip checks while the gate is disabled.
12. Finalizer escape hatch: apply the documented atomic UID/resourceVersion/finalizer-value tests plus removal to a CoordinatedPolicy carrying unrelated finalizers and verify that only `topology.x-k8s.io/policy` disappears. Between read and patch, separately remove an earlier finalizer, reorder the array, change resourceVersion, delete the target entry, and recreate the policy with a new UID; each stale patch must fail without changing any finalizer. A fresh snapshot may be patched only after rechecking eligibility. The runbook test forbids unguarded index removal and clearing the entire finalizer array.
13. Layout retention: start instance-only topology on P without a cross-role topology rule, pause rollout, then add a gang over P/D/R. This is a supported initial shape but would move existing P instances into another PodGroup, so assert `PlacementLayoutChanged` before backend mutation. Old bindings/groups and recovery remain intact. Separately test that adding a wider gang to cross-role P/D topology returns `SchedulerUnsupported`, also without cleanup. Reverting either rejected change resumes the accepted layout; threshold-only updates and scale-out remain supported when layout is unchanged.
14. Binding retirement: exercise the actual Stateful/Stateless scale-in and recreate deletion entry points without pre-deleting dependent Pods in the test. Assert that RI drain actively issues Pod deletes and retains the subgroup/finalizer until all Pods disappear. Use a temporary Pod finalizer to simulate slow deletion and restart midway; verify no replacements, no lost sibling subgroups, and a new UID on same-name recreation. Also delete the RI after the group write but before acknowledgement and confirm it does not wait for a Bound state that will never arrive.
15. Feature disablement with downstream work: retain groups and previously acknowledged bindings, permit recovery under those bindings, and leave new explicit instances pending. Reenablement validates the accepted layout before new acknowledgements or Role updates. Separately delete an RBG/RI after disabling the gate and confirm that cleanup branches, watches, permissions, and five-second retries remain effective.
16. RBG drain: envtest verifies each write/read-back and conflict recovery step, explicitly simulating GC events because envtest does not include kube-controller-manager. Cover background/foreground deletion requests, GC marking PodGroups terminating first, a policy with a blocking ownerReference, old rollout/template references, and restarts at every persistence boundary. Assert that the accepted record remains readable until groups actually disappear, the policy and RBG do not wait on each other, and other finalizers are preserved; e2e tests separately verify real GC ordering.
17. KAI alias swap: preserve the native UID and set of alias names while swapping the block/rack mapping. The old RBG reports `TopologyMappingChanged` without changing groups/templates, and the accepted rack binding retains the original rack nodeLabel. Cover status that has not yet refreshed, workloads with required but no preferred, restart during drift, restoration of the original mapping, and a new RBG accepting the new mapping in a separate lifecycle.
18. Multi-writer status: exercise the full sequence of placement acknowledgement → ordinary RI status update → concurrent Pod Ready/restart/custom condition updates → controller restart. Assert that the payload and `PlacementBound` remain consistent and all writers' fields are preserved; races between acknowledgement writes and RI/RBG deletion must not restore a terminating instance to Bound.
19. Volcano capability rejection: before first acceptance, effective-template BestEffort or conflicting `startAfter` inputs produce `SchedulerUnsupported` without groups or Roles. After valid acceptance, remove requests or add a startup dependency without changing the placement hash; verify that existing groups/templates remain unchanged, the update is blocked, and deletion still drains. Restore eligible inputs and verify recovery. Exercise actual template-reference, patch, and runtime-injection paths, not only hand-built RoleSpecs.
20. Koordinator baseline gate: import a valid `default` topology and observe Ready, then reconcile topology-only and gang+topology RBGs with the inspected scheduler profile. Both remain `SchedulerUnsupported` with no workload writes. Changing only the CRD dialect, `minMember`, or gang match policy must not enable compilation. A fake capable-backend fixture tests only plumbing, not Phase 2 readiness.
21. Topology deletion races: run acquisition and foreground/background topology deletion concurrently; either reservation wins and retains native resources, or deletion wins and no acceptance/workload write occurs. Simulate GC explicitly in envtest and assert that native objects have no ownerReferences or deletionTimestamp. Restart after reservation but before accepted status, merge concurrent Ready/consumer updates, and delete the RBG to complete empty or populated drain. Cover multiple namespaces, errors, feature disablement, scheduler mismatch, and slow native finalizers; topology removal waits for all consumers and native UIDs, without RBG/topology circular waits.
22. Volcano inherited constraints: render same-scope gang plus cross-role topology with no instance constraint, with one stronger child, and with a preferred-only child. Assert every explicit subgroup's effective topology and unchanged per-role minima, including after threshold-only updates and read-back retries. A stale parent-only group cannot pass verification.
23. KAI fixed-backend gate: Managed and Imported vocabularies may become Ready on the unpatched baseline, but no workload groups/templates or new acknowledgements are emitted. A capability change after acceptance retains existing state and permits drain; fake fixed-backend fixtures test compiler plumbing only, not safe live scheduler downgrade.
24. Legacy upgrade: seed old-controller workloads without an accepted record and create a matching topology policy before the supporting controller starts. Verify bootstrap records `none` before candidate resolution, reports `TopologyPolicyLate`, and leaves existing groups/templates unchanged. Repeat with zero replicas, retained creators but no Pods, never-launched old RBGs, stale caches, status conflicts, and controller restart. Deleting the rejected policy resumes KEP-430 reconciliation without topology finalizers; a newly created current-epoch RBG may accept topology normally. Simulate downgrade losing accepted status and reupgrade with a new epoch; a surviving old marker must still bootstrap to `none`.
25. Koordinator semantic guard: native admission rejects same-UID `labelKey` and parent-chain edits, including swaps preserving portable names, while allowing metadata-only updates. In a deliberately unsupported guard-bypass fixture, confirm `TopologyMappingChanged` retains accepted state; do not claim this detection prevents scheduler races. Missing guard/preflight evidence blocks workload compilation but not vocabulary-only import.
26. RBAC: verify the documented resource/verb matrix and uncached cross-namespace visibility with restricted service accounts. Imported-only reconciliation requires no native write privileges; missing read/status/finalizer/delete permission blocks the relevant acquisition/drain step without treating Forbidden as NotFound. Repeat cleanup after feature disablement and scheduler mismatch.

#### e2e tests

1. Volcano cluster with labeled Nodes: create a Managed ClusterNetworkTopology, verify generated HyperNodes/tierNames, deploy the example RBG, and verify each prefill instance's pods land within one rack-tier HyperNode and the rule's prefill+decode pods within one block-tier HyperNode.
2. Insufficient domain capacity: hard constraint → pods stay Pending, condition/event explains; soft constraint → cross-domain placement allowed.
3. Node label drift: adding/removing/changing a topology label updates HyperNode membership without silently changing workload constraints.
4. Unknown status level: explicit error surfaced before initial workload creation; after acceptance, verify upper-level gating and persisted-binding recovery separately.
5. Volcano instance-local threshold: with gang plugin enabled, `minMember: 0`, omitted `minSubGroups`, and `subGroupSize: 4`, provide capacity for only three resource-requesting Pods. None is committed; adding the fourth Pod's capacity permits dispatch. Repeat with staggered Pod arrival and a multi-component instance.
6. Volcano composition: for equal topology/gang scope, verify two P instances and one D instance are independently required; excess P Pods do not substitute for D. Wider gang plus narrower cross-role topology fails before workload creation rather than silently dropping topology or per-role minima.
7. Lifecycle: recover a deleted Pod under an accepted binding, scale out, perform a surge rollout, and verify required domain membership. With topology NotReady, confirm retained groups and the documented downstream behavior; do not infer global suspension from an RBG condition.
8. Host materialization: verify singleton placement for hostname-level constraints, host-only definitions, missing/duplicate hostname handling, and recovery after correcting labels.
9. Deployment preflight: install compatible CRDs but omit the required scheduler plugin configuration; preflight must not certify the deployment. Record the tested image/profile/plugin configuration for the successful run.
10. KAI phase gate: real-scheduler tests cover acknowledged Stateful/Stateless/surge instance groups, sibling-safe updates, and reference-drained scale-in before enabling the KAI instance capability.
11. Real GC deletion: delete RBGs that still have Pods using background and foreground propagation, restarting the controller midway. Cover static backings and KAI RIs, policy blocking ownerReferences, and slow Pod finalizers. Verify that RBGs/accepted records and PodGroup objects do not disappear before references drain, and that all topology-owned finalizers are eventually released normally without manual intervention. Repeat deletion with the gate disabled and topology NotReady.
12. KAI alias swap with a real scheduler: first accept an instance with only `required: rack`, swap valid block/rack aliases, and then delete one of its Pods. The replacement must still satisfy the common-domain constraint of the original rack nodeLabel, never relax to the broader block. Repeat before the controller observes drift and after restart; assert that native group fields never switch to aliases and new explicit instances receive no acknowledgement.
13. Volcano BestEffort boundary: enable backfill and submit hard cross-role and instance topology with at least one resource-empty covered Pod, including mixed GPU/non-GPU components. RBG rejects before workload creation. With eligible non-empty requests, place selectors in mutually incompatible domains and verify that hard placement never crosses the boundary. Check the admitted Pod requests against the preflight assumptions. Any future BestEffort support must test actual backfill dispatch, not merely remove the rejection or turn backfill off.
14. Volcano startup deadlock boundary: with sufficient resources, topology-only CustomComponents A/B and B `startAfter: [A]` must fail fast without creating a permanently Pending A Pod. Removing the dependency allows the full subgroup to dispatch; adding it to a running accepted workload blocks the template update. Repeat cold creation after controller restart and cover `deleteAfter`-only configuration as a non-conflicting startup case.
15. Koordinator Phase 2 gate: first verify rejection on the inspected baseline despite Ready vocabulary and compatible CRDs. For a proposed fixed version, place initial members in block A, leave spare capacity only in block B, then test incremental arrivals, scale-out, single-Pod replacement, rolling/surge updates, and scheduler restart. Hard-constrained additions must remain Pending rather than leave A while surviving members remain there, and resume when capacity in A returns. Also verify preferred evaluation after the first batch, genuine zero-threshold topology-only dispatch, and every declared gang minimum. Record the passing scheduler image/profile before enabling workload topology; CRD-only or first-batch-only success is insufficient.
16. Real GC topology retention: with multiple consuming RBGs still running, delete their Managed topology CR with foreground and background propagation. Native Topology/HyperNodes retain their UIDs and remain non-terminating; existing bindings still support constrained replacement while upper-level updates pause. Delete consumers one at a time, restart controllers, and block one Pod's deletion; native cleanup must wait for the last complete drain, then finish before the topology finalizer is released. Repeat with the feature disabled and verify that Imported native resources survive unchanged.
17. Volcano scheduler restart: same-scope P/D gang plus hard block topology, without instance constraints, starts in block A. Restart the scheduler with spare capacity only in block B, then scale out and replace one Pod; additions stay Pending until A has capacity. Repeat with a stronger child rack constraint, a preferred-only child, and only the role without an explicit instance constraint surviving. Verify parent domain reconstruction, inherited policy fields, and every gang minimum.
18. KAI domain collision: verify baseline rejection, then test a proposed fixed backend using two Nodes with paths `["a.b", "c"]` and `["a", "b.c"]`, each able to hold only one member of a hard rack group. The group must not span them; add a Node in one genuine rack to permit valid placement. Repeat with incremental Node arrival, scheduler restart, nested groups, Managed/Imported vocabularies, and preferred evaluation. Record the exact backend fix and passing image/profile before enabling Phase 3.
19. Upgrade handover: run an old controller with existing RBGs, stop all old workload writers, install the configured current-epoch supporting admission, then start the supporting controller. Cover a policy created before first reconciliation, old zero-replica/backing-template cases, RBGSet children, webhook unavailability, and controller restart. Old UIDs cannot gain topology; newly admitted UIDs follow normal acceptance. Repeat after a safe downgrade with a new epoch. This is a deployment rehearsal, not just an envtest status fixture.
20. Koordinator Phase 2 semantic stability: with a verified continuous-placement scheduler and native immutability guard, attempts to swap label keys or change parent links must fail before the scheduler sees a changed spec. Existing required constraints remain valid across replacement and scheduler restart; metadata-only updates remain usable. Removing/bypassing the guard is outside the supported deployment contract.

### Graduation Criteria

- [ ] `ClusterNetworkTopology` CRD defined with Managed/Imported mode, immutable scheduler/levels/binding, and minimal status contract
- [ ] Mutating webhook stamps the active scheduler; validating webhook enforces mode exclusivity, immutability, and structural level validation
- [ ] ClusterNetworkTopology controller implemented with finalizer, Ready/NativeTopologyDrift conditions, status.levels, status.resources, and concurrency-safe status.consumers
- [ ] Managed native resources have no ownerReferences; UID-based explicit cleanup preserves them through foreground/background topology deletion until all consumer reservations drain
- [ ] Volcano Managed materialization implemented, including Node/HyperNode watches, path-safe naming, tier/tierName assignment, drift restoration, and conflict handling
- [ ] `TopologyConstraint` API defined (Role `instanceTopologyConstraint` + CoordinatedPolicy `scheduling.topologyConstraint`) with `topologyName` referencing the RBG topology CR
- [ ] `PlacementPlan` resolver/planner implemented, including `(Roles, PartitionBy)` scope-composition validation and KEP-430 equivalence golden tests
- [ ] Topology lifecycle immutability enforced by admission and the controller, including UID-keyed CoordinatedPolicy finalizer release and the documented rollback escape hatch
- [ ] Scheduler compiler interface + restricted Volcano scope/threshold matrix, preserving per-role gang minima and reporting implicit instance dispatch thresholds
- [ ] Volcano effective-request validation and instance startup-dependency rejection before acceptance and updates, with backfill and cold-start tests
- [ ] Volcano parent topology propagated into every explicit subgroup without weakening child bounds or gang minima; scheduler-restart common-domain tests pass
- [ ] KAI workload topology gated on a verified unambiguous-domain-encoding backend; unpatched v0.18.6 is rejected and collision/restart tests pass
- [ ] Koordinator workload topology disabled on the inspected baseline and unknown versions; Phase 2 requires continuous-placement tests, fail-closed native semantic immutability, and accepted mapping checks
- [ ] Finalizer escape hatch uses atomic UID/resourceVersion/value tests and preserves unrelated finalizers under concurrent changes
- [ ] Persisted accepted layout, explicit policy dependency, and reference-drained group cleanup
- [ ] Coordinated upgrade handover, epoch-scoped creation provenance, conservative legacy `none` bootstrap, and downgrade/reupgrade tests
- [ ] Required RBAC, cross-namespace retention visibility, and permission-failure/disabled-gate cleanup tests
- [ ] RBG drain and PodGroup protection finalizers, accepted status preservation, and background/foreground deletion and restart recovery without policy circular waits
- [ ] Active RI drain, binding status ownership, stable nodeLabel rendering, and valid alias swap tests completed before enabling the KAI phase
- [ ] Failure gating distinguishes initial creation from recovery under existing bindings
- [ ] Admission + reconcile validation (syntax at admission; Ready/status-level existence, preferred/required ordering, and parent/child ordering at reconcile)
- [ ] TopologyTranslated status and edge-triggered events on RBG
- [ ] Unit test coverage
- [ ] Integration test coverage
- [ ] e2e tests (Volcano environment)

### Upgrade / Downgrade Strategy

- **Upgrade**: the API additions preserve topology-free KEP-430 rendering, but a missing accepted record must not be treated as a fresh workload. For the handover from an older controller, stop all old controller replicas capable of workload writes and pause RBG/RBGSet creation; provision a new persisted handover UUID shared by the supporting controller and admission replicas; install and verify fail-closed supporting CREATE/UPDATE admission before resuming creation and starting workload reconciliation. In-flight requests served by old admission must finish before the handover completes. Pre-handover RBGs are not stamped retroactively and bootstrap conservatively to `none`, including zero-replica and never-launched objects. Policies arriving before the new controller's first reconcile cannot add topology to those UIDs. Current-epoch CREATE-stamped RBGs use normal first acceptance. Ordinary supporting restarts/upgrades and gate toggles retain the epoch; reupgrade after any older-controller interval uses a new epoch. No default topology CR is created.
- **Downgrade**: topology fields are immutable in place. Rolling back is safe only after every topology-bearing RBG is deleted or drained to zero managed Pods and removed. A workload that no longer needs topology must then be recreated without topology so its rendered dialect fields are removed for the new lifecycle; already-scheduled pods from the old lifecycle are not migrated. Do not roll back while a topology-bearing RBG remains active, because the older controller will not gate Role create/update on the topology contract.
- **Topology CR rollback**: an older controller does not understand `ClusterNetworkTopology` or its finalizer and must not be assumed to complete cleanup. First use a supporting version to confirm that all workload references and consumer reservations have drained, then delete topology CRs and complete UID-based Managed backend cleanup before rolling back; Imported native resources are never deleted. Managed native resources have no ownerReferences and will not be cleaned up automatically by GC after an unsupported rollback. If rollback has already left topology finalizers unhandled, restore a supporting version to continue cleanup; the manual policy patch is not a general procedure for releasing arbitrary topology finalizers.
- **CoordinatedPolicy rollback**: a topology-bearing CoordinatedPolicy may carry the topology finalizer that the older controller does not know; delete the matching RBG first, or use the documented finalizer-removal escape hatch, before recreating the policy.
- **Drain finalizer rollback**: keep a controller supporting this protocol running while deleting topology-bearing RBGs. Wait for creators, Pods, RI bindings, and scheduler groups to drain, for `instance-binding`, `group-protection`, `rbg-drain`, and corresponding `policy` finalizers to be released normally, and finally for the RBG to disappear before installing an older controller. replicas=0 or a zero Pod count alone is insufficient for rollback; paused templates, old RIs, and other reusable bindings must also drain. The feature gate may be disabled, but cleanup handlers must remain active.
- **Do not expand the policy escape hatch**: the manual patch above applies only to `topology.x-k8s.io/policy` and does not authorize removing `rbg-drain`, `group-protection`, `instance-binding`, `topology-protection`, or consumer reservations. If drain is blocked, first restore a controller supporting the protocol, required CRDs, and permissions, then locate remaining references; do not bypass the retention order by clearing finalizers.

### Version Skew Strategy

`ClusterNetworkTopology` and `TopologyConstraint` are additive, optional APIs for topology-free workloads. An older controller does not know the new fields and therefore cannot enforce them. Leaving an active topology-bearing RBG running while rolling back is **unsafe and unsupported**: scale-out, replacement, or template reconciliation could create pods without the declared hard placement constraint. Before installing an older controller, the operator must stop topology-related GitOps changes and delete or drain every topology-bearing RBG, then wait until its managed workloads and Pods are gone. Only after workload references are removed may topology CRs and finalizers be cleaned up. Mixed old/new workload writers during the provenance handover are unsupported. Newer controllers bootstrap recordless objects with missing or prior-epoch provenance to `none`, even if a newer topology policy already exists; they do not infer freshness from Pod counts. Downgrade must stop current-epoch marker issuance before older workload writers resume; subsequent reupgrade provisions a new epoch. This avoids trusting a surviving annotation if an older controller erased accepted status. Compatibility depends on scheduler CRD versions, webhook availability, and the verified scheduler image/profile/plugin configuration; runtime schema checks alone do not establish behavioral support. Clusters using Managed/Imported topology must run the RBG controller version that owns the corresponding CRD, webhooks, and finalizer.

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

#### How can this feature be enabled / disabled in a live cluster?

Use the controller feature gate `RBGTopologyAwareScheduling`:

- Gate name: `RBGTopologyAwareScheduling`
- Component: `rbgs`
- Alpha default: `false`

Enabling or disabling requires restarting the RBG controller; it does not require API-server downtime, node downtime, or node reprovisioning. The controller must also run with the topology CRD, webhooks, and a topology-capable scheduler backend enabled. The first transition from an older controller additionally requires the coordinated admission/epoch handover in Upgrade / Downgrade Strategy; simply flipping the gate is not a legacy-workload migration.

#### Does enabling the feature change any default behavior?

No implicit startup delay is added. RBGs without topology declarations and with the default `topologyPolicyRequired: false` retain KEP-430 behavior and immediate policy-free startup. Only an RBG explicitly setting `topologyPolicyRequired: true` waits for its topology-bearing CoordinatedPolicy, reporting `TopologyPolicyPending` without a time-based fallback. Topology-bearing RBGs also freeze their accepted membership layout; gang-only workloads retain their existing mutability. The supporting admission adds internal creation provenance and the controller persists a `none` record before topology-free writes, even with the gate disabled. This adds no policy-settling wait, but failed bookkeeping writes must retry before upper-level workload mutation. Pre-handover RBGs remain topology-free until replaced with a new lifecycle.

#### Can the feature be disabled once it has been enabled?

Yes. Set `RBGTopologyAwareScheduling=false` and restart `rbgs`. Unconstrained workloads continue normally. Topology-bearing RBGs report `TopologyTranslated=False/FeatureDisabled` and stop issuing new Role updates or placement mutations; reference-drained retirement remains allowed. Existing groups, bindings, and finalizers are retained. Downstream controllers may still complete persisted work or replace Pods under static/previously acknowledged bindings; disabling the gate is not a global Pod-creation pause. New explicit instances without an acknowledged group remain `PlacementBindingPending`. Running Pods are not evicted by this feature. Cleanup controllers remain active for draining references and releasing known finalizers; generated resources may be removed only after no workload, instance, or Pod can use them. User-initiated RBG/RI deletion takes the active drain path first and is not blocked by `FeatureDisabled` or topology readiness gates; retaining finalizers here does not prohibit their normal release after drain.

#### What happens if we reenable the feature if it was previously rolled back?

The controller revalidates the accepted topology, physical layout, KAI alias→nodeLabel/depth mapping, native identity, topology readiness/levels, scheduler capability, and rendered objects. Role updates and pending acknowledgements resume only when all checks pass. Drift, late policy arrival, or layout-changing gang updates report `LevelUnresolved` / `TopologyMappingChanged`, `TopologyPolicyLate`, or `PlacementLayoutChanged` / `SchedulerUnsupported`, respectively, without replacing accepted groups with rejected candidates. Terminating lifecycles continue draining; reenablement does not resume creation for them.

#### Are there any tests for feature enablement/disablement?

Yes. Unit and integration tests cover the gate default, admission with the gate disabled, rendering with the gate enabled, disabling after a plan has been accepted (Role updates gated, running pods unchanged), and reenablement after a rollback. Conversion and API round-trip tests ensure old objects remain readable when the gate is disabled.

### Rollout, Upgrade and Rollback Planning

#### How can a rollout or rollback fail? Can it impact already running workloads?

Rollout can fail if the topology CRD/webhooks are unavailable, the active scheduler differs from a topology's immutable scheduler, or backend capability checks fail, including Koordinator continuous placement, KAI domain encoding, Volcano restart-domain recovery, effective requests, and instance startup dependencies. Template changes must pass those checks even when the accepted placement layout is unchanged. In those cases no topology group is written and topology-bearing Role create/update is gated. Already-running pods are not evicted. Rollback to a controller without the feature is allowed only after every topology-bearing RBG is deleted or fully drained and removed; otherwise pod recovery or scale-out can bypass the constraint. Topology CRs, finalizers, and generated scheduler objects remain until workload references are gone, as described in Upgrade / Downgrade Strategy.

#### What specific metrics should inform a rollback?

Monitor failed topology translation conditions/events, `rbg_topology_translation_duration_seconds`, and `rbg_topology_native_resource_ready` (0/1 by topology CR). A sustained increase in translation failures or a topology that is not Ready supports disabling the feature gate and rolling back.

### Monitoring Requirements

The controller emits edge-triggered events and RBG/`ClusterNetworkTopology` conditions for unresolved topology, invalid levels, unsupported schedulers, drift, required-policy waiting/late arrival, layout changes, and feature-disabled states. Compiler warnings distinguish `PreferredAbsorbed` from `ImplicitInstanceGang`; RoleInstance `PlacementBound` identifies concrete binding waits. `TopologyMappingChanged` identifies KAI or gated Koordinator semantic mapping drift; `TopologyBootstrapBlocked` identifies invalid provenance or unavailable handover configuration; the reason/message of `PlacementDrained` distinguishes drain progress from blocking object UIDs/API errors. The metrics above expose translation latency and topology readiness. No new node-level metrics are required.

### Runtime Dependencies

The alpha feature depends on the RBG controller, its webhooks, the `ClusterNetworkTopology` CRD, and the selected scheduler backend including its topology/PodGroup CRDs and enabled scheduler plugins. Runtime schema checks reject unavailable fields; deployment preflight and scheduler-running tests establish behavioral support. Schema presence alone does not certify a scheduler deployment.

### Scalability

Placement planning is bounded by the number of roles, CoordinatedPolicy rules, and RoleInstances, not by the number of nodes. Generated names are bounded and hashed; PodGroup/subGroup retirement follows reference drain and protected GC. Managed native topology resources instead use UID-scoped watches and explicit cleanup, with one consumer reservation per referencing RBG lifecycle. Reservation writes must enforce an object-size limit and reject new acquisition rather than truncate existing consumers when that limit is reached. Managed Volcano mode can create HyperNodes for participating nodes; the controller must respect API list/write limits and batch materialization. No per-pod custom resources are introduced.

## Drawbacks

- Cross-role co-location requires a second object: users declare instance packing on the RBG but must create a CoordinatedPolicy (same name/namespace) for PD co-location, even when no other coordination strategy is needed. Gang scheduling already established this pattern, and keeping every inter-role behavior in one object avoids two sources of truth.
- The planning layer is more explicit than letting each translator create resources directly. The compensating benefit is that gang, topology, and other placement intents have one membership owner; composition no longer depends on translator call order.
- A topology rule over a role set gathers all current and future pods of that scope into one domain. This can become a hard capacity ceiling; the limitation is explicit in this KEP.
- Volcano Phase 1 rejects cross-role containment between different gang/topology scopes and resource-empty/BestEffort topology members, cannot anchor a preferred middle tier, and imposes local dispatch thresholds for instance packing. Component startup dependencies conflicting with those thresholds are rejected even without declared gang; supported compatibility differences are reported individually.
- Koordinator vocabulary import can be delivered independently, but workload topology remains blocked until a backend proves continuous placement across batches and recovery. Native annotation availability and CRD compatibility do not make the current baseline a supported serving backend.
- Freezing physical layout prevents online gang changes that would move Pods between groups, even if topology declarations are unchanged. A new workload lifecycle is required.
- Explicit per-instance binding adds a controller handshake and retirement finalizer for KAI. A shared Role template alone cannot safely carry distinct instance subgroup names. It also requires active RI drain, separate status field ownership, and shared RBG/PodGroup deletion protection across all topology backends; controller downtime or blocking third-party finalizers can prolong deletion.
- ClusterNetworkTopology adds a new CR, consumer reservation writes, and an explicit native-resource cleanup lifecycle. Avoiding ownerReferences on native resources preserves them during foreground topology deletion, but requires a supporting controller to complete cleanup; manual finalizer removal can leak resources.
- KAI workload support requires a verified domain-encoding fix; portable aliases and a currently collision-free cluster cannot certify the unpatched backend.

## Alternatives

- **Independent gang and topology translators.** Rejected: KEP-430 would create one RBG-level gang PodGroup while topology rules partition their own PodGroups. A pod can carry only one `group-name`, so overlapping scopes either lose the gang guarantee, over-constrain topology, or produce order-dependent results. Placement membership needs one owner.
- **RBG-level `spec.topologyConstraint` field.** Rejected: the constraint group "all pods of the RBG" is the wrong granularity for the driving scenario — PD-disaggregated deployments need prefill+decode co-located while roles without latency-critical cross-role traffic stay outside the domain, and an over-broad group wastes scarce in-domain capacity and can make the constraint unsatisfiable. Cross-role grouping is by definition inter-role coordination, which is exactly CoordinatedPolicy's domain. Whole-RBG co-location remains expressible by listing every role, so no expressiveness is lost.
- **Per-role topology with pairwise role references** (prefill declares "co-locate with decode"). Rejected: pairwise declarations create N² relationships, make the constraint group implicit, and complicate conflict validation; a single rule naming the full role set states the group explicitly, once.
- **Direct pass-through of scheduler level identifiers.** Rejected: the same RBG field would mean a Volcano `tierName`, Koordinator `topologyLayer`, or KAI `nodeLabel`/alias depending on the active scheduler. The schema would be stable but the semantic contract would not be. `ClusterNetworkTopology` makes the vocabulary and backend ownership explicit.
- **Putting a backend GVK in `topologyBinding`.** Rejected: users would need to understand each scheduler's CRD, and a GVK inconsistent with the active scheduler would require additional conflict semantics. A name plus the immutable scheduler stamp is sufficient; status records the actual GVK, name, and UID.
- **Koordinator Managed mode.** Rejected: Koordinator reads only the global `default` ClusterNetworkTopology, so RBG ownership could conflict with cluster administrators. Imported `default` makes external ownership explicit.
- **Approximating unsupported plans** (merging overlapping scopes into the union, dropping one constraint, or rendering native pod affinities). Rejected: silent semantic changes are worse than an explicit `IncompatiblePlacementGroups` or `SchedulerUnsupported` condition.
- **A fixed policy-settling timeout.** Rejected: it delays topology-free workloads without guaranteeing GitOps ordering. An immutable explicit policy dependency provides deterministic waiting.
- **Deleting old groups immediately after patching Role templates.** Rejected: paused rollouts, old instances, and persisted templates can still create Pods with the old binding. Layout migration is excluded and group retirement requires reference drain.
- **OwnerReferences plus native-resource finalizers for Managed topology retention.** Rejected for this lifecycle: foreground GC can still mark native resources terminating while workloads remain. Explicit UID ownership and consumer-drained cleanup preserve non-terminating native objects; PodGroup deletion uses its separate active-workload-drain protocol.
- **A one-time KAI domain-collision scan.** Rejected as a safety gate for an unpatched backend: later Node changes can race scheduler tree construction. A verified unambiguous encoding avoids imposing new cluster-wide label-value restrictions.

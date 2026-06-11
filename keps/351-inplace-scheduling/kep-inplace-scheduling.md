# KEP-351: Inplace Scheduling

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
- [Goals](#goals)
- [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Cache Reuse via Rolling Update with MaxSurge (Component Granularity)](#story-1-cache-reuse-via-rolling-update-with-maxsurge-component-granularity)
    - [Story 2: Per-Pod Cache Reuse via Rolling Update (Pod Granularity)](#story-2-per-pod-cache-reuse-via-rolling-update-pod-granularity)
    - [Story 3: GPU Node Affinity After Pod Failure](#story-3-gpu-node-affinity-after-pod-failure)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Configuration](#configuration)
  - [Node Binding Store](#node-binding-store)
  - [Affinity Injection](#affinity-injection)
  - [Interaction with Existing Mechanisms](#interaction-with-existing-mechanisms)
  - [Implementation Plan](#implementation-plan)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [End to End Tests](#end-to-end-tests)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

In-Place Scheduling is a scheduling enhancement for RoleBasedGroup (RBG) that
ensures Pods being recreated during **rolling upgrades** are preferentially
scheduled to nodes where they (or their component peers) previously ran. This
enables: new Pods land on nodes with cached node-local resources (model weights,
GPU state, inference framework dump files) and take over traffic without the
overhead of re-downloading or re-initializing.

The feature operates at the **Role level** with per-role opt-in via annotation,
supports two modes (**Preferred** and **Required**), two binding granularities
(**Pod** and **Component**), and uses an in-memory node binding store within
the controller to track Pod-to-node mappings.

## Motivation

In AI inference workloads, model weight files are typically tens to hundreds of
gigabytes. When a Pod is scheduled to a node for the first time, these files
must be downloaded from remote storage, which can take minutes. However, if the
same node is already hosting a Pod running the same model, the weight files are
already cached on the node's local disk or in memory.

The **primary scenario** is rolling upgrades with the recreate pod update
policy. During a rolling upgrade, new Pods are created to replace old ones. If
the new Pods can land on the same nodes as the old Pods, they immediately reuse
the cached model weights and GPU warm-up state, enabling faster upgrade.

Without in-place scheduling, the Kubernetes scheduler has no awareness of
where the old Pods are running. New Pods may be placed on entirely different
nodes, forcing a full re-download of model weights (10+ minutes for large
models) and defeating the purpose of a rolling upgrade.

Beyond model weights, modern inference frameworks might also dump per-Pod state to
local disk during operation — model shards, KV-cache files, and runtime metadata.
In a multi-node setup (e.g., prefill with master + worker across multiple GPUs),
each Pod dumps **different** data. If Pods are swapped across nodes during recreation,
the dumped files are useless to the wrong Pod. Per-Pod node binding ensures each Pod
returns to its own node where its specific state resides.

Secondary scenarios include:
- **In-place update fallback**: When in-place update is not possible and Pods
  are deleted and recreated.
- **Failure recovery**: When a Pod crashes or is evicted, the replacement Pod
  benefits from node affinity to reuse cached resources.

## Goals

- **Built-in policy**: Provide an in-place scheduling strategy that, when
  enabled, injects `preferredDuringScheduling` node affinity to steer recreated
  Pods toward previously used nodes.
- **Strict mode**: Support a configurable `Required` mode that uses
  `requiredDuringScheduling` node affinity for hard node binding.
- **Configurable granularity**: Support **Pod-level** binding (each Pod returns
  to its own historical node) and **Component-level** binding (Pod prefers any
  node that has hosted the same component type).
- **Failure recovery**: Ensure Pod failures and restarts preserve node
  affinity, allowing the replacement Pod to reuse node-local resources.
- **Per-role configuration**: Allow each role in an RBG to independently enable
  or disable in-place scheduling via annotation.

## Non-Goals

- **Timeout-based degradation**: We do not implement automatic degradation from
  Required to Preferred mode when a bound node is unavailable. Users who need
  this safety net should use Preferred mode directly.
- **Persistent node binding**: Node bindings are stored in controller memory
  and are not persisted to etcd. Controller restarts clear all bindings.
- **Garbage collection of stale bindings**: Stale entries are harmless (bounded
  memory, preferred affinity ignores unreachable nodes). Entries are naturally
  cleared on controller restart.

## Proposal

### User Stories

#### Story 1: Cache Reuse via Rolling Update (Stateless + Component Granularity + MaxSurge)

An RBG manages a `prefill` role with 2 replicas in **Stateless mode**, each
serving a 70B parameter model. The model weights (~140GB) are cached on each
node's NVMe disk. The role is configured with `maxSurge: 2` and
`maxUnavailable: 0` for zero-downtime upgrades.

The user triggers a rolling upgrade. Because the role is Stateless,
RoleInstance names are randomly generated on each reconcile (e.g., old
instances are `abc-Prefill-0`, `abc-Prefill-1`; new instances are
`def-Prefill-0`, `def-Prefill-1`). Pod names change between old and new
instances, so per-Pod binding is ineffective. **Component granularity** is
required here.

**Without in-place scheduling**: The 2 surge Pods are scheduled to random
nodes by the Kubernetes scheduler. They must download 140GB of model weights
from remote storage, taking 10+ minutes.

**With in-place scheduling (Preferred mode, Component granularity)**: The 2
surge Pods receive a `preferredDuringScheduling` node affinity listing all
nodes where the same component type has previously run (`{A, B}`). The
scheduler places the new Pods on those nodes. The new Pods immediately find
the 140GB weights cached on local NVMe, load them in seconds, and become
Ready. The upgrade controller then deletes the corresponding old Pods.

```
Timeline (Preferred mode, Component granularity, Stateless):

Old Pods running:   [abc-Prefill-0@node-A] [abc-Prefill-1@node-B]
                                            │
                        ┌────────────────────┤ MaxSurge: create surge Pods first
                        │                    │
Surge Pods created:     │  [def-Prefill-0 → prefers {A, B}]  (component-level)
                        │  [def-Prefill-1 → prefers {A, B}]
                        │                    │
                        ▼                    ▼
Scheduler places:     [def-Prefill-0@node-A] [def-Prefill-1@node-B]
Load weights:         ~5 seconds (from local NVMe cache)
                        │
Old Pods deleted:     [abc-Prefill-1 ✗] [abc-Prefill-0 ✗]
```

```yaml
spec:
  roles:
    - name: prefill
      replicas: 2
      annotations:
        rbg.workloads.x-k8s.io/role-instance-pattern: "Stateless"
        rbg.workloads.x-k8s.io/role-inplace-scheduling: "Preferred"
        rbg.workloads.x-k8s.io/role-inplace-scheduling-granularity: "Component"
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxSurge: 2
          maxUnavailable: 0
          type: RecreatePod
      template:
        spec:
          containers:
          - name: inference
            image: my-model:v2
```

#### Story 2: Per-Pod Cache Reuse via Rolling Update (Stateful + Pod Granularity + No Surge)

An RBG manages a `prefill` role with 2 Pods (`prefill-0`, `prefill-1`) in
**Stateful mode**. Each Pod dumps different per-Pod state (model shards,
KV-cache) to its node's local disk. The role is configured with
`maxSurge: 0` and `maxUnavailable: 100%` — old Pods are deleted **first**,
then new Pods are created.

Because Pod names are stable in Stateful mode, each recreated Pod has the
**same name** as its predecessor. Per-Pod binding ensures each Pod returns
to its exact previous node.

**Without in-place scheduling**: New Pods may land on different nodes, losing
access to their per-Pod state and requiring full re-download.

**With in-place scheduling (Preferred mode, Pod granularity)**: `prefill-0`
returns to node-A (where its model shards reside), `prefill-1` returns to
node-B (where its KV-cache resides).

```
Timeline (Preferred mode, Pod granularity, Stateful):

Old Pods running:   [prefill-0@node-A] [prefill-1@node-B]
                          │
                     ┌────┤ maxSurge=0: delete old Pods first
                     │    │
Old Pods deleted:   [prefill-0 ✗] [prefill-1 ✗]  ← Pod names are stable
                     │    │
                     ▼    ▼
New Pods created:   [prefill-0 → prefer node-A]  (per-Pod binding hit!)
                    [prefill-1 → prefer node-B]
                     │    │
Scheduler places:  [prefill-0@node-A] [prefill-1@node-B]  ← exact match!
State reuse:       ~5 seconds (per-Pod state reused from local disk)
```

```yaml
spec:
  roles:
    - name: prefill
      replicas: 2
      annotations:
        rbg.workloads.x-k8s.io/role-inplace-scheduling: "Preferred"
        # granularity defaults to "Pod" for Stateful mode
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxSurge: 0
          maxUnavailable: 100%
          type: RecreatePod
      template:
        spec:
          containers:
          - name: inference
            image: my-model:v2
```

#### Story 3: GPU Node Affinity After Pod Failure

An RBG manages a `decode` role running on GPU nodes with specific hardware
(e.g., A100 80GB). Each node has preloaded GPU driver caches and NCCL
configuration files.

A Pod crashes due to a transient GPU error (e.g., ECC error). The
`RecreateRoleInstanceOnPodRestart` restart policy triggers instance recreation.

**Without in-place scheduling**: The new Pod may land on a different GPU node
type (e.g., V100), causing compatibility issues or suboptimal performance.

**With in-place scheduling (Required mode)**: The replacement Pod is hard-bound
to the exact node where it previously ran, ensuring it lands on the same
GPU hardware type with all driver caches intact.

```yaml
spec:
  roles:
    - name: decode
      replicas: 4
      annotations:
        rbg.workloads.x-k8s.io/role-inplace-scheduling: "Required"
      restartPolicy: RecreateRoleInstanceOnPodRestart
```

### Risks and Mitigations

| Risk                                         | Impact                                                       | Mitigation                                                                                                                                      |
|----------------------------------------------|--------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------|
| Required mode + node permanently unavailable | Pod stays Pending indefinitely                               | Users should use Preferred mode for failure-prone environments. Required mode is opt-in and intended for stable, hardware-specific deployments. |
| Stale node bindings after RBG deletion       | Wasted memory in controller                                  | Memory footprint is negligible (~10 entries per RBG, 1000 RBGs = 10K entries < 1MB). Controller restarts naturally clear all bindings.          |
| Interaction with exclusive topology          | Redundant or conflicting affinity rules                      | When exclusive topology is active for a role, in-place scheduling injection is skipped entirely.                                                |
| Gang scheduling deadlock with Required mode  | Entire PodGroup stuck if one Pod's bound node is unavailable | No timeout degradation is implemented. Users combining Required mode with gang scheduling must ensure node availability or accept the risk.     |
| Pod granularity + Stateless mode             | Affinity silently fails (Pod names change every reconcile)   | Granularity defaults to Component for Stateless mode. Users are not expected to manually configure Pod granularity for Stateless workloads.     |
| Pod granularity + Stateless mode + MaxSurge  | Both Pod names and instance names change                     | Granularity defaults to Component for Stateless mode. Pod granularity is only effective in Stateful mode.                                       |

## Design Details

### Configuration

In-place scheduling is configured via two annotations on the `RoleSpec`:

#### Mode Annotation

```
rbg.workloads.x-k8s.io/role-inplace-scheduling: "Preferred" | "Required"
```

| Value       | Behavior                                                                                                                                              |
|-------------|-------------------------------------------------------------------------------------------------------------------------------------------------------|
| *(not set)* | Feature disabled. No node affinity is injected.                                                                                                       |
| `Preferred` | Inject `preferredDuringSchedulingIgnoredDuringExecution` nodeAffinity with weight 100. Pod is steered toward historical nodes but can land elsewhere. |
| `Required`  | Inject `requiredDuringSchedulingIgnoredDuringExecution` nodeAffinity. Pod must land on a historical node.                                             |

#### Granularity Annotation

```
rbg.workloads.x-k8s.io/role-inplace-scheduling-granularity: "Pod" | "Component"
```

| Value       | Behavior                                                                                                                     | Default For  |
|-------------|------------------------------------------------------------------------------------------------------------------------------|--------------|
| *(not set)* | Auto-detect: **Pod** for Stateful mode, **Component** for Stateless mode.                                                    | —            |
| `Pod`       | Per-Pod binding: each Pod returns to its own historical node. Key uses Pod name.                                             | Stateful     |
| `Component` | Component-level binding: Pod prefers any node that has hosted the same component type. Key uses role + component name.       | Stateless    |

**Smart defaults**: When the granularity annotation is not set:
- **Stateful mode** defaults to `Pod`. Stateful RoleInstances have stable names, so Pod-level binding works correctly. This is the most common scenario.
- **Stateless mode** defaults to `Component`. Stateless RoleInstances get random names on each reconcile, so Pod names change and per-Pod binding would be ineffective.

**Annotation propagation** follows the existing pattern (analogous to `role-instance-gang-scheduling`):

```
RoleSpec.Annotations
    |
    v  (GetCommonAnnotationsFromRole - auto-propagates user annotations)
RoleInstanceSet.Annotations
    |
    v  (annotationsToCopy whitelist in stateless/stateful controllers)
RoleInstance.Annotations
```

Both annotation keys must be added to the `annotationsToCopy` list in:
- `pkg/reconciler/roleinstanceset/statelessmode/core/implement.go`
- `pkg/reconciler/roleinstanceset/statefulmode/stateful_instance_set_utils.go`

### Node Binding Store

The node binding store is an **in-memory** data structure within the
RoleInstance controller. A single unified store handles both granularities.

```go
type nodeBindingStore struct {
    mu       sync.RWMutex
    bindings map[string]sets.Set[string] // key -> set of node names
}
```

**Operations**:
- `Add(key, nodeName)`: Insert `nodeName` into the set for `key`. Idempotent.
- `Load(key) sets.Set[string]`: Return the set of node names for `key`.

The granularity difference is determined entirely by the **key format**, not by
the storage structure.

**Pod-level key**: `{rbgUID}/{podName}`

Where `rbgUID` is the RBG's real Kubernetes object UID from the
`RBGOwnerUIDLabelKey` label (propagated only when in-place scheduling is
enabled), and `podName` is the Pod's name. A complete key looks like:
`a1b2c3d4-e5f6-.../my-rbg-prefill-0-master-0`. Using the real RBG UID
ensures that when an RBG is deleted and recreated with the same name, the
new RBG gets a different UID and does not inherit stale bindings.

Since each Pod-level key is unique per Pod, the set always contains exactly
one node. When a Pod is recreated, it picks up its own previous node.

**Component-level key**: `{rbgUID}/{roleName}-{componentName}`

Multiple Pods of the same component type share the same key, so their nodes
accumulate into a set. Affinity injection targets the entire set.

**Record logic** (both granularities):

```
for each pod in filteredPods:
    if pod.Spec.NodeName == "" || !isPodRunningAndReady(pod):
        continue
    key = buildKey(granularity, rbgUID, pod, roleName)
    store.Add(key, pod.Spec.NodeName)
```

`Add` is idempotent — calling it repeatedly with the same `(key, node)` pair
is a no-op. Only Running **and Ready** Pods are recorded.

**Cleanup**:
- When an RBG is deleted, entries may remain in the store until controller
  restart. This is acceptable because entries are bounded in size and using
  the RBG UID as key ensures a recreated RBG with the same name will not
  inherit stale bindings.
- No periodic garbage collection needed. The store is naturally bounded:
  Pod-level: max entries = total number of Pods across all RBGs.
  Component-level: max entries = total number of distinct component types.
  Estimated: 100 Pods/RBG * 1000 RBGs = 100K entries, ~3MB memory.

### Affinity Injection

The affinity injection occurs in `createPods` (the Pod creation path in
`instance_scale.go`), **only when a node binding exists** for the Pod.

**Initial Pod creation** (no binding): No affinity is injected. Pods are
scheduled normally by the Kubernetes scheduler.

**Pod recreation** (binding exists): The Pod receives a node affinity
targeting the nodes returned by `store.Load(key)`. For Pod-level granularity,
the set contains a single node; for Component-level, it contains all nodes
that have hosted the same component type.

*Preferred Mode* (weight=100):

```yaml
nodeAffinity:
  preferredDuringSchedulingIgnoredDuringExecution:
    - weight: 100
      preference:
        matchExpressions:
          - key: kubernetes.io/hostname
            operator: In
            values: ["node-A", "node-B"]  # single node (Pod) or node set (Component)
```

*Required Mode*:

```yaml
nodeAffinity:
  requiredDuringSchedulingIgnoredDuringExecution:
    nodeSelectorTerms:
      - matchExpressions:
          - key: kubernetes.io/hostname
            operator: In
            values: ["node-A", "node-B"]
```

**Injection logic (pseudocode)**:

```
func injectInPlaceScheduling(pod, instance, store):
    mode = instance.Annotations["rbg.workloads.x-k8s.io/role-inplace-scheduling"]
    if mode == "":
        return  // feature not enabled
    if mode not in ("Preferred", "Required"):
        log warning and return  // invalid value

    if pod.Annotations["rbg.workloads.x-k8s.io/group-exclusive-topology"] != "":
        return  // exclusive topology takes precedence

    granularity = instance.Annotations["...granularity"]
    if granularity == "":
        if isStateless(instance):
            granularity = "Component"
        else:
            granularity = "Pod"

    key = buildKey(granularity, rbgUID, pod, roleName)
    nodes = store.Load(key)
    if len(nodes) == 0:
        return  // no binding

    ensure pod.Spec.Affinity.NodeAffinity exists

    switch mode:
        case "Preferred":
            append PreferredSchedulingTerm(weight=100, nodes)
        case "Required":
            set RequiredDuringSchedulingIgnoredDuringExecution(nodes)
```

### Interaction with Existing Mechanisms

#### Exclusive Topology

When a role has exclusive topology enabled
(`rbg.workloads.x-k8s.io/group-exclusive-topology`), the role's Pods are
already locked to a specific topology domain via PodAffinity/PodAntiAffinity.
In-place scheduling injection is **skipped** to avoid redundant or conflicting
affinity rules.

#### In-Place Update

In-place scheduling and in-place update are **complementary**:
- If in-place update succeeds: Pod is not recreated, stays on the same node.
  No scheduling needed — this is the ideal case.
- If in-place update is not possible (ReCreate pod update policy, or spec
  changes beyond container images): Pod is deleted and recreated. In-place
  scheduling injects node affinity to steer the new Pod back to the same node.

#### Rolling Upgrade (Primary Use Case)

This is the **primary scenario** for in-place scheduling. The behavior differs
based on the rolling update strategy and binding granularity:

**Pod Granularity (Stateful mode)**:

1. The node binding store already contains per-Pod bindings from previous
   reconciles.
2. In Stateful mode, rolling upgrades delete old Pods in **descending index
   order** (highest index first). With MaxSurge, new surge Pods receive
   **incrementing indices** beyond the current replica count.
3. When a surge Pod is later deleted and the replacement Pod is created, the
   new Pod reclaims the original index and thus the **same name** (Pod names
   are stable in Stateful mode).
4. Each new Pod receives affinity targeting its own previous node.
5. The scheduler places new Pods on the same nodes, enabling immediate reuse
   of per-Pod cached state (model shards, KV-cache, runtime metadata).

This is the **most precise** strategy — each Pod returns to its exact previous
node. It requires Stateful mode (stable Pod names) but works with **any**
rolling update strategy (MaxSurge or MaxUnavailable). With MaxSurge, temporary
surge Pods (higher indices) have no per-Pod binding and are scheduled normally,
but the main recreated Pods still return to their original nodes.

**Component Granularity**:

1. The node binding store contains component-level bindings (node sets) from
   previous reconciles.
2. Each new Pod receives affinity pointing to all nodes that have hosted the
   same component type.
3. The scheduler places new Pods on those nodes, enabling reuse of model
   weights and GPU state.

This strategy is **less precise** (any node of the same component type) but
works in **all** scenarios: Stateful, Stateless, MaxSurge, and MaxUnavailable.
It is the only effective granularity for Stateless mode (where Pod names
change) and provides broader affinity for surge Pods in MaxSurge upgrades.

#### Stateless Mode

In Stateless mode, RoleInstance names are randomly generated on each reconcile.
This means Pod names change between old and new instances:
- Old: `test-prefill-abc123-master-0`
- New: `test-prefill-xyz789-master-0`

Per-Pod binding is ineffective because the new Pod's name never existed in the
store. Component-level binding is the correct choice, and is the **default**
when granularity is not explicitly configured.

#### RestartPolicy

When `RecreateRoleInstanceOnPodRestart` triggers full instance recreation,
in-place scheduling ensures the new instance's Pods prefer the same nodes.

#### Gang Scheduling

In-place scheduling is transparent to gang scheduling:
- Preferred mode: No impact. Soft affinity doesn't block scheduling.
- Required mode: Potential risk if bound nodes are unavailable and gang
  scheduling requires all Pods to be placed simultaneously. No timeout
  degradation is implemented. Users must assess this risk.

#### Component Dependencies

In-place scheduling operates at the Pod spec level and does not affect
component startup/shutdown ordering (`startAfter`/`deleteAfter`).

### Implementation Plan

| Step | Description                                        | Files                                                                        |
|------|----------------------------------------------------|------------------------------------------------------------------------------|
| 1    | Define annotation constants                        | `api/workloads/constants/annotation.go`                                      |
| 2    | Implement `nodeBindingStore` (Pod + Component)     | `pkg/reconciler/roleinstance/sync/node_binding.go` (new)                     |
| 3    | Record node bindings in Reconcile                  | `pkg/reconciler/roleinstance/instance_reconciler.go`                         |
| 4    | Inject affinity in `createPods`                    | `pkg/reconciler/roleinstance/sync/instance_scale.go`                         |
| 5    | Add annotations to `annotationsToCopy` (stateless) | `pkg/reconciler/roleinstanceset/statelessmode/core/implement.go`             |
| 6    | Add annotations to `annotationsToCopy` (stateful)  | `pkg/reconciler/roleinstanceset/statefulmode/stateful_instance_set_utils.go` |
| 7    | RBGOwnerUIDLabelKey propagation                    | `pkg/reconciler/roleinstanceset_reconciler.go`                               |
| 8    | Exclusive topology mutual exclusion check          | Within affinity injection function                                           |
| 9    | Unit tests                                         | Corresponding `*_test.go` files                                              |
| 10   | E2E tests                                          | `test/e2e/`                                                                  |

### Test Plan

#### Unit Tests

- **nodeBindingStore**: Test Set, Load, AddToSet, LoadSet, concurrent access,
  empty state.
- **Affinity injection**: Test Pod-level Preferred/Required injection. Test
  Component-level Preferred/Required injection. Test no injection when binding
  is empty. Test no injection when exclusive topology is active. Test
  annotation not set = no injection. Test granularity auto-detection (Stateful
  to Pod, Stateless to Component).
- **Annotation propagation**: Verify both annotations flow from Role to
  RoleInstanceSet to RoleInstance in both stateless and stateful modes.

#### End to End Tests

- **Rolling upgrade with Pod granularity** (Stateful): Deploy RBG
  with Preferred annotation. Trigger a rolling upgrade with ReCreate policy.
  Verify new Pods land on the same nodes as old Pods (per-Pod matching).
- **Rolling upgrade with Component granularity** (Stateful/Stateless,
  MaxSurge): Deploy RBG with Preferred + Component granularity. Trigger a
  rolling upgrade. Verify surge Pods land on nodes that have hosted the same
  component type.
- **Pod failure recovery**: Deploy RBG with Preferred annotation. Delete a Pod
  to simulate failure. Verify replacement Pod is scheduled to the same node.

## Alternatives

### Persist Node Bindings

Store node bindings in status instead of controller memory.

**Rejected for v1**: Adds API surface (new status field or CRD), requires
update on every reconcile, and provides minimal benefit since the in-memory
approach is self-correcting after controller restart. This may be done
in the future version.

### Timeout-Based Degradation for Required Mode

Automatically downgrade Required mode to Preferred after a configurable
timeout when a Pod is stuck Pending.

**Rejected because**: Adds significant complexity (timer management, Pod
patching, edge cases). Users who need this safety net can use Preferred mode
directly.

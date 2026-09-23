# KEP-465: Continuous Node-Pool Warmup for RoleBasedGroup

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [API Changes](#api-changes)
    - [Mode](#mode)
    - [Status](#status)
    - [Example](#example)
  - [Update Semantics and Admission Validation](#update-semantics-and-admission-validation)
  - [Continuous State Machine](#continuous-state-machine)
  - [Revision and Node Identity](#revision-and-node-identity)
  - [Controller Flow](#controller-flow)
  - [Event Sources](#event-sources)
  - [Failure Semantics](#failure-semantics)
  - [Pod Retention and Garbage Collection](#pod-retention-and-garbage-collection)
  - [Changing Warmup Content](#changing-warmup-content)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Deterministic Effective Actions](#deterministic-effective-actions)
  - [Status Reconstruction and Controller Restarts](#status-reconstruction-and-controller-restarts)
  - [Webhook Integration](#webhook-integration)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration Tests](#integration-tests)
    - [End-to-End Tests](#end-to-end-tests)
  - [Upgrade and Downgrade Strategy](#upgrade-and-downgrade-strategy)
  - [Scalability](#scalability)
  - [Security](#security)
  - [Observability and Troubleshooting](#observability-and-troubleshooting)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

[KEP-129](../129-rbg-warmup/README.md) introduced
`RoleBasedGroupWarmup` as a one-shot job. It resolves a target set, creates one
warmup Pod per node, and stops after reaching `Completed` or `Failed`. That
model works before a planned rollout, but it does not warm nodes that join an
autoscaled resource pool after the job has finished. This limitation is tracked
in [issue #465](https://github.com/sgl-project/rbg/issues/465).

This KEP adds an opt-in `Continuous` mode. A Continuous Warmup applies one
immutable warmup definition to the current target nodes and keeps watching the
target. When a node joins, is recreated, or acquires a different effective set
of role actions, the controller warms that node without requiring a new Warmup
object. The existing behavior remains the default `Once` mode.

Continuous mode does not make image, model, target, or Pod inputs mutable. A
new warmup definition requires a new `RoleBasedGroupWarmup` resource. This
keeps a Warmup resource auditable and prevents one object from containing a
mixture of unrelated image or model versions.

## Motivation

Autoscaled inference pools can add Nodes and RoleBasedGroup Pods long after a
deployment or rollout has completed. A new inference Pod can then pay the full
image pull, model download, or runtime initialization cost on its first start.
The current one-shot Warmup cannot react because its terminal phases only
perform optional TTL cleanup.

A long-lived Warmup must also have stricter update semantics than the current
API. Although the API server currently accepts updates to targets and actions,
the controller tracks completion only by node name. An update can therefore
leave running Pods on the old definition, reuse an old successful Pod, and
create later Pods from the new definition. This KEP formalizes the intended
job-like immutability for both modes.

### Goals

- Preserve the current one-shot behavior as the default `Once` mode.
- Continuously warm newly eligible or recreated nodes using one immutable
  warmup definition.
- React promptly to Node and RoleBasedGroup Pod changes, with a periodic safety
  reconciliation for missed events.
- Distinguish a recreated Node and changes to the effective role actions on a
  node.
- Expose the current per-node progress and a stable Continuous health state.
- Reject ambiguous create and update requests with a validating webhook.
- Bound owned Pod history without introducing another public API type.

### Non-Goals

- Updating an existing Warmup to deploy a new image, model, target, volume, or
  customized action.
- Detecting changes behind mutable image tags, object-store paths, PVC content,
  Secret data, ConfigMap data, or host paths when the Warmup API fields do not
  change.
- Persisting a history of executions or introducing a `WarmupExecution` CRD.
- Coordinating Warmup replacement with an RBG rollout automatically.
- Delaying RBG Pod startup until Continuous Warmup finishes on its Node.
- Adding a node agent, runtime sidecar, or inference-engine-specific protocol.
- Supporting a global lifetime timeout or TTL deletion for a Continuous
  resource in the initial release.

## Proposal

### User Stories

1. As a platform operator, I create one Continuous Warmup for an autoscaled GPU
   pool and use `Ready` to control when the pool becomes workload-eligible.
   Nodes added later receive the same image and model warmup before the platform
   exposes their capacity to inference workloads.
2. As an RBG operator, I target role-specific actions. When an RBG Pod is
   scheduled onto a new node, the controller performs a best-effort warmup by
   merging the actions for the roles on that node. This path does not block the
   already-scheduled RBG Pod from starting.
3. As an operator rolling out a new image or model, I create a new Warmup rather
   than mutating the old task, so status and Pod logs always describe one
   immutable definition.

### API Changes

#### Mode

`RoleBasedGroupWarmupSpec` gains an optional mode:

```go
type WarmupMode string

const (
	WarmupModeOnce       WarmupMode = "Once"
	WarmupModeContinuous WarmupMode = "Continuous"
)

// +kubebuilder:default=Once
// +kubebuilder:validation:Enum=Once;Continuous
Mode WarmupMode `json:"mode,omitempty"`
```

An absent value is interpreted and defaulted as `Once`. The field is immutable
after creation.

#### Status

The existing `WarmupJobPhase` enum gains two values:

```go
WarmupJobPhaseReady    WarmupJobPhase = "Ready"
WarmupJobPhaseDegraded WarmupJobPhase = "Degraded"
```

`Completed` and `Failed` remain terminal phases used by Once. `Ready` and
`Degraded` are non-terminal Continuous phases. `Running` and `Paused` are
shared by both modes.

Status also gains the observed generation and current per-node state:

```go
type WarmupNodeStatus struct {
	NodeName           string          `json:"nodeName"`
	NodeUID            types.UID       `json:"nodeUID"`
	Revision           string          `json:"revision"`
	Phase              WarmupNodePhase `json:"phase"`
	Attempts           int32           `json:"attempts,omitempty"`
	LastTransitionTime metav1.Time     `json:"lastTransitionTime"`
	Message            string          `json:"message,omitempty"`
}

ObservedGeneration int64              `json:"observedGeneration,omitempty"`
NodeStatuses       []WarmupNodeStatus `json:"nodeStatuses,omitempty"`
```

`WarmupNodePhase` has `Pending`, `Running`, `Succeeded`, and `Failed` values.
`nodeStatuses` is a map-style list keyed by `nodeName`. It contains only the
current target set, not historical executions. The existing aggregate fields
`desired`, `active`, `succeeded`, and `failed` remain available.

Continuous does not set `completionTime`. The transition time of the `Ready`
condition records when the current target set most recently became ready.
`startTime` remains the time the resource first created a warmup Pod.

#### Example

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroupWarmup
metadata:
  name: qwen-resource-pool-v2
spec:
  mode: Continuous
  paused: false
  policies:
    parallelism: 4
    backoffLimitPerNode: 3
    maxFailedNodes: 0
  targetRoleBasedGroup:
    name: qwen
    roles:
      decode:
        imagePreload:
          images:
          - registry.example.com/sglang@sha256:0123456789abcdef
      prefill:
        customizedAction:
          containers:
          - name: model-warmup
            image: registry.example.com/model-loader@sha256:fedcba9876543210
            args: ["--model", "s3://models/qwen/v2"]
status:
  phase: Ready
  observedGeneration: 1
  desired: 2
  active: 0
  succeeded: 2
  failed: 0
  nodeStatuses:
  - nodeName: gpu-1
    nodeUID: 292a8727-54f7-48e6-ae16-8ee267e5461f
    revision: 7fb2c91a
    phase: Succeeded
    attempts: 1
```

The digest values above are abbreviated for readability. Production image
references must use valid full digests.

### Update Semantics and Admission Validation

A new `RoleBasedGroupWarmupValidator` implements
`admission.CustomValidator`. It uses `failurePolicy=Fail` and handles create
and update requests for the main resource. Status subresource updates are not
sent to this webhook.

The API continues to use OpenAPI and CEL for local structural validation, such
as enum values, numeric ranges, mutually exclusive targets, and non-empty
actions. The webhook owns rules that depend on mode or compare old and new
objects.

On create, the webhook:

- normalizes an empty mode to `Once` for validation;
- rejects `globalTimeoutSeconds` in Continuous mode; and
- rejects `ttlSecondsAfterFinished` in Continuous mode.

On update, the webhook permits only these spec changes:

| Field | Once | Continuous |
|---|---:|---:|
| `paused` | Mutable | Mutable |
| `policies.parallelism` | Mutable | Mutable |
| `policies.ttlSecondsAfterFinished` | Mutable | Not allowed |
| All other spec fields | Immutable | Immutable |

In particular, `mode`, targets, actions, images, customized containers,
volumes, tolerations, `backoffLimitPerNode`, `maxFailedNodes`, and
`globalTimeoutSeconds` are immutable. Implementations should compare semantic
values after normalizing absent/defaulted fields, rather than relying on raw
JSON equality. New spec fields are immutable by default until their update
semantics are explicitly designed.

This validation also applies to pre-existing Once resources after upgrade. It
turns a previously accepted but unreliable update into an admission error with
a field-specific message.

### Continuous State Machine

The existing phase type is extended rather than replaced:

```text
                    paused=true
                +---------------> Paused
                |                    |
                |                    | paused=false
                |                    v
None --------> Running <-------------------------------+
                |                                      |
                | Once                                 | target changes
                +--------> Completed                   |
                +--------> Failed                      |
                |                                      |
                | Continuous                           |
                +--------> Ready ----------------------+
                +--------> Degraded -------------------+
```

Phase is calculated in this order:

1. `Paused` when `spec.paused` is true.
2. `Running` when a current target node is pending or active.
3. In Continuous, `Degraded` when the target is unavailable or permanent
   failures exceed `maxFailedNodes`.
4. In Continuous, `Ready` when the current set has reached terminal outcomes
   within the configured failure tolerance.
5. In Once, the existing `Completed` and `Failed` rules apply unchanged.

A Continuous resource with an available target and no matching nodes is
`Ready` with a `NoNodesMatched` reason. It remains subscribed to target events.

For a non-paused Continuous resource, phase and conditions follow this table:

| Target available | Pending or active nodes | Permanent failures exceed `maxFailedNodes` | Phase | Conditions |
|---|---:|---:|---|---|
| No | Yes | Any | `Running` | `Progressing=True`, `TargetAvailable=False`, `Degraded=True` |
| No | No | Any | `Degraded` | `Ready=False`, `TargetAvailable=False`, `Degraded=True` |
| Yes | Yes | No | `Running` | `Progressing=True`, `Ready=False`, `TargetAvailable=True` |
| Yes | Yes | Yes | `Running` | `Progressing=True`, `Ready=False`, `Degraded=True` |
| Yes | No | Yes | `Degraded` | `Ready=False`, `Progressing=False`, `Degraded=True` |
| Yes | No | No | `Ready` | `Ready=True`, `Progressing=False`, `Degraded=False` |

When `maxFailedNodes` is nil, permanent failures do not exceed a threshold and
the resource can be `Ready` with a non-zero `status.failed`, matching the
existing Once tolerance semantics. `status.failed` and per-node status expose
these tolerated failures. When paused, `Paused` takes phase precedence while
conditions continue to describe the latest observed target and execution
state.

### Revision and Node Identity

For each desired node, the controller computes a short, deterministic revision
from the effective warmup Pod inputs after role merging and normalization. The
revision covers:

- image preload image references and pull-secret references;
- complete customized container specifications;
- volumes;
- tolerations; and
- other future fields that change the generated warmup Pod.

The execution identity is the tuple `(nodeUID, revision)`. A Node recreated
with the same name has a different UID and must be warmed again. Revision does
not include mutable scheduling controls such as `paused` or `parallelism`.

Warmup Pods gain this label:

```text
workloads.x-k8s.io/warmup-revision=<revision>
workloads.x-k8s.io/warmup-attempt=<positive integer>
workloads.x-k8s.io/warmup-node-uid=<Node UID>
```

The existing warmup name, Warmup UID, and node name labels remain. The Node UID
label distinguishes a recreated Node from an old Pod that still references the
same node name.

Because the Warmup spec is immutable, revision usually remains constant for
`targetNodes`. For `targetRoleBasedGroup`, the RBG Pods placed on a node can
change the set of roles and therefore the merged effective actions, producing a
new revision without changing the Warmup spec.

The controller does not inspect registry manifests, Secret data, PVC content,
host paths, or remote object stores. Users should use immutable image digests
and versioned model or artifact locations. Replacing content behind the same
reference is not observable and does not trigger a new warmup.

### Controller Flow

Each Continuous reconciliation performs these steps:

1. List owned warmup Pods and index them by node UID, node name, and revision.
2. Resolve the current target nodes and their effective actions.
3. Read each target Node UID and compute its effective revision.
4. Remove status and Pods for nodes that are no longer targeted.
5. For each desired execution identity:
   - keep a matching active or successful Pod;
   - advance retry accounting from the highest observed attempt label;
   - delete an obsolete active Pod before starting the new revision; and
   - create a missing Pod within the global `parallelism` budget.
6. After persisting an observed failure, retain it until the next attempt exists,
   then remove the previous failed Pod. After a new revision succeeds, delete
   obsolete terminal Pods for that node.
7. Update per-node status, aggregate counters, conditions, and phase.
8. Return a jittered safety requeue no later than five minutes in the future.

The controller never treats `Ready` or `Degraded` as a finished resource. Once
continues to route `Completed` and `Failed` to the existing TTL handler.

### Event Sources

The controller continues to watch `RoleBasedGroupWarmup` and owned Pods. In
addition, Continuous mode requires:

- Node create, delete, and label-change events;
- RBG workload Pod create and delete events, plus changes to `spec.nodeName` or
  the RBG role label; and
- target `RoleBasedGroup` deletion and recreation events.

RBG Pod and RBG events map to Warmups through a namespaced target-RBG field
index. Explicit node-name targets can also use an index. Arbitrary node label
selectors require filtering candidate Continuous Warmups from the informer
cache. Predicates must ignore unrelated Node and Pod updates.

Event-driven reconciliation provides prompt scale-out handling. A five-minute
jittered requeue repairs missed mappings or cache races. The initial release
does not expose this interval as API configuration.

For a `targetRoleBasedGroup`, the controller observes a workload Pod only after
the scheduler assigns it to a Node. Continuous Warmup is therefore not a
scheduling or startup barrier for that first Pod. Operators that require warmup
before admitting workloads should target a labeled node pool and arrange for
the pool to become workload-eligible only after the Warmup reports `Ready`.

### Failure Semantics

- `backoffLimitPerNode` is counted independently for each `(nodeUID,
  revision)`. A nil value retries indefinitely.
- A node becomes permanently failed after exhausting its retry limit.
- `maxFailedNodes` remains the accepted-failure threshold. Exceeding it places
  an otherwise settled Continuous resource in `Degraded`, but does not stop
  watches or prevent unrelated nodes from warming.
- If failed nodes leave the target set, the aggregate state is recalculated and
  the resource can return to `Ready`.
- A missing target RBG sets `TargetAvailable=False` and moves an otherwise idle
  Continuous resource to `Degraded`. Recreation triggers recovery.
- Transient API and cache errors return a reconcile error and use workqueue
  rate-limited retries; they do not become permanent node failures.
- `globalTimeoutSeconds` is rejected for Continuous. A long-lived resource has
  no single meaningful global deadline. A future KEP may add explicitly
  per-node or per-attempt timeout semantics.

### Pod Retention and Garbage Collection

Continuous does not use `ttlSecondsAfterFinished`. It keeps at most the current
terminal execution evidence per target node:

- the matching successful or permanently failed Pod is retained for status,
  retry recovery, and logs;
- a retry Pod carries a monotonically increasing attempt label, while
  `nodeStatuses.attempts` stores the highest persisted attempt;
- after the next retry Pod exists, the previous failed Pod can be deleted;
- an obsolete terminal Pod is removed after its replacement succeeds; and
- all Pods for a node are removed when that node leaves the target set.

Transiently, a node can have an old terminal Pod and one active replacement.
Steady-state Pod count is therefore bounded by the current target size even
when `backoffLimitPerNode` is nil and retries continue indefinitely. Owner
references continue to cascade-delete all Pods when the Warmup is deleted.

### Changing Warmup Content

Changing an image, model, customized action, target, or volume requires a new
resource. A recommended replacement flow is:

1. Pause the old Continuous Warmup so it creates no additional Pods.
2. Create a new Continuous Warmup with a distinct name and the new content.
3. Wait for the new resource to become `Ready`.
4. Delete the old resource.

An operator may instead overlap both resources when warming old and new
artifacts on the same node is safe and desired.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Node or Pod event fan-out | Use predicates and target indexes; retain periodic reconciliation only as a safety net. |
| Status size grows with pool size | Keep one compact entry per current target node and no history. |
| Mutable external content is not detected | Recommend image digests and versioned model/artifact references. |
| Multiple Once or Continuous Warmups target the same nodes | Treat them as independent resources; document pause-and-replace workflow and rely on each resource's parallelism. A Warmup is namespaced, but Nodes are cluster-scoped, so resources in different namespaces can select the same Node. |
| Admission webhook is unavailable | Use `failurePolicy=Fail`; existing reconciliation continues, but create/update requests fail until the webhook recovers. |
| Stricter updates surprise existing users | Document that execution-content updates were not reliably implemented and provide the create-new-resource migration path. |
| Periodic reconciliation causes synchronized load | Add jitter and cap the interval; event handlers remain the primary trigger. |

## Design Details

### Deterministic Effective Actions

The revision input must be independent of Go map iteration and informer list
order. For RBG targets, role names are sorted before merging. Images and pull
secrets are deduplicated and sorted. Customized containers are ordered by their
content hash after clearing controller-assigned names. Volumes are ordered by
name, and tolerations use a deterministic field ordering.

The current first-wins volume-conflict behavior must also use sorted role order
so the selected volume is deterministic. A volume conflict continues to emit
the existing warning event and condition.

Revision encoding uses a versioned canonical structure and a stable hash. Hash
collisions are treated as implementation defects; a sufficiently wide digest
is stored in status, while a Kubernetes-label-safe representation is used on
Pods.

### Status Reconstruction and Controller Restarts

Pods remain the durable execution evidence for active and terminal work.
`nodeStatuses` is also the durable retry checkpoint for Continuous mode. The
controller reconciles the two sources by taking the highest attempt recorded in
status or on a matching Pod. This makes failure observation idempotent across a
controller crash between a Pod update, status update, retry creation, and old
Pod deletion.

Pod and status matching always includes the owning Warmup UID. Recreating a
Warmup under the same name cannot adopt Pods from the deleted object.

Once continues to count retained failed Pods as specified by KEP-129.
Continuous uses the attempt label and `nodeStatuses.attempts`, allowing older
failed Pods to be removed without resetting an unlimited retry sequence. A
failed Pod must not be deleted until its attempt has been persisted or a higher
numbered retry Pod has been created.

### Webhook Integration

The validator follows the existing v1alpha2 webhook pattern:

- add `RoleBasedGroupWarmup.SetupWebhookWithManager`;
- register it from `cmd/rbgs/main.go`;
- add the kubebuilder webhook marker for create and update;
- generate the `ValidatingWebhookConfiguration` manifest;
- update Helm and consolidated installation manifests; and
- add validator unit tests plus webhook envtests.

The webhook performs no API reads. Validation is deterministic from the old and
new objects, avoids cache consistency concerns, and does not require additional
RBAC.

### Test Plan

#### Unit Tests

- Mode defaulting and phase selection for Once and Continuous.
- Validator create rejection for Continuous timeout and TTL combinations.
- Validator update acceptance for `paused`, `parallelism`, and Once TTL.
- Validator update rejection for mode, target, actions, images, containers,
  volumes, tolerations, and failure-policy changes.
- Revision stability across map and list ordering.
- Revision change when effective roles change.
- Node UID mismatch for a recreated node.
- Pod labels include the expected Node UID and prevent an old same-name Node
  execution from satisfying the new Node.
- Pending-node collection and global parallelism across revisions.
- Phase transitions among `Running`, `Paused`, `Ready`, and `Degraded`.
- Cleanup of departed nodes and obsolete revisions.
- Retry counts scoped to node UID and revision.

#### Integration Tests

- Node creation and selector-label changes enqueue only matching Continuous
  Warmups.
- RBG Pod scheduling, deletion, and role changes enqueue the indexed Warmup.
- Target RBG deletion produces `Degraded`; recreation recovers.
- A successful current revision is not rerun on periodic reconciliation.
- Controller restart reconciles attempt checkpoints and current state from
  status and Pods without duplicating an attempt.
- Webhook admission rejects immutable-field updates and invalid mode-policy
  combinations with useful field paths.

#### End-to-End Tests

- Existing Once scenarios continue to pass without specifying `mode`.
- Continuous initial reconciliation reaches `Ready`.
- Adding a target Node creates a warmup Pod and returns to `Ready`.
- Removing a target Node cleans its status and Pods.
- Recreating a Node under the same name executes warmup for the new UID.
- Scheduling an RBG role onto a new node triggers its effective actions.
- Pause prevents new Pod creation and resume drains the pending set.
- Permanent node failure moves an over-threshold resource to `Degraded` while
  other newly added nodes can still warm.

### Upgrade and Downgrade Strategy

On upgrade, the CRD defaults an absent `mode` to `Once`; existing objects and
their controller behavior remain one-shot. New optional status fields do not
affect old clients. The validating webhook begins enforcing execution-content
immutability for existing objects on their next spec update.

Continuous objects must be paused or deleted before downgrading to a controller
that does not understand the new phase values and watches. Downgrade automation
must install the older CRD only after no Continuous objects remain. Once objects
remain compatible.

### Scalability

The feature adds watches for Nodes and RBG workload Pods and adds one status
entry per current target node. It does not add a new API type. The controller
already requires list/watch access to Nodes, Pods, and RBGs; this KEP changes
which events enqueue Warmup reconciliations.

The main scalability risk is Node label churn combined with arbitrary label
selectors. Predicates limit events to create, delete, and label changes. The
mapper evaluates only Continuous Warmups that use node selectors. RBG Pod
events use a namespaced field index and do not scan unrelated Warmups.

The periodic safety reconciliation is jittered and capped at one enqueue per
Continuous resource per five minutes. Scale testing should cover the supported
Node and Warmup cardinalities before promotion beyond alpha.

### Security

The controller does not read Secret content or external model data to compute
revisions. Pull-secret names and volume references are hashed as API inputs,
but their content is neither logged nor copied into status.

The new webhook uses no external calls and has `sideEffects=None`. Fail-closed
admission can temporarily prevent Warmup create/update operations during a
webhook outage, but it does not interrupt existing Pods or controller
reconciliation.

### Observability and Troubleshooting

Operators can inspect:

- `status.phase` for the high-level state;
- aggregate desired, active, succeeded, and failed counters;
- `status.nodeStatuses` for node UID, revision, attempts, and last transition;
- `Ready`, `Progressing`, `Degraded`, and `TargetAvailable` conditions;
- warning events for target, Pod creation, and volume-conflict failures; and
- retained terminal Pod logs for the current node execution.

A Continuous resource stuck in `Running` indicates pending work or an active
Pod. `Degraded` indicates an unavailable target or failures beyond policy. A
node with an unchanged mutable external artifact reference is intentionally not
re-executed; operators should create a new Warmup using immutable references.

### Graduation Criteria

Alpha requires:

- API, webhook, controller, unit, integration, and end-to-end tests described
  above;
- regression coverage showing omitted mode remains Once;
- documented scale-out, replacement, pause, and troubleshooting workflows; and
- no unbounded Pod or status history.

Beta requires production feedback from autoscaled inference pools, scale tests
for Node and Warmup event fan-out, stable end-to-end tests, and metrics for
reconcile latency and per-node outcomes.

## Implementation History

- 2026-09-12: Issue #465 opened for lifecycle discussion.
- 2026-09-22: A maintainer requested a KEP covering API and controller
  ownership semantics.
- 2026-09-23: Initial KEP drafted.

## Drawbacks

- Continuous resources and one retained terminal Pod per target node are
  long-lived.
- A validating webhook becomes part of Warmup create/update availability.
- Users must create a new resource for every image or model definition.
- Mutable external content cannot be detected automatically.
- Node selector events can cause additional reconciliation load.

## Alternatives

### Add a `WarmupExecution` CRD

The parent could create a child resource for every target or content revision.
This gives explicit history and independent TTL semantics, but adds another API,
controller, RBAC surface, status aggregation path, and garbage collector. It is
unnecessary for the initial no-history requirement.

### Periodic Reconciliation Only

A timer avoids Node and RBG Pod watches, but every scale-out waits for the next
scan and all Continuous objects perform repeated full discovery. Event-driven
reconciliation with a periodic safety net provides lower latency without making
the timer correctness-critical.

### External Operator Creates Once Warmups

An autoscaler or higher-level operator could create a new Once resource for
every node-pool change. This keeps RBG unchanged but duplicates target
discovery, retry, cleanup, and status semantics in every integration.

### Allow In-Place Image and Model Updates

The controller could treat spec changes as new executions. That requires
versioned history, cancellation semantics, policy reset rules, and careful
handling of mixed revisions. It also makes one resource represent multiple
unrelated artifact definitions. This KEP deliberately requires a new Warmup
resource instead.

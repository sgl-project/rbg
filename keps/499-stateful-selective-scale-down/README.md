# KEP-499: Stateful selective scale-down

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
- [Goals](#goals)
- [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [API](#api)
  - [Desired Membership](#desired-membership)
  - [User Workflows](#user-workflows)
- [Design Details](#design-details)
  - [Validation and Conversion](#validation-and-conversion)
  - [A Shared Ordinal Planner](#a-shared-ordinal-planner)
  - [Deletion and Lifecycle Hooks](#deletion-and-lifecycle-hooks)
  - [Rolling Updates, Partition, and Surge](#rolling-updates-partition-and-surge)
  - [Persisted Rollback Protection](#persisted-rollback-protection)
  - [Scheduling and Service Discovery](#scheduling-and-service-discovery)
  - [ScalingAdapter and Other Writers](#scalingadapter-and-other-writers)
  - [Status and Observability](#status-and-observability)
- [Risks and Mitigations](#risks-and-mitigations)
- [Test Plan](#test-plan)
  - [Unit Tests](#unit-tests)
  - [Integration Tests](#integration-tests)
  - [End-to-End Tests](#end-to-end-tests)
- [Graduation Criteria](#graduation-criteria)
- [Upgrade, Downgrade, and Version Skew](#upgrade-downgrade-and-version-skew)
- [Alternatives](#alternatives)
- [Implementation Plan](#implementation-plan)
- [Open Questions](#open-questions)
- [Implementation History](#implementation-history)
- [References](#references)
<!-- /toc -->

## Summary

Tracking issue: [sgl-project/rbg#499](https://github.com/sgl-project/rbg/issues/499).
Status: **provisional**. This PR proposes a design; it does not implement the API,
change controller behavior, or claim release readiness.

Add persistent ordinal reservations to native Stateful RoleInstanceSets and expose
that intent through RoleBasedGroup roles. Operators can retire selected logical
instances without renumbering or recreating unrelated higher-ordinal survivors.
A reservation retires an identity, including every component Pod of that
RoleInstance. The desired replica count remains independent of reservations.

For example, a role running ordinals `[0,1,2,3,4]` can converge to `[0,2,4]` by
updating `replicas=3` and `reserveOrdinals=[1,3]` in one API request. Reconciliation
must continue to skip 1 and 3 after a controller restart and subsequent scale-out.

Reservation membership follows the semantics of OpenKruise Advanced StatefulSet.
RBG-specific partition, surge, and rollback behavior is specified separately below.

## Motivation

A stateful inference replica can contain valuable local model state, warm caches,
or multiple cooperating component Pods. During node maintenance, relocation, or
application migration, the desired retirement targets may be low ordinals rather
than the highest ordinals chosen by ordinary scale-down.

Reducing only `replicas` can remove healthy higher ordinals. Directly deleting a
RoleInstance leaves its desired identity unchanged, so reconciliation recreates
it. A one-shot deletion request cannot express that an identity must stay absent.

As of upstream main `a0b11e6a`:

- RoleSpec and RoleInstanceSetSpec do not expose `reserveOrdinals`.
- The stateful topology contains an always-empty reservation set as a future hook.
- Stateful creation, rollout, and rollback use dense ordinal intervals elsewhere.
- `scaleStrategy.roleInstanceToDelete` is consumed by the stateless flow; it is
  not a persistent stateful identity-retirement mechanism.

The proposal therefore includes the entire sparse-membership control path, not
only adding an API field or skipping an entry during creation.

## Goals

- Persistently skip selected ordinals while keeping `replicas` as a count.
- Preserve unrelated survivor names, ordinals, and UIDs during isolated selective
  scale-down with unchanged templates and no independent lifecycle event.
- Retire all component Pods through their owning RoleInstance lifecycle.
- Support native Stateful RoleInstanceSets created directly or through RBG roles.
- Preserve lifecycle hooks, ownership checks, partition counts, rollout budgets,
  paused-rollout behavior, and controller-restart recovery with sparse ordinals.
- Keep scheduler membership and service discovery consistent with desired members.
- Preserve existing behavior when reservations are absent or empty.

## Non-Goals

- Automatic failure detection, target selection, node fencing, force deletion,
  traffic cutover, or data migration.
- Guaranteeing exactly-once application execution after a node becomes unreachable.
- Retiring an individual worker Pod within a multi-Pod RoleInstance.
- A transaction across multiple Kubernetes objects, or a new imperative scale API.
- Native Kubernetes StatefulSet, external LeaderWorkerSet, or other compatibility
  backends; stateless specified deletion remains unchanged.
- A nonzero starting ordinal, automatic compaction, or automatic reservation expiry.
- Creating, migrating, or changing retention policies for application PVCs.

## Proposal

### API

Add the optional field to both native desired-state surfaces:

```go
// RoleSpec
ReserveOrdinals []intstr.IntOrString `json:"reserveOrdinals,omitempty"`

// RoleInstanceSetSpec
ReserveOrdinals []intstr.IntOrString `json:"reserveOrdinals,omitempty"`
```

Entries are nonnegative integers or inclusive range strings such as `"3-5"`.
A numeric string such as `"3"` has the same meaning as integer `3`. Duplicate or
overlapping entries describe a set union, not additional replicas to subtract.
The controller normalizes intervals internally without rewriting user intent.

For an RBG-managed set, `spec.roles[i].reserveOrdinals` is authoritative and is
propagated to the corresponding child in the same reconciliation write as its
replicas. Direct edits to that managed child are not a second control surface.
RoleBasedGroupSet and RoleTemplate expansion must preserve the role field too.

The following is a proposed role fragment; the existing pattern and Pod templates
are omitted. These fields are not supported by current releases.

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: serving-group
spec:
  roles:
    - name: decode
      replicas: 3
      reserveOrdinals: [1, 3]
```

For an independently managed native RoleInstanceSet:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleInstanceSet
metadata:
  name: decode
spec:
  replicas: 3
  reserveOrdinals: [1, 3]
```

### Desired Membership

Let `N` be desired replicas and `R` the normalized set of reserved ordinals.
Define `A(N,R)` as the **first N nonnegative integers not in R** in ascending order.
This is the steady-state membership, independent of which instances currently exist.

| N | R | A(N,R) | Meaning |
| --- | --- | --- | --- |
| 5 | empty | 0,1,2,3,4 | Existing dense behavior |
| 3 | 1,3 | 0,2,4 | Selectively remove two instances |
| 5 | 1,3 | 0,2,4,5,6 | Replace retired capacity at new ordinals |
| 4 | 1,3 | 0,2,4,5 | Scale out without restoring reserved identities |
| 3 | 1,3,100 | 0,2,4 | Future reservations do not consume replica count |
| 2 | 0-3 | 4,5 | A reserved prefix is skipped |
| 0 | any valid R | empty | No desired instances |

Consequently `N + len(R)` is not generally a valid end ordinal. A reservation far
beyond the active prefix does not increase the current window. Status replica
counts count actual instances, not ordinal span lengths or reserved slots.

### User Workflows

**Selective scale-down.** Read the current parent identity, generation, resource
version, replicas, reservations, and selected RoleInstance ordinals. Validate that
new targets belong to the current desired base membership. In one parent update,
reduce N by the number of newly selected base identities and add their ordinals
to R. For `[0,1,2,3,4]`, selecting 1 and 3 produces `(3, {1,3})`.

The client must submit the replica and reservation changes atomically, using
resource-version optimistic concurrency or a JSON Patch with UID and
resourceVersion tests. If changing several roles, their desired-state changes can
share one RBG patch; child convergence is still asynchronous and independent.

Retries resubmit the same absolute target pair `(N,R)`. They must not repeatedly
subtract selected ordinals from an already reduced replica count. On a conflict,
re-read and revalidate user intent rather than automatically merging an unrelated
concurrent membership change. This API selects logical ordinals, not a transactional
snapshot of Pod or RoleInstance UIDs across objects.

**Replacement at a new identity.** Add reservations without changing N. The
controller retires the selected identities and fills the first N eligible slots.
This is deliberately different from shrinking capacity.

**Later scaling.** Change N and keep R. Previously retired ordinals remain absent.
For example, `(3, {1,3})` to `(4, {1,3})` adds ordinal 5.

**Reusing identities.** Explicitly removing reservations changes membership even
when N is unchanged. Removing all reservations from `(3, {1,3})` changes the base
from `[0,2,4]` to `[0,1,2]`; it can recreate 1 and replace 4. Therefore reservations
are durable desired state, not operation records to clear after success.

To reset numbering without replacing running survivors, first converge to N=0,
wait for all owned RoleInstances including terminating ones to disappear, resolve
any rollout replacement state, and only then clear R before the next scale-out.
The controller does not perform this reset automatically.

## Design Details

### Validation and Conversion

- Accept nonempty reservations only for the native Stateful RoleInstanceSet path.
  Reject them for stateless roles and unsupported compatibility workloads instead
  of silently ignoring them. Check the actual backend and pattern; a generic
  "stateful role" helper also classifies unsupported external workloads.
- Validate integer/range syntax, nonnegative endpoints, increasing range bounds,
  and representability of every generated ordinal. Use widened arithmetic for
  counts and interval boundaries. Propose a maximum of 1,024 input entries; the
  exact resource bound is subject to API review.
- Validate the complete target replicas/reservations/rollout combination, including
  existing nonzero-replica partition and budget constraints. Do not silently alter
  partition or rollout policy to make a scale request pass validation.
- Use merged intervals and bounded traversal, not expansion proportional to the
  largest reserved ordinal or the size of a range. Reject a target that cannot
  fit N plus the permitted surge identities in the supported ordinal domain.
- Add structural schemas, deepcopy/client generation, and conversion coverage.
  Every served API version that can update the same stored object must preserve
  reservations and new rollback state through a round trip. This includes RBGSet
  templates. The feature must not be enabled with a lossy conversion path.
- CRDs and webhooks must be upgraded before clients write this field. Clients
  should use strict server-side field validation; pruning an unknown reservation
  field while accepting a smaller replica count would select the wrong victims.

### A Shared Ordinal Planner

Introduce a planner that returns ordered base identities, optional surge identities,
ordinal-to-rank lookup, and interval membership from one `(N,R,rollout)` snapshot.
All create, delete, update, availability, and completion decisions use this plan.

For each nonreserved ordinal o, define its logical rank as its position among the
nonreserved ordinals. A base ordinal has rank in `[0,N)`. Reserved identities and
nonreserved identities outside the planned base/surge membership are condemned.
Only planned missing identities are created; a reservation never becomes an empty
array slot that a later helper accidentally fills.

Keep allocation proportional to planned capacity and interval count. In particular,
replace the dense `make(..., endOrdinal-startOrdinal)` assumption when a reserved
prefix can make the first desired ordinal large. A range such as `"0-1000000000"`
with N=1 must not allocate or iterate a billion slots.

Changing only N or R must not produce a new Pod-template revision. Reconciliation
must distinguish membership changes from template changes. Parent/child propagation
must preserve both fields, including explicit clearing of reservations.

### Deletion and Lifecycle Hooks

A reserved existing RoleInstance enters the existing condemned/deletion flow.
Honor pre-delete lifecycle hooks, readiness withdrawal, finalizers, and the
established ordered/parallel deletion behavior. Use current owner UID checks and
UID-preconditioned deletion to avoid affecting a same-named replacement object.
The RoleInstance owns cleanup of all its component Pods.

An unhealthy selected low ordinal must not be blocked solely by an unrelated
unhealthy survivor whose identity is retained. Any exception to existing health
ordering must be narrowly defined for already-unhealthy retired targets; it must
not bypass lifecycle hooks or permit extra healthy disruption beyond the existing
policy. The implementation must document and test this ordering explicitly.

For an unreachable node, record and report the outstanding deletion. Do not
force-delete Pods or finalizers, assume a process has stopped, or promise safe
migration. An external operator may fence the node and complete cleanup. Other
roles' healthy instances are not retirement targets by implication.

### Rolling Updates, Partition, and Surge

The RoleInstanceSet API describes partition as a **count** of instances retained
at the old revision. Resolve the existing absolute/percentage partition against N
using the controller's existing rounding and admission rules, then preserve the
first P base identities by logical rank. Update the remaining N-P identities.
This RBG-specific sparse mapping is a proposal, not a claim that every OpenKruise
rolling-update detail is identical.

For N=3, R={1,3}, and P=1, ordinal 0 is protected while 2 and 4 are eligible.
A raw comparison `ordinal < P` or iteration over `[P,N)` is no longer sufficient
for membership, completion, or revision assignment.

Resolve maxSurge and maxUnavailable against N, not the expanded ordinal span.
Allocate surge from the next nonreserved ordinals after the base, following the
existing computed surge budget. Neither base nor surge may use a reservation.
All revision, health, minReadySeconds, paused-rollout, and surge-stickiness checks
must use the same planned identities and existing readiness requirements.

Reservations apply to surge identities too. Reserving a current surge identity
retires it; retain or replace only still-eligible surge capacity under the existing
budget. Pausing must preserve eligible existing surge without allocating fresh
surge solely because reservations changed. Once the rollout finishes, remove surge
without disturbing surviving base identities.

### Persisted Rollback Protection

Upstream now persists `status.rolloutReplacementState` before early-rollback base
replacement. Its Replicas/Partition currently describe dense raw ordinal bounds.
Reinterpreting those values as ranks after R changes could protect the wrong
instances or release surge too early.

For sparse membership, persist an explicit, compact snapshot of protected logical
ordinals before issuing replacements. A proposed additive field is
`rolloutReplacementState.protectedOrdinals`, using the same interval vocabulary.
Keep existing revision fencing and observed-generation/health checks.

Completion checks use the intersection of that recorded identity set with the
current eligible base update set. A scale/partition/reservation change alone does
not extend the recorded scope. New replacement work can explicitly extend it.
Retired identities stop blocking completion, but unrelated surviving identities
already in the protected scope still gate release until they converge.

Read legacy state without the new field as its recorded **raw** `[Partition,
Replicas)` identity scope; do not recompute that historical scope from new ranks.
Preserve existing empty-reservation behavior. Persist any extended sparse scope
before deletion, retain it across pause/restart/leader changes, and clear it only
on the existing completion or revision-change boundaries. Final status shape and
migration details require review with the existing rollback implementation.

### Scheduling and Service Discovery

Audit code that derives membership from `0..replicas-1`, an ordinal span, or raw
partition bounds. Feed the planned identities to gang-size/topology decisions,
ordinal-aware addresses, peer lists, environment injection, and membership-derived
configuration. Desired membership excludes reservations; observed readiness still
comes from real objects and conditions.

Preserve a survivor's ordinal label, instance name, Pod names, and identity-based
addressing. Do not compact identities to make lists dense. A reserved member must
not remain a peer merely because its ordinal is below N, and a higher surviving
ordinal must not disappear merely because its ordinal is at least N.

Service selectors and endpoint readiness remain Kubernetes-native. This KEP does
not make deletion synchronously drain connections; existing lifecycle and external
traffic management are still required. Desired peer configuration and observed
terminating endpoints may converge at different times.

### ScalingAdapter and Other Writers

Reservations do not transfer ownership of replicas away from an existing HPA,
ScalingAdapter, or other writer. Guaranteed manual selective scale-down requires
one writer to control the target replica update for that operation.

For an RBG-managed ScalingAdapter, the operator workflow is to stop competing
replica writers, disable the selected role's adapter, and wait for the old adapter
to be absent before patching `(N,R)`. A terminating adapter is not sufficient proof
that no stale adapter will be reused after re-enable. Re-enable only after the
new desired state has been accepted, allowing a fresh adapter to initialize from N.
This is client orchestration, not a new hidden transaction in the RBG controller.

Autoscaling can subsequently change N while preserving R; new capacity skips the
retired identities. The KEP does not claim that a manual target N persists while
an autoscaler is actively selecting a different N. Direct RIS users must likewise
coordinate writers. Existing scale subresources must preserve reservations.

### Status and Observability

Keep replicas, readyReplicas, availableReplicas, and revision counts as counts of
real instances under their existing contracts, including surge where currently
applicable. Desired N excludes reserved slots. Report observedGeneration only
through the established observation contract; it is not a synchronous deletion
receipt.

Use existing FailedScale/FailedUpdate conditions and warning events to expose
invalid/unplannable membership, ownership conflict, or blocked deletion, without
inventing a parallel phase model. Events should identify the role, ordinal, and
reason. Operators verify that reserved RoleInstances are absent and the intended
survivors remain, not just that replicas equals a desired number. Avoid unbounded
per-ordinal metric labels.

## Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| Replicas and reservations written separately choose unintended victims | One parent update with optimistic concurrency; absolute-target retries |
| Clearing R recreates retired identities | Durable desired state; explicit reset workflow |
| Dense rollout helpers omit high survivors or recreate holes | Shared rank-aware planner and sparse rollout/rollback tests |
| Restart loses rollback protection | Persist exact protected identities before replacement |
| Long range exhausts memory or overflows arithmetic | Interval processing, input bounds, widened math, capacity validation |
| Old controller or conversion drops reservations | Upgrade ordering, fail-closed feature handling, round-trip tests |
| Node is unreachable but processes remain alive | No fencing or force deletion; external operator responsibility |
| Competing autoscaler overwrites N | Explicit single-writer workflow and subsequent N/R semantics |
| Existing finalizer delays completion | Expose blocked deletion and preserve the hook |

## Test Plan

This KEP-only PR adds no implementation tests and does not claim the following
scenarios already pass. They are acceptance requirements for the implementation.

### Unit Tests

- Parse integers, numeric strings, ranges, overlap/duplicates, negatives, invalid
  bounds, large reserved prefixes, and overflow/resource limits.
- Verify the membership table, future reservations, scale to zero, partial
  unreservation, and scaling up/down without compacting identities.
- Validate backend/pattern restrictions, feature-gate behavior, template expansion,
  served-version round trips, and scale-subresource preservation.
- Verify replica/revision hashes do not turn a membership-only change into a rollout.
- Cover rank-based partition and existing percentage rounding, sparse surge,
  paused rollout, early rollback, and protected-scope changes across R/N changes.

### Integration Tests

- RBG and RBGSet propagate N/R together to native child sets; RoleTemplate expansion
  and conversion preserve them, and direct child edits do not override the parent.
- Converge N=5 to N=3,R={1,3}; selected instances disappear, survivors 0/2/4 retain
  their UIDs, and every component Pod of each retired instance follows its lifecycle.
- Lifecycle hooks block deletion until released; an unhealthy retired instance can
  make progress under the specified ordering without selecting an unrelated survivor.
- Restart the controller during deletion and during sparse early rollback; the
  reserved identities stay absent and eligible surge protection is not lost.
- Retry the same absolute patch, exercise stale resourceVersion/UID preconditions,
  and verify unrelated configuration or a recreated parent is not overwritten.
- Populate scheduler/discovery inputs with sparse members and verify no reserved
  peer or missing high-ordinal survivor; preserve existing endpoint-readiness rules.

### End-to-End Tests

- Standalone and multi-component native Stateful roles; healthy survivor UID checks.
- Selective shrink, replacement with unchanged N, later scale-out, range reservations,
  zero replicas, and an explicit stop/reset/restart identity-reset procedure.
- Paused/active rolling update with nonzero partition, percentage budgets, surge,
  early rollback, controller restart, and reservation changes affecting surge.
- Unreachable-node scenario: observe blocked cleanup and externally fenced recovery;
  verify no automatic force deletion or claim of fencing by this feature.
- Adapter disable/old-object removal/re-enable and subsequent HPA scaling with R;
  verify stale adapter data cannot undo the selected target during the manual workflow.
- Regression with empty R: ordinary scaling, stateful rollouts, stateless specified
  deletion, compatibility workloads, and controller ownership checks remain unchanged.

## Graduation Criteria

Propose an opt-in alpha feature gate named `StatefulReserveOrdinals` (final naming
subject to review). Gate both admission and reconciliation. If a controller sees
nonempty reservations while the gate is disabled, it must report an unsupported
configuration and avoid a dense-membership fallback. Silently ignoring R is unsafe.

Alpha requires all schemas/conversions and all consumers listed above, plus sparse
scale, rollout, and restart-recovery tests. A field-only implementation is incomplete.
Beta requires demonstrated lifecycle, node-failure, version-skew, and autoscaling
coverage, documented operational procedures, and maintainer review of defaults and
resource bounds. No target release or graduation approval is claimed here.

## Upgrade, Downgrade, and Version Skew

1. Upgrade CRDs, conversions, and validating webhooks before reservation writers.
2. Upgrade every controller that can reconcile these objects; do not run an old
   dense-only reconciler concurrently with nonempty reservations.
3. Enable the feature only after conversion, scheduler, discovery, and rollback
   support are deployed and verified, then allow users to write R.
4. Keep reservations through normal upgrades. Unknown-field preservation alone does
   not make an old controller safe to reconcile a sparse desired membership.

Disabling the gate or downgrading with live sparse membership is not supported.
This explains `disable-supported: false` in the provisional metadata. To downgrade,
stop replica writers, scale to zero, complete owned-instance cleanup, safely resolve
rollback state, clear reservations, and verify dense-compatible objects before
starting old controllers. A live transition back to dense identities is a separate,
explicitly disruptive user operation, not automatic rollback behavior.

## Alternatives

- **Reduce replicas only:** simple, but removes the highest ordinals rather than
  preserving the operator-selected survivors.
- **Delete selected Pods or RoleInstances:** recreates desired identities and lacks
  persistent retirement; deleting a component is also the wrong logical unit.
- **Reuse stateless roleInstanceToDelete:** one-shot name-based selection does not
  define persistent sparse stateful membership or later scale-out behavior.
- **A separate retirement-request resource:** useful for a future higher-level
  workflow, but still needs a durable membership model and adds another API now.
- **Native/Advanced StatefulSet integration:** Advanced StatefulSet is useful prior
  art; substituting the workload would not directly provide native multi-component
  RoleInstance lifecycle, scheduling, or RBG rollout integration.

## Implementation Plan

This PR contains the KEP and metadata only. Follow-up implementation should:

1. Add API validation, schemas, generation, conversion, and gate plumbing.
2. Implement the shared planner and parent/template propagation.
3. Integrate stateful creation/deletion and lifecycle ordering.
4. Make partition, surge, status, and persisted rollback protection sparse-aware.
5. Audit and update scheduling/discovery and scale-writer interoperability.
6. Add the acceptance tests and operator documentation before enabling the feature.

Steps 1-5 are a single completeness requirement for an enabled feature; intermediate
patches must keep the feature disabled and must not expose partial retirement behavior.

## Open Questions

- Confirm integer/range vocabulary and the proposed entry limit during API review.
- Review rank-based partition mapping against the existing count-based API contract.
- Review the additive exact-identity rollback state and legacy-state conversion.
- Confirm the gate name, introduction release, and criteria for a future default.
- Review whether stronger automation for writer coordination belongs in a later API.

## Implementation History

- 2026-10-23: Initial provisional KEP for issue #499, based on upstream main
  `a0b11e6a`; implementation and release approval are pending.

## References

- [Tracking issue #499](https://github.com/sgl-project/rbg/issues/499).
- [OpenKruise Advanced StatefulSet ordinal reservations](https://openkruise.io/docs/user-manuals/advancedstatefulset#ordinals-reserveskip).
- [InstanceSet KEP](../30-instanceset/README.md).
- [Current RoleInstanceSet API](../../api/workloads/v1alpha2/roleinstanceset_types.go).
- [Current stateful topology and rollout helpers](../../pkg/reconciler/roleinstanceset/statefulmode/stateful_instance_set_utils.go).
- [Current stateful control flow](../../pkg/reconciler/roleinstanceset/statefulmode/stateful_instance_set_control.go).
- [Current parent ownership protection #500](https://github.com/sgl-project/rbg/pull/500).
- [Persisted stateful rollback protection #508](https://github.com/sgl-project/rbg/pull/508).

# KEP-173: Inactive Pod Handling Enhancement

Refs #542. This proposal covers explicit failure signals and best-effort observed
member deletion, not every missing-member or NotReady recovery scenario.

<!-- toc -->
- [Background and Motivation](#background-and-motivation)
- [Goals](#goals)
- [Non-Goals](#non-goals)
- [Design Proposal](#design-proposal)
  - [User Stories](#user-stories)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Detection and Reconciliation Outline](#detection-and-reconciliation-outline)
  - [RoleInstance Controller Changes](#roleinstance-controller-changes)
  - [RestartPolicy Handling Matrix](#restartpolicy-handling-matrix)
  - [Test Plan](#test-plan)
<!-- /toc -->

## Two Levels of Failed Pod Handling

This KEP addresses two distinct levels of handling when a Pod enters Failed state (Evicted, UnexpectedAdmissionError, etc.). Understanding this separation is critical for correct implementation.

### Level 1: Ordinary Failed-Pod Repair (All RestartPolicies)

| Aspect | Description |
|--------|-------------|
| **Trigger** | Pod enters Failed state (Phase=Failed) |
| **Handler** | RoleInstance Controller (`instance_scale.go`) |
| **Behavior** | When no group recovery/backoff/drain or budget suppression takes precedence, delete Failed pod, then create replacement on next reconcile |
| **Why delete first** | `hasOrphanPod` blocks creation when same-name pod still exists |
| **Key point** | This follows native K8s StatefulSet behavior - Failed Pods with fixed names must be deleted before replacement |

### Level 2: Recreate All Member Pods (Either Recreation Policy)

| Aspect | Description |
|--------|-------------|
| **Trigger** | Eligible Failed-pod or unexpected ordinary-container restart under either recreation policy; observed desired-member deletion additionally triggers `RecreateRoleInstanceOnPodRestartFromCreation` |
| **Handler** | RoleInstance Controller (`instance_scale.go` → `shouldRecreateInstance`) |
| **Behavior** | Persist all old member names/UIDs → drain every recorded UID → create replacements; preserve the RoleInstance object and UID |
| **Key point** | Only the affected instance's Pods are rebuilt; legacy Ready/recent-restart eligibility is preserved, while the new policy permits startup recovery |

### Container Restart Handling

| Aspect | Description |
|--------|-------------|
| **Trigger** | Unexpected ordinary-container restart; init containers and native sidecars are not inspected |
| **Handler** | RoleInstance controller for RoleInstanceSet-backed roles; LWS controller for LWS-backed roles |
| **restartPolicy involved** | Both recreation policies on RoleInstanceSet; only legacy `RecreateRoleInstanceOnPodRestart` on LWS |
| **Key point** | RoleInstanceSet-backed roles handle restart detection in `shouldRecreateInstance`, including in-place-update allowances; LWS-backed roles remain delegated because RBG only manages the LeaderWorkerSet CR |

### Why Two Levels

The RoleInstance Controller handles Failed pods in a two-level approach:

1. **Level 1 (pod-level replacement)**: For `RestartPolicy=None`, a Failed pod is deleted, then normal reconciliation detects the missing pod and creates a replacement. This is analogous to how StatefulSet handles Failed pods with ordinal identity.

2. **Level 2 (group rebuild)**: Under either recreation policy, an eligible fault retires all member Pods, including active, inactive, and `Ignore` members. The RoleInstance object is retained. The durable UID barrier completes before any replacements are created. The legacy policy retains its Ready/recent-restart gate; the new policy additionally handles startup faults and observed member deletion.

## Policy Compatibility

`RecreateRoleInstanceOnPodRestart` retains its existing eligibility for Failed-pod and unexpected ordinary-container restart triggers: Ready or a recent restart is required, and desired-member deletion is not a trigger. Durable draining changes how an accepted round completes, not which faults opt existing workloads into recovery.

`RecreateRoleInstanceOnPodRestartFromCreation` is a separate opt-in policy for RoleInstanceSet-backed roles. It handles the same explicit faults before first Ready and observed deletion (`deletionTimestamp != nil`) of a desired member. Desired generation must be observed and current/update revisions must match. Missing slots alone are not triggers. This policy is rejected for LeaderWorkerSet-backed roles because vendored LWS v0.7 cannot implement the additional semantics; the older policy remains supported there. The default remains `RecreateRoleInstanceOnPodRestart`; upgrading does not switch existing workloads to the new policy, and Deployment/StatefulSet behavior is unchanged.

**Upgrade behavior:** a legacy `Restarting=True` condition without a durable
`status.retiringPods` ledger is not a resumable drain or a Ready-based suppression
latch. It can clear and a still-visible eligible fault may cause one extra rebuild,
subject to normal backoff and budget. This is not a one-release migration. Update
the CRDs together with the controller so accepted drains can persist their ledger.

## Bounded RoleInstance Recovery (phase 2)

This extends the whole-instance recovery path using the circuit-breaker contract of
[LWS KEP-820](https://github.com/kubernetes-sigs/lws/blob/cc5462087f10c3bef95e20dc452fc01c76513484/keps/820-distributed-preflight-check/README.md),
adapted to native RoleInstanceSet/RoleInstance ownership rather than LWS ordinals
and retained leader Pods. It does not add support to the backing LWS adapter.

### API and lifetime

`RestartPolicyConfig.maxGroupRestarts` is optional `*int32` with minimum zero. It
requires an explicit whole-RI recreate policy (either the old readiness-gated policy
or the new opt-in failure policy); `None` is rejected through schema validation.
Role admission also rejects budgets on Deployment, StatefulSet and LeaderWorkerSet
backends. Role configuration is propagated to the native RI template.

RI status is the source of truth: `groupRestartCount`, `groupRestartRevision`, and
`restartBudgetExhausted`. RI UID is the per-replica identity. The opaque revision
identifies component Pod templates and Pod-affecting component metadata, not sizes,
policy, backoff, or budget. An accepted group rebuild consumes exactly one attempt.
Repeated/suppressed attempts and the exhaustion termination consume none. Persist
accounting and restart tracking with optimistic locking before deleting any Pod,
after uncached RI UID/generation and owned failure-evidence revalidation. Keep the
budget mutation composable with a retiring-Pod UID ledger in a single status CAS.

The durable budget is independent of backoff `restartCount`, which may reset after
a quiet period. Ready/quiet transitions, policy edits, and limit edits/removal do
not reset budget accounting. Unsetting the limit allows unbounded recovery before
exhaustion without discarding an existing count. Zero stops at the first eligible
fault, not initial creation. Exhaustion is evaluated before backoff.

### Exhaustion, resource release, and status

Persist `restartBudgetExhausted=true`, set RI `Degraded=True` with reason
`GroupRestartBudgetExceeded`, and force `RoleInstanceReady=False`. Suppress all
replacement creation and updates. Then add
`rbg.workloads.x-k8s.io/group-restart-budget-cleanup` to the COMPLETE owned group's
Pods that can still receive it, before requesting deletion of any of them. Already
terminating Pods cannot gain finalizers. Use owner-UID validation, optimistic
metadata patches and UID-only deletion preconditions so concurrent kubelet status
updates do not prevent deletion or accidentally target a same-name replacement.

Retained Pod API objects preserve status and coordinate recovery, not running
containers or resource reservations. Kubernetes 1.27+ kubelet deletion transitions
Pods to a terminal phase; only then are scheduled CPU/memory/GPU resources released.
Never synthesize Pod phases. Native kubelet/CRI logs are best effort, NOT guaranteed;
external log export is required for durable diagnostics. Healthy RI replicas remain
independent. Suppression survives process restarts and missing retained Pod objects.

### Recovery and teardown

Recovery is requested on the RI by the exact annotation
`rbg.workloads.x-k8s.io/recover=true`. Remove any pre-existing annotation before the
optimistic exhaustion status patch, so stale intent cannot automatically recover a
newly exhausted group. Post-exhaustion recovery first removes Pod budget finalizers,
then clears suppression/count and backoff tracking and consumes the annotation.
A crash between these steps is retryable from persistent RI state. A changed Pod
template similarly releases obsolete retained objects and starts a fresh budget.
Policy or limit edits alone cannot unfreeze an exhausted RI.

The RI also carries the cleanup finalizer: background deletion must not remove the
only cleanup owner before Pod finalizers are released. On RI deletion (including
scale-down/rollout replacement), remove only budget finalizers from Pods controlled
by that RI UID, then remove its RI cleanup finalizer. Do not reset/charge counters,
interpret teardown as recovery, or create a replacement. The generic status writer
must preserve live budget fields and Degraded across stale cached writes, preserve
external conditions and observedGeneration, and enforce Ready=False while exhausted.

### Verification

Unit coverage includes nil/zero/positive limits, exact counts, no quiet reset,
idempotent retention, complete-group termination including ignored/obsolete members,
foreign owner safety, already-deleting Pods, recovery, stale preannotations, limit
and policy edits, revision change, teardown, optimistic-lock failures and stale
status preservation. Focused envtest covers CRD validation and the real API-server
retention/recovery/deletion lifecycle (without a kubelet, so no claim about actual
terminal-phase resource release or durable logs).

## Background and Motivation

The RBG project's original failure mode was incomplete handling of Pod abnormal
states (e.g., Evicted, UnexpectedAdmissionError, Failed). The recovery design below
addresses explicit signals while retaining the observation limits described here.

**Original root cause**: Failed Pods with fixed names blocked replacement through `hasOrphanPod`; this KEP introduced explicit Failed-pod cleanup. The implemented recovery now also supports durable group draining and opt-in startup/member-loss triggers, as described below.

**Historical Issues**:

1. **Failed/Evicted Pod does not trigger replacement**
   - RoleInstance Controller treats Failed pods as inactive (correct) but doesn't delete them
   - `hasOrphanPod` detects same-name pod still exists, blocking replacement creation
   - Failed Pods stay indefinitely, causing reduced replica count

2. **Comparison with Native K8s Controllers**:
   - **Deployment (ReplicaSet)**: Ignores Failed pods (random names), creates new pod with different name
   - **StatefulSet**: Deletes Failed pod (ordinal names), then creates replacement with same name
   - **RoleInstance**: Must delete Failed pod (fixed names like StatefulSet), then create replacement

3. **Disruption intent alone is not a failure signal**
   - A `DisruptionTarget` condition alone does not initiate recovery.
   - Failed phase is sufficient, regardless of the reason string; deletion is a separate best-effort signal only under the opt-in policy.

**Example**: Suppose a Pod is Evicted due to node resource shortage, its Phase becomes Failed. Without this fix, the Failed Pod stays indefinitely because `hasOrphanPod` blocks replacement creation. The service operates with reduced replicas until manual intervention.

## Goals

- Correctly detect and handle Failed/Evicted/UnexpectedAdmissionError Pod states
- Delete Failed pods through ordinary repair when no group recovery, backoff, drain, or budget suppression takes precedence
- Follow Kubernetes native StatefulSet pattern for fixed-name pod replacement
- Preserve the existing behavior of `RecreateRoleInstanceOnPodRestart` for compatibility
- Offer `RecreateRoleInstanceOnPodRestartFromCreation` as an opt-in policy that also covers pre-Ready failures and deletion of a desired member pod
- Reject the new policy for LeaderWorkerSet-backed roles until their adapter can implement its semantics
- Correctly detect Failed phase without requiring eviction-specific reason strings

## Non-Goals

- A live `Succeeded` Pod is normal completion, not a fault trigger. Under `RecreateRoleInstanceOnPodRestartFromCreation`, observed deletion is a separate signal and triggers regardless of phase, including `Succeeded`. With Pod `restartPolicy: Always`, kubelet normally assigns `Succeeded` only during termination.
- Do not introduce persistent membership history, a missing-slot trigger, a deletion-intent log, or a Pending recovery task
- Do not trigger on Pending, model loading, NotReady alone, or a NotReady timeout; do not force-delete Pods or fence nodes
- Do not inspect init-container or native-sidecar restarts; ordinary containers (including sidecars declared there) remain eligible
- Container restart handling for LWS-backed roles remains delegated to LWS
- Do not handle Unschedulable pods (PodScheduled=False) - these pods are still active per IsPodActive and rely on K8s scheduler retry
- Keep the new policy's additional triggers opt-in; existing `RecreateRoleInstanceOnPodRestart` workloads retain their current behavior

## Design Proposal

### User Stories

#### Story 1: Evicted Pod Automatic Replacement

> As an operator using ordinary member repair, when a Pod is evicted due to node resource shortage, I want RBG to detect its Failed phase and create a replacement without waiting for GC cleanup.

**Expected Behavior**:

- After Pod is evicted, Phase becomes Failed, status.reason="Evicted"
- RoleInstance Controller detects Pod entering inactive state
- For `None` (or an ineligible group fault), the Failed pod is deleted and replaced through ordinary reconciliation; eligible group faults instead follow the guarded, backoff/budget-governed rebuild path.
- Active replica count restored automatically

#### Story 2: Failed Pod Triggers Instance Recreation

> As an operator with `RecreateRoleInstanceOnPodRestart` policy, when a Pod enters Failed state, I want the entire RoleInstance to be recreated for consistency.

**Expected Behavior**:

- Pod enters Failed state (e.g., Evicted, Error)
- For an eligible, already observed instance, `shouldRecreateInstance` detects Failed pod with RecreateRoleInstanceOnPodRestart policy
- All pods in the affected Instance are deleted and recreated together
- Only the affected Instance is recreated; other Instances remain untouched

#### Story 3: RestartPolicy=None Creates Replacement Pod

> As an operator with `RestartPolicy=None`, when a Pod enters Failed state, I want a replacement Pod created to maintain the desired replica count.

**Expected Behavior**:

- Pod enters Failed state
- Failed pod is deleted on next reconcile
- Replacement pod with same name created on subsequent reconcile
- Active replica count returns to desired count

### Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Extended trigger conditions may cause unexpected recreation loops | Keep legacy Ready/recent-restart gating. The new policy requires explicit faults, not NotReady; both policies retain generation/revision and in-place guards. Backoff bounds attempt frequency, and `status.retiringPods` prevents overlapping rounds. |
| Frequent Failed Pod state transitions may increase controller load | Only react to Failed phase (terminal state), not intermediate transitions |
| Disruption intent can precede an actual failure | Do not use `DisruptionTarget` alone as a trigger; use Failed phase or, under the opt-in policy, observed desired-member deletion |

## Design Details

### Detection and Reconciliation Outline

The implementation uses the existing Pod lifecycle classification and
`shouldRecreateInstance` in `pkg/reconciler/roleinstance/sync/instance_scale.go`.
Failed phase is sufficient: no eviction-specific helper or reason-string matching
is required. A live Succeeded Pod is completion, not failure. Pod events enqueue
reconciliation; they are not a persistent membership history.

The following is **conceptual pseudocode**, not additional Go helper APIs:

```text
if a retiring-Pod UID ledger exists:
    resume UID-pinned deletion; block normal creation/update
    confirm every recorded old UID is absent through the API before clearing ledger
else:
    evaluate resolved policy and generation/revision/in-place/Ignore guards
    if an eligible explicit fault is present:
        enforce budget and remaining attempt-time backoff
        freshly validate instance identity/generation and owned member evidence
        if the observed deletion disappeared: warn RestartSignalDropped; do not accept
        otherwise persist ledger, count/time and budget accounting before deletion
    if no recovery, backoff, or exhaustion suppression takes precedence:
        perform ordinary Failed-Pod cleanup and missing-member repair
```

Only ordinary-container statuses are inspected for unexpected restarts; init
containers and native sidecars are excluded. The new policy additionally accepts
observed deletion of a currently desired member, regardless of Pod phase, but
cannot turn a missing slot into a deletion signal.

### RoleInstance Controller Changes

**File**: `pkg/reconciler/roleinstance/sync/instance_scale.go`

**Scope**: RoleInstance Controller handles Pod Failed → replacement Pod creation. This is inherent Pod lifecycle management.

#### Trigger Evaluation and Compatibility

`calculateDiffsWithExpectation` evaluates recovery before ordinary Failed-pod
cleanup and scaling. `shouldRecreateInstance` checks the resolved policy,
observed generation, matching current/update revisions, Pod in-place-update
state, and the effective `restart-trigger-policy` annotation. A recorded drain
is resumed before evaluating any new trigger or backoff.

- `None`: no group-rebuild trigger. Failed, non-terminating Pods are removed so
  normal reconciliation can replace them individually.
- `RecreateRoleInstanceOnPodRestart`: retain the existing Ready or recent-restart
  eligibility gate and Failed/non-terminating or unexpected ordinary-container
  restart triggers. Deletion does not initiate recovery.
- `RecreateRoleInstanceOnPodRestartFromCreation`: no Ready gate. The same explicit fault
  signals and observed deletion of a desired member can initiate a round, even
  during startup. Slots no longer desired after scale-in are excluded. Observed
  deletion is a signal regardless of phase, including `Succeeded`; a live
  `Succeeded` member is not a fault.

Init containers and native sidecars do not trigger recovery or consume the
ordinary-container in-place allowance. Ordinary containers retain the
ImageID-based +1 allowance after an in-place image change; same-digest or
ContainerID-based detection is not added. Only the exact annotation value
`"Ignore"` excludes a Pod as an initiator; absent, `"Inherit"`, and unrecognised
values inherit the policy. This is not a deletion exemption: another member's
fault still retires all selected owned members, including `Ignore` Pods.

#### Durable Acceptance and Drain

An accepted rebuild persists `status.retiringPods`, a list of
`RoleInstancePodReference` (`name`, `uid`) values, with `restartCount` and
`lastRestartTime` before any Delete. Acceptance revalidates the instance and its
owned member Pods against the API, preventing a stale cache snapshot from
starting an obsolete round. Old in-place-update baselines are cleared when the
round is recorded, so replacement Pods do not inherit old restart allowances.

Subsequent reconciles and controller restarts resume the recorded UID list
without charging another attempt. A recorded drain takes precedence even if
policy, generation, or revision changes. Normal creation/update is blocked
until **all recorded old UIDs are absent from the API**, not merely until Delete
is accepted. The controller uses the informer view during draining and confirms
completion against the API before releasing creation. Delete requests use UID
preconditions, not stale resourceVersion preconditions; same-name replacements
are not deleted on behalf of old UIDs. Pod events drive progress, with a
30-second safety requeue while draining.

`Restarting` is projected from the recorded list, not an independent gate or a
Ready-based completion latch. During drain, `RoleInstanceReady=False` has reason
`Restarting` and message `Removing the previous member Pods`. Once all old UIDs
are absent, the list and `Restarting` clear without waiting for replacements to
become Ready. The RoleInstance object, UID, and revision history remain intact.
Under the new policy, replacement startup faults can start successive rounds
without any Ready transition. Legacy trigger eligibility remains unchanged.
After five minutes of an active drain, emit a `RetirementStalled` Warning event on
the RoleInstance to prompt inspection of remaining old Pods, finalizers, and node
health. The warning is diagnostic only: never force-delete Pods or remove their
finalizers to break the barrier.

#### Backoff and Observation Limits

The first accepted attempt has no prior delay. Later eligible faults wait only
for the remainder of the interval from the preceding `lastRestartTime`:

`interval = min(baseDelaySeconds * 2^(restartCount-1), maxDelaySeconds)`

`remaining = max(0, lastRestartTime + interval - now)`

For example, base=10, count=1, and a previous attempt at t=0 means a fault at t=3
waits 7 seconds; one at t=12 waits no more. Draining and initialization consume
the interval. On the next accepted attempt more than
`max(maxDelaySeconds * 2, 10 minutes)` after the last attempt began, the count is
reset before charging the new round (count becomes 1). This measures elapsed
time, not continuous Ready service; long unsuccessful startup can also reset it.

During backoff, visible eligible faults are re-evaluated without cleaning Failed
Pods or individually replacing members. A policy or actual revision change can
release that eligibility, but cannot cancel a recorded drain. A generation-only
spec change that preserves policy and Pod-template revision can first be
acknowledged in `observedGeneration`, then still retain the remaining failure
backoff; a generation bump is not a guaranteed immediate bypass.

Deletion recovery is best-effort: the controller must observe a desired member
with `deletionTimestamp` set, then confirm the owned signal through fresh API
validation before accepting a round. If the observed deleting member disappears
before validation, emit `RestartSignalDropped` as a Warning on the RoleInstance;
do not persist a recovery task, and ordinary reconciliation may repair only the
missing member. Deletions missed during backoff or a controller outage, or removal
without an observable terminating window (for example force deletion/PodGC), can
also fall back to ordinary repair. Never-observed signals do not imply a warning.
Missing-member triggers and a NotReady timeout are deferred.

Under the new policy a later explicit Failed phase or unexpected ordinary-container
restart can rebuild the current group. A runtime failure timeout that exits the
container, or a configured startup/liveness probe that kills it, can provide this
later signal. A replacement that only hangs or stays NotReady cannot itself
trigger recovery; without such a timeout or probe, no later signal is guaranteed.
Readiness probes alone do not restart containers.

Component `startAfter` dependencies still use PodReady; mutually initializing
ranks must not wait on each other's Ready. Status writes, Pod writes, and
creation remain separate API operations, not a cross-resource transaction with
external/racing creators. Finalizer-held old Pods block replacement indefinitely.
For an unreachable node, eviction normally starts after not-ready/unreachable
tolerations (typically 300 seconds), but deletion can remain stuck. Old UID
absence is not proof that a process on a partitioned node has exited. RBG does
not force-delete or fence; lower tolerations improve detection, not necessarily
safe cleanup.

### RestartPolicy Handling Matrix

#### Pod Failed

| RestartPolicy | Pod Failed Behavior |
|--------------|---------------------|
| `None` | Delete Failed pod → next reconcile creates replacement (pod-level) |
| `RecreateRoleInstanceOnPodRestart` | Eligible Failed-pod signal → persist and drain all old member UIDs → create replacements; retain Ready/recent-restart and generation/revision guards |
| `RecreateRoleInstanceOnPodRestartFromCreation` | Eligible Failed-pod signal can rebuild all member Pods before first Ready; observed desired-member deletion is an additional trigger, after generation/revision guards |

#### Container Restart

| RestartPolicy | Container Restart Behavior |
|--------------|---------------------------|
| `None` | Do nothing - let container restart naturally |
| `RecreateRoleInstanceOnPodRestart` | RoleInstance controller handles unexpected ordinary-container restart with its Ready/recent-restart gate; LWS-backed roles delegate to LWS v0.7 |
| `RecreateRoleInstanceOnPodRestartFromCreation` | RoleInstance controller rebuilds on unexpected ordinary-container restart, including before first Ready; unsupported for LeaderWorkerSet |

#### Summary

| Scenario | Handler | Action |
|----------|---------|--------|
| Pod Failed + `None` | RoleInstance Controller | Delete Failed pod, create replacement |
| Pod Failed + `RecreateRoleInstanceOnPodRestart` | RoleInstance Controller | Recreate entire Instance, preserving existing guards |
| Pod Failed + `RecreateRoleInstanceOnPodRestartFromCreation` | RoleInstance Controller | Recreate entire Instance, including before first Ready |
| Observed desired-member deletion + `RecreateRoleInstanceOnPodRestartFromCreation` | RoleInstance Controller | Rebuild affected instance's Pods after generation/revision guards; missed termination falls back to individual repair |
| Container restart + `RecreateRoleInstanceOnPodRestartFromCreation` | RoleInstance Controller (RoleInstanceSet only) | Recreate affected Instance, including before first Ready |
| Container restart + `None` | None (K8s default) | Let container restart |
| Container restart + `RecreateRoleInstanceOnPodRestart` | RoleInstance Controller (LWS Controller for LWS-backed roles) | Rebuild member Pods with legacy Ready/recent-restart eligibility |

### Test Plan

[X] I/we understand that owners of the involved components may require updates to existing tests before implementing this enhancement.

#### Unit Tests

| Test Name | File | Test Content |
|-----------|------|--------------|
| `TestShouldRecreateInstance` | `pkg/reconciler/roleinstance/sync/instance_scale_test.go` | Verify legacy Ready/recent-restart gate and no deletion trigger; new-policy startup faults, desired-member observed deletion, live Succeeded exclusion, generation/revision/in-place guards, ordinary containers only, and Ignore initiation filtering |
| `TestRestartAcceptance*` / `TestRestartRecordBeforeDelete` | `pkg/reconciler/roleinstance/sync/instance_scale_test.go` | Verify retiring UIDs and count/time persist before Delete, stale-cache rejection, and resume without re-charging |
| `TestRestartFinalizerCountsOnceAndBlocksReplacement` / `TestRestartBarrierUsesOldUIDNotReusedName` | `pkg/reconciler/roleinstance/sync/instance_scale_test.go` | Verify durable drain count, finalizer barrier, and same-name/different-UID safety |
| `TestFailedPodDeletion` | `pkg/reconciler/roleinstance/sync/instance_scale_test.go` | Verify Failed pods without DeletionTimestamp are deleted, Succeeded pods and terminating pods are skipped |
| `TestPodRunningAndReady` | `pkg/utils/pod_utils_test.go` | Verify pod running and ready detection |
| `TestPodDeleted` | `pkg/utils/pod_utils_test.go` | Verify pod deletion detection |

#### Durable Recovery Coverage

Unit and envtest coverage must verify controller-restart recovery of a recorded
drain, finalizer-held old UIDs blocking every replacement, same-name/different-UID
safety, repeated startup-failure rounds without Ready under the new policy, and
`Restarting` clearing at drain completion rather than at Ready. Verify that
`Ignore` members cannot initiate a round but are retired in another member's
round, and that backoff/stability reset uses attempt time, not Ready duration.
The envtest recovery suite is in
`test/envtest/testcase/restart_policy/durable_recovery_test.go`. These tests use
explicit simulated faults; they do not establish real distributed-framework
startup or serving correctness.

#### E2E Tests

**Test File Location**: `test/e2e/testcase/v1alpha2/inactive_pod.go`

**Test Environment**: a Kubernetes cluster with a real kubelet (for example Kind)
with the controller and current CRDs installed. Compilation alone does not verify
kubelet behavior, controller convergence, or distributed-framework correctness.

These specs do not synthesize Pod phases through status patches. Existing helpers
and role wrappers create native RoleInstanceSet workloads. `buildFailingPodTemplate`
runs a container with `restartPolicy: Never`; `failPod` requests a non-zero exit and
watches for the kubelet-owned Failed phase before the controller removes the Pod.
`utils.RestartNginxContainer` induces an actual ordinary-container restart.

- **Ordinary Failed-Pod repair / None**: start Ready standalone replicas, induce
  the real terminal failure, and verify that only the failed member's UID changes.
- **Unexpected ordinary-container restart**: start Ready multi-member instances
  with `RecreateRoleInstanceOnPodRestartFromCreation`, induce a real restart, and verify that
  all affected members get new UIDs while other instances remain unchanged.
- **Observed member deletion**: start a Ready, two-member native RoleInstance with
  the opt-in policy. Record all Pod UIDs and the RoleInstance UID. Attach a
  test-only finalizer to one member and issue a real UID-preconditioned Delete.
  Wait until its deletion is observed and the durable ledger contains every old
  member UID with `restartCount == 1`; only then release the finalizer. Verify
  every old member UID rotates, the RoleInstance UID is unchanged, the ledger
  clears, `restartCount` remains exactly one, and the instance returns to Ready.
  Always remove the test finalizer during cleanup, including assertion failures,
  so the observation barrier cannot strand a Pod or block namespace teardown.
- **Ignore and recovery stability**: use real ordinary-container restarts to
  verify initiation filtering, complete-group retirement of ignored siblings,
  and stable member UIDs after recovery.

The deletion spec deliberately holds the terminating window: a deletion that
vanishes before fresh API validation is outside the guaranteed trigger boundary.
Unit/envtest coverage, not fake kubelet e2e phase mutations, exercises dropped
signals, drain resumption, and controller race paths.

---

## Implementation History

- 2026-05-08: Initial implementation completed
  - Added pod inactive detection functions in `pkg/utils/pod_utils.go`
  - Fixed RoleInstance Controller in `pkg/reconciler/roleinstance/sync/instance_scale.go`
  - Added unit tests
  - Added E2E test cases in `test/e2e/testcase/v1alpha2/inactive_pod.go`

- 2026-05-09: Refactored to properly separate controller responsibilities
  - Updated KEP documentation with two-level handling approach
  - RoleInstance Controller handles both Level 1 (delete Failed pod) and Level 2 (recreate Instance)
  - Historical container-restart delegation applied to the LWS path; current RoleInstanceSet handling is described above.

- 2026-05-18: Updated after upstream #340 removed Pod Controller
  - Removed Pod Controller references (deprecated by upstream #340)
  - Removed `RecreateRBGOnPodRestart` references (deprecated by upstream #340)
  - Simplified handling matrix to two policies: None and RecreateRoleInstanceOnPodRestart
  - Updated test plan to reference actual existing tests

- Current: Opt-in startup/member-loss recovery and durable member drain (Refs #542)
  - Added `RecreateRoleInstanceOnPodRestartFromCreation` for RoleInstanceSet-backed roles; legacy `RecreateRoleInstanceOnPodRestart` retains Ready/recent-restart gating and no deletion trigger.
  - Persist `status.retiringPods` name/UID references with count/time before Delete, resume across controller restarts, and block creation until every recorded old UID is gone.
  - Project `Restarting` and readiness from the drain; do not wait for replacement Ready to complete a round. The new policy can retry repeated startup faults.
  - Observed deletion triggers only the new policy, regardless of Pod phase; missed termination is ordinary member repair. Init-container/native-sidecar restarts are excluded, and `Ignore` filters initiation only.
  - Fresh validation can drop an observed deletion (`RestartSignalDropped`); missing-member/NotReady triggers remain deferred. `RetirementStalled` warns after five minutes of active drain without forcing cleanup.
  - A real-Delete e2e uses a cleanup-protected finalizer to keep the deletion observable until durable acceptance; compile-only validation does not establish live-cluster results.

## Implementation File Map

This map describes the current recovery path, not a list of newly introduced
helper APIs:

- `pkg/reconciler/roleinstance/sync/instance_scale.go`: trigger eligibility,
  attempt-time backoff, durable acceptance/drain and ordinary Failed-Pod cleanup.
- `pkg/reconciler/roleinstance/sync/restart_budget.go`: persistent budget,
  exhaustion and retained-Pod recovery/cleanup.
- `pkg/reconciler/roleinstance/instance_reconciler.go` and
  `pkg/reconciler/roleinstance/instance_status.go`: reconciliation and
  recovery status projection/preservation.
- `api/workloads/v1alpha2/roleinstance_types.go`: restart tracking and retiring-Pod
  reference API.
- `pkg/reconciler/roleinstance/sync/*_test.go`,
  `test/envtest/testcase/restart_policy/`, and
  `test/e2e/testcase/v1alpha2/inactive_pod.go`: unit, API-server, and real-kubelet
  recovery coverage respectively.

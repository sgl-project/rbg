# KEP-173: Inactive Pod Handling Enhancement

<!-- toc -->
- [Background and Motivation](#background-and-motivation)
- [Goals](#goals)
- [Non-Goals](#non-goals)
- [Design Proposal](#design-proposal)
  - [User Stories](#user-stories)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [New Detection Functions](#new-detection-functions)
  - [RoleInstance Controller Changes](#roleinstance-controller-changes)
  - [RestartPolicy Handling Matrix](#restartpolicy-handling-matrix)
  - [Test Plan](#test-plan)
<!-- /toc -->

## Two Levels of Failed Pod Handling

This KEP addresses two distinct levels of handling when a Pod enters Failed state (Evicted, UnexpectedAdmissionError, etc.). Understanding this separation is critical for correct implementation.

### Level 1: Delete Failed Pod (All RestartPolicies)

| Aspect | Description |
|--------|-------------|
| **Trigger** | Pod enters Failed state (Phase=Failed) |
| **Handler** | RoleInstance Controller (`instance_scale.go`) |
| **Behavior** | Delete Failed pod, then create replacement on next reconcile |
| **Why delete first** | `hasOrphanPod` blocks creation when same-name pod still exists |
| **Key point** | This follows native K8s StatefulSet behavior - Failed Pods with fixed names must be deleted before replacement |

### Level 2: Recreate Entire Instance (RecreateRoleInstanceOnPodRestart only)

| Aspect | Description |
|--------|-------------|
| **Trigger** | An armed `RecreateRoleInstanceOnPodRestart` policy detects Failed phase, a desired member observed deleting, or an unexpected ordinary-container restart |
| **Handler** | RoleInstance Controller (`instance_scale.go` → `shouldRecreateInstance`) |
| **Behavior** | Delete ALL pods (active + inactive) → recreate entire Instance |
| **Key point** | Maintains Instance-level consistency; only the affected Instance is recreated |

### Container Restart Handling

| Aspect | Description |
|--------|-------------|
| **Trigger** | Container inside Pod restarts (RestartCount increases) |
| **Handler** | Native RoleInstance controller; LWS controller for LeaderWorkerSet-backed roles |
| **restartPolicy involved** | `RecreateRoleInstanceOnPodRestart`: recreate the affected native instance or LWS group |
| **Key point** | Native RoleInstance handles ordinary-container restarts with the activation, generation/revision and in-place-update guards below. The LWS backend delegates to LWS v0.7's ungated `RecreateGroupOnPodRestart`; it does not use the native Ready gate or `activateAfter`. |

### Why Two Levels

The RoleInstance Controller handles Failed pods in a two-level approach:

1. **Level 1 (pod-level replacement)**: For `RestartPolicy=None`, a Failed pod is deleted, then normal reconciliation detects the missing pod and creates a replacement. This is analogous to how StatefulSet handles Failed pods with ordinal identity.

2. **Level 2 (Instance-level recreation)**: For an armed `RestartPolicy=RecreateRoleInstanceOnPodRestart`, a Failed pod can trigger recreation of the entire RoleInstance (all pods deleted and recreated together), subject to the guards below. This ensures Instance-level consistency for tightly-coupled multi-pod workloads.

## Policy Compatibility

There is one recreate policy, `RecreateRoleInstanceOnPodRestart`, and two orthogonal
settings describe it:

- `type` says *what* recreates the instance.
- `restartPolicyConfig.activateAfter` says *when* those triggers are armed.

The triggers are unexpected container restarts, Failed Pods, and loss of a desired member
Pod observed while Terminating. Member loss is part of the default policy: it is not a
separate opt-in, because a lost member that is silently replaced on its own is the failure
mode #542 reports, and LeaderWorkerSet already behaves this way under the same policy name.

`activateAfter` default is `Ready`:

- `Ready` — triggers are armed once the instance has been Ready at its current generation.
  Creation and scale-up therefore keep the protection from #348/#349, and any spec change
  (which bumps the generation) re-arms that protection by construction.
- `Created` — triggers are armed as soon as the instance exists, which also covers
  failures that happen before the instance first becomes Ready. The `Restarting`
  guard is scoped to one recreation wave: it releases once the pre-restart Pods are
  gone, so repeated pre-Ready recreations are bounded by the exponential backoff
  ([#563](https://github.com/sgl-project/rbg/issues/563)).

The stability fact is persisted as `status.lastReadyGeneration`, the `metadata.generation`
at which the instance was last Ready. Gating on the live `RoleInstanceReady` condition
instead would be unsound: status is written after scaling, so a fault flips the condition
to False before the restart signal becomes visible, and the policy would never fire again.

A member that disappears without ever being observed as Terminating is deferred to
[#562](https://github.com/sgl-project/rbg/issues/562); NotReady alone is not a trigger and a
NotReady timeout is deferred to [#380](https://github.com/sgl-project/rbg/issues/380).

Compatibility notes:

- The default policy intentionally gains the readiness latch and Terminating-member trigger;
  existing workloads do **not** retain identical runtime behavior. `lastReadyGeneration`
  starts at 0 and is populated on the next observed Ready. A Ready instance with an already
  Terminating desired member at upgrade can be rebuilt on the first reconcile. An existing
  instance stuck NotReady without a latch still needs manual deletion of the **RoleInstance**,
  not just one Pod.
- `activateAfter` has no CRD default and uses `omitempty`. Its getter resolves `Ready`, and
  the adapter writes it to the RoleInstance template only when explicitly set. The latch
  is status-only and not hashed. This avoids upgrade-only template/default materialization
  and rollout; it does not promise unchanged runtime behavior.
- Explicitly setting or changing `activateAfter` changes the serialized RoleInstance
  template and can roll the role, even when setting `Ready` where previously omitted.
  `RecreatePod` can rebuild every RoleInstance and all its Pods; `InPlaceIfPossible` updates
  RoleInstance specs in place without Pod restarts for a policy-only change.
- Both served APIs accept the existing policy, but only v1alpha2 can express `activateAfter`.
  A v1alpha1 GET omits it from the response without changing persisted configuration or
  running behavior. A v1alpha1 read-modify-write drops it and resolves to `Ready`, just as it
  already drops backoff delays. An older writer, including a rolled-back controller, may
  drop the field when writing an object/template; the rollback alone does not rewrite stored
  objects. Losing the serialized field on a write can roll the role again, rebuilding all
  Pods with `RecreatePod`. See the [upgrade review](https://github.com/sgl-project/rbg/pull/559#issuecomment-6769631451).
- LeaderWorkerSet-backed roles keep the policy they had. They are not offered
  `activateAfter` because their behavior is already ungated (LWS's
  `RecreateGroupOnPodRestart` fires on `ContainerRestarted(pod) || PodDeleted(pod)`
  without a start or Ready gate), and the vendored LWS v0.7 adapter exposes no field to
  express the distinction.

## Background and Motivation

The RBG project has deficiencies in handling Pod abnormal states (e.g., Evicted, UnexpectedAdmissionError, Failed).

**Original root cause**: Native Kubernetes controllers treat Failed/Succeeded Pods as inactive and create replacements to maintain replica count. RBG's original implementation lacked handling for Pods that directly entered Failed state; same-name inactive Pods blocked replacement. The design below adds Failed-Pod cleanup and extends whole-instance recovery.

**Specific Issues**:

1. **Failed/Evicted Pod does not trigger replacement**
   - RoleInstance Controller treats Failed pods as inactive (correct) but doesn't delete them
   - `hasOrphanPod` detects same-name pod still exists, blocking replacement creation
   - Failed Pods stay indefinitely, causing reduced replica count

2. **Comparison with Native K8s Controllers**:
   - **Deployment (ReplicaSet)**: Ignores Failed pods (random names), creates new pod with different name
   - **StatefulSet**: Deletes Failed pod (ordinal names), then creates replacement with same name
   - **RoleInstance**: Must delete Failed pod (fixed names like StatefulSet), then create replacement

3. **DisruptionTarget handling limited to terminal pods**
   - Kubernetes 1.22+ introduced `DisruptionTarget` condition
   - RBG handles DisruptionTarget condition on terminal pods (Phase=Failed) but does not preemptively act before eviction completes
   - This matches K8s native controller behavior - controllers react after pod becomes terminal

**Example**: Suppose a Pod is Evicted due to node resource shortage, its Phase becomes Failed. Without this fix, the Failed Pod stays indefinitely because `hasOrphanPod` blocks replacement creation. The service operates with reduced replicas until manual intervention.

## Goals

- Correctly detect and handle Failed/Evicted/UnexpectedAdmissionError Pod states
- Delete Failed pods immediately so replacements can be created, without waiting for GC
- Follow Kubernetes native StatefulSet pattern for fixed-name pod replacement
- Retain the existing policy enum and creation/scale-up protection with `activateAfter: Ready`
- Add the desired-member Terminating trigger and persisted readiness latch to `RecreateRoleInstanceOnPodRestart` to recover from #542 when member termination is observed
- Offer `restartPolicyConfig.activateAfter: Ready | Created` so pre-Ready recovery is opt-in without adding a second policy type
- Support DisruptionTarget condition detection (K8s 1.22+)

## Non-Goals

- Succeeded phase alone does not trigger recreation (normal completion). A desired member observed deleting can still trigger recreation regardless of phase, including Succeeded.
- Do not add Unschedulable (PodScheduled=False) as a trigger; these Pods rely on K8s scheduler retry
- Do not introduce new `RestartPolicy` types. New behavior is expressed as a field on
  `RestartPolicyConfig` (`activateAfter`), so the policy enum stays two-valued and a role
  can be described on independent axes instead of by growing the enum
- Keep pre-Ready arming opt-in through `activateAfter: Created`; the default `Ready` keeps
  today's protection for creation and scale-up

## Design Proposal

### User Stories

#### Story 1: Evicted Pod Automatic Replacement

> As an operator, when a Pod is evicted due to node resource shortage, I want RBG to immediately detect it and create a replacement Pod, without waiting for GC cleanup.

**Expected Behavior**:

- After Pod is evicted, Phase becomes Failed, status.reason="Evicted"
- RoleInstance Controller detects Pod entering inactive state
- Failed pod is deleted, replacement pod created on next reconcile
- Active replica count restored automatically

#### Story 2: Failed Pod Triggers Instance Recreation

> As an operator with `RecreateRoleInstanceOnPodRestart` policy, when a Pod enters Failed state, I want the entire RoleInstance to be recreated for consistency.

**Expected Behavior**:

- Pod enters Failed state (e.g., Evicted, Error)
- `shouldRecreateInstance` detects Failed pod with RecreateRoleInstanceOnPodRestart policy
- All pods in the affected Instance are deleted and recreated together
- Only the affected Instance is recreated; other Instances remain untouched

#### Story 3: RestartPolicy=None Creates Replacement Pod

> As an operator with explicit RestartPolicy `None`, when a Pod enters Failed state, I want a replacement Pod created to maintain the desired replica count.

**Expected Behavior**:

- Pod enters Failed state
- Failed pod is deleted on next reconcile
- Replacement pod with same name created on subsequent reconcile
- Active replica count returns to desired count

### Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Extended trigger conditions may cause unexpected recreation loops | Default `Ready` activation uses current Ready, the generation-scoped readiness latch, or the existing recent-restart bypass; retain generation/revision guards and the `Restarting` guard |
| Frequent Failed Pod state transitions may increase controller load | Trigger on Failed phase or observed desired-member deletion, not NotReady or other intermediate conditions alone |
| DisruptionTarget condition depends on K8s 1.22+ | Also support traditional detection method (status.reason) to ensure backward compatibility |

## Design Details

### New Detection Functions

**Principle**: Prioritize reusing native Kubernetes functions, avoid redefining.

Native K8s functions available (can be used directly):

| Native Function | Location | Purpose |
|----------------|----------|---------|
| `kubecontroller.IsPodActive(pod)` | `vendor/k8s.io/kubernetes/pkg/controller/controller_utils.go` | Phase != Succeeded/Failed && DeletionTimestamp == nil |
| `podutil.IsPodTerminal(pod)` | `vendor/k8s.io/kubernetes/pkg/api/v1/pod/util.go` | Phase == Failed \|\| Phase == Succeeded |
| `kubecontroller.IsPodTerminating(pod)` | `vendor/k8s.io/kubernetes/pkg/controller/controller_utils.go` | !IsPodTerminal && DeletionTimestamp != nil |

**Note**: The project already uses `kubecontroller.IsPodActive` in `pkg/reconciler/roleinstance/utils/instance_utils.go`.

Add the following **helper functions** in `pkg/utils/pod_utils.go` (based on native functions and constants):

#### PodBecameInactive (State Transition Detection)

```go
import (
    kubecontroller "k8s.io/kubernetes/pkg/controller"
    podutil "k8s.io/kubernetes/pkg/api/v1/pod"
)

// PodBecameInactive checks if a Pod transitioned from active to inactive state.
// Used in predicates to capture state transition events.
func PodBecameInactive(oldPod, newPod *corev1.Pod) bool {
    if oldPod == nil || newPod == nil {
        return false
    }
    wasActive := kubecontroller.IsPodActive(oldPod)
    nowInactive := !kubecontroller.IsPodActive(newPod)
    return wasActive && nowInactive
}
```

#### IsPodEvicted

```go
// IsPodEvicted checks if a Pod was evicted due to resource shortage.
// Evicted Pod has Phase=Failed, status.reason="Evicted".
// Also supports DisruptionTarget condition (K8s 1.22+).
func IsPodEvicted(pod *corev1.Pod) bool {
    if pod == nil || !podutil.IsPodTerminal(pod) {
        return false
    }
    if pod.Status.Phase != corev1.PodFailed {
        return false
    }
    // Check DisruptionTarget condition (K8s 1.22+)
    // Native reasons: PreemptionByScheduler, TerminationByKubelet
    for _, cond := range pod.Status.Conditions {
        if cond.Type == corev1.DisruptionTarget {
            return true
        }
    }
    // Traditional detection: status.reason = "Evicted"
    return pod.Status.Reason == "Evicted"
}
```

#### IsPodUnexpectedAdmissionError

```go
// IsPodUnexpectedAdmissionError checks if a Pod failed due to admission issues.
func IsPodUnexpectedAdmissionError(pod *corev1.Pod) bool {
    if pod == nil || pod.Status.Phase != corev1.PodFailed {
        return false
    }
    return pod.Status.Reason == "UnexpectedAdmissionError"
}
```

#### GetPodInactiveReason

```go
// GetPodInactiveReason returns the specific reason for Pod being inactive.
// Based on podutil.IsPodTerminal and native constants.
func GetPodInactiveReason(pod *corev1.Pod) string {
    if pod == nil {
        return "PodNotFound"
    }
    if pod.DeletionTimestamp != nil {
        if podutil.IsPodTerminal(pod) {
            return "PodTerminatingTerminal"
        }
        return "PodTerminating"
    }
    if pod.Status.Phase == corev1.PodSucceeded {
        return "PodSucceeded"
    }
    if pod.Status.Phase == corev1.PodFailed {
        if IsPodEvicted(pod) {
            return "PodEvicted"
        }
        if IsPodUnexpectedAdmissionError(pod) {
            return "UnexpectedAdmissionError"
        }
        return "PodFailed"
    }
    return "Unknown"
}
```

### RoleInstance Controller Changes

**File**: `pkg/reconciler/roleinstance/sync/instance_scale.go`

**Scope**: Native RoleInstance Controller handles Pod Failed → replacement and the restart-policy triggers described below. LeaderWorkerSet-backed roles delegate their Pod lifecycle to LWS.

#### Understanding shouldRecreateInstance

The `shouldRecreateInstance` function exists to handle a special case:

**When an armed `RecreateRoleInstanceOnPodRestart` policy detects a failure**:

- Instead of just replacing one Pod, recreate the entire Instance
- Triggers include Failed phase, desired-member deletion and unexpected ordinary-container restarts
- This maintains Instance-level consistency on Pod failures

**When restartPolicy is `None`**:

- Pod Failed → delete Failed pod → next reconcile creates replacement Pod
- This pod-level replacement follows StatefulSet behavior

#### Conceptual Outline of shouldRecreateInstance

This is an outline, not replacement source code. The current implementation lives in
[`instance_scale.go`](../../pkg/reconciler/roleinstance/sync/instance_scale.go)
(`restartTriggersArmed`, `shouldRecreateInstance` and `shouldRecreateInstanceGuarded`):

1. Require the recreate policy, observed generation and converged current/update revisions.
2. For default `Ready`, arm on current Ready, `lastReadyGeneration == generation`, or the
   existing recent-restart bypass. `Created` bypasses the readiness gate, not the generation
   or rollout guards.
3. Skip initiation from Pods whose restart-trigger-policy annotation is exactly `Ignore`.
   A sibling's trigger still rebuilds all Pods, including ignored ones.
4. Check desired-member deletion (`deletionTimestamp`, irrespective of phase) and Failed
   phase before any in-place-update exemption. Scale-in ordinals are not desired members.
   Succeeded phase alone is not a trigger.
5. Skip container-restart detection for a Pod with `InPlaceUpdateReady=False`. For other
   Pods, check ordinary-container restarts against recorded in-place-update baselines.
   Init-container and native-sidecar restarts do not count.
6. The `Restarting` guard is scoped to one recreation wave: it blocks only while the
   pre-restart Pods are still being deleted, including with `Created`. Once they are all
   gone the wave is complete, the guard is released and a new failure may start another
   recreation, bounded by the existing backoff
   ([#563](https://github.com/sgl-project/rbg/issues/563)).

#### How Pod Failed → Replacement Pod Works (Two Levels)

The `calculateDiffsWithExpectation` function handles Pod Failed in two levels:

##### Level 1: Delete Failed Pods (applies to ALL RestartPolicies)

Failed pods must be deleted first because they block replacement creation (`hasOrphanPod` detects the same-name pod still exists):

```go
// Delete inactive (Failed) pods so that replacements can be created on next reconcile.
for _, p := range inactivePods {
    if p.Status.Phase == v1.PodFailed && p.DeletionTimestamp == nil {
        toDeletePods = append(toDeletePods, p)
    }
}
```

##### Level 2: Instance Recreation (only RecreateRoleInstanceOnPodRestart)

When `shouldRecreateInstance` returns true, ALL pods (active + inactive) are deleted and recreated:

```go
allPods := make([]*v1.Pod, 0, len(pods)+len(inactivePods))
allPods = append(allPods, pods...)
allPods = append(allPods, inactivePods...)
if shouldRecreateInstance(updateInstance, allPods) {
    return &expectationDiff{toDeleteNum: len(allPods), toDeletePod: allPods}, nil
}
```

**Scale Function Ordering**: Delete is checked before Create to ensure Failed pods are removed before attempting replacement (which would be blocked by `hasOrphanPod`). The snippets in this subsection illustrate ordering only; consult the current source for signatures, backoff and guards. Already-Terminating Pods and `NotFound` delete responses count as **handled**, blocking the Update path and preserving restart bookkeeping, but do not emit a false `SuccessfulDelete` event.

```go
func (c *realControl) Scale(...) (bool, error) {
    diffRes, err := c.calculateDiffsWithExpectation(...)
    if diffRes.toDeleteNum > 0 { return c.deletePods(...) }  // Delete first
    if diffRes.toScaleNum > 0 { return c.createPods(...) }   // Then create
    return false, nil
}
```

### RestartPolicy Handling Matrix

These rows describe native RoleInstance workloads. Whole-instance actions are subject to
activation, generation/revision, backoff and `Restarting` guards described above.

| Scenario | `None` | `RecreateRoleInstanceOnPodRestart` |
|----------|--------|----------------------------------|
| Failed Pod | Delete Failed Pod, then replace individually | Recreate the affected whole Instance when armed |
| Desired member observed Terminating (any phase) | Normal individual Pod reconciliation | Recreate the affected whole Instance when armed |
| Unexpected ordinary-container restart | Let the container restart naturally | Recreate the affected whole Instance when armed; apply the Pod-level in-place-update exemption and baselines |
| Before first Ready | Normal Pod reconciliation | Default `Ready` is not armed; `Created` permits repeated whole-instance recreations, bounded by backoff once the prior wave's pre-restart Pods are gone |
| Succeeded phase alone, Pending or NotReady alone | No restart-policy action | No whole-instance trigger |
| Pod annotated with exact `Ignore` | Normal Pod reconciliation | Cannot initiate recreation, but is rebuilt if a sibling triggers it |

LeaderWorkerSet-backed roles instead use LWS v0.7's ungated `RecreateGroupOnPodRestart`
(`ContainerRestarted` or `PodDeleted`); `activateAfter` does not change that backend.

### Test Plan

[X] I/we understand that owners of the involved components may require updates to existing tests before implementing this enhancement.

#### Unit Tests

| Test Name | File | Test Content |
|-----------|------|--------------|
| `TestShouldRecreateInstance` | `pkg/reconciler/roleinstance/sync/instance_scale_test.go` | Verify Instance recreation logic: RecreateRoleInstanceOnPodRestart triggers on PodFailed, None does not, guards for Ready/Generation/DeletionTimestamp |
| `TestWasInstanceReady` | `pkg/reconciler/roleinstance/sync/instance_scale_test.go` | Verify wasInstanceReady helper checks condition correctly |
| `TestFailedPodDeletion` | `pkg/reconciler/roleinstance/sync/instance_scale_test.go` | Verify Failed pods without DeletionTimestamp are deleted, Succeeded pods and terminating pods are skipped |
| `TestPodRunningAndReady` | `pkg/utils/pod_utils_test.go` | Verify pod running and ready detection |
| `TestPodDeleted` | `pkg/utils/pod_utils_test.go` | Verify pod deletion detection |

#### E2E Tests

**Test File Location**: `test/e2e/testcase/v1alpha2/inactive_pod.go`

**Test Environment**: Kind cluster (existing CI workflow)

**Method to Simulate Pod Abnormal State**:

In a real K8s cluster, triggering real Pod Eviction is difficult, so use **manually modifying Pod status subresource** to simulate:

```go
// Simulate Evicted Pod
func SetPodEvicted(ctx context.Context, client client.Client, pod *corev1.Pod) error {
    pod.Status.Phase = corev1.PodFailed
    pod.Status.Reason = "Evicted"
    pod.Status.Message = "The node was low on resource: ephemeral-storage."
    return client.Status().Update(ctx, pod)
}
```

**Test Cases**:

##### Case 1: Evicted Pod Triggers Replacement Pod Creation

- Create RBG with standalone role (Deployment, Replicas=2)
- Wait for pods ready
- Simulate one pod Evicted (Phase=Failed, Reason="Evicted")
- Verify active pod count returns to 2 (replacement created)

##### Case 2: Failed Pod Triggers RoleInstance Recreation (RecreateRoleInstanceOnPodRestart)

- Create RBG with LeaderWorker role and `RestartPolicy=RecreateRoleInstanceOnPodRestart` (Replicas=2)
- Wait for pods ready, record initial UIDs
- Simulate one pod Failed
- Verify affected Instance's pods are all recreated (new UIDs, correct count)
- Verify other Instances are unaffected

##### Case 3: RestartPolicy=None Creates Replacement Pod

- Create RBG with standalone role (Deployment, Replicas=3)
- Wait for pods ready, record initial UIDs
- Simulate one pod Evicted
- Verify active pod count returns to 3
- Verify at least one pod has a new UID (replacement created)

#### Test Helper Functions

Located in `test/utils/utils.go`:

```go
// SetPodEvicted simulates Pod Evicted state
func SetPodEvicted(ctx context.Context, rclient client.Client, pod *corev1.Pod) error

// SetPodFailed simulates Pod Failed state
func SetPodFailed(ctx context.Context, rclient client.Client, pod *corev1.Pod) error

// GetActivePodCount gets active Pod count (using native IsPodActive)
func GetActivePodCount(ctx context.Context, rclient client.Client, namespace, rbgName string) (int, error)
```

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
  - Historically removed `ContainerRestarted` from `shouldRecreateInstance` in favor of LWS delegation; native RoleInstance now handles ordinary-container restarts itself as described above.

- 2026-05-18: Updated after upstream #340 removed Pod Controller
  - Removed Pod Controller references (deprecated by upstream #340)
  - Removed `RecreateRBGOnPodRestart` references (deprecated by upstream #340)
  - Simplified handling matrix to two policies: None and RecreateRoleInstanceOnPodRestart
  - Updated test plan to reference actual existing tests

## Key Files Changed

| File | Change Type |
|------|-------------|
| `pkg/utils/pod_utils.go` | Added/modified functions |
| `pkg/utils/pod_utils_test.go` | Added tests |
| `pkg/reconciler/roleinstance/sync/instance_scale.go` | Modified: two-level Failed pod handling |
| `pkg/reconciler/roleinstance/sync/instance_scale_test.go` | Added: TestShouldRecreateInstance, TestFailedPodDeletion |
| `pkg/reconciler/roleinstance/instance_reconciler.go` | Modified: pass inactivePods to Scale |
| `pkg/reconciler/roleinstance/sync/api.go` | Modified: Scale interface accepts inactivePods |
| `test/e2e/testcase/v1alpha2/inactive_pod.go` | Added E2E tests |
| `test/utils/utils.go` | Added helper functions |

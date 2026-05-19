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
| **Trigger** | Pod enters Failed state AND restartPolicy=`RecreateRoleInstanceOnPodRestart` |
| **Handler** | RoleInstance Controller (`instance_scale.go` → `shouldRecreateInstance`) |
| **Behavior** | Delete ALL pods (active + inactive) → recreate entire Instance |
| **Key point** | Maintains Instance-level consistency; only the affected Instance is recreated |

### Container Restart Handling

| Aspect | Description |
|--------|-------------|
| **Trigger** | Container inside Pod restarts (RestartCount increases) |
| **Handler** | Underlying workload controller (e.g. LWS controller) |
| **restartPolicy involved** | `RecreateRoleInstanceOnPodRestart`: LWS controller recreates RoleInstance |
| **Key point** | RBG controller does NOT handle container restart - delegated to workload controllers |

### Why Two Levels

The RoleInstance Controller handles Failed pods in a two-level approach:

1. **Level 1 (pod-level replacement)**: For `RestartPolicy=None`, a Failed pod is deleted, then normal reconciliation detects the missing pod and creates a replacement. This is analogous to how StatefulSet handles Failed pods with ordinal identity.

2. **Level 2 (Instance-level recreation)**: For `RestartPolicy=RecreateRoleInstanceOnPodRestart`, when a pod becomes Failed, the entire RoleInstance is recreated (all pods deleted and recreated together). This ensures Instance-level consistency for tightly-coupled multi-pod workloads.

## Background and Motivation

The RBG project has deficiencies in handling Pod abnormal states (e.g., Evicted, UnexpectedAdmissionError, Failed).

**Root Cause**: Native Kubernetes controllers treat Failed/Succeeded Pods as inactive and immediately create new Pods to maintain replica count. However, RBG's current implementation only triggers recreation when a Pod is deleted, lacking handling for Pods that directly enter the Failed state.

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
- Support two-level handling: pod-level replacement (None) and Instance-level recreation (RecreateRoleInstanceOnPodRestart)
- Support DisruptionTarget condition detection (K8s 1.22+)

## Non-Goals

- Do not introduce new RestartPolicy types
- Do not handle Pod Succeeded state (normal completion) - explicitly excluded from trigger conditions
- Do not handle Unschedulable pods (PodScheduled=False) - these pods are still active per IsPodActive and rely on K8s scheduler retry
- Do not handle container restart - delegated to underlying workload controllers (e.g. LWS)

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

> As an operator with default RestartPolicy (None), when a Pod enters Failed state, I want a replacement Pod created to maintain the desired replica count.

**Expected Behavior**:
- Pod enters Failed state
- Failed pod is deleted on next reconcile
- Replacement pod with same name created on subsequent reconcile
- Active replica count returns to desired count

### Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Extended trigger conditions may cause unexpected recreation loops | Use `wasInstanceReady` check to avoid triggering during initial creation; require `Generation == ObservedGeneration` to avoid triggering during spec changes |
| Frequent Failed Pod state transitions may increase controller load | Only react to Failed phase (terminal state), not intermediate transitions |
| DisruptionTarget condition depends on K8s 1.22+ | Also support traditional detection method (status.reason) to ensure backward compatibility |

## Design Details

### New Detection Functions

**Principle**: Prioritize reusing native Kubernetes functions, avoid redefining.

Native K8s functions available (can be used directly):

| Native Function | Location | Purpose |
|----------------|----------|---------|
| `kubecontroller.IsPodActive(pod)` | `vendor/k8s.io/kubernetes/pkg/controller/controller_utils.go` | Phase != Succeeded/Failed && DeletionTimestamp == nil |
| `podutil.IsPodTerminal(pod)` | `vendor/k8s.io/kubernetes/pkg/api/v1/pod/util.go` | Phase == Failed || Phase == Succeeded |
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

**Scope**: RoleInstance Controller handles Pod Failed → replacement Pod creation. This is inherent Pod lifecycle management.

#### Understanding shouldRecreateInstance

The `shouldRecreateInstance` function exists to handle a special case:

**When restartPolicy=`RecreateRoleInstanceOnPodRestart` AND a Pod becomes Failed**:
- Instead of just deleting and replacing the Failed Pod, recreate the entire Instance
- This maintains Instance-level consistency on Pod failures

**When restartPolicy is NOT `RecreateRoleInstanceOnPodRestart`** (i.e., `None`):
- Pod Failed → delete Failed pod → next reconcile creates replacement Pod
- This is the default behavior (same as StatefulSet)

#### Correct Implementation of shouldRecreateInstance

```go
// shouldRecreateInstance checks if the instance should be recreated (all pods deleted then recreated).
// This applies when:
//   - restartPolicy = RecreateRoleInstanceOnPodRestart AND any Pod is in Failed phase
//
// Note: This function does NOT handle container restart.
// Container restart with RecreateRoleInstanceOnPodRestart is handled by the
// underlying workload controller (e.g. LWS controller).
//
// Per KEP Non-Goals: Succeeded pods are NOT handled here - they represent normal completion
// and should not trigger Instance recreation.
func shouldRecreateInstance(instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod) bool {
    if instance.Spec.RestartPolicy != workloadsv1alpha2.RoleInstanceRestartPolicyRecreateOnPodRestart {
        return false
    }
    if len(pods) == 0 {
        return false
    }
    if !wasInstanceReady(instance) || instance.Generation != instance.Status.ObservedGeneration {
        return false
    }
    // Check if any Pod is in Failed phase (excluding pods being deleted)
    for _, p := range pods {
        if p.Status.Phase == v1.PodFailed && p.DeletionTimestamp == nil {
            return true
        }
    }
    return false
}
```

#### How Pod Failed → Replacement Pod Works (Two Levels)

The `calculateDiffsWithExpectation` function handles Pod Failed in two levels:

**Level 1: Delete Failed Pods (applies to ALL RestartPolicies)**

Failed pods must be deleted first because they block replacement creation (`hasOrphanPod` detects the same-name pod still exists):

```go
// Delete inactive (Failed) pods so that replacements can be created on next reconcile.
for _, p := range inactivePods {
    if p.Status.Phase == v1.PodFailed && p.DeletionTimestamp == nil {
        toDeletePods = append(toDeletePods, p)
    }
}
```

**Level 2: Instance Recreation (only RecreateRoleInstanceOnPodRestart)**

When `shouldRecreateInstance` returns true, ALL pods (active + inactive) are deleted and recreated:

```go
allPods := make([]*v1.Pod, 0, len(pods)+len(inactivePods))
allPods = append(allPods, pods...)
allPods = append(allPods, inactivePods...)
if shouldRecreateInstance(updateInstance, allPods) {
    return &expectationDiff{toDeleteNum: len(allPods), toDeletePod: allPods}, nil
}
```

**Scale Function Ordering**: Delete is checked before Create to ensure Failed pods are removed before attempting replacement (which would be blocked by `hasOrphanPod`):

```go
func (c *realControl) Scale(...) (bool, error) {
    diffRes, err := c.calculateDiffsWithExpectation(...)
    if diffRes.toDeleteNum > 0 { return c.deletePods(...) }  // Delete first
    if diffRes.toScaleNum > 0 { return c.createPods(...) }   // Then create
    return false, nil
}
```

### RestartPolicy Handling Matrix

#### Pod Failed

| RestartPolicy | Pod Failed Behavior |
|--------------|---------------------|
| `None` | Delete Failed pod → next reconcile creates replacement (pod-level) |
| `RecreateRoleInstanceOnPodRestart` | `shouldRecreateInstance` triggers → delete ALL Instance pods → recreate all (Instance-level) |

#### Container Restart

| RestartPolicy | Container Restart Behavior |
|--------------|---------------------------|
| `None` | Do nothing - let container restart naturally |
| `RecreateRoleInstanceOnPodRestart` | Handled by underlying workload controller (e.g. LWS controller), RBG controller does nothing |

#### Summary

| Scenario | Handler | Action |
|----------|---------|--------|
| Pod Failed + `None` | RoleInstance Controller | Delete Failed pod, create replacement |
| Pod Failed + `RecreateRoleInstanceOnPodRestart` | RoleInstance Controller | Recreate entire Instance |
| Container restart + `None` | None (K8s default) | Let container restart |
| Container restart + `RecreateRoleInstanceOnPodRestart` | LWS Controller | Recreate RoleInstance |

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
  - Removed `ContainerRestarted` check from `shouldRecreateInstance` (handled by LWS controller)

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

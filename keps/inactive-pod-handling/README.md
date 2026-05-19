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
    - [Pod Controller Changes](#pod-controller-changes)
    - [RoleInstance Controller Changes](#roleinstance-controller-changes)
    - [RestartPolicy Handling Matrix](#restartpolicy-handling-matrix)
    - [Test Plan](#test-plan)
<!-- /toc -->

## Two Aspects of Pod Restart Handling

This KEP addresses two distinct aspects that are often conflated. Understanding this separation is critical for correct implementation.

### Container Restart → restartPolicy Behavior

| Aspect | Description |
|--------|-------------|
| **Trigger** | Container inside Pod restarts (RestartCount increases) |
| **Handler** | Pod Controller (`pod_controller.go`) |
| **restartPolicy involved** | Yes - behavior depends on policy: |
| | - `None`: Do nothing |
| | - `RecreateRBGOnPodRestart`: Recreate entire RBG |
| | - `RecreateRoleInstanceOnPodRestart`: Recreate RoleInstance (handled by LWS controller, RBG controller does nothing) |
| **Key point** | This is a user-configurable policy for handling container crashes |

### Pod Failed/Error → Replacement Pod Creation

| Aspect | Description |
|--------|-------------|
| **Trigger** | Pod enters Failed state (Evicted, UnexpectedAdmissionError, etc.) |
| **Handler** | RoleInstance Controller (`instance_scale.go`) |
| **restartPolicy involved** | No - this is inherent Pod lifecycle management |
| **Behavior** | Create replacement Pod to maintain replica count |
| **Key point** | This follows native K8s controller behavior (StatefulSet/ReplicaSet) - Failed Pods should always get replacement |

### Why Separation Matters

The current implementation conflates these two aspects:

1. **Pod Controller (`podToRBG`)** checks both `containerRestarted` AND `podBecameFailed`
2. **RoleInstance Controller (`shouldRecreateInstance`)** checks both `ContainerRestarted` AND Pod Failed state

This causes confusion because:
- Pod Failed should NOT be routed through Pod Controller's restartPolicy logic
- Pod Controller's `restartPolicy` check should only apply to container restart

### Correct Separation

**Pod Controller should only handle container restart**:
- Trigger: `containerRestarted` or `podDeleted`
- Check `restartPolicy` to determine action
- Do NOT check `podBecameFailed`

**RoleInstance Controller should handle Pod Failed**:
- Through normal reconciliation, detects Pod Failed → active count decreases
- Creates replacement Pod (inherent lifecycle management, NOT dependent on restartPolicy)
- Do NOT check `ContainerRestarted`

**Special case for `RecreateRoleInstanceOnPodRestart`**:
- When restartPolicy=`RecreateRoleInstanceOnPodRestart` AND a Pod becomes Failed:
- RoleInstance Controller should recreate the entire Instance (not just replacement Pod)
- This is because the policy indicates user wants Instance-level consistency on Pod failures

## Background and Motivation

The RBG project has deficiencies in handling Pod abnormal states (e.g., Evicted, UnexpectedAdmissionError, Failed).

**Root Cause**: Native Kubernetes controllers treat Failed/Succeeded Pods as inactive and immediately create new Pods to maintain replica count. However, RBG's current implementation only triggers recreation when a Pod is deleted or a container restarts, lacking handling for Pods that directly enter the Failed state.

**Specific Issues**:

1. **Failed/Evicted Pod does not trigger RBG recreation**
   - `ContainerRestarted` function only checks Running/Pending status, returns false for Failed Pods
   - `PodDeleted` function only checks DeletionTimestamp, not Phase
   - Pod Controller `podToRBG` trigger conditions cannot cover Evicted/Failed Pods

2. **RoleInstance Controller relies on GC for pod replacement**
   - `GetActiveAndInactivePods` correctly classifies Pods, but inactive Pods need to wait for GC deletion before creating new Pods
   - Before GC runs, Failed Pods cause RoleInstance's Ready condition to become False

3. **DisruptionTarget handling limited to terminal pods**
   - Kubernetes 1.22+ introduced `DisruptionTarget` condition
   - RBG handles DisruptionTarget condition on terminal pods (Phase=Failed) but does not preemptively act before eviction completes
   - This matches K8s native controller behavior - controllers react after pod becomes terminal

**Example**: Suppose a Pod is Evicted due to node resource shortage, its Phase becomes Failed. The current RBG controller does not detect this state change, and the Pod stays in Failed state until GC cleans it up (default threshold may be large). During this period, the service has insufficient replicas.

## Goals

- Correctly detect and handle Failed/Evicted/UnexpectedAdmissionError Pod states
- Trigger recreation logic immediately when Pod enters Failed state, without waiting for GC deletion
- Follow Kubernetes native controller pattern: don't actively delete Failed Pods, instead create replacement Pods
- **Maintain backward compatibility** - existing container restart trigger behavior remains unchanged, only extend trigger conditions for Failed pods
- Support DisruptionTarget condition detection (K8s 1.22+)

## Non-Goals

- Do not introduce new RestartPolicy types
- Do not change existing RBG recreation workflow
- Do not handle Pod Succeeded state (normal completion) - explicitly excluded from trigger conditions
- Do not handle Unschedulable pods (PodScheduled=False) - these pods are still active per IsPodActive and rely on K8s scheduler retry

## Design Proposal

### User Stories

#### Story 1: Evicted Pod Automatic Recreation

> As an operator, when a Pod is evicted due to node resource shortage, I want RBG to immediately detect it and create a replacement Pod, without waiting for GC cleanup.

**Expected Behavior**:
- After Pod is evicted, Phase becomes Failed, status.reason="Evicted"
- RBG controller detects Pod entering inactive state
- Trigger corresponding recreation logic based on RestartPolicy
- Emit Event to notify user of Pod state change

#### Story 2: Scheduling Failed Pod Recreation

> As an operator, when a Pod cannot be scheduled due to insufficient resources (Unschedulable), I want to trigger recreation to attempt scheduling on other nodes.

**Expected Behavior**:
- Pod Scheduled condition is False, reason="Unschedulable"
- RBG controller detects scheduling failure
- May trigger Instance/RBG recreation based on RestartPolicy
- New Pod has chance to be scheduled on other nodes

#### Story 3: RecreateRBGOnPodRestart Triggering

> As a user, when configuring RestartPolicy=RecreateRBGOnPodRestart, I want any Pod entering Failed state to trigger entire RBG recreation.

**Expected Behavior**:
- Any Pod enters inactive state due to Eviction/Failed
- Entire RBG recreates all Roles in dependency order
- Ensure service overall consistency

### Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Extended trigger conditions may cause unexpected recreation loops | Use `wasInstanceReady` check to avoid triggering during initial creation; use `RestartInProgress` condition to prevent duplicate triggers |
| Frequent Failed Pod state transitions may increase controller load | predicate only captures active→inactive state transitions, avoiding triggering on every status update |
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

#### IsPodFailedSchedule

```go
// IsPodFailedSchedule checks if Pod scheduling failed (PodScheduled=False).
// Uses native constants: PodReasonUnschedulable, PodReasonSchedulerError.
func IsPodFailedSchedule(pod *corev1.Pod) bool {
    if pod == nil {
        return false
    }
    for _, cond := range pod.Status.Conditions {
        if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
            return cond.Reason == corev1.PodReasonUnschedulable ||
                cond.Reason == corev1.PodReasonSchedulerError
        }
    }
    return false
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
    if IsPodFailedSchedule(pod) {
        return "PodUnschedulable"
    }
    return "Unknown"
}
```

### Pod Controller Changes

**File**: `internal/controller/workloads/pod_controller.go`

**Scope**: Pod Controller ONLY handles container restart and pod deletion. It should NOT handle Pod Failed.

#### Modify podToRBG Trigger Conditions

Pod Controller should only trigger on:
1. Container restart
2. Pod deletion

It should NOT trigger on Pod becoming Failed (handled by RoleInstance Controller).

```go
func (r *PodReconciler) podToRBG(ctx context.Context, obj client.Object) []reconcile.Request {
    pod, ok := obj.(*corev1.Pod)
    if !ok {
        return []reconcile.Request{}
    }

    rbgName := pod.Labels[constants.GroupNameLabelKey]
    if rbgName == "" {
        return []reconcile.Request{}
    }

    // Only trigger on container restart or pod deletion
    containerRestarted := utils.ContainerRestarted(pod)
    podDeleted := utils.PodDeleted(pod)

    // Do NOT check podBecameFailed - handled by RoleInstance Controller

    if !containerRestarted && !podDeleted {
        return []reconcile.Request{}
    }

    // ... subsequent RBG fetch and RestartPolicy check logic ...
}
```

#### Modify Predicate

Predicate passes all Pod update events for RBG-owned pods. The filtering logic is centralized in `podToRBG` mapFunc which checks containerRestarted and podDeleted.

```go
UpdateFunc: func(e event.UpdateEvent) bool {
    oldPod, ok1 := e.ObjectOld.(*corev1.Pod)
    newPod, ok2 := e.ObjectNew.(*corev1.Pod)
    if ok1 && ok2 {
        _, oldExist := oldPod.Labels[constants.GroupNameLabelKey]
        _, newExist := newPod.Labels[constants.GroupNameLabelKey]

        return oldExist && newExist
    }
    return false
},
```

**Note**: Pod deletion is already handled by DeleteFunc, no change needed.

### RoleInstance Controller Changes

**File**: `pkg/reconciler/roleinstance/sync/instance_scale.go`

**Scope**: RoleInstance Controller handles Pod Failed → replacement Pod creation. This is inherent Pod lifecycle management, NOT dependent on restartPolicy.

#### Understanding shouldRecreateInstance

The `shouldRecreateInstance` function exists to handle a special case:

**When restartPolicy=`RecreateRoleInstanceOnPodRestart` AND a Pod becomes Failed**:
- Instead of creating a replacement Pod, recreate the entire Instance
- This maintains Instance-level consistency on Pod failures

**When restartPolicy is NOT `RecreateRoleInstanceOnPodRestart`**:
- Pod Failed → RoleInstance Controller creates replacement Pod through normal reconciliation
- This is the default behavior (same as StatefulSet/ReplicaSet)

#### Correct Implementation of shouldRecreateInstance

```go
// shouldRecreateInstance checks if the instance should be recreated (all pods deleted then recreated).
// This applies when:
//   - restartPolicy = RecreateRoleInstanceOnPodRestart AND any Pod is in Failed phase
//
// Note: This function does NOT handle container restart.
// Container restart with RecreateRoleInstanceOnPodRestart is handled by the
// underlying workload controller (e.g. LWS controller).
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

**Note**: We DO NOT check `ContainerRestarted` here. Container restart with `RecreateRoleInstanceOnPodRestart` policy is handled by the underlying workload controller (e.g. LWS controller), RBG controller has no action.

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
allPods := append(pods, inactivePods...)
if shouldRecreateInstance(updateInstance, allPods) {
    return &expectationDiff{toDeleteNum: len(allPods), toDeletePod: allPods}, nil
}
```

**Complete Flow**:

| RestartPolicy | Pod Failed Behavior |
|--------------|---------------------|
| `None` | Delete Failed pod → next reconcile creates replacement (pod-level) |
| `RecreateRoleInstanceOnPodRestart` | `shouldRecreateInstance` triggers → delete ALL Instance pods → recreate all (Instance-level) |
| `RecreateRBGOnPodRestart` | Delete Failed pod → next reconcile creates replacement (pod-level, same as None) |

### RestartPolicy Handling Matrix

#### Container Restart

| RestartPolicy | Container Restart Behavior |
|--------------|---------------------------|
| `None` | Do nothing - let container restart naturally |
| `RecreateRoleInstanceOnPodRestart` | LWS controller recreates RoleInstance, RBG controller does nothing |
| `RecreateRBGOnPodRestart` | Pod Controller triggers RBG recreation |

#### Pod Failed

| RestartPolicy | Pod Failed Behavior |
|--------------|---------------------|
| `None` | Create replacement Pod (default behavior, same as StatefulSet) |
| `RecreateRoleInstanceOnPodRestart` | Recreate entire RoleInstance (for Instance-level consistency) |
| `RecreateRBGOnPodRestart` | Pod Controller does NOT handle Pod Failed; RoleInstance Controller creates replacement Pod |

**Note**: `RecreateRBGOnPodRestart` policy only applies to container restart. Pod Failed should NOT trigger RBG recreation through Pod Controller. However, if multiple Pods across different roles become Failed, the RoleBasedGroup's overall status reflects this through Ready condition.

#### Summary

| Scenario | Handler | Action |
|----------|---------|--------|
| Container restart + `None` | None | Let container restart |
| Container restart + `RecreateRoleInstanceOnPodRestart` | LWS Controller | Recreate RoleInstance |
| Container restart + `RecreateRBGOnPodRestart` | Pod Controller | Recreate RBG |
| Pod Failed + `None` | RoleInstance Controller | Create replacement Pod |
| Pod Failed + `RecreateRoleInstanceOnPodRestart` | RoleInstance Controller | Recreate RoleInstance |
| Pod Failed + `RecreateRBGOnPodRestart` | RoleInstance Controller | Create replacement Pod (NOT RBG recreation) |

### Test Plan

[X] I/we understand that owners of the involved components may require updates to existing tests before implementing this enhancement.

#### Unit Tests

| Test Name | Test Content |
|-----------|--------------|
| `TestShouldRecreateInstance` | Verify Instance recreation logic: RecreateRoleInstanceOnPodRestart triggers on PodFailed, None does not, guards for Ready/Generation |
| `TestFailedPodDeletion` | Verify Failed pods without DeletionTimestamp are deleted, Succeeded pods and terminating pods are skipped |
| `TestPodReconciler_podToRBG_ContainerRestartAndDeletion` | Verify Pod Controller triggers RBG restart on container restart and pod deletion |
| `TestPodReconciler_podToRBG_PodFailedNotTriggered` | Verify Pod Failed does NOT trigger Pod Controller |
| `TestShouldRecreateInstanceWithInactivePod` | Verify inactive Pod triggers Instance recreation |
| `TestPredicateStateTransition` | Verify predicate only captures active→inactive transition, no duplicate triggers |

**Test Notes**:
- Ensure using native `kubecontroller.IsPodActive` and `podutil.IsPodTerminal` for judgment
- Test native constants: `corev1.PodReasonUnschedulable`, `corev1.DisruptionTarget`, etc.
- Test consistency with native K8s ReplicaSet/StatefulSet controller behavior

#### E2E Tests

**Test File Location**: `test/e2e/testcase/v1alpha2/inactive_pod.go`

**Test Environment**: Kind cluster (existing CI workflow)

**Method to Simulate Pod Abnormal State**:

In a real K8s cluster, triggering real Pod Eviction is difficult, so use **manually modifying Pod status subresource** to simulate:

```go
// Simulate Evicted Pod
func setPodEvicted(ctx context.Context, client client.Client, pod *corev1.Pod) error {
    pod.Status.Phase = corev1.PodFailed
    pod.Status.Reason = "Evicted"
    pod.Status.Message = "The node was low on resource: ephemeral-storage. Evicted."
    return client.Status().Update(ctx, pod)
}
```

**Test Case Design**:

##### Case 1: Evicted Pod Triggers RBG Recreation (RecreateRBGOnPodRestart)

- Create RBG with `RestartPolicy=RecreateRBGOnPodRestart`
- Wait for Pod Ready
- Manually change Pod status to `Phase=Failed, Reason="Evicted"`
- Verify RBG triggers recreation (RestartInProgress condition → True → False)
- Verify new Pod creation completes

##### Case 2: Failed Pod Triggers RoleInstance Recreation (RecreateRoleInstanceOnPodRestart)

- Create RBG with `RestartPolicy=RecreateRoleInstanceOnPodRestart` (LeaderWorkerSet)
- Wait for Pod Ready
- Manually change Pod status to `Phase=Failed, Reason="Error"`
- Verify RoleInstance recreation (LWS controller recreates Group)

##### Case 3: RestartPolicy=None Creates Replacement Pod

- Create RBG with `RestartPolicy=None` (default, Replicas=3)
- Wait for Pod Ready, record initial Pod UIDs
- Manually change one Pod to Evicted state
- Verify active Pod count returns to 3 (replacement Pod created)
- Verify no RestartInProgress condition (no RBG recreation)

##### Case 4: Predicate Only Captures State Transition (No Duplicate Triggers)

- Create RBG, trigger one Evicted → recreation
- Update new Pod status again (without changing Phase)
- Verify no second recreation triggered

#### Test Helper Functions

Add in `test/utils/utils.go`:

```go
// SetPodEvicted simulates Pod Evicted state
func SetPodEvicted(ctx context.Context, rclient client.Client, pod *corev1.Pod) error

// SetPodUnexpectedAdmissionError simulates Admission Error
func SetPodUnexpectedAdmissionError(ctx context.Context, rclient client.Client, pod *corev1.Pod) error

// SetPodFailed simulates Pod Failed state
func SetPodFailed(ctx context.Context, rclient client.Client, pod *corev1.Pod) error

// GetActivePodCount gets active Pod count (using native IsPodActive)
func GetActivePodCount(ctx context.Context, rclient client.Client, namespace, rbgName string) (int, error)
```

---

## Implementation History

- 2026-05-08: Initial implementation completed (conflated two dimensions)
  - Added pod inactive detection functions in `pkg/utils/pod_utils.go`
  - Modified Pod Controller trigger conditions in `internal/controller/workloads/pod_controller.go`
  - Fixed RoleInstance Controller in `pkg/reconciler/roleinstance/sync/instance_scale.go`
  - Added unit tests with 27 test cases
  - Added E2E test cases in `test/e2e/testcase/v1alpha2/inactive_pod.go`

- 2026-05-09: Refactored to properly separate controller responsibilities
  - Updated KEP documentation with controller responsibility separation
  - Pod Controller: removed `podBecameFailed` check, only handles container restart and pod deletion
  - RoleInstance Controller: removed `ContainerRestarted` check from `shouldRecreateInstance`
  - Predicate: simplified to check RBG label existence only
  - RoleInstance Controller handles Pod Failed through normal reconciliation

## Key Files Changed

| File | Change Type |
|------|-------------|
| `pkg/utils/pod_utils.go` | Added functions |
| `pkg/utils/pod_utils_test.go` | Added tests |
| `internal/controller/workloads/pod_controller.go` | Modified functions |
| `pkg/reconciler/roleinstance/sync/instance_scale.go` | Modified functions |
| `test/e2e/testcase/v1alpha2/inactive_pod.go` | Added E2E tests |
| `test/utils/utils.go` | Added helper functions |
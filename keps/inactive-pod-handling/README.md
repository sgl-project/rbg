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
- Do not actively clean up Failed Pods (follow K8s native pattern, let GC or users handle it)
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

#### Modify podToRBG Trigger Conditions

Use native `kubecontroller.IsPodActive` to judge Pod state:

```go
import (
    kubecontroller "k8s.io/kubernetes/pkg/controller"
)

func (r *PodReconciler) podToRBG(ctx context.Context, obj client.Object) []reconcile.Request {
    pod, ok := obj.(*corev1.Pod)
    if !ok {
        return []reconcile.Request{}
    }

    rbgName := pod.Labels[constants.GroupNameLabelKey]
    if rbgName == "" {
        return []reconcile.Request{}
    }

    // Original trigger conditions
    containerRestarted := utils.ContainerRestarted(pod)
    podDeleted := utils.PodDeleted(pod)

    // New trigger condition: Pod became inactive (reuse native IsPodActive)
    // Pod inactive = !IsPodActive, i.e., Phase=Failed/Succeeded or being deleted
    // Exclude being deleted case (already handled by podDeleted)
    podBecameInactive := !kubecontroller.IsPodActive(pod) && pod.DeletionTimestamp == nil

    // Only trigger if any condition is met
    if !containerRestarted && !podDeleted && !podBecameInactive {
        return []reconcile.Request{}
    }

    // ... subsequent RBG fetch and RestartPolicy check logic unchanged ...
}
```

#### Modify Predicate to Capture State Transition and Container Restarts

Use both `PodBecameFailed` (for Failed pod transitions) and `ContainerRestartCountChanged` (for container restarts) helper functions:

```go
UpdateFunc: func(e event.UpdateEvent) bool {
    oldPod, ok1 := e.ObjectOld.(*corev1.Pod)
    newPod, ok2 := e.ObjectNew.(*corev1.Pod)
    if ok1 && ok2 {
        _, oldExist := oldPod.Labels[constants.GroupNameLabelKey]
        _, newExist := newPod.Labels[constants.GroupNameLabelKey]

        if !oldExist || !newExist {
            return false
        }

        // Detect Pod state transition from active to Failed (excluding Succeeded)
        // and container restart count changes.
        // This ensures both inactive pod handling AND container restart handling work correctly.
        return utils.PodBecameFailed(oldPod, newPod) || utils.ContainerRestartCountChanged(oldPod, newPod)
    }
    return false
},
```

**Note**: We use `PodBecameFailed` instead of `PodBecameInactive` to explicitly exclude Succeeded pods per Non-Goals. Succeeded pods represent normal completion and should not trigger RBG recreation.

### RoleInstance Controller Changes

**File**: `pkg/reconciler/roleinstance/sync/instance_scale.go`

#### Modify shouldRecreateInstance

Use explicit check for Failed pods (excluding Succeeded per Non-Goals):

```go
func shouldRecreateInstance(instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod) bool {
    if instance.Spec.RestartPolicy != workloadsv1alpha2.RoleInstanceRestartPolicyRecreateOnPodRestart {
        return false
    }

    if len(pods) == 0 {
        return false
    }

    // Check container restart
    for _, pod := range pods {
        if utils.ContainerRestarted(pod) {
            return true
        }
    }

    // Check Failed Pods (excluding Succeeded per Non-Goals)
    if wasInstanceReady(instance) && instance.Generation == instance.Status.ObservedGeneration {
        expectedPodCount := getExpectedPodCount(instance)
        activeCount := 0
        for _, p := range pods {
            // Pod is active if: Phase != Failed && Phase != Succeeded && DeletionTimestamp == nil
            // We explicitly exclude Succeeded pods per Non-Goals
            if p.Status.Phase != corev1.PodFailed && p.Status.Phase != corev1.PodSucceeded && p.DeletionTimestamp == nil {
                activeCount++
            }
        }
        // Failed Pod causes active count to be less than expected
        if activeCount < expectedPodCount {
            return true
        }
    }

    return false
}
```

**Note**: We explicitly check `Phase != Failed && Phase != Succeeded` instead of using `IsPodActive` directly, to ensure Succeeded pods are excluded per Non-Goals while still correctly detecting Failed pods.

#### Modify calculateDiffsWithExpectation

The project already has `GetActiveAndInactivePods` function in `instance_utils.go`, using native `IsPodActive` for classification. Just ensure correct handling when calling:

```go
func (c *realControl) calculateDiffsWithExpectation(...) (*expectationDiff, error) {
    // ... shouldRecreateInstance check ...

    // Use existing GetActiveAndInactivePods to separate Pods
    // This function uses kubecontroller.IsPodActive for classification
    // inactive Pods are not counted in ExistIDs, automatically triggers new Pod creation

    // Current code already uses pods parameter to calculate topology
    // Internal GetComponentsTopology handles via GroupPodsByComponentName
    // Need to confirm only activePods participate in topology calculation

    prt, err := coreControl.GetComponentsTopology(pods)
    // ...
}
```

**Note**: The project's `pkg/reconciler/roleinstance/utils/instance_utils.go` already has:

```go
func GetActiveAndInactivePods(ctx context.Context, reader client.Reader, opts *client.ListOptions) ([]*v1.Pod, []*v1.Pod, error) {
    // ...
    for i := range podList.Items {
        pod := &podList.Items[i]
        if kubecontroller.IsPodActive(pod) {
            activePods = append(activePods, pod)
        } else {
            inactivePods = append(inactivePods, pod)
        }
    }
    return activePods, inactivePods, nil
}
```

This function already correctly uses native `IsPodActive`, no modification needed.

### RestartPolicy Handling Matrix

| RestartPolicy | Inactive Pod Behavior |
|--------------|----------------------|
| `None` | Create replacement Pod to maintain replica count, no Instance/RBG recreation. This is RoleInstanceSet default behavior. |
| `RecreateRoleInstanceOnPodRestart` | Entire RoleInstance recreation (all Pods deleted then recreated). Triggered when any Pod enters inactive state. |
| `RecreateRBGOnPodRestart` | Entire RBG recreation (all Roles recreated in dependency order). Triggered when Pod enters inactive state and role has this policy. |

### Test Plan

[X] I/we understand that owners of the involved components may require updates to existing tests before implementing this enhancement.

#### Unit Tests

| Test Name | Test Content |
|-----------|--------------|
| `TestPodBecameInactive` | Verify state transition detection: Running→Failed, Pending→Failed, Running→Succeeded |
| `TestIsPodEvicted` | Verify Evicted Pod detection: status.reason="Evicted", DisruptionTarget condition |
| `TestIsPodUnexpectedAdmissionError` | Verify admission error detection |
| `TestIsPodFailedSchedule` | Verify scheduling failure detection: PodReasonUnschedulable, PodReasonSchedulerError |
| `TestGetPodInactiveReason` | Verify correct reason string returned for each state |
| `TestPodToRBGWithInactivePod` | Verify inactive Pod triggers RBG reconcile (using native IsPodActive) |
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

- 2026-05-08: Initial implementation completed
  - Added pod inactive detection functions in `pkg/utils/pod_utils.go`
  - Modified Pod Controller trigger conditions in `internal/controller/workloads/pod_controller.go`
  - Fixed RoleInstance Controller in `pkg/reconciler/roleinstance/sync/instance_scale.go`
  - Added unit tests with 27 test cases
  - Added E2E test cases in `test/e2e/testcase/v1alpha2/inactive_pod.go`

## Key Files Changed

| File | Change Type |
|------|-------------|
| `pkg/utils/pod_utils.go` | Added functions |
| `pkg/utils/pod_utils_test.go` | Added tests |
| `internal/controller/workloads/pod_controller.go` | Modified functions |
| `pkg/reconciler/roleinstance/sync/instance_scale.go` | Modified functions |
| `test/e2e/testcase/v1alpha2/inactive_pod.go` | Added E2E tests |
| `test/utils/utils.go` | Added helper functions |
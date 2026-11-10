# RoleBasedGroup API Reference (v1alpha2)

This document describes the API fields for RoleBasedGroup and related CRDs in v1alpha2.

## RoleBasedGroup

| Field | Description |
|-------|-------------|
| `apiVersion` | `workloads.x-k8s.io/v1alpha2` |
| `kind` | `RoleBasedGroup` |
| `metadata` | Standard Kubernetes object metadata |
| `spec` | RoleBasedGroupSpec — desired state |
| `status` | RoleBasedGroupStatus — observed state |

## RoleBasedGroupSpec

| Field | Description |
|-------|-------------|
| `roles` | []RoleSpec — list of role specifications (required) |
| `roleTemplates` | []RoleTemplate — reusable pod templates (optional) |

## RoleSpec

| Field | Description |
|-------|-------------|
| `name` | string — unique role identifier (required) |
| `replicas` | *int32 — desired replicas (default: 1) |
| `dependencies` | []string — names of roles this role depends on |
| `standalonePattern` | *StandalonePattern — single pod per instance |
| `leaderWorkerPattern` | *LeaderWorkerPattern — leader + workers per instance |
| `customComponentsPattern` | *CustomComponentsPattern — heterogeneous pod groups |
| `rolloutStrategy` | *RolloutStrategy — update strategy configuration |
| `minReadySeconds` | *int32 — minimum seconds before considered ready |
| `scalingAdapter` | *ScalingAdapter — external autoscaling config |
| `engineRuntimes` | []EngineRuntime — runtime profiles to inject |

## Workload Patterns

### StandalonePattern

Single pod per instance:

| Field | Description |
|-------|-------------|
| `template` | PodTemplateSpec — inline pod template |
| `templateRef` | *TemplateRef — reference to roleTemplate |

### LeaderWorkerPattern

Leader + workers per instance (for tensor parallelism):

| Field | Description |
|-------|-------------|
| `size` | *int32 — total pods per instance (1 leader + size-1 workers) |
| `restartPolicyConfig` | *RestartPolicyConfig — restart behavior config: `type` (default: `RecreateRoleInstanceOnPodRestart`; also supports opt-in `RecreateRoleInstanceOnPodRestartFromCreation`), `baseDelaySeconds` (default: 30), `maxDelaySeconds` (default: 600) |
| `restartPolicy` | RestartPolicyType — **Deprecated**, use `restartPolicyConfig`. Honoured only when `restartPolicyConfig.type` is unset |
| `template` | PodTemplateSpec — base pod template |
| `templateRef` | *TemplateRef — reference to roleTemplate |
| `leaderTemplatePatch` | runtime.RawExtension — patch for leader pod |
| `workerTemplatePatch` | runtime.RawExtension — patch for worker pods |

### CustomComponentsPattern

Heterogeneous pod groups per instance:

| Field | Description |
|-------|-------------|
| `components` | []ComponentSpec — list of component definitions |
| `restartPolicyConfig` | *RestartPolicyConfig — restart behavior config: `type` (default: `RecreateRoleInstanceOnPodRestart`; also supports opt-in `RecreateRoleInstanceOnPodRestartFromCreation`), `baseDelaySeconds` (default: 30), `maxDelaySeconds` (default: 600) |
| `restartPolicy` | RestartPolicyType — **Deprecated**, use `restartPolicyConfig`. Honoured only when `restartPolicyConfig.type` is unset |

### ComponentSpec

| Field | Description |
|-------|-------------|
| `name` | string — component identifier |
| `size` | *int32 — number of pods for this component |
| `serviceName` | string — headless service name (optional) |
| `template` | PodTemplateSpec — pod template for this component |

## TemplateRef

Reference to a roleTemplate with optional patch:

| Field | Description |
|-------|-------------|
| `name` | string — name of roleTemplate to reference |
| `patch` | runtime.RawExtension — strategic merge patch |

## RoleTemplate

Reusable pod template at RBG level:

| Field | Description |
|-------|-------------|
| `name` | string — template identifier |
| `template` | PodTemplateSpec — pod template definition |

## RolloutStrategy

| Field | Description |
|-------|-------------|
| `type` | RolloutStrategyType — only `RollingUpdate` supported |
| `rollingUpdate` | *RollingUpdate — rolling update configuration |

### RollingUpdate

| Field | Description |
|-------|-------------|
| `type` | RollingUpdateType — `RecreatePod` or `InPlaceIfPossible` |
| `maxUnavailable` | intstr.IntOrString — max unavailable pods (default: 1) |
| `maxSurge` | intstr.IntOrString — max extra pods during update (default: 0) |
| `partition` | *int32 — update pods with ordinal >= partition |
| `inPlaceUpdateStrategy` | *InPlaceUpdateStrategy — in-place update config |

### InPlaceUpdateStrategy

| Field | Description |
|-------|-------------|
| `gracePeriodSeconds` | *int32 — wait time before forcing update |

## RestartPolicyConfig and RoleInstance restart budget

Both served RBG APIs preserve this configuration. The v1alpha1 path is
`spec.roles[].restartPolicy` plus `spec.roles[].maxGroupRestarts`; the v1alpha2
path is under `leaderWorkerPattern` or `customComponentsPattern`. Conversion
carries the effective policy and nil/zero/nonzero restart budgets without losing
them. `baseDelaySeconds` and `maxDelaySeconds` are v1alpha2-only: they have no
v1alpha1 field and are not round-tripped through v1alpha1.

Writing a component-backed role through v1alpha1 is still subject to the existing
v1alpha1 rule that a role-level template is required; a role created through
v1alpha2 has none, so that write is rejected until the rule is relaxed. The
leader-worker shape is unaffected.


`RestartPolicyConfig` also has optional `maxGroupRestarts` (`*int32`, minimum `0`,
unset = unlimited). It requires an explicit `type` of
`RecreateRoleInstanceOnPodRestart` or `RecreateRoleInstanceOnPodRestartFromCreation` and is
supported only by native RoleInstanceSet workloads. Place it on a role's
`leaderWorkerPattern.restartPolicyConfig` or
`customComponentsPattern.restartPolicyConfig` (v1alpha1: `spec.roles[].maxGroupRestarts`,
which requires the same policy spelled out in `spec.roles[].restartPolicy`); it is propagated to
`RoleInstanceSet.spec.roleInstanceTemplate.restartPolicyConfig` and then to
`RoleInstance.spec.restartPolicyConfig`.

The following `RoleInstanceStatus` fields persist the per-instance budget:

- `groupRestartCount` (`int32`): accepted full-group rebuilds. Persisted before
  deleting Pods; does not reset after Ready or a quiet period, unlike backoff's
  `restartCount`.
- `groupRestartRevision` (`string`): opaque Pod-template identity. Policy, delay,
  limit and component-size edits do not change it; a new Pod template gets a fresh
  budget. The RI UID already identifies the replica.
- `restartBudgetExhausted` (`bool`): persistent suppression, independent of the
  current configured policy/limit. No replacement creation or updates while true.

At exhaustion, `Degraded=True` with reason `GroupRestartBudgetExceeded` and
`RoleInstanceReady=False`. The complete group is terminated. Owned Pods that are
not already deleting receive the internal
`rbg.workloads.x-k8s.io/group-restart-budget-cleanup` finalizer, retaining their API
objects while kubelet terminates containers and releases scheduled resources.
No Pod phase is synthesized; native logs are not guaranteed. The same finalizer on
the RI protects teardown cleanup.

Explicit recovery is the RI annotation `rbg.workloads.x-k8s.io/recover=true`, added
**after** exhaustion. The controller invalidates pre-exhaustion annotations,
releases budget Pod finalizers, clears the count and suppression, consumes the
annotation, and permits a fresh group. Other annotation values, Ready transitions,
and policy/limit edits/removal do not recover an exhausted RI. RI deletion,
scale-down and rollout remove budget finalizers as teardown without charging or
recovering the removed RI. See [Failure Handling](../features/failure-handling.md).

## RestartPolicyType

| Value | Description |
|-------|-------------|
| `None` | No automatic restart |
| `RecreateRoleInstanceOnPodRestart` | Compatibility policy: recreate the affected instance's member Pods on Failed-pod or unexpected ordinary-container restart triggers, retaining the Ready/recent-restart gate and no deletion trigger |
| `RecreateRoleInstanceOnPodRestartFromCreation` | Opt-in RoleInstanceSet-only policy: recreate all affected instance member Pods on Failed phase, unexpected ordinary-container restart, or observed desired-member deletion, including before first Ready, after desired generation is observed and revisions match. Retains the RoleInstance object and UID. Deletion is best-effort; see [Failure handling](../features/failure-handling.md). |

## ScalingAdapter

| Field | Description |
|-------|-------------|
| `enable` | bool — enable autoscaling (default: false) |
| `labels` | map[string]string — additional labels for RBGSA |

## EngineRuntime

| Field | Description |
|-------|-------------|
| `profileName` | string — ClusterEngineRuntimeProfile name |
| `injectContainers` | []string — target container names |
| `containers` | []Container — override container configs |

## RoleBasedGroupStatus

| Field | Description |
|-------|-------------|
| `observedGeneration` | int64 — controller-observed generation |
| `conditions` | []Condition — standard conditions |
| `roleStatuses` | []RoleStatus — per-role status |

### RoleStatus

| Field | Description |
|-------|-------------|
| `name` | string — role name |
| `replicas` | int32 — desired replicas |
| `readyReplicas` | int32 — ready replicas |

## RoleBasedGroupScalingAdapter (RBGSA)

| Field | Description |
|-------|-------------|
| `apiVersion` | `workloads.x-k8s.io/v1alpha2` |
| `kind` | `RoleBasedGroupScalingAdapter` |
| `metadata` | Standard Kubernetes metadata |
| `spec` | ScalingAdapterSpec |
| `status` | ScalingAdapterStatus |

### ScalingAdapterSpec

| Field | Description |
|-------|-------------|
| `scaleTargetRef` | ScaleTargetRef — reference to RBG and role |

### ScaleTargetRef

| Field | Description |
|-------|-------------|
| `name` | string — RoleBasedGroup name |
| `role` | string — role name to scale |

### ScalingAdapterStatus

| Field | Description |
|-------|-------------|
| `replicas` | int32 — desired replicas |
| `readyReplicas` | int32 — ready replicas |
| `selector` | string — pod selector string |

## CoordinatedPolicy

| Field | Description |
|-------|-------------|
| `apiVersion` | `workloads.x-k8s.io/v1alpha2` |
| `kind` | `CoordinatedPolicy` |
| `metadata` | Standard Kubernetes metadata |
| `spec` | CoordinatedPolicySpec |

### CoordinatedPolicySpec

| Field | Description |
|-------|-------------|
| `policies` | []PolicySpec — list of coordination policies |

### PolicySpec

| Field | Description |
|-------|-------------|
| `name` | string — policy identifier |
| `roles` | []string — role names to coordinate |
| `strategy` | CoordinationStrategy — strategy configuration |

### CoordinationStrategy

| Field | Description |
|-------|-------------|
| `rollingUpdate` | *RollingUpdateCoordination — coordinated rolling update |
| `scaling` | *ScalingCoordination — coordinated scaling |

### RollingUpdateCoordination

| Field | Description |
|-------|-------------|
| `maxSkew` | string — max progress difference (e.g., "1%", "10%") |
| `maxUnavailable` | string — max unavailable across all roles |

### ScalingCoordination

| Field | Description |
|-------|-------------|
| `maxSkew` | string — max deployment progress difference |
| `progression` | ProgressionType — `OrderScheduled` or `OrderReady` |

## ClusterEngineRuntimeProfile

| Field | Description |
|-------|-------------|
| `apiVersion` | `workloads.x-k8s.io/v1alpha2` |
| `kind` | `ClusterEngineRuntimeProfile` |
| `metadata` | Standard Kubernetes metadata (cluster-scoped) |
| `spec` | EngineRuntimeProfileSpec |

### EngineRuntimeProfileSpec

| Field | Description |
|-------|-------------|
| `updateStrategy` | UpdateStrategy — `NoUpdate` or `RollingUpdate` |
| `initContainers` | []Container — init containers to inject |
| `containers` | []Container — sidecar containers to inject |
| `volumes` | []Volume — volumes to inject |

## RoleInstance Member Recovery

A `RoleInstance` owns the member Pods of one group. Recreation replaces its
member Pods, not the RoleInstance object: its UID and revision history remain
unchanged. RoleInstanceSet-backed roles implement both recreation policies;
LeaderWorkerSet-backed roles delegate their legacy restart handling to LWS and
reject `RecreateRoleInstanceOnPodRestartFromCreation` at admission. The default remains
`RecreateRoleInstanceOnPodRestart`; existing configurations do not opt into startup
or observed-deletion triggers on upgrade. Deployment/StatefulSet behavior is
unchanged.

With `RecreateRoleInstanceOnPodRestartFromCreation`, an explicit Failed phase, unexpected
ordinary-container restart, or observed `deletionTimestamp` on a desired member
can rebuild all member Pods before first Ready. Init-container and native-sidecar
restarts are not triggers. A live `Succeeded` member does not trigger recovery,
but its observed deletion does. Missing slots alone are not deletion signals: if
termination is missed, normal reconciliation may replace only that member.
`RecreateRoleInstanceOnPodRestart` retains its Ready/recent-restart gate and no
deletion trigger. Both policies respect generation/revision and in-place-update
guards and the exact Pod annotation value `restart-trigger-policy: "Ignore"`.
`Ignore` prevents initiation only; those Pods still retire when another member
triggers a group rebuild. Absent, `"Inherit"`, and unknown annotation values inherit
the role policy. Sidecars declared as ordinary containers are inspected; init
containers and native sidecars are not.

Deletion must remain observable through fresh API validation before acceptance.
An observed deleting member already gone at validation causes a
`RestartSignalDropped` Warning event, not a durable recovery request. Deletions
missed during backoff, controller downtime, or force deletion can fall back to
ordinary member repair; never-observed signals do not imply a warning. A later
runtime failure timeout or startup/liveness probe that terminates an ordinary
container can produce a new explicit failure/restart signal. Pending or NotReady
alone, including a readiness-probe failure, does not. Missing-member triggers and
a NotReady timeout remain deferred.

Generation-only spec changes that preserve policy and Pod-template revision can
be acknowledged before recovery, then retain the remaining failure backoff; a
spec generation bump alone does not guarantee immediate bypass.

### RoleInstancePodReference

Identifies a specific Pod incarnation in the RoleInstance's namespace:

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Name of the member Pod selected for deletion |
| `uid` | types.UID (string) | UID of that Pod incarnation; a same-name replacement is not selected by this reference |

### RoleInstanceStatus Recovery Fields

| Field / condition | Type | Description |
|-------------------|------|-------------|
| `retiringPods` | []RoleInstancePodReference (optional) | Durable deletion list persisted with restart count/time before any Delete. While non-empty, normal creation/update is blocked. The list is cleared only after every recorded old UID is confirmed absent from the API; Delete acceptance or replacement readiness is not completion. Resumed after controller restart without charging another attempt, including after policy/template changes. |
| `restartCount` | int32 | Accepted rebuild attempts. On the next attempt more than `max(maxDelaySeconds*2, 10 minutes)` after the previous attempt began, reset before charging the new attempt (count becomes 1); Ready is not required for this reset. |
| `lastRestartTime` | *metav1.Time | Start time of the most recently accepted attempt; backoff waits only the remainder of the interval measured from this timestamp. |
| `Restarting` (condition) | RoleInstanceCondition | True only while `retiringPods` is non-empty. Cleared after old UIDs disappear, not after replacements become Ready; it is informational, not an independent readiness latch. |
| `RoleInstanceReady` (condition) | RoleInstanceCondition | False with reason `Restarting` and message `Removing the previous member Pods` while draining. |

All recorded old UIDs must be gone before any replacement is created. UID-pinned
deletes do not remove same-name replacements. Finalizers can hold the drain
indefinitely. After five minutes of active draining, a `RetirementStalled` Warning
event asks operators to inspect remaining Pods/finalizers; it never forces cleanup.
The controller does not force-delete or fence nodes, and API UID absence is not
proof that processes on a partitioned node have stopped. Baselines for
old in-place container updates are cleared when recording the drain. After the
drain, the new policy permits repeated startup-failure rounds without ever
becoming Ready; the legacy policy's eligibility gate is unchanged.

**Upgrade/behavior change:** a legacy `Restarting=True` condition without a
`retiringPods` ledger is not a recorded drain or a suppression latch. It can clear;
a remaining eligible fault may cause one extra rebuild under normal backoff and
budget. This is not a one-release migration. Update CRDs with the controller to
retain the ledger. See
[Failure handling](../features/failure-handling.md) for backoff, missed deletion,
component dependency, and node-partition limitations.

## RoleInstanceSet Rollback Recovery

`status.rolloutReplacementState` is managed by the controller during an early
rollback (A → B → A). It preserves existing surge instances across controller
restarts while base instances are being replaced.

| Field | Description |
|-------|-------------|
| `updateRevision` | Target revision of the replacements |
| `replicas` | Exclusive upper ordinal of the persisted replacement scope |
| `partition` | Inclusive lower ordinal of the persisted replacement scope |

When maxSurge is positive, the controller persists this state before replacing
a base instance and waits for its cache to observe the write before reconciling
again. It clears the state when all base instances in the replacement range are
ready at the target revision, are not terminating, and have observed their
current generation. This can retain surge while an unrelated base instance in
that range is unhealthy, even after the originally replaced instances recover.

A different target revision invalidates the previous scope. Changes to replicas
or partition retain protection for the intersection of the original and current
base ranges; an empty intersection ends protection. Only an additional actual
replacement outside the persisted scope expands it. Pause and resume preserve
protection. After scale-down, a former base instance may temporarily act as surge
until an in-range replacement is ready.

Update the RoleInstanceSet CRD together with the controller. If replacement
intent cannot be persisted, the controller emits a Warning event and retries
without replacing the instance; an outdated CRD may have pruned the new field.
With maxSurge=0 there is no surge to protect, so replacement does not require
this state and remains possible with the older CRD.

## Condition Types

| Condition | Description |
|-----------|-------------|
| `Ready` | RBG is available (minimum replicas ready) |
| `Progressing` | RBG is creating or changing pods |
| `RollingUpdateInProgress` | Rolling update is active |
| `RestartInProgress` | Restart is in progress |

## Annotations

### Gang Scheduling Annotations

| Annotation | Description |
|------------|-------------|
| `rbg.workloads.x-k8s.io/group-gang-scheduling` | Enable gang scheduling |
| `rbg.workloads.x-k8s.io/group-gang-scheduling-timeout` | Timeout seconds (scheduler-plugins) |
| `rbg.workloads.x-k8s.io/group-gang-scheduling-volcano-queue` | Volcano queue name |
| `rbg.workloads.x-k8s.io/group-gang-scheduling-volcano-priority` | Volcano priority class |

## Labels

The following labels are automatically added by the controller:

| Label | Description |
|-------|-------------|
| `rbg.workloads.x-k8s.io/group-name` | RoleBasedGroup name |
| `rbg.workloads.x-k8s.io/role-name` | Role name |
| `rbg.workloads.x-k8s.io/role-instance-index` | RoleInstance index ordinal |

See [Labels, Annotations and Environment Variables](variables.md) for complete reference.

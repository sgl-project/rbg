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
| `restartPolicyConfig` | *RestartPolicyConfig — restart behavior config: `type` (default: `RecreateRoleInstanceOnPodRestart`), `activateAfter` (default: `Ready`), `baseDelaySeconds` (default: 30), `maxDelaySeconds` (default: 600) |
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
| `restartPolicyConfig` | *RestartPolicyConfig — restart behavior config: `type` (default: `RecreateRoleInstanceOnPodRestart`), `activateAfter` (default: `Ready`), `baseDelaySeconds` (default: 30), `maxDelaySeconds` (default: 600) |
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

## RestartPolicyType

| Value | Description |
|-------|-------------|
| `None` | No automatic restart |
| `RecreateRoleInstanceOnPodRestart` | Recreate only the role instance |

See [Failure Handling](../features/failure-handling.md) for native RoleInstance activation,
the pre-Ready `Restarting` guard limit, backend support and upgrade/rollout considerations.

## ScalingAdapter

| Field | Description |
|-------|-------------|
| `enable` | bool — enable autoscaling (default: false) |
| `labels` | map[string]string — additional labels for RBGSA |

## EngineRuntime

| Field | Description |
|-------|-------------|
| `profileName` | string — ClusterEngineRuntimeProfile name (required) |
| `component` | string — the one component of the role's pattern this reference operates on: `leader` or `worker` under leaderWorkerPattern, a declared component name under customComponentsPattern, the role's own name under standalonePattern (any other name there is a render error). Absent, the fragments (and the legacy `containers` merge) deliver to the pattern's default component set — the leader only under leaderWorkerPattern, every declared component under customComponentsPattern, the single role-named component under standalonePattern. Required whenever `targetContainers` is set, except under standalonePattern, where a component-less reference carrying `targetContainers` is scoped to the role's own name |
| `targetContainers` | []EngineRuntimeTargetContainer — one entry per container this reference touches: the entry's `name` is the consent to the profile's `containerPatches` at that container and the address of the entry's own `extraPatches` (requires `component`; duplicate names rejected at write time via the map list) |
| `containers` | []Container — legacy: role-provided env/args appended to profile-declared containers within the reference's component scope; superseded by `targetContainers[].extraPatches` |
| `injectContainers` | []string — inert: accepted and ignored, nothing reads it; removed at the next API version boundary |

### EngineRuntimeTargetContainer

| Field | Description |
|-------|-------------|
| `name` | string — the rendered container this entry addresses (required): role-declared or profile-declared, containers and init containers alike. Listing the name is the single toggle — it consents to the profile's patches at that container *and* fixes where the entry's `extraPatches` land. A name the role's own template also declares resolves to the role-owned container; a name injected only by a different reference or profile is a render error |
| `extraPatches` | []ContainerPatch — the role's RFC 6902 patch rules for this entry's container, applied in list order after the profile's `containerPatches` — all five roots at any index; what the role writes lands where it is written. Optional, 1–32 items |

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
| `updateStrategy` | UpdateStrategy — `NoUpdate` or `RollingUpdate`. **Deprecated:** accepted and never consumed; immutability removes the surface it would have governed, and the field is removed at the next API version boundary alongside `injectContainers` (see [Profile Mutability](../features/engine-runtime.md#profile-mutability)) |
| `initContainers` | []Container — init containers to inject |
| `containers` | []Container — sidecar containers to inject |
| `volumes` | []Volume — volumes to inject |
| `containerPatches` | []ContainerPatch — the profile-side patch list: target-less ordered entries, each firing at every `targetContainers` address a referencing `engineRuntimes[]` entry lists, in list order. Profile-side entries may address `/env`, `/volumeMounts` and `/command/0` only; `/args`, `/command/<i>` with `i > 0` and `/securityContext/capabilities/add` are role-side-only roots, and a profile-side entry addressing one is a render error attributed to the profile. Optional, 1–32 items. Mirrored into v1alpha1 with identical markers |

### EngineRuntimeProfileStatus

Maintained by the controller as reference tracking (display and detection surface; rendering never consults it, and enforcement stays with the admission webhook):

| Field | Description |
|-------|-------------|
| `referenceCount` | int32 — the count of live references (RoleBasedGroups and RoleBasedGroupSet group templates), surfaced as a `REFERENCES` printer column |
| `referencedGeneration` | int64 — the `metadata.generation` at which the profile became referenced, recorded on the none-to-referenced transition, never refreshed while referenced, cleared when the last reference leaves |
| `conditions` | []Condition — carries the `Frozen` condition: `True` with reason `Referenced` while referenced, flipping to reason `SpecChangedWhileReferenced` when `metadata.generation` no longer matches `referencedGeneration` (evidence the spec changed since the baseline was recorded: a bypassed write, or a legal edit in a zero-reference window the controller never observed) |

### ContainerPatch

| Field | Description |
|-------|-------------|
| `jsonPatch` | ContainerJSONPatch — the RFC 6902 format. Exactly one patch field must be set; today `jsonPatch` is the only one, and a future patch format joins as a sibling field |

### ContainerJSONPatch

| Field | Description |
|-------|-------------|
| `op` | string — patch operation; only `add` is accepted |
| `path` | string — RFC 6901 pointer, relative to the target container. Must be an element pointer under `/command`, `/args`, `/env`, `/volumeMounts` or `/securityContext/capabilities/add` |
| `value` | object — raw JSON matching the path's element shape: a string for `/command`, `/args` and capability elements; a corev1 EnvVar object for `/env`; a corev1 VolumeMount object for `/volumeMounts` |

Ops apply in list order (later ops see earlier ops' mutations; `add /command/0`
head-inserts, so multi-element prepends are written in reverse). Same-named
env entries resolve positionally, last-wins: a role-side append of an existing
name is the override gesture; a profile-side entry for a name the target
already carries must default ahead of it via `/env/0`, and a profile-side `-`
append onto a role-owned name is a render error.

## RoleInstanceSet Rollback Recovery

`status.rolloutReplacementState` is managed by the controller during an early
rollback (A → B → A). It preserves existing surge instances across controller
restarts while base instances are being replaced.

| Field | Description |
|-------|-------------|
| `updateRevision` | Target revision of the replacements |
| `replicas` | Recorded exclusive upper position bound when replacement intent was persisted |
| `partition` | Recorded inclusive lower position bound when replacement intent was persisted |

When maxSurge is positive, the controller persists this state before replacing
a base instance and waits for its cache to observe the write before reconciling
again. It clears the state when all base members in the current update range
exist, are ready at the target revision, are not terminating, and have observed
their current generation.

The saved position bounds are not stable member identities: gap filling can
move a different member into the same position. Completion therefore
conservatively checks the entire current update range, without restricting it
to the saved bounds. No additional API fields are required. Scale-up or a
partition decrease may retain surge for newly eligible members, even after the
original replacements recover.

A different target revision invalidates the previous state. An empty current
update range ends protection; members outside that range no longer block it.
Pause and resume preserve unfinished protection. After scale-down, a former
base instance may temporarily act as surge until the current range is ready.

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

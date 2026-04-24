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
| `restartPolicy` | RestartPolicyType — restart behavior enum |
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
| `template` | PodTemplateSpec — base pod template |
| `templateRef` | *TemplateRef — reference to roleTemplate |
| `leaderTemplatePatch` | runtime.RawExtension — patch for leader pod |
| `workerTemplatePatch` | runtime.RawExtension — patch for worker pods |

### CustomComponentsPattern

Heterogeneous pod groups per instance:

| Field | Description |
|-------|-------------|
| `components` | []ComponentSpec — list of component definitions |

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
| `RecreateRBGOnPodRestart` | Recreate entire RBG on pod restart |
| `RecreateRoleInstanceOnPodRestart` | Recreate only the role instance |

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
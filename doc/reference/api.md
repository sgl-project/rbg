# RoleBasedGroup API

## RoleBasedGroup

 Field               | Description
---------------------|----------------------------------------------------------------------------------------------
 apiVersion (string) | API version, e.g. v1alpha1
 kind (string)       | Resource kind, RoleBasedGroup
 metadata            | Standard Kubernetes object metadata
 spec [Required]     | RoleBasedGroupSpec — desired state of the RoleBasedGroup
 status              | RoleBasedGroupStatus — observed state (controller populated; exposed via subresource/status)

## RoleBasedGroupSpec

 Field            | Description
------------------|-----------------------------------------------------------------------------------------------
 roles [Required] | []RoleSpec — list of role specifications; at least one role required
 podGroupPolicy   | *PodGroupPolicy — optional PodGroup configuration to enable gang-scheduling (plugin-specific)

### PodGroupPolicy

 Field                         | Description
-------------------------------|------------------------------------------------------------------------------------
 (inline) PodGroupPolicySource | Inlined PodGroupPolicySource that selects the gang-scheduling plugin configuration

#### PodGroupPolicySource

 Field          | Description
----------------|----------------------------------------------------------------------------------------------------------------------------------------
 kubeScheduling | *KubeSchedulingPodGroupPolicySource — configuration for Kubernetes scheduler-plugins gang-scheduling support (only one source allowed)

### RolloutStrategy

 Field         | Description
---------------|------------------------------------------------------------------------------------------
 type          | RolloutStrategyType — rollout strategy type (enum: RollingUpdate); default=RollingUpdate
 rollingUpdate | *RollingUpdate — parameters for rolling updates (optional)

### RollingUpdate

 Field          | Description
----------------|----------------------------------------------------------------------------------------------------------------
 maxUnavailable | intstr.IntOrString — maximum number or percentage of replicas that can be unavailable during update; default=1
 maxSurge       | intstr.IntOrString — maximum number or percentage of replicas added above original during update; default=0
 partition      | intstr.IntOrString — an ordinal partition number that controls which StatefulSet Pods are updated; default=0

### RoleSpec

 Field               | Description
---------------------|-----------------------------------------------------------------------------------------------------------
 name [Required]     | string — unique role identifier (minLength=1)
 replicas            | *int32 — desired replicas for the role (minimum 0, default=1)
 rolloutStrategy     | *RolloutStrategy — rollout strategy applied when leader/worker templates change
 restartPolicy       | RestartPolicyType — restart policy enum (None, RecreateRBGOnPodRestart, RecreateRoleInstanceOnPodRestart)
 dependencies        | []string — names of roles this role depends on
 workload            | WorkloadSpec — workload type to use (apiVersion/kind); defaults to apps/v1 StatefulSet
 template [Required] | corev1.PodTemplateSpec — pod template for this role
 leaderWorkerSet     | LeaderWorkerTemplate — leader/worker split and related templates (optional)
 servicePorts        | []corev1.ServicePort — ports exposed by this role (optional)
 engineRuntimes      | []EngineRuntime — engine runtime profiles / injected containers (optional)
 scalingAdapter      | *ScalingAdapter — external scaling adapter config (optional)

#### WorkloadSpec

 Field      | Description
------------|-------------------------------------------------------------------------------------
 apiVersion | string — workload API version (default "apps/v1"); must match group/version pattern
 kind       | string — workload kind (default "StatefulSet")

#### EngineRuntime

 Field            | Description
------------------|-------------------------------------------------------------------------------------
 profileName      | string — engine runtime profile name
 injectContainers | []string — container names to inject runtime into (optional)
 containers       | []corev1.Container — engine runtime containers to override (command/args supported)

#### LeaderWorkerTemplate

 Field               | Description
---------------------|----------------------------------------------------------------------------------------------------------------------
 size                | *int32 — number of pods per group (minimum 1, default=1). 1 implies a single leader and a 0-replica worker workload.
 patchLeaderTemplate | runtime.RawExtension — schemaless patch applied to leader template (optional)
 patchWorkerTemplate | runtime.RawExtension — schemaless patch applied to worker template (optional)

#### ScalingAdapter

 Field  | Description
--------|----------------------------------------------------------------------------
 enable | bool — whether the scaling adapter is enabled for the role (default=false)

## RoleBasedGroupStatus

 Field              | Description
--------------------|-------------------------------------------------------------------------
 observedGeneration | int64 — controller-observed generation
 conditions         | []metav1.Condition — standard resource conditions (merge/patch by type)
 roleStatuses       | []RoleStatus — per-role status entries

### RoleStatus

 Field         | Description
---------------|-----------------------------------------------
 name          | string — role name
 readyReplicas | int32 — number of ready replicas for the role
 replicas      | int32 — total desired replicas for the role

### Condition Types (RoleBasedGroupConditionType)

 Field                   | Description
-------------------------|-----------------------------------------------------------------------------------------------------
 Ready                   | "Ready" — RBG is available (minimum groups up and running)
 Progressing             | "Progressing" — RBG is creating or changing groups/pods; any in-progress group sets this
 RollingUpdateInProgress | "RollingUpdateInProgress" — RBG is performing a rolling update after leader/worker template changes
 RestartInProgress       | "RestartInProgress" — RBG is restarting due to pod/container restarts

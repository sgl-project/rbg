# Labels, Annotations and Environment Variables

## Labels

### Group Level Labels

| Key | Description |
|-----|-------------|
| `rbg.workloads.x-k8s.io/group-name` | The name of the RoleBasedGroup to which these resources belong. |
| `rbg.workloads.x-k8s.io/group-uid` | A short hash identifying all Pods belonging to the same RoleBasedGroup instance. Used for topology affinity. |
| `rbg.workloads.x-k8s.io/group-revision` | The revision hash of the RoleBasedGroup, used to determine whether the RBG object has changed. |
| `rbg.workloads.x-k8s.io/group-unique-hash` | Used for pod affinity rules in exclusive topology. |

### Role Level Labels

| Key | Description |
|-----|-------------|
| `rbg.workloads.x-k8s.io/role-name` | The name of the role to which these resources belong. |
| `rbg.workloads.x-k8s.io/role-type` | The role template type. |
| `rbg.workloads.x-k8s.io/role-revision-<role-name>` | The revision hash of the specific role, used to determine whether the role has changed. |

### RoleInstance Level Labels

| Key | Description |
|-----|-------------|
| `rbg.workloads.x-k8s.io/role-instance-id` | Unique ID for RoleInstance and its Pods. |
| `rbg.workloads.x-k8s.io/role-instance-name` | The name of the RoleInstance. |
| `rbg.workloads.x-k8s.io/role-instance-index` | The index of RoleInstance in Role (for ordered scenarios). |
| `rbg.workloads.x-k8s.io/role-instance-owner` | The owning RoleInstanceSet name; used as the RoleInstanceSet selector. |
| `rbg.workloads.x-k8s.io/role-instance-delete` | Marks a RoleInstance for specified deletion. |

### Component Level Labels

| Key | Description |
|-----|-------------|
| `rbg.workloads.x-k8s.io/component-name` | The component name (e.g., leader/worker/coordinator). |
| `rbg.workloads.x-k8s.io/component-id` | The component instance index within the Instance. |
| `rbg.workloads.x-k8s.io/component-size` | The component replica count. |
| `rbg.workloads.x-k8s.io/component-index` | The component instance index. |

### PodGroup Label (Gang Scheduling)

| Key | Description |
|-----|-------------|
| `pod-group.scheduling.sigs.k8s.io/name` | The name of the PodGroup for gang scheduling (scheduler-plugins). |

## Annotations

### Group Level Annotations

| Key | Description |
|-----|-------------|
| `rbg.workloads.x-k8s.io/group-exclusive-topology` | Declares the topology domain (e.g. `kubernetes.io/hostname`) for exclusive scheduling. |
| `rbg.workloads.x-k8s.io/group-gang-scheduling` | Set to `"true"` to enable gang scheduling for the RoleBasedGroup. |
| `rbg.workloads.x-k8s.io/group-gang-scheduling-timeout` | Schedule timeout in seconds for scheduler-plugins gang scheduling (default: 60). |
| `rbg.workloads.x-k8s.io/group-gang-scheduling-volcano-queue` | Queue name for Volcano gang scheduling. |
| `rbg.workloads.x-k8s.io/group-gang-scheduling-volcano-priority` | PriorityClassName for Volcano gang scheduling. |

### Role Level Annotations

| Key | Description |
|-----|-------------|
| `rbg.workloads.x-k8s.io/role-size` | The size of the role (managed by controller). |
| `rbg.workloads.x-k8s.io/role-disable-exclusive` | Set to `"true"` to skip exclusive-topology affinity injection for that role. |
| `rbg.workloads.x-k8s.io/role-workload-type` | Specifies the workload type (primarily for v1alpha1 conversion). |

### RoleInstance Level Annotations

| Key | Description |
|-----|-------------|
| `rbg.workloads.x-k8s.io/role-instance-pattern` | Identifies the RoleInstance organization pattern (Stateful/Stateless). |
| `rbg.workloads.x-k8s.io/role-instance-gang-scheduling` | Enables gang-scheduling aware behavior at the RoleInstance level. |
| `rbg.workloads.x-k8s.io/role-instance-lifecycle-state` | Identifies the lifecycle state of a RoleInstance. |
| `rbg.workloads.x-k8s.io/inplace-update-state` | Identifies the in-place update state. |
| `rbg.workloads.x-k8s.io/inplace-update-grace` | Identifies the in-place update grace period configuration. |

## Environment Variables

| Key | Description |
|-----|-------------|
| `RBG_GROUP_NAME` | The name of the RoleBasedGroup. |
| `RBG_ROLE_NAME` | The name of the role. |
| `RBG_ROLE_INDEX` | The index or identity of the pod within the role. |
| `RBG_ROLE_INSTANCE_NAME` | The name of the RoleInstance. |
| `RBG_COMPONENT_NAME` | The component name within the RoleInstance (e.g., leader, worker, coordinator). |
| `RBG_COMPONENT_INDEX` | The index of the component instance within the RoleInstance. |
| `RBG_LWP_LEADER_ADDRESS` | The network address of the leader for leader-worker pattern workloads. |
| `RBG_LWP_WORKER_INDEX` | The component index within the Instance. |
| `RBG_LWP_GROUP_SIZE` | The total number of components in the Instance. |
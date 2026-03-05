# Migrating from v1alpha1 to v1alpha2

## Motivation

1. **Remove dependencies on StatefulSet, Deployment, and LeaderWorkerSet**
   - Using these three resources as child resources introduced additional cognitive overhead for users
   - The version dependency on LeaderWorkerSet also imposed Kubernetes version constraints
   - Features like in-place upgrades were difficult to implement on top of StatefulSet, Deployment, and LeaderWorkerSet
   - Instance and InstanceSet can provide full parity with the capabilities of all three resources

2. **Streamline Labels, Annotations, and Environment Variables**
   - Standardize metadata conventions while reducing potential conflicts with other products

## Schema Changes

<table>
<tr>
<th style="vertical-align: top; width: 50%;">v1alpha1</th>
<th style="vertical-align: top; width: 50%;">v1alpha2</th>
</tr>
<tr>
<td style="vertical-align: top;">

```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata: {}
spec:
  podGroup:
    volcanoScheduling:
      queue: default
  roles:
  - name: #roleName
    workload: # Deprecated
      apiVersion: workloads.x-k8s.io/v1alpha1
      kind: InstanceSet
    template: {} # pod template
    leaderWorkerSet:
      patchLeaderTemplate:
        metadata:
          labels:
            role: leader
      patchWorkerTemplate:
        metadata:
          labels:
            role: worker
      size: 3
```

</td>
<td style="vertical-align: top;">

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  annotations:
    rbg.workloads.x-k8s.io/enable-gang: "true"
spec:
  roles:
  - name: #roleName
    standalonePattern:
      template: {} # pod template
    leaderWorkerPattern:
      template: {} # pod template
      leaderPatch:
        metadata:
          labels:
            role: leader
      workerPatch:
        metadata:
          labels:
            role: worker
      size: 3
```

```bash
/manager --gang-vendor kubeScheduling
```

</td>
</tr>
</table>

### Summary of Changes

1. **Gradual deprecation of `spec.roles[*].workload` field**
   - The field is retained temporarily with InstanceSet as the new default
   - Setting it to StatefulSet/Deployment/LeaderWorkerSet triggers a deprecation warning event

2. **Gang scheduling configuration moved from spec to annotations**
   - The scheduler is now selected globally via controller flags
   - Gang scheduling is enabled per-resource through annotations

3. **New `spec.roles[*].standalonePattern` field**
   - Provides equivalent functionality to Deployment and StatefulSet

4. **New `spec.roles[*].leaderWorkerPattern` field**
   - Provides equivalent functionality to LeaderWorkerSet
   - Field names updated: `patchLeaderTemplate` → `leaderPatch`, `patchWorkerTemplate` → `workerPatch`

Multi-version CRD support is implemented through conversion webhooks.

## Labels & Annotations

### Current State

**Excessive and Inconsistent Prefixes**

| Prefix | Scope |
| ------ | ----- |
| `rolebasedgroup.workloads.x-k8s.io/` | RBG level |
| `rolebasedgroupset.workloads.x-k8s.io/` | RBGSet level |
| `component.rolebasedgroup.workloads.x-k8s.io/` | Component level |
| `instance.rolebasedgroup.workloads.x-k8s.io/` | Instance level (embedded in RBG) |
| `role.rolebasedgroup.workloads.x-k8s.io/` | Role level |
| `instanceset.workloads.x-k8s.io/` | InstanceSet level |
| `instance.workloads.x-k8s.io/` | Instance level (standalone) |
| `lifecycle.workloads.x-k8s.io/` | Lifecycle |
| `workloads.x-k8s.io/` | General purpose |

**Unclear Hierarchy**

The current labels and annotations do not clearly reflect the following hierarchy:

```
RBGSet (GroupSet)
  └── RBG (Group)
        └── Role
              └── RoleInstance
                    └── Component (Pod)
```

**Inconsistent Naming Conventions**

- Mix of `-id` and `-name` suffixes
- Inconsistent use of suffixes (LabelKey vs none)

### Kubernetes Community Best Practices

| Approach | Format | Example |
| -------- | ------ | ------- |
| Approach A (RBG adopted) | `{domain}/{key}` | `rbg.workloads.x-k8s.io/groupset-name` |
| Approach B | `{subdomain}.{domain}/{key}` | `groupset.rbg.workloads.x-k8s.io/name` |

**Kubernetes Official Recommendation — Approach A**

```yaml
app.kubernetes.io/name: mysql
app.kubernetes.io/component: database
app.kubernetes.io/version: "5.7.21"
```

**LeaderWorkerSet — Approach A**

```yaml
leaderworkerset.sigs.k8s.io/name: my-lws
leaderworkerset.sigs.k8s.io/worker-index: "0"
```

**Kueue — Approach A**

```yaml
kueue.x-k8s.io/queue-name: default
kueue.x-k8s.io/job-uid: abc123
```

**Istio — Approach B (Exception)**

```yaml
service.istio.io/canonical-name: myservice
topology.istio.io/cluster: cluster-1
gateway.istio.io/managed: istio
```

### Design Goals

1. **Unified prefix domain**: All metadata uses the `rbg.workloads.x-k8s.io/` prefix
2. **Hierarchy expressed through key names**: Uses `{level}-{attribute}` format (kebab-case)
3. **Align with Kubernetes conventions**: Consistent with sigs.k8s.io projects like LWS and Kueue
4. **Separation of concerns**: Labels for selectors, annotations for configuration, environment variables for runtime

### Labels Reference

| Level | v1alpha1 | v1alpha2 | Description |
| ----- | -------- | -------- | ----------- |
| GroupSet | `rolebasedgroupset.workloads.x-k8s.io/name` | `rbg.workloads.x-k8s.io/groupset-name` | Identifies the parent RBGSet |
| GroupSet | `rolebasedgroupset.workloads.x-k8s.io/rbg-index` | `rbg.workloads.x-k8s.io/groupset-index` | Index of RBG within RBGSet |
| Group | `rolebasedgroup.workloads.x-k8s.io/name` | `rbg.workloads.x-k8s.io/group-name` | Identifies the parent RBG |
| Group | `rolebasedgroup.workloads.x-k8s.io/group-unique-key` | `rbg.workloads.x-k8s.io/group-uid` | Unique UID for topology affinity |
| Group | `rolebasedgroup.workloads.x-k8s.io/controller-revision-hash` | `rbg.workloads.x-k8s.io/group-revision` | Controller revision hash for RBG |
| Role | `rolebasedgroup.workloads.x-k8s.io/role` | `rbg.workloads.x-k8s.io/role-name` | Identifies the parent Role |
| Role | `role.rolebasedgroup.workloads.x-k8s.io/template-type` | `rbg.workloads.x-k8s.io/role-type` | Template type of the Role |
| Role | `rolebasedgroup.workloads.x-k8s.io/role-revision-hash-%s` | `rbg.workloads.x-k8s.io/role-revision-%s` | Role-level revision hash |
| RoleInstance | `instanceset.workloads.x-k8s.io/owner-uid` | `rbg.workloads.x-k8s.io/role-instance-owner` | Controller UID of RoleInstance |
| RoleInstance | `instanceset.workloads.x-k8s.io/instance-id` | `rbg.workloads.x-k8s.io/role-instance-id` | Unique identifier of RoleInstance |
| RoleInstance | `instanceset.workloads.x-k8s.io/instance-name` | `rbg.workloads.x-k8s.io/role-instance-name` | Name of RoleInstance |
| RoleInstance | `instance.workloads.x-k8s.io/instance-name` | `rbg.workloads.x-k8s.io/role-instance-name` | Name of RoleInstance |
| RoleInstance | `instanceset.workloads.x-k8s.io/specified-delete` | `rbg.workloads.x-k8s.io/role-instance-delete` | Marks RoleInstance for deletion |
| Component | `instanceset.workloads.x-k8s.io/instance-component-id` | `rbg.workloads.x-k8s.io/component-id` | Unique ID of component within Instance |
| Component | `instanceset.workloads.x-k8s.io/instance-component-name` | `rbg.workloads.x-k8s.io/component-name` | Name of component |
| Component | `instance.workloads.x-k8s.io/component-name` | `rbg.workloads.x-k8s.io/component-name` | Name of component |
| Component | `instance.workloads.x-k8s.io/component-id` | `rbg.workloads.x-k8s.io/component-id` | Unique ID of component within Instance |
| Component | `component.rolebasedgroup.workloads.x-k8s.io/name` | `rbg.workloads.x-k8s.io/component-name` | Name of component |
| Component | `component.rolebasedgroup.workloads.x-k8s.io/index` | `rbg.workloads.x-k8s.io/component-id` | Unique ID of component within Instance |
| Component | `component.rolebasedgroup.workloads.x-k8s.io/size` | `rbg.workloads.x-k8s.io/component-size` | Replica count of component |

### Annotations Reference

| Level | v1alpha1 | v1alpha2 | Description |
| ----- | -------- | -------- | ----------- |
| Group | `rolebasedgroup.workloads.x-k8s.io/exclusive-topology` | `rbg.workloads.x-k8s.io/group-exclusive-topology` | Declares exclusive scheduling topology domain |
| Role | `rolebasedgroup.workloads.x-k8s.io/role-size` | `rbg.workloads.x-k8s.io/role-size` | Replica count of Role |
| Role | `rolebasedgroup.workloads.x-k8s.io/disable-exclusive-topology` | `rbg.workloads.x-k8s.io/role-disable-exclusive` | Disables exclusive topology scheduling for Role |
| RoleInstance | `instance.rolebasedgroup.workloads.x-k8s.io/pattern` | `rbg.workloads.x-k8s.io/role-instance-pattern` | Organization pattern of RoleInstance |
| Lifecycle | `lifecycle.workloads.x-k8s.io/state` | `rbg.workloads.x-k8s.io/lifecycle-state` | Lifecycle state |
| Lifecycle | `lifecycle.workloads.x-k8s.io/timestamp` | `rbg.workloads.x-k8s.io/lifecycle-timestamp` | Timestamp of lifecycle state change |
| InPlace | `workloads.x-k8s.io/inplace-update-state` | `rbg.workloads.x-k8s.io/inplace-state` | In-place update state |
| InPlace | `workloads.x-k8s.io/inplace-update-grace` | `rbg.workloads.x-k8s.io/inplace-grace` | In-place update grace period configuration |

## Environment Variables

| v1alpha1 | v1alpha2 | Description | Source | Notes |
| -------- | -------- | ----------- | ------ | ----- |
| GROUP_NAME | RBG_GROUP_NAME | Name of RBG | Direct injection: rbg.Name | |
| ROLE_NAME | RBG_ROLE_NAME | Name of Role | Direct injection: role.Name | |
| ROLE_INDEX | — | Ordinal index of Pod within Role | Downward API: metadata.labels['apps.kubernetes.io/pod-index'] | |
| INSTANCE_NAME | RBG_INSTANCE_NAME | Name of RoleInstance | Downward API: metadata.labels['instance.workloads.x-k8s.io/instance-name'] | |
| COMPONENT_NAME | RBG_COMPONENT_NAME | Name of component | Downward API: metadata.labels['instance.workloads.x-k8s.io/component-name'] | |
| LWS_LEADER_ADDRESS | RBG_COMPONENT_LEADER_ADDRESS | DNS address of leader component | Computed: $(RBG_INSTANCE_NAME)-0.{svcName}.{namespace} | For multi-node distributed inference/training only |
| LWS_WORKER_INDEX | RBG_COMPONENT_INDEX | Index of component within Instance | Downward API: metadata.labels['rbg.workloads.x-k8s.io/component-id'] | For multi-node distributed inference/training only |
| LWS_GROUP_SIZE | RBG_COMPONENT_SIZE | Total number of components in Instance | Downward API: label rbg.workloads.x-k8s.io/component-size | For multi-node distributed inference/training only |

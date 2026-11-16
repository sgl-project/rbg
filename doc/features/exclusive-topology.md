# Exclusive Topology

Exclusive topology lets a RoleBasedGroup or RoleBasedGroupSet constrain participating Pods to a topology domain, such as a node, rack, or zone. This is useful for distributed applications that require low-latency communication between roles or need to ensure data locality.

## How It Works

The controller uses the Pod label `rbg.workloads.x-k8s.io/group-uid` to identify a group. For roles
with exclusive topology enabled, it adds required Pod affinity for the same group and required Pod
anti-affinity for other groups with that label. In the same namespace, this prevents another
RoleBasedGroup from sharing the topology domain, even if that group does not enable exclusive topology.
Plan for one eligible topology domain per concurrent exclusive group, in addition to domains
already occupied by other groups in the namespace. An ordinary group that schedules first can
leave an exclusive group Pending if it occupies the last eligible domain.

The rules do not exclude Pods without `group-uid` or Pods in other namespaces. They apply when Pods
are scheduled and do not evict already running Pods if conditions later change. A topology domain can
be a node, zone, or other domain defined by a node label; some clusters restrict required Pod
anti-affinity to `kubernetes.io/hostname`.

## Annotation

To enable exclusive topology, add the following annotation to your RoleBasedGroup:

```yaml
rbg.workloads.x-k8s.io/group-exclusive-topology: "<topology-key>"
```

Common topology keys include:

- `kubernetes.io/hostname` - Schedule all pods on the same node
- `topology.kubernetes.io/zone` - Schedule all pods in the same zone
- `topology.kubernetes.io/region` - Schedule all pods in the same region

## Disable Exclusive Topology for Specific Role

To skip exclusive-topology affinity injection for a specific role, add this annotation to the role's template:

```yaml
rbg.workloads.x-k8s.io/role-disable-exclusive: "true"
```

That role keeps its group label, so its Pods can still be affinity targets for enabled roles in the
same group. If same-group Pods already occupy multiple topology domains, enabled roles may follow
any of those domains.

## Example: RoleBasedGroup with Exclusive Topology

The [rbg-with-exclusive-topology example](../../examples/basic/rbg/scheduling/exclusive-topology.yaml) demonstrates how to use exclusive topology with a RoleBasedGroup, ensuring all roles are scheduled on the same node.

In this example, all pods (leader, worker, etc.) will be scheduled on the same node due to the `kubernetes.io/hostname` topology key.

## Example: RoleBasedGroupSet with Exclusive Topology

RoleBasedGroupSet extends the exclusive topology feature to multiple groups, where each group is scheduled exclusively on a topology domain. See example [rbgs-exclusive-topology.yaml](../../examples/basic/rbgs/rbgs-exclusive-topology.yaml).

In this example:

- 3 RoleBasedGroups will be created (as specified by `replicas: 3`)
- Each RoleBasedGroup will have its pods scheduled on a separate node
- All pods within each individual RoleBasedGroup will be co-located on the same node

## Existing workloads

Enabling or changing exclusive topology on a running RBG changes its Pod templates and may replace
Pods. The controller upgrade replaces the generated `group-unique-hash` affinity selector with
`group-uid` on already-configured RoleInstanceSet, Deployment, StatefulSet, and LeaderWorkerSet
workloads. This may replace their Pods, including running Pods whose templates carry the old
label as a workaround. The upgrade activates the namespace-scoped mutual exclusion for newly
scheduled Pods, including Pods of groups without the annotation.

Before upgrading, verify that replacement Pods can satisfy both the same-group affinity and the
cross-group anti-affinity for the configured topology key. A group using the `group-unique-hash`
workaround may already share its domain with another RBG in the same namespace. After the upgrade,
the other RBG's Pods can prevent a replacement Pod from scheduling in that domain. If no eligible
domain satisfies both rules, the replacement stays Pending and a single-replica role can become
unavailable. A free domain elsewhere may still be unusable while same-group Pods remain in the
shared domain, because the required affinity follows those Pods.

Existing workload selectors are preserved. If an existing selector pins a conflicting `group-uid`,
reconciliation for that role stops before applying the workload and the controller reports the
conflict in a `FailedReconcileWorkload` Warning event on the RoleBasedGroup. Disable exclusive
topology for the affected role to resume reconciliation with that selector. When both required Pod
affinity and anti-affinity already use the topology key, the controller uses those rules as written.

## Use Cases

1. **Disaggregated Inference**: Deploy Prefill and Decode on the same node to reduce inter-PD communication overhead
2. **High-Performance Computing**: Place related computational tasks on the same physical hardware
3. **Multi-GPU Workloads**: Ensure tensor parallel pods are on the same node for fast interconnect

## Benefits

- **Reduced Network Latency**: Co-located pods can communicate with minimal network overhead
- **Improved Performance**: Related workloads benefit from data locality
- **Resource Efficiency**: Better utilization of node resources by grouping related workloads
- **Simplified Management**: Logical grouping of distributed applications

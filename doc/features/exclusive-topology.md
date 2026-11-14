# Exclusive Topology

Exclusive topology lets a RoleBasedGroup or RoleBasedGroupSet constrain participating Pods to a topology domain, such as a node, rack, or zone. This is useful for distributed applications that require low-latency communication between roles or need to ensure data locality.

## How It Works

The controller uses the Pod label `rbg.workloads.x-k8s.io/group-uid` to identify a group. For roles
with exclusive topology enabled, it adds required Pod affinity for the same group and required Pod
anti-affinity for other groups with that label. In the same namespace, this prevents another
RoleBasedGroup from sharing the topology domain, even if that group does not enable exclusive topology.
The cluster needs enough topology domains for these constraints.

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
Pods. Upgrading the controller can also replace Pods of RBGs already configured for exclusive
topology, because the affinity selector changes from `group-unique-hash` to `group-uid`.

Existing workload selectors are preserved. If a selector requires a different `group-uid`, the
controller reports a conflict. When both required Pod affinity and anti-affinity already use the
topology key, the controller keeps them without adding its own; check their selectors.

## Use Cases

1. **Disaggregated Inference**: Deploy Prefill and Decode on the same node to reduce inter-PD communication overhead
2. **High-Performance Computing**: Place related computational tasks on the same physical hardware
3. **Multi-GPU Workloads**: Ensure tensor parallel pods are on the same node for fast interconnect

## Benefits

- **Reduced Network Latency**: Co-located pods can communicate with minimal network overhead
- **Improved Performance**: Related workloads benefit from data locality
- **Resource Efficiency**: Better utilization of node resources by grouping related workloads
- **Simplified Management**: Logical grouping of distributed applications

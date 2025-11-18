# Exclusive Topology

Exclusive topology is a feature that allows you to specify a topology domain for a RoleBasedGroup or RoleBasedGroupSet,
ensuring that all pods belonging to the same group are scheduled on the same topology domain (e.g., the same node, rack,
or zone). This is particularly useful for distributed applications that require low-latency communication between roles
or need to ensure data locality.

## How It Works

The exclusive topology feature uses Kubernetes topology keys to define scheduling constraints. When you specify an
exclusive topology key, all pods within the same RoleBasedGroup or RoleBasedGroupSet will be scheduled on nodes that
share the same value for that topology key. This ensures co-location of related workloads for better performance and
reduced network latency.

## Annotation

To enable exclusive topology, add the following annotation to your resource:

```yaml
rolebasedgroup.workloads.x-k8s.io/exclusive-topology: "<topology-key>"
```

Common topology keys include:

- `kubernetes.io/hostname` - Schedule all pods on the same node
- `topology.kubernetes.io/zone` - Schedule all pods in the same zone
- `topology.kubernetes.io/region` - Schedule all pods in the same region

## Example: RoleBasedGroup with Exclusive Topology

The [rbg-with-exclusive-topology example](../../examples/basics/exclusive-topology.yaml) demonstrates how to use
exclusive topology with a RoleBasedGroup, ensuring all roles are scheduled on the same node.  
In this example, all pods (leader, worker, and lws) will be scheduled on the same node due to the
`kubernetes.io/hostname` topology key.

## Example: RoleBasedGroupSet with Exclusive Topology

RoleBasedGroupSet extends the exclusive topology feature to multiple groups, where each group is scheduled exclusively
on a topology domain. See example [rbgs-exclusive-topology.yaml](../../examples/rbgs/exclusive-topology.yaml).

In this example:

- 2 RoleBasedGroups will be created (as specified by `replicas: 2`)
- Each RoleBasedGroup will have its pods scheduled on a separate node
- All pods within each individual RoleBasedGroup will be co-located on the same node

## Use Cases

1. **Disaggregated Inference**: Deploy Prefill and Decode on the same node to reduce inter-PD communication overhead
2. **High-Performance Computing**: Place related computational tasks on the same physical hardware

## Benefits

- **Reduced Network Latency**: Co-located pods can communicate with minimal network overhead
- **Improved Performance**: Related workloads benefit from data locality
- **Resource Efficiency**: Better utilization of node resources by grouping related workloads
- **Simplified Management**: Logical grouping of distributed applications

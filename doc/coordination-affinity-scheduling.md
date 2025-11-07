# Role Coordination - Affinity Scheduling

## Overview

The Affinity Scheduling coordination type enables fine-grained control over pod placement across multiple roles in a RoleBasedGroup. This feature ensures that pods from different roles are deployed together on the same topology domain (e.g., same node) according to specified ratios.

## Use Case

In disaggregated inference architectures like prefill-decode separation, it's critical to maintain specific ratios between different components on the same physical node to optimize performance and resource utilization. For example:

- **Prefill-Decode (P-D) Architecture**: Deploy 2 prefill pods and 1 decode pod together on the same node
- **Multi-tier Systems**: Co-locate frontend, middleware, and backend components in specific ratios
- **GPU Sharing**: Ensure different ML workloads share GPU nodes in optimal ratios

## Configuration

### Basic Example

```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: inference-service
spec:
  coordination:
    - name: prefill-decode-affinity
      type: AffinityScheduling
      roles:
        - prefill
        - decode
      strategy:
        affinityScheduling:
          topologyKey: kubernetes.io/hostname
          ratios:
            prefill: 2
            decode: 1
          batchMode: Sequential
  roles:
    - name: prefill
      replicas: 6
      template:
        spec:
          containers:
            - name: prefill
              image: my-prefill:latest
    - name: decode
      replicas: 3
      template:
        spec:
          containers:
            - name: decode
              image: my-decode:latest
```

### Parameters

#### `topologyKey`
- **Type**: string
- **Default**: `kubernetes.io/hostname`
- **Description**: The node label key used to define the topology domain. Common values:
  - `kubernetes.io/hostname`: Same physical node
  - `topology.kubernetes.io/zone`: Same availability zone
  - `topology.kubernetes.io/region`: Same region

#### `ratios`
- **Type**: map[string]int32
- **Required**: Yes
- **Description**: Defines the deployment ratio between roles. The map key is the role name, and the value is the number of pods to deploy together in each batch.
- **Constraints**:
  - Must include all roles specified in the coordination
  - All ratios must be positive integers
  - Total replicas for each role must be divisible by its ratio

#### `batchMode`
- **Type**: string (enum)
- **Default**: `Sequential`
- **Values**:
  - `Sequential`: Deploy one batch at a time, waiting for all pods in the batch to be ready before starting the next batch
  - `Parallel`: Deploy multiple batches concurrently

## How It Works

### 1. Batch Calculation

The controller calculates the number of batches based on the ratios and replica counts:

```
Number of batches = min(role1.replicas / ratio1, role2.replicas / ratio2, ...)
```

**Example**: 
- prefill: 6 replicas, ratio: 2 → 6/2 = 3 batches
- decode: 3 replicas, ratio: 1 → 3/1 = 3 batches
- Total batches: min(3, 3) = 3

### 2. Pod Labeling

The controller adds a coordination label to each pod:
- Label key: `coordination.workloads.x-k8s.io/<coordination-name>`
- Label value: `member`

### 3. Affinity Injection

The controller injects PodAffinity rules into the pod template:

```yaml
spec:
  affinity:
    podAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        - labelSelector:
            matchExpressions:
              - key: coordination.workloads.x-k8s.io/prefill-decode-affinity
                operator: In
                values:
                  - member
          topologyKey: kubernetes.io/hostname
```

### 4. Deployment Flow

For a 2:1 prefill-decode ratio with Sequential batch mode:

1. **Batch 1**: 
   - Deploy prefill-0, prefill-1, decode-0
   - Kubernetes scheduler places all three on the same node (e.g., node-1)
   - Controller waits for all pods to be Ready

2. **Batch 2**:
   - Deploy prefill-2, prefill-3, decode-1
   - Scheduler places all three on another node (e.g., node-2)
   - Controller waits for all pods to be Ready

3. **Batch 3**:
   - Deploy prefill-4, prefill-5, decode-2
   - Scheduler places all three on another node (e.g., node-3)
   - Deployment complete

## Validation Rules

The coordination configuration is validated with the following rules:

1. **Role Existence**: All roles referenced in the coordination must exist in the RBG spec
2. **Ratio Completeness**: All coordinated roles must have a ratio defined
3. **Positive Ratios**: All ratios must be positive integers (> 0)
4. **Divisibility**: Each role's replica count must be divisible by its ratio
   - Example: If `prefill: 2`, then prefill replicas must be divisible by 2 (e.g., 2, 4, 6, 8...)

### Validation Examples

**Valid Configuration**:
```yaml
roles:
  - name: prefill
    replicas: 6  # 6 % 2 = 0 ✓
  - name: decode
    replicas: 3  # 3 % 1 = 0 ✓
coordination:
  - name: pd-affinity
    roles: [prefill, decode]
    strategy:
      affinityScheduling:
        ratios:
          prefill: 2
          decode: 1
```

**Invalid Configuration** (not divisible):
```yaml
roles:
  - name: prefill
    replicas: 7  # 7 % 2 = 1 ✗
  - name: decode
    replicas: 3
coordination:
  - name: pd-affinity
    roles: [prefill, decode]
    strategy:
      affinityScheduling:
        ratios:
          prefill: 2  # Error: 7 is not divisible by 2
          decode: 1
```

## Advanced Usage

### Multiple Coordinations

You can define multiple coordination strategies for different role groups:

```yaml
coordination:
  - name: prefill-decode-affinity
    type: AffinityScheduling
    roles: [prefill, decode]
    strategy:
      affinityScheduling:
        topologyKey: kubernetes.io/hostname
        ratios:
          prefill: 2
          decode: 1
  - name: cache-storage-affinity
    type: AffinityScheduling
    roles: [cache, storage]
    strategy:
      affinityScheduling:
        topologyKey: kubernetes.io/hostname
        ratios:
          cache: 1
          storage: 1
```

### Combining with Rolling Updates

You can use both AffinityScheduling and RollingUpdate coordination together:

```yaml
coordination:
  - name: deployment-affinity
    type: AffinityScheduling
    roles: [prefill, decode]
    strategy:
      affinityScheduling:
        topologyKey: kubernetes.io/hostname
        ratios:
          prefill: 2
          decode: 1
  - name: update-coordination
    type: RollingUpdate
    roles: [prefill, decode]
    strategy:
      rollingUpdate:
        maxUnavailableRatio: "5%"
        maxSkew: "1%"
        partition: "80%"
```

## Status Tracking

The coordination status is tracked in the RBG status field:

```yaml
status:
  coordinationStatus:
    - name: prefill-decode-affinity
      type: AffinityScheduling
      phase: InProgress
      currentBatch: 2
      totalBatches: 3
      roleStates:
        prefill:
          readyReplicas: 4
          targetReplicas: 6
        decode:
          readyReplicas: 2
          targetReplicas: 3
      lastUpdateTime: "2025-11-07T10:30:00Z"
      message: "Deploying batch 2 of 3"
```

## Troubleshooting

### Pods Stuck in Pending

**Symptom**: Pods remain in Pending state after coordination is configured.

**Possible Causes**:
1. **Insufficient Resources**: The cluster doesn't have nodes with enough resources to accommodate all pods in a batch
   - **Solution**: Add more nodes or reduce the batch size (ratios)

2. **Topology Constraints**: No single topology domain (node) can fit all pods in the batch
   - **Solution**: Use a different topologyKey (e.g., zone instead of hostname) or ensure nodes have sufficient capacity

### Uneven Distribution

**Symptom**: Pods are not evenly distributed across nodes as expected.

**Possible Cause**: Existing pods or node taints/tolerations affecting scheduling
- **Solution**: Check node labels, taints, and existing pod placement

### Validation Errors

**Symptom**: RBG creation fails with validation error.

**Common Issues**:
1. Replica count not divisible by ratio
   - **Solution**: Adjust replica count or ratio to ensure divisibility
2. Missing role in ratios
   - **Solution**: Ensure all coordinated roles have a ratio defined

## Best Practices

1. **Resource Planning**: Ensure nodes have sufficient resources to accommodate all pods in a batch
2. **Batch Size**: Choose ratios that align with your cluster's node capacity
3. **Monitoring**: Monitor coordination status to track deployment progress
4. **Testing**: Test coordination configurations in a staging environment before production
5. **Graceful Degradation**: Consider what happens if coordination fails (pods fall back to default scheduling)

## Examples

See the `examples/basics/` directory for complete working examples:
- `affinity-scheduling-coordination.yaml`: Basic affinity scheduling
- `multi-coordination.yaml`: Multiple coordination strategies

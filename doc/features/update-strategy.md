# Update Strategy

Rolling update is important for online services with zero downtime. For LLM inference services, this is particularly critical. RoleBasedGroup supports multiple update strategies with fine-grained control.

## Overview

In v1alpha2, each role can configure its own `rolloutStrategy`:

```yaml
roles:
  - name: inference
    replicas: 4
    rolloutStrategy:
      type: RollingUpdate
      rollingUpdate:
        type: RecreatePod  # or InPlaceIfPossible
        maxUnavailable: 1
        maxSurge: 1
```

## Update Types

| Type | Description | Use Case |
|------|-------------|----------|
| `RecreatePod` | Delete old pods before creating new ones | Stateful workloads, GPU pods |
| `InPlaceIfPossible` | Update pod spec without recreation if possible | Image updates, resource changes |
| `InPlaceOnly` | Only in-place update, fail if not possible | Strict in-place updates |

### RecreatePod Strategy

Deletes old pods and creates new ones. Safer for GPU workloads:

```yaml
rolloutStrategy:
  type: RollingUpdate
  rollingUpdate:
    type: RecreatePod
    maxUnavailable: 1
```

### InPlaceIfPossible Strategy

Updates pods in-place when only certain fields change (image, resources). Faster updates with less disruption:

```yaml
rolloutStrategy:
  type: RollingUpdate
  rollingUpdate:
    type: InPlaceIfPossible
    maxUnavailable: 1
    inPlaceUpdateStrategy:
      gracePeriodSeconds: 30  # Wait before forcing update
```

**Supported in-place updates**:
- Container image changes
- Resource requests/limits changes
- Certain environment variable changes

**Not supported for in-place**:
- Pod spec changes (volumes, ports, etc.)
- New containers added
- Requires pod recreation

## Configuration Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `maxUnavailable` | Maximum pods unavailable during update | 1 |
| `maxSurge` | Maximum extra pods during update | 0 |
| `partition` | Update pods with ordinal >= partition only | 0 |

### maxUnavailable

Can be absolute number or percentage:

```yaml
rollingUpdate:
  maxUnavailable: 2        # Absolute number
  # or
  maxUnavailable: "10%"    # Percentage of replicas
```

### maxSurge

Extra pods created during update for faster rollout:

```yaml
rollingUpdate:
  maxSurge: 2              # Absolute number
  # or
  maxSurge: "20%"          # Percentage of replicas
```

### partition

Control which pods get updated (StatefulSet-style):

```yaml
rollingUpdate:
  partition: 2  # Pods with ordinal >= 2 get updated
```

Useful for:
- Canary updates
- Gradual rollouts
- Testing on specific instances

## Example: Rolling Update with InPlaceIfPossible

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inference-cluster
spec:
  roles:
    - name: prefill
      replicas: 4
      minReadySeconds: 10
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: InPlaceIfPossible
          maxUnavailable: 1
          inPlaceUpdateStrategy:
            gracePeriodSeconds: 30
      standalonePattern:
        template:
          spec:
            containers:
              - name: inference
                image: inference:v1
                resources:
                  limits:
                    nvidia.com/gpu: "1"
```

### Updating the Image

```bash
kubectl patch rolebasedgroup inference-cluster --type='json' -p='[
  {
    "op": "replace",
    "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/image",
    "value": "inference:v2"
  }
]'
```

With `InPlaceIfPossible`, pods will be updated in-place (container restart) rather than full pod recreation.

## Example: Partition Configuration

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inference-cluster
spec:
  roles:
    - name: inference
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: RecreatePod
          maxUnavailable: 1
          partition: 2  # Only update pods 2, 3
      standalonePattern:
        template:
          spec:
            containers:
              - name: inference
                image: inference:v1
```

With partition=2:
- Pods with ordinal < 2 (0, 1) stay on old version
- Pods with ordinal >= 2 (2, 3) get updated to new version

Useful for testing new version on subset of pods before full rollout.

## Coordinated Rolling Update

For multi-role updates, use CoordinatedPolicy to keep roles synchronized:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: coordinated-rollout
spec:
  policies:
    - name: prefill-decode-sync
      roles:
        - prefill
        - decode
      strategy:
        rollingUpdate:
          maxSkew: "1%"
          maxUnavailable: "10%"
```

See [Coordinated Policy](coordinated-policy.md) for details.

## Examples: Rolling Update Operations

### Rolling Update Example

1. Create a RoleBasedGroup with multiple roles:

   ```bash
   kubectl apply -f examples/basic/rbg/update-strategy/rolling-update.yaml
   ```

1. Update a role and observe the rollout:

   ```bash
   kubectl patch rolebasedgroup rolling-update-demo --type='json' -p='[
     {
       "op": "replace",
       "path": "/spec/roles/0/standalonePattern/template/metadata/labels/appVersion",
       "value": "v2"
     }
   ]'
   ```

1. Check the rollout status:

   ```bash
   kubectl get rolebasedgroup rolling-update-demo -ojsonpath='{.status}'
   ```

### Partition Update Example

1. Create a RoleBasedGroup with partition:

   ```bash
   kubectl apply -f examples/basic/rbg/update-strategy/rolling-update-with-partition.yaml
   ```

1. Update and verify partition behavior:

   ```bash
   kubectl patch rolebasedgroup rolling-update-with-partition --type='json' -p='[
     {
       "op": "replace",
       "path": "/spec/roles/0/standalonePattern/template/metadata/labels/appVersion",
       "value": "v2"
     }
   ]'
   ```

1. Check that only pods >= partition are updated:

   ```bash
   kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rolling-update-with-partition
   ```

## Supported Workloads

| Workload | maxUnavailable | maxSurge | partition | InPlaceIfPossible |
|----------|---------------|----------|-----------|-------------------|
| StatefulSet | ✓ | ✓ | ✓ | ✓ |
| Deployment | ✓ | ✓ | - | ✓ |
| LeaderWorkerSet | ✓ | ✓ | ✓ (LWS >= 0.7.0) | ✓ |

**Note**: LeaderWorkerSet partition support requires LWS version >= 0.7.0.

## Examples

- [Rolling Update](../../examples/basic/rbg/update-strategy/rolling-update.yaml)
- [Rolling Update with Partition](../../examples/basic/rbg/update-strategy/rolling-update-with-partition.yaml)
- [Coordinated Rolling Update](../../examples/basic/coordinated-policy/coordinated-rolling-update.yaml)
# Autoscaling

RBG provides a `ScalingAdapter` that supports HPA, KEDA, or KPA to adjust the number of replicas for Roles.

The RBGSA exposes `status.readyReplicas` mirrored from the parent RBG's `status.roleStatuses[].readyReplicas`, allowing consumers to read readiness directly without a cross-resource lookup.

![autoscaler](../img/autoscaler.jpg)

## Enabling the Scaling Adapter

To enable autoscaling for a role, set `scalingAdapter.enable: true` in the role spec. RBG will automatically create a `RoleBasedGroupScalingAdapter` (RBGSA) resource that exposes a scale subresource for the HPA to target.

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: my-inference
spec:
  roles:
  - name: worker
    replicas: 4
    scalingAdapter:
      enable: true
    workload:
      apiVersion: apps/v1
      kind: StatefulSet
    template:
      # ...
```

Once created, an HPA can target the RBGSA:

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: my-inference-worker
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: my-inference-worker
  minReplicas: 2
  maxReplicas: 20
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
```

### ScalingAdapter Configuration

| Field           | Type                | Default        | Description                                                                 |
|-----------------|---------------------|----------------|-----------------------------------------------------------------------------|
| enable          | bool                | false          | Whether the scaling adapter is enabled for the role.                        |
| scaleDownPolicy | ScaleDownPolicyType | Unrestricted   | Controls scale-down behavior during rolling updates.                        |

### ScaleDownPolicy Values

| Value              | Description                                                                                                     |
|--------------------|-----------------------------------------------------------------------------------------------------------------|
| Unrestricted       | Allows scale-down at any time, even during an active rollout. This is the default behavior.                     |
| DeferDuringRollout | Defers scale-down for partition-based workloads (StatefulSet, LWS) while a rolling update is in progress.       |

## Scale-Down During Rolling Updates

### The Problem

For partition-based workloads (StatefulSet, LeaderWorkerSet), rolling updates proceed from the highest ordinals down — pods with ordinal >= partition run the new template. However, StatefulSet also deletes from the highest ordinals on scale-down. This means **scale-down during a rollout kills the already-updated pods first**, resetting rollout progress to zero.

```text
Before scale-down:  replicas=10, partition=7
                    Pods:  [0  1  2  3  4  5  6 | 7  8  9]
                            ── old template ──   ─ new ─

HPA scales down to 6:
  StatefulSet deletes pods 9, 8, 7 (the updated ones)

After scale-down:   replicas=6, partition=6
                    Pods:  [0  1  2  3  4  5]
                            ── all old template ──
                    Rollout progress: 3 → 0 updated pods
```

### The Solution: DeferDuringRollout

Setting `scaleDownPolicy: DeferDuringRollout` gates scale-down at the RBGSA controller level while a partition-based rolling update is in progress:

```yaml
scalingAdapter:
  enable: true
  scaleDownPolicy: DeferDuringRollout
```

When gating is active:
- The RBGSA sets a `ScaleDownDeferred` condition (visible via `kubectl get rbgsa -o yaml`)
- A Warning event is emitted on the RBGSA resource
- The controller requeues every 30 seconds to re-check rollout status
- Once the rollout completes (`UpdatedReplicas == Replicas`), the pending scale-down proceeds

Scale-up operations are never gated — new pods land on the new template automatically (their ordinal >= partition).

**Note**: This policy only affects partition-based workloads (StatefulSet, LeaderWorkerSet, RoleInstanceSet). Deployment-backed roles are unaffected because Kubernetes Deployments handle concurrent scaling and rollout natively via ReplicaSet proportioning.

## Example YAMLs

- [PD-Disagg with Scaling Adapter](../../examples/pd-disagg/sglang/sglang-pd.yaml)

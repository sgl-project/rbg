# Autoscaling

RoleBasedGroup provides a `scalingAdapter` that integrates with HPA, KEDA, or KPA to dynamically adjust the number of replicas for individual roles.

The RoleBasedGroupScalingAdapter (RBGSA) exposes `status.readyReplicas` mirrored from the parent RoleBasedGroup's `status.roleStatuses[].readyReplicas`, allowing HPA to read readiness directly without cross-resource lookup.

![autoscaler](../img/autoscaler.jpg)

## Overview

In v1alpha2, each role can enable `scalingAdapter` to allow external autoscaling:

```yaml
roles:
  - name: prefill
    replicas: 2
    scalingAdapter:
      enable: true
    standalonePattern:
      template:
        spec:
          containers:
            - name: prefill
              image: inference:latest
              resources:
                requests:
                  cpu: "2"
                  memory: "8Gi"
```

When `scalingAdapter.enable` is set to `true`:
- A RoleBasedGroupScalingAdapter CR is automatically created by the controller
- The adapter's lifecycle is bound to the RoleBasedGroup
- HPA can target the adapter's scale subresource

## RoleBasedGroupScalingAdapter CRD

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroupScalingAdapter
metadata:
  name: prefill-scaling-adapter
  namespace: default
spec:
  scaleTargetRef:
    name: inference-cluster  # RoleBasedGroup name
    role: prefill             # Role name to scale
status:
  readyReplicas: 2
  replicas: 2
```

### Key Fields

| Field | Description |
|-------|-------------|
| `scaleTargetRef.name` | Name of the RoleBasedGroup |
| `scaleTargetRef.role` | Name of the role to scale |
| `status.readyReplicas` | Mirrored from RBG role status |
| `status.replicas` | Current desired replicas |

## HPA Integration

### Step 1: Enable scalingAdapter on the Role

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inference-cluster
spec:
  roles:
    - name: prefill
      replicas: 2
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: prefill
                image: inference:latest
                ports:
                  - containerPort: 8080
                resources:
                  requests:
                    cpu: "1"
                    memory: "2Gi"

    - name: decode
      replicas: 4
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: decode
                image: inference:latest
                ports:
                  - containerPort: 8081
                resources:
                  requests:
                    cpu: "2"
                    memory: "4Gi"
```

### Step 2: Create HPA targeting the scaling adapter

**Note**: When `scalingAdapter.enable` is true, the controller automatically creates the RoleBasedGroupScalingAdapter. You only need to create the HPA.

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: prefill-hpa
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: inference-cluster-prefill  # Auto-generated name: <rbg-name>-<role-name>
  minReplicas: 1
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 80
```

### Scaling Adapter Naming Convention

The auto-created scaling adapter follows this naming pattern:
- `<rbg-name>-<role-name>`
- Example: `inference-cluster-prefill` for role "prefill" in RBG "inference-cluster"

## Custom Metrics

HPA can use custom metrics from Prometheus:

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: decode-hpa
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: inference-cluster-decode
  minReplicas: 1
  maxReplicas: 20
  metrics:
    - type: Pods
      pods:
        metric:
          name: inference_tokens_per_second
        target:
          type: AverageValue
          averageValue: "100"
```

## Multiple Roles Scaling

For PD-disaggregated inference, you can scale both prefill and decode independently:

```yaml
# HPA for prefill
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: prefill-hpa
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: inference-cluster-prefill
  minReplicas: 2
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          averageUtilization: 70

---
# HPA for decode
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: decode-hpa
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: inference-cluster-decode
  minReplicas: 4
  maxReplicas: 20
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          averageUtilization: 80
```

## Coordinated Scaling

For roles that need to scale together (like prefill/decode), use CoordinatedPolicy:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: coordinated-scaling
spec:
  policies:
    - name: prefill-decode-sync
      roles:
        - prefill
        - decode
      strategy:
        scaling:
          maxSkew: "10%"
          progression: OrderScheduled
```

See [Coordinated Policy](coordinated-policy.md) for details.

## scalingAdapter Configuration Options

```yaml
scalingAdapter:
  enable: true          # Enable autoscaling for this role
  labels:               # Additional labels on the scaling adapter (optional)
    custom-label: value
```

The controller adds these labels automatically:
- `rbg.workloads.x-k8s.io/group-name`: RoleBasedGroup name
- `rbg.workloads.x-k8s.io/role-name`: Role name

## KEDA Integration

KEDA can scale based on external metrics (queue length, etc.):

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: inference-scaler
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: inference-cluster-prefill
  minReplicaCount: 1
  maxReplicaCount: 10
  triggers:
    - type: prometheus
      metadata:
        serverAddress: http://prometheus:9090
        metricName: inference_queue_length
        threshold: "100"
```

## Examples

- [Scaling Adapter with HPA](../../examples/basic/rbg/scaling/scaling-adapter-with-hpa.yaml)
- [Coordinated Scaling](../../examples/basic/coordinated-policy/coordinated-scaling.yaml)
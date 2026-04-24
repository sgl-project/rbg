# Coordinated Policy

CoordinatedPolicy is a separate CRD introduced in v1alpha2 that enables coordinated operations across multiple roles in a RoleBasedGroup. This ensures that roles don't diverge too much during updates or scaling operations.

## Overview

In complex multi-role deployments like PD-disaggregated inference, different roles (prefill, decode) need to stay synchronized during:

- **Rolling Updates**: Prevent one role from updating too far ahead of others
- **Scaling**: Ensure roles scale together to maintain balanced capacity

CoordinatedPolicy provides `maxSkew` and `progression` controls to manage this coordination.

## CoordinatedPolicy CRD

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: pd-rollout-policy
  namespace: default
spec:
  policies:
    - name: prefill-decode-rollout
      roles:
        - prefill
        - decode
      strategy:
        rollingUpdate:
          maxSkew: "1%"
          maxUnavailable: "10%"
```

### Key Fields

| Field | Description |
|-------|-------------|
| `policies` | List of coordination policies to apply |
| `policies[].name` | Name identifier for the policy |
| `policies[].roles` | List of role names to coordinate |
| `policies[].strategy` | Coordination strategy configuration |

## Coordinated Rolling Update

### maxSkew

Controls the maximum allowed difference in update progress between coordinated roles:

- `"1%"`: At most 1% difference in progress
- `"10%"`: Allows more divergence for faster updates
- `1`: Absolute number of pods difference

### Example: Coordinated Rolling Update

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
spec:
  roles:
    - name: prefill
      replicas: 7
      minReadySeconds: 10
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: InPlaceIfPossible
      standalonePattern:
        template:
          spec:
            containers:
              - name: prefill
                image: prefill-engine:v1

    - name: decode
      replicas: 3
      minReadySeconds: 10
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: InPlaceIfPossible
      standalonePattern:
        template:
          spec:
            containers:
              - name: decode
                image: decode-engine:v1

---
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: pd-rollout-policy
spec:
  policies:
    - name: prefill-decode-rollout
      roles:
        - prefill
        - decode
      strategy:
        rollingUpdate:
          maxSkew: "1%"
          maxUnavailable: "10%"
```

When the RoleBasedGroup is updated, the CoordinatedPolicy ensures:
- Prefill and decode roles update in lockstep
- The update progress difference stays within `maxSkew` bounds
- At most `maxUnavailable` pods can be unavailable during update

## Coordinated Scaling

Coordinated scaling ensures roles scale together during scale-out operations.

### Progression Types

| Type | Description |
|------|-------------|
| `OrderScheduled` | Proceed when pods are scheduled (faster, less safe) |
| `OrderReady` | Proceed when pods are fully ready (slower, safer) |

### Example: Simple Coordinated Scaling

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: simple-inference
spec:
  roles:
    - name: prefill
      replicas: 10
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: prefill
                image: prefill-engine:latest

    - name: decode
      replicas: 10
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: decode
                image: decode-engine:latest

---
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: simple-scaling-policy
spec:
  policies:
    - name: prefill-decode-scaling
      roles:
        - prefill
        - decode
      strategy:
        scaling:
          maxSkew: "10%"
          progression: OrderScheduled
```

### Example: Safe Coordinated Scaling

For critical deployments where you want to ensure pods are fully ready before proceeding:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: safe-progressive-deployment
spec:
  roles:
    - name: prefill
      replicas: 50
      standalonePattern:
        template:
          spec:
            containers:
              - name: prefill
                image: prefill-engine:latest
            readinessProbe:
              exec:
                command: ["/bin/sh", "-c", "echo 1"]
              successThreshold: 3
              periodSeconds: 3
              initialDelaySeconds: 10

    - name: decode
      replicas: 30
      standalonePattern:
        template:
          spec:
            containers:
              - name: decode
                image: decode-engine:latest
            readinessProbe:
              exec:
                command: ["/bin/sh", "-c", "echo 1"]
              successThreshold: 3
              periodSeconds: 3
              initialDelaySeconds: 10

---
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: safe-scaling-policy
spec:
  policies:
    - name: prefill-decode-scaling
      roles:
        - prefill
        - decode
      strategy:
        scaling:
          maxSkew: "5%"
          progression: OrderReady
```

With `OrderReady`:
- All pods in a batch must be fully ready before the next batch starts
- Safer for critical workloads but slower scale-out
- `maxSkew: "5%"` ensures minimal divergence

## Use Cases

CoordinatedPolicy is essential for:

- **PD-Disaggregated Inference**: Prefill and decode must stay balanced
- **Multi-Role Training**: Coordinator and workers need synchronized updates
- **Production Deployments**: Minimize risk of capacity imbalance during updates

## Examples

- [Coordinated Rolling Update](../../examples/basic/coordinated-policy/coordinated-rolling-update.yaml)
- [Coordinated Scaling](../../examples/basic/coordinated-policy/coordinated-scaling.yaml)
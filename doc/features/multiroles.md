# Multi Roles

RoleBasedGroup allows configuring any number of roles with any workload types, and defining the startup order among roles using dependencies.

![rbg](../img/rbg.jpg)

## Overview

In v1alpha2, a RoleBasedGroup can have multiple roles, each with:
- Independent replica counts
- Different workload patterns (standalone, leader-worker, custom components)
- Dependencies on other roles for startup ordering
- Individual rollout strategies and restart policies

## Role Dependencies

Roles can specify `dependencies` to control startup order. A role with dependencies is created only after all its dependent roles are ready.

```yaml
spec:
  roles:
    - name: frontend
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: frontend
                image: nginx:latest

    - name: backend
      replicas: 3
      dependencies: ["frontend"]  # backend starts after frontend is ready
      standalonePattern:
        template:
          spec:
            containers:
              - name: backend
                image: nginx:latest
```

See [Role Dependencies](role-dependencies.md) for detailed documentation.

## Workload Patterns

Each role can use one of three workload patterns:

| Pattern | Description |
|---------|-------------|
| `standalonePattern` | Single pod per instance |
| `leaderWorkerPattern` | Leader + workers per instance (for TP) |
| `customComponentsPattern` | Heterogeneous pod groups |

See [Workload Patterns](patterns.md) for detailed documentation.

## Example: Multi-Role with Different Patterns

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: multi-role-demo
spec:
  roles:
    # Role with standalone pattern
    - name: gateway
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: gateway
                image: gateway:latest

    # Role with leader-worker pattern (TP=4)
    - name: inference
      replicas: 2
      dependencies: ["gateway"]
      leaderWorkerPattern:
        size: 4
        template:
          spec:
            containers:
              - name: sglang
                image: sglang:latest
                resources:
                  limits:
                    nvidia.com/gpu: "1"

    # Role with custom components pattern
    - name: disaggregated
      replicas: 1
      customComponentsPattern:
        components:
          - name: prefill
            size: 1
            template:
              spec:
                containers:
                  - name: prefill
                    image: prefill:latest
          - name: decode
            size: 4
            template:
              spec:
                containers:
                  - name: decode
                    image: decode:latest
```

## Role-Based Scaling

Each role can independently configure scaling:

```yaml
roles:
  - name: prefill
    replicas: 2
    scalingAdapter:
      enable: true
    standalonePattern:
      template:
        ...

  - name: decode
    replicas: 4
    scalingAdapter:
      enable: true
    standalonePattern:
      template:
        ...
```

HPA can target individual roles via RoleBasedGroupScalingAdapter. See [Autoscaling](autoscaler.md) for details.

## Examples

- [Multirole with Standalone Pattern](../../examples/basic/rbg/patterns/standalone-pattern.yaml)
- [Multirole with Leader-Worker Pattern](../../examples/basic/rbg/patterns/leader-worker-pattern.yaml)
- [Multirole with Dependencies](../../examples/basic/rbg/dependency/role-dependencies.yaml)
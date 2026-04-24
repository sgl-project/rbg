# Role Dependencies

Role dependencies allow you to control the startup order of roles within a RoleBasedGroup. A role with dependencies will only be created after all its dependent roles are ready, ensuring proper initialization sequences for multi-role applications.

## Overview

In a RBG CR, each role can specify a `dependencies` field listing the names of roles it depends on. This creates a dependency chain where:

- Roles with no dependencies start first
- Roles with dependencies wait for their dependent roles to become ready before being created
- Multiple roles can depend on the same role
- Dependencies can form hierarchies (A → B → C)

## Use Cases

Role dependencies are particularly useful for:

- **LLM Inference Services**: Router waits for workers to register before accepting requests
- **Distributed Training**: Workers wait for parameter server/coordinator to initialize
- **Service Mesh**: Sidecars wait for main service to be ready
- **PD-Disaggregated Inference**: Prefill workers start before decode workers

## Configuration

The `dependencies` field is specified in each role's spec as a list of role names:

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
      dependencies: ["frontend"]  # backend starts only after frontend is ready
      standalonePattern:
        template:
          spec:
            containers:
              - name: backend
                image: nginx:latest
```

## Dependency Patterns

### 1. Simple Router + Workers Pattern

The router starts first, then workers register with the router after they become ready:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: router-workers
spec:
  roles:
    # Router role (no dependencies, starts first)
    - name: router
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: router
                image: inference-router:latest

    # Worker-1 (depends on router)
    - name: worker-1
      replicas: 2
      dependencies: ["router"]
      standalonePattern:
        template:
          spec:
            containers:
              - name: worker
                image: inference-worker:latest

    # Worker-2 (depends on router)
    - name: worker-2
      replicas: 2
      dependencies: ["router"]
      standalonePattern:
        template:
          spec:
            containers:
              - name: worker
                image: inference-worker:latest
```

### 2. Hierarchical Dependency Pattern (PD-Disaggregated)

For disaggregated inference, you may need a hierarchical dependency chain:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
spec:
  roles:
    # Gateway (entry point, no dependencies)
    - name: gateway
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: gateway
                image: gateway:latest

    # Prefill workers (depend on gateway)
    - name: prefill
      replicas: 2
      dependencies: ["gateway"]
      standalonePattern:
        template:
          spec:
            containers:
              - name: prefill
                image: prefill-engine:latest

    # Decode workers (depend on prefill)
    - name: decode
      replicas: 2
      dependencies: ["prefill"]  # decode starts after prefill is ready
      standalonePattern:
        template:
          spec:
            containers:
              - name: decode
                image: decode-engine:latest
```

### 3. Master + Multiple Worker Groups Pattern

A master role coordinates multiple types of workers, with optional cross-dependencies:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: master-workers
spec:
  roles:
    # Master/Coordinator (no dependencies)
    - name: master
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: master
                image: coordinator:latest

    # GPU workers (depend on master)
    - name: gpu-workers
      replicas: 2
      dependencies: ["master"]
      standalonePattern:
        template:
          spec:
            containers:
              - name: gpu-worker
                image: gpu-worker:latest

    # CPU workers (depend on master)
    - name: cpu-workers
      replicas: 2
      dependencies: ["master"]
      standalonePattern:
        template:
          spec:
            containers:
              - name: cpu-worker
                image: cpu-worker:latest

    # Monitor (depends on both worker types)
    - name: monitor
      replicas: 1
      dependencies: ["gpu-workers", "cpu-workers"]
      standalonePattern:
        template:
          spec:
            containers:
              - name: monitor
                image: monitor:latest
```

## How Dependencies Work

When a role has dependencies:

1. The controller checks if all dependent roles are ready (all pods in ready state)
2. Once all dependencies are ready, the controller creates the role's instances
3. The role's `readyReplicas` status is updated as pods become ready
4. Downstream roles waiting on this role can then proceed

A role is considered "ready" when its `status.roleStatuses[].readyReplicas` equals the desired replicas.

## Examples

- [Router + Workers Pattern](../../examples/basic/rbg/dependency/role-dependencies.yaml)
- [Multiple Dependency Patterns](../../examples/basic/rbg/dependency/role-dependencies-2.yaml)
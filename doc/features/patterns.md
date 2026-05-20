# Workload Patterns

In v1alpha2, RoleBasedGroup supports three workload patterns for different deployment scenarios. Each pattern determines how pods are organized per role instance.

## Pattern Types

| Pattern | Description | Use Case |
|---------|-------------|----------|
| `standalonePattern` | Single pod per instance | Simple services, standalone pods |
| `leaderWorkerPattern` | Leader + workers per instance | Tensor parallelism, multi-node inference |
| `customComponentsPattern` | Heterogeneous pod groups | Disaggregated inference, custom pod compositions |

## standalonePattern

The simplest pattern where each instance is a single pod. Suitable for:
- Simple microservices
- Stateless applications
- Single-node inference

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: nginx-cluster
spec:
  roles:
    - name: frontend
      replicas: 3
      standalonePattern:
        template:
          spec:
            containers:
              - name: nginx
                image: nginx:latest
                ports:
                  - containerPort: 80
```

### Features

- Each replica creates exactly one pod
- Pods are managed as StatefulSet or Deployment based on configuration
- Supports in-place updates when configured

## leaderWorkerPattern

Creates grouped instances where each instance has one leader pod and multiple worker pods. Ideal for:
- LLM inference with tensor parallelism
- Multi-GPU workloads requiring coordinated pods
- Leader-worker distributed systems

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: lws-pattern-demo
spec:
  roles:
    - name: prefill
      replicas: 2
      leaderWorkerPattern:
        size: 4    # 1 leader + 3 workers = 4 pods per instance
        template:
          metadata:
            labels:
              app: inference
          spec:
            containers:
              - name: nginx
                image: inference-engine:latest
                ports:
                  - containerPort: 8080
        # Patch applied only to leader pod
        leaderTemplatePatch:
          metadata:
            labels:
              role: leader
          spec:
            containers:
              - name: nginx
                env:
                  - name: ROLE
                    value: leader
        # Patch applied only to worker pods
        workerTemplatePatch:
          metadata:
            labels:
              role: worker
          spec:
            containers:
              - name: nginx
                env:
                  - name: ROLE
                    value: worker
```

### Key Fields

| Field | Description |
|-------|-------------|
| `size` | Total pods per instance (1 leader + size-1 workers) |
| `template` | Base pod template for all pods |
| `leaderTemplatePatch` | Patch applied only to leader pod |
| `workerTemplatePatch` | Patch applied only to worker pods |
| `templateRef` | Reference to a roleTemplate (optional) |

### Use Case: Tensor Parallelism

For LLM inference with TP=4:

```yaml
leaderWorkerPattern:
  size: 4  # 4 GPUs across 4 pods
  template:
    spec:
      containers:
        - name: sglang
          image: sglang:latest
          resources:
            limits:
              nvidia.com/gpu: "1"
```

Each instance creates 4 pods, each with 1 GPU, communicating for tensor parallel inference.

## customComponentsPattern

Creates instances with multiple heterogeneous pod types (components). Each component can have:
- Different pod templates
- Different replica counts (size)
- Different resource requirements

Ideal for:
- Disaggregated inference (router/leader/worker pods in same instance)
- Heterogeneous multi-node deployments
- Custom pod compositions

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: custom-components-demo
spec:
  roles:
    - name: engine
      replicas: 3
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: InPlaceIfPossible
      customComponentsPattern:
        components:
          - name: prefill-router
            size: 1
            template:
              spec:
                containers:
                  - name: router
                    image: prefill-router:latest
                    resources:
                      requests:
                        cpu: "2"
                        memory: "8Gi"

          - name: prefill-leader
            size: 1
            template:
              spec:
                containers:
                  - name: leader
                    image: prefill-leader:latest
                    resources:
                      requests:
                        cpu: "4"
                        memory: "32Gi"
                        nvidia.com/gpu: "1"

          - name: prefill-worker
            size: 2
            template:
              spec:
                containers:
                  - name: worker
                    image: prefill-worker:latest
                    resources:
                      requests:
                        cpu: "2"
                        memory: "16Gi"
                        nvidia.com/gpu: "1"
```

### customComponentsPattern Fields

| Field | Description |
|-------|-------------|
| `components` | List of component definitions |
| `components[].name` | Component identifier |
| `components[].size` | Number of pods for this component |
| `components[].serviceName` | Service name governing this component's pods |
| `components[].labels` | Additional labels merged into every pod of this component |
| `components[].annotations` | Controller-directive annotations (component-depends-on, component-discovery, etc.) |
| `components[].template` | Pod template for this component |

#### `components[].labels` / `components[].annotations`

The `labels` and `annotations` fields on each component are merged into every pod created for that component at creation time. They take precedence over any labels/annotations already present in `template.metadata`.

**Controller-directive annotations** — such as `component-depends-on`, `component-discovery`, and `port-allocator` — should be placed here rather than inside `template.metadata.annotations`. This keeps the pod template spec clean and allows controller-level processing.

---

### Component Lifecycle Ordering (component-depends-on)

When deploying multi-component roles (e.g. Router → Leader → Worker), the default parallel creation/deletion of all components may cause startup failures. Components may crash if their dependencies are not ready.

The per-component annotation **`rolebasedgroup.workloads.x-k8s.io/component-depends-on`** solves this by allowing explicit ordering constraints.

#### Annotation Schema

```json
{
  "startAfter": ["component-name-1", "component-name-2"],
  "deleteAfter": ["component-name-1"]
}
```

| Field | Description |
|-------|-------------|
| `startAfter` | Component names that must have `ReadyReplicas >= Size` before this component starts |
| `deleteAfter` | Component names that must be fully deleted before this component is removed |

#### Start Order

A component listed in `startAfter` is considered ready when its entry in `RoleInstance.Status.ComponentStatuses` satisfies:

```text
ReadyReplicas >= Size  &&  Size > 0
```

Components without `startAfter` constraints start immediately in parallel.

#### Delete Order

Deletion gates are derived from two sources (union):
1. **Reverse of `startAfter`**: If component X started after Y, Y is deleted only after X is gone.
2. **Explicit `deleteAfter`**: The annotated component waits for the listed components to be fully deleted.

#### Example: Hierarchical Component Startup

```yaml
customComponentsPattern:
  components:
    # Leader and Worker start in parallel (no startAfter)
    - name: leader
      size: 1
      annotations:
        rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
          {"deleteAfter": ["router"]}
      template: ...

    - name: worker
      size: 2
      annotations:
        rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
          {"deleteAfter": ["router"]}
      template: ...

    # Router starts only after Leader and Worker are Ready
    - name: router
      size: 1
      annotations:
        rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
          {"startAfter": ["leader", "worker"]}
      template: ...
```

**Start order**: leader/worker (parallel) → router  
**Delete order**: router first → leader/worker (parallel)  

*This annotation is only meaningful within `CustomComponentsPattern` roles. It is ignored on `StandalonePattern` and `LeaderWorkerPattern`.*

#### Cycle Detection

If the dependency graph contains a cycle, the controller logs an error and falls back to the default parallel mode to avoid deadlocks.

#### Readiness Probe Recommendation

When using `startAfter`, define proper `readinessProbe` on the dependency component's pod spec to ensure the application is truly serving before dependents start.

---

### Intra-Role Service Discovery (component-discovery)

In multi-component roles, pods often need to discover each other's network addresses and port values at launch time. The opt-in annotation **`rolebasedgroup.workloads.x-k8s.io/component-discovery`** injects these values as environment variables.

#### Discovery Annotation Schema

```json
{
  "addressRefs": [
    {
      "env": "LEADER_ADDR",
      "component": "leader",
      "index": 0
    }
  ],
  "portRefs": [
    {
      "env": "LEADER_GRPC_PORT",
      "component": "leader",
      "portName": "leader-grpc"
    }
  ]
}
```

| Field | Description |
|-------|-------------|
| `addressRefs[].env` | Environment variable name to inject |
| `addressRefs[].component` | Target component name within the same role |
| `addressRefs[].index` | Zero-based pod ordinal (default: 0) |
| `portRefs[].env` | Environment variable name to inject |
| `portRefs[].component` | Target component name within the same role |
| `portRefs[].portName` | Logical port name defined in the port-allocator annotation |
| `portRefs[].index` | Zero-based pod ordinal for PodScoped ports (default: 0) |

#### Address Resolution

Pod addresses are resolved to a deterministic FQDN:

```text
<rbg-name>-<role-name>-<instance-index>-<comp-name>-<pod-index>.<headless-svc>.<ns>.svc.cluster.local
```

This FQDN is computed at Pod creation time without any lookups and works with Kubernetes headless service DNS.

#### Example: Worker Discovers Leader

```yaml
- name: worker
  size: 2
  annotations:
    rolebasedgroup.workloads.x-k8s.io/component-discovery: |
      {
        "addressRefs": [
          {"env": "LEADER_ADDR", "component": "leader", "index": 0}
        ],
        "portRefs": [
          {"env": "LEADER_GRPC_PORT", "component": "leader", "portName": "leader-grpc"}
        ]
      }
  template: ...
```

When this worker pod starts, it will have `LEADER_ADDR` and `LEADER_GRPC_PORT` injected as environment variables, pointing to the leader component's first pod.

*This annotation is processed by the Pod reconciler after port allocation and before writing the final Pod spec.*

---

### Use Case: Disaggregated Inference with Ordering & Discovery

A Prefill role with Router/Leader/Worker topology using both new annotations:

```yaml
roles:
  - name: prefill
    replicas: 2
    customComponentsPattern:
      components:
        - name: leader
          size: 1
          annotations:
            rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
              {"deleteAfter": ["router"]}
          template:
            spec:
              containers:
                - name: leader
                  image: prefill-leader:v1

        - name: worker
          size: 4
          annotations:
            rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
              {"deleteAfter": ["router"]}
            rolebasedgroup.workloads.x-k8s.io/component-discovery: |
              {
                "addressRefs": [
                  {"env": "LEADER_ADDR", "component": "leader", "index": 0}
                ],
                "portRefs": [
                  {"env": "LEADER_GRPC_PORT", "component": "leader", "portName": "leader-grpc"}
                ]
              }
          template:
            spec:
              containers:
                - name: worker
                  image: prefill-worker:v1

        - name: router
          size: 1
          annotations:
            rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
              {"startAfter": ["leader", "worker"]}
          template:
            spec:
              containers:
                - name: router
                  image: prefill-router:v1
```

Each instance creates 6 pods (1 leader + 4 workers + 1 router) with ordered startup and automatic address/port injection.

## Pattern Comparison

| Feature | standalone | leaderWorker | customComponents |
|---------|------------|--------------|------------------|
| Pods per instance | 1 | 1 + N (leader+workers) | Multiple heterogeneous |
| Pod types | Same | Same with patches | Different |
| Use case | Simple services | TP inference | Disaggregated |
| Backend workload | StatefulSet/Deployment | RoleInstanceSet (or external LeaderWorkerSet) | RoleInstanceSet |

## Examples

- [Standalone Pattern](../../examples/basic/rbg/patterns/standalone-pattern.yaml)
- [Leader-Worker Pattern](../../examples/basic/rbg/patterns/leader-worker-pattern.yaml)
- [Custom Components Pattern](../../examples/basic/rbg/patterns/custom-components-pattern.yaml)
- [Custom Components with Ordering & Discovery](../../examples/basic/rbg/patterns/custom-components-ordered-discovery.yaml)
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
- Disaggregated inference (prefill/decode pods in same instance)
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
          - name: prefill-component
            size: 1
            template:
              spec:
                containers:
                  - name: prefill
                    image: prefill-engine:latest
                    resources:
                      requests:
                        cpu: "4"
                        memory: "32Gi"
                        nvidia.com/gpu: "1"

          - name: decode-component
            size: 2
            template:
              spec:
                containers:
                  - name: decode
                    image: decode-engine:latest
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
| `components[].template` | Pod template for this component |

### Use Case: Disaggregated Instance

An instance containing both prefill and decode pods in one unit:

```yaml
customComponentsPattern:
  components:
    - name: prefill
      size: 1
      template:
        spec:
          containers:
            - name: prefill
              image: prefill:v1
    - name: decode
      size: 4
      template:
        spec:
          containers:
            - name: decode
              image: decode:v1
```

Each instance has 1 prefill pod + 4 decode pods working together.

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
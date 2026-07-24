# Deploying Inference Services with RBG

## Overview

RoleBasedGroup (RBG) is a Kubernetes API for orchestrating distributed, stateful AI inference workloads. RBG abstracts an inference service as a **role-based group** — composed of multiple roles (Roles) that collaborate to form a complete inference service.

Deploying an inference service involves two core decisions:

1. **Role architecture**: What roles make up the inference service? For example, a single engine role, or a three-role Router + Prefill + Decode?
2. **Role topology**: What is the Pod topology within each role? Single Pod, or Leader + Workers multi-Pod?

This document uses the [SGLang](https://github.com/sgl-project/sglang) inference engine as an example. The same topology patterns apply to any inference engine (such as vLLM, TensorRT-LLM, etc.).

## Prerequisites

+ Kubernetes cluster version >= 1.24
+ RBG Controller installed (see [Installation Guide](https://github.com/sgl-project/rbg))
+ GPU nodes in the cluster (`nvidia.com/gpu` resource available)
+ NVIDIA Device Plugin installed

> **Note**: The following examples use the SGLang engine (`lmsysorg/sglang:v0.5.9`) and SGLang Router (`lmsysorg/sglang-router:v0.2.4`). If using other inference engines, replace with the corresponding image and adjust startup parameters.
>

---

## Multi-Role Definition

The core concept of RBG is the **Role**. An inference service can consist of one or more roles, each bearing different responsibilities. Depending on the inference architecture, the number and division of roles varies.

### Aggregated Deployment: Single Role

Aggregated deployment treats the entire inference engine as a single role. Prefill (prompt encoding) and Decode (token generation) are completed within the same engine instance. This is the simplest deployment approach.

```plain
┌────────────────────────────────┐
│  RoleBasedGroup                │
│  ┌──────────────────────────┐  │
│  │ Role: backend             │  │
│  │ Prefill + Decode          │  │
│  └──────────────────────────┘  │
└────────────────────────────────┘
```

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: agg-inference
spec:
  roles:
    - name: backend
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "Qwen/Qwen3-0.6B"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                  - --tp-size
                  - "1"
                ports:
                  - name: http
                    containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"
                volumeMounts:
                  - name: dshm
                    mountPath: /dev/shm
            volumes:
              - name: dshm
                emptyDir:
                  medium: Memory
                  sizeLimit: 30Gi
```

#### Parameter Description

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `spec.roles[].name` | string | Yes | - | Role name, unique within the RBG |
| `spec.roles[].replicas` | int32 | No | 1 | Number of role instance replicas |

### PD-Disaggregated Deployment: Multi-Role Collaboration

PD disaggregation (Prefill-Decode Disaggregated) splits the inference process into multiple roles, each with its own responsibility:

+ **Router**: Request router (CPU), distributes requests to Prefill or Decode instances
+ **Prefill**: Prompt encoding engine (GPU), compute-intensive
+ **Decode**: Token generation engine (GPU), memory-intensive

All three roles are declared in the same RBG and can scale independently.

```plain
┌──────────────────────────────────────────────────────┐
│  RoleBasedGroup                                      │
│                                                      │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐          │
│  │ Router    │  │ Prefill   │  │ Decode    │          │
│  │ (CPU)     │  │ (GPU)     │  │ (GPU)     │          │
│  └─────┬────┘  └──────────┘  └──────────┘          │
│        │                                             │
│        ├──→ Prefill instance                         │
│        └──→ Decode instance                          │
└──────────────────────────────────────────────────────┘
```

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
spec:
  roles:
    # Router: Request router
    - name: router
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: router
                image: lmsysorg/sglang-router:v0.2.4
                command:
                  - python3
                  - -m
                  - sglang_router.launch_router
                  - --pd-disaggregation
                  - --prefill
                  - "http://pd-inference-prefill-0.s-pd-inference-prefill:8000"
                  - --decode
                  - "http://pd-inference-decode-0.s-pd-inference-decode:8000"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                ports:
                  - name: http
                    containerPort: 8000
                resources:
                  requests:
                    cpu: "2"
                    memory: "4Gi"

    # Prefill: Prompt encoding engine
    - name: prefill
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "Qwen/Qwen3-0.6B"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                  - --disaggregation-mode
                  - "prefill"
                  - --tp-size
                  - "1"
                ports:
                  - name: http
                    containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"
                volumeMounts:
                  - name: dshm
                    mountPath: /dev/shm
            volumes:
              - name: dshm
                emptyDir:
                  medium: Memory
                  sizeLimit: 30Gi

    # Decode: Token generation engine
    - name: decode
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "Qwen/Qwen3-0.6B"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                  - --disaggregation-mode
                  - "decode"
                  - --tp-size
                  - "1"
                ports:
                  - name: http
                    containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"
                volumeMounts:
                  - name: dshm
                    mountPath: /dev/shm
            volumes:
              - name: dshm
                emptyDir:
                  medium: Memory
                  sizeLimit: 30Gi
```

#### Service Discovery Between Roles

In a multi-role architecture, the Router typically needs to be configured with the addresses of downstream inference instances. RBG Controller automatically creates a **Headless Service** for each role, and the Router can access Prefill and Decode instances directly via DNS addresses. The DNS address naming convention is as follows:

```plain
{rbgName}-{roleName}-{ordinal}.s-{rbgName}-{roleName}.{namespace}.svc.cluster.local
```

Taking the PD-disaggregated deployment above as an example (RBG name `pd-inference`, namespace `default`), the Router's startup parameters reference these DNS addresses:

```yaml
# Service discovery addresses in Router startup parameters
- --prefill
- "http://pd-inference-prefill-0.s-pd-inference-prefill:8000"
- --decode
- "http://pd-inference-decode-0.s-pd-inference-decode:8000"
```

Corresponding DNS address resolution:

| Instance | DNS Address |
| --- | --- |
| Prefill instance 0 | `pd-inference-prefill-0.s-pd-inference-prefill.default.svc.cluster.local` |
| Decode instance 0 | `pd-inference-decode-0.s-pd-inference-decode.default.svc.cluster.local` |

> **Note**: The Headless Service (`s-{rbgName}-{roleName}`) is automatically created and managed by RBG Controller, no manual configuration needed. The Service's `publishNotReadyAddresses: true` ensures DNS records are resolvable even when Pods are not ready. When `replicas` is greater than 1, each instance's address increments by `{roleName}-{ordinal}` (e.g., `prefill-0`, `prefill-1`, ...), and can be configured individually in the Router's startup parameters.
>

#### Parameter Description (PD-Disaggregated)

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `spec.roles[].name` | string | Yes | - | Role name, unique within the RBG |
| `spec.roles[].replicas` | int32 | No | 1 | Number of role instance replicas |

> **Note**: In PD-disaggregated architecture, Prefill and Decode `replicas` can be configured independently. It is generally recommended to have fewer Prefill instances (encoding is compute-intensive) and more Decode instances (generation phase can handle more requests in parallel).
>

---

## Role Topology Definition

After determining the role architecture, you also need to select a deployment pattern for each role, defining the Pod topology within role instances. RBG provides three patterns:

| Pattern | Description | Typical Scenario |
| --- | --- | --- |
| `standalonePattern` | Each instance = 1 Pod | Single GPU, frontend router |
| `leaderWorkerPattern` | Each instance = 1 Leader + N Workers | Multi-GPU tensor parallelism |
| `customComponentsPattern` | Each instance = multiple heterogeneous components | Complex hybrid orchestration |

> **Note**: The examples in scenario 1 above all use `standalonePattern`. This section focuses on how to use `leaderWorkerPattern` for multi-GPU tensor parallelism.
>

### standalonePattern: Single-Node Deployment

`standalonePattern` is the simplest pattern, where each role instance corresponds to a single Pod. Suitable for scenarios where the model can be loaded on a single GPU.

```plain
┌────────────────────────────────────────┐
│  Role: backend                          │
│  Pattern: standalonePattern             │
│  Replicas: 2                            │
│                                         │
│  ┌───────────┐    ┌───────────┐        │
│  │ Instance 0 │    │ Instance 1 │        │
│  │ 1 Pod      │    │ 1 Pod      │        │
│  │ (1 GPU)    │    │ (1 GPU)    │        │
│  └───────────┘    └───────────┘        │
└────────────────────────────────────────┘
```

The scenario 1 example above has already demonstrated the usage of `standalonePattern`. The core fields are as follows:

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `spec.roles[].standalonePattern` | object | Yes (one of three) | - | Single-node mode, 1 Pod per instance |
| `spec.roles[].standalonePattern.template` | object | Yes | - | Pod template, follows standard Kubernetes `PodTemplateSpec` |

### leaderWorkerPattern: Multi-Node Tensor Parallelism

When the model is too large for a single GPU's memory to load the complete model, use `leaderWorkerPattern` for tensor parallelism. Each role instance consists of a group of Pods: 1 Leader + N Workers, with each Pod bound to one GPU.

```plain
┌──────────────────────────────────────────────────────┐
│  Role: backend                                        │
│  Pattern: leaderWorkerPattern                         │
│  Replicas: 2, Size: 2                                 │
│                                                       │
│  ┌──────────────────────┐  ┌──────────────────────┐  │
│  │ Instance 0            │  │ Instance 1            │  │
│  │ ┌────────┐ ┌────────┐ │  │ ┌────────┐ ┌────────┐ │  │
│  │ │ Leader  │ │ Worker  │ │  │ │ Leader  │ │ Worker  │ │  │
│  │ │ (1 GPU) │ │ (1 GPU) │ │  │ │ (1 GPU) │ │ (1 GPU) │ │  │
│  │ └────────┘ └────────┘ │  │ └────────┘ └────────┘ │  │
│  │     ←─ Tensor Parallel ─→    │  │     ←─ Tensor Parallel ─→    │  │
│  └──────────────────────┘  └──────────────────────┘  │
└──────────────────────────────────────────────────────┘
```

#### YAML Example

Taking aggregated deployment + tensor parallelism as an example:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: agg-tp-inference
spec:
  roles:
    - name: backend
      replicas: 1
      leaderWorkerPattern:
        size: 2   # Total 2 Pods: 1 Leader + 1 Worker
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "Qwen/Qwen3-0.6B"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                  - --tp-size
                  - "2"
                  # RBG auto-injected environment variables for distributed initialization
                  - --dist-init-addr
                  - $(RBG_LWP_LEADER_ADDRESS):6379
                  - --nnodes
                  - $(RBG_LWP_GROUP_SIZE)
                  - --node-rank
                  - $(RBG_LWP_WORKER_INDEX)
                ports:
                  - name: http
                    containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"
                volumeMounts:
                  - name: dshm
                    mountPath: /dev/shm
            volumes:
              - name: dshm
                emptyDir:
                  medium: Memory
                  sizeLimit: 30Gi
```

#### Parameter Description (leaderWorkerPattern)

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `leaderWorkerPattern.size` | int32 | No | 1 | Total number of Pods per instance (including Leader). Set to N for 1 Leader + (N-1) Workers |
| `leaderWorkerPattern.template` | object | Yes | - | Base Pod template shared by all Pods (Leader and Workers) |
| `leaderWorkerPattern.leaderTemplatePatch` | object | No | - | Strategic Merge Patch applied only to Leader Pod |
| `leaderWorkerPattern.workerTemplatePatch` | object | No | - | Strategic Merge Patch applied only to Worker Pod |
| `leaderWorkerPattern.restartPolicy` | object | No | `type: RecreateRoleInstanceOnPodRestart` | Restart policy config: `type` (`None` or `RecreateRoleInstanceOnPodRestart`), `baseDelaySeconds` (default: 30), `maxDelaySeconds` (default: 600) |

#### RBG Auto-Injected Environment Variables

When using `leaderWorkerPattern`, RBG Controller automatically injects the following environment variables into each Pod. Inference engines can use these variables for distributed initialization — specific parameter names vary by engine.

| Environment Variable | Description |
| --- | --- |
| `$(RBG_LWP_LEADER_ADDRESS)` | Network address of the Leader Pod |
| `$(RBG_LWP_GROUP_SIZE)` | Total number of Pods in the instance (= size) |
| `$(RBG_LWP_WORKER_INDEX)` | Index of the current Pod (Leader = 0) |

> **Important**: The inference engine's tensor parallelism degree must match `leaderWorkerPattern.size`, with each Pod requesting 1 GPU.
>

#### PD Disaggregation + Tensor Parallelism Example

`leaderWorkerPattern` also applies to inference roles in PD-disaggregated architecture. Router uses `standalonePattern` (no GPU needed), while Prefill and Decode use `leaderWorkerPattern`:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-tp-inference
spec:
  roles:
    - name: router
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: router
                image: lmsysorg/sglang-router:v0.2.4
                command:
                  - python3
                  - -m
                  - sglang_router.launch_router
                  - --pd-disaggregation
                  - --prefill
                  - "http://pd-tp-inference-prefill-0.s-pd-tp-inference-prefill:8000"
                  - --decode
                  - "http://pd-tp-inference-decode-0.s-pd-tp-inference-decode:8000"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                ports:
                  - name: http
                    containerPort: 8000

    # Prefill: 2 instances, each with 4 GPU tensor parallelism
    - name: prefill
      replicas: 2
      leaderWorkerPattern:
        size: 4   # 1 Leader + 3 Workers = 4 GPU
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "Qwen/Qwen3-0.6B"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                  - --disaggregation-mode
                  - "prefill"
                  - --tp-size
                  - "4"
                  - --dist-init-addr
                  - $(RBG_LWP_LEADER_ADDRESS):6379
                  - --nnodes
                  - $(RBG_LWP_GROUP_SIZE)
                  - --node-rank
                  - $(RBG_LWP_WORKER_INDEX)
                ports:
                  - name: http
                    containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"
                volumeMounts:
                  - name: dshm
                    mountPath: /dev/shm
            volumes:
              - name: dshm
                emptyDir:
                  medium: Memory
                  sizeLimit: 30Gi

    # Decode: 4 instances, each with 2 GPU tensor parallelism
    - name: decode
      replicas: 4
      leaderWorkerPattern:
        size: 2   # 1 Leader + 1 Worker = 2 GPU
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "Qwen/Qwen3-0.6B"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                  - --disaggregation-mode
                  - "decode"
                  - --tp-size
                  - "2"
                  - --dist-init-addr
                  - $(RBG_LWP_LEADER_ADDRESS):6379
                  - --nnodes
                  - $(RBG_LWP_GROUP_SIZE)
                  - --node-rank
                  - $(RBG_LWP_WORKER_INDEX)
                ports:
                  - name: http
                    containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"
                volumeMounts:
                  - name: dshm
                    mountPath: /dev/shm
            volumes:
              - name: dshm
                emptyDir:
                  medium: Memory
                  sizeLimit: 30Gi
```

> **Note**: Prefill and Decode `replicas` and `size` can be configured independently. Typically, Prefill has fewer instances but higher tensor parallelism (encoding is compute-intensive), while Decode has more instances but lower tensor parallelism (generation phase has lower parallelism requirements).
>

---

## Verify Deployment

```bash
# Check RBG status
kubectl get rbg

# Check RBG details
kubectl get rbg <rbg-name> -o wide

# Check Pod status
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=<rbg-name>
```

## Related Documents

<!-- TODO: The following documents have not been created yet; links will be added once the documents are complete -->

+ Using RoleTemplates to Reduce Configuration Duplication
+ Configuring HPA Autoscaling
+ Gang Scheduling Configuration
+ Rolling Updates and Canary Releases
+ PD-Disaggregated Coordinated Scaling (CoordinatedPolicy)

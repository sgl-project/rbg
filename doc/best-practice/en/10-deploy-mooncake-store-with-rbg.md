# Deploying Mooncake Store with RBG

## Overview

In LLM inference services, KV Cache is a core resource that impacts performance and cost. In scenarios such as multi-turn conversations and prefix sharing, a large amount of KV Cache can be reused, but GPU memory is limited and cannot cache all historical KV data. Mooncake Store is a distributed KV Cache storage engine that provides a cross-node distributed memory pool, serving as the L3 cache backend for inference engines, enabling KV Cache offload and cross-instance reuse.

RBG orchestrates Mooncake Store's Master and Store nodes as roles within the inference service, leveraging RBG's role dependencies, service discovery, and lossless update capabilities to achieve integrated deployment and management of Mooncake Store alongside inference engines.

> **Note**: This document focuses on the deployment and usage of Mooncake Store as a distributed KV Cache storage backend. Mooncake Transfer Engine (used for KV data transfer between Prefill and Decode in PD-disaggregated architectures) is outside the scope of this document.
>

## Prerequisites

+ Kubernetes cluster version >= 1.24
+ RBG Controller installed (refer to the [Installation Guide](https://github.com/sgl-project/rbg))
+ Mooncake Store does not require GPUs; it only needs CPU nodes with sufficient memory

> **Note**: The following examples use the SGLang engine (`lmsysorg/sglang:v0.5.9`) to demonstrate Mooncake Store integration. Mooncake Store also supports other inference engines (such as vLLM).
>

---

## Background: Why KV Cache Offload and Reuse Are Needed

### The Role of KV Cache in Inference

LLM inference consists of two phases:

1. **Prefill**: Processes the input prompt and computes Key-Value pairs (KV Cache) for each token
2. **Decode**: Generates output tokens one by one; each generated token requires reading the KV Cache of all previous tokens

KV Cache computation is expensive, especially for long prompts. If the same prompt prefix appears across multiple requests (e.g., system prompts in multi-turn conversations, shared context), recomputing the KV Cache is wasteful.

### GPU Memory Bottleneck

KV Cache is stored in GPU memory, but memory capacity is limited:

+ A 70B model in FP16 occupies approximately 140 GB of GPU memory
+ On a single A100 80GB, the space available for KV Cache is very limited
+ When memory is insufficient, KV Cache from older requests must be evicted, and subsequent requests with the same prefix require recomputation

```plain
┌─────────────────────────────────────────────────────────┐
│                    GPU Memory Allocation                │
│                                                         │
│  ┌─────────────────┐  ┌─────────────────────────────┐  │
│  │  Model Weights   │  │  KV Cache (memory-limited)  │  │
│  │  (~140 GB)       │  │  Limited capacity, frequent │  │
│  │                  │  │  eviction                   │  │
│  └─────────────────┘  └─────────────────────────────┘  │
│                                                         │
│  Problem: Insufficient KV Cache → Low cache hit rate → High TTFT
└─────────────────────────────────────────────────────────┘
```

### Hierarchical Cache Architecture

SGLang's HiCache (Hierarchical Cache) provides a tiered caching mechanism that extends KV Cache beyond GPU memory:

```plain
┌──────────────────────────────────────────────────────────────┐
│                   Hierarchical Cache Architecture            │
│                                                              │
│  L1: GPU Memory (VRAM)                                      │
│  ├── Fastest, smallest capacity                             │
│  └── Stores KV Cache for requests currently being processed │
│           │ Eviction                                         │
│           ▼                                                  │
│  L2: CPU Memory (RAM)                                       │
│  ├── Fast, larger capacity                                  │
│  └── Stores recently evicted KV Cache                       │
│           │ Eviction                                         │
│           ▼                                                  │
│  L3: Mooncake Store (Distributed Memory Pool)               │
│  ├── Speed: RDMA/TCP network transfer                       │
│  ├── Capacity: Aggregated memory from multiple Store nodes, │
│  │   can reach hundreds of GB or even TB level              │
│  └── Stores long-term reusable KV Cache, shared across     │
│      instances                                               │
│                                                              │
│  On L3 hit: KV Cache retrieved from Mooncake Store,         │
│  avoiding recomputation                                      │
│  Effect: TTFT reduced by 90%+, throughput increased by 40%+ │
└──────────────────────────────────────────────────────────────┘
```

### Typical Beneficial Scenarios

| Scenario | Description | Without L3 Cache | With Mooncake L3 Cache |
| --- | --- | --- | --- |
| Multi-turn conversation | User sends multiple consecutive messages, sharing historical context | Each turn recomputes KV Cache for all previous tokens | Retrieves historical KV Cache from Mooncake, only computes new tokens |
| Shared system prompt | Multiple users use the same system prompt | Each request independently computes the system prompt's KV Cache | The system prompt's KV Cache is shared across all instances in Mooncake |
| Long document analysis | Multiple requests analyze the same long document | Each request must re-encode the entire document | The document's KV Cache is computed only once; subsequent requests reuse it directly |

---

## Mooncake Store Capabilities

Mooncake Store is a distributed KV Cache storage engine built on Transfer Engine, designed specifically for LLM inference:

+ **Distributed memory pool**: Memory from multiple Store nodes is aggregated into a unified storage space with horizontally scalable capacity
+ **Intelligent storage management**: The Master node handles metadata management, object space allocation, and eviction policies, specifically optimized for LLM inference workloads
+ **Multi-replica support**: Multiple replicas of the same object can be stored to alleviate hot-spot access pressure
+ **Large object striping**: Supports striping and parallel I/O for large objects, fully utilizing aggregated bandwidth from multiple NICs
+ **Flexible protocols**: Supports both TCP and RDMA transport protocols; RDMA mode enables zero-copy, low-latency transfers

### Role Composition

Mooncake Store consists of two types of roles:

| Role | Function | GPU | Description |
| --- | --- | --- | --- |
| `mooncake-master` | Metadata server | Not required | Manages storage pools, handles Store node join/leave, object space allocation, eviction policies |
| `mooncake-store` | Storage node | Not required | Contributes memory to the global storage pool; acts as passive storage only, does not initiate KV Cache operations |

---

## Why Deploy Mooncake Store with RBG

While manually deploying Mooncake Store as a standalone service is feasible, it faces the following challenges in production environments:

1. **Service discovery**: The inference engine needs to know the Mooncake Master's address; manual configuration is error-prone
2. **Startup ordering**: Store nodes can only start after the Master is ready; the inference engine can only connect after the Master is ready
3. **Integrated updates**: When updating the inference engine image, Mooncake Store's KV Cache data should be preserved to avoid cache cold starts
4. **Lifecycle management**: Mooncake Store and the inference engine should be deployed, scaled, and cleaned up as a unified whole

RBG addresses all these issues through role dependencies, automatic service discovery, and lossless updates:

```plain
┌──────────────────────────────────────────────────────────────────┐
│  RoleBasedGroup (Unified Management)                             │
│                                                                  │
│  ┌─────────────────┐                                             │
│  │ mooncake-master  │  ← No dependencies, starts first           │
│  │ (CPU, 1 replica) │                                             │
│  └────────┬────────┘                                             │
│           │ Once ready                                            │
│           ├──→ ┌─────────────────┐                               │
│           │    │ mooncake-store   │  ← Depends on master         │
│           │    │ (CPU, 3 replicas)│                               │
│           │    └─────────────────┘                               │
│           │                                                      │
│           └──→ ┌─────────────────┐                               │
│                │ worker           │  ← Depends on master         │
│                │ (GPU, inference  │                               │
│                │  engine)         │                               │
│                └─────────────────┘                               │
│                                                                  │
│  Service Discovery: RBG auto-creates Headless Service,           │
│  roles communicate via DNS                                       │
│  Lossless Update: InPlaceIfPossible preserves KV Cache data      │
└──────────────────────────────────────────────────────────────────┘
```

---

## Deploying Mooncake Store Service

When multiple inference services need to share the same KV Cache storage pool, Mooncake Store can be deployed as a standalone RBG, and each inference service connects via Service references.

### Deploying a Standalone Mooncake Store

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: mooncake-service
spec:
  roleTemplates:
    - name: mooncake-base
      template:
        spec:
          containers:
            - name: mooncake
              image: lmsysorg/sglang:v0.5.9
              env:
                - name: POD_IP
                  valueFrom:
                    fieldRef:
                      fieldPath: status.podIP

  roles:
    - name: mooncake-master
      replicas: 1
      standalonePattern:
        templateRef:
          name: mooncake-base
          patch:
            spec:
              containers:
                - name: mooncake
                  command:
                    - mooncake_master
                  args:
                    - --rpc_address
                    - $(POD_IP)
                    - --rpc_port
                    - "50051"
                    - --http_metadata_server_host
                    - $(POD_IP)
                    - --http_metadata_server_port
                    - "8080"
                    - --enable_http_metadata_server
                    - --metrics_port
                    - "9003"
                  ports:
                    - containerPort: 8080
                      name: http
                    - containerPort: 50051
                      name: rpc
                  readinessProbe:
                    initialDelaySeconds: 10
                    periodSeconds: 10
                    tcpSocket:
                      port: 8080

    - name: mooncake-store
      replicas: 3
      dependencies: ["mooncake-master"]
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: InPlaceIfPossible
          maxUnavailable: 1
          inPlaceUpdateStrategy:
            gracePeriodSeconds: 30
      standalonePattern:
        templateRef:
          name: mooncake-base
          patch:
            spec:
              containers:
                - name: mooncake
                  env:
                    - name: MOONCAKE_MASTER
                      value: "s-mooncake-service-mooncake-master:50051"
                    - name: MOONCAKE_TE_META_DATA_SERVER
                      value: "http://s-mooncake-service-mooncake-master:8080/metadata"
                    - name: MOONCAKE_GLOBAL_SEGMENT_SIZE
                      value: "10gb"
                    - name: MOONCAKE_LOCAL_BUFFER_SIZE
                      value: "0"
                    - name: MOONCAKE_PROTOCOL
                      value: rdma
                    - name: MOONCAKE_DEVICE
                      value: ""
                    - name: MC_GID_INDEX
                      value: "3"
                    - name: MOONCAKE_LOCAL_HOSTNAME
                      value: $(POD_IP)
                  command:
                    - python3
                    - -m
                    - mooncake.mooncake_store_service
                  args:
                    - --port
                    - "8088"
                  ports:
                    - containerPort: 8088
                      name: http
                  readinessProbe:
                    initialDelaySeconds: 10
                    periodSeconds: 10
                    tcpSocket:
                      port: 8088
                  resources:
                    requests:
                      memory: "16Gi"
                      rdma/hca: 1
                    limits:
                      memory: "16Gi"
                      rdma/hca: 1
```

### Inference Service Connecting to a Standalone Mooncake Store

In the inference service's RBG, point `MOONCAKE_MASTER` and `MOONCAKE_TE_META_DATA_SERVER` to the standalone Mooncake service's Headless Service address:

```yaml
# Worker role configuration in the inference service RBG
- name: worker
  replicas: 1
  standalonePattern:
    template:
      spec:
        containers:
          - name: engine
            image: lmsysorg/sglang:v0.5.5
            env:
              # Point to the standalone Mooncake service address
              - name: MOONCAKE_MASTER
                value: "s-mooncake-service-mooncake-master:50051"
              - name: MOONCAKE_TE_META_DATA_SERVER
                value: "http://s-mooncake-service-mooncake-master:8080/metadata"
              - name: MOONCAKE_GLOBAL_SEGMENT_SIZE
                value: "0"
              - name: MOONCAKE_LOCAL_BUFFER_SIZE
                value: "16777216"
              - name: MOONCAKE_PROTOCOL
                value: "tcp"
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
              - --enable-hierarchical-cache
              - --hicache-storage-backend
              - mooncake
            securityContext:
              capabilities:
                add:
                - IPC_LOCK
              privileged: true
            resources:
              requests:
                nvidia.com/gpu: "1"
              limits:
                nvidia.com/gpu: "1"
```

---

## RDMA Mode Configuration

For production environments, using the RDMA protocol is recommended for lower transfer latency and higher bandwidth. Enabling RDMA requires configuring the following parameters in both Store and inference engine:

```yaml
# Both Store and inference engine need this configuration
env:
  - name: MOONCAKE_PROTOCOL
    value: "rdma"
  - name: MOONCAKE_DEVICE
    value: ""  # Leave empty for auto-discovery, or specify e.g. "mlx5_0,mlx5_1"
resources:
  limits:
    rdma/hca: "1"  # Request RDMA device
```

> **Note**: Using RDMA mode requires cluster nodes equipped with RDMA NICs (e.g., Mellanox ConnectX), and the NVIDIA Network Operator installed to provide `rdma/hca` device resources. You can check available RDMA devices with the `ibv_devices` command.
>

---

## KV Cache Capacity Planning

The total cache capacity of Mooncake Store = number of Store replicas × `MOONCAKE_GLOBAL_SEGMENT_SIZE` per Store.

| Configuration | Total L3 Cache Capacity | Applicable Scenarios |
| --- | --- | --- |
| 3 replicas × 10 GB | 30 GB | Lightweight multi-turn conversations, small models |
| 3 replicas × 50 GB | 150 GB | Medium-scale production environments |
| 6 replicas × 50 GB | 300 GB | Large-scale multi-tenant shared cache |

> **Note**: Each Store node's `resources.limits.memory` should be greater than or equal to `MOONCAKE_GLOBAL_SEGMENT_SIZE` to ensure the container has sufficient memory to hold the allocated cache space.
>

---

## Testing and Verifying KV Cache Offloading and Reuse

### Step 1: Verify Mooncake Store Readiness

Check the Master logs to confirm that Store nodes have joined the storage pool:

```bash
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master
```

Expected output:

```plain
Master Metrics: Storage: 0 B / 30.00 GB (0.0%) | Keys: 0 (soft-pinned: 0)
```

`30.00 GB` indicates that 3 Store nodes each contribute 10 GB, totaling 30 GB of L3 cache space.

### Step 2: Send Requests and Verify KV Cache Offload

Send multi-turn conversation requests to the inference service:

```bash
# Port forward to the inference service
kubectl port-forward svc/inference-with-mooncake 8000:8000

# First turn
curl -s http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Qwen/Qwen3-0.6B",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Explain the theory of relativity in detail."}
    ],
    "max_tokens": 200
  }'

# Second turn (reuses first turn's KV Cache)
curl -s http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Qwen/Qwen3-0.6B",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Explain the theory of relativity in detail."},
      {"role": "assistant", "content": "The theory of relativity consists of two parts..."},
      {"role": "user", "content": "Can you give a real-world example?"}
    ],
    "max_tokens": 200
  }'
```

Check the Master logs again to confirm that KV Cache has been offloaded to the storage pool:

```bash
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master
```

Expected output:

```plain
Master Metrics: Mem Storage: 26.58 MB / 100.00 GB (0.0%)
```

Non-zero `Storage` and `Keys` values indicate that KV Cache has been successfully offloaded to Mooncake Store.

### Step 3: Benchmark to Verify Performance Improvement

Use SGLang's built-in `bench_serving` tool to run multi-turn conversation benchmarks, comparing SLO metrics with and without Mooncake:

```bash
python3 -m sglang.bench_serving --backend sglang-oai \
  --num-prompts 300 --random-input 2048 \
  --random-output 512 --random-range-ratio 0.5 \
  --host 0.0.0.0 --port 8000 --model Qwen/Qwen3-0.6B \
  --dataset-path ShareGPT_V3_unfiltered_cleaned_split.json \
  --request-rate 10 --seed 43
```

#### Expected Results

Benchmark data based on KEP-74 (Qwen3-32B, 3 Store × 10 GiB L3 cache):

| Metric | Without Mooncake | With Mooncake | Improvement |
| --- | --- | --- | --- |
| **First-turn TTFT** | 4808 ms | 5017 ms | Roughly the same (no cache to reuse on first turn) |
| **Multi-turn TTFT** | 1172 ms | 94 ms | **-91.94%** |
| **Multi-turn ITL** | 140 ms | 58 ms | **-58.83%** |
| **Multi-turn throughput** | 1385 tok/s | 1936 tok/s | **+39.80%** |

> **Note**: The first turn has no reusable KV Cache, so performance is roughly the same. In multi-turn conversations, the historical context's KV Cache is retrieved from the Mooncake L3 cache, avoiding recomputation and significantly reducing TTFT.
>

### Step 4: Verify KV Cache Cross-Instance Reuse (Optional)

Deploy multiple inference engine replicas and verify KV Cache sharing across different instances:

```bash
# Scale worker replicas to 2
kubectl patch rbg inference-with-mooncake --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/2/replicas", "value": 2}]'
```

Send requests with the same prefix to different instances and observe the `Keys` changes in the Master logs. If the number of Keys does not increase proportionally, it indicates that multiple instances are sharing the same KV Cache rather than computing independently.

---

## Verify Deployment

```bash
# Check RBG status
kubectl get rbg inference-with-mooncake

# Check Pod status for all roles
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake

# Confirm Mooncake Master is ready
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=mooncake-master

# Confirm Store node count
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=mooncake-store

# Check storage pool status in Master logs
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master | tail -5
```

## Related Documentation

+ [Deploying Inference Services with RBG](01-deploy-inference-service.md)
+ [Using RoleTemplates to Reduce Configuration Duplication](02-using-role-templates.md)
+ In-Place Upgrade and In-Place Scheduling
+ [Configuring Rolling Update Strategies](03-configuring-rolling-updates.md)

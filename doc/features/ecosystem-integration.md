# Ecosystem Integration

RoleBasedGroup integrates with various inference ecosystem components to enable advanced deployment patterns. This document covers integration with NVIDIA Dynamo and Mooncake KV Cache.

## NVIDIA Dynamo Integration

NVIDIA Dynamo is a distributed inference runtime that supports PD-disaggregated and aggregated deployment patterns. RoleBasedGroup provides native support for Dynamo SGLang runtime.

### Key Features

- **K8s-native Service Discovery**: No external etcd required for component discovery
- **PD Disaggregation**: Separate prefill and decode engines
- **Aggregated Deployment**: Single engine with all-in-one architecture
- **HPA Integration**: scalingAdapter for dynamic scaling

### Dynamo Architecture

```text
┌─────────────────────────────────────────────────────────────┐
│                     RoleBasedGroup                           │
├─────────────────────────────────────────────────────────────┤
│  processor (frontend)                                        │
│  - HTTP API endpoint                                         │
│  - Request routing                                           │
│  - Load balancing                                            │
├─────────────────────────────────────────────────────────────┤
│  prefill (worker)                                            │
│  - Prompt processing                                         │
│  - KV cache production                                       │
│  - Transfer to decode                                        │
├─────────────────────────────────────────────────────────────┤
│  decode (worker)                                             │
│  - Token generation                                          │
│  - KV cache consumption                                      │
│  - Response streaming                                        │
└─────────────────────────────────────────────────────────────┘
```

### Deployment Example: PD-Disaggregated

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: dynamo-pd-inference
spec:
  roleTemplates:
    - name: dynamo-base
      template:
        spec:
          containers:
            - name: sglang
              image: nvcr.io/nvidia/ai-dynamo/sglang-runtime:1.0.1
              env:
                - name: DYN_DISCOVERY_BACKEND
                  value: kubernetes

  roles:
    - name: processor
      replicas: 1
      standalonePattern:
        templateRef:
          name: dynamo-base
          patch:
            spec:
              containers:
                - name: sglang
                  command: ["python3", "-m", "dynamo.frontend"]

    - name: prefill
      replicas: 1
      scalingAdapter:
        enable: true
      standalonePattern:
        templateRef:
          name: dynamo-base
          patch:
            spec:
              containers:
                - name: sglang
                  command: ["python3", "-m", "dynamo.sglang"]
                  args:
                    - --disaggregation-mode=prefill
                    - --model-path=Qwen/Qwen3-0.6B

    - name: decode
      replicas: 1
      scalingAdapter:
        enable: true
      standalonePattern:
        templateRef:
          name: dynamo-base
          patch:
            spec:
              containers:
                - name: sglang
                  command: ["python3", "-m", "dynamo.sglang"]
                  args:
                    - --disaggregation-mode=decode
                    - --model-path=Qwen/Qwen3-0.6B
```

### Prerequisites

1. **Dynamo Runtime Image**: `nvcr.io/nvidia/ai-dynamo/sglang-runtime:1.0.1`
2. **GPU Nodes**: CUDA-capable nodes with NVIDIA drivers
3. **NATS Service**: Optional, for advanced KV routing features
4. **DynamoWorkerMetadata CRD**: For K8s-native service discovery
5. **RBAC Configuration**: Service account with discovery permissions

### K8s-Native Service Discovery

Dynamo supports Kubernetes-native service discovery without requiring external etcd:

```yaml
env:
  - name: DYN_DISCOVERY_BACKEND
    value: kubernetes
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: POD_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
  - name: POD_UID
    valueFrom:
      fieldRef:
        fieldPath: metadata.uid
```

Services must have specific labels for discovery:

```yaml
metadata:
  labels:
    nvidia.com/dynamo-discovery-backend: kubernetes
    nvidia.com/dynamo-discovery-enabled: "true"
    nvidia.com/dynamo-component-type: prefill
    nvidia.com/dynamo-namespace: default
```

## Mooncake KV Cache Integration

Mooncake is a distributed KV cache system that enables KV cache reuse across inference instances, improving throughput and reducing latency.

### Mooncake Architecture

```text
┌─────────────────────────────────────────────────────────────┐
│                   Mooncake Service                           │
├─────────────────────────────────────────────────────────────┤
│  mooncake-master                                             │
│  - Metadata server                                           │
│  - KV cache management                                       │
│  - RPC endpoint (port 50051)                                 │
│  - HTTP metadata endpoint (port 8080)                        │
├─────────────────────────────────────────────────────────────┤
│  mooncake-store                                              │
│  - Distributed storage nodes                                 │
│  - KV cache persistence                                      │
│  - Horizontal scaling                                        │
└─────────────────────────────────────────────────────────────┘
```

### Standalone Mooncake Service

Deploy a shared Mooncake cluster for multiple inference services:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: mooncake-service
spec:
  roleTemplates:
    - name: sglang-base
      template:
        spec:
          containers:
            - name: sglang
              image: lmsysorg/sglang:v0.5.5

  roles:
    - name: mooncake-master
      replicas: 1
      standalonePattern:
        templateRef:
          name: sglang-base
          patch:
            spec:
              containers:
                - name: sglang
                  command: ["mooncake_master"]
                  args:
                    - --rpc_address=$(POD_IP)
                    - --rpc_port=50051
                    - --http_metadata_server_port=8080

    - name: mooncake-store
      replicas: 3
      dependencies: ["mooncake-master"]
      standalonePattern:
        templateRef:
          name: sglang-base
          patch:
            spec:
              containers:
                - name: sglang
                  env:
                    - name: MOONCAKE_MASTER
                      value: "s-mooncake-service-mooncake-master:50051"
                  command: ["python3", "-m", "mooncake.mooncake_store_service"]
```

### Using Mooncake in Inference RBG

Configure inference pods to use the Mooncake service:

```yaml
env:
  - name: MOONCAKE_MASTER
    value: "s-mooncake-service-mooncake-master:50051"
  - name: MOONCAKE_TE_META_DATA_SERVER
    value: "http://s-mooncake-service-mooncake-master:8080/metadata"
  - name: MOONCAKE_PROTOCOL
    value: "tcp"  # or "rdma" for RDMA networks
```

Enable hierarchical cache in SGLang:

```yaml
args:
  - --enable-hierarchical-cache
  - --hicache-storage-backend=mooncake
```

### Mooncake Transfer Engine

For vLLM with Mooncake KV transfer backend:

```yaml
args:
  - --kv-transfer-config
  - '{"kv_connector":"MooncakeConnector","kv_role":"kv_producer"}'  # prefill
  # or
  - '{"kv_connector":"MooncakeConnector","kv_role":"kv_consumer"}'  # decode
```

### Mooncake Integration Benefits

- **KV Cache Reuse**: Share KV cache across multiple requests
- **Reduced Latency**: Faster response for similar prompts
- **Higher Throughput**: Efficient memory utilization
- **Cost Savings**: Reduce GPU memory pressure

## vLLM Integration

vLLM can be deployed with RoleBasedGroup using standalonePattern:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: vllm-inference
spec:
  roles:
    - name: inference
      replicas: 4
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: vllm
                image: vllm/vllm-openai:latest
                command: ["vllm", "serve"]
                args:
                  - Qwen/Qwen3-0.6B
                  - --tensor-parallel-size=1
                resources:
                  limits:
                    nvidia.com/gpu: "1"
```

## SGLang Integration

SGLang can be deployed standalone or with tensor parallelism:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: sglang-tp-inference
spec:
  roles:
    - name: inference
      replicas: 2
      leaderWorkerPattern:
        size: 4  # TP=4
        template:
          spec:
            containers:
              - name: sglang
                image: lmsysorg/sglang:latest
                args:
                  - --model-path=Qwen/Qwen3-0.6B
                  - --tp-size=4
                resources:
                  limits:
                    nvidia.com/gpu: "1"
```

## Examples

### Dynamo Examples

- [Dynamo Aggregated Deployment](../../examples/inference/ecosystem/dynamo/agg.yaml)
- [Dynamo PD-Disaggregated](../../examples/inference/ecosystem/dynamo/pd-disagg.yaml)
- [Dynamo Multi-Node](../../examples/inference/ecosystem/dynamo/agg-multi-nodes.yaml)
- [Dynamo Infrastructure](../../examples/inference/ecosystem/dynamo/) (etcd, nats, rbac)

### Mooncake Examples

- [Standalone Mooncake Service](../../examples/inference/ecosystem/mooncake/mooncake-store/standalone-mooncake-store.yaml)
- [Mooncake Transfer Engine with vLLM](../../examples/inference/ecosystem/mooncake/mooncake-transfer-engine/vllm-pd-disgg-with-mooncake-te.yaml)
- [Mooncake Transfer Engine with SGLang](../../examples/inference/ecosystem/mooncake/mooncake-transfer-engine/sgl-pd-disgg-with-mooncake-te.yaml)
- [Mooncake KV Cache Reuse](../../examples/inference/ecosystem/mooncake/mooncake-store/)

### Infrastructure Examples

- [ETCD Deployment](../../examples/inference/ecosystem/etcd.yaml)
- [NATS Deployment](../../examples/inference/ecosystem/nats.yaml)
- [RBAC Configuration](../../examples/inference/ecosystem/rbac.yaml)
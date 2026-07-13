# 通过 RBG 部署 Mooncake Store

## 概述

LLM 推理服务中，KV Cache 是影响性能和成本的核心资源。多轮对话、前缀共享等场景下，大量 KV Cache 可以被复用，但 GPU 显存有限，无法缓存所有历史 KV 数据。Mooncake Store 是一个分布式 KV Cache 存储引擎，提供跨节点的分布式内存池，作为推理引擎的 L3 缓存后端，实现 KV Cache 的 offload 和跨实例复用。

RBG 将 Mooncake Store 的 Master 和 Store 节点作为推理服务中的角色进行编排，利用 RBG 的角色依赖、服务发现和无损更新能力，实现 Mooncake Store 与推理引擎的一体化部署和管理。

> **说明**：本文聚焦 Mooncake Store 作为分布式 KV Cache 存储后端的部署和使用。Mooncake Transfer Engine（用于 PD 分离架构中 Prefill 和 Decode 之间的 KV 数据传输）不在本文讨论范围。
>

## 前提条件

+ Kubernetes 集群版本 >= 1.24
+ 已安装 RBG Controller（参考 [安装指南](https://github.com/sgl-project/rbg)）
+ Mooncake Store 不需要 GPU，仅需 CPU 节点和充足内存

> **说明**：以下示例使用 SGLang 引擎（`lmsysorg/sglang:v0.5.5`）演示 Mooncake Store 的集成。Mooncake Store 也支持其他推理引擎（如 vLLM）。
>

---

## 背景：为什么需要 KV Cache Offload 和复用

### KV Cache 在推理中的作用

LLM 推理分为两个阶段：

1. **Prefill（预填充）**：处理输入 prompt，为每个 token 计算 Key-Value 对（KV Cache）
2. **Decode（解码）**：逐 token 生成输出，每生成一个 token 都需要读取之前所有 token 的 KV Cache

KV Cache 的计算开销很大，尤其是对于长 prompt。如果相同的 prompt 前缀在多个请求中出现（如多轮对话的系统提示、共享上下文），重新计算 KV Cache 是浪费。

### GPU 显存的瓶颈

KV Cache 存储在 GPU 显存中，但显存容量有限：

+ 一个 70B 模型在 FP16 下占用约 140 GB 显存
+ 单张 A100 80GB 显存中，留给 KV Cache 的空间非常有限
+ 当显存不足时，旧请求的 KV Cache 必须被驱逐，后续相同前缀的请求需要重新计算

```plain
┌─────────────────────────────────────────────────────────┐
│                    GPU 显存分配                           │
│                                                         │
│  ┌─────────────────┐  ┌─────────────────────────────┐  │
│  │  模型权重         │  │  KV Cache（受显存限制）       │  │
│  │  (~140 GB)       │  │  容量有限，频繁驱逐           │  │
│  └─────────────────┘  └─────────────────────────────┘  │
│                                                         │
│  问题：KV Cache 容量不足 → 缓存命中率低 → TTFT 高        │
└─────────────────────────────────────────────────────────┘
```

### 分层缓存架构

SGLang 的 HiCache（Hierarchical Cache）提供了分层缓存机制，将 KV Cache 扩展到 GPU 显存之外：

```plain
┌──────────────────────────────────────────────────────────────┐
│                     分层缓存架构                               │
│                                                              │
│  L1: GPU 显存 (VRAM)                                         │
│  ├── 速度最快，容量最小                                        │
│  └── 存储当前正在处理的请求的 KV Cache                          │
│           │ 驱逐                                              │
│           ▼                                                   │
│  L2: CPU 内存 (RAM)                                          │
│  ├── 速度较快，容量较大                                        │
│  └── 存储近期被驱逐的 KV Cache                                 │
│           │ 驱逐                                              │
│           ▼                                                   │
│  L3: Mooncake Store（分布式内存池）                             │
│  ├── 速度：RDMA/TCP 网络传输                                   │
│  ├── 容量：多个 Store 节点的内存聚合，可达数百 GB 甚至 TB 级       │
│  └── 存储长期可复用的 KV Cache，跨实例共享                       │
│                                                              │
│  命中 L3 时：从 Mooncake Store 取回 KV Cache，避免重新计算       │
│  效果：TTFT 降低 90%+，吞吐量提升 40%+                         │
└──────────────────────────────────────────────────────────────┘
```

### 典型受益场景

| 场景 | 说明 | 无 L3 缓存 | 有 Mooncake L3 缓存 |
| --- | --- | --- | --- |
| 多轮对话 | 用户连续发送多条消息，共享历史上下文 | 每轮重新计算之前所有 token 的 KV Cache | 从 Mooncake 取回历史 KV Cache，仅计算新增 token |
| 共享系统提示 | 多个用户使用相同的 system prompt | 每个请求独立计算 system prompt 的 KV Cache | system prompt 的 KV Cache 在 Mooncake 中被所有实例共享 |
| 长文档分析 | 多个请求分析同一篇长文档 | 每个请求都要重新编码整个文档 | 文档的 KV Cache 只需计算一次，后续请求直接复用 |

---

## Mooncake Store 的能力

Mooncake Store 是一个基于 Transfer Engine 构建的分布式 KV Cache 存储引擎，专为 LLM 推理设计：

+ **分布式内存池**：多个 Store 节点的内存聚合为一个统一的存储空间，容量可水平扩展
+ **智能存储管理**：Master 节点负责元数据管理、对象空间分配和驱逐策略，针对 LLM 推理工作负载进行了专门优化
+ **多副本支持**：同一对象可存储多个副本，缓解热点访问压力
+ **大对象条带化**：支持大对象的 striping 和并行 I/O，充分利用多网卡聚合带宽
+ **协议灵活**：支持 TCP 和 RDMA 两种传输协议，RDMA 模式下可实现零拷贝、低延迟传输

### 角色组成

Mooncake Store 由两类角色组成：

| 角色 | 功能 | GPU | 说明 |
| --- | --- | --- | --- |
| `mooncake-master` | 元数据服务器 | 不需要 | 管理存储池、处理 Store 节点的加入/离开、对象空间分配、驱逐策略 |
| `mooncake-store` | 存储节点 | 不需要 | 贡献内存到全局存储池，仅作为被动存储，不发起 KV Cache 操作 |

---

## 为什么通过 RBG 部署 Mooncake Store

将 Mooncake Store 作为独立服务手动部署虽然可行，但在生产环境中面临以下问题：

1. **服务发现**：推理引擎需要知道 Mooncake Master 的地址，手动配置容易出错
2. **启动顺序**：Store 节点必须在 Master 就绪后才能启动，推理引擎必须在 Master 就绪后才能连接
3. **一体化更新**：更新推理引擎镜像时，Mooncake Store 的 KV Cache 数据需要保留，避免缓存冷启动
4. **生命周期管理**：Mooncake Store 和推理引擎应该作为一个整体进行部署、扩缩和清理

RBG 通过角色依赖、自动服务发现和无损更新，解决上述所有问题：

```plain
┌──────────────────────────────────────────────────────────────────┐
│  RoleBasedGroup (一体化管理)                                      │
│                                                                  │
│  ┌─────────────────┐                                             │
│  │ mooncake-master  │  ← 无依赖，最先启动                          │
│  │ (CPU, 1 副本)    │                                             │
│  └────────┬────────┘                                             │
│           │ 就绪后                                                │
│           ├──→ ┌─────────────────┐                               │
│           │    │ mooncake-store   │  ← 依赖 master               │
│           │    │ (CPU, 3 副本)    │                               │
│           │    └─────────────────┘                               │
│           │                                                      │
│           └──→ ┌─────────────────┐                               │
│                │ worker           │  ← 依赖 master               │
│                │ (GPU, 推理引擎)   │                               │
│                └─────────────────┘                               │
│                                                                  │
│  服务发现：RBG 自动创建 Headless Service，各角色通过 DNS 互访       │
│  无损更新：InPlaceIfPossible 保留 KV Cache 数据                    │
└──────────────────────────────────────────────────────────────────┘
```

---

## 部署 Mooncake Store 服务

当多个推理服务需要共享同一个 KV Cache 存储池时，可以将 Mooncake Store 部署为独立的 RBG，各推理服务通过 Service 引用连接。

### 部署独立的 Mooncake Store

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

### 推理服务连接独立的 Mooncake Store

在推理服务的 RBG 中，将 `MOONCAKE_MASTER` 和 `MOONCAKE_TE_META_DATA_SERVER` 指向独立 Mooncake 服务的 Headless Service 地址：

```yaml
# 推理服务 RBG 中的 worker 角色配置
- name: worker
  replicas: 1
  standalonePattern:
    template:
      spec:
        containers:
          - name: engine
            image: lmsysorg/sglang:v0.5.5
            env:
              # 指向独立 Mooncake 服务的地址
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

## RDMA 模式配置

对于生产环境，推荐使用 RDMA 协议以获得更低的传输延迟和更高的带宽。启用 RDMA 需要在 Store 和推理引擎中配置以下参数：

```yaml
# Store 和推理引擎均需配置
env:
  - name: MOONCAKE_PROTOCOL
    value: "rdma"
  - name: MOONCAKE_DEVICE
    value: ""  # 留空自动发现，或指定如 "mlx5_0,mlx5_1"
resources:
  limits:
    rdma/hca: "1"  # 请求 RDMA 设备
```

> **说明**：使用 RDMA 模式需要集群节点配备 RDMA 网卡（如 Mellanox ConnectX），并安装 NVIDIA Network Operator 以提供 `rdma/hca` 设备资源。可通过 `ibv_devices` 命令查看可用的 RDMA 设备。
>

---

## KV Cache 容量规划

Mooncake Store 的总缓存容量 = Store 副本数 × 每个 Store 的 `MOONCAKE_GLOBAL_SEGMENT_SIZE`。

| 配置 | 总 L3 缓存容量 | 适用场景 |
| --- | --- | --- |
| 3 副本 × 10 GB | 30 GB | 轻量级多轮对话、小模型 |
| 3 副本 × 50 GB | 150 GB | 中等规模生产环境 |
| 6 副本 × 50 GB | 300 GB | 大规模多租户共享缓存 |

> **说明**：每个 Store 节点的 `resources.limits.memory` 应大于等于 `MOONCAKE_GLOBAL_SEGMENT_SIZE`，确保容器有足够的内存来承载分配的缓存空间。
>

---

## 测试验证 KV Cache Offloading 和复用

### 步骤 1：验证 Mooncake Store 就绪

检查 Master 日志，确认 Store 节点已加入存储池：

```bash
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master
```

预期输出：

```plain
Master Metrics: Storage: 0 B / 30.00 GB (0.0%) | Keys: 0 (soft-pinned: 0)
```

`30.00 GB` 表示 3 个 Store 节点各贡献 10 GB，总共 30 GB 的 L3 缓存空间。

### 步骤 2：发送请求并验证 KV Cache Offload

向推理服务发送多轮对话请求：

```bash
# 端口转发到推理服务
kubectl port-forward svc/inference-with-mooncake 8000:8000

# 第一轮对话
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

# 第二轮对话（复用第一轮的 KV Cache）
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

再次查看 Master 日志，确认 KV Cache 已被 offload 到存储池：

```bash
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master
```

预期输出：

```plain
Master Metrics: Mem Storage: 26.58 MB / 100.00 GB (0.0%)
```

`Storage` 和 `Keys` 的非零值说明 KV Cache 已成功 offload 到 Mooncake Store。

### 步骤 3：验证 KV Cache 跨实例复用（可选）

部署多个推理引擎副本，验证 KV Cache 在不同实例间的共享：

```bash
# 将 worker 副本数扩展到 2
kubectl patch rbg inference-with-mooncake --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/2/replicas", "value": 2}]'
```

向不同实例发送相同前缀的请求，观察 Master 日志中 `Keys` 的变化。如果 Keys 数量没有成倍增长，说明多个实例共享了同一份 KV Cache，而非各自独立计算。

---

## 验证部署

```bash
# 查看 RBG 状态
kubectl get rbg inference-with-mooncake

# 查看所有角色的 Pod 状态
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake

# 确认 Mooncake Master 已就绪
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=mooncake-master

# 确认 Store 节点数量
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=mooncake-store

# 查看 Master 日志中的存储池状态
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master | tail -5
```

## 相关文档

+ [使用 RBG 部署推理服务](01-deploy-inference-service.md)
+ [使用 RoleTemplates 减少配置重复](02-using-role-templates.md)
+ 原地升级与原地调度
+ [配置滚动更新策略](03-configuring-rolling-updates.md)

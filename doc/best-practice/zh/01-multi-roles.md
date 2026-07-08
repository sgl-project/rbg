# 使用 RBG 部署推理服务
## 概述
RoleBasedGroup（RBG）是 Kubernetes 上用于编排分布式、有状态 AI 推理工作负载的 API。RBG 将推理服务抽象为一个**基于角色的组**——由多个角色（Role）协作组成一个完整的推理服务。

部署推理服务涉及两个核心决策：

1. **角色架构**：推理服务由哪些角色组成？例如单个引擎角色，还是 Router + Prefill + Decode 三角色？
2. **角色拓扑**：每个角色内部的 Pod 拓扑是什么？单 Pod 还是 Leader + Workers 多 Pod？

本文以 [SGLang](https://github.com/sgl-project/sglang) 推理引擎为例进行演示，同样的拓扑模式适用于任意推理引擎（如 vLLM、TensorRT-LLM 等）。

## 前提条件
+ Kubernetes 集群版本 >= 1.24
+ 已安装 RBG Controller（参考 [安装指南](https://github.com/sgl-project/rbg)）
+ 集群中包含 GPU 节点（`nvidia.com/gpu` 资源可用）
+ 已安装 NVIDIA Device Plugin

> **说明**：以下示例使用 SGLang 引擎（`lmsysorg/sglang:v0.5.9`）和 SGLang Router（`lmsysorg/sglang-router:v0.2.4`）。如使用其他推理引擎，请替换为对应镜像并调整启动参数。
>

---

## 多角色定义
RBG 的核心概念是**角色（Role）**。一个推理服务可以由一个或多个角色组成，每个角色承担不同的职责。根据推理架构的不同，角色数量和分工也有所不同。

### 聚合部署：单角色
聚合部署（Aggregated）将整个推理引擎作为一个角色。Prefill（Prompt 编码）和 Decode（Token 生成）在同一个引擎实例内完成。这是最简单的部署方式。

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

#### 参数说明
| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `spec.roles[].name` | string | 是 | - | 角色名称，在 RBG 内唯一 |
| `spec.roles[].replicas` | int32 | 否 | 1 | 角色实例副本数 |


### PD 分离部署：多角色协作
PD 分离（Prefill-Decode Disaggregated）将推理过程拆分为多个角色，各司其职：

+ **Router**：请求路由器（CPU），将请求分发到 Prefill 或 Decode 实例
+ **Prefill**：Prompt 编码引擎（GPU），计算密集型
+ **Decode**：Token 生成引擎（GPU），内存密集型

三个角色在同一个 RBG 中声明，可独立扩缩容。

```plain
┌──────────────────────────────────────────────────────┐
│  RoleBasedGroup                                      │
│                                                      │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐          │
│  │ Router    │  │ Prefill   │  │ Decode    │          │
│  │ (CPU)     │  │ (GPU)     │  │ (GPU)     │          │
│  └─────┬────┘  └──────────┘  └──────────┘          │
│        │                                             │
│        ├──→ Prefill 实例                              │
│        └──→ Decode 实例                               │
└──────────────────────────────────────────────────────┘
```

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
spec:
  roles:
    # Router：请求路由器
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

    # Prefill：Prompt 编码引擎
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

    # Decode：Token 生成引擎
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

#### 角色间的服务发现
在多角色架构中，Router 通常需要配置下游推理实例的地址。RBG Controller 会自动为每个角色创建 **Headless Service**，Router 可通过 DNS 地址直接访问 Prefill 和 Decode 实例。DNS 地址命名规则如下：

```plain
{rbgName}-{roleName}-{ordinal}.s-{rbgName}-{roleName}.{namespace}.svc.cluster.local
```

以上方的 PD 分离部署为例（RBG 名称为 `pd-inference`，命名空间为 `default`），Router 的启动参数中引用了这些 DNS 地址：

```yaml
# Router 启动参数中的服务发现地址
- --prefill
- "http://pd-inference-prefill-0.s-pd-inference-prefill:8000"
- --decode
- "http://pd-inference-decode-0.s-pd-inference-decode:8000"
```

对应的 DNS 地址解析：

| 实例 | DNS 地址 |
| --- | --- |
| Prefill 实例 0 | `pd-inference-prefill-0.s-pd-inference-prefill.default.svc.cluster.local` |
| Decode 实例 0 | `pd-inference-decode-0.s-pd-inference-decode.default.svc.cluster.local` |


> **说明**：Headless Service（`s-{rbgName}-{roleName}`）由 RBG Controller 自动创建和管理，无需手动配置。Service 的 `publishNotReadyAddresses: true` 确保 DNS 记录在 Pod 未就绪时也可解析。当 `replicas` 大于 1 时，每个实例的地址按 `{roleName}-{ordinal}` 递增（如 `prefill-0`、`prefill-1`、...），在 Router 启动参数中逐个配置即可。
>

#### 参数说明
| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `spec.roles[].name` | string | 是 | - | 角色名称，在 RBG 内唯一 |
| `spec.roles[].replicas` | int32 | 否 | 1 | 角色实例副本数 |


> **说明**：在 PD 分离架构中，Prefill 和 Decode 的 `replicas` 可以独立配置。通常建议 Prefill 实例数较少（编码计算量大），Decode 实例数较多（生成阶段可并行处理更多请求）。
>

---

## 角色的拓扑定义
在确定了角色架构后，还需要为每个角色选择部署模式（Pattern），定义角色实例内部的 Pod 拓扑。RBG 提供三种 Pattern：

| Pattern | 说明 | 典型场景 |
| --- | --- | --- |
| `standalonePattern` | 每个实例 = 1 个 Pod | 单 GPU、前端路由 |
| `leaderWorkerPattern` | 每个实例 = 1 Leader + N Workers | 多 GPU 张量并行 |
| `customComponentsPattern` | 每个实例 = 多个异构组件 | 复杂混合编排 |


> **说明**：上文场景一中的示例均使用了 `standalonePattern`。本节重点介绍如何使用 `leaderWorkerPattern` 实现多 GPU 张量并行。
>

### standalonePattern：单机部署
`standalonePattern` 是最简单的模式，每个角色实例对应一个 Pod。适用于模型可以在单张 GPU 上加载的场景。

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

上面的场景一示例已演示了 `standalonePattern` 的用法。核心字段如下：

| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `spec.roles[].standalonePattern` | object | 是（三选一） | - | 单机模式，每实例 1 个 Pod |
| `spec.roles[].standalonePattern.template` | object | 是 | - | Pod 模板，遵循 Kubernetes 标准 `PodTemplateSpec` |


### leaderWorkerPattern：多机张量并行
当模型较大，单张 GPU 显存不足以加载完整模型时，使用 `leaderWorkerPattern` 实现张量并行。每个角色实例由一组 Pod 构成：1 个 Leader + N 个 Worker，每个 Pod 绑定一张 GPU。

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
│  │     ←─ 张量并行 ─→    │  │     ←─ 张量并行 ─→    │  │
│  └──────────────────────┘  └──────────────────────┘  │
└──────────────────────────────────────────────────────┘
```

#### YAML 示例
以聚合部署 + 张量并行为例：

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
        size: 2   # 总共 2 个 Pod：1 Leader + 1 Worker
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
                  # RBG 自动注入的环境变量，用于分布式初始化
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

#### 参数说明
| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `leaderWorkerPattern.size` | int32 | 否 | 1 | 每个实例的 Pod 总数（含 Leader）。设为 N 表示 1 Leader + (N-1) Workers |
| `leaderWorkerPattern.template` | object | 是 | - | 所有 Pod（Leader 和 Worker）共享的基础 Pod 模板 |
| `leaderWorkerPattern.leaderTemplatePatch` | object | 否 | - | 仅应用到 Leader Pod 的 Strategic Merge Patch |
| `leaderWorkerPattern.workerTemplatePatch` | object | 否 | - | 仅应用到 Worker Pod 的 Strategic Merge Patch |
| `leaderWorkerPattern.restartPolicy` | string | 否 | `RecreateRoleInstanceOnPodRestart` | Pod 故障时的策略：`None` 或 `RecreateRoleInstanceOnPodRestart`（重建整个实例） |


#### RBG 自动注入的环境变量
使用 `leaderWorkerPattern` 时，RBG Controller 自动向每个 Pod 注入以下环境变量。推理引擎可利用这些环境变量完成分布式初始化，具体的参数名称因引擎而异。

| 环境变量 | 说明 |
| --- | --- |
| `$(RBG_LWP_LEADER_ADDRESS)` | Leader Pod 的网络地址 |
| `$(RBG_LWP_GROUP_SIZE)` | 实例内 Pod 总数（= size） |
| `$(RBG_LWP_WORKER_INDEX)` | 当前 Pod 的序号（Leader = 0） |


> **重要**：推理引擎的张量并行度需要与 `leaderWorkerPattern.size` 保持一致，每个 Pod 请求 1 张 GPU。
>

#### PD 分离 + 张量并行示例
`leaderWorkerPattern` 同样适用于 PD 分离架构中的推理角色。Router 保持 `standalonePattern`（无需 GPU），Prefill 和 Decode 使用 `leaderWorkerPattern`：

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

    # Prefill：2 实例，每实例 4 GPU 张量并行
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

    # Decode：4 实例，每实例 2 GPU 张量并行
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

> **说明**：Prefill 和 Decode 的 `replicas` 和 `size` 可以独立配置。通常 Prefill 实例数少但张量并行度大（编码计算密集），Decode 实例数多但张量并行度小（生成阶段对并行度要求低）。
>

---

## 验证部署
```bash
# 查看 RBG 状态
kubectl get rbg

# 查看 RBG 详细信息
kubectl get rbg <rbg-name> -o wide

# 查看 Pod 状态
kubectl get pods -l app=llm-inference
```

## 相关文档
+ [使用 RoleTemplates 减少配置重复](#)
+ [配置 HPA 弹性伸缩](#)
+ [Gang 调度配置](#)
+ [滚动更新与金丝雀发布](#)
+ [PD 分离协调扩缩容（CoordinatedPolicy）](#)


# 为 RBG 服务配置弹性伸缩策略

## 概述

RBG 推理服务的弹性伸缩通过 `scalingAdapter` 实现——它为每个角色暴露一个标准的 Kubernetes Scale 子资源，使得任意伸缩策略都可以将副本数决策下发到 RBG。`scalingAdapter` 本身不提供伸缩策略，它只是一个执行通道。

不同的伸缩策略通过各自的决策逻辑驱动这个通道，主要分为两类：

+ **指标/事件驱动伸缩**：使用 HPA、KEDA 等社区伸缩器，基于 CPU/内存利用率、自定义指标或外部事件进行响应式伸缩。
+ **SLA 驱动伸缩**：使用 [RBG Planner](https://github.com/sgl-project/rbg-planner)，基于 TTFT/ITL 延迟目标、负载预测和离线性能画像进行预测式伸缩，专门针对 PD 分离推理场景设计。

```plain
伸缩策略（决策层）
┌─────────────┐  ┌─────────────┐  ┌─────────────┐
│    HPA       │  │    KEDA     │  │ RBG Planner │
│ (指标驱动)    │  │ (事件驱动)   │  │ (SLA 驱动)   │
└──────┬──────┘  └──────┬──────┘  └──────┬──────┘
       │                │                │
       └────────────────┼────────────────┘
                        │ 写入 spec.replicas
                        ▼
              ┌─────────────────────┐
              │  ScalingAdapter     │  ← 统一执行通道
              │  (Scale 子资源)      │
              └──────────┬──────────┘
                         │ 同步副本数
                         ▼
              ┌─────────────────────┐
              │  RoleBasedGroup     │  ← Pod 伸缩
              └─────────────────────┘
```

## 前提条件

+ Kubernetes 集群版本 >= 1.24
+ 已安装 RBG Controller（参考 [安装指南](https://github.com/sgl-project/rbg)）
+ Metrics Server 已安装（HPA 资源指标依赖）
+ 使用自定义指标时需要安装 Prometheus Adapter 或 KEDA

---

## ScalingAdapter：统一的伸缩执行通道

### 工作原理

RBG 的每个角色可以通过 `scalingAdapter.enable: true` 启用弹性伸缩。启用后，RBG Controller 自动创建一个 `RoleBasedGroupScalingAdapter`（简称 RBGSA）资源，该资源实现了 Kubernetes 标准的 `/scale` 子资源接口。无论是 HPA、KEDA 还是 RBG Planner，都通过这个 Scale 子资源将副本数决策下发到 RBG 角色。

```plain
HPA / KEDA / RBG Planner
        │
        │ 写入 spec.replicas
        ▼
RoleBasedGroupScalingAdapter (Scale 子资源)
        │
        │ 同步副本数
        ▼
RoleBasedGroup → 角色 Pod 伸缩
```

### 启用 ScalingAdapter

在角色的 spec 中设置 `scalingAdapter.enable: true`：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inference-cluster
spec:
  roles:
    - name: prefill
      replicas: 2
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
                resources:
                  requests:
                    cpu: "1"
                    memory: "1Gi"
                  limits:
                    cpu: "1"
                    memory: "1Gi"
```

#### 参数说明（ScalingAdapter）

| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `scalingAdapter.enable` | bool | 否 | `false` | 是否启用弹性伸缩 |
| `scalingAdapter.labels` | map[string]string | 否 | - | 附加到 RBGSA 资源的自定义标签 |

### 自动创建的 RBGSA 命名规则

当 `scalingAdapter.enable: true` 时，Controller 自动创建的 RBGSA 资源遵循以下命名规则：

```plain
<rbg-name>-<role-name>
```

例如，RBG 名为 `inference-cluster`，角色名为 `prefill`，则自动创建的 RBGSA 名称为 `inference-cluster-prefill`。

> **说明**：RBGSA 的生命周期通过 OwnerReference 绑定到 RBG。当 RBG 被删除或角色被移除时，对应的 RBGSA 会自动清理。无需手动创建 RBGSA。
>

---

## 场景一：指标驱动伸缩 — HPA

HPA（Horizontal Pod Autoscaler）是 Kubernetes 内置的伸缩器，支持基于 CPU 和内存利用率进行响应式伸缩。这是最简单的伸缩方式，适合对资源利用率有明确阈值的场景。

### 配置示例（HPA）

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: prefill-hpa
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: inference-cluster-prefill   # <rbg-name>-<role-name>
  minReplicas: 1
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
```

#### HPA 关键字段

| 字段 | 说明 |
| --- | --- |
| `scaleTargetRef.apiVersion` | 固定为 `workloads.x-k8s.io/v1alpha2` |
| `scaleTargetRef.kind` | 固定为 `RoleBasedGroupScalingAdapter` |
| `scaleTargetRef.name` | RBGSA 名称，格式为 `<rbg-name>-<role-name>` |
| `minReplicas` | 最小副本数 |
| `maxReplicas` | 最大副本数 |
| `metrics` | 伸缩指标列表 |

> **说明**：HPA 通过 RBGSA 的 `status.selector` 获取 Pod 标签选择器来采集指标数据，无需额外配置 labelSelector。
>

### GPU 推理服务的 HPA 配置建议

对于 GPU 推理服务，CPU 利用率通常不能准确反映负载情况。建议参考社区实践（如 KServe、vLLM 生产部署方案），使用自定义指标进行伸缩：

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: hpa-demo-backend
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: hpa-demo-backend       # <rbg-name>-<role-name>
  minReplicas: 1
  maxReplicas: 10
  metrics:
    # 基于推理引擎暴露的自定义指标
    - type: Pods
      pods:
        metric:
          name: inference_queue_length    # 推理请求队列长度
        target:
          type: AverageValue
          averageValue: "100"             # 每实例平均队列长度阈值
```

常见的推理服务自定义指标（需通过 Prometheus Adapter 暴露给 HPA）：

| 指标 | 说明 | 适用场景 |
| --- | --- | --- |
| `inference_queue_length` | 推理请求排队数量 | 队列驱动的伸缩 |
| `inference_tokens_per_second` | 每秒处理的 token 数 | 吞吐量驱动的伸缩 |
| `inference_kv_cache_usage` | KV Cache 使用率 | 显存驱动的伸缩 |

> **说明**：自定义指标需要推理引擎在 `/metrics` 端点暴露 Prometheus 格式的指标，并通过 [Prometheus Adapter](https://github.com/kubernetes-sigs/prometheus-adapter) 转换为 HPA 可消费的 Custom Metrics API。具体的指标名称和配置方式因推理引擎而异，请参考对应引擎的文档。
>

---

## 场景二：事件驱动伸缩 — KEDA

KEDA（Kubernetes Event-Driven Autoscaling）支持更丰富的外部指标源，适合基于消息队列深度、外部 API 延迟等指标进行伸缩。

### 配置示例（KEDA）

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: keda-demo-backend
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: keda-demo-backend
  minReplicaCount: 1
  maxReplicaCount: 10
  pollingInterval: 30
  cooldownPeriod: 300
  triggers:
    - type: prometheus
      metadata:
        serverAddress: http://prometheus.monitoring:9090   # 替换为集群中实际的 Prometheus 地址
        metricName: inference_queue_length
        query: |
          avg(sglang_num_queue_requests{rbg="keda-demo", role="backend"})
        threshold: "100"
```

#### KEDA 关键字段

| 字段 | 说明 |
| --- | --- |
| `scaleTargetRef` | 与 HPA 相同，指向 RBGSA |
| `pollingInterval` | 指标采集间隔（秒） |
| `cooldownPeriod` | 缩容冷却时间（秒），防止频繁缩容 |
| `triggers` | 触发器列表，支持 prometheus、kafka、redis 等多种类型 |

> **说明**：KEDA 的 `cooldownPeriod` 对于推理服务尤为重要。推理引擎的启动时间较长（模型加载、KV Cache 初始化），过于频繁的缩容会导致服务抖动。建议将 `cooldownPeriod` 设置为 5-10 分钟。
>

---

## 场景三：SLA 驱动伸缩 — RBG Planner

### 聚合部署：HPA/KEDA 足够

对于聚合部署（非 PD 分离）的推理服务，HPA 或 KEDA 可以满足大多数伸缩需求。推理引擎作为一个整体角色，伸缩决策相对简单——只需关注单一角色的资源利用率或自定义指标即可。

### PD 分离：HPA/KEDA 力不从心

在 PD 分离架构中，Prefill 和 Decode 是两个独立角色，各自有独立的 HPA。虽然可以通过 CoordinatedPolicy 协调伸缩进度，但这种方式的根本问题在于：**HPA/KEDA 不理解推理工作负载的特性**。

1. **Prefill 和 Decode 的资源需求不对称**：Prefill 是计算密集型（处理长 prompt），Decode 是内存密集型（逐 token 生成）。相同的请求模式下，两者的资源消耗完全不同，简单的进度协调无法解决资源配比问题
2. **响应式伸缩的滞后**：GPU 推理引擎启动需要数分钟（模型加载、KV Cache 初始化），HPA 检测到指标升高后再扩容，用户已经感知到延迟。对于 PD 分离场景，一个角色的滞后会导致整条链路阻塞
3. **缺乏 SLA 感知**：HPA 基于 CPU/内存利用率或队列深度，无法直接以 TTFT、ITL 等推理质量指标作为伸缩目标。而 PD 分离场景下，Prefill 影响 TTFT，Decode 影响 ITL，两者需要独立优化
4. **缺乏性能画像**：不同模型在不同硬件上的吞吐能力差异巨大。HPA 无法知道"处理 2048 token 的 prompt 需要多少 Prefill 实例"，只能被动等待指标升高

这些问题不是 CoordinatedPolicy 能解决的——它只控制伸缩进度的同步，不提供伸缩决策的智能。PD 分离推理需要一个**理解推理工作负载、能同时考虑 Prefill 和 Decode 关系**的伸缩策略。

### RBG Planner：为 PD 分离推理设计的智能伸缩

[RBG Planner](https://github.com/sgl-project/rbg-planner) 是一个独立的 Kubernetes Operator，专门为 PD 分离推理提供 **SLA 驱动的预测式伸缩**。它的核心算法源自 [NVIDIA Dynamo](https://github.com/ai-dynamo/dynamo) 项目，已适配为原生 Kubernetes RBG API。与 HPA/KEDA 一样，RBG Planner 通过 ScalingAdapter 的 Scale 子资源将副本数决策下发到 RBG。

与 HPA/KEDA 独立伸缩各角色不同，RBG Planner 将 Prefill 和 Decode 作为一个整体进行伸缩决策——根据请求负载的特征（输入长度、输出长度），分别计算两个角色所需的最优副本数。

RBG Planner 的工作循环：

```plain
┌──────────────────────────────────────────────────────────────────┐
│                    RBG Planner 工作循环                           │
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐           │
│  │   Observe     │─→│   Predict    │─→│   Compute    │           │
│  │              │  │              │  │              │           │
│  │ 采集实时指标   │  │ 预测下一周期  │  │ 基于性能画像  │           │
│  │ TTFT, ITL,   │  │ 请求量, ISL,  │  │ 计算所需      │           │
│  │ 请求量, ISL  │  │ OSL (ARIMA)  │  │ Prefill/     │           │
│  │              │  │              │  │ Decode 副本数  │           │
│  └──────────────┘  └──────────────┘  └──────┬───────┘           │
│                                             │                    │
│                                             ▼                    │
│  ┌──────────────┐                                                  │
│  │   Scale       │  通过 RBGSA 或直接 Patch RBG                     │
│  │              │  写入副本数                                       │
│  └──────────────┘                                                  │
│                                                                  │
│  每 N 秒执行一次（默认 180 秒）                                      │
└──────────────────────────────────────────────────────────────────┘
```

### 关键能力

| 能力 | 说明 |
| --- | --- |
| SLA 目标驱动 | 以 TTFT 和 ITL 延迟目标（毫秒）作为伸缩约束 |
| 负载预测 | 使用 ARIMA/Prophet 时间序列预测，提前感知负载变化 |
| 性能画像 | 通过离线 Profiling 获取模型在特定硬件上的吞吐能力，使用 scipy 三次插值计算精确的资源需求 |
| PD 独立伸缩 | 分别计算 Prefill 和 Decode 的最优副本数 |
| GPU 预算控制 | 设定总 GPU 上限，在预算内按比例分配资源 |
| 修正因子 | 实时对比观测值与预期值的偏差，自动修正伸缩决策 |

### 安装 RBG Planner

```bash
helm install rbg-planner oci://ghcr.io/sgl-project/charts/rbg-planner \
  -n rbg-system --create-namespace
```

### 前置条件：PD 分离推理服务

RBG Planner 要求推理服务已部署为 PD 分离架构，且两个角色都启用了 ScalingAdapter：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
  namespace: inference
spec:
  roles:
    - name: prefill
      replicas: 2
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                ports:
                  - containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"

    - name: decode
      replicas: 4
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                ports:
                  - containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"
```

### 创建 AutoScaler CR

```yaml
apiVersion: inference-extension.rolebasedgroup.io/v1alpha1
kind: AutoScaler
metadata:
  name: pd-inference          # 必须与 RBG 名称一致
  namespace: inference        # 必须与 RBG 命名空间一致
spec:
  scalingInterval: 180        # 伸缩决策间隔（秒）

  pattern:
    PDDisaggregated:
      prefill:
        roleName: prefill     # RBG 中的 Prefill 角色名
        minReplicas: 1
        maxReplicas: 10
      decode:
        roleName: decode      # RBG 中的 Decode 角色名
        minReplicas: 1
        maxReplicas: 10

  implementation:
    DynamoPlanner:
      modelName: "Qwen/Qwen3-0.6B"

      # SLA 目标（毫秒）
      ttft: 200.0             # 首 token 延迟目标
      itl: 20.0               # token 间延迟目标

      # 负载预测配置
      loadPredictor: arima     # 预测算法：arima | constant | prophet
      predictionWindow: 50     # 预测窗口大小（数据点数）

      # 修正因子
      noCorrection: false      # 是否禁用实时修正

      # Dry-run 模式（仅观测，不执行伸缩）
      dryRun: false

      profiling:
        image: "ghcr.io/sgl-project/rbg-profiler:latest"

      metricsEndpoint:
        metricSource: sglang   # 推理引擎类型：sglang | vllm | dynamo
        port: 9091             # 指标端口
```

#### 参数说明（AutoScaler）

| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `spec.scalingInterval` | int | 伸缩决策间隔（秒），默认 180 |
| `spec.pattern.PDDisaggregated.prefill.roleName` | string | Prefill 角色名称，必须与 RBG 中的角色名一致 |
| `spec.pattern.PDDisaggregated.prefill.minReplicas` | int | Prefill 最小副本数 |
| `spec.pattern.PDDisaggregated.prefill.maxReplicas` | int | Prefill 最大副本数 |
| `spec.implementation.DynamoPlanner.modelName` | string | 模型名称，用于 Profiling |
| `spec.implementation.DynamoPlanner.ttft` | float | TTFT SLA 目标（毫秒） |
| `spec.implementation.DynamoPlanner.itl` | float | ITL SLA 目标（毫秒） |
| `spec.implementation.DynamoPlanner.loadPredictor` | string | 负载预测算法：`arima`（推荐）、`constant`、`prophet` |
| `spec.implementation.DynamoPlanner.predictionWindow` | int | 预测窗口大小（历史数据点数） |
| `spec.implementation.DynamoPlanner.noCorrection` | bool | 是否禁用实时修正因子 |
| `spec.implementation.DynamoPlanner.dryRun` | bool | 仅观测不执行伸缩（用于调参验证） |
| `spec.implementation.DynamoPlanner.profiling.image` | string | Profiler 镜像地址 |
| `spec.implementation.DynamoPlanner.metricsEndpoint.metricSource` | string | 推理引擎类型 |
| `spec.implementation.DynamoPlanner.metricsEndpoint.port` | int | 推理引擎指标端口 |

### Profiling 流程

创建 AutoScaler 后，Operator 会自动执行以下流程：

```plain
1. 创建 Profiling Job（一次性任务）
   └── 使用 bench_serving 对模型进行多组参数基准测试
   └── 生成 Prefill 和 Decode 的性能画像数据
   └── 保存为 ConfigMap

2. 部署 Planner Engine（常驻 Deployment）
   └── 加载 Profiling 数据
   └── 开始观测-预测-计算-伸缩循环
```

Profiler 生成的性能画像包含：

+ **Prefill 画像**：输入序列长度（ISL）→ TTFT + 每 GPU 吞吐量（三次样条插值）
+ **Decode 画像**：KV Cache 使用率 × 上下文长度 → ITL + 每 GPU 吞吐量（二维散点插值）

> **说明**：Profiling 只需执行一次。性能画像数据存储在 ConfigMap 中，AutoScaler 重启后自动加载。如果更换模型或硬件，需要删除 AutoScaler 重新创建以触发重新 Profiling。
>

### 验证 Planner 运行状态

```bash
# 查看 AutoScaler 状态
kubectl get autoscaler -n inference

# 查看 Planner Pod 日志
kubectl logs -n inference -l app=rbg-planner

# 查看当前副本数决策
kubectl get autoscaler pd-inference -n inference -o jsonpath='{.status.prefillReplicas}{""}{.status.decodeReplicas}'

# 查看 RBG 角色副本数（由 Planner 驱动）
kubectl get rbg pd-inference -n inference -o jsonpath='{range .spec.roles[*]}{.name}{"="}{.replicas}{"\n"}{end}'
```

### 推荐的调参流程

1. **先开启 dryRun**：设置 `dryRun: true`，Planner 仅观测和计算，不执行伸缩。通过日志观察预测值和计算结果是否合理。
2. **调整 SLA 目标**：根据 dryRun 期间的观测数据，设置合理的 TTFT/ITL 目标。目标过低会导致过度扩容，过高则用户体验下降。
3. **关闭 dryRun**：确认配置合理后，设置 `dryRun: false` 启用实际伸缩。
4. **观察修正因子**：Planner 会实时计算修正因子（observed / expected），如果持续偏高说明实际性能低于画像预期，可能需要重新 Profiling。

---

## 方案选型建议

### 按部署架构选择

| 部署架构 | 推荐方案 | 理由 |
| --- | --- | --- |
| **聚合部署**（非 PD 分离） | HPA 或 KEDA | 推理引擎作为单一角色，伸缩决策简单，基于资源利用率或自定义指标即可满足需求 |
| **PD 分离** | RBG Planner | HPA/KEDA 独立伸缩各角色，无法理解 Prefill 和 Decode 之间的资源配比关系；RBG Planner 将两者作为整体进行 SLA 驱动的伸缩决策 |

### 各方案能力对比

| 维度 | HPA | KEDA | RBG Planner |
| --- | --- | --- | --- |
| 决策类型 | 响应式（指标阈值） | 响应式（外部事件） | 预测式（SLA + 负载预测） |
| 指标来源 | CPU/内存/自定义指标 | 外部指标源（Prometheus、Kafka 等） | 推理引擎 Prometheus 指标 |
| SLA 感知 | 否 | 否 | 是（TTFT/ITL） |
| 负载预测 | 否 | 否 | 是（ARIMA/Prophet） |
| 性能画像 | 否 | 否 | 是（离线 Profiling） |
| PD 协调能力 | 有限（CoordinatedPolicy 仅同步进度） | 有限（CoordinatedPolicy 仅同步进度） | 内置（整体计算 Prefill/Decode 最优配比） |
| GPU 预算控制 | 否 | 否 | 是 |
| 执行通道 | ScalingAdapter | ScalingAdapter | ScalingAdapter |
| 适用部署架构 | 聚合部署 | 聚合部署 | PD 分离 |

**关键差异**：HPA/KEDA 为每个角色独立创建伸缩器，即使配合 CoordinatedPolicy，也只能控制伸缩进度的同步，无法根据推理工作负载的特性（如 Prefill 计算密集 vs Decode 内存密集）做出智能的资源配比决策。RBG Planner 将 Prefill 和 Decode 作为一个整体，根据请求特征（输入长度、输出长度）和性能画像，分别计算两个角色的最优副本数。

---

## 验证伸缩状态

```bash
# 查看 HPA 状态
kubectl get hpa

# 查看 KEDA ScaledObject 状态
kubectl get scaledobject

# 查看 AutoScaler 状态
kubectl get autoscaler -n inference

# 查看 RBGSA 状态
kubectl get rbgsa

# 查看 RBG 各角色副本数
kubectl get rbg -o wide
```

## 相关文档

<!-- TODO: 以下文档尚未创建，待文档完成后统一添加链接 -->

+ 使用 RBG 部署推理服务
+ 使用 RoleTemplates 减少配置重复
+ 配置滚动更新策略
+ 原地升级与原地调度
+ [RBG Planner 项目](https://github.com/sgl-project/rbg-planner)

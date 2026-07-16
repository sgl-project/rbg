# 配置原地升级和原地调度策略

## 概述

AI 推理服务的升级面临一个核心挑战：**冷启动延迟**。模型权重文件通常有数十到数百 GB，首次加载到 GPU 需要数分钟；推理引擎的 CUDA kernel 预编译、共享库初始化等也会带来额外的启动开销。传统的 Pod 重建方式会将 Pod 调度到任意节点，导致所有缓存失效，升级过程耗时且对服务指标（如 TTFT）产生显著波动。

RBG 提供两个互补的特性来解决这个问题：

+ **原地升级（In-Place Update）**：当只变更容器镜像时，直接在原 Pod 上更新镜像并重启容器，Pod 不离开当前节点，所有节点级缓存自然保留。
+ **原地调度（In-Place Scheduling）**：当原地升级不可行（如 Pod Spec 变更超出镜像范围），Pod 需要重建时，通过注入节点亲和性（nodeAffinity），将新 Pod 调度回原来的节点，复用节点上的缓存资源。

两者配合使用，可以最大限度地减少升级对推理服务可用性和性能指标的影响。

## 前提条件

+ Kubernetes 集群版本 >= 1.24
+ 已安装 RBG Controller（参考 [安装指南](https://github.com/sgl-project/rbg)）

---

## 背景：推理服务的冷启动问题

理解"原地"的价值，需要先了解推理服务 Pod 启动过程中发生了什么。

### 启动阶段的资源加载

一个推理服务 Pod 从创建到就绪（Ready），通常经历以下阶段：

```plain
1. 镜像拉取（数分钟）
        |
        v
2. 容器启动（数十秒）
        |
        v
3. 模型加载（数分钟）
        |
        v
4. 引擎就绪（接收流量）
```

每个阶段涉及大量节点级资源的加载：

| 资源类型 | 来源 | 首次加载耗时 | 缓存后复用耗时 |
| --- | --- | --- | --- |
| 容器镜像 | 镜像仓库 → 节点本地存储 | 数分钟（数十 GB） | 秒级（已在本地） |
| 模型权重文件 | 远程存储 → 节点磁盘/内存 | 数分钟（数十~数百 GB） | 秒级（page cache 命中） |
| CUDA kernel 预编译 | 运行时编译 → 节点缓存 | 数十秒 | 瞬间（已编译） |
| 推理引擎初始化 | 共享库加载、GPU 上下文创建 | 数十秒 | — |


### KV Cache 复用的重要性

对于使用分布式 KV Cache 后端（如 Mooncake）的推理服务，KV Cache 数据在运行期间会积累在节点内存中。这些缓存的 KV 数据直接影响推理性能的关键指标：

+ **TTFT（Time To First Token）**：如果 KV Cache 命中，引擎可以直接复用之前的计算结果，大幅降低首 token 延迟
+ **升级后的指标波动**：如果 Pod 被调度到新节点，之前积累的 KV Cache 全部丢失，所有请求需要从头计算，TTFT 会出现明显的尖峰

**原地升级**和**原地调度**的核心价值就是最大限度地保留这些节点级状态，确保升级过程对服务指标的影响降到最低。

---

## 原地升级：容器级快速更新

原地升级是最快的更新方式——Pod 不离开当前节点，仅重启容器来应用新的镜像。所有节点级缓存（镜像、模型权重 page cache、CUDA 预编译文件、KV Cache）自然保留。

### 更新策略类型

RBG 提供两种更新策略，通过 `rollingUpdate.type` 配置：

| 策略 | 行为 | 适用场景 |
| --- | --- | --- |
| `InPlaceIfPossible` | 优先原地更新；若变更超出镜像范围，回退到重建 Pod | **推荐**，大多数场景的默认选择 |
| `RecreatePod` | 删除旧 Pod 并创建新 Pod | 需要完全重建的场景 |

> **说明**：`InPlaceOnly` 策略已废弃（Deprecated），不建议使用。请使用 `InPlaceIfPossible` 替代。


### 支持原地更新的变更范围

原地更新**仅支持**以下类型的变更：

+ 容器镜像（`image`）变更
+ 容器元数据（如 labels、annotations）变更

以下变更**不支持**原地更新，需要回退到 Pod 重建：

+ 添加或删除容器
+ 修改端口（`ports`）
+ 修改卷挂载（`volumeMounts`）
+ 修改资源请求/限制（`resources`）
+ 修改环境变量（`env`）

### 配置示例
```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inplace-update-demo
spec:
  roles:
    - name: backend
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: InPlaceIfPossible
          maxUnavailable: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.5
                command: ["sleep", "3600"]
```

#### 参数说明
| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `rollingUpdate.type` | string | 否 | `InPlaceIfPossible` | 更新策略类型 |
| `rollingUpdate.inPlaceUpdateStrategy.gracePeriodSeconds` | int32 | 否 | `0` | 原地更新前的流量排空等待时间（秒） |


### 原地更新的工作流程

当角色的容器镜像发生变更时，RBG Controller 执行以下流程：

```plain
1. 检测到 template 变更
        │
        ▼
2. 计算新旧 Spec 差异
        │
        ├── 差异仅为镜像变更 ──→ 执行原地更新
        │         │
        │         ▼
        │   3a. 设置 Pod 为 NotReady（从 Service 端点摘除）
        │         │
        │         ▼
        │   3b. 等待 gracePeriodSeconds（流量排空）
        │         │
        │         ▼
        │   3c. Patch 容器镜像，kubelet 重启容器
        │         │
        │         ▼
        │   3d. 容器就绪后恢复 Ready 状态
        │
        └── 差异超出镜像范围 ──→ 回退到 Pod 重建
```

> **说明**：原地更新期间，Pod 始终留在同一节点上。模型权重的 page cache、GPU 上下文状态、KV Cache 等节点级资源完全保留，容器重启后可以直接复用。
>

---

## Grace Period：流量排空

`gracePeriodSeconds` 控制原地更新前的等待时间。在更新镜像之前，Controller 会先将 Pod 标记为 NotReady（通过 Readiness Gate），将其从 Service 端点中摘除，然后等待指定时间再执行实际的镜像更新。

```yaml
rollingUpdate:
  type: InPlaceIfPossible
  inPlaceUpdateStrategy:
    gracePeriodSeconds: 30  # 等待 30 秒让流量排空
```

### 工作原理

1. **标记 NotReady**：Controller 将 Pod 的 `InPlaceUpdateReady` 条件设为 `False`
2. **端点摘除**：由于 Pod 不再 Ready，Kubernetes Service 自动将其从端点列表中移除
3. **等待排空**：等待 `gracePeriodSeconds` 秒，让已建立的连接处理完成
4. **执行更新**：Patch 容器镜像，kubelet 重启容器

### 如何选择 gracePeriodSeconds
| 值          | 适用场景             |
|------------|------------------|
| `0`        | 无状态请求或请求可由其他实例重试 |
| `60-300`   | 大多数推理服务，请求处理时间较短 |
| `600-1800` | 长连接或请求处理时间较长的场景  |


> **说明**：`gracePeriodSeconds` 仅在原地更新时生效。使用 `RecreatePod` 策略时，Pod 的删除遵循标准的 `terminationGracePeriodSeconds`。

---

## 原地调度：Pod 重建时的节点亲和

当原地升级不可行时（如 Pod Spec 发生了超出镜像范围的变更，或使用了 `RecreatePod` 策略），Pod 需要被删除并重建。此时，**原地调度**通过注入节点亲和性，引导新 Pod 调度回原来的节点，复用节点上已缓存的资源。

### 工作原理（原地调度）

原地调度通过两个注解在角色级别配置：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inplace-scheduling-demo
spec:
  roles:
    - name: backend
      replicas: 4
      annotations:
        rbg.workloads.x-k8s.io/role-inplace-scheduling: "Preferred"
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: RecreatePod
          maxUnavailable: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.5
                command: ["sleep", "3600"]
```

#### 配置注解
| 注解 | 值 | 说明 |
| --- | --- | --- |
| `rbg.workloads.x-k8s.io/role-inplace-scheduling` | `Preferred` | 软亲和：优先调度到历史节点，但允许调度到其他节点 |
| `rbg.workloads.x-k8s.io/role-inplace-scheduling` | `Required` | 硬亲和：必须调度到历史节点，否则 Pod 保持 Pending |
| `rbg.workloads.x-k8s.io/role-inplace-scheduling-granularity` | `Pod` | Pod 级绑定：每个 Pod 回到自己的历史节点（Stateful 模式默认） |
| `rbg.workloads.x-k8s.io/role-inplace-scheduling-granularity` | `Component` | 组件级绑定：Pod 可以调度到同组件类型曾运行的任意节点（Stateless 模式默认） |


### 模式选择：Preferred vs Required
| 模式 | 行为 | 适用场景 | 风险 |
| --- | --- | --- | --- |
| `Preferred` | 注入 `preferredDuringScheduling`（weight=100），新 Pod 优先回到历史节点，但可调度到其他节点 | 大多数生产环境 | 如果历史节点资源不足，Pod 可能调度到其他节点，缓存无法复用 |
| `Required` | 注入 `requiredDuringScheduling`，新 Pod 必须调度到历史节点 | 对缓存复用有严格要求、节点稳定性高的环境 | 如果历史节点不可用，Pod 将永远 Pending |


> **说明**：推荐使用 `Preferred` 模式。它在保证缓存复用的同时，保留了调度灵活性，避免因节点故障导致 Pod 无法调度。
>

### 绑定粒度：Pod vs Component

绑定粒度决定了"回到哪个节点"的精确程度：

**Pod 粒度**（Stateful 模式默认）：每个 Pod 回到**自己**之前运行的节点。

| Pod         | 重建前所在节点  | 重建后目标节点  | 说明          |
|-------------|----------|----------|-------------|
| `backend-0` | `node-A` | `node-A` | 精确回到自己的历史节点 |
| `backend-1` | `node-B` | `node-B` | 精确回到自己的历史节点 |

适用场景：每个 Pod 在节点上有独立的本地状态（如各自的 KV Cache 文件、模型分片）。

**Component 粒度**（Stateless 模式默认）：Pod 可以调度到同组件类型曾运行的**任意**节点。

| Pod                    | 重建前所在节点  | 重建后目标节点             | 说明                     |
|------------------------|----------|---------------------|------------------------|
| `abc-prefill-master-0` | `node-A` | `node-A` 或 `node-C` | 同组件类型 (master) 的任意历史节点 |
| `abc-prefill-worker-0` | `node-B` | `node-B` 或 `node-D` | 同组件类型 (worker) 的任意历史节点 |
| `def-prefill-master-0` | `node-C` | `node-A` 或 `node-C` | 同组件类型 (master) 的任意历史节点 |
| `def-prefill-worker-0` | `node-D` | `node-B` 或 `node-D` | 同组件类型 (worker) 的任意历史节点 |

适用场景：所有 Pod 共享相同的模型权重缓存，回到任意历史节点都可以复用。

> **说明**：在 Stateful 模式下，Pod 名称稳定，Pod 粒度可以精确匹配；在 Stateless 模式下，Pod 名称每次重建都会变化，必须使用 Component 粒度。如果未显式配置粒度，RBG 会自动根据模式选择默认值。
>

### 原地调度的工作流程
```plain
1. Pod 运行中，Controller 记录 Pod → Node 绑定关系
        │
        ▼
2. Pod 需要重建（原地升级不可行 / RecreatePod 策略）
        │
        ▼
3. 创建新 Pod 时，注入 nodeAffinity（基于历史绑定）
        │
        ▼
4. 调度器根据亲和性，将新 Pod 调度到历史节点
        │
        ▼
5. 新 Pod 复用节点上的镜像、模型权重 page cache、预编译文件
        │
        ▼
6. 服务就绪时间大幅缩短
```

---

## 配合使用：分层加速策略

原地升级和原地调度不是互斥的，而是互补的分层策略。它们共同构成了升级加速的分层方案：

| 层级 | 策略 | 触发条件 | 缓存复用效果 | 服务就绪时间 |
| --- | --- | --- | --- | --- |
| 第 1 层 | 原地升级（最快） | 仅变更镜像，容器原地重启 | Pod 不离开节点，所有缓存自然保留 | 秒级（`gracePeriodSeconds`） |
| 第 2 层 | 原地调度（次快） | Pod 需要重建，调度回历史节点 | 复用节点上的镜像、模型权重、预编译文件 | 分钟级（跳过镜像拉取和模型下载） |
| 第 3 层 | 标准调度（最慢） | Pod 调度到新节点 | 需要完整的镜像拉取、模型下载、引擎初始化 | 数分钟到十余分钟 |

### 推荐配置

将 `InPlaceIfPossible` 和原地调度同时配置，让系统自动选择最优路径：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: layered-update-demo
spec:
  roles:
    - name: backend
      replicas: 4
      annotations:
        # 原地调度：当 Pod 需要重建时，调度回历史节点
        rbg.workloads.x-k8s.io/role-inplace-scheduling: "Preferred"
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          # 原地升级：优先尝试原地更新
          type: InPlaceIfPossible
          maxUnavailable: 1
          inPlaceUpdateStrategy:
            gracePeriodSeconds: 30
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.5
                command: ["sleep", "3600"]
```

这种配置下的升级行为：

| 变更类型 | 实际路径 | 缓存复用 | 服务就绪时间 |
| --- | --- | --- | --- |
| 仅变更镜像 | 原地升级 | 全部保留 | 秒级 |
| 变更镜像 + 资源限制 | 原地升级失败 → 原地调度 | 节点级缓存复用 | 分钟级 |
| 变更端口/卷/环境变量 | 原地升级失败 → 原地调度 | 节点级缓存复用 | 分钟级 |
| 节点故障 Pod 重建 | 原地调度 | 节点级缓存复用 | 分钟级 |

---

## 验证更新状态

```bash
# 查看 RBG 状态
kubectl get rbg

# 查看 Pod 状态，确认 Pod 未被重建（AGE 不变）
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=<rbg-name> -o wide

# 查看 Pod 的事件，确认原地更新是否执行
kubectl describe pod <pod-name> | grep -A5 "Events"

# 查看节点亲和性（原地调度生效时）
kubectl get pod <pod-name> -o jsonpath='{.spec.affinity}'
```

### 判断原地更新是否成功

原地更新成功时，Pod 的 AGE 不会重置（Pod 未被删除重建），但容器的 `RESTARTS` 计数会增加。可以通过以下方式确认：

```bash
# 对比容器重启次数和 Pod AGE
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=<rbg-name> -o wide
```

如果 Pod AGE 远大于容器 RESTARTS 对应的重启时间，说明原地更新已成功执行。

## 相关文档

<!-- TODO: 以下文档尚未创建，待文档完成后统一添加链接 -->

+ 使用 RBG 部署推理服务
+ 配置滚动更新策略
+ RBG Warmup 预热


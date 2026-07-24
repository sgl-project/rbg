# 配置多角色协作策略

## 概述
在多角色推理服务（如 PD 分离架构）中，各角色独立运行，但它们的部署和升级需要保持协调。如果 Prefill 和 Decode 以不同速度创建或更新，可能导致版本不匹配、资源争抢或服务不可用。

`CoordinatedPolicy` 是一个独立的 CRD，用于定义跨角色的协作策略。它支持两种协作场景：

+ **协作伸缩**：控制多个角色在首次部署时的创建进度同步，确保各角色的副本数按比例渐进式增长
+ **协作升级**：控制多个角色在滚动更新时的进度同步，确保各角色的更新进度保持一致

`CoordinatedPolicy` **作用于当前命名空间的同名 RBG 上**，通过引用 RBG 中的角色名称来关联角色，与 RBG 的生命周期解耦——创建或删除 CoordinatedPolicy 不影响 RBG 本身。

## 前提条件
+ Kubernetes 集群版本 >= 1.24
+ 已安装 RBG Controller（参考 [安装指南](https://github.com/sgl-project/rbg)）
+ 已部署包含多个角色的 RoleBasedGroup

---

## 背景：为什么需要多角色协作
在 PD 分离架构中，Prefill（Prompt 编码）和 Decode（Token 生成）是两个独立角色，各自拥有独立的副本数和更新策略。当它们同时进行部署或升级时，如果不加以协调，会出现以下问题：

### 首次部署场景

**不协调的首次部署**

| 角色 | 部署进度 | 说明 |
| --- | --- | --- |
| Prefill | `0 → 6`（快速创建完成） | 实例已全部就绪 |
| Decode | `0 → 2`（仅创建了一半） | 实例明显不足 |

问题：

- Prefill 实例已就绪但 Decode 不足，无法形成有效的推理链路
- 先创建的 Prefill 实例空闲等待，浪费 GPU 资源

### 升级场景

**不协调的升级**

| 角色 | 更新进度 | 说明 |
| --- | --- | --- |
| Prefill | 全部更新到新版本 | 新实例已全量上线 |
| Decode | 只更新了一半 | 旧实例仍在服务 |

问题：

- 新版本的 Prefill 与旧版本的 Decode 可能存在协议不兼容
- Prefill 大量实例不可用，Decode 还在正常服务

`CoordinatedPolicy` 通过 `maxSkew` 参数控制各角色之间的进度差异，确保它们"齐头并进"。

---

## CoordinatedPolicy 基础结构
`CoordinatedPolicy` 通过 `spec.policies` 定义一组协作规则，每条规则指定一组角色和对应的策略：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: <policy-name>             # 名称与需要绑定的 RBG 相同
  namespace: <ns-name>            # 命名空间与需要绑定的 RBG 相同
spec:
  policies:
    - name: <rule-name>           # 规则名称，在 CoordinatedPolicy 内唯一
      roles:                       # 需要协作的角色名称列表
        - <role-1>
        - <role-2>
      strategy:                    # 协作策略
        scaling:                   # 协作伸缩（可选）
          ...
        rollingUpdate:             # 协作升级（可选）
          ...
```

#### 参数说明
| 参数 | 类型 | 是否必填 | 说明 |
| --- | --- | --- | --- |
| `spec.policies[].name` | string | 是 | 规则名称，在 CoordinatedPolicy 内唯一 |
| `spec.policies[].roles` | []string | 是 | 需要协作的角色名称列表，必须与 RBG 中的角色名一致 |
| `spec.policies[].strategy.scaling` | object | 否 | 协作伸缩策略（仅影响首次部署） |
| `spec.policies[].strategy.rollingUpdate` | object | 否 | 协作升级策略 |


> **说明**：`scaling` 和 `rollingUpdate` 可以同时配置，也可以单独使用。`scaling` 仅在首次部署阶段生效，部署完成后不影响后续的弹性伸缩行为。一个 CoordinatedPolicy 可以包含多条规则，分别作用于不同的角色组。
>

---

## 场景一：协作伸缩（首次部署）
在 PD 分离架构首次部署时，Prefill 和 Decode 的副本需要按比例渐进式创建。如果不加协调，一个角色可能快速创建完毕而另一个角色还在排队，导致先就绪的角色空闲等待。`CoordinatedPolicy` 的 `scaling` 策略通过 `maxSkew` 控制各角色的创建进度差异，通过 `progression` 控制创建的节奏，确保多角色副本数比例始终符合预期。

> **说明**：协作伸缩仅影响首次部署阶段的副本创建速度，不影响线上运行时的弹性伸缩行为。首次部署完成后，该策略不再生效。
>

### 配置示例
```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
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

---
# 协作伸缩策略（首次部署）
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: pd-inference
spec:
  policies:
    - name: prefill-decode-scaling
      roles:
        - prefill
        - decode
      strategy:
        scaling:
          maxSkew: "10%"
          progression: OrderScheduled
```

### 参数说明
| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `strategy.scaling.maxSkew` | intOrString | 否 | `"100%"` | 允许的角色间部署进度偏差。可以是绝对值（如 `2`）或百分比（如 `"10%"`） |
| `strategy.scaling.progression` | string | 否 | `OrderScheduled` | 创建节奏：`OrderScheduled` 或 `OrderReady` |


### maxSkew 的工作原理
`maxSkew` 控制各角色的创建进度差异。Controller 计算每个角色的部署进度（已创建的副本数 / 目标副本数），确保任意两个角色的进度差异不超过 `maxSkew`。

假设 Prefill 目标副本数为 6，Decode 目标副本数为 8，`maxSkew: "10%"`：

**协作伸缩过程（maxSkew: 10%）**

| 批次 | Prefill 进度 | Decode 进度 | 进度差异 | 是否符合 maxSkew |
| --- | --- | --- | --- | --- |
| 1 | `0 → 1`（17%） | `0 → 1`（13%） | 4% | ≤ 10%，允许继续 |
| 2 | `1 → 2`（33%） | `1 → 3`（38%） | 5% | ≤ 10%，允许继续 |

以此类推，两个角色始终保持按比例渐进式创建。如果 Prefill 创建过快，进度差异超过 10%，Controller 会暂停 Prefill 的创建，等待 Decode 跟上。

#### maxSkew 的选择
| maxSkew 值 | 行为 | 适用场景 |
| --- | --- | --- |
| `"1%"` | 几乎同步创建，进度差异极小 | 对角色间一致性要求极高的场景 |
| `"10%"` | 允许较小的进度差异 | 大多数生产环境 |
| `"50%"` | 允许较大的进度差异 | 对部署速度要求较高，对一致性要求较低 |
| `"100%"` | 不限制进度差异（默认值） | 不需要协作，各角色独立创建 |


### progression 模式对比
| 模式 | 行为 | 适用场景 |
| --- | --- | --- |
| `OrderScheduled` | Pod 被调度（分配节点）后即视为完成，继续下一批创建 | GPU 资源充足，追求部署速度 |
| `OrderReady` | 所有 Pod 完全 Ready 后才视为完成，继续下一批创建 | 需要确保新实例可用后再继续，更安全 |


> **说明**：`OrderReady` 模式下，如果 Pod 因为模型加载时间过长而迟迟不就绪，会阻塞后续角色的创建。建议配合 `startupProbe` / `readinessProbe` 合理配置初始延迟。
>

---

## 场景二：协作升级
当 RBG 的多个角色同时触发滚动更新（如更新推理引擎镜像版本）时，`CoordinatedPolicy` 的 `rollingUpdate` 策略确保各角色的更新进度保持一致，避免版本不匹配。

### 配置示例
```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
spec:
  roles:
    - name: prefill
      replicas: 7
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.6.0   # 新版本

    - name: decode
      replicas: 3
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.6.0   # 新版本

---
# 协作升级策略
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: pd-inference
spec:
  policies:
    - name: prefill-decode-rollout
      roles:
        - prefill
        - decode
      strategy:
        rollingUpdate:
          maxSkew: "10%"
          maxUnavailable: "10%"
```

### 参数说明
| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `strategy.rollingUpdate.maxSkew` | intOrString | 否 | `"100%"` | 允许的角色间更新进度偏差 |
| `strategy.rollingUpdate.maxUnavailable` | intOrString | 否 | - | 协作更新过程中，所有角色合计允许不可用的最大实例比例 |
| `strategy.rollingUpdate.partition` | intOrString | 否 | - | 协作更新的全局分区值（所有角色共享） |


### 协作升级的工作原理
假设 Prefill 有 7 个实例，Decode 有 3 个实例，`maxSkew: "10%"`：

1. **计算更新进度**：每个角色的更新进度 = 已更新实例数 / 总实例数
2. **控制进度差异**：Controller 确保 Prefill 和 Decode 的更新进度差异不超过 10%
3. **协调更新顺序**：如果 Prefill 更新过快，Controller 暂停 Prefill 的更新，等待 Decode 跟上

**协作升级过程（maxSkew: 10%）**

| 角色 | 实例数 | 更新状态 | 更新进度 |
| --- | --- | --- | --- |
| Prefill | 7 | 4/7 已更新 | 57% |
| Decode | 3 | 1/3 已更新 | 33% |

进度差异：`57% - 33% = 24%`，超过 `10%`，Controller 暂停 Prefill 的更新，优先推进 Decode 的更新。

### maxSkew 的选择
| maxSkew 值 | 行为 | 适用场景 |
| --- | --- | --- |
| `"1%"` | 几乎同步更新，进度差异极小 | 对版本一致性要求极高的场景 |
| `"10%"` | 允许较小的进度差异 | 大多数生产环境 |
| `"50%"` | 允许较大的进度差异 | 对更新速度要求较高，对一致性要求较低 |
| `"100%"` | 不限制进度差异（默认值） | 不需要协作，各角色独立更新 |


> **说明**：`maxSkew` 越小，更新速度越慢，但各角色的版本一致性越高。建议根据业务需求和集群规模选择合适的值。
>

---

## 场景三：同时配置伸缩和升级协作
`scaling` 和 `rollingUpdate` 可以在同一个 CoordinatedPolicy 中同时配置，互不冲突：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: pd-full-policy
spec:
  policies:
    - name: prefill-decode
      roles:
        - prefill
        - decode
      strategy:
        # 协作伸缩（首次部署）
        scaling:
          maxSkew: "10%"
          progression: OrderScheduled
        # 协作升级
        rollingUpdate:
          maxSkew: "10%"
          maxUnavailable: "10%"
```

> **说明**：伸缩和升级的 `maxSkew` 可以设置不同的值。例如，首次部署时允许较大偏差（`"50%"`）以追求部署速度，升级时要求严格同步（`"1%"`）以确保版本一致。
>

---

## 多规则配置
一个 CoordinatedPolicy 可以包含多条规则，分别作用于不同的角色组。这在复杂的推理服务中很有用，例如 Router + Prefill + Decode 三角色架构：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: multi-rule-policy
spec:
  policies:
    # 规则 1：Prefill 和 Decode 协作部署
    - name: engine-scaling
      roles:
        - prefill
        - decode
      strategy:
        scaling:
          maxSkew: "10%"
          progression: OrderReady

    # 规则 2：Prefill 和 Decode 协作升级
    - name: engine-rollout
      roles:
        - prefill
        - decode
      strategy:
        rollingUpdate:
          maxSkew: "5%"
```

---

## 验证
```bash
# 查看 CoordinatedPolicy 状态
kubectl get cpolicy

# 查看 CoordinatedPolicy 详细信息
kubectl get cpolicy <policy-name> -o yaml

# 查看 RBG 各角色副本数（部署进度协作）
kubectl get rbg <rbg-name> -o jsonpath='{range .spec.roles[*]}{.name}{"="}{.replicas}{"\n"}{end}'

# 查看各角色 Pod 更新状态（升级协作）
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=<rbg-name> -o wide

# 查看 CoordinatedPolicy 的 Conditions
kubectl get cpolicy <policy-name> -o jsonpath='{.status.conditions}'
```

## 相关文档
+ [使用 RBG 部署推理服务](#)
+ [配置滚动更新策略](#)
+ [为 RBG 服务配置弹性伸缩策略](#)
+ [原地升级与原地调度](#)


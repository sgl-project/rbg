## 概述
RBG 支持通过 `rolloutStrategy` 配置滚动更新策略，控制角色实例的更新方式、速率和进度。对于多角色推理服务（如 PD 分离架构），还可以通过 `CoordinatedPolicy` 实现跨角色的协作升级，确保各角色的更新进度保持一致。

> **说明**：本文档不涉及原地升级（In-Place Update）的详细机制，原地升级将在单独的文档中介绍。
>

## 前提条件
+ Kubernetes 集群版本 >= 1.24
+ 已安装 RBG Controller（参考 [安装指南](https://github.com/sgl-project/rbg)）

---

## 滚动更新基础配置
每个角色可以通过 `rolloutStrategy` 字段配置独立的滚动更新策略。当角色的 `template` 发生变化时（如更新镜像版本），RBG Controller 会按照配置的策略逐步更新实例。

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: rolling-update-demo
spec:
  roles:
    - name: backend
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
          maxSurge: 0
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                ports:
                  - containerPort: 8000
```

### 参数说明
| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `rolloutStrategy.type` | string | 否 | `RollingUpdate` | 更新策略类型，目前仅支持 `RollingUpdate` |
| `rolloutStrategy.rollingUpdate.maxUnavailable` | intOrString | 否 | `1` | 更新过程中允许不可用的最大实例数。可以是绝对值（如 `1`）或百分比（如 `"25%"`） |
| `rolloutStrategy.rollingUpdate.maxSurge` | intOrString | 否 | `0` | 更新过程中允许超出期望副本数的最大实例数。可以是绝对值或百分比 |


#### maxUnavailable 和 maxSurge 的作用
+ **maxUnavailable**: 控制更新过程中"不可用实例"的上限。值越大，更新速度越快，但服务可用性越低。
+ **maxSurge**: 控制是否允许创建额外的实例来加速更新。设为 `0` 表示先删除旧实例再创建新实例；设为正数表示可以先创建新实例再删除旧实例。

**示例组合**：

| maxUnavailable | maxSurge | 行为 | 适用场景 |
| --- | --- | --- | --- |
| `1` | `0` | 逐个替换，不创建额外实例 | 资源受限环境 |
| `1` | `1` | 先创建 1 个新实例，再删除 1 个旧实例 | 平衡可用性和更新速度 |
| `"25%"` | `"25%"` | 允许 25% 实例不可用，同时创建 25% 额外实例 | 快速更新，对可用性要求较低 |


---

## 金丝雀发布：Partition 控制
`partition` 参数允许你控制更新的进度，实现金丝雀发布。只有序号（ordinal）**大于等于** `partition` 的实例会被更新，序号小于 `partition` 的实例保持旧版本。

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: canary-deployment
spec:
  roles:
    - name: backend
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
          partition: 2  # 只更新序号 >= 2 的实例（即实例 2 和 3）
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.6.0  # 新版本
```

### 参数说明
| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `rolloutStrategy.rollingUpdate.partition` | int | 否 | `0` | 分区值。只有序号 >= partition 的实例会被更新 |


### 金丝雀发布流程
假设 `replicas: 4`，更新流程如下：

1. **设置 **`partition: 4`：所有实例保持旧版本，不开始更新
2. **设置 **`partition: 3`：只更新实例 3（最后一个），观察新版本表现
3. **设置 **`partition: 2`：更新实例 2 和 3，继续观察
4. **设置 **`partition: 0`：更新所有实例，完成发布

> **说明**：实例序号从 0 开始。`partition: 0`（默认值）表示更新所有实例。
>

---

## 暂停和恢复更新
`paused` 参数可以暂停滚动更新，允许你手动控制更新的时机。

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: paused-update
spec:
  roles:
    - name: backend
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
          paused: true  # 暂停更新
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.6.0
```

### 参数说明
| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `rolloutStrategy.rollingUpdate.paused` | bool | 否 | `false` | 是否暂停滚动更新 |


### 使用场景
+ **手动审批**：在更新模板后先暂停，确认配置无误后再恢复更新
+ **分阶段发布**：先更新部分实例（配合 `partition`），观察一段时间后继续
+ **紧急回滚**：发现问题时立即暂停更新，避免影响扩大

恢复更新时，将 `paused` 设为 `false`（或删除该字段），更新会继续执行。

---

## 高级配置：多角色协作升级
### 为什么需要协作升级？
在 PD 分离架构中，Prefill 和 Decode 是两个独立的角色，各自配置独立的滚动更新策略。如果同时触发更新，两个角色可能会以不同的速度进行更新，导致以下问题：

1. **版本不匹配**：Prefill 已经更新到新版本，但 Decode 还在使用旧版本，两者之间的协议或数据结构可能不兼容
2. **服务不可用**：如果 Prefill 更新过快，大量实例不可用，而 Decode 还没准备好接收流量，可能导致请求失败
3. **资源争抢**：两个角色同时更新可能导致集群资源（如 GPU、网络带宽）争抢，影响更新速度

**协作升级**确保 Prefill 和 Decode 的更新进度保持一致，避免上述问题。

### 配置 CoordinatedPolicy
`CoordinatedPolicy` 是一个独立的 CRD，用于定义跨角色的协作策略。它通过 `maxSkew` 参数控制各角色之间的更新进度差异。

```yaml
---
# RBG 定义
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
                image: lmsysorg/sglang:v0.6.0

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
                image: lmsysorg/sglang:v0.6.0

---
# CoordinatedPolicy 定义协作升级
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: pd-rollout-policy
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
| `spec.policies[].name` | string | 是 | - | 策略名称，在 CoordinatedPolicy 内唯一 |
| `spec.policies[].roles` | []string | 是 | - | 需要协作的角色名称列表 |
| `spec.policies[].strategy.rollingUpdate.maxSkew` | string | 否 | `"100%"` | 允许的最大进度偏差（百分比）。值越小，协作越紧密 |
| `spec.policies[].strategy.rollingUpdate.maxUnavailable` | string | 否 | - | 协作更新过程中允许不可用的最大实例比例 |


### 协作升级的工作原理
假设 Prefill 有 7 个实例，Decode 有 3 个实例，`maxSkew: "10%"`：

1. **计算更新进度**：每个角色的更新进度 = 已更新实例数 / 总实例数
2. **控制进度差异**：Controller 会确保 Prefill 和 Decode 的更新进度差异不超过 10%
3. **协调更新顺序**：如果 Prefill 更新过快，Controller 会暂停 Prefill 的更新，等待 Decode 跟上；反之亦然

**示例**：

+ Prefill 更新了 4/7 = 57% 的实例
+ Decode 更新了 1/3 = 33% 的实例
+ 进度差异 = 57% - 33% = 24% > 10%（超出 maxSkew）
+ Controller 会暂停 Prefill 的更新，优先更新 Decode，直到进度差异 <= 10%

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

## 验证更新状态
```bash
# 查看 RBG 状态
kubectl get rbg

# 查看角色实例的更新状态
kubectl get roleinstances -l rbg-name=<rbg-name>

# 查看 CoordinatedPolicy 状态
kubectl get coordinatedpolicy
```

## 相关文档
+ [使用 RBG 部署推理服务](#)
+ [使用 RoleTemplates 减少配置重复](#)
+ [原地升级（In-Place Update）](#)
+ [配置 HPA 弹性伸缩](#)


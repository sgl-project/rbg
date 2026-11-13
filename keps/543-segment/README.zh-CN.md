# KEP-543：Segment 跨 Role 服务单元

<!-- toc -->
- [KEP-543：Segment 跨 Role 服务单元](#kep-543segment-跨-role-服务单元)
  - [发布签核清单](#发布签核清单)
  - [摘要](#摘要)
  - [动机](#动机)
    - [目标](#目标)
    - [非目标](#非目标)
  - [方案](#方案)
    - [设计概述](#设计概述)
    - [约束与注意事项](#约束与注意事项)
    - [风险与缓解](#风险与缓解)
  - [设计细节](#设计细节)
    - [控制器职责](#控制器职责)
    - [API](#api)
    - [校验](#校验)
      - [Policy 准入校验](#policy-准入校验)
      - [成员模式校验](#成员模式校验)
      - [副本与执行条件校验](#副本与执行条件校验)
    - [组指派与持久化](#组指派与持久化)
      - [通过 RIS annotation 下发分组描述](#通过-ris-annotation-下发分组描述)
      - [RI 与 Pod 的 label 同步](#ri-与-pod-的-label-同步)
    - [完整 Segment 不变量](#完整-segment-不变量)
    - [与 Scaling 的集成](#与-scaling-的集成)
      - [副本目标下发](#副本目标下发)
    - [ScalingAdapter](#scalingadapter)
      - [Segment Adapter 配置与自动创建](#segment-adapter-配置与自动创建)
      - [目标与副本单位](#目标与副本单位)
      - [副本控制权与生命周期](#副本控制权与生命周期)
      - [Adapter 与 RBG 的扩缩协调闭环](#adapter-与-rbg-的扩缩协调闭环)
      - [scale 子资源与指标边界](#scale-子资源与指标边界)
    - [与 Gang 的集成](#与-gang-的集成)
      - [Volcano subGroup 映射](#volcano-subgroup-映射)
      - [与 Scaling 的配置校验](#与-scaling-的配置校验)
    - [与 Rolling Update 的集成](#与-rolling-update-的集成)
      - [RBG 与 RIS 的更新协调闭环](#rbg-与-ris-的更新协调闭环)
      - [成员可用性与预算补位](#成员可用性与预算补位)
      - [Policy 覆盖与 RIS 执行窗口](#policy-覆盖与-ris-执行窗口)
      - [maxSurge 上限与 RIS 执行](#maxsurge-上限与-ris-执行)
      - [按 Segment 生成 partition](#按-segment-生成-partition)
      - [目标版本变化时的新模板与 partition 下发](#目标版本变化时的新模板与-partition-下发)
    - [与 Topology 的组合](#与-topology-的组合)
    - [CoordinatedPolicy 的添加与删除](#coordinatedpolicy-的添加与删除)
      - [已有 RBG 后添加 Policy](#已有-rbg-后添加-policy)
      - [删除 Policy 后撤销协同约束](#删除-policy-后撤销协同约束)
      - [重新添加与规则重命名](#重新添加与规则重命名)
    - [测试计划](#测试计划)
      - [前置测试更新](#前置测试更新)
      - [单元测试](#单元测试)
        - [API 校验](#api-校验)
        - [组指派](#组指派)
        - [Policy 生命周期](#policy-生命周期)
        - [Scaling](#scaling)
        - [ScalingAdapter 状态与控制权](#scalingadapter-状态与控制权)
          - [Adapter 配置与目标提交](#adapter-配置与目标提交)
          - [Adapter 控制权与恢复](#adapter-控制权与恢复)
          - [Adapter 状态与 scale 映射](#adapter-状态与-scale-映射)
        - [Rolling update](#rolling-update)
          - [组级准入与预算恢复](#组级准入与预算恢复)
          - [更新参数与 surge 投影](#更新参数与-surge-投影)
        - [Gang](#gang)
      - [集成测试](#集成测试)
        - [成员模式与组身份](#成员模式与组身份)
        - [Adapter 目标与控制权交接](#adapter-目标与控制权交接)
        - [创建批次与 progression](#创建批次与-progression)
        - [组级更新与 surge](#组级更新与-surge)
        - [Gang 资源准入](#gang-资源准入)
        - [地址发现](#地址发现)
        - [Policy 接管与撤销](#policy-接管与撤销)
        - [controller 降级准备](#controller-降级准备)
      - [端到端测试](#端到端测试)
    - [阶段准入条件](#阶段准入条件)
      - [Alpha](#alpha)
      - [Beta](#beta)
      - [GA](#ga)
    - [升级与降级策略](#升级与降级策略)
      - [迁移与兼容](#迁移与兼容)
      - [升级](#升级)
      - [controller 降级](#controller-降级)
    - [版本差异策略](#版本差异策略)
  - [生产就绪评审](#生产就绪评审)
    - [功能启用与回滚](#功能启用与回滚)
      - [如何在运行中的集群启用或关闭功能？](#如何在运行中的集群启用或关闭功能)
      - [启用功能是否改变默认行为？](#启用功能是否改变默认行为)
      - [关闭后重新启用会怎样？](#关闭后重新启用会怎样)
      - [功能启用与关闭有哪些测试？](#功能启用与关闭有哪些测试)
    - [发布、升级与回滚计划](#发布升级与回滚计划)
      - [controller 回滚前需要完成哪些准备？](#controller-回滚前需要完成哪些准备)
      - [升级与回滚是否经过测试？](#升级与回滚是否经过测试)
    - [监控要求](#监控要求)
      - [如何判断工作负载正在使用该功能？](#如何判断工作负载正在使用该功能)
      - [如何确认实例的分组与策略执行符合预期？](#如何确认实例的分组与策略执行符合预期)
    - [依赖](#依赖)
      - [功能依赖哪些集群服务？](#功能依赖哪些集群服务)
    - [规模与资源开销](#规模与资源开销)
      - [是否增加 API 调用？](#是否增加-api-调用)
      - [是否增加 API 对象的大小或数量？](#是否增加-api-对象的大小或数量)
      - [是否增加控制器的资源开销？](#是否增加控制器的资源开销)
    - [故障排查](#故障排查)
      - [已知故障模式有哪些，如何处理？](#已知故障模式有哪些如何处理)
  - [实施历史](#实施历史)
  - [限制与代价](#限制与代价)
  - [替代方案](#替代方案)
    - [只使用现有 CoordinatedPolicy](#只使用现有-coordinatedpolicy)
    - [由应用自行管理分组](#由应用自行管理分组)
    - [将 Segment 作为调度策略](#将-segment-作为调度策略)
  - [参考资料](#参考资料)
<!-- /toc -->

## 发布签核清单

- [x] 增强 Issue：[#543](https://github.com/sgl-project/rbg/issues/543)
- [ ] KEP 评审人和批准人确认
- [ ] 设计细节已完整记录
- [ ] 测试计划已确认
- [ ] 阶段准入条件已确认
- [ ] 生产就绪评审完成
- [ ] 用户文档与示例已补充
- [ ] 实施历史持续更新

## 摘要

Segment 将多个 role 的 RoleInstance 按固定配比分组，为扩缩容、滚动更新和 Gang 调度提供共同的操作单位。例如，`2P:1D` 表示一个组包含 2 个 prefill RoleInstance 和 1 个 decode RoleInstance。

`segment` 定义在 `CoordinatedPolicy.spec.policies[]` 中，只声明成员结构；同一规则的 `strategy` 决定创建批量、调度门槛和更新预算。组身份保存在 RoleInstance 与 Pod 的受管标签中，在同一有效分组配置下保持稳定。组成员可以先后创建、启动和 Ready；更新期间允许新旧版本共存。

本文面向 KEP 评审者和控制器开发者，假定读者了解 RBG、RIS 及 Kubernetes 控制器的基本工作方式。本文是设计提案，Segment API 扩展、控制器适配和测试尚未完成；文中标注为“现有行为”的内容仅说明复用边界，其余行为是实现要求。

本文使用以下简称：

| 简称 | 含义 |
| --- | --- |
| RBG | RoleBasedGroup，保存各 role 的最终期望副本和业务配置 |
| RIS | RoleInstanceSet，管理一个 role 的 RoleInstance |
| RI | RoleInstance；一个 RI 可以包含多个 component Pod |
| Policy | 与目标 RBG 同 namespace、同名的 CoordinatedPolicy |
| Adapter | RoleBasedGroupScalingAdapter，接收外部扩缩目标 |
| P / D | prefill / decode；配比中的数量均按 RI 计，不按 Pod 计 |
| 基础组 / surge RI | 基础组由期望副本确定；surge RI 是更新期间超出 `RIS.spec.replicas` 的临时额外实例 |

## 动机

PD 分离推理需要 prefill 与 decode 配对。现有 CoordinatedPolicy 能协调多个 role，但缺少稳定的跨 role 子组：单独扩缩可能产生未配对实例，滚动更新无法统一组级批次，router 也难以识别哪些实例应共同服务。后续拓扑放置同样需要共享的子组定义。

### 目标

- 用规则级 `segment` map 声明各 role 的 RI 配额，并根据实例序号计算组归属。
- 维护 RI 与 Pod 的稳定组身份，支持已有 RBG 在不重启成员的情况下接管分组。
- 以完整基础组计算副本目标、更新批次和调度资源门槛，为后续 router 与拓扑设计提供可查询的身份；本 KEP 只提供身份，不实现路由。

### 非目标

- 自动推断配比、实现或强制启用 router 组感知路由。
- 设计完整 topology API、tier、pack 或调度器翻译。
- 保证跨对象原子操作、成员同时 Ready、无混版更新或重新分组时的应用状态无缝迁移。
- 改造非 Segment 规则的现有地址发现。Segment 感知的地址发现要求在[完整 Segment 不变量](#完整-segment-不变量)中定义。
- 新增首次创建专用 progression、“整 Segment 更新”开关、实际 surge 目标或跨 role 临时组生命周期协议。
- 设计完整 HPA 使用方案，或增加通用 Pod 选择及业务指标采集、聚合、去重职责；本期只定义 ScalingAdapter 能力。
- 适配 Stateless RIS，以及后续版本不再支持的 Deployment、StatefulSet、LWS 等 deprecated workload。
- 设计 API 降级、旧客户端兼容或旧 coordination 反向映射；本期仅定义撤销 Segment 后的 controller 降级流程。

## 方案

Segment 的结构、策略和执行分层定义：Policy 声明配比与策略，RBG 计算组级目标，RIS 和 RI 执行实例操作。

### 设计概述

以下设计示例将 `llm-serving` RBG 的 prefill、decode 按 `2P:1D` 分组。Policy 与 RBG 必须同 namespace、同名：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: llm-serving
spec:
  policies:
    - name: kv-cache
      roles: [prefill, decode]
      segment:
        prefill: 2
        decode: 1
      strategy:
        scheduling:
          gang:
            minSegments: 1
        scaling:
          segBatchSize: 1
          progression: OrderScheduled
        rollingUpdate:
          maxUnavailable: 1
          maxSurge: 0
```

期望副本为 `P=4、D=2` 时，组归属如下。实例名用于表示 role 与 ordinal：

| 组 | 成员 | RI 及其 Pod 的 `rbg.workloads.x-k8s.io/segment` 标签 |
| --- | --- | --- |
| Segment 0 | prefill-0、prefill-1、decode-0 | `e7652f82cdee-0` |
| Segment 1 | prefill-2、prefill-3、decode-1 | `e7652f82cdee-1` |

标签值遵循规范定义的 `<ruleID>-<groupIndex>` 方案。规则名 `kv-cache` 派生的确定性 rule ID 为 `e7652f82cdee`，即 `SHA-256("kv-cache")` 的前 12 位小写十六进制字符。

各策略使用相同分组，但职责不同：

| 功能 | 操作及示例 | 限制与边界 |
| --- | --- | --- |
| 扩缩容 | 从 2 组扩到 3 组，最终副本从 `P=4、D=2` 变为 `P=6、D=3`；`segBatchSize` 控制创建批量，`progression` 决定何时继续 | 不单独扩缩某个成员 role |
| 滚动更新 | 同组成员同批准入；无 surge、`maxUnavailable: 1` 时，最多允许主动更新同时占用 1 组预算 | 成员可先后执行，期间允许混版；有 surge 时按[额外可用容量](#maxsurge-上限与-ris-执行)参与预算 |
| Gang 调度 | `minSegments: 1` 要求可调度资源满足一个完整组的全部 Pod | 只做资源准入，不等待业务 Ready |
| router / topology | 消费组身份进行路由或拓扑放置 | 本 KEP 不实现路由或拓扑 API |

“同批准入”指同一 Segment 的成员进入同一轮更新范围；执行、启动和 Ready 仍可先后完成。

### 约束与注意事项

- 当前仅支持成员为原生 Stateful RoleInstanceSet 且成员序号稠密的配置。拒绝 Stateless RIS、deprecated workload、未知后端、`excluded-ordinals` 以及子 RIS 上非空的 `committed-skip-ordinals` 记录；分组概念本身不等于 Stateful 模式。
- 同一 Policy 中，Segment 成员不得出现在其他任何规则中，不区分策略域。需要共同协调的 role 应在一条规则内声明配比和策略。
- 配比在同一有效配置内不可原地修改；撤销后可按新配比重新校验、接管，不记录或限制历史配比。
- Segment 下忽略 `scaling.maxSkew` 和 `rollingUpdate.maxSkew`。创建使用批量与 progression，更新使用共同边界与组级预算。
- Segment 的 scaling 与 Gang 独立执行，执行期不调整彼此参数；Policy 准入和 RBG reconcile 校验两者是否兼容，避免首批成员不足造成持续互等。
- 非 Segment 保留现有行为；唯一新增覆盖能力是显式配置 Policy `maxSurge`，详见[参数覆盖规则](#maxsurge-上限与-ris-执行)。

### 风险与缓解

| 风险或代价 | 设计边界与处理方式 |
| --- | --- |
| 配比或规则组织限制灵活性 | 不支持单 role 扩缩、成员跨规则重叠或原地改配比；相关 role 在同一规则内协调，改配比通过撤销和重新接管完成 |
| 期望副本不满足配比 | 报告各 role 的剩余实例，并在写入任何 Segment 或 role 目标前停止整个 RBG reconcile；首次创建不生成成员目标，已生效的 Segment 保持当前合法目标，存量接管不生效，由用户修正目标 |
| 分组和业务路由不一致 | RBG 发布并维护身份，但无法验证 router 是否按组路由；重新分组不保证应用状态或路由无缝迁移 |
| 各 RIS 独立执行可能突破预算 | RBG 按组身份去重并控制共同边界；部分下发、目标变化及状态延迟必须验证 |
| 慢成员或坏版本持续占用预算 | 无 surge 时，预算耗尽允许阻塞新增组；有余量且满足其他条件时仍可补位，不自动扩大预算 |
| surge 容量被误当成完整备用服务 | 仅保证上限投影，实际数量与回收由 RIS 决定，不保证额外 RI 成组 |
| scaling 与 Gang 门槛冲突 | 静态冲突由 Policy 准入拒绝，跨资源变化由 RBG reconcile 报告；系统不自动抬升目标或降低门槛 |
| 标签被修改或同步延迟 | 控制器恢复受管字段；消费者区分标签存在与当前配置有效 |

## 设计细节

以下依次定义控制器职责、API 与校验、组身份、扩缩容、更新及配置生命周期，并列出测试计划与发布要求。实现必须同时满足组级约束和既有 RIS 执行条件。

### 控制器职责

| 组件 | 职责 |
| --- | --- |
| RBG controller | 读取 Policy；校验接管条件；计算组级副本目标、更新边界与预算；维护分组描述、Adapter 和策略生命周期 |
| WorkloadReconciler | RBG controller 内的后端适配层，将计算结果写入 RIS；不是独立控制器 |
| RIS controller | 读取本 role 的分组描述并维护 RI 标签；在下发的副本目标、partition 和局部预算内执行，不直接解析完整 Policy |
| RI controller | 管理 component Pod，同步已有 Pod 和后续 Pod template 的组身份，执行具体 Pod 更新 |
| ScalingAdapter controller | 接收最终期望组数，按配比一次提交所有成员的 RBG 副本目标并维护状态；不创建自身或直接操作 RIS、RI、Pod |

### API

Policy 字段以 `CoordinatedPolicy.spec.policies[]` 为起点。新增字段均为可选字段；下表集中说明配置位置和默认行为，执行细节见对应功能章节。

| 字段 | 类型或单位 | 默认行为与用途 |
| --- | --- | --- |
| `segment` | `map[string]int32`，RI 配额 | 未配置时按非 Segment 规则执行 |
| `strategy.scaling.segBatchSize` | 正整数，组数 | 有 Segment scaling 策略时默认 `1`；用于首次创建和扩容 |
| `strategy.scaling.scalingAdapter` | 复用现有 `ScalingAdapter` 类型 | `enable` 默认 `false`；`labels` 附加到自动创建的 Adapter |
| `strategy.scheduling.gang.minSegments` | 正整数，组数 | 未配置 scaling 时，省略表示全部期望完整组；同时配置 scaling 和 Gang 时必须显式设置；与 `minReplicas` 互斥 |
| `strategy.rollingUpdate.maxSurge` | 整数或百分比 | Segment 下未配置按 `0` 下发；非 Segment 未配置时保留 role 行为 |

现有 `strategy.scaling.maxSkew` 和 `strategy.rollingUpdate.maxSkew` 字段继续保留，但 Segment 计算忽略其值。现有更新字段的组级单位见[滚动更新](#与-rolling-update-的集成)。Adapter 资源新增的 `spec.scaleTargetRef.segment` 及副本字段语义见[目标与副本单位](#目标与副本单位)。

在 `CoordinatedPolicyRule` 中增加字段：

```go
type CoordinatedPolicyRule struct {
    // ...
    // Segment maps role name to the number of RoleInstances in one Segment.
    // It is a structural declaration and does not define rollout behavior.
    // +optional
    // +kubebuilder:validation:Type=object
    Segment map[string]int32 `json:"segment,omitempty"`
}
```

在 `ScalingCoordinationStrategy` 中增加可选字段：

```go
type ScalingCoordinationStrategy struct {
    // ...
    // SegBatchSize 表示每批新增的完整 Segment 数，仅用于 Segment 规则。
    // 未配置时按 1 处理，不支持百分比。
    // +optional
    // +kubebuilder:validation:Minimum=1
    SegBatchSize *int32 `json:"segBatchSize,omitempty"`

    // ScalingAdapter 配置当前 Segment 规则的自动扩缩适配器。
    // +optional
    ScalingAdapter *ScalingAdapter `json:"scalingAdapter,omitempty"`
}
```

`ScalingAdapter` 复用 [RBG API 中的现有类型](../../api/workloads/v1alpha2/rolebasedgroup_types.go)，不另定义相同配置。没有 `strategy.scaling` 时不自动补建策略，也不启用默认创建批量。

在 `RollingUpdateCoordinationStrategy` 中增加可选字段：

```go
type RollingUpdateCoordinationStrategy struct {
    // ...
    // MaxSurge 表示更新期间允许的额外容量上限，支持整数或百分比。
    // Segment 规则按组数解析后投影到各 role；普通规则原样覆盖 role 配置。
    // +optional
    // +kubebuilder:validation:XIntOrString
    MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`
}
```

现有 Role 与 RIS 已有 `maxSurge`，Policy API 和协调覆盖分支需要增加该能力。实现须同步 API、CRD、生成代码、RBG 参数计算及 RIS 参数下发。

在 `GangSchedulingStrategy` 中增加按 Segment 计数的字段：

```go
type GangSchedulingStrategy struct {
    // ...
    // MinSegments counts complete Segments for a partitioned rule.
    // It is mutually exclusive with MinReplicas within the same rule.
    // +optional
    MinSegments *int32 `json:"minSegments,omitempty"`
}
```

更新 Adapter 目标引用，使 role 与 Segment 目标互斥但各自可选：

```go
type AdapterScaleTargetRef struct {
    Name string `json:"name"`

    // Role 指向单个 RBG role。Role 与 Segment 必须且只能设置一个；
    // 现有 role 目标客户端行为保持不变。
    // +optional
    Role string `json:"role,omitempty"`

    // Segment 指向 CoordinatedPolicy 规则，此时 spec.replicas 表示完整 Segment 数。
    // +optional
    Segment string `json:"segment,omitempty"`
}
```

准入和 reconcile 必须拒绝 `role` 与 `segment` 同时设置或同时为空。CRD schema、生成代码和校验必须同步更新；`role` 从 required 变为 optional 是本契约的一部分。

### 校验

准入校验检查配置结构，RBG reconcile 检查关联资源与执行条件。以下 Policy 字段路径均相对于 `spec.policies[]`。

#### Policy 准入校验

| 校验对象 | 要求 |
| --- | --- |
| `segment` 与 `roles` | key 集合完全一致，至少两个 role，每个 value 大于 0，且 `roles` 中不得重复 |
| 成员独占 | Segment 成员与其他所有规则的 `roles` 不相交，不区分策略域；创建、更新及不同规则排列顺序下结果一致 |
| `strategy.scheduling.gang.minSegments` | 正整数，只用于 Segment，与同一规则的 `minReplicas` 互斥 |
| Segment Gang 的调度器 | 要求 Volcano 支持 `subGroupPolicy`；scheduler-plugins 只有 Pod 总数门槛，不能保证完整 Segment 身份，拒绝该组合 |
| Segment 同时配置 scaling 与 Gang | 必须显式设置 `minSegments`，且有效 `segBatchSize >= minSegments`；`segBatchSize` 省略时按 1 参与检查 |
| 配比更新 | 同一命名规则在更新前后均声明 Segment 时，map 必须完全相同；禁止增删 key 或修改 value |
| 规则重命名 | 同一次更新只有在不改变 Segment map 时才允许重命名；同时改变规则名和配比必须拒绝，并拆分为撤销后另行添加 |
| 规则身份 | 同一 Policy 内确定性 rule ID 不得冲突 |
| `strategy.scaling.segBatchSize` | 正整数，只用于 Segment，不支持百分比；省略时按 1 处理 |

同时配置 scaling 与 Gang 时要求显式设置 `minSegments`，使上述检查只依赖当前 Policy，不需要在 webhook 中读取 RBG 或 Adapter 的最终期望组数。未配置 scaling 时，RIS 直接收到全部期望组目标，Gang 可以继续使用省略 `minSegments` 表示全部期望组的默认规则。

添加、移除及重新添加 Segment 按[配置生命周期](#coordinatedpolicy-的添加与删除)处理，不校验历史配比。

如果需要重叠协调，应将相关 role 的并集共同构建为一个 Segment，并在同一规则内配置策略。例如，既要协调 prefill 与 decode，又要协调 prefill 与 router，应在初始设计时用一条规则覆盖 `[prefill, decode, router]`，并声明三个 role 各自在同一个 Segment 中的实例数量，而不是让 prefill 同时出现在两条规则中。配比由业务需求确定，controller 不自动合并规则。已运行配置的配比仍不可原地修改；调整成员集合可撤销旧配置后按新配置重新校验、接管，不要求迁移到独立 workload。

该限制仅针对包含 Segment 成员的重叠；不涉及 Segment 成员的普通规则保持现有行为。同一 Segment 按配比包含某个 role 的多个 RoleInstance，不属于规则间成员重叠。

#### 成员模式校验

Policy 与 RBG 的关联配置决定成员模式是否支持 Segment：

- 创建或更新 Policy、RBG 时，若关联配置已存在，校验成员的有效 workload 后端和模式。每个成员必须使用 `workloads.x-k8s.io/v1alpha2 RoleInstanceSet`，且 pattern 为 `Stateful` 或默认 Stateful。拒绝 Stateless RIS、Deployment、StatefulSet、LeaderWorkerSet 和未知后端，不自动转换模式或忽略 Segment。
- 稠密成员是前置条件。Segment 成员不得携带用户 `excluded-ordinals`，其子 RIS 不得携带非空的 controller 管理 `committed-skip-ordinals` annotation。稀疏 Stateful 成员使整个 RBG reconcile 停止，不得直接按配比做整数除法。
- Policy 可以先于 RBG 创建；此时不能仅凭 Policy 确认成员模式，允许先保存 Policy，待 RBG 出现时校验。reconcile 在接管及下发 Segment 参数前再次检查，发现不合法组合时停止整个 RBG reconcile 并报告具体成员和原因，不改写模式或删除已有实例。
- 非 Segment 的 Stateless role 不受该限制影响；移除 Segment 后，模式变更仍遵守原有 workload 校验，不因撤销 Segment 而承诺支持原地切换模式。

例：prefill 为 Stateful RIS、decode 为 Stateless RIS，即使副本满足 `1P:1D`，也不能启用 Segment；两个成员均为 Stateful RIS 时，继续执行其余校验。

#### 副本与执行条件校验

RBG reconcile 先检查有效期望副本是否满足共同整数组数：所有 role 的 `roleReplicas / segment[role]` 必须等于同一个非负整数 `N`。满足时，`N` 为期望完整组数。副本未对齐时拒绝并报告；Segment 不静默向下取整，不发布剩余实例地址，也不删除已有成员。若 `minSegments` 超过 `N`，则报告调度门槛不可满足。

任一 role 不能整除，或各 role 得到的组数不同，都属于配比错误。RBG 按[完整 Segment 不变量](#完整-segment-不变量)计算并报告各 role 的剩余实例，停止整个 RBG reconcile，不写入任何目标或分组描述。该规则同时适用于首次创建、已生效 Segment 的后续 reconcile，以及已有 RBG 添加或重新添加 Segment：

- 首次创建不生成成员副本目标，不按向下取整后的组数创建部分实例。
- 已生效的 Segment 保持各 RIS 当前已下发的合法目标，不根据非法期望继续扩缩。
- 已有 RBG 添加或重新添加 Segment 时阻塞接管，不删除、重排已有成员，也不切换 Adapter 控制权。

该校验发生在 RBG reconcile 开始处，早于 discovery ConfigMap、分组描述、协调参数或 role 目标的任何写入。配比错误因此停止整个 RBG reconcile：RBG 报告 condition，不写入任何部分目标，也不改已有 RIS 对象及其 spec；已有实例继续运行，由独立的 RIS controller 继续维护。用户修正期望副本后，RBG 再重新校验并继续处理。

Policy 准入不校验 RBG 副本数，因为 Policy 可以先于 RBG 创建，副本数也可能随后由外部扩缩容系统修改。Segment 生效后，RBG 准入和 reconcile 必须拒绝只修改部分成员副本的请求，除非所有成员在同一请求中更新为同一个整数 Segment 数。ScalingAdapter controller 同样拒绝单 role 目标。这些是硬对齐规则，不是静默取整，并且必须在 RBG reconcile 中再次检查。

### 组指派与持久化

有效 Segment 下的 Stateful RIS 成员必须稠密：ordinal 从 0 开始且没有 exclusion。此时才通过整数除法确定组归属：

```text
groupIndex = instanceOrdinal / segment[role]
ruleID = SHA-256(ruleName) 的前 12 位小写十六进制字符
segmentRuleLabelKey = rbg.workloads.x-k8s.io/segment-rule
segmentRuleLabelValue = <ruleID>
segmentLabelKey = rbg.workloads.x-k8s.io/segment
segmentLabelValue = <ruleID>-<groupIndex>
```

例如，`2P:1D` 配比下，prefill-2 的组序号为 `2 / 2 = 1`。同一 RI 的所有 component Pod 共用规则标签和组标签。固定长度 `ruleID` 保证生成 label value 始终满足 Kubernetes 限制，且不依赖规则名的拼写或长度。准入必须拒绝同一 Policy 内 12 位十六进制 rule ID 冲突。

RIS 和 RI controller 通过独立于扩缩容、滚动更新的 metadata 同步链路维护身份。用户修改受管标签后，reconcile 恢复期望值。同一有效配置内，相同 ordinal 的 RI 或 Pod 重建、原地更新都不改变组归属；重新接管时则按新配置计算。

#### 通过 RIS annotation 下发分组描述

RBG controller 解析 Policy，通过 RIS 的受管 annotation 传递本 role 的分组描述。RIS controller 读取该描述，不自行查询完整 Policy 或计算跨 role 策略。

例如，规则 `kv-cache` 的配比为 `1P:2D`，本轮目标为 3 组，对 decode RIS 下发以下字段：

```yaml
metadata:
  annotations:
    # 内部协议示例名称，最终名称在实现时确定
    rbg.workloads.x-k8s.io/segment-config: |
      {
        "policyUID": "<policy-uid>",
        "ruleName": "kv-cache",
        "ruleID": "<12 位十六进制 rule id>",
        "instancesPerSegment": 2
      }
spec:
  replicas: 6
```

| 字段 | 用途 |
| --- | --- |
| `policyUID` | 区分同名 Policy 的删除重建，不表示永久绑定 |
| `ruleName` | 标识规则，用于派生确定性 rule ID |
| `ruleID` | 用于受管标签和选择器的确定性短身份 |
| `instancesPerSegment` | 本 role 每组的 RI 数；同例中 prefill RIS 的值为 `1`，副本目标为 `3` |

annotation 是内部传递协议，不是用户配置入口。RBG 负责维护和撤销它。具体的标签值如 `rbg.workloads.x-k8s.io/segment=e7652f82cdee-0` 只写到对应 RI 及其 Pod，不写入 RIS 公共实例模板，因为同一 RIS 的实例分属多个组。

#### RI 与 Pod 的 label 同步

组身份按以下链路传递，图中 metadata key 使用简称：

```text
RIS.metadata.annotations[segment-config]
    ↓ RIS controller 按实例 ordinal 计算 groupIndex
RI.metadata.labels[segment-rule] = <ruleID>
RI.metadata.labels[segment] = <ruleID>-<groupIndex>
    ↓ RI controller 同步各 component 的 Pod template 和已有 Pod
Pod.metadata.labels[segment-rule] = <ruleID>
Pod.metadata.labels[segment] = <ruleID>-<groupIndex>
```

完整 key 分别为 `rbg.workloads.x-k8s.io/segment-config`、`rbg.workloads.x-k8s.io/segment-rule` 和 `rbg.workloads.x-k8s.io/segment`。

RIS 与 RI controller 在常规 reconcile 中使用同一期望状态同步逻辑，覆盖新建和已有成员，而非仅在扩缩容分支处理：

| 当前状态 | 同步动作 |
| --- | --- |
| 存在有效分组身份 | 添加或更新受管组标签 |
| 确认分组身份已撤销 | 移除受管组标签 |
| 已符合期望 | 不操作 |
| 读取或解析失败 | 重试，不按无分组处理 |

- RI controller 直接 patch 已有 Pod 的 metadata，并同步后续创建或重建使用的 Pod template。ControllerRevision 计算必须忽略两个受管 Segment label key：仅新增、修改或删除这些标签时，不得产生更新 revision，不得修改 `updatedReplicas`，也不得进入滚动更新链路；不通过业务 rollout、镜像变更或 Pod 重建传播标签，不改变容器运行状态。
- Policy 事件触发对应 RBG reconcile；RIS annotation 和 RI label 的增改删触发下游同步。事件过滤和 metadata 比较必须保留这些变化。
- patch 只修改有差异的受管字段，保留其他 metadata。跨层允许短暂不一致，失败后重试，无需等待 Pod 自然重建。
- 同一 controller 对同一对象串行 reconcile，每次及冲突重试时按当前配置和归属重算期望。缓存延迟通过幂等 reconcile 收敛，不保存独立旧清理任务，也不新增清理控制器或保护协议。

Stateless RIS 不接收有效分组描述。已有 RBG 接管和撤销复用本链路，见[CoordinatedPolicy 的添加与删除](#coordinatedpolicy-的添加与删除)。

### 完整 Segment 不变量

完整性约束的是基础组目标和更新批次，不是 Kubernetes 对象的原子执行。本节先固定四个口径，避免后续把“期望”“下发”和“实际”混在一起：

- 期望副本：用户或 Adapter 提交的最终期望副本数，可以包含无法组成完整组的残差；存在残差的期望不能作为 Segment 副本目标执行。
- 有效期望副本：经过控制权解析后的最终期望副本数，用于校验配比和计算组数。
- 基础组目标：有效期望满足共同整数组数时，RBG 下发到 RIS 的目标副本数，只包含完整组；配比错误时不生成或更新该目标。
- 实际创建数：RIS 当前已创建的 RI 数，可能低于基础组目标。

以下三个状态口径用于判断：

| 口径 | 判定方式 |
| --- | --- |
| 合法有效期望的组数 | 各 role 的期望 RI 数分别除以配比，商必须为相同的非负整数；不读取 Ready 数 |
| 实际完整组数 | 按组身份检查配比要求的全部 RI 是否存在 |
| 实际可用组数 | 成员完整，且每个成员都满足 RIS 的可用性条件 |

诊断配比错误时，可以按以下公式计算向下取整的组数和各 role 的剩余实例：

```text
completeSegments = min(floor(roleReplicas / segment[role]))
residual[role] = roleReplicas - completeSegments × segment[role]
aligned = 所有 residual[role] 均为 0
```

`completeSegments` 只是诊断值，不是非法期望的执行目标。只有所有 `residual[role]` 都为 `0` 时，有效期望才通过配比校验，`completeSegments` 才能作为期望完整组数 `N`。对于 `2P:1D` 规则，`1P:1D` 和 `2P:0D` 都不是完整组目标。

例如 `2P:1D`、期望 `P=5、D=2`，公式得到 `completeSegments=2`、`residual[P]=1`、`residual[D]=0`。由于存在剩余实例，本次配比校验失败：RBG 通过 condition 报告原因，不生成或更新 RIS 副本目标。首次创建不能按 2 组创建 `4P、2D`；已生效的 Segment 保持当前合法目标；已有 RBG 添加或重新添加 Segment 时[阻塞接管](#已有-rbg-后添加-policy)，不删除或重排已有成员。

期望满足配比但实际缺员不属于配比错误。此时保留已有成员，按原目标补齐，不以实际完整组数缩容。有效 Segment 的单 role 扩缩请求同样被拒绝，不能通过上述公式部分执行。

创建、删除和更新期间允许暂态缺员，成员无需同时启动或 Ready。surge RI 不改变基础组目标，各 RIS 独立创建和回收，额外实例可能始终未组成完整组；组标签也不证明它们可服务，见[maxSurge 上限与 RIS 执行](#maxsurge-上限与-ris-执行)。

有效 Segment 的地址发现必须只枚举对齐后的基础组目标，不得发布不会被创建的剩余身份，也不得把 surge 身份发布为基础服务成员。地址表仍不是实际完整组或可用成员列表。

### 与 Scaling 的集成

RBG controller 先确认有效期望副本满足共同整数组数，再在组数空间计算共同副本目标，并乘配比生成各 role 的 RI 目标；不先算各 role 的实例目标再分别取整。配比校验失败时不生成或更新 RIS 副本目标。Policy 的 `strategy.scaling` 同时控制首次创建和后续扩容。

| 配置 | 行为 |
| --- | --- |
| 无 `strategy.scaling` | 直接下发期望的完整组目标，不隐式限制为每批 1 组 |
| 有 scaling 策略 | 每批最多新增 `segBatchSize` 组，默认 `1`；最后一批允许不足配置值 |
| `progression: OrderScheduled`（默认） | 当前批次全部成员的预期 Pod 已调度后，才能进入下一批 |
| `progression: OrderReady` | 当前批次全部 RI Ready 后，才能进入下一批 |

`segBatchSize` 只控制新增组数，不是期望总数，也不限制缩容、滚动更新或 surge。两个 `maxSkew` 不参与 Segment 计算；scaling 也不读取 Gang minimum 抬升目标，配置冲突见[与 Gang 的集成](#与-gang-的集成)。

目标不变且 `C < N` 时，扩容公式为：

```text
N = 合法有效期望副本对应的完整 Segment 数
C = 已下发的当前组数目标（不是当前 Ready 或实际完整组数）
B = segBatchSize，未配置为 1
当前批次尚未满足 progression：保持 C，继续补齐当前批次
没有前批，或当前批次已满足 progression：nextTarget = min(N, C + B)
```

首次从零创建没有前批需要等待。缺员、部分下发或状态反馈延迟时，RBG 继续收敛当前批次，不因重试多放一批，也不按实际完整组数回退目标。部分 role 扩缩请求被拒绝，原因写入 RBG 及相关 Adapter 的 condition。

例如，目标 5 组、`segBatchSize: 2`，按 `0 → 2 → 4 → 5` 推进。当前批次全部 Pod 已调度、其中一个 D 未 Ready 时，`OrderScheduled` 可继续，`OrderReady` 仍等待。对 `2P:1D` 配比，每新增 1 组的共同目标是 `2P、1D`，不是 `1P、1D`；对象仍可先后创建。

#### 副本目标下发

RBG controller 下发的是各 RIS 管理的 RI 数量，不是 RIS 对象数量：

```text
RBG controller：有效期望副本 + 当前状态 + Policy
    ↓ 计算本轮 targetSegments
    ↓ targetReplicas[role] = targetSegments × segment[role]
各 RIS.spec.replicas
    ↓ RIS controller 创建或删除 RI
    ↓ RI controller 管理 Pod
```

`targetReplicas` 是本轮绝对副本总数，不是增量。分批推进不把中间目标反写为 RBG 的 `spec.roles[].replicas`。分组描述独立下发，仅修改副本数不会自动产生组标签。

### ScalingAdapter

Adapter 接收最终期望组数，按配比写入 RBG；RBG 再按 scaling 策略分批下发 RIS 目标。现有 Adapter 只支持 role target，本设计增加 Segment target、控制权交接和组级状态。

#### Segment Adapter 配置与自动创建

`CoordinatedPolicy.spec.policies[].strategy.scaling.scalingAdapter` 复用现有配置类型：`enable` 默认 `false`；`labels` 附加到 Adapter，但不能覆盖控制器维护的归属与目标标识。普通 role Adapter 的配置入口不变。

以下示例采用 `1P:2D` 配比，让 RBG 为 `kv-cache` 规则自动创建一个 Adapter：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: llm
spec:
  policies:
    - name: kv-cache
      roles: [prefill, decode]
      segment:
        prefill: 1
        decode: 2
      strategy:
        scaling:
          segBatchSize: 1
          progression: OrderReady
          scalingAdapter:
            enable: true
            labels:
              app.kubernetes.io/part-of: inference
```

RBG 校验规则和接管条件后，创建并维护规则级 Adapter，关联当前 RBG 的 OwnerReference。未启用时不创建；对象意外丢失且配置仍有效时重建。自动创建不替代绑定校验，Policy 配置中不另设初始副本字段，初始化与成员 role Adapter 的处理见[副本控制权与生命周期](#副本控制权与生命周期)。

#### 目标与副本单位

以下为自动创建的 Adapter 被设置为期望 4 组后的示意，名称仅作示例：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroupScalingAdapter
metadata:
  name: kv-cache-scaling-adapter
spec:
  scaleTargetRef:
    name: llm
    segment: kv-cache
  replicas: 4
```

| Adapter 字段 | 语义 |
| --- | --- |
| `spec.scaleTargetRef.name` | 同 namespace 的 RBG 名称 |
| `spec.scaleTargetRef.segment` | 与 RBG 同名 Policy 中的规则名，不是单个组的标签值 |
| `spec.scaleTargetRef.role` | 与 `segment` 二选一；原 role target 的单位和行为不变 |
| `spec.replicas` | 最终期望组数，是绝对总数，不是 RI / Pod 数或扩缩增量 |

全部成员的最终副本按 `desiredReplicas[role] = spec.replicas × segment[role]` 生成。本例的 4 组对应 `P=4、D=8`。单 role 扩缩请求不执行，通过 condition 报告原因。

#### 副本控制权与生命周期

现有副本控制包含两条链路：Adapter controller 持久写入 RBG，RBG controller 的 `applyRBGSAReplicasOverride` 将 Adapter 目标覆盖到本轮内存对象。交接必须同时隔离这两条旧写入链路，仅删除 Adapter 或拦截新请求不足以完成交接。

Segment 模式遵守以下规则：

- 每条规则只有一个有效 Adapter 副本控制者。成员 `role.replicas` 是组目标的投影，不是独立扩缩入口；旧 Adapter 和其他写入者不能拆散组目标。
- role 属于有效 Segment 时，其 role Adapter 不生效，与 Segment Adapter 是否启用或存在无关。RBG 按有效配置和接管状态维护 Adapter 期望集合，不改写用户原有的 role 开关。
- 接管后，RBG 清理当前 RBG 管理的成员 role Adapter，并停止重建。清理校验对象归属和目标，不影响其他 RBG 或非成员 role。
- 创建、删除允许最终一致，但新 Adapter 取得写入权前必须隔离旧持久写入和内存覆盖；删除延迟不能使旧目标继续生效。
- 创建、绑定、清理和事件关联均识别 Segment target，不能因 `role` 为空误删 Adapter。同名 Policy 重建后按新对象身份重新校验和绑定。

没有显式期望组数时，从当前有效副本初始化：所有成员的 `replicas / segment[role]` 必须为相同的非负整数，否则报告阻塞，不静默取整。已有显式目标先校验，不无条件覆盖。初始化不要求全部成员 Ready，也不替代存量接管。

| 操作 | Segment Adapter | 成员 role Adapter |
| --- | --- | --- |
| 只删除 Adapter 对象，配置仍启用 | RBG 重建 | 不恢复创建或写入 |
| 关闭或移除 Adapter 配置，但保留 Segment | 停止写入及覆盖，报告未启用并清理 | 不恢复创建或写入 |
| 移除 Segment 配置、规则或 Policy，使 role 不再属于有效 Segment | 停止写入及覆盖，报告目标失效并清理 | 按原 `scalingAdapter.enable` 和正常绑定、归属条件恢复；未启用则不创建 |

配置失效不表示期望组数为零，不删除 workload 或清零已提交目标。恢复 role Adapter 前先隔离旧 Segment Adapter 的两条写入链路；新建 role Adapter 从当前 RBG 已提交的 `role.replicas` 初始化，不使用历史值或 RIS 中间目标。其他有效策略继续生效，无需删除整个 Policy。

双向交接的并发隔离、删除延迟、重试、重启和重新绑定必须通过实现验证；不增加历史配比限制或标签清理协议。

#### Adapter 与 RBG 的扩缩协调闭环

Segment 复用现有 Adapter → RBG → RIS 链路，将单 role 写入扩展为整条规则的提交：

```text
用户或外部扩缩容系统修改 Adapter.spec.replicas（可通过 /scale）
    ↓ ScalingAdapter controller 校验目标、配比和控制权
    ↓ 在一次 RBG 更新中提交全部成员的最终期望副本
RBG controller 按 Policy 计算本轮 targetSegments
    ↓ 按配比下发各 RIS.spec.replicas
RIS / RI controller 执行并反馈状态
    ↓ RBG controller 判断是否继续推进
```

Adapter controller 按以下顺序处理：

1. 验证 RBG、Policy、规则和成员存在，且 Adapter 已取得写入权。目标无效时报告阻塞，不提交部分成员目标。
2. 从同一配比和组数计算全部成员的绝对副本数，检查非法值和乘积溢出。
3. 在一次带资源版本冲突保护的 RBG 更新中提交全部成员，保留无关 role 和字段。冲突时重新读取并校验后重算，不逐 role 提交；目标相同时跳过重复写入。
4. 同步必要状态。Adapter、Policy 和 RBG 变化均触发 reconcile，绑定、撤销和状态同步不依赖用户再次修改副本。

一次 RBG 写入只保证最终目标一并提交，不保证多个 RIS 或 Pod 原子变更。重试与重启仍写绝对目标，不重复累加副本。缩容同样使用完整组目标，由 Stateful RIS 执行成员操作。

例如，当前 2 个 `1P:2D` 组满足 progression，Adapter 请求扩至 4 组，`segBatchSize: 1`：

| 阶段 | Adapter 期望组数 | RBG 最终期望 P / D | RIS 本轮目标 P / D |
| --- | --- | --- | --- |
| 扩容前 | 2 | 2 / 4 | 2 / 4 |
| 提交新目标，放行到 3 组 | 4 | 4 / 8 | 3 / 6 |
| 满足 progression，放行到 4 组 | 4 | 4 / 8 | 4 / 8 |

等待期间，RBG 和 Adapter 保持最终期望，不能用 RIS 中间目标反向覆盖。

实现入口：[Adapter API](../../api/workloads/v1alpha2/rolebasedgroupscalingadapter_types.go)、[ScalingAdapter controller](../../internal/controller/workloads/rolebasedgroupscalingadapter_controller.go) 的 `Reconcile` / `updateRoleReplicas`，以及 [RBG controller](../../internal/controller/workloads/rolebasedgroup_controller.go) 的 `applyRBGSAReplicasOverride`、Adapter 生命周期与 `handleCoordinationStrategies`。API、CRD 和生成代码须同步扩展。

#### scale 子资源与指标边界

`/scale` 仍映射 Adapter 的 `spec.replicas`、`status.replicas` 和 `status.selector`；`status.readyReplicas` 留在 Adapter status 中，不进入标准 `/scale` 副本映射。

| Adapter 字段 | 组级语义 | 请求 4 组、已提交、本轮到 3 组、实际 Ready 2 组时 |
| --- | --- | --- |
| `spec.replicas` | 最终请求组数，不随中间目标回退 | `4` |
| `status.replicas` | 全部成员目标成功提交到 RBG 的有效组数，不是已创建或 Ready 数 | `4` |
| `status.readyReplicas` | 实际完整且全部成员 Ready 的组数 | `2` |

提交失败时，`status.replicas` 保持此前已提交的目标。RBG 写入成功、Adapter status 写入失败时，后续 reconcile 或重启后补齐状态；目标相同也不能跳过必要状态同步。成员状态变化时，独立更新 Ready 组数。

Ready 组数必须按组身份统计。例如 P 仅在 Segment 0 Ready、D 仅在 Segment 1 Ready，不能拼成一个 Ready 组。Ready 组数用于观测或相应 scaling progression，不作为 Gang 资源准入条件。

`status.selector` 保留为 `/scale` 的 Pod 标签选择器。Segment Adapter 必须用单个 AND selector 覆盖该规则的全部 component Pod：

```text
rbg.workloads.x-k8s.io/group-name=<rbg-name>,
rbg.workloads.x-k8s.io/segment-rule=<ruleID>
```

该 selector 不得匹配其他 RBG、其他 Segment 规则、非成员 role 或单个 component。它只用于观测和后续指标接入；组数统计仍独立按组身份计算。本 KEP 不增加业务指标采集、聚合或去重职责。

HPA 使用是开放问题，边界如下：

- 本期承诺：Adapter 提供组级 target、`/scale` 基本映射和组级状态。
- 本期不承诺：HPA 配置方式、指标选择、通用 Pod selector、业务指标采集、聚合或去重。
- 待定：是否采用 `External + AverageValue` 或其他接入方式，需在 HPA 使用方案中确定。

### 与 Gang 的集成

声明 Segment 后，Gang 的计数单位从 RoleInstance 切换为完整 Segment：

- `minSegments: n` 表示集群可调度资源至少满足 `n` 个完整 Segment 的资源需求时，才允许调度放行。需求覆盖这些 Segment 中全部 RoleInstance 的全部 Pod。
- 资源是否足够由调度器结合 Pod 的 requests 和调度约束判断，不能只比较集群资源总量；资源不足时等待，不以成员 Ready 作为准入条件。
- 未配置 scaling 时，声明 Gang 但省略 `minSegments` 表示全部期望完整 Segment；同时配置 scaling 与 Gang 时必须显式设置该字段。
- `minSegments` 与 `minReplicas` 在同一规则内互斥。未声明 Segment 的规则继续使用 `minReplicas`。
- Gang 只负责资源准入与调度放行，不负责 Segment 的结构划分，也不保证成员同时启动或同时 Ready。

#### Volcano subGroup 映射

Segment 复用现有单 RBG PodGroup，但将 Volcano subGroup 的划分单位从一个 RoleInstance 改为一个完整 Segment。每条 Segment Gang 规则生成一个 SubGroupPolicy：

```text
labelSelector:
  rbg.workloads.x-k8s.io/group-name=<rbg-name>
  rbg.workloads.x-k8s.io/segment-rule=<ruleID>
matchLabelKeys = [rbg.workloads.x-k8s.io/segment]
subGroupSize   = Σ(segment[role] × ComputeSubGroupSize(role))
minSubGroups   = minSegments
minMember      = minSegments × subGroupSize
```

`matchLabelKeys` 只包含 `segment`，不再包含 `role-instance-name`。因此，同一 Segment 标签下不同 role、不同 RI 的 Pod 属于同一个 subGroup；不同 Segment 的 P、D 即使分别满足各 role 的数量要求，也不能拼成完整组满足门槛。

例如 `1P:2D`、每个 RI 一个 Pod、`minSegments: 2` 时，一个 subGroup 包含 `1P + 2D`，`subGroupSize=3`、`minSubGroups=2`、`minMember=6`。多条 Gang 规则共用一个 RBG PodGroup，每条 Segment 规则分别生成 SubGroupPolicy，并将各自门槛累加到 `minMember`；`segment-rule` selector 保证不同规则互不重叠。未配置 scaling 且省略 `minSegments` 时，先将门槛解析为全部期望完整组，再生成策略。

scheduler-plugins 只有 Pod 总数门槛，无法表达上述完整 Segment 约束。Segment 规则声明 Gang 时要求 Volcano 支持 `subGroupPolicy`；admission 检查配置的调度器，reconcile 再检查实际 CRD 能力，不降级为 Pod 总数或 per-role 数量门槛。

#### 与 Scaling 的配置校验

Segment 的 scaling 与 Gang 独立执行。Gang 不改变 `segBatchSize` 或 scaling target，scaling 不降低 Gang 门槛。两者通过配置校验避免首批成员不足，而不是在执行期互相调参。

设 `B` 为有效 `segBatchSize`，省略时为 1，`G` 为显式 `minSegments`。同一 Segment 规则同时配置 scaling 与 Gang 时必须满足：

```text
B >= G
```

这样首次创建或从低于门槛的目标继续扩容时，下一批能够提供至少 G 组。后续最后一批可以小于 G，因为此前下发的目标已经达到门槛，不需要限制每一批都大于等于 G。

例如从零创建 4 组：

| 配置 | 行为 |
| --- | --- |
| `segBatchSize: 2`、`minSegments: 2` | 合法；首批部署 2 组，满足资源准入和 progression 后再部署后 2 组 |
| `segBatchSize: 1`、`minSegments: 2` | Policy webhook 拒绝；首批必然不足，不进入持续互等 |
| 配置 scaling、`segBatchSize: 1`，省略 `minSegments` | Policy webhook 拒绝；同时配置 scaling 与 Gang 时必须显式设置门槛 |
| 未配置 scaling，省略 `minSegments` | 合法；直接下发 4 组目标，Gang 默认要求全部 4 组 |

Policy webhook 只检查当前 Policy 能确定的组合。RBG reconcile 还需检查 `minSegments <= 期望完整组数`、有效配比、调度器能力以及绕过准入的旧配置；Adapter 或 RBG 后续修改期望组数导致冲突时，按现有 Gang 不兼容路径报告 `GangConfigured=False`，不自动增加期望副本或抬升中间目标。配置合法但资源不足不属于配置错误，`GangConfigured` 不代替 Pod 和 PodGroup 的调度状态。

缩容到 `minSegments` 以下时，现有 Gang 不兼容路径同样是用户可见的结果：RBG 报告 condition，RIS 继续保持上一次有效的组目标，不降低各 role 的副本目标。该缩容会持续阻塞，直到用户修正期望组数或门槛；controller 按现有 Gang 不兼容间隔重新入队，配置修正后恢复正常 role reconcile。

`B >= G` 的 webhook 校验是有意保持静态、无状态的。对已经运行至少 `G` 个完整组的 RBG，该校验同样拒绝 `B < G`，尽管此时下一批一旦落地就达到或超过门槛。这使稳态下的扩容批量粒度与 Gang 门槛耦合：例如已运行 5 组、`minSegments: 2` 的成熟 workload，每次扩容进度必须至少增加 2 组，而不能逐组增加。这是 alpha 阶段的既定取舍，可让 Policy 准入保持确定性且无需读取 RBG 状态；将校验移到已知实际组数的 reconcile 阶段属于后续可选优化，不在本方案范围内。

现有非 Segment 路径会[按 Gang minimum 抬升 scaling target](../../internal/controller/workloads/rolebasedgroup_controller.go)。Segment 不沿用该联动；非 Segment 行为保持不变。

### 与 Rolling Update 的集成

声明 Segment 并配置 `strategy.rollingUpdate` 后，同组成员同批准入，不将部分 role 单独划入另一批。成员可以先后执行、启动和 Ready，期间允许混合版本。

下表字段均位于 `CoordinatedPolicy.spec.policies[].strategy.rollingUpdate`：

| 字段 | Segment 语义 |
| --- | --- |
| `partition` | 保留不更新的组数；`1` 保留 Segment 0，后续组可进入更新范围 |
| `maxUnavailable` | 显式配置且解析预算大于 0 时，作为 RBG 跨 role 共享的组级预算，获准组内不再按同一值限制 RI 并发；预算为 0 时与 surge 配合；未配置时沿用 RIS 设置 |
| `maxSurge` | 额外组容量上限，按配额投影为各 RIS 的 RI 上限；不承诺创建完整备用组 |
| `maxSkew` | 忽略；创建批量字段 `scaling.segBatchSize` 也不参与更新准入 |

前三个字段在 RBG 层按期望完整组数解析百分比。下发 RIS 时，`partition` 和 `maxSurge` 按配比生成实例参数；`maxUnavailable` 根据组级预算是否为零设置 RIS 的执行窗口，不再原值覆盖，详见[执行窗口](#policy-覆盖与-ris-执行窗口)。

百分比取整规则固定如下：

| 字段 | 组级取整规则 |
| --- | --- |
| `partition` | 向下取整为保留的完整 Segment 数 |
| `maxUnavailable` | 向下取整为不可用的完整 Segment 数 |
| `maxSurge` | 向上取整为额外完整 Segment 数 |

例如期望 3 组时，`25%` 分别解析为 0 个保留组、0 个不可用组和 1 个额外组。

需要区分两类语义：

- `maxUnavailable` 是用户声明的组级预算。解析预算大于 0 时，RBG 按 Segment 计数，RIS 收到 `100%`，获准组内不再增加 RI 数限制；预算为 0 时，RIS 收到 `0`，通过可用 surge 维持不增加不可用实例。未配置时不生成组级默认预算，保留 RIS 原设置。
- `partition` 有两层含义：作为用户配置，它表示保留不更新的 Segment 数；作为 RBG 下发值，它是本轮准入结果在 RIS 上的投影边界。它不是传统 StatefulSet partition 的单调递减序号，目标版本变化时可以收紧或放宽。

例如 `2P:1D`、`partition: 1` 保留的是 `2P + 1D`，不是各 role 各保留一个 RI。

#### RBG 与 RIS 的更新协调闭环

现有非 Segment 路径由 RBG 根据各 role 的更新进度和 `maxSkew` 生成动态 partition，再与用户分区取更严格者。Segment 复用该下发链路，改由 RBG 按组身份、目标版本和成员状态计算共同边界与预算。

```text
RBG controller：成员状态 + Policy
    ↓ 按组统计不可用与在途占用，计算共同更新边界
    ↓ 投影 partition，按组级预算设置 maxUnavailable，投影 maxSurge
WorkloadReconciler：写入各 RIS.spec.updateStrategy
    ↓ RIS controller 在 partition、执行窗口和 surge 条件内更新 RI
    ↓ RI controller 执行 Pod 更新，成员状态反馈给 RBG
RBG controller：下一次 reconcile 重算边界
```

各 RIS 不能因本地预算空闲越过共同边界。无 surge、`maxUnavailable: 1` 时，如果 D 仍在 Segment 1 更新，P 即使已恢复可用，也不能继续更新 Segment 0，导致两组同时不可用。

RBG 必须读取成员归属与状态，不能仅比较各 role 的聚合副本数或依赖 RIS 的局部窗口，也不通过伪造健康成员的 unhealthy 状态传递组级占用。多个 RIS 可以先后接收参数，但新模板与 partition 必须落实同一组级预算。

#### 成员可用性与预算补位

成员可用性沿用 RIS 的判断，包括原地更新完成、健康状态和 `minReadySeconds`。RBG 按组身份聚合，任一配比成员缺失或不可用，该组就不可用：

```text
Segment 0 的 P、D 都不可用 → 1 个不可用组
Segment 0 的 P、Segment 1 的 D 不可用 → 2 个不可用组
```

`updatedReplicas` 增加或瞬时 Ready 不足以释放预算。

一个已准入 Segment 中，需要更新的成员全部完成各自的本轮目标版本更新，且全部配比成员按 RIS 语义恢复可用后，才释放该组的在途更新占用。只完成某个 role，或组内仍有尚未执行的更新，都不能提前释放。

显式配置 `maxUnavailable` 时，RBG 使用可补位的组级窗口，而不是等待整批完成后再开启下一批。以下公式描述无 surge 时的简化窗口，不作为有 surge 时禁止候选组进入更新范围的固定门槛：

```text
U = 当前不可用的基础 Segment 集合（包括外部故障）
I = 已准入但尚未完成并恢复可用的基础 Segment 集合
B = Policy 的 strategy.rollingUpdate.maxUnavailable 按期望完整组数解析后的预算
剩余预算 = max(B - |U ∪ I|, 0)
```

同一组同时属于 `U` 和 `I` 时只计一次。RBG 在剩余预算内放行符合 partition 等条件的完整组；新增准入立即占用预算，避免状态延迟造成重复放行。外部故障也计入 `U`，可能使实际不可用数超过预算；此时控制器不得继续主动扩大不可用组集合。

有 surge 时，RBG 使用以下确定性准入规则：

```text
availableSurge[role] = 该 role 中已创建、处于目标版本、健康且非 terminating 的额外 RI 数
surgeEligible(group) = 每个成员 role 均满足 availableSurge[role] >= segment[role]

若 partition 允许，且满足以下任一条件，则放行该组：
    |U ∪ I_loss| + 1 <= B
    或 surgeEligible(group)
```

`I_loss` 包含已准入但尚未完成、且已经出现基础成员不可用，或每个 role 已不再保有足够目标版本 surge 以继续无损推进的组。安全的 surge 支撑在途组仍被跟踪为 in-flight，但不消耗不可用预算。若 surge 容量消失或基础成员变为不可用，该组立即转为 `I_loss`。仅一个 role 有富余 surge 不能放行组；每个成员 role 都必须至少达到本组配额。

例如无 surge、`maxUnavailable: 2`，在满足 `partition` 等其他推进条件时：

| 不可用与在途 Segment 的去重占用数 | 最多还能新增放行 |
| --- | --- |
| 0 | 2 组 |
| 1 | 1 组 |
| 2 或更多 | 0 组 |

一组完成并恢复可用后，下一次 reconcile 可以补入一组，不必等待另一组完成。某组因慢成员或失败成员卡住，只保留该组的占用；有剩余预算且满足其他条件时，其他组仍可推进，不因一组未 Ready 就冻结整条规则或整个 RBG。这里的“两组”是准入窗口，组内不再用同一个 `maxUnavailable` 值限制 RI 并发，但仍不保证各 RIS 同时启动或同时 Ready。

#### Policy 覆盖与 RIS 执行窗口

RBG 控制哪些 Segment 可以进入更新，RIS 在共同边界内执行 RI 更新。组级预算已经允许整个 Segment 暂时不可用时，不再用同一个值限制组内 RI 轮换；未配置组级预算时，仍保留 RIS 原有的局部窗口。

Segment 的参数覆盖规则为：

| 配置情况 | RBG 的组级控制 | RIS 的执行窗口 |
| --- | --- | --- |
| Policy 显式设置 `maxUnavailable`，解析预算 `B > 0` | 按组聚合不可用与在途状态；无 surge 时使用上述窗口，有 surge 时保留额外可用容量参与预算的机制 | 忽略 role 原值，下发 `100%`；共同 partition 内的获准 RI 不再受额外 RI 数限制 |
| Policy 显式设置 `maxUnavailable`，解析预算 `B = 0` | 无 surge 时不主动增加不可用组；有 surge 时允许准备额外容量，并结合组状态推进 | 忽略 role 原值，下发 `0`；只有实际可用的 surge 增加执行预算 |
| Policy 未设置 `maxUnavailable` | 不新增隐式组级默认预算，仍按 Segment 边界及其他已配置的协调条件推进 | 保留 role 配置；role 也未配置时沿用 RIS 默认值 |

`B > 0` 时，Policy 的整数或百分比只用于 RBG 的组级预算，不再作为 RI 预算二次执行。下发 `100%` 是取消 RIS 的额外局部限流，不是按配比放大 Policy 原值；各 RIS 仍不得越过共同 partition。`B = 0` 不能下发 `100%`，否则会在没有可用 surge 时主动制造不可用实例。RBG 不从各 RIS 的局部值反推统一组级默认预算。

例如无 surge、起始全部健康的 `1P:2D`，Policy 配置 `maxUnavailable: 1`。RBG 一次最多让一个 Segment 占用更新预算，P、D 两个 RIS 都收到 `100%`。该组获准后，P、D0、D1 都可以进入更新，不要求两个 D 串行轮换；整个组可能暂时不可用，但只占用一份组级预算。实际启动和 Ready 时间仍可不同，整组完成目标版本并恢复可用后，RBG 才释放在途占用并继续补位。

`B = 0` 时，P、D 两个 RIS 都收到 `0`。配置了 surge 时，各 RIS 先按现有算法准备额外实例，只有实际可用的 surge 才能支持基础 RI 更新；未配置 surge 时，健康基础 RI 不因组级准入而开始会造成额外不可用的更新。

#### maxSurge 上限与 RIS 执行

采用 Policy 配置、RBG 计算并覆盖下发、RIS 正常执行的方案，不增加实际 surge 目标字段或新的 surge 控制 annotation。

| 规则类型 | Policy 的 `maxSurge` | 下发到 RIS 的行为 |
| --- | --- | --- |
| 非 Segment | 显式配置 | 原值覆盖 role 的 `maxSurge`，百分比由各 RIS 按自身副本数解析 |
| 非 Segment | 未配置 | 保留 role 配置和原默认行为 |
| Segment | 显式配置 | 忽略 role 配置，先解析额外 Segment 容量，再乘各 role 配额生成整数 RI 上限 |
| Segment | 未配置 | 显式下发 `0`，不回退到 role 配置 |

Segment 模式的计算为：

```text
S = Policy 的 strategy.rollingUpdate.maxSurge 解析出的额外 Segment 容量上限
    整数：直接使用；百分比：ceil(期望完整 Segment 数 × 百分比)
    未配置：0
risMaxSurge[role] = S × segment[role]
```

百分比沿用 `maxSurge` 向上取整的规则，但先在 Segment 层解析，再投影整数；不能把原百分比分别交给 RIS 取整。例如期望 3 个 `1P:2D` Segment，`maxSurge: "25%"` 得到 `S=1`，P RIS 收到 `1`，D RIS 收到 `2`。`maxUnavailable` 使用另一套规则：组级预算大于 0 时两个 RIS 都收到 `100%`，预算为 0 时都收到 `0`，不下发 Policy 原值。

覆盖只改变下发的执行参数，不改写 RBG 中用户声明的 role 配置。Segment 仍有效时，移除 Policy 的 `maxSurge` 字段应下发 `0`，撤销旧正值的影响，不能因值为空而跳过覆盖；整个 Policy 或 Segment 撤销后，则按当前 role 配置及剩余有效策略重算，见生命周期章节。

RIS 收到的是上限，不是必须达到的额外副本目标。实际 surge 数量、保留与回收沿用各 RIS 的现有算法，`RIS.spec.replicas` 不因 surge 增加。当前 Stateful RIS 在正在更新且没有已有有效 surge 时，计算为：

```text
healthyOld = [partition, replicas) 内健康、非 terminating 的旧版本 RI 数
activeSurge = min(maxSurge, max(healthyOld - maxUnavailable, 0))
```

公式中的 `maxUnavailable` 是 RIS 最终接收的执行值，不是 Policy 的组级原始值。已有有效 surge 时还会应用本地保留逻辑，不能直接套用上述简化式。`replicas=4、partition=0、maxUnavailable=2、maxSurge=2` 且 4 个旧实例健康时，额外目标为 `2`；`replicas=3、partition=2、maxUnavailable=1、maxSurge=1` 且仅放行的旧实例健康时，额外目标为 `0`。两者均遵守上限，不要求每轮用满 surge。Segment 组级预算大于 0 时，RIS 收到 `100%`，上述简化式不会要求额外创建 surge；预算为 0 时则收到 `0`，可按可用 surge 推进。

各 RIS 的实际额外数量可以不满足 Segment 配比，也可以先后回收；RBG 不强制补齐临时组或等待跨 role 的统一回收条件。surge RI 复用普通 RI 的创建及分组 label 同步链路，但 label 不代表临时组已完整或可服务。本方案不承诺完整备用 Segment 的创建、保留或生命周期协调。

surge 与 `maxUnavailable` 的组合沿用现有执行机制，不另设 Segment 专属的“零不可用预算禁止更新”规则。Segment 调整协调计量单位、成员归属和参数投影，不替换 RIS 的 surge 创建、可用性预算及回收算法。当前 Stateful RIS 的本地执行预算为：

```text
availableSurge = 本 RIS 中已创建、处于目标版本、健康且非 terminating 的额外 RI 数
有效执行预算 = maxUnavailable + availableSurge
```

这里参与预算的是实际可用的额外实例，不是配置的 `maxSurge` 上限，也不是刚创建但尚未可用的数量。对健康旧实例，`maxUnavailable: 0、maxSurge > 0` 时，RIS 在已允许更新的范围内按现有算法创建 surge；额外实例可用后，才用新增的可用容量支持基础实例更新，最终按本地条件回收。原有故障实例处理、暂停及用户 partition 等限制仍生效，不因配置 surge 就保证任意状态下都能推进。

RBG 继续生成共同的 Segment 更新边界，并聚合不可用与在途状态；RIS 不得越过该边界。进入候选更新范围不等于立即允许损失可用容量，后者仍受 RIS 的实际执行预算约束。因此，不能把无 surge 时的 `max(B - |U ∪ I|, 0)` 公式直接用作所有场景的 partition 前置门槛，在 `B=0` 时连 surge 的准备机会也一并阻断。

上述准入规则就是组状态、在途占用与各 role 额外容量之间的必需衔接规则。单个 role 的 surge Ready 不表示基础组更新完成，各 role 的可用 RI 数也不能拼成幻影完整组。该规则不增加完整备用组保证或跨 role 统一回收协议。

#### 按 Segment 生成 partition

对于已通过校验、ordinal 从 0 开始且稠密的 Stateful RIS，常规更新优先复用现有 partition 表达共同准入边界，无需预先增加 Segment 更新名单 annotation：

```text
segmentBoundary = max(userSegmentPartition, dynamicSegmentBoundary)
risPartition[role] = segmentBoundary × segment[role]
```

其中，`userSegmentPartition` 是用户配置按 Segment 单位解析后的保留数量；`dynamicSegmentBoundary` 是 RBG 根据当前状态和协调策略计算的本轮边界。下发时投影为实例级参数，不改变 Policy 字段本身采用 Segment 单位的语义。

例如，有效期望副本固定为 `3P、6D`，Segment 配比为 `1P:2D`，本轮目标版本固定，无 surge、无其他成员故障，用户配置 `partition: 0`、`maxUnavailable: 1`。采用逐组推进时：

| 阶段 | Segment 边界 | P RIS partition | D RIS partition | 本次新增放行成员 |
| --- | --- | --- | --- | --- |
| 暂不放行 | 3 | 3 | 6 | 无 |
| 放行 Segment 2 | 2 | 2 | 4 | P-2、D-4、D-5 |
| 放行 Segment 1 | 1 | 1 | 2 | P-1、D-2、D-3 |
| 放行 Segment 0 | 0 | 0 | 0 | P-0、D-0、D-1 |

此例中，当前组需要更新的成员全部完成目标版本更新并恢复可用后，才降低边界放行下一组。P 先完成而 D 尚未完成时，P RIS 的 partition 保持不变，不能提前进入下一组。若用户配置 `partition: 1`，则推进到 Segment 边界 1 后停止，保留 Segment 0。

partition 是准入结果的下发方式，不替代组级预算判断，也不要求单调降低。无 surge、预算耗尽时不再放宽；目标版本变化时可能需要收紧。

常规 Stateful 路径优先从已持久化的 RIS partition、目标版本和成员状态重建窗口，不预设新增持久化准入名单。部分下发、状态延迟、重启、暂停、更新中扩缩容及 surge 窗口衔接均需实现验证。Stateless 不在适用范围内，非 Segment 的 partition 行为不变。

#### 目标版本变化时的新模板与 partition 下发

目标从 B 改为 C 或回滚时，继续使用已有组级预算：

- 保留不可用与在途组的去重占用 `U ∪ I`，不因目标变化清空或提前释放。
- 按新目标、成员状态和剩余预算重算 partition，并保留用户分区限制。必要时收紧，保持旧值本身不构成预算保证。
- 预算耗尽时允许阻塞新增组；有剩余预算且满足其他条件时继续推进。坏版本不改变该规则，不增加特殊恢复机制，也不要求 B 必须先成功。
- 无 surge 时，组级预算耗尽会阻止继续放行健康 RI；partition 允许范围内、满足既有持续 unhealthy 条件的成员仍可能被修复。修复不能越过边界或绕过组级预算。

例如，无 surge、`maxUnavailable: 1`，Segment 2 已完成 B，Segment 1 的 P 已 Ready、D 未 Ready，此时目标改为 C。Segment 1 仍占满预算；RBG 必须重算并下发边界，阻止 P 因本地预算空闲而更新 Segment 2，避免两组同时不可用。

新模板与 partition 的配合仍需实现验证，覆盖预算为 1 和大于 1、各 RIS 部分下发、状态延迟及重启。沿用既有 surge 边界，不把无 surge 窗口用于阻断 surge 准备，也不新增任意选组协议或伪造 unhealthy 状态。

### 与 Topology 的组合

本 KEP 不设计 topology API，只定义组合语义：

> 如果 topology 约束作用于 segment 规则，其放置单位是完整 Segment：同一个 Segment 的全部 Pod 收敛到同一个拓扑域。

Segment 提供的契约是稳定子组身份；tier、pack、required/preferred 以及调度器翻译由 topology 设计定义。

### CoordinatedPolicy 的添加与删除

Policy 是可启用、可撤销的协同配置，不与 RBG 永久绑定。同名只是 RBG 查找 Policy 的规则，不表示所有权或生命周期绑定；删除或重建同名 Policy 后，RBG 会按新的对象身份重新校验和接管。创建、删除 Policy 或增删 Segment 配置时，即使 RBG 副本和业务模板不变，也必须触发对应 RBG 的 reconcile。只变更部分规则时，其他有效规则继续生效。

#### 已有 RBG 后添加 Policy

RBG 先执行[成员模式校验](#成员模式校验)，再检查当前有效副本、配比和存量成员。启用 Adapter 时，还须满足[副本控制权交接](#副本控制权与生命周期)。校验失败只阻塞接管，不转换模式或删除实例。

| 当前情况 | RBG 的处理 |
| --- | --- |
| 有效期望副本不满足配比 | 阻塞本次 Segment 接管并报告原因，不因新 Policy 静默取整、重排或删除已有成员。 |
| 有效期望副本满足配比，现有成员暂不完整 | 先接管已有成员的分组身份，包括不完整组中的成员；保持有效期望目标，沿用创建与调度机制补齐缺员，不等全部成员齐全或 Ready 才接管，也不因完整组数较少而缩容。 |
| 有效期望副本与现有成员均满足配比 | 保持现有成员运行，补齐分组描述与 label，无需仅为接管而扩缩容或重启。 |

期望满足配比，要求所有 role 的 `replicas / segment[role]` 为相同的非负整数，不只是各自能整除。校验通过后：

1. RBG 向 RIS 下发分组描述和执行参数，RIS、RI 通过[标签同步链路](#ri-与-pod-的-label-同步)接管已有成员，包括缺员组中仍在运行的 RI / Pod。
2. 保持有效期望与已下发目标，沿用创建链路补齐缺员。补齐仍受 scaling progression 和调度条件限制，不以成员先配齐作为接管前提。
3. 按组统计实际完整性和可用性；缺员组不可视为 Ready，也不能忽略其更新预算占用。

身份同步只 patch 受管 metadata，不触发业务 rollout 或 Pod 重建。给运行中的 Pod 补标签不会重新触发 Gang 准入，也不保证自动迁移到新拓扑域。

例如，已通过校验、ordinal 稠密的 Stateful RIS 配比为 `1P:1D`，期望 `2P、2D`，实际仅有 `P0、P1、D0`：

```text
Segment 0：P0、D0 → 成员完整，可用性另行判断
Segment 1：P1、缺少 D1 → 先为 P1 标记组身份，保留 P1
后续：RIS 按目标创建 D1，并赋予 Segment 1 身份
```

该例不因只有一个完整组就把 P 的目标缩到 1，也不先删除 P1 再重新创建。

分组描述已下发，且现有 RI 与 Pod 的身份和当前规则一致，即可完成接管；无需等待所有期望成员创建或 Ready。缺少的成员后续沿同一链路加入。接管或正常运行中的暂态缺员都不触发“先删除剩余成员再补齐”的清理。

#### 删除 Policy 后撤销协同约束

RBG 不再读取已删除 Policy 的规则做新的协调决策，但仅返回“无协调结果”并不足够：旧约束已写入 RIS 和其他受管对象，必须在后续 reconcile 中撤销或按当前有效配置重算。

| 处理对象 | RBG 及相关控制器的职责 |
| --- | --- |
| 有效策略与执行目标 | 整个 Policy 删除后按普通 role 配置计算；仅移除 Segment 配置或部分规则时按剩余有效 Policy 计算，不沿用旧组级 target 或更新准入结果。 |
| RIS 分组描述 | 撤销旧 Segment annotation，使下游在同一标签期望状态同步逻辑中移除 RI、已有 Pod 及后续 Pod template 中的受管组 label，不再依据旧描述生成或修复身份。 |
| RIS 副本与更新参数 | 根据当前有效副本目标、role 配置及剩余策略重新下发 `replicas`、`partition`、`maxUnavailable`、`maxSurge` 等受管字段；恢复当前应有值，不是统一清零，也不能因未生成新的协调参数而保留旧覆盖值。Segment 撤销后不再强制 `maxSurge: 0`，按剩余 Policy 或 role 配置恢复。 |
| Gang 与其他受管约束 | 重新计算相应 PodGroup、模板调度字段及已下发的其他约束，移除失效规则的部分，保留其他仍有效配置；不能无条件删除共享对象。 |
| Segment Adapter | 停止按失效规则写入 RBG 和执行副本 override，报告目标失效并清理受管 Adapter；成员不再属于有效 Segment 时，按各 role 的 `scalingAdapter.enable` 恢复普通 Adapter 流程，从当前 RBG 目标初始化，交接仍须隔离旧写入。 |

Policy 删除不等于 RBG 或 workload 删除，不把已提交的期望副本归零，也不为了取消组身份主动重启已有成员。撤销旧执行限制后，普通副本目标或更新配置可能继续驱动正常扩缩容、更新，不能因此承诺所有已有 Pod 永远不变。

撤销遵守以下边界：

- 标签通过原同步链路异步清理，不阻塞 Policy 删除或停止协同，也不为此重启 Pod。失败、重试和重启后，控制器按当前配置继续收敛，不保存独立清理任务。
- 当前有效 Policy 和分组描述决定策略是否生效，残留标签不能继续驱动旧策略。只看 Pod 标签的外部消费者接受同步延迟；需要识别策略失效时，还须观察 Policy、分组描述并校验归属。
- 执行中的扩缩容、更新不要求先完成旧组或等待 Ready 才能撤销，也不自动回滚已执行操作。RIS 继续统计真实不可用状态，RBG 不沿用失效的组级预算与准入结果。
- 跨对象操作最终一致，删除瞬间不保证取消所有已发出的操作。读取 Policy 失败不等于确认其不存在，不能据此执行撤销。

例如，同一组 P 已完成更新、D 尚未完成时删除 Policy：不回滚 P，也不等待 D 完成才撤销；RBG 按当前 role 配置重新下发更新参数，RIS 继续执行。如果只更新了 P 的 RIS 配置就重启，下次 reconcile 继续将各 RIS 收敛到当前配置。该场景作为部分下发、重试及重启恢复的验证用例。

#### 重新添加与规则重命名

Policy 是可插拔的协同能力。配比不可变仅限制已声明 Segment 的原地修改，不扩展为历史配比限制；撤销后重新添加可使用相同或不同配比，统一复用已有 RBG 的接管流程，不为此保存历史配比记录。

| 操作 | 处理方式 |
| --- | --- |
| 同配比重新添加 | 按当前副本、规则及控制权条件重新校验和接管，不沿用旧准入结果。 |
| 撤销后以不同配比重新添加 | 按新配比校验当前有效期望副本；满足共同组数条件则接管，不满足则阻塞，不自动取整或调整副本。 |
| 规则重命名 | 仅在 Segment map 不变时允许。按当前规则重新校验并同步分组描述、label，成员配对保持不变，rule ID 与标签通过同一同步逻辑收敛；同一次更新同时改变规则名和配比必须拒绝，并拆分为撤销后另行添加。 |

同名 Policy 删除重建后，其 UID 已变化，应按新的 Policy 实例重新校验、接管并更新 RIS 分组描述，不沿用旧绑定或旧准入结果。`policyUID` 仅用于区分对象身份，不表示永久冻结协同关系；残留 label 也不能作为接管已完成的依据。

规则重命名还存在一个 Gang 收敛边界。新规则名派生新的 `ruleID`，全集群所有受管的 `segment` 与 `segment-rule` 标签值都会重新键值，Volcano `SubGroupPolicy` 也会按新的 `segment-rule` selector 重新生成。label 同步是异步的：在所有 Pod 收敛之前，仍携带旧标签值的 Pod 不再匹配重新生成的 subGroup selector，也不会计入 `minSubGroups` 或 `minMember`，因此重命名窗口内待调度的 Pod 可能出现瞬时无法放行的情况。该窗口由现有 metadata 同步链路收敛：不需要重启 Pod 或删除 PodGroup，标签收敛后 gang 调度即可重新满足条件。活跃 PodGroup 下的重命名不是原子操作，用户应预期这一暂态的 gang 统计窗口。

重新接管可能改变成员配对，不保证应用状态或路由无缝迁移。身份同步不重启或删除成员，也不要求迁移到独立 workload。控制器直接按当前配置计算期望，无需先清完旧标签；重试与重启后的收敛仍需验证。

### 测试计划

以下是实现后的验收要求，尚未执行。单元测试验证计算与校验规则，集成测试验证跨控制器状态收敛，端到端测试验证集群中的实际行为。

#### 前置测试更新

- 保持现有非 Segment CoordinatedPolicy scaling 测试可通过，保留原 `maxSkew` 与 progression 行为。
- 保持非 Segment 的 Stateless RIS 创建、扩缩容和滚动更新测试可通过，不将组合限制扩大为全局禁用 Stateless。
- 保持现有非 Segment gang 测试可通过，包括按 Gang minimum 抬升 scaling target；为 Segment 增加不抬升 target、由配置校验排除冲突的独立断言。
- 保持现有 RIS surge 单元与端到端测试可通过，不因增加 Policy 字段改变 RIS 的实际 surge 算法。
- 为 segment 与 scaling 的组合增加回归测试，确认首次创建不会退化为实例粒度。

#### 单元测试

按以下功能验证正常输入、非法输入和边界状态。

##### API 校验

- `segment` 的 key 集合与 `roles` 不一致、value 小于等于 0 或仅包含一个 role 时拒绝。
- `roles` 中存在重复项时拒绝，即使去重后的 key 集合与 `segment` 一致。
- Segment 成员出现在其他 Segment 或普通规则时拒绝，包括双方使用不同策略域的情况；覆盖创建、更新与规则排列顺序变化。
- 相关 role 合并为一条 Segment 规则并声明完整配比时接受；不涉及 Segment 成员的普通规则保持原有重叠处理行为。
- 关联 RBG 已存在时，向 Stateless RIS 成员添加 Segment 被拒绝；覆盖全部 Stateless 和 Stateful/Stateless 混用，不受成员顺序影响。
- 非 RoleInstanceSet、未知后端、Deployment、StatefulSet 和 LeaderWorkerSet 均拒绝；仅接受原生 Stateful RoleInstanceSet 成员。
- Segment 成员存在用户 `excluded-ordinals` 或子 RIS 存在非空 `committed-skip-ordinals` 时拒绝；非 Segment 的选择性成员行为保持不变。
- 已有 Segment 成员改为 Stateless 被拒绝；所有 RIS 成员均为 Stateful 且其余条件满足时接受，非 Segment 的 Stateless role 保持原行为。
- `minSegments` 与 `minReplicas` 同时配置、`minSegments` 非法或用于未声明 `segment` 的规则时拒绝。
- `segBatchSize` 显式为零、负数、百分比或用于未声明 `segment` 的规则时拒绝；正整数接受，字段在 API、CRD 和生成代码中保留，Segment scaling 缺省按 1 处理。
- Segment 同时配置 scaling 与 Gang 时，省略 `minSegments` 被拒绝；`segBatchSize: 1、minSegments: 2` 以及省略批量但门槛为 2 的组合被拒绝，`segBatchSize: 2、minSegments: 2` 等满足 `B >= G` 的组合通过。
- scheduler-plugins 下的 Segment Gang 被拒绝；Volcano 下允许通过 Policy 准入，实际 `subGroupPolicy` 能力由 reconcile 再检查。
- 修改已声明 segment 的 value 或 key 集合被拒绝。
- 同一次更新同时改变规则名和 Segment map 时拒绝，要求拆分为撤销后另行添加。
- 覆盖 rule ID 冲突，并验证长规则名、大组序号下生成的 label value 仍是合法 Kubernetes label value。
- Segment rolling update 的有效组级 `maxUnavailable` 与 `maxSurge` 同时解析为 0 时仍接受；无 surge 时不开始会增加不可用实例的更新。
- 验证更新后的 CRD 中 `AdapterScaleTargetRef.role` 可选，且 `role` 与 `segment` 必须二选一。
- 配比不变时允许修改策略参数；添加、删除及重新添加 segment 遵循接管与撤销规则，不以历史配比拒绝新配置。
- Policy `maxSurge` 的整数、百分比、显式零值与未配置在 API、CRD 和生成代码中正确保留，不将未配置一律默认成 `0` 而丢失非 Segment 的回退语义。

##### 组指派

- 按整数除法将实例序号映射为组序号，同一 RI 的所有 component Pod 具有相同组身份。
- 验证确定性 12 位十六进制 rule ID、`segment-rule` 标签和短 `segment` 标签值。
- label 正确写入 RoleInstance 与 Pod template。
- 用户修改受管 label 后，reconcile 恢复期望值。
- Pod 重建后 label 不漂移。
- 固定配比下，同一 ordinal 的 RoleInstance 删除重建后组身份不漂移。
- RIS annotation 正确传递 Policy UID、规则名和当前 role 的配比，不将具体组 label 写入 RIS 公共模板。
- RIS annotation 同时传递用于标签和 selector 的确定性 rule ID。
- replicas 不变时仍同步已有 RI 与 Pod 的 label，metadata 变化可触发下游 reconcile。
- label 的添加、更新、移除及无差异时不操作使用同一期望状态同步逻辑，幂等且保留其他 metadata，不触发业务 rollout 或 Pod 重建。
- 仅增删改受管 Segment 标签时，ControllerRevision hash 和 `updatedReplicas` 不变。
- RIS 分组描述移除后，RI、已有 Pod 及后续 Pod template 不再保留受管组 label，之后重建 Pod 不恢复旧标签。
- annotation 和 label 的移除可触发下游同步；读取或解析描述失败不按无分组清理。

##### Policy 生命周期

- Policy 创建、删除以及添加或移除 Segment 配置，在 RBG replicas 和业务模板不变时也触发接管或撤销。
- 首次添加、重新添加及 reconcile 重试均检查成员模式；发现 Stateless RIS 时停止整个 RBG reconcile，不下发有效分组描述、不创建对应 Segment Adapter，也不转换模式或删除已有实例。
- 稀疏成员、用户 exclusion 和非原生 Stateful 后端阻塞接管，不删除实例或改写成员记录。
- 首次接管时，有效期望副本不满足共同配比则阻塞且不因新 Policy 重排或删除已有成员；满足配比时，成员完整与否都可先同步已有 RI / Pod 的分组身份。
- 期望 `2P、2D`、实际 `2P、1D` 时，保留不完整组中的 P，保持有效目标并由 RIS 补齐 D；不按当前完整组数回退目标或先清理再重建。
- 分组描述与现有成员身份同步完成后可完成接管，不等待全部期望成员创建或 Ready；缺员组计为不完整、不可用，后续成员继承正确组身份。
- 接管不绕过原有创建、scaling progression 和调度限制；身份同步及正常暂态缺员都不触发接管式清理。
- 删除后撤销 RIS 分组 annotation，按当前有效配置恢复副本、更新及调度参数；保留用户配置和其他仍有效规则，不统一清零或误删共享对象。
- 旧 label 尚未清理时不继续执行失效组策略；Policy 读取失败不按已删除处理。
- 标签清理不阻塞 Policy 删除，失败重试及 controller 重启后仍由同一同步逻辑最终收敛。
- 同配比重新添加、不同配比重新添加及规则重命名均按当前配置校验、接管；新配比合法但当前副本不满足共同组数时阻塞，不自动调整副本；不查询或限制历史配比。
- 重复 reconcile、冲突重试、controller 重启和同名 Policy 重建后仍检查当前配置及对象身份，通过同一同步逻辑收敛描述与标签，不依赖独立清理任务。
- 撤销时 P 已完成更新、D 尚未完成，不回滚 P、不等待 D Ready；只完成部分 RIS 参数下发就重启时，后续按当前配置继续收敛，不沿用失效组预算或重新执行旧撤销步骤。

##### Scaling

- 有效期望满足共同整数组数时，在 Segment 数量空间计算共同 target，再按配比生成各 role 的实例 target。
- 扩缩容均以完整 Segment 为单位，target 不超过合法期望的组数。
- 首次创建使用 `2P:1D`、`P=5、D=2` 等不满足配比的期望时，报告各 role 剩余实例，不创建 `4P、2D` 或其他 Segment 成员。
- 已生效 Segment 的后续副本变更不满足配比时，保持各 RIS 当前合法目标，不部分扩缩；添加或重新添加 Segment 时阻塞接管，不切换 Adapter 控制权。
- 配比错误在写入任何 Segment 或 role 目标前停止整个 RBG reconcile；已有 RIS 对象不变，其 controller 继续维护已有实例；修正期望后恢复处理。
- 首次创建与后续扩容统一使用 `segBatchSize`，缺省每批 1 组；目标 5 组、批量 2 时按 `0 → 2 → 4 → 5` 推进，最后一批不要求满额；没有 scaling 策略时直接下发完整目标。
- Segment 的 `scaling.maxSkew` 无论省略、零值或其他合法整数、百分比均不影响 target；批量字段不作用于缩容或 surge。
- `OrderScheduled` 检查当前批次全部成员的预期 Pod 已调度，`OrderReady` 检查全部成员 Ready；多 component、缺员、部分 role 已达到最终副本数但当前批次未 Ready 时仍按批次判断。
- 当前批次未完成时保持已下发目标，不按 Ready 或实际完整组数回退，不在重复 reconcile、部分下发或状态延迟时提前增加下一批。
- Segment scaling 不读取 Gang minimum 来抬升 target，也不自动修改批量；`segBatchSize < minSegments` 的静态冲突由准入拒绝，绕过准入的组合由 reconcile 报告。
- 部分 role 扩缩被拒绝，不通过向下取整部分执行。
- desired replicas 不整除或各 role 组数不同时报告剩余实例并停止整个 RBG reconcile，地址发现不更新，不发布剩余身份。

##### ScalingAdapter 状态与控制权

分别验证配置与目标提交、控制权交接、状态同步。

###### Adapter 配置与目标提交

- `strategy.scaling.scalingAdapter` 复用现有 `ScalingAdapter` 类型，`enable` 默认 `false`，`labels` 正确传递且不能覆盖受管标识；API、CRD 和生成代码保留该配置。
- 仅为启用配置的有效 Segment 规则自动创建一个 Adapter，关联当前 RBG，重复 reconcile 不重复创建，Adapter 意外丢失后可恢复；未配置或未启用时不自动创建。
- `role` 与 `segment` target 二选一；无效 RBG、Policy、规则或成员不触发部分写入，原 role 模式保持兼容。
- 验证 CRD 将 `role` 改为可选，同时拒绝两者均设置或均未设置，并保持既有 role-only 客户端兼容。
- Segment `spec.replicas` 使用组级绝对目标，覆盖扩容、缩容、重复 reconcile、非法组数和投影溢出。
- 全部成员副本在一次 RBG 更新中提交；冲突重试重新读取目标，保留无关 role 和字段，重试与重启不重复累加副本。
- RBG 分批下发 RIS target 时，不把中间目标回写为 Adapter 或 RBG 的最终期望。
- 未显式指定组数时，仅从满足固定配比的有效期望副本初始化；不满足配比时阻塞且不静默取整，显式组数不被无条件覆盖。

###### Adapter 控制权与恢复

- Segment Adapter 接管后清理当前 RBG 的成员 role Adapter；成员 role 的自动创建开关仍为 `true` 时也不重新创建，非成员 role 与其他 RBG 的 Adapter 不受影响。
- 旧 role Adapter 无新请求时仍可能执行 override，接管必须同时隔离持久写入与内存覆盖；覆盖旧对象删除延迟、同步重试与重启，并防止重复 Segment Adapter 争用。
- Adapter 配置关闭、Policy 或规则失效后停止旧 Adapter 写入和 override，清理失效受管 Segment Adapter，不自动清零 RBG 期望；对象身份变化不能沿用旧绑定。
- role 仍属于有效 Segment 时，无论 Segment Adapter 未启用、关闭还是意外丢失，成员 role Adapter 都不恢复创建或写入。
- role 不再属于有效 Segment 时，按其 `scalingAdapter.enable` 决定是否恢复普通 Adapter 流程；初始副本来自当前 RBG 已提交目标，不恢复旧 Adapter 历史值，不取 RIS 中间 target；其他有效策略仍生效。

###### Adapter 状态与 scale 映射

- `status.replicas` 仅在全部成员目标成功提交到 RBG 后更新，目标校验或提交失败时不提前推进；不随 RIS 分批 target 或实际创建数量变化。
- RBG 写入成功而 Adapter status 写入失败时，重复 reconcile 或重启后补齐状态；目标相同时仍执行必要的状态同步，不重复累加副本。
- 请求 4 组、目标已提交、本轮推进到 3 组、实际 2 组 Ready 时，Adapter 的 `spec.replicas`、`status.replicas`、`status.readyReplicas` 分别为 4、4、2。
- Ready 组数按组身份与成员实际状态统计，缺员或成员未 Ready 时不计入，不能将不同组的 Ready 成员拼为完整组；目标不变时仍同步 Ready 数变化。
- 通过 `/scale` 写入期望组数、读取已提交目标，验证其与 Adapter 副本字段映射一致；HPA 使用方案、通用 Pod 选择和指标聚合、去重不作为本期验收要求。
- 验证 Segment Adapter selector 精确等于 group-name 与 segment-rule 的 AND selector，不匹配其他 RBG、规则、非成员 role 或单个 component。

##### Rolling update

分别验证组级准入、预算恢复与下发到 RIS 的执行参数。

###### 组级准入与预算恢复

- 同一 Segment 同批获准更新；组级预算大于 0 时，获准组内不再按同一个值限制 RI 并发，允许成员同时或先后启动、Ready 以及暂态新旧版本共存。
- `partition` 保留完整 Segment，不按各 role 独立保留相同实例数。
- Stateful 常规更新将共同 Segment 边界按配比投影为各 RIS 的 partition，并始终保留用户要求的旧组。
- 在固定目标版本、`maxUnavailable: 1` 的逐组推进示例中，当前组未完成或共享预算不足时，后续 reconcile 保持边界；满足推进条件后才放宽，较快 role 不得单独提前进入下一组。
- 成员可用性沿用 RIS 判断；原地更新未完成、未满足 `minReadySeconds` 或组内仍有未更新成员时，不因瞬时 Ready 或 `updatedReplicas` 增加而释放在途占用。
- 无 surge、`maxUnavailable: 2` 时，无其他占用且满足推进条件可同时准入两组；一组完成后即可补一组，不等待整批完成；覆盖占用为 0、1、2 以及不可用与在途集合重叠的情况。
- 一组因成员失败持续占用、另一窗口可用时，仍可放行其他符合条件的组；占满预算后不再放宽边界，目标变化时必要收紧，恢复可用并完成目标版本后补位，不因单组失败冻结整个 RBG。
- RBG 使用组身份和成员状态计算预算，不通过改写健康 RI / Pod 的 Ready 状态传递组级不可用信息。
- Policy 的 `maxUnavailable` 直接以 Segment 计数，无需额外更新模式开关；Segment 下 `rollingUpdate.maxSkew` 的省略、零值及其他合法值均不改变共同边界和组级预算，`segBatchSize` 不限制更新窗口。
- 组级 `partition` 和 `maxUnavailable` 百分比向下取整，`maxSurge` 百分比向上取整；覆盖 0、1 和边界百分比。
- 同一 Segment 的多个成员不可用只计一次，不同 Segment 的不可用成员分别计数。
- 无 surge、`maxUnavailable: 1` 时，不允许各 RIS 独立推进更新而同时使两个 Segment 不可用。
- 已放行但尚未反映到成员状态中的更新占用预算，不因状态延迟重复放行。
- 目标从 B 改为 C 或回滚时，保留不可用与在途 Segment 的去重占用，按当前目标重新计算 partition；不因目标变化重新获得一份预算，也不以保持旧 partition 代替组级校验。
- 坏版本占满预算时允许阻塞新增组更新，不额外要求 B 必须先成功；位于 partition 范围内且满足 RIS 既有条件的 unhealthy 成员仍可尝试修复，但不能绕过边界或扩大组级占用。
- 外部故障导致预算耗尽或超限时，不再放行会扩大不可用 Segment 集合的更新。

###### 更新参数与 surge 投影

- Policy `maxUnavailable` 的百分比只以期望完整 Segment 数为基数解析组级预算，不再交给 RIS 按各自副本数二次解析。
- 组级预算 `B > 0` 时忽略 role 原值并向各 RIS 下发 `100%`，不按配比放大 Policy 原值；`1P:2D`、Policy 值为 `1` 时，一个获准 Segment 的 P、D0、D1 都可进入更新，整组只占用一份组级预算，不要求实际同时启动或 Ready。
- 组级预算 `B = 0` 时向各 RIS 下发 `0`，不能为绕过零预算而下发 `100%`；无 surge 时不主动增加不可用实例，有 surge 时仅由实际可用额外实例增加执行预算。
- Policy 未配置 `maxUnavailable` 时保留各 role 配置或 RIS 默认值，不新增或反推组级默认预算，仍保持 Segment 更新边界和其他协调条件。
- 未声明 segment 的规则继续使用各 role 独立的不可用预算。
- 非 Segment 的 Policy `maxSurge` 显式配置时原样覆盖 role，未配置时保留原行为，显式 `0` 不等同于未配置。
- Segment 的 Policy `maxSurge` 忽略 role 配置；`1P:2D`、组级值为 `1` 时下发 `P=1、D=2`，未配置或移除字段后显式下发 `0`，撤销整个 Segment 后恢复当前普通配置。
- `maxSurge` 百分比按期望完整组数向上取整后再乘配额，覆盖零副本、取整边界和乘积溢出，不能先按各 RIS 的副本分别取整。
- 保留 Stateful 实际 surge 算法：完整更新范围可创建上限数量，较小 partition 范围可能创建更少或不创建；已有有效 surge 的保留与回收仍由 RIS 决定，不增加跨 role 补齐与统一回收要求。
- `maxUnavailable: 0、maxSurge > 0` 时 RIS 收到 `0`，沿用先准备额外容量、待其可用后更新健康基础实例的机制；RBG 不因简化组级窗口为零而阻断 surge 准备，RIS 不将配置上限或未 Ready 的额外 RI 当成可用预算。
- 实现 surge 准入规则：每个 role 至少拥有配额数量的健康目标版本 surge RI 时组才可 surge 放行，否则必须消耗不可用预算；覆盖 surge 容量消失和从安全在途转为 loss-risk 的场景。
- surge 的创建路径同样写入 RI 与 Pod 分组 label；额外成员不完整时不将其误判为完整可服务组，不因局部 surge Ready 就释放基础 Segment 的在途占用。

##### Gang

- 每条 Segment 规则只生成一个 SubGroupPolicy，使用 group-name 与 segment-rule selector，`matchLabelKeys` 只包含 `segment`，不为每个 Segment 创建独立 PodGroup。
- `subGroupSize` 按整组全部 RI、全部 component Pod 计算，`minSubGroups=minSegments`，`minMember=minSegments×subGroupSize`；覆盖多 component 与多 Segment 规则累加。
- 资源至少满足 `minSegments` 个完整 Segment 时才允许调度放行，资源不足或调度约束不满足时不放行。
- 从零创建无需等待成员 Ready，放行后允许成员先后启动。
- 未配置 scaling 时，省略 `minSegments` 要求全部期望完整 Segment；同时配置 scaling 时省略该字段会被准入拒绝。
- `minSegments` 超过期望完整组数时报告不可满足，不自动扩大 desired。
- `segBatchSize: 1、minSegments: 2` 被准入拒绝；批量 2、门槛 2 的组合不抬升 target，在调度及 progression 条件满足后继续推进。
- 将 Segment 规则翻译为一个 Volcano subgroup，验证 group-name 与 segment-rule selector、segment match key、每组 Pod 总数和 `minSegments`；多条规则保持隔离。
- scheduler-plugins 的 Segment Gang 被拒绝；Volcano CRD 缺少 `subGroupPolicy` 时报告不兼容，不降级为 Pod 总数或 per-role 数量门槛。
- 未声明 Segment 的规则继续使用 `minReplicas` 和现有 target 抬升逻辑。

#### 集成测试

以下场景验证控制器之间的参数下发、事件触发和最终状态。

##### 成员模式与组身份

- 所有 RIS 成员均为 Stateful 且其余校验通过时，创建带 segment 的 CoordinatedPolicy 后，RoleInstance 与 Pod 均带 label。
- 覆盖先创建 RBG 再添加 Segment、先创建 Policy 再创建 RBG 两种顺序，Stateless 成员组合均不能生效；有效 Segment 的成员切换为 Stateless 被拒绝。
- 模拟跳过准入后出现不合法组合，验证 reconcile 停止并报告具体成员，不下发有效分组描述、不自动转换模式或删除已有实例；已有实例（含非 Segment 的 Stateless role）继续运行，不因停止被删除或改写。
- Pod 重建后 label 不漂移。

##### Adapter 目标与控制权交接

- 在 Policy 的 `strategy.scaling.scalingAdapter` 下启用配置，验证 RBG 自动创建并维护 Segment Adapter、同步自定义 labels；成员已有 role Adapter 被清理且不因原开关仍开启而重建，非成员 role Adapter 保持不变。
- 覆盖 Segment Adapter 意外丢失、成员 role Adapter 删除延迟及 controller 重启，验证恢复仍按有效配置收敛，旧持久写入与 override 不覆盖组目标。
- 通过 ScalingAdapter 按 `1P:2D` 从 2 组请求扩至 4 组，验证 RBG 最终期望与 RIS 分批执行目标保持区分；状态满足推进条件后继续收敛。
- 覆盖 Adapter 更新冲突、重复 reconcile、controller 重启、旧 role Adapter 控制权交接，以及 Policy 删除后不再按旧组目标写入且不误缩容到零。
- 区分只删除 Segment Adapter 对象、关闭 Adapter 配置但保留 Segment、移除 Segment 配置三条路径；前两者不恢复成员 role Adapter，最后一种按各 role 开关恢复。验证新 role Adapter 以当前 RBG 目标初始化，保留其他有效策略，交接期间旧 Segment Adapter 不覆盖新目标。

##### 创建批次与 progression

- role 粒度扩缩被拒绝并生成 condition。
- Segment 创建分别覆盖缺省批量 1 与 `segBatchSize: 2`，验证目标 5 组的最后一批为 1 组；当前批次未满足 progression 时不推进，部分下发、重试或重启不额外放行。
- 当前批次全部 Scheduled 但一个成员未 Ready 时，`OrderScheduled` 推进、`OrderReady` 等待；缺失 component Pod 时不能误判调度完成。
- 验证 Segment 下两个 `maxSkew` 的配置变更不影响创建批量或更新窗口，非 Segment 原有进度协调保持不变。

##### 组级更新与 surge

- 滚动更新按 Segment 批次准入，`partition` 和 `maxUnavailable` 使用 Segment 单位，`rollingUpdate.maxSkew` 被忽略。
- 在固定副本、固定目标版本、无 surge 的 Stateful 场景中，验证 RBG 计算 partition、RIS 执行、状态反馈、RBG 重新计算的完整闭环；覆盖 3P、6D 按 1P:2D 逐组推进的边界序列。
- 覆盖 Policy 值为 `1` 时 P、D RIS 均收到 `100%`，一个获准 Segment 的全部成员都可进入更新，不再要求两个 D 串行轮换；整组按 RIS 判断完成更新并恢复可用前，RBG 不放行下一组，实际启动和 Ready 顺序不受保证。包含原地更新和 `minReadySeconds` 等待。
- 覆盖无 surge、Policy 值为 `2` 时同时准入两组、一组完成后补位，以及一组持续失败时另一窗口仍可推进；Policy 未配置时保留局部窗口且不新增组级默认预算。
- 覆盖 Policy `maxSurge` 从 API 到 RBG 计算、RIS 参数覆盖和额外 RI 创建的链路：非 Segment 原样覆盖，Segment 按配比投影；下发上限正确不等于每轮必须创建满额。
- 覆盖较小 partition 下各 role 实际额外数量不成组、独立回收以及 surge RI / Pod label 同步；基础组仍遵守共同边界和有效执行预算。
- 覆盖 Segment Policy `maxUnavailable: 0、maxSurge: 1`：各 RIS 收到 `maxUnavailable: 0`，在用户 partition 和其他协调条件允许、旧成员健康时，候选更新范围可触发 surge 创建；额外 RI 未可用时不更新健康旧实例，可用后按 RIS 原预算机制推进，不新增零预算冻结规则；同时验证组级状态和在途记录不因局部 Ready 被提前清除。
- 一个 role 更新较快、另一个较慢或失败时，不可用 Segment 的并集不因各 RIS 独立推进而突破已配置的共享预算。
- controller 重启或成员状态延迟时，共同准入恢复后不重复放行，也不丢失在途更新的预算占用。
- 无 surge、`maxUnavailable: 1`，Segment 2 已完成 B，Segment 1 的 P 已 Ready、D 未 Ready，此时目标改为 C：验证新模板与重算后的 partition 阻止 P 因本地预算空闲而更新 Segment 2，避免两组同时不可用；覆盖各 RIS 部分下发、状态延迟和重启。
- 连续更新、坏版本与回滚覆盖预算为 1 和大于 1 的情况：占满时阻塞新增组，有剩余预算且满足条件时允许推进；不清空旧占用、不强制等待 B 成功，不改写真实健康状态；验证允许范围内的 unhealthy 修复沿用 RIS 条件。

##### Gang 资源准入

- Gang 仅检查资源准入，不等待应用 Ready。
- 在 Volcano 中验证同一 Segment 标签下不同 role、不同 RI 的 Pod 被划为一个 subGroup；不同 Segment 的 P、D 即使各自数量足够，也不能满足门槛，至少 `minSegments` 个身份一致且成员完整的组才可放行。
- 从零创建时使用 `segBatchSize: 2、minSegments: 2`，验证首批目标保持为 2 组、资源准入后按 progression 推进；最后一批为 1 组时不重复要求首批门槛。
- 模拟旧配置绕过准入以及 Adapter 将期望组数缩到 `minSegments` 以下，验证 reconcile 报告配置错误、RIS 继续保持上一次有效组目标，并在用户修正期望组数或门槛前阻塞该缩容；资源不足时保持正常 Pending，不误报为配置不兼容。
- 未配置 scaling 时省略 `minSegments` 仍要求全部期望组；同一 RBG 的非 Segment 规则不受影响。
- 在 Volcano PodGroup 的 `minSubGroups` 处于生效且有 Pod 等待 gang 放行时重命名规则，验证 subGroup selector 按新 rule ID 重新生成、收敛期间旧标签 Pod 不再匹配，以及标签同步完成后放行恢复，全程不重启 Pod、不删除 PodGroup。

##### 地址发现

- 当有效期望副本违反配比（例如 `2P:1D` 且 `P=5、D=2`）时，验证 reconcile 在更新地址表前停止：不出现 `prefill-4` 等剩余实例地址，保留上一次对齐地址表；同时验证不把 surge 身份发布为基础可服务成员。
- 滚动更新和 surge 创建期间，验证地址发现输出只随实际下发的基础组目标变化，任何剩余实例或 surge 身份都不会被发布为可服务成员。

##### Policy 接管与撤销

- 原地修改配比被准入拒绝，同一有效配置下的实例重建保持组身份；撤销后以相同或不同配比重新添加按当前目标校验，符合条件时同步新身份，不受历史配比限制。
- 未声明 segment 且未配置新增 Policy `maxSurge` 的规则保持现有行为；显式配置时仅增加相应参数覆盖。
- 先创建 RBG，再添加 segment 规则；有效期望副本和现有成员满足配比时，即使 replicas 与业务模板不变，也能经 RIS annotation、RI label、Pod label 链路完成同步，且不重启已有 Pod。
- 后添加规则时，期望为两个 `1P:1D` 组、实际只有 `P0、P1、D0`，验证已有成员先完成身份同步，P1 及其 Pod 不因接管重建，目标不降为一组；允许接管完成而 Segment 1 仍缺员，随后在创建及调度条件满足时补齐 D1 并继承组身份。
- 覆盖现有成员未 Ready、metadata 分批同步、接管过程中 controller 重启，以及新成员在同步期间创建的情况；接管不等待全员 Ready，重复执行不漏标或触发非预期缩容，缺员和不可用状态统计保持正确。
- 在已下发分组描述和协调更新参数后删除 Policy，验证旧 annotation 和参数覆盖被撤销、普通配置继续生效，且不因残留 label 继续按旧规则协调；部分规则移除不影响其他有效规则。
- 在 P 已完成更新、D 尚未完成时撤销 Policy，不等待整组 Ready 或回滚 P；模拟仅 P 的 RIS 参数恢复后失败或重启，验证各 RIS 最终按当前配置收敛，D 的实际不可用状态仍由 RIS 统计。
- 同名 Policy 删除重建、Segment 重新添加及规则重命名后，验证当前身份重新校验，已有标签按新配置同步；配比不符时非破坏性阻塞，不自动调整副本，不依赖历史配比记录或旧标签清理完成。
- 在 replicas 与业务模板不变时，通过同一同步链路验证标签添加、更新、移除及无差异时不操作；撤销后已有 RI / Pod 的组标签最终清除，保留其他 metadata，不因标签同步触发重建，后续新建或重建 Pod 不恢复旧标签；覆盖同步中途失败与 controller 重启。

##### controller 降级准备

- controller 降级准备期间保留新 controller，验证移除目标版本不支持的配置后，RIS 执行参数及 Gang 约束按当前普通配置恢复、Segment Adapter 清理和副本控制权交接完成，RIS 分组描述及 RI、Pod、Pod template 的受管组标签清除。
- 覆盖降级准备中参数恢复、Adapter 交接或标签同步未完成的场景，此时不满足切换旧 controller 的前置条件，应继续由新 controller 收敛；即使业务 Pod 未 Ready，以上准备完成后仍可切换，不为清理标签重建 Pod。

#### 端到端测试

- 运行 PD 分离推理示例，验证 RI 与 Pod 的组身份符合 prefill / decode 配比。
- 从 router 消费的 ConfigMap 出发端到端验证地址发现：期望副本违反配比时 reconcile 在 ConfigMap 更新前停止并保留上次对齐身份；存在 surge 实例期间只发布对齐的基础组身份，不发布剩余或 surge 身份；期望副本修正后恢复为目标地址。
- 执行 Segment 粒度扩缩，验证最终副本满足配比、创建批次遵守 progression；分别验证首次创建和已生效 Segment 的非法配比会停止 RBG reconcile，修正后恢复。
- 执行 Segment 粒度滚动升级，验证共同 partition 和组级预算限制实际更新范围；正值预算下获准组内不按 Policy 原值轮换 RI，零值与 surge 组合仍不增加不可用实例。
- 通过非 Segment Policy 配置 `maxSurge: 2`，在 `replicas=4、partition=0`、初始全部健康旧版本的条件下，分别覆盖 `maxUnavailable: 0` 与 `2`，验证出现两个额外 RI 并最终回收至 4 个；复用现有 RIS surge E2E 场景，不扩展为任意 partition 下必须创建两个的断言。
- Segment Policy 的上限投影、role 配置被覆盖以及未配置时归零；验证各 RIS 按本地算法执行，不要求额外 RI 构成完整组。
- 与 gang 调度组合：批量与门槛匹配时正常推进，不匹配时保持等待而不自动改写 target；覆盖默认 Gang 门槛及用户调整配置后的恢复。
- 已有 RBG 接管与功能撤销，以及新 controller 完成降级准备后再切换旧 controller 的场景；验证旧 Segment 目标和标签不再恢复、普通副本控制继续生效，不包含 API 降级兼容测试。

### 阶段准入条件

各阶段均需满足前一阶段的条件。验收完成前，不将设计能力视为已发布功能。

#### Alpha

- API、校验、组指派与 label 持久化已实现。
- scaling、rolling update 和 Gang 均完成 Segment 粒度适配。
- 单元与集成测试覆盖核心语义。
- 未声明 segment 且未使用新增 Policy `maxSurge` 的规则行为不变。

#### Beta

- ScalingAdapter 支持 segment target。
- 状态与 condition 完整。
- 迁移与回滚测试覆盖。
- topology 组合语义与 router co-design 文档明确。
- 至少一个真实 PD 分离推理 workload 验证。

#### GA

- 两个及以上真实用户使用。
- 两个 release 无正确性问题。
- 升级、降级、迁移路径稳定。
- 文档、示例、监控和故障排查指南完整。

### 升级与降级策略

#### 迁移与兼容

存量接管、撤销和重新添加统一遵循[Policy 生命周期](#coordinatedpolicy-的添加与删除)。修改副本、镜像或策略参数不改变当前配比；调整配比需先撤销，再按新配置接管。

Segment 不接管 Stateless 成员，也不自动改变成员模式。撤销后的模式变更仍受原有 workload 校验限制，不承诺原地切换模式。

#### 升级

- 新字段全部可选。
- 未声明 segment 且未使用新增 Policy `maxSurge` 的规则行为保持不变。
- controller 可逐个 policy rule 启用 Segment。

#### controller 降级

只切换控制面，不回退 API 版本或 CRD schema：

1. 保持新 controller 运行，按[删除 Policy 后撤销协同约束](#删除-policy-后撤销协同约束)流程移除 Segment 配置，并移除目标旧 controller 不支持的关联字段或规则，例如 `minSegments`、`segBatchSize`、Segment Adapter 配置及旧版本不支持的 Policy `maxSurge`。需要保留的普通协调配置应使用目标版本支持的形式，不直接沿用组级计数语义。
2. 由新 controller 按当前有效副本目标、role 配置和剩余有效策略恢复 RIS 的 `replicas`、`partition`、`maxUnavailable`、`maxSurge` 及 Gang 等受管约束。恢复当前应有值，不统一清零，也不遗留旧 Segment 的参数覆盖。
3. 完成 Segment Adapter 的停止写入和清理；成员不再属于 Segment 后，按原开关和正常绑定条件恢复 role Adapter，并从当前 RBG 已提交目标初始化。持久写入与内存 override 两条路径均完成交接，不恢复历史目标。
4. 通过既有同步链路撤销 RIS 分组描述，清除已有 RI、Pod 和后续 Pod template 的受管组标签，确保后续新建成员不再继承旧身份。不依赖旧 controller 接手后继续清理。
5. 确认以上准备完成后，再切换到旧 controller。任一步尚未完成时，保留新 controller 继续 reconcile 或重试；不要求业务全部 Ready，也不为清理标签重启或重建 Pod。

这是运维切换旧 controller 的手工前置流程，自动降级和独立清理机制不在本设计中引入。普通功能撤销允许标签异步清理；切换旧 controller 前需要确认收尾完成。

例如，删除 Segment 后 D Pod 仍未 Ready 且带旧组标签：先由新 controller 清除标签并完成参数恢复、Adapter 交接，即可切换旧 controller，无需等 D Ready。若标签同步失败，则继续运行新 controller 重试，不能把清理留给旧 controller。

本流程不包含 API 版本或 CRD schema 回退；目标旧 controller 必须能够处理保留的 API 资源与配置，不承诺任意旧版本均可直接接管。

### 版本差异策略

- 旧版本 API 即将弃用，不为 Segment 设计 API 降级、旧客户端兼容或旧 coordination 反向映射，也不新增旧版访问拒绝或转换保留方案。
- `segment` 定义在独立的 `CoordinatedPolicy` 中，该资源当前只有 v1alpha2；RBG 的 API 版本转换不会转换或删除旁侧 Policy，不能将其描述为 RBG 转换时保留或丢弃 `segment`。
- controller 降级与 API 版本转换是不同操作。前者按上一节先完成 Segment 撤销及收尾，再切换旧 controller；不将旧 controller 忽略未知字段视为安全接管保证。

## 生产就绪评审

实现与发布评审需确认以下启停、观测和故障处理能力。

### 功能启用与回滚

#### 如何在运行中的集群启用或关闭功能？

- 启用：在 CoordinatedPolicy rule 中声明 `segment`。
- 关闭：删除 `segment` 字段、整条规则或 Policy，并按生命周期章节撤销已下发的协同约束。
- 不要求控制面重启。

#### 启用功能是否改变默认行为？

未声明 segment 的规则不受影响。

#### 关闭后重新启用会怎样？

按当前配置重新校验和接管，见[重新添加与规则重命名](#重新添加与规则重命名)。

#### 功能启用与关闭有哪些测试？

接管、撤销和重新添加的验收场景见[测试计划](#测试计划)，尚未执行。

### 发布、升级与回滚计划

#### controller 回滚前需要完成哪些准备？

若同时需要 controller 降级，必须先由新 controller 完成参数恢复、Adapter 交接及受管组标签清理，再切换旧 controller；不要求业务全部 Ready。具体步骤见[升级与降级策略](#升级与降级策略)。

#### 升级与回滚是否经过测试？

尚未执行。验收场景见[controller 降级准备](#controller-降级准备)和[端到端测试](#端到端测试)。

### 监控要求

#### 如何判断工作负载正在使用该功能？

实现后可用以下命令查找带组标签的 RI。标签可能处于异步清理阶段，还需核对当前有效 Policy 和 RIS 分组描述，不能仅凭标签判断策略仍生效。

```sh
kubectl get roleinstances -l rbg.workloads.x-k8s.io/segment
```

#### 如何确认实例的分组与策略执行符合预期？

RBG 及相关 Adapter 的状态信息应支持确认：

- 配置是否被接受，以及被拒绝的原因。
- 最终期望组数与当前已下发的组数目标。
- 实际完整组数、Ready 组数和满足 RIS 可用性条件的组数。
- 各 role 的未对齐明细，以及是否存在被拒绝的扩缩请求。

最小 condition 契约如下：

| 对象与 condition | 必须表达的含义 |
| --- | --- |
| RBG `SegmentConfigurationValid` | 规则、后端、稠密成员、rule ID 和更新参数是否合法；reason 指明第一个阻塞成员或字段 |
| RBG `SegmentReplicasAligned` | 各成员 role 的有效期望副本是否解析为同一个整数组数 |
| RBG `SegmentAssignmentReady` | 有效规则的 RIS 描述及已有 RI / Pod 身份是否已收敛 |
| RBG `SegmentGangConfigured` | 调度器是否支持 Segment 子组，PodGroup 翻译是否为当前状态 |
| Adapter `SegmentTargetBound` | Segment target 是否绑定、是否取得写入权，以及是否已成功提交全部成员目标 |

condition reason 必须区分配置非法、调度器能力不支持、副本未对齐、同步延迟和控制器错误。

### 依赖

#### 功能依赖哪些集群服务？

Segment 结构本身不依赖特定 scheduler。Segment Gang 依赖 Volcano `subGroupPolicy`，scheduler-plugins 不支持完整 Segment 门槛；非 Segment Gang 的调度器支持范围不变。与 topology 组合时依赖后续 topology 设计，router 组感知路由依赖具体 router 实现。

### 规模与资源开销

#### 是否增加 API 调用？

新成员的标签写入复用 RI / Pod 创建路径；存量接管与撤销会额外产生 metadata 差异 patch，不依赖滚动更新。

#### 是否增加 API 对象的大小或数量？

- Policy 增加分组与策略字段；启用适配器时，每条 Segment 规则增加一个 Adapter。Gang 继续使用单 RBG PodGroup，每条 Segment 规则增加一个 SubGroupPolicy，不为每个 Segment 或 Pod 创建独立的管理资源。
- 状态汇总按规则组织；具体状态字段与持久化开销以实现及规模测试结果为准。

#### 是否增加控制器的资源开销？

组级可用性和预算统计需要遍历成员身份与状态，开销随规则及成员规模增长，不能仅用规则数和 role 数估算。实际缓存、索引、扫描和 API 请求开销需在实现后测量。

### 故障排查

以下为状态与诊断能力的设计要求；[监控要求](#监控要求)中的 condition 名称是最小契约，实现可以增加字段但不能缺失这些含义。

#### 已知故障模式有哪些，如何处理？

| 现象 | 检查与处理 |
| --- | --- |
| 副本不满足配比 | 查看配比违规原因及各 role 剩余数量；按共同组数修正有效目标，启用 Adapter 时通过 Adapter 提交组数，不单独改成员副本 |
| Adapter 请求被拒绝 | 检查是否请求了单 role 扩缩，以及 RBG、Policy、规则、对象归属和控制权是否有效 |
| Gang 或创建批次持续等待 | 先检查是否存在配置校验错误，包括 `segBatchSize < minSegments`、`minSegments` 大于期望组数或调度器不支持；再检查 Pod requests、调度约束和实际资源。配置合法时等待资源属于正常行为，业务未 Ready 不等于资源准入失败 |
| Pod 标签与预期不一致 | 沿 Policy → RIS annotation → RI label → Pod label 检查当前期望与同步错误，确认是否被外部修改；通过 reconcile 恢复，不靠重启传播 |
| 成员模式被拒绝 | 检查是否存在 Stateless RIS、非原生后端或稀疏成员（`excluded-ordinals` / `committed-skip-ordinals`）；如需保留该成员形态，移除对应 Segment 配置，控制器不自动转换模式或填充 exclusion |
| 更新未继续 | 检查不可用与在途组占用、用户 partition、RIS 局部预算及实际可用 surge；目标改版不清空已有占用 |

## 实施历史

- 2026-11-03：基于 issue #543 创建 KEP 草案。

## 限制与代价

Segment 用更严格的成员关系换取共同操作单位。用户需要确认配比符合业务服务模型，并接受[风险与缓解](#风险与缓解)中列出的灵活性限制、等待条件和身份同步延迟。

Stateless RIS 和 deprecated workload 的支持范围见[非目标](#非目标)，成员同时 Ready 和更新无混版不属于本方案保证。

## 替代方案

以下方案未采用，因为它们不能同时提供稳定身份和跨策略的共同操作单位。

### 只使用现有 CoordinatedPolicy

现有 scaling、rolling update 和 gang 已经能协调多个 role，但没有稳定的服务组身份，也无法保证 gang、scaling、rolling update 使用同一个最小服务单元。

### 由应用自行管理分组

应用可以自行维护 prefill 与 decode 的配对关系，但分组正确性需要由应用和运维流程分别保证，RBG 无法在调度、扩缩容和更新时统一使用该结构。

### 将 Segment 作为调度策略

如果把 Segment 放在调度策略中并让它控制 rollout，会使结构划分与行为策略耦合，难以同时服务 gang、scaling、rolling update 和 topology。因此本设计将 Segment 保持在规则级结构字段。

## 参考资料

- [Issue #543：Support Segment as the cross-role service unit in CoordinatedPolicy](https://github.com/sgl-project/rbg/issues/543)
- [KEP-430：Gang Scheduling](../430-gang-scheduling/README.md)
- [KEP-30：Role Coordination](../30-role-coordination/README.md)

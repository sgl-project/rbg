# KEP-576：紧凑 Discovery ConfigMap

[English](README.md) | 简体中文

<!-- toc -->
  - [发布签核清单](#发布签核清单)
  - [摘要](#摘要)
  - [动机](#动机)
    - [背景](#背景)
      - [当前 v1alpha1 内容](#当前-v1alpha1-内容)
      - [三种 pattern 的 Pod DNS](#三种-pattern-的-pod-dns)
      - [大小问题](#大小问题)
    - [目标](#目标)
    - [非目标](#非目标)
  - [方案](#方案)
    - [术语](#术语)
    - [用户故事（可选）](#用户故事可选)
      - [故事 1](#故事-1)
      - [故事 2](#故事-2)
    - [备注、约束与注意事项（可选）](#备注约束与注意事项可选)
    - [风险与缓解](#风险与缓解)
  - [设计细节](#设计细节)
    - [Discovery Config Version Annotation](#discovery-config-version-annotation)
    - [Deprecated Workloads](#deprecated-workloads)
    - [Consumer Decoding Contract](#consumer-decoding-contract)
    - [v1 内容格式](#v1-内容格式)
      - [地址模板](#地址模板)
      - [组件编号](#组件编号)
      - [成员编码](#成员编码)
      - [写入与替换](#写入与替换)
    - [迁移](#迁移)
    - [测试计划](#测试计划)
      - [单元测试](#单元测试)
      - [集成测试](#集成测试)
      - [端到端测试](#端到端测试)
    - [毕业标准](#毕业标准)
      - [Alpha](#alpha)
      - [Beta](#beta)
      - [GA](#ga)
      - [废弃](#废弃)
    - [升级与降级策略](#升级与降级策略)
    - [版本差异策略](#版本差异策略)
  - [生产就绪评审问卷](#生产就绪评审问卷)
    - [功能启用与回滚](#功能启用与回滚)
        - [如何在运行中的集群启用或禁用该功能？](#如何在运行中的集群启用或禁用该功能)
      - [启用该功能是否改变默认行为？](#启用该功能是否改变默认行为)
      - [启用后能否禁用？](#启用后能否禁用)
      - [禁用后再次启用会发生什么？](#禁用后再次启用会发生什么)
        - [是否有启用或禁用测试？](#是否有启用或禁用测试)
    - [发布、升级与回滚计划](#发布升级与回滚计划)
      - [发布或回滚可能如何失败？是否影响运行中的 workload？](#发布或回滚可能如何失败是否影响运行中的-workload)
      - [哪些指标应触发回滚？](#哪些指标应触发回滚)
      - [是否测试了升级和回滚？](#是否测试了升级和回滚)
      - [发布是否伴随废弃或移除？](#发布是否伴随废弃或移除)
    - [监控要求](#监控要求)
      - [运维如何判断功能正在使用？](#运维如何判断功能正在使用)
      - [使用者如何判断功能正常？](#使用者如何判断功能正常)
      - [合理的 SLO 是什么？](#合理的-slo-是什么)
      - [SLI 是什么？](#sli-是什么)
      - [还缺哪些指标？](#还缺哪些指标)
    - [依赖](#依赖)
      - [该功能是否依赖集群中的特定服务？](#该功能是否依赖集群中的特定服务)
    - [规模](#规模)
      - [启用该功能是否新增 API 调用？](#启用该功能是否新增-api-调用)
      - [启用该功能是否引入新 API 类型？](#启用该功能是否引入新-api-类型)
      - [启用该功能是否新增云厂商调用？](#启用该功能是否新增云厂商调用)
      - [启用该功能是否增加现有 API 对象的大小或数量？](#启用该功能是否增加现有-api-对象的大小或数量)
      - [启用该功能是否增加现有操作耗时？](#启用该功能是否增加现有操作耗时)
      - [启用该功能是否增加资源使用？](#启用该功能是否增加资源使用)
      - [启用该功能是否可能导致资源耗尽？](#启用该功能是否可能导致资源耗尽)
    - [故障排查](#故障排查)
      - [API server 或 etcd 不可用时，功能如何表现？](#api-server-或-etcd-不可用时功能如何表现)
      - [其他已知故障模式？](#其他已知故障模式)
      - [SLO 不满足时应采取什么步骤？](#slo-不满足时应采取什么步骤)
  - [实施历史](#实施历史)
  - [缺点](#缺点)
  - [替代方案](#替代方案)
  - [所需基础设施（可选）](#所需基础设施可选)
<!-- /toc -->

## 发布签核清单

标记 `(R)` 的项目在本增强目标发布前必须完成。

- [ ] (R) 发布里程碑中的增强 issue 链接到本 KEP。
- [ ] (R) KEP 审批者已批准状态为 `implementable`。
- [ ] (R) 设计细节已记录。
- [ ] (R) 测试计划已就绪。
- [ ] (R) 毕业标准已就绪。
- [ ] (R) 生产就绪评审已完成。
- [ ] (R) 生产就绪评审已批准。
- [ ] 实施历史已更新。
- [ ] 用户文档已创建。
- [ ] 支撑文档已创建。

## 摘要

本 KEP 把当前 discovery ConfigMap 从逐实例全量枚举，改为按角色和组件描述的紧凑格式。

当前内容没有版本字段。本文将其命名为 **v1alpha1**。新内容显式写入：

```yaml
apiVersion: v1
```

RBG metadata 通过一个专用 annotation 持久化内容版本：

```yaml
metadata:
  annotations:
    rbg.workloads.x-k8s.io/discovery-config-version: v1
```

控制器继续使用同一个 RBG 级 ConfigMap `<rbg>` 和同一个 `config.yaml` key。当 annotation 为 `v1` 时，控制器写入 v1 内容并替换 v1alpha1 内容。

ConfigMap 是已提交 membership 的发布快照，不是 controller 状态的事实源。
控制器根据 RBG spec 和 child RoleInstanceSet 的 committed target 生成它，
并且可以随时重建。

本 KEP 定义存储格式、替换语义、迁移步骤，以及 v1alpha1 和 v1 共存期间
消费者需要的解码契约。单独的兼容层不在本 KEP 范围内。

在本 KEP 实现前，必须移除 legacy discovery 支持。控制器只保留一个
discovery 实现，不再需要 mode selector。

## 动机

### 背景

#### 当前 v1alpha1 内容

当前 discovery 实现写入的 ConfigMap 内容如下：

```yaml
# ConfigMap: pd-inference
# key: config.yaml
group:
  name: pd-inference
  size: 2
  roles:
    - prefill
    - decode
roles:
  decode:
    size: 4
    instances:
      - address: pd-inference-decode-0.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-1.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-2.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-3.s-pd-inference-decode
        ports:
          http: 8000
  prefill:
    size: 2
    instances:
      - address: pd-inference-prefill-0.s-pd-inference-prefill
        ports:
          http: 8000
      - address: pd-inference-prefill-1.s-pd-inference-prefill
        ports:
          http: 8000
```

其中：

- `group.roles` 保持 spec 声明顺序。
- `roles` 是 Go map，经 `sigs.k8s.io/yaml` 序列化后按 key 字典序输出。
- 每个 role 的 `size` 是序列化出来的 address 条目数；角色副本数记录在 `role.replicas`。
- role 级 `servicePorts` 被复制到每个 instance。

相关代码：

- `pkg/discovery/config_builder.go`：`ClusterConfig` / `GroupInfo` / `RolesInfo`
- `pkg/discovery/config_builder.go`：`Build()`

#### 三种 pattern 的 Pod DNS

下文使用以下占位符：

- `<workloadName>`：workload 名，来自 `rbg.GetWorkloadName(role)`。
- `<headlessServiceName>`：headless Service 名，来自 `GetCompatibleHeadlessServiceName()`。
- `<roleInstanceIndex>`：RoleInstance 序号。
- `<componentIndex>`：组件内 Pod 序号。
- `<workerIndex>`：LeaderWorkerPattern 内的 worker 序号，取值范围是 `1` 到 `size - 1`。

默认情况下：

- `<workloadName> = <rbg>-<role>`
- `<headlessServiceName> = s-<rbg>-<role>`

| Pattern | Pod DNS | 代码位置 |
|---|---|---|
| `StandalonePattern` | `<workloadName>-<roleInstanceIndex>.<headlessServiceName>` | `pkg/discovery/config_builder.go`：`buildInstances()` default 分支；`api/workloads/v1alpha2/helper.go`：`GetWorkloadName()` / `GetServiceName()` |
| `CustomComponentsPattern` | `<workloadName>-<roleInstanceIndex>-<componentName>-<componentIndex>.<headlessServiceName>` | `pkg/discovery/config_builder.go`：`buildInstances()` CustomComponents 分支；`pkg/reconciler/roleinstance/utils/instance_utils.go`：`FormatComponentPodName()` |
| `LeaderWorkerPattern` | leader：`<workloadName>-<roleInstanceIndex>-0.<headlessServiceName>`；worker：`<workloadName>-<roleInstanceIndex>-<workerIndex>.<headlessServiceName>` | `pkg/discovery/config_builder.go`：`buildInstances()` LeaderWorker 分支；`pkg/reconciler/roleinstance/utils/instance_utils.go`：`FormatComponentPodName()` |

需要注意，LeaderWorkerPattern 虽然也是多 Components 形式模版，但是其 componentName 并不出现在 DNS 中。

Service 兼容逻辑位于：

- `pkg/utils/service_utils.go`：`GetCompatibleHeadlessServiceName()`

#### 大小问题

当前 v1alpha1 内容是全量枚举：

- 每条 address 记录约 40–120 字节。
- 在本 KEP 的示例命名下，一条记录约 117.7 字节。
- 4000 个副本、每实例 5 个 Pod 时，约 2.35 MB（2.25 MiB），超过
  ConfigMap 的 1 MiB 限制。

即使未超过 1 MiB，每次成员变化也会重写整个对象，并由 kubelet 同步到所有挂载节点。

### 目标

1. 存储大小与 role 数、component 数和互不相连的排除区间数量相关，
   与副本数无关。
2. 用 `apiVersion` 标识 ConfigMap 内容版本。
3. 在一个专用 annotation 中持久化内容版本。
4. 保留同一个 `<rbg>` ConfigMap 和 `config.yaml` key。
5. v1 内容替换 v1alpha1 内容。
6. 正确表达三种 RoleInstanceSet pattern 的 Pod DNS。
7. 保留当前 `memberOrdinals` 的成员选择语义。
8. 使用 API server 的 1 MiB ConfigMap 限制执行存储对象大小约束。
9. 去掉 legacy 支持、只保留一个 discovery 实现，从而移除 discovery mode
   selector。
10. 定义新 RBG 如何默认使用 v1、存量 RBG 如何保持 v1alpha1，以及仅修改
    annotation 时如何触发 reconcile。
11. 定义回滚和 controller 降级行为。

### 非目标

1. 单独的消费者兼容层或翻译脚本。
2. 运行时 readiness / liveness 由 DNS / EndpointSlice 承担。
3. Deprecated Deployment / StatefulSet / LeaderWorkerSet workload 保持现状。
4. 动态端口分配维持现有环境变量和注解机制。
5. 将 legacy RBG 迁移到单一 discovery 实现。
6. 新增自定义存储对象 guardrail。API server 已强制 ConfigMap 的 1 MiB
   限制；控制器只在写入前拒绝序列化后的 `config.yaml` 数据，使超限回滚
   保留紧凑内容，而不是反复在 API server 写入时失败。
7. 弃用或移除 v1alpha1。本 KEP 只定义两个版本共存。

## 方案

### 术语

- **v1alpha1 内容**：当前 discovery 实现写入的逐实例全量枚举，没有
  `apiVersion` 字段。
- **v1 内容**：本 KEP 定义的紧凑格式，显式写入 `apiVersion: v1`。
- **Discovery-config-version annotation**：
  `rbg.workloads.x-k8s.io/discovery-config-version` annotation，取值为
  `v1alpha1` 和 `v1`。
- **已提交 membership**：child RoleInstanceSet 上已持久化的成员集合。
  discovery 投影该状态，不直接投影用户侧 exclusion annotation。
- **成员**：被 discovery 选中的 RoleInstance 序号。
- **组件**：RoleInstanceSet 实例内的一个命名 Pod 组。
- `<rbg>`：RBG 对象名。

### 用户故事（可选）

#### 故事 1

作为集群管理员，我希望角色有数千个实例时，discovery ConfigMap 仍然低于 1 MiB 限制。

#### 故事 2

作为引擎开发者，我希望 ConfigMap schema 稳定且带版本标记，以便升级消费者时不需要再次修改控制器存储格式。

### 备注、约束与注意事项（可选）

- 每个 RBG 只维护一个 discovery ConfigMap。
- ConfigMap 名称是 `<rbg>`，数据 key 是 `config.yaml`。
- v1 内容在同一个对象中替换 v1alpha1 内容。
- 本 KEP 不定义 renderer init container。
- v1 只序列化 stateful role，与当前实现一致。
- v1 地址保持当前 builder 输出。在 `sharedServiceSelection: LeaderOnly`
  下，worker 地址可能无法通过集群 DNS 解析，这一点与 v1alpha1 相同。
- v1alpha1 和 v1 共存。弃用属于单独 KEP。

### 风险与缓解

| 风险 | 缓解 |
|---|---|
| v1 内容编码的地址集合与当前 builder 输出不同 | 单元测试解码 v1 内容，并与当前 builder 输出做结构化比较 |
| workload / Service 名称触发 Kubernetes 名称截断 | 控制器计算完整地址模板，包含截断和兼容规则 |
| controller 升级后意外改变内容版本 | discovery-config-version annotation 记录选择的版本，controller 重建 ConfigMap 时使用该 annotation |
| 消费者只支持 v1alpha1 | 在消费者实现解码契约之前，将 annotation 保持为 `v1alpha1` |
| 回滚到 v1alpha1 会超过 1 MiB | controller 在替换 v1 内容前拒绝回滚 |
| RBGSet 覆盖 child annotation | 版本 annotation 由 `spec.groupTemplate.annotations` 管理，并由 child 继承 |
| `LeaderOnly` 下 worker 地址不可解析 | v1 保持当前 builder 输出；DNS 可解析性仍由 `sharedServiceSelection` 决定 |
| 仅修改 annotation 不会触发 reconcile | `RBGPredicate` 监听 discovery-config-version annotation |

## 设计细节

### Discovery Config Version Annotation

ConfigMap 内容需要显式版本，消费者才能区分 v1alpha1、v1 和后续版本。

RBG metadata 记录版本：

```yaml
metadata:
  annotations:
    rbg.workloads.x-k8s.io/discovery-config-version: v1
```

规则：

1. annotation key 是
   `rbg.workloads.x-k8s.io/discovery-config-version`。
2. 支持的取值是 `v1alpha1` 和 `v1`。
3. mutating webhook 只在 `CREATE` 时把 annotation 默认为 `v1`。
   它不会在 `UPDATE` 时为缺失的 annotation 补默认值；没有 annotation 的
   存量 RBG 保持 `v1alpha1`，重建对象但省略该键的 `UPDATE` 会保留已
   存储版本。显式空值在 `CREATE` 时按缺失处理。
   含 deprecated stateful role、无法序列化紧凑内容的对象默认为
   `v1alpha1`；对这些 role，`v1` 仍会被拒绝。
4. 创建时显式设置 `v1alpha1` 是文档化的 opt-out。
5. 对 RBGSet，webhook 在 `spec.groupTemplate.annotations` 中写默认值。
   child RBG 继承该值；早于该 annotation 的 set 保持历史默认
   `v1alpha1`。直接修改 child annotation 会被覆盖，省略该键的模板
   `UPDATE` 会保留已存储版本；如需回滚，请显式设置 `v1alpha1`。
6. Admission 拒绝 `v1alpha1` 和 `v1` 以外的取值。
7. Admission 对包含 deprecated stateful role 的 RBG 拒绝 `v1`。
8. `RBGPredicate` 监听该 annotation，因此只修改 annotation 也会让 RBG
   进入 reconcile 队列。
9. 控制器根据该 annotation 选择 ConfigMap 内容版本。
10. ConfigMap 被删除后，控制器在下一次 reconcile 时按 annotation 版本
    重建内容。
11. v1alpha1 内容没有 `apiVersion`。
12. v1 内容写入 `apiVersion: v1`。

在实现 v1 之前，legacy discovery 支持会被移除。控制器不再读写
`discovery-config-mode`，并为每个有 stateful role 的 RBG 维护 `<rbg>`。
因此，存量 `legacy` RBG 会在下一次 reconcile 时开始得到 `<rbg>`；它们的
Pod template 不变，因为 v0.7 及之后的版本已经挂载 `<rbg>`。

### Deprecated Workloads

紧凑格式只支持 RoleInstanceSet。Admission 对包含 deprecated stateful
role 的 RBG 拒绝 `v1`。包含这些 role 的存量对象保持 `v1alpha1`，包括
admission 被绕过的情况。

### Consumer Decoding Contract

读取 `/etc/rbg/config.yaml` 的消费者必须同时支持两个共存版本：

1. 缺失 `apiVersion` 表示 v1alpha1。
2. `apiVersion: v1` 表示 v1。
3. 其他 `apiVersion` 取值一律拒绝。

挂载的文件是 whole-volume ConfigMap。修改存量 RBG 的 annotation 后，
kubelet 会在同步周期内更新 running Pod 里的挂载文件。运行中会重复读取
该文件的消费者，必须在 operator 迁移 RBG 前同时支持两个版本。

best-practice 文档包含 version-aware 解码示例。测试使用的 reference
decoder 按下文的 range-jumping 规则解码 v1。

| 内容版本 | 标记 | 含义 |
|---|---|---|
| v1alpha1 | 缺失 `apiVersion` | 当前全量枚举 |
| v1 | `apiVersion: v1` | 本 KEP 定义的紧凑格式 |

### v1 内容格式

v1 只序列化 stateful role，与当前 controller 行为一致。

示例：

```yaml
apiVersion: v1
group: pd-inference

roles:
  - name: prefill
    members: 500
    excludedOrdinals: "3,17"
    ports:
      http: 8080
      grpc: 50000
    components:
      - name: leader
        size: 1
        base: "pd-inference-prefill-{roleIndex}-{componentIndex}.s-pd-inference-prefill"
      - name: worker
        size: 4
        offset: 1
        base: "pd-inference-prefill-{roleIndex}-{componentIndex}.s-pd-inference-prefill"
```

字段说明：

| 字段 | 类型 | 说明 |
|---|---|---|
| `apiVersion` | string | 必须为 `v1`；缺失时按 v1alpha1 解析 |
| `group` | string | RBG 名 |
| `roles` | array | 按 spec 声明顺序排列 |
| `roles[].name` | string | 角色名 |
| `roles[].members` | int | 被选中的 RoleInstance 数量 |
| `roles[].excludedOrdinals` | string | 最高选中成员以下的已提交空洞；不是用户 annotation 原值 |
| `roles[].ports` | map | role 级 `servicePorts`，key 按下方规则生成 |
| `roles[].components` | array | 按 spec 声明顺序排列 |
| `roles[].components[].name` | string | 组件名 |
| `roles[].components[].size` | int | 单个实例中该组件的 Pod 数 |
| `roles[].components[].offset` | int | 可选，默认 `0`；组件内第一个 `componentIndex` |
| `roles[].components[].base` | string | 相对地址模板，包含 `{roleIndex}` / `{componentIndex}` |

#### 地址模板

`base` 是完整的相对地址：

```text
<pod>.<service>
```

占位符：

- `{roleIndex}`：RoleInstance 序号。
- `{componentIndex}`：组件内最终 Pod 序号，等于 `offset + 组件内从 0 开始的序号`。

以上是全部占位符。

各 pattern 必需的占位符：

| Pattern | 必需占位符 |
|---|---|
| `StandalonePattern` | 仅 `{roleIndex}` |
| `CustomComponentsPattern` | `{roleIndex}` 和 `{componentIndex}` |
| `LeaderWorkerPattern` | `{roleIndex}` 和 `{componentIndex}` |

未知占位符，以及 pattern 缺少必需占位符，都是 controller bug，并由单元
测试拒绝。

替换规则：

1. 执行字符串替换。
2. 用 `offset` 表达编号偏移。

Port key 使用当前 builder 规则：

1. ServicePort name 转小写
2. `-` 替换为 `_`
3. ServicePort 没有 name 时使用 `port<N>`

#### 组件编号

LeaderWorkerPattern 的实际地址是：

```text
leader:  <workloadName>-<roleInstanceIndex>-0.<headlessServiceName>
worker:  <workloadName>-<roleInstanceIndex>-<workerIndex>.<headlessServiceName>
```

可选表达方式有两种：

1. 模板表达式：`{componentIndex + 1}`。
2. 组件字段 `offset`，worker 的值为 `1`。

本 KEP 选择 `offset`：

```yaml
components:
  - name: leader
    size: 1
    offset: 0
    base: "pd-inference-prefill-{roleIndex}-{componentIndex}.s-pd-inference-prefill"
  - name: worker
    size: 4
    offset: 1
    base: "pd-inference-prefill-{roleIndex}-{componentIndex}.s-pd-inference-prefill"
```

因此：

- leader 的 `componentIndex` 是 `0`。
- worker 的 `componentIndex` 范围是 `offset` 到 `offset + size - 1`。
- 模板保持纯字符串替换。
- 行为与 `FormatComponentPodName()` 一致。

StandalonePattern：

```yaml
components:
  - name: prefill
    size: 1
    offset: 0
    base: "pd-inference-prefill-{roleIndex}.s-pd-inference-prefill"
```

CustomComponentsPattern：

```yaml
components:
  - name: leader
    size: 1
    offset: 0
    base: "pd-inference-prefill-{roleIndex}-leader-{componentIndex}.s-pd-inference-prefill"
```

如果组件覆盖 Service，`base` 使用覆盖后的 Service 名。

`base` 记录当前 builder 生成的地址。DNS 可解析性仍由
`sharedServiceSelection` 决定；在 `LeaderOnly` 下，worker 地址存在但不是
网络身份。

#### 成员编码

示例：

```yaml
members: 3
excludedOrdinals: "1,3"
```

表示：

```text
[0, 2, 4]
```

成员选择沿用当前 `memberOrdinals()`：

1. 创建 child RoleInstanceSet 之前：
   - 未配置排除输入：使用 `[0, role.replicas)`。
   - 有合法排除输入：把排除投影到稠密区间。
   - 输入非法时，成员列表为空。
2. child 存在时：
   - 排除输入存在或非法，或 child 带有 committed skip record：使用 child 的 committed target。
   - 否则使用 `[0, role.replicas)`。

编码规则：

- `members` 是被选中的序号数量。
- `excludedOrdinals` 是最高选中序号以下空洞的归一化区间记录。

`excludedOrdinals` 记录的是已提交 membership 中的空洞，不是用户侧
`excluded-ordinals` annotation 的复制值。即使用户删除该 annotation，
已提交空洞也会保留，直到 controller 提交新的 membership target。

区间语法：

- 条目之间用逗号分隔。
- 每个条目是 `n` 或 `a-b`。
- 区间为闭区间。
- 值是非负十进制整数。
- 最大值是 `2147483647`。
- 条目按升序排列。
- 重叠和相邻区间会合并。
- 规范形式中没有空格。

选中的成员是升序排列、且没有被 `excludedOrdinals` 任一区间覆盖的前
`members` 个序号。解码器通过区间跳跃找到它们，不会把区间展开成单个
序号。

解码规则：

1. `5-3` 这类反向区间会被拒绝。
2. 未排序的条目会被接受，并通过排序和合并归一化。
3. 小于 `0` 或大于 `2147483647` 的值会被拒绝。
4. `members: 0` 生成空成员列表。

#### 写入与替换

控制器在 role reconcile 之前写入同一个 `<rbg>` ConfigMap：

1. 计算成员和地址模板。
2. 按 discovery-config-version annotation 生成内容。
3. 写入 `<rbg>/config.yaml`。
4. 目标版本为 `v1` 时，替换 v1alpha1 内容。

写入失败时，reconcile 返回错误并重试，保持当前 discovery 行为。

存储对象大小由 API server 的 1 MiB ConfigMap 限制约束。写入被拒绝时，
沿用现有 reconcile 错误和重试路径。

### 迁移

1. 移除 legacy discovery 支持，并部署支持 v1 的 controller。
2. 没有 annotation 的存量 RBG 保持 v1alpha1 内容。存量 `legacy` RBG 会在
   下一次 reconcile 时开始得到 `<rbg>`。
3. 新 RBG 通过 mutating webhook 默认使用 `v1`。新 RBGSet 在
   `spec.groupTemplate.annotations` 中携带该 annotation。
4. 要迁移某个存量 RBG，设置：

   ```yaml
   metadata:
     annotations:
       rbg.workloads.x-k8s.io/discovery-config-version: v1
   ```

5. 等待 reconcile 完成，确认 ConfigMap 内容为 `apiVersion: v1`。

回退步骤：

1. 将 annotation 改回 `v1alpha1`。
2. controller 序列化 v1alpha1 内容。如果超过 1 MiB，拒绝回滚并保留 v1
   内容。controller 会报告配置类 blocker；只有当每个 stateful role 的
   目标 ordinal 都在保留内容之内、且其余内容不变时，才继续 role
   reconcile，因此缩减成员规模可以让后续重试重新满足大小限制。与回滚
   一起做的其他变更需要先撤回，或继续使用 `v1`。
3. 否则，等待 reconcile 完成，确认 ConfigMap 恢复为 v1alpha1。

### 测试计划

#### 单元测试

- v1 字段完整性。
- annotation 缺失、`v1alpha1` 和 `v1` 的行为。
- 新 RBG 的默认值行为，包括 `v1alpha1` opt-out。
- webhook 只在 CREATE 时默认；UPDATE 不会为缺失 annotation 补 `v1`
  默认值，重建对象的 UPDATE 会保留已存储版本。
- RBGSet 通过 `spec.groupTemplate.annotations` 传播 annotation。
- `RBGPredicate` 对仅修改 annotation 的变化入队。
- Admission 拒绝非法 annotation 取值，并对 deprecated stateful role 拒绝
  `v1`。
- `apiVersion` 缺失时按 v1alpha1 解析。
- 未知 `apiVersion` 被拒绝。
- 三种 pattern 的地址模板。
- LeaderWorkerPattern 的 leader / worker offset。
- CustomComponentsPattern 的 Service 覆盖。
- 各 pattern 的必需占位符集合，以及未知占位符拒绝。
- Port key 生成规则：小写、`-` 转 `_`、未命名端口使用 `port<N>`。
- 成员选择和 `excludedOrdinals` 编码。
- 使用 range jumping 解码长区间，包括
  `members: 1, excludedOrdinals: "0-2147483645"`。
- 混合 stateful / stateless role 时只序列化 stateful role。
#### 集成测试

- 同一个 `<rbg>` ConfigMap 被替换为 v1 内容。
- ConfigMap 被删除后，按 discovery-config-version annotation 重建对应内容。
- ConfigMap 数量保持为 1。
- 写入失败时 reconcile 失败并重试。
- ConfigMap 写入因 API server 的 1 MiB 限制被拒绝时，reconcile 失败并
  重试。
- 迁移后 ConfigMap 内容为 v1。
- 没有 annotation 的存量 RBG 保持 v1alpha1。
- mode selector 移除后，存量 `legacy` RBG 得到 `<rbg>`。
- RBGSet child 从 GroupTemplate 继承版本 annotation。
- 仅修改 annotation 会触发 reconcile。
- v1alpha1 超过 1 MiB 时回滚被拒绝。

#### 端到端测试

- 迁移后 `<rbg>/config.yaml` 内容为 `apiVersion: v1`。
- 三种 pattern 的地址与当前 builder 输出一致。
- LeaderWorkerPattern worker 地址从 `1` 开始。
- 英文和中文 best-practice 指南中的 v1alpha1 与 v1 消费者示例都能解码
  挂载文件。
- controller 降级行为符合下方矩阵。

### 毕业标准

#### Alpha

- 控制器已写入 v1 内容。
- 上述单元、集成和端到端测试完成。
- 2000 实例规模测试显示存储 ConfigMap 低于 API server 的 1 MiB 限制，
  且 reconcile 成功。

#### Beta

- v1 直接消费文档完成。
- reference decoder 完成。
- 没有未解决的“v1 内容与 builder 输出不一致”报告。

#### GA

- 两个发布周期行为稳定。
- v1alpha1 和 v1 行为保持稳定。

#### 废弃

- 不属于本 KEP。本 KEP 只定义 v1alpha1 和 v1 共存。

### 升级与降级策略

见 [迁移](#迁移)。

升级只改变 discovery ConfigMap 内容，Pod template、volume 和 mount 保持不变。

降级到 v1 之前的 controller 后，该 controller 忽略
discovery-config-version annotation，并继续写入 v1alpha1 内容。降级迁移
属于后续 KEP。

降级矩阵：

| 起始状态 | 旧 controller 行为 | 结果 |
|---|---|---|
| 已有 `discovery-config-mode: refine` 的 RBG | 忽略版本 annotation 并写入 v1alpha1 | 枚举小于 1 MiB 时回滚成功 |
| v1alpha1 枚举超过 1 MiB 的 RBG | v1alpha1 写入被拒绝 | reconcile 在 role reconcile 前停止，直到升级 controller |
| mode selector 移除后创建的新 RBG | 没有 `discovery-config-mode`，且因已有 status 被标记为 `legacy` | `<rbg>` 停止更新，内容冻结 |
| 任意再次升级的 RBG | 版本 annotation 仍然存在 | 新 controller 恢复所选版本 |

对 selector 移除后创建的 RBG，不支持原地降级；恢复兼容 controller 后需要
重建这些 RBG。

### 版本差异策略

ConfigMap 中的 `apiVersion` 和 RBG metadata 中的
discovery-config-version annotation 让消费者和控制器识别选择的内容。

旧 controller 忽略该 annotation，并继续写入 v1alpha1 内容。

## 生产就绪评审问卷

### 功能启用与回滚

#### 如何在运行中的集群启用或禁用该功能？

将 discovery-config-version annotation 设置为 `v1`。回滚时改回
`v1alpha1`。

#### 启用该功能是否改变默认行为？

Pod template、volume 和 mount 保持不变。挂载的 ConfigMap 内容会在
kubelet 同步周期内更新到 running Pod，因此会重复读取
`/etc/rbg/config.yaml` 的消费者必须在迁移前同时支持两个版本。

#### 启用后能否禁用？

将 discovery-config-version annotation 改回 `v1alpha1`。只有 v1alpha1
序列化内容不超过 1 MiB 时，回滚才会被接受。

#### 禁用后再次启用会发生什么？

控制器根据 RBG 的 discovery-config-version annotation 和已提交
membership，确定性地重建 v1 内容。

#### 是否有启用或禁用测试？

有。集成测试和端到端测试覆盖迁移和回滚。

### 发布、升级与回滚计划

#### 发布或回滚可能如何失败？是否影响运行中的 workload？

ConfigMap 写入可能失败。reconcile 返回错误并重试。Pod template 不变，
但 live-mounted 文件会在 kubelet 同步周期内变化；只支持 v1alpha1 且会
重复读取该文件的消费者可能失败。

#### 哪些指标应触发回滚？

- RBG controller reconcile 错误率。
- ConfigMap 写入失败次数。

#### 是否测试了升级和回滚？

是。测试计划覆盖迁移和回滚。

#### 发布是否伴随废弃或移除？

是。legacy discovery 支持和 `discovery-config-mode` selector 会被移除。
存量 `legacy` RBG 会在下一次 reconcile 时开始得到 `<rbg>`。

### 监控要求

#### 运维如何判断功能正在使用？

检查 discovery-config-version annotation 和 ConfigMap 的 `apiVersion`。

#### 使用者如何判断功能正常？

ConfigMap 内容与所选版本以及 RBG 当前的 roles、components 和 membership 一致。

#### 合理的 SLO 是什么？

- ConfigMap reconcile 成功率。
- ConfigMap 写入延迟。

#### SLI 是什么？

- RBG controller reconcile 错误率。
- Warning Event。

#### 还缺哪些指标？

Alpha 阶段未识别。

### 依赖

#### 该功能是否依赖集群中的特定服务？

否。

### 规模

#### 启用该功能是否新增 API 调用？

现有 ConfigMap reconcile，以及一次 ConfigMap list/watch。控制器会为每个
discovery ConfigMap 打上 group-name label，并把 cache 限制在该 label 上，
因此不会缓存无关 ConfigMap。稳态 reconcile 不发起 ConfigMap 请求；内容变化时
执行一次非缓存 ownership 读取，随后执行 server-side apply。

#### 启用该功能是否引入新 API 类型？

否。

#### 启用该功能是否新增云厂商调用？

否。

#### 启用该功能是否增加现有 API 对象的大小或数量？

否。每个 RBG 仍维护一个 discovery ConfigMap。

#### 启用该功能是否增加现有操作耗时？

否。生成紧凑内容比枚举所有 Pod 更快。

#### 启用该功能是否增加资源使用？

Pod 资源不变。

#### 启用该功能是否可能导致资源耗尽？

不会。存储对象受 API server 的 1 MiB ConfigMap 限制约束。

### 故障排查

#### API server 或 etcd 不可用时，功能如何表现？

reconcile 失败并按现有 backoff 重试。运行中的 Pod 不受影响。

#### 其他已知故障模式？

- ConfigMap 写入因超过 1 MiB 限制被 API server 拒绝：
  - 检测：reconcile 错误。
  - 缓解：缩减拓扑，或在 v1alpha1 仍可写入时保持该版本。
- 内容版本不匹配：
  - 检测：比较 discovery-config-version annotation 和 ConfigMap
    `apiVersion`。
  - 缓解：设置期望版本并等待 reconcile。

#### SLO 不满足时应采取什么步骤？

检查 RBG Warning Event、controller 日志和 ConfigMap。

## 实施历史

- 2026-11-13：提出初始 KEP。
- 2026-11-17：改为紧凑内容设计，新增版本 annotation，修正
  LeaderWorkerPattern 地址和 worker 偏移。
- 2026-11-19：补充 CREATE-only annotation 默认值、RBGSet 传播、
  deprecated workload 拒绝、消费者解码规则、回滚和降级矩阵，以及只包含
  stateful role 的范围。

## 缺点

- 消费者可见的内容格式发生变化。
- v1 和 v1alpha1 消费者需要迁移路径。

## 替代方案

1. **保留 v1alpha1 全量枚举**
   - 无法满足 1 MiB 限制。
2. **增加并行 compact ConfigMap**
   - 引入两个数据来源和一致性成本。
3. **发布命名规则，由客户端推导**
   - workload / Service 名称存在截断和兼容规则。
4. **压缩 v1alpha1**
   - 可以降低对象大小，但消费者需要理解压缩内容。
5. **运行时 DNS / EndpointSlice**
   - 提供 readiness 信息，缺少静态序号拓扑。

## 所需基础设施（可选）

无。

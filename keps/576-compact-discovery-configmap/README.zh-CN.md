# KEP-576：紧凑 Discovery ConfigMap

[English](README.md) | 简体中文

<!-- toc -->
- [KEP-576：紧凑 Discovery ConfigMap](#kep-576紧凑-discovery-configmap)
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
      - [如何在运行中的集群启用 / 禁用该功能？](#如何在运行中的集群启用--禁用该功能)
      - [启用该功能是否改变默认行为？](#启用该功能是否改变默认行为)
      - [启用后能否禁用？](#启用后能否禁用)
      - [禁用后再次启用会发生什么？](#禁用后再次启用会发生什么)
      - [是否有启用 / 禁用测试？](#是否有启用--禁用测试)
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

本 KEP 定义存储格式、替换语义和迁移步骤。消费者兼容层属于后续 KEP。

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
- 在本 KEP 的示例命名下，一条记录约 124.7 字节。
- 4000 个副本、每实例 5 个 Pod 时，约 2.38 MiB，超过 ConfigMap 1 MiB 限制。

即使未超过 1 MiB，每次成员变化也会重写整个对象，并由 kubelet 同步到所有挂载节点。

### 目标

1. 存储大小与 role / component 数量相关，与副本数无关。
2. 用 `apiVersion` 标识 ConfigMap 内容版本。
3. 在一个专用 annotation 中持久化内容版本。
4. 保留同一个 `<rbg>` ConfigMap 和 `config.yaml` key。
5. v1 内容替换 v1alpha1 内容。
6. 正确表达三种 RoleInstanceSet pattern 的 Pod DNS。
7. 保留当前 `memberOrdinals` 的成员选择语义。
8. 使用 API server 的 1 MiB ConfigMap 限制执行存储对象大小约束。
9. 去掉 legacy 支持、只保留一个 discovery 实现，从而移除 discovery mode
   selector。

### 非目标

1. 消费者兼容层、翻译脚本和迁移窗口属于后续 KEP。
2. 运行时 readiness / liveness 由 DNS / EndpointSlice 承担。
3. Deprecated Deployment / StatefulSet / LeaderWorkerSet workload 保持现状。
4. 动态端口分配维持现有环境变量和注解机制。
5. 将 legacy RBG 迁移到单一 discovery 实现。
6. 新增自定义存储对象 guardrail。API server 已强制 ConfigMap 的 1 MiB
   限制。

## 方案

### 术语

- **v1alpha1 内容**：当前 discovery 实现写入的逐实例全量枚举，没有
  `apiVersion` 字段。
- **v1 内容**：本 KEP 定义的紧凑格式，显式写入 `apiVersion: v1`。
- **Discovery-config-version annotation**：
  `rbg.workloads.x-k8s.io/discovery-config-version` annotation，取值为
  `v1alpha1` 和 `v1`。
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
### 风险与缓解

| 风险 | 缓解 |
|---|---|
| v1 内容编码的地址集合与当前 builder 输出不同 | 单元测试展开 v1 内容，并与当前 builder 输出做结构化比较 |
| workload / Service 名称触发 Kubernetes 名称截断 | 控制器计算完整地址模板，包含截断和兼容规则 |
| controller 升级后意外改变内容版本 | discovery-config-version annotation 记录选择的版本，controller 重建 ConfigMap 时使用该 annotation |
| 消费者只支持 v1alpha1 | 在消费者支持 v1 之前，将 annotation 保持为 `v1alpha1` |

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
3. legacy discovery 支持被移除，只保留一个 discovery 实现。
4. 没有 annotation 的存量 RBG 按 `v1alpha1` 处理。
5. RBG 创建后的第一次 reconcile 中，控制器写入 `v1`。创建时显式设置
   `v1alpha1` 是文档化的 opt-out。
6. 控制器根据该 annotation 选择 ConfigMap 内容版本。
7. ConfigMap 被删除后，控制器在下一次 reconcile 时按 annotation 版本
   重建内容。
8. v1alpha1 内容没有 `apiVersion`。
9. v1 内容写入 `apiVersion: v1`。

| 内容版本 | 标记 | 含义 |
|---|---|---|
| v1alpha1 | 缺失 `apiVersion` | 当前全量枚举 |
| v1 | `apiVersion: v1` | 本 KEP 定义的紧凑格式 |

### v1 内容格式

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
| `roles[].excludedOrdinals` | string | 归一化的连续跳过区间 |
| `roles[].ports` | map | role 级 `servicePorts` |
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

替换规则：

1. 执行字符串替换。
2. 用 `offset` 表达编号偏移。

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

区间语法：

- 条目之间用逗号分隔。
- 每个条目是 `n` 或 `a-b`。
- 区间为闭区间。
- 值是非负十进制整数。
- 最大值是 `2147483647`。
- 条目按升序排列。
- 重叠和相邻区间会合并。
- 规范形式中没有空格。

解码时，将条目展开成序号集合，并拒绝格式错误、降序或超出范围的值。

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

1. 部署支持 v1 的 controller。
2. 没有 annotation 的存量 RBG 保持 v1alpha1 内容。
3. 要迁移某个 RBG，设置：

   ```yaml
   metadata:
     annotations:
       rbg.workloads.x-k8s.io/discovery-config-version: v1
   ```

4. 等待 reconcile 完成，确认 ConfigMap 内容为 `apiVersion: v1`。

回退步骤：

1. 将 annotation 改回 `v1alpha1`。
2. 等待 reconcile 完成，确认 ConfigMap 恢复为 v1alpha1。

### 测试计划

#### 单元测试

- v1 字段完整性。
- annotation 缺失、`v1alpha1` 和 `v1` 的行为。
- 新 RBG 的默认值行为，包括 `v1alpha1` opt-out。
- `apiVersion` 缺失时按 v1alpha1 解析。
- 三种 pattern 的地址模板。
- LeaderWorkerPattern 的 leader / worker offset。
- CustomComponentsPattern 的 Service 覆盖。
- 成员选择和 `excludedOrdinals` 编码。
#### 集成测试

- 同一个 `<rbg>` ConfigMap 被替换为 v1 内容。
- ConfigMap 被删除后，按 discovery-config-version annotation 重建对应内容。
- ConfigMap 数量保持为 1。
- 写入失败时 reconcile 失败并重试。
- ConfigMap 写入因 API server 的 1 MiB 限制被拒绝时，reconcile 失败并
  重试。
- 迁移后 ConfigMap 内容为 v1。

#### 端到端测试

- 迁移后 `<rbg>/config.yaml` 内容为 `apiVersion: v1`。
- 三种 pattern 的地址与实际 Pod DNS 一致。
- LeaderWorkerPattern worker 地址从 `1` 开始。

### 毕业标准

#### Alpha

- 控制器已写入 v1 内容。
- 上述单元、集成和端到端测试完成。
- 2000 实例规模测试显示存储 ConfigMap 低于 API server 的 1 MiB 限制，
  且 reconcile 成功。

#### Beta

- v1 直接消费文档完成。
- 没有未解决的“v1 内容与 builder 输出不一致”报告。

#### GA

- 两个发布周期行为稳定。
- v1alpha1 和 v1 行为保持稳定。

#### 废弃

- 无。

### 升级与降级策略

见 [迁移](#迁移)。

升级只改变 discovery ConfigMap 内容，Pod template、volume 和 mount 保持不变。

降级到 v1 之前的 controller 后，该 controller 忽略
discovery-config-version annotation，并继续写入 v1alpha1 内容。降级迁移
属于后续 KEP。

### 版本差异策略

ConfigMap 中的 `apiVersion` 和 RBG metadata 中的
discovery-config-version annotation 让消费者和控制器识别选择的内容。

旧 controller 忽略该 annotation，并继续写入 v1alpha1 内容。

## 生产就绪评审问卷

### 功能启用与回滚

#### 如何在运行中的集群启用 / 禁用该功能？

将 discovery-config-version annotation 设置为 `v1`。回滚时改回
`v1alpha1`。

#### 启用该功能是否改变默认行为？

只有 ConfigMap 内容改变。Pod template、volume 和 mount 保持不变。

#### 启用后能否禁用？

将 discovery-config-version annotation 改回 `v1alpha1`，等待 reconcile
完成。

#### 禁用后再次启用会发生什么？

控制器根据 RBG 的 discovery-config-version annotation 和已提交
membership，确定性地重建 v1 内容。

#### 是否有启用 / 禁用测试？

有。集成测试和端到端测试覆盖迁移和回滚。

### 发布、升级与回滚计划

#### 发布或回滚可能如何失败？是否影响运行中的 workload？

ConfigMap 写入可能失败。reconcile 返回错误并重试。运行中的 workload 不受影响。

#### 哪些指标应触发回滚？

- RBG controller reconcile 错误率。
- ConfigMap 写入失败次数。

#### 是否测试了升级和回滚？

是。测试计划覆盖迁移和回滚。

#### 发布是否伴随废弃或移除？

否。

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

只有现有 ConfigMap reconcile。

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

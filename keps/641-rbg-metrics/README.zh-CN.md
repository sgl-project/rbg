# KEP-641：RBG Workload 与 Controller 指标

[English](README.md) | 简体中文

<!-- toc -->
- [发布签核清单](#发布签核清单)
- [摘要](#摘要)
- [动机](#动机)
  - [目标](#目标)
  - [非目标](#非目标)
- [方案](#方案)
  - [用户故事](#用户故事)
  - [备注、约束与注意事项](#备注约束与注意事项)
  - [风险与缓解](#风险与缓解)
- [设计细节](#设计细节)
  - [指标清单](#指标清单)
  - [术语](#术语)
  - [指标命名与标签](#指标命名与标签)
  - [构建信息指标](#构建信息指标)
  - [Workload 容量指标](#workload-容量指标)
  - [Group 进度指标](#group-进度指标)
  - [Rollout 耗时指标](#rollout-耗时指标)
  - [Pod Cache 指标（KEP-284）](#pod-cache-指标kep-284)
  - [Controller 阶段指标](#controller-阶段指标)
  - [特性指标](#特性指标)
  - [可选指标](#可选指标)
  - [采集架构](#采集架构)
  - [安全抓取与部署](#安全抓取与部署)
  - [测试计划](#测试计划)
    - [前置测试更新](#前置测试更新)
    - [单元测试](#单元测试)
    - [集成测试](#集成测试)
    - [端到端测试](#端到端测试)
  - [毕业标准](#毕业标准)
  - [升级与降级策略](#升级与降级策略)
  - [版本差异策略](#版本差异策略)
- [生产就绪评审问卷](#生产就绪评审问卷)
  - [功能启用与回滚](#功能启用与回滚)
  - [发布、升级与回滚计划](#发布升级与回滚计划)
  - [监控要求](#监控要求)
  - [依赖](#依赖)
  - [规模](#规模)
  - [故障排查](#故障排查)
- [实施历史](#实施历史)
- [缺点](#缺点)
- [替代方案](#替代方案)
- [所需基础设施](#所需基础设施)
<!-- /toc -->

## 发布签核清单

以下清单适用于 RBG 项目的发布。

- [ ] 已定义目标发布版本、启用机制、升级与回滚策略。
- [ ] 已确认测试计划与毕业标准。
- [ ] 已完成并批准生产就绪评审。
- [ ] 实现、生成产物和必要的兼容性回归测试通过项目 CI。
- [ ] 已完成用户文档、迁移指引和发布说明。

## 摘要

本 KEP 定义 RoleBasedGroup（RBG）controller 暴露的 Prometheus 指标。
RBG 目前暴露 controller-runtime 和 Go runtime 指标，但没有自己的 workload 指标。
运维人员无法通过 controller 的 metrics endpoint 对 role 容量损失、rollout 阻塞或 Pod cache 正确性进行告警。

本 KEP 在现有 `/metrics` endpoint 上增加一组少量 workload 与 controller 指标。
首批实现覆盖五个指标：构建信息、role 副本数、group 进度、rollout 耗时、Pod cache stub 读取次数。
后续阶段增加 controller 阶段耗时、Warmup、RoleInstance phase、扩缩容和 gang scheduling 指标。
设计复用 controller-runtime metrics registry，从 informer cache 读取 workload 状态，并保持 label 有界。

## 动机

RBG controller 已经提供 `/metrics`，但该 endpoint 只包含内置的 controller-runtime、workqueue、leader election、client-go、Go 和 process 指标。
这些指标描述 controller 自身健康，不描述 workload 状态。

运维人员需要三类时间序列：

1. 哪个 role 丢失了 ready 容量，何时恢复？
2. rollout 是在推进、被阻塞、暂停，还是已经收敛？
3. Pod cache filter 是否通过某个本应返回完整 Pod 的读取路径返回了 stub？

`kubectl get rbg` 和 RBG status 能回答单个对象的当前状态。
它们不提供历史趋势、跨对象聚合或 Prometheus 告警。
[Issue #641](https://github.com/sgl-project/rbg/issues/641) 提出了用于填补该缺口的 workload 指标。
[KEP-284](../284-pod-cache-filter/README.md) 定义了尚未实现的 Pod cache 监控指标。

本 KEP 不导出所有 status 字段。
它选择支持告警、SLO 和故障诊断的指标，将详细对象检查留给 Kubernetes status 和 events。

### 目标

1. 定义 workload 容量、group 进度、rollout 耗时和 Pod cache 正确性指标的集合、类型、label 与语义。
2. 定义有界 label 值，并复用 [KEP-502](../502-workload-status-model/README.md) 的 reason taxonomy。
3. 定义状态 gauge 从 informer cache 采集，counter 和 histogram 通过代码路径埋点。
4. 定义安全抓取、Helm 部署和 leader election 行为。
5. 定义单元、集成、端到端测试以及毕业标准。

### 非目标

1. 采集 SGLang、vLLM 或其他 workload 进程的推理引擎指标。这些指标仍由 workload 自身暴露，并从 workload Pod 抓取。
2. 替代 kube-state-metrics，或导出所有 CRD status 字段。
3. 修改 KEP-502 定义的 status 模型，或 KEP-284 定义的 cache 行为。
4. 导出 per-Pod、per-node、per-revision-hash 或 per-generation 指标。
5. 为指标单独新增 API 类型或 feature gate。

## 方案

通过现有 controller-runtime metrics registry 和 `/metrics` endpoint 暴露 RBG 指标。
不新增第二个 metrics endpoint。

实现分阶段：

| 阶段 | 指标 | 目的 |
|---|---|---|
| Alpha | `rbg_build_info`、`rbg_role_replicas`、`rbg_group_progressing_info`、`rbg_rollout_duration_seconds`、`rbg_pod_cache_stub_reads_total` | 容量、进度、rollout SLO、cache 正确性 |
| Alpha（KEP-284） | Pod cache scope/objects/transform/initial sync | 由 KEP-284 定义；其 alpha 必需 |
| Beta | Controller 阶段耗时/失败、Warmup 耗时/失败、RoleInstance phase 耗时、扩缩容失败、gang PodGroup 失败 | 定位慢或失败的 controller 路径 |
| 可选 | gang scheduling 耗时 | 特性调优与验证 |

状态 gauge 在抓取时从 informer cache 采集。
counter 和 histogram 在对应事件发生时记录。
只有 elected leader 发布 workload 状态 gauge。

### 用户故事

#### 故事 1：值班容量损失

运维人员收到告警：某个 `decode` role 的 ready 副本为 0，而 desired 为 8。
运维人员查询：

```promql
rbg_role_replicas{role="decode", state="desired"} > 0
  and on(namespace, rbg, role)
rbg_role_replicas{role="decode", state="ready"} == 0
```

随后查看 `rbg_group_progressing_info`，看到 `reason="WaitingForResources"`。
这将排查方向指向集群容量或 quota，而不是应用 readiness。

#### 故事 2：Rollout 阻塞或变慢

发布运维人员看到 `rbg_group_progressing_info{status="True", reason="WaitingForGang"} == 1`。
group 正在等待 gang scheduling。
如果 group 最终收敛，但 `rbg_rollout_duration_seconds` 的 p95 偏高，运维人员检查 controller 阶段耗时，定位慢的 reconcile 路径。

#### 故事 3：Pod cache 正确性

controller 开发者看到 `rbg_pod_cache_stub_reads_total > 0`。
KEP-284 预期该 counter 保持为 0。
非零值说明某个读取路径在应返回完整 Pod 时收到了 stub。

### 备注、约束与注意事项

- 指标反映 controller 观察到的状态，不是对每个 Pod 的独立探测。
- 状态 gauge 的新鲜度取决于 informer cache 和对象的 `observedGeneration`。
- controller 进程重启时，counter 和 histogram 会重置。Prometheus `rate()` 会处理 counter 重置。
- rollout 耗时样本需要开始时间。如果 controller 在完成前重启，则跳过该样本，而不是伪造数据。
- 混合后端 group 在 status 可用时仍暴露 role 副本 gauge，但在 KEP-502 进度模型适用前不暴露 `rbg_group_progressing_info`。

### 风险与缓解

| 风险 | 缓解 |
|---|---|
| label 基数随对象和 role 数量增长 | 只使用 `namespace`、`rbg`、`role` 和有界枚举 label；从当前 cache 对象采集状态，删除的对象不再产生 series |
| 过期 status 产生误导性进度 | 仅当 group 和 condition 的 `observedGeneration` 都等于 `metadata.generation` 时导出 `rbg_group_progressing_info`；过期 series 在下次 scrape 时不再发出 |
| Prometheus 缺少 RBAC 导致安全抓取失败 | 文档化并测试抓取 ServiceAccount 的 TokenReview / SubjectAccessReview 权限 |
| 指标采集增加抓取延迟 | 从 informer cache 读取，避免 per-Pod series，限制 histogram bucket |
| 重启后 rollout 耗时不准确 | 在内存中按对象 UID 和目标 revision 记录开始时间；没有开始时间则跳过样本 |

## 设计细节

### 指标清单

下表列出本 KEP 提议的所有指标。
具体语义在后续章节分别定义。

| 指标 | 阶段 | 类型 | Label | 用途 |
|---|---|---|---|---|
| `rbg_build_info` | Alpha | Gauge | `version`、`commit`、`go_version` | 标识 controller 构建版本 |
| `rbg_role_replicas` | Alpha | Gauge | `namespace`、`rbg`、`role`、`state` | 跟踪每个 role 的 desired、ready、updated 副本 |
| `rbg_group_progressing_info` | Alpha | Gauge | `namespace`、`rbg`、`status`、`reason` | 报告当前 group 进度或阻塞原因 |
| `rbg_rollout_duration_seconds` | Alpha | Histogram | `namespace`、`rbg` | 测量 rollout 完成耗时 |
| `rbg_pod_cache_stub_reads_total` | Alpha | Counter | `method` | 检测 Pod cache 非预期 stub 读取 |
| `rbg_controller_stage_duration_seconds` | Beta | Histogram | `controller`、`stage` | 定位慢的 reconcile 阶段 |
| `rbg_controller_stage_failures_total` | Beta | Counter | `controller`、`stage`、`reason_class` | 按 reconcile 阶段统计失败 |
| `rbg_warmup_duration_seconds` | Beta | Histogram | `namespace`、`warmup` | 测量 Warmup 完成耗时 |
| `rbg_warmup_failures_total` | Beta | Counter | `namespace`、`warmup`、`reason` | 按 reason 统计 Warmup 失败 |
| `rbg_roleinstance_phase_duration_seconds` | Beta | Histogram | `phase` | 测量 RI 各 phase 停留时间 |
| `rbg_scaling_adapter_scale_failures_total` | Beta | Counter | `namespace`、`adapter`、`reason_class` | 统计 scaling adapter 失败 |
| `rbg_gang_podgroup_reconcile_failures_total` | Beta | Counter | `scheduler`、`reason_class` | 统计 PodGroup reconcile 失败 |
| `rbg_pod_cache_scope` | Alpha（KEP-284） | Gauge | `scope` | 报告 Pod cache 模式 |
| `rbg_pod_cache_objects` | Alpha（KEP-284） | Gauge | `form` | 统计缓存完整 Pod 和 stub 数量 |
| `rbg_pod_cache_transform_total` | Alpha（KEP-284） | Counter | `result` | 统计 Pod cache 转换决策 |
| `rbg_pod_cache_initial_sync_seconds` | Alpha（KEP-284） | Gauge | 无 | 报告 Pod informer 初始同步耗时 |
| `rbg_gang_scheduling_duration_seconds` | 可选 | Histogram | `scheduler` | 测量 gang scheduling 完成耗时 |

### 术语

| 术语 | 定义 |
|---|---|
| RBG | RoleBasedGroup，顶层 workload CRD |
| RIS | RoleInstanceSet，管理某个 role 的 RoleInstance 的 workload |
| RI | RoleInstance，作为单个副本单位管理的一组紧密绑定 Pod |
| Role 副本 | RBG 计数的 workload 单位。一个跨多个 Pod 的 RI 计为一个副本 |
| Progressing condition | KEP-502 定义的 condition，用于报告向目标推进的进度或当前阻塞原因 |
| `observedGeneration` | status 字段，记录 controller 最近观察到的 `metadata.generation` |
| PodGroup | scheduler-plugins 或 Volcano 使用的 gang scheduling 对象 |
| Stub | KEP-284 Pod cache filter 保留的精简 Pod 对象 |
| Gauge | 值可升可降的 Prometheus 指标 |
| Counter | 只增不减的 Prometheus 指标，进程重启除外 |
| Histogram | 将观测值记录到 bucket 的 Prometheus 指标 |
| Informer cache | controller-runtime 使用的 Kubernetes 对象本地缓存 |
| Leader election | 选择一个 controller 副本执行主动 reconcile 的机制 |
| ServiceMonitor | Prometheus Operator 自定义资源，定义如何抓取 Service |
| TokenReview | 用于认证 bearer token 的 Kubernetes API |
| SubjectAccessReview | 用于对用户或 ServiceAccount 请求授权的 Kubernetes API |
| SLO | Service Level Objective，服务级别目标 |
| SLI | Service Level Indicator，用于评估 SLO 的测量值 |
| p95 | 分布的第 95 百分位 |

### 指标命名与标签

所有指标使用 `rbg_` 前缀。
耗时指标使用 `_seconds` 后缀。
累计事件指标使用 `_total` 后缀。

允许的 label：

| Label | 含义 | 基数 |
|---|---|---|
| `namespace` | 对象 namespace | 受集群 namespace 数量限制 |
| `rbg` | RoleBasedGroup 名称 | 受 RBG 对象数量限制 |
| `role` | RBG 内部 role 名称 | 受每个 RBG 的 role 数量限制 |
| `state` | 副本状态：`desired`、`ready`、`updated` | 固定枚举 |
| `status` | condition 状态：`True`、`False`、`Unknown` | 固定枚举 |
| `reason` | 有界 reason：进度指标用 KEP-502 进度 reason（或 `Other`），Warmup 等特性指标用特性自身的有界 reason | 有界枚举 |
| `controller` | controller 名称 | 固定枚举 |
| `stage` | reconcile 阶段名称 | 固定枚举 |
| `reason_class` | KEP-502 taxonomy 中归类后的操作失败家族 | 有界枚举 |
| `phase` | RI 生命周期 phase | 固定枚举 |
| `scheduler` | gang scheduler 后端 | 固定枚举 |
| `method` | Pod cache 读取路径 | 有界枚举 |
| `warmup` | Warmup 对象名 | 受 Warmup 对象数量限制 |
| `adapter` | Scaling adapter 名称 | 受 adapter 对象数量限制 |
| `scope` | Pod cache 模式：`all`、`filtered` | 固定枚举 |
| `form` | 缓存 Pod 形式：`whole`、`stub` | 固定枚举 |
| `result` | 转换决策：`whole`、`stub` | 固定枚举 |
| `version`、`commit`、`go_version` | 构建信息 | 每个构建一条 series |

不使用 Pod 名称、node 名称、revision hash、generation 值、condition message 或原始错误字符串作为 label。

### 构建信息指标

```text
rbg_build_info{version, commit, go_version}
```

类型：Gauge。
值：`1`。

该指标标识 controller 构建版本。
它不描述 workload 状态。
值固定为 `1`；构建元数据通过 label 携带，并通过 PromQL 查询，而不是从 metric value 读取。

示例查询：

```promql
# 当前构建 series，包含 version 和 commit label。
rbg_build_info

# 按构建统计 controller 副本数。
count by (version, commit) (rbg_build_info)

# 当副本运行非预期版本时告警。
rbg_build_info{version!="v0.8.0"} == 1

# 当存在多个版本时告警。
count(count by (version) (rbg_build_info)) > 1
```

### Workload 容量指标

```text
rbg_role_replicas{namespace, rbg, role, state}
```

类型：Gauge。

`state` 值：

| State | 来源 | 含义 |
|---|---|---|
| `desired` | API defaulting 后的 `spec.roles[].replicas` | 目标副本数 |
| `ready` | `status.roleStatuses[].readyReplicas` | 报告为 Ready 的副本数 |
| `updated` | `status.roleStatuses[].updatedReplicas` | 报告处于 update revision 的副本数 |

语义：

- 按 workload 单位计数。一个跨四个 Pod 的 RI 计为一个副本。
- role 存在于 spec 时发布 `desired`。
- 存在匹配 status 条目时发布 `ready` 和 `updated`，包括显式 0。
- 按 role 名称匹配 spec 和 status 条目。
- 删除 role 或 RBG 时移除所有对应 series。
- 不同 namespace 中同名 RBG 产生独立 series。

示例：

```promql
rbg_role_replicas{state="ready"}
  / ignoring(state)
(rbg_role_replicas{state="desired"} > 0)
```

### Group 进度指标

```text
rbg_group_progressing_info{namespace, rbg, status, reason}
```

类型：Gauge。
值：当前 `Progressing` condition 对应为 `1`。

语义：

- 仅当所有 role 都使用 RoleInstanceSet 时导出。
- 仅当 RBG 的 `status.observedGeneration` 和 condition 的 `observedGeneration` 都等于 `metadata.generation` 时导出。
- 使用 condition 的 `status` 和 controller 选择的 `reason`。
- 未知 reason 映射为 `Other`。
- 按实际报告导出 `Unknown/ObservationIncomplete`。
- condition 变化、消失或过期时，旧 series 在下次 scrape 时不再发出。

reason 值遵循 KEP-502 taxonomy，包括 `Converged`、`Paused`、`WaitingForQuota`、`WaitingForResources`、`WaitingForGang`、`WaitingForDependencies`。

示例：

```promql
rbg_group_progressing_info{status="True", reason="WaitingForQuota"} == 1
```

### Rollout 耗时指标

```text
rbg_rollout_duration_seconds{namespace, rbg}
```

类型：Histogram。

rollout 指从一个目标 revision 切换到另一个目标 revision 的过程。
耗时从第一次观察到新目标的 reconcile 开始，到 group 达到该目标且所有 role updated 并 ready 的 reconcile 结束。

语义：

- 在内存中记录开始时间，key 为 RBG UID 和目标 revision。
- rollout 完成时观测 histogram。
- 如果没有开始时间，跳过样本。
- 本 KEP 不在 RBG API 中持久化 rollout 开始时间。

建议 bucket，单位秒：

```text
1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600
```

示例：

```promql
histogram_quantile(
  0.95,
  rate(rbg_rollout_duration_seconds_bucket[5m])
)
```

### Pod Cache 指标（KEP-284）

这些指标由 [KEP-284](../284-pod-cache-filter/README.md) 定义。
定义归属保留在 KEP-284；本 KEP 负责实现。
全部五个都是 KEP-284 alpha 毕业的必需项，因此随 KEP-284 alpha 一并交付，而非在此列为可选。

```text
rbg_pod_cache_stub_reads_total{method}
```

类型：Counter。

它统计缓存 Pod `List` 返回 stub 的次数。
预期值为 0。
非零值表示某个读取路径在应返回完整 Pod 时收到了 stub。

`method` 标识返回 stub 的读取路径。
实现必须使用有界的 method 名称集合。

示例告警：

```promql
rbg_pod_cache_stub_reads_total > 0
```

其余 KEP-284 指标：

| 指标 | 类型 | Label | 含义 |
|---|---|---|---|
| `rbg_pod_cache_scope` | Gauge | `scope` | 配置的 Pod cache 模式，`all` 或 `filtered`，置为 1 |
| `rbg_pod_cache_objects` | Gauge | `form` | 按形式统计缓存 Pod：`whole` 或 `stub` |
| `rbg_pod_cache_transform_total` | Counter | `result` | Pod 转换决策：`whole` 或 `stub` |
| `rbg_pod_cache_initial_sync_seconds` | Gauge | 无 | Pod informer 初始同步耗时 |

### Controller 阶段指标

这些指标目标为 beta。

```text
rbg_controller_stage_duration_seconds{controller, stage}
rbg_controller_stage_failures_total{controller, stage, reason_class}
```

`controller` 示例：

- `rolebasedgroup`
- `roleinstanceset`
- `roleinstance`
- `rolebasedgroupwarmup`
- `rolebasedgroupscalingadapter`

`stage` 示例：

| Controller | Stage | 含义 |
|---|---|---|
| `rolebasedgroup` | `precheck` | 校验 role template、依赖和 gang annotation |
| `rolebasedgroup` | `revisions` | 获取或创建 ControllerRevision，计算 role hash |
| `rolebasedgroup` | `reconcile_roles` | 创建或更新 role workload |
| `roleinstanceset` | `validate_membership` | 校验 committed membership |
| `roleinstanceset` | `stateful_reconcile` | reconcile stateful RoleInstanceSet |
| `roleinstance` | `get_owned_pods` | 列出 RI 拥有的 Pod |
| `roleinstance` | `sync_instance` | 创建、更新、恢复或删除 Pod |
| `roleinstance` | `update_status` | 写入 RI status |

`reason_class` 使用 KEP-502 的有界操作失败家族，例如 `PermissionDenied`、`AdmissionDenied`、`UnclassifiedBlocker`。
`WaitingFor*` 是阻塞原因而非操作失败；它们通过进度/阻塞指标报告，而不计入 `*_failures_total`。
不使用原始错误 message。

这些指标补充而不是替代 `controller_runtime_reconcile_time_seconds` 和 `controller_runtime_reconcile_errors_total`。

### 特性指标

这些指标目标为 beta。

| 指标 | 类型 | Label | 含义 |
|---|---|---|---|
| `rbg_warmup_duration_seconds` | Histogram | `namespace`、`warmup` | Warmup 从开始到完成的耗时 |
| `rbg_warmup_failures_total` | Counter | `namespace`、`warmup`、`reason` | 按有界 reason 统计 Warmup 失败 |
| `rbg_roleinstance_phase_duration_seconds` | Histogram | `phase` | RI 生命周期 phase 停留时间 |
| `rbg_scaling_adapter_scale_failures_total` | Counter | `namespace`、`adapter`、`reason_class` | Scaling adapter 失败 |
| `rbg_gang_podgroup_reconcile_failures_total` | Counter | `scheduler`、`reason_class` | PodGroup reconcile 失败 |

RI phase 为 KEP-502 定义的 `Creating`、`Running`、`Updating`、`Recovering`。
KEP-502 不定义 `Terminating` phase；删除由 `metadata.deletionTimestamp` 表示，而非 phase。
`status.phase` 缺失的 RI 应跳过，不归入臆造值。

### 可选指标

该指标不是 alpha 或 beta 的必需项。
当 gang scheduling 需要调优或验证时再添加。

| 指标 | 类型 | Label | 含义 |
|---|---|---|---|
| `rbg_gang_scheduling_duration_seconds` | Histogram | `scheduler` | 从 PodGroup 创建到 gang scheduling 完成的耗时 |

### 采集架构

所有指标注册到 `sigs.k8s.io/controller-runtime/pkg/metrics.Registry`。
它们由现有 manager metrics endpoint 提供。

状态 gauge：

- 实现自定义 Prometheus collector，在抓取时从 informer cache 读取 RBG 对象。
- 从当前缓存对象发出 `rbg_role_replicas` 和 `rbg_group_progressing_info`。
- 删除的对象不再发出 series，因为 collector 只遍历当前对象。
- 只有 elected leader 运行 workload 状态 collector。
- leader 切换后，新 leader 从自己的 cache 重建 series。

事件指标：

- 在事件发生的代码路径递增 counter。
- 在被测量操作完成时观测 histogram。
- 使用有界 `reason_class`。
- 不在每次 reconcile 时写状态 gauge。

Pod cache 指标：

- 在 KEP-284 cache filter 和读取路径埋点。
- 除非 stub 通过非预期读取路径返回，否则 `rbg_pod_cache_stub_reads_total` 保持为 0。

不引入新的 informer 或 watch。
collector 复用 controller 已经需要的 RBG informer。

### 安全抓取与部署

controller 已支持：

- `--metrics-bind-address`
- `--metrics-secure`
- controller-runtime 认证与授权 filter

本 KEP 增加部署指引和 Helm 资源：

- 指向 controller metrics port 的 metrics Service。
- 可选的 Prometheus Operator ServiceMonitor。
- 启用安全 metrics 时，Prometheus ServiceAccount 的 RBAC 指引。

当 `--metrics-secure=true` 时，抓取 ServiceAccount 必须能够创建：

- `TokenReviews`
- `SubjectAccessReviews`

缺少这些权限时，metrics endpoint 存在，但 Prometheus 无法抓取。

### 测试计划

[x] 我/我们理解相关组件的 owner 可能要求更新现有测试，以确保代码足够健壮，然后再提交实现本增强所需的变更。

#### 前置测试更新

- KEP-502 status 和 reason taxonomy 测试必须覆盖 `rbg_group_progressing_info` 消费的 condition。
- KEP-284 cache filter 测试必须覆盖 whole/stub 转换和读取路径。

#### 单元测试

- `rbg_role_replicas` 发出 desired、ready、updated 值，包括显式 0。
- spec 和 status 条目按 role 名称匹配。
- 删除 role 和 RBG 时移除 series。
- 不同 namespace 中同名 RBG 产生独立 series。
- 仅当 generation 检查通过时导出 `rbg_group_progressing_info`。
- 正确处理过期、缺失和 `Unknown/ObservationIncomplete` condition。
- 未知 reason 映射为 `Other`。
- 仅当存在开始时间时观测 rollout 耗时样本。
- 仅非预期 stub 读取递增 Pod cache stub reads。
- label 值有界；不使用原始错误作为 label。

#### 集成测试

- 使用 envtest 启动 controller 并抓取 `/metrics`。
- 验证 RBG 创建和 status 更新后出现 alpha 指标。
- 验证 RBG 删除后 series 消失。
- 验证 leader election：只有 leader 发布 workload 状态 gauge。
- 使用具备 TokenReview / SubjectAccessReview 权限的 ServiceAccount 验证安全抓取。
- 验证启用安全 metrics 但缺少权限时抓取失败。

#### 端到端测试

- 部署 Prometheus 并抓取 controller metrics Service。
- 执行 scale-up，验证 desired、ready、updated series 变化。
- 执行 rollout，验证 `rbg_rollout_duration_seconds` 记录样本。
- 验证正常运行时 `rbg_pod_cache_stub_reads_total` 保持为 0。
- 验证示例告警查询返回预期结果。

### 毕业标准

#### Alpha

- 实现并注册五个核心 alpha 指标。
- 按 KEP-284 alpha 毕业要求导出 KEP-284 Pod cache 指标。
- 单元和集成测试通过。
- 文档化安全抓取权限。
- 用户文档列出指标名称、label 和示例查询。

#### Beta

- 实现 controller 阶段、Warmup、RI phase、扩缩容和 gang 失败指标。
- reason label 使用 KEP-502 taxonomy，不使用原始错误字符串。
- 在代表性集群规模下测量基数和抓取延迟。
- 测试升级和降级路径。
- 文档化 SLO 示例和告警规则。

#### GA

- 指标名称和 label 语义稳定。
- 至少两个 release 的 beta 反馈已处理。
- 文档化指标变更的废弃策略。

### 升级与降级策略

增加指标不改变 RBG API 类型或 workload 行为。
升级 controller 会在 `/metrics` 增加新 series。
降级会移除这些 series。

依赖新指标的 dashboard 和告警必须在降级期间容忍缺失 series。
Helm 回滚会移除本 KEP 增加的 metrics Service 和可选 ServiceMonitor。

### 版本差异策略

该功能仅涉及 controller。
不需要 node 组件或 API server 变更。

版本差异期间，旧 controller 不暴露新指标。
Prometheus 看到缺失 series，而不是错误值。
不需要与 kubelet、kube-proxy 或 scheduler 协调。

## 生产就绪评审问卷

### 功能启用与回滚

###### 如何在运行中的集群启用或禁用该功能？

启用 controller metrics endpoint 时暴露指标。
没有单独 feature gate。

- 启用：将 `--metrics-bind-address` 设置为非零地址，例如 `:8443`。
- 禁用：设置 `--metrics-bind-address=0`。

禁用 endpoint 需要重启 controller。
不影响运行中的 workload。

###### 启用该功能是否改变默认行为？

不改变 workload 行为。
controller 在现有 endpoint 上暴露额外指标。

###### 启用后能否禁用？

可以。
设置 `--metrics-bind-address=0` 或降级 controller。
现有 workload 不受影响。
依赖被移除指标的 Prometheus 查询将返回无数据。

###### 禁用后再次启用会发生什么？

状态 gauge 从 informer cache 重建。
counter 和 histogram 在进程重启后从 0 开始。

###### 是否有启用或禁用测试？

集成测试在启用 endpoint 时抓取，并验证禁用时不提供 metrics。

### 发布、升级与回滚计划

###### 发布或回滚可能如何失败？是否影响运行中的 workload？

主要失败模式是抓取认证配置错误和意外 label 基数。
两者都不改变 workload reconcile。
运行中的 workload 不受指标启用或回滚影响。

###### 哪些指标应触发回滚？

- `controller_runtime_reconcile_errors_total`
- `controller_runtime_reconcile_time_seconds`
- `process_resident_memory_bytes`
- Prometheus 对 controller target 的抓取耗时和样本数

###### 是否测试了升级和回滚？

测试计划要求升级、降级、再升级抓取测试。

###### 发布是否伴随废弃或移除？

没有。

### 监控要求

###### 运维如何判断功能正在使用？

`rbg_build_info` 和 `rbg_role_replicas` 的存在表示 controller 正在暴露 workload 指标。

###### 使用者如何判断功能正常？

- `rbg_role_replicas` 与其 namespace 和 RBG 名称对应的 RBG spec/status 一致。
- `rbg_group_progressing_info` 反映当前 `Progressing` condition。
- 完成 rollout 后，`rbg_rollout_duration_seconds` 记录样本。
- Pod cache filter 健康时，`rbg_pod_cache_stub_reads_total` 保持为 0。

###### 合理的 SLO 是什么？

- 正常运行期间 `rbg_pod_cache_stub_reads_total` 保持为 0。
- 按集群跟踪 rollout 耗时 p95，并与运维人员的发布预期比较。具体目标取决于部署，本 KEP 不固定。
- controller 抓取耗时低于 Prometheus scrape timeout。

###### SLI 是什么？

- 指标：
  - `rbg_role_replicas`
  - `rbg_group_progressing_info`
  - `rbg_rollout_duration_seconds`
  - `rbg_pod_cache_stub_reads_total`
  - `controller_runtime_reconcile_errors_total`

###### 还缺哪些指标？

per-role rollout 耗时和 gang scheduling 耗时有用，但为保持 alpha 集合小，推迟到 beta 或可选阶段。

### 依赖

###### 该功能是否依赖集群中的特定服务？

只有抓取和告警需要 Prometheus。
没有 Prometheus 时，controller 仍可暴露指标。

###### 该功能是否依赖特定 API？

依赖现有 RBG、RIS、RI API 和 KEP-502 status 字段。
Pod cache 指标依赖 KEP-284。

###### 该功能是否依赖其他组件的特定版本？

使用 RBG 已 vendor 的 controller-runtime 版本。
不需要新的外部组件版本。

### 规模

###### 启用该功能是否新增 API 调用？

不新增 API 调用。
状态 gauge 从现有 informer cache 读取。
事件指标在现有 reconcile 路径记录。

###### 启用该功能是否引入新 API 类型？

不引入。

###### 启用该功能是否新增云厂商调用？

不新增。

###### 启用该功能是否增加现有 API 对象的大小或数量？

不增加。

###### 启用该功能是否增加现有操作耗时？

抓取时采集增加的工作量与缓存 RBG 对象和 role 数量成正比。
不增加 Pod 创建、更新、删除路径的工作量。

###### 启用该功能是否增加资源使用？

预期增加很小：一个遍历缓存 RBG 对象的 collector，以及一组有界 counter/histogram。
基数约为：

```text
3 * number_of_roles
  + number_of_rbg_progressing_series
  + 13 * number_of_RBGs_with_completed_rollouts
  + fixed_series
```

其中：

- `number_of_roles` 是缓存 RBG 中的 role 总数（每个 role 三个 `state` 值）。
- `number_of_rbg_progressing_series` 最多为每个具有当前进度 condition 的 RBG 一条。
- `13 * number_of_RBGs_with_completed_rollouts` 对应 `rbg_rollout_duration_seconds{namespace,rbg}`：10 个 bucket 加 `+Inf`、`_sum`、`_count`，每个完成过至少一次 rollout 的 RBG 都会保留。
- `fixed_series` 涵盖 `rbg_build_info`、Pod cache stub 读取，以及（beta 阶段的）controller 阶段 series，与对象数量无关。

###### 启用该功能是否可能导致资源耗尽？

不引入 node 资源。
主要风险是 Prometheus series 基数，通过有界 label 和基于 cache 的采集缓解。

### 故障排查

###### API server 或 etcd 不可用时，功能如何表现？

informer cache 可能过期。
状态 gauge 反映最后缓存状态。
generation 检查失败时不再发出 `rbg_group_progressing_info`，减少误导性进度报告。

###### 其他已知故障模式？

- controller 不是 leader 导致指标缺失。
  - 检测：`leader_election_master_status` 为 0，且 workload gauge 缺失。
  - 缓解：抓取 leader，或使用选择 leader 的 Service。
  - 诊断：controller leader election 日志。
  - 测试：两个 controller 副本的集成测试。

- 安全抓取返回 401/403。
  - 检测：Prometheus target health 和 scrape error。
  - 缓解：授予抓取 ServiceAccount TokenReview / SubjectAccessReview 权限。
  - 诊断：controller metrics filter 日志。
  - 测试：有/无 RBAC 的集成测试。

- 进度 condition 过期。
  - 检测：RBG status 存在，但 `rbg_group_progressing_info` 缺失。
  - 缓解：检查 `observedGeneration` 和 controller reconcile 错误。
  - 诊断：RBG status 和 controller 日志。
  - 测试：过期 condition 不再发出的单元测试。

###### SLO 不满足时应采取什么步骤？

1. 查看 `rbg_group_progressing_info` 的当前阻塞 reason。
2. 查看 `rbg_role_replicas` 的 desired/ready/updated 差距。
3. 查看 `rbg_rollout_duration_seconds` 的慢 rollout。
4. beta 指标可用后，查看 controller 阶段耗时和失败。
5. 查看 `rbg_pod_cache_stub_reads_total` 的 cache 正确性违规。

## 实施历史

- 2026-11-24：初始 provisional KEP。

## 缺点

- 增加指标维护和文档负担。
- 引入随 RBG 和 role 数量增长的对象级 series。
- 如果运维人员将 controller 观察状态当作独立 Pod 探测，可能产生误导。

## 替代方案

### 导出所有 status 字段

拒绝。
大多数 status 字段用于检查单个对象，而不是告警或聚合。
导出它们会增加基数，并重复 Kubernetes status。

### 仅使用 kube-state-metrics custom resource state

适合通用 status 导出，但不提供 controller 专属事件指标，例如 rollout 耗时、阶段失败、Pod cache stub 读取。
本 KEP 可与 kube-state-metrics 共存。

### 每次 reconcile 写状态 gauge

对 workload 状态 gauge 拒绝。
它需要显式删除清理，并可能在 leader 切换后留下过期 series。
基于 cache 的 collector 从当前对象重建状态。

### 四个独立 role 副本指标

Issue #641 提议独立 desired、observed、ready、updated 指标。
本 KEP 使用一个带 `state` label 的 `rbg_role_replicas`，减少近似重复的指标名。
不导出 `observed` state，因为观察新鲜度由 `rbg_group_progressing_info` 表达。

## 所需基础设施

除现有 RBG controller 和可选 Prometheus 部署外，无其他基础设施。

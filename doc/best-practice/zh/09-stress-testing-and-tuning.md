# 大规模集群资源评估、配置与压测
## 概述
将 RBG Controller 部署到大规模集群（数百到上千个 RBG 实例）之前，需要回答三个问题：

1. **Controller 需要多少资源？** CPU、内存、并发 Reconcile 数、API QPS 如何配置
2. **Controller 能否扛住目标规模？** 在目标 RBG 数量和操作 QPS 下，延迟和吞吐是否满足预期
3. **瓶颈在哪里？** 是 Reconcile 并发不足、API 限流、还是 CPU/内存瓶颈

RBG 提供一套完整的压测工具链来解决这些问题：

+ `/stress-test`** Skill**：Claude Code 内置的压测 Skill，通过对话式交互完成从环境搭建到结果分析的全流程
+ **压测客户端**（`test/stress/`）：Go 编写的压测驱动，支持 Create/Update/Delete 三阶段速率控制的负载测试
+ **KWOK 模拟**：使用 [KWOK](https://kwok.sigs.k8s.io/) 在真实集群中模拟数千个 Pod，无需实际 GPU 资源

```plain
┌──────────────────────────────────────────────────────────────────┐
│                     压测工具链架构                                  │
│                                                                  │
│  /stress-test Skill                                              │
│  ┌───────────────┐  ┌──────────────┐  ┌──────────────┐          │
│  │  对话式交互     │─→│  自动化编排   │─→│  AI 驱动分析  │          │
│  │  配置压测场景   │  │  环境搭建     │  │  瓶颈定位     │          │
│  │              │  │  压测执行     │  │  调优建议     │          │
│  └───────────────┘  │  Profiling   │  └──────────────┘          │
│                     │  报告生成     │                             │
│                     └──────┬───────┘                             │
│                            │                                     │
│                     ┌──────▼───────┐                             │
│                     │  Go 压测客户端 │                             │
│                     │  (test/stress)│                             │
│                     └──────┬───────┘                             │
│                            │                                     │
│              ┌─────────────┼─────────────┐                       │
│              ▼             ▼             ▼                       │
│       ┌────────────┐ ┌─────────┐ ┌────────────┐                 │
│       │ Controller │ │  KWOK   │ │  Pprof     │                 │
│       │  (真实 Pod) │ │ (模拟)  │ │  (性能画像) │                 │
│       └────────────┘ └─────────┘ └────────────┘                 │
└──────────────────────────────────────────────────────────────────┘
```

## 前提条件
+ Kubernetes 集群版本 >= 1.24
+ 已安装 RBG CRD（参考 [安装指南](https://github.com/sgl-project/rbg)）
+ 本地工具：`helm`、`kubectl`、`go`、`curl`
+ （可选）Controller 镜像支持 `--enable-pprof`（用于性能画像采集）

---

## 背景：为什么需要压测
RBG Controller 负责管理所有 RBG 实例的全生命周期——创建子资源（Deployment/LeaderWorkerSet/Service）、监控 Pod 状态、执行滚动更新、协调角色依赖。每个 RBG 实例的 Reconcile 循环涉及多次 API Server 交互，当管理数百个 RBG 时，Controller 的处理能力直接影响：

| 影响维度 | 说明 |
| --- | --- |
| **首次部署延迟** | 从 `kubectl apply` 到所有 Pod Ready 的端到端时间 |
| **滚动更新速度** | 大规模更新场景下，所有实例完成更新的总耗时 |
| **状态同步时效** | Pod 状态变更后，Controller 感知并响应的时间 |
| **API Server 压力** | Controller 对 API Server 的读写频率，过高会导致限流 |


在没有压测数据的情况下，凭经验配置 Controller 参数往往会导致两个极端：

+ **过度配置**：分配过多 CPU/内存，浪费集群资源
+ **配置不足**：Reconcile 队列堆积，操作延迟飙升，更新超时

压测的目标是找到**目标规模下的最优配置**——在满足延迟要求的前提下，使用最少的资源。

---

## 压测架构：KWOK 模拟
压测使用 [KWOK](https://kwok.sigs.k8s.io/)（Kubernetes WithOut Kubelet）在真实集群中模拟工作负载 Pod，无需实际的 GPU 或计算资源。

### 工作原理
```plain
真实节点                              KWOK 模拟节点
┌─────────────────────────┐         ┌─────────────────────────┐
│ RBG Controller Pod      │         │ kwok-node-0..N           │
│ (Helm 部署，真实运行)     │         │ (KWOK 创建的虚拟节点)     │
│                         │         │                          │
│ kwok Controller Pod     │         │ 工作负载 Pod              │
│ (模拟 Pod 生命周期)      │         │ (自动模拟 Ready 状态)     │
└─────────────────────────┘         └─────────────────────────┘
         │                                    ▲
         │ Watch Pod 事件                     │ 调度到模拟节点
         └────────────────────────────────────┘
```

KWOK 通过 Stage CRD 模拟 Pod 的生命周期。

`setup-kwok.sh` 脚本安装两套 Stage：

- **官方 `stage-fast` chart**：提供 Node 相关 Stage（`node-heartbeat-with-lease`、`node-initialize`），模拟节点心跳和初始化
- **项目自定义 Pod 生命周期 Stage**（`test/stress/templates/kwok-stage.yaml`）：覆盖官方默认 Pod Stage，提供更精确的延迟控制

Pod 生命周期 Stage 如下：

| Stage | 行为 | 延迟 |
| --- | --- | --- |
| `pod-initialize` | 分配 PodIP，设置为 Pending | 即时 |
| `pod-running` | 转换为 Running 状态 | 500ms |
| `pod-ready` | 设置 Ready 条件为 True | 1000ms |
| `pod-delete` | 删除 Pod | 500ms |


这意味着每个模拟 Pod 从创建到 Ready 约 1.5 秒，可以模拟大规模 Pod 的就绪过程。

### 模拟节点配置
每个 KWOK 模拟节点的默认容量：

| 资源 | 默认值 | 说明 |
| --- | --- | --- |
| CPU | 128 核 | 模拟的 CPU 容量，用于调度决策 |
| 内存 | 512Gi | 模拟的内存容量 |
| Pod 数 | 1000 | 单节点最大 Pod 数 |


> **说明**：模拟节点的容量是"虚拟"的，仅影响调度器的调度决策。实际 Pod 不运行真实容器，不消耗这些资源。
>

---

## 资源评估：Controller 需要多少资源
在运行压测之前，需要先估算目标规模下 Controller 的资源需求。以下是基于 RBG Controller 架构的资源评估方法。

### 计算资源规模
每个 RBG 实例的管理开销取决于角色数量和角色类型：

| 子资源 | 每个角色产生的 API 操作（单次 Reconcile） |
| --- | --- |
| 角色子资源（Deployment/LWS） | 1 次 Get + 1 次 Create/Update |
| Service（服务发现） | 1 次 Get + 1 次 Create/Update |
| RoleInstanceSet | 1 次 Get + 1 次 Create/Update |
| Status 更新 | 1 次 Patch |
| **合计** | **约 4~8 次 API 操作/角色/Reconcile** |


假设管理 500 个 RBG，每个 RBG 包含 3 个角色：

```plain
总角色数 = 500 × 3 = 1500
API 操作/轮 = 1500 × 6（平均）= 9000 次
```

### 参数配置指南
| 参数 | 说明 | 推荐公式 | 示例（500 RBG × 3 角色） |
| --- | --- | --- | --- |
| `max-concurrent-reconciles` | 并行 Reconcile 工作线程数 | `总角色数 / 目标 P99 秒数` | 1500 / 30 = 50 |
| `kube-api-qps` | API Server 持续请求速率 | `max-concurrent-reconciles × 3` | 50 × 3 = 150 |
| `kube-api-burst` | API Server 突发请求上限 | `kube-api-qps × 2` | 150 × 2 = 300 |
| CPU | Controller 计算资源 | 根据并发数和 API 调用量 | 8~16 核 |
| 内存 | Controller 内存资源 | Informer 缓存与对象数量成正比 | 16~32Gi |


### 推荐配置档位
| 规模 | RBG 数量 | 角色总数 | 推荐配置 |
| --- | --- | --- | --- |
| 小型 | < 50 | < 150 | CPU 4 核 / 内存 8Gi / Reconciles 10 / QPS 50 / Burst 100 |
| 中型 | 50~200 | 150~600 | CPU 8 核 / 内存 16Gi / Reconciles 20 / QPS 100 / Burst 200 |
| 大型 | 200~500 | 600~1500 | CPU 16 核 / 内存 32Gi / Reconciles 50 / QPS 200 / Burst 400 |
| 超大型 | 500~1000+ | 1500+ | CPU 32 核 / 内存 64Gi / Reconciles 100 / QPS 500 / Burst 1000 |


> **说明**：以上配置为参考起点。实际最优配置需要通过压测验证——这正是本文后续章节的内容。
>

### Controller 关键参数说明
| 参数 | 说明 | 影响 |
| --- | --- | --- |
| `max-concurrent-reconciles` | Controller 并行处理 Reconcile 循环的工作线程数 | 值越大，吞吐越高，但 CPU 和内存消耗增加，且可能加剧 API Server 冲突 |
| `kube-api-qps` | Controller 对 API Server 的持续请求速率限制 | 值过低会导致客户端限流（`Throttling request took Xs`），值过高会给 API Server 带来压力 |
| `kube-api-burst` | 在 QPS 之上的突发请求容量 | 允许短时间内的请求峰值，避免在 Reconcile 高峰时被限流 |


---

## 快速开始：使用 /stress-test Skill
`/stress-test` 是一个 Claude Code 内置的 Skill，通过对话式交互完成压测全流程。在 Claude Code 中输入 `/stress-test` 即可启动。

### 使用流程
```plain
/stress-test
```

Skill 会依次询问以下配置项：

| 配置项 | 选项 | 说明 |
| --- | --- | --- |
| **测试模式** | 手动模式 / 自动调优模式 | 手动模式执行一轮压测并输出报告；自动调优模式运行多轮并推荐最优配置 |
| **Controller 资源** | 4c/8g、8c/16g、16c/32g、32c/64g | Controller Pod 的资源限制 |
| **Controller 参数** | Reconciles / QPS / Burst | Controller 运行时参数 |
| **工作负载模板** | 角色数 / LWS 角色数 / LWS 大小 / RBG 总数 | 模拟的 RBG 工作负载结构 |
| **操作 QPS** | Create / Update / Delete QPS | 各阶段的提交速率 |
| **更新策略** | 原地更新 / 重建更新 | Update 阶段的更新方式 |


Skill 自动执行以下阶段：

```plain
┌──────────────────────────────────────────────────────────────────┐
│                    /stress-test 执行流程                           │
│                                                                  │
│  Phase 1: 环境搭建                                                │
│  ├── 部署 KWOK Controller + Stage（模拟 Pod 生命周期）             │
│  ├── 创建 KWOK 模拟节点（默认 5 个，各 128 CPU / 512Gi）           │
│  └── 通过 Helm 部署/升级 RBG Controller（配置资源、参数、pprof）    │
│                                                                  │
│  Phase 2: 执行压测                                                │
│  ├── Create 阶段：按目标 QPS 创建 N 个 RBG，等待全部 Ready         │
│  ├── Update 阶段：按目标 QPS 更新所有 RBG，等待全部完成             │
│  └── Delete 阶段：按目标 QPS 删除所有 RBG，等待全部清除             │
│                                                                  │
│  Phase 3: Profiling（可选）                                       │
│  ├── CPU Profile：每个阶段采集 30 秒 CPU 画像（与压测并行）         │
│  ├── Heap/Allocs/Goroutine：每个阶段结束后采集内存快照              │
│  └── 生成 Top-N 文本报告                                          │
│                                                                  │
│  Phase 4: 报告生成                                                │
│  ├── HTML 报告：延迟分布图、错误分类、pprof 数据                    │
│  ├── summary.json：P50/P90/P99/Max/Avg/实际 QPS                  │
│  ├── timing-*.csv：每个操作的精确延迟数据                          │
│  └── errors.log + controller-full.log：Controller 日志            │
│                                                                  │
│  Phase 5: AI 驱动分析                                             │
│  ├── 吞吐量差距分析（实际 QPS vs 目标 QPS）                        │
│  ├── 延迟分布分类（均匀 / 尾部延迟 / 队列饱和）                     │
│  ├── 瓶颈定位（Reconciles / API QPS / CPU / 内存）                │
│  └── 具体调优建议（参数名 + 推荐值 + 理由）                         │
└──────────────────────────────────────────────────────────────────┘
```

---

## 手动执行压测
如果不使用 Skill，也可以手动执行压测的各个步骤。

### 步骤 1：搭建 KWOK 环境
```bash
# 安装 KWOK Controller + Stage（官方 stage-fast + 项目自定义 Pod 生命周期），创建 5 个模拟节点
FAKE_NODE_COUNT=5 bash test/stress/scripts/setup-kwok.sh

# 验证模拟节点已就绪
kubectl get nodes -l type=kwok
```

自定义模拟节点容量：

```bash
FAKE_NODE_COUNT=10 \
FAKE_NODE_CPU=256 \
FAKE_NODE_MEMORY=1024Gi \
FAKE_NODE_PODS=2000 \
bash test/stress/scripts/setup-kwok.sh
```

### 步骤 2：部署 Controller
```bash
# 部署 Controller，配置资源、参数和 pprof
CONTROLLER_CPU=8 CONTROLLER_MEMORY=16Gi \
MAX_RECONCILES=20 KUBE_API_QPS=100 KUBE_API_BURST=200 \
PPROF_ENABLED=true \
bash test/stress/scripts/deploy-controller.sh
```

部署脚本会：

1. 构建 Controller 镜像
2. 通过 Helm 部署（或升级）Controller，设置资源限制和运行时参数
3. 等待 Controller Pod 就绪
4. 建立 pprof 的端口转发（`localhost:6060`）

### 步骤 3：运行压测
```bash
go run ./test/stress/ \
    --namespace=stress-test \
    --total-rbgs=100 \
    --roles-per-rbg=3 \
    --lws-roles=1 \
    --lws-size=4 \
    --create-qps=5 \
    --update-qps=5 \
    --delete-qps=5 \
    --in-place-update=true \
    --use-kwok-nodes=true \
    --pprof-addr=localhost:6060 \
    --output-dir=/tmp/rbg-stress-results \
    --timeout=30m
```

#### 压测客户端参数说明
| 参数 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--namespace` | string | `stress-test` | 压测命名空间 |
| `--total-rbgs` | int | 10 | 创建的 RBG 实例总数 |
| `--roles-per-rbg` | int | 2 | 每个 RBG 的角色数 |
| `--lws-roles` | int | 0 | 使用 LeaderWorkerPattern 的角色数 |
| `--lws-size` | int | 4 | 每个 LWS 角色的 Pod 数 |
| `--create-qps` | float | 5 | Create 阶段提交速率（次/秒） |
| `--update-qps` | float | 5 | Update 阶段提交速率（次/秒） |
| `--delete-qps` | float | 5 | Delete 阶段提交速率（次/秒） |
| `--in-place-update` | bool | false | 使用 InPlaceIfPossible 策略（true）或 RecreatePod（false） |
| `--use-kwok-nodes` | bool | true | 调度到 KWOK 模拟节点 |
| `--pprof-addr` | string | "" | Controller pprof 地址，为空则跳过 Profiling |
| `--output-dir` | string | `/tmp/rbg-stress-results` | 输出目录 |
| `--timeout` | duration | 30m | 总超时时间 |
| `--max-concurrent-waiters` | int | 0 | 最大并发等待协程数（0 = 不限制） |
| `--controller-namespace` | string | `rbgs-system` | Controller 所在命名空间 |
| `--controller-label` | string | `control-plane=rbgs-controller` | Controller Pod 标签选择器 |


### 步骤 4：查看结果
```bash
# 打开 HTML 报告
open /tmp/rbg-stress-results/report.html

# 查看 JSON 摘要
cat /tmp/rbg-stress-results/summary.json
```

### 步骤 5：清理环境
```bash
# 删除模拟节点和压测命名空间
bash test/stress/scripts/teardown-kwok.sh

# 同时卸载 KWOK
UNINSTALL_KWOK=true bash test/stress/scripts/teardown-kwok.sh
```

---

## 压测输出与结果分析
压测客户端在输出目录中生成以下文件：

### 输出文件
| 文件 | 说明 |
| --- | --- |
| `report.html` | **主报告** — 浏览器打开，包含延迟图表、错误分类、pprof 数据 |
| `summary.json` | 机器可读的性能统计（每个阶段的 P50/P90/P99/QPS） |
| `timing-create.csv` | Create 阶段的逐操作延迟数据 |
| `timing-update.csv` | Update 阶段的逐操作延迟数据 |
| `timing-delete.csv` | Delete 阶段的逐操作延迟数据 |
| `controller-full.log` | 压测期间的 Controller 完整日志 |
| `errors.log` | 过滤后的错误日志 |
| `cpu-{phase}.prof` | CPU pprof 二进制画像 |
| `heap-{phase}.prof` | 堆内存 pprof 二进制画像 |
| `allocs-{phase}.prof` | 内存分配 pprof 二进制画像 |
| `goroutine-{phase}.prof` | 协程 pprof 二进制画像 |
| `*-top.txt` | 人类可读的 pprof Top-N 文本报告 |


### 分析维度一：吞吐量与延迟
读取 `summary.json`，对比每个阶段的实际 QPS 与目标 QPS：

```plain
┌──────────────────────────────────────────────────────────────────┐
│  吞吐量差距分析                                                    │
│                                                                  │
│  Create 阶段:                                                     │
│  目标 QPS: 10        实际 QPS: 6.95      差距: -30.5%             │
│  判定: ⚠ 落后 — Controller 处理能力不足                            │
│                                                                  │
│  Update 阶段:                                                     │
│  目标 QPS: 10        实际 QPS: 10.0      差距: 0%                 │
│  判定: ✓ 达标                                                    │
└──────────────────────────────────────────────────────────────────┘
```

**判定规则**：实际 QPS < 目标 QPS 的 90%，说明 Controller 处理能力跟不上提交速率。

### 分析维度二：延迟分布模式
| 延迟模式 | 特征 | 含义 |
| --- | --- | --- |
| 均匀分布 | P50 ≈ P99 | 处理稳定，无瓶颈 |
| 尾部延迟 | P99 >> P50（>5x） | 队列堆积或 GC 暂停 |
| 队列饱和 | P99 > 10s | Reconcile 工作线程全部繁忙 |
| 异常离群 | Max >> P99 | API Server 抖动或 Leader 选举 |


### 分析维度三：延迟趋势
读取 `timing-*.csv` 中的操作时间戳和延迟，观察延迟是否随时间单调增长：

+ **延迟稳定**：Controller 处理能力充足，队列不堆积
+ **延迟线性增长**：提交速率超过处理能力，Reconcile 队列持续膨胀

### 分析维度四：Controller 日志
`errors.log` 中的错误分类和根因：

| 错误类型 | 日志特征 | 根因 | 建议 |
| --- | --- | --- | --- |
| API 限流 | `Throttling request took Xs` | `kube-api-qps` 过低 | 增大 `kube-api-qps` 和 `kube-api-burst` |
| 乐观锁冲突 | `the object has been modified` | 并发 Reconcile 竞争同一对象 | < 1% 正常；> 5% 需降低 `max-concurrent-reconciles` |
| 超时 | `context deadline exceeded` | 单次 Reconcile 耗时过长 | 增大超时时间或减少单次 Reconcile 工作量 |
| 工作负载类型 | `unsupported workload type` | 角色缺少 `role-workload-type` 注解 | 压测模板需包含正确注解 |
| Panic | `panic` / `runtime error` | Controller 内部错误 | 严重问题，需要排查代码 |


**错误率参考**：< 1% 可接受（瞬时冲突），> 5% 说明存在系统性问题。

### 分析维度五：Pprof 性能画像
如果启用了 pprof（`--pprof-addr`），可以通过 Top-N 报告分析性能瓶颈。

**CPU 画像**（`cpu-{phase}-top.txt`）：

| CPU 消耗类别 | 说明 | 调优方向 |
| --- | --- | --- |
| `crypto/tls`、`net/http`、`runtime.*` | 基础设施开销 | 正常，无需调优 |
| Reconciler 函数、依赖解析 | Controller 业务逻辑 | `max-concurrent-reconciles` 可能是瓶颈 |
| `encoding/json`、`Unmarshal`、`Marshal` | 对象序列化 | 对象流转量大，考虑减少不必要的 Update |


**堆内存画像**（`heap-{phase}-top.txt`）：

+ `runtime.allocm` → 协程栈内存，与 `max-concurrent-reconciles` 成正比
+ `Unmarshal` → Informer 缓存反序列化 Kubernetes 对象
+ 管理 < 1000 个 RBG 时，堆内存超过 1GB 需关注

**协程画像**（`goroutine-{phase}-top.txt`）：

| 协程数量 | 判定 |
| --- | --- |
| < 100 | 健康 |
| 100~500 | 高并发 Controller 的正常范围 |
| > 1000 | 可能存在协程泄漏，需要排查 |


---

## 调优决策树
根据压测结果，按照以下决策树定位瓶颈并调优：

```plain
┌──────────────────────────────────────────────────────────────────┐
│  压测结果分析 → 调优决策                                           │
│                                                                  │
│  实际 QPS < 目标 QPS？                                            │
│  ├── 是                                                          │
│  │   ├── 有 API 限流日志？ ──→ 增大 kube-api-qps / kube-api-burst │
│  │   ├── CPU 使用率高？ ──→ 增大 CPU 资源限制                      │
│  │   └── 都不是 ──→ 增大 max-concurrent-reconciles               │
│  └── 否 → Controller 处理能力达标，检查延迟分布                     │
│                                                                  │
│  P99 延迟过高？                                                   │
│  ├── P99 >> P50（尾部延迟） ──→ Reconcile 队列饱和，增加并发数     │
│  ├── Max >> P99（离群值） ──→ API Server 抖动，检查集群负载        │
│  └── 延迟线性增长 ──→ 提交速率超过处理能力，降低 QPS 或增加并发      │
│                                                                  │
│  冲突错误 > 5%？                                                  │
│  └── 是 ──→ 降低 max-concurrent-reconciles，减少并发竞争           │
│                                                                  │
│  内存持续增长？                                                    │
│  └── 检查 Informer 缓存是否无界，考虑限制 Watch 范围                │
└──────────────────────────────────────────────────────────────────┘
```

### 调优公式参考
| 场景 | 公式 | 说明 |
| --- | --- | --- |
| Reconcile 并发数不足 | `max-concurrent-reconciles = 总角色数 / 目标 P99 秒数` | 确保在目标延迟内处理完所有角色 |
| API QPS 限流 | `kube-api-qps >= max-concurrent-reconciles × 3` | 每个 Reconcile 约 3 次 API 调用 |
| API Burst 不足 | `kube-api-burst >= kube-api-qps × 2` | 允许突发流量通过 |


---

## 自动调优模式
使用 `/stress-test` Skill 的自动调优模式，Skill 会自动运行多轮压测，逐步逼近最优配置：

| 轮次 | 策略 | 目的 |
| --- | --- | --- |
| 第 1 轮 | 用户初始配置 | 建立基线 |
| 第 2 轮 | 增大 `kube-api-qps` / `kube-api-burst` | 测试 API 限流是否为瓶颈 |
| 第 3 轮 | 增大 `max-concurrent-reconciles` | 测试工作线程数是否为瓶颈 |
| 第 4 轮 | 综合前几轮的最优组合 | 验证最优配置 |


Skill 跨轮次比较以下指标，推荐最终配置：

+ 最低 P99 延迟
+ 无 OOM 或过度内存增长
+ 最少 API 限流错误
+ 最佳资源效率（每核 CPU 的延迟表现）

---

## 完整操作流程
以下是从零开始完成一次完整压测的操作步骤：

```plain
步骤 1: 启动压测（推荐方式）
    $ claude    # 启动 Claude Code
    > /stress-test
    # 按提示选择测试场景和参数

步骤 1': 或手动执行
    $ FAKE_NODE_COUNT=10 bash test/stress/scripts/setup-kwok.sh
    $ CONTROLLER_CPU=8 CONTROLLER_MEMORY=16Gi \
      MAX_RECONCILES=20 KUBE_API_QPS=100 KUBE_API_BURST=200 \
      PPROF_ENABLED=true \
      bash test/stress/scripts/deploy-controller.sh
    $ kubectl port-forward -n rbgs-system \
      deploy/rbgs-controller-manager 6060:6060 &
    $ go run ./test/stress/ \
      --total-rbgs=100 --roles-per-rbg=3 \
      --lws-roles=1 --lws-size=4 \
      --create-qps=5 --update-qps=5 --delete-qps=5 \
      --in-place-update=true \
      --pprof-addr=localhost:6060

步骤 2: 查看报告
    $ open /tmp/rbg-stress-results/report.html
    $ cat /tmp/rbg-stress-results/summary.json

步骤 3: 根据分析结果调整 Controller 配置
    $ CONTROLLER_CPU=16 CONTROLLER_MEMORY=32Gi \
      MAX_RECONCILES=50 KUBE_API_QPS=200 KUBE_API_BURST=400 \
      PPROF_ENABLED=true \
      bash test/stress/scripts/deploy-controller.sh

步骤 4: 重新压测验证
    $ go run ./test/stress/ ...

步骤 5: 清理环境
    $ bash test/stress/scripts/teardown-kwok.sh
```

### 各规模的压测参考配置
| 规模 | RBG 数 | 角色数/RBG | LWS 角色 | Create QPS | 推荐 Controller 配置 |
| --- | --- | --- | --- | --- | --- |
| 小型验证 | 10 | 2 | 0 | 5 | 4c/8g, Reconciles 10, QPS 50 |
| 中型基准 | 100 | 3 | 1 | 5 | 8c/16g, Reconciles 20, QPS 100 |
| 大型压力 | 500 | 3 | 1 | 10 | 16c/32g, Reconciles 50, QPS 200 |
| 超大规模 | 1000 | 5 | 2 | 10 | 32c/64g, Reconciles 100, QPS 500 |


## 相关文档
+ [使用 RBG 部署推理服务](#)
+ [配置滚动更新策略](#)
+ [原地升级与原地调度](#)
+ [为 RBG 服务配置弹性伸缩策略](#)


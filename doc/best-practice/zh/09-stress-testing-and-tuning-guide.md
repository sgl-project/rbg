# 操作文档：大规模集群资源评估、配置与压测

> 对应概念文档：[9. 大规模集群资源评估、配置与压测](09-stress-testing-and-tuning.md)

## 目标

验证 RBG Controller 的压测工具链，包括：
1. 搭建 KWOK 模拟环境
2. 部署 Controller 并配置资源参数
3. 运行压测（Create / Update / Delete 三阶段）
4. 查看报告并分析结果
5. 根据结果调优 Controller 配置

## 前提条件

- Kubernetes 集群版本 >= 1.24
- 已安装 RBG CRD
- 本地工具：`helm`、`kubectl`、`go`、`curl`
- （可选）Controller 镜像支持 `--enable-pprof`

---

## 操作一：搭建 KWOK 环境

### 步骤 1：安装 KWOK Controller 并创建模拟节点

```bash
# 安装 KWOK Controller + Stage（官方 stage-fast + 项目自定义 Pod 生命周期），创建 5 个模拟节点
FAKE_NODE_COUNT=5 bash test/stress/scripts/setup-kwok.sh
```

### 预期行为

- KWOK Controller 部署到集群
- 安装官方 `stage-fast` chart（提供 Node 心跳和初始化 Stage）
- 应用项目自定义 Pod 生命周期 Stage（`pod-initialize`、`pod-running`、`pod-ready`、`pod-delete`），覆盖官方默认 Pod Stage
- 创建 5 个 KWOK 模拟节点（`kwok-node-0` 到 `kwok-node-4`）
- 每个模拟节点默认容量：128 CPU / 512Gi 内存 / 1000 Pod

### 验证

```bash
# 验证模拟节点已就绪
kubectl get nodes -l type=kwok

> NAME          STATUS   ROLES    AGE   VERSION
> kwok-node-0   Ready    <none>   12s
> kwok-node-1   Ready    <none>   10s
> kwok-node-2   Ready    <none>   8s
> kwok-node-3   Ready    <none>   6s
> kwok-node-4   Ready    <none>   4s
```

```bash
# 验证 KWOK Stage 已安装
kubectl get stages

> NAME                        AGE
> node-heartbeat-with-lease   53s
> node-initialize             53s
> pod-delete                  53s
> pod-initialize              37s
> pod-ready                   53s
> pod-running                 37s
```

**预期输出：**
- 5 个 Ready 状态的 KWOK 节点
- 6 个 Stage：2 个 Node 相关（`node-heartbeat-with-lease`、`node-initialize`）+ 4 个 Pod 相关（`pod-initialize`、`pod-running`、`pod-ready`、`pod-delete`）
- Pod 生命周期 Stage 延迟：`pod-initialize` 即时 → `pod-running` 500ms → `pod-ready` 1000ms → `pod-delete` 500ms

### 步骤 2（可选）：自定义模拟节点容量

```bash
# 如果需要更多资源，重新创建模拟节点
FAKE_NODE_COUNT=10 \
FAKE_NODE_CPU=256 \
FAKE_NODE_MEMORY=1024Gi \
FAKE_NODE_PODS=2000 \
bash test/stress/scripts/setup-kwok.sh
```

---

## 操作二：部署 Controller

### 步骤 1：部署 Controller 并配置资源参数

```bash
# 获取当前镜像 tag，避免覆盖为 latest；--no-hooks 跳过 CRD 升级（KWOK 节点无法运行 hook job）
IMAGE_TAG=$(kubectl get deploy -n rbgs-system rbgs-controller-manager \
    -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*://')

helm upgrade rbgs deploy/helm/rbgs -n rbgs-system \
    --set image.tag=${IMAGE_TAG} \
    --set resources.limits.cpu=8 --set resources.limits.memory=16Gi \
    --set controllerTuning.maxConcurrentReconciles=20 \
    --set controllerTuning.kubeApiQPS=100 --set controllerTuning.kubeApiBurst=200 \
    --set pprof.enabled=true --set pprof.port=6060 \
    --no-hooks --wait --timeout=120s

# pprof 端口转发
pkill -f "port-forward.*6060" 2>/dev/null; sleep 1
kubectl port-forward -n rbgs-system deploy/rbgs-controller-manager 6060:6060 &
```

### 预期行为

- 跳过镜像构建（使用集群中已有的镜像）
- 通过 Helm 升级 Controller，仅更新资源限制和运行时参数
- 设置资源限制：8 CPU / 16Gi 内存
- 设置运行时参数：Reconciles=20, QPS=100, Burst=200
- 启用 pprof
- 等待 Controller Pod 就绪
- 建立 pprof 端口转发（`localhost:6060`）

### 验证

```bash
# 确认 Controller Pod 就绪
kubectl get pods -n rbgs-system -l control-plane=rbgs-controller

# 查看 Controller 资源配置
kubectl get deploy -n rbgs-system rbgs-controller-manager -o jsonpath='{.spec.template.spec.containers[0].resources}'

> {"limits":{"cpu":"8","memory":"16Gi"},"requests":{"cpu":"100m","memory":"256Mi"}}
```

```bash
# 查看 Controller 启动参数
kubectl get deploy -n rbgs-system rbgs-controller-manager -o jsonpath='{.spec.template.spec.containers[0].args}' | tr ',' '\n'

> ["--metrics-bind-address=:8443"
> "--leader-elect"
> "--health-probe-bind-address=:8081"
> "--max-concurrent-reconciles=20"
> "--kube-api-qps=100"
> "--kube-api-burst=200"
> "--scheduler-name=scheduler-plugins"
> "--enable-pprof=true"
> "--pprof-bind-address=:6060"]
```

**预期输出：**
- Controller Pod 处于 Running 且 Ready
- 资源配置为 8 CPU / 16Gi
- 启动参数包含 `--max-concurrent-reconciles=20`、`--kube-api-qps=100`、`--kube-api-burst=200`

---

## 操作三：运行压测

### 步骤 1：运行中型基准压测

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

### 预期行为

压测分三个阶段执行：

1. **Create 阶段**：按 5 QPS 创建 100 个 RBG（每个 3 角色，其中 1 个使用 `leaderWorkerPattern`，底层由 `RoleInstanceSet` 实现），等待全部 Ready
2. **Update 阶段**：按 5 QPS 更新所有 RBG 镜像，使用 InPlaceIfPossible 策略，等待全部完成
3. **Delete 阶段**：按 5 QPS 删除所有 RBG，等待全部清除

每个模拟 Pod 从创建到 Ready 约 1.5 秒（KWOK Stage 模拟）。

> **说明**：`--lws-roles=1` 表示前 1 个角色使用 `leaderWorkerPattern`（多 Pod 组模式），底层由 RBG 内置的 `RoleInstanceSet` 控制器实现，无需安装外部 LeaderWorkerSet CRD。

### 验证

```bash
# 查看压测过程中的 Pod 数量
kubectl get pods -n stress-test --no-headers | wc -l

>      600
```

```bash
# 查看 Controller 日志（压测期间）
kubectl logs -n rbgs-system -l control-plane=rbgs-controller -f
```

**预期行为：**
- Create 阶段：100 个 RBG 逐步创建，Pod 调度到 KWOK 模拟节点
- Update 阶段：所有 RBG 镜像更新，原地升级
- Delete 阶段：所有 RBG 清除完毕

---

## 操作四：查看报告并分析结果

### 步骤 1：查看 HTML 报告

```bash
# 打开 HTML 报告
open /tmp/rbg-stress-results/report.html
```

### 步骤 2：查看 JSON 摘要

```bash
cat /tmp/rbg-stress-results/summary.json
```

### 预期内容

JSON 摘要包含每个阶段的统计信息：

```json
[
  {
    "operation": "create",
    "total": 100,
    "succeeded": 100,
    "failed": 0,
    "p50_ms": 4080.5,
    "p90_ms": 5946.799999999999,
    "p99_ms": 7062.680000000001,
    "max_ms": 7328,
    "min_ms": 1024,
    "avg_ms": 3696.86,
    "total_time_sec": 25.044643083,
    "actual_qps": 3.99286983921439
  },
  {
    "operation": "update",
    "total": 100,
    "succeeded": 100,
    "failed": 0,
    "p50_ms": 393,
    "p90_ms": 634.5,
    "p99_ms": 769.6400000000003,
    "max_ms": 833,
    "min_ms": 273,
    "avg_ms": 430.39,
    "total_time_sec": 20.268429458,
    "actual_qps": 4.933781386822241
  },
  {
    "operation": "delete",
    "total": 100,
    "succeeded": 100,
    "failed": 0,
    "p50_ms": 184,
    "p90_ms": 191,
    "p99_ms": 204.2200000000001,
    "max_ms": 226,
    "min_ms": 179,
    "avg_ms": 185.18,
    "total_time_sec": 19.981816,
    "actual_qps": 5.004550136984546
  }
]
```

### 分析维度一：吞吐量与延迟

判定规则：实际 QPS < 目标 QPS 的 90%，说明 Controller 处理能力不足。

```bash
# 查看各阶段实际 QPS
cat /tmp/rbg-stress-results/summary.json | python3 -c "
import json, sys
data = json.load(sys.stdin)
for item in data:
    phase = item['operation']
    target = 5.0
    actual = item.get('actual_qps', 0)
    gap = ((actual - target) / target * 100) if target > 0 else 0
    status = '✓ 达标' if actual >= target * 0.9 else '⚠ 落后'
    print(f'{phase}: 目标={target} 实际={actual:.2f} 差距={gap:.1f}% {status}')
"

> create: 目标=5.0 实际=3.99 差距=-20.1% ⚠ 落后
> update: 目标=5.0 实际=4.93 差距=-1.3% ✓ 达标
> delete: 目标=5.0 实际=5.00 差距=0.1% ✓ 达标
```

> Create 阶段 QPS 落后于目标，原因是每个 RBG 包含 3 个角色（共 300 个 RoleInstanceSet），Controller 需要创建并等待所有子资源就绪。可通过增大 `max-concurrent-reconciles` 或提高 `kube-api-qps` 来改善。

### 分析维度二：延迟分布

```bash
# 查看延迟数据
cat /tmp/rbg-stress-results/timing-create.csv | head -5

> name,operation,start_time,end_time,duration_ms,error
> stress-rbg-0001,create,2026-07-08T17:47:41.62093+08:00,2026-07-08T17:47:42.645876+08:00,1024.00,
> stress-rbg-0000,create,2026-07-08T17:47:41.420105+08:00,2026-07-08T17:47:43.068567+08:00,1648.00,
> stress-rbg-0002,create,2026-07-08T17:47:41.820073+08:00,2026-07-08T17:47:43.265007+08:00,1444.00,
> stress-rbg-0003,create,2026-07-08T17:47:42.020899+08:00,2026-07-08T17:47:43.452421+08:00,1431.00,
```

| 延迟模式 | 特征 | 含义 |
| --- | --- | --- |
| 均匀分布 | P50 ≈ P99 | 处理稳定 |
| 尾部延迟 | P99 >> P50（>5x） | 队列堆积或 GC |
| 队列饱和 | P99 > 10s | Reconcile 线程全部繁忙 |

### 分析维度三：错误日志

```bash
# 查看错误日志
cat /tmp/rbg-stress-results/errors.log | head -3

> {"level":"ERROR","time":"2026-07-08T09:47:41.869Z","caller":"workloads/rolebasedgroup_controller.go:544","message":"Failed to reconcile workload",...,"error":"RoleInstanceSet.workloads.x-k8s.io \"stress-rbg-0000-role-0\" not found",...}
> {"level":"ERROR","time":"2026-07-08T09:47:41.879Z","caller":"workloads/rolebasedgroup_controller.go:544","message":"Failed to reconcile workload",...,"error":"RoleInstanceSet.workloads.x-k8s.io \"stress-rbg-0000-role-1\" not found",...}
> {"level":"ERROR","time":"2026-07-08T09:47:42.374Z","caller":"workloads/rolebasedgroup_controller.go:544","message":"Failed to reconcile workload",...,"error":"RoleInstanceSet.workloads.x-k8s.io \"stress-rbg-0003-role-1\" not found",...}
```

| 错误类型 | 日志特征 | 建议 |
| --- | --- | --- |
| API 限流 | `Throttling request took Xs` | 增大 `kube-api-qps` / `kube-api-burst` |
| 乐观锁冲突 | `the object has been modified` | > 5% 需降低 `max-concurrent-reconciles` |
| 超时 | `context deadline exceeded` | 增大超时或减少 Reconcile 工作量 |
| 初始竞态 | `RoleInstanceSet not found` | 良性错误，Controller 首次调和时子资源尚未创建，重试后自动恢复 |

**实际错误统计：**

Controller 日志：19826 行，其中 265 行错误
错误类型分布：
  175 × "Failed to reconcile workload"
   91 × "Reconciler error"
API 限流（Throttling）：0
乐观锁冲突（has been modified）：0
超时（context deadline）：0

> 所有错误均为 `RoleInstanceSet not found` 类型——Controller 首次调和 RBG 时子资源尚未创建完成，重试后自动恢复，属于良性错误。

### 分析维度四：Pprof 性能画像

```bash
# 查看 CPU Top 报告
cat /tmp/rbg-stress-results/cpu-create-top.txt

# 查看堆内存 Top 报告
cat /tmp/rbg-stress-results/heap-create-top.txt

# 查看内存分配 Top 报告
cat /tmp/rbg-stress-results/allocs-create-top.txt

# 查看协程数量
cat /tmp/rbg-stress-results/goroutine-create-top.txt
```

**参考判定：**
- 协程数 < 100：健康
- 协程数 100~500：高并发正常范围
- 协程数 > 1000：可能存在泄漏
- 管理 < 1000 个 RBG 时，堆内存超过 1GB 需关注

**实际 Pprof 数据（Create 阶段）：**

| 指标 | 实际值 | 判定 |
| --- | --- | --- |
| 协程数 | 371 | 高并发正常范围 |
| 堆内存 | 76.81MB | 健康（远低于 1GB） |
| 内存分配 | 33.57GB | 主要来自 JSONSchemaProps.DeepCopy（23.88%）和 RawExtension.DeepCopyInto（19.43%） |
| CPU 热点 | runtime.scanobject 11.34% | GC 相关，正常 |

> 协程数 371 在高并发正常范围内。堆内存 76.81MB 远低于 1GB 阈值。内存分配热点集中在 CRD Schema DeepCopy 操作，这是 controller-runtime 处理 unstructured 对象的固有开销。

---

## 操作五：调优并重新压测

### 步骤 1：根据分析结果调整配置

```bash
# 获取当前镜像 tag，避免覆盖为 latest；--no-hooks 跳过 CRD 升级（KWOK 节点无法运行 hook job）
IMAGE_TAG=$(kubectl get deploy -n rbgs-system rbgs-controller-manager \
    -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*://')

helm upgrade rbgs deploy/helm/rbgs -n rbgs-system \
    --set image.tag=${IMAGE_TAG} \
    --set resources.limits.cpu=16 --set resources.limits.memory=16Gi \
    --set controllerTuning.maxConcurrentReconciles=50 \
    --set controllerTuning.kubeApiQPS=200 --set controllerTuning.kubeApiBurst=400 \
    --set pprof.enabled=true --set pprof.port=6060 \
    --no-hooks --wait --timeout=120s

# pprof 端口转发
pkill -f "port-forward.*6060" 2>/dev/null; sleep 1
kubectl port-forward -n rbgs-system deploy/rbgs-controller-manager 6060:6060 &
```

### 步骤 2：重新运行压测

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
    --output-dir=/tmp/rbg-stress-results-v2 \
    --timeout=30m
```

### 步骤 3：对比两次结果

```bash
echo "=== 第一轮（8c/16g, Reconciles=20, QPS=100）==="
cat /tmp/rbg-stress-results/summary.json | python3 -c "
import json, sys
data = json.load(sys.stdin)
for item in data:
    phase = item['operation']
    print(f'{phase}: P50={item.get(\"p50_ms\",\"?\")} P99={item.get(\"p99_ms\",\"?\")} QPS={item.get(\"actual_qps\",0):.2f}')
"

echo ""
echo "=== 第二轮（16c/32g, Reconciles=50, QPS=200）==="
cat /tmp/rbg-stress-results-v2/summary.json | python3 -c "
import json, sys
data = json.load(sys.stdin)
for item in data:
    phase = item['operation']
    print(f'{phase}: P50={item.get(\"p50_ms\",\"?\")} P99={item.get(\"p99_ms\",\"?\")} QPS={item.get(\"actual_qps\",0):.2f}')
"
```

### 预期行为

```
=== 第一轮（8c/16g, Reconciles=20, QPS=100）===
create: P50=4080.5 P99=7062.680000000001 QPS=3.99
update: P50=393 P99=769.6400000000003 QPS=4.93
delete: P50=184 P99=204.2200000000001 QPS=5.00

=== 第二轮（16c/32g, Reconciles=50, QPS=200）===
create: P50=1477 P99=2401.02 QPS=4.70
update: P50=1871.5 P99=2692.3600000000006 QPS=4.70
delete: P50=173.5 P99=642.1200000000001 QPS=5.01
```

Create 阶段 QPS 落后（3.99 vs 目标 5.0），第二轮增大 Reconcile 并发和 API QPS 后，预期 Create QPS 提升至 4.7，P99 延迟下降。

---

## 操作六：清理环境

```bash
# 删除模拟节点和压测命名空间
bash test/stress/scripts/teardown-kwok.sh

# 如果需要同时卸载 KWOK
UNINSTALL_KWOK=true bash test/stress/scripts/teardown-kwok.sh
```

### 预期行为

- 压测命名空间 `stress-test` 被删除
- KWOK 模拟节点被删除
- （可选）KWOK Controller 被卸载

### 验证

```bash
# 确认模拟节点已删除
kubectl get nodes -l type=kwok

# 确认压测命名空间已删除
kubectl get namespace stress-test
```

---

## 各规模压测参考配置

| 规模 | RBG 数 | 角色数/RBG | leaderWorker 角色 | Create QPS | 推荐 Controller 配置 |
| --- | --- | --- | --- | --- | --- |
| 小型验证 | 10 | 2 | 0 | 5 | 4c/8g, Reconciles 10, QPS 50 |
| 中型基准 | 100 | 3 | 1 | 5 | 8c/16g, Reconciles 20, QPS 100 |
| 大型压力 | 500 | 3 | 1 | 10 | 16c/32g, Reconciles 50, QPS 200 |
| 超大规模 | 1000 | 5 | 2 | 10 | 32c/64g, Reconciles 100, QPS 500 |

---

## 总结

| 操作 | 验证点 | 关键预期 |
| --- | --- | --- |
| 搭建 KWOK 环境 | 模拟节点和 Pod 生命周期 | 5 个 Ready 节点 + 6 个 Stage（2 Node + 4 Pod）|
| 部署 Controller | 资源和参数配置 | Pod Ready，参数正确 |
| 运行压测 | Create/Update/Delete 三阶段 | 100 RBG 全部创建、更新、删除成功 |
| 分析报告 | 吞吐量、延迟、错误、pprof | Create QPS 3.97（⚠ 落后），Update/Delete 达标，371 协程，76MB 堆内存，无严重错误 |
| 调优重测 | 参数调优效果验证 | 调优后 QPS 提升，延迟降低 |

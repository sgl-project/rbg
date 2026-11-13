# Benchmark 指南

## 概述

部署完成后，可通过 `llmctl benchmark` 对推理服务进行压测，评估真实性能。

---

## 前置准备

### 创建 Benchmark 输出 PVC

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: benchmark-output
  namespace: <namespace>
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 10Gi
EOF
```

> **注意**：PVC 需支持 `ReadWriteMany`（如 NFS、OSS、CephFS 等），不支持 ReadWriteOnce（如本地 hostPath）。若集群无 RWX PVC，请告知用户需要先创建合适的存储资源。

---

## 参数采集

向用户询问以下 Benchmark 参数：

| 参数 | 说明 | 默认值 | 示例 |
|------|------|--------|------|
| `--model-tokenizer` | 模型 tokenizer（HF ID 或 PVC 路径） | 必填 | `Qwen/Qwen3-8B` |
| `--traffic-scenario` | 流量场景（可多个） | 推荐 `D(512,256)` | `D(100,1000)` `D(500,500)` |
| `--num-concurrency` | 并发数列表 | `1,2,4,8` | `1,4,8,16` |
| `--max-time-per-run` | 每次测试最大时间（分钟） | `15` | `10` |
| `--max-requests-per-run` | 每次最大请求数 | `100` | `500` |

**流量场景格式说明**：
- `D(input_tokens, output_tokens)`：固定输入输出 token 数
- 例如 `D(512, 256)` = 输入 512 tokens，输出 256 tokens

**常见测试组合**：
```
短输入短输出：D(128,128)
短输入长输出：D(128,1024)
长输入短输出：D(1024,128)
代码生成场景：D(512,512)
长文档摘要：D(2048,256)
```

---

## 运行 Benchmark

```bash
# 基础运行
llmctl benchmark run <rbg-name> \
  --model-tokenizer <model-id> \
  --experiment-base-dir pvc://benchmark-output/rbg-benchmark \
  --traffic-scenario "D(512,256)" \
  --num-concurrency 1,2,4,8

# 多场景运行
llmctl benchmark run <rbg-name> \
  --model-tokenizer <model-id> \
  --experiment-base-dir pvc://benchmark-output/rbg-benchmark \
  --traffic-scenario "D(128,128)" \
  --traffic-scenario "D(512,256)" \
  --traffic-scenario "D(1024,128)" \
  --num-concurrency 1,4,8,16 \
  --max-time-per-run 10

# 同步等待完成（实时输出日志）
llmctl benchmark run <rbg-name> \
  --model-tokenizer <model-id> \
  --experiment-base-dir pvc://benchmark-output/rbg-benchmark \
  --traffic-scenario "D(512,256)" \
  --num-concurrency 1,4,8 \
  --wait
```

### 使用 YAML 配置文件

```yaml
# benchmark-config.yaml
modelTokenizer: <model-id>
experimentBaseDir: pvc://benchmark-output/rbg-benchmark
trafficScenarios:
  - "D(128,128)"
  - "D(512,256)"
  - "D(1024,128)"
numConcurrency:
  - 1
  - 4
  - 8
  - 16
maxTimePerRun: 10
maxRequestsPerRun: 300
```

```bash
llmctl benchmark run <rbg-name> -f benchmark-config.yaml
```

---

## 查看结果

### 命令行查看

```bash
# 列出所有 Benchmark Job
llmctl benchmark list <rbg-name>

# 查看 Job 日志
llmctl benchmark logs <rbg-name> --job <job-name>

# 不 follow（查看历史日志）
llmctl benchmark logs <rbg-name> --job <job-name> -f=false

# 查看 Job 配置
llmctl benchmark get <rbg-name> --job <job-name>
```

### Web Dashboard

```bash
# 启动 Dashboard（自动 port-forward 并打开浏览器）
llmctl benchmark dashboard \
  --experiment-base-dir pvc://benchmark-output/rbg-benchmark

# 指定本地端口
llmctl benchmark dashboard \
  --experiment-base-dir pvc://benchmark-output/rbg-benchmark \
  --port 18888
```

Dashboard 提供：
- 各场景的 TTFT/TPOT/吞吐量 P50/P90/P99 指标
- 不同并发数下的性能曲线
- 与 SLO 目标的对比

---

## 结果解读

### 关键指标

| 指标 | 说明 | SLO 对应 |
|------|------|---------|
| TTFT (Time To First Token) | 首 token 延迟 | `ttftP99MaxMs` |
| TPOT (Time Per Output Token) | 每输出 token 延迟 | `tpotP99MaxMs` |
| Output Throughput | 输出 token/秒 | 吞吐量目标 |
| Error Rate | 错误率 | 通常要求 < 1% |

### 性能调优建议

**TTFT 过高**（超过目标 TTFT）：
- 考虑 PD 分离（降低 Prefill 阶段等待时间）
- 增加 Prefill 实例数
- 降低 `--max-running-requests` / `--max-num-seqs`（减少排队）

**TPOT 过高**（超过目标 TPOT）：
- 增加 Decode 实例数
- 调整 `--mem-fraction-static` 增加 KV Cache 空间
- 检查 GPU 显存是否充足（避免 KV Cache 溢出）

**吞吐量不足**：
- 增加 replicas
- 启用 ScalingAdapter + HPA 自动扩容
- 提高 `--max-running-requests` / `--max-num-seqs`

**Error Rate 过高**：
- 检查 OOM（增加显存或减小 batch size）
- 检查 context length 是否超限
- 查看 Pod 日志排查具体错误

---

## 清理

```bash
# 删除 Benchmark Job
llmctl benchmark delete <rbg-name> --job <job-name>
```

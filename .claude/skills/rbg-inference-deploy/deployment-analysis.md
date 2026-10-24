# 部署方案分析

## 引擎版本建议

通过引擎官方发布页判断用户镜像版本是否过旧。若用户提供的版本明显落后，输出建议但不阻断流程。

| 引擎 | 官方文档 | 推荐镜像获取方式 |
|------|----------|-----------------|
| SGLang | https://docs.sglang.ai / https://github.com/sgl-project/sglang/releases | `docker pull lmsysorg/sglang:latest` |
| vLLM | https://docs.vllm.ai / https://github.com/vllm-project/vllm/releases | `docker pull vllm/vllm-openai:latest` |

**建议升级场景**：
- 用户 tag 为 `latest` 但实际 digest 已超过 6 个月
- 用户 tag 版本号低于 GitHub releases 最新版 2 个以上 minor 版本
- 用户使用 `v0.x.x` 且当前已发布 `v0.(x+3)` 以上

若需检查版本，可访问：
- SGLang: `curl -s https://api.github.com/repos/sgl-project/sglang/releases/latest | jq .tag_name`
- vLLM: `curl -s https://api.github.com/repos/vllm-project/vllm/releases/latest | jq .tag_name`

---

## 显存估算公式

### 模型权重显存（粗估）

| 精度 | 每 10 亿参数显存 |
|------|-----------------|
| FP16 / BF16 | ~2 GB |
| INT8 | ~1 GB |
| INT4 / AWQ / GPTQ | ~0.5 GB |

**公式**：`model_vram_GB = params_B × per_param_GB`

### 总显存需求（含 KV Cache）

| 用途 | 估算 |
|------|------|
| 模型权重 | 见上表 |
| KV Cache | 约为模型权重的 20%~50%（取决于 batch size 和 context length） |
| 框架开销 | 约 1~2 GB |

**最低 GPU 数 = ceil(总显存需求 / 单卡显存 × 1.15)**（15% 安全冗余）

### 常见模型 + 常见 GPU 最少卡数参考

| 模型参数量 | GPU 型号 | 精度 | 最少卡数 |
|-----------|---------|------|---------|
| 7B | A100 80G | BF16 | 1 |
| 7B | L20 48G | BF16 | 1 |
| 7B | A10 24G | BF16 | 1（紧张） |
| 13B | A100 80G | BF16 | 1 |
| 14B | L20 48G | BF16 | 1（需开 kv cache 量化） |
| 32B | A100 80G | BF16 | 1（紧张）/ 2（推荐） |
| 32B | L20 48G | BF16 | 2 |
| 70B | A100 80G | BF16 | 2 |
| 70B | H100 80G | BF16 | 2 |
| 72B | A100 80G | BF16 | 2 |
| 235B (MoE) | H100 80G | BF16 | 4~8 |

> **注意**：以上为粗估，实际需结合 batch size、context length、框架 overhead 综合判断。

---

## SLO 参考数据

> 以下数据来自社区测试，仅供初步评估参考，实际结果因集群环境、负载分布差异较大。

### SGLang 参考（单实例，`--tp-size` 对应卡数）

| 模型 | GPU | tp-size | TTFT P99 参考 | TPOT P99 参考 | 并发 |
|------|-----|---------|--------------|--------------|------|
| Qwen3-7B | A100 80G | 1 | ~300ms | ~20ms | 32 |
| Qwen3-14B | A100 80G | 1 | ~500ms | ~30ms | 16 |
| Qwen3-32B | A100 80G | 2 | ~800ms | ~40ms | 8 |
| Qwen3-72B | A100 80G | 2 | ~1500ms | ~60ms | 4 |
| Llama-3-8B | A100 80G | 1 | ~250ms | ~15ms | 32 |

> **评估逻辑**：若用户 SLO 要求低于参考 TTFT 的 50%，建议增加实例数（replicas）而非张量并行度，或使用 PD 分离降低 Prefill 阶段延迟。

---

## Cookbook 参考

### SGLang 官方部署建议

参考：https://docs.sglang.ai/backend/server_arguments.html

**关键参数**：
- `--tp-size N`：张量并行度，等于实例内 GPU 数
- `--mem-fraction-static F`：静态显存比例（默认 0.88），控制 KV Cache 空间
- `--max-running-requests N`：最大并发请求数
- `--chunked-prefill-size N`：Chunked Prefill 大小（建议 4096~8192）
- `--disable-radix-cache`：关闭 Radix Cache（节省显存但降低命中率）

**PD 分离额外参数**：
- Prefill 角色：`--disaggregation-mode prefill`
- Decode 角色：`--disaggregation-mode decode`

**常见优化组合**：
```bash
# 高吞吐场景（牺牲延迟）
--mem-fraction-static 0.92 --max-running-requests 512

# 低延迟场景（牺牲吞吐）
--mem-fraction-static 0.80 --max-running-requests 32 --chunked-prefill-size 4096
```

### vLLM 官方部署建议

参考：https://docs.vllm.ai/en/stable/serving/engine_args.html

**关键参数**：
- `--tensor-parallel-size N`：张量并行度
- `--gpu-memory-utilization F`：GPU 显存利用率（默认 0.90）
- `--max-num-seqs N`：最大并发序列数
- `--max-model-len N`：最大上下文长度
- `--enforce-eager`：关闭 CUDA graph（节省显存）

**vLLM 启动命令**：
```bash
python3 -m vllm.entrypoints.openai.api_server \
  --model /models/Qwen3-8B \
  --tensor-parallel-size 2 \
  --gpu-memory-utilization 0.90 \
  --max-num-seqs 256 \
  --port 8000
```

---

## Auto-Benchmark 流程

> 需要 `LLMCTL_AVAILABLE=true` 且集群可用，且已有 RBG 部署（或先用 Cookbook 初始部署）

### 准备 Benchmark 输出 PVC

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: benchmark-output
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 10Gi
EOF
```

### 生成 Auto-Benchmark 配置

根据用户的 SLO 要求和模型信息，生成如下配置文件（`autobench-config.yaml`）：

```yaml
name: <rbg-name>-autobench

# 部署模板（引用用户生成的 RBG YAML 作为模板）
templates:
  - name: baseline
    template: ./rbg-deployment.yaml

backend: sglang  # 或 vllm

# 搜索空间（根据 GPU 数量和模型大小调整）
searchSpace:
  default:
    # 搜索最优的 max_num_seqs / max-running-requests
    maxNumSeqs:
      type: categorical
      values: [64, 128, 256, 512]
    # 若需要，搜索显存分配比例
    # gpuMemoryUtilization:
    #   type: categorical
    #   values: [0.85, 0.90, 0.95]

# 基准测试场景
scenario:
  name: <workload-name>
  workload: "normal(512,256/256,128)"  # 或根据用户业务调整
  concurrency: 8
  maxRequests: 500

# SLO 目标
objectives:
  sla:
    ttftP99MaxMs: <用户提供的 TTFT 要求>
    tpotP99MaxMs: <用户提供的 TPOT 要求>
    errorRateMax: 0.01
  optimize: outputThroughput  # 在满足 SLO 前提下最大化吞吐

# 搜索策略
strategy:
  algorithm: tpe   # Optuna TPE 算法，自动高效搜索
  maxTrialsPerTemplate: 10

evaluator:
  type: inference-perf
  config:
    tokenizerSource: <model-id>
    baseSeed: 42
    apiType: completion
    streaming: true

results:
  pvc: benchmark-output
  subPath: <rbg-name>-autobench
```

### 运行 Auto-Benchmark

```bash
# 运行 autobenchmark（异步，返回 job name）
llmctl autobenchmark run autobench-config.yaml

# 或等待完成
llmctl autobenchmark run autobench-config.yaml --wait

# 查看进度
llmctl autobenchmark list

# 查看日志
llmctl autobenchmark logs <job-name>
```

### 查看结果

```bash
# 启动 Dashboard 查看可视化结果
llmctl autobenchmark dashboard \
  --experiment-base-dir pvc://benchmark-output/<rbg-name>-autobench
```

### 解析最优参数

Auto-Benchmark 完成后，从结果中提取满足 SLO 的最优参数组合（在满足 TTFT/TPOT P99 约束下，outputThroughput 最高的参数组合），更新到 RBG YAML 的容器启动参数中。

---

## 部署架构决策树

```
用户有 GPU 预算 N 张
│
├─ N < 最少卡数 → 警告：建议换小模型或增加预算
│
├─ 模型 > 单卡显存
│   └─ 使用 leaderWorkerPattern，tp-size = ceil(model_vram / gpu_vram)
│
├─ 模型 ≤ 单卡显存
│   └─ 使用 standalonePattern
│
├─ 用户有 SLO 要求 AND N 足够分离
│   ├─ 对 TTFT 要求严格（<500ms P99）→ 考虑 PD 分离（Prefill 减少排队延迟）
│   └─ 对吞吐量要求高 → 多副本 + scalingAdapter + HPA
│
└─ 简单场景 → 聚合部署（单角色）
```

### PD 分离典型比例参考

| 场景 | Prefill 实例 | Decode 实例 | Prefill tp-size | Decode tp-size |
|------|-------------|------------|----------------|----------------|
| 低延迟优先 | 1 | 2~4 | = model min GPU | = model min GPU |
| 高吞吐优先 | 1 | 6~8 | 大（减少队列）| 小（增加并行） |
| 均衡 | 2 | 4 | 2 | 2 |

> **经验法则**：Decode 实例数通常是 Prefill 的 2~4 倍（Decode 阶段是内存带宽瓶颈，多副本效果好）。

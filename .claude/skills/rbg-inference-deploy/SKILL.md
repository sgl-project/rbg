---
name: rbg-inference-deploy
description: Interactive wizard for deploying LLM inference services on Kubernetes using RoleBasedGroup (RBG). Guides users through prerequisites checks (k8s cluster, RBG installation, llmctl CLI), deployment configuration (engine, model, GPU, SLO), deployment pattern analysis, RBG YAML generation, deployment, testing, and benchmarking. Use when users want to deploy LLM/AI inference services with RBG, need help creating RBG deployment YAML, or want to configure SGLang/vLLM inference on Kubernetes.
---

# RBG Inference Deployment Wizard / RBG 推理服务部署向导

## 🌐 Language / 语言

**Default language: English.**

Language switching rules:
- Detect user's language from each message
- If the user writes in **Chinese** (contains Chinese characters 一-鿿), switch to **Chinese** for ALL subsequent responses
- If the user writes in **English**, switch to **English** for ALL subsequent responses
- Apply consistently to: questions, tables, status messages, YAML comments, error messages, and all user-facing output
- You may explicitly say: `"Switching to Chinese / 切换为中文"` when language changes

---

## Overview / 概述

**EN**: An interactive wizard that guides you through end-to-end deployment of LLM inference services on Kubernetes via conversation. Executes in 5 sequential phases.

**ZH**: 一个交互式向导，帮助用户通过对话完成 LLM 推理服务在 Kubernetes 上的端到端部署。分为 5 个阶段依次执行。

**Before starting**: Read [prerequisites.md](prerequisites.md), [deployment-analysis.md](deployment-analysis.md), [yaml-rules.md](yaml-rules.md), [benchmark.md](benchmark.md) for full context.

---

## Phase 1: Prerequisites Check / 前置环境检查

> Details: see [prerequisites.md](prerequisites.md)

### 1.1 Check Kubernetes Cluster / 检查 Kubernetes 集群连通性

```bash
kubectl cluster-info 2>/dev/null
kubectl get nodes 2>/dev/null
```

**Result handling / 结果处理**:
- **Success / 成功**: proceed to 1.2
- **Failed / 失败**:
  - EN: "No reachable k8s cluster detected. Switching to Dry Run mode — YAML will be generated but not deployed."
  - ZH: "未检测到可用 k8s 集群，将以 Dry Run 模式生成 YAML，无法真实部署。"
  - Record `DRY_RUN=true`, skip 1.2, proceed to Phase 2

### 1.2 Check RBG Controller / 检查 RBG Controller 是否已安装

```bash
kubectl get deployment rbgs-controller-manager -n rbgs-system 2>/dev/null
kubectl get crd rolebasedgroups.workloads.x-k8s.io 2>/dev/null
```

**Result handling / 结果处理**:
- **Installed & Ready / 已安装且 Ready**: proceed to 1.3
- **Not installed / 未安装**: prompt user, ask whether to install now
  - **Yes / 是**: run install flow (see [prerequisites.md](prerequisites.md) § RBG Installation)
  - **No / 否**: record `DRY_RUN=true`, proceed to 1.3

### 1.3 Check llmctl / 检查 llmctl 是否已安装

```bash
which llmctl 2>/dev/null || llmctl --help 2>/dev/null
```

**Result handling / 结果处理**:
- **Installed / 已安装**: record `LLMCTL_AVAILABLE=true`, proceed to Phase 2
- **Not installed / 未安装**: show the prompt below (in detected language), ask whether to install:

  **EN prompt**:
  > `llmctl` is the RBG inference extension CLI offering:
  > - **Model management**: Auto-download from HuggingFace/ModelScope to PVC
  > - **Auto-Benchmark**: Automated parameter search + SLO evaluation to find optimal config (tp_size, max_num_seqs, etc.)
  > - **Benchmark**: One-click load testing with visual Dashboard
  > - **Service management**: One-click deploy/list/delete
  >
  > **Without it**: Model path must be provided manually; deployment params will be estimated via Cookbook instead of empirically tuned.

  **ZH prompt**:
  > `llmctl` 是 RBG 的推理服务扩展 CLI，提供以下能力：
  > - **模型管理**：从 HuggingFace/ModelScope 自动下载模型到 PVC
  > - **Auto-Benchmark**：自动化参数搜索 + SLO 评估，找到最优部署方案
  > - **Benchmark**：一键对已部署服务进行压测，可视化结果 Dashboard
  > - **服务管理**：一键 deploy/list/delete 推理服务
  >
  > **不安装的影响**：模型需用户自行提供 PVC 路径；部署参数将通过引擎 Cookbook 估算而非实测调优。

  - **Install / 选择安装**: run install (see [prerequisites.md](prerequisites.md) § llmctl installation), record `LLMCTL_AVAILABLE=true`
  - **Skip / 暂不安装**: record `LLMCTL_AVAILABLE=false`, proceed to Phase 2

---

## Phase 2: Deployment Requirements / 部署需求采集

> Ask one item at a time, confirm before proceeding / 逐步询问用户，每项确认后再进入下一项

### 2.1 Namespace

Ask for the target namespace (default: `default`). If cluster is reachable, check whether it exists; if not, ask whether to create it.

> ZH: 询问目标 namespace（默认 `default`）。若集群可用，检查是否存在，不存在则询问是否自动创建。

```bash
kubectl get namespace <ns> 2>/dev/null || kubectl create namespace <ns>
```

### 2.2 Inference Engine & Image / 推理引擎 & 镜像

Ask which inference engine to use / 询问使用哪种推理引擎：
- **SGLang**: default image `lmsysorg/sglang:latest`, see recommended versions in [deployment-analysis.md](deployment-analysis.md)
- **vLLM**: default image `vllm/vllm-openai:latest`, see recommended versions in [deployment-analysis.md](deployment-analysis.md)

If the provided image tag is outdated (below minimum recommended version), suggest upgrading but do not block. / 若用户提供的镜像 tag 明显过旧，输出升级建议但不强制。

### 2.3 Model / 模型

Ask how the model will be provided / 询问模型获取方式：

| Method / 方式 | Condition / 条件 | Action / 操作 |
|------|------|------|
| Existing PVC / 用户已有 PVC | Always / 始终可用 | Ask PVC name and model subpath / 询问 PVC 名称和模型子路径 |
| HuggingFace/ModelScope ID | `LLMCTL_AVAILABLE=true` | Download via `llmctl model pull <model-id>` |
| Manual model-path / 手动提供 | `LLMCTL_AVAILABLE=false` | Record model-path string (e.g. `Qwen/Qwen3-8B`, engine can download online) |

Record: `MODEL_ID`, `MODEL_PATH_ARG` (container path or HF ID), `MODEL_PVC` (optional)

### 2.4 Node Label, Taint & GPU Type / 节点 Label、Taint & GPU 型号

Ask for the target node NodeSelector label, then **auto-detect GPU node Taints** / 询问目标节点 NodeSelector label，然后**自动检测 GPU 节点的 Taints**：

```bash
# 检测节点 label、GPU 数量、Taint
kubectl get nodes -o json | jq -r '
  .items[] |
  select(.status.allocatable["nvidia.com/gpu"] != null) |
  [.metadata.name,
   (.status.allocatable["nvidia.com/gpu"]),
   (.spec.taints // [] | map(.key+"="+(.value//"")+":"+.effect) | join("|"))
  ] | @tsv'
```

Show node/GPU count/Taint to user for confirmation, and **record all Taints to `NODE_TAINTS`** for automatic toleration injection during YAML generation.

> ZH: 展示节点/GPU数/Taint，让用户确认，并**记录所有 Taint 到 `NODE_TAINTS` 列表**，后续 YAML 生成时自动写入 tolerations。

Ask for GPU type (for SLO estimation) / 询问 GPU 型号（用于 SLO 估算）。

### 2.5 Resource Budget & SLO / 资源预算 & SLO 要求

Ask / 询问：
- Max total GPU count / 最多使用 GPU 总数
- TTFT P99 requirement (ms, e.g. 2000) / TTFT P99 要求
- TPOT P99 requirement (ms, e.g. 50) / TPOT P99 要求
- Expected concurrent requests (optional) / 预期并发请求数（可选）

---

## Phase 3: Deployment Plan Analysis / 方案分析与推荐

> Details: see [deployment-analysis.md](deployment-analysis.md)

### 3.1 Memory Feasibility Estimation / 显存可行性估算

Estimate minimum GPU count based on model size + GPU type (see [deployment-analysis.md](deployment-analysis.md) § Memory estimation), compare with user budget:

- **Budget sufficient / 预算充足**: continue
- **Budget insufficient / 预算不足**: warn user, suggest smaller model or increase budget, ask whether to continue

### 3.2 SLO Feasibility Assessment / SLO 可行性评估

Estimate SLO achievability based on GPU type + model size + community Cookbook data (see [deployment-analysis.md](deployment-analysis.md) § SLO reference).

### 3.3 Deployment Plan Selection / 部署方案获取

Ask user to choose one of three options / 询问用户选择三种方式之一：

**Option A / 选项 A: User-provided / 用户自行提供**
- Ask whether to use PD disaggregation / 询问是否采用 PD 分离
- If yes: ask Prefill replicas/tp-size, Decode replicas/tp-size, whether Router is needed
- If no: ask backend replicas/tp-size
- Ask extra launch flags (e.g. `--mem-fraction-static`, `--max-running-requests`)

**Option B / 选项 B: llmctl Auto-Benchmark** (requires `LLMCTL_AVAILABLE=true` and cluster available)
- See [deployment-analysis.md](deployment-analysis.md) § Auto-Benchmark
- Run autobenchmark, wait, parse results

**Option C / 选项 C: Cookbook Estimation / Cookbook 估算** (default / 默认)
- See [deployment-analysis.md](deployment-analysis.md) § Cookbook reference
- Look up recommended config by engine/model/GPU type

### 3.4 Plan Confirmation / 方案确认

Display deployment plan summary in a table; user can adjust via conversation until confirmed / 以表格形式展示最终部署方案，用户可调整直到确认：

```
┌─────────────────────────────────────────────────────────────┐
│                      部署方案摘要                            │
```
┌─────────────────┬───────────────────────────────────────────────┐
│ Architecture    │ PD Disaggregated / Aggregated                 │
│ Engine          │ SGLang / vLLM                                  │
│ Image           │ lmsysorg/sglang:v0.4.6.post4                  │
│ Model           │ Qwen/Qwen3-8B                                  │
├─────────────────┼───────────────────────────────────────────────┤
│ Router          │ replicas=1, standalone, CPU-only               │
│ Prefill         │ replicas=2, tp-size=4 (8 GPU total)            │
│ Decode          │ replicas=4, tp-size=2 (8 GPU total)            │
├─────────────────┼───────────────────────────────────────────────┤
│ Total GPU       │ 16                                             │
│ Est. TTFT       │ ~800ms P99                                     │
│ Est. TPOT       │ ~40ms P99                                      │
└─────────────────┴───────────────────────────────────────────────┘
```

---

## Phase 4: RBG YAML Generation / RBG YAML 生成与配置

> YAML generation rules: see [yaml-rules.md](yaml-rules.md)

### 4.1 Generate Base YAML / 生成基础 YAML

Generate RBG YAML based on confirmed deployment plan, following these rules / 根据确认的部署方案生成 RBG YAML，遵循以下规则：

**roleTemplates conditions** (all must be met) / **roleTemplates 适用条件**（全部满足才使用）：
- Multiple roles share the same image **and** / 多角色共用相同镜像 **且**
- **No Taints on target nodes** (`NODE_TAINTS` is empty) / **目标节点无 Taint**（`NODE_TAINTS` 为空）
- If Taints exist, **must use direct `template`** (not `templateRef`) — tolerations are silently dropped by RBG controller when going through templateRef (verified in source code)
  / 若有 Taint，必须用直接 `template`，否则 tolerations 不会被 RBG controller 写入 Pod（已通过源码确认）

**Other rules / 其他规则**:
- leaderWorkerPattern: inject `$(RBG_LWP_LEADER_ADDRESS)` etc.
- PD disaggregated Router address: use auto-generated Headless Service DNS
- **Do NOT write `initContainers: []` in patch** (causes unintended override)

### 4.2 Configure Extra Features / 配置额外功能

Ask user whether to enable each optional feature (with explanation) / 依次询问是否启用以下功能（给出说明）：

**4.2.1 In-Place Update / 原地升级**（recommended / 推荐开启）
> EN: Image updates replace containers without recreating Pods, preserving GPU memory state.
> ZH: 镜像更新时原地替换容器而不重建 Pod，保留 GPU 显存状态，升级更快更稳定。

```yaml
rolloutStrategy:
  type: RollingUpdate
  rollingUpdate:
    type: InPlaceIfPossible
    maxUnavailable: 1
    inPlaceUpdateStrategy:
      gracePeriodSeconds: 30
```

**4.2.2 CoordinatedPolicy / 协调策略**
> EN: Cross-role coordinated rolling update to prevent Prefill/Decode upgrade skew from causing service instability. Requires a separate `CoordinatedPolicy` CR.
> ZH: 多角色联合控制升级进度，防止 Prefill/Decode 升级进度偏差过大导致服务抖动。需要创建独立的 `CoordinatedPolicy` CR。

**4.2.3 Gang Scheduling / Gang 调度**（Volcano）
> EN: Atomic scheduling for all Pods, prevents deadlock when partial GPUs are allocated but others can't be scheduled. Recommended for large TP sizes.
> ZH: 所有 Pod 原子性调度，适合大规模张量并行。
> Annotation: `rbg.workloads.x-k8s.io/scheduling-backend: volcano`

**4.2.4 ScalingAdapter / 弹性伸缩**
> EN: Automatically creates `RoleBasedGroupScalingAdapter` CR compatible with HPA for autoscaling.
> ZH: 自动创建弹性伸缩适配器，支持与 HPA 联动。
> Field: `scalingAdapter.enable: true`

**4.2.5 RestartPolicy** (leaderWorkerPattern only)
> `RecreateRoleInstanceOnPodRestart` (default): Recreate entire instance group on any Pod failure
> `None`: Do not handle Pod restart (for fault-tolerant frameworks)

### 4.3 YAML Preview & Confirm / YAML 预览与确认

Show user the complete final YAML (RBG + CoordinatedPolicy if any), allow adjustments via conversation until confirmed / 展示完整的最终 YAML，用户可通过对话调整直到确认。

---

## Phase 5: Deploy, Verify & Test / 部署、验证与测试

### 5.1 Deployment Decision / 部署决策

Ask user / 询问用户：
- **Deploy directly / 直接部署**: `kubectl apply -f <yaml>` → run 5.2
- **Save YAML / 保存 YAML**: ask for save path, write file and end

### 5.2 Execute Deployment / 执行部署

```bash
# Create namespace if needed / 若 namespace 需要创建
kubectl create namespace <ns> --dry-run=client -o yaml | kubectl apply -f -

# Deploy RBG / 部署 RBG
kubectl apply -f rbg-deployment.yaml -n <ns>
```

### 5.3 Wait for Ready + Smart Diagnosis / 等待 Ready + 智能诊断

Poll every 15s, show readyReplicas per role / 每 15 秒轮询一次，展示各角色 readyReplicas：
```bash
kubectl get rbg <name> -n <ns> -o wide
kubectl get pods -n <ns> -o wide
```

**If Pod stays Pending > 60s, run diagnosis immediately / 若 Pod 持续 Pending 超过 60 秒，立即执行诊断**：

```bash
# 获取 Pending Pod 的调度失败原因
kubectl describe pod <pending-pod> -n <ns> | grep -A 10 'Events:'
```

Map error keywords to actions (respond in user's language) / 根据关键词给出对应处理建议：

| Error keyword | Cause / 原因 | Suggestion / 建议 |
|-----------|------|------|
| `untolerated taint` | Pod missing taint toleration / Pod 没有对应污点容忍 | Check and fill tolerations in template.spec / 检查并补全 tolerations |
| `Insufficient nvidia.com/gpu` | GPU resources exhausted / GPU 资源不足 | Reduce replicas or wait / 减少副本数或等待资源 |
| `didn't match node affinity` | nodeSelector mismatch / nodeSelector 不匹配 | Verify node labels / 检查节点 label |
| `no nodes available` | No schedulable nodes / 无可调度节点 | Check node status and resources / 检查节点状态和资源 |

**If Pod is ImagePullBackOff / 若 Pod 为 ImagePullBackOff**:
```bash
kubectl describe pod <pod> -n <ns> | grep -A 5 'Failed\|Back-off'
```
Prompt user to verify image tag and registry network access / 提示用户检查镜像 tag 是否正确、网络是否可以访问镜像仓库。

**Wait for full readiness (timeout 10 min) / 等待完全就绪**（超时 10 分钟）：
```bash
kubectl wait rbg/<name> -n <ns> --for=condition=Ready --timeout=600s
```

### 5.4 Smoke Test / 冒烟测试

Port-forward and run a quick test / port-forward 并进行快速测试：

```bash
# For PD disaggregated: forward Router / 对于 PD 分离：转发 Router
kubectl port-forward svc/s-<rbg-name>-router -n <ns> 8000:8000

# Health check / 健康检查
curl http://localhost:8000/health

# Quick inference test / 快速推理测试
curl -X POST http://localhost:8000/v1/completions \
  -H 'Content-Type: application/json' \
  -d '{"model": "<MODEL_ID>", "prompt": "Hello", "max_tokens": 20}'
```

Report result to user in detected language / 向用户报告测试结果。

### 5.5 Benchmark (Optional) / 性能压测（可选）

Ask whether to run benchmark / 询问是否进行 Benchmark 测试：

- **Yes / 是**（`LLMCTL_AVAILABLE=true`）: see [benchmark.md](benchmark.md) for full benchmark workflow
- **No / 否**: End of wizard / 向导结束

Use `llmctl benchmark run` to generate a visual report / 执行 `llmctl benchmark run` 并生成可视化报告。

---

## Execution Notes / 执行注意事项

1. **Always confirm before executing / 始终确认再执行**: Show command to user before any cluster-modifying operation
2. **Preserve context / 保留上下文**: Record all confirmed options (`DRY_RUN`, `LLMCTL_AVAILABLE`, `NS`, `ENGINE`, `MODEL`, etc.), avoid re-asking
3. **Graceful degradation / 友好降级**: If a step fails, offer fallback (e.g. switch from autobenchmark to Cookbook estimation)
4. **Dry Run mode / Dry Run 模式**: When `DRY_RUN=true`, all kubectl commands use `--dry-run=client` or generate YAML only
5. **Language consistency / 语言一致性**: Respond entirely in detected language; never mix languages within a single response

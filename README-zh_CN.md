# RoleBasedGroup (RBG) 中文文档 🚀

[English](./README.md) | 简体中文

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](https://github.com/sgl-project/rbg/blob/main/LICENSE)
[![GitHub release](https://img.shields.io/github/release/sgl-project/rbg.svg)](https://github.com/sgl-project/rbg/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/sgl-project/rbg)](https://goreportcard.com/report/github.com/sgl-project/rbg)

> 🎯 一个 Kubernetes API，用于编排分布式、有状态的 AI 推理工作负载，支持**多角色协同**和**内置服务发现**。

**🌐 官方网站**: [rolebasedgroup.github.io](https://rolebasedgroup.github.io)

---

## 🏗️ 架构图

![RBG 架构图](doc/rbg-structure.png)

---

## 📰 最新动态

| 日期 | 版本 | 亮点 |
|:----:|:----:|:-----|
| 2026-06-11 | [v0.7.0](https://github.com/sgl-project/rbg/releases/tag/v0.7.0) | `v1alpha2` API 正式发布、转换 webhook、CLI 多节点 LLM 服务、Pod 端口分配器、协同策略、gang 调度 |
| 2026-02-18 | [v0.6.0](https://github.com/sgl-project/rbg/releases/tag/v0.6.0) | 协同伸缩、有状态 InstanceSet |
| 2025-12-03 | [v0.5.0](https://github.com/sgl-project/rbg/releases/tag/v0.5.0) | 原生 InstanceSet、原地更新、Mooncake 集成 |
| 2025-09-23 | [v0.4.0](https://github.com/sgl-project/rbg/releases/tag/v0.4.0) | RBGS 伸缩、Volcano podgroup 支持 |

---

## 🤔 为什么需要 RBG？

传统的 Kubernetes 原语（StatefulSet / Deployment）难以支持 LLM 推理服务：

| 挑战 | 描述 |
|:----:|:-----|
| 多角色拓扑 | gateway → router → prefill → decode |
| 性能敏感 | GPU/网络拓扑至关重要 |
| 原子操作 | 跨角色的部署、升级、扩缩容、故障恢复 |

**RBG** 将推理服务视为一个**基于角色的组**——具备拓扑结构、有状态、可协同的多角色有机体，作为整体单元管理。

---

## 🎯 核心概念

| 概念 | 描述 |
|:-----|:-----|
| **角色 (Role)** | 基础调度与发布单元。每个角色（prefill、decode）拥有独立的配置、生命周期和策略。 |
| **角色组 (RoleBasedGroup)** | 多个角色构成的一个逻辑服务（例如一次 LLM 推理部署）。 |
| **角色实例 (RoleInstance)** | Pod 集合，生命周期紧密绑定。支持原地更新，控制 Pod 组的升级与状态。 |
| **协同策略 (CoordinatedPolicy)** | 独立 CRD，用于跨角色协同操作。控制滚动更新与伸缩期间的 `maxSkew` 和 `progression`。 |

---

## ✨ 核心特性 — SCOPE

| 能力 | 描述 |
|:-----|:-----|
| **稳定 (Stable)** | 唯一 RoleID 注入，拓扑感知确定性运维 |
| **协同 (Coordination)** | 跨角色策略引擎：部署配对、协同升级、联动恢复 |
| **编排 (Orchestration)** | 角色依赖、精确启动顺序、拓扑自感知服务发现 |
| **性能 (Performance)** | 硬件亲和性调度：GPU-NVLink → PCIe → RDMA → VPC |
| **可扩展 (Extensible)** | 声明式 API 与插件机制，适配未来架构 |

---

## 🚀 快速开始

### 📦 安装

从 [GitHub Releases](https://github.com/sgl-project/rbg/releases) 安装（最新版本）：

```shell
VERSION=$(curl -sL https://api.github.com/repos/sgl-project/rbg/releases/latest | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')
helm upgrade --install rbgs https://github.com/sgl-project/rbg/releases/download/v$VERSION/rbgs-$VERSION.tgz \
            --namespace rbgs-system --create-namespace --wait
```

详细安装说明请参考 [安装指南](doc/install.md)。

### 🎮 快速示例

部署一个基础 RoleBasedGroup，包含两个角色和启动依赖：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: nginx-cluster
spec:
  roles:
    - name: frontend
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: nginx
                image: nginx:1.14.1
                ports:
                  - containerPort: 80

    - name: backend
      replicas: 3
      dependencies: ["frontend"]  # backend 在 frontend 就绪后启动
      standalonePattern:
        template:
          spec:
            containers:
              - name: nginx
                image: nginx:1.14.1
                ports:
                  - containerPort: 8080
```

### 部署模式

| 模式 | 用途 | 描述 |
|:-----|:-----|:-----|
| **standalonePattern** | 单机部署 | 单 pod 每实例 |
| **leaderWorkerPattern** | 多机分布式部署 | Leader + workers 用于张量并行 |

### RoleTemplates

使用可复用模板减少配置重复：

```yaml
spec:
  roleTemplates:
    - name: base-template
      template:
        spec:
          containers:
            - name: nginx
              image: nginx:1.14.1

  roles:
    - name: frontend
      replicas: 2
      standalonePattern:
        templateRef:
          name: base-template

    - name: backend
      replicas: 3
      standalonePattern:
        templateRef:
          name: base-template
          patch:  # 角色特定覆盖配置
            spec:
              containers:
                - name: nginx
                  resources:
                    requests:
                      memory: "128Mi"
```

---

## 🖥️ CLI 工具

kubectl-rbg 是管理 RBG 资源和 LLM 部署的 CLI 工具。

### 安装

```shell
# 从源码构建
make build-cli
chmod +x bin/kubectl-rbg
sudo mv bin/kubectl-rbg /usr/local/bin/
```

---

## 🧠 推理示例

### Prefill/Decode 解耦部署

`examples/inference/` 目录下的 SGLang PD 解耦示例：

| 示例 | 模式 | 说明 |
|:-----|:-----|:-----|
| [pd-disagg-standalone.yaml](examples/inference/pd-disagg-standalone.yaml) | standalonePattern | 单 pod 每角色，适用于单 GPU 实例 |
| [pd-disagg-leader-worker.yaml](examples/inference/pd-disagg-leader-worker.yaml) | leaderWorkerPattern | decode 角色 Multi-GPU 张量并行 |

### 聚合推理

SGLang 聚合推理示例：

| 示例 | 模式 | 说明 |
|:-----|:-----|:-----|
| [agg-standalone.yaml](examples/inference/agg-standalone.yaml) | standalonePattern | 单 GPU 聚合推理 |
| [agg-leader-worker.yaml](examples/inference/agg-leader-worker.yaml) | leaderWorkerPattern | 多 GPU 张量并行 |

---

## 🔗 生态集成

RBG 集成生态组件用于生产级 LLM 推理：

### NVIDIA Dynamo

[NVIDIA Dynamo](https://github.com/ai-dynamo/dynamo) 是开源的数据中心级推理栈，作为推理引擎（如 vLLM、SGLang）之上的编排层，协调多节点 AI 工作负载：

| 示例 | 说明 |
|:-----|:-----|
| [dynamo/pd-disagg.yaml](examples/inference/ecosystem/dynamo/pd-disagg.yaml) | Dynamo SGLang 运行时 PD 解耦部署 |
| [dynamo/pd-disagg-multi-nodes.yaml](examples/inference/ecosystem/dynamo/pd-disagg-multi-nodes.yaml) | 多节点 PD 解耦部署 |
| [dynamo/agg.yaml](examples/inference/ecosystem/dynamo/agg.yaml) | Dynamo 聚合推理 |
| [dynamo/agg-multi-nodes.yaml](examples/inference/ecosystem/dynamo/agg-multi-nodes.yaml) | 多节点聚合推理 |

### Mooncake

[Mooncake](https://github.com/kvcache-ai/Mooncake) 采用解耦架构用于 LLM 推理服务，为分布式推理提供 KV 缓存传输与复用：

| 示例 | 说明 |
|:-----|:-----|
| [mooncake-store/pd-disagg-kvcache-reuse-with-mooncake.yaml](examples/inference/ecosystem/mooncake/mooncake-store/pd-disagg-kvcache-reuse-with-mooncake.yaml) | PD 解耦 KV 缓存复用 |
| [mooncake-store/agg-kvcache-reuse-with-mooncake.yaml](examples/inference/ecosystem/mooncake/mooncake-store/agg-kvcache-reuse-with-mooncake.yaml) | 聚合推理 KV 缓存复用 |
| [mooncake-transfer-engine/sgl-pd-disagg-with-mooncake-te.yaml](examples/inference/ecosystem/mooncake/mooncake-transfer-engine/sgl-pd-disagg-with-mooncake-te.yaml) | SGLang PD 解耦传输引擎 |
| [mooncake-transfer-engine/vllm-pd-disagg-with-mooncake-te.yaml](examples/inference/ecosystem/mooncake/mooncake-transfer-engine/vllm-pd-disagg-with-mooncake-te.yaml) | vLLM PD 解耦传输引擎 |

---

## 📂 示例目录

### 🧱 基础示例 (`examples/basic/`)

| 路径 | 说明 |
|:-----|:-----|
| `rbg/base.yaml` | 基础 RoleBasedGroup 配置，演示角色依赖 |
| `rbg/dependency/` | 角色依赖配置示例 |
| `rbg/patterns/` | 部署模式：standalone、leader-worker、custom-components |
| `rbg/scheduling/` | Gang 调度：Volcano、scheduler-plugins |
| `rbg/update-strategy/` | 滚动更新，支持分区控制 |
| `rbg/restart-policy/` | 重启策略配置 |
| `rbg/scaling/` | 伸缩适配器，支持 HPA 集成 |
| `rbg/role-template/` | RoleTemplates 模板复用配置 |
| `coordinated-policy/` | 协同滚动更新与伸缩策略 |
| `engine-runtime/` | 引擎运行时配置 |

### 🧠 推理示例 (`examples/inference/`)

| 路径 | 说明 |
|:-----|:-----|
| `agg-standalone.yaml` | 聚合 SGLang 部署（standalone 模式） |
| `agg-leader-worker.yaml` | 聚合部署（leader-worker 模式） |
| `pd-disagg-standalone.yaml` | Prefill/Decode 解耦（standalone） |
| `pd-disagg-leader-worker.yaml` | Prefill/Decode 解耦（leader-worker） |
| `ecosystem/` | NATS、etcd、Dynamo、Mooncake 集成 |
| `ecosystem/dynamo/` | NVIDIA Dynamo 运行时示例 |
| `ecosystem/mooncake/` | Mooncake KV cache 传输引擎 |

---

## 📚 文档

| 来源 | 链接 |
|:-----|:-----|
| **官方文档** | [rolebasedgroup.github.io](https://rolebasedgroup.github.io) |
| **本地文档** | [doc/TOC.md](doc/TOC.md) |

### 版本兼容性

| RBG 版本 | Kubernetes | LeaderWorkerSet |
|:---------|:----------:|:---------------:|
| v0.7.0 | >=v1.22.x | 不依赖 |
| v0.6.0 | >=v1.28.x | >=v0.7.0 |
| v0.5.0 | >=v1.28.x | >=v0.6.0 |
| v0.4.0 | >=v1.28.x | >=v0.7.0 |

---

## 🌐 生态项目

[rolebasedgroup](https://github.com/rolebasedgroup) GitHub 组织托管了一系列配套项目，为 RBG 提供自动伸缩、CLI 工具、AI Agent 集成和文档支持：

| 项目 | 说明 |
|:-----|:-----|
| [**rbg-planner**](https://github.com/rolebasedgroup/rbg-planner) | 引擎无关的 SLA 驱动 LLM 推理自动伸缩器。通过可插拔指标适配器支持 SGLang、vLLM、NVIDIA Dynamo，基于 ARIMA 负载预测和自动 SLA 画像，扩缩 prefill/decode 角色以满足 TTFT/ITL 延迟目标。 |
| [**inference-engine-runtime**](https://github.com/rolebasedgroup/inference-engine-runtime) | Python 推理引擎 Sidecar 运行时。为 SGLang 和 vLLM 引擎提供 LoRA 适配器管理、统一 Prometheus 指标及分布式拓扑管理。 |
| [**inference-ext-cli**](https://github.com/rolebasedgroup/inference-ext-cli) | RBG CLI 扩展（`llmctl`），用于 LLM 推理工作负载管理。提供服务/模型管理、基准测试编排、自动参数搜索（Optuna）、收敛分析及实验可视化 Web 面板。 |
| [**rbg-agent-guide**](https://github.com/rolebasedgroup/rbg-agent-guide) | RBG 运维 AI Agent 技能指南。为 AI 编程助手（如 Claude Code）提供部署技能，帮助用户通过 RBG CRD 和 CLI 将 LLM 模型部署到 Kubernetes。 |
| [**rolebasedgroup.github.io**](https://github.com/rolebasedgroup/rolebasedgroup.github.io) | RBG 官方文档网站，基于 Docusaurus 构建，部署于 [rolebasedgroup.github.io](https://rolebasedgroup.github.io)。 |

---

## 🤝 参与贡献

欢迎通过 Issue 和 PR 参与贡献！详见 [贡献指南](CONTRIBUTING.md)。

```shell
# 校验版权头
make copyright-check

# 自动补全版权头
make copyright-fix
```

---

## 💬 社区

| 渠道 | 链接 |
|:-----|:-----|
| **Slack** | [#rbg 频道](https://sgl-fru7574.slack.com/archives/C098X0LQZV5) |
| **Issues** | [GitHub Issues](https://github.com/sgl-project/rbg/issues) |
| **Discussions** | [社区讨论](https://github.com/sgl-project/rbg/discussions) |

### 📜 行为准则

本项目遵循 [Kubernetes 行为准则](doc/code-of-conduct.md)。

---

## 🙏 致谢

RBG 受 [LeaderWorkerSet (LWS)](https://github.com/kubernetes-sigs/lws) 启发并复用了部分代码。
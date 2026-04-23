# RoleBasedGroup (RBG) 中文文档 🚀

[English](./README.md) | 简体中文

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](https://github.com/sgl-project/rbg/blob/main/LICENSE)
[![GitHub release](https://img.shields.io/github/release/sgl-project/rbg.svg)](https://github.com/sgl-project/rbg/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/sgl-project/rbg)](https://goreportcard.com/report/github.com/sgl-project/rbg)

> 🎯 一个 Kubernetes API，用于编排分布式、有状态的 AI 推理工作负载，支持**多角色协同**和**内置服务发现**。

**🌐 官方网站**: [rolebasedgroup.github.io](https://rolebasedgroup.github.io)

---

## 📰 最新动态

| 日期 | 版本 | 亮点 |
|:----:|:----:|:-----|
| 2026-04-22 | [v0.7.0-alpha.3](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.3) | `v1alpha2` 转换 webhook、CLI 多节点 LLM 服务 |
| 2026-03-31 | [v0.7.0-alpha.2](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.2) | Pod 端口分配器、CLI 基础功能 |
| 2026-03-18 | [v0.7.0-alpha.1](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.1) | `v1alpha2` API、协同策略、gang 调度 |
| 2026-02-18 | [v0.6.0](https://github.com/sgl-project/rbg/releases/tag/v0.6.0) | 协同伸缩、有状态 InstanceSet |
| 2025-12-03 | [v0.5.0](https://github.com/sgl-project/rbg/releases/tag/v0.5.0) | 原生 InstanceSet、原地更新、Mooncake 集成 |
| 2025-09-23 | [v0.4.0](https://github.com/sgl-project/rbg/releases/tag/v0.4.0) | RBGS 伸缩、Volcano podgroup 支持 |

---

## 🤔 为什么需要 RBG？

传统的 Kubernetes 原语（StatefulSet / Deployment）难以支持 LLM 推理服务：

| 挑战 | 描述 |
|:----:|:-----|
| 🧩 **多角色拓扑** | gateway → router → prefill → decode |
| ⚡ **性能敏感** | GPU/网络拓扑至关重要 |
| 🔗 **原子操作** | 跨角色的部署、升级、扩缩容、故障恢复 |

**RBG** 将推理服务视为一个**基于角色的组**——具备拓扑结构、有状态、可协同的多角色有机体，作为整体单元管理。

---

## 🎯 核心概念

| 概念 | 描述 |
|:-----|:-----|
| 角色 (Role) | 基础调度与发布单元。每个角色（prefill、decode）拥有独立的配置、生命周期和策略。 |
| 角色组 (RoleBasedGroup) | 多个角色构成的一个逻辑服务（例如一次 LLM 推理部署）。 |

---

## ✨ 核心特性 — SCOPE

RBG 提供五大核心能力：

| 能力 | 描述 |
|:-----|:-----|
| 🔄 稳定 (Stable) | 唯一 RoleID 注入，拓扑感知确定性运维 |
| 🤝 协同 (Coordination) | 跨角色策略引擎：部署配对、协同升级、联动恢复 |
| 🧭 编排 (Orchestration) | 角色依赖、精确启动顺序、拓扑自感知服务发现 |
| ⚡ 性能 (Performance) | 硬件亲和性调度：GPU-NVLink → PCIe → RDMA → VPC |
| 🧩 可扩展 (Extensible) | 声明式 API 与插件机制，适配未来架构 |

---

## 🏗️ 架构图

![RBG 架构图](doc/rbgs-concept.png)

---

## 🚀 快速开始

### 📦 安装

```shell
helm install rbg-controller oci://registry-1.docker.io/sglproject/rbg-controller-chart --version v0.7.0-alpha.3
```

📖 详细安装说明请参考 [安装指南](doc/install.md)。

### 🎮 快速示例

部署 **Prefill/Decode 解耦** LLM 推理服务，展示混合部署模式：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-disagg-lws
spec:
  roles:
    # Router: SGLang Model Gateway
    - name: router
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: router
                image: lmsysorg/sglang-router:v0.2.4
                command:
                  - python3
                  - -m
                  - sglang_router.launch_router
                  - --pd-disaggregation
                  - --prefill
                  - "http://pd-disagg-lws-prefill-0.s-pd-disagg-lws-prefill:8000"
                  - --decode
                  - "http://pd-disagg-lws-decode-0.s-pd-disagg-lws-decode:8000"
                ports:
                  - containerPort: 8000

    # Prefill: standalone 模式（单 GPU 每实例）
    - name: prefill
      replicas: 2
      standalonePattern:
        template:
          spec:
            containers:
              - name: sglang
                image: lmsysorg/sglang:v0.5.9
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "Qwen/Qwen3-0.6B"
                  - --disaggregation-mode
                  - "prefill"
                ports:
                  - containerPort: 8000
                resources:
                  limits:
                    nvidia.com/gpu: "1"

    # Decode: leader-worker 模式用于张量并行
    - name: decode
      replicas: 4
      leaderWorkerPattern:
        size: 2  # 1 leader + 1 worker
        template:
          spec:
            containers:
              - name: sglang
                image: lmsysorg/sglang:v0.5.9
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "Qwen/Qwen3-0.6B"
                  - --disaggregation-mode
                  - "decode"
                  - --tp-size
                  - "2"
                  - --dist-init-addr
                  - $(RBG_LWP_LEADER_ADDRESS):6379
                  - --nnodes
                  - $(RBG_LWP_GROUP_SIZE)
                  - --node-rank
                  - $(RBG_LWP_WORKER_INDEX)
                ports:
                  - containerPort: 8000
                resources:
                  limits:
                    nvidia.com/gpu: "1"
```

| 模式 | 用途 | 描述 |
|:-----|:-----|:-----|
| **StandalonePattern** | router, prefill | 单 pod 每实例 |
| **LeaderWorkerPattern** | decode | 多 GPU 张量并行 |

---

## 📂 示例说明

### 🧱 基础示例 (`examples/basic/`)

| 类别 | 说明 |
|:-----|:-----|
| **rbg/base.yaml** | 基础 RoleBasedGroup 配置，演示角色依赖 |
| **rbg/dependency/** | 角色依赖配置示例 |
| **rbg/patterns/** | 部署模式：standalone、leader-worker、custom-components |
| **rbg/scheduling/** | Gang 调度：Volcano、scheduler-plugins |
| **rbg/update-strategy/** | 滚动更新，支持分区控制 |
| **rbg/restart-policy/** | 重启策略配置 |
| **rbg/scaling/** | 伸缩适配器，支持 HPA 集成 |
| **coordinated-policy/** | 协同滚动更新与伸缩策略 |
| **engine-runtime/** | 引擎运行时配置 |

### 🧠 推理示例 (`examples/inference/`)

| 示例 | 说明 |
|:-----|:-----|
| **agg-standalone.yaml** | 聚合 SGLang 部署（standalone 模式） |
| **agg-leader-worker.yaml** | 聚合部署（leader-worker 模式） |
| **pd-disagg-standalone.yaml** | Prefill/Decode 解耦（standalone） |
| **pd-disagg-leader-worker.yaml** | Prefill/Decode 解耦（leader-worker） |
| **ecosystem/** | NATS、etcd、Dynamo、Mooncake 集成 |

---

## 📚 文档

| 来源 | 链接 |
|:-----|:-----|
| 🌐 **官方文档** | [rolebasedgroup.github.io](https://rolebasedgroup.github.io) |
| 📁 **本地文档** | [doc/TOC.md](doc/TOC.md) |

### 📋 版本兼容性

| RBG 版本 | Kubernetes | LeaderWorkerSet |
|:---------|:----------:|:---------------:|
| main / v0.7.0-alpha.x | >=v1.22.x | 不依赖 |
| v0.6.0 | >=v1.28.x | >=v0.7.0 |
| v0.5.0 | >=v1.28.x | >=v0.6.0 |
| v0.4.0 | >=v1.28.x | >=v0.7.0 |

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
| 💬 **Slack** | [#rbg 频道](https://sgl-fru7574.slack.com/archives/C098X0LQZV5) |
| 🐛 **Issues** | [GitHub Issues](https://github.com/sgl-project/rbg/issues) |
| 🗨️ **Discussions** | [社区讨论](https://github.com/sgl-project/rbg/discussions) |

### 📜 行为准则

本项目遵循 [Kubernetes 行为准则](doc/code-of-conduct.md)。

---

## 🙏 致谢

RBG 受 [LeaderWorkerSet (LWS)](https://github.com/kubernetes-sigs/lws) 启发并复用了部分代码 🎉
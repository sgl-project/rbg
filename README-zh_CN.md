# RoleBasedGroup (RBG) 中文文档

[English](./README.md) | 简体中文

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](https://github.com/sgl-project/rbg/blob/main/LICENSE)
[![GitHub release](https://img.shields.io/github/release/sgl-project/rbg.svg)](https://github.com/sgl-project/rbg/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/sgl-project/rbg)](https://goreportcard.com/report/github.com/sgl-project/rbg)

**RoleBasedGroup (RBG)** 是一个 Kubernetes API，用于编排分布式、有状态的 AI 推理工作负载，支持**多角色协同**和**内置服务发现**。它为生产环境中的大语言模型推理（尤其是**解耦架构**，如 Prefill/Decode 分离场景）提供了通用的部署范式。

**官方网站**: [rolebasedgroup.github.io](https://rolebasedgroup.github.io)

## 最新动态

- **[2026-04-22]** 发布 RBG v0.7.0-alpha.3！新增 `v1alpha2` 转换 webhook、CLI 多节点 LLM 服务等功能。详见 [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.3)。
- **[2026-03-31]** 发布 RBG v0.7.0-alpha.2，新增 pod 端口分配器、CLI 基础功能等。详见 [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.2)。
- **[2026-03-18]** 发布 RBG v0.7.0-alpha.1，引入 `v1alpha2` API，支持协同策略和 gang 调度。详见 [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.1)。
- **[2026-02-18]** 发布 RBG v0.6.0，提供协同伸缩支持、有状态 InstanceSet 等功能。详见 [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.6.0)。
- **[2025-12-03]** 发布 RBG v0.5.0，提供原生 InstanceSet 工作负载、协同滚动更新等功能。详见 [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.5.0)。
- **[2025-09-23]** 发布 RBG v0.4.0，引入 RBGS 伸缩能力和 Volcano podgroup 支持。详见 [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.4.0)。

## 概述

传统的 Kubernetes 原语（如原生 StatefulSet / Deployment）难以很好地支持具有以下特点的大语言模型推理服务：

- 以**多角色拓扑**（如网关/路由/Prefill/Decode）的形式运行
- 对 **GPU/网络拓扑结构**性能敏感
- 需要**跨角色的原子操作**（部署、升级、扩缩容和故障恢复）

**RBG** 将推理服务视为一个**基于角色的组**，而非一组松散的工作负载。它将服务建模为一个具备拓扑结构、有状态、可协同的**多角色有机体**，并将其作为一个整体单元进行管理。

## 核心概念

- **角色 (Role)** - 基础调度与发布单元。每个角色（如 prefill、decode）拥有独立的配置、生命周期和策略。
- **角色组 (RoleBasedGroup)** - 由多个角色共同构成的一个逻辑服务（例如一次 LLM 推理部署）。

## 核心特性

基于将"角色"视为调度编排原子单位的理念，RBG 提供了 **SCOPE 五大核心能力**：

| 能力 | 描述 |
|------|------|
| **稳定 (Stable)** | 基于唯一 RoleID 注入与最小替换域原则，实现拓扑感知的确定性运维 |
| **协同 (Coordination)** | 跨角色策略引擎，支持部署配对、协同升级、联动恢复与协调伸缩 |
| **编排 (Orchestration)** | 定义角色依赖与精确启动顺序；拓扑自感知服务发现无需依赖外部组件 |
| **性能 (Performance)** | 拓扑感知调度，支持硬件亲和性（GPU-NVLink > PCIe > RDMA > VPC）与角色亲和性调度 |
| **可扩展 (Extensible)** | 基于声明式 API 与插件机制，实现面向未来的部署抽象 |

## 架构图

![RBG 架构图](doc/rbgs-concept.png)

## 快速开始

### 安装

通过 Helm 安装 RBG：

```shell
helm install rbg-controller oci://registry-1.docker.io/sglproject/rbg-controller-chart --version v0.7.0-alpha.3
```

详细安装说明请参考 [安装指南](doc/install.md)。

### 快速示例

部署一个 Prefill/Decode 解耦的 LLM 推理服务，展示混合部署模式：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-disagg-lws
spec:
  roles:
    # Router: SGLang Model Gateway 用于 PD 解耦路由
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

    # Prefill: standalone 模式（每个实例使用单 GPU）
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

    # Decode: leader-worker 模式用于张量并行（1 leader + 1 worker）
    - name: decode
      replicas: 4
      leaderWorkerPattern:
        size: 2
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

此示例展示了：
- **StandalonePattern** 用于 router 和 prefill（单 pod 每实例）
- **LeaderWorkerPattern** 用于 decode（多 GPU 张量并行）
- 内置服务发现，自动生成 DNS 名称

## 示例说明

RBG 提供了覆盖各种部署场景的完整示例：

### 基础示例 (`examples/basic/`)

| 类别 | 说明 |
|------|------|
| **rbg/base.yaml** | 基础 RoleBasedGroup 配置，演示角色依赖 |
| **rbg/dependency/** | 角色依赖配置示例 |
| **rbg/patterns/** | 部署模式：standalone、leader-worker、custom-components |
| **rbg/scheduling/** | 调度策略：独占拓扑、gang 调度（Volcano、scheduler-plugins） |
| **rbg/update-strategy/** | 滚动更新策略，支持分区控制 |
| **rbg/restart-policy/** | 重启策略配置 |
| **rbg/scaling/** | 伸缩适配器，支持 HPA 集成 |
| **coordinated-policy/** | 协同滚动更新与伸缩策略 |
| **engine-runtime/** | 引擎运行时配置 |

### 推理示例 (`examples/inference/`)

真实 LLM 推理部署示例：

| 示例 | 说明 |
|------|------|
| **agg-standalone.yaml** | 使用 standalone 模式的聚合 SGLang 部署 |
| **agg-leader-worker.yaml** | 使用 leader-worker 模式的聚合部署 |
| **pd-disagg-standalone.yaml** | Prefill/Decode 解耦部署（standalone 模式） |
| **pd-disagg-leader-worker.yaml** | Prefill/Decode 解耦部署（leader-worker 模式） |
| **ecosystem/** | 支撑基础设施：NATS、etcd、Dynamo 集成、Mooncake 集成 |

更多详情请查看 [examples 目录](examples/)。

## 文档

完整文档可通过以下渠道获取：

- **官方文档**: [rolebasedgroup.github.io](https://rolebasedgroup.github.io)
- **本地文档**: [doc/TOC.md](doc/TOC.md)

### 版本兼容性

| RBG 版本 | Kubernetes 版本 | LeaderWorkerSet 版本 |
|:--------:|:---------------:|:--------------------:|
| main / v0.7.0-alpha.x | >=v1.28.x | >=v0.7.0 |
| v0.6.0 | >=v1.28.x | >=v0.7.0 |
| v0.5.0 | >=v1.28.x | >=v0.6.0 |
| v0.4.0 | >=v1.28.x | >=v0.7.0 |

## 参与贡献

欢迎通过 Issue 和 PR 参与贡献！详见 [贡献指南](CONTRIBUTING.md)。

Go 代码修改请运行 `make copyright-check` 校验版权头，或 `make copyright-fix` 自动补全。

## 社区

加入社区并与维护者交流：

- **Slack**: [#rbg 频道](https://sgl-fru7574.slack.com/archives/C098X0LQZV5)
- **GitHub Issues**: [提交 Bug 或功能请求](https://github.com/sgl-project/rbg/issues)
- **Discussions**: [社区讨论](https://github.com/sgl-project/rbg/discussions)

### 行为准则

本项目遵循 [Kubernetes 行为准则](doc/code-of-conduct.md)。

## 致谢

RBG 受 [LeaderWorkerSet (LWS)](https://github.com/kubernetes-sigs/lws) 启发并复用了部分代码。
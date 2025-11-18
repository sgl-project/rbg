# RoleBasedGroup API 中文文档

[English](./README.md)｜简体中文

**RoleBasedGroup**：用于编排多角色协作分布式工作负载服务的 API，专注于解决 AI/ML
推理工作负载的常见部署模式。特别适用于 Prefill/Decode 分离场景（如 prefill,
decode 和 scheduler 等角色），支持大语言模型（LLM）跨多节点设备的分布式运行。

## 最新消息 🔥

[2025-07-21] 发布 RBG v0.3.0 版本，
发布内容请参考[release notes](https://github.com/sgl-project/rbg/releases)。

## 概述

传统 Kubernetes 有状态集合（StatefulSet）在分布式有状态服务场景下面临多角色协调难题。本方案重点解决：

- 角色间启动顺序依赖
- 跨角色服务发现复杂
- 配置管理碎片化

### 核心特性

- **多角色模板定义** - 将分布式有状态工作负载建模为统一 K8s 工作负载组
- **基于角色的启动控制** - 为 RoleBasedGroup 中的 ReplicatedJobs 建立角色依赖关系和启动序列
- **自动服务发现** - 通过配置文件和环境变量注入拓扑细节
- **弹性伸缩** - 支持工作组/角色级伸缩操作
- **原子化滚动更新** - 角色级更新：以角色为单元顺序升级（同一角色内所有 Pod 同步更新）
- **拓扑感知调度** - 保障工作组/角色内 Pod 在同一拓扑域共置
- **原子化故障恢复** - 同一工作组/角色内任意 Pod/容器故障时触发全角色重建
- **可定制工作负载** - 支持多种工作负载类型（如 StatefulSet、Deployment 等）

## 架构图

![rbgs-concept](doc/rbgs-concept.png)

## 快速开始

- [安装 RBG Controller](doc/install.md)
- [快速开始](doc/quick_start.md)

## Documentation

如果需要详细了解 RBG 的特性及使用示例，请参考[文档](doc/TOC.md).

## 参与贡献

欢迎通过提交 Issue 和 PR 参与贡献！详见[贡献指南](CONTRIBUTING.md)

### Community, discussion, contribution, and support

访问 [Kubernetes 社区页面](https://kubernetes.io/community/)了解参与方式。

项目维护者联系方式：

- [Slack](https://sgl-fru7574.slack.com/archives/C098X0LQZV5)

### 行为准则

参与 Kubernetes 社区需遵守 [Kubernetes 行为准则](doc/code-of-conduct.md)。

## Acknowledgment

我们在设计和实现时参考了这些优秀的开源项目：[lws](https://github.com/kubernetes-sigs/lws)

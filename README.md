# The RoleBasedGroup API

English｜[简体中文](./README-zh_CN.md)

**RoleBasedGroup (RBG)** is a Kubernetes API for orchestrating distributed, stateful AI inference workloads with **multi‑role collaboration** and **built‑in service discovery**.  
It provides a common deployment pattern for production LLM inference, especially **disaggregated architectures** such as prefill/decode separation.


## Latest News 🔥

**[2025-09-23]** RBG v0.4.0 is released. Please check out
the [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.4.0) for more details.

**[2025-07-21]** RBG v0.3.0 is released. Please check out
the [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.3.0) for more details.

## Overview

Traditional Kubernetes primitives (e.g. plain StatefulSets / Deployments) are ill‑suited for LLM inference services that:

- run as **multi‑role topologies** (gateway / router / prefill / decode),
- are **performance‑sensitive** to GPU / network topology,
- and require **atomic, cross‑role operations** (deploy, upgrade, scale, failover).

**RBG** treats an inference service as a **role‑based group**, not a loose set of workloads. It models the service as a **topologized, stateful, coordinated multi‑role organism** and manages it as a single unit.

## Key Concepts

- **Role**  
  The basic scheduling and rollout unit. Each role (e.g. prefill, decode) has its own spec, lifecycle and policies.

- **RoleBasedGroup**  
  A group of roles that together form one logical service (e.g. one LLM inference deployment).


### Key Features

RBG treats "Role" as the atomic unit for scheduling orchestration, while establishing configurable relationships between different roles. It views a single inference service as a topological, stateful, and collaborative "Role Organism," rather than an isolated collection of Deployments.

Based on this philosophy, RBG has built the five core capabilities of **SCOPE**:

#### 🔁 **Stable**
- Topology-aware deterministic operations with unique RoleID injection and minimal replacement domain principles.

#### 🤝 **Coordination**
- Cross-role policy engine supporting deployment pairing, coordinated upgrades, linked recovery, and coordinated scaling.

#### 🧭 **Orchestration**
- Defines role dependencies and precise startup sequences within a RoleBasedGroup.  
- Topology self-aware service discovery - injects complete role topology into Pods, eliminating external service dependencies.

#### ⚡ **Performance**
Topology-aware placement with hardware affinity (GPU-NVLink > PCIe > RDMA > VPC) and role affinity scheduling.

#### 🧩 **Extensible**
Future-proof deployment abstraction using declarative APIs and plugin mechanisms to adapt new architectures in weeks.

## Architecture

![rbgs-concept](doc/rbgs-concept.png)

## Getting Started

- [Install RBG Controller](doc/install.md)
- [Quick Start](doc/quick_start.md)

## Documentation

You can see our documentation at [docs](doc/TOC.md) for more in-depth installation and instructions for production.

### Version Compatibility

| RBG Version | Kubernetes Version | LeaderWorkerSet Version |
|:-----------:|:------------------:|:-----------------------:|
|    main     |     >=v1.28.x      |        >=v0.7.0         |
|   v0.4.0    |     >=v1.28.x      |        >=v0.7.0         |
|   v0.3.0    |     >=v1.28.x      |        >=v0.6.0         |

## Contributing

We welcome contributions through issues and PRs! See [CONTRIBUTING.md](CONTRIBUTING.md).

### Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](https://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack](https://sgl-fru7574.slack.com/archives/C098X0LQZV5)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](doc/code-of-conduct.md).

## Acknowledgment

We learned the design and reused code from the following projects: [lws](https://github.com/kubernetes-sigs/lws)

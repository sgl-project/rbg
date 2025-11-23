# The RoleBasedGroup API

English｜[简体中文](./README-zh_CN.md)

**RoleBasedGroup(RBG)**: An API for orchestrating distributed AI inference workloads with multi-role collaboration and automated service discovery. It provides production-ready deployment patterns for AI Inferences, especially like disaggregated architectures like prefill and decode.


## Latest News 🔥

**[2025-07-21]** RBG v0.3.0 is released. Please check out
the [release notes](https://github.com/sgl-project/rbg/releases) for more details.

## Overview

RoleBasedGroup(RBG) coordinates multiple roles in distributed, stateful services, addressing key challenges in modern AI inference:

- **Rapid Architecture Evolution** - Adapts quickly to new disaggregated model architectures. 
- **Performance Sensitivity** - Millisecond optimization for TTFT/TPOT with GPU topology awareness. 
- **Strong Component Dependencies** - Atomic upgrades and 1:1 role binding (e.g., Prefill-Decode). 
- **Operational Efficiency** - Reduces GPU idle time from frequent restarts and scaling. 
- **Resource Fluctuations** - Handles 10x traffic variations, improving GPU utilization beyond 30%. 


### Key Features

RBG treats "Role" as the atomic unit for scheduling orchestration, while establishing configurable relationships between different roles. It views a single inference service as a topological, stateful, and collaborative "Role Organism," rather than an isolated collection of Deployments.

Based on this philosophy, RBG has built the five core capabilities of **SCOPE**:

#### 🔁 **Stable**
Topology-aware deterministic operations with unique RoleID injection and minimal replacement domain principles.

#### 🤝 **Coordination**
Cross-role policy engine supporting deployment pairing, two-phase commit upgrades, failure联动, and coordinated scaling.

#### 🧭 **Orchestration**
Topology self-aware service discovery - injects complete role topology into Pods, eliminating external service dependencies.

#### ⚡ **Performance**
Topology-aware placement with hardware affinity (GPU-NVLink > PCIe > RDMA) and role affinity scheduling.

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

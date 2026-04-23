# RoleBasedGroup (RBG)

English | [简体中文](./README-zh_CN.md)

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](https://github.com/sgl-project/rbg/blob/main/LICENSE)
[![GitHub release](https://img.shields.io/github/release/sgl-project/rbg.svg)](https://github.com/sgl-project/rbg/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/sgl-project/rbg)](https://goreportcard.com/report/github.com/sgl-project/rbg)

**RoleBasedGroup (RBG)** is a Kubernetes API for orchestrating distributed, stateful AI inference workloads with **multi-role collaboration** and **built-in service discovery**. It provides a common deployment pattern for production LLM inference, especially **disaggregated architectures** such as prefill/decode separation.

**Official Website**: [rolebasedgroup.github.io](https://rolebasedgroup.github.io)

## Latest News

- **[2026-04-22]** RBG v0.7.0-alpha.3 is released! Features include `v1alpha2` conversion webhooks, CLI multi-node LLM serving, and scaling adapter improvements. See [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.3).
- **[2026-03-31]** RBG v0.7.0-alpha.2 is released with pod port allocator, CLI foundations, and scheduler annotation inheritance. See [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.2).
- **[2026-03-18]** RBG v0.7.0-alpha.1 introduces `v1alpha2` API with coordinated policies and gang scheduling support. See [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.1).
- **[2026-02-18]** RBG v0.6.0 delivers coordinated scaling support, stateful InstanceSet, and KEP-8 RoleTemplate implementation. See [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.6.0).
- **[2025-12-03]** RBG v0.5.0 provides native InstanceSet workloads, coordinated rollouts, in-place updates, and Mooncake engine integration. See [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.5.0).
- **[2025-09-23]** RBG v0.4.0 introduces RBGS scaling capabilities and Volcano podgroup support. See [release notes](https://github.com/sgl-project/rbg/releases/tag/v0.4.0).

## Overview

Traditional Kubernetes primitives (e.g. plain StatefulSets / Deployments) are ill-suited for LLM inference services that:

- Run as **multi-role topologies** (gateway / router / prefill / decode)
- Are **performance-sensitive** to GPU / network topology
- Require **atomic, cross-role operations** (deploy, upgrade, scale, failover)

**RBG** treats an inference service as a **role-based group**, not a loose set of workloads. It models the service as a **topologized, stateful, coordinated multi-role organism** and manages it as a single unit.

## Key Concepts

- **Role** - The basic scheduling and rollout unit. Each role (e.g. prefill, decode) has its own spec, lifecycle and policies.
- **RoleBasedGroup** - A group of roles that together form one logical service (e.g. one LLM inference deployment).

## Key Features

Based on the philosophy of treating "Role" as the atomic unit for scheduling orchestration, RBG provides the five core capabilities of **SCOPE**:

| Capability | Description |
|------------|-------------|
| **Stable** | Topology-aware deterministic operations with unique RoleID injection and minimal replacement domain principles |
| **Coordination** | Cross-role policy engine supporting deployment pairing, coordinated upgrades, linked recovery, and coordinated scaling |
| **Orchestration** | Defines role dependencies and precise startup sequences; topology self-aware service discovery eliminates external dependencies |
| **Performance** | Topology-aware placement with hardware affinity (GPU-NVLink > PCIe > RDMA > VPC) and role affinity scheduling |
| **Extensible** | Future-proof deployment abstraction using declarative APIs and plugin mechanisms |

## Architecture

![RBG Architecture](doc/rbgs-concept.png)

## Getting Started

### Installation

RBG can be installed via Helm:

```shell
helm install rbg-controller oci://registry-1.docker.io/sglproject/rbg-controller-chart --version v0.7.0-alpha.3
```

For detailed installation instructions, see [Installation Guide](doc/install.md).

### Quick Start

Deploy a Prefill/Decode disaggregated LLM inference service with mixed deployment patterns:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-disagg-lws
spec:
  roles:
    # Router: SGLang Model Gateway for PD disaggregation
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

    # Prefill: standalone pattern (single GPU per instance)
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

    # Decode: leader-worker pattern for tensor parallel (1 leader + 1 worker)
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

This example demonstrates:
- **StandalonePattern** for router and prefill (single pod per instance)
- **LeaderWorkerPattern** for decode (multi-GPU tensor parallelism)
- Built-in service discovery with auto-generated DNS names

## Examples

RBG provides comprehensive examples for various deployment scenarios:

### Basic Examples (`examples/basic/`)

| Category | Description |
|----------|-------------|
| **rbg/base.yaml** | Basic RoleBasedGroup with role dependencies |
| **rbg/dependency/** | Role dependency configurations |
| **rbg/patterns/** | Deployment patterns: standalone, leader-worker, custom-components |
| **rbg/scheduling/** | Scheduling strategies: exclusive topology, gang scheduling (Volcano, scheduler-plugins) |
| **rbg/update-strategy/** | Rolling update strategies with partition support |
| **rbg/restart-policy/** | Restart policy configurations |
| **rbg/scaling/** | Scaling adapter with HPA integration |
| **coordinated-policy/** | Coordinated rolling update and scaling policies |
| **engine-runtime/** | Engine runtime profile configurations |

### Inference Examples (`examples/inference/`)

Real-world LLM inference deployment examples:

| Example | Description |
|---------|-------------|
| **agg-standalone.yaml** | Aggregated SGLang deployment with standalone pattern |
| **agg-leader-worker.yaml** | Aggregated deployment with leader-worker pattern |
| **pd-disagg-standalone.yaml** | Prefill/Decode disaggregated deployment (standalone) |
| **pd-disagg-leader-worker.yaml** | Prefill/Decode disaggregated deployment (leader-worker) |
| **ecosystem/** | Supporting infrastructure: NATS, etcd, Dynamo integration, Mooncake integration |

For more details, explore the [examples directory](examples/).

## Documentation

Comprehensive documentation is available at:

- **Official Docs**: [rolebasedgroup.github.io](https://rolebasedgroup.github.io)
- **Local Docs**: [doc/TOC.md](doc/TOC.md)

### Version Compatibility

| RBG Version | Kubernetes Version | LeaderWorkerSet Version |
|:-----------:|:------------------:|:-----------------------:|
| main / v0.7.0-alpha.x | >=v1.28.x | >=v0.7.0 |
| v0.6.0 | >=v1.28.x | >=v0.7.0 |
| v0.5.0 | >=v1.28.x | >=v0.6.0 |
| v0.4.0 | >=v1.28.x | >=v0.7.0 |

## Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

For Go code changes, run `make copyright-check` to verify headers or `make copyright-fix` to add missing ones.

## Community

Join the community and engage with maintainers:

- **Slack**: [#rbg channel](https://sgl-fru7574.slack.com/archives/C098X0LQZV5)
- **GitHub Issues**: [Report bugs or request features](https://github.com/sgl-project/rbg/issues)
- **Discussions**: [Community discussions](https://github.com/sgl-project/rbg/discussions)

### Code of Conduct

This project follows the [Kubernetes Code of Conduct](doc/code-of-conduct.md).

## Acknowledgment

RBG is inspired by and reuses code from [LeaderWorkerSet (LWS)](https://github.com/kubernetes-sigs/lws).
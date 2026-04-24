# RoleBasedGroup (RBG) 🚀

English | [简体中文](./README-zh_CN.md)

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](https://github.com/sgl-project/rbg/blob/main/LICENSE)
[![GitHub release](https://img.shields.io/github/release/sgl-project/rbg.svg)](https://github.com/sgl-project/rbg/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/sgl-project/rbg)](https://goreportcard.com/report/github.com/sgl-project/rbg)

> 🎯 A Kubernetes API for orchestrating distributed, stateful AI inference workloads with **multi-role collaboration** and **built-in service discovery**.

**🌐 Official Website**: [rolebasedgroup.github.io](https://rolebasedgroup.github.io)

---

## 📰 Latest News

| Date | Release | Highlights |
|:----:|:-------:|:-----------|
| 2026-04-22 | [v0.7.0-alpha.3](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.3) | `v1alpha2` conversion webhooks, CLI multi-node LLM serving |
| 2026-03-31 | [v0.7.0-alpha.2](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.2) | Pod port allocator, CLI foundations |
| 2026-03-18 | [v0.7.0-alpha.1](https://github.com/sgl-project/rbg/releases/tag/v0.7.0-alpha.1) | `v1alpha2` API, coordinated policies, gang scheduling |
| 2026-02-18 | [v0.6.0](https://github.com/sgl-project/rbg/releases/tag/v0.6.0) | Coordinated scaling, stateful InstanceSet |
| 2025-12-03 | [v0.5.0](https://github.com/sgl-project/rbg/releases/tag/v0.5.0) | Native InstanceSet, in-place updates, Mooncake integration |
| 2025-09-23 | [v0.4.0](https://github.com/sgl-project/rbg/releases/tag/v0.4.0) | RBGS scaling, Volcano podgroup support |

---

## 🤔 Why RBG?

Traditional Kubernetes primitives (StatefulSets / Deployments) struggle with LLM inference services that:

| Challenge | Description |
|:---------:|:------------|
| Multi-role topologies | gateway → router → prefill → decode |
| Performance-sensitive | GPU/network topology matters |
| Atomic operations | deploy, upgrade, scale, failover across roles |

**RBG** treats an inference service as a **role-based group** — a topologized, stateful, coordinated multi-role organism managed as a single unit.

---

## 🎯 Key Concepts

| Concept | Description |
|:--------|:------------|
| **Role** | Basic scheduling and rollout unit. Each role (prefill, decode) has its own spec, lifecycle and policies. |
| **RoleBasedGroup** | A group of roles forming one logical service (e.g., one LLM inference deployment). |

---

## ✨ Key Features — SCOPE

| Capability | Description |
|:-----------|:------------|
| **Stable** | Topology-aware deterministic operations with unique RoleID injection |
| **Coordination** | Cross-role policy engine: deployment pairing, coordinated upgrades, linked recovery |
| **Orchestration** | Role dependencies, precise startup sequences, topology self-aware service discovery |
| **Performance** | Hardware affinity scheduling: GPU-NVLink → PCIe → RDMA → VPC |
| **Extensible** | Declarative APIs and plugin mechanisms for future architectures |

---

## 🏗️ Architecture

![RBG Architecture](doc/rbg-structure.png)

---

## 🚀 Getting Started

### 📦 Installation

```shell
helm install rbg-controller oci://registry-1.docker.io/sglproject/rbg-controller-chart --version v0.7.0-alpha.3
```

For detailed instructions, see [Installation Guide](doc/install.md).

### 🎮 Quick Start

Deploy a basic RoleBasedGroup with two roles and startup dependencies:

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
      dependencies: ["frontend"]  # backend starts after frontend is ready
      standalonePattern:
        template:
          spec:
            containers:
              - name: nginx
                image: nginx:1.14.1
                ports:
                  - containerPort: 8080
```

### Deployment Patterns

| Pattern | Used For | Description |
|:--------|:---------|:------------|
| **standalonePattern** | router, prefill, single-GPU | Single pod per instance |
| **leaderWorkerPattern** | decode, multi-GPU TP | Leader + workers for tensor parallelism |

### RoleTemplates

Reduce configuration duplication with reusable templates:

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
          patch:  # role-specific overrides
            spec:
              containers:
                - name: nginx
                  resources:
                    requests:
                      memory: "128Mi"
```

---

## 🧠 Inference Examples

### Prefill/Decode Disaggregated

Deploy PD-disaggregated LLM inference with SGLang:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: sglang-pd-inference
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
                  - "http://sglang-pd-inference-prefill-0.s-sglang-pd-inference-prefill:8000"
                  - --decode
                  - "http://sglang-pd-inference-decode-0.s-sglang-pd-inference-decode:8000"

    # Prefill: prompt encoding engine
    - name: prefill
      replicas: 1
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: InPlaceIfPossible
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
                resources:
                  limits:
                    nvidia.com/gpu: "1"

    # Decode: token generation engine
    - name: decode
      replicas: 1
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
                  - "decode"
                resources:
                  limits:
                    nvidia.com/gpu: "1"
```

### NVIDIA Dynamo Runtime

Deploy with NVIDIA Dynamo SGLang runtime:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: dynamo-pd-inference
spec:
  roleTemplates:
    - name: dynamo-base
      template:
        spec:
          containers:
            - name: sglang
              image: nvcr.io/nvidia/ai-dynamo/sglang-runtime:1.0.1
              env:
                - name: DYN_DISCOVERY_BACKEND
                  value: kubernetes

  roles:
    - name: processor
      replicas: 1
      standalonePattern:
        templateRef:
          name: dynamo-base
          patch:
            spec:
              containers:
                - name: sglang
                  command:
                    - python3
                    - -m
                    - dynamo.frontend

    - name: prefill
      replicas: 1
      scalingAdapter:
        enable: true
      standalonePattern:
        templateRef:
          name: dynamo-base
          patch:
            spec:
              containers:
                - name: sglang
                  command:
                    - python3
                    - -m
                    - dynamo.sglang
                  args:
                    - --disaggregation-mode
                    - prefill

    - name: decode
      replicas: 1
      standalonePattern:
        templateRef:
          name: dynamo-base
          patch:
            spec:
              containers:
                - name: sglang
                  command:
                    - python3
                    - -m
                    - dynamo.sglang
                  args:
                    - --disaggregation-mode
                    - decode
```

---

## 📂 Examples Directory

### 🧱 Basic Examples (`examples/basic/`)

| Path | Description |
|:-----|:------------|
| `rbg/base.yaml` | Basic RoleBasedGroup with role dependencies |
| `rbg/dependency/` | Role dependency configurations |
| `rbg/patterns/` | Deployment patterns: standalone, leader-worker, custom-components |
| `rbg/scheduling/` | Gang scheduling: Volcano, scheduler-plugins |
| `rbg/update-strategy/` | Rolling update with partition support |
| `rbg/restart-policy/` | Restart policy configurations |
| `rbg/scaling/` | Scaling adapter with HPA integration |
| `rbg/role-template/` | RoleTemplates for reducing duplication |
| `coordinated-policy/` | Coordinated rollout and scaling policies |
| `engine-runtime/` | Engine runtime profile configurations |

### 🧠 Inference Examples (`examples/inference/`)

| Path | Description |
|:-----|:------------|
| `agg-standalone.yaml` | Aggregated SGLang (standalone pattern) |
| `agg-leader-worker.yaml` | Aggregated (leader-worker pattern) |
| `pd-disagg-standalone.yaml` | Prefill/Decode disaggregated (standalone) |
| `pd-disagg-leader-worker.yaml` | Prefill/Decode disaggregated (leader-worker) |
| `ecosystem/` | NATS, etcd, Dynamo, Mooncake integration |
| `ecosystem/dynamo/` | NVIDIA Dynamo runtime examples |
| `ecosystem/mooncake/` | Mooncake KV cache transfer engine |

---

## 📚 Documentation

| Source | Link |
|:-------|:-----|
| **Official Docs** | [rolebasedgroup.github.io](https://rolebasedgroup.github.io) |
| **Local Docs** | [doc/TOC.md](doc/TOC.md) |

### Version Compatibility

| RBG Version | Kubernetes | LeaderWorkerSet |
|:------------|:----------:|:---------------:|
| main / v0.7.0-alpha.x | >=v1.22.x | Not Required |
| v0.6.0 | >=v1.28.x | >=v0.7.0 |
| v0.5.0 | >=v1.28.x | >=v0.6.0 |
| v0.4.0 | >=v1.28.x | >=v0.7.0 |

---

## 🤝 Contributing

We welcome contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

```shell
# Verify copyright headers
make copyright-check

# Add missing headers
make copyright-fix
```

---

## 💬 Community

| Channel | Link |
|:--------|:-----|
| **Slack** | [#rbg channel](https://sgl-fru7574.slack.com/archives/C098X0LQZV5) |
| **Issues** | [GitHub Issues](https://github.com/sgl-project/rbg/issues) |
| **Discussions** | [Community Discussions](https://github.com/sgl-project/rbg/discussions) |

### 📜 Code of Conduct

This project follows the [Kubernetes Code of Conduct](doc/code-of-conduct.md).

---

## 🙏 Acknowledgment

RBG is inspired by and reuses code from [LeaderWorkerSet (LWS)](https://github.com/kubernetes-sigs/lws).
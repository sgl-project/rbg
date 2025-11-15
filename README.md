# The RoleBasedGroup API

English｜[简体中文](./README-zh_CN.md)

**RoleBasedGroup**: An API for orchestrating distributed workload services with multirole collaboration and automated
service discovery. It provides a common deployment pattern of AI inference workloads, especially for disaggregated
prefill and decode architecture.

## Latest News 🔥

**[2025-07-21]** RBG v0.3.0 is released. Please check out
the [release notes](https://github.com/sgl-project/rbg/releases) for more details.

## Overview

Kubernetes StatefulSet is ill-suited for coordinating multiple roles in distributed, stateful services. This solution
tackles the following challenges:

- Role startup-order dependencies
- Complex, cross-role service discovery
- Fragmented configuration management

### Key Features

- **Multirole Template Spec** - Model distributed stateful workloads as unified K8s workload groups.
- **Role-based Startup Control** - Orchestrate StatefulSets by defining role dependencies and precise startup sequences
  within a RoleBasedGroup.
- **Auto Service Discovery** - Inject topology details via configs and env vars.
- **Elastic Scaling** - Enable group/role-level scaling operations.
- **Atomic Rollout** - Role-level rollout/update: Upgrade entire Roles sequentially as single units (all pods in the
  same role updated simultaneously).
- **Topology-aware Placement** - Guarantee co-location of group/role pods within the same topology domain.
- **Atomic Failure Recovery** - Trigger full role recreation if any pod/container fails within the same group/role.
- **Customizable Workload** - Support for multiple workload types (e.g. StatefulSet, Deployment, LeaderWorkerSet etc.)
  for the role.

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

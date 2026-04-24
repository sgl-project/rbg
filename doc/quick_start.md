# Quick Start

RoleBasedGroup (RBG) is a custom resource that models a group of roles (each role represents a workload type and set of pods) and the relationships between them. It is intended to manage multi-role applications that may require coordinated scheduling, lifecycle management, rolling updates, and optional gang-scheduling (PodGroup) support.

## Conceptual View

![rbg](./img/rbg.jpg)

## Key Features

- [Multi Roles](features/multiroles.md)
- [Workload Patterns](features/patterns.md)
- [Role Dependencies](features/role-dependencies.md)
- [Autoscaling](features/autoscaler.md)
- [Update Strategy](features/update-strategy.md)
- [Coordinated Policy](features/coordinated-policy.md)
- [Failure Handling](features/failure-handling.md)
- [Gang Scheduling](features/gang-scheduling.md)
- [Exclusive Topology](features/exclusive-topology.md)
- [Monitoring](features/monitoring.md)

## PD Colocation

When a request comes into an LLM inference engine, the system will first take the user input to generate the first token (**prefill**), then generate outputs token-by-token autoregressively (**decode**). A request usually consists of one prefill step, and multiple decoding steps until termination.

![colocation](./img/colocation.png)

### Aggregated Deployment (Single Node)

When the model is small enough that a single Kubernetes Node can load all model files, you can deploy the LLM inference service on a single node using aggregated deployment.

![single-node](./img/single-node.jpg)

#### Aggregated inference examples

- [Aggregated Standalone](../examples/inference/agg-standalone.yaml)
- [Aggregated Leader-Worker](../examples/inference/agg-leader-worker.yaml)

### Aggregated Deployment (Multi Nodes with Tensor Parallelism)

When the model is too large for a single Node to load all files, use multi-node distributed inference with tensor parallelism.

![multi-nodes](./img/multi-nodes.jpg)

#### Multi-node aggregated examples

- [Aggregated Multi-Node (Dynamo)](../examples/inference/ecosystem/dynamo/agg-multi-nodes.yaml)

## PD Disaggregated

Colocating the two phases and batching the computation of prefill and decoding across all users and requests not only leads to strong prefill-decoding interferences but also couples the resource allocation and parallelism plans for both phases. Disaggregating the prefill and decoding computation improves the performance of large language models (LLMs) serving.

![pd-disagg](./img/pd-disagg.jpg)

### PD-disagg inference examples

Deploying PD-disagg inference service with RBG.

![rbg-pd](./img/rbg-pd.jpg)

- [PD-Disaggregated Standalone](../examples/inference/pd-disagg-standalone.yaml)
- [PD-Disaggregated Leader-Worker](../examples/inference/pd-disagg-leader-worker.yaml)
- [PD-Disaggregated Multi-Node (Dynamo)](../examples/inference/ecosystem/dynamo/pd-disagg-multi-nodes.yaml)

## Ecosystem Integration

For advanced deployments with NVIDIA Dynamo or Mooncake KV Cache:

- [Dynamo Integration](../examples/inference/ecosystem/dynamo/)
- [Mooncake Integration](../examples/inference/ecosystem/mooncake/)

See [Ecosystem Integration](features/ecosystem-integration.md) for detailed documentation.
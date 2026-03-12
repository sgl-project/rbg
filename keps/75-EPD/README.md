# KEP-75 SGLang EPD Integration

<!--
This KEP introduces the integration of Encoder-Prefill-Decode (EPD) Disaggregation 
within the Role-Based Group (RBG) framework for SGLang.
-->

<!-- toc -->
- [Motivation](#motivation)
- [Proposal](#proposal)
    - [User Stories](#user-stories)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
    - [EPD Architecture](#epd-architecture)
    - [Role Definitions](#role-definitions)
    - [RBG Deployment Example](#rbg-deployment-example)
- [Benchmark](#benchmark)
- [Test Plan](#test-plan)
    - [Integration Tests](#integration-tests)
    - [End to End Tests](#end-to-end-tests)
<!-- /toc -->

## Motivation

Vision-Language Models (VLMs), such as Qwen2.5-VL and Llama-Vision, introduce unique computational challenges that standard collocated inference architectures struggle to handle efficiently:

1.  **ViT Scaling Inefficiency**: Vision Transformers (ViT) do not scale linearly with Tensor Parallelism (TP). Increasing TP for ViT often degrades performance due to communication overhead.
2.  **Resource Imbalance**: Vision processing (encoding) is compute-intensive but only occurs during the prefill phase. In requests with multiple images, the vision encoder becomes a significant bottleneck for Time To First Token (TTFT).
3.  **Static Resource Allocation**: In traditional deployments, vision and language components share the same GPU resources, preventing independent scaling based on workload characteristics.

This KEP aims to integrate **Encoder-Prefill-Decode (EPD) Disaggregation** as a core pattern in RBG-deployed SGLang services. By separating vision encoding into a dedicated role, users can scale encoders horizontally to handle image-heavy workloads, significantly improving service SLOs.

## Proposal

The proposal introduces a three-tier disaggregated architecture:
- **Encoder**: Independent nodes for ViT processing.
- **Prefill**: Language nodes that retrieve embeddings from the Encoders.
- **Decode**: Dedicated nodes for token generation.

This enables independent scaling of the vision encoding capacity without altering the language model's configuration.

### User Stories

#### Story 1
As a multi-modal service provider, I want to handle requests containing 8+ images. By deploying multiple `vlm-encoder` replicas in an RBG, I want the system to parallelize image encoding across these nodes, reducing the TTFT from seconds to milliseconds.

### Risks and Mitigations

-   **Network Latency**: Moving large vision embedding tensors between nodes adds overhead.
    -   *Mitigation*: Support high-performance transfer backends like Mooncake (GPU-Direct RDMA) or ZMQ to ensure transfer time is significantly lower than the compute time saved.
-   **Resource Utilization**: Dedicated encoders may be underutilized during text-only requests.
    -   *Mitigation*: Use RBG's horizontal scaling capabilities to adjust encoder counts based on real-time multimodal traffic.

## Design Details

### EPD Architecture

The EPD workflow follows a specific request flow:
1.  **Client Request**: Arrives at the `sglang-router`.
2.  **Image Distribution**: The `vlm-prefill` node splits image inputs and distributes them to the `vlm-encoder` pool.
3.  **Vision Encoding**: Encoders run the ViT forward pass and generate embeddings (optionally using a vision cache).
4.  **Embedding Transfer**: Embeddings are returned to the `vlm-prefill` node.
5.  **LLM Computation**: The prefill node processes the language prompt and hands off the KV cache to the `vlm-decode` node.

### Role Definitions

| Component | RBG Role Name | Flag | Description |
| :--- | :--- | :--- | :--- |
| **Encoder** | `vlm-encoder` | `--encoder-only` | Dedicated to ViT. Supports prefix multi-modal caching. |
| **Prefill** | `vlm-prefill` | `--language-only` | Dedicated to LLM prefill. Fetches embeddings from encoders. |
| **Decode** | `vlm-decode` | `--disaggregation-mode decode` | Dedicated to auto-regressive token generation. |
| **Router** | `sglang-router` | N/A | Entry point for load balancing and PD coordination. |

### RBG Deployment Example

The following RBG manifest deploys a Qwen2.5-VL-7B EPD cluster with 2 Encoders, 1 Prefiller, and 1 Decoder.

```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: sglang-vlm-epd
spec:
  roles:
    - name: vlm-encoder
      replicas: 2
      template:
        spec:
          containers:
            - name: sglang
              image: lmsysorg/sglang:dev
              command:
                - python3
                - -m sglang.launch_server
                - --model-path /models/Qwen2.5-VL-7B-Instruct
                - --encoder-only
                - --enable-prefix-mm-cache
                - --port 30002
              resources:
                limits:
                  nvidia.com/gpu: "1"

    - name: vlm-prefill
      dependencies: [ "vlm-encoder" ]
      replicas: 1
      template:
        spec:
          containers:
            - name: sglang
              image: lmsysorg/sglang:dev
              env:
                - name: ENCODER_URLS
                  value: "http://s-sglang-vlm-epd-vlm-encoder-0:30002 http://s-sglang-vlm-epd-vlm-encoder-1:30002"
              command:
                - sh
                - -c
                - "python3 -m sglang.launch_server \
                  --model-path /models/Qwen2.5-VL-7B-Instruct \
                  --language-only \
                  --disaggregation-mode prefill \
                  --encoder-urls $(ENCODER_URLS) \
                  --port 30000"
              resources:
                limits:
                  nvidia.com/gpu: "1"

    - name: vlm-decode
      replicas: 1
      template:
        spec:
          containers:
            - name: sglang
              image: lmsysorg/sglang:dev
              command:
                - python3
                - -m sglang.launch_server
                - --model-path /models/Qwen2.5-VL-7B-Instruct
                - --disaggregation-mode decode
                - --port 30001
              resources:
                limits:
                  nvidia.com/gpu: "1"

    - name: sglang-router
      dependencies: [ "vlm-prefill", "vlm-decode" ]
      replicas: 1
      template:
        spec:
          containers:
            - name: router
              image: lmsysorg/sglang:dev
              command:
                - python3
                - -m sglang_router.launch_router
                - --pd-disaggregation
                - --prefill http://s-sglang-vlm-epd-vlm-prefill:30000
                - --decode http://s-sglang-vlm-epd-vlm-decode:30001
                - --port 8000

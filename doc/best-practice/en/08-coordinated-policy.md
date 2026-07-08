## Overview
RBG inference service autoscaling is implemented through `scalingAdapter` — it exposes a standard Kubernetes Scale subresource for each role, allowing any scaling strategy to deliver replica count decisions to RBG. `scalingAdapter` itself does not provide a scaling strategy; it is simply an execution channel.

Different scaling strategies drive this channel through their respective decision logic, mainly falling into two categories:

+ **Metric/Event-driven scaling**: Using community scalers like HPA, KEDA, based on CPU/memory utilization, custom metrics, or external events for reactive scaling.
+ **SLA-driven scaling**: Using [RBG Planner](https://github.com/sgl-project/rbg-planner), based on TTFT/ITL latency targets, load prediction, and offline performance profiling for predictive scaling, specifically designed for PD-disaggregated inference scenarios.

```plain
Scaling Strategy (Decision Layer)
┌─────────────┐  ┌─────────────┐  ┌─────────────┐
│    HPA       │  │    KEDA     │  │ RBG Planner │
│ (Metric-driven)│  │ (Event-driven)│  │ (SLA-driven) │
└──────┬──────┘  └──────┬──────┘  └──────┬──────┘
       │                │                │
       └────────────────┼────────────────┘
                        │ Writes spec.replicas
                        ▼
              ┌─────────────────────┐
              │  ScalingAdapter     │  ← Unified execution channel
              │  (Scale subresource) │
              └──────────┬──────────┘
                         │ Sync replicas
                         ▼
              ┌─────────────────────┐
              │  RoleBasedGroup     │  ← Pod scaling
              └─────────────────────┘
```

## Prerequisites
+ Kubernetes cluster version >= 1.24
+ RBG Controller installed (see [Installation Guide](https://github.com/sgl-project/rbg))
+ Metrics Server installed (HPA resource metrics dependency)
+ When using custom metrics, Prometheus Adapter or KEDA must be installed

---

## ScalingAdapter: Unified Scaling Execution Channel
### How It Works
Each role in RBG can enable autoscaling via `scalingAdapter.enable: true`. Once enabled, RBG Controller automatically creates a `RoleBasedGroupScalingAdapter` (RBGSA for short) resource that implements the standard Kubernetes `/scale` subresource interface. Whether HPA, KEDA, or RBG Planner, they all deliver replica count decisions to RBG roles through this Scale subresource.

```plain
HPA / KEDA / RBG Planner
        │
        │ Writes spec.replicas
        ▼
RoleBasedGroupScalingAdapter (Scale subresource)
        │
        │ Sync replicas
        ▼
RoleBasedGroup → Role Pod scaling
```

### Enabling ScalingAdapter
Set `scalingAdapter.enable: true` in the role's spec:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inference-cluster
spec:
  roles:
    - name: prefill
      replicas: 2
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                ports:
                  - containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"
```

#### Parameter Description
| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `scalingAdapter.enable` | bool | No | `false` | Whether to enable autoscaling |
| `scalingAdapter.labels` | map[string]string | No | - | Custom labels attached to the RBGSA resource |


### Auto-Created RBGSA Naming Convention
When `scalingAdapter.enable: true`, the Controller auto-created RBGSA resource follows this naming convention:

```plain
<rbg-name>-<role-name>
```

For example, if the RBG name is `inference-cluster` and the role name is `prefill`, the auto-created RBGSA name is `inference-cluster-prefill`.

> **Note**: The RBGSA's lifecycle is bound to the RBG via OwnerReference. When the RBG is deleted or the role is removed, the corresponding RBGSA is automatically cleaned up. No manual RBGSA creation is needed.
>

---

## Scenario 1: Metric-Driven Scaling — HPA
HPA (Horizontal Pod Autoscaler) is Kubernetes' built-in autoscaler that supports reactive scaling based on CPU and memory utilization. This is the simplest scaling approach, suitable for scenarios with clear resource utilization thresholds.

### Configuration Example
```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: prefill-hpa
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: inference-cluster-prefill   # <rbg-name>-<role-name>
  minReplicas: 1
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
```

#### HPA Key Fields
| Field | Description |
| --- | --- |
| `scaleTargetRef.apiVersion` | Fixed as `workloads.x-k8s.io/v1alpha2` |
| `scaleTargetRef.kind` | Fixed as `RoleBasedGroupScalingAdapter` |
| `scaleTargetRef.name` | RBGSA name, format `<rbg-name>-<role-name>` |
| `minReplicas` | Minimum replica count |
| `maxReplicas` | Maximum replica count |
| `metrics` | List of scaling metrics |


> **Note**: HPA obtains the Pod label selector via RBGSA's `status.selector` to collect metric data — no additional labelSelector configuration needed.
>

### HPA Configuration Recommendations for GPU Inference Services
For GPU inference services, CPU utilization typically does not accurately reflect load. It is recommended to follow community practices (such as KServe, vLLM production deployment solutions) and use custom metrics for scaling:

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: inference-hpa
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: inference-cluster-prefill
  minReplicas: 1
  maxReplicas: 10
  metrics:
    # Based on custom metrics exposed by the inference engine
    - type: Pods
      pods:
        metric:
          name: inference_queue_length    # Inference request queue length
        target:
          type: AverageValue
          averageValue: "100"             # Per-instance average queue length threshold
```

Common custom metrics for inference services (need to be exposed to HPA via Prometheus Adapter):

| Metric | Description | Applicable Scenario |
| --- | --- | --- |
| `inference_queue_length` | Number of queued inference requests | Queue-driven scaling |
| `inference_tokens_per_second` | Tokens processed per second | Throughput-driven scaling |
| `inference_kv_cache_usage` | KV Cache usage rate | VRAM-driven scaling |


> **Note**: Custom metrics require the inference engine to expose Prometheus-format metrics at the `/metrics` endpoint, and use [Prometheus Adapter](https://github.com/kubernetes-sigs/prometheus-adapter) to convert them to Custom Metrics API consumable by HPA. The specific metric names and configuration methods vary by inference engine — please refer to the corresponding engine's documentation.
>

---

## Scenario 2: Event-Driven Scaling — KEDA
KEDA (Kubernetes Event-Driven Autoscaling) supports richer external metric sources, suitable for scaling based on message queue depth, external API latency, and other metrics.

### Configuration Example
```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: inference-keda-scaler
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: inference-cluster-prefill
  minReplicaCount: 1
  maxReplicaCount: 10
  pollingInterval: 30
  cooldownPeriod: 300
  triggers:
    - type: prometheus
      metadata:
        serverAddress: http://prometheus.monitoring:9090   # Replace with the actual Prometheus address in your cluster
        metricName: inference_queue_length
        query: |
          avg(sglang_num_queue_requests{rbg="inference-cluster", role="prefill"})
        threshold: "100"
```

#### KEDA Key Fields
| Field | Description |
| --- | --- |
| `scaleTargetRef` | Same as HPA, points to RBGSA |
| `pollingInterval` | Metric collection interval (seconds) |
| `cooldownPeriod` | Scale-down cooldown period (seconds), prevents frequent scale-downs |
| `triggers` | Trigger list, supports prometheus, kafka, redis, and many other types |


> **Note**: KEDA's `cooldownPeriod` is particularly important for inference services. Inference engines have long startup times (model loading, KV Cache initialization), and overly frequent scale-downs cause service jitter. It is recommended to set `cooldownPeriod` to 5-10 minutes.
>

---

## Scenario 3: SLA-Driven Scaling — RBG Planner
### Aggregated Deployment: HPA/KEDA Is Sufficient
For aggregated (non-PD-disaggregated) inference services, HPA or KEDA can meet most scaling needs. The inference engine serves as a single role, and scaling decisions are relatively simple — just focus on a single role's resource utilization or custom metrics.

### PD Disaggregation: HPA/KEDA Falls Short
In PD-disaggregated architecture, Prefill and Decode are two independent roles, each with its own HPA. Although CoordinatedPolicy can coordinate scaling progress, the fundamental problem with this approach is: **HPA/KEDA does not understand the characteristics of inference workloads**.

1. **Asymmetric resource requirements for Prefill and Decode**: Prefill is compute-intensive (processing long prompts), Decode is memory-intensive (generating tokens one by one). Under the same request pattern, their resource consumption is completely different — simple progress coordination cannot solve the resource ratio problem
2. **Reactive scaling lag**: GPU inference engines take minutes to start (model loading, KV Cache initialization). By the time HPA detects rising metrics and scales up, users have already experienced latency. For PD-disaggregated scenarios, lag in one role causes the entire pipeline to block
3. **Lack of SLA awareness**: HPA is based on CPU/memory utilization or queue depth, and cannot directly use inference quality metrics like TTFT and ITL as scaling targets. In PD-disaggregated scenarios, Prefill affects TTFT, Decode affects ITL — both need independent optimization
4. **Lack of performance profiling**: Different models have vastly different throughput on different hardware. HPA cannot know "how many Prefill instances are needed to handle 2048-token prompts" — it can only passively wait for metrics to rise

These problems cannot be solved by CoordinatedPolicy — it only controls scaling progress synchronization, not scaling decision intelligence. PD-disaggregated inference needs a scaling strategy that **understands inference workloads and can simultaneously consider the relationship between Prefill and Decode**.

### RBG Planner: Intelligent Scaling Designed for PD-Disaggregated Inference
[RBG Planner](https://github.com/sgl-project/rbg-planner) is an independent Kubernetes Operator that provides **SLA-driven predictive scaling** specifically for PD-disaggregated inference. Its core algorithm originates from the [NVIDIA Dynamo](https://github.com/ai-dynamo/dynamo) project, adapted to native Kubernetes RBG API. Like HPA/KEDA, RBG Planner delivers replica count decisions to RBG through the ScalingAdapter's Scale subresource.

Unlike HPA/KEDA which independently scale each role, RBG Planner makes scaling decisions for Prefill and Decode as a whole — based on the characteristics of the request load (input length, output length), it separately calculates the optimal replica count for both roles.

RBG Planner's working loop:

```plain
┌──────────────────────────────────────────────────────────────────┐
│                    RBG Planner Working Loop                       │
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐           │
│  │   Observe     │─→│   Predict    │─→│   Compute    │           │
│  │              │  │              │  │              │           │
│  │ Collect real- │  │ Predict next │  │ Based on     │           │
│  │ time metrics  │  │ cycle request│  │ performance  │           │
│  │ TTFT, ITL,   │  │ volume, ISL, │  │ profile      │           │
│  │ requests, ISL│  │ OSL (ARIMA)  │  │ compute      │           │
│  │              │  │              │  │ required     │           │
│  │              │  │              │  │ Prefill/     │           │
│  │              │  │              │  │ Decode repl. │           │
│  └──────────────┘  └──────────────┘  └──────┬───────┘           │
│                                             │                    │
│                                             ▼                    │
│  ┌──────────────┐                                                  │
│  │   Scale       │  Via RBGSA or direct RBG Patch                  │
│  │              │  Write replica count                              │
│  └──────────────┘                                                  │
│                                                                  │
│  Execute every N seconds (default 180 seconds)                    │
└──────────────────────────────────────────────────────────────────┘
```

### Key Capabilities
| Capability | Description |
| --- | --- |
| SLA target driven | Uses TTFT and ITL latency targets (milliseconds) as scaling constraints |
| Load prediction | Uses ARIMA/Prophet time series forecasting to anticipate load changes |
| Performance profiling | Obtains model throughput on specific hardware through offline Profiling, uses scipy cubic interpolation to calculate precise resource requirements |
| PD independent scaling | Separately calculates optimal replica counts for Prefill and Decode |
| GPU budget control | Sets total GPU limit, allocates resources proportionally within budget |
| Correction factor | Real-time comparison of observed vs. expected values, auto-corrects scaling decisions |


### Installing RBG Planner
```bash
helm install rbg-planner oci://ghcr.io/sgl-project/charts/rbg-planner \
  -n rbg-system --create-namespace
```

### Prerequisite: PD-Disaggregated Inference Service
RBG Planner requires the inference service to be deployed in PD-disaggregated architecture, with both roles having ScalingAdapter enabled:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
  namespace: inference
spec:
  roles:
    - name: prefill
      replicas: 2
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                ports:
                  - containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"

    - name: decode
      replicas: 4
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                ports:
                  - containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"
```

### Creating AutoScaler CR
```yaml
apiVersion: inference-extension.rolebasedgroup.io/v1alpha1
kind: AutoScaler
metadata:
  name: pd-inference          # Must match the RBG name
  namespace: inference        # Must match the RBG namespace
spec:
  scalingInterval: 180        # Scaling decision interval (seconds)

  pattern:
    PDDisaggregated:
      prefill:
        roleName: prefill     # Prefill role name in RBG
        minReplicas: 1
        maxReplicas: 10
      decode:
        roleName: decode      # Decode role name in RBG
        minReplicas: 1
        maxReplicas: 10

  implementation:
    DynamoPlanner:
      modelName: "Qwen/Qwen3-0.6B"

      # SLA targets (milliseconds)
      ttft: 200.0             # First token latency target
      itl: 20.0               # Inter-token latency target

      # Load prediction configuration
      loadPredictor: arima     # Prediction algorithm: arima | constant | prophet
      predictionWindow: 50     # Prediction window size (data points)

      # Correction factor
      noCorrection: false      # Whether to disable real-time correction

      # Dry-run mode (observe only, no scaling executed)
      dryRun: false

      profiling:
        image: "ghcr.io/sgl-project/rbg-profiler:latest"

      metricsEndpoint:
        metricSource: sglang   # Inference engine type: sglang | vllm | dynamo
        port: 9091             # Metrics port
```

#### Parameter Description
| Parameter | Type | Description |
| --- | --- | --- |
| `spec.scalingInterval` | int | Scaling decision interval (seconds), default 180 |
| `spec.pattern.PDDisaggregated.prefill.roleName` | string | Prefill role name, must match the role name in RBG |
| `spec.pattern.PDDisAggregated.prefill.minReplicas` | int | Prefill minimum replicas |
| `spec.pattern.PDDisAggregated.prefill.maxReplicas` | int | Prefill maximum replicas |
| `spec.implementation.DynamoPlanner.modelName` | string | Model name, used for Profiling |
| `spec.implementation.DynamoPlanner.ttft` | float | TTFT SLA target (milliseconds) |
| `spec.implementation.DynamoPlanner.itl` | float | ITL SLA target (milliseconds) |
| `spec.implementation.DynamoPlanner.loadPredictor` | string | Load prediction algorithm: `arima` (recommended), `constant`, `prophet` |
| `spec.implementation.DynamoPlanner.predictionWindow` | int | Prediction window size (historical data points) |
| `spec.implementation.DynamoPlanner.noCorrection` | bool | Whether to disable real-time correction factor |
| `spec.implementation.DynamoPlanner.dryRun` | bool | Observe only without executing scaling (for tuning validation) |
| `spec.implementation.DynamoPlanner.profiling.image` | string | Profiler image address |
| `spec.implementation.DynamoPlanner.metricsEndpoint.metricSource` | string | Inference engine type |
| `spec.implementation.DynamoPlanner.metricsEndpoint.port` | int | Inference engine metrics port |


### Profiling Process
After creating the AutoScaler, the Operator automatically executes the following process:

```plain
1. Create Profiling Job (one-time task)
   └── Uses bench_serving to benchmark the model with multiple parameter sets
   └── Generates performance profile data for Prefill and Decode
   └── Saves as ConfigMap

2. Deploy Planner Engine (long-running Deployment)
   └── Loads Profiling data
   └── Starts the observe-predict-compute-scale loop
```

The performance profile generated by the Profiler includes:

+ **Prefill profile**: Input sequence length (ISL) → TTFT + per-GPU throughput (cubic spline interpolation)
+ **Decode profile**: KV Cache usage × context length → ITL + per-GPU throughput (2D scatter interpolation)

> **Note**: Profiling only needs to be executed once. Performance profile data is stored in ConfigMap, and the AutoScaler automatically loads it on restart. If the model or hardware is changed, the AutoScaler needs to be deleted and recreated to trigger re-Profiling.
>

### Verifying Planner Running Status
```bash
# Check AutoScaler status
kubectl get autoscaler -n inference

# Check Planner Pod logs
kubectl logs -n inference -l app=rbg-planner

# Check current replica count decision
kubectl get autoscaler pd-inference -n inference -o jsonpath='{.status.prefillReplicas}{""}{.status.decodeReplicas}'

# Check RBG role replica counts (driven by Planner)
kubectl get rbg pd-inference -n inference -o jsonpath='{range .spec.roles[*]}{.name}{"="}{.replicas}{"\n"}{end}'
```

### Recommended Tuning Process
1. **Start with dryRun**: Set `dryRun: true`, Planner only observes and computes without executing scaling. Observe through logs whether predictions and computed results are reasonable.
2. **Adjust SLA targets**: Based on observations during dryRun, set reasonable TTFT/ITL targets. Targets that are too low cause over-scaling, too high degrades user experience.
3. **Disable dryRun**: After confirming the configuration is reasonable, set `dryRun: false` to enable actual scaling.
4. **Observe correction factor**: Planner calculates correction factor in real-time (observed / expected). If it persistently runs high, it means actual performance is below profile expectations — re-Profiling may be needed.

---

## Solution Selection Recommendations
### By Deployment Architecture
| Deployment Architecture | Recommended Solution | Rationale |
| --- | --- | --- |
| **Aggregated** (non-PD-disaggregated) | HPA or KEDA | Inference engine as a single role, simple scaling decisions, resource utilization or custom metrics meet the needs |
| **PD-disaggregated** | RBG Planner | HPA/KEDA scales each role independently, cannot understand the resource ratio relationship between Prefill and Decode; RBG Planner makes SLA-driven scaling decisions for both as a whole |


### Capability Comparison
| Dimension | HPA | KEDA | RBG Planner |
| --- | --- | --- | --- |
| Decision type | Reactive (metric threshold) | Reactive (external events) | Predictive (SLA + load prediction) |
| Metric source | CPU/memory/custom metrics | External metric sources (Prometheus, Kafka, etc.) | Inference engine Prometheus metrics |
| SLA awareness | No | No | Yes (TTFT/ITL) |
| Load prediction | No | No | Yes (ARIMA/Prophet) |
| Performance profiling | No | No | Yes (offline Profiling) |
| PD coordination | Limited (CoordinatedPolicy only syncs progress) | Limited (CoordinatedPolicy only syncs progress) | Built-in (holistic computation of Prefill/Decode optimal ratio) |
| GPU budget control | No | No | Yes |
| Execution channel | ScalingAdapter | ScalingAdapter | ScalingAdapter |
| Applicable architecture | Aggregated | Aggregated | PD-disaggregated |


**Key difference**: HPA/KEDA creates independent scalers for each role. Even with CoordinatedPolicy, it can only control scaling progress synchronization — it cannot make intelligent resource ratio decisions based on inference workload characteristics (e.g., Prefill compute-intensive vs Decode memory-intensive). RBG Planner treats Prefill and Decode as a whole, calculating optimal replica counts for both roles based on request characteristics (input length, output length) and performance profiles.

---

## Verify Scaling Status
```bash
# Check HPA status
kubectl get hpa

# Check KEDA ScaledObject status
kubectl get scaledobject

# Check AutoScaler status
kubectl get autoscaler -n inference

# Check RBGSA status
kubectl get rbgsa

# Check RBG role replica counts
kubectl get rbg -o wide
```

## Related Documents
+ [Deploying Inference Services with RBG](#)
+ [Configuring Rolling Update Strategies](#)
+ [In-Place Update and In-Place Scheduling](#)
+ [RBG Planner Project](https://github.com/sgl-project/rbg-planner)

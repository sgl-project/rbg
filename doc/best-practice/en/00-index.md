# Documentation Index
This best practice documentation series covers the full range of scenarios from deployment to operations. Below is a quick index of all documents:

| # | Document | Core Content |
| --- | --- | --- |
| 1 | [Multi-Role Configuration & Role Topology](01-deploy-inference-service.md) | 4 deployment topologies (Aggregated/PD-disaggregated × single-node/multi-node) |
| 2 | [Simplifying Configuration with RoleTemplates](02-using-role-templates.md) | Reuse role configuration via RoleTemplate |
| 3 | [Configuring Rolling Update Strategies](03-configuring-rolling-updates.md) | Rolling update parameters and strategy selection |
| 8 | [Configuring Autoscaling for RBG Services](08-configuring-autoscaling.md) | HPA/KEDA/RBG Planner — three scaling solutions |
| 9 | [Large-Scale Cluster Resource Estimation, Configuration, and Stress Testing](09-stress-testing-and-tuning.md) | Controller resource estimation and stress testing tools |
| 10 | [Deploying Mooncake Store with RBG](10-deploy-mooncake-store-with-rbg.md) | Deployment and management of distributed KV Cache storage |


---

# Choosing Documents by Model Size
Model size determines deployment topology and resource requirements — this is the first dimension for selecting documents.

## Small Models (<7B, Single GPU)
**Typical models**: Qwen-1.8B, Llama-3-8B, GLM-4-9B

| Scenario | Recommended Document | Notes |
| --- | --- | --- |
| **First deployment** | Document 1 (Aggregated deployment + single-node single-GPU) | Simplest approach, 1 role 1 Pod |
| **Configuration simplification** | Document 2 | Use RoleTemplate to reuse common configuration |
| **Basic operations** | Document 3 | Learn basic rolling update parameters |
| **Service discovery** | Document 10 (Layers 1-2) | Headless Service + ConfigMap is sufficient |


**Not needed**: Multi-node tensor parallelism, Mooncake Store, CoordinatedPolicy, port allocation.

---

## Medium Models (7B-70B, Single-Node Multi-GPU)
**Typical models**: Qwen-72B, Llama-3-70B, DeepSeek-V2-Lite

| Scenario | Recommended Document | Notes |
| --- | --- | --- |
| **First deployment** | Document 1 (Aggregated deployment + multi-node multi-GPU) | Use LeaderWorkerPattern for tensor parallelism |
| **Configuration simplification** | Document 2 | RoleTemplate to reuse tensor parallelism configuration |
| **Basic operations** | Document 3 | Rolling update parameter configuration |
| **Upgrade acceleration** | Document 4 | In-place update avoids Pod rebuild, preserves KV Cache |
| **Warmup acceleration** | Document 5 | Pre-download large model weights to nodes, shorten first readiness time |
| **Service discovery** | Document 10 (Layers 1-2) | DNS + ConfigMap for cluster topology |
| **Autoscaling** | Document 7 (HPA/KEDA sections) | Aggregated deployment can use HPA/KEDA |


**As needed**:

+ If KV Cache reuse is needed → Document 6 (Mooncake Store)
+ If upgrade speed is critical → Document 4 (In-place update)

---

## Large Models (>70B, Multi-Node)
**Typical models**: DeepSeek-R1-671B, Llama-3-405B, Qwen-110B

| Scenario | Recommended Document | Notes |
| --- | --- | --- |
| **First deployment** | Document 1 (Aggregated deployment + multi-node multi-GPU) | LeaderWorkerPattern, TP=8 or higher |
| **Configuration simplification** | Document 2 | RoleTemplate to reuse multi-node configuration |
| **Warmup acceleration** | Document 5 (**strongly recommended**) | 671B model cold start takes 20-40 minutes, Warmup reduces to 5 minutes |
| **Upgrade acceleration** | Document 4 (**strongly recommended**) | In-place update avoids model reload, in-place scheduling reuses node cache |
| **KV Cache reuse** | Document 6 (**strongly recommended**) | Mooncake Store distributed KV Cache, cross-Pod reuse |
| **Basic operations** | Document 3 | Rolling update parameter configuration |
| **Service discovery** | Document 10 (Layers 1-2) | DNS + ConfigMap |
| **Autoscaling** | Document 7 (HPA/KEDA sections) | Aggregated deployment uses HPA/KEDA |


---

# Choosing Documents by Deployment Architecture
Deployment architecture (Aggregated vs. PD-disaggregated) determines operational complexity — this is the second dimension for selecting documents.

## Aggregated Deployment
The inference engine serves as a single role, making deployment and operations relatively simple.

| Operational Need | Recommended Document | Notes |
| --- | --- | --- |
| **First deployment** | Document 1 | Choose single-node or multi-node topology |
| **Rolling update** | Document 3 | Basic rolling update configuration |
| **Upgrade acceleration** | Document 4 | In-place update + in-place scheduling |
| **Warmup acceleration** | Document 5 | Recommended for large models |
| **Autoscaling** | Document 7 (HPA/KEDA) | Metric- or event-driven scaling |
| **KV Cache** | Document 6 (optional) | If cross-Pod KV Cache reuse is needed |


**Not needed**: CoordinatedPolicy (single role requires no coordination).

---

## PD-Disaggregated Deployment
Prefill and Decode are independent roles that require coordinated scaling and upgrades.

| Operational Need | Recommended Document | Notes |
| --- | --- | --- |
| **First deployment** | Document 1 (PD-disaggregated topology) | Single-node or multi-node PD disaggregation |
| **Coordinated deployment** | Document 8 (**required**) | CoordinatedPolicy controls progressive creation during initial deployment |
| **Coordinated upgrade** | Document 8 (**required**) | CoordinatedPolicy controls multi-role synchronized upgrades |
| **Rolling update** | Document 3 | Rolling update strategy per role |
| **Upgrade acceleration** | Document 4 | In-place update + in-place scheduling |
| **Warmup acceleration** | Document 5 | Recommended for large models |
| **Autoscaling** | Document 7 (RBG Planner, **strongly recommended**) | PD disaggregation requires SLA-driven predictive scaling |
| **KV Cache** | Document 6 (**strongly recommended**) | KV Cache generated by Prefill needs to be passed to Decode |


---

# Choosing Documents by Operational Need
## First Deployment
| Scenario | Recommended Document |
| --- | --- |
| Quick start, deploy your first inference service | Document 1 |
| Simplify configuration, reuse role templates | Document 2 |
| PD-disaggregated first deployment, ensure proportional role creation | Document 8 |
| Large model warmup, shorten first readiness time | Document 5 |


---

## Upgrades and Updates
| Scenario | Recommended Document |
| --- | --- |
| Configure rolling update strategy (maxUnavailable, partition) | Document 3 |
| In-place update, avoid Pod rebuild, preserve KV Cache | Document 4 |
| In-place scheduling, schedule back to original node on Pod rebuild | Document 4 |
| PD-disaggregated multi-role synchronized upgrade | Document 8 |


---

## Autoscaling
| Scenario | Recommended Document |
| --- | --- |
| Aggregated deployment, scale based on CPU/memory/custom metrics | Document 7 (HPA/KEDA) |
| PD disaggregation, predictive scaling based on SLA (TTFT/ITL) | Document 7 (RBG Planner) |
| PD disaggregation, ensure synchronized Prefill and Decode scaling | Document 7 + Document 8 |


---

## Performance Optimization
| Scenario | Recommended Document |
| --- | --- |
| Large model cold start is slow, warmup needed | Document 5 |
| KV Cache loss during upgrades, TTFT fluctuation | Document 4 (in-place update) + Document 6 (Mooncake Store) |
| Cross-Pod KV Cache reuse | Document 6 |
| hostNetwork + RDMA port conflicts | Document 10 (Layer 3: Port Allocation) |


---

## Large-Scale Production
| Scenario | Recommended Document |
| --- | --- |
| Controller resource estimation and configuration | Document 9 |
| Stress test to validate Controller performance | Document 9 |
| Controller parameter tuning (Reconciles, QPS, Burst) | Document 9 |
| Service discovery and cluster topology management | Document 10 |


---

# Quick Decision Matrix
The following matrix quickly locates the documents you need to read based on **model size** and **deployment architecture**:

```plain
                    Aggregated                    PD-Disaggregated
                ┌─────────────────┐        ┌─────────────────┐
                │  Small (<7B)     │        │  Small (<7B)     │
                │                 │        │  (not recommended)│
                │  Docs: 1,2,3,10 │        │                 │
                └─────────────────┘        └─────────────────┘

                ┌─────────────────┐        ┌─────────────────┐
                │  Medium          │        │  Medium          │
                │  (7B-70B)        │        │  (7B-70B)        │
                │                 │        │                  │
                │  Docs: 1,2,3,   │        │  Docs: 1,2,3,    │
                │        4,5,7,10 │        │        4,5,6,7,8 │
                └─────────────────┘        └─────────────────┘

                ┌─────────────────┐        ┌─────────────────┐
                │  Large           │        │  Large           │
                │  (>70B)          │        │  (>70B)          │
                │                 │        │                  │
                │  Docs: 1,2,3,   │        │  Docs: 1,2,3,    │
                │        4,5,6,   │        │        4,5,6,7,8 │
                │        7,10     │        │                  │
                └─────────────────┘        └─────────────────┘
```

**Legend**:

+ Numbers are document IDs (see "Documentation Index" table)
+ Document 9 (stress testing) applies to all large-scale production environments, regardless of model size and deployment architecture

---

# Typical Scenario Recommended Paths
## Scenario A: Quick Deployment of Qwen-72B Inference Service
**Goal**: Deploy Qwen-72B in production to provide stable inference service.

**Recommended path**:

```plain
1. Read Document 1 (Aggregated deployment + multi-node multi-GPU)
   ↓ Understand LeaderWorkerPattern topology

2. Read Document 5 (Warmup pre-warming)
   ↓ Pre-warm model weights, shorten first readiness time

3. Read Document 3 (Rolling update)
   ↓ Configure update strategy

4. Read Document 4 (In-place update)
   ↓ Configure in-place update, avoid model reload on rebuild

5. Read Document 7 (Autoscaling)
   ↓ Configure HPA/KEDA for elastic scaling

6. Read Document 10 (Service discovery)
   ↓ Understand DNS and ConfigMap service discovery
```

---

## Scenario B: Deploying DeepSeek-R1 PD-Disaggregated Inference Service
**Goal**: Deploy DeepSeek-R1 671B PD-disaggregated architecture, ensuring coordinated Prefill and Decode operation.

**Recommended path**:

```plain
1. Read Document 1 (PD disaggregation + multi-node multi-GPU)
   ↓ Understand PD-disaggregated topology and LeaderWorkerPattern

2. Read Document 8 (CoordinatedPolicy)
   ↓ Configure progressive creation and synchronized upgrades

3. Read Document 5 (Warmup pre-warming)
   ↓ Pre-warm 671B model weights and DeepGEMM pre-compilation

4. Read Document 6 (Mooncake Store)
   ↓ Deploy distributed KV Cache, Prefill → Decode transfer

5. Read Document 4 (In-place update)
   ↓ Configure in-place update, preserve KV Cache

6. Read Document 7 (RBG Planner)
   ↓ Configure SLA-driven predictive scaling

7. Read Document 10 (Service discovery)
   ↓ Understand service discovery between Prefill and Decode
```

---

## Scenario C: Large-Scale Cluster Controller Performance Tuning
**Goal**: Ensure Controller performance meets targets in a large-scale cluster managing 500+ RBG instances.

**Recommended path**:

```plain
1. Read Document 9 (Resource estimation and stress testing)
   ↓ Understand Controller resource requirements and configuration guidelines

2. Run the /stress-test Skill
   ↓ Execute stress test, collect performance data

3. Analyze report, adjust parameters
   ↓ Tune based on Document 9's decision tree

4. Re-run stress test to validate
   ↓ Confirm configuration meets target scale
```

---

## Scenario D: hostNetwork + RDMA Inference Service
**Goal**: Deploy RDMA inference service using hostNetwork, avoiding port conflicts.

**Recommended path**:

```plain
1. Read Document 10 (Port allocation and service discovery)
   ↓ Focus on Layer 3: Port allocation and component discovery

2. Enable Controller port allocator
   ↓ --enable-port-allocator=true

3. Configure port allocation annotations
   ↓ PodScoped / RoleScoped port allocation

4. Configure component discovery annotations
   ↓ addressRefs + portRefs

5. Read Document 1 (CustomComponentsPattern)
   ↓ Understand multi-component deployment topology
```

---

# Document Dependencies
Some documents have dependencies on others. It is recommended to read them in order:

```plain
Document 1 (Deployment topology)
    ↓
Document 2 (RoleTemplate) ──→ Optional, simplifies configuration
    ↓
Document 3 (Rolling update) ──→ Basic operations
    ↓
Document 4 (In-place update) ──→ Upgrade acceleration, depends on Document 3
    ↓
Document 5 (Warmup) ──→ First deployment acceleration, independent
    ↓
Document 6 (Mooncake Store) ──→ KV Cache optimization, independent
    ↓
Document 7 (Autoscaling) ──→ Runtime scaling
    ↓
Document 8 (CoordinatedPolicy) ──→ Required for PD disaggregation, depends on Document 1
    ↓
Document 9 (Stress testing) ──→ Large-scale production, independent
    ↓
Document 10 (Service discovery) ──→ Spans all scenarios, independent
```

---

# Quick FAQ
| Question | Reference Document |
| --- | --- |
| How to deploy Qwen-72B? | Document 1 (Aggregated deployment + multi-node multi-GPU) |
| How to deploy DeepSeek-R1? | Document 1 (PD disaggregation + multi-node multi-GPU) + Document 5 + Document 6 + Document 8 |
| KV Cache lost during rolling update? | Document 4 (in-place update) + Document 6 (Mooncake Store) |
| How to configure autoscaling? | Document 7 |
| How to synchronize Prefill and Decode upgrades in PD disaggregation? | Document 8 |
| How to warm up large models? | Document 5 |
| How to configure Controller resources? | Document 9 |
| Port conflicts in hostNetwork scenarios? | Document 10 (Layer 3) |
| How to get cluster topology information? | Document 10 (Layer 2: ConfigMap) |
| How to simplify repetitive role configuration? | Document 2 |


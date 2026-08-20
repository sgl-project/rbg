# Configuring In-Place Update and In-Place Scheduling Strategies

## Overview

Upgrading AI inference services faces a core challenge: **cold-start latency**. Model weight files are typically tens to hundreds of GB, and loading them into GPU memory for the first time takes several minutes. Additional startup overhead comes from CUDA kernel pre-compilation, shared library initialization, and other engine-specific warmup steps. The traditional Pod recreation approach schedules Pods to arbitrary nodes, invalidating all caches and causing significant upgrade downtime and metric fluctuations (e.g., TTFT spikes).

RBG provides two complementary features to address this problem:

+ **In-Place Update**: When only the container image changes, the image is updated and the container restarted directly on the original Pod. The Pod never leaves its current node, so all node-level caches are naturally preserved.
+ **In-Place Scheduling**: When in-place update is not feasible (e.g., Pod Spec changes beyond image scope) and the Pod must be recreated, node affinity (nodeAffinity) is injected to schedule the new Pod back to its original node, reusing cached resources on that node.

Used together, they minimize the impact of upgrades on inference service availability and performance metrics.

## Prerequisites

+ Kubernetes cluster version >= 1.24
+ RBG Controller installed (see [Installation Guide](https://github.com/sgl-project/rbg))

---

## Background: The Cold-Start Problem in Inference Services

To understand the value of "in-place", it helps to first understand what happens during an inference service Pod's startup.

### Resource Loading During Startup

An inference service Pod typically goes through the following stages from creation to Ready:

```plain
1. Image pull (minutes)
        |
        v
2. Container start (tens of seconds)
        |
        v
3. Model loading (minutes)
        |
        v
4. Engine ready (accepting traffic)
```

Each stage involves loading substantial node-level resources:

| Resource Type | Source | First Load Time | Cached Reuse Time |
| --- | --- | --- | --- |
| Container image | Registry → node local storage | Minutes (tens of GB) | Seconds (already local) |
| Model weight files | Remote storage → node disk/memory | Minutes (tens to hundreds of GB) | Seconds (page cache hit) |
| CUDA kernel pre-compilation | Runtime compilation → node cache | Tens of seconds | Instant (already compiled) |
| Inference engine initialization | Shared library loading, GPU context creation | Tens of seconds | — |


### The Importance of KV Cache Reuse

For inference services using distributed KV Cache backends (e.g., Mooncake), KV Cache data accumulates in node memory during runtime. This cached KV data directly affects key inference performance metrics:

+ **TTFT (Time To First Token)**: If the KV Cache hits, the engine can directly reuse previous computation results, significantly reducing first-token latency
+ **Post-upgrade metric fluctuations**: If a Pod is scheduled to a new node, all previously accumulated KV Cache is lost, every request must be computed from scratch, and TTFT exhibits a noticeable spike

The core value of **In-Place Update** and **In-Place Scheduling** is to preserve these node-level states as much as possible, ensuring minimal impact on service metrics during upgrades.

---

## In-Place Update: Container-Level Fast Update

In-place update is the fastest update method — the Pod never leaves its current node, and only the container is restarted to apply the new image. All node-level caches (image, model weight page cache, CUDA pre-compiled files, KV Cache) are naturally preserved.

### Update Strategy Types

RBG provides two update strategies, configured via `rollingUpdate.type`:

| Strategy | Behavior | Applicable Scenario |
| --- | --- | --- |
| `InPlaceIfPossible` | Prefer in-place update; fall back to Pod recreation if changes exceed image scope | **Recommended**, default choice for most scenarios |
| `RecreatePod` | Delete the old Pod and create a new one | Scenarios requiring full recreation |

> **Note**: The `InPlaceOnly` strategy is retained in the API but not separately implemented; it behaves identically to `InPlaceIfPossible` (falling back to Pod recreation when in-place update is not feasible). Use `InPlaceIfPossible` instead.


### Supported Change Scope for In-Place Update

In-place update **only supports** the following types of changes:

+ Container image (`image`) changes
+ Container metadata (e.g., labels, annotations) changes

The following changes **do not support** in-place update and require falling back to Pod recreation:

+ Adding or removing containers
+ Modifying ports (`ports`)
+ Modifying volume mounts (`volumeMounts`)
+ Modifying resource requests/limits (`resources`)
+ Modifying environment variables (`env`)

### Configuration Example
```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inplace-update-demo
spec:
  roles:
    - name: backend
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: InPlaceIfPossible
          maxUnavailable: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.5
                command: ["sleep", "3600"]
```

#### Parameter Description
| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `rollingUpdate.type` | string | No | `InPlaceIfPossible` | Update strategy type |
| `rollingUpdate.inPlaceUpdateStrategy.gracePeriodSeconds` | int32 | No | `0` | Traffic drain wait time (seconds) before in-place update |


### In-Place Update Workflow

When a role's container image changes, the RBG Controller executes the following flow:

```plain
1. Detect template change
        │
        ▼
2. Compute diff between old and new Spec
        │
        ├── Diff is image-only ──→ Perform in-place update
        │         │
        │         ▼
        │   3a. Set Pod to NotReady (remove from Service endpoints)
        │         │
        │         ▼
        │   3b. Wait gracePeriodSeconds (traffic drain)
        │         │
        │         ▼
        │   3c. Patch container image, kubelet restarts container
        │         │
        │         ▼
        │   3d. Restore Ready state after container becomes ready
        │
        └── Diff exceeds image scope ──→ Fall back to Pod recreation
```

> **Note**: During in-place update, the Pod always stays on the same node. Node-level resources such as model weight page cache, GPU context state, and KV Cache are fully preserved and can be directly reused after the container restarts.
>

---

## Grace Period: Traffic Draining

`gracePeriodSeconds` controls the wait time before an in-place update. Before updating the image, the Controller marks the Pod as NotReady (via Readiness Gate), removing it from Service endpoints, then waits for the specified duration before performing the actual image update.

```yaml
rollingUpdate:
  type: InPlaceIfPossible
  inPlaceUpdateStrategy:
    gracePeriodSeconds: 30  # Wait 30 seconds for traffic to drain
```

### How It Works

1. **Mark NotReady**: Controller sets the Pod's `InPlaceUpdateReady` condition to `False`
2. **Endpoint removal**: Since the Pod is no longer Ready, Kubernetes Service automatically removes it from the endpoint list
3. **Wait for drain**: Wait `gracePeriodSeconds` seconds for established connections to finish processing
4. **Execute update**: Patch the container image, kubelet restarts the container

### Choosing gracePeriodSeconds
| Value | Applicable Scenario |
|-------|---------------------|
| `0` | Stateless requests or requests that can be retried by other instances |
| `60-300` | Most inference services with short request processing times |
| `600-1800` | Long-lived connections or scenarios with long request processing times |


> **Note**: `gracePeriodSeconds` only takes effect during in-place update. When using the `RecreatePod` strategy, Pod deletion follows the standard `terminationGracePeriodSeconds`.

---

## In-Place Scheduling: Node Affinity During Pod Recreation

When in-place update is not feasible (e.g., Pod Spec changes exceed image scope, or the `RecreatePod` strategy is used), the Pod must be deleted and recreated. In this case, **In-Place Scheduling** injects node affinity to guide the new Pod back to its original node, reusing cached resources on that node.

### How It Works (In-Place Scheduling)

In-place scheduling is configured at the role level via two annotations:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inplace-scheduling-demo
spec:
  roles:
    - name: backend
      replicas: 4
      annotations:
        rbg.workloads.x-k8s.io/role-inplace-scheduling: "Preferred"
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: RecreatePod
          maxUnavailable: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.5
                command: ["sleep", "3600"]
```

#### Configuration Annotations
| Annotation | Value | Description |
| --- | --- | --- |
| `rbg.workloads.x-k8s.io/role-inplace-scheduling` | `Preferred` | Soft affinity: prefer scheduling to the historical node, but allow scheduling to other nodes |
| `rbg.workloads.x-k8s.io/role-inplace-scheduling` | `Required` | Hard affinity: must schedule to the historical node, otherwise Pod remains Pending |
| `rbg.workloads.x-k8s.io/role-inplace-scheduling-granularity` | `Pod` | Pod-level binding: each Pod returns to its own historical node (default for Stateful mode) |
| `rbg.workloads.x-k8s.io/role-inplace-scheduling-granularity` | `Component` | Component-level binding: Pod can be scheduled to any node where the same component type previously ran (default for Stateless mode) |


### Mode Selection: Preferred vs Required
| Mode | Behavior | Applicable Scenario | Risk |
| --- | --- | --- | --- |
| `Preferred` | Injects `preferredDuringScheduling` (weight=100), new Pod prefers the historical node but can be scheduled elsewhere | Most production environments | If the historical node lacks resources, Pod may land on another node and caches cannot be reused |
| `Required` | Injects `requiredDuringScheduling`, new Pod must be scheduled to the historical node | Environments with strict cache reuse requirements and high node stability | If the historical node is unavailable, Pod will remain Pending indefinitely |


> **Note**: The `Preferred` mode is recommended. It ensures cache reuse while retaining scheduling flexibility, avoiding situations where node failures prevent Pod scheduling.
>

### Binding Granularity: Pod vs Component

Binding granularity determines how precisely "which node to return to" is defined:

**Pod granularity** (default for Stateful mode): Each Pod returns to **its own** previously running node.

| Pod | Node Before Recreation | Target Node After Recreation | Description |
|-----|------------------------|------------------------------|-------------|
| `backend-0` | `node-A` | `node-A` | Returns precisely to its own historical node |
| `backend-1` | `node-B` | `node-B` | Returns precisely to its own historical node |

Applicable scenario: Each Pod has independent local state on its node (e.g., its own KV Cache files, model shards).

**Component granularity** (default for Stateless mode): Pods can be scheduled to **any** node where the same component type previously ran.

| Pod | Node Before Recreation | Target Node After Recreation | Description |
|-----|------------------------|------------------------------|-------------|
| `abc-prefill-master-0` | `node-A` | `node-A` or `node-C` | Any historical node of the same component type (master) |
| `abc-prefill-worker-0` | `node-B` | `node-B` or `node-D` | Any historical node of the same component type (worker) |
| `def-prefill-master-0` | `node-C` | `node-A` or `node-C` | Any historical node of the same component type (master) |
| `def-prefill-worker-0` | `node-D` | `node-B` or `node-D` | Any historical node of the same component type (worker) |

Applicable scenario: All Pods share the same model weight cache, so returning to any historical node enables reuse.

> **Note**: In Stateful mode, Pod names are stable, so Pod granularity can match precisely. In Stateless mode, Pod names change with every recreation, so Component granularity must be used. If granularity is not explicitly configured, RBG automatically selects the default based on the mode.
>

### In-Place Scheduling Workflow
```plain
1. Pod is running, Controller records Pod → Node binding
        │
        ▼
2. Pod needs recreation (in-place update not feasible / RecreatePod strategy)
        │
        ▼
3. When creating the new Pod, inject nodeAffinity (based on historical binding)
        │
        ▼
4. Scheduler places the new Pod on the historical node based on affinity
        │
        ▼
5. New Pod reuses the node's image, model weight page cache, and pre-compiled files
        │
        ▼
6. Service ready time is significantly reduced
```

---

## Combined Usage: Layered Acceleration Strategy

In-place update and in-place scheduling are not mutually exclusive — they are complementary layered strategies. Together they form a layered acceleration scheme for upgrades:

| Layer | Strategy | Trigger Condition | Cache Reuse Effect | Service Ready Time |
| --- | --- | --- | --- | --- |
| Layer 1 | In-Place Update (fastest) | Image-only change, container restarted in place | Pod stays on node, all caches naturally preserved | Seconds (`gracePeriodSeconds`) |
| Layer 2 | In-Place Scheduling (second fastest) | Pod needs recreation, scheduled back to historical node | Reuses node's image, model weights, pre-compiled files | Minutes (skips image pull and model download) |
| Layer 3 | Standard Scheduling (slowest) | Pod scheduled to a new node | Requires full image pull, model download, engine initialization | Minutes to tens of minutes |

### Recommended Configuration

Configure `InPlaceIfPossible` and in-place scheduling together, letting the system automatically choose the optimal path:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: layered-update-demo
spec:
  roles:
    - name: backend
      replicas: 4
      annotations:
        # In-place scheduling: when Pod needs recreation, schedule back to historical node
        rbg.workloads.x-k8s.io/role-inplace-scheduling: "Preferred"
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          # In-place update: prefer in-place update first
          type: InPlaceIfPossible
          maxUnavailable: 1
          inPlaceUpdateStrategy:
            gracePeriodSeconds: 30
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.5
                command: ["sleep", "3600"]
```

Upgrade behavior under this configuration:

| Change Type | Actual Path | Cache Reuse | Service Ready Time |
| --- | --- | --- | --- |
| Image-only change | In-place update | All preserved | Seconds |
| Image + resource limits change | In-place update fails → in-place scheduling | Node-level cache reuse | Minutes |
| Port/volume/env change | In-place update fails → in-place scheduling | Node-level cache reuse | Minutes |
| Node failure Pod recreation | In-place scheduling | Node-level cache reuse | Minutes |

---

## Verifying Update Status

```bash
# Check RBG status
kubectl get rbg

# Check Pod status, confirm Pod was not recreated (AGE unchanged)
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=<rbg-name> -o wide

# Check Pod events, confirm whether in-place update was executed
kubectl describe pod <pod-name> | grep -A5 "Events"

# Check node affinity (when in-place scheduling is active)
kubectl get pod <pod-name> -o jsonpath='{.spec.affinity}'
```

### Determining Whether In-Place Update Succeeded

When in-place update succeeds, the Pod's AGE does not reset (Pod was not deleted and recreated), but the container's `RESTARTS` count increases. You can confirm this with:

```bash
# Compare container restart count with Pod AGE
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=<rbg-name> -o wide
```

If the Pod AGE is significantly greater than the time corresponding to the container RESTARTS, in-place update has been successfully executed.

## Related Documents

+ [Deploying Inference Services with RBG](./01-deploy-inference-service.md)
+ [Configuring Rolling Update Strategies](./03-configuring-rolling-updates.md)
+ RBG Warmup

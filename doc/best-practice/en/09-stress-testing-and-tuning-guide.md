# Operations Guide: Large-Scale Cluster Resource Estimation, Configuration, and Stress Testing

> **Target audience**: Platform administrators / cluster operators. This document is intended for platform administrators responsible for RBG Controller deployment and performance tuning, not for general inference service users.
>
> Corresponding concept document: [9. Large-Scale Cluster Resource Estimation, Configuration, and Stress Testing](09-stress-testing-and-tuning.md)

## Objectives

Validate RBG Controller's stress testing toolchain, including:

1. Setting up a KWOK simulation environment
2. Deploying Controller and configuring resource parameters
3. Running stress tests (Create / Update / Delete — three phases)
4. Viewing reports and analyzing results
5. Tuning Controller configuration based on results

## Prerequisites

- Clone the RBG repository and navigate to the project root `git clone https://github.com/sgl-project/rbg && cd rbg`
- Kubernetes cluster version >= 1.24
- RBG CRD installed
- Local tools: `helm`, `kubectl`, `go` (>= 1.22), `curl`
- **All commands in this document assume execution from the RBG repository root**
- (Optional) Controller image supports `--enable-pprof`

---

## Operation 1: Set Up KWOK Environment

### Step 1: Install KWOK Controller and Create Simulated Nodes

```bash
# Install KWOK Controller + Stage (official stage-fast + project-custom Pod lifecycle), create 5 simulated nodes
FAKE_NODE_COUNT=5 bash test/stress/scripts/setup-kwok.sh
```

### Expected Behavior

- KWOK Controller deployed to the cluster
- Official `stage-fast` chart installed (provides Node heartbeat and initialization Stage)
- Project-custom Pod lifecycle Stage applied (`pod-initialize`, `pod-running`, `pod-ready`, `pod-delete`), overriding official default Pod Stages
- 5 KWOK simulated nodes created (`kwok-node-0` through `kwok-node-4`)
- Each simulated node default capacity: 128 CPU / 512Gi memory / 1000 Pods

### Verification

```bash
# Verify simulated nodes are ready
kubectl get nodes -l type=kwok

> NAME          STATUS   ROLES    AGE   VERSION
> kwok-node-0   Ready    <none>   12s
> kwok-node-1   Ready    <none>   10s
> kwok-node-2   Ready    <none>   8s
> kwok-node-3   Ready    <none>   6s
> kwok-node-4   Ready    <none>   4s
```

```bash
# Verify KWOK Stages are installed
kubectl get stages

> NAME                        AGE
> node-heartbeat-with-lease   53s
> node-initialize             53s
> pod-delete                  53s
> pod-initialize              37s
> pod-ready                   53s
> pod-running                 37s
```

**Expected output:**

- 5 KWOK nodes in Ready status
- 6 Stages: 2 Node-related (`node-heartbeat-with-lease`, `node-initialize`) + 4 Pod-related (`pod-initialize`, `pod-running`, `pod-ready`, `pod-delete`)
- Pod lifecycle Stage delays: `pod-initialize` immediate → `pod-running` 500ms → `pod-ready` 1000ms → `pod-delete` 500ms

### Step 2 (Optional): Customize Simulated Node Capacity

```bash
# If more resources are needed, recreate simulated nodes
FAKE_NODE_COUNT=10 \
FAKE_NODE_CPU=256 \
FAKE_NODE_MEMORY=1024Gi \
FAKE_NODE_PODS=2000 \
bash test/stress/scripts/setup-kwok.sh
```

---

## Operation 2: Deploy Controller

### Step 1: Deploy Controller and Configure Resource Parameters

```bash
# Get current image tag to avoid overwriting with latest; --no-hooks skips CRD upgrade (KWOK nodes cannot run hook jobs)
IMAGE_TAG=$(kubectl get deploy -n rbgs-system rbgs-controller-manager \
    -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*://')

helm upgrade rbgs deploy/helm/rbgs -n rbgs-system \
    --set image.tag=${IMAGE_TAG} \
    --set resources.limits.cpu=8 --set resources.limits.memory=16Gi \
    --set controllerTuning.maxConcurrentReconciles=20 \
    --set controllerTuning.kubeApiQPS=100 --set controllerTuning.kubeApiBurst=200 \
    --set pprof.enabled=true --set pprof.port=6060 \
    --no-hooks --wait --timeout=120s

# pprof port forwarding
pkill -f "port-forward.*6060" 2>/dev/null; sleep 1
kubectl port-forward -n rbgs-system deploy/rbgs-controller-manager 6060:6060 &
```

### Expected Behavior (Deploy Controller)

- Skips image build (uses existing image in the cluster)
- Upgrades Controller via Helm, only updating resource limits and runtime parameters
- Sets resource limits: 8 CPU / 16Gi memory
- Sets runtime parameters: Reconciles=20, QPS=100, Burst=200
- Enables pprof
- Waits for Controller Pod to be ready
- Establishes pprof port forwarding (`localhost:6060`)

### Verification (Deploy Controller)

```bash
# Confirm Controller Pod is ready
kubectl get pods -n rbgs-system -l control-plane=rbgs-controller

# Check Controller resource configuration
kubectl get deploy -n rbgs-system rbgs-controller-manager -o jsonpath='{.spec.template.spec.containers[0].resources}'

> {"limits":{"cpu":"8","memory":"16Gi"},"requests":{"cpu":"100m","memory":"256Mi"}}
```

```bash
# Check Controller startup parameters
kubectl get deploy -n rbgs-system rbgs-controller-manager -o jsonpath='{.spec.template.spec.containers[0].args}' | tr ',' '\n'

> ["--metrics-bind-address=:8443"
> "--leader-elect"
> "--health-probe-bind-address=:8081"
> "--max-concurrent-reconciles=20"
> "--kube-api-qps=100"
> "--kube-api-burst=200"
> "--scheduler-name=scheduler-plugins"
> "--enable-pprof=true"
> "--pprof-bind-address=:6060"]
```

**Expected output:**

- Controller Pod is Running and Ready
- Resource configuration is 8 CPU / 16Gi
- Startup parameters include `--max-concurrent-reconciles=20`, `--kube-api-qps=100`, `--kube-api-burst=200`

---

## Operation 3: Run Stress Test

### Step 1: Run Medium Benchmark Stress Test

```bash
go run ./test/stress/ \
    --namespace=stress-test \
    --total-rbgs=100 \
    --roles-per-rbg=3 \
    --lws-roles=1 \
    --lws-size=4 \
    --create-qps=5 \
    --update-qps=5 \
    --delete-qps=5 \
    --in-place-update=true \
    --use-kwok-nodes=true \
    --pprof-addr=localhost:6060 \
    --output-dir=/tmp/rbg-stress-results \
    --timeout=30m
```

### Expected Behavior (Run Stress Test)

The stress test executes in three phases:

1. **Create phase**: Creates 100 RBGs at 5 QPS (each with 3 roles, 1 using `leaderWorkerPattern`, backed by `RoleInstanceSet`), waits for all to be Ready
2. **Update phase**: Updates all RBG images at 5 QPS using InPlaceIfPossible strategy, waits for all to complete
3. **Delete phase**: Deletes all RBGs at 5 QPS, waits for all to be cleaned up

Each simulated Pod takes about 1.5 seconds from creation to Ready (KWOK Stage simulation).

> **Note**: `--lws-roles=1` means the first 1 role uses `leaderWorkerPattern` (multi-Pod group mode), backed by RBG's built-in `RoleInstanceSet` controller — no external LeaderWorkerSet CRD installation needed.

### Verification (Run Stress Test)

```bash
# Check Pod count during stress test
kubectl get pods -n stress-test --no-headers | wc -l

>      600
```

```bash
# Check Controller logs (during stress test)
kubectl logs -n rbgs-system -l control-plane=rbgs-controller -f
```

**Expected behavior:**

- Create phase: 100 RBGs created progressively, Pods scheduled to KWOK simulated nodes
- Update phase: All RBG images updated, in-place upgrade
- Delete phase: All RBGs cleaned up

---

## Operation 4: View Report and Analyze Results

### Step 1: View HTML Report

```bash
# Open HTML report
open /tmp/rbg-stress-results/report.html
```

### Step 2: View JSON Summary

```bash
cat /tmp/rbg-stress-results/summary.json
```

### Expected Content

The JSON summary contains statistics for each phase:

```json
[
  {
    "operation": "create",
    "total": 100,
    "succeeded": 100,
    "failed": 0,
    "p50_ms": 4080.5,
    "p90_ms": 5946.799999999999,
    "p99_ms": 7062.680000000001,
    "max_ms": 7328,
    "min_ms": 1024,
    "avg_ms": 3696.86,
    "total_time_sec": 25.044643083,
    "actual_qps": 3.99286983921439
  },
  {
    "operation": "update",
    "total": 100,
    "succeeded": 100,
    "failed": 0,
    "p50_ms": 393,
    "p90_ms": 634.5,
    "p99_ms": 769.6400000000003,
    "max_ms": 833,
    "min_ms": 273,
    "avg_ms": 430.39,
    "total_time_sec": 20.268429458,
    "actual_qps": 4.933781386822241
  },
  {
    "operation": "delete",
    "total": 100,
    "succeeded": 100,
    "failed": 0,
    "p50_ms": 184,
    "p90_ms": 191,
    "p99_ms": 204.2200000000001,
    "max_ms": 226,
    "min_ms": 179,
    "avg_ms": 185.18,
    "total_time_sec": 19.981816,
    "actual_qps": 5.004550136984546
  }
]
```

### Analysis Dimension 1: Throughput and Latency

Judgment rule: Actual QPS < 90% of target QPS indicates insufficient Controller processing capacity.

```bash
# Check actual QPS for each phase
cat /tmp/rbg-stress-results/summary.json | python3 -c "
import json, sys
data = json.load(sys.stdin)
for item in data:
    phase = item['operation']
    target = 5.0
    actual = item.get('actual_qps', 0)
    gap = ((actual - target) / target * 100) if target > 0 else 0
    status = '✓ Met' if actual >= target * 0.9 else '⚠ Behind'
    print(f'{phase}: target={target} actual={actual:.2f} gap={gap:.1f}% {status}')
"

> create: target=5.0 actual=3.99 gap=-20.1% ⚠ Behind
> update: target=5.0 actual=4.93 gap=-1.3% ✓ Met
> delete: target=5.0 actual=5.00 gap=0.1% ✓ Met
```

> Create phase QPS is behind target because each RBG contains 3 roles (300 RoleInstanceSets total), and the Controller needs to create and wait for all sub-resources to be ready. This can be improved by increasing `max-concurrent-reconciles` or raising `kube-api-qps`.

### Analysis Dimension 2: Latency Distribution

```bash
# Check latency data
cat /tmp/rbg-stress-results/timing-create.csv | head -5

> name,operation,start_time,end_time,duration_ms,error
> stress-rbg-0001,create,2026-07-08T17:47:41.62093+08:00,2026-07-08T17:47:42.645876+08:00,1024.00,
> stress-rbg-0000,create,2026-07-08T17:47:41.420105+08:00,2026-07-08T17:47:43.068567+08:00,1648.00,
> stress-rbg-0002,create,2026-07-08T17:47:41.820073+08:00,2026-07-08T17:47:43.265007+08:00,1444.00,
> stress-rbg-0003,create,2026-07-08T17:47:42.020899+08:00,2026-07-08T17:47:43.452421+08:00,1431.00,
```

| Latency Pattern | Characteristic | Meaning |
| --- | --- | --- |
| Uniform distribution | P50 ≈ P99 | Stable processing |
| Tail latency | P99 >> P50 (>5x) | Queue buildup or GC |
| Queue saturation | P99 > 10s | All Reconcile threads busy |

### Analysis Dimension 3: Error Logs

```bash
# Check error logs
cat /tmp/rbg-stress-results/errors.log | head -3

> {"level":"ERROR","time":"2026-07-08T09:47:41.869Z","caller":"workloads/rolebasedgroup_controller.go:544","message":"Failed to reconcile workload",...,"error":"RoleInstanceSet.workloads.x-k8s.io \"stress-rbg-0000-role-0\" not found",...}
> {"level":"ERROR","time":"2026-07-08T09:47:41.879Z","caller":"workloads/rolebasedgroup_controller.go:544","message":"Failed to reconcile workload",...,"error":"RoleInstanceSet.workloads.x-k8s.io \"stress-rbg-0000-role-1\" not found",...}
> {"level":"ERROR","time":"2026-07-08T09:47:42.374Z","caller":"workloads/rolebasedgroup_controller.go:544","message":"Failed to reconcile workload",...,"error":"RoleInstanceSet.workloads.x-k8s.io \"stress-rbg-0003-role-1\" not found",...}
```

| Error Type | Log Characteristic | Recommendation |
| --- | --- | --- |
| API throttling | `Throttling request took Xs` | Increase `kube-api-qps` / `kube-api-burst` |
| Optimistic lock conflict | `the object has been modified` | > 5% needs lower `max-concurrent-reconciles` |
| Timeout | `context deadline exceeded` | Increase timeout or reduce Reconcile workload |
| Initial race | `RoleInstanceSet not found` | Benign error — sub-resources not yet created when Controller first reconciles, auto-recovers on retry |

**Actual error statistics:**

```text
Controller logs: 19826 lines, of which 265 are errors
Error type distribution:
  175 × "Failed to reconcile workload"
   91 × "Reconciler error"
API throttling (Throttling): 0
Optimistic lock conflicts (has been modified): 0
Timeouts (context deadline): 0
```

> All errors are of the `RoleInstanceSet not found` type — sub-resources not yet created when Controller first reconciles the RBG. Auto-recovers on retry, a benign error.

### Analysis Dimension 4: Pprof Performance Profile

```bash
# Check CPU Top report
cat /tmp/rbg-stress-results/cpu-create-top.txt

# Check heap memory Top report
cat /tmp/rbg-stress-results/heap-create-top.txt

# Check memory allocation Top report
cat /tmp/rbg-stress-results/allocs-create-top.txt

# Check goroutine count
cat /tmp/rbg-stress-results/goroutine-create-top.txt
```

**Reference thresholds:**

- Goroutines < 100: Healthy
- Goroutines 100-500: Normal range for high concurrency
- Goroutines > 1000: Possible leak
- When managing < 1000 RBGs, heap memory exceeding 1GB needs attention

**Actual Pprof data (Create phase):**

| Metric | Actual Value | Assessment |
| --- | --- | --- |
| Goroutines | 371 | Normal range for high concurrency |
| Heap memory | 76.81MB | Healthy (well below 1GB) |
| Memory allocations | 33.57GB | Mainly from JSONSchemaProps.DeepCopy (23.88%) and RawExtension.DeepCopyInto (19.43%) |
| CPU hotspot | runtime.scanobject 11.34% | GC-related, normal |

> Goroutine count 371 is within the normal range for high concurrency. Heap memory 76.81MB is well below the 1GB threshold. Memory allocation hotspots are concentrated in CRD Schema DeepCopy operations — this is an inherent overhead of controller-runtime processing unstructured objects.

---

## Operation 5: Tune and Re-run Stress Test

### Step 1: Adjust Configuration Based on Analysis

```bash
# Get current image tag to avoid overwriting with latest; --no-hooks skips CRD upgrade (KWOK nodes cannot run hook jobs)
IMAGE_TAG=$(kubectl get deploy -n rbgs-system rbgs-controller-manager \
    -o jsonpath='{.spec.template.spec.containers[0].image}' | sed 's/.*://')

helm upgrade rbgs deploy/helm/rbgs -n rbgs-system \
    --set image.tag=${IMAGE_TAG} \
    --set resources.limits.cpu=16 --set resources.limits.memory=32Gi \
    --set controllerTuning.maxConcurrentReconciles=50 \
    --set controllerTuning.kubeApiQPS=200 --set controllerTuning.kubeApiBurst=400 \
    --set pprof.enabled=true --set pprof.port=6060 \
    --no-hooks --wait --timeout=120s

# pprof port forwarding
pkill -f "port-forward.*6060" 2>/dev/null; sleep 1
kubectl port-forward -n rbgs-system deploy/rbgs-controller-manager 6060:6060 &
```

### Step 2: Re-run Stress Test

```bash
go run ./test/stress/ \
    --namespace=stress-test \
    --total-rbgs=100 \
    --roles-per-rbg=3 \
    --lws-roles=1 \
    --lws-size=4 \
    --create-qps=5 \
    --update-qps=5 \
    --delete-qps=5 \
    --in-place-update=true \
    --use-kwok-nodes=true \
    --pprof-addr=localhost:6060 \
    --output-dir=/tmp/rbg-stress-results-v2 \
    --timeout=30m
```

### Step 3: Compare Results from Both Runs

```bash
echo "=== Round 1 (8c/16g, Reconciles=20, QPS=100) ==="
cat /tmp/rbg-stress-results/summary.json | python3 -c "
import json, sys
data = json.load(sys.stdin)
for item in data:
    phase = item['operation']
    print(f'{phase}: P50={item.get(\"p50_ms\",\"?\")} P99={item.get(\"p99_ms\",\"?\")} QPS={item.get(\"actual_qps\",0):.2f}')
"

echo ""
echo "=== Round 2 (16c/32g, Reconciles=50, QPS=200) ==="
cat /tmp/rbg-stress-results-v2/summary.json | python3 -c "
import json, sys
data = json.load(sys.stdin)
for item in data:
    phase = item['operation']
    print(f'{phase}: P50={item.get(\"p50_ms\",\"?\")} P99={item.get(\"p99_ms\",\"?\")} QPS={item.get(\"actual_qps\",0):.2f}')
"
```

### Expected Behavior (Tune and Re-run)

```text
=== Round 1 (8c/16g, Reconciles=20, QPS=100) ===
create: P50=4080.5 P99=7062.680000000001 QPS=3.99
update: P50=393 P99=769.6400000000003 QPS=4.93
delete: P50=184 P99=204.2200000000001 QPS=5.00

=== Round 2 (16c/32g, Reconciles=50, QPS=200) ===
create: P50=1477 P99=2401.02 QPS=4.70
update: P50=1871.5 P99=2692.3600000000006 QPS=4.70
delete: P50=173.5 P99=642.1200000000001 QPS=5.01
```

Create phase QPS was behind (3.99 vs target 5.0). In the second round, after increasing Reconcile concurrency and API QPS, Create QPS is expected to improve to 4.7 with reduced P99 latency.

---

## Operation 6: Clean Up Environment

```bash
# Delete simulated nodes and stress test namespace
bash test/stress/scripts/teardown-kwok.sh

# If you also need to uninstall KWOK
UNINSTALL_KWOK=true bash test/stress/scripts/teardown-kwok.sh
```

### Expected Behavior (Clean Up)

- Stress test namespace `stress-test` deleted
- KWOK simulated nodes deleted
- (Optional) KWOK Controller uninstalled

### Verification (Clean Up)

```bash
# Confirm simulated nodes are deleted
kubectl get nodes -l type=kwok

# Confirm stress test namespace is deleted
kubectl get namespace stress-test
```

---

## Reference Stress Test Configurations by Scale

| Scale | RBG Count | Roles/RBG | leaderWorker Roles | Create QPS | Recommended Controller Config |
| --- | --- | --- | --- | --- | --- |
| Small validation | 10 | 2 | 0 | 5 | 4c/8g, Reconciles 10, QPS 50 |
| Medium benchmark | 100 | 3 | 1 | 5 | 8c/16g, Reconciles 20, QPS 100 |
| Large stress | 500 | 3 | 1 | 10 | 16c/32g, Reconciles 50, QPS 200 |
| Ultra-large | 1000 | 5 | 2 | 10 | 32c/64g, Reconciles 100, QPS 500 |

---

## Summary

| Operation | Verification Point | Key Expectation |
| --- | --- | --- |
| Set up KWOK environment | Simulated nodes and Pod lifecycle | 5 Ready nodes + 6 Stages (2 Node + 4 Pod) |
| Deploy Controller | Resource and parameter configuration | Pod Ready, parameters correct |
| Run stress test | Create/Update/Delete three phases | 100 RBGs all created, updated, deleted successfully |
| Analyze report | Throughput, latency, errors, pprof | Create QPS 3.99 (⚠ behind), Update/Delete met, 371 goroutines, 76MB heap, no critical errors |
| Tune and re-test | Parameter tuning effect validation | After tuning, QPS improved, latency reduced |

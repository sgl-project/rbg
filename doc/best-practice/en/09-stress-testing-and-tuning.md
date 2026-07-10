# Large-Scale Cluster Resource Estimation, Configuration, and Stress Testing
## Overview
Before deploying RBG Controller to a large-scale cluster (hundreds to thousands of RBG instances), three questions need to be answered:

1. **How many resources does the Controller need?** How to configure CPU, memory, concurrent Reconciles, and API QPS
2. **Can the Controller handle the target scale?** At the target RBG count and operation QPS, do latency and throughput meet expectations
3. **Where is the bottleneck?** Is it insufficient Reconcile concurrency, API throttling, or CPU/memory bottleneck

RBG provides a complete stress testing toolchain to answer these questions:

+ `/stress-test`** Skill**: Claude Code's built-in stress testing Skill, completing the entire workflow from environment setup to result analysis through conversational interaction
+ **Stress test client** (`test/stress/`): A Go-written stress testing driver, supporting rate-controlled load testing across Create/Update/Delete three phases
+ **KWOK simulation**: Uses [KWOK](https://kwok.sigs.k8s.io/) to simulate thousands of Pods in a real cluster without actual GPU resources

```plain
┌──────────────────────────────────────────────────────────────────┐
│                     Stress Testing Toolchain Architecture          │
│                                                                  │
│  /stress-test Skill                                              │
│  ┌───────────────┐  ┌──────────────┐  ┌──────────────┐          │
│  │ Conversational  │─→│ Automated    │─→│ AI-driven    │          │
│  │ interaction     │  │ orchestration │  │ analysis     │          │
│  │ Configure test  │  │ Env setup     │  │ Bottleneck   │          │
│  │ scenarios       │  │ Test execution│  │ identification│          │
│  └───────────────┘  │ Profiling    │  │ Tuning       │          │
│                     │ Report gen   │  │ suggestions   │          │
│                     └──────┬───────┘  └──────────────┘          │
│                            │                                     │
│                     ┌──────▼───────┐                             │
│                     │ Go stress    │                             │
│                     │ test client  │                             │
│                     │ (test/stress)│                             │
│                     └──────┬───────┘                             │
│                            │                                     │
│              ┌─────────────┼─────────────┐                       │
│              ▼             ▼             ▼                       │
│       ┌────────────┐ ┌─────────┐ ┌────────────┐                 │
│       │ Controller │ │  KWOK   │ │  Pprof     │                 │
│       │ (real Pod) │ │ (sim.)  │ │ (profiling)│                 │
│       └────────────┘ └─────────┘ └────────────┘                 │
└──────────────────────────────────────────────────────────────────┘
```

## Prerequisites
+ Kubernetes cluster version >= 1.24
+ RBG CRD installed (see [Installation Guide](https://github.com/sgl-project/rbg))
+ Local tools: `helm`, `kubectl`, `go`, `curl`
+ (Optional) Controller image supports `--enable-pprof` (for performance profile collection)

---

## Background: Why Stress Testing Is Needed
RBG Controller manages the full lifecycle of all RBG instances — creating sub-resources (Deployment/LeaderWorkerSet/Service), monitoring Pod status, executing rolling updates, coordinating role dependencies. Each RBG instance's Reconcile loop involves multiple API Server interactions. When managing hundreds of RBGs, the Controller's processing capacity directly affects:

| Impact Dimension | Description |
| --- | --- |
| **First deployment latency** | End-to-end time from `kubectl apply` to all Pods Ready |
| **Rolling update speed** | Total time for all instances to complete updates in large-scale update scenarios |
| **Status sync timeliness** | Time for Controller to perceive and respond after Pod status changes |
| **API Server pressure** | Controller's read/write frequency to API Server — too high causes throttling |


Without stress testing data, configuring Controller parameters based on experience often leads to two extremes:

+ **Over-provisioning**: Allocating too much CPU/memory, wasting cluster resources
+ **Under-provisioning**: Reconcile queue buildup, operation latency spikes, update timeouts

The goal of stress testing is to find the **optimal configuration for the target scale** — using the fewest resources while meeting latency requirements.

---

## Stress Testing Architecture: KWOK Simulation
The stress test uses [KWOK](https://kwok.sigs.k8s.io/) (Kubernetes WithOut Kubelet) to simulate workload Pods in a real cluster without actual GPU or compute resources.

### How It Works
```plain
Real Nodes                              KWOK Simulated Nodes
┌─────────────────────────┐         ┌─────────────────────────┐
│ RBG Controller Pod      │         │ kwok-node-0..N           │
│ (Helm deployed, real)    │         │ (Virtual nodes created   │
│                         │         │  by KWOK)                │
│ kwok Controller Pod     │         │                          │
│ (Simulates Pod lifecycle)│         │ Workload Pods            │
│                         │         │ (Auto-simulates Ready)   │
└─────────────────────────┘         └─────────────────────────┘
         │                                    ▲
         │ Watch Pod events                    │ Scheduled to simulated nodes
         └────────────────────────────────────┘
```

KWOK simulates Pod lifecycle through the Stage CRD.

The `setup-kwok.sh` script installs two sets of Stages:

- **Official `stage-fast` chart**: Provides Node-related Stages (`node-heartbeat-with-lease`, `node-initialize`), simulating node heartbeat and initialization
- **Project-custom Pod lifecycle Stages** (`test/stress/templates/kwok-stage.yaml`): Overrides official default Pod Stages, providing more precise delay control

Pod lifecycle Stages are as follows:

| Stage | Behavior | Delay |
| --- | --- | --- |
| `pod-initialize` | Assigns PodIP, sets to Pending | Immediate |
| `pod-running` | Transitions to Running state | 500ms |
| `pod-ready` | Sets Ready condition to True | 1000ms |
| `pod-delete` | Deletes Pod | 500ms |


This means each simulated Pod takes about 1.5 seconds from creation to Ready, simulating the readiness process of large-scale Pods.

### Simulated Node Configuration
Each KWOK simulated node's default capacity:

| Resource | Default | Description |
| --- | --- | --- |
| CPU | 128 cores | Simulated CPU capacity, used for scheduling decisions |
| Memory | 512Gi | Simulated memory capacity |
| Pods | 1000 | Maximum Pods per node |


> **Note**: The simulated node's capacity is "virtual" — it only affects the scheduler's scheduling decisions. Actual Pods do not run real containers and do not consume these resources.
>

---

## Resource Estimation: How Many Resources Does the Controller Need
Before running stress tests, you need to estimate the Controller's resource requirements for the target scale. The following is a resource estimation method based on RBG Controller's architecture.

### Compute Resource Scale
Each RBG instance's management overhead depends on the number of roles and role types:

| Sub-resource | API Operations per Role per Reconcile |
| --- | --- |
| Role sub-resources (Deployment/LWS) | 1 Get + 1 Create/Update |
| Service (service discovery) | 1 Get + 1 Create/Update |
| RoleInstanceSet | 1 Get + 1 Create/Update |
| Status update | 1 Patch |
| **Total** | **~4-8 API operations/role/Reconcile** |


Assuming 500 RBGs are managed, each with 3 roles:

```plain
Total roles = 500 × 3 = 1500
API operations/round = 1500 × 6 (average) = 9000
```

### Parameter Configuration Guide
| Parameter | Description | Recommended Formula | Example (500 RBG × 3 roles) |
| --- | --- | --- | --- |
| `max-concurrent-reconciles` | Parallel Reconcile worker threads | `total_roles / target_P99_seconds` | 1500 / 30 = 50 |
| `kube-api-qps` | Sustained API Server request rate | `max-concurrent-reconciles × 3` | 50 × 3 = 150 |
| `kube-api-burst` | API Server burst request limit | `kube-api-qps × 2` | 150 × 2 = 300 |
| CPU | Controller compute resources | Based on concurrency and API call volume | 8-16 cores |
| Memory | Controller memory resources | Informer cache proportional to object count | 16-32Gi |


### Recommended Configuration Tiers
| Scale | RBG Count | Total Roles | Recommended Configuration |
| --- | --- | --- | --- |
| Small | < 50 | < 150 | CPU 4 cores / Memory 8Gi / Reconciles 10 / QPS 50 / Burst 100 |
| Medium | 50-200 | 150-600 | CPU 8 cores / Memory 16Gi / Reconciles 20 / QPS 100 / Burst 200 |
| Large | 200-500 | 600-1500 | CPU 16 cores / Memory 32Gi / Reconciles 50 / QPS 200 / Burst 400 |
| Ultra-large | 500-1000+ | 1500+ | CPU 32 cores / Memory 64Gi / Reconciles 100 / QPS 500 / Burst 1000 |


> **Note**: The above configurations are starting points. The actual optimal configuration needs to be validated through stress testing — which is exactly what the subsequent sections of this document cover.
>

### Controller Key Parameter Description
| Parameter | Description | Impact |
| --- | --- | --- |
| `max-concurrent-reconciles` | Number of worker threads for parallel Reconcile processing | Higher values mean higher throughput, but increased CPU and memory consumption, and may exacerbate API Server conflicts |
| `kube-api-qps` | Sustained request rate limit to API Server | Too low causes client-side throttling (`Throttling request took Xs`), too high puts pressure on API Server |
| `kube-api-burst` | Burst request capacity above QPS | Allows short-term request spikes, avoids throttling during Reconcile peaks |


---

## Quick Start: Using the /stress-test Skill
`/stress-test` is a Claude Code built-in Skill that completes the entire stress testing workflow through conversational interaction. Enter `/stress-test` in Claude Code to launch it.

### Usage Flow
```plain
/stress-test
```

The Skill will sequentially ask for the following configuration items:

| Configuration Item | Options | Description |
| --- | --- | --- |
| **Test mode** | Manual mode / Auto-tuning mode | Manual mode runs one round of stress testing and outputs a report; auto-tuning mode runs multiple rounds and recommends the optimal configuration |
| **Controller resources** | 4c/8g, 8c/16g, 16c/32g, 32c/64g | Controller Pod resource limits |
| **Controller parameters** | Reconciles / QPS / Burst | Controller runtime parameters |
| **Workload template** | Roles / LWS roles / LWS size / Total RBGs | Simulated RBG workload structure |
| **Operation QPS** | Create / Update / Delete QPS | Submission rate for each phase |
| **Update strategy** | In-place update / Rebuild update | Update method for the Update phase |


The Skill automatically executes the following phases:

```plain
┌──────────────────────────────────────────────────────────────────┐
│                    /stress-test Execution Flow                     │
│                                                                  │
│  Phase 1: Environment Setup                                       │
│  ├── Deploy KWOK Controller + Stage (simulate Pod lifecycle)      │
│  ├── Create KWOK simulated nodes (default 5, each 128 CPU / 512Gi)│
│  └── Deploy/upgrade RBG Controller via Helm (configure resources, │
│      parameters, pprof)                                           │
│                                                                  │
│  Phase 2: Execute Stress Test                                     │
│  ├── Create phase: Create N RBGs at target QPS, wait for all Ready│
│  ├── Update phase: Update all RBGs at target QPS, wait for all done│
│  └── Delete phase: Delete all RBGs at target QPS, wait for cleared│
│                                                                  │
│  Phase 3: Profiling (optional)                                    │
│  ├── CPU Profile: 30s CPU profile per phase (parallel with test)  │
│  ├── Heap/Allocs/Goroutine: Memory snapshot after each phase      │
│  └── Generate Top-N text reports                                  │
│                                                                  │
│  Phase 4: Report Generation                                       │
│  ├── HTML report: latency distribution charts, error classification│
│  ├── summary.json: P50/P90/P99/Max/Avg/actual QPS                │
│  ├── timing-*.csv: precise latency data per operation             │
│  └── errors.log + controller-full.log: Controller logs            │
│                                                                  │
│  Phase 5: AI-Driven Analysis                                      │
│  ├── Throughput gap analysis (actual QPS vs target QPS)           │
│  ├── Latency distribution classification (uniform / tail / queue) │
│  ├── Bottleneck identification (Reconciles / API QPS / CPU / mem) │
│  └── Specific tuning suggestions (param name + recommended value  │
│      + rationale)                                                 │
└──────────────────────────────────────────────────────────────────┘
```

---

## Manual Stress Testing
If not using the Skill, you can also manually execute each step of the stress test.

### Step 1: Set Up KWOK Environment
```bash
# Install KWOK Controller + Stage (official stage-fast + project-custom Pod lifecycle), create 5 simulated nodes
FAKE_NODE_COUNT=5 bash test/stress/scripts/setup-kwok.sh

# Verify simulated nodes are ready
kubectl get nodes -l type=kwok
```

Customize simulated node capacity:

```bash
FAKE_NODE_COUNT=10 \
FAKE_NODE_CPU=256 \
FAKE_NODE_MEMORY=1024Gi \
FAKE_NODE_PODS=2000 \
bash test/stress/scripts/setup-kwok.sh
```

### Step 2: Deploy Controller
```bash
# Deploy Controller, configure resources, parameters, and pprof
CONTROLLER_CPU=8 CONTROLLER_MEMORY=16Gi \
MAX_RECONCILES=20 KUBE_API_QPS=100 KUBE_API_BURST=200 \
PPROF_ENABLED=true \
bash test/stress/scripts/deploy-controller.sh
```

The deploy script will:

1. Build the Controller image
2. Deploy (or upgrade) the Controller via Helm, setting resource limits and runtime parameters
3. Wait for the Controller Pod to be ready
4. Establish pprof port forwarding (`localhost:6060`)

### Step 3: Run Stress Test
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

#### Stress Test Client Parameters
| Parameter | Type | Default | Description |
| --- | --- | --- | --- |
| `--namespace` | string | `stress-test` | Stress test namespace |
| `--total-rbgs` | int | 10 | Total RBG instances to create |
| `--roles-per-rbg` | int | 2 | Number of roles per RBG |
| `--lws-roles` | int | 0 | Number of roles using LeaderWorkerPattern |
| `--lws-size` | int | 4 | Number of Pods per LWS role |
| `--create-qps` | float | 5 | Create phase submission rate (ops/sec) |
| `--update-qps` | float | 5 | Update phase submission rate (ops/sec) |
| `--delete-qps` | float | 5 | Delete phase submission rate (ops/sec) |
| `--in-place-update` | bool | false | Use InPlaceIfPossible strategy (true) or RecreatePod (false) |
| `--use-kwok-nodes` | bool | true | Schedule to KWOK simulated nodes |
| `--pprof-addr` | string | "" | Controller pprof address, empty skips Profiling |
| `--output-dir` | string | `/tmp/rbg-stress-results` | Output directory |
| `--timeout` | duration | 30m | Total timeout |
| `--max-concurrent-waiters` | int | 0 | Max concurrent waiter goroutines (0 = unlimited) |
| `--controller-namespace` | string | `rbgs-system` | Controller namespace |
| `--controller-label` | string | `control-plane=rbgs-controller` | Controller Pod label selector |


### Step 4: View Results
```bash
# Open HTML report
open /tmp/rbg-stress-results/report.html

# View JSON summary
cat /tmp/rbg-stress-results/summary.json
```

### Step 5: Clean Up Environment
```bash
# Delete simulated nodes and stress test namespace
bash test/stress/scripts/teardown-kwok.sh

# Also uninstall KWOK
UNINSTALL_KWOK=true bash test/stress/scripts/teardown-kwok.sh
```

---

## Stress Test Output and Result Analysis
The stress test client generates the following files in the output directory:

### Output Files
| File | Description |
| --- | --- |
| `report.html` | **Main report** — open in browser, includes latency charts, error classification, pprof data |
| `summary.json` | Machine-readable performance stats (P50/P90/P99/QPS per phase) |
| `timing-create.csv` | Per-operation latency data for Create phase |
| `timing-update.csv` | Per-operation latency data for Update phase |
| `timing-delete.csv` | Per-operation latency data for Delete phase |
| `controller-full.log` | Complete Controller logs during stress test |
| `errors.log` | Filtered error logs |
| `cpu-{phase}.prof` | CPU pprof binary profile |
| `heap-{phase}.prof` | Heap memory pprof binary profile |
| `allocs-{phase}.prof` | Memory allocation pprof binary profile |
| `goroutine-{phase}.prof` | Goroutine pprof binary profile |
| `*-top.txt` | Human-readable pprof Top-N text reports |


### Analysis Dimension 1: Throughput and Latency
Read `summary.json`, compare each phase's actual QPS with target QPS:

```plain
┌──────────────────────────────────────────────────────────────────┐
│  Throughput Gap Analysis                                          │
│                                                                  │
│  Create phase:                                                    │
│  Target QPS: 10        Actual QPS: 6.95      Gap: -30.5%          │
│  Assessment: ⚠ Behind — Controller processing capacity insufficient│
│                                                                  │
│  Update phase:                                                    │
│  Target QPS: 10        Actual QPS: 10.0      Gap: 0%              │
│  Assessment: ✓ Met                                                │
└──────────────────────────────────────────────────────────────────┘
```

**Judgment rule**: Actual QPS < 90% of target QPS indicates the Controller's processing capacity cannot keep up with the submission rate.

### Analysis Dimension 2: Latency Distribution Patterns
| Latency Pattern | Characteristic | Meaning |
| --- | --- | --- |
| Uniform distribution | P50 ≈ P99 | Stable processing, no bottleneck |
| Tail latency | P99 >> P50 (>5x) | Queue buildup or GC pauses |
| Queue saturation | P99 > 10s | All Reconcile worker threads busy |
| Anomalous outlier | Max >> P99 | API Server jitter or Leader election |


### Analysis Dimension 3: Latency Trend
Read operation timestamps and latencies from `timing-*.csv`, observe whether latency increases monotonically over time:

+ **Stable latency**: Controller processing capacity is sufficient, queue not building up
+ **Linearly increasing latency**: Submission rate exceeds processing capacity, Reconcile queue continuously expanding

### Analysis Dimension 4: Controller Logs
Error classification and root causes in `errors.log`:

| Error Type | Log Characteristic | Root Cause | Recommendation |
| --- | --- | --- | --- |
| API throttling | `Throttling request took Xs` | `kube-api-qps` too low | Increase `kube-api-qps` and `kube-api-burst` |
| Optimistic lock conflict | `the object has been modified` | Concurrent Reconcile competing for same object | < 1% normal; > 5% needs lower `max-concurrent-reconciles` |
| Timeout | `context deadline exceeded` | Single Reconcile taking too long | Increase timeout or reduce single Reconcile workload |
| Workload type | `unsupported workload type` | Role missing `role-workload-type` annotation | Stress test template needs correct annotation |
| Panic | `panic` / `runtime error` | Controller internal error | Critical issue, needs code investigation |


**Error rate reference**: < 1% acceptable (transient conflicts), > 5% indicates a systemic problem.

### Analysis Dimension 5: Pprof Performance Profile
If pprof is enabled (`--pprof-addr`), you can analyze performance bottlenecks through Top-N reports.

**CPU profile** (`cpu-{phase}-top.txt`):

| CPU Consumption Category | Description | Tuning Direction |
| --- | --- | --- |
| `crypto/tls`, `net/http`, `runtime.*` | Infrastructure overhead | Normal, no tuning needed |
| Reconciler functions, dependency resolution | Controller business logic | `max-concurrent-reconciles` may be the bottleneck |
| `encoding/json`, `Unmarshal`, `Marshal` | Object serialization | High object flow volume, consider reducing unnecessary Updates |


**Heap memory profile** (`heap-{phase}-top.txt`):

+ `runtime.allocm` → Goroutine stack memory, proportional to `max-concurrent-reconciles`
+ `Unmarshal` → Informer cache deserializing Kubernetes objects
+ When managing < 1000 RBGs, heap memory exceeding 1GB needs attention

**Goroutine profile** (`goroutine-{phase}-top.txt`):

| Goroutine Count | Assessment |
| --- | --- |
| < 100 | Healthy |
| 100-500 | Normal range for high-concurrency Controllers |
| > 1000 | Possible goroutine leak, needs investigation |


---

## Tuning Decision Tree
Based on stress test results, follow this decision tree to locate bottlenecks and tune:

```plain
┌──────────────────────────────────────────────────────────────────┐
│  Stress Test Result Analysis → Tuning Decisions                    │
│                                                                  │
│  Actual QPS < Target QPS?                                        │
│  ├── Yes                                                          │
│  │   ├── API throttling logs? ──→ Increase kube-api-qps / burst  │
│  │   ├── High CPU usage? ──→ Increase CPU resource limit          │
│  │   └── Neither? ──→ Increase max-concurrent-reconciles          │
│  └── No → Controller capacity is adequate, check latency dist.   │
│                                                                  │
│  P99 latency too high?                                            │
│  ├── P99 >> P50 (tail latency) ──→ Reconcile queue saturated,   │
│  │                                  increase concurrency          │
│  ├── Max >> P99 (outlier) ──→ API Server jitter, check cluster   │
│  │                              load                              │
│  └── Latency increasing linearly ──→ Submission rate exceeds      │
│  │                                    processing capacity,        │
│  │                                    lower QPS or increase concurrency│
│                                                                  │
│  Conflict errors > 5%?                                           │
│  └── Yes ──→ Lower max-concurrent-reconciles, reduce contention  │
│                                                                  │
│  Memory continuously growing?                                    │
│  └── Check if Informer cache is unbounded, consider limiting      │
│      Watch scope                                                  │
└──────────────────────────────────────────────────────────────────┘
```

### Tuning Formula Reference
| Scenario | Formula | Description |
| --- | --- | --- |
| Insufficient Reconcile concurrency | `max-concurrent-reconciles = total_roles / target_P99_seconds` | Ensure all roles processed within target latency |
| API QPS throttling | `kube-api-qps >= max-concurrent-reconciles × 3` | ~3 API calls per Reconcile |
| Insufficient API Burst | `kube-api-burst >= kube-api-qps × 2` | Allow burst traffic through |


---

## Auto-Tuning Mode
Using the `/stress-test` Skill's auto-tuning mode, the Skill automatically runs multiple rounds of stress testing, gradually approaching the optimal configuration:

| Round | Strategy | Purpose |
| --- | --- | --- |
| Round 1 | User's initial configuration | Establish baseline |
| Round 2 | Increase `kube-api-qps` / `kube-api-burst` | Test if API throttling is the bottleneck |
| Round 3 | Increase `max-concurrent-reconciles` | Test if worker thread count is the bottleneck |
| Round 4 | Combine optimal settings from previous rounds | Validate optimal configuration |


The Skill compares the following metrics across rounds, recommending the final configuration:

+ Lowest P99 latency
+ No OOM or excessive memory growth
+ Fewest API throttling errors
+ Best resource efficiency (latency performance per CPU core)

---

## Complete Workflow
The following are the operational steps to complete a full stress test from scratch:

```plain
Step 1: Launch stress test (recommended approach)
    $ claude    # Launch Claude Code
    > /stress-test
    # Follow prompts to select test scenarios and parameters

Step 1': Or execute manually
    $ FAKE_NODE_COUNT=10 bash test/stress/scripts/setup-kwok.sh
    $ CONTROLLER_CPU=8 CONTROLLER_MEMORY=16Gi \
      MAX_RECONCILES=20 KUBE_API_QPS=100 KUBE_API_BURST=200 \
      PPROF_ENABLED=true \
      bash test/stress/scripts/deploy-controller.sh
    $ kubectl port-forward -n rbgs-system \
      deploy/rbgs-controller-manager 6060:6060 &
    $ go run ./test/stress/ \
      --total-rbgs=100 --roles-per-rbg=3 \
      --lws-roles=1 --lws-size=4 \
      --create-qps=5 --update-qps=5 --delete-qps=5 \
      --in-place-update=true \
      --pprof-addr=localhost:6060

Step 2: View report
    $ open /tmp/rbg-stress-results/report.html
    $ cat /tmp/rbg-stress-results/summary.json

Step 3: Adjust Controller configuration based on analysis
    $ CONTROLLER_CPU=16 CONTROLLER_MEMORY=32Gi \
      MAX_RECONCILES=50 KUBE_API_QPS=200 KUBE_API_BURST=400 \
      PPROF_ENABLED=true \
      bash test/stress/scripts/deploy-controller.sh

Step 4: Re-run stress test to validate
    $ go run ./test/stress/ ...

Step 5: Clean up environment
    $ bash test/stress/scripts/teardown-kwok.sh
```

### Reference Stress Test Configurations by Scale
| Scale | RBG Count | Roles/RBG | LWS Roles | Create QPS | Recommended Controller Config |
| --- | --- | --- | --- | --- | --- |
| Small validation | 10 | 2 | 0 | 5 | 4c/8g, Reconciles 10, QPS 50 |
| Medium benchmark | 100 | 3 | 1 | 5 | 8c/16g, Reconciles 20, QPS 100 |
| Large stress | 500 | 3 | 1 | 10 | 16c/32g, Reconciles 50, QPS 200 |
| Ultra-large | 1000 | 5 | 2 | 10 | 32c/64g, Reconciles 100, QPS 500 |


## Related Documents
+ [Deploying Inference Services with RBG](#)
+ [Configuring Rolling Update Strategies](#)
+ [In-Place Update and In-Place Scheduling](#)
+ [Configuring Autoscaling Strategies for RBG Services](#)

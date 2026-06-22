---
name: stress-test
description: RBG controller stress test - deploy kwok in existing cluster, run create/update/delete load tests with real controller pod, collect pprof profiling and logs, generate analysis report with recommendations
---

# RBG Controller Stress Test

This skill orchestrates a full stress test of the RBG controller. It supports two modes:
- **Manual mode**: User defines all parameters, skill runs tests and outputs analysis report
- **Auto-tuning mode**: User defines the workload scenario, skill runs multiple rounds and recommends optimal controller resource/QPS/burst configuration

## Architecture

The stress test uses **in-cluster kwok** to simulate workload pods:
- kwok controller + stage-fast deployed via Helm in the existing cluster
- Fake nodes created with `type=kwok` label and `kwok.x-k8s.io/node` taint
- RBG controller runs as a real Pod (via Helm) on real nodes with configurable resources
- Stress test workload pods schedule to fake nodes and are simulated as Ready by kwok
- This allows testing controller performance without needing real GPU/compute resources

## Prerequisites

- A Kubernetes cluster accessible via current kubeconfig
- `helm`, `kubectl`, `go`, `curl` available in PATH
- RBG CRDs and controller already deployed (or will be deployed by this skill)

## Workflow

### Phase 1: Ask User for Scenario

Use AskUserQuestion to gather parameters:

**Question 1 - Test Mode:**
- Manual mode (user sets all params, get report)
- Auto-tuning mode (auto-iterate to find optimal config)

**Question 2 - Controller Resources (affects scheduling and memory):**
- 4c/8g (small)
- 8c/16g (medium, recommended)
- 16c/32g (large)
- 32c/64g (extra-large)

**Question 3 - Controller Parameters:**
- max-concurrent-reconciles: 5, 10, 20, 50
- kube-api-qps: 20 (default), 50, 100, 200
- kube-api-burst: 30 (default), 100, 200, 400

**Question 4 - RBG Workload Template:**
- Roles per RBG: 2, 3, 5
- LeaderWorkerPattern roles: 0, 1, 2
- LWS size (pods per instance): 2, 4, 8
- Total RBG instances: 10, 50, 100, 500, 1000

**Question 5 - QPS for Operations:**
- Create QPS: 1, 5, 10, 20
- Update QPS: 1, 5, 10, 20
- Delete QPS: 1, 5, 10, 20

**Question 6 - In-place Update:**
- Yes (update image to trigger InPlaceIfPossible)
- No (update annotations to trigger RollingUpdate with RecreatePod)

### Phase 2: Setup Environment

1. **Deploy kwok in-cluster** (installs kwok controller + stage-fast + creates fake nodes):
   ```bash
   FAKE_NODE_COUNT=5 bash test/stress/scripts/setup-kwok.sh
   ```

2. **Deploy/upgrade RBG controller** via Helm with user-specified resources and params:
   ```bash
   CONTROLLER_CPU=8 CONTROLLER_MEMORY=16Gi \
   MAX_RECONCILES=10 KUBE_API_QPS=50 KUBE_API_BURST=100 \
   PPROF_ENABLED=true \
   bash test/stress/scripts/deploy-controller.sh
   ```

3. **Verify controller is ready**:
   ```bash
   kubectl rollout status deploy/rbgs-controller-manager -n rbgs-system --timeout=120s
   ```

### Phase 3: Run Stress Test

Execute the Go stress test client:
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

The stress client runs three phases sequentially: Create -> Update -> Delete.
Each phase collects CPU profile (concurrently) and timing data to CSV files.

### Phase 4: Profiling (integrated, requires pprof-enabled controller)

Pprof collection is integrated into the Go stress client. When `--pprof-addr` is specified:
1. CPU profile (30s) is collected **during** each phase (concurrent with workload)
2. Heap, allocs, goroutine snapshots are collected **after** each phase
3. Top-N text reports are generated via `go tool pprof`

Prerequisites:
```bash
# Setup port-forward to controller's pprof port
kubectl port-forward -n rbgs-system deploy/rbgs-controller-manager 6060:6060 &
```

Then pass `--pprof-addr=localhost:6060` to the stress client.

If pprof is not available (controller image without pprof support), omit the `--pprof-addr` flag.

Alternatively, use the standalone script for manual collection:
```bash
PHASE=create OUTPUT_DIR=/tmp/rbg-stress-results \
bash test/stress/scripts/collect-profiling.sh
```

### Phase 5: Report Generation (automatic)

The Go stress test client automatically:
1. Collects controller logs (scoped to the test period via `--since-time`)
2. Classifies errors (reconcile, conflict, throttle, timeout, panic, workload-type)
3. Loads pprof top-N text reports if available
4. Generates an HTML report at `{output-dir}/report.html`

The HTML report includes:
- **Test Scenario**: all configured parameters
- **Controller Configuration**: image, resources, startup args, node
- **Issues & Findings**: auto-detected problems (high latency, throttling, panics, etc.)
- **Performance Results Table**: P50/P90/P99/Max/Avg per phase, actual QPS achieved
- **Per-phase Latency Distribution**: visual bar chart with hover details
- **Controller Log Analysis**: classified error counts + sample errors
- **Profiling (pprof)**: CPU top functions, heap allocators, allocs, goroutine stacks

Output files:
```
{output-dir}/
├── report.html              # Main HTML report (open in browser)
├── summary.json             # Machine-readable stats
├── timing-create.csv        # Raw create latencies
├── timing-update.csv        # Raw update latencies
├── timing-delete.csv        # Raw delete latencies
├── controller-full.log      # Full controller logs during test
├── errors.log               # Filtered error lines
├── cpu-{phase}.prof         # CPU pprof binary (if pprof enabled)
├── heap-{phase}.prof        # Heap pprof binary
├── allocs-{phase}.prof      # Allocs pprof binary
├── goroutine-{phase}.prof   # Goroutine pprof binary
├── cpu-{phase}-top.txt      # CPU top-N text report
├── heap-{phase}-top.txt     # Heap top-N text report
├── allocs-{phase}-top.txt   # Allocs top-N text report
└── goroutine-{phase}-top.txt # Goroutine top-N text report
```

### Phase 6: Model-Driven Analysis

After the Go client produces output files, perform expert analysis by reading the output data. Present the analysis as a structured report to the user.

**IMPORTANT**: This is not just a pass-through of the data — you must apply Kubernetes controller domain knowledge to interpret the results and produce actionable recommendations.

Read the following files from `{output-dir}` and analyze each dimension:

#### 6A. Performance & QPS Analysis

Read `{output-dir}/summary.json` and `{output-dir}/timing-create.csv`.

Analyze:
1. **Throughput gap**: For each phase, compare `actual_qps` against the target QPS (from `--create-qps`/`--update-qps`/`--delete-qps`). Calculate the percentage gap. If actual < 90% of target, the controller is falling behind.

2. **Latency profile**: Classify the latency pattern:
   - P50 ≈ P99 → uniform, healthy processing
   - P99 >> P50 (>5x) → tail latency, likely queue buildup or GC pauses
   - P99 > 10s → severe queue saturation, reconcile workers are backlogged
   - Max >> P99 → outliers, possibly due to leader election or API server hiccups

3. **Latency trend**: Read the timing CSV and check if latency increases over time (monotonically growing = reconcile queue saturation, the submission rate exceeds the controller's processing capacity).

4. **Phase comparison**: Compare create vs update vs delete latencies. Create is always the heaviest (must create sub-resources: Deployments/LWS + Services + RoleInstanceSets). Update and delete should be fast (<500ms). If update is slow, it suggests the controller re-reconciles too many objects on annotation change.

Format the output as:

```
## Performance Analysis

### Throughput
| Phase  | Target QPS | Actual QPS | Gap    | Verdict      |
|--------|-----------|------------|--------|--------------|
| create | 10        | 6.95       | -30.5% | ⚠ Behind     |
| update | 10        | 10.0       | 0%     | ✓ On target  |

### Latency Distribution
- Create: P50=Xms, P99=Yms (ratio: Z) — [interpretation]
- ...

### Latency Trend
[Is latency stable or growing? What does this mean?]
```

#### 6B. Controller Tuning Recommendations

Based on the performance data, controller configuration (from `{output-dir}/report.html` or the controller args), and log analysis, recommend specific parameter changes.

Decision tree:

1. **If actual QPS < target QPS AND no throttle errors AND CPU < 80%**:
   → Bottleneck is `max-concurrent-reconciles`. Recommend increasing it.
   Rule of thumb: for N RBGs with M roles, at least `N*M / target_P99_seconds` workers needed.

2. **If throttle errors > 0 OR `Throttling request took` in logs**:
   → Bottleneck is `kube-api-qps`/`kube-api-burst`. Recommend increasing both.
   Rule of thumb: `kube-api-qps` should be at least `max-concurrent-reconciles × 3` (each reconcile makes ~3 API calls).

3. **If conflict errors > 5% of total operations**:
   → Too many concurrent reconciles competing on the same objects. Recommend decreasing `max-concurrent-reconciles` or adding jitter.

4. **If memory usage in heap profile > 500MB for < 1000 RBGs**:
   → Potential memory inefficiency. Check if informer cache is unbounded.

5. **If P99 latency > 30s but CPU is low and no throttling**:
   → Controller might be I/O bound (waiting on API server). Check API server latency or consider enabling watch bookmarks.

Provide concrete recommendations:
```
## Tuning Recommendations

### Current Configuration
- max-concurrent-reconciles: 50
- kube-api-qps: 200
- kube-api-burst: 400
- CPU limit: 8 cores
- Memory limit: 16Gi

### Recommendations
1. **[parameter]**: Change from X to Y. Reason: [evidence from the data].
2. ...

### Estimated Impact
[What improvement to expect from these changes]
```

#### 6C. Controller Log Analysis

Read `{output-dir}/errors.log` (first 50 lines) and the error classification from the HTML report.

Analyze:
1. **Error categorization**: Group errors by type and identify the root cause for each category:
   - `unsupported workload type: /` → Missing `rbg.workloads.x-k8s.io/role-workload-type` annotation on roles
   - `the object has been modified` → Optimistic lock conflict, normal under high concurrency, becomes a problem if > 10% of operations
   - `Throttling request took Xs` → Client-side rate limiting, increase `kube-api-qps`
   - `context deadline exceeded` → Reconcile took too long, increase timeout or reduce work per reconcile
   - `Failed to create workload reconciler` → Likely workload type or CRD issue
   - `panic` / `runtime error` → Critical bug, needs immediate investigation

2. **Error ratio**: Calculate errors as a percentage of total reconcile operations. < 1% is acceptable (transient conflicts). > 5% indicates a systemic issue.

3. **Error pattern over time**: Are errors concentrated at the beginning (startup/cache-sync) or throughout (systemic)?

Format:
```
## Log Analysis

### Error Summary
| Category           | Count | % of Operations | Severity | Root Cause       |
|-------------------|-------|-----------------|----------|------------------|
| Workload Type     | 422   | 14%             | Medium   | Missing annotation|

### Key Findings
- [Finding 1]
- [Finding 2]

### Actionable Items
1. [What to fix and how]
```

#### 6D. Profiling Analysis

Read the pprof top-N text files: `{output-dir}/cpu-create-top.txt`, `{output-dir}/heap-create-top.txt`, `{output-dir}/goroutine-create-top.txt`, `{output-dir}/allocs-create-top.txt`.

If pprof files are not available, skip this section and note "Profiling not available — deploy controller with `--enable-pprof=true`."

Analyze:

1. **CPU hotspots**: Categorize top CPU consumers:
   - **Infrastructure** (expected): `crypto/tls`, `runtime.*` (GC), `net/http`, `encoding/json` — normal overhead
   - **Controller logic** (tunable): reconciler functions, status update, dependency resolution — high % here means the controller logic itself is the bottleneck
   - **Serialization** (reducible): `k8s.io/apimachinery/pkg/runtime`, `Unmarshal`, `Marshal` — indicates heavy object churn
   - If total CPU sample time is < 1% of profile duration, the controller is I/O bound, not CPU bound.

2. **Heap analysis**: Check the top memory allocators:
   - `runtime.allocm` → goroutine stack memory, scales with `max-concurrent-reconciles`
   - `k8s.io/apimachinery/...Unmarshal` → informer cache deserializing K8s objects
   - If total heap > 1GB for < 1000 RBGs, flag as high memory usage
   - Note: heap profile shows in-use memory, not peak. For peak, check container metrics.

3. **Goroutine analysis**: Check total goroutine count:
   - < 100 goroutines: healthy
   - 100-500: normal for high-concurrency controllers
   - > 1000: potential goroutine leak, check for unbounded goroutine creation

Format:
```
## Profiling Analysis

### CPU Profile (create phase)
- Total sample time: X of Y seconds (Z% CPU utilization)
- Top consumers: [categorized list]
- Verdict: [CPU bound / I/O bound / balanced]

### Memory Profile
- Heap in-use: XMB
- Top allocators: [list with interpretation]
- Verdict: [memory efficient / needs attention]

### Goroutine Profile
- Active goroutines: N
- Verdict: [healthy / needs investigation]
```

#### 6E. Executive Summary

Synthesize all findings into a brief summary:

```
## Summary

**Test**: [N] RBGs × [M] roles at [Q] QPS — [all succeeded / X failed]

**Performance**: [One sentence on whether the controller met the throughput target]

**Bottleneck**: [What is the primary bottleneck — worker count / API QPS / CPU / memory]

**Top 3 Recommendations**:
1. [Most impactful change]
2. [Second most impactful]
3. [Third]

**Overall Assessment**: [Ready for production at this scale / Needs tuning / Critical issues found]
```

### Phase 7: Teardown (optional)

```bash
# Remove fake nodes and stress test namespace
bash test/stress/scripts/teardown-kwok.sh

# To also uninstall kwok:
UNINSTALL_KWOK=true bash test/stress/scripts/teardown-kwok.sh
```

### Auto-Tuning Mode (if selected)

In auto-tuning mode, repeat Phase 2-6 multiple rounds with different configurations:

Round 1: User's initial config (baseline)
Round 2: Increase kube-api-qps/burst (test if API throttling is bottleneck)
Round 3: Increase max-concurrent-reconciles (test if worker count is bottleneck)
Round 4: Optimal combination based on Round 2+3 findings

Compare results across rounds and recommend the configuration that achieves:
- Lowest P99 latency
- No OOM or excessive memory growth
- Minimal API throttle errors
- Best resource efficiency (latency per CPU-core)

## Background Knowledge

### Workload Type Annotation

The stress test templates must include `rbg.workloads.x-k8s.io/role-workload-type` annotation on each role:
- Standalone roles: `"apps/v1/Deployment"`
- LWS roles: `"leaderworkerset.x-k8s.io/v1/LeaderWorkerSet"`

Without this annotation, the controller falls back to `RoleInstanceSet` type which may not be supported by older controller images, causing transient "unsupported workload type: /" errors and slow reconciliation.

### In-Cluster KWOK Architecture

```
Real Nodes (ACK)              Fake Nodes (kwok)
┌─────────────────────┐      ┌─────────────────────┐
│ RBG Controller Pod  │      │ kwok-node-0..N       │
│ (real, Helm-managed)│      │ (simulated by kwok)  │
│                     │      │                      │
│ kwok Controller Pod │      │ Workload Pods        │
│ (simulates pods)    │      │ (auto Ready by kwok) │
└─────────────────────┘      └─────────────────────┘
         │                            ▲
         │ watches pods on            │ schedules to
         │ fake nodes                 │ (toleration + nodeSelector)
         └────────────────────────────┘
```

### controller-runtime Client QPS/Burst

The controller uses `client-go` rest.Config which has:
- `QPS`: sustained queries-per-second to the API server (default 20 for client-go, often 5 for controller-runtime)
- `Burst`: maximum burst above QPS (default 30)

When the controller manages many objects, low QPS/Burst causes client-side throttling visible as:
- `Throttling request took Xs` log messages
- Increased reconcile latency without high CPU usage
- `rate: Wait(n=1) would exceed context deadline` errors

### max-concurrent-reconciles

Controls how many reconcile loops run in parallel per controller. Higher values:
- Pros: faster throughput, better utilization of API quota
- Cons: more memory, more concurrent API calls, more conflicts

### Key Metrics to Watch

- **Reconcile queue depth**: if growing, controller can't keep up
- **API request latency**: high = API server overloaded or network issues
- **Memory growth**: linear growth during test = potential leak
- **Conflict errors**: high = too many concurrent reconciles competing on same objects

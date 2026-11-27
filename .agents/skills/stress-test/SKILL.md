---
name: stress-test
description: RBG controller stress test - deploy kwok in existing cluster, run create/update/delete load tests with real controller pod, collect pprof profiling and logs, generate analysis report with recommendations. Use when measuring controller throughput, latency, API load or memory, tuning max-concurrent-reconciles and kube-api QPS/burst, or comparing the load of two controller builds.
license: Apache-2.0
compatibility: Agent-neutral (Agent Skills format). Needs kubectl, helm, go and curl, plus docker with buildx to build an image. Installs a controller with webhooks, kwok and fake nodes into the cluster, so use a dedicated cluster or one whose owner agreed.
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
- `helm`, `kubectl`, `go`, `curl` available in PATH; docker with buildx to build and push an image
- RBG CRDs and controller already deployed (or will be deployed by this skill)
- A registry the cluster's nodes can pull from, when you test a build that isn't published

## Workflow

### Phase 0: Preflight (always)

This skill changes the whole cluster: `deploy-controller.sh` runs `helm upgrade --install rbgs` in
`rbgs-system`, which installs a controller and its admission webhooks or **replaces an existing
install**, and `setup-kwok.sh` installs kwok and fake nodes. Teardown deletes every node labeled
`type=kwok`. So before Phase 2:

1. Show the user the current kubectl context and server, and confirm that it is the cluster they
   mean.
2. Run `.agents/skills/rbg-conventions/scripts/cluster-preflight.sh`. It changes nothing.
   - Exit status 1 means someone else uses RBG on the cluster: an RBG controller or Helm release,
     admission webhooks, RoleBasedGroups in other namespaces, kwok fake nodes from another stress
     test, or a controller started by another session on this machine. Stop and ask the user.
     Continue only on a dedicated cluster, or with the agreement of everyone who uses it. Exit
     status 1 wins even when some checks couldn't run.
   - Exit status 2 means nothing was found, but a check couldn't run; find out why before you
     continue.
3. Check that the tools in Prerequisites are installed. Locate them, or ask the user, before you
   start installing anything.

### Choosing the build under test

The controller runs in the cluster, so the nodes must be able to pull its image.

- **A published release:** `BUILD_IMAGE=false IMAGE_TAG=<published tag>`.
- **This checkout or a PR:** `PUSH_IMAGE=true IMAGE_REPO=<registry/namespace the nodes can pull>`.
  `deploy-controller.sh` then builds the controller and CRD upgrader images for linux/amd64 and
  linux/arm64, pushes them, and deploys both. The chart always comes from this checkout.
  - Leave `IMAGE_TAG` unset. The script then tags every build `stress-<commit>-<UTC time>` and
    prints the image. The chart pulls `IfNotPresent`, so under a reused tag the nodes can keep an
    older image they cached, and Helm may not roll the controller at all. The script refuses to
    push `latest`.
  - To deploy the same build again, as in later auto-tuning rounds, pass `BUILD_IMAGE=false` with
    the same `IMAGE_REPO` and the tag the script printed.
- A local build that isn't pushed only works with a local kwokctl cluster, or with nodes that use
  this machine's images. On a remote cluster the script warns. The nodes then can't pull the new
  tag (ImagePullBackOff), or with a tag that you set, they may pull an older image from the
  registry.
- Never report results without checking what the controller runs. The script prints each controller
  Pod with its creation time, image and imageID at the end. Each Pod must be newer than the
  `Started` time the script printed and run the image it printed. For a pushed build, its imageID
  must name the digest that `docker buildx imagetools inspect <image>` prints, or one of the
  platform manifests that the command lists.

### Phase 1: Ask User for Scenario

Ask the user for the following, one question at a time, offering the listed choices:

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
- Leader/worker roles (`--lws-roles`, rendered with `leaderWorkerPattern` on RoleInstanceSet): 0, 1, 2
- Pods per leader/worker instance (`--lws-size`): 2, 4, 8
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

2. **Deploy/upgrade RBG controller** via Helm with user-specified resources and params, and the
   image from [Choosing the build under test](#choosing-the-build-under-test):

   ```bash
   PUSH_IMAGE=true IMAGE_REPO=<registry/namespace> \
   CONTROLLER_CPU=8 CONTROLLER_MEMORY=16Gi \
   MAX_RECONCILES=10 KUBE_API_QPS=50 KUBE_API_BURST=100 \
   PPROF_ENABLED=true \
   bash test/stress/scripts/deploy-controller.sh
   ```

3. **Verify controller is ready** and runs the intended build, as described in
   [Choosing the build under test](#choosing-the-build-under-test):

   ```bash
   kubectl rollout status deploy/rbgs-controller-manager -n rbgs-system --timeout=120s
   kubectl get pods -n rbgs-system -l control-plane=rbgs-controller \
     -o custom-columns='POD:.metadata.name,CREATED:.metadata.creationTimestamp,IMAGE:.status.containerStatuses[*].image,IMAGE_ID:.status.containerStatuses[*].imageID'
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

```text
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

4. **Phase comparison**: Compare create vs update vs delete latencies. Create is always the heaviest (the controllers must create RoleInstanceSets, RoleInstances, Pods and Services). Update and delete should be fast (<500ms). If update is slow, it suggests the controller re-reconciles too many objects on annotation change.

Format the output as:

```markdown
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

```markdown
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
   - `unsupported workload type` → The controller doesn't reconcile a role's workload type. The stress RBGs set no workload-type annotation, so they use RoleInstanceSet: check that the controller Pod runs the image you meant to test (see Choosing the build under test). The report's hint to set the annotation is outdated
   - `the object has been modified` → Optimistic lock conflict, normal under high concurrency, becomes a problem if > 10% of operations
   - `Throttling request took Xs` → Client-side rate limiting, increase `kube-api-qps`
   - `context deadline exceeded` → Reconcile took too long, increase timeout or reduce work per reconcile
   - `Failed to create workload reconciler` → Likely workload type or CRD issue
   - `panic` / `runtime error` → Critical bug, needs immediate investigation

2. **Error ratio**: Calculate errors as a percentage of total reconcile operations. < 1% is acceptable (transient conflicts). > 5% indicates a systemic issue.

3. **Error pattern over time**: Are errors concentrated at the beginning (startup/cache-sync) or throughout (systemic)?

Format:

```markdown
## Log Analysis

### Error Summary
| Category           | Count | % of Operations | Severity | Root Cause       |
|-------------------|-------|-----------------|----------|------------------|
| Conflict          | 42    | 1.4%            | Low      | Optimistic lock contention |

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

```markdown
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

```markdown
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

### Phase 7: Teardown (always)

Clean up everything this run installed, even after a failed run. Keep the RBG CRDs installed; on a
shared cluster other sessions may depend on them.

```bash
# Stop the pprof port-forward that deploy-controller.sh started
kill "$(cat /tmp/rbg-stress-pprof-pf.pid)"

# Remove fake nodes and the stress test namespace
bash test/stress/scripts/teardown-kwok.sh

# If this run installed kwok, uninstall it too:
UNINSTALL_KWOK=true bash test/stress/scripts/teardown-kwok.sh

# If this run installed the controller release, uninstall it (CRDs stay installed)
helm uninstall rbgs -n rbgs-system
```

If the run upgraded an existing install instead, restore that install's previous image and values
as agreed with its owner. Then run the preflight again and report what it finds.

### Auto-Tuning Mode (if selected)

In auto-tuning mode, repeat Phase 2-6 multiple rounds with different configurations. Build and push
the image once; later rounds deploy it with `BUILD_IMAGE=false`, the same `IMAGE_REPO`, and the
`IMAGE_TAG` that the first round printed:

Round 1: User's initial config (baseline)
Round 2: Increase kube-api-qps/burst (test if API throttling is bottleneck)
Round 3: Increase max-concurrent-reconciles (test if worker count is bottleneck)
Round 4: Optimal combination based on Round 2+3 findings

Compare results across rounds and recommend the configuration that achieves:

- Lowest P99 latency
- No OOM or excessive memory growth
- Minimal API throttle errors
- Best resource efficiency (latency per CPU-core)

### Comparing Two Builds

To show that a change keeps the controller's load flat (for example in a PR review), run the same
scenario against both builds on the same cluster, with the same controller resources and
parameters:

1. Deploy the base build (`BUILD_IMAGE=false IMAGE_TAG=<published tag>`, or push it as above), and
   run Phase 3 with `--output-dir=/tmp/rbg-stress-base`. Its delete phase removes the RBGs; check
   that the stress namespace has none left.
2. Deploy the PR build and run the same command with `--output-dir=/tmp/rbg-stress-pr`. Before
   each run, check that the controller runs that build (Phase 2, step 3): a run on the wrong build
   makes the two look equal.
3. Compare `summary.json` per phase (actual QPS, P50/P99/max), the error categories, and the pprof
   tops. Run each build at least twice: differences within the run-to-run spread are not findings.

## Background Knowledge

### Workload Type

The stress RBGs use `standalonePattern` and `leaderWorkerPattern` roles without the
`rbg.workloads.x-k8s.io/role-workload-type` annotation, so they run on RoleInstanceSet, the
workload new features target. Don't add the annotation for the deprecated Deployment, StatefulSet
or LeaderWorkerSet types: they are deprecated, and with webhooks enabled the 0.9.0 gate rejects new
uses of them. The `--lws-roles` and `--lws-size` flags configure `leaderWorkerPattern` roles, not
LeaderWorkerSet objects.

### In-Cluster KWOK Architecture

```text
Real Nodes                    Fake Nodes (kwok)
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

- `QPS`: sustained queries-per-second to the API server (client-go's default is 5; the RBG controller sets it from `--kube-api-qps`, default 20)
- `Burst`: maximum burst above QPS (client-go's default is 10; `--kube-api-burst`, default 30)

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

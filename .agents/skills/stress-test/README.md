# /stress-test — RBG Controller Stress Test Skill

An agent skill (Agent Skills format) for stress testing the RBG (RoleBasedGroup) controller at scale using in-cluster KWOK simulation.

## What It Does

This skill orchestrates end-to-end stress testing of the RBG controller:

1. **Checks the cluster first** with the read-only `cluster-preflight.sh`, and stops if anyone else uses RBG there
2. **Deploys KWOK** in your existing cluster to simulate thousands of pods without real compute
3. **Configures the controller** with tuned resources, concurrency, and API QPS settings
4. **Runs load tests** — creates, updates, and deletes RBG instances at controlled QPS
5. **Collects profiling** data (CPU, heap, allocs, goroutines) via pprof
6. **Generates an HTML report** with latency charts, error classification, and pprof data
7. **Analyzes results** to identify bottlenecks and recommend tuning parameters
8. **Tears down** everything it installed, leaving the CRDs in place

## Quick Start

Ask your agent to run the stress-test skill, for example with `/stress-test` where slash commands
load skills. It runs the preflight, asks you to configure the test scenario, then runs everything.

## Architecture

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

- **KWOK** simulates pod lifecycle (scheduling, readiness) without running real containers
- **Fake nodes** have configurable capacity (default: 128 CPU, 512Gi memory, 1000 pods each)
- **RBG controller** runs as a real pod with configurable resources and tuning parameters
- **Stress client** (`test/stress/`) is a Go program that drives the workload with rate limiting

## Prerequisites

- A Kubernetes cluster accessible via `kubectl`: a dedicated one, or one whose owner agreed. The
  skill installs a controller with admission webhooks, kwok and fake nodes into it.
- `helm`, `kubectl`, `go`, `curl` in PATH, and docker with buildx to build an image
- RBG CRDs installed in the cluster
- To test a build that isn't published, a registry the cluster's nodes can pull from: set
  `PUSH_IMAGE=true IMAGE_REPO=<registry/namespace>` for `deploy-controller.sh`. It tags each build
  uniquely and prints the image.
- (Optional) Controller image with `--enable-pprof` support for profiling

## Test Phases

| Phase | What Happens | Metrics Collected |
|-------|-------------|-------------------|
| Create | Submit N RBGs at target QPS, wait for Ready | E2E latency (submit → Ready) |
| Update | Update all RBGs (annotation or image), wait for completion | E2E latency (update → Ready) |
| Delete | Delete all RBGs, wait for removal | E2E latency (delete → 404) |

## Output

The stress client generates files in the output directory (default: `/tmp/rbg-stress-results/`):

| File | Description |
|------|-------------|
| `report.html` | Main HTML report — open in browser |
| `summary.json` | Machine-readable stats (P50/P90/P99/QPS per phase) |
| `timing-{phase}.csv` | Raw per-operation latency data |
| `controller-full.log` | Controller logs during the test period |
| `errors.log` | Filtered error lines |
| `cpu-{phase}.prof` | CPU pprof binary profile |
| `heap-{phase}.prof` | Heap memory profile |
| `goroutine-{phase}.prof` | Goroutine profile |
| `*-top.txt` | Human-readable pprof top-N reports |

## Analysis

After generating the report, the agent analyzes:

- **Performance**: actual vs target QPS, latency trends, bottleneck identification
- **Tuning**: specific `max-concurrent-reconciles`, `kube-api-qps`, `kube-api-burst` recommendations
- **Logs**: error classification, root cause analysis, severity assessment
- **Profiling**: CPU hotspots, memory allocators, goroutine health

## Configurable Parameters

### Controller Tuning

| Parameter | Flag | Default | Description |
|-----------|------|---------|-------------|
| Max concurrent reconciles | `--max-concurrent-reconciles` | 10 | Parallel reconcile workers |
| API QPS | `--kube-api-qps` | 20 | Sustained API request rate |
| API burst | `--kube-api-burst` | 30 | Max burst above QPS |
| Pprof | `--enable-pprof` | false | Enable pprof HTTP server |

### Stress Test

| Parameter | Flag | Default | Description |
|-----------|------|---------|-------------|
| Total RBGs | `--total-rbgs` | 10 | Number of RBG instances |
| Roles per RBG | `--roles-per-rbg` | 2 | Roles in each RBG |
| Leader/worker roles | `--lws-roles` | 0 | Roles using `leaderWorkerPattern` (on RoleInstanceSet) |
| Leader/worker size | `--lws-size` | 4 | Pods per leader/worker instance |
| Create/Update/Delete QPS | `--{op}-qps` | 5 | Submission rate per phase |
| In-place update | `--in-place-update` | false | Use InPlaceIfPossible strategy |
| KWOK nodes | `--use-kwok-nodes` | true | Schedule to fake nodes |

## File Structure

```text
.agents/skills/stress-test/
├── SKILL.md                    # Skill definition and workflow
└── README.md                   # This file

test/stress/
├── main.go                     # Entry point, CLI flags, orchestration
├── client.go                   # K8s client setup (dynamic + clientset)
├── scenario.go                 # Test configuration structs
├── templates.go                # RBG manifest generation
├── create.go                   # Create phase with rate limiting
├── update.go                   # Update phase with rate limiting
├── delete.go                   # Delete phase with rate limiting
├── timing.go                   # Timing recorder + CSV/JSON output
├── pprof.go                    # Pprof collection (CPU/heap/allocs/goroutine)
├── report.go                   # HTML report generation
├── scripts/
│   ├── setup-kwok.sh           # Install KWOK + create fake nodes
│   ├── deploy-controller.sh    # Helm deploy with tuning params and the image under test
│   ├── collect-profiling.sh    # Standalone pprof collection
│   ├── collect-logs.sh         # Standalone log collection
│   └── teardown-kwok.sh        # Clean up fake nodes and namespace
└── templates/
    └── kwok-stage.yaml         # KWOK Stage CRs for pod simulation
```

The preflight is shared with the rbg-conventions skill:
`.agents/skills/rbg-conventions/scripts/cluster-preflight.sh`.

## Teardown

Always clean up what the run installed; keep the RBG CRDs. Phase 7 in [SKILL.md](SKILL.md) has
the full list.

```bash
# Remove fake nodes and test namespace
bash test/stress/scripts/teardown-kwok.sh

# Also uninstall KWOK from the cluster, if this run installed it
UNINSTALL_KWOK=true bash test/stress/scripts/teardown-kwok.sh

# Uninstall the controller release, if this run installed it
helm uninstall rbgs -n rbgs-system
```

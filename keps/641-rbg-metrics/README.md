# KEP-641: RBG Workload and Controller Metrics

English | [简体中文](README.zh-CN.md)

<!-- toc -->
- [Release Signoff Checklist](#release-signoff-checklist)
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Metric Inventory](#metric-inventory)
  - [Terminology](#terminology)
  - [Metric Naming and Labels](#metric-naming-and-labels)
  - [Build Information Metric](#build-information-metric)
  - [Workload Capacity Metrics](#workload-capacity-metrics)
  - [Group Progress Metric](#group-progress-metric)
  - [Rollout Duration Metric](#rollout-duration-metric)
  - [Pod Cache Metrics (KEP-284)](#pod-cache-metrics-kep-284)
  - [Controller Stage Metrics](#controller-stage-metrics)
  - [Feature Metrics](#feature-metrics)
  - [Optional Metrics](#optional-metrics)
  - [Collection Architecture](#collection-architecture)
  - [Implementation Notes](#implementation-notes)
  - [Secure Scraping and Deployment](#secure-scraping-and-deployment)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
  - [Upgrade / Downgrade Strategy](#upgrade--downgrade-strategy)
  - [Version Skew Strategy](#version-skew-strategy)
- [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
  - [Feature Enablement and Rollback](#feature-enablement-and-rollback)
  - [Rollout, Upgrade and Rollback Planning](#rollout-upgrade-and-rollback-planning)
  - [Monitoring Requirements](#monitoring-requirements)
  - [Dependencies](#dependencies)
  - [Scalability](#scalability)
  - [Troubleshooting](#troubleshooting)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure Needed](#infrastructure-needed)
<!-- /toc -->

## Release Signoff Checklist

The following checklist applies to releases in the RBG project.

- [ ] The target release, enablement mechanism, and upgrade / rollback strategy are defined.
- [ ] The test plan and graduation criteria are confirmed.
- [ ] The production readiness review is complete and approved.
- [ ] The implementation, generated artifacts, and required compatibility regression tests pass project CI.
- [ ] User documentation, migration guidance, and release notes are complete.

## Summary

This KEP defines the Prometheus metrics exposed by the RoleBasedGroup (RBG) controller.
RBG currently exposes controller-runtime and Go runtime metrics, but no workload metrics of its own.
Operators cannot alert on role capacity loss, rollout blockers, or Pod cache correctness from the controller endpoint.

This KEP adds a small set of workload and controller metrics on the existing `/metrics` endpoint.
The first implementation covers the RBG workload metrics (build information, role replica counts, group progress, rollout duration) and the five KEP-284 Pod cache metrics.
Later stages add controller stage timing, Warmup, RoleInstance phase, scaling, and gang scheduling metrics.
The design reuses the controller-runtime metrics registry, reads workload state from the informer cache, and keeps labels bounded.

## Motivation

The RBG controller already serves `/metrics`, but the endpoint only contains built-in controller-runtime, workqueue, leader election, client-go, Go, and process metrics.
These metrics describe controller health, not workload state.

Operators need time series for three questions:

1. Which role lost ready capacity, and when did it recover?
2. Is a rollout progressing, blocked, paused, or converged?
3. Did the Pod cache filter return a stub through a read path that should have returned a whole Pod?

`kubectl get rbg` and RBG status answer the current state of one object.
They do not provide historical trends, cross-object aggregation, or Prometheus alerting.
[Issue #641](https://github.com/sgl-project/rbg/issues/641) proposes workload metrics for this gap.
[KEP-284](../284-pod-cache-filter/README.md) defines Pod cache monitoring metrics that are not yet implemented.

This KEP does not export every status field.
It selects metrics that support alerting, SLOs, and incident diagnosis, and leaves detailed object inspection to Kubernetes status and events.

### Goals

1. Define the metric set, types, labels, and semantics for workload capacity, group progress, rollout duration, and Pod cache correctness.
2. Define bounded label values and reuse the reason taxonomy from [KEP-502](../502-workload-status-model/README.md).
3. Define collection from the informer cache for state gauges and code-path instrumentation for counters and histograms.
4. Define secure scraping, Helm deployment, and leader-election behavior.
5. Define unit, integration, and e2e tests, plus graduation criteria.

### Non-Goals

1. Collecting inference engine metrics from SGLang, vLLM, or other workload processes. Those remain workload-owned and are scraped from workload Pods.
2. Replacing kube-state-metrics or exporting every CRD status field.
3. Changing the status model defined by KEP-502 or the cache behavior defined by KEP-284.
4. Exporting per-Pod, per-node, per-revision-hash, or per-generation metrics.
5. Adding new API types or a feature gate solely for metrics.

## Proposal

Expose RBG metrics through the existing controller-runtime metrics registry and `/metrics` endpoint.
Do not add a second metrics endpoint.

The implementation is staged:

| Stage | Metrics | Purpose |
|---|---|---|
| Alpha | `rbg_build_info`, `rbg_role_replicas`, `rbg_group_progressing_info`, `rbg_rollout_duration_seconds`, and the `rbg_pod_cache_*` metrics (defined by KEP-284) | Capacity, progress, rollout SLO, cache correctness |
| Beta | Controller stage duration/failures, Warmup duration/failures, RoleInstance phase duration, scaling failures, gang PodGroup failures | Locate slow or failing controller paths |
| Optional | Gang scheduling duration | Feature-specific tuning and validation |

State gauges are collected from the informer cache at scrape time.
Counters and histograms are recorded when the corresponding event occurs.
Only the elected leader publishes workload state gauges.

### User Stories

#### Story 1: On-call capacity loss

An operator receives an alert that a `decode` role has zero ready replicas while its desired count is eight.
The operator queries:

```promql
rbg_role_replicas{role="decode", state="desired"} > 0
  and on(namespace, rbg, role)
rbg_role_replicas{role="decode", state="ready"} == 0
```

The operator then checks `rbg_group_progressing_info` and sees `reason="WaitingForResources"`.
This directs the investigation to cluster capacity or quota instead of application readiness.

#### Story 2: Rollout blocked or slow

A release operator sees `rbg_group_progressing_info{status="True", reason="WaitingForGang"} == 1`.
The group is waiting for gang scheduling.
If the group eventually converges but `rbg_rollout_duration_seconds` shows a high p95, the operator checks controller stage duration to find the slow reconcile path.

#### Story 3: Pod cache correctness

A controller developer sees `rbg_pod_cache_stub_reads_total > 0`.
KEP-284 expects this counter to remain zero.
A non-zero value identifies a read path that received a stub when it should have received a whole Pod.

### Notes/Constraints/Caveats

- Metrics reflect controller-observed state, not an independent probe of every Pod.
- State gauges are only as fresh as the informer cache and the object's `observedGeneration`.
- Counters and histograms reset when the controller process restarts. Prometheus `rate()` handles counter resets.
- Rollout duration samples require a start timestamp. If the controller restarts before completion, the in-flight sample is skipped rather than fabricated.
- Mixed-backend groups still expose role replica gauges when status is available, but do not expose `rbg_group_progressing_info` until the KEP-502 progress model applies.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Label cardinality grows with objects and roles | Use only `namespace`, `rbg`, `role`, and bounded enum labels; collect state from current cache objects so deleted objects stop emitting series |
| Stale status produces misleading progress | Export `rbg_group_progressing_info` only when group and condition `observedGeneration` match `metadata.generation`; stale series stop being emitted at the next scrape |
| Secure scraping fails because Prometheus lacks RBAC | Document and test TokenReview / SubjectAccessReview permissions for the scraping ServiceAccount |
| Metric collection adds scrape latency | Read from informer cache, avoid per-Pod series, and bound histogram buckets |
| Rollout duration is inaccurate after restart | Track start time in memory keyed by object UID and target revision; skip samples with no start time |

## Design Details

### Metric Inventory

The following table lists all metrics proposed by this KEP.
Detailed semantics are defined in the sections that follow.

| Metric | Stage | Type | Labels | Purpose |
|---|---|---|---|---|
| `rbg_build_info` | Alpha | Gauge | `version`, `commit`, `go_version` | Identify the controller build |
| `rbg_role_replicas` | Alpha | Gauge | `namespace`, `rbg`, `role`, `state` | Track desired, ready, and updated replicas per role |
| `rbg_group_progressing_info` | Alpha | Gauge | `namespace`, `rbg`, `status`, `reason` | Report the current group progress or blocker |
| `rbg_rollout_duration_seconds` | Alpha | Histogram | `namespace`, `rbg` | Measure rollout completion time |
| `rbg_pod_cache_stub_reads_total` | Alpha | Counter | `method` | Detect unexpected stub reads from the Pod cache |
| `rbg_pod_cache_scope` | Alpha | Gauge | `scope` | Report Pod cache mode |
| `rbg_pod_cache_objects` | Alpha | Gauge | `form` | Count cached whole Pods and stubs |
| `rbg_pod_cache_transform_total` | Alpha | Counter | `result` | Count Pod cache transform decisions |
| `rbg_pod_cache_initial_sync_seconds` | Alpha | Gauge | none | Report Pod informer initial sync duration |
| `rbg_controller_stage_duration_seconds` | Beta | Histogram | `controller`, `stage` | Locate slow reconcile stages |
| `rbg_controller_stage_failures_total` | Beta | Counter | `controller`, `stage`, `reason_class` | Count failures by reconcile stage |
| `rbg_warmup_duration_seconds` | Beta | Histogram | `namespace`, `warmup` | Measure Warmup completion time |
| `rbg_warmup_failures_total` | Beta | Counter | `namespace`, `warmup`, `reason` | Count Warmup failures by reason |
| `rbg_roleinstance_phase_duration_seconds` | Beta | Histogram | `phase` | Measure time spent in each RI phase |
| `rbg_scaling_adapter_scale_failures_total` | Beta | Counter | `namespace`, `adapter`, `reason_class` | Count scaling adapter failures |
| `rbg_gang_podgroup_reconcile_failures_total` | Beta | Counter | `scheduler`, `reason_class` | Count PodGroup reconcile failures |
| `rbg_gang_scheduling_duration_seconds` | Optional | Histogram | `scheduler` | Measure gang scheduling completion time |

Stage means when this KEP delivers the metric, not which KEP defines it.
The `rbg_pod_cache_*` metrics are defined by KEP-284 and required for its alpha graduation; this KEP implements them, so they are delivered at Alpha.

### Terminology

| Term | Definition |
|---|---|
| RBG | RoleBasedGroup, the top-level workload CRD |
| RIS | RoleInstanceSet, the workload that manages RoleInstances for a role |
| RI | RoleInstance, a group of tightly bound Pods managed as one replica unit |
| Role replica | The workload unit counted by RBG. An RI spanning multiple Pods counts as one replica |
| Progressing condition | The KEP-502 condition that reports progress toward the target or the current blocker |
| `observedGeneration` | A status field recording the `metadata.generation` last observed by the controller |
| PodGroup | The gang scheduling object used by scheduler-plugins or Volcano |
| Stub | A reduced Pod object retained by the KEP-284 Pod cache filter |
| Gauge | A Prometheus metric whose value can go up and down |
| Counter | A Prometheus metric whose value only increases, except for process restart |
| Histogram | A Prometheus metric that records observations into buckets |
| Informer cache | The controller-runtime local cache of Kubernetes objects used by the controller |
| Leader election | The mechanism that selects one controller replica to perform active reconciliation |
| ServiceMonitor | A Prometheus Operator custom resource that defines how to scrape a Service |
| TokenReview | A Kubernetes API used to authenticate a bearer token |
| SubjectAccessReview | A Kubernetes API used to authorize a request for a user or ServiceAccount |
| SLO | Service Level Objective, a target value or range for a service level |
| SLI | Service Level Indicator, a measured value used to evaluate an SLO |
| p95 | The 95th percentile of a distribution |

### Metric Naming and Labels

All metrics use the `rbg_` prefix.
Duration metrics use the `_seconds` suffix.
Cumulative event metrics use the `_total` suffix.

Allowed labels:

| Label | Meaning | Cardinality |
|---|---|---|
| `namespace` | Object namespace | Bounded by cluster namespaces |
| `rbg` | RoleBasedGroup name | Bounded by RBG objects |
| `role` | Role name inside an RBG | Bounded by roles per RBG |
| `state` | Replica state: `desired`, `ready`, `updated` | Fixed enum |
| `status` | Condition status: `True`, `False`, `Unknown` | Fixed enum |
| `reason` | Bounded reason: a KEP-502 progress reason (or `Other`) for progress metrics, or a feature-specific bounded reason for feature metrics such as Warmup | Bounded enum |
| `controller` | Controller name | Fixed enum |
| `stage` | Reconcile stage name | Fixed enum |
| `reason_class` | Classified operation-failure family from the KEP-502 taxonomy | Bounded enum |
| `phase` | RI lifecycle phase | Fixed enum |
| `scheduler` | Gang scheduler backend | Fixed enum |
| `method` | Pod cache read path | Bounded enum |
| `warmup` | Warmup object name | Bounded by Warmup objects |
| `adapter` | Scaling adapter name | Bounded by adapter objects |
| `scope` | Pod cache mode: `all`, `filtered` | Fixed enum |
| `form` | Cached Pod form: `whole`, `stub` | Fixed enum |
| `result` | Transform decision: `whole`, `stub` | Fixed enum |
| `version`, `commit`, `go_version` | Build information | One series per build |

Do not use Pod names, node names, revision hashes, generation values, condition messages, or raw error strings as labels.

### Build Information Metric

```text
rbg_build_info{version, commit, go_version}
```

Type: Gauge.
Value: `1`.

This metric identifies the controller build.
It does not describe workload state.
The value is always `1`; build metadata is carried by labels and queried through PromQL, not read from the metric value.

Example query:

```promql
# Which versions are running, e.g. to confirm a rollout converged to one version.
count by (version) (rbg_build_info)
```

### Workload Capacity Metrics

```text
rbg_role_replicas{namespace, rbg, role, state}
```

Type: Gauge.

`state` values:

| State | Source | Meaning |
|---|---|---|
| `desired` | `spec.roles[].replicas` after API defaulting | Target replica count |
| `ready` | `status.roleStatuses[].readyReplicas` | Replicas reported Ready |
| `updated` | `status.roleStatuses[].updatedReplicas` | Replicas reported at the update revision |

Semantics:

- Count replicas in workload units. An RI spanning four Pods counts as one replica.
- Publish `desired` when the role exists in spec.
- Publish `ready` and `updated` when a matching status entry exists, including explicit zero.
- Match spec and status entries by role name.
- Remove all series for a deleted role or RBG.
- Same-named RBGs in different namespaces produce separate series.

Example:

```promql
rbg_role_replicas{state="ready"}
  / ignoring(state)
(rbg_role_replicas{state="desired"} > 0)
```

### Group Progress Metric

```text
rbg_group_progressing_info{namespace, rbg, status, reason}
```

Type: Gauge.
Value: `1` for the current `Progressing` condition.

Semantics:

- Export only when all roles use RoleInstanceSet.
- Export only when both the RBG's `status.observedGeneration` and the condition's `observedGeneration` equal `metadata.generation`.
- Use the condition's `status` and controller-selected `reason`.
- Map unfamiliar reasons to `Other`.
- Export `Unknown/ObservationIncomplete` as reported.
- The previous series is no longer emitted at the next scrape when the condition changes, disappears, or becomes stale.

The reason values follow the KEP-502 taxonomy, including `Converged`, `Paused`, `WaitingForQuota`, `WaitingForResources`, `WaitingForGang`, and `WaitingForDependencies`.

Example:

```promql
rbg_group_progressing_info{status="True", reason="WaitingForQuota"} == 1
```

### Rollout Duration Metric

```text
rbg_rollout_duration_seconds{namespace, rbg}
```

Type: Histogram.

A rollout is the transition from one target revision to another.
The duration is measured from the first reconcile that observes the new target to the reconcile where the group reaches that target with all roles updated and ready.

Semantics:

- Record the start time in memory, keyed by RBG UID and target revision.
- Observe the histogram when the rollout completes.
- If no start time exists, skip the sample.
- Do not persist rollout start time in the RBG API for this KEP.

Suggested buckets, in seconds:

```text
1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600
```

Example:

```promql
histogram_quantile(
  0.95,
  rate(rbg_rollout_duration_seconds_bucket[5m])
)
```

### Pod Cache Metrics (KEP-284)

These metrics are defined by [KEP-284](../284-pod-cache-filter/README.md).
KEP-284 retains definition ownership; this KEP implements them.
All five are required for KEP-284's alpha graduation, so they ship with KEP-284 alpha rather than being optional here.

```text
rbg_pod_cache_stub_reads_total{method}
```

Type: Counter.

It counts stubs returned by a cached Pod `List`.
The expected value is zero.
A non-zero value means a read path received a stub where a whole Pod was expected.

`method` identifies the read path that returned the stub.
The implementation must use a bounded set of method names.

Example alert:

```promql
rbg_pod_cache_stub_reads_total > 0
```

The remaining KEP-284 metrics:

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `rbg_pod_cache_scope` | Gauge | `scope` | Configured Pod cache mode, `all` or `filtered`, set to 1 |
| `rbg_pod_cache_objects` | Gauge | `form` | Cached Pods by form: `whole` or `stub` |
| `rbg_pod_cache_transform_total` | Counter | `result` | Pod transform decisions: `whole` or `stub` |
| `rbg_pod_cache_initial_sync_seconds` | Gauge | none | Pod informer initial sync duration |

### Controller Stage Metrics

These metrics are targeted for beta.

```text
rbg_controller_stage_duration_seconds{controller, stage}
rbg_controller_stage_failures_total{controller, stage, reason_class}
```

`controller` examples:

- `rolebasedgroup`
- `roleinstanceset`
- `roleinstance`
- `rolebasedgroupwarmup`
- `rolebasedgroupscalingadapter`

`stage` examples:

| Controller | Stage | Meaning |
|---|---|---|
| `rolebasedgroup` | `precheck` | Validate role templates, dependencies, and gang annotations |
| `rolebasedgroup` | `revisions` | Get or create ControllerRevision and compute role hashes |
| `rolebasedgroup` | `reconcile_roles` | Create or update role workloads |
| `roleinstanceset` | `validate_membership` | Validate committed membership |
| `roleinstanceset` | `stateful_reconcile` | Reconcile stateful RoleInstanceSet |
| `roleinstance` | `get_owned_pods` | List Pods owned by the RI |
| `roleinstance` | `sync_instance` | Create, update, recover, or delete Pods |
| `roleinstance` | `update_status` | Write RI status |

`reason_class` uses bounded operation-failure families from KEP-502, such as `PermissionDenied`, `AdmissionDenied`, and `UnclassifiedBlocker`.
`WaitingFor*` reasons are blockers, not operation failures; they are reported through progress/blocker metrics rather than counted in `*_failures_total`.
Do not use raw error messages.

These metrics complement, not replace, `controller_runtime_reconcile_time_seconds` and `controller_runtime_reconcile_errors_total`.

### Feature Metrics

These metrics are targeted for beta.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `rbg_warmup_duration_seconds` | Histogram | `namespace`, `warmup` | Warmup elapsed time from start to completion |
| `rbg_warmup_failures_total` | Counter | `namespace`, `warmup`, `reason` | Warmup failures by bounded reason |
| `rbg_roleinstance_phase_duration_seconds` | Histogram | `phase` | Time spent in an RI lifecycle phase |
| `rbg_scaling_adapter_scale_failures_total` | Counter | `namespace`, `adapter`, `reason_class` | Scaling adapter failures |
| `rbg_gang_podgroup_reconcile_failures_total` | Counter | `scheduler`, `reason_class` | PodGroup reconcile failures |

RI phases are `Creating`, `Running`, `Updating`, and `Recovering`, as defined by KEP-502.
KEP-502 defines no `Terminating` phase; deletion is indicated by `metadata.deletionTimestamp`, not by phase.
An RI with an absent `status.phase` is skipped, not bucketed into an invented value.

### Optional Metrics

This metric is not required for alpha or beta.
Add it when gang scheduling needs tuning or validation.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `rbg_gang_scheduling_duration_seconds` | Histogram | `scheduler` | Time from PodGroup creation to gang scheduling completion |

### Collection Architecture

Register all metrics with `sigs.k8s.io/controller-runtime/pkg/metrics.Registry`.
They are served by the existing manager metrics endpoint.

State gauges:

- Implement a custom Prometheus collector that reads RBG objects from the informer cache at scrape time.
- Emit `rbg_role_replicas` and `rbg_group_progressing_info` from current cached objects.
- Deleted objects stop emitting series because the collector iterates only current objects.
- Only the elected leader runs the workload state collector.
- After leader change, the new leader rebuilds series from its cache.

Event metrics:

- Increment counters at the code path where the event occurs.
- Observe histograms when the measured operation completes.
- Use bounded `reason_class` values.
- Do not write state gauges on every reconcile.

Pod cache metrics:

- Instrument the KEP-284 cache filter and read paths.
- Keep `rbg_pod_cache_stub_reads_total` at zero unless a stub is returned through an unexpected read path.

No new informer or watch is introduced.
The collector reuses the RBG informer already required by the controller.

### Implementation Notes

These are design decisions for the implementation PR, not code.

**Collector wiring and leader gating.**
Add a new `pkg/metrics` package.
Construct the workload state collector after the manager exists, give it a read-only `client.Reader` backed by `mgr.GetCache()`, and register it with `metrics.Registry`.
The collector must emit only on the elected leader: gate emission on leadership (for example, check leader status inside `Collect`, or wrap the collector in a leader-aware runnable) so non-leader replicas emit no workload series.
This avoids duplicate or stale series across replicas.

**Rollout start-time state.**
The rollout start map is held by the rollout-duration recorder, keyed by RBG UID and target revision.
Entries are removed when the rollout completes or the RBG is deleted, so the map stays bounded by in-flight rollouts.

**Read-only and upgrade safety.**
Metrics collection adds no writes to the reconcile path and does not change rendered templates or revision hashes, so upgrading the controller does not roll existing workloads.
Change scope: the new `pkg/metrics` package, wiring in `cmd/rbgs/main.go`, and the metrics Service, optional ServiceMonitor, and scraping RBAC.
The Helm chart, `config/`, and `deploy/kubectl` move together.

**Stage timing and reason classification.**
Stage duration is recorded by timing wrappers around the named reconcile steps.
`reason_class` reuses the single KEP-502 error-classification function so all controllers report the same bounded families.

### Secure Scraping and Deployment

The controller already supports:

- `--metrics-bind-address`
- `--metrics-secure`
- controller-runtime authentication and authorization filters

This KEP adds deployment guidance and Helm resources:

- A metrics Service targeting the controller metrics port.
- An optional ServiceMonitor for Prometheus Operator.
- RBAC guidance for the Prometheus ServiceAccount when secure metrics are enabled.

When `--metrics-secure=true`, the scraping ServiceAccount must be able to create:

- `TokenReviews`
- `SubjectAccessReviews`

Without these permissions, the metrics endpoint exists but Prometheus cannot scrape it.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to existing tests to make this code solid enough prior to committing the changes necessary to implement this enhancement.

#### Prerequisite testing updates

- KEP-502 status and reason taxonomy tests must cover the conditions consumed by `rbg_group_progressing_info`.
- KEP-284 cache filter tests must cover whole/stub transformation and read paths.

#### Unit tests

- `rbg_role_replicas` emits desired, ready, and updated values, including explicit zero.
- Spec and status entries are matched by role name.
- Role deletion and RBG deletion remove series.
- Same-named RBGs in different namespaces produce separate series.
- `rbg_group_progressing_info` is exported only when generation checks pass.
- Stale, missing, and `Unknown/ObservationIncomplete` conditions are handled correctly.
- Unfamiliar reasons map to `Other`.
- Rollout duration observes a sample only when a start time exists.
- Pod cache stub reads increment only on unexpected stub reads.
- Label values are bounded; raw errors are not used as labels.

#### Integration tests

- Start the controller with envtest and scrape `/metrics`.
- Verify alpha metrics appear after RBG creation and status updates.
- Verify series disappear after RBG deletion.
- Verify leader election: only the leader publishes workload state gauges.
- Verify secure scraping with a ServiceAccount that has TokenReview / SubjectAccessReview permissions.
- Verify scraping fails without those permissions when secure metrics are enabled.

#### e2e tests

- Deploy Prometheus and scrape the controller metrics Service.
- Run a scale-up and verify desired, ready, and updated series change.
- Run a rollout and verify `rbg_rollout_duration_seconds` records a sample.
- Verify `rbg_pod_cache_stub_reads_total` remains zero in normal operation.
- Verify example alert queries return expected results.

### Graduation Criteria

#### Alpha

- The alpha metrics are implemented and registered.
- The KEP-284 Pod cache metrics are exported, as required by KEP-284's alpha graduation.
- Unit and integration tests pass.
- Secure scraping permissions are documented.
- User documentation lists metric names, labels, and example queries.

#### Beta

- Controller stage, Warmup, RI phase, scaling, and gang failure metrics are implemented.
- Reason labels use the KEP-502 taxonomy without raw error strings.
- Cardinality and scrape latency are measured at representative cluster sizes.
- Upgrade and downgrade paths are tested.
- SLO examples and alert rules are documented.

#### GA

- Metric names and label semantics are stable.
- At least two releases of beta feedback are addressed.
- Deprecation policy for metric changes is documented.

### Upgrade / Downgrade Strategy

Adding metrics does not change RBG API types or workload behavior.
Upgrading the controller adds new series to `/metrics`.
Downgrading removes those series.

Dashboards and alerts that depend on new metrics must tolerate missing series during downgrade.
Helm rollback removes the metrics Service and optional ServiceMonitor added by this KEP.

### Version Skew Strategy

This feature is controller-only.
It does not require node components or API server changes.

During skew, an older controller does not expose the new metrics.
Prometheus sees missing series rather than incorrect values.
No coordination with kubelet, kube-proxy, or scheduler is required.

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

###### How can this feature be enabled / disabled in a live cluster?

Metrics are exposed when the controller metrics endpoint is enabled.
There is no separate feature gate.

- Enable: set `--metrics-bind-address` to a non-zero address, for example `:8443`.
- Disable: set `--metrics-bind-address=0`.

Disabling the endpoint requires restarting the controller.
It does not affect running workloads.

###### Does enabling the feature change any default behavior?

No workload behavior changes.
The controller exposes additional metrics on the existing endpoint.

###### Can the feature be disabled once it has been enabled?

Yes.
Set `--metrics-bind-address=0` or downgrade the controller.
Existing workloads are unaffected.
Prometheus queries that depend on the removed metrics will return no data.

###### What happens if we reenable the feature if it was previously rolled back?

State gauges are rebuilt from the informer cache.
Counters and histograms start from zero after process restart.

###### Are there any tests for feature enablement/disablement?

Integration tests scrape with the endpoint enabled and verify no metrics are served when disabled.

### Rollout, Upgrade and Rollback Planning

###### How can a rollout or rollback fail? Can it impact already running workloads?

The main failure modes are scrape authentication misconfiguration and unexpected label cardinality.
Neither changes workload reconciliation.
Running workloads are not affected by metrics enablement or rollback.

###### What specific metrics should inform a rollback?

- `controller_runtime_reconcile_errors_total`
- `controller_runtime_reconcile_time_seconds`
- `process_resident_memory_bytes`
- Prometheus scrape duration and sample count for the controller target

###### Were upgrade and rollback tested? Was the upgrade->downgrade->upgrade path tested?

The test plan requires upgrade, downgrade, and re-upgrade scrape tests.

###### Is the rollout accompanied by any deprecations and/or removals of features, APIs, fields of API types, flags, etc.?

No.

### Monitoring Requirements

###### How can an operator determine if the feature is in use by workloads?

The presence of `rbg_build_info` and `rbg_role_replicas` indicates the controller is exposing workload metrics.

###### How can someone using this feature know that it is working for their instance?

- `rbg_role_replicas` matches RBG spec and status for their namespace and RBG name.
- `rbg_group_progressing_info` reflects the current `Progressing` condition.
- `rbg_rollout_duration_seconds` records samples after completed rollouts.
- `rbg_pod_cache_stub_reads_total` remains zero when the Pod cache filter is healthy.

###### What are the reasonable SLOs for the enhancement?

- `rbg_pod_cache_stub_reads_total` remains zero during normal operation.
- Rollout duration p95 is tracked per cluster and compared against the operator's release expectation. A concrete target is deployment-specific and is not fixed by this KEP.
- Controller scrape latency remains below the Prometheus scrape timeout.

###### What are the SLIs an operator can use to determine the health of the service?

- Metrics:
  - `rbg_role_replicas`
  - `rbg_group_progressing_info`
  - `rbg_rollout_duration_seconds`
  - `rbg_pod_cache_stub_reads_total`
  - `controller_runtime_reconcile_errors_total`

###### Are there any missing metrics that would be useful to have to improve observability of this feature?

Per-role rollout duration and gang scheduling duration are useful but deferred to beta or optional stages to keep the alpha set small.

### Dependencies

###### Does this feature depend on any specific services running in the cluster?

Prometheus is required only for scraping and alerting.
The controller can expose metrics without Prometheus.

###### Does this feature depend on any specific APIs?

It depends on the existing RBG, RIS, and RI APIs and the KEP-502 status fields.
Pod cache metrics depend on KEP-284.

###### Does this feature depend on any specific versions of other components?

It uses the controller-runtime version already vendored by RBG.
No new external component version is required.

### Scalability

###### Will enabling / using this feature result in new API calls?

No new API calls are introduced.
State gauges read from the existing informer cache.
Event metrics are recorded in existing reconcile paths.

###### Will enabling / using this feature result in introducing new API types?

No.

###### Will enabling / using this feature result in any new calls to the cloud provider?

No.

###### Will enabling / using this feature result in increasing size or count of the existing API objects?

No.

###### Will enabling / using this feature result in increasing time taken by any operations covered by existing SLIs/SLOs?

Scrape-time collection adds work proportional to cached RBG objects and roles.
It does not add work to Pod creation, update, or deletion paths.

###### Will enabling / using this feature result in non-negligible increase of resource usage?

The expected increase is small: one collector over cached RBG objects and a bounded set of counters/histograms.
Cardinality is approximately:

```text
3 * number_of_roles
  + number_of_rbg_progressing_series
  + 13 * number_of_RBGs_with_completed_rollouts
  + fixed_series
```

where:

- `number_of_roles` is the total number of roles across cached RBGs (three `state` values each).
- `number_of_rbg_progressing_series` is at most one per RBG with a current progress condition.
- `13 * number_of_RBGs_with_completed_rollouts` accounts for `rbg_rollout_duration_seconds{namespace,rbg}`: 10 buckets plus `+Inf`, `_sum`, and `_count`, retained for each RBG that has completed at least one rollout.
- `fixed_series` covers `rbg_build_info`, Pod cache stub reads, and (at beta) controller-stage series, independent of object count.

###### Can enabling / using this feature result in resource exhaustion of some node resources?

No node resources are introduced.
The main risk is Prometheus series cardinality, mitigated by bounded labels and cache-based collection.

### Troubleshooting

###### How does this feature react if the API server and/or etcd is unavailable?

The informer cache may become stale.
State gauges reflect the last cached state.
`rbg_group_progressing_info` is no longer emitted when generation checks fail, reducing misleading progress reports.

###### What are other known failure modes?

- Missing metrics because the controller is not leader.
  - Detection: `leader_election_master_status` is 0 and workload gauges are absent.
  - Mitigation: scrape the leader or use a Service that selects the leader.
  - Diagnostics: controller leader election logs.
  - Testing: integration test with two controller replicas.

- Secure scrape returns 401/403.
  - Detection: Prometheus target health and scrape error.
  - Mitigation: grant TokenReview / SubjectAccessReview permissions to the scraping ServiceAccount.
  - Diagnostics: controller metrics filter logs.
  - Testing: integration test with and without RBAC.

- Stale progress condition.
  - Detection: `rbg_group_progressing_info` absent while RBG status exists.
  - Mitigation: check `observedGeneration` and controller reconcile errors.
  - Diagnostics: RBG status and controller logs.
  - Testing: unit test that a stale condition stops being emitted.

###### What steps should be taken if SLOs are not being met to determine the problem?

1. Check `rbg_group_progressing_info` for the current blocker reason.
2. Check `rbg_role_replicas` for desired/ready/updated gaps.
3. Check `rbg_rollout_duration_seconds` for slow rollouts.
4. Check controller stage duration and failures once beta metrics are available.
5. Check `rbg_pod_cache_stub_reads_total` for cache correctness violations.

## Implementation History

- 2026-11-24: Initial provisional KEP.

## Drawbacks

- Adds metric maintenance and documentation burden.
- Introduces object-scoped series that grow with RBG and role count.
- Can mislead operators if they treat controller-observed state as an independent Pod probe.

## Alternatives

### Export every status field

Rejected.
Most status fields are useful for inspecting one object, not for alerting or aggregation.
Exporting them increases cardinality and duplicates Kubernetes status.

### Use kube-state-metrics custom resource state only

Valid for generic status export, but it does not provide controller-specific event metrics such as rollout duration, stage failures, or Pod cache stub reads.
This KEP can coexist with kube-state-metrics.

### Write state gauges on every reconcile

Rejected for workload state gauges.
It requires explicit deletion cleanup and can leave stale series after leader changes.
A cache-based collector rebuilds state from current objects.

### Four separate role replica metrics

Issue #641 proposes separate desired, observed, ready, and updated metrics.
This KEP uses one `rbg_role_replicas` metric with a `state` label to reduce near-duplicate metric names.
The `observed` state is not exported because observation freshness is represented by `rbg_group_progressing_info`.

## Infrastructure Needed

None beyond the existing RBG controller and optional Prometheus deployment.

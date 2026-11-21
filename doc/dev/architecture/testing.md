# Testing

> Last verified against `main` at `1fc69f70`. Pointers name files and symbols, not line
> numbers; if a symbol moved, search for it and update this page.

This page maps the RBG test code: the test layers and their frameworks, the envtest and e2e harnesses, how to run and
focus each layer, and where a new test belongs. The make-target and CI table, and the rules every test must meet, are in
[build-test-release.md](../../../.agents/skills/rbg-conventions/references/build-test-release.md); this page links to
them instead of repeating them.

## Where the code lives

| Path | Responsibility |
|---|---|
| `api/`, `cmd/`, `internal/`, `pkg/` | Unit tests (`*_test.go`) next to the code they test; the only trees `make test` runs |
| `pkg/reconciler/testdata/restart-policy-upgrade/` | RoleInstanceSet and ControllerRevision fixtures emitted by released code |
| `hack/gen-helm-rbac/main_test.go` | Unit tests of the generator for the chart ClusterRole |
| `test/wrappers/`, `test/wrappers/v1alpha1/`, `test/wrappers/v1alpha2/` | Fluent builders for RBGs, roles, RBGSets, Pods and pod templates |
| `test/utils/` | E2E timeouts, default image, update and `kubectl exec` helpers, `WithLegacyCreationTimestamp` |
| `test/envtest/README.md`, `test/envtest/doc.go` | Envtest how-to and package doc, partly stale (see Behaviors worth knowing) |
| `test/envtest/testutil/` | Envtest environments (`SetupTestEnv`, `SetupWebhookTestEnv`, `SetupDeprecationGateTestEnv`, `SetupV1alpha1GateTestEnv`) and helpers |
| `test/envtest/testutil/webhook/` | Webhook configurations that `SetupWebhookTestEnv` installs |
| `test/envtest/testcase/` | One envtest suite per directory: `rbg`, `restart_policy`, `stateful`, `webhook`, `deprecation`, `v1a1gate`, `selective_commit` |
| `test/e2e/e2e_test.go` | Main e2e suite entry point, `TestE2E` |
| `test/e2e/framework/`, `test/e2e/framework/workloads/` | `Framework` (clients, suite namespace, cleanup), the `Expect*` helpers, and checkers per workload type |
| `test/e2e/testcase/v1alpha1/`, `test/e2e/testcase/v1alpha2/` | Spec bodies; each file exports one `Run*TestCases(f)` function |
| `test/e2e/apicompat/` | Standalone suite for a release installed with deprecated workload types disabled |
| `test/e2e/upgrade/` | Standalone upgrade suite: install, fixtures, snapshots and detectors, per-hop `releaseProfile` |
| `test/e2e/ci/` | Cluster-free guards for the e2e CI matrix and the `make test-e2e` recipe |
| `test/chart/` | Chart render tests (`helm template`) and a kustomize manager check |
| `test/stress/`, `test/stress/scripts/`, `test/stress/templates/` | KWOK stress client (`package main`), setup and collection scripts, KWOK stage template |
| `hack/preload-e2e-images.sh` | Loads the e2e workload images into Kind and selects `E2E_DEFAULT_IMAGE` |
| `.github/workflows/project-check.yml`, `.github/workflows/envtest.yml` | Project Check: codegen drift, lint, `make test`, `make test-chart`, `make test-e2e-matrix-coverage`; Envtest: the envtest suites |
| `.github/workflows/e2e-test.yml` | E2E image build, label-sliced matrix and special-install jobs |
| `.github/workflows/release-test.yml` | `/release-test`: Helm, manifest and upgrade e2e on published images |
| `.github/actions/setup-e2e-cluster/action.yaml` | Kind, LWS, optional gang schedulers, image loading, RBGS install, readiness waits |

## Test layers

| Layer | Location | Framework | What it can prove | Run command | Run in CI by |
|---|---|---|---|---|---|
| Unit | `*_test.go` under `api/`, `cmd/`, `internal/`, `pkg/` | stdlib `testing` + testify; controller-runtime fake client; Ginkgo only in `pkg/webhook/cert/writer` | Pure logic, rendering, revision hashes, reconcile decisions against an in-memory client | `make test`, or `go test` on a package | Project Check, `unit-test` job |
| Tool unit | `hack/gen-helm-rbac/`, `test/utils/`, `test/stress/` | stdlib `testing` | Generator, helper and stress-client logic | `go test ./hack/gen-helm-rbac/ ./test/utils/ ./test/stress/` | Not run |
| Chart render | `test/chart/` | stdlib `testing` + `helm template` | What the chart renders, plus the manager probes in `config/manager/manager.yaml` | `make test-chart` | Project Check, `lint` job |
| E2E routing guard | `test/e2e/ci/` | stdlib `testing`; Ginkgo dry run; fake `go` binary | Matrix slices cover the default suite; `make test-e2e` passes its flags intact | `make test-e2e-matrix-coverage` | Project Check, `e2e-matrix-coverage` job |
| Envtest | `test/envtest/testcase/` | Ginkgo/Gomega + controller-runtime envtest; `selective_commit` uses `testing` + testify | Real kube-apiserver and etcd: CRD schema and CEL, defaults on read, SSA and managedFields, admission and conversion webhooks, controllers without kubelet | `make test-envtest` | Envtest workflow |
| E2E | `test/e2e/` (`TestE2E`) | Ginkgo/Gomega + controller-runtime client against Kind | The installed controller with kubelet, scheduler, garbage collector, real certificates and webhooks | `make test-e2e` | E2E Test; Release Test |
| Deprecated-disabled e2e | `test/e2e/apicompat/` | Ginkgo/Gomega | The chart toggle off: manager flag, RBAC, admission rejections | `make test-e2e-deprecated-disabled` | E2E Test, `e2e-deprecated-disabled` job |
| Upgrade e2e | `test/e2e/upgrade/` | Ginkgo `Ordered` container; execs `helm` and `git` | A v0.7.0 or v0.8.0 install upgrades without disturbing running workloads | `make test-e2e-upgrade` | Release Test, `e2e-test-upgrade` job |
| Stress | `test/stress/` | Go program + KWOK | Latency, error rates and profiles at scale | The stress-test skill | Not run |

## Unit tests

### Conventions in use

- Most tests are table-driven (a slice of named cases, one `t.Run(tt.name, ...)` each) and check with testify `assert`
  and `require`. They build their own `runtime.NewScheme()` and take most objects from the `test/wrappers` builders.
- The controller-runtime fake client stands in for the API server: `fake.NewClientBuilder().WithScheme(s).WithObjects(...)`,
  plus `WithStatusSubresource` for status writers, `WithInterceptorFuncs` to inject read or write failures, and
  `WithIndex` for field-selector lists. Events go to `record.NewFakeRecorder`. Narrower interfaces get hand-written
  fakes that record calls, such as the revision histories, `fakeInplaceControl` and `recordingAdapter` below.
- Upgrade-shape tests load fixtures from `pkg/reconciler/testdata/`, or rewrite a revision patch into the older
  serialization with `WithLegacyCreationTimestamp` (`test/utils/revision.go`). Rule: see
  [upgrade-compatibility.md](../../../.agents/skills/rbg-conventions/references/upgrade-compatibility.md).

### Representative unit tests

| Area | Example | Pattern |
|---|---|---|
| API defaulting | `api/workloads/v1alpha2/rolebasedgroup_default_test.go`, `TestRoleBasedGroupDefaulter_Default` | Table-driven, no client |
| Generated CRD | `api/workloads/v1alpha2/rolebasedgroupwarmup_crd_test.go`, `TestGeneratedWarmupCRDDoesNotValidateContainerImages` | Reads the CRD YAML in `config/crd/bases` |
| Conversion | `api/workloads/v1alpha1/rolebasedgroup_conversion_test.go`, `TestRoleBasedGroup_ConvertTo_LeaderWorkerPattern` | Converts in memory, no API server |
| Controllers | `internal/controller/workloads/rolebasedgroup_controller_test.go`, `TestRoleBasedGroupReconciler_Reconcile` | Builds the reconciler struct around a fake client and recorder, then calls `Reconcile` |
| Workload reconcilers | `pkg/reconciler/roleinstanceset_reconciler_test.go`, `TestRoleInstanceSetReconciler_RestartPolicyUpgrade` | Compares rendering and revision with fixtures from a release |
| Error paths | `pkg/reconciler/sts_reconciler_test.go`, `TestRollingUpdateParameters_PreservesReadErrors` | Interceptor that fails one list type (`failListOf`) |
| RoleInstanceSet | `pkg/reconciler/roleinstanceset/statefulmode/stateful_rollout_regression_test.go`, `rollbackTestHarness` | Real reconcile phases; recording fakes replace API writes |
| Revision history | `pkg/reconciler/roleinstanceset/statefulmode/stateful_instance_set_control_test.go`, `fakeStatefulHistory` | Records deleted ControllerRevisions (stateless twin: `fakeStatelessHistory` in `pkg/reconciler/roleinstanceset/statelessmode/reconciler_test.go`) |
| RoleInstance | `pkg/reconciler/roleinstance/instance_status_test.go`, `TestAuxiliaryOwnerReferencesDoNotBlockMemberRepair`; `pkg/reconciler/roleinstance/sync/instance_update_test.go`, `TestUpdatePod` | Fake client with `WithIndex` on `fieldindex.IndexNameForOwnerRefUID`; `fakeInplaceControl` |
| In-place update | `pkg/inplace/pod/readiness/pod_readiness_utils_test.go`, `recordingAdapter` | Records writes; deliberately does not imitate kubelet |
| Gang scheduling | `pkg/scheduler/gang_scheduler_test.go`, `TestPodGroupScheduler_Reconcile` | scheduler-plugins and Volcano PodGroup types in the fake scheme |
| Port allocator | `pkg/port-allocator/port_allocator_test.go`, `TestPodScopedPortAllocation` | Replaces the package-level allocator with `newTestAllocator` |
| CLI | `cmd/cli/cmd/rollout/rollout_test.go` | Generated fake clientset from `client-go/clientset/versioned/fake` |

### Running and focusing unit tests

```shell
go test ./pkg/reconciler/... -run 'TestRoleInstanceSetReconciler_RestartPolicyUpgrade/leader-worker-legacy' -count=1
go test ./api/workloads/v1alpha2/ -run 'TestRoleBasedGroupDefaulter_Default/legacy_Recreate' -count=1 -v
go test -race -shuffle=on -count=5 ./pkg/reconciler/roleinstanceset/statefulmode/
```

`-run` matches each `/`-separated level as a regular expression (spaces in a `t.Run` name become underscores), and
`-count=1` bypasses the test cache. `make test` first runs `manifests generate fmt vet`, which rewrites generated files.

## Envtest

### Envtest suites and harnesses

| Suite in `test/envtest/testcase/` | Environment (`test/envtest/testutil`) | Webhooks served | Controllers | Covers |
|---|---|---|---|---|
| `rbg` | `SetupTestEnv` | None | RBG, RoleInstanceSet, RoleInstance, ScalingAdapter, RBGSet | CRD validation, v1alpha1-created RBGs, selective scale-down, shared Service selection |
| `restart_policy` | `SetupTestEnv` | None | Same | Restart policies, backoff, restart waves |
| `stateful` | `SetupTestEnv` | None | Same | Stateful RoleInstanceSet rollout recovery and orphan Pod recovery |
| `webhook` | `SetupWebhookTestEnv` | Mutating RBG, RBGSet, RoleInstanceSet; validating RBG, RBGSet | None | Defaulters and excluded-ordinal admission |
| `deprecation` | `SetupDeprecationGateTestEnv`, then `InstallDeprecationGateWebhooks` | Validating RBG, RBGSet, installed after a legacy RBGSet is seeded | None | The 0.9.0 deprecated-workload gate on objects that predate it |
| `v1a1gate` | `SetupV1alpha1GateTestEnv` | CRD conversion; validating RBG, RBGSet | None | v1alpha1 requests through conversion and the v1alpha2 validators; CRD deprecation warnings |
| `selective_commit` | Its own `envtest.Environment` per `Test*` function | None | None; drives `reconciler.NewRoleInstanceSetReconciler` directly | Server-side apply writer transitions and managedFields of selective membership |

### How `SetupTestEnv` builds the environment

In `test/envtest/testutil/setup.go`, in order:

1. Starts envtest with the CRDs in `config/crd/bases` (`ErrorIfCRDPathMissing`, path resolved with `runtime.Caller` so
   tests run from any directory), then registers the v1alpha1, v1alpha2 and apiextensions types in `scheme.Scheme`.
2. Creates `K8sClient`, a direct client that reads the API server, and `TestMgr` through `SetupManager` (metrics off,
   default cache options), then registers the production field indexes (`fieldindex.RegisterFieldIndexes`).
3. Registers the RBG controller (`scheduler.KubeSchedulerPlugin`, deprecated workload types off) and the RoleInstanceSet,
   RoleInstance, ScalingAdapter and RBGSet controllers; RBG and RoleInstance share one `NodeBindingStore`.
4. Starts the manager in a goroutine and sleeps 100 ms without waiting for cache sync, so specs assert with `Eventually`.

### CRDs, conversion and webhooks in envtest

- The CRDs in `config/crd/bases` carry no `conversion` stanza, so the API server converts with strategy `None`, which
  only rewrites `apiVersion`. envtest enables webhook conversion only for kinds registered in the scheme before `Start`,
  which only `SetupV1alpha1GateTestEnv` (`test/envtest/testutil/v1a1gate_setup.go`) does: `v1a1gate` is the one suite
  where v1alpha1 objects go through the real conversion code (see `test/envtest/testcase/webhook/default_test.go`).
- `SetupWebhookTestEnv` (`test/envtest/testutil/webhook_setup.go`) installs every YAML file in
  `test/envtest/testutil/webhook/` through `WebhookInstallOptions.Paths`, registers the production handlers with
  `SetupWebhookWithManager` on a server bound to envtest's host, port and certificates, and waits for the port. The YAML
  files are hand-written; the CoordinatedPolicy validator in `config/webhook/manifests.yaml` is not among them.
- `InstallDeprecationGateWebhooks` (`test/envtest/testutil/deprecation_setup.go`) and `installV1a2GateWebhooks` build
  the `ValidatingWebhookConfiguration` in Go and wait until a server-side dry-run create of a StatefulSet-typed role is
  rejected with "new uses are not accepted".
- envtest loads whatever is in `config/crd/bases`. `make test-envtest` runs `manifests generate` first; a plain
  `go test` after an API change runs against stale CRDs.

### Running envtest suites

```shell
make setup-envtest
export KUBEBUILDER_ASSETS="$(bin/setup-envtest use 1.31.0 --bin-dir bin -p path)"
go test ./test/envtest/testcase/rbg/ -v -ginkgo.v -ginkgo.focus='Stateful selective scale-down' -timeout 30m
go test ./test/envtest/testcase/selective_commit/ -run 'TestSelectiveDenseOrphanReapply' -count=1 -v
```

- Envtest needs the etcd and kube-apiserver binaries. It looks for them through per-binary `TEST_ASSET_*` variables,
  then `KUBEBUILDER_ASSETS`, then `/usr/local/kubebuilder/bin`, and fails to start if none has them. `make test-envtest`
  sets `KUBEBUILDER_ASSETS` from `ENVTEST_K8S_VERSION`, default `1.31.0` like `.github/workflows/envtest.yml`. No suite checks the API
  server version; to test against the 1.28 minimum (CEL cost budget, no validation ratcheting), use `1.28.0` instead.
- Ginkgo suites focus with `-ginkgo.focus`, a regular expression on the full spec text, and skip with `-ginkgo.skip`;
  `selective_commit` uses plain `-run`. Manager and controller logs go to `GinkgoWriter`, which Ginkgo prints with a
  failed spec's report, or for every spec with `-ginkgo.v`. CI pipes the run into `envtest.log` and uploads it as the
  `envtest-logs` artifact on failure.
- `Eventually` timeouts are constants at the top of each test file, 25 to 60 seconds with 200 to 500 ms intervals.
  Webhook readiness waits allow 10 seconds, and the whole run has `-timeout 30m`.

## E2E

### Cluster contract

`make test-e2e` builds and installs nothing; the kubebuilder scaffold comment above `E2E_LABEL_FILTER` in the
`Makefile` that mentions image builds and CertManager is out of date. `TestE2E` uses the cluster in the current
kubeconfig (`Framework.Initialize`) and expects RBGS in `rbgs-system` (`RBGS_NAMESPACE` overrides it for the RBAC specs)
with webhook certificates bootstrapped, LeaderWorkerSet installed, the port allocator enabled for
`test/e2e/testcase/v1alpha2/port_allocator.go`, Volcano or scheduler-plugins only for the specs labelled with those
names, and the workload images on the nodes. `hack/preload-e2e-images.sh` loads the images; `E2E_DEFAULT_IMAGE`
(default `nginx:latest`) replaces the main one.

CI builds that cluster with `.github/actions/setup-e2e-cluster/action.yaml`: Kind `v0.31.0` with
`kindest/node:v1.31.12`, LWS `v0.7.0`, controller and CRD-upgrader images tagged `e2e-ci` (built once by the
`build-images` job), a Helm or manifest install, and waits for the webhook certificate Secret and caBundle injection. A
local equivalent, on a Kind cluster named `rbgs-e2e` that already runs LWS:

```shell
make docker-build-controller docker-build-crd-upgrader TAG=e2e-ci
kind load docker-image rolebasedgroup/rbgs-controller:e2e-ci rolebasedgroup/rbgs-upgrade-crd:e2e-ci --name rbgs-e2e
helm upgrade --install rbgs deploy/helm/rbgs --namespace rbgs-system --create-namespace --wait \
  --set controller.image.tag=e2e-ci --set crdUpgrade.image.tag=e2e-ci \
  --set controller.features.portAllocator.enabled=true
KIND_CLUSTER_NAME=rbgs-e2e hack/preload-e2e-images.sh
make test-e2e E2E_LABEL_FILTER='smoke' E2E_FAIL_FAST=false
```

### Suite structure

- `TestE2E` builds the spec tree with `framework.NewLazyFramework` (`test/e2e/framework/framework.go`); the clients
  connect in `BeforeSuite` (`Framework.BeforeAll`), so a Ginkgo dry run needs no cluster. `test/e2e/apicompat` and
  `test/e2e/upgrade` use `framework.NewFramework`, which connects at once.
- Two top-level containers call the `Run*TestCases(f)` functions: `[v1alpha1] ...` (labels `v1alpha1` and `compat`) and
  `[v1alpha2] ...`. A new spec file runs only once its `Run*TestCases` function is called from `TestE2E`.
- `Framework.BeforeAll` creates one namespace (`test-ns-*`, label `rbgs-e2e-test=true`) for the whole run.
  `Framework.AfterEach` runs the registered debug function, deletes every RBG, RBGSet, Warmup and CoordinatedPolicy in
  that namespace and waits up to `utils.Timeout` (150 s) for its Pods to go. An empty `f.Namespace` would scope the
  deletes to every namespace, so `test/e2e/upgrade/suite_test.go` calls it only once the namespace is set.
  Cluster-scoped objects, such as the `patio-runtime` profile from `utils.CreatePatioRuntime`, stay.

### Labels and CI slices

| Label | Carried by (examples) | CI selection |
|---|---|---|
| `lifecycle` | `test/e2e/testcase/v1alpha2/rbg.go`, `test/e2e/testcase/v1alpha2/revision.go` | lifecycle slice |
| `workload` | `test/e2e/testcase/v1alpha2/standalone_role.go`, `test/e2e/testcase/v1alpha2/leader_worker_role.go` | workload slice, unless also `rollout` |
| `update` | `test/e2e/testcase/v1alpha2/inactive_pod.go`, `test/e2e/testcase/v1alpha2/stability.go` | update slice, unless also `rollout` |
| `rollout` | `test/e2e/testcase/v1alpha2/ris.go`, `test/e2e/testcase/v1alpha2/update_strategy.go` | rollout slice |
| `feature` | `test/e2e/testcase/v1alpha2/port_allocator.go`, `test/e2e/testcase/v1alpha2/warmup.go` | feature slice, unless also `rollout` |
| `webhook`, `rbac`, `install` | `test/e2e/testcase/v1alpha2/webhook_validation.go`, `test/e2e/testcase/v1alpha2/rbac_webhook_bootstrap.go` | webhook-rbac slice |
| `scheduler` | `test/e2e/testcase/v1alpha2/inplace_scheduling.go` | scheduler slice |
| `volcano`, `scheduler-plugins` | `test/e2e/testcase/v1alpha2/gang_scheduling.go` (`gangBackends`); `scheduler-plugins` also in `test/e2e/testcase/v1alpha1/rbg.go` | Their own jobs; every other filter excludes them |
| `v1alpha1`, `compat` | The `[v1alpha1]` container in `test/e2e/e2e_test.go` | v1alpha1 slice; the other slices add `!v1alpha1` |
| `smoke`, `serial`, `slow`, `customized-action` | Individual specs | No CI filter uses them |

- `E2E_LABEL_FILTER` defaults to `!volcano && !scheduler-plugins`, `E2E_TIMEOUT` to `45m` and `E2E_FAIL_FAST` to
  `true`. In `.github/workflows/e2e-test.yml`, `build-images` fans out to the eight-slice `e2e-matrix` (no fail-fast,
  25 to 40 minute timeouts), `e2e-manifest` (default filter on a `deploy/kubectl/manifests.yaml` install),
  `e2e-deprecated-disabled`, `e2e-volcano-gang` and `e2e-scheduler-plugins-gang`.
- `make test-e2e-matrix-coverage` runs `TestMatrixSlicesCoverLegacyDefaultSuite` (`test/e2e/ci/matrix_test.go`): it
  dry-runs the suite with the default filter and with each slice filter from that workflow, and fails when the union of
  the slices misses or adds a spec, or a slice selects nothing. `TestMakeTestE2EForwardsFlagsIntact`
  (`test/e2e/ci/makefile_test.go`) runs the real recipe with a fake `go` and checks the flags arrive as single arguments.

### Running e2e specs locally

```shell
go test ./test/e2e/ -v -ginkgo.v -ginkgo.focus='create & delete rbg with standalone roles' -timeout 45m
go test ./test/e2e/ -run '^TestE2E$' -ginkgo.dry-run -ginkgo.v -ginkgo.label-filter='rollout && !v1alpha1'
go test ./test/e2e/... -run '^$'
```

The first runs one spec against the current cluster; without `-timeout`, `go test` stops at its 10-minute default with
a panic. The second lists what a filter selects, without a cluster. The third only compiles the e2e packages.

### Diagnostics and reports

- `make test-e2e` writes `junit.xml` and `report.json` into an `artifacts` directory inside `test/e2e/`, and the
  standalone suites into their own. CI uploads the reports on every run and, on failure or cancellation, prints the
  controller logs, the controller Pod description and recent events, and uploads the `kind export logs` output.
- Specs register `dumpDebugInfo` (`test/e2e/testcase/v1alpha2/debug.go`) through `Framework.RegisterDebugFn`; when the
  spec failed, it prints the RBG conditions and each role's workload and Pods before `Framework.AfterEach` deletes them.

## Upgrade and compatibility suites

### Upgrade suite

- **Preconditions** (`requireCleanCluster` in `test/e2e/upgrade/install.go`): no RBGS install in the cluster, and
  `helm` and `git` on `PATH`.
- **Upgrade from**: `RBGS_FROM_GIT_TAG` selects the hop, `v0.7.0` (default) or `v0.8.0`. The chart comes from a git
  worktree of that tag; the images default to the published `v0.7.0-888ade7` or `v0.8.0-16705159` (`RBGS_FROM_TAG`).
- **Upgrade to**: the working-tree chart with its default images, unless `RBGS_TO_REPO`, `RBGS_TO_TAG`,
  `RBGS_CRD_UPGRADE_REPO` or `RBGS_CRD_UPGRADE_TAG` are set; the CRD-upgrader tag falls back to `RBGS_TO_TAG`. The other
  overrides and all defaults are in `test/e2e/upgrade/preflight.go`.
- **Phases**, one `Ordered` container in `test/e2e/upgrade/specs.go`:
  1. Phase 1 probes the old CRDs, creates the fixtures from `test/e2e/upgrade/fixtures.go` (every role pattern,
     deprecated workload types, RBGSets, legacy strategy values, v1alpha1 objects, a Pending and a mid-rollout RBG) and
     records the running Pods.
  2. Phase 2 halts one rollout at its partition, then runs `helm upgrade` from inside the suite
     (`test/e2e/upgrade/upgrade.go`, `runHelmUpgrade`), so the snapshot stays in memory.
  3. Phase 3 waits for two agreeing samples and runs the detectors (`test/e2e/upgrade/snapshot.go`, `waitQuiesced`,
     `runDetectors`): Pod identity, restarts, labels, annotations and readiness gates (`PodFacts`); owner UID and
     stored spec; Service UID, ClusterIP, ports, selector and endpoints; ControllerRevision names; readiness;
     Pod-killing events. It repeats them after a cold controller restart and a second identical `helm upgrade`.
  4. Phase 4 checks that the upgraded controller still recreates, scales and rolls only what changed, handles v1alpha1
     writes, heals legacy strategy types and serves the Warmup API.
- **Recorded rewrites**: `test/e2e/upgrade/profile.go` holds one `releaseProfile` per hop (old values layout, the marker
  that proves the new CRD bundle landed, and the `recordedRewrites` the hop may perform, such as the v0.7.0
  `sharedServiceSelection` narrowing). Teardown deletes the `workloads.x-k8s.io` CRDs. Rule: see
  [verification-cases.md](../../../.agents/skills/rbg-conventions/references/verification-cases.md). What the suite
  does not prove is in the package doc of `test/e2e/upgrade/preflight.go` and in
  [upgrade-compatibility.md](../../../.agents/skills/rbg-conventions/references/upgrade-compatibility.md).

### Deprecated-disabled suite

`test/e2e/apicompat` needs a release installed with `controller.deprecatedWorkloadTypes.enabled=false`;
`preflightRequireToggleDisabled` (`test/e2e/apicompat/suite_test.go`) fails the suite with setup instructions otherwise.
Its specs check manager health, the missing RBAC for deprecated types (also by SubjectAccessReview), the rejection of
deprecated types on RBG and RBGSet creates and updates (including a v1alpha1 RBG that relies on the default StatefulSet
workload), and the admission of the default RoleInstanceSet workload.

### Release test command

A `/release-test` comment by an OWNER, MEMBER or COLLABORATOR runs `.github/workflows/release-test.yml` on the PR head
and builds no image: `e2e-test-helm` and `e2e-test-manifest` run `make test-e2e` against the images the chart and
`deploy/kubectl/manifests.yaml` reference, and `e2e-test-upgrade` runs `make test-e2e-upgrade` from `v0.7.0` and from
`v0.8.0` with `CI=true`. Results are mirrored onto the PR head as `Release Test / ...` commit statuses.

## Chart and stress tests

- `renderChart` (`test/chart/deprecated_workload_types_test.go`) runs `helm template` on `deploy/helm/rbgs`.
  `TestManagerFlagReflectsToggle`, `TestClusterRoleGrantsReflectToggle` and
  `TestToggleDeltaIsScopedToDeprecatedRBACAndFlag` render the deprecated-workload toggle enabled, unset and disabled and
  check that only the manager flag and the grants in `deprecatedGrants` change. `TestChartManagerHasHealthProbes` and
  `TestKustomizeManagerHasHealthProbes` (`test/chart/manager_probes_test.go`) check the `/healthz` and `/readyz` probes
  in the chart and in `config/manager/manager.yaml` (#575).
- `test/stress/main.go` is a load client: it creates, updates and deletes RBGs at the rates its flags set
  (`-total-rbgs`, `-create-qps`, `-use-kwok-nodes`, `-pprof-addr` and others), measures the time to Ready, and writes
  `report.html`, `summary.json`, timing CSVs and pprof profiles to `-output-dir`. `test/stress/scripts/` sets up KWOK,
  deploys the controller and collects logs and profiles; the [stress-test skill](../../../.claude/skills/stress-test/SKILL.md)
  drives a complete run.

## Test helpers

`BuildBasicRoleBasedGroup(name, ns)` (`test/wrappers/v1alpha2/rbg_wrapper.go`) returns an RBG with the `group-name`
label, the fixed UID `rbg-test-uid` and one standalone role named `test-role`. `BuildStandaloneRole`,
`BuildLeaderWorkerRole` (size 2, leader and worker patches), `BuildBasicRoleBasedGroupSet` and `BuildRollingUpdate`
complete the v1alpha2 set; chained `With*` calls set fields and `Obj()` returns the object. `test/wrappers/v1alpha1/`
holds the v1alpha1 builders and `test/wrappers/pod_wrapper.go` the Pod builders. From
`test/e2e/testcase/v1alpha2/restart_policy_fields.go`:

```go
rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-rp-config-wins", f.Namespace).WithRoles(
    []workloadsv1alpha2.RoleSpec{
        wrappersv2.BuildLeaderWorkerRole("role-1").
            WithReplicas(1).
            WithSize(2).
            WithLegacyRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
            WithRestartPolicy(workloadsv1alpha2.RestartPolicyNone).
            Obj(),
    }).Obj()
f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })
```

- `test/utils/utils.go`: `Timeout` (150 s) and `Interval` (250 ms) for e2e `Eventually`; `DefaultImage`, read from
  `E2E_DEFAULT_IMAGE` at package initialization and used by the wrapper pod templates, in unit tests too;
  `CreatePatioRuntime`; `UpdateRbg` and `UpdateRbgSet` (v1alpha1, retried until the update succeeds).
  `RestartNginxContainer` (`test/utils/pod_failure.go`) uses `kubectl exec`, so kubelet records a real restart.
- `test/envtest/testutil/setup.go`: the package variables `K8sClient`, `Ctx`, `TestEnv` and `TestMgr`,
  `CreateNamespace`, `DeleteNamespace`, and `SetPodRunningAndReady` and `SetPodContainerRestarted`, which fake the
  kubelet-owned Pod status. `GateWarnings` (`test/envtest/testutil/v1a1gate_setup.go`) records API server warnings.
- `test/e2e/framework/framework.go`: `Framework` with `Ctx`, `Client`, `Clientset`, `Namespace` and `GetPodLogs`.
  Assertions are methods named after object, version and check, such as `ExpectRbgV2Equal`, `ExpectRbgV2Condition`,
  `ExpectRbgSetV2Equal` and `ExpectWorkloadV2PodTemplateLabelContains`; `NewWorkloadEqualCheckerV2`
  (`test/e2e/framework/workloads/workload_v2_expect.go`) picks a checker by the role's workload type.

## Where to add a test

| Change | Layer | Location | Existing example |
|---|---|---|---|
| Go validator or defaulter | Unit, then envtest for the admission path | `api/workloads/v1alpha2/`, `test/envtest/testcase/webhook/` | `api/workloads/v1alpha2/rolebasedgroup_validation_test.go`, `test/envtest/testcase/webhook/excluded_ordinals_test.go` |
| CRD schema, CEL or schema default | Envtest; unit for invariants of the generated CRD | `test/envtest/testcase/rbg/` or a new suite, `api/workloads/v1alpha2/` | `test/envtest/testcase/rbg/basic_test.go`, `api/workloads/v1alpha2/rolebasedgroupwarmup_crd_test.go` |
| v1alpha1 conversion | Unit, then envtest with conversion | `api/workloads/v1alpha1/`, `test/envtest/testcase/v1a1gate/` | `api/workloads/v1alpha1/rolebasedgroup_conversion_test.go`, `test/envtest/testcase/v1a1gate/gate_test.go` |
| Deprecated-workload gate | Envtest, chart, e2e | `test/envtest/testcase/deprecation/`, `test/chart/`, `test/e2e/apicompat/` | `test/envtest/testcase/deprecation/compat_test.go` |
| RBG rendering or status | Unit, then e2e | `pkg/reconciler/`, `internal/controller/workloads/`, `test/e2e/testcase/v1alpha2/` | `pkg/reconciler/pod_reconciler_test.go`, `internal/controller/workloads/rolebasedgroup_controller_test.go` |
| Revision hashes, upgrade safety | Unit with released fixtures, upgrade e2e | `pkg/utils/`, `pkg/reconciler/testdata/`, `test/e2e/upgrade/` | `pkg/utils/revision_utils_test.go`, `pkg/reconciler/roleinstanceset_reconciler_test.go` |
| RoleInstanceSet logic | Unit, envtest for requeues and persisted status | `pkg/reconciler/roleinstanceset/`, `test/envtest/testcase/stateful/` | `pkg/reconciler/roleinstanceset/statefulmode/stateful_rollout_regression_test.go`, `test/envtest/testcase/stateful/rollout_recovery_test.go` |
| RoleInstance restart and lifecycle | Unit, envtest, e2e | `pkg/reconciler/roleinstance/`, `test/envtest/testcase/restart_policy/`, `test/e2e/testcase/v1alpha2/` | `pkg/reconciler/roleinstance/sync/instance_scale_test.go`, `test/e2e/testcase/v1alpha2/inactive_pod.go` |
| Server-side apply ownership | Envtest without controllers | `test/envtest/testcase/selective_commit/` | `test/envtest/testcase/selective_commit/membership_test.go` |
| In-place update | Unit, then e2e | `pkg/inplace/`, `pkg/reconciler/roleinstance/sync/` | `pkg/inplace/pod/inplaceupdate/inplace_update_test.go`, `test/e2e/testcase/v1alpha2/update_strategy.go` |
| Scheduling or port allocation | Unit, then labelled e2e | `pkg/scheduler/`, `pkg/port-allocator/`, `test/e2e/testcase/v1alpha2/` | `pkg/scheduler/gang_scheduler_test.go`, `pkg/port-allocator/port_allocator_test.go`, `test/e2e/testcase/v1alpha2/gang_scheduling.go` |
| Chart, manifests, RBAC | Chart render, generator unit, e2e | `test/chart/`, `hack/gen-helm-rbac/`, `test/e2e/testcase/v1alpha2/` | `test/chart/manager_probes_test.go`, `test/e2e/testcase/v1alpha2/rbac_webhook_bootstrap.go` |
| Manager setup | Unit | `cmd/rbgs/` | `cmd/rbgs/main_test.go`, which today covers the `init` function, `printVersion` and scheme registration, not manager setup |
| CLI (`kubectl-rbg`) | Unit with fake clientsets | `cmd/cli/` | `cmd/cli/cmd/rollout/rollout_undo_test.go` |
| E2E label or CI slice | Routing guard | `test/e2e/ci/`, `.github/workflows/e2e-test.yml` | `test/e2e/ci/matrix_test.go` |
| Controller throughput | Stress | `test/stress/` | `test/stress/templates_test.go` |

## Behaviors worth knowing

- `make test` runs only `./api/... ./cmd/... ./internal/... ./pkg/...` (`Makefile`, target `test`). Tests under `test/`
  and `hack/` run only through their own targets or an explicit `go test`; no workflow runs `hack/gen-helm-rbac`,
  `test/utils` or `test/stress`, and `client-go/` has no tests.
- Project Check sets `CI_TEST_FLAGS` (with `-race`) on the `unit-test` job, but `make test` never reads it, so CI unit
  tests run without the race detector (`.github/workflows/project-check.yml`). Rule: see
  [build-test-release.md](../../../.agents/skills/rbg-conventions/references/build-test-release.md).
- The fake client serves server-side apply on RBG types through controller-runtime's deduced type converter, which may
  differ from the API server (no test passes `WithTypeConverters`); field ownership and key removal are proved in
  envtest (`test/envtest/testcase/selective_commit/membership_test.go`). A fake-client `List` with
  `client.MatchingFields` fails unless the test registers the index with `WithIndex`. Rule: see
  [controller-patterns.md](../../../.agents/skills/rbg-conventions/references/controller-patterns.md).
- The envtest manager is not the production one (`test/envtest/testutil/setup.go`, `SetupTestEnv`): it lacks the
  `cacheOptions()` label filter (`cmd/rbgs/main.go`), runs RBG with deprecated workload types off, and starts no Warmup
  controller, webhook cert controller or port allocator. A Service, StatefulSet or Deployment without the `group-name`
  label is visible to the controller in envtest but hidden from it in production. Rule: see
  [controller-patterns.md](../../../.agents/skills/rbg-conventions/references/controller-patterns.md).
- envtest runs only etcd and kube-apiserver. Tests set Pod status themselves, nothing garbage-collects owned objects,
  and a deleted namespace never finishes terminating: `DeleteNamespace` (`test/envtest/testutil/setup.go`) gives up
  after about three seconds without failing, so specs use a fresh namespace name (`time.Now().UnixNano()` suffix).
- A test that writes an object the controller also writes must retry on conflict (`retry.RetryOnConflict`), and one
  that snapshots a set the controller is still building must wait for the full set; #585 fixed one flake of each kind
  (`test/envtest/testcase/restart_policy/restart_policy_test.go`, `test/envtest/testcase/rbg/selective_scale_down_test.go`).
- `Label("serial")` is only a label: no spec uses `ginkgo.Serial`, and `go test` runs all specs in one Ginkgo process,
  one after another (#494). The host-port spread spec skips itself when fewer than two nodes are schedulable
  (`test/e2e/testcase/v1alpha2/port_allocator.go`, `countAvailableNodes`), so it never runs on the single-node CI Kind
  clusters (#532). Rule: see [build-test-release.md](../../../.agents/skills/rbg-conventions/references/build-test-release.md).
- A v1alpha2 spec without a routing label (`lifecycle`, `workload`, `update`, `rollout`, `feature`, `webhook`, `rbac` or
  `scheduler`) runs in no matrix slice, and `make test-e2e-matrix-coverage` reports it as missing
  (`test/e2e/ci/matrix_test.go`). The default `E2E_LABEL_FILTER` does select the v1alpha1 specs; the comment in
  `test/e2e/e2e_test.go` that calls them skipped by default is out of date.
- Upgrade fixtures are written through the old release's CRDs with today's Go types, so the API server prunes any field
  the old schema lacks. Fixtures use the old shapes, for example `WithLegacyRestartPolicy` instead of
  `restartPolicyConfig` (comment above `buildFixtures` in `test/e2e/upgrade/fixtures.go`).
- Without `helm` (and `git` for the upgrade suite) the chart tests and the upgrade suite skip themselves locally and
  fail when `CI` is set (`test/chart/deprecated_workload_types_test.go`, `renderChart`; `test/e2e/upgrade/install.go`,
  `requireCleanCluster`).
- Unit tests share package-level state and none calls `t.Parallel()`: the port allocator tests swap the package's
  allocator (`pkg/port-allocator/port_allocator_test.go`, `newTestAllocator`), and the `statefulmode` tests clear the
  `instanceUnhealthySince` map (`pkg/reconciler/roleinstanceset/statefulmode/stateful_instance_set_control_test.go`,
  `resetInstanceUnhealthySince`).
- Path filters differ per workflow (`check-changes` jobs). Envtest skips a change set confined to `examples/`, `doc/` or
  `deploy/`; E2E Test runs only for Go files, `Makefile`, `Dockerfile`, `go.mod`, `go.sum`, shell scripts,
  `config/crd/`, `config/rbac/`, `deploy/helm/`, `deploy/kubectl/`, `.github/workflows/`, `.github/actions/` or `test/`.
  A chart-only change runs e2e but not envtest; a change only under `config/webhook/` runs envtest but not e2e.
- `test/envtest/README.md` and `test/envtest/doc.go` list suite directories that no longer exist (`instanceset`, `rbgs`,
  `instance`, `pod`, `scalingadapter`), a `make envtest` target and a `TEST_PLAN.md`; this page lists the current ones.

## Related

- Rules: [build-test-release.md](../../../.agents/skills/rbg-conventions/references/build-test-release.md),
  [upgrade-compatibility.md](../../../.agents/skills/rbg-conventions/references/upgrade-compatibility.md),
  [verification-cases.md](../../../.agents/skills/rbg-conventions/references/verification-cases.md),
  [controller-patterns.md](../../../.agents/skills/rbg-conventions/references/controller-patterns.md),
  [review-playbook.md](../../../.agents/skills/rbg-conventions/references/review-playbook.md).
- Test docs: [envtest README](../../../test/envtest/README.md), [stress client README](../../../test/stress/README.md),
  [e2e CI optimization plan](../../../test/e2e/ci-optimization-plan.md) (#494),
  [stress-test skill](../../../.claude/skills/stress-test/SKILL.md), [how to develop](../how_to_develop.md).
- KEP with an envtest test plan: [KEP-499 stateful selective scale-down](../../../keps/499-stateful-selective-scale-down/README.md).
- Sibling pages: [overview](overview.md), [API layer](api-layer.md), [controller manager](controller-manager.md),
  [RoleBasedGroup controller](rbg-controller.md), [other controllers](other-controllers.md),
  [instance workloads](instance-workloads.md), [scheduling and networking](scheduling-and-networking.md),
  [feature index](feature-index.md).

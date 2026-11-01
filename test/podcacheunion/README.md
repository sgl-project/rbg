# Pod cache prototypes (KEP-284)

[KEP](../../keps/284-pod-cache-filter/README.md) ·
[Current measurements](../../keps/284-pod-cache-filter/performance.md)

This package is excluded from ordinary builds by `//go:build podcacheunion`.
It validates the proposed cache architecture; production flag wiring, marking,
CLI / Helm migration and full operator e2e remain integration work.

The selected design keeps one Pod informer. Admitted Pods stay whole except
`managedFields`; other Pods become identity/label/ownerReference stubs.
`WatchStreamTransformInformer` transforms watch objects before the reflector's
streaming-list temporary store. With client-go v0.34.2, the ordinary FIFO
transform alone runs too late to reduce that store's peak. The transform is
idempotent because the FIFO transforms watched objects again.

`union.go` is retained as the rejected four-source comparison and for the
shared admission-label helper. It is not a production cache. The identity-only
stub remains an optional comparison variant; the default matrix uses the KEP's
labels-and-owners stub.

## Correctness tests

Run the commands from the repository root. Install Go 1.26 or newer, `make`,
Git, and a C compiler (`clang` on macOS; `cc`/GCC on Linux). The reported
performance runs used Go 1.26.0. The race tests require CGO.

On a fresh checkout, first install the envtest downloader and fetch the control
plane binaries. This preparation needs internet access; the tests themselves
use an isolated control plane and need no existing Kubernetes cluster:

```sh
make setup-envtest
export CGO_ENABLED=1
export USE_EXISTING_CLUSTER=false
export KUBEBUILDER_ASSETS="$(bin/setup-envtest use 1.31.0 --bin-dir "$PWD/bin" -p path)"
```

The tests start an isolated Kubernetes v1.31.0 API server and etcd 3.5.15 from
the downloaded binaries. The fixture loads the RoleInstance CRD and binds its advertise address to loopback,
so it also runs in a container with networking disabled. There is no kubelet or
running workload controller.

```sh
KUBE_FEATURE_WatchListClient=false \
RBG_POD_UNION_APISERVER_FEATURE_GATES= \
KUBE_CACHE_MUTATION_DETECTOR=true \
go test -race -mod=vendor -tags=podcacheunion ./test/podcacheunion \
  -count=1 -v -timeout=10m

KUBE_FEATURE_WatchListClient=true \
RBG_POD_UNION_APISERVER_FEATURE_GATES=WatchList=true \
KUBE_CACHE_MUTATION_DETECTOR=true \
go test -race -mod=vendor -tags=podcacheunion ./test/podcacheunion \
  -count=1 -v -timeout=10m
```

`KUBEBUILDER_ASSETS` can override the default
`bin/k8s/1.31.0-<GOOS>-<GOARCH>`. These commands run the unit and real-API tests;
opt-in diagnostics and performance probes skip themselves.

- `TestTransformIsIdempotent`: repeated transformation, exact retained fields,
  deletion timestamp, dropped annotations/finalizers/spec/status on stubs,
  nonempty admitted status, managedFields stripping and tombstone pass-through.
- `TestAPITransformAdmissionAndStubs`: every admission condition, status
  preservation, label selectors, owner index and deep-copy/no-deepcopy Lists;
  store-only controls and the selected wrapper.
- `TestAPITransformTransitionsAndEvents`: live admission changes, an owned Pod
  losing its labels, and full/stub delete events through the selected wrapper.
- `TestAPITransformRelist`: stop the old watch, change admission before the 410
  event, then verify replacement forms and resourceVersions from the new snapshot.
- `TestAPITransformProductionReadPaths`: the production owner-list utility and
  cached Get/ownership checks for foreign-name collisions. Complete reconciler
  outcomes still need production-integration tests.
- `TestWatchStreamWrapperTransformsEvents` and
  `TestWatchStreamWrapperStopDoesNotLeak`: Add/Modify/Delete, bookmarks/errors,
  natural closure, and deterministic idle/pending-send loop exit without draining
  the watch or comparing global goroutine counts.
- `TestFeatureGateSetOverridesProductionImport`: the production import chain
  makes the environment variable ineffective; setting the effective gate works
  and the test restores its initial value.

**The test environment variable is a harness control.** `TestMain` explicitly
sets and verifies the effective WatchListClient gate in every process, including
diagnostics. The operator's imports replace client-go's environment-reading
implementation; production integration must wire the proposed
`--watch-list-client` flag to `utilfeature.DefaultMutableFeatureGate.Set`.

## Performance matrix

```sh
GOMAXPROCS=4 bash test/podcacheunion/scripts/transform-campaign.sh /path/to/new-run
```

For a shorter entry-point check, run one shape and one round in both server configurations:

```sh
GOMAXPROCS=4 SHAPES=48:1000 ROUNDS=0 \
bash test/podcacheunion/scripts/transform-campaign.sh /path/to/new-smoke-run
```

Use a new directory outside the checkout: the script refuses to overwrite an existing campaign.
Defaults are five related/unrelated shapes (`48:1000 900:100 1000:0 200:4800
5000:0`), three rotated rounds, and two server configurations. Each round starts
one control plane and dataset; modes run sequentially in separate processes.
Related Pods carry all four labels, a maximum-overlap case for the union, and
all Pods contain 64 × 512-byte environment values.

| Server configuration | Default modes |
| --- | --- |
| WatchList off | `full`, `union-serial`, `transform-labels-owners-stream`, `transform-labels-owners-stream+wl` (fallback) |
| WatchList on | `full`, `full+wl`, `transform-labels-owners+wl` (store-only control), `transform-labels-owners-stream+wl` |

`BATCHES`, `SHAPES`, `ROUNDS`, and comma-separated `MODES` can narrow the matrix.
`+wl` enables the effective client gate. Every child checks retained whole/stub
counts and actual List, fallback or streaming behavior for startup and 410 recovery.
The script saves source/binary hashes, runtime/control-plane versions, resource
settings and load averages. Successful campaigns remove their compiled test binary.

Sync and startup CPU cover cache start to sync; transport bytes include setup
and sync. Startup peak RSS covers process lifetime through sync + two seconds;
retained heap follows two GCs. Relist fetch time ends at the response/bookmark,
while CPU and memory sampling continue for two seconds. See the
[measurement definitions](../../keps/284-pod-cache-filter/performance.md#environment-and-method)
for the exact scopes and platform differences.

### Linux container from macOS

These are the commands used with a local Podman VM. The example selects its
rootless connection; adjust that connection on other installations. The source
is mounted read-only, results are writable, and the container has no external network.
Install Podman and create a VM first if needed; the example requires at least
4 vCPUs and 8 GiB available to the container. Run the downloader preparation above
before these commands.

```sh
podman machine start podman-machine-default
bin/setup-envtest use 1.31.0 --os linux --arch "$(go env GOARCH)" --bin-dir "$PWD/bin" -p path
PODCACHE_RESULTS_DIR="$(dirname "$PWD")/podcache-results-linux"
mkdir -p "$PODCACHE_RESULTS_DIR"
podman --connection podman-machine-default pull docker.io/library/golang:1.26.0-bookworm
podman --connection podman-machine-default run --rm \
  --cpus=4 --memory=8g --network=none --security-opt label=disable \
  -v "$PWD:/workspace:ro" -v "$PODCACHE_RESULTS_DIR:/results" \
  -v rbg-podcache-go126-cache:/go-cache -w /workspace \
  -e GOTOOLCHAIN=local -e GOCACHE=/go-cache -e GOMAXPROCS=4 \
  -e CGO_ENABLED=1 -e USE_EXISTING_CLUSTER=false \
  -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory \
  -e GIT_CONFIG_VALUE_0=/workspace \
  docker.io/library/golang:1.26.0-bookworm \
  bash test/podcacheunion/scripts/transform-campaign.sh /results/run
```

For correctness, use the same mounts/image and replace the final script command
with the two `go test -race` commands above; set their gate and mutation-detector
variables inside the container. On a Linux host, install native envtest assets
and run the same test/campaign commands directly. Use the actual host architecture
when downloading assets.

### Summary and diagnostics

Summarize **one platform and campaign at a time**; groups do not label platforms:

```sh
RBG_POD_UNION_TRANSFORM_SUMMARY_DIR=/path/to/one-platform-run \
go test -mod=vendor -tags=podcacheunion ./test/podcacheunion \
  -run '^TestTransformComparisonSummary$' -count=1 -v
```

`summary.md` contains medians and ranges. Per-mode JSON and native CSV plus
`results.json` preserve the raw observations in the campaign output directory;
these generated files are not committed. Temporary test kubeconfigs stay outside
saved reports and are deleted by the fixture.

`RBG_POD_UNION_TRANSFORM_DIAG=1` opts into
`TestAPITransformSyncHeapTimeline`; enable client/server streaming as above to
observe early versus near-Replace transform calls. Its intrusive heap sampling
is diagnostic and is excluded from performance medians.

The native sampler is compiled from `testdata/memory_sampler_<GOOS>.c` using
`clang` on macOS or `cc` on Linux. macOS reports physical footprint; Linux reports
sampled RssAnon, not the same measure. Process RSS/heap results do not measure
container cgroup memory, working set or OOM behavior.

The namespace field selector keeps the production single-informer layout for
isolation. `DefaultNamespaces` introduces controller-runtime's
`multiNamespaceInformer`, which the existing no-deepcopy type assertion does
not support independently of this proposal. Supporting that configuration needs
an adapter and regression coverage during production integration.

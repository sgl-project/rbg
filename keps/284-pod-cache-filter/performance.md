# KEP-284: Pod cache measurements

[KEP](README.md) · [Harness and commands](../../test/podcacheunion/README.md)

Measured on **2026-11-01**, with the same prototype sources on macOS and a local Linux container.
All 60 comparison rounds and 240 cache processes passed. Source hashes, summary values,
per-mode versus combined JSON, and all 29,335 native CSV samples were checked locally.
Generated JSON, CSV and logs are not committed; the published harness reruns the same comparison
and generates its own raw results.

## Findings

- **Keep the transform design for the cache prototype.** It preserves one informer, the owner
  index, scoped Lists, no-deepcopy reads, and named existence checks. Its retained-heap reduction
  at 200 / 4,800 is 91.2% on both platforms. The functional tests exercise the
  selected watch wrapper, admission changes, nonempty Pod status, and replacement snapshots after 410.
- **A List still requires decoding all Pods.** Its startup memory cannot be bounded by the admitted
  Pod count. RSS varies with allocation and GC timing; the ranges below matter as much as the medians.
- **Streaming needs the early watch transform.** At 200 / 4,800, ordinary transform versus the
  selected wrapper peaks at 346.66 / 70.61 MiB on macOS and 358.43 / 84.09 MiB on Linux.
- **CPU and latency costs depend on the environment.** In the same streaming-server batch at
  200 / 4,800, `full+wl` / `full` startup CPU is 3.83× on macOS and 0.72× on
  Linux. The previous blanket “about 3.7× CPU” and “1.8–7× sync time” claims do not hold across
  these platforms. List-based filtering also has varying CPU overhead; the macOS sample is
  309 → 359 ms, while Linux is 562 → 492 ms. These are sample medians, not a zero-overhead guarantee.
- **The union has a real advantage in sparse shared shapes:** it retains no unrelated stubs and
  fetches fewer objects. It also requires four sources and a merge state machine. This dataset
  gives every related Pod all four labels, so its duplicate traffic and timing are the maximum-overlap
  case; they do not establish a universal transform-versus-union speedup.
- With no unrelated Pods, the transform's roughly 11% retained-heap reduction comes from stripping
  `managedFields`. The full cache could adopt that operation independently.

## Environment and method

| Setting | macOS | Linux |
| --- | --- | --- |
| OS / architecture | macOS 15.5 (24F74), arm64 | Debian 12 container, arm64; kernel 6.17.7-300.fc43.aarch64 |
| Resources | 14 host CPUs, 48 GiB; `GOMAXPROCS=4` | local Podman VM: 7 vCPUs / 16 GiB; container quota 4 CPUs / 8 GiB; `GOMAXPROCS=4` |
| Runtime | Go 1.26.0 | Go 1.26.0, `golang:1.26.0-bookworm` |
| Control plane | Kubernetes v1.31.0, etcd 3.5.15 | same versions, isolated loopback; container network disabled |

Both use client-go v0.34.2, controller-runtime v0.22.1, `GOGC=100`, no `GOMEMLIMIT`, no race
instrumentation, and the cache mutation detector disabled for measurement. Correctness tests run
separately with `-race` and the mutation detector enabled. The host also ran other work; load
averages are saved in each campaign log. Compare modes within each platform and configuration.

- Synthetic unscheduled Pods: 64 environment values of 512 bytes each. Related Pods carry all four
  admission labels; unrelated Pods carry `app=unrelated`. Payload sizes and label overlap are synthetic.
- Five shapes, three rounds each, for both server configurations: `WatchList` off (including client
  fallback) and `WatchList=true`. Each round starts a fresh API server and dataset; every cache mode
  runs in its own process, sequentially, with mode order rotated. Platforms also ran sequentially.
- **Sync / startup CPU:** cache start to `HasSynced`, polled every 5 ms; CPU is process user + system
  CPU. **Decoded response bytes:** transport body bytes during setup and sync, including discovery;
  this is a payload measure, not compressed wire traffic.
- **Startup RSS:** kernel lifetime high-water mark read at sync + two seconds, without explicit GC
  in that window. **Retained heap:** process Go `HeapAlloc` after two GCs, including fixed test-binary
  and client overhead, rather than just Pod objects. A lower heap does not imply immediate RSS return.
- **Relist:** inject 410 into the open watch. Fetch time runs from the next request to the List
  response or initial-events-end bookmark; it excludes reconnect backoff and later cache processing.
  CPU / bytes / native memory cover expiry through fetch completion + two seconds. The separate
  `TestAPITransformRelist` changes admission while disconnected and checks the applied resourceVersions.
- **Footprint:** macOS physical footprint from `proc_pid_rusage`; Linux sampled `RssAnon` from
  `/proc/PID/status`. These are different measures. Linux's 50 ms samples can miss short peaks;
  peak RSS comes independently from `getrusage`. A negative relist growth means sampled memory
  fell during that window.

### Modes

| Mode | Meaning |
| --- | --- |
| `full` | current Pod cache, no transform |
| `union-serial` | rejected four-source union, serial initial Lists; no relist injection |
| `transform-labels-owners-stream` | selected stub + early watch wrapper, WatchListClient off |
| `transform-labels-owners+wl` | store transform only, WatchListClient on; streaming control |
| `transform-labels-owners-stream+wl` | selected stub + early watch wrapper, WatchListClient on |
| `full+wl` | full cache with WatchListClient on |

`+wl` with server WatchList off must show attempted streaming and List fallback; with it on,
startup and relist must show zero Lists and at least two initial-events-end bookmarks. All
processes also assert whole/stub counts and managedFields retention.

The test harness explicitly sets the effective feature gate in `TestMain`. The production import
chain replaces client-go's environment-reading gate; **the operator cannot rely on
`KUBE_FEATURE_WatchListClient` alone**. The KEP proposes `--watch-list-client` wired to
`utilfeature.DefaultMutableFeatureGate.Set`; production integration is still pending.

## Results

Medians in MiB; each cell's three values follow its heading. `filtered` is the KEP's stub **with
its early watch wrapper**. Heap and List columns come from the List-server batch; all streaming
columns use WatchListClient on in the streaming-server batch. The tables below give the shape
medians and detailed ranges for the 200 / 4,800 case; the harness generates full summaries for
each new campaign.

| Platform | Related / unrelated Pods | Retained heap: full / union / filtered | List startup RSS: full / union / filtered | Streaming startup RSS: full / store-only transform / filtered |
| --- | --- | --- | --- | --- |
| macOS arm64 | 48 / 1,000 | 47.31 / 5.20 / 6.61 | 124.38 / 51.02 / 125.30 | 99.69 / 103.05 / 45.39 |
| macOS arm64 | 900 / 100 | 45.41 / 42.06 / 37.03 | 120.33 / 186.27 / 120.59 | 98.31 / 101.34 / 90.89 |
| macOS arm64 | 1,000 / 0 | 45.42 / 46.32 / 40.61 | 120.73 / 202.12 / 120.64 | 101.20 / 100.89 / 90.50 |
| macOS arm64 | 200 / 4,800 | 214.04 / 11.74 / 18.84 | 672.22 / 74.66 / 677.70 | 337.81 / 346.66 / 70.61 |
| macOS arm64 | 5,000 / 0 | 214.70 / 218.46 / 190.63 | 675.73 / 800.97 / 675.94 | 341.80 / 344.81 / 325.47 |
| Linux arm64 | 48 / 1,000 | 47.30 / 5.20 / 6.60 | 172.15 / 56.69 / 130.77 | 108.52 / 108.88 / 55.86 |
| Linux arm64 | 900 / 100 | 45.40 / 42.08 / 37.02 | 166.71 / 186.53 / 166.64 | 103.61 / 107.74 / 99.16 |
| Linux arm64 | 1,000 / 0 | 45.41 / 46.31 / 40.60 | 166.57 / 205.65 / 126.01 | 105.53 / 104.74 / 100.58 |
| Linux arm64 | 200 / 4,800 | 214.02 / 11.73 / 18.83 | 677.64 / 80.00 / 628.00 | 372.54 / 358.43 / 84.09 |
| Linux arm64 | 5,000 / 0 | 214.69 / 218.45 / 190.62 | 681.05 / 872.17 / 631.20 | 367.69 / 359.53 / 337.25 |

### Startup detail: 200 related / 4,800 unrelated

Median [minimum–maximum], three runs per row. Server `off` plus `+wl` is the actual fallback path.

| Platform | Server WatchList | Mode | Retained heap (MiB) | Peak RSS (MiB) | Sync (ms) | Startup CPU (ms) | Decoded response (MiB) |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| macOS arm64 | off | `full` | 214.04 [214.04–214.05] | 672.22 [671.89–672.38] | 369 [366–396] | 309 [295–313] | 184.6 [184.6–184.6] |
| macOS arm64 | off | `union-serial` | 11.74 [11.74–11.75] | 74.66 [70.67–75.20] | 1335 [1285–1432] | 94 [93–100] | 29.8 [29.8–29.8] |
| macOS arm64 | off | `transform-labels-owners-stream` | 18.84 [18.83–18.84] | 677.70 [677.66–677.78] | 451 [387–454] | 359 [329–369] | 184.6 [184.6–184.6] |
| macOS arm64 | off | `transform-labels-owners-stream+wl` | 18.84 [18.84–18.84] | 677.98 [677.89–677.98] | 361 [344–382] | 296 [294–301] | 184.6 [184.6–184.6] |
| macOS arm64 | on | `full` | 214.04 [214.04–214.05] | 672.22 [672.12–672.31] | 406 [396–416] | 345 [329–367] | 184.6 [184.6–184.6] |
| macOS arm64 | on | `full+wl` | 214.23 [214.22–214.23] | 337.81 [326.17–340.72] | 754 [749–755] | 1322 [1317–1325] | 184.7 [184.7–184.7] |
| macOS arm64 | on | `transform-labels-owners+wl` | 18.95 [18.95–18.95] | 346.66 [343.06–368.86] | 759 [753–779] | 1296 [1295–1307] | 184.7 [184.7–184.7] |
| macOS arm64 | on | `transform-labels-owners-stream+wl` | 18.95 [18.95–18.96] | 70.61 [70.11–70.66] | 748 [719–807] | 1363 [1309–1397] | 184.7 [184.7–184.7] |
| Linux arm64 | off | `full` | 214.02 [214.02–214.03] | 677.64 [470.92–677.82] | 572 [526–643] | 562 [463–686] | 184.6 [184.6–184.6] |
| Linux arm64 | off | `union-serial` | 11.73 [11.73–11.74] | 80.00 [79.29–81.29] | 992 [903–1067] | 97 [97–110] | 29.8 [29.8–29.8] |
| Linux arm64 | off | `transform-labels-owners-stream` | 18.83 [18.83–18.83] | 628.00 [627.92–628.48] | 545 [454–590] | 492 [412–621] | 184.6 [184.6–184.6] |
| Linux arm64 | off | `transform-labels-owners-stream+wl` | 18.83 [18.83–18.84] | 627.51 [476.35–683.48] | 474 [461–679] | 449 [409–590] | 184.6 [184.6–184.6] |
| Linux arm64 | on | `full` | 214.03 [214.03–214.04] | 629.01 [628.49–678.52] | 658 [649–774] | 715 [683–886] | 184.6 [184.6–184.6] |
| Linux arm64 | on | `full+wl` | 214.23 [214.22–214.24] | 372.54 [359.98–375.19] | 769 [726–1061] | 515 [494–596] | 184.7 [184.7–184.7] |
| Linux arm64 | on | `transform-labels-owners+wl` | 18.96 [18.95–18.97] | 358.43 [356.37–381.69] | 655 [416–1077] | 521 [498–549] | 184.7 [184.7–184.7] |
| Linux arm64 | on | `transform-labels-owners-stream+wl` | 18.96 [18.95–18.96] | 84.09 [83.23–90.47] | 385 [378–508] | 526 [521–541] | 184.7 [184.7–184.7] |

### Relist detail: 200 related / 4,800 unrelated

Median [minimum–maximum]. Footprint columns use each platform's definition above. They are
process measures; the union's relist was not measured by this comparison.

| Platform | Server WatchList | Mode | Relist fetch (ms) | Relist peak footprint (MiB) | Footprint growth (MiB) | Relist CPU (ms) | Decoded response (MiB) |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| macOS arm64 | off | `full` | 433 [377–590] | 994.0 [904.0–1008.7] | 344.5 [254.0–358.7] | 328 [319–349] | 184.5 [184.5–184.5] |
| macOS arm64 | off | `transform-labels-owners-stream` | 399 [397–453] | 649.3 [648.0–651.4] | -5.8 [-7.1–-3.5] | 357 [348–362] | 184.5 [184.5–184.5] |
| macOS arm64 | off | `transform-labels-owners-stream+wl` | 411 [409–441] | 648.7 [647.4–653.0] | -6.4 [-8.0–-2.2] | 390 [366–396] | 184.5 [184.5–184.5] |
| macOS arm64 | on | `full` | 427 [417–434] | 996.9 [991.8–1006.7] | 347.2 [342.1–357.0] | 320 [312–321] | 184.5 [184.5–184.5] |
| macOS arm64 | on | `full+wl` | 754 [753–961] | 599.0 [598.2–600.7] | 285.8 [281.2–295.2] | 1317 [1304–1354] | 184.7 [184.7–184.7] |
| macOS arm64 | on | `transform-labels-owners+wl` | 788 [777–978] | 332.2 [327.1–339.3] | 15.6 [6.9–17.9] | 1367 [1320–1367] | 184.7 [184.7–184.7] |
| macOS arm64 | on | `transform-labels-owners-stream+wl` | 957 [771–1152] | 79.9 [78.9–80.1] | 45.0 [41.4–45.2] | 1397 [1368–1414] | 184.7 [184.7–184.7] |
| Linux arm64 | off | `full` | 481 [442–654] | 879.2 [871.6–884.9] | 233.3 [224.2–445.5] | 470 [366–580] | 184.5 [184.5–184.5] |
| Linux arm64 | off | `transform-labels-owners-stream` | 664 [411–718] | 644.3 [642.6–845.2] | 71.5 [64.0–300.0] | 712 [431–771] | 184.5 [184.5–184.5] |
| Linux arm64 | off | `transform-labels-owners-stream+wl` | 546 [539–634] | 644.3 [644.2–737.7] | 80.5 [-7.9–293.0] | 627 [566–651] | 184.5 [184.5–184.5] |
| Linux arm64 | on | `full` | 628 [529–647] | 872.8 [849.1–880.4] | 270.1 [233.2–312.7] | 485 [471–549] | 184.5 [184.5–184.5] |
| Linux arm64 | on | `full+wl` | 521 [439–884] | 606.5 [604.8–607.2] | 271.2 [263.6–277.9] | 562 [484–938] | 184.7 [184.7–184.7] |
| Linux arm64 | on | `transform-labels-owners+wl` | 733 [456–940] | 349.7 [349.3–374.3] | 44.1 [7.2–63.8] | 657 [571–708] | 184.7 [184.7–184.7] |
| Linux arm64 | on | `transform-labels-owners-stream+wl` | 467 [432–508] | 84.6 [82.5–89.4] | 36.5 [33.3–40.0] | 613 [613–637] | 184.7 [184.7–184.7] |

## Coverage and limits

The unit and real-API integration tests passed on both platforms with server streaming off and on,
including admission, exact stub contents, full Pod status, scoped Lists with/without deep copy,
owner-index reads, admission transitions, delete events, watch-loop exit, and 410 replacement.
The production utility is exercised for owner-index reads; the named-conflict test checks cached
Get plus ownership semantics. Complete reconciler conflict outcomes still need operator integration.
Initial Linux startup failed without a default route; setting the test API server's advertise
address to `127.0.0.1` fixed it while keeping the container offline. The original verification
output is retained locally rather than checked into the repository.

These arm64, static-dataset experiments do not cover controller reconciliation throughput,
steady event churn, amd64, real-cluster OOM limits, or cgroup memory/working set. The container's
quota is recorded, but the measured memory belongs to the cache process. Creation-time marking,
CLI / Helm migration, workload rollout and upgrade/rollback e2e remain production-integration
criteria. The prototype supports the architecture and these measured shapes; alpha defaults
remain `all`, with WatchListClient independently off.

## Reproduction and provenance

Run the [harness commands](../../test/podcacheunion/README.md) with a fresh output directory
outside the checkout:

```sh
GOMAXPROCS=4 bash test/podcacheunion/scripts/transform-campaign.sh /path/to/new-campaign
```

The default matrix now measures the selected wrapper with streaming disabled, real fallback,
and streaming enabled, with the store-only transform as the streaming control. Summarize each
platform/campaign separately; the summary groups by shape and mode and does not label platforms.

- The recorded campaign used base main `4a0765f78b0930114a4b73a64433ee8f27c34372` and the
  prototype in `test/podcacheunion`; environment and resource settings are listed above.
- Each new campaign writes source/binary SHA-256 values, version information, load averages,
  summaries and per-round raw JSON, CSV and logs to its output directory. These generated files
  stay outside version control. Temporary kubeconfigs stay outside the results and are removed.
- `TestAPITransformSyncHeapTimeline` confirms early transformation versus transformation near
  `Replace`; its intrusive heap sampling is diagnostic and excluded from performance medians.

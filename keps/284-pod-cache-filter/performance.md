# KEP-284: Performance Measurements

Status: prototype measurements, not production capacity. Baseline `91fcdbe`
(`doc/pod-cache-filter-kep`), measured 2026-10-29/30 on macOS arm64.

This report is the single supporting measurement document for KEP-284. It compares the current
unfiltered Pod cache with the four-source union in its parallel, serial-initial, and bounded-page
forms. The raw prototype harness is a local artifact and is not part of this PR.

## Summary

- **Shared cluster (48 related / 1,000 unrelated Pods):** retained heap falls from 46.91 MiB to
  4.82 MiB (−89.7%), peak RSS from 165.08 MiB to 47.03 MiB (−71.5%), and initial sync grows from
  101 ms to 301 ms.
- **Mostly related (900 / 100):** retained heap falls 7.3% (45.01 → 41.71 MiB), but peak RSS rises
  20.3% (159.81 → 192.31 MiB) and sync grows from 101 ms to 701 ms.
- **Dedicated (1,000 / 0):** retained heap is 2.1% higher (45.02 → 45.94 MiB) and peak RSS 30.3%
  higher (159.92 → 208.34 MiB); filtering buys no Pod payload here.
- Serial initialization avoids the parallel union's 275.95 / 292.36 MiB peak. Bounded page merging
  is lower still (115.58 / 123.70 MiB) at the same sync time, but needs a custom page-merge store;
  the KEP keeps serial initialization for the first implementation.

The memory win is a shared-cluster argument. In a dedicated cluster the union adds a startup peak
and roughly 2% retained-heap overhead, and saves no Pod payload.

## Environment and Method

- macOS 15.5 arm64 (24F74), Go 1.26.0.
- Isolated kube-apiserver v1.31.0 + etcd 3.5.15, started by the prototype; no external cluster or
  kubeconfig.
- Synthetic Pods, each with 64 environment values of 512 bytes; unscheduled; no kubelet,
  application containers, or native workload controllers.
- Shapes: 48 related / 1,000 unrelated, 900 / 100, and 1,000 / 0. Every related Pod matches all
  four selectors, so this is a maximum-overlap dataset.
- Modes: `full` (ordinary informer, default pagination, no Pod label filter),
  `union-shared` (parallel full source snapshots), `union-serial` (one complete source at a time),
  `union-bounded` (page-by-page merge with one in-flight page; comparison only).
- Three independent rounds per shape. Each round starts a fresh control plane and runs one cache
  mode per process, with the mode order rotated so no mode always runs first.
- Peak window: process start through observed cache sync plus two seconds. No explicit GC or
  `FreeOSMemory` during the window. Historical peak RSS and native physical footprint are recorded,
  not a single point sample.
- Retained heap: two GCs after the peak window and after the native sampler stops.
- Sync time is polled at roughly 100 ms granularity; it is a coarse startup measurement, not a
  latency benchmark.
- The ordinary full cache does not attach union request counters, so its filtered List/Watch counts
  are not applicable rather than zero.
- The same harness passed 23 functional/state cases under the race detector and cache mutation
  detector, with one memory test skipped in that run.

## Results

Medians with the observed range across the three runs. Heap is post-GC retained heap; RSS and
footprint are process peaks during the startup window.

| Related / unrelated | Mode | Retained heap (MiB) | Peak RSS (MiB) | Peak footprint (MiB) |
| --- | --- | ---: | ---: | ---: |
| 48 / 1,000 | full | 46.91 [46.90–46.92] | 165.08 [164.95–165.41] | 143.3 [143.3–143.6] |
| 48 / 1,000 | union-shared | 4.81 [4.81–4.81] | 52.64 [51.62–53.75] | 29.3 [28.3–30.6] |
| 48 / 1,000 | union-serial | 4.82 [4.81–4.82] | 47.03 [46.62–48.58] | 24.0 [23.6–25.3] |
| 48 / 1,000 | union-bounded | 4.82 [4.81–4.82] | 46.64 [46.42–46.84] | 23.3 [22.9–23.5] |
| 900 / 100 | full | 45.01 [45.01–45.02] | 159.81 [159.72–160.06] | 138.0 [137.9–138.2] |
| 900 / 100 | union-shared | 41.72 [41.69–41.72] | 275.95 [272.78–281.14] | 252.9 [249.7–258.1] |
| 900 / 100 | union-serial | 41.71 [41.70–41.71] | 192.31 [189.08–197.84] | 169.1 [165.8–174.3] |
| 900 / 100 | union-bounded | 41.72 [41.71–41.73] | 115.58 [115.47–119.38] | 92.4 [92.3–96.2] |
| 1,000 / 0 | full | 45.02 [45.01–45.02] | 159.92 [159.81–160.03] | 138.1 [138.1–138.1] |
| 1,000 / 0 | union-shared | 45.96 [45.96–45.96] | 292.36 [288.09–315.64] | 269.3 [264.6–292.6] |
| 1,000 / 0 | union-serial | 45.94 [45.94–45.94] | 208.34 [207.03–215.75] | 185.0 [183.6–192.2] |
| 1,000 / 0 | union-bounded | 45.97 [45.96–45.97] | 123.70 [123.64–128.56] | 100.6 [100.5–105.3] |

| Related / unrelated | Mode | Sync (ms) | Allocation to sync (MiB) |
| --- | --- | ---: | ---: |
| 48 / 1,000 | full | 101 [101–102] | 171 |
| 48 / 1,000 | union-shared | 301 [201–301] | 34 |
| 48 / 1,000 | union-serial | 301 [300–301] | 34 |
| 48 / 1,000 | union-bounded | 301 [301–301] | 34 |
| 900 / 100 | full | 101 [101–101] | 166 |
| 900 / 100 | union-shared | 301 [300–301] | 642 |
| 900 / 100 | union-serial | 701 [701–801] | 641 |
| 900 / 100 | union-bounded | 701 [700–801] | 646 |
| 1,000 / 0 | full | 101 [101–101] | 166 |
| 1,000 / 0 | union-shared | 301 [300–301] | 713 |
| 1,000 / 0 | union-serial | 701 [700–800] | 712 |
| 1,000 / 0 | union-bounded | 701 [700–702] | 718 |

### Per-run peak RSS

| Shape | Run | full | union-serial | union-bounded |
| --- | ---: | ---: | ---: | ---: |
| 48 / 1,000 | 1 | 165.41 | 47.03 | 46.84 |
| 48 / 1,000 | 2 | 165.08 | 46.62 | 46.64 |
| 48 / 1,000 | 3 | 164.95 | 48.58 | 46.42 |
| 900 / 100 | 1 | 159.72 | 192.31 | 119.38 |
| 900 / 100 | 2 | 160.06 | 189.08 | 115.58 |
| 900 / 100 | 3 | 159.81 | 197.84 | 115.47 |
| 1,000 / 0 | 1 | 159.92 | 208.34 | 123.64 |
| 1,000 / 0 | 2 | 159.81 | 215.75 | 128.56 |
| 1,000 / 0 | 3 | 160.03 | 207.03 | 123.70 |

## Analysis

### Startup peak

Serial initialization holds the canonical store plus one complete source snapshot at a time.
Parallel initialization holds up to four snapshots at once; bounded merging holds one page. This is
why the three union modes differ at startup even though their retained heap is the same.

At high related share the store already holds almost every Pod, so one more full source snapshot
makes the serial peak larger than the full cache's single initial list (900 / 100: +20.3%;
1,000 / 0: +30.3%). At low related share the filtered snapshot is small, so the union peak falls far
below the full cache (48 / 1,000: −71.5%).

### Retained heap

Retained heap tracks admitted Pods, not total Pods. At 95.4% unrelated Pods the full cache retains
about 10x the union's heap; at 900 / 100 the union retains 7.3% less; with no unrelated Pods the
union is 2.1% higher because of source identities and merge metadata. This is the measured form of
the KEP's "benefit depends on the unrelated-Pod share" statement.

### Sync time and allocation

Serial initialization costs 301–801 ms in these shapes, 3–8x the full cache's 101 ms, and grows with
the number of admitted Pods. In the two high-related shapes, cumulative allocation to sync is
641–718 MiB versus 166 MiB for the full cache, about 4x, because four overlapping sources read and
decode the same related Pods. This is cumulative allocation, not live memory, and it is the main
startup CPU/API cost of the design.

### Bounded page merging

Bounded merging keeps one page in flight and merges it immediately, so its peak RSS is lower than
serial at the same 701 ms sync (900 / 100: 115.58 vs 192.31 MiB; 1,000 / 0: 123.70 vs 208.34 MiB).
It is not the first implementation because it requires a custom page-merge store and its own
continuation, timeout, and snapshot-consistency handling; the KEP records that trade-off in
Alternatives. The measurement is kept here so the decision is auditable.

## Caveats

- macOS RSS and physical footprint, not Linux cgroup memory, working set, or OOM behavior. A
  production conclusion needs measurement in the target container.
- Synthetic, unscheduled Pods with one process per cache mode. No kubelet, application containers,
  native workload controllers, real churn, or leader failover.
- The peak window includes process and Go runtime startup. Sync timing has roughly 100 ms polling
  granularity.
- Cross-batch comparisons are unsafe: an earlier 48 / 1,000 batch measured the full cache at
  122.97 MiB RSS, while this same-batch run measured 165.08 MiB. Only same-batch differences are
  used above.
- This is the prototype cache path, not the production operator. Startup peaks and behavior with a
  real API server load, multiple replicas, and Linux memory accounting remain to be measured.

## Provenance

- Raw data: local `measurements/full-cache-comparison-20261030.json` (not part of this PR).
- Baseline `91fcdbe`; three repeats per shape; mode order rotated; source and binary SHA-256 are
  recorded in the raw data.
- Verification run: 23 functional/state cases passed under race and cache mutation detection, one
  memory test skipped.
- The ordinary full-cache baseline is the existing cache path with no Pod label filter and default
  pagination; the union modes share immutable Pod objects between the merge records and the
  business index when no transform is configured.

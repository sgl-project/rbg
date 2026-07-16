# PR #394 restart-backoff — bug verification

Reproducible evidence for the suspected bugs raised while reviewing
[sgl-project/rbg#394](https://github.com/sgl-project/rbg/pull/394)
("feat: add exponential backoff for restart policy").

The harness has three layers, all run against the **PR head** (`0c0fcc11`):

| Layer | What it exercises | How to run |
|-------|-------------------|------------|
| 1. Go function tests | pure logic (`calculateRestartDelay`, `updateRestartTracking`, `checkRestartBackoff`) | `go test ./pkg/reconciler/roleinstance/sync/ -run RestartBackoffVerify -v` |
| 2. envtest | real kube-apiserver + the actual reconcilers | `make test-envtest` (or focus `PR394`) |
| 3. live cluster | the real controller (host-run) + real pods on `~/.kube/rbg` | `scripts/00-setup.sh` → run controller → `scripts/10-live-backoff.sh` / `20-live-negative-delay.sh` → `scripts/99-teardown.sh` |

> Test polarity: B1/B2/B4 tests assert the **intended** contract, so a **FAIL = bug reproduced**.
> B5 tests assert the **observed (buggy)** behavior, so a **PASS = bug present**.

## Summary of results

| ID | Claim | Layer | Verdict | Evidence |
|----|-------|-------|---------|----------|
| **B1** | int64 overflow disables backoff for `restartCount ≥ 59` | 1 | **Confirmed** | `calculateRestartDelay(30,600,59)=0` (should be 600); 0 for all ≥59 |
| **B2** | stable-period reset is clobbered once count > 1 | 2 | **Confirmed** | seeded count=5 + backdated LRT + crash → count stays **5**, never resets to 1 |
| **B4** | negative `baseDelaySeconds` accepted & bypasses backoff | 1 + 2 | **Confirmed** | apiserver accepts `-30` on RoleInstance (rejects on RBG); `checkRestartBackoff`→`0s` |
| **B5** | first realized backoff is `2×base`, not `base` | 1 + 3 | **Confirmed** | unit: first delay=60s (base=30); live: controller logs `delay=120s` at base=60 |
| lint | empty branch (SA9003) + gocyclo 33 | CI | **Confirmed** | already red in PR CI; static, not re-run here |

## B1 — int64 overflow (Layer 1)

`calculateRestartDelay` applies the `maxDelay` cap *after* `base * (1<<restartCount)`,
so the multiply overflows int64 before the cap is checked.

```
calculateRestartDelay(base=30, max=600, restartCount=58) = 600   # correct (capped)
calculateRestartDelay(base=30, max=600, restartCount=59) = 0     # BUG: cap bypassed
calculateRestartDelay(base=30, max=600, restartCount=64) = 0     # shift zeroes
calculateRestartDelay(base=30, max=600, restartCount=100)= 0
```

A `0`/negative return makes `checkRestartBackoff` treat it as "no backoff" → furious
restarts return for a long-running crashloop. Harness sanity check: applying the
proposed fix (short-circuit on `restartCount` before the shift) turns all of these
back into `600` and the tests pass. Full output: `results/layer1-gotest.txt`.

## B5 — off-by-one (Layers 1 & 3)

`updateRestartTracking` increments `RestartCount` to `1` *before* the first backoff
check, so `calculateRestartDelay` never runs with `restartCount==0` at runtime. The
smallest realized delay is `calculateRestartDelay(base,max,1) = 2*base`.

- Unit: first realized delay = **60s** with base=30 (docs/PR table say 30s).
- Live: controller log with base=60 →
  `Restart backoff: ... waiting 1m59s (restartCount=1, delay=120s)` — **120s = 2×base**.
  See `results/live-offbyone-evidence.log`.

## B2 — stable-period reset clobbered (Layer 2)

`updateStatus` unconditionally keeps the larger live `RestartCount`
(`if liveRestartCount > newStatus.RestartCount`), which overrides the reset-to-1 that
`updateRestartTracking` performs after a stable period. Seeding count=5, backdating
`LastRestartTime` past the reset threshold, and triggering a fresh crash leaves the
count at **5** (observed repeatedly), never returning to 1. The shipped
`1 → 1` envtest can't catch this because `1 > 1` is false. Full output:
`results/layer2-envtest.txt`.

## B4 — negative delay / missing validation (Layers 1 & 2)

The RBG pattern fields carry `+kubebuilder:validation:Minimum=0`, but the
`RoleInstanceSpec` fields do not. Against the envtest apiserver:

```
create RBG(baseDelaySeconds=-30)          -> rejected: "should be greater than or equal to 0"
create RoleInstance(baseDelaySeconds=-30) -> err = <nil>   (accepted; validation gap)
```

And the logic: `checkRestartBackoff` with `base=-30` returns `0s` (backoff bypassed),
vs `>0` with `base=30`. Full output: `results/layer2-envtest.txt`.

## Live cluster run (Layer 3)

Controller run out-of-cluster against `~/.kube/rbg` (no image build). RBG uses
`leaderWorkerPattern` (the delay getters only read LW/CustomComponents),
`baseDelaySeconds=60`, `maxDelaySeconds=600`, image
`registry.cn-hangzhou.aliyuncs.com/acs-sample/nginx:latest`. Pods run on real
`cn-hongkong` nodes. Crashes triggered with `kubectl exec ... -- sh -c 'kill 1'`
(container restarts under `restartPolicy=Always`, so `RestartCount>0` fires the policy).

Observations (see `results/live-backoff.log`, `results/controller-backoff-lines.log`):
- crash #1 (no prior history) → recreation in **5s** (no backoff).
- crash #2 (6s into the window) → recreation **held for 117s**; the crashed pod is
  **preserved** (same pod, `RESTARTS=1`, still Running) throughout.
- controller logged the full exponential progression live:
  - `restartCount=1, delay=120s` → **2×base** (base=60) — the off-by-one (B5)
  - `restartCount=2, delay=240s` → **4×base** — exponential growth working
- A transient `FailedScale` condition ("pod ... already exists") was observed once
  during a recreate/backoff transition — noted as a rough edge, not a primary finding.

## Files

```
scripts/00-setup.sh              install CRDs + create ns (then run controller per README)
scripts/rbg-backoff.yaml         test RBG (leaderWorkerPattern, base=60, max=600)
scripts/10-live-backoff.sh       crash#1 (immediate) + crash#2 (backoff, preserved pod)
scripts/20-live-negative-delay.sh  B4 live: RBG rejects -30, RoleInstance accepts -30
scripts/99-teardown.sh           delete ns, stop controller (UNINSTALL_CRDS=true to drop CRDs)
results/                         captured logs and command output
pkg/reconciler/roleinstance/sync/restart_backoff_verify_test.go   Layer 1 (B1, B4, B5)
test/envtest/testcase/restart_policy/backoff_bug_verify_test.go   Layer 2 (B2, B4)
```

## Proposed fixes (not applied to production code here)

- **B1**: cap on `restartCount` before shifting —
  `if maxDelaySeconds>0 && (restartCount>=31 || int64(base)<<restartCount > int64(max)) { return max }`.
- **B2**: version the `(RestartCount, LastRestartTime)` pair together; only preserve the
  live count when the live timestamp is newer than `newStatus`.
- **B4**: add `+kubebuilder:validation:Minimum=0` to the `RoleInstanceSpec` delay fields
  (and consider a CEL rule `maxDelaySeconds >= baseDelaySeconds`).
- **B5**: use `1 << (restartCount-1)` to match the documented "first retry = base", or
  correct the docs/PR table to `2×base`.

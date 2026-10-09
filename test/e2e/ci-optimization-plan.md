# E2E CI Optimization Plan

## Expected results

1. Every PR still executes the full e2e coverage, but the default-scheduler suite is split across parallel matrix slices:
   - lifecycle;
   - workload;
   - update;
   - rollout;
   - feature;
   - webhook/RBAC;
   - default-scheduler scheduling;
   - v1alpha1 compatibility.
2. The special installation and scheduler suites run on every eligible PR:
   - committed-manifest installation;
   - deprecated-workload-types disabled;
   - Volcano gang scheduling;
   - scheduler-plugins gang scheduling.
3. Controller and CRD upgrader images are built once per workflow run and reused by every matrix job.
4. Common Kind, LWS, scheduler, image-loading, and RBGS setup is centralized in a composite action.
5. Ginkgo emits JUnit and JSON reports, which are uploaded even when tests pass.
6. Jobs have bounded timeouts, and a newer push to the same PR cancels the older run.
7. Timing-sensitive tests carry explicit labels and run serially within their slice; the zero-unavailable surge test monitors Pod-level invariants continuously.
8. No issue-comment command or PR label is used to select whether a suite runs.

## Verification

- Static workflow validation:
  - `yamllint -s .github/workflows/e2e-test.yml .github/actions/setup-e2e-cluster/action.yaml`
  - `git diff --check`
- Unit and compile checks:
  - `go test ./test/e2e/... -run '^$'`
- Focused local e2e checks, when a Kind cluster with RBGS is available:
  - `make test-e2e`
  - `make test-e2e E2E_LABEL_FILTER='(lifecycle || workload || update || rollout || feature || webhook || rbac || scheduler) && !volcano && !scheduler-plugins'`
- CI validation:
  - `Build E2E images` completes once;
  - all eight default-scheduler matrix slices are scheduled in parallel;
  - manifest, deprecated-disabled, Volcano, and scheduler-plugins jobs are scheduled without a label or comment;
  - each slice uploads a JUnit/JSON report;
  - the union of all matrix filters covers the full v1alpha1 and v1alpha2 e2e suite.

## Implemented scope

- Added `v1alpha1`/`compat` labels and a dedicated v1alpha1 matrix slice.
- Added functional, rollout, workload, feature, webhook, RBAC, scheduler, slow, and serial labels to v1alpha2 suites.
- Added a build-once `e2e-images` artifact job.
- Added a reusable `.github/actions/setup-e2e-cluster` composite action for Kind, LWS, Volcano, scheduler-plugins, image loading, and RBGS installation.
- Replaced the monolithic PR/full default-scheduler job with an eight-way parallel matrix.
- Kept manifest, deprecated-disabled, Volcano, and scheduler-plugins as independent parallel jobs.
- Added retries for the LWS registry pull and webhook caBundle injection checks, addressing the setup failures observed in the first CI run.
- Added Ginkgo JUnit/JSON reports to e2e Make targets.
- Reworked the zero-unavailable surge rollout test to gate on a stable base, wait for the rollout to begin, and continuously monitor Pod-level maxSurge/readiness invariants until completion.

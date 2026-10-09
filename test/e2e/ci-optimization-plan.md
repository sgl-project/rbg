# E2E CI Optimization Plan

## Expected results

1. PR runs are smaller and cheaper:
   - controller and CRD upgrader images are built once and reused;
   - common Kind/LWS/Helm/deployment setup is centralized;
   - the manifest installation path runs installation-focused smoke coverage, not the full functional suite;
   - v1alpha1 compatibility tests are excluded from the default PR suite.
2. E2E results are easier to diagnose:
   - Ginkgo emits JUnit and JSON reports;
   - reports are uploaded even when tests pass;
   - jobs have bounded timeouts and PR runs are cancelled when a newer commit is pushed.
3. Timing-sensitive behavior is represented explicitly:
   - default PR e2e runs a smoke/core subset;
   - Volcano, scheduler-plugins, and deprecated-disabled suites run on manual/main/label-triggered paths;
   - future serial/parallel work has label hooks available.
4. Label behavior is testable:
   - `run-gang-e2e` enables Volcano and scheduler-plugins suites;
   - `run-compat-e2e` enables the deprecated-disabled suite;
   - adding or removing those labels re-evaluates the PR workflow.

## Verification

- Static workflow validation:
  - `yamllint -s .github/workflows/e2e-test.yml .github/actions/setup-e2e-cluster/action.yaml`
  - `git diff --check`
- Unit and compile checks:
  - `go test ./test/e2e/... -run '^$'`
- Focused local e2e checks, when a Kind cluster with RBGS is available:
  - `make test-e2e`
  - `make test-e2e E2E_LABEL_FILTER='(smoke || rbac || webhook) && !volcano && !scheduler-plugins && !v1alpha1'`
- CI validation:
  - the PR workflow builds the `e2e-images` artifact once;
  - the PR workflow runs the Helm smoke and manifest smoke jobs;
  - adding `run-gang-e2e` triggers both gang scheduling jobs;
  - adding `run-compat-e2e` triggers the deprecated-disabled job;
  - JUnit/JSON reports are attached for both successful and failed runs;
  - no PR e2e job builds controller or CRD upgrader images directly.

## Implemented scope

- Added `v1alpha1`/`compat` labels and excluded v1alpha1 from the default e2e filter.
- Added functional/serial labels to v1alpha2 suites and smoke labels to representative PR coverage.
- Added a build-once `e2e-images` artifact job.
- Added a reusable `.github/actions/setup-e2e-cluster` composite action for Kind, LWS, Volcano, scheduler-plugins, image loading, and RBGS installation.
- Split PR execution into Helm-installed smoke and manifest-installed installation smoke.
- Kept full default-scheduler, deprecated-disabled, Volcano, and scheduler-plugins suites on manual/main/label-triggered runs.
- Added workflow-level concurrency, job timeouts, report artifacts, and diagnostics.
- Added Ginkgo JUnit/JSON reports to e2e Make targets.
- Reworked the zero-unavailable surge rollout test to gate on a stable base, wait for the rollout to begin, and continuously monitor Pod-level maxSurge/readiness invariants until completion.

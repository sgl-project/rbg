# Build, codegen, chart and tests

## Codegen

- **After changing API types, regenerate and commit:**
  - `make manifests` regenerates CRDs, webhook manifests and RBAC. It also regenerates the chart's
    `templates/rbac/clusterrole.yaml` through `hack/gen-helm-rbac`, and `deploy/kubectl/manifests.yaml`.
  - `make generate` regenerates deepcopy code and runs `hack/update-codegen.sh`, which produces
    `client-go/`: the clientset, listers, informers and applyconfigurations.
- **CI fails on generated-code drift.** Project Check runs both targets and fails on any diff. Its
  error message mentions `make update-crd`, which doesn't exist; run `make manifests generate`.
  Never hand-edit generated files or the chart ClusterRole. Resolve a merge conflict in one of them,
  as `deploy/kubectl/manifests.yaml` had in #589, by regenerating it, not by hand.
- **Every new verb or resource the controller touches needs a `+kubebuilder:rbac` marker.** Example:
  patching `roleinstancesets/status` (#508, #478).
- **New Go files need the Apache 2.0 header.** Check with `make copyright-check` (against
  `origin/main`), fix with `make copyright-fix`.

## Helm chart and manifests

There are three install paths, and they must stay in lockstep: the Helm chart (`deploy/helm/rbgs`),
kustomize (`config/`), and `deploy/kubectl/manifests.yaml`.

- **Webhook configurations are hand-maintained in the chart** (`templates/webhook/manifests.yaml`).
  Diff them against `config/webhook/manifests.yaml` and the kubectl manifests (#552).
- **Flipping a chart default changes behavior for every upgrader.** `helm upgrade` without
  `--reuse-values` applies the new chart defaults to every value the user never set (#524, #552).
  - Guard a flip with a pre-upgrade check Job. Give it its own ServiceAccount and ClusterRole, keep
    it independent of `crdUpgrade.enabled`, and make it pass when the CRDs don't exist yet. Model it
    on `templates/crd-upgrade/crd-upgrade.yaml`.
  - Don't infer the default with `lookup` or `.Release.IsInstall`. GitOps renders offline, and
    `IsInstall` changes at the next upgrade.
- **Some install paths skip hooks.** Hooks don't run with `--no-hooks`, with
  `helm template | kubectl apply`, or with the kubectl manifests. Keep safe Go defaults, add a
  startup self-check, and document manual steps.
  - Helm's default `--timeout` is 5 minutes. Document long-running hooks.
- **Bump images everywhere at once:** values, `Chart.yaml`, kustomization and kubectl manifests. A
  `failurePolicy: Fail` webhook pointing at a path the deployed image doesn't serve rejects all
  writes (#552).
- **Old and new controllers overlap during `helm upgrade`.** Old pods keep running while pre-upgrade
  hooks execute, and leadership changes hands. Design for that mixed-version window (#530).
- **One `helm upgrade` with `crdUpgrade.enabled=true` (the default) is the supported upgrade path.**
  Its pre-upgrade hook applies the CRDs before the controller rolls. Don't propose upgrading in two
  passes (#601). Measure and document any window in which the upgrade rejects writes: in #601, one
  upgrade on a cluster rejected 60–86% of RBG writes for about 21.5 s, until the new controller
  served the new webhook entries.
- **Validate the chart:**
  - `make test-chart` renders the chart with `helm template`, without a cluster, and checks the
    deprecated-workload-types toggle, the Pod cache settings, and the manager's probes and preStop
    drain.
  - It needs helm 3.10 or later (`--set-json`; CI pins 3.11.1). Without helm it **skips** instead
    of failing, so a local pass without helm proves nothing.
  - Since #642 it parses only helm's stdout, so warnings on stderr, such as one about kubeconfig
    file permissions, no longer break it. On a branch from before #642, run it with
    `KUBECONFIG=/nonexistent`.
  - CI also runs `helm lint` and `yamllint` on `config/`, `deploy/` and `examples/`.
  - Render `NOTES.txt` for each value combination you touch.

## Make targets

| Target | What it covers | Runs in CI |
|---|---|---|
| `make fmt vet lint` | gofmt, go vet, golangci-lint (incl. `gocyclo`) | every PR (Project Check) |
| `make test` | unit tests; runs `manifests generate fmt vet` first | every PR (Project Check) |
| `make test-envtest` | real API server: CRD validation, defaulting, conversion, webhooks, SSA | PRs touching relevant paths (Envtest runs the equivalent `go test`) |
| `make test-e2e` | Kind e2e; narrow with `E2E_LABEL_FILTER` (default `!volcano && !scheduler-plugins`) | PRs touching relevant paths (E2E Test) |
| `make test-e2e-deprecated-disabled` | `test/e2e/apicompat`, run with deprecated workload types disabled in the chart | PRs touching relevant paths (E2E Test) |
| `make test-chart` | renders the chart without a cluster | every PR (Project Check) |
| `make test-e2e-matrix-coverage` | checks that the E2E matrix slices cover the default suite, without a cluster | every PR (Project Check) |
| `make test-e2e-upgrade` | upgrade from v0.7.0/v0.8.0 without disturbing running Pods (see `upgrade-compatibility.md`) | only on `/release-test` |
| `make copyright-check` | license headers on changed Go files | every PR (Copyright Check) |

CI pins Go in `.github/workflows/project-check.yml` and golangci-lint through
`GOLANGCI_LINT_VERSION` in the `Makefile`. Lint with those versions before trusting a local pass.
Envtest defaults to Kubernetes 1.31 (`ENVTEST_K8S_VERSION`).

## Test expectations

- **Every bug fix ships a regression test shown to fail on the base branch** (#553, #533, #540,
  #487).
- **Prove API-server behavior in envtest, side by side with `main`.** This covers validation,
  defaulting on read, conversion, SSA/managedFields and webhooks (#538, #508, #478).
- **Use a real cluster for kubelet, scheduler or garbage-collector behavior,** and say which
  environment produced the evidence. Changes to runtime behavior also need a run on a real cluster
  before a merge verdict ([review-playbook.md](review-playbook.md), step 4).
- **Fixtures must match what real components write:**
  - LWS ≤ v0.8.0 never sets `observedGeneration` (#510).
  - A "foreign owner" fixture needs an ownerReference (#500).
  - No assertions that are always true (#553).
  - Error messages and API responses are captured from the real tool or server, not written from
    memory. In #568, a test used an error message kubectl never prints, so it passed while the code
    it covered was broken.
  - Envtest fixtures that write Pods use `testutil.UpdatePodStatusOnConflict` and
    `UpdatePodOnConflict` (`test/envtest/testutil/conflict_update.go`). The controller writes Pods
    too, so a fixture must reread the Pod on every conflict and refuse a replaced UID (#620).
- **Know your test labels and environment:**
  - `Label("serial")` is not `ginkgo.Serial` (#494).
  - Multi-node specs are skipped on single-node Kind (#532).
- **Reproduce a failure before you call it a flake or a regression.**
  - Rerun the test alone and under load (`-count=N`, `-race`, several packages in parallel).
  - A test that fails only under load usually reads state before it has converged: in #623,
    revisions appeared 8–22 ms after the test had read them. Wait for the condition the test needs
    instead of a fixed point in time.
  - A test binary the operating system killed (`Killed: 9`, exit status 137) produced no result:
    find out why and rerun it, but don't count it as a pass or a failure.
- **Check dependency changes against the latest `main`** before calling `go.mod`, `go.sum` or
  `vendor/` changes accidental: run `go mod tidy -diff` with the Go version CI pins (#620).
- **Cover new features on the RoleInstanceSet path,** not only on deprecated workloads (#487).
- **For risky or concurrency-sensitive changes:**
  - Run `go test -race`, `-shuffle=on` and repeated runs (`-count=N`).
  - Build and test the PR merged onto the latest `main`.
  - Back scale claims with `test/stress`.

## Commits and pull requests

- **Sign off commits for DCO:** `git commit -s`.
- **Fill in every section of `.github/pull_request_template.md`.** Use `fixes #N` or `NONE`, never the
  `#XXXX` placeholder, and keep the description in sync with the head commit.
- **Keep docs bilingual:** keep `README.md` and `README-zh_CN.md` in sync. Markdown lint (Markdown
  Quality Check) covers `doc/**`, the root `*.md` files and `.agents/**/*.md`.
- **`/release-test` covers release plumbing, not unreleased controller code.**
  - Only repository owners, organization members and collaborators can start it, and its results
    are informational, not required checks. Re-run it after new pushes.
  - It runs `main`'s workflow on the PR head. The helm and manifest jobs and the published-target
    upgrade legs install the chart's published images, so they test the PR's chart, manifests and
    e2e specs against the released controller. Only the `pod-cache` upgrade leg builds the PR's
    controller and CRD upgrader.
  - It matters for release manifests, chart changes and image bumps. A new e2e spec that needs the
    PR's controller fails there until a release ships it (#588). Controller changes are covered by
    E2E Test, which builds the images from source, and by a run on a real cluster.

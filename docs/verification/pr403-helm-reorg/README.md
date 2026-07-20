# pr403-helm-reorg — reorg verification

Reproducible evidence for the review of
[sgl-project/rbg#403](https://github.com/sgl-project/rbg/pull/403)
*(chore(rbgs): reorganize Helm chart templates into kubebuilder-style layout)*.

This PR is a **pure reorganization**: it moves the flat `deploy/helm/rbgs/templates/*.yaml`
into kubebuilder-style subdirs (`manager/`, `rbac/`, `webhook/`, `crd-upgrade/`), splits the
combined `secret-role.yaml` into `rbac/cert_role.yaml` + `rbac/cert_role_binding.yaml`,
renames files to snake_case, and (in a follow-up commit) renames the matching
`config/rbac/secret_role*` kustomize source to `cert_role*`. It also updates the Makefile
paths and drops a dead `uninstall` line. **No production-code bug was found.**

The harness is therefore *regression evidence* that the move is behavior-preserving. All
checks run against the **code under review** (PR head), with a render baseline at the
merge-base with upstream `main`.

| Layer | What it exercises | How to run |
|-------|-------------------|------------|
| Static | helm/kustomize render equivalence, lint, stale-path grep, RBAC cross-refs | `bash scripts/re-verify.sh` |

> **Test polarity — all CONTRACT, all must STAY GREEN.** Every check asserts the *intended*
> post-reorg state. There are **no bug-canaries** here because no bug was found. "All green"
> honestly means the reorg is still behavior-preserving. If any check flips to FAIL on a
> later push, the reorg regressed.

## Summary of results

Reviewed at PR head `2885e3b1` (baseline `d95d4b39`). `bash scripts/re-verify.sh` → exit 0.

| ID | Claim | Verdict | Evidence |
|----|-------|---------|----------|
| F1 | Rendered **helm** manifests identical (merge-base vs PR head) — no object added/dropped/altered | ✅ Pass | 12 objects both sides, diff empty · `results/render-equivalence.txt`, `results/render-ref.yaml` |
| F2 | `helm lint` passes at PR head | ✅ Pass | 1 linted, 0 failed · `results/helm-lint.txt` |
| F3 | No stale old-path references (helm flat names + old `config/rbac/secret_role*`) | ✅ Pass | no matches · `results/stale-paths.txt` |
| F4 | Every RBAC `roleRef`/subject resolves to a defined resource | ✅ Pass | all references OK · `results/rbac-xref.txt` |
| F5 | `kustomize build config/default` identical (merge-base vs PR head) | ✅ Pass | 17 objects both sides, diff empty · `results/kustomize-equivalence.txt` |

**Harness-bites** (proof the checks aren't vacuously green): injecting three regressions on a
throwaway commit (drop a template, mutate a kustomize RBAC source, add a stale path) flips
F1, F3, F4, F5 to FAIL — see `results/harness-bites.txt`.

## Per-finding detail

- **F1 — helm render equivalence.** `helm template` on both refs, strip the `# Source:` path
  comments (the only thing a move is allowed to change) and blank lines, sort docs, `diff`.
  Identical → the deployed object set (RBAC roles/bindings, SA, webhook, manager, CRD-upgrade
  job — 12 objects) is unchanged.
- **F2 — helm lint.** `helm lint deploy/helm/rbgs` passes (only the cosmetic "icon is
  recommended" info).
- **F3 — stale paths.** `git grep` for old flat helm filenames (outside the chart templates
  dir) and old `config/rbac/secret_role*` names (anywhere). None found. The only surviving
  references to the chart are directory-level (`.yamllint.yml`, CI/e2e/stress `helm upgrade
  … deploy/helm/rbgs`) and the correctly-updated Makefile `rbac/clusterrole.yaml` path.
- **F4 — RBAC cross-refs.** Parse the PR-head render; every `roleRef` and ServiceAccount
  subject (`rbgs-controller-role`, `rbgs-controller-secret-role`, `rbgs-controller-sa`)
  resolves to a resource the chart defines.
- **F5 — kustomize render equivalence.** `kustomize build config/default` (the source for
  `make manifests` → `deploy/kubectl/manifests.yaml`, touched by the `config/rbac` rename) is
  byte-for-byte identical (17 objects). Gracefully skipped if no `kustomize`/`kubectl` is on
  the machine (F1 already covers the deployed helm chart).

## Notes on the change (non-blocking observations)

- The commit message *"Rename rbac resources' name: role → clusterrole, secret_role →
  cert_role"* describes **file** renames, not resource (`metadata.name`) renames — the
  rendered `metadata.name` values are unchanged (proven by F1/F5). Cosmetic.
- The follow-up Go change in `webhook_cert_controller.go` is **comment-only** (a file
  reference `config/rbac/secret_role.yaml` → `cert_role.yaml`); no functional change.
- Minor file-naming inconsistency (`cert_role.yaml` snake_case vs `clusterrole.yaml`
  one-word vs `crd-upgrade.yaml` kebab), matching upstream kustomize outputs. Not worth
  blocking.

## Proposed fixes

None — no bug found. The observations above are optional polish, not blockers.

## Continuing after a new push (possibly on another machine)

Durable state lives on branch `verify/pr403-helm-reorg` (pushed to the reviewer's fork
`origin`). Production code is untouched, so the harness re-runs against whatever the PR head
becomes.

1. Get the branch and run one command — the PR head is auto-resolved from `verify-manifest.json`'s
   `pr` field (machine-independent `git fetch <repo-url> pull/403/head`):
   ```bash
   git fetch origin && git checkout verify/pr403-helm-reorg
   bash docs/verification/pr403-helm-reorg/scripts/re-verify.sh        # no ref = current PR head
   ```
2. Prereqs: `git`, `helm` (v3), `python3`. `kustomize` (or `kubectl kustomize`) optional — F5
   self-skips without it.
3. Read results via the polarity table: **all contract, all should be GREEN**. Any FAIL = a
   regression in the reorg.
4. Review the incremental delta the script prints (`.last-reviewed..head`), add any new
   surface to the harness, then advance the marker:
   ```bash
   echo <new-head-sha> > docs/verification/pr403-helm-reorg/.last-reviewed
   git add docs/verification/pr403-helm-reorg/.last-reviewed && git commit -m "review: advance last-reviewed" && git push origin verify/pr403-helm-reorg
   ```
5. Harness-bites is already recorded (`results/harness-bites.txt`); re-run it if you change a check.

### Kickoff prompt for a fresh agent
```text
Continue the review pipeline for https://github.com/sgl-project/rbg/pull/403. The verification
branch verify/pr403-helm-reorg (remote origin) holds the harness. Read
docs/verification/pr403-helm-reorg/README.md ("Continuing after a new push"): checkout the
branch, run scripts/re-verify.sh (no ref -> current PR head), confirm all contract checks are
GREEN, review the .last-reviewed..head delta for any new blast radius, extend the harness if
needed, advance .last-reviewed, commit + push. Do not publish unless explicitly asked.
```

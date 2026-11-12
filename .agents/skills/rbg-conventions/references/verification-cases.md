# Verification cases

These cases prove the skill's most important rules on a running API server instead of by code
reading. Each one has a script in `../scripts/`, a pass condition, and the result of its last run.
Rerun a case when you touch the code it covers, and update "Last verified".

## Safety rules for shared clusters

Clusters used for this are often shared with other people's tests.

- **Use your own namespace.** `rbg-fixtures.sh apply` creates it with a random owner token.
  - The token and the namespace UID are recorded in `~/.rbg-conventions/` (override with
    `RBG_VERIFY_STATE_DIR`).
  - Scripts that write refuse any namespace whose token and UID don't match that record. They will
    not touch another tester's verification namespace either.
  - Namespaces and the throwaway CRD are deleted with their UID as a delete precondition, so an
    object recreated under the same name is never deleted.
  - A write is retried only when it provably never reached the API server, so it is never
    replayed.
- **Never run a second controller next to one that is already running.** The RBG controller
  watches every namespace and has no namespace flag, so two of them fight over every
  RoleBasedGroup.
  - Check for other controllers first, and ask whoever runs them.
  - On a shared cluster, scope your controller with a local, uncommitted patch. In `cacheOptions()`
    in `cmd/rbgs/main.go`, set
    `DefaultFieldSelector: fields.OneTermEqualSelector("metadata.namespace", "<ns>")`.
  - Don't use `DefaultNamespaces` for this: it hands `statefulmode` a `multiNamespaceInformer`, and
    its `SharedIndexInformer` type assertion panics at startup.
- **Never delete the RBG CRDs on a shared cluster.** Other sessions can start depending on them at
  any moment; that happened during the first run of these cases.
  - If the CRDs are missing, install them only with the cluster owner's agreement, and leave them
    installed.
  - Install and remove CRDs freely only on a dedicated, disposable cluster.
- **Never run `make test-e2e-upgrade` on a shared cluster.** It requires a cluster with no RBG
  install and deletes the CRDs on teardown.

## Running the controller under test

Run each build locally against the cluster's kubeconfig. No image or registry is needed:

```sh
go run ./cmd/rbgs --enable-webhooks=none --leader-elect=false --health-probe-bind-address=:18183
```

- To swap builds, stop the process and start the other checkout the same way.
- `--enable-webhooks=none` also disables conversion, so v1alpha1 requests only work where the
  cluster's CRDs already convert through a webhook (VC-5).
- The fixtures need any image with `sh` and `sleep` that the nodes can pull. Pass it with
  `--image`.

## What the snapshot covers

`rbg-snapshot.sh` records, for every RBG-managed object in the namespace:

- identities, generations and revisions
- every label and annotation
- Pod images and restart counts, for regular and init containers. Native sidecars are init
  containers, and one can restart while the Pod UID and its regular containers stay unchanged.
- Service type, ports and selector
- the ready endpoints

Each output starts with a header line that carries the format version (currently v2).
`rbg-compare.sh` refuses empty files, snapshots of different namespaces and older formats, so two
failed snapshot runs can never compare as identical.

The fixtures cover the RoleInstanceSet paths only (standalone, leader/worker, custom components,
roleTemplate). Deprecated Deployment, StatefulSet and LeaderWorkerSet roles need their own
fixtures, or `make test-e2e-upgrade` on a disposable cluster. When webhooks are enabled, the 0.9.0
gate rejects new uses of those deprecated types.

## Testing the scripts

`scripts/selftest.sh` runs the scripts against a fake kubectl and needs only bash and jq. It
covers the retry rules of `kc`, the UID-checked deletes and the snapshot/compare pair. Run it after
changing any script.

## Cases

### VC-1: CRD defaults apply on read

- **Proves:** a new `+kubebuilder:default` changes every stored object whose parent field exists,
  without writing it; defaults under an absent optional parent are not materialized
  ([upgrade-compatibility.md](upgrade-compatibility.md), "What moves a hash").
- **Run:** `scripts/crd-default-on-read.sh`. It creates a throwaway CRD with a unique name, deletes
  only the CRD it created (by UID precondition), and touches no RBG resources, so it is safe on any
  cluster.
- **Pass:** A1–A4 print PASS, and the script exits 0. It exits non-zero if cleanup fails.
- **Last verified:** 2026-11-12 on a Kubernetes 1.37 cluster: 4/4 PASS (also twice on 2026-11-11).

### VC-2: A controller restart or binary swap changes nothing

- **Proves:** golden rule 1 for the build under test.
- **Run:**
  1. `scripts/rbg-fixtures.sh apply -n <ns> --image <image> --wait 420` with the old build running.
  2. `scripts/rbg-snapshot.sh -n <ns> --settle 20 > before.txt`.
  3. Stop the old build and start the new one (or the same build again, for a restart).
  4. `scripts/rbg-snapshot.sh -n <ns> --settle 20 > after.txt`.
  5. `scripts/rbg-compare.sh before.txt after.txt`.
  6. `scripts/rbg-fixtures.sh delete -n <ns>` when done.
- **Pass:** `PASS: nothing changed`. A diff in any revision, label, annotation, UID, image, restart
  count, Service field or endpoint is a finding.
- **Last verified:** 2026-11-11, `main` (fd160cc9) restarted cold:
  - Kubernetes 1.37 cluster with running Pods, controller scoped with the field-selector patch:
    PASS, 352 records.
  - envtest with the production webhooks: PASS, 302 records.

  These runs used snapshot format v1. Format v2 only adds init-container fields, and the fixtures
  have no init containers, so the results of VC-2 to VC-4 are unaffected.

### VC-3: The detector catches template stamping (negative control)

- **Proves:** rendering new content into every pod template rolls every role, and VC-2 detects it.
  Rerun it whenever you change the snapshot script.
- **Run:** as VC-2, but swap to a local build that adds one annotation to the pod template in
  `ConstructPodTemplateSpecApplyConfiguration` (`pkg/reconciler/pod_reconciler.go`).
- **Pass:** `rbg-compare.sh` reports FAIL with new RoleInstanceSet revisions.
- **Last verified:** 2026-11-11, on the cluster and on envtest, with the same result. Measured on
  the cluster:
  - The annotation appeared on all 11 running Pods **in place**: 0 UID changes, 0 container
    restarts, no change in ready endpoints.
  - 13 new ControllerRevisions, all 6 RoleInstanceSets moved to a new revision, all 11 Pods moved to
    a new `controller-revision-hash`.
  - The role revision hashes did not change: they cover only the RBG spec, not what the controller
    renders from it.

  A check that looks only at Pod UIDs, restart counts or role hashes misses this kind of rollout.

### VC-4: Removing a key from the role removes it downstream

- **Proves:** fields the controller renders through server-side apply have one owner, so a key the
  apply stops sending is removed ([controller-patterns.md](controller-patterns.md)).
- **Run:** `scripts/ssa-key-removal.sh -n <ns>` on fresh fixtures with a controller running. It
  removes the `rbg-conventions/remove-me` label from role `server` of `fx-standalone`, waits for
  RoleInstanceSet `fx-standalone-server` to drop it, and checks the field managers. Take snapshots
  before and after to see the side effects.
- **Pass:**
  - Both checks print PASS: the label is gone, and the main resource has the single field manager
    `rbg/Apply`.
  - `rbg-compare.sh` shows only a new RBG revision, the RBG generation, the role hash label and the
    removed label.
  - There are no RoleInstanceSet revision, RoleInstance or Pod changes, because a role hash change
    alone doesn't roll the RoleInstanceSet path.
- **Last verified:** 2026-11-11, on the cluster and on envtest: PASS.

### VC-5: A v1alpha1 round trip leaves the object unchanged

- **Proves or disproves** that conversion is lossless ([api-and-admission.md](api-and-admission.md),
  "Conversion").
- **Run:** `scripts/v1alpha1-roundtrip.sh -n <ns> <rbg>` for each fixture, with `rbg-snapshot.sh`
  before and after.
  - Exit status: 0 unchanged, 1 changed, 3 the API server rejected the write, 2 other error.
  - The script refuses to run unless the CRD converts through a webhook. With `conversion: None` a
    v1alpha1 write would prune fields instead of exercising the production conversion; that is
    why this case was not run on the cluster, whose CRDs had no conversion webhook.
  - On envtest, register the API types in the scheme before starting the environment, as
    `test/envtest/testutil/v1alpha1_contract_setup.go` does, so the real conversion webhook is used.
- **Pass:** exit status 0 for every fixture.
- **Last verified:** 2026-11-11, `main` (fd160cc9), envtest (kube-apiserver 1.31.0) with the
  production conversion and admission webhooks, run twice with the same result.

  **FAIL: no fixture passes.** Five writes were accepted and changed the stored object (exit 1).
  One was rejected (exit 3).

  | Fixture | Result | Stored v1alpha2 spec | Downstream (snapshot) |
  |---|---|---|---|
  | `fx-standalone`, `fx-template-ref` | exit 1 | role annotation added | new RBG revision and role hash; no RoleInstanceSet revision; no Pod change |
  | `fx-lwp-legacy` | exit 1 | role annotation added; deprecated `restartPolicy` string replaced by `restartPolicyConfig` with delays defaulted to 30/600 | new RoleInstanceSet revision; the RoleInstance updated in place; no recorded Pod field changed |
  | `fx-lwp-default` | exit 1 | role annotation added; `restartPolicyConfig` materialized with the defaulted delays | same as `fx-lwp-legacy` |
  | `fx-lwp-backoff` | exit 1 | role annotation added; configured `baseDelaySeconds: 10` reset to 30 (**data loss**: v1alpha1 has no field for it) | same as `fx-lwp-legacy` |
  | `fx-components` | exit 3 | unchanged: the write was **rejected** ("template or templateRef must be set for non-InstanceSet workloads") | unchanged |

  - The added annotation is `rbg.workloads.x-k8s.io/role-workload-type:
    workloads.x-k8s.io/v1alpha2/RoleInstanceSet`. It is added to every role of an accepted write,
    and it also appears on the RoleInstanceSet's metadata.
  - "No recorded Pod field changed" covers UID, labels, annotations, images and revision. envtest
    has no kubelet, so restart counts aren't observable there.

## What the runs changed in the skill

- Rollout checks look at RoleInstanceSet revisions and `controller-revision-hash` labels, not only
  at Pod UIDs (VC-3).
- The v1alpha1 conversion claims are now measured, not inferred (VC-5).
- Running a scoped controller on a shared cluster needs the field-selector patch above
  (`DefaultNamespaces` panics).
- The scripts guard ownership with per-run tokens, delete only by UID precondition, retry a write
  only when it provably never reached the server, and fail instead of printing a partial snapshot.
- Snapshots include init and sidecar containers: on the cluster, a native sidecar restart left the
  Pod UID and its regular containers unchanged, and the v1 snapshot compared it as PASS.

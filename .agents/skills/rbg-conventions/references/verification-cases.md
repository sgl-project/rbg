# Verification cases

These cases prove the skill's most important rules on a running API server instead of by code
reading. Each one has a script in `../scripts/`, a pass condition, and the result of its last run.
Rerun a case when you touch the code it covers, and update "Last verified".

## Safety rules for shared clusters

Clusters used for this are often shared with other people's tests and with other agent sessions,
which often run on the same machine.

- **Run `scripts/cluster-preflight.sh` first,** and again before every controller start or swap,
  after you stop your own controller. Name each namespace that this session created with
  `-n <ns>`. It changes nothing. It looks for RBG controller Pods, a recently renewed
  leader-election Lease, admission webhooks for `workloads.x-k8s.io`, verification namespaces and
  RoleBasedGroups of other runs, kwok fake nodes, RBG controller processes on this machine, and Helm
  releases of the chart.
  - Other runs include your other sessions on this machine. They share the local ownership
    records, so only the namespaces you name with `-n` count as yours.
  - Exit status 1 means someone else uses RBG on the cluster, even when some checks couldn't run.
    Stop, find out who, and agree with them before you start a controller, apply CRDs or install
    anything.
  - Exit status 2 means nothing was found but a check couldn't run, so the result is incomplete.
- **Use your own namespace.** `rbg-fixtures.sh apply` creates it with a random owner token.
  - The token and the namespace UID are recorded in `~/.rbg-conventions/` (override with
    `RBG_VERIFY_STATE_DIR`).
  - Scripts that write refuse any namespace whose token and UID don't match that record. They will
    not touch another tester's verification namespace either. The record belongs to your user on
    this machine, not to one session, so pass the scripts only namespaces this session created.
  - Namespaces and the throwaway CRD are deleted with their UID as a delete precondition, so an
    object recreated under the same name is never deleted.
  - `kc` retries a write only when it provably never reached the API server, so a write is never
    replayed. When the outcome of creating the run's own namespace or CRD is unknown (a lost
    response, a server timeout), the scripts read the object back and, while it is absent, send
    the create again, three attempts in all. A duplicate create is refused as AlreadyExists, so it
    changes nothing. If the outcome is still unknown after that, the script fails and names the
    namespace's owner token; the CRD is looked up again by its unique name on exit.
- **Scope every controller you run to your namespace,** with the patch in
  [Scoping the controller to one namespace](#scoping-the-controller-to-one-namespace). The RBG
  controller watches every namespace and has no namespace flag, so an unscoped controller fights
  every other controller over every RoleBasedGroup and rewrites objects it doesn't own. Controllers
  scoped to different namespaces can share a cluster; never start one next to an unscoped one.
- **Never delete the RBG CRDs on a shared cluster.** Other sessions can start depending on them at
  any moment; that happened during the first run of these cases.
  - If the CRDs are missing, install them only with the cluster owner's agreement, and leave them
    installed.
  - If the build under test adds fields to a CRD, the API server prunes them until that CRD is
    installed. Back up the installed CRDs, apply the new ones additively with server-side apply,
    never downgrade them, and tell the other users of the cluster: CRDs are cluster-wide.
  - Install and remove CRDs freely only on a dedicated, disposable cluster.
- **Never run `make test-e2e-upgrade` on a shared cluster.** It requires a cluster with no RBG
  install and deletes the CRDs on teardown.
- **Tear down while your controller still runs.** Run `rbg-fixtures.sh delete` before you stop the
  controller: it adds finalizers, such as `rbg.workloads.x-k8s.io/group-restart-budget-cleanup` on
  RoleInstances and Pods, that only a running controller removes. Then stop it, and check with the
  preflight that none of your processes is left.

## Running the controller under test

Run each build as a local process against the cluster's kubeconfig, with the scoping patch below.
No image or registry is needed:

```sh
go run ./cmd/rbgs --review-scope-namespace=<ns> --enable-webhooks=none --leader-elect=false \
  --health-probe-bind-address=:18183
```

- `--enable-webhooks=none` skips the certificate bootstrap and the caBundle patching and starts no
  webhook server, so the run changes no CRD or webhook configuration. It also disables conversion,
  so v1alpha1 requests only work where the cluster's CRDs already convert through a webhook (VC-5).
- Only the health probe binds a port (`:8082` by default). Give each local controller its own port;
  a bind error means another local controller already runs.
- Before you apply anything, check the log: the scoping patch logs `REVIEW-ONLY: the cache is
  scoped to one namespace` with your namespace, every controller logs `Starting workers` (seven on
  `main`), and nothing logs `Failed to watch`. A failed watch stalls every controller at cache sync,
  and the manager exits after `--cache-sync-timeout` (120s by default).
- If kubectl reaches the API server but the controller can't (a managed machine may block other
  binaries, for example with `connect: bad file descriptor`), run `kubectl proxy --port=<port>` and
  point the controller at a kubeconfig whose server is `http://127.0.0.1:<port>`. Started with
  `-v=6`, the proxy also logs every request the controller sends, which helps to count API calls.
- To swap builds, stop the process, check with `cluster-preflight.sh -n <ns>` that it is gone, and
  start the other checkout, patched the same way.
- Admission and conversion webhooks run only in an in-cluster install, and they apply to the whole
  cluster. Test them only on a dedicated cluster or with the agreement of everyone who uses it, and
  remove the install afterwards.
- The fixtures need an image with `sh` and `sleep` that the nodes can pull; pass it with `--image`.
  Some clusters pull only from a private registry: reuse an image the nodes already have
  (`kubectl get nodes -o json` lists them under `.status.images`).

### Scoping the controller to one namespace

Apply this local patch to `cmd/rbgs/main.go` in every checkout you run, and never commit it. Declare
the flag next to `cacheOptions()`:

```go
// REVIEW-ONLY, never commit. A build without this patch rejects the flag and exits, so it
// can't run unscoped by mistake.
var reviewScopeNamespace = flag.String("review-scope-namespace", "", "REVIEW-ONLY: cache only this namespace")
```

In `cacheOptions()`, turn `return cache.Options{...}` into `opts := cache.Options{...}`, add the
following at the end of the function, and import `k8s.io/apimachinery/pkg/fields`:

```go
    // REVIEW-ONLY, never commit: scope this controller to one namespace on a shared cluster.
    ns := *reviewScopeNamespace
    if ns == "" {
        panic("--review-scope-namespace is not set")
    }
    setupLog.Info("REVIEW-ONLY: the cache is scoped to one namespace", "namespace", ns)
    opts.DefaultFieldSelector = fields.OneTermEqualSelector("metadata.namespace", ns)
    // The API server rejects that selector for Nodes and ClusterEngineRuntimeProfiles, and it
    // matches no CRD. Cache all Nodes and CRDs, which the controller only reads, but only the
    // profiles labeled for this run: the controller rewrites the status of every profile it sees.
    opts.ByObject[&corev1.Node{}] = cache.ByObject{Field: fields.Everything()}
    opts.ByObject[&apiextv1.CustomResourceDefinition{}] = cache.ByObject{Field: fields.Everything()}
    opts.ByObject[&workloadsv1alpha2.ClusterEngineRuntimeProfile{}] = cache.ByObject{
        Field: fields.Everything(),
        Label: labels.SelectorFromSet(labels.Set{"rbg-conventions/scope": ns}),
    }
    return opts
```

- **Why each part is there:**
  - The namespace comes from a flag that only the patch defines. A checkout without the patch
    rejects `--review-scope-namespace` with `flag provided but not defined` and exits, and the
    patched build panics when the flag is missing. So a swap to a checkout you forgot to patch
    can't start an unscoped controller, as it could with an environment variable that the
    unpatched build ignores. The log line shows which namespace the cache holds.
  - `DefaultFieldSelector` also applies to cluster-scoped kinds. The API server rejects it for Nodes
    and ClusterEngineRuntimeProfiles (`field label not supported: metadata.namespace`). The profile
    informer then never syncs, so no controller starts its workers and the manager exits after the
    cache sync timeout (#619). The Node informer fails the same way once a warmup selects nodes by
    label. For CRDs the selector returns an empty list, so the LeaderWorkerSet CRD looks absent.
  - The ClusterEngineRuntimeProfile controller counts a profile's references through the cache,
    which holds only your namespace. Without the label, the scoped controller rewrites the status of
    every profile that RoleBasedGroups in other namespaces reference: `referenceCount` and
    `referencedGeneration` to 0, and the `Frozen` condition removed (VC-6).
  - Roles resolve their `engineRuntimes` through the same cache, so to the scoped controller an
    unlabeled profile doesn't exist. Create the profiles your fixtures use under unique names, label
    them `rbg-conventions/scope=<ns>`, and delete them by UID afterwards. Never label a profile that
    others use: the scoped controller would rewrite its status too.
- **Every cluster-scoped kind the controller reads through its cache needs an entry here.** The
  ClusterEngineRuntimeProfile controller (#571) broke the previous recipe that way; rerun VC-6 when
  the controller starts to read a new one.
- **The patch limits what the controller caches, not the permissions it needs.** Every namespaced
  list and watch goes to your namespace (`/namespaces/<ns>/...`), so namespaced RBAC covers those.
  It still needs cluster-scoped access:
  - `get` on CRDs at startup, through the API reader;
  - `list` and `watch` on CRDs once it reconciles a RoleBasedGroup with deprecated workload types
    enabled, because the LeaderWorkerSet CRD is then read through the cache;
  - `list`, `watch` and status updates on ClusterEngineRuntimeProfiles;
  - `list` and `watch` on Nodes once a warmup selects nodes by label.

  A tester's kubeconfig usually allows all of that; a ServiceAccount bound only to the namespace
  doesn't.
- **Don't use `DefaultNamespaces`.** It gives `statefulmode` a `multiNamespaceInformer`, which has no
  single indexer. Since #615 that fails setup with an error; before, it panicked at startup (#581).
  The remedy that error describes also covers only one namespace. Field selectors keep plain
  informers, so this patch avoids the problem.
- The patch also works with `--pod-cache-scope=filtered` (VC-2).

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

`fixtures/rbg-fixtures.yaml` covers the RoleInstanceSet paths (standalone, leader/worker, custom
components, roleTemplate). `rbg-fixtures.sh apply --with-deprecated` adds a StatefulSet role and a
Deployment role from `fixtures/rbg-deprecated-fixtures.yaml`.

- Creating them needs a controller with `--enable-deprecated-workload-types` (the default) and no
  RBG admission webhook on the cluster: with webhooks enabled, the 0.9.0 gate rejects every new use
  of a deprecated type.
- LeaderWorkerSet roles also need the LWS controller. Cover them with `make test-e2e-upgrade` on a
  disposable cluster.

## Testing the scripts

`scripts/selftest.sh` runs the scripts against a fake kubectl and needs only bash and jq. It
covers the retry rules of `kc`, the UID-checked deletes, the recovery of lost create responses,
the waits and probes that must fail closed, the snapshot/compare pair, the fixture sets, and the
preflight's findings. Run it after changing any script, and check that a new test fails on the old
script.

## Cases

### VC-1: CRD defaults apply on read

- **Proves:** a new `+kubebuilder:default` changes every stored object whose parent field exists,
  without writing it; defaults under an absent optional parent are not materialized
  ([upgrade-compatibility.md](upgrade-compatibility.md), "What moves a hash").
- **Run:** `scripts/crd-default-on-read.sh`. It creates a throwaway CRD with a unique name, deletes
  only the CRD it created (by UID precondition), and touches no RBG resources, so it is safe on any
  cluster.
- **Pass:** A1–A4 print PASS, and the script exits 0. It exits non-zero if cleanup fails.
- **Last verified:** 2026-11-13, with the polling wait for `Established`, on a Kubernetes 1.37
  cluster and on envtest (kube-apiserver 1.31.0): 4/4 PASS on both (also on 2026-11-11 and
  2026-11-12 with `kubectl wait`).

### VC-2: A controller restart or binary swap changes nothing

- **Proves:** golden rule 1 for the build under test.
- **Run:**
  1. `scripts/rbg-fixtures.sh apply -n <ns> --image <image> --with-deprecated --wait 420` with the
     old build running. Leave out `--with-deprecated` when the cluster has an RBG admission webhook.
  2. `scripts/rbg-snapshot.sh -n <ns> --settle 20 > before.txt`. Exit status 3 means the state
     didn't settle: stdout stays empty and stderr shows what kept changing. Find out why before
     comparing.
  3. Stop the old build, run the preflight with `-n <ns>`, and start the new one with the same
     scoping patch (or the same build again, for a restart).
  4. `scripts/rbg-snapshot.sh -n <ns> --settle 20 > after.txt`.
  5. `scripts/rbg-compare.sh before.txt after.txt`.
  6. `scripts/rbg-fixtures.sh delete -n <ns>` when done, while a controller still runs.
- **Pass:** `PASS: nothing changed`. A diff in any revision, label, annotation, UID, image, restart
  count, Service field or endpoint is a finding.
- **Last verified:** 2026-11-25, `main` (c037ec67) on a Kubernetes 1.37 cluster, controller scoped
  with the patch above, fixtures with `--with-deprecated` (8 RoleBasedGroups, 14 running Pods):
  - a cold restart: PASS, 422 records
  - a restart with `--pod-cache-scope=filtered`: PASS, 422 records

  The fixtures as committed, with `fx-template-ref`'s empty patch, were applied again in a fresh
  namespace through `kubectl proxy` after the controller lost direct access to the API server: all
  Ready.

  Earlier runs, without the deprecated fixtures:
  - 2026-11-13, `main` (ea5e82ae, after #538 and #556) restarted cold on envtest with the
    production webhooks: PASS, 373 records, snapshot format v2.
  - 2026-11-11, `main` (fd160cc9) restarted cold on a Kubernetes 1.37 cluster with running Pods
    (PASS, 352 records) and on envtest with the production webhooks (PASS, 302 records). These runs
    used snapshot format v1. Format v2 only adds init-container fields, and the fixtures have no
    init containers, so the results of VC-2 to VC-4 are unaffected.

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
  - On envtest, register both API versions in the scheme before starting the environment, as
    `test/envtest/testutil/v1a1gate_setup.go` does. envtest then installs every convertible CRD
    with the conversion webhook, so the real conversion is used.
- **Pass:** exit status 0 for every fixture.
- **Last verified:** 2026-11-13, `main` (ea5e82ae), envtest (kube-apiserver 1.31.0) with the
  production conversion webhook and the v1alpha2 admission webhooks (the v1alpha1 ones were removed
  in #556), and a controller running. Same result as on fd160cc9 (2026-11-11, run twice); tracked
  in #580.

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
  - An extra leaderWorkerPattern role with `activateAfter: Created`, `baseDelaySeconds: 10` and
    `maxDelaySeconds: 300` lost all three (exit 1, same downstream effect): the role falls back to
    the default `Ready` and to 30/600. It isn't in the fixtures: CRDs from before #559 don't have
    the field, so the API server would reject that object (strict field validation), and
    `rbg-fixtures.sh apply` would fail against them.

### VC-6: A scoped controller starts and stays in its namespace

- **Proves:** the patch in [Scoping the controller to one namespace](#scoping-the-controller-to-one-namespace)
  works on `main`: every controller starts, and the controller leaves the profiles of other runs
  alone. Rerun it when `cacheOptions()` changes, or when the controller starts to read a new
  cluster-scoped kind.
- **Run:**
  1. `scripts/cluster-preflight.sh`.
  2. Create two ClusterEngineRuntimeProfiles with unique names, one labeled
     `rbg-conventions/scope=<ns>` and one not. Give both the status of a profile that
     RoleBasedGroups elsewhere reference: with `kubectl patch --subresource=status`, set
     `referenceCount: 3`, `referencedGeneration: 1` and a `Frozen` condition.
  3. Start the patched controller, then apply the fixtures with `--with-deprecated` and wait until
     they are Ready.
  4. Compare both profiles' status and `resourceVersion` with step 2. Delete them by UID
     (`delete_with_uid` in `scripts/common.sh`) and the fixtures while the controller still runs.
- **Pass:**
  - The scoping log line names your namespace, seven `Starting workers` lines, and no
    `Failed to watch`; every fixture Ready.
  - The labeled profile's status is rewritten, because the controller counts references only in
    your namespace; the unlabeled one keeps its `resourceVersion`.
- **Negative controls:**
  - With `DefaultFieldSelector` alone (the previous recipe), the ClusterEngineRuntimeProfile
    informer fails with `field label not supported: metadata.namespace`, no controller starts its
    workers, and the manager exits 1 after the cache sync timeout (#619).
  - Without the label, the unlabeled profile is rewritten as well.
- **Last verified:** 2026-11-25, `main` (c037ec67), Kubernetes 1.37 cluster: PASS, and both negative
  controls behaved as described. The labeled profile went from `referenceCount: 3` and a `Frozen`
  condition to an empty status.

  Later that day, the namespace moved from an environment variable to `--review-scope-namespace`.
  `main` without the patch exited with `flag provided but not defined`, and the patched build
  panicked without the flag. With the flag, it logged the namespace and started all seven
  controllers. `kubectl proxy -v=6` showed only GETs: every namespaced list and watch had the
  namespace field selector, and profiles were listed by the scope label.

## What the runs changed in the skill

- Rollout checks look at RoleInstanceSet revisions and `controller-revision-hash` labels, not only
  at Pod UIDs (VC-3).
- The v1alpha1 conversion claims are now measured, not inferred (VC-5).
- A scoped controller needs an exemption for every cluster-scoped kind it caches, and must see
  only the ClusterEngineRuntimeProfiles of its own run. Since #571, a field selector alone stalled
  every controller at cache sync, and without the label the controller rewrote other runs' profile
  status (VC-6, #619). `DefaultNamespaces` fails setup (#615; it panicked before, #581).
- The scoping patch reads its namespace from a flag that only the patch defines. With an
  environment variable, a swap to a checkout without the patch would start an unscoped controller;
  `main` now exits with `flag provided but not defined` (VC-6).
- Reviews hand-wrote StatefulSet and Deployment fixtures to check that an upgrade leaves those
  roles alone; `--with-deprecated` now provides them (VC-2).
- `fx-template-ref` sets `templateRef.patch: {}`. v0.8.0's controller and webhook reject a
  templateRef without a patch, so an upgrade test from v0.8.0 had to drop the fixture (#601).
- Other agent sessions shared the cluster and the machine during reviews, so the preflight looks
  for them first, and counts as yours only the namespaces you name with `-n`. On the cluster it
  first reported a shell whose command line only mentioned `cmd/rbgs` as a controller. It now
  matches the `go` tool running `cmd/rbgs` and binaries named `rbgs`. A process called `main` or
  `manager`, or an IDE debug build, counts when its executable contains the controller's code,
  whatever its flags: `make build` gives `bin/manager`, and the development guide starts it with no
  RBG-specific flag (#651). With such a build and another program called `manager` running, it
  reported only the controller.
- The scripts guard ownership with per-run tokens, delete only by UID precondition, retry a write
  only when it provably never reached the server, read a create with an unknown outcome back
  before trusting or repeating it, and fail instead of printing a partial snapshot.
- Snapshots include init and sidecar containers: on the cluster, a native sidecar restart left the
  Pod UID and its regular containers unchanged, and the v1 snapshot compared it as PASS.
- `kubectl wait --for=condition=...` fails at once, with kubectl 1.31 and 1.37 alike, when
  `status.conditions` is null, which a new CRD has until the API server processes it. VC-1 polls
  for `Established` instead.
- The duplicate-env advice was wrong. With two `env` entries of the same name, server-side dry
  runs on Kubernetes 1.37 and real writes on envtest 1.31 showed that SSA rejects them in Pods and
  StatefulSets, and the RoleBasedGroup CRD in its typed pod templates, while RoleInstanceSet
  templates keep them verbatim. Dry runs on 1.37 showed that RoleBasedGroup component templates
  and `workerTemplatePatch` keep them too. So the skill no longer says to de-duplicate
  ([controller-patterns.md](controller-patterns.md)).
- A create whose response is lost may still be in progress at the API server. The scripts read
  the object back and send the create again while it is absent; with the first create made to
  report a lost connection before or after it was sent, the namespace and CRD were adopted or
  created once in every case, on the cluster. bash 3.2 runs the EXIT trap when SIGPIPE kills a
  script file, so VC-1 cleans up also when its output goes to `head -n 1` (measured on the
  cluster), but not when the same code runs as `bash -c`.

# RBGS Helm Chart

Helm chart for the [RoleBasedGroup](https://github.com/sgl-project/rbg) (RBG) controller. It deploys the
`rbgs-controller-manager` Deployment together with its RBAC, webhook configuration, and an optional
CRD upgrade Job into your Kubernetes cluster.

## What Gets Installed

- **Controller manager** (`rbgs-controller-manager` Deployment) reconciling RBG workloads.
- **RBAC**: ServiceAccounts, ClusterRole/ClusterRoleBinding, and cert Role/RoleBinding.
- **Webhooks**: validating and mutating webhook configurations and the webhook Service
  (certificates are bootstrapped by the controller itself, no cert-manager required).
- **CRD Upgrader Job** (optional, enabled by default): a `pre-install,pre-upgrade` Helm hook Job
  (with its own ServiceAccount and RBAC) that applies/upgrades the RBG CRDs before the controller starts.
- **Chart version marker**: a ConfigMap deployed with the release that records the chart version and
  application version.

## Prerequisites

- Kubernetes >= 1.28
- Helm 3

## Deprecated workload types

The `Deployment`, `StatefulSet`, and `LeaderWorkerSet` workload types are deprecated in favour of
`RoleInstanceSet`, which is the default in `v1alpha2`. Every `v1alpha1` request returns a
deprecation warning; `v1alpha1` will be removed in `0.10.0`. New uses of these workload types are
rejected in `0.9.0`.

| Key | Description | Default |
| --- | ----------- | ------- |
| `controller.deprecatedWorkloadTypes.enabled` | Compatibility mode for existing deprecated workloads. Recommended: `false` for new installations and for clusters where migration is complete — no deprecated roles remain, reconciliation has finished, and old RBG-owned workloads are gone; keep `true` only while migration and cleanup are still in progress. | `true` (see [choosing a value](#choosing-a-value)) |

### Choosing a value

Decide by whether the cluster still has deprecated roles:

- **New installation**: set `false`. This is the recommended starting point; only `RoleInstanceSet` is accepted.
- **Upgraded cluster, migration complete**: set `false`. The inventory below is empty, reconciliation has finished, and no RBG-owned legacy workloads remain.
- **Upgraded cluster with deprecated roles**: keep `true` until the migration is complete, then switch to `false`.

Take the inventory to find remaining role specs. The command must exit successfully; only then does empty output mean no role still selects a deprecated or unsupported legacy workload type:

```bash
inventory="$(kubectl get rbg,rbgs -A -o json)" &&
printf '%s' "$inventory" | jq -r '.items[] | . as $o
  | (.spec.roles // .spec.groupTemplate.spec.roles // .spec.template.roles // [])[]
  | (if .workload.kind then "\(.workload.apiVersion)/\(.workload.kind)"
     else ((.annotations // {})["rbg.workloads.x-k8s.io/role-workload-type"] // "")
     end) as $wt
  | select($wt != "" and $wt != "workloads.x-k8s.io/v1alpha2/RoleInstanceSet")
  | "\($o.kind) \($o.metadata.namespace)/\($o.metadata.name) role=\(.name) \($wt)"'
```

Lines ending in `Deployment`, `StatefulSet`, or `LeaderWorkerSet` can remain during compatibility mode while they are migrated. A `workloads.x-k8s.io/v1alpha1/InstanceSet` line is different: InstanceSet roles have not been reconciled since v0.7.0 in either mode, and they stop the whole RoleBasedGroup from reconciling. The admission gate treats InstanceSet as a deprecated workload type, so the rejected writes listed below apply to it as well, including restoring a saved InstanceSet selector into a role that has already been migrated. Migrate the role to `RoleInstanceSet` (when coming from v0.6.0 or earlier, right after the upgrade, since RoleInstanceSet first ships in v0.7.0), then delete the old InstanceSet yourself; orphan cleanup does not cover it. Any other printed value is an unknown selector: stop and review it with the object owner instead of treating it as a migration instruction. See the [migration guide](../../../doc/migration-v1alpha1-to-v1alpha2.md) for the full procedure.

While the value is `true`, the controller retains RBAC, watches, and orphan cleanup so existing roles can be reconciled and manually migrated (migrate `RoleBasedGroupSet` templates before their children). New deprecated workload uses are rejected during this transition, except children a RoleBasedGroupSet creates from its existing legacy template.

An empty inventory is necessary but not sufficient for the final cutoff. The inventory becomes empty as soon as the last role is edited, while the controller may still be reconciling and deleting the old workload. Before switching to `false`, wait for reconciliation to complete without errors and verify that the old RBG-owned Deployment, StatefulSet, and LeaderWorkerSet resources have been deleted (their Pods should be gone as well). Switching earlier disables legacy cleanup and can leave those resources running.

When the value is `false`, every deprecated workload use is rejected and the chart also removes legacy workload RBAC, so the controller does not watch those resources.

> **v1alpha1 defaults to a deprecated workload type.** The `v1alpha1` schema defaults
> `spec.roles[].workload` to `apps/v1 StatefulSet`, so a new `v1alpha1` RoleBasedGroup or
> RoleBasedGroupSet that never mentions `workload` is rejected **in both modes** — the defaulted
> StatefulSet counts as a new use. To submit `v1alpha1`, set
> `workload: {apiVersion: workloads.x-k8s.io/v1alpha2, kind: RoleInstanceSet}` on every role;
> prefer submitting the object as `v1alpha2`, where `RoleInstanceSet` is already the default. This
> is the first error a new user is likely to hit.

Rejected writes include:

- creating a RoleBasedGroup or RoleBasedGroupSet whose roles use a deprecated workload type;
- changing a role that did not already use a deprecated workload type to one;
- switching a role between deprecated workload types (for example StatefulSet to
  Deployment or LeaderWorkerSet) — the compatibility exemption is type-exact, so
  the switch provisions a new deprecated workload and counts as a new use; migrate
  the role to `RoleInstanceSet` instead;
- introducing a deprecated workload type through a new RoleBasedGroupSet template role;
- while the value is false, updating or re-applying an object that still uses a deprecated workload type.

Every `v1alpha1` request returns a CRD deprecation warning; `v1alpha1` will be removed in
`0.10.0`.

See the [manual v1alpha1-to-v1alpha2 migration guide](../../../doc/migration-v1alpha1-to-v1alpha2.md) for
role inventory, per-role rollout, InstanceSet cleanup, and storage-version migration.

## Installing

```bash
helm upgrade --install rbgs deploy/helm/rbgs \
    --create-namespace \
    --namespace rbgs-system \
    --wait
```

Or use the Makefile shortcut:

```bash
make helm-deploy
```

If you prefer to manage CRDs manually, apply `config/crd/bases/` with kubectl and disable the
CRD Upgrader Job with `--set crdUpgrade.enabled=false`. See [doc/install.md](../../../doc/install.md)
for the full installation guide.

Verify the deployment:

```bash
kubectl -n rbgs-system get deploy rbgs-controller-manager
kubectl -n rbgs-system get pods -l control-plane=rbgs-controller
```

## Values

### Global

| Key | Description | Default |
| --- | ----------- | ------- |
| `global.imagePullSecrets` | Image pull secrets shared by the controller Deployment and the crd-upgrade Job | `[]` |

### Controller

| Key | Description | Default |
| --- | ----------- | ------- |
| `controller.replicaCount` | Number of controller replicas (leader election is enabled) | `2` |
| `controller.podCacheScope` | Pod cache retention mode (`all` \| `filtered`) — see [Pod Cache Controls (Alpha)](#pod-cache-controls-alpha) | Unset (`all` in Alpha) |
| `controller.watchListClient` | Enable client-go streaming lists for all informers when supported by the API server | Unset (`false` in Alpha) |
| `controller.image.repository` | Controller image repository | `rolebasedgroup/rbgs-controller` |
| `controller.image.tag` | Controller image tag | pinned per release in `values.yaml` (falls back to `.Chart.AppVersion` if unset) |
| `controller.image.pullPolicy` | Controller image pull policy | `IfNotPresent` |
| `controller.imagePullSecrets` | Overrides `global.imagePullSecrets` for the controller when set | `[]` |
| `controller.resources` | Controller container resources | see `values.yaml` |
| `controller.nodeSelector` | Controller pod node selector | `{}` |
| `controller.tolerations` | Controller pod tolerations | `[]` |
| `controller.podAnnotations` | Controller pod annotations | `{}` |
| `controller.podSecurityContext` | Controller pod security context | `{}` |
| `controller.securityContext` | Controller container security context | `{}` |
| `controller.tuning.maxConcurrentReconciles` | Concurrent reconcile workers per controller | `10` |
| `controller.tuning.kubeApiQPS` | Max QPS from the controller to the API server | `20` |
| `controller.tuning.kubeApiBurst` | Max burst for throttle to the API server (should be > QPS) | `30` |
| `controller.pprof.enabled` | Enable the pprof profiling server | `false` |
| `controller.pprof.bindAddress` | Address the pprof endpoint binds to | `":6060"` |
| `controller.pprof.containerPort` | Container port exposed for pprof | `6060` |
| `controller.features.gangScheduling.schedulerName` | Gang scheduling scheduler: `scheduler-plugins` \| `volcano` | `scheduler-plugins` |
| `controller.features.gangScheduling.schedulerProfileName` | kube-scheduler profile running the coscheduling plugin, injected as `pod.spec.schedulerName` (`scheduler-plugins` only; empty leaves it untouched) | `""` |
| `controller.features.portAllocator.enabled` | Enable dynamic port allocation to pods | `false` |
| `controller.features.portAllocator.strategy` | Port allocation strategy (`random`) | `random` |
| `controller.features.portAllocator.startPort` | Starting port of the allocatable range | `30000` |
| `controller.features.portAllocator.portRange` | Size of the allocatable port range | `5000` |
| `controller.deprecatedWorkloadTypes.enabled` | Compatibility for existing deprecated workloads — `false` recommended for new installations and completed migrations — see [Deprecated workload types](#deprecated-workload-types) | `true` |

### CRD Upgrader

| Key | Description | Default |
| --- | ----------- | ------- |
| `crdUpgrade.enabled` | Enable the CRD Upgrader Job (`pre-install,pre-upgrade` hook) | `true` |
| `crdUpgrade.image.repository` | CRD Upgrader image repository | `rolebasedgroup/rbgs-upgrade-crd` |
| `crdUpgrade.image.tag` | CRD Upgrader image tag | pinned per release in `values.yaml` (falls back to `.Chart.AppVersion` if unset) |
| `crdUpgrade.image.pullPolicy` | CRD Upgrader image pull policy | `IfNotPresent` |
| `crdUpgrade.imagePullSecrets` | Overrides `global.imagePullSecrets` for the Job when set | `[]` |
| `crdUpgrade.ttlSecondsAfterFinished` | Job TTL after completion | `259200` (3 days) |
| `crdUpgrade.backoffLimit` | Job retry limit before marking failed | `3` |
| `crdUpgrade.tolerations` | Job pod tolerations | `[{operator: Exists}]` |
| `crdUpgrade.nodeSelector` | Job pod node selector | `{}` |

### Pod Label Migration

| Key | Description | Default |
| --- | ----------- | ------- |
| `podLabelMigration.enabled` | Run Pod-label migration as a pre-upgrade hook | `false` |
| `podLabelMigration.rateLimit` | Maximum API requests per second for Pod label migration | `10` |
| `podLabelMigration.timeout` | Overall timeout for each migration / check invocation | `2m` |
| `podLabelMigration.activeDeadlineSeconds` | Job deadline, including scheduling and both CLI invocations | `300` |
| `podLabelMigration.ttlSecondsAfterFinished` | Job TTL after completion | `259200` (3 days) |

## Uninstalling

```bash
# Uninstall the controller (CRDs are preserved)
helm uninstall rbgs --namespace rbgs-system

# Optional: delete CRDs (WARNING: this deletes all RoleBasedGroup resources)
kubectl delete -f config/crd/bases/
```

## Upgrading

`helm upgrade` is supported. Review [Deprecated workload types](#deprecated-workload-types) and take
the inventory before upgrading: if it is empty and the old RBG-owned workloads are gone, set
`controller.deprecatedWorkloadTypes.enabled=false` with the upgrade; otherwise keep `true`, migrate
the listed roles using the [migration guide](../../../doc/migration-v1alpha1-to-v1alpha2.md), and switch
to `false` only after reconciliation has completed and the legacy workloads have been deleted.

Controller rollouts drain admission traffic: each old manager Pod sleeps 5s in a preStop hook
before SIGTERM (`terminationGracePeriodSeconds: 35`). An upgrade that picks this up rolls only the
controller Pods, which take about 5s longer to terminate; RBG workloads are not affected. On large
clusters where endpoint propagation takes longer, raise both with a post-renderer or kustomize
patch; keep the grace period at least the sleep plus 30s.

### Engine runtime profiles (KEP-531) in chart v0.9.0

The CRD upgrader runs ahead of the controller: until the upgrade has completed, the cluster accepts
`engineRuntimes[].component`, `engineRuntimes[].targetContainers` and
`ClusterEngineRuntimeProfile.spec.containerPatches`, but the previous controller doesn't know them —
it renders a new reference across the pattern's default components, and its own updates to a
RoleBasedGroup can drop them. Write those fields only once the upgrade has completed. A
ClusterEngineRuntimeProfile update or delete can be rejected while the upgrade itself is in
progress — the new webhook entry is served only by the new controller — so retry it once the
upgrade has completed.

### Breaking change: values regrouped in chart v0.8.0

**Upgrade path**: from chart **v0.8.0-alpha.2 or earlier** (all previous releases, e.g. `v0.7.0`
and the `v0.8.0-alpha.x` pre-releases, used the flat values layout) to chart **v0.8.0 or later**.

Chart values were restructured from a flat layout into three top-level groups: `global`,
`controller`, and `crdUpgrade`.

> **Warning**: Helm does **not** fail on unknown values. If your values file or `--set` flags
> still use the old top-level keys, they are **silently ignored** and the controller runs with
> chart defaults. Migrate your overrides before upgrading.

| Old key (<= v0.8.0-alpha.2) | New key (>= v0.8.0) |
| --------------------------- | ------------------- |
| `replicaCount` | `controller.replicaCount` |
| `image.*` | `controller.image.*` |
| `resources.*` | `controller.resources.*` |
| `nodeSelector` / `tolerations` | `controller.nodeSelector` / `controller.tolerations` |
| `podAnnotations` | `controller.podAnnotations` |
| `podSecurityContext` / `securityContext` | `controller.podSecurityContext` / `controller.securityContext` |
| `imagePullSecrets` | `global.imagePullSecrets` (or per-component `controller.imagePullSecrets` / `crdUpgrade.imagePullSecrets`) |
| `controllerTuning.*` | `controller.tuning.*` |
| `pprof.*` | `controller.pprof.*` |
| `schedulerName` | `controller.features.gangScheduling.schedulerName` |
| `portAllocator.*` | `controller.features.portAllocator.*` |
| `crdUpgrade.repository` | `crdUpgrade.image.repository` |
| `crdUpgrade.tag` | `crdUpgrade.image.tag` |
| `crdUpgrade.imagePullPolicy` | `crdUpgrade.image.pullPolicy` |

After upgrading, verify that your overrides took effect:

```bash
helm -n rbgs-system get values rbgs
kubectl -n rbgs-system get deploy rbgs-controller-manager -o yaml | grep -A20 'args:'
```


## Pod Cache Controls (Alpha)

`controller.podCacheScope` (`--pod-cache-scope`) accepts `all` (default) or `filtered`.
With `filtered`, RBG-managed Pods (carrying `rbg.workloads.x-k8s.io/group-name`,
`rolebasedgroup.workloads.x-k8s.io/name` (the v0.6 group label),
`rbg.workloads.x-k8s.io/role-instance-name`, `workloads.x-k8s.io/warmup-name`,
`rbg.workloads.x-k8s.io/managed=true`, or any `workloads.x-k8s.io` owner reference)
are retained in full (minus `managedFields`), while unrelated Pods are reduced to
metadata stubs. Set `controller.podCacheScope=all` to disable filtering.

`controller.watchListClient` (`--watch-list-client`) defaults to `false`; enabling it
opts all controller informers into streaming lists where supported by the API server.
When a value is unset, the chart uses the controller image's default. Set a value explicitly
to pass the corresponding flag, including `all` and `false`. Explicit settings require a
controller image that supports the flags.

Set `podLabelMigration.enabled=true` to opt in to a pre-upgrade Job using
`/kubectl-rbg` from the controller image. The Job uses `rbgs-controller-sa` from the
installed release and relies on its existing permissions. If the account cannot list
legacy workload types, the CLI warns that the template report is incomplete. If either
migration command fails, the Job logs a warning and Helm continues. Helm waits for hooks
even without `--wait-for-jobs`, so a hook that cannot start or exceeds its
deadline can fail the upgrade.

The hook creates just the Job, which remains with its logs until the configured TTL. If you
increase `podLabelMigration.timeout`, allow enough time in the Job deadline and Helm's
`--timeout` for both invocations and Pod startup.

Hooks do not run with `--no-hooks`. The CLI can backfill labels independently of Helm.
When piping `helm template`
to `kubectl apply`, pass `--no-hooks` to avoid creating the rendered hooks as ordinary resources.
The migration preserves existing markers. The read-only check uses the same
candidate selector as the migration and succeeds when no legacy-labeled Pods
are missing the marker. It also reports unmarked templates of legacy RBG
workloads and warns if that report is incomplete. The Pod scan determines the
check's exit status.

To inspect the result, use `kubectl logs -n rbgs-system job/<release>-pod-label-migration-<revision>`.
You can also run the migration manually:

```bash
kubectl rbg migrate pod-labels -A --dry-run
kubectl rbg migrate pod-labels -A --rate-limit=10 --timeout=2m
kubectl rbg migrate pod-labels check -A
```

See [KEP-284](../../../keps/284-pod-cache-filter/README.md) for architecture and
read-path details.


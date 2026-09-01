# RBGS Helm Chart

Helm chart for the [RoleBasedGroup](https://github.com/sgl-project/rbg) (RBG) controller. It deploys the
`rbgs-controller-manager` Deployment together with its RBAC, webhook configuration, and an optional
CRD upgrade Job into your Kubernetes cluster.

## What Gets Installed

- **Controller manager** (`rbgs-controller-manager` Deployment) reconciling RBG workloads.
- **RBAC**: ServiceAccounts, ClusterRole/ClusterRoleBinding, and cert Role/RoleBinding.
- **Webhooks**: a validating webhook configuration and the webhook Service
  (certificates are bootstrapped by the controller itself, no cert-manager required).
- **CRD Upgrader Job** (optional, enabled by default): a `pre-install,pre-upgrade` Helm hook Job
  (with its own ServiceAccount and RBAC) that applies/upgrades the RBG CRDs before the controller starts.
- **Chart version marker**: a persistent post-install/post-upgrade ConfigMap that records the latest
  successfully installed chart version for a later upgrade compatibility check.

## Prerequisites

- Kubernetes >= 1.28
- Helm 3

## Deprecated workload types

The `Deployment`, `StatefulSet`, and `LeaderWorkerSet` workload types are deprecated in favour of
`RoleInstanceSet`, which is the default in `v1alpha2`. They remain enabled by default and can be
turned off to shrink the controller's RBAC surface.

The deprecation is not yet declared on the API itself: `v1alpha1` is still served, the CRDs carry no
deprecation warning, and no removal timeline has been published. Until that lands, this key is an
opt-in escape hatch rather than a migration deadline.

| Key | Description | Default |
| --- | ----------- | ------- |
| `controller.deprecatedWorkloadTypes.enabled` | Enable the deprecated workload types (Deployment/StatefulSet/LeaderWorkerSet). When false, RBAC for these resources is removed, the controller stops watching them, and the validating webhook rejects any create or update whose roles use them. | `true` |

> **Only set `false` on a fresh installation.** With the toggle off the controller holds no RBAC for
> Deployment/StatefulSet/LeaderWorkerSet and watches none of them, so it cannot reconcile an object
> that uses one. An installation that already has such objects must keep `true`. A migration path
> for moving existing objects to `RoleInstanceSet` will be published later; until then there is no
> supported way to turn the toggle off on an existing installation.

With `controller.deprecatedWorkloadTypes.enabled=false`, a role is only accepted if its workload
type is `RoleInstanceSet`. The whole object is validated on every write, not just the part the write
changes, so all of the following are rejected:

- creating a RoleBasedGroup/RoleBasedGroupSet whose roles use a deprecated workload type;
- updating one that already does — including an unchanged re-apply, a `kubectl scale`, and the
  controllers' own writes, so such an object cannot be reconciled at all;
- scaling a RoleBasedGroupSet whose `groupTemplate` uses one, since scale-up creates child
  RoleBasedGroups that would be rejected in turn.

> **Warning**: a v1alpha1 role must name `RoleInstanceSet` **explicitly** to be accepted.
>
> The v1alpha1 schema defaults `spec.roles[].workload` to `apps/v1 StatefulSet`, so a role submitted through v1alpha1 carries a deprecated workload type even if it never mentions `workload`. The conversion webhook records that defaulted value and the validating webhook then rejects it. In practice:
>
> - Every **new** v1alpha1 RoleBasedGroup/RoleBasedGroupSet that relies on the default workload is rejected, including manifests that look workload-free.
> - Updating an existing object through v1alpha1 without naming the workload is rejected the same way, whatever the object's roles used before.
> - To keep using v1alpha1, set `workload.apiVersion: workloads.x-k8s.io/v1alpha2` and `workload.kind: RoleInstanceSet` on every role. Prefer submitting the object as `v1alpha2`, where `RoleInstanceSet` is already the default.
>
> An automated migration path is still to be implemented.

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
| `controller.features.portAllocator.enabled` | Enable dynamic port allocation to pods | `false` |
| `controller.features.portAllocator.strategy` | Port allocation strategy (`random`) | `random` |
| `controller.features.portAllocator.startPort` | Starting port of the allocatable range | `30000` |
| `controller.features.portAllocator.portRange` | Size of the allocatable port range | `5000` |
| `controller.deprecatedWorkloadTypes.enabled` | Enable the deprecated workload types — see [Deprecated workload types](#deprecated-workload-types) | `true` |

### CRD Upgrader

| Key | Description | Default |
| --- | ----------- | ------- |
| `crdUpgrade.enabled` | Enable the CRD Upgrader Job (`pre-install,pre-upgrade` hook) | `true` |
| `crdUpgrade.image.repository` | CRD Upgrader image repository | `rolebasedgroup/rbgs-upgrade-crd` |
| `crdUpgrade.image.tag` | CRD Upgrader image tag | pinned per release in `values.yaml` (falls back to `.Chart.AppVersion` if unset) |
| `crdUpgrade.image.pullPolicy` | CRD Upgrader image pull policy | `IfNotPresent` |
| `crdUpgrade.imagePullSecrets` | Overrides `global.imagePullSecrets` for the Job when set | `[]` |
| `crdUpgrade.ttlSecondsAfterFinished` | Job TTL after completion | `259200` (3 days) |
| `crdUpgrade.tolerations` | Job pod tolerations | `[{operator: Exists}]` |
| `crdUpgrade.nodeSelector` | Job pod node selector | `{}` |

## Uninstalling

```bash
# Uninstall the controller (CRDs are preserved)
helm uninstall rbgs --namespace rbgs-system

# Optional: delete CRDs (WARNING: this deletes all RoleBasedGroup resources)
kubectl delete -f config/crd/bases/
```

## Upgrading

Every chart declares its minimum supported source chart version in the
`workloads.x-k8s.io/minimum-upgrade-source-version` Chart annotation. Before **every**
`helm upgrade`, the chart reads the prior release's `<release>-chart-version` ConfigMap and rejects a
source below that minimum; see [Minimum upgrade source version](#minimum-upgrade-source-version). Nothing checks the cluster's
existing objects against `controller.deprecatedWorkloadTypes.enabled`, so keeping it at its default
`true` on an installation that already has objects using a deprecated workload type is the
operator's responsibility — see [Deprecated workload types](#deprecated-workload-types).

### Minimum upgrade source version

Every chart declares its minimum compatible source version through this `Chart.yaml` annotation:

```yaml
annotations:
  workloads.x-k8s.io/minimum-upgrade-source-version: "0.8.0"
```

On a successful install or upgrade, a `post-install,post-upgrade` ConfigMap hook persists the target
chart version as `<release>-chart-version`:

```yaml
data:
  chartVersion: "0.8.0"
```

Before **every** upgrade, Helm renders the chart, uses `lookup` to read that ConfigMap, and compares
`data.chartVersion` with the chart's declared minimum as SemVer. A missing marker, a missing version,
or a source below the minimum calls `fail`. This happens before Helm applies any resource or invokes
the CRD upgrade hook, so a refusal changes neither the controller nor the CRDs.

A release installed before this version-marker mechanism has no ConfigMap and is intentionally
rejected. For the current chart, that includes every source older than v0.8.0, whose flat values are
not compatible with the regrouped layout described below.

The Helm identity that performs an upgrade needs `get` on ConfigMaps in the release namespace.
Offline `helm template --is-upgrade` cannot read the marker and therefore cannot validate an upgrade;
use `helm upgrade`, server-side dry-run, or a Helm-based GitOps controller with cluster API access.

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

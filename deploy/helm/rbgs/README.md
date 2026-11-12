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
| `controller.deprecatedWorkloadTypes.enabled` | Compatibility mode for existing deprecated workloads. Recommended: `false` for new installations and for clusters with no remaining deprecated roles; keep `true` only while roles still use a deprecated workload type. | `true` (see [choosing a value](#choosing-a-value)) |

### Choosing a value

Decide by whether the cluster still has deprecated roles:

- **New installation**: set `false`. This is the recommended starting point; only `RoleInstanceSet` is accepted.
- **Upgraded cluster, migration complete**: set `false`. The inventory below is empty, reconciliation has finished, and no RBG-owned legacy workloads remain.
- **Upgraded cluster with deprecated roles**: keep `true` until the migration is complete, then switch to `false`.

Take the inventory to find remaining role specs (empty output means no role still selects a deprecated workload type):

```bash
kubectl get rbg,rbgs -A -o json | jq -r '.items[] | . as $o
  | (.spec.roles // .spec.groupTemplate.spec.roles // .spec.template.roles // [])[]
  | ((.annotations // {})["rbg.workloads.x-k8s.io/role-workload-type"]
     // (if .workload.kind then "\(.workload.apiVersion)/\(.workload.kind)" else "" end)) as $wt
  | select($wt | test("/(Deployment|StatefulSet|LeaderWorkerSet)$"))
  | "\($o.kind) \($o.metadata.namespace)/\($o.metadata.name) role=\(.name) \($wt)"'
```

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

The project migration guide (manual role inventory, per-role rollout, rollback procedure, and
storage-version migration) is tracked in [sgl-project/rbg#454](https://github.com/sgl-project/rbg/issues/454).

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
| `controller.features.gangScheduling.schedulerProfileName` | kube-scheduler profile running the coscheduling plugin, injected as `pod.spec.schedulerName` (`scheduler-plugins` only; empty leaves it untouched) | `""` |
| `controller.features.portAllocator.enabled` | Enable dynamic port allocation to pods | `false` |
| `controller.features.portAllocator.strategy` | Port allocation strategy (`random`) | `random` |
| `controller.features.portAllocator.startPort` | Starting port of the allocatable range | `30000` |
| `controller.features.portAllocator.portRange` | Size of the allocatable port range | `5000` |
| `controller.deprecatedWorkloadTypes.enabled` | Compatibility for existing deprecated workloads — `false` recommended unless deprecated roles remain — see [Deprecated workload types](#deprecated-workload-types) | `true` |

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
the listed roles (the migration guide is tracked in
[sgl-project/rbg#454](https://github.com/sgl-project/rbg/issues/454)), and switch to `false` only
after reconciliation has completed and the legacy workloads have been deleted.

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



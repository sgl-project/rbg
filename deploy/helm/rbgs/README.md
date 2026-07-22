# RBGS Helm Chart

Helm chart for the [RoleBasedGroup](https://github.com/sgl-project/rbg) (RBG) controller. It deploys the
`rbgs-controller-manager` Deployment together with its RBAC, webhook configuration, and an optional
CRD upgrade Job into your Kubernetes cluster.

## What Gets Installed

- **Controller manager** (`rbgs-controller-manager` Deployment) reconciling RBG workloads.
- **RBAC**: ServiceAccounts, ClusterRole/ClusterRoleBinding, and cert Role/RoleBinding.
- **Webhooks**: validating/mutating webhook configurations and the webhook Service
  (certificates are bootstrapped by the controller itself, no cert-manager required).
- **CRD Upgrader Job** (optional, enabled by default): a `pre-install,pre-upgrade` Helm hook Job
  that applies/upgrades the RBG CRDs before the controller starts.

## Prerequisites

- Kubernetes >= 1.28
- Helm 3

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
| `controller.image.tag` | Controller image tag | `.Chart.appVersion` |
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

### CRD Upgrader

| Key | Description | Default |
| --- | ----------- | ------- |
| `crdUpgrade.enabled` | Enable the CRD Upgrader Job (`pre-install,pre-upgrade` hook) | `true` |
| `crdUpgrade.image.repository` | CRD Upgrader image repository | `rolebasedgroup/rbgs-upgrade-crd` |
| `crdUpgrade.image.tag` | CRD Upgrader image tag | `.Chart.appVersion` |
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

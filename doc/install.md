# Installation

Installing RBG to a Kubernetes Cluster  

## Prerequisites

- A Kubernetes cluster with version >= 1.28 is Required, or it will behave unexpected.
- Kubernetes cluster has at least 1 node with 1+ CPUs and 1G of memory available for the RoleBasedGroup controller manager Deployment to run on.
- The kubectl command-line tool has communication with your cluster.  Learn how to [install the Kubernetes tools](https://kubernetes.io/docs/tasks/tools/).

## Install a released version

### Install by kubectl

```bash
kubectl apply --server-side -f ./deploy/kubectl/manifests.yaml
```

To wait for RoleBasedGroup controller to be fully available, run:

```bash
kubectl wait deploy/rbgs-controller-manager -n rbgs-system --for=condition=available --timeout=5m
```

### Install by Helm

Due to the large size of CRD files (exceeding Helm's 1MB Secret limit), there are two installation methods:

#### Method 1: Manual CRD Installation (Recommended for first install)

**Step 1: Install CRDs**

```bash
kubectl apply --server-side -f deploy/helm/rbgs/crds/
```

**Step 2: Install Controller via Helm**

```bash
helm upgrade --install rbgs deploy/helm/rbgs \
    --create-namespace \
    --namespace rbgs-system \
    --skip-crds \
    --wait
```

Or use the Makefile shortcut:

```bash
make helm-deploy
```

#### Method 2: Automatic CRD Upgrade (For upgrades)

The Helm chart includes a CRD Upgrader Job that automatically installs/upgrades CRDs during `helm upgrade`. This is enabled by default.

```bash
helm upgrade --install rbgs deploy/helm/rbgs \
    --create-namespace \
    --namespace rbgs-system \
    --set crdUpgrade.enabled=true \
    --wait
```

**CRD Upgrader Configuration:**

| Parameter | Description | Default |
|-----------|-------------|------|
| `crdUpgrade.enabled` | Enable CRD Upgrader Job | `true` |
| `crdUpgrade.repository` | CRD Upgrader image repository | `rolebasedgroup/rbgs-upgrade-crd` |
| `crdUpgrade.tag` | CRD Upgrader image tag | Chart appVersion |
| `crdUpgrade.ttlSecondsAfterFinished` | Job TTL after completion | `259200` (3 days) |
| `crdUpgrade.tolerations` | Pod tolerations | `[{operator: Exists}]` |
| `crdUpgrade.nodeSelector` | Pod node selector | `{}` |

### Uninstall

To uninstall RoleBasedGroup installed via kubectl:

```bash
kubectl delete -f ./deploy/kubectl/manifests.yaml
```

To uninstall RoleBasedGroup installed via Helm:

```bash
# Uninstall controller (CRDs are preserved)
helm uninstall rbgs --namespace rbgs-system

# Optional: Delete CRDs (WARNING: this will delete all RoleBasedGroup/InstanceSet resources)
kubectl delete -f deploy/helm/rbgs/crds/
```

Or use the Makefile shortcuts:

```bash
make helm-undeploy    # Uninstall controller only
make uninstall-crds   # Delete CRDs (use with caution)
```

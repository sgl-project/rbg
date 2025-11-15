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

### Install by helm

To install a released version of rbg in your cluster by [Helm](https://helm.sh/), run the following command:

```bash
helm install rbgs deploy/helm/rbgs -n rbgs-system --create-namespace
```

### Uninstall

To uninstall a released version of RoleBasedGroup from your cluster, run the following command:

```bash
kubectl delete -f ./deploy/kubectl/manifests.yaml
```

To uninstall a released version of RoleBasedGroup from your cluster by Helm, run the following command:

```bash
helm uninstall rbgs --namespace rbgs-system 
```

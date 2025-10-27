
kubectl-rbg is a local executable command-line tool for managing RBG (RoleBasedGroup) and related resources such as ControllerRevision. Currently, it provides features such as viewing the RBG status, viewing RBG historical revisions, and rolling back the RBG. kubectl-rbg can be used both as a standalone tool or as a kubectl plugin.


# 📜 Deploying kubectl-rbg
## Prerequisites
1. Go 1.24 development environment
2. Access to a Kubernetes cluster

## Installation Steps
```shell
# Download source code
$ git clone https://github.com/sgl-project/rbg.git
# Build locally
$ make build-cli
# Install
$ chmod +x bin/kubectl-rbg
$ sudo mv bin/kubectl-rbg /usr/local/bin/
```
## Verify Installation
```shell
$ kubectl plugin list | grep rbg
# Expected output
/usr/local/bin/kubectl-rbg

$ kubectl rbg -h
# The above command works the same as "kubectl-rbg -h"
Kubectl plugin for RoleBasedGroup

Usage:
  kubectl [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  rollout     Manage the rollout of a rbg object
  status      Display rbg status information

Flags:
      --as string                      Username to impersonate for the operation. User could be a regular user or a service account in a namespace.
      --as-group stringArray           Group to impersonate for the operation, this flag can be repeated to specify multiple groups.
      --as-uid string                  UID to impersonate for the operation.
      --cache-dir string               Default cache directory (default "/Users/gxf/.kube/cache")
      --certificate-authority string   Path to a cert file for the certificate authority
      --client-certificate string      Path to a client certificate file for TLS
      --client-key string              Path to a client key file for TLS
      --cluster string                 The name of the kubeconfig cluster to use
      --context string                 The name of the kubeconfig context to use
      --disable-compression            If true, opt-out of response compression for all requests to the server
  -h, --help                           help for kubectl
      --insecure-skip-tls-verify       If true, the server's certificate will not be checked for validity. This will make your HTTPS connections insecure
      --kubeconfig string              Path to the kubeconfig file to use for CLI requests.
  -n, --namespace string               If present, the namespace scope for this CLI request
      --request-timeout string         The length of time to wait before giving up on a single server request. Non-zero values should contain a corresponding time unit (e.g. 1s, 2m, 3h). A value of zero means don't timeout requests. (default "0")
  -s, --server string                  The address and port of the Kubernetes API server
      --tls-server-name string         Server name to use for server certificate validation. If it is not provided, the hostname used to contact the server is used
      --token string                   Bearer token for authentication to the API server
      --user string                    The name of the kubeconfig user to use
  -v, --v Level                        number for the log level verbosity
      --version                        version for kubectl

Use "kubectl [command] --help" for more information about a command.
```

# 📖 Feature Overview
## Prepare Test Environment
1. For example, after applying [RBG Base](../../examples/basics/rbg-base.yaml), the RBG will automatically create ControllerRevisions and the corresponding workloads.
2. Update the RBG object specification:
```shell
$ kubectl patch rolebasedgroup nginx-cluster --type=json -p='[
  {
    "op": "replace",
    "path": "/spec/roles/1/template/spec/containers/0/resources",
    "value": {
      "requests": {
        "memory": "100Mi"
      },
      "limits": {
        "memory": "512Mi"
      }
    }
  }
]'
```

## View RBG Status
```shell
$ kubectl rbg status nginx-cluster -n default
📊 Resource Overview
  Namespace: default
  Name:      nginx-cluster

  Age:       50s

📦 Role Statuses
leader       1/1                (total: 1)      [████████████████] 100%
worker       3/3                (total: 3)      [████████████████] 100%

∑ Summary: 2 roles | 4/4 Ready
```

## View All ControllerRevisions Corresponding to an RBG
```shell
$ kubectl rbg rollout history nginx-cluster
Name                                 Revision
nginx-cluster-8676cf98bd-1           1
nginx-cluster-6f9cf75ddf-2           2
```

## View Details of a Specific ControllerRevision
```shell
$ kubectl rbg rollout history nginx-cluster --revision=1
data:
  spec:
    roles:
    - $patch: replace
    - leaderWorkerSet:
        patchLeaderTemplate: null
        patchWorkerTemplate: null
      name: leader
      replicas: 1
      template:
        metadata: {}
        spec:
          containers:
          - image: anolis-registry.cn-zhangjiakou.cr.aliyuncs.com/openanolis/nginx:1.14.1-8.6
            name: nginx-leader
            ports:
            - containerPort: 80
              protocol: TCP
            resources: {}
      workload:
        apiVersion: apps/v1
        kind: StatefulSet
    - dependencies:
      - leader
      leaderWorkerSet:
        patchLeaderTemplate: null
        patchWorkerTemplate: null
      name: worker
      replicas: 3
      template:
        metadata: {}
        spec:
          containers:
          - image: anolis-registry.cn-zhangjiakou.cr.aliyuncs.com/openanolis/nginx:1.14.1-8.6
            name: nginx-worker
            ports:
            - containerPort: 8080
              protocol: TCP
            resources: {}
      workload:
        apiVersion: apps/v1
        kind: Deployment
metadata:
  labels:
    rolebasedgroup.workloads.x-k8s.io/controller-revision-hash: 8676cf98bd
    rolebasedgroup.workloads.x-k8s.io/name: nginx-cluster
  name: nginx-cluster-8676cf98bd-1
  namespace: default
revision: 1

```

## View Differences Between Current RBG Object and Specified Revision
```shell
$ kubectl rbg rollout diff nginx-cluster --revision=1
  (
        """
        ... // 34 identical lines
                - containerPort: 8080
                  protocol: TCP
-               resources: {}
+               resources:
+                 limits:
+                   memory: 512Mi
+                 requests:
+                   memory: 100Mi
          workload:
            apiVersion: apps/v1
        ... // 2 identical lines
        """
  )
```
## Roll Back RBG to a Specific ControllerRevision
```shell
$ kubectl rbg rollout undo nginx-cluster --revision=1
rbg nginx-cluster rollback to revision 1 successfully
$ kubectl get po
NAME                                   READY   STATUS    RESTARTS   AGE
nginx-cluster-leader-0                 1/1     Running   0          4m41s
nginx-cluster-worker-97b95d9cd-8xgrm   1/1     Running   0          11s
nginx-cluster-worker-97b95d9cd-ndvtj   1/1     Running   0          9s
nginx-cluster-worker-97b95d9cd-tkt27   1/1     Running   0          9s
```

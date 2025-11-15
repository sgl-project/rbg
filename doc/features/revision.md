# Revision

ControllerRevisions are used to store the historical spec of RBG (RoleBasedGroup) objects, serving to determine whether the RBG itself or the workloads it manages have changed. You can inspect the ControllerRevisions of an RBG to retrieve its historical configurations. This feature is enabled by default without requiring additional configuration.

For example, after applying [RBG Base](../../examples/basics/rbg-base.yaml), the RBG will automatically create ControllerRevisions.

```yaml
apiVersion: apps/v1
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
kind: ControllerRevision
metadata:
  labels:
    rolebasedgroup.workloads.x-k8s.io/controller-revision-hash: 8676cf98bd
    rolebasedgroup.workloads.x-k8s.io/name: nginx-cluster
  name: nginx-cluster-8676cf98bd-1
  namespace: default
  ownerReferences:
  - kind: RoleBasedGroup
    name: nginx-cluster
revision: 1
```

The Role workload objects created by RBG will carry revision hash label.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    rolebasedgroup.workloads.x-k8s.io/group-unique-key: 4c81010f509b0ea495e76e1d66ed42ae9b0dc5ef
    rolebasedgroup.workloads.x-k8s.io/name: nginx-cluster
    rolebasedgroup.workloads.x-k8s.io/role: worker
    rolebasedgroup.workloads.x-k8s.io/role-revision-hash-worker: 6c98b798bd
  name: nginx-cluster-worker
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  labels:
    rolebasedgroup.workloads.x-k8s.io/group-unique-key: 4c81010f509b0ea495e76e1d66ed42ae9b0dc5ef
    rolebasedgroup.workloads.x-k8s.io/name: nginx-cluster
    rolebasedgroup.workloads.x-k8s.io/role: leader
    rolebasedgroup.workloads.x-k8s.io/role-revision-hash-leader: bc666cd45
  name: nginx-cluster-leader
```

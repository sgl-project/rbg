# Revision

ControllerRevisions are used to store the historical spec of RBG (RoleBasedGroup) objects, serving to determine whether the RBG itself or the workloads it manages have changed. You can inspect the ControllerRevisions of an RBG to retrieve its historical configurations. This feature is enabled by default without requiring additional configuration.

For example, after applying [RBG Base](../../examples/basic/rbg/base.yaml), the RBG will automatically create ControllerRevisions.

```yaml
apiVersion: apps/v1
kind: ControllerRevision
metadata:
  labels:
    rbg.workloads.x-k8s.io/group-revision: 8676cf98bd
    rbg.workloads.x-k8s.io/group-name: nginx-cluster
  name: nginx-cluster-8676cf98bd-1
  namespace: default
  ownerReferences:
    - kind: RoleBasedGroup
      name: nginx-cluster
revision: 1
data:
  spec:
    roles:
      - name: leader
        replicas: 1
        standalonePattern:
          template:
            spec:
              containers:
                - name: nginx-leader
                  image: nginx:latest
                  ports:
                    - containerPort: 80
      - name: worker
        replicas: 3
        dependencies: ["leader"]
        standalonePattern:
          template:
            spec:
              containers:
                - name: nginx-worker
                  image: nginx:latest
                  ports:
                    - containerPort: 8080
```

The Role workload objects created by RBG will carry revision hash label.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    rbg.workloads.x-k8s.io/group-uid: 4c81010f509b0ea495e76e1d66ed42ae9b0dc5ef
    rbg.workloads.x-k8s.io/group-name: nginx-cluster
    rbg.workloads.x-k8s.io/role-name: worker
    rbg.workloads.x-k8s.io/role-revision-worker: 6c98b798bd
  name: nginx-cluster-worker
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  labels:
    rbg.workloads.x-k8s.io/group-uid: 4c81010f509b0ea495e76e1d66ed42ae9b0dc5ef
    rbg.workloads.x-k8s.io/group-name: nginx-cluster
    rbg.workloads.x-k8s.io/role-name: leader
    rbg.workloads.x-k8s.io/role-revision-leader: bc666cd45
  name: nginx-cluster-leader
```

## Labels Reference

| Label Key | Description |
|-----------|-------------|
| `rbg.workloads.x-k8s.io/group-revision` | Revision hash of the RBG object |
| `rbg.workloads.x-k8s.io/group-name` | Name of the RoleBasedGroup |
| `rbg.workloads.x-k8s.io/role-name` | Name of the role |
| `rbg.workloads.x-k8s.io/role-revision-<role>` | Revision hash of the specific role |

See [Labels, Annotations and Environment Variables](../reference/variables.md) for complete reference.
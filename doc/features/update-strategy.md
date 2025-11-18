# Update Strategy

Rolling update is important to online services with zero downtime. For LLM inference services, this is particularly
important. Three different configurations are supported in RBG, **maxUnavailable**, **maxSurge** and **partition**.

| Configuration  | Description                                                                                                                                                                                                                                                                                       | Supported Workloads                     |
|----------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|-----------------------------------------|
| maxUnavailable | The maximum number (or percentage) of pods that may be unavailable during the update.                                                                                                                                                                                                             | Deployment, StatefulSet, LWS            |
| maxSurge       | The maximum number (or percentage) of extra pods that may be created above the desired replica count during the update.                                                                                                                                                                           | Deployment, StatefulSet, LWS            |
| partition      | An ordinal partition number that controls which StatefulSet Pods are updated.  Pods with ordinal ≥ partition are updated to the new revision; pods with ordinal < partition are left at the previous revision. This lets you roll out updates in order and stop/resume at a particular partition. | StatefulSet, LWS (LWS Version >= 0.7.0) |

## Example: RollingUpdate with MaxSurge and MaxUnavailable

```yaml
rolloutStrategy:
  type: RollingUpdate
  rollingUpdateConfiguration:
    maxUnavailable: 2
    maxSurge: 2
  replicas: 4
```

1. Create a RBG with three roles

    ```bash
    kubectl apply -f examples/basics/rolling-update.yaml
    ```

2. Update the label for roles and verify the rollout  
   2.1 Update the label for the role with `StatefulSet` workload

```bash
kubectl patch rolebasedgroup rolling-update --type='json' -p='[
  {
    "op": "replace",
    "path": "/spec/roles/0/template/metadata/labels/appVersion",
    "value": "v2"
  }
]'
```

2.2 Update the label for the role with `Deployment` workload

```bash
kubectl patch rolebasedgroup rolling-update --type='json' -p='[
  {
    "op": "replace",
    "path": "/spec/roles/1/template/metadata/labels/appVersion",
    "value": "v2"
  }
]'
```

2.3 Update the label for the role with `LeaderWorkerSet` workload

```bash
kubectl patch rolebasedgroup rolling-update --type='json' -p='[
  {
    "op": "replace",
    "path": "/spec/roles/2/template/metadata/labels/appVersion",
    "value": "v2"
  }
]'
```

## Example: RollingUpdate with Partition

**LWS version >= 0.7.0 supports RollingUpdate with Partition.**

```yaml
rolloutStrategy:
  type: RollingUpdate
  rollingUpdateConfiguration:
    maxUnavailable: 2
    maxSurge: 2
    partition: 1
  replicas: 4
```

1. Create a RBG with two roles

    ```bash
    kubectl apply -f examples/basics/rolling-update-with-partition.yaml
    ```

2. Update the label for roles and verify the rollout  
   2.1 Update the label for the role with `StatefulSet` workload

```bash
kubectl patch rolebasedgroup rolling-update-with-partition --type='json' -p='[
  {
    "op": "replace",
    "path": "/spec/roles/0/template/metadata/labels/appVersion",
    "value": "v2"
  }
]'
```

By checking the sts status, you can see that pods from partition to replicas-1 (1-3) have been updated to the new
version,
while pods with ordinal < partition have not been updated.

```bash
kubectl get sts rolling-update-with-partition-sts -ojsonpath='{.status}'
|jq
{
  "availableReplicas": 4,
  "collisionCount": 0,
  "currentReplicas": 1,
  "currentRevision": "rolling-update-with-partition-sts-74894d9bf",
  "observedGeneration": 6,
  "readyReplicas": 4,
  "replicas": 4,
  "updateRevision": "rolling-update-with-partition-sts-cb5f86b9b",
  "updatedReplicas": 3
}
```

2.2 Update the label for the role with `LeaderWorkerSet` workload

```bash
kubectl patch rolebasedgroup rolling-update-with-partition --type='json' -p='[
  {
    "op": "replace",
    "path": "/spec/roles/1/template/metadata/labels/appVersion",
    "value": "v2"
  }
]'
```

By checking the lws status, you can see that pods from partition to replicas-1 (1-3) have been updated to the new
version,
while pods with ordinal < partition have not been updated.

```bash
kubectl get lws rolling-update-with-partition-lws -ojsonpath='{.status}' | jq

{
  "conditions": [xxxx],
  "hpaPodSelector": "leaderworkerset.sigs.k8s.io/name=rolling-update-with-partition-lws,leaderworkerset.sigs.k8s.io/worker-index=0",
  "readyReplicas": 4,
  "replicas": 4,
  "updatedReplicas": 3
}

```

## Example YAMLs

- [rolling-update.yaml](../../examples/basics/rolling-update.yaml)
- [rolling-update-with-partition.yaml](../../examples/basics/rolling-update-with-partition.yaml)

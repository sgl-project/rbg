# Gang Scheduling

Gang Scheduling is a critical feature for Deep Learning workloads to enable all-or-nothing scheduling capability. Gang Scheduling avoids resource inefficiency and scheduling deadlock.

RBG supports two gang scheduling strategies: **Scheduler Plugins** and **Volcano**, providing flexibility for different cluster environments.

## Scheduler Plugins

```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: scheduler-plugins-gang
spec:
   podGroupPolicy:
       kubeScheduling: 
           scheduleTimeoutSeconds: 120
```

Based on this configuration, RBG will automatically create a `PodGroup.scheduling.x-k8s.io` CR; the PodGroup's minNumber equals the sum of all pods across all Roles in the RBG.

```yaml
apiVersion: scheduling.x-k8s.io/v1alpha1
kind: PodGroup
metadata:
  name: scheduler-plugins-gang
  namespace: default
  ownerReferences:
  - apiVersion: workloads.x-k8s.io/v1alpha1
    controller: true
    kind: RoleBasedGroup
    name: scheduler-plugins-gang
spec:
  minMember: 2
  scheduleTimeoutSeconds: 120
status:
  phase: Running
  running: 2
```

## Volcano

```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: volcano-gang
spec:
  podGroupPolicy:
    # using volcano for gang-scheduling
    volcanoScheduling:
      queue: default  
  roles:
    - name: role-sts
      template:
        spec:
          # Important ensure that Pods are scheduled by the Volcano scheduler.
          schedulerName: volcano
```

Based on this configuration, RBG will automatically create a `PodGroup.scheduling.volcano.sh` CR; the PodGroup's minNumber equals the sum of all pods across all Roles in the RBG.

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: volcano-gang
  namespace: default
spec:
  minMember: 2
  queue: default
status:
  phase: Running
  running: 2
```

## Examples

- [Scheduler Plugins Gang Scheduling](../../examples/basics/scheduler-plugins-gang.yaml)
- [Volcano Gang Scheduling](../../examples/basics/volcano-gang.yaml)

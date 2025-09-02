# Gang Scheduling
Gang Scheduling is a critical feature for Deep Learning workloads to enable all-or-nothing scheduling capability. Gang Scheduling avoids resource inefficiency and scheduling deadlock.

```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: nginx
spec:
   podGroupPolicy:
       kubeScheduling: 
           scheduleTimeoutSeconds: 120
```
Based on this configuration, RBG will automatically create a PodGroup CR; the PodGroup's minNumber equals the sum of all pods across all Roles in the RBG.

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: example-podgroup
  namespace: default
spec:
  minMember: 4 # the sum of all pods across all Roles in the RBG
  scheduleTimeoutSeconds: 30
```

Other gang scheduling policies will be supported soon.

## Examples
- [Gang Scheduling](../../examples/basics/gang-scheduling.yaml)
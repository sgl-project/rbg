# Gang Scheduling

Gang Scheduling is a critical feature for Deep Learning workloads that enables all-or-nothing scheduling capability. This prevents resource inefficiency and scheduling deadlock by ensuring all pods in a group are scheduled atomically.

RoleBasedGroup supports two gang scheduling implementations: **Scheduler Plugins** and **Volcano**, providing flexibility for different cluster environments.

## Overview

Gang scheduling ensures that all pods in a workload are scheduled together:
- If one pod cannot be scheduled, all pods wait
- Eliminates partial scheduling that leads to resource waste
- Essential for distributed training and multi-role inference

## Scheduler Plugins Gang Scheduling

Scheduler Plugins is the Kubernetes-native gang scheduling solution. In v1alpha2, enable it via annotations:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: scheduler-plugins-gang
  annotations:
    rbg.workloads.x-k8s.io/group-gang-scheduling: "true"
    # Optional: timeout in seconds (default: 60)
    rbg.workloads.x-k8s.io/group-gang-scheduling-timeout: "120"
spec:
  roles:
    - name: prefill
      replicas: 2
      standalonePattern:
        template:
          spec:
            containers:
              - name: prefill
                image: inference:latest

    - name: decode
      replicas: 4
      standalonePattern:
        template:
          spec:
            containers:
              - name: decode
                image: inference:latest
```

### How It Works

1. RBG controller creates a `PodGroup.scheduling.x-k8s.io` CR
2. PodGroup's `minMember` = sum of all pods across all roles
3. Scheduler waits until all pods can be placed simultaneously
4. If timeout expires, scheduling fails

### Scheduler Plugins PodGroup

```yaml
apiVersion: scheduling.x-k8s.io/v1alpha1
kind: PodGroup
metadata:
  name: scheduler-plugins-gang
  namespace: default
  ownerReferences:
    - apiVersion: workloads.x-k8s.io/v1alpha2
      controller: true
      kind: RoleBasedGroup
      name: scheduler-plugins-gang
spec:
  minMember: 6  # 2 prefill + 4 decode
  scheduleTimeoutSeconds: 120
```

## Volcano Gang Scheduling

Volcano is a batch scheduling system with advanced gang scheduling features. To use Volcano, configure the controller with `--scheduler-name=volcano` or set `schedulerName: volcano` in Helm values.

### Enable via Annotations

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: volcano-gang
  annotations:
    rbg.workloads.x-k8s.io/group-gang-scheduling: "true"
    # Optional: specify queue
    rbg.workloads.x-k8s.io/group-gang-scheduling-volcano-queue: "default"
    # Optional: specify priority class
    rbg.workloads.x-k8s.io/group-gang-scheduling-volcano-priority: "high-priority"
spec:
  roles:
    - name: prefill
      replicas: 2
      standalonePattern:
        template:
          spec:
            schedulerName: volcano  # Required: use Volcano scheduler
            containers:
              - name: prefill
                image: inference:latest

    - name: decode
      replicas: 4
      standalonePattern:
        template:
          spec:
            schedulerName: volcano  # Required: use Volcano scheduler
            containers:
              - name: decode
                image: inference:latest
```

### Important Configuration

1. **Controller Setting**: RBG controller must be configured with `--scheduler-name=volcano`
2. **Pod schedulerName**: Each pod must have `schedulerName: volcano`
3. **Annotations**: Enable gang scheduling via annotations

### Volcano PodGroup

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: volcano-gang
  namespace: default
spec:
  minMember: 6  # 2 prefill + 4 decode
  queue: default
  priorityClassName: high-priority
```

### Volcano Features

- **Queue Management**: Organize workloads into queues
- **Priority Classes**: Prioritize critical workloads
- **Resource Reservation**: Reserve resources for pending groups

## Annotation Configuration

| Annotation | Description | Required |
|------------|-------------|----------|
| `rbg.workloads.x-k8s.io/group-gang-scheduling` | Enable gang scheduling | Yes |
| `rbg.workloads.x-k8s.io/group-gang-scheduling-timeout` | Timeout in seconds (scheduler-plugins) | No (default: 60) |
| `rbg.workloads.x-k8s.io/group-gang-scheduling-volcano-queue` | Volcano queue name | No |
| `rbg.workloads.x-k8s.io/group-gang-scheduling-volcano-priority` | Volcano priority class | No |

## Comparison

| Feature | Scheduler Plugins | Volcano |
|---------|-------------------|---------|
| Setup | Default Kubernetes scheduler-plugins | Requires Volcano installation |
| Queue Support | No | Yes |
| Priority Support | Via PodPriority | Via Volcano priority classes |
| Resource Reservation | No | Yes |
| Controller Config | None required | `--scheduler-name=volcano` |

## Use Cases

- **Distributed Training**: All workers must be scheduled together
- **Multi-Role Inference**: Prefill and decode pods need coordinated scheduling
- **GPU Workloads**: Prevent partial GPU allocation
- **Batch Jobs**: All-or-nothing for job execution

## Examples

- [Scheduler Plugins Gang Scheduling](../../examples/basic/rbg/scheduling/scheduler-plugins-gang.yaml)
- [Volcano Gang Scheduling](../../examples/basic/rbg/scheduling/volcano-gang.yaml)
- [Exclusive Topology Scheduling](../../examples/basic/rbg/scheduling/exclusive-topology.yaml)
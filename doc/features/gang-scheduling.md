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

Pods must be scheduled by the scheduler that runs the coscheduling plugin. Its profile name
is chosen when scheduler-plugins is deployed (the upstream chart uses
`scheduler-plugins-scheduler`), so pass it to the controller with
`--scheduler-profile-name` (Helm: `controller.features.gangScheduling.schedulerProfileName`)
and it is injected as `pod.spec.schedulerName`. Leave it empty only when scheduler-plugins
replaces the cluster's default scheduler or the role template sets `schedulerName` itself.

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

Volcano is a batch scheduling system with advanced gang scheduling features. To use Volcano, configure the controller with `--scheduler-name=volcano` or set `controller.features.gangScheduling.schedulerName: volcano` in Helm values.

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

### Important Configuration

1. **Controller Setting**: RBG controller must be configured with `--scheduler-name=volcano`
2. **Pod schedulerName**: injected automatically as `volcano` for every role of a
   gang-enabled RoleBasedGroup, including roles excluded from the gang constraint, so the
   whole group is placed by one scheduler
3. **Enablement**: via the `group-gang-scheduling` annotation or a CoordinatedPolicy

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

## CoordinatedPolicy Gang Scheduling

The annotation enables all-or-nothing scheduling for the whole group. To express a
*partial* gang — "start as soon as N replicas of this role can be placed" — use a
`CoordinatedPolicy` whose name and namespace match the RoleBasedGroup:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: volcano-gang   # must match the RoleBasedGroup name
  namespace: default
spec:
  policies:
    - name: gang-scheduling
      roles: [prefill, decode]
      strategy:
        scheduling:
          gang:
            minReplicas:
              prefill: 1
              decode: 2
```

- The rule's `roles` field scopes the gang: only those roles are enrolled in it. Every other
  role of the RoleBasedGroup still gets the gang scheduler's `schedulerName`, so one scheduler
  places the whole group, but it carries no PodGroup membership and is not counted in
  `minMember`. Its pods are therefore scheduled independently.
- Omitting `minReplicas` requests all-or-nothing gang over the roles the rule lists, so
  `minMember` is the sum of their pods. The `group-gang-scheduling` annotation is the special
  case of that where every role is covered.
- Each `minReplicas` key must be listed in that rule's `roles` and must be at least 1. The
  validating webhook rejects violations of these two rules.
- Each `minReplicas` must also name an existing role and must not exceed that role's
  `replicas`. Both depend on the RoleBasedGroup, so they are enforced when the PodGroup is
  built rather than at admission time (see [Interaction with scaling](#interaction-with-scaling)).
- When several rules declare a gang strategy, their `roles` are unioned and the per-role
  minimums are merged with the largest value winning for a role appearing in more than one
  rule. A rule with an empty `minReplicas` makes that union all-or-nothing.
- The CoordinatedPolicy takes precedence over the `group-gang-scheduling` annotation.

**`minReplicas` requires `--scheduler-name=volcano` with Volcano >= 1.14.** It maps to the
PodGroup `spec.subGroupPolicy` field, where one subGroup is one RoleInstance. Only the
scheduler choice is checked at admission time: with any other `--scheduler-name`, the
webhook rejects `minReplicas` outright. The remaining requirements are enforced when the
PodGroup is built, and surface as a `GangConfigured=False` condition on the RoleBasedGroup
plus one `IncompatibleGangConfig` warning event:

- The installed Volcano PodGroup CRD must actually carry `subGroupPolicy` (Volcano >= 1.14).
- Roles whose `spec.roles[].annotations` select a workload type other than
  `RoleInstanceSet` cannot be used with `minReplicas`, because they do not label pods with
  the RoleInstance name that subGroup membership is derived from.

### Interaction with scaling

An admitted `minReplicas` is a hard floor on the role's `replicas`, but scaling below it is
not blocked at admission: a policy states intent, and the workload is expected to follow it,
so a temporarily unsatisfiable minimum is a state to wait out rather than a write to reject.
Instead, the PodGroup is left untouched, role creates and updates are paused, and the
RoleBasedGroup reports:

```console
kubectl get rbg <name> -o jsonpath='{.status.conditions}'
```

a `GangConfigured=False` condition with `reason=IncompatibleGangConfig`, together with one
warning event of the same reason. The event fires on the transition only, so unrelated
workload churn does not repeat it. While the condition is false the RoleBasedGroup is
re-examined every 5 minutes; editing either the CoordinatedPolicy or the RoleBasedGroup
resolves it within seconds, because both are watched.

Pausing role reconcile is deliberate. Without a PodGroup, any pod created in the meantime
would carry the gang scheduler's `schedulerName` but no gang membership, so it would be
placed on its own and hold its accelerators while the rest of the group waits — the outcome
gang scheduling exists to prevent. Already-running pods keep running, and roles removed from
the spec are still cleaned up, so a broken policy does not pin resources the user has
released.

This applies to writes coming from a `RoleBasedGroupScalingAdapter`/HPA as well, so keep the
minimum at or below the autoscaler's `minReplicas`. To scale further down, lower or remove
the minimum in the CoordinatedPolicy first.

Coordinated scaling (`strategy.scaling.maxSkew`) paces a role up in batches and holds the
next batch until the current replicas are Scheduled or Ready. That would deadlock a
gang-covered role paced below its gang minimum: the PodGroup withholds scheduling until the
whole gang can be placed at once, so the replicas never become Ready, and scaling never
creates the ones the gang is waiting for. The controller therefore raises any coordinated
scaling target that sits below its gang minimum up to that minimum, letting the role exceed
`maxSkew` for one step. The gang minimum wins because it is the harder constraint: a larger
skew only loosens how gradually a role ramps, while starving the gang blocks the whole group.

Enabling gang scheduling — via either the annotation or a CoordinatedPolicy — also turns on
RoleInstance-level gang scheduling for every covered role, unless the role sets
`rbg.workloads.x-k8s.io/role-instance-gang-scheduling` itself. One RoleInstance is one
scheduling unit, so its pods are placed and recreated atomically rather than one at a time.

## Annotation Configuration

| Annotation | Description | Required |
|------------|-------------|----------|
| `rbg.workloads.x-k8s.io/group-gang-scheduling` | Enable gang scheduling | No (unless enabling via CoordinatedPolicy instead) |
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
| Per-Role `minReplicas` | No | Yes (Volcano >= 1.14) |
| Controller Config | `--scheduler-profile-name` (unless it is the default scheduler) | `--scheduler-name=volcano` |

Running scheduler-plugins as a second scheduler alongside the default one is convenient for
trying gang scheduling out, but the two schedulers make placement decisions from independent
views of the cluster and can conflict when resources are tight. Upstream does not recommend it
for production; deploy scheduler-plugins as the cluster's default scheduler instead, and leave
`--scheduler-profile-name` empty.

## Use Cases

- **Distributed Training**: All workers must be scheduled together
- **Multi-Role Inference**: Prefill and decode pods need coordinated scheduling
- **GPU Workloads**: Prevent partial GPU allocation
- **Batch Jobs**: All-or-nothing for job execution

## Examples

- [Scheduler Plugins Gang Scheduling](../../examples/basic/rbg/scheduling/scheduler-plugins-gang.yaml)
- [Volcano Gang Scheduling](../../examples/basic/rbg/scheduling/volcano-gang.yaml)
- [Exclusive Topology Scheduling](../../examples/basic/rbg/scheduling/exclusive-topology.yaml)
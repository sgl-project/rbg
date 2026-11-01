# Stateful selective scale-down

English | [简体中文](selective-scale-down.zh-CN.md)

Use the role annotation `rbg.workloads.x-k8s.io/excluded-ordinals` to prefer
specific instances for scale-down and avoid those ordinals during later scale-up.
This Alpha capability supports native Stateful RoleInstanceSet roles on RBGs
that are not managed by an RBGSet. Stateless roles, other workload backends,
RBGSet templates, and RBGSet-managed RBGs do not support the annotation.

No policy or controller feature flag is required. Without the annotation and
without previously committed sparse membership, scaling keeps its existing order.

## Configure and scale

Start with the [example](../../examples/basic/rbg/scaling/selective-scale-down.yaml),
which creates three members `[0,1,2]`:

```sh
kubectl apply -f examples/basic/rbg/scaling/selective-scale-down.yaml
```

Set exclusions and reduce replicas in the same update:

```sh
kubectl patch rbg selective-scaling --type=json -p='[
  {"op":"add","path":"/spec/roles/0/annotations","value":{"rbg.workloads.x-k8s.io/excluded-ordinals":"1"}},
  {"op":"replace","path":"/spec/roles/0/replicas","value":2}
]'
```

The committed members become `[0,2]`. Setting exclusions before a separate
replica edit is also supported. Editing exclusions alone never replaces an
instance. Preserve other role annotations when adapting this example.

Read the child RIS once to observe both parts of the committed target:

```sh
kubectl get ris selective-scaling-worker -o json |
  jq '{replicas: .spec.replicas, skipped: .metadata.annotations["rbg.workloads.x-k8s.io/committed-skip-ordinals"]}'
```

The scale-down is committed when this object shows `replicas: 2` and `skipped: "1"`.
It does not mean the old instance has finished retiring. After commitment, scale
back to three, retaining the exclusion:

```sh
kubectl patch rbg selective-scaling --type=json -p='[
  {"op":"replace","path":"/spec/roles/0/replicas","value":3}
]'
```

The new target is `[0,2,3]`. Instances 0 and 2 retain their identities.
Rapid scale-down and scale-up edits before commitment can be coalesced by the
controller and do not guarantee removal of instance 1.

Replacement through scaling temporarily reduces desired capacity by the number
removed; serving capacity may remain reduced until replacements are ready.
Wait for old instances to retire as well if replacement Pods need their GPU,
memory, or other resources. Traffic draining, data migration and PVC cleanup
remain separate operations. For leader/worker roles, one ordinal selects the
entire leader/worker group, preserving the configured group size.

## Selection rules

Values accept individual nonnegative 32-bit ordinals and inclusive ranges, such
as `"1,3,8-10"`. Duplicates and overlaps are normalized; input order is not priority.

- Scale-down removes only the required number: excluded members first, highest
  ordinal first within each priority, then the remaining highest ordinals.
- Scale-up retains committed members and adds the lowest unused, nonexcluded
  ordinals. Initial creation uses this rule too.
- At fixed replicas, editing or clearing the annotation leaves membership alone.
- Missing or unhealthy instances are recovered at their committed ordinals.
- Admission rejects invalid values and unsupported locations with a field and
  cause. If invalid input bypasses admission, reconciliation reports a Warning
  event and a failed Ready condition and blocks scaling without a default-order
  fallback.

For example, changing replicas from five to three and exclusions to `"1,3"`
together preserves `[0,2,4]`. Exclusions can contain more or fewer members than
the scale-down amount, or ordinals that have no current instance.

## Rollout and autoscaling

The existing rollout strategies and autoscaler replica ownership remain in use.
Partition counts positions in the sorted committed member list: for `[0,2,4]`
and `partition: 2`, 0 and 2 keep the old revision and 4 is eligible for upgrade.
Changing only exclusions does not move this scope, change template revisions,
or trigger a rollout. Template rollback preserves the current exclusion input
and committed membership.

## Disablement and downgrade

Remove the role annotation to disable future exclusion-based selection. This
keeps already committed members. For `[0,2,3]`, clearing exclusions keeps
`[0,2,3]`; a later scale-up to four can fill ordinal 1.

The controller stores the actual gaps under a distinct child RIS annotation,
`rbg.workloads.x-k8s.io/committed-skip-ordinals`, together with `spec.replicas`.
This record is not a copy of the role's exclusion input.

Child RIS objects are controller-managed and must not be edited directly.
Editing or deleting the internal annotation changes desired membership
immediately and is not automatically recovered. No identity-based admission
protection is added for it.

An old controller must not run against sparse RIS objects. Before downgrade,
remove the role input, scale out over the gaps until every child RIS has an
empty internal record, and wait for reconciliation to finish. All active
controller replicas must understand the feature before exclusions are used.

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
  event and a failed Ready condition. Updates to the offending role stop without
  a default-order fallback; other roles and cleanup of removed workloads continue.
  Discovery retains that role's committed target, or publishes no members if the
  invalid role has never created a child.

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

New rollout surge slots also skip the current exclusions, including ordinals
above the last committed member. For members `[0,2,4]`, E=`"1,3,5"`, and one
surge slot, the new surge uses 6. The conditional writer mirrors E onto child
RIS metadata for this allocation; this input copy is separate from committed
membership and stays out of revisions and RI/Pod templates. Editing or removing
E preserves already-created surge identities until normal rollout completion,
subject to the configured surge count budget.

Discovery intentionally publishes the committed target immediately: selected
members leave the discovery ConfigMap before their instances finish retiring.
This is an early-drain signal, not a guarantee that consumers have stopped
traffic or that the old instances have exited.

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

## Recover a corrupt committed record

An unparseable `committed-skip-ordinals` record stops membership reconciliation
and reports `InvalidCommittedOrdinals`. Removing E from the role does not repair
the record. There is no automatic dense fallback: dropping the record can
change desired identities and retire higher members.

This is an exceptional operator repair. Stop concurrent replica and template
edits for the role, save the RBG/RIS/RoleInstance state, and establish the intended
membership from a trusted previous committed target. Do not infer it solely
from live instances: some may be missing, terminating, or surge instances.
Check associated Pod/PVC ownership before changing membership.

The following example assumes role 0 is `worker`, the verified target has three
members `[0,2,4]`, and the corrupted annotation still exists. Use the appropriate
namespace on every command. First remove E from the role if it is present:

```sh
kubectl get rbg selective-scaling -o yaml > rbg-before-repair.yaml
kubectl patch rbg selective-scaling --type=json -p='[
  {"op":"test","path":"/spec/roles/0/name","value":"worker"},
  {"op":"remove","path":"/spec/roles/0/annotations/rbg.workloads.x-k8s.io~1excluded-ordinals"}
]'
kubectl get ris selective-scaling-worker -o json > ris-before-repair.json
kubectl get ri -l rbg.workloads.x-k8s.io/group-name=selective-scaling -o yaml > instances-before-repair.yaml
```

Restore the verified record using UID, resourceVersion, and replica preconditions:

```sh
jq --arg record '1,3' --argjson replicas 3 '[
  {op:"test",path:"/metadata/uid",value:.metadata.uid},
  {op:"test",path:"/metadata/resourceVersion",value:.metadata.resourceVersion},
  {op:"test",path:"/spec/replicas",value:$replicas},
  (if $record == "" then
    {op:"remove",path:"/metadata/annotations/rbg.workloads.x-k8s.io~1committed-skip-ordinals"}
   else
    {op:"replace",path:"/metadata/annotations/rbg.workloads.x-k8s.io~1committed-skip-ordinals",value:$record}
   end)
]' ris-before-repair.json > membership-repair.json
kubectl patch ris selective-scaling-worker --type=json --patch-file=membership-repair.json
```

Use `--arg record ''` only when the verified intended target is dense
`[0,1,...,replicas-1]` and retirement of any higher instances is understood and
approved. This deletes the corrupt annotation with E absent, allowing ordinary
reconciliation to resume. Never use deletion as a generic sparse-record repair.

If a precondition fails, reread the objects and reassess the target; do not
remove the guards or replay a stale patch. Controllers resume immediately after
the repair. Verify committed membership, survivor identities and discovery
addresses, and confirm that the `InvalidCommittedOrdinals` condition clears.
Unrelated FailedScale conditions are retained.

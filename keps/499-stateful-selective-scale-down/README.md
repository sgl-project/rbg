# KEP-499: Stateful selective scale-down

English | [简体中文](README.zh-CN.md)

## Table of Contents

<!-- toc -->
- [KEP-499: Stateful selective scale-down](#kep-499-stateful-selective-scale-down)
  - [Table of Contents](#table-of-contents)
  - [Release Signoff Checklist](#release-signoff-checklist)
  - [Summary](#summary)
  - [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
  - [Proposal](#proposal)
    - [User Stories](#user-stories)
      - [Selective Scale-Down and Subsequent Scale-Up](#selective-scale-down-and-subsequent-scale-up)
      - [Removing Exclusions](#removing-exclusions)
    - [Design Overview](#design-overview)
    - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Risks and Mitigations](#risks-and-mitigations)
  - [Design Details](#design-details)
    - [Configuration and Admission](#configuration-and-admission)
    - [Member Selection and Commitment](#member-selection-and-commitment)
    - [Commit Replicas and Ordinal Records Together](#commit-replicas-and-ordinal-records-together)
    - [Internal Results and Annotation Isolation](#internal-results-and-annotation-isolation)
    - [RBGSet Scope](#rbgset-scope)
    - [Relationship to Existing Rollout Strategies](#relationship-to-existing-rollout-strategies)
    - [Autoscaling and Revision Management](#autoscaling-and-revision-management)
    - [Observability](#observability)
    - [Dependencies](#dependencies)
    - [Implementation Phases](#implementation-phases)
    - [Test Plan](#test-plan)
      - [Unit Tests](#unit-tests)
      - [Integration Tests](#integration-tests)
      - [End-to-End Tests](#end-to-end-tests)
    - [Graduation Criteria](#graduation-criteria)
    - [Upgrade / Downgrade Strategy](#upgrade--downgrade-strategy)
    - [Version Skew Strategy](#version-skew-strategy)
  - [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
    - [Feature Enablement and Disablement](#feature-enablement-and-disablement)
    - [Rollout, Upgrade and Rollback Planning](#rollout-upgrade-and-rollback-planning)
    - [Monitoring Requirements](#monitoring-requirements)
    - [Runtime Dependencies](#runtime-dependencies)
    - [Scalability](#scalability)
  - [Drawbacks](#drawbacks)
  - [Alternatives](#alternatives)
  - [Implementation History](#implementation-history)
  - [References](#references)
<!-- /toc -->

## Release Signoff Checklist

Check items only after completion and maintainer confirmation. This checklist
does not imply design, implementation, or release approval.

- [ ] Design review is complete and the KEP is approved for implementation.
- [ ] Member selection, persistence, write isolation, and integration with
      existing mechanisms are complete.
- [ ] Unit, integration, and end-to-end acceptance tests pass.
- [ ] Production readiness review is complete and approved.
- [ ] User documentation covers capacity trade-offs, commitment signals, safe
      disablement, and downgrade requirements.
- [ ] Implementation History is current, with links to implementation,
      validation, and release records.
- [ ] Maintainers confirm the target release and release notes.

## Summary

Use the role annotation `rbg.workloads.x-k8s.io/excluded-ordinals` to select
instances for stateful scale-down:

- Prefer the specified ordinals when scaling down.
- Skip the specified ordinals when scaling up.
- Editing the annotation without changing replicas does not create, delete, or
  replace instances.

Replicas and excluded ordinals can be edited together or separately.

Alpha is limited to native Stateful RIS roles on RBGs not managed by an RBGSet.

To replace an instance, set its exclusion and scale down, wait for the scale-down
target to be committed to the child RIS, then scale up. Wait for retirement as
well if the new instance needs resources released by the old instance.

Users configure one exclusion annotation. The controller persists committed
membership under a distinct internal annotation. No API field, policy setting,
or controller startup flag is added.

This is a provisional design. Controller behavior and runtime acceptance tests
are not implemented or verified by this documentation PR.

## Motivation

Stateful instances may have loaded models, warmed caches, or attached storage.
During node failure or maintenance, operators may want to remove a particular
instance while preserving the others.

The existing scale-down order cannot directly express that choice. Deleting an
instance may also cause the controller to recreate it at the same ordinal.

Exclusions let users influence scaling: which instances to prefer for removal
and which ordinals to avoid when adding capacity.

### Goals

- Prefer specified instances during scale-down.
- Skip excluded ordinals during scale-up.
- Support editing replicas and excluded ordinals in the same update.
- Preserve the names and UIDs of unselected instances without rebuilding them
  because of membership selection.
- Support ordinary stateful instances and leader/worker groups.
- Recover accepted membership after controller restart.
- Remain compatible with existing scaling, failure recovery, rollout, and
  revision-management mechanisms.

### Non-Goals

- Selective scale-down for RBGSet-managed RBGs.
- Automatically replacing instances without changing replicas.
- Automatically detecting node failure or changing the desired replica count.
- Selecting an individual Pod within a leader/worker group.
- Automatically draining traffic, migrating data, or cleaning up PVCs.
- Changing replica ownership by HPA, Scaling Adapter, or other components.
- Providing an exclusion input for standalone RIS objects, stateless roles, or
  other workload backends.

## Proposal

### User Stories

#### Selective Scale-Down and Subsequent Scale-Up

Starting with three instances, a user wants to remove instance 1 and use a
different ordinal during subsequent scale-up:

| Operation | Replicas | Excluded ordinals | Base members |
|---|---:|---|---|
| Initial state | 3 | `[]` | `[0,1,2]` |
| Set exclusions to `[1]` and commit the scale-down target | 2 | `[1]` | `[0,2]` |
| Scale up to 3, retaining exclusions | 3 | `[1]` | `[0,2,3]` |

Instances 0 and 2 are not rebuilt because of this membership selection. Instance
3 is created at a new ordinal.

Users can set exclusions and reduce replicas together, or set exclusions first
and scale down separately. Editing exclusions alone keeps existing members
unchanged.

Before scale-up, only confirm that the scale-down target is committed to the
child RIS. Instance 1 does not always have to finish deleting. Subsequent
scale-up selects new ordinals from committed membership.

This workflow replaces instances by reducing replicas first. Between scale-down
and subsequent scale-up, desired capacity falls by one per replaced instance;
for a batch, the reduction equals the scale-down amount. Serving capacity may
remain reduced until replacements are ready. GPU serving operators should
choose replacement batches and maintenance windows based on remaining capacity
and workload.

Committing the scale-down target does not mean GPU, memory, or other resources
have been released. If replacements need those resources, also wait for the old
instances to retire. Otherwise, scale-up can proceed from committed membership.

The same workflow can be used when node failure makes a Local PV inaccessible.
The user must still provide usable storage for the new instance. A new ordinal
does not automatically migrate data, change a fixed PVC reference, or clean up
old storage.

For leader/worker workloads, an ordinal identifies an entire group. Selecting
ordinal 1 removes that group's leader and all workers. Subsequent scale-up
creates a new group without changing its size. For example, a group size of 4
contains one leader and three workers.

#### Removing Exclusions

Starting with three replicas, exclusions `[1]`, and base members `[0,2,3]`,
clearing exclusions keeps base members `[0,2,3]`. It does not immediately create
instance 1 or delete instance 3.

A subsequent scale-up to four replicas can use ordinal 1 again, producing base
members `[0,1,2,3]`.

### Design Overview

Without exclusions, instances use consecutive ordinals as they do today.
For example, initially creating three instances gives:

```text
Replicas: 3
Exclusions: none
Base members: [0,1,2]
```

Three symbols describe the behavior:

| Symbol | Meaning | Example |
|---|---|---|
| N | Committed desired replica count on the child RIS. | `3` |
| E | User-configured excluded ordinals on the role. | `[1]` |
| M | Committed base members, sorted by ordinal. | `[0,2,3]` |

M contains N members and excludes temporary rollout surge instances.

M records desired membership, not the currently surviving objects. A failed or
missing instance remains in M and is recovered at its original ordinal.

E guides scaling. It does not immediately delete every listed instance, and
input order does not define priority. These symbols are explanatory and add no
API fields.

The controller first selects new members from M, the desired replica count, and
E. It commits replicas and membership results together to the child RIS, then
uses existing flows for creation and retirement.

### Notes/Constraints/Caveats

- At fixed replicas, editing, clearing, or losing E does not change committed M.
- When replicas and E change together, calculate membership using E from that
  update.
- Rapid consecutive replica edits before scale-down commitment may expose only
  the final value and do not guarantee the selected removal.
- Keep exclusions for ordinals that must still be avoided during scale-up.
- Persist user input and controller results separately. Do not copy role E
  directly into the child RIS membership result.
- Existing scaling and rollout strategies remain in use, with adaptation for
  nonconsecutive members and operational-annotation synchronization, revision,
  and write isolation.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Invalid exclusions cause unintended deletion | Reject at admission; if invalid input reaches reconciliation, block that role's scale-down without default-order fallback. |
| User input overwrites committed membership results | Use distinct keys, filter user-key propagation, and isolate ordinary synchronization. |
| Direct edits to a child RIS change desired membership | Child RIS objects are controller-managed and must not be edited directly. Editing or deleting `committed-skip-ordinals` takes effect immediately and is not automatically restored. |
| Exclusion edits trigger template rollout | Exclude operational annotations from revision calculation and preserve current input on rollback. |
| Reduced capacity or insufficient resources during replacement | Document the capacity trade-off and wait for retirement when resources must be released. |
| An old controller mishandles nonconsecutive members | Prohibit direct takeover; fill missing ordinals and confirm empty internal results before downgrade. |

## Design Details

### Configuration and Admission

The admission requirements in this section apply to the content and placement
of user configuration on RBGs and RBGSets. Alpha adds no validating webhook for
child RIS objects and no caller-identity checks on their internal result
annotation.

Configure exclusions in the RBG role annotations. Alpha rejects this key in
RBGSet templates and on RBGSet-managed RBGs.

Child RIS objects are controller-managed and must not be edited directly.
Editing or deleting `committed-skip-ordinals` immediately changes the desired
membership; the controller does not automatically restore the previous record.

The following is a role configuration fragment:

```yaml
name: decode
replicas: 3
annotations:
  rbg.workloads.x-k8s.io/excluded-ordinals: "1"
```

Individual ordinals and inclusive ranges are supported, for example:

```yaml
rbg.workloads.x-k8s.io/excluded-ordinals: "1,3,8-10"
```

Duplicate and overlapping entries are normalized.

| Operation | Membership behavior |
|---|---|
| Edit E without changing replicas | Keep M unchanged. |
| Scale down | Prefer members of M listed in E, then use the existing scale-down order for any remaining removals. |
| Scale up | Keep existing members; choose unused ordinals outside E in ascending order. |
| Remove exclusions | Keep M unchanged; those ordinals become eligible for subsequent scale-up. |

Replicas and E can change together. For example, starting with five members
`[0,1,2,3,4]`, changing replicas to 3 and E to `[1,3]` together leaves
`[0,2,4]` after scale-down.

For separate edits, scaling uses the role configuration observed by the
controller for that membership calculation.

Initial creation uses the scale-up allocation rule. For example, N of 3 and E of
`[1,3]` produce initial M of `[0,2,4]`.

Admission must reject:

- Invalid syntax, negative ordinals, reversed ranges, and numeric overflow.
- Exclusions on unsupported locations, including stateless roles and
  non-native-RIS backends.
- Users configuring the controller's internal result key on a role.
- The exclusion key on RBGSet templates or roles of RBGSet-managed RBGs.

Admission failures must return the field and specific reason in the API response,
rather than silently ignoring the input or applying it partially.

If invalid E still reaches reconciliation, the controller must block scale-down
for that role and report the reason through existing status and Warning events.
It must not treat invalid input as empty E or fall back to default deletion
ordering.

### Member Selection and Commitment

The controller starts from committed M and uses the desired replica count and E
from the same version of the role configuration to select the next base members.

For scale-down:

1. Calculate how many instances must be removed.
2. Prefer members of M that are listed in E.
3. If that is insufficient, select the remaining removals using the existing
   scale-down order.
4. Remove only the number required by this scale-down.

The existing scale-down order also applies within the same priority. E may
contain multiple ordinals or ordinals that are not currently members. Its size
need not equal the scale-down amount. Entries outside M do not cause deletions.

For scale-up:

1. Keep existing members.
2. Choose unused ordinals outside E in ascending order.
3. Add enough members to reach the desired replica count.

Without a replica-count change, membership is not reselected. When replicas and
E change together, calculate one membership target rather than performing two
separate adjustments.

Commit child RIS replicas and internal results in one conditional update.
Only after it succeeds, create or retire instances according to the new target.
After an update conflict, reread the latest role configuration and committed
membership before recalculating.

Membership commitment and actual creation or deletion are not atomic. During
execution, the live count may temporarily be above or below N; the existing
reconciliation flow converges it.

### Commit Replicas and Ordinal Records Together

Role E is user input for scaling. The child RIS `committed-skip-ordinals`
annotation records skip ordinals calculated and persisted by the controller.
They serve different purposes.

For roles with the exclusion annotation or non-empty skip records on the child
RIS, one flow calculates replicas and skip ordinals and commits them in the same
update request with a resource-version check.

For example, starting with five instances `[0,1,2,3,4]`, a user changes replicas
to 3 and E to `[1,3]` together:

| State | Child RIS replicas | Recorded skip ordinals | Desired instances |
|---|---:|---|---|
| Before commitment | 5 | Empty | `[0,1,2,3,4]` |
| After successful commitment | 3 | `[1,3]` | `[0,2,4]` |

Changing replicas to 3 before recording the skip ordinals could incorrectly
delete instance 4 using the default order.

Ordinary synchronization continues updating instance templates and other
configuration. It must not independently change, clear, or reset these replicas
or ordinal records. When changing its writing responsibilities, ensure that
omitting the relevant fields does not remove existing values or reset them to
defaults.

On commitment failure, retain the previous values. On conflict, reread the latest
role configuration and committed child RIS records before recalculating.
Ordinary synchronization must not independently write the desired replica count.

Initial child RIS creation must also establish replicas and skip ordinals
together. Clearing a record must be committed together with the corresponding
replica count. Create or retire instances for the new target only after
successful commitment.

After clearing E, these requirements still apply while the child RIS retains
non-empty skip records.

### Internal Results and Annotation Isolation

User input and controller results use distinct annotation keys:

| Location | Annotation key | Writer | Meaning |
|---|---|---|---|
| RBG role annotations | `rbg.workloads.x-k8s.io/excluded-ordinals` | User | Exclusions E for scaling. |
| Child RIS metadata annotations | `rbg.workloads.x-k8s.io/committed-skip-ordinals` | Controller | Ordinals to skip to represent committed membership. |

The controller maintains internal results. They are not user configuration and
add no RIS API field.

The child RIS replica count and internal result together determine M: starting
at zero, skip recorded ordinals and take the first N remaining ordinals.

For example:

```yaml
metadata:
  annotations:
    rbg.workloads.x-k8s.io/committed-skip-ordinals: "1"
spec:
  replicas: 2
```

This represents M of `[0,2]`.

Compute the internal result from accepted M rather than copying E. For example,
if E is `[1,3]` but a scale-down removes only instance 3 while retaining instance
1, the result must not exclude ordinal 1. Consecutive members need no non-empty
result.

Isolation requirements:

- Filter the user exclusion key from both paths in
  `constructRoleInstanceSetApplyConfiguration`: `GetCommonAnnotationsFromRole`
  and the direct merge of `role.Annotations`.
- Do not copy the user key into RIS results or propagate it into RI or Pod
  templates.
- Ordinary template synchronization and forced SSA must not overwrite, remove,
  or acquire ownership of the internal result key.
- Controller restart, removal of the role exclusion annotation, and temporary
  instance loss do not change committed membership.
- Check object UID and ownership so a new object does not inherit membership
  from a deleted object.

At N of zero, desired membership is empty. An invalid internal result must
produce an error and block membership changes that depend on it, rather than
silently falling back to consecutive ordinals.

### RBGSet Scope

Alpha does not support exclusions on RBGSet-managed RBGs. Admission must reject
`excluded-ordinals` both in RBGSet templates and on roles of RBGSet-managed RBGs.

KEP-455 makes child specs template-owned. Independent child scaling and
preservation of operational input and committed records through updates and
recreation require follow-up design; they are outside this Alpha scope.

### Relationship to Existing Rollout Strategies

This proposal does not change existing rollout strategies. The existing
`partition`, `maxSurge`, `maxUnavailable`, health checks, `minReadySeconds`, and
pause rules remain in use.

Rollouts use committed base members M on the child RIS, not the role's E.
Editing E alone does not change M, trigger a template rollout, or move the
canary scope.

For nonconsecutive members, partition applies to positions in the sorted member
list. For example:

```text
M = [0,2,4]
partition = 2
```

The first two members, 0 and 2, retain the old revision; member 4 is eligible for
upgrade.

Implementation must adapt member identification, rollout surge allocation and
cleanup, and completion checks to nonconsecutive members. It adds no new rollout
strategy or budget rule.

### Autoscaling and Revision Management

HPA, Scaling Adapter, and other components continue managing replicas as they do
today. This proposal adds no configuration, writer responsibility, or ownership
handoff flow for them. Replica changes use the same member-selection rules.

Exclusion input and internal results are membership information and must not
trigger template revision changes. Explicit revision isolation is required:

- Strip the user exclusion key in `getRBGPatch`, like replicas, so
  `GetRolesRevisionHash` and `SetMatchesRevision` use consistent revision content.
- In `ApplyRevision`, preserve current E by role name, including absence, rather
  than restoring a historical exclusion value.
- Exclude internal results from template revision snapshots. Template rollback
  must not overwrite or remove committed membership results.
- Setting, editing, or clearing E must not create a new ControllerRevision or
  change the role revision hash or role revision label on the RIS.
- Roles without the key retain existing hash inputs without injected defaults
  or empty annotations. Handle empty maps consistently to preserve the hash.

Template revision generation, selection, and rollback retain their existing
business flow, with the operational-input isolation above.

### Observability

Observe membership commitment through the child RIS replica count and internal
result. For example, from the same object read:

```text
spec.replicas = 2
committed-skip-ordinals = "1"
```

This records members `[0,2]`; subsequent scale-up can use that membership.

Observe retirement and readiness through existing instance status. Commitment
does not mean the old instance has been deleted or the new one is available.

Report invalid input, invalid internal results, and blocked execution through
existing status and Warning events with specific reasons.

### Dependencies

This proposal depends on existing native Stateful RIS and RI scaling and recovery
flows, admission, revision management, status, and events.

HPA and Scaling Adapter continue managing replicas without a new handoff flow.
Existing operational procedures remain responsible for traffic draining, data
migration, and storage preparation.

### Implementation Phases

1. **Configuration and persistence:** implement parsing, admission, member
   selection, distinct result persistence, atomic commitment, and write isolation.
2. **Existing-mechanism integration:** implement revision isolation and rollout
   handling for nonconsecutive members.
3. **Recovery and acceptance:** verify conflict retries, controller restart,
   autoscaling, safe disablement, and constrained downgrade.

All necessary paths must be complete and pass acceptance before Alpha use.
Parsing the annotation or skipping ordinals alone is not a complete
implementation.

### Test Plan

The following are implementation acceptance requirements, not claims that
controller implementation or runtime verification has been completed.

#### Unit Tests

- Parse individual ordinals, ranges, duplicates, and overlaps.
- Reject invalid syntax, negative ordinals, reversed ranges, and overflow.
- Keep M unchanged when E is edited or cleared without changing replicas.
- Prefer excluded members during scale-down and remove only the required count.
- Preserve members and skip exclusions during scale-up.
- Select correct members when exclusions contain more or fewer members than the
  scale-down amount.
- Cover zero replicas and encoding and recovery of nonconsecutive membership.
- Cover partition and revision selection for nonconsecutive members.

#### Integration Tests

- Reject invalid input and unsupported roles or locations at admission,
  including RBGSet templates and RBGSet-managed RBGs.
- Reject the controller's internal result key in role configuration.
- If invalid input bypasses admission, report the error and block scale-down for
  that role without default-order fallback.
- Select correct members when replicas and E change together, including clearing
  E while scaling.
- Allow scale-up from new M after scale-down commitment even before retirement
  completes.
- Commit child replicas and results atomically, covering concurrent updates,
  conflicts, and failed retries.
- Preserve committed membership through controller restart, missing instances,
  and removal of the role annotation.
- At fixed replicas, set, edit, or clear E, then repeatedly exercise both
  annotation propagation paths and forced SSA without changing internal results
  or M.
- Setting, editing, or clearing E leaves revisions, hashes, revision matching,
  and revision labels unchanged. Rollback preserves current E, including absence.
- Cover rollout, surge cleanup, and completion for nonconsecutive members.
- Preserve existing behavior and hashes for roles that do not use the feature.
- When ordinary synchronization runs before membership commitment, preserve the
  previous child RIS replicas and ordinal records.
- When commitment conflicts or fails and ordinary synchronization then runs, do
  not let the desired replica count take effect independently.
- Establish replicas and records together at initial child RIS creation, and
  commit record clearing together with the corresponding replica count.
- Repeated ordinary synchronization must not overwrite, clear, or reset
  committed replicas or ordinal records.
- Changing ordinary synchronization's writing responsibilities must not
  accidentally remove existing values or reset them to defaults.
- After clearing E, continue committing replicas and records together while the
  child RIS retains non-empty skip records.

For replicas changing from 5 to 3 together with E set to `[1,3]`, assert that the
state remains "replicas 5, empty record" before commitment and becomes "replicas
3, record `1,3`" after success. The intermediate state "replicas 3, empty record"
must never occur.

#### End-to-End Tests

- Cover manual scaling, HPA, and Scaling Adapter, observing commitment through
  child RIS values.
- Exercise selective scale-down and scale-up after node failure.
- Cover Local PV workflows with externally prepared storage.
- Scale down and up complete leader/worker groups.
- Cover resources not yet released, blocked retirement, unready instances, and
  controller restart.
- Cover safe disablement by removing role input and controller downgrade after
  its conditions are satisfied.

### Graduation Criteria

The role annotation explicitly opts into this feature. No global flag or default
configuration is added.

Alpha requires complete member selection, distinct result persistence, admission,
write and revision isolation, restart recovery, and adaptation to nonconsecutive
members, with corresponding tests.

Beta requires coverage for multicomponent instances, autoscaling, and failure
recovery, together with operational documentation. Maintainers must review input
limits and resource bounds.

This proposal commits to no target release or graduation date.

### Upgrade / Downgrade Strategy

After controller upgrade, objects that do not use the feature retain existing
members, write behavior, and revision hashes.

Deploy supporting controllers and admission components before enabling the
feature.

Removing the role's `excluded-ordinals` annotation safely disables user-selected
exclusions. Subsequent scaling no longer references the previous E. Existing
members and internal results remain unchanged, without triggering replacement.

`disable-supported: true` refers to this safe disablement behavior, not support
for directly downgrading the controller.

Direct downgrade is unsupported while non-empty internal results exist. Before
downgrading, clear E and scale out to fill the missing ordinals, confirm that
internal results are empty or removed, and wait for reconciliation to complete.
For example, `[0,2,3]` can first scale out to `[0,1,2,3]`.

Do not bypass this condition by directly deleting internal results.

### Version Skew Strategy

| Scenario | Requirement |
|---|---|
| New controller, objects not using the feature | Preserve existing behavior. |
| New controller, nonconsecutive members | Reconcile from committed results. |
| Old controller, nonconsecutive members | Unsupported. |
| API version conversion | Preserve role input and child RIS internal results. |

Before enabling the feature, every controller instance that may take over
reconciliation must understand internal results. An old instance must not regain
control and treat nonconsecutive members as consecutive.

## Production Readiness Review Questionnaire

### Feature Enablement and Disablement

Enable through the role annotation and disable by removing it. No controller
restart is required.

Disablement preserves existing members and internal results. On reenablement,
new E affects only subsequent scaling.

Regression tests must verify enablement, disablement, and reenablement.

### Rollout, Upgrade and Rollback Planning

Template rollback preserves current E and committed membership results.
Controller downgrade requires the conditions in "Upgrade / Downgrade Strategy".

Replacement uses scale-down followed by scale-up and does not guarantee
unchanged capacity during the operation. Before rollout, confirm that remaining
serving capacity can handle the workload and choose replacement batches and
maintenance windows. Scale-up can proceed after the scale-down target is
committed; wait for retirement as well when reusing the old instances' resources.

This KEP documents the design only. Controller implementation and runtime
validation remain pending.

### Monitoring Requirements

- Read child RIS replicas and internal results together to observe commitment.
- Inspect instance status to distinguish retirement, creation, and readiness.
- Use existing status and Warning events to observe invalid input, result
  errors, and blocked execution.
- Observe serving capacity using replicas, instance readiness, and workload
  monitoring. GPU serving operators should also watch throughput and request
  latency during replacement.

### Runtime Dependencies

Use existing controller, admission, scheduling, and storage flows without new
external-service dependencies.

Existing operational procedures remain responsible for traffic draining, data
migration, and storage preparation.

### Scalability

Normalize ranges without expanding their length or iterating to the largest
excluded ordinal.

During implementation, confirm limits on input entries, ordinal bounds, and
annotation size. Measure CPU, memory, and API writes for long ranges, multiple
roles, and conflict retries.

No capacity or performance conclusion is claimed.

## Drawbacks

- Selected replacement requires scale-down then scale-up, reducing desired
  capacity by one per replaced instance. Restoring serving capacity also requires
  replacements to become ready.
- The controller must maintain membership results and adapt synchronization,
  revision, and rollout paths.
- Old controllers cannot directly take over nonconsecutive members; preparing
  for downgrade may require temporary scale-up.
- This proposal does not provide traffic draining, data migration, or storage
  cleanup, or guarantee request-level zero interruption.

## Alternatives

An opt-in, one-directional `Immediate` mode could be considered as future work
for workloads that cannot accept the capacity trade-off of scaling down first:

- Newly added exclusions could replace matching base members without changing
  replicas.
- Removing exclusions would leave existing membership unchanged, without
  restoring previous ordinals.
- It would require explicit user selection and would not change default scaling
  behavior.

This is outside the current Alpha scope and adds no configuration in this
proposal. Capacity budgets, execution ordering, and failure recovery need a
separate review. Preserving available capacity would also depend on available
resources and replacement readiness.

Future protection of the internal key could use a content-based rule requiring
changes to `committed-skip-ordinals` to accompany the corresponding replica
update in the same request. Such validation requires separate review and is not
part of Alpha.

## Implementation History

- 2026-10-23: Created the design proposal for community review.
- 2026-10-29: Incorporated maintainer feedback on configuration isolation,
  revision management, commitment signals, and safe disablement. Added capacity
  trade-offs, a future alternative, and template review sections. Controller
  implementation, runtime validation, and release approval remain pending.
- 2026-10-30: Excluded RBGSet-managed RBGs from Alpha and removed identity-based
  admission requirements for child RIS records. Documented direct-edit effects
  and the absence of automatic restoration.

## References

- [Issue #499](https://github.com/sgl-project/rbg/issues/499)
- [PR #513](https://github.com/sgl-project/rbg/pull/513)
- [KEP-455: RBGSet rolling update](../455-rbgs-rolling-update/README.md)
- [Kubernetes local volumes](https://kubernetes.io/docs/concepts/storage/volumes/#local)

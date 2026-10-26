# KEP-455: Rolling Update for RoleBasedGroupSet

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
- [Goals](#goals)
- [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Child Template Propagation](#child-template-propagation)
  - [Revision Management](#revision-management)
  - [Scenario Handling](#scenario-handling)
  - [Availability and Surge](#availability-and-surge)
  - [Child Rollout Barrier](#child-rollout-barrier)
  - [Status](#status)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Reconcile Tests](#reconcile-tests)
    - [E2E Tests](#e2e-tests)
- [Appendix](#appendix)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Add opt-in, revision-aware rolling updates to RoleBasedGroupSet (RBGSet).
The update unit is one child RoleBasedGroup (RBG). `InPlaceUpdate`, the default
when a strategy object is present, updates that child without changing its UID
and lets each role decide how its Pods move. `Recreate` deletes and rebuilds the
child, including every downstream workload and Pod. Both strategies use
`partition` and `maxUnavailable`; only `Recreate` uses `maxSurge`.

An omitted `spec.rolloutStrategy` keeps the legacy semantics: no group-level
ordering, availability gating, or revision labels. Enabling rolling updates is
still an explicit choice, not a side effect of upgrading the operator. One
propagation fix applies to every set: children now receive the full
`groupTemplate.spec`, including `roleTemplates` (see Child Template Propagation).

## Motivation

An RBG can represent a complete serving unit with cooperating roles. Updating
all child RBGs together can disrupt the whole service, even when each role has
its own Pod update policy. RBGSet needs to control **which groups change and
how much serving capacity can be removed**, independently of updates inside a role.

Partitioning also requires a durable old template: comparing every child only
against the latest template cannot reconstruct an old-version group after deletion.

## Goals

- Roll out at group granularity with stable names and ordinals.
- Support `maxUnavailable`, `maxSurge`, and ordinal-based `partition`.
- Preserve the legacy update semantics for sets that do not opt in — no
  group-level ordering, availability gating, or revision labels — without mass
  relabeling or restarting existing children when they do opt in. How much of
  the template reaches a child is not part of that contract and changes on both
  paths (see Child Template Propagation).
- Report rollout start and completion in RBGSet status without "false rolling transitions"
  caused by fault recovery, manually deleted RBGs, or similar events.
- Keep role-replica-only changes in place under `Recreate`; `InPlaceUpdate`
  paces every spec change without deleting the child RBG.

## Non-Goals

- Guaranteeing that `InPlaceUpdate` preserves every Pod; the role-level update
  strategy still decides whether an image can be patched or a Pod must be
  replaced.
- Adding `paused`, automatic rollback, or automatic promotion of a partition.
- Making role scaling adapters usable under a RBGSet. The set owns role
  replicas, so adapters stay unsupported on both paths.

## Proposal

### User Stories

- **New deployment:** Create a RBGSet with rolling updates enabled. All initial
  children use the requested template; future changes advance gradually.
- **Existing deployment:** Add a strategy to an already converged RBGSet. Matching
  children keep their UIDs, even without revision labels. A later template
  change starts the controlled rollout.
- **Staged rollout:** With four groups and `partition: 2`, update ordinals 2
  and 3 first. Ordinals 0 and 1 remain on the old version. Lower the partition
  to release the rest.

### Risks and Mitigations

| Risk | Handling                                                                                                                                                                          |
|------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Slow foreground deletion occupies an ordinal and consumes capacity | Count terminating children against the capacity limit and wait for deletion; do not reuse their names prematurely.                                                                |
| Controllers write metadata onto child RBGs | Exclude explicitly enumerated system keys from revision identity, preserve child-owned values during sync, and still propagate template-owned compatibility annotations. Do not exempt all project-prefixed annotations. |
| A metadata-only `InPlaceUpdate` retains old Ready and rollout conditions | Persist a new request with the child update; keep the child unavailable to the rollout budget until its controller acknowledges downstream completion. Include Pod-shaping group annotations in expected role hashes. See [Child Rollout Barrier](#child-rollout-barrier). |
| Existing children differ when rolling updates are first enabled | Bootstrap one old revision from the lowest existing base ordinal; do not assume every child has that content. Recommend enabling on a converged set before changing its template. |
| A child RBG's spec was edited directly, or predates `roleTemplates` propagation | Overwriting the whole child spec reconciles the drift back to the template; existing drifted children are rewritten once on upgrade. Bounded by the two fields `RoleBasedGroupSpec` has. Record it in the release notes as a behavior change rather than describing the legacy path as unchanged. |
| Older clients read or write the set through v1alpha1 | The conversion webhook stashes `spec.rolloutStrategy` and the v1alpha2-only rollout status fields in `conversion.workloads.x-k8s.io/` annotations on the v1alpha1 view and restores them on write. The round-trip is not lossless: v1alpha1 has no representation for `groupTemplate.labels` and `groupTemplate.annotations`, so such a write drops both maps apart from the compatibility keys restored from the set's own annotations, which changes revision identity and rolls every child. Record in the release notes that rolling sets must be written through v1alpha2. The rendered CRD manifests configure this webhook; a cluster whose CRD lacks it falls back to structural pruning, in which case v1alpha1 writes are not supported. |

## Design Details

### API Changes

The strategy is a flat, optional field on the **v1alpha2 RBGSet**, separate from
`groupTemplate.spec.roles[].rolloutStrategy`.

Both RBG API versions also expose the optional string
`status.observedRolloutRequest`. This acknowledges the controller-owned
`rbg.workloads.x-k8s.io/rollout-request` annotation on a child RBG; users do not
set it in the group template. See [Child Rollout Barrier](#child-rollout-barrier).

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroupSet
metadata:
  name: rbgs-rolling
spec:
  replicas: 2
  rolloutStrategy:
    type: InPlaceUpdate
    partition: 0
    maxUnavailable: 1
    maxSurge: 0
  groupTemplate:
    spec:
      roles:
      - name: server
        replicas: 1
        standalonePattern:
          template:
            spec:
              containers:
              - name: nginx
                image: nginx:1.27
```

| Field | Default | Meaning |
|-------|---------|---------|
| `type` | `InPlaceUpdate` | Preserve the child RBG's name and UID, overwrite its spec and template metadata, and let the role-level strategy move its Pods. `Recreate` deletes and rebuilds the child instead. |
| `partition` | `0` | Hold back ordinals in `[0, partition)`; update base ordinals in `[partition, replicas)`. Integer or percentage, rounded down. Not bounded by `replicas`: the controller resolves `min(partition, replicas)`, matching StatefulSet (see Scenario Handling). |
| `maxUnavailable` | `1` | Unavailability budget in RBG units. Integer or percentage of replicas, rounded down and capped at replicas. `InPlaceUpdate` requires a positive declared value and enforces a resolved minimum of 1. |
| `maxSurge` | `0` | `Recreate` only: maximum extra RBGs above replicas, rounded up. This is a ceiling, not a target. `InPlaceUpdate` ignores the field, and admission permits only omitted/`0`/`0%`. |

An absent strategy uses the legacy path; `rolloutStrategy: {}` opts into
`InPlaceUpdate`. All three numeric fields accept a non-negative integer or
`0%-100%`, enforced by CRD CEL and webhook. `Recreate` rejects zero
`maxUnavailable` together with zero `maxSurge`, including `0%`; a positive
percentage that resolves to zero is allowed and falls back to
`maxUnavailable = 1` in the controller.

There is no `paused` field for now. `partition: "100%"` holds back updates to existing
base groups, but does not freeze scaling, missing-group recovery, or initial creation.

Admission rejects enabled role scaling adapters on create and newly enabled
adapters on update, including on newly added roles. Adapters already enabled
on the same role are grandfathered for update compatibility; this does not make
the conflicting configuration supported.

The conflict is not limited to the rolling path, and it does not resolve in the
adapter's favour. The adapter writes role replicas onto the child RBG while
RBGSet propagates the same roles from `groupTemplate`. With webhooks enabled,
the RBG validating webhook rejects the set's write with
`cannot be changed to <N> while scalingAdapter.enable is true`, so the set
requeues without converging and its rollout stalls on that group; where webhooks
are disabled, the set overwrites the adapter's value back instead.

An existing set that already has an enabled adapter should disable it
(`roles[].scalingAdapter.enable: false`, or remove the field) before opting into
`rolloutStrategy`, and keep it disabled afterwards. Admission only blocks
newly enabled adapters, so clearing a grandfathered one is a manual migration
step, not an automatic rejection. Role replicas are then changed in the set's
`groupTemplate`; a replicas-only edit follows the in-place path described in
Scenario Handling.

### Child Template Propagation

Both paths create children from the full `groupTemplate.spec` and update them by
overwriting the child's whole spec rather than `spec.roles` alone. This applies
to sets without a strategy as well, so omitting `rolloutStrategy` does not keep
child propagation byte-for-byte identical to the previous release.

- **`roleTemplates` are propagated.** Children previously received only
  `spec.roles`, so a set template defining `roleTemplates` produced children that
  could not resolve a `templateRef`. Propagation closes that gap.
- **Drift written directly onto a child is reconciled back.** Because the drift
  comparison now covers `roleTemplates`, a `spec.roleTemplates` entry added to a
  child RBG out of band is replaced by the set template. Children carrying such
  drift are rewritten once on controller upgrade; the rewrite is one-shot, not a
  reconcile loop.
- **Blast radius is bounded.** `RoleBasedGroupSpec` holds only `roles` and
  `roleTemplates`, so overwriting the whole spec cannot discard another field
  today. A set whose template has no `roleTemplates`, and whose children have
  none either, sees no change at all.

A child RBG is owned by its set, and the template is the source of truth for its
spec. Editing a child directly is not a supported way to make one group differ.

`syncRBGMetadata` preserves child-side values for the enumerated system keys,
then applies template annotations over them. Excluding a key from revision
identity does not exclude it from propagation. The rollout-request key is an
exception: only the set controller generates it; template values cannot copy,
reset, or override it.

On the rolling path, creation uses the selected revision's spec and user
metadata, then copies unversioned system annotations from the live template.
These live annotations also converge in place on settled, partition-held, and
retained surge children, independently of revision changes and rollout budgets.
In particular, `conversion.workloads.x-k8s.io/v1alpha1-coordination` must reach
every child so the RBG controller can reconcile its `CoordinatedPolicy`. Template
absence does not erase child-side compatibility data.

`conversion.workloads.x-k8s.io/v1alpha1-pod-group-policy` is handled separately:
its raw value is stored on the set-owned `ControllerRevision` metadata, while the
translated gang configuration participates in the normalized template. Creation
below partition restores the selected revision's policy rather than the live
policy. Removing a policy only clears a child stash proven to originate from a
set-owned snapshot; a child-only conversion stash is preserved.

### Revision Management

Only the rolling path manages RBGSet revisions. Each reconcile ensures an
`apps/v1 ControllerRevision` exists for the current `groupTemplate`; a matching
snapshot is reused rather than created on every reconcile.

- **Identity:** `rbgs-` plus the hexadecimal encoding of the first 24 bytes of
  `SHA-256(set UID + NUL + normalized template JSON)`. The snapshot stores the
  template and is owned by the set. Its numeric `revision` records the set
  generation at creation; the content-derived name is the template identity.
- **Canonical payload:** The hashed bytes and the stored `data` are the same
  canonical JSON — sorted keys, standard HTML escaping, integers preserved — not
  Go struct field order. `ControllerRevision.data` is immutable and compared
  byte-wise, while a strategic-merge patch re-serializes the whole object with
  sorted keys, so a struct-ordered payload fails validation on any such patch.
  That includes the patch garbage collection uses to orphan built-in dependents:
  `kubectl delete --cascade=orphan` then blocks on the orphan finalizer instead of
  leaving the children behind. Canonicalizing before release avoids renaming every
  stored revision afterwards.
- **Normalization:** Sort roles and role templates by name, normalize legacy
  update-strategy spellings, and exclude set identity/revision labels and the
  following enumerated system annotations from revision hashing and content
  drift comparison:
  `rbg.workloads.x-k8s.io/discovery-config-mode`,
  `rbg.workloads.x-k8s.io/rollout-request`,
  `conversion.workloads.x-k8s.io/v1alpha1-coordination`,
  `conversion.workloads.x-k8s.io/v1alpha1-pod-group-policy`,
  `conversion.workloads.x-k8s.io/v1alpha2-rollout-strategy`,
  `conversion.workloads.x-k8s.io/v1alpha2-rollout-status`.
  The same key set identifies values to preserve during metadata sync, not keys
  to discard from child creation or template propagation. User metadata,
  translated gang settings, and role replica counts remain part of the revision.
- **Current versus update:** `updateRevision` represents the desired template.
  On first entry, `currentRevision` is bootstrapped from the lowest existing
  base ordinal, or equals update when no base children exist. Thereafter, it is
  read from persisted history and advanced only on full completion.
- **Label timing:** Newly created base and surge RBGs receive
  `rbg.workloads.x-k8s.io/groupset-revision`. An eligible in-place child update,
  including a role-replica-only update under `Recreate`, writes the target
  revision label in the same Update. Matching unlabeled legacy children are not
  bulk relabeled merely to enable the feature.
- **Comparison:** A non-empty, different label marks a child outdated. An equal
  or absent label still requires normalized content comparison, including
  `rolesEqual`; labels do not hide external drift.
- **Retention:** Protect current, update, and revisions referenced by children;
  retain up to five additional unused revisions. Save the current reference
  before deleting children that may be the last copies of the old template.

### Scenario Handling

| Scenario | Principle |
|----------|-----------|
| Existing or new set without a strategy | Keep the legacy path: update child objects in place without group-level ordering or availability gating; do not add RBGSet revision labels. Downstream controllers retain their existing update behavior. Children still receive the whole `groupTemplate.spec`, including `roleTemplates` (see Child Template Propagation). |
| New set with a strategy | Create all desired base groups at update revision, including those below partition. Partition preserves an existing old version; it does not prevent initial creation. |
| Existing set opts in without changing its template | Persist revision snapshots, compare unlabeled children by content, and retain matching children without recreation. |
| Existing set keeps a grandfathered role scaling adapter | Unsupported configuration: the set and the adapter both own role replicas, so either the set's propagation is rejected and its rollout stalls on that group, or the adapter's value is overwritten. Disable the adapter before opting in and keep it disabled; scale by editing the template instead. Admission does not remove such an adapter. |
| Opt-in and template change happen together | Preserve the lowest existing base child's template as current; reconcile actual mismatches against update within partition and budgets. |
| General template change | Update eligible outdated groups in descending ordinal order. `InPlaceUpdate` preserves the child UID and writes the whole target spec/metadata; `Recreate` preserves the name but changes the UID. |
| Only `roles[].replicas` changes | Both strategies update eligible children in descending ordinal order under the normal availability budget and wait for downstream completion before advancing. `Recreate` preserves the child UID instead of deleting it; this avoids recreation, not availability accounting. |
| User template labels or annotations change | They participate in the revision. `InPlaceUpdate` writes a new request with the metadata and keeps its budget slot occupied until downstream completion is acknowledged, even though child generation does not change. `Recreate` follows group-recreation semantics. System keys follow the separate propagation rules above. |
| Set scales out | Missing new ordinals are created at update revision, even below partition. Existing surge children that become base children are retained and follow partition rules. The internal `groupset-observed-replicas` checkpoint distinguishes scale-out from replacement. |
| Set scales down during rollout | Re-evaluate base and surge ranges. Remove excess children under the availability budget; children in the new surge range may remain until the partition batch can safely release them. |
| An autoscaler scales `replicas` below an absolute `partition` | Accepted: the validating rule does not match `rolebasedgroupsets/scale`, and admission does not bound `partition` by `replicas`, so the object stays writable for every later request. The controller resolves `min(partition, replicas)`, as StatefulSet does. Rationale in the Appendix. |
| A base RBG is deleted | Recreate a previously existing ordinal below partition from current revision; otherwise use update revision. Restore capacity without treating recovery alone as a new template rollout. |
| Children were orphaned by a `--cascade=orphan` deletion of the set | The rolling path considers children that are owned **or adoptable** — no controller reference plus this set's name label — and adopts the orphans before any budgeted write, matching the legacy path. A create collision re-reads through the uncached reader and adopts an adoptable orphan rather than failing, so the ordinals above it are still filled. Only an object controlled by a different set UID stays an error, because claiming it would steal another set's child. Adoption transfers ownership only; the child then rolls under the normal budget and order. |
| A Pod fails but its RBG still exists | Let the downstream controllers recover it; lack of readiness alone is not a reason for RBGSet to recreate a matching group. |
| A group is already unavailable when a surge rollout starts | Its failure has spent part of `maxUnavailable`, but it must not stall the batch: surge is sized from the headroom the ready base count actually has left, so the serving in-scope groups still get their replacements and capacity stays at the floor. The failed group never becomes an update candidate. |
| Template changes again during rollout | Re-evaluate both base and surge groups against the latest update revision. An outdated, unready surge group must not permanently occupy the only surge slot. |
| Partition is lowered | Resume the previously held-back work, even though update revision is unchanged. Increasing partition does not actively roll back already updated children. |
| Strategy is removed | Return to legacy propagation, without rolling budgets. Removing the strategy is not a pause operation. |
| Strategy switches between `Recreate` and `InPlaceUpdate` | Validate the target mode. Existing out-of-range surge groups are reclaimed under the availability budget; base groups then follow the new update action. |
| Full-object write through v1alpha1 (older tooling, GitOps pinned to the deprecated version) | The conversion webhook stashes the strategy and the v1alpha2-only rollout status fields on the v1alpha1 view and restores them on write, so the set stays on the rolling path. v1alpha1 can express neither the strategy nor `groupTemplate.labels`/`groupTemplate.annotations`: changing or removing the strategy requires v1alpha2, and a v1alpha1 write drops both maps apart from the compatibility keys restored from the set's own annotations, which changes revision identity and rolls every child. Write rolling sets through v1alpha2. |

The old-revision recovery rule restores the persisted **current template**, not
an arbitrary last-seen template of each individual child. Controller restarts
must not lose that reference or the observed-replicas checkpoint.

### Availability and Surge

`groupSetChildReady` is shared by both strategies. A ready child is not
terminating, has `status.observedGeneration >= metadata.generation`, and has
`Ready=True`. If a rollout request exists, its value must match
`status.observedRolloutRequest`. A present `RollingUpdateInProgress` condition
must be `False` with `observedGeneration >= metadata.generation`; a missing
condition retains the legacy fallback, but never bypasses a pending request.
Ready base and surge groups both contribute to serving capacity.

Both strategies use authoritative API reads and an availability floor of
`max(0, replicas - maxUnavailable)`. Acting on a ready outdated group consumes
budget; an already-unready outdated group does not. Skip a budget-blocked
candidate rather than preventing repair of lower, unready ordinals. `Recreate`
uses foreground deletion with UID/resource-version preconditions and counts
terminating objects toward creation capacity.

The same availability gate applies to `Recreate` replicas-only in-place updates:
scaling a role from one to two replicas can make its RBG unready until the new
replica is ready, even though the old replica continues serving. Updating a
ready child therefore reserves one availability slot. The request remains
unacknowledged until the desired workload capacity is updated and ready; only
then may the next ready child advance. Existing ready surge can back a zero
`maxUnavailable`, just as for a replacement. In-place scaling is not a separate,
unbounded update path.

`maxSurge` applies only to `Recreate`. Surge occupies ordinals
`[replicas, replicas + maxSurge)` and serves like any other RBG. Surge is sized
from the live availability budget: only in-scope base groups that are **still
serving** need cover, the headroom they get is whatever the ready base count
leaves above the availability floor, and surge groups that already exist are
subtracted. Sizing it from the declared `maxUnavailable` instead lets an existing
failure spend the whole allowance, after which no surge is created even where
`maxSurge` permits it and the rollout waits for that group to recover on its own
— including a failure in a group held back below the partition, which no rollout
touches. For `replicas=4, maxSurge=5, maxUnavailable=0`, four serving base groups
need at most four surge groups, not five, and with `maxUnavailable=1` the same
batch needs three. No surge is created just because held-back old groups remain
after the partition batch completes.

Once all in-scope base groups match update and are Ready, reclaim surge rather
than retain a permanent canary. Reclamation still respects the availability
floor: a failure below partition may delay safe release, but does not itself
make that held-back group an update candidate. Budgets govern controller actions;
they cannot prevent external failures from reducing availability.

### Child Rollout Barrier

A child matching the set revision has received the requested template; this is
not evidence that its workloads have finished updating. In particular, a
metadata-only child write does not increase `metadata.generation`, so old
`Ready=True`, `observedGeneration`, and `RollingUpdateInProgress=False` values
remain current until the child controller runs. Generation checks, expected
workload identity, and a persistent request/acknowledgement close different parts
of this asynchronous update window.

**Expected workload identity.** The RBG controller derives per-role hashes from
the persisted role revision, then folds in two Pod-shaping group annotations:
`rbg.workloads.x-k8s.io/group-exclusive-topology` when present and
`rbg.workloads.x-k8s.io/group-gang-scheduling` only when its value is `"true"`.
These inputs are combined with each role hash using FNV-32a and safe encoding.
Without either input, the existing hash is unchanged. PodGroup-only settings,
such as scheduling timeout, Volcano queue, and priority class, are not added to
role hashes. A hash-only relabel of an already matching workload does not itself
change its Pod template or require Pod replacement.

**Rollout condition.** During status construction, before workload application,
the RBG controller checks every target workload with uncached (`apiReader`)
reads. Each workload must be controlled by the RBG, not terminating, and carry
the expected role-revision label. It must also have observed its generation
where supported, with the desired live replica count, target revisions, and
readiness. The controller publishes `RollingUpdateInProgress=True` while any
role fails these checks and `False` when all pass; the condition and
`status.observedGeneration` refer to the RBG generation being reconciled.
Changing a Pod-shaping annotation therefore invalidates the old workload label,
even though the RBG generation is unchanged. Workload template updates then
increase the downstream generation, which prevents early completion while Pods
are still rolling.

**Request writer.** In the rolling path, every budgeted child write advances
`rbg.workloads.x-k8s.io/rollout-request` on that child in the same resource-version
protected `Update` as the spec, metadata, and set revision label: an in-place template
update, a user-metadata-only change, and a `Recreate` replicas-only update, which is
availability-gated and acknowledged like any other rather than a shortcut. The value is
a per-child decimal counter, starting at `"1"` when no previous counter exists;
actual later writes increment it. Reconciles that make no change do not advance
it. The key is excluded from template identity and preserved during metadata
sync, but is never copied from the template. Repeated content such as `A -> B -> A`
therefore receives a new request rather than reusing an old acknowledgement.

New children have no prior completed status to invalidate and receive no request
on creation. The static path and unversioned compatibility-annotation convergence
do not issue requests or acquire rolling budget; their existing independent
propagation semantics are unchanged.

**Acknowledgement writer.** RBG, not RBGSet, writes the optional string
`status.observedRolloutRequest`, available in both API versions and preserved by
conversion. For a pending request, it acknowledges only after policy migration,
PodGroup reconciliation, role workload application, and orphan cleanup succeed.
It then directly rechecks every role workload for rollout completion. Until all
pass, the old acknowledgement remains, even if the pre-application status check
reported `False`. The acknowledgement status patch carries existing conditions
and role status through the shared SSA serializer; another status write must not
clear it. An unacknowledged request is the barrier's only periodic requeue: it
fires after two seconds even when the condition currently reports `False`.
`RollingUpdateInProgress=True` by itself requeues nothing — completion still
arrives through workload watch events — so children that are merely Pending or
unready do not put their parent on a polling loop.

**Consumer and recovery.** `groupSetChildReady` requires a matching
request/acknowledgement whenever the annotation exists, in addition to generation,
Ready, and rollout-condition checks. A mismatching request immediately consumes
availability budget, before RBG has received the watch event. The request and
acknowledgement persist on the child object, so controller restart or leader
handover does not release the budget. A legacy child without a request retains
the existing readiness fallback; a missing condition cannot release an issued
request. Controller startup enqueues existing RBGs through the informer's initial
list, so condition population does not depend on a future resync. Conditions
remain necessary for independent RBGs and changes not initiated by RBGSet;
request equality alone is not a general readiness guarantee.

### Status

| Field | Meaning in the rolling path |
|-------|-----------------------------|
| `replicas`, `readyReplicas` | Total child capacity and ready capacity, including surge. Terminating children count only toward total capacity. |
| `currentReplicas` | Non-terminating base groups not matching update, including held-back old groups. |
| `updatedReplicas`, `updatedReadyReplicas` | Non-terminating base groups matching update, and their ready subset; surge is excluded. |
| `expectedUpdatedReplicas` | `replicas - partition`, the target batch size; not a cap on updated counters. |
| `currentRevision`, `updateRevision` | Persisted old/full-completion reference and desired template identity. |

On the child RBG, `status.observedRolloutRequest` identifies the last verified
request, not the last request merely read by its controller. Pending requests
exclude the child from `readyReplicas` and `updatedReadyReplicas`, even when its
old `Ready=True` condition still describes serving Pods. RBGSet revision labels
identify the applied template, while this acknowledgement and the child rollout
condition determine when its update has completed.

`Rolling` distinguishes `True/RolloutInProgress`, `False/PartitionComplete`,
and `False/RolloutComplete`. `Ready` independently reflects serving capacity.
Full completion requires `updatedReplicas == readyReplicas == replicas ==
spec.replicas`, after surge has gone; only then is current advanced to update.
A nonzero partition does not prohibit full completion if all base groups already
match, as on initial creation. If old held-back groups remain, current stays readable.

Readiness loss or replacement after a completed rollout does not reopen it for
the same template. A new template, or expanding an unfinished partition rollout
by lowering partition, requires further rollout work. Initial creation with
partition zero reports progress until all desired groups are Ready; a nonzero
partition can report `PartitionComplete` earlier.

**Scale subresource interaction.** `status.replicas` reports total child
capacity, including surge groups. The RBGSet scale subresource exposes
`.status.replicas` as its current replica count, so an autoscaler reading it will
see the surge as extra current replicas. Rolling updates with `maxSurge > 0` are
therefore intended for manual or set-aware scaling; running an HPA against the
RBGSet scale subresource while surge is in flight is not a supported
configuration.

### Test Plan

These are coverage plans, not a claim that every case is already automated.

#### Unit Tests

- **Revision identity**: same normalized template reuses one `ControllerRevision`;
  a changed template yields a new name; the set UID separates same-named sets.
- **Canonical payload**: the stored `data` stays byte-identical when a
  strategic-merge patch re-serializes it with sorted keys, which is what keeps
  orphan cascading working. The fixture must store a payload whose struct field
  order is not already sorted, or the assertion proves nothing.
- **Normalization**: role ordering, legacy update-strategy spelling, set identity
  labels, and controller-owned annotations do not change the revision; user
  template metadata and role replicas do.
- **Comparison**: an unlabeled child can still match the update revision; a
  matching label never masks content drift.
- **Orphan adoption**: a labeled orphan is claimed instead of colliding, the
  ordinals above it are still filled, and a create collision adopts it after the
  uncached re-read; a child controlled by another set UID stays an error and keeps
  its owner.
- **Scale-only update**: both strategies pace role-replica changes under the same
  availability budget; `Recreate` preserves the child UID and updates its revision
  label rather than deleting it. With two ready groups and `maxUnavailable=1`,
  an increase from one to two role replicas updates only the upper ordinal until
  its downstream capacity is ready and its request is acknowledged.
- **Budget resolution**: percentage rounding (surge up, unavailable down),
  negative values rejected, clamping to replicas, `InPlaceUpdate` ignoring stored
  surge, and resolved-zero budgets falling back to `maxUnavailable = 1`.
- **Surge accounting**: sized from the live budget — only serving in-scope groups
  need cover, the ready base count above the availability floor is headroom,
  existing surge is subtracted, and a failure below the partition must not reduce
  the batch to no surge; never bounded by `maxSurge` alone.
- **Child rollout barrier**: a stale-generation or non-`False` condition blocks
  readiness; an issued request also blocks until the matching acknowledgement,
  even when the generation and old conditions are unchanged.
- **Metadata-only budget**: with multiple ordinals and `maxUnavailable=1`, repeated
  reconciles cannot update the next child until the current child acknowledges
  completion; metadata writes preserve UID, owner references, finalizers, and
  generation.
- **Request ownership**: template annotations cannot overwrite the request,
  requests do not affect set revision identity, and repeated writes advance the
  counter without reusing an old acknowledgement.
- **Acknowledgement ordering**: status construction before workload application
  never acknowledges a request; the post-application check requires every role
  to be complete, and both SSA status writers preserve the acknowledgement.
- **Requeue scope**: neither controller polls. An RBG requeues only while a rollout
  request is unacknowledged, not on `RollingUpdateInProgress=True`; an RBGSet
  requeues only after it wrote something, not while children stay unready or its
  ready count still differs from `spec.replicas`.
- **Role hashes**: topology changes and the enabled gang annotation alter
  expected role hashes; unrelated metadata and PodGroup-only settings do not,
  and absent Pod-shaping annotations preserve existing hashes.
- **Controller-owned annotation exclusion**: enumerate hash-excluded keys while
  preserving child-side stashes and propagating template-owned compatibility
  data; PodGroupPolicy snapshots preserve below-partition recovery and distinguish
  parent-managed policy removal from child-only conversion stashes.
- **Conversion**: request annotations and acknowledgements round-trip between
  v1alpha1 and v1alpha2 without loss.
- **Admission**: create/update validation of both strategy modes, mode switches,
  `0%-100%` ceilings, newly enabled role scaling adapters, and grandfathering of
  already-enabled adapters. A partition above `replicas` is admitted on both verbs
  — StatefulSet parity, with the controller clamping — while a malformed or
  above-`100%` partition is still rejected.

#### Reconcile Tests

- Repeated template changes within one rollout window, including a superseded
  surge group being replaced rather than retained.
- Terminating children counted against creation capacity, and foreground deletion
  with UID/resource-version preconditions.
- Availability accounting: ready groups consume the delete/update budget, including
  `Recreate` replicas-only in-place updates; unready outdated groups do not, and a
  budget-blocked candidate does not block repair of lower unready ordinals.
- Recreate replica increase: extend the scale path to withhold readiness of each
  added replica, assert the next child retains its old desired replica count and
  UID, and release it only after downstream capacity and request acknowledgement
  converge; ready RBG capacity stays above the configured floor.
- In-place ordering: a higher ordinal is not released until its downstream
  workload reports completion, for both an environment change that replaces Pods
  and an image-only change that patches them.
- Child rollout barrier: advance only after the current child has a matching
  request acknowledgement and satisfies the current-generation rollout condition.
- Metadata-only topology rollout: stateful and stateless RIS paths keep the lower
  ordinal at its old revision while the upper child's new affinity reaches an
  unready Pod; withhold simulated kubelet readiness before asserting the request
  remains unacknowledged, then complete each child in descending order.
- Bootstrap barrier semantics: a never-requested legacy child retains the
  readiness fallback, but a missing condition cannot bypass an issued request.
- Compatibility propagation: converted v1alpha1 coordination converges through
  real policy SSA on settled and partition-held children without revision churn.
- Scale subresource semantics: `status.replicas` includes surge while a rollout
  is in flight and returns to `spec.replicas` after surge reclamation.
- Status stickiness: readiness loss after completion does not reopen `Rolling`
  for the same template; lowering the partition does.
- Revision retention: current, update, and child-referenced revisions are
  preserved; only unused revisions beyond the history limit are deleted.
- Revision payload immutability: the API server accepts a strategic-merge patch
  against a stored `ControllerRevision` and the patched `data` is byte-identical,
  which is the merge garbage collection relies on to orphan dependents.
- Legacy path propagation: a set without a strategy carries a newly added
  `roleTemplates` entry into its existing children in place, without recreating
  them or adding revision labels.

#### E2E Tests

- **New set with a strategy**: initial children are created at the update revision
  and the set converges to `RolloutComplete`.
- **Legacy opt-in**: adding a strategy to a converged set neither recreates
  children nor changes their UIDs.
- **Recreate rolling update**: groups are replaced one at a time in descending
  ordinal order while ready capacity never drops below `replicas - maxUnavailable`.
- **InPlaceUpdate**: child RBG UIDs are preserved while role-level strategies
  replace or patch the actual Pods, also in descending ordinal order.
- **Partition**: upper ordinals roll first, held-back ordinals stay on the old
  revision, and lowering the partition releases the rest.
- **Deletion below partition**: a removed held-back group is recreated from the
  old template, not the current one.
- **Surge**: surge backs a zero `maxUnavailable`, is released once the in-scope
  batch completes, and is never created when the unavailable budget already
  covers the work. A separate spec covers the opposite case: a group held back
  below the partition has already spent that budget, and the surge for the
  in-scope batch still appears.
- **Role scaling**: extend the `Recreate` scale case to increase each role from
  one to two replicas with two child groups and `maxUnavailable=1`; both strategies
  preserve child UIDs and respect the normal update budget, and the lower child
  waits for the upper child's new replica to become ready and be acknowledged.
- **Metadata-only child rollout**: use native Deployments with two groups and
  `maxUnavailable=1`; add an unavailable topology key to keep the upper Pod
  Pending, assert the lower child and Pod remain untouched for 20 seconds, then
  withdraw the annotation and verify recovery, matching acknowledgement, stable
  UIDs and generations, and `RolloutComplete`.
- **Child rollout barrier**: the next ordinal waits for a matching request
  acknowledgement plus the current-generation rollout condition; keep the existing
  spec-change, partition, and replicas-only regression coverage.
- **Scale subresource / HPA**: automated scaling through the scale subresource
  during surge is not a supported configuration; E2E coverage focuses on manual
  scaling and surge accounting.
- **Admission**: create and update paths validate strategy-specific budgets,
  percentage ceilings, mode switches, and newly enabled role scaling adapters.

Additional kind validation is manual, not additional automated E2E specs:
verify an in-flight request survives controller restart; verify v1alpha1
full-object and status-subresource writes preserve the acknowledgement; verify
gang enable/disable updates PodGroup objects and Pod-template labels before
acknowledgement; verify `--cascade=orphan` deletion of a rolling set completes
without a stuck orphan finalizer, and that recreating the set adopts the
surviving children while still filling the ordinal that was removed; verify a set
whose two children stay Pending forever issues no controller reads while idle, and
still completes a rollout on child events alone once the template becomes
schedulable. A scheduler-plugins CRD without the coscheduling plugin validates
controller propagation only, not actual gang scheduling enforcement. The native
Deployment case requires deprecated workload types enabled. The stateful RIS
`A -> B -> A` early-rollback stall remains a separate known issue; the metadata
budget fix does not claim to resolve it.

## Appendix

- **Restart-safe observed-replicas checkpoint.** The checkpoint is stored as an
  annotation (`rbg.workloads.x-k8s.io/groupset-observed-replicas`) on the RBGSet
  object. It survives controller restarts and distinguishes scale-out from
  replacement below a newly raised partition.
- **Numeric revision field is informational only.** The `revision` field of a
  `ControllerRevision` records the RBGSet generation at creation time; the
  content-derived `ControllerRevision` name is the canonical template identity.
  Reusing a previous revision after `A → B → A` is therefore handled by name
  equality, not by the numeric field.
- **Percentage-partition resolution timing.** `partition` is resolved against the
  current `spec.replicas` at reconcile time. A change to `replicas` re-resolves
  the partition but does not retroactively re-evaluate a batch that has already
  completed.
- **Admission verdict for `maxUnavailable: 0` under `InPlaceUpdate`.** The
  webhook rejects it; `InPlaceUpdate` requires a positive declared
  `maxUnavailable` and the controller enforces a resolved minimum of `1`.
- **Operator-downgrade stance.** Downgrading to a controller that does not
  implement rolling updates leaves `spec.rolloutStrategy` ignored; children
  remain, but template changes revert to legacy ungated updates. The conversion
  webhook preserves the strategy and rollout status fields so an upgrade resumes
  correctly, but not `groupTemplate` labels and annotations, which v1alpha1 cannot
  express. A cluster whose CRD lacks the conversion webhook falls back to
  structural pruning, in which case v1alpha1 writes are not supported.

## Alternatives

- **Content comparison without stored revisions:** Simpler, but once the template
  is changed, no old-version rbg can be reconstructed anymore: every deleted
  or failed child below the partition would be recreated from the new template.
- **Bulk relabeling on opt-in:** Avoids unlabeled children, but adds writes and
  can misidentify heterogeneous legacy children. Prefer content fallback and
  labeling on creation or an actual in-place update.
- **A separate pause switch:** Partition already delivers pause-like control: a
  non-zero partition holds the rollout at a stable boundary without a dedicated
  switch. Introducing `paused` now would add interactions with scaling, recovery,
  and surge retention; it can be revisited as a follow-up if a real need emerges.

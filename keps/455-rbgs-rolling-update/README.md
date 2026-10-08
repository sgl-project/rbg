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
  - [Status](#status)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Reconcile Tests](#reconcile-tests)
    - [E2E Tests](#e2e-tests)
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
| Controllers write metadata onto child RBGs | Exclude explicitly controller-owned keys from revision comparison; do not exempt all project-prefixed annotations.                                                                |
| `InPlaceUpdate` leaves the child Ready while its workloads still run the old revision | RBG publishes `RollingUpdateInProgress` only after directly checking the target role workload's revision, generation, capacity, and readiness; RBGSet requires that barrier to be false before spending another budget slot. |
| Existing children differ when rolling updates are first enabled | Bootstrap one old revision from the lowest existing base ordinal; do not assume every child has that content. Recommend enabling on a converged set before changing its template. |
| A child RBG's spec was edited directly, or predates `roleTemplates` propagation | Overwriting the whole child spec reconciles the drift back to the template; existing drifted children are rewritten once on upgrade. Bounded by the two fields `RoleBasedGroupSpec` has. Record it in the release notes as a behavior change rather than describing the legacy path as unchanged. |
| Older clients read or write the set through v1alpha1 | The conversion webhook keeps round-trips lossless: `spec.rolloutStrategy` and the v1alpha2-only rollout status fields are stashed in `conversion.workloads.x-k8s.io/` annotations on the v1alpha1 view and restored on write, so a full-object write through the deprecated version does not drop the strategy or the revision bookkeeping. The rendered CRD manifests configure this webhook; a cluster whose CRD lacks it falls back to structural pruning, in which case v1alpha1 writes are not supported. |

## Design Details

### API Changes

The strategy is a flat, optional field on the **v1alpha2 RBGSet**, separate from
`groupTemplate.spec.roles[].rolloutStrategy`.

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
| `partition` | `0` | Hold back ordinals in `[0, partition)`; update base ordinals in `[partition, replicas)`. Integer or percentage, rounded down, not greater than replicas. |
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

### Revision Management

Only the rolling path manages RBGSet revisions. Each reconcile ensures an
`apps/v1 ControllerRevision` exists for the current `groupTemplate`; a matching
snapshot is reused rather than created on every reconcile.

- **Identity:** `rbgs-` plus the hexadecimal encoding of the first 24 bytes of
  `SHA-256(set UID + NUL + normalized template JSON)`. The snapshot stores the
  template and is owned by the set. Its numeric `revision` records the set
  generation at creation; the content-derived name is the template identity.
- **Normalization:** Sort roles and role templates by name, normalize legacy
  update-strategy spellings, and exclude set identity/revision labels and
  controller-owned annotations such as `discovery-config-mode`. User template
  metadata and role replica counts remain part of the revision.
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
| Only `roles[].replicas` changes | Under `Recreate`, scale eligible children in place without consuming the recreation budget. Under `InPlaceUpdate`, the change follows the normal in-place budget because every spec change uses the same path. |
| User template labels or annotations change | They participate in the revision. `InPlaceUpdate` syncs them onto the existing child; `Recreate` follows its group-recreation semantics. Controller-owned metadata remains excluded. |
| Set scales out | Missing new ordinals are created at update revision, even below partition. Existing surge children that become base children are retained and follow partition rules. The internal `groupset-observed-replicas` checkpoint distinguishes scale-out from replacement. |
| Set scales down during rollout | Re-evaluate base and surge ranges. Remove excess children under the availability budget; children in the new surge range may remain until the partition batch can safely release them. |
| A base RBG is deleted | Recreate a previously existing ordinal below partition from current revision; otherwise use update revision. Restore capacity without treating recovery alone as a new template rollout. |
| A Pod fails but its RBG still exists | Let the downstream controllers recover it; lack of readiness alone is not a reason for RBGSet to recreate a matching group. |
| Template changes again during rollout | Re-evaluate both base and surge groups against the latest update revision. An outdated, unready surge group must not permanently occupy the only surge slot. |
| Partition is lowered | Resume the previously held-back work, even though update revision is unchanged. Increasing partition does not actively roll back already updated children. |
| Strategy is removed | Return to legacy propagation, without rolling budgets. Removing the strategy is not a pause operation. |
| Strategy switches between `Recreate` and `InPlaceUpdate` | Validate the target mode. Existing out-of-range surge groups are reclaimed under the availability budget; base groups then follow the new update action. |
| Full-object write through v1alpha1 (older tooling, GitOps pinned to the deprecated version) | The conversion webhook stashes the strategy and the v1alpha2-only rollout status fields on the v1alpha1 view and restores them on write, so the set stays on the rolling path and children keep their identities. v1alpha1 cannot express the strategy itself: changing or removing it requires v1alpha2. |

The old-revision recovery rule restores the persisted **current template**, not
an arbitrary last-seen template of each individual child. Controller restarts
must not lose that reference or the observed-replicas checkpoint.

### Availability and Surge

A ready child is not terminating, has observed its current generation, and has
`Ready=True`. For `InPlaceUpdate`, RBG must also report
`RollingUpdateInProgress=False` for that generation; otherwise stale capacity
could let the next ordinal start before the real downstream workload finished.
Ready base and surge groups both contribute to serving capacity.

Both strategies use authoritative API reads and an availability floor of
`max(0, replicas - maxUnavailable)`. Acting on a ready outdated group consumes
budget; an already-unready outdated group does not. Skip a budget-blocked
candidate rather than preventing repair of lower, unready ordinals. `Recreate`
uses foreground deletion with UID/resource-version preconditions and counts
terminating objects toward creation capacity.

`maxSurge` applies only to `Recreate`. Surge occupies ordinals
`[replicas, replicas + maxSurge)` and serves like any other RBG. New surge
creation is limited by remaining in-scope base replacements not covered by
`maxUnavailable`, subtracting already existing surge capacity. For
`replicas=4, maxSurge=5, maxUnavailable=0`, four outdated base groups need at
most four surge groups, not five. No surge is created just because held-back old
groups remain after the partition batch completes.

Once all in-scope base groups match update and are Ready, reclaim surge rather
than retain a permanent canary. Reclamation still respects the availability
floor: a failure below partition may delay safe release, but does not itself
make that held-back group an update candidate. Budgets govern controller actions;
they cannot prevent external failures from reducing availability.

### Status

| Field | Meaning in the rolling path |
|-------|-----------------------------|
| `replicas`, `readyReplicas` | Total child capacity and ready capacity, including surge. Terminating children count only toward total capacity. |
| `currentReplicas` | Non-terminating base groups not matching update, including held-back old groups. |
| `updatedReplicas`, `updatedReadyReplicas` | Non-terminating base groups matching update, and their ready subset; surge is excluded. |
| `expectedUpdatedReplicas` | `replicas - partition`, the target batch size; not a cap on updated counters. |
| `currentRevision`, `updateRevision` | Persisted old/full-completion reference and desired template identity. |

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

### Test Plan

These are coverage plans, not a claim that every case is already automated.

#### Unit Tests

- **Revision identity**: same normalized template reuses one `ControllerRevision`;
  a changed template yields a new name; the set UID separates same-named sets.
- **Normalization**: role ordering, legacy update-strategy spelling, set identity
  labels, and controller-owned annotations do not change the revision; user
  template metadata and role replicas do.
- **Comparison**: an unlabeled child can still match the update revision; a
  matching label never masks content drift.
- **Scale-only update**: under `Recreate`, a `roles[].replicas`-only change is
  updated in place and advances the child revision label instead of recreating
  the group.
- **Budget resolution**: percentage rounding (surge up, unavailable down),
  negative values rejected, clamping to replicas, `InPlaceUpdate` ignoring stored
  surge, and resolved-zero budgets falling back to `maxUnavailable = 1`.
- **Surge accounting**: created surge is bounded by remaining in-scope base
  replacements minus the unavailable budget, never by `maxSurge` alone.
- **Admission**: create/update validation of both strategy modes, mode switches,
  `0%-100%` ceilings, newly enabled role scaling adapters, and grandfathering of
  already-enabled adapters.

#### Reconcile Tests

- Repeated template changes within one rollout window, including a superseded
  surge group being replaced rather than retained.
- Terminating children counted against creation capacity, and foreground deletion
  with UID/resource-version preconditions.
- Availability accounting: ready groups consume the delete/update budget, unready
  outdated groups do not, and a budget-blocked candidate does not block repair of
  lower unready ordinals.
- In-place ordering: a higher ordinal is not released until its downstream
  workload reports completion, for both an environment change that replaces Pods
  and an image-only change that patches them.
- Status stickiness: readiness loss after completion does not reopen `Rolling`
  for the same template; lowering the partition does.
- Revision retention: current, update, and child-referenced revisions are
  preserved; only unused revisions beyond the history limit are deleted.
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
  covers the work.
- **Role scaling**: under `Recreate`, a `roles[].replicas` change scales groups
  in place without group recreation; under `InPlaceUpdate`, it preserves the
  child UID while respecting the normal update budget.
- **Admission**: create and update paths validate strategy-specific budgets,
  percentage ceilings, mode switches, and newly enabled role scaling adapters.

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

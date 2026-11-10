# KEP-502: Workload Status Model

<!-- toc -->
- [KEP-502: Workload Status Model](#kep-502-workload-status-model)
  - [Release Signoff Checklist](#release-signoff-checklist)
  - [Summary](#summary)
  - [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
  - [Proposal](#proposal)
    - [User Stories](#user-stories)
    - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Risks and Mitigations](#risks-and-mitigations)
  - [Design Details](#design-details)
    - [Reason Taxonomy and Aggregation](#reason-taxonomy-and-aggregation)
    - [Consuming the Status](#consuming-the-status)
    - [Validation Ownership and Failure Semantics](#validation-ownership-and-failure-semantics)
    - [RI Phase Transition Principles](#ri-phase-transition-principles)
    - [RoleBasedGroup](#rolebasedgroup)
    - [RoleInstanceSet](#roleinstanceset)
    - [RoleInstance](#roleinstance)
    - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
      - [Unit tests](#unit-tests)
      - [Integration tests](#integration-tests)
      - [e2e tests](#e2e-tests)
    - [Graduation Criteria](#graduation-criteria)
    - [Upgrade / Downgrade Strategy](#upgrade--downgrade-strategy)
    - [Version Skew Strategy](#version-skew-strategy)
  - [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
    - [Feature Enablement and Rollback](#feature-enablement-and-rollback)
    - [Rollout, Upgrade and Rollback Planning](#rollout-upgrade-and-rollback-planning)
    - [Monitoring Requirements](#monitoring-requirements)
    - [Dependencies](#dependencies)
    - [Scalability](#scalability)
    - [Troubleshooting](#troubleshooting)
  - [Drawbacks](#drawbacks)
  - [Alternatives](#alternatives)
<!-- /toc -->

## Release Signoff Checklist

The following checklist applies to releases in the RBG project.

- [ ] The target release, enablement mechanism, and upgrade / rollback strategy are defined.
- [ ] The test plan and graduation criteria are confirmed.
- [ ] The production readiness review is complete and approved.
- [ ] The implementation, generated artifacts, and required compatibility regression tests pass project CI.
- [ ] User documentation, migration guidance, and release notes are complete.

## Summary

This KEP defines a workload status model for RoleBasedGroup (RBG), RoleInstanceSet (RIS), and RoleInstance (RI).
Conditions, stable reasons, and existing counters distinguish progress toward the target, current blockers, resource waits, and workload-level readiness.
RBG and RIS do not add `status.phase`; their workflow is reported through `Progressing.reason` and existing counters. RI adds an optional `status.phase` because its controller already has a meaningful instance-level lifecycle state machine.
The scope is limited to condition fields and values, RI phase, compatibility migration, and contract tests. It does not include controller refactoring or extensions to the role status schema.

## Motivation

The current status model has the following limitations:

- RBG and RIS lack a durable progress condition, requiring users to infer the lifecycle of the group and instance set from counters and child resources. RI lacks a persisted lifecycle phase even though its controller already reconciles from a state machine.
- RBG declares `Progressing` and `RollingUpdateInProgress`, but the built-in controllers on the main branch do not currently write them.
- RBG's existing `Ready` condition summarizes all-replica readiness at the orchestration layer, but that verdict is not well-defined: serving availability depends on consumer policy, and partial RoleInstance availability can be usable. Deployment and StatefulSet define no `Ready` condition for the same reason. Rollout completion needs a separate representation, and readiness needs per-role and per-instance facts.
- RIS / RI `FailedScale` and `FailedUpdate` record individual operation failures, making it easy to confuse transient events with facts that remain true.
- Status does not clearly distinguish current blockers that require a spec, configuration, or environment change from runtime waits that may clear on their own.

### Goals

1. Define the relationship between lifecycle, readiness, progress toward the target, and observation freshness.
2. Define how lower-level conditions and recovery facts are aggregated into RBG and RIS progress, and how operations across roles are summarized.
3. Define condition ownership, reasons, transition timestamps, and compatibility migration rules.
4. Preserve readiness gates, availability timing, and safeguards for restarts and rolling updates.

### Non-Goals

1. Refactoring reconciliation layers, internal interfaces, or status persistence paths.
2. Changing scheduling, dependency-based startup, scaling, rolling updates, restarts, or their gate policies.
3. Redesigning retry, logging, or event mechanisms, or reorganizing recovery fields. Operation failure reporting reuses the existing Event mechanism.
4. Changing the independent Warmup status.
5. Comprehensively redesigning status for RBGSet, ScalingAdapter, or CoordinatedPolicy; only consumer compatibility is assessed.
6. Adding features to deprecated backends or the feature-frozen v1alpha1 API, or automatically migrating workload types.
7. Adding a minimum-available-replica readiness policy or replacing the all-replicas-ready requirement with gang min.
8. Extending the roleStatuses schema or adding role conditions, activities, workloadRef, or similar fields.

## Proposal

Use conditions for configuration compatibility, progress toward the target, and readiness facts, and Events for individual operation failures. RI phase remains the instance-level lifecycle anchor.
RBG summarizes the workflows of its roles. Specific activities are available through reasons, messages, and lower-level status; programs use structured fields and stable reasons rather than parsing messages.

### User Stories

- Operators distinguish creation, updates, scaling, recovery, and waiting through reasons, messages, and lower-level status without treating Running as ready.
- Release systems distinguish observing a target from completing the target revision.
- Troubleshooters identify activity and blockers for a specific role through messages and the corresponding RIS / RI status.
- Readiness gate providers retain control over existing behavior, and the aggregation layer does not overwrite their conditions.

### Notes/Constraints/Caveats

Deployment, StatefulSet, and LeaderWorkerSet are deprecated backends whose status capabilities will not be extended.
RBG enables the new condition model only when all roles use RIS. If any role uses a legacy backend, RBG continues to use the existing `Ready` condition and role replica status during the compatibility window.
For example, an RBG with prefill using RIS and decode using LeaderWorkerSet does not report the new overall conditions, but the prefill RIS / RI resources still use the new status model.
Missing new fields on legacy backends do not affect existing Ready checks for existing objects.

The `role-workload-type` annotation provides compatibility with legacy workload types in v1alpha1. It will be retired as part of the v1alpha1 deprecation process and is not a long-term v1alpha2 capability.

Status contains fields consumed by control logic and must not be treated as presentation-only data. Changes to readiness reporting or transition timestamps can affect release waits, dependency-based startup,
rollouts, scaling, and `minReadySeconds`. Recovery fields must not be discarded on the assumption that they can be recomputed.
RI phase is not set through ordinary spec updates; the status subresource remains subject to RBAC.

### Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| Confusing lifecycle, service readiness, and revision convergence | Define field semantics separately and cover partition, pause, and zero-replica cases |
| New checks leave deprecated backends permanently not Ready | Preserve legacy semantics during migration and run compatibility regression tests with actual backend status |
| Readiness changes bypass existing gates or alter timing | Validate against stateful / stateless consumer baselines |
| Overwriting external conditions or causing timestamp churn | Define ownership, unique keys, and transition rules; test concurrent writes |
| Losing information during schema or legacy condition migration | Validate legacy writes, conversion, failure Event coverage, current blocker reporting, and recovery data preservation |
| Warning Events exhaust an object's event budget or are suppressed by the client-go spam filter | Keep failure Events edge-triggered and aggregated, key them by object and event type, validate repeated-failure volume, and keep current blockers in conditions rather than Events |

## Design Details

The following sections describe status API changes by CR type. Example annotations labeled "New", "Changed", "Retained", and "Proposed deprecation" are relative to the current API; "TBD" indicates an unresolved decision.
The YAML examples show only status-related fragments, omitting spec and unchanged fields, and cannot be applied directly.
New fields target v1alpha2. Compatibility requirements for older APIs are described in the upgrade strategy.

Status fields across all three CR types follow these common rules:

- Status describes observed facts. Successfully issuing an API request does not mean that lower-level work has completed.
- Conditions describe facts that currently hold. FailedScale / FailedUpdate are deprecated; individual operation attempts are reported as Warning Events on the corresponding RIS / RI. Persistent current blockers are represented by the owning layer's `Progressing` reason and message, not by replacement failure conditions. Events are diagnostic history and must not be used to infer current status.
- RI phase records the lifecycle stage the instance has entered. It does not replace conditions, readiness gates, or recovery fields, and does not copy a parent or child phase.
- Conditions use type as the unique key, with an explicit owner for each type. The aggregation layer does not overwrite externally owned conditions, reasons, or messages. Reasons are scoped to the object that owns the condition; parents aggregate reason families rather than copying child reasons verbatim.
- Controller-written reasons must be valid, non-empty, and consistent with status. New, unrecognized reasons must not cause client parsing failures.
- `lastTransitionTime` changes only when condition.status changes. Changes only to reason, message, or observedGeneration do not reset it.
- Transitions compare the latest persisted status with the final status being submitted, not intermediate values within a reconciliation. For example, False → temporary True → final False is not a transition.
- `status.observedGeneration` identifies the generation of this object that has been processed; a condition's observedGeneration identifies the generation of this object to which its fact applies. Neither directly proves target completion.
- RI phase transitions and progress decisions use valid observations corresponding to the current operation target. Parent and child generations cannot be compared directly, and old status cannot prove completion of a new target.
- When observations are insufficient, affected conditions whose facts cannot be determined use Unknown with reason `ObservationIncomplete`. Controller-owned readiness conditions such as `RoleInstanceReady` are the exception: after the first readiness observation, they retain their last known value until a fresh observation succeeds, so upper-level ready counts do not flap. A read failure does not mean zero replicas, and an absent RI phase does not mean Running.
- When children have not yet received the current generation's target, the parent must still record that it has observed the new generation: write `status.observedGeneration=N` together with the `Progressing` value derived from the validation, pause, and progress rules before or while dispatching that target to children. Incomplete work writes `Progressing=True`; a confirmed compatibility failure writes `Progressing=False` with the matching incompatibility reason; a full pause writes `Progressing=False(reason=Paused)`. Preflight or compatibility failures must not skip this status write; they are represented through the corresponding conditions.
- A condition-only change is a status change. If condition status, reason, message, or observedGeneration changes, the controller must write status even when counters and revision fields are unchanged.
- Status must eventually update after changes only to label / revision metadata or after dependencies recover, without requiring an additional manual spec change.
- Do not add a finalizer solely to display RI phase, and do not guarantee that clients observe every intermediate phase.

### Reason Taxonomy and Aggregation

RBG, RIS, and RI do not share one flat reason enumeration. They share reason families so parents can aggregate child facts without parsing messages. Reason strings remain scoped to the object that owns the condition.

External blocker families use stable, machine-readable categories. RI may report the operation affected by the blocker, while RIS and RBG map those facts to their canonical reasons:

| Reason family | RI example reason | RIS reason | RBG reason |
| --- | --- | --- | --- |
| Configuration | Not owned by RI | Not newly defined at RIS | `InvalidSpec`, `IncompatibleGangConfig`, `SchedulerUnsupported` |
| Permission | `PodCreateForbidden`, `PodUpdateForbidden` | `PermissionDenied` | `PermissionDenied` |
| Admission | `PodAdmissionRejected` | `AdmissionDenied` | `AdmissionDenied` |
| Quota | `PodCreateQuotaExceeded`, `PodUpdateQuotaExceeded` | `WaitingForQuota` | `WaitingForQuota` |
| Resources | `InsufficientResources` | `WaitingForResources` | `WaitingForResources` |
| Dependencies | Not owned by RI | Not owned by RIS | `WaitingForDependencies` |
| Unclassified | `PodOperationUnclassified` | `UnclassifiedBlocker` | `UnclassifiedBlocker` |
| Gang scheduling | Not owned by RI | Not owned by RIS | `WaitingForGang` |

Not every RBG reason comes from RIS. Each fact has one lowest layer that can observe it, and only facts observed below RBG are propagated upward:

| RBG reason | Lowest observer | Path to RBG |
| --- | --- | --- |
| `Creating`, `RolesNotReady` | RBG | Computed from required-child existence and `roleStatuses` |
| `InvalidSpec`, `IncompatibleGangConfig`, `SchedulerUnsupported` | RBG | Computed from the expanded target, role combination, and scheduler capability probes |
| `WaitingForDependencies` | RBG | Role dependencies are declared in the RBG spec and cross roles; RIS cannot observe them |
| `WaitingForGang` | RBG | Gang scheduling facts are observed with the gang state owned at RBG scope (today the `GangConfigured` condition) |
| `RoleInstanceSetUpdated`, `Recovering` | RIS | RIS `Progressing` reason, aggregated by family |
| `PermissionDenied`, `AdmissionDenied`, `WaitingForQuota`, `WaitingForResources`, `UnclassifiedBlocker` | RI (or RIS for its own CR operations) | RI `Progressing` reason, mapped to a family by RIS, then aggregated by RBG |

RBG does not probe Pod-level errors itself, and RIS does not report RBG-owned facts such as dependencies, gang configuration, or configuration compatibility. This bound is what keeps top-level reasons at family granularity instead of mirroring lower-level detail.

Blocker classification is deterministic and is performed by the layer that observed the error, normally the RI controller. Kubernetes commonly returns 403 for quota, RBAC, and admission denials, so classification must use error details rather than the status code alone: quota errors identify the exceeded quota object; RBAC denials identify the missing verb, resource, or subresource; admission denials identify the rejecting admission plugin or webhook. The implementation must document this mapping and tests must cover representative errors for each family.

If an error cannot be classified, for example 5xx responses, timeouts, or `AlreadyExists` while a same-name Pod from a previous RI UID is still being garbage-collected, the controller must not guess a blocker family. It reports the unclassified blocker family: RI uses `PodOperationUnclassified`, and RIS / RBG use `UnclassifiedBlocker`, with the concrete error in message and a Warning Event. When a blocker family is already confirmed for the current target, an unclassifiable error does not replace it: the confirmed blocker remains until a fresh successful observation confirms it no longer holds, and the new error is recorded in message and a Warning Event. The unclassified reason is replaced by a specific family after a fresh observation confirms one, or cleared when that observation succeeds; when the condition clears, classification is discarded with it.

Activity and child-readiness reasons are layer-specific:

| Fact | RI reason | RIS reason | RBG reason |
| --- | --- | --- | --- |
| Child readiness | `PodsNotReady` | `InstancesNotReady` | `RolesNotReady` |
| Initial creation | `Creating` | `Creating` | `Creating` |
| Update | `Updating` | `RollingUpdate` | `RoleInstanceSetUpdated` |
| Instance-count scaling | Not owned by RI | `ScalingUp`, `ScalingDown` | `RoleInstanceSetUpdated` |
| Recovery | `Recovering` | `Recovering` | `Recovering` |

`RoleInstanceSetUpdated` is an RBG reporting aggregate modeled on the Deployment `ReplicaSetUpdated` reason: it states that the child RoleInstanceSets are converging, without distinguishing whether a role is updating or scaling, because those workflows are controlled below RBG and may run concurrently. RIS reports the concrete workflow, and RI does not own instance-count scaling.

Parent controllers must aggregate through the documented reason-family mapping and must not parse condition messages. When multiple facts belong to the same family, the parent uses its canonical reason for that family and describes the details in message. For a `Progressing=True` condition, current external blockers take precedence, then an active workflow, then child readiness only when no active workflow remains:

```text
Configuration
Permission
Admission
Quota
Resources
Gang
Dependencies
Unclassified
Activity
ChildReadiness
```

A current external blocker outranks an activity reason because it explains why the current target is not converging. Configuration-family blockers rank first because they require a change to the spec, referenced configuration, or environment. Unclassified ranks below the specific blocker families because a confirmed family is more actionable, and an unclassifiable error never replaces a confirmed family for the same target. Child readiness is not an external blocker: during creation, update, scaling, or recovery it is subsumed by the active workflow reason and is reported independently by readiness facts such as `RoleInstanceReady` and replica counters. If no active workflow remains but the target is not ready, child readiness may be the `Progressing` reason. This precedence affects reporting only, not controller execution order.

Within the Activity family, RBG uses this deterministic precedence:

```text
Creating
Recovering
RoleInstanceSetUpdated
```

When multiple Activity facts are present, RBG uses the highest-precedence activity reason and describes all activities in message. `Creating` is used only during initial RBG establishment, before the target's required roles have been established. Once the RBG is established, a missing child is not `Creating`: it is classified under the workflow that requires it, such as `Recovering` for replacing an established child or `RoleInstanceSetUpdated` for a target that adds one. `Recovering` is used when an established child is being repaired or replaced; that fact is more specific than ordinary target changes. `RoleInstanceSetUpdated` is the aggregate reason for normal target changes in established children.

RIS maps the same Activity ordering to its concrete reasons: `Creating`, `Recovering`, `RollingUpdate`, and `ScalingUp` / `ScalingDown`. This ordering selects the `Progressing` condition reason only; RIS workflow state remains expressed by counters and conditions, not by a persisted lifecycle phase.

`RolesNotReady` and the legacy `RoleNotReady` are distinct, case- and plural-sensitive machine-readable strings. `RolesNotReady` is the RBG child-readiness reason for `Progressing`; `RoleNotReady` belongs only to the deprecated RBG `Ready` condition retained during the compatibility window. Clients must match the exact string and must not normalize singular and plural forms.

### Consuming the Status

Automation should use a small, stable decision table:

| Consumer question | Answer |
| --- | --- |
| Is it serving? | RBG and RIS define no `Ready` condition. Evaluate serving readiness from the facts required by consumer policy: per-role `roleStatuses`, RIS `readyReplicas` / `availableReplicas` versus targets, and RI `RoleInstanceReady` plus configured gates |
| Is the change done? | `Progressing=False`, `reason=Converged`, `status.observedGeneration == metadata.generation`, and the `Progressing` condition's own `observedGeneration == metadata.generation` |
| Does it need attention? | `Progressing=True` with a reason in the configuration-compatibility, permission, admission, quota, resources, gang, dependencies, or unclassified families |
| Unknown / unrecognized reason | Treat as still progressing, not as failure |

The condition-level `observedGeneration` check is required for completion: a legacy or rolled-back controller can advance `status.observedGeneration` while leaving a `Converged` condition written for an older generation. A missing or stale condition `observedGeneration` must be treated as incomplete, not done.
`Progressing=True` means the target has not converged; it does not imply a healthy rate of progress. kstatus-based tools do not understand these condition types by default, so deployments can provide tool-specific health checks: Flux 2.5+ health checks can use CEL expressions over the fields above, and Argo CD can use a Lua health check implementing the same rules.

### Validation Ownership and Failure Semantics

RBG does not define an `Accepted` condition. Static errors belong to admission, and runtime incompatibility is a current blocker reported through `Progressing`:

- The webhook must reject create or update requests for invalid names, rollout parameters, dependency cycles, unsupported workload declarations, and other local spec errors before the target is persisted. If an invalid object is already persisted by a legacy writer or is discovered through defense-in-depth checks, the controller reports `Progressing=True` with `InvalidSpec`; admission rejection remains the preferred path.
- Runtime incompatibility is also a current blocker, not a separate acceptance verdict. A required referenced configuration confirmed missing, an installed scheduler lacking a required capability, or an unsatisfiable gang combination reports `Progressing=True` with `InvalidSpec`, `SchedulerUnsupported`, or `IncompatibleGangConfig`, describes the incompatible fact in message, and emits a Warning Event. A check that cannot be completed reports `Unknown(reason=ObservationIncomplete)`, not a confirmed incompatibility.

Configuration-family blockers rank first in the blocker precedence because a human must change the spec, referenced configuration, or environment. Like other blockers, they are cleared only by a fresh observation confirming the incompatibility no longer holds — for example after the scheduler is upgraded or the referenced configuration is restored — and they may appear or disappear without a new RBG generation.

Quota, execution permissions, external admission decisions, dependencies, and runtime readiness remain execution blockers with their own reasons. There is no `Failed` phase. RBG and RIS do not add phase, and RI phase is not an acceptance or success verdict; an incompatible target does not by itself change it.

### RI Phase Transition Principles

RI phase transitions start from the last persisted phase and occur only when the corresponding transition conditions are met. Phase is not reselected from current Ready, replica counts, or child phases; the same phase, target, and observations must produce consistent transition results.
RI Running means the instance-level target is steady; it is not a rollup of Pod readiness and does not imply that every readiness gate is currently True.
Child readiness is a transition condition, not an invariant required to remain Running. Conditions continuously report current readiness, progress, and blockers.

- Initial creation transitions to Running once the target structure is established and the effective readiness policy is satisfied. RI retains readyPolicy (AllPodReady by default) and configured readiness gates; gates gate externally observed readiness and parent availability, but phase completion uses controller-observed workload readiness only.
- Recreating missing resources after the RI has entered normal operation does not trigger a return to Creating; a replacement Pod goes through creation independently while the RI remains in its current phase unless an update or independent recovery is confirmed.
- Recovery completes when the target Pods are all present, the relevant recreation / replacement / cleanup work is complete, and readyPolicy is satisfied; external readiness gates do not block the phase transition.
- When observations are insufficient to confirm a transition condition, retain the last phase; if it was absent, leave it absent. Related conditions may still change. A confirmed deletionTimestamp on the RI allows it to enter Terminating without complete observations of child resources.
- When multiple workflows remain incomplete, select the RI phase using the display priority defined in the RoleInstance section. Display priority does not determine execution order.
- When a target changes or is withdrawn, evaluate remaining work against the current target, accounting for operations already issued and required cleanup. Target changes during initial creation do not waive structural completeness or effective readiness requirements; pausing does not mean completion. Such changes fold into Create convergence: `Creating` does not transition directly to `Updating` or `Recovering`, consistent with the KEP-527 hook rules.

### RoleBasedGroup

RBG does not add `status.phase`. It reports progress and current blockers through a single `Progressing` condition, with `Progressing.reason` as the single-word summary. RBG defines no `Ready` or `Accepted` condition: it is an orchestration resource, and neither a single service-ready verdict nor a separate acceptance verdict is well-defined at this layer.
In the example below, all roles use RIS. Prefill is scaling up and decode is updating; decode has a target of 3 replicas, of which 2 are ready.

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inference
  generation: 5
status:
  observedGeneration: 5
  conditions:
    - type: Progressing # [Changed] The type is already declared; define writes and execution, waiting, and pause semantics.
      status: "True"
      reason: RoleInstanceSetUpdated # [Changed] Mirrors Deployment ReplicaSetUpdated: established RIS are converging, covering updates and scaling.
      message: "prefill is Scaling; decode is Updating, with 1/3 replicas updated"
      observedGeneration: 5
      lastTransitionTime: "2026-10-26T09:01:00Z"
```

RBG does not persist a lifecycle phase. `Progressing.reason` summarizes the current workflow; `message` describes the concurrent activities; `roleStatuses` and lower-level status carry details. This avoids a second single-valued summary that would need its own transition, initialization, and rollback rules. A printer column on `Progressing.reason` can still provide a one-word `kubectl` summary. Deletion remains indicated by `metadata.deletionTimestamp`, not by a new phase.

The proposed semantics of RBG conditions are:

| Condition | True | False | Unknown |
| --- | --- | --- | --- |
| `Progressing` | The target is incomplete, with work executing or waiting for execution prerequisites | Converged, all relevant progress paused, or configuration incompatible | Progress facts cannot be determined |

Configuration-family blockers validate the expanded target, referenced configuration actually in use (such as CoordinatedPolicy), and required environmental capabilities (such as Volcano gang support) against current observations. An absent optional configuration reference does not cause a blocker. A required reference confirmed missing reports `Progressing=True(reason=InvalidSpec)`; confirmed missing capabilities use `SchedulerUnsupported`; an unsatisfiable gang combination uses `IncompatibleGangConfig`. A read or validation that cannot be completed reports `Unknown(reason=ObservationIncomplete)`, not a confirmed incompatibility. Details are described in message, and a Warning Event records the discovery.
Configuration-family blockers may appear or disappear without a new RBG generation, and they do not rewrite readiness facts or trigger a phase transition. GPU capacity, quota, permissions to execute operations, external admission decisions, dependencies, and runtime readiness use their own blocker reasons.

Progressing covers execution and waiting during creation, updates, scaling, and recovery, without requiring a generation change. True does not guarantee that progress has been made or that completion will be timely. Pausing only updates must not set it to False while other workflows are still progressing.
Current blockers caused by configuration, resources, quota, execution permissions, external admission policies, or dependencies are expressed through Progressing reason / message. Clear stale waiting reasons after recovery; do not infer current blockers from historical errors or Events.

RBG does not define or continue writing a `Ready` condition. RBG and RIS are orchestration resources: whether the workload can serve traffic depends on consumer policy — all replicas, gang minimum, a routing threshold, or partial availability — so a single boolean cannot represent it. Deployment and StatefulSet likewise expose readiness through counters rather than a `Ready` condition. Readiness facts remain `status.roleStatuses` per role, RIS counters, and RI `RoleInstanceReady` plus configured gates.
The existing RBG `Ready` condition is deprecated. During the compatibility window the controller may continue writing the legacy all-replicas-ready value for existing consumers; new consumers must migrate to the readiness facts above. After the window, the controller stops writing it and cleans up controller-owned entries. Legacy-backend RBGs keep the existing condition and role replica status while those backends are supported.

RBG completion is computed from the RBG's own current target and generation; it never derives completion by copying a child's phase or `Progressing=False(reason=Converged)`. CoordinatedPolicy may lower a role's RIS partition or coordinated scaling targets one step at a time, so a single RBG rollout can make each RIS converge between steps and report `Progressing=False(reason=Converged)` several times. During such rollouts RBG must remain `Progressing=True` until every coordinated step required by the RBG-level target completes.

Each condition has one reason. Overall RBG progress in established children uses `RoleInstanceSetUpdated`, with message describing each role's activity. Creation, recovery, and waiting use the corresponding reasons below. A current blocker takes precedence over an activity reason.

| Condition / Status | Reasons |
| --- | --- |
| Progressing=True | `Creating`, `RoleInstanceSetUpdated`, `Recovering` |
| Progressing=True, blocked or waiting | `InvalidSpec`, `IncompatibleGangConfig`, `SchedulerUnsupported`, `PermissionDenied`, `AdmissionDenied`, `WaitingForQuota`, `WaitingForResources`, `WaitingForGang`, `WaitingForDependencies`, `UnclassifiedBlocker`, `RolesNotReady` |
| Progressing=False | `Converged`, `Paused` |
| Unknown | `ObservationIncomplete` |

`status.roleStatuses[]` retains existing replica information.

### RoleInstanceSet

RIS does not add `status.phase` or a `Ready` condition. It adds a `Progressing` condition and continues to expose workflow progress through its existing replica and revision counters. The new condition's `observedGeneration` refers to the RIS itself.

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleInstanceSet
metadata:
  name: inference-decode
  generation: 8
status:
  conditions:
    - type: Progressing # [New type] Progress toward the target, whether executing or waiting.
      status: "True" # [Retained values] Continue using True / False / Unknown.
      reason: RollingUpdate # [Changed] Non-empty, stable, and consistent with status; do not turn a single error into a lasting failure state.
      message: "1/3 instances updated"
      lastTransitionTime: "2026-10-26T09:01:00Z" # [Rule clarified] Changes only when the final persisted status changes.
      observedGeneration: 8 # [New] Absent from current RIS conditions; when used, refers to the RIS itself.
```

RIS workflow progress is reported by `Progressing.reason`, not by a persisted lifecycle phase. Deletion remains indicated by `metadata.deletionTimestamp`. The existing counters and revision fields remain the primary readiness and convergence facts:
`replicas`, `readyReplicas`, `availableReplicas`, `currentReplicas`, `updatedReplicas`, `updatedReadyReplicas`, `expectedUpdatedReplicas`, `currentRevision`, `updateRevision`, `labelSelector`, `collisionCount`, and `rolloutReplacementState`. This mirrors StatefulSet-style status and avoids duplicating counters in a `Ready` condition.

`Progressing` follows RBG rules for progress, pauses, blockers, and insufficient observations, scoped to that instance set. Activity reasons are `Creating`, `ScalingUp`, `ScalingDown`, `RollingUpdate`, and `Recovering`; current blockers use the RIS reasons in the reason-family table, and child readiness uses `InstancesNotReady`.
RIS propagates lower-level blockers by reading the RI `Progressing` condition and mapping RI reason families to these canonical reasons without parsing RI messages. Today's RIS code reads only `RoleInstanceReady`, so this aggregation is a required implementation change for blocker propagation. RIS reports its own operation errors, such as failures creating or updating RI objects, directly as blockers (normally `UnclassifiedBlocker`). Gang waiting and dependencies are not RIS facts and are reported by RBG.

Activity precedence for RIS `Progressing` is `Creating`, `Recovering`, `RollingUpdate`, and `ScalingUp` / `ScalingDown`. `Creating` is used only during initial set establishment, before the RIS has an established revision or established RIs for its target. Once the set is established, a required RI that does not yet exist is not `Creating`: it is classified under the workflow that requires it — `ScalingUp` when scaling adds instances, `RollingUpdate` when update work creates or replaces instances, and `Recovering` when an established RI is being repaired or replaced. Recovery outranks active update and scaling, an active update outranks scaling, and if only scaling remains active, use `ScalingUp` or `ScalingDown`.

Concurrent update and scaling is represented by one `Progressing` reason; message describes the other workflow for humans and is not a machine interface. This is an accepted v1alpha2 trade-off to avoid a multi-workflow status schema. RBG reports the aggregate `RoleInstanceSetUpdated` for both workflows; a future structured multi-workflow representation requires a separate proposal.

Completion conditions are listed below. `Progressing=False(reason=Converged)` is written only after the relevant workflows complete:

| Workflow | Completion Conditions |
| --- | --- |
| ScalingUp | All instances required by the current target are present; newly added instances have completed startup and satisfy effective readiness conditions |
| ScalingDown | Instances selected for deletion by the current target are gone, with no outstanding scale-down work; readiness of all remaining instances is not additionally required |
| Updating | Instances that the current policy requires updating have completed their updates and satisfy the corresponding readiness conditions; replacement and surge cleanup for this update are complete |

Completion checks retain readiness gate and `minReadySeconds` rules rather than using revision equality as a substitute. Partition may retain older-revision instances; completion is evaluated over the update scope required by the current policy. Readiness itself remains expressed by the existing counters and lower-level RI conditions rather than by a RIS `Ready` condition, mirroring StatefulSet.

The two RIS modes currently differ in how they count an RI as ready: stateless mode requires both `RoleInstanceReady` and every `spec.readinessGates` condition to be True, while stateful mode checks only `RoleInstanceReady`. This KEP documents the difference instead of silently unifying it. Any future unification must preserve external gate semantics and include compatibility tests for `minReadySeconds` and availability calculations in both modes.

Conditions retain the existing Go type for now, adding optional `observedGeneration`. Its omission by a legacy writer must not be treated as confirmation of the current generation.
> This proposal retains existing required-field constraints and the condition list schema; future changes must validate compatibility with legacy writes and SSA.

### RoleInstance

RI adds optional `status.phase` while retaining existing readiness condition names. The instance-level phase is retained because RI reconciliation is already stateful: restart-policy recreation, in-place update, and readiness recovery use persisted facts and conditions to decide the next step. Parent controllers aggregate conditions and counters; they do not copy RI phase.
In the example, the RI has completed startup and enters Recovering while a required Pod recreation is quota-blocked and a custom readiness gate is unsatisfied.

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleInstance
metadata:
  name: inference-decode-0
  generation: 2
status:
  phase: Recovering # [New] Optional lifecycle field; recovery is independent of RoleInstanceReady.
  observedGeneration: 2 # [Retained, semantics clarified] Refers to the RI generation, not the parent RIS generation.
  conditions: # [Changed] Standardize ownership, reasons, and transition rules; retain the custom type for now, with future schema changes marked TBD.
    - type: Progressing # [New type] Current progress or current blocker for this RI; not a readiness gate.
      status: "True"
      reason: PodCreateQuotaExceeded # [New] Operation-specific RI reason; RIS maps this family to WaitingForQuota.
      message: "Recreating a required Pod is blocked by resource quota"
      lastTransitionTime: "2026-10-26T09:01:00Z" # [Rule clarified] Changes only when status changes.
      observedGeneration: 2 # [New] Refers to the RI generation.
    - type: RoleInstanceReady
      status: "False"
      reason: PodsNotReady
      message: "A required Pod is being recreated and is blocked by resource quota"
      lastTransitionTime: "2026-10-26T09:01:00Z" # [Rule clarified] Changes only when status changes, preserving availability timing.
      observedGeneration: 2 # [New] Absent from current RI conditions; when used, refers to the RI itself.
    - type: RoleInstanceCustomReady
      status: "False"
      reason: ExternalCheckPending
      message: "External check has not yet passed"
      lastTransitionTime: "2026-10-26T09:01:00Z"
      observedGeneration: 2
```

The proposed RI phase values and boundaries are:

| Phase | Meaning |
| --- | --- |
| `Creating` | Initially establishing the target Pods for each component, waiting for readyPolicy to be satisfied; external readiness gates do not gate this phase |
| `Running` | The RI target is steady after startup, an update, or recovery completes; readiness may still be False and a readiness drop alone does not change the phase |
| `Updating` | An identified component spec, template, or revision update is in progress, including in-place updates and Pod replacements required by the update |
| `Recovering` | Independent recreation or replacement of required Pods is in progress outside an update, until recreation, cleanup, and controller-observed readiness complete |
| `Terminating` | The RI itself has a deletionTimestamp, rather than a child Pod being deleted |

RI phase is not the lifecycle-hook protocol. Hook execution, retries, and release use the hook-state labels and finalizer handshake defined by KEP-527. Phase describes the instance lifecycle for observation and pacing and serves as an eligibility input: an RI that is still `Creating` has never established its target, so target changes during Create convergence do not invoke `preUpdate` or `preRecover`. Phase must not be used as a hook trigger, handshake, or completion signal.

Deletion of the RI takes priority and enters Terminating. Identifying an update target enters Updating; an empty revision during initial creation is not evidence of an update.
Confirming independent recovery of required Pods outside an update enters Recovering. Pod recreation that is already part of an update remains Updating rather than entering a separate Recovering phase.
Scaling the number of RIs belongs to RIS; component spec changes within an RI are expressed as Updating.

To leave Updating, the RI must have all components / Pods required by the current target present, the target configuration must have taken effect, in-place updates or replacement must have completed, and the controller-observed readiness conditions for this update must be satisfied. Revision equality alone does not prove completion.
To leave Recovering, all target Pods must be present, the relevant recreation / replacement / cleanup work must be complete, and readyPolicy must be satisfied. The RI then returns to Running.

Phase completion is not externally observed readiness. RI phase transitions use controller-observed workload readiness: required Pods present and ready per readyPolicy, plus the in-place update readiness gate when it is configured. `RoleInstanceCustomReady` and other externally owned readiness gates do not gate phase transitions; they gate the externally observed Ready state and parent availability calculations. This keeps phase distinct from `RoleInstanceReady` plus gate aggregation and removes the circular wait with KEP-527: the lifecycle-owned `RoleInstanceCustomReady` contribution established by `markPodNotReady` remains until the RI enters `Running`, but it does not prevent the RI from entering `Running`.

The allowed phase edges are:

| From | Event | To |
| --- | --- | --- |
| absent | RI creation begins | `Creating` |
| `Creating` | The current creation target is structurally complete and its controller-observed readiness requirements are satisfied | `Running` |
| `Creating` | An update or recovery target is identified before initial creation completes | Remain `Creating`; fold the new target into Create convergence without invoking hooks |
| `Creating` | RI deletion is confirmed | `Terminating` |
| `Running` | An update target is identified | `Updating` |
| `Running` | Independent recreation or replacement of required Pods is confirmed | `Recovering` |
| `Running` | RI deletion is confirmed | `Terminating` |
| `Updating` | The update target, its replacement work, and controller-observed readiness requirements complete | `Running` |
| `Updating` | RI deletion is confirmed | `Terminating` |
| `Recovering` | Recovery and its controller-observed readiness requirements complete | `Running` |
| `Recovering` | An update target is identified while recovery continues | `Updating` |
| `Recovering` | RI deletion is confirmed | `Terminating` |

No phase returns to `Creating`, and `Creating` exits only to `Running` or `Terminating`: recreating an established RI's Pods is recovery, and an update or recovery target identified before initial creation completes is folded into Create convergence. This matches KEP-527: `preUpdate` and `preRecover` apply to an established instance, so replacing members that have not yet reached workload Ready during a first deploy does not invoke them. `Creating` still describes first convergence toward the latest target, including replacement of partially created resources. `Updating` is the Activity reason only when no current blocker or incomplete observation takes precedence; for example, a quota-blocked Pod creation keeps `Progressing=True(reason=PodCreateQuotaExceeded)` while `phase=Updating`. Message notes that the target changed during initial creation. Readiness gates only `Creating → Running` and completion decisions, but a readiness drop alone never triggers a phase transition. Deletion wins from every phase.

| Situation | Phase Handling |
| --- | --- |
| An update remains incomplete, and target Pods are missing or being recreated | Remain Updating until the current update target and its recovery work are complete |
| An independent failure and recreation occur after an update completes | Enter Recovering; return to Running only after recovery and its controller-observed readiness conditions complete |
| A readiness drop occurs without confirmed resource recreation or replacement | Remain Running; readiness conditions report the current facts |
| The target changes during initial creation, for example fixing a bad image before first readiness | Remain Creating and converge toward the latest target; do not invoke preUpdate or preRecover |
| Observations are insufficient to confirm update or recovery completion | Retain the phase and handle affected conditions as having insufficient observations |

`inPlaceUpdateContainerBaselines` may remain after an in-place update completes to identify expected container restarts. A non-empty field does not mean the RI is still Updating.

RI readiness and restart safeguards remain expressed by the following conditions; phase changes do not directly rewrite them:

| RI Condition | Ownership and Compatibility Requirements |
| --- | --- |
| `Progressing` | Current target progress or current blocker for this RI; owned by the RI controller and not used as a readiness gate |
| `RoleInstanceReady` | Controller-owned aggregate of Pod readiness and the configured in-place update readiness gate; external gates are not folded into it |
| `RoleInstanceAllPodsReady` | Pod readiness facts provided by the RI controller, preserving readyPolicy semantics |
| `RoleInstanceInPlaceUpdateReady` | Maintained by writers involved in in-place updates, preserving existing gate behavior |
| `RoleInstanceCustomReady` and external gates | Maintained by the corresponding owners, preserving their conditions, reasons, and messages |
| `Restarting` | Restart safeguard maintained by the RI controller; continues to suppress duplicate restart-policy recreation and does not automatically become a readiness gate |

RI `Progressing` uses operation-specific reasons such as `PodCreateQuotaExceeded`, `PodCreateForbidden`, `PodAdmissionRejected`, `InsufficientResources`, `Creating`, `Updating`, and `Recovering`. A blocker reason is cleared only after a fresh observation of the current target succeeds or confirms that the blocker no longer holds; it must not be inferred from Event history. `RoleInstanceReady` reasons are Ready, PodsNotReady, InplaceUpdateNotReady, and ReadinessGatesNotReady when a configured gate condition is missing. `RoleInstanceCustomReady` and other externally owned readiness gates remain separate conditions and do not change `RoleInstanceReady`.
Multiple blocking gates require a stable priority. If blocking recurs after recovery, a stale `False / Ready` combination must not remain.
Preserve restartCount, lastRestartTime, inPlaceUpdateContainerBaselines, and their safety-critical persistence ordering.

RI conditions retain the custom Go type for now, adding optional observedGeneration. Its absence does not confirm the current generation, and the aggregation layer does not fill in confirmation on behalf of external owners.
This proposal retains existing required-field constraints and the condition list schema.

<<[UNRESOLVED RI condition schema]>>
Whether to adopt metav1.Condition or change required fields, listType=map / listMapKey=type in the future remains unresolved. Compatibility with legacy data, external writers, and SSA must be validated.
<<[/UNRESOLVED]>>

### Test Plan

New-feature tests cover RIS / RI and RBGs whose roles all use RIS. Legacy backends receive regression coverage only while supported during migration;
after support is removed, tests verify rejection and migration guidance instead. The following is a test plan; there are no execution records or coverage measurements for the new model yet.

#### Prerequisite testing updates

- Record main-branch behavior baselines for readiness gates, readyPolicy, minReadySeconds, restarts, and existing backend status.
- Preserve genuinely absent fields and zero values in fixtures; for example, tests must not fill in observedGeneration when LWS does not publish it.
- Validate schemas, conversion, and external condition writes against a real API Server, not only a fake client.

#### Unit tests

- Cover condition and reason transitions for creation, recreation, updates, scaling, pattern migration, pauses, deletion, and insufficient observations for RBG, RIS, and RI.
- Validation ownership is split between admission and controller status: deterministic static errors are rejected by the webhook. Persisted invalid specs and runtime incompatibility report `Progressing=True` with a configuration-family reason at the top of the blocker priority; no `Accepted` condition or `Failed` phase is introduced.
- RIS does not add `status.phase` or a `Ready` condition. RI recovery is reported by RIS `Progressing=True(reason=Recovering)` and the existing counters / lower-level conditions. RBG derives recovery facts from lower-level conditions, not from a RIS phase.
- When different RISs are updating and scaling, RBG reports `Progressing=True(reason=RoleInstanceSetUpdated)` and message describes each role's activity. When all relevant progress is paused, RBG reports `Progressing=False(reason=Paused)`.
- When RIS update and scaling workflows overlap, RIS reports one dominant `Progressing` reason; fully paused progress reports `False(reason=Paused)`, and an active update without a higher-priority blocker reports `RollingUpdate` while message describes the scaling workflow. Verify that programs do not parse that message to detect concurrent scaling; structured multi-workflow reporting is intentionally out of scope for v1alpha2.
- Propagate initial readiness and creation completion signals upward through conditions and counters. Recreating a child with a new UID does not by itself change parent status.
- Recovery completion requires all target Pods to be present and readyPolicy to be satisfied, with completion facts aggregated upward. Cover AllPodReady waiting for Pod readiness and None skipping that wait. Entering Running does not wait for external readiness gates: an RI can enter Running while `RoleInstanceCustomReady` is False, including while a KEP-527 `markPodNotReady` lifecycle gate is still held, and stateless availability still counts that gate as not ready.
- Insufficient observations leave affected conditions Unknown or retain the last RI phase; related facts may change. Deletion of the object does not require complete child observations.
- Cover RI deletion taking priority to enter Terminating, empty revisions during initial creation, child Pod deletion versus RI deletion, and readiness gates becoming unsatisfied again after Running.
- Distinguish RIS initial creation from scale-up, and cover scaling direction and target reversal. `Creating` is reserved for initial set establishment; a scale-up that creates a new RI, such as 2 → 3 with the third RI not yet created, reports `ScalingUp`. Surge, terminating instances, and fault recovery must not be misclassified as scaling.
- Target changes during initial creation do not bypass structural or readiness requirements; withdrawing a target does not bypass operations already issued or required cleanup. For RI, template changes during `Creating` remain `Creating` and fold into Create convergence without invoking `preUpdate` or `preRecover`; when a current blocker exists, the blocker reason takes precedence over the activity reason.
- Cover template, replica, partition, and pause changes during RIS initial creation; RIS has no phase, so `Progressing.reason` is recomputed from current facts. Include the stateful OrderedReady case where the first instance is stuck, then the target is fixed: the affected RI remains `Creating` and converges toward the latest target without invoking hooks, and completion still waits for structural and readiness requirements without reporting early convergence.
- Lowering the target during scale-up while still requiring more instances keeps `ScalingUp`. A target requiring fewer instances transitions to `ScalingDown` without first completing the old target or bypassing observation waits, OrderedReady, budgets, or hooks.
- Scale-up requires new instances to complete startup and satisfy effective readiness conditions; scale-down requires actual completion of target deletions; updates require target configuration to take effect and related replacement / surge cleanup to finish. Request success is not completion.
- Partition may retain older revisions while the current update target completes. A paused, incomplete target must not report `Progressing=False(reason=Converged)` early; counts or revision equality alone are not completion signals.
- Missing Pods during an RI update keep the RI in Updating; independent failures after completion enter Recovering and return to Running only after recovery completes. Updates may complete while inPlaceUpdateContainerBaselines remains populated.
- Cover readiness / convergence truth tables for partition, surge, zero replicas, terminating instances, revisions, and stale observations.
- RBG no longer publishes a `Ready` condition. Verify that legacy consumers (release waits, dependency-based startup, upper-level replica counting) migrate to `roleStatuses`, RIS counters, and RI `RoleInstanceReady`; during the compatibility window the legacy condition keeps existing semantics and is then stopped and cleaned up.
- Configuration-family blockers cover the current target, expanded templates, referenced configuration, and required environmental capabilities. Absent optional configuration does not cause a blocker; a required reference confirmed missing reports `InvalidSpec`, while one that cannot be read reports `Unknown(reason=ObservationIncomplete)`. Confirmed unsupported capabilities are distinct from read failures, and insufficient GPU capacity, quota, external admission decisions, or execution permissions use their own blocker reasons. A blocker is cleared only after a fresh observation confirms the incompatibility no longer holds.
- Individual RIS / RI scaling or update failures produce Warning Events on the corresponding objects, not new failure conditions, and do not directly change the aggregated `Progressing` reason, readiness facts, or RI phase. Legacy writes during the compatibility window follow migration rules. Repeated failures must remain edge-triggered and aggregated so they do not exhaust the object's event budget or trigger the client-go spam filter.
- Progressing reasons match current facts during resource or dependency blocking, recovery, and repeated blocking, without inferring current blockers from historical Events or errors. RI operation-specific reasons map to the documented RIS / RBG reason families, and parents do not parse messages. Blocker propagation covers the full chain: RI `Progressing` is read by RIS, mapped to a family, and then aggregated by RBG; RBG-owned reasons (`WaitingForDependencies`, `WaitingForGang`, configuration-family blockers, `Creating`, `RolesNotReady`) are computed by RBG without requiring a child to report them.
- When one role is scaling and another is quota-blocked, RBG uses `WaitingForQuota` while its message describes both facts. When permission and quota blockers overlap, permission takes precedence. Multiple facts in the same reason family retain the canonical parent reason and list all affected roles / instances in message.
- When one role is `Recovering` and another is updating, RBG uses `Recovering`. When only update or scaling work remains, RBG uses `RoleInstanceSetUpdated`. Message describes every concurrent activity.
- A blocker clears only after a fresh successful observation of the current target. Reason or message changes do not reset `lastTransitionTime`; only condition status changes do.
- Record today's 403-classification behavior, then test quota, RBAC, and admission denials with representative error details. Unclassifiable errors (5xx, timeout, `AlreadyExists` during garbage collection) report `PodOperationUnclassified` at RI and `UnclassifiedBlocker` at RIS / RBG, with the concrete error in message and a Warning Event. A confirmed blocker family is not replaced by an unclassified error and is cleared only by a fresh observation; the unclassified reason is replaced or cleared by the next fresh observation.
- CoordinatedPolicy rollouts step RIS partition and coordinated scaling targets one step at a time. A RIS may alternate between `Converged` and active reasons while RBG remains `Progressing=True` until the RBG-level target completes; RBG completion never derives from child phase or `Converged`.
- Before or during dispatch of a new generation, including preCheck failures, the parent writes `observedGeneration=N` together with the `Progressing` value derived from validation, pause, and progress rules; condition-only changes trigger a status write even when counters and revisions are unchanged.
- When RI recovery overlaps an active RIS update or scaling workflow, RIS `Progressing` uses `Recovering` and message describes the update or scaling workflow.
- After controller restart, RBG and RIS recompute `Progressing.reason` from current facts rather than from a persisted lifecycle phase.
- RI phase and `Progressing` remain independent, including `phase=Recovering` with `Progressing=True` while recovery is blocked, and `phase=Running` with `Progressing=True` for a blocker that does not involve recovery.
- Cover absent / False / Unknown / True gates, multiple gates, readyPolicy, and reason consistency across blocking, recovery, and repeated blocking.
- Cover gate and minReadySeconds consumer behavior in stateful / stateless modes, and condition uniqueness, ownership, and transition timestamps. `RoleInstanceReady` aggregates Pod readiness and the configured in-place gate only; external gates are reported separately and must not change stateful `isHealthy` behavior.
- Document and regression-test the stateless / stateful readiness-gate difference: stateless requires `RoleInstanceReady` plus every `spec.readinessGates` condition, stateful checks only `RoleInstanceReady`.
- Cover legacy API writes, historically incomplete conditions, and applicability boundaries for v1alpha2 objects using legacy backends and mixed-backend RBGs.
- A legacy writer omitting condition.observedGeneration does not confirm the current generation; the aggregation layer does not supply confirmation for external owners.
- For existing RI objects without phase, confirmed completion initializes Running, confirmed in-progress workflows initialize the corresponding RI phase, and uncertainty leaves phase absent. Confirmed deletion of the RI initializes Terminating.

#### Integration tests

- Validate CRD constraints, condition list semantics, SSA, conversion, and writes to existing objects against a real API Server.
- Test concurrent external gate writes and status retries: external fields are preserved, and timestamps do not change without a transition.
- After changes only to revision labels or after dependency recovery, status eventually reflects current facts without an additional manual spec change.
- Provide consumer-facing health-check examples for the decision table above: Flux 2.5+ CEL expressions and an Argo CD Lua check. These examples are implementation deliverables and are validated against the same contract tests.
- Re-evaluate configuration-family blockers when required environmental capabilities recover, even if RBG generation is unchanged. Capability probe failures must not be reported as confirmed `SchedulerUnsupported`.
- Validate failure Event attribution to the correct object and duplicate reporting handling. After upgrade, stop writing and clean up controller-owned failure conditions according to the compatibility window; legacy writers / rollback must not cause removal of external conditions.
- Preserve restart / rollout recovery fields across status migration and controller restarts. Healthy workloads are not recreated because of status field migration.
- RI phase changes do not overwrite external conditions, reset readiness timing, or bypass Restarting safeguards. Initialization of existing objects does not fabricate initial creation; re-upgrade after rollback revalidates current facts rather than reusing a phase that stopped being updated.
- RIS does not add phase. `Progressing.reason` is recomputed from valid observations of the current target and does not change existing Stateless / Stateful execution order, budgets, or gates. RBG and RIS aggregate lower-level conditions and recovery facts; stale or absent fields must not trigger completion.
- Missing resources and recreation can still trigger RBG and RIS recovery reporting when generation is unchanged; RI enters Recovering. After controller restart, completion still requires all target Pods to be present and effective readiness conditions to hold; recovery waits must not report early convergence, and read failures must not be treated as missing resources.
- RBGs containing legacy backends retain existing status. Clean up controller-owned new-model output when applicability changes, and initialize from fresh observations after migration to all-RIS roles, without recreating workloads.

#### e2e tests

- Cover RBG / RIS / RI lifecycle, observation freshness, overlapping operations across roles, resource loss and recovery during normal operation, gang / dependency recovery, and consumer compatibility across RIS modes.
- Cover custom gates, minReadySeconds, partition, pauses, zero replicas, and upgrade / downgrade / re-upgrade.
- Prevent regressions in legacy-backend status behavior for mixed-backend RBGs during migration. When LWS is involved, run its actual controller rather than only installing its CRD.
- Backends whose support has been removed receive explicit rejection and migration guidance, rather than being ignored or reported as globally converged.

### Graduation Criteria

| Stage | Criteria |
| --- | --- |
| Alpha | Approved scope implemented; enablement and rollback boundaries defined; unit, integration, RIS e2e, consumer, and required legacy-backend regression tests pass; generated artifacts match the English API documentation |
| Beta | Functional, security, monitoring, compatibility, testing, and scalability requirements met; known defects and gaps resolved; stable test records, user feedback, and overhead measurements available |
| GA | A defined feedback period has elapsed, Beta feedback has been addressed, and status semantics are stable; missing functionality or test coverage is not deferred to GA |

Deprecating legacy conditions / reasons first requires defining reporting behavior and the compatibility window, then stopping writes and cleaning up entries, and finally removing Go constants or types approved for deprecation.
Specific release versions and feedback periods will be added when the release target is determined.

### Upgrade / Downgrade Strategy

| Interface | Migration Requirements |
| --- | --- |
| RI phase | New optional field; initialization when absent follows the table below; updates may stop after rollback |
| RBG `Ready` | Deprecated and removed after the compatibility window: legacy writes may continue with existing all-replicas-ready semantics, then stop and clean up controller-owned entries; new consumers use `roleStatuses`, RIS counters, and RI `RoleInstanceReady` |
| Progressing | RBG summarizes updates and scaling as `RoleInstanceSetUpdated`; validate compatibility for release waits, dependency-based startup, and upper-level replica counts |
| Legacy backend applicability | RBGs containing legacy backends retain existing status without enabling the new overall condition model; new status on RIS / RI themselves is unaffected |
| RollingUpdateInProgress | Not a new implementation goal; deprecate before handling constants and persisted legacy values |
| GangConfigured | Retain existing semantics and absence when gang is not enabled during the compatibility window; new and legacy conditions must not contradict each other |
| FailedScale / FailedUpdate | After confirming Warning Event coverage and the compatibility window, stop writing and clean up controller-owned legacy entries |
| Condition schema | RIS / RI retain custom types for now, adding optional observedGeneration; do not tighten existing required-field constraints or change the list schema; absence does not confirm the current generation |
| Recovery fields and external conditions | Upgrades, downgrades, and legacy writers must not inadvertently overwrite or delete them |
| RI transition timestamps | Today the RI controller rebuilds `RoleInstanceReady` / `RoleInstanceAllPodsReady` with the current time and replaces the condition list, so every status write resets `lastTransitionTime`. Preserving it is a behavior change: stateless RIS uses these timestamps for `minReadySeconds`, so availability timing changes after upgrade. Migration must preserve timestamps deliberately and regression-test `minReadySeconds` behavior before and after upgrade |

While v1alpha1 is supported, conversion and legacy writes must preserve data compatibility; equivalent new fields are not required in the frozen API.
Status field migration does not recreate healthy Pods / RIs. User-initiated migration of legacy workload types may recreate Pods and is a separate process.
When an RBG no longer uses RIS for all roles, clean up controller-owned new-model output while preserving existing status and external conditions.
After migration to all-RIS roles, initialize from fresh observations rather than inheriting legacy backend completion conclusions. New fields may stop updating after rollback; consumers must recognize missing or stale status.

Initialize existing RI objects without phase based on facts that can currently be confirmed:

| Confirmed Fact | Initialization Behavior |
| --- | --- |
| Current relevant workflows have completed | May enter Running directly without repeating initial creation |
| Creation, update, or recovery is in progress | Initialize according to the RI phase definitions; independent recovery enters Recovering |
| The current phase cannot be confirmed | Leave phase absent; do not default to initial creation or Running |
| The RI itself has a deletionTimestamp | Enter Terminating without requiring complete child observations |

Re-upgrade after rollback revalidates current targets and observations rather than trusting a retained RI phase that stopped being updated.
Upgrade to a CRD that supports the additional fields and remains compatible with legacy writes before enabling the new controller. Rollback does not guarantee continued updates to new status, and new CRD fields must not be removed casually.
Upgrades, rollbacks, and re-upgrades preserve valid readiness timing, external conditions, and recovery safeguard fields.

<<[UNRESOLVED Upgrade and schema compatibility]>>
The specific projection and field-preservation behavior for legacy APIs, handling of historical duplicate conditions and missing timestamps, and supported version / rollback combinations still require confirmation and compatibility testing against a real API Server.
<<[/UNRESOLVED]>>

### Version Skew Strategy

| Combination | Compatibility Requirements |
| --- | --- |
| New CRD + old controller | Legacy writes are not rejected by new required fields or list constraints |
| Old CRD + new controller | Define the outcome of pruning / rejecting new fields; do not claim the new contract is enabled |
| Existing objects / old clients + new controller | Accept historically absent fields, handle unknown RI phases / reasons conservatively, and preserve recovery data |
| v1alpha1 / v1alpha2 coexistence | Ensure projection / write compatibility within the support window, without promising new status capabilities in v1alpha1 |
| Existing deprecated backends | Preserve legacy behavior while supported; retain rejection and migration guidance after support is removed |

This feature does not require synchronized upgrades of kubelet, CRI, CNI, or cloud services, and introduces no new LWS version support commitment.

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

| Question | Answer |
| --- | --- |
| How is the feature enabled / disabled? | Release-based enablement versus a feature gate is TBD and will be specified with the release target |
| Does it change default behavior? | New conditions and RI phase add output; the deprecated RBG `Ready` condition is removed after a compatibility window, and aggregation, observation, and legacy condition / schema changes still require consumer impact assessment |
| Can enablement be rolled back? | No supported combinations have been validated yet; new fields may stop updating, recovery fields must be preserved, and rollback cannot consist solely of deleting CRD fields |
| What happens on re-enablement? | Revalidate conditions and RI phase against current targets and observations rather than reusing stale values; preserve readiness timing, external conditions, and recovery information |
| Have enable / disable tests been run? | No results yet; upgrade / downgrade / re-upgrade are planned, with enabled / disabled combinations added if a feature gate is adopted |

Enabling the status feature should not require workload or node recreation. Availability during controller upgrades follows the project's deployment approach.

### Rollout, Upgrade and Rollback Planning

| Question | Answer |
| --- | --- |
| How can rollout or rollback fail? | Schema rejection of legacy writes, field pruning, incorrect aggregation, readiness-reporting regressions from the deprecated `Ready` condition, gate bypass, or loss of recovery data may affect running workloads |
| What signals indicate a need to roll back? | Increased status update errors, unexpected changes to the deprecated `Ready` condition or stale readiness facts, abnormal transition timestamps, gate failures, or unexpected recreation |
| Has upgrade / downgrade / re-upgrade been validated? | Not yet; traceable execution results are required for delivery |
| Does the feature introduce deprecations? | See the upgrade strategy for legacy condition / reason handling; specific windows will be determined with the release plan |

### Monitoring Requirements

| Question | Answer |
| --- | --- |
| How can feature usage be identified? | Check the version / configuration, each role's actual workload type, and valid output from new fields; apiVersion or historical RI phase alone is insufficient |
| How is object health assessed? | Read conditions, observedGeneration, role status, RIS targets, and RI phase together; evaluate legacy / mixed-backend RBGs using compatibility semantics |
| What is the SLO? | Report actual convergence without false convergence or status churn for stable facts; measure quantitative latency and scale baselines before release |
| Which SLIs are used? | Reconcile errors and duration, workqueue latency, status write failures, and the duration of discrepancies between targets and reported status |
| Are additional metrics needed? | Decide after quantitative assessment; do not use messages or unbounded reasons as high-cardinality labels |

### Dependencies

The feature depends on the Kubernetes API Server, controller cache, and RBG / RIS / RI controllers. External readiness writers and enabled scheduling capabilities affect condition facts.
Controller or dependency failures can delay observations and must not result in unverified convergence reports. The new feature does not depend on the LWS controller or add services outside the cluster.
Legacy backends retain their existing runtime dependencies during migration; their compatibility regression tests require actual backend controllers.

### Scalability

| Question | Answer |
| --- | --- |
| Does it add API calls? | Field changes may increase status updates; request volume remains to be measured |
| Does it add API types or cloud service calls? | Neither is added |
| Does it increase object size or count? | No workload objects are added; RI phase and condition fields enlarge status, so field and message lengths must be bounded; failure reporting reuses Event aggregation, with event volume and write overhead to be assessed |
| Does it increase operation latency? | Aggregation and serialization add overhead that must be measured |
| Does CPU, memory, or IO usage increase? | Measure cache reads, deep copies, serialization, and write volume across multiple roles and RIS modes; assess mixed-backend RBG impact during the compatibility window |
| Can it exhaust node resources? | No node processes or workload instances are added; validation focuses on controller / API load and sustained status-change scenarios |

### Troubleshooting

When the API Server / etcd is unavailable, the controller cannot guarantee writing Unknown, and clients may see old persisted values.
Check controller and API health first, then verify that status updates resume after recovery. Historical readiness facts do not prove completion of a new target.

| Symptom | Checks and Mitigation | Planned Validation |
| --- | --- | --- |
| Legacy backends report unexpected readiness changes after upgrade during migration | Compare against the legacy behavior baseline and check for unintended freshness requirements; roll back if necessary rather than requiring new fields from legacy backends | Compatibility regression tests |
| Existing operations continue while a gate is False | Compare against consumer baselines and roll back affected changes; do not bypass the issue by removing the gate | Gate / minReadySeconds tests |
| Transition timestamps change without a status change | Compare persisted results and concurrent writers | Concurrent write tests |
| Status does not recover after label-only changes | Check target association and status refresh | Label-change tests |
| Legacy status writes are rejected | Check CRD constraints, list semantics, conversion, and version combinations | Schema / upgrade tests |

Use structured reasons to identify current status. For individual scaling or update failures, inspect Warning Events on the corresponding RIS / RI along with logs.
Events may be aggregated and have a retention period; they are neither permanent audit records nor the sole evidence of current blockers. Public messages must not contain sensitive information, credentials, or unfiltered container output.
If the SLO is not met, check the workqueue, dependency health, and status errors, then follow a validated rollback procedure.

## Drawbacks

- Multi-layer status increases the long-term cost of maintaining consistency and API compatibility.
- RBG and RIS rely on `Progressing.reason` instead of a lifecycle phase; concurrent activities are summarized in one reason and detailed in message.
- Even without a RBG-level `Ready` condition, aggregation, observation, and condition migration can affect automation and upper-level controllers. Migration cost cannot be estimated from the number of new fields alone.
- New and legacy status coexist during migration, and mixed-backend RBGs cannot receive the full new top-level convergence guarantees.

## Alternatives

| Approach | Trade-off |
| --- | --- |
| Add reasons only | Smaller API change, but clients must still infer lifecycle |
| Keep a RBG `Accepted` compatibility condition | Not adopted: static errors belong to admission, runtime incompatibility is a current blocker, and Deployment / Pod expose such facts through condition reasons plus Events rather than an acceptance verdict |
| Use phase on RBG / RIS | Simple presentation, but creates a second single-valued summary and requires additional transition, initialization, and rollback rules |
| Keep the existing all-replicas-ready `Ready` condition on RBG | Not adopted: RBG is an orchestration resource, a service-ready verdict depends on consumer policy, and Deployment / StatefulSet expose readiness through counters instead |
| Use gang min as the readiness threshold | Not adopted; serving policy is consumer-specific and is not a controller-owned status fact |
| Retain only conditions on RI without adding phase | Smaller API change, but the RI controller's instance-level lifecycle state would remain implicit; this proposal adds RI phase while preserving independent condition facts |

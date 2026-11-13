# KEP-543: Segment as the Cross-Role Service Unit

<!-- toc -->
- [KEP-543: Segment as the Cross-Role Service Unit](#kep-543-segment-as-the-cross-role-service-unit)
  - [Release Signoff Checklist](#release-signoff-checklist)
  - [Summary](#summary)
  - [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
  - [Proposal](#proposal)
    - [Design Overview](#design-overview)
    - [Notes and Constraints](#notes-and-constraints)
    - [Risks and Mitigations](#risks-and-mitigations)
  - [Design Details](#design-details)
    - [Controller Responsibilities](#controller-responsibilities)
    - [API](#api)
    - [Validation](#validation)
      - [Policy Admission Validation](#policy-admission-validation)
      - [Member Mode Validation](#member-mode-validation)
      - [Replica and Execution Validation](#replica-and-execution-validation)
    - [Group Assignment and Persistence](#group-assignment-and-persistence)
      - [Passing Group Descriptions Through RIS Annotations](#passing-group-descriptions-through-ris-annotations)
      - [RI and Pod Label Synchronization](#ri-and-pod-label-synchronization)
    - [Complete Segment Invariants](#complete-segment-invariants)
    - [Integration with Scaling](#integration-with-scaling)
      - [Applying Replica Targets](#applying-replica-targets)
    - [ScalingAdapter](#scalingadapter)
      - [Segment Adapter Configuration and Automatic Creation](#segment-adapter-configuration-and-automatic-creation)
      - [Targets and Replica Units](#targets-and-replica-units)
      - [Replica Control Authority and Lifecycle](#replica-control-authority-and-lifecycle)
      - [Adapter and RBG Scaling Coordination Loop](#adapter-and-rbg-scaling-coordination-loop)
    - [Scale Subresource and Metrics Scope](#scale-subresource-and-metrics-scope)
    - [Integration with Gang](#integration-with-gang)
      - [Volcano SubGroup Mapping](#volcano-subgroup-mapping)
      - [Configuration Validation with Scaling](#configuration-validation-with-scaling)
    - [Integration with Rolling Update](#integration-with-rolling-update)
      - [RBG and RIS Update Coordination Loop](#rbg-and-ris-update-coordination-loop)
      - [Member Availability and Reusing Budget Capacity](#member-availability-and-reusing-budget-capacity)
      - [Policy Overrides and RIS Execution Windows](#policy-overrides-and-ris-execution-windows)
      - [maxSurge Limits and RIS Execution](#maxsurge-limits-and-ris-execution)
      - [Generating Partitions by Segment](#generating-partitions-by-segment)
      - [Applying New Templates and Partitions After a Target Version Change](#applying-new-templates-and-partitions-after-a-target-version-change)
    - [Composition with Topology](#composition-with-topology)
    - [Adding and Removing CoordinatedPolicy](#adding-and-removing-coordinatedpolicy)
      - [Adding a Policy to an Existing RBG](#adding-a-policy-to-an-existing-rbg)
      - [Withdrawing Coordination Constraints After Policy Deletion](#withdrawing-coordination-constraints-after-policy-deletion)
      - [Re-adding Configuration and Renaming Rules](#re-adding-configuration-and-renaming-rules)
    - [Test Plan](#test-plan)
      - [Prerequisite Testing Updates](#prerequisite-testing-updates)
      - [Unit Tests](#unit-tests)
        - [API Validation](#api-validation)
        - [Group Assignment](#group-assignment)
        - [Policy Lifecycle](#policy-lifecycle)
        - [Scaling](#scaling)
        - [ScalingAdapter Status and Control Authority](#scalingadapter-status-and-control-authority)
          - [Adapter Configuration and Target Submission](#adapter-configuration-and-target-submission)
          - [Adapter Control Authority and Recovery](#adapter-control-authority-and-recovery)
          - [Adapter Status and Scale Mapping](#adapter-status-and-scale-mapping)
        - [Rolling Update](#rolling-update)
          - [Group-Level Admission and Budget Recovery](#group-level-admission-and-budget-recovery)
          - [Update Parameters and Surge Projection](#update-parameters-and-surge-projection)
        - [Gang](#gang)
      - [Integration Tests](#integration-tests)
        - [Member Modes and Group Identity](#member-modes-and-group-identity)
        - [Adapter Targets and Control Handoff](#adapter-targets-and-control-handoff)
        - [Creation Batches and Progression](#creation-batches-and-progression)
        - [Group-Level Updates and Surge](#group-level-updates-and-surge)
        - [Gang Resource Admission](#gang-resource-admission)
        - [Address Discovery](#address-discovery)
        - [Policy Adoption and Withdrawal](#policy-adoption-and-withdrawal)
        - [Controller Downgrade Preparation](#controller-downgrade-preparation)
      - [End-to-End Tests](#end-to-end-tests)
    - [Graduation Criteria](#graduation-criteria)
      - [Alpha](#alpha)
      - [Beta](#beta)
      - [GA](#ga)
    - [Upgrade / Downgrade Strategy](#upgrade--downgrade-strategy)
      - [Migration and Compatibility](#migration-and-compatibility)
      - [Upgrade](#upgrade)
      - [Controller Downgrade](#controller-downgrade)
    - [Version Skew Strategy](#version-skew-strategy)
  - [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
    - [Feature Enablement and Rollback](#feature-enablement-and-rollback)
      - [How can this feature be enabled or disabled in a live cluster?](#how-can-this-feature-be-enabled-or-disabled-in-a-live-cluster)
      - [Does enabling the feature change any default behavior?](#does-enabling-the-feature-change-any-default-behavior)
      - [What happens if the feature is re-enabled after being disabled?](#what-happens-if-the-feature-is-re-enabled-after-being-disabled)
      - [What tests cover feature enablement and disablement?](#what-tests-cover-feature-enablement-and-disablement)
    - [Rollout, Upgrade and Rollback Planning](#rollout-upgrade-and-rollback-planning)
      - [What must be completed before a controller rollback?](#what-must-be-completed-before-a-controller-rollback)
      - [Have upgrade and rollback been tested?](#have-upgrade-and-rollback-been-tested)
    - [Monitoring Requirements](#monitoring-requirements)
      - [How can an operator determine whether workloads use the feature?](#how-can-an-operator-determine-whether-workloads-use-the-feature)
      - [How can users verify group assignment and policy execution?](#how-can-users-verify-group-assignment-and-policy-execution)
    - [Dependencies](#dependencies)
      - [Which cluster services does the feature depend on?](#which-cluster-services-does-the-feature-depend-on)
    - [Scalability](#scalability)
      - [Does the feature introduce additional API calls?](#does-the-feature-introduce-additional-api-calls)
      - [Does the feature increase API object size or count?](#does-the-feature-increase-api-object-size-or-count)
      - [Does the feature increase controller resource usage?](#does-the-feature-increase-controller-resource-usage)
    - [Troubleshooting](#troubleshooting)
      - [What are the known failure modes and how are they handled?](#what-are-the-known-failure-modes-and-how-are-they-handled)
  - [Implementation History](#implementation-history)
  - [Drawbacks](#drawbacks)
  - [Alternatives](#alternatives)
    - [Use Only the Existing CoordinatedPolicy](#use-only-the-existing-coordinatedpolicy)
    - [Let Applications Manage Groups](#let-applications-manage-groups)
    - [Define Segment as a Scheduling Strategy](#define-segment-as-a-scheduling-strategy)
  - [References](#references)
<!-- /toc -->

## Release Signoff Checklist

- [x] Enhancement issue: [#543](https://github.com/sgl-project/rbg/issues/543)
- [ ] KEP reviewers and approvers confirmed
- [ ] Design details fully documented
- [ ] Test plan confirmed
- [ ] Graduation criteria confirmed
- [ ] Production readiness review completed
- [ ] User documentation and examples added
- [ ] Implementation history kept up to date

## Summary

Segment groups RoleInstances from multiple roles in a fixed ratio, providing a common unit for scaling, rolling updates, and Gang scheduling. For example, `2P:1D` means that one group contains two prefill RoleInstances and one decode RoleInstance.

The `segment` field in `CoordinatedPolicy.spec.policies[]` declares only the member structure. The `strategy` in the same rule determines creation batch size, scheduling thresholds, and update budgets. Group identity is stored in managed labels on RoleInstances and Pods and remains stable within the same effective grouping configuration. Members can be created, started, and become Ready at different times; old and new versions may coexist during updates.

This document is intended for KEP reviewers and controller developers familiar with RBG, RIS, and Kubernetes controller fundamentals. It is a design proposal: the Segment API extensions, controller adaptations, and tests are not yet complete. Statements marked as existing behavior describe the mechanisms being reused; all other behavior is an implementation requirement. This English version follows the [Chinese design](README.zh-CN.md).

This document uses the following terms:

| Term | Meaning |
| --- | --- |
| RBG | RoleBasedGroup; stores the final desired replicas and workload configuration for each role |
| RIS | RoleInstanceSet; manages the RoleInstances of one role |
| RI | RoleInstance; one RI can contain multiple component Pods |
| Policy | The CoordinatedPolicy with the same namespace and name as the target RBG |
| Adapter | RoleBasedGroupScalingAdapter; receives external scaling targets |
| P / D | prefill / decode; quantities in a ratio count RIs, not Pods |
| Base group / surge RI | Base groups are determined by desired replicas; surge RIs are temporary extra instances beyond `RIS.spec.replicas` during an update |

## Motivation

PD-disaggregated inference requires pairing prefill and decode instances. The existing CoordinatedPolicy coordinates multiple roles but lacks stable cross-role subgroups: independent scaling can create unpaired instances, rolling updates cannot share group-level batches, and routers cannot easily identify which instances should serve together. Future topology placement also needs a shared subgroup definition.

### Goals

- Declare each role's RI quota in a rule-level `segment` map and derive group membership from instance ordinals.
- Maintain stable group identities on RIs and Pods, allowing existing RBGs to adopt grouping without restarting members.
- Calculate replica targets, update batches, and scheduling resource thresholds in complete base groups. Provide queryable identities for future router and topology designs; this KEP provides identity only, not routing.

### Non-Goals

- Infer ratios automatically, implement group-aware routing, or require routers to use it.
- Design a complete topology API, tiers, packs, or scheduler translation.
- Guarantee atomic operations across objects, simultaneous readiness, updates without mixed versions, or seamless application-state migration when regrouping.
- Change address discovery for non-Segment roles. Segment-aware discovery requirements are defined in [complete Segment invariants](#complete-segment-invariants).
- Add progression specifically for initial creation, a whole-Segment update switch, an actual surge target, or a cross-role lifecycle protocol for temporary groups.
- Design a complete HPA integration or add general-purpose Pod selection and business-metric collection, aggregation, or deduplication. This phase defines only ScalingAdapter capabilities.
- Support Stateless RIS or deprecated workloads such as Deployment, StatefulSet, and LWS that will no longer be supported in subsequent versions.
- Design API downgrades, compatibility with old clients, or reverse mapping to legacy coordination configuration. This phase defines only the controller downgrade procedure after withdrawing Segment.

## Proposal

Segment separates structure, policy, and execution: Policy declares ratios and strategies, RBG calculates group-level targets, and RIS and RI perform instance operations.

### Design Overview

The following design example groups the prefill and decode roles of the `llm-serving` RBG at a `2P:1D` ratio. The Policy and RBG must have the same namespace and name:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: llm-serving
spec:
  policies:
    - name: kv-cache
      roles: [prefill, decode]
      segment:
        prefill: 2
        decode: 1
      strategy:
        scheduling:
          gang:
            minSegments: 1
        scaling:
          segBatchSize: 1
          progression: OrderScheduled
        rollingUpdate:
          maxUnavailable: 1
          maxSurge: 0
```

With desired replicas `P=4, D=2`, group membership is as follows. Instance names illustrate the role and ordinal:

| Group | Members | `rbg.workloads.x-k8s.io/segment` label on each RI and its Pods |
| --- | --- | --- |
| Segment 0 | prefill-0, prefill-1, decode-0 | `e7652f82cdee-0` |
| Segment 1 | prefill-2, prefill-3, decode-1 | `e7652f82cdee-1` |

The label value follows the normative `<ruleID>-<groupIndex>` scheme. The rule name `kv-cache` derives the deterministic rule ID `e7652f82cdee`, the first 12 lowercase hexadecimal characters of `SHA-256("kv-cache")`.

Strategies share the same grouping but have different responsibilities:

| Capability | Operation and example | Limits and boundaries |
| --- | --- | --- |
| Scaling | Scaling from 2 to 3 groups changes final replicas from `P=4, D=2` to `P=6, D=3`; `segBatchSize` controls creation batches and `progression` determines when to continue | A member role cannot scale independently |
| Rolling updates | Members of a group are admitted in the same batch; without surge, `maxUnavailable: 1` allows active updates to occupy at most one group of budget at a time | Members may execute sequentially with mixed versions; when surge is enabled, [extra available capacity](#maxsurge-limits-and-ris-execution) participates in the budget |
| Gang scheduling | `minSegments: 1` requires schedulable resources for all Pods in one complete group | Resource admission only; does not wait for application readiness |
| Router / topology | Consume group identity for routing or topology placement | This KEP implements neither routing nor a topology API |

Admission in the same batch means that members of a Segment enter the update scope in the same round. Execution, startup, and readiness can still occur at different times.

### Notes and Constraints

- Only native Stateful RoleInstanceSet members with dense membership are supported in this phase. Reject Stateless RIS, deprecated workload backends, unknown backends, `excluded-ordinals`, and a non-empty child `committed-skip-ordinals` record. Grouping itself does not imply Stateful mode.
- Within one Policy, Segment members must not appear in any other rule, regardless of strategy domain. Roles that require joint coordination must declare their ratio and strategies in one rule.
- The ratio cannot be changed in place within the same effective configuration. After withdrawal, a new ratio can be validated and adopted without recording or restricting historical ratios.
- Segment ignores `scaling.maxSkew` and `rollingUpdate.maxSkew`. Creation uses batches and progression; updates use a shared boundary and group-level budget.
- Segment scaling and Gang execute independently without adjusting each other's parameters. Policy admission and RBG reconciliation validate their compatibility to prevent persistent mutual waiting caused by an insufficient first batch.
- Non-Segment behavior is preserved. The only new override capability is an explicitly configured Policy `maxSurge`; see [parameter override rules](#maxsurge-limits-and-ris-execution).

### Risks and Mitigations

| Risk or cost | Design boundary and handling |
| --- | --- |
| Ratios or rule organization limit flexibility | No independent member-role scaling, membership overlap across rules, or in-place ratio changes; coordinate related roles in one rule and change ratios through withdrawal and re-adoption |
| Desired replicas violate the ratio | Report residual instances for each role and stop the entire RBG reconciliation before any Segment or role target is written. Initial creation produces no member target, an active Segment keeps its current valid targets, and existing-workload adoption does not take effect; users must correct the target |
| Grouping differs from application routing | RBG publishes and maintains identity but cannot verify group-aware routing; regrouping does not guarantee seamless application-state or routing migration |
| Independent RIS execution could exceed the budget | RBG deduplicates by group identity and controls a common boundary; partial parameter application, target changes, and delayed status require verification |
| Slow members or a bad version keep occupying budget | Without surge, an exhausted budget can block new groups; otherwise eligible groups may use remaining capacity, without automatically increasing the budget |
| Surge capacity is mistaken for a complete standby service | Only limit projection is guaranteed; RIS determines actual counts and reclamation, and extra RIs need not form groups |
| Scaling and Gang thresholds conflict | Policy admission rejects static conflicts, while RBG reconciliation reports conflicts caused by related-resource changes; the system neither raises targets nor lowers thresholds automatically |
| Zero update budget without surge | A zero Segment group budget requires positive effective `maxSurge`; admission rejects the static conflict and reconciliation reports combinations that bypass admission |
| Labels are modified or synchronization is delayed | Controllers restore managed fields; consumers distinguish label presence from an effective configuration |

## Design Details

The following sections define controller responsibilities, APIs and validation, group identity, scaling, updates, and configuration lifecycle, followed by testing and release requirements. Implementations must satisfy both group-level constraints and existing RIS execution conditions.

### Controller Responsibilities

| Component | Responsibilities |
| --- | --- |
| RBG controller | Read Policy; validate adoption conditions; calculate group-level replica targets, update boundaries, and budgets; maintain group descriptions, Adapters, and policy lifecycle |
| WorkloadReconciler | Backend adaptation layer within the RBG controller that writes calculated results to RIS; not a separate controller |
| RIS controller | Read the description for its role and maintain RI labels; execute within the supplied replica target, partition, and local budget without directly parsing the full Policy |
| RI controller | Manage component Pods, synchronize group identity to existing Pods and future Pod templates, and perform Pod updates |
| ScalingAdapter controller | Receive the final desired group count, submit all members' RBG replica targets together according to the ratio, and maintain status; neither create itself nor directly operate on RIS, RI, or Pod objects |

### API

Policy field paths start at `CoordinatedPolicy.spec.policies[]`. All new fields are optional. The table summarizes configuration locations and defaults; the corresponding capability sections define execution details.

| Field | Type or unit | Default behavior and purpose |
| --- | --- | --- |
| `segment` | `map[string]int32`, RI quotas | If omitted, use non-Segment rule behavior |
| `strategy.scaling.segBatchSize` | Positive integer, groups | Defaults to `1` when a Segment scaling strategy exists; applies to initial creation and scale-out |
| `strategy.scaling.scalingAdapter` | Reuses the existing `ScalingAdapter` type | `enable` defaults to `false`; `labels` are added to the automatically created Adapter |
| `strategy.scheduling.gang.minSegments` | Positive integer, groups | Without scaling, omission means all desired complete groups; required when scaling and Gang are both configured; mutually exclusive with `minReplicas` |
| `strategy.rollingUpdate.maxSurge` | Integer or percentage | If omitted, explicitly apply `0` for Segment; retain role behavior for non-Segment rules |

The existing `strategy.scaling.maxSkew` and `strategy.rollingUpdate.maxSkew` fields remain, but Segment calculations ignore their values. See [rolling updates](#integration-with-rolling-update) for the group-level units of existing update fields. See [targets and replica units](#targets-and-replica-units) for the Adapter's new `spec.scaleTargetRef.segment` field and replica semantics.

Add a field to `CoordinatedPolicyRule`:

```go
type CoordinatedPolicyRule struct {
    // ...
    // Segment maps role name to the number of RoleInstances in one Segment.
    // It is a structural declaration and does not define rollout behavior.
    // +optional
    // +kubebuilder:validation:Type=object
    Segment map[string]int32 `json:"segment,omitempty"`
}
```

Add optional fields to `ScalingCoordinationStrategy`:

```go
type ScalingCoordinationStrategy struct {
    // ...
    // SegBatchSize is the number of complete Segments added per batch.
    // It applies only to Segment rules, defaults to 1, and does not support percentages.
    // +optional
    // +kubebuilder:validation:Minimum=1
    SegBatchSize *int32 `json:"segBatchSize,omitempty"`

    // ScalingAdapter configures the autoscaling adapter for this Segment rule.
    // +optional
    ScalingAdapter *ScalingAdapter `json:"scalingAdapter,omitempty"`
}
```

`ScalingAdapter` reuses the [existing type in the RBG API](../../api/workloads/v1alpha2/rolebasedgroup_types.go) rather than defining the same configuration again. If `strategy.scaling` is absent, do not create a strategy implicitly or enable the default creation batch size.

Add an optional field to `RollingUpdateCoordinationStrategy`:

```go
type RollingUpdateCoordinationStrategy struct {
    // ...
    // MaxSurge limits extra capacity during updates and supports integers or percentages.
    // Segment rules resolve it in groups and project it to roles.
    // Ordinary rules override role configuration with the original value.
    // +optional
    // +kubebuilder:validation:XIntOrString
    MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`
}
```

Role and RIS already have `maxSurge`; the Policy API and coordination override paths must add this capability. Implementation must update the API, CRDs, generated code, RBG parameter calculations, and parameter application to RIS together.

Add a Segment-counting field to `GangSchedulingStrategy`:

```go
type GangSchedulingStrategy struct {
    // ...
    // MinSegments counts complete Segments for a partitioned rule.
    // It is mutually exclusive with MinReplicas within the same rule.
    // +optional
    MinSegments *int32 `json:"minSegments,omitempty"`
}
```

Update the Adapter target reference so role and Segment targeting are mutually exclusive but individually optional:

```go
type AdapterScaleTargetRef struct {
    Name string `json:"name"`

    // Role names an individual RBG role. Exactly one of Role and Segment
    // must be set. Existing role-target clients are unchanged.
    // +optional
    Role string `json:"role,omitempty"`

    // Segment names a CoordinatedPolicy rule and makes spec.replicas a
    // complete-Segment count.
    // +optional
    Segment string `json:"segment,omitempty"`
}
```

Admission and reconciliation must reject references where both `role` and `segment` are set or both are empty. The CRD schema, generated code, and validation must be updated together; a required-to-optional change to `role` is part of this contract.

### Validation

Admission validates configuration structure; RBG reconciliation validates related resources and execution conditions. Policy field paths below are relative to `spec.policies[]`.

#### Policy Admission Validation

| Validation target | Requirement |
| --- | --- |
| `segment` and `roles` | Exactly matching key sets, at least two roles, every value greater than zero, and no duplicate role entries |
| Exclusive membership | Segment members must not intersect with `roles` in any other rule, regardless of strategy domain; results must be consistent for creation, updates, and different rule orderings |
| `strategy.scheduling.gang.minSegments` | Positive integer, valid only for Segment, and mutually exclusive with `minReplicas` in the same rule |
| Scheduler for Segment Gang | Requires Volcano with `subGroupPolicy`; reject scheduler-plugins because its Pod-count threshold cannot guarantee complete Segment identity |
| Segment with both scaling and Gang | Require explicit `minSegments` and effective `segBatchSize >= minSegments`; an omitted batch size counts as 1 |
| Ratio updates | If the same named rule declares Segment before and after an update, its map must be identical; no keys may be added or removed, and no values changed |
| Rule renaming | A single update may rename a rule only when its Segment map is unchanged; changing both the rule name and ratio is rejected and requires withdrawal followed by a separate addition |
| Rule identity | Deterministic rule IDs must not collide within a Policy |
| `strategy.scaling.segBatchSize` | Positive integer, valid only for Segment, with no percentage support; defaults to 1 |
| Segment rolling-update zero budget | An explicit `maxUnavailable` value of `0` requires a positive `maxSurge`; omitted or zero `maxSurge` is rejected |

Requiring explicit `minSegments` when scaling and Gang are combined keeps this check within the current Policy. The webhook does not need to read the final desired group count from an RBG or Adapter. Without scaling, RIS receives the full desired group target directly, so Gang can retain the default of all desired groups when `minSegments` is omitted.

A zero group budget requires positive effective `maxSurge`. Without surge, a zero budget forbids both unavailable instances and update progress, so the combination is a static conflict rather than a paused update: admission rejects the explicit combination, while reconciliation reports percentage budgets that round down to zero without surge. This follows the existing `segBatchSize < minSegments` pattern.

Adding, removing, and re-adding Segment follow the [configuration lifecycle](#adding-and-removing-coordinatedpolicy), without historical-ratio validation.

If overlapping coordination is needed, form one Segment from the union of related roles and configure its strategies in the same rule. For example, coordinating both prefill with decode and prefill with router requires an initial rule covering `[prefill, decode, router]`, with each role's instance count in a Segment. Do not place prefill in two rules. Business requirements determine the ratio; the controller does not merge rules automatically. Ratios in running configurations remain immutable in place. Membership can change by withdrawing the old configuration and validating and adopting a new one; migration to a separate workload is not required.

This restriction applies only to overlap involving Segment members. Ordinary rules with no Segment members retain existing behavior. Multiple RoleInstances of one role within a Segment's ratio do not constitute cross-rule membership overlap.

#### Member Mode Validation

The related Policy and RBG configuration determines whether member modes support Segment:

- On Policy or RBG creation or update, validate members' effective workload backend and mode when the related configuration exists. Every member must use `workloads.x-k8s.io/v1alpha2 RoleInstanceSet` with `Stateful` or the default Stateful pattern. Reject Stateless RIS, Deployment, StatefulSet, LeaderWorkerSet, and unknown backends; do not convert modes or silently ignore Segment.
- Dense membership is a prerequisite. A Segment member may not carry user `excluded-ordinals`, and its child RIS may not carry a non-empty controller-managed `committed-skip-ordinals` annotation. Sparse Stateful membership stops the RBG reconciliation rather than being silently divided by the ratio.
- A Policy can be created before its RBG. Because Policy alone cannot establish member modes, allow it to be saved and validate when the RBG appears. Reconciliation checks again before adoption and before applying Segment parameters. An invalid combination stops the entire RBG reconciliation and reports the member and reason without changing modes or deleting existing instances.
- Non-Segment Stateless roles are unaffected. After Segment is removed, mode changes still follow existing workload validation; withdrawing Segment does not guarantee support for in-place mode switching.

For example, Stateful prefill and Stateless decode cannot enable Segment even if their replicas satisfy `1P:1D`. If both members use Stateful RIS, proceed with the remaining validation.

#### Replica and Execution Validation

RBG reconciliation first checks whether effective desired replicas represent a common integer group count: `roleReplicas / segment[role]` must equal the same non-negative integer `N` for every role. If so, `N` is the desired complete-group count. Misaligned replicas are rejected and reported; Segment does not silently floor them, publish residual addresses, or delete existing members. If `minSegments` exceeds `N`, report an unsatisfiable scheduling threshold.

A role whose replicas do not divide evenly, or different group counts across roles, indicates a ratio violation. RBG calculates residual instances for each role according to the [complete Segment invariants](#complete-segment-invariants), reports them through a condition, and stops the RBG reconciliation before any target or group description is written. This rule applies to initial creation, subsequent reconciliation of an active Segment, and adding or re-adding Segment to an existing RBG:

- Initial creation produces no member replica target and does not create a subset based on rounded-down group counts.
- An active Segment keeps the valid targets currently applied to each RIS and does not continue scaling from invalid desired replicas.
- Adding or re-adding Segment to an existing RBG blocks adoption without deleting or rearranging existing members or transferring Adapter control authority.

The validation runs at the start of RBG reconciliation, before the discovery ConfigMap, group descriptions, coordination parameters, or role targets are updated. A ratio violation therefore stops the entire RBG reconciliation: RBG reports the condition, writes no partial target, and leaves existing RIS objects and their specs unchanged. Existing instances keep running, and the separate RIS controllers continue maintaining them. After the user corrects desired replicas, RBG validates them again and resumes processing.

Policy admission does not validate RBG replica counts because a Policy can precede its RBG and external scaling systems may change replicas later. Once a Segment is effective, RBG admission and reconciliation reject a direct member-role replica update unless all member roles are updated together to the same integer Segment count. The ScalingAdapter controller likewise rejects single-role targets. These are hard alignment rules, not a silent floor, and must also be checked during RBG reconciliation.

### Group Assignment and Persistence

For an effective Segment, Stateful RIS membership must be dense: ordinals start at zero and contain no exclusions. Integer division then determines group membership:

```text
groupIndex = instanceOrdinal / segment[role]
ruleID = first 12 lowercase hexadecimal characters of SHA-256(ruleName)
segmentRuleLabelKey = rbg.workloads.x-k8s.io/segment-rule
segmentRuleLabelValue = <ruleID>
segmentLabelKey = rbg.workloads.x-k8s.io/segment
segmentLabelValue = <ruleID>-<groupIndex>
```

For example, with `2P:1D`, prefill-2 has group index `2 / 2 = 1`. All component Pods in the same RI share both the rule label and the group label. The fixed-length `ruleID` keeps every generated label value within Kubernetes limits and independent of the rule name's spelling or length. Admission must reject a 12-hex rule-ID collision within the same Policy.

RIS and RI controllers maintain identity through metadata synchronization independent of scaling and rolling updates. Reconciliation restores the desired value after users modify managed labels. Within the same effective configuration, recreating an RI or Pod with the same ordinal, or performing an in-place update, does not change group membership. Re-adoption calculates membership from the new configuration.

#### Passing Group Descriptions Through RIS Annotations

The RBG controller parses Policy and passes the group description for each role through a managed RIS annotation. The RIS controller reads this description rather than querying the full Policy or calculating cross-role strategies.

For example, for rule `kv-cache`, ratio `1P:2D`, and a current target of 3 groups, apply these fields to the decode RIS:

```yaml
metadata:
  annotations:
    # Example internal protocol name; the final name will be chosen during implementation.
    rbg.workloads.x-k8s.io/segment-config: |
      {
        "policyUID": "<policy-uid>",
        "ruleName": "kv-cache",
        "ruleID": "<12-hex-rule-id>",
        "instancesPerSegment": 2
      }
spec:
  replicas: 6
```

| Field | Purpose |
| --- | --- |
| `policyUID` | Distinguishes deletion and recreation of a Policy with the same name; does not imply permanent binding |
| `ruleName` | Identifies the rule and is used to derive the deterministic rule ID |
| `ruleID` | Deterministic short identity used in managed labels and selectors |
| `instancesPerSegment` | RIs per group for this role; in the same example, the prefill RIS receives `1` with a replica target of `3` |

The annotation is an internal transport protocol, not a user configuration interface. RBG maintains and removes it. A specific label value such as `rbg.workloads.x-k8s.io/segment=e7652f82cdee-0` belongs only on the corresponding RIs and their Pods, not on the shared RIS instance template, because instances in one RIS belong to different groups.

#### RI and Pod Label Synchronization

Group identity flows as follows. Metadata keys are abbreviated in the diagram:

```text
RIS.metadata.annotations[segment-config]
    ↓ RIS controller calculates groupIndex from the instance ordinal
RI.metadata.labels[segment-rule] = <ruleID>
RI.metadata.labels[segment] = <ruleID>-<groupIndex>
    ↓ RI controller synchronizes component Pod templates and existing Pods
Pod.metadata.labels[segment-rule] = <ruleID>
Pod.metadata.labels[segment] = <ruleID>-<groupIndex>
```

The full keys are `rbg.workloads.x-k8s.io/segment-config`, `rbg.workloads.x-k8s.io/segment-rule`, and `rbg.workloads.x-k8s.io/segment`, respectively.

RIS and RI controllers use the same desired-state synchronization logic during normal reconciliation for both new and existing members, not only in scaling paths:

| Current state | Synchronization action |
| --- | --- |
| A valid group identity exists | Add or update the managed group label |
| Group identity is confirmed withdrawn | Remove the managed group label |
| The object already matches the desired state | No action |
| Reading or parsing fails | Retry; do not treat it as absence of grouping |

- The RI controller directly patches existing Pod metadata and synchronizes templates for future creation or recreation. ControllerRevision calculation must ignore both managed Segment label keys. Adding, changing, or removing these labels must not create an update revision, change `updatedReplicas`, or enter the rolling-update path. Labels do not propagate through workload rollouts, image changes, or Pod recreation, and synchronization does not change container runtime state.
- Policy events trigger reconciliation of the corresponding RBG. Additions, updates, and removals of RIS annotations and RI labels trigger downstream synchronization. Event filters and metadata comparisons must preserve these changes.
- Patches change only differing managed fields and preserve other metadata. Temporary cross-layer inconsistency is allowed; failures are retried without waiting for natural Pod recreation.
- Each controller reconciles a given object serially. On each reconciliation and conflict retry, it recalculates desired state from the current configuration and ownership. Idempotent reconciliation handles cache delay; no separate stale-cleanup task, cleanup controller, or protection protocol is introduced.

Stateless RIS does not receive a valid group description. Adoption and withdrawal for existing RBGs reuse this path; see [adding and removing CoordinatedPolicy](#adding-and-removing-coordinatedpolicy).

### Complete Segment Invariants

Completeness constrains base-group targets and update batches, not atomic execution of Kubernetes objects. The following four terms distinguish desired, applied, and actual counts:

- Desired replicas: the final replica count submitted by a user or Adapter; it may include residual instances that cannot form a complete group, but such a desired state cannot be executed as a Segment replica target.
- Effective desired replicas: the final desired replica count after resolving control authority, used for ratio validation and group-count calculation.
- Base-group target: when effective desired replicas represent a common integer group count, the complete-group replica count that RBG applies to RIS. No target is generated or updated on a ratio violation.
- Actual created count: the number of RIs currently created by RIS, which may be below the base-group target.

The following three status measures serve different purposes:

| Measure | Evaluation |
| --- | --- |
| Groups represented by valid effective desired replicas | Divide each role's desired RI count by its ratio; all quotients must be the same non-negative integer, without using Ready counts |
| Actual complete groups | Check group identities to determine whether all RIs required by the ratio exist |
| Actual available groups | All members exist and each satisfies RIS availability conditions |

To diagnose a ratio violation, calculate the rounded-down group count and residual instances for each role as follows:

```text
completeSegments = min(floor(roleReplicas / segment[role]))
residual[role] = roleReplicas - completeSegments × segment[role]
aligned = every residual[role] is zero
```

`completeSegments` is a diagnostic value, not an execution target for invalid desired replicas. Effective desired replicas pass ratio validation only when every `residual[role]` is zero. Only then can `completeSegments` be used as the desired complete-group count `N`. Neither `1P:1D` nor `2P:0D` is a complete-group target for a `2P:1D` rule.

For example, `2P:1D` with desired replicas `P=5, D=2` produces `completeSegments=2`, `residual[P]=1`, and `residual[D]=0`. The residual instance makes ratio validation fail. RBG reports the reason through a condition and does not generate or update RIS replica targets. Initial creation must not create `4P, 2D` as a two-group subset. An active Segment keeps its current valid targets. Adding or re-adding Segment to an existing RBG [blocks adoption](#adding-a-policy-to-an-existing-rbg) without deleting or rearranging existing members.

Desired replicas that satisfy the ratio while actual members are missing do not violate the ratio. Preserve existing members and fill the gaps against the original target; do not scale down to the actual complete-group count. Single-role scaling requests for an effective Segment are also rejected and cannot be partially executed through this formula.

Members may be temporarily missing during creation, deletion, and updates; simultaneous startup or readiness is not required. Surge RIs do not change the base-group target. Each RIS creates and reclaims them independently, and extra instances may never form complete groups. Group labels do not establish service availability; see [maxSurge limits and RIS execution](#maxsurge-limits-and-ris-execution).

For an effective Segment, address discovery must enumerate only the aligned base-group target. It must not publish a residual identity that will not be created, and surge identities must not be published as base service members. The address table still is not a list of actual complete groups or available members.

The owner is the existing RBG discovery ConfigMap builder; no separate Segment discovery component is introduced. Discovery derives the address table from the committed base target — RBG role replicas before a child exists, otherwise the child RIS committed ordinals — and never from live Pods or RoleInstances. Stateful surge instances live at ordinals at or above `RIS.spec.replicas` and never change that field, so they are excluded by construction rather than by a surge-aware selector.

### Integration with Scaling

The RBG controller first verifies that effective desired replicas represent a common integer group count. It then calculates a common replica target in group-count space and multiplies by the ratio to produce RI targets for each role, rather than calculating and rounding each role's instance target independently. No RIS replica target is generated or updated when ratio validation fails. Policy `strategy.scaling` controls both initial creation and subsequent scale-out.

| Configuration | Behavior |
| --- | --- |
| No `strategy.scaling` | Apply the desired complete-group target directly, without implicitly limiting creation to one group per batch |
| A scaling strategy exists | Add at most `segBatchSize` groups per batch, defaulting to `1`; the final batch may be smaller |
| `progression: OrderScheduled` (default) | Start the next batch only after all expected Pods of every member in the current batch are scheduled |
| `progression: OrderReady` | Start the next batch only after all RIs in the current batch are Ready |

`segBatchSize` controls only newly added groups. It is not the desired total and does not limit scale-in, rolling updates, or surge. Neither `maxSkew` participates in Segment calculations. Scaling also does not read Gang minimums to raise its target; see [integration with Gang](#integration-with-gang) for configuration conflicts.

For a fixed target with `C < N`, scale-out follows:

```text
N = Complete Segment count represented by valid effective desired replicas
C = Current applied group-count target, not the Ready or actual complete-group count
B = segBatchSize, or 1 if omitted
Current batch has not satisfied progression: keep C and continue filling the current batch
No previous batch, or current batch has satisfied progression: nextTarget = min(N, C + B)
```

Initial creation from zero has no previous batch to wait for. With missing members, partially applied targets, or delayed status feedback, RBG continues converging the current batch. Retries must not admit an extra batch or lower the target to the actual complete-group count. Scaling requests for only some roles are rejected, with the reason reported in conditions on RBG and the relevant Adapter.

For example, a target of 5 groups with `segBatchSize: 2` progresses as `0 → 2 → 4 → 5`. If all Pods in the current batch are scheduled but one D is not Ready, `OrderScheduled` can proceed while `OrderReady` waits. At `2P:1D`, each additional group's common target is `2P, 1D`, not `1P, 1D`; objects can still be created at different times.

#### Applying Replica Targets

The RBG controller applies the number of RIs managed by each RIS, not the number of RIS objects:

```text
RBG controller: effective desired replicas + current state + Policy
    ↓ Calculate targetSegments for this reconciliation
    ↓ targetReplicas[role] = targetSegments × segment[role]
Each RIS.spec.replicas
    ↓ RIS controller creates or deletes RIs
    ↓ RI controller manages Pods
```

`targetReplicas` is an absolute replica total for the current reconciliation, not an increment. Batch progression must not write intermediate targets back to RBG `spec.roles[].replicas`. Group descriptions are applied separately; changing replicas alone does not generate group labels.

### ScalingAdapter

The Adapter receives the final desired group count and writes ratio-derived targets to RBG. RBG then applies RIS targets in batches according to the scaling strategy. Existing Adapters support only role targets; this design adds Segment targets, control handoff, and group-level status.

#### Segment Adapter Configuration and Automatic Creation

`CoordinatedPolicy.spec.policies[].strategy.scaling.scalingAdapter` reuses the existing configuration type. `enable` defaults to `false`; `labels` are added to the Adapter but cannot override controller-managed ownership and target identifiers. The configuration entry point for ordinary role Adapters is unchanged.

This example uses `1P:2D` and asks RBG to create one Adapter automatically for rule `kv-cache`:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: llm
spec:
  policies:
    - name: kv-cache
      roles: [prefill, decode]
      segment:
        prefill: 1
        decode: 2
      strategy:
        scaling:
          segBatchSize: 1
          progression: OrderReady
          scalingAdapter:
            enable: true
            labels:
              app.kubernetes.io/part-of: inference
```

After validating the rule and adoption conditions, RBG creates and maintains a rule-level Adapter with an OwnerReference to the current RBG. No Adapter is created when disabled; an unexpectedly missing object is recreated while its configuration remains effective. Automatic creation does not replace binding validation. Policy configuration adds no separate initial replica field. See [replica control authority and lifecycle](#replica-control-authority-and-lifecycle) for initialization and member role Adapters.

#### Targets and Replica Units

The following illustrates an automatically created Adapter after its desired count is set to 4 groups. The name is only an example:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroupScalingAdapter
metadata:
  name: kv-cache-scaling-adapter
spec:
  scaleTargetRef:
    name: llm
    segment: kv-cache
  replicas: 4
```

| Adapter field | Semantics |
| --- | --- |
| `spec.scaleTargetRef.name` | RBG name in the same namespace |
| `spec.scaleTargetRef.segment` | Rule name in the Policy sharing the RBG's name, not an individual group's label value |
| `spec.scaleTargetRef.role` | Optional role target; exactly one of `role` and `segment` must be set, and existing role-target units and behavior remain unchanged |
| `spec.replicas` | Final desired group count, an absolute total rather than an RI count, Pod count, or scaling increment |

Final replicas for all members are derived as `desiredReplicas[role] = spec.replicas × segment[role]`. The 4 groups in this example correspond to `P=4, D=8`. Single-role scaling requests are not executed; a condition reports the reason.

#### Replica Control Authority and Lifecycle

Existing replica control has two paths: the Adapter controller persistently writes to RBG, and the RBG controller's `applyRBGSAReplicasOverride` overrides the in-memory object with Adapter targets for the current reconciliation. Handoff must fence off both old write paths. Deleting the Adapter or rejecting new requests alone is insufficient.

Segment mode follows these rules:

- Each rule has only one effective Adapter replica writer. Member `role.replicas` values are projections of the group target, not independent scaling entry points. Old Adapters and other writers must not split the group target.
- A role Adapter is ineffective while its role belongs to an effective Segment, regardless of whether a Segment Adapter is enabled or exists. RBG maintains the desired Adapter set from effective configuration and adoption state, without rewriting the user's original role enablement flags.
- After adoption, RBG cleans up the member role Adapters it manages for the current RBG and stops recreating them. Cleanup validates ownership and targets and does not affect other RBGs or non-member roles.
- Object creation and deletion may be eventually consistent, but both old persistent writes and in-memory overrides must be fenced off before a new Adapter obtains write authority. Deletion delay must not keep old targets effective.
- Creation, binding, cleanup, and event association must recognize Segment targets; an empty `role` must not cause an Adapter to be deleted incorrectly. A Policy recreated under the same name is revalidated and rebound using its new object identity.

Without an explicit desired group count, initialize from current effective replicas: every member's `replicas / segment[role]` must be the same non-negative integer. Otherwise, report a blocked state without silently rounding. Validate an existing explicit target instead of unconditionally overwriting it. Initialization does not require all members to be Ready and does not replace adoption validation.

| Operation | Segment Adapter | Member role Adapters |
| --- | --- | --- |
| Delete only the Adapter object while configuration remains enabled | RBG recreates it | Do not resume creation or writes |
| Disable or remove Adapter configuration while retaining Segment | Stop writes and overrides, report it as disabled, and clean it up | Do not resume creation or writes |
| Remove Segment configuration, its rule, or its Policy so the role no longer belongs to an effective Segment | Stop writes and overrides, report an invalid target, and clean it up | Restore according to the original `scalingAdapter.enable` and normal binding and ownership conditions; do not create if disabled |

Invalid configuration does not mean a desired group count of zero. Do not delete workloads or clear submitted targets. Before restoring role Adapters, fence off both write paths of the old Segment Adapter. Initialize new role Adapters from the currently submitted RBG `role.replicas`, not historical values or intermediate RIS targets. Other effective strategies continue to apply; deleting the entire Policy is unnecessary.

Implementation must verify concurrency isolation in both handoff directions, deletion delays, retries, restarts, and rebinding. This adds neither historical-ratio restrictions nor a label-cleanup protocol.

#### Adapter and RBG Scaling Coordination Loop

Segment reuses the existing Adapter → RBG → RIS path, extending a single-role write to a whole-rule submission:

```text
User or external scaling system changes Adapter.spec.replicas, optionally through /scale
    ↓ ScalingAdapter controller validates the target, ratio, and control authority
    ↓ Submit all members' final desired replicas in one RBG update
RBG controller calculates targetSegments for this reconciliation from Policy
    ↓ Apply ratio-derived replicas to each RIS.spec.replicas
RIS / RI controllers execute and report status
    ↓ RBG controller determines whether to continue progressing
```

The Adapter controller processes the request in this order:

1. Verify that RBG, Policy, the rule, and its members exist and that the Adapter has write authority. Report invalid targets as blocked without submitting partial member targets.
2. Calculate every member's absolute replica count from the same ratio and group count. Check invalid values and multiplication overflow.
3. Submit all member targets in one RBG update protected by resource-version conflict detection, preserving unrelated roles and fields. On conflict, reread, revalidate, and recalculate; do not submit per role. Skip redundant writes when targets already match.
4. Synchronize required status. Adapter, Policy, and RBG changes all trigger reconciliation; binding, withdrawal, and status synchronization must not depend on another user replica change.

One RBG write guarantees only that final targets are submitted together, not atomic changes across RIS objects or Pods. Retries and restarts continue to write absolute targets rather than adding replicas again. Scale-in likewise uses complete-group targets, with Stateful RIS performing member operations.

For example, 2 current `1P:2D` groups satisfy progression, the Adapter requests 4 groups, and `segBatchSize: 1`:

| Stage | Adapter desired groups | RBG final desired P / D | Current RIS target P / D |
| --- | --- | --- | --- |
| Before scale-out | 2 | 2 / 4 | 2 / 4 |
| Submit new target and admit up to 3 groups | 4 | 4 / 8 | 3 / 6 |
| Progression satisfied; admit up to 4 groups | 4 | 4 / 8 | 4 / 8 |

While waiting, RBG and Adapter retain the final desired target; intermediate RIS targets must not overwrite it.

Implementation entry points are the [Adapter API](../../api/workloads/v1alpha2/rolebasedgroupscalingadapter_types.go), `Reconcile` / `updateRoleReplicas` in the [ScalingAdapter controller](../../internal/controller/workloads/rolebasedgroupscalingadapter_controller.go), and `applyRBGSAReplicasOverride`, Adapter lifecycle handling, and `handleCoordinationStrategies` in the [RBG controller](../../internal/controller/workloads/rolebasedgroup_controller.go). Extend the API, CRDs, and generated code together.

#### Scale Subresource and Metrics Scope

`/scale` continues to map Adapter `spec.replicas`, `status.replicas`, and `status.selector`. `status.readyReplicas` stays in Adapter status and is not mapped to the standard `/scale` replica fields.

| Adapter field | Group-level semantics | Request for 4 groups submitted; current batch target 3; actual Ready groups 2 |
| --- | --- | --- |
| `spec.replicas` | Final requested group count, never reduced to an intermediate target | `4` |
| `status.replicas` | Effective group count whose member targets were all successfully submitted to RBG, not the created or Ready count | `4` |
| `status.readyReplicas` | Actual complete groups with every member Ready | `2` |

If submission fails, `status.replicas` retains the previously submitted target. If the RBG write succeeds but the Adapter status write fails, later reconciliation or restart repairs status. Matching targets must not skip required status synchronization. Member-state changes update the Ready-group count independently.

Ready groups must be counted by group identity. If P is Ready only in Segment 0 and D only in Segment 1, they cannot be combined into one Ready group. Ready counts support observation or the corresponding scaling progression, not Gang resource admission.

`status.selector` remains the Pod label selector for `/scale`. For a Segment Adapter it must select all component Pods of the rule with a single AND selector:

```text
rbg.workloads.x-k8s.io/group-name=<rbg-name>,
rbg.workloads.x-k8s.io/segment-rule=<ruleID>
```

The selector must not select other RBGs, other Segment rules, non-member roles, or only one component. It selects Pods for observation and future metric integrations; group counting remains identity-based and separate. This KEP adds no business-metric collection, aggregation, or deduplication.

HPA usage remains an open question with these boundaries:

- In scope for this phase: group-level Adapter targets, basic `/scale` mapping, and group-level status.
- Not promised in this phase: HPA configuration, metric selection, general-purpose Pod selectors, or business-metric collection, aggregation, and deduplication.
- To be determined in an HPA usage design: whether to use `External + AverageValue` or another integration approach.

### Integration with Gang

When Segment is declared, Gang counts complete Segments instead of RoleInstances:

- `minSegments: n` allows scheduling admission only when schedulable cluster resources meet the requirements of at least `n` complete Segments, covering every Pod of every RoleInstance in those Segments.
- The scheduler evaluates sufficiency using Pod requests and scheduling constraints, not cluster resource totals alone. Insufficient resources cause waiting; member readiness is not an admission criterion.
- Without scaling, declaring Gang while omitting `minSegments` requires all desired complete Segments. Configuring both scaling and Gang requires an explicit value.
- `minSegments` and `minReplicas` are mutually exclusive within one rule. Rules without Segment continue to use `minReplicas`.
- Gang handles only resource admission and scheduling release. It neither defines Segment structure nor guarantees simultaneous startup or readiness.

#### Volcano SubGroup Mapping

Segment retains the existing single PodGroup per RBG but changes the Volcano subGroup from one RoleInstance to one complete Segment. Each Segment Gang rule generates one SubGroupPolicy:

```text
labelSelector:
  rbg.workloads.x-k8s.io/group-name=<rbg-name>
  rbg.workloads.x-k8s.io/segment-rule=<ruleID>
matchLabelKeys = [rbg.workloads.x-k8s.io/segment]
subGroupSize   = Σ(segment[role] × ComputeSubGroupSize(role))
minSubGroups   = minSegments
minMember      = minSegments × subGroupSize
```

`matchLabelKeys` contains only `segment`, not `role-instance-name`. Pods from different roles and RIs therefore join one subGroup when they share a Segment label. P and D instances from different Segments cannot satisfy the threshold by meeting independent per-role counts.

For `1P:2D`, one Pod per RI, and `minSegments: 2`, one subGroup contains `1P + 2D`, with `subGroupSize=3`, `minSubGroups=2`, and `minMember=6`. Multiple Gang rules share the RBG PodGroup. Each Segment rule contributes one SubGroupPolicy and adds its threshold to `minMember`; the `segment-rule` selector keeps rules separate. When scaling is absent and `minSegments` is omitted, resolve the threshold to all desired complete groups before generating the policy.

scheduler-plugins provides only a Pod-count threshold and cannot express this complete-Segment constraint. A Segment rule with Gang requires Volcano `subGroupPolicy`. Admission checks the configured scheduler, reconciliation checks the installed CRD, and neither path degrades to a Pod-count or per-role threshold.

#### Configuration Validation with Scaling

Segment scaling and Gang execute independently. Gang does not change `segBatchSize` or the scaling target, and scaling does not lower the Gang threshold. Configuration validation prevents an insufficient first batch instead of coupling their execution parameters.

Let `B` be the effective `segBatchSize`, defaulting to 1, and `G` be explicit `minSegments`. A Segment rule with both scaling and Gang must satisfy:

```text
B >= G
```

The next batch can then supply at least G groups during initial creation or scale-out from below the threshold. A later final batch may be smaller than G because the applied target has already crossed the threshold; not every batch must be at least G.

For example, when creating 4 groups from zero:

| Configuration | Behavior |
| --- | --- |
| `segBatchSize: 2`, `minSegments: 2` | Valid. Create 2 groups initially, then create the remaining 2 after resource admission and progression are satisfied. |
| `segBatchSize: 1`, `minSegments: 2` | Rejected by the Policy webhook because the first batch cannot satisfy the threshold. |
| Scaling with `segBatchSize: 1` and omitted `minSegments` | Rejected by the Policy webhook because combining scaling and Gang requires an explicit threshold. |
| No scaling and omitted `minSegments` | Valid. Apply all 4 groups directly, with Gang requiring all 4 by default. |

The Policy webhook checks combinations determined by the Policy itself. RBG reconciliation also checks `minSegments <= desired complete groups`, ratio validity, scheduler capability, and old configurations that bypass admission. If an Adapter or RBG update later creates a conflict, report `GangConfigured=False` through the existing incompatible-Gang path without increasing desired replicas or raising intermediate targets. Insufficient resources with a valid configuration is not a configuration error, and `GangConfigured` does not replace Pod or PodGroup scheduling status.

The incompatible-Gang path is also the user-visible outcome for a scale-in below `minSegments`: RBG reports the condition, keeps the last valid group target applied to RIS, and does not lower role replica targets. The scale-in stays blocked until the user corrects the desired group count or the threshold, and the controller requeues on its existing incompatible-Gang interval. Correcting the configuration resumes normal role reconciliation.

The webhook check `B >= G` is intentionally static and stateless. It also rejects `B < G` for an RBG that already runs at least `G` complete groups, where the next batch would be at or above the threshold as soon as it lands. This couples scale-out batch granularity to the gang threshold in steady state: for example, a mature five-group workload with `minSegments: 2` must add at least two groups per scaling progression instead of one group at a time. This is a documented alpha tradeoff that keeps Policy admission deterministic without reading RBG state. Moving the check to reconciliation, where the applied group count is known, remains a possible follow-up and is not part of this proposal.

The existing non-Segment path [raises the scaling target to the Gang minimum](../../internal/controller/workloads/rolebasedgroup_controller.go). Segment does not reuse that coupling; non-Segment behavior remains unchanged.

### Integration with Rolling Update

When Segment and `strategy.rollingUpdate` are configured, members of the same group are admitted in the same batch; a subset of roles is not assigned to a separate batch. Members can execute, start, and become Ready at different times, with mixed versions allowed during the update.

All fields in this table are under `CoordinatedPolicy.spec.policies[].strategy.rollingUpdate`:

| Field | Segment semantics |
| --- | --- |
| `partition` | Number of groups retained without updates; `1` retains Segment 0 while later groups may enter the update scope |
| `maxUnavailable` | When the resolved budget is positive, a group-level budget shared across roles at the RBG layer, with no repeated RI concurrency limit inside admitted groups; a zero budget works with surge; omission retains RIS settings |
| `maxSurge` | Extra group-capacity limit, projected by quota into RI limits for each RIS; does not promise complete standby groups |
| `maxSkew` | Ignored; the creation batch field `scaling.segBatchSize` also does not participate in update admission |

At the RBG layer, percentages for the first three fields are resolved against the desired complete-group count. For RIS, `partition` and `maxSurge` are projected by ratio into instance parameters. `maxUnavailable` sets the RIS execution window according to whether the group budget is zero rather than overriding it with the original value; see [execution windows](#policy-overrides-and-ris-execution-windows).

Percentage resolution is deterministic:

| Field | Group-level rounding |
| --- | --- |
| `partition` | Round down to a whole retained Segment |
| `maxUnavailable` | Round down to a whole unavailable Segment |
| `maxSurge` | Round up to a whole extra Segment |

For example, with 3 desired groups, `25%` resolves to 0 retained groups, 0 unavailable groups, and 1 extra group.

Distinguish these semantics:

- `maxUnavailable` is the user-declared group-level budget. When the resolved budget is positive, RBG counts Segments and RIS receives `100%`, adding no RI-count limit inside admitted groups. When it is zero, RIS receives `0` and uses available surge to avoid increasing unavailable instances. If omitted, no default group budget is generated and existing RIS settings are retained.
- `partition` has two meanings. As user configuration, it counts Segments retained without updates. As an RBG-applied value, it projects the current admission result into a RIS boundary. It is not a monotonically decreasing ordinal as in a conventional StatefulSet partition workflow; target version changes can tighten or relax it.

For example, at `2P:1D`, `partition: 1` retains `2P + 1D`, not one RI per role.

#### RBG and RIS Update Coordination Loop

In the existing non-Segment path, RBG calculates a dynamic partition from each role's update progress and `maxSkew`, then takes the stricter of that boundary and the user partition. Segment reuses the parameter-application path but has RBG calculate a common boundary and budget from group identity, target versions, and member states.

```text
RBG controller: member states + Policy
    ↓ Count unavailable and in-flight groups and calculate a common update boundary
    ↓ Project partition, set maxUnavailable from the group budget, and project maxSurge
WorkloadReconciler: write each RIS.spec.updateStrategy
    ↓ RIS controller updates RIs within partition, execution window, and surge conditions
    ↓ RI controller performs Pod updates; member states are reported to RBG
RBG controller: recalculate the boundary on the next reconciliation
```

An RIS must not cross the common boundary simply because its local budget has capacity. Without surge and with `maxUnavailable: 1`, if D is still updating in Segment 1, P must not proceed to Segment 0 after recovering, which would make two groups unavailable.

RBG must read member identities and states. Comparing aggregate per-role replica counts or relying on RIS local windows is insufficient. It must not communicate group-level occupancy by fabricating unhealthy states for healthy members. RIS objects may receive parameters at different times, but new templates and partitions must enforce the same group-level budget.

#### Member Availability and Reusing Budget Capacity

Member availability follows RIS semantics, including in-place update completion, health, and `minReadySeconds`. RBG aggregates by group identity. A group is unavailable if any member required by the ratio is missing or unavailable:

```text
P and D in Segment 0 are unavailable → 1 unavailable group
P in Segment 0 and D in Segment 1 are unavailable → 2 unavailable groups
```

An increase in `updatedReplicas` or momentary readiness is insufficient to release budget.

An admitted Segment releases its in-flight update occupancy only after every member needing an update has completed its current target version and every required member is available according to RIS semantics. Completing just one role, or leaving any update within the group unexecuted, must not release occupancy early.

With explicit `maxUnavailable`, RBG uses a group-level window that can admit replacements as individual groups finish, rather than waiting for the entire batch. The following formula describes the simplified window without surge. It is not a fixed admission gate that prevents candidate groups from entering the update scope when surge is enabled:

```text
U = Set of currently unavailable base Segments, including external failures
I = Set of admitted base Segments that have not completed and recovered availability
B = Policy strategy.rollingUpdate.maxUnavailable resolved against desired complete groups
remainingBudget = max(B - |U ∪ I|, 0)
```

Count a group only once if it belongs to both `U` and `I`. Within the remaining budget, RBG admits complete groups that meet partition and other conditions. New admission occupies budget immediately, preventing duplicate admission due to delayed status. External failures also count in `U` and may make actual unavailability exceed the budget; the controller must not actively expand the unavailable-group set further in that state.

With surge, RBG uses the following deterministic admission rule:

```text
availableSurge[role] = extra RIs of that role that exist, use the target version,
                       are healthy, and are non-terminating
surgeEligible(group) = availableSurge[role] >= segment[role] for every member role

admit(group) if partition permits and either:
    |U ∪ I_loss| + 1 <= B
    or surgeEligible(group)
```

`I_loss` contains admitted, incomplete groups that already have an unavailable base member or no longer retain enough target-version surge in every role to continue without base availability loss. Safe surge-backed in-flight groups remain tracked as in-flight but do not consume the no-unavailability budget. If surge capacity disappears or a base member becomes unavailable, the group moves to `I_loss` immediately. A group is never admitted merely because one role has spare surge; every member role must have at least its Segment quota.

For example, without surge and with `maxUnavailable: 2`, assuming partition and other progression conditions are met:

| Deduplicated count of unavailable and in-flight Segments | Maximum additional groups admitted |
| --- | --- |
| 0 | 2 |
| 1 | 1 |
| 2 or more | 0 |

After one group completes and becomes available, the next reconciliation may admit one more without waiting for the other group. A group blocked by a slow or failing member retains only its own occupancy. Other eligible groups can proceed while budget remains; one non-Ready group does not freeze the entire rule or RBG. The two-group window is an admission window. The same `maxUnavailable` value does not limit RI concurrency within these groups, but simultaneous startup or readiness across RIS objects is still not guaranteed.

#### Policy Overrides and RIS Execution Windows

RBG controls which Segments may enter an update, while RIS updates RIs within the common boundary. Once the group budget permits an entire Segment to become temporarily unavailable, the same value does not also throttle RI rotation within that group. Without a configured group budget, the existing RIS local window remains in effect.

Segment parameter overrides follow these rules:

| Configuration | RBG group-level control | RIS execution window |
| --- | --- | --- |
| Policy explicitly sets `maxUnavailable`, with resolved budget `B > 0` | Aggregate unavailable and in-flight groups; use the window above without surge, and retain the contribution of extra available capacity when surge is enabled | Ignore the role value and apply `100%`; admitted RIs within the common partition have no additional RI-count limit |
| Policy explicitly sets `maxUnavailable`, with resolved budget `B = 0` | Valid only with positive effective `maxSurge`: allow extra-capacity preparation and advance using group state. A zero budget without surge is rejected by admission or reported by reconciliation | Ignore the role value and apply `0`; only actually available surge increases the execution budget |
| Policy omits `maxUnavailable` | Do not add an implicit default group-level budget; continue using Segment boundaries and other configured coordination conditions | Retain role configuration, or RIS defaults if the role also omits it |

With `B > 0`, the Policy integer or percentage is used only for RBG's group-level budget and is not executed again as an RI budget. Applying `100%` removes additional local RIS throttling; it does not multiply the original Policy value by the ratio. Each RIS must still respect the common partition. With `B = 0`, applying `100%` would incorrectly create unavailable instances without available surge. RBG does not infer a default group budget from local RIS values.

This zero-budget rule deliberately aligns with existing Stateful RIS resolution. RIS floors a resolved `maxUnavailable` to `1` only when `maxSurge` is zero; the admission and reconciliation rule above prevents Segment from ever writing a zero budget into that case. With positive `maxSurge`, the literal `0` written by RBG survives existing RIS logic unchanged, and updates consume only actually available surge. Non-Segment rules keep the existing floor behavior.

For example, without surge and with initially healthy `1P:2D` members, Policy `maxUnavailable: 1` allows at most one Segment to occupy RBG's update budget. Both P and D RIS receive `100%`. Once the group is admitted, P, D0, and D1 may all enter the update; the two D instances need not rotate serially. The whole group may become temporarily unavailable while occupying only one group-budget slot. Startup and readiness times may still differ. RBG releases occupancy and admits another group only after the whole group reaches the target version and becomes available.

With `B = 0` and positive `maxSurge`, both P and D RIS receive `0`. Each RIS first prepares extra instances through its existing algorithm, and only actually available surge can support base RI updates. The zero-budget-without-surge combination is rejected or reported before overrides are written, so healthy base RIs never begin an update that adds unavailable instances merely because the group entered a candidate update scope.

#### maxSurge Limits and RIS Execution

Policy supplies configuration, RBG calculates and applies overrides, and RIS executes normally. No actual surge target field or new surge-control annotation is introduced.

| Rule type | Policy `maxSurge` | Behavior applied to RIS |
| --- | --- | --- |
| Non-Segment | Explicitly configured | Override role `maxSurge` with the original value; each RIS resolves percentages against its own replica count |
| Non-Segment | Omitted | Retain role configuration and existing defaults |
| Segment | Explicitly configured | Ignore role configuration; resolve extra Segment capacity first, then multiply by each role's quota to obtain an integer RI limit |
| Segment | Omitted | Explicitly apply `0`, without falling back to role configuration |

In Segment mode:

```text
S = Extra Segment capacity limit resolved from Policy strategy.rollingUpdate.maxSurge
    Integer: use directly; percentage: ceil(desired complete Segment count × percentage)
    Omitted: 0
risMaxSurge[role] = S × segment[role]
```

Percentages retain the round-up rule for `maxSurge`, but are resolved at the Segment layer before projecting integer values. Do not pass the original percentage to each RIS for independent rounding. For example, 3 desired `1P:2D` Segments and `maxSurge: "25%"` produce `S=1`: P RIS receives `1` and D RIS receives `2`. `maxUnavailable` follows a different rule: both RIS objects receive `100%` when the group budget is positive and `0` when it is zero, rather than the original Policy value.

Overrides change only the applied execution parameters, not the user's role configuration in RBG. While Segment remains effective, removing Policy `maxSurge` must apply `0` and withdraw the previous positive value; an absent value must not skip the override. If the entire Policy or Segment is withdrawn, recalculate from current role configuration and remaining effective strategies as described in the lifecycle section.

RIS receives a limit, not an extra-replica target that must be reached. Each RIS retains its existing algorithms for actual surge count, retention, and reclamation. Surge does not increase `RIS.spec.replicas`. During an update with no existing valid surge instances, current Stateful RIS calculates:

```text
healthyOld = Number of healthy, non-terminating old-version RIs in [partition, replicas)
activeSurge = min(maxSurge, max(healthyOld - maxUnavailable, 0))
```

In this formula, `maxUnavailable` is the execution value received by RIS, not the original group-level Policy value. Existing valid surge instances invoke additional local retention logic; the simplified formula cannot be used alone. With `replicas=4, partition=0, maxUnavailable=2, maxSurge=2` and 4 healthy old instances, the extra target is `2`. With `replicas=3, partition=2, maxUnavailable=1, maxSurge=1` and only the admitted old instance healthy, it is `0`. Both respect the limit; neither requires fully using surge capacity in every reconciliation. With a positive Segment group budget, RIS receives `100%`, so this simplified formula does not require additional surge creation. With a zero group budget, RIS receives `0` and can proceed through available surge.

Actual extra counts across RIS objects may not satisfy the Segment ratio, and reclamation can occur at different times. RBG does not force temporary groups to become complete or wait for a cross-role reclamation condition. Surge RIs reuse ordinary RI creation and group-label synchronization, but labels do not mean a temporary group is complete or serviceable. This proposal does not guarantee creation, retention, or coordinated lifecycle of complete standby Segments.

The combination of surge and `maxUnavailable` follows existing execution mechanisms. There is no Segment-specific rule that forbids updates with zero unavailability budget. Segment changes coordination units, membership, and parameter projection, not RIS surge creation, availability budgeting, or reclamation algorithms. Current Stateful RIS uses the following local execution budget:

```text
availableSurge = Number of extra RIs in this RIS that exist, use the target version, are healthy, and are non-terminating
effectiveExecutionBudget = maxUnavailable + availableSurge
```

Only actual available extra instances contribute, not the configured `maxSurge` limit or newly created instances that are not yet available. For healthy old instances with `maxUnavailable: 0, maxSurge > 0`, RIS creates surge within the admitted update scope using its existing algorithm. After those extra instances become available, it uses their capacity to update base instances and eventually reclaims them according to local conditions. Existing handling of failed instances, pauses, user partitions, and other restrictions still applies; configuring surge does not guarantee progress from every state.

RBG continues to calculate a common Segment update boundary and aggregate unavailable and in-flight state; RIS must not cross that boundary. Entering a candidate update scope does not immediately authorize loss of available capacity, which remains constrained by the RIS execution budget. Therefore, the no-surge formula `max(B - |U ∪ I|, 0)` must not become a universal prerequisite for lowering partition, blocking even surge preparation when `B=0`.

The preceding rule is the required interaction between group state, in-flight occupancy, and per-role extra capacity. Surge readiness in one role does not make a group eligible, and per-role available RI counts are never combined into a phantom complete group. The rule adds neither a complete standby-group guarantee nor a cross-role reclamation protocol.

#### Generating Partitions by Segment

For supported Stateful RIS with validated dense ordinal grouping, normal updates should first reuse the existing partition to express the common admission boundary. A Segment update-list annotation is not required in advance:

```text
segmentBoundary = max(userSegmentPartition, dynamicSegmentBoundary)
risPartition[role] = segmentBoundary × segment[role]
```

`userSegmentPartition` is the user-configured retained count resolved in Segment units. `dynamicSegmentBoundary` is the boundary RBG calculates from current state and coordination strategies for the current reconciliation. Projection produces instance-level execution parameters without changing the Policy field's Segment unit.

For example, assume fixed effective desired replicas of `3P, 6D`, ratio `1P:2D`, a fixed target version, no surge or other member failures, and user configuration `partition: 0, maxUnavailable: 1`. Group-by-group progression is:

| Stage | Segment boundary | P RIS partition | D RIS partition | Newly admitted members |
| --- | --- | --- | --- | --- |
| No admission yet | 3 | 3 | 6 | None |
| Admit Segment 2 | 2 | 2 | 4 | P-2, D-4, D-5 |
| Admit Segment 1 | 1 | 1 | 2 | P-1, D-2, D-3 |
| Admit Segment 0 | 0 | 0 | 0 | P-0, D-0, D-1 |

In this example, lower the boundary to admit the next group only after every member needing an update in the current group has reached its target version and recovered availability. If P completes before D, P RIS's partition remains unchanged; it must not enter the next group early. With user `partition: 1`, stop at Segment boundary 1 and retain Segment 0.

Partition conveys the admission result; it does not replace group-level budget calculation and need not decrease monotonically. Without surge, do not relax it when budget is exhausted. A target version change may require tightening it.

The normal Stateful path should first reconstruct its window from persisted RIS partitions, target versions, and member states, without assuming a new persistent admission list. Partial parameter application, delayed status, restarts, pauses, scaling during updates, and interaction with surge windows require implementation verification. Stateless is out of scope; non-Segment partition behavior is unchanged.

#### Applying New Templates and Partitions After a Target Version Change

When the target changes from B to C or rolls back, continue using the existing group-level budget:

- Retain deduplicated unavailable and in-flight occupancy `U ∪ I`; do not clear or release it early because the target changed.
- Recalculate partition from the new target, member states, and remaining budget while preserving user partition restrictions. Tighten it if necessary; retaining the old value alone does not guarantee budget compliance.
- An exhausted budget may block new groups. Continue if budget remains and other conditions are met. A bad version does not change this rule, introduce a special recovery mechanism, or require B to succeed first.
- Without surge, an exhausted group budget prevents further admission of healthy RIs. Members within partition that meet existing stable-unhealthy conditions may still be repaired. Repair must not cross the boundary or bypass the group-level budget.

For example, without surge and with `maxUnavailable: 1`, Segment 2 has completed B while Segment 1's P is Ready and D is not. The target then changes to C. Segment 1 still occupies the entire budget. RBG must recalculate and apply a boundary that prevents P from updating Segment 2 merely because its local budget is free, avoiding two simultaneously unavailable groups.

The interaction between new templates and partitions still requires implementation verification with budgets of 1 and greater than 1, partially applied RIS parameters, delayed status, and restarts. Preserve existing surge boundaries: do not use the no-surge window to block surge preparation, add an arbitrary-group selection protocol, or fabricate unhealthy states.

### Composition with Topology

This KEP does not design a topology API. It defines only composition semantics:

> When topology constraints apply to a Segment rule, the placement unit is a complete Segment: all Pods in the same Segment converge on the same topology domain.

Segment provides stable subgroup identity. The topology design defines tiers, packs, required/preferred behavior, and scheduler translation.

### Adding and Removing CoordinatedPolicy

Policy is coordination configuration that can be enabled and withdrawn; it is not permanently bound to RBG. Matching names are a lookup rule, not ownership or lifecycle binding. After deletion or recreation of a same-name Policy, RBG revalidates and adopts the new object identity. Policy creation or deletion, and adding or removing Segment configuration, must trigger the corresponding RBG reconciliation even if replicas and workload templates are unchanged. Other effective rules remain active when only some rules change.

#### Adding a Policy to an Existing RBG

RBG first performs [member mode validation](#member-mode-validation), then checks current effective replicas, the ratio, and existing members. If an Adapter is enabled, [replica control handoff](#replica-control-authority-and-lifecycle) must also be satisfied. Validation failure only blocks adoption; it does not convert modes or delete instances.

| Current state | RBG handling |
| --- | --- |
| Effective desired replicas do not satisfy the ratio | Block this Segment adoption and report the reason. Do not silently round, rearrange, or delete existing members because of the new Policy. |
| Effective desired replicas satisfy the ratio, but some members are missing | Adopt identities of existing members first, including members of incomplete groups. Preserve the effective desired target and fill missing members through existing creation and scheduling mechanisms. Do not wait for all members to exist or become Ready, or scale down because fewer complete groups currently exist. |
| Both effective desired replicas and existing members satisfy the ratio | Keep existing members running and synchronize descriptions and labels, without scaling or restarting solely for adoption. |

Satisfying the ratio requires every role's `replicas / segment[role]` to be the same non-negative integer, not merely individually divisible. After validation:

1. RBG supplies RIS group descriptions and execution parameters. RIS and RI adopt existing members through [label synchronization](#ri-and-pod-label-synchronization), including running RIs and Pods in incomplete groups.
2. Preserve the effective desired target and the applied target, and fill missing members through the existing creation path. Scaling progression and scheduling conditions still apply; a complete member set is not a prerequisite for adoption.
3. Count actual completeness and availability by group. An incomplete group cannot be considered Ready, and its update-budget occupancy must not be ignored.

Identity synchronization patches only managed metadata; it does not trigger a workload rollout or Pod recreation. Labeling a running Pod neither reruns Gang admission nor guarantees automatic migration to a new topology domain.

For example, Stateful RIS uses validated dense ordinals with ratio `1P:1D`, desired replicas `2P, 2D`, and actual members `P0, P1, D0`:

```text
Segment 0: P0, D0 → Complete membership; availability is evaluated separately
Segment 1: P1, missing D1 → Label P1 with its group identity first and retain it
Later: RIS creates D1 against the target and assigns Segment 1 identity
```

Do not reduce P's target to 1 because only one complete group exists, or delete P1 before recreating it.

Adoption can complete once group descriptions have been applied and existing RI and Pod identities match the current rule. It need not wait for all desired members to be created or Ready. Missing members join later through the same path. Neither temporary gaps during adoption nor during normal operation trigger cleanup that deletes residual members before filling gaps.

#### Withdrawing Coordination Constraints After Policy Deletion

RBG stops using the deleted Policy for new coordination decisions, but merely returning no coordination result is insufficient. Old constraints have been written to RIS and other managed objects and must be withdrawn or recalculated from current effective configuration in later reconciliations.

| Target | Responsibilities of RBG and related controllers |
| --- | --- |
| Effective strategies and execution targets | After deleting the entire Policy, calculate from ordinary role configuration. If only Segment configuration or some rules are removed, use the remaining effective Policy. Do not reuse old group targets or update-admission results. |
| RIS group description | Remove the old Segment annotation so the same downstream desired-state synchronization removes managed group labels from RIs, existing Pods, and future Pod templates. Stop generating or repairing identity from the old description. |
| RIS replicas and update parameters | Reapply managed fields such as `replicas`, `partition`, `maxUnavailable`, and `maxSurge` from current effective replica targets, role configuration, and remaining strategies. Restore the values currently required, rather than setting everything to zero or retaining old overrides when no new coordination parameters are produced. After Segment withdrawal, stop forcing `maxSurge: 0`; restore the remaining Policy or role configuration. |
| Gang and other managed constraints | Recalculate the relevant PodGroup, template scheduling fields, and other applied constraints. Remove portions belonging to invalid rules while retaining other effective configuration; do not unconditionally delete shared objects. |
| Segment Adapter | Stop RBG writes and replica overrides based on the invalid rule, report an invalid target, and clean up the managed Adapter. When members no longer belong to an effective Segment, restore ordinary Adapters according to each role's `scalingAdapter.enable`, initialize from current RBG targets, and fence off old writes during handoff. |

Deleting Policy does not delete RBG or workloads, reset submitted desired replicas to zero, or restart existing members merely to remove group identity. After old execution restrictions are withdrawn, ordinary replica targets or update configuration may drive normal scaling or updates. It is therefore not a guarantee that all existing Pods remain unchanged indefinitely.

Withdrawal has the following boundaries:

- Labels are cleaned asynchronously through the original synchronization path. Cleanup neither blocks Policy deletion or the end of coordination nor restarts Pods. After failures, retries, or restarts, controllers continue converging from current configuration without a separate cleanup task.
- Effective Policy and group descriptions determine whether a strategy applies. Residual labels cannot keep an old strategy active. External consumers that inspect only Pod labels accept synchronization delay; detecting policy invalidation also requires observing Policy and group descriptions and validating ownership.
- In-flight scaling and updates need not finish old groups or wait for readiness before withdrawal, and completed operations are not automatically rolled back. RIS continues tracking real unavailability; RBG does not reuse invalid group budgets or admission results.
- Cross-object operations are eventually consistent. Deletion does not immediately cancel all already-issued operations. A Policy read failure is not proof of absence and must not trigger withdrawal.

For example, delete Policy after P in a group finishes updating but before D finishes. Do not roll back P or wait for D before withdrawing constraints. RBG applies update parameters from current role configuration, and RIS continues execution. If the controller restarts after only P's RIS configuration is updated, the next reconciliation continues converging every RIS to current configuration. This is a verification scenario for partial application, retries, and restart recovery.

#### Re-adding Configuration and Renaming Rules

Policy is pluggable coordination. Ratio immutability restricts only in-place changes to a declared Segment, not historical ratios. After withdrawal, re-addition may use the same or a different ratio and reuses adoption for existing RBGs. No ratio history is stored.

| Operation | Handling |
| --- | --- |
| Re-add the same ratio | Revalidate and adopt using current replicas, rules, and control authority, without reusing old admission results. |
| Re-add a different ratio after withdrawal | Validate current effective desired replicas against the new ratio. Adopt if all roles represent a common group count; otherwise block, without automatic rounding or replica adjustment. |
| Rename a rule | Allowed only when the Segment map is unchanged. Revalidate and synchronize descriptions and labels against the current rule, retaining member pairing; the rule ID and labels converge through the same synchronization logic. A single update that changes both rule name and ratio is rejected and must be split into withdrawal followed by addition. |

Deleting and recreating a same-name Policy changes its UID. Revalidate and adopt the new Policy instance and update RIS descriptions rather than reusing old bindings or admission results. `policyUID` distinguishes object identity; it does not permanently freeze coordination relationships. Residual labels are not evidence of completed adoption.

Renaming a rule also has a gang convergence boundary. The new rule name derives a new `ruleID`, so every managed `segment` and `segment-rule` label value is re-keyed fleet-wide, and the Volcano `SubGroupPolicy` is regenerated with the new `segment-rule` selector. Label synchronization is asynchronous: until every Pod converges, Pods still carrying the old label value no longer match the regenerated subGroup selector and are not counted toward `minSubGroups` or `minMember`. Gang dispatch of pending Pods may therefore stall transiently during the rename window. The window is bounded by the existing metadata synchronization path: it requires no Pod restart or PodGroup deletion, and dispatch becomes satisfiable again once the labels converge. Rename under an active PodGroup is not atomic; users should expect this transient gang accounting window.

Re-adoption may change member pairing and does not guarantee seamless application-state or routing migration. Identity synchronization does not restart or delete members, and migration to a separate workload is not required. Controllers calculate desired state directly from current configuration without waiting for all old labels to be removed. Convergence after retries and restarts still requires verification.

### Test Plan

The following are acceptance requirements for the implementation and have not been executed. Unit tests validate calculations and rules; integration tests validate cross-controller convergence; end-to-end tests validate actual cluster behavior.

#### Prerequisite Testing Updates

- Keep existing non-Segment CoordinatedPolicy scaling tests passing, preserving `maxSkew` and progression behavior.
- Keep non-Segment Stateless RIS creation, scaling, and rolling-update tests passing; do not turn the combination restriction into a global Stateless ban.
- Keep existing non-Segment Gang tests passing, including raising scaling targets to Gang minimums. Add separate assertions that Segment does not raise targets and that validation rejects conflicting configurations.
- Keep existing RIS surge unit and end-to-end tests passing. Adding Policy fields must not change the actual RIS surge algorithm.
- Add regression tests for Segment with scaling to ensure initial creation does not fall back to instance-level behavior.

#### Unit Tests

For each capability below, cover valid inputs, invalid inputs, and boundary states.

##### API Validation

- Reject `segment` if its keys differ from `roles`, any value is at most zero, or it contains only one role.
- Reject duplicate entries in `roles`, even when the deduplicated key set matches `segment`.
- Reject Segment members appearing in another Segment or ordinary rule, including different strategy domains. Cover creation, updates, and rule-order changes.
- Accept related roles combined into one Segment rule with a complete ratio. Preserve existing overlap handling for ordinary rules that do not involve Segment members.
- If the related RBG exists, reject adding Segment to Stateless RIS members. Cover all-Stateless and mixed Stateful/Stateless membership regardless of member order.
- Reject non-RoleInstanceSet, unknown, Deployment, StatefulSet, and LeaderWorkerSet backends. Accept only native Stateful RoleInstanceSet members.
- Reject user `excluded-ordinals` and a non-empty child `committed-skip-ordinals` annotation for Segment members; preserve non-Segment selective-membership behavior.
- Reject changing an existing Segment member to Stateless. Accept all-Stateful RIS membership when other conditions hold; preserve non-Segment Stateless behavior.
- Reject simultaneous `minSegments` and `minReplicas`, invalid `minSegments`, or use of `minSegments` without `segment`.
- Reject explicit zero, negative, or percentage `segBatchSize`, or its use without `segment`. Accept positive integers, preserve the field in the API, CRDs, and generated code, and default Segment scaling to 1.
- When Segment combines scaling and Gang, reject omitted `minSegments`, `segBatchSize: 1, minSegments: 2`, and an omitted batch size with threshold 2. Accept combinations satisfying `B >= G`, such as `segBatchSize: 2, minSegments: 2`.
- Reject Segment Gang with scheduler-plugins. Allow Policy admission with Volcano, leaving actual `subGroupPolicy` capability validation to reconciliation.
- Reject changes to the values or key set of a declared `segment`.
- Reject a single update that changes both a rule name and its Segment map; require withdrawal and a separate addition.
- Reject rule-ID collisions and verify generated label values remain valid Kubernetes label values for long rule names and large group indices.
- Accept a Segment rolling-update configuration whose effective group-level `maxUnavailable` and `maxSurge` both resolve to zero; without surge, do not start updates that increase unavailable instances.
- Verify `AdapterScaleTargetRef.role` is optional in the updated schema and that exactly one of `role` and `segment` is required.
- Allow strategy parameter changes with an unchanged ratio. Adding, deleting, and re-adding Segment follow adoption and withdrawal rules, without rejecting new configuration based on ratio history.
- Preserve integer, percentage, explicit zero, and omitted Policy `maxSurge` values in the API, CRDs, and generated code. Do not globally default omission to `0`, losing non-Segment fallback semantics.

##### Group Assignment

- Map instance ordinals to group indices using integer division; all component Pods in one RI share its identity.
- Verify the deterministic 12-hex rule ID, the `segment-rule` label, and the short `segment` label value.
- Write labels correctly to RoleInstances and Pod templates.
- Reconciliation restores the expected value after users modify managed labels.
- Pod recreation does not change group identity.
- With a fixed ratio, deleting and recreating a RoleInstance at the same ordinal does not change its identity.
- RIS annotations carry Policy UID, rule name, and the current role's quota correctly. Do not put specific group labels on the shared RIS template.
- RIS annotations also carry the deterministic rule ID used by labels and selectors.
- Synchronize existing RI and Pod labels even when replicas are unchanged; metadata changes trigger downstream reconciliation.
- Add, update, remove, and no-op label cases use the same idempotent desired-state logic, preserve other metadata, and do not trigger workload rollouts or Pod recreation.
- ControllerRevision hashes and `updatedReplicas` are unchanged when only managed Segment labels are added, changed, or removed.
- After removing RIS group descriptions, RIs, existing Pods, and future Pod templates no longer retain managed group labels. Subsequent Pod recreation must not restore old labels.
- Annotation and label removals trigger downstream synchronization. Reading or parsing failures must not cause cleanup as if grouping were absent.

##### Policy Lifecycle

- Policy creation, deletion, and Segment addition or removal trigger adoption or withdrawal even when RBG replicas and workload templates are unchanged.
- Initial adoption, re-adoption, and reconciliation retries all check member modes. Stateless RIS stops the entire RBG reconciliation without a valid group description, a corresponding Segment Adapter, mode conversion, or deletion of existing instances.
- Sparse membership, user exclusions, and non-native Stateful backends block adoption without deleting instances or rewriting membership.
- On initial adoption, block mismatched effective desired ratios without rearranging or deleting existing members. If the desired ratio is valid, synchronize existing RI and Pod identities whether or not actual membership is complete.
- With desired `2P, 2D` and actual `2P, 1D`, retain P in the incomplete group and the effective target, and let RIS create the missing D. Do not reduce the target to current complete groups or clean up before recreating.
- Adoption may complete once descriptions and existing member identities are synchronized, without waiting for every desired member to exist or become Ready. Missing-member groups remain incomplete and unavailable; later members inherit the correct identity.
- Adoption does not bypass creation, scaling progression, or scheduling restrictions. Neither identity synchronization nor ordinary temporary missing members trigger adoption-related cleanup.
- Deletion withdraws RIS annotations and restores replica, update, and scheduling parameters from current effective configuration. Preserve user configuration and other effective rules, without clearing all values to zero or deleting shared objects incorrectly.
- Invalid group strategies stop applying even while old labels remain. A Policy read failure is not treated as deletion.
- Label cleanup does not block Policy deletion and converges through the same synchronization logic after failures, retries, and controller restarts.
- Re-addition with the same or a different ratio and rule renaming use current configuration for validation and adoption. A valid new ratio with incompatible current replicas blocks without automatic adjustment or historical-ratio checks.
- Repeated reconciliation, conflict retries, restarts, and same-name Policy recreation validate current configuration and object identity. Descriptions and labels converge through the same logic without separate cleanup tasks.
- Withdrawal after P finishes updating but before D finishes neither rolls back P nor waits for D readiness. After a restart with only some RIS parameters restored, continue converging to current configuration without reusing invalid group budgets or replaying stale withdrawal steps.

##### Scaling

- When effective desired replicas represent a common integer group count, calculate a common target in Segment-count space, then derive each role's instance target by ratio.
- Scale out and in in complete Segments; targets must not exceed the group count represented by valid desired replicas.
- During initial creation with an invalid desired state such as `2P:1D` and `P=5, D=2`, report residual instances for each role and create neither `4P, 2D` nor any other Segment members.
- When a subsequent replica change to an active Segment violates the ratio, keep the valid targets currently applied to each RIS without partial scaling. Adding or re-adding Segment blocks adoption without transferring Adapter control authority.
- A ratio violation stops the entire RBG reconciliation before any Segment or role target is written; existing RIS objects are unchanged and their controllers continue maintaining existing instances. Processing resumes after desired replicas are corrected.
- Initial creation and later scale-out both use `segBatchSize`, defaulting to 1. A target of 5 with batch size 2 progresses as `0 → 2 → 4 → 5`, allowing a partial final batch. Without a scaling strategy, apply the full target directly.
- Omitted, zero, or other valid integer and percentage `scaling.maxSkew` values do not affect Segment targets. The batch field does not affect scale-in or surge.
- `OrderScheduled` checks all expected Pods of all current-batch members; `OrderReady` checks all members' readiness. Evaluate by batch with multiple components, missing members, or some roles already at final replicas while the current batch is not Ready.
- Keep the applied target while a batch is incomplete. Do not reduce it to Ready or actual complete groups, or add the next batch early on repeated reconciliation, partial target application, or delayed status.
- Segment scaling neither raises targets to Gang minimums nor adjusts batch size automatically. Admission rejects static `segBatchSize < minSegments` conflicts, while reconciliation reports configurations that bypass admission.
- Reject scaling only some roles without partially executing the request through rounding.
- When desired replicas do not divide evenly or yield different group counts, report residual instances and stop the RBG reconciliation. Verify that the address discovery update is skipped and no residual identity is published while the violation persists.

##### ScalingAdapter Status and Control Authority

Verify configuration and submission, control handoff, and status synchronization separately.

###### Adapter Configuration and Target Submission

- `strategy.scaling.scalingAdapter` reuses the existing `ScalingAdapter` type. `enable` defaults to `false`, labels propagate without overriding managed identifiers, and the API, CRDs, and generated code retain the configuration.
- Automatically create one Adapter only for an effective Segment rule with Adapter configuration enabled, associated with the current RBG. Repeated reconciliation does not create duplicates, and unexpected loss is recoverable. Do not create one if configuration is absent or disabled.
- Exactly one of `role` and `segment` targets is set. Invalid RBG, Policy, rule, or members do not cause partial writes; existing role mode remains compatible.
- Verify the CRD makes `role` optional, rejects both-set and neither-set references, and preserves existing role-only clients.
- Segment `spec.replicas` is an absolute group-level target. Cover scale-out, scale-in, repeated reconciliation, invalid group counts, and projection overflow.
- Submit all member replicas in one RBG update. Conflict retries reread targets and preserve unrelated roles and fields; retries and restarts do not add replicas again.
- Applying RIS targets in batches does not write intermediate targets back as final desired values in Adapter or RBG.
- Without an explicit group count, initialize only from effective desired replicas satisfying the fixed ratio. Block mismatches without silent rounding, and do not unconditionally overwrite explicit counts.

###### Adapter Control Authority and Recovery

- After Segment Adapter adoption, clean up member role Adapters for the current RBG and do not recreate them even if their automatic-creation flags remain `true`. Non-member roles and other RBGs are unaffected.
- Old role Adapters may still apply overrides without new requests. Handoff fences off both persistent writes and in-memory overrides. Cover delayed deletion, synchronization retries, restarts, and contention from duplicate Segment Adapters.
- Disabling Adapter configuration or invalidating Policy or its rule stops old Adapter writes and overrides and cleans up invalid managed Segment Adapters. Do not reset RBG desired replicas to zero or reuse bindings after object identity changes.
- While a role belongs to an effective Segment, do not restore its role Adapter's creation or writes whether the Segment Adapter is not enabled, disabled, or unexpectedly missing.
- Once a role no longer belongs to an effective Segment, restore the ordinary Adapter workflow according to its `scalingAdapter.enable`. Initialize from currently submitted RBG targets, not historical Adapter values or intermediate RIS targets; other effective strategies still apply.

###### Adapter Status and Scale Mapping

- Update `status.replicas` only after every member target is successfully submitted to RBG. Validation or submission failures must not advance it, and RIS batch targets or actual creation counts must not change it.
- If the RBG write succeeds and Adapter status fails, repeated reconciliation or restart repairs status. Matching targets still require necessary status synchronization, without adding replicas again.
- With 4 requested groups, the target submitted, the current batch at 3, and 2 actual Ready groups, Adapter `spec.replicas`, `status.replicas`, and `status.readyReplicas` are 4, 4, and 2 respectively.
- Count Ready groups by identity and actual member state. Exclude groups with missing or non-Ready members, never combine Ready members from different groups, and synchronize Ready-count changes even with an unchanged target.
- Write desired groups and read submitted targets through `/scale`, verifying mapping to Adapter replica fields. HPA usage, general-purpose Pod selection, and metric aggregation and deduplication are not acceptance requirements for this phase.
- Verify the Segment Adapter selector is the exact group-name plus segment-rule AND selector and does not match other RBGs, rules, non-member roles, or a single component.

##### Rolling Update

Verify group-level admission, budget recovery, and RIS execution parameters separately.

###### Group-Level Admission and Budget Recovery

- Admit the same Segment in one update batch. With a positive group budget, do not limit RI concurrency within admitted groups by the same value again. Members may start and become Ready simultaneously or at different times, with temporary coexistence of old and new versions.
- `partition` retains complete Segments, not the same independent RI count per role.
- Normal Stateful updates project the common Segment boundary into per-RIS partitions by ratio and always retain the old groups requested by the user.
- In the fixed-target, `maxUnavailable: 1`, group-by-group example, later reconciliations retain the boundary while the current group is incomplete or shared budget is insufficient. Relax it only after progression conditions are met; faster roles cannot enter the next group early.
- Use RIS availability semantics. Incomplete in-place updates, unsatisfied `minReadySeconds`, or pending member updates must not release in-flight occupancy because of momentary readiness or increased `updatedReplicas`.
- Without surge and with `maxUnavailable: 2`, admit two groups when there is no other occupancy and progression conditions hold. Admit a replacement as soon as one finishes, without waiting for the whole batch. Cover occupancy of 0, 1, and 2, and overlap between unavailable and in-flight sets.
- If one group remains occupied by a failed member while another slot is free, allow other eligible groups. Do not relax the boundary when budget is full; tighten it on target changes when needed. Reuse capacity after target completion and availability recovery without freezing the entire RBG for one failed group.
- RBG calculates budgets from group identities and member states; it does not communicate group unavailability by rewriting Ready states of healthy RIs or Pods.
- Policy `maxUnavailable` counts Segments directly without an extra update-mode switch. Omitted, zero, and other valid Segment `rollingUpdate.maxSkew` values do not change the common boundary or budget, and `segBatchSize` does not limit the update window.
- Resolve group-level `partition` and `maxUnavailable` percentages by rounding down, and `maxSurge` percentages by rounding up. Cover 0, 1, and boundary percentages.
- Count multiple unavailable members of the same Segment once, and unavailable members of different Segments separately.
- Without surge and with `maxUnavailable: 1`, independent RIS progression must not make two Segments unavailable simultaneously.
- Admitted updates occupy budget before member status reflects them; delayed status must not admit updates twice.
- Changing B to C or rolling back retains deduplicated unavailable and in-flight occupancy and recalculates partition for the current target. A target change neither grants a fresh budget nor permits retaining the old partition as a substitute for group-level validation.
- A bad version occupying the entire budget may block new group updates, without additionally requiring B to succeed first. Unhealthy members within partition may attempt repair under existing RIS conditions, without crossing the boundary or increasing group-level occupancy.
- If external failures exhaust or exceed the budget, do not admit updates that expand the unavailable Segment set.

###### Update Parameters and Surge Projection

- Resolve Policy `maxUnavailable` percentages only against desired complete Segments for the group budget. Do not pass them to RIS for a second calculation against per-RIS replica counts.
- With group budget `B > 0`, ignore role values and apply `100%` to each RIS without multiplying the original Policy value by the ratio. With `1P:2D` and Policy value `1`, P, D0, and D1 in one admitted Segment may all enter the update while the group occupies one budget slot; simultaneous startup or readiness is not required.
- With group budget `B = 0` and positive `maxSurge`, apply `0` to each RIS, never `100%` as a way around the zero budget; only actually available extra instances increase the execution budget. Verify admission rejects an explicit zero budget with omitted or zero `maxSurge`, and reconciliation reports percentage budgets that round down to zero without surge.
- Verify the literal `0` override survives existing Stateful RIS resolution when `maxSurge` is positive, and that no Segment path writes a zero budget that the RIS floor would raise to `1`. Non-Segment rules retain the existing floor behavior.
- If Policy omits `maxUnavailable`, retain role configuration or RIS defaults, without adding or inferring a group-level default. Preserve Segment boundaries and other coordination conditions.
- Rules without Segment retain independent per-role unavailability budgets.
- For non-Segment rules, explicit Policy `maxSurge` overrides role configuration unchanged; omission preserves existing behavior. Explicit `0` differs from omission.
- For Segment, Policy `maxSurge` ignores role configuration. With `1P:2D` and group-level value `1`, apply `P=1, D=2`. Omission or field removal explicitly applies `0`; withdrawing Segment restores current ordinary configuration.
- Resolve `maxSurge` percentages by rounding up desired complete groups before multiplying by quotas. Cover zero replicas, rounding boundaries, and multiplication overflow; do not round per RIS first.
- Preserve the actual Stateful surge algorithm: a full update range may create up to the limit, while a narrower partition range may create fewer instances or none. RIS retains and reclaims existing valid surge instances without cross-role completion or unified reclamation requirements.
- With `maxUnavailable: 0, maxSurge > 0`, RIS receives `0`. Retain preparation of extra capacity followed by healthy base-instance updates only after that capacity is available. RBG must not block surge preparation because the simplified group window is zero. RIS must not count configured limits or non-Ready extra RIs as available budget.
- Implement the surge admission rule: a group is eligible only when every role has at least its quota of healthy target-version surge RIs; otherwise it consumes the no-unavailability budget. Cover capacity disappearance and movement from safe in-flight to loss-risk.
- Surge creation also writes RI and Pod group labels. Incomplete extra membership must not be considered a complete serviceable group, and local surge readiness must not release a base Segment's in-flight occupancy.

##### Gang

- Generate one SubGroupPolicy per Segment rule, using the group-name and segment-rule selector and only `segment` in `matchLabelKeys`. Do not create a separate PodGroup for each Segment.
- Calculate `subGroupSize` from all RIs and component Pods in a complete group, with `minSubGroups=minSegments` and `minMember=minSegments×subGroupSize`. Cover multiple components and accumulation across Segment rules.
- Admit scheduling only when resources can satisfy at least `minSegments` complete Segments. Do not admit when resources or scheduling constraints are insufficient.
- Creation from zero does not wait for member readiness; members may start at different times after admission.
- Without scaling, omitted `minSegments` requires all desired complete Segments. Combining scaling with Gang requires an explicit value at admission.
- Report an unsatisfiable threshold if `minSegments` exceeds desired complete groups, without automatically increasing desired replicas.
- Reject batch size 1 with threshold 2 at admission. Batch size 2 with threshold 2 proceeds without target raising once scheduling and progression conditions are met.
- Translate a Segment rule to one Volcano subGroup using the group-name and segment-rule selector, the segment match key, total per-Segment Pod count, and `minSegments`. Verify multiple rules remain separate.
- Reject Segment Gang with scheduler-plugins. If the Volcano CRD lacks `subGroupPolicy`, report incompatibility without degrading to Pod-count or per-role thresholds.
- Rules without Segment continue using `minReplicas` and existing target-raising logic.

#### Integration Tests

These scenarios verify parameter application, event propagation, and final state across controllers.

##### Member Modes and Group Identity

- With all-Stateful RIS membership and other validation satisfied, creating a CoordinatedPolicy with Segment labels both RoleInstances and Pods.
- Cover creating RBG before adding Segment and creating Policy before RBG. Stateless-member combinations must never become effective, and switching an effective Segment member to Stateless is rejected.
- Simulate an invalid combination bypassing admission. Reconciliation stops with a condition and reports the specific member without a valid group description, automatic mode conversion, or deletion of existing instances. Existing instances, including non-Segment Stateless roles, continue running unchanged.
- Pod recreation does not change group identity.

##### Adapter Targets and Control Handoff

- Enable `strategy.scaling.scalingAdapter` in Policy and verify that RBG creates and maintains a Segment Adapter and custom labels. Existing member role Adapters are cleaned up and not recreated even if their original flags remain enabled. Non-member role Adapters are unchanged.
- Cover unexpected Segment Adapter loss, delayed member role Adapter deletion, and controller restarts. Recovery converges according to effective configuration, and old persistent writes and overrides cannot overwrite group targets.
- Through ScalingAdapter, request scale-out from 2 to 4 groups at `1P:2D`. Keep final RBG desired replicas distinct from batched RIS execution targets; continue convergence when status satisfies progression.
- Cover Adapter update conflicts, repeated reconciliation, controller restarts, old role Adapter handoff, and Policy deletion that stops old group-target writes without accidentally scaling to zero.
- Distinguish deleting only the Segment Adapter object, disabling its configuration while retaining Segment, and removing Segment configuration. The first two do not restore member role Adapters; the third restores them according to role flags. New role Adapters initialize from current RBG targets, preserve other effective strategies, and are not overwritten by the old Segment Adapter during handoff.

##### Creation Batches and Progression

- Reject role-level scaling and report a condition.
- Cover default batch size 1 and `segBatchSize: 2`, including a final batch of 1 for a target of 5 groups. Do not advance before current-batch progression is satisfied; partial application, retries, and restarts do not admit extra groups.
- When the entire batch is Scheduled but one member is not Ready, `OrderScheduled` proceeds and `OrderReady` waits. Missing component Pods must not be mistaken for completed scheduling.
- Changing either `maxSkew` under Segment does not affect creation batches or update windows. Non-Segment progress coordination remains unchanged.

##### Group-Level Updates and Surge

- Admit rolling updates in Segment batches. `partition` and `maxUnavailable` use Segment units, and `rollingUpdate.maxSkew` is ignored.
- For Stateful mode with fixed replicas, a fixed target version, and no surge, verify the full loop: RBG partition calculation, RIS execution, status feedback, and RBG recalculation. Cover the boundary sequence for group-by-group updates of 3P and 6D at `1P:2D`.
- With Policy value `1`, both P and D RIS receive `100%`, and every member of one admitted Segment may enter the update without serial rotation of the two D instances. RBG must not admit the next group until RIS semantics establish target completion and availability of the whole group. Actual startup and readiness order is not guaranteed. Cover in-place updates and `minReadySeconds`.
- Without surge and with Policy value `2`, cover admission of two groups, reuse of one slot after completion, and continued progression through the other slot when one group persistently fails. If Policy omits the value, retain local windows without a new group-level default budget.
- Cover Policy `maxSurge` from the API through RBG calculation, RIS overrides, and extra RI creation: unchanged overrides for non-Segment, ratio projection for Segment. A correct limit does not require creating the full amount every reconciliation.
- Cover narrower partitions where actual extra counts do not form groups, independent reclamation, and surge RI/Pod label synchronization. Base groups still obey the common boundary and effective execution budget.
- With Segment Policy `maxUnavailable: 0, maxSurge: 1`, each RIS receives `maxUnavailable: 0`. With healthy old members and user partition and other coordination conditions permitting, the candidate update scope can trigger surge creation. Do not update healthy old instances until extra RIs are available; then proceed under the existing RIS budget mechanism, without a new zero-budget freeze rule. Local readiness must not clear group state or in-flight records early.
- If one role updates faster while another is slow or failing, independent RIS progression must not make the union of unavailable Segments exceed the configured shared budget.
- After a controller restart or delayed member status, restoring common admission must neither admit groups twice nor lose in-flight budget occupancy.
- Without surge and with `maxUnavailable: 1`, Segment 2 has completed B, Segment 1's P is Ready, and D is not. Change the target to C and verify that the new template and recalculated partition prevent P from updating Segment 2 on its free local budget, avoiding two unavailable groups. Cover partial RIS parameter application, delayed status, and restarts.
- Cover successive updates, bad versions, and rollbacks with budgets of 1 and greater than 1. Full occupancy blocks new groups; remaining budget permits eligible groups. Do not clear old occupancy, force B to succeed first, or rewrite real health states. Verify that unhealthy repair within the allowed scope follows RIS conditions.

##### Gang Resource Admission

- Gang checks resource admission only and does not wait for application readiness.
- With Volcano, verify that Pods from different roles and RIs sharing a Segment label form one subGroup. P and D instances from different Segments must not satisfy the threshold through independent role counts; admission requires at least `minSegments` complete groups with matching identities.
- From zero, use `segBatchSize: 2, minSegments: 2` and verify that the initial target remains 2 groups and progression continues after resource admission. A final batch of 1 group does not repeat the initial-threshold check.
- Simulate an old configuration bypassing admission and an Adapter reducing desired groups below `minSegments`. Reconciliation reports the configuration error, keeps the last valid group target applied to RIS, and blocks the scale-in until the user corrects the desired group count or threshold. Insufficient resources leave Pods Pending without being misreported as an incompatible configuration.
- Without scaling, omitted `minSegments` still requires all desired groups; non-Segment rules in the same RBG are unaffected.
- Rename a rule while a Volcano PodGroup with `minSubGroups` is active and pending gang dispatch. Verify that the subGroup selector is regenerated with the new rule ID, Pods carrying old label values stop matching during convergence, and dispatch resumes after label synchronization completes without Pod restarts or PodGroup deletion.

##### Address Discovery

- While effective desired replicas violate the ratio, such as `2P:1D` with `P=5, D=2`, verify that reconciliation stops before the address table is updated: no residual address such as `prefill-4` appears and the last aligned table is retained. Also verify that no surge identity is published as a base service member.
- During rolling updates and surge creation, verify that discovery output changes only with the applied base-group target and that residual or surge identities are never published as serviceable members.

##### Policy Adoption and Withdrawal

- Admission rejects in-place ratio changes; instance recreation within the same effective configuration retains identity. After withdrawal, re-addition with the same or a different ratio validates current targets and synchronizes new identities if valid, without historical-ratio restrictions.
- Rules that neither declare Segment nor configure the new Policy `maxSurge` retain existing behavior. Explicit `maxSurge` adds only the corresponding parameter override.
- Create RBG first, then add a Segment rule. If effective desired replicas and existing members satisfy the ratio, synchronize RIS annotations, RI labels, and Pod labels even with unchanged replicas and workload templates, without restarting existing Pods.
- Add a rule with two desired `1P:1D` groups and actual `P0, P1, D0`. Existing members synchronize identity first; adoption does not recreate P1 or its Pods or reduce the target to one group. Adoption can finish while Segment 1 is incomplete, followed by D1 creation and correct identity when creation and scheduling conditions allow.
- Cover non-Ready existing members, metadata synchronization in batches, controller restarts during adoption, and members created during synchronization. Adoption does not wait for all members to be Ready. Repetition neither misses labels nor triggers unexpected scale-in; completeness and unavailability counts remain correct.
- Delete Policy after descriptions and coordinated update parameters have been applied. Withdraw old annotations and overrides, preserve ordinary configuration, and stop old coordination despite residual labels. Removing some rules does not affect other effective rules.
- Withdraw Policy after P has finished updating but D has not. Do not wait for whole-group readiness or roll back P. Simulate failure or restart after only P's RIS parameters are restored. Every RIS eventually converges to current configuration, while RIS still tracks D's actual unavailability.
- After same-name Policy recreation, Segment re-addition, or rule renaming, revalidate current identity and synchronize existing labels to the new configuration. Ratio mismatches block non-destructively without automatic replica changes, ratio history, or waiting for old-label cleanup.
- With replicas and workload templates unchanged, verify add, update, remove, and no-op label cases through the same path. After withdrawal, existing RI/Pod labels are eventually removed while preserving other metadata, without recreation. Later new or recreated Pods do not restore old labels. Cover synchronization failure and controller restart.

##### Controller Downgrade Preparation

- Keep the new controller running during downgrade preparation. After removing configuration unsupported by the target version, restore RIS execution parameters and Gang constraints from current ordinary configuration, finish Segment Adapter cleanup and replica control handoff, and clear RIS group descriptions and managed labels from RIs, Pods, and Pod templates.
- If parameter restoration, Adapter handoff, or label synchronization remains incomplete, switching to the old controller is not yet allowed; the new controller continues converging. Once preparation completes, switching is allowed even if workload Pods are not Ready. Do not recreate Pods to clean labels.

#### End-to-End Tests

- Run a PD-disaggregated inference example and verify RI and Pod identities against the prefill/decode ratio.
- Exercise address discovery end-to-end from the ConfigMap consumed by routers: while desired replicas violate the ratio, reconciliation stops before the ConfigMap is updated and the last aligned identities remain; while surge instances exist, publish only aligned base-group identities and never surge identities; return to the corrected target after the desired replicas are fixed.
- Scale out and in using Segment units; verify final replicas satisfy the ratio and creation batches obey progression. For both initial creation and an active Segment, verify that invalid ratios stop RBG reconciliation and processing resumes after correction.
- Perform Segment-level rolling upgrades; verify that common partitions and group budgets constrain actual updates. With a positive budget, admitted groups do not rotate RIs using the original Policy value; a zero budget with surge still adds no unavailable instances.
- Set non-Segment Policy `maxSurge: 2` with `replicas=4, partition=0` and initially healthy old-version instances. Cover `maxUnavailable: 0` and `2`; verify two extra RIs appear and are eventually reclaimed to 4. Reuse existing RIS surge E2E scenarios without asserting that every partition must create two extra instances.
- Verify Segment Policy limit projection, role-configuration overrides, and explicit zero on omission. Each RIS executes its local algorithm; extra RIs need not form complete groups.
- Combine with Gang: matching batches and thresholds progress normally; mismatches keep waiting without automatic target changes. Cover default Gang thresholds and recovery after user adjustment.
- Cover adoption of an existing RBG, feature withdrawal, and switching to an old controller only after the new controller completes downgrade preparation. Old Segment targets and labels must not return; ordinary replica control continues. API downgrade compatibility tests are out of scope.

### Graduation Criteria

Each stage also requires the preceding stage's criteria. Do not treat proposed capabilities as released features before acceptance is complete.

#### Alpha

- API, validation, group assignment, and label persistence are implemented.
- Scaling, rolling updates, and Gang all support Segment-level operation.
- Unit and integration tests cover core semantics.
- Rules that neither declare Segment nor configure the new Policy `maxSurge` retain existing behavior.

#### Beta

- ScalingAdapter supports Segment targets.
- Status and conditions are complete.
- Migration and rollback tests provide coverage.
- Topology composition semantics and router co-design documentation are defined.
- At least one real PD-disaggregated inference workload has been validated.

#### GA

- Two or more real users use the feature.
- Two releases without correctness issues.
- Stable upgrade, downgrade, and migration paths.
- Complete documentation, examples, monitoring, and troubleshooting guides.

### Upgrade / Downgrade Strategy

#### Migration and Compatibility

Adoption, withdrawal, and re-addition for existing workloads follow the [Policy lifecycle](#adding-and-removing-coordinatedpolicy). Changing replicas, images, or strategy parameters does not change the current ratio. To change the ratio, withdraw the configuration first, then adopt the new one.

Segment neither adopts Stateless members nor changes member modes automatically. Mode changes after withdrawal remain subject to existing workload validation; in-place mode switching is not guaranteed.

#### Upgrade

- All new fields are optional.
- Rules that neither declare Segment nor configure the new Policy `maxSurge` retain existing behavior.
- Controllers can enable Segment one Policy rule at a time.

#### Controller Downgrade

Switch only the control plane, without reverting API versions or CRD schemas:

1. Keep the new controller running. Remove Segment configuration through [coordination withdrawal](#withdrawing-coordination-constraints-after-policy-deletion), along with associated fields or rules unsupported by the target old controller, such as `minSegments`, `segBatchSize`, Segment Adapter configuration, and unsupported Policy `maxSurge`. Any retained ordinary coordination configuration must use forms supported by the target version, not group-counting semantics carried over unchanged.
2. Have the new controller restore RIS `replicas`, `partition`, `maxUnavailable`, `maxSurge`, Gang, and other managed constraints from current effective replica targets, role configuration, and remaining effective strategies. Restore the currently required values, without setting everything to zero or leaving old Segment overrides.
3. Stop Segment Adapter writes and complete cleanup. After members leave Segment, restore role Adapters according to their original flags and normal binding conditions, initialized from currently submitted RBG targets. Complete handoff for both persistent writes and in-memory overrides; do not restore historical targets.
4. Withdraw RIS group descriptions through the existing synchronization path and clear managed group labels from existing RIs, Pods, and future Pod templates so new members cannot inherit old identity. Do not rely on the old controller to finish cleanup after takeover.
5. Switch to the old controller only after confirming all preparation is complete. If any step is unfinished, retain the new controller for reconciliation and retries. Workloads need not all be Ready, and Pods must not be restarted or recreated to clean labels.

This is a manual operational prerequisite for switching controllers, not an automatic downgrade or a separate cleanup mechanism. Ordinary feature withdrawal allows asynchronous label cleanup; switching to the old controller requires confirming that cleanup has finished.

For example, after Segment deletion, a D Pod is still not Ready and retains an old group label. Once the new controller removes labels, restores parameters, and completes Adapter handoff, switching to the old controller is allowed without waiting for D readiness. If label synchronization fails, keep the new controller running and retry; do not leave cleanup to the old controller.

This procedure does not revert API versions or CRD schemas. The target old controller must support the retained API resources and configuration; arbitrary old versions are not guaranteed to take over directly.

### Version Skew Strategy

- Legacy API versions are approaching deprecation. Segment does not define API downgrades, compatibility with old clients, reverse mapping to legacy coordination, new old-version access restrictions, or conversion-preservation mechanisms.
- `segment` is defined in the separate `CoordinatedPolicy` resource, currently available only as v1alpha2. RBG API conversion neither converts nor deletes the companion Policy; it must not be described as preserving or discarding `segment` during RBG conversion.
- Controller downgrade and API conversion are different operations. A controller downgrade first completes Segment withdrawal and cleanup as described above, then switches controllers. An old controller ignoring unknown fields is not a guarantee of safe takeover.

## Production Readiness Review Questionnaire

Implementation and release reviews must confirm the following enablement, observability, and failure-handling capabilities.

### Feature Enablement and Rollback

#### How can this feature be enabled or disabled in a live cluster?

- Enable: declare `segment` in a CoordinatedPolicy rule.
- Disable: delete the `segment` field, the entire rule, or the Policy, and withdraw applied coordination constraints according to the lifecycle section.
- A control-plane restart is not required.

#### Does enabling the feature change any default behavior?

Rules without Segment are unaffected.

#### What happens if the feature is re-enabled after being disabled?

Revalidate and adopt using current configuration; see [re-adding configuration and renaming rules](#re-adding-configuration-and-renaming-rules).

#### What tests cover feature enablement and disablement?

Acceptance scenarios for adoption, withdrawal, and re-addition are in the [test plan](#test-plan) and have not yet been executed.

### Rollout, Upgrade and Rollback Planning

#### What must be completed before a controller rollback?

If a controller downgrade is also required, the new controller must first restore parameters, complete Adapter handoff, and clean managed group labels. Only then can the old controller take over. Workloads need not all be Ready. See the [upgrade / downgrade strategy](#upgrade--downgrade-strategy) for the procedure.

#### Have upgrade and rollback been tested?

Not yet. See [controller downgrade preparation](#controller-downgrade-preparation) and [end-to-end tests](#end-to-end-tests) for acceptance scenarios.

### Monitoring Requirements

#### How can an operator determine whether workloads use the feature?

After implementation, the following command can find RIs with group labels. Labels may be awaiting asynchronous cleanup, so also check the effective Policy and RIS group descriptions. Label presence alone does not establish that a strategy remains effective.

```sh
kubectl get roleinstances -l rbg.workloads.x-k8s.io/segment
```

#### How can users verify group assignment and policy execution?

RBG and relevant Adapter status should let users determine:

- Whether configuration was accepted and, if rejected, why.
- The final desired group count and the currently applied group-count target.
- Actual complete groups, Ready groups, and groups satisfying RIS availability conditions.
- Each role's misalignment details and whether scaling requests have been rejected.

The minimum condition contract is:

| Object and condition | Required meaning |
| --- | --- |
| RBG `SegmentConfigurationValid` | Whether the rule, backend, dense membership, rule ID, and update parameters are valid; the reason identifies the first blocking member or field |
| RBG `SegmentReplicasAligned` | Whether every member role's effective desired replicas resolve to the same integer group count |
| RBG `SegmentAssignmentReady` | Whether RIS descriptions and existing RI/Pod identities have converged for the effective rule |
| RBG `SegmentGangConfigured` | Whether the scheduler supports Segment subgroups and the PodGroup translation is current |
| Adapter `SegmentTargetBound` | Whether the Segment target is bound, has write authority, and successfully submitted all member targets |

Condition reasons must distinguish invalid configuration, unsupported scheduler capability, alignment failure, synchronization delay, and controller error.

### Dependencies

#### Which cluster services does the feature depend on?

Segment structure itself does not depend on a particular scheduler.

Segment Gang requires Volcano `subGroupPolicy`; scheduler-plugins does not support complete-Segment thresholds. Non-Segment Gang retains its existing scheduler support. Composition with topology depends on the future topology design. Group-aware routing depends on the specific router implementation.

### Scalability

#### Does the feature introduce additional API calls?

Labeling new members reuses RI and Pod creation paths. Adoption and withdrawal for existing workloads add metadata patches for changed fields and do not depend on rolling updates.

#### Does the feature increase API object size or count?

- Policy adds grouping and strategy fields. When Adapters are enabled, each Segment rule adds one Adapter. Gang retains one PodGroup per RBG and adds one SubGroupPolicy per Segment rule; no separate management resource is created for every Segment or Pod.
- Status is aggregated by rule. Exact status fields and persistence costs depend on implementation and scalability testing.

#### Does the feature increase controller resource usage?

Group-level availability and budget calculations traverse member identities and states. Cost grows with both rules and members and cannot be estimated from rule and role counts alone. Measure actual cache, index, scan, and API request costs after implementation.

### Troubleshooting

The following are requirements for status and diagnostics. The condition names in [monitoring requirements](#monitoring-requirements) are the minimum contract; implementation may add fields but may not omit these meanings.

#### What are the known failure modes and how are they handled?

| Symptom | Checks and handling |
| --- | --- |
| Replicas do not satisfy the ratio | Inspect the ratio violation and residual counts per role. Correct effective targets to a common group count. With an Adapter enabled, submit the group count through the Adapter rather than changing member replicas independently. |
| Adapter request rejected | Check for a single-role scaling request and validate RBG, Policy, the rule, object ownership, and control authority. |
| Gang or creation batches keep waiting | First check configuration errors, including `segBatchSize < minSegments`, `minSegments` above desired groups, or an unsupported scheduler. Then check Pod requests, scheduling constraints, and actual resources. Waiting for resources with a valid configuration is expected; application non-readiness does not mean resource admission failed. |
| Pod labels differ from expectations | Trace current desired state and synchronization errors through Policy → RIS annotation → RI label → Pod label, and check for external modification. Restore through reconciliation, not restarts. |
| Member mode rejected | Check for Stateless RIS, a non-native backend, or sparse membership (`excluded-ordinals` / `committed-skip-ordinals`). If that membership must be retained, remove the corresponding Segment configuration; the controller does not convert modes or fill exclusions automatically. |
| Updates stop progressing | Check unavailable and in-flight group occupancy, user partition, RIS local budgets, and actual available surge. A target version change does not clear existing occupancy. |

## Implementation History

- 2026-11-03: Created the KEP draft based on issue #543.

## Drawbacks

Segment trades stricter membership constraints for a common operation unit. Users must confirm that the ratio matches their service model and accept the flexibility limits, waiting conditions, and identity synchronization delays in [risks and mitigations](#risks-and-mitigations).

See [non-goals](#non-goals) for Stateless RIS and deprecated workload scope. Simultaneous member readiness and updates without mixed versions are not guaranteed.

## Alternatives

The following approaches were not selected because they cannot provide both stable identity and a common operation unit across strategies.

### Use Only the Existing CoordinatedPolicy

Existing scaling, rolling updates, and Gang already coordinate multiple roles, but lack stable service-group identity and cannot guarantee that all three strategies use the same minimum service unit.

### Let Applications Manage Groups

Applications can maintain their own prefill/decode pairings, but grouping correctness then depends separately on the application and operational procedures. RBG cannot use that structure consistently for scheduling, scaling, and updates.

### Define Segment as a Scheduling Strategy

Putting Segment inside a scheduling strategy and making it control rollouts couples structural grouping with behavioral policy, making it difficult to serve Gang, scaling, rolling updates, and topology together. This design therefore keeps Segment as a rule-level structural field.

## References

- [Issue #543: Support Segment as the cross-role service unit in CoordinatedPolicy](https://github.com/sgl-project/rbg/issues/543)
- [KEP-430: Gang Scheduling](../430-gang-scheduling/README.md)
- [KEP-30: Role Coordination](../30-role-coordination/README.md)

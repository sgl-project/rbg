# KEP-576: Compact Discovery ConfigMap

English | [简体中文](README.zh-CN.md)

<!-- toc -->
- [Release Signoff Checklist](#release-signoff-checklist)
- [Summary](#summary)
- [Motivation](#motivation)
    - [Background](#background)
        - [Current v1alpha1 Content](#current-v1alpha1-content)
        - [Pod DNS by Pattern](#pod-dns-by-pattern)
        - [Size Problem](#size-problem)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [Terms](#terms)
    - [User Stories (Optional)](#user-stories-optional)
        - [Story 1](#story-1)
        - [Story 2](#story-2)
    - [Notes, Constraints, and Caveats (Optional)](#notes-constraints-and-caveats-optional)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
    - [ConfigMap Versioning](#configmap-versioning)
    - [v1 Content Format](#v1-content-format)
        - [Address Templates](#address-templates)
        - [Component Indexing](#component-indexing)
        - [Membership Encoding](#membership-encoding)
        - [Write and Replace](#write-and-replace)
        - [Size Guardrail](#size-guardrail)
    - [Migration](#migration)
    - [Test Plan](#test-plan)
        - [Unit tests](#unit-tests)
        - [Integration tests](#integration-tests)
        - [e2e tests](#e2e-tests)
    - [Graduation Criteria](#graduation-criteria)
        - [Alpha](#alpha)
        - [Beta](#beta)
        - [GA](#ga)
        - [Deprecation](#deprecation)
    - [Upgrade and Downgrade Strategy](#upgrade-and-downgrade-strategy)
    - [Version Skew Strategy](#version-skew-strategy)
- [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
    - [Feature Enablement and Rollback](#feature-enablement-and-rollback)
        - [How can this feature be enabled or disabled in a live cluster?](#how-can-this-feature-be-enabled-or-disabled-in-a-live-cluster)
        - [Does enabling the feature change any default behavior?](#does-enabling-the-feature-change-any-default-behavior)
        - [Can the feature be disabled after it is enabled?](#can-the-feature-be-disabled-after-it-is-enabled)
        - [What happens if the feature is enabled again after being disabled?](#what-happens-if-the-feature-is-enabled-again-after-being-disabled)
        - [Are there tests for enabling and disabling the feature?](#are-there-tests-for-enabling-and-disabling-the-feature)
    - [Rollout, Upgrade, and Rollback Planning](#rollout-upgrade-and-rollback-planning)
        - [How can a rollout or rollback fail, and can it affect running workloads?](#how-can-a-rollout-or-rollback-fail-and-can-it-affect-running-workloads)
        - [Which metrics should trigger rollback?](#which-metrics-should-trigger-rollback)
        - [Were upgrade and rollback tested?](#were-upgrade-and-rollback-tested)
        - [Does the rollout deprecate or remove anything?](#does-the-rollout-deprecate-or-remove-anything)
    - [Monitoring Requirements](#monitoring-requirements)
        - [How can an operator determine whether the feature is in use?](#how-can-an-operator-determine-whether-the-feature-is-in-use)
        - [How can a user determine whether the feature is working?](#how-can-a-user-determine-whether-the-feature-is-working)
        - [What are the reasonable SLOs?](#what-are-the-reasonable-slos)
        - [What are the SLIs?](#what-are-the-slis)
        - [Are any metrics missing?](#are-any-metrics-missing)
    - [Dependencies](#dependencies)
        - [Does this feature depend on any specific services running in the cluster?](#does-this-feature-depend-on-any-specific-services-running-in-the-cluster)
    - [Scalability](#scalability)
        - [Does enabling this feature add API calls?](#does-enabling-this-feature-add-api-calls)
        - [Does enabling this feature introduce new API types?](#does-enabling-this-feature-introduce-new-api-types)
        - [Does enabling this feature add cloud-provider calls?](#does-enabling-this-feature-add-cloud-provider-calls)
        - [Does enabling this feature increase the size or number of existing API objects?](#does-enabling-this-feature-increase-the-size-or-number-of-existing-api-objects)
        - [Does enabling this feature increase existing operation latency?](#does-enabling-this-feature-increase-existing-operation-latency)
        - [Does enabling this feature increase resource usage?](#does-enabling-this-feature-increase-resource-usage)
        - [Can enabling this feature exhaust node resources?](#can-enabling-this-feature-exhaust-node-resources)
    - [Troubleshooting](#troubleshooting)
        - [How does the feature behave when the API server or etcd is unavailable?](#how-does-the-feature-behave-when-the-api-server-or-etcd-is-unavailable)
        - [What are the other known failure modes?](#what-are-the-other-known-failure-modes)
        - [What should be done when SLOs are not met?](#what-should-be-done-when-slos-are-not-met)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure Needed (Optional)](#infrastructure-needed-optional)
<!-- /toc -->

## Release Signoff Checklist

Items marked `(R)` must be completed before this enhancement is targeted at a
release.

- [ ] (R) The enhancement issue in the release milestone links to this KEP.
- [ ] (R) KEP approvers have approved the KEP status as `implementable`.
- [ ] (R) Design details are documented.
- [ ] (R) A test plan is in place.
- [ ] (R) Graduation criteria are in place.
- [ ] (R) Production readiness review is completed.
- [ ] (R) Production readiness review is approved.
- [ ] Implementation History is up to date.
- [ ] User-facing documentation is created.
- [ ] Supporting documentation is created.

## Summary

This KEP changes the `refine` discovery ConfigMap from a full per-instance
enumeration into a compact format described by role and component.

The current content has no version field; this KEP calls it **v1alpha1**. The
new content explicitly writes:

```yaml
apiVersion: v1
```

RBG spec persists the content version:

```yaml
spec:
  discoveryConfigVersion: v1alpha1
```

The controller continues to use the same RBG-level ConfigMap `<rbg>` and the
same `config.yaml` key. When `discoveryConfigVersion` is `v1`, the controller
writes v1 content and replaces the v1alpha1 content.

This KEP defines the storage format, replacement semantics, and migration
steps. Consumer compatibility is a follow-up KEP.

## Motivation

### Background

The intended reader implements RBG discovery, storage formats, or migration.
The reader is assumed to know Kubernetes ConfigMaps, RBG
`RoleBasedGroup` / `RoleInstanceSet`, and the three RoleInstanceSet patterns.

#### Current v1alpha1 Content

The current `refine` mode writes this ConfigMap:

```yaml
# ConfigMap: pd-inference
# key: config.yaml
group:
  name: pd-inference
  size: 2
  roles:
    - prefill
    - decode
roles:
  prefill:
    size: 2
    instances:
      - address: pd-inference-prefill-0.s-pd-inference-prefill
        ports:
          http: 8000
      - address: pd-inference-prefill-1.s-pd-inference-prefill
        ports:
          http: 8000
  decode:
    size: 4
    instances:
      - address: pd-inference-decode-0.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-1.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-2.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-3.s-pd-inference-decode
        ports:
          http: 8000
```

`group.roles` preserves spec declaration order. `roles` is a Go map serialized
by `sigs.k8s.io/yaml`, so its keys are sorted alphabetically. Each role's
`size` is the number of serialized address entries; role replicas are stored
in `role.replicas`. Role-level `servicePorts` are copied to each instance.

Relevant code:

- `pkg/discovery/config_builder.go`: `ClusterConfig` / `GroupInfo` / `RolesInfo`
- `pkg/discovery/config_builder.go`: `Build()`

#### Pod DNS by Pattern

The following placeholders are used:

- `<workloadName>`: workload name from `rbg.GetWorkloadName(role)`.
- `<headlessServiceName>`: headless Service name from
  `GetCompatibleHeadlessServiceName()`.
- `<roleInstanceIndex>`: RoleInstance ordinal.
- `<componentIndex>`: Pod ordinal within a component.
- `<workerIndex>`: worker ordinal in LeaderWorkerPattern, ranging from `1` to
  `size - 1`.

By default:

- `<workloadName> = <rbg>-<role>`
- `<headlessServiceName> = s-<rbg>-<role>`

| Pattern | Pod DNS | Code |
|---|---|---|
| `StandalonePattern` | `<workloadName>-<roleInstanceIndex>.<headlessServiceName>` | `pkg/discovery/config_builder.go`: `buildInstances()` default branch; `api/workloads/v1alpha2/helper.go`: `GetWorkloadName()` / `GetServiceName()` |
| `CustomComponentsPattern` | `<workloadName>-<roleInstanceIndex>-<componentName>-<componentIndex>.<headlessServiceName>` | `pkg/discovery/config_builder.go`: `buildInstances()` CustomComponents branch; `pkg/reconciler/roleinstance/utils/instance_utils.go`: `FormatComponentPodName()` |
| `LeaderWorkerPattern` | leader: `<workloadName>-<roleInstanceIndex>-0.<headlessServiceName>`; worker: `<workloadName>-<roleInstanceIndex>-<workerIndex>.<headlessServiceName>` | `pkg/discovery/config_builder.go`: `buildInstances()` LeaderWorker branch; `pkg/reconciler/roleinstance/utils/instance_utils.go`: `FormatComponentPodName()` |

LeaderWorkerPattern is also a multi-component template, but its component name
does not appear in DNS.

Service compatibility is implemented in:

- `pkg/utils/service_utils.go`: `GetCompatibleHeadlessServiceName()`

#### Size Problem

The current v1alpha1 content is a full enumeration:

- Each address entry is roughly 40–120 bytes.
- With the example names in this KEP, one entry is about 118 bytes.
- A role with 4000 replicas and 5 Pods per instance is about 2.4 MB, which
  exceeds the 1 MiB ConfigMap limit.

Even below the limit, every membership change rewrites the whole object and
kubelet redistributes it to every node with a mounting Pod.

### Goals

1. Make stored size depend on role and component count, and keep it
   independent of replica count.
2. Identify ConfigMap content with `apiVersion`.
3. Persist the content version in RBG spec.
4. Keep the same `<rbg>` ConfigMap and `config.yaml` key.
5. Let v1 content replace v1alpha1 content.
6. Represent Pod DNS correctly for the three RoleInstanceSet patterns.
7. Preserve the current `memberOrdinals` selection semantics.
8. Make a stored-object size breach visible.

### Non-Goals

1. Consumer compatibility, translation scripts, and migration windows are
   follow-up KEPs.
2. Runtime readiness and liveness remain with DNS and EndpointSlice.
3. Deprecated Deployment, StatefulSet, and LeaderWorkerSet workloads remain
   unchanged.
4. Dynamic port allocation remains on Pod environment variables and
   annotations.

## Proposal

### Terms

- **v1alpha1 content**: the full per-instance enumeration currently written
  by `refine`; it has no `apiVersion` field.
- **v1 content**: the compact format defined by this KEP; it writes
  `apiVersion: v1`.
- **`discoveryConfigVersion`**: the RBG spec field that persists the content
  version.
- **Member**: a RoleInstance ordinal selected for discovery.
- **Component**: a named Pod group inside a RoleInstanceSet instance.
- `<rbg>`: the RBG object name.

### User Stories (Optional)

#### Story 1

As a cluster operator, I want the discovery ConfigMap to remain below the
1 MiB limit when a role has thousands of instances.

#### Story 2

As an engine developer, I want a stable, versioned ConfigMap schema so that I
can upgrade consumers without changing the controller's storage format again.

### Notes, Constraints, and Caveats (Optional)

- Each RBG maintains one discovery ConfigMap.
- The ConfigMap name is `<rbg>`, and its data key is `config.yaml`.
- v1 content replaces v1alpha1 content in the same object.
- This KEP does not define a renderer init container.
- Consumer migration is a follow-up KEP.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| v1 content encodes a different address set from the current builder | Unit tests expand v1 content and compare it structurally with current builder output |
| Workload or Service names trigger Kubernetes name truncation | The controller computes the full address template, including truncation and compatibility rules |
| A controller upgrade unexpectedly changes the content version | RBG spec records `discoveryConfigVersion`, and the controller uses that field when rebuilding the ConfigMap |
| A consumer supports only v1alpha1 | Migration is explicit through `spec.discoveryConfigVersion`; consumer compatibility is a follow-up KEP |

## Design Details

### ConfigMap Versioning

This KEP adds content versioning because changing the ConfigMap format
creates compatibility problems, and future changes should be identifiable by
version.

RBG spec records the version:

```yaml
spec:
  discoveryConfigVersion: v1alpha1
```

Rules:

1. `discoveryConfigVersion` is optional and accepts `v1alpha1` or `v1`.
2. When the field is missing, the webhook applies defaulting rules and
   normally sets the latest version. RBGs created after this KEP should always
   carry the field; RBGs that predate migration should have it set manually.
3. The controller uses the field to select the ConfigMap content version.
4. If the ConfigMap is deleted, the controller rebuilds the same version.
5. The ConfigMap's `apiVersion` matches the spec field.
6. v1alpha1 content has no `apiVersion`.

| Content version | Marker | Meaning |
|---|---|---|
| v1alpha1 | missing `apiVersion` | current full enumeration |
| v1 | `apiVersion: v1` | compact format defined by this KEP |

### v1 Content Format

Example:

```yaml
apiVersion: v1
group: pd-inference

roles:
  - name: prefill
    members: 500
    excludedOrdinals: "3,17"
    ports:
      http: 8080
      grpc: 50000
    components:
      - name: leader
        size: 1
        base: "pd-inference-prefill-{roleIndex}-{componentIndex}.s-pd-inference-prefill"
      - name: worker
        size: 4
        offset: 1
        base: "pd-inference-prefill-{roleIndex}-{componentIndex}.s-pd-inference-prefill"
```

Field reference:

| Field | Type | Description |
|---|---|---|
| `apiVersion` | string | must be `v1`; a missing value is parsed as v1alpha1 |
| `group` | string | RBG name |
| `roles` | array | role entries in spec declaration order |
| `roles[].name` | string | role name |
| `roles[].members` | int | number of selected RoleInstances |
| `roles[].excludedOrdinals` | string | normalized contiguous excluded ranges |
| `roles[].ports` | map | role-level `servicePorts` |
| `roles[].components` | array | component entries in spec declaration order |
| `roles[].components[].name` | string | component name |
| `roles[].components[].size` | int | number of Pods in one instance |
| `roles[].components[].offset` | int | optional, default `0`; first `componentIndex` |
| `roles[].components[].base` | string | relative address template containing `{roleIndex}` and `{componentIndex}` |

#### Address Templates

`base` is a complete relative address:

```text
<pod>.<service>
```

Placeholders:

- `{roleIndex}`: RoleInstance ordinal.
- `{componentIndex}`: final component ordinal, equal to
  `offset + zero-based index within the component`.

Replacement rules:

1. Perform string replacement.
2. Use `offset` to express numbering offsets.

#### Component Indexing

LeaderWorkerPattern's actual addresses are:

```text
leader:  <workloadName>-<roleInstanceIndex>-0.<headlessServiceName>
worker:  <workloadName>-<roleInstanceIndex>-<workerIndex>.<headlessServiceName>
```

Two representations are possible:

1. A template expression: `{componentIndex + 1}`.
2. A component field `offset`, with worker `offset: 1`.

This KEP uses `offset`:

```yaml
components:
  - name: leader
    size: 1
    offset: 0
    base: "pd-inference-prefill-{roleIndex}-{componentIndex}.s-pd-inference-prefill"
  - name: worker
    size: 4
    offset: 1
    base: "pd-inference-prefill-{roleIndex}-{componentIndex}.s-pd-inference-prefill"
```

Therefore:

- Leader `componentIndex` is `0`.
- Worker `componentIndex` ranges from `1` to `size - 1`.
- The template remains a string replacement.
- Behavior matches `FormatComponentPodName()`.

StandalonePattern:

```yaml
components:
  - name: default
    size: 1
    offset: 0
    base: "pd-inference-prefill-{roleIndex}.s-pd-inference-prefill"
```

CustomComponentsPattern:

```yaml
components:
  - name: leader
    size: 1
    offset: 0
    base: "pd-inference-prefill-{roleIndex}-leader-{componentIndex}.s-pd-inference-prefill"
```

If a component overrides the Service, `base` uses the overridden Service name.

#### Membership Encoding

Example:

```yaml
members: 3
excludedOrdinals: "1,3"
```

This represents:

```text
[0, 2, 4]
```

Membership selection follows the current `memberOrdinals()`:

1. Before the child RoleInstanceSet exists:
   - with no exclusion input, use `[0, role.replicas)`
   - with valid exclusion input, project exclusions onto the dense range
   - with invalid input, use an empty member list
2. After the child exists:
   - when exclusion input is present or invalid, or the child has a committed
     skip record, use the child's committed target
   - otherwise use `[0, role.replicas)`

Encoding:

- `members` is the number of selected ordinals.
- `excludedOrdinals` is the normalized range record for holes below the
  highest selected ordinal.

#### Write and Replace

The controller writes the ConfigMap before role reconciliation:

1. compute membership and address templates
2. generate content according to `spec.discoveryConfigVersion`
3. write `<rbg>/config.yaml`
4. when the target version is `v1`, replace v1alpha1 content

If writing fails, reconcile returns an error and retries, matching current
`refine` behavior.

#### Size Guardrail

Serialized v1 content is limited to 512 KiB.

On breach, the controller keeps the current valid content, skips the
replacement, and emits a Warning Event when the state changes.

Excluded ranges are represented as contiguous ranges:

- skipping 5000 contiguous members costs only a few bytes
- each disjoint hole costs one short range

Stored size is therefore independent of replica count and ordinal span.

### Migration

1. Install the CRD that adds `discoveryConfigVersion`.
2. Set existing RBGs to:

   ```yaml
   spec:
     discoveryConfigVersion: v1alpha1
   ```

3. Deploy the controller that supports v1.
4. Wait for reconcile and verify that ConfigMap content is still v1alpha1.
5. Change `discoveryConfigVersion` to `v1`.
6. Wait for reconcile and verify that ConfigMap content is `apiVersion: v1`.

Rollback:

1. Change `discoveryConfigVersion` back to `v1alpha1`.
2. Wait for reconcile and verify that ConfigMap content is v1alpha1.

Consumer compatibility is a follow-up KEP.

### Test Plan

#### Unit tests

- v1 field completeness.
- Behavior for `discoveryConfigVersion` missing, `v1alpha1`, and `v1`.
- Parsing missing `apiVersion` as v1alpha1.
- Address templates for the three patterns.
- LeaderWorkerPattern leader and worker offsets.
- CustomComponentsPattern Service overrides.
- Membership selection and `excludedOrdinals` encoding.
- Size guardrail behavior.

#### Integration tests

- The same `<rbg>` ConfigMap is replaced with v1 content.
- After ConfigMap deletion, the controller rebuilds the version recorded in
  `discoveryConfigVersion`.
- ConfigMap count remains 1.
- A write failure fails reconcile and retries.
- ConfigMap content is v1 after migration.

#### e2e tests

- After migration, `<rbg>/config.yaml` contains `apiVersion: v1`.
- Addresses for the three patterns match actual Pod DNS.
- LeaderWorkerPattern worker addresses start at 1.

### Graduation Criteria

#### Alpha

- Compact content publication, format, and guardrail are implemented.
- The unit, integration, and e2e tests above are complete.
- A 2000-instance scale test shows the stored ConfigMap stays below the
  guardrail and reconciliation succeeds.

#### Beta

- Documentation for direct v1 consumption is complete.
- No compact-content divergence reports remain open.

#### GA

- Two releases of stable behavior.
- `legacy` and `refine` populations remain unchanged.

#### Deprecation

- None.

### Upgrade and Downgrade Strategy

See [Migration](#migration).

Upgrade changes only discovery ConfigMap content. Pod templates, volumes, and
mounts remain unchanged.

After downgrade to a controller that predates v1, that controller cannot parse
v1 content. Downgrade migration is a follow-up KEP.

### Version Skew Strategy

`apiVersion` in the ConfigMap and `discoveryConfigVersion` in RBG spec let
consumers and controllers identify the content version.

Older controllers ignore `discoveryConfigVersion` and continue writing
v1alpha1 content.

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

#### How can this feature be enabled or disabled in a live cluster?

Set `spec.discoveryConfigVersion` to `v1`. Set it back to `v1alpha1` to roll
back.

#### Does enabling the feature change any default behavior?

Only ConfigMap content changes. Pod templates, volumes, and mounts remain
unchanged.

#### Can the feature be disabled after it is enabled?

Set `discoveryConfigVersion` back to `v1alpha1` and wait for reconcile.

#### What happens if the feature is enabled again after being disabled?

The controller deterministically rebuilds v1 content from RBG spec and
committed membership.

#### Are there tests for enabling and disabling the feature?

Yes. Integration and e2e tests cover migration and rollback.

### Rollout, Upgrade, and Rollback Planning

#### How can a rollout or rollback fail, and can it affect running workloads?

ConfigMap writes can fail. Reconcile returns an error and retries. Running
workloads are unaffected.

#### Which metrics should trigger rollback?

- RBG controller reconcile error rate.
- ConfigMap write failure count.
- Size guardrail Warning Events.

#### Were upgrade and rollback tested?

Yes. The test plan covers migration and rollback.

#### Does the rollout deprecate or remove anything?

No.

### Monitoring Requirements

#### How can an operator determine whether the feature is in use?

Inspect `spec.discoveryConfigVersion` and the ConfigMap's `apiVersion`.

#### How can a user determine whether the feature is working?

ConfigMap content matches the selected version and the RBG's current roles,
components, and membership.

#### What are the reasonable SLOs?

- ConfigMap reconcile success rate.
- ConfigMap write latency.

#### What are the SLIs?

- RBG controller reconcile error rate.
- Warning Events.

#### Are any metrics missing?

None identified for alpha.

### Dependencies

#### Does this feature depend on any specific services running in the cluster?

No.

### Scalability

#### Does enabling this feature add API calls?

Only the existing ConfigMap reconcile.

#### Does enabling this feature introduce new API types?

No.

#### Does enabling this feature add cloud-provider calls?

No.

#### Does enabling this feature increase the size or number of existing API objects?

No. Each RBG still maintains one discovery ConfigMap.

#### Does enabling this feature increase existing operation latency?

No. Building compact content is faster than enumerating every Pod.

#### Does enabling this feature increase resource usage?

Pod resources remain unchanged.

#### Can enabling this feature exhaust node resources?

The stored object is bounded by the 512 KiB guardrail.

### Troubleshooting

#### How does the feature behave when the API server or etcd is unavailable?

Reconcile fails and retries with the existing backoff. Running Pods are
unaffected.

#### What are the other known failure modes?

- Stored content exceeds the guardrail:
  - Detection: Warning Event.
  - Mitigation: keep current valid content and skip the replacement.
- Content version mismatch:
  - Detection: compare `spec.discoveryConfigVersion` with ConfigMap
    `apiVersion`.
  - Mitigation: set the desired version and wait for reconcile.

#### What should be done when SLOs are not met?

Inspect RBG Warning Events, controller logs, and the ConfigMap.

## Implementation History

- 2026-11-13: Initial KEP.
- 2026-11-17: Changed to compact content design, added version fields, and
  corrected LeaderWorkerPattern addresses and worker offsets.

## Drawbacks

- The consumer-visible content format changes.
- v1 and v1alpha1 consumers need a migration path.

## Alternatives

1. **Keep v1alpha1 full enumeration**
   - cannot satisfy the 1 MiB limit.
2. **Add a parallel compact ConfigMap**
   - introduces two data sources and consistency costs.
3. **Publish naming rules and let clients derive addresses**
   - workload and Service names have truncation and compatibility rules.
4. **Compress v1alpha1**
   - every membership change still rewrites the full data.
5. **Runtime DNS / EndpointSlice**
   - provides readiness information, but lacks static ordinal topology.

## Infrastructure Needed (Optional)

None.

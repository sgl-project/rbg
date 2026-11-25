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
    - [Discovery Config Version Annotation](#discovery-config-version-annotation)
    - [Deprecated Workloads](#deprecated-workloads)
    - [Consumer Decoding Contract](#consumer-decoding-contract)
    - [v1 Content Format](#v1-content-format)
        - [Address Templates](#address-templates)
        - [Component Indexing](#component-indexing)
        - [Membership Encoding](#membership-encoding)
        - [Write and Replace](#write-and-replace)
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

This KEP changes the current discovery ConfigMap from a full per-instance
enumeration into a compact format described by role and component.

The current content has no version field. This KEP names it **v1alpha1**. The
new content explicitly writes:

```yaml
apiVersion: v1
```

RBG metadata persists the content version with a dedicated annotation:

```yaml
metadata:
  annotations:
    rbg.workloads.x-k8s.io/discovery-config-version: v1
```

The controller continues to use the same RBG-level ConfigMap `<rbg>` and the
same `config.yaml` key. When the annotation is `v1`, the controller writes v1
content and replaces the v1alpha1 content.

The ConfigMap is a published snapshot of committed membership, not the source
of truth for controller state. The controller derives it from RBG spec and the
child RoleInstanceSet's committed target, and can rebuild it at any time.

This KEP defines the storage format, replacement semantics, migration steps,
and the decoding contract consumers need while v1alpha1 and v1 coexist. A
separate compatibility layer is out of scope.

Legacy discovery support must be removed before this KEP is implemented. The
controller then has one discovery implementation and no mode selector.

## Motivation

### Background

The intended reader implements RBG discovery, storage formats, or migration.
The reader is assumed to know Kubernetes ConfigMaps, RBG
`RoleBasedGroup` / `RoleInstanceSet`, and the three RoleInstanceSet patterns.

#### Current v1alpha1 Content

The current discovery implementation writes this ConfigMap:

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
  prefill:
    size: 2
    instances:
      - address: pd-inference-prefill-0.s-pd-inference-prefill
        ports:
          http: 8000
      - address: pd-inference-prefill-1.s-pd-inference-prefill
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
- With the example names in this KEP, one entry is about 117.7 bytes.
- A role with 4000 replicas and 5 Pods per instance is about 2.35 MB
  (2.25 MiB), which exceeds the 1 MiB ConfigMap limit.

Even below the limit, every membership change rewrites the whole object and
kubelet redistributes it to every node with a mounting Pod.

### Goals

1. Make stored size depend on role count, component count, and the number of
   disjoint excluded ranges, not on replica count.
2. Identify ConfigMap content with `apiVersion`.
3. Persist the content version in a dedicated annotation.
4. Keep the same `<rbg>` ConfigMap and `config.yaml` key.
5. Let v1 content replace v1alpha1 content.
6. Represent Pod DNS correctly for the three RoleInstanceSet patterns.
7. Preserve the current `memberOrdinals` selection semantics.
8. Use the API server's 1 MiB ConfigMap limit for stored-object size
   enforcement.
9. Remove the discovery-mode selector by dropping legacy support and
   retaining one discovery implementation.
10. Define how new RBGs default to v1, how existing RBGs stay on v1alpha1,
    and how annotation-only changes trigger reconciliation.
11. Define rollback and controller-downgrade behavior.

### Non-Goals

1. A separate consumer compatibility layer or translation script.
2. Runtime readiness and liveness remain with DNS and EndpointSlice.
3. Deprecated Deployment, StatefulSet, and LeaderWorkerSet workloads remain
   unchanged.
4. Dynamic port allocation remains on Pod environment variables and
   annotations.
5. Migrating legacy RBGs to the single discovery implementation.
6. Adding a custom stored-object size guardrail. The API server enforces the
   1 MiB ConfigMap limit; the controller only rejects the serialized
   `config.yaml` data before writing, so an oversized rollback retains compact
   content instead of failing the API-server write repeatedly.
7. Deprecating or removing v1alpha1. This KEP defines coexistence only.

## Proposal

### Terms

- **v1alpha1 content**: the full per-instance enumeration currently written
  by the discovery implementation; it has no `apiVersion` field.
- **v1 content**: the compact format defined by this KEP; it writes
  `apiVersion: v1`.
- **Discovery-config-version annotation**: the
  `rbg.workloads.x-k8s.io/discovery-config-version` annotation, whose values
  are `v1alpha1` and `v1`.
- **Committed membership**: the member set persisted on the child
  RoleInstanceSet. Discovery projects this state; it does not project the
  user-facing exclusion annotation directly.
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
- v1 serializes stateful roles only, matching the current implementation.
- v1 addresses preserve the current builder output. Under
  `sharedServiceSelection: LeaderOnly`, worker addresses may not resolve
  through cluster DNS, exactly as in v1alpha1.
- v1alpha1 and v1 coexist. Deprecation is a separate KEP.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| v1 content encodes a different address set from the current builder | Unit tests decode v1 content and compare it structurally with current builder output |
| Workload or Service names trigger Kubernetes name truncation | The controller computes the full address template, including truncation and compatibility rules |
| A controller upgrade unexpectedly changes the content version | The discovery-config-version annotation records the selected version, and the controller uses it when rebuilding the ConfigMap |
| A consumer supports only v1alpha1 | Keep the annotation at `v1alpha1` until the consumer implements the decoding contract |
| Rollback to v1alpha1 exceeds the 1 MiB limit | The controller rejects the rollback before replacing v1 content |
| RBGSet overwrites a child annotation | The version annotation is owned by `spec.groupTemplate.annotations` and inherited by children |
| Worker addresses do not resolve under `LeaderOnly` | v1 preserves the current builder output; DNS resolvability remains governed by `sharedServiceSelection` |
| An annotation-only change is never reconciled | `RBGPredicate` watches the discovery-config-version annotation |

## Design Details

### Discovery Config Version Annotation

ConfigMap content needs an explicit version so consumers can distinguish
v1alpha1 from v1 and future revisions.

RBG metadata records the version:

```yaml
metadata:
  annotations:
    rbg.workloads.x-k8s.io/discovery-config-version: v1
```

Rules:

1. The annotation key is
   `rbg.workloads.x-k8s.io/discovery-config-version`.
2. The supported values are `v1alpha1` and `v1`.
3. The mutating webhook defaults the annotation to `v1` only on `CREATE`.
   It never defaults a missing annotation on `UPDATE`. Existing RBGs without
   the annotation remain `v1alpha1`, while an UPDATE that rebuilds an object
   and omits the key preserves the stored value. An explicitly empty value is
   treated as missing on `CREATE`. Objects whose deprecated stateful roles
   cannot serialize compact content default to `v1alpha1`; `v1` remains
   rejected for those roles.
4. Setting `v1alpha1` at creation is the documented opt-out.
5. For an RBGSet, the webhook defaults the annotation in
   `spec.groupTemplate.annotations`. Child RBGs inherit that value, including
   the historical `v1alpha1` default when a set predates the annotation.
   Direct edits to a child annotation are overwritten, and a template UPDATE
   that omits the key preserves the stored value. Set the key to `v1alpha1`
   explicitly to roll the set and its children back.
6. Admission rejects values other than `v1alpha1` and `v1`.
7. Admission rejects `v1` for RBGs containing deprecated stateful roles.
8. `RBGPredicate` watches the annotation, so annotation-only changes enqueue
   the RBG.
9. The controller uses the annotation to select the ConfigMap content version.
10. If the ConfigMap is deleted, the controller rebuilds the content for the
    annotated version at the next reconcile.
11. v1alpha1 content has no `apiVersion`.
12. v1 content writes `apiVersion: v1`.

Legacy discovery support is removed before v1 is implemented. The controller
stops reading and writing `discovery-config-mode` and maintains `<rbg>` for
every RBG with a stateful role. Existing `legacy` RBGs therefore receive
`<rbg>` at their next reconcile; their Pod templates are unchanged because
v0.7 and later already mount `<rbg>`.

### Deprecated Workloads

The compact format supports RoleInstanceSet only. Admission rejects `v1` for
RBGs containing deprecated stateful roles. Existing objects with those roles
remain on `v1alpha1`, including when admission is bypassed.

### Consumer Decoding Contract

Consumers that read `/etc/rbg/config.yaml` must support both coexisting
versions:

1. Missing `apiVersion` means v1alpha1.
2. `apiVersion: v1` means v1.
3. Any other `apiVersion` value is rejected.

The mounted file is a whole-volume ConfigMap. Changing an existing RBG's
annotation updates the mounted file under running Pods within the kubelet sync
period. Consumers that re-read the file at runtime must implement both
versions before an operator migrates an RBG.

The best-practice documentation includes a version-aware decoding example.
The reference decoder used by tests decodes v1 through the same range-jumping
rules defined below.

| Content version | Marker | Meaning |
|---|---|---|
| v1alpha1 | missing `apiVersion` | current full enumeration |
| v1 | `apiVersion: v1` | compact format defined by this KEP |

### v1 Content Format

v1 serializes stateful roles only, matching the current controller.

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
| `roles[].excludedOrdinals` | string | normalized committed holes below the highest selected member; not the user annotation value |
| `roles[].ports` | map | role-level `servicePorts`, with keys derived by the rules below |
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

These are the only placeholders.

Required placeholders by pattern:

| Pattern | Required placeholders |
|---|---|
| `StandalonePattern` | `{roleIndex}` only |
| `CustomComponentsPattern` | `{roleIndex}` and `{componentIndex}` |
| `LeaderWorkerPattern` | `{roleIndex}` and `{componentIndex}` |

Unknown placeholders and placeholders missing from a pattern's required set
are controller bugs and are rejected by unit tests.

Replacement rules:

1. Perform string replacement.
2. Use `offset` to express numbering offsets.

Port keys use the current builder rules:

1. lowercase the ServicePort name
2. replace `-` with `_`
3. use `port<N>` when the ServicePort has no name

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
- Worker `componentIndex` ranges from `offset` to `offset + size - 1`.
- The template remains a string replacement.
- Behavior matches `FormatComponentPodName()`.

StandalonePattern:

```yaml
components:
  - name: prefill
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

`base` records the address produced by the current builder. DNS resolvability
still follows `sharedServiceSelection`; under `LeaderOnly`, worker addresses
are present but are not network identities.

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

`excludedOrdinals` records holes in committed membership. It is not a copy of
the user-facing `excluded-ordinals` annotation. Committed holes remain after
that annotation is removed until the controller commits a new membership target.

Range grammar:

- Entries are separated by commas.
- Each entry is either `n` or `a-b`.
- Ranges are inclusive.
- Values are nonnegative decimal integers.
- The maximum value is `2147483647`.
- Entries are sorted in ascending order.
- Overlapping and adjacent ranges are merged.
- The canonical form contains no spaces.

The selected members are the first `members` ordinals, in ascending order,
that no range in `excludedOrdinals` covers. A decoder finds them by jumping
over ranges and never expands a range into individual ordinals.

Decoding rules:

1. Reversed ranges such as `5-3` are rejected.
2. Unsorted entries are accepted and normalized by sorting and merging.
3. Values below `0` and above `2147483647` are rejected.
4. `members: 0` produces an empty member list.

#### Write and Replace

The controller writes the ConfigMap before role reconciliation:

1. compute membership and address templates
2. generate content according to the discovery-config-version annotation
3. write `<rbg>/config.yaml`
4. when the target version is `v1`, replace v1alpha1 content

If writing fails, reconcile returns an error and retries, matching current
discovery behavior.

Stored-object size is enforced by the API server's 1 MiB ConfigMap limit. A
rejected write follows the existing reconcile error and retry path.

### Migration

1. Remove legacy discovery support and deploy the controller that supports v1.
2. Existing RBGs without the annotation keep v1alpha1 content. Existing
   `legacy` RBGs begin receiving `<rbg>` at their next reconcile.
3. New RBGs default to `v1` through the mutating webhook. New RBGSets carry
   the annotation in `spec.groupTemplate.annotations`.
4. To migrate an existing RBG, set:

   ```yaml
   metadata:
     annotations:
       rbg.workloads.x-k8s.io/discovery-config-version: v1
   ```

5. Wait for reconcile and verify that ConfigMap content is `apiVersion: v1`.

Rollback:

1. Change the annotation back to `v1alpha1`.
2. The controller estimates the serialized v1alpha1 size. If it exceeds
   1 MiB, the rollback is rejected and v1 content is retained. The controller
   reports the configuration blocker and continues role reconciliation, so
   reducing membership can make a retry fit.
3. Otherwise, wait for reconcile and verify that ConfigMap content is
   v1alpha1.

### Test Plan

#### Unit tests

- v1 field completeness.
- Behavior for the annotation missing, `v1alpha1`, and `v1`.
- New-RBG defaulting, including the `v1alpha1` opt-out.
- CREATE-only webhook defaulting; missing annotations are never defaulted to
  `v1` on update, and a rebuilt UPDATE preserves the stored value.
- RBGSet annotation propagation through `spec.groupTemplate.annotations`.
- `RBGPredicate` enqueueing annotation-only changes.
- Admission rejecting invalid annotation values and `v1` for deprecated
  stateful roles.
- Parsing missing `apiVersion` as v1alpha1.
- Unknown `apiVersion` rejection.
- Address templates for the three patterns.
- LeaderWorkerPattern leader and worker offsets.
- CustomComponentsPattern Service overrides.
- Required placeholder sets per pattern and unknown-placeholder rejection.
- Port-key derivation: lowercase, `-` to `_`, and `port<N>` for unnamed
  ports.
- Membership selection and `excludedOrdinals` encoding.
- Long-range decoding with range jumping, including
  `members: 1, excludedOrdinals: "0-2147483645"`.
- Stateful-only serialization with mixed stateful and stateless roles.
#### Integration tests

- The same `<rbg>` ConfigMap is replaced with v1 content.
- After ConfigMap deletion, the controller rebuilds the content selected by
  the discovery-config-version annotation.
- ConfigMap count remains 1.
- A write failure fails reconcile and retries.
- A ConfigMap write rejected by the API server's 1 MiB limit fails reconcile
  and retries.
- ConfigMap content is v1 after migration.
- Existing RBGs without the annotation remain v1alpha1.
- Existing `legacy` RBGs receive `<rbg>` after the mode selector is removed.
- RBGSet children inherit the version annotation from the GroupTemplate.
- Annotation-only changes are reconciled.
- Rollback is rejected when v1alpha1 would exceed 1 MiB.

#### e2e tests

- After migration, `<rbg>/config.yaml` contains `apiVersion: v1`.
- Addresses for the three patterns match the current builder output.
- LeaderWorkerPattern worker addresses start at 1.
- v1alpha1 and v1 consumer examples in the English and Chinese best-practice
  guides both decode the mounted file.
- Controller downgrade behavior matches the matrix below.

### Graduation Criteria

#### Alpha

- The controller writes v1 content.
- The unit, integration, and e2e tests above are complete.
- A 2000-instance scale test shows the stored ConfigMap stays below the
  API server's 1 MiB limit and reconciliation succeeds.

#### Beta

- Documentation for direct v1 consumption is complete.
- The reference decoder is complete.
- No unresolved reports show v1 content differing from the builder output.

#### GA

- Two releases of stable behavior.
- v1alpha1 and v1 behavior remain stable.

#### Deprecation

- Not part of this KEP. v1alpha1 and v1 coexist here.

### Upgrade and Downgrade Strategy

See [Migration](#migration).

Upgrade changes only discovery ConfigMap content. Pod templates, volumes, and
mounts remain unchanged.

Downgrade matrix:

| Starting state | Older controller behavior | Result |
|---|---|---|
| existing RBG with `discovery-config-mode: refine` | ignores the version annotation and writes v1alpha1 | rollback succeeds while the enumeration fits 1 MiB |
| existing RBG whose v1alpha1 enumeration exceeds 1 MiB | v1alpha1 write is rejected | reconcile stops before role reconciliation until the controller is upgraded |
| new RBG created after the mode selector is removed | has no `discovery-config-mode` and is pinned to `legacy` because it has status | `<rbg>` stops updating and its content freezes |
| any RBG upgraded again | the version annotation is still present | the new controller restores the selected version |

In-place downgrade of RBGs created after the selector is removed is
unsupported; recreate those RBGs after restoring a compatible controller.

### Version Skew Strategy

`apiVersion` in the ConfigMap and the discovery-config-version annotation in
RBG metadata let consumers and controllers identify the selected content.

Older controllers ignore the annotation and continue writing v1alpha1 content.
They do not understand v1 and must not be used to reconcile RBGs created
after the selector was removed.

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

#### How can this feature be enabled or disabled in a live cluster?

Set the discovery-config-version annotation to `v1`. Set it back to
`v1alpha1` to roll back.

#### Does enabling the feature change any default behavior?

Pod templates, volumes, and mounts remain unchanged. The mounted ConfigMap
content changes under running Pods within the kubelet sync period, so
consumers that re-read `/etc/rbg/config.yaml` must support both versions
before migration.

#### Can the feature be disabled after it is enabled?

Set the discovery-config-version annotation back to `v1alpha1`. The rollback
is accepted only when the serialized v1alpha1 content fits within 1 MiB.

#### What happens if the feature is enabled again after being disabled?

The controller deterministically rebuilds v1 content from the RBG's
discovery-config-version annotation and committed membership.

#### Are there tests for enabling and disabling the feature?

Yes. Integration and e2e tests cover migration and rollback.

### Rollout, Upgrade, and Rollback Planning

#### How can a rollout or rollback fail, and can it affect running workloads?

ConfigMap writes can fail. Reconcile returns an error and retries. Pod
templates are unchanged, but a live-mounted file changes within the kubelet
sync period; a v1alpha1-only consumer that re-reads it can fail.

#### Which metrics should trigger rollback?

- RBG controller reconcile error rate.
- ConfigMap write failure count.

#### Were upgrade and rollback tested?

Yes. The test plan covers migration and rollback.

#### Does the rollout deprecate or remove anything?

Yes. Legacy discovery support and the `discovery-config-mode` selector are
removed. Existing `legacy` RBGs begin receiving `<rbg>` at their next
reconcile.

### Monitoring Requirements

#### How can an operator determine whether the feature is in use?

Inspect the discovery-config-version annotation and the ConfigMap's
`apiVersion`.

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

The existing ConfigMap reconcile plus one ConfigMap list/watch. The controller
labels each discovery ConfigMap with the group-name label and restricts its
cache to that label, so unrelated ConfigMaps are not cached. Steady-state
reconciles issue no ConfigMap request; a content change performs one uncached
ownership read followed by the server-side apply.

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

No. The stored object is subject to the API server's 1 MiB ConfigMap limit.

### Troubleshooting

#### How does the feature behave when the API server or etcd is unavailable?

Reconcile fails and retries with the existing backoff. Running Pods are
unaffected.

#### What are the other known failure modes?

- The API server rejects a ConfigMap write because it exceeds the 1 MiB limit:
  - Detection: reconcile error.
  - Mitigation: reduce the topology or keep the RBG on v1alpha1 if that
    format remains writable.
- Content version mismatch:
  - Detection: compare the discovery-config-version annotation with ConfigMap
    `apiVersion`.
  - Mitigation: set the desired version and wait for reconcile.

#### What should be done when SLOs are not met?

Inspect RBG Warning Events, controller logs, and the ConfigMap.

## Implementation History

- 2026-11-13: Initial KEP.
- 2026-11-17: Changed to compact content design, added the version
  annotation, and corrected LeaderWorkerPattern addresses and worker offsets.
- 2026-11-19: Added CREATE-only annotation defaulting, RBGSet propagation,
  deprecated-workload rejection, consumer decoding rules, rollback and
  downgrade matrices, and stateful-only scope.

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
   - reduces object size, but consumers must understand compressed content.
5. **Runtime DNS / EndpointSlice**
   - provides readiness information, but lacks static ordinal topology.

## Infrastructure Needed (Optional)

None.

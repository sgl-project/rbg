# KEP-275: Enhanced Per-Replica Headless Service Policy for RoleBasedGroup

<!-- toc -->
- [Release Signoff Checklist](#release-signoff-checklist)
- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [User Stories (Optional)](#user-stories-optional)
        - [Story 1](#story-1)
        - [Story 2](#story-2)
        - [Story 3](#story-3)
    - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
    - [API](#api)
    - [Naming Rationale](#naming-rationale)
    - [Behavior by Policy](#behavior-by-policy)
    - [Supported Matrix](#supported-matrix)
    - [Service Naming and Ownership](#service-naming-and-ownership)
    - [Rollout and Transition Behavior](#rollout-and-transition-behavior)
    - [Discovery and Environment Variables](#discovery-and-environment-variables)
    - [Test Plan](#test-plan)
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
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure Needed (Optional)](#infrastructure-needed-optional)
<!-- /toc -->

## Release Signoff Checklist

<!--
**ACTION REQUIRED:** In order to merge code into a release, there must be an
issue in [kubernetes/enhancements] referencing this KEP and targeting a release
milestone **before the [Enhancement Freeze](https://git.k8s.io/sig-release/releases)
of the targeted release**.

For enhancements that make changes to code or processes/procedures in core
Kubernetes—i.e., [kubernetes/kubernetes], we require the following Release
Signoff checklist to be completed.

Check these off as they are completed for the Release Team to track. These
checklist items _must_ be updated for the enhancement to be released.
-->

Items marked with (R) are required *prior to targeting to a milestone / release*.

- [ ] (R) Enhancement issue in release milestone, which links to KEP dir in [kubernetes/enhancements] (not the initial KEP PR)
- [ ] (R) KEP approvers have approved the KEP status as `implementable`
- [ ] (R) Design details are appropriately documented
- [ ] (R) Test plan is in place, giving consideration to SIG Architecture and SIG Testing input (including test refactors)
    - [ ] e2e Tests for all Beta API Operations (endpoints)
    - [ ] (R) Ensure GA e2e tests meet requirements for [Conformance Tests](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/conformance-tests.md)
    - [ ] (R) Minimum Two Week Window for GA e2e tests to prove flake free
- [ ] (R) Graduation criteria is in place
    - [ ] (R) [all GA Endpoints](https://github.com/kubernetes/community/pull/1806) must be hit by [Conformance Tests](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/conformance-tests.md) within one minor version of promotion to GA
- [ ] (R) Production readiness review completed
- [ ] (R) Production readiness review approved
- [ ] "Implementation History" section is up-to-date for milestone
- [ ] User-facing documentation has been created in [kubernetes/website], for publication to [kubernetes.io]
- [ ] Supporting documentation—e.g., additional design documents, links to mailing list discussions/SIG meetings, relevant PRs/issues, release notes

<!--
**Note:** This checklist is iterative and should be reviewed and updated every time this enhancement is being considered for a milestone.
-->

[kubernetes.io]: https://kubernetes.io/
[kubernetes/enhancements]: https://git.k8s.io/enhancements
[kubernetes/kubernetes]: https://git.k8s.io/kubernetes
[kubernetes/website]: https://git.k8s.io/website


## Summary

This KEP proposes a new role-level networking field `NetworkConfig.SubdomainPolicy` with two supported policies:

- `Shared` — one headless Service per role (current default behavior)
- `UniquePerReplica` — one headless Service per `RoleInstance`

RBG currently creates one headless Service per role. That works for simple cases, but it is not enough for some large-scale cases.

In particular, `RoleInstanceSet` with `leaderWorkerPattern` has two practical needs that are not supported by the current controller:

1. Segregated service per replica to enable custom load balancing algorithms (e.g., cache-aware routing) instead of native round-robin.
2. At larger scale, a single shared headless Service becomes a bottleneck when aggregating every Pod of the role. Setting one headless Service per replica — as `LeaderWorkerSet` does — gives each replica its own DNS domain.


## Motivation

Currently, one role corresponds to one headless Service.

- The Service reconciler builds a selector at role scope.
- `RoleInstanceSet` writes a static `ServiceName` for the leader component in leader-worker mode.

For some gateways, informers discover services by label and redirect requests to each service rather than individual pods.

At larger scale, it is often more natural to treat each replica as its own DNS boundary. One headless Service per `RoleInstance` makes that possible.

More importantly, direct Pod DNS access through headless Services depends on the Pod `subdomain` matching the governing Service name.
This means `UniquePerReplica` cannot be handled by extra Services alone — it changes the actual network identity used by the Pods,
so this KEP must define rollout behavior and compatibility carefully.

### Goals

1. Introduce a clear API for selecting the headless Service topology of a role while keeping current behavior as the default.
2. Support one headless Service per `RoleInstance` in `RoleInstanceSet` with `leaderWorkerPattern`.
3. Ensure that transitions between policies trigger a proper rollout (Pod recreation) since Pod DNS identity changes.

### Non-Goals

1. Supporting `standalonePattern` or `customComponentsPattern`.
2. Supporting `Deployment`, `StatefulSet`, `LeaderWorkerSet`, or other workload types.
3. Introducing `LeaderOnly` service selection in this KEP (see KEP-260).
4. Keeping every policy transition non-rolling. Transitions that change Pod DNS identity must trigger a rollout.

## Proposal

Add a new optional `NetworkConfig` field in `RoleSpec` with a `SubdomainPolicy` that supports two policies:

- `Shared` (default): Keeps the current model — one shared headless Service for the role, selecting all Pods in the role.
- `UniquePerReplica`: Creates a separate headless Service per `RoleInstance`, selecting only Pods in that replica. Each replica gets its own subdomain. This policy is only supported for `RoleInstanceSet` with `leaderWorkerPattern`.


### User Stories (Optional)

#### Story 1

When using `sglang` or `vllm` to serve large models, I want the requests to target a specific replica's service instead of a random one.
The routing algorithm may be `cache aware` or `least running length`, which is not supported through Kubernetes native kube-proxy service.

#### Story 2

I have a large cluster with many replicas. The shared headless Service's endpoint list is so large that CoreDNS may struggle.
I want each replica to have its own headless Service so that DNS and endpoint lookups remain fast and isolated.

#### Story 3

I was using `LeaderWorkerSet` and want to migrate to `RoleBasedGroup` using `RoleInstanceSet` with `leaderWorkerPattern`.
I prefer a smooth migration that preserves the per-replica DNS model without changing the service discovery pattern.

## Design Details

### API

Add a role-level networking configuration under `RoleSpec`:

```go
type RoleSpec struct {
    // NetworkConfig defines role-level networking configuration.
    // +optional
    NetworkConfig *NetworkConfig `json:"networkConfig,omitempty"`
}

type NetworkConfig struct {
    // SubdomainPolicy determines how headless Services are created for the role.
    // Defaults to Shared when omitted.
    // +optional
    // +kubebuilder:default=Shared
    // +kubebuilder:validation:Enum={Shared,UniquePerReplica}
    SubdomainPolicy *SubdomainPolicy `json:"subdomainPolicy,omitempty"`
}

type SubdomainPolicy string

const (
    // SubdomainShared - one shared headless Service for the entire role.
    // DNS shape: <pod-name>.<shared-service-name>.<namespace>.svc.cluster.local
    // Example:
    //   replica 0: my-rbg-0-0.my-rbg-role, my-rbg-0-1.my-rbg-role
    //   replica 1: my-rbg-1-0.my-rbg-role, my-rbg-1-1.my-rbg-role
    SubdomainShared SubdomainPolicy = "Shared"

    // SubdomainUniquePerReplica - one headless Service per RoleInstance.
    // DNS shape: <pod-name>.<role-instance-name>.<namespace>.svc.cluster.local
    // Example:
    //   replica 0: my-rbg-0-0.my-rbg-0, my-rbg-0-1.my-rbg-0
    //   replica 1: my-rbg-1-0.my-rbg-1, my-rbg-1-1.my-rbg-1
    SubdomainUniquePerReplica SubdomainPolicy = "UniquePerReplica"
)
```

Default:
- If the field is unset (`nil`), the policy defaults to `Shared`.

### Naming Rationale

The field is named `SubdomainPolicy` because the core semantic this field controls is the Pod `subdomain` — which in turn determines the governing headless Service name and DNS identity. While the implementation also affects service count, ownership, selector granularity, and discovery generation, all of these are consequences of the subdomain assignment. The name focuses on the user-facing concept: "which subdomain do my Pods get?"

### Behavior by Policy

#### `Shared`

This is the current behavior and remains the default.

- One shared headless Service per role; the selector includes all Pods in the role.
- Existing workloads require no spec changes and see no behavioral change.
- The shared Service is the only headless Service in steady state.

#### `UniquePerReplica`

- One headless Service per `RoleInstance`; each Service selects only the Pods belonging to that replica.
- The role-level shared headless Service is **removed** in steady state after rollout completes.
  - Rationale: Retaining the shared Service would create ambiguity — Pods' `subdomain` points to the per-replica Service, so the shared Service would have no endpoints and serve no purpose. Removing it avoids stale objects.
- Each Pod's `subdomain` is set to its `RoleInstance` name, enabling direct Pod DNS access through the per-replica Service.

### Supported Matrix

| Workload Kind | Role Pattern | SubdomainPolicy | Supported |
|---------------|-------------|-----------------|-----------|
| `RoleInstanceSet` | `leaderWorkerPattern` | `Shared` | Yes (default) |
| `RoleInstanceSet` | `leaderWorkerPattern` | `UniquePerReplica` | Yes |
| `RoleInstanceSet` | `standalonePattern` | `Shared` | Yes (default) |
| `RoleInstanceSet` | `standalonePattern` | `UniquePerReplica` | No — reject at admission |
| `StatefulSet` | any | `UniquePerReplica` | No — reject at admission |
| `Deployment` | any | `UniquePerReplica` | No — reject at admission |

**Eligibility rule**: `UniquePerReplica` requires `RoleInstanceSet` + `leaderWorkerPattern`, because this is the only combination that provides stable per-replica identity (`RoleInstance`) suitable for per-replica Service ownership.

Unsupported combinations are rejected at admission time via CEL validation, not silently falling back to `Shared`.

### Service Naming and Ownership

#### `Shared`
- Service name: Uses the existing shared naming convention (derived from RBG name and role name).
- Owner: The shared Service is owned by the `RoleInstanceSet`.
- No per-replica Services exist in steady state.

#### `UniquePerReplica`
- Service name: Each per-instance Service name equals the `RoleInstance` name.
- Owner: Each per-instance Service is owned by the corresponding `RoleInstance`.
  - Rationale: Ownership by `RoleInstance` ensures automatic garbage collection when a replica is scaled down or deleted. If Services were owned by `RoleInstanceSet` with label-based association, GC would depend on controller-level cleanup logic, which is timing-sensitive and error-prone during rapid scale-down. `RoleInstance` ownership leverages Kubernetes built-in owner-reference GC.
- Selector: Matches the instance identity label, plus group and role labels for safety.
- The role-level shared Service is deleted after all Pods have transitioned to per-replica Services.

### Rollout and Transition Behavior

Both directions require a `RoleInstanceSet` rollout because Pod DNS identity (`subdomain`) changes:

#### `Shared` → `UniquePerReplica`

1. The controller creates per-replica Services for each `RoleInstance`.
2. `RoleInstanceSet` triggers a rolling update. Pods are recreated with `subdomain` set to their `RoleInstance` name.
3. Old and new Services coexist during rollout. Once all Pods are updated, the old shared Service is deleted.

#### `UniquePerReplica` → `Shared`

1. The controller creates (or re-creates) the role-level shared headless Service.
2. `RoleInstanceSet` triggers a rolling update. Pods are recreated with `subdomain` set to the shared Service name.
3. Once all Pods are updated, per-replica Services are deleted.

#### Convergence Conditions

- An old Service is safe to delete when: all `RoleInstance` Pods reference the new governing Service (i.e., their `subdomain` matches the target policy), and all `RoleInstances` report Ready.
- On rollout failure: both old and new Services persist. Partial convergence is allowed — each Pod's DNS identity is consistent with its own `subdomain` field. The controller will not delete old Services until convergence is complete.
- In-place DNS identity mutation is not supported. Any policy transition **must** recreate Pods to update their `subdomain` field.

### Discovery and Environment Variables

Current discovery and environment injection assume one role-level service name. Affected artifacts include:

| Artifact | Under `Shared` | Under `UniquePerReplica` |
|----------|----------------|--------------------------|
| Discovery ConfigMap | Role-scoped: one service name per role | Replica-scoped: one service name per `RoleInstance` |
| Environment variables (e.g., `RBG_LEADER_ADDRESS`) | Points to shared Service name | Points to per-replica Service name (local replica) |
| Service name derivation helpers | Single value from role name | Per-replica value from `RoleInstance` name |
| Pod DNS FQDN | `<pod-name>.<shared-svc>.<ns>.svc.cluster.local` | `<pod-name>.<role-instance-name>.<ns>.svc.cluster.local` |

Under `Shared`, discovery remains role-scoped. Pods keep using the shared headless Service name, and direct Pod DNS names keep the existing shape:

```
<pod-name>.<shared-service-name>
```

Under `UniquePerReplica`, discovery becomes replica-scoped. Each `RoleInstance` uses its own governing Service name, so direct Pod DNS names change to:

```
<pod-name>.<role-instance-name>
```

Because of this, `UniquePerReplica` is not just an additive Service creation policy. It changes the service name used by Pods,
so discovery artifacts and environment variables that embed service addresses must be generated according to the active `SubdomainPolicy`.

**Migration for existing consumers**: Consumers that assume "one role = one service name" must be updated to iterate over per-replica service names when `UniquePerReplica` is active. The discovery ConfigMap will contain all per-replica service names, so consumers can derive addresses from it.

### Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

##### Unit tests

- API defaulting: unset field defaults to `Shared`
- Per-replica Service name derivation for `UniquePerReplica`
- RoleInstance component `ServiceName` propagation under `UniquePerReplica`
- Cleanup of old Services after transition completes
- Validation rejects unsupported pattern + `UniquePerReplica` combinations
- Existing workloads without `NetworkConfig` see zero behavioral change

##### Integration tests

- `Shared` creates one headless Service per role and includes all Pods
- `UniquePerReplica` creates one headless Service per replica
- Pods resolve correctly under shared subdomain
- Pods resolve correctly under replica-specific subdomain
- `Shared` → `UniquePerReplica` triggers rollout and converges; old Service is deleted
- `UniquePerReplica` → `Shared` triggers rollout and converges; per-replica Services are deleted
- Mid-rollout failure: old and new Services coexist without cross-contamination
- Scale up: new replica gets its own Service under `UniquePerReplica`
- Scale down: per-replica Service is garbage-collected via owner reference

##### e2e tests

- Direct DNS resolution works in both policies
- Policy transitions preserve availability according to rollout strategy
- Owner deletion triggers proper Service GC

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

###### Does enabling the feature change any default behavior?

No. The default remains `Shared`. Existing workloads without the `NetworkConfig` field see no change.

###### Can the feature be disabled once it has been enabled?

Yes. Users can set the policy back to `Shared`. This triggers a rollout to recreate Pods with the shared subdomain.

###### Are there any tests for feature enablement/disablement?

Yes. Both directions (`Shared` ↔ `UniquePerReplica`) are covered in integration and e2e tests.


## Drawbacks

- The feature adds another policy dimension to role networking, and `UniquePerReplica` increases Service object count (one per replica instead of one per role).
- The rollout matrix becomes more complex because policy transitions require Pod recreation.
- Discovery generation must handle both role-scoped and replica-scoped modes.

## Alternatives

### Make per-replica Services non-rolling (avoid Pod restart)

This is not possible. Pods must use the per-replica service name as their `subdomain` for DNS resolution to work. Changing `subdomain` requires Pod recreation, so `UniquePerReplica` cannot be applied without a rollout.

### Let users create custom Services outside RBG

Possible, but it pushes a runtime-specific correctness problem to every user and makes the platform behavior inconsistent. Additionally, custom Services do not solve the `subdomain` problem — Pods still need their `subdomain` updated to match the governing Service.

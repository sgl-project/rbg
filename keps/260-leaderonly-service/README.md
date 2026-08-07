
# KEP-260: Leader-Only Shared Service for RoleBasedGroup

<!--
This is the title of your KEP. Keep it short, simple, and descriptive. A good
title can help communicate what the KEP is and should be considered as part of
any review.
-->

<!--
A table of contents is helpful for quickly jumping to sections of a KEP and for
highlighting any additional information provided beyond the standard KEP
template.

Ensure the TOC is wrapped with
  <code>&lt;!-- toc --&rt;&lt;!-- /toc --&rt;</code>
tags, and then generate with `hack/update-toc.sh`.
-->

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
    - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
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

This KEP proposes a new optional field `SharedServiceSelection` (of type `SharedServiceSelectionPolicy`) under `LeaderWorkerPattern`, to control which Pods are selected by the existing shared headless Service of a role.

The new field has two values:

- `All` — the Service selector targets every Pod of the role, and every component of the role instance is bound to the Service, so each Pod is addressable at `<pod-name>.<service-name>.<namespace>.svc`
- `LeaderOnly` — the Service selector only targets leader Pods, and only the leader component is bound to the Service

On a `RoleInstanceSet` role the field defaults to `LeaderOnly`, which matches the common inference topology where only leader Pods serve requests. This changes the previous behavior of an unset field, so see [Upgrade Considerations](#upgrade-considerations).


## Motivation

RBG currently creates one shared headless Service per role, and the Service selector includes all Pods of that role.

That behavior is acceptable when every Pod behind the Service is a real serving endpoint. However, for large-scale inference engines where only leader Pods accept external requests, routing traffic to all Pods causes request failures.

This problem occurs with runtimes such as `sglang`. In a cross-node engine, worker Pods may only run a dummy API server, and exposing those Pods through the role-level Service causes requests to be routed to non-functional endpoints.

The problem here is different from the per-replica headless Service problem. This KEP aims to keep the role shared headless Service while controlling which Pods are targeted by it.


- the shared Service name never changes, and the controller only needs to update the Service selector
- the leader Pod's `ServiceName`, `subdomain`, and DNS identity never change

The policy also decides which components of the role instance are bound to the shared Service, so
worker Pods gain or lose their `hostname`/`subdomain` when it changes.

### Goals

1. Introduce a clear API for controlling which Pods of a role participate in its shared headless Service.
2. Default to `LeaderOnly`, which matches the common inference topology where only leader Pods serve requests, and support `All` for roles whose worker Pods must be reachable by DNS.
3. Ensure that switching between `All` and `LeaderOnly` never renames or recreates the shared Service, and never changes the leader Pod's identity.

### Non-Goals

1. Introducing per-replica headless Services or per-replica subdomain policy.
2. Changing the shared Service name, or the leader Pod's `ServiceName`, `subdomain`, and FQDN.
3. Supporting patterns that do not define a leader component.


## Proposal

Add an optional `SharedServiceSelection` field (of type `SharedServiceSelectionPolicy`) under `LeaderWorkerPattern`.

- `All` keeps the shared headless Service targeting every Pod in the role, and binds every component of the role instance to it, so worker Pods are reachable at `<pod-name>.<service-name>.<namespace>.svc` as well.
- `LeaderOnly` keeps the same shared headless Service object and the same Service name, but narrows its selector so that only leader Pods are exposed, and binds only the leader component to it.

The feature is intended for `RoleInstanceSet + leaderWorkerPattern`, where the role has a clear leader component and 
where only leader Pods should serve requests.

Switching between `All` and `LeaderOnly` updates the shared Service in place:

- no Service rename, no Service recreation, and no change to the leader Pod's DNS identity
- worker Pods gain or lose their `hostname`/`subdomain`, which are immutable Pod fields, so the switch triggers a `RoleInstanceSet` rollout

### User Stories (Optional)

#### Story 1

As an inference engineer using `sglang` in a multi-node pattern, I want the shared role Service to expose only leader Pods so that
followers that do not run a fully functional serving endpoint do not receive external traffic.

#### Story 2

As an operator building a gateway, I want one stable shared Service per role, but I only want the real serving Pods to appear in that Service's endpoints. 
This lets the gateway keep using Service-level discovery without routing requests to worker Pods that should only participate in internal execution.


#### Story 3
As a platform engineer, although we do support a pod-level model gateway (e.g. `sglang` model gateway), we still need a fallback in case the gateway is absent.
However, I cannot control user behavior, and once they use the `sglang` engine or `vllm` in headless mode to serve a model across nodes, I need to
configure the service manually instead of automatically.

## Design Details

### API

```go
type LeaderWorkerPattern struct {
    // SharedServiceSelection indicates the service policy of the role. When unset, the controller
    // treats a RoleInstanceSet role as LeaderOnly and any other workload type as All.
    // +optional
    // +kubebuilder:validation:Enum=All;LeaderOnly
    SharedServiceSelection *SharedServiceSelectionPolicy `json:"sharedServiceSelection,omitempty"`
}

type SharedServiceSelectionPolicy string

const (
    // SharedServiceSelectionAll - All pods would be routed to
    SharedServiceSelectionAll SharedServiceSelectionPolicy = "All"

    // SharedServiceSelectionLeaderOnly - The headless service would only target at the leaders
    SharedServiceSelectionLeaderOnly SharedServiceSelectionPolicy = "LeaderOnly"
)
```

Default:

- If the field is unset (`nil`) on a `RoleInstanceSet` role, the controller treats it as
  `LeaderOnly` (`RoleSpec.GetSharedServiceSelection`).
- On any other workload type the controller resolves to `All` regardless of the stored value. Those
  roles have no component-name label on their Pods, so a narrowed selector would match no Pod at
  all. The CEL rule below already rejects an explicit `LeaderOnly` there at admission, but the
  controller does not depend on that: a cluster whose CRDs lag the controller is still safe.
- The default is applied by the controller rather than by a CRD `default`, so that the stored
  field stays unset. A CRD default would be written into every `leaderWorkerPattern` role before
  validation runs, and the validation rule below would then reject every role that uses another
  workload type, even one that never set the field.

### Validation

A CEL rule on `RoleSpec` keeps `LeaderOnly` restricted to the combination that supports it:

```yaml
x-kubernetes-validations:
  - rule: >-
      !has(self.leaderWorkerPattern) ||
      !has(self.leaderWorkerPattern.sharedServiceSelection) ||
      self.leaderWorkerPattern.sharedServiceSelection != 'LeaderOnly' ||
      !has(self.annotations) ||
      !('rbg.workloads.x-k8s.io/role-workload-type' in self.annotations) ||
      self.annotations['rbg.workloads.x-k8s.io/role-workload-type'] == 'workloads.x-k8s.io/v1alpha2/RoleInstanceSet'
    message: "leaderWorkerPattern.sharedServiceSelection=LeaderOnly is only supported for RoleInstanceSet + leaderWorkerPattern"
```

Unsupported combinations are therefore rejected at admission time instead of silently having no
effect. An unset field stays valid on every workload type, which is what keeps the controller-side
default compatible with this rule.

### Behavior

#### `All`

- one shared headless Service is created for the role and the Service selector includes leader and worker Pods
- every component of the role instance is bound to the shared Service, so each Pod gets `hostname` = Pod name and `subdomain` = shared Service name and is addressable at `<pod-name>.<service-name>.<namespace>.svc`

#### `LeaderOnly`

This is the default. It keeps the shared Service model but narrows the endpoint set.

- the shared Service selector includes only leader Pods, and worker Pods are no longer exposed through the shared Service
- only the leader component is bound to the shared Service; worker Pods have no `hostname`/`subdomain` and are not addressable through it
- the leader Pod's `ServiceName`, `subdomain`, and FQDN are the same under both policies

### Supported Pattern

The supported scope of this KEP is:

- `RoleInstanceSet + leaderWorkerPattern`

The policy only drives the shared headless Service that RBG manages for the role. `LeaderWorkerSet` roles are governed by the Service that `LeaderWorkerSet` creates itself, so `LeaderOnly` has no meaning there and is rejected at admission. Outside the supported scope the controller resolves the policy to `All` whatever the stored value is, which is the behavior those roles already had, so admission rejection is a second line of defence rather than the only one.

### Rollout and Transition Behavior

`All -> LeaderOnly` and `LeaderOnly -> All` update the shared Service selector in place.

These transitions do not require:

- Service renaming or recreation
- any change to the leader Pod's DNS identity

They do require a `RoleInstanceSet` rollout: the worker component gains or loses its
`serviceName`, which changes the worker Pods' `hostname`/`subdomain`. Those Pod fields are
immutable, so worker Pods cannot be updated in place and the role instances are replaced
according to the role's rollout strategy (`maxUnavailable`/`maxSurge`).

### Upgrade Considerations

Upgrading the controller changes behavior for existing `RoleInstanceSet + leaderWorkerPattern`
roles. The impact depends on what the role already stores, so the three cases differ:

**Field unset** — this is the breaking case for endpoints. The field shipped in v0.7.0 with an
unset field behaving as `All`, and every `leaderWorkerPattern` example in this repository leaves it
unset. The controller now resolves these roles to `LeaderOnly` and patches the shared Service
selector in place, so worker Pod IPs leave the Service's A record and its EndpointSlice. No Pod is
recreated: worker Pods never had a `hostname`/`subdomain` under the old behavior either, because
only the leader component carried a `serviceName`, so the worker component's `serviceName` stays
absent and the component revision is unchanged.

**Field explicitly `All`** — endpoints are unaffected, but **every worker Pod is replaced**. Under
v0.7.0 an explicit `All` never reached the RoleInstance template; the worker component had no
`serviceName`. It now gets one, which changes the component's extension spec revision, so the
in-place update path refuses the change and the worker Pods are recreated according to the role's
rollout strategy. This happens on the first reconcile after the controller image is rolled, with no
spec change from the user. That rollout is also what delivers the fix — worker Pods come back with
`hostname`/`subdomain` and become addressable at `<pod-name>.<shared-service-name>`, which is what
`All` was always supposed to mean.

**Field explicitly `LeaderOnly`** — no change. Selector and Pods are already in the target state.

In every case the shared Service keeps its name, its UID, and the leader Pod's DNS identity.

Workloads that resolve worker Pod IPs through the role's shared Service must set
`sharedServiceSelection: All` to keep working. Note that doing so — whether before or after the
upgrade — triggers the worker Pod rollout described above, because it gives worker Pods a
`subdomain` they did not have. Plan the upgrade during a window that tolerates a worker rollout for
those roles.



### Discovery and Environment Variables

Discovery artifacts and environment variables remain stable because the shared Service name does not change.

In particular:

- `RBG_LEADER_ADDRESS` keeps the same address shape, and the leader Pod's DNS name remains unchanged
- config generation that derives addresses from the shared Service name does not need a new naming mode

The only behavior changes are that worker Pods are not targeted by the shared Service endpoints under `LeaderOnly`, and that worker Pods are only addressable at `<pod-name>.<shared-service-name>` under `All`.

### Test Plan

##### Unit tests

- Effective policy resolution: on a `RoleInstanceSet` role an unset field falls back to `LeaderOnly` and explicit values are honored; on any other workload type the result is `All` whatever is stored
- Shared Service selector generation for `All` and `LeaderOnly`
- Component `serviceName` propagation: `All` binds leader and worker, `LeaderOnly` binds the leader only
- In-place Service selector update: `All` → `LeaderOnly` and `LeaderOnly` → `All`
- A `StatefulSet + leaderWorkerPattern` role keeps the unnarrowed selector

##### Integration tests

- An unset field is left `nil` in the stored object and resolved to `LeaderOnly` by the controller
- An explicit `LeaderOnly` on a `LeaderWorkerSet` role is rejected by the CEL rule, while an unset field on such a role is accepted
- `All` binds every component and every Pod carries `hostname`/`subdomain` under the shared Service
- `LeaderOnly` binds the leader only and worker Pods carry no `subdomain`
- `All` → `LeaderOnly` removes the worker component's `serviceName` from the `RoleInstanceSet` template and narrows the shared Service selector in place, keeping the same Service UID

##### e2e tests

- In leader-worker mode, `LeaderOnly` prevents worker Pods from appearing in the shared Service EndpointSlice
- Switching from `LeaderOnly` to `All` preserves the Service UID (in-place update) and replaces worker Pods with Pods that carry `hostname`/`subdomain` under the shared Service
- Switching back from `All` to `LeaderOnly` again preserves the Service UID, drops worker Pods from the EndpointSlice, and replaces them with Pods that carry no `subdomain`


## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

###### Does enabling the feature change any default behavior?

Yes, and it is a breaking change for already-running roles. A `RoleInstanceSet` role that leaves
the field unset resolves to `LeaderOnly`, so the shared Service of a leader-worker role stops
exposing worker Pods as endpoints. Pods are not affected: the leader keeps its identity and worker
Pods never had one, so no rollout is triggered. See [Upgrade Considerations](#upgrade-considerations)
for what existing workloads need to do.

###### Can the feature be disabled once it has been enabled?

Yes. Users can set the policy to `All`, which restores worker Pods as endpoints and additionally
gives them a DNS identity. Because that changes worker Pod identity, it triggers a rollout of the
role instances.

###### Are there any tests for feature enablement/disablement?

Yes. Unit and integration tests should cover both `All` and `LeaderOnly`, and the transition in both directions.


## Alternatives


### Let users create custom Services outside RBG

This is possible, but it pushes a runtime-specific correctness problem to every user and makes the platform behavior inconsistent across workloads.




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

This KEP proposes a new role-level networking field,`NetworkConfig.TargetPolicy`, to control which Pods are selected by the existing shared headless Service of a role.

The new field has two values:

- `All`
- `LeaderOnly`

`All` keeps the current behavior and remains the default. `LeaderOnly` preserves the existing shared Service name, 
but changes the Service selector so that only leader Pods are targeted by that Service.


## Motivation

RBG currently creates one shared headless Service per role, and the Service selector includes all Pods of that role.

That behavior is acceptable when every Pod behind the Service is a real serving endpoint. But it would make requests for large EP inference engine,
which only leader Pods are expected to accept, requests abnormal

This problem occurs with runtimes such as `sglang`. In a cross-node engine, follower Pods may only run a dummy API server,
and exposing those Pods through the role level Service causes requests routed to the dummy API servers.

The problem here is different from the per-replica headless Service problem. This KEP aims to keep the role shared headless Service while controlling which Pods are targeted by it.


- the shared Service name does not change, and the controller only needs to update Service selectors
- Pod `ServiceName` and `subdomain` do not change and direct Pod DNS identity does not change

This makes `LeaderOnly` a selector policy, not a service identity policy.


### Goals

1. Introduce a clear API for controlling which Pods are selected by the shared headless Service of a role.
2. Keep current behavior as the default and support a `LeaderOnly` mode for `RoleInstanceSet + leaderWorkerPattern`.
3. Ensure that switching between `All` and `LeaderOnly` does not require Pod recreation or Service renaming.

### Non-Goals

1. Introducing per-replica headless Services or per-replica subdomain policy.
2. Changing Pod network identity, `ServiceName`, or `subdomain`.
3. Supporting patterns that do not define a leader component.


## Proposal

Add an optional `SharedServiceSelectionPolicy` field under `RoleSpec.LeaderWorkerPattern`.

- `All` keeps the current shared headless Service behavior. The Service continues to select every Pod in the role.
- `LeaderOnly` keeps the same shared headless Service object and the same Service name, but narrows its selector so that only leader Pods are exposed

The feature is intended for `RoleInstanceSet + leaderWorkerPattern`, where the role has a clear leader component and 
where only leader Pods should serve requests.

Switching between `All` and `LeaderOnly` is an in-place Service update:

- no Pod restart nor `RoleInstanceSet` rollout
- no Pod DNS identity change nor Service rename

### User Stories (Optional)

#### Story 1

As an inference engineer using `sglang` in a multi-node pattern, I want the shared role Service to expose only leader Pods so that
followers that do not run a fully functional serving endpoint do not receive external traffic.

#### Story 2

As an operator building a gateway, I want one stable shared Service per role, but I only want the real serving Pods to appear in that Service's endpoints. 
This lets the gateway keep using Service-level discovery without routing requests to worker Pods that should only participate in internal execution.


#### Story 3
As a platform engineer, although we do support a pod-level model gateway, e.g. `sgalng model gateway`, we still need a fallback in case the gateway is absent.
However, I cannot control user behavior, and once they use the `sglang` engine or `vllm` in headless mode to serve a model across nodes, I need to
configure service manually instead of automatically.

## Design Details

### API

```go

type LeaderWorkerPattern struct {
    // +optional
	SharedServiceSelection  *SharedServiceSelectionPolicy  `json:"sharedServiceSelection,omitempty"`
}

type SharedServiceSelectionPolicy  string

const (
    // all pods would be routed to
    SharedServiceSelectionAll               SharedServiceSelectionPolicy = "All"

    // the headless service would only target at the leaders
    SharedServiceSelectionLeaderOnly        SharedServiceSelectionPolicy = "LeaderOnly"
)
```

Default:

- If the field is unset, the policy defaults to `All`.

### Behavior

#### `All`

This is the current behavior and remains the default.

- one shared headless Service is created for the role and the Service selector includes leader and worker Pods

#### `LeaderOnly`

This policy keeps the shared Service model but narrows the endpoint set.

- the shared Service selector includes only leader Pods, and worker Pods are no longer exposed through the shared Service
- Pod `ServiceName`, `subdomain`, and FQDN remain unchanged

### Supported Pattern

The supported scope of this KEP is:

- `RoleInstanceSet + leaderWorkerPattern`

Unsupported combinations should reject `LeaderOnly` instead of silently falling back to `All`.

### Rollout and Transition Behavior

`All -> LeaderOnly` and `LeaderOnly -> All` should be handled by updating the shared Service selector in place.

These transitions do not require:

- Pod recreation or `RoleInstanceSet` rollout
- Service renaming or DNS identity updates



### Discovery and Environment Variables

Discovery artifacts and environment variables remain stable because the shared Service name does not change.

In particular:

- `RBG_LEADER_ADDRESS` keeps the same address shape, and direct Pod DNS names remain unchanged
- config generation that derives addresses from the shared Service name does not need a new naming mode

The only behavior change is that worker Pods are no longer targeted by in the shared Service endpoints when `LeaderOnly` is enabled.

### Test Plan

##### Unit tests

- API defaulting for `All`
- Validation for unsupported combinations using `LeaderOnly`
- Shared Service selector generation for `All` and `LeaderOnly`


##### Integration tests

- `All` creates one shared headless Service per role and includes leader and worker Pods
- `LeaderOnly` creates one shared headless Service per role and includes only leader Pods
- `All <-> LeaderOnly` updates the shared Service in place

##### e2e tests

- In leader-worker mode, `LeaderOnly` prevents worker Pods from appearing in the shared Service endpoints
- Switching between `All` and `LeaderOnly` preserves availability and does not recreate Pods


## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

###### Does enabling the feature change any default behavior?

No. The default remains `All`.

###### Can the feature be disabled once it has been enabled?

Yes. Users can switch the policy back to `All`.

###### Are there any tests for feature enablement/disablement?

Yes. Unit and integration tests should cover both `All` and `LeaderOnly`, and the transition in both directions.


## Alternatives


### Let users create custom Services outside RBG

This is possible, but it pushes a runtime-specific correctness problem to every user and makes the platform behavior inconsistent across workloads.



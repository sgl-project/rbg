# KEP-NNNN: Enhanced Headless Service Policy for RoleBasedGroup

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
    - [API](#api)
    - [Behavior by Policy](#behavior-by-policy)
    - [Service Naming and Ownership](#service-naming-and-ownership)
    - [Rollout and Transition Behavior](#rollout-and-transition-behavior)
    - [Discovery and Environment Variables](#discovery-and-environment-variables)
    - [Validation and Supported Matrix](#validation-and-supported-matrix)
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

This KEP proposes a new role-level networking field,`networking.headlessServicePolicy`, with three supported policies:

- `SharedFull`
- `SharedLeaderOnly`
- `PerReplicaFull`


RBG currently creates one headless Service per role. That works for simple cases, but it is noy enough for some distributed inference runtimes.

In particular, `RoleInstanceSet + leaderWorkerPattern` has two practical needs that are not supported by the current controller:

1. Some runtimes, such as `sglang` in cross-node DP headless mode, only want leader Pods to be target by shared role Service. 
Follower Pods may run only a dummy API server and should not be treated as real serving endpoints.
2. At larger scale, a single shared headless Service can become a bottleneck to aggregate every Pod of the role. 
Setting one headless Service per replica as `leaderworkerset` does so that each replica has its own DNS domain.

This KEP intentionally does not include `PerReplicaLeaderOnly`. That behavior is useful, but it introduces another service identity mode and a complex larger rollout reconcile.

## Motivation

Currently, one role corresponds to one headless Service.

- The Service reconciler builds a selector at role scope.
- `RoleInstanceSet` writes a static `ServiceName` for the leader component in leader-worker mode.


For `sglang`, follower nodes in headless multi-node DP do not expose a fully operational API server. Sending requests to every Pod in the role would lead to failure.

At larger scale, it is often more natural to treat each replica as its own DNS boundary. One headless Service per `RoleInstance` makes that possible.

More importantly, direct Pod DNS access through headless Services depends on the Pod `subdomain` matching the governing Service name.
This means `PerReplicaFull` cannot be handled by extra Services only. It changes the actual network identity used by the Pods,
so this KEP must define rollout behavior and compatibility carefully.

### Goals

1. Introduce a clear API for selecting the headless Service topology of a role while keeping current behavior as the default.
2. Support a shared leader-only Service for `RoleInstanceSet + leaderWorkerPattern`.
3. Support one headless Service per `RoleInstance` in the same `Roleinstanceset`.

### Non-Goals

1. Supporting `Deployment`, `StatefulSet`, `LeaderWorkerSet`, or other role pattern.
2. Supporting `standalonePattern` or `customComponentsPattern`.
3. Introducing `PerReplicaLeaderOnly` in this KEP.
4. Keeping every policy transition non-rolling. Transitions which change Pod DNS identity must rollout.

## Proposal

Add a new optional networking field in `RoleSpec` which contains there types:
- `SharedFull` Default value. Keeps the current model: one shared headless Service for the role, selecting all Pods in the role.
- `SharedLeaderOnly` keeps one shared headless Service for the role, but it selects only leader Pods.
- `PerReplicaFull` One headless Service per `RoleInstance`, and each Service selects all Pods in that replica. No leader worker separation.


### User Stories (Optional)

#### Story 1

When using `sglang` or `vllm` in headless mode across nodes, I want the requests target to the shared role svc to only route to leader Pods.
So that followers that only run a dummy API server would not serve requests.

#### Story 2

I have a large cluster. The replica is so large that the `CoreDNS` may fail to solve. I want each replica to have its own headless Service 
so that DNS and endpoints can be looked up safely instead of be choped by connections.


## Design Details

### API

Adding `RoleSpec` with a role-level networking configuration

```go
type RoleSpec struct {
    // +optional
    Networking *RoleNetworking `json:"networking,omitempty"`
}

type RoleNetworking struct {
    // HeadlessServicePolicy controls the canonical headless Service topology for the role.
    // +optional
    // +kubebuilder:default=SharedFull
    HeadlessServicePolicy HeadlessServicePolicy `json:"headlessServicePolicy,omitempty"`
}

type HeadlessServicePolicy string

const (
    // Default behavior. One shared headless Service selects every Pod in the role.
    HeadlessServiceSharedFull HeadlessServicePolicy = "SharedFull"

    // One shared headless Service selects only leader Pods.
    HeadlessServiceSharedLeaderOnly HeadlessServicePolicy = "SharedLeaderOnly"

    // One headless Service is created for each RoleInstance. Each such Service
    // selects all Pods in that replica and becomes the governing service name
    // for the Pods in that instance.
    HeadlessServicePerReplicaFull HeadlessServicePolicy = "PerReplicaFull"
)
```

Default:
- If the field is empty, the policy defaults to `SharedFull`.
- Non-default policies are valid only for stateful `RoleInstanceSet + leaderWorkerPattern`.
- Unsupported combinations must be rejected rather than silently ignored.

### Behavior by Policy

#### `SharedFull`

This is current behavior and remains the default.
- One shared headless Service for one role and includes all role Pods.
- Existing workloads require no spec changes and see no behavioral change.

#### `SharedLeaderOnly`
- One shared headless Service exists for the role and includes only leader Pods.
- Follower Pods are no longer available through the shared Service and shared service name remains the same as `SharedFull`.


#### `PerReplicaFull`
- One headless Service is created for each `RoleInstance`.
- Each per-instance Service selects all Pods that belong to that replica.
Because the Service name changes, this policy changes Pod subdomain and requires rollout-aware handling after update.



### Service Naming and Owner Reference

#### Shared policies
- Service name continues to use the existing shared naming convention.
- The shared Service is owned by the `RoleInstanceSet`.
- Changing between these two policies does not change the Service name of the Pods.

#### `PerReplicaFull`
- Each per-instance Service name is equal to the `RoleInstance` name.
- The per-instance Service is owned by that `RoleInstance`.
- The selector matches the instance identity label, and include group and role labels for safety.


### Rollout and Transition Behavior

#### Shared-to-shared transition

`SharedFull <-> SharedLeaderOnly`

This transition changes which Pods are selected by the shared Service, but it does not change the shared Service name. 

This can be reconciled by updating the Service selector only. The controller may handle this transition in place without recreating Pods and Pods' DNS identity remains stable.

#### Transitions involving `PerReplicaFull`
There are 4 scenarios

`SharedFull -> PerReplicaFull`  
`SharedLeaderOnly -> PerReplicaFull`  
`PerReplicaFull -> SharedFull`  
`PerReplicaFull -> SharedLeaderOnly`

These require a `RoleInstanceSet` rollout. Transition rules:

1. The controller first creates the target Services for the new policy.
2. Pods are recreated through the normal rollout path so that their `subdomain` matches the new governing Service name.
3. Old and new Services may temporarily coexist during rollout. And once the rollout is complete, old Services are deleted.


### Discovery and Environment Variables

Current discovery and environment injection assume one role-level service name.

- leader address environment construction
- discovery ConfigMap generation
- any helper that derives a service name from a role alone

Shared policies remain role-scoped for discovery.

`PerReplicaFull` becomes instance-scoped for service identity. For leader-worker roles, leader-oriented addresses should resolve against the instance service name. 
Concretely, the leader address shape becomes: `<role-instance-name>-0.<role-instance-name>`


Only `RoleInstanceSet` with stateful `leaderWorkerPattern` can use policy field. Everything else remains at the default behavior and must reject non-default policies.


### Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.


##### Unit tests

- API defaulting for `SharedFull`
- Shared Service selector generation for `SharedFull` and `SharedLeaderOnly`
- Per replica Service name for `PerReplicaFull`
- `RoleInstance` spec generation to ensure all components receive the correct `ServiceName` under `PerReplicaFull`
- Cleanup logic for old shared or per-instance Services after rollout

##### Integration tests

- `SharedLeaderOnly` creates one shared headless Service and its selector matches only leader Pods
- `PerReplicaFull` creates one headless Service per `RoleInstance`
- In `PerReplicaFull`, Pods can be resolved correct
- `SharedFull -> PerReplicaFull` transition creates the target Services first, rolls instances
- `PerReplicaFull -> SharedFull` and `PerReplicaFull -> SharedLeaderOnly` restore the shared Service topology and remove obsolete per-instance Services

##### e2e tests

- A leader-worker role using `SharedLeaderOnly` does not expose worker Pods through the shared Service
- A multi-replica role using `PerReplicaFull` gets one Service per replica and each Service contains only that replica's Pods
- A rollout from any other policy to `PerReplicaFull` DNS identity transitions from shared-service to instance-service



## Production Readiness Review Questionnaire

### Feature Enablement and Rollback


###### Does enabling the feature change any default behavior?

No. The default remains `SharedFull`.

###### Can the feature be disabled once it has been enabled?

Yes. Users can set the policy back to `SharedFull`.


###### Are there any tests for feature enablement/disablement?

Yes.


## Drawbacks

- The feature adds another policy dimension to role networking and `PerReplicaFull` increases Service object count.
- The rollout matrix becomes more complex because some transitions need Pods to restart while other would not

## Alternatives
### Add `PerReplicaLeaderOnly`

This would be closer to some `sglang` deployment scenario, but it would create many svc. This KEP leaves it for future work.

### Make per-replica Services smooth to avoid restart
Pods must use that service name as their `subdomain`. That means `PerReplicaFull` must lead to service recreation and Pod spec change.


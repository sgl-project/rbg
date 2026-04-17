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

This KEP proposes a new role-level networking field,`NetworkConfig.SubdomainPolicy`, with two supported policies:

- `Shared`
- `UniquePerReplica`


RBG currently creates one headless Service per role. That works for simple cases, but it is noy enough for some large scale cases.

In particular, `RoleInstanceSet + leaderWorkerPattern` has two practical needs that are not supported by the current controller:

1. We would like to have segregated service for each replica to avert using native round robin load balance algorithms to enhance serving.
2. At larger scale, a single shared headless Service can become a bottleneck to aggregate every Pod of the role. 
Setting one headless Service per replica as `leaderworkerset` does so that each replica has its own DNS domain.


## Motivation

Currently, one role corresponds to one headless Service.

- The Service reconciler builds a selector at role scope.
- `RoleInstanceSet` writes a static `ServiceName` for the leader component in leader-worker mode.


As for some gateway, informers would discover service by label, and redirect requests to each service instead of pods.

At larger scale, it is often more natural to treat each replica as its own DNS boundary. One headless Service per `RoleInstance` makes that possible.

More importantly, direct Pod DNS access through headless Services depends on the Pod `subdomain` matching the governing Service name.
This means `UniquePerReplica` cannot be handled by extra Services only. It changes the actual network identity used by the Pods,
so this KEP must define rollout behavior and compatibility carefully.

### Goals

1. Introduce a clear API for selecting the headless Service topology of a role while keeping current behavior as the default.
2. Keep a shared Service for `RoleInstanceSet + leaderWorkerPattern`.
3. Support one headless Service per `RoleInstance` in the same `Roleinstanceset` when using `StatefulPattern`.


### Non-Goals


1. Supporting `standalonePattern` or `customComponentsPattern`.
2. Supporting `Deployment`, `StatefulSet`, `LeaderWorkerSet`, or other role pattern.
3. Introducing `LeaderOnly`  service in this KEP.
4. Keeping every policy transition non-rolling. Transitions which change Pod DNS identity must rollout.

## Proposal

Add a new optional networking field in `RoleSpec` which contains there types:
- `Shared` Default value. Keeps the current model: one shared headless Service for the role, selecting all Pods in the role.
- `UniquePerReplica` create separated headless Service for the role, and it selects only Pods in that replicas, 
while each replica get its own subdomain. This policy is only for `StatefulPattern`



### User Stories (Optional)

#### Story 1

When using `sglang` or `vllm` to serve large models, I want the requests target to the certain service instead of random one,
and the algorithm may be `cache aware` or `least running length`, which is not supported through k8s native kube-proxy service.

#### Story 2

I have a large cluster. The replica is so large that the `CoreDNS` may fail to solve. I want each replica to have its own headless Service 
so that DNS and endpoints can be looked up safely instead of be choped by connections.

#### Story 3
I was using `LeaderWorkerSet` and I want to migrate to `RoleBasedGroup` using stateful pattern of `RoleInstanceSet` workload while not change any API field, 
and I prefer smooth migration to `RoleBaseGroup` and not change API.

## Design Details

### API

Adding `RoleSpec` with a role-level networking configuration

```go
type RoleSpec struct {
    // +optional
    NetworkConfig *NetworkConfig `json:"networkConfig,omitempty"`
}

type NetworkConfig struct {
    // SubdomainPolicy determines the policy that will be used when creating
    // the headless service, defaults to shared
    // +kubebuilder:validation:Enum={Shared,UniquePerReplica}
    SubdomainPolicy *SubdomainPolicy `json:"subdomainPolicy"`
}

type SubdomainPolicy string

const (
    // replica 0: my-rbg-0-0.my-rbg, my-rbg-0-1.my-rbg
    // replica 1: my-rbg-1-0.my-rbg, my-rbg-1-1.my-rbg
    SubdomainShared SubdomainPolicy = "Shared"


    // replica 0: my-rbg-0-0.my-rbg-0, my-rbg-0-0.my-rbg-0
    // replica 1: my-rbg-1-0.my-rbg-1, my-rbg-1-1.my-rbg-1
    SubdomainUniquePerReplica SubdomainPolicy = "UniquePerReplica"
)
```

Default:
- If the field is empty, the policy defaults to `Shared`.


### Behavior by Policy

#### `Shared`

This is current behavior and remains the default.
- One shared headless Service for one role and includes all role Pods.
- Existing workloads require no spec changes and see no behavioral change.

#### `UniquePerReplica`
- One unique headless Service for Pod in each replica within the same role.


### Service Naming and Owner Reference

#### `Shared`
- Service name continues to use the existing shared naming convention.
- The shared Service is owned by the `RoleInstanceSet`.
- transitions between `Shared` and `UniquePerReplica` change the governing service name of Pods

#### `UniquePerReplica`
- Each per-instance Service name is equal to the `RoleInstance` name.
- The per-instance Service is owned by that `RoleInstance`.
- The selector matches the instance identity label, and include group and role labels for safety.


### Rollout and Transition Behavior


There are 2 scenarios

`Shared -> UniquePerReplica`  
`UniquePerReplica -> Shared`  


These require a `RoleInstanceSet` rollout. Transition rules:

1. The controller first creates the target Services for the new policy.
2. Pods are recreated through the normal rollout path so that their `subdomain` matches the new governing Service name.
3. Old and new Services may temporarily coexist during rollout. And once the rollout is complete, old Services are deleted.


### Discovery and Environment Variables

Current discovery and environment injection assume one role-level service name.

- discovery ConfigMap generation
- environment variables that contain service-based addresses
- any helper that derives a service name from a role alone

Under `Shared`, discovery remains role-scoped. Pods keep using the shared headless Service name, and direct Pod DNS names keep the existing shape:

`<pod-name>.<shared-service-name>`

Under `UniquePerReplica`, discovery becomes replica-scoped. Each `RoleInstance` uses its own governing Service name, so direct Pod DNS names change to:

`<pod-name>.<role-instance-name>`

Because of this, `UniquePerReplica` is not just an additive Service creation policy. It changes the service name used by Pods, 
so discovery artifacts and environment variables that embed service addresses must be generated according to the active `SubdomainPolicy`.


Only `RoleInstanceSet` with stateful `leaderWorkerPattern` can use policy field.


### Test Plan

[ ] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.


##### Unit tests

- API defaulting for `Shared`
- Per replica Service name for `UniquePerReplica`
- Role instance component ServiceName propagation under `UniquePerReplica`
- Cleanup of old services after transition

##### Integration tests

- `Shared` creates one headless Service per role
- `UniquePerReplica` creates one headless Service per replica
- pods resolve correctly under shared subdomain
- pods resolve correctly under replica-specific subdomain
- `Shared` -> `UniquePerReplica` triggers rollout and converges
- `UniquePerReplica` -> `Shared` triggers rollout and converges

##### e2e tests

- direct DNS resolution works in both policies
- policy transitions preserve availability according to rollout strategy


## Production Readiness Review Questionnaire

### Feature Enablement and Rollback


###### Does enabling the feature change any default behavior?

No. The default remains `Shared`.

###### Can the feature be disabled once it has been enabled?

Yes. Users can set the policy back to `Shared`.


###### Are there any tests for feature enablement/disablement?

Yes.


## Drawbacks

- The feature adds another policy dimension to role networking and `UniquePerReplica` increases Service object count.
- The rollout matrix becomes more complex because some transitions need Pods to restart while other would not

## Alternatives

### Make per-replica Services smooth to avoid restart
Pods must use that service name as their `subdomain`. That means `UniquePerReplica` must lead to service recreation and Pod spec change.

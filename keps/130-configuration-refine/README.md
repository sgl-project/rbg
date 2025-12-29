# KEP-130: Configuration Refine for RoleBasedGroup (RBG)

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

This KEP proposes a refined configuration format for the RBG service discovery configuration file(/etc/rbg/config.yaml).
The new format aims to reduce redundancy by eliminating derivable fields and supporting both continuous and
non-continuous instance indices, thereby optimizing file size and maintainability.

Currently, the configuration includes redundant fields such as group.size and roles.*.size, which can be calculated 
from the instances' env. Additionally, the full FQDNs for instances often share common templates, leading to repetitive patterns.

Also, currently each role of one RBG has a configmap while the content is the same. This redundancy can be reduced by
using a single configmap template with parameters for size and excludes.



## Motivation

Currently, the RBG service discovery configuration (/etc/rbg/config.yaml) contains redundant fields that increase
file size and maintenance overhead.

Current Configuration Issues

- Redundant size fields: group.size and roles.*.size can be calculated from instances
- Repetitive address patterns: Full FQDNs share common templates


### Goals


1. Remove Template-based address generation:
    - {role}-{i}.s-{group}-{role}.{namespace}.svc.cluster.local
2. Excludes as metadata: Optional field, only when needed

New format as below:
```yaml
namespace: <namespace>
group: <group-name>
roles:
  <role-name>:
    size: <number-of-instances>
    excludes: [<list-of-excluded-indices>] # Optional
```

### Non-Goals

1. Accelerate Dynamic configuration updates: This KEP focuses on configuration file size reduction rather than update speed.
2. Handle Pod name patterns: This KEP does not address complex naming schemes.



## Proposal

The proposal is to introduce a new configuration format for the RBG service discovery configuration file that eliminates 
redundant fields and supports excludes for non-continuous instance indices. This new format will reduce file size and 
improve maintainability while retaining all necessary information for service discovery.



### User Stories (Optional)

#### Story 1
As a cluster operator, one single large scale RBG may contain multiple roles, with each role may have hundreds of replicas.
Using the current configuration format, the config file becomes excessively large. 
By adopting the new configuration format, the operator can significantly reduce the size of the config file,
making it easier to maintain and read.


#### Story 2



### Risks and Mitigations

With the new configuration format, there is a risk of no compatible format with existing systems.
To mitigate this, we will provide clear examples to help users transition to the new format.


## Design Details

The new configuration format will eliminate redundant fields and support excludes for non-continuous instance indices.


### Configuration Format
old format as below:
```yaml
group:
    name: <group-name>
    roles:
    - <role-name>
    size: <number-of-instances>
roles:
    <role-name>:
        instances:
        - address: <full-fqdn-of-instance-0>
        - address: <full-fqdn-of-instance-1>
        ...
        size: <number-of-instances>

```
For instance: one RBG with 4 instances, 1 router, 2 encoder, 2 prefill, 2 decode, the config file is like below:
```yaml
group:
  name: epd-test
  roles:
  - router
  - encoder
  - prefill
  - decode
  size: 4
roles:
  decode:
    instances:
    - address: decode-0.s-epd-test-decode
    - address: decode-1.s-epd-test-decode
    size: 2
  encoder:
    instances:
    - address: encoder-0.s-epd-test-encoder
    - address: encoder-1.s-epd-test-encoder
    size: 2
  prefill:
    instances:
    - address: prefill-0.s-epd-test-prefill
    - address: prefill-1.s-epd-test-prefill
    size: 2
  router:
    instances:
    - address: router-0.s-epd-test-router
    size: 1
```

New format as below:
```yaml
namespace: <namespace>
group: <group-name>
roles:
  <role-name>:
    size: <number-of-instances>
    excludes: [<list-of-excluded-indices>] # Optional
```
For instance: one RBG with 4 instances, 1 router, 2 encoder (excluding index 1), 2 prefill, 2 decode, the config file is like below:
```yaml
namespace: sgl-workspace
group: epd-test
roles:  
   router: 
    size: 1 
   encoder: 
    size: 2
   prefill:
     size: 2 
   decode: 
     size: 2
     excludes: [1] # Optional
```

As long as the service name pattern is consistent, the full FQDNs can be derived using the template:
`{role}-{i}.s-{group}-{role}.{namespace}.svc.cluster.local`, such as `encoder-0.s-epd-test-encoder.sgl-workspace.svc.cluster.local`.


### Test Plan


[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

##### Unit tests

<!--
In principle every added code should have complete unit test coverage, so providing
the exact set of tests will not bring additional value.
However, if complete unit test coverage is not possible, explain the reason of it
together with explanation why this is acceptable.
-->

<!--
Additionally, for Alpha try to enumerate the core package you will be touching
to implement this enhancement and provide the current unit coverage for those
in the form of:
- <package>: <date> - <current test coverage>
The data can be easily read from:
https://testgrid.k8s.io/sig-testing-canaries#ci-kubernetes-coverage-unit

This can inform certain test coverage improvements that we want to do before
extending the production code to implement this enhancement.
-->

- `<package>`: `<date>` - `<test coverage>`

##### Integration tests

<!--
Integration tests are contained in https://git.k8s.io/kubernetes/test/integration.
Integration tests allow control of the configuration parameters used to start the binaries under test.
This is different from e2e tests which do not allow configuration of parameters.
Doing this allows testing non-default options and multiple different and potentially conflicting command line options.
For more details, see https://github.com/kubernetes/community/blob/master/contributors/devel/sig-testing/testing-strategy.md

If integration tests are not necessary or useful, explain why.
-->

<!--
This question should be filled when targeting a release.
For Alpha, describe what tests will be added to ensure proper quality of the enhancement.

For Beta and GA, document that tests have been written,
have been executed regularly, and have been stable.
This can be done with:
- permalinks to the GitHub source code
- links to the periodic job (typically https://testgrid.k8s.io/sig-release-master-blocking#integration-master), filtered by the test name
- a search in the Kubernetes bug triage tool (https://storage.googleapis.com/k8s-triage/index.html)
-->

- [test name](https://github.com/kubernetes/kubernetes/blob/2334b8469e1983c525c0c6382125710093a25883/test/integration/...): [integration master](https://testgrid.k8s.io/sig-release-master-blocking#integration-master?include-filter-by-regex=MyCoolFeature), [triage search](https://storage.googleapis.com/k8s-triage/index.html?test=MyCoolFeature)

##### e2e tests

<!--
This question should be filled when targeting a release.
For Alpha, describe what tests will be added to ensure proper quality of the enhancement.

For Beta and GA, document that tests have been written,
have been executed regularly, and have been stable.
This can be done with:
- permalinks to the GitHub source code
- links to the periodic job (typically a job owned by the SIG responsible for the feature), filtered by the test name
- a search in the Kubernetes bug triage tool (https://storage.googleapis.com/k8s-triage/index.html)

We expect no non-infra related flakes in the last month as a GA graduation criteria.
If e2e tests are not necessary or useful, explain why.
-->

- [test name](https://github.com/kubernetes/kubernetes/blob/2334b8469e1983c525c0c6382125710093a25883/test/e2e/...): [SIG ...](https://testgrid.k8s.io/sig-...?include-filter-by-regex=MyCoolFeature), [triage search](https://storage.googleapis.com/k8s-triage/index.html?test=MyCoolFeature)




### Scalability


With the new configuration format, the size of the configuration file is expected to decrease, especially for RBGs with 
a large number of roles or one role with many replicas . This reduction in file size can lead to improved performance 
in terms of loading and parsing the configuration.





## Drawbacks

May break compatibility with existing systems that rely on the current configuration format.


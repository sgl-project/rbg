# KEP-29 Auto-create ScalingAdapter
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
- [Motivation](#motivation)
- [Proposal](#proposal)
    - [User Stories (Optional)](#user-stories-optional)
        - [Story 1](#story-1)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
    - [Implementation](#implementation)
    - [Test Plan](#test-plan)
        - [Unit Tests](#unit-tests)
        - [Integration tests](#integration-tests)
        - [End to End Tests](#end-to-end-tests)
<!-- /toc -->

## Motivation

<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

ScalingAdapter CRD allows users to independently scale each role.
Currently, users need to manually create it for each role.

This KEP is to define a mechanism that allow users to configure a new API for Roles within a RoleBasedGroup which enables to automatically create the corresponding RoleBasedGroupScalingAdapter.
And keep the lifecycle of the automatically created RoleBasedGroupScalingAdapter aligned with that of the RoleBasedGroup.


## Proposal

<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

### User Stories (Optional)

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

#### Story 1

As a cluster operator,
I can enable automatic creation once in an RBG manifest so that tenants do not need to understand CRDs beyond the RBG itself.

#### Story 2
As a user, after an admin has enabled auto-creation, I can run kubectl scale rbg-sa my-app-role-1 --replicas=5 without extra YAML.

### Risks and Mitigations

<!--
What are the risks of this proposal, and how do we mitigate? Think broadly.
For example, consider both security and how this will impact the larger
Kubernetes ecosystem.

How will security be reviewed, and by whom?

How will UX be reviewed, and by whom?

Consider including folks who also work outside the SIG or subproject.
-->
To avoid the changes from affecting initial functionality, the default behavior remains unchanged,
meaning it will not automatically create a RoleBasedGroupScalingAdapter by default. 
Allow users to configure if a RoleBasedGroupScalingAdapter referenced with a specified role in RBG needs to be automatically created.

## Design Details

The overall goal of auto-creating scalingAdapter

<!--
This section should contain enough information that the specifics of your
change are understandable. This may include API specs (though not always
required) or even code snippets. If there's any ambiguity about HOW your
proposal will be implemented, this is the place to discuss them.
-->
### RoleBasedGroup API

We extend the RoleBasedGroup API to introduce a new field: `RoleBasedGroup.spec.roles.scalingAdapter`
to opt in and currently user can set if scalingAdapter is enabled for a specified role. 
Current behavior is kept if not set.

```go
type RoleSpec struct {
    // ScalingAdapter describes the config of scalingAdapter that will be applied when creating rbg.
    ScalingAdapter *ScalingAdapter `json:"scalingAdapter,omitempty"`
}

type ScalingAdapter struct {
    // Enable indicates that if scalingAdapter need to be auto-created and referenced with a corresponding role in rbg
    // +kubebuilder:default=false
    Enable bool   `json:"enable,omitempty"`
    // Labels are additional labels to apply to the auto-created RBGSA.
    // Controller-managed labels (group-name, role-name) take precedence.
    Labels map[string]string `json:"labels,omitempty"`
}
```

The value corresponding to field `scalingAdapter.enable` is a boolean, with a default value of `false`,
maintaining consistency with the current behavior, 
meaning the scalingAdapter will not be started by default.

In the following show case, the scalingAdapter referenced with role-1 will be automatically created.

```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: ebg-demo
spec:
  roles:
  - name: role-1
    replicas: 1
    scalingAdapter:
      enable: true
      labels:
        custom-label/key: custom-value
    template:
      ...
  - name: role-2
    replicas: 1
    template:
      ...
  - name: role-3
    replicas: 1
    template:
      ...
```

The detail of the automatically created scalingAdapter is as follows:
```
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroupScalingAdapter
metadata:
  name: $RBG_NAME-$ROLE_NAME
  labels:
    custom-label/key: custom-value        # from scalingAdapter.labels
    workloads.x-k8s.io/group-name: $RBG_NAME  # controller-managed
    workloads.x-k8s.io/role-name: $ROLE_NAME   # controller-managed
  ownerReferences:
  - apiVersion: workloads.x-k8s.io/v1alpha1
    blockOwnerDeletion: true
    kind: RoleBasedGroup
    name: $RBG_NAME
    uid: $RBG_UID
spec:
  scaleTargetRef:
    name: $RBG_NAME
    role: $ROLE_NAME
```

User-specified labels from `scalingAdapter.labels` are merged into the RBGSA's
`metadata.labels`. Controller-managed labels (`group-name`, `role-name`) always
take precedence and cannot be overridden by user labels.

Users can directly adjust the replicas of an RBG Role workload that has a created ScalingAdapter through the scale operation.

```shell
kubectl scale RoleBasedGroupScalingAdapter $RBG_NAME-$ROLE_NAME --replicas=$NEW_REPLICAS
```

#### Lifecycle Management
The ScalingAdapter automatically created by the RBG will have the OwnerReference to the corresponding RBG added automatically,
ensuring that the lifecycle of the automatically created ScalingAdapter is aligned with the RBG it references.

#### Handling of User-Manually-Created ScalingAdapters

We want ScalingAdapters to be uniformly managed by the RBG-controller so users don't need to worry about this part of the process. Therefore, the RBG-Controller will determine management based on the OwnerReference and will only handle ScalingAdapters that are automatically created by the RBG.

### Test Plan

<!--
**Note:** *Not required until targeted at a release.*
The goal is to ensure that we don't accept enhancements with inadequate testing.

All code is expected to have adequate tests (eventually with coverage
expectations). Please adhere to the [Kubernetes testing guidelines][testing-guidelines]
when drafting this test plan.

[testing-guidelines]: https://git.k8s.io/community/contributors/devel/sig-testing/testing.md
-->

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.


#### Unit Tests

<!--
In principle every added code should have complete unit test coverage, so providing
the exact set of tests will not bring additional value.
However, if complete unit test coverage is not possible, explain the reason of it
together with explanation why this is acceptable.
-->

<!--
Additionally, try to enumerate the core package you will be touching
to implement this enhancement and provide the current unit coverage for those
in the form of:
- <package>: <date> - <current test coverage>

This can inform certain test coverage improvements that we want to do before
extending the production code to implement this enhancement.
-->

#### Integration tests

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

#### End to End Tests


## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->

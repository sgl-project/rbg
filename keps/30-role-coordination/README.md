# KEP-30: Role Coordination for RoleBasedGroup

## Table of Contents

<!-- toc -->

- [Release Signoff Checklist](#release-signoff-checklist)
- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [User Stories](#user-stories)
        - [Story 1: Coordinated Rolling Update](#story-1-coordinated-rolling-update)
    - [Implementation Details](#implementation-details)
        - [API Changes](#api-changes)
        - [Example](#yaml-example)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
    - [Test Plan](#test-plan)
    - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)

<!-- /toc -->

## Release Signoff Checklist

- [ ] (R) Enhancement issue in release milestone, which links to KEP dir in [kubernetes/enhancements] (not the initial
  KEP PR)
- [ ] (R) KEP approvers have approved the KEP status as `implementable`
- [ ] (R) Design details are appropriately documented
- [ ] (R) Test plan is in place, giving consideration to SIG Architecture and SIG Testing input (including test
  refactors)
    - [ ] e2e Tests for all Beta API Operations (endpoints)
    - [ ] (R) Ensure GA e2e tests meet requirements
      for [Conformance Tests](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/conformance-tests.md)
    - [ ] (R) Minimum Two Week Window for GA e2e tests to prove flake free
- [ ] (R) Graduation criteria is in place
    - [ ] (R) [all GA Endpoints](https://github.com/kubernetes/community/pull/1806) must be hit
      by [Conformance Tests](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/conformance-tests.md)
      within one minor version of promotion to GA
- [ ] (R) Production readiness review completed
- [ ] (R) Production readiness review approved
- [ ] "Implementation History" section is up-to-date for milestone
- [ ] User-facing documentation has been created in [kubernetes/website], for publication to [kubernetes.io]
- [ ] Supporting documentation—e.g., additional design documents, links to mailing list discussions/SIG meetings,
  relevant PRs/issues, release notes

## Summary

This KEP proposes adding role coordination capabilities to the RoleBasedGroup (RBG) controller.
Currently, RBG manages multiple roles independently, but many real-world applications require coordinated updates
across multiple roles. This enhancement introduces a coordination mechanism that allows defining complex update
strategies spanning multiple roles, such as updating a frontend role partially, then updating a backend role completely,
and finally completing the frontend update.

## Motivation

In complex distributed applications, individual components often need to be updated in a specific sequence to
maintain application availability and consistency. The current RoleBasedGroup implementation updates each role
independently, which can lead to service disruptions during updates. For example,
it's often necessary to update the prefill and decode at a fixed ratio (4P2D) in PD-disagg LLM inferences.

| Stage | Upgrade Process                                             | Comments                                                                                             |
|-------|-------------------------------------------------------------|------------------------------------------------------------------------------------------------------|
| 1     | Old Prefill: 4; Old Decode: 2                               | Begin to update rbg.                                                                                 |
| 2     | Old Prefill: 2, New Prefill: 2; Old Decode: 2               | Update 2 prefill pods first .                                                                        |
| 3     | Old Prefill: 2, New Prefill: 2; Old Decode: 1; New Decode 1 | Stop updating the prefill and update only one decode. The ratio of P to D must be maintained at 2:1. |
| 4     | New Prefill: 4; Old Decode: 1; New Decode 1                 | Continue to update the prefill pods.                                                                 |
| 5     | New Prefill: 4; New Decode 2                                | Update completely.                                                                                   |

### Goals

1. Enable coordinated updates across multiple roles in a RoleBasedGroup
2. Support phased rollouts where some roles are updated partially while others wait
3. Provide a flexible coordination strategy definition mechanism
4. Maintain backward compatibility with existing RoleBasedGroup resources
5. Allow rollback capabilities for coordinated updates

### Non-Goals

1. Implement coordination across multiple RoleBasedGroupSet
2. Handle coordination of non-workload resources (e.g., ConfigMaps, Secrets)

## Proposal

This KEP introduces a new `coordination` field to the RoleBasedGroup specification that defines how roles should
be connected in relation to each other. The coordination strategy consists of a series of steps, each specifying which
roles to update and how.

### User Stories

#### Story 1: Coordinated Rolling Update

In the PD-disaggregated scenario for LLM inference, the input/output pattern is relatively fixed,
with an optimal P:D ratio of 2:1.
Each time 2 Prefill Pods are updated, 1 Decode Pod needs to be updated accordingly to maintain this ratio.

##### Coordinate Rolling Update Process

The coordinated rolling update process ensures that the P:D ratio is maintained throughout the update cycle.
Here's how it works:

1. **Initial State**: The system starts with all old Prefill and Decode pods running
2. **Step-by-Step Update**:
    - Update Prefill pods in batches of 2
    - For each batch of 2 Prefill pods updated, update 1 Decode pod
    - Monitor readiness of updated pods before proceeding
3. **Completion**: Continue until all Prefill and Decode pods are updated while maintaining the 2:1 ratio

This approach ensures service continuity and optimal resource utilization during the update process,
preventing performance degradation due to imbalanced P:D ratios.

### Implementation Details

#### API Changes

Add a new `Coordination` field to the RoleBasedGroup spec:

```go

type RoleBasedGroupSpec struct {
// Existing fields...

// Coordination defines how roles should be coordinated 
// +optional
Coordination []Coordination `json:"coordination,omitempty"`
}

type Coordination struct {
Strategy []RoleStrategy `json:"strategy,omitempty"`
}

type RoleStrategy struct {
// Role is the name of the role (e.g. "prefill", "decode", "router").
Role string `json:"role"`

// UpdateStrategy describes how this role should be updated.
UpdateStrategy *RoleUpdateStrategy `json:"updateStrategy,omitempty"`

// TODO: add more strategy here as needed. e.g. ScalingStrategy
}

// RoleUpdateStrategy describes how to update a role's workload.
type RoleUpdateStrategy struct {
// Type is the update strategy type (e.g. "Recreate", "InplaceIfPossible").
Type string `json:"type,omitempty"`
Partition *int32 `json:"partition,omitempty"`
MaxUnavailable intstr.IntOrString `json:"maxUnavailable,omitempty"`
MaxSurge intstr.IntOrString `json:"maxSurge,omitempty"`
}

// status
type RoleBasedGroupStatus struct {
// Existing fields...

// CoordinationState Status of coordination
CoordinationState []CoordinationState `json:"coordinationState,omitempty"`
}

type CoordinationState struct {
RoleState      map[string]RoleCoordinationState `json:"progress,omitempty"`
LastUpdateTime metav1.Time                      `json:"lastUpdateTime,omitempty"`
}

type RoleCoordinationState struct {
Strategy string `json:"strategy"`
State    string `json:"state"`
}


```

#### Yaml Example

```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: role-coordination
spec:
  coordination:
    - strategy: # strategy 1: reconcile prefill & decode at 2:1 ratio
        - role: prefill
          updateStrategy:
            type: Recreate # [Recreate, InplaceIfPossible]
            maxSurge: 0
            partition: 0
            maxUnavailable: 2
        - role: decode
          updateStrategy:
            type: Recreate # [Recreate, InplaceIfPossible]
            maxSurge: 0
            partition: 0
            maxUnavailable: 1
    - strategy: # strategy 2: reconcile router & planner at 1:1 ratio
        - role: router
          updateStrategy:
            maxUnavailable: 1
        - role: planner
          updateStrategy:
            maxUnavailable: 1
```

### Risks and Mitigations

1. **Complexity Risk**: Adding coordination logic increases controller complexity
    - Mitigation: Implement thorough unit and integration tests

2. **Deadlock Risk**: Poorly configured coordination strategies could cause updates to stall
    - Mitigation: Add timeouts and clear status reporting

3. **Backward Compatibility**: Existing RoleBasedGroups should continue to work unchanged
    - Mitigation: Only apply coordination logic when `coordination` is specified

## Design Details

The implementation will modify the main reconciliation loop
in [RoleBasedGroupReconciler] to check for a coordination strategy. If present, it will execute the coordinated
update logic; otherwise, it will fall back to the existing independent role update behavior.

### Controller Logic

```go
package controller

func (r *RoleBasedGroupReconciler) Reconcile() {
	if r.spec.Coordination != nil {
		r.executeCoordinationStrategy()
	}
	// existing independent role update behavior
}

func (r *RoleBasedGroupReconciler) executeCoordinationStrategy() {
	for _, coordination := range rbg.Spec.Coordination {
		for _, strategy := range coordination.Strategy {
			if strategy.UpdateStrategy.Partition != nil {
				role.RolloutStrategy.RollingUpdate.Partition = strategy.UpdateStrategy.Partition
			} else {
				rollingStep = role.replicas - maxUnvailable
				if isCoordinationRoleCompleted() {
					// calculate partition 
					role.RolloutStrategy.RollingUpdate.Partition -= rollingStep
				}
			}
		}
	}
}

func isCoordinationRoleCompleted() {
	// get pod and check rollingUpdate status
}

```

### Router

We will add new labels for updated pods (e.g. revision-hash) to identify which roles are being updated.
So sglang router can route new requests to updated prefill and decode pods.

### Test Plan

#### Unit Tests

- Test coordination strategy parsing and validation
- Test step execution logic
- Test status tracking and updates
- Test edge cases (empty steps, invalid configurations)

#### Integration Tests

- Test full coordination flow with multiple roles
- Test partial updates within steps
- Test rollback scenarios
- Test interaction with existing independent role updates

#### E2E Tests

- Deploy a multi-role application with coordination strategy
- Execute coordinated update and verify correct sequence
- Verify application availability during update

### Graduation Criteria

#### Alpha

- Basic coordination strategy implementation
- Support for simple sequential role updates
- Unit and integration tests
- Documentation and examples

#### Beta

- Support for complex coordination patterns
- Comprehensive e2e tests
- Metrics and monitoring
- User feedback and iterations

#### GA

- Proven stability in production environments
- Complete documentation and best practices
- No critical bugs reported for 2 consecutive releases

## Implementation History

- 2025-10-17: KEP created
- TBD: Alpha implementation
- TBD: Beta implementation
- TBD: GA implementation

## Drawbacks

1. Increased complexity in the RoleBasedGroup controller
2. Additional status tracking and state management
3. Potential for misconfigured coordination strategies to block updates


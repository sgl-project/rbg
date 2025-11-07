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
        - [Story 1](#story-1)
        - [Story 2](#story-2)
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

In a PD-disaggregated inference service, updates to multiple interdependent roles—such as Prefill and Decode, are often triggered concurrently.
A cross-role coordination mechanism is therefore needed to ensure proportional canary rollout, so that Prefill and Decode progress at the same relative rate.
This prevents skew where the fraction of new-version replicas in one role becomes significantly higher or lower than in the other,
leading to an imbalanced distribution of old and new Prefill/Decode workers.
For example, if an inference service has 40 Prefill workers and 20 Decode workers, the upgrade should maintain approximately a 40:20 (2:1) ratio across the two roles.

| Stage | Upgrade Process                                                 | Comments                                                                                                                                                                        |
|-------|-----------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| 1     | Old Prefill: 40; Old Decode: 20                                 | Begin to update rbg with p/d coordination.                                                                                                                                      |
| 2     | Old Prefill: 30, New Prefill: 10; Old Decode: 17, New Decode: 3 | Update 10 prefill pods and 3 decode pods.                                                                                                                                       |
| 3     | Old Prefill: 30, New Prefill: 10; Old Decode: 15; New Decode: 5 | The ratio of new Prefill is 10/40=25% and new Decode is 3/20=15%. The proportionate split between the two is highly skewed. Stop updating the prefill and update only 2 decode. |
| 4     | Old Prefill: 25, New Prefill: 15; Old Decode: 12; New Decode: 8 | Continue to update the prefill pods.                                                                                                                                            |
| 5     | New Prefill: 40; New Decode 20                                  | Update completely.                                                                                                                                                              |

### Goals

1. Enable coordinated updates across multiple roles in a RoleBasedGroup with a maximum skew configuration of multi roles' new version ratio 
2. Support configuring identical rollout-partition percentages across multiple roles to control the new-version canary fraction and enable phased rollouts
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

#### Story 1:

As a cluster operator, Prefill and Decode are typically rolled out and rolled back together.
If the current Decode upgrade fails for any reason, the MaxUnavailable constraint will block further Decode rollout.
In that case, we also want to halt Prefill’s rollout progress so that both roles can be rolled back in tandem.

#### Story 2:

As a developer, if Prefill’s rollout rate is significantly faster than Decode’s, 
the resulting imbalance in their new-version ratios makes traffic-based canary validation difficult.

##### Coordinate Rolling Update Process

The coordinated rolling update can control the rollout proportions between Prefill and Decode to prevent excessive skew in their new-version ratios.
For example, if Prefill is upgrading faster while Decode lags, pause Prefill’s rollout and wait for Decode to catch up, keeping the share of new-version replicas for both roles within a bounded range.

Here's how it works:
1. **Initial State**: The system starts with all old Prefill and Decode pods running
2. **Step-by-Step Update**:
    - Update Prefill pods and Decode pods with a MaxUnavailable limitation
    - Monitor readiness of updated pods before proceeding
    - Compute the current ratio of new-version replicas for Prefill and Decode.
    - If the gap between their new-version ratios exceeds the configured threshold, suspend updates for the role with the higher ratio.
    - If all Roles meet the current partition, pause the rollout.
3. **Completion**: Continue until all Prefill and Decode pods are updated while maintaining the 2:1 ratio

This approach ensures service continuity and optimal resource utilization during the update process,
preventing performance degradation due to imbalanced P:D ratios.

### Implementation Details

#### API Changes

Add a new `Coordination` field to the RoleBasedGroup spec:

```go
package test

type RoleBasedGroupSpec struct {
	// Existing fields...

	// Coordination defines how roles should be coordinated 
	// +optional
	Coordination []Coordination `json:"coordination,omitempty"`
}

type Coordination struct {
	// Name is the name of the coordination strategy
	Name string `json:"name"`

	// Type is the type of the coordination strategy (e.g. "RollingUpdate").
	Type string `json:"type"`

	// Roles define which roles need to work together in coordination
	Roles []string `json:"roles"`
	
	// Strategy defines the coordination parameters corresponding to coordination type for the roles.
	Strategy map[string]string `json:"strategy"`
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

#### Coordinated-roles Upgrading Yaml Example
- Prefill and decode roles rollingUpdate with coordination
```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: rolling-update-with-partition
spec:
  coordination:
    - name: prefill-decode-update
      type: RollingUpdate
      roles:
        - prefill
        - decode
      strategy:
        # The maximum ratio of pods that can be unavailable during the update.
        # For example, one rbg with (200p, 100d) allows max `200 * %5 = 10` prefill pods and `100 * 5% = 5` decode pods are unavailable during updating.
        maxUnavailableRatio: 5%
        # the max skew of updated replicas between prefill and decode.
        # For example, one rbg with (200p, 100d) allows `abs(updated_prefill/200 - updated_decode/100) < 1%`.
        maxSkew: 1%
        # Partition indicates the ratio of new version at which the workload should be partitioned for updates.
        # During a rolling update, all pods from ordinal replicas-1 to (Partition * ordinal replicas) are updated. All pods from (ordinal Partition * ordinal replicas) to 0 remain untouched.
        # For example, one rbg with (200p, 100d), 
        # the prefill pods from ordinal 199 to `abs(200 * 80% = 160)` are updated, all pods from `abs(200 * 80%) - 1 = 159` to 0 remain untouched.
        # the decode pods from ordinal 99 to `abs(100 * 80% = 80)` are updated, all pods from `abs(100 * 80%) - 1 = 79` to 0 remain untouched.
        partition: 80%
```

- Prefill and decode roles with affinity-based coordinated scheduling
```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: deepseek-pd
spec:
  coordination:
    - name: prefill-decode-affinity
      type: AffinityScheduling
      roles:
        - prefill
        - decode
      strategy:
        affinityScheduling:
          # Topology key for node affinity
          topologyKey: kubernetes.io/hostname
          # Deploy 2 prefill pods and 1 decode pod together on the same node
          ratios:
            prefill: 2
            decode: 1
          # Deploy batches sequentially - wait for all pods in a batch to be ready
          batchMode: Sequential
  roles:
    - name: prefill
      replicas: 6             # Will deploy in 3 batches (6/2 = 3)
      template:
        metadata:
          labels:
            role: prefill
        spec:
          containers:
            - name: prefill
              image: vllm/vllm-openai:latest
              command: ["start-prefill.sh"]
    - name: decode
      replicas: 3             # Will deploy in 3 batches (3/1 = 3)
      template:
        metadata:
          labels:
            role: decode
        spec:
          containers:
            - name: decode
              image: vllm/vllm-openai:latest
              command: ["start-decode.sh"]
```

## Design Details

The implementation will modify the main reconciliation loop
in [RoleBasedGroupReconciler] to check for a coordination strategy. If present, it will execute the coordinated
update logic; otherwise, it will fall back to the existing independent role update behavior.

### Controller Logic

#### AffinityScheduling Coordination

The AffinityScheduling coordination type ensures that pods from different roles are deployed together
on the same topology domain (typically the same node) according to specified ratios.

**Implementation Details:**

1. **Affinity Injection**: When a role is part of an AffinityScheduling coordination, the controller
   injects PodAffinity rules into the pod template to ensure pods from coordinated roles are scheduled
   together on the same topology domain.

2. **Coordination Labels**: Each coordination strategy adds a unique label to pods in coordinated roles:
   - Label key: `coordination.workloads.x-k8s.io/<coordination-name>`
   - Label value: `member`
   
   This label is used in the PodAffinity selector to identify coordinated pods.

3. **Batch Deployment**: Pods are deployed in batches according to the specified ratios:
   - The number of batches is calculated as: `min(role.replicas / ratio for each role)`
   - Each batch contains exactly the number of pods specified by the ratio for each role
   - In Sequential mode, the controller waits for all pods in a batch to be ready before deploying the next batch
   - In Parallel mode, multiple batches can be deployed concurrently

4. **Scheduling Constraints**: The injected PodAffinity ensures:
   - Pods from different roles in the same coordination are scheduled on the same topology domain
   - The Kubernetes scheduler respects the topology constraints and ratios
   - If a node cannot accommodate all pods in a batch, the pods remain pending until suitable resources are available

**Example**: For a coordination with `ratios: {prefill: 2, decode: 1}` and `topologyKey: kubernetes.io/hostname`:
- Batch 1: Deploy prefill-0, prefill-1, decode-0 on node-1
- Batch 2: Deploy prefill-2, prefill-3, decode-1 on node-2
- Batch 3: Deploy prefill-4, prefill-5, decode-2 on node-3

#### RollingUpdate Coordination

#TODO

### Risks and Mitigations

1. **Complexity Risk**: Adding coordination logic increases controller complexity
    - Mitigation: Implement thorough unit and integration tests

2. **Deadlock Risk**: Poorly configured coordination strategies could cause updates to stall
    - Mitigation: Add timeouts and clear status reporting

3. **Backward Compatibility**: Existing RoleBasedGroups should continue to work unchanged
    - Mitigation: Only apply coordination logic when `coordination` is specified

4. **Configuration conflict**: Coordination settings may conflict with each role’s own updateStrategy and Dependency configuration.
    - Mitigation: The coordination-driven upgrade process is not subject to Dependency constraints. Moreover, if a role is defined in coordination, validation will prohibit that role from specifying its own updateStrategy.

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


# KEP-32: Segment Placement for Multi-Level Gang Scheduling

<!-- toc -->
- [Release Signoff Checklist](#release-signoff-checklist)
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Prefill-Decode Separation Deployment](#story-1-prefill-decode-separation-deployment)
    - [Story 2: Multi-Role Coordinated Scaling](#story-2-multi-role-coordinated-scaling)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [API Changes](#api-changes)
  - [Segment Calculation Algorithm](#segment-calculation-algorithm)
  - [Multi-Coordination Merge Logic](#multi-coordination-merge-logic)
  - [Conflict Detection](#conflict-detection)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Future Enhancements](#future-enhancements)
<!-- /toc -->

## Release Signoff Checklist

Items marked with (R) are required *prior to targeting to a milestone / release*.

- [x] (R) Enhancement issue in release milestone
- [x] (R) KEP approvers have approved the KEP status as `implementable`
- [x] (R) Design details are appropriately documented
- [x] (R) Test plan is in place
  - [x] Unit tests completed and enabled
  - [x] Integration tests planned
- [ ] (R) Graduation criteria is in place
- [ ] (R) Production readiness review completed
- [ ] "Implementation History" section is up-to-date for milestone

## Summary

This KEP introduces **Segment Placement**, a coordination strategy that enables proportional, incremental deployment of multi-role workloads in RoleBasedGroup (RBG). It addresses critical limitations in the current gang scheduling implementation and enables multi-level gang scheduling for distributed AI serving scenarios, particularly Prefill-Decode (PD) separation architectures.

**Problem Statement**: The current RBG gang scheduling implementation places all pods from all roles into a single PodGroup and dynamically adjusts `minNum` as role replicas and LeaderWorkerSet (LWS) sizes change. This design has two critical issues:

1. **Scaling Deadlock**: During scale-up, if some replicas cannot be scheduled due to resource constraints, all newly created pods remain pending because the PodGroup's `minNum` requirement cannot be satisfied. This blocks the entire scaling operation.

2. **Rolling Update Stall**: When LWS size changes trigger rolling updates, RBG immediately recalculates and updates the PodGroup's `minNum` to reflect the target state. However, during the rolling update process, old pods are being terminated while new pods are being created. The updated `minNum` expects all new pods to be ready, which conflicts with the rolling update's gradual replacement strategy, causing the update to stall indefinitely.

Segment Placement solves these problems by:
- Deploying replicas in controlled batches (segments) that ensure both RoleInstance-level gang scheduling (within each instance) and role-level proportional scaling (across roles)
- Preventing partial deployments that would result in unusable systems (e.g., only Prefills without Decodes deployed)
- Decoupling PodGroup `minNum` updates from the actual deployment progression, allowing rolling updates to proceed smoothly

## Motivation

### Current Limitations

The existing RBG gang scheduling mechanism has fundamental design flaws:

**Single PodGroup for All Pods**:
- All pods from all roles are placed in one PodGroup
- The `minNum` is dynamically calculated as: `sum(role.replicas * role.lws.size)`
- Any change in role replicas or LWS size immediately updates `minNum`

**Problem 1: Scaling Deadlock**:
```
Initial state: 100 pods (minNum=100, all running)
User scales up: Increase replicas (new minNum=120)
→ 20 new pods created
→ Cluster has insufficient resources for all 20 pods
→ PodGroup requires 120 pods, but only 115 can be scheduled
→ Gang scheduling blocks the new 20 pods (all pending)
→ Result: Only 15 of the 20 new pods can be scheduled, but Gang blocks all 20
         The 100 existing pods keep running, but scale-up is blocked indefinitely
```

**Problem 2: Rolling Update Stall**:
```
Initial state: 20 pods (2 instances × 10 workers each), minNum=20
User changes LWS size: 2 → 4 (new minNum=40)
→ RBG immediately updates PodGroup minNum to 40
→ Rolling update starts: Delete 1 old instance (2 pods), create 1 new instance (4 pods)
→ During transition: 18 old + 4 new = 22 pods (< 44)
→ Gang scheduling blocks because minNum=40 not satisfied
→ Result: Rolling update stalled, system in inconsistent state
```

These issues make the current implementation unsuitable for production environments where resource constraints and rolling updates are common.

### Goals

1. **Enable Multi-Level Gang Scheduling**: Support both instance-level (RoleInstance as PodGroup) and role-level (Role as PodGroup) gang scheduling requirements
2. **Solve Scaling Deadlock**: Prevent resource constraints in one segment from blocking the entire deployment
3. **Enable Safe Rolling Updates**: Decouple PodGroup `minNum` updates from deployment progression to allow rolling updates to proceed smoothly
4. **Proportional Scaling**: Maintain correct ratios between roles during incremental deployment
5. **Resource-Aware Deployment**: Prevent resource exhaustion and partial deployments in constrained environments
7. **Flexible Progression Strategies**: Provide configurable progression strategies (Ordered, OrderedReady, Parallel) to meet different deployment requirements

### Non-Goals

1. **Topology-aware scheduling within segments**: Segment-level network domain or GPU topology affinity scheduling (planned for future enhancement)
5. **Custom readiness criteria**: Per-segment custom readiness checks beyond standard pod readiness

## Proposal

### Solution Overview

Segment Placement introduces a **segment-based progressive deployment** mechanism that solves the current gang scheduling limitations:

**Key Concepts**:
1. **Segment**: A unit of deployment containing a fixed number of replicas for each role (e.g., 10 Prefills + 5 Decodes)
2. **Progressive Deployment**: Deploy segments incrementally (Segment 1 → Segment 2 → ...) rather than all replicas at once
3. **Independent PodGroups**: Each RoleInstance has its own PodGroup (instance-level gang), while segment placement ensures role-level coordination

**How It Solves the Problems**:

**Scaling Deadlock → Isolated Failure Domain**:
```
Before (with Segment Placement):
  Segment 1: 10 prefills + 5 decodes (all running, minNum=10+5=15)
  Segment 2: 10 prefills + 5 decodes (all running, minNum=15)
  ...
  Segment 10: 10 prefills + 5 decodes (all running, minNum=15)

User scales up to Segment 11:
  → Create 10 new prefills + 5 new decodes
  → If resources insufficient: Only Segment 11 blocked
  → Segments 1-10 continue running normally (150 pods still serving traffic)
  → Result: Graceful degradation instead of total failure
```

**Rolling Update Stall → Segment-Aligned Updates**:
```
Before (with Segment Placement):
  10 segments, each segment = 1 instance (10 workers)
  PodGroup per instance: minNum=10

User changes LWS size 10 → 12:
  → Segment Placement calculates: 10 segments × 12 workers = target 120 pods
  → Rolling update processes ONE segment at a time
  → During update of Segment 5:
    - Old Segment 5: 10 pods (being deleted, PodGroup A with minNum=10)
    - New Segment 5: 12 pods (being created, PodGroup B with minNum=12)
    - Other 9 segments: Unaffected (90 pods still running)
  → Each segment's PodGroup is independent
  → Result: Rolling update proceeds smoothly, no deadlock
```

### User Stories

#### Story 1: Prefill-Decode Separation Deployment

As a **platform engineer** deploying a large-scale LLM inference service with Prefill-Decode separation,

I want to **deploy Prefills and Decodes in coordinated segments**,

So that **I can ensure both roles scale proportionally and avoid situations where only one role is deployed due to resource constraints**, which would make the system unusable. Additionally, I need to prevent the current issue where resource shortage for a few replicas blocks the entire deployment.

**Current Problem**:
- Target: 100 Prefills, 50 Decodes (2:1 ratio) = 150 pods total
- Cluster can only schedule 140 pods (not enough for all 150)
- Without segment placement: All 150 pods created, but only 140 can schedule
- Gang scheduling with minNum=150 blocks the unsatisfied pods
- Result: **System can run with 140 pods, but unable to scale to full 150 pods**
  - If starting from 0: All 150 pods pending (Gang requires all-or-nothing)
  - If scaling from 100 to 150: The 100 existing pods keep running, but 50 new pods blocked

**With Segment Placement**:
- Configure segment: 10 Prefills + 5 Decodes per segment
- Target: 10 segments (100 Prefills + 50 Decodes)
- Deployment: Segment 1 → 2 → ... → 9 (135 pods) → Segment 10 blocked
- Result: **9 segments running (135 pods serving traffic), only Segment 10 pending**

#### Story 2: Multi-Role Coordinated Scaling

As an **SRE managing a complex AI inference system** with Prefills, Decodes, and Routers,

I want to **coordinate scaling across multiple role pairs** (Prefill-Decode AND Decode-Router),

So that **all roles scale in lockstep, and no role gets ahead or behind causing system imbalance**.

**Example Scenario**:
- 3 roles: Prefill (300), Decode (100), Router (50)
- Coordination 1: Prefill-Decode must scale together (5:3 segments)
- Coordination 2: Decode-Router must scale together (3:2 segments)
- With multi-coordination support: System automatically synchronizes both coordinations, ensuring Decode (shared role) respects both constraints

#### Story 3: Safe Rolling Updates with LWS Size Changes

As a **cluster administrator** managing LLM inference workloads with LeaderWorkerSet,

I want to **change the worker count per instance (LWS size) without causing deployment deadlock**,

So that **rolling updates can complete successfully and my service remains available during the update**.

**Current Problem**:
- Initial: 10 instances, each with 10 workers (100 pods total)
- Change LWS size: 10 → 12 workers per instance
- RBG immediately updates PodGroup minNum: 100 → 120
- Rolling update starts replacing instances one by one
- During transition: 9 old instances (90 pods) + 1 new instance (12 pods) = 102 pods
- Gang scheduling requires 120 pods (minNum=120), but only 102 exist
- Result: **Rolling update stalled, system stuck in transition state**

**With Segment Placement**:
- Configure segment size aligned with instance count (e.g., 1 segment = 1 instance)
- Each instance has independent PodGroup (minNum = workers per instance)
- LWS size change 10 → 12:
  - Old instance: PodGroup A with minNum=10
  - New instance: PodGroup B with minNum=12
  - Both PodGroups are independent and satisfied
- Rolling update proceeds: Replace instance 1 → 2 → ... → 10
- Result: **Rolling update completes successfully, no deadlock**

### Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| **Conflicting segment configurations** | Deployment failure, system inconsistency | Strict validation and conflict detection before deployment starts |
| **One coordination blocks another indefinitely** | Deployment stalls | Clear error messages, monitoring alerts, timeout mechanisms (future) |
| **Performance overhead of readiness checks** | Slower reconciliation | Optimize status lookup, cache frequently accessed data |
| **Incorrect segment size causes resource waste** | Inefficient resource usage | Provide best practice guidelines, consider auto-calculation (future) |
| **Segment size misaligned with LWS instances** | Rolling updates may still face issues | Document best practices: align segment boundaries with instance boundaries |
| **Migration complexity from current gang scheduling** | Adoption barrier | Provide migration guide, support both modes during transition period (if needed) |

## Design Details

### API Changes

New fields added to `CoordinationStrategy`:

```go
type CoordinationStrategy struct {
    // ... existing fields ...
    
    // SegmentPlacement enables segment-based deployment strategy
    // +optional
    SegmentPlacement *SegmentPlacement `json:"segmentPlacement,omitempty"`
}

type SegmentPlacement struct {
    // SegmentSize defines the deployment segments and their replica counts for each role.
    // Key is the role name, value is the number of replicas in this segment.
    // +required
    SegmentSize map[string]int32 `json:"segmentSize"`
    
    // Progression defines how the controller moves from one segment to the next.
    // It determines the timing and conditions for advancing through segments.
    // Defaults to OrderedReady.
    // +optional
    // +kubebuilder:validation:Enum={Ordered,OrderedReady,Parallel}
    // +kubebuilder:default=OrderedReady
    Progression SegmentProgressionType `json:"progression,omitempty"`
}

// SegmentProgressionType defines the progression strategy for segment placement.
type SegmentProgressionType string

const (
    // SegmentProgressionOrderedReady ensures strict sequential deployment with readiness checks.
    // Behavior: Deploy Segment 1 -> Wait for Readiness -> Deploy Segment 2.
    // Use case: Production environments where stability is critical.
    SegmentProgressionOrderedReady SegmentProgressionType = "OrderedReady"
    
    // SegmentProgressionOrdered ensures sequential creation without waiting.
    // Behavior: Deploy Segment 1 -> Immediately deploy Segment 2.
    // It preserves the order of creation but proceeds to the next segment without blocking.
    // Use case: Fast deployment scenarios or testing.
    SegmentProgressionOrdered SegmentProgressionType = "Ordered"
    
    // SegmentProgressionParallel enables simultaneous deployment of all segments.
    // Behavior: Deploy Segment 1, Segment 2, Segment 3... at the same time.
    // In implementation, this effectively sets the TargetReplicas to the total sum of all segments immediately.
    // Use case: When segment placement is used only for proportional ratio enforcement.
    SegmentProgressionParallel SegmentProgressionType = "Parallel"
)
```

### PodGroup Integration

**Key Design Decision**: Segment Placement **does NOT modify PodGroup behavior**. Instead, it changes **when and how many replicas are created**.

**Current RBG Gang Scheduling (Problem)**:
```
Single PodGroup for all roles:
  name: rbg-service-podgroup
  minNum: sum(role.replicas * role.lws.size)
  
Example:
  Prefill: 100 replicas × 1 pod = 100 pods
  Decode: 50 replicas × 1 pod = 50 pods
  → PodGroup minNum = 150
  → All 150 pods must be schedulable or all block
```

**With Segment Placement (Solution)**:
```
Independent PodGroups per RoleInstance:
  Prefill-Instance-1: PodGroup with minNum = LWS.size (e.g., 10 workers)
  Prefill-Instance-2: PodGroup with minNum = LWS.size (e.g., 10 workers)
  ...
  Decode-Instance-1: PodGroup with minNum = LWS.size (e.g., 10 workers)
  ...

Segment Placement controls:
  - How many instances to create (based on segment progression)
  - Which roles to scale together (role-level coordination)
  - When to advance to next segment (based on readiness)

Example:
  Segment 1: Create Prefill-Instance-1 (10 pods) + Decode-Instance-1 (10 pods)
    → Each instance has its own PodGroup (minNum=10)
    → If one instance can't schedule, only that instance blocks
    → Other instances continue running
```

**Benefits**:
1. **Isolated Failure Domains**: Resource shortage in one segment doesn't affect others
2. **Safe Rolling Updates**: Old and new instances have independent PodGroups
3. **Instance-Level Gang Scheduling**: Preserves RoleInstance gang semantics (all workers of one instance scheduled together)
4. **Role-Level Coordination**: Segment Placement ensures roles scale proportionally

### RBG Readiness Model

**Current Atomic Readiness Model (Problem)**:

Without Segment Placement, RBG has a single atomic `Ready` condition:
```
RBG is Ready = ALL pods across ALL roles are Ready
```

This all-or-nothing model has significant drawbacks:
- During scale-up, RBG remains `Ready=False` until ALL new pods are ready
- A single pod failure or pending pod makes the entire RBG not ready
- No indication that the service can handle traffic with partial replicas
- Operators cannot distinguish between "completely down" and "partially available"

**New Readiness Model with Segment Placement**:

With Segment Placement, we introduce a more nuanced readiness model:

```yaml
status:
  conditions:
  - type: Ready
    status: "True"  # All desired replicas are ready
    
  - type: MinimumSegmentsvailable
    status: "True"  # At least one complete segment is ready
    message: "3/10 segments ready (45/150 pods serving traffic)"
```

**MinimumReplicasAvailable Condition**:

- **Purpose**: Indicates whether the RBG has enough ready replicas to serve traffic
- **Calculation**: 
  ```
  MinimumReplicasAvailable = True if:
    - At least 1 complete segment is fully ready (all roles in segment ready)
    - All roles in that segment meet their proportional replica counts
  ```
- **Use Cases**:
  - Load balancers can start routing traffic when `MinimumReplicasAvailable=True`
  - Monitoring systems can differentiate "service degraded" from "service down"
  - Autoscalers can make decisions based on available capacity
  - Rolling updates can proceed knowing minimum service level is maintained

**Example Progression**:

```yaml
# Initial deployment (0 pods)
status:
  conditions:
  - type: Ready
    status: "False"
    reason: "DeploymentInProgress"
  - type: MinimumSegmentsvailable
    status: "False"
    reason: "NoSegmentsReady"

# After Segment 1 is ready (15 pods: 10 prefills + 5 decodes)
status:
  conditions:
  - type: Ready
    status: "False"
    reason: "PartialDeployment"
    message: "15/150 pods ready"
  - type: MinimumSegmentsvailable
    status: "True"  # ✅ Can serve traffic now!
    reason: "MinimumSegmentReady"
    message: "1/10 segments ready (15/150 pods)"

# After all 10 segments are ready (150 pods)
status:
  conditions:
  - type: Ready
    status: "True"  # ✅ Full deployment complete
    reason: "AllReplicasReady"
  - type: MinimumSegmentsvailable
    status: "True"
    reason: "AllSegmentsReady"
    message: "10/10 segments ready (150/150 pods)"

# During scale-up from 150 to 165 (Segment 11 pending)
status:
  conditions:
  - type: Ready
    status: "False"
    reason: "ScalingInProgress"
    message: "150/165 pods ready"
  - type: MinimumSegmentsvailable
    status: "True"  # ✅ Still serving with 150 pods
    reason: "MinimumMet"
    message: "10/11 segments ready (150/165 pods)"
```

**Benefits**:
1. **Progressive Service Availability**: Service can start accepting traffic as soon as first segment is ready, rather than waiting for all replicas
2. **Better Observability**: Clear distinction between "minimum viable" and "fully ready" states
3. **Safer Operations**: Operations teams can monitor service degradation during scaling/updates
4. **Integration-Friendly**: External systems (load balancers, service mesh) can use `MinimumReplicasAvailable` for traffic routing decisions

**API Impact**:

No changes to the existing `Ready` condition semantics (maintains backward compatibility). The new `MinimumReplicasAvailable` condition is additive.

**Usage Example**:

```yaml
apiVersion: workloads.instai.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: llm-service
spec:
  roles:
  - name: prefill
    replicas: 100
  - name: decode
    replicas: 50
  
  coordinationRequirements:
  - strategy:
      segmentPlacement:
        segmentSize:
          prefill: 10
          decode: 5
        progression: OrderedReady
```

### Segment Calculation Algorithm

The scheduler implements a three-phase algorithm:

**Phase 1: Calculate Minimum Complete Segment**

```
minFullSegment = min(
    prefill_replicas / prefill_segment_size,
    decode_replicas / decode_segment_size,
    ...
)
```

Only fully deployed segments are counted. Partial segments are ignored.

**Phase 2: Check Readiness (for OrderedReady progression)**

```
isSegmentReady(segmentIndex):
    for each role:
        expectedReplicas = segmentIndex * segmentSize
        if currentReplicas < expectedReplicas OR readyReplicas < expectedReplicas:
            return false
    return true
```

**Phase 3: Calculate Target Replicas**

```
if progression == OrderedReady && !isSegmentReady(minFullSegment):
    return currentReplicas  // Keep current, wait for readiness

if progression == Parallel:
    return totalDesiredReplicas  // Deploy all segments at once

return (minFullSegment + 1) * segmentSize  // Advance to next segment (Ordered or OrderedReady when ready)
```

**Example Walkthrough**:

Initial state: `prefill: 0, decode: 0`

1. Segment 0 (initial): minFullSegment=0, isReady(0)=true → Deploy segment 1: `prefill: 10, decode: 5`
2. Wait for readiness... `prefill: 10/10 ready, decode: 5/5 ready`
3. Segment 1 complete: minFullSegment=1, isReady(1)=true → Deploy segment 2: `prefill: 20, decode: 10`
4. Continue until reaching desired replicas: `prefill: 100, decode: 50`

### Multi-Coordination Merge Logic

When multiple coordinations exist, the scheduler:

1. **Calculate targets independently**: Each coordination calculates its target replicas
2. **Check progression eligibility**: Determine if each coordination can progress (not blocked)
3. **Detect cross-coordination blocking**: If a role appears in multiple coordinations and one is not ready, all coordinations sharing that role are blocked
4. **Merge targets**: For shared roles, take the **minimum** target across all coordinations
5. **Apply blocking**: If a coordination is blocked, keep current replicas for all its roles

**Example**:

```yaml
coordinationRequirements:
- strategy:  # Coordination 1
    segmentPlacement:
      segmentSize: {prefill: 5, decode: 3}
- strategy:  # Coordination 2
    segmentPlacement:
      segmentSize: {decode: 3, router: 2}
```

Coordination 1 wants: `prefill: 15, decode: 9`  
Coordination 2 wants: `decode: 6, router: 4`  
Merged result: `prefill: 15, decode: min(9, 6) = 6, router: 4`

### Conflict Detection

The system validates configurations and rejects conflicting setups:

**Segment Size Conflict**:
```
Error: segment size conflict for role "prefill": 
       coordination has segment size 10, but another coordination has 5
```

**Progression Strategy Conflict**:
```
Error: progression strategy conflict for role "prefill": 
       coordination has strategy "OrderedReady", but another coordination has "Ordered"
```

Conflicts are detected during the `detectSegmentConflicts()` phase before any scheduling occurs.

### Test Plan

#### Unit tests

**Coverage**: `pkg/coordination/segmentplacement/`

Comprehensive unit tests covering:

- [x] Single coordination scenarios
  - Basic segment progression
  - Ordered vs OrderedReady vs Parallel progressions
  - Partial segment handling
  - Desired replicas limits
  - Initial deployment (0 replicas)
- [x] Multi-coordination scenarios
  - Different roles in different coordinations
  - Overlapping roles with same configuration
  - Cross-coordination blocking
  - Minimum target merging
- [x] Conflict detection
  - Segment size conflicts
  - Progression strategy conflicts
- [x] Edge cases
  - Zero segment size
  - Readiness blocking behavior
  - Default progression strategy
- [x] Readiness conditions
  - `Ready` condition transitions during segment deployment
  - `MinimumReplicasAvailable` condition calculation
  - Condition state during scale-up, scale-down, and rolling updates

**Current Coverage**: 15 test cases, all passing

#### e2e tests

**Planned for Beta**:

- [ ] End-to-end deployment with segment placement
- [ ] Multi-role coordinated scaling
- [ ] Rollback and recovery scenarios
- [ ] Large-scale stress testing (100+ replicas)
- [ ] Parallel progression strategy validation

### Graduation Criteria

#### Alpha

- [x] Feature implemented and functional
- [x] Basic unit tests completed
- [x] API design documented
- [x] Internal dogfooding completed
- [ ] Known limitations documented

**Availability**: v0.6.0

#### Beta

- [ ] Gather feedback from alpha users
- [ ] Additional e2e tests in place
- [ ] Performance benchmarks established
- [ ] Multi-coordination scenarios tested in production-like environments
- [ ] Monitoring and observability added
- [ ] Documentation completed (user guide, troubleshooting)

**Target**: v0.7.0

#### GA

- [ ] At least 3 production deployments using segment placement
- [ ] Proven resolution of scaling deadlock and rolling update stall issues in production
- [ ] No critical bugs in beta for 2+ releases
- [ ] All beta feedback addressed
- [ ] Conformance tests added
- [ ] Complete troubleshooting guide
- [ ] Performance benchmarks demonstrate acceptable overhead (<5% reconciliation latency increase)

**Target**: v0.9.0

## Implementation History

- 2025-12-30: KEP created and marked as `implementable`
- 2025-12-30: Initial implementation completed
  - Core scheduler logic implemented in `pkg/coordination/segmentplacement/`
  - API changes merged to `api/workloads/v1alpha1/`
  - Controller integration completed in `internal/controller/workloads/`
  - 15 unit tests added with 100% coverage of core logic
- 2025-12-30: Alpha release targeted for v0.6.0

## Drawbacks

1. **Increased Complexity**: Adds another layer of coordination logic to the RBG controller
2. **Deployment Time**: OrderedReady progression increases overall deployment time due to readiness waits
3. **Configuration Overhead**: Users need to understand and configure appropriate segment sizes
4. **Limited Rollback**: No built-in rollback mechanism in alpha (manual intervention required)

## Alternatives

### Alternative 1: Manual Phased Deployment

**Description**: Users manually scale RBG in phases using `kubectl scale`

**Pros**:
- No code changes needed
- Simple to understand

**Cons**:
- Manual, error-prone
- No automatic coordination
- Doesn't integrate with gang scheduling
- Requires constant monitoring

**Decision**: Rejected - doesn't solve the core automation and coordination problems

### Alternative 2: Operator-Level Coordination

**Description**: Create a separate operator to manage coordinated scaling

**Pros**:
- Separation of concerns
- Could be reused for other workloads

**Cons**:
- Additional component to deploy and maintain
- Increases system complexity
- Doesn't integrate well with RBG's native coordination features
- Slower reconciliation (cross-operator communication)

**Decision**: Rejected - segment placement is a core coordination feature that belongs in RBG

### Alternative 3: Pre-create All Replicas with PodGroup

**Description**: Create all pods immediately but use PodGroup gang scheduling to gate their scheduling

**Pros**:
- Simpler implementation
- Leverages existing gang scheduling

**Cons**:
- Creates many pending pods, increasing API server load
- Doesn't solve resource contention issues
- Can't enforce segment-based progression
- Poor visibility into deployment progress

**Decision**: Rejected - doesn't address the incremental deployment and resource efficiency goals

### Alternative 4: Keep Single PodGroup with Dynamic minMember Adjustment

**Description**: Continue using a single PodGroup for all pods but implement smarter `minMember` adjustment logic that accounts for rolling updates and partial deployments

**Pros**:
- Minimal architectural changes
- Simpler to understand (single PodGroup)
- No need for segment configuration

**Cons**:
- **Cannot solve the scaling deadlock**: If cluster lacks resources for target replicas, single PodGroup still blocks everything
- **Complex minNum calculation**: Need to track rolling update state, pending pods, ready pods, etc.
- **Race conditions**: Rolling updates and scaling operations can conflict
- **Tight coupling**: PodGroup state tightly coupled with deployment state
- **No isolation**: One role's resource shortage affects all roles
- **Limited flexibility**: Cannot support different progression strategies (Ordered, OrderedReady, Parallel)

**Decision**: Rejected - this approach fundamentally cannot solve the scaling deadlock problem. The core issue is that a single PodGroup with `minNum=N` requires **all N pods** to be schedulable simultaneously. In resource-constrained environments, this is often impossible. Segment Placement solves this by breaking the deployment into smaller independent units.

## Future Enhancements

### High Priority

- [ ] **Topology-aware segment placement**: 
  - **Primary Use Case**: Schedule all pods within a segment to be affinity-bound to the same topology domain
  - **Motivation**: In distributed AI workloads, pods within the same segment often need high-bandwidth, low-latency communication. Co-locating them in the same network domain or GPU topology can significantly improve performance.
  - **Examples**:
    - Network topology: Place all pods in Segment 1 on nodes within `topology.kubernetes.io/zone=zone-a`
    - GPU topology: Co-locate segment pods on nodes with NVLink-connected GPUs
    - NUMA topology: Ensure segment pods share the same NUMA node for memory locality
  - **API Design (Draft)**:
    ```yaml
    segmentPlacement:
      segmentSize:
        prefill: 10
        decode: 5
      topologyAffinity:
        # Topology domain to use for segment placement
        topologyKey: "topology.kubernetes.io/zone"
        # or: "nvidia.com/gpu-topology-domain"
        # or: "kubernetes.io/hostname" (for single-node segments)
        
        # Placement strategy
        strategy: PreferSegmentAffinity  # or: RequireSegmentAffinity, BestEffort
        
        # Optional: Spread segments across topology domains
        spreadSegments: true  # Segment 1 in zone-a, Segment 2 in zone-b, etc.
    ```
  - **Benefits**:
    - Reduced cross-zone network latency (e.g., 10ms → <1ms within zone)
    - Lower network egress costs (no cross-zone traffic billing)
    - Better GPU communication efficiency (NVLink vs PCIe)
    - Improved fault isolation (segment failures isolated to single topology domain)
  - **Challenges**:
    - Requires scheduler awareness of topology constraints
    - May conflict with resource availability (one zone lacks capacity)
    - Need fallback mechanism when strict affinity cannot be satisfied

### Medium Priority

- [ ] **Support for more progression strategies**: Canary, BlueGreen deployment strategies
- [ ] **Configurable readiness criteria per segment**: Custom health checks beyond pod readiness
- [ ] **Automatic segment size calculation**: Determine optimal segment sizes based on cluster resources and topology
- [ ] **Rollback capabilities**: Automatic rollback of failed segments
- [ ] **Progress tracking and metrics**: Detailed metrics and status tracking for segment progression

### Low Priority

- [ ] **Segment-level hooks**: Pre/post segment deployment hooks for custom actions
- [ ] **Cross-namespace coordination**: Coordinate segments across multiple namespaces

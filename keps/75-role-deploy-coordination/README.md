# KEP-76: Role Coordination Deploy for RoleBasedGroup



## Summary
This KEP extends [KEP-30 (Role Coordination for RoleBasedGroup)](../30-role-coordination/README.md) by introducing proportional, multi-role rolling deployment (Rolling Deploy). This feature enables users to define cross-role deployment steps, ensuring that Pods across different roles maintain the desired ratio even when cluster resources are constrained. It also provides fine-grained scheduling control at the role level.

## Motivation
In PD-disaggregated inference services (such as Prefill and Decode), multiple roles must be deployed together, often with strict ratio constraints. When resources are limited, the lack of coordinated rolling deploy may cause the following issues:

- **Failure to meet the minimum runnable ratio**. 
For example, the optimal P:D ratio is 2:1, but if 2 P Pods and 4 D Pods are scheduled, resources for 3 D Pods are wasted.
- If gang scheduling is used, insufficient resources may prevent the entire service from starting.
- **Pod placement violates cross-role constraints**.
If only using Pod affinity may place a 4P4D service into 1P4D on one node and 3P0D on another, deviating from the expected ratio.
- **Inability to treat multiple roles as a single logical workload**.
Users may want to manage Router, Prefill, and Decode together within one RBG object, with the operator ensuring that scheduled Pods always follow a defined ratio.

Therefore, RBG requires coordinated rolling deployment to ensure that services can start gradually and maintain proper ratios even when resources are limited. This also allows for further expansion of RBG's use cases.

### Goals
1. Support coordinated rolling deployment across multiple roles in RBG
2. Support gang scheduling for minimal deployable units
3. Enable fine-grained cross-role scheduling control
4. Provide flexible coordination strategy definitions
5. Maintain full backward compatibility with existing RBG resources
6. Support rollback capability

### Non-Goals
1. Coordination across multiple RoleBasedGroupSets
2. Coordination of non-workload resources such as ConfigMaps or Secrets

## Proposal
A new field `CoordinationRollingDeploy` is added to the RoleBasedGroup spec to define per-role deployment steps.
Each role can belong to only one `CoordinationRollingDeploy` configuration.

### User Stories

#### Story 1: Resource-constrained environments
A user wants to deploy a 4P4D service (minimum runnable ratio P:D=2:2).
Without coordinated rolling deploy, the following may occur:

- Only 4P pods are scheduled → service cannot run
- Only 4D pods are scheduled → service cannot run
- Gang scheduling all 8 pods → startup fails due to insufficient resources

With coordinated rolling deployment, the operator deploys Pods gradually using 2P:2D as the deploy step:
- step1: 2P + 2D
- step2: next 2P + 2D
- continue until all replicas are deployed

This ensures the service remains runnable at all intermediate stages and avoids resource waste.

#### Story 2: Fine-grained placement with cross-role affinity
A user wants role A and role B pods to be co-located within the same node or network domain, and to maintain a specific ratio such as 1:1.
Rolling deployment allows A and B to be scheduled in coordinated batches, combined with Pod affinity, to get closer to the user's intended deployment location.

#### Story 3: Treating RBG as an all-in-one workload
A user manages Router, Prefill, and Decode within a single RBG, enforcing P:D=2:1.
Regardless of changes to Prefill or Decode replica counts, the operator ensures scheduled Pods follow this ratio.

### Design Details

#### API Changes
Add `CoordinationRollingDeploy` to the RBG spec:
```go
// RoleBasedGroupSpec defines the desired state of RoleBasedGroup.
type RoleBasedGroupSpec struct {
	...
	CoordinationRequirements []Coordination `json:"coordination,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
}

// Coordination describes the requirements of coordination strategies for roles.
type Coordination struct {
	// Name of the coordination.
	Name string `json:"name"`
	// Roles that should be constrained by this coordination.
	Roles []string `json:"roles"`

	// RolloutStrategy describes the coordination strategies.
	Strategy *CoordinationStrategy `json:"strategy,omitempty"`
}

type CoordinationStrategy struct {
	// RollingUpdate defines the coordination strategies about rolling update.
	RollingUpdate *CoordinationRollingUpdate `json:"rollingUpdate,omitempty"`
	
    // new field
	// RollingDeploy defines the coordination strategies about rolling deploy.
	RollingDeploy *CoordinationRollingDeploy `json:"rollingDeploy,omitempty"`
}

// new field
// CoordinationRollingDeploy describes the rolling deploy coordination strategy.
type CoordinationRollingDeploy struct {
	roleDeployStep map[string]int
}

type RoleBasedGroupStatus struct {
	...
	// Status of individual roles
	RoleStatuses []RoleStatus `json:"roleStatuses"`
}

// RoleStatus shows the current state of a specific role
type RoleStatus struct {
	...
    // new field
	// Percentage of instances already deployed relative to the expected number of instances deployed.
	DeployedReplicasPercent int32 `json:"deployedReplicasPercent"`
}
```

`CoordinationRollingDeploy` specifies the maximum number of replicas of each role that may be deployed in one step.

RoleStatus is extended with `deployedReplicasPercent`, indicating the percentage of deployed replicas compared to the expected number.

#### Coordinated-roles Deploy Yaml Example
Prefill and decode roles rollingDeploy with coordination
```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata:
  name: nginx-cluster
spec:
  coordination:
    - name: pd-rollout-deploy
      strategy:
        rollingDeploy:
          prefill: 4
          decode: 2
    - name: pd-rollout
      roles:
        - prefill
        - decode
      strategy:
        rollingUpdate:
          maxSkew: 1%
          maxUnavailable: 10%
  roles:
    - name: prefill
      replicas: 8
      template:
        spec:
          containers:
            - image: anolis-registry.cn-zhangjiakou.cr.aliyuncs.com/openanolis/nginx:1.14.1-8.6
              name: nginx-leader
    - name: decode
      replicas: 4
      template:
        spec:
          containers:
            - image: anolis-registry.cn-zhangjiakou.cr.aliyuncs.com/openanolis/nginx:1.14.1-8.6
              name: nginx-worker

```

#### Controller Logic

**Initial Phase**
- Identify roles governed by CoordinationRollingDeploy
- Deploy all replicas for roles not participating in coordinated deploy
- For coordinated roles, deploy replicas according to their configured step size

**Deployment Process**
- A new batch step is deployed only after all Pods in the previous step are successfully scheduled
- If Pods from the previous step are still pending, deployment is paused
- The maximum cross-role deviation in scheduled replicas is determined by the step sizes
- The process repeats until all replicas are deployed

**Update Process**
- Scaling is completed before executing rolling updates

### Risks and Mitigations
- Rolling deploy and rolling update are mutually exclusive within a single reconciliation cycle
- If scaling occurs during deploy coordination, update coordination is skipped for that cycle
- Shrinking is unaffected; only expansion order is controlled
- The system does not reclaim misaligned Pods, but converges to the desired ratio through subsequent steps
- dependencies and podGroup cannot be used simultaneously
- If scheduling events are delayed and cause step inconsistency, the operator resets the step to avoid resource waste
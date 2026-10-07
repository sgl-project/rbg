# KEP-75: Enhanced Topology-Based Multi-Role Coordinated Scheduling



## Summary
This KEP extends [KEP-30 (Role Coordination for RoleBasedGroup)](../30-role-coordination/README.md) by introducing proportional, multi-role rolling deployment (Rolling Deploy). This feature allows users to define cross-role deployment steps, ensuring that even with limited cluster resources, Pods in different roles maintain the required proportions within a specific cluster topology. It provides fine-grained scheduling control between roles through RBG Operator.

## Motivation

1. **Need for Fine-Grained Placement Policies**: Serving jobs are long-running and do not support rescheduling. Therefore, fine-grained placement policies are required during initial scheduling to achieve a globally optimal placement strategy.

2. **Network Topology Optimization**: In PD-separated deployment architectures, placing P and D instances that process the same request within close network topology domains(NVLink > RDMA > TCP) can reduce KV transmission latency and increase throughput. Placements across different network switches can reduce the available bandwidth for KV cache transfer by approximately 20% [[1](https://arxiv.org/pdf/2508.19559)].

3. 在特定场景下，PD扽里

4. **Improved Service Fault Tolerance**: For inference services, dispersing homogeneous instances of the same role across different topology domains enhances the service's fault tolerance.

### Goals
1. **Cross-Role Topology Affinity Deployment**: Support topology affinity deploy for instances of specific roles (e.g. P:D) within corresponding sub-batches.

2. **Intra-Role Anti-Affinity Deployment**: Support topology anti-affinity deployment among instances within the same role to enhance service fault tolerance.

3. **Group Scheduling Capability**: Support group scheduling capability for each minimal topology affinity batch of Pods.

### Non-Goals
1. **Scenario Limitations**: This only considers the initial deployment scenario and does not cover subsequent scaling up or down scenarios.

2. **Cross-Group Coordination**: Coordination across multiple RoleBasedGroup is not supported.

3. **Non-Workload Resources**: Coordination for non-workload resources such as ConfigMaps or Secrets is not supported.


## Proposal

### User Stories
### User Story 1: Ensuring Optimal PD Instance Ratio Within a Topology Domain

**Scenario**: When PD instances within each topology domain maintain the optimal ratio, each P can prioritize selecting D nodes within the same topology domain when choosing Decode nodes.

**Example**: With an optimal ratio P:D=2:1:
- Node A: Deploy P1, P2, D1
- Node B: Deploy P3, P4, D2

P1, P2, and D1 form a virtual subgroup where the network is not a bottleneck for KV transmission between P and D. Furthermore, PD roles from subgroup1 and subgroup2 can still pair with each other in the router.

### User Story 2: Cluster Fault Tolerance Across Topology Domains

**Problem**: Cluster physical resources can fail at any time. If all P or D nodes are deployed in a specific topology domain (e.g., NodeA), and NodeA fails, the entire LLM inference service crashes due to the lack of a critical role.

**Solution**: Disperse instances of the same role across different topology domains. Even if a topology domain fails, the remaining PD roles can still connect to handle request inference in a degraded service mode.

### User Story 3: Resource-Constrained Environment

**Scenario**: A user wants to deploy a 4P4D service (minimum viable ratio P:D=2:2).

**Problems with Uncoordinated Rolling Deployment**:
- Only schedule 4P Pods → Service cannot run
- Only schedule 4D Pods → Service cannot run
- Group schedule all 8 Pods → Startup fails due to insufficient resources

**Advantages of Coordinated Rolling Deployment**:
The operator deploys Pods progressively in steps of 2P:2D:
- Step 1: 2P + 2D
- Step 2: Next group of 2P + 2D
- Continue until all replicas are deployed

This ensures the service remains operational at all intermediate stages and avoids resource wastage.


## Design Details


### Cluster Topology Definition

- **New CRD**: Introduce a Custom Resource Definition (CRD) to describe the cluster topology hierarchy.
- **Administrative Control**: This CRD object is created by the cluster administrator. Users can directly reference the topology name.
- **Resource Protection**: The RBG validates the topology configuration and adds a finalizer to protect the resource object upon successful validation.
- **Immutability**: The CRD is immutable after creation and cannot be deleted if referenced by an RBG.

#### API Extensions
```go
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:printcolumn:name="CurrentToplogyLevel",type="string",JSONPath=".status.CurrentToplogyLevel"
type ClusterTopology struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterTopologySpec   `json:"spec"`
	Status ClusterTopologyStatus `json:"status,omitempty"`
}

type ClusterTopologySpec struct {
	// If topologyScope is empty, index maps from smaller to larger network domains
	Layers []TopologyLayer `json:"Layers"`
	// Larger numbers indicate larger managed network domains with worse communication performance
	topologyScope map[TopologyLayerName]int
}

type TopologyLayer struct {
	TopologyLayerName TopologyLayerName `json:"name"`

	MatchLabelKey string `json:"key"`
}

type TopologyLayerName string

type ClusterTopologyStatus struct {
	// Output format example: Host < TOR < Pod
	CurrentToplogyLevel string
	// Validates whether the topology structure definition is correct
	// If validate = false, this topology cannot be referenced in RBG. Normally this should be handled by Webhook validation.
	validate bool
}

```


### RBG Operator Enhancements


In large model inference services, considering service fault tolerance and resource-constrained cluster scenarios, it is often impractical to deploy all RBG workloads within a single network domain. Optimization strategies include:

1. **Intra-Role-Instance Aggregation**: Aggregate P or D instances pod within high-performance networks.
2. **Cross-Role Optimization**: Due to the high-speed communication requirements between P and D roles and the inability to place all instances in the same topology domain, place the optimally proportioned number of Pods within the same topology domain.

### API Extensions

To achieve topology affinity scheduling between instances of different roles in a fixed ratio, it is necessary to further partition the role internally using a virtual object called `RoleSubGroup`.

```go
// RoleBasedGroupSpec defines the desired state of RoleBasedGroup.
type RoleBasedGroupSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:Required
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	Roles []RoleSpec `json:"roles" patchStrategy:"merge" patchMergeKey:"name"`

	// Configuration for the PodGroup to enable gang-scheduling via supported plugins.
	PodGroupPolicy *PodGroupPolicy `json:"podGroupPolicy,omitempty"`

	// CoordinationRequirements describes the requirements of coordination strategies for some specified roles.
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
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
	// Number of Instance to deploy in each step, and the minimum number of P:D pairs that should form a logical group during the deployment process.
	// eg. RoleDeployStep = {"prefill": 4, "decode": 2}
	RoleDeployStep map[string]int
	// Topology requirements for the current logical group
	DeployTopologyConstraint *DeployTopologyConstraint
}

type DeployTopologyConstraint struct {
	// TODO: Consider whether this should be globally unique or allow users to customize the configuration.
	// If not configured by user, use default value: rbg-cluster-topology
	clusterTopologyName *string

	// Declares which topology level the current batch of Pods should be constrained to
	LayerName *TopologyLayerName `json:"topologyLayerName,omitempty"`

	// Default value: hard
	constraintMode TopologyConstraintMode
    
	// Configuration for the PodGroup to enable gang-scheduling via supported plugins.
	PodGroupPolicy *PodGroupPolicy `json:"podGroupPolicy,omitempty"`
}

// +enum
type TopologyConstraintMode string

const (
	// HardTopologyConstraintMode represents a strict network topology constraint that workload must adhere to.
	HardTopologyConstraintMode TopologyConstraintMode = "hard"

	// SoftTopologyConstraintMode represents a flexible network topology constraint that allows workload
	// to cross network boundaries under certain conditions.
	SoftTopologyConstraintMode TopologyConstraintMode = "soft"
)
```


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
        deployTopologyConstraint: 
          clusterTopologyName: rbg-cluster-topology
          topologyLayerName: host
          constraintMode: hard
          podGroupPolicy: nil
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
              name: nginx
    - name: decode
      replicas: 4
      template:
        spec:
          containers:
            - image: anolis-registry.cn-zhangjiakou.cr.aliyuncs.com/openanolis/nginx:1.14.1-8.6
              name: nginx

```

**Example Scenario**:
- RBG object with 8P, 4D
- Minimum optimal instance number between roles is P:D = 4:2
- Each 4P:2D group must satisfy the same topology affinity requirements
- Divided into two deploy batches

#### Controller Logic

**Label Propagation Mechanism**:
The RBG Operator propagates the Role Subgroup Size to the underlying workloads. In the specific workload (InstanceSet Operator), the specific subgroup for the current instance is calculated based on the Subgroup Size and Role Instance Index, and the corresponding affinity policy is configured for the Role Pod.

**Workload Label Example**:
```yaml
rolebasedgroup.workloads.x-k8s.io/name: rbgs-test-1
rolebasedgroup.workloads.x-k8s.io/role: p
rolebasedgroup.workloads.x-k8s.io/role-subgroup-size: 4
rolebasedgroup.workloads.x-k8s.io/topology-coordination-name: p-d-4-2-deploy-test
```

**Topology Coordination Name Generation Strategy**:
```shell
hash(fmt.Sprintf("%s-%s", ${topology-coordination-name}, ${role-index} / ${role-subgroup}))
```

**Pod Label Example**:
```yaml
rolebasedgroup.workloads.x-k8s.io/name: rbgs-test-1
rolebasedgroup.workloads.x-k8s.io/role: p
rolebasedgroup.workloads.x-k8s.io/role-subgroup-size: 4
rolebasedgroup.workloads.x-k8s.io/role-instance-index: 1
rolebasedgroup.workloads.x-k8s.io/topology-coordination-name: p-d-4-2-deploy-test
rolebasedgroup.workloads.x-k8s.io/deploy-topology-coordination-name: p-d-4-2-deploy-test-0
```

**Pod Affinity Example**:
```yaml
  affinity:
    podAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        - labelSelector:
            matchExpressions:
              - key: rolebasedgroup.workloads.x-k8s.io/deploy-topology-coordination-name
                operator: In
                values:
                  - p-d-4-2-deploy-test-0
          topologyKey: kubernetes.io/hostname
    podAntiAffinity:
      preferredDuringSchedulingIgnoredDuringExecution:
        - weight: 100
          podAffinityTerm:
            labelSelector:
              matchExpressions:
                - key: rolebasedgroup.workloads.x-k8s.io/role
                  operator: In
                  values:
                    - p
            topologyKey: kubernetes.io/hostname
```

### 6. Progressive Scheduling Deployment

**Implementation Mechanism**: Achieve batch scheduling using the PodSchedulingGate feature:
- After the Pods of the previous batch are successfully scheduled
- Remove the PodSchedulingGate for the Pods of the next batch
- Trigger the scheduling process for the next batch of Pods

### Risks and Mitigations
- Rolling deploy and rolling update are mutually exclusive within a single reconciliation cycle
- If scaling occurs during deploy coordination, update coordination is skipped for that cycle
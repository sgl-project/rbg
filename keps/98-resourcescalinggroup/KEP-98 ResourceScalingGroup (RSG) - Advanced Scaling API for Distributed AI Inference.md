# KEP-98 ResourceScalingGroup (RSG) - Advanced Scaling API for Distributed AI Inference



- [Motivation](https://github.com/sgl-project/rbg/tree/1e0d9fb6c4213a7564fca4ae3b402927d4d48174/keps/29-auto-create-scaling-adapter#motivation)
- Proposal
  - User Stories (Optional)
    - [Story 1](https://github.com/sgl-project/rbg/tree/1e0d9fb6c4213a7564fca4ae3b402927d4d48174/keps/29-auto-create-scaling-adapter#story-1)
  - [Risks and Mitigations](https://github.com/sgl-project/rbg/tree/1e0d9fb6c4213a7564fca4ae3b402927d4d48174/keps/29-auto-create-scaling-adapter#risks-and-mitigations)
- Design Details
  - [Implementation](https://github.com/sgl-project/rbg/tree/1e0d9fb6c4213a7564fca4ae3b402927d4d48174/keps/29-auto-create-scaling-adapter#implementation)
  - Test Plan
    - [Unit Tests](https://github.com/sgl-project/rbg/tree/1e0d9fb6c4213a7564fca4ae3b402927d4d48174/keps/29-auto-create-scaling-adapter#unit-tests)
    - [Integration tests](https://github.com/sgl-project/rbg/tree/1e0d9fb6c4213a7564fca4ae3b402927d4d48174/keps/29-auto-create-scaling-adapter#integration-tests)
    - [End to End Tests](https://github.com/sgl-project/rbg/tree/1e0d9fb6c4213a7564fca4ae3b402927d4d48174/keps/29-auto-create-scaling-adapter#end-to-end-tests)

## Motivation

While RBG excels in workloads management, it exhibits notable limitations regarding auto scaling:

1. **Limitations of the Current ScalingAdapter:** The existing `ScalingAdapter` in RBG is primarily designed for single-dimension scaling. However, in typical "Prefill-Decode (PD) Disaggregation" architectures, components like Routers, Prefill nodes, and Decode nodes must scale in tandem based on specific ratios (e.g., 1:4). Achieving this coupled, multi-role scaling is difficult with current mechanisms without introducing complex external logic.

2. **High Barrier to Entry for Existing Workloads:** Many production clusters have existing inference workloads deployed via standard Deployments or StatefulSets. Forcing users to migrate and reconstruct these workloads into RBG resources to gain advanced scaling capabilities incurs high migration costs and potential service downtime. There is a critical need for a "non-intrusive" mechanism to adopt and orchestrate existing workloads without requiring redeployment.

3. **Lack of Deterministic Scale-Down Control:** In long-running inference scenarios, native Kubernetes scale-down policies (random or timestamp-based) often interrupt active requests. Current RBG implementations lack a mechanism to allow external systems to designate specific "idle groups" for graceful termination.

## Goals

**Multi-Role Coupled Scaling:** Enable ratio-based coupled scaling by defining bindings between a "Source" resource and "Target" resources (e.g., HPA controls Prefill, and Decode automatically scales at a 1:4 ratio).

**Non-Intrusive Adoption:** Allow direct management of existing Deployments, StatefulSets, or RBG objects via references, conferring advanced scaling capabilities without modifying the original resource definitions.

**State-Aware Scale Down:** Provide a declarative API to target specific resource group IDs for termination, facilitating zero-downtime scaling when coordinated with external traffic gateways.

## Non-Goals

**Replacing HPA Metrics Logic:** RSG does not calculate scaling metrics. It relies on HPA or other planner to drive changes in the resource groups.

## Proposal



### User Stories



#### Story 1



> Ratio-Based Scaling for PD Disaggregation

In PD disaggregation scenarios (1 Router + N Prefill wokers + M Decode Workers), RSG's `GroupReplication` mode binds components into atomic groups. Via the `scaleDown.candidates` field, RSG coordinates with traffic gateways to deterministically terminate specific idle units, achieving **zero-downtime graceful scale-down** and eliminating errors caused by native Kubernetes random pod deletion.



#### Story 2



> Proportional Scaling for Resource Pools

In existing resource pool scenarios (e.g., separate Prefill and Decode Deployments), RSG's `InplaceScaling` mode adopts existing workloads directly. Users configure HPA only for the primary resource (Prefill), and RSG automatically adjusts secondary resources (Decode) based on a fixed ratio (e.g., 1:4), **preventing ratio drift ** often caused by independent HPA configurations.

### Risks and Mitigations

### 1. Circular Dependencies

- **Risk**: In `InplaceScaling` mode, if Resource A follows B, and B follows A, an infinite scaling loop will occur.
- **Mitigation**: Implement strict validation webhooks to ensure the dependency graph in `bindings` is a Directed Acyclic Graph (DAG) and contains exactly one `Source`.

### 2. Orphaned Resources

- **Risk**: If RSG is deleted, should the scaled resources revert to their original state?
- **Mitigation**: For `InplaceScaling` (Adoption mode), the default behavior is **Retain**. RSG stops managing the resources, leaving them at their current scale. This prevents accidental outages during control plane maintenance.

## Design Details

This KEP proposes a **Structural Union** design pattern to support two distinct operational modes within a single CRD: `GroupReplication` and `InplaceScaling`.

An example of GroupReplication mode.

```yaml
apiVersion: resourcescalinggroup.com/v1
kind: ResourceScalingGroup
metadata:
  name: pd-cluster
spec:
  targetResources:
    - name: prefill
      namespace: ai
      apiVersion: apps/v1
      kind: Deployment
    - name: decode
      namespace: ai
      apiVersion: apps/v1
      kind: Deployment

  # 2. two different strategies of scaling: GroupReplication and InplaceScaling
  scalingStrategy:
    type: GroupReplication
    groupConfig:
      groupName: "inference-unit"
      replicas: 1 #  
      scaleDown:
        policy: Ordered  # Ordered/Oldest/Newest
        candidates: 
          - "inference-unit-02" 
```

RSG orchestrates the cloning and lifecycle management of multiple resource groups. In this configuration, the controller utilizes the resources defined in `targetResources` as templates, where a default `replicas` value of 1 represents the initial resource group. During scale-out events, new resources are provisioned with the prefix `inference-unit-{index}`. During scale-in, the controller prioritizes entries in the `candidates` list to achieve precise, graceful termination.

```
apiVersion: resourcescalinggroup.com/v1
kind: ResourceScalingGroup
metadata:
  name: pd-pool
spec:
  targetResources:
    - name: rolebasedgroup1
      namespace: ai
      apiVersion: workloads.x-k8s.io/v1alpha1
      kind: RoleBasedGroup
      subResources:
        - roleName: "prefill"
        - roleName: "Decode"
    - name: router
      namespace: ai
      apiVersion: apps/v1
      kind: Deployment
  scalingStrategy:
    type: InplaceScaling
    inplaceConfig:
      bindings:
        - name: router
          role: Source
        - name: prefill
          role: Target
          ratio: 1.0
        - name: decode
          role: Target
          ratio: 2.0
```

RSG watches for changes in the `Source` resource's replica count and automatically adjusts `Target` resources according to the defined ratios. In this configuration, RSG operates in a **stateless manner** (omitting the `replicas` field). The controller watch the `router`. Upon detecting a scale-out event (e.g., the router scaling to 10 replicas), RSG immediately calculates the **desired state** for the `prefill` (10 * 1 = 10) and `decode` (10 * 2 = 20) resources. It then performs an **in-place Patch** on the corresponding resources, rather than provisioning new resource clones.



#### Interface

```go
// ScalingStrategyHandler abstracts the logic for different scaling modes.
type ScalingStrategyHandler interface {
    // Reconcile executes the scaling logic based on the RSG spec.
    // It returns the desired state or performs the scaling actions.
    Reconcile(ctx context.Context, rsg *v1.ResourceScalingGroup) (ctrl.Result, error)
}
```

```go
// ResourceAdapter abstracts operations on underlying Kubernetes resources.
type ResourceAdapter interface {
    // GetReplicas returns the current replica count of the resource.
    // For RBG, this might involve parsing a specific role from the Spec.
    GetReplicas(ctx context.Context, objName types.NamespacedName) (int32, error)

    // SetReplicas patches the resource to the desired replica count.
    // Supports JSON Patch or SubResource updates.
    SetReplicas(ctx context.Context, objName types.NamespacedName, replicas int32) error
    
    // Clone creates a copy of the resource with a new name (For GroupReplication).
    Clone(ctx context.Context, templateName types.NamespacedName, newName string) error
}
```

```go
type RSGReconciler struct {
    Client    client.Client
    Scheme    *runtime.Scheme
    // Registry of supported strategies
    Strategies map[v1.ScalingStrategyType]ScalingStrategyHandler
}

func (r *RSGReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // 1. Fetch RSG object
    var rsg v1.ResourceScalingGroup
    if err := r.Get(ctx, req.NamespacedName, &rsg); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // 2. Select Strategy Implementation
    handler, ok := r.Strategies[rsg.Spec.ScalingStrategy.Type]
    if !ok {
        return ctrl.Result{}, fmt.Errorf("unknown strategy type")
    }

    return handler.Reconcile(ctx, &rsg)
}
```

```
type ResourceScalingGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ResourceScalingGroupSpec   `json:"spec,omitempty"`
	Status ResourceScalingGroupStatus `json:"status,omitempty"`
}

// ResourceScalingGroupSpec defines the desired state of ResourceScalingGroup
type ResourceScalingGroupSpec struct {
	// TargetResources defines the inventory of resources managed by this RSG.
	// +kubebuilder:validation:MinItems=1
	TargetResources []TargetResource `json:"targetResources"`

	// ScalingStrategy defines the operational mode and configuration.
	ScalingStrategy ScalingStrategy `json:"scalingStrategy"`
}

// TargetResource defines a specific Kubernetes resource to be managed.
type TargetResource struct {
	// Name serves as a local identifier to be referenced in bindings.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// TargetRef points to the actual K8s resource.
	TargetRef CrossVersionObjectReference `json:"targetRef"`

	// SubResources allows targeting specific internal fields (e.g., for RBG).
	// +optional
	SubResources []SubResource `json:"subResources,omitempty"`
}

// CrossVersionObjectReference contains enough information to let you identify the referred resource.
type CrossVersionObjectReference struct {
	// Kind of the referent.
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`
	// Name of the referent.
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// API version of the referent.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
}

// SubResource defines specific paths within a custom resource.
type SubResource struct {
	// RoleName identifies a specific role within a complex CRD (like SGLang RBG).
	// +kubebuilder:validation:Required
	RoleName string `json:"roleName"`
}

// ScalingStrategyType defines the supported scaling modes.
// +kubebuilder:validation:Enum=GroupReplication;InplaceScaling
type ScalingStrategyType string

const (
	GroupReplication ScalingStrategyType = "GroupReplication"
	InplaceScaling   ScalingStrategyType = "InplaceScaling"
)

// ScalingStrategy holds the configuration for the selected mode.
type ScalingStrategy struct {
	// Type determines the logic used by the controller.
	// +kubebuilder:validation:Required
	Type ScalingStrategyType `json:"type"`

	// GroupConfig holds configuration for GroupReplication mode.
	// +optional
	GroupConfig *GroupConfig `json:"groupConfig,omitempty"`

	// InplaceConfig holds configuration for InplaceScaling mode.
	// +optional
	InplaceConfig *InplaceConfig `json:"inplaceConfig,omitempty"`
}

// GroupConfig defines parameters for creating multiple replicas of a resource group.
type GroupConfig struct {
	// GroupNamePrefix is used to generate names for new resource groups.
	// +kubebuilder:validation:Required
	GroupNamePrefix string `json:"groupNamePrefix"`

	// Replicas is the desired number of groups.
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`

	// ScaleDown defines the policy for removing groups.
	// +optional
	ScaleDown *ScaleDownConfig `json:"scaleDown,omitempty"`
}

// ScaleDownPolicy defines the strategy for selecting victims.
// +kubebuilder:validation:Enum=Ordered;Newest;Oldest
type ScaleDownPolicy string

const (
	Ordered ScaleDownPolicy = "Ordered"
	Newest  ScaleDownPolicy = "Newest"
	Oldest  ScaleDownPolicy = "Oldest"
)

// ScaleDownConfig controls how scale-in events are handled.
type ScaleDownConfig struct {
	// Policy determines the order of deletion.
	// +optional
	// +kubebuilder:default=Ordered
	Policy ScaleDownPolicy `json:"policy,omitempty"`

	// Candidates is a list of group IDs preferred for deletion.
	// +optional
	Candidates []string `json:"candidates,omitempty"`
}

// InplaceConfig defines parameters for proportional scaling.
type InplaceConfig struct {
	// Bindings defines the relationship between Source and Targets.
	// +kubebuilder:validation:MinItems=1
	Bindings []Binding `json:"bindings"`
}

// BindingRole defines the role of a resource in the dependency graph.
// +kubebuilder:validation:Enum=Source;Target
type BindingRole string

const (
	SourceRole BindingRole = "Source"
	TargetRole BindingRole = "Target"
)

// Binding connects a resource from TargetResources to a scaling role.
type Binding struct {
	// Name must match a name defined in spec.targetResources.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Role determines if this resource drives scaling (Source) or is driven by it (Target).
	// +kubebuilder:validation:Required
	Role BindingRole `json:"role"`

	// Ratio is the multiplier applied to the Source replicas to calculate Target replicas.
	// Only valid when Role is Target.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// Using string to support decimals safely in JSON (parsed as float64 in controller).
	Ratio string `json:"ratio,omitempty"`
}

// ResourceScalingGroupStatus defines the observed state of ResourceScalingGroup
type ResourceScalingGroupStatus struct {
	// CurrentReplicas is the total number of managed groups (Mode A) or source replicas (Mode B).
	CurrentReplicas int32 `json:"currentReplicas"`

	// LabelSelector is used by HPA to discover the resource.
	// +optional
	LabelSelector string `json:"labelSelector,omitempty"`

	// Conditions store the status of the controller (Ready, Stalled, etc.)
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// ResourceScalingGroupList contains a list of ResourceScalingGroup
type ResourceScalingGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceScalingGroup `json:"items"`
}
```

### Test Plan



[X] I/we understand the owners of the involved components may require updates to existing tests to make this code solid enough prior to committing the changes necessary to implement this enhancement.

#### Unit Tests



#### Integration tests



#### End to End Tests



## Alternatives
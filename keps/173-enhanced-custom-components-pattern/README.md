# KEP-173: Enhanced CustomComponentsPattern — Component Lifecycle Ordering and Intra-Role Service Discovery

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
    - [Pattern Design: StandalonePattern and LeaderWorkerPattern as Syntactic Sugar](#pattern-design-standalonepattern-and-leaderworkerpattern-as-syntactic-sugar)
    - [Emerging Need: Complex Heterogeneous Topologies](#emerging-need-complex-heterogeneous-topologies)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [User Stories](#user-stories)
        - [Story 1: Disaggregated Prefill with Hierarchical Components](#story-1-disaggregated-prefill-with-hierarchical-components)
        - [Story 2: Domestic GPU Card Special Adaptation](#story-2-domestic-gpu-card-special-adaptation)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
    - [Feature 1: Component Lifecycle Ordering](#feature-1-component-lifecycle-ordering)
    - [Feature 2: Intra-Role Service Discovery Annotation](#feature-2-intra-role-service-discovery-annotation)
        - [API](#api)
        - [Address Reference Resolution](#address-reference-resolution)
        - [Port Reference Resolution](#port-reference-resolution)
        - [Implementation](#implementation)
    - [Test Plan](#test-plan)
        - [Unit Tests](#unit-tests)
        - [Integration Tests](#integration-tests)
        - [End to End Tests](#end-to-end-tests)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

`CustomComponentsPattern` is the foundational building block for all heterogeneous, multi-pod-per-replica
deployments in RoleBasedGroup (RBG). Both `StandalonePattern` and `LeaderWorkerPattern` are
higher-level abstractions — effectively syntactic sugar — built on top of the same underlying
`CustomComponentsPattern` execution path in the controller.

As AI model serving architectures evolve, especially with the proliferation of disaggregated
Prefill-Decode (PD) deployments and special adaptations for domestic GPU cards, users increasingly
need to express complex, flexible topologies using `CustomComponentsPattern` directly. Two critical
capabilities are missing today:

1. **Deterministic component startup/teardown order** — components within a role often have hard
   boot dependencies (e.g. Router must start before Leader, Leader must start before Workers).
2. **Intra-role service discovery** — components within the same role instance need to discover each
   other's network addresses and port values at Pod launch time via environment variables, without
   relying on external service meshes or manual configuration.

This KEP proposes to address both gaps with minimal API surface changes.

## Motivation

### Pattern Design: StandalonePattern and LeaderWorkerPattern as Syntactic Sugar

In the current RBG v1alpha2 implementation, `RoleSpec` embeds a `Pattern` struct inline:

```
RoleSpec.Pattern (inline)
├── StandalonePattern
│     └── TemplateSource (inline/templateRef)
├── LeaderWorkerPattern
│     ├── TemplateSource (base, inline/templateRef)
│     ├── LeaderTemplatePatch  (strategic merge patch on base)
│     └── WorkerTemplatePatch  (strategic merge patch on base)
└── CustomComponentsPattern
      └── Components[]
            ├── name
            ├── size
            ├── serviceName
            └── template (inline PodTemplateSpec)
```

When the controller reconciles a RoleInstanceSet, the `switch` on pattern type ultimately converges
on the same `RoleInstanceTemplate.Components[]` abstraction for all three patterns:

| Pattern | Components generated | Notes |
|---|---|---|
| `StandalonePattern` | 1 component named after the role | size=1 |
| `LeaderWorkerPattern` | `leader` (size=1) + `worker` (size=N-1) | base template + patch per component |
| `CustomComponentsPattern` | N components as declared | fully user-defined |

`StandalonePattern` and `LeaderWorkerPattern` are therefore convenience wrappers that compile down
to `CustomComponentsPattern`-equivalent structures. This design gives `CustomComponentsPattern` the
full expressive power of the system.

### Emerging Need: Complex Heterogeneous Topologies

Modern LLM inference deployments — particularly disaggregated Prefill-Decode architectures and
inference frameworks targeting domestic GPU accelerators — require multi-component role topologies
that go beyond the two fixed slots of `LeaderWorkerPattern`. A concrete example:

**Disaggregated Prefill Role**

```
Role: prefill (replicas: N)
  ┌─────────────────────────────────────────────────┐
  │  Instance i                                     │
  │                                                 │
  │  [Prefill Router]  ←── routes requests          │
  │       │                                         │
  │  [Prefill Leader]  ←── coordinates KV cache     │
  │       │                                         │
  │  [Prefill Worker × M]  ←── tensor parallel exec │
  └─────────────────────────────────────────────────┘
```

These three component types (Router, Leader, Worker) must:
- **Scale as a single unit** — one `replicas` field controls the entire topology.
  Splitting into three separate roles would break the 1:M proportional relationship and
  add unnecessary coordination overhead.
- **Start in a defined order** — Workers register with Leader; Leader registers with Router.
  If Worker starts before Leader's gRPC server is ready, the worker crashes and restarts.
- **Discover each other at Pod launch** — Worker Pods need Leader's address/port as env vars
  to establish their initial connection before the service mesh DNS is warm.

Today, `CustomComponentsPattern` already handles the "scale as a unit" requirement. This KEP
addresses the remaining two gaps.

### Goals

- Support deterministic component creation order (array order) and deletion order (reverse array
  order) within `CustomComponentsPattern`, without introducing new API fields.
- Introduce an opt-in annotation `rolebasedgroup.workloads.x-k8s.io/component-discovery` on
  component Pod templates to inject intra-role component addresses and port values as environment
  variables.
- Address injection must support FQDN format and work with headless services.
- Port injection must integrate seamlessly with the existing
  `rolebasedgroup.workloads.x-k8s.io/port-allocator` annotation.

### Non-Goals

- Inter-role service discovery (already handled by existing `DiscoveryConfig` mechanisms).
- Complex DAG-based dependency ordering with readiness probes between components (deferred to a
  future KEP when operational evidence shows the simple array-order assumption is insufficient).
- Dynamic re-discovery after initial Pod launch (env vars are static at Pod creation time).
- `CustomComponentsPattern` support for `templateRef` (tracked separately).

## Proposal

### User Stories

#### Story 1: Disaggregated Prefill with Hierarchical Components

As an AI infrastructure engineer, I want to deploy a Prefill role that consists of:
- 1 **Router** pod: receives incoming prefill requests and dispatches them
- 1 **Leader** pod: coordinates KV-cache transfer among workers
- 4 **Worker** pods: execute the actual tensor-parallel forward pass

All 6 pods belong to one role instance and must scale together. The Workers need to know the
Leader's gRPC address at startup. The Leader needs to know the Router's address.

With this KEP, I can express this as:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: llm-prefill
spec:
  roles:
    - name: prefill
      replicas: 2
      customComponentsPattern:
        components:
          # Array order = creation order: router → leader → worker
          - name: router
            size: 1
            template:
              metadata:
                annotations:
                  rolebasedgroup.workloads.x-k8s.io/port-allocator: |
                    {
                      "allocations": [
                        {
                          "name": "router-grpc",
                          "env": "ROUTER_GRPC_PORT",
                          "annotationKey": "example/router-grpc-port",
                          "scope": "RoleScoped"
                        }
                      ]
                    }
              spec:
                containers:
                  - name: router
                    image: prefill-router:v1

          - name: leader
            size: 1
            template:
              metadata:
                annotations:
                  rolebasedgroup.workloads.x-k8s.io/port-allocator: |
                    {
                      "allocations": [
                        {
                          "name": "leader-grpc",
                          "env": "LEADER_GRPC_PORT",
                          "annotationKey": "example/leader-grpc-port",
                          "scope": "RoleScoped"
                        }
                      ]
                    }
                  rolebasedgroup.workloads.x-k8s.io/component-discovery: |
                    {
                      "portRefs": [
                        {
                          "env": "ROUTER_GRPC_PORT",
                          "component": "router",
                          "portName": "router-grpc"
                        }
                      ],
                      "addressRefs": [
                        {
                          "env": "ROUTER_ADDR",
                          "component": "router",
                          "index": 0
                        }
                      ]
                    }
              spec:
                containers:
                  - name: leader
                    image: prefill-leader:v1

          - name: worker
            size: 4
            template:
              metadata:
                annotations:
                  rolebasedgroup.workloads.x-k8s.io/component-discovery: |
                    {
                      "portRefs": [
                        {
                          "env": "LEADER_GRPC_PORT",
                          "component": "leader",
                          "portName": "leader-grpc"
                        }
                      ],
                      "addressRefs": [
                        {
                          "env": "LEADER_ADDR",
                          "component": "leader",
                          "index": 0
                        }
                      ]
                    }
              spec:
                containers:
                  - name: worker
                    image: prefill-worker:v1
```

When an instance `prefill-0` is created, the resulting Pods will have:
- `leader-0` Pod env: `ROUTER_GRPC_PORT=<allocated-port>`, `ROUTER_ADDR=<router-0-fqdn>`
- `worker-{0..3}` Pods env: `LEADER_GRPC_PORT=<allocated-port>`, `LEADER_ADDR=<leader-0-fqdn>`

#### Story 2: Domestic GPU Card Special Adaptation

As an AI platform engineer deploying inference workloads on domestic GPU accelerators (e.g. Ascend,
Cambricon, Iluvatar), I need to model a role that contains:
- A **coordinator** process that manages the GPU communication fabric
- A **compute** process that runs the model

The coordinator must start first, establish its communication endpoints, and the compute process
must receive those endpoints at startup. `CustomComponentsPattern` with the new
`component-discovery` annotation lets me express this without any per-framework custom operator.

### Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Array-order creation assumption may not hold in all scheduler scenarios | The ordering is best-effort at the controller level (create in order, do not create next until previous is scheduled). For strict readiness-based ordering, users should use `restartPolicy` + liveness/readiness probes. |
| Discovery annotation references a component that doesn't exist | Controller validation (webhook) rejects the RBG at admission time. |
| Port reference targets a port not defined in port-allocator annotation | Controller returns error in reconcile and surfaces via RBG status condition. |
| Circular address references (A refs B, B refs A) | No cycle is possible since a component can only reference addresses/ports; it cannot be referenced by itself to create a dependency loop. Annotations are pure data injection, not execution ordering. |

## Design Details

### Feature 1: Component Lifecycle Ordering

**Creation Order**

Components within `CustomComponentsPattern.components[]` are created in array declaration order.
The controller iterates the components slice in forward order when constructing `RoleInstance` specs.
The underlying `RoleInstanceSet` controller then creates component pods in the same order, waiting
for each component's pods to reach the `Scheduled` phase before proceeding to the next component.

This is a **zero-API-change** implementation: the semantics are derived purely from the existing
array ordering of the `components` field.

Example:
```yaml
components:
  - name: router    # created first
  - name: leader    # created after router pods are scheduled
  - name: worker    # created last
```

**Deletion Order**

When a RoleInstance is deleted (scale-down or RBG deletion), components are torn down in
**reverse array order**:

```
worker → leader → router
```

This ensures dependent processes are stopped before the processes they depend on.

The deletion order is implemented in the `RoleInstanceSet` controller's scale-down path by
reversing the component list when issuing pod deletions.

**Rationale for not adding API fields now**

Explicit ordering fields (e.g. `dependsOn`, `after`, priority integers) add significant API surface
and validation complexity. Operational evidence from early adopters will inform whether simple
array-order is sufficient. A follow-up KEP will introduce explicit ordering API if needed.

---

### Feature 2: Intra-Role Service Discovery Annotation

#### API

A new opt-in annotation is introduced on the Pod template of an `InstanceComponent`:

```
rolebasedgroup.workloads.x-k8s.io/component-discovery
```

The annotation value is a JSON object with the following structure:

```go
// ComponentDiscoveryConfig is the top-level configuration for the component-discovery annotation.
type ComponentDiscoveryConfig struct {
    // AddressRefs specifies intra-role pod address references to inject as env vars.
    // +optional
    AddressRefs []ComponentAddressRef `json:"addressRefs,omitempty"`

    // PortRefs specifies intra-role port value references to inject as env vars.
    // Port values are resolved from the port-allocator annotation on the referenced component.
    // +optional
    PortRefs []ComponentPortRef `json:"portRefs,omitempty"`
}

// ComponentAddressRef injects the FQDN address of a specific pod within a named component
// of the same role instance as an environment variable.
type ComponentAddressRef struct {
    // Env is the name of the environment variable to inject.
    // Must be a valid environment variable name.
    // +required
    Env string `json:"env"`

    // Component is the name of the InstanceComponent within the same role to reference.
    // +required
    Component string `json:"component"`

    // Index is the zero-based ordinal index of the pod within the component.
    // Defaults to 0 (the first pod).
    // +optional
    // +kubebuilder:default=0
    Index int32 `json:"index,omitempty"`
}

// ComponentPortRef injects a port value that was allocated by the port-allocator annotation
// on a named component of the same role instance as an environment variable.
type ComponentPortRef struct {
    // Env is the name of the environment variable to inject.
    // Must be a valid environment variable name.
    // +required
    Env string `json:"env"`

    // Component is the name of the InstanceComponent within the same role to reference.
    // +required
    Component string `json:"component"`

    // PortName is the logical port name as defined in the port-allocator annotation's
    // `allocations[].name` field on the referenced component.
    // +required
    PortName string `json:"portName"`

    // Index is the zero-based ordinal index of the pod within the component.
    // Used to resolve PodScoped port keys. Defaults to 0.
    // +optional
    // +kubebuilder:default=0
    Index int32 `json:"index,omitempty"`
}
```

Example annotation value on a `worker` component that needs to know the `leader`'s address and
gRPC port:

```json
{
  "addressRefs": [
    {
      "env": "LEADER_ADDR",
      "component": "leader",
      "index": 0
    }
  ],
  "portRefs": [
    {
      "env": "LEADER_GRPC_PORT",
      "component": "leader",
      "portName": "leader-grpc"
    }
  ]
}
```

#### Address Reference Resolution

The FQDN address for the `index`-th pod of component `<comp>` in role instance `<role>-<i>` is
constructed as:

```
<rbg-name>-<role-name>-<instance-index>-<comp-name>-<pod-index>.<service-name>.<namespace>.svc.cluster.local
```

Where `<service-name>` is the headless service name for the role, which already exists as created
by the `ServiceReconciler`.

**Example**: For RBG `llm-prefill`, role `prefill`, instance ordinal `0`, component `leader`, pod
index `0`, service `s-llm-prefill-prefill`:

```
llm-prefill-prefill-0-leader-0.s-llm-prefill-prefill.default.svc.cluster.local
```

This FQDN is deterministic and can be computed at Pod creation time without any lookups.

#### Port Reference Resolution

Port references are resolved using the same mechanism as the existing port-allocator reference
feature (`from` field in `PortReference`). The resolution reads the allocated port value from the
`RoleInstance` annotation.

**Port key formats** (consistent with KEP-171):

| Port scope | Key format | Example |
|---|---|---|
| `RoleScoped` | `<component-name>.<port-name>` | `leader.leader-grpc` |
| `PodScoped` | `<pod-name>.<port-name>` | `llm-prefill-prefill-0-leader-0.leader-grpc` |

When a `ComponentPortRef` is processed, the controller:
1. Determines whether the target port is `RoleScoped` or `PodScoped` by inspecting the
   port-allocator annotation on the referenced component template.
2. Constructs the appropriate key and looks up the value in the `RoleInstance` annotation.
3. Injects the value as the specified env var into all pods of the annotated component.

#### Implementation

The `component-discovery` annotation is processed in the **Pod reconciler**
(`PodReconciler.ConstructPodTemplateSpecApplyConfiguration`), after port allocation and before
writing the final Pod spec. This mirrors the existing `port-allocator` injection flow.

**Processing steps in the Pod reconciler:**

```
1. Parse component-discovery annotation from pod template
2. For each AddressRef:
   a. Compute target pod FQDN from (component, index, role context)
   b. Append env var {Name: ref.Env, Value: <fqdn>} to all containers
3. For each PortRef:
   a. Look up resolved port value from RoleInstance annotation
      (using same key format as port-allocator)
   b. Append env var {Name: ref.Env, Value: <port>} to all containers
4. Remove component-discovery annotation from the generated Pod
   (it is a controller directive, not a pod-level annotation)
```

**Validation (admission webhook):**

- All `component` names in `ComponentAddressRef` and `ComponentPortRef` must reference a component
  name that exists in the same `CustomComponentsPattern.components[]` array.
- A component cannot reference itself (self-reference is a no-op and is rejected to avoid
  confusion).
- `index` must be within the bounds of the referenced component's `size`.
- `portName` in `ComponentPortRef` must correspond to an `allocations[].name` defined in the
  referenced component's port-allocator annotation.

**Constraint**: The `component-discovery` annotation is only valid within `CustomComponentsPattern`.
Using it in `StandalonePattern` or `LeaderWorkerPattern` templates is rejected by the webhook.

### Test Plan

#### Unit Tests

- `ComponentDiscoveryConfig` JSON parsing and validation
- FQDN construction for `AddressRef` with various (component, index) inputs
- Port key construction for `PortRef` with `RoleScoped` and `PodScoped` ports
- Env var injection into Pod spec
- Validation: unknown component name rejected
- Validation: out-of-bounds index rejected
- Validation: portName not defined in port-allocator annotation rejected

#### Integration Tests

- Component creation order is respected: verify pods are created in array order within a
  RoleInstance
- Component deletion order is respected: verify pods are deleted in reverse array order on
  scale-down
- Address injection: FQDN env var is correctly set in target pods
- Port injection: port value env var matches the value in RoleInstance annotation

#### End to End Tests

- Deploy RBG with a 3-component role (router/leader/worker), verify:
  - Pods start in the correct order
  - `LEADER_ADDR` env var in worker pods resolves to the expected FQDN
  - `LEADER_GRPC_PORT` env var in worker pods matches the port-allocator allocated value
  - Scale-down removes worker pods before leader pods before router pods

## Alternatives

### Alternative 1: Explicit `dependsOn` Field in InstanceComponent

Add a `dependsOn []string` field to `InstanceComponent` to express explicit ordered dependencies
between components. This is more expressive but significantly more complex to validate and reconcile
(cycle detection, partial ordering). Deferred until array-order proves insufficient.

### Alternative 2: Dedicated ServiceDiscovery CRD

Introduce a new `RoleInstanceDiscovery` CRD that manages address/port bindings between components.
This is architecturally cleaner but adds operational overhead (a new CRD, a new controller, new
RBAC). The annotation-based approach delivers the same user value with zero new resources.

### Alternative 3: Init Container for Discovery

Users could use init containers that poll for the target pod's address before the main container
starts. This is workable but shifts complexity to the user, is error-prone, and duplicates logic
that RBG can provide centrally and consistently.

### Alternative 4: Environment Variable Injection via Downward API

Kubernetes Downward API does not support referencing other pods' fields. Custom env injection at
the controller level (as proposed) is the only viable approach that keeps the user experience
declarative.

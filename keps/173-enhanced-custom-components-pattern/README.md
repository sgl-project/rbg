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

**What is a Component — the RoleInstanceSet connection**

The `component` concept in `CustomComponentsPattern` is not an RBG-internal abstraction; it
maps **one-to-one** onto the `RoleInstanceComponent` type defined in `RoleInstanceSet`.

```
RBG.spec.roles[].customComponentsPattern.components[]   (InstanceComponent)
          │   controller translates
          ▼
RoleInstanceSet.spec.roleInstanceTemplate.components[]  (RoleInstanceComponent)
          │   controller creates one RoleInstance per replica
          ▼
RoleInstance.spec.components[]                          (RoleInstanceComponent)
          │   controller creates pods
          ▼
Pod × component.size
```

`RoleInstanceSet` is the actual workload CR that the RBG controller creates for a role. It holds
`spec.roleInstanceTemplate`, which carries a `components[]` slice — each entry corresponds
directly to one component in the user-declared `CustomComponentsPattern`. The
`RoleInstanceSet` controller then instantiates one `RoleInstance` per replica, and each
`RoleInstance` spawns `component.size` pods per component. Therefore:

> A **component** in `CustomComponentsPattern` is the user-facing description of a
> `RoleInstanceComponent` inside the `RoleInstanceSet` that backs this role.

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

- Support flexible, explicit component startup/teardown ordering within `CustomComponentsPattern`
  via a per-component annotation `rolebasedgroup.workloads.x-k8s.io/component-depends-on` with
  `startAfter` and `deleteAfter` fields. The readiness gate uses
  `RoleInstance.Status.ComponentStatuses.ReadyReplicas >= Size` rather than pod-level scheduling.
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
          # leader and worker start in parallel (no startAfter);
          # both must be deleted after router is gone (deleteAfter).
          - name: leader
            size: 1
            annotations:
              rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
                {"deleteAfter": ["router"]}
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
            template:
              spec:
                containers:
                  - name: leader
                    image: prefill-leader:v1

          - name: worker
            size: 4
            annotations:
              rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
                {"deleteAfter": ["router"]}
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
            template:
              spec:
                containers:
                  - name: worker
                    image: prefill-worker:v1

          # router starts only after both leader and worker are Ready;
          # it is deleted first (no deleteAfter — reverse of startAfter is auto-derived).
          - name: router
            size: 1
            annotations:
              rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
                {"startAfter": ["leader", "worker"]}
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
            template:
              spec:
                containers:
                  - name: router
                    image: prefill-router:v1
```

When an instance `prefill-0` is created, the resulting Pods will have:
- `worker-{0..3}` Pods env: `LEADER_GRPC_PORT=<allocated-port>`, `LEADER_ADDR=<leader-0-fqdn>`

The `router` pod is created only after both `leader` and `worker` pods have `ReadyReplicas == Size`
in the instance's `componentStatuses`. On deletion, `router` is removed first, followed by
`leader` and `worker` in parallel.

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
| `startAfter` gate may cause components to wait longer than expected | The gate is evaluated against `RoleInstance.Status.ComponentStatuses.ReadyReplicas >= Size`, which is set only when pods pass readiness probes. This is the intended semantics — if a dependency is slow to become ready, the controller waits correctly without creating unready dependents. |
| Discovery annotation references a component that doesn't exist | Controller validation (webhook) rejects the RBG at admission time. |
| Port reference targets a port not defined in port-allocator annotation | Controller returns error in reconcile and surfaces via RBG status condition. |
| Circular dependency in `startAfter` / `deleteAfter` (A refs B, B refs A) | `DetectCycle` runs DFS on the dependency graph at each reconcile. If a cycle is found, the controller logs an error and falls back to the default parallel mode to avoid a deadlock. Note: cycles involving different components (A→B and B→A) are possible even when self-reference is disallowed; the cycle detection handles this case. |

## Design Details

### Feature 1: Component Lifecycle Ordering

**Default behavior: Parallel (no breaking change)**

When no `component-depends-on` annotation is present on any component, all components in a
`CustomComponentsPattern` are created and deleted concurrently — preserving existing behavior.
No change is made to any workload that does not set the annotation.

**Per-component annotation: `rolebasedgroup.workloads.x-k8s.io/component-depends-on`**

Ordering constraints are expressed **per component** via the component's top-level `annotations`
field (not on the role, and not in `template.metadata.annotations`). This gives fine-grained,
explicit control over which component must start/stop before which.

The annotation value is a JSON object:

```go
// ComponentDependsOnConfig is the JSON-encoded value of the component-depends-on annotation.
type ComponentDependsOnConfig struct {
    // StartAfter lists components that must have ReadyReplicas == Size in
    // the instance's componentStatuses before this component's pods are created.
    // By default (when DeleteAfter is absent), the implicit delete order is the reverse:
    // this component is deleted before any component it listed in StartAfter.
    // +optional
    StartAfter []string `json:"startAfter,omitempty"`

    // DeleteAfter lists components that must be fully deleted before this
    // component's pods are deleted. Independent of StartAfter — use it to express
    // a delete order that differs from the reverse start order.
    // When both are set, both constraints are applied (union).
    // +optional
    DeleteAfter []string `json:"deleteAfter,omitempty"`
}
```

**Readiness gate for `startAfter`**

A component listed in `startAfter` is considered **ready** when its entry in the owning
`RoleInstance.Status.ComponentStatuses` satisfies:

```
ReadyReplicas >= Size  &&  Size > 0
```

This is evaluated on the status written by the previous reconcile loop, so the gate becomes true
only after the dependency's pods are genuinely ready (not merely scheduled).

**Why `ReadyReplicas >= Size` instead of `Scheduled`?**

The motivation section describes scenarios where workers crash because the leader's gRPC server
is not yet ready when they attempt to connect. The `Scheduled` condition only indicates that a pod
has been assigned to a node — the container may not even be running yet. Using `ReadyReplicas >= Size`
ensures that all pods of the dependency component have passed their readiness probes, meaning the
application is actually serving traffic.

**Recommended user-side configuration:**

When using `startAfter` ordering, users should:
- Define proper `readinessProbe` in the dependency component's pod spec (e.g., TCP socket check on
  the gRPC port, or an HTTP health endpoint).
- Ensure the dependent component's startup logic handles transient connection failures gracefully
  (the dependency may become unavailable after initial readiness due to restarts).
- Consider using `restartPolicy: OnFailure` at the RBG level if transient failures should trigger
  pod restart rather than role instance recreation.

**Example: Router starts after Leader and Worker**

```yaml
spec:
  roles:
    - name: prefill
      replicas: 2
      customComponentsPattern:
        components:
          - name: leader
            size: 1
            annotations:
              rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
                {"deleteAfter": ["router"]}
            # ...

          - name: worker
            size: 2
            annotations:
              rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
                {"deleteAfter": ["router"]}
            # ...

          - name: router
            size: 1
            annotations:
              rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
                {"startAfter": ["leader", "worker"]}
            # ...
```

**Creation Order**

When the controller reconciles scale-out, it iterates all components with pending pod creation.
For each component, it checks whether all names in its `startAfter` list have
`ReadyReplicas >= Size` in `RoleInstance.Status.ComponentStatuses`. Only components whose
dependencies are satisfied are allowed to proceed with pod creation in that reconcile pass.

**Deletion Order**

Deletion gates are derived from two sources (union of both):

1. **Reverse of `startAfter`**: if component `X` started after `Y`, then `Y` is deleted only
   after `X` is fully gone (no excess pods remain).
2. **Explicit `deleteAfter`**: the annotated component waits for the listed components to be
   fully deleted before its own pods are removed.

Example (using the YAML above):
```
Delete order: router → (leader, worker in parallel)
  gates["router"]  = []                  # deleted first
  gates["leader"]  = ["router"]          # waits for router deletion
  gates["worker"]  = ["router"]          # waits for router deletion
```

**Cycle detection**

If the `startAfter` or `deleteAfter` dependency graph contains a cycle, the controller logs an
error and falls back to the default parallel mode to avoid a deadlock.

**Rationale for component-level annotation over role-level annotation**

The previous design used a single role-level annotation (`role-component-lifecycle: Ordered`) that
imposed strict array-order on all components. The new per-component annotation:

- Allows non-sequential topologies (e.g. leader and worker start in parallel, router starts after both).
- Keeps the CRD schema unchanged (annotations, not fields).
- Is orthogonal — components without the annotation are unaffected.

**Constraint**: This annotation is only meaningful within `CustomComponentsPattern` roles.
Using it on `StandalonePattern` or `LeaderWorkerPattern` templates is ignored (those patterns
manage their own lifecycle).

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
by the `ServiceReconciler`. The relationship between `serviceName` and the role-level headless
service is:
- The `InstanceComponent.serviceName` field specifies the service that backs the component's pods.
- If not explicitly set, the controller uses the role's default headless service (named
  `s-<rbg-name>-<role-name>` by convention).
- The FQDN uses this service name as the subdomain, enabling DNS resolution via Kubernetes
  headless service DNS.

**Cluster domain assumption**: The FQDN format uses `.svc.cluster.local` as the default cluster
domain suffix. This matches the vast majority of Kubernetes clusters. If your cluster uses a
custom domain (e.g., `.svc.mycluster.local`), the injected address may not resolve correctly.
A future enhancement may support configurable cluster domains or use the shorter
`<pod>.<svc>.<namespace>` format which is domain-agnostic. For now, users on non-default cluster
domains should consider this limitation or use the shorter address format via init containers.

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

**`Index` field semantics for different scopes:**

- For `RoleScoped` ports: The `Index` field in `ComponentPortRef` is **ignored** because
  `RoleScoped` ports have the same value for all pods in the component. The key does not include
  pod-specific identifiers, so specifying an index is unnecessary.
- For `PodScoped` ports: The `Index` field is used to construct the pod name portion of the key.
  It must be within bounds `[0, size-1]` of the referenced component.

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
   b. Inject env var {Name: ref.Env, Value: <fqdn>} to all containers
3. For each PortRef:
   a. Look up resolved port value from RoleInstance annotation
      (using same key format as port-allocator)
   b. Inject env var {Name: ref.Env, Value: <port>} to all containers
4. Remove component-discovery annotation from the generated Pod
   (it is a controller directive, not a pod-level annotation)
```

**Environment variable collision behavior:**

If an injected environment variable name already exists in a container's spec:
- The injected value **overrides** the existing value. This follows the Kubernetes pattern where
  controller-level injection takes precedence over user-declared values for orchestration purposes.
- Users should avoid using reserved variable names (those that may be injected by RBG) in their
  container specs, or explicitly document that they are intentionally overriding controller values.
- The admission webhook does **not** reject collisions — this allows users to override defaults
  while still getting the benefits of automatic injection for other components.

**Validation (admission webhook):**

- All `component` names in `ComponentAddressRef` and `ComponentPortRef` must reference a component
  name that exists in the same `CustomComponentsPattern.components[]` array.
- A component cannot reference itself (self-reference is a no-op and is rejected to avoid
  confusion). **Note on intra-component discovery**: For components with `size > 1` (e.g., 4 workers),
  pods within the same component may need to discover each other's addresses (e.g., ring allreduce,
  mesh topologies). This use case is intentionally **out of scope** for this KEP because:
  - Intra-component pod discovery requires a different addressing scheme (all pods share the same
    component name but different indices).
  - Users can achieve this via Kubernetes Downward API for self-index, combined with the FQDN
    formula documented above, or by using a dedicated service mesh.
  - A future enhancement may introduce a `ComponentAddressRef.scope: IntraComponent` option to
    explicitly support this pattern.
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
- Validation: out-of-bounds `index` rejected
- Validation: portName not defined in port-allocator annotation rejected
- `ComponentDependsOnConfig` JSON parsing: `startAfter` / `deleteAfter` arrays parsed correctly; absent annotation yields nil entries
- `allNamedComponentsReady`: returns true iff all named components have `ReadyReplicas >= Size && Size > 0` in `componentStatuses`; returns false for missing component, `Size == 0`, or `ReadyReplicas < Size`
- `DetectCycle`: correctly identifies cyclic and acyclic dependency graphs
- `BuildDeletionGates`: correctly derives deletion constraints from reverse-startAfter + explicit deleteAfter (union, deduplicated)

#### Integration Tests

- Component `startAfter` gate: pods are not created for a component until all listed dependencies have `ReadyReplicas >= Size` in `componentStatuses`
- Component deletion gate: pods are not deleted for a component until all listed `deleteAfter` (and reverse-startAfter) components have no excess pods remaining
- When no `component-depends-on` annotation is present, all component pods are created/deleted concurrently (default behavior unchanged)
- Cycle detection: controller falls back to parallel mode and logs error when a cycle is detected
- Address injection: FQDN env var is correctly set in target pods
- Port injection: port value env var matches the value in RoleInstance annotation

#### End to End Tests

- Deploy RBG with a 3-component role (`leader`/`worker`/`router`), verify:
  - `router` pod creation timestamp is strictly after `leader` and `worker` pods
  - `LEADER_ADDR` env var in `worker` pods resolves to the expected FQDN
  - `LEADER_GRPC_PORT` env var in `worker` pods matches the port-allocator allocated value
  - RBG deletion completes cleanly: `router` is removed first, then `leader`/`worker`
  - `RoleInstance.Status.ComponentStatuses` shows `ReadyReplicas == Size` for all components when RBG is Ready

## Alternatives

### Alternative 1: Structured `dependsOn` API Field in InstanceComponent

Add a structured `dependsOn` field to `InstanceComponent` to move the lifecycle ordering
configuration into the CRD schema. This is more discoverable and validatable but requires a CRD
schema change and graduation process. The annotation-based approach was chosen to allow fast
iteration; a structured field can be introduced in a follow-up KEP if operational evidence shows
annotations are insufficient.

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

# Port Allocation and Service Discovery

## Overview

In distributed inference services, multiple roles and components need to communicate with each other. RBG provides a three-layer service discovery mechanism, covering different scenarios from simple to complex:

- **Layer 1: Headless Service + DNS**
  - Automatically creates Headless Services
  - Each Pod has a stable DNS name
  - Use case: Fixed ports, known topology
- **Layer 2: Environment Variables + ConfigMap**
  - Controller automatically injects environment variables (role name, index, leader address, etc.)
  - Automatically generates a global ConfigMap (address and port topology for all roles)
  - ConfigMap mounted at `/etc/rbg/config.yaml`
  - Use case: Runtime cluster topology retrieval
- **Layer 3: Port Allocation + Component Discovery**
  - Dynamic port allocation (avoids port conflicts in hostNetwork scenarios)
  - Component discovery annotation (component-discovery) to obtain addresses and ports of other components
  - Use case: CustomComponentsPattern + hostNetwork + RDMA scenarios

## Prerequisites

+ Kubernetes cluster version >= 1.24
+ RBG Controller installed (see [Installation Guide](https://github.com/sgl-project/rbg))
+ Port allocation feature requires enabling `--enable-port-allocator` (disabled by default)

---

## Layer 1: Headless Service and DNS Service Discovery

### Automatically Created Headless Service

RBG Controller automatically creates a Headless Service (`ClusterIP: None`) for each role, with the naming convention:

```plain
s-{rbgName}-{roleName}
```

For example, if the RBG is named `pd-inference` and the role is named `prefill`, the automatically created Service is named `s-pd-inference-prefill`.

Headless Service configuration:

| Property | Value | Description |
| --- | --- | --- |
| `clusterIP` | `None` | Headless, no virtual IP assigned |
| `publishNotReadyAddresses` | `true` | Publishes DNS records even when Pods are not ready |
| `selector` | `rbg.workloads.x-k8s.io/group-name: <rbgName>, rbg.workloads.x-k8s.io/role-name: <roleName>` | Automatically matches all Pods of the role |


### Pod DNS Naming Convention

Each Pod receives a stable DNS name through the Headless Service:

```plain
{rbgName}-{roleName}-{index}.{serviceName}.{namespace}.svc.cluster.local
```

Examples:

```plain
# The 0th Pod of the Prefill role
pd-inference-prefill-0.s-pd-inference-prefill.default.svc.cluster.local

# The 2nd Pod of the Decode role
pd-inference-decode-2.s-pd-inference-decode.default.svc.cluster.local
```

### Usage

Pods can directly access Pods of other roles via DNS names, without manually configuring Services or IP addresses:

```yaml
# Access the 0th Prefill instance from a Decode Pod
containers:
  - name: engine
    env:
      - name: PREFILL_ADDR
        value: "pd-inference-prefill-0.s-pd-inference-prefill.default.svc.cluster.local:8000"
```

> **Note**: The `{index}` in the DNS name is the Pod's ordinal index (starting from 0). In StatefulSet and RoleInstanceSet modes, Pod names are stable, and DNS addresses remain unchanged after Pod recreation.
>

---

## Layer 2: Environment Variables and ConfigMap

### Automatically Injected Environment Variables

Controller automatically injects the following environment variables into each Pod, allowing inference engines to obtain their own role information and cluster topology at runtime:

#### Basic Variables (All Roles)
| Variable | Value | Description |
| --- | --- | --- |
| `RBG_GROUP_NAME` | RBG name | Name of the owning RoleBasedGroup |
| `RBG_ROLE_NAME` | Role name | Name of the role the current Pod belongs to |


#### Stateful Roles (StatefulSet / LeaderWorkerSet / RoleInstanceSet)
| Variable | Value | Source |
| --- | --- | --- |
| `RBG_ROLE_INDEX` | Pod ordinal index | Downward API (StatefulSet: `pod-index` label, RoleInstanceSet: `role-instance-index` label) |


#### RoleInstanceSet-Specific Variables
| Variable | Value | Source |
| --- | --- | --- |
| `RBG_ROLE_INSTANCE_NAME` | RoleInstance name | Downward API (`role-instance-name` label) |
| `RBG_COMPONENT_NAME` | Component name | Downward API (`component-name` label) |
| `RBG_COMPONENT_INDEX` | Component index | Downward API (`component-id` label) |


#### LeaderWorkerPattern-Specific Variables
| Variable | Value | Description |
| --- | --- | --- |
| `RBG_LWP_LEADER_ADDRESS` | Leader Pod FQDN | Computed value: `$(RBG_ROLE_INSTANCE_NAME)-0.{svcName}.{namespace}` |
| `RBG_LWP_WORKER_INDEX` | Current Worker index | Downward API (`component-index` label) |
| `RBG_LWP_GROUP_SIZE` | Total Pods in group | Downward API (`component-size` label) |


> **Note**: Size-related environment variables (such as `RBG_LWP_GROUP_SIZE`) change during scaling, but Controller intentionally does not inject these variables in non-LWP scenarios to avoid triggering Pod recreation when replica counts change.
>

### ConfigMap Cluster Topology

Controller automatically creates an RBG-level ConfigMap (with the same name as the RBG) containing address and port information for all roles. The ConfigMap is automatically mounted into every Stateful role's Pods.

#### ConfigMap Structure
```yaml
# ConfigMap: pd-inference (key: config.yaml)
group:
  name: pd-inference
  size: 2
  roles:
    - prefill
    - decode
roles:
  prefill:
    size: 2
    instances:
      - address: pd-inference-prefill-0.s-pd-inference-prefill
        ports:
          http: 8000
      - address: pd-inference-prefill-1.s-pd-inference-prefill
        ports:
          http: 8000
  decode:
    size: 4
    instances:
      - address: pd-inference-decode-0.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-1.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-2.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-3.s-pd-inference-decode
        ports:
          http: 8000
```

#### ConfigMap Mount Configuration
| Property | Value | Description |
| --- | --- | --- |
| Volume name | `rbg-cluster-config` | Automatically injected Volume |
| Mount path | `/etc/rbg` | Automatically mounted in all containers |
| File name | `config.yaml` | ConfigMap key |
| Permissions | Read-only | Pods cannot modify |


Inference engines can directly read `/etc/rbg/config.yaml` to obtain the entire cluster's topology information:

```python
# Inference engine code example: reading cluster topology
import yaml

with open("/etc/rbg/config.yaml") as f:
    config = yaml.safe_load(f)

# Get all Prefill instance addresses
prefill_instances = config["roles"]["prefill"]["instances"]
for inst in prefill_instances:
    print(f"Prefill: {inst['address']}:{inst['ports']['http']}")

# Get all Decode instance addresses
decode_instances = config["roles"]["decode"]["instances"]
for inst in decode_instances:
    print(f"Decode: {inst['address']}:{inst['ports']['http']}")
```

#### Port Information Source

Port information in the ConfigMap comes from the `servicePorts` definition in the role Spec:

```yaml
spec:
  roles:
    - name: prefill
      replicas: 2
      servicePorts:
        - name: http
          port: 8000
          protocol: TCP
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                ports:
                  - containerPort: 8000
```

Port names are converted to lowercase with `-` replaced by `_` (e.g., `http-api` → `http_api`). Unnamed ports use the `port{number}` format (e.g., `port8000`).

> **Note**: The ConfigMap is automatically maintained by the Controller. When role replica counts change (scaling) or ServicePorts are modified, the ConfigMap updates automatically. Since the ConfigMap is mounted as a Volume, files inside Pods are automatically synced by kubelet without requiring Pod restart.
>

---

## Layer 3: Port Allocation and Component Discovery

### Background: Why Dynamic Port Allocation Is Needed

In RDMA inference scenarios using `hostNetwork: true`, multiple Pods on the same node share the host's network namespace. If two Pods' containers listen on the same port, a port conflict occurs.

```plain
┌──────────────────────────────────────────────────────────────────┐
│  hostNetwork Port Conflict Problem                                │
│                                                                  │
│  Node A                                                          │
│  ┌──────────────────────────────────────────────────────┐        │
│  │  Prefill Pod-0        Prefill Pod-1                  │        │
│  │  containerPort: 8000   containerPort: 8000  ← Conflict! │     │
│  │                                                      │        │
│  │  Shared host network namespace → cannot bind same port│        │
│  └──────────────────────────────────────────────────────┘        │
│                                                                  │
│  Solution: RBG dynamically allocates different ports per Pod      │
│  ┌──────────────────────────────────────────────────────┐        │
│  │  Prefill Pod-0        Prefill Pod-1                  │        │
│  │  Allocated: 30001      Allocated: 30002              │        │
│  │  Injected env: PORT=30001  PORT=30002                │        │
│  └──────────────────────────────────────────────────────┘        │
└──────────────────────────────────────────────────────────────────┘
```

### Enabling the Port Allocator

The port allocation feature is disabled by default and must be enabled via Controller startup parameters:

```bash
# Controller startup parameters
--enable-port-allocator=true     # Enable port allocation
--port-allocate-strategy=random  # Allocation strategy (currently only random is supported)
--start-port=30000               # Port range start value
--port-range=5000                # Port range size (30000~34999)
```

Helm deployment configuration:

```yaml
# values.yaml
controller:
  features:
    portAllocator:
      enabled: true
      strategy: random
      startPort: 30000
      portRange: 5000
```

### Port Scopes

Port allocation supports two scopes, controlled by the `scope` parameter:

| Scope | Description | Use Case |
| --- | --- | --- |
| `PodScoped` | Each Pod receives an independent port value | Multiple Pods of the same component need different ports (hostNetwork scenarios) |
| `RoleScoped` | All Pods within a role share the same port value | Role exposes a unified port externally (inter-component communication) |


### Configuring Port Allocation

Port allocation is configured via the component-level annotation `rolebasedgroup.workloads.x-k8s.io/port-allocator`, with a JSON-formatted value:

```yaml
customComponentsPattern:
  components:
    - name: worker
      size: 2
      annotations:
        rolebasedgroup.workloads.x-k8s.io/port-allocator: |
          {
            "allocations": [
              {
                "name": "worker-grpc",
                "env": "WORKER_GRPC_PORT",
                "scope": "PodScoped"
              }
            ]
          }
```

#### allocations Parameter Reference
| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | Yes | Logical port name, used for reference by other components |
| `env` | string | Yes | Environment variable name injected into containers |
| `scope` | string | No | Scope: `PodScoped` (default) or `RoleScoped` |
| `annotationKey` | string | No | Additionally inject into Pod annotation with this key (optional) |


### Referencing Ports from Other Components

Via the `references` field, a component can obtain allocated ports from other components within the same role:

```yaml
- name: worker
  size: 2
  annotations:
    rolebasedgroup.workloads.x-k8s.io/port-allocator: |
      {
        "allocations": [
          {
            "name": "worker-grpc",
            "env": "WORKER_GRPC_PORT",
            "scope": "PodScoped"
          }
        ],
        "references": [
          {
            "env": "LEADER_GRPC_PORT",
            "from": "prefill.leader.leader-grpc"
          }
        ]
      }
```

#### references Parameter Reference
| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| `env` | string | Yes | Environment variable name injected into containers |
| `from` | string | Yes | Reference format: `{roleName}.{componentName}.{portName}` |


### Port Allocation Storage and Propagation

Port values propagate through annotations across resource levels, without relying on ConfigMaps or additional CRDs:

```plain
RoleInstanceSet (RoleScoped port allocation)
    │  Annotation: <component>.<portName> = "30001"
    │
    ▼
RoleInstance (PodScoped port allocation + RoleScoped copy)
    │  Annotation: <component>.<portName> = "30001"  (RoleScoped copy)
    │  Annotation: <podName>.<portName>   = "30002"  (PodScoped new allocation)
    │
    ▼
Pod (environment variable injection + Pod annotation injection)
    Env: LEADER_GRPC_PORT=30001
    Env: WORKER_GRPC_PORT=30002
    Pod annotation: (if annotationKey is configured)
```

---

## Component Discovery

For scenarios in `CustomComponentsPattern` where discovering other components' **addresses and ports** is needed, RBG provides the `component-discovery` annotation. Unlike port allocation's `references`, component discovery can obtain both the target component's FQDN address and dynamically allocated port simultaneously.

### Configuring Component Discovery

Component discovery is configured via the annotation `rolebasedgroup.workloads.x-k8s.io/component-discovery`:

```yaml
- name: router
  size: 1
  annotations:
    rolebasedgroup.workloads.x-k8s.io/component-discovery: |
      {
        "addressRefs": [
          {
            "env": "LEADER_ADDR",
            "component": "leader",
            "index": 0
          },
          {
            "env": "WORKER_0_ADDR",
            "component": "worker",
            "index": 0
          }
        ],
        "portRefs": [
          {
            "env": "LEADER_GRPC_PORT",
            "component": "leader",
            "portName": "leader-grpc"
          },
          {
            "env": "WORKER_0_GRPC_PORT",
            "component": "worker",
            "portName": "worker-grpc",
            "index": 0
          }
        ]
      }
```

#### addressRefs Parameter Reference
| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| `env` | string | Yes | Environment variable name injected into containers |
| `component` | string | Yes | Target component name |
| `index` | int | No | Pod index within the target component (default 0) |


Injected value is the full FQDN: `{podName}.{svcName}.{namespace}.svc.cluster.local`

#### portRefs Parameter Reference
| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| `env` | string | Yes | Environment variable name injected into containers |
| `component` | string | Yes | Target component name |
| `portName` | string | Yes | Logical port name (corresponds to the `name` field in port allocation) |
| `index` | int | No | Pod index within the target component (only needed for PodScoped ports) |


Port resolution first looks up the PodScoped value (`<podName>.<portName>`), then falls back to the RoleScoped value (`<componentName>.<portName>`).

---

## Complete Example: Three-Component Inference Service

The following example demonstrates an inference service with leader, worker, and router components, combining port allocation and component discovery:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-server
spec:
  roles:
    - name: prefill
      replicas: 1
      customComponentsPattern:
        components:

          # ── leader: 1 Pod, allocates RoleScoped port ──
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
                      "scope": "RoleScoped"
                    }
                  ]
                }
            template:
              spec:
                hostNetwork: true
                containers:
                  - name: leader
                    image: inference-engine:v1.0
                    command: ["./start-leader", "--port", "$(LEADER_GRPC_PORT)"]

          # ── worker: 2 Pods, allocates PodScoped ports + discovers leader ──
          - name: worker
            size: 2
            annotations:
              rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
                {"deleteAfter": ["router"]}
              rolebasedgroup.workloads.x-k8s.io/port-allocator: |
                {
                  "allocations": [
                    {
                      "name": "worker-grpc",
                      "env": "WORKER_GRPC_PORT",
                      "scope": "PodScoped"
                    }
                  ]
                }
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
                hostNetwork: true
                containers:
                  - name: worker
                    image: inference-engine:v1.0
                    command:
                      - "./start-worker"
                      - "--port"
                      - "$(WORKER_GRPC_PORT)"
                      - "--leader"
                      - "$(LEADER_ADDR):$(LEADER_GRPC_PORT)"

          # ── router: 1 Pod, discovers leader and all workers ──
          - name: router
            size: 1
            annotations:
              rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
                {"startAfter": ["leader", "worker"]}
              rolebasedgroup.workloads.x-k8s.io/component-discovery: |
                {
                  "portRefs": [
                    {
                      "env": "LEADER_GRPC_PORT",
                      "component": "leader",
                      "portName": "leader-grpc"
                    },
                    {
                      "env": "WORKER_0_GRPC_PORT",
                      "component": "worker",
                      "portName": "worker-grpc",
                      "index": 0
                    },
                    {
                      "env": "WORKER_1_GRPC_PORT",
                      "component": "worker",
                      "portName": "worker-grpc",
                      "index": 1
                    }
                  ],
                  "addressRefs": [
                    {
                      "env": "LEADER_ADDR",
                      "component": "leader",
                      "index": 0
                    },
                    {
                      "env": "WORKER_0_ADDR",
                      "component": "worker",
                      "index": 0
                    },
                    {
                      "env": "WORKER_1_ADDR",
                      "component": "worker",
                      "index": 1
                    }
                  ]
                }
            template:
              spec:
                hostNetwork: true
                containers:
                  - name: router
                    image: inference-engine:v1.0
                    command:
                      - "./start-router"
                      - "--leader"
                      - "$(LEADER_ADDR):$(LEADER_GRPC_PORT)"
                      - "--workers"
                      - "$(WORKER_0_ADDR):$(WORKER_0_GRPC_PORT),$(WORKER_1_ADDR):$(WORKER_1_GRPC_PORT)"
```

### Injected Environment Variables

After deployment, the environment variables actually injected into each component's Pods:

**leader Pod:**

| Environment Variable | Example Value | Description |
| --- | --- | --- |
| `LEADER_GRPC_PORT` | `30142` | RoleScoped port allocated by the port allocator |
| `RBG_GROUP_NAME` | `pd-server` | RBG name |
| `RBG_ROLE_NAME` | `prefill` | Role name |


**worker-0 Pod:**

| Environment Variable | Example Value | Description |
| --- | --- | --- |
| `WORKER_GRPC_PORT` | `31205` | PodScoped port (different for each worker) |
| `LEADER_GRPC_PORT` | `30142` | Referenced from leader's RoleScoped port |
| `LEADER_ADDR` | `pd-server-prefill-0-...s-pd-server-prefill.default.svc.cluster.local` | Leader's FQDN address |


**worker-1 Pod:**

| Environment Variable | Example Value | Description |
| --- | --- | --- |
| `WORKER_GRPC_PORT` | `32718` | PodScoped port different from worker-0 |
| `LEADER_GRPC_PORT` | `30142` | Same as worker-0 (RoleScoped) |
| `LEADER_ADDR` | `pd-server-prefill-0-...s-pd-server-prefill.default.svc.cluster.local` | Same as worker-0 |


**router Pod:**

| Environment Variable | Example Value | Description |
| --- | --- | --- |
| `LEADER_GRPC_PORT` | `30142` | Referenced from leader |
| `LEADER_ADDR` | `pd-server-prefill-0-...` | Leader address |
| `WORKER_0_GRPC_PORT` | `31205` | worker-0's port (specified via index: 0) |
| `WORKER_0_ADDR` | `pd-server-prefill-0-...` | worker-0's address |
| `WORKER_1_GRPC_PORT` | `32718` | worker-1's port (specified via index: 1) |
| `WORKER_1_ADDR` | `pd-server-prefill-0-...` | worker-1's address |


---

## Scenario Selection Guide
| Scenario | Recommended Approach | Description |
| --- | --- | --- |
| **Co-located deployment, fixed ports** | Headless Service + DNS + ConfigMap | All Pods use the same port; obtain addresses via DNS or ConfigMap |
| **PD disaggregation, fixed ports** | Headless Service + DNS + ConfigMap | Prefill and Decode each use fixed ports; ConfigMap automatically maintains full cluster topology |
| **hostNetwork + RDMA** | Port Allocation + Component Discovery | Multiple Pods on the same node need different ports; dynamically allocated via port allocator |
| **CustomComponentsPattern multi-component communication** | Port Allocation + Component Discovery + Dependency Management | Components need to discover each other's addresses and dynamic ports |


---

## Verification

```bash
# View automatically created Headless Services
kubectl get svc -l rbg.workloads.x-k8s.io/group-name=<rbg-name>

# Verify DNS resolution (from any Pod in the cluster)
kubectl exec -it <any-pod> -- nslookup <rbg-name>-<role-name>-0.s-<rbg-name>-<role-name>.<namespace>.svc.cluster.local

# View the automatically created ConfigMap
kubectl get configmap <rbg-name> -o yaml

# View the ConfigMap content mounted in a Pod
kubectl exec -it <pod-name> -- cat /etc/rbg/config.yaml

# View environment variables injected into a Pod
kubectl exec -it <pod-name> -- env | grep RBG_

# View port allocation results (PodScoped scenario)
kubectl exec -it <pod-name> -- env | grep -E 'LEADER_GRPC_PORT|WORKER_GRPC_PORT'

# View port values in RoleInstance annotations
kubectl get roleinstance <name> -o jsonpath='{.metadata.annotations}'
```

## Related Documents

+ [Deploying Inference Services with RBG](./01-deploy-inference-service.md)
+ [Configuring Rolling Update Strategies](./03-configuring-rolling-updates.md)
+ In-Place Update and In-Place Scheduling

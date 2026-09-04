# Operations Guide: Port Allocation and Service Discovery

> Corresponding concept document: [5. Port Allocation and Service Discovery](05-port-allocation-and-service-discovery.md)

## Objectives

Validate RBG's three-layer service discovery mechanism, including:

1. Headless Service + DNS: Pods access each other via stable DNS names
2. Environment Variables + ConfigMap: Pods obtain role information and cluster topology at runtime
3. Port Allocation + Component Discovery: Dynamically allocate ports and discover addresses and ports of other components in `CustomComponentsPattern` scenarios

## Prerequisites

- Kubernetes cluster version >= 1.24
- RBG Controller installed
- Images accessible: `alpine:3.23.5`
- Operations 3 and 4 require the Controller to be started with `--enable-port-allocator=true` (disabled by default); confirm or redeploy the Controller in advance

> **Note**: This document uses `sleep 3600` as a placeholder command, focusing on validating RBG service discovery control plane behavior without requiring GPU. To test real inference functionality, replace with the full inference engine startup command.

---

## Operation 1: Headless Service and DNS Service Discovery

### Step 1: Create a Two-Role RBG

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
  namespace: default
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
                image: alpine:3.23.5
                imagePullPolicy: IfNotPresent
                command: ["sleep", "3600"]
    - name: decode
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
                image: alpine:3.23.5
                imagePullPolicy: IfNotPresent
                command: ["sleep", "3600"]
EOF
```

### Expected Behavior

- Controller automatically creates two Headless Services: `s-pd-inference-prefill` and `s-pd-inference-decode`
- Each Pod receives a stable DNS name: `{rbgName}-{roleName}-{index}.{svcName}.{namespace}.svc.cluster.local`

### Verification

```bash
# View the automatically created Headless Services
kubectl get svc -n default -l rbg.workloads.x-k8s.io/group-name=pd-inference

> NAME                       TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)   AGE
> s-pd-inference-decode      ClusterIP   None         <none>        <none>    7s
> s-pd-inference-prefill     ClusterIP   None         <none>        <none>    7s
```

```bash
# View the created Pods
kubectl get po -n default -l rbg.workloads.x-k8s.io/group-name=pd-inference -o wide

> NAME                     READY   STATUS    RESTARTS   AGE   IP            NODE                 NOMINATED NODE   READINESS GATES
> pd-inference-decode-0    1/1     Running   0          32s   10.xx.xx.11   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-inference-decode-1    1/1     Running   0          32s   10.xx.xx.12   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-inference-prefill-0   1/1     Running   0          32s   10.xx.xx.13   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-inference-prefill-1   1/1     Running   0          32s   10.xx.xx.14   e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# Resolve the DNS name of prefill-0 from within decode-0
kubectl exec -n default pd-inference-decode-0 -- \
  getent hosts pd-inference-prefill-0.s-pd-inference-prefill.default.svc.cluster.local

> 10.xx.xx.13    pd-inference-prefill-0.s-pd-inference-prefill.default.svc.cluster.local
```

**Expected output:**
- Both Headless Services have `CLUSTER-IP` set to `None`
- DNS names resolve correctly to the corresponding Pod IPs

### Cleanup

```bash
kubectl delete rbg -n default pd-inference
```

---

## Operation 2: Environment Variables and ConfigMap Cluster Topology

### Step 1: Reuse the RBG from Operation 1 (or recreate it)

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
  namespace: default
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
                image: alpine:3.23.5
                imagePullPolicy: IfNotPresent
                command: ["sleep", "3600"]
    - name: decode
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
                image: alpine:3.23.5
                imagePullPolicy: IfNotPresent
                command: ["sleep", "3600"]
EOF
```

### Expected Behavior (Environment Variables and ConfigMap)

- Each Pod automatically receives injected environment variables such as `RBG_GROUP_NAME` and `RBG_ROLE_NAME`
- Controller automatically creates a ConfigMap with the same name as the RBG, mounted at `/etc/rbg/config.yaml`

### Verification (Environment Variable Injection)

```bash
# View the injected environment variables in a Pod
kubectl exec -n default pd-inference-prefill-0 -- env | grep RBG_

> RBG_GROUP_NAME=pd-inference
> RBG_ROLE_NAME=prefill
> ...
```

```bash
# View the automatically created ConfigMap
kubectl get cm -n default pd-inference -o yaml
```

```bash
# View the ConfigMap content mounted in the Pod
kubectl exec -n default pd-inference-prefill-0 -- cat /etc/rbg/config.yaml

> group:
>   name: pd-inference
>   size: 2
>   roles:
>     - prefill
>     - decode
> roles:
>   prefill:
>     size: 2
>     instances:
>       - address: pd-inference-prefill-0.s-pd-inference-prefill
>         ports:
>           http: 8000
>       - address: pd-inference-prefill-1.s-pd-inference-prefill
>         ports:
>           http: 8000
>   decode:
>     size: 2
>     instances:
>       - address: pd-inference-decode-0.s-pd-inference-decode
>         ports:
>           http: 8000
>       - address: pd-inference-decode-1.s-pd-inference-decode
>         ports:
>           http: 8000
```

**Expected output:**
- Environment variables correctly reflect the RBG and role the Pod belongs to
- ConfigMap contains the complete address and port topology for both roles

### Step 2: Scale Out the decode Role and Observe ConfigMap Auto-Update

```bash
kubectl patch rbg -n default pd-inference --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/1/replicas", "value": 3}]'
```

### Verification (ConfigMap Scale-Out Update)

```bash
# Confirm that decode's size and instances in the ConfigMap have been updated to 3, without restarting Pods
# Since the mounted ConfigMap has a propagation delay inside the container, changes may not be visible immediately
kubectl exec -n default pd-inference-prefill-0 -- cat /etc/rbg/config.yaml | grep -A11 "decode:"

> decode:
>   instances:
>   - address: pd-inference-decode-0.s-pd-inference-decode
>     ports:
>       http: 8000
>   - address: pd-inference-decode-1.s-pd-inference-decode
>     ports:
>       http: 8000
>   - address: pd-inference-decode-2.s-pd-inference-decode
>     ports:
>       http: 8000
>   size: 3
```

**Expected output:** ConfigMap content updates automatically upon scale-out; existing Pods do not require restart

### Cleanup (Environment Variables and ConfigMap)

```bash
kubectl delete rbg -n default pd-inference
```

---

## Operation 3: Port Allocation (PodScoped + RoleScoped)

### Step 1: Create an RBG with Port Allocation Annotations

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: port-allocation-demo
  namespace: default
spec:
  roles:
    - name: prefill
      replicas: 1
      customComponentsPattern:
        components:
          - name: leader
            size: 1
            annotations:
              rolebasedgroup.workloads.x-k8s.io/port-allocator: |
                {
                  "allocations": [
                    {
                      "name": "leader-grpc",
                      "env": "LEADER_GRPC_PORT",
                      "scope": "PodScoped"
                    }
                  ]
                }
            template:
              spec:
                containers:
                  - name: leader
                    image: alpine:3.23.5
                    imagePullPolicy: IfNotPresent
                    command: ["sleep", "3600"]
          - name: worker
            size: 2
            annotations:
              rolebasedgroup.workloads.x-k8s.io/port-allocator: |
                {
                  "allocations": [
                    {
                      "name": "worker-grpc",
                      "env": "WORKER_GRPC_PORT",
                      "scope": "RoleScoped"
                    }
                  ],
                  "references": [
                    {
                      "env": "LEADER_GRPC_PORT",
                      "from": "prefill.leader.leader-grpc"
                    }
                  ]
                }
            template:
              spec:
                containers:
                  - name: worker
                    image: alpine:3.23.5
                    imagePullPolicy: IfNotPresent
                    command: ["sleep", "3600"]
EOF
```

### Expected Behavior (Port Allocation)

- The `leader` component uses `PodScoped`: each leader instance receives a different `LEADER_GRPC_PORT` (this example has only 1 instance)
- The `worker` component uses `RoleScoped`: all worker instances share the same `WORKER_GRPC_PORT`
- `worker` references the `leader`'s `PodScoped` port via `references` (always references the first instance of the target component, i.e., `leader-0`)

### Verification (Port Allocation)

```bash
# View leader's port allocation (PodScoped)
kubectl exec -n default port-allocation-demo-prefill-0-leader-0 -- env | grep LEADER_GRPC_PORT

> LEADER_GRPC_PORT=32423
```

```bash
# View both workers' port allocation (RoleScoped, all workers share the same WORKER_GRPC_PORT)
kubectl exec -n default port-allocation-demo-prefill-0-worker-0 -- env | grep -E 'WORKER_GRPC_PORT|LEADER_GRPC_PORT'

> WORKER_GRPC_PORT=31775
> LEADER_GRPC_PORT=32423

kubectl exec -n default port-allocation-demo-prefill-0-worker-1 -- env | grep -E 'WORKER_GRPC_PORT|LEADER_GRPC_PORT'

> WORKER_GRPC_PORT=31775
> LEADER_GRPC_PORT=32423
```

**Expected output:**
- Both workers have the same `WORKER_GRPC_PORT` (RoleScoped shares a single value)
- Both workers have the same `LEADER_GRPC_PORT`, matching the port allocated to `leader-0` itself (references the first instance of the target component)

### Cleanup (Port Allocation)

```bash
kubectl delete rbg -n default port-allocation-demo
```

---

## Operation 4: Component Discovery

### Step 1: Add a router Component on Top of Operation 3 to Discover leader and worker Addresses and Ports

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: component-discovery-demo
  namespace: default
spec:
  roles:
    - name: prefill
      replicas: 1
      customComponentsPattern:
        components:
          - name: leader
            size: 1
            annotations:
              rolebasedgroup.workloads.x-k8s.io/port-allocator: |
                {
                  "allocations": [
                    {
                      "name": "leader-grpc",
                      "env": "LEADER_GRPC_PORT",
                      "scope": "PodScoped"
                    }
                  ]
                }
            template:
              spec:
                containers:
                  - name: leader
                    image: alpine:3.23.5
                    imagePullPolicy: IfNotPresent
                    command: ["sleep", "3600"]
          - name: worker
            size: 2
            annotations:
              rolebasedgroup.workloads.x-k8s.io/port-allocator: |
                {
                  "allocations": [
                    {
                      "name": "worker-grpc",
                      "env": "WORKER_GRPC_PORT",
                      "scope": "RoleScoped"
                    }
                  ]
                }
            template:
              spec:
                containers:
                  - name: worker
                    image: alpine:3.23.5
                    imagePullPolicy: IfNotPresent
                    command: ["sleep", "3600"]
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
                    },
                    {
                      "env": "WORKER_1_ADDR",
                      "component": "worker",
                      "index": 1
                    }
                  ],
                  "portRefs": [
                    {
                      "env": "LEADER_GRPC_PORT",
                      "component": "leader",
                      "portName": "leader-grpc",
                      "index": 0
                    },
                    {
                      "env": "WORKER_GRPC_PORT",
                      "component": "worker",
                      "portName": "worker-grpc"
                    }
                  ]
                }
            template:
              spec:
                containers:
                  - name: router
                    image: alpine:3.23.5
                    imagePullPolicy: IfNotPresent
                    command: ["sleep", "3600"]
EOF
```

### Expected Behavior (Component Discovery)

- The `router` component does not perform port allocation; it only discovers the addresses and ports of `leader` and `worker` via the `component-discovery` annotation
- Addresses are injected as full FQDNs; ports are injected as the allocated port values of the corresponding components

### Verification (Component Discovery)

```bash
# View the injected address and port environment variables in router
kubectl exec -n default component-discovery-demo-prefill-0-router-0 -- env | grep -E 'LEADER_|WORKER_'

> LEADER_GRPC_PORT=32562
> LEADER_ADDR=component-discovery-demo-prefill-0-leader-0.s-component-discovery-demo-prefill.default.svc.cluster.local
> WORKER_GRPC_PORT=33062
> WORKER_0_ADDR=component-discovery-demo-prefill-0-worker-0.s-component-discovery-demo-prefill.default.svc.cluster.local
> WORKER_1_ADDR=component-discovery-demo-prefill-0-worker-1.s-component-discovery-demo-prefill.default.svc.cluster.local
```

**Expected output:**
- `router` can obtain the FQDN addresses of `leader` and both `worker` instances
- The `LEADER_GRPC_PORT` obtained by `router` matches the port allocated to `leader-0` itself
- The `WORKER_GRPC_PORT` obtained by `router` matches the RoleScoped shared port of the `worker` component (both workers have the same port value)

### Cleanup (Component Discovery)

```bash
kubectl delete rbg -n default component-discovery-demo
```

---

## Summary

| Operation | Validation Point | Key Expectation |
| --- | --- | --- |
| Headless Service + DNS | Service CLUSTER-IP / DNS resolution | Each Pod has a stable DNS name; Headless Service has no virtual IP |
| Environment Variables + ConfigMap | RBG_* environment variables / ConfigMap content | Topology information auto-injected; ConfigMap auto-updates after scaling without Pod restart |
| Port Allocation | PodScoped / RoleScoped port values | Ports correctly allocated by scope within a role; cross-component references via `references` |
| Component Discovery | addressRefs / portRefs injection results | Target component FQDN addresses and ports correctly discovered and injected |

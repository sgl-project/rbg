# Operations Guide: Configuring Coordinated Policy

> Corresponding concept document: [7. Configuring CoordinatedPolicy](07-configuring-coordinated-policy.md)

## Objectives

Validate RBG's `CoordinatedPolicy` multi-role coordination capabilities, including:

1. Coordinated scaling: During initial deployment, create Pods across multiple roles at similar progress rates
2. Coordinated upgrade: When multiple roles update simultaneously, limit the update progress difference between roles

## Prerequisites

- Kubernetes cluster version >= 1.24
- RBG Controller installed
- Images accessible: `alpine:3.23.5`

> **Note**: This document uses `sleep 3600` as a placeholder command, focusing on validating RBG multi-role coordination control plane behavior without requiring GPU. To test real inference functionality, replace with the full inference engine startup command.

---

## Operation 1: Coordinated Scaling (Initial Deployment)

### Step 1: Create a CoordinatedPolicy and Multi-Role RBG

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: coordinated-scaling-demo
  namespace: default
spec:
  policies:
    - name: prefill-decode-scaling
      roles:
        - prefill
        - decode
      strategy:
        scaling:
          maxSkew: "50%"
          progression: OrderReady
---
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: coordinated-scaling-demo
  namespace: default
spec:
  roles:
    - name: prefill
      replicas: 4
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.5
                imagePullPolicy: IfNotPresent
                command: ["sleep", "3600"]
                startupProbe:
                  exec:
                    command:
                      - /bin/true
                  initialDelaySeconds: 10
                  periodSeconds: 1
                  failureThreshold: 3
    - name: decode
      replicas: 2
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

- The `CoordinatedPolicy` shares the same name and namespace (`default`) as the RBG, so the policy applies to the `coordinated-scaling-demo` RBG
- `prefill` and `decode` create Pods at similar progress rates — 1 decode and 2 prefill per batch; Prefill always becomes ready slightly later, and the next batch is created only after Prefill becomes ready
- `progression: OrderReady`: The next batch is created only after Pods become Ready
- Ultimately 4 prefill Pods and 2 decode Pods are created

### Verification

```bash
# Observe the Pod creation process
watch -n0.5 kubectl get pods -n default -l rbg.workloads.x-k8s.io/group-name=coordinated-scaling-demo

> NAME                                 READY   STATUS    RESTARTS   AGE
> coordinated-scaling-demo-decode-0    1/1     Running   0          19s
> coordinated-scaling-demo-decode-1    1/1     Running   0          7s
> coordinated-scaling-demo-prefill-0   1/1     Running   0          18s
> coordinated-scaling-demo-prefill-1   1/1     Running   0          18s
> coordinated-scaling-demo-prefill-2   0/1     Running   0          7s
> coordinated-scaling-demo-prefill-3   0/1     Running   0          7s
```

**Expected output:**

- Pods are not created all at once for a single role; instead, prefill and decode are created progressively at similar rates — 1 decode and 2 prefill per batch
- Ultimately there are 4 prefill Pods and 2 decode Pods

### Cleanup

```bash
kubectl delete cpolicy -n default coordinated-scaling-demo
kubectl delete rbg -n default coordinated-scaling-demo
```

---

## Operation 2: Coordinated Upgrade (rollingUpdate.maxSkew)

### Step 1: Create a CoordinatedPolicy and Multi-Role RBG (Coordinated Upgrade)

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: coordinated-rollout-demo
  namespace: default
spec:
  policies:
    - name: prefill-decode-rollout
      roles:
        - prefill
        - decode
      strategy:
        rollingUpdate:
          maxSkew: "25%"
          maxUnavailable: 1
---
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: coordinated-rollout-demo
  namespace: default
spec:
  roles:
    - name: prefill
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.5
                imagePullPolicy: IfNotPresent
                command: ["sleep", "3600"]
                startupProbe:
                  exec:
                    command:
                      - /bin/true
                  initialDelaySeconds: 8
                  periodSeconds: 1
                  failureThreshold: 3
    - name: decode
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
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

### Expected Behavior (Initial Deployment)

- 4 prefill Pods and 4 decode Pods are created
- Once the RBG status is Ready, you can trigger updates for both roles simultaneously
- `rollingUpdate.maxSkew: "25%"`: The update progress difference between the two roles is at most 25% (in this example, the maximum difference equals 1 instance)

### Step 2: Update Environment Variables for Both Roles Simultaneously

```bash
kubectl patch rbg -n default coordinated-rollout-demo --type='json' \
  -p='[{"op": "add", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/env", "value": [{"name": "new_env", "value": "test"}]},
       {"op": "add", "path": "/spec/roles/1/standalonePattern/template/spec/containers/0/env", "value": [{"name": "new_env", "value": "test"}]}]'
```

### Expected Behavior (Update Triggered)

- prefill and decode enter rolling update simultaneously
- If one role updates too fast, the controller waits for the other role to catch up
- During the update, the update progress difference between the two roles does not exceed 25% (in this example, the maximum difference equals 1 instance)

### Verification (Coordinated Upgrade)

```bash
# Observe the Pod recreation process
watch -n0.5 kubectl get pods -n default -l rbg.workloads.x-k8s.io/group-name=coordinated-rollout-demo

> NAME                                 READY   STATUS    RESTARTS   AGE
> coordinated-rollout-demo-decode-0    1/1     Running   0          11s
> coordinated-rollout-demo-decode-1    1/1     Running   0          51s
> coordinated-rollout-demo-decode-2    1/1     Running   0          90s
> coordinated-rollout-demo-decode-3    1/1     Running   0          2m10s
> coordinated-rollout-demo-prefill-0   0/1     Running   0          11s
> coordinated-rollout-demo-prefill-1   1/1     Running   0          51s
> coordinated-rollout-demo-prefill-2   1/1     Running   0          90s
> coordinated-rollout-demo-prefill-3   1/1     Running   0          2m10s
```

```bash
# After the update completes, confirm all Pods contain the new environment variable
kubectl get pods -n default -l rbg.workloads.x-k8s.io/group-name=coordinated-rollout-demo -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].env[?(@.name=="new_env")].value}{"\n"}{end}'

> coordinated-rollout-demo-decode-0=test
> coordinated-rollout-demo-decode-1=test
> coordinated-rollout-demo-decode-2=test
> coordinated-rollout-demo-decode-3=test
> coordinated-rollout-demo-prefill-0=test
> coordinated-rollout-demo-prefill-1=test
> coordinated-rollout-demo-prefill-2=test
> coordinated-rollout-demo-prefill-3=test
```

**Expected output:**

- Neither prefill nor decode significantly outpaces the other in completing the update
- After the update, all Pods in both roles contain the environment variable `new_env=test`

### Cleanup (Coordinated Upgrade)

```bash
kubectl delete cpolicy -n default coordinated-rollout-demo
kubectl delete rbg -n default coordinated-rollout-demo
```

---

## Summary

| Operation | Validation Point | Key Expectation |
| --- | --- | --- |
| Coordinated scaling | scaling.maxSkew / progression | Multi-role initial deployment creates Pods at similar progress rates |
| Coordinated upgrade | rollingUpdate.maxSkew / maxUnavailable | Multi-role update progress difference stays within limits |

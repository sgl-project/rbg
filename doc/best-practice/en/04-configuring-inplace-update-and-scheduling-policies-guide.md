# Operations Guide: Configuring In-Place Update and In-Place Scheduling Strategies

> Corresponding concept document: [4. Configuring In-Place Update and In-Place Scheduling Strategies](04-configuring-inplace-update-and-scheduling-policies.md)

## Objectives

Validate RBG's In-Place Update and In-Place Scheduling strategies, including:
1. In-Place Update with Grace Period traffic draining: Pod stays on the original node when only the image changes, with drain wait support
2. In-Place Scheduling (Preferred): Pod is preferentially scheduled back to its historical node during recreation
3. In-Place Scheduling (Required): Pod must be scheduled back to its historical node during recreation

## Prerequisites

- Kubernetes cluster version >= 1.24
- RBG Controller installed
- Images accessible: `alpine:3.23.2`, `alpine:3.23.5`

> **Note**: This document uses `sleep 3600` as a placeholder command, focusing on validating RBG upgrade control plane behavior without requiring GPU. To test real inference functionality, replace with the full inference engine startup command.

---

## Operation 1: In-Place Update with Grace Period Traffic Draining

### Step 1: Create a 2-Replica RBG with In-Place Update Strategy

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inplace-update-demo
spec:
  roles:
    - name: backend
      replicas: 2
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: InPlaceIfPossible
          maxUnavailable: 1
          inPlaceUpdateStrategy:
            gracePeriodSeconds: 30
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.2
                imagePullPolicy: IfNotPresent
                command: ["sleep", "3600"]
EOF
```

### Expected Behavior

- 2 Pods created (`inplace-update-demo-backend-0` through `inplace-update-demo-backend-1`)
- After all are ready, RBG status is Ready

### Step 2: Record Pod State Before Update

```bash
# Record Pod RESTARTS, AGE, and IP
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-update-demo -o wide

> NAME                            READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-update-demo-backend-0   1/1     Running   0          10s   10.xx.xx.11  e01-xxxxxxxxxxxxxx   <none>           2/2
> inplace-update-demo-backend-1   1/1     Running   0          10s   10.xx.xx.12  e01-xxxxxxxxxxxxxx   <none>           2/2
```

### Step 3: Trigger Image Update

```bash
kubectl patch rbg inplace-update-demo --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/image", "value": "alpine:3.23.5"}]'
```

### Expected Behavior (Image Update)

For each Pod being updated:
1. Controller sets the Pod's `InPlaceUpdateReady` condition to `False`, Pod becomes NotReady
2. Pod is removed from Service endpoints
3. Wait 30 seconds (`gracePeriodSeconds`) for existing connections to finish processing
4. Patch the container image, kubelet restarts the container
5. After the container becomes ready, Pod restores Ready state

The following in-place update behaviors are also satisfied:
- Instances are updated one by one from high to low ordinal (1 → 0); the next instance starts only after the current one finishes draining, restarts, and becomes Ready again
- `type: InPlaceIfPossible`: only image changed, triggering in-place update
- Pod does not leave its current node, AGE does not reset
- Container RESTARTS count increases

### Verification

```bash
# Observe the update process
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-update-demo -o wide -w

> NAME                            READY   STATUS    RESTARTS      AGE   IP            NODE                 NOMINATED NODE   READINESS GATES
> inplace-update-demo-backend-0   1/1     Running   0             14s   10.xx.xx.11   e01-xxxxxxxxxxxxxx   <none>           2/2
> inplace-update-demo-backend-1   1/1     Running   0             14s   10.xx.xx.12   e01-xxxxxxxxxxxxxx   <none>           2/2
> inplace-update-demo-backend-1   1/1     Running   0             16s   10.xx.xx.12   e01-xxxxxxxxxxxxxx   <none>           1/2
> inplace-update-demo-backend-1   1/1     Running   0             16s   10.xx.xx.12   e01-xxxxxxxxxxxxxx   <none>           1/2
> inplace-update-demo-backend-1   1/1     Running   0             16s   10.xx.xx.12   e01-xxxxxxxxxxxxxx   <none>           0/2
> inplace-update-demo-backend-1   1/1     Running   0             46s   10.xx.xx.12   e01-xxxxxxxxxxxxxx   <none>           0/2
> inplace-update-demo-backend-1   1/1     Running   1 (1s ago)    47s   10.xx.xx.12   e01-xxxxxxxxxxxxxx   <none>           0/2
> inplace-update-demo-backend-1   1/1     Running   1 (1s ago)    47s   10.xx.xx.12   e01-xxxxxxxxxxxxxx   <none>           1/2
> inplace-update-demo-backend-1   1/1     Running   1 (1s ago)    47s   10.xx.xx.12   e01-xxxxxxxxxxxxxx   <none>           2/2
> inplace-update-demo-backend-0   1/1     Running   0             47s   10.xx.xx.11   e01-xxxxxxxxxxxxxx   <none>           1/2
> inplace-update-demo-backend-0   1/1     Running   0             47s   10.xx.xx.11   e01-xxxxxxxxxxxxxx   <none>           1/2
> inplace-update-demo-backend-0   1/1     Running   0             47s   10.xx.xx.11   e01-xxxxxxxxxxxxxx   <none>           0/2
> inplace-update-demo-backend-0   1/1     Running   0             48s   10.xx.xx.11   e01-xxxxxxxxxxxxxx   <none>           0/2
> inplace-update-demo-backend-0   1/1     Running   0             77s   10.xx.xx.11   e01-xxxxxxxxxxxxxx   <none>           0/2
> inplace-update-demo-backend-0   1/1     Running   1 (1s ago)    78s   10.xx.xx.11   e01-xxxxxxxxxxxxxx   <none>           0/2
> inplace-update-demo-backend-0   1/1     Running   1 (1s ago)    78s   10.xx.xx.11   e01-xxxxxxxxxxxxxx   <none>           1/2
> inplace-update-demo-backend-0   1/1     Running   1 (1s ago)    78s   10.xx.xx.11   e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# Confirm all Pods have been updated to the new image and Pods were not recreated (AGE not reset)
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-update-demo -o wide

> NAME                            READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-update-demo-backend-0   1/1     Running   1          3m    10.xx.xx.11  e01-xxxxxxxxxxxxxx   <none>           2/2
> inplace-update-demo-backend-1   1/1     Running   1          3m    10.xx.xx.12  e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# Confirm all Pods use the new image
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-update-demo -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> inplace-update-demo-backend-0=alpine:3.23.5
> inplace-update-demo-backend-1=alpine:3.23.5
```

**Expected output:**
- Each Pod has approximately 30 seconds of wait time between becoming NotReady and the image update
- Pod AGE not reset (Pod was not deleted and recreated)
- Container RESTARTS count increased (container was restarted in place)
- All Pods remain on the same nodes as before the update (Pod did not migrate)
- All Pods use the new image `alpine:3.23.5`

### Cleanup

```bash
kubectl delete rbg inplace-update-demo
```

---

## Operation 2: In-Place Scheduling — Preferred Mode (Soft Affinity)

### Step 1: Create an RBG with Preferred In-Place Scheduling

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inplace-scheduling-preferred
spec:
  roles:
    - name: backend
      replicas: 4
      annotations:
        rbg.workloads.x-k8s.io/role-inplace-scheduling: "Preferred"
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: RecreatePod
          maxUnavailable: 1
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

### Step 2: Record Pod Nodes Before Update

```bash
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-preferred -o wide

> NAME                                  READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-scheduling-preferred-backend-0   1/1     Running   0          60s   10.xx.xx.xx  node-A               <none>           2/2
> inplace-scheduling-preferred-backend-1   1/1     Running   0          60s   10.xx.xx.xx  node-B               <none>           2/2
> inplace-scheduling-preferred-backend-2   1/1     Running   0          60s   10.xx.xx.xx  node-C               <none>           2/2
> inplace-scheduling-preferred-backend-3   1/1     Running   0          60s   10.xx.xx.xx  node-D               <none>           2/2
```

### Step 3: Trigger Environment Variable Update

> **Note**: An environment variable change is intentionally used here because `env` modification is not within the supported scope of in-place update (see concept document), which forces Pod recreation and thus validates whether in-place scheduling takes effect.

```bash
kubectl patch rbg inplace-scheduling-preferred --type='json' \
  -p='[{"op": "add", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/env", "value": [{"name": "new_env", "value": "test"}]}]'
```

### Expected Behavior (Preferred In-Place Scheduling)

- `type: RecreatePod`: Pod needs to be deleted and recreated
- `role-inplace-scheduling: Preferred`: injects `preferredDuringScheduling` (weight=100), new Pod preferentially scheduled back to historical node
- If the historical node has sufficient resources, the new Pod returns to the original node
- If the historical node lacks resources, the Pod can be scheduled to another node (scheduling is not blocked)

### Verification (Preferred In-Place Scheduling)

```bash
# Observe Pod recreation process and node placement
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-preferred -o wide -w
```

```bash
# Check the new Pod's node affinity, confirm Preferred affinity is injected
kubectl get pod inplace-scheduling-preferred-backend-3 -o jsonpath='{.spec.affinity}'

# Expected output contains preferredDuringSchedulingIgnoredDuringExecution, weight=100
```

```bash
# After update completes, confirm Pod node placement
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-preferred -o wide

> NAME                                  READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-scheduling-preferred-backend-0   1/1     Running   0          30s   10.xx.xx.xx  node-A               <none>           2/2
> inplace-scheduling-preferred-backend-1   1/1     Running   0          30s   10.xx.xx.xx  node-B               <none>           2/2
> inplace-scheduling-preferred-backend-2   1/1     Running   0          30s   10.xx.xx.xx  node-C               <none>           2/2
> inplace-scheduling-preferred-backend-3   1/1     Running   0          30s   10.xx.xx.xx  node-D               <none>           2/2
```

```bash
# Confirm all Pods contain the new environment variable
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-preferred -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].env[?(@.name=="new_env")].value}{"\n"}{end}'

> inplace-scheduling-preferred-backend-0=test
> inplace-scheduling-preferred-backend-1=test
> inplace-scheduling-preferred-backend-2=test
> inplace-scheduling-preferred-backend-3=test
```

**Expected output:**
- Pod AGE reset (Pod was deleted and recreated)
- New Pod's affinity contains `preferredDuringSchedulingIgnoredDuringExecution`
- With sufficient node resources, new Pods return to their original nodes

### Cleanup (Preferred In-Place Scheduling)

```bash
kubectl delete rbg inplace-scheduling-preferred
```

---

## Operation 3: In-Place Scheduling — Required Mode (Hard Affinity)

### Step 1: Create an RBG with Required In-Place Scheduling

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inplace-scheduling-required
spec:
  roles:
    - name: backend
      replicas: 4
      annotations:
        rbg.workloads.x-k8s.io/role-inplace-scheduling: "Required"
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          type: RecreatePod
          maxUnavailable: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.5
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
EOF
```

### Step 2: Record Pod Nodes Before Update (Required)

```bash
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-required -o wide

> NAME                                  READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-scheduling-required-backend-0   1/1     Running   0          60s   10.xx.xx.xx  node-A               <none>           2/2
> inplace-scheduling-required-backend-1   1/1     Running   0          60s   10.xx.xx.xx  node-B               <none>           2/2
> inplace-scheduling-required-backend-2   1/1     Running   0          60s   10.xx.xx.xx  node-C               <none>           2/2
> inplace-scheduling-required-backend-3   1/1     Running   0          60s   10.xx.xx.xx  node-D               <none>           2/2
```

### Step 3: Trigger Environment Variable Update (Required)

> **Note**: An environment variable change is intentionally used here because `env` modification is not within the supported scope of in-place update (see concept document), which forces Pod recreation and thus validates whether in-place scheduling takes effect.

```bash
kubectl patch rbg inplace-scheduling-required --type='json' \
  -p='[{"op": "add", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/env", "value": [{"name": "new_env", "value": "test"}]}]'
```

### Expected Behavior (Required In-Place Scheduling)

- `role-inplace-scheduling: Required`: injects `requiredDuringScheduling`, new Pod **must** be scheduled back to the historical node
- If the historical node is unavailable, the Pod will remain in Pending state
- After update completes, new Pods return precisely to their original nodes

### Verification (Required In-Place Scheduling)

```bash
# Observe Pod recreation process and node placement
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-required -o wide -w
```

```bash
# Check the new Pod's node affinity, confirm Required affinity is injected
kubectl get pod inplace-scheduling-required-backend-3 -o jsonpath='{.spec.affinity}'

# Expected output contains requiredDuringSchedulingIgnoredDuringExecution
```

```bash
# After update completes, confirm Pod nodes match pre-update placement
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-required -o wide

> NAME                                  READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-scheduling-required-backend-0   1/1     Running   0          30s   10.xx.xx.xx  node-A               <none>           2/2
> inplace-scheduling-required-backend-1   1/1     Running   0          30s   10.xx.xx.xx  node-B               <none>           2/2
> inplace-scheduling-required-backend-2   1/1     Running   0          30s   10.xx.xx.xx  node-C               <none>           2/2
> inplace-scheduling-required-backend-3   1/1     Running   0          30s   10.xx.xx.xx  node-D               <none>           2/2
```

```bash
# Confirm all Pods contain the new environment variable
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-required -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].env[?(@.name=="new_env")].value}{"\n"}{end}'

> inplace-scheduling-required-backend-0=test
> inplace-scheduling-required-backend-1=test
> inplace-scheduling-required-backend-2=test
> inplace-scheduling-required-backend-3=test
```

**Expected output:**
- Pod AGE reset (Pod was deleted and recreated)
- New Pod's affinity contains `requiredDuringSchedulingIgnoredDuringExecution`
- New Pods return precisely to their original nodes (NODE column exactly matches pre-update placement)

### Cleanup (Required In-Place Scheduling)

```bash
kubectl delete rbg inplace-scheduling-required
```

---

## Summary

| Operation | Verification Point | Key Expectation |
| --- | --- | --- |
| In-Place Update with Grace Period | Pod AGE not reset, RESTARTS increased, ~30s wait after NotReady | With image-only change, Pod stays on original node, waits gracePeriodSeconds before in-place update |
| In-Place Scheduling (Preferred) | preferredDuringScheduling injected | Pod preferentially returns to historical node during recreation, but scheduling is not blocked |
| In-Place Scheduling (Required) | requiredDuringScheduling injected | Pod must return to historical node during recreation, otherwise Pending |

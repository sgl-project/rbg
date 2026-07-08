# Operations Guide: Configuring Rolling Update Strategies

> Corresponding concept document: [3. Configuring Rolling Update Strategies](03-configuring-rolling-updates.md)

## Objectives

Validate RBG's `rolloutStrategy` rolling update strategy, including:
1. Basic rolling update configuration (maxUnavailable / maxSurge)
2. Partition canary release
3. Paused pause and resume updates
4. CoordinatedPolicy multi-role coordinated upgrade

## Prerequisites

- Kubernetes cluster version >= 1.24
- RBG Controller installed
- Images accessible: `lmsysorg/sglang:v0.5.9`, `lmsysorg/sglang:v0.5.12.post1`

> **Note**: This document uses `sleep 3600` as a placeholder command, focusing on validating RBG rolling update control plane behavior without requiring GPU. To test real inference functionality, replace with the full inference engine startup command.

---

## Operation 1: Basic Rolling Update

### Step 1: Create a 4-Replica RBG

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: rolling-update-demo
spec:
  roles:
    - name: backend
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
          maxSurge: 0
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
EOF
```

### Expected Behavior

- 4 Pods created (`rolling-update-demo-backend-0` through `rolling-update-demo-backend-3`)
- After all are ready, RBG status is Ready

### Step 2: Trigger Rolling Update

```bash
# Update image version to trigger rolling update
kubectl patch rbg rolling-update-demo --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/image", "value": "lmsysorg/sglang:v0.5.12.post1"}]'
```

### Expected Behavior

- Instances are updated one by one from high to low ordinal (3 → 2 → 1 → 0)
- `maxUnavailable: 1`: At most 1 instance unavailable at a time
- `maxSurge: 0`: No extra instances created, delete old then create new
- 3 instances always available during the update process

### Verification

```bash
# Observe the update process (continuously execute to see changes)
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rolling-update-demo -o wide -w

> NAME                            READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> rolling-update-demo-backend-0   1/1     Running   0          68s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-1   1/1     Running   0          68s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-2   1/1     Running   0          68s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-3   1/1     Running   0          68s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-3   1/1     Running   0          78s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-3   1/1     Running   0          78s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-3   1/1     Running   1 (0s ago)   108s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-3   1/1     Running   1 (0s ago)   108s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-3   1/1     Running   1 (0s ago)   108s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-2   1/1     Running   0            108s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-2   1/1     Running   0            108s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-3   1/1     Running   1 (2s ago)   110s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-2   1/1     Running   1 (1s ago)   2m19s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-2   1/1     Running   1 (1s ago)   2m19s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-2   1/1     Running   1 (1s ago)   2m19s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-1   1/1     Running   0            2m19s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-1   1/1     Running   0            2m19s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-2   1/1     Running   1 (2s ago)   2m20s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-1   1/1     Running   1 (0s ago)   2m49s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-1   1/1     Running   1 (0s ago)   2m49s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-1   1/1     Running   1 (0s ago)   2m49s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-0   1/1     Running   0            2m49s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-0   1/1     Running   0            2m49s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-1   1/1     Running   1 (2s ago)   2m51s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# Confirm all Pods have been updated to the new image
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rolling-update-demo -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> rolling-update-demo-backend-0=lmsysorg/sglang:v0.5.12.post1
> rolling-update-demo-backend-1=lmsysorg/sglang:v0.5.12.post1
> rolling-update-demo-backend-2=lmsysorg/sglang:v0.5.12.post1
> rolling-update-demo-backend-3=lmsysorg/sglang:v0.5.12.post1
```

**Expected output:**
- 3 Pods always Ready during the update process
- After update completes, all 4 Pods use `lmsysorg/sglang:v0.5.12.post1`

### Cleanup

```bash
kubectl delete rbg rolling-update-demo
```

---

## Operation 2: Canary Release (Partition)

### Step 1: Create a 4-Replica RBG with partition

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: canary-deployment
spec:
  roles:
    - name: backend
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
          partition: 4  # Initially set to 4, no instances updated
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
EOF
```

### Step 2: Update Image but Keep partition=4

```bash
# Update image version
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/image", "value": "lmsysorg/sglang:v0.5.12.post1"}]'
```

### Expected Behavior

- Since `partition: 4`, only instances with ordinal >= 4 will be updated
- Ordinals 0-3 are all less than 4, so **no instances are updated**
- All Pods still use the old image `v0.5.9`

### Verification

```bash
# Confirm all Pods still use the old image
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> canary-deployment-backend-0=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-1=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-2=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-3=lmsysorg/sglang:v0.5.9
```

**Expected output:** All 4 Pods use `lmsysorg/sglang:v0.5.9`

### Step 3: Lower partition to 3, Update the Last Instance

```bash
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/partition", "value": 3}]'
```

### Expected Behavior

- Instances with ordinal >= 3 (i.e., instance 3) are updated to the new image
- Ordinals 0-2 remain on the old version

### Verification

```bash
# Check each Pod's image version
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> canary-deployment-backend-0=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-1=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-2=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-3=lmsysorg/sglang:v0.5.12.post1
```

**Expected output:**
- `canary-deployment-backend-3` uses `v0.5.12.post1`
- `canary-deployment-backend-0` through `2` use `v0.5.9`

### Step 4: Lower partition to 0, Complete Full Update

```bash
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/partition", "value": 0}]'
```

### Expected Behavior

- All instances with ordinal >= 0 are updated to the new image

### Verification

```bash
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> canary-deployment-backend-0=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-1=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-2=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-3=lmsysorg/sglang:v0.5.12.post1
```

**Expected output:** All 4 Pods use `lmsysorg/sglang:v0.5.12.post1`

### Cleanup

```bash
kubectl delete rbg canary-deployment
```

---

## Operation 3: Pause and Resume Updates

### Step 1: Create an RBG and Trigger Update Then Pause

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: paused-update
spec:
  roles:
    - name: backend
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 4
          paused: true  # Pause updates
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
EOF
```

### Step 2: Update Image

```bash
kubectl patch rbg paused-update --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/image", "value": "lmsysorg/sglang:v0.5.12.post1"}]'
```

### Expected Behavior

- Since `paused: true`, the update is paused
- All Pods remain on the old version

### Verification

```bash
# Confirm all Pods still use the old image
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=paused-update -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> paused-update-backend-0=lmsysorg/sglang:v0.5.9
> paused-update-backend-1=lmsysorg/sglang:v0.5.9
> paused-update-backend-2=lmsysorg/sglang:v0.5.9
> paused-update-backend-3=lmsysorg/sglang:v0.5.9
```

**Expected output:** All 4 Pods use `v0.5.9` (update is paused)

### Step 3: Resume Updates

```bash
kubectl patch rbg paused-update --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/paused", "value": false}]'
```

### Expected Behavior

- Updates resume
- Pods are updated to the new image one by one

### Verification

```bash
# Wait for update to complete, confirm all Pods use the new image
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=paused-update -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> canary-deployment-backend-0=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-1=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-2=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-3=lmsysorg/sglang:v0.5.12.post1
```

**Expected output:** All 4 Pods use `v0.5.12.post1`

### Cleanup

```bash
kubectl delete rbg paused-update
```

---

## Operation 4: Multi-Role Coordinated Upgrade (CoordinatedPolicy)

### Step 1: Create a Multi-Role RBG

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-rollout-demo
spec:
  roles:
    - name: prefill
      replicas: 7
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000

    - name: decode
      replicas: 3
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
EOF
```

### Step 2: Create CoordinatedPolicy

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: pd-rollout-demo
spec:
  policies:
    - name: prefill-decode-rollout
      roles:
        - prefill
        - decode
      strategy:
        rollingUpdate:
          maxSkew: "10%"
          maxUnavailable: "10%"
EOF
```

### Step 3: Update Both Roles' Images Simultaneously

```bash
kubectl patch rbg pd-rollout-demo --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/image", "value": "lmsysorg/sglang:v0.5.12.post1"},
       {"op": "replace", "path": "/spec/roles/1/standalonePattern/template/spec/containers/0/image", "value": "lmsysorg/sglang:v0.5.12.post1"}]'
```

### Expected Behavior

- Prefill and Decode start updating simultaneously
- `maxSkew: "10%"` constrains the update progress difference between the two roles to within 10%
- If Prefill updates too fast (progress difference > 10%), Controller pauses Prefill, waiting for Decode to catch up
- Both roles' update progress remains close at all times

### Verification

```bash
# Observe the Pod update process
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=pd-rollout-demo -o wide -w

> NAME                        READY   STATUS    RESTARTS   AGE     IP            NODE                 NOMINATED NODE   READINESS GATES
> pd-rollout-demo-decode-0    1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-decode-1    1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-decode-2    1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           0/2
> pd-rollout-demo-prefill-0   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-1   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-2   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-3   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-4   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-5   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-6   1/1     Running   1 (1s ago)   3m56s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-decode-2    1/1     Running   1 (1s ago)   3m56s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-5   1/1     Running   1 (1s ago)   4m27s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-4   1/1     Running   1 (0s ago)   4m57s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-decode-1    1/1     Running   1 (0s ago)   4m57s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-3   1/1     Running   1 (1s ago)   5m28s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-2   1/1     Running   1 (1s ago)   5m59s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-decode-0    1/1     Running   1 (1s ago)   5m59s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-1   1/1     Running   1 (0s ago)   6m29s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-0   1/1     Running   1 (1s ago)   7m      10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
```

Through the AGE field, you can observe the update steps: 1p1d (1 prefill + 1 decode) - 1p - 1p1d - 1p - 1p1d - 1p - 1p..


```bash
# After update completes, confirm all Pods use the new image
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=pd-rollout-demo -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> pd-rollout-demo-decode-0=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-decode-1=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-decode-2=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-0=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-1=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-2=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-3=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-4=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-5=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-6=lmsysorg/sglang:v0.5.12.post1
```

**Expected output:**
- During the update process, the progress difference between the two roles remains <= 10%
- After update completes, all Pods use `v0.5.12.post1`
- CoordinatedPolicy's status shows the coordination state

### Cleanup

```bash
kubectl delete cpolicy pd-rollout-demo
kubectl delete rbg pd-rollout-demo
```

---

## Summary

| Operation | Verification Point | Key Expectation |
| --- | --- | --- |
| Basic rolling update | maxUnavailable / maxSurge | One-by-one replacement, always 3/4 available |
| Canary release | partition partition control | Only instances with ordinal >= partition are updated |
| Pause and resume | paused pause mechanism | No updates when paused, continues after resume |
| Coordinated upgrade | CoordinatedPolicy maxSkew | Two roles' update progress difference <= 10% |

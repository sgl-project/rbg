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
- Images accessible: `alpine:3.23.5`

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
                image: alpine:3.23.5
                command: ["sleep", "3600"]
EOF
```

### Expected Behavior

- 4 Pods created (`rolling-update-demo-backend-0` through `rolling-update-demo-backend-3`)
- After all are ready, RBG status is Ready

### Step 2: Trigger Rolling Update

```bash
# Modify command (extend the sleep duration) to trigger a rolling update (Pod rebuild)
kubectl patch rbg rolling-update-demo --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/command", "value": ["sleep", "7200"]}]'
```

### Expected Behavior (Rolling Update Triggered)

- Instances are updated one by one from high to low ordinal (3 → 2 → 1 → 0)
- `maxUnavailable: 1`: At most 1 instance unavailable at a time
- `maxSurge: 0`: No extra instances created, delete old then create new
- 3 instances always available during the update process
- `command` is a non-image field, triggering Pod rebuild: the old Pod terminates first, then is recreated with the same ordinal (AGE resets, RESTARTS is 0)

### Verification

```bash
# Observe the update process (continuously execute to see changes)
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rolling-update-demo -w

> NAME                            READY   STATUS        RESTARTS   AGE
> rolling-update-demo-backend-0   1/1     Running       0          2m5s
> rolling-update-demo-backend-1   1/1     Running       0          2m5s
> rolling-update-demo-backend-2   1/1     Running       0          2m5s
> rolling-update-demo-backend-3   1/1     Terminating   0          2m5s
> rolling-update-demo-backend-3   0/1     Pending       0          0s
> rolling-update-demo-backend-3   0/1     ContainerCreating   0          0s
> rolling-update-demo-backend-3   1/1     Running             0          28s
> rolling-update-demo-backend-2   1/1     Terminating         0          2m59s
> rolling-update-demo-backend-2   0/1     Pending             0          0s
> rolling-update-demo-backend-2   0/1     ContainerCreating   0          0s
> rolling-update-demo-backend-2   1/1     Running             0          26s
> rolling-update-demo-backend-1   1/1     Terminating         0          3m56s
> rolling-update-demo-backend-1   0/1     Pending             0          0s
> rolling-update-demo-backend-1   0/1     ContainerCreating   0          0s
> rolling-update-demo-backend-1   1/1     Running             0          30s
> rolling-update-demo-backend-0   1/1     Terminating         0          4m58s
> rolling-update-demo-backend-0   0/1     Pending             0          0s
> rolling-update-demo-backend-0   0/1     ContainerCreating   0          0s
> rolling-update-demo-backend-0   1/1     Running             0          28s
```

You can observe Pods being "terminated → recreated" one by one in ordinal order 3 → 2 → 1 → 0, with at most 1 instance unavailable at any moment.

```bash
# Confirm all Pods have been updated to the new command
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rolling-update-demo -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> rolling-update-demo-backend-0=["sleep","7200"]
> rolling-update-demo-backend-1=["sleep","7200"]
> rolling-update-demo-backend-2=["sleep","7200"]
> rolling-update-demo-backend-3=["sleep","7200"]
```

**Expected output:**

- 3 Pods always Ready during the update process
- After update completes, all 4 Pods have command `["sleep","7200"]`

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
                image: alpine:3.23.5
                command: ["sleep", "3600"]
EOF
```

### Step 2: Update command but Keep partition=4

```bash
# Modify command (extend the sleep duration)
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/command", "value": ["sleep", "7200"]}]'
```

### Expected Behavior (Partition=4)

- Since `partition: 4`, only instances with ordinal >= 4 will be updated
- Ordinals 0-3 are all less than 4, so **no instances are updated**
- All Pods still use the old command `["sleep","3600"]`

### Verification (Partition=4)

```bash
# Confirm all Pods still use the old command
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> canary-deployment-backend-0=["sleep","3600"]
> canary-deployment-backend-1=["sleep","3600"]
> canary-deployment-backend-2=["sleep","3600"]
> canary-deployment-backend-3=["sleep","3600"]
```

**Expected output:** All 4 Pods have command `["sleep","3600"]`

### Step 3: Lower partition to 3, Update the Last Instance

```bash
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/partition", "value": 3}]'
```

### Expected Behavior (Partition=3)

- Instances with ordinal >= 3 (i.e., instance 3) are updated to the new command
- Ordinals 0-2 remain on the old configuration

### Verification (Partition=3)

```bash
# Check each Pod's command
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> canary-deployment-backend-0=["sleep","3600"]
> canary-deployment-backend-1=["sleep","3600"]
> canary-deployment-backend-2=["sleep","3600"]
> canary-deployment-backend-3=["sleep","7200"]
```

**Expected output:**

- `canary-deployment-backend-3` uses `["sleep","7200"]`
- `canary-deployment-backend-0` through `2` use `["sleep","3600"]`

### Step 4: Lower partition to 0, Complete Full Update

```bash
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/partition", "value": 0}]'
```

### Expected Behavior (Partition=0)

- All instances with ordinal >= 0 are updated to the new command

### Verification (Partition=0)

```bash
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> canary-deployment-backend-0=["sleep","7200"]
> canary-deployment-backend-1=["sleep","7200"]
> canary-deployment-backend-2=["sleep","7200"]
> canary-deployment-backend-3=["sleep","7200"]
```

**Expected output:** All 4 Pods have command `["sleep","7200"]`

### Cleanup (Canary Release)

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
                image: alpine:3.23.5
                command: ["sleep", "3600"]
EOF
```

### Step 2: Update command

```bash
kubectl patch rbg paused-update --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/command", "value": ["sleep", "7200"]}]'
```

### Expected Behavior (Paused)

- Since `paused: true`, the update is paused
- All Pods remain on the old configuration

### Verification (Paused)

```bash
# Confirm all Pods still use the old command
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=paused-update -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> paused-update-backend-0=["sleep","3600"]
> paused-update-backend-1=["sleep","3600"]
> paused-update-backend-2=["sleep","3600"]
> paused-update-backend-3=["sleep","3600"]
```

**Expected output:** All 4 Pods have command `["sleep","3600"]` (update is paused)

### Step 3: Resume Updates

```bash
kubectl patch rbg paused-update --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/paused", "value": false}]'
```

### Expected Behavior (Resumed)

- Updates resume
- Pods are updated to the new command one by one

### Verification (Resumed)

```bash
# Wait for update to complete, confirm all Pods use the new command
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=paused-update -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> paused-update-backend-0=["sleep","7200"]
> paused-update-backend-1=["sleep","7200"]
> paused-update-backend-2=["sleep","7200"]
> paused-update-backend-3=["sleep","7200"]
```

**Expected output:** All 4 Pods have command `["sleep","7200"]`

### Cleanup (Pause and Resume)

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
                image: alpine:3.23.5
                command: ["sleep", "3600"]

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
                image: alpine:3.23.5
                command: ["sleep", "3600"]
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

### Step 3: Update Both Roles' command Simultaneously

```bash
kubectl patch rbg pd-rollout-demo --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/command", "value": ["sleep", "7200"]},
       {"op": "replace", "path": "/spec/roles/1/standalonePattern/template/spec/containers/0/command", "value": ["sleep", "7200"]}]'
```

### Expected Behavior (Coordinated Upgrade)

- Prefill and Decode start updating simultaneously
- `maxSkew: "10%"` constrains the update progress difference between the two roles to within 10%
- If Prefill updates too fast (progress difference > 10%), Controller pauses Prefill, waiting for Decode to catch up
- Both roles' update progress remains close at all times
- `command` is a non-image field, so the update proceeds via Pod rebuild (Pods terminate and are recreated with the same ordinals one by one)

### Verification (Coordinated Upgrade)

```bash
# Observe the Pod update process
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=pd-rollout-demo -w

> NAME                        READY   STATUS        RESTARTS   AGE
> pd-rollout-demo-decode-0    1/1     Running       0          43m
> pd-rollout-demo-decode-1    1/1     Running       0          43m
> pd-rollout-demo-decode-2    1/1     Terminating   0          43m
> pd-rollout-demo-prefill-0   1/1     Running       0          43m
> pd-rollout-demo-prefill-1   1/1     Running       0          43m
> pd-rollout-demo-prefill-2   1/1     Running       0          43m
> pd-rollout-demo-prefill-3   1/1     Running       0          43m
> pd-rollout-demo-prefill-4   1/1     Running       0          43m
> pd-rollout-demo-prefill-5   1/1     Running       0          43m
> pd-rollout-demo-prefill-6   1/1     Terminating   0          43m
> pd-rollout-demo-prefill-6   0/1     Pending       0          0s
> pd-rollout-demo-decode-2    0/1     Pending       0          0s
> pd-rollout-demo-prefill-6   0/1     ContainerCreating   0          0s
> pd-rollout-demo-decode-2    0/1     ContainerCreating   0          0s
> pd-rollout-demo-decode-2    1/1     Running             0          39s
> pd-rollout-demo-prefill-6   1/1     Running             0          48s
> pd-rollout-demo-prefill-5   1/1     Terminating         0          44m
> pd-rollout-demo-prefill-5   0/1     Pending             0          0s
> pd-rollout-demo-prefill-5   0/1     ContainerCreating   0          0s
> pd-rollout-demo-prefill-5   1/1     Running             0          28s
> pd-rollout-demo-prefill-4   1/1     Terminating         0          45m
> pd-rollout-demo-decode-1    1/1     Terminating         0          45m
> pd-rollout-demo-prefill-4   0/1     Pending             0          0s
> pd-rollout-demo-prefill-4   0/1     ContainerCreating   0          0s
> pd-rollout-demo-decode-1    0/1     Pending             0          0s
> pd-rollout-demo-decode-1    0/1     ContainerCreating   0          0s
> pd-rollout-demo-prefill-4   1/1     Running             0          37s
> pd-rollout-demo-prefill-3   1/1     Terminating         0          47m
> pd-rollout-demo-decode-1    0/1     ContainerCreating   0          38s
> pd-rollout-demo-decode-1    1/1     Running             0          38s
> pd-rollout-demo-prefill-3   0/1     Pending             0          0s
> pd-rollout-demo-prefill-3   0/1     ContainerCreating   0          0s
> pd-rollout-demo-prefill-3   1/1     Running             0          27s
> pd-rollout-demo-prefill-2   1/1     Terminating         0          48m
> pd-rollout-demo-decode-0    1/1     Terminating         0          48m
> pd-rollout-demo-prefill-2   0/1     Pending             0          0s
> pd-rollout-demo-decode-0    0/1     Pending             0          0s
> pd-rollout-demo-prefill-2   0/1     ContainerCreating   0          0s
> pd-rollout-demo-decode-0    0/1     ContainerCreating   0          0s
> pd-rollout-demo-decode-0    1/1     Running             0          38s
> pd-rollout-demo-prefill-2   1/1     Running             0          48s
> pd-rollout-demo-prefill-1   1/1     Terminating         0          49m
> pd-rollout-demo-prefill-1   0/1     Pending             0          0s
> pd-rollout-demo-prefill-1   0/1     ContainerCreating   0          0s
> pd-rollout-demo-prefill-1   1/1     Running             0          30s
> pd-rollout-demo-prefill-0   1/1     Terminating         0          50m
> pd-rollout-demo-prefill-0   0/1     Error               0          50m
> pd-rollout-demo-prefill-0   0/1     Pending             0          0s
> pd-rollout-demo-prefill-0   0/1     ContainerCreating   0          0s
> pd-rollout-demo-prefill-0   1/1     Running             0          29s
```

From the update cadence, you can observe the coordination steps: 1p1d (1 prefill + 1 decode) - 1p - 1p1d - 1p - 1p1d - 1p - 1p, with the progress difference between the two roles always constrained by `maxSkew`.

```bash
# After update completes, confirm all Pods use the new command
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=pd-rollout-demo -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> pd-rollout-demo-decode-0=["sleep","7200"]
> pd-rollout-demo-decode-1=["sleep","7200"]
> pd-rollout-demo-decode-2=["sleep","7200"]
> pd-rollout-demo-prefill-0=["sleep","7200"]
> pd-rollout-demo-prefill-1=["sleep","7200"]
> pd-rollout-demo-prefill-2=["sleep","7200"]
> pd-rollout-demo-prefill-3=["sleep","7200"]
> pd-rollout-demo-prefill-4=["sleep","7200"]
> pd-rollout-demo-prefill-5=["sleep","7200"]
> pd-rollout-demo-prefill-6=["sleep","7200"]
```

**Expected output:**

- During the update process, the progress difference between the two roles remains <= 10%
- After update completes, all Pods have command `["sleep","7200"]`
- CoordinatedPolicy's status shows the coordination state

### Cleanup (Coordinated Upgrade)

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

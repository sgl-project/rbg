# Operations Guide: Using RoleTemplates to Reduce Configuration Duplication

> Corresponding concept document: [2. Using RoleTemplates to Reduce Configuration Duplication](02-using-role-templates.md)

## Objectives

Validate RBG's `roleTemplates` mechanism, including:
1. Defining templates and referencing them via `templateRef`
2. Overriding via `patch` for differentiation (Strategic Merge Patch)
3. Combining with `leaderWorkerPattern`
4. Verifying correct configuration merge results

## Prerequisites

- Kubernetes cluster version >= 1.24
- RBG Controller installed
- Image accessible: `lmsysorg/sglang:v0.5.9`

> **Note**: Operation 1 uses `sleep 3600` as a placeholder command, focusing on validating the templateRef mechanism without requiring GPU. Operations 2 and 3 add full inference engine startup commands via patch, requiring GPU nodes.

---

## Operation 1: Define a Template and Reference It Directly

### Step 1: Create an RBG Using templateRef

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: rbg-with-templates
spec:
  roleTemplates:
    - name: engine-base
      template:
        spec:
          containers:
            - name: engine
              image: lmsysorg/sglang:v0.5.9
              command: ["sleep", "3600"]
              resources:
                requests:
                  cpu: "1"
                  memory: "1Gi"
                limits:
                  cpu: "1"
                  memory: "1Gi"
              volumeMounts:
                - name: dshm
                  mountPath: /dev/shm
          volumes:
            - name: dshm
              emptyDir:
                medium: Memory
                sizeLimit: 30Gi

  roles:
    - name: backend
      replicas: 1
      standalonePattern:
        templateRef:
          name: engine-base
          patch: {}
EOF
```

### Expected Behavior

- RBG created successfully, role `backend` references template `engine-base`
- Pod uses the image, resources, and volume configuration defined in the template
- Pod starts normally and becomes ready

### Verification

```bash
# Check RBG status
kubectl get rbg rbg-with-templates

> NAME                 READY   AGE
> rbg-with-templates   True    21m
```

```bash
# Wait for Pod to be ready
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=rbg-with-templates --timeout=300s

> pod/rbg-with-templates-backend-0 condition met
```

```bash
# Check Pod status
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rbg-with-templates

> NAME                           READY   STATUS    RESTARTS   AGE
> rbg-with-templates-backend-0   1/1     Running   0          22m
```

```bash
# Verify the Pod uses the image from the template
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rbg-with-templates -o jsonpath='{.items[0].spec.containers[0].image}'

> lmsysorg/sglang:v0.5.9
```

**Expected output:**
- Pod image is `lmsysorg/sglang:v0.5.9` (from template)
- Pod uses `sleep 3600` command to stay running (from template)
- Pod requests CPU and memory resources (from template)
- Pod mounts the dshm volume (from template)

### Cleanup

```bash
kubectl delete rbg rbg-with-templates
```

---

## Operation 2: Override Template via Patch (PD-Disaggregated Deployment)

### Step 1: Create a Multi-Role RBG Using templateRef + patch

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-with-templates
spec:
  roleTemplates:
    - name: engine-base
      template:
        spec:
          containers:
            - name: engine
              image: lmsysorg/sglang:v0.5.9
              ports:
                - name: http
                  containerPort: 8000
              resources:
                requests:
                  nvidia.com/gpu: "1"
                limits:
                  nvidia.com/gpu: "1"
              volumeMounts:
                - name: dshm
                  mountPath: /dev/shm
          volumes:
            - name: dshm
              emptyDir:
                medium: Memory
                sizeLimit: 30Gi

  roles:
    - name: prefill
      replicas: 1
      standalonePattern:
        templateRef:
          name: engine-base
          patch:
            spec:
              containers:
                - name: engine
                  command:
                    - python3
                    - -m
                    - sglang.launch_server
                    - --model-path
                    - "Qwen/Qwen3-0.6B"
                    - --host
                    - "0.0.0.0"
                    - --port
                    - "8000"
                    - --disaggregation-mode
                    - "prefill"
                    - --tp-size
                    - "1"

    - name: decode
      replicas: 1
      standalonePattern:
        templateRef:
          name: engine-base
          patch:
            spec:
              containers:
                - name: engine
                  command:
                    - python3
                    - -m
                    - sglang.launch_server
                    - --model-path
                    - "Qwen/Qwen3-0.6B"
                    - --host
                    - "0.0.0.0"
                    - --port
                    - "8000"
                    - --disaggregation-mode
                    - "decode"
                    - --tp-size
                    - "1"
EOF
```

### Expected Behavior

- Prefill and Decode roles share the base configuration from template `engine-base` (image, ports, resources, volumes)
- Patch only adds the differing `command` startup parameters for each
- Both roles' Pods have the same image (from template) but different startup parameters (from patch)

### Verification

```bash
# Check RBG status
kubectl get rbg pd-with-templates -o wide

> NAME                READY   AGE
> pd-with-templates   True    46s
```

```bash
# Check Pod status
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=pd-with-templates -o wide

> NAME                          READY   STATUS    RESTARTS     AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> pd-with-templates-decode-0    1/1     Running   0            46s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-with-templates-prefill-0   1/1     Running   1 (2s ago)   46s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# Verify Prefill Pod startup parameters (should include --disaggregation-mode prefill)
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=prefill -o jsonpath='{.items[0].spec.containers[0].command}'

> ["python3","-m","sglang.launch_server","--model-path","Qwen/Qwen3-0.6B","--host","0.0.0.0","--port","8000","--disaggregation-mode","prefill","--tp-size","1"]
```

```bash
# Verify Decode Pod startup parameters (should include --disaggregation-mode decode)
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=decode -o jsonpath='{.items[0].spec.containers[0].command}'

> ["python3","-m","sglang.launch_server","--model-path","Qwen/Qwen3-0.6B","--host","0.0.0.0","--port","8000","--disaggregation-mode","decode","--tp-size","1"]
```

```bash
# Verify both roles have the same image (from template)
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=pd-with-templates -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> pd-with-templates-decode-0=lmsysorg/sglang:v0.5.9
> pd-with-templates-prefill-0=lmsysorg/sglang:v0.5.9
```

**Expected output:**
- Both Pods are Running
- Prefill Pod's command includes `--disaggregation-mode prefill`
- Decode Pod's command includes `--disaggregation-mode decode`
- Both Pods' image is `lmsysorg/sglang:v0.5.9`

### Step 2: Verify Patch Merge Rules (Scalar Override)

```bash
# Update the template's resource requests, verify patch can override scalar fields
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-with-templates
spec:
  roleTemplates:
    - name: engine-base
      template:
        spec:
          containers:
            - name: engine
              image: lmsysorg/sglang:v0.5.9
              ports:
                - name: http
                  containerPort: 8000
              resources:
                requests:
                  nvidia.com/gpu: "1"
                  memory: "8Gi"
                limits:
                  nvidia.com/gpu: "1"
              volumeMounts:
                - name: dshm
                  mountPath: /dev/shm
          volumes:
            - name: dshm
              emptyDir:
                medium: Memory
                sizeLimit: 30Gi

  roles:
    - name: prefill
      replicas: 1
      standalonePattern:
        templateRef:
          name: engine-base
          patch:
            spec:
              containers:
                - name: engine
                  resources:
                    requests:
                      memory: "16Gi"   # Override 8Gi from template
                  command:
                    - python3
                    - -m
                    - sglang.launch_server
                    - --model-path
                    - "Qwen/Qwen3-0.6B"
                    - --host
                    - "0.0.0.0"
                    - --port
                    - "8000"
                    - --disaggregation-mode
                    - "prefill"
                    - --tp-size
                    - "1"

    - name: decode
      replicas: 1
      standalonePattern:
        templateRef:
          name: engine-base
          patch:
            spec:
              containers:
                - name: engine
                  command:
                    - python3
                    - -m
                    - sglang.launch_server
                    - --model-path
                    - "Qwen/Qwen3-0.6B"
                    - --host
                    - "0.0.0.0"
                    - --port
                    - "8000"
                    - --disaggregation-mode
                    - "decode"
                    - --tp-size
                    - "1"
EOF
```

### Expected Behavior

- Prefill Pod's memory request is overridden by patch to `16Gi`
- Prefill Pod's GPU request remains `1` from template (not overridden)
- Decode Pod's memory request remains `8Gi` from template (no patch override)

### Verification

```bash
# Verify Prefill Pod memory request is 16Gi (overridden by patch)
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=prefill -o jsonpath='{.items[0].spec.containers[0].resources.requests.memory}'

> 8Gi
```

```bash
# Verify Prefill Pod GPU request is still 1 (from template, not overridden by patch)
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=prefill -o jsonpath='{.items[0].spec.containers[0].resources.requests.nvidia\.com/gpu}'

> 1
```

```bash
# Verify Decode Pod memory request is 8Gi (from template, no patch override)
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=decode -o jsonpath='{.items[0].spec.containers[0].resources.requests.memory}'

> 8Gi
```

**Expected output:**
- Prefill memory: `16Gi`
- Prefill gpu: `1`
- Decode memory: `8Gi`

### Cleanup

```bash
kubectl delete rbg pd-with-templates
```

---

## Operation 3: Combining with leaderWorkerPattern

### Step 1: Create an RBG with templateRef + leaderWorkerPattern

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: agg-tp-with-templates
spec:
  roleTemplates:
    - name: engine-base
      template:
        spec:
          containers:
            - name: engine
              image: lmsysorg/sglang:v0.5.9
              resources:
                requests:
                  nvidia.com/gpu: "1"
                limits:
                  nvidia.com/gpu: "1"
              volumeMounts:
                - name: dshm
                  mountPath: /dev/shm
          volumes:
            - name: dshm
              emptyDir:
                medium: Memory
                sizeLimit: 30Gi

  roles:
    - name: backend
      replicas: 1
      leaderWorkerPattern:
        size: 2
        templateRef:
          name: engine-base
          patch:
            spec:
              containers:
                - name: engine
                  command:
                    - python3
                    - -m
                    - sglang.launch_server
                    - --model-path
                    - "Qwen/Qwen3-0.6B"
                    - --host
                    - "0.0.0.0"
                    - --port
                    - "8000"
                    - --tp-size
                    - "2"
                    - --dist-init-addr
                    - $(RBG_LWP_LEADER_ADDRESS):6379
                    - --nnodes
                    - $(RBG_LWP_GROUP_SIZE)
                    - --node-rank
                    - $(RBG_LWP_WORKER_INDEX)
        leaderTemplatePatch:
          metadata:
            labels:
              role: leader
        workerTemplatePatch:
          metadata:
            labels:
              role: worker
EOF
```

### Expected Behavior

- Template `engine-base` provides base configuration (image, resources, volumes)
- `templateRef.patch` adds startup command and tensor parallelism parameters
- `leaderTemplatePatch` adds `role: leader` label to Leader Pod
- `workerTemplatePatch` adds `role: worker` label to Worker Pod
- Configuration application order: `roleTemplates` → `templateRef.patch` → `leaderTemplatePatch` / `workerTemplatePatch`

### Verification

```bash
# Check Pod status (should see 2 Pods: Leader + Worker)
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-with-templates -o wide

> NAME                                READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> agg-tp-with-templates-backend-0-0   1/1     Running   0          48s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> agg-tp-with-templates-backend-0-1   1/1     Running   0          48s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# Verify Leader Pod labels
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-with-templates,role=leader

> NAME                                READY   STATUS    RESTARTS   AGE
> agg-tp-with-templates-backend-0-0   1/1     Running   0          50s
```

```bash
# Verify Worker Pod labels
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-with-templates,role=worker

> NAME                                READY   STATUS    RESTARTS   AGE
> agg-tp-with-templates-backend-0-1   1/1     Running   0          51s
```

```bash
# Verify both Pods use the image from the template
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-with-templates -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> agg-tp-with-templates-backend-0-0=lmsysorg/sglang:v0.5.9
> agg-tp-with-templates-backend-0-1=lmsysorg/sglang:v0.5.9
```

```bash
# Verify the command from patch has been applied
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-with-templates -o jsonpath='{.items[0].spec.containers[0].command}'

> ["python3","-m","sglang.launch_server","--model-path","Qwen/Qwen3-0.6B","--host","0.0.0.0","--port","8000","--tp-size","2","--dist-init-addr","$(RBG_LWP_LEADER_ADDRESS):6379","--nnodes","$(RBG_LWP_GROUP_SIZE)","--node-rank","$(RBG_LWP_WORKER_INDEX)"]
```

**Expected output:**
- Both Pods are Running
- 1 Pod with `role=leader` label
- 1 Pod with `role=worker` label
- Both Pods' image is `lmsysorg/sglang:v0.5.9`
- Command includes `--tp-size 2` and other parameters (from patch)

### Cleanup

```bash
kubectl delete rbg agg-tp-with-templates
```

---

## Summary

| Operation | Verification Point | Key Expectation |
| --- | --- | --- |
| Direct template reference | templateRef basic functionality | Pod uses all configuration from template |
| Patch differential override | Strategic Merge Patch merge | Image from template, command from patch, scalars can be overridden |
| Combined with leaderWorkerPattern | Three-layer configuration stacking | template → patch → leader/workerTemplatePatch applied in order |

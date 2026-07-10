# Operations Guide: Multi-Role and Role Topology Configuration

> Corresponding concept document: [1. Multi-Role and Role Topology Configuration](01-deploy-inference-service.md)

## Objectives

Validate RBG's multi-role definition and role topology configuration capabilities, including:
1. Single-role aggregated deployment (standalonePattern)
2. PD-disaggregated multi-role deployment (Router + Prefill + Decode)
3. Multi-GPU tensor parallel deployment (leaderWorkerPattern)
4. Headless Service auto-creation and DNS service discovery

## Prerequisites

- Kubernetes cluster version >= 1.24
- RBG Controller installed
- GPU nodes in the cluster (`nvidia.com/gpu` resource available)
- NVIDIA Device Plugin installed
- Images accessible: `lmsysorg/sglang:v0.5.9`, `lmsysorg/sglang-router:v0.2.4`

---

## Operation 1: Single-Role Aggregated Deployment

### Step 1: Create a Single-Role RBG

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: agg-inference
spec:
  roles:
    - name: backend
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
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
                  - "1"
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
EOF
```

### Expected Behavior

- RBG `agg-inference` created successfully
- Controller automatically creates 1 Pod (`agg-inference-backend-0`)
- Controller automatically creates Headless Service `s-agg-inference-backend`

### Verification

```bash
# Check RBG status
kubectl get rbg agg-inference

> NAME            READY   AGE
> agg-inference   True    52s
```

```bash
# Wait for Pod to be ready
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=agg-inference --timeout=300s

> pod/agg-inference-backend-0 condition met
```

```bash
# Check Pod status
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-inference -o wide

> NAME                      READY   STATUS    RESTARTS   AGE   IP            NODE                 NOMINATED NODE   READINESS GATES
> agg-inference-backend-0   1/1     Running   0          69s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# Check auto-created Headless Service
kubectl get svc -l rbg.workloads.x-k8s.io/group-name=agg-inference

> NAME                      TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)   AGE
> s-agg-inference-backend   ClusterIP   None         <none>        <none>    2m51s
```

### Step 2: Verify DNS Service Discovery

```bash
# Verify Headless Service DNS resolution from within a Pod (via health API)
kubectl exec -it agg-inference-backend-0 -- python3 -c "import urllib.request; print(urllib.request.urlopen('http://agg-inference-backend-0.s-agg-inference-backend.default.svc.cluster.local:8000/health').status)"
```

**Expected output:** HTTP status code `200`, indicating DNS resolution succeeded and the service is reachable.

### Cleanup

```bash
kubectl delete rbg agg-inference
```

---

## Operation 2: PD-Disaggregated Multi-Role Deployment

### Step 1: Create a Three-Role RBG

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
spec:
  roles:
    - name: router
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: router
                image: lmsysorg/sglang-router:v0.2.4
                command:
                  - python3
                  - -m
                  - sglang_router.launch_router
                  - --pd-disaggregation
                  - --prefill
                  - "http://pd-inference-prefill-0.s-pd-inference-prefill:8000"
                  - --decode
                  - "http://pd-inference-decode-0.s-pd-inference-decode:8000"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                ports:
                  - name: http
                    containerPort: 8000
                resources:
                  requests:
                    cpu: "2"
                    memory: "4Gi"

    - name: prefill
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
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

    - name: decode
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
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
EOF
```

### Expected Behavior

- Each of the 3 roles (router, prefill, decode) creates 1 Pod
- Controller automatically creates 3 Headless Services:
  - `s-pd-inference-router`
  - `s-pd-inference-prefill`
  - `s-pd-inference-decode`
- Router can access Prefill and Decode instances via DNS addresses

### Verification

```bash
# Check RBG status
kubectl get rbg pd-inference -o wide

> NAME           READY   AGE
> pd-inference   True    80s
```

```bash
# Check all Pods
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=pd-inference -o wide

> NAME                     READY   STATUS    RESTARTS   AGE   IP            NODE                 NOMINATED NODE   READINESS GATES
> pd-inference-decode-0    1/1     Running   0          92s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-inference-prefill-0   1/1     Running   0          92s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-inference-router-0    1/1     Running   0          92s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# Check Headless Services
kubectl get svc -l rbg.workloads.x-k8s.io/group-name=pd-inference

> NAME                     TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)   AGE
> s-pd-inference-decode    ClusterIP   None         <none>        <none>    2m19s
> s-pd-inference-prefill   ClusterIP   None         <none>        <none>    2m19s
> s-pd-inference-router    ClusterIP   None         <none>        <none>    2m19s
```

```bash
# Verify Prefill service discovery (via health API)
kubectl exec -it pd-inference-router-0 -- python3 -c "import urllib.request; print(urllib.request.urlopen('http://pd-inference-prefill-0.s-pd-inference-prefill:8000/health').status)"

> 200
```

```bash
# Verify Decode service discovery (via health API)
kubectl exec -it pd-inference-router-0 -- python3 -c "import urllib.request; print(urllib.request.urlopen('http://pd-inference-decode-0.s-pd-inference-decode:8000/health').status)"

> 200
```

**Expected output:**
- All 3 Pods are Running and Ready
- 3 Headless Services, all with ClusterIP set to None
- DNS resolution succeeds, both Prefill and Decode health APIs return `200`

### Cleanup

```bash
kubectl delete rbg pd-inference
```

---

## Operation 3: leaderWorkerPattern Tensor Parallel Deployment

### Step 1: Create a Tensor Parallel RBG (size=2)

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: agg-tp-inference
spec:
  roles:
    - name: backend
      replicas: 1
      leaderWorkerPattern:
        size: 2
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
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
EOF
```

### Expected Behavior

- 1 RoleInstance created, containing 2 Pods (1 Leader + 1 Worker)
- Each Pod requests 1 GPU, totaling 2 GPUs
- RBG automatically injects environment variables `RBG_LWP_LEADER_ADDRESS`, `RBG_LWP_GROUP_SIZE`, `RBG_LWP_WORKER_INDEX`
- Leader Pod (index=0) and Worker Pod (index=1) work together via tensor parallelism

### Verification

```bash
# Check RBG status
kubectl get rbg agg-tp-inference -o wide
```

```bash
> NAME               READY   AGE
> agg-tp-inference   True    39s
```

```bash
# Check Pods (should see 2 Pods)
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-inference -o wide
```

```bash
> NAME                           READY   STATUS    RESTARTS   AGE   IP            NODE                 NOMINATED NODE   READINESS GATES
> agg-tp-inference-backend-0-0   1/1     Running   0          48s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> agg-tp-inference-backend-0-1   1/1     Running   0          48s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# Check auto-injected environment variables
kubectl exec -it agg-tp-inference-backend-0-0 -- env | grep RBG_LWP

> RBG_LWP_WORKER_INDEX=0
> RBG_LWP_GROUP_SIZE=2
> RBG_LWP_LEADER_ADDRESS=agg-tp-inference-backend-0-0.s-agg-tp-inference-backend.default
```

```bash
kubectl exec -it agg-tp-inference-backend-0-1 -- env | grep RBG_LWP

> RBG_LWP_LEADER_ADDRESS=agg-tp-inference-backend-0-0.s-agg-tp-inference-backend.default
> RBG_LWP_WORKER_INDEX=1
> RBG_LWP_GROUP_SIZE=2
```

**Expected output:**
- 2 Pods are Running and Ready
- Leader Pod's `RBG_LWP_WORKER_INDEX=0`
- Worker Pod's `RBG_LWP_WORKER_INDEX=1`
- Both Pods have `RBG_LWP_GROUP_SIZE=2`
- `RBG_LWP_LEADER_ADDRESS` points to the Leader Pod's FQDN

### Cleanup

```bash
kubectl delete rbg agg-tp-inference
```

---

## Summary

| Operation | Verification Point | Key Expectation |
| --- | --- | --- |
| Single-role aggregated deployment | standalonePattern basic functionality | 1 Pod + 1 Headless Service |
| PD-disaggregated multi-role deployment | Multi-role collaboration + DNS service discovery | 3 Pods + 3 Headless Services + DNS resolvable |
| leaderWorkerPattern | Tensor parallelism + env var injection | 2 Pods (Leader+Worker) + RBG_LWP_* variables correct |

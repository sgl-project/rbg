# Operations Guide: Deploying Mooncake Store with RBG for KV Cache Reuse

> Corresponding concept document: [10. Deploying Mooncake Store with RBG for KV Cache Reuse](../10.%20通过%20RBG%20部署%20Mooncake%20Store%20实现%20KV%20Cache%20复用.md)

## Objective

Verify the deployment of Mooncake Store distributed KV Cache storage engine through RBG, and achieve KV Cache Offload and cross-instance reuse for inference services, including:
1. Deploy a standalone Mooncake Store service (Master + Store nodes)
2. Deploy an inference service connected to Mooncake Store
3. Verify role dependency startup ordering
4. Verify KV Cache Offload is effective
5. Verify multi-turn conversation performance improvement
6. Verify KV Cache cross-instance reuse

## Prerequisites

- Kubernetes cluster version >= 1.24
- RBG Controller installed
- Mooncake Store does not require GPUs; it only needs CPU nodes with sufficient memory
- Inference service requires GPU nodes
- Image accessible: `lmsysorg/sglang:v0.5.9`

---

## Operation 1: Deploy a Standalone Mooncake Store Service

### Step 1: Create the Mooncake Store RBG

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: mooncake-service
spec:
  roles:
    - name: mooncake-master
      replicas: 1
      standalonePattern:
        template:
          spec:
            hostNetwork: true
            dnsPolicy: ClusterFirstWithHostNet
            containers:
              - name: master
                image: &image lmsysorg/sglang:v0.5.9
                env:
                  - name: POD_IP
                    valueFrom:
                      fieldRef:
                        fieldPath: status.podIP
                command:
                  - sh
                  - -c
                  - |
                    mooncake_master --rpc_address $(POD_IP) --rpc_port 52858 \
                    --http_metadata_server_host $(POD_IP) --http_metadata_server_port 52856 \
                    --enable_http_metadata_server --metrics_port 52857
                ports:
                  - containerPort: 52856
                    name: http
                readinessProbe:
                  initialDelaySeconds: 10
                  periodSeconds: 10
                  tcpSocket:
                    port: 52856
    - name: mooncake-store
      dependencies: [ "mooncake-master" ]
      replicas: 1
      standalonePattern:
        template:
          spec:
            hostNetwork: true
            dnsPolicy: ClusterFirstWithHostNet
            containers:
              - name: store
                image: *image
                securityContext:
                  capabilities:
                    add: ["IPC_LOCK"]
                  privileged: true
                env:
                  - name: POD_IP
                    valueFrom:
                      fieldRef:
                        fieldPath: status.podIP
                  - name: MOONCAKE_LOCAL_HOSTNAME
                    value: $(POD_IP)
                  - name: MOONCAKE_MASTER
                    value: "s-mooncake-service-mooncake-master:52858"
                  - name: MOONCAKE_TE_META_DATA_SERVER
                    value: "http://s-mooncake-service-mooncake-master:52856/metadata"
                  - name: MOONCAKE_GLOBAL_SEGMENT_SIZE
                    value: "100gb"
                  - name: MOONCAKE_LOCAL_BUFFER_SIZE
                    value: "4194304"
                  - name: MOONCAKE_PROTOCOL
                    value: "rdma" #support tcp, rdma
                  - name: MOONCAKE_DEVICE
                    value: ""
                  - name: MC_GID_INDEX
                    value: "3"
                command:
                  - sh
                  - -c
                  - "ulimit -l unlimited && python -m mooncake.mooncake_store_service --port 52859"
                ports:
                  - containerPort: 52859
                    name: http
                readinessProbe:
                  initialDelaySeconds: 10
                  periodSeconds: 10
                  tcpSocket:
                    port: 52859
                resources:
                  limits:
                    memory: "128Gi"
                    rdma/hca: "1"
                  requests:
                    memory: "128Gi"
                    rdma/hca: "1"
EOF
```

### Expected Behavior

1. **mooncake-master** role has no dependencies, starts first
2. **mooncake-store** role declares `dependencies: ["mooncake-master"]`, waits for Master to be ready before starting
3. After Store nodes start, they register with the Master and contribute memory to the global storage pool
4. Total L3 cache capacity = 3 replicas × 10 GB = 30 GB
5. Controller automatically creates Headless Services:
   - `s-mooncake-service-mooncake-master`
   - `s-mooncake-service-mooncake-store`

### Verification

```bash
# Check RBG status
kubectl get rbg mooncake-service

> NAME               READY   AGE
> mooncake-service   True    2m53s
```

```bash
# Check all Pod statuses
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=mooncake-service -o wide

> NAME                                 READY   STATUS    RESTARTS   AGE     IP            NODE                 NOMINATED NODE   READINESS GATES
> mooncake-service-mooncake-master-0   1/1     Running   0          2m18s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> mooncake-service-mooncake-store-0    1/1     Running   0          98s     10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# Check Master logs to confirm Store nodes have joined the storage pool
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master | tail -2

> I0709 06:37:43.677397    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 0 B / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 0 (soft-pinned: 0) | Clients: 1 | Requests (Success/Total): PutStart=0/0, PutEnd=0/0, PutRevoke=0/0, Get=0/0, Exist=0/0, Del=0/0, DelAll=0/0, Ping=64/64, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=0/0/0, Item=0/0), PutEnd:(Req=0/0/0, Item=0/0), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=0/0/0, Item=0/0), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask:(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 06:37:53.677528    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 0 B / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 0 (soft-pinned: 0) | Clients: 1 | Requests (Success/Total): PutStart=0/0, PutEnd=0/0, PutRevoke=0/0, Get=0/0, Exist=0/0, Del=0/0, DelAll=0/0, Ping=74/74, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=0/0/0, Item=0/0), PutEnd:(Req=0/0/0, Item=0/0), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=0/0/0, Item=0/0), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask:(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
```

**Expected output:**
- 1 Master Pod, Running and Ready
- 1 Store Pod, Running and Ready
- Master logs show storage pool capacity: `Storage: 0 B / 100.00 GB (0.0%)`

### Step 2: Verify Startup Ordering

```bash
# Check Pod creation timestamps to confirm Master was created before Store
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=mooncake-service \
  -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.metadata.creationTimestamp}{"\n"}{end}'

> mooncake-service-mooncake-master-0=2026-07-09T06:34:11Z
> mooncake-service-mooncake-store-0=2026-07-09T06:36:39Z
```

**Expected output:** Master Pod's creation timestamp is earlier than Store Pod's (dependency relationship is effective).

---

## Operation 2: Deploy Inference Service Connected to Mooncake Store

### Step 1: Create the Inference Service RBG

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inference-with-mooncake
spec:
  roles:
    - name: worker
      replicas: 1
      standalonePattern:
        template:
          spec:
            hostNetwork: true
            dnsPolicy: ClusterFirstWithHostNet
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                env:
                  - name: POD_IP
                    valueFrom:
                      fieldRef:
                        fieldPath: status.podIP
                  - name: MOONCAKE_LOCAL_HOSTNAME
                    value: $(POD_IP)
                  - name: MOONCAKE_MASTER
                    value: "s-mooncake-service-mooncake-master:52858"
                  - name: MOONCAKE_TE_META_DATA_SERVER
                    value: "http://s-mooncake-service-mooncake-master:52856/metadata"
                  - name: MOONCAKE_GLOBAL_SEGMENT_SIZE
                    value: "0"
                  - name: MOONCAKE_LOCAL_BUFFER_SIZE
                    value: "4194304"
                  - name: MOONCAKE_PROTOCOL
                    value: "rdma"
                  - name: MOONCAKE_DEVICE
                    value: ""
                  - name: MC_GID_INDEX
                    value: "3"
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
                  - --enable-hierarchical-cache
                  - --hicache-storage-backend
                  - mooncake
                ports:
                  - containerPort: 8000
                    name: http
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                    rdma/hca: 1
                  limits:
                    nvidia.com/gpu: "1"
                    rdma/hca: 1
---
apiVersion: v1
kind: Service
metadata:
  name: inference-with-mooncake
spec:
  selector:
    rbg.workloads.x-k8s.io/group-name: inference-with-mooncake
  ports:
    - port: 8000
      targetPort: 8000
EOF
```

### Expected Behavior

- The inference engine enables hierarchical cache via `--enable-hierarchical-cache` and `--hicache-storage-backend mooncake`
- The engine connects to Mooncake Store via `MOONCAKE_MASTER` and `MOONCAKE_TE_META_DATA_SERVER` environment variables
- The engine uses the Headless Service DNS address (`s-mooncake-service-mooncake-master`) to connect to the Master

### Verification

```bash
# Wait for inference engine to be ready
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake --timeout=600s

# Check Pod status
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake

# Check engine logs to confirm Mooncake connection succeeded
kubectl logs -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake -c engine | grep -i mooncake

> [2026-07-09 07:18:17] Mooncake store warmup successfully.
```

**Expected output:**
- Inference engine Pod Running and Ready
- Logs show Mooncake connection success message

---

## Operation 3: Verify KV Cache Offload

### Step 1: Send Multi-turn Conversation Requests

```bash
# Port forward to the inference service
kubectl port-forward svc/inference-with-mooncake 8000:8000 &

# First turn
curl -s http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Qwen/Qwen3-0.6B",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Explain the theory of relativity in detail."}
    ],
    "max_tokens": 200
  }'

# Second turn (reuses first turn's KV Cache)
curl -s http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Qwen/Qwen3-0.6B",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Explain the theory of relativity in detail."},
      {"role": "assistant", "content": "The theory of relativity consists of two parts..."},
      {"role": "user", "content": "Can you give a real-world example?"}
    ],
    "max_tokens": 200
  }'
```

### Step 2: Verify KV Cache Has Been Offloaded

```bash
# Check Master logs to confirm KV Cache has been offloaded to the storage pool
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master | tail -5

> I0709 07:22:03.712014    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 4.00 KB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 1 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=3131/3131, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=0/0/0, Item=0/0), PutEnd:(Req=0/0/0, Item=0/0), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=0/0/0, Item=0/0), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask:(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:22:13.712157    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 4.00 KB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 1 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=3151/3151, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=0/0/0, Item=0/0), PutEnd:(Req=0/0/0, Item=0/0), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=0/0/0, Item=0/0), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask=(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:22:23.712291    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 1.54 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 29 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=3171/3171, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=2/0/2, Item=28/28), PutEnd:(Req=2/0/2, Item=28/28), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=2/0/2, Item=28/28), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask=(Req=0/0), CreateCopyTask=(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:22:33.712416    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 26.58 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 487 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=3191/3191, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=5/0/5, Item=486/486), PutEnd:(Req=5/0/5, Item=486/486), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=5/0/5, Item=486/486), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask=(Req=0/0), CreateCopyTask=(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:22:43.712549    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 26.58 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 487 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=3211/3211, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=5/0/5, Item=486/486), PutEnd:(Req=5/0/5, Item=486/486), PutRevoke=(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=5/0/5, Item=486/486), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask=(Req=0/0), CreateCopyTask=(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
```

### Expected Behavior

- Before requests: `Mem Storage: 4.00 KB / 100.00 GB`
- After requests: `Mem Storage: 26.58 MB / 100.00 GB (0.0%)`
- The increase in `Storage` and `Keys` indicates KV Cache has been successfully offloaded to Mooncake Store

---

## Operation 4: Verify KV Cache Cross-Instance Reuse

### Step 1: Scale Inference Engine to 2 Replicas

```bash
kubectl patch rbg inference-with-mooncake --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/replicas", "value": 2}]'
```

### Step 2: Wait for New Replica to Be Ready

```bash
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake --timeout=600s
```

### Step 3: Send Requests with the Same Prefix to Different Instances

```bash
# Get addresses of both instances
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'

> inference-with-mooncake-worker-0
> inference-with-mooncake-worker-1

# Send the same request to the new instance
kubectl port-forward inference-with-mooncake-worker-1 8000:8000

curl -s http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Qwen/Qwen3-0.6B",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Explain the theory of relativity in detail."},
      {"role": "assistant", "content": "The theory of relativity consists of two parts..."},
      {"role": "user", "content": "Can you give a real-world example?"}
    ],
    "max_tokens": 200
  }'
```

### Step 4: Verify KV Cache Sharing

```bash
# Check Keys count changes in Master logs
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master | tail -5

> I0709 07:32:33.721189    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 26.58 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 487 (soft-pinned: 0) | Clients: 1 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=4228/4228, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=5/0/5, Item=486/486), PutEnd:(Req=5/0/5, Item=486/486), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=5/0/5, Item=486/486), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask=(Req=0/0), CreateCopyTask=(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:32:43.721324    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 26.58 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 487 (soft-pinned: 0) | Clients: 1 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=4238/4238, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=5/0/5, Item=486/486), PutEnd:(Req=5/0/5, Item=486/486), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=5/0/5, Item=486/486), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask=(Req=0/0), CreateCopyTask=(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:32:53.721458    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 26.58 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 487 (soft-pinned: 0) | Clients: 1 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=4248/4248, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=5/0/5, Item=486/486), PutEnd:(Req=5/0/5, Item=486/486), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=5/0/5, Item=486/486), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask=(Req=0/0), CreateCopyTask=(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:33:03.721589    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 26.59 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 488 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=9/9, PutEnd=7/7, PutRevoke=2/2, Get=7/7, Exist=7/7, Del=0/0, DelAll=0/0, Ping=4264/4264, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=5/0/5, Item=486/486), PutEnd:(Req=5/0/5, Item=486/486), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=5/0/5, Item=486/486), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask=(Req=0/0), CreateCopyTask=(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:33:13.721725    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 26.59 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 488 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=9/9, PutEnd=7/7, PutRevoke=2/2, Get=7/7, Exist=7/7, Del=0/0, DelAll=0/0, Ping=4284/4284, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=5/0/5, Item=486/486), PutEnd:(Req=5/0/5, Item=486/486), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=5/0/5, Item=486/486), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask=(Req=0/0), CreateCopyTask=(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
```

### Expected Behavior

- If both instances compute KV Cache independently, the Keys count should double
- If KV Cache is shared, the Keys count changes minimally, indicating the second instance reused the first instance's KV Cache

---

## Cleanup

```bash
# Delete inference service
kubectl delete rbg inference-with-mooncake
kubectl delete svc inference-with-mooncake

# Delete Mooncake Store service
kubectl delete rbg mooncake-service
```

---

## Summary

| Operation | Verification Point | Key Expected Result |
| --- | --- | --- |
| Deploy Mooncake Store | Role dependency + service discovery | Master starts first, Store starts after, storage pool 30 GB |
| Inference service connects to Mooncake | HiCache integration | Engine connects to Master via DNS, hierarchical cache enabled |
| KV Cache Offload | KV Cache uploaded to storage pool | Master logs show non-zero Storage and Keys |
| Benchmark | Performance improvement | Multi-turn TTFT reduced by 90%+, throughput increased by 40%+ |
| Cross-instance reuse | KV Cache sharing | Keys count does not double |
| Lossless update | In-place upgrade preserves KV Cache | Pod restarts in-place, KV Cache data preserved |

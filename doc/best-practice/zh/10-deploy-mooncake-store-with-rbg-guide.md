# 操作文档：通过 RBG 部署 Mooncake Store 实现 KV Cache 复用

> 对应概念文档：[10. 通过 RBG 部署 Mooncake Store 实现 KV Cache 复用](../10.%20通过%20RBG%20部署%20Mooncake%20Store%20实现%20KV%20Cache%20复用.md)

## 目标

验证通过 RBG 部署 Mooncake Store 分布式 KV Cache 存储引擎，并实现推理服务的 KV Cache Offload 和跨实例复用，包括：
1. 部署独立的 Mooncake Store 服务（Master + Store 节点）
2. 部署推理服务连接 Mooncake Store
3. 验证角色依赖启动顺序
4. 验证 KV Cache Offload 生效
5. 验证多轮对话性能提升
6. 验证 KV Cache 跨实例复用

## 前提条件

- Kubernetes 集群版本 >= 1.24
- 已安装 RBG Controller
- Mooncake Store 不需要 GPU，仅需 CPU 节点和充足内存
- 推理服务需要 GPU 节点
- 镜像可访问：`lmsysorg/sglang:v0.5.9`

---

## 操作一：部署独立的 Mooncake Store 服务

### 步骤 1：创建 Mooncake Store RBG

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

### 预期行为

1. **mooncake-master** 角色无依赖，最先启动
2. **mooncake-store** 角色声明 `dependencies: ["mooncake-master"]`，等待 Master 就绪后启动
3. 3 个 Store 节点启动后向 Master 注册，贡献内存到全局存储池
4. 总 L3 缓存容量 = 3 副本 × 10 GB = 30 GB
5. Controller 自动创建 Headless Service：
   - `s-mooncake-service-mooncake-master`
   - `s-mooncake-service-mooncake-store`

### 验证

```bash
# 查看 RBG 状态
kubectl get rbg mooncake-service

> NAME               READY   AGE
> mooncake-service   True    2m53s
```

```bash
# 查看所有 Pod 状态
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=mooncake-service -o wide

> NAME                                 READY   STATUS    RESTARTS   AGE     IP            NODE                 NOMINATED NODE   READINESS GATES
> mooncake-service-mooncake-master-0   1/1     Running   0          2m18s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> mooncake-service-mooncake-store-0    1/1     Running   0          98s     10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# 查看 Master 日志，确认 Store 节点已加入存储池
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master | tail -2

> I0709 06:37:43.677397    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 0 B / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 0 (soft-pinned: 0) | Clients: 1 | Requests (Success/Total): PutStart=0/0, PutEnd=0/0, PutRevoke=0/0, Get=0/0, Exist=0/0, Del=0/0, DelAll=0/0, Ping=64/64, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=0/0/0, Item=0/0), PutEnd:(Req=0/0/0, Item=0/0), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=0/0/0, Item=0/0), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask:(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 06:37:53.677528    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 0 B / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 0 (soft-pinned: 0) | Clients: 1 | Requests (Success/Total): PutStart=0/0, PutEnd=0/0, PutRevoke=0/0, Get=0/0, Exist=0/0, Del=0/0, DelAll=0/0, Ping=74/74, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=0/0/0, Item=0/0), PutEnd:(Req=0/0/0, Item=0/0), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=0/0/0, Item=0/0), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask:(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
```

**预期输出：**
- Master Pod 1 个，Running 且 Ready
- Store Pod 1 个，Running 且 Ready
- Master 日志中显示存储池容量：`Storage: 0 B / 100.00 GB (0.0%)`

### 步骤 2：验证启动顺序

```bash
# 查看 Pod 创建时间戳，确认 Master 先于 Store 创建
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=mooncake-service \
  -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.metadata.creationTimestamp}{"\n"}{end}'

> mooncake-service-mooncake-master-0=2026-07-09T06:34:11Z
> mooncake-service-mooncake-store-0=2026-07-09T06:36:39Z
```

**预期输出：** Master Pod 的创建时间戳早于 Store Pod（依赖关系生效）。

---

## 操作二：部署推理服务连接 Mooncake Store

### 步骤 1：创建推理服务 RBG

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

### 预期行为

- 推理引擎启动时通过 `--enable-hierarchical-cache` 和 `--hicache-storage-backend mooncake` 启用分层缓存
- 引擎通过 `MOONCAKE_MASTER` 和 `MOONCAKE_TE_META_DATA_SERVER` 环境变量连接 Mooncake Store
- 引擎使用 Headless Service DNS 地址（`s-mooncake-service-mooncake-master`）连接 Master

### 验证

```bash
# 等待推理引擎就绪
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake --timeout=600s

# 查看 Pod 状态
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake

# 查看引擎日志，确认 Mooncake 连接成功
kubectl logs -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake -c engine | grep -i mooncake

> [2026-07-09 07:18:17] Mooncake store warmup successfully.
```

**预期输出：**
- 推理引擎 Pod Running 且 Ready
- 日志中显示 Mooncake 连接成功的信息

---

## 操作三：验证 KV Cache Offload

### 步骤 1：发送多轮对话请求

```bash
# 端口转发到推理服务
kubectl port-forward svc/inference-with-mooncake 8000:8000 &

# 第一轮对话
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

# 第二轮对话（复用第一轮的 KV Cache）
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

### 步骤 2：验证 KV Cache 已 Offload

```bash
# 查看 Master 日志，确认 KV Cache 已被 offload 到存储池
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master | tail -5

> I0709 07:22:03.712014    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 4.00 KB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 1 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=3131/3131, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=0/0/0, Item=0/0), PutEnd:(Req=0/0/0, Item=0/0), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=0/0/0, Item=0/0), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask:(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:22:13.712157    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 4.00 KB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 1 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=3151/3151, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=0/0/0, Item=0/0), PutEnd:(Req=0/0/0, Item=0/0), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=0/0/0, Item=0/0), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask:(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:22:23.712291    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 1.54 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 29 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=3171/3171, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=2/0/2, Item=28/28), PutEnd:(Req=2/0/2, Item=28/28), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=2/0/2, Item=28/28), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask:(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:22:33.712416    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 26.58 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 487 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=3191/3191, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=5/0/5, Item=486/486), PutEnd:(Req=5/0/5, Item=486/486), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=5/0/5, Item=486/486), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask:(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
> I0709 07:22:43.712549    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 26.58 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 487 (soft-pinned: 0) | Clients: 2 | Requests (Success/Total): PutStart=8/8, PutEnd=6/6, PutRevoke=2/2, Get=6/6, Exist=6/6, Del=0/0, DelAll=0/0, Ping=3211/3211, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=5/0/5, Item=486/486), PutEnd:(Req=5/0/5, Item=486/486), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=5/0/5, Item=486/486), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask:(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
```

### 预期行为

- 请求前：`Mem Storage: 4.00 KB / 100.00 GB`
- 请求后：`Mem Storage: 26.58 MB / 100.00 GB (0.0%)`
- `Storage` 和 `Keys` 的增加说明 KV Cache 已成功 offload 到 Mooncake Store

---

## 操作四：验证 KV Cache 跨实例复用

### 步骤 1：扩容推理引擎到 2 副本

```bash
kubectl patch rbg inference-with-mooncake --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/replicas", "value": 2}]'
```

### 步骤 2：等待新副本就绪

```bash
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake --timeout=600s
```

### 步骤 3：向不同实例发送相同前缀的请求

```bash
# 获取两个实例的地址
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inference-with-mooncake -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'

> inference-with-mooncake-worker-0
> inference-with-mooncake-worker-1

# 向新实例发送同样的请求
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

### 步骤 4：验证 KV Cache 共享

```bash
# 查看 Master 日志中 Keys 数量变化
kubectl logs -l rbg.workloads.x-k8s.io/role-name=mooncake-master -c master | tail -1

> I0709 08:00:59.104270    30 rpc_service.cpp:41] Master Metrics: Mem Storage: 48.46 MB / 100.00 GB (0.0%) | SSD Storage: 0 B / 0 B | Keys: 888 (soft-pinned: 0) | Clients: 3 | Requests (Success/Total): PutStart=2/2, PutEnd=2/2, PutRevoke=0/0, Get=2/2, Exist=2/2, Del=0/0, DelAll=0/0, Ping=928/928, CopyStart=0/0, CopyEnd=0/0, CopyRevoke=0/0, MoveStart=0/0, MoveEnd=0/0, MoveRevoke=0/0 | Batch Requests (Req=Success/PartialSuccess/Total, Item=Success/Total): PutStart:(Req=7/0/7, Item=886/886), PutEnd:(Req=7/0/7, Item=886/886), PutRevoke:(Req=0/0/0, Item=0/0), Get:(Req=0/0/0, Item=0/0), ExistKey:(Req=7/0/7, Item=886/886), QueryIp:(Req=0/0/0, Item=0/0), Clear:(Req=0/0/0, Item=0/0), CreateMoveTask:(Req=0/0), CreateCopyTask:(Req=0/0), QueryTask=(Req=0/0), FetchTasks=(Req=0/0), MarkTaskToComplete= (Req=0/0),  | Eviction: Success/Attempts=0/0, keys=0, size=0 B | Discard: Released/Total=0/0, StagingSize=0 B
```

### 预期行为

- 如果两个实例独立计算 KV Cache，Keys 数量应成倍增长
- 如果 KV Cache 被共享，Keys 没有翻倍，说明第二个实例复用了第一个实例的 KV Cache

---

## 清理

```bash
# 删除推理服务
kubectl delete rbg inference-with-mooncake
kubectl delete svc inference-with-mooncake

# 删除 Mooncake Store 服务
kubectl delete rbg mooncake-service
```

---

## 总结

| 操作 | 验证点 | 关键预期 |
| --- | --- | --- |
| 部署 Mooncake Store | 角色依赖 + 服务发现 | Master 先启动，Store 后启动，存储池 30 GB |
| 推理服务连接 Mooncake | HiCache 集成 | 引擎通过 DNS 连接 Master，分层缓存启用 |
| KV Cache Offload | KV Cache 上传到存储池 | Master 日志显示 Storage 和 Keys 非零 |

# 操作文档：多角色、角色拓扑配置

> 对应概念文档：[1. 多角色、角色拓扑配置](01-deploy-inference-service.md)

## 目标

验证 RBG 的多角色定义和角色拓扑配置能力，包括：
1. 单角色聚合部署（standalonePattern）
2. PD 分离多角色部署（Router + Prefill + Decode）
3. 多 GPU 张量并行部署（leaderWorkerPattern）
4. Headless Service 自动创建与 DNS 服务发现

## 前提条件

- Kubernetes 集群版本 >= 1.24
- 已安装 RBG Controller
- 集群中包含 GPU 节点（`nvidia.com/gpu` 资源可用）
- 已安装 NVIDIA Device Plugin
- 镜像可访问：`lmsysorg/sglang:v0.5.9`、`lmsysorg/sglang-router:v0.2.4`

---

## 操作一：单角色聚合部署

### 步骤 1：创建单角色 RBG

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

### 预期行为

- RBG `agg-inference` 创建成功
- Controller 自动创建 1 个 Pod（`agg-inference-backend-0`）
- Controller 自动创建 Headless Service `s-agg-inference-backend`

### 验证

```bash
# 查看 RBG 状态
kubectl get rbg agg-inference

> NAME            READY   AGE
> agg-inference   True    52s
```

```bash
# 等待 Pod 就绪
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=agg-inference --timeout=300s

> pod/agg-inference-backend-0 condition met
```

```bash
# 查看 Pod 状态
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-inference -o wide

> NAME                      READY   STATUS    RESTARTS   AGE   IP            NODE                 NOMINATED NODE   READINESS GATES
> agg-inference-backend-0   1/1     Running   0          69s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# 查看自动创建的 Headless Service
kubectl get svc -l rbg.workloads.x-k8s.io/group-name=agg-inference

> NAME                      TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)   AGE
> s-agg-inference-backend   ClusterIP   None         <none>        <none>    2m51s
```

### 步骤 2：验证 DNS 服务发现

```bash
# 从 Pod 内验证 Headless Service DNS 解析（通过 health API）
kubectl exec -it agg-inference-backend-0 -- python3 -c "import urllib.request; print(urllib.request.urlopen('http://agg-inference-backend-0.s-agg-inference-backend.default.svc.cluster.local:8000/health').status)"
```

**预期输出：** HTTP 状态码 `200`，表示 DNS 解析成功且服务可达。

### 清理

```bash
kubectl delete rbg agg-inference
```

---

## 操作二：PD 分离多角色部署

### 步骤 1：创建三角色 RBG

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

### 预期行为（PD 分离部署）

- 3 个角色（router、prefill、decode）各自创建 1 个 Pod
- Controller 自动创建 3 个 Headless Service：
  - `s-pd-inference-router`
  - `s-pd-inference-prefill`
  - `s-pd-inference-decode`
- Router 可通过 DNS 地址访问 Prefill 和 Decode 实例

### 验证（PD 分离部署）

```bash
# 查看 RBG 状态
kubectl get rbg pd-inference -o wide

> NAME           READY   AGE
> pd-inference   True    80s
```

```bash
# 查看所有 Pod
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=pd-inference -o wide

> NAME                     READY   STATUS    RESTARTS   AGE   IP            NODE                 NOMINATED NODE   READINESS GATES
> pd-inference-decode-0    1/1     Running   0          92s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-inference-prefill-0   1/1     Running   0          92s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-inference-router-0    1/1     Running   0          92s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# 查看 Headless Service
kubectl get svc -l rbg.workloads.x-k8s.io/group-name=pd-inference

> NAME                     TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)   AGE
> s-pd-inference-decode    ClusterIP   None         <none>        <none>    2m19s
> s-pd-inference-prefill   ClusterIP   None         <none>        <none>    2m19s
> s-pd-inference-router    ClusterIP   None         <none>        <none>    2m19s
```

```bash
# 验证 Prefill 服务发现（通过 health API）
kubectl exec -it pd-inference-router-0 -- python3 -c "import urllib.request; print(urllib.request.urlopen('http://pd-inference-prefill-0.s-pd-inference-prefill:8000/health').status)"

> 200
```

```bash
# 验证 Decode 服务发现（通过 health API）
kubectl exec -it pd-inference-router-0 -- python3 -c "import urllib.request; print(urllib.request.urlopen('http://pd-inference-decode-0.s-pd-inference-decode:8000/health').status)"

> 200
```

**预期输出：**
- 3 个 Pod 全部 Running 且 Ready
- 3 个 Headless Service，ClusterIP 均为 None
- DNS 解析成功，Prefill 和 Decode 的 health API 均返回 `200`

### 清理（PD 分离部署）

```bash
kubectl delete rbg pd-inference
```

---

## 操作三：leaderWorkerPattern 张量并行部署

### 步骤 1：创建张量并行 RBG（size=2）

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

### 预期行为（张量并行部署）

- 创建 1 个 RoleInstance，包含 2 个 Pod（1 Leader + 1 Worker）
- 每个 Pod 请求 1 张 GPU，总共使用 2 张 GPU
- RBG 自动注入环境变量 `RBG_LWP_LEADER_ADDRESS`、`RBG_LWP_GROUP_SIZE`、`RBG_LWP_WORKER_INDEX`
- Leader Pod（index=0）和 Worker Pod（index=1）通过张量并行协同工作

### 验证（张量并行部署）

```bash
# 查看 RBG 状态
kubectl get rbg agg-tp-inference -o wide
```

```bash
> NAME               READY   AGE
> agg-tp-inference   True    39s
```

```bash
# 查看 Pod（应看到 2 个 Pod）
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-inference -o wide
```

```bash
> NAME                           READY   STATUS    RESTARTS   AGE   IP            NODE                 NOMINATED NODE   READINESS GATES
> agg-tp-inference-backend-0-0   1/1     Running   0          48s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> agg-tp-inference-backend-0-1   1/1     Running   0          48s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# 查看自动注入的环境变量
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

**预期输出：**
- 2 个 Pod 处于 Running 且 Ready
- Leader Pod 的 `RBG_LWP_WORKER_INDEX=0`
- Worker Pod 的 `RBG_LWP_WORKER_INDEX=1`
- 两个 Pod 的 `RBG_LWP_GROUP_SIZE=2`
- `RBG_LWP_LEADER_ADDRESS` 指向 Leader Pod 的 FQDN

### 清理（张量并行部署）

```bash
kubectl delete rbg agg-tp-inference
```

---

## 总结

| 操作 | 验证点 | 关键预期 |
| --- | --- | --- |
| 单角色聚合部署 | standalonePattern 基本功能 | 1 Pod + 1 Headless Service |
| PD 分离多角色部署 | 多角色协作 + DNS 服务发现 | 3 Pod + 3 Headless Service + DNS 可解析 |
| leaderWorkerPattern | 张量并行 + 环境变量注入 | 2 Pod（Leader+Worker）+ RBG_LWP_* 变量正确 |

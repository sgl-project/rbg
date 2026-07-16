# 操作文档：端口分配与服务发现

> 对应概念文档：[5. 端口分配与服务发现](05-port-allocation-and-service-discovery.md)

## 目标

验证 RBG 提供的三层服务发现机制，包括：
1. Headless Service + DNS：Pod 通过稳定 DNS 名称互相访问
2. 环境变量 + ConfigMap：Pod 运行时获取角色信息和集群拓扑
3. 端口分配 + 组件发现：`CustomComponentsPattern` 场景下动态分配端口并发现其他组件的地址和端口

## 前提条件

- Kubernetes 集群版本 >= 1.24
- 已安装 RBG Controller
- 镜像可访问：`alpine:3.23.5`
- 操作三、操作四需要 Controller 启动时开启 `--enable-port-allocator=true`（默认关闭），请提前确认或重新部署 Controller

> **说明**：本文档使用 `sleep 3600` 作为占位命令，专注于验证 RBG 服务发现控制面行为，无需 GPU。如需测试真实推理功能，请替换为完整的推理引擎启动命令。

---

## 操作一：Headless Service 与 DNS 服务发现

### 步骤 1：创建两角色 RBG

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

### 预期行为

- Controller 自动创建两个 Headless Service：`s-pd-inference-prefill`、`s-pd-inference-decode`
- 每个 Pod 获得稳定的 DNS 名称：`{rbgName}-{roleName}-{index}.{svcName}.{namespace}.svc.cluster.local`

### 验证

```bash
# 查看自动创建的 Headless Service
kubectl get svc -n default -l rbg.workloads.x-k8s.io/group-name=pd-inference

> NAME                       TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)   AGE
> s-pd-inference-decode      ClusterIP   None         <none>        <none>    7s
> s-pd-inference-prefill     ClusterIP   None         <none>        <none>    7s
```

```bash
# 查看创建的 Pod
kubectl get po -n default -l rbg.workloads.x-k8s.io/group-name=pd-inference -o wide

> NAME                     READY   STATUS    RESTARTS   AGE   IP            NODE                 NOMINATED NODE   READINESS GATES
> pd-inference-decode-0    1/1     Running   0          32s   10.xx.xx.11   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-inference-decode-1    1/1     Running   0          32s   10.xx.xx.12   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-inference-prefill-0   1/1     Running   0          32s   10.xx.xx.13   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-inference-prefill-1   1/1     Running   0          32s   10.xx.xx.14   e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# 从 decode-0 内解析 prefill-0 的 DNS 名称
kubectl exec -n default pd-inference-decode-0 -- \
  getent hosts pd-inference-prefill-0.s-pd-inference-prefill.default.svc.cluster.local

> 10.xx.xx.13    pd-inference-prefill-0.s-pd-inference-prefill.default.svc.cluster.local
```

**预期输出：**
- 两个 Headless Service 的 `CLUSTER-IP` 均为 `None`
- DNS 名称可以正确解析为对应 Pod 的 IP

### 清理

```bash
kubectl delete rbg -n default pd-inference
```

---

## 操作二：环境变量与 ConfigMap 集群拓扑

### 步骤 1：复用操作一的 RBG（或重新创建）

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

### 预期行为

- 每个 Pod 自动注入 `RBG_GROUP_NAME`、`RBG_ROLE_NAME` 等环境变量
- Controller 自动创建与 RBG 同名的 ConfigMap，并挂载到 `/etc/rbg/config.yaml`

### 验证

```bash
# 查看 Pod 中注入的环境变量
kubectl exec -n default pd-inference-prefill-0 -- env | grep RBG_

> RBG_GROUP_NAME=pd-inference
> RBG_ROLE_NAME=prefill
> ...
```

```bash
# 查看自动创建的 ConfigMap
kubectl get cm -n default pd-inference -o yaml
```

```bash
# 查看 Pod 中挂载的 ConfigMap 内容
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

**预期输出：**
- 环境变量正确反映 Pod 所属的 RBG 和角色
- ConfigMap 中包含两个角色的完整地址和端口拓扑

### 步骤 2：扩容 decode 角色，观察 ConfigMap 自动更新

```bash
kubectl patch rbg -n default pd-inference --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/1/replicas", "value": 3}]'
```

### 验证

```bash
# 确认 ConfigMap 中 decode 的 size 和 instances 已更新为 3，且无需重启 Pod
# 由于挂载的 ConfigMap 在容器内存在一定的更新延迟，可能无法立即看到内容的变化，需要等待一小段时间
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

**预期输出：** ConfigMap 内容随扩容自动更新，已存在 Pod 无需重启

### 清理

```bash
kubectl delete rbg -n default pd-inference
```

---

## 操作三：端口分配（PodScoped + RoleScoped）

### 步骤 1：创建带端口分配注解的 RBG

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

### 预期行为

- `leader` 组件使用 `PodScoped`：每个 leader 实例分配不同的 `LEADER_GRPC_PORT`（本示例只有 1 个实例）
- `worker` 组件使用 `RoleScoped`：所有 worker 实例共享同一个 `WORKER_GRPC_PORT`
- `worker` 通过 `references` 引用 `leader` 的 `PodScoped` 端口（始终引用目标组件的第一个实例，即 `leader-0`）

### 验证

```bash
# 查看 leader 的端口分配（PodScoped）
kubectl exec -n default port-allocation-demo-prefill-0-leader-0 -- env | grep LEADER_GRPC_PORT

> LEADER_GRPC_PORT=32423
```

```bash
# 查看两个 worker 的端口分配（RoleScoped，所有 worker 共享同一个 WORKER_GRPC_PORT）
kubectl exec -n default port-allocation-demo-prefill-0-worker-0 -- env | grep -E 'WORKER_GRPC_PORT|LEADER_GRPC_PORT'

> WORKER_GRPC_PORT=31775
> LEADER_GRPC_PORT=32423

kubectl exec -n default port-allocation-demo-prefill-0-worker-1 -- env | grep -E 'WORKER_GRPC_PORT|LEADER_GRPC_PORT'

> WORKER_GRPC_PORT=31775
> LEADER_GRPC_PORT=32423
```

**预期输出：**
- 两个 worker 的 `WORKER_GRPC_PORT` 相同（RoleScoped 共享同一个值）
- 两个 worker 的 `LEADER_GRPC_PORT` 相同，且与 `leader-0` 自身的端口一致（引用目标组件的第一个实例）

### 清理

```bash
kubectl delete rbg -n default port-allocation-demo
```

---

## 操作四：组件发现（Component Discovery）

### 步骤 1：在操作三基础上增加 router 组件，发现 leader 和 worker 的地址与端口

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

### 预期行为

- `router` 组件不做端口分配，仅通过 `component-discovery` 注解发现 `leader` 和 `worker` 的地址与端口
- 地址注入为完整 FQDN，端口注入为对应组件已分配的端口值

### 验证

```bash
# 查看 router 中注入的地址和端口环境变量
kubectl exec -n default component-discovery-demo-prefill-0-router-0 -- env | grep -E 'LEADER_|WORKER_'

> LEADER_GRPC_PORT=32562
> LEADER_ADDR=component-discovery-demo-prefill-0-leader-0.s-component-discovery-demo-prefill.default.svc.cluster.local
> WORKER_GRPC_PORT=33062
> WORKER_0_ADDR=component-discovery-demo-prefill-0-worker-0.s-component-discovery-demo-prefill.default.svc.cluster.local
> WORKER_1_ADDR=component-discovery-demo-prefill-0-worker-1.s-component-discovery-demo-prefill.default.svc.cluster.local
```

**预期输出：**
- `router` 能够获取 `leader` 和两个 `worker` 的 FQDN 地址
- `router` 获取的 `LEADER_GRPC_PORT` 与 `leader-0` 自身分配到的端口一致
- `router` 获取的 `WORKER_GRPC_PORT` 与 `worker` 组件的 RoleScoped 共享端口一致（两个 worker 端口值相同）

### 清理

```bash
kubectl delete rbg -n default component-discovery-demo
```

---

## 总结

| 操作 | 验证点 | 关键预期 |
| --- | --- | --- |
| Headless Service + DNS | Service CLUSTER-IP / DNS 解析 | 每个 Pod 拥有稳定 DNS 名称，Headless Service 无虚拟 IP |
| 环境变量 + ConfigMap | RBG_* 环境变量 / ConfigMap 内容 | 拓扑信息自动注入，扩缩容后 ConfigMap 自动更新且无需重启 Pod |
| 端口分配 | PodScoped / RoleScoped 端口值 | 同角色内端口按作用域正确分配，可通过 references 跨组件引用 |
| 组件发现 | addressRefs / portRefs 注入结果 | 目标组件的 FQDN 地址和端口被正确发现并注入 |

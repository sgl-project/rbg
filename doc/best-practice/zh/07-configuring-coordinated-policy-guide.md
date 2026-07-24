# 操作文档：配置多角色协作策略

> 对应概念文档：[7. 配置 CoordinatedPolicy](07-configuring-coordinated-policy.md)

## 目标

验证 RBG 的 `CoordinatedPolicy` 多角色协作能力，包括：
1. 协作伸缩：首次部署时，让多个角色按接近的进度创建 Pod
2. 协作升级：多个角色同时变更时，限制角色之间的更新进度差异

## 前提条件

- Kubernetes 集群版本 >= 1.24
- 已安装 RBG Controller
- 镜像可访问：`alpine:3.23.5`

> **说明**：本文档使用 `sleep 3600` 作为占位命令，专注于验证 RBG 多角色协作控制面行为，无需 GPU。如需测试真实推理功能，请替换为完整的推理引擎启动命令。

---

## 操作一：协作伸缩（首次部署）

### 步骤 1：创建 CoordinatedPolicy 和多角色 RBG

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
          maxSkew: "25%"
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

### 预期行为

- `CoordinatedPolicy` 与 RBG 同名且同命名空间（`default`），策略会作用于 `coordinated-scaling-demo` 这个 RBG
- `prefill` 和 `decode` 按接近的进度创建 Pod
- `progression: OrderReady`：Pod 就绪后才继续下一批创建
- 最终创建 4 个 prefill Pod 和 2 个 decode Pod

### 验证

```bash
# 观察 Pod 创建过程
watch -n0.5 kubectl get pods -n default -l rbg.workloads.x-k8s.io/group-name=coordinated-scaling-demo

> NAME                                 READY   STATUS    RESTARTS   AGE
> coordinated-scaling-demo-decode-0    1/1     Running   0          19s
> coordinated-scaling-demo-decode-1    1/1     Running   0          7s
> coordinated-scaling-demo-prefill-0   1/1     Running   0          18s
> coordinated-scaling-demo-prefill-1   1/1     Running   0          18s
> coordinated-scaling-demo-prefill-2   0/1     Running   0          7s
> coordinated-scaling-demo-prefill-3   0/1     Running   0          7s
```

**预期输出：**
- Pod 不是单一角色一次性全部创建完成，而是 prefill 和 decode 按接近进度逐步创建，每次创建 1 decode 和 2 prefill
- 最终 prefill 为 4 个 Pod，decode 为 2 个 Pod

### 清理

```bash
kubectl delete cpolicy -n default coordinated-scaling-demo
kubectl delete rbg -n default coordinated-scaling-demo
```

---

## 操作二：协作升级（rollingUpdate.maxSkew）

### 步骤 1：创建 CoordinatedPolicy 和多角色 RBG

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
          maxSkew: 1
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

### 预期行为

- 创建 4 个 prefill Pod 和 4 个 decode Pod
- RBG 状态 Ready 后，可以同时触发两个角色的更新
- `rollingUpdate.maxSkew: 1`：两个角色之间的更新进度差异最多为 1 个实例

### 步骤 2：同时更新两个角色的环境变量

```bash
kubectl patch rbg -n default coordinated-rollout-demo --type='json' \
  -p='[{"op": "add", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/env", "value": [{"name": "new_env", "value": "test"}]},
       {"op": "add", "path": "/spec/roles/1/standalonePattern/template/spec/containers/0/env", "value": [{"name": "new_env", "value": "test"}]}]'
```

### 预期行为

- prefill 和 decode 同时进入滚动更新
- 如果某个角色更新过快，控制器会等待另一个角色跟上
- 更新过程中，两个角色之间已更新实例数差异不超过 1

### 验证

```bash
# 观察 Pod 重建过程
watch -n0.5 kubectl get pods -n default -l rbg.workloads.x-k8s.io/group-name=coordinated-rollout-demo

> NAME                                 READY   STATUS    RESTARTS   AGE
> coordinated-rollout-demo-decode-0    1/1     Running   0          6s
> coordinated-rollout-demo-decode-1    1/1     Running   0          15s
> coordinated-rollout-demo-decode-2    1/1     Running   0          30s
> coordinated-rollout-demo-decode-3    1/1     Running   0          33s
> coordinated-rollout-demo-prefill-0   0/1     Running   0          6s
> coordinated-rollout-demo-prefill-1   1/1     Running   0          15s
> coordinated-rollout-demo-prefill-2   1/1     Running   0          24s
> coordinated-rollout-demo-prefill-3   1/1     Running   0          33s
```

```bash
# 更新完成后，确认所有 Pod 已包含新增环境变量
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

**预期输出：**
- prefill 和 decode 不会出现某一方明显领先另一方完成更新的情况
- 更新完成后，两个角色的所有 Pod 均包含环境变量 `new_env=test`

### 清理

```bash
kubectl delete cpolicy -n default coordinated-rollout-demo
kubectl delete rbg -n default coordinated-rollout-demo
```

---

## 总结

| 操作 | 验证点 | 关键预期 |
| --- | --- | --- |
| 协作伸缩 | scaling.maxSkew / progression | 多角色首次部署时按接近进度创建 Pod |
| 协作升级 | rollingUpdate.maxSkew / maxUnavailable | 多角色更新进度差异保持在限制内 |

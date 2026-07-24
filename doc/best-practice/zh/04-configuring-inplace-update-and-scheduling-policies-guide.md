# 操作文档：配置原地升级与原地调度策略

> 对应概念文档：[4. 配置原地升级与原地调度策略](04-configuring-inplace-update-and-scheduling-policies.md)

## 目标

验证 RBG 的原地升级（In-Place Update）和原地调度（In-Place Scheduling）策略，包括：
1. 原地升级与 Grace Period 流量排空：仅变更镜像时 Pod 留在原节点，支持排空等待
2. 原地调度（Preferred）：Pod 重建时优先调度回历史节点
3. 原地调度（Required）：Pod 重建时必须调度回历史节点

## 前提条件

- Kubernetes 集群版本 >= 1.24
- 已安装 RBG Controller
- 镜像可访问：`alpine:3.23.2`、`alpine:3.23.5`

> **说明**：本文档使用 `sleep 3600` 作为占位命令，专注于验证 RBG 升级控制面行为，无需 GPU。如需测试真实推理功能，请替换为完整的推理引擎启动命令。

---

## 操作一：原地升级（In-Place Update）与 Grace Period 流量排空

### 步骤 1：创建 2 副本 RBG，配置原地升级策略

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

### 预期行为

- 创建 2 个 Pod（`inplace-update-demo-backend-0` 到 `inplace-update-demo-backend-1`）
- 全部就绪后，RBG 状态为 Ready

### 步骤 2：记录更新前的 Pod 状态

```bash
# 记录 Pod 的 RESTARTS，AGE 和 IP
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-update-demo -o wide

> NAME                            READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-update-demo-backend-0   1/1     Running   0          10s   10.xx.xx.11  e01-xxxxxxxxxxxxxx   <none>           2/2
> inplace-update-demo-backend-1   1/1     Running   0          10s   10.xx.xx.12  e01-xxxxxxxxxxxxxx   <none>           2/2
```

### 步骤 3：触发镜像更新

```bash
kubectl patch rbg inplace-update-demo --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/image", "value": "alpine:3.23.5"}]'
```

### 预期行为（镜像更新）

对于每个待更新的 Pod：
1. Controller 将 Pod 的 `InPlaceUpdateReady` 条件设为 `False`，Pod 变为 NotReady
2. Pod 从 Service 端点中摘除
3. 等待 30 秒（`gracePeriodSeconds`），让已有连接处理完成
4. Patch 容器镜像，kubelet 重启容器
5. 容器就绪后，Pod 恢复 Ready 状态

同时满足以下原地升级行为：
- 按序号从高到低逐个更新实例（1 → 0），两个实例更新间隔约 30 秒
- `type: InPlaceIfPossible`：仅变更镜像，触发原地升级
- Pod 不离开当前节点，AGE 不会重置
- 容器的 RESTARTS 计数增加

### 验证

```bash
# 观察更新过程
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
# 确认所有 Pod 已更新到新镜像，且 Pod 未被重建（AGE 不重置）
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-update-demo -o wide

> NAME                            READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-update-demo-backend-0   1/1     Running   1          3m    10.xx.xx.11  e01-xxxxxxxxxxxxxx   <none>           2/2
> inplace-update-demo-backend-1   1/1     Running   1          3m    10.xx.xx.12  e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# 确认所有 Pod 使用新镜像
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-update-demo -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> inplace-update-demo-backend-0=alpine:3.23.5
> inplace-update-demo-backend-1=alpine:3.23.5
```

**预期输出：**
- 每个 Pod 从 NotReady 到镜像更新之间有约 30 秒的等待时间
- Pod AGE 未重置（Pod 未被删除重建）
- 容器 RESTARTS 计数增加（容器被原地重启）
- 所有 Pod 所在节点与更新前一致（Pod 未迁移）
- 所有 Pod 使用新镜像 `alpine:3.23.5`

### 清理

```bash
kubectl delete rbg inplace-update-demo
```

---

## 操作二：原地调度 — Preferred 模式（软亲和）

### 步骤 1：创建 RBG 并配置 Preferred 原地调度

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

### 步骤 2：记录更新前的 Pod 所在节点

```bash
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-preferred -o wide

> NAME                                  READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-scheduling-preferred-backend-0   1/1     Running   0          60s   10.xx.xx.xx  node-A               <none>           2/2
> inplace-scheduling-preferred-backend-1   1/1     Running   0          60s   10.xx.xx.xx  node-B               <none>           2/2
> inplace-scheduling-preferred-backend-2   1/1     Running   0          60s   10.xx.xx.xx  node-C               <none>           2/2
> inplace-scheduling-preferred-backend-3   1/1     Running   0          60s   10.xx.xx.xx  node-D               <none>           2/2
```

### 步骤 3：触发环境变量更新

> **说明**：这里使用环境变量变更，因为 `env` 修改不属于原地更新的支持范围（参见概念文档），会强制触发 Pod 重建，从而验证原地调度是否生效。

```bash
kubectl patch rbg inplace-scheduling-preferred --type='json' \
  -p='[{"op": "add", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/env", "value": [{"name": "new_env", "value": "test"}]}]'
```

### 预期行为（Preferred 原地调度）

- `type: RecreatePod`：Pod 需要删除重建
- `role-inplace-scheduling: Preferred`：注入 `preferredDuringScheduling`（weight=100），新 Pod 优先调度回历史节点
- 如果历史节点资源充足，新 Pod 回到原节点
- 如果历史节点资源不足，Pod 可调度到其他节点（不阻塞调度）

### 验证（Preferred 原地调度）

```bash
# 观察 Pod 重建过程和所在节点
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-preferred -o wide -w
```

```bash
# 查看新 Pod 的节点亲和性，确认 Preferred 亲和已注入
kubectl get pod inplace-scheduling-preferred-backend-3 -o jsonpath='{.spec.affinity}'

# 预期输出包含 preferredDuringSchedulingIgnoredDuringExecution，weight=100
```

```bash
# 更新完成后，确认 Pod 所在节点
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-preferred -o wide

> NAME                                  READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-scheduling-preferred-backend-0   1/1     Running   0          30s   10.xx.xx.xx  node-A               <none>           2/2
> inplace-scheduling-preferred-backend-1   1/1     Running   0          30s   10.xx.xx.xx  node-B               <none>           2/2
> inplace-scheduling-preferred-backend-2   1/1     Running   0          30s   10.xx.xx.xx  node-C               <none>           2/2
> inplace-scheduling-preferred-backend-3   1/1     Running   0          30s   10.xx.xx.xx  node-D               <none>           2/2
```

```bash
# 确认所有 Pod 已包含新增环境变量
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-preferred -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].env[?(@.name=="new_env")].value}{"\n"}{end}'

> inplace-scheduling-preferred-backend-0=test
> inplace-scheduling-preferred-backend-1=test
> inplace-scheduling-preferred-backend-2=test
> inplace-scheduling-preferred-backend-3=test
```

**预期输出：**
- Pod AGE 重置（Pod 被删除重建）
- 新 Pod 的 affinity 中包含 `preferredDuringSchedulingIgnoredDuringExecution`
- 在节点资源充足的情况下，新 Pod 回到原来的节点

### 清理（Preferred 原地调度）

```bash
kubectl delete rbg inplace-scheduling-preferred
```

---

## 操作三：原地调度 — Required 模式（硬亲和）

### 步骤 1：创建 RBG 并配置 Required 原地调度

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

### 步骤 2：记录更新前的 Pod 所在节点（Required）

```bash
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-required -o wide

> NAME                                  READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-scheduling-required-backend-0   1/1     Running   0          60s   10.xx.xx.xx  node-A               <none>           2/2
> inplace-scheduling-required-backend-1   1/1     Running   0          60s   10.xx.xx.xx  node-B               <none>           2/2
> inplace-scheduling-required-backend-2   1/1     Running   0          60s   10.xx.xx.xx  node-C               <none>           2/2
> inplace-scheduling-required-backend-3   1/1     Running   0          60s   10.xx.xx.xx  node-D               <none>           2/2
```

### 步骤 3：触发环境变量更新（Required）

> **说明**：这里使用环境变量变更，因为 `env` 修改不属于原地更新的支持范围（参见概念文档），会强制触发 Pod 重建，从而验证原地调度是否生效。

```bash
kubectl patch rbg inplace-scheduling-required --type='json' \
  -p='[{"op": "add", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/env", "value": [{"name": "new_env", "value": "test"}]}]'
```

### 预期行为（Required 原地调度）

- `role-inplace-scheduling: Required`：注入 `requiredDuringScheduling`，新 Pod **必须**调度回历史节点
- 如果历史节点不可用，Pod 将保持 Pending 状态
- 更新完成后，新 Pod 精确回到原来的节点

### 验证（Required 原地调度）

```bash
# 观察 Pod 重建过程和所在节点
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-required -o wide -w
```

```bash
# 查看新 Pod 的节点亲和性，确认 Required 亲和已注入
kubectl get pod inplace-scheduling-required-backend-3 -o jsonpath='{.spec.affinity}'

# 预期输出包含 requiredDuringSchedulingIgnoredDuringExecution
```

```bash
# 更新完成后，确认 Pod 所在节点与更新前一致
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-required -o wide

> NAME                                  READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> inplace-scheduling-required-backend-0   1/1     Running   0          30s   10.xx.xx.xx  node-A               <none>           2/2
> inplace-scheduling-required-backend-1   1/1     Running   0          30s   10.xx.xx.xx  node-B               <none>           2/2
> inplace-scheduling-required-backend-2   1/1     Running   0          30s   10.xx.xx.xx  node-C               <none>           2/2
> inplace-scheduling-required-backend-3   1/1     Running   0          30s   10.xx.xx.xx  node-D               <none>           2/2
```

```bash
# 确认所有 Pod 使用新镜像
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=inplace-scheduling-required -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].env[?(@.name=="new_env")].value}{"\n"}{end}'

> inplace-scheduling-required-backend-0=test
> inplace-scheduling-required-backend-1=test
> inplace-scheduling-required-backend-2=test
> inplace-scheduling-required-backend-3=test
```

**预期输出：**
- Pod AGE 重置（Pod 被删除重建）
- 新 Pod 的 affinity 中包含 `requiredDuringSchedulingIgnoredDuringExecution`
- 新 Pod 精确回到原来的节点（NODE 列与更新前完全一致）

### 清理（Required 原地调度）

```bash
kubectl delete rbg inplace-scheduling-required
```

---

## 总结

| 操作 | 验证点 | 关键预期 |
| --- | --- | --- |
| 原地升级与 Grace Period | Pod AGE 不重置、RESTARTS 增加、NotReady 后等待约 30 秒 | 仅变更镜像时，Pod 留在原节点，原地升级前等待 gracePeriodSeconds |
| 原地调度（Preferred） | 注入 preferredDuringScheduling | Pod 重建时优先回到历史节点，但不阻塞调度 |
| 原地调度（Required） | 注入 requiredDuringScheduling | Pod 重建时必须回到历史节点，否则 Pending |

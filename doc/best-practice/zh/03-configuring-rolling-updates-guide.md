# 操作文档：配置滚动更新策略

> 对应概念文档：[3. 配置滚动更新策略](03-configuring-rolling-updates.md)

## 目标

验证 RBG 的 `rolloutStrategy` 滚动更新策略，包括：

1. 基础滚动更新配置（maxUnavailable / maxSurge）
2. Partition 金丝雀发布
3. Paused 暂停和恢复更新
4. CoordinatedPolicy 多角色协作升级

## 前提条件

- Kubernetes 集群版本 >= 1.24
- 已安装 RBG Controller
- 镜像可访问：`alpine:3.23.5`

> **说明**：本文档使用 `sleep 3600` 作为占位命令，专注于验证 RBG 滚动更新控制面行为，无需 GPU。如需测试真实推理功能，请替换为完整的推理引擎启动命令。

---

## 操作一：基础滚动更新

### 步骤 1：创建 4 副本 RBG

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

### 预期行为

- 创建 4 个 Pod（`rolling-update-demo-backend-0` 到 `rolling-update-demo-backend-3`）
- 全部就绪后，RBG 状态为 Ready

### 步骤 2：触发滚动更新

```bash
# 修改 command（延长 sleep 时长），触发滚动更新（Pod 重建）
kubectl patch rbg rolling-update-demo --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/command", "value": ["sleep", "7200"]}]'
```

### 预期行为（滚动更新触发）

- 按序号从高到低逐个更新实例（3 → 2 → 1 → 0）
- `maxUnavailable: 1`：同一时间最多 1 个实例不可用
- `maxSurge: 0`：不创建额外实例，先删旧再建新
- 更新过程中始终有 3 个实例可用
- `command` 属于非镜像字段，触发 Pod 重建：旧 Pod 先终止，再按相同序号重建（AGE 归零，RESTARTS 为 0）

### 验证

```bash
# 观察更新过程（持续执行查看变化）
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

可以观测到 Pod 按序号 3 → 2 → 1 → 0 逐个「终止 → 重建」，且同一时刻最多 1 个实例不可用。

```bash
# 确认所有 Pod 已更新到新 command
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rolling-update-demo -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> rolling-update-demo-backend-0=["sleep","7200"]
> rolling-update-demo-backend-1=["sleep","7200"]
> rolling-update-demo-backend-2=["sleep","7200"]
> rolling-update-demo-backend-3=["sleep","7200"]
```

**预期输出：**

- 更新过程中始终有 3 个 Pod Ready
- 更新完成后 4 个 Pod 的 command 均为 `["sleep","7200"]`

### 清理

```bash
kubectl delete rbg rolling-update-demo
```

---

## 操作二：金丝雀发布（Partition）

### 步骤 1：创建 4 副本 RBG 并设置 partition

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
          partition: 4  # 初始设为 4，不更新任何实例
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.5
                command: ["sleep", "3600"]
EOF
```

### 步骤 2：更新 command 但保持 partition=4

```bash
# 修改 command（延长 sleep 时长）
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/command", "value": ["sleep", "7200"]}]'
```

### 预期行为（Partition=4）

- 由于 `partition: 4`，序号 >= 4 的实例才会更新
- 序号 0~3 均小于 4，所以**不更新任何实例**
- 所有 Pod 仍使用旧 command `["sleep","3600"]`

### 验证（Partition=4）

```bash
# 确认所有 Pod 仍使用旧 command
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> canary-deployment-backend-0=["sleep","3600"]
> canary-deployment-backend-1=["sleep","3600"]
> canary-deployment-backend-2=["sleep","3600"]
> canary-deployment-backend-3=["sleep","3600"]
```

**预期输出：** 4 个 Pod 的 command 均为 `["sleep","3600"]`

### 步骤 3：降低 partition 到 3，更新最后一个实例

```bash
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/partition", "value": 3}]'
```

### 预期行为（Partition=3）

- 序号 >= 3 的实例（即实例 3）被更新到新 command
- 序号 0~2 保持旧配置

### 验证（Partition=3）

```bash
# 查看各 Pod 的 command
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> canary-deployment-backend-0=["sleep","3600"]
> canary-deployment-backend-1=["sleep","3600"]
> canary-deployment-backend-2=["sleep","3600"]
> canary-deployment-backend-3=["sleep","7200"]
```

**预期输出：**

- `canary-deployment-backend-3` 使用 `["sleep","7200"]`
- `canary-deployment-backend-0` ~ `2` 使用 `["sleep","3600"]`

### 步骤 4：降低 partition 到 0，完成全部更新

```bash
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/partition", "value": 0}]'
```

### 预期行为（Partition=0）

- 序号 >= 0 的全部实例被更新到新 command

### 验证（Partition=0）

```bash
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> canary-deployment-backend-0=["sleep","7200"]
> canary-deployment-backend-1=["sleep","7200"]
> canary-deployment-backend-2=["sleep","7200"]
> canary-deployment-backend-3=["sleep","7200"]
```

**预期输出：** 4 个 Pod 的 command 均为 `["sleep","7200"]`

### 清理（金丝雀发布）

```bash
kubectl delete rbg canary-deployment
```

---

## 操作三：暂停和恢复更新

### 步骤 1：创建 RBG 并触发更新后暂停

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
          paused: true  # 暂停更新
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: alpine:3.23.5
                command: ["sleep", "3600"]
EOF
```

### 步骤 2：更新 command

```bash
kubectl patch rbg paused-update --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/command", "value": ["sleep", "7200"]}]'
```

### 预期行为（已暂停）

- 由于 `paused: true`，更新被暂停
- 所有 Pod 保持旧配置

### 验证（已暂停）

```bash
# 确认所有 Pod 仍使用旧 command
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=paused-update -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> paused-update-backend-0=["sleep","3600"]
> paused-update-backend-1=["sleep","3600"]
> paused-update-backend-2=["sleep","3600"]
> paused-update-backend-3=["sleep","3600"]
```

**预期输出：** 4 个 Pod 的 command 均为 `["sleep","3600"]`（更新被暂停）

### 步骤 3：恢复更新

```bash
kubectl patch rbg paused-update --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/paused", "value": false}]'
```

### 预期行为（已恢复）

- 更新恢复执行
- Pod 逐个更新到新 command

### 验证（已恢复）

```bash
# 等待更新完成，确认所有 Pod 使用新 command
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=paused-update -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].command}{"\n"}{end}'

> paused-update-backend-0=["sleep","7200"]
> paused-update-backend-1=["sleep","7200"]
> paused-update-backend-2=["sleep","7200"]
> paused-update-backend-3=["sleep","7200"]
```

**预期输出：** 4 个 Pod 的 command 均为 `["sleep","7200"]`

### 清理（暂停和恢复）

```bash
kubectl delete rbg paused-update
```

---

## 操作四：多角色协作升级（CoordinatedPolicy）

### 步骤 1：创建多角色 RBG

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

### 步骤 2：创建 CoordinatedPolicy

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

### 步骤 3：同时更新两个角色的 command

```bash
kubectl patch rbg pd-rollout-demo --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/command", "value": ["sleep", "7200"]},
       {"op": "replace", "path": "/spec/roles/1/standalonePattern/template/spec/containers/0/command", "value": ["sleep", "7200"]}]'
```

### 预期行为（协作升级）

- Prefill 和 Decode 同时开始更新
- `maxSkew: "10%"` 约束两个角色的更新进度差异不超过 10%
- 如果 Prefill 更新过快（进度差异 > 10%），Controller 暂停 Prefill，等待 Decode 跟上
- 两个角色的更新进度始终保持接近
- command 为非镜像字段，更新以 Pod 重建方式进行（Pod 逐个终止并按序号重建）

### 验证（协作升级）

```bash
# 观察 Pod 更新过程
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
> pd-rollout-demo-prefill-0   0/1     Pending             0          0s
> pd-rollout-demo-prefill-0   0/1     ContainerCreating   0          0s
> pd-rollout-demo-prefill-0   1/1     Running             0          29s
```

通过更新节奏，可以观测到协作步骤：1p1d（1 prefill + 1 decode）- 1p - 1p1d - 1p - 1p1d - 1p - 1p，两个角色的进度差异始终受 `maxSkew` 约束。

```bash
# 更新完成后确认所有 Pod 使用新 command
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

**预期输出：**

- 更新过程中两个角色的进度差异始终 <= 10%
- 更新完成后所有 Pod 的 command 均为 `["sleep","7200"]`
- CoordinatedPolicy 的 status 中显示协作状态

### 清理（协作升级）

```bash
kubectl delete cpolicy pd-rollout-demo
kubectl delete rbg pd-rollout-demo
```

---

## 总结

| 操作 | 验证点 | 关键预期 |
| --- | --- | --- |
| 基础滚动更新 | maxUnavailable / maxSurge | 逐个替换，始终 3/4 可用 |
| 金丝雀发布 | partition 分区控制 | 仅序号 >= partition 的实例更新 |
| 暂停和恢复 | paused 暂停机制 | 暂停时不更新，恢复后继续 |
| 协作升级 | CoordinatedPolicy maxSkew | 两个角色更新进度差异 <= 10% |

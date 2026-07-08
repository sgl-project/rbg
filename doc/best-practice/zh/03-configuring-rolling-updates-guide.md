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
- 镜像可访问：`lmsysorg/sglang:v0.5.9`、`lmsysorg/sglang:v0.5.12.post1`

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
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
EOF
```

### 预期行为

- 创建 4 个 Pod（`rolling-update-demo-backend-0` 到 `rolling-update-demo-backend-3`）
- 全部就绪后，RBG 状态为 Ready

### 步骤 2：触发滚动更新

```bash
# 更新镜像版本，触发滚动更新
kubectl patch rbg rolling-update-demo --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/image", "value": "lmsysorg/sglang:v0.5.12.post1"}]'
```

### 预期行为

- 按序号从高到低逐个更新实例（3 → 2 → 1 → 0）
- `maxUnavailable: 1`：同一时间最多 1 个实例不可用
- `maxSurge: 0`：不创建额外实例，先删旧再建新
- 更新过程中始终有 3 个实例可用

### 验证

```bash
# 观察更新过程（持续执行查看变化）
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rolling-update-demo -o wide -w

> NAME                            READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> rolling-update-demo-backend-0   1/1     Running   0          68s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-1   1/1     Running   0          68s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-2   1/1     Running   0          68s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-3   1/1     Running   0          68s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-3   1/1     Running   0          78s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-3   1/1     Running   0          78s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-3   1/1     Running   1 (0s ago)   108s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-3   1/1     Running   1 (0s ago)   108s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-3   1/1     Running   1 (0s ago)   108s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-2   1/1     Running   0            108s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-2   1/1     Running   0            108s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-3   1/1     Running   1 (2s ago)   110s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-2   1/1     Running   1 (1s ago)   2m19s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-2   1/1     Running   1 (1s ago)   2m19s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-2   1/1     Running   1 (1s ago)   2m19s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-1   1/1     Running   0            2m19s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-1   1/1     Running   0            2m19s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-2   1/1     Running   1 (2s ago)   2m20s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-1   1/1     Running   1 (0s ago)   2m49s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-1   1/1     Running   1 (0s ago)   2m49s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-1   1/1     Running   1 (0s ago)   2m49s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> rolling-update-demo-backend-0   1/1     Running   0            2m49s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           1/2
> rolling-update-demo-backend-0   1/1     Running   0            2m49s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           0/2
> rolling-update-demo-backend-1   1/1     Running   1 (2s ago)   2m51s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# 确认所有 Pod 已更新到新镜像
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rolling-update-demo -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> rolling-update-demo-backend-0=lmsysorg/sglang:v0.5.12.post1
> rolling-update-demo-backend-1=lmsysorg/sglang:v0.5.12.post1
> rolling-update-demo-backend-2=lmsysorg/sglang:v0.5.12.post1
> rolling-update-demo-backend-3=lmsysorg/sglang:v0.5.12.post1
```

**预期输出：**
- 更新过程中始终有 3 个 Pod Ready
- 更新完成后 4 个 Pod 均使用 `lmsysorg/sglang:v0.5.12.post1`

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
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
EOF
```

### 步骤 2：更新镜像但保持 partition=4

```bash
# 更新镜像版本
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/image", "value": "lmsysorg/sglang:v0.5.12.post1"}]'
```

### 预期行为

- 由于 `partition: 4`，序号 >= 4 的实例才会更新
- 序号 0~3 均小于 4，所以**不更新任何实例**
- 所有 Pod 仍使用旧镜像 `v0.5.9`

### 验证

```bash
# 确认所有 Pod 仍使用旧镜像
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> canary-deployment-backend-0=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-1=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-2=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-3=lmsysorg/sglang:v0.5.9
```

**预期输出：** 4 个 Pod 均使用 `lmsysorg/sglang:v0.5.9`

### 步骤 3：降低 partition 到 3，更新最后一个实例

```bash
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/partition", "value": 3}]'
```

### 预期行为

- 序号 >= 3 的实例（即实例 3）被更新到新镜像
- 序号 0~2 保持旧版本

### 验证

```bash
# 查看各 Pod 镜像版本
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> canary-deployment-backend-0=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-1=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-2=lmsysorg/sglang:v0.5.9
> canary-deployment-backend-3=lmsysorg/sglang:v0.5.12.post1
```

**预期输出：**
- `canary-deployment-backend-3` 使用 `v0.5.12.post1`
- `canary-deployment-backend-0` ~ `2` 使用 `v0.5.9`

### 步骤 4：降低 partition 到 0，完成全部更新

```bash
kubectl patch rbg canary-deployment --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/partition", "value": 0}]'
```

### 预期行为

- 序号 >= 0 的全部实例被更新到新镜像

### 验证

```bash
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=canary-deployment -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> canary-deployment-backend-0=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-1=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-2=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-3=lmsysorg/sglang:v0.5.12.post1
```

**预期输出：** 4 个 Pod 均使用 `lmsysorg/sglang:v0.5.12.post1`

### 清理

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
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
EOF
```

### 步骤 2：更新镜像

```bash
kubectl patch rbg paused-update --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/image", "value": "lmsysorg/sglang:v0.5.12.post1"}]'
```

### 预期行为

- 由于 `paused: true`，更新被暂停
- 所有 Pod 保持旧版本

### 验证

```bash
# 确认所有 Pod 仍使用旧镜像
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=paused-update -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> paused-update-backend-0=lmsysorg/sglang:v0.5.9
> paused-update-backend-1=lmsysorg/sglang:v0.5.9
> paused-update-backend-2=lmsysorg/sglang:v0.5.9
> paused-update-backend-3=lmsysorg/sglang:v0.5.9
```

**预期输出：** 4 个 Pod 均使用 `v0.5.9`（更新被暂停）

### 步骤 3：恢复更新

```bash
kubectl patch rbg paused-update --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/rolloutStrategy/rollingUpdate/paused", "value": false}]'
```

### 预期行为

- 更新恢复执行
- Pod 逐个更新到新镜像

### 验证

```bash
# 等待更新完成，确认所有 Pod 使用新镜像
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=paused-update -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> canary-deployment-backend-0=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-1=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-2=lmsysorg/sglang:v0.5.12.post1
> canary-deployment-backend-3=lmsysorg/sglang:v0.5.12.post1
```

**预期输出：** 4 个 Pod 均使用 `v0.5.12.post1`

### 清理

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
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000

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
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
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

### 步骤 3：同时更新两个角色的镜像

```bash
kubectl patch rbg pd-rollout-demo --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/0/standalonePattern/template/spec/containers/0/image", "value": "lmsysorg/sglang:v0.5.12.post1"},
       {"op": "replace", "path": "/spec/roles/1/standalonePattern/template/spec/containers/0/image", "value": "lmsysorg/sglang:v0.5.12.post1"}]'
```

### 预期行为

- Prefill 和 Decode 同时开始更新
- `maxSkew: "10%"` 约束两个角色的更新进度差异不超过 10%
- 如果 Prefill 更新过快（进度差异 > 10%），Controller 暂停 Prefill，等待 Decode 跟上
- 两个角色的更新进度始终保持接近

### 验证

```bash
# 观察 Pod 更新过程
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=pd-rollout-demo -o wide -w

> NAME                        READY   STATUS    RESTARTS   AGE     IP            NODE                 NOMINATED NODE   READINESS GATES
> pd-rollout-demo-decode-0    1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-decode-1    1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-decode-2    1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           0/2
> pd-rollout-demo-prefill-0   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-1   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-2   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-3   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-4   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-5   1/1     Running   0          3m33s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-6   1/1     Running   1 (1s ago)   3m56s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-decode-2    1/1     Running   1 (1s ago)   3m56s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-5   1/1     Running   1 (1s ago)   4m27s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-4   1/1     Running   1 (0s ago)   4m57s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-decode-1    1/1     Running   1 (0s ago)   4m57s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-3   1/1     Running   1 (1s ago)   5m28s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-2   1/1     Running   1 (1s ago)   5m59s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-decode-0    1/1     Running   1 (1s ago)   5m59s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-1   1/1     Running   1 (0s ago)   6m29s   10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-rollout-demo-prefill-0   1/1     Running   1 (1s ago)   7m      10.xx.xx.xx   e01-xxxxxxxxxxxxxx   <none>           2/2
```

通过 AGE 字段，可以观测到更新步骤：1p1d（1 prefill + 1 decode） - 1p - 1p1d - 1p - 1p1d - 1p - 1p。。


```bash
# 更新完成后确认所有 Pod 使用新镜像
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=pd-rollout-demo -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> pd-rollout-demo-decode-0=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-decode-1=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-decode-2=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-0=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-1=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-2=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-3=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-4=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-5=lmsysorg/sglang:v0.5.12.post1
> pd-rollout-demo-prefill-6=lmsysorg/sglang:v0.5.12.post1
```

**预期输出：**
- 更新过程中两个角色的进度差异始终 <= 10%
- 更新完成后所有 Pod 均使用 `v0.5.12.post1`
- CoordinatedPolicy 的 status 中显示协作状态

### 清理

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

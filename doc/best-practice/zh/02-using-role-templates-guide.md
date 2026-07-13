# 操作文档：使用 RoleTemplates 减少配置重复

> 对应概念文档：[2. 使用 RoleTemplates 减少配置重复](02-using-role-templates.md)

## 目标

验证 RBG 的 `roleTemplates` 机制，包括：

1. 定义模板并通过 `templateRef` 引用
2. 通过 `patch` 进行差异化覆盖（Strategic Merge Patch）
3. 与 `leaderWorkerPattern` 结合使用
4. 验证配置合并结果正确

## 前提条件

- Kubernetes 集群版本 >= 1.24
- 已安装 RBG Controller
- 镜像可访问：`lmsysorg/sglang:v0.5.9`

> **说明**：操作一使用 `sleep 3600` 作为占位命令，专注于验证 templateRef 机制，无需 GPU。操作二和操作三通过 patch 添加完整的推理引擎启动命令，需要 GPU 节点。

---

## 操作一：定义模板并直接引用

### 步骤 1：创建使用 templateRef 的 RBG

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

### 预期行为

- RBG 创建成功，角色 `backend` 引用模板 `engine-base`
- Pod 使用模板中定义的镜像、资源、卷配置
- Pod 正常启动并就绪

### 验证

```bash
# 查看 RBG 状态
kubectl get rbg rbg-with-templates

> NAME                 READY   AGE
> rbg-with-templates   True    21m
```

```bash
# 等待 Pod 就绪
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=rbg-with-templates --timeout=300s

> pod/rbg-with-templates-backend-0 condition met
```

```bash
# 查看 Pod 状态
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rbg-with-templates

> NAME                           READY   STATUS    RESTARTS   AGE
> rbg-with-templates-backend-0   1/1     Running   0          22m
```

```bash
# 验证 Pod 使用了模板中的镜像
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=rbg-with-templates -o jsonpath='{.items[0].spec.containers[0].image}'

> lmsysorg/sglang:v0.5.9
```

**预期输出：**

- Pod 镜像为 `lmsysorg/sglang:v0.5.9`（来自模板）
- Pod 使用 `sleep 3600` 命令保持运行（来自模板）
- Pod 请求 CPU 和内存资源（来自模板）
- Pod 挂载了 dshm 卷（来自模板）

### 清理

```bash
kubectl delete rbg rbg-with-templates
```

---

## 操作二：通过 patch 覆盖模板（PD 分离部署）

### 步骤 1：创建使用 templateRef + patch 的多角色 RBG

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

### 预期行为（PD 分离 Patch 覆盖）

- Prefill 和 Decode 角色共享模板 `engine-base` 的基础配置（镜像、端口、资源、卷）
- Patch 仅添加各自不同的 `command` 启动参数
- 两个角色的 Pod 镜像相同（来自模板），启动参数不同（来自 patch）

### 验证（PD 分离 Patch 覆盖）

```bash
# 查看 RBG 状态
kubectl get rbg pd-with-templates -o wide

> NAME                READY   AGE
> pd-with-templates   True    46s
```

```bash
# 查看 Pod 状态
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=pd-with-templates -o wide

> NAME                          READY   STATUS    RESTARTS     AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> pd-with-templates-decode-0    1/1     Running   0            46s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> pd-with-templates-prefill-0   1/1     Running   1            46s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# 验证 Prefill Pod 的启动参数（应包含 --disaggregation-mode prefill）
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=prefill -o jsonpath='{.items[0].spec.containers[0].command}'

> ["python3","-m","sglang.launch_server","--model-path","Qwen/Qwen3-0.6B","--host","0.0.0.0","--port","8000","--disaggregation-mode","prefill","--tp-size","1"]
```

```bash
# 验证 Decode Pod 的启动参数（应包含 --disaggregation-mode decode）
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=decode -o jsonpath='{.items[0].spec.containers[0].command}'

> ["python3","-m","sglang.launch_server","--model-path","Qwen/Qwen3-0.6B","--host","0.0.0.0","--port","8000","--disaggregation-mode","decode","--tp-size","1"]
```

**预期输出：**

- 2 个 Pod 均为 Running
- Prefill Pod 的 command 包含 `--disaggregation-mode prefill`
- Decode Pod 的 command 包含 `--disaggregation-mode decode`

### 步骤 2：验证 patch 合并规则（标量覆盖）

```bash
# 更新模板中的资源请求，验证 patch 可以覆盖标量字段
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
                      memory: "16Gi"   # 覆盖模板中的 8Gi
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

### 预期行为（Patch 合并规则）

- Prefill Pod 的内存请求被 patch 覆盖为 `16Gi`
- Prefill Pod 的 GPU 请求保持模板中的 `1`（未被覆盖）
- Decode Pod 的内存请求保持模板中的 `8Gi`（无 patch 覆盖）

### 验证（Patch 合并规则）

```bash
# 验证 Prefill Pod 内存请求为 16Gi（被 patch 覆盖）
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=prefill -o jsonpath='{.items[0].spec.containers[0].resources.requests.memory}'

> 16Gi
```

```bash
# 验证 Prefill Pod GPU 请求仍为 1（来自模板，未被 patch 覆盖）
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=prefill -o jsonpath='{.items[0].spec.containers[0].resources.requests.nvidia\.com/gpu}'

> 1
```

```bash
# 验证 Decode Pod 内存请求为 8Gi（来自模板，无 patch 覆盖）
kubectl get pods -l rbg.workloads.x-k8s.io/role-name=decode -o jsonpath='{.items[0].spec.containers[0].resources.requests.memory}'

> 8Gi
```

**预期输出：**

- Prefill memory: `16Gi`
- Prefill gpu: `1`
- Decode memory: `8Gi`

### 清理（PD 分离 Patch 覆盖）

```bash
kubectl delete rbg pd-with-templates
```

---

## 操作三：与 leaderWorkerPattern 结合使用

### 步骤 1：创建 templateRef + leaderWorkerPattern 的 RBG

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

### 预期行为（leaderWorkerPattern）

- 模板 `engine-base` 提供基础配置（镜像、资源、卷）
- `templateRef.patch` 添加启动命令和张量并行参数
- `leaderTemplatePatch` 为 Leader Pod 添加 `role: leader` 标签
- `workerTemplatePatch` 为 Worker Pod 添加 `role: worker` 标签
- 配置应用顺序：`roleTemplates` → `templateRef.patch` → `leaderTemplatePatch` / `workerTemplatePatch`

### 验证（leaderWorkerPattern）

```bash
# 查看 Pod 状态（应看到 2 个 Pod：Leader + Worker）
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-with-templates -o wide

> NAME                                READY   STATUS    RESTARTS   AGE   IP           NODE                 NOMINATED NODE   READINESS GATES
> agg-tp-with-templates-backend-0-0   1/1     Running   0          48s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
> agg-tp-with-templates-backend-0-1   1/1     Running   0          48s   10.xx.xx.xx  e01-xxxxxxxxxxxxxx   <none>           2/2
```

```bash
# 验证 Leader Pod 标签
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-with-templates,role=leader

> NAME                                READY   STATUS    RESTARTS   AGE
> agg-tp-with-templates-backend-0-0   1/1     Running   0          50s
```

```bash
# 验证 Worker Pod 标签
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-with-templates,role=worker

> NAME                                READY   STATUS    RESTARTS   AGE
> agg-tp-with-templates-backend-0-1   1/1     Running   0          51s
```

```bash
# 验证两个 Pod 均使用模板中的镜像
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-with-templates -o jsonpath='{range .items[*]}{.metadata.name}{"="}{.spec.containers[0].image}{"\n"}{end}'

> agg-tp-with-templates-backend-0-0=lmsysorg/sglang:v0.5.9
> agg-tp-with-templates-backend-0-1=lmsysorg/sglang:v0.5.9
```

```bash
# 验证 patch 中的 command 已应用
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=agg-tp-with-templates -o jsonpath='{.items[0].spec.containers[0].command}'

> ["python3","-m","sglang.launch_server","--model-path","Qwen/Qwen3-0.6B","--host","0.0.0.0","--port","8000","--tp-size","2","--dist-init-addr","$(RBG_LWP_LEADER_ADDRESS):6379","--nnodes","$(RBG_LWP_GROUP_SIZE)","--node-rank","$(RBG_LWP_WORKER_INDEX)"]
```

**预期输出：**

- 2 个 Pod 均为 Running
- 1 个 Pod 带有 `role=leader` 标签
- 1 个 Pod 带有 `role=worker` 标签
- 两个 Pod 镜像均为 `lmsysorg/sglang:v0.5.9`
- command 包含 `--tp-size 2` 等参数（来自 patch）

### 清理（leaderWorkerPattern）

```bash
kubectl delete rbg agg-tp-with-templates
```

---

## 总结

| 操作 | 验证点 | 关键预期 |
| --- | --- | --- |
| 模板直接引用 | templateRef 基本功能 | Pod 使用模板中的全部配置 |
| Patch 差异化覆盖 | Strategic Merge Patch 合并 | 镜像来自模板，command 来自 patch，标量可覆盖 |
| 与 leaderWorkerPattern 结合 | 三层配置叠加 | 模板 → patch → leader/workerTemplatePatch 依次应用 |

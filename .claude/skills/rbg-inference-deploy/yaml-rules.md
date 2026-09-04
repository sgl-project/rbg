# RBG YAML 生成规则

## 核心原则

1. **roleTemplates 仅适用于无 Taint 节点**：多角色共用相同镜像且目标节点无 Taint 时，才提取公共部分到 `spec.roleTemplates`，各角色通过 `templateRef` 引用并用 `patch` 差异化。
2. **有 Taint 时必须用直接 `template`**：`tolerations` 字段**无法**通过 `templateRef.patch` 传递到 Pod（RBG controller 已知行为限制，已通过源码验证）。只要节点有 Taint，该角色必须使用直接 `template`，在 `spec.tolerations` 中显式写入容忍规则。
3. **templateRef.patch 必须设置**：即使无覆盖也需设置为 `patch: {}`。
4. **templateRef 与 template 互斥**：一个角色只能选其一。
5. **PD 分离时 Router DNS 地址由 RBG 自动生成**，格式见下方。
6. **patch 中禁止写 `initContainers: []`**，会覆盖 base 模板中的 initContainers 导致意外行为。

---

## API 版本与 Kind

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: <rbg-name>
  namespace: <namespace>
  labels:
    app: <app-label>
```

---

## DNS 命名规则

RBG Controller 为每个 Role 自动创建 Headless Service，DNS 格式：

```
{rbgName}-{roleName}-{ordinal}.s-{rbgName}-{roleName}.{namespace}.svc.cluster.local
```

**示例**（RBG name=`pd-inference`，namespace=`default`）：
- Prefill 实例 0：`pd-inference-prefill-0.s-pd-inference-prefill.default.svc.cluster.local`
- Decode 实例 0：`pd-inference-decode-0.s-pd-inference-decode.default.svc.cluster.local`

在 Router 启动参数中可省略 namespace 后缀（同 namespace 内）：
```
http://pd-inference-prefill-0.s-pd-inference-prefill:8000
```

---

## 模式一：聚合单卡（standalonePattern）

```yaml
spec:
  roles:
    - name: backend
      replicas: <N>
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:<tag>
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "<model-path>"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                  - --tp-size
                  - "1"
                  # 追加用户额外参数
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
```

---

## 模式二：聚合张量并行（leaderWorkerPattern）

```yaml
spec:
  roles:
    - name: backend
      replicas: <N>
      leaderWorkerPattern:
        size: <TP_SIZE>        # 1 Leader + (TP_SIZE-1) Workers
        restartPolicy: RecreateRoleInstanceOnPodRestart
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:<tag>
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "<model-path>"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                  - --tp-size
                  - "<TP_SIZE>"
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
                    nvidia.com/gpu: "1"   # 每 Pod 1 张 GPU
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
        leaderTemplatePatch:     # 可选：为 Leader 添加差异化配置
          metadata:
            labels:
              role: leader
        workerTemplatePatch:     # 可选：为 Worker 添加差异化配置
          metadata:
            labels:
              role: worker
```

**vLLM 张量并行的环境变量注入**：vLLM 使用不同的分布式初始化参数，通过环境变量传入：
```yaml
env:
  - name: VLLM_HOST_IP
    valueFrom:
      fieldRef:
        fieldPath: status.podIP
command:
  - python3
  - -m
  - vllm.entrypoints.openai.api_server
  - --tensor-parallel-size
  - "$(RBG_LWP_GROUP_SIZE)"
  # vLLM Ray 会自动发现节点，RBG 注入的变量提供地址信息
```

---

## 模式三：PD 分离（多角色）

> **选择规则**：
> - GPU 节点**有 Taint** → 用「直接 template 方案」（Section 3.1）
> - GPU 节点**无 Taint** → 可用「roleTemplates 方案」（Section 3.2）

### 3.1 有 Taint 节点 - 直接 template 方案（推荐用于云厂商 GPU 集群）

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: <rbg-name>
  namespace: <namespace>
spec:
  roles:
    - name: router
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: router
                image: lmsysorg/sglang-router:<same-version>  # 与 engine 同系列版本
                command:
                  - python3
                  - -m
                  - sglang_router.launch_router
                  - --pd-disaggregation
                  - --prefill
                  - "http://<rbg-name>-prefill-0.s-<rbg-name>-prefill:8000"
                  - --decode
                  - "http://<rbg-name>-decode-0.s-<rbg-name>-decode:8000"
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
                  limits:
                    cpu: "4"
                    memory: "8Gi"

    - name: prefill
      replicas: <PREFILL_REPLICAS>
      standalonePattern:
        template:
          spec:
            # 有 Taint 时必须在此处直接写 tolerations
            tolerations:
              - key: "<taint-key>"        # 从 NODE_TAINTS 中填入
                operator: "Exists"         # 或 Equal + value
                effect: "NoSchedule"       # 或 NoExecute / PreferNoSchedule
            nodeSelector:
              <label-key>: <label-value>
            containers:
              - name: engine
                image: lmsysorg/sglang:<tag>
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "<model-path>"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                  - --disaggregation-mode
                  - "prefill"
                  - --tp-size
                  - "<TP_SIZE>"
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
      replicas: <DECODE_REPLICAS>
      standalonePattern:
        template:
          spec:
            tolerations:
              - key: "<taint-key>"
                operator: "Exists"
                effect: "NoSchedule"
            nodeSelector:
              <label-key>: <label-value>
            containers:
              - name: engine
                image: lmsysorg/sglang:<tag>
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --model-path
                  - "<model-path>"
                  - --host
                  - "0.0.0.0"
                  - --port
                  - "8000"
                  - --disaggregation-mode
                  - "decode"
                  - --tp-size
                  - "<TP_SIZE>"
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
```

### 3.2 无 Taint 节点 - 使用 roleTemplates 的完整示例

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: <rbg-name>
  namespace: <namespace>
spec:
  # 公共模板：Prefill 和 Decode 共用相同的引擎镜像和资源
  roleTemplates:
    - name: engine-base
      template:
        spec:
          containers:
            - name: engine
              image: lmsysorg/sglang:<tag>
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
    # Router：CPU-only，不使用公共模板（资源差异大）
    - name: router
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: router
                image: lmsysorg/sglang-router:<tag>
                command:
                  - python3
                  - -m
                  - sglang_router.launch_router
                  - --pd-disaggregation
                  - --prefill
                  - "http://<rbg-name>-prefill-0.s-<rbg-name>-prefill:8000"
                  - --decode
                  - "http://<rbg-name>-decode-0.s-<rbg-name>-decode:8000"
                  # 若 replicas > 1，逐个列出：
                  # - --prefill
                  # - "http://<rbg-name>-prefill-1.s-<rbg-name>-prefill:8000"
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
                  limits:
                    cpu: "4"
                    memory: "8Gi"

    # Prefill：引用公共模板，patch 添加 PD 分离参数
    - name: prefill
      replicas: <PREFILL_REPLICAS>
      standalonePattern:           # 或 leaderWorkerPattern（张量并行时）
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
                    - "<model-path>"
                    - --host
                    - "0.0.0.0"
                    - --port
                    - "8000"
                    - --disaggregation-mode
                    - "prefill"
                    - --tp-size
                    - "<TP_SIZE>"

    # Decode：引用公共模板，patch 添加 PD 分离参数
    - name: decode
      replicas: <DECODE_REPLICAS>
      standalonePattern:           # 或 leaderWorkerPattern
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
                    - "<model-path>"
                    - --host
                    - "0.0.0.0"
                    - --port
                    - "8000"
                    - --disaggregation-mode
                    - "decode"
                    - --tp-size
                    - "<TP_SIZE>"
```

> **Router 地址列表**：若 prefill 或 decode replicas > 1，需在 Router 的 `--prefill` / `--decode` 参数中逐个列出每个实例的 DNS 地址（ordinal 从 0 到 N-1）。

---

## 附加功能配置

### 滚动升级策略（推荐）

```yaml
# 在 RoleSpec 中添加
rolloutStrategy:
  type: RollingUpdate
  rollingUpdate:
    type: InPlaceIfPossible      # 优先原地升级
    maxUnavailable: 1
    inPlaceUpdateStrategy:
      gracePeriodSeconds: 30     # 原地升级前的 grace period
```

### ScalingAdapter（HPA 集成）

```yaml
# 在 RoleSpec 中添加
scalingAdapter:
  enable: true
```

RBG Controller 自动创建 `RoleBasedGroupScalingAdapter` CR，配置 HPA：
```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: decode-hpa
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: <rbg-name>-decode    # 格式：{rbgName}-{roleName}
  minReplicas: 1
  maxReplicas: 10
  metrics:
    - type: External
      external:
        metric:
          name: custom_metric
        target:
          type: AverageValue
          averageValue: "100"
```

### Gang 调度（Volcano）

```yaml
# 在 RoleSpec 的 annotations 中添加
annotations:
  rbg.workloads.x-k8s.io/scheduling-backend: volcano
```

### CoordinatedPolicy（协调升级/扩缩容）

创建独立的 `CoordinatedPolicy` CR，与 RBG 同名同 namespace：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: <rbg-name>          # 必须与 RBG 同名
  namespace: <namespace>
spec:
  policies:
    - name: pd-coordination
      roles:
        - prefill
        - decode
      strategy:
        # 协调滚动更新（与 rolloutStrategy 二选一时：此处控制跨角色协调）
        rollingUpdate:
          maxSkew: 1          # 两角色升级进度差不超过 1 个实例
          maxUnavailable: 1
        # 协调扩缩容
        scaling:
          maxSkew: 2          # 扩缩容时两角色差不超过 2 个实例
          progression: OrderReady   # 等待前一个 Ready 再扩下一个
```

> **注意**：`rolloutStrategy` 控制单角色的升级细节（type/maxUnavailable），`CoordinatedPolicy` 控制多角色之间的协调约束（maxSkew），两者可以共存。

### NodeSelector

```yaml
# 在 template.spec 中添加
spec:
  nodeSelector:
    <label-key>: <label-value>
    # 例如：nvidia.com/gpu.product: A100-SXM4-80GB
```

### PVC 挂载（模型存储）

```yaml
# 在 template.spec 中添加
volumes:
  - name: model-storage
    persistentVolumeClaim:
      claimName: <pvc-name>
containers:
  - name: engine
    volumeMounts:
      - name: model-storage
        mountPath: /models
    command:
      - --model-path
      - /models/<model-subpath>
```

### ReadinessProbe & LivenessProbe

```yaml
containers:
  - name: engine
    readinessProbe:
      httpGet:
        path: /health
        port: 8000
      initialDelaySeconds: 60
      periodSeconds: 10
    livenessProbe:
      httpGet:
        path: /health
        port: 8000
      initialDelaySeconds: 120
      periodSeconds: 30
```

---

## Patch 合并规则速查

| 字段类型 | 合并行为 |
|---------|----------|
| 标量（string/int/bool） | 覆盖 |
| containers 列表 | 按 `name` 字段匹配，递归合并 |
| volumes 列表 | 按 `name` 字段匹配，递归合并 |
| map（labels/annotations/resources） | 递归合并（patch 中的 key 覆盖模板中的同名 key） |
| **tolerations 列表** | **⚠️ 不会被 patch 传递**，controller 转换时丢失，必须用直接 template |

**适用顺序**：`roleTemplates` → `templateRef.patch` → `leaderTemplatePatch` / `workerTemplatePatch`

**patch 中禁止出现的字段**：
- `tolerations`（无效，会被丢弃）
- `initContainers: []`（会清空 base 模板的 initContainers）
- `affinity`（行为未经充分验证，谨慎使用）

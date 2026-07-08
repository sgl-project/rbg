## 概述
在 RBG 中，多个角色往往共享相同的 Pod 配置（如镜像、环境变量、资源限制等）。`roleTemplates` 允许你在 RBG 级别定义可复用的 Pod 模板，各角色通过 `templateRef` 引用模板，并可通过 `patch` 进行差异化覆盖，避免重复编写相同的配置。

## 前提条件
+ Kubernetes 集群版本 >= 1.24
+ 已安装 RBG Controller（参考 [安装指南](https://github.com/sgl-project/rbg)）

> **说明**：以下示例使用 SGLang 引擎（`lmsysorg/sglang:v0.5.9`）进行演示。如使用其他推理引擎，请替换为对应镜像并调整启动参数。
>

---

## 不使用 RoleTemplates 的问题
在多角色推理服务中，引擎镜像版本、模型 Volume 配置等对各个角色来说是相同的。例如 PD 分离部署中的 Prefill 和 Decode 角色，通常使用相同的推理引擎镜像、相同的 GPU 资源请求和相同的共享内存挂载，仅启动参数不同。

当这些公共配置需要更新时（如升级引擎镜像版本），必须逐一修改每个角色的 `template`，一是操作复杂，二是容易遗漏某个角色导致版本不一致。`roleTemplates` 将这些公共配置集中管理，更新时只需修改模板定义，所有引用该模板的角色自动生效。

---

## 使用 RoleTemplates
`roleTemplates` 在 RBG 的 `spec` 级别定义可复用的 Pod 模板，角色通过 `templateRef` 引用。

### 定义模板并直接引用
在 `spec.roleTemplates` 中定义模板，角色通过 `templateRef.name` 引用：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: rbg-with-templates
spec:
  # 定义可复用的模板
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
    # 直接引用模板，不做任何覆盖
    - name: backend
      replicas: 1
      standalonePattern:
        templateRef:
          name: engine-base
```

#### 参数说明
| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `spec.roleTemplates` | []object | 否 | - | 可复用的 Pod 模板列表 |
| `spec.roleTemplates[].name` | string | 是 | - | 模板名称，在 RBG 内唯一，符合 DNS label 格式 |
| `spec.roleTemplates[].template` | object | 是 | - | Pod 模板，遵循 Kubernetes 标准 `PodTemplateSpec` |
| `spec.roles[].standalonePattern.templateRef.name` | string | 是 | - | 引用的模板名称 |


> **说明**：`templateRef` 和 `template` 互斥——一个角色要么使用 `templateRef` 引用模板，要么使用 `template` 直接定义，不能同时使用。
>

### 通过 patch 覆盖模板
引用模板后，角色可以通过 `templateRef.patch` 进行差异化覆盖。Patch 使用 Kubernetes 标准的 **Strategic Merge Patch** 语义，可以添加、修改或覆盖模板中的字段。

以 PD 分离部署为例，Prefill 和 Decode 共享相同的基础配置，但启动参数不同：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
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
    # Prefill：引用模板，通过 patch 添加启动参数
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

    # Decode：引用模板，通过 patch 添加不同的启动参数
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
```

#### 参数说明
| 参数 | 类型 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `spec.roles[].standalonePattern.templateRef.name` | string | 是 | - | 引用的模板名称 |
| `spec.roles[].standalonePattern.templateRef.patch` | object | 否 | - | Strategic Merge Patch，用于覆盖模板中的字段 |


### Patch 的合并规则
Patch 遵循 Kubernetes 标准 Strategic Merge Patch 语义：

| 字段类型 | 合并行为 | 示例 |
| --- | --- | --- |
| 标量字段（string、int、bool） | 覆盖 | `resources.requests.memory: "128Mi"` 覆盖模板中的值 |
| 列表字段（按 name 匹配） | 按 name 合并 | `containers` 列表按 `name` 字段匹配，合并对应容器的字段 |
| Map 字段 | 递归合并 | `metadata.labels` 会合并而非覆盖 |


以覆盖资源请求为例：

```yaml
# 模板中定义
containers:
  - name: engine
    resources:
      requests:
        memory: "8Gi"
        nvidia.com/gpu: "1"

# Patch 覆盖 memory，gpu 保持不变
patch:
  spec:
    containers:
      - name: engine          # 通过 name 匹配到模板中的容器
        resources:
          requests:
            memory: "16Gi"    # 覆盖 memory
```

### 与 leaderWorkerPattern 结合使用
`templateRef` 同样适用于 `leaderWorkerPattern`。模板定义基础 Pod 配置，`leaderTemplatePatch` 和 `workerTemplatePatch` 在此基础上进一步差异化 Leader 和 Worker：

```yaml
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
        # 引用模板作为基础配置
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
        # 在模板基础上，为 Leader 和 Worker 添加差异化标签
        leaderTemplatePatch:
          metadata:
            labels:
              role: leader
        workerTemplatePatch:
          metadata:
            labels:
              role: worker
```

> **说明**：配置应用顺序为 `roleTemplates` → `templateRef.patch` → `leaderTemplatePatch` / `workerTemplatePatch`，后面的 patch 在前面的结果上叠加。
>

---

## 验证部署
```bash
# 查看 RBG 状态
kubectl get rbg

# 查看 Pod 状态
kubectl get pods

# 确认模板引用已正确解析
kubectl get rbg <rbg-name> -o yaml | grep templateRef
```

## 相关文档
+ [使用 RBG 部署推理服务](#)
+ [配置 HPA 弹性伸缩](#)
+ [Gang 调度配置](#)
+ [滚动更新与金丝雀发布](#)


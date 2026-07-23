# 端口分配与服务发现

## 概述

在分布式推理服务中，多个角色和组件需要相互通信。RBG 提供三个层次的服务发现机制，从简单到复杂依次覆盖不同场景：

- **第 1 层：Headless Service + DNS**
  - 自动创建 Headless Service
  - 每个 Pod 拥有稳定的 DNS 名称
  - 适用：固定端口、已知拓扑的场景
- **第 2 层：环境变量 + ConfigMap**
  - Controller 自动注入环境变量（角色名、索引、Leader 地址等）
  - 自动生成全局 ConfigMap（所有角色的地址和端口拓扑）
  - ConfigMap 挂载到 `/etc/rbg/config.yaml`
  - 适用：需要运行时获取集群拓扑的场景
- **第 3 层：端口分配 + 组件发现**
  - 动态分配端口（hostNetwork 场景避免端口冲突）
  - 组件发现注解（component-discovery）获取其他组件的地址和端口
  - 适用：CustomComponentsPattern + hostNetwork + RDMA 场景

## 前提条件

+ Kubernetes 集群版本 >= 1.24
+ 已安装 RBG Controller（参考 [安装指南](https://github.com/sgl-project/rbg)）
+ 端口分配功能需要启用 `--enable-port-allocator`（默认关闭）

---

## 第 1 层：Headless Service 与 DNS 服务发现
### 自动创建的 Headless Service

RBG Controller 为每个角色自动创建一个 Headless Service（`ClusterIP: None`），命名规则为：

```plain
s-{rbgName}-{roleName}
```

例如，RBG 名为 `pd-inference`，角色名为 `prefill`，自动创建的 Service 名称为 `s-pd-inference-prefill`。

Headless Service 的配置：

| 属性 | 值 | 说明 |
| --- | --- | --- |
| `clusterIP` | `None` | Headless，不分配虚拟 IP |
| `publishNotReadyAddresses` | `true` | 即使 Pod 未就绪也发布 DNS 记录 |
| `selector` | `group-name: <rbgName>, role-name: <roleName>` | 自动匹配角色的所有 Pod |


### Pod DNS 命名规则

每个 Pod 通过 Headless Service 获得一个稳定的 DNS 名称：

```plain
{rbgName}-{roleName}-{index}.{serviceName}.{namespace}.svc.cluster.local
```

示例：

```plain
# Prefill 角色的第 0 个 Pod
pd-inference-prefill-0.s-pd-inference-prefill.default.svc.cluster.local

# Decode 角色的第 2 个 Pod
pd-inference-decode-2.s-pd-inference-decode.default.svc.cluster.local
```

### 使用方式

Pod 可以直接通过 DNS 名称访问其他角色的 Pod，无需手动配置 Service 或 IP 地址：

```yaml
# 在 Decode Pod 中访问 Prefill 的第 0 个实例
containers:
  - name: engine
    env:
      - name: PREFILL_ADDR
        value: "pd-inference-prefill-0.s-pd-inference-prefill.default.svc.cluster.local:8000"
```

> **说明**：DNS 名称中的 `{index}` 是 Pod 的有序索引（从 0 开始），StatefulSet 和 RoleInstanceSet 模式下 Pod 名称稳定，DNS 地址在 Pod 重建后保持不变。
>

---

## 第 2 层：环境变量与 ConfigMap
### 自动注入的环境变量

Controller 自动向每个 Pod 注入以下环境变量，供推理引擎在运行时获取自身角色信息和集群拓扑：

#### 基础变量（所有角色）
| 变量 | 值 | 说明 |
| --- | --- | --- |
| `RBG_GROUP_NAME` | RBG 名称 | 所属 RoleBasedGroup 的名称 |
| `RBG_ROLE_NAME` | 角色名称 | 当前 Pod 所属的角色名 |


#### Stateful 角色（StatefulSet / LeaderWorkerSet / RoleInstanceSet）
| 变量 | 值 | 来源 |
| --- | --- | --- |
| `RBG_ROLE_INDEX` | Pod 有序索引 | Downward API（StatefulSet: `pod-index` label，RoleInstanceSet: `role-instance-index` label） |


#### RoleInstanceSet 特有变量
| 变量 | 值 | 来源 |
| --- | --- | --- |
| `RBG_ROLE_INSTANCE_NAME` | RoleInstance 名称 | Downward API（`role-instance-name` label） |
| `RBG_COMPONENT_NAME` | 组件名称 | Downward API（`component-name` label） |
| `RBG_COMPONENT_INDEX` | 组件索引 | Downward API（`component-id` label） |


#### LeaderWorkerPattern 特有变量
| 变量 | 值 | 说明 |
| --- | --- | --- |
| `RBG_LWP_LEADER_ADDRESS` | Leader Pod 的 FQDN | 计算值：`$(RBG_ROLE_INSTANCE_NAME)-0.{svcName}.{namespace}` |
| `RBG_LWP_WORKER_INDEX` | 当前 Worker 的索引 | Downward API（`component-index` label） |
| `RBG_LWP_GROUP_SIZE` | 组内总 Pod 数 | Downward API（`component-size` label） |


> **说明**：Size 相关的环境变量（如 `RBG_LWP_GROUP_SIZE`）在扩缩容时会变化，但 Controller 有意不将这些变量注入到非 LWP 场景中，避免因副本数变化触发 Pod 重建。
>

### ConfigMap 集群拓扑

Controller 自动创建一个 RBG 级别的 ConfigMap（名称与 RBG 相同），包含所有角色的地址和端口信息。ConfigMap 自动挂载到每个 Stateful 角色的 Pod 中。

#### ConfigMap 结构
```yaml
# ConfigMap: pd-inference (key: config.yaml)
group:
  name: pd-inference
  size: 2
  roles:
    - prefill
    - decode
roles:
  prefill:
    size: 2
    instances:
      - address: pd-inference-prefill-0.s-pd-inference-prefill
        ports:
          http: 8000
      - address: pd-inference-prefill-1.s-pd-inference-prefill
        ports:
          http: 8000
  decode:
    size: 4
    instances:
      - address: pd-inference-decode-0.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-1.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-2.s-pd-inference-decode
        ports:
          http: 8000
      - address: pd-inference-decode-3.s-pd-inference-decode
        ports:
          http: 8000
```

#### ConfigMap 挂载方式
| 属性 | 值 | 说明 |
| --- | --- | --- |
| Volume 名称 | `rbg-cluster-config` | 自动注入的 Volume |
| 挂载路径 | `/etc/rbg` | 所有容器自动挂载 |
| 文件名 | `config.yaml` | ConfigMap 的 key |
| 权限 | 只读 | Pod 不可修改 |


推理引擎可以直接读取 `/etc/rbg/config.yaml` 获取整个集群的拓扑信息：

```python
# 推理引擎代码示例：读取集群拓扑
import yaml

with open("/etc/rbg/config.yaml") as f:
    config = yaml.safe_load(f)

# 获取所有 Prefill 实例的地址
prefill_instances = config["roles"]["prefill"]["instances"]
for inst in prefill_instances:
    print(f"Prefill: {inst['address']}:{inst['ports']['http']}")

# 获取所有 Decode 实例的地址
decode_instances = config["roles"]["decode"]["instances"]
for inst in decode_instances:
    print(f"Decode: {inst['address']}:{inst['ports']['http']}")
```

#### 端口信息来源

ConfigMap 中的端口信息来自角色 Spec 中的 `servicePorts` 定义：

```yaml
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
                image: lmsysorg/sglang:v0.5.9
                ports:
                  - containerPort: 8000
```

端口名称会转换为小写并将 `-` 替换为 `_`（如 `http-api` → `http_api`），未命名的端口使用 `port{number}` 格式（如 `port8000`）。

> **说明**：ConfigMap 由 Controller 自动维护，当角色副本数变化（扩缩容）或 ServicePort 变更时，ConfigMap 自动更新。由于 ConfigMap 以 Volume 方式挂载，Pod 内的文件会通过 kubelet 自动同步，无需重启 Pod。
>

---

## 第 3 层：端口分配与组件发现
### 背景：为什么需要动态端口分配

在使用 `hostNetwork: true` 的 RDMA 推理场景中，同一节点上的多个 Pod 共享宿主机的网络命名空间。如果两个 Pod 的容器监听相同端口，会产生端口冲突。

```plain
┌──────────────────────────────────────────────────────────────────┐
│  hostNetwork 端口冲突问题                                          │
│                                                                  │
│  节点 A                                                           │
│  ┌──────────────────────────────────────────────────────┐        │
│  │  Prefill Pod-0        Prefill Pod-1                  │        │
│  │  containerPort: 8000   containerPort: 8000  ← 冲突!   │        │
│  │                                                      │        │
│  │  共享宿主机网络命名空间 → 无法绑定相同端口                 │        │
│  └──────────────────────────────────────────────────────┘        │
│                                                                  │
│  解决：RBG 为每个 Pod 动态分配不同端口                                │
│  ┌──────────────────────────────────────────────────────┐        │
│  │  Prefill Pod-0        Prefill Pod-1                  │        │
│  │  分配端口: 30001        分配端口: 30002                 │        │
│  │  注入环境变量: PORT=30001  PORT=30002                  │        │
│  └──────────────────────────────────────────────────────┘        │
└──────────────────────────────────────────────────────────────────┘
```

### 启用端口分配器

端口分配功能默认关闭，需要在 Controller 启动时通过参数启用：

```bash
# Controller 启动参数
--enable-port-allocator=true     # 启用端口分配
--port-allocate-strategy=random  # 分配策略（目前仅支持 random）
--start-port=30000               # 端口范围起始值
--port-range=5000                # 端口范围大小（30000~34999）
```

Helm 部署配置：

```yaml
# values.yaml
portAllocator:
  enabled: true
  strategy: random
  startPort: 30000
  portRange: 5000
```

### 端口作用域

端口分配支持两种作用域，通过 `scope` 参数控制：

| 作用域 | 说明 | 适用场景 |
| --- | --- | --- |
| `PodScoped` | 每个 Pod 分配独立的端口值 | 同一组件的多个 Pod 需要不同端口（hostNetwork 场景） |
| `RoleScoped` | 角色内所有 Pod 共享同一个端口值 | 角色对外暴露统一端口（组件间通信） |


### 配置端口分配

端口分配通过组件级别的注解 `rolebasedgroup.workloads.x-k8s.io/port-allocator` 配置，注解值为 JSON 格式：

```yaml
customComponentsPattern:
  components:
    - name: worker
      size: 2
      annotations:
        rolebasedgroup.workloads.x-k8s.io/port-allocator: |
          {
            "allocations": [
              {
                "name": "worker-grpc",
                "env": "WORKER_GRPC_PORT",
                "scope": "PodScoped"
              }
            ]
          }
```

#### allocations 参数说明
| 参数 | 类型 | 是否必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string | 是 | 端口逻辑名称，用于被其他组件引用 |
| `env` | string | 是 | 注入到容器的环境变量名 |
| `scope` | string | 否 | 作用域：`PodScoped`（默认）或 `RoleScoped` |
| `annotationKey` | string | 否 | 同时注入到 Pod 注解的 key（可选） |


### 引用其他组件的端口

通过 `references` 字段，一个组件可以获取同角色内其他组件的已分配端口：

```yaml
- name: worker
  size: 2
  annotations:
    rolebasedgroup.workloads.x-k8s.io/port-allocator: |
      {
        "allocations": [
          {
            "name": "worker-grpc",
            "env": "WORKER_GRPC_PORT",
            "scope": "PodScoped"
          }
        ],
        "references": [
          {
            "env": "LEADER_GRPC_PORT",
            "from": "prefill.leader.leader-grpc"
          }
        ]
      }
```

#### references 参数说明
| 参数 | 类型 | 是否必填 | 说明 |
| --- | --- | --- | --- |
| `env` | string | 是 | 注入到容器的环境变量名 |
| `from` | string | 是 | 引用格式：`{roleName}.{componentName}.{portName}` |


### 端口分配的存储与传播

端口值通过注解在资源层级间传播，不依赖 ConfigMap 或额外的 CRD：

```plain
RoleInstanceSet (RoleScoped 端口分配)
    │  注解: <component>.<portName> = "30001"
    │
    ▼
RoleInstance (PodScoped 端口分配 + 复制 RoleScoped)
    │  注解: <component>.<portName> = "30001"  (RoleScoped 复制)
    │  注解: <podName>.<portName>   = "30002"  (PodScoped 新分配)
    │
    ▼
Pod (环境变量注入 + Pod 注解注入)
    环境变量: LEADER_GRPC_PORT=30001
    环境变量: WORKER_GRPC_PORT=30002
    Pod 注解: (如果配置了 annotationKey)
```

---

## 组件发现（Component Discovery）

对于 `CustomComponentsPattern` 中需要发现其他组件的**地址和端口**的场景，RBG 提供 `component-discovery` 注解。与端口分配的 `references` 不同，组件发现可以同时获取目标组件的 FQDN 地址和动态分配的端口。

### 配置组件发现

组件发现通过注解 `rolebasedgroup.workloads.x-k8s.io/component-discovery` 配置：

```yaml
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
          }
        ],
        "portRefs": [
          {
            "env": "LEADER_GRPC_PORT",
            "component": "leader",
            "portName": "leader-grpc"
          },
          {
            "env": "WORKER_0_GRPC_PORT",
            "component": "worker",
            "portName": "worker-grpc",
            "index": 0
          }
        ]
      }
```

#### addressRefs 参数说明
| 参数 | 类型 | 是否必填 | 说明 |
| --- | --- | --- | --- |
| `env` | string | 是 | 注入到容器的环境变量名 |
| `component` | string | 是 | 目标组件名称 |
| `index` | int | 否 | 目标组件内的 Pod 索引（默认 0） |


注入值为完整的 FQDN：`{podName}.{svcName}.{namespace}.svc.cluster.local`

#### portRefs 参数说明
| 参数 | 类型 | 是否必填 | 说明 |
| --- | --- | --- | --- |
| `env` | string | 是 | 注入到容器的环境变量名 |
| `component` | string | 是 | 目标组件名称 |
| `portName` | string | 是 | 端口逻辑名称（对应端口分配的 `name` 字段） |
| `index` | int | 否 | 目标组件内的 Pod 索引（仅 PodScoped 端口需要指定） |


端口解析优先查找 PodScoped 值（`<podName>.<portName>`），再回退到 RoleScoped 值（`<componentName>.<portName>`）。

---

## 完整示例：三角色推理服务

以下示例展示一个包含 leader、worker、router 三个组件的推理服务，综合使用端口分配和组件发现：

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-server
spec:
  roles:
    - name: prefill
      replicas: 1
      customComponentsPattern:
        components:

          # ── leader：1 个 Pod，分配 RoleScoped 端口 ──
          - name: leader
            size: 1
            annotations:
              rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
                {"deleteAfter": ["router"]}
              rolebasedgroup.workloads.x-k8s.io/port-allocator: |
                {
                  "allocations": [
                    {
                      "name": "leader-grpc",
                      "env": "LEADER_GRPC_PORT",
                      "scope": "RoleScoped"
                    }
                  ]
                }
            template:
              spec:
                hostNetwork: true
                containers:
                  - name: leader
                    image: inference-engine:v1.0
                    command: ["./start-leader", "--port", "$(LEADER_GRPC_PORT)"]

          # ── worker：2 个 Pod，分配 PodScoped 端口 + 发现 leader ──
          - name: worker
            size: 2
            annotations:
              rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
                {"deleteAfter": ["router"]}
              rolebasedgroup.workloads.x-k8s.io/port-allocator: |
                {
                  "allocations": [
                    {
                      "name": "worker-grpc",
                      "env": "WORKER_GRPC_PORT",
                      "scope": "PodScoped"
                    }
                  ]
                }
              rolebasedgroup.workloads.x-k8s.io/component-discovery: |
                {
                  "portRefs": [
                    {
                      "env": "LEADER_GRPC_PORT",
                      "component": "leader",
                      "portName": "leader-grpc"
                    }
                  ],
                  "addressRefs": [
                    {
                      "env": "LEADER_ADDR",
                      "component": "leader",
                      "index": 0
                    }
                  ]
                }
            template:
              spec:
                hostNetwork: true
                containers:
                  - name: worker
                    image: inference-engine:v1.0
                    command:
                      - "./start-worker"
                      - "--port"
                      - "$(WORKER_GRPC_PORT)"
                      - "--leader"
                      - "$(LEADER_ADDR):$(LEADER_GRPC_PORT)"

          # ── router：1 个 Pod，发现 leader 和所有 worker ──
          - name: router
            size: 1
            annotations:
              rolebasedgroup.workloads.x-k8s.io/component-depends-on: |
                {"startAfter": ["leader", "worker"]}
              rolebasedgroup.workloads.x-k8s.io/component-discovery: |
                {
                  "portRefs": [
                    {
                      "env": "LEADER_GRPC_PORT",
                      "component": "leader",
                      "portName": "leader-grpc"
                    },
                    {
                      "env": "WORKER_0_GRPC_PORT",
                      "component": "worker",
                      "portName": "worker-grpc",
                      "index": 0
                    },
                    {
                      "env": "WORKER_1_GRPC_PORT",
                      "component": "worker",
                      "portName": "worker-grpc",
                      "index": 1
                    }
                  ],
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
                  ]
                }
            template:
              spec:
                hostNetwork: true
                containers:
                  - name: router
                    image: inference-engine:v1.0
                    command:
                      - "./start-router"
                      - "--leader"
                      - "$(LEADER_ADDR):$(LEADER_GRPC_PORT)"
                      - "--workers"
                      - "$(WORKER_0_ADDR):$(WORKER_0_GRPC_PORT),$(WORKER_1_ADDR):$(WORKER_1_GRPC_PORT)"
```

### 注入的环境变量

部署后，各组件 Pod 中实际注入的环境变量：

**leader Pod：**

| 环境变量 | 示例值 | 说明 |
| --- | --- | --- |
| `LEADER_GRPC_PORT` | `30142` | 端口分配器分配的 RoleScoped 端口 |
| `RBG_GROUP_NAME` | `pd-server` | RBG 名称 |
| `RBG_ROLE_NAME` | `prefill` | 角色名称 |


**worker-0 Pod：**

| 环境变量 | 示例值 | 说明 |
| --- | --- | --- |
| `WORKER_GRPC_PORT` | `31205` | PodScoped 端口（每个 worker 不同） |
| `LEADER_GRPC_PORT` | `30142` | 从 leader 的 RoleScoped 端口引用 |
| `LEADER_ADDR` | `pd-server-prefill-0-...s-pd-server-prefill.default.svc.cluster.local` | leader 的 FQDN 地址 |


**worker-1 Pod：**

| 环境变量 | 示例值 | 说明 |
| --- | --- | --- |
| `WORKER_GRPC_PORT` | `32718` | 与 worker-0 不同的 PodScoped 端口 |
| `LEADER_GRPC_PORT` | `30142` | 与 worker-0 相同（RoleScoped） |
| `LEADER_ADDR` | `pd-server-prefill-0-...s-pd-server-prefill.default.svc.cluster.local` | 与 worker-0 相同 |


**router Pod：**

| 环境变量 | 示例值 | 说明 |
| --- | --- | --- |
| `LEADER_GRPC_PORT` | `30142` | 从 leader 引用 |
| `LEADER_ADDR` | `pd-server-prefill-0-...` | leader 地址 |
| `WORKER_0_GRPC_PORT` | `31205` | worker-0 的端口（通过 index: 0 指定） |
| `WORKER_0_ADDR` | `pd-server-prefill-0-...` | worker-0 的地址 |
| `WORKER_1_GRPC_PORT` | `32718` | worker-1 的端口（通过 index: 1 指定） |
| `WORKER_1_ADDR` | `pd-server-prefill-0-...` | worker-1 的地址 |


---

## 场景选择指南
| 场景 | 推荐方案 | 说明 |
| --- | --- | --- |
| **聚合部署，固定端口** | Headless Service + DNS + ConfigMap | 所有 Pod 使用相同端口，通过 DNS 或 ConfigMap 获取地址 |
| **PD 分离，固定端口** | Headless Service + DNS + ConfigMap | Prefill 和 Decode 各自使用固定端口，ConfigMap 自动维护全集群拓扑 |
| **hostNetwork + RDMA** | 端口分配 + 组件发现 | 同一节点上多个 Pod 需要不同端口，通过端口分配器动态分配 |
| **CustomComponentsPattern 多组件通信** | 端口分配 + 组件发现 + 依赖管理 | 组件间需要发现彼此的地址和动态端口 |


---

## 验证

```bash
# 查看自动创建的 Headless Service
kubectl get svc -l workloads.x-k8s.io/group-name=<rbg-name>

# 验证 DNS 解析（从集群内任意 Pod）
kubectl exec -it <any-pod> -- nslookup <rbg-name>-<role-name>-0.s-<rbg-name>-<role-name>.<namespace>.svc.cluster.local

# 查看自动创建的 ConfigMap
kubectl get configmap <rbg-name> -o yaml

# 查看 Pod 中挂载的 ConfigMap 内容
kubectl exec -it <pod-name> -- cat /etc/rbg/config.yaml

# 查看 Pod 注入的环境变量
kubectl exec -it <pod-name> -- env | grep RBG_

# 查看端口分配结果（PodScoped 场景）
kubectl exec -it <pod-name> -- env | grep -E 'LEADER_GRPC_PORT|WORKER_GRPC_PORT'

# 查看 RoleInstance 注解中的端口值
kubectl get roleinstance <name> -o jsonpath='{.metadata.annotations}'
```

## 相关文档

<!-- TODO: 以下文档尚未创建，待文档完成后统一添加链接 -->

+ 使用 RBG 部署推理服务
+ 配置滚动更新策略
+ 原地升级与原地调度


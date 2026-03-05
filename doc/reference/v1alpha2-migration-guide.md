# Migrating from v1alpha1 to v1alpha2

## 更新目的

1. 移除对 StatefuleSet、Deployment、LeaderWorkerSet 的依赖
   1. RoleBasedGroup 选择三种资源作为子资源，这为用户带来额外的理解成本
   2. 对 LeaderWorkerSet 的版本依赖，也导致了对 K8s 版本的依赖
   3. 在 StatefuleSet、Deployment、LeaderWorkerSet 上难以支持原地升级等特性
   4. Instance 和 InstanceSet 可以实现上述三种资源的全部能力
2. 梳理 Label、Annotation 和 Envs，规范化的同时降低与其他产品冲突的可能

## Schema

<table>
<tr>
<th style="vertical-align: top; width: 50%;">v1alpha1</th>
<th style="vertical-align: top; width: 50%;">v1alpha2</th>
</tr>
<tr>
<td style="vertical-align: top;">

```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
metadata: {}
spec:
  podGroup:
    volcanoScheduling:
      queue: default
  roles:
  - name: #roleName
    workload: # Deprecated
      apiVersion: workloads.x-k8s.io/v1alpha1
      kind: InstanceSet
    template: {} # pod template
    leaderWorkerSet:
      patchLeaderTemplate:
        metadata:
          labels:
            role: leader
      patchWorkerTemplate:
        metadata:
          labels:
            role: worker
      size: 3
```

</td>
<td style="vertical-align: top;">

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  annotations:
    group.rbg.workload.x-k8s.io/enable-gang: "true"
spec:
  roles:
  - name: #roleName
    standalonePattern:
      template: {} # pod template
    leaderWorkerPattern:
      template: {} # pod template
      leaderPatch:
        metadata:
          labels:
            role: leader
      workerPatch:
        metadata:
          labels:
            role: worker
      size: 3
```

```bash
/manager --gang-vendor kubeScheduling
```

</td>
</tr>
</table>

修改内容：

1. 逐步停用 `spec.roles[*].workload` 字段
   1. 暂时保留该字段，默认值改为 InstanceSet
   2. 若设置为 StatefulSet/Deployment/LeaderWorkerSet，提供 Warning Event 提醒 Deprecated
2. 将组调度从 `spec` 中移除，改为通过控制器参数选择全局调度器，使用 `annotations` 设置是否使用组调度
3. 使用 `spec.roles[*].standalonePattern` 实现 Deployment、StatefuleSet 的功能
4. 使用 `spec.roles[*].leaderWorkerPattern` 实现 LeaderWorkerSet 的功能
   1. 字段名称调整：`patchLeaderTemplate` -> `leaderPatch`，`patchWorkerTemplate` -> `workerPatch`

使用 conversion 进行多版本 CRD 支持。

## Labels & Annotations

### 现状

**前缀过多且混乱**

| 前缀                                           | 位置                     |
| ---------------------------------------------- | ------------------------ |
| `rolebasedgroup.workloads.x-k8s.io/`           | RBG 层级                 |
| `rolebasedgroupset.workloads.x-k8s.io/`        | RBGSet 层级              |
| `component.rolebasedgroup.workloads.x-k8s.io/` | Component 层级           |
| `instance.rolebasedgroup.workloads.x-k8s.io/`  | Instance 层级 (RBG 内嵌) |
| `role.rolebasedgroup.workloads.x-k8s.io/`      | Role 层级                |
| `instanceset.workloads.x-k8s.io/`              | InstanceSet 层级         |
| `instance.workloads.x-k8s.io/`                 | Instance 层级 (独立)     |
| `lifecycle.workloads.x-k8s.io/`                | 生命周期                 |
| `workloads.x-k8s.io/`                          | 通用                     |

**层级关系不清晰**
  
当前 label/annotation 没有清晰反映以下层级：

```
RBGSet (GroupSet)
  └── RBG (Group)
        └── Role
              └── RoleInstance
                    └── Component (Pod)
```

**命名风格不统一**

* 有的用 -id，有的用 -name
* 后缀风格不一致（LabelKey vs 无后缀

### K8s 社区实践参考

| 方式               | 格式                       | 示例                                 |
| ------------------ | -------------------------- | ------------------------------------ |
| 方式 A（RBG 采用） | {domain}/{key}             | rbg.workloads.x-k8s.io/groupset-name |
| 方式 B             | {subdomain}.{domain}/{key} | groupset.rbg.workloads.x-k8s.io/name |

**Kubernetes 官方推荐 — 方式 A**

```yaml
app.kubernetes.io/name: mysql
app.kubernetes.io/component: database
app.kubernetes.io/version: "5.7.21"
```

**LeaderWorkerSet — 方式 A**

```yaml
leaderworkerset.sigs.k8s.io/name: my-lws
leaderworkerset.sigs.k8s.io/worker-index: "0"
```

**Kueue — 方式 A**

```yaml
kueue.x-k8s.io/queue-name: default
kueue.x-k8s.io/job-uid: abc123
```

**Istio — 方式 B（例外）**

```yaml
service.istio.io/canonical-name: myservice
topology.istio.io/cluster: cluster-1
gateway.istio.io/managed: istio
```

### 设计目标

1. 统一前缀域：所有元数据统一使用 rbg.workloads.x-k8s.io/ 前缀
2. 层级通过 key 名称区分：使用 {层级}-{属性} 格式（kebab-case）
3. 遵循 K8s 命名规范：与 LWS、Kueue 等 sigs.k8s.io 项目保持一致
4. 职责分离：Label 用于选择器，Annotation 用于配置，Env 用于运行时

### Labels 对照表

| 层级         | v1alpha1                                                     | v1alpha2                                      | 说明                                     |
| ------------ | ------------------------------------------------------------ | --------------------------------------------- | ---------------------------------------- |
| GroupSet     | `rolebasedgroupset.workloads.x-k8s.io/name`                  | `rbg.workloads.x-k8s.io/groupset-name`        | 标识资源所属的 RBGSet                    |
| GroupSet     | `rolebasedgroupset.workloads.x-k8s.io/rbg-index`             | `rbg.workloads.x-k8s.io/groupset-index`       | 标识 RBG 在 RBGSet 中的索引              |
| Group        | `rolebasedgroup.workloads.x-k8s.io/name`                     | `rbg.workloads.x-k8s.io/group-name`           | 标识资源所属的 RBG                       |
| Group        | `rolebasedgroup.workloads.x-k8s.io/group-unique-key`         | `rbg.workloads.x-k8s.io/group-uid`            | RBG 的唯一 UID（用于 topology affinity） |
| Group        | `rolebasedgroup.workloads.x-k8s.io/controller-revision-hash` | `rbg.workloads.x-k8s.io/group-revision`       | RBG 整体的 controller revision hash      |
| Role         | `rolebasedgroup.workloads.x-k8s.io/role`                     | `rbg.workloads.x-k8s.io/role-name`            | 标识资源所属的 Role                      |
| Role         | `role.rolebasedgroup.workloads.x-k8s.io/template-type`       | `rbg.workloads.x-k8s.io/role-type`            | Role 的模板类型                          |
| Role         | `rolebasedgroup.workloads.x-k8s.io/role-revision-hash-%s`    | `rbg.workloads.x-k8s.io/role-revision-%s`     | Role 级别的 revision hash                |
| RoleInstance | `instanceset.workloads.x-k8s.io/owner-uid`                   | `rbg.workloads.x-k8s.io/role-instance-owner`  | RoleInstance 所属的控制器 UID            |
| RoleInstance | `instanceset.workloads.x-k8s.io/instance-id`                 | `rbg.workloads.x-k8s.io/role-instance-id`     | RoleInstance 的唯一标识                  |
| RoleInstance | `instanceset.workloads.x-k8s.io/instance-name`               | `rbg.workloads.x-k8s.io/role-instance-name`   | RoleInstance 的名称                      |
| RoleInstance | `instance.workloads.x-k8s.io/instance-name`                  | `rbg.workloads.x-k8s.io/role-instance-name`   | RoleInstance 的名称                      |
| RoleInstance | `instanceset.workloads.x-k8s.io/specified-delete`            | `rbg.workloads.x-k8s.io/role-instance-delete` | 标记 RoleInstance 应被删除               |
| Component    | `instanceset.workloads.x-k8s.io/instance-component-id`       | `rbg.workloads.x-k8s.io/component-id`         | 组件在 Instance 内的唯一 ID              |
| Component    | `instanceset.workloads.x-k8s.io/instance-component-name`     | `rbg.workloads.x-k8s.io/component-name`       | 组件名称                                 |
| Component    | `instance.workloads.x-k8s.io/component-name`                 | `rbg.workloads.x-k8s.io/component-name`       | 组件名称                                 |
| Component    | `instance.workloads.x-k8s.io/component-id`                   | `rbg.workloads.x-k8s.io/component-id`         | 组件在 Instance 内的唯一 ID              |
| Component    | `component.rolebasedgroup.workloads.x-k8s.io/name`           | `rbg.workloads.x-k8s.io/component-name`       | 组件名称                                 |
| Component    | `component.rolebasedgroup.workloads.x-k8s.io/index`          | `rbg.workloads.x-k8s.io/component-id`         | 组件在 Instance 内的唯一 ID              |
| Component    | `component.rolebasedgroup.workloads.x-k8s.io/size`           | `rbg.workloads.x-k8s.io/component-size`       | 组件的副本数量                           |

### Annotations 对照表

| 层级         | v1alpha1                                                       | v1alpha2                                          | 说明                       |
| ------------ | -------------------------------------------------------------- | ------------------------------------------------- | -------------------------- |
| Group        | `rolebasedgroup.workloads.x-k8s.io/exclusive-topology`         | `rbg.workloads.x-k8s.io/group-exclusive-topology` | 声明独占调度的拓扑域       |
| Role         | `rolebasedgroup.workloads.x-k8s.io/role-size`                  | `rbg.workloads.x-k8s.io/role-size`                | Role 的副本规模            |
| Role         | `rolebasedgroup.workloads.x-k8s.io/disable-exclusive-topology` | `rbg.workloads.x-k8s.io/role-disable-exclusive`   | 禁用该 Role 的独占拓扑调度 |
| RoleInstance | `instance.rolebasedgroup.workloads.x-k8s.io/pattern`           | `rbg.workloads.x-k8s.io/role-instance-pattern`    | RoleInstance 的组织模式    |
| Lifecycle    | `lifecycle.workloads.x-k8s.io/state`                           | `rbg.workloads.x-k8s.io/lifecycle-state`          | 生命周期状态               |
| Lifecycle    | `lifecycle.workloads.x-k8s.io/timestamp`                       | `rbg.workloads.x-k8s.io/lifecycle-timestamp`      | 生命周期状态变更时间戳     |
| InPlace      | `workloads.x-k8s.io/inplace-update-state`                      | `rbg.workloads.x-k8s.io/inplace-state`            | 原地更新状态               |
| InPlace      | `workloads.x-k8s.io/inplace-update-grace`                      | `rbg.workloads.x-k8s.io/inplace-grace`            | 原地更新 grace period 配置 |

## Envs

| v1alpha1           | v1alpha2                     | 说明                         | 来源                                                                    | 说明                            |
| ------------------ | ---------------------------- | ---------------------------- | ----------------------------------------------------------------------- | ------------------------------- |
| GROUP_NAME         | RGB_GROUP_NAME               | RBG 的名称                   | 直接注入 rbg.Name                                                       |                                 |
| ROLE_NAME          | RGB_ROLE_NAME                | Role 的名称                  | 直接注入 role.Name                                                      |                                 |
| ROLE_INDEX         | -                            | Pod 在 Role 中的有序索引     | 下行 API: metadata.labels['apps.kubernetes.io/pod-index']               |
| INSTANCE_NAME      | RGB_INSTANCE_NAME            | RoleInstance 的名称          | 下行 API: metadata.labels['instance.workloads.x-k8s.io/instance-name']  |                                 |
| COMPONENT_NAME     | RGB_COMPONENT_NAME           | 组件名称                     | 下行 API: metadata.labels['instance.workloads.x-k8s.io/component-name'] |                                 |
| LWS_LEADER_ADDRESS | RGB_COMPONENT_LEADER_ADDRESS | Leader component 的 DNS 地址 | 计算: $(INSTANCE_NAME)-0.{svcName}.{namespace}                          | 仅用于多节点分布式推理/训练场景 |
| LWS_WORKER_INDEX   | RGB_COMPONENT_INDEX          | 组件在 Instance 内的索引     | 下行 API: metadata.labels['instance.workloads.x-k8s.io/component-id']   | 仅用于多节点分布式推理/训练场景 |
| LWS_GROUP_SIZE     | RGB_COMPONENT_SIZE           | Instance 内 Component 总数   | 下行 API: label rbg.workloads.x-k8s.io/component-size                   | 仅用于多节点分布式推理/训练场景 |

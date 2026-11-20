# 手工从 v1alpha1 迁移到 v1alpha2

本指南适用于 RBG `0.9.0`。该版本仍提供 `v1alpha1` API，存量对象可读写；每个 `v1alpha1` 请求都会返回 CRD 弃用警告，新的弃用 workload 类型使用会被拒绝。`v1alpha1` 将在 `0.10.0` 停止服务。

下文的存储版本重写适用于同时提供两个版本的四个 CRD：`RoleBasedGroup`、`RoleBasedGroupSet`、`RoleBasedGroupScalingAdapter` 和 `ClusterEngineRuntimeProfile`。前两者使用 conversion webhook；后两者使用 `None` 转换，且 v1alpha1 与 v1alpha2 schema 完全一致。workload 迁移覆盖特殊的 v1alpha1 `InstanceSet` 选择器；其他 v1alpha1 资源请按各自 CRD 弃用警告指定的替代版本迁移。

## 范围与权责

RBG 有意**不提供可执行的迁移命令或脚本**。迁移会修改正在运行的工作负载，并可能重建 Pod。操作者必须逐 Role 审查、选择灰度顺序、根据业务状态暂停或调整，并通过组织既有的变更管理流程提交修改。

本文中的 Kubernetes 示例仅用于对象检查，或由操作者逐个对象显式执行的对象写入。它们不是自动化迁移。如果组织使用 GitOps、策略引擎或其他部署流程，应使用该流程替代本文示例。

workload 类型内容迁移必须使用 `v1alpha2` 表达和写入路径。在 `v1alpha1` 中，`spec.roles[].workload` 是权威字段，转换会由它重新生成兼容注解。不要通过 `v1alpha1` 执行这种只修改注解的迁移编辑。

## 正在迁移的内容

迁移包含两条独立轨道：

1. **API 编码（v1alpha1 到 v1alpha2）。** 通过 `v1alpha1` 创建且从未更新过的对象，在 etcd 中可能仍按 `v1alpha1` 编码存储。存储版本重写会通过 `v1alpha2` 端点重写这些对象，内容保持不变。
2. **旧 workload 选择器。** `Deployment`、`StatefulSet` 和 `LeaderWorkerSet` Role 受兼容开关保护，必须迁移到 `RoleInstanceSet`。v1alpha1 `InstanceSet` 选择器不同：v0.7.0 及之后的控制器没有 InstanceSet reconciler，因此无论兼容开关如何，这类 Role 都无法 reconcile。不过 admission 仍会像对待其他弃用类型一样校验它：无论开关取值，新的 InstanceSet 使用都会被拒绝。

完成其中一条轨道并不代表另一条轨道也已完成。升级超过 `0.9.0` 前，必须分别检查两条轨道。

## 前置条件

- 将 RBG 控制器升级到 `0.9.0` 或更新版本，并确认控制器、conversion webhook 和 admission webhook 健康。
- 确认待迁移 Role 当前 ready，且没有其他 rollout 正在进行。`InstanceSet` Role 是例外：它从 v0.7.0 起就不再 reconcile，且不支持的 workload 类型会让整个 RoleBasedGroup 无法通过 preflight reconcile。
- 在提交任何有状态 Role 的迁移前，先完成 discovery ConfigMap 前置条件。从 v0.6.0 或更早版本升级上来的 RBG 处于 `legacy` 发现模式，显式带有 `legacy` 的 RoleBasedGroupSet 子对象也是如此；参见[discovery ConfigMap 前置条件](#discovery-configmap-前置条件)。
- 与自动扩缩容、流量管理、调度以及可能在迁移期间修改 RBG 的业务流程协调。
- 在变更窗口内冻结外部写入。
- 修改前保存当前对象，并记录 revision。

`controller.deprecatedWorkloadTypes.enabled` 在迁移窗口默认为 `true`。既有的 `Deployment`、`StatefulSet` 和 `LeaderWorkerSet` Role 会继续 reconcile，新使用会被拒绝。该模式（后文称为兼容模式）下 RBAC、watch 和孤儿清理保持生效，操作者可以迁移 Role 并删除旧工作负载。只有在 inventory 为空、reconcile 完成且旧 RBG-owned workload 已删除后，才设置为 `false`。

兼容模式下，存量 Role 只能保留自己当前的弃用 workload 类型；重命名 Role、新增 Role，或在弃用类型之间切换，都属于新使用并会被拒绝。请将 Role 迁移到 `RoleInstanceSet`。v1alpha1 schema 还会把缺失的 `spec.roles[].workload` 默认为 `apps/v1 StatefulSet`，因此一个没有显式 workload 的新 v1alpha1 Role 实际上也是新的 StatefulSet 使用。

## 1. 盘点受影响 Role

执行能覆盖两种 API 形态的盘点。命令必须成功退出；只有在这种情况下，空输出才表示没有 Role 继续选择旧 workload：

```bash
inventory="$(kubectl get rbg,rbgs -A -o json)" &&
printf '%s' "$inventory" | jq -r '.items[] | . as $o
  | (.spec.roles // .spec.groupTemplate.spec.roles // .spec.template.roles // [])[]
  | (if .workload.kind then "\(.workload.apiVersion)/\(.workload.kind)"
     else ((.annotations // {})["rbg.workloads.x-k8s.io/role-workload-type"] // "")
     end) as $wt
  | select($wt != "" and $wt != "workloads.x-k8s.io/v1alpha2/RoleInstanceSet")
  | "\($o.kind) \($o.metadata.namespace)/\($o.metadata.name) role=\(.name) \($wt)"'
```

该命令会列出所有选择器不是 v1alpha2 默认值的 Role，包括未知或拼写错误的值。只要某个值不在下表中，就一律按未知选择器处理，即使命令会像已知类型一样打印它。

`kubectl get rbg,rbgs` 返回 preferred `v1alpha2` 表达，因此命令通常读取注解 `rbg.workloads.x-k8s.io/role-workload-type`；`workload` 字段和 `spec.template.roles` 两个分支只在输出固定为 `v1alpha1` 时执行。Role 所在路径：

- `RoleBasedGroup`：`spec.roles`
- `RoleBasedGroupSet`：`spec.groupTemplate.spec.roles`

按下表解释盘点结果：

| workload 选择器 | 含义 | 动作 |
| --- | --- | --- |
| 缺失或 `workloads.x-k8s.io/v1alpha2/RoleInstanceSet` | 使用 v1alpha2 默认值。 | 不需要 workload 迁移。 |
| `apps/v1/Deployment` | Role 仍依赖 Deployment。 | 迁移到 `RoleInstanceSet`。 |
| `apps/v1/StatefulSet` | Role 仍依赖 StatefulSet。 | 迁移到 `RoleInstanceSet`。 |
| `leaderworkerset.x-k8s.io/v1/LeaderWorkerSet` | Role 仍依赖 LeaderWorkerSet。 | 迁移到 `RoleInstanceSet`。 |
| `workloads.x-k8s.io/v1alpha1/InstanceSet` | 该选择器从 v0.7.0 起不再 reconcile，并阻塞整个 RBG。 | 迁移到 `RoleInstanceSet`；之后手工删除旧 InstanceSet。 |
| 第一个字符之后不含 `/` 的值，例如 `RoleInstanceSet` 或 `StatefulSet` | 不是 `apiVersion/kind` 形式的选择器。无论它写的是什么，控制器都会回退为 `RoleInstanceSet`，因此该 Role 以 `RoleInstanceSet` 运行。新增 Role 或修改 Role 的取值时，admission 会拒绝它；已经存储该值的 Role 仍可写入。 | 与对象负责人一起审查。设置为 `workloads.x-k8s.io/v1alpha2/RoleInstanceSet` 或删除该注解；两者都保持该 Role 当前的运行方式。 |
| 其他值 | 没有 reconciler 支持该值，因此会阻塞整个 RBG。新增 Role 或修改 Role 的取值时，admission 会拒绝它；已经存储该值的 Role 仍可写入。 | 停止并与对象负责人审查，不要猜测。 |

盘点结果是影响面报告，不是 rollout 决策。操作者决定先迁移哪个 Role，以及何时暂停。

## 2. 逐 Role 手工迁移

### discovery ConfigMap 前置条件

discovery ConfigMap 是控制器创建的 Pod 发现信息（`config.yaml`），挂载到有状态 Role 的 Pod 上。v0.7.0 起，控制器为每个 RBG 维护一份与 RBG 同名的 ConfigMap。KEP-576 移除了旧的 `discovery-config-mode` 选择器：控制器现在为每个含 stateful role 的 RBG 维护 `<rbg>`。原先固定在 `legacy` 的 RBG 会在下一次 reconcile 时开始接收 `<rbg>`；Pod 模板不变，因为 v0.7.0 及之后的版本已经挂载 `<rbg>`。

序列化内容由注解 `rbg.workloads.x-k8s.io/discovery-config-version` 选择：

| 注解值 | ConfigMap 内容 |
| --- | --- |
| 缺失或 `v1alpha1` | 历史的全量逐实例枚举 |
| `v1` | KEP-576 的紧凑格式 |

mutating webhook 会把新建 RBG 默认为 `v1`；没有该注解的存量 RBG 保持 `v1alpha1`。创建时如需退回旧行为，可将注解设为 `v1alpha1`。RoleBasedGroupSet 应把注解放在 `spec.groupTemplate.annotations`：子 RBG 会继承该值，直接改子对象注解会被 set controller 覆盖。

迁移 stateful role 前，确认 RBG 级 ConfigMap 已存在：

```bash
kubectl get configmap RBG_NAME -n NAMESPACE
```

只修改注解也会触发 reconcile：RBG controller 会监听 discovery-config-version 注解。若 Role 已按步骤 3 设为 `Stateless`，本节不适用：无状态 Role 不挂载 discovery ConfigMap。

### 迁移操作

#### Deployment、StatefulSet、LeaderWorker Role

完成上述前置条件后，对每个 `Deployment`、`StatefulSet` 或 `LeaderWorkerSet` Role：

1. 保存当前 `v1alpha2` RBG 表达用于审计和审查：

   ```bash
   kubectl get rolebasedgroups.v1alpha2.workloads.x-k8s.io RBG_NAME -o yaml > RBG_NAME.pre-migration.yaml
   ```

2. 在 RoleBasedGroup 所在 namespace 内，识别当前下游工作负载拥有的 Pod。workload Kind 变化后，这些 Pod 会被重建：

   ```bash
   kubectl get pods -n NAMESPACE \
     -l rbg.workloads.x-k8s.io/group-name=RBG_NAME,rbg.workloads.x-k8s.io/role-name=ROLE_NAME \
     -o wide
   ```

3. 审查 Role 既有 `Pattern`：
   - Deployment 与 StatefulSet Role 使用 `standalonePattern`，LeaderWorkerSet Role 使用 `leaderWorkerPattern`；迁移后保持不变。
   - 保留既有 template source、replicas、size、patch、rollout strategy 和 restart policy 配置。
   - Pattern 缺失，或 Pattern 没有可解析的 template source，属于对象健康问题。请停止迁移，先通过独立变更修复或删除该 Role。
   - 如果既有 Pattern 与旧 workload 类型冲突，停止并与工作负载负责人设计变更；不要仅为通过校验而删除 workload 选择器。
   - 确定 RoleInstanceSet 的实例组织模式。迁移后的 Role 若没有 `rbg.workloads.x-k8s.io/role-instance-pattern` 注解，默认为 `Stateful`。如果 Role 迁移自 Deployment，且不需要按实例的身份——不需要稳定的 ordinal 名称、不使用排除序数、不需要有状态 membership——可以在该 Role 上把注解设为 `Stateless`。无状态组织更贴近 Deployment 语义，并且不会挂载 discovery ConfigMap。取值区分大小写：`Stateless` 或 `Stateful`。

4. 在 `v1alpha2` 表达中只编辑选中的一个 Role：
   - 从该 Role 的 annotations 中删除 `rbg.workloads.x-k8s.io/role-workload-type`。
   - 其他 Role 的 annotations 与 spec 保持不变。
   - 保留上述正确 Pattern。
   - 对冗余的 `RoleInstanceSet` 注解，只删除注解，Pattern 保持不变。

5. 通过正常部署流程提交这个单 Role 的 `v1alpha2` 变更。如果配置源仍是 v1alpha1 清单，请先审查并把配置源转换为 v1alpha2。不要把多个 Role 合并成一次变更。

6. 观察替换过程：
   - 控制器创建 `RoleInstanceSet`，并在同一轮 reconcile 中把旧 Deployment、StatefulSet 或 LeaderWorkerSet 判定为孤儿删除。即使替代对象创建失败，删除也会发生。
   - 在 `RoleInstanceSet` ready 之前，该 Role 没有任何 Pod。没有重叠或 surge 阶段。
   - 如果新 Pod 停留在 `ContainerCreating` 并报 `configmap "RBG_NAME" not found`，说明 discovery ConfigMap 前置条件未完成；先补做前置条件，再重试迁移。
   - 该 Role 的全部 Pod 会重建。

7. 继续前验证该 Role：

   ```bash
   kubectl get rolebasedgroup RBG_NAME -n NAMESPACE -o wide
   kubectl describe rolebasedgroup RBG_NAME -n NAMESPACE
   kubectl get events -n NAMESPACE --field-selector involvedObject.name=RBG_NAME
   ```

   确认旧 workload 已删除，而不只是替代对象 ready。同时检查 Pod，以及对该业务真正重要的流量、readiness、缓存预热或其他健康信号。

8. 只要业务状态需要，就停止、暂停或调整 rollout。不要因为对象可写而自动继续下一个 Role。

#### v1alpha1 InstanceSet Role

v0.6.0 及更早版本没有 `RoleInstanceSet`（v0.7.0 引入）：从这些版本升级的集群，先升级到 `0.9.0`，再迁移。

无论兼容开关取值如何，v1alpha1 `InstanceSet` 选择器都不会 reconcile：它从 v0.7.0 起就没有 reconciler，且不支持的 workload 类型会让整个 RoleBasedGroup preflight 失败，而不是把失败隔离在单个 Role 内。

兼容开关仍会像对待其他弃用类型一样，在 admission 阶段校验它。取值为 `true` 时，已有的 InstanceSet Role 仍可写，但新使用会被拒绝，包括把保存的 InstanceSet 选择器恢复到已迁移的 Role 上；这样的选择器本来也不会 reconcile，因此不要把恢复方案建立在它之上。取值为 `false` 时，只要还有 Role 使用 InstanceSet 选择器，创建和更新请求都会被拒绝。

迁移时把 Role 迁移到 `RoleInstanceSet`。孤儿清理不覆盖 InstanceSet：替代 `RoleInstanceSet` ready 后，旧 InstanceSet 及其 Pod 会与替代对象并存，需要显式删除。请与工作负载负责人审查该删除操作，并将其纳入恢复方案。

### RoleBasedGroupSet rollout 顺序

`RoleBasedGroupSet` 不是独立的逐子对象灰度机制。其控制器会比较生成的子对象与 `groupTemplate`，并把模板 Role 写回子对象。因此，在 set 模板仍携带旧 workload 类型时迁移某个子对象并不稳定：

- 如果 set 控制器更新已迁移的子对象，恢复旧 Role 属于新的弃用使用，admission guard 会拒绝。
- 如果 set 控制器按未迁移模板删除并重建子对象，重建的子对象会通过 set 子对象的创建时豁免继承旧类型，该 Role 必须再次迁移。

先迁移 set 模板。每个子对象都会在同一轮 reconcile 中删除旧 workload，因此传播的变更会让所有子对象的该 Role 同时降为零 Pod。请把它规划为该 Role 在整个 set 内的完全中断，而不是灰度 rollout。迁移前先按 discovery ConfigMap 前置条件的说明，在模板上设置发现模式注解。如果确实需要逐子对象灰度，必须使用单独审查过的方案，协调或隔离子对象与 set reconcile 的关系。不要假设在父模板未更新时，某个子对象的迁移可以永久保持。

## 3. 将对象重写为 v1alpha2 存储编码

只有所有已识别旧 workload 选择器都已迁移、确认 ready 并完成清理后，才开始当前步骤。

先检查哪些 CRD 仍记录 `v1alpha1`。v0.7.0 及以后首次安装的集群从未用 v1alpha1 存储这些资源，其列表已经是 `[v1alpha2]`，以下步骤对它们是空操作：

```bash
kubectl get customresourcedefinition \
  rolebasedgroups.workloads.x-k8s.io \
  rolebasedgroupsets.workloads.x-k8s.io \
  rolebasedgroupscalingadapters.workloads.x-k8s.io \
  clusterengineruntimeprofiles.workloads.x-k8s.io \
  -o custom-columns=CRD:.metadata.name,STORED_VERSIONS:.status.storedVersions
```

对每个仍列出 `v1alpha1` 的 CRD，重写该 kind 的**每一个**对象。API 无法报告单个对象在 etcd 中的编码，因此不能挑选“仍需重写”的对象；跳过任何对象都会让它在 `0.10.0` 移除 v1alpha1 后无法读取：

1. 通过 `v1alpha2` 端点读取 CR 对象（非 CRD）。
2. 保存并审查返回对象。
3. 通过 `v1alpha2` 端点把同一对象写回。使用当前 resourceVersion 的普通 update 即可；spec 与 status 不应改变。
4. 确认写入成功后，再处理下一个对象。

一种显式且不批量执行的 Kubernetes 流程是：

```bash
kubectl get rolebasedgroups.v1alpha2.workloads.x-k8s.io RBG_NAME -n NAMESPACE -o yaml > RBG_NAME.storage-rewrite.yaml
# 审查 RBG_NAME.storage-rewrite.yaml 后写回：
kubectl replace -f RBG_NAME.storage-rewrite.yaml
```

RoleBasedGroupSet、RoleBasedGroupScalingAdapter 和 ClusterEngineRuntimeProfile 使用等价的、完整限定的 `v1alpha2` 资源；最后一类是 cluster-scoped，其命令不使用 namespace。如果写入因对象变化而冲突，重新获取副本、审查后再决定是否重试。

不要通过 `v1alpha1` API 获取和修改对象：该路径可能重新引入 `role-workload-type` 兼容注解，为后续检查制造噪音。

所有对象重写完成后，用 `kubectl patch --subresource=status` 把每个 CRD 的 `status.storedVersions` 替换为 `["v1alpha2"]`（需要 kubectl 1.24 或更新版本）。重写对象不会自动从该列表移除 `v1alpha1`：它是 CRD 级历史记录，只能人工替换：

```bash
kubectl patch customresourcedefinition rolebasedgroups.workloads.x-k8s.io \
  --subresource=status --type=merge \
  -p '{"status":{"storedVersions":["v1alpha2"]}}'

kubectl patch customresourcedefinition rolebasedgroupsets.workloads.x-k8s.io \
  --subresource=status --type=merge \
  -p '{"status":{"storedVersions":["v1alpha2"]}}'

kubectl patch customresourcedefinition rolebasedgroupscalingadapters.workloads.x-k8s.io \
  --subresource=status --type=merge \
  -p '{"status":{"storedVersions":["v1alpha2"]}}'

kubectl patch customresourcedefinition clusterengineruntimeprofiles.workloads.x-k8s.io \
  --subresource=status --type=merge \
  -p '{"status":{"storedVersions":["v1alpha2"]}}'
```

然后检查全部四个双版本 CRD：

```bash
kubectl get customresourcedefinition \
  rolebasedgroups.workloads.x-k8s.io \
  rolebasedgroupsets.workloads.x-k8s.io \
  rolebasedgroupscalingadapters.workloads.x-k8s.io \
  clusterengineruntimeprofiles.workloads.x-k8s.io \
  -o custom-columns=CRD:.metadata.name,STORED_VERSIONS:.status.storedVersions
```

每个值都必须只包含 `v1alpha2`。如果 status 更新前仍包含 `v1alpha1`，不要直接从该列表推断哪些对象仍是旧编码；应先重新确认重写清单。只要仍有对象按 `v1alpha1` 编码存储，就绝不能移除历史条目。

## 4. 结束迁移窗口

1. 重新执行 Role 盘点。命令必须成功执行且输出为空。
2. 确认 reconcile 已完成，没有 RBG-owned Deployment、StatefulSet 或 LeaderWorkerSet 残留，并确认所有旧 v1alpha1 `InstanceSet` 也已删除。
3. 根据重写记录确认：凡是所属 CRD 曾列出 `v1alpha1` 的 kind，其每个对象都已完成重写；API 无法报告对象编码，完整性只能来自记录本身。然后按上述说明更新并验证全部四个 `status.storedVersions` 值。
4. 设置 `controller.deprecatedWorkloadTypes.enabled=false`，然后确认控制器健康，且普通 v1alpha2 更新不会被弃用 gate 拒绝。
5. 将清单、客户端和自动化迁移到 `v1alpha2`。
6. 只有在与业务负责人约定的恢复窗口结束后，才清理保存的迁移产物。

## 风险与失败模式

- **Pod 重建：** workload Kind 变化对选定 Role 是破坏性变更，迁移期间该 Role 的全部 Pod 可能重建。
- **切换期间零容量：** 旧 workload 会在创建 `RoleInstanceSet` 的同一轮 reconcile 中删除，即使替代对象创建失败也会删除。替代对象 ready 之前该 Role 没有任何 Pod；没有 surge 或重叠期。
- **部分 ready：** 每次只迁移一个 Role，不要自动化全部 Role。
- **业务状态：** 仅 ready 可能不足。数据库、缓存、会话状态、流量摘除和调度行为必须由业务负责人审查。
- **旧工作负载残留：** 兼容模式开启时，Role 切换到 `RoleInstanceSet` 后，孤儿清理会删除旧 Deployment、StatefulSet 或 LeaderWorkerSet。最终切断前必须验证清理已完成。除非控制器无法清理，否则不要手工删除。
- **InstanceSet 残留：** 孤儿清理不会删除旧 v1alpha1 InstanceSet。替代对象 ready 后，旧对象的 Pod 会一直运行，直到操作者删除旧对象。
- **存储幽灵对象：** `status.storedVersions` 记录曾经使用过的存储版本。必须先确认对象重写完整，再从列表中移除 `v1alpha1`；如果旧编码对象仍在而移除 API 版本，这些对象可能无法读取。
- **静默类型回落：** 未完成内容迁移就直接删除 workload-type 注解，会使 Role 回落到 `RoleInstanceSet`。只有在 Pattern 和业务影响都经过审查后，才应执行该变更。
- **Set 级传播：** 更新 RoleBasedGroupSet 模板可能迁移所有生成的子对象，且它们的旧 workload 会在同一轮 reconcile 中删除。应把它视为该 Role 在整个 set 内的完全中断，而不是长期灰度单个子对象的方式。

# 手工从 v1alpha1 迁移到 v1alpha2

本指南适用于 RBG `0.9.0`。该版本仍提供 `v1alpha1` API，存量对象可读写；每个 `v1alpha1` 请求都会返回 CRD 弃用警告，新的弃用 workload 类型使用会被拒绝。`v1alpha1` 将在 `0.10.0` 停止服务。

下文的存储版本重写适用于 `RoleBasedGroup` 和 `RoleBasedGroupSet`，即使用 conversion webhook 的两个 CRD。workload 迁移同时覆盖特殊的 v1alpha1 `InstanceSet` 选择器。对其他 v1alpha1 资源，请使用 CRD 弃用警告中指定的替代版本，并在修改清单或客户端前审查 v1alpha2 schema。

## 范围与权责

RBG 有意**不提供可执行的迁移命令或脚本**。迁移会修改正在运行的工作负载，并可能重建 Pod。操作者必须逐 role 审查、选择灰度顺序、根据业务状态暂停或调整，并通过组织既有的变更管理流程提交修改。

本文中的 Kubernetes 示例仅用于对象检查，或由操作者逐个对象显式执行的对象写入。它们不是自动化迁移。如果组织使用 GitOps、策略引擎或其他部署流程，应使用该流程替代本文示例。

workload 类型内容迁移必须使用 `v1alpha2` 表达和写入路径。在 `v1alpha1` 中，`spec.roles[].workload` 是权威字段，转换会由它重新生成兼容注解。不要通过 `v1alpha1` 执行这种只修改注解的迁移编辑。

## 正在迁移的内容

迁移包含两条独立轨道：

1. **API 编码（v1alpha1 到 v1alpha2）。** 通过 `v1alpha1` 创建且从未更新过的对象，在 etcd 中可能仍按 `v1alpha1` 编码存储。存储版本迁移会通过 `v1alpha2` 端点重写这些对象，内容保持不变。
2. **旧 workload 选择器。** `Deployment`、`StatefulSet` 和 `LeaderWorkerSet` role 受兼容开关保护，必须迁移到 `RoleInstanceSet`。v1alpha1 `InstanceSet` 选择器不同：v0.7.0 及之后的控制器没有 InstanceSet reconciler，因此无论兼容开关如何，这类 role 都无法 reconcile。

完成其中一条轨道并不代表另一条轨道也已完成。升级超过 `0.9.0` 前，必须分别检查两条轨道。

## 前置条件

- 将 RBG 控制器升级到 `0.9.0` 或更新版本，并确认控制器、conversion webhook 和 admission webhook 健康。
- 确认待迁移 role 当前 ready，且没有其他 rollout 正在进行。`InstanceSet` role 是例外：它从 v0.7.0 起就不再 reconcile，且不支持的 workload 类型会让整个 RoleBasedGroup 无法通过 preflight reconcile。
- 确认每个 role 已有 Pattern，并且 `template` 或 `templateRef` 可解析。缺少这些内容的 role 本身已无法 reconcile；请先通过独立变更修复或删除该 role，再进行迁移。
- 与自动扩缩容、流量管理、调度以及可能在迁移期间修改该 role 的业务流程协调。
- 在变更窗口内冻结外部写入。
- 修改前保存当前对象，并记录 revision。

`controller.deprecatedWorkloadTypes.enabled` 在迁移窗口默认为 `true`。既有的 `Deployment`、`StatefulSet` 和 `LeaderWorkerSet` role 会继续 reconcile，新使用会被拒绝。该模式下 RBAC、watch 和孤儿清理保持生效，操作者可以迁移 role 并删除旧工作负载。只有在 inventory 为空、reconcile 完成且旧 RBG-owned workload 已删除后，才设置为 `false`。

兼容规则是 type-exact。存量 role 只保留自己当前的弃用 workload 类型；重命名 role、新增 role，或在弃用类型之间切换，都属于新使用并会被拒绝。请将 role 迁移到 `RoleInstanceSet`。v1alpha1 schema 还会把缺失的 `spec.roles[].workload` 默认为 `apps/v1 StatefulSet`，因此一个没有显式 workload 的新 v1alpha1 role 实际上也是新的 StatefulSet 使用。

## 1. 盘点受影响 role

执行能覆盖两种 API 形态的盘点。命令必须成功退出；只有在这种情况下，空输出才表示没有 role 继续选择旧 workload：

```bash
inventory="$(kubectl get rbg,rbgs -A -o json)" &&
printf '%s' "$inventory" | jq -r '.items[] | . as $o
  | (.spec.roles // .spec.groupTemplate.spec.roles // .spec.template.roles // [])[]
  | (if .workload.kind then "\(.workload.apiVersion)/\(.workload.kind)"
     else ((.annotations // {})["rbg.workloads.x-k8s.io/role-workload-type"] // "")
     end) as $wt
  | select(($wt | test("/(Deployment|StatefulSet|LeaderWorkerSet)$"))
           or $wt == "workloads.x-k8s.io/v1alpha1/InstanceSet")
  | "\($o.kind) \($o.metadata.namespace)/\($o.metadata.name) role=\(.name) \($wt)"'
```

对 v1alpha1 role，直接 `workload` 字段优先于 stale annotation。对没有 `workload` 字段的 v1alpha2 role，命令读取 `rbg.workloads.x-k8s.io/role-workload-type`。Role 所在路径为：

- `RoleBasedGroup`：`spec.roles`
- `v1alpha2 RoleBasedGroupSet`：`spec.groupTemplate.spec.roles`
- `v1alpha1 RoleBasedGroupSet`：`spec.template.roles`

按下表解释盘点结果：

| workload 选择器 | 含义 | 动作 |
| --- | --- | --- |
| 缺失或 `workloads.x-k8s.io/v1alpha2/RoleInstanceSet` | 使用 v1alpha2 默认值。 | 不需要 workload 迁移。 |
| `apps/v1/Deployment` | role 仍依赖 Deployment。 | 迁移到 `RoleInstanceSet`。 |
| `apps/v1/StatefulSet` | role 仍依赖 StatefulSet。 | 迁移到 `RoleInstanceSet`。 |
| `leaderworkerset.x-k8s.io/v1/LeaderWorkerSet` | role 仍依赖 LeaderWorkerSet。 | 迁移到 `RoleInstanceSet`。 |
| `workloads.x-k8s.io/v1alpha1/InstanceSet` | 该选择器从 v0.7.0 起不再 reconcile，并阻塞整个 RBG。 | 迁移到 `RoleInstanceSet`；之后手工删除旧 InstanceSet。 |
| 其他值 | 未知兼容值。 | 停止并与对象负责人审查，不要猜测。 |

盘点结果是影响面报告，不是 rollout 决策。操作者决定先迁移哪个 role，以及何时暂停。

## 2. 逐 role 手工迁移

对每个 `Deployment`、`StatefulSet` 或 `LeaderWorkerSet` role：

1. 保存当前 `v1alpha2` 表达用于审计和审查：

   ```bash
   kubectl get rolebasedgroups.v1alpha2.workloads.x-k8s.io RBG_NAME -o yaml > RBG_NAME.pre-migration.yaml
   ```

2. 识别当前下游工作负载拥有的 Pod。workload Kind 变化后，这些 Pod 会被重建：

   ```bash
   kubectl get pods -A \
     -l rbg.workloads.x-k8s.io/group-name=RBG_NAME,rbg.workloads.x-k8s.io/role-name=ROLE_NAME \
     -o wide
   ```

3. 审查 role 既有 `Pattern`：
   - Deployment 与 StatefulSet 使用 `standalonePattern`。
   - LeaderWorkerSet 使用 `leaderWorkerPattern`。
   - 保留既有 template source、replicas、size、patch、rollout strategy 和 restart policy 配置。
   - Pattern 缺失，或 Pattern 没有可解析的 template source，属于对象健康问题。请停止迁移，先通过独立变更修复或删除该 role。
   - 如果既有 Pattern 与旧 workload 类型冲突，停止并与工作负载负责人设计变更；不要仅为通过校验而删除 workload 选择器。

4. 在 `v1alpha2` 表达中只编辑选中的一个 role：
   - 从该 role 的 annotations 中删除 `rbg.workloads.x-k8s.io/role-workload-type`。
   - 其他 role 的 annotations 与 spec 保持不变。
   - 保留上述正确 Pattern。
   - 对冗余的 `RoleInstanceSet` 注解，只删除注解，Pattern 保持不变。

5. 通过正常部署流程提交这个单 role 的 `v1alpha2` 变更。如果配置源仍是 v1alpha1 清单，请先审查并把配置源转换为 v1alpha2。不要把多个 role 合并成一次变更。

6. 观察替换过程：
   - 控制器创建 `RoleInstanceSet`。
   - 控制器把旧 Deployment、StatefulSet 或 LeaderWorkerSet 判定为孤儿并删除。
   - 该 role 的全部 Pod 会重建。

7. 继续前验证该 role：

   ```bash
   kubectl get rolebasedgroup RBG_NAME -o wide
   kubectl describe rolebasedgroup RBG_NAME
   kubectl get events --field-selector involvedObject.name=RBG_NAME
   ```

   确认旧 workload 已删除，而不只是替代对象 ready。同时检查 Pod，以及对该业务真正重要的流量、readiness、缓存预热或其他健康信号。

8. 只要业务状态需要，就停止、暂停或调整 rollout。不要因为对象可写而自动继续下一个 role。

### v1alpha1 InstanceSet role

v1alpha1 `InstanceSet` 选择器不受兼容开关保护。它从 v0.7.0 起就不再 reconcile，且不支持的 workload 类型会让整个 RoleBasedGroup preflight 失败，而不是把失败隔离在单个 role 内。

如果集群运行 v0.6.0 或更早版本，升级前没有可用的 `RoleInstanceSet`。请先升级到 v0.7.0 或更新版本，然后尽快把 role 迁移到 `RoleInstanceSet`。如果集群已经在 v0.7.0 或更新版本上，该 role 本来就无法 reconcile。

替代 `RoleInstanceSet` ready 后，显式删除旧 `InstanceSet`。孤儿清理不覆盖 InstanceSet，因此在旧对象删除前，它的 Pod 会继续与替代 workload 并存。请与工作负载负责人审查该删除操作，并将其纳入恢复方案。

### RoleBasedGroupSet rollout 顺序

`RoleBasedGroupSet` 不是独立的逐子对象灰度机制。其控制器会比较生成的子对象与 `groupTemplate`，并把模板 role 写回子对象。因此，在 set 模板仍携带旧 workload 类型时迁移某个子对象并不稳定：控制器可能尝试恢复旧 role，而 admission guard 会拒绝这种新引入。

先迁移 set 模板。这可能把变更传播到所有剩余子对象，因此这是 set 级 rollout 决策，而不是单 role 变更。如果确实需要逐子对象灰度，必须使用单独审查过的方案，协调或隔离子对象与 set reconcile 的关系。不要假设在父模板未更新时，某个子对象的迁移可以永久保持。

## 回滚

每个 role 迁移前，保留不可变对象副本、resourceVersion，以及部署流程使用的 revision 历史。这些记录仍可用于审计和恢复非 workload 字段，但不能让 workload 类型变更变得可逆。

一旦 role 已切换到 `RoleInstanceSet`，恢复保存的 Deployment、StatefulSet、LeaderWorkerSet 或 InstanceSet 选择器就是一次新的弃用使用。即使 `controller.deprecatedWorkloadTypes.enabled=true`，validator 也会拒绝该写入；兼容模式保留存量 role，但不会授权在迁移后重新引入弃用类型。在业务负责人批准 `RoleInstanceSet` rollout 失败后的恢复方案之前，不要开始迁移。

如果旧 workload 已被删除，不要手工用同名重建。应让控制器管理替代 workload。提交迁移后的恢复必须使用事先约定的应急方案，而不是简单恢复弃用 workload 类型。

## 3. 将对象重写为 v1alpha2 存储编码

只有所有已识别旧 workload 选择器都已迁移、确认 ready 并完成清理后，才开始这条轨道。

对每个 RoleBasedGroup 和 RoleBasedGroupSet：

1. 通过 `v1alpha2` 端点读取对象。
2. 保存并审查返回对象。
3. 通过 `v1alpha2` 端点把同一对象写回。使用当前 resourceVersion 的普通 update 即可；spec 与 status 不应改变。
4. 确认写入成功后，再处理下一个对象。

一种显式且不批量执行的 Kubernetes 流程是：

```bash
kubectl get rolebasedgroups.v1alpha2.workloads.x-k8s.io RBG_NAME -o yaml > RBG_NAME.storage-rewrite.yaml
# 审查 RBG_NAME.storage-rewrite.yaml 后写回：
kubectl replace -f RBG_NAME.storage-rewrite.yaml
```

RoleBasedGroupSet 使用等价的、完整限定的 `v1alpha2` 资源。如果写入因对象变化而冲突，重新获取副本、审查后再决定是否重试。

不要通过 `v1alpha1` 往返对象：该路径可能重新引入 `role-workload-type` 兼容注解，为后续检查制造噪音。

所有对象重写完成后，必须根据迁移清单独立确认重写集合已经完整。`status.storedVersions` 是 CRD 级历史记录，不是逐对象清单；对象重写后它也不会自动丢弃 `v1alpha1`。重复写对象不能移除该列表中的历史条目。

只有在完整重写得到确认后，才通过明确审查过的变更更新每个 CRD status：

```bash
kubectl patch customresourcedefinition rolebasedgroups.workloads.x-k8s.io \
  --subresource=status --type=merge \
  -p '{"status":{"storedVersions":["v1alpha2"]}}'

kubectl patch customresourcedefinition rolebasedgroupsets.workloads.x-k8s.io \
  --subresource=status --type=merge \
  -p '{"status":{"storedVersions":["v1alpha2"]}}'
```

然后检查两个启用转换的 CRD：

```bash
kubectl get customresourcedefinition \
  rolebasedgroups.workloads.x-k8s.io \
  rolebasedgroupsets.workloads.x-k8s.io \
  -o custom-columns=CRD:.metadata.name,STORED_VERSIONS:.status.storedVersions
```

每个值都必须只包含 `v1alpha2`。如果 status 更新前仍包含 `v1alpha1`，不要直接从该列表推断哪些对象仍是旧编码；应先重新确认重写清单。只要仍有对象按 `v1alpha1` 编码存储，就绝不能移除历史条目。

## 4. 结束迁移窗口

1. 重新执行 role 盘点。命令必须成功执行且输出为空。
2. 确认 reconcile 已完成，没有 RBG-owned Deployment、StatefulSet 或 LeaderWorkerSet 残留，并确认所有旧 v1alpha1 `InstanceSet` 也已删除。
3. 根据迁移清单确认每个 RoleBasedGroup 和 RoleBasedGroupSet 均已重写，然后按上述说明更新并验证两个 `status.storedVersions` 值。
4. 设置 `controller.deprecatedWorkloadTypes.enabled=false`，然后确认控制器健康，且普通 v1alpha2 更新不会被弃用 gate 拒绝。
5. 将清单、客户端和自动化迁移到 `v1alpha2`。
6. 只有在与业务负责人约定的回滚窗口结束后，才清理保存的迁移产物。

## 风险与失败模式

- **Pod 重建：** workload Kind 变化对选定 role 是破坏性变更，迁移期间该 role 的全部 Pod 可能重建。
- **部分 ready：** 每次只迁移一个 role，不要自动化全部 role。
- **业务状态：** 仅 ready 可能不足。数据库、缓存、会话状态、流量摘除和调度行为必须由业务负责人审查。
- **旧工作负载残留：** 兼容模式开启时，role 切换到 `RoleInstanceSet` 后，孤儿清理会删除旧 Deployment、StatefulSet 或 LeaderWorkerSet。最终切断前必须验证清理已完成。除非控制器无法清理，否则不要手工删除，因为手工删除可能与替代对象创建竞争。
- **InstanceSet 残留：** 孤儿清理不会删除旧 v1alpha1 InstanceSet。替代对象 ready 后，旧对象的 Pod 会一直运行，直到操作者删除旧对象。
- **存储幽灵对象：** `status.storedVersions` 记录曾经使用过的存储版本。必须先确认对象重写完整，再从列表中移除 `v1alpha1`；如果旧编码对象仍在而移除 API 版本，这些对象可能无法读取。
- **静默类型回落：** 未完成内容迁移就直接删除 workload-type 注解，会使 role 回落到 `RoleInstanceSet`。只有在 Pattern 和业务影响都经过审查后，才应执行该变更。
- **Set 级传播：** 更新 RoleBasedGroupSet 模板可能迁移所有生成的子对象。应把它视为 set 级 rollout，而不是长期灰度单个子对象的方式。

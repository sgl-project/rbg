# 手工从 v1alpha1 迁移到 v1alpha2

本指南适用于 RBG `0.9.0`。该版本仍继续提供 `v1alpha1` API，存量对象可读写；但每次写入都会返回 API Warning，且新的弃用 workload 类型使用会被拒绝。`v1alpha1` 将在 `0.10.0` 停止服务。

## 范围与权责

RBG 有意**不提供可执行的迁移命令或脚本**。迁移会修改正在运行的工作负载，并可能重建 Pod。操作者必须逐 role 审查、决定灰度顺序、根据业务状态暂停或调整，并通过组织既有的变更管理流程提交修改。

本文中的 Kubernetes 示例仅用于对象检查，或由操作者逐个对象显式执行的对象写入。它们不是自动化迁移。如果组织使用 GitOps、策略引擎或其他部署流程，应使用该流程替代本文示例。
workload 类型内容迁移必须使用 `v1alpha2` 表达和写入路径。`v1alpha1` 仅为兼容保留；在该版本中 `spec.roles[].workload` 是权威字段，转换会重新生成兼容注解。不要通过 `v1alpha1` 执行这种只修改注解的迁移编辑。

## 正在迁移的内容

迁移包含两条独立轨道：

1. **API 编码（v1alpha1 到 v1alpha2）。**  
   通过 `v1alpha1` 创建且从未更新过的对象，在 etcd 中可能仍按 `v1alpha1` 编码存储。存储版本迁移会通过 `v1alpha2` 端点重写这些对象，内容保持不变。

2. **弃用 workload 类型（Deployment、StatefulSet、LeaderWorkerSet）。**  
   Role 可通过 `rbg.workloads.x-k8s.io/role-workload-type` 选择旧 workload Kind。替代方案是 `v1alpha2` 默认的 `RoleInstanceSet`。改变 workload Kind 会重建该 role 的 Pod。

完成其中一条轨道并不代表另一条轨道也已完成。升级超过 `0.9.0` 前，必须分别检查两条轨道。

## 前置条件

- 将 RBG 控制器升级到 `0.9.0` 或更新版本，并确认控制器与 webhook 健康。
- 确认待迁移 role 当前 ready，且没有其他 rollout 正在进行。
- 确认每个 role 已有 Pattern，并且 `template` 或 `templateRef` 可解析。缺少这些内容的 role 本身已无法 reconcile；请先通过独立变更修复或删除该 role，再进行迁移。
- 与自动扩缩容、流量管理、调度以及可能在迁移期间修改该 role 的业务流程协调。
- 在变更窗口内冻结外部写入。
- 修改前保存当前对象，并记录 revision。

`controller.deprecatedWorkloadTypes.enabled` 在迁移窗口默认为 `true`。已经使用弃用 workload 类型的存量 role 会继续 reconcile；新 role，以及原本未使用弃用类型的 role，会被拒绝。该模式下 RBAC、watch 和孤儿清理保持生效，操作者可以迁移 role 并删除旧工作负载。只有在所有 role 迁移完成后才设置为 `false`；这是最终切断点，所有残留使用都会被拒绝。

存量 role 按 role 名称识别。兼容模式开启期间，已有 role 可以在弃用 workload 类型之间切换（例如 StatefulSet 切换为 Deployment）；重命名的 role 或新增 role 会被视为新使用并被拒绝。

## 1. 盘点受影响 role

审查当前对象和 role 注解：

```bash
kubectl get rolebasedgroups -A -o yaml
kubectl get rolebasedgroupsets -A -o yaml
```

Role 所在路径：

- `RoleBasedGroup`：`spec.roles`
- `v1alpha2 RoleBasedGroupSet`：`spec.groupTemplate.spec.roles`
- `v1alpha1 RoleBasedGroupSet`：`spec.template.roles`

逐个 role 检查 `rbg.workloads.x-k8s.io/role-workload-type`：

| 注解 | 含义 | 动作 |
| --- | --- | --- |
| 缺失 | 使用默认 `RoleInstanceSet`。 | 不需要 workload 迁移。 |
| `workloads.x-k8s.io/v1alpha2/RoleInstanceSet` | 等价于默认值。 | 可选择删除冗余注解；保持既有 Pattern 不变。 |
| `apps/v1/Deployment` | role 仍依赖 Deployment。 | 需要手工 workload 迁移。 |
| `apps/v1/StatefulSet` | role 仍依赖 StatefulSet。 | 需要手工 workload 迁移。 |
| `leaderworkerset.x-k8s.io/v1/LeaderWorkerSet` | role 仍依赖 LeaderWorkerSet。 | 需要手工 workload 迁移。 |
| 其他值 | 未知兼容值。 | 停止并与对象负责人一起审查，不要猜测。 |

该盘点是影响面报告，不是 rollout 决策。操作者决定先迁移哪个 role，以及何时暂停。

## 2. 逐 role 手工迁移

对每个使用弃用 workload 类型的 role：

1. 保存当前 `v1alpha2` 表达用于审计和审查。普通 Kubernetes 流程可以使用：

   ```bash
   kubectl get rolebasedgroups.v1alpha2.workloads.x-k8s.io RBG_NAME -o yaml > RBG_NAME.pre-migration.yaml
   ```

2. 识别当前下游工作负载拥有的 Pod。workload Kind 变化后，这些 Pod 会被重建。例如：

   ```bash
   kubectl get pods -A \
     -l rbg.workloads.x-k8s.io/group-name=RBG_NAME,rbg.workloads.x-k8s.io/role-name=ROLE_NAME \
     -o wide
   ```

3. 审查 role 既有 `Pattern`：
   - Deployment 与 StatefulSet 使用 `standalonePattern`。
   - LeaderWorkerSet 使用 `leaderWorkerPattern`。
   - 保留既有 template source、replicas、size、patch、rollout strategy 和 restart policy 配置。
   - Pattern 缺失，或 Pattern 没有可解析的 template source，属于对象自身的健康问题，不是迁移步骤。请停止迁移，先通过独立变更修复或删除该 role。
   - 如果既有 Pattern 与旧 workload 类型冲突，停止并与工作负载负责人设计变更；不要仅为通过校验而删除注解。

4. 在 `v1alpha2` 表达中只编辑选中的一个 role：
   - 从该 role 的 annotations 中删除 `rbg.workloads.x-k8s.io/role-workload-type`。
   - 其他 role 的 annotations 与 spec 保持不变。
   - 保留上述正确 Pattern。
   - 对冗余的 `RoleInstanceSet` 注解，只删除注解，Pattern 保持不变。

5. 通过正常部署流程提交这个单 role 的 `v1alpha2` 变更。如果配置源仍是 v1alpha1 清单，请先审查并把配置源转换为 v1alpha2。不要把多个 role 合并成一次变更。

6. 观察替换过程：
   - 控制器把旧 Deployment、StatefulSet 或 LeaderWorkerSet 判定为孤儿并删除。
   - 控制器创建 RoleInstanceSet。
   - 该 role 的全部 Pod 会重建。

7. 继续前验证该 role：

   ```bash
   kubectl get rolebasedgroup RBG_NAME -o wide
   kubectl describe rolebasedgroup RBG_NAME
   kubectl get events --field-selector involvedObject.name=RBG_NAME
   ```

   同时检查 Pod，以及对该业务真正重要的流量、readiness、缓存预热或其他健康信号。

8. 只要业务状态需要，就停止、暂停或调整 rollout。不要因为对象可写而自动继续下一个 role。

`RoleBasedGroupSet` 不是独立的逐子对象灰度机制。其控制器会比较生成的子对象与 `groupTemplate`，并把模板 role 写回子对象。因此，在 set 模板仍携带旧 workload 类型时迁移某个子对象并不稳定：控制器可能尝试恢复旧 role，而 admission guard 会拒绝这种新引入。

更新 set 模板可能把迁移传播到所有剩余子对象，所以这是 set 级 rollout 决策，而不是单 role 变更。如果确实需要逐子对象灰度，必须使用单独审查过的方案，协调或隔离子对象与 set reconcile 的关系。不要假设在父模板未更新时，某个子对象的迁移可以永久保持。

## 回滚

每个 role 迁移前，保留不可变对象副本、resourceVersion，以及部署流程使用的 revision 历史。这些记录仍可用于审计和恢复非 workload 字段，但不能让 workload 类型变更变得可逆。

一旦 role 已切换到 `RoleInstanceSet`，恢复保存的弃用 workload 类型就是一次新的弃用使用。即使 `controller.deprecatedWorkloadTypes.enabled=true`，v1alpha2 和 v1alpha1 validator 也会拒绝该写入；兼容模式只保留存量 role，不会授权在迁移后重新引入弃用类型。在业务负责人批准 RoleInstanceSet rollout 失败后的恢复方案之前，不要开始迁移。

如果旧工作负载已被删除，不要手工用同名重建。应让控制器管理替代工作负载。提交迁移后的恢复必须使用事先约定的应急方案，而不是简单恢复弃用 workload 类型。

## 3. 将对象重写为 v1alpha2 存储编码

只有所有已识别的弃用 workload 类型都迁移并确认 ready 后，才开始这条轨道。

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

1. 重新执行 role 盘点，处理所有剩余弃用 workload 类型。
2. 根据迁移清单确认所有对象均已重写，然后按上述说明更新并验证两个 `status.storedVersions` 值。
3. 只有在所有 role 迁移完成后，才设置 `controller.deprecatedWorkloadTypes.enabled=false`，并确认没有写入被拒绝。
4. 将清单、客户端和自动化迁移到 `v1alpha2`。
5. 只有在与业务负责人约定的回滚窗口结束后，才清理保存的迁移产物。

## 风险与失败模式

- **Pod 重建：** workload Kind 变化对选定 role 是破坏性变更，迁移期间该 role 的全部 Pod 可能重建。
- **部分 ready：** 每次只迁移一个 role，不要自动化全部 role。
- **业务状态：** 仅 ready 可能不足。数据库、缓存、会话状态、流量摘除和调度行为必须由业务负责人审查。
- **旧工作负载残留：** 在 `controller.deprecatedWorkloadTypes.enabled=true` 时，孤儿清理可以在 role 切换到 `RoleInstanceSet` 后删除旧工作负载。除非控制器无法清理，否则不要手工删除旧工作负载，因为手工删除可能与替代对象创建竞争。所有 role 迁移完成前保持兼容模式开启；过早设置为 `false` 会关闭旧类型 RBAC、watch 和孤儿清理，可能留下旧工作负载。
- **存储幽灵对象：** `status.storedVersions` 记录曾经使用过的存储版本。必须先确认对象重写完整，再从列表中移除 `v1alpha1`；如果旧编码对象仍在而移除 API 版本，这些对象可能无法读取。升级超过 `0.9.0` 前必须完成存储清单、status 更新和验证。
- **静默类型回落：** 未完成内容迁移就直接删除 workload-type 注解，会使 role 回落到 RoleInstanceSet。只有在 Pattern 和业务影响都经过审查后，才应执行该变更。

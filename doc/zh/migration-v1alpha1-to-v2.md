# 从 v1alpha1 迁移到 v1alpha2

本指南适用于 RBG `0.9.0`。该版本仍继续提供 `v1alpha1` API，存量对象可读写；但每次写入都会返回 API Warning，且新的弃用 workload 类型使用会被拒绝。`v1alpha1` 将在 `0.10.0` 停止服务。

## 正在弃用的内容

迁移包含两条独立轨道：

1. **API 编码（v1alpha1 到 v1alpha2）。**  
   通过 `v1alpha1` 创建且从未更新过的对象，在 etcd 中可能仍按 `v1alpha1` 编码存储。存储版本迁移会通过 `v1alpha2` 端点重写这些对象，内容保持不变。

2. **弃用 workload 类型（Deployment、StatefulSet、LeaderWorkerSet）。**  
   Role 可通过 `rbg.workloads.x-k8s.io/role-workload-type` 选择旧 workload Kind。替代方案是 `v1alpha2` 默认的 `RoleInstanceSet`。与存储迁移不同，这会改变下游工作负载的 `Kind`，并重建该 role 的 Pod。

不要合并两项判据：完成其中一条轨道并不代表另一条轨道也已完成。

## 前置条件

- 将 RBG 控制器升级到 `0.9.0` 或更新版本。
- 安装同一 release 中匹配的 `kubectl-rbg` CLI。
- 为每个仍使用 Deployment、StatefulSet 或 LeaderWorkerSet 的 role 规划维护窗口。
- 修改对象前保存回滚清单和 revision 历史。

`controller.deprecatedWorkloadTypes.enabled` 默认为 `false`。已经使用弃用 workload 类型的存量 role 会被豁免并继续 reconcile；新 role，以及原本未使用弃用类型的 role，会被拒绝。RBAC、watch 和孤儿清理在开关为 `false` 时仍保持生效，以便迁移能够删除旧工作负载。仅在需要临时回滚窗口时才使用 `true`。

## 1. 查看影响面

`plan` 子命令是只读的。它会扫描所有 RoleBasedGroup 和 RoleBasedGroupSet，并对每个 role 注解分类：

```bash
kubectl rbg migrate plan
```

分类含义：

- `safe-cleanup`：注解缺失，或值为 `workloads.x-k8s.io/v1alpha2/RoleInstanceSet`。删除该注解不会改变行为。
- `workload-type`：注解选择 Deployment、StatefulSet、LeaderWorkerSet 或未知类型。迁移该 role 会用 RoleInstanceSet 替换旧工作负载，并重建其 Pod。

该报告是影响面清单，不是迁移决策。它不会替用户选择停机窗口，也不会断言某个 role 可以安全迁移。

## 2. 逐 role 迁移 workload 类型

对报告中标记为 `workload-type` 的每个 RoleBasedGroup role，先确认将重建哪些 Pod：

```bash
kubectl rbg migrate workload-type \
  --rbg RBG_NAME \
  --role ROLE_NAME
```

两个参数均为必填。命令会列出受影响 Pod，并在修改对象前要求确认。非交互终端中，只有在已审查 Pod 列表后才使用 `--yes`。

命令会从指定 role 删除 `rbg.workloads.x-k8s.io/role-workload-type`，并落地对应 Pattern：

- Deployment 与 StatefulSet 使用 `standalonePattern`。
- LeaderWorkerSet 使用 `leaderWorkerPattern`。
- 冗余的 `RoleInstanceSet` 注解会被删除，不改变既有 Pattern。

确认后，控制器会把旧 Deployment、StatefulSet 或 LeaderWorkerSet 判定为孤儿并删除，创建 RoleInstanceSet，并重建该 role 的全部 Pod。

按以下方式灰度执行：

1. 选择一个 role。
2. 对该 role 执行迁移命令。
3. 等待 RoleBasedGroup 报告该 role ready：

   ```bash
   kubectl get rolebasedgroup RBG_NAME -o wide
   kubectl get pods -l rbg.workloads.x-k8s.io/group-name=RBG_NAME,rbg.workloads.x-k8s.io/role-name=ROLE_NAME
   ```

4. 查看该 role 的控制器事件：

   ```bash
   kubectl describe rolebasedgroup RBG_NAME
   kubectl get events --field-selector involvedObject.name=RBG_NAME
   ```

5. 只有上一个 role ready 后才继续下一个 role。

如果迁移涉及 RoleBasedGroupSet，`migrate plan` 会报告其模板 role。请先检查并按同样的逐 role 语义更新该模板，再从它创建新的 RoleBasedGroup；已生成的存量 RoleBasedGroup 仍需逐个迁移。

## 回滚

每个 role 迁移前保存对象：

```bash
kubectl get rolebasedgroup RBG_NAME -o yaml > RBG_NAME.pre-migration.yaml
```

在 `v1alpha1` 仍被服务期间，可通过创建该对象时使用的 API 版本恢复保存的清单来回滚单个 role。回滚会再次重建 Pod，并使该 role 回到弃用 workload 类型。恢复带有原始 `role-workload-type` 注解的 `v1alpha2` 对象只是临时逃生通道，不是已完成迁移。

如果旧工作负载已被删除，不要手工用同名重建它。应让控制器管理替代工作负载；只有在停止流量后，才使用保存的清单恢复 role 配置。

## 3. 将对象重写为 v1alpha2 存储编码

所有报告出的 workload-type role 都迁移并确认 ready 后，执行：

```bash
kubectl rbg migrate storage-version
```

该命令会列出并更新所有 RoleBasedGroup 和 RoleBasedGroupSet，全程通过 `v1alpha2` 端点执行。更新不改变内容，可重复执行。由于存储版本切换不会重写从未更新过的对象，这一步是必要的。

检查两个启用转换的 CRD：

```bash
kubectl get customresourcedefinition \
  rolebasedgroups.workloads.x-k8s.io \
  rolebasedgroupsets.workloads.x-k8s.io \
  -o custom-columns=CRD:.metadata.name,STORED_VERSIONS:.status.storedVersions
```

每个值都必须只包含 `v1alpha2`。如果仍有 `v1alpha1`，重新执行 `migrate storage-version`，并在继续前检查控制器与 conversion webhook 健康状态。

## 4. 结束迁移窗口

1. 重新执行 `kubectl rbg migrate plan`，处理所有剩余 `workload-type` 项。
2. 确认两个 `status.storedVersions` 值均只包含 `v1alpha2`。
3. 保持 `controller.deprecatedWorkloadTypes.enabled=false`。
4. 停止编写 `v1alpha1` 清单，将客户端和示例迁移到 `v1alpha2`。
5. 在所有集群通过两项检查前，保留迁移指南和 CLI。

## 风险与失败模式

- **Pod 重建：** workload Kind 变化对选定 role 是破坏性变更，迁移期间该 role 的全部 Pod 都会重建。
- **部分 ready：** 每次只迁移一个 role，确认 ready 后再继续；不要把所有 role 放进同一个脚本一次性执行。
- **旧工作负载残留：** RBG `0.9.0` 在弃用类型关闭时仍保留孤儿清理。除非控制器无法清理，否则不要手工删除旧工作负载；手工删除可能与替代对象创建发生竞争。
- **存储幽灵对象：** 如果启用转换的 CRD 的 `status.storedVersions` 仍包含 `v1alpha1`，后续版本移除该 API 时对象可能无法读取。升级超过 `0.9.0` 前必须执行存储检查。
- **静默类型回落：** 未完成内容迁移就直接删除 workload-type 注解，会使 role 回落到 RoleInstanceSet。只有通过迁移命令并在确认后执行才是预期行为。

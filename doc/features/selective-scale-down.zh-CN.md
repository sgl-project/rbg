# 有状态指定缩容

[English](selective-scale-down.md) | 简体中文

在 RBG 角色上配置 `rbg.workloads.x-k8s.io/excluded-ordinals`，表示“缩容时优先移除这些实例，扩容时跳过这些序号”。无需配置策略或控制器开关。

Alpha 仅支持不受 RBGSet 管理的 RBG，以及原生 Stateful RIS 后端。无状态角色、其他后端、RBGSet 模板和 RBGSet 管理的子 RBG 不支持该注解。

## 使用方法

使用[完整示例](../../examples/basic/rbg/scaling/selective-scale-down.yaml)创建三个实例 `[0,1,2]`。将角色配置改为：

```yaml
name: worker
replicas: 2
annotations:
  rbg.workloads.x-k8s.io/excluded-ordinals: "1"
```

控制器提交的目标为 `[0,2]`。副本数和排除配置可以一起修改，也可以先设置排除配置、再缩容。只修改排除配置不会创建、删除或替换实例。

从同一次子 RIS 查询中检查以下内容，确认缩容目标已提交：

```yaml
metadata:
  annotations:
    rbg.workloads.x-k8s.io/committed-skip-ordinals: "1"
spec:
  replicas: 2
```

提交后可扩容到三个副本，保留排除配置，目标变为 `[0,2,3]`。0、2 的实例身份保持不变。若在提交前快速缩容再扩容，控制器可能只观察到最后一个副本数，无法保证选中的实例已被移除。

“目标已提交”不表示旧实例已经退出。替换流程会暂时减少期望容量，新实例可用前实际服务容量可能仍然不足。如果新实例需要旧实例释放 GPU、内存等资源，还需等待旧实例退役。摘流、数据迁移和 PVC 清理仍由现有运维流程完成。

leader/worker 场景中，一个序号代表整个组，选中后退役该组的 leader 和全部 worker，不改变组大小。

## 选择规则

- 支持单个序号和闭区间，例如 `"1,3,8-10"`。重复、重叠条目会归一化，书写顺序不是优先级。
- 缩容只删除所需数量：先选排除配置中的成员，同一优先级按序号从大到小选择；不够时再选其他高序号成员。
- 扩容保留已有成员，从小到大选择未占用且未被排除的序号。首次创建使用同样规则。
- 副本数不变时，设置、修改、清空排除配置均不改变已提交成员。
- 成员故障或暂时缺失，仍按已提交的原序号恢复。
- 非法语法、负数、倒置区间、溢出及不支持的配置位置会在准入时返回具体字段和原因；绕过准入的非法配置会产生 Warning 事件和失败的 Ready 条件，只阻止该角色的新写入，不会退回默认顺序缩容；其他角色及已移除工作负载的清理继续执行。discovery 保留该角色的已提交目标，尚无子对象的非法角色不发布推测成员。

例如，原来有五个成员 `[0,1,2,3,4]`，一起修改为三个副本、排除配置 `"1,3"`，最终保留 `[0,2,4]`。排除条目数量无需等于缩容数量，也可以包含没有对应实例的序号。

## 与升级的关系

沿用现有升级策略和自动扩缩容的副本数管理方式。partition 按排序后的成员位置计算：成员 `[0,2,4]`、partition 为 2 时，0、2 保留旧版本，4 允许升级。仅修改排除配置不移动灰度范围、不改变模板版本、不触发滚动升级。模板回滚保留当前排除配置和已提交成员。

新分配的 rollout surge 序号也避开当前 E，包括最大已提交成员之后的序号。例如成员为 `[0,2,4]`、E 为 `"1,3,5"`、允许一个 surge 时，新 surge 使用 6。条件写入方将 E 单独同步到子 RIS 元数据，仅供这项分配使用；它不是成员结果，不进入版本快照或 RI、Pod 模板。修改或移除 E 后，已创建的 surge 按身份保留，仍受数量预算和正常完成条件约束。

discovery 有意在成员目标提交后立即更新 ConfigMap：选中的旧成员可能尚未退出，就已从地址列表移除。这是提前摘流信号，不保证消费者已经停止流量，也不表示实例已完成退役。

## 关闭与降级

删除角色上的排除注解，即可关闭后续的指定缩容选择能力，现有成员不变。例如 `[0,2,3]` 清空排除配置后仍为 `[0,2,3]`，之后扩容到四个副本才可补入序号 1。

子 RIS 的 `committed-skip-ordinals` 是控制器计算并保存的实际跳过序号，与用户的排除参考不同，不应直接编辑。修改或删除该注解会立即改变期望成员，控制器不会自动恢复原记录；本期不新增按请求者身份进行的准入保护。

旧控制器不能接管非连续成员。降级前应删除角色排除配置，扩容填满缺口，确认所有子 RIS 的内部记录为空且调谐完成。开始使用功能前，所有运行中的控制器副本都必须支持该功能。

## 损坏提交记录的人工恢复

`committed-skip-ordinals` 无法解析时，成员调谐停止并报告
`InvalidCommittedOrdinals`。只删除角色 E 不会修复损坏记录。控制器不会自动按连续成员处理，因为清空记录可能改变实例身份并删除高序号成员。

人工修复是禁止直接编辑子 RIS 规则的异常恢复路径。先停止该角色的并发扩缩容和模板编辑，保存 RBG、RIS、RI 状态，根据可信的原提交目标确认成员集合，并核对 Pod、PVC 归属。不能仅根据存活 RI 猜测目标：其中可能包含 surge、退役成员，也可能缺少应恢复的成员。

下面假设角色 0 为 `worker`，已核实目标为三个成员 `[0,2,4]`，且损坏注解仍存在。每条命令都应使用正确的 namespace。若角色 E 已不存在，跳过删除 E 的 patch：

```sh
kubectl get rbg selective-scaling -o yaml > rbg-before-repair.yaml
kubectl patch rbg selective-scaling --type=json -p='[
  {"op":"test","path":"/spec/roles/0/name","value":"worker"},
  {"op":"remove","path":"/spec/roles/0/annotations/rbg.workloads.x-k8s.io~1excluded-ordinals"}
]'
kubectl get ris selective-scaling-worker -o json > ris-before-repair.json
kubectl get ri -l rbg.workloads.x-k8s.io/group-name=selective-scaling -o yaml > instances-before-repair.yaml
```

通过 UID、resourceVersion 和副本数前置条件恢复已核实的记录：

```sh
jq --arg record '1,3' --argjson replicas 3 '[
  {op:"test",path:"/metadata/uid",value:.metadata.uid},
  {op:"test",path:"/metadata/resourceVersion",value:.metadata.resourceVersion},
  {op:"test",path:"/spec/replicas",value:$replicas},
  (if $record == "" then
    {op:"remove",path:"/metadata/annotations/rbg.workloads.x-k8s.io~1committed-skip-ordinals"}
   else
    {op:"replace",path:"/metadata/annotations/rbg.workloads.x-k8s.io~1committed-skip-ordinals",value:$record}
   end)
]' ris-before-repair.json > membership-repair.json
kubectl patch ris selective-scaling-worker --type=json --patch-file=membership-repair.json
```

只有确认期望目标就是连续序号 `[0,1,...,replicas-1]`，并已理解、批准高序号实例的退役后，才将参数改为 `--arg record ''`。这会在角色 E 已移除的前提下删除损坏注解，让普通调谐恢复。删除注解不能作为稀疏记录的通用修复方式。

前置条件失败时应重新读取对象、核实目标，不能取消保护或重放旧 patch。修复后控制器立即恢复执行，应检查提交成员、存活实例身份和 discovery 地址，并确认 `InvalidCommittedOrdinals` 条件清除；其他 FailedScale 历史会保留。

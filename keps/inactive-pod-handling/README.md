# KEP: Inactive Pod 处理增强

<!-- toc -->
- [背景与动机](#背景与动机)
- [目标](#目标)
- [非目标](#非目标)
- [方案设计](#方案设计)
    - [用户场景](#用户场景)
    - [风险与缓解措施](#风险与缓解措施)
- [详细设计](#详细设计)
    - [新增检测函数](#新增检测函数)
    - [Pod Controller 改造](#pod-controller-改造)
    - [RoleInstance Controller 改造](#roleinstance-controller-改造)
    - [RestartPolicy 处理矩阵](#restartpolicy-处理矩阵)
    - [测试计划](#测试计划)
<!-- /toc -->

## 背景与动机

RBG 项目在处理 Pod 异常状态（如 Evicted、UnexpectedAdmissionError、Failed 等）时存在缺陷。

**问题根源**：原生 Kubernetes 控制器会将 Failed/Succeeded 状态的 Pod 视为 inactive，立即创建新 Pod 补足副本数。但 RBG 当前的实现需要等待 Pod 被删除或容器重启才会触发重建，对于直接进入 Failed 状态的情况缺乏处理。

**具体问题点**：

1. **Failed/Evicted Pod 不触发 RBG 重建**
   - `ContainerRestarted` 函数只检查 Running/Pending 状态，对 Failed Pod 返回 false
   - `PodDeleted` 函数只检查 DeletionTimestamp，不检查 Phase
   - Pod Controller `podToRBG` 的触发条件无法覆盖 Evicted/Failed Pod

2. **RoleInstance Controller 补足依赖 GC**
   - `GetActiveAndInactivePods` 正确分类 Pod，但 inactive Pod 需要等 GC 删除后才创建新 Pod
   - 在 GC 之前，Failed Pod 导致 RoleInstance 的 Ready 条件变为 False

3. **无 DisruptionTarget 处理**
   - Kubernetes 1.22+ 引入的 `DisruptionTarget` condition 未被检测
   - 无法在 Pod 被驱逐前提前做出反应

4. **调度失败无重试**
   - `PodScheduled` condition 为 False（Unschedulable）时 Pod 无限期 Pending
   - 没有触发重建或重试的机制

**举例说明**：假设一个 Pod 因节点资源不足被 Evicted，其 Phase 变为 Failed。当前 RBG 控制器不会检测到这个状态变化，Pod 会一直停留在 Failed 状态直到被 GC 清理（默认阈值可能很大）。在此期间，服务副本数不足。

## 目标

- 正确检测并处理 Failed/Evicted/UnexpectedAdmissionError 等异常 Pod 状态
- 在 Pod 进入 inactive 状态时立即触发重建逻辑，无需等待 GC 删除
- 遵循 Kubernetes 原生控制器模式，不主动删除 Failed Pod，而是创建替代 Pod
- **保持向后兼容** —— 现有 RestartPolicy 行为保持不变，只是扩展触发条件
- 支持 DisruptionTarget condition 检测（K8s 1.22+）

## 非目标

- 不引入新的 RestartPolicy 类型
- 不改变现有的 RBG 重建流程
- 不主动清理 Failed Pod（遵循 K8s 原生模式，让 GC 或用户处理）
- 不处理 Pod Succeeded 状态（正常完成任务）

## 方案设计

### 用户场景

#### 场景一：Evicted Pod 自动重建

> 作为运维人员，当 Pod 因节点资源不足被驱逐时，我希望 RBG 能够立即检测到并创建替代 Pod，而不是等待 GC 清理。

**期望行为**：
- Pod 被驱逐后 Phase 变为 Failed，status.reason="Evicted"
- RBG 控制器检测到 Pod 进入 inactive 状态
- 根据 RestartPolicy 触发相应重建逻辑
- 发出 Event 通知用户 Pod 状态变化

#### 场景二：调度失败 Pod 重建

> 作为运维人员，当 Pod 因资源不足无法调度（Unschedulable）时，我希望能够触发重建尝试调度到其他节点。

**期望行为**：
- Pod Scheduled condition 为 False，reason="Unschedulable"
- RBG 控制器检测到调度失败
- 根据 RestartPolicy 可能触发 Instance/RBG 重建
- 新 Pod 有机会调度到其他节点

#### 场景三：RecreateRBGOnPodRestart 触发

> 作为用户，当配置 RestartPolicy=RecreateRBGOnPodRestart 时，我希望任何 Pod 进入 Failed 状态都能触发整个 RBG 重建。

**期望行为**：
- 任一 Pod 因 Eviction/Failed 进入 inactive 状态
- 整个 RBG 按依赖顺序重建所有 Role
- 确保服务整体一致性

### 风险与缓解措施

| 风险 | 缓解措施 |
|------|----------|
| 扩展触发条件可能导致意外的重建循环 | 使用 `wasInstanceReady` 检查避免初始创建期间触发；使用 `RestartInProgress` condition 防止重复触发 |
| Failed Pod 状态转换频繁可能增加 controller 负载 | predicate 只捕获 active→inactive 的状态转换，避免每次状态更新都触发 |
| DisruptionTarget condition 依赖 K8s 1.22+ | 同时支持传统检测方式（status.reason），确保向后兼容 |

## 详细设计

### 新增检测函数

**原则**：优先复用原生 Kubernetes 函数，避免重复定义。

原生 K8s 已有的函数（可直接使用）：

| 原生函数 | 定义位置 | 作用 |
|---------|---------|------|
| `kubecontroller.IsPodActive(pod)` | `vendor/k8s.io/kubernetes/pkg/controller/controller_utils.go` | Phase != Succeeded/Failed && DeletionTimestamp == nil |
| `podutil.IsPodTerminal(pod)` | `vendor/k8s.io/kubernetes/pkg/api/v1/pod/util.go` | Phase == Failed || Phase == Succeeded |
| `kubecontroller.IsPodTerminating(pod)` | `vendor/k8s.io/kubernetes/pkg/controller/controller_utils.go` | !IsPodTerminal && DeletionTimestamp != nil |

**注意**：项目已在 `pkg/reconciler/roleinstance/utils/instance_utils.go` 中使用 `kubecontroller.IsPodActive`。

在 `pkg/utils/pod_utils.go` 中新增以下**辅助函数**（基于原生函数和常量）：

#### PodBecameInactive（状态转换检测）

```go
import (
    kubecontroller "k8s.io/kubernetes/pkg/controller"
    podutil "k8s.io/kubernetes/pkg/api/v1/pod"
)

// PodBecameInactive 检测 Pod 是否从活跃状态变为非活跃状态
// 用于 predicate 中捕获状态转换事件
func PodBecameInactive(oldPod, newPod *corev1.Pod) bool {
    if oldPod == nil || newPod == nil {
        return false
    }
    wasActive := kubecontroller.IsPodActive(oldPod)
    nowInactive := !kubecontroller.IsPodActive(newPod)
    return wasActive && nowInactive
}
```

#### IsPodEvicted

```go
// IsPodEvicted 判断 Pod 是否因资源不足被驱逐
// Evicted Pod 的 Phase=Failed，status.reason="Evicted"
// 同时支持 DisruptionTarget condition (K8s 1.22+)
func IsPodEvicted(pod *corev1.Pod) bool {
    if pod == nil || !podutil.IsPodTerminal(pod) {
        return false
    }
    if pod.Status.Phase != corev1.PodFailed {
        return false
    }
    // 检查 DisruptionTarget condition (K8s 1.22+)
    // 原生定义的 reason: PreemptionByScheduler, TerminationByKubelet
    for _, cond := range pod.Status.Conditions {
        if cond.Type == corev1.DisruptionTarget {
            return true
        }
    }
    // 传统检测方式：status.reason = "Evicted"
    return pod.Status.Reason == "Evicted"
}
```

#### IsPodUnexpectedAdmissionError

```go
// IsPodUnexpectedAdmissionError 判断 admission 异常
func IsPodUnexpectedAdmissionError(pod *corev1.Pod) bool {
    if pod == nil || pod.Status.Phase != corev1.PodFailed {
        return false
    }
    return pod.Status.Reason == "UnexpectedAdmissionError"
}
```

#### IsPodFailedSchedule

```go
// IsPodFailedSchedule 判断调度失败 (PodScheduled=False)
// 使用原生定义的常量: PodReasonUnschedulable, PodReasonSchedulerError
func IsPodFailedSchedule(pod *corev1.Pod) bool {
    if pod == nil {
        return false
    }
    for _, cond := range pod.Status.Conditions {
        if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
            return cond.Reason == corev1.PodReasonUnschedulable ||
                cond.Reason == corev1.PodReasonSchedulerError
        }
    }
    return false
}
```

#### GetPodInactiveReason

```go
// GetPodInactiveReason 返回 Pod inactive 的具体原因
// 基于 podutil.IsPodTerminal 和原生常量
func GetPodInactiveReason(pod *corev1.Pod) string {
    if pod == nil {
        return "PodNotFound"
    }
    if pod.DeletionTimestamp != nil {
        if podutil.IsPodTerminal(pod) {
            return "PodTerminatingTerminal"
        }
        return "PodTerminating"
    }
    if pod.Status.Phase == corev1.PodSucceeded {
        return "PodSucceeded"
    }
    if pod.Status.Phase == corev1.PodFailed {
        if IsPodEvicted(pod) {
            return "PodEvicted"
        }
        if IsPodUnexpectedAdmissionError(pod) {
            return "UnexpectedAdmissionError"
        }
        return "PodFailed"
    }
    if IsPodFailedSchedule(pod) {
        return "PodUnschedulable"
    }
    return "Unknown"
}
```

### Pod Controller 改造

**文件**: `internal/controller/workloads/pod_controller.go`

#### 修改 podToRBG 触发条件

使用原生 `kubecontroller.IsPodActive` 判断 Pod 状态：

```go
import (
    kubecontroller "k8s.io/kubernetes/pkg/controller"
)

func (r *PodReconciler) podToRBG(ctx context.Context, obj client.Object) []reconcile.Request {
    pod, ok := obj.(*corev1.Pod)
    if !ok {
        return []reconcile.Request{}
    }

    rbgName := pod.Labels[constants.GroupNameLabelKey]
    if rbgName == "" {
        return []reconcile.Request{}
    }

    // 原有触发条件
    containerRestarted := utils.ContainerRestarted(pod)
    podDeleted := utils.PodDeleted(pod)

    // 新增触发条件：Pod 变为非活跃状态（复用原生 IsPodActive）
    // Pod inactive = !IsPodActive，即 Phase=Failed/Succeeded 或正在删除
    // 但排除正在删除的情况（已有 podDeleted 处理）
    podBecameInactive := !kubecontroller.IsPodActive(pod) && pod.DeletionTimestamp == nil

    // 只有满足任一条件才触发
    if !containerRestarted && !podDeleted && !podBecameInactive {
        return []reconcile.Request{}
    }

    // ... 后续 RBG 获取和 RestartPolicy 检查逻辑保持不变 ...
}
```

#### 修改 predicate 捕获状态转换

使用新增的 `PodBecameInactive` 辅助函数（基于原生 `IsPodActive`）：

```go
UpdateFunc: func(e event.UpdateEvent) bool {
    oldPod, ok1 := e.ObjectOld.(*corev1.Pod)
    newPod, ok2 := e.ObjectNew.(*corev1.Pod)
    if ok1 && ok2 {
        _, oldExist := oldPod.Labels[constants.GroupNameLabelKey]
        _, newExist := newPod.Labels[constants.GroupNameLabelKey]

        if !oldExist || !newExist {
            return false
        }

        // 检测 Pod 从 active 变为 inactive 的状态转换
        // 只有状态转换时才触发，避免每次状态更新都触发
        return utils.PodBecameInactive(oldPod, newPod)
    }
    return false
},
```

### RoleInstance Controller 改造

**文件**: `pkg/reconciler/roleinstance/sync/instance_scale.go`

#### 修改 shouldRecreateInstance

使用原生 `kubecontroller.IsPodActive` 替代仅检查 `DeletionTimestamp`：

```go
import (
    kubecontroller "k8s.io/kubernetes/pkg/controller"
)

func shouldRecreateInstance(instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod) bool {
    if instance.Spec.RestartPolicy != workloadsv1alpha2.RoleInstanceRestartPolicyRecreateOnPodRestart {
        return false
    }

    if len(pods) == 0 {
        return false
    }

    // 检查容器重启
    for _, pod := range pods {
        if utils.ContainerRestarted(pod) {
            return true
        }
    }

    // 检查非活跃 Pod（Failed/Evicted 等）
    // 使用原生 IsPodActive，与 K8s 控制器行为一致
    if wasInstanceReady(instance) && instance.Generation == instance.Status.ObservedGeneration {
        expectedPodCount := getExpectedPodCount(instance)
        activeCount := 0
        for _, p := range pods {
            // 使用原生 IsPodActive 替代仅检查 DeletionTimestamp
            // 这样可以正确检测 Failed/Evicted Pod（Phase=Failed 也视为 inactive）
            if kubecontroller.IsPodActive(p) {
                activeCount++
            }
        }
        // 有 inactive Pod 导致 active 数量不足
        if activeCount < expectedPodCount {
            return true
        }
    }

    return false
}
```

#### 修改 calculateDiffsWithExpectation

项目已在 `instance_utils.go` 中有 `GetActiveAndInactivePods` 函数，使用原生 `IsPodActive` 分类。只需确保调用时正确处理：

```go
func (c *realControl) calculateDiffsWithExpectation(...) (*expectationDiff, error) {
    // ... shouldRecreateInstance 检查 ...

    // 使用已有的 GetActiveAndInactivePods 分离 Pod
    // 该函数使用 kubecontroller.IsPodActive 进行分类
    // inactive Pod 不计入 ExistIDs，自动触发创建新 Pod

    // 当前代码已使用 pods 参数计算拓扑
    // 内部 GetComponentsTopology 会通过 GroupPodsByComponentName 处理
    // 需确认只有 activePods 参与拓扑计算

    prt, err := coreControl.GetComponentsTopology(pods)
    // ...
}
```

**注意**：项目 `pkg/reconciler/roleinstance/utils/instance_utils.go` 中已有：

```go
func GetActiveAndInactivePods(ctx context.Context, reader client.Reader, opts *client.ListOptions) ([]*v1.Pod, []*v1.Pod, error) {
    // ...
    for i := range podList.Items {
        pod := &podList.Items[i]
        if kubecontroller.IsPodActive(pod) {
            activePods = append(activePods, pod)
        } else {
            inactivePods = append(inactivePods, pod)
        }
    }
    return activePods, inactivePods, nil
}
```

此函数已正确使用原生 `IsPodActive`，无需修改。

### RestartPolicy 处理矩阵

| RestartPolicy | Inactive Pod 行为 |
|--------------|-----------------|
| `None` | 创建替代 Pod 维护副本数，不重建 Instance/RBG。这是 RoleInstanceSet 的默认行为。 |
| `RecreateRoleInstanceOnPodRestart` | 整个 RoleInstance 重建（所有 Pod 删除后重建）。当任一 Pod 进入 inactive 状态触发。 |
| `RecreateRBGOnPodRestart` | 整个 RBG 重建（所有 Role 按依赖顺序重建）。当 Pod 进入 inactive 状态且角色配置此策略时触发。 |

### 测试计划

[X] 我/我们理解相关组件的所有者可能需要在实现此增强功能之前更新现有测试。

#### 单元测试

| 测试名称 | 测试内容 |
|---------|---------|
| `TestPodBecameInactive` | 验证状态转换检测：Running→Failed、Pending→Failed、Running→Succeeded |
| `TestIsPodEvicted` | 验证 Evicted Pod 检测：status.reason="Evicted"、DisruptionTarget condition |
| `TestIsPodUnexpectedAdmissionError` | 验证 admission 异常检测 |
| `TestIsPodFailedSchedule` | 验证调度失败检测：PodReasonUnschedulable、PodReasonSchedulerError |
| `TestGetPodInactiveReason` | 验证各状态返回正确的 reason 字符串 |
| `TestPodToRBGWithInactivePod` | 验证 inactive Pod 触发 RBG reconcile（使用原生 IsPodActive） |
| `TestShouldRecreateInstanceWithInactivePod` | 验证 inactive Pod 触发 Instance 重建 |
| `TestPredicateStateTransition` | 验证 predicate 只捕获 active→inactive 转换，不重复触发 |

**测试注意点**：
- 确保使用原生 `kubecontroller.IsPodActive` 和 `podutil.IsPodTerminal` 进行判断
- 测试原生常量：`corev1.PodReasonUnschedulable`、`corev1.DisruptionTarget` 等
- 测试与原生 K8s ReplicaSet/StatefulSet 控制器行为的一致性

#### E2E 测试

**测试文件位置**: `test/e2e/testcase/v1alpha2/inactive_pod.go`

**测试环境**: Kind cluster (已有 CI 流程)

**模拟 Pod 异常状态的方法**:

在真实 K8s 集群中无法轻易触发真实的 Pod Eviction，因此采用**手动修改 Pod status subresource** 的方式模拟：

```go
// 模拟 Evicted Pod
func setPodEvicted(ctx context.Context, client client.Client, pod *corev1.Pod) error {
    pod.Status.Phase = corev1.PodFailed
    pod.Status.Reason = "Evicted"
    pod.Status.Message = "The node was low on resource: ephemeral-storage. Evicted."
    return client.Status().Update(ctx, pod)
}
```

**测试 Case 设计**:

##### Case 1: Evicted Pod 触发 RBG 重建 (RecreateRBGOnPodRestart)

- 创建 RBG with `RestartPolicy=RecreateRBGOnPodRestart`
- 等待 Pod Ready
- 手动将 Pod status 改为 `Phase=Failed, Reason="Evicted"`
- 验证 RBG 触发重建（RestartInProgress condition → True → False）
- 验证新 Pod 创建完成

##### Case 2: Failed Pod 触发 RoleInstance 重建 (RecreateRoleInstanceOnPodRestart)

- 创建 RBG with `RestartPolicy=RecreateRoleInstanceOnPodRestart` (LeaderWorkerSet)
- 等待 Pod Ready
- 手动将 Pod status 改为 `Phase=Failed, Reason="Error"`
- 验证 RoleInstance 重建（LWS 控制器重建 Group）

##### Case 3: RestartPolicy=None 时创建替代 Pod

- 创建 RBG with `RestartPolicy=None` (默认，Replicas=3)
- 等待 Pod Ready，记录初始 Pod UIDs
- 手动将一个 Pod 改为 Evicted 状态
- 验证活跃 Pod 数量恢复到 3（创建替代 Pod）
- 验证没有 RestartInProgress condition（不重建 RBG）

##### Case 4: predicate 只捕获状态转换（不重复触发）

- 创建 RBG，触发一次 Evicted → 重建
- 对新 Pod 再次更新 status（不改变 Phase）
- 验证没有再次触发重建

#### 测试辅助函数

在 `test/utils/utils.go` 中添加：

```go
// SetPodEvicted 模拟 Pod Evicted 状态
func SetPodEvicted(ctx context.Context, rclient client.Client, pod *corev1.Pod) error

// SetPodUnexpectedAdmissionError 模拟 Admission Error
func SetPodUnexpectedAdmissionError(ctx context.Context, rclient client.Client, pod *corev1.Pod) error

// SetPodFailed 模拟 Pod Failed 状态
func SetPodFailed(ctx context.Context, rclient client.Client, pod *corev1.Pod) error

// GetActivePodCount 获取活跃 Pod 数量（使用原生 IsPodActive）
func GetActivePodCount(ctx context.Context, rclient client.Client, namespace, rbgName string) (int, error)
```

---

## 关键文件清单

| 文件 | 改动类型 |
|-----|---------|
| `pkg/utils/pod_utils.go` | 新增函数 |
| `pkg/utils/pod_utils_test.go` | 新增测试 |
| `internal/controller/workloads/pod_controller.go` | 修改函数 |
| `pkg/reconciler/roleinstance/sync/instance_scale.go` | 修改函数 |
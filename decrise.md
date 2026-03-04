# RBG + Patio 缩容时 Worker 无法自动注销问题分析与排查

## 问题概述

### 现象描述

在使用 Patio + RBG 部署 PD 分离的推理实例时遇到以下问题:

- ✅ **扩容(Scale UP)**: 使用 `kubectl edit rbg` 增加 P/D 实例时,Patio 可以正确在 Router 中注册新 worker
- ❌ **缩容(Scale DOWN)**: 减少 P/D 实例时,Router 不能自动删除已减少的 worker

### 关键发现

通过手动 API 测试发现:

```bash
# ✅ 添加 worker - 正常工作
curl -X POST http://127.0.0.1:30080/workers \
  -H "Content-Type: application/json" \
  -d '{
    "url": "http://sglang-rbg-1p1d-qw14-prefill-1.s-sglang-rbg-1p1d-qw14-prefill.default.svc.cluster.local:8000",
    "worker_type": "prefill"
  }'

# ❌ 删除 worker - 不生效
curl -X DELETE http://127.0.0.1:30080/workers/http://sglang-rbg-1p1d-qw14-prefill-1.s-sglang-rbg-1p1d-qw14-prefill.default.svc.cluster.local:8000
```

**结论**: 问题不仅在 RBG/Patio 的 Pod 生命周期管理,**Router 的 DELETE /workers/{endpoint} API 本身可能存在问题**。

---

## 根因分析

### 问题 1: RBG 缩容时缺少优雅关闭机制

#### 代码位置
`pkg/reconciler/instanceset/sync/scale.go:244-271`

#### 扩容流程(正常工作) ✅

```go
// 创建新实例
func (rc *realControl) createInstances(...) {
    // 创建新的 Instance/Pod
    rc.Create(context.TODO(), instance)
    // Pod 启动后,Patio sidecar 自动执行注册
}
```

**Patio 启动流程** (`python/patio/app.py:73-108`):
```python
def run_topo_client(worker_instance_info: str):
    # 1. 解析 worker 信息
    worker_dict = json.loads(worker_instance_info)

    # 2. 创建 topology client
    topo_client = create_topo_client(worker_dict["topo_type"], worker_info)

    # 3. 等待推理引擎就绪
    topo_client.wait_engine_ready(worker_info)

    # 4. 自动注册到 Router ✅
    topo_client.register("", worker_info)

    # 5. 设置信号处理器(用于优雅关闭)
    signal.signal(signal.SIGTERM, stop_topo_client_signal_handler)
    signal.signal(signal.SIGINT, stop_topo_client_signal_handler)
```

#### 缩容流程(存在问题) ❌

```go
func (rc *realControl) deleteInstances(gs *appsv1alpha1.InstanceSet,
                                       instancesToDelete []*appsv1alpha1.Instance) {
    for _, instance := range instancesToDelete {
        // 检查是否配置了 PreDelete lifecycle hook
        if gs.Spec.Lifecycle != nil &&
           lifecycle.IsInstanceHooked(gs.Spec.Lifecycle.PreDelete, instance) {
            // 标记为 PreparingDelete 状态
            rc.lifecycleControl.UpdateInstanceLifecycle(instance,
                                                        LifecycleStatePreparingDelete,
                                                        markNotReady)
        } else {
            // ❌ 直接删除,没有通知 Patio sidecar
            rc.Delete(context.TODO(), instance)
        }
    }
}
```

**问题**:
1. 当前配置中**没有设置 lifecycle.preDelete hook**
2. RBG 直接删除 Pod,导致 Patio sidecar 没有收到 SIGTERM 信号(或收到但来不及处理)
3. `stop_topo_client_signal_handler` 中的 `topo_client.unregister()` 未被调用
4. Router 中的 worker 信息未被清理

#### Patio 的注销逻辑

**信号处理器** (`python/patio/app.py:67-71`):
```python
def stop_topo_client_signal_handler(signal, frame):
    global topo_client
    if topo_client is not None:
        topo_client.unregister()  # 调用 DELETE /workers/{endpoint}
    sys.exit(0)
```

**注销实现** (`python/patio/topo/client/sgl_topo_client.py:133-151`):
```python
def unregister(self):
    def f():
        url = f"http://{self.worker_endpoint}"
        worker_registration_url = f"http://{self.sgl_router_endpoint}/workers/{translate_http_response_parameters(url)}"
        resp = requests.delete(worker_registration_url)
        if resp.status_code == 202:
            logger.info(f"unregistered worker successfully.")
        else:
            raise Exception(
                f"unregister failed, url: {worker_registration_url}, status_code: {resp.status_code}, content: {resp.text}")

    try:
        utils.retry(f, retry_times=60, interval=3)
        return True
    except Exception as e:
        logger.error(f"failed to unregister worker: {e}")
        traceback.print_exc()
        return False
```

### 问题 2: Router DELETE API 的 URL 编码问题

#### URL 编码函数

**代码位置**: `python/patio/topo/client/sgl_topo_client.py:62-63`

```python
def translate_http_response_parameters(raw: str) -> str:
    return raw.replace("/", "%2F")  # 只替换 /,不是完整的 URL 编码!
```

#### 问题分析

**原始 URL**:
```
http://sglang-rbg-1p1d-qw14-prefill-1.s-sglang-rbg-1p1d-qw14-prefill.default.svc.cluster.local:8000
```

**经过 `translate_http_response_parameters` 编码后**:
```
http:%2F%2Fsglang-rbg-1p1d-qw14-prefill-1.s-sglang-rbg-1p1d-qw14-prefill.default.svc.cluster.local:8000
```

**最终 DELETE 请求**:
```
DELETE http://router:8000/workers/http:%2F%2Fsglang-rbg-1p1d-qw14-prefill-1.s-sglang-rbg-1p1d-qw14-prefill.default.svc.cluster.local:8000
```

#### 可能导致的问题

1. **Key 不匹配**:
   - 注册时存储的 key: `http://worker:8000`
   - 删除时查找的 key: `http:%2F%2Fworker:8000`
   - 如果 Router 不做正确的解码,这两个 key 不匹配

2. **路径参数解析问题**:
   - URL 中的 `:` 可能被 Web 框架误识别为路径分隔符
   - URL 编码不完整可能导致解析失败

3. **Web 框架自动解码问题**:
   - FastAPI/Flask 等框架会自动 URL 解码路径参数
   - 但 Patio 的编码方式可能与框架预期不一致

### 问题 3: Pod 快速删除导致注销来不及执行

即使 SIGTERM 信号发送给 Patio sidecar,默认的 `terminationGracePeriodSeconds` 可能不足:

- Kubernetes 默认: 30 秒
- 需要时间: 发送 SIGTERM → Patio 接收信号 → 调用 DELETE API → 等待响应 → 退出
- 如果网络延迟或 Router 响应慢,30 秒可能不够

---

## 排查步骤

### 步骤 1: 检查 Router 日志

查看删除请求是否到达以及有什么错误:

```bash
# 查看 Router 当前日志
kubectl logs <router-pod-name> -c sglang-router

# 实时监控日志
kubectl logs <router-pod-name> -c sglang-router -f
```

**期望看到**:
- DELETE 请求的日志
- 任何错误信息(如 "worker not found", "invalid URL" 等)

### 步骤 2: 测试不同的 DELETE 格式

创建测试脚本 `test_delete_worker.sh`:

```bash
#!/bin/bash

ROUTER_URL="http://127.0.0.1:30080"
WORKER_URL="http://sglang-rbg-1p1d-qw14-prefill-1.s-sglang-rbg-1p1d-qw14-prefill.default.svc.cluster.local:8000"

echo "========================================="
echo "测试前准备: 手动添加一个测试 worker"
echo "========================================="
curl -X POST "${ROUTER_URL}/workers" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "http://test-worker:8000",
    "worker_type": "prefill"
  }'
echo -e "\n"

echo "验证 worker 已添加:"
curl -s "${ROUTER_URL}/workers" | jq
echo -e "\n"

# 使用测试 worker URL 进行后续测试
TEST_WORKER_URL="http://test-worker:8000"

echo "========================================="
echo "测试 1: Patio 当前使用的方式(只替换 /)"
echo "========================================="
ENCODED_1=$(echo "$TEST_WORKER_URL" | sed 's|/|%2F|g')
echo "编码后 URL: ${ENCODED_1}"
echo "完整请求: DELETE ${ROUTER_URL}/workers/${ENCODED_1}"
curl -v -X DELETE "${ROUTER_URL}/workers/${ENCODED_1}" 2>&1 | grep -E "< HTTP|< Location|worker"
echo -e "\n"

echo "验证 worker 是否被删除:"
curl -s "${ROUTER_URL}/workers" | jq -r '.[] | .url'
echo -e "\n"

# 重新添加测试 worker
curl -s -X POST "${ROUTER_URL}/workers" \
  -H "Content-Type: application/json" \
  -d "{\"url\": \"${TEST_WORKER_URL}\", \"worker_type\": \"prefill\"}" > /dev/null

echo "========================================="
echo "测试 2: 完整 URL 编码"
echo "========================================="
ENCODED_2=$(python3 -c "from urllib.parse import quote; print(quote('$TEST_WORKER_URL', safe=''))")
echo "编码后 URL: ${ENCODED_2}"
echo "完整请求: DELETE ${ROUTER_URL}/workers/${ENCODED_2}"
curl -v -X DELETE "${ROUTER_URL}/workers/${ENCODED_2}" 2>&1 | grep -E "< HTTP|< Location|worker"
echo -e "\n"

echo "验证 worker 是否被删除:"
curl -s "${ROUTER_URL}/workers" | jq -r '.[] | .url'
echo -e "\n"

# 重新添加测试 worker
curl -s -X POST "${ROUTER_URL}/workers" \
  -H "Content-Type: application/json" \
  -d "{\"url\": \"${TEST_WORKER_URL}\", \"worker_type\": \"prefill\"}" > /dev/null

echo "========================================="
echo "测试 3: 不编码,直接传递"
echo "========================================="
echo "完整请求: DELETE ${ROUTER_URL}/workers/${TEST_WORKER_URL}"
curl -v -X DELETE "${ROUTER_URL}/workers/${TEST_WORKER_URL}" 2>&1 | grep -E "< HTTP|< Location|worker"
echo -e "\n"

echo "验证 worker 是否被删除:"
curl -s "${ROUTER_URL}/workers" | jq -r '.[] | .url'
echo -e "\n"

# 重新添加测试 worker
curl -s -X POST "${ROUTER_URL}/workers" \
  -H "Content-Type: application/json" \
  -d "{\"url\": \"${TEST_WORKER_URL}\", \"worker_type\": \"prefill\"}" > /dev/null

echo "========================================="
echo "测试 4: 使用请求体(JSON body)"
echo "========================================="
echo "完整请求: DELETE ${ROUTER_URL}/workers (body: {\"url\": \"${TEST_WORKER_URL}\"})"
curl -v -X DELETE "${ROUTER_URL}/workers" \
  -H "Content-Type: application/json" \
  -d "{\"url\": \"${TEST_WORKER_URL}\"}" 2>&1 | grep -E "< HTTP|< Location|worker"
echo -e "\n"

echo "验证 worker 是否被删除:"
curl -s "${ROUTER_URL}/workers" | jq -r '.[] | .url'
echo -e "\n"

# 重新添加测试 worker
curl -s -X POST "${ROUTER_URL}/workers" \
  -H "Content-Type: application/json" \
  -d "{\"url\": \"${TEST_WORKER_URL}\", \"worker_type\": \"prefill\"}" > /dev/null

echo "========================================="
echo "测试 5: 使用查询参数"
echo "========================================="
ENCODED_QUERY=$(python3 -c "from urllib.parse import quote; print(quote('$TEST_WORKER_URL'))")
echo "编码后 URL: ${ENCODED_QUERY}"
echo "完整请求: DELETE ${ROUTER_URL}/workers?url=${ENCODED_QUERY}"
curl -v -X DELETE "${ROUTER_URL}/workers?url=${ENCODED_QUERY}" 2>&1 | grep -E "< HTTP|< Location|worker"
echo -e "\n"

echo "验证 worker 是否被删除:"
curl -s "${ROUTER_URL}/workers" | jq -r '.[] | .url'
echo -e "\n"

echo "========================================="
echo "测试完成! 最终 worker 列表:"
echo "========================================="
curl -s "${ROUTER_URL}/workers" | jq
```

**执行测试**:

```bash
chmod +x test_delete_worker.sh
./test_delete_worker.sh > delete_test_results.txt 2>&1
cat delete_test_results.txt
```

**分析结果**:

查看哪个测试返回了 HTTP 202 状态码,并成功删除了 worker。

### 步骤 3: 检查 Router 的 worker 注册表

```bash
# 查看当前注册的所有 workers
curl http://127.0.0.1:30080/workers | jq

# 查看 worker 列表中存储的 URL 格式(这很重要!)
curl http://127.0.0.1:30080/workers | jq -r '.[] | .url'

# 如果有多个 worker,查看完整信息
curl http://127.0.0.1:30080/workers | jq '.[] | {url: .url, worker_type: .worker_type}'
```

**关键观察**:
- Worker URL 是否包含 `http://` 前缀?
- URL 是否被转义或编码存储?
- 不同 worker 的 URL 格式是否一致?

### 步骤 4: 增加 Router 日志级别

修改 RBG 配置,增加 Router 的日志详细程度:

```bash
kubectl edit rbg sglang-pd-qw14
```

修改 router role 的启动命令:

```yaml
- name: router
  # ... 其他配置 ...
  template:
    spec:
      containers:
      - name: sglang-router
        image: lmsysorg/sglang:v0.5.8-cu130-amd64-runtime
        command:
        - sh
        - -c
        - |
          python3 -m sglang_router.launch_router \
            --log-level debug \  # 改为 debug 级别
            --pd-disaggregation \
            --host 0.0.0.0 \
            --port 8000 \
            --model-path /models \
            --policy random \
            --prometheus-host 0.0.0.0 \
            --prometheus-port 9090
```

保存后等待 Router Pod 重启,然后再次测试删除操作。

### 步骤 5: 抓包分析实际 HTTP 请求

如果上述方法都无法确定问题,使用 tcpdump 抓包:

```bash
# 在 Router Pod 中抓包
kubectl exec -it <router-pod-name> -c sglang-router -- \
  tcpdump -i any -A 'port 8000 and (tcp[((tcp[12:1] & 0xf0) >> 2):4] = 0x44454c45)' -s 0

# 在另一个终端执行删除操作
curl -X DELETE http://127.0.0.1:30080/workers/...
```

**分析抓包结果**:
- 请求路径是否正确?
- URL 编码格式如何?
- Router 是否返回了错误?

### 步骤 6: 检查 SGLang Router 源码

```bash
# 进入 Router 容器
kubectl exec -it <router-pod-name> -c sglang-router -- /bin/bash

# 查找 /workers 端点的实现
find /usr/local/lib/python*/dist-packages -name "*.py" -type f -exec grep -l "def.*workers" {} \; 2>/dev/null

# 或者查找 sglang_router 模块
python3 -c "import sglang_router; print(sglang_router.__file__)"

# 查看具体实现
cat /path/to/router_implementation.py | grep -A 20 "DELETE"
```

**关注点**:
- DELETE 端点如何定义?
- 路径参数如何提取?
- Worker 如何从注册表中删除?

### 步骤 7: 测试 Patio 的注销逻辑

模拟 Pod 删除,观察 Patio 的行为:

```bash
# 选择一个 prefill pod
POD_NAME="sglang-pd-qw14-prefill-1"

# 查看当前 worker 列表
curl http://127.0.0.1:30080/workers | jq

# 手动向 Patio sidecar 发送 SIGTERM
kubectl exec -it ${POD_NAME} -c patio-runtime -- kill -TERM 1

# 立即查看 Patio 日志
kubectl logs ${POD_NAME} -c patio-runtime -f

# 等待几秒后查看 worker 列表
sleep 10
curl http://127.0.0.1:30080/workers | jq
```

**期望看到**:
- Patio 日志中有 "unregistered worker successfully" 消息
- Worker 从列表中消失

**如果失败**:
- 查看具体的错误信息
- 确认是 DELETE API 问题还是信号处理问题

---

## 解决方案

根据排查结果,选择对应的解决方案:

### 解决方案 A: 修复 Patio 的 URL 编码

**适用条件**: 步骤 2 测试发现测试 2(完整 URL 编码)有效

**操作步骤**:

1. 编辑 Patio 源码:

```bash
cd /home/zzy/code/rbg
vi python/patio/topo/client/sgl_topo_client.py
```

2. 修改内容:

```python
# 在文件顶部添加导入(约第 6 行)
from urllib.parse import quote

# 修改 translate_http_response_parameters 函数(约第 62-63 行)
def translate_http_response_parameters(raw: str) -> str:
    # 旧版本(有问题):
    # return raw.replace("/", "%2F")

    # 新版本:使用标准 URL 编码
    return quote(raw, safe='')  # safe='' 表示编码所有字符,包括 :/ 等
```

3. 重新构建 Patio 镜像:

```bash
cd /home/zzy/code/rbg

# 构建新镜像
docker build -t rolebasedgroup/rbgs-patio-runtime:v0.5.0-fix-delete \
  -f python/patio/Dockerfile .

# 推送到镜像仓库(如果集群无法访问本地镜像)
docker push rolebasedgroup/rbgs-patio-runtime:v0.5.0-fix-delete
```

4. 更新 ClusterEngineRuntimeProfile:

```bash
kubectl edit clusterengineruntimeprofile sglang-pd-runtime
```

修改镜像:

```yaml
spec:
  containers:
    - image: rolebasedgroup/rbgs-patio-runtime:v0.5.0-fix-delete
      imagePullPolicy: Always
      name: patio-runtime
      # ... 其他配置保持不变
```

5. 重启 Pods 应用新镜像:

```bash
# 删除现有 pods
kubectl delete pods -l rolebasedgroup.workloads.x-k8s.io/name=sglang-pd-qw14

# 等待重建
kubectl get pods -w
```

### 解决方案 B: 改用 JSON body 传递 URL

**适用条件**: 步骤 2 测试发现测试 4(JSON body)有效

**操作步骤**:

1. 编辑 Patio 源码:

```bash
vi python/patio/topo/client/sgl_topo_client.py
```

2. 修改 `unregister()` 方法(约第 133-151 行):

```python
def unregister(self):
    def f():
        url = f"http://{self.worker_endpoint}"
        # 不再使用路径参数,改用 DELETE + JSON body
        worker_registration_url = f"http://{self.sgl_router_endpoint}/workers"
        resp = requests.delete(worker_registration_url,
                             json={"url": url},
                             headers={"Content-Type": "application/json"})
        if resp.status_code == 202:
            logger.info(f"unregistered worker successfully.")
        else:
            raise Exception(
                f"unregister failed, url: {worker_registration_url}, status_code: {resp.status_code}, content: {resp.text}")

    try:
        utils.retry(f, retry_times=60, interval=3)
        return True
    except Exception as e:
        logger.error(f"failed to unregister worker: {e}")
        traceback.print_exc()
        return False
```

3. 后续步骤同解决方案 A(重新构建镜像、更新 Profile、重启 Pods)

### 解决方案 C: 使用不编码的路径

**适用条件**: 步骤 2 测试发现测试 3(不编码)有效

**操作步骤**:

1. 编辑 Patio 源码:

```bash
vi python/patio/topo/client/sgl_topo_client.py
```

2. 修改 `unregister()` 方法(约第 133-151 行):

```python
def unregister(self):
    def f():
        url = f"http://{self.worker_endpoint}"
        # 直接使用 URL,不编码,让 requests 库自动处理
        worker_registration_url = f"http://{self.sgl_router_endpoint}/workers/{url}"
        resp = requests.delete(worker_registration_url)
        if resp.status_code == 202:
            logger.info(f"unregistered worker successfully.")
        else:
            raise Exception(
                f"unregister failed, url: {worker_registration_url}, status_code: {resp.status_code}, content: {resp.text}")

    try:
        utils.retry(f, retry_times=60, interval=3)
        return True
    except Exception as e:
        logger.error(f"failed to unregister worker: {e}")
        traceback.print_exc()
        return False
```

3. 后续步骤同解决方案 A

### 解决方案 D: 添加 PreStop Hook 和增加优雅终止时间

**适用条件**: DELETE API 修复后,确保 Pod 有足够时间完成注销

**操作步骤**:

1. 修改 `patio-profile.yaml`:

```bash
vi examples/pd-disagg/sglang/patio-profile.yaml
```

内容:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: ClusterEngineRuntimeProfile
metadata:
  name: sglang-pd-runtime
spec:
  containers:
    - image: rolebasedgroup/rbgs-patio-runtime:v0.5.0-fix-delete
      imagePullPolicy: Always
      name: patio-runtime
      env:
        - name: TOPO_TYPE
          value: "sglang"
        - name: SGL_ROUTER_ROLE_NAME
          value: "router"
      # 添加 lifecycle hook
      lifecycle:
        preStop:
          exec:
            command:
            - /bin/sh
            - -c
            - |
              echo "Triggering graceful shutdown of Patio..."
              # 发送 SIGTERM 给主进程(PID 1)
              kill -TERM 1
              # 等待最多 50 秒让注销完成
              for i in $(seq 1 50); do
                if ! ps -p 1 > /dev/null 2>&1; then
                  echo "Patio process exited gracefully"
                  exit 0
                fi
                sleep 1
              done
              echo "Warning: Timeout waiting for Patio shutdown"
  updateStrategy: NoUpdate
```

2. 应用新的 Profile:

```bash
kubectl apply -f examples/pd-disagg/sglang/patio-profile.yaml
```

3. 修改 RBG 配置,增加 terminationGracePeriodSeconds:

```bash
kubectl edit rbg sglang-pd-qw14
```

为 prefill 和 decode role 添加:

```yaml
- name: prefill
  # ... 其他配置 ...
  template:
    spec:
      terminationGracePeriodSeconds: 60  # 增加到 60 秒
      tolerations:
      - key: node.kubernetes.io/disk-pressure
        operator: Exists
        effect: NoSchedule
      # ... 其他配置

- name: decode
  # ... 其他配置 ...
  template:
    spec:
      terminationGracePeriodSeconds: 60  # 增加到 60 秒
      tolerations:
      - key: node.kubernetes.io/disk-pressure
        operator: Exists
        effect: NoSchedule
      # ... 其他配置
```

### 解决方案 E: 添加 Lifecycle PreDelete Hook(高级方案)

**适用条件**: 需要更精细的生命周期控制

**操作步骤**:

1. 修改 RBG 配置,为 prefill 和 decode 添加 lifecycle:

```bash
kubectl edit rbg sglang-pd-qw14
```

```yaml
- name: prefill
  replicas: 2
  scalingAdapter:
    enable: true
  workload:
    lifecycle:  # 添加 lifecycle 配置
      preDelete:
        finalizersHandler:
          - "patio.sglang/graceful-shutdown"
        markPodNotReady: true  # 删除前标记为 NotReady
  # ... 其他配置

- name: decode
  replicas: 2
  scalingAdapter:
    enable: true
  workload:
    lifecycle:  # 添加 lifecycle 配置
      preDelete:
        finalizersHandler:
          - "patio.sglang/graceful-shutdown"
        markPodNotReady: true
  # ... 其他配置
```

2. **注意**: 这个方案需要创建一个 Finalizer Controller 来处理 `patio.sglang/graceful-shutdown` finalizer,这超出了简单配置的范围,需要额外的代码开发。

**推荐**: 优先使用解决方案 D(preStop Hook),它更简单且不需要额外的 controller。

---

## 验证方案

完成修复后,按以下步骤验证:

### 1. 验证扩容(应该仍然正常)

```bash
# 增加 replicas
kubectl patch rbg sglang-pd-qw14 --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/1/replicas", "value": 3}]'

# 等待新 Pod Ready
kubectl get pods -l rolebasedgroup.workloads.x-k8s.io/name=sglang-pd-qw14 -w

# 查看 Patio 日志,应该看到注册成功
kubectl logs sglang-pd-qw14-prefill-2 -c patio-runtime | grep "registered worker successfully"

# 验证 worker 列表
curl http://127.0.0.1:30080/workers | jq -r '.[] | .url' | grep prefill
```

### 2. 验证缩容(重点验证)

```bash
# 减少 replicas
kubectl patch rbg sglang-pd-qw14 --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/1/replicas", "value": 2}]'

# 观察 Pod 删除过程
kubectl get pods -w | grep prefill

# 实时查看即将删除的 Pod 的 Patio 日志
kubectl logs sglang-pd-qw14-prefill-2 -c patio-runtime -f
```

**期望看到的日志**:
```
Triggering graceful shutdown of Patio...
unregistered worker successfully.
Patio process exited gracefully
```

```bash
# 等待 Pod 完全删除
kubectl wait --for=delete pod/sglang-pd-qw14-prefill-2 --timeout=120s

# 验证 worker 已从列表中删除
curl http://127.0.0.1:30080/workers | jq -r '.[] | .url' | grep prefill
# 应该只看到 prefill-0 和 prefill-1,没有 prefill-2
```

### 3. 多次缩放测试

```bash
# 脚本化测试
for i in {1..5}; do
  echo "=== Round $i: Scale UP ==="
  kubectl patch rbg sglang-pd-qw14 --type='json' \
    -p='[{"op": "replace", "path": "/spec/roles/1/replicas", "value": 3}]'
  sleep 60

  echo "Worker count: $(curl -s http://127.0.0.1:30080/workers | jq -r '.[] | select(.worker_type=="prefill") | .url' | wc -l)"

  echo "=== Round $i: Scale DOWN ==="
  kubectl patch rbg sglang-pd-qw14 --type='json' \
    -p='[{"op": "replace", "path": "/spec/roles/1/replicas", "value": 2}]'
  sleep 60

  echo "Worker count: $(curl -s http://127.0.0.1:30080/workers | jq -r '.[] | select(.worker_type=="prefill") | .url' | wc -l)"

  # 验证 worker 数量是否正确
  WORKER_COUNT=$(curl -s http://127.0.0.1:30080/workers | jq -r '.[] | select(.worker_type=="prefill") | .url' | wc -l)
  if [ "$WORKER_COUNT" -ne 2 ]; then
    echo "ERROR: Expected 2 prefill workers, got $WORKER_COUNT"
    curl -s http://127.0.0.1:30080/workers | jq
    exit 1
  fi
done

echo "All tests passed!"
```

### 4. 压力测试

在缩容过程中发送推理请求,验证服务稳定性:

```bash
# 终端 1: 持续发送请求
while true; do
  curl -X POST http://127.0.0.1:30080/v1/completions \
    -H "Content-Type: application/json" \
    -d '{
      "model": "qwen3-14b",
      "prompt": "Hello",
      "max_tokens": 10
    }'
  sleep 1
done

# 终端 2: 执行缩容
kubectl patch rbg sglang-pd-qw14 --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/1/replicas", "value": 1}]'
```

**期望结果**:
- 请求应该继续成功(可能有少量失败,但不应该大量 500 错误)
- Worker 应该优雅退出,不会突然中断正在处理的请求

---

## 调试技巧

### 1. 启用详细日志

修改 Patio 的日志级别:

```bash
kubectl edit clusterengineruntimeprofile sglang-pd-runtime
```

```yaml
spec:
  containers:
    - image: rolebasedgroup/rbgs-patio-runtime:v0.5.0-fix-delete
      name: patio-runtime
      args:
        - --log-level
        - DEBUG  # 改为 DEBUG 级别
      # ... 其他配置
```

### 2. 使用 kubectl events 观察

```bash
# 实时查看事件
kubectl get events -w | grep sglang-pd-qw14

# 查看特定 Pod 的事件
kubectl describe pod sglang-pd-qw14-prefill-2 | grep -A 20 Events
```

### 3. 检查 Instance 状态

```bash
# 查看 Instance 资源
kubectl get instances -l rolebasedgroup.workloads.x-k8s.io/name=sglang-pd-qw14

# 查看详细信息
kubectl describe instance <instance-name>

# 查看生命周期状态
kubectl get instances -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.lifecycleState}{"\n"}{end}'
```

### 4. 监控 Patio sidecar 的信号处理

在 Patio 容器中:

```bash
# 查看进程
kubectl exec -it <pod-name> -c patio-runtime -- ps aux

# 发送测试信号
kubectl exec -it <pod-name> -c patio-runtime -- kill -TERM 1

# 查看日志
kubectl logs <pod-name> -c patio-runtime -f
```

### 5. 使用 curl 详细模式调试 DELETE 请求

```bash
# 使用 -v 查看完整的 HTTP 交互
curl -v -X DELETE "http://127.0.0.1:30080/workers/..." 2>&1 | tee delete_debug.log

# 查看响应头
grep "< HTTP" delete_debug.log
grep "< " delete_debug.log
```

---

## 相关文件清单

### RBG 代码文件

| 文件 | 位置 | 说明 |
|------|------|------|
| 缩容逻辑 | `pkg/reconciler/instanceset/sync/scale.go:244-271` | deleteInstances 函数,处理 Pod 删除 |
| Patio 注册 | `python/patio/app.py:73-108` | run_topo_client 函数,启动时注册 |
| Patio 注销 | `python/patio/app.py:67-71` | 信号处理器,收到 SIGTERM 时注销 |
| SGLang Client | `python/patio/topo/client/sgl_topo_client.py:133-151` | unregister 方法实现 |
| URL 编码 | `python/patio/topo/client/sgl_topo_client.py:62-63` | translate_http_response_parameters 函数 |
| Lifecycle 定义 | `api/workloads/v1alpha1/instanceset_types.go:82-97` | Lifecycle 和 LifecycleHook 类型定义 |
| Profile 示例 | `examples/pd-disagg/sglang/patio-profile.yaml` | ClusterEngineRuntimeProfile 配置 |

### 配置文件

| 文件 | 说明 |
|------|------|
| `patio-profile.yaml` | Patio sidecar 的运行时 Profile |
| `sglang-pd.yaml` | SGLang PD 分离部署示例 |
| 你的 RBG YAML | 包含 router、prefill、decode 三个 role 的配置 |

---

## 常见问题

### Q1: 为什么扩容正常但缩容不正常?

**A**: 扩容时,新 Pod 启动,Patio 主动调用 POST /workers 注册。缩容时,RBG 直接删除 Pod,Patio 的 SIGTERM 信号处理器可能:
1. 没有被触发(lifecycle hook 未配置)
2. 来不及执行(terminationGracePeriodSeconds 太短)
3. 执行了但 DELETE API 失败(URL 编码问题)

### Q2: 为什么 DELETE API 手动调用也不工作?

**A**: 可能的原因:
1. URL 编码格式不对,Router 无法匹配 worker key
2. Router 的 DELETE 端点实现有 bug
3. Router 版本太旧,DELETE 功能未实现或有缺陷

### Q3: terminationGracePeriodSeconds 应该设置多少?

**A**: 建议 60 秒。需要考虑:
- 发送 SIGTERM: ~1 秒
- Patio 处理信号: ~1 秒
- 调用 DELETE API: ~5 秒(包括重试)
- Router 处理删除: ~5 秒
- 缓冲时间: ~48 秒

### Q4: 如何确认 Patio 收到了 SIGTERM 信号?

**A**: 查看 Patio 日志:
```bash
kubectl logs <pod-name> -c patio-runtime | grep -E "SIGTERM|unregister|shutdown"
```

如果看到 "unregistered worker successfully",说明信号被正确处理。

### Q5: 修改后如何快速测试?

**A**:
```bash
# 1. 重建 Patio 镜像
docker build -t rolebasedgroup/rbgs-patio-runtime:test -f python/patio/Dockerfile .

# 2. 更新 Profile
kubectl patch clusterengineruntimeprofile sglang-pd-runtime \
  --type='json' \
  -p='[{"op": "replace", "path": "/spec/containers/0/image", "value": "rolebasedgroup/rbgs-patio-runtime:test"}]'

# 3. 删除一个 Pod 触发重建
kubectl delete pod sglang-pd-qw14-prefill-0

# 4. 等待新 Pod Ready
kubectl wait --for=condition=ready pod/sglang-pd-qw14-prefill-0 --timeout=300s

# 5. 测试缩容
kubectl patch rbg sglang-pd-qw14 --type='json' \
  -p='[{"op": "replace", "path": "/spec/roles/1/replicas", "value": 1}]'
```

---

## 下一步行动

1. **明天首先执行**: 步骤 2 的测试脚本,确定正确的 DELETE API 调用方式
2. **根据测试结果**: 选择对应的解决方案(A/B/C)
3. **同时实施**: 解决方案 D(preStop Hook),确保优雅关闭
4. **验证**: 按验证方案进行完整测试
5. **记录**: 将测试结果和最终方案记录到这个文档

---

## 总结

### 问题本质

1. **RBG 层面**: 缩容时直接删除 Pod,没有给 Patio 足够时间和机制来注销
2. **Patio 层面**: URL 编码方式可能与 Router 预期不符
3. **Router 层面**: DELETE API 可能存在实现问题或版本 bug

### 解决思路

1. **短期**: 修复 URL 编码,添加 preStop hook,增加优雅终止时间
2. **长期**: 考虑升级 SGLang Router 版本,或向上游提交 bug 报告

### 优先级

1. **P0**: 运行测试脚本,确定 DELETE API 的正确调用方式
2. **P1**: 修复 Patio 的 URL 编码或调用方式
3. **P2**: 添加 preStop hook 和增加 terminationGracePeriodSeconds
4. **P3**: 完整验证和压力测试

---

**文档版本**: v1.0
**创建时间**: 2026-02-04
**最后更新**: 2026-02-04
**作者**: Claude Code Analysis

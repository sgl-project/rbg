# 操作文档：为 RBG 服务配置弹性伸缩策略

> 对应概念文档：[8. 为 RBG 服务配置弹性伸缩策略](08-configuring-autoscaling.md)

## 目标

验证 RBG 的弹性伸缩机制，包括：

1. 启用 ScalingAdapter 并通过 Scale 子资源手动伸缩
2. 配置 HPA 实现指标驱动伸缩
3. 配置 KEDA 实现事件驱动伸缩
4. 配置 RBG Planner 实现 SLA 驱动伸缩（PD 分离场景）

## 前提条件

- Kubernetes 集群版本 >= 1.24
- 已安装 RBG Controller
- 镜像可访问：`lmsysorg/sglang:v0.5.9`
- 操作二需要安装 Metrics Server
- 操作三需要安装 KEDA 和 Prometheus
- 操作四需要安装 RBG Planner（`helm install rbg-planner oci://ghcr.io/sgl-project/charts/rbg-planner -n rbg-system --create-namespace`）

> **说明**：操作一至操作二使用 `sleep 3600` 作为占位命令，专注于验证 ScalingAdapter 和伸缩器配置的控制面行为，无需 GPU。操作三使用模拟指标端点验证 KEDA 自动伸缩全流程（含扩容和缩容）。操作四需要真实推理服务，请替换为完整的推理引擎启动命令。

---

## 操作一：启用 ScalingAdapter 并验证 Scale 子资源

### 步骤 1：创建启用 ScalingAdapter 的 RBG

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: scaling-demo
spec:
  roles:
    - name: backend
      replicas: 2
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
                resources:
                  requests:
                    cpu: "1"
                    memory: "1Gi"
                  limits:
                    cpu: "1"
                    memory: "1Gi"
EOF

# 等待 Pod 就绪
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=scaling-demo --timeout=300s
```

### 步骤 2：验证 RBGSA 自动创建

```bash
# 查看自动创建的 RBGSA
kubectl get rbgsa

> NAME                   PHASE   REPLICAS   READY_REPLICAS   AGE
> scaling-demo-backend   Bound   2          2                8m39s
```

```bash
# 查看 RBGSA 详情
kubectl get rbgsa scaling-demo-backend -o yaml

> apiVersion: workloads.x-k8s.io/v1alpha2
> kind: RoleBasedGroupScalingAdapter
> metadata:
>   creationTimestamp: "2026-07-08T03:06:50Z"
>   generation: 2
>   labels:
>     rbg.workloads.x-k8s.io/group-name: scaling-demo
>     rbg.workloads.x-k8s.io/role-name: backend
>   name: scaling-demo-backend
>   ownerReferences:
>   - apiVersion: workloads.x-k8s.io/v1alpha2
>     blockOwnerDeletion: true
>     kind: RoleBasedGroup
>     name: scaling-demo
>     uid: 91f2c7da-9a0f-4c6c-b032-7befc81ca1e6
>   resourceVersion: "27637990"
>   uid: bbf80047-5e7d-4eac-9f49-6b87d045dceb
> spec:
>   replicas: 2
>   scaleTargetRef:
>     name: scaling-demo
>     role: backend
> status:
>   phase: Bound
>   readyReplicas: 2
>   replicas: 2
>   selector: rbg.workloads.x-k8s.io/group-name=scaling-demo,rbg.workloads.x-k8s.io/group-uid=8481eaa0eb18d5d84918728022b8816dfb465d0c,rbg.workloads.x-k8s.io/role-name=backend
```

**预期输出：**

- RBGSA `scaling-demo-backend` 自动创建（命名规则：`<rbg-name>-<role-name>`）
- RBGSA 的 `status.replicas` 为 2
- RBGSA 的 `status.selector` 包含 Pod 标签选择器
- RBGSA 的 `status.phase` 为 `Bound`

### 步骤 3：通过 Scale 子资源手动伸缩

```bash
# 扩容到 4 副本
kubectl scale rbgsa scaling-demo-backend --replicas=4

> rolebasedgroupscalingadapter.workloads.x-k8s.io/scaling-demo-backend scaled
```

```bash
# 查看 Pod 数量变化
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=scaling-demo

> NAME                     READY   STATUS              RESTARTS   AGE
> scaling-demo-backend-0   1/1     Running             0          11m
> scaling-demo-backend-1   1/1     Running             0          11m
> scaling-demo-backend-2   0/1     ContainerCreating   0          12s
> scaling-demo-backend-3   0/1     ContainerCreating   0          12s
```

```bash
# 验证副本数已同步到 RBG
kubectl get rbg scaling-demo -o jsonpath='{range .spec.roles[*]}{.name}{"="}{.replicas}{"\n"}{end}'

> backend=4
```

```bash
# 缩容到 1 副本
kubectl scale rbgsa scaling-demo-backend --replicas=1

> rolebasedgroupscalingadapter.workloads.x-k8s.io/scaling-demo-backend scaled
```

```bash
# 查看 Pod 数量变化
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=scaling-demo

> NAME                     READY   STATUS    RESTARTS   AGE
> scaling-demo-backend-0   1/1     Running   0          12m
```

**预期输出：**

- 扩容后 Pod 数量从 2 增加到 4
- RBG 的 `spec.roles[0].replicas` 同步更新为 4
- 缩容后 Pod 数量减少到 1

### 清理

```bash
kubectl delete rbg scaling-demo
```

---

## 操作二：HPA 指标驱动伸缩

### 步骤 1：创建启用 ScalingAdapter 的 RBG（HPA）

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: hpa-demo
spec:
  roles:
    - name: backend
      replicas: 1
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
                resources:
                  requests:
                    cpu: "500m"
                    memory: "512Mi"
                  limits:
                    cpu: "1"
                    memory: "1Gi"
EOF

# 等待 Pod 就绪
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=hpa-demo --timeout=300s
```

### 步骤 2：创建 HPA

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: hpa-demo-backend
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: hpa-demo-backend       # <rbg-name>-<role-name>
  minReplicas: 1
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 50
EOF
```

### 步骤 3：验证 HPA 配置

```bash
# 查看 HPA 状态
kubectl get hpa hpa-demo-backend

> NAME               REFERENCE                                       TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
> hpa-demo-backend   RoleBasedGroupScalingAdapter/hpa-demo-backend   cpu: 0%/50%   1         10        1          30s
```

```bash
# 查看 HPA 详细配置和状态
kubectl get hpa hpa-demo-backend -o yaml

> apiVersion: autoscaling/v2
> kind: HorizontalPodAutoscaler
> metadata:
>   creationTimestamp: "2026-07-08T03:36:00Z"
>   name: hpa-demo-backend
>   resourceVersion: "27658487"
>   uid: 74789e96-7b21-444f-ae77-7384afb81d6d
> spec:
>   maxReplicas: 10
>   metrics:
>   - resource:
>       name: cpu
>       target:
>         averageUtilization: 50
>         type: Utilization
>     type: Resource
>   minReplicas: 1
>   scaleTargetRef:
>     apiVersion: workloads.x-k8s.io/v1alpha2
>     kind: RoleBasedGroupScalingAdapter
>     name: hpa-demo-backend
> status:
>   conditions:
>   - lastTransitionTime: "2026-07-08T03:36:15Z"
>     message: recent recommendations were higher than current one, applying the highest
>       recent recommendation
>     reason: ScaleDownStabilized
>     status: "True"
>     type: AbleToScale
>   - lastTransitionTime: "2026-07-08T03:36:30Z"
>     message: the HPA was able to successfully calculate a replica count from cpu resource
>       utilization (percentage of request)
>     reason: ValidMetricFound
>     status: "True"
>     type: ScalingActive
>   - lastTransitionTime: "2026-07-08T03:36:30Z"
>     message: the desired count is within the acceptable range
>     reason: DesiredWithinRange
>     status: "False"
>     type: ScalingLimited
>   currentMetrics:
>   - resource:
>       current:
>         averageUtilization: 0
>         averageValue: "0"
>       name: cpu
>     type: Resource
>   currentReplicas: 1
>   desiredReplicas: 1
```

```bash
# 确认 HPA 目标指向 RBGSA
kubectl get hpa hpa-demo-backend -o jsonpath='{.spec.scaleTargetRef}'

> {"apiVersion":"workloads.x-k8s.io/v1alpha2","kind":"RoleBasedGroupScalingAdapter","name":"hpa-demo-backend"}
```

```bash
# 查看 RBGSA 状态（HPA 通过 RBGSA 的 Scale 子资源控制副本数）
kubectl get rbgsa hpa-demo-backend

> NAME               PHASE   REPLICAS   READY_REPLICAS   AGE
> hpa-demo-backend   Bound   1          1                2m43s
```

**预期输出：**

- HPA 的 `TARGETS` 列显示当前 CPU 利用率和目标值（如 `0%/50%`）
- HPA 的 `scaleTargetRef.kind` 为 `RoleBasedGroupScalingAdapter`
- HPA 的 `scaleTargetRef.name` 为 `hpa-demo-backend`
- RBGSA 的 `status.replicas` 与 HPA 管理的副本数一致

### 步骤 4：触发伸缩并观察

```bash
# 在 Pod 中制造 CPU 负载（触发扩容）
# 启动 4 个死循环进程，持续消耗 CPU
POD_NAME=$(kubectl get pods -l rbg.workloads.x-k8s.io/group-name=hpa-demo -o jsonpath='{.items[0].metadata.name}')
kubectl exec ${POD_NAME} -- python3 -c "
import multiprocessing
def burn():
    while True:
        pass
for _ in range(4):
    multiprocessing.Process(target=burn).start()
"
```

```bash
# 持续观察 HPA 状态
kubectl get hpa hpa-demo-backend -w

> NAME               REFERENCE                                       TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
> hpa-demo-backend   RoleBasedGroupScalingAdapter/hpa-demo-backend   cpu: 0%/50%   1         10        1          111s
> hpa-demo-backend   RoleBasedGroupScalingAdapter/hpa-demo-backend   cpu: 199%/50%   1         10        1          2m30s
```

```bash
# 观察 Pod 数量变化
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=hpa-demo -w

> NAME                 READY   STATUS    RESTARTS   AGE
> hpa-demo-backend-0   1/1     Running   0          3m1s
> hpa-demo-backend-1   0/1     Pending   0          0s
> hpa-demo-backend-2   0/1     Pending   0          0s
> hpa-demo-backend-3   0/1     Pending   0          0s
> hpa-demo-backend-1   0/1     ContainerCreating   0          0s
> hpa-demo-backend-2   0/1     ContainerCreating   0          0s
> hpa-demo-backend-3   0/1     ContainerCreating   0          0s
```

### 清理（HPA）

```bash
kubectl delete hpa hpa-demo-backend
kubectl delete rbg hpa-demo
```

---

## 操作三：KEDA 事件驱动伸缩

> **说明**：本操作通过模拟指标端点验证 KEDA 自动伸缩全流程，无需 GPU。Pod 内运行 Python HTTP 服务器模拟 `sglang_num_queue_requests` 指标，通过 HTTP 接口控制指标值触发扩缩容。

### 步骤 1：部署 Prometheus

```bash
# 部署 Prometheus，配置为自动采集带 prometheus.io/scrape: "true" annotation 的 Pod
cat <<'EOF' | kubectl apply -f -
apiVersion: v1
kind: Namespace
metadata:
  name: monitoring
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: prometheus
  namespace: monitoring
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: prometheus
rules:
  - apiGroups: [""]
    resources: ["pods", "nodes", "endpoints", "services"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: prometheus
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: prometheus
subjects:
  - kind: ServiceAccount
    name: prometheus
    namespace: monitoring
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: prometheus-config
  namespace: monitoring
data:
  prometheus.yml: |
    global:
      scrape_interval: 5s
    scrape_configs:
      - job_name: 'kubernetes-pods'
        kubernetes_sd_configs:
          - role: pod
        relabel_configs:
          - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scrape]
            action: keep
            regex: "true"
          - source_labels: [__address__, __meta_kubernetes_pod_annotation_prometheus_io_port]
            action: replace
            regex: ([^:]+)(?::\d+)?;(\d+)
            replacement: $1:$2
            target_label: __address__
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: prometheus
  namespace: monitoring
spec:
  replicas: 1
  selector:
    matchLabels:
      app: prometheus
  template:
    metadata:
      labels:
        app: prometheus
    spec:
      serviceAccountName: prometheus
      containers:
        - name: prometheus
          image: prom/prometheus:latest
          args:
            - "--config.file=/etc/prometheus/prometheus.yml"
            - "--storage.tsdb.path=/prometheus"
          ports:
            - containerPort: 9090
          volumeMounts:
            - name: config
              mountPath: /etc/prometheus
            - name: storage
              mountPath: /prometheus
      volumes:
        - name: config
          configMap:
            name: prometheus-config
        - name: storage
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: prometheus
  namespace: monitoring
spec:
  selector:
    app: prometheus
  ports:
    - port: 9090
      targetPort: 9090
EOF

# 等待 Prometheus 就绪
kubectl wait --for=condition=ready pod -l app=prometheus -n monitoring --timeout=60s
```

### 步骤 2：创建启用 ScalingAdapter 的 RBG（含模拟指标端点）

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: keda-demo
spec:
  roles:
    - name: backend
      replicas: 1
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          metadata:
            annotations:
              prometheus.io/scrape: "true"
              prometheus.io/port: "9091"
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command:
                  - python3
                  - -c
                  - |
                    import http.server
                    from urllib.parse import urlparse, parse_qs
                    queue = [0]
                    class H(http.server.BaseHTTPRequestHandler):
                        def do_GET(self):
                            if self.path == '/metrics':
                                self.send_response(200)
                                self.send_header('Content-Type', 'text/plain')
                                self.end_headers()
                                self.wfile.write(f'sglang_num_queue_requests{{rbg="keda-demo",role="backend"}} {queue[0]}\n'.encode())
                            elif self.path.startswith('/set'):
                                q = parse_qs(urlparse(self.path).query)
                                queue[0] = int(q.get('value', ['0'])[0])
                                self.send_response(200)
                                self.end_headers()
                                self.wfile.write(b'OK')
                            else:
                                self.send_response(404)
                                self.end_headers()
                        def log_message(self, *a): pass
                    http.server.HTTPServer(('0.0.0.0', 9091), H).serve_forever()
                ports:
                  - containerPort: 9091
                resources:
                  requests:
                    cpu: "1"
                    memory: "1Gi"
                  limits:
                    cpu: "1"
                    memory: "1Gi"
EOF

# 等待 Pod 就绪
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=keda-demo --timeout=300s
```

> **说明**：Pod 内运行 Python HTTP 服务器，模拟 SGLang 的 Prometheus 指标端点：
>
> - `GET /metrics`：返回 `sglang_num_queue_requests{rbg="keda-demo",role="backend"} 0`
> - `GET /set?value=200`：将指标值设为 200（用于触发扩容）
> - `GET /set?value=0`：将指标值设为 0（用于触发缩容）

### 步骤 3：创建 KEDA ScaledObject

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: keda-demo-backend
spec:
  scaleTargetRef:
    apiVersion: workloads.x-k8s.io/v1alpha2
    kind: RoleBasedGroupScalingAdapter
    name: keda-demo-backend       # <rbg-name>-<role-name>
  minReplicaCount: 1
  maxReplicaCount: 10
  pollingInterval: 30
  triggers:
    - type: prometheus
      metadata:
        serverAddress: http://prometheus.monitoring:9090
        metricName: inference_queue_length
        query: |
          avg(sglang_num_queue_requests{rbg="keda-demo", role="backend"})
        threshold: "100"
EOF
```

### 步骤 4：验证 KEDA 配置和指标采集

```bash
# 验证 Prometheus 已采集到指标（等待约 10s 让 Prometheus 完成首次采集）
kubectl run tmp-curl --rm -i --restart=Never --image=curlimages/curl:latest -- \
  curl -s 'http://prometheus.monitoring:9090/api/v1/query?query=sglang_num_queue_requests'

> {"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"sglang_num_queue_requests","instance":"10.xx.xx.xx:9091","job":"kubernetes-pods","rbg":"keda-demo","role":"backend"},"value":[1783493293.481,"0"]}]}}
```

```bash
# 查看 ScaledObject 状态
kubectl get scaledobject keda-demo-backend

> NAME                SCALETARGETKIND                                            SCALETARGETNAME     MIN   MAX   READY   ACTIVE   FALLBACK   PAUSED   TRIGGERS     AUTHENTICATIONS   AGE
> keda-demo-backend   workloads.x-k8s.io/v1alpha2.RoleBasedGroupScalingAdapter   keda-demo-backend   1     10    True    False    False      False    prometheus                     41s
```

```bash
# 查看 KEDA 管理的 HPA（KEDA 会在后台创建 HPA）
kubectl get hpa -l scaledobject.keda.sh/name=keda-demo-backend

> NAME                         REFERENCE                                       TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
> keda-hpa-keda-demo-backend   RoleBasedGroupScalingAdapter/keda-demo-backend   0/100 (avg)   1         10        1          30s
```

**预期输出：**

- Prometheus 查询返回 `sglang_num_queue_requests` 值为 `0`
- ScaledObject `READY=True`，`ACTIVE=False`（指标值 0 < 阈值 100）
- KEDA 自动创建 HPA（名为 `keda-hpa-keda-demo-backend`）指向 RBGSA，`TARGETS` 显示 `0/100 (avg)`

### 步骤 5：触发扩容

```bash
# 将队列深度设为 200（超过 threshold 100），模拟推理请求积压
kubectl exec $(kubectl get pods -l rbg.workloads.x-k8s.io/group-name=keda-demo -o jsonpath='{.items[0].metadata.name}') -- \
  python3 -c "import urllib.request; urllib.request.urlopen('http://localhost:9091/set?value=200')"

# 等待 HPA 轮询（默认 15-30s），观察 HPA 状态变化
kubectl get hpa -l scaledobject.keda.sh/name=keda-demo-backend -w

> NAME                         REFERENCE                                        TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
> keda-hpa-keda-demo-backend   RoleBasedGroupScalingAdapter/keda-demo-backend   0/100 (avg)   1         10        1          88s
> keda-hpa-keda-demo-backend   RoleBasedGroupScalingAdapter/keda-demo-backend   200/100 (avg)   1         10        1          90s
```

```bash
# 观察 Pod 数量变化
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=keda-demo -w

> NAME                  READY   STATUS              RESTARTS   AGE
> keda-demo-backend-0   1/1     Running             0          2m32s
> keda-demo-backend-1   0/1     ContainerCreating   0          26s
> keda-demo-backend-1   1/1     Running             0          29s
```

> **说明**：KEDA 通过 Prometheus 查询到 `avg(sglang_num_queue_requests) = 200 > threshold = 100`，计算 desired replicas = `ceil(1 × 200/100) = 2`，通过 RBGSA Scale 子资源扩容到 2 副本。扩容后 HPA 显示 `100/100 (avg)`：KEDA 返回的 Prometheus 查询结果为 2 个 Pod 的平均值 `(200+0)/2 = 100`，等于阈值 100，HPA 不再继续扩容，系统稳定在 2 副本。

### 步骤 6：触发缩容

```bash
# 将队列深度设为 0，模拟请求清空
kubectl exec $(kubectl get pods -l rbg.workloads.x-k8s.io/group-name=keda-demo -o jsonpath='{.items[0].metadata.name}') -- \
  python3 -c "import urllib.request; urllib.request.urlopen('http://localhost:9091/set?value=0')"

# 等待 HPA 缩容（约 1-3 分钟），观察 Pod 变化
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=keda-demo -w

> NAME                  READY   STATUS        RESTARTS   AGE
> keda-demo-backend-0   1/1     Running       0          9m
> keda-demo-backend-1   1/1     Terminating   0          5m58s
```

```bash
# 验证 HPA 已缩容到 1 副本
kubectl get hpa -l scaledobject.keda.sh/name=keda-demo-backend

> NAME                         REFERENCE                              TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
> keda-hpa-keda-demo-backend   RoleBased.../keda-demo-backend         0/100 (avg)   1         10        1          7m42s
```

> **说明**：指标降为 0 后，HPA 在 1-3 分钟内检测到指标低于阈值并完成缩容。KEDA 默认不设置自定义 scaleDown behavior，缩容速度取决于 HPA 控制器的默认 downscale stabilization 窗口（通常 5 分钟内）。

### 清理（KEDA）

```bash
kubectl delete scaledobject keda-demo-backend
kubectl delete rbg keda-demo
# （可选）删除 Prometheus
# kubectl delete namespace monitoring
```

---

## 操作四：RBG Planner SLA 驱动伸缩（PD 分离）

> **说明**：本操作需要 GPU 节点和真实推理服务，验证 RBG Planner 的 PD 分离智能伸缩能力。如仅需验证 AutoScaler CR 配置，可设置 `dryRun: true`。

### 步骤 1：创建 PD 分离推理服务（启用 ScalingAdapter）

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-autoscaler-demo
  namespace: inference
spec:
  roles:
    - name: prefill
      replicas: 2
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
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
                ports:
                  - name: http
                    containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"

    - name: decode
      replicas: 4
      scalingAdapter:
        enable: true
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
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
                ports:
                  - name: http
                    containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"
EOF

# 等待所有 Pod 就绪
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=pd-autoscaler-demo -n inference --timeout=600s
```

### 步骤 2：创建 AutoScaler CR

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: inference-extension.rolebasedgroup.io/v1alpha1
kind: AutoScaler
metadata:
  name: pd-autoscaler-demo        # 必须与 RBG 名称一致
  namespace: inference
spec:
  scalingInterval: 180

  pattern:
    PDDisaggregated:
      prefill:
        roleName: prefill
        minReplicas: 1
        maxReplicas: 10
      decode:
        roleName: decode
        minReplicas: 1
        maxReplicas: 10

  implementation:
    DynamoPlanner:
      modelName: "Qwen/Qwen3-0.6B"

      # SLA 目标（毫秒）
      ttft: 200.0
      itl: 20.0

      # 负载预测配置
      loadPredictor: arima
      predictionWindow: 50

      # 修正因子
      noCorrection: false

      # Dry-run 模式（仅观测，不执行伸缩）
      dryRun: true

      profiling:
        image: "ghcr.io/sgl-project/rbg-profiler:latest"

      metricsEndpoint:
        metricSource: sglang
        port: 9091
EOF
```

### 步骤 3：验证 AutoScaler 运行状态

```bash
# 查看 AutoScaler 状态
kubectl get autoscaler -n inference

# 查看 AutoScaler 详情
kubectl get autoscaler pd-autoscaler-demo -n inference -o yaml

# 查看 Planner Pod
kubectl get pods -n inference -l app=rbg-planner

# 查看 Planner 日志（观察预测和计算结果）
kubectl logs -n inference -l app=rbg-planner --tail=50

# 查看当前副本数决策（dryRun 模式下不实际伸缩）
kubectl get autoscaler pd-autoscaler-demo -n inference -o jsonpath='{.status}'

# 查看 RBG 各角色实际副本数（dryRun 模式下应保持不变）
kubectl get rbg pd-autoscaler-demo -n inference -o jsonpath='{range .spec.roles[*]}{.name}{"="}{.replicas}{"\n"}{end}'

# 查看 RBGSA 状态（两个角色各一个）
kubectl get rbgsa -n inference
```

**预期输出：**

- AutoScaler 状态为 `Ready` 或 `Profiling`（首次运行需进行 Profiling）
- Planner Pod 正常运行
- 日志中显示观测到的 TTFT、ITL 指标和预测的副本数
- dryRun 模式下 RBG 副本数保持不变（prefill=2, decode=4）
- 两个 RBGSA 被创建：`pd-autoscaler-demo-prefill` 和 `pd-autoscaler-demo-decode`

### 步骤 4：关闭 dryRun 启用实际伸缩

```bash
# 确认 dryRun 观测数据合理后，关闭 dryRun
kubectl patch autoscaler pd-autoscaler-demo -n inference --type='json' \
  -p='[{"op": "replace", "path": "/spec/implementation/DynamoPlanner/dryRun", "value": false}]'

# 持续观察副本数变化
watch -n 5 'kubectl get rbg pd-autoscaler-demo -n inference -o jsonpath="{range .spec.roles[*]}{.name}={.replicas}\n{end}"'

# 观察 Pod 变化
kubectl get pods -n inference -l rbg.workloads.x-k8s.io/group-name=pd-autoscaler-demo -w
```

> **说明**：关闭 dryRun 后，Planner 会根据 SLA 目标和负载预测自动调整 Prefill 和 Decode 的副本数。观察一段时间后，如果伸缩行为符合预期，则配置完成。如需调参，可参考概念文档中的「推荐的调参流程」。

### 清理（RBG Planner）

```bash
kubectl delete autoscaler pd-autoscaler-demo -n inference
kubectl delete rbg pd-autoscaler-demo -n inference
```

---

## 总结

| 操作 | 伸缩方式 | 验证点 | 关键预期 |
| --- | --- | --- | --- |
| ScalingAdapter 基础 | 手动 `kubectl scale` | RBGSA 自动创建、Scale 子资源可用 | `kubectl scale rbgsa` 后副本数同步到 RBG |
| HPA 指标驱动 | 响应式（CPU 利用率） | HPA 目标指向 RBGSA | HPA 通过 RBGSA Scale 子资源控制副本数 |
| KEDA 事件驱动 | 响应式（外部指标） | ScaledObject 目标指向 RBGSA | KEDA 后台创建 HPA 指向 RBGSA |
| RBG Planner SLA 驱动 | 预测式（TTFT/ITL） | AutoScaler CR 创建、dryRun 观测 | Planner 将 Prefill 和 Decode 作为整体计算最优副本数 |

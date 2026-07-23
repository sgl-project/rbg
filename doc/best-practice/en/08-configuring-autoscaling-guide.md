# Operations Guide: Configuring Autoscaling Strategies for RBG Services

> Corresponding concept document: [8. Configuring Autoscaling Strategies for RBG Services](08-configuring-autoscaling.md)

## Objectives

Validate RBG's autoscaling mechanism, including:

1. Enabling ScalingAdapter and manual scaling via Scale subresource
2. Configuring HPA for metric-driven scaling
3. Configuring KEDA for event-driven scaling
4. Configuring RBG Planner for SLA-driven scaling (PD-disaggregated scenario)

## Prerequisites

- Kubernetes cluster version >= 1.24
- RBG Controller installed
- Image accessible: `lmsysorg/sglang:v0.5.9`
- Operation 2 requires Metrics Server installed
- Operation 3 requires KEDA and Prometheus installed
- Operation 4 requires RBG Planner installed (`helm install rbg-planner oci://ghcr.io/sgl-project/charts/rbg-planner -n rbg-system --create-namespace`)

> **Note**: Operations 1-2 use `sleep 3600` as a placeholder command, focusing on validating ScalingAdapter and scaler configuration control plane behavior without requiring GPU. Operation 3 uses a simulated metrics endpoint to validate the full KEDA autoscaling flow (including scale-up and scale-down). Operation 4 requires a real inference service — replace with the full inference engine startup command.

---

## Operation 1: Enable ScalingAdapter and Verify Scale Subresource

### Step 1: Create an RBG with ScalingAdapter Enabled

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

# Wait for Pods to be ready
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=scaling-demo --timeout=300s
```

### Step 2: Verify RBGSA Auto-Creation

```bash
# Check auto-created RBGSA
kubectl get rbgsa

> NAME                   PHASE   REPLICAS   READY_REPLICAS   AGE
> scaling-demo-backend   Bound   2          2                8m39s
```

```bash
# Check RBGSA details
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

**Expected output:**

- RBGSA `scaling-demo-backend` auto-created (naming convention: `<rbg-name>-<role-name>`)
- RBGSA's `status.replicas` is 2
- RBGSA's `status.selector` contains Pod label selector
- RBGSA's `status.phase` is `Bound`

### Step 3: Manual Scaling via Scale Subresource

```bash
# Scale up to 4 replicas
kubectl scale rbgsa scaling-demo-backend --replicas=4

> rolebasedgroupscalingadapter.workloads.x-k8s.io/scaling-demo-backend scaled
```

```bash
# Check Pod count changes
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=scaling-demo

> NAME                     READY   STATUS              RESTARTS   AGE
> scaling-demo-backend-0   1/1     Running             0          11m
> scaling-demo-backend-1   1/1     Running             0          11m
> scaling-demo-backend-2   0/1     ContainerCreating   0          12s
> scaling-demo-backend-3   0/1     ContainerCreating   0          12s
```

```bash
# Verify replica count has synced to RBG
kubectl get rbg scaling-demo -o jsonpath='{range .spec.roles[*]}{.name}{"="}{.replicas}{"\n"}{end}'

> backend=4
```

```bash
# Scale down to 1 replica
kubectl scale rbgsa scaling-demo-backend --replicas=1

> rolebasedgroupscalingadapter.workloads.x-k8s.io/scaling-demo-backend scaled
```

```bash
# Check Pod count changes
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=scaling-demo

> NAME                     READY   STATUS    RESTARTS   AGE
> scaling-demo-backend-0   1/1     Running   0          12m
```

**Expected output:**

- After scaling up, Pod count increases from 2 to 4
- RBG's `spec.roles[0].replicas` syncs to 4
- After scaling down, Pod count decreases to 1

### Cleanup

```bash
kubectl delete rbg scaling-demo
```

---

## Operation 2: HPA Metric-Driven Scaling

### Step 1: Create an RBG with ScalingAdapter Enabled (HPA)

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

# Wait for Pod to be ready
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=hpa-demo --timeout=300s
```

### Step 2: Create HPA

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

### Step 3: Verify HPA Configuration

```bash
# Check HPA status
kubectl get hpa hpa-demo-backend

> NAME               REFERENCE                                       TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
> hpa-demo-backend   RoleBasedGroupScalingAdapter/hpa-demo-backend   cpu: 0%/50%   1         10        1          30s
```

```bash
# Check HPA detailed configuration and status
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
# Confirm HPA target points to RBGSA
kubectl get hpa hpa-demo-backend -o jsonpath='{.spec.scaleTargetRef}'

> {"apiVersion":"workloads.x-k8s.io/v1alpha2","kind":"RoleBasedGroupScalingAdapter","name":"hpa-demo-backend"}
```

```bash
# Check RBGSA status (HPA controls replicas via RBGSA's Scale subresource)
kubectl get rbgsa hpa-demo-backend

> NAME               PHASE   REPLICAS   READY_REPLICAS   AGE
> hpa-demo-backend   Bound   1          1                2m43s
```

**Expected output:**

- HPA's `TARGETS` column shows current CPU utilization and target value (e.g., `0%/50%`)
- HPA's `scaleTargetRef.kind` is `RoleBasedGroupScalingAdapter`
- HPA's `scaleTargetRef.name` is `hpa-demo-backend`
- RBGSA's `status.replicas` matches the replica count managed by HPA

### Step 4: Trigger Scaling and Observe

```bash
# Generate CPU load in the Pod (trigger scale-up)
# Start 4 infinite loop processes to continuously consume CPU
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
# Continuously observe HPA status
kubectl get hpa hpa-demo-backend -w

> NAME               REFERENCE                                       TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
> hpa-demo-backend   RoleBasedGroupScalingAdapter/hpa-demo-backend   cpu: 0%/50%   1         10        1          111s
> hpa-demo-backend   RoleBasedGroupScalingAdapter/hpa-demo-backend   cpu: 199%/50%   1         10        1          2m30s
```

```bash
# Observe Pod count changes
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

### Cleanup (HPA)

```bash
kubectl delete hpa hpa-demo-backend
kubectl delete rbg hpa-demo
```

---

## Operation 3: KEDA Event-Driven Scaling

> **Note**: This operation validates the full KEDA autoscaling flow through a simulated metrics endpoint without requiring GPU. A Python HTTP server runs inside the Pod to simulate the `sglang_num_queue_requests` metric, and the metric value is controlled via an HTTP interface to trigger scale-up and scale-down.

### Step 1: Deploy Prometheus

```bash
# Deploy Prometheus, configured to auto-scrape Pods with prometheus.io/scrape: "true" annotation
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

# Wait for Prometheus to be ready
kubectl wait --for=condition=ready pod -l app=prometheus -n monitoring --timeout=60s
```

### Step 2: Create an RBG with ScalingAdapter Enabled (with Simulated Metrics Endpoint)

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

# Wait for Pod to be ready
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=keda-demo --timeout=300s
```

> **Note**: A Python HTTP server runs inside the Pod, simulating SGLang's Prometheus metrics endpoint:
>
> - `GET /metrics`: Returns `sglang_num_queue_requests{rbg="keda-demo",role="backend"} 0`
> - `GET /set?value=200`: Sets the metric value to 200 (for triggering scale-up)
> - `GET /set?value=0`: Sets the metric value to 0 (for triggering scale-down)

### Step 3: Create KEDA ScaledObject

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

### Step 4: Verify KEDA Configuration and Metric Collection

```bash
# Verify Prometheus has collected the metric (wait about 10s for Prometheus to complete first scrape)
kubectl run tmp-curl --rm -i --restart=Never --image=curlimages/curl:latest -- \
  curl -s 'http://prometheus.monitoring:9090/api/v1/query?query=sglang_num_queue_requests'

> {"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"sglang_num_queue_requests","instance":"10.xx.xx.xx:9091","job":"kubernetes-pods","rbg":"keda-demo","role":"backend"},"value":[1783493293.481,"0"]}]}}
```

```bash
# Check ScaledObject status
kubectl get scaledobject keda-demo-backend

> NAME                SCALETARGETKIND                                            SCALETARGETNAME     MIN   MAX   READY   ACTIVE   FALLBACK   PAUSED   TRIGGERS     AUTHENTICATIONS   AGE
> keda-demo-backend   workloads.x-k8s.io/v1alpha2.RoleBasedGroupScalingAdapter   keda-demo-backend   1     10    True    False    False      False    prometheus                     41s
```

```bash
# Check KEDA-managed HPA (KEDA creates an HPA in the background)
kubectl get hpa -l scaledobject.keda.sh/name=keda-demo-backend

> NAME                         REFERENCE                                       TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
> keda-hpa-keda-demo-backend   RoleBasedGroupScalingAdapter/keda-demo-backend   0/100 (avg)   1         10        1          30s
```

**Expected output:**

- Prometheus query returns `sglang_num_queue_requests` value as `0`
- ScaledObject `READY=True`, `ACTIVE=False` (metric value 0 < threshold 100)
- KEDA auto-creates HPA (named `keda-hpa-keda-demo-backend`) pointing to RBGSA, `TARGETS` shows `0/100 (avg)`

### Step 5: Trigger Scale-Up

```bash
# Set queue depth to 200 (exceeds threshold 100), simulating inference request backlog
kubectl exec $(kubectl get pods -l rbg.workloads.x-k8s.io/group-name=keda-demo -o jsonpath='{.items[0].metadata.name}') -- \
  python3 -c "import urllib.request; urllib.request.urlopen('http://localhost:9091/set?value=200')"

# Wait for HPA polling (default 15-30s), observe HPA status changes
kubectl get hpa -l scaledobject.keda.sh/name=keda-demo-backend -w

> NAME                         REFERENCE                                        TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
> keda-hpa-keda-demo-backend   RoleBasedGroupScalingAdapter/keda-demo-backend   0/100 (avg)   1         10        1          88s
> keda-hpa-keda-demo-backend   RoleBasedGroupScalingAdapter/keda-demo-backend   200/100 (avg)   1         10        1          90s
```

```bash
# Observe Pod count changes
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=keda-demo -w

> NAME                  READY   STATUS              RESTARTS   AGE
> keda-demo-backend-0   1/1     Running             0          2m32s
> keda-demo-backend-1   0/1     ContainerCreating   0          26s
> keda-demo-backend-1   1/1     Running             0          29s
```

> **Note**: KEDA queries Prometheus for `avg(sglang_num_queue_requests) = 200 > threshold = 100`, calculates desired replicas = `ceil(1 × 200/100) = 2`, and scales up to 2 replicas via the RBGSA Scale subresource. After scaling up, HPA shows `100/100 (avg)`: KEDA's returned Prometheus query result is the average of 2 Pods `(200+0)/2 = 100`, equal to the threshold 100, so HPA does not continue scaling up. The system stabilizes at 2 replicas.

### Step 6: Trigger Scale-Down

```bash
# Set queue depth to 0, simulating request clearance
kubectl exec $(kubectl get pods -l rbg.workloads.x-k8s.io/group-name=keda-demo -o jsonpath='{.items[0].metadata.name}') -- \
  python3 -c "import urllib.request; urllib.request.urlopen('http://localhost:9091/set?value=0')"

# Wait for HPA scale-down (about 1-3 minutes), observe Pod changes
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=keda-demo -w

> NAME                  READY   STATUS        RESTARTS   AGE
> keda-demo-backend-0   1/1     Running       0          9m
> keda-demo-backend-1   1/1     Terminating   0          5m58s
```

```bash
# Verify HPA has scaled down to 1 replica
kubectl get hpa -l scaledobject.keda.sh/name=keda-demo-backend

> NAME                         REFERENCE                              TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
> keda-hpa-keda-demo-backend   RoleBased.../keda-demo-backend         0/100 (avg)   1         10        1          7m42s
```

> **Note**: After the metric drops to 0, HPA detects the metric below threshold within 1-3 minutes and completes the scale-down. KEDA does not set a custom scaleDown behavior by default — the scale-down speed depends on the HPA controller's default downscale stabilization window (usually within 5 minutes).

### Cleanup (KEDA)

```bash
kubectl delete scaledobject keda-demo-backend
kubectl delete rbg keda-demo
# (Optional) Delete Prometheus
# kubectl delete namespace monitoring
```

---

## Operation 4: RBG Planner SLA-Driven Scaling (PD Disaggregation)

> **Note**: This operation requires GPU nodes and a real inference service, validating RBG Planner's PD-disaggregated intelligent scaling capability. If you only need to validate the AutoScaler CR configuration, set `dryRun: true`.

### Step 1: Create PD-Disaggregated Inference Service (with ScalingAdapter Enabled)

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

# Wait for all Pods to be ready
kubectl wait --for=condition=ready pod -l rbg.workloads.x-k8s.io/group-name=pd-autoscaler-demo -n inference --timeout=600s
```

### Step 2: Create AutoScaler CR

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: inference-extension.rolebasedgroup.io/v1alpha1
kind: AutoScaler
metadata:
  name: pd-autoscaler-demo        # Must match the RBG name
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

      # SLA targets (milliseconds)
      ttft: 200.0
      itl: 20.0

      # Load prediction configuration
      loadPredictor: arima
      predictionWindow: 50

      # Correction factor
      noCorrection: false

      # Dry-run mode (observe only, no scaling executed)
      dryRun: true

      profiling:
        image: "ghcr.io/sgl-project/rbg-profiler:latest"

      metricsEndpoint:
        metricSource: sglang
        port: 9091
EOF
```

### Step 3: Verify AutoScaler Running Status

```bash
# Check AutoScaler status
kubectl get autoscaler -n inference

# Check AutoScaler details
kubectl get autoscaler pd-autoscaler-demo -n inference -o yaml

# Check Planner Pod
kubectl get pods -n inference -l app=rbg-planner

# Check Planner logs (observe predictions and computed results)
kubectl logs -n inference -l app=rbg-planner --tail=50

# Check current replica count decision (dryRun mode does not actually scale)
kubectl get autoscaler pd-autoscaler-demo -n inference -o jsonpath='{.status}'

# Check RBG role actual replica counts (should remain unchanged in dryRun mode)
kubectl get rbg pd-autoscaler-demo -n inference -o jsonpath='{range .spec.roles[*]}{.name}{"="}{.replicas}{"\n"}{end}'

# Check RBGSA status (one for each role)
kubectl get rbgsa -n inference
```

**Expected output:**

- AutoScaler status is `Ready` or `Profiling` (first run requires Profiling)
- Planner Pod running normally
- Logs show observed TTFT, ITL metrics and predicted replica counts
- In dryRun mode, RBG replica counts remain unchanged (prefill=2, decode=4)
- Two RBGSAs created: `pd-autoscaler-demo-prefill` and `pd-autoscaler-demo-decode`

### Step 4: Disable dryRun to Enable Actual Scaling

```bash
# After confirming dryRun observations are reasonable, disable dryRun
kubectl patch autoscaler pd-autoscaler-demo -n inference --type='json' \
  -p='[{"op": "replace", "path": "/spec/implementation/DynamoPlanner/dryRun", "value": false}]'

# Continuously observe replica count changes
watch -n 5 'kubectl get rbg pd-autoscaler-demo -n inference -o jsonpath="{range .spec.roles[*]}{.name}={.replicas}\n{end}"'

# Observe Pod changes
kubectl get pods -n inference -l rbg.workloads.x-k8s.io/group-name=pd-autoscaler-demo -w
```

> **Note**: After disabling dryRun, the Planner automatically adjusts Prefill and Decode replica counts based on SLA targets and load predictions. Observe for a while — if the scaling behavior meets expectations, the configuration is complete. For tuning, refer to the "Recommended Tuning Process" in the concept document.

### Cleanup (RBG Planner)

```bash
kubectl delete autoscaler pd-autoscaler-demo -n inference
kubectl delete rbg pd-autoscaler-demo -n inference
```

---

## Summary

| Operation | Scaling Method | Verification Point | Key Expectation |
| --- | --- | --- | --- |
| ScalingAdapter basics | Manual `kubectl scale` | RBGSA auto-creation, Scale subresource available | After `kubectl scale rbgsa`, replica count syncs to RBG |
| HPA metric-driven | Reactive (CPU utilization) | HPA target points to RBGSA | HPA controls replicas via RBGSA Scale subresource |
| KEDA event-driven | Reactive (external metrics) | ScaledObject target points to RBGSA | KEDA creates HPA in background pointing to RBGSA |
| RBG Planner SLA-driven | Predictive (TTFT/ITL) | AutoScaler CR creation, dryRun observation | Planner computes optimal replica counts for Prefill and Decode as a whole |

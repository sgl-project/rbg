# 前置检查详情

## RBG 安装

### 方式一：Helm（推荐）

```bash
# 安装最新发布版
VERSION=$(curl -sL https://api.github.com/repos/sgl-project/rbg/releases/latest \
  | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')

helm upgrade --install rbgs \
  https://github.com/sgl-project/rbg/releases/download/v${VERSION}/rbgs-${VERSION}.tgz \
  --namespace rbgs-system \
  --create-namespace \
  --wait

# 验证安装
kubectl wait deploy/rbgs-controller-manager -n rbgs-system \
  --for=condition=available --timeout=5m
```

### 方式二：本地源码（开发环境）

```bash
# 若在 rbg 项目目录下
helm upgrade --install rbgs deploy/helm/rbgs \
  --create-namespace \
  --namespace rbgs-system \
  --wait
```

### 验证安装成功

```bash
kubectl get crd rolebasedgroups.workloads.x-k8s.io
kubectl get deploy rbgs-controller-manager -n rbgs-system
# 期望输出：AVAILABLE=1
```

---

## llmctl 安装

### 方式一：从源码构建（推荐）

```bash
git clone https://github.com/rolebasedgroup/inference-ext-cli.git
cd inference-ext-cli

# 构建当前平台的 CLI binary
make build-cli

# 安装到系统 PATH
make install       # 安装到 $GOPATH/bin
# 或
chmod +x llmctl
sudo mv llmctl /usr/local/bin/
```

> **前提**：需要 Go 1.26+，可通过 `go version` 检查

### 验证安装成功

```bash
llmctl --help
# 期望输出显示 service/model/benchmark 等子命令
```

### 初始化配置

安装后需要配置存储和数据源才能使用模型下载功能：

```bash
# 交互式向导（推荐）
llmctl config init

# 或手动配置
# 配置 HuggingFace 数据源
llmctl config add-source huggingface --type huggingface --config token=hf_xxx

# 配置 ModelScope 数据源
llmctl config add-source modelscope --type modelscope --config token=<token>

# 配置 PVC 存储（需要集群中已存在的 PVC）
llmctl config add-storage my-models --type pvc --config pvcName=model-pvc

# 查看配置
llmctl config view
```

---

## 模型下载

> 需要 `LLMCTL_AVAILABLE=true` 且集群可用

### 下载模型到 PVC

```bash
# 交互式（先执行 config init 完成存储/数据源配置）
llmctl model pull Qwen/Qwen3-8B

# 指定来源和存储
llmctl model pull Qwen/Qwen3-8B \
  --source huggingface \
  --storage my-models

# 不等待完成（异步）
llmctl model pull Qwen/Qwen3-8B --wait=false
```

### 查看已下载的模型

```bash
llmctl model list
llmctl model list --storage my-models
```

### 在 RBG YAML 中使用已下载的模型

```yaml
# 容器中挂载 PVC
volumes:
  - name: model-storage
    persistentVolumeClaim:
      claimName: model-pvc
containers:
  - name: engine
    volumeMounts:
      - name: model-storage
        mountPath: /models
    command:
      - python3
      - -m
      - sglang.launch_server
      - --model-path
      - /models/Qwen/Qwen3-8B   # PVC 挂载路径
```

---

## 节点 GPU 检查命令参考

```bash
# 列出所有 GPU 节点
kubectl get nodes -l nvidia.com/gpu -o wide

# 按 label 过滤节点
kubectl get nodes -l <node-selector-key>=<value> \
  -o custom-columns=NAME:.metadata.name,GPU:.status.allocatable."nvidia\.com/gpu"

# 查看节点详细信息（包括 GPU 型号 label）
kubectl describe node <node-name> | grep -E "nvidia.com/gpu|Labels"

# 统计可用 GPU 总数
kubectl get nodes -l <selector> -o json | \
  jq '[.items[].status.allocatable["nvidia.com/gpu"] | tonumber] | add'

# 检查常见 GPU 型号 label（NVIDIA Device Plugin 注入）
# nvidia.com/gpu.product: A100-SXM4-80GB
# nvidia.com/gpu.product: H100-SXM5-80GB
# nvidia.com/gpu.product: Tesla-V100-SXM2-32GB
kubectl get nodes -o json | \
  jq '.items[] | {name:.metadata.name, gpu_product:.metadata.labels["nvidia.com/gpu.product"]}'
```

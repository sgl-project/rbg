# RBG Benchmark Command

## Overview

The `kubectl rbg llm benchmark` command runs [genai-bench](https://github.com/sgl-project/genai-bench) benchmarks against a RoleBasedGroup deployment in Kubernetes.

## Prerequisites

1. **Install kubectl-rbg plugin** (refer to [kubectl-rbg](kubectl-rbg.md)):

   ```bash
   git clone https://github.com/sgl-project/rbg.git
   make build-cli
   chmod +x bin/kubectl-rbg
   sudo mv bin/kubectl-rbg /usr/local/bin/
   ```

2. **Prepare a PVC for storing benchmark results**

    ```yaml
    apiVersion: v1
    kind: PersistentVolume
    metadata:
    name: benchmark-output
    labels:
        alicloud-pvname: benchmark-output
    spec:
    capacity:
        storage: 5Gi
    accessModes:
        - ReadWriteMany
    persistentVolumeReclaimPolicy: Retain
    csi:
        driver: ${driver}
        volumeHandle: benchmark-output
        nodePublishSecretRef:
        name: ${name}
        namespace: ${namespace}
        volumeAttributes:
        bucket: ${bucket}
        url: ${url}
        path: ${path}
    ---
    apiVersion: v1
    kind: PersistentVolumeClaim
    metadata:
    name: benchmark-output
    spec:
    accessModes:
        - ReadWriteMany
    resources:
        requests:
        storage: 5Gi
    selector:
        matchLabels:
        alicloud-pvname: ${pvname}
    ```

3. **Ensure the target RoleBasedGroup is deployed and ready**

refer to [kubectl-rbg-llm-generate](kubectl-rbg-llm-generate.md)

## Usage

### Run Benchmark

```bash
kubectl rbg llm benchmark run <rbg-name> \
  --model-tokenizer Qwen/Qwen3-8B \
  --experiment-base-dir pvc://benchmark-output/rbg-benchmark \
  --traffic-scenario "D(100,1000)" \
  --num-concurrency 1,2,4
```

By default, the benchmark runs asynchronously and returns immediately. Use `--wait` to synchronously wait for completion and stream logs:

```bash
kubectl rbg llm benchmark run <rbg-name> \
  --model-tokenizer Qwen/Qwen3-8B \
  --experiment-base-dir pvc://benchmark-output/rbg-benchmark \
  --wait
```

### List Benchmark Jobs

```bash
# Show all benchmark jobs for a RBG (sorted by creation time, oldest first)
kubectl rbg llm benchmark list <rbg-name>
```

### View Benchmark Logs

```bash
# Stream logs from a benchmark job (follows in real time by default)
kubectl rbg llm benchmark logs <rbg-name> --job <job-name>

# View logs without following
kubectl rbg llm benchmark logs <rbg-name> --job <job-name> -f=false
```

### Get Benchmark Config

```bash
# Display the benchmark configuration for a specific job
kubectl rbg llm benchmark get <rbg-name> --job <job-name>
```

### Delete Benchmark Job

```bash
# Delete a specific benchmark job
kubectl rbg llm benchmark delete <rbg-name> --job <job-name>
```

### Browse Results via Web Dashboard

```bash
kubectl rbg llm benchmark dashboard \
  --experiment-base-dir pvc://benchmark-output/rbg-benchmark
```

## Command Flags

### run `<rbg-name>`

#### Required Flags

| Flag | Description |
|------|-------------|
| `--model-tokenizer` | Tokenizer to use. Can be a HuggingFace model name (e.g., `Qwen/Qwen3-8B`) or a PVC path (e.g., `pvc://model-pvc/tokenizer`) |
| `--experiment-base-dir` | PVC path for storing results (e.g., `pvc://benchmark-output/rbg-benchmark`) |

#### Optional Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--task` | `text-to-text` | Benchmark task type |
| `--max-time-per-run` | `15` | Maximum time per run in minute |
| `--max-requests-per-run` | `100` | Maximum requests per run |
| `--traffic-scenario` | - | Traffic scenario (e.g., `D(100,1000)`), can be specified multiple times |
| `--num-concurrency` | - | List of concurrency levels to run the experiment with |
| `--api-backend` | `sglang` | API backend type |
| `--api-base` | auto | Base URL for model API (auto-discovered from RBG) |
| `--api-key` | `rbg` | API key for model serving |
| `--api-model-name` | auto | Model name (defaults to RBG name) |
| `--experiment-folder-name` | auto | Folder name for results (defaults to job name) |
| `--image` | `rolebasedgroup/rbgs-benchtool-genai:latest` | Container image for benchmark job |
| `--cpu-request` | `1` | CPU request |
| `--cpu-limit` | `2` | CPU limit |
| `--memory-request` | `2Gi` | Memory request |
| `--memory-limit` | `2Gi` | Memory limit |
| `--wait` | `false` | Wait for benchmark to complete and stream logs |
| `--extra-args` | - | Extra args in JSON format (e.g., `'{"key":"value"}'`) |

### list `<rbg-name>`

No additional flags. Lists all benchmark jobs sorted by creation time (oldest first).

### get `<rbg-name>`

| Flag | Description |
|------|-------------|
| `--job` | Name of the benchmark job to inspect (required) |

### delete `<rbg-name>`

| Flag | Description |
|------|-------------|
| `--job` | Name of the benchmark job to delete (required) |

### logs `<rbg-name>`

| Flag | Default | Description |
|------|---------|-------------|
| `--job` | - | Name of the benchmark job to view logs for (required) |
| `-f, --follow` | `true` | Follow log output in real time |

### dashboard

| Flag | Default | Description |
|------|---------|-------------|
| `--experiment-base-dir` | - | PVC path containing experiment results (required) |
| `--port` | `18888` | Local port for port-forward |
| `--open-browser` | `true` | Automatically open browser |
| `--image` | `rolebasedgroup/rbgs-benchmark-dashboard:nightly` | Container image for benchmark server |

## PVC Path Format

Supported formats:
- `pvc://{pvc-name}/`
- `pvc://{pvc-name}/{sub-path}`

The PVC namespace is determined by the current kubeconfig context.

## Example

```bash
# 1. Create a PVC for benchmark output
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: benchmark-output
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 10Gi
  selector:
    matchLabels:
    alicloud-pvname: ${pvname}
EOF

# 2. Run benchmark against RBG "qwen3-8b"
kubectl rbg llm benchmark run qwen3-8b \
  --model-tokenizer Qwen/Qwen3-8B \
  --experiment-base-dir pvc://benchmark-output/rbg-benchmark \
  --traffic-scenario "D(100,1000)" \
  --traffic-scenario "D(500,500)" \
  --num-concurrency 1,2,4,8 \
  --image ${image}

# 2.1 Alternatively, use a YAML config file to provide parameters:
# Note: --config/-f is mutually exclusive with other parameter flags.
cat <<EOF > benchmark-config.yaml
modelTokenizer: Qwen/Qwen3-8B
experimentBaseDir: pvc://benchmark-output/rbg-benchmark
trafficScenarios:
  - "D(100,1000)"
  - "D(500,500)"
numConcurrency:
  - 1
  - 2
  - 4
  - 8
image: ${image}
EOF

kubectl rbg llm benchmark run qwen3-8b -f benchmark-config.yaml

# 3. List benchmark jobs
kubectl rbg llm benchmark list qwen3-8b

# 4. View logs of a benchmark job
kubectl rbg llm benchmark logs qwen3-8b --job <job-name>

# 5. Get benchmark config of a job
kubectl rbg llm benchmark get qwen3-8b --job <job-name>

# 6. Delete a benchmark job
kubectl rbg llm benchmark delete qwen3-8b --job <job-name>

# 7. Browse results
kubectl rbg llm benchmark dashboard \
  --experiment-base-dir pvc://benchmark-output/rbg-benchmark \
  --image ${image}
```

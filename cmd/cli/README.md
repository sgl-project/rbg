# kubectl-rbg CLI

A kubectl plugin for managing Role-Based Groups (RBG) on Kubernetes, with a focus on LLM (Large Language Model) inference services.

## Overview

This CLI tool provides a comprehensive set of commands for deploying, managing, and interacting with LLM inference services on Kubernetes. It simplifies the process of running models, managing configurations, and benchmarking performance.

## Getting Started

### 1. Initialize Configuration

First, set up your configuration:

```bash
kubectl rbg llm config init
```

This will guide you through configuring:
- **Storage**: Where models are stored (e.g., OSS)
- **Source**: Where models are downloaded from (e.g., HuggingFace)

### 2. Pull an LLM model

Download a model to storage:

```bash
kubectl rbg llm model pull Qwen/Qwen3.5-0.8B
```

### 3. Run an LLM Service

Deploy a model to your Kubernetes cluster:

```bash
# Deploy a model with a pre-built model config
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B

# Or deploy a custom model without a pre-built model config
kubectl rbg llm svc run my-model org/new-model --engine vllm --resource nvidia.com/gpu=1
```

### 4. Chat with the Model

Once the service is running, you can chat with it:

```bash
# Interactive mode
kubectl rbg llm svc chat my-qwen -i

# Or send a single prompt
kubectl rbg llm svc chat my-qwen --prompt "Hello, how are you?"
```

### 5. Generate Optimized Configurations

For production deployments, use the AI Configurator to generate optimized configurations:

```bash
kubectl rbg llm generate --model Qwen/Qwen3.5-9B --system h200_sxm --total-gpus 8 \
  --isl 4000 --osl 1000 --ttft 1000 --tpot 10
```

Then deploy using the generated YAML:

```bash
kubectl apply -f /tmp/rbg-llm-generate-output/qwen3-5-9b-sglang-disagg.yaml
```

## Commands

### llm svc run

Run an LLM inference service on Kubernetes.

```bash
kubectl rbg llm svc run NAME MODEL_ID [flags]
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `NAME` | Name for the inference service (required) |
| `MODEL_ID` | Model identifier (required), e.g., `Qwen/Qwen3.5-0.8B` |

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--replicas` | | Number of replicas | 1 |
| `--mode` | | Run mode (from model config) | (first mode in model config) |
| `--engine` | | Inference engine override (`vllm`, `sglang`). Required when no model config exists for MODEL_ID. | (from mode config) |
| `--image` | | Container image override | (from mode config) |
| `--resource` | | Resource requirements (`key=value`, e.g. `nvidia.com/gpu=1`), can be specified multiple times. Flag values override config on conflict. | |
| `--distributed-size` | | Multi-node deployment size (<=1 means standalone) | 0 |
| `--shm-size` | | Shared memory size (e.g. `8Gi`, `16Gi`) | |
| `--env` | | Environment variables (`KEY=VALUE`), can be specified multiple times | |
| `--arg` | | Additional arguments for the engine, can be specified multiple times | |
| `--storage` | | Storage to use (overrides default) | (current storage) |
| `--revision` | | Model revision | `main` |
| `--model-path` | | Absolute model path inside the container. Storage is mounted at `/models`, so the default path is `/models/<model>/<revision>` | |
| `--dry-run` | | Print the generated template without creating the workload | `false` |
| `--wait` | | Wait for the RoleBasedGroup to be ready before returning | `true` |
| `--wait-timeout` | | Timeout for waiting for RoleBasedGroup to be ready | `20m` |
| `--test-api` | | Test the `/v1/chat/completions` API endpoint after the service is ready | `true` |
| `--test-api-timeout` | | Timeout for testing the API endpoint | `5m` |
| `--local-port` | | Local port for port-forward when testing API | `32432` |

**Examples:**

```bash
# Run a model with default settings
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B

# Use a specific mode
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --mode throughput

# Override engine
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --mode custom --engine sglang

# Use a custom image (e.g., mirror registry)
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --image registry.cn-hangzhou.aliyuncs.com/my/vllm:latest

# Run with multiple replicas
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --replicas 3

# Deploy a custom model without a pre-built model config
kubectl rbg llm svc run my-model org/new-model --engine vllm --resource nvidia.com/gpu=1

# Deploy with custom resources, distributed size, and shared memory
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --resource nvidia.com/gpu=2 --resource memory=16Gi --distributed-size 4 --shm-size 16Gi

# Dry run to preview the generated configuration
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --dry-run

# Pass additional environment variables
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --env CUDA_VISIBLE_DEVICES=0,1 --env LOG_LEVEL=debug

# Pass additional engine arguments
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --arg --max-model-len=4096 --arg --dtype=half

# Specify a custom model path (when models are placed manually in storage)
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --model-path /models/my-custom-model

# Skip API readiness test
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --test-api=false
```

### llm svc list

List all running LLM inference services.

```bash
kubectl rbg llm svc list [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--all-namespaces` | `-A` | List across all namespaces | false |

**Example:**

```bash
# List services in current namespace
kubectl rbg llm svc list

# List services in all namespaces
kubectl rbg llm svc list -A
```

### llm svc delete

Delete an LLM inference service.

```bash
kubectl rbg llm svc delete NAME... [flags]
```

**Example:**

```bash
kubectl rbg llm svc delete my-qwen
```

### llm model list

List downloaded models in storage.

```bash
kubectl rbg llm model list [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--storage` | | Storage to use (overrides default) | (current storage) |

**Example:**

```bash
# List models in the default storage
kubectl rbg llm model list

# List models in a specific storage
kubectl rbg llm model list --storage my-pvc
```

### llm model pull

Pull a model from the configured source to storage.

```bash
kubectl rbg llm model pull MODEL_ID [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--source` | | Source configuration name | (current source) |
| `--storage` | | Storage configuration name | (current storage) |
| `--revision` | | Model revision to download | `main` |
| `--wait` | | Wait for the pull job to complete and stream logs | `true` |

**Example:**

```bash
# Pull a model with default settings
kubectl rbg llm model pull Qwen/Qwen2.5-7B

# Pull a specific revision
kubectl rbg llm model pull Qwen/Qwen2.5-7B --revision v1.0

# Pull without waiting for completion
kubectl rbg llm model pull Qwen/Qwen2.5-7B --wait=false
```

### llm svc chat

Chat with a running LLM inference service.

```bash
kubectl rbg llm svc chat NAME [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--prompt` | `-p` | Single prompt (non-interactive); combined with `-i` to seed the first turn | |
| `--interactive` | `-i` | Start an interactive chat session (REPL) | false |
| `--local-port` | | Local port for the tunnel | 0 (random) |
| `--system` | | System prompt prepended to conversation | |
| `--no-stream` | | Disable streaming | false |
| `--request-timeout` | | HTTP request timeout for model inference | 5m |
| `--port-forward-timeout` | | Port-forward tunnel timeout | 30s |

**Examples:**

```bash
# Interactive chat session
kubectl rbg llm svc chat my-qwen -i

# Single prompt (non-interactive)
kubectl rbg llm svc chat my-qwen --prompt "What is the capital of France?"

# With system prompt (interactive)
kubectl rbg llm svc chat my-qwen -i --system "You are a helpful assistant."
```

### llm generate

Generate optimized RBG deployment configurations using AI Configurator.

```bash
kubectl rbg llm generate [flags]
```

**Required Flags:**

| Flag | Description |
|------|-------------|
| `--model` | Model name |
| `--system` | GPU system type (h100_sxm, a100_sxm, b200_sxm, gb200_sxm, l40s, h200_sxm) |
| `--total-gpus` | Total number of GPUs for deployment |
| `--isl` | Input sequence length |
| `--osl` | Output sequence length |

**Optional Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--configurator-tool` | Configurator tool to use | `aiconfigurator` |
| `--ttft` | Time to first token (ms) | -1 |
| `--tpot` | Time per output token (ms) | -1 |
| `--request-latency` | End-to-end request latency target (ms) | -1 |
| `--decode-system` | GPU system for decode workers | (same as --system) |
| `--backend` | Inference backend (sglang, vllm, trtllm) | sglang |
| `--backend-version` | Backend version | |
| `--prefix` | Prefix cache length | 0 |
| `--database-mode` | Database mode (SILICON, HYBRID, EMPIRICAL, SOL) | SILICON |
| `--save-dir` | Directory to save results | /tmp/rbg-llm-generate-output |

**Examples:**

```bash
# Generate configuration with TTFT and TPOT targets
kubectl rbg llm generate --model Qwen/Qwen3.5-9B --system h200_sxm --total-gpus 8 \
  --isl 4000 --osl 1000 --ttft 1000 --tpot 10

# Generate with request latency target
kubectl rbg llm generate --model meta-llama/Llama-3-70B --system h100_sxm --total-gpus 16 \
  --isl 8192 --osl 2048 --request-latency 5000
```

### llm benchmark

Run benchmarks against LLM inference services.

```bash
kubectl rbg llm benchmark [command] [flags]
```

#### benchmark run

Run a benchmark test.

```bash
kubectl rbg llm benchmark run RBG_NAME [flags]
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `RBG_NAME` | Name of the RoleBasedGroup to benchmark (required) |

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--config` | `-f` | Path to a YAML config file (mutually exclusive with other parameter flags) | |
| `--task` | | Benchmark task type | `text-to-text` |
| `--max-time-per-run` | | Maximum time per benchmark run in minutes | `15` |
| `--max-requests-per-run` | | Maximum number of requests per benchmark run | `100` |
| `--traffic-scenario` | | Traffic scenario definitions (e.g., `D(100,1000)`), can be specified multiple times | |
| `--num-concurrency` | | Concurrency levels, can be specified multiple times or comma-separated | |
| `--api-backend` | | API backend type (overrides auto-discovered value) | |
| `--api-base` | | Base URL for the model serving API (overrides auto-discovered value) | |
| `--api-port` | | Port for the model serving API (used when auto-generating api-base) | `8000` |
| `--api-key` | | API key used to call the model serving API | `rbg` |
| `--api-model-name` | | Model name used by the backend (overrides auto-discovered value) | |
| `--model-tokenizer` | | Tokenizer to use (HuggingFace model name or PVC path) | (required) |
| `--experiment-base-dir` | | Base directory for storing experiment results (PVC path) | (required) |
| `--experiment-folder-name` | | Name of the folder to save experiment results | (job name) |
| `--image` | | Container image used for benchmark job | `rolebasedgroup/rbgs-benchmark-tool-genai:v0.6.0` |
| `--cpu-request` | | CPU request for benchmark pod | `1` |
| `--cpu-limit` | | CPU limit for benchmark pod | `2` |
| `--memory-request` | | Memory request for benchmark pod | `2Gi` |
| `--memory-limit` | | Memory limit for benchmark pod | `2Gi` |
| `--wait` | | Wait for benchmark to complete and stream logs | `false` |
| `--extra-args` | | Extra arguments to pass to genai-bench in JSON format | |

**Examples:**

```bash
# Run benchmark with required flags
kubectl rbg llm benchmark run my-qwen \
  --model-tokenizer Qwen/Qwen3-8B \
  --experiment-base-dir pvc://my-pvc/results

# Run with custom traffic scenarios and concurrency levels
kubectl rbg llm benchmark run my-qwen \
  --model-tokenizer Qwen/Qwen3-8B \
  --experiment-base-dir pvc://my-pvc/results \
  --traffic-scenario "D(100,1000)" \
  --traffic-scenario "D(200,2000)" \
  --num-concurrency 1,2,4,8

# Run with config file
kubectl rbg llm benchmark run my-qwen --config benchmark-config.yaml

# Run and wait for completion
kubectl rbg llm benchmark run my-qwen \
  --model-tokenizer Qwen/Qwen3-8B \
  --experiment-base-dir pvc://my-pvc/results \
  --wait
```

#### benchmark get

Get benchmark configuration for a benchmark job.

```bash
kubectl rbg llm benchmark get RBG_NAME --job JOB_NAME [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--job` | | Name of the benchmark job to inspect (required) | |

#### benchmark list

List benchmark jobs for a RoleBasedGroup.

```bash
kubectl rbg llm benchmark list RBG_NAME
```

#### benchmark delete

Delete a benchmark job.

```bash
kubectl rbg llm benchmark delete RBG_NAME --job JOB_NAME [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--job` | | Name of the benchmark job to delete (required) | |

#### benchmark logs

View benchmark job logs.

```bash
kubectl rbg llm benchmark logs RBG_NAME [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--job` | | Name of the benchmark job to view logs for (required) | |
| `--follow` | `-f` | Follow log output in real time | `true` |

#### benchmark dashboard

Start a web dashboard to browse benchmark results.

```bash
kubectl rbg llm benchmark dashboard [flags]
```

**Required Flags:**

| Flag | Description |
|------|-------------|
| `--experiment-base-dir` | PVC path containing experiment results (e.g. `pvc://benchmark-output/rbg-benchmark`) |

**Optional Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--port` | | Local port for port-forward | `18888` |
| `--open-browser` | | Automatically open browser after dashboard is ready | `true` |
| `--image` | | Container image for the benchmark dashboard | `rolebasedgroup/rbgs-benchmark-dashboard:v0.6.0` |

### llm config

Manage LLM configuration including storage, sources, and engines.

#### config init

Initialize LLM configuration interactively.

```bash
kubectl rbg llm config init
```

This command guides you through setting up storage and source configurations.

#### config view

View current configuration.

```bash
kubectl rbg llm config view
```

#### Storage Management

##### config add-storage

Add a storage configuration.

```bash
kubectl rbg llm config add-storage NAME [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--type` | | Storage type (pvc, oss) | pvc |
| `--config` | | Configuration key=value pairs | |
| `--interactive` | `-i` | Interactive configuration mode | false |

**Examples:**

```bash
# Add PVC storage
kubectl rbg llm config add-storage my-pvc --type pvc --config pvcName=model-pvc

# Add OSS storage
kubectl rbg llm config add-storage my-oss --type oss \
  --config url=oss-cn-hangzhou.aliyuncs.com --config bucket=my-bucket

# Interactive mode
kubectl rbg llm config add-storage my-pvc -i
```

##### config get-storages

List all storage configurations.

```bash
kubectl rbg llm config get-storages
```

##### config use-storage

Set the current active storage.

```bash
kubectl rbg llm config use-storage NAME
```

##### config set-storage

Update a storage configuration.

```bash
kubectl rbg llm config set-storage NAME --config key=value
```

##### config delete-storage

Delete a storage configuration.

```bash
kubectl rbg llm config delete-storage NAME
```

#### Source Management

##### config add-source

Add a model source configuration.

```bash
kubectl rbg llm config add-source NAME [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--type` | | Source type (huggingface, modelscope) | huggingface |
| `--config` | | Configuration key=value pairs | |
| `--interactive` | `-i` | Interactive configuration mode | false |

**Examples:**

```bash
# Add HuggingFace source
kubectl rbg llm config add-source huggingface --type huggingface --config token=hf_xxx

# Interactive mode
kubectl rbg llm config add-source huggingface -i
```

##### config get-sources

List all source configurations.

```bash
kubectl rbg llm config get-sources
```

##### config use-source

Set the current active source.

```bash
kubectl rbg llm config use-source NAME
```

##### config set-source

Update a source configuration.

```bash
kubectl rbg llm config set-source NAME --config key=value
```

##### config delete-source

Delete a source configuration.

```bash
kubectl rbg llm config delete-source NAME
```

#### Engine Management

##### config get-engines

List customized engine configurations.

```bash
kubectl rbg llm config get-engines
```

##### config set-engine

Customize engine configuration (optional).

```bash
kubectl rbg llm config set-engine ENGINE_TYPE --config key=value
```

Supported engines: `sglang`, `vllm`

**Example:**

```bash
kubectl rbg llm config set-engine sglang --config defaultPort=8000
```

##### config reset-engine

Remove custom engine configuration, reverting to defaults.

```bash
kubectl rbg llm config reset-engine ENGINE_TYPE
```

## Configuration File

The configuration is stored at `~/.rbg/config.yaml`:

```yaml
apiVersion: rbg/v1alpha2
kind: Config
currentStorage: my-pvc
currentSource: huggingface
storages:
  - name: my-pvc
    type: pvc
    config:
      pvcName: model-pvc
sources:
  - name: huggingface
    type: huggingface
    config:
      token: hf_xxx
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `KUBECONFIG` | Path to kubeconfig file |
| `RBG_CONFIG` | Path to rbg configuration directory |

## Examples

### Complete Workflow Example

```bash
# 1. Initialize configuration
kubectl rbg llm config init

# 2. Verify configuration
kubectl rbg llm config view

# 3. Pull a model
kubectl rbg llm model pull Qwen/Qwen2.5-7B

# 4. Run the model
kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --replicas 2

# 5. Check service status
kubectl rbg llm svc list

# 6. Chat with the model
kubectl rbg llm svc chat my-qwen --prompt "Explain Kubernetes in simple terms"

# 7. Run benchmarks
kubectl rbg llm benchmark run my-qwen \
  --model-tokenizer Qwen/Qwen3-8B \
  --experiment-base-dir pvc://my-pvc/results \
  --wait

# 8. Clean up
kubectl rbg llm svc delete my-qwen
```

### Multi-Model Deployment Example

```bash
# Deploy multiple models with different configurations
kubectl rbg llm svc run qwen-small Qwen/Qwen3.5-0.8B --replicas 1
kubectl rbg llm svc run qwen-large Qwen/Qwen3-32B --mode throughput --replicas 2
kubectl rbg llm svc run llama-model meta-llama/Llama-3-8B --engine sglang

# Deploy a custom model with no pre-built model config
kubectl rbg llm svc run my-custom org/my-model --engine vllm --resource nvidia.com/gpu=4 --shm-size 32Gi

# List all services
kubectl rbg llm svc list
```

## Troubleshooting

### Port-forward issues

If you encounter port-forward timeout errors, increase the timeout:

```bash
kubectl rbg llm svc chat my-model --port-forward-timeout 60s
```

### Model not found

Ensure the model is pulled to storage:

```bash
kubectl rbg llm model pull MODEL_ID
```

### GPU allocation issues

Check available GPUs in your cluster:

```bash
kubectl describe nodes | grep -A 5 "Allocated resources"
```

## License

Apache License 2.0

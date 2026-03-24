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
kubectl rbg llm run pull Qwen/Qwen3.5-0.8B
```

### 3. Run an LLM Service

Deploy a model to your Kubernetes cluster:

```bash
kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B
```

### 4. Chat with the Model

Once the service is running, you can chat with it:

```bash
# Interactive mode
kubectl rbg llm chat my-qwen

# Or send a single prompt
kubectl rbg llm chat my-qwen --prompt "Hello, how are you?"
```

### 5. Generate Optimized Configurations

For production deployments, use the AI Configurator to generate optimized configurations:

```bash
kubectl rbg llm generate --model QWEN3_32B --system h200_sxm --total-gpus 8 \
  --isl 4000 --osl 1000 --ttft 1000 --tpot 10
```

Then deploy using the generated YAML:

```bash
kubectl apply -f /tmp/rbg-llm-generate-output/qwen3-32b-sglang-disagg.yaml
```

## Commands

### llm run

Run an LLM inference service on Kubernetes.

```bash
kubectl rbg llm run NAME MODEL_ID [flags]
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
| `--engine` | | Inference engine override (`vllm`, `sglang`) | (from mode config) |
| `--env` | | Environment variables (`KEY=VALUE`), can be specified multiple times | |
| `--arg` | | Additional arguments for the engine, can be specified multiple times | |
| `--storage` | | Storage to use (overrides default) | (current storage) |
| `--revision` | | Model revision | `main` |
| `--dry-run` | | Print the generated template without creating the workload | `false` |
| `--namespace` | `-n` | Kubernetes namespace | default |

**Examples:**

```bash
# Run a model with default settings
kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B

# Use a specific mode
kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --mode throughput

# Override engine
kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --mode custom --engine sglang

# Run with multiple replicas
kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --replicas 3

# Dry run to preview the generated configuration
kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --dry-run

# Pass additional environment variables
kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --env CUDA_VISIBLE_DEVICES=0,1 --env LOG_LEVEL=debug

# Pass additional engine arguments
kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --arg --max-model-len=4096 --arg --dtype=half
```

### llm list

List all running LLM inference services.

```bash
kubectl rbg llm list [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--namespace` | `-n` | Kubernetes namespace | default |
| `--all-namespaces` | `-A` | List across all namespaces | false |

**Example:**

```bash
# List services in current namespace
kubectl rbg llm list

# List services in all namespaces
kubectl rbg llm list -A
```

### llm delete

Delete an LLM inference service.

```bash
kubectl rbg llm delete NAME [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--namespace` | `-n` | Kubernetes namespace | default |

**Example:**

```bash
kubectl rbg llm delete my-qwen
```

### llm models

List available models from configured sources.

```bash
kubectl rbg llm models [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--source` | | Filter by source name | |

**Example:**

```bash
# List all available models
kubectl rbg llm models

# List models from a specific source
kubectl rbg llm models --source huggingface
```

### llm pull

Pull a model from the configured source to storage.

```bash
kubectl rbg llm pull MODEL_NAME [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--source` | | Source configuration name | (current source) |
| `--storage` | | Storage configuration name | (current storage) |

**Example:**

```bash
kubectl rbg llm pull Qwen/Qwen2.5-7B
```

### llm chat

Chat with a running LLM inference service.

```bash
kubectl rbg llm chat NAME [flags]
```

**Flags:**

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--prompt` | `-p` | Single prompt (non-interactive) | |
| `--interactive` | `-i` | Start interactive chat session (REPL) | false |
| `--local-port` | | Local port for the tunnel | (random) |
| `--system` | | System prompt prepended to conversation | |
| `--no-stream` | | Disable streaming | false |
| `--request-timeout` | | HTTP request timeout | 5m |
| `--port-forward-timeout` | | Port-forward tunnel timeout | 30s |

**Examples:**

```bash
# Interactive chat session
kubectl rbg llm chat my-qwen

# Single prompt (non-interactive)
kubectl rbg llm chat my-qwen --prompt "What is the capital of France?"

# With system prompt
kubectl rbg llm chat my-qwen --system "You are a helpful assistant."
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

**Optional Flags:**

| Flag | Description | Default |
|------|-------------|---------|
| `--isl` | Input sequence length | 4000 |
| `--osl` | Output sequence length | 1000 |
| `--ttft` | Time to first token (ms) | -1 |
| `--tpot` | Time per output token (ms) | -1 |
| `--request-latency` | End-to-end request latency target (ms) | -1 |
| `--hf-id` | HuggingFace model ID | |
| `--decode-system` | GPU system for decode workers | (same as --system) |
| `--backend` | Inference backend (sglang, vllm, trtllm) | sglang |
| `--backend-version` | Backend version | |
| `--prefix` | Prefix cache length | 0 |
| `--database-mode` | Database mode (SILICON, HYBRID, EMPIRICAL, SOL) | SILICON |
| `--save-dir` | Directory to save results | /tmp/rbg-llm-generate-output |

**Examples:**

```bash
# Generate configuration with TTFT and TPOT targets
kubectl rbg llm generate --model QWEN3_32B --system h200_sxm --total-gpus 8 \
  --isl 4000 --osl 1000 --ttft 1000 --tpot 10

# Generate with request latency target
kubectl rbg llm generate --model LLAMA3_70B --system h100_sxm --total-gpus 16 \
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

Get benchmark results.

```bash
kubectl rbg llm benchmark get JOB_NAME [flags]
```

#### benchmark list

List benchmark jobs.

```bash
kubectl rbg llm benchmark list [RBG_NAME] [flags]
```

#### benchmark delete

Delete a benchmark job.

```bash
kubectl rbg llm benchmark delete JOB_NAME [flags]
```

#### benchmark logs

View benchmark job logs.

```bash
kubectl rbg llm benchmark logs JOB_NAME [flags]
```

#### benchmark dashboard

Open benchmark dashboard.

```bash
kubectl rbg llm benchmark dashboard [flags]
```

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
kubectl rbg llm config add-storage my-pvc --type pvc --config claimName=model-pvc

# Add OSS storage
kubectl rbg llm config add-storage my-oss --type oss \
  --config endpoint=oss-cn-hangzhou.aliyuncs.com --config bucket=my-bucket

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
apiVersion: rbg/v1alpha1
kind: Config
currentStorage: my-pvc
currentSource: huggingface
storages:
  - name: my-pvc
    type: pvc
    config:
      claimName: model-pvc
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
kubectl rbg llm pull Qwen/Qwen2.5-7B

# 4. Run the model
kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --replicas 2

# 5. Check service status
kubectl rbg llm list

# 6. Chat with the model
kubectl rbg llm chat my-qwen --prompt "Explain Kubernetes in simple terms"

# 7. Run benchmarks
kubectl rbg llm benchmark run my-qwen \
  --model-tokenizer Qwen/Qwen3-8B \
  --experiment-base-dir pvc://my-pvc/results \
  --wait

# 8. Clean up
kubectl rbg llm delete my-qwen
```

### Multi-Model Deployment Example

```bash
# Deploy multiple models with different configurations
kubectl rbg llm run qwen-small Qwen/Qwen3.5-0.8B --replicas 1
kubectl rbg llm run qwen-large Qwen/Qwen3-32B --mode throughput --replicas 2
kubectl rbg llm run llama-model meta-llama/Llama-3-8B --engine sglang

# List all services
kubectl rbg llm list
```

## Troubleshooting

### Port-forward issues

If you encounter port-forward timeout errors, increase the timeout:

```bash
kubectl rbg llm chat my-model --port-forward-timeout 60s
```

### Model not found

Ensure the model is pulled to storage:

```bash
kubectl rbg llm pull MODEL_NAME
```

### GPU allocation issues

Check available GPUs in your cluster:

```bash
kubectl describe nodes | grep -A 5 "Allocated resources"
```

## License

Apache License 2.0

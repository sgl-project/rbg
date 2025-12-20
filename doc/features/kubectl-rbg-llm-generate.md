# RBG Recommender Command

## Overview

The `kubectl-rbg llm generate` command currently integrates with [AI Configurator](https://github.com/ai-dynamo/aiconfigurator) to automatically generate optimized RoleBasedGroup deployment configurations for AI model serving.

## Prerequisites

1. **Install aiconfigurator**:

   ```bash
   pip install aiconfigurator
   ```

2. **Verify installation**:

   ```bash
   aiconfigurator version

   # version >= 0.5.0
   ```

3. **Refer to [kubectl-rbg](doc/features/kubectl-rbg.md) for instructions on installing the kubectl-rbg plugin locally:**

```shell
# Download source code
$ git clone https://github.com/sgl-project/rbg.git
# Build locally
$ make build-cli
# Install
$ chmod +x bin/kubectl-rbg
$ sudo mv bin/kubectl-rbg /usr/local/bin/
```

## Usage

### Basic Command

#### Option 1: Using TTFT and TPOT

```bash
kubectl rbg llm generate \
  --configurator-tool aiconfigurator \
  --model QWEN3_32B \
  --system h200_sxm \
  --total-gpus 8 \
  --backend sglang \
  --isl 5000 \
  --osl 1000 \
  --ttft 1000 \
  --tpot 10 \
  --save-dir /tmp/rbg-llm-generate-output
```

#### Option 2: Using Request Latency

```bash
kubectl rbg llm generate \
  --configurator-tool aiconfigurator \
  --model QWEN3_32B \
  --system h200_sxm \
  --total-gpus 8 \
  --backend sglang \
  --isl 5000 \
  --osl 1000 \
  --request-latency 2000 \
  --save-dir /tmp/rbg-llm-generate-output
```

### Command Flags

#### Required Flags

- `--model`: Model name (e.g., QWEN3_32B, LLAMA3.1_70B)
- `--system`: GPU system type (h100_sxm, a100_sxm, b200_sxm, gb200_sxm, l40s, h200_sxm)
- `--total-gpus`: Total number of GPUs to use for deployment
- `--isl`: Input sequence length
- `--osl`: Output sequence length

#### Latency Parameters (At least one group required)

You must provide **either**:
- **Option 1**: Both `--ttft` AND `--tpot`
- **Option 2**: `--request-latency`

#### Latency Flags
- `--ttft`: Time to first token in milliseconds (required with `--tpot` if `--request-latency` not set)
- `--tpot`: Time per output token in milliseconds (required with `--ttft` if `--request-latency` not set)
- `--request-latency`: End-to-end request latency target in milliseconds (alternative to `--ttft` and `--tpot`)

#### Optional Flags

- `--configurator-tool`: A tool for generating startup and configuration files, currently only supporting aiconfigurator (default: "aiconfigurator")
- `--backend`: Inference backend (current only supported: "sglang", default: "sglang")
- `--hf-id`: HuggingFace model ID (e.g., "Qwen/Qwen2.5-7B")
- `--decode-system`: GPU system for decode workers (defaults to `--system`)
- `--prefix`: Prefix cache length (default: 0)
- `--database-mode`: Performance database mode (default: "SILICON")
  - Options: SILICON, HYBRID, EMPIRICAL, SOL
- `--save-dir`: Directory to save results (default: "/tmp/rbg-llm-generate-output")

## Workflow

The command executes the following steps:

1. **Validation**: Validates all input parameters
2. **Dependency Check**: Verifies `configurator-tool` (e.g., aiconfigurator) is installed
3. **Optimization**: Runs Configurator Tool to generate optimal configurations
4. **Parsing**: Locates and parses the generated configuration files
5. **Rendering**: Generates two RBG deploy YAML files:
   - Prefill-Decode disaggregated mode
   - Aggregated mode
6. **Output**: Displays deploy recommendations and file paths

## Output

The command generates two YAML files in the specified `--save-dir`:

1. **`{model}-{backend}-disagg.yaml`**: Prefill-Decode disaggregated deploy
   - Separate prefill and decode workers
   - Includes router component

2. **`{model}-{backend}-agg.yaml`**: Aggregated deploy
   - Single worker role
   - Simpler architecture

## Example Output

```text
=== RBG LLM Generate ===

Checking dependencies...
Found aiconfigurator version: 0.5.0

Running AI Configurator optimization... This may take a few seconds.
✓ AI Configurator optimization completed successfully

Locating generated configurations...

Parsing AI Configurator output...

Generating RBG deployment YAMLs...

✓ Successfully generated 2 deployment recommendations:

Plan 1: Prefill-Decode Disaggregated Mode
  File: /tmp/rbg-llm-generate-output/qwen3-32b-sglang-disagg.yaml
  Configuration:
    - Prefill Workers: 4 (each using 1 GPU)
    - Decode Workers: 1 (each using 4 GPU)
    - Total GPU Usage: 8

Plan 2: Aggregated Mode
  File: /tmp/rbg-llm-generate-output/qwen3-32b-sglang-agg.yaml
  Configuration:
    - Workers: 1 (each using 8 GPU)
    - Total GPU Usage: 8

To deploy, run:
  kubectl apply -f /tmp/rbg-llm-generate-output/qwen3-32b-sglang-disagg.yaml
or
  kubectl apply -f /tmp/rbg-llm-generate-output/qwen3-32b-sglang-agg.yaml

Note: Ensure the 'qwen3-32b' PVC exists in your cluster before deploying.
```

## Deployment

Before deploying the generated YAML:

1. **Create model PVC**:

   ```bash
   kubectl apply -f - <<EOF
   apiVersion: v1
   kind: PersistentVolumeClaim
   metadata:
     name: qwen3-32b
   spec:
     accessModes:
       - ReadOnlyMany
     resources:
       requests:
         storage: 100Gi
     storageClassName: your-storage-class
   EOF
   ```

2. **Deploy the recommended configuration**:

   ```bash
   kubectl apply -f /tmp/rbg-llm-generate-output/qwen3-32b-sglang-disagg.yaml
   ```

3. **Monitor deployment**:

   ```bash
   kubectl get rbg
   kubectl get pod
   ```

## Troubleshooting

### aiconfigurator not found

```text
Error: aiconfigurator is not installed

Please install it using one of the following methods:
  pip install aiconfigurator
Or visit: https://github.com/ai-dynamo/aiconfigurator
```

**Solution**: Install aiconfigurator using pip.

### No output directory found

```text
Error: no output directory found matching pattern: QWEN3_32B_isl5000_osl1000_ttft1000_tpot10_*
```

**Solution**:

- Check if aiconfigurator executed successfully
- Verify the `--save-dir` path is correct
- Enable `-v=5` mode for detailed logs

### Invalid system type

```text
Error: invalid system invalid_system, must be one of: h100_sxm, a100_sxm, b200_sxm, gb200_sxm, l40s, h200_sxm
```

**Solution**: Use one of the supported GPU system types.

### Latency parameters validation failed

```text
Error: configuration validation failed: latency parameters validation failed: either (--ttft AND --tpot) or --request-latency must be specified with positive values
```

**Solution**: You must provide either:
- Both `--ttft` and `--tpot` with positive values (e.g., `--ttft 1000 --tpot 10`)
- Or `--request-latency` with a positive value (e.g., `--request-latency 2000`)

## Architecture

The recommender command consists of several modules:

- **types.go**: Data structures for configuration and parameters
- **dependency.go**: Checks for aiconfigurator availability
- **executor.go**: Builds and executes aiconfigurator commands
- **parser.go**: Locates and parses generator configurations
- **renderer.go**: Renders RBG YAML templates
- **generate.go**: Main command orchestration

# Engine Components Documentation

This document describes the engine components in the Patio module.

## Overview

The engine module provides interfaces and implementations for different inference engines. It allows Patio to work with
multiple inference engines like vLLM and SGLang through a common interface.

## Base Components

### InferenceEngine (Abstract Base Class)

The base class for all inference engines.

#### Properties

- `name` (str): Name of the inference engine
- `version` (str): Version of the inference engine
- `endpoint` (str): Endpoint URL for the inference engine
- `headers` (dict, optional): HTTP headers for requests

#### Methods

##### ready() -> bool

Check if the inference engine is ready to serve requests.

Returns:

- `bool`: True if the engine is ready, False otherwise

##### load_lora_adapter(request: LoadLoraAdapterRequest) -> Union[ErrorResponse, str]

Load a LoRA adapter into the inference engine.

Parameters:

- `request`: LoadLoraAdapterRequest object containing adapter information

Returns:

- `Union[ErrorResponse, str]`: Success message or error response

##### unload_lora_adapter(request: UnLoadLoraAdapterRequest) -> Union[ErrorResponse, str]

Unload a LoRA adapter from the inference engine.

Parameters:

- `request`: UnLoadLoraAdapterRequest object containing adapter information

Returns:

- `Union[ErrorResponse, str]`: Success message or error response

### inference_engine() -> InferenceEngine

Factory function to create an inference engine instance based on environment variables.

Returns:

- `InferenceEngine`: Instance of the appropriate inference engine

Raises:

- `ValueError`: If required environment variables are not set or if the engine is not supported

## Engine Implementations

### SGLangInferenceEngine

Implementation for the SGLang inference engine.

#### Endpoints

- Load LoRA adapter: `/load_lora_adapter`
- Unload LoRA adapter: `/unload_lora_adapter`

## Environment Variables

The engine components use the following environment variables:

| Variable                    | Description                                   | Required |
|-----------------------------|-----------------------------------------------|----------|
| `INFERENCE_ENGINE`          | Name of the inference engine (vllm or sglang) | Yes      |
| `INFERENCE_ENGINE_VERSION`  | Version of the inference engine               | Yes      |
| `INFERENCE_ENGINE_ENDPOINT` | Endpoint URL for the inference engine         | Yes      |

## Usage Examples

### Loading a LoRA Adapter

```python
from patio.api.protocol import LoadLoraAdapterRequest
from patio.engine.base import inference_engine

# Create engine instance
engine = inference_engine()

# Create request
request = LoadLoraAdapterRequest(
    lora_name="my-adapter",
    lora_path="/path/to/adapter"
)

# Load adapter
response = await engine.load_lora_adapter(request)
```

```bash
curl -X POST http://localhost:9091/load_lora_adapter \
    -H "Content-Type: application/json" \
    -d '{
      "lora_name": "'$loraName'",
      "lora_path": "'"$loraPath"'"
    }'
```

### Unloading a LoRA Adapter

```python
from patio.api.protocol import UnLoadLoraAdapterRequest
from patio.engine.base import inference_engine

# Create engine instance
engine = inference_engine()

# Create request
request = UnLoadLoraAdapterRequest(
    lora_name="my-adapter"
)

# Unload adapter
response = await engine.unload_lora_adapter(request)
```

```bash
curl -X POST http://localhost:9091/unload_lora_adapter \
    -H "Content-Type: application/json" \
    -d '{
      "lora_name": "'$loraName'"
    }'
```
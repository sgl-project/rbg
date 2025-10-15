# API Protocol Documentation

This document describes the data models used in the Patio API.

## Error Responses

### ErrorResponse

Standard error response format used throughout the API.

| Field   | Type    | Required | Description                    |
|---------|---------|----------|--------------------------------|
| code    | integer | Yes      | HTTP status code               |
| type    | string  | Yes      | Error type identifier          |
| message | string  | Yes      | Human-readable error message   |
| object  | string  | No       | Object type (always "error")   |
| param   | string  | No       | Parameter related to the error |

## Topology Management

### TopoRegisterRequest

Request to register a worker with the topology server.

| Field       | Type   | Required | Description                                       |
|-------------|--------|----------|---------------------------------------------------|
| topo_type   | string | Yes      | Type of inference engine (e.g., 'SGLang')         |
| worker_info | dict   | Yes      | Dictionary containing worker-specific information |

The `worker_info` dictionary must contain an `endpoint` key with a non-empty string value.

### TopoUnRegisterRequest

Request to unregister a worker from the topology server.

| Field     | Type   | Required | Description                                  |
|-----------|--------|----------|----------------------------------------------|
| topo_type | string | Yes      | Type of inference engine (e.g., 'SGLang')    |
| endpoint  | string | Yes      | Worker endpoint (must be a non-empty string) |

### TopoInfoRequest

Request for topology information.

| Field     | Type   | Required | Description                               |
|-----------|--------|----------|-------------------------------------------|
| topo_type | string | Yes      | Type of inference engine (e.g., 'SGLang') |

## LoRA Adapter Management

### LoadLoraAdapterRequest

Request to load a LoRA adapter.

| Field     | Type   | Required | Description                    |
|-----------|--------|----------|--------------------------------|
| lora_name | string | Yes      | Name of the LoRA adapter       |
| lora_path | string | Yes      | Path to the LoRA adapter files |

### UnLoadLoraAdapterRequest

Request to unload a LoRA adapter.

| Field     | Type   | Required | Description              |
|-----------|--------|----------|--------------------------|
| lora_name | string | Yes      | Name of the LoRA adapter |

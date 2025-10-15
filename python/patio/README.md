# Patio Python Module

Patio is a Python runtime component for the RBGS (Role-Based Group Scheduling) system. It provides a FastAPI-based server that acts as an interface between the RBGS controller and inference engines like vLLM and SGLang.

## Features

- FastAPI-based HTTP server
- LoRA adapter management for inference engines
- Prometheus metrics collection and exposition
- Topology management for distributed inference workloads
- Health check and readiness endpoints

## Installation

### Requirements

- Python 3.8+
- Required Python packages (see `requirements.txt`)

### Installation Steps

1. Clone the repository:
   ```bash
   git clone <repository-url>
   cd rbgs/python/patio
   ```

2. Install dependencies:
   ```bash
   pip install -r requirements.txt
   ```

3. For development, also install test dependencies:
   ```bash
   pip install -r requirements-dev.txt
   ```

## Usage

### Running the Server

To run the Patio server:

```bash
python -m patio.app
```

### Command Line Options

The Patio server accepts several command line arguments:

- `--host`: Host to listen on (default: 0.0.0.0)
- `--port`: Port to listen on (default: 9091)
- `--log-level`: Set the logging level (DEBUG, INFO, WARNING, ERROR, CRITICAL)
- `--enable-fastapi-docs`: Enable FastAPI documentation endpoints
- `--scrape-engine-metrics`: Enable scraping of engine metrics (default: True)

Example:
```bash
python -m patio.app --host 127.0.0.1 --port 8080 --log-level DEBUG
```

### Environment Variables

Patio uses several environment variables for configuration:

| Variable | Description | Default |
|----------|-------------|---------|
| `INFERENCE_ENGINE` | The inference engine to use (vllm or sglang) | sglang |
| `INFERENCE_ENGINE_VERSION` | Version of the inference engine | v0.5.3 |
| `INFERENCE_ENGINE_ENDPOINT` | Endpoint URL for the inference engine | None |
| `TOPO_TYPE` | Type of topology to use | None |
| `GROUP_NAME` | Name of the group in RBGS | None |
| `ROLE_NAME` | Name of the role in RBGS | None |
| `ROLE_INDEX` | Index of the role in RBGS | None |

## API Endpoints

### Server Endpoints

- `GET /` - Root endpoint returning server status
- `GET /health` - Health check endpoint
- `GET /metrics` - Prometheus metrics endpoint

### LoRA Management Endpoints

- `POST /load_lora_adapter` - Load a LoRA adapter
- `POST /unload_lora_adapter` - Unload a LoRA adapter

## Modules

### API

The API module contains FastAPI routers for handling HTTP requests:
- `server_router.py` - Server management endpoints
- `lora_router.py` - LoRA adapter management endpoints
- `protocol.py` - Request/response data models

### Engine

The engine module provides interfaces and implementations for different inference engines:
- `base.py` - Base inference engine interface
- `vllm_engine.py` - vLLM engine implementation
- `sglang_engine.py` - SGLang engine implementation

### Metrics

The metrics module handles Prometheus metrics collection:
- `metrics.py` - Built-in metrics definitions
- `engine_collector.py` - Collector for engine metrics
- `engine_metric_rules.py` - Rules for standardizing engine metrics
- `standard_rules.py` - Standard metric transformation rules

### Topology

The topology module manages distributed worker topology:
- `factory.py` - Factory for creating topology clients and servers
- `utils.py` - Utility functions for topology management
- `client/` - Client-side topology management
- `server/` - Server-side topology management

## Testing

Patio includes comprehensive unit tests using pytest.

### Running Tests

To run all tests:

```bash
cd python/patio
python -m pytest tests/ -v
```

Or use the provided test runner:

```bash
cd python/patio
python run_tests.py
```


## Development

### Adding New Features

1. Create new modules in the appropriate directory
2. Add corresponding tests in the `tests/` directory
3. Update documentation as needed
4. Run tests to ensure everything works correctly

## License

This project is licensed under the Apache License 2.0 - see the LICENSE file for details.
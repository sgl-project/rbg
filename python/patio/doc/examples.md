# Example Configuration

This document provides example configurations for running the Patio server with different inference engines.

## Environment Variables

### For SGLang Engine

```bash
export INFERENCE_ENGINE=sglang
export INFERENCE_ENGINE_VERSION=v0.5.3
export INFERENCE_ENGINE_ENDPOINT=http://sglang-server:30000
```

## Local

```bash
cd <project-dir>/python
python -m patio.app
```

## Testing LoRA Adapter Management




## Monitoring

### Accessing Metrics

Metrics are available at the `/metrics` endpoint:

```bash
curl http://localhost:9091/metrics
```

### Example Prometheus Configuration

```yaml

```

## Kubernetes Deployment

Coming soon…


# Metrics Documentation

This document describes the metrics components in the Patio module.

## Overview

The metrics module provides Prometheus metrics collection and exposition for the Patio server. It includes both built-in
metrics for the HTTP server and support for collecting metrics from inference engines.

## Built-in Metrics

### REQUEST_COUNT

A Counter metric tracking the total number of HTTP requests.

Labels:

- `method`: HTTP method (GET, POST, etc.)
- `endpoint`: Request endpoint path
- `status`: HTTP status code

### REQUEST_LATENCY

A Histogram metric tracking HTTP request latency in seconds.

Labels:

- `method`: HTTP method (GET, POST, etc.)
- `endpoint`: Request endpoint path

## Engine Metrics Collection

### EngineCollector

The `EngineCollector` class collects metrics from inference engines and standardizes them for consistent reporting.

#### Constructor Parameters

- `scrape_endpoint` (str): URL to scrape metrics from
- `metric_rules` (Dict[str, StandardRule]): Rules for standardizing metrics
- `prefix` (str, optional): Prefix for metric names (default: "")
- `timeout` (float, optional): Timeout for scraping requests (default: 2.0)

#### Methods

##### collect()

Collect and yield standardized metrics from the inference engine.

### StandardRule

Abstract base class for metric standardization rules.

### RenameStandardRule

Rule for renaming metrics and their sample names.

#### Constructor Parameters

- `old_name` (str): Original metric name
- `new_name` (str): New metric name

## Engine Metric Standardization

### SGLang Metrics

SGLang metrics are standardized to use the `patio:` prefix:

| Original Name                        | Standardized Name                   |
|--------------------------------------|-------------------------------------|
| `sglang:num_running_reqs`            | `patio:num_requests_running`        |
| `sglang:num_queue_reqs`              | `patio:num_requests_waiting`        |
| `sglang:token_usage`                 | `patio:gpu_cache_usage_perc`        |
| `sglang:time_to_first_token_seconds` | `patio:time_to_first_token_seconds` |
| `sglang:inter_token_latency_seconds` | `patio:inter_token_latency_seconds` |
| `sglang:e2e_request_latency_seconds` | `patio:e2e_request_latency_seconds` |

## Usage Examples

### Accessing Metrics

Metrics are exposed through the `/metrics` endpoint:

```bash
curl http://localhost:9091/metrics
```

### Custom Metric Collection

To collect metrics from a custom inference engine:

```python
from patio.metrics.engine_collector import EngineCollector
from patio.metrics.engine_metric_rules import get_metric_standard_rules

# Get standardization rules for your engine
rules = get_metric_standard_rules("sglang")

# Create collector
collector = EngineCollector(
    scrape_endpoint="http://<sglang_host>/metrics",
    metric_rules=rules
)

# Register collector
from patio.metrics.metrics import patio_registry

patio_registry.register(collector)
```

## Environment Variables

| Variable             | Description                        | Default  |
|----------------------|------------------------------------|----------|
| `METRIC_SCRAPE_PATH` | Path to scrape engine metrics from | /metrics |
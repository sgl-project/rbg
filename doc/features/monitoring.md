# Monitoring

## Prerequisites

- A Kubernetes cluster with version >= 1.28 is Required, or it will behave unexpected.
- Add the label `alibabacloud.com/inference-workload=xxx` for RBG instances

## Usage

1. Collect inference engine monitoring metrics

   The PodMonitor configuration is available in the deprecated examples for reference:
   
   ```bash
   kubectl apply -f examples/deprecated/v1alpha1/monitoring/podmonitor.yaml
   ```

2. Access the monitoring interfaces
   
   Install Prometheus and Grafana following the [SGLang monitoring documentation](https://github.com/sgl-project/sglang/blob/main/examples/monitoring/README.md).

   - Grafana: `http://localhost:3000`
   - Prometheus: `http://localhost:9090`

   If you have an existing Prometheus instance, import the corresponding Grafana dashboard using the provided [SGLang Grafana JSON](https://github.com/sgl-project/sglang/blob/main/examples/monitoring/grafana/dashboards/json/sglang-dashboard.json).

## Metrics

SGLang and vLLM expose Prometheus metrics that can be scraped:

- **SGLang**: Metrics on port 9090 (request latency, token throughput, GPU utilization)
- **vLLM**: Metrics on port 8000 (similar metrics via `/metrics` endpoint)

Configure the PodMonitor to scrape these endpoints from RBG pods.
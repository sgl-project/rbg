# Monitoring

## Prerequisites

- A Kubernetes cluster with version >= 1.28 is Required, or it will behave unexpected.
- Add the label `alibabacloud.com/inference-workload=xxx` for RBG instances

## Usage

1. Collect inference engine monitoring metrics

   ```bash
   kubectl apply -f examples/monitoring/podmonitor.yaml
   ```

2. Access the monitoring interfaces
   Install Prometheus and Grafana
   by [doc](https://github.com/sgl-project/sglang/blob/main/examples/monitoring/README.md).

- Grafana: `http://localhost:3000`
- Prometheus: `http://localhost:9090`

If you have an existing Prometheus instance, import the corresponding Grafana dashboard using the
provided [SGLang Grafana JSON](https://github.com/sgl-project/sglang/blob/main/examples/monitoring/grafana/dashboards/json/sglang-dashboard.json).

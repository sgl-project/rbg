# RBG Prometheus and Grafana integration

Standalone monitoring configuration for the RBG controller, following the layout of
Fluid's `integration/prometheus`. It does not change the RBG Helm chart or the
controller Deployment; apply it next to your existing Prometheus and Grafana.

## Files

| File | Purpose |
| ---- | ------- |
| `prometheus.yaml` | ConfigMap with a Prometheus scrape config for `rbgs-controller-manager` pods |
| `rbac.yaml` | ClusterRole and ClusterRoleBinding that let Prometheus read the authenticated `/metrics` endpoint |
| `rbgs-prometheus-grafana-monitor.json` | Grafana dashboard for the controller-runtime metrics exposed by the RBG controller |

## How the scrape works

The RBG controller serves metrics on the `--metrics-bind-address` port (`8443` by
default) with HTTPS. Unlike a plain HTTP metrics endpoint, every request is
authenticated with `TokenReview` and authorized with `SubjectAccessReview`
against the Kubernetes API server. Two consequences:

1. Prometheus must send its own service account token
   (`authorization.credentials_file` in `prometheus.yaml`).
2. That service account must be allowed to `get` the non-resource URL
   `/metrics`, which `rbac.yaml` grants.

The scrape config discovers controller pods by the `control-plane:
rbgs-controller` label, adds `namespace` and `pod` labels to every series, and
rebuilds the scrape address as `pod-ip:8443` because the metrics port is not
declared as a container port in the chart.

The config assumes the default secure metrics serving. Controllers started
with `--metrics-secure=false`, or with `--enable-webhooks=none` (which forces
`Metrics.SecureServing=false`), serve the same port over plain HTTP. For those
installations, change `scheme: https` to `scheme: http` in the scrape config;
the bearer token then becomes unnecessary, but leaving it configured is
harmless.

## Usage

1. Adjust `rbac.yaml` to bind the service account your Prometheus runs as
   (the default matches kube-prometheus-stack: `prometheus-k8s` in
   `monitoring`), then apply it:

   ```bash
   kubectl apply -f integration/prometheus/rbac.yaml
   ```

2. Either mount `prometheus.yaml` as an additional config in your Prometheus,
   or copy its `scrape_configs` entry into your existing Prometheus
   configuration. For a kube-prometheus-stack install, use the
   `additionalScrapeConfigs` secret.

3. Import `rbgs-prometheus-grafana-monitor.json` into Grafana (Dashboards ->
   New -> Import), select your Prometheus data source, and pick the
   controller namespace.

## Dashboard contents

The dashboard is built from the metrics controller-runtime exposes on
`/metrics`; RBG does not yet export custom business metrics. Panels cover:

- Reconciliation: rate, errors, terminal errors, panics, duration quantiles,
  active workers and max concurrent reconciles per controller.
- Work queues: depth, adds, retries, queue and work latency, stuck work
  indicators.
- Kubernetes API client: request rate by status code, method and failed
  (4xx/5xx) requests.
- Webhooks: request rate, latency quantiles, in-flight requests and panics.
- Controller process: leader election status, goroutines, resident memory and
  CPU usage.

The `namespace` and `controller` variables are label-based, so the dashboard
works for any RBG installation scraped by the same Prometheus.

## Security note

`prometheus.yaml` sets `insecure_skip_verify: true` because the metrics server
uses a self-signed certificate unless the controller is run with
`--metrics-cert-path` (see `config/default/kustomization.yaml` for the
cert-manager option). When you provision a real certificate for the metrics
server, replace `insecure_skip_verify` with the matching `ca_file`.

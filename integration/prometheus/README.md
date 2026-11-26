# RBG Prometheus and Grafana integration

Standalone monitoring configuration for the RBG controller, following the layout of
Fluid's `integration/prometheus`. It does not change the RBG Helm chart or the
controller Deployment; apply it next to your existing Prometheus and Grafana.

## Files

| File | Purpose |
| ---- | ------- |
| `scrape-config.yaml` | Bare Prometheus scrape config (`scrape_configs` only) for `rbgs-controller-manager` pods |
| `rbac.yaml` | ClusterRoles and ClusterRoleBindings that let Prometheus discover controller pods and read the authenticated `/metrics` endpoint |
| `rbgs-prometheus-grafana-monitor.json` | Grafana dashboard for the controller-runtime metrics exposed by the RBG controller |

## How the scrape works

The RBG controller serves metrics on the `--metrics-bind-address` port (`8443` by
default) with HTTPS. Unlike a plain HTTP metrics endpoint, every request is
authenticated with `TokenReview` and authorized with `SubjectAccessReview`
against the Kubernetes API server, and the `role: pod` service discovery
needs its own permissions. Three consequences:

1. Prometheus must send its own service account token
   (`authorization.credentials_file` in `scrape-config.yaml`).
2. That service account must be allowed to `get` the non-resource URL
   `/metrics`, which `rbac.yaml` grants.

3. The `role: pod` service discovery requires `get`/`list`/`watch` on pods.
   `rbac.yaml` also grants this via a separate `rbgs-prometheus-discovery`
   ClusterRole. A default kube-prometheus-stack Prometheus already has both
   permissions, so its users can skip `rbac.yaml` entirely. A kube-prometheus
   (jsonnet) Prometheus, whose service account the default subject matches,
   can list pods only in a few namespaces, so it needs the discovery
   ClusterRole; without it the job silently discovers no targets. If you
   know the namespace the controller runs in and prefer least privilege,
   bind a namespaced Role with the same rules instead, and add a
   `namespaces:` filter to the `kubernetes_sd_configs` entry.

The scrape config discovers controller pods by the `control-plane:
rbgs-controller` label, adds `namespace` and `pod` labels to every series, and
rebuilds the scrape address as `pod-ip:8443` because the metrics port is not
declared as a container port in the chart.

The config assumes the default secure metrics serving. Controllers started
with `--metrics-secure=false`, or with `--enable-webhooks=none` (which forces
`Metrics.SecureServing=false`), serve the same port over plain HTTP. For those
installations, change `scheme: https` to `scheme: http` in the scrape config;
remove the `authorization` block as well: with plain HTTP, Prometheus would
send the service-account token in cleartext to the pod IP on every scrape.

## Usage

1. Adjust `rbac.yaml` to bind the service account your Prometheus runs as,
   then apply it. The default subject matches a kube-prometheus (jsonnet)
   install (`prometheus-k8s` in `monitoring`); kube-prometheus-stack names
   the account `<release>-prometheus`, and its default ClusterRole already
   grants both discovery and `/metrics`, so kube-prometheus-stack users can
   skip this file entirely:

   ```bash
   kubectl apply -f integration/prometheus/rbac.yaml
   ```

2. Add the scrape job to your Prometheus. Consumers expect different shapes
   of the same job:

   - Prometheus `scrape_config_files` includes: reference `scrape-config.yaml`
     as-is. The include format is a mapping with a single `scrape_configs`
     key, which is exactly what the file contains.
   - The Prometheus Operator's `additionalScrapeConfigs` secret, and
     kube-prometheus-stack's `additionalScrapeConfigs` value: use the bare
     list under `scrape_configs`, i.e. the single `- job_name:
     rbgs-controller` entry, as the secret's or value's content.
   - An existing full configuration: copy the job entry under
     `scrape_configs`.

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

The `job`, `namespace` and `controller` variables are label-based. Every
panel query filters on the scrape `job`, so the dashboard stays scoped to the
RBG controller even when the same Prometheus scrapes other components, and it
keeps working if you rename the scrape job.

## Security note

`scrape-config.yaml` sets `insecure_skip_verify: true` because the shipped
RBG manifests do not provision a certificate for the metrics server: the
cert-manager blocks in `config/default/kustomization.yaml` are commented-out
kubebuilder scaffold, and the Helm chart has no knob for
`--metrics-cert-path`, so the controller serves a self-signed certificate.
If you run the controller with `--metrics-cert-path` and a real certificate,
note that the scrape targets are addressed by pod IP: set both
`tls_config.ca_file` and `tls_config.server_name` (a DNS SAN of that
certificate). Setting a CA without `server_name` fails, because the
certificate's SANs are service DNS names, not pod IPs.

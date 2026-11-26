# RBG Prometheus and Grafana integration

Standalone monitoring configuration for the RBG controller, following the layout of
Fluid's `integration/prometheus`. It does not change the RBG Helm chart or the
controller Deployment; apply it next to your existing Prometheus and Grafana.

## Files

| File | Purpose |
| ---- | ------- |
| `scrape-config.yaml` | Bare Prometheus scrape config (`scrape_configs` only) for `rbgs-controller-manager` pods |
| `rbac.yaml` | RBAC that lets Prometheus discover controller pods (a namespaced Role) and read the authenticated `/metrics` endpoint (a ClusterRole) |
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
   `rbac.yaml` grants this via a `rbgs-prometheus-discovery` Role and
   RoleBinding in `rbgs-system`, and the scrape config scopes discovery to
   the same namespace. Adjust both to the namespace the controller runs in.
   Scoping discovery matters for security: the scrape job sends the
   Prometheus service account token to every discovered target, and the
   `control-plane` label is not a security boundary, so without the namespace
   filter any workload in the cluster could impersonate a controller pod and
   capture the token. A default kube-prometheus-stack Prometheus already has
   both permissions, so its users can skip `rbac.yaml` entirely. A
   kube-prometheus (jsonnet) Prometheus, whose service account the default
   subject matches, can list pods only in a few namespaces, so it needs the
   discovery Role; without it the job silently discovers no targets.

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
   skip this file entirely. Also adjust the `rbgs-prometheus-discovery` Role
   and RoleBinding to the controller's namespace, and the `namespaces`
   filter in `scrape-config.yaml` to match:

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

## Troubleshooting

If the dashboard shows no data, first check whether the scrape itself is
healthy: empty `job`, `namespace` or `controller` variables usually mean the
scrape chain has not succeeded yet, not that the controller is broken. The
Scrape Health panel at the top of the dashboard shows the `up` metric per
controller pod.

- Check the Prometheus web UI (`/-/targets`) for the `rbgs-controller` job.
  The target's last error distinguishes the failure mode: `401`/`403` means
  authentication or authorization failed (the token, the `/metrics` RBAC, or
  the controller's TokenReview permissions), `x509` errors mean TLS, and
  `connection refused` usually means the port.
- Query `up{job="rbgs-controller"}` (or your renamed job) in Prometheus:
  `1` is a healthy scrape, `0` is a failed one.
- Verify the controller pods are discovered:
  `kubectl get pods -n <controller-namespace> -l control-plane=rbgs-controller -o wide`.
- Verify the Prometheus service account can discover pods and read
  `/metrics`:
  `kubectl auth can-i list pods -n <controller-namespace> --as=system:serviceaccount:<prometheus-namespace>:<prometheus-sa>`
  and
  `kubectl auth can-i get /metrics --as=system:serviceaccount:<prometheus-namespace>:<prometheus-sa>`.
- If you changed `--metrics-bind-address`, update the `8443` port in the
  `__address__` relabel rules of `scrape-config.yaml` as well.

## Dashboard contents

The dashboard is built from the metrics controller-runtime exposes on
`/metrics`; RBG does not yet export custom business metrics. Panels cover:

- Reconciliation: rate, errors, terminal errors, panics, duration quantiles,
  active workers and max concurrent reconciles per controller.
- Work queues: depth, adds, retries, queue and work latency, stuck work
  indicators.
- Kubernetes API client: request rate by status code, method and failed
  (4xx/5xx) requests.
- Scrape health: the `up` metric per controller pod.
- Webhooks: request rate, latency quantiles, in-flight requests, and
  admission and conversion panics.
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

`insecure_skip_verify: true` also means Prometheus does not verify the
identity of the metrics server: an attacker who can intercept or redirect
traffic between Prometheus and the controller pods can capture the
Prometheus service account token, and read or forge the scraped metrics. The
namespace-scoped discovery limits where that token is sent, but clusters that
do not fully trust pod-to-pod traffic should additionally restrict access to
the metrics port (for example with a NetworkPolicy) or provision a real
certificate for the metrics server and remove `insecure_skip_verify`.

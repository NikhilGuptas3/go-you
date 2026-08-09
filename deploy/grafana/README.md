# go-you Grafana dashboard

Dashboard for the go-you persona service metrics (exposed on `/metrics`, scraped
by the ServiceMonitor). 11 panels across API / Crawlers / Intelligence rows.

Two ways to load it:

## 1. Import the JSON by hand (quickest)

Grafana → **Dashboards → New → Import** → paste `go-you-dashboard.json` (or
upload the file) → pick your Prometheus data source → **Import**.

## 2. Provision via ConfigMap (persistent, GitOps)

```sh
kubectl apply -n <grafana-namespace> -f deploy/grafana/go-you-dashboard-configmap.yaml
```

The Grafana sidecar auto-loads any ConfigMap labelled `grafana_dashboard: "1"`
(the convention kube-prometheus-stack uses). The dashboard shows up under the
**go-you** folder within ~30s. No Grafana restart needed.

## Notes

- **Templating.** The dashboard has a `namespace` variable (defaults to
  `go-you-poc`) and a `Data source` variable, so nothing is hardcoded. Every
  query filters on `namespace=$namespace` — **required**, because go-you reuses
  hey-you's metric names and both land in the same Prometheus. If your Prometheus
  labels series by `job`/`service` instead of `namespace`, edit the variable's
  `label_values(api_status, namespace)` query and the panel matchers accordingly.
- **In-cluster only.** `ml_service_counter`, `real_time_cache`, and
  `you_intelligence` populate only when the ml/cache/config lanes run — they're
  empty under `LOCAL_DEV`.
- Regenerate the ConfigMap after editing the JSON:
  the ConfigMap embeds `go-you-dashboard.json` verbatim under `data:`.

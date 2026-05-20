# Grafana dashboard

Example dashboard for `k8s-pv-orphan-exporter`. Mirrors the alerting rules in
[`../deploy/prometheus-rules.yaml`](../deploy/prometheus-rules.yaml) so the
Grafana view shows the same signal Alertmanager paged on.

## Layout

Six panels driven by three template variables (`datasource`, `backend`, `node`):

| Panel | Source metric |
|-------|---------------|
| Dangling PVs (stat, red>0) | `pv_orphan_exporter_dangling_pvs` |
| Orphaned directories (stat, yellow>0) | `pv_orphan_exporter_orphaned_directories` |
| Stalest scan age (stat, yellow≥15m, red≥30m) | `time() - pv_orphan_exporter_last_scan_timestamp_seconds` |
| PV inventory size (stat) | `pv_orphan_exporter_pv_inventory_size` |
| Detection counts over time (timeseries) | dangling / orphaned / archived |
| Scan health (timeseries) | `scan_duration_seconds` p95 + `scan_errors_total` rate |

`released_pvs_retained` is intentionally omitted — the metric is registered but
not yet emitted (it's owned by the deferred cluster-wide collector, issue #4).
Adding a panel today would show "no data" everywhere, which is worse than
absent.

The "Stalest scan age" thresholds (15m yellow / 30m red) match the
`PVOrphanExporterScanStalled` alert default of 1800s; keep them aligned with
your `--scan.interval`.

## Import

**Grafana UI:** Dashboards → New → Import → upload JSON or paste contents.
On import, pick your Prometheus datasource for the `datasource` variable.

**Provisioning** (`grafana.ini` `[paths] provisioning = …`): drop the file in
`dashboards/` and point a YAML provider at it.

**kube-prometheus-stack grafana sidecar:**

```bash
kubectl -n monitoring create configmap k8s-pv-orphan-exporter-dashboard \
  --from-file=dashboards/k8s-pv-orphan-exporter.json \
  --dry-run=client -o yaml \
  | kubectl label --local -f - grafana_dashboard=1 --dry-run=client -o yaml - \
  | kubectl apply -f -
```

(Adjust the `grafana_dashboard=1` label to whatever label your sidecar selects
on.) A first-class chart toggle that bundles this is tracked as a follow-up —
see #12 for chart-side improvements generally.

## Editing

The JSON was hand-written, not exported from Grafana. If you edit it in the
Grafana UI and re-export, Grafana will rewrite a lot of fields it didn't
emit originally (panel IDs, version numbers, `__inputs`). Strip the
`__inputs` / `__requires` sections (the `datasource` template variable
replaces both) before committing.

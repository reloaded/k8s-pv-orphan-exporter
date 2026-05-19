# Deployment manifests

Plain `kubectl apply -f` manifests for the exporter. The Phase 4
Helm chart will parameterise these; until then, edit the YAML in
place to match your cluster.

## Layout

| File | When to use |
|------|-------------|
| `local-path-daemonset.yaml` | local-path / hostPath PVs — runs one pod per node, mounts the storage root read-only via `hostPath`. |
| `deployment-nfs.yaml` | NFS PVs (in-tree, nfs-subdir-external-provisioner, or `nfs.csi.k8s.io`) — runs one replica per export, mounts the export read-only. |
| `prometheus-rules.yaml` | Prometheus Operator `PrometheusRule` with the alerting rules (design.md §9.4). Apply on whichever topology you run; see [Alerting](#alerting). |

Both manifests (re)declare the same `pv-orphan-exporter` Namespace /
ServiceAccount / ClusterRole / ClusterRoleBinding, so they can be
applied independently or together (idempotent).

## Quick start

```bash
kubectl apply -f deploy/local-path-daemonset.yaml
kubectl -n pv-orphan-exporter rollout status ds/pv-orphan-exporter-local-path
```

Then point Prometheus at the headless `Service` (or use the
`prometheus.io/*` annotations on the pod template if your operator
honours them).

## Storage root

`local-path-daemonset.yaml` defaults to scanning
`/opt/local-path-provisioner`. If your local-path provisioner is
configured for a different root (e.g. `/var/lib/rancher/k3s/storage`
on k3s):

1. Edit the `hostPath.path` of the `local-path` volume.
2. Edit `--scanner.local-path.storage-roots=...` in the container
   args. Multiple roots are supported as a comma-separated list.

The two must agree — the flag tells the binary where to look inside
the container, the volume tells the kubelet what to mount there.

## Permissions

The container runs as the distroless-nonroot user (UID 65532). The
walker only needs `ReadDir` on the storage root, which works against
the 0777 directory mode that local-path-provisioner sets by default.
If your storage root is more restrictive:

- Easiest: `chmod o+rx` the root and per-PV directories.
- Alternative: switch the Dockerfile base image to a
  `:static`-rooted variant and add `securityContext.runAsUser: 0`
  to the DaemonSet pod template.

## NFS

`deployment-nfs.yaml` runs **one replica per export**. Before
applying, edit the manifest's `nfs` volume (server + path) and these
container args so PV path resolution is correct:

| Arg | Must equal | Why |
|-----|-----------|-----|
| `--scanner.nfs.mount-path` | the `volumeMount.mountPath` (default `/mnt/nfs`) | where the binary looks inside the container |
| `--scanner.nfs.server` | `spec.nfs.server` / CSI `volumeAttributes.server` on covered PVs | scopes this instance to its export; empty = match every NFS PV |
| `--scanner.nfs.export-root` | the server-side export path (the `nfs.path` you mount) | stripped from in-tree `spec.nfs.path` to locate the PV under the mount — **required** for in-tree dangling detection |
| `--scanner.nfs.archived-prefix` | your subdir provisioner's retained-dir prefix (default `archived-`) | so retained dirs report as *archived*, not *orphaned* |

Run several exports by applying several copies with distinct
`metadata.name`s (and matching args/volume per export).

NFS PVs are cluster-wide, so unlike the DaemonSet the NFS Deployment
emits metrics with an empty `node` label.

## Alerting

`prometheus-rules.yaml` ships three rules (design.md §9.4):

| Alert | Fires when | Severity |
|-------|-----------|----------|
| `PVOrphanExporterScanStalled` | `time() - pv_orphan_exporter_last_scan_timestamp_seconds > 1800` for 10m | warning |
| `KubernetesDanglingPV` | `pv_orphan_exporter_dangling_pvs > 0` sustained 15m, for 30m | warning |
| `KubernetesOrphanedStorageDirectories` | `pv_orphan_exporter_orphaned_directories > 0` sustained 1h, for 1h | info |

**`PVOrphanExporterScanStalled` is the supported way to detect a
wedged scan loop — not the pod liveness probe.** The `/healthz`
probe deliberately only reports that the HTTP server is up: a stuck
informer or a filesystem walk hung on a slow mount leaves the pod
"healthy" while its metrics silently go stale. Tying `/healthz` to
scan freshness instead would restart pods every time a node's disk is
briefly slow — worse than a stale metric. The alert is the right
tool; the probe is intentionally dumb. (Background: issue #7.)

Two things to check before relying on these:

1. **Operator selector.** Most Prometheus Operator installs only load
   `PrometheusRule` objects whose labels match the Prometheus CR's
   `spec.ruleSelector` (often `release: <kube-prometheus-stack
   release>`). Add that label to `metadata.labels` or the rules are
   silently inert. Plain (non-operator) Prometheus: copy `spec.groups`
   into a `rule_files:` file — the schema is identical.
2. **Threshold vs scan interval.** The 1800s stall threshold assumes
   the default `--scan.interval=5m` (~6 missed scans). If you raise
   `--scan.interval`, raise the threshold to keep roughly that margin
   (> 6 × interval).

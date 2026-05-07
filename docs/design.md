# Design: k8s-pv-orphan-exporter

> Status: draft. This is the v0 design — concrete enough to start coding,
> loose enough to be revised once we hit reality. Sections marked **TBD** need
> follow-up before the corresponding code lands.

## 1. Problem statement

Kubernetes `PersistentVolume` (PV) objects are abstractions that point at
real storage on real disks. The k8s API tracks the abstraction; nothing in
core k8s validates that the underlying directory or block device actually
exists, or that every directory under a known storage root corresponds to a
live PV.

Two failure modes silently accumulate over time:

- **Dangling PV** — A `PersistentVolume` exists in the API, but its backing
  directory is missing on disk. Causes include: manual `rm -rf` of the storage
  root, a node whose disk was reformatted, a CSI driver that lost track of the
  volume, or an operator who deleted the directory thinking it was unused.
  Workloads bound to this PV will fail mount; new workloads might bind it and
  get a confusing "no such file or directory" later.

- **Orphaned directory** — A directory exists under a known storage root, but
  no `PersistentVolume` references it. Causes include: failed PV deletion (the
  reclaim policy did not actually clean up), `Retain` reclaim policy with the
  PV later force-deleted, manual provisioning that bypassed the cluster, or a
  CSI driver crash mid-cleanup. These directories silently consume disk and
  never get reclaimed.

Neither is surfaced by the standard observability stack:

- `kube-state-metrics` reports PV state from the API. It cannot see the disk.
- `node_exporter` reports filesystem usage. It does not know which directory
  belongs to which PV.
- `kubelet` exposes `kubelet_volume_stats_*` for **mounted** volumes only.
  Orphans are by definition not mounted.

This exporter closes that gap.

## 2. Goals and non-goals

### Goals

- Detect both **dangling PVs** (PV → no folder) and **orphaned directories**
  (folder → no PV).
- Support the two backend classes most self-hosted and small-cluster setups
  use: **local-path / host-path** and **NFS** (both subdir-provisioner and CSI styles).
- Be safe to run continuously: read-only, low overhead, no destructive actions.
- Be Prometheus-native: standard exporter binary, `/metrics` endpoint, idiomatic
  metric naming, low cardinality by default.
- Run in-cluster (preferred) or out-of-cluster (for development and one-shot scans).

### Non-goals (v0)

- **Deletion or auto-cleanup.** This exporter never touches storage. Cleanup is
  a human (or separate operator) decision.
- **Block-mode PVs.** `volumeMode: Block` PVs are out of scope for v0.
  Backends like LVM, iSCSI raw block, or Ceph RBD raw block require entirely
  different probing semantics (block device existence, not directory existence).
- **General CSI driver coverage.** Each CSI driver has its own on-disk layout.
  We support a curated set of drivers; everything else is roadmap.
- **Tracking PVC orphans.** A PVC without a bound PV is a separate problem
  (workload misconfiguration), already visible from `kube-state-metrics`.

## 3. Glossary

| Term | Definition |
|------|------------|
| **PV** | A `PersistentVolume` API object. |
| **PVC** | A `PersistentVolumeClaim` API object. |
| **Backend** | The underlying storage system referenced by a PV (local-path, NFS, etc.). |
| **Storage root** | The top-level directory under which a backend stores per-PV directories (e.g. `/opt/local-path-provisioner/`, an NFS export root). |
| **PV directory** | The specific directory referenced by one PV (e.g. `<storage-root>/pvc-<uuid>_<namespace>_<pvc-name>`). |
| **Dangling PV** | A PV exists in the API; its PV directory does not exist on disk. |
| **Orphaned directory** | A directory exists under a storage root; no PV references it. |
| **Archived directory** | A directory under a storage root that the provisioner has prefixed with `archived-` (nfs-subdir-external-provisioner convention) — intentionally retained, but no longer referenced by a PV. We track these as a separate category. |
| **Released PV** | A PV in the `Released` phase whose PVC has been deleted but whose reclaim policy is `Retain` — by design has no PVC, but still has a backing directory. Not an orphan. |

## 4. High-level architecture

```
┌──────────────────────── k8s-pv-orphan-exporter (single Go binary) ─────────────────────────┐
│                                                                                            │
│   ┌──────────────────┐    ┌────────────────────┐    ┌─────────────────────────────────┐    │
│   │ k8s API watcher  │───▶│   PV inventory     │◀───│  Scanner runner (per backend)   │    │
│   │ (client-go       │    │   (in-memory       │    │  - LocalPathScanner             │    │
│   │  informers)      │    │   index by         │    │  - NFSScanner                   │    │
│   └──────────────────┘    │   backend, node,   │    │  - (future) LonghornScanner …   │    │
│                           │   path)            │    └─────────────────────────────────┘    │
│                           └─────────┬──────────┘                  │                        │
│                                     ▼                             ▼                        │
│                           ┌────────────────────────────────────────────┐                   │
│                           │            Diff engine                     │                   │
│                           │  - dangling = PV ∧ ¬folder                 │                   │
│                           │  - orphan   = folder ∧ ¬PV ∧ ¬archived     │                   │
│                           │  - archived = folder ∧ ¬PV ∧ archived-pfx  │                   │
│                           │  - released = PV phase=Released ∧ Retain   │                   │
│                           └────────────────────┬───────────────────────┘                   │
│                                                ▼                                           │
│                           ┌────────────────────────────────────────────┐                   │
│                           │       Prometheus collectors (registry)     │                   │
│                           └────────────────────┬───────────────────────┘                   │
└────────────────────────────────────────────────┼───────────────────────────────────────────┘
                                                 ▼
                                       HTTP /metrics  :9877
```

### Components

- **k8s API watcher** — `client-go` informers on `PersistentVolume` (and
  `StorageClass`, used to look up provisioner names). PVCs are not watched in
  v0; PV → PVC linkage comes from `pv.spec.claimRef`.
- **PV inventory** — Indexed in-memory snapshot, refreshed via the informer's
  delta queue. Indexed by:
  - backend kind (local-path, NFS, …)
  - node name (for node-local backends)
  - resolved on-disk path
- **Scanner runner** — One scanner per configured backend. Scanners are pluggable
  (`scanner.Scanner` interface). Each scanner produces a `ScanResult` with
  the directories it observed under its storage root.
- **Diff engine** — Joins PV inventory and scan results into four sets:
  *dangling*, *orphaned*, *archived*, *released*. Pure function, easily testable.
- **Prometheus collectors** — Custom collectors that re-evaluate on each scrape
  using the most recent scan result. No goroutine push to a global registry.
- **HTTP server** — Standard `promhttp` handler on `:9877` (TBD: register in
  the Prometheus default port allocations registry once the project is public).

## 5. Detection model

### 5.1 The set algebra

Let:

- `P` = the set of PV directories *expected* to exist, derived from the PV
  inventory after filtering to the current backend / node.
- `D` = the set of directories *actually* observed by the scanner under the
  configured storage roots.
- `A ⊆ D` = the subset of observed directories whose names match a known
  "archived" prefix (e.g. `archived-`).
- `R ⊆ P` = PVs in phase `Released` with `persistentVolumeReclaimPolicy: Retain`.

Then:

```
dangling   = P  \ D
orphaned   = D  \ (P ∪ A)
archived   = A
released   = R                  (informational; not an orphan)
```

These four sets are computed per backend, per node (where applicable), per scan.

### 5.2 Grace period

A directory may transiently be missing while a PV is being provisioned, or a
PV may transiently exist before the directory shows up. Without a grace
period the exporter would alert on every provisioning event.

Each candidate dangling/orphaned item is held in a "pending" state until it
has been observed for `--grace-period` (default `5m`) consecutive scans. Only
then is it counted in the exposed metrics.

The pending state itself is *not* exported as a metric — it would be noisy
and not actionable.

### 5.3 Path resolution per PV

The scanner needs to map each PV to a concrete on-disk path within the storage
root it scans. Resolution rules:

| PV spec field | Path used |
|---------------|-----------|
| `spec.local.path` | Used directly. PV is bound to a node via `nodeAffinity` — match by node name. |
| `spec.hostPath.path` | Used directly. PV applies to **every** node (hostPath has no node affinity in core k8s). For DaemonSet deployments this means we expect the directory on the local node. |
| `spec.nfs.server` + `spec.nfs.path` | Match against the NFSScanner's configured `(server, export-root)` pair; relative path under the mount = `path` minus `export-root`. |
| `spec.csi.driver == "nfs.csi.k8s.io"` + `volumeAttributes.server` + `volumeAttributes.share` + `volumeAttributes.subDir` | Same as above, with `subDir` as the relative path under the mount. |
| `spec.csi.driver` (other drivers) | Not supported in v0. PV is recorded but produces a "unknown backend" info metric. |

### 5.4 Subdir provisioner ("archived-" prefix)

`nfs-subdir-external-provisioner` renames a deleted-but-retained directory
from `<namespace>-<pvc>-<pvname>` to `archived-<namespace>-<pvc>-<pvname>`.
We treat any directory whose basename starts with `archived-` as an
**archived directory** rather than an orphan, and expose it as a separate
metric (`pv_orphan_exporter_archived_directories`).

This is important because archived directories are *intentionally* unreferenced;
treating them as orphans would generate constant alerts on healthy clusters.

## 6. Backends

### 6.1 Local-path / host-path

Most common implementation: Rancher's
[`local-path-provisioner`](https://github.com/rancher/local-path-provisioner).

- **Default storage root:** `/opt/local-path-provisioner/`
- **Per-PV directory naming:** `pvc-<uuid>_<namespace>_<pvc-name>` (the
  provisioner's default; configurable per StorageClass).
- **Node binding:** The PV is bound to a single node via `nodeAffinity`. Only
  that node has the directory.

Deployment: **DaemonSet**. Each pod scans the local storage root via a `hostPath`
volume mount. Each pod knows its own `NODE_NAME` (downward API) and only
considers PVs whose `nodeAffinity` includes that node.

Configuration:

```
--scanner.local-path.enabled
--scanner.local-path.storage-roots=/opt/local-path-provisioner,/var/lib/rancher/k3s/storage
--scanner.local-path.exclude=lost+found,.snapshot
```

Multiple roots are supported because some clusters configure multiple paths
in a single StorageClass.

### 6.2 NFS

Two flavors are supported in v0:

- **In-tree NFS PVs** — `spec.nfs.server` + `spec.nfs.path`.
- **`nfs.csi.k8s.io` CSI** — modern CSI driver; PV path is in
  `spec.csi.volumeAttributes`.

**`nfs-subdir-external-provisioner`** uses in-tree-style PV specs (it provisions
a subdirectory and creates an NFS PV pointing to it), so it is covered by the
in-tree path.

Deployment: **Deployment** (single replica). The pod mounts the NFS export
root as a regular volume (the user provides an NFS-backed `PersistentVolume`
or `Volume` for the exporter pod itself). The scanner walks the mounted root.

Configuration:

```
--scanner.nfs.enabled
--scanner.nfs.mount-path=/mnt/nfs            # path inside the container
--scanner.nfs.server=nfs.example.internal    # used to match PV.spec.nfs.server
--scanner.nfs.export-root=/export/k8s        # used to compute PV-relative paths
--scanner.nfs.archived-prefix=archived-
--scanner.nfs.exclude=.snapshot,lost+found
```

If the user runs multiple NFS exports, they run multiple instances of the
exporter (one per export). This is simpler than juggling multiple mounts in
one process, and matches how `node_exporter` is typically deployed for NFS
(one per export).

## 7. Deployment topologies

| Topology | Use when | Workload kind |
|----------|----------|---------------|
| Per-node DaemonSet | local-path / hostPath PVs | `DaemonSet` |
| Per-export Deployment | NFS exports | `Deployment` (replicas: 1) |
| Local development | Quick check from a workstation | Plain `go run`, `--kubeconfig=~/.kube/config` |

A single binary handles all three. The mode is selected by which `--scanner.*.enabled`
flags are set; the user can enable multiple scanners in one process if it makes sense
(e.g. one Deployment scanning two NFS exports — though we recommend separate pods).

## 8. Configuration surface

### 8.1 Flags

```
# Global
--web.listen-address=":9877"
--web.telemetry-path="/metrics"
--log.level=info|debug|warn|error
--log.format=text|json

# Kubernetes client
--kubeconfig=""                              # empty = in-cluster
--k8s.qps=20
--k8s.burst=40

# Scanning
--scan.interval=5m
--scan.timeout=2m
--scan.grace-period=5m
--scan.max-depth=2                           # how deep under storage root to walk

# Cardinality controls
--metrics.per-item-info=false                # emit pv_orphan_exporter_*_info per item
--metrics.per-item-info.max=500              # cap before truncation

# Backends (any subset can be enabled)
--scanner.local-path.enabled
--scanner.local-path.storage-roots=/opt/local-path-provisioner
--scanner.local-path.exclude=lost+found,.snapshot

--scanner.nfs.enabled
--scanner.nfs.mount-path=/mnt/nfs
--scanner.nfs.server=
--scanner.nfs.export-root=
--scanner.nfs.archived-prefix=archived-
--scanner.nfs.exclude=.snapshot,lost+found
```

### 8.2 Environment variables

All flags can be set as env vars by upper-casing and replacing dots with
underscores (kingpin convention or stdlib + a small helper).

`NODE_NAME` is read from the downward API in DaemonSet deployments; not exposed
as a flag.

## 9. Metrics surface

All metric names are prefixed `pv_orphan_exporter_`.

### 9.1 Operational metrics (always on)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `pv_orphan_exporter_build_info` | Gauge (=1) | `version`, `revision`, `branch`, `goversion` | Static build info. |
| `pv_orphan_exporter_up` | Gauge | `backend`, `instance_id` | 1 if the most recent scan completed without error. |
| `pv_orphan_exporter_scan_duration_seconds` | Histogram | `backend` | How long each scan took. |
| `pv_orphan_exporter_scan_errors_total` | Counter | `backend`, `error_kind` | Scan errors. |
| `pv_orphan_exporter_last_scan_timestamp_seconds` | Gauge | `backend` | Unix timestamp of most recent successful scan. |
| `pv_orphan_exporter_pv_inventory_size` | Gauge | `backend` | Number of PVs the exporter is tracking for this backend. |

### 9.2 Aggregate counts (always on, low cardinality)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `pv_orphan_exporter_dangling_pvs` | Gauge | `backend`, `storageclass`, `node` | PVs whose backing directory is missing. |
| `pv_orphan_exporter_orphaned_directories` | Gauge | `backend`, `node` | Directories with no referencing PV. |
| `pv_orphan_exporter_archived_directories` | Gauge | `backend`, `node` | Directories matching the archived prefix. |
| `pv_orphan_exporter_released_pvs_retained` | Gauge | `backend`, `storageclass` | PVs in `Released` phase with `Retain` reclaim policy (informational). |

`node` label is only set for node-local backends (otherwise empty string or omitted).

### 9.3 Per-item info metrics (opt-in, capped)

Enabled with `--metrics.per-item-info=true`. Values are always `1`; the
information lives in the labels.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `pv_orphan_exporter_dangling_pv_info` | Gauge | `pv_name`, `pvc_namespace`, `pvc_name`, `storageclass`, `backend`, `node`, `expected_path` | One series per dangling PV. |
| `pv_orphan_exporter_orphaned_directory_info` | Gauge | `path`, `backend`, `node`, `bytes` | One series per orphaned directory. |

Cardinality is capped at `--metrics.per-item-info.max` (default 500). When
exceeded, the exporter emits a warning log line and a `pv_orphan_exporter_per_item_info_truncated`
gauge so operators know they're missing detail.

### 9.4 Recommended Prometheus rules / alerts (sketch)

Shipped under `deploy/prometheus-rules.yaml`:

```yaml
- alert: KubernetesDanglingPV
  expr: max_over_time(pv_orphan_exporter_dangling_pvs[15m]) > 0
  for: 30m
  labels: { severity: warning }
  annotations:
    summary: "{{ $value }} PV(s) with missing backing directory ({{ $labels.backend }})"

- alert: KubernetesOrphanedStorageDirectories
  expr: max_over_time(pv_orphan_exporter_orphaned_directories[1h]) > 0
  for: 1h
  labels: { severity: info }

- alert: PVOrphanExporterScanStalled
  expr: time() - pv_orphan_exporter_last_scan_timestamp_seconds > 1800
  for: 10m
  labels: { severity: warning }
```

## 10. RBAC

Minimal cluster-scoped read access:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: k8s-pv-orphan-exporter }
rules:
  - apiGroups: [""]
    resources: [persistentvolumes, persistentvolumeclaims]
    verbs: [get, list, watch]
  - apiGroups: ["storage.k8s.io"]
    resources: [storageclasses]
    verbs: [get, list, watch]
  - apiGroups: [""]
    resources: [nodes]
    verbs: [get, list, watch]
```

No write verbs anywhere. `nodes` is only needed so the DaemonSet pod can
correlate `nodeAffinity` selectors with the actual node name.

## 11. Security & filesystem access

- The exporter mounts storage roots **read-only** (`readOnly: true` on the
  hostPath / NFS volume).
- It does not call `os.Remove`, `os.RemoveAll`, `os.Rename`, or `os.Truncate`
  anywhere. A linter rule (`forbidigo`) enforces this in `golangci-lint`.
- It does not follow symlinks (`filepath.WalkDir` with manual `Lstat` checks).
  This avoids escapes from the scanned root.
- Walk depth is bounded by `--scan.max-depth` (default 2) — enough to catch
  the per-PV directory layer plus one level of nested provisioner layout, no
  deeper.

## 12. Implementation plan (phased)

### Phase 1 — skeleton (one PR)

- `cmd/k8s-pv-orphan-exporter/main.go` with flag parsing and `/metrics` server.
- `internal/k8s` with a `client-go` informer factory.
- `internal/scanner` interface and a stub `LocalPathScanner` that returns hardcoded data.
- Basic operational metrics (`build_info`, `up`, `scan_duration_seconds`).
- Unit tests for the diff engine using table-driven inputs.
- CI/CD under `.github/workflows/` (pulled forward from Phase 4):
  - `ci.yml` — lint + test + build + docker build on every PR and `main` push (intended to be a required status check via repo branch protection).
  - `release.yml` — multi-arch (`linux/amd64`, `linux/arm64`) image to `ghcr.io/reloaded/k8s-pv-orphan-exporter` on `v*` tags, tagged `vX.Y.Z`, `latest`, and `sha-<short>`.
  - `nightly.yml` — daily unit (`-count=2`) and integration (`-tags=integration`) runs; the integration job runs now (no integration tests yet) and starts exercising real cases as Phase 2 lands them.
- Goal: `go build`, `go test`, container builds, scrape returns sensible metrics, CI is green on PRs.

### Phase 2 — local-path scanner

- Real `LocalPathScanner` that walks configured roots.
- Path resolution against `spec.local.path` and `spec.hostPath.path`.
- Node correlation via downward-API `NODE_NAME`.
- DaemonSet manifest under `deploy/daemonset.yaml`.
- Integration test against `kind` with `local-path-provisioner` installed.

### Phase 3 — NFS scanner

- `NFSScanner` walking a mounted NFS root.
- Path resolution against `spec.nfs.*` and `nfs.csi.k8s.io` CSI.
- Archived-prefix handling.
- Deployment manifest under `deploy/deployment-nfs.yaml`.
- Integration test against `kind` + a sidecar NFS server.

### Phase 4 — release polish

- Helm chart under `charts/k8s-pv-orphan-exporter/`.
- Example Grafana dashboard JSON.
- Prometheus alerting rules under `deploy/prometheus-rules.yaml`.
- Goreleaser config for tagged releases (binaries, checksums, GitHub Releases).

> Note: the GitHub Actions workflows that build and publish multi-arch
> container images to GHCR were originally scoped here but landed in
> Phase 1 (`release.yml`). Phase 4 narrows to release artifacts other
> than the container image.

### Phase 5 (roadmap, not committed)

- Longhorn backend.
- Ceph RBD / CephFS backends.
- iSCSI / LVM block-mode support (requires block-device probing, not directory walks).
- An optional "what would I delete?" dry-run report (still no actual deletes).

## 13. Testing strategy

### Unit

- **Diff engine**: pure function over `(PV inventory, scan result)`. Table-driven
  tests cover every combination: matching, missing folder, missing PV, archived,
  released, hostPath multi-node, CSI volume attributes.
- **Path resolution**: table-driven against synthetic PV specs.
- **Scanner walk**: use [`spf13/afero`](https://github.com/spf13/afero) or
  `testing/fstest.MapFS` so the walker can be tested without touching real disk.

### Integration

- A `kind` cluster spun up by the test harness with `local-path-provisioner`
  pre-installed. Tests create PVs, manually delete the backing directory or
  manually create stray directories, and assert the exporter's `/metrics`
  reflects the expected diff after one scan cycle.
- For NFS: a sidecar `itsthenetwork/nfs-server-alpine` container in the same
  `kind` network, with `nfs-subdir-external-provisioner` installed.

### Lint / static analysis

- `golangci-lint` with `forbidigo` rules forbidding `os.Remove*`, `os.Rename`,
  `os.Truncate`, `syscall.Unlink`.
- `gofumpt`, `goimports`, `govet`, `staticcheck`, `errcheck`, `gosec`.

## 14. Edge cases & gotchas

- **Provisioning races.** A PV is created seconds before its directory exists.
  Mitigated by `--scan.grace-period`. Scanners include a configurable list of
  prefixes (e.g. `pvc-`) that, if a directory is *being* created and is empty
  for less than the grace period, are not flagged.
- **PVs with no `spec.local.path` / `spec.nfs.path`.** Could happen with
  third-party CSI drivers we don't recognize. These are recorded in
  `pv_orphan_exporter_pv_inventory_size` but not in any orphan/dangling count;
  they are surfaced by an `unknown_backend` label so operators know coverage
  is incomplete.
- **Symlinks.** Not followed. A symlink in the storage root is treated as a
  directory entry to be matched against PVs by name only, never traversed.
- **Mount points within the storage root.** Skipped via `os.Stat` + device
  number comparison against the root's device. (Optional in v0; controlled by
  `--scan.cross-fs=false` default.)
- **Hidden / system files.** `lost+found`, `.snapshot`, `.zfs` — excluded by
  the per-scanner `--exclude` flag (defaults populated for known cases).
- **Slow NFS.** A `Stat` on an unresponsive NFS export can hang for the kernel
  RPC timeout. Each scan is bounded by `--scan.timeout`; a hung scan is
  abandoned and `pv_orphan_exporter_scan_errors_total{error_kind="timeout"}`
  is incremented. The previous successful scan's metrics remain exposed.
- **Cardinality blowup.** Per-item info metrics are off by default and capped
  when on. We deliberately do **not** label the aggregate counts with PV name
  or path.
- **`Released` PVs with `Retain` reclaim policy.** These are *intentional* —
  the cluster operator wants the data to survive PVC deletion. Tracked
  separately so operators can decide when to clean up.

## 15. Open questions (TBD)

- **Port number.** `:9877` is a placeholder. Need to register a port in the
  Prometheus default port allocations registry once the project is public.
- **`local-path-provisioner` configmap parsing.** The provisioner's storage roots
  are configured in a ConfigMap. Should the exporter read that ConfigMap
  automatically (one less flag to set, one more permission to grant), or stick
  with the explicit-flag approach?
- **Per-PVC labels.** Some operators want the orphan metric labelled with the
  *previous* PVC namespace/name (parsed out of the directory name) so they can
  attribute disk waste to a team. This is per-item info territory; defer to phase 4.
- **Rate of disk I/O.** Walking large storage roots could be expensive. Do we
  need to throttle (`golang.org/x/time/rate`) the per-directory `Stat` calls?
  Probably not at typical home-cluster sizes; revisit if anyone complains.
- **Directory size in bytes.** Computing directory size means recursing through
  the contents, which is much more expensive than just enumerating top-level
  entries. v0 emits `bytes` only on the per-item info metric and only when
  `--metrics.per-item-info.compute-size=true` (off by default).

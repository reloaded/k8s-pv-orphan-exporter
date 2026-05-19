# k8s-pv-orphan-exporter Helm chart

Parameterised install of the exporter — the chart-managed equivalent
of the raw manifests in [`deploy/`](../../deploy/). It renders the
local-path DaemonSet, the NFS Deployment, their Services, RBAC, and
the Prometheus alerting rules, all toggleable.

> The raw `deploy/*.yaml` manifests are still maintained for
> Helm-free users; this chart is the recommended path otherwise.

## Install

```bash
helm install pv-orphan-exporter ./charts/k8s-pv-orphan-exporter \
  --namespace pv-orphan-exporter --create-namespace
```

Defaults reproduce `deploy/local-path-daemonset.yaml`: **local-path
scanner on, NFS off, PrometheusRule off.** Namespace is **not**
templated — use `--namespace` / `--create-namespace` (idiomatic
Helm). RBAC is cluster-scoped and release-named, so multiple installs
don't collide.

## Common configurations

local-path with a k3s storage root:

```bash
helm install ... \
  --set localPath.hostPath=/var/lib/rancher/k3s/storage \
  --set localPath.mountPath=/host/local-path \
  --set 'localPath.storageRoots={/host/local-path}'
```

NFS only (one release per export):

```bash
helm install pv-orphan-nfs-team-a ./charts/k8s-pv-orphan-exporter \
  -n pv-orphan-exporter --create-namespace \
  --set localPath.enabled=false \
  --set nfs.enabled=true \
  --set nfs.server=nfs.example.internal \
  --set nfs.exportRoot=/export/k8s \
  --set nfs.volume.nfs.server=nfs.example.internal \
  --set nfs.volume.nfs.path=/export/k8s
```

Alerting rules (needs the Prometheus Operator CRD):

```bash
helm upgrade ... \
  --set prometheusRule.enabled=true \
  --set prometheusRule.additionalLabels.release=kube-prometheus-stack
```

## Gotchas

- **`nfs.exportRoot` is required for in-tree-NFS dangling detection.**
  An in-tree `spec.nfs.path` is server-side absolute; the exporter
  strips `exportRoot` to locate the PV under the mount. Wrong/empty
  ⇒ those PVs are silently never dangling-checked (CSI `subDir` PVs
  are unaffected). `NOTES.txt` warns when it's unset.
- **`nfs.server` / `nfs.volume.nfs.server` are different knobs.**
  The first scopes which PVs this instance owns (matches
  `spec.nfs.server`); the second is where the *exporter pod itself*
  mounts the export. They're usually the same host but conceptually
  distinct — use `nfs.volume.existingClaim` to mount via a PVC
  instead.
- **`prometheusRule.additionalLabels`** must match your operator's
  `ruleSelector` (often `release: <kube-prometheus-stack release>`)
  or the rules are silently ignored.
- **local-path permissions:** the distroless-nonroot UID (65532)
  only needs `ReadDir`; works against the 0777 dirs
  local-path-provisioner creates. Stricter roots need `chmod o+rx`
  or a root-running variant (see `deploy/README.md`).
- Run **one release per NFS export** — never `nfs.replicaCount > 1`
  (it double-counts identical cluster-wide series).

See [`docs/design.md`](../../docs/design.md) for the architecture and
[`deploy/README.md`](../../deploy/README.md) for the alerting model.

## Values

See [`values.yaml`](./values.yaml) — every key is commented inline.

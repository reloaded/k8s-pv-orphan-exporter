# Deployment manifests

Plain `kubectl apply -f` manifests for the exporter. The Phase 4
Helm chart will parameterise these; until then, edit the YAML in
place to match your cluster.

## Layout

| File | When to use |
|------|-------------|
| `local-path-daemonset.yaml` | local-path / hostPath PVs — runs one pod per node, mounts the storage root read-only via `hostPath`. |

NFS-backed deployment (`deployment-nfs.yaml`) lands in Phase 3.

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

# Deployment manifests

Plain `kubectl apply -f` manifests for the exporter. The Phase 4
Helm chart will parameterise these; until then, edit the YAML in
place to match your cluster.

## Layout

| File | When to use |
|------|-------------|
| `local-path-daemonset.yaml` | local-path / hostPath PVs — runs one pod per node, mounts the storage root read-only via `hostPath`. |
| `deployment-nfs.yaml` | NFS PVs (in-tree, nfs-subdir-external-provisioner, or `nfs.csi.k8s.io`) — runs one replica per export, mounts the export read-only. |

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

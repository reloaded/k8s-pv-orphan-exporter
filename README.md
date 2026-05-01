# k8s-pv-orphan-exporter

Prometheus exporter that detects orphaned Kubernetes PersistentVolumes and unreferenced storage directories across local-path and NFS backends.

> Status: pre-alpha. Bootstrap in progress.

## What it does

Surfaces two kinds of storage drift between a Kubernetes cluster and the disks behind it:

- **Dangling PVs** — a `PersistentVolume` exists in the API, but its backing directory does not exist on the host or NFS export it points to.
- **Orphaned directories** — a directory exists under a known storage root, but no `PersistentVolume` references it.

Both indicate a failed reclaim, a manually deleted PV with the finalizer stripped, or a manually created folder that the cluster no longer knows about. They tend to fail silently and slowly fill disks.

See [`docs/design.md`](docs/design.md) for the full design.

## License

TBD.

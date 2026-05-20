# `ci/` — chart-testing values overlays

Each `*-values.yaml` is rendered through `helm lint` + `helm template`
by `.github/workflows/chart-ci.yml`'s matrix. The set is intentionally
small but covers every meaningful values-driven branch in the
templates:

| Overlay                            | Covers                                                                                                                  |
| ---------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| (no overlay — `values.yaml`)       | The default install: local-path DaemonSet only, NFS off, PrometheusRule off.                                            |
| `nfs-only-values.yaml`             | NFS Deployment with an inline NFS volume; local-path off. Exercises in-tree NFS path-rewrite knobs.                     |
| `nfs-existing-claim-values.yaml`   | NFS Deployment with `volume.existingClaim` instead of an inline NFS volume — the `volumeClaim` branch.                  |
| `both-scanners-values.yaml`        | Local-path + NFS together + PrometheusRule with `additionalLabels` — exercises every template at once.                  |
| `rule-only-values.yaml`            | Both scanners off, PrometheusRule on. Confirms SA + RBAC still render (cluster-wide informer would still be in scope).  |
| `nfs-missing-volume-values.yaml`   | Negative — `nfs.enabled=true` with no `volume.nfs` and no `existingClaim`. The `required` guard MUST fail the render.   |

Add a new overlay any time a values branch is added and the existing
overlays don't exercise it. The matrix is fast (each render is sub-
second), so it's cheap to grow.

The naming convention `<scenario>-values.yaml` matches what
`helm/chart-testing` (`ct`) expects, so this directory is reusable if
we ever wire `ct lint` in alongside the raw `helm lint`/`template`
matrix.

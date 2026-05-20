# Project: k8s-pv-orphan-exporter

A Prometheus exporter, written in Go, that detects orphaned Kubernetes
PersistentVolumes and unreferenced storage directories on the disks behind
them. Initial backends: local-path (host-path) and NFS.

See [`docs/design.md`](docs/design.md) for the full design and architecture.

## Project metadata

- **Go module path:** `github.com/reloaded/k8s-pv-orphan-exporter`.
- **License:** Apache-2.0. See [`LICENSE`](LICENSE). New source files should carry the standard short Apache-2.0 header.
- **Container images:** published to `ghcr.io/reloaded/k8s-pv-orphan-exporter`. Tags: `latest` for `main`, `vX.Y.Z` for tagged releases, `sha-<short>` for every push.
- **Default repo branch:** `main`.

## Current state of the repo

What is in the repo today:

- Devcontainer (`.devcontainer/`) — Go toolchain + supporting CLI tools.
- Agent guidance (`CLAUDE.md` — this file).
- Concurrent-development convention (`docs/worktrees.md`).
- v0 architecture and phased implementation plan (`docs/design.md`).
- License (`LICENSE`), `.gitignore`, `.gitattributes`, `.vscode/`.
- A short `README.md` with a "Getting started" section.
- **Phase 1 skeleton:**
  - `go.mod` / `go.sum` (module path `github.com/reloaded/k8s-pv-orphan-exporter`).
  - `cmd/k8s-pv-orphan-exporter/` — `main` wiring the full pipeline.
  - `internal/{version,inventory,scanner,scanner/localpath,diff,metrics,k8s}/` packages.
  - Operational metrics (`build_info`, `up`, `scan_duration_seconds`, `scan_errors_total`, `last_scan_timestamp_seconds`, `pv_inventory_size`).
  - Diff engine with table-driven tests in `internal/diff/`.
  - `Dockerfile` (multi-stage, distroless runtime).
  - **CI/CD** under `.github/workflows/`:
    - `ci.yml` — lint + `go test -race` + `go build` + `docker build` on every PR and every push to `main`. Intended to be a required status check (configured via repo branch protection, not the workflow file itself).
    - `release.yml` — on `v*` tags, builds a multi-arch (`linux/amd64`, `linux/arm64`) image and pushes to `ghcr.io/reloaded/k8s-pv-orphan-exporter` tagged `vX.Y.Z`, `latest`, and `sha-<short>`.
    - `nightly.yml` — daily `unit` (`-count=2`) and `integration` (`-tags=integration`) test passes.
- **Phase 2 (local-path):**
  - `internal/inventory/from_corev1.go` — `FromPV` translating `corev1.PersistentVolume` (Local + nodeAffinity, hostPath, in-tree NFS, `nfs.csi.k8s.io` subDir) into the engine's `PVRef`.
  - `internal/inventory/inventory.go` — thread-safe `Inventory` with Set/Delete/Snapshot/SizeByBackend.
  - `internal/k8s/pvhandler.go` — `RegisterPVHandler` wires the PV informer's Add/Update/Delete events into the inventory; handles `cache.DeletedFinalStateUnknown`.
  - `internal/scanner/localpath/localpath.go` — real walker: `os.ReadDir` + `os.Lstat`, skip symlinks, exclude basenames, cross-fs boundary via `syscall.Stat_t.Dev` (per-platform via `device_unix.go` / `device_other.go`), recursive descent bounded by `--scan.max-depth` (default 2). Ancestor-aware orphan classification in `internal/diff` suppresses entries that are children of a known PV directory or of an already-reported orphan.
  - `internal/diff/diff.go` — `ExpectedPath.Node == ""` is now a wildcard for hostPath; `ScanResult.Roots` filters expected paths to those under a configured root.
  - `internal/grace/grace.go` — `Tracker.Step` implements design.md §5.2 grace gating; resets on disappearance.
  - `internal/metrics/aggregate.go` — the four cardinality-bounded gauge vectors (`dangling_pvs`, `orphaned_directories`, `archived_directories`, `released_pvs_retained`); `Publish` resets the per-(backend, node) slice cleanly between scans via `DeletePartialMatch`.
  - `cmd/k8s-pv-orphan-exporter/main.go` — full pipeline: informer → inventory → scan → diff → grace → aggregate publish, with new flags `--scan.grace-period`, `--scanner.local-path.exclude`, `--scanner.local-path.cross-fs`, `--k8s.sync-timeout`.
  - `deploy/local-path-daemonset.yaml` — Namespace + ServiceAccount + ClusterRole (PVs only) + ClusterRoleBinding + DaemonSet (hostPath read-only, NODE_NAME, tolerates every taint, distroless-nonroot, readOnlyRootFilesystem) + headless Service. `deploy/README.md` documents quick-start and the storage-root / permissions trade-offs.
  - `internal/integration/local_path_test.go` — `//go:build integration` end-to-end test against `fake.NewClientset` + `t.TempDir()`, exercising both initial sync and the watch path (PV created after first scan).
- **Phase 3 (NFS):**
  - `internal/scanner/nfs/nfs.go` — `NFSScanner`: a near-twin of the local-path walker over a single `--scanner.nfs.mount-path` root, emitting an empty-`Node` `ScanResult` (NFS is cluster-wide), and tagging `--scanner.nfs.archived-prefix` directories as `Archived` (design.md §5.4). Per-package `device_{unix,other}.go` copies for the cross-fs check.
  - `internal/inventory/from_corev1.go` — `FromPV` now takes an `inventory.Config`; `NFSConfig{MountPath,ExportRoot,Server}` rewrites a PV's server-side path (in-tree `spec.nfs.path` minus export-root; `nfs.csi.k8s.io` `subDir`) to the path the scanner observes under its mount, and drops PVs whose server is out of scope (issue #6). `RegisterPVHandler` threads the config through.
  - `cmd/k8s-pv-orphan-exporter/main.go` — `--scanner.nfs.{enabled,mount-path,server,export-root,archived-prefix,exclude,cross-fs}` flags; NFS scanner appended to the scan loop when enabled.
  - `deploy/deployment-nfs.yaml` — single-replica Deployment, read-only `nfs` volume, self-contained shared RBAC. `deploy/README.md` documents the arg/volume agreement.
  - `internal/integration/nfs_test.go` — `//go:build integration` end-to-end NFS test (in-tree + CSI PVs, dangling/orphaned/archived, issue-#6 path rewrite, watch path).
- **Phase 4 (release polish — in progress):**
  - `deploy/prometheus-rules.yaml` — Prometheus Operator `PrometheusRule` with the design.md §9.4 alerts: `PVOrphanExporterScanStalled` (issue #7 — the supported scan-stall detector, deliberately not the liveness probe), `KubernetesDanglingPV`, `KubernetesOrphanedStorageDirectories`. `deploy/README.md` §Alerting documents the operator-selector and threshold-vs-scan-interval caveats. No Go changes; metric names verified against `internal/metrics`.
  - `charts/k8s-pv-orphan-exporter/` — Helm chart parameterising both topologies + the PrometheusRule (Chart.yaml, values.yaml, `_helpers.tpl`, SA/RBAC/DaemonSet/Deployment/Services/PrometheusRule templates, NOTES.txt, chart README). Namespace is NOT templated (idiomatic `--create-namespace`); cluster-scoped RBAC is release-named. Defaults reproduce `deploy/local-path-daemonset.yaml` (local-path on, NFS+rule off). Validated with `helm lint` + `helm template` across all toggles; no Go changes. Raw `deploy/*.yaml` retained as Helm-free path + diff reference.
  - `.goreleaser.yaml` + `release.yml` `binaries` job — Goreleaser config cross-builds `linux/darwin × amd64/arm64` tarballs, generates `checksums.txt`, and creates the GitHub Release on `v*` tags. ldflags mirror the Dockerfile's so binary + image report the same `version.Version/Revision/Branch`. Runs in parallel with the existing image job, no docker overlap. Pinned to `goreleaser-action` `~> v2.5` (the latest v2 needs Go ≥1.26 while CI is on 1.25).

What is **not** here yet (do not assume any of this exists — it is on the roadmap in `docs/design.md`):

- No real `kind` integration test — Phases 2 & 3 integration tests use `fake.NewClientset` + `t.TempDir()`; a real `kind` + sidecar-NFS variant (design.md §13) is future work. Nightly's `integration` job runs the fake variants today.
- No cluster-wide inventory collector — issue #4 (per-DaemonSet informer dedup + cluster-owned `released_pvs_retained`) is deferred; the `Released` gauge stays registered but unpublished.
- Phase 4 remaining: no Grafana dashboard (separate open draft PR). (Prometheus alerting rules + Helm chart + Goreleaser: done.)
- No `Makefile` or `taskfile`.
- No `.golangci.yml` lint config (CI uses golangci-lint v2 defaults).

When starting a task, the **first thing to do** is read `docs/design.md` and pick the lowest-numbered phase whose work is not yet done. Phases 1–3 are landed and Phase 4 is in progress (Prometheus alerting rules + Helm chart done; Grafana dashboard and Goreleaser remain). Issues #4 (cluster-wide collector), #5 (`filepath.Clean` normalisation), #8 (Update/Delete integration coverage) remain open out of band.

## Tech stack

- **Language:** Go (latest stable; the devcontainer tracks `mcr.microsoft.com/devcontainers/go:1`).
- **Metrics library:** `github.com/prometheus/client_golang`.
- **Kubernetes client:** `k8s.io/client-go`.
- **Logging:** `log/slog` (stdlib).
- **CLI / flags:** `github.com/alecthomas/kingpin/v2` (Prometheus-ecosystem standard) or stdlib `flag` — pick one and stick to it.
- **Lint:** `golangci-lint` (config in `.golangci.yml` once added).
- **Format:** `gofumpt` + `goimports`.

## Commit style

- Imperative mood, 1–2 sentence summary.
- Do **not** add `Co-Authored-By` lines to commit messages.
- Prefer one commit per logical task. Don't split per-file.
- Author identity: `Jason Harris <1337reloaded@gmail.com>`.

## Git workflow

- **Never commit directly to `main`. Never push to `main`. All work goes on a `workitem/` feature branch.** This is absolute.
- Create a `workitem/<short-name>` branch before making any changes (e.g., `workitem/add-local-path-scanner`).
- Each logical task should land as **one commit** on the feature branch.
- When a task is complete, you MUST immediately:
  1. Stage the changed files.
  2. Commit on the feature branch with a concise message.
  3. Push the commit to the remote.
  4. Create a **draft** PR via `gh pr create --draft` if one does not already exist.
- Do not wait to be asked to commit/push.
- **Always create PRs as drafts.** The author marks them ready when appropriate. Subsequent pushes must not change the PR's draft/published status.
- PRs are squash-merged, so the PR title becomes the final commit message — write a clear, concise PR title.
- The PR body should summarize all changes so a reviewer can understand the full scope without reading every commit.
- Do not merge PRs — leave them for review and merge by the author.

## Concurrent work with worktrees

Multiple Claude Code instances (or developers) can work in parallel on separate
tasks. Each instance **must** use a git worktree to avoid conflicts.

See [`docs/worktrees.md`](docs/worktrees.md) for the full pattern.

At the start of a task, create an isolated worktree for the feature branch:

```bash
git worktree add .claude/worktrees/<branch-slug> -b workitem/<short-name>
```

`.claude/` is gitignored, so worktrees do not show up as untracked files in the
main checkout.

## Code conventions

- **Format:** `gofumpt` over `gofmt`. Run `goimports` for import grouping.
- **Lint:** `golangci-lint run ./...` must pass before pushing.
- **Tests:** Table-driven where it makes sense. Use `testing.T.Run` for subtests so output is grep-friendly.
- **Errors:** Wrap with `fmt.Errorf("...: %w", err)`. Define sentinel errors only when callers actually need to match on them.
- **Logging:** `slog` with structured key/value pairs. No `fmt.Println` in library code.
- **Context:** Pass `context.Context` as the first parameter through call chains that hit I/O or k8s APIs.
- **No init() functions** for behavior beyond registering global metric collectors. Configuration belongs in `main`.
- **Package layout:**
  - `cmd/k8s-pv-orphan-exporter/` — `main` entry point only.
  - `internal/scanner/` — backend scanners (local-path, NFS, ...).
  - `internal/k8s/` — Kubernetes client wrappers.
  - `internal/metrics/` — Prometheus collectors.
  - Public packages under `pkg/` only if there is a clear external consumer.

## Build, test, lint

Once the Go module exists:

```bash
go build ./...
go test -race ./...
golangci-lint run ./...
```

## Documentation conventions

When writing or updating docs in `docs/`:

- **Document the "why" and the gotchas, not the "what".** The code shows what happens. Docs should explain why a pattern was chosen and what breaks if you do it differently.
- **Don't restate things the source already says clearly.** If a function name and signature explain the behavior, don't write a doc that paraphrases it.
- **Link, don't duplicate.** If another doc covers a topic, link to it with a one-line summary.
- **Keep cross-cutting concerns in `docs/`. Keep package-level details in package doc comments.**

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
  - `cmd/k8s-pv-orphan-exporter/` — `main` with flag parsing and `/metrics` server.
  - `internal/{version,inventory,scanner,scanner/localpath,diff,metrics,k8s}/` packages.
  - The `LocalPathScanner` is a stub — it returns hardcoded data; the real walker lands in Phase 2.
  - Operational metrics (`build_info`, `up`, `scan_duration_seconds`, `scan_errors_total`, `last_scan_timestamp_seconds`, `pv_inventory_size`).
  - Diff engine with table-driven tests in `internal/diff/`.
  - `Dockerfile` (multi-stage, distroless runtime).
  - **CI/CD** under `.github/workflows/`:
    - `ci.yml` — lint + `go test -race` + `go build` + `docker build` on every PR and every push to `main`. Intended to be a required status check (configured via repo branch protection, not the workflow file itself).
    - `release.yml` — on `v*` tags, builds a multi-arch (`linux/amd64`, `linux/arm64`) image and pushes to `ghcr.io/reloaded/k8s-pv-orphan-exporter` tagged `vX.Y.Z`, `latest`, and `sha-<short>`.
    - `nightly.yml` — daily `unit` (`-count=2`) and `integration` (`-tags=integration`) test passes; the integration job is wired now so Phase 2's kind-based tests start running automatically when added.

What is **not** here yet (do not assume any of this exists — it is on the roadmap in `docs/design.md`):

- No real disk-walking scanner — Phase 2.
- No NFS scanner — Phase 3.
- No Helm chart, no Kubernetes manifests, no `deploy/`.
- No `Makefile` or `taskfile`.
- No `.golangci.yml` lint config (CI uses golangci-lint v2 defaults).
- No Prometheus alerting rules, no Grafana dashboards.
- No Goreleaser config (Phase 4).

When starting a task, the **first thing to do** is read `docs/design.md` and pick the lowest-numbered phase whose work is not yet done. With Phase 1 landed, Phase 2 (real `LocalPathScanner` walker) is the obvious next step.

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

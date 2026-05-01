# k8s-pv-orphan-exporter

Prometheus exporter that detects orphaned Kubernetes PersistentVolumes and unreferenced storage directories across local-path and NFS backends.

> Status: pre-alpha. Bootstrap in progress.

## What it does

Surfaces two kinds of storage drift between a Kubernetes cluster and the disks behind it:

- **Dangling PVs** — a `PersistentVolume` exists in the API, but its backing directory does not exist on the host or NFS export it points to.
- **Orphaned directories** — a directory exists under a known storage root, but no `PersistentVolume` references it.

Both indicate a failed reclaim, a manually deleted PV with the finalizer stripped, or a manually created folder that the cluster no longer knows about. They tend to fail silently and slowly fill disks.

See [`docs/design.md`](docs/design.md) for the full design.

## Getting started

This repo is intended to be developed inside its devcontainer. The devcontainer pre-installs the Go toolchain, `golangci-lint`, `goimports`, `gofumpt`, `dlv`, `kubectl`, `helm`, `gh`, and Claude Code, so a fresh clone needs no host-level setup beyond Docker and a devcontainer-aware editor.

### Prerequisites

- Docker (Docker Desktop on macOS/Windows, Docker Engine on Linux).
- VSCode with the [Dev Containers](https://marketplace.visualstudio.com/items?itemName=ms-vscode-remote.remote-containers) extension, or another editor that understands the [devcontainer spec](https://containers.dev/).

### First-time setup

1. **Clone:**
   ```bash
   git clone https://github.com/reloaded/k8s-pv-orphan-exporter.git
   cd k8s-pv-orphan-exporter
   ```
2. **Open in devcontainer:** in VSCode, run *"Dev Containers: Reopen in Container"*. The container builds and `postCreateCommand.sh` installs the Go tooling. First build takes a few minutes; subsequent opens are fast.
3. **Authenticate the GitHub CLI** (only needed once per devcontainer volume):
   ```bash
   gh auth login
   ```
   Choose HTTPS and a token with `repo` and `workflow` scopes. Without this, `git push` and `gh pr create` will fail with credential errors.
4. **Read the agent guidance:** [`CLAUDE.md`](CLAUDE.md) covers the development workflow, conventions, and how to start a task. [`docs/design.md`](docs/design.md) is the v0 architecture and implementation plan.

### Workflow at a glance

- Always work on a `workitem/<short-name>` branch — never commit directly to `main`.
- Always open PRs as **draft** (`gh pr create --draft`); the maintainer marks them ready.
- Concurrent work uses git worktrees — see [`docs/worktrees.md`](docs/worktrees.md).

## License

[Apache License 2.0](LICENSE).

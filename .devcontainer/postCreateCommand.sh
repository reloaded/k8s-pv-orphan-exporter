#!/bin/bash
# .devcontainer/postCreateCommand.sh
#
# Runs once after the devcontainer is created. Keep this idempotent — it also
# runs on container rebuild.

set -euo pipefail

: "${WORKSPACE_DIR:=/workspaces/k8s-pv-orphan-exporter}"

echo "==> Ensuring vscode owns the workspace and ~/.claude"
sudo chown -R vscode:vscode "$WORKSPACE_DIR"
sudo chown -R vscode:vscode "${HOME}/.claude" 2>/dev/null || true

echo "==> Installing Go developer tooling"
# golangci-lint: linter aggregator (use the official install script for a pinned, recent version).
GOLANGCI_LINT_VERSION="v1.62.2"
if ! command -v golangci-lint >/dev/null 2>&1 || \
   [[ "$(golangci-lint version --format short 2>/dev/null || true)" != "${GOLANGCI_LINT_VERSION#v}" ]]; then
  curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh \
    | sh -s -- -b "$(go env GOPATH)/bin" "${GOLANGCI_LINT_VERSION}"
fi

# Common Go tools — install via `go install` so they end up in $GOPATH/bin.
go install golang.org/x/tools/cmd/goimports@latest
go install mvdan.cc/gofumpt@latest
go install github.com/go-delve/delve/cmd/dlv@latest

echo "==> Pre-fetching Go module dependencies (if go.mod exists)"
if [ -f "$WORKSPACE_DIR/go.mod" ]; then
  (cd "$WORKSPACE_DIR" && go mod download)
fi

echo "==> Done. Run 'go build ./...' or 'make' to get started."

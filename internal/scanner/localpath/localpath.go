// Copyright 2026 Jason Harris
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package localpath implements scanner.Scanner for the
// local-path / hostPath backend.
//
// The walker enumerates direct children of each configured storage
// root: for local-path-provisioner and the in-tree hostPath
// provisioner, every per-PV directory lives exactly one level under
// the root (`/opt/local-path-provisioner/pvc-<uuid>_<ns>_<name>`),
// so a single ReadDir per root gives us every candidate entry. The
// `--scan.max-depth` flag is plumbed in Config for future scanners
// (CSI drivers that nest one or more provisioner-layout levels) but
// is not used by this implementation.
package localpath

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner"
)

// Backend is the value of the `backend` Prometheus label for this scanner.
const Backend = "local-path"

// Config configures the local-path scanner.
//
// All fields default to safe values: an empty StorageRoots list
// makes Scan a no-op, an empty Excludes list lets every basename
// through, and CrossFS=false stays inside the root's filesystem.
type Config struct {
	// StorageRoots are absolute paths to scan. Each is enumerated
	// independently; an unreadable root logs a warning and is
	// skipped, the others continue.
	StorageRoots []string
	// Excludes is a set of basenames to skip — typically
	// "lost+found", ".snapshot", ".zfs". Match is exact, not glob.
	Excludes []string
	// NodeName is the value of the `node` label on emitted
	// ScanResult / metrics. Read from the downward API by main.
	NodeName string
	// CrossFS controls whether entries on a different filesystem
	// than the root are followed. Default false: a mountpoint
	// inside the root (kernel-mounted CSI volume, NFS sub-export)
	// is skipped because its contents belong to a different
	// scanner.
	CrossFS bool
}

// Scanner walks the configured storage roots on the local node.
type Scanner struct {
	cfg Config
}

// New constructs a Scanner. Configuration is not validated here —
// invalid roots are reported per-scan so a transient
// permissions / mount problem doesn't crash the exporter at startup.
func New(cfg Config) *Scanner {
	return &Scanner{cfg: cfg}
}

// Backend returns the stable backend identifier.
func (s *Scanner) Backend() string { return Backend }

// Scan enumerates every directory entry that is a direct child of
// any configured storage root, honouring excludes, symlink
// avoidance, and the cross-filesystem boundary.
//
// Errors reading an individual root are logged and skipped; only a
// context-cancellation or timeout is returned as an error so the
// caller can record it under scan_errors_total.
func (s *Scanner) Scan(ctx context.Context) (*scanner.ScanResult, error) {
	excludes := excludeSet(s.cfg.Excludes)

	result := &scanner.ScanResult{
		Backend: Backend,
		Node:    s.cfg.NodeName,
		Roots:   append([]string(nil), s.cfg.StorageRoots...),
	}

	for _, root := range s.cfg.StorageRoots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := s.scanRoot(ctx, root, excludes)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			slog.WarnContext(ctx, "local-path scan: skipping root",
				"root", root, "err", err)
			continue
		}
		result.Entries = append(result.Entries, entries...)
	}

	return result, nil
}

// scanRoot returns the direct-child directory entries of root. It
// returns ctx errors verbatim so the outer Scan loop can propagate
// them; everything else is wrapped with the root path for context.
func (s *Scanner) scanRoot(ctx context.Context, root string, excludes map[string]struct{}) ([]scanner.Entry, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("lstat root %q: %w", root, err)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("root %q is not a directory", root)
	}
	rootDev, rootDevOK := deviceID(rootInfo)

	dirEntries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("readdir %q: %w", root, err)
	}

	out := make([]scanner.Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := de.Name()
		if _, skip := excludes[name]; skip {
			continue
		}

		full := filepath.Join(root, name)
		info, err := os.Lstat(full)
		if err != nil {
			slog.WarnContext(ctx, "local-path scan: lstat failed",
				"path", full, "err", err)
			continue
		}

		// Symlinks: record by name but never traverse. This
		// matches design.md §11 — we deliberately do not
		// follow links out of the scanned root.
		if info.Mode()&os.ModeSymlink != 0 {
			out = append(out, scanner.Entry{Path: full, BaseName: name})
			continue
		}

		if !info.IsDir() {
			// Non-directory at depth 1 isn't a PV layout we
			// understand. Skip silently rather than count as
			// an orphan.
			continue
		}

		if !s.cfg.CrossFS && rootDevOK {
			if dev, ok := deviceID(info); ok && dev != rootDev {
				// Mountpoint inside the root: a different
				// volume or filesystem starts here.
				continue
			}
		}

		out = append(out, scanner.Entry{Path: full, BaseName: name})
	}
	return out, nil
}

func excludeSet(exclude []string) map[string]struct{} {
	if len(exclude) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(exclude))
	for _, e := range exclude {
		if e == "" {
			continue
		}
		out[e] = struct{}{}
	}
	return out
}

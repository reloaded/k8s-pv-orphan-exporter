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
// The walker enumerates every directory entry from depth 1 to
// Config.MaxDepth under each configured storage root. For
// local-path-provisioner and the in-tree hostPath provisioner, every
// per-PV directory lives exactly one level under the root
// (`/opt/local-path-provisioner/pvc-<uuid>_<ns>_<name>`), so a depth
// of 1 is sufficient for the typical layout. The flag's default of 2
// (design.md §11) gives one extra level of headroom for CSI drivers
// that nest a provisioner layer under the per-PV directory.
//
// Deeper entries don't false-positive as orphans: the diff engine
// performs ancestor-aware classification — an observed directory
// whose parent is in the PV inventory's expected paths is suppressed.
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
	// MaxDepth bounds how deep under each storage root the walker
	// descends. MaxDepth=1 means direct children only (the
	// per-PV directory layer for local-path-provisioner);
	// MaxDepth=2 catches one level of nested provisioner layout.
	// MaxDepth<=0 emits no entries — useful for tests but a
	// surprising default in production, so main.go always passes
	// the explicit flag value.
	MaxDepth int
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
		// Clean each root so the diff engine's prefix check is
		// trailing-slash / double-slash agnostic (issue #5).
		Roots: cleanRoots(s.cfg.StorageRoots),
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

// scanRoot returns directory entries under root, walking from depth
// 1 to s.cfg.MaxDepth. It returns ctx errors verbatim so the outer
// Scan loop can propagate them; everything else is wrapped with the
// root path for context.
func (s *Scanner) scanRoot(ctx context.Context, root string, excludes map[string]struct{}) ([]scanner.Entry, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("lstat root %q: %w", root, err)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("root %q is not a directory", root)
	}
	rootDev, rootDevOK := deviceID(rootInfo)

	var out []scanner.Entry
	if err := s.walk(ctx, root, 1, rootDev, rootDevOK, excludes, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// walk recurses under current, appending one Entry per directory
// (and per symlink) it observes at depths 1..MaxDepth. A directory
// at the boundary depth is still emitted; its contents are not
// enumerated.
func (s *Scanner) walk(
	ctx context.Context,
	current string,
	depth int,
	rootDev uint64,
	rootDevOK bool,
	excludes map[string]struct{},
	out *[]scanner.Entry,
) error {
	if depth > s.cfg.MaxDepth {
		return nil
	}
	dirEntries, err := os.ReadDir(current)
	if err != nil {
		return fmt.Errorf("readdir %q: %w", current, err)
	}

	for _, de := range dirEntries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := de.Name()
		if _, skip := excludes[name]; skip {
			continue
		}

		full := filepath.Join(current, name)
		info, err := os.Lstat(full)
		if err != nil {
			slog.WarnContext(ctx, "local-path scan: lstat failed",
				"path", full, "err", err)
			continue
		}

		// Symlinks: record by name but never traverse. This
		// matches design.md §11 — we deliberately do not follow
		// links out of the scanned root.
		if info.Mode()&os.ModeSymlink != 0 {
			*out = append(*out, scanner.Entry{Path: full, BaseName: name})
			continue
		}

		if !info.IsDir() {
			// A regular file at any depth isn't part of the
			// per-PV directory layout. Skip silently rather
			// than count as an orphan.
			continue
		}

		if !s.cfg.CrossFS && rootDevOK {
			if dev, ok := deviceID(info); ok && dev != rootDev {
				// Mountpoint inside the root: a different
				// volume or filesystem starts here.
				continue
			}
		}

		*out = append(*out, scanner.Entry{Path: full, BaseName: name})

		if depth < s.cfg.MaxDepth {
			if err := s.walk(ctx, full, depth+1, rootDev, rootDevOK, excludes, out); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				slog.WarnContext(ctx, "local-path scan: descend failed",
					"path", full, "err", err)
			}
		}
	}
	return nil
}

// cleanRoots returns a Cleaned copy of roots (filepath.Clean each
// non-empty entry; empty entries are dropped so callers can't be
// surprised by Clean("")==".").
func cleanRoots(roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		out = append(out, filepath.Clean(r))
	}
	return out
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

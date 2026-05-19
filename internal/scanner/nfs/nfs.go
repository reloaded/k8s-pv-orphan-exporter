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

// Package nfs implements scanner.Scanner for the NFS backend
// (in-tree spec.nfs PVs, nfs-subdir-external-provisioner, and the
// nfs.csi.k8s.io CSI driver).
//
// Unlike local-path, NFS is a single cluster-wide export rather than
// a per-node store, so the scanner walks one locally-mounted root
// (--scanner.nfs.mount-path) and emits a ScanResult with an empty
// Node — the diff engine then matches every NFS PV against it
// regardless of node. design.md §6.2 deploys one exporter instance
// per export; running several exports means several instances, not
// several roots in one process.
//
// The walker is deliberately a near-twin of the local-path walker
// (same depth/symlink/cross-fs/exclude semantics) with one addition:
// it understands the nfs-subdir-external-provisioner "archived-"
// prefix convention (design.md §5.4) and tags those entries so the
// diff engine reports them as archived rather than orphaned.
package nfs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner"
)

// Backend is the value of the `backend` Prometheus label for this scanner.
const Backend = "nfs"

// Config configures the NFS scanner.
//
// The zero value scans nothing (empty MountPath), which keeps Scan a
// safe no-op until --scanner.nfs.enabled wires a real mount in.
type Config struct {
	// MountPath is the single locally-mounted NFS export root to
	// walk. It must agree with inventory.NFSConfig.MountPath so the
	// paths the diff engine compares are in the same namespace.
	MountPath string
	// Excludes is a set of basenames to skip — typically
	// ".snapshot", "lost+found". Match is exact, not glob.
	Excludes []string
	// ArchivedPrefix marks intentionally-retained directories
	// (nfs-subdir-external-provisioner renames a deleted-but-retained
	// PV directory to "archived-<...>"). An entry whose basename has
	// this prefix is reported as archived, not orphaned. Empty
	// disables the convention (every stray dir is a plain orphan).
	ArchivedPrefix string
	// CrossFS controls whether entries on a different filesystem than
	// the mount root are descended into. Default false: a nested
	// export/mountpoint inside the root belongs to a different
	// scanner instance and is skipped.
	CrossFS bool
	// MaxDepth bounds how deep under the mount root the walker
	// descends, identically to the local-path scanner. MaxDepth<=0
	// emits no entries.
	MaxDepth int
}

// Scanner walks one mounted NFS export.
type Scanner struct {
	cfg Config
}

// New constructs a Scanner. Configuration is not validated here — an
// unreachable or not-yet-mounted export is reported per-scan rather
// than crashing the exporter at startup (NFS mounts can be slow to
// appear).
func New(cfg Config) *Scanner {
	return &Scanner{cfg: cfg}
}

// Backend returns the stable backend identifier.
func (s *Scanner) Backend() string { return Backend }

// Scan walks the mounted export root from depth 1 to MaxDepth and
// returns every directory (and symlink) observed, tagging
// archived-prefixed entries.
//
// A read error on the root itself is logged and yields an empty
// (non-error) result so a transiently-unavailable NFS mount doesn't
// spike scan_errors_total with a non-actionable signal; only context
// cancellation/timeout is returned as an error.
func (s *Scanner) Scan(ctx context.Context) (*scanner.ScanResult, error) {
	result := &scanner.ScanResult{
		Backend: Backend,
		// Node intentionally empty: NFS is cluster-wide, not
		// node-local. The diff engine treats an empty scan Node as
		// "match every PV".
		Node: "",
	}
	if s.cfg.MountPath == "" {
		return result, nil
	}
	result.Roots = []string{s.cfg.MountPath}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entries, err := s.scanRoot(ctx, s.cfg.MountPath, excludeSet(s.cfg.Excludes))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		slog.WarnContext(ctx, "nfs scan: skipping mount root",
			"root", s.cfg.MountPath, "err", err)
		return result, nil
	}
	result.Entries = entries
	return result, nil
}

// scanRoot returns directory entries under root, walking from depth 1
// to s.cfg.MaxDepth. ctx errors propagate verbatim; everything else is
// wrapped with the root path for context.
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

// walk recurses under current, appending one Entry per directory (and
// per symlink) it observes at depths 1..MaxDepth. A directory at the
// boundary depth is still emitted; its contents are not enumerated.
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
			slog.WarnContext(ctx, "nfs scan: lstat failed",
				"path", full, "err", err)
			continue
		}

		// Symlinks: record by name but never traverse (design.md
		// §11). On NFS a symlink could point anywhere on the
		// server; following it would escape the scanned export.
		if info.Mode()&os.ModeSymlink != 0 {
			*out = append(*out, s.entry(full, name))
			continue
		}

		if !info.IsDir() {
			continue
		}

		if !s.cfg.CrossFS && rootDevOK {
			if dev, ok := deviceID(info); ok && dev != rootDev {
				// A nested mountpoint inside the export: a
				// different filesystem starts here and belongs
				// to a different scanner instance.
				continue
			}
		}

		*out = append(*out, s.entry(full, name))

		if depth < s.cfg.MaxDepth {
			if err := s.walk(ctx, full, depth+1, rootDev, rootDevOK, excludes, out); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				slog.WarnContext(ctx, "nfs scan: descend failed",
					"path", full, "err", err)
			}
		}
	}
	return nil
}

// entry builds a scanner.Entry, tagging it archived when its basename
// carries the configured archived prefix.
func (s *Scanner) entry(full, base string) scanner.Entry {
	return scanner.Entry{
		Path:     full,
		BaseName: base,
		Archived: s.cfg.ArchivedPrefix != "" && strings.HasPrefix(base, s.cfg.ArchivedPrefix),
	}
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

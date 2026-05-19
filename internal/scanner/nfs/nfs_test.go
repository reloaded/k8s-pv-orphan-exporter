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

package nfs_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner/nfs"
)

func TestScan_DirectChildren(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "pvc-1", "pvc-2", ".snapshot")
	mkfile(t, filepath.Join(root, "notes.txt"))

	s := nfs.New(nfs.Config{
		MountPath: root,
		Excludes:  []string{".snapshot"},
		MaxDepth:  1,
	})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Backend != nfs.Backend {
		t.Errorf("Backend: want %q, got %q", nfs.Backend, res.Backend)
	}
	if res.Node != "" {
		t.Errorf("Node: want empty (NFS is cluster-wide), got %q", res.Node)
	}
	if len(res.Roots) != 1 || res.Roots[0] != root {
		t.Errorf("Roots: want [%q], got %v", root, res.Roots)
	}
	if got, want := basenames(res.Entries), []string{"pvc-1", "pvc-2"}; !equalStringSlices(got, want) {
		t.Errorf("entries: want %v, got %v", want, got)
	}
}

func TestScan_ArchivedPrefixTagged(t *testing.T) {
	// The subdir provisioner renames a deleted-but-retained PV
	// directory to "archived-<...>". Those must be tagged Archived
	// so the diff engine reports them separately from orphans
	// (design.md §5.4).
	root := t.TempDir()
	mkdirs(
		t, root,
		"team-a-claim-pvc-001",          // live-looking dir
		"archived-team-a-claim-pvc-000", // retained
	)

	s := nfs.New(nfs.Config{
		MountPath:      root,
		ArchivedPrefix: "archived-",
		MaxDepth:       1,
	})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	archived := map[string]bool{}
	for _, e := range res.Entries {
		archived[e.BaseName] = e.Archived
	}
	if archived["team-a-claim-pvc-001"] {
		t.Errorf("non-archived dir was tagged Archived")
	}
	if !archived["archived-team-a-claim-pvc-000"] {
		t.Errorf("archived-prefixed dir was NOT tagged Archived")
	}
}

func TestScan_EmptyArchivedPrefixTagsNothing(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "archived-foo")
	s := nfs.New(nfs.Config{MountPath: root, MaxDepth: 1})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, e := range res.Entries {
		if e.Archived {
			t.Errorf("entry %q tagged Archived with empty ArchivedPrefix", e.BaseName)
		}
	}
}

func TestScan_EmptyMountPathIsNoOp(t *testing.T) {
	s := nfs.New(nfs.Config{MaxDepth: 2})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Entries) != 0 || len(res.Roots) != 0 {
		t.Errorf("empty MountPath: want no entries/roots, got entries=%v roots=%v",
			res.Entries, res.Roots)
	}
}

func TestScan_MissingMountSkipped(t *testing.T) {
	// A not-yet-available NFS mount must not error the scan loop —
	// it returns an empty result so scan_errors_total stays clean.
	s := nfs.New(nfs.Config{
		MountPath: "/nonexistent/nfs/mount/should/not/exist",
		MaxDepth:  1,
	})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Entries) != 0 {
		t.Errorf("entries: want empty, got %v", res.Entries)
	}
}

func TestScan_DepthBoundsRespected(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "pvc-1/data/deep")

	s := nfs.New(nfs.Config{MountPath: root, MaxDepth: 2})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got, want := basenames(res.Entries), []string{"data", "pvc-1"}; !equalStringSlices(got, want) {
		t.Errorf("entries: want %v (depth-3 'deep' excluded), got %v", want, got)
	}
}

func TestScan_SymlinkRecordedNotTraversed(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "real")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	s := nfs.New(nfs.Config{MountPath: root, MaxDepth: 1})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := basenames(res.Entries); !equalStringSlices(got, []string{"linked", "real"}) {
		t.Errorf("entries: want [linked real], got %v", got)
	}
}

func TestScan_NonDirectoriesIgnored(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "pvc-real")
	mkfile(t, filepath.Join(root, "README"))
	s := nfs.New(nfs.Config{MountPath: root, MaxDepth: 1})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := basenames(res.Entries); !equalStringSlices(got, []string{"pvc-real"}) {
		t.Errorf("entries: want [pvc-real], got %v", got)
	}
}

func TestScan_ContextCancelled(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "pvc-1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := nfs.New(nfs.Config{MountPath: root, MaxDepth: 1})
	if _, err := s.Scan(ctx); err == nil {
		t.Errorf("expected context error, got nil")
	}
}

// helpers

func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", n, err)
		}
	}
}

func mkfile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func basenames(entries []scanner.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.BaseName)
	}
	sort.Strings(out)
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

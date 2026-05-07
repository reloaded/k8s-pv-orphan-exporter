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

package localpath_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner/localpath"
)

func TestScan_DirectChildren(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "pvc-foo_default_demo", "pvc-bar_kube-system_other", "lost+found")
	mkfile(t, filepath.Join(root, "stray-file.txt"))

	s := localpath.New(localpath.Config{
		StorageRoots: []string{root},
		Excludes:     []string{"lost+found"},
		NodeName:     "node-1",
	})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Backend != localpath.Backend {
		t.Errorf("Backend: want %q, got %q", localpath.Backend, res.Backend)
	}
	if res.Node != "node-1" {
		t.Errorf("Node: want node-1, got %q", res.Node)
	}
	if len(res.Roots) != 1 || res.Roots[0] != root {
		t.Errorf("Roots: want [%q], got %v", root, res.Roots)
	}

	got := basenames(res.Entries)
	want := []string{"pvc-bar_kube-system_other", "pvc-foo_default_demo"}
	if !equalStringSlices(got, want) {
		t.Errorf("entries: want %v, got %v", want, got)
	}
}

func TestScan_MultipleRoots(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	mkdirs(t, rootA, "pvc-a")
	mkdirs(t, rootB, "pvc-b")

	s := localpath.New(localpath.Config{StorageRoots: []string{rootA, rootB}})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := basenames(res.Entries)
	if !equalStringSlices(got, []string{"pvc-a", "pvc-b"}) {
		t.Errorf("entries: want [pvc-a pvc-b], got %v", got)
	}
}

func TestScan_MissingRootSkipped(t *testing.T) {
	s := localpath.New(localpath.Config{
		StorageRoots: []string{"/nonexistent/path/that/should/not/exist"},
	})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Entries) != 0 {
		t.Errorf("entries: want empty, got %v", res.Entries)
	}
}

func TestScan_DepthOneOnly(t *testing.T) {
	// A nested directory under a per-PV folder must not produce
	// its own Entry. The walker only emits direct children of
	// the configured root.
	root := t.TempDir()
	mkdirs(t, root, "pvc-foo/data", "pvc-foo/etc")
	mkdirs(t, root, "pvc-bar/etc")

	s := localpath.New(localpath.Config{StorageRoots: []string{root}})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := basenames(res.Entries)
	if !equalStringSlices(got, []string{"pvc-bar", "pvc-foo"}) {
		t.Errorf("entries: want [pvc-bar pvc-foo], got %v", got)
	}
}

func TestScan_SymlinksRecordedNotTraversed(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "real-dir")
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	s := localpath.New(localpath.Config{StorageRoots: []string{root}})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := basenames(res.Entries)
	if !equalStringSlices(got, []string{"linked", "real-dir"}) {
		t.Errorf("entries: want [linked real-dir], got %v", got)
	}
}

func TestScan_NonDirectoriesIgnored(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "pvc-real")
	mkfile(t, filepath.Join(root, "stray-file.log"))
	s := localpath.New(localpath.Config{StorageRoots: []string{root}})
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := basenames(res.Entries)
	if !equalStringSlices(got, []string{"pvc-real"}) {
		t.Errorf("entries: want [pvc-real], got %v", got)
	}
}

func TestScan_ContextCancelled(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "pvc-foo")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := localpath.New(localpath.Config{StorageRoots: []string{root}})
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

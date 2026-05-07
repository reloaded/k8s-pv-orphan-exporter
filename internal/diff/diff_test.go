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

package diff_test

import (
	"sort"
	"testing"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/diff"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/inventory"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner"
)

func TestCompute(t *testing.T) {
	tests := []struct {
		name  string
		pvs   []inventory.PVRef
		scan  scanner.ScanResult
		wantD []string // dangling PV names
		wantO []string // orphaned directory paths
		wantA []string // archived directory paths
		wantR []string // released PV names
	}{
		{
			name: "matching pv and folder, no diff",
			pvs: []inventory.PVRef{
				pv("pv-a", inventory.BackendLocalPath, "Bound", "Delete",
					expected("node-1", "/opt/lpp/pv-a")),
			},
			scan: scanResult("local-path", "node-1",
				entry("/opt/lpp/pv-a", false)),
		},
		{
			name: "missing folder produces dangling",
			pvs: []inventory.PVRef{
				pv("pv-a", inventory.BackendLocalPath, "Bound", "Delete",
					expected("node-1", "/opt/lpp/pv-a")),
			},
			scan:  scanResult("local-path", "node-1"),
			wantD: []string{"pv-a"},
		},
		{
			name:  "stray folder produces orphan",
			scan:  scanResult("local-path", "node-1", entry("/opt/lpp/stray", false)),
			wantO: []string{"/opt/lpp/stray"},
		},
		{
			name: "archived folder is archived not orphan",
			scan: scanResult("nfs", "",
				entry("/mnt/nfs/archived-foo", true)),
			wantA: []string{"/mnt/nfs/archived-foo"},
		},
		{
			name: "released pv with retain policy is released",
			pvs: []inventory.PVRef{
				pv("pv-r", inventory.BackendLocalPath, "Released", "Retain",
					expected("node-1", "/opt/lpp/pv-r")),
			},
			scan: scanResult("local-path", "node-1",
				entry("/opt/lpp/pv-r", false)),
			wantR: []string{"pv-r"},
		},
		{
			name: "released pv with delete policy is not released",
			pvs: []inventory.PVRef{
				pv("pv-d", inventory.BackendLocalPath, "Released", "Delete",
					expected("node-1", "/opt/lpp/pv-d")),
			},
			scan: scanResult("local-path", "node-1",
				entry("/opt/lpp/pv-d", false)),
		},
		{
			name: "hostpath multi-node only flags missing on the scanning node",
			pvs: []inventory.PVRef{
				pv("pv-host", inventory.BackendLocalPath, "Bound", "Delete",
					expected("node-1", "/data/pv-host"),
					expected("node-2", "/data/pv-host")),
			},
			scan:  scanResult("local-path", "node-1"),
			wantD: []string{"pv-host"},
		},
		{
			name: "different backend pv is ignored",
			pvs: []inventory.PVRef{
				pv("pv-nfs", inventory.BackendNFS, "Bound", "Delete",
					expected("", "/export/pv-nfs")),
			},
			scan: scanResult("local-path", "node-1"),
		},
		{
			name: "csi nfs pv with subdir produces dangling when folder missing",
			pvs: []inventory.PVRef{
				pv("pv-csi", inventory.BackendNFS, "Bound", "Delete",
					expected("", "/mnt/nfs/team-a/pvc-1")),
			},
			scan:  scanResult("nfs", ""),
			wantD: []string{"pv-csi"},
		},
		{
			name: "matched pv in nfs scan with archived sibling",
			pvs: []inventory.PVRef{
				pv("pv-live", inventory.BackendNFS, "Bound", "Delete",
					expected("", "/mnt/nfs/pv-live")),
			},
			scan: scanResult("nfs", "",
				entry("/mnt/nfs/pv-live", false),
				entry("/mnt/nfs/archived-old", true),
				entry("/mnt/nfs/stray", false)),
			wantA: []string{"/mnt/nfs/archived-old"},
			wantO: []string{"/mnt/nfs/stray"},
		},
		{
			name: "empty inputs yield empty result",
			scan: scanResult("local-path", "node-1"),
		},
		{
			name: "hostpath PV (empty Node) flags missing on every scanning node",
			pvs: []inventory.PVRef{
				pv("pv-hp", inventory.BackendLocalPath, "Bound", "Delete",
					expected("", "/data/pv-hp")),
			},
			scan:  scanResult("local-path", "node-1"),
			wantD: []string{"pv-hp"},
		},
		{
			name: "hostpath PV is satisfied when its directory is observed on the local node",
			pvs: []inventory.PVRef{
				pv("pv-hp", inventory.BackendLocalPath, "Bound", "Delete",
					expected("", "/data/pv-hp")),
			},
			scan: scanResultWithRoots("local-path", "node-1", []string{"/data"},
				entry("/data/pv-hp", false)),
		},
		{
			name: "expected path outside configured roots is ignored",
			pvs: []inventory.PVRef{
				pv("pv-elsewhere", inventory.BackendLocalPath, "Bound", "Delete",
					expected("node-1", "/var/elsewhere/pv-elsewhere")),
			},
			scan: scanResultWithRoots("local-path", "node-1", []string{"/opt/lpp"}),
		},
		{
			name: "expected path under configured root with trailing-slash boundary",
			pvs: []inventory.PVRef{
				pv("pv-similar", inventory.BackendLocalPath, "Bound", "Delete",
					expected("node-1", "/opt/lppextra/pv-similar")),
			},
			scan: scanResultWithRoots("local-path", "node-1", []string{"/opt/lpp"}),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := diff.Compute(tc.pvs, &tc.scan)

			if got.Backend != tc.scan.Backend {
				t.Errorf("Backend: want %q, got %q", tc.scan.Backend, got.Backend)
			}
			if got.Node != tc.scan.Node {
				t.Errorf("Node: want %q, got %q", tc.scan.Node, got.Node)
			}

			danglingNames := make([]string, 0, len(got.Dangling))
			for _, d := range got.Dangling {
				danglingNames = append(danglingNames, d.PV.Name)
			}
			assertNames(t, "dangling", tc.wantD, danglingNames)

			orphanPaths := make([]string, 0, len(got.Orphaned))
			for _, o := range got.Orphaned {
				orphanPaths = append(orphanPaths, o.Path)
			}
			assertNames(t, "orphaned", tc.wantO, orphanPaths)

			archivedPaths := make([]string, 0, len(got.Archived))
			for _, a := range got.Archived {
				archivedPaths = append(archivedPaths, a.Path)
			}
			assertNames(t, "archived", tc.wantA, archivedPaths)

			releasedNames := make([]string, 0, len(got.Released))
			for _, r := range got.Released {
				releasedNames = append(releasedNames, r.PV.Name)
			}
			assertNames(t, "released", tc.wantR, releasedNames)
		})
	}
}

// helpers below keep the table above readable.

func pv(name string, backend inventory.Backend, phase, reclaim string, paths ...inventory.ExpectedPath) inventory.PVRef {
	return inventory.PVRef{
		Name:          name,
		Backend:       backend,
		Phase:         phase,
		ReclaimPolicy: reclaim,
		ExpectedPaths: paths,
	}
}

func expected(node, path string) inventory.ExpectedPath {
	return inventory.ExpectedPath{Node: node, Path: path}
}

func entry(path string, archived bool) scanner.Entry {
	base := path
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			base = path[i+1:]
			break
		}
	}
	return scanner.Entry{Path: path, BaseName: base, Archived: archived}
}

func scanResult(backend, node string, entries ...scanner.Entry) scanner.ScanResult {
	return scanner.ScanResult{Backend: backend, Node: node, Entries: entries}
}

func scanResultWithRoots(backend, node string, roots []string, entries ...scanner.Entry) scanner.ScanResult {
	return scanner.ScanResult{Backend: backend, Node: node, Roots: roots, Entries: entries}
}

func assertNames(t *testing.T, label string, want, got []string) {
	t.Helper()
	w := append([]string(nil), want...)
	g := append([]string(nil), got...)
	sort.Strings(w)
	sort.Strings(g)
	if len(w) != len(g) {
		t.Errorf("%s: want %v, got %v", label, w, g)
		return
	}
	for i := range w {
		if w[i] != g[i] {
			t.Errorf("%s: want %v, got %v", label, w, g)
			return
		}
	}
}

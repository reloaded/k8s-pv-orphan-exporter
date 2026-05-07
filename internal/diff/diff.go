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

// Package diff joins a PV inventory with a single scanner result and
// emits the four detection sets (dangling, orphaned, archived,
// released) defined in docs/design.md §5.1.
//
// Compute is a pure function of its inputs so it can be exhaustively
// tested without touching disk or the Kubernetes API.
package diff

import (
	"strings"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/inventory"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner"
)

// Result is the four-set output for one (backend, node) pair.
type Result struct {
	Backend  string
	Node     string
	Dangling []DanglingPV
	Orphaned []OrphanedDir
	Archived []ArchivedDir
	Released []ReleasedPV
}

// DanglingPV is a PV whose expected backing directory was not found
// in the scan result. ExpectedPath is the specific path that was
// expected but missing — a hostPath PV may produce one DanglingPV per
// node it covers.
type DanglingPV struct {
	PV           inventory.PVRef
	ExpectedPath inventory.ExpectedPath
}

// OrphanedDir is a directory observed by the scanner that no PV
// references and that does not match an archived prefix.
type OrphanedDir struct {
	Path     string
	BaseName string
}

// ArchivedDir is an observed directory whose name matches the
// configured archived-prefix. These are intentionally retained by
// some provisioners and tracked separately from orphans.
type ArchivedDir struct {
	Path     string
	BaseName string
}

// ReleasedPV is a PV in phase=Released with reclaimPolicy=Retain. It
// is informational, not an orphan: the operator deliberately kept the
// data after PVC deletion.
type ReleasedPV struct {
	PV inventory.PVRef
}

// Compute joins the PV inventory with one scan result.
//
// Filtering rules:
//   - PVs whose Backend does not match scan.Backend are ignored.
//   - For node-local scans (scan.Node != ""), an ExpectedPath whose
//     Node names a different node is skipped. An ExpectedPath with
//     Node = "" is a wildcard — used for hostPath PVs that apply to
//     every node.
//   - If scan.Roots is non-empty, ExpectedPaths whose Path is not
//     under any configured root are skipped: those PVs aren't
//     covered by this scanner instance and shouldn't be flagged
//     dangling.
//
// The function is deterministic: outputs only depend on inputs.
func Compute(pvs []inventory.PVRef, scan *scanner.ScanResult) Result {
	res := Result{
		Backend: scan.Backend,
		Node:    scan.Node,
	}

	observed := make(map[string]scanner.Entry, len(scan.Entries))
	for _, e := range scan.Entries {
		observed[e.Path] = e
		if e.Archived {
			res.Archived = append(res.Archived, ArchivedDir{
				Path:     e.Path,
				BaseName: e.BaseName,
			})
		}
	}

	expected := make(map[string]struct{})
	for _, pv := range pvs {
		if string(pv.Backend) != scan.Backend {
			continue
		}
		if pv.Phase == "Released" && pv.ReclaimPolicy == "Retain" {
			res.Released = append(res.Released, ReleasedPV{PV: pv})
		}
		for _, ep := range pv.ExpectedPaths {
			if scan.Node != "" && ep.Node != "" && ep.Node != scan.Node {
				continue
			}
			if !pathUnderRoots(ep.Path, scan.Roots) {
				continue
			}
			expected[ep.Path] = struct{}{}
			if _, ok := observed[ep.Path]; !ok {
				res.Dangling = append(res.Dangling, DanglingPV{
					PV:           pv,
					ExpectedPath: ep,
				})
			}
		}
	}

	for _, e := range scan.Entries {
		if e.Archived {
			continue
		}
		if _, ok := expected[e.Path]; ok {
			continue
		}
		res.Orphaned = append(res.Orphaned, OrphanedDir{
			Path:     e.Path,
			BaseName: e.BaseName,
		})
	}

	return res
}

// pathUnderRoots returns true when path is under any of the given
// roots, treating an empty roots list as "no filter" (any path
// passes). Match is by path-component, so /opt/lpp does not match
// /opt/lppextra.
func pathUnderRoots(path string, roots []string) bool {
	if len(roots) == 0 {
		return true
	}
	for _, root := range roots {
		if path == root {
			return true
		}
		if !strings.HasSuffix(root, "/") {
			root += "/"
		}
		if strings.HasPrefix(path, root) {
			return true
		}
	}
	return false
}

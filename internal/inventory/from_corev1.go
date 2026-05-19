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

package inventory

import (
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// hostnameLabel is the well-known kubernetes.io/hostname label used by
// local-path-provisioner (and most CSI drivers) to pin a Local PV to
// a specific node via spec.nodeAffinity.
const hostnameLabel = "kubernetes.io/hostname"

// Config carries the scanner-instance facts FromPV needs to translate
// a PV's server-side path into the path the scanner actually observes
// on its own filesystem.
//
// It exists because NFS PV specs describe the path on the NFS server
// (spec.nfs.path is server-side absolute; the CSI subDir is relative
// to the share), whereas the NFSScanner walks a locally-mounted copy
// of that export. Without the mount root the diff engine would be
// comparing server-side paths against scanner-observed paths and
// every NFS PV would false-positive as dangling.
type Config struct {
	NFS NFSConfig
}

// NFSConfig mirrors the --scanner.nfs.* flags (design.md §6.2) that
// affect PV→path resolution.
//
// Zero value is safe: an empty MountPath disables the join and FromPV
// falls back to the raw server-side path (the pre-Phase-3 behaviour),
// which is correct for local-path-only deployments where NFS PVs
// produce no signal anyway.
type NFSConfig struct {
	// MountPath is where the NFS export is mounted inside the
	// scanner container (--scanner.nfs.mount-path, e.g. /mnt/nfs).
	// Empty disables NFS path rewriting.
	MountPath string
	// ExportRoot is the server-side export path
	// (--scanner.nfs.export-root, e.g. /export/k8s). It is stripped
	// from an in-tree spec.nfs.path to yield the path relative to
	// the mount. Not needed for the CSI subDir case (subDir is
	// already relative to the share).
	ExportRoot string
	// Server, when non-empty, restricts path resolution to PVs whose
	// NFS server matches (--scanner.nfs.server). A PV pointing at a
	// different server is recorded as BackendNFS but given no
	// expected path: it belongs to a different exporter instance, so
	// flagging it dangling here would be a false positive. Empty
	// Server accepts every NFS PV (single-export deployments where
	// the operator knows what they mounted).
	Server string
}

// FromPV translates a corev1.PersistentVolume into the diff engine's
// normalized PVRef.
//
// Backend resolution rules (design.md §5.3):
//   - spec.local.path             → BackendLocalPath, expected on each
//     node named in spec.nodeAffinity
//     under kubernetes.io/hostname In.
//   - spec.hostPath.path          → BackendLocalPath, expected on every
//     node (no node affinity in core k8s).
//   - spec.nfs.path               → BackendNFS, expected at
//     cfg.NFS.MountPath joined with (path minus cfg.NFS.ExportRoot).
//   - spec.csi.driver=="nfs.csi.k8s.io"
//     with volumeAttributes.subDir → BackendNFS, expected at
//     cfg.NFS.MountPath joined with subDir.
//   - anything else               → BackendUnknown, no expected paths.
//
// A PVRef with no ExpectedPaths is intentionally never flagged as
// dangling: we don't know where to look (unrecognized affinity, a
// different NFS server/export, or an unsupported CSI driver).
func FromPV(pv *corev1.PersistentVolume, cfg Config) PVRef {
	ref := PVRef{
		Name:          pv.Name,
		StorageClass:  pv.Spec.StorageClassName,
		Phase:         string(pv.Status.Phase),
		ReclaimPolicy: string(pv.Spec.PersistentVolumeReclaimPolicy),
	}
	if pv.Spec.ClaimRef != nil {
		ref.ClaimNamespace = pv.Spec.ClaimRef.Namespace
		ref.ClaimName = pv.Spec.ClaimRef.Name
	}

	switch {
	case pv.Spec.Local != nil:
		ref.Backend = BackendLocalPath
		ref.ExpectedPaths = expand(pv.Spec.Local.Path, nodesFromAffinity(pv.Spec.NodeAffinity))
	case pv.Spec.HostPath != nil:
		ref.Backend = BackendLocalPath
		ref.ExpectedPaths = []ExpectedPath{{Path: pv.Spec.HostPath.Path}}
	case pv.Spec.NFS != nil:
		ref.Backend = BackendNFS
		if p, ok := cfg.NFS.resolveInTree(pv.Spec.NFS.Server, pv.Spec.NFS.Path); ok {
			ref.ExpectedPaths = []ExpectedPath{{Path: p}}
		}
	case pv.Spec.CSI != nil && pv.Spec.CSI.Driver == "nfs.csi.k8s.io":
		ref.Backend = BackendNFS
		attrs := pv.Spec.CSI.VolumeAttributes
		if p, ok := cfg.NFS.resolveCSI(attrs["server"], attrs["subDir"]); ok {
			ref.ExpectedPaths = []ExpectedPath{{Path: p}}
		}
	default:
		ref.Backend = BackendUnknown
	}
	return ref
}

// resolveInTree maps an in-tree NFS PV's (server, server-side path) to
// the path the scanner observes under its mount, or (_, false) when
// the PV belongs to a different server/export and should produce no
// signal.
func (c NFSConfig) resolveInTree(server, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	if !c.serverMatches(server) {
		return "", false
	}
	if c.MountPath == "" {
		// No NFS scanner configured (local-path-only deployment):
		// preserve the raw server-side path. NFS PVs produce no
		// signal without an NFS scan, so this is inert.
		return path, true
	}
	rel, ok := relativeUnder(c.ExportRoot, path)
	if !ok {
		// ExportRoot unset or path not under it: we can't reliably
		// split the server-side path. Fall back to the raw path and
		// rely on the diff engine's root filter to drop it rather
		// than emit a wrong join. Operators must set
		// --scanner.nfs.export-root for in-tree dangling detection.
		return path, true
	}
	return filepath.Join(c.MountPath, rel), true
}

// resolveCSI maps an nfs.csi.k8s.io PV's (server, subDir) to the path
// the scanner observes. subDir is already relative to the share, so no
// ExportRoot strip is needed.
func (c NFSConfig) resolveCSI(server, subDir string) (string, bool) {
	if subDir == "" {
		return "", false
	}
	if !c.serverMatches(server) {
		return "", false
	}
	if c.MountPath == "" {
		return subDir, true
	}
	return filepath.Join(c.MountPath, subDir), true
}

// serverMatches reports whether a PV's NFS server is in scope for this
// exporter instance. An empty configured Server matches everything; an
// empty PV server (CSI attrs may omit it) is accepted so we don't drop
// PVs purely for lacking the optional attribute.
func (c NFSConfig) serverMatches(server string) bool {
	if c.Server == "" || server == "" {
		return true
	}
	return server == c.Server
}

// relativeUnder returns path expressed relative to root, matched on
// path-component boundaries so /export/k8s does not match
// /export/k8stra. Returns ("", false) when root is empty or path is
// not under it.
func relativeUnder(root, path string) (string, bool) {
	if root == "" {
		return "", false
	}
	root = strings.TrimRight(root, "/")
	if path == root {
		return "", true
	}
	prefix := root + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	return strings.TrimPrefix(path, prefix), true
}

// nodesFromAffinity extracts kubernetes.io/hostname In-matched node
// names from a PV's nodeAffinity.
//
// Only the local-path-provisioner shape is recognized: a
// NodeSelectorTerm whose MatchExpressions contains the hostname key
// with operator In. Anything more exotic (NotIn, Exists, label
// selectors on other keys) yields nil so the PVRef has no expected
// paths and produces no signal — the safe default for unrecognized
// affinity.
func nodesFromAffinity(aff *corev1.VolumeNodeAffinity) []string {
	if aff == nil || aff.Required == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var nodes []string
	for _, term := range aff.Required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key != hostnameLabel || expr.Operator != corev1.NodeSelectorOpIn {
				continue
			}
			for _, v := range expr.Values {
				if _, dup := seen[v]; dup {
					continue
				}
				seen[v] = struct{}{}
				nodes = append(nodes, v)
			}
		}
	}
	return nodes
}

// expand turns a path + node list into ExpectedPath entries. An empty
// node list produces nil — we will not emit an empty-node ExpectedPath
// for a Local PV because that would be interpreted as hostPath
// semantics ("expected on every node") and dangling-flag a PV whose
// affinity we just don't recognize.
func expand(path string, nodes []string) []ExpectedPath {
	if len(nodes) == 0 {
		return nil
	}
	out := make([]ExpectedPath, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, ExpectedPath{Node: n, Path: path})
	}
	return out
}

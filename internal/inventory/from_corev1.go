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
	corev1 "k8s.io/api/core/v1"
)

// hostnameLabel is the well-known kubernetes.io/hostname label used by
// local-path-provisioner (and most CSI drivers) to pin a Local PV to
// a specific node via spec.nodeAffinity.
const hostnameLabel = "kubernetes.io/hostname"

// FromPV translates a corev1.PersistentVolume into the diff engine's
// normalized PVRef.
//
// Backend resolution rules (design.md §5.3):
//   - spec.local.path             → BackendLocalPath, expected on each
//     node named in spec.nodeAffinity
//     under kubernetes.io/hostname In.
//   - spec.hostPath.path          → BackendLocalPath, expected on every
//     node (no node affinity in core k8s).
//   - spec.nfs.path               → BackendNFS, single expected path.
//   - spec.csi.driver=="nfs.csi.k8s.io"
//     with volumeAttributes.subDir → BackendNFS, single expected path
//     relative to the share root.
//   - anything else               → BackendUnknown, no expected paths.
//
// A PVRef with no ExpectedPaths is intentionally never flagged as
// dangling: we don't know where to look.
func FromPV(pv *corev1.PersistentVolume) PVRef {
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
		ref.ExpectedPaths = []ExpectedPath{{Path: pv.Spec.NFS.Path}}
	case pv.Spec.CSI != nil && pv.Spec.CSI.Driver == "nfs.csi.k8s.io":
		ref.Backend = BackendNFS
		if sub, ok := pv.Spec.CSI.VolumeAttributes["subDir"]; ok && sub != "" {
			ref.ExpectedPaths = []ExpectedPath{{Path: sub}}
		}
	default:
		ref.Backend = BackendUnknown
	}
	return ref
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

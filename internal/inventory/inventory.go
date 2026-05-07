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

// Package inventory holds the diff engine's normalized view of
// PersistentVolume objects. The k8s-facing code translates real
// corev1.PersistentVolume specs into PVRef values; the diff engine and
// its tests only ever see PVRef.
package inventory

// Backend identifies the storage backend a PV belongs to.
type Backend string

const (
	BackendLocalPath Backend = "local-path"
	BackendNFS       Backend = "nfs"
	BackendUnknown   Backend = "unknown"
)

// PVRef is the diff engine's view of a PersistentVolume. It is
// deliberately decoupled from the corev1 type so the engine can be
// unit-tested without an apimachinery dependency.
type PVRef struct {
	Name           string
	StorageClass   string
	Backend        Backend
	Phase          string
	ReclaimPolicy  string
	ClaimNamespace string
	ClaimName      string
	ExpectedPaths  []ExpectedPath
}

// ExpectedPath is one (node, path) location where a PV's backing
// directory is expected to exist on disk.
//
// Node is empty for non-node-local backends (e.g. NFS). For
// hostPath PVs (which apply to every node), one ExpectedPath is
// emitted per node so a per-node DaemonSet scanner only matches the
// entry for its own node.
type ExpectedPath struct {
	Node string
	Path string
}

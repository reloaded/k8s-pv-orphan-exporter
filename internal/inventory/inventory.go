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

import "sync"

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
// Node semantics:
//   - Node = "" with a node-local backend (e.g. hostPath PVs)
//     means the directory is expected on every node. The diff
//     engine treats an empty Node as a wildcard against the
//     scan's Node when filtering expected paths.
//   - Node != "" pins the expectation to a specific node, used by
//     local-path PVs whose nodeAffinity selects on
//     kubernetes.io/hostname.
//   - Node = "" with a non-node-local backend (NFS) is the only
//     thing that makes sense: there is just one cluster-wide
//     copy of the directory.
type ExpectedPath struct {
	Node string
	Path string
}

// Inventory is a thread-safe map of PV name to PVRef. It is fed by
// the k8s informer and read by the scan loop and the inventory_size
// metric. All methods are safe to call concurrently.
type Inventory struct {
	mu  sync.RWMutex
	pvs map[string]PVRef
}

// NewInventory returns an empty Inventory.
func NewInventory() *Inventory {
	return &Inventory{pvs: make(map[string]PVRef)}
}

// Set inserts or replaces the PVRef for ref.Name. A zero-Name ref
// is silently dropped — it would have no useful key.
func (i *Inventory) Set(ref PVRef) {
	if ref.Name == "" {
		return
	}
	i.mu.Lock()
	i.pvs[ref.Name] = ref
	i.mu.Unlock()
}

// Delete removes the PVRef with the given name, if present.
func (i *Inventory) Delete(name string) {
	i.mu.Lock()
	delete(i.pvs, name)
	i.mu.Unlock()
}

// Snapshot returns a copy of the current PVRef set as a slice. The
// returned slice is owned by the caller and never aliases the
// internal map. Order is unspecified.
func (i *Inventory) Snapshot() []PVRef {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]PVRef, 0, len(i.pvs))
	for _, ref := range i.pvs {
		out = append(out, ref)
	}
	return out
}

// SizeByBackend returns counts of PVs per backend. Backends with
// zero PVs are present in the map with value 0 if they have ever
// been counted; callers should treat absence and zero as
// equivalent.
func (i *Inventory) SizeByBackend() map[Backend]int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := map[Backend]int{
		BackendLocalPath: 0,
		BackendNFS:       0,
		BackendUnknown:   0,
	}
	for _, ref := range i.pvs {
		out[ref.Backend]++
	}
	return out
}

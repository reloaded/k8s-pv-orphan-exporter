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

// Package scanner defines the per-backend scanner interface and the
// shape of the directory observations the diff engine consumes.
//
// Each backend (local-path, NFS, …) supplies an implementation. A
// single exporter process can run multiple scanners concurrently;
// each Scan call must be self-contained and respect the supplied
// context's deadline (see --scan.timeout).
package scanner

import "context"

// Scanner observes the directories a storage backend has on disk.
type Scanner interface {
	// Backend returns a stable backend identifier (e.g. "local-path")
	// used as the value of the `backend` Prometheus label.
	Backend() string

	// Scan walks the configured storage roots and returns every
	// directory entry observed at the configured depth. It must
	// honour ctx for both cancellation and timeout.
	Scan(ctx context.Context) (*ScanResult, error)
}

// ScanResult is the output of one scanner pass.
type ScanResult struct {
	Backend string
	// Node is the node name for node-local backends (local-path,
	// hostPath); empty for cluster-wide backends like NFS.
	Node    string
	Entries []Entry
}

// Entry is one directory observed under a storage root.
//
// Archived is true when BaseName matches the configured
// archived-prefix for the backend (see design.md §5.4). Archived
// entries are reported separately rather than as orphans.
type Entry struct {
	Path     string
	BaseName string
	Archived bool
}

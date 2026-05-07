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

// Package localpath implements the scanner.Scanner for the
// local-path / hostPath backend.
//
// Phase 1 ships a stub: Scan returns hardcoded data so the rest of the
// pipeline (diff engine, metrics, /metrics endpoint, container build)
// can be exercised end-to-end without touching real disk or a real
// cluster. Phase 2 replaces the stub with a real walker.
package localpath

import (
	"context"
	"log/slog"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner"
)

// Backend is the value of the `backend` Prometheus label for this
// scanner.
const Backend = "local-path"

// Config configures the local-path scanner.
type Config struct {
	StorageRoots []string
	NodeName     string
}

// Scanner walks the configured storage roots on the local node.
type Scanner struct {
	cfg Config
}

// New constructs a Scanner. Phase 1 does not validate the storage
// roots — Phase 2 will, since they have to be real directories on
// the host.
func New(cfg Config) *Scanner {
	return &Scanner{cfg: cfg}
}

// Backend returns the stable backend identifier.
func (s *Scanner) Backend() string { return Backend }

// Scan is a Phase 1 stub. It does not touch the disk; it emits a
// single synthetic entry so the rest of the pipeline produces
// non-empty metrics. Phase 2 replaces this with filepath.WalkDir
// over s.cfg.StorageRoots.
func (s *Scanner) Scan(ctx context.Context) (*scanner.ScanResult, error) {
	slog.DebugContext(
		ctx, "local-path stub scan",
		"node", s.cfg.NodeName,
		"roots", s.cfg.StorageRoots,
	)
	return &scanner.ScanResult{
		Backend: Backend,
		Node:    s.cfg.NodeName,
		Entries: []scanner.Entry{
			{
				Path:     "/opt/local-path-provisioner/pvc-stub_default_demo",
				BaseName: "pvc-stub_default_demo",
			},
		},
	}, nil
}

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

package metrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/diff"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/inventory"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/metrics"
)

// TestAggregate_PublishDoesNotEmitReleased locks in the design
// decision (PR #3 comment thread, option b) that Released is owned
// by a future cluster-wide collector — not by the DaemonSet's per
// scan publishes — because every node sees the same Released set
// and publishing the same value from N pods inflates Prometheus
// aggregations by a factor of N.
func TestAggregate_PublishDoesNotEmitReleased(t *testing.T) {
	agg := metrics.NewAggregate()
	agg.Publish(&diff.Result{
		Backend: "local-path",
		Node:    "node-1",
		Released: []diff.ReleasedPV{
			{PV: inventory.PVRef{Name: "pv-r1", StorageClass: "local-path"}},
			{PV: inventory.PVRef{Name: "pv-r2", StorageClass: "local-path"}},
		},
	})
	if n := testutil.CollectAndCount(agg.Released); n != 0 {
		t.Errorf("released gauge: want 0 series after DaemonSet publish, got %d", n)
	}
}

func TestAggregate_PublishOrphanedArchivedCounts(t *testing.T) {
	agg := metrics.NewAggregate()
	agg.Publish(&diff.Result{
		Backend: "local-path",
		Node:    "node-1",
		Orphaned: []diff.OrphanedDir{
			{Path: "/opt/lpp/stray-1"},
			{Path: "/opt/lpp/stray-2"},
		},
		Archived: []diff.ArchivedDir{
			{Path: "/opt/lpp/archived-foo"},
		},
	})
	if got := testutil.ToFloat64(agg.Orphaned.WithLabelValues("local-path", "node-1")); got != 2 {
		t.Errorf("orphaned: want 2, got %v", got)
	}
	if got := testutil.ToFloat64(agg.Archived.WithLabelValues("local-path", "node-1")); got != 1 {
		t.Errorf("archived: want 1, got %v", got)
	}
}

func TestAggregate_PublishDanglingByStorageClass(t *testing.T) {
	agg := metrics.NewAggregate()
	agg.Publish(&diff.Result{
		Backend: "local-path",
		Node:    "node-1",
		Dangling: []diff.DanglingPV{
			{PV: inventory.PVRef{Name: "pv-a", StorageClass: "local-path"}},
			{PV: inventory.PVRef{Name: "pv-b", StorageClass: "local-path"}},
			{PV: inventory.PVRef{Name: "pv-c", StorageClass: "fast"}},
		},
	})
	if got := testutil.ToFloat64(agg.Dangling.WithLabelValues("local-path", "local-path", "node-1")); got != 2 {
		t.Errorf("dangling local-path: want 2, got %v", got)
	}
	if got := testutil.ToFloat64(agg.Dangling.WithLabelValues("local-path", "fast", "node-1")); got != 1 {
		t.Errorf("dangling fast: want 1, got %v", got)
	}
}

func TestAggregate_PublishResetsPreviousSeries(t *testing.T) {
	agg := metrics.NewAggregate()
	// First publish populates two storageclass series.
	agg.Publish(&diff.Result{
		Backend: "local-path",
		Node:    "node-1",
		Dangling: []diff.DanglingPV{
			{PV: inventory.PVRef{Name: "pv-a", StorageClass: "local-path"}},
			{PV: inventory.PVRef{Name: "pv-c", StorageClass: "fast"}},
		},
	})
	if got := testutil.CollectAndCount(agg.Dangling); got != 2 {
		t.Fatalf("dangling after first publish: want 2 series, got %d", got)
	}
	// Second publish drops "fast" entirely.
	agg.Publish(&diff.Result{
		Backend: "local-path",
		Node:    "node-1",
		Dangling: []diff.DanglingPV{
			{PV: inventory.PVRef{Name: "pv-a", StorageClass: "local-path"}},
		},
	})
	if got := testutil.CollectAndCount(agg.Dangling); got != 1 {
		t.Errorf("dangling after second publish: want 1 series (fast pruned), got %d", got)
	}
	if got := testutil.ToFloat64(agg.Dangling.WithLabelValues("local-path", "local-path", "node-1")); got != 1 {
		t.Errorf("dangling local-path after second publish: want 1, got %v", got)
	}
}

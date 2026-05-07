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

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/diff"
)

// Aggregate wraps the four cardinality-bounded gauge vectors that
// expose the diff engine's per-scan counts (design.md §9.2).
//
// Cardinality is intentionally low: the aggregate gauges never carry
// per-item labels (PV name, path). Per-item info metrics live in
// metrics.PerItem (Phase 4) and are gated behind --metrics.per-item-info.
type Aggregate struct {
	Dangling *prometheus.GaugeVec
	Orphaned *prometheus.GaugeVec
	Archived *prometheus.GaugeVec
	Released *prometheus.GaugeVec
}

// NewAggregate constructs the aggregate gauge vectors.
func NewAggregate() *Aggregate {
	return &Aggregate{
		Dangling: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "dangling_pvs",
			Help:      "Number of PVs whose backing directory was missing in the most recent scan, by backend, storageclass, and node.",
		}, []string{"backend", "storageclass", "node"}),
		Orphaned: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "orphaned_directories",
			Help:      "Number of directories observed under the storage roots that no PV references, by backend and node.",
		}, []string{"backend", "node"}),
		Archived: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "archived_directories",
			Help:      "Number of directories whose name matches the configured archived prefix, by backend and node.",
		}, []string{"backend", "node"}),
		Released: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "released_pvs_retained",
			Help:      "Number of PVs in phase=Released with reclaimPolicy=Retain, by backend and storageclass (informational).",
		}, []string{"backend", "storageclass"}),
	}
}

// Register registers every aggregate collector with r.
func (a *Aggregate) Register(r prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{a.Dangling, a.Orphaned, a.Archived, a.Released} {
		if err := r.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// Publish replaces this (backend, node) slice of the aggregate
// gauges with the counts derived from result. It is safe for one
// goroutine per (backend, node) pair to call concurrently.
//
// The released gauge is keyed by (backend, storageclass) only — node
// is not a label there because Released PVs are a cluster-wide fact,
// not a per-node observation. To avoid double-counting from a
// DaemonSet, only scans where Node == "" publish to Released. The
// caller (main) is responsible for calling PublishReleased exactly
// once per scan cycle from a non-DaemonSet vantage point; in v0
// that's the cluster-wide sweep we haven't built yet, so for now
// every scan publishes Released and DaemonSet runs will overcount.
// Phase 3 will add a dedicated cluster-wide informer-only scan that
// owns Released exclusively.
func (a *Aggregate) Publish(result *diff.Result) {
	a.Dangling.DeletePartialMatch(prometheus.Labels{"backend": result.Backend, "node": result.Node})
	a.Orphaned.DeletePartialMatch(prometheus.Labels{"backend": result.Backend, "node": result.Node})
	a.Archived.DeletePartialMatch(prometheus.Labels{"backend": result.Backend, "node": result.Node})
	a.Released.DeletePartialMatch(prometheus.Labels{"backend": result.Backend})

	type scKey struct{ sc string }
	dangling := map[scKey]int{}
	for _, d := range result.Dangling {
		dangling[scKey{d.PV.StorageClass}]++
	}
	for k, n := range dangling {
		a.Dangling.WithLabelValues(result.Backend, k.sc, result.Node).Set(float64(n))
	}

	a.Orphaned.WithLabelValues(result.Backend, result.Node).Set(float64(len(result.Orphaned)))
	a.Archived.WithLabelValues(result.Backend, result.Node).Set(float64(len(result.Archived)))

	released := map[scKey]int{}
	for _, r := range result.Released {
		released[scKey{r.PV.StorageClass}]++
	}
	for k, n := range released {
		a.Released.WithLabelValues(result.Backend, k.sc).Set(float64(n))
	}
}

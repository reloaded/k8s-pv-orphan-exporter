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

// Package metrics defines the operational Prometheus collectors that
// describe the exporter's own health (build_info, up,
// scan_duration_seconds, …).
//
// Aggregate orphan/dangling/archived/released collectors land alongside
// the diff engine wiring in Phase 2; only the always-on operational
// metrics live here in Phase 1.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/version"
)

const Namespace = "pv_orphan_exporter"

// Operational bundles the always-on operational collectors. The
// caller registers them into a prometheus.Registry exactly once.
type Operational struct {
	BuildInfo         *prometheus.GaugeVec
	Up                *prometheus.GaugeVec
	ScanDuration      *prometheus.HistogramVec
	ScanErrors        *prometheus.CounterVec
	LastScanTimestamp *prometheus.GaugeVec
	InventorySize     *prometheus.GaugeVec
}

// NewOperational constructs the operational collectors. They are not
// registered until Register is called.
func NewOperational() *Operational {
	return &Operational{
		BuildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "build_info",
			Help:      "Static build info for the exporter binary; value is always 1.",
		}, []string{"version", "revision", "branch", "goversion"}),
		Up: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "up",
			Help:      "1 if the most recent scan for this backend completed without error.",
		}, []string{"backend", "instance_id"}),
		ScanDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      "scan_duration_seconds",
			Help:      "How long each scan took, in seconds, by backend.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"backend"}),
		ScanErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      "scan_errors_total",
			Help:      "Number of scan errors observed, by backend and error kind.",
		}, []string{"backend", "error_kind"}),
		LastScanTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "last_scan_timestamp_seconds",
			Help:      "Unix timestamp of the most recent successful scan, by backend.",
		}, []string{"backend"}),
		InventorySize: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "pv_inventory_size",
			Help:      "Number of PVs the exporter is tracking, by backend.",
		}, []string{"backend"}),
	}
}

// Register registers every operational collector with r and seeds
// the build_info gauge with the values from internal/version.
func (o *Operational) Register(r prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{
		o.BuildInfo,
		o.Up,
		o.ScanDuration,
		o.ScanErrors,
		o.LastScanTimestamp,
		o.InventorySize,
	} {
		if err := r.Register(c); err != nil {
			return err
		}
	}
	o.BuildInfo.WithLabelValues(
		version.Version,
		version.Revision,
		version.Branch,
		version.GoVersion(),
	).Set(1)
	return nil
}

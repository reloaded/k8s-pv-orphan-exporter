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

// Command k8s-pv-orphan-exporter wires the exporter pipeline:
// flag parsing, slog logging, the Kubernetes informer-fed PV
// inventory, the per-backend scanners, the diff engine, the
// grace-period gate, and the Prometheus collectors served on
// /metrics.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/diff"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/grace"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/inventory"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/k8s"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/metrics"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner/localpath"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner/nfs"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("exporter failed", "err", err)
		os.Exit(1)
	}
}

// scanState bundles the per-backend grace trackers so each scan loop
// keeps stable state across iterations.
type scanState struct {
	dangling *grace.Tracker
	orphaned *grace.Tracker
}

func run(args []string) error {
	app := kingpin.New(
		"k8s-pv-orphan-exporter",
		"Prometheus exporter that detects orphaned Kubernetes PersistentVolumes and unreferenced storage directories.",
	)
	app.Version(version.Version)

	listenAddr := app.Flag("web.listen-address", "Address to listen on for HTTP scrapes.").Default(":9877").String()
	telemetryPath := app.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").String()
	logLevel := app.Flag("log.level", "Log level (debug|info|warn|error).").Default("info").Enum("debug", "info", "warn", "error")
	logFormat := app.Flag("log.format", "Log format (text|json).").Default("text").Enum("text", "json")

	kubeconfig := app.Flag("kubeconfig", "Path to kubeconfig (empty = in-cluster).").Default("").String()
	qps := app.Flag("k8s.qps", "QPS for the Kubernetes client.").Default("20").Float32()
	burst := app.Flag("k8s.burst", "Burst for the Kubernetes client.").Default("40").Int()

	scanInterval := app.Flag("scan.interval", "How often to run a backend scan.").Default("5m").Duration()
	scanTimeout := app.Flag("scan.timeout", "Per-scan timeout.").Default("2m").Duration()
	gracePeriod := app.Flag("scan.grace-period", "Hold dangling/orphaned candidates back for at least this duration before exposing them, to suppress provisioning races.").Default("5m").Duration()
	scanMaxDepth := app.Flag("scan.max-depth", "Maximum directory depth under each storage root the walker descends. 1 catches the per-PV directory layer; 2 leaves headroom for CSI drivers that nest one extra level.").Default("2").Int()
	syncTimeout := app.Flag("k8s.sync-timeout", "Maximum time to wait for the PV informer cache to sync at startup.").Default("60s").Duration()

	localPathEnabled := app.Flag("scanner.local-path.enabled", "Enable the local-path scanner.").Default("false").Bool()
	localPathRoots := app.Flag("scanner.local-path.storage-roots", "Comma-separated list of storage roots to scan.").Default("/opt/local-path-provisioner").String()
	localPathExcludes := app.Flag("scanner.local-path.exclude", "Comma-separated list of basenames to skip while walking storage roots.").Default("lost+found,.snapshot,.zfs").String()
	localPathCrossFS := app.Flag("scanner.local-path.cross-fs", "Follow directory entries onto other filesystems. Default: skip mountpoints inside the storage root.").Default("false").Bool()

	nfsEnabled := app.Flag("scanner.nfs.enabled", "Enable the NFS scanner.").Default("false").Bool()
	nfsMountPath := app.Flag("scanner.nfs.mount-path", "Path inside the container where the NFS export is mounted (read-only).").Default("/mnt/nfs").String()
	nfsServer := app.Flag("scanner.nfs.server", "NFS server to match against PV specs. Empty matches every NFS PV (single-export deployments).").Default("").String()
	nfsExportRoot := app.Flag("scanner.nfs.export-root", "Server-side export root, stripped from an in-tree spec.nfs.path to compute the PV's path under the mount. Required for in-tree-NFS dangling detection.").Default("").String()
	nfsArchivedPrefix := app.Flag("scanner.nfs.archived-prefix", "Basename prefix the subdir provisioner uses for deleted-but-retained directories; matching entries are reported as archived, not orphaned.").Default("archived-").String()
	nfsExcludes := app.Flag("scanner.nfs.exclude", "Comma-separated list of basenames to skip while walking the NFS mount.").Default(".snapshot,lost+found").String()
	nfsCrossFS := app.Flag("scanner.nfs.cross-fs", "Descend into nested mountpoints inside the NFS export. Default: skip them (they belong to a different scanner instance).").Default("false").Bool()

	if _, err := app.Parse(args); err != nil {
		return err
	}

	setupLogging(*logLevel, *logFormat)

	slog.Info(
		"starting",
		"version", version.Version,
		"revision", version.Revision,
		"branch", version.Branch,
		"go", version.GoVersion(),
	)

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector())
	registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	ops := metrics.NewOperational()
	if err := ops.Register(registry); err != nil {
		return fmt.Errorf("register operational metrics: %w", err)
	}

	agg := metrics.NewAggregate()
	if err := agg.Register(registry); err != nil {
		return fmt.Errorf("register aggregate metrics: %w", err)
	}

	nodeName := os.Getenv("NODE_NAME")
	instanceID := nodeName
	if instanceID == "" {
		host, _ := os.Hostname()
		instanceID = host
	}

	inv := inventory.NewInventory()

	// Only thread NFS path-rewriting config in when the NFS scanner
	// is enabled. Without an NFS scan, NFS PVs produce no signal
	// anyway, so the zero NFSConfig (raw server-side paths) keeps
	// behaviour inert and unsurprising for local-path-only pods.
	invCfg := inventory.Config{}
	if *nfsEnabled {
		invCfg.NFS = inventory.NFSConfig{
			MountPath:  *nfsMountPath,
			ExportRoot: *nfsExportRoot,
			Server:     *nfsServer,
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *kubeconfig != "" || os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		cs, err := k8s.NewClient(k8s.ClientConfig{
			Kubeconfig: *kubeconfig,
			QPS:        *qps,
			Burst:      *burst,
		})
		if err != nil {
			slog.Warn("could not build kubernetes client; running without informer", "err", err)
		} else {
			factory := k8s.NewInformerFactory(cs, 5*time.Minute)
			if err := k8s.RegisterPVHandler(factory, inv, invCfg); err != nil {
				return fmt.Errorf("register PV handler: %w", err)
			}
			factory.Start(ctx.Done())

			syncCtx, syncCancel := context.WithTimeout(ctx, *syncTimeout)
			synced := factory.WaitForCacheSync(syncCtx.Done())
			syncCancel()
			allSynced := true
			for k, ok := range synced {
				if !ok {
					slog.Error("informer cache failed to sync", "type", k.String())
					allSynced = false
				}
			}
			if allSynced {
				slog.Info("PV informer cache synced", "pv_count", len(inv.Snapshot()))
			}
		}
	} else {
		slog.Info("no kubeconfig and not in cluster; skipping kubernetes client")
	}

	var scanners []scanner.Scanner
	if *localPathEnabled {
		scanners = append(scanners, localpath.New(localpath.Config{
			StorageRoots: splitCSV(*localPathRoots),
			Excludes:     splitCSV(*localPathExcludes),
			NodeName:     nodeName,
			CrossFS:      *localPathCrossFS,
			MaxDepth:     *scanMaxDepth,
		}))
	}
	if *nfsEnabled {
		scanners = append(scanners, nfs.New(nfs.Config{
			MountPath:      *nfsMountPath,
			Excludes:       splitCSV(*nfsExcludes),
			ArchivedPrefix: *nfsArchivedPrefix,
			CrossFS:        *nfsCrossFS,
			MaxDepth:       *scanMaxDepth,
		}))
	}

	var wg sync.WaitGroup
	for _, s := range scanners {
		state := &scanState{
			dangling: grace.New(*gracePeriod),
			orphaned: grace.New(*gracePeriod),
		}
		wg.Add(1)
		go func(s scanner.Scanner, state *scanState) {
			defer wg.Done()
			runScanLoop(ctx, s, inv, ops, agg, state, instanceID, *scanInterval, *scanTimeout)
		}(s, state)
	}

	mux := http.NewServeMux()
	mux.Handle(*telemetryPath, promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		Registry:          registry,
		EnableOpenMetrics: true,
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, "<html><head><title>k8s-pv-orphan-exporter</title></head>"+
			"<body><h1>k8s-pv-orphan-exporter</h1>"+
			"<p><a href=%q>metrics</a></p></body></html>", *telemetryPath)
	})

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", *listenAddr, "metrics_path", *telemetryPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err, ok := <-serverErr:
		if ok && err != nil {
			cancel()
			return fmt.Errorf("http server: %w", err)
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown", "err", err)
	}

	wg.Wait()
	return nil
}

// runScanLoop drives one scanner: each tick scans, diffs against the
// inventory, applies the grace gate, publishes the aggregate gauges,
// and updates operational metrics.
func runScanLoop(
	ctx context.Context,
	s scanner.Scanner,
	inv *inventory.Inventory,
	ops *metrics.Operational,
	agg *metrics.Aggregate,
	state *scanState,
	instanceID string,
	interval, timeout time.Duration,
) {
	backend := s.Backend()
	tick := func() {
		scanCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		start := time.Now()
		scanResult, err := s.Scan(scanCtx)
		ops.ScanDuration.WithLabelValues(backend).Observe(time.Since(start).Seconds())
		if err != nil {
			slog.Error("scan failed", "backend", backend, "err", err)
			ops.ScanErrors.WithLabelValues(backend, classifyError(err)).Inc()
			ops.Up.WithLabelValues(backend, instanceID).Set(0)
			return
		}

		pvs := inv.Snapshot()
		result := diff.Compute(pvs, scanResult)
		applyGrace(&result, state)
		agg.Publish(&result)

		for backendKind, count := range inv.SizeByBackend() {
			ops.InventorySize.WithLabelValues(string(backendKind)).Set(float64(count))
		}

		ops.Up.WithLabelValues(backend, instanceID).Set(1)
		ops.LastScanTimestamp.WithLabelValues(backend).Set(float64(time.Now().Unix()))

		slog.Debug(
			"scan completed",
			"backend", backend,
			"node", scanResult.Node,
			"entries", len(scanResult.Entries),
			"dangling", len(result.Dangling),
			"orphaned", len(result.Orphaned),
			"archived", len(result.Archived),
			"released", len(result.Released),
		)
	}

	tick()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// applyGrace gates result.Dangling and result.Orphaned through the
// scan state's per-kind trackers, dropping items whose continuous
// observation hasn't yet reached the configured grace period.
//
// Dangling keys are (PV.Name + path) so a hostPath PV with multiple
// expected paths gets independent grace timers per node.
func applyGrace(result *diff.Result, state *scanState) {
	if state == nil {
		return
	}

	danglingKeys := make([]string, 0, len(result.Dangling))
	danglingByKey := make(map[string]diff.DanglingPV, len(result.Dangling))
	for _, d := range result.Dangling {
		key := d.PV.Name + "\x00" + d.ExpectedPath.Node + "\x00" + d.ExpectedPath.Path
		danglingKeys = append(danglingKeys, key)
		danglingByKey[key] = d
	}
	survived := state.dangling.Step(danglingKeys)
	out := make([]diff.DanglingPV, 0, len(survived))
	for _, key := range survived {
		out = append(out, danglingByKey[key])
	}
	result.Dangling = out

	orphanKeys := make([]string, 0, len(result.Orphaned))
	orphanByKey := make(map[string]diff.OrphanedDir, len(result.Orphaned))
	for _, o := range result.Orphaned {
		orphanKeys = append(orphanKeys, o.Path)
		orphanByKey[o.Path] = o
	}
	survivedO := state.orphaned.Step(orphanKeys)
	outO := make([]diff.OrphanedDir, 0, len(survivedO))
	for _, key := range survivedO {
		outO = append(outO, orphanByKey[key])
	}
	result.Orphaned = outO
}

func classifyError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "other"
	}
}

func setupLogging(level, format string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

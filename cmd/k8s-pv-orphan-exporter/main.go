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

// Command k8s-pv-orphan-exporter is the Phase 1 skeleton entry point.
// It wires flag parsing, logging, the Kubernetes client/informer
// factory, the (stub) local-path scanner, the operational metrics,
// and the /metrics HTTP server. The diff engine and per-item metrics
// land in Phase 2.
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

	"github.com/reloaded/k8s-pv-orphan-exporter/internal/k8s"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/metrics"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/scanner/localpath"
	"github.com/reloaded/k8s-pv-orphan-exporter/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("exporter failed", "err", err)
		os.Exit(1)
	}
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

	localPathEnabled := app.Flag("scanner.local-path.enabled", "Enable the local-path scanner.").Default("false").Bool()
	localPathRoots := app.Flag("scanner.local-path.storage-roots", "Comma-separated list of storage roots to scan.").Default("/opt/local-path-provisioner").String()

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

	nodeName := os.Getenv("NODE_NAME")
	instanceID := nodeName
	if instanceID == "" {
		host, _ := os.Hostname()
		instanceID = host
	}

	var scanners []scanner.Scanner
	if *localPathEnabled {
		scanners = append(scanners, localpath.New(localpath.Config{
			StorageRoots: splitCSV(*localPathRoots),
			NodeName:     nodeName,
		}))
	}

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
			// Phase 1 wires the PV informer just enough to validate
			// the shape of the dependency; it does not consume events
			// yet — that's Phase 2.
			_ = factory.Core().V1().PersistentVolumes().Informer()
			slog.Info("kubernetes client and informer factory ready")
		}
	} else {
		slog.Info("no kubeconfig and not in cluster; skipping kubernetes client")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup
	for _, s := range scanners {
		wg.Add(1)
		go func(s scanner.Scanner) {
			defer wg.Done()
			runScanLoop(ctx, s, ops, instanceID, *scanInterval, *scanTimeout)
		}(s)
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

func runScanLoop(ctx context.Context, s scanner.Scanner, ops *metrics.Operational, instanceID string, interval, timeout time.Duration) {
	backend := s.Backend()
	tick := func() {
		scanCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		start := time.Now()
		_, err := s.Scan(scanCtx)
		ops.ScanDuration.WithLabelValues(backend).Observe(time.Since(start).Seconds())
		if err != nil {
			slog.Error("scan failed", "backend", backend, "err", err)
			ops.ScanErrors.WithLabelValues(backend, classifyError(err)).Inc()
			ops.Up.WithLabelValues(backend, instanceID).Set(0)
			return
		}
		ops.Up.WithLabelValues(backend, instanceID).Set(1)
		ops.LastScanTimestamp.WithLabelValues(backend).Set(float64(time.Now().Unix()))
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

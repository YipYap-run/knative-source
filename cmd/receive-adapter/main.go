/*
Copyright 2026 The YipYap Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command yipyap-knative-source runs inside a user's Kubernetes cluster as a
// Knative ContainerSource. It pulls CloudEvents from yipyap's HTTP API and
// forwards them to $K_SINK.
//
// This file is the binary's entry point: it loads the env-var config, starts
// the health-check HTTP server, handles graceful shutdown on SIGINT/SIGTERM,
// and hands off to source.Run for the poll/stream dispatcher. The rest lives
// in the source sub-package so tests can run the same code paths in-process.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/YipYap-run/knative-source/cmd/receive-adapter/source"
	"github.com/YipYap-run/knative-source/pkg/adapterstatus"
)

func main() {
	cfg, err := source.LoadConfig(os.Getenv)
	if err != nil {
		slog.Error("config error", "error", err)
		os.Exit(2)
	}
	slog.Info("yipyap-knative-source starting",
		"sink", cfg.Sink,
		"base_url", cfg.BaseURL,
		"mode", cfg.Mode,
		"event_filters", cfg.EventFilters,
		"source_id", cfg.SourceID,
		"sink_ca_certs", cfg.SinkRootCAs != nil,
		"sink_audience", cfg.SinkAudience,
	)
	if cfg.SinkAudience != "" && strings.HasPrefix(strings.ToLower(cfg.Sink), "http://") {
		slog.Warn("the sink has an OIDC audience but no TLS; its bearer token is sent in clear text inside the cluster")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start health server (liveness + readiness).
	healthSrv := startHealthServer(cfg.HealthAddr)

	if err := source.Run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("run loop exited with error", "error", err)
	}

	// Graceful shutdown of health server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := healthSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("health server shutdown error", "error", err)
	}
}

// startHealthServer starts the health HTTP server on addr and returns the
// *http.Server so main can Shutdown it.
func startHealthServer(addr string) *http.Server {
	srv := &http.Server{
		Addr:              addr,
		Handler:           healthMux(source.DefaultHealth, source.DefaultStats),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("health server exited", "error", err)
		}
	}()
	return srv
}

// healthMux serves:
//
//   - /healthz: liveness. Always 200 while the process runs. It never looks
//     at API or sink errors, so a bad API key does not cause a restart loop.
//   - /readyz: readiness. 503 with the reason while the API key is rejected,
//     the API is unreachable or the sink keeps failing (see source.Health).
//   - /status: the same verdict as JSON (adapterstatus.Status), read by the
//     controller to fill the source's Connected condition.
//   - /metrics: delivery counters.
func healthMux(health *source.Health, stats *source.Stats) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/readyz", health.ReadyzHandler())
	mux.Handle(adapterstatus.Path, health.StatusHandler())
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = stats.WriteMetrics(w)
	})
	return mux
}

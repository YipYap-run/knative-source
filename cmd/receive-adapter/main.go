// Command yipyap-knative-source runs inside a user's Kubernetes cluster as a
// Knative ContainerSource. It pulls CloudEvents from yipyap's HTTP API and
// forwards them to $K_SINK.
//
// This file is the binary's entry point: it handles env-var config, a
// health-check HTTP server, graceful shutdown on SIGINT/SIGTERM, and then
// hands off to source.Run for the poll/stream dispatcher. Everything
// substantive lives in the internal/source sub-package so the same code
// paths can be exercised in-process by integration tests and reused by the
// Phase-4 controller.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/YipYap-run/knative-source/cmd/receive-adapter/source"
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
	)

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

// startHealthServer starts a /healthz + /readyz HTTP server on addr and returns
// the *http.Server so main can Shutdown it.
func startHealthServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("health server exited", "error", err)
		}
	}()
	return srv
}

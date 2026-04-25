// Package source is the importable library behind the yipyap-knative-source
// binary. It exports the runtime entry point (Run), the Config loader, and
// the poll/stream/sink clients so that integration tests and the Phase-4
// controller can drive the same code paths without shelling out to a
// subprocess.
package source

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// MinRefreshAfter bounds the mode-refresh window from below so a
// misconfigured server hint (or a fast-returning transport) can't cause
// resolve-storms against the API. Even if a hint says refresh every 2s,
// we cap the actual cadence at once-per-minute.
const MinRefreshAfter = 1 * time.Minute

// ResolveErrBackoff is how long Run waits before retrying after a mode
// resolution returns a hard error (e.g. invalid YIPYAP_MODE). 30s keeps the
// logs from flooding without leaving a genuine config mistake too long
// uncorrected.
const ResolveErrBackoff = 30 * time.Second

// Run is the binary's core loop. Each iteration runs the mode-precedence
// chain, picks poll or stream, and runs the chosen transport inside a
// context capped at the resolution's RefreshAfter window. When the window
// elapses (or the transport returns early) the loop re-resolves.
//
// Run is the sole entry point intended for external callers (main, tests,
// Phase-4 controller). It owns nothing that survives ctx cancellation: the
// sink client, mode resolver, and per-session transport are all scoped to
// the loop.
func Run(ctx context.Context, cfg *Config) error {
	httpClient := &http.Client{Timeout: 30 * time.Second}

	sink := NewSinkClient(cfg)
	emit := func(ctx context.Context, ev ce.Event) error {
		if err := sink.Send(ctx, ev); err != nil {
			slog.Warn("sink send failed",
				"ce-id", ev.ID(),
				"ce-type", ev.Type(),
				"sink", cfg.Sink,
				"error", err,
			)
			return err
		}
		slog.Debug("sink send ok", "ce-id", ev.ID(), "ce-type", ev.Type())
		return nil
	}

	for ctx.Err() == nil {
		res, err := ResolveMode(ctx, cfg, httpClient, time.Now)
		if err != nil {
			slog.Error("mode resolution failed", "error", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(ResolveErrBackoff):
				continue
			}
		}

		refresh := res.RefreshAfter
		if refresh < MinRefreshAfter {
			slog.Warn("refresh window below floor; clamping",
				"requested", refresh, "floor", MinRefreshAfter)
			refresh = MinRefreshAfter
		}

		slog.Info("source mode resolved",
			"mode", res.Mode,
			"source", res.Source,
			"poll_interval", res.PollInterval,
			"refresh_after", refresh,
		)

		sessionCtx, cancel := context.WithTimeout(ctx, refresh)
		switch res.Mode {
		case "poll":
			client := NewPollClient(cfg)
			client.interval = res.PollInterval
			if err := client.Run(sessionCtx, emit); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Warn("poll client exited with error", "error", err)
			}
		case "stream":
			client := NewStreamClient(cfg)
			if err := client.run(sessionCtx, emit); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Warn("stream client exited with error", "error", err)
			}
		default:
			slog.Error("unreachable: unknown resolved mode", "mode", res.Mode)
		}
		cancel()
	}
	return ctx.Err()
}

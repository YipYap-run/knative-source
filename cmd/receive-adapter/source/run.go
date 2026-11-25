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

// Package source is the importable library behind the yipyap-knative-source
// binary. It exports the runtime entry point (Run), the Config loader, and
// the poll/stream/sink clients so that tests can drive the same code paths
// in-process instead of starting the binary.
package source

import (
	"context"
	"errors"
	"log/slog"
	"time"
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
// When ctx ends (SIGTERM), the transports stop fetching, give the in-flight
// event up to cfg.ShutdownGrace to reach the sink, confirm it, and return.
//
// Run is the sole entry point intended for external callers (main and
// tests). It owns nothing that survives ctx cancellation: the
// sink client, mode resolver, and per-session transport are all scoped to
// the loop.
func Run(ctx context.Context, cfg *Config) error {
	httpClient := newServerClient(30 * time.Second)

	if cfg.SourceID == "" {
		slog.Warn("YIPYAP_SOURCE_ID is not set; the server cannot track delivery for this source separately")
	}
	sink := NewSinkClient(cfg)
	// emit retries each event in place until the sink accepts or rejects it
	// (or shutdown ends delivery). Poll and stream only confirm an event to
	// the server after emit returns nil or ErrRejected.
	emit := NewDeliverer(sink.Send, DefaultStats).Deliver
	// Delivery and confirmation run on a context that only ends ShutdownGrace
	// after ctx does. A mode refresh ends the session (no more fetching) but
	// must not cut short an event the sink is still retrying, or the event
	// would be redelivered (poll) or, against a server without since= support,
	// lost (stream).
	dctx, cancelDelivery := deliveryContext(ctx, cfg.ShutdownGrace)
	defer cancelDelivery()
	// One confirmer for the run, so its "no ack endpoint" warning is logged
	// once per process rather than once per refresh window.
	conf := newConfirmer(cfg)
	// One poll client and one stream client for the whole run, so the last
	// confirmed cursor survives mode refreshes (even when writing it to disk
	// failed) and is sent as since= on the next request.
	var pollClient *PollClient
	var streamClient *StreamClient

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
			if pollClient == nil {
				pollClient = NewPollClient(cfg)
				pollClient.confirm = conf
			}
			pollClient.interval = safePollInterval(res.PollInterval)
			pollClient.health.SetPollInterval(pollClient.interval)
			if err := pollClient.runWith(sessionCtx, dctx, emit); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Warn("poll client exited with error", "error", err)
			}
		case "stream":
			if streamClient == nil {
				streamClient = NewStreamClient(cfg)
				streamClient.confirm = conf
			}
			if err := streamClient.runWith(sessionCtx, dctx, emit); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				slog.Warn("stream client exited with error", "error", err)
			}
		default:
			slog.Error("unreachable: unknown resolved mode", "mode", res.Mode)
		}
		cancel()
	}
	return ctx.Err()
}

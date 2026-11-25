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

package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// ErrRejected marks an event the sink rejected permanently. The adapter
// skips it (logged and counted) and confirms it to the server, so one bad
// event cannot block the stream forever.
var ErrRejected = errors.New("event rejected by sink")

// Retry backoff bounds for delivering one event to the sink.
const (
	deliverBackoffInitial = 250 * time.Millisecond
	deliverBackoffCap     = 30 * time.Second
)

// Stats counts delivery outcomes. All fields are safe for concurrent use.
type Stats struct {
	Delivered atomic.Uint64 // events the sink accepted
	Retries   atomic.Uint64 // failed attempts that were retried
	Rejected  atomic.Uint64 // events skipped after a permanent sink rejection
	AckErrors atomic.Uint64 // confirmations the server did not accept
	Filtered  atomic.Uint64 // events dropped by filter.severities or filter.monitorIDs
}

// DefaultStats is the process-wide counter set exposed on /metrics.
var DefaultStats = &Stats{}

// WriteMetrics writes s in the Prometheus text exposition format.
func (s *Stats) WriteMetrics(w io.Writer) error {
	_, err := fmt.Fprintf(w,
		"# HELP yipyap_source_events_delivered_total Events accepted by the sink.\n"+
			"# TYPE yipyap_source_events_delivered_total counter\n"+
			"yipyap_source_events_delivered_total %d\n"+
			"# HELP yipyap_source_delivery_retries_total Failed sink attempts that were retried.\n"+
			"# TYPE yipyap_source_delivery_retries_total counter\n"+
			"yipyap_source_delivery_retries_total %d\n"+
			"# HELP yipyap_source_events_rejected_total Events skipped after a permanent sink rejection.\n"+
			"# TYPE yipyap_source_events_rejected_total counter\n"+
			"yipyap_source_events_rejected_total %d\n"+
			"# HELP yipyap_source_ack_errors_total Delivery confirmations the server did not accept.\n"+
			"# TYPE yipyap_source_ack_errors_total counter\n"+
			"yipyap_source_ack_errors_total %d\n"+
			"# HELP yipyap_source_events_filtered_total Events dropped by the severity or monitor filter.\n"+
			"# TYPE yipyap_source_events_filtered_total counter\n"+
			"yipyap_source_events_filtered_total %d\n",
		s.Delivered.Load(), s.Retries.Load(), s.Rejected.Load(), s.AckErrors.Load(), s.Filtered.Load())
	return err
}

// Deliverer sends one event to the sink, retrying in place with capped,
// jittered exponential backoff until the sink accepts it, rejects it
// permanently, or ctx ends. Callers must not fetch or confirm further events
// until Deliver returns.
type Deliverer struct {
	send    func(context.Context, ce.Event) error
	stats   *Stats
	initial time.Duration
	cap     time.Duration
	// sleep waits d or until ctx ends. Swapped in tests.
	sleep func(ctx context.Context, d time.Duration) error
	// health records sink outcomes for readiness. Nil disables it.
	health *Health
}

// NewDeliverer builds a Deliverer around send (normally SinkClient.Send).
// A nil stats uses DefaultStats.
func NewDeliverer(send func(context.Context, ce.Event) error, stats *Stats) *Deliverer {
	if stats == nil {
		stats = DefaultStats
	}
	return &Deliverer{
		send:    send,
		stats:   stats,
		initial: deliverBackoffInitial,
		cap:     deliverBackoffCap,
		sleep:   sleepCtx,
		health:  DefaultHealth,
	}
}

// Deliver returns nil once the sink accepts ev, an error wrapping ErrRejected
// when the sink rejects it permanently, or ctx's error when ctx ends first.
// Errors that are neither a *SinkError nor permanent are treated as
// retryable.
func (d *Deliverer) Deliver(ctx context.Context, ev ce.Event) error {
	backoff := d.initial
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Send mutates the event's extension map, so hand it a copy.
		err := d.send(ctx, ev.Clone())
		if err == nil {
			d.health.SinkOK()
			d.stats.Delivered.Add(1)
			slog.Debug("sink send ok", "ce_id", ev.ID(), "ce_type", ev.Type(), "attempt", attempt)
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		var se *SinkError
		if errors.As(err, &se) && !se.Retryable {
			d.health.SinkOK()
			d.stats.Rejected.Add(1)
			slog.Error("sink rejected event; skipping",
				"ce_id", ev.ID(), "ce_type", ev.Type(), "status", se.StatusCode, "error", err)
			return fmt.Errorf("%w: %w", ErrRejected, err)
		}
		wait := jitter(backoff)
		if se != nil && se.RetryAfter > wait {
			wait = se.RetryAfter
		}
		d.health.SinkFailed(err)
		d.stats.Retries.Add(1)
		slog.Warn("sink send failed; retrying",
			"ce_id", ev.ID(), "ce_type", ev.Type(), "attempt", attempt, "wait", wait, "error", err)
		if err := d.sleep(ctx, wait); err != nil {
			return err
		}
		backoff *= 2
		if backoff > d.cap {
			backoff = d.cap
		}
	}
}

// jitter returns a duration in [d/2, d], so many adapters recovering from the
// same sink outage do not retry in lockstep.
func jitter(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	half := d / 2
	return half + rand.N(d-half+1)
}

// sleepCtx waits d or until ctx ends, returning ctx's error in the latter case.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// deliveryContext returns a context for delivering and confirming events that
// outlives stop by grace: when stop ends, in-flight work gets up to grace to
// finish before the returned context is cancelled. A grace of 0 cancels it
// together with stop.
func deliveryContext(stop context.Context, grace time.Duration) (context.Context, context.CancelFunc) {
	if grace <= 0 {
		return context.WithCancel(stop)
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(stop))
	var timer atomic.Pointer[time.Timer]
	unregister := context.AfterFunc(stop, func() {
		timer.Store(time.AfterFunc(grace, cancel))
	})
	return ctx, func() {
		unregister()
		if t := timer.Load(); t != nil {
			t.Stop()
		}
		cancel()
	}
}

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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"sync/atomic"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// Wire contract with the YipYap server. docs/delivery.md is the reference;
// keep the two in sync.
const (
	// SourceIDHeader carries the YipYapSource UID on every request, so the
	// server keeps one consumer (and one confirmed position) per source
	// instead of one per org and filter set.
	SourceIDHeader = "X-YipYap-Source-ID"

	// AckPath is where the adapter confirms events the sink has accepted or
	// permanently rejected.
	AckPath = "/api/v1/cloudevents/ack"

	// CursorExtension is the CloudEvent extension attribute in which the
	// stream endpoint sends each event's opaque cursor. The adapter strips it
	// before forwarding, since it is transport state, not event data.
	CursorExtension = "yipyapcursor"
)

// ackAttempts bounds how often one confirmation is retried. A confirmation
// that never lands is safe: the server redelivers and the sink sees a
// duplicate, which the contract allows.
const ackAttempts = 3

// ackTimeout bounds a single confirmation attempt.
const ackTimeout = 5 * time.Second

// ackBudget bounds one confirmation across all attempts and waits, including
// on shutdown when the delivery context may already be gone. The adapter
// pod's terminationGracePeriodSeconds must cover DefaultShutdownGrace plus
// ackBudget (see resources.AdapterTerminationGracePeriodSeconds).
const ackBudget = 10 * time.Second

// ackProbeInterval is how long the adapter stops sending confirmations after
// the server answers that it has no ack endpoint. It then tries again, so it
// starts confirming soon after the server adds the endpoint, without sending
// a request per event that is certain to fail in the meantime.
const ackProbeInterval = 5 * time.Minute

// errAckUnsupported is returned while confirmations are paused because the
// server has no ack endpoint.
var errAckUnsupported = errors.New("ack: server has no confirmation endpoint")

// ackRequest is the JSON body of POST AckPath.
type ackRequest struct {
	Cursor string `json:"cursor,omitempty"`
}

// setServerHeaders applies the headers every request to the YipYap API
// carries: the bearer API key and, when known, the source identity.
func setServerHeaders(req *http.Request, apiKey, sourceID string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if sourceID != "" {
		req.Header.Set(SourceIDHeader, sourceID)
	}
}

// confirmer posts delivery confirmations to the server.
type confirmer struct {
	baseURL    string
	apiKey     string
	sourceID   string
	httpClient *http.Client
	stats      *Stats
	// ackBudget overrides the package ackBudget when positive. Tests only.
	ackBudget time.Duration
	// warnedUnsupported limits the "server has no ack endpoint" warning to
	// one log line per process.
	warnedUnsupported atomic.Bool
	// unsupportedUntil (Unix nanoseconds) pauses confirmations after the
	// server reported it has no ack endpoint. See ackProbeInterval.
	unsupportedUntil atomic.Int64
	// now is the clock. Swapped in tests.
	now func() time.Time
}

func newConfirmer(cfg *Config) *confirmer {
	return &confirmer{
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		sourceID:   cfg.SourceID,
		httpClient: newServerClient(ackTimeout),
		stats:      DefaultStats,
		now:        time.Now,
	}
}

// ack confirms cursor. It retries transient failures a few times and then
// gives up; the caller carries on either way, because an unconfirmed event is
// redelivered rather than lost. ack ignores cancellation of ctx, so a
// confirmation sent during shutdown still goes out, and is bounded by
// ackBudget in total.
func (c *confirmer) ack(ctx context.Context, cursor string) error {
	if cursor == "" {
		return nil
	}
	if c.now().UnixNano() < c.unsupportedUntil.Load() {
		return errAckUnsupported
	}
	body, err := json.Marshal(ackRequest{Cursor: cursor})
	if err != nil {
		return fmt.Errorf("marshal ack: %w", err)
	}
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("parse baseURL: %w", err)
	}
	u.Path = path.Join(u.Path, AckPath)

	// Detach from ctx so a confirmation sent during shutdown still goes out,
	// but cap the whole exchange at ackBudget.
	bctx, cancelBudget := context.WithTimeout(context.WithoutCancel(ctx), c.budget())
	defer cancelBudget()

	var lastErr error
	wait := deliverBackoffInitial
	for attempt := 1; attempt <= ackAttempts; attempt++ {
		actx, cancel := context.WithTimeout(bctx, ackTimeout)
		status, err := c.post(actx, u.String(), body)
		cancel()
		switch {
		case err == nil && status >= 200 && status < 300:
			return nil
		case err == nil && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented):
			// The server predates the ack endpoint. Without it the server
			// falls back to its own acking, so there is nothing to retry.
			c.stats.AckErrors.Add(1)
			c.unsupportedUntil.Store(c.now().Add(ackProbeInterval).UnixNano())
			if c.warnedUnsupported.CompareAndSwap(false, true) {
				slog.Warn("server does not accept delivery confirmations yet; delivery is best effort until it does",
					"status", status, "path", AckPath, "recheck_every", ackProbeInterval)
			}
			return fmt.Errorf("%w (status %d)", errAckUnsupported, status)
		case err == nil && status >= 400 && status < 500 && status != http.StatusTooManyRequests && status != http.StatusRequestTimeout:
			// Not retried: the same request gets the same answer. Logged
			// because a 401 or 403 here usually means the API key lacks
			// access, and every confirmation will fail until that is fixed.
			c.stats.AckErrors.Add(1)
			slog.Warn("server refused delivery confirmation; the server will redeliver",
				"status", status, "cursor", cursor)
			return fmt.Errorf("ack: server returned %d", status)
		case err == nil:
			lastErr = fmt.Errorf("ack: server returned %d", status)
		default:
			lastErr = err
		}
		if bctx.Err() != nil {
			break
		}
		if attempt < ackAttempts {
			if sleepCtx(bctx, jitter(wait)) != nil {
				break
			}
			wait *= 2
		}
	}
	c.stats.AckErrors.Add(1)
	slog.Warn("delivery confirmation failed; the server will redeliver", "cursor", cursor, "error", lastErr)
	return lastErr
}

func (c *confirmer) budget() time.Duration {
	if c.ackBudget > 0 {
		return c.ackBudget
	}
	return ackBudget
}

func (c *confirmer) post(ctx context.Context, u string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("new request: %w", err)
	}
	setServerHeaders(req, c.apiKey, c.sourceID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http do: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// takeCursor removes the server cursor extension from ev and returns it.
func takeCursor(ev *ce.Event) string {
	v, ok := ev.Extensions()[CursorExtension]
	if !ok {
		return ""
	}
	cur := fmt.Sprint(v)
	ev.SetExtension(CursorExtension, nil)
	return cur
}

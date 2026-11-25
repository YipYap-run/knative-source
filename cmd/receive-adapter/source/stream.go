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
	"bufio"
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
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// StreamClient talks to the yipyap server's /api/v1/cloudevents/stream
// endpoint and emits each received CloudEvent through a caller-supplied
// callback.
//
// Delivery contract (docs/delivery.md): each event is retried in place by
// emit until the sink accepts or permanently rejects it, and only then is it
// confirmed to the server with POST AckPath. The server redelivers anything
// not confirmed. On reconnect the client sends since=<last confirmed cursor>,
// taken from the CursorExtension attribute of the last confirmed event.
//
// The underlying *http.Client must NOT have a Timeout set: the stream is
// long-lived and any response-body timeout will truncate normal operation.
type StreamClient struct {
	baseURL  string
	apiKey   string
	sourceID string
	filters  []string
	// filter applies filter.severities and filter.monitorIDs locally. Nil
	// keeps every event.
	filter     *EventFilter
	httpClient *http.Client
	// grace is how long in-flight delivery may continue after ctx ends.
	grace time.Duration
	// confirm posts per-event confirmations. Nil skips them (tests).
	confirm *confirmer
	// health records API round trips for readiness. Nil disables it.
	health *Health
	// lastCursor is the cursor of the last confirmed event, sent as since=
	// on reconnect.
	lastCursor string
	// cursorPath persists lastCursor so a restarted container resumes from
	// it. Empty disables persistence. Kept apart from the poll cursor file
	// because the contract does not promise the two cursors are comparable.
	cursorPath   string
	cursorLoaded bool
}

// streamCursorSuffix is appended to YIPYAP_CURSOR_PATH for the stream cursor.
const streamCursorSuffix = ".stream"

// streamBackoffInitial is the first wait between a disconnect and the
// reconnect attempt. Doubles on each subsequent failure.
const streamBackoffInitial = 250 * time.Millisecond

// streamBackoffCap is the ceiling for the exponential reconnect backoff.
// Picked to match the SaaS server's typical rolling-deployment drain time
// without hammering it during an outage.
const streamBackoffCap = 30 * time.Second

// NewStreamClient builds a StreamClient from the binary's loaded Config.
// The http.Client deliberately has no Timeout: read deadlines on the
// response body would break the long-lived stream contract.
func NewStreamClient(cfg *Config) *StreamClient {
	return &StreamClient{
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		sourceID:   cfg.SourceID,
		filters:    cfg.EventFilters,
		filter:     NewEventFilter(cfg.Severities, cfg.MonitorIDs),
		httpClient: newServerClient(0),
		grace:      cfg.ShutdownGrace,
		confirm:    newConfirmer(cfg),
		cursorPath: streamCursorPath(cfg.CursorPath),
		health:     DefaultHealth,
	}
}

func streamCursorPath(pollPath string) string {
	if pollPath == "" {
		return ""
	}
	return pollPath + streamCursorSuffix
}

// loadCursor reads the persisted stream cursor once per client.
func (s *StreamClient) loadCursor() {
	if s.cursorLoaded {
		return
	}
	s.cursorLoaded = true
	if s.lastCursor != "" {
		return
	}
	c, err := readCursorFile(s.cursorPath)
	if err != nil {
		slog.Warn("stream cursor read failed; starting without one", "error", err)
		return
	}
	s.lastCursor = c
}

// run blocks until ctx is cancelled. On each transport-level disconnect,
// it reconnects with an exponential backoff capped at streamBackoffCap.
// The backoff resets to streamBackoffInitial once the stream has
// successfully delivered at least one event on the new connection.
//
// When ctx ends, the event being delivered gets up to s.grace to finish and
// be confirmed before run returns.
func (s *StreamClient) run(ctx context.Context, emit func(context.Context, ce.Event) error) error {
	dctx, cancel := deliveryContext(ctx, s.grace)
	defer cancel()
	return s.runWith(ctx, dctx, emit)
}

// runWith is run with the delivery context supplied by the caller. ctx scopes
// the connections; dctx bounds delivery and confirmation of the current
// event, so a mode refresh that ends ctx does not abandon it.
func (s *StreamClient) runWith(ctx, dctx context.Context, emit func(context.Context, ce.Event) error) error {
	backoff := streamBackoffInitial
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		gotEvent, err := s.streamOnce(ctx, dctx, emit)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// If we made it to a real event, the server is healthy: reset
		// the backoff so a later disconnect doesn't inherit a stale cap.
		if gotEvent {
			backoff = streamBackoffInitial
		}
		slog.Warn("stream disconnected; reconnecting", "error", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > streamBackoffCap {
			backoff = streamBackoffCap
		}
	}
}

// stream opens one connection to /api/v1/cloudevents/stream and runs
// the NDJSON decode loop. It returns when the connection closes, ctx is
// cancelled, or a non-retryable error surfaces. Used directly in tests
// that want to assert a single-shot behaviour; run() wraps this in a
// reconnect loop.
func (s *StreamClient) stream(ctx context.Context, emit func(context.Context, ce.Event) error) error {
	dctx, cancel := deliveryContext(ctx, s.grace)
	defer cancel()
	_, err := s.streamOnce(ctx, dctx, emit)
	return err
}

// streamOnce is the guts of a single connection: it opens, reads NDJSON,
// delivers and confirms each event, and returns (gotEvent, err). gotEvent
// indicates whether at least one valid CloudEvent was handled, so run() can
// use that signal to reset its exponential backoff. ctx scopes the
// connection; dctx scopes delivery and confirmation of the current event.
func (s *StreamClient) streamOnce(ctx, dctx context.Context, emit func(context.Context, ce.Event) error) (bool, error) {
	s.loadCursor()
	u, err := url.Parse(s.baseURL)
	if err != nil {
		return false, fmt.Errorf("parse baseURL: %w", err)
	}
	u.Path = path.Join(u.Path, "/api/v1/cloudevents/stream")
	q := u.Query()
	// One filter param per type glob; the server OR-matches them.
	for _, f := range s.filters {
		q.Add("filter", f)
	}
	s.filter.addQuery(q)
	if s.lastCursor != "" {
		q.Set("since", s.lastCursor)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, fmt.Errorf("new request: %w", err)
	}
	setServerHeaders(req, s.apiKey, s.sourceID)
	req.Header.Set("Accept", "application/cloudevents-batch+json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			s.health.APIFailed(err)
		}
		return false, fmt.Errorf("http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		err := &APIStatusError{Op: "stream", StatusCode: resp.StatusCode, Body: string(body)}
		s.health.APIFailed(err)
		return false, err
	}
	// The stream is open: the API counts as reachable until it closes, even
	// when no event arrives for a long time.
	s.health.StreamConnected()
	defer s.health.StreamClosed()

	// Allow up to 4MB per line. 64KB start size handles the common case;
	// the hard cap exists so a pathological server can't OOM us with a
	// single gigantic record.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var gotEvent bool
	for scanner.Scan() {
		// The scanner can still hold buffered lines after ctx ends. Stop
		// here so shutdown does not start delivering events it was told to
		// stop fetching; the server redelivers them.
		if ctx.Err() != nil {
			return gotEvent, ctx.Err()
		}
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev ce.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			slog.Warn("stream: skipping malformed event", "error", err)
			continue
		}
		cursor := takeCursor(&ev)
		// A filtered event is not sent to the sink but is still confirmed
		// below, so the server does not redeliver it.
		if s.filter.skip(ev) {
			// Not emitted; fall through to the confirmation.
		} else if err := emit(dctx, ev); err != nil && !errors.Is(err, ErrRejected) {
			// Delivery was abandoned (shutdown, or a caller-supplied emit
			// that does not retry). Do not confirm: the server redelivers
			// this event, and since= on reconnect still names the last
			// confirmed one.
			return gotEvent, fmt.Errorf("emit %s: %w", ev.ID(), err)
		}
		gotEvent = true
		if s.confirm != nil {
			_ = s.confirm.ack(dctx, cursor)
		}
		if cursor != "" && cursor != s.lastCursor {
			s.lastCursor = cursor
			if err := writeCursorFile(s.cursorPath, cursor); err != nil {
				slog.Warn("stream cursor persist failed", "error", err)
			}
		}
	}
	return gotEvent, scanner.Err()
}

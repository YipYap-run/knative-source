package source

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
// callback. Server-side durable-consumer state persists cursor position
// across reconnects, so no on-disk cursor file is needed.
//
// The underlying *http.Client must NOT have a Timeout set — the stream is
// long-lived and any response-body timeout will truncate normal operation.
type StreamClient struct {
	baseURL    string
	apiKey     string
	filters    []string
	httpClient *http.Client
}

// streamBackoffInitial is the first wait between a disconnect and the
// reconnect attempt. Doubles on each subsequent failure.
const streamBackoffInitial = 250 * time.Millisecond

// streamBackoffCap is the ceiling for the exponential reconnect backoff.
// Picked to match the SaaS server's typical rolling-deployment drain time
// without hammering it during an outage.
const streamBackoffCap = 30 * time.Second

// NewStreamClient builds a StreamClient from the binary's loaded Config.
// The http.Client deliberately has no Timeout — read deadlines on the
// response body would break the long-lived stream contract.
func NewStreamClient(cfg *Config) *StreamClient {
	return &StreamClient{
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		filters:    cfg.EventFilters,
		httpClient: &http.Client{},
	}
}

// run blocks until ctx is cancelled. On each transport-level disconnect,
// it reconnects with an exponential backoff capped at streamBackoffCap.
// The backoff resets to streamBackoffInitial once the stream has
// successfully delivered at least one event on the new connection —
// signalled by stream() returning nil or io.EOF after it saw data.
//
// The backoff is opinionated: 250ms on a flapping network doesn't burn
// requests, and 30s during a persistent outage is gentle enough for the
// upstream to recover. Callers that want a smaller cap can reach inside
// via the streamBackoffCap constant during tests.
func (s *StreamClient) run(ctx context.Context, emit func(context.Context, ce.Event) error) error {
	backoff := streamBackoffInitial
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		gotEvent, err := s.streamOnce(ctx, emit)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// If we made it to a real event, the server is healthy — reset
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
	_, err := s.streamOnce(ctx, emit)
	return err
}

// streamOnce is the guts of a single connection: it opens, reads NDJSON,
// and returns (gotEvent, err). gotEvent indicates whether at least one
// valid CloudEvent was emitted, so run() can use that signal to reset its
// exponential backoff.
func (s *StreamClient) streamOnce(ctx context.Context, emit func(context.Context, ce.Event) error) (bool, error) {
	u, err := url.Parse(s.baseURL)
	if err != nil {
		return false, fmt.Errorf("parse baseURL: %w", err)
	}
	u.Path = path.Join(u.Path, "/api/v1/cloudevents/stream")
	if len(s.filters) > 0 {
		q := u.Query()
		// One filter param per type glob; the server OR-matches them.
		for _, f := range s.filters {
			q.Add("filter", f)
		}
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Accept", "application/cloudevents-batch+json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return false, fmt.Errorf("stream returned %d: %s", resp.StatusCode, string(body))
	}

	// Allow up to 4MB per line. 64KB start size handles the common case;
	// the hard cap exists so a pathological server can't OOM us with a
	// single gigantic record.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var gotEvent bool
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev ce.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			slog.Warn("stream: skipping malformed event", "error", err)
			continue
		}
		if err := emit(ctx, ev); err != nil {
			// Emit failure: log and continue. Lost-event accountability
			// lives at the outbound-commit layer — crashing the stream
			// on a transient sink hiccup would amplify, not mitigate,
			// the problem.
			slog.Warn("stream emit failed", "error", err)
		}
		gotEvent = true
	}
	return gotEvent, scanner.Err()
}

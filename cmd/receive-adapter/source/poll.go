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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// PollClient polls yipyap's /api/v1/cloudevents/poll endpoint and emits each
// received event through a caller-supplied callback. The resume cursor is
// persisted to cursorPath between calls so the client picks up where it left
// off across container restarts.
//
// Construction is intentionally lightweight: the struct is just configuration
// plus an *http.Client. Concurrency is per-run: a single Run goroutine owns
// the cursor file and the HTTP client.
type PollClient struct {
	baseURL  string
	apiKey   string
	sourceID string
	filters  []string
	// filter applies filter.severities and filter.monitorIDs locally. Nil
	// keeps every event.
	filter     *EventFilter
	interval   time.Duration
	cursorPath string
	httpClient *http.Client
	// grace is how long in-flight delivery may continue after ctx ends.
	grace time.Duration
	// confirm sends the final confirmation on shutdown. Nil skips it.
	confirm *confirmer
	// health records API round trips for readiness. Nil disables it.
	health *Health
	// authRetry is the wait before the next poll after a 401 or 403, while
	// fewer than AuthFailureThreshold have been seen in a row and it is
	// shorter than interval. Zero disables it.
	authRetry time.Duration
	// cursor is the last fully delivered next_cursor. It is loaded from
	// cursorPath once and then kept in memory, so a client reused across
	// mode refreshes resumes from it even when persisting it failed.
	cursor       string
	cursorLoaded bool
}

// NewPollClient builds a PollClient from the binary's loaded Config. The
// default HTTP client has a generous timeout: the server-side drain window
// is ~2s and we add slack for network/TLS overhead.
func NewPollClient(cfg *Config) *PollClient {
	return &PollClient{
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		sourceID:   cfg.SourceID,
		filters:    cfg.EventFilters,
		filter:     NewEventFilter(cfg.Severities, cfg.MonitorIDs),
		interval:   safePollInterval(cfg.PollInterval),
		cursorPath: cfg.CursorPath,
		httpClient: newServerClient(10 * time.Second),
		grace:      cfg.ShutdownGrace,
		confirm:    newConfirmer(cfg),
		health:     DefaultHealth,
		authRetry:  AuthRetryInterval,
	}
}

// AuthRetryInterval is how soon the poll client asks again after a 401 or
// 403 when its poll interval is longer. With a slow poll schedule, waiting
// a full interval between attempts would delay a "key rejected" verdict by
// AuthFailureThreshold intervals. Once the threshold is reached the client
// goes back to its normal interval.
const AuthRetryInterval = 10 * time.Second

// isAuthError reports whether err is a 401 or 403 from the YipYap API.
func isAuthError(err error) bool {
	var se *APIStatusError
	return errors.As(err, &se) &&
		(se.StatusCode == http.StatusUnauthorized || se.StatusCode == http.StatusForbidden)
}

// Run ticks at p.interval, fetching a batch and handing each event to emit
// in order. emit is expected to retry in place (see Deliverer.Deliver): it
// returns nil when the sink accepted the event, an error wrapping
// ErrRejected when the sink rejected it permanently, and any other error
// only when delivery was abandoned (shutdown).
//
// The cursor advances, and is persisted, only after every event in the batch
// was accepted or rejected. The next poll sends it as since=, which is the
// confirmation the server uses to stop redelivering that batch. If a batch is
// abandoned, the old cursor stays and the server redelivers the whole batch,
// so the sink can see duplicates but does not lose events.
//
// When ctx ends, Run stops fetching. A batch already in progress keeps
// delivering for up to p.grace, then Run sends any cursor the server has not
// seen yet to the ack endpoint and returns.
//
// Fetch errors are logged but never fatal, because a broken server or sink
// should not take down a long-lived ContainerSource.
func (p *PollClient) Run(ctx context.Context, emit func(context.Context, ce.Event) error) error {
	dctx, cancel := deliveryContext(ctx, p.grace)
	defer cancel()
	return p.runWith(ctx, dctx, emit)
}

// runWith is Run with the delivery context supplied by the caller. ctx ends
// fetching; dctx bounds delivery and the final confirmation. The top-level
// Run loop passes one dctx for the whole process, so a mode refresh that
// ends ctx lets the current batch finish instead of abandoning it.
func (p *PollClient) runWith(ctx, dctx context.Context, emit func(context.Context, ce.Event) error) error {
	if !p.cursorLoaded {
		c, err := p.readCursor()
		if err != nil {
			slog.Warn("cursor read failed; starting without one", "error", err)
		}
		p.cursor, p.cursorLoaded = c, true
	}
	cursor := p.cursor
	// sent is the cursor the server has already been told about.
	sent := cursor
	defer func() {
		p.cursor = cursor
		if cursor != sent && p.confirm != nil {
			_ = p.confirm.ack(dctx, cursor)
		}
	}()

	// time.NewTicker panics on a non-positive duration. The interval can be
	// set after construction (Run assigns the resolved interval), so guard
	// it here too.
	interval := safePollInterval(p.interval)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	period := interval
	setPeriod := func(d time.Duration) {
		if d != period {
			period = d
			tick.Reset(d)
		}
	}
	// authFails counts 401 and 403 answers in a row. Transport errors do
	// not reset it, matching Health.
	authFails := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		next, events, err := p.fetch(ctx, cursor)
		if err != nil {
			if ctx.Err() == nil {
				p.health.APIFailed(err)
				slog.Warn("poll fetch failed", "error", err)
			}
			var se *APIStatusError
			switch {
			case isAuthError(err):
				authFails++
			case errors.As(err, &se):
				authFails = 0
			}
			if authFails > 0 && authFails < AuthFailureThreshold && p.authRetry > 0 && p.authRetry < interval {
				setPeriod(p.authRetry)
			} else {
				setPeriod(interval)
			}
			continue
		}
		authFails = 0
		setPeriod(interval)
		p.health.APIOK()
		sent = cursor
		if !p.deliverBatch(dctx, events, emit) {
			slog.Warn("batch delivery interrupted; the server will redeliver it", "cursor", cursor)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Keep the old cursor so the next poll asks for the batch again.
			continue
		}
		if next != "" && next != cursor {
			cursor = next
			if err := p.writeCursor(cursor); err != nil {
				slog.Warn("cursor persist failed", "error", err)
			}
		}
	}
}

// deliverBatch emits every event in order. Events the filter drops are not
// emitted but count as handled, so the next since= confirms them with the
// rest of the batch. It reports false when an event was neither accepted
// nor permanently rejected, which only happens when delivery is abandoned.
func (p *PollClient) deliverBatch(ctx context.Context, events []ce.Event, emit func(context.Context, ce.Event) error) bool {
	for _, ev := range events {
		if p.filter.skip(ev) {
			continue
		}
		err := emit(ctx, ev)
		if err == nil || errors.Is(err, ErrRejected) {
			continue
		}
		slog.Warn("emit abandoned", "error", err, "ce_id", ev.ID())
		return false
	}
	return true
}

// safePollInterval returns d, or DefaultPollInterval when d is not positive.
func safePollInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultPollInterval
	}
	return d
}

// fetch performs a single GET /api/v1/cloudevents/poll call and decodes the
// response into a slice of ce.Event plus the server's next_cursor string.
// Malformed individual envelopes are skipped with a warning; a malformed
// top-level payload is returned as an error so the caller can back off.
func (p *PollClient) fetch(ctx context.Context, cursor string) (string, []ce.Event, error) {
	u, err := url.Parse(p.baseURL)
	if err != nil {
		return "", nil, fmt.Errorf("parse baseURL: %w", err)
	}
	u.Path = path.Join(u.Path, "/api/v1/cloudevents/poll")
	q := u.Query()
	// One filter param per type glob; the server OR-matches them.
	for _, f := range p.filters {
		q.Add("filter", f)
	}
	p.filter.addQuery(q)
	if cursor != "" {
		q.Set("since", cursor)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, fmt.Errorf("new request: %w", err)
	}
	setServerHeaders(req, p.apiKey, p.sourceID)
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return "", nil, &APIStatusError{Op: "poll", StatusCode: resp.StatusCode, Body: string(body)}
	}

	var payload struct {
		Events     []json.RawMessage `json:"events"`
		NextCursor string            `json:"next_cursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", nil, fmt.Errorf("decode poll response: %w", err)
	}

	events := make([]ce.Event, 0, len(payload.Events))
	for _, raw := range payload.Events {
		var ev ce.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			slog.Warn("skipping malformed event", "error", err)
			continue
		}
		events = append(events, ev)
	}
	return payload.NextCursor, events, nil
}

// readCursor reads the persisted cursor from disk. A missing file returns an
// empty cursor and a nil error: first boot must not be a hard failure.
func (p *PollClient) readCursor() (string, error) {
	return readCursorFile(p.cursorPath)
}

// readCursorFile reads a cursor written by writeCursorFile. An empty path or
// a missing file yields an empty cursor and a nil error.
func readCursorFile(cursorPath string) (string, error) {
	if cursorPath == "" {
		return "", nil
	}
	b, err := os.ReadFile(cursorPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read cursor: %w", err)
	}
	return string(b), nil
}

// writeCursor persists the cursor atomically: write to a sibling temp file,
// then rename over the target. os.Rename is atomic on POSIX filesystems, so
// a crash mid-write leaves either the old cursor or the new one, never a
// half-written file.
func (p *PollClient) writeCursor(cursor string) error {
	return writeCursorFile(p.cursorPath, cursor)
}

// writeCursorFile atomically replaces cursorPath with cursor. An empty path
// is a no-op.
func writeCursorFile(cursorPath, cursor string) error {
	if cursorPath == "" {
		return nil
	}
	dir := filepath.Dir(cursorPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir cursor dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(cursorPath)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	// Clean up the temp file if we fail before the rename succeeds.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.WriteString(cursor); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, cursorPath); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	committed = true
	return nil
}

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
// off across restarts.
//
// Construction is intentionally lightweight: the struct is just configuration
// plus an *http.Client. Concurrency is per-run — a single Run goroutine owns
// the cursor file and the HTTP client.
type PollClient struct {
	baseURL    string
	apiKey     string
	filters    []string
	interval   time.Duration
	cursorPath string
	httpClient *http.Client
}

// NewPollClient builds a PollClient from the binary's loaded Config. The
// default HTTP client has a generous timeout: the server-side drain window
// is ~2s and we add slack for network/TLS overhead.
func NewPollClient(cfg *Config) *PollClient {
	interval := cfg.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &PollClient{
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		filters:    cfg.EventFilters,
		interval:   interval,
		cursorPath: cfg.CursorPath,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Run ticks at p.interval, fetching events and invoking emit for each. It
// only advances (and persists) the cursor when every event in a batch has
// been emitted successfully: on partial failure the next tick re-fetches
// starting from the old cursor, relying on CloudEvents ce-id idempotency at
// the sink.
//
// Errors are logged but never fatal — the poll client is defensive by
// design, because a broken sink shouldn't take down a long-lived
// ContainerSource.
func (p *PollClient) Run(ctx context.Context, emit func(context.Context, ce.Event) error) error {
	cursor, _ := p.readCursor()
	tick := time.NewTicker(p.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			next, events, err := p.fetch(ctx, cursor)
			if err != nil {
				slog.Warn("poll fetch failed", "error", err)
				continue
			}
			allOK := true
			for _, ev := range events {
				if err := emit(ctx, ev); err != nil {
					slog.Warn("emit failed; will retry via cursor resume", "error", err, "ce_id", ev.ID())
					allOK = false
					break
				}
			}
			if !allOK {
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
	if cursor != "" {
		q.Set("since", cursor)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return "", nil, fmt.Errorf("poll returned %d: %s", resp.StatusCode, string(body))
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
// empty cursor and a nil error — first boot must not be a hard failure.
func (p *PollClient) readCursor() (string, error) {
	if p.cursorPath == "" {
		return "", nil
	}
	b, err := os.ReadFile(p.cursorPath)
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
// a crash mid-write leaves either the old cursor or the new one — never a
// half-written file.
func (p *PollClient) writeCursor(cursor string) error {
	if p.cursorPath == "" {
		return nil
	}
	dir := filepath.Dir(p.cursorPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir cursor dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(p.cursorPath)+".*.tmp")
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
	if err := os.Rename(tmpPath, p.cursorPath); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	committed = true
	return nil
}

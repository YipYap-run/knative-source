package source

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// SinkClient POSTs CloudEvents to K_SINK in binary mode, applying CEOverrides
// as extension attributes before serialization.
//
// Binary mode is the only supported egress form — ContainerSource sinks
// (Brokers, Channels, KSvcs) all accept binary and it preserves the raw data
// payload without re-envelope cost. Structured mode would re-wrap the event
// in a JSON envelope on every hop, defeating the point of binary CE.
type SinkClient struct {
	sink       string
	overrides  map[string]string
	httpClient *http.Client
}

// NewSinkClient builds a SinkClient from the loaded config. The 30s client
// timeout is generous enough for slow sinks (KServices cold-starting) while
// still bounding a dead-socket stall.
func NewSinkClient(cfg *Config) *SinkClient {
	return &SinkClient{
		sink:      cfg.Sink,
		overrides: cfg.CEOverrides,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Send POSTs ev to the sink in binary HTTP mode. Returns nil on 2xx.
// Retryable errors (5xx / 408 / 429 / transport) are wrapped so callers can
// identify them via the substring "retryable". The poll/stream client must
// NOT commit its cursor if Send returns non-nil — at-least-once delivery is
// preserved by not advancing.
//
// CEOverrides are applied as extension attributes on the outgoing event
// (mutating a copy of ev's extension set), not as envelope surgery. This
// matches the Knative ContainerSource semantics where K_CE_OVERRIDES layers
// tags like `environment=prod` onto every forwarded event.
func (s *SinkClient) Send(ctx context.Context, ev ce.Event) error {
	// Apply CEOverrides as extension attributes. SetExtension on ce.Event
	// mutates the receiver's extension map — fine here because the caller
	// doesn't reuse the event after Send.
	for k, v := range s.overrides {
		ev.SetExtension(k, v)
	}

	req, err := buildBinaryRequest(ctx, s.sink, ev)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		// Transport-level failure: DNS, dial, read, context cancel. Classify
		// as retryable — the sink is presumably reachable eventually, and
		// the cursor-commit guard is what keeps us honest on redelivery.
		return fmt.Errorf("sink: POST %s: %w (retryable)", s.sink, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusRequestTimeout,
		resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode >= 500:
		return fmt.Errorf("sink: %d (retryable): %s", resp.StatusCode, truncate(body, 256))
	default:
		// Other 4xx: malformed event, auth failure, etc. Retrying won't
		// help; surface as permanent so an operator can see it.
		return fmt.Errorf("sink: %d (permanent): %s", resp.StatusCode, truncate(body, 256))
	}
}

// buildBinaryRequest constructs an HTTP POST in CloudEvents binary mode:
//   - ce-* headers encode the envelope attributes.
//   - Body is the raw data bytes (ev.Data()).
//   - Content-Type reflects the event's datacontenttype (default application/json).
func buildBinaryRequest(ctx context.Context, sinkURL string, ev ce.Event) (*http.Request, error) {
	u, err := url.Parse(sinkURL)
	if err != nil {
		return nil, fmt.Errorf("parse sink URL: %w", err)
	}

	body := ev.Data()
	if body == nil {
		body = []byte{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// Required CE attributes.
	req.Header.Set("Ce-Specversion", ev.SpecVersion())
	req.Header.Set("Ce-Id", ev.ID())
	req.Header.Set("Ce-Source", ev.Source())
	req.Header.Set("Ce-Type", ev.Type())
	if t := ev.Time(); !t.IsZero() {
		req.Header.Set("Ce-Time", t.UTC().Format(time.RFC3339Nano))
	}
	if sub := ev.Subject(); sub != "" {
		req.Header.Set("Ce-Subject", sub)
	}
	// Content-Type from datacontenttype, default application/json.
	ct := ev.DataContentType()
	if ct == "" {
		ct = "application/json"
	}
	req.Header.Set("Content-Type", ct)

	// Extension attributes → Ce-<Name> headers. CE spec says extension
	// attribute names are lowercase; HTTP headers are case-insensitive,
	// but Title-casing the first rune keeps Go's canonical form happy.
	for k, v := range ev.Extensions() {
		name := "Ce-" + canonicalHeaderKey(k)
		req.Header.Set(name, fmt.Sprint(v))
	}

	return req, nil
}

// canonicalHeaderKey upper-cases the first rune so header names are valid
// Go-canonical HTTP field names. CE extension attribute names are lowercase
// by spec, so `alertref` → `Alertref` → `Ce-Alertref`.
func canonicalHeaderKey(k string) string {
	if k == "" {
		return ""
	}
	return strings.ToUpper(k[:1]) + k[1:]
}

// truncate returns b as a string, capped at max bytes for error logging so a
// multi-MB sink error page doesn't blow up our structured-log line.
func truncate(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}

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
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// SinkClient POSTs CloudEvents to K_SINK in binary mode, applying CEOverrides
// as extension attributes before serialization.
//
// Binary mode is the only supported egress form. ContainerSource sinks
// (Brokers, Channels, KSvcs) all accept binary and it preserves the raw data
// payload without re-envelope cost. Structured mode would re-wrap the event
// in a JSON envelope on every hop, defeating the point of binary CE.
type SinkClient struct {
	sink       string
	overrides  map[string]string
	httpClient *http.Client
	// tokenPath is the projected OIDC token file, or "" when the sink has
	// no audience and requests go out without Authorization.
	tokenPath string
}

// NewSinkClient builds a SinkClient from the loaded config. The 30s client
// timeout is generous enough for slow sinks (KServices cold-starting) while
// still bounding a dead-socket stall. With cfg.SinkRootCAs set, https sinks
// are verified against the system roots plus the sink's CA bundle.
func NewSinkClient(cfg *Config) *SinkClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    cfg.SinkRootCAs, // nil means the system roots
	}
	return &SinkClient{
		sink:      cfg.Sink,
		overrides: cfg.CEOverrides,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
			// Never follow a redirect. A 301 or 302 turns the POST into a
			// GET without the event, and that GET's 2xx would count as a
			// delivery. Go also keeps Authorization on a same-host redirect
			// from https to http. The 3xx is returned as a retryable error.
			CheckRedirect: refuseRedirect,
		},
		tokenPath: cfg.SinkTokenPath,
	}
}

// sinkToken reads the projected OIDC token. The kubelet replaces the file
// before the token expires, so it is read on every send rather than cached.
func (s *SinkClient) sinkToken() (string, error) {
	b, err := os.ReadFile(s.tokenPath)
	if err != nil {
		return "", fmt.Errorf("read OIDC token: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("read OIDC token: %s is empty", s.tokenPath)
	}
	return tok, nil
}

// Send POSTs ev to the sink in binary HTTP mode, once. It returns nil on 2xx
// and a *SinkError otherwise; SinkError.Retryable says whether another attempt
// can succeed. Send does not retry. Retrying, and the delivery guarantee, live
// in Deliverer: the adapter retries each event in place until the sink
// accepts it, the sink rejects it permanently, or the adapter stops, and it
// confirms an event to the server only after one of the first two. That gives
// at-least-once delivery to the sink (see docs/delivery.md). Duplicates are
// possible, so sinks should dedupe on ce-id.
//
// CEOverrides are applied as extension attributes on the outgoing event
// (mutating a copy of ev's extension set), not as envelope surgery. This
// matches the Knative ContainerSource semantics where K_CE_OVERRIDES layers
// tags like `environment=prod` onto every forwarded event.
func (s *SinkClient) Send(ctx context.Context, ev ce.Event) error {
	// Apply CEOverrides as extension attributes. SetExtension on ce.Event
	// mutates the receiver's extension map, so Deliverer passes a clone.
	for k, v := range s.overrides {
		ev.SetExtension(k, v)
	}

	req, err := buildBinaryRequest(ctx, s.sink, ev)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if s.tokenPath != "" {
		tok, err := s.sinkToken()
		if err != nil {
			// Retryable: the kubelet writes the projected token before the
			// container starts and keeps it fresh, so a missing or empty
			// file is a transient node problem, not a bad event.
			return &SinkError{Retryable: true, Err: fmt.Errorf("sink: %w (retryable)", err)}
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		// Transport failure: DNS, dial, read, timeout or context cancel.
		// Retryable, since the sink is presumably reachable eventually.
		return &SinkError{Retryable: true, Err: fmt.Errorf("sink: POST %s: %w (retryable)", s.sink, err)}
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	se := &SinkError{StatusCode: resp.StatusCode}
	if permanentSinkStatus(resp.StatusCode) {
		se.Err = fmt.Errorf("sink: %d (permanent): %s", resp.StatusCode, truncate(body, 256))
		return se
	}
	se.Retryable = true
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		se.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	se.Err = fmt.Errorf("sink: %d (retryable): %s", resp.StatusCode, truncate(body, 256))
	return se
}

// SinkError is the error Send returns for a failed delivery attempt.
type SinkError struct {
	// StatusCode is the sink's HTTP status, or 0 for a transport failure.
	StatusCode int
	// Retryable is false only when the sink rejected the event itself
	// (see permanentSinkStatus), so sending it again cannot succeed.
	Retryable bool
	// RetryAfter is the sink's Retry-After hint on 429 or 503, or 0.
	RetryAfter time.Duration
	Err        error
}

func (e *SinkError) Error() string { return e.Err.Error() }
func (e *SinkError) Unwrap() error { return e.Err }

// permanentSinkStatus reports whether a status means the sink rejected this
// particular event (malformed, too large, unsupported content type, or no
// such target), so a retry would get the same answer. Every other non-2xx is
// retried: 401, 403 and 409 usually mean a sink or mesh misconfiguration that
// an operator can fix, and skipping events in that state would lose them all.
func permanentSinkStatus(code int) bool {
	switch code {
	case http.StatusBadRequest,
		http.StatusNotFound,
		http.StatusGone,
		http.StatusRequestEntityTooLarge,
		http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// maxRetryAfter caps a Retry-After hint so a hostile or buggy header cannot
// park the adapter for hours.
const maxRetryAfter = 5 * time.Minute

// parseRetryAfter reads a Retry-After header in either delay-seconds or
// HTTP-date form. It returns 0 when the header is absent or invalid.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil || errors.Is(err, strconv.ErrRange) {
		if secs <= 0 {
			return 0
		}
		// Compare in seconds first: secs*time.Second overflows int64 for
		// values above about 292 years and would wrap to a bogus delay.
		if secs >= int64(maxRetryAfter/time.Second) {
			return maxRetryAfter
		}
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
		if d <= 0 {
			return 0
		}
	} else {
		return 0
	}
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d
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

	// Required CE attributes. Values pass through headerValue so a control
	// character in server data cannot make the HTTP client refuse the
	// request, which would look retryable and block the stream forever.
	req.Header.Set("Ce-Specversion", headerValue(ev.SpecVersion()))
	req.Header.Set("Ce-Id", headerValue(ev.ID()))
	req.Header.Set("Ce-Source", headerValue(ev.Source()))
	req.Header.Set("Ce-Type", headerValue(ev.Type()))
	if t := ev.Time(); !t.IsZero() {
		req.Header.Set("Ce-Time", t.UTC().Format(time.RFC3339Nano))
	}
	if sub := ev.Subject(); sub != "" {
		req.Header.Set("Ce-Subject", headerValue(sub))
	}
	if ds := ev.DataSchema(); ds != "" {
		req.Header.Set("Ce-Dataschema", headerValue(ds))
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
		req.Header.Set(name, headerValue(fmt.Sprint(v)))
	}

	return req, nil
}

// headerValue percent-encodes the control characters (0x00-0x1F and 0x7F) in
// an attribute value, following the CloudEvents HTTP binding. Go's HTTP
// client rejects a request whose header holds one of these. Every other byte
// is passed through unchanged, so values that were valid before go out
// exactly as they did.
func headerValue(v string) string {
	if !strings.ContainsFunc(v, isCTL) {
		return v
	}
	var b strings.Builder
	b.Grow(len(v) + 8)
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < 0x20 || c == 0x7f {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func isCTL(r rune) bool { return r < 0x20 || r == 0x7f }

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

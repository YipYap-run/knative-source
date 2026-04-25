package source

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// newTestEvent builds a minimal valid CloudEvent with JSON data.
func newTestEvent(t *testing.T) ce.Event {
	t.Helper()
	ev := ce.NewEvent()
	ev.SetID("ev-123")
	ev.SetSource("/yipyap/org/test")
	ev.SetType("run.yipyap.alert.firing.v1")
	ev.SetTime(time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC))
	if err := ev.SetData(ce.ApplicationJSON, map[string]string{"foo": "bar"}); err != nil {
		t.Fatalf("set data: %v", err)
	}
	return ev
}

// captureHandler records the request headers and body of the most recent request.
type captureHandler struct {
	status      int
	gotHeaders  http.Header
	gotBody     []byte
	gotMethod   string
	gotURLPath  string
	gotQueryRaw string
}

func (h *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.gotHeaders = r.Header.Clone()
	h.gotMethod = r.Method
	h.gotURLPath = r.URL.Path
	h.gotQueryRaw = r.URL.RawQuery
	h.gotBody, _ = io.ReadAll(r.Body)
	status := h.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
}

func TestSinkClient_Send_Binary_2xx(t *testing.T) {
	h := &captureHandler{status: 200}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := &Config{Sink: srv.URL}
	sc := NewSinkClient(cfg)

	ev := newTestEvent(t)
	if err := sc.Send(t.Context(), ev); err != nil {
		t.Fatalf("Send returned err: %v", err)
	}

	if h.gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", h.gotMethod)
	}
	if got := h.gotHeaders.Get("Ce-Specversion"); got != ev.SpecVersion() {
		t.Errorf("Ce-Specversion = %q, want %q", got, ev.SpecVersion())
	}
	if got := h.gotHeaders.Get("Ce-Id"); got != "ev-123" {
		t.Errorf("Ce-Id = %q, want ev-123", got)
	}
	if got := h.gotHeaders.Get("Ce-Source"); got != "/yipyap/org/test" {
		t.Errorf("Ce-Source = %q, want /yipyap/org/test", got)
	}
	if got := h.gotHeaders.Get("Ce-Type"); got != "run.yipyap.alert.firing.v1" {
		t.Errorf("Ce-Type = %q, want run.yipyap.alert.firing.v1", got)
	}
	if got := h.gotHeaders.Get("Ce-Time"); got == "" {
		t.Errorf("Ce-Time missing")
	}
	if ct := h.gotHeaders.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json*", ct)
	}
	if !strings.Contains(string(h.gotBody), `"foo":"bar"`) {
		t.Errorf("body = %q, expected to contain foo:bar", string(h.gotBody))
	}
}

func TestSinkClient_Send_AppliesOverrides(t *testing.T) {
	h := &captureHandler{status: 200}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := &Config{
		Sink:        srv.URL,
		CEOverrides: map[string]string{"environment": "prod"},
	}
	sc := NewSinkClient(cfg)

	ev := newTestEvent(t)
	if err := sc.Send(t.Context(), ev); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := h.gotHeaders.Get("Ce-Environment"); got != "prod" {
		t.Errorf("Ce-Environment = %q, want prod", got)
	}
}

func TestSinkClient_Send_ExtensionHeadersLowercase(t *testing.T) {
	h := &captureHandler{status: 200}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := &Config{Sink: srv.URL}
	sc := NewSinkClient(cfg)

	ev := newTestEvent(t)
	ev.SetExtension("alertref", "01HABCD")
	if err := sc.Send(t.Context(), ev); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := h.gotHeaders.Get("Ce-Alertref"); got != "01HABCD" {
		t.Errorf("Ce-Alertref = %q, want 01HABCD", got)
	}
}

func TestSinkClient_Send_4xx_Permanent(t *testing.T) {
	h := &captureHandler{status: 400}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := &Config{Sink: srv.URL}
	sc := NewSinkClient(cfg)

	err := sc.Send(t.Context(), newTestEvent(t))
	if err == nil {
		t.Fatal("Send: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "permanent") {
		t.Errorf("err = %v, expected 'permanent' in message", err)
	}
}

func TestSinkClient_Send_5xx_Retryable(t *testing.T) {
	h := &captureHandler{status: 503}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := &Config{Sink: srv.URL}
	sc := NewSinkClient(cfg)

	err := sc.Send(t.Context(), newTestEvent(t))
	if err == nil {
		t.Fatal("Send: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "retryable") {
		t.Errorf("err = %v, expected 'retryable' in message", err)
	}
}

func TestSinkClient_Send_408_Retryable(t *testing.T) {
	h := &captureHandler{status: 408}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := &Config{Sink: srv.URL}
	sc := NewSinkClient(cfg)

	err := sc.Send(t.Context(), newTestEvent(t))
	if err == nil {
		t.Fatal("Send: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "retryable") {
		t.Errorf("err = %v, expected 'retryable' in message", err)
	}
}

func TestSinkClient_Send_429_Retryable(t *testing.T) {
	h := &captureHandler{status: 429}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := &Config{Sink: srv.URL}
	sc := NewSinkClient(cfg)

	err := sc.Send(t.Context(), newTestEvent(t))
	if err == nil {
		t.Fatal("Send: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "retryable") {
		t.Errorf("err = %v, expected 'retryable' in message", err)
	}
}

func TestSinkClient_Send_TransportError_Retryable(t *testing.T) {
	cfg := &Config{Sink: "http://127.0.0.1:1"}
	sc := NewSinkClient(cfg)
	// Tighten timeout so the test doesn't hang if the local OS behaves oddly.
	sc.httpClient.Timeout = 2 * time.Second

	err := sc.Send(t.Context(), newTestEvent(t))
	if err == nil {
		t.Fatal("Send: expected transport error, got nil")
	}
	if !strings.Contains(err.Error(), "retryable") {
		t.Errorf("err = %v, expected 'retryable' in message", err)
	}
}

func TestSinkClient_Send_BinaryBodyIsRawData(t *testing.T) {
	h := &captureHandler{status: 200}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := &Config{Sink: srv.URL}
	sc := NewSinkClient(cfg)

	ev := newTestEvent(t)
	if err := sc.Send(t.Context(), ev); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Binary mode: body is raw data JSON — no envelope wrapping.
	body := strings.TrimSpace(string(h.gotBody))
	if !strings.HasPrefix(body, "{") {
		t.Errorf("body = %q, expected to start with {", body)
	}
	if strings.Contains(body, `"specversion"`) || strings.Contains(body, `"datacontenttype"`) {
		t.Errorf("body = %q, expected no CE envelope keys in binary mode", body)
	}
	if body != `{"foo":"bar"}` {
		t.Errorf("body = %q, want %q", body, `{"foo":"bar"}`)
	}
}

func TestSinkClient_Send_NoDataContentType_DefaultsJSON(t *testing.T) {
	h := &captureHandler{status: 200}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := &Config{Sink: srv.URL}
	sc := NewSinkClient(cfg)

	ev := ce.NewEvent()
	ev.SetID("ev-999")
	ev.SetSource("/src")
	ev.SetType("t.v1")
	// Deliberately no SetData → datacontenttype remains empty.

	if err := sc.Send(t.Context(), ev); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if ct := h.gotHeaders.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

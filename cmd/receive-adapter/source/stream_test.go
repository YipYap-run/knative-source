package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// newTestStreamClient returns a StreamClient wired to the supplied baseURL
// with defaults sensible for tests (no HTTP timeout; stream is long-lived).
func newTestStreamClient(_ *testing.T, baseURL, apiKey, filter string) *StreamClient {
	return &StreamClient{
		baseURL:    baseURL,
		apiKey:     apiKey,
		filter:     filter,
		httpClient: &http.Client{},
	}
}

// writeNDJSON writes v as a single NDJSON line and flushes. The server-side
// stream handler does the same pattern — tests mimic it here.
func writeNDJSON(w http.ResponseWriter, v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if _, err := w.Write([]byte{'\n'}); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// TestStreamClient_ReceivesEvents: server writes 2 NDJSON lines; client
// calls emit twice with correct event types. We cancel the client context
// after the second event so stream() returns.
func TestStreamClient_ReceivesEvents(t *testing.T) {
	ev1 := makeEvent(t, "run.yipyap.alert.firing")
	ev2 := makeEvent(t, "run.yipyap.alert.resolved")

	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/cloudevents-batch+json")
		w.WriteHeader(http.StatusOK)
		_ = writeNDJSON(w, ev1)
		_ = writeNDJSON(w, ev2)
		// Hold the connection open until the client cancels, or the
		// test signals shutdown.
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(stop); srv.Close() }()

	c := newTestStreamClient(t, srv.URL, "k", "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var got []string
	var mu sync.Mutex
	emit := func(_ context.Context, ev ce.Event) error {
		mu.Lock()
		got = append(got, ev.Type())
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			cancel()
		}
		return nil
	}

	_ = c.stream(ctx, emit)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %v", len(got), got)
	}
	if got[0] != "run.yipyap.alert.firing" || got[1] != "run.yipyap.alert.resolved" {
		t.Fatalf("event order/types wrong: %v", got)
	}
}

// TestStreamClient_Reconnects: server closes the connection after 1 event on
// the first call; client reconnects and receives a second event on the new
// connection. Uses run() (with reconnect loop), not stream().
func TestStreamClient_Reconnects(t *testing.T) {
	stop := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/cloudevents-batch+json")
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			_ = writeNDJSON(w, makeEvent(t, "run.yipyap.alert.firing"))
			// Close by returning from the handler — the response body
			// gets a final EOF on the client side.
			return
		}
		_ = writeNDJSON(w, makeEvent(t, "run.yipyap.alert.resolved"))
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(stop); srv.Close() }()

	c := newTestStreamClient(t, srv.URL, "k", "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var got []string
	var mu sync.Mutex
	emit := func(_ context.Context, ev ce.Event) error {
		mu.Lock()
		got = append(got, ev.Type())
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			cancel()
		}
		return nil
	}

	_ = c.run(ctx, emit)

	mu.Lock()
	defer mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("want >=2 events across reconnect, got %d: %v", len(got), got)
	}
	if got[0] != "run.yipyap.alert.firing" {
		t.Fatalf("first event wrong: %q", got[0])
	}
	found := false
	for _, t := range got[1:] {
		if t == "run.yipyap.alert.resolved" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("did not observe second-connection event: %v", got)
	}
}

// TestStreamClient_Auth: outbound GET carries Authorization: Bearer <apikey>.
func TestStreamClient_Auth(t *testing.T) {
	const key = "secret-stream-key"
	var gotAuth string
	var gotAcceptHdr string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAcceptHdr = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/cloudevents-batch+json")
		w.WriteHeader(http.StatusOK)
		// Hold briefly so client completes the request round-trip.
		<-time.After(100 * time.Millisecond)
	}))
	defer srv.Close()

	c := newTestStreamClient(t, srv.URL, key, "")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = c.stream(ctx, func(_ context.Context, _ ce.Event) error { return nil })

	if gotAuth != "Bearer "+key {
		t.Fatalf("Authorization: got %q, want %q", gotAuth, "Bearer "+key)
	}
	if gotAcceptHdr != "application/cloudevents-batch+json" {
		t.Fatalf("Accept: got %q", gotAcceptHdr)
	}
}

// TestStreamClient_ContextCancelExits: cancelling ctx causes run() to return
// promptly (within a second), not hang.
func TestStreamClient_ContextCancelExits(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/cloudevents-batch+json")
		w.WriteHeader(http.StatusOK)
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(stop); srv.Close() }()

	c := newTestStreamClient(t, srv.URL, "k", "")
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.run(ctx, func(_ context.Context, _ ce.Event) error { return nil })
	}()

	// Let the client connect before we cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("run() did not return after ctx cancel")
	}
}

// TestStreamClient_MalformedLineSkipped: the server injects a garbage line
// between two valid events; client emits only the two valid ones.
func TestStreamClient_MalformedLineSkipped(t *testing.T) {
	ev1 := makeEvent(t, "run.yipyap.alert.firing")
	ev2 := makeEvent(t, "run.yipyap.alert.resolved")

	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/cloudevents-batch+json")
		w.WriteHeader(http.StatusOK)
		_ = writeNDJSON(w, ev1)
		// Inject a line that isn't valid JSON.
		_, _ = fmt.Fprint(w, "not-a-json-envelope\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_ = writeNDJSON(w, ev2)
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(stop); srv.Close() }()

	c := newTestStreamClient(t, srv.URL, "k", "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var got []string
	var mu sync.Mutex
	emit := func(_ context.Context, ev ce.Event) error {
		mu.Lock()
		got = append(got, ev.Type())
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			cancel()
		}
		return nil
	}

	_ = c.stream(ctx, emit)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("want exactly 2 valid events (garbage line skipped), got %d: %v", len(got), got)
	}
	if got[0] != "run.yipyap.alert.firing" || got[1] != "run.yipyap.alert.resolved" {
		t.Fatalf("event order/types wrong: %v", got)
	}
}

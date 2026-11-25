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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// flakySink answers each POST with the next status in script, then with the
// last status forever. It records the ce-id of every attempt.
type flakySink struct {
	mu       sync.Mutex
	script   []int
	header   http.Header // extra response headers
	attempts []string
	onPost   func(attempt int)
}

func (f *flakySink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	f.mu.Lock()
	n := len(f.attempts)
	f.attempts = append(f.attempts, r.Header.Get("Ce-Id"))
	status := f.script[len(f.script)-1]
	if n < len(f.script) {
		status = f.script[n]
	}
	hook := f.onPost
	f.mu.Unlock()
	if hook != nil {
		hook(n + 1)
	}
	for k, v := range f.header {
		w.Header()[k] = v
	}
	w.WriteHeader(status)
}

func (f *flakySink) Attempts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.attempts...)
}

// fastDeliverer returns a Deliverer against sinkURL with millisecond backoff.
func fastDeliverer(sinkURL string, stats *Stats) *Deliverer {
	sc := NewSinkClient(&Config{Sink: sinkURL})
	sc.httpClient.Timeout = 2 * time.Second
	d := NewDeliverer(sc.Send, stats)
	d.initial = time.Millisecond
	d.cap = 5 * time.Millisecond
	return d
}

func TestDeliverer_RetriesUntilAccepted(t *testing.T) {
	sink := &flakySink{script: []int{503, 503, 202}}
	srv := httptest.NewServer(sink)
	defer srv.Close()
	stats := &Stats{}
	d := fastDeliverer(srv.URL, stats)

	ev := newTestEvent(t)
	if err := d.Deliver(t.Context(), ev); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got := sink.Attempts(); len(got) != 3 {
		t.Fatalf("attempts = %d, want 3", len(got))
	}
	if stats.Delivered.Load() != 1 || stats.Retries.Load() != 2 || stats.Rejected.Load() != 0 {
		t.Errorf("stats delivered=%d retries=%d rejected=%d, want 1/2/0",
			stats.Delivered.Load(), stats.Retries.Load(), stats.Rejected.Load())
	}
}

func TestDeliverer_PermanentRejectionSkipsAndCounts(t *testing.T) {
	for _, code := range []int{400, 404, 413, 415} {
		sink := &flakySink{script: []int{code}}
		srv := httptest.NewServer(sink)
		stats := &Stats{}
		d := fastDeliverer(srv.URL, stats)

		err := d.Deliver(t.Context(), newTestEvent(t))
		srv.Close()
		if !errors.Is(err, ErrRejected) {
			t.Errorf("%d: err = %v, want ErrRejected", code, err)
		}
		if n := len(sink.Attempts()); n != 1 {
			t.Errorf("%d: attempts = %d, want 1 (no retry)", code, n)
		}
		if stats.Rejected.Load() != 1 {
			t.Errorf("%d: rejected = %d, want 1", code, stats.Rejected.Load())
		}
	}
}

func TestDeliverer_OtherClientErrorsRetry(t *testing.T) {
	for _, code := range []int{401, 403, 408, 409, 429} {
		sink := &flakySink{script: []int{code, 200}}
		srv := httptest.NewServer(sink)
		d := fastDeliverer(srv.URL, &Stats{})
		err := d.Deliver(t.Context(), newTestEvent(t))
		srv.Close()
		if err != nil {
			t.Errorf("%d: Deliver: %v", code, err)
		}
		if n := len(sink.Attempts()); n != 2 {
			t.Errorf("%d: attempts = %d, want 2", code, n)
		}
	}
}

func TestDeliverer_HonorsRetryAfter(t *testing.T) {
	sink := &flakySink{script: []int{429, 202}, header: http.Header{"Retry-After": {"7"}}}
	srv := httptest.NewServer(sink)
	defer srv.Close()
	d := fastDeliverer(srv.URL, &Stats{})
	var waits []time.Duration
	d.sleep = func(_ context.Context, w time.Duration) error {
		waits = append(waits, w)
		return nil
	}
	if err := d.Deliver(t.Context(), newTestEvent(t)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(waits) != 1 || waits[0] != 7*time.Second {
		t.Errorf("waits = %v, want [7s]", waits)
	}
}

func TestDeliverer_BackoffIsCappedAndJittered(t *testing.T) {
	calls := 0
	d := NewDeliverer(func(context.Context, ce.Event) error {
		calls++
		if calls > 8 {
			return nil
		}
		return &SinkError{Retryable: true, Err: errors.New("boom")}
	}, &Stats{})
	d.initial = 100 * time.Millisecond
	d.cap = 400 * time.Millisecond
	var waits []time.Duration
	d.sleep = func(_ context.Context, w time.Duration) error {
		waits = append(waits, w)
		return nil
	}
	if err := d.Deliver(t.Context(), newTestEvent(t)); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	for i, w := range waits {
		if w > d.cap || w < d.initial/2 {
			t.Errorf("wait[%d] = %v, outside [%v, %v]", i, w, d.initial/2, d.cap)
		}
	}
	if last := waits[len(waits)-1]; last < d.cap/2 {
		t.Errorf("last wait %v did not grow toward the cap", last)
	}
}

func TestDeliverer_ContextCancelStopsRetry(t *testing.T) {
	sink := &flakySink{script: []int{503}}
	srv := httptest.NewServer(sink)
	defer srv.Close()
	d := fastDeliverer(srv.URL, &Stats{})
	d.initial = 50 * time.Millisecond
	d.cap = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(120*time.Millisecond, cancel)
	start := time.Now()
	err := d.Deliver(ctx, newTestEvent(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("Deliver took %v after cancel, want prompt return", el)
	}
	if n := len(sink.Attempts()); n < 2 {
		t.Errorf("attempts = %d, want retries before cancel", n)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"0":                             0,
		"-3":                            0,
		"junk":                          0,
		"12":                            12 * time.Second,
		"86400":                         maxRetryAfter,
		"Tue, 24 Nov 2026 12:00:30 GMT": 30 * time.Second,
		"Tue, 24 Nov 2026 11:00:00 GMT": 0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDeliveryContext_OutlivesStopByGrace(t *testing.T) {
	stop, cancelStop := context.WithCancel(t.Context())
	dctx, cancel := deliveryContext(stop, 80*time.Millisecond)
	defer cancel()
	cancelStop()
	select {
	case <-dctx.Done():
		t.Fatal("delivery context ended with stop; want it to outlive stop by grace")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-dctx.Done():
	case <-time.After(time.Second):
		t.Fatal("delivery context still alive long after grace")
	}
}

func TestStats_WriteMetrics(t *testing.T) {
	s := &Stats{}
	s.Rejected.Add(2)
	var b strings.Builder
	if err := s.WriteMetrics(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "yipyap_source_events_rejected_total 2\n") {
		t.Errorf("metrics output missing rejected counter:\n%s", b.String())
	}
}

// pollServer serves a scripted sequence of poll batches and records the
// since= value and source header of each request.
type pollServer struct {
	mu      sync.Mutex
	batches []pollBatch
	sinces  []string
	headers []string
	calls   int
}

type pollBatch struct {
	events []ce.Event
	next   string
}

func (p *pollServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.sinces = append(p.sinces, r.URL.Query().Get("since"))
	p.headers = append(p.headers, r.Header.Get(SourceIDHeader))
	var b pollBatch
	if p.calls < len(p.batches) {
		b = p.batches[p.calls]
	}
	p.calls++
	p.mu.Unlock()
	evs := b.events
	if evs == nil {
		evs = []ce.Event{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"events": evs, "next_cursor": b.next})
}

func (p *pollServer) snapshot() (sinces, headers []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sinces...), append([]string(nil), p.headers...)
}

// TestPollClient_FlakySinkCursorAdvancesOnlyAfterAccept: the sink fails twice
// with 503 and then accepts. The event reaches the sink once, the cursor is
// not written while the sink is failing, and the next poll confirms it.
func TestPollClient_FlakySinkCursorAdvancesOnlyAfterAccept(t *testing.T) {
	ev := makeEvent(t, "run.yipyap.alert.firing.v1")
	ps := &pollServer{batches: []pollBatch{{events: []ce.Event{ev}, next: "c1"}}}
	api := httptest.NewServer(ps)
	defer api.Close()

	c := newTestPollClient(t, api.URL, "key")
	c.sourceID = "uid-123"
	var cursorDuringFailures atomic.Value
	cursorDuringFailures.Store("")
	sink := &flakySink{script: []int{503, 503, 202}}
	sink.onPost = func(n int) {
		if n <= 2 {
			b, _ := os.ReadFile(c.cursorPath)
			if len(b) > 0 {
				cursorDuringFailures.Store(string(b))
			}
		}
	}
	sinkSrv := httptest.NewServer(sink)
	defer sinkSrv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, fastDeliverer(sinkSrv.URL, &Stats{}).Deliver) }()

	waitFor(t, func() bool {
		sinces, _ := ps.snapshot()
		return len(sinces) >= 2 && sinces[len(sinces)-1] == "c1"
	})
	cancel()
	<-done

	attempts := sink.Attempts()
	for _, id := range attempts {
		if id != ev.ID() {
			t.Errorf("unexpected ce-id %q at sink", id)
		}
	}
	if len(attempts) != 3 {
		t.Fatalf("sink attempts = %d, want 3 (503, 503, 202)", len(attempts))
	}
	if got := cursorDuringFailures.Load().(string); got != "" {
		t.Errorf("cursor was persisted as %q while the sink was failing", got)
	}
	b, err := os.ReadFile(c.cursorPath)
	if err != nil || string(b) != "c1" {
		t.Errorf("cursor file = %q (%v), want c1", b, err)
	}
	_, headers := ps.snapshot()
	for i, h := range headers {
		if h != "uid-123" {
			t.Errorf("poll %d: %s = %q, want uid-123", i, SourceIDHeader, h)
		}
	}
}

// TestPollClient_RejectedEventDoesNotBlockBatch: a 400 for the first event is
// skipped and counted, the second event is delivered and the cursor advances.
func TestPollClient_RejectedEventDoesNotBlockBatch(t *testing.T) {
	bad := makeEvent(t, "run.yipyap.alert.firing.v1")
	good := makeEvent(t, "run.yipyap.alert.resolved.v1")
	ps := &pollServer{batches: []pollBatch{{events: []ce.Event{bad, good}, next: "c1"}}}
	api := httptest.NewServer(ps)
	defer api.Close()

	sink := &flakySink{script: []int{400, 202}}
	sinkSrv := httptest.NewServer(sink)
	defer sinkSrv.Close()
	stats := &Stats{}

	c := newTestPollClient(t, api.URL, "key")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, fastDeliverer(sinkSrv.URL, stats).Deliver) }()
	waitFor(t, func() bool {
		sinces, _ := ps.snapshot()
		return len(sinces) >= 2 && sinces[len(sinces)-1] == "c1"
	})
	cancel()
	<-done

	if got := sink.Attempts(); len(got) != 2 || got[0] != bad.ID() || got[1] != good.ID() {
		t.Errorf("sink attempts = %v, want [%s %s]", got, bad.ID(), good.ID())
	}
	if stats.Rejected.Load() != 1 || stats.Delivered.Load() != 1 {
		t.Errorf("rejected=%d delivered=%d, want 1/1", stats.Rejected.Load(), stats.Delivered.Load())
	}
}

// ackRecorder records POSTs to AckPath.
type ackRecorder struct {
	mu      sync.Mutex
	bodies  []ackRequest
	headers []string
	status  int
}

func (a *ackRecorder) handle(w http.ResponseWriter, r *http.Request) {
	var body ackRequest
	_ = json.NewDecoder(r.Body).Decode(&body)
	a.mu.Lock()
	a.bodies = append(a.bodies, body)
	a.headers = append(a.headers, r.Header.Get(SourceIDHeader))
	status := a.status
	a.mu.Unlock()
	if status == 0 {
		status = http.StatusNoContent
	}
	w.WriteHeader(status)
}

func (a *ackRecorder) snapshot() ([]ackRequest, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ackRequest(nil), a.bodies...), append([]string(nil), a.headers...)
}

// TestPollClient_ShutdownFinishesInFlightAndConfirms: SIGTERM arrives while
// the sink is still failing. The adapter keeps retrying within the grace
// window, delivers the event, and sends the cursor the server has not seen
// to the ack endpoint before Run returns.
func TestPollClient_ShutdownFinishesInFlightAndConfirms(t *testing.T) {
	ev := makeEvent(t, "run.yipyap.alert.firing.v1")
	ps := &pollServer{batches: []pollBatch{{events: []ce.Event{ev}, next: "c1"}}}
	acks := &ackRecorder{}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/cloudevents/poll", ps)
	mux.HandleFunc(AckPath, acks.handle)
	api := httptest.NewServer(mux)
	defer api.Close()

	ctx, cancel := context.WithCancel(t.Context())
	sink := &flakySink{script: []int{503, 503, 503, 202}}
	sink.onPost = func(n int) {
		if n == 1 {
			cancel() // SIGTERM during the first attempt
		}
	}
	sinkSrv := httptest.NewServer(sink)
	defer sinkSrv.Close()

	cfg := &Config{BaseURL: api.URL, APIKey: "key", SourceID: "uid-9", ShutdownGrace: 5 * time.Second}
	c := NewPollClient(cfg)
	c.interval = 5 * time.Millisecond
	c.cursorPath = ""

	if err := c.Run(ctx, fastDeliverer(sinkSrv.URL, &Stats{}).Deliver); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	if n := len(sink.Attempts()); n != 4 {
		t.Errorf("sink attempts = %d, want 4 (delivery continued after shutdown)", n)
	}
	bodies, headers := acks.snapshot()
	if len(bodies) != 1 || bodies[0].Cursor != "c1" {
		t.Fatalf("acks = %+v, want one with cursor c1", bodies)
	}
	if headers[0] != "uid-9" {
		t.Errorf("ack %s = %q, want uid-9", SourceIDHeader, headers[0])
	}
}

// TestStreamClient_RetriesThenConfirms: the sink fails then accepts; the
// event is confirmed only after acceptance, the cursor extension is not
// forwarded, and a reconnect sends since=<confirmed cursor>.
func TestStreamClient_RetriesThenConfirms(t *testing.T) {
	ev := makeEvent(t, "run.yipyap.alert.firing.v1")
	ev.SetExtension(CursorExtension, "s-42")

	var mu sync.Mutex
	var sinces, streamHeaders []string
	acks := &ackRecorder{}
	var acceptedBeforeAck atomic.Bool
	sink := &flakySink{script: []int{503, 500, 202}}
	ackHandler := func(w http.ResponseWriter, r *http.Request) {
		acceptedBeforeAck.Store(len(sink.Attempts()) == 3)
		acks.handle(w, r)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/cloudevents/stream", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sinces = append(sinces, r.URL.Query().Get("since"))
		streamHeaders = append(streamHeaders, r.Header.Get(SourceIDHeader))
		first := len(sinces) == 1
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		if first {
			_ = writeNDJSON(w, ev)
		}
	})
	mux.HandleFunc(AckPath, ackHandler)
	api := httptest.NewServer(mux)
	defer api.Close()

	var forwarded atomic.Value
	forwarded.Store("")
	sinkMux := http.NewServeMux()
	sinkMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		forwarded.Store(r.Header.Get("Ce-" + canonicalHeaderKey(CursorExtension)))
		sink.ServeHTTP(w, r)
	})
	sinkSrv := httptest.NewServer(sinkMux)
	defer sinkSrv.Close()

	cfg := &Config{BaseURL: api.URL, APIKey: "key", SourceID: "uid-7"}
	c := NewStreamClient(cfg)
	if err := c.stream(t.Context(), fastDeliverer(sinkSrv.URL, &Stats{}).Deliver); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if err := c.stream(t.Context(), fastDeliverer(sinkSrv.URL, &Stats{}).Deliver); err != nil {
		t.Fatalf("second stream: %v", err)
	}

	if n := len(sink.Attempts()); n != 3 {
		t.Errorf("sink attempts = %d, want 3", n)
	}
	if !acceptedBeforeAck.Load() {
		t.Error("ack was sent before the sink accepted the event")
	}
	bodies, ackHeaders := acks.snapshot()
	if len(bodies) != 1 || bodies[0].Cursor != "s-42" {
		t.Fatalf("acks = %+v, want one with cursor s-42", bodies)
	}
	if ackHeaders[0] != "uid-7" {
		t.Errorf("ack %s = %q, want uid-7", SourceIDHeader, ackHeaders[0])
	}
	if got := forwarded.Load().(string); got != "" {
		t.Errorf("cursor extension forwarded to sink as %q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sinces) != 2 || sinces[0] != "" || sinces[1] != "s-42" {
		t.Errorf("since values = %q, want [\"\" \"s-42\"]", sinces)
	}
	for i, h := range streamHeaders {
		if h != "uid-7" {
			t.Errorf("stream %d: %s = %q, want uid-7", i, SourceIDHeader, h)
		}
	}
}

// TestStreamClient_AbandonedEventNotConfirmed: when delivery is abandoned the
// event is not acked and the stream connection ends.
func TestStreamClient_AbandonedEventNotConfirmed(t *testing.T) {
	ev := makeEvent(t, "run.yipyap.alert.firing.v1")
	acks := &ackRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/cloudevents/stream", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = writeNDJSON(w, ev)
	})
	mux.HandleFunc(AckPath, acks.handle)
	api := httptest.NewServer(mux)
	defer api.Close()

	c := NewStreamClient(&Config{BaseURL: api.URL, APIKey: "key"})
	err := c.stream(t.Context(), func(context.Context, ce.Event) error { return context.Canceled })
	if err == nil {
		t.Fatal("stream: want error for abandoned event")
	}
	if bodies, _ := acks.snapshot(); len(bodies) != 0 {
		t.Errorf("acks = %+v, want none", bodies)
	}
}

// TestStreamClient_RejectedEventConfirmed: a permanent rejection is confirmed
// so the server stops redelivering it.
func TestStreamClient_RejectedEventConfirmed(t *testing.T) {
	ev := makeEvent(t, "run.yipyap.alert.firing.v1")
	ev.SetExtension(CursorExtension, "s-1")
	acks := &ackRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/cloudevents/stream", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = writeNDJSON(w, ev)
	})
	mux.HandleFunc(AckPath, acks.handle)
	api := httptest.NewServer(mux)
	defer api.Close()
	sinkSrv := httptest.NewServer(&flakySink{script: []int{415}})
	defer sinkSrv.Close()
	stats := &Stats{}

	c := NewStreamClient(&Config{BaseURL: api.URL, APIKey: "key"})
	if err := c.stream(t.Context(), fastDeliverer(sinkSrv.URL, stats).Deliver); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if stats.Rejected.Load() != 1 {
		t.Errorf("rejected = %d, want 1", stats.Rejected.Load())
	}
	if bodies, _ := acks.snapshot(); len(bodies) != 1 || bodies[0].Cursor != "s-1" {
		t.Errorf("acks = %+v, want one with cursor s-1", bodies)
	}
}

func TestConfirmer_UnsupportedServerDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := newConfirmer(&Config{BaseURL: srv.URL, APIKey: "k"})
	c.stats = &Stats{}
	if err := c.ack(t.Context(), "c1"); err == nil {
		t.Error("ack: want error on 404")
	}
	if calls.Load() != 1 || c.stats.AckErrors.Load() != 1 {
		t.Errorf("calls=%d ackErrors=%d, want 1/1", calls.Load(), c.stats.AckErrors.Load())
	}
}

func TestConfirmer_RetriesTransientFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := newConfirmer(&Config{BaseURL: srv.URL, APIKey: "k"})
	if err := c.ack(t.Context(), "c1"); err != nil {
		t.Errorf("ack: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestConfirmer_SendsOnlyCursor(t *testing.T) {
	type seen struct {
		cursor   string
		sourceID string
		status   int
	}
	var mu sync.Mutex
	var got seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Cursor string `json:"cursor"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		status := http.StatusNoContent
		if err := dec.Decode(&body); err != nil {
			status = http.StatusBadRequest
		}
		mu.Lock()
		got = seen{cursor: body.Cursor, sourceID: r.Header.Get(SourceIDHeader), status: status}
		mu.Unlock()
		w.WriteHeader(status)
	}))
	defer srv.Close()

	c := newConfirmer(&Config{BaseURL: srv.URL, APIKey: "k", SourceID: "uid-1"})
	if err := c.ack(t.Context(), "c1"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.status != http.StatusNoContent || got.cursor != "c1" || got.sourceID != "uid-1" {
		t.Errorf("ack = %+v, want 204 for cursor c1 from uid-1", got)
	}
}

func TestResolveMode_SendsSourceIDHeader(t *testing.T) {
	var got atomic.Value
	got.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get(SourceIDHeader))
		_ = json.NewEncoder(w).Encode(sourceConfigHint{RecommendedMode: "poll"})
	}))
	defer srv.Close()
	cfg := &Config{BaseURL: srv.URL, APIKey: "k", SourceID: "uid-1"}
	if _, err := ResolveMode(t.Context(), cfg, srv.Client(), time.Now); err != nil {
		t.Fatal(err)
	}
	if got.Load().(string) != "uid-1" {
		t.Errorf("%s = %q, want uid-1", SourceIDHeader, got.Load())
	}
}

// waitFor polls cond until it is true or the test times out.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

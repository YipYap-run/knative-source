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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// TestSinkClient_Send_ControlCharsInHeaderValues: an attribute value with a
// control character (a newline in a subject, say) used to make the HTTP
// client refuse the request. That looked like a transport error, so the
// event was retried forever and blocked every event behind it. The value is
// now percent-encoded as the CloudEvents HTTP binding asks.
func TestSinkClient_Send_ControlCharsInHeaderValues(t *testing.T) {
	h := &captureHandler{status: http.StatusAccepted}
	srv := httptest.NewServer(h)
	defer srv.Close()

	ev := newTestEvent(t)
	ev.SetSubject("disk full\non db-1")
	ev.SetExtension("note", "a\x01b\x7fc")

	sc := NewSinkClient(&Config{Sink: srv.URL})
	if err := sc.Send(context.Background(), ev); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got, want := h.gotHeaders.Get("Ce-Subject"), "disk full%0Aon db-1"; got != want {
		t.Errorf("Ce-Subject = %q, want %q", got, want)
	}
	if got, want := h.gotHeaders.Get("Ce-Note"), "a%01b%7Fc"; got != want {
		t.Errorf("Ce-Note = %q, want %q", got, want)
	}
}

// TestSinkClient_Send_PlainValuesUnchanged: values without control
// characters, including non-ASCII text and '%', go out exactly as before.
func TestSinkClient_Send_PlainValuesUnchanged(t *testing.T) {
	h := &captureHandler{status: http.StatusAccepted}
	srv := httptest.NewServer(h)
	defer srv.Close()

	ev := newTestEvent(t)
	ev.SetSubject("CPU 95% on café-1")
	sc := NewSinkClient(&Config{Sink: srv.URL})
	if err := sc.Send(context.Background(), ev); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := h.gotHeaders.Get("Ce-Subject"); got != "CPU 95% on café-1" {
		t.Errorf("Ce-Subject = %q", got)
	}
}

// TestParseRetryAfter_HugeSecondsCapped: a delay-seconds value whose
// nanosecond count overflows int64 is capped, not wrapped.
func TestParseRetryAfter_HugeSecondsCapped(t *testing.T) {
	for _, v := range []string{"9223372036", "18446744073", "9223372036854775807"} {
		if got := parseRetryAfter(v, time.Now()); got != maxRetryAfter {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", v, got, maxRetryAfter)
		}
	}
}

// TestConfirmer_UnsupportedEndpointBacksOff: against a server without the
// ack endpoint, the adapter does not POST a doomed confirmation for every
// event. It probes again after ackProbeInterval, so it starts confirming
// once the server ships the endpoint.
func TestConfirmer_UnsupportedEndpointBacksOff(t *testing.T) {
	var hits atomic.Int32
	var status atomic.Int32
	status.Store(http.StatusNotFound)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()

	now := time.Unix(1_700_000_000, 0)
	c := newConfirmer(&Config{BaseURL: srv.URL, APIKey: "k"})
	c.stats = &Stats{}
	c.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		_ = c.ack(context.Background(), "c1")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("ack requests while unsupported = %d, want 1", n)
	}

	status.Store(http.StatusNoContent)
	now = now.Add(ackProbeInterval + time.Second)
	if err := c.ack(context.Background(), "c1"); err != nil {
		t.Fatalf("ack after probe interval: %v", err)
	}
	_ = c.ack(context.Background(), "c2")
	if n := hits.Load(); n != 3 {
		t.Errorf("ack requests = %d, want 3 (probe, then normal acking)", n)
	}
}

// TestPollClient_CursorKeptAcrossSessionsWhenPersistFails: Run reuses one
// PollClient across mode refreshes. If the cursor file cannot be written,
// the next session still resumes from the cursor in memory instead of
// rewinding to the last one on disk.
func TestPollClient_CursorKeptAcrossSessionsWhenPersistFails(t *testing.T) {
	ev := makeEvent(t, "run.yipyap.alert.firing.v1")
	ps := &pollServer{batches: []pollBatch{{events: []ce.Event{ev}, next: "c1"}}}
	api := httptest.NewServer(ps)
	defer api.Close()
	session1, endSession1 := context.WithCancel(t.Context())
	sink := &flakySink{script: []int{202}}
	sink.onPost = func(int) { endSession1() } // refresh right after the batch lands
	sinkSrv := httptest.NewServer(sink)
	defer sinkSrv.Close()

	c := newTestPollClient(t, api.URL, "key")
	// A regular file as the parent directory makes every write fail.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c.cursorPath = filepath.Join(blocker, "cursor")
	d := fastDeliverer(sinkSrv.URL, &Stats{})

	_ = c.runWith(session1, t.Context(), d.Deliver)

	session2, endSession2 := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.runWith(session2, t.Context(), d.Deliver) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if s, _ := ps.snapshot(); len(s) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the second session to poll")
		}
		time.Sleep(5 * time.Millisecond)
	}
	endSession2()
	<-done

	sinces, _ := ps.snapshot()
	if sinces[0] != "" || sinces[1] != "c1" {
		t.Errorf("sinces = %q, want [\"\" \"c1\" ...] (cursor kept in memory)", sinces)
	}
}

// TestStreamClient_PersistsConfirmedCursor: the stream cursor is written to
// disk once confirmed, so a restarted container reconnects with since= the
// last confirmed event instead of no cursor at all.
func TestStreamClient_PersistsConfirmedCursor(t *testing.T) {
	ev := makeEvent(t, "run.yipyap.alert.firing.v1")
	ev.SetExtension(CursorExtension, "s-7")
	var sinces []string
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/cloudevents/stream", func(w http.ResponseWriter, r *http.Request) {
		sinces = append(sinces, r.URL.Query().Get("since"))
		w.WriteHeader(http.StatusOK)
		if calls.Add(1) == 1 {
			_ = writeNDJSON(w, ev)
		}
	})
	acks := &ackRecorder{}
	mux.HandleFunc(AckPath, acks.handle)
	api := httptest.NewServer(mux)
	defer api.Close()
	sinkSrv := httptest.NewServer(&flakySink{script: []int{202}})
	defer sinkSrv.Close()

	cursorPath := filepath.Join(t.TempDir(), "cursor")
	cfg := &Config{BaseURL: api.URL, APIKey: "key", CursorPath: cursorPath}
	d := fastDeliverer(sinkSrv.URL, &Stats{})

	first := NewStreamClient(cfg)
	if err := first.stream(t.Context(), d.Deliver); err != nil {
		t.Fatalf("first stream: %v", err)
	}
	// A new client models a restarted container.
	second := NewStreamClient(cfg)
	if err := second.stream(t.Context(), d.Deliver); err != nil {
		t.Fatalf("second stream: %v", err)
	}
	if len(sinces) != 2 || sinces[0] != "" || sinces[1] != "s-7" {
		t.Errorf("sinces = %q, want [\"\" \"s-7\"]", sinces)
	}
}

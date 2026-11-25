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
	"sync/atomic"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
)

// TestPollClient_RefreshDoesNotAbandonBatch: a mode refresh ends the session
// context while the sink is still failing. Delivery runs on the process-wide
// delivery context, so the event keeps retrying past the (tiny) per-client
// grace, reaches the sink, and the cursor advances.
func TestPollClient_RefreshDoesNotAbandonBatch(t *testing.T) {
	ev := makeEvent(t, "run.yipyap.alert.firing.v1")
	ps := &pollServer{batches: []pollBatch{{events: []ce.Event{ev}, next: "c1"}}}
	api := httptest.NewServer(ps)
	defer api.Close()

	session, endSession := context.WithCancel(t.Context())
	sink := &flakySink{script: []int{503, 503, 503, 202}}
	sink.onPost = func(n int) {
		if n == 1 {
			endSession() // refresh window elapses mid-retry
		}
	}
	sinkSrv := httptest.NewServer(sink)
	defer sinkSrv.Close()

	c := newTestPollClient(t, api.URL, "key")
	c.grace = time.Nanosecond
	d := fastDeliverer(sinkSrv.URL, &Stats{})
	d.initial = 20 * time.Millisecond
	d.cap = 20 * time.Millisecond

	_ = c.runWith(session, t.Context(), d.Deliver)

	if n := len(sink.Attempts()); n != 4 {
		t.Errorf("sink attempts = %d, want 4 (delivery must survive a refresh)", n)
	}
	if b, err := os.ReadFile(c.cursorPath); err != nil || string(b) != "c1" {
		t.Errorf("cursor file = %q (%v), want c1", b, err)
	}
}

// TestStreamClient_RefreshDoesNotAbandonEvent: as above for stream mode. The
// event is delivered and confirmed even though the connection context ended.
func TestStreamClient_RefreshDoesNotAbandonEvent(t *testing.T) {
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

	session, endSession := context.WithCancel(t.Context())
	sink := &flakySink{script: []int{503, 503, 503, 202}}
	sink.onPost = func(n int) {
		if n == 1 {
			endSession()
		}
	}
	sinkSrv := httptest.NewServer(sink)
	defer sinkSrv.Close()

	c := NewStreamClient(&Config{BaseURL: api.URL, APIKey: "key"})
	c.grace = time.Nanosecond
	d := fastDeliverer(sinkSrv.URL, &Stats{})
	d.initial = 20 * time.Millisecond
	d.cap = 20 * time.Millisecond

	_ = c.runWith(session, t.Context(), d.Deliver)

	if n := len(sink.Attempts()); n != 4 {
		t.Errorf("sink attempts = %d, want 4 (delivery must survive a refresh)", n)
	}
	if bodies, _ := acks.snapshot(); len(bodies) != 1 || bodies[0].Cursor != "s-1" {
		t.Errorf("acks = %+v, want one with cursor s-1", bodies)
	}
	if c.lastCursor != "s-1" {
		t.Errorf("lastCursor = %q, want s-1", c.lastCursor)
	}
}

// TestStreamClient_StopsBeforeBufferedEventsOnShutdown: two events arrive in
// one read. Shutdown during the first must not start delivering the second,
// even though it is already buffered and the grace window is open.
func TestStreamClient_StopsBeforeBufferedEventsOnShutdown(t *testing.T) {
	first := makeEvent(t, "run.yipyap.alert.firing.v1")
	first.SetExtension(CursorExtension, "s-1")
	second := makeEvent(t, "run.yipyap.alert.resolved.v1")
	// Serve both events in a single write so they share one buffer.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/cloudevents/stream", func(w http.ResponseWriter, r *http.Request) {
		a, _ := first.MarshalJSON()
		b, _ := second.MarshalJSON()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append(append(append(a, '\n'), b...), '\n'))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	acks := &ackRecorder{}
	mux.HandleFunc(AckPath, acks.handle)
	api := httptest.NewServer(mux)
	defer api.Close()

	ctx, cancel := context.WithCancel(t.Context())
	var emitted atomic.Int32
	emit := func(context.Context, ce.Event) error {
		emitted.Add(1)
		cancel() // SIGTERM while the first event is in flight
		return nil
	}
	c := NewStreamClient(&Config{BaseURL: api.URL, APIKey: "key", ShutdownGrace: 5 * time.Second})
	_ = c.stream(ctx, emit)

	if n := emitted.Load(); n != 1 {
		t.Errorf("emitted %d events, want 1 (buffered event delivered after shutdown)", n)
	}
	if bodies, _ := acks.snapshot(); len(bodies) != 1 || bodies[0].Cursor != "s-1" {
		t.Errorf("acks = %+v, want one with cursor s-1", bodies)
	}
}

// TestConfirmer_BudgetBoundsShutdown: a server that never answers cannot hold
// the adapter past the ack budget, even with a cancelled context.
func TestConfirmer_BudgetBoundsShutdown(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)

	c := newConfirmer(&Config{BaseURL: srv.URL, APIKey: "k"})
	c.stats = &Stats{}
	c.ackBudget = 150 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	start := time.Now()
	if err := c.ack(ctx, "c1"); err == nil {
		t.Error("ack: want error when the server never answers")
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("ack took %v, want it bounded by the budget", took)
	}
	if c.stats.AckErrors.Load() != 1 {
		t.Errorf("ackErrors = %d, want 1", c.stats.AckErrors.Load())
	}
}

// TestShutdownFitsTerminationGrace: the default drain plus the confirmation
// budget, with slack for exit, fits in the adapter pod's
// terminationGracePeriodSeconds (resources.AdapterTerminationGracePeriodSeconds,
// 40s). Update both if either changes.
func TestShutdownFitsTerminationGrace(t *testing.T) {
	const podGrace = 40 * time.Second
	if MaxShutdownGrace+ackBudget+5*time.Second > podGrace {
		t.Errorf("shutdown grace %v + ack budget %v + 5s slack exceeds pod grace %v",
			MaxShutdownGrace, ackBudget, podGrace)
	}
}

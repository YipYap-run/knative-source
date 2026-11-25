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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"

	"github.com/YipYap-run/knative-source/pkg/adapterstatus"
)

// fakeClock is a settable clock for Health tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func statusErr(code int) error {
	return &APIStatusError{Op: "poll", StatusCode: code}
}

func TestHealth(t *testing.T) {
	transportErr := errors.New("dial tcp: connection refused")
	tests := []struct {
		name       string
		steps      func(h *Health, c *fakeClock)
		wantReady  bool
		wantReason string
		wantMsg    string
	}{{
		name:      "fresh tracker is ready",
		steps:     func(*Health, *fakeClock) {},
		wantReady: true,
	}, {
		name: "401 below the threshold stays ready",
		steps: func(h *Health, _ *fakeClock) {
			for range AuthFailureThreshold - 1 {
				h.APIFailed(statusErr(401))
			}
		},
		wantReady: true,
	}, {
		name: "consecutive 401s reject the key",
		steps: func(h *Health, _ *fakeClock) {
			for range AuthFailureThreshold {
				h.APIFailed(statusErr(401))
			}
		},
		wantReason: adapterstatus.ReasonAPIKeyRejected,
		wantMsg:    "API key rejected (401)",
	}, {
		name: "consecutive 403s report forbidden",
		steps: func(h *Health, _ *fakeClock) {
			for range AuthFailureThreshold {
				h.APIFailed(statusErr(403))
			}
		},
		wantReason: adapterstatus.ReasonAPIForbidden,
		wantMsg:    "API key forbidden (403)",
	}, {
		name: "a transport error between 401s does not reset the count",
		steps: func(h *Health, _ *fakeClock) {
			h.APIFailed(statusErr(401))
			h.APIFailed(transportErr)
			h.APIFailed(statusErr(401))
			h.APIFailed(statusErr(401))
		},
		wantReason: adapterstatus.ReasonAPIKeyRejected,
		wantMsg:    "API key rejected (401)",
	}, {
		name: "a 500 between 401s resets the count",
		steps: func(h *Health, _ *fakeClock) {
			h.APIFailed(statusErr(401))
			h.APIFailed(statusErr(401))
			h.APIFailed(statusErr(500))
			h.APIFailed(statusErr(401))
		},
		wantReady: true,
	}, {
		name: "success after rejection recovers",
		steps: func(h *Health, _ *fakeClock) {
			for range AuthFailureThreshold {
				h.APIFailed(statusErr(401))
			}
			h.APIOK()
		},
		wantReady: true,
	}, {
		name: "transport errors within the window stay ready",
		steps: func(h *Health, c *fakeClock) {
			c.advance(APIUnreachableAfter - time.Second)
			h.APIFailed(transportErr)
		},
		wantReady: true,
	}, {
		name: "no success within the window is unreachable",
		steps: func(h *Health, c *fakeClock) {
			h.APIFailed(transportErr)
			c.advance(APIUnreachableAfter)
			h.APIFailed(transportErr)
		},
		wantReason: adapterstatus.ReasonAPIUnreachable,
		wantMsg:    "cannot reach the YipYap API",
	}, {
		name: "server errors past the window name the status",
		steps: func(h *Health, c *fakeClock) {
			c.advance(APIUnreachableAfter)
			h.APIFailed(statusErr(502))
		},
		wantReason: adapterstatus.ReasonAPIUnreachable,
		wantMsg:    "YipYap API returned an error (502)",
	}, {
		name: "slow poll interval stretches the window",
		steps: func(h *Health, c *fakeClock) {
			h.SetPollInterval(10 * time.Minute)
			c.advance(APIUnreachableAfter + time.Minute)
			h.APIFailed(transportErr)
		},
		wantReady: true,
	}, {
		name: "a quiet open stream is reachable",
		steps: func(h *Health, c *fakeClock) {
			h.StreamConnected()
			c.advance(time.Hour)
		},
		wantReady: true,
	}, {
		name: "the window starts when the stream closes",
		steps: func(h *Health, c *fakeClock) {
			h.StreamConnected()
			c.advance(time.Hour)
			h.StreamClosed()
			c.advance(APIUnreachableAfter - time.Second)
			h.APIFailed(transportErr)
		},
		wantReady: true,
	}, {
		name: "sink failing past the window is unavailable",
		steps: func(h *Health, c *fakeClock) {
			h.SinkFailed(&SinkError{StatusCode: 503, Retryable: true})
			c.advance(SinkUnavailableAfter)
			h.SinkFailed(&SinkError{StatusCode: 503, Retryable: true})
		},
		wantReason: adapterstatus.ReasonSinkUnavailable,
		wantMsg:    "sink is not accepting events (503)",
	}, {
		name: "unreachable sink",
		steps: func(h *Health, c *fakeClock) {
			h.SinkFailed(errors.New("connection refused"))
			c.advance(SinkUnavailableAfter)
		},
		wantReason: adapterstatus.ReasonSinkUnavailable,
		wantMsg:    "sink is unreachable",
	}, {
		name: "sink recovers",
		steps: func(h *Health, c *fakeClock) {
			h.SinkFailed(&SinkError{StatusCode: 503, Retryable: true})
			c.advance(SinkUnavailableAfter)
			h.SinkOK()
		},
		wantReady: true,
	}, {
		name: "a rejected key outranks a failing sink",
		steps: func(h *Health, c *fakeClock) {
			h.SinkFailed(&SinkError{StatusCode: 503, Retryable: true})
			c.advance(SinkUnavailableAfter)
			for range AuthFailureThreshold {
				h.APIFailed(statusErr(401))
			}
		},
		wantReason: adapterstatus.ReasonAPIKeyRejected,
		wantMsg:    "API key rejected (401)",
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeClock()
			h := NewHealth(c.now)
			tc.steps(h, c)
			got := h.Status()
			if got.Ready != tc.wantReady || got.Reason != tc.wantReason || got.Message != tc.wantMsg {
				t.Fatalf("Status() = {ready:%v reason:%q message:%q}, want {ready:%v reason:%q message:%q}",
					got.Ready, got.Reason, got.Message, tc.wantReady, tc.wantReason, tc.wantMsg)
			}
		})
	}
}

func TestHealth_NilIsReadyAndSafe(t *testing.T) {
	var h *Health
	h.APIOK()
	h.APIFailed(statusErr(401))
	h.StreamConnected()
	h.StreamClosed()
	h.SinkFailed(errors.New("x"))
	h.SinkOK()
	h.SetPollInterval(time.Second)
	if !h.Status().Ready {
		t.Fatal("nil Health must report ready")
	}
}

func rejectedHealth() *Health {
	h := NewHealth(nil)
	for range AuthFailureThreshold {
		h.APIFailed(statusErr(http.StatusUnauthorized))
	}
	return h
}

func TestReadyzHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHealth(nil).ReadyzHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready tracker: status %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	rejectedHealth().ReadyzHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("rejected key: status %d, want 503", rec.Code)
	}
	if want := "APIKeyRejected: API key rejected (401)"; rec.Body.String() != want {
		t.Fatalf("body %q, want %q", rec.Body.String(), want)
	}
}

func TestStatusHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	rejectedHealth().StatusHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, adapterstatus.Path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 even when not ready", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q", ct)
	}
	var got adapterstatus.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Ready || got.Reason != adapterstatus.ReasonAPIKeyRejected || got.Message != "API key rejected (401)" {
		t.Fatalf("got %+v", got)
	}

	rec = httptest.NewRecorder()
	rejectedHealth().StatusHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, adapterstatus.Path, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: status %d, want 405", rec.Code)
	}
}

// TestPollClient_HealthTracksAuth drives a real PollClient against a server
// that answers 401 and then 200, and checks the tracker turns not ready and
// then recovers.
func TestPollClient_HealthTracksAuth(t *testing.T) {
	var authorized atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !authorized.Load() {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[],"next_cursor":""}`))
	}))
	defer srv.Close()

	h := NewHealth(nil)
	c := newTestPollClient(t, srv.URL, "bad-key")
	c.health = h
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Run(ctx, func(context.Context, ce.Event) error { return nil })
	}()
	defer func() { cancel(); <-done }()

	waitFor(t, func() bool { return h.Status().Reason == adapterstatus.ReasonAPIKeyRejected })
	authorized.Store(true)
	waitFor(t, func() bool { return h.Status().Ready })
}

// TestStreamClient_HealthTracksForbidden checks the stream transport feeds
// the tracker too.
func TestStreamClient_HealthTracksForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	h := NewHealth(nil)
	s := newTestStreamClient(t, srv.URL, "key")
	s.health = h
	for range AuthFailureThreshold {
		_, err := s.streamOnce(t.Context(), t.Context(), func(context.Context, ce.Event) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "stream returned 403") {
			t.Fatalf("streamOnce err = %v", err)
		}
	}
	if got := h.Status(); got.Reason != adapterstatus.ReasonAPIForbidden {
		t.Fatalf("status %+v, want APIForbidden", got)
	}
}

// TestDeliverer_FeedsSinkHealth checks failed and successful sink attempts
// reach the tracker.
func TestDeliverer_FeedsSinkHealth(t *testing.T) {
	c := newFakeClock()
	h := NewHealth(c.now)
	var calls atomic.Int32
	d := NewDeliverer(func(context.Context, ce.Event) error {
		if calls.Add(1) < 3 {
			return &SinkError{StatusCode: 503, Retryable: true}
		}
		return nil
	}, &Stats{})
	d.health = h
	d.sleep = func(context.Context, time.Duration) error {
		c.advance(SinkUnavailableAfter)
		if got := h.Status(); got.Reason != adapterstatus.ReasonSinkUnavailable {
			t.Errorf("during retries: status %+v, want SinkUnavailable", got)
		}
		return nil
	}
	ev := ce.NewEvent()
	ev.SetID("1")
	ev.SetType("t")
	ev.SetSource("s")
	if err := d.Deliver(t.Context(), ev); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !h.Status().Ready {
		t.Fatalf("after success: %+v, want ready", h.Status())
	}
}

// TestPollClient_AuthRetryShortensSlowInterval checks that with a slow poll
// interval the client retries a 401 sooner, so the key is reported as
// rejected after a few seconds instead of AuthFailureThreshold intervals,
// and then returns to its normal interval.
func TestPollClient_AuthRetryShortensSlowInterval(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	h := NewHealth(nil)
	c := newTestPollClient(t, srv.URL, "bad-key")
	c.health = h
	c.interval = 400 * time.Millisecond
	c.authRetry = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		_ = c.Run(ctx, func(context.Context, ce.Event) error { return nil })
	}()
	defer func() { cancel(); <-done }()

	waitFor(t, func() bool { return h.Status().Reason == adapterstatus.ReasonAPIKeyRejected })
	// Without the short retry this takes three full intervals (1.2s).
	if el := time.Since(start); el >= 3*c.interval-100*time.Millisecond {
		t.Fatalf("key rejected after %v; want about one interval", el)
	}
	// Past the threshold the client is back on its normal interval.
	n := hits.Load()
	time.Sleep(150 * time.Millisecond)
	if got := hits.Load(); got > n+1 {
		t.Fatalf("requests grew from %d to %d within 150ms after the threshold; want the normal interval", n, got)
	}
}

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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/YipYap-run/knative-source/pkg/adapterstatus"
)

// Readiness thresholds. The adapter stays ready through short blips and
// turns not ready only when a problem persists, so a single 401 during a
// key rotation does not flip the source.
const (
	// AuthFailureThreshold is how many consecutive 401 (or 403) answers from
	// the YipYap API make the adapter not ready.
	AuthFailureThreshold = 3

	// APIUnreachableAfter is how long the adapter may go without a
	// successful API round trip, while its requests are failing, before it
	// turns not ready. It is stretched to three poll intervals for slow
	// poll schedules.
	APIUnreachableAfter = 5 * time.Minute

	// SinkUnavailableAfter is how long one event may keep failing at the
	// sink before the adapter turns not ready.
	SinkUnavailableAfter = 2 * time.Minute
)

// APIStatusError is a non-200 answer from the YipYap API.
type APIStatusError struct {
	Op         string // "poll" or "stream"
	StatusCode int
	Body       string // first 64KiB of the response, for logs only
}

func (e *APIStatusError) Error() string {
	return fmt.Sprintf("%s returned %d: %s", e.Op, e.StatusCode, e.Body)
}

// Health tracks the outcome of the adapter's API round trips and sink
// deliveries and turns it into a readiness verdict. It only affects
// readiness: liveness (/healthz) never consults it, so a bad API key makes
// the source NotReady without putting the pod in a restart loop. All
// methods are safe for concurrent use and on a nil *Health (no-ops).
type Health struct {
	mu  sync.Mutex
	now func() time.Time

	// lastAPIOK is the last successful round trip, or the start time.
	lastAPIOK  time.Time
	everAPIOK  bool
	apiFailing bool
	lastStatus int // status of the last failed round trip, 0 for a transport error
	streaming  bool
	unauth     int // consecutive 401s
	forbidden  int // consecutive 403s
	interval   time.Duration

	sinkFailingSince time.Time
	sinkStatus       int
}

// DefaultHealth is the process-wide tracker served on /readyz and /status.
var DefaultHealth = NewHealth(time.Now)

// NewHealth returns a tracker that starts ready. now is the clock.
func NewHealth(now func() time.Time) *Health {
	if now == nil {
		now = time.Now
	}
	return &Health{now: now, lastAPIOK: now()}
}

// SetPollInterval tells the tracker the current poll cadence, so a slow
// schedule does not read as an unreachable API.
func (h *Health) SetPollInterval(d time.Duration) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.interval = d
	h.mu.Unlock()
}

// APIOK records a successful round trip to the YipYap API.
func (h *Health) APIOK() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastAPIOK = h.now()
	h.everAPIOK = true
	h.apiFailing = false
	h.lastStatus = 0
	h.unauth, h.forbidden = 0, 0
}

// StreamConnected records an open stream. The API counts as reachable
// until StreamClosed, however long the stream stays quiet.
func (h *Health) StreamConnected() {
	if h == nil {
		return
	}
	h.APIOK()
	h.mu.Lock()
	h.streaming = true
	h.mu.Unlock()
}

// StreamClosed records the end of an open stream. The API was reachable up
// to now.
func (h *Health) StreamClosed() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.streaming {
		h.streaming = false
		h.lastAPIOK = h.now()
	}
}

// APIFailed records a failed round trip. err may wrap an *APIStatusError;
// anything else counts as a transport failure.
func (h *Health) APIFailed(err error) {
	if h == nil || err == nil {
		return
	}
	status := 0
	var se *APIStatusError
	if errors.As(err, &se) {
		status = se.StatusCode
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.apiFailing = true
	h.lastStatus = status
	switch status {
	case http.StatusUnauthorized:
		h.unauth++
		h.forbidden = 0
	case http.StatusForbidden:
		h.forbidden++
		h.unauth = 0
	case 0:
		// A transport error says nothing about the key. Keep the counts so
		// a network blip between two 401s does not hide a bad key.
	default:
		h.unauth, h.forbidden = 0, 0
	}
}

// SinkOK records that the sink accepted or permanently rejected an event.
// Either way the sink is answering.
func (h *Health) SinkOK() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sinkFailingSince = time.Time{}
	h.sinkStatus = 0
}

// SinkFailed records a failed, retryable delivery attempt.
func (h *Health) SinkFailed(err error) {
	if h == nil {
		return
	}
	status := 0
	var se *SinkError
	if errors.As(err, &se) {
		status = se.StatusCode
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sinkFailingSince.IsZero() {
		h.sinkFailingSince = h.now()
	}
	h.sinkStatus = status
}

// Status returns the current verdict. Auth problems come first because they
// never fix themselves, then the sink, then general API reachability.
func (h *Health) Status() adapterstatus.Status {
	if h == nil {
		return adapterstatus.Status{Ready: true}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	st := adapterstatus.Status{Ready: true}
	if h.everAPIOK {
		t := h.lastAPIOK.UTC()
		st.LastSuccess = &t
	}
	notReady := func(reason, msg string) adapterstatus.Status {
		st.Ready, st.Reason, st.Message = false, reason, msg
		return st
	}

	switch {
	case h.unauth >= AuthFailureThreshold:
		return notReady(adapterstatus.ReasonAPIKeyRejected, "API key rejected (401)")
	case h.forbidden >= AuthFailureThreshold:
		return notReady(adapterstatus.ReasonAPIForbidden, "API key forbidden (403)")
	}
	if !h.sinkFailingSince.IsZero() && now.Sub(h.sinkFailingSince) >= SinkUnavailableAfter {
		if h.sinkStatus != 0 {
			return notReady(adapterstatus.ReasonSinkUnavailable,
				fmt.Sprintf("sink is not accepting events (%d)", h.sinkStatus))
		}
		return notReady(adapterstatus.ReasonSinkUnavailable, "sink is unreachable")
	}
	window := APIUnreachableAfter
	if w := 3 * h.interval; w > window {
		window = w
	}
	if h.apiFailing && !h.streaming && now.Sub(h.lastAPIOK) >= window {
		if h.lastStatus != 0 {
			return notReady(adapterstatus.ReasonAPIUnreachable,
				fmt.Sprintf("YipYap API returned an error (%d)", h.lastStatus))
		}
		return notReady(adapterstatus.ReasonAPIUnreachable, "cannot reach the YipYap API")
	}
	return st
}

// ReadyzHandler answers 200 when the adapter is ready and 503 with the
// plain-language reason when it is not.
func (h *Health) ReadyzHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		st := h.Status()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !st.Ready {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "%s: %s", st.Reason, st.Message)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
}

// StatusHandler serves Status as JSON. It always answers 200 so a reader can
// tell "not ready, here is why" apart from "status endpoint broken".
func (h *Health) StatusHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(h.Status())
	})
}

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

package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"

	ce "github.com/cloudevents/sdk-go/v2"
)

// The stub answers the YipYap API calls the receive adapter makes, so the
// smoke test can run an adapter to Ready without a YipYap backend. It
// serves one CloudEvent, then an empty batch for every later poll. The
// smoke script reads the log lines below to check the adapter called the
// API with the right key, delivered the event to the sink and confirmed it.
//
// Log messages the smoke script matches. Keep test/smoke/smoke.sh in sync.
const (
	logPoll    = "stub poll"
	logAck     = "stub ack"
	logSink    = "stub sink received"
	logBadAuth = "stub rejected credentials"
)

// SmokeEventID is the ID of the one event the stub serves.
const SmokeEventID = "smoke-1"

// SmokeEventType is the type of that event. It matches the type filter in
// example.yaml.
const SmokeEventType = "run.yipyap.monitor.state"

// firstCursor is the next_cursor returned with the smoke event. A poll that
// sends since=firstCursor confirms the event (see docs/delivery.md).
const firstCursor = "c1"

const sourceIDHeader = "X-YipYap-Source-ID"

// api serves the YipYap API surface on the TLS listener.
type api struct {
	key string
	log *slog.Logger
}

func newAPI(key string, log *slog.Logger) http.Handler {
	a := &api{key: key, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/knative/source-config", a.authed(a.sourceConfig))
	mux.HandleFunc("GET /api/v1/cloudevents/poll", a.authed(a.poll))
	mux.HandleFunc("POST /api/v1/cloudevents/ack", a.authed(a.ack))
	return mux
}

// authed rejects requests whose bearer token is not the configured key.
// The token itself is never logged.
func (a *api) authed(next http.HandlerFunc) http.HandlerFunc {
	want := []byte("Bearer " + a.key)
	return func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			a.log.Warn(logBadAuth, "path", r.URL.Path)
			http.Error(w, "invalid API key", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (a *api) sourceConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"recommended_mode":      "poll",
		"poll_interval_seconds": 1,
		"refresh_after_seconds": 300,
	})
}

func (a *api) poll(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	a.log.Info(logPoll, "since", since, "source_id", r.Header.Get(sourceIDHeader))
	if since == firstCursor {
		writeJSON(w, map[string]any{"events": []any{}, "next_cursor": firstCursor})
		return
	}
	ev := ce.NewEvent()
	ev.SetID(SmokeEventID)
	ev.SetType(SmokeEventType)
	ev.SetSource("https://yipyap.run/smoke")
	if err := ev.SetData(ce.ApplicationJSON, map[string]string{"state": "up"}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"events": []any{ev}, "next_cursor": firstCursor})
}

func (a *api) ack(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cursor string   `json:"cursor"`
		CEIDs  []string `json:"ce_ids"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad ack body", http.StatusBadRequest)
		return
	}
	a.log.Info(logAck, "cursor", body.Cursor, "ce_ids", body.CEIDs, "source_id", r.Header.Get(sourceIDHeader))
	w.WriteHeader(http.StatusNoContent)
}

// sink is a CloudEvents receiver on the plain HTTP listener, standing in
// for the Broker in example.yaml. It also serves the pod's probes.
type sink struct {
	log  *slog.Logger
	mu   sync.Mutex
	seen []string
}

func newSink(log *slog.Logger) *sink { return &sink{log: log} }

func (s *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && r.URL.Path == "/":
		ev, err := ce.NewEventFromHTTPRequest(r)
		if err != nil {
			http.Error(w, "not a CloudEvent", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.seen = append(s.seen, ev.ID())
		s.mu.Unlock()
		s.log.Info(logSink, "ce_id", ev.ID(), "type", ev.Type())
		w.WriteHeader(http.StatusAccepted)
	default:
		http.NotFound(w, r)
	}
}

func (s *sink) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

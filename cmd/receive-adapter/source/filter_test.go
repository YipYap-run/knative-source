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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
	"github.com/oklog/ulid/v2"
)

// filterEvent builds an event of type typ with data as its JSON payload and
// the given extension attributes.
func filterEvent(t *testing.T, typ string, data any, ext map[string]string) ce.Event {
	t.Helper()
	ev := ce.NewEvent()
	ev.SetID(ulid.Make().String())
	ev.SetType(typ)
	ev.SetSource("filter-test")
	if data != nil {
		if err := ev.SetData(ce.ApplicationJSON, data); err != nil {
			t.Fatalf("set data: %v", err)
		}
	}
	for k, v := range ext {
		ev.SetExtension(k, v)
	}
	return ev
}

func TestEventFilter_Match(t *testing.T) {
	const (
		fired    = "run.yipyap.alert.fired.v1"
		resolved = "run.yipyap.alert.resolved.v1"
		down     = "run.yipyap.monitor.down.v1"
		maint    = "run.yipyap.maintenance.started.v1"
	)
	critical := NewEventFilter([]string{"critical"}, nil)
	mon1 := NewEventFilter(nil, []string{"mon-1"})
	both := NewEventFilter([]string{"critical"}, []string{"mon-1"})

	cases := []struct {
		name   string
		filter *EventFilter
		ev     ce.Event
		want   bool
	}{
		{"nil filter keeps all", nil, filterEvent(t, fired, map[string]string{"severity": "info"}, nil), true},
		{"critical keeps critical alert", critical, filterEvent(t, fired, map[string]string{"severity": "critical"}, nil), true},
		{"critical drops info alert", critical, filterEvent(t, fired, map[string]string{"severity": "info"}, nil), false},
		{"critical drops warning alert", critical, filterEvent(t, fired, map[string]string{"severity": "warning"}, nil), false},
		{"severity compared case-insensitively", critical, filterEvent(t, fired, map[string]string{"severity": "CRITICAL"}, nil), true},
		{"extension wins over data", critical, filterEvent(t, fired, map[string]string{"severity": "critical"}, map[string]string{SeverityExtension: "info"}), false},
		{"extension alone", critical, filterEvent(t, resolved, map[string]string{"alert_id": "a"}, map[string]string{SeverityExtension: "critical"}), true},
		{"alert without severity passes", critical, filterEvent(t, resolved, map[string]string{"alert_id": "a"}, nil), true},
		{"critical keeps monitor events", critical, filterEvent(t, down, map[string]string{"monitor_id": "mon-9", "severity": "info"}, nil), true},
		{"critical keeps maintenance events", critical, filterEvent(t, maint, map[string]any{"window_id": "w"}, nil), true},
		{"non-JSON data passes", critical, func() ce.Event {
			ev := ce.NewEvent()
			ev.SetID("x")
			ev.SetType(fired)
			ev.SetSource("s")
			_ = ev.SetData("text/plain", []byte("severity=info"))
			return ev
		}(), true},
		{"monitor filter keeps listed monitor", mon1, filterEvent(t, down, map[string]string{"monitor_id": "mon-1"}, nil), true},
		{"monitor filter drops other monitor", mon1, filterEvent(t, down, map[string]string{"monitor_id": "mon-2"}, nil), false},
		{"monitor filter applies to alerts", mon1, filterEvent(t, fired, map[string]string{"monitor_id": "mon-2", "severity": "critical"}, nil), false},
		{"monitor extension wins over data", mon1, filterEvent(t, down, map[string]string{"monitor_id": "mon-1"}, map[string]string{MonitorIDExtension: "mon-2"}), false},
		{"event without monitor passes", mon1, filterEvent(t, maint, map[string]any{"window_id": "w", "monitors": []string{"mon-2"}}, nil), true},
		{"both: matching alert", both, filterEvent(t, fired, map[string]string{"monitor_id": "mon-1", "severity": "critical"}, nil), true},
		{"both: right monitor wrong severity", both, filterEvent(t, fired, map[string]string{"monitor_id": "mon-1", "severity": "warning"}, nil), false},
		{"both: right severity wrong monitor", both, filterEvent(t, fired, map[string]string{"monitor_id": "mon-3", "severity": "critical"}, nil), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.filter.Match(tc.ev); got != tc.want {
				t.Errorf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadConfig_FilterEnv(t *testing.T) {
	env := map[string]string{
		"K_SINK":                    "http://sink",
		"YIPYAP_API_KEY":            "k",
		"YIPYAP_FILTER_SEVERITIES":  "critical, Warning,critical",
		"YIPYAP_FILTER_MONITOR_IDS": "mon-1,,mon-2,mon-1",
	}
	cfg, err := LoadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if want := []string{"critical", "warning"}; !reflect.DeepEqual(cfg.Severities, want) {
		t.Errorf("Severities = %v, want %v", cfg.Severities, want)
	}
	if want := []string{"mon-1", "mon-2"}; !reflect.DeepEqual(cfg.MonitorIDs, want) {
		t.Errorf("MonitorIDs = %v, want %v", cfg.MonitorIDs, want)
	}

	env["YIPYAP_FILTER_SEVERITIES"] = "critical,fatal"
	if _, err := LoadConfig(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "fatal") {
		t.Errorf("unknown severity: err = %v, want an error naming it", err)
	}
}

// Both transports ask the server to apply the filters with one severity
// and one monitor_id parameter per value.
func TestFilterQueryParams_PollAndStream(t *testing.T) {
	cfg := &Config{
		APIKey:     "k",
		Severities: []string{"critical", "warning"},
		MonitorIDs: []string{"mon-1", "mon-2"},
	}
	var (
		mu  sync.Mutex
		got = map[string][2][]string{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		got[r.URL.Path] = [2][]string{q["severity"], q["monitor_id"]}
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/poll") {
			_, _ = w.Write([]byte(`{"events":[],"next_cursor":""}`))
		}
	}))
	defer srv.Close()
	cfg.BaseURL = srv.URL

	if _, _, err := NewPollClient(cfg).fetch(t.Context(), ""); err != nil {
		t.Fatalf("poll fetch: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	sc := NewStreamClient(cfg)
	sc.confirm = nil
	_ = sc.stream(ctx, func(context.Context, ce.Event) error { return nil })

	want := [2][]string{{"critical", "warning"}, {"mon-1", "mon-2"}}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range []string{"/api/v1/cloudevents/poll", "/api/v1/cloudevents/stream"} {
		if !reflect.DeepEqual(got[p], want) {
			t.Errorf("%s: severity, monitor_id = %v, want %v", p, got[p], want)
		}
	}
}

// Without filters no severity or monitor_id parameter is sent.
func TestFilterQueryParams_NoneConfigured(t *testing.T) {
	var q map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	defer srv.Close()
	if _, _, err := NewPollClient(&Config{BaseURL: srv.URL, APIKey: "k"}).fetch(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := q["severity"]; ok {
		t.Errorf("unexpected severity param: %v", q)
	}
	if _, ok := q["monitor_id"]; ok {
		t.Errorf("unexpected monitor_id param: %v", q)
	}
}

// A filtered stream event never reaches the sink but is still confirmed, so
// the server does not redeliver it. The matching event is delivered.
func TestStreamClient_FilteredEventConfirmed(t *testing.T) {
	drop := filterEvent(t, "run.yipyap.alert.fired.v1", map[string]string{"severity": "info"}, map[string]string{CursorExtension: "c1"})
	keep := filterEvent(t, "run.yipyap.alert.fired.v1", map[string]string{"severity": "critical"}, map[string]string{CursorExtension: "c2"})
	acks := &ackRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/cloudevents/stream", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = writeNDJSON(w, drop)
		_ = writeNDJSON(w, keep)
	})
	mux.HandleFunc(AckPath, acks.handle)
	api := httptest.NewServer(mux)
	defer api.Close()
	sink := &flakySink{script: []int{202}}
	sinkSrv := httptest.NewServer(sink)
	defer sinkSrv.Close()

	c := NewStreamClient(&Config{BaseURL: api.URL, APIKey: "key", Severities: []string{"critical"}})
	stats := &Stats{}
	c.filter.stats = stats
	if err := c.stream(t.Context(), fastDeliverer(sinkSrv.URL, &Stats{}).Deliver); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got := sink.Attempts(); len(got) != 1 || got[0] != keep.ID() {
		t.Errorf("sink attempts = %v, want only %s", got, keep.ID())
	}
	bodies, _ := acks.snapshot()
	if len(bodies) != 2 || bodies[0].Cursor != "c1" || bodies[1].Cursor != "c2" {
		t.Errorf("acks = %+v, want c1 then c2", bodies)
	}
	if c.lastCursor != "c2" {
		t.Errorf("lastCursor = %q, want c2", c.lastCursor)
	}
	if stats.Filtered.Load() != 1 {
		t.Errorf("filtered = %d, want 1", stats.Filtered.Load())
	}
}

// A poll batch with filtered events is confirmed as a whole: the filtered
// events skip the sink and the next poll sends the batch cursor as since=.
func TestPollClient_FilteredEventsConfirmedWithBatch(t *testing.T) {
	info := filterEvent(t, "run.yipyap.alert.fired.v1", map[string]string{"severity": "info", "monitor_id": "mon-1"}, nil)
	warn := filterEvent(t, "run.yipyap.alert.fired.v1", map[string]string{"severity": "warning", "monitor_id": "mon-1"}, nil)
	crit := filterEvent(t, "run.yipyap.alert.fired.v1", map[string]string{"severity": "critical", "monitor_id": "mon-1"}, nil)
	monUp := filterEvent(t, "run.yipyap.monitor.up.v1", map[string]string{"monitor_id": "mon-1"}, nil)
	ps := &pollServer{batches: []pollBatch{{events: []ce.Event{info, warn, crit, monUp}, next: "c1"}}}
	api := httptest.NewServer(ps)
	defer api.Close()
	sink := &flakySink{script: []int{202}}
	sinkSrv := httptest.NewServer(sink)
	defer sinkSrv.Close()

	c := newTestPollClient(t, api.URL, "key")
	c.filter = NewEventFilter([]string{"critical"}, nil)
	stats := &Stats{}
	c.filter.stats = stats
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, fastDeliverer(sinkSrv.URL, &Stats{}).Deliver) }()
	waitFor(t, func() bool {
		sinces, _ := ps.snapshot()
		return len(sinces) >= 2 && sinces[len(sinces)-1] == "c1"
	})
	cancel()
	<-done

	if got, want := sink.Attempts(), []string{crit.ID(), monUp.ID()}; !reflect.DeepEqual(got, want) {
		t.Errorf("sink attempts = %v, want %v (critical alert and monitor event)", got, want)
	}
	if stats.Filtered.Load() != 2 {
		t.Errorf("filtered = %d, want 2", stats.Filtered.Load())
	}
}

func TestStats_WriteMetricsFiltered(t *testing.T) {
	s := &Stats{}
	s.Filtered.Add(3)
	var b strings.Builder
	if err := s.WriteMetrics(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "yipyap_source_events_filtered_total 3\n") {
		t.Errorf("metrics output missing filtered counter:\n%s", b.String())
	}
}

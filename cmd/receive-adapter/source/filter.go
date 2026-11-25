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
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	ce "github.com/cloudevents/sdk-go/v2"
)

// Filter attributes and query parameters. See docs/delivery.md, Filters.
const (
	// SeverityExtension is the CloudEvent extension attribute that carries
	// an alert's severity. When it is absent the adapter reads the
	// "severity" field of the event's JSON data.
	SeverityExtension = "yipyapseverity"
	// MonitorIDExtension is the CloudEvent extension attribute that carries
	// the monitor an event belongs to. When it is absent the adapter reads
	// the "monitor_id" field of the event's JSON data.
	MonitorIDExtension = "yipyapmonitorid"

	// severityParam and monitorIDParam are the query parameters that ask the
	// server to apply the same filters. Each value is sent as its own
	// parameter.
	severityParam  = "severity"
	monitorIDParam = "monitor_id"

	// alertTypePrefix marks the event types the severity filter applies to.
	alertTypePrefix = "run.yipyap.alert."
)

// validSeverities mirrors the YipYapSource filter.severities enum.
var validSeverities = map[string]struct{}{
	"info":     {},
	"warning":  {},
	"critical": {},
}

// EventFilter applies filter.severities and filter.monitorIDs in the
// adapter, so they hold even when the server ignores the query parameters.
// A nil *EventFilter matches every event.
type EventFilter struct {
	severities []string
	monitorIDs []string
	sevSet     map[string]struct{}
	monSet     map[string]struct{}
	// stats counts dropped events. Defaults to DefaultStats.
	stats *Stats
}

// NewEventFilter builds a filter from the configured lists. It returns nil
// when both are empty.
func NewEventFilter(severities, monitorIDs []string) *EventFilter {
	if len(severities) == 0 && len(monitorIDs) == 0 {
		return nil
	}
	f := &EventFilter{
		severities: severities,
		monitorIDs: monitorIDs,
		sevSet:     make(map[string]struct{}, len(severities)),
		monSet:     make(map[string]struct{}, len(monitorIDs)),
		stats:      DefaultStats,
	}
	for _, s := range severities {
		f.sevSet[s] = struct{}{}
	}
	for _, m := range monitorIDs {
		f.monSet[m] = struct{}{}
	}
	return f
}

// addQuery adds one severity and one monitor_id parameter per configured
// value, so a server that supports them sends only matching events.
func (f *EventFilter) addQuery(q url.Values) {
	if f == nil {
		return
	}
	for _, s := range f.severities {
		q.Add(severityParam, s)
	}
	for _, m := range f.monitorIDs {
		q.Add(monitorIDParam, m)
	}
}

// Match reports whether ev passes the filter.
//
//   - Severities apply only to alert events (type run.yipyap.alert.*). An
//     alert event whose severity is not listed is dropped. An alert event
//     without a severity is delivered.
//   - Monitor IDs apply to every event that carries a monitor ID. An event
//     whose monitor is not listed is dropped. An event without one (for
//     example a maintenance event) is delivered.
func (f *EventFilter) Match(ev ce.Event) bool {
	if f == nil {
		return true
	}
	var data map[string]json.RawMessage
	dataLoaded := false
	field := func(name string) (string, bool) {
		if !dataLoaded {
			dataLoaded = true
			data = jsonDataObject(ev)
		}
		raw, ok := data[name]
		if !ok {
			return "", false
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || s == "" {
			return "", false
		}
		return s, true
	}
	if len(f.sevSet) > 0 && strings.HasPrefix(ev.Type(), alertTypePrefix) {
		sev, ok := extensionString(ev, SeverityExtension)
		if !ok {
			sev, ok = field("severity")
		}
		if ok {
			if _, keep := f.sevSet[strings.ToLower(strings.TrimSpace(sev))]; !keep {
				return false
			}
		}
	}
	if len(f.monSet) > 0 {
		id, ok := extensionString(ev, MonitorIDExtension)
		if !ok {
			id, ok = field("monitor_id")
		}
		if ok {
			if _, keep := f.monSet[id]; !keep {
				return false
			}
		}
	}
	return true
}

// skip reports whether ev should be dropped, and logs and counts it when so.
// A skipped event still counts as handled: the caller confirms it to the
// server so it is not redelivered.
func (f *EventFilter) skip(ev ce.Event) bool {
	if f.Match(ev) {
		return false
	}
	if f.stats != nil {
		f.stats.Filtered.Add(1)
	}
	slog.Debug("event dropped by filter", "ce_id", ev.ID(), "ce_type", ev.Type())
	return true
}

// extensionString returns a non-empty string extension attribute.
func extensionString(ev ce.Event, name string) (string, bool) {
	v, ok := ev.Extensions()[name]
	if !ok || v == nil {
		return "", false
	}
	s := fmt.Sprint(v)
	return s, s != ""
}

// jsonDataObject decodes ev's data as a JSON object. It returns nil when the
// data is absent, not JSON, or not an object.
func jsonDataObject(ev ce.Event) map[string]json.RawMessage {
	ct := ev.DataContentType()
	if ct != "" && !strings.Contains(strings.ToLower(ct), "json") {
		return nil
	}
	b := ev.Data()
	if len(b) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

// parseSeverities parses YIPYAP_FILTER_SEVERITIES. Values are lowercased,
// duplicates dropped, and an unknown value is an error so a typo does not
// silently deliver every severity.
func parseSeverities(raw string) ([]string, error) {
	var out []string
	seen := map[string]struct{}{}
	for _, s := range parseEventFilters(raw) {
		s = strings.ToLower(s)
		if _, ok := validSeverities[s]; !ok {
			return nil, fmt.Errorf("YIPYAP_FILTER_SEVERITIES: unknown severity %q; want info, warning or critical", s)
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, nil
}

// parseMonitorIDs parses YIPYAP_FILTER_MONITOR_IDS, dropping blanks and
// duplicates.
func parseMonitorIDs(raw string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, m := range parseEventFilters(raw) {
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}

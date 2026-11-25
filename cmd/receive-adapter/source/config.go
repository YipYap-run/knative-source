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
	"crypto/x509"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/YipYap-run/knative-source/pkg/cacerts"
)

// Config holds runtime configuration for the yipyap-knative-source binary.
// Values are sourced from environment variables at startup; see LoadConfig.
type Config struct {
	Sink         string            // K_SINK
	CEOverrides  map[string]string // parsed from K_CE_OVERRIDES
	APIKey       string            // YIPYAP_API_KEY
	BaseURL      string            // YIPYAP_BASE_URL
	EventFilters []string          // YIPYAP_EVENT_FILTER, parsed: comma-separated path.Match globs over the CloudEvent type, OR-matched server-side
	Severities   []string          // YIPYAP_FILTER_SEVERITIES, parsed: alert severities to keep (info, warning, critical)
	MonitorIDs   []string          // YIPYAP_FILTER_MONITOR_IDS, parsed: monitor IDs to keep
	Mode         string            // YIPYAP_MODE: "", "auto", "poll", or "stream"
	PollInterval time.Duration     // YIPYAP_POLL_INTERVAL
	CursorPath   string            // YIPYAP_CURSOR_PATH
	HealthAddr   string            // YIPYAP_HEALTH_ADDR

	// SourceID is the owning YipYapSource UID (YIPYAP_SOURCE_ID, set by the
	// controller). It is sent as SourceIDHeader on every server request so
	// the server tracks delivery per source. Empty when the adapter runs
	// outside the controller; the header is then omitted.
	SourceID string

	// SinkRootCAs is the system root pool plus the PEM bundle in
	// K_CA_CERTS (the sink address's CACerts). Nil when K_CA_CERTS is unset,
	// so the sink client uses the system roots. LoadConfig fails on a bundle
	// it cannot parse, so the adapter never starts with a CA it would
	// silently ignore.
	SinkRootCAs *x509.CertPool

	// SinkAudience is the sink's OIDC audience (K_AUDIENCE). When set, every
	// sink request carries "Authorization: Bearer <token>" with the token
	// read from SinkTokenPath.
	SinkAudience string

	// SinkTokenPath (YIPYAP_SINK_TOKEN_PATH) is the projected ServiceAccount
	// token for SinkAudience. The kubelet rotates the file, so it is read
	// on every send. Defaults to DefaultSinkTokenPath when an audience is set.
	SinkTokenPath string

	// ShutdownGrace (YIPYAP_SHUTDOWN_GRACE) is how long the adapter keeps
	// delivering and confirming the in-flight event after SIGTERM. The
	// controller sizes terminationGracePeriodSeconds to cover it.
	ShutdownGrace time.Duration
}

// DefaultPollInterval is the poll cadence used when YIPYAP_POLL_INTERVAL is
// unset, and the fallback whenever a non-positive interval reaches a ticker.
const DefaultPollInterval = 5 * time.Second

// DefaultShutdownGrace is the YIPYAP_SHUTDOWN_GRACE default. Keep it below
// the adapter pod's terminationGracePeriodSeconds minus the confirmation
// timeout (see resources.AdapterTerminationGracePeriodSeconds).
const DefaultShutdownGrace = 25 * time.Second

// MaxShutdownGrace is the largest YIPYAP_SHUTDOWN_GRACE accepted. A longer
// drain plus the confirmation budget would outlast the pod's
// terminationGracePeriodSeconds, and the kubelet would kill the adapter
// before it confirms the in-flight event.
const MaxShutdownGrace = DefaultShutdownGrace

// DefaultSinkTokenPath is where the controller mounts the projected token
// for an OIDC sink (resources.OIDCTokenMountPath plus OIDCTokenFile).
const DefaultSinkTokenPath = "/var/run/secrets/yipyap/oidc/token"

// ceOverridesEnvelope mirrors the K_CE_OVERRIDES JSON shape injected by Knative:
//
//	{"extensions":{"key":"value", ...}}
type ceOverridesEnvelope struct {
	Extensions map[string]string `json:"extensions"`
}

// LoadConfig builds a Config from the supplied getenv callback (typically os.Getenv).
// Injecting getenv keeps the parser pure so tests do not have to mutate process env.
func LoadConfig(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		Sink:          getenv("K_SINK"),
		APIKey:        getenv("YIPYAP_API_KEY"),
		BaseURL:       defaultStr(getenv("YIPYAP_BASE_URL"), "https://console.yipyap.run"),
		EventFilters:  parseEventFilters(getenv("YIPYAP_EVENT_FILTER")),
		MonitorIDs:    parseMonitorIDs(getenv("YIPYAP_FILTER_MONITOR_IDS")),
		Mode:          strings.ToLower(strings.TrimSpace(getenv("YIPYAP_MODE"))),
		CursorPath:    defaultStr(getenv("YIPYAP_CURSOR_PATH"), "/var/run/yipyap/cursor"),
		HealthAddr:    defaultStr(getenv("YIPYAP_HEALTH_ADDR"), ":8080"),
		CEOverrides:   map[string]string{},
		PollInterval:  DefaultPollInterval,
		SourceID:      strings.TrimSpace(getenv("YIPYAP_SOURCE_ID")),
		ShutdownGrace: DefaultShutdownGrace,
	}

	if cfg.Sink == "" {
		return nil, fmt.Errorf("K_SINK is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("YIPYAP_API_KEY is required")
	}

	sev, err := parseSeverities(getenv("YIPYAP_FILTER_SEVERITIES"))
	if err != nil {
		return nil, err
	}
	cfg.Severities = sev

	if raw := getenv("K_CE_OVERRIDES"); raw != "" {
		var env ceOverridesEnvelope
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			return nil, fmt.Errorf("K_CE_OVERRIDES: invalid JSON: %w", err)
		}
		if env.Extensions != nil {
			cfg.CEOverrides = env.Extensions
		}
	}

	if raw := getenv("K_CA_CERTS"); strings.TrimSpace(raw) != "" {
		pool, err := cacerts.Pool(raw)
		if err != nil {
			return nil, fmt.Errorf("K_CA_CERTS: %w", err)
		}
		cfg.SinkRootCAs = pool
	}

	cfg.SinkAudience = strings.TrimSpace(getenv("K_AUDIENCE"))
	if cfg.SinkAudience != "" {
		cfg.SinkTokenPath = defaultStr(strings.TrimSpace(getenv("YIPYAP_SINK_TOKEN_PATH")), DefaultSinkTokenPath)
	}

	if raw := getenv("YIPYAP_POLL_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("YIPYAP_POLL_INTERVAL: invalid duration %q: %w", raw, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("YIPYAP_POLL_INTERVAL: must be a positive duration, got %q", raw)
		}
		cfg.PollInterval = d
	}

	if raw := getenv("YIPYAP_SHUTDOWN_GRACE"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("YIPYAP_SHUTDOWN_GRACE: invalid duration %q: %w", raw, err)
		}
		if d < 0 {
			return nil, fmt.Errorf("YIPYAP_SHUTDOWN_GRACE: must not be negative, got %q", raw)
		}
		if d > MaxShutdownGrace {
			return nil, fmt.Errorf("YIPYAP_SHUTDOWN_GRACE: must be at most %s to fit the pod termination grace, got %q", MaxShutdownGrace, raw)
		}
		cfg.ShutdownGrace = d
	}

	// "auto" is the CRD default (spec.mode), rendered verbatim into
	// YIPYAP_MODE by the controller. It means "let ResolveMode decide".
	switch cfg.Mode {
	case "", "auto", "poll", "stream":
		// ok
	default:
		return nil, fmt.Errorf("YIPYAP_MODE: must be one of \"\", \"auto\", \"poll\", \"stream\"; got %q", cfg.Mode)
	}

	return cfg, nil
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// parseEventFilters splits the comma-separated YIPYAP_EVENT_FILTER value into
// individual type globs, trimming surrounding whitespace and dropping empty
// entries. An empty or all-blank value yields a nil slice (no filtering).
func parseEventFilters(raw string) []string {
	var filters []string
	for _, part := range strings.Split(raw, ",") {
		if f := strings.TrimSpace(part); f != "" {
			filters = append(filters, f)
		}
	}
	return filters
}

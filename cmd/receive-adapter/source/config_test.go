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
	"strings"
	"testing"
	"time"
)

// fakeEnv returns a getenv function backed by a map, matching os.Getenv semantics.
func fakeEnv(m map[string]string) func(string) string {
	return func(k string) string {
		return m[k]
	}
}

func TestLoadConfig_Minimal(t *testing.T) {
	cfg, err := LoadConfig(fakeEnv(map[string]string{
		"K_SINK":         "http://sink.example/",
		"YIPYAP_API_KEY": "secret",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Sink != "http://sink.example/" {
		t.Errorf("Sink = %q, want %q", cfg.Sink, "http://sink.example/")
	}
	if cfg.APIKey != "secret" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "secret")
	}
	if cfg.BaseURL != "https://console.yipyap.run" {
		t.Errorf("BaseURL default = %q, want https://console.yipyap.run", cfg.BaseURL)
	}
	if len(cfg.EventFilters) != 0 {
		t.Errorf("EventFilters default = %v, want empty", cfg.EventFilters)
	}
	if cfg.Mode != "" {
		t.Errorf("Mode default = %q, want empty", cfg.Mode)
	}
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval default = %v, want 5s", cfg.PollInterval)
	}
	if cfg.CursorPath != "/var/run/yipyap/cursor" {
		t.Errorf("CursorPath default = %q, want /var/run/yipyap/cursor", cfg.CursorPath)
	}
	if cfg.HealthAddr != ":8080" {
		t.Errorf("HealthAddr default = %q, want :8080", cfg.HealthAddr)
	}
	if cfg.CEOverrides == nil {
		t.Errorf("CEOverrides should be non-nil empty map, got nil")
	}
	if len(cfg.CEOverrides) != 0 {
		t.Errorf("CEOverrides should be empty, got %v", cfg.CEOverrides)
	}
}

func TestLoadConfig_EventFilters(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "unset", raw: "", want: nil},
		{name: "single", raw: "run.yipyap.alert.*", want: []string{"run.yipyap.alert.*"}},
		{name: "multiple", raw: "a.*,b.*", want: []string{"a.*", "b.*"}},
		{name: "whitespace trimmed", raw: " a.* , b.* ", want: []string{"a.*", "b.*"}},
		{name: "empties dropped", raw: "a.*,,b.*,", want: []string{"a.*", "b.*"}},
		{name: "only commas", raw: ",,", want: nil},
		{name: "only whitespace", raw: "  ", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{
				"K_SINK":         "http://sink.example/",
				"YIPYAP_API_KEY": "secret",
			}
			if tc.raw != "" {
				env["YIPYAP_EVENT_FILTER"] = tc.raw
			}
			cfg, err := LoadConfig(fakeEnv(env))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cfg.EventFilters) != len(tc.want) {
				t.Fatalf("EventFilters = %v, want %v", cfg.EventFilters, tc.want)
			}
			for i := range tc.want {
				if cfg.EventFilters[i] != tc.want[i] {
					t.Errorf("EventFilters[%d] = %q, want %q", i, cfg.EventFilters[i], tc.want[i])
				}
			}
		})
	}
}

func TestLoadConfig_MissingSink(t *testing.T) {
	_, err := LoadConfig(fakeEnv(map[string]string{
		"YIPYAP_API_KEY": "secret",
	}))
	if err == nil {
		t.Fatal("expected error for missing K_SINK, got nil")
	}
	if !strings.Contains(err.Error(), "K_SINK") {
		t.Errorf("error should mention K_SINK, got %v", err)
	}
}

func TestLoadConfig_MissingAPIKey(t *testing.T) {
	_, err := LoadConfig(fakeEnv(map[string]string{
		"K_SINK": "http://sink.example/",
	}))
	if err == nil {
		t.Fatal("expected error for missing YIPYAP_API_KEY, got nil")
	}
	if !strings.Contains(err.Error(), "YIPYAP_API_KEY") {
		t.Errorf("error should mention YIPYAP_API_KEY, got %v", err)
	}
}

func TestLoadConfig_InvalidCEOverrides(t *testing.T) {
	_, err := LoadConfig(fakeEnv(map[string]string{
		"K_SINK":         "http://sink.example/",
		"YIPYAP_API_KEY": "secret",
		"K_CE_OVERRIDES": "not-json{",
	}))
	if err == nil {
		t.Fatal("expected error for invalid K_CE_OVERRIDES JSON, got nil")
	}
	if !strings.Contains(err.Error(), "K_CE_OVERRIDES") {
		t.Errorf("error should mention K_CE_OVERRIDES, got %v", err)
	}
}

func TestLoadConfig_CEOverrides(t *testing.T) {
	cfg, err := LoadConfig(fakeEnv(map[string]string{
		"K_SINK":         "http://sink.example/",
		"YIPYAP_API_KEY": "secret",
		"K_CE_OVERRIDES": `{"extensions":{"environment":"prod","region":"us-east-1"}}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := cfg.CEOverrides["environment"], "prod"; got != want {
		t.Errorf("CEOverrides[environment] = %q, want %q", got, want)
	}
	if got, want := cfg.CEOverrides["region"], "us-east-1"; got != want {
		t.Errorf("CEOverrides[region] = %q, want %q", got, want)
	}
	if len(cfg.CEOverrides) != 2 {
		t.Errorf("CEOverrides should have 2 entries, got %d: %v", len(cfg.CEOverrides), cfg.CEOverrides)
	}
}

func TestLoadConfig_PollIntervalParsed(t *testing.T) {
	cfg, err := LoadConfig(fakeEnv(map[string]string{
		"K_SINK":               "http://sink.example/",
		"YIPYAP_API_KEY":       "secret",
		"YIPYAP_POLL_INTERVAL": "30s",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want 30s", cfg.PollInterval)
	}
}

func TestLoadConfig_PollIntervalInvalid(t *testing.T) {
	_, err := LoadConfig(fakeEnv(map[string]string{
		"K_SINK":               "http://sink.example/",
		"YIPYAP_API_KEY":       "secret",
		"YIPYAP_POLL_INTERVAL": "not-a-duration",
	}))
	if err == nil {
		t.Fatal("expected error for invalid YIPYAP_POLL_INTERVAL, got nil")
	}
	if !strings.Contains(err.Error(), "YIPYAP_POLL_INTERVAL") {
		t.Errorf("error should mention YIPYAP_POLL_INTERVAL, got %v", err)
	}
}

func TestLoadConfig_ModeValidation(t *testing.T) {
	for _, mode := range []string{"", "auto", "poll", "stream"} {
		cfg, err := LoadConfig(fakeEnv(map[string]string{
			"K_SINK":         "http://sink.example/",
			"YIPYAP_API_KEY": "secret",
			"YIPYAP_MODE":    mode,
		}))
		if err != nil {
			t.Errorf("mode %q: unexpected error: %v", mode, err)
			continue
		}
		if cfg.Mode != mode {
			t.Errorf("mode %q: Mode = %q, want %q", mode, cfg.Mode, mode)
		}
	}

	_, err := LoadConfig(fakeEnv(map[string]string{
		"K_SINK":         "http://sink.example/",
		"YIPYAP_API_KEY": "secret",
		"YIPYAP_MODE":    "weird",
	}))
	if err == nil {
		t.Fatal("expected error for invalid YIPYAP_MODE, got nil")
	}
	if !strings.Contains(err.Error(), "YIPYAP_MODE") {
		t.Errorf("error should mention YIPYAP_MODE, got %v", err)
	}
}

func TestLoadConfig_ModeIsNormalized(t *testing.T) {
	// ResolveMode trims and lowercases the mode, so LoadConfig must accept
	// the same spellings instead of exiting at startup.
	for raw, want := range map[string]string{" Auto ": "auto", "POLL": "poll", "Stream\n": "stream"} {
		cfg, err := LoadConfig(fakeEnv(map[string]string{
			"K_SINK":         "http://sink.example/",
			"YIPYAP_API_KEY": "secret",
			"YIPYAP_MODE":    raw,
		}))
		if err != nil {
			t.Errorf("mode %q: unexpected error: %v", raw, err)
			continue
		}
		if cfg.Mode != want {
			t.Errorf("mode %q: Mode = %q, want %q", raw, cfg.Mode, want)
		}
	}
}

func TestLoadConfig_PollIntervalMustBePositive(t *testing.T) {
	for _, raw := range []string{"0", "0s", "-1s", "-5m"} {
		t.Run(raw, func(t *testing.T) {
			_, err := LoadConfig(fakeEnv(map[string]string{
				"K_SINK":               "http://sink.example/",
				"YIPYAP_API_KEY":       "secret",
				"YIPYAP_POLL_INTERVAL": raw,
			}))
			if err == nil {
				t.Fatalf("YIPYAP_POLL_INTERVAL=%q: expected error, got nil", raw)
			}
			if !strings.Contains(err.Error(), "positive") {
				t.Errorf("error should say the interval must be positive, got %v", err)
			}
		})
	}
}

func TestSafePollInterval(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		0:                DefaultPollInterval,
		-1:               DefaultPollInterval,
		-time.Minute:     DefaultPollInterval,
		time.Millisecond: time.Millisecond,
		30 * time.Second: 30 * time.Second,
	}
	for in, want := range cases {
		if got := safePollInterval(in); got != want {
			t.Errorf("safePollInterval(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestLoadConfig_SourceIDAndShutdownGrace(t *testing.T) {
	base := map[string]string{"K_SINK": "http://sink/", "YIPYAP_API_KEY": "k"}

	cfg, err := LoadConfig(fakeEnv(base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceID != "" || cfg.ShutdownGrace != DefaultShutdownGrace {
		t.Errorf("defaults: SourceID=%q ShutdownGrace=%v, want empty and %v", cfg.SourceID, cfg.ShutdownGrace, DefaultShutdownGrace)
	}

	env := map[string]string{"YIPYAP_SOURCE_ID": " uid-1 ", "YIPYAP_SHUTDOWN_GRACE": "10s"}
	for k, v := range base {
		env[k] = v
	}
	cfg, err = LoadConfig(fakeEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceID != "uid-1" || cfg.ShutdownGrace != 10*time.Second {
		t.Errorf("SourceID=%q ShutdownGrace=%v, want uid-1 and 10s", cfg.SourceID, cfg.ShutdownGrace)
	}

	for _, bad := range []string{"soon", "-1s", "26s", "5m"} {
		env["YIPYAP_SHUTDOWN_GRACE"] = bad
		if _, err := LoadConfig(fakeEnv(env)); err == nil || !strings.Contains(err.Error(), "YIPYAP_SHUTDOWN_GRACE") {
			t.Errorf("YIPYAP_SHUTDOWN_GRACE=%q: err = %v, want a YIPYAP_SHUTDOWN_GRACE error", bad, err)
		}
	}
}

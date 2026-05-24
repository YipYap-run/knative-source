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
	if cfg.EventFilter != "" {
		t.Errorf("EventFilter default = %q, want empty", cfg.EventFilter)
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
	for _, mode := range []string{"", "poll", "stream"} {
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

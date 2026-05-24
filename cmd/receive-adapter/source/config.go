package source

import (
	"encoding/json"
	"fmt"
	"time"
)

// Config holds runtime configuration for the yipyap-knative-source binary.
// Values are sourced from environment variables at startup; see LoadConfig.
type Config struct {
	Sink         string            // K_SINK
	CEOverrides  map[string]string // parsed from K_CE_OVERRIDES
	APIKey       string            // YIPYAP_API_KEY
	BaseURL      string            // YIPYAP_BASE_URL
	EventFilter  string            // YIPYAP_EVENT_FILTER (path.Match glob over CloudEvent type)
	Mode         string            // "", "poll", or "stream"
	PollInterval time.Duration     // YIPYAP_POLL_INTERVAL
	CursorPath   string            // YIPYAP_CURSOR_PATH
	HealthAddr   string            // YIPYAP_HEALTH_ADDR

	// CRModeFilePath points at the single-line file the YipYapSource
	// controller writes to convey spec.mode to the running source pod.
	// Defaulted to /etc/yipyap/source-mode; overridable in tests.
	CRModeFilePath string
}

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
		Sink:           getenv("K_SINK"),
		APIKey:         getenv("YIPYAP_API_KEY"),
		BaseURL:        defaultStr(getenv("YIPYAP_BASE_URL"), "https://console.yipyap.run"),
		EventFilter:    getenv("YIPYAP_EVENT_FILTER"),
		Mode:           getenv("YIPYAP_MODE"),
		CursorPath:     defaultStr(getenv("YIPYAP_CURSOR_PATH"), "/var/run/yipyap/cursor"),
		HealthAddr:     defaultStr(getenv("YIPYAP_HEALTH_ADDR"), ":8080"),
		CEOverrides:    map[string]string{},
		PollInterval:   5 * time.Second,
		CRModeFilePath: "/etc/yipyap/source-mode",
	}

	if cfg.Sink == "" {
		return nil, fmt.Errorf("K_SINK is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("YIPYAP_API_KEY is required")
	}

	if raw := getenv("K_CE_OVERRIDES"); raw != "" {
		var env ceOverridesEnvelope
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			return nil, fmt.Errorf("K_CE_OVERRIDES: invalid JSON: %w", err)
		}
		if env.Extensions != nil {
			cfg.CEOverrides = env.Extensions
		}
	}

	if raw := getenv("YIPYAP_POLL_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("YIPYAP_POLL_INTERVAL: invalid duration %q: %w", raw, err)
		}
		cfg.PollInterval = d
	}

	switch cfg.Mode {
	case "", "poll", "stream":
		// ok
	default:
		return nil, fmt.Errorf("YIPYAP_MODE: must be one of \"\", \"poll\", \"stream\"; got %q", cfg.Mode)
	}

	return cfg, nil
}

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

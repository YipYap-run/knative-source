package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeHintServer returns a server that responds to GET /api/v1/knative/source-config
// with the supplied hint JSON and status code.
func makeHintServer(t *testing.T, status int, body any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v1/knative/source-config") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
}

// baseCfg returns a Config with just enough filled in for ResolveMode tests.
func baseCfg(t *testing.T, baseURL string) *Config {
	t.Helper()
	return &Config{
		Sink:           "http://sink.example/",
		APIKey:         "secret",
		BaseURL:        baseURL,
		PollInterval:   5 * time.Second,
		CRModeFilePath: filepath.Join(t.TempDir(), "source-mode-missing"),
	}
}

func TestResolveMode_EnvPoll(t *testing.T) {
	srv := makeHintServer(t, http.StatusOK, map[string]any{
		"recommended_mode":      "stream",
		"poll_interval_seconds": 10,
		"refresh_after_seconds": 600,
	})
	defer srv.Close()
	cfg := baseCfg(t, srv.URL)
	cfg.Mode = "poll"
	// Write a CR file that would otherwise suggest stream.
	cfg.CRModeFilePath = filepath.Join(t.TempDir(), "source-mode")
	if err := os.WriteFile(cfg.CRModeFilePath, []byte("stream"), 0o644); err != nil {
		t.Fatalf("write cr file: %v", err)
	}

	res, err := ResolveMode(context.Background(), cfg, srv.Client(), time.Now)
	if err != nil {
		t.Fatalf("ResolveMode: %v", err)
	}
	if res.Mode != "poll" || res.Source != "env" {
		t.Errorf("got mode=%q source=%q; want poll/env", res.Mode, res.Source)
	}
}

func TestResolveMode_EnvStream(t *testing.T) {
	cfg := baseCfg(t, "http://unused.invalid")
	cfg.Mode = "stream"
	res, err := ResolveMode(context.Background(), cfg, &http.Client{}, time.Now)
	if err != nil {
		t.Fatalf("ResolveMode: %v", err)
	}
	if res.Mode != "stream" || res.Source != "env" {
		t.Errorf("got mode=%q source=%q; want stream/env", res.Mode, res.Source)
	}
}

func TestResolveMode_EnvInvalid(t *testing.T) {
	cfg := baseCfg(t, "http://unused.invalid")
	cfg.Mode = "weird"
	if _, err := ResolveMode(context.Background(), cfg, &http.Client{}, time.Now); err == nil {
		t.Fatal("expected error for invalid mode, got nil")
	}
}

func TestResolveMode_CRFileOverridesServer(t *testing.T) {
	srv := makeHintServer(t, http.StatusOK, map[string]any{
		"recommended_mode": "poll",
	})
	defer srv.Close()
	cfg := baseCfg(t, srv.URL)
	cfg.CRModeFilePath = filepath.Join(t.TempDir(), "source-mode")
	if err := os.WriteFile(cfg.CRModeFilePath, []byte("stream"), 0o644); err != nil {
		t.Fatalf("write cr file: %v", err)
	}
	res, err := ResolveMode(context.Background(), cfg, srv.Client(), time.Now)
	if err != nil {
		t.Fatalf("ResolveMode: %v", err)
	}
	if res.Mode != "stream" || res.Source != "cr" {
		t.Errorf("got mode=%q source=%q; want stream/cr", res.Mode, res.Source)
	}
}

func TestResolveMode_CRFileAuto_FallsThroughToServer(t *testing.T) {
	srv := makeHintServer(t, http.StatusOK, map[string]any{
		"recommended_mode":      "stream",
		"poll_interval_seconds": 0,
		"refresh_after_seconds": 0,
	})
	defer srv.Close()
	cfg := baseCfg(t, srv.URL)
	cfg.CRModeFilePath = filepath.Join(t.TempDir(), "source-mode")
	if err := os.WriteFile(cfg.CRModeFilePath, []byte("auto\n"), 0o644); err != nil {
		t.Fatalf("write cr file: %v", err)
	}
	res, err := ResolveMode(context.Background(), cfg, srv.Client(), time.Now)
	if err != nil {
		t.Fatalf("ResolveMode: %v", err)
	}
	if res.Mode != "stream" || res.Source != "server" {
		t.Errorf("got mode=%q source=%q; want stream/server", res.Mode, res.Source)
	}
}

func TestResolveMode_CRFileInvalid_FallsThrough(t *testing.T) {
	srv := makeHintServer(t, http.StatusOK, map[string]any{
		"recommended_mode": "poll",
	})
	defer srv.Close()
	cfg := baseCfg(t, srv.URL)
	cfg.CRModeFilePath = filepath.Join(t.TempDir(), "source-mode")
	if err := os.WriteFile(cfg.CRModeFilePath, []byte("sideways"), 0o644); err != nil {
		t.Fatalf("write cr file: %v", err)
	}
	res, err := ResolveMode(context.Background(), cfg, srv.Client(), time.Now)
	if err != nil {
		t.Fatalf("ResolveMode: %v", err)
	}
	if res.Mode != "poll" || res.Source != "server" {
		t.Errorf("got mode=%q source=%q; want poll/server", res.Mode, res.Source)
	}
}

func TestResolveMode_ServerHintRespected(t *testing.T) {
	srv := makeHintServer(t, http.StatusOK, map[string]any{
		"recommended_mode":      "stream",
		"poll_interval_seconds": 10,
		"refresh_after_seconds": 600,
	})
	defer srv.Close()
	cfg := baseCfg(t, srv.URL)
	res, err := ResolveMode(context.Background(), cfg, srv.Client(), time.Now)
	if err != nil {
		t.Fatalf("ResolveMode: %v", err)
	}
	if res.Mode != "stream" || res.Source != "server" {
		t.Errorf("got mode=%q source=%q; want stream/server", res.Mode, res.Source)
	}
	if res.PollInterval != 10*time.Second {
		t.Errorf("PollInterval = %v, want 10s", res.PollInterval)
	}
	if res.RefreshAfter != 600*time.Second {
		t.Errorf("RefreshAfter = %v, want 600s", res.RefreshAfter)
	}
}

func TestResolveMode_ServerUnreachable_FallsBackToDefault(t *testing.T) {
	srv := makeHintServer(t, http.StatusInternalServerError, nil)
	defer srv.Close()
	cfg := baseCfg(t, srv.URL)
	cfg.PollInterval = 7 * time.Second
	res, err := ResolveMode(context.Background(), cfg, srv.Client(), time.Now)
	if err != nil {
		t.Fatalf("ResolveMode: %v", err)
	}
	if res.Mode != "poll" || res.Source != "default" {
		t.Errorf("got mode=%q source=%q; want poll/default", res.Mode, res.Source)
	}
	if res.PollInterval != 7*time.Second {
		t.Errorf("PollInterval = %v, want 7s (from cfg)", res.PollInterval)
	}
	if res.RefreshAfter != 5*time.Minute {
		t.Errorf("RefreshAfter = %v, want 5m", res.RefreshAfter)
	}
}

func TestResolveMode_PartialHint_UsesDefaults(t *testing.T) {
	srv := makeHintServer(t, http.StatusOK, map[string]any{
		"recommended_mode": "stream",
	})
	defer srv.Close()
	cfg := baseCfg(t, srv.URL)
	cfg.PollInterval = 3 * time.Second
	res, err := ResolveMode(context.Background(), cfg, srv.Client(), time.Now)
	if err != nil {
		t.Fatalf("ResolveMode: %v", err)
	}
	if res.Mode != "stream" || res.Source != "server" {
		t.Errorf("got mode=%q source=%q; want stream/server", res.Mode, res.Source)
	}
	if res.PollInterval != 3*time.Second {
		t.Errorf("PollInterval = %v, want 3s from cfg", res.PollInterval)
	}
	if res.RefreshAfter != 5*time.Minute {
		t.Errorf("RefreshAfter = %v, want 5m default", res.RefreshAfter)
	}
}

func TestReadCRModeFile_Missing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope")
	mode, ok := readCRModeFile(p)
	if ok {
		t.Errorf("expected ok=false for missing file, got mode=%q ok=true", mode)
	}
	if mode != "" {
		t.Errorf("expected empty mode for missing file, got %q", mode)
	}
}

func TestReadCRModeFile_Valid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "source-mode")
	if err := os.WriteFile(p, []byte("stream"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mode, ok := readCRModeFile(p)
	if !ok || mode != "stream" {
		t.Errorf("got mode=%q ok=%v; want stream/true", mode, ok)
	}
}

func TestReadCRModeFile_TrimsWhitespace(t *testing.T) {
	p := filepath.Join(t.TempDir(), "source-mode")
	if err := os.WriteFile(p, []byte("  poll\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mode, ok := readCRModeFile(p)
	if !ok || mode != "poll" {
		t.Errorf("got mode=%q ok=%v; want poll/true", mode, ok)
	}
}

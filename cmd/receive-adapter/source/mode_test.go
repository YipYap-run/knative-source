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
	"net/http"
	"net/http/httptest"
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
		Sink:         "http://sink.example/",
		APIKey:       "secret",
		BaseURL:      baseURL,
		PollInterval: 5 * time.Second,
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
	// The server recommends stream, but YIPYAP_MODE wins.
	cfg.Mode = "poll"

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

func TestResolveMode_AutoUsesServerHint(t *testing.T) {
	srv := makeHintServer(t, http.StatusOK, map[string]any{
		"recommended_mode": "stream",
	})
	defer srv.Close()
	for _, mode := range []string{"", "auto", " AUTO "} {
		cfg := baseCfg(t, srv.URL)
		cfg.Mode = mode
		res, err := ResolveMode(context.Background(), cfg, srv.Client(), time.Now)
		if err != nil {
			t.Fatalf("mode %q: ResolveMode: %v", mode, err)
		}
		if res.Mode != "stream" || res.Source != "server" {
			t.Errorf("mode %q: got mode=%q source=%q; want stream/server", mode, res.Mode, res.Source)
		}
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

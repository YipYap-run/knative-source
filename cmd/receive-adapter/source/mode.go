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
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// ModeResolution captures the effective mode plus the knobs chosen by the
// precedence resolver. Source names the layer that decided ("env", "server"
// or "default") so the logs show where the mode came from.
type ModeResolution struct {
	Mode         string        // "poll" or "stream"
	PollInterval time.Duration // only meaningful for poll; populated for both so callers don't have to branch
	RefreshAfter time.Duration // how long to trust this resolution before re-consulting the chain
	Source       string        // "env", "server", "default"
}

// defaultRefreshAfter is how long a resolution stays valid before the dispatcher
// re-runs the precedence chain. It matches the server-hint endpoint's
// default: long enough that a flapping stream doesn't storm the API, short
// enough that server-side mode changes propagate within a few minutes.
const defaultRefreshAfter = 5 * time.Minute

// ResolveMode applies the precedence chain:
//
//  1. cfg.Mode (YIPYAP_MODE, set by the controller from spec.mode) when it
//     is "poll" or "stream"
//  2. server hint from /api/v1/knative/source-config, when the mode is
//     "auto" or empty
//  3. built-in default (poll, cfg.PollInterval)
//
// A spec.mode change reaches the adapter as a new YIPYAP_MODE value, which
// rolls the adapter Deployment, so there is no in-pod mode file to watch.
//
// httpClient is injected so tests can supply an httptest.Server's Client().
// clock is reserved for tests that want deterministic timestamps; the current
// implementation doesn't consult it, but keeping the signature makes it cheap
// to add time-sensitive logic later without touching call sites.
func ResolveMode(ctx context.Context, cfg *Config, client *http.Client, _ func() time.Time) (*ModeResolution, error) {
	// 1. env (YIPYAP_MODE). Highest precedence, because it carries
	// spec.mode from the YipYapSource.
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "poll":
		return &ModeResolution{
			Mode:         "poll",
			PollInterval: cfg.PollInterval,
			RefreshAfter: defaultRefreshAfter,
			Source:       "env",
		}, nil
	case "stream":
		return &ModeResolution{
			Mode:         "stream",
			PollInterval: 0,
			RefreshAfter: defaultRefreshAfter,
			Source:       "env",
		}, nil
	case "", "auto":
		// fall through
	default:
		return nil, fmt.Errorf("invalid YIPYAP_MODE %q: must be poll, stream, auto, or empty", cfg.Mode)
	}

	// 2. server hint.
	if hint, err := fetchServerHint(ctx, client, cfg); err == nil {
		return resolutionFromHint(hint, cfg), nil
	} else {
		slog.Warn("server hint unavailable; falling back to default", "error", err)
	}

	// 3. built-in default.
	return &ModeResolution{
		Mode:         "poll",
		PollInterval: cfg.PollInterval,
		RefreshAfter: defaultRefreshAfter,
		Source:       "default",
	}, nil
}

// sourceConfigHint mirrors the JSON shape of GET /api/v1/knative/source-config.
type sourceConfigHint struct {
	RecommendedMode     string `json:"recommended_mode"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	RefreshAfterSeconds int    `json:"refresh_after_seconds"`
}

// fetchServerHint performs a single GET against the server-hint endpoint.
// It does not retry: ResolveMode falls back to the default on any error,
// and Run re-resolves when the refresh window ends.
func fetchServerHint(ctx context.Context, client *http.Client, cfg *Config) (*sourceConfigHint, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse baseURL: %w", err)
	}
	u.Path = path.Join(u.Path, "/api/v1/knative/source-config")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	setServerHeaders(req, cfg.APIKey, cfg.SourceID)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("source-config returned %d: %s", resp.StatusCode, string(body))
	}
	var h sourceConfigHint
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, fmt.Errorf("decode source-config: %w", err)
	}
	return &h, nil
}

// resolutionFromHint projects a server hint into a ModeResolution, applying
// defaults for any field the server omitted. Callers only reach this when
// YIPYAP_MODE is "auto" or empty.
func resolutionFromHint(h *sourceConfigHint, cfg *Config) *ModeResolution {
	r := &ModeResolution{Source: "server"}
	switch strings.ToLower(strings.TrimSpace(h.RecommendedMode)) {
	case "stream":
		r.Mode = "stream"
	default:
		// "poll" and any unrecognised value. Poll is the safe default.
		r.Mode = "poll"
	}
	if h.PollIntervalSeconds > 0 {
		r.PollInterval = time.Duration(h.PollIntervalSeconds) * time.Second
	} else {
		r.PollInterval = cfg.PollInterval
	}
	if h.RefreshAfterSeconds > 0 {
		r.RefreshAfter = time.Duration(h.RefreshAfterSeconds) * time.Second
	} else {
		r.RefreshAfter = defaultRefreshAfter
	}
	return r
}

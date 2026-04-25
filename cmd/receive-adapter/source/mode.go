package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// ModeResolution captures the effective mode plus the knobs chosen by the
// precedence resolver. Source is an audit-trail string — "env" / "cr" /
// "server" / "default" — so logs make it obvious which layer decided.
type ModeResolution struct {
	Mode         string        // "poll" or "stream"
	PollInterval time.Duration // only meaningful for poll; populated for both so callers don't have to branch
	RefreshAfter time.Duration // how long to trust this resolution before re-consulting the chain
	Source       string        // "env", "cr", "server", "default"
}

// defaultRefreshAfter is how long a resolution stays valid before the dispatcher
// re-runs the precedence chain. Picked to match the server-hint endpoint's
// default — long enough that a flapping stream doesn't storm the API, short
// enough that operator-side mode flips propagate within a few minutes.
const defaultRefreshAfter = 5 * time.Minute

// ResolveMode applies the precedence chain:
//
//  1. cfg.Mode (from YIPYAP_MODE env; non-empty and non-"auto")
//  2. CR spec file (cfg.CRModeFilePath, controller-written)
//  3. server hint from /api/v1/knative/source-config
//  4. ship default (poll, cfg.PollInterval)
//
// httpClient is injected so tests can supply an httptest.Server's Client().
// clock is reserved for tests that want deterministic timestamps; the current
// implementation doesn't consult it, but keeping the signature makes it cheap
// to add time-sensitive logic later without touching call sites.
func ResolveMode(ctx context.Context, cfg *Config, client *http.Client, _ func() time.Time) (*ModeResolution, error) {
	// 1. env (YIPYAP_MODE) — highest precedence, because the pod spec is
	// the operator's most direct knob.
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

	// 2. CR spec file — the controller drops a single-line file into the
	// pod so spec.mode changes don't require a restart. Absence / empty /
	// unrecognised content all fall through.
	crPath := cfg.CRModeFilePath
	if crPath == "" {
		crPath = "/etc/yipyap/source-mode"
	}
	if m, ok := readCRModeFile(crPath); ok {
		switch m {
		case "poll", "stream":
			return &ModeResolution{
				Mode:         m,
				PollInterval: cfg.PollInterval,
				RefreshAfter: defaultRefreshAfter,
				Source:       "cr",
			}, nil
		case "auto":
			// fall through to server hint
		default:
			slog.Warn("CR source-mode file has unrecognised content; ignoring",
				"path", crPath, "content", m)
		}
	}

	// 3. server hint.
	if hint, err := fetchServerHint(ctx, client, cfg); err == nil {
		return resolutionFromHint(hint, cfg), nil
	} else {
		slog.Warn("server hint unavailable; falling back to default", "error", err)
	}

	// 4. ship default.
	return &ModeResolution{
		Mode:         "poll",
		PollInterval: cfg.PollInterval,
		RefreshAfter: defaultRefreshAfter,
		Source:       "default",
	}, nil
}

// readCRModeFile reads the single-line CR-written file. Returns (lowercased
// trimmed mode, true) when the file exists and has non-empty content;
// (empty, false) otherwise. An empty file is treated as "no opinion" — the
// controller occasionally writes empty then rewrites, and we'd rather fall
// through than crash.
func readCRModeFile(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	s := strings.ToLower(strings.TrimSpace(string(b)))
	if s == "" {
		return "", false
	}
	return s, true
}

// sourceConfigHint mirrors the JSON shape of GET /api/v1/knative/source-config.
type sourceConfigHint struct {
	RecommendedMode     string `json:"recommended_mode"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	RefreshAfterSeconds int    `json:"refresh_after_seconds"`
}

// fetchServerHint performs a single GET against the server-hint endpoint.
// It is intentionally short-and-sweet — the enclosing resolver already
// knows how to handle an error (fall through to default) so there's no
// value in retrying here.
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
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
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
// the precedence chain actually wants the server's opinion — either env was
// empty/auto and no CR file was set, or the CR file said "auto".
func resolutionFromHint(h *sourceConfigHint, cfg *Config) *ModeResolution {
	r := &ModeResolution{Source: "server"}
	switch strings.ToLower(strings.TrimSpace(h.RecommendedMode)) {
	case "stream":
		r.Mode = "stream"
	default:
		// Includes "poll" and any unrecognised value — safe default is poll.
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

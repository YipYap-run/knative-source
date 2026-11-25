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

// Package baseurl holds the operator policy for YipYapSource spec.baseURL.
//
// The adapter sends the API key from spec.apiKeyRef as a bearer token to
// spec.baseURL, so the URL decides where a Secret value goes. The policy
// requires https, rejects user info, and only allows hosts the cluster
// operator listed. The webhook enforces it at admission and the controller
// enforces it again before it renders the adapter Deployment.
//
// Allowlist entries:
//
//   - "console.yipyap.run" matches that host (case-insensitive) on the
//     default https port only (no port, or :443).
//   - "yipyap.internal:8443" matches that host on port 8443 only.
//   - "*.example.com" matches any subdomain of example.com (not example.com
//     itself) on the default https port. "*.example.com:8443" adds a port.
//   - IPv6 literals use brackets: "[fd00::1]" or "[fd00::1]:8443".
package baseurl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	// EnvAllowedHosts is the environment variable the chart sets on the
	// webhook and controller. It holds a comma separated allowlist.
	EnvAllowedHosts = "YIPYAP_ALLOWED_BASE_URL_HOSTS"

	// DefaultAllowedHost is the allowlist used when EnvAllowedHosts is unset.
	DefaultAllowedHost = "console.yipyap.run"

	// DefaultBaseURL is what the adapter uses when spec.baseURL is empty.
	// Keep in sync with v1alpha1.DefaultBaseURL and the adapter config.
	DefaultBaseURL = "https://console.yipyap.run"

	defaultHTTPSPort = "443"
)

type entry struct {
	// host is lowercase without a trailing dot. For a wildcard entry it is
	// the suffix after "*." (for example "example.com").
	host     string
	wildcard bool
	// port is the required port, "443" when the entry has none.
	port string
	// raw is the entry as the operator wrote it, for messages.
	raw string
}

// Policy is an operator host allowlist for spec.baseURL. The zero value
// allows nothing; use Default, Parse or FromEnv.
type Policy struct {
	entries []entry
}

// Default returns the policy used when the operator configured nothing.
func Default() *Policy {
	p, err := Parse(DefaultAllowedHost)
	if err != nil {
		panic(err) // constant input
	}
	return p
}

// FromEnv reads the allowlist from EnvAllowedHosts via lookup (pass
// os.LookupEnv). An unset variable yields Default. A variable that is set but
// lists no hosts is an error, so a misconfigured install fails at startup
// instead of quietly rejecting every source.
func FromEnv(lookup func(string) (string, bool)) (*Policy, error) {
	v, ok := lookup(EnvAllowedHosts)
	if !ok {
		return Default(), nil
	}
	p, err := Parse(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvAllowedHosts, err)
	}
	return p, nil
}

// Parse builds a policy from a comma or whitespace separated list of
// allowlist entries. It returns an error for an empty list or any entry
// that is not a plain host, host:port or *.domain pattern.
func Parse(list string) (*Policy, error) {
	fields := strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(fields) == 0 {
		return nil, errors.New("the base URL host allowlist is empty; list at least one host")
	}
	p := &Policy{}
	for _, f := range fields {
		e, err := parseEntry(f)
		if err != nil {
			return nil, err
		}
		p.entries = append(p.entries, e)
	}
	return p, nil
}

func parseEntry(raw string) (entry, error) {
	bad := func(why string) (entry, error) {
		return entry{}, fmt.Errorf("invalid base URL allowlist entry %q: %s", raw, why)
	}
	if strings.Contains(raw, "://") || strings.ContainsAny(raw, "/@?#") {
		return bad("use a bare host such as console.yipyap.run, without scheme, path or user info")
	}

	host, port := raw, ""
	switch {
	case strings.HasPrefix(raw, "["):
		end := strings.Index(raw, "]")
		if end < 0 {
			return bad("unclosed IPv6 bracket")
		}
		host = raw[1:end]
		rest := raw[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return bad("unexpected text after IPv6 address")
			}
			port = rest[1:]
		}
		if net.ParseIP(host) == nil {
			return bad("not an IP address inside brackets")
		}
	case strings.Count(raw, ":") == 1:
		h, pt, err := net.SplitHostPort(raw)
		if err != nil {
			return bad(err.Error())
		}
		host, port = h, pt
	case strings.Count(raw, ":") > 1:
		return bad("IPv6 addresses must be written in brackets, for example [fd00::1]")
	}

	if port != "" && !validPort(port) {
		return bad("port must be a number from 1 to 65535")
	}
	if port == "" {
		port = defaultHTTPSPort
	}

	host = normalizeHost(host)
	wildcard := false
	if strings.HasPrefix(host, "*.") {
		wildcard = true
		host = host[2:]
	}
	if host == "" {
		return bad("missing host")
	}
	if strings.Contains(host, "*") {
		return bad(`a wildcard is only allowed as a leading "*." label`)
	}
	if wildcard && net.ParseIP(host) != nil {
		return bad("a wildcard cannot be combined with an IP address")
	}
	return entry{host: host, wildcard: wildcard, port: port, raw: raw}, nil
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == p
}

func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// Hosts returns the allowlist entries as configured, for messages.
func (p *Policy) Hosts() []string {
	out := make([]string, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, e.raw)
	}
	return out
}

// Check returns nil when raw is an https URL without user info whose host
// and port match an allowlist entry. An empty raw is checked as
// DefaultBaseURL, which is what the adapter falls back to.
func (p *Policy) Check(raw string) error {
	if raw == "" {
		raw = DefaultBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("must be a valid URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return errors.New("must use https")
	}
	if u.Opaque != "" {
		return errors.New("must be an absolute URL such as https://console.yipyap.run")
	}
	if u.User != nil {
		return errors.New("must not contain user info (user:password@)")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("must not contain a query or fragment")
	}
	host := normalizeHost(u.Hostname())
	if host == "" {
		return errors.New("must include a host")
	}
	port := u.Port()
	if port == "" {
		port = defaultHTTPSPort
	} else if !validPort(port) {
		return errors.New("port must be a number from 1 to 65535")
	}
	if p.allows(host, port) {
		return nil
	}
	return fmt.Errorf("host %q is not allowed by the operator; allowed hosts: %s",
		u.Host, strings.Join(p.Hosts(), ", "))
}

func (p *Policy) allows(host, port string) bool {
	if p == nil {
		return false
	}
	for _, e := range p.entries {
		if e.port != port {
			continue
		}
		if e.wildcard {
			if strings.HasSuffix(host, "."+e.host) && len(host) > len(e.host)+1 {
				return true
			}
			continue
		}
		if host == e.host {
			return true
		}
	}
	return false
}

type policyKey struct{}

// WithPolicy returns a context that carries p.
func WithPolicy(ctx context.Context, p *Policy) context.Context {
	return context.WithValue(ctx, policyKey{}, p)
}

// FromContext returns the policy stored by WithPolicy, or Default when the
// context carries none. Callers always get a policy, so a missing injection
// falls back to the strict default instead of allowing every host.
func FromContext(ctx context.Context) *Policy {
	if p, ok := ctx.Value(policyKey{}).(*Policy); ok && p != nil {
		return p
	}
	return Default()
}

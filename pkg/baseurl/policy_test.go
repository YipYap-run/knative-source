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

package baseurl

import (
	"context"
	"strings"
	"testing"
)

func mustParse(t *testing.T, list string) *Policy {
	t.Helper()
	p, err := Parse(list)
	if err != nil {
		t.Fatalf("Parse(%q): %v", list, err)
	}
	return p
}

func TestCheck(t *testing.T) {
	def := Default()
	custom := mustParse(t, "console.yipyap.run, yipyap.internal:8443,*.corp.example.com,[fd00::1]")

	cases := []struct {
		name    string
		policy  *Policy
		url     string
		wantErr string // empty means allowed
	}{
		{"default host", def, "https://console.yipyap.run", ""},
		{"default host with path", def, "https://console.yipyap.run/prefix", ""},
		{"empty means default", def, "", ""},
		{"host is case-insensitive", def, "https://CONSOLE.YipYap.run", ""},
		{"trailing dot", def, "https://console.yipyap.run.", ""},
		{"explicit 443", def, "https://console.yipyap.run:443", ""},
		{"scheme is case-insensitive", def, "HTTPS://console.yipyap.run", ""},
		{"http rejected", def, "http://console.yipyap.run", "must use https"},
		{"other scheme rejected", def, "ftp://console.yipyap.run", "must use https"},
		{"userinfo rejected", def, "https://user:pw@console.yipyap.run", "user info"},
		{"username only rejected", def, "https://user@console.yipyap.run", "user info"},
		{"unlisted host rejected", def, "https://attacker.example", "not allowed"},
		{"suffix trick rejected", def, "https://console.yipyap.run.attacker.example", "not allowed"},
		{"subdomain not implied", def, "https://eu.console.yipyap.run", "not allowed"},
		{"other port rejected", def, "https://console.yipyap.run:8443", "not allowed"},
		{"query rejected", def, "https://console.yipyap.run/?x=1", "query"},
		{"fragment rejected", def, "https://console.yipyap.run/#x", "query"},
		{"no host", def, "https:///path", "must include a host"},
		{"relative", def, "console.yipyap.run", "must use https"},
		{"opaque", def, "https:console.yipyap.run", "absolute URL"},
		{"listed port", custom, "https://yipyap.internal:8443", ""},
		{"listed host wrong port", custom, "https://yipyap.internal", "not allowed"},
		{"wildcard subdomain", custom, "https://a.corp.example.com", ""},
		{"wildcard deep subdomain", custom, "https://a.b.corp.example.com", ""},
		{"wildcard apex rejected", custom, "https://corp.example.com", "not allowed"},
		{"wildcard lookalike rejected", custom, "https://evilcorp.example.com", "not allowed"},
		{"ipv6 listed", custom, "https://[fd00::1]", ""},
		{"ipv6 unlisted", custom, "https://[fd00::2]", "not allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Check(tc.url)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Check(%q) = %v, want allowed", tc.url, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Check(%q) allowed, want error containing %q", tc.url, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Check(%q) = %q, want it to contain %q", tc.url, err, tc.wantErr)
			}
		})
	}
}

func TestCheckMessageListsAllowedHosts(t *testing.T) {
	err := Default().Check("https://attacker.example")
	if err == nil || !strings.Contains(err.Error(), "console.yipyap.run") {
		t.Fatalf("error should name the allowed hosts, got %v", err)
	}
}

func TestParseRejectsBadEntries(t *testing.T) {
	for _, in := range []string{
		"",
		" , ",
		"https://console.yipyap.run",
		"console.yipyap.run/path",
		"user@console.yipyap.run",
		"*",
		"*.",
		"a.*.example.com",
		"console.yipyap.run:0",
		"console.yipyap.run:99999",
		"console.yipyap.run:abc",
		"fd00::1",
		"[fd00::1",
		"[not-an-ip]",
		"*.10.0.0.1",
	} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", in)
		}
	}
}

func TestFromEnv(t *testing.T) {
	unset := func(string) (string, bool) { return "", false }
	p, err := FromEnv(unset)
	if err != nil {
		t.Fatalf("FromEnv(unset): %v", err)
	}
	if got := p.Hosts(); len(got) != 1 || got[0] != DefaultAllowedHost {
		t.Fatalf("FromEnv(unset) hosts = %v, want [%s]", got, DefaultAllowedHost)
	}

	empty := func(string) (string, bool) { return "", true }
	if _, err := FromEnv(empty); err == nil || !strings.Contains(err.Error(), EnvAllowedHosts) {
		t.Fatalf("FromEnv(empty) = %v, want an error naming %s", err, EnvAllowedHosts)
	}

	set := func(string) (string, bool) { return "a.example,b.example", true }
	p, err = FromEnv(set)
	if err != nil {
		t.Fatalf("FromEnv(set): %v", err)
	}
	if p.Check("https://b.example") != nil || p.Check("https://console.yipyap.run") == nil {
		t.Fatalf("FromEnv(set) should replace the default allowlist, hosts = %v", p.Hosts())
	}
}

func TestFromContext(t *testing.T) {
	if FromContext(context.Background()).Check("https://console.yipyap.run") != nil {
		t.Fatal("a context without a policy should fall back to the default")
	}
	ctx := WithPolicy(context.Background(), mustParse(t, "only.example"))
	if FromContext(ctx).Check("https://console.yipyap.run") == nil {
		t.Fatal("the injected policy should replace the default")
	}
}

func TestZeroPolicyAllowsNothing(t *testing.T) {
	var p Policy
	if p.Check("https://console.yipyap.run") == nil {
		t.Fatal("the zero Policy must not allow any host")
	}
}

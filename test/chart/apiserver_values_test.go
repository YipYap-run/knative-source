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

package chart

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeKubectl answers the four queries hack/apiserver-cidrs-values.sh makes.
const fakeKubectl = `#!/usr/bin/env bash
case "$*" in
  *endpointslices*addresses*) printf '%s' "$FAKE_ENDPOINTS" ;;
  *endpointslices*ports*) printf '%s' "$FAKE_ENDPOINT_PORTS" ;;
  *service*clusterIP*) printf '%s' "$FAKE_CLUSTER_IP" ;;
  *service*ports*) printf '%s' "$FAKE_SERVICE_PORTS" ;;
  *) echo "unexpected kubectl call: $*" >&2; exit 2 ;;
esac
`

func runValuesScript(t *testing.T, env map[string]string) (string, string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(fakeKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "hack", "apiserver-cidrs-values.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, script)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

// The values file the script prints must carry the endpoint and Service
// ports, so clusters with a nonstandard API server port (MicroK8s uses
// 16443) get a working policy, and it must render with the chart.
func TestAPIServerValuesScript(t *testing.T) {
	out, stderr, err := runValuesScript(t, map[string]string{
		"FAKE_ENDPOINTS":      "10.0.0.5\n10.0.0.6\nfd00::5\n",
		"FAKE_ENDPOINT_PORTS": "16443\n",
		"FAKE_CLUSTER_IP":     "10.152.183.1",
		"FAKE_SERVICE_PORTS":  "443\n",
	})
	if err != nil {
		t.Fatalf("script: %v: %s", err, stderr)
	}
	values := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(values, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	r := mustRender(t, "-f", values)
	for _, name := range []string{controllerPolicy, webhookPolicy} {
		np := r.policies[name]
		if np == nil {
			t.Fatalf("policy %s not rendered", name)
		}
		cidrs, ports := apiServerEgress(np)
		wantCIDRs := []string{"10.0.0.5/32", "10.0.0.6/32", "10.152.183.1/32", "fd00::5/128"}
		if !slices.Equal(cidrs, wantCIDRs) {
			t.Errorf("%s egress CIDRs = %v, want %v", name, cidrs, wantCIDRs)
		}
		if want := []int{443, 16443}; !slices.Equal(ports, want) {
			t.Errorf("%s egress ports = %v, want %v", name, ports, want)
		}
	}
}

func TestAPIServerValuesScriptRejectsBadInput(t *testing.T) {
	good := map[string]string{
		"FAKE_ENDPOINTS":      "10.0.0.5\n",
		"FAKE_ENDPOINT_PORTS": "6443\n",
		"FAKE_CLUSTER_IP":     "10.96.0.1",
		"FAKE_SERVICE_PORTS":  "443\n",
	}
	cases := map[string]map[string]string{
		"no addresses":    {"FAKE_ENDPOINTS": "", "FAKE_CLUSTER_IP": ""},
		"no ports":        {"FAKE_ENDPOINT_PORTS": "", "FAKE_SERVICE_PORTS": ""},
		"quote injection": {"FAKE_ENDPOINTS": "10.0.0.5\"\n"},
		"hostname":        {"FAKE_ENDPOINTS": "api.example.com\n"},
		"port range":      {"FAKE_ENDPOINT_PORTS": "70000\n"},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range good {
				env[k] = v
			}
			for k, v := range override {
				env[k] = v
			}
			out, _, err := runValuesScript(t, env)
			if err == nil {
				t.Fatalf("script accepted bad input, printed:\n%s", out)
			}
			if strings.TrimSpace(out) != "" {
				t.Errorf("script printed a partial values file on failure:\n%s", out)
			}
		})
	}
}

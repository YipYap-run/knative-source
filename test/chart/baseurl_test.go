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
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/YipYap-run/knative-source/pkg/baseurl"
)

// allowlistEnv renders the chart and returns YIPYAP_ALLOWED_BASE_URL_HOSTS
// per Deployment name.
func allowlistEnv(t *testing.T, args ...string) map[string]string {
	t.Helper()
	out, stderr, err := render(t, append([]string{"-f", filepath.Join(chartDir(t), ciValues)}, args...)...)
	if err != nil {
		t.Fatalf("helm template %v: %v: %s", args, err, stderr)
	}
	got := map[string]string{}
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(out), 4096)
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				return got
			}
			t.Fatalf("decode manifest: %v", err)
		}
		if obj == nil || (&unstructured.Unstructured{Object: obj}).GetKind() != "Deployment" {
			continue
		}
		d := &appsv1.Deployment{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, d); err != nil {
			t.Fatalf("convert Deployment: %v", err)
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			for _, e := range c.Env {
				if e.Name == baseurl.EnvAllowedHosts {
					got[d.Name] = e.Value
				}
			}
		}
	}
}

func TestBaseURLAllowlistDefault(t *testing.T) {
	env := allowlistEnv(t)
	for _, name := range []string{"yipyap-source-controller", "yipyap-source-webhook"} {
		v, ok := env[name]
		if !ok {
			t.Errorf("%s does not set %s", name, baseurl.EnvAllowedHosts)
			continue
		}
		if v != "console.yipyap.run" {
			t.Errorf("%s %s = %q, want console.yipyap.run", name, baseurl.EnvAllowedHosts, v)
		}
	}
}

func TestBaseURLAllowlistCustomParses(t *testing.T) {
	env := allowlistEnv(t, "--set", "webhook.allowedBaseURLHosts={yipyap.corp.example,*.eu.example.com:8443,[fd00::1]}")
	v := env["yipyap-source-webhook"]
	if v != env["yipyap-source-controller"] {
		t.Fatalf("webhook and controller disagree: %q vs %q", v, env["yipyap-source-controller"])
	}
	p, err := baseurl.Parse(v)
	if err != nil {
		t.Fatalf("rendered allowlist %q does not parse: %v", v, err)
	}
	if p.Check("https://a.eu.example.com:8443") != nil || p.Check("https://console.yipyap.run") == nil {
		t.Errorf("rendered allowlist %q does not behave as configured", v)
	}
}

func TestBaseURLAllowlistRejectsBadValues(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(empty, []byte("webhook:\n  allowedBaseURLHosts: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"empty list": {"-f", empty},
		"null":       {"--set", "webhook.allowedBaseURLHosts=null"},
		"scheme":     {"--set", "webhook.allowedBaseURLHosts={https://console.yipyap.run}"},
		"path":       {"--set", "webhook.allowedBaseURLHosts={console.yipyap.run/x}"},
		"bare star":  {"--set", "webhook.allowedBaseURLHosts={*}"},
	} {
		t.Run(name, func(t *testing.T) {
			a := append([]string{"-f", filepath.Join(chartDir(t), ciValues)}, args...)
			if _, _, err := render(t, a...); err == nil {
				t.Fatalf("helm template accepted %v", args)
			}
		})
	}
}

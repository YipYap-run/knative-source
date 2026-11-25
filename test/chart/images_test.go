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
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

const (
	controllerRepo = "ghcr.io/yipyap-run/knative-source-controller"
	webhookRepo    = "ghcr.io/yipyap-run/knative-source-webhook"
	adapterRepo    = "ghcr.io/yipyap-run/knative-source-receive-adapter"
	raImageEnv     = "YIPYAP_SOURCE_RA_IMAGE"
)

var (
	digestA = "sha256:" + strings.Repeat("a", 64)
	digestB = "sha256:" + strings.Repeat("b", 64)
	digestC = "sha256:" + strings.Repeat("c", 64)
)

// imageRefs renders the chart in dir and returns the controller, webhook
// and adapter image references it deploys.
func imageRefs(t *testing.T, dir string, args ...string) map[string]string {
	t.Helper()
	cmd := exec.Command(helmBin(t), append([]string{"template", "yipyap-source", dir, "-f", filepath.Join(chartDir(t), ciValues)}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helm template %v: %v: %s", args, err, stderr.String())
	}
	got := map[string]string{}
	dec := utilyaml.NewYAMLOrJSONDecoder(&stdout, 4096)
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
			switch d.Name {
			case "yipyap-source-controller":
				got["controller"] = c.Image
				for _, e := range c.Env {
					if e.Name == raImageEnv {
						got["adapter"] = e.Value
					}
				}
			case "yipyap-source-webhook":
				got["webhook"] = c.Image
			}
		}
	}
}

func appVersion(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(chartDir(t), "Chart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^appVersion:\s*"?([^"\s]+)"?\s*$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("Chart.yaml has no appVersion")
	}
	return string(m[1])
}

func checkRefs(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s image = %q, want %q", k, got[k], w)
		}
	}
}

func TestImagesDefaultToAppVersionTag(t *testing.T) {
	v := appVersion(t)
	checkRefs(t, imageRefs(t, chartDir(t)), map[string]string{
		"controller": controllerRepo + ":" + v,
		"webhook":    webhookRepo + ":" + v,
		"adapter":    adapterRepo + ":" + v,
	})
}

func TestImageDigestPinsRepoAtDigest(t *testing.T) {
	checkRefs(t, imageRefs(t, chartDir(t),
		"--set", "images.controller.digest="+digestA,
		"--set", "images.webhook.digest="+digestB,
		"--set", "images.adapter.digest="+digestC,
	), map[string]string{
		"controller": controllerRepo + "@" + digestA,
		"webhook":    webhookRepo + "@" + digestB,
		"adapter":    adapterRepo + "@" + digestC,
	})
}

func TestImageRejectsTagWithDigestAndBadDigest(t *testing.T) {
	for name, args := range map[string][]string{
		"tag and digest": {"--set", "images.webhook.digest=" + digestA, "--set", "images.webhook.tag=0.1.0"},
		"short digest":   {"--set", "images.adapter.digest=sha256:abc"},
		"no algorithm":   {"--set", "images.controller.digest=" + strings.Repeat("a", 64)},
		"uppercase hex":  {"--set", "images.controller.digest=sha256:" + strings.Repeat("A", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			a := append([]string{"-f", filepath.Join(chartDir(t), ciValues)}, args...)
			if _, _, err := render(t, a...); err == nil {
				t.Fatalf("helm template accepted %v", args)
			}
		})
	}
}

// copyChart copies the chart to a temp dir so the release script can edit
// its values.yaml.
func copyChart(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "yipyap-source")
	src := chartDir(t)
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func setDigests(t *testing.T, values string, pairs ...string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	script := filepath.Join(chartDir(t), "..", "..", "hack", "set-image-digests.sh")
	out, err := exec.Command(bash, append([]string{script, values}, pairs...)...).CombinedOutput()
	return string(out), err
}

// The release runs hack/set-image-digests.sh before packaging, so the
// packaged chart must deploy exactly the digests it was given.
func TestSetImageDigestsPinsPackagedChart(t *testing.T) {
	dir := copyChart(t)
	values := filepath.Join(dir, "values.yaml")
	if out, err := setDigests(t, values, "controller="+digestA, "webhook="+digestB, "adapter="+digestC); err != nil {
		t.Fatalf("set-image-digests.sh: %v: %s", err, out)
	}
	checkRefs(t, imageRefs(t, dir), map[string]string{
		"controller": controllerRepo + "@" + digestA,
		"webhook":    webhookRepo + "@" + digestB,
		"adapter":    adapterRepo + "@" + digestC,
	})

	// A second run finds no empty digest left and must fail rather than
	// stack a second value.
	if _, err := setDigests(t, values, "controller="+digestB); err == nil {
		t.Error("set-image-digests.sh rewrote an already pinned digest")
	}
}

func TestSetImageDigestsRejectsBadInput(t *testing.T) {
	for name, pair := range map[string]string{
		"unknown key": "receive-adapter=" + digestA,
		"bad digest":  "controller=sha256:xyz",
		"tag":         "webhook=1.2.3",
	} {
		t.Run(name, func(t *testing.T) {
			dir := copyChart(t)
			values := filepath.Join(dir, "values.yaml")
			before, err := os.ReadFile(values)
			if err != nil {
				t.Fatal(err)
			}
			// The valid pair first proves a later failure leaves the
			// file untouched.
			if _, err := setDigests(t, values, "controller="+digestC, pair); err == nil {
				t.Fatalf("set-image-digests.sh accepted %q", pair)
			}
			after, err := os.ReadFile(values)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("values.yaml changed after a failed run")
			}
		})
	}
}

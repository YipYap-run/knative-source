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

// Package release tests the release workflow and the scripts it runs.
package release

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const baseImage = "cgr.dev/chainguard/static"

var (
	digestA = "sha256:" + strings.Repeat("a", 64)
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func bash(t *testing.T) string {
	t.Helper()
	b, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	return b
}

// fakeCrane writes a crane stand-in that answers `crane digest REF` from
// the given map: a value starting with "sha256:" is printed as the digest,
// anything else is printed to stderr as the failure.
func fakeCrane(t *testing.T, answers map[string]string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n[ \"$1\" = digest ] || exit 64\ncase \"$2\" in\n")
	for ref, ans := range answers {
		if strings.HasPrefix(ans, "sha256:") {
			b.WriteString("  '" + ref + "') echo '" + ans + "' ;;\n")
		} else {
			b.WriteString("  '" + ref + "') echo 'Error: " + ans + "' >&2; exit 1 ;;\n")
		}
	}
	b.WriteString("  *) echo 'Error: unexpected ref' >&2; exit 1 ;;\nesac\n")
	p := filepath.Join(t.TempDir(), "crane")
	if err := os.WriteFile(p, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func runScript(t *testing.T, env []string, script string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bash(t), append([]string{filepath.Join(repoRoot(t), "hack", script)}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestReleaseTagAbsent(t *testing.T) {
	const (
		repo     = "ghcr.io/yipyap-run/knative-source-controller"
		missing  = repo + ":9.9.9"
		existing = repo + ":0.1.0"
		newRepo  = "ghcr.io/yipyap-run/knative-source-new:0.1.0"
		denied   = repo + ":0.2.0"
	)
	crane := fakeCrane(t, map[string]string{
		missing:  "GET https://ghcr.io/v2/x/manifests/9.9.9: MANIFEST_UNKNOWN: manifest unknown",
		existing: digestA,
		newRepo:  "GET https://ghcr.io/v2/x/manifests/0.1.0: NAME_UNKNOWN: repository name not known",
		denied:   "GET https://ghcr.io/token: DENIED: denied",
	})
	env := []string{"CRANE=" + crane}

	for name, tc := range map[string]struct {
		refs []string
		ok   bool
	}{
		"absent tag":          {[]string{missing}, true},
		"absent repository":   {[]string{newRepo}, true},
		"existing tag":        {[]string{existing}, false},
		"one of many exists":  {[]string{missing, existing, newRepo}, false},
		"registry error":      {[]string{denied}, false},
		"all absent together": {[]string{missing, newRepo}, true},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := runScript(t, env, "release-tag-absent.sh", tc.refs...)
			if (err == nil) != tc.ok {
				t.Fatalf("release-tag-absent.sh %v: err=%v, want ok=%v\n%s", tc.refs, err, tc.ok, out)
			}
			if !tc.ok && !strings.Contains(out, "::error::") {
				t.Errorf("refusal does not annotate the job:\n%s", out)
			}
		})
	}
}

var pinRE = regexp.MustCompile(regexp.QuoteMeta(baseImage) + `(:[^@\s]+)?(@sha256:[a-f0-9]{64})?`)

// Every reference to the base image in .ko.yaml is pinned by digest, and
// to the same digest, so the bump script moves them all together.
func TestKoBaseImagePinnedByDigest(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".ko.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var digests []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range pinRE.FindAllStringSubmatch(line, -1) {
			if m[2] == "" || m[1] != "" {
				t.Errorf(".ko.yaml references %s without a bare digest pin: %q", baseImage, m[0])
				continue
			}
			digests = append(digests, m[2])
		}
	}
	if len(digests) == 0 {
		t.Fatalf(".ko.yaml does not pin %s", baseImage)
	}
	slices.Sort(digests)
	if len(slices.Compact(digests)) != 1 {
		t.Errorf(".ko.yaml pins %s to several digests: %v", baseImage, digests)
	}
}

func TestBumpBaseImage(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot(t), ".ko.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ko := filepath.Join(t.TempDir(), ".ko.yaml")
	if err := os.WriteFile(ko, src, 0o644); err != nil {
		t.Fatal(err)
	}
	crane := fakeCrane(t, map[string]string{baseImage + ":latest": digestA})

	out, err := runScript(t, []string{"CRANE=" + crane}, "bump-base-image.sh", ko)
	if err != nil {
		t.Fatalf("bump-base-image.sh: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != digestA {
		t.Errorf("bump-base-image.sh printed %q, want the new digest", out)
	}
	got, err := os.ReadFile(ko)
	if err != nil {
		t.Fatal(err)
	}
	old := pinRE.FindString(strings.SplitN(string(src), "defaultBaseImage:", 2)[1])
	if strings.Contains(string(got), old) {
		t.Errorf("old pin %s survived the bump", old)
	}
	if n, want := strings.Count(string(got), baseImage+"@"+digestA), strings.Count(string(src), baseImage+"@sha256:"); n != want || n == 0 {
		t.Errorf("new digest pinned %d times, want %d", n, want)
	}

	// Already current: no output, no change.
	out, err = runScript(t, []string{"CRANE=" + crane}, "bump-base-image.sh", ko)
	if err != nil || strings.TrimSpace(out) != "" {
		t.Errorf("second run: err=%v out=%q, want a silent no-op", err, out)
	}
	again, _ := os.ReadFile(ko)
	if string(again) != string(got) {
		t.Error("second run changed the file")
	}

	// A lookup failure or a malformed digest must fail and leave the
	// file alone.
	for name, env := range map[string][]string{
		"lookup fails": {"CRANE=" + fakeCrane(t, map[string]string{baseImage + ":latest": "DENIED"})},
		"bad digest":   {"BASE_IMAGE_DIGEST=sha256:" + strings.Repeat("z", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			if out, err := runScript(t, env, "bump-base-image.sh", ko); err == nil {
				t.Fatalf("bump-base-image.sh succeeded: %s", out)
			}
			after, _ := os.ReadFile(ko)
			if string(after) != string(got) {
				t.Error("failed run changed the file")
			}
		})
	}
}

type workflow struct {
	Permissions map[string]string `json:"permissions"`
	Concurrency map[string]any    `json:"concurrency"`
	Jobs        map[string]struct {
		Needs       any               `json:"needs"`
		Permissions map[string]string `json:"permissions"`
		Strategy    map[string]any    `json:"strategy"`
		Steps       []struct {
			Name string         `json:"name"`
			Uses string         `json:"uses"`
			Run  string         `json:"run"`
			With map[string]any `json:"with"`
		} `json:"steps"`
	} `json:"jobs"`
}

func loadWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return w
}

func needs(v any) []string {
	switch n := v.(type) {
	case string:
		return []string{n}
	case []any:
		var out []string
		for _, s := range n {
			out = append(out, s.(string))
		}
		return out
	}
	return nil
}

func TestReleaseWorkflowContract(t *testing.T) {
	w := loadWorkflow(t, "release.yaml")
	if len(w.Permissions) != 1 || w.Permissions["contents"] != "read" {
		t.Errorf("workflow permissions = %v, want only contents: read", w.Permissions)
	}

	// Two runs for the same tag must not race between the tag check and
	// the push, and a run in progress must never be cancelled mid-push.
	if g := fmt.Sprint(w.Concurrency["group"]); !strings.Contains(g, "inputs.tag || github.ref_name") {
		t.Errorf("concurrency group = %q, want one keyed on the release tag", g)
	}
	if c := fmt.Sprint(w.Concurrency["cancel-in-progress"]); c != "false" {
		t.Errorf("concurrency cancel-in-progress = %s, want false", c)
	}
	// A failing leg must not cancel a sibling that may be mid-push.
	for _, name := range []string{"images", "sign"} {
		if ff := fmt.Sprint(w.Jobs[name].Strategy["fail-fast"]); ff != "false" {
			t.Errorf("%s job fail-fast = %s, want false", name, ff)
		}
	}

	// Only the sign job can mint an OIDC token.
	for name, j := range w.Jobs {
		if _, ok := j.Permissions["id-token"]; ok && name != "sign" {
			t.Errorf("job %s has id-token permission", name)
		}
	}
	sign := w.Jobs["sign"]
	if sign.Permissions["id-token"] != "write" || sign.Permissions["attestations"] != "write" {
		t.Errorf("sign permissions = %v, want id-token and attestations write", sign.Permissions)
	}
	var signs, attests bool
	for _, s := range sign.Steps {
		if strings.Contains(s.Run, "cosign sign --yes") && strings.Contains(s.Run, "@$DIGEST") {
			signs = true
		}
		if strings.HasPrefix(s.Uses, "actions/attest-build-provenance@") && fmt.Sprint(s.With["push-to-registry"]) == "true" {
			attests = true
		}
		if s.Uses != "" && !regexp.MustCompile(`@[0-9a-f]{40}$`).MatchString(s.Uses) {
			t.Errorf("sign step %q uses %s, not pinned to a commit", s.Name, s.Uses)
		}
	}
	if !signs || !attests {
		t.Errorf("sign job: cosign sign by digest=%v, provenance pushed to registry=%v", signs, attests)
	}

	// Images are pushed only after every tag is checked, and the check
	// runs again right before the push.
	if !slices.Contains(needs(w.Jobs["images"].Needs), "preflight") {
		t.Error("images job does not need preflight")
	}
	var pre bool
	for _, s := range w.Jobs["preflight"].Steps {
		pre = pre || strings.Contains(s.Run, "hack/release-tag-absent.sh")
	}
	if !pre {
		t.Error("preflight does not run hack/release-tag-absent.sh")
	}
	checked, published := -1, -1
	for i, s := range w.Jobs["images"].Steps {
		if strings.Contains(s.Run, "hack/release-tag-absent.sh") && checked < 0 {
			checked = i
		}
		if strings.Contains(s.Run, "ko publish") {
			published = i
		}
	}
	if checked < 0 || published < 0 || checked > published {
		t.Errorf("images job: tag check at step %d, ko publish at step %d", checked, published)
	}

	// The chart is packaged with the pinned digests, after signing.
	chart := w.Jobs["chart"]
	if !slices.Contains(needs(chart.Needs), "sign") {
		t.Error("chart job does not wait for sign")
	}
	pinned, packaged := -1, -1
	for i, s := range chart.Steps {
		if strings.Contains(s.Run, "hack/set-image-digests.sh") {
			pinned = i
		}
		if strings.Contains(s.Run, "helm package") {
			packaged = i
		}
		if strings.HasPrefix(s.Uses, "helm/chart-releaser-action@") && fmt.Sprint(s.With["skip_packaging"]) != "true" {
			t.Error("chart-releaser repackages the chart and would drop the pinned digests")
		}
	}
	if pinned < 0 || packaged < 0 || pinned > packaged {
		t.Errorf("chart job: digests pinned at step %d, packaged at step %d", pinned, packaged)
	}
}

func TestBaseImageWorkflowDoesNotPersistToken(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "base-image.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string         `json:"uses"`
				With map[string]any `json:"with"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	for name, j := range w.Jobs {
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Uses, "actions/checkout@") && fmt.Sprint(s.With["persist-credentials"]) != "false" {
				t.Errorf("job %s: checkout leaves the write token in .git/config for the build step", name)
			}
		}
	}
}

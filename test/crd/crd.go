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

// Package crd loads the YipYapSource CRDs that ship with this repo: the Helm
// chart template and the static config/ manifest. Tests use it so they run
// against the same schema users install, not a hand-written copy.
package crd

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

const (
	// ChartPath is the chart CRD template, relative to the repo root.
	ChartPath = "chart/yipyap-source/templates/crd.yaml"
	// ConfigPath is the static install CRD, relative to the repo root.
	ConfigPath = "config/300-yipyapsource.yaml"
)

// allowedTemplateLines are the only Helm template actions the chart CRD may
// hold: the installCRD guard, its end, and the common labels include. None of
// them changes the schema, so Chart drops them. Any other action, even on a
// line of its own (a toYaml of values, say), is an error.
var allowedTemplateLines = []*regexp.Regexp{
	regexp.MustCompile(`^\{\{-? if \.Values\.installCRD -?\}\}$`),
	regexp.MustCompile(`^\{\{-? end -?\}\}$`),
	regexp.MustCompile(`^\{\{-? include "yipyap-source\.labels" \. \| nindent \d+ -?\}\}$`),
}

func allowedTemplateLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	for _, re := range allowedTemplateLines {
		if re.MatchString(trimmed) {
			return true
		}
	}
	return false
}

// RepoRoot returns the repository root, found from this file's location so
// callers work from any package directory.
func RepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate %s", "test/crd/crd.go")
	}
	return filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
}

// Chart returns the CRD from the Helm chart template. The installCRD guard and
// the common labels include are dropped; neither affects the schema. Any
// other template syntax is an error, so a template change that would alter
// the rendered CRD fails loudly here instead of being silently skipped.
func Chart() (*apiextensionsv1.CustomResourceDefinition, error) {
	raw, err := readRepoFile(ChartPath)
	if err != nil {
		return nil, err
	}
	rendered, err := stripTemplateLines(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ChartPath, err)
	}
	return decode(ChartPath, rendered)
}

// Config returns the CRD from the static config/ install path.
func Config() (*apiextensionsv1.CustomResourceDefinition, error) {
	raw, err := readRepoFile(ConfigPath)
	if err != nil {
		return nil, err
	}
	return decode(ConfigPath, raw)
}

func readRepoFile(rel string) ([]byte, error) {
	root, err := RepoRoot()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(root, rel))
}

func stripTemplateLines(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(raw))
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if allowedTemplateLine(line) {
			continue
		}
		if strings.Contains(line, "{{") {
			return nil, fmt.Errorf("line %d: unsupported template syntax %q", n, strings.TrimSpace(line))
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decode(name string, data []byte) (*apiextensionsv1.CustomResourceDefinition, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.UnmarshalStrict(data, crd); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if crd.Kind != "CustomResourceDefinition" {
		return nil, fmt.Errorf("%s: kind is %q, want CustomResourceDefinition", name, crd.Kind)
	}
	return crd, nil
}

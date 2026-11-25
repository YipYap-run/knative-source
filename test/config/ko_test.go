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

// Package config checks the static manifests under config/ that `ko apply`
// installs.
package config

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/YipYap-run/knative-source/test/crd"
)

const modulePath = "github.com/YipYap-run/knative-source"

var koRef = regexp.MustCompile(`ko://([^\s"']+)`)

// TestKoReferencesResolve checks that every ko:// image reference in config/
// names a main package in this module, so `ko resolve -f config/` can build
// it. The adapter image used to point at cmd/receive_adapter, which does not
// exist.
func TestKoReferencesResolve(t *testing.T) {
	root, err := crd.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "config", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"cmd/controller":      false,
		"cmd/webhook":         false,
		"cmd/receive-adapter": false,
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range koRef.FindAllStringSubmatch(string(data), -1) {
			ref := m[1]
			rel, ok := strings.CutPrefix(ref, modulePath+"/")
			if !ok {
				t.Errorf("%s: ko://%s is outside module %s", filepath.Base(f), ref, modulePath)
				continue
			}
			if !isMainPackage(t, filepath.Join(root, filepath.FromSlash(rel))) {
				t.Errorf("%s: ko://%s does not name a main package in this repo", filepath.Base(f), ref)
				continue
			}
			if _, tracked := want[rel]; tracked {
				want[rel] = true
			}
		}
	}
	for pkg, seen := range want {
		if !seen {
			t.Errorf("no ko:// reference to %s in config/", pkg)
		}
	}
}

func isMainPackage(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.PackageClauseOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if f.Name.Name == "main" {
			return true
		}
	}
	return false
}

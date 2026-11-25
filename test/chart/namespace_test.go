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
	"regexp"
	"testing"
)

var namespaceDoc = regexp.MustCompile(`(?m)^kind: Namespace$`)

// TestNamespaceRendering checks when the chart renders its Namespace without
// a cluster to ask (helm template, GitOps tools, and the render step of
// helm install, which runs before --create-namespace creates the release
// namespace).
//
// The chart must never render the release namespace itself. Helm stores the
// release in that namespace, so it has to exist before any chart resource is
// created; with --create-namespace Helm creates it after rendering and then
// fails with "namespaces ... already exists" when the chart also creates it.
func TestNamespaceRendering(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{
			name: "release namespace differs from namespace.name",
			args: []string{"--namespace", "default"},
			want: 1,
		},
		{
			name: "release namespace is namespace.name",
			args: []string{"--namespace", "yipyap-sources"},
			want: 0,
		},
		{
			name: "release namespace is a custom namespace.name",
			args: []string{"--namespace", "ops", "--set", "namespace.name=ops"},
			want: 0,
		},
		{
			name: "create=false",
			args: []string{"--namespace", "default", "--set", "namespace.create=false"},
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"-f", chartDir(t) + "/" + ciValues}, tc.args...)
			out, stderr, err := render(t, args...)
			if err != nil {
				t.Fatalf("helm template: %v\n%s", err, stderr)
			}
			if got := len(namespaceDoc.FindAllString(out, -1)); got != tc.want {
				t.Errorf("rendered %d Namespace objects, want %d", got, tc.want)
			}
		})
	}
}

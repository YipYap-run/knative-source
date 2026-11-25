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

package yipyapsource

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const eventTypesKey = "registry.knative.dev/eventTypes: |"

// eventTypesAnnotation returns the body of the eventTypes block scalar: the
// lines after the key that are indented deeper than the key itself.
func eventTypesAnnotation(data []byte) (string, bool) {
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != eventTypesKey {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		var body []string
		for _, l := range lines[i+1:] {
			if len(l)-len(strings.TrimLeft(l, " ")) <= indent {
				break
			}
			body = append(body, l)
		}
		return strings.Join(body, "\n"), true
	}
	return "", false
}

// TestCRDEventTypesAnnotation checks that every CRD copy advertises exactly
// the CloudEvent types the reconciler puts in status.ceAttributes.
func TestCRDEventTypesAnnotation(t *testing.T) {
	want := make([]string, 0, len(knownCloudEventAttributes))
	for _, a := range knownCloudEventAttributes {
		want = append(want, a.Type)
	}
	slices.Sort(want)

	root := filepath.Join("..", "..", "..")
	for _, rel := range []string{
		"config/300-yipyapsource.yaml",
		"chart/yipyap-source/templates/crd.yaml",
	} {
		t.Run(rel, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			body, ok := eventTypesAnnotation(data)
			if !ok {
				t.Fatal("registry.knative.dev/eventTypes annotation not found")
			}
			var entries []struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(body), &entries); err != nil {
				t.Fatalf("annotation is not valid JSON: %v", err)
			}
			got := make([]string, 0, len(entries))
			for _, e := range entries {
				got = append(got, e.Type)
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("eventTypes annotation = %v, want %v", got, want)
			}
		})
	}
}

// TestCRDReadyMessageColumn checks that every CRD copy prints the Ready
// message (for example "API key rejected (401)") in `kubectl get -o wide`.
func TestCRDReadyMessageColumn(t *testing.T) {
	const want = `        - name: Message
          type: string
          priority: 1
          jsonPath: ".status.conditions[?(@.type=='Ready')].message"
`
	root := filepath.Join("..", "..", "..")
	for _, rel := range []string{
		"config/300-yipyapsource.yaml",
		"chart/yipyap-source/templates/crd.yaml",
	} {
		t.Run(rel, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), want) {
				t.Errorf("no Message printer column for the Ready message")
			}
		})
	}
}

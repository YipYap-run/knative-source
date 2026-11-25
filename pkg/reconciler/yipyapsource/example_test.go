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
	"os"
	"path"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
)

func TestExampleFilterTypesMatchKnownEvents(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Spec struct {
			Filter *v1alpha1.YipYapSourceFilter `json:"filter"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Spec.Filter == nil || len(doc.Spec.Filter.Types) == 0 {
		t.Fatal("example.yaml has no spec.filter.types")
	}
	for _, glob := range doc.Spec.Filter.Types {
		matched := false
		for _, attr := range knownCloudEventAttributes {
			if ok, err := path.Match(glob, attr.Type); err == nil && ok {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("spec.filter.types entry %q matches no type the source emits", glob)
		}
	}
}

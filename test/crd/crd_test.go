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

package crd

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	crdvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

type loader struct {
	name string
	load func() (*apiextensionsv1.CustomResourceDefinition, error)
}

var loaders = []loader{
	{name: ChartPath, load: Chart},
	{name: ConfigPath, load: Config},
}

func mustLoad(t *testing.T, l loader) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	crd, err := l.load()
	if err != nil {
		t.Fatalf("load %s: %v", l.name, err)
	}
	return crd
}

// validateCRD runs the apiserver's create-time CRD validation, after the same
// defaulting the apiserver applies, and returns its errors.
func validateCRD(t *testing.T, in *apiextensionsv1.CustomResourceDefinition) field.ErrorList {
	t.Helper()
	crd := in.DeepCopy()
	apiextensionsv1.SetObjectDefaults_CustomResourceDefinition(crd)
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			crd.Status.StoredVersions = append(crd.Status.StoredVersions, v.Name)
		}
	}
	internal := &apiextensions.CustomResourceDefinition{}
	if err := apiextensionsv1.Convert_v1_CustomResourceDefinition_To_apiextensions_CustomResourceDefinition(crd, internal, nil); err != nil {
		t.Fatalf("convert: %v", err)
	}
	return crdvalidation.ValidateCustomResourceDefinition(context.Background(), internal)
}

// TestCRDsPassAPIServerValidation checks each shipped CRD against the rules
// the apiserver applies on create, including the structural schema rules.
// The static config/ CRD used to set additionalProperties next to properties,
// which the apiserver rejects.
func TestCRDsPassAPIServerValidation(t *testing.T) {
	for _, l := range loaders {
		t.Run(l.name, func(t *testing.T) {
			if errs := validateCRD(t, mustLoad(t, l)); len(errs) > 0 {
				t.Fatalf("apiserver would reject %s:\n%s", l.name, errs.ToAggregate())
			}
		})
	}
}

// TestCRDValidationCatchesAdditionalProperties proves the validation above
// is wired to the structural rules: a schema that sets additionalProperties
// next to properties must fail.
func TestCRDValidationCatchesAdditionalProperties(t *testing.T) {
	crd := mustLoad(t, loaders[0])
	spec := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	spec.AdditionalProperties = &apiextensionsv1.JSONSchemaPropsOrBool{Allows: false}
	crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"] = spec

	errs := validateCRD(t, crd)
	if len(errs) == 0 {
		t.Fatal("validation accepted additionalProperties next to properties")
	}
	if !strings.Contains(errs.ToAggregate().Error(), "additionalProperties and properties are mutual exclusive") {
		t.Fatalf("unexpected errors: %v", errs.ToAggregate())
	}
}

// TestCRDsMatch fails when the chart CRD and the static config/ CRD drift.
// Labels differ on purpose (the chart adds Helm labels, config/ adds a
// release label), so only the API-facing parts are compared.
func TestCRDsMatch(t *testing.T) {
	chart := mustLoad(t, loaders[0])
	config := mustLoad(t, loaders[1])

	if chart.Name != config.Name {
		t.Errorf("metadata.name: chart %q, config %q", chart.Name, config.Name)
	}
	const eventTypes = "registry.knative.dev/eventTypes"
	if diff := cmp.Diff(chart.Annotations[eventTypes], config.Annotations[eventTypes]); diff != "" {
		t.Errorf("%s annotation differs (-chart +config):\n%s", eventTypes, diff)
	}
	for _, label := range []string{"eventing.knative.dev/source", "knative.dev/crd-install"} {
		if chart.Labels[label] != config.Labels[label] {
			t.Errorf("label %s: chart %q, config %q", label, chart.Labels[label], config.Labels[label])
		}
	}
	// The spec holds the group, names, scope and every version with its
	// openAPIV3Schema, subresources and printer columns. Descriptions are
	// part of the schema and are compared too, since kubectl explain shows
	// them.
	if diff := cmp.Diff(chart.Spec, config.Spec); diff != "" {
		t.Errorf("CRD spec differs between %s and %s (-chart +config):\n%s", ChartPath, ConfigPath, diff)
	}
}

// TestModeEnum checks the mode enum in each shipped schema: the apiserver
// accepts auto, poll, stream and the empty default, and rejects anything else
// even when the admission webhook is down.
func TestModeEnum(t *testing.T) {
	for _, l := range loaders {
		t.Run(l.name, func(t *testing.T) {
			crd := mustLoad(t, l)
			v1Schema := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
			internal := &apiextensions.JSONSchemaProps{}
			if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v1Schema, internal, nil); err != nil {
				t.Fatalf("convert schema: %v", err)
			}
			validator, _, err := apiservervalidation.NewSchemaValidator(internal)
			if err != nil {
				t.Fatalf("schema validator: %v", err)
			}
			obj := func(mode string) map[string]interface{} {
				return map[string]interface{}{
					"apiVersion": "sources.yipyap.run/v1alpha1",
					"kind":       "YipYapSource",
					"metadata":   map[string]interface{}{"name": "s", "namespace": "ns"},
					"spec": map[string]interface{}{
						"apiKeyRef": map[string]interface{}{"name": "key"},
						"mode":      mode,
						"sink":      map[string]interface{}{"uri": "http://example.com"},
					},
				}
			}
			for _, mode := range []string{"", "auto", "poll", "stream"} {
				if errs := apiservervalidation.ValidateCustomResource(nil, obj(mode), validator); len(errs) > 0 {
					t.Errorf("mode %q rejected: %v", mode, errs.ToAggregate())
				}
			}
			for _, mode := range []string{"bogus", "Auto", "websocket"} {
				errs := apiservervalidation.ValidateCustomResource(nil, obj(mode), validator)
				if len(errs) == 0 {
					t.Errorf("mode %q accepted, want rejected", mode)
					continue
				}
				if !strings.Contains(errs.ToAggregate().Error(), "spec.mode") {
					t.Errorf("mode %q: error does not name spec.mode: %v", mode, errs.ToAggregate())
				}
			}
		})
	}
}

func TestStripTemplateLines(t *testing.T) {
	ok := "{{- if .Values.installCRD }}\nmetadata:\n  labels:\n    {{- include \"yipyap-source.labels\" . | nindent 4 }}\n    a: b\n{{- end }}\n"
	got, err := stripTemplateLines([]byte(ok))
	if err != nil {
		t.Fatalf("allowed actions rejected: %v", err)
	}
	if want := "metadata:\n  labels:\n    a: b\n"; string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	for _, in := range []string{
		"a: b\nc: {{ .Values.x }}\n",
		"spec:\n  {{- toYaml .Values.crdSpec | nindent 2 }}\n",
		"{{- if .Values.somethingElse }}\na: b\n{{- end }}\n",
	} {
		if _, err := stripTemplateLines([]byte(in)); err == nil {
			t.Errorf("template %q accepted, want error", in)
		}
	}
}

// TestChartMatchesHelmTemplate checks that Chart, which strips the template
// actions in process, yields the same CRD as helm template. It is skipped
// when helm is not installed.
func TestChartMatchesHelmTemplate(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed")
	}
	root, err := RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	chartDir := filepath.Join(root, "chart", "yipyap-source")
	out, err := exec.Command(helm, "template", "crdcheck", chartDir,
		"-f", filepath.Join(chartDir, "ci", "apiserver-cidrs-values.yaml"),
		"--show-only", "templates/crd.yaml").Output()
	if err != nil {
		t.Fatalf("helm template: %v", err)
	}
	rendered, err := decode("helm template", out)
	if err != nil {
		t.Fatal(err)
	}
	stripped, err := Chart()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(rendered.Spec, stripped.Spec); diff != "" {
		t.Errorf("Chart() spec differs from helm template (-helm +Chart):\n%s", diff)
	}
	if diff := cmp.Diff(rendered.Annotations, stripped.Annotations); diff != "" {
		t.Errorf("Chart() annotations differ from helm template (-helm +Chart):\n%s", diff)
	}
}

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

package source

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	"github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/resources"
)

// TestLoadConfig_AcceptsControllerRenderedEnv renders the adapter Deployment
// for a source with only the required fields set (so every default applies)
// and feeds the resulting container env into LoadConfig. This keeps the
// controller and the adapter in agreement on every value the controller can
// emit, including the default spec.mode of "auto".
func TestLoadConfig_AcceptsControllerRenderedEnv(t *testing.T) {
	sink, err := apis.ParseURL("http://broker.default.svc/broker")
	if err != nil {
		t.Fatal(err)
	}
	src := &v1alpha1.YipYapSource{
		ObjectMeta: metav1.ObjectMeta{Name: "ys", Namespace: "ns", UID: types.UID("uid-1")},
		Spec: v1alpha1.YipYapSourceSpec{
			SourceSpec: duckv1.SourceSpec{Sink: duckv1.Destination{URI: sink}},
			APIKeyRef:  v1alpha1.APIKeyReference{Name: "yipyap-api-key"},
		},
	}
	src.SetDefaults(context.Background())
	if src.Spec.Mode != v1alpha1.YipYapSourceModeAuto {
		t.Fatalf("precondition: defaulted Mode = %q, want auto", src.Spec.Mode)
	}

	dep := resources.MakeAdapterDeployment(src, sink, "registry.test/adapter:unit")
	env := map[string]string{}
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name != resources.ContainerName {
			continue
		}
		for _, e := range c.Env {
			if e.ValueFrom != nil {
				// Secret-backed values are resolved by the kubelet; stand in
				// a placeholder so LoadConfig sees a non-empty value.
				env[e.Name] = "placeholder"
				continue
			}
			env[e.Name] = e.Value
		}
	}
	if _, ok := env["YIPYAP_MODE"]; !ok {
		t.Fatal("rendered env has no YIPYAP_MODE")
	}

	cfg, err := LoadConfig(fakeEnv(env))
	if err != nil {
		t.Fatalf("LoadConfig rejected controller-rendered env: %v", err)
	}
	if cfg.Mode != "auto" {
		t.Errorf("Mode = %q, want auto", cfg.Mode)
	}
}

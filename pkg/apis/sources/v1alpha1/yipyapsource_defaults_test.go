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

package v1alpha1

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	duckv1 "knative.dev/pkg/apis/duck/v1"
)

func baseValidSpec() YipYapSourceSpec {
	return YipYapSourceSpec{
		APIKeyRef: APIKeyReference{Name: "yipyap-credentials"},
		SourceSpec: duckv1.SourceSpec{
			Sink: duckv1.Destination{
				Ref: &duckv1.KReference{
					APIVersion: "eventing.knative.dev/v1",
					Kind:       "Broker",
					Name:       "default",
				},
			},
		},
	}
}

func TestYipYapSource_SetDefaults_EmptyKey(t *testing.T) {
	s := &YipYapSource{
		Spec: YipYapSourceSpec{
			APIKeyRef: APIKeyReference{Name: "creds"}, // Key empty
		},
	}
	s.SetDefaults(context.Background())
	if got, want := s.Spec.APIKeyRef.Key, "api-key"; got != want {
		t.Errorf("APIKeyRef.Key = %q, want %q", got, want)
	}
}

func TestYipYapSource_SetDefaults_ExplicitKeyPreserved(t *testing.T) {
	s := &YipYapSource{
		Spec: YipYapSourceSpec{
			APIKeyRef: APIKeyReference{Name: "creds", Key: "my-custom-key"},
		},
	}
	s.SetDefaults(context.Background())
	if got, want := s.Spec.APIKeyRef.Key, "my-custom-key"; got != want {
		t.Errorf("APIKeyRef.Key = %q, want %q (should preserve explicit value)", got, want)
	}
}

func TestYipYapSource_SetDefaults_EmptyBaseURL(t *testing.T) {
	s := &YipYapSource{Spec: YipYapSourceSpec{APIKeyRef: APIKeyReference{Name: "creds"}}}
	s.SetDefaults(context.Background())
	if got, want := s.Spec.BaseURL, "https://console.yipyap.run"; got != want {
		t.Errorf("BaseURL = %q, want %q", got, want)
	}
}

func TestYipYapSource_SetDefaults_ExplicitBaseURLPreserved(t *testing.T) {
	s := &YipYapSource{
		Spec: YipYapSourceSpec{
			APIKeyRef: APIKeyReference{Name: "creds"},
			BaseURL:   "https://my.self-hosted.yipyap.example",
		},
	}
	s.SetDefaults(context.Background())
	if got, want := s.Spec.BaseURL, "https://my.self-hosted.yipyap.example"; got != want {
		t.Errorf("BaseURL = %q, want %q (should preserve explicit value)", got, want)
	}
}

func TestYipYapSource_SetDefaults_EmptyMode(t *testing.T) {
	s := &YipYapSource{Spec: YipYapSourceSpec{APIKeyRef: APIKeyReference{Name: "creds"}}}
	s.SetDefaults(context.Background())
	if got, want := s.Spec.Mode, YipYapSourceModeAuto; got != want {
		t.Errorf("Mode = %q, want %q", got, want)
	}
}

func TestYipYapSource_SetDefaults_ExplicitModePreserved(t *testing.T) {
	s := &YipYapSource{
		Spec: YipYapSourceSpec{
			APIKeyRef: APIKeyReference{Name: "creds"},
			Mode:      YipYapSourceModeStream,
		},
	}
	s.SetDefaults(context.Background())
	if got, want := s.Spec.Mode, YipYapSourceModeStream; got != want {
		t.Errorf("Mode = %q, want %q (should preserve explicit value)", got, want)
	}
}

func TestYipYapSource_SetDefaults_Idempotent(t *testing.T) {
	s := &YipYapSource{
		ObjectMeta: metav1.ObjectMeta{Namespace: "observability"},
		Spec:       baseValidSpec(),
	}
	s.SetDefaults(context.Background())
	first := s.DeepCopy()
	s.SetDefaults(context.Background())
	if diff := cmp.Diff(first, s); diff != "" {
		t.Errorf("SetDefaults not idempotent (-first, +second):\n%s", diff)
	}
}

func TestYipYapSource_SetDefaults_SinkNamespaceFromParent(t *testing.T) {
	s := &YipYapSource{
		ObjectMeta: metav1.ObjectMeta{Namespace: "parent-ns"},
		Spec: YipYapSourceSpec{
			APIKeyRef: APIKeyReference{Name: "creds"},
			SourceSpec: duckv1.SourceSpec{
				Sink: duckv1.Destination{Ref: &duckv1.KReference{
					APIVersion: "eventing.knative.dev/v1",
					Kind:       "Broker",
					Name:       "default",
					// Namespace omitted — should inherit from parent.
				}},
			},
		},
	}
	s.SetDefaults(context.Background())
	if got, want := s.Spec.Sink.Ref.Namespace, "parent-ns"; got != want {
		t.Errorf("Sink.Ref.Namespace = %q, want %q (should inherit from parent)", got, want)
	}
}

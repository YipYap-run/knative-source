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
	"strings"
	"testing"

	duckv1 "knative.dev/pkg/apis/duck/v1"
)

func validYipYapSource() *YipYapSource {
	return &YipYapSource{
		Spec: YipYapSourceSpec{
			APIKeyRef: APIKeyReference{Name: "creds", Key: "api-key"},
			BaseURL:   "https://console.yipyap.run",
			Mode:      YipYapSourceModeAuto,
			SourceSpec: duckv1.SourceSpec{
				Sink: duckv1.Destination{Ref: &duckv1.KReference{
					APIVersion: "eventing.knative.dev/v1",
					Kind:       "Broker",
					Name:       "default",
					Namespace:  "observability",
				}},
			},
		},
	}
}

func TestYipYapSource_Validate_MissingAPIKeyRefName(t *testing.T) {
	s := validYipYapSource()
	s.Spec.APIKeyRef.Name = ""
	err := s.Validate(context.Background())
	if err == nil {
		t.Fatalf("expected validation error for missing apiKeyRef.name, got nil")
	}
	if !strings.Contains(err.Error(), "apiKeyRef.name") {
		t.Errorf("expected error mentioning apiKeyRef.name, got: %v", err)
	}
}

func TestYipYapSource_Validate_InvalidMode(t *testing.T) {
	s := validYipYapSource()
	s.Spec.Mode = YipYapSourceMode("sideways")
	err := s.Validate(context.Background())
	if err == nil {
		t.Fatalf("expected validation error for invalid mode, got nil")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Errorf("expected error mentioning mode, got: %v", err)
	}
}

func TestYipYapSource_Validate_InvalidSeverity(t *testing.T) {
	s := validYipYapSource()
	s.Spec.Filter = &YipYapSourceFilter{
		Severities: []string{"warning", "apocalypse"},
	}
	err := s.Validate(context.Background())
	if err == nil {
		t.Fatalf("expected validation error for invalid severity, got nil")
	}
	if !strings.Contains(err.Error(), "severities") {
		t.Errorf("expected error mentioning severities, got: %v", err)
	}
}

func TestYipYapSource_Validate_InvalidFilterGlob(t *testing.T) {
	s := validYipYapSource()
	// path.Match treats `[` with no closing bracket as a syntax error.
	s.Spec.Filter = &YipYapSourceFilter{Types: []string{"run.yipyap.alert.["}}
	err := s.Validate(context.Background())
	if err == nil {
		t.Fatalf("expected validation error for invalid filter glob, got nil")
	}
	if !strings.Contains(err.Error(), "filter.types") {
		t.Errorf("expected error mentioning filter.types, got: %v", err)
	}
}

func TestYipYapSource_Validate_ValidMinimal(t *testing.T) {
	s := validYipYapSource()
	if err := s.Validate(context.Background()); err != nil {
		t.Errorf("expected no error for minimal valid spec, got: %v", err)
	}
}

func TestYipYapSource_Validate_ValidFull(t *testing.T) {
	s := validYipYapSource()
	s.Spec.Filter = &YipYapSourceFilter{
		Types:      []string{"run.yipyap.alert.*", "run.yipyap.monitor.state"},
		MonitorIDs: []string{"mon-1", "mon-2"},
		Severities: []string{"info", "warning", "critical"},
	}
	s.Spec.Mode = YipYapSourceModeStream
	s.Spec.ServiceAccountName = "yipyap-source"
	s.Spec.CloudEventOverrides = &duckv1.CloudEventOverrides{
		Extensions: map[string]string{"environment": "prod"},
	}
	if err := s.Validate(context.Background()); err != nil {
		t.Errorf("expected no error for full valid spec, got: %v", err)
	}
}

func TestYipYapSource_Validate_MissingSink(t *testing.T) {
	s := validYipYapSource()
	s.Spec.Sink = duckv1.Destination{}
	err := s.Validate(context.Background())
	if err == nil {
		t.Fatalf("expected validation error for missing sink, got nil")
	}
	if !strings.Contains(err.Error(), "sink") {
		t.Errorf("expected error mentioning sink, got: %v", err)
	}
}

func TestYipYapSource_Validate_EmptyModeIsValid(t *testing.T) {
	// Empty mode is allowed pre-defaulting.
	s := validYipYapSource()
	s.Spec.Mode = ""
	if err := s.Validate(context.Background()); err != nil {
		t.Errorf("expected no error for empty mode (defaulted later), got: %v", err)
	}
}

func TestYipYapSource_Validate_AllValidModes(t *testing.T) {
	for _, m := range []YipYapSourceMode{YipYapSourceModeAuto, YipYapSourceModePoll, YipYapSourceModeStream} {
		t.Run(string(m), func(t *testing.T) {
			s := validYipYapSource()
			s.Spec.Mode = m
			if err := s.Validate(context.Background()); err != nil {
				t.Errorf("expected no error for mode %q, got: %v", m, err)
			}
		})
	}
}

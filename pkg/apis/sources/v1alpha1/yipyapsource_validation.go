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
	"fmt"
	"path"

	"knative.dev/pkg/apis"
)

// validSeverities is the canonical set of alert severities accepted by
// YipYapSourceFilter.Severities. Keep in sync with the CloudEvent catalog.
var validSeverities = map[string]struct{}{
	"info":     {},
	"warning":  {},
	"critical": {},
}

// Validate validates a YipYapSource.
func (y *YipYapSource) Validate(ctx context.Context) *apis.FieldError {
	return y.Spec.Validate(ctx).ViaField("spec")
}

// Validate validates a YipYapSourceSpec.
func (s *YipYapSourceSpec) Validate(ctx context.Context) *apis.FieldError {
	var errs *apis.FieldError

	if s.APIKeyRef.Name == "" {
		errs = errs.Also(apis.ErrMissingField("apiKeyRef.name"))
	}

	switch s.Mode {
	case "", YipYapSourceModeAuto, YipYapSourceModePoll, YipYapSourceModeStream:
		// ok (empty means "default later").
	default:
		errs = errs.Also(apis.ErrInvalidValue(string(s.Mode), "mode"))
	}

	if s.Filter != nil {
		errs = errs.Also(s.Filter.Validate(ctx).ViaField("filter"))
	}

	// Validate the sink via the embedded duck type.
	errs = errs.Also(s.Sink.Validate(ctx).ViaField("sink"))

	return errs
}

// Validate validates a YipYapSourceFilter.
func (f *YipYapSourceFilter) Validate(_ context.Context) *apis.FieldError {
	var errs *apis.FieldError

	for i, pat := range f.Types {
		if _, err := path.Match(pat, ""); err != nil {
			errs = errs.Also(&apis.FieldError{
				Message: fmt.Sprintf("invalid glob: %v", err),
				Paths:   []string{fmt.Sprintf("types[%d]", i)},
			})
		}
	}

	for i, sev := range f.Severities {
		if _, ok := validSeverities[sev]; !ok {
			errs = errs.Also(apis.ErrInvalidValue(sev, fmt.Sprintf("severities[%d]", i)))
		}
	}

	return errs
}

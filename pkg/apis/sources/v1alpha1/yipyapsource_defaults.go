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

	"knative.dev/pkg/apis"
)

// Default values applied by SetDefaults when the corresponding fields are unset.
const (
	// DefaultAPIKeySecretKey is the default Secret key that carries the API token.
	DefaultAPIKeySecretKey = "api-key"

	// DefaultBaseURL is the hosted YipYap console endpoint. Override for
	// self-hosted FOSS deployments.
	DefaultBaseURL = "https://console.yipyap.run"
)

// SetDefaults applies API defaults to the YipYapSource.
func (y *YipYapSource) SetDefaults(ctx context.Context) {
	if y == nil {
		return
	}
	// Scope sink reference resolution to the YipYapSource's own namespace
	// so that a missing Destination.Ref.Namespace is filled in as expected.
	withNS := apis.WithinParent(ctx, y.ObjectMeta)
	y.Spec.SetDefaults(withNS)
}

// SetDefaults applies API defaults to the YipYapSourceSpec.
func (s *YipYapSourceSpec) SetDefaults(ctx context.Context) {
	if s.APIKeyRef.Key == "" {
		s.APIKeyRef.Key = DefaultAPIKeySecretKey
	}
	if s.BaseURL == "" {
		s.BaseURL = DefaultBaseURL
	}
	if s.Mode == "" {
		s.Mode = YipYapSourceModeAuto
	}

	// Let the embedded Destination default its namespace from the parent.
	s.Sink.SetDefaults(ctx)
}

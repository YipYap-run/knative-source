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

package main

import (
	"context"
	"strings"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	"github.com/YipYap-run/knative-source/pkg/baseurl"
)

type denyAll struct{ calls int }

func (d *denyAll) CanGetSecret(context.Context, *authenticationv1.UserInfo, string, string) (bool, error) {
	d.calls++
	return false, nil
}

// TestValidationContextWiresPolicyAndChecker checks that the infuser the
// validating webhook uses makes Validate apply the configured allowlist and
// the Secret access check.
func TestValidationContextWiresPolicyAndChecker(t *testing.T) {
	policy, err := baseurl.Parse("yipyap.corp.example")
	if err != nil {
		t.Fatal(err)
	}
	checker := &denyAll{}
	ctx := validationContext(policy, checker)(context.Background())
	ctx = apis.WithinCreate(apis.WithUserInfo(ctx, &authenticationv1.UserInfo{Username: "alice"}))

	src := &v1alpha1.YipYapSource{}
	src.Namespace = "team-a"
	src.Spec.APIKeyRef = v1alpha1.APIKeyReference{Name: "creds", Key: "api-key"}
	src.Spec.BaseURL = "https://console.yipyap.run"
	src.Spec.Sink = duckv1.Destination{Ref: &duckv1.KReference{
		APIVersion: "eventing.knative.dev/v1", Kind: "Broker", Name: "default", Namespace: "team-a",
	}}

	verr := src.Validate(ctx)
	if verr == nil {
		t.Fatal("expected errors, got nil")
	}
	msg := verr.Error()
	if !strings.Contains(msg, "spec.baseURL") || !strings.Contains(msg, "yipyap.corp.example") {
		t.Errorf("configured allowlist not applied: %v", msg)
	}
	if !strings.Contains(msg, "you are not allowed to read Secret creds in team-a") || checker.calls != 1 {
		t.Errorf("Secret access check not applied (calls=%d): %v", checker.calls, msg)
	}
}

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
	"errors"
	"strings"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clientgotesting "k8s.io/client-go/testing"
	"knative.dev/pkg/apis"

	"github.com/YipYap-run/knative-source/pkg/baseurl"
	"github.com/YipYap-run/knative-source/pkg/secretaccess"
)

const testNS = "team-a"

var alice = &authenticationv1.UserInfo{
	Username: "alice",
	UID:      "uid-alice",
	Groups:   []string{"team-a-devs", "system:authenticated"},
	Extra:    map[string]authenticationv1.ExtraValue{"tenant": {"a"}},
}

// sarClient is a fake kube client whose SubjectAccessReview reactor allows
// only get on the Secrets in readable, and counts reviews.
type sarClient struct {
	*fake.Clientset
	reviews []authorizationv1.SubjectAccessReviewSpec
}

func newSARClient(readable ...string) *sarClient {
	c := &sarClient{Clientset: fake.NewSimpleClientset()}
	c.PrependReactor("create", "subjectaccessreviews", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		sar := action.(clientgotesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview).DeepCopy()
		c.reviews = append(c.reviews, sar.Spec)
		ra := sar.Spec.ResourceAttributes
		for _, name := range readable {
			if ra != nil && ra.Verb == "get" && ra.Resource == "secrets" && ra.Namespace == testNS && ra.Name == name {
				sar.Status.Allowed = true
			}
		}
		return true, sar, nil
	})
	return c
}

func admissionCtx(client *sarClient, user *authenticationv1.UserInfo) context.Context {
	ctx := apis.WithUserInfo(context.Background(), user)
	return WithSecretReadChecker(ctx, secretaccess.SARChecker{Client: client})
}

func namespacedSource() *YipYapSource {
	s := validYipYapSource()
	s.Namespace = testNS
	return s
}

func wantFieldError(t *testing.T, err *apis.FieldError, field, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error on %s containing %q, got nil", field, msg)
	}
	if !strings.Contains(err.Error(), field) || !strings.Contains(err.Error(), msg) {
		t.Fatalf("expected an error on %s containing %q, got: %v", field, msg, err)
	}
}

func TestValidate_BaseURLPolicy(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr string
	}{
		{"https default host accepted", "https://console.yipyap.run", ""},
		{"http rejected", "http://console.yipyap.run", "must use https"},
		{"userinfo rejected", "https://key:x@console.yipyap.run", "user info"},
		{"unlisted host rejected", "https://exfil.example.net", "not allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validYipYapSource()
			s.Spec.BaseURL = tc.url
			err := s.Validate(apis.WithinCreate(context.Background()))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			wantFieldError(t, err, "spec.baseURL", tc.wantErr)
		})
	}
}

func TestValidate_BaseURLPolicyFromContext(t *testing.T) {
	p, err := baseurl.Parse("yipyap.corp.example:8443")
	if err != nil {
		t.Fatal(err)
	}
	ctx := baseurl.WithPolicy(apis.WithinCreate(context.Background()), p)

	s := validYipYapSource()
	s.Spec.BaseURL = "https://yipyap.corp.example:8443"
	if err := s.Validate(ctx); err != nil {
		t.Fatalf("listed host rejected: %v", err)
	}

	s.Spec.BaseURL = "https://console.yipyap.run"
	wantFieldError(t, s.Validate(ctx), "spec.baseURL", "not allowed")
}

func TestValidate_UpdateKeepsUnchangedLegacyBaseURL(t *testing.T) {
	old := namespacedSource()
	old.Spec.BaseURL = "http://legacy.example"
	updated := old.DeepCopy()
	updated.Finalizers = []string{"yipyapsources.sources.yipyap.run"}

	client := newSARClient() // nothing readable
	ctx := apis.WithinUpdate(admissionCtx(client, &authenticationv1.UserInfo{Username: "system:serviceaccount:yipyap-sources:controller"}), old)
	if err := updated.Validate(ctx); err != nil {
		t.Fatalf("an update that leaves baseURL and apiKeyRef alone must pass: %v", err)
	}
	if len(client.reviews) != 0 {
		t.Fatalf("no SubjectAccessReview expected, got %d", len(client.reviews))
	}

	updated.Spec.BaseURL = "http://other.example"
	wantFieldError(t, updated.Validate(ctx), "spec.baseURL", "must use https")
}

func TestValidate_SecretAccessDeniedOnCreate(t *testing.T) {
	client := newSARClient() // alice cannot read anything
	s := namespacedSource()
	err := s.Validate(apis.WithinCreate(admissionCtx(client, alice)))
	wantFieldError(t, err, "spec.apiKeyRef.name", "you are not allowed to read Secret creds in team-a")

	if len(client.reviews) != 1 {
		t.Fatalf("want 1 SubjectAccessReview, got %d", len(client.reviews))
	}
	got := client.reviews[0]
	if got.User != "alice" || got.UID != "uid-alice" || len(got.Groups) != 2 || got.Extra["tenant"][0] != "a" {
		t.Errorf("review identity does not match the admission user: %+v", got)
	}
}

func TestValidate_SecretAccessAllowedOnCreate(t *testing.T) {
	client := newSARClient("creds")
	s := namespacedSource()
	if err := s.Validate(apis.WithinCreate(admissionCtx(client, alice))); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.reviews) != 1 {
		t.Fatalf("want 1 SubjectAccessReview, got %d", len(client.reviews))
	}
}

func TestValidate_SecretAccessOnUpdate(t *testing.T) {
	old := namespacedSource()
	client := newSARClient("creds")
	ctx := apis.WithinUpdate(admissionCtx(client, alice), old)

	// Changing an unrelated field does not trigger a review.
	same := old.DeepCopy()
	same.Spec.Mode = YipYapSourceModePoll
	if err := same.Validate(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.reviews) != 0 {
		t.Fatalf("no review expected for an unrelated change, got %d", len(client.reviews))
	}

	// Pointing apiKeyRef at a Secret alice cannot read is denied.
	other := old.DeepCopy()
	other.Spec.APIKeyRef.Name = "db-password"
	wantFieldError(t, other.Validate(ctx), "spec.apiKeyRef.name", "you are not allowed to read Secret db-password in team-a")

	// Changing baseURL re-checks the Secret too.
	moved := old.DeepCopy()
	moved.Spec.BaseURL = "https://console.yipyap.run:443"
	if err := moved.Validate(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.reviews) != 2 {
		t.Fatalf("want 2 reviews (apiKeyRef change, baseURL change), got %d", len(client.reviews))
	}
}

func TestValidate_StatusUpdateSkipsAdmissionChecks(t *testing.T) {
	old := namespacedSource()
	old.Spec.BaseURL = "http://legacy.example"
	client := newSARClient()
	ctx := apis.WithinSubResourceUpdate(admissionCtx(client, alice), old, "status")
	s := old.DeepCopy()
	s.Spec.APIKeyRef.Name = "other" // ignored on a status write by the API server anyway
	if err := s.Validate(ctx); err != nil {
		t.Fatalf("status update must not be blocked: %v", err)
	}
	if len(client.reviews) != 0 {
		t.Fatalf("no review expected for a status update, got %d", len(client.reviews))
	}
}

type errChecker struct{}

func (errChecker) CanGetSecret(context.Context, *authenticationv1.UserInfo, string, string) (bool, error) {
	return false, errors.New("apiserver unavailable")
}

func TestValidate_SecretAccessFailsClosed(t *testing.T) {
	s := namespacedSource()

	ctx := WithSecretReadChecker(apis.WithUserInfo(context.Background(), alice), errChecker{})
	wantFieldError(t, s.Validate(apis.WithinCreate(ctx)), "spec.apiKeyRef.name", "could not check")

	noUser := WithSecretReadChecker(context.Background(), errChecker{})
	wantFieldError(t, s.Validate(apis.WithinCreate(noUser)), "spec.apiKeyRef.name", "no user")

	noNS := validYipYapSource()
	wantFieldError(t, noNS.Validate(apis.WithinCreate(ctx)), "spec.apiKeyRef.name", "no namespace")
}

// A source stored without defaults (for example while the defaulting webhook
// was unavailable) gets baseURL and apiKeyRef.key filled in on its next
// update. That is not a change in where the key goes, so it must not need a
// review or block a user without Secret access.
func TestValidate_DefaultingAloneIsNotAChange(t *testing.T) {
	old := namespacedSource()
	old.Spec.BaseURL = ""
	old.Spec.APIKeyRef.Key = ""
	updated := old.DeepCopy()
	updated.SetDefaults(context.Background())
	updated.Labels = map[string]string{"team": "a"}

	client := newSARClient() // nothing readable
	ctx := apis.WithinUpdate(admissionCtx(client, alice), old)
	if err := updated.Validate(ctx); err != nil {
		t.Fatalf("filling defaults must not be treated as a change: %v", err)
	}
	if len(client.reviews) != 0 {
		t.Fatalf("no SubjectAccessReview expected, got %d", len(client.reviews))
	}

	// An explicit key change still triggers a review.
	updated.Spec.APIKeyRef.Key = "token"
	wantFieldError(t, updated.Validate(ctx), "spec.apiKeyRef.name", "you are not allowed to read Secret creds in team-a")
}

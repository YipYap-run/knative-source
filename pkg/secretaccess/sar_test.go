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

package secretaccess

import (
	"context"
	"errors"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clientgotesting "k8s.io/client-go/testing"
)

// reviewReactor answers SubjectAccessReviews with allowed and records the
// last review it saw.
func reviewReactor(allowed bool, seen **authorizationv1.SubjectAccessReview) clientgotesting.ReactionFunc {
	return func(action clientgotesting.Action) (bool, runtime.Object, error) {
		sar := action.(clientgotesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview).DeepCopy()
		*seen = sar
		sar.Status.Allowed = allowed
		return true, sar, nil
	}
}

func TestCanGetSecretSendsFullIdentity(t *testing.T) {
	client := fake.NewSimpleClientset()
	var seen *authorizationv1.SubjectAccessReview
	client.PrependReactor("create", "subjectaccessreviews", reviewReactor(true, &seen))

	user := &authenticationv1.UserInfo{
		Username: "alice",
		UID:      "uid-1",
		Groups:   []string{"team-a", "system:authenticated"},
		Extra:    map[string]authenticationv1.ExtraValue{"scopes": {"x"}},
	}
	ok, err := SARChecker{Client: client}.CanGetSecret(context.Background(), user, "ns1", "creds")
	if err != nil || !ok {
		t.Fatalf("CanGetSecret = %v, %v; want true, nil", ok, err)
	}
	spec := seen.Spec
	ra := spec.ResourceAttributes
	if spec.User != "alice" || spec.UID != "uid-1" || len(spec.Groups) != 2 || spec.Extra["scopes"][0] != "x" {
		t.Errorf("review identity = %+v", spec)
	}
	if ra == nil || ra.Verb != "get" || ra.Resource != "secrets" || ra.Group != "" || ra.Namespace != "ns1" || ra.Name != "creds" {
		t.Errorf("review resource = %+v", ra)
	}
}

func TestCanGetSecretDenied(t *testing.T) {
	client := fake.NewSimpleClientset()
	var seen *authorizationv1.SubjectAccessReview
	client.PrependReactor("create", "subjectaccessreviews", reviewReactor(false, &seen))
	ok, err := SARChecker{Client: client}.CanGetSecret(context.Background(),
		&authenticationv1.UserInfo{Username: "bob"}, "ns1", "creds")
	if err != nil || ok {
		t.Fatalf("CanGetSecret = %v, %v; want false, nil", ok, err)
	}
}

func TestCanGetSecretError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "subjectaccessreviews", func(clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("boom")
	})
	ok, err := SARChecker{Client: client}.CanGetSecret(context.Background(),
		&authenticationv1.UserInfo{Username: "bob"}, "ns1", "creds")
	if err == nil || ok {
		t.Fatalf("CanGetSecret = %v, %v; want false and an error", ok, err)
	}
}

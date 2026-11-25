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

// Package secretaccess asks the API server, through a SubjectAccessReview,
// whether an admission request's user may get a Secret.
package secretaccess

import (
	"context"
	"fmt"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// SARChecker implements v1alpha1.SecretReadChecker with a
// SubjectAccessReview. The webhook service account needs create on
// subjectaccessreviews.authorization.k8s.io.
type SARChecker struct {
	Client kubernetes.Interface
}

// CanGetSecret reports whether user may get secrets/<name> in namespace.
// The review carries the full admission identity (user, groups, UID and
// extra) so group and impersonation based grants are evaluated as the API
// server would evaluate them for the user.
func (c SARChecker) CanGetSecret(ctx context.Context, user *authenticationv1.UserInfo, namespace, name string) (bool, error) {
	extra := make(map[string]authorizationv1.ExtraValue, len(user.Extra))
	for k, v := range user.Extra {
		extra[k] = authorizationv1.ExtraValue(v)
	}
	review := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   user.Username,
			Groups: user.Groups,
			UID:    user.UID,
			Extra:  extra,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: namespace,
				Verb:      "get",
				Group:     "",
				Version:   "v1",
				Resource:  "secrets",
				Name:      name,
			},
		},
	}
	out, err := c.Client.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("SubjectAccessReview: %w", err)
	}
	return out.Status.Allowed && !out.Status.Denied, nil
}

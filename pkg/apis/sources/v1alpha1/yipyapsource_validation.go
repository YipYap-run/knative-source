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
	"net/url"
	"path"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	"knative.dev/pkg/apis"

	"github.com/YipYap-run/knative-source/pkg/baseurl"
)

// validSeverities is the canonical set of alert severities accepted by
// YipYapSourceFilter.Severities. Keep in sync with the CloudEvent catalog.
var validSeverities = map[string]struct{}{
	"info":     {},
	"warning":  {},
	"critical": {},
}

// maxFilterQueryBytes caps the encoded size of the filter query parameters
// the adapter sends on every poll and stream request (filter=, severity=,
// monitor_id=). Common proxies reject request lines over 8 KiB with 414, and
// the path, since= cursor and headers need the rest. Without this cap a
// long monitorIDs list passes the CRD schema but makes every request fail.
const maxFilterQueryBytes = 6144

// SecretReadChecker answers whether a user may get a Secret. The webhook
// injects one backed by a SubjectAccessReview; see WithSecretReadChecker.
type SecretReadChecker interface {
	CanGetSecret(ctx context.Context, user *authenticationv1.UserInfo, namespace, name string) (bool, error)
}

type secretReadCheckerKey struct{}

// WithSecretReadChecker returns a context whose Validate calls check that
// the requesting user may read the Secret named by spec.apiKeyRef.
func WithSecretReadChecker(ctx context.Context, c SecretReadChecker) context.Context {
	return context.WithValue(ctx, secretReadCheckerKey{}, c)
}

func secretReadCheckerFrom(ctx context.Context) SecretReadChecker {
	c, _ := ctx.Value(secretReadCheckerKey{}).(SecretReadChecker)
	return c
}

// Validate validates a YipYapSource.
//
// Beyond the shape checks in Spec.Validate, admission enforces two rules
// that stop a user from sending a Secret value somewhere they choose:
//
//   - spec.baseURL must pass the operator policy (https, no user info, host
//     on the allowlist). The policy comes from baseurl.FromContext.
//   - the requesting user must be allowed to get the Secret named by
//     spec.apiKeyRef, because the adapter sends its value to spec.baseURL.
//     This runs only when a SecretReadChecker is in the context.
//
// Both run on create, and on update only when the fields they guard change.
// Unchanged sources stay writable (finalizer removal, sink edits) even if
// they predate the policy; the controller keeps those not ready instead.
// Status updates skip both checks.
func (y *YipYapSource) Validate(ctx context.Context) *apis.FieldError {
	errs := y.Spec.Validate(ctx).ViaField("spec")

	if apis.GetUpdatedSubresource(ctx) != "" {
		return errs
	}
	var old *YipYapSource
	if apis.IsInUpdate(ctx) {
		old, _ = apis.GetBaseline(ctx).(*YipYapSource)
	}

	// Compare effective values: the defaulting webhook fills an empty
	// baseURL or apiKeyRef.key on every update, and that alone does not send
	// the key anywhere new, so it must not trigger a review.
	baseURLChanged := old == nil || effectiveBaseURL(old.Spec.BaseURL) != effectiveBaseURL(y.Spec.BaseURL)
	keyRefChanged := old == nil || effectiveKeyRef(old.Spec.APIKeyRef) != effectiveKeyRef(y.Spec.APIKeyRef)

	if baseURLChanged {
		if err := baseurl.FromContext(ctx).Check(y.Spec.BaseURL); err != nil {
			errs = errs.Also(&apis.FieldError{
				Message: fmt.Sprintf("invalid baseURL: %v", err),
				Paths:   []string{"spec.baseURL"},
			})
		}
	}

	if baseURLChanged || keyRefChanged {
		errs = errs.Also(y.checkSecretAccess(ctx))
	}
	return errs
}

// effectiveBaseURL is the URL the adapter uses for spec.baseURL u.
func effectiveBaseURL(u string) string {
	if u == "" {
		return DefaultBaseURL
	}
	return u
}

// effectiveKeyRef is r with the default Secret key filled in.
func effectiveKeyRef(r APIKeyReference) APIKeyReference {
	if r.Key == "" {
		r.Key = DefaultAPIKeySecretKey
	}
	return r
}

// checkSecretAccess denies the request unless the requesting user may get
// the API key Secret in the source namespace. It fails closed: a missing
// user or a failed review is a denial.
func (y *YipYapSource) checkSecretAccess(ctx context.Context) *apis.FieldError {
	checker := secretReadCheckerFrom(ctx)
	name := y.Spec.APIKeyRef.Name
	if checker == nil || name == "" {
		return nil
	}
	field := "spec.apiKeyRef.name"
	ns := y.Namespace
	if ns == "" {
		return &apis.FieldError{Message: "cannot check access to the API key Secret: the source has no namespace", Paths: []string{field}}
	}
	user := apis.GetUserInfo(ctx)
	if user == nil || user.Username == "" {
		return &apis.FieldError{Message: "cannot check access to the API key Secret: the request has no user", Paths: []string{field}}
	}
	allowed, err := checker.CanGetSecret(ctx, user, ns, name)
	if err != nil {
		return &apis.FieldError{
			Message: fmt.Sprintf("could not check whether you may read Secret %s in %s: %v", name, ns, err),
			Paths:   []string{field},
		}
	}
	if !allowed {
		return &apis.FieldError{
			Message: fmt.Sprintf("you are not allowed to read Secret %s in %s", name, ns),
			Paths:   []string{field},
		}
	}
	return nil
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

	seenSev := make(map[string]int, len(f.Severities))
	for i, sev := range f.Severities {
		field := fmt.Sprintf("severities[%d]", i)
		switch _, ok := validSeverities[sev]; {
		case sev == "":
			errs = errs.Also(apis.ErrInvalidValue(sev, field, "must not be empty"))
		case !ok:
			errs = errs.Also(apis.ErrInvalidValue(sev, field, "must be one of info, warning, critical"))
		}
		if j, dup := seenSev[sev]; dup && sev != "" {
			errs = errs.Also(apis.ErrInvalidValue(sev, field, fmt.Sprintf("duplicates severities[%d]", j)))
		} else if !dup {
			seenSev[sev] = i
		}
	}

	// Monitor IDs reach the adapter as one comma-separated env var, so a
	// comma inside an ID would split it in two.
	seenMon := make(map[string]int, len(f.MonitorIDs))
	for i, id := range f.MonitorIDs {
		field := fmt.Sprintf("monitorIDs[%d]", i)
		switch {
		case strings.TrimSpace(id) == "":
			errs = errs.Also(apis.ErrInvalidValue(id, field, "must not be empty"))
		case strings.ContainsRune(id, ','):
			errs = errs.Also(apis.ErrInvalidValue(id, field, "must not contain a comma"))
		case strings.TrimSpace(id) != id:
			errs = errs.Also(apis.ErrInvalidValue(id, field, "must not have leading or trailing spaces"))
		}
		if j, dup := seenMon[id]; dup && id != "" {
			errs = errs.Also(apis.ErrInvalidValue(id, field, fmt.Sprintf("duplicates monitorIDs[%d]", j)))
		} else if !dup {
			seenMon[id] = i
		}
	}

	if n := f.encodedQueryLen(); n > maxFilterQueryBytes {
		errs = errs.Also(&apis.FieldError{
			Message: fmt.Sprintf("filter is too long: it encodes to %d bytes of query parameters, the limit is %d", n, maxFilterQueryBytes),
			Paths:   []string{apis.CurrentField},
			Details: "List fewer types or monitorIDs, or split them across several sources.",
		})
	}

	return errs
}

// encodedQueryLen is the length of the query string the adapter builds from
// this filter. It must match the parameter names in the adapter (see
// docs/delivery.md, Filters).
func (f *YipYapSourceFilter) encodedQueryLen() int {
	q := url.Values{}
	for _, t := range f.Types {
		q.Add("filter", t)
	}
	for _, s := range f.Severities {
		q.Add("severity", s)
	}
	for _, m := range f.MonitorIDs {
		q.Add("monitor_id", m)
	}
	return len(q.Encode())
}

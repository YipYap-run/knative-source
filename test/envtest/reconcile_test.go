//go:build envtest
// +build envtest

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

package envtest

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"

	sourcesv1alpha1 "github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
)

// TestCRD_CreatesYipYapSource proves a YipYapSource round-trips through the
// real API server: creation survives storage and Get returns the same spec.
func TestCRD_CreatesYipYapSource(t *testing.T) {
	ns := "crd-create"
	newNamespace(t, ns)

	src := &sourcesv1alpha1.YipYapSource{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns},
		Spec: sourcesv1alpha1.YipYapSourceSpec{
			APIKeyRef: sourcesv1alpha1.APIKeyReference{Name: "my-secret"},
			SourceSpec: duckv1.SourceSpec{
				Sink: duckv1.Destination{
					URI: mustParseURL(t, "http://example.com/sink"),
				},
			},
		},
	}
	if err := testClient.Create(testCtx, src); err != nil {
		t.Fatalf("create: %v", err)
	}

	got := &sourcesv1alpha1.YipYapSource{}
	if err := testClient.Get(testCtx, types.NamespacedName{Namespace: ns, Name: "test"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.APIKeyRef.Name != "my-secret" {
		t.Errorf("APIKeyRef.Name = %q, want %q", got.Spec.APIKeyRef.Name, "my-secret")
	}
	if got.Spec.Sink.URI == nil || got.Spec.Sink.URI.String() != "http://example.com/sink" {
		t.Errorf("Sink.URI round-trip broken: %+v", got.Spec.Sink.URI)
	}
}

// TestCRD_StatusSubresource verifies the status subresource is enabled in the
// CRD: Status().Update must mutate status independently of spec.
func TestCRD_StatusSubresource(t *testing.T) {
	ns := "crd-status"
	newNamespace(t, ns)

	src := &sourcesv1alpha1.YipYapSource{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns},
		Spec: sourcesv1alpha1.YipYapSourceSpec{
			APIKeyRef: sourcesv1alpha1.APIKeyReference{Name: "my-secret"},
			SourceSpec: duckv1.SourceSpec{
				Sink: duckv1.Destination{URI: mustParseURL(t, "http://example.com/sink")},
			},
		},
	}
	if err := testClient.Create(testCtx, src); err != nil {
		t.Fatal(err)
	}

	// Mutate status only; the status subresource must accept this.
	src.Status.InitializeConditions()
	src.Status.DeploymentName = "prod-source-abc"
	if err := testClient.Status().Update(testCtx, src); err != nil {
		t.Fatalf("status update: %v", err)
	}

	got := &sourcesv1alpha1.YipYapSource{}
	if err := testClient.Get(testCtx, types.NamespacedName{Namespace: ns, Name: "test"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.DeploymentName != "prod-source-abc" {
		t.Errorf("Status.DeploymentName = %q, want %q", got.Status.DeploymentName, "prod-source-abc")
	}
}

// TestCRD_ListByLabel proves label selectors work via the real API server's
// indexer; exercises list + watch plumbing end-to-end.
func TestCRD_ListByLabel(t *testing.T) {
	ns := "crd-list"
	newNamespace(t, ns)

	for _, name := range []string{"a", "b", "c"} {
		src := &sourcesv1alpha1.YipYapSource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels:    map[string]string{"team": "sre"},
			},
			Spec: sourcesv1alpha1.YipYapSourceSpec{
				APIKeyRef: sourcesv1alpha1.APIKeyReference{Name: "my-secret"},
				SourceSpec: duckv1.SourceSpec{
					Sink: duckv1.Destination{URI: mustParseURL(t, "http://example.com/sink")},
				},
			},
		}
		if err := testClient.Create(testCtx, src); err != nil {
			t.Fatal(err)
		}
	}

	var list sourcesv1alpha1.YipYapSourceList
	if err := testClient.List(testCtx, &list,
		client.InNamespace(ns),
		client.MatchingLabels{"team": "sre"},
	); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 3 {
		t.Errorf("list.Items = %d, want 3", len(list.Items))
	}
}

// TestCRD_AcceptsAnyModeAtAPITier documents the separation of concerns
// between the API-server's schema validation and our admission webhook. The
// CRD uses x-kubernetes-preserve-unknown-fields, so the API server accepts
// any Mode string; the webhook (not installed in this envtest) is the one
// that rejects bogus values. This test pins that contract: if we ever add
// structural-schema enum validation to the CRD itself, this test must fail
// loudly so we revisit the webhook's responsibility.
func TestCRD_AcceptsAnyModeAtAPITier(t *testing.T) {
	ns := "crd-mode"
	newNamespace(t, ns)

	src := &sourcesv1alpha1.YipYapSource{
		ObjectMeta: metav1.ObjectMeta{Name: "bogus-mode", Namespace: ns},
		Spec: sourcesv1alpha1.YipYapSourceSpec{
			APIKeyRef: sourcesv1alpha1.APIKeyReference{Name: "my-secret"},
			Mode:      sourcesv1alpha1.YipYapSourceMode("not-a-real-mode"),
			SourceSpec: duckv1.SourceSpec{
				Sink: duckv1.Destination{URI: mustParseURL(t, "http://example.com/sink")},
			},
		},
	}
	if err := testClient.Create(testCtx, src); err != nil {
		t.Fatalf("API server rejected bogus mode at CRD tier; was schema tightened? err: %v", err)
	}
}

// TestCRD_GVK pins the string form of the GroupKind so accidental group
// renames are caught by the suite.
func TestCRD_GVK(t *testing.T) {
	gk := sourcesv1alpha1.Kind("YipYapSource")
	if !strings.HasPrefix(gk.String(), "YipYapSource.sources.yipyap.run") {
		t.Errorf("unexpected GVK string: %s", gk.String())
	}
}

func mustParseURL(t *testing.T, s string) *apis.URL {
	t.Helper()
	u, err := apis.ParseURL(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

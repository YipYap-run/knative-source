//go:build conformance
// +build conformance

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

package conformance

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	duckv1 "knative.dev/pkg/apis/duck/v1"

	sourcesv1alpha1 "github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
)

// The Knative source duck-type contract we model (each upstream helper is
// cited inline with the test that covers it):
//
//   - Source CR applies + registers at the API server.
//     (SourceCRDRegistryTestHelper in test/conformance/helpers/sources.)
//   - Reconciler populates Ready=True and SinkProvided=True within a
//     reasonable timeout.
//     (SourceStatusTestHelperWithComponentsTestRunner.)
//   - Status.SinkURI is non-nil / non-empty.
//     (SourceStatusTestHelper — "SinkURI populated" table row.)
//   - Status.CloudEventAttributes advertises at least one event type.
//     (SourceStatusTestHelper — "CloudEventAttributes non-empty" row.)
//   - The adapter Deployment exists and reports Available=True.
//     (YipYapSource-specific — upstream sources without an adapter skip
//     this; ours has one, so we check it explicitly.)
//   - Deleting the source cascades to the Deployment via owner references.
//     (Lifecycle contract; not a named upstream helper but implicit in the
//     RBAC + status tests.)
//
// End-to-end event flow and OIDC are out of scope here (see README.md).

const (
	readyTimeout    = 5 * time.Minute
	deletionTimeout = 2 * time.Minute
	pollInterval    = 2 * time.Second
)

// TestYipYapSource_Lifecycle applies a minimal YipYapSource CR and walks the
// full lifecycle contract: Ready, SinkURI, CloudEventAttributes, adapter
// Deployment, cascade-on-delete. One "table-style" test keeps the suite easy
// to read; individual assertions use t.Run subtests so a failure in step N
// doesn't hide steps N+1..M.
func TestYipYapSource_Lifecycle(t *testing.T) {
	requireCluster(t)

	ctx, cancel := context.WithTimeout(testCtx, readyTimeout+deletionTimeout)
	defer cancel()

	nsName := "yipyap-conformance-" + randString(4)
	ns := newNamespace(t, nsName)
	newAPIKeySecret(t, ns.Name, "yipyap-api-key")
	sink := newSinkService(t, ns.Name, "conformance-sink")

	src := &sourcesv1alpha1.YipYapSource{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns.Name,
			Name:      "test",
		},
		Spec: sourcesv1alpha1.YipYapSourceSpec{
			SourceSpec: duckv1.SourceSpec{
				Sink: duckv1.Destination{
					Ref: &duckv1.KReference{
						APIVersion: "v1",
						Kind:       "Service",
						Namespace:  sink.Namespace,
						Name:       sink.Name,
					},
				},
			},
			APIKeyRef: sourcesv1alpha1.APIKeyReference{
				Name: "yipyap-api-key",
			},
			// Default BaseURL, default Mode=auto; conformance exercises the
			// minimum viable spec.
		},
	}

	t.Run("CR creates", func(t *testing.T) {
		if err := testClient.Create(ctx, src); err != nil {
			t.Fatalf("create YipYapSource: %v", err)
		}
		// Cleanup registered at the end so the cascade-on-delete subtest can
		// explicitly delete first and observe the adapter going away.
		t.Cleanup(func() {
			deleteIgnoreNotFound(ctx, testClient, src, t)
		})
	})

	t.Run("Ready=True within timeout", func(t *testing.T) {
		err := waitFor(ctx, readyTimeout, pollInterval, func(ctx context.Context) (bool, error) {
			var got sourcesv1alpha1.YipYapSource
			if err := testClient.Get(ctx, client.ObjectKeyFromObject(src), &got); err != nil {
				return false, err
			}
			return got.Status.IsReady(), nil
		})
		if err != nil {
			// Dump the current status to make debugging a CI failure
			// tractable without needing kubectl access to the cluster.
			dumpStatus(t, ctx, src)
			t.Fatalf("Ready never reached True: %v", err)
		}
	})

	t.Run("Status.SinkURI is populated", func(t *testing.T) {
		var got sourcesv1alpha1.YipYapSource
		if err := testClient.Get(ctx, client.ObjectKeyFromObject(src), &got); err != nil {
			t.Fatalf("refetch source: %v", err)
		}
		if got.Status.SinkURI == nil || got.Status.SinkURI.String() == "" {
			t.Fatalf("Status.SinkURI empty after Ready=True; got %+v", got.Status.SinkURI)
		}
	})

	t.Run("Status.CloudEventAttributes non-empty", func(t *testing.T) {
		var got sourcesv1alpha1.YipYapSource
		if err := testClient.Get(ctx, client.ObjectKeyFromObject(src), &got); err != nil {
			t.Fatalf("refetch source: %v", err)
		}
		if len(got.Status.CloudEventAttributes) == 0 {
			t.Fatal("Status.CloudEventAttributes is empty; a Knative source MUST advertise at least one attribute")
		}
		// Soft check: every advertised attribute should have a non-empty type.
		// The upstream helper requires only that the list be non-empty, so we
		// treat a bad row as a warning rather than a failure — filed inline.
		for i, a := range got.Status.CloudEventAttributes {
			if strings.TrimSpace(a.Type) == "" {
				t.Errorf("CloudEventAttributes[%d].Type is empty", i)
			}
		}
	})

	t.Run("Adapter Deployment exists and is Available", func(t *testing.T) {
		var got sourcesv1alpha1.YipYapSource
		if err := testClient.Get(ctx, client.ObjectKeyFromObject(src), &got); err != nil {
			t.Fatalf("refetch source: %v", err)
		}
		depName := got.Status.DeploymentName
		if depName == "" {
			t.Fatal("Status.DeploymentName unset; the controller must surface the adapter Deployment name")
		}
		var dep appsv1.Deployment
		if err := testClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: depName}, &dep); err != nil {
			t.Fatalf("get adapter Deployment %q: %v", depName, err)
		}
		if !deploymentAvailable(&dep) {
			t.Fatalf("adapter Deployment %q not Available: status=%+v", depName, dep.Status)
		}
	})

	t.Run("Delete source cascades to adapter Deployment", func(t *testing.T) {
		var got sourcesv1alpha1.YipYapSource
		if err := testClient.Get(ctx, client.ObjectKeyFromObject(src), &got); err != nil {
			t.Fatalf("refetch source: %v", err)
		}
		depName := got.Status.DeploymentName
		if err := testClient.Delete(ctx, &got); err != nil {
			t.Fatalf("delete YipYapSource: %v", err)
		}

		// The adapter Deployment is owned by the source via an OwnerReference
		// (controller=true, blockOwnerDeletion=true). Garbage collection is
		// asynchronous, hence the poll.
		err := waitFor(ctx, deletionTimeout, pollInterval, func(ctx context.Context) (bool, error) {
			var dep appsv1.Deployment
			err := testClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: depName}, &dep)
			if err == nil {
				return false, nil
			}
			// Any non-NotFound error bubbles up; IsNotFound means GC is done.
			if client.IgnoreNotFound(err) == nil {
				return true, nil
			}
			return false, err
		})
		if err != nil {
			t.Fatalf("adapter Deployment %q still present after source deletion: %v", depName, err)
		}
	})
}

// dumpStatus logs the current YipYapSource status. Called on Ready-timeout so
// the failing test log captures enough context to diagnose a controller hang
// without kubectl access.
func dumpStatus(t *testing.T, ctx context.Context, src *sourcesv1alpha1.YipYapSource) {
	t.Helper()
	var got sourcesv1alpha1.YipYapSource
	if err := testClient.Get(ctx, client.ObjectKeyFromObject(src), &got); err != nil {
		t.Logf("dumpStatus: refetch failed: %v", err)
		return
	}
	t.Logf("YipYapSource %s/%s status:", got.Namespace, got.Name)
	t.Logf("  SinkURI: %v", got.Status.SinkURI)
	t.Logf("  DeploymentName: %q", got.Status.DeploymentName)
	for _, c := range got.Status.Conditions {
		t.Logf("  Condition %s=%s reason=%q message=%q", c.Type, c.Status, c.Reason, c.Message)
	}
}

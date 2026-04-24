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

// Package conformance is the Knative-source conformance test suite for
// YipYapSource. It is a locally-authored subset of the contract exercised by
// knative.dev/eventing/test/conformance/helpers/sources; we model the same
// lifecycle checks without pulling in the upstream `test/lib` framework
// (which would drag the full eventing e2e harness into vendor/). Each test
// cites the upstream helper it mirrors.
//
// All files in this package carry the `conformance` build tag so a default
// `go test ./...` never attempts to run against a real cluster. The suite
// must, however, BUILD cleanly under `go vet -tags conformance ./...` — the
// envtest package follows the same convention.
//
// To run locally against a kind cluster with the controller installed via
// Helm see test/conformance/README.md.
package conformance

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	sourcesv1alpha1 "github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
)

// Shared clients for every test in the package. Initialized in TestMain once
// we confirm a kubeconfig is present and the cluster is reachable.
var (
	testCfg    *rest.Config
	testClient client.Client
	testCtx    = context.Background()
)

// Defaults. Overridable via env vars so CI can pin them.
const (
	envKubeconfig     = "KUBECONFIG"
	envSkipReason     = "conformance tests require a live cluster with the YipYapSource controller installed; set KUBECONFIG"
	defaultAPIKey     = "dummy-api-key-for-conformance"
	defaultAdapterImg = "ghcr.io/yipyap-run/knative-source:latest"
)

// TestMain discovers the cluster. When no KUBECONFIG is set we skip the whole
// suite rather than fail — that keeps `go test -tags conformance ./...` a
// useful *build* check on developer machines.
func TestMain(m *testing.M) {
	utilruntime.Must(sourcesv1alpha1.AddToScheme(scheme.Scheme))

	if !kubeconfigAvailable() {
		fmt.Fprintln(os.Stderr, envSkipReason)
		// Exit 0 so CI/dev runs without a cluster don't red-fail; individual
		// tests also t.Skip() to surface clearly in verbose output.
		os.Exit(0)
	}

	cfg, err := config.GetConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "get kubeconfig: %v\n", err)
		os.Exit(1)
	}
	testCfg = cfg

	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "build client: %v\n", err)
		os.Exit(1)
	}
	testClient = c

	os.Exit(m.Run())
}

// kubeconfigAvailable returns true when either KUBECONFIG is set in the env
// or the default $HOME/.kube/config is readable. Mirrors clientcmd's fallback
// order but without actually dialing the server.
func kubeconfigAvailable() bool {
	if os.Getenv(envKubeconfig) != "" {
		return true
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if _, err := os.Stat(rules.GetDefaultFilename()); err == nil {
		return true
	}
	return false
}

// requireCluster skips the test if the cluster wasn't wired in TestMain.
// Every top-level Test function calls this first.
func requireCluster(t *testing.T) {
	t.Helper()
	if testClient == nil {
		t.Skip(envSkipReason)
	}
}

// newNamespace creates a fresh namespace for the duration of a test and
// registers cleanup. Each conformance test gets its own namespace so failures
// don't leak state into later runs.
func newNamespace(t *testing.T, name string) *corev1.Namespace {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"yipyap.run/conformance": "true",
			},
		},
	}
	if err := testClient.Create(testCtx, ns); err != nil {
		t.Fatalf("create namespace %q: %v", name, err)
	}
	t.Cleanup(func() {
		deleteIgnoreNotFound(testCtx, testClient, ns, t)
	})
	return ns
}

// newAPIKeySecret creates the Secret a YipYapSource references through
// .spec.apiKeyRef. Conformance doesn't exercise a real yipyap backend, so a
// placeholder value is fine.
func newAPIKeySecret(t *testing.T, ns, name string) *corev1.Secret {
	t.Helper()
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		StringData: map[string]string{
			"api-key": defaultAPIKey,
		},
	}
	if err := testClient.Create(testCtx, sec); err != nil {
		t.Fatalf("create api-key Secret: %v", err)
	}
	return sec
}

// newSinkService creates a minimal addressable sink: a ClusterIP Service that
// points at nothing. The controller resolves `ref:` sinks through the addressable
// duck type, and a bare Service exposes an HTTP URL the controller can plug into
// Status.SinkURI. Real event flow is out of scope here; we're verifying the
// source reports SinkProvided=True when the ref resolves.
func newSinkService(t *testing.T, ns, name string) *corev1.Service {
	t.Helper()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{
				Name: "http", Port: 80, TargetPort: intFromInt(8080),
			}},
			// Selector is intentionally empty: we don't need real pods behind
			// the sink to exercise source reconciliation.
			Selector: map[string]string{"app": "conformance-sink"},
		},
	}
	if err := testClient.Create(testCtx, svc); err != nil {
		t.Fatalf("create sink Service: %v", err)
	}
	return svc
}

// deleteIgnoreNotFound best-effort deletes an object during cleanup. A
// NotFound error is expected when the test already deleted the object, so we
// swallow it rather than fail the cleanup phase.
func deleteIgnoreNotFound(ctx context.Context, c client.Client, obj client.Object, t *testing.T) {
	t.Helper()
	if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		t.Logf("cleanup delete %T %s/%s: %v", obj, obj.GetNamespace(), obj.GetName(), err)
	}
}

// waitFor polls predicate until it returns (true, nil) or the context/timeout
// expires. It's a stripped-down version of wait.PollUntilContextTimeout so
// the suite doesn't pick up `k8s.io/apimachinery/pkg/util/wait` just for one
// helper; the explicit loop also makes failure messages easier to read.
func waitFor(ctx context.Context, timeout, interval time.Duration, predicate func(context.Context) (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		done, err := predicate(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for condition", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// deploymentAvailable is true when .status.conditions contains
// Available=True. The controller gates Ready on this condition, so mirroring
// the check gives us a crisp "is the adapter up?" signal.
func deploymentAvailable(d *appsv1.Deployment) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

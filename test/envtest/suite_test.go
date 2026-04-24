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

// Package envtest exercises the YipYapSource CRD against a real kube-apiserver
// + etcd via sigs.k8s.io/controller-runtime/pkg/envtest. It does NOT run the
// reconciler or the admission webhook — those are covered by the table-driven
// tests in pkg/reconciler/yipyapsource and the webhook package. Here we verify
// that the CRD registers, that objects round-trip through real storage, and
// that the status subresource behaves. Gated behind the `envtest` build tag so
// normal `go test ./...` runs do not depend on setup-envtest binaries.
package envtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlenvtest "sigs.k8s.io/controller-runtime/pkg/envtest"

	sourcesv1alpha1 "github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
)

var (
	testEnv    *ctrlenvtest.Environment
	testCfg    *rest.Config
	testClient client.Client
	testCtx    = context.Background()
)

func TestMain(m *testing.M) {
	// Register our API types into the global scheme so the client can
	// codec YipYapSource objects.
	utilruntime.Must(sourcesv1alpha1.AddToScheme(scheme.Scheme))

	testEnv = &ctrlenvtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("testdata", "crds"),
		},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest start failed: %v\n", err)
		os.Exit(1)
	}
	testCfg = cfg

	testClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest client init failed: %v\n", err)
		_ = testEnv.Stop()
		os.Exit(1)
	}

	code := m.Run()

	if err := testEnv.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "envtest stop failed: %v\n", err)
	}
	os.Exit(code)
}

// newNamespace creates a fresh namespace for the duration of a test and
// schedules its deletion via t.Cleanup. Tests use per-test namespaces so the
// suite is trivially parallelizable later if desired.
func newNamespace(t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := testClient.Create(testCtx, ns); err != nil {
		t.Fatalf("create namespace %q: %v", name, err)
	}
	t.Cleanup(func() {
		_ = testClient.Delete(testCtx, ns)
	})
}

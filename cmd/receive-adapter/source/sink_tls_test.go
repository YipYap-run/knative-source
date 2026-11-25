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

package source

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	"github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/resources"
)

// privateCA is a throwaway CA plus a server certificate it signed for
// 127.0.0.1, standing in for a cluster-internal CA that is not in the
// system roots.
type privateCA struct {
	caPEM  string
	server tls.Certificate
}

func newPrivateCA(t *testing.T) privateCA {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test private CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "sink"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return privateCA{
		caPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		server: tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey},
	}
}

// startTLSSink serves h over TLS with a certificate from ca.
func startTLSSink(t *testing.T, ca privateCA, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{ca.server}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// renderedEnv renders the adapter Deployment for sink and returns its
// container env, with Secret-backed values replaced by a placeholder.
func renderedEnv(t *testing.T, sink *duckv1.Addressable) map[string]string {
	t.Helper()
	src := &v1alpha1.YipYapSource{
		ObjectMeta: metav1.ObjectMeta{Name: "ys", Namespace: "ns", UID: types.UID("uid-1")},
		Spec: v1alpha1.YipYapSourceSpec{
			SourceSpec: duckv1.SourceSpec{Sink: duckv1.Destination{URI: sink.URL}},
			APIKeyRef:  v1alpha1.APIKeyReference{Name: "yipyap-api-key"},
		},
	}
	src.SetDefaults(context.Background())
	dep := resources.MakeAdapterDeploymentForSink(src, sink, "registry.test/adapter:unit")
	env := map[string]string{}
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if e.ValueFrom != nil {
			env[e.Name] = "placeholder"
			continue
		}
		env[e.Name] = e.Value
	}
	return env
}

// TestSinkClient_PrivateCAFromRenderedEnv renders the controller's env for a
// sink address that carries a private CA, loads it the way the adapter does
// at start, and delivers to a TLS server whose certificate that CA signed.
// The same client without K_CA_CERTS must fail verification.
func TestSinkClient_PrivateCAFromRenderedEnv(t *testing.T) {
	ca := newPrivateCA(t)
	h := &captureHandler{status: http.StatusAccepted}
	srv := startTLSSink(t, ca, h)
	sinkURL, err := apis.ParseURL(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	env := renderedEnv(t, &duckv1.Addressable{URL: sinkURL, CACerts: &ca.caPEM})
	if env["K_CA_CERTS"] != ca.caPEM {
		t.Fatalf("rendered K_CA_CERTS = %q, want the sink CA bundle", env["K_CA_CERTS"])
	}
	cfg, err := LoadConfig(fakeEnv(env))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SinkRootCAs == nil {
		t.Fatal("SinkRootCAs is nil with K_CA_CERTS set")
	}
	if err := NewSinkClient(cfg).Send(t.Context(), newTestEvent(t)); err != nil {
		t.Fatalf("Send with the sink CA: %v", err)
	}
	if h.gotMethod != http.MethodPost {
		t.Fatalf("sink saw method %q, want POST", h.gotMethod)
	}

	delete(env, "K_CA_CERTS")
	cfg, err = LoadConfig(fakeEnv(env))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	err = NewSinkClient(cfg).Send(t.Context(), newTestEvent(t))
	var se *SinkError
	if !errors.As(err, &se) || !se.Retryable {
		t.Fatalf("Send without the sink CA = %v, want a retryable verification failure", err)
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("Send without the sink CA = %v, want a certificate error", err)
	}
}

func TestLoadConfig_RejectsBadCACerts(t *testing.T) {
	for name, bundle := range map[string]string{
		"garbage":  "not a certificate",
		"bad DER":  "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
		"key only": "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(fakeEnv(map[string]string{
				"K_SINK":         "https://sink.example",
				"YIPYAP_API_KEY": "k",
				"K_CA_CERTS":     bundle,
			}))
			if err == nil || !strings.HasPrefix(err.Error(), "K_CA_CERTS:") {
				t.Fatalf("LoadConfig error = %v, want a K_CA_CERTS error", err)
			}
		})
	}
}

// TestSinkClient_OIDCBearerFromRenderedEnv checks that the rendered env
// points the adapter at the projected token mount, and that every send
// reads the current token, so a kubelet rotation takes effect at once.
func TestSinkClient_OIDCBearerFromRenderedEnv(t *testing.T) {
	h := &captureHandler{status: http.StatusOK}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	sinkURL, err := apis.ParseURL(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	aud := "https://broker.example/default"

	env := renderedEnv(t, &duckv1.Addressable{URL: sinkURL, Audience: &aud})
	if env["K_AUDIENCE"] != aud {
		t.Fatalf("rendered K_AUDIENCE = %q, want %q", env["K_AUDIENCE"], aud)
	}
	want := resources.OIDCTokenMountPath + "/" + resources.OIDCTokenFile
	if env["YIPYAP_SINK_TOKEN_PATH"] != want || DefaultSinkTokenPath != want {
		t.Fatalf("token path: rendered %q, adapter default %q, want %q", env["YIPYAP_SINK_TOKEN_PATH"], DefaultSinkTokenPath, want)
	}

	// Point the adapter at a temp file in place of the projected volume.
	tokenFile := filepath.Join(t.TempDir(), "token")
	env["YIPYAP_SINK_TOKEN_PATH"] = tokenFile
	cfg, err := LoadConfig(fakeEnv(env))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SinkAudience != aud || cfg.SinkTokenPath != tokenFile {
		t.Fatalf("SinkAudience = %q, SinkTokenPath = %q", cfg.SinkAudience, cfg.SinkTokenPath)
	}
	sc := NewSinkClient(cfg)

	// No token file yet: retryable, and nothing reaches the sink.
	err = sc.Send(t.Context(), newTestEvent(t))
	var se *SinkError
	if !errors.As(err, &se) || !se.Retryable || h.gotMethod != "" {
		t.Fatalf("Send without a token = %v (sink saw %q), want a retryable error and no request", err, h.gotMethod)
	}

	for _, tok := range []string{"token-one", "token-two"} {
		if err := os.WriteFile(tokenFile, []byte(tok+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := sc.Send(t.Context(), newTestEvent(t)); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if got := h.gotHeaders.Get("Authorization"); got != "Bearer "+tok {
			t.Fatalf("Authorization = %q, want Bearer %s", got, tok)
		}
	}
}

func TestSinkClient_NoAudienceSendsNoAuthorization(t *testing.T) {
	h := &captureHandler{status: http.StatusOK}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	if err := NewSinkClient(&Config{Sink: srv.URL}).Send(t.Context(), newTestEvent(t)); err != nil {
		t.Fatal(err)
	}
	if got := h.gotHeaders.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want none", got)
	}
}

func TestSinkClient_SendsDataschema(t *testing.T) {
	h := &captureHandler{status: http.StatusOK}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ev := newTestEvent(t)
	ev.SetDataSchema("https://schemas.yipyap.run/alert/v1.json")
	if err := NewSinkClient(&Config{Sink: srv.URL}).Send(t.Context(), ev); err != nil {
		t.Fatal(err)
	}
	if got := h.gotHeaders.Get("Ce-Dataschema"); got != "https://schemas.yipyap.run/alert/v1.json" {
		t.Fatalf("Ce-Dataschema = %q, want the event's dataschema", got)
	}

	// Absent dataschema: no header.
	if err := NewSinkClient(&Config{Sink: srv.URL}).Send(t.Context(), newTestEvent(t)); err != nil {
		t.Fatal(err)
	}
	if got, ok := h.gotHeaders["Ce-Dataschema"]; ok {
		t.Fatalf("Ce-Dataschema = %q, want no header", got)
	}
}

// A redirect is not a delivery. Followed, a 302 turns the POST into a GET
// without the event, and the target's 200 would confirm an event nobody
// received, while a same-host redirect would carry the bearer token along.
func TestSinkClient_RedirectIsRetryableAndNotFollowed(t *testing.T) {
	var targetHits int
	var targetAuth string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		targetAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect} {
		sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, code)
		}))
		t.Cleanup(sink.Close)

		tokenFile := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokenFile, []byte("secret-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		sc := NewSinkClient(&Config{Sink: sink.URL, SinkAudience: "aud", SinkTokenPath: tokenFile})
		err := sc.Send(t.Context(), newTestEvent(t))
		var se *SinkError
		if !errors.As(err, &se) || !se.Retryable || se.StatusCode != code {
			t.Fatalf("%d: Send = %v, want a retryable SinkError with status %d", code, err, code)
		}
	}
	if targetHits != 0 || targetAuth != "" {
		t.Fatalf("redirect target got %d requests (Authorization %q), want none", targetHits, targetAuth)
	}
}

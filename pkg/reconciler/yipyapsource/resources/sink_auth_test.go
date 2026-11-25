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

package resources

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	duckv1 "knative.dev/pkg/apis/duck/v1"
)

const testCA = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func sinkAddr(t *testing.T, ca, aud string) *duckv1.Addressable {
	t.Helper()
	addr := &duckv1.Addressable{URL: sinkURL(t)}
	if ca != "" {
		addr.CACerts = &ca
	}
	if aud != "" {
		addr.Audience = &aud
	}
	return addr
}

func volumeByName(vols []corev1.Volume, name string) *corev1.Volume {
	for i := range vols {
		if vols[i].Name == name {
			return &vols[i]
		}
	}
	return nil
}

func TestMakeAdapterDeploymentForSink_PlainSink(t *testing.T) {
	d := MakeAdapterDeploymentForSink(baseSource(), sinkAddr(t, "", ""), testImage)
	ps := d.Spec.Template.Spec
	c := ps.Containers[0]
	envAbsent(t, c.Env, EnvCACerts)
	envAbsent(t, c.Env, EnvAudience)
	envAbsent(t, c.Env, EnvSinkTokenPath)
	if v := volumeByName(ps.Volumes, OIDCTokenVolumeName); v != nil {
		t.Fatalf("plain sink got an OIDC token volume: %+v", v)
	}
	if ps.ServiceAccountName != "" {
		t.Fatalf("ServiceAccountName = %q, want unset", ps.ServiceAccountName)
	}
}

func TestMakeAdapterDeploymentForSink_CACerts(t *testing.T) {
	d := MakeAdapterDeploymentForSink(baseSource(), sinkAddr(t, testCA, ""), testImage)
	if got := envByName(t, d.Spec.Template.Spec.Containers[0].Env, EnvCACerts).Value; got != testCA {
		t.Fatalf("%s = %q, want the sink CA bundle", EnvCACerts, got)
	}
}

func TestMakeAdapterDeploymentForSink_AudienceProjectsToken(t *testing.T) {
	const aud = "https://broker.example/ns/default"
	src := baseSource()
	d := MakeAdapterDeploymentForSink(src, sinkAddr(t, "", aud), testImage)
	ps := d.Spec.Template.Spec
	c := ps.Containers[0]

	if got := envByName(t, c.Env, EnvAudience).Value; got != aud {
		t.Fatalf("%s = %q, want %q", EnvAudience, got, aud)
	}
	tokenPath := envByName(t, c.Env, EnvSinkTokenPath).Value
	if tokenPath != OIDCTokenMountPath+"/"+OIDCTokenFile {
		t.Fatalf("%s = %q", EnvSinkTokenPath, tokenPath)
	}

	// The API token stays off; only the audience-bound token is mounted.
	if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken {
		t.Fatal("automountServiceAccountToken must stay false")
	}
	v := volumeByName(ps.Volumes, OIDCTokenVolumeName)
	if v == nil || v.Projected == nil || len(v.Projected.Sources) != 1 {
		t.Fatalf("OIDC token volume = %+v, want one projected source", v)
	}
	tok := v.Projected.Sources[0].ServiceAccountToken
	if tok == nil || tok.Audience != aud || tok.Path != OIDCTokenFile ||
		tok.ExpirationSeconds == nil || *tok.ExpirationSeconds != OIDCTokenExpirationSeconds {
		t.Fatalf("projected token = %+v, want audience %q, path %q", tok, aud, OIDCTokenFile)
	}
	var mounted bool
	for _, m := range c.VolumeMounts {
		if m.Name == OIDCTokenVolumeName {
			mounted = true
			if m.MountPath != OIDCTokenMountPath || !m.ReadOnly {
				t.Fatalf("token mount = %+v, want read-only at %s", m, OIDCTokenMountPath)
			}
		}
	}
	if !mounted {
		t.Fatal("OIDC token volume is not mounted")
	}

	// The pod runs as the generated per-source identity.
	if ps.ServiceAccountName != OIDCServiceAccountName(src) {
		t.Fatalf("ServiceAccountName = %q, want %q", ps.ServiceAccountName, OIDCServiceAccountName(src))
	}
}

// A pinned account must not receive a token for an audience the source's
// author chose: the audience could be one the API server accepts, and the
// token goes to the sink URL. With an audience the pod runs as the generated
// account; without one the pinned account is kept.
func TestMakeAdapterDeploymentForSink_AudienceOverridesPinnedServiceAccount(t *testing.T) {
	src := baseSource()
	src.Spec.ServiceAccountName = "pinned"
	d := MakeAdapterDeploymentForSink(src, sinkAddr(t, "", "https://kubernetes.default.svc"), testImage)
	if got := d.Spec.Template.Spec.ServiceAccountName; got != OIDCServiceAccountName(src) {
		t.Fatalf("ServiceAccountName = %q, want the generated %q", got, OIDCServiceAccountName(src))
	}
	d = MakeAdapterDeploymentForSink(src, sinkAddr(t, "", ""), testImage)
	if got := d.Spec.Template.Spec.ServiceAccountName; got != "pinned" {
		t.Fatalf("plain sink: ServiceAccountName = %q, want pinned", got)
	}
}

func TestOIDCServiceAccount(t *testing.T) {
	src := baseSource()
	sa := MakeOIDCServiceAccount(src)
	if sa.Name != "ys-test-oidc-sources.yipyap.run-yipyapsource" {
		t.Fatalf("name = %q", sa.Name)
	}
	if !metav1.IsControlledBy(sa, src) {
		t.Fatal("OIDC ServiceAccount is not controlled by the source")
	}
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Fatal("OIDC ServiceAccount must not automount API tokens")
	}

	// Long source names still give a valid, bounded name.
	src.Name = strings.Repeat("a", 253)
	name := OIDCServiceAccountName(src)
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 || len(name) > 63 {
		t.Fatalf("name %q (len %d) invalid: %v", name, len(name), errs)
	}
}

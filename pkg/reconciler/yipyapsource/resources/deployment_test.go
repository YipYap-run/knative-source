/*
Copyright 2026 The YipYap Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package resources

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	"knative.dev/pkg/kmeta"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
)

const (
	testName      = "ys-test"
	testNamespace = "ys-ns"
	testUID       = "abc-uid-123"
	testImage     = "ko://example.com/adapter:latest"
	testBaseURL   = "https://console.yipyap.run"
	testAPIKeyNm  = "yipyap-key"
	testAPIKeyKey = "api-key"
)

func baseSource() *v1alpha1.YipYapSource {
	return &v1alpha1.YipYapSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testName,
			Namespace: testNamespace,
			UID:       types.UID(testUID),
		},
		Spec: v1alpha1.YipYapSourceSpec{
			APIKeyRef: v1alpha1.APIKeyReference{
				Name: testAPIKeyNm,
				Key:  testAPIKeyKey,
			},
			BaseURL: testBaseURL,
			Mode:    v1alpha1.YipYapSourceModeAuto,
		},
	}
}

func sinkURL(t *testing.T) *apis.URL {
	t.Helper()
	u, err := apis.ParseURL("http://broker.default.svc/broker")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	return u
}

func envByName(t *testing.T, envs []corev1.EnvVar, name string) corev1.EnvVar {
	t.Helper()
	for _, e := range envs {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("env %q not found; got %+v", name, envs)
	return corev1.EnvVar{}
}

func envAbsent(t *testing.T, envs []corev1.EnvVar, name string) {
	t.Helper()
	for _, e := range envs {
		if e.Name == name {
			t.Fatalf("env %q should be absent, got %+v", name, e)
		}
	}
}

func TestMakeAdapterDeployment_Basic(t *testing.T) {
	src := baseSource()
	sink := sinkURL(t)

	d := MakeAdapterDeployment(src, sink, testImage)

	// Labels
	if got, want := d.Labels["sources.yipyap.run/source"], testName; got != want {
		t.Errorf("source label: got %q, want %q", got, want)
	}
	if got, want := d.Spec.Selector.MatchLabels["sources.yipyap.run/source"], testName; got != want {
		t.Errorf("selector label: got %q, want %q", got, want)
	}
	if got, want := d.Spec.Template.Labels["sources.yipyap.run/source"], testName; got != want {
		t.Errorf("template label: got %q, want %q", got, want)
	}

	// Name is deterministic (kmeta.ChildName)
	wantName := kmeta.ChildName(testName+"-source-", testUID)
	if d.Name != wantName {
		t.Errorf("Name: got %q, want %q", d.Name, wantName)
	}

	// Owner ref wires GC to the CR
	if len(d.OwnerReferences) != 1 {
		t.Fatalf("OwnerReferences: want 1, got %d", len(d.OwnerReferences))
	}
	if d.OwnerReferences[0].UID != types.UID(testUID) {
		t.Errorf("OwnerReferences UID: got %q, want %q", d.OwnerReferences[0].UID, testUID)
	}
	if d.OwnerReferences[0].Controller == nil || !*d.OwnerReferences[0].Controller {
		t.Errorf("OwnerReferences Controller must be true")
	}

	// Replicas
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
		t.Errorf("Replicas: got %v, want 1", d.Spec.Replicas)
	}

	// Container
	if len(d.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("Containers: want 1, got %d", len(d.Spec.Template.Spec.Containers))
	}
	c := d.Spec.Template.Spec.Containers[0]
	if c.Name != "source" {
		t.Errorf("container name: got %q, want %q", c.Name, "source")
	}
	if c.Image != testImage {
		t.Errorf("container image: got %q, want %q", c.Image, testImage)
	}

	// Env vars
	if got, want := envByName(t, c.Env, "K_SINK").Value, sink.String(); got != want {
		t.Errorf("K_SINK: got %q, want %q", got, want)
	}
	if got, want := envByName(t, c.Env, "YIPYAP_BASE_URL").Value, testBaseURL; got != want {
		t.Errorf("YIPYAP_BASE_URL: got %q, want %q", got, want)
	}
	mode := envByName(t, c.Env, "YIPYAP_MODE")
	if mode.Value != string(v1alpha1.YipYapSourceModeAuto) {
		t.Errorf("YIPYAP_MODE: got %q, want %q", mode.Value, v1alpha1.YipYapSourceModeAuto)
	}
	key := envByName(t, c.Env, "YIPYAP_API_KEY")
	if key.ValueFrom == nil || key.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("YIPYAP_API_KEY must use SecretKeyRef, got %+v", key)
	}
	if key.ValueFrom.SecretKeyRef.Name != testAPIKeyNm {
		t.Errorf("SecretKeyRef Name: got %q, want %q", key.ValueFrom.SecretKeyRef.Name, testAPIKeyNm)
	}
	if key.ValueFrom.SecretKeyRef.Key != testAPIKeyKey {
		t.Errorf("SecretKeyRef Key: got %q, want %q", key.ValueFrom.SecretKeyRef.Key, testAPIKeyKey)
	}

	// Optional envs absent
	envAbsent(t, c.Env, "YIPYAP_EVENT_FILTER")
	envAbsent(t, c.Env, "K_CE_OVERRIDES")

	// Probes
	if c.LivenessProbe == nil || c.LivenessProbe.HTTPGet == nil || c.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("LivenessProbe: got %+v, want /healthz HTTPGet", c.LivenessProbe)
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil || c.ReadinessProbe.HTTPGet.Path != "/readyz" {
		t.Errorf("ReadinessProbe: got %+v, want /readyz HTTPGet", c.ReadinessProbe)
	}

	// Volume + VolumeMount
	foundVol := false
	for _, v := range d.Spec.Template.Spec.Volumes {
		if v.Name == "cursor" {
			if v.EmptyDir == nil {
				t.Errorf("volume cursor: want EmptyDir, got %+v", v.VolumeSource)
			}
			foundVol = true
		}
	}
	if !foundVol {
		t.Errorf("volume cursor missing")
	}
	foundMount := false
	for _, m := range c.VolumeMounts {
		if m.Name == "cursor" && m.MountPath == "/var/run/yipyap" {
			foundMount = true
		}
	}
	if !foundMount {
		t.Errorf("cursor volume mount missing (want /var/run/yipyap)")
	}

	// No service account set by default
	if d.Spec.Template.Spec.ServiceAccountName != "" {
		t.Errorf("ServiceAccountName: got %q, want empty", d.Spec.Template.Spec.ServiceAccountName)
	}
}

func TestMakeAdapterDeployment_FullSpec(t *testing.T) {
	src := baseSource()
	src.Spec.ServiceAccountName = "adapter-sa"
	src.Spec.Filter = &v1alpha1.YipYapSourceFilter{
		Types: []string{"run.yipyap.alert.*", "run.yipyap.monitor.state"},
	}
	src.Spec.CloudEventOverrides = &duckv1.CloudEventOverrides{
		Extensions: map[string]string{"env": "prod", "region": "us"},
	}

	sink := sinkURL(t)
	d := MakeAdapterDeployment(src, sink, testImage)

	c := d.Spec.Template.Spec.Containers[0]

	// SA
	if d.Spec.Template.Spec.ServiceAccountName != "adapter-sa" {
		t.Errorf("ServiceAccountName: got %q, want %q", d.Spec.Template.Spec.ServiceAccountName, "adapter-sa")
	}

	// All filter types are forwarded as a comma-separated list.
	if got, want := envByName(t, c.Env, "YIPYAP_EVENT_FILTER").Value, "run.yipyap.alert.*,run.yipyap.monitor.state"; got != want {
		t.Errorf("YIPYAP_EVENT_FILTER: got %q, want %q", got, want)
	}

	// CE overrides
	ovr := envByName(t, c.Env, "K_CE_OVERRIDES")
	var decoded struct {
		Extensions map[string]string `json:"extensions"`
	}
	if err := json.Unmarshal([]byte(ovr.Value), &decoded); err != nil {
		t.Fatalf("K_CE_OVERRIDES not valid JSON: %v (%q)", err, ovr.Value)
	}
	if decoded.Extensions["env"] != "prod" || decoded.Extensions["region"] != "us" {
		t.Errorf("K_CE_OVERRIDES extensions: got %+v, want env=prod region=us", decoded.Extensions)
	}
}

func TestMakeAdapterDeployment_ChildNameDeterministic(t *testing.T) {
	src := baseSource()
	sink := sinkURL(t)

	d1 := MakeAdapterDeployment(src, sink, testImage)
	d2 := MakeAdapterDeployment(src, sink, testImage)

	if d1.Name != d2.Name {
		t.Errorf("names diverge: %q vs %q", d1.Name, d2.Name)
	}

	wantPrefix := testName + "-source-"
	if got := d1.Name; len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Errorf("Name prefix: got %q, want prefix %q", got, wantPrefix)
	}
}

// A single filter type forwards verbatim (no trailing comma).
func TestMakeAdapterDeployment_SingleFilterType(t *testing.T) {
	src := baseSource()
	src.Spec.Filter = &v1alpha1.YipYapSourceFilter{Types: []string{"run.yipyap.alert.*"}}
	d := MakeAdapterDeployment(src, sinkURL(t), testImage)
	c := d.Spec.Template.Spec.Containers[0]
	if got, want := envByName(t, c.Env, "YIPYAP_EVENT_FILTER").Value, "run.yipyap.alert.*"; got != want {
		t.Errorf("YIPYAP_EVENT_FILTER: got %q, want %q", got, want)
	}
}

// Guard against regressions: filter types empty → no YIPYAP_EVENT_FILTER
func TestMakeAdapterDeployment_EmptyFilterTypesSkipsEnv(t *testing.T) {
	src := baseSource()
	src.Spec.Filter = &v1alpha1.YipYapSourceFilter{Types: nil}
	d := MakeAdapterDeployment(src, sinkURL(t), testImage)
	envAbsent(t, d.Spec.Template.Spec.Containers[0].Env, "YIPYAP_EVENT_FILTER")
}

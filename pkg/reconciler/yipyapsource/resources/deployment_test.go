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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

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

// TestMakeAdapterDeployment_LongNameLabels: object names may be 253
// characters, label values only 63. Every label and selector value on the
// rendered Deployment must be a valid label value, the selector must match
// the pod template, and two long names sharing a prefix must not collide.
func TestMakeAdapterDeployment_LongNameLabels(t *testing.T) {
	longName := func(last byte) string {
		b := []byte(strings.Repeat("a", 253))
		b[len(b)-1] = last
		return string(b)
	}

	render := func(name string) map[string]string {
		src := baseSource()
		src.Name = name
		dep := MakeAdapterDeployment(src, sinkURL(t), testImage)

		for _, set := range []map[string]string{
			dep.Labels,
			dep.Spec.Selector.MatchLabels,
			dep.Spec.Template.Labels,
		} {
			for k, v := range set {
				if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
					t.Errorf("label %s=%q invalid: %v", k, v, errs)
				}
			}
		}
		sel, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
		if err != nil {
			t.Fatalf("selector: %v", err)
		}
		if !sel.Matches(labels.Set(dep.Spec.Template.Labels)) {
			t.Errorf("selector %v does not match template labels %v", sel, dep.Spec.Template.Labels)
		}
		if errs := validation.IsDNS1123Subdomain(dep.Name); len(errs) > 0 {
			t.Errorf("Deployment name %q invalid: %v", dep.Name, errs)
		}
		return dep.Spec.Selector.MatchLabels
	}

	a := render(longName('a'))
	b := render(longName('b'))
	if a[SourceLabelKey] == b[SourceLabelKey] {
		t.Errorf("distinct long names share label value %q", a[SourceLabelKey])
	}

	// Short names keep the plain name, so existing Deployments keep their
	// (immutable) selector across the upgrade.
	if got := render(testName)[SourceLabelKey]; got != testName {
		t.Errorf("short name label = %q, want %q", got, testName)
	}
}

// TestMakeAdapterDeployment_SafeRollout: the adapter uses Recreate so old and
// new pods never consume for the same source at once, and its termination
// grace covers the adapter's shutdown drain.
func TestMakeAdapterDeployment_SafeRollout(t *testing.T) {
	d := MakeAdapterDeployment(baseSource(), sinkURL(t), testImage)
	if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Errorf("Strategy.Type = %q, want Recreate", d.Spec.Strategy.Type)
	}
	if d.Spec.Strategy.RollingUpdate != nil {
		t.Errorf("Strategy.RollingUpdate = %+v, want nil with Recreate", d.Spec.Strategy.RollingUpdate)
	}
	g := d.Spec.Template.Spec.TerminationGracePeriodSeconds
	if g == nil || *g != AdapterTerminationGracePeriodSeconds {
		t.Fatalf("TerminationGracePeriodSeconds = %v, want %d", g, AdapterTerminationGracePeriodSeconds)
	}
	// 25s default drain plus the 10s confirmation budget must fit.
	if *g < 35 {
		t.Errorf("TerminationGracePeriodSeconds = %d, too short for the adapter drain", *g)
	}
}

// TestMakeAdapterDeployment_SourceIDEnv: the adapter gets the source UID so it
// can identify itself to the server.
func TestMakeAdapterDeployment_SourceIDEnv(t *testing.T) {
	d := MakeAdapterDeployment(baseSource(), sinkURL(t), testImage)
	env := d.Spec.Template.Spec.Containers[0].Env
	if got := envByName(t, env, "YIPYAP_SOURCE_ID").Value; got != testUID {
		t.Errorf("YIPYAP_SOURCE_ID = %q, want %q", got, testUID)
	}
}

// TestMakeAdapterDeployment_PodSecurityRestricted checks the fields the Pod
// Security Standards "restricted" profile requires, so the adapter is
// admitted in namespaces that enforce it.
func TestMakeAdapterDeployment_PodSecurityRestricted(t *testing.T) {
	dep := MakeAdapterDeployment(baseSource(), sinkURL(t), testImage)
	ps := dep.Spec.Template.Spec

	if ps.HostNetwork || ps.HostPID || ps.HostIPC {
		t.Error("host namespaces must not be shared")
	}
	psc := ps.SecurityContext
	if psc == nil {
		t.Fatal("pod securityContext is nil")
	}
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Error("pod runAsNonRoot must be true")
	}
	if psc.RunAsUser != nil && *psc.RunAsUser == 0 {
		t.Error("pod runAsUser must not be 0")
	}
	if psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod seccompProfile = %+v, want RuntimeDefault", psc.SeccompProfile)
	}
	for _, v := range ps.Volumes {
		if v.EmptyDir == nil {
			t.Errorf("volume %q is not an emptyDir; restricted allows only a fixed set of volume types", v.Name)
		}
	}

	for _, c := range append(append([]corev1.Container{}, ps.InitContainers...), ps.Containers...) {
		sc := c.SecurityContext
		if sc == nil {
			t.Fatalf("container %q securityContext is nil", c.Name)
		}
		if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
			t.Errorf("container %q allowPrivilegeEscalation must be false", c.Name)
		}
		if sc.Privileged != nil && *sc.Privileged {
			t.Errorf("container %q must not be privileged", c.Name)
		}
		if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
			t.Errorf("container %q readOnlyRootFilesystem must be true", c.Name)
		}
		if sc.RunAsUser != nil && *sc.RunAsUser == 0 {
			t.Errorf("container %q runAsUser must not be 0", c.Name)
		}
		if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
			t.Errorf("container %q capabilities.drop = %+v, want [ALL]", c.Name, sc.Capabilities)
		} else if len(sc.Capabilities.Add) != 0 {
			t.Errorf("container %q adds capabilities %v", c.Name, sc.Capabilities.Add)
		}
		if sc.SeccompProfile != nil && sc.SeccompProfile.Type == corev1.SeccompProfileTypeUnconfined {
			t.Errorf("container %q seccompProfile must not be Unconfined", c.Name)
		}
		for _, p := range c.Ports {
			if p.HostPort != 0 {
				t.Errorf("container %q uses hostPort %d", c.Name, p.HostPort)
			}
		}
	}

	// With a read-only root filesystem the cursor must land on a writable
	// volume. The adapter's default YIPYAP_CURSOR_PATH sits under this mount.
	mounts := ps.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].MountPath != CursorMountPath || mounts[0].ReadOnly {
		t.Errorf("cursor mount = %+v, want writable %s", mounts, CursorMountPath)
	}
}

func TestMakeAdapterDeployment_NoServiceAccountToken(t *testing.T) {
	dep := MakeAdapterDeployment(baseSource(), sinkURL(t), testImage)
	am := dep.Spec.Template.Spec.AutomountServiceAccountToken
	if am == nil || *am {
		t.Fatalf("automountServiceAccountToken = %v, want false", am)
	}
	if got := dep.Spec.Template.Spec.ServiceAccountName; got != "" {
		t.Errorf("serviceAccountName = %q, want unset (namespace default SA)", got)
	}
}

func TestMakeAdapterDeployment_Resources(t *testing.T) {
	dep := MakeAdapterDeployment(baseSource(), sinkURL(t), testImage)
	res := dep.Spec.Template.Spec.Containers[0].Resources
	checks := []struct {
		name string
		list corev1.ResourceList
		key  corev1.ResourceName
		want string
	}{
		{"cpu request", res.Requests, corev1.ResourceCPU, "10m"},
		{"memory request", res.Requests, corev1.ResourceMemory, "32Mi"},
		{"memory limit", res.Limits, corev1.ResourceMemory, "128Mi"},
	}
	for _, c := range checks {
		q, ok := c.list[c.key]
		if !ok {
			t.Errorf("%s missing", c.name)
			continue
		}
		if q.String() != c.want {
			t.Errorf("%s = %s, want %s", c.name, q.String(), c.want)
		}
	}
	if _, ok := res.Limits[corev1.ResourceCPU]; ok {
		t.Error("cpu limit set; the adapter should not be throttled")
	}
}

// Severity and monitor filters reach the adapter as comma-separated env vars.
func TestMakeAdapterDeployment_SeverityAndMonitorFilterEnv(t *testing.T) {
	src := baseSource()
	src.Spec.Filter = &v1alpha1.YipYapSourceFilter{
		Severities: []string{"critical", "warning"},
		MonitorIDs: []string{"mon-1", "mon-2"},
	}
	c := MakeAdapterDeployment(src, sinkURL(t), testImage).Spec.Template.Spec.Containers[0]
	if got, want := envByName(t, c.Env, "YIPYAP_FILTER_SEVERITIES").Value, "critical,warning"; got != want {
		t.Errorf("YIPYAP_FILTER_SEVERITIES: got %q, want %q", got, want)
	}
	if got, want := envByName(t, c.Env, "YIPYAP_FILTER_MONITOR_IDS").Value, "mon-1,mon-2"; got != want {
		t.Errorf("YIPYAP_FILTER_MONITOR_IDS: got %q, want %q", got, want)
	}
	envAbsent(t, c.Env, "YIPYAP_EVENT_FILTER")
}

// Empty severity and monitor lists render no env vars.
func TestMakeAdapterDeployment_EmptySeverityAndMonitorFilterSkipsEnv(t *testing.T) {
	src := baseSource()
	src.Spec.Filter = &v1alpha1.YipYapSourceFilter{Types: []string{"run.yipyap.alert.*"}}
	c := MakeAdapterDeployment(src, sinkURL(t), testImage).Spec.Template.Spec.Containers[0]
	envAbsent(t, c.Env, "YIPYAP_FILTER_SEVERITIES")
	envAbsent(t, c.Env, "YIPYAP_FILTER_MONITOR_IDS")
}

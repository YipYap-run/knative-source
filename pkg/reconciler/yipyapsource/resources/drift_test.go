/*
Copyright 2026 The YipYap Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package resources_test

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	"github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/resources"
	rttesting "github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/testing"
)

const restartedAt = "2026-11-24T10:00:00Z"

func renderAdapter(t *testing.T, mutate func(*v1alpha1.YipYapSourceSpec), image string) *appsv1.Deployment {
	t.Helper()
	spec := v1alpha1.YipYapSourceSpec{
		APIKeyRef: v1alpha1.APIKeyReference{Name: "key", Key: "api-key"},
		BaseURL:   "https://console.yipyap.run",
		Mode:      v1alpha1.YipYapSourceModeAuto,
	}
	if mutate != nil {
		mutate(&spec)
	}
	src := rttesting.NewYipYapSource("src", "ns",
		rttesting.WithYipYapSourceUID("uid-1"),
		rttesting.WithYipYapSourceSpec(spec),
	)
	sink, err := apis.ParseURL("http://broker.ns.svc/default")
	if err != nil {
		t.Fatal(err)
	}
	return resources.MakeAdapterDeployment(src, sink, image)
}

// liveCopy models the informer's view of a Deployment created from dep and
// then restarted with kubectl rollout restart.
func liveCopy(dep *appsv1.Deployment) *appsv1.Deployment {
	return rttesting.WithRestartedAt(rttesting.WithServerDefaults(dep), restartedAt)
}

func TestNeedsUpdateIgnoresDefaultsAndForeignAnnotations(t *testing.T) {
	expected := renderAdapter(t, nil, "img:v1")
	current := liveCopy(expected)
	// Quantities that read differently but mean the same are not drift.
	current.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("131072Ki")
	current.Spec.Template.Labels["team"] = "ops"

	if resources.NeedsUpdate(expected, current) {
		t.Fatal("NeedsUpdate = true for a server-defaulted, restarted copy of the rendered Deployment")
	}
}

func TestNeedsUpdateDefaultProcMountIsNotDrift(t *testing.T) {
	expected := renderAdapter(t, nil, "img:v1")
	current := liveCopy(expected)
	pm := corev1.DefaultProcMount
	current.Spec.Template.Spec.Containers[0].SecurityContext.ProcMount = &pm
	if resources.NeedsUpdate(expected, current) {
		t.Fatal("procMount Default should match an unset procMount")
	}
}

func TestNeedsUpdateUnsetServiceAccountMatchesDefault(t *testing.T) {
	expected := renderAdapter(t, nil, "img:v1")
	current := liveCopy(expected)
	current.Spec.Template.Spec.ServiceAccountName = "default"
	if resources.NeedsUpdate(expected, current) {
		t.Fatal("unset serviceAccountName should match \"default\"")
	}
}

func TestNeedsUpdateDetectsOwnedChanges(t *testing.T) {
	base := renderAdapter(t, nil, "img:v1")
	cases := map[string]struct {
		expected *appsv1.Deployment
		edit     func(*appsv1.Deployment)
	}{
		"image changed": {expected: renderAdapter(t, nil, "img:v2")},
		"env var changed": {expected: renderAdapter(t, func(s *v1alpha1.YipYapSourceSpec) {
			s.Mode = v1alpha1.YipYapSourceModePoll
		}, "img:v1")},
		"env var added": {expected: renderAdapter(t, func(s *v1alpha1.YipYapSourceSpec) {
			s.Filter = &v1alpha1.YipYapSourceFilter{Types: []string{"run.yipyap.alert.*"}}
		}, "img:v1")},
		"service account changed": {expected: renderAdapter(t, func(s *v1alpha1.YipYapSourceSpec) {
			s.ServiceAccountName = "pinned"
		}, "img:v1")},
		"image pull secret now expected": {
			expected: func() *appsv1.Deployment {
				d := renderAdapter(t, nil, "img:v1")
				d.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "regcred"}}
				return d
			}(),
		},
		"image pull secret added on live copy": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "other"}}
		}},
		"token automount enabled on live copy": {edit: func(d *appsv1.Deployment) {
			v := true
			d.Spec.Template.Spec.AutomountServiceAccountToken = &v
		}},
		"pod security context removed": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{}
		}},
		"container security context loosened": {edit: func(d *appsv1.Deployment) {
			v := false
			d.Spec.Template.Spec.Containers[0].SecurityContext.ReadOnlyRootFilesystem = &v
		}},
		"container made privileged": {edit: func(d *appsv1.Deployment) {
			v := true
			d.Spec.Template.Spec.Containers[0].SecurityContext.Privileged = &v
		}},
		"container runs as root": {edit: func(d *appsv1.Deployment) {
			var uid int64
			d.Spec.Template.Spec.Containers[0].SecurityContext.RunAsUser = &uid
		}},
		"pod runs as root": {edit: func(d *appsv1.Deployment) {
			var uid int64
			d.Spec.Template.Spec.SecurityContext.RunAsUser = &uid
		}},
		"capability added": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers[0].SecurityContext.Capabilities.Add = []corev1.Capability{"NET_ADMIN"}
		}},
		"host network enabled": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.HostNetwork = true
		}},
		"host PID enabled": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.HostPID = true
		}},
		"host IPC enabled": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.HostIPC = true
		}},
		"hostPath volume added": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Volumes = append(d.Spec.Template.Spec.Volumes, corev1.Volume{
				Name:         "host",
				VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}},
			})
		}},
		"init container added": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "init", Image: "x"}}
		}},
		"memory limit changed": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("1Gi")
		}},
		"probe path changed": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Path = "/other"
		}},
		"volume removed": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Volumes = nil
		}},
		"sidecar added": {edit: func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers = append(d.Spec.Template.Spec.Containers, corev1.Container{Name: "extra", Image: "x"})
		}},
		"owned label removed": {edit: func(d *appsv1.Deployment) {
			delete(d.Spec.Template.Labels, resources.SourceLabelKey)
		}},
		"replicas scaled": {edit: func(d *appsv1.Deployment) {
			n := int32(3)
			d.Spec.Replicas = &n
		}},
		"strategy changed": {edit: func(d *appsv1.Deployment) {
			d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			expected := tc.expected
			if expected == nil {
				expected = base
			}
			current := liveCopy(base)
			if tc.edit != nil {
				tc.edit(current)
			}
			if !resources.NeedsUpdate(expected, current) {
				t.Fatal("NeedsUpdate = false, want true")
			}
		})
	}
}

func TestMergeForUpdateKeepsForeignTemplateMetadata(t *testing.T) {
	current := liveCopy(renderAdapter(t, nil, "img:v1"))
	current.Spec.Template.Labels["team"] = "ops"
	expected := renderAdapter(t, nil, "img:v2")

	updated := resources.MergeForUpdate(expected, current)

	if got := updated.Spec.Template.Annotations[rttesting.RestartedAtAnnotation]; got != restartedAt {
		t.Errorf("restartedAt annotation = %q, want %q", got, restartedAt)
	}
	if got := updated.Spec.Template.Labels["team"]; got != "ops" {
		t.Errorf("foreign template label = %q, want ops", got)
	}
	if got := updated.Spec.Template.Spec.Containers[0].Image; got != "img:v2" {
		t.Errorf("image = %q, want img:v2", got)
	}
	if updated.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || updated.Spec.Strategy.RollingUpdate != nil {
		t.Errorf("strategy = %+v, want Recreate with no rollingUpdate", updated.Spec.Strategy)
	}
	if updated.ResourceVersion != current.ResourceVersion {
		t.Errorf("resourceVersion = %q, want %q kept for optimistic concurrency", updated.ResourceVersion, current.ResourceVersion)
	}
	if resources.NeedsUpdate(expected, updated) {
		t.Error("NeedsUpdate is still true after MergeForUpdate")
	}
	// The input must not be mutated (it comes from the informer cache).
	if current.Spec.Template.Spec.Containers[0].Image != "img:v1" {
		t.Error("MergeForUpdate mutated current")
	}
}

func TestNeedsUpdateOIDCSinkServerDefaultsAreNotDrift(t *testing.T) {
	src := rttesting.NewYipYapSource("src", "ns",
		rttesting.WithYipYapSourceUID("uid-1"),
		rttesting.WithYipYapSourceSpec(v1alpha1.YipYapSourceSpec{
			APIKeyRef: v1alpha1.APIKeyReference{Name: "key", Key: "api-key"},
			BaseURL:   "https://console.yipyap.run",
			Mode:      v1alpha1.YipYapSourceModeAuto,
		}),
	)
	sink, err := apis.ParseURL("https://broker.ns.svc/default")
	if err != nil {
		t.Fatal(err)
	}
	aud := "broker-audience"
	expected := resources.MakeAdapterDeploymentForSink(src, &duckv1.Addressable{URL: sink, Audience: &aud}, "img:v1")
	if resources.NeedsUpdate(expected, liveCopy(expected)) {
		t.Fatal("NeedsUpdate = true for a server-defaulted copy of an OIDC sink Deployment")
	}

	// A new audience must reach the live Deployment.
	other := "other-audience"
	changed := resources.MakeAdapterDeploymentForSink(src, &duckv1.Addressable{URL: sink, Audience: &other}, "img:v1")
	if !resources.NeedsUpdate(changed, liveCopy(expected)) {
		t.Fatal("NeedsUpdate = false after the sink audience changed")
	}
}

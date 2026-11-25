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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

// NeedsUpdate reports whether the live adapter Deployment differs from the
// rendered one in a field the controller sets. Fields the API server
// defaults (imagePullPolicy, terminationMessagePath, probe timings,
// dnsPolicy and so on) and metadata other tools add (such as the
// kubectl.kubernetes.io/restartedAt template annotation) are ignored, so an
// unchanged source causes no Update and does not undo a rollout restart.
//
// Any field MakeAdapterDeployment starts setting must be compared here too,
// or a change to it never reaches existing Deployments.
func NeedsUpdate(expected, current *appsv1.Deployment) bool {
	if !equality.Semantic.DeepEqual(expected.Spec.Replicas, current.Spec.Replicas) ||
		expected.Spec.Strategy.Type != current.Spec.Strategy.Type {
		return true
	}
	if !mapContains(current.Spec.Template.Labels, expected.Spec.Template.Labels) ||
		!mapContains(current.Spec.Template.Annotations, expected.Spec.Template.Annotations) {
		return true
	}
	return podSpecDiffers(&expected.Spec.Template.Spec, &current.Spec.Template.Spec)
}

// MergeForUpdate returns a copy of current carrying the controller-owned
// fields of expected. The pod spec is replaced, while template labels and
// annotations from other writers are kept.
func MergeForUpdate(expected, current *appsv1.Deployment) *appsv1.Deployment {
	updated := current.DeepCopy()
	tmpl := expected.Spec.Template.DeepCopy()
	tmpl.Labels = mergeMaps(current.Spec.Template.Labels, expected.Spec.Template.Labels)
	tmpl.Annotations = mergeMaps(current.Spec.Template.Annotations, expected.Spec.Template.Annotations)
	updated.Spec.Template = *tmpl
	updated.Spec.Replicas = expected.Spec.Replicas
	// Switching an existing RollingUpdate Deployment to Recreate must also
	// drop its rollingUpdate parameters, which the API server rejects
	// alongside Recreate.
	updated.Spec.Strategy = expected.Spec.Strategy
	return updated
}

func podSpecDiffers(want, got *corev1.PodSpec) bool {
	if !sameServiceAccount(want.ServiceAccountName, got.ServiceAccountName) ||
		!equality.Semantic.DeepEqual(want.AutomountServiceAccountToken, got.AutomountServiceAccountToken) ||
		!equality.Semantic.DeepEqual(want.TerminationGracePeriodSeconds, got.TerminationGracePeriodSeconds) ||
		!equality.Semantic.DeepEqual(podSecurity(want.SecurityContext), podSecurity(got.SecurityContext)) ||
		want.HostNetwork != got.HostNetwork ||
		want.HostPID != got.HostPID ||
		want.HostIPC != got.HostIPC ||
		!equality.Semantic.DeepEqual(want.Volumes, got.Volumes) ||
		!equality.Semantic.DeepEqual(want.ImagePullSecrets, got.ImagePullSecrets) ||
		len(want.InitContainers) != len(got.InitContainers) ||
		len(want.Containers) != len(got.Containers) {
		return true
	}
	for i := range want.Containers {
		if containerDiffers(&want.Containers[i], &got.Containers[i]) {
			return true
		}
	}
	return false
}

func containerDiffers(want, got *corev1.Container) bool {
	return want.Name != got.Name ||
		want.Image != got.Image ||
		!equality.Semantic.DeepEqual(want.Command, got.Command) ||
		!equality.Semantic.DeepEqual(want.Args, got.Args) ||
		!equality.Semantic.DeepEqual(want.Env, got.Env) ||
		!equality.Semantic.DeepEqual(want.Ports, got.Ports) ||
		!equality.Semantic.DeepEqual(want.VolumeMounts, got.VolumeMounts) ||
		!equality.Semantic.DeepEqual(want.Resources, got.Resources) ||
		!equality.Semantic.DeepEqual(containerSecurity(want.SecurityContext), containerSecurity(got.SecurityContext)) ||
		!equality.Semantic.DeepEqual(probeHandler(want.LivenessProbe), probeHandler(got.LivenessProbe)) ||
		!equality.Semantic.DeepEqual(probeHandler(want.ReadinessProbe), probeHandler(got.ReadinessProbe))
}

// sameServiceAccount treats an unset name and "default" as the same
// identity, since both run the pod as the namespace default SA.
func sameServiceAccount(want, got string) bool {
	norm := func(s string) string {
		if s == "" {
			return "default"
		}
		return s
	}
	return norm(want) == norm(got)
}

// podSecurity returns the pod security context for comparison, with nil
// read as empty (the API server fills in an empty context). The whole
// context is compared, not only the fields MakeAdapterDeployment sets, so an
// edit such as runAsUser: 0 on the live Deployment is reverted.
func podSecurity(sc *corev1.PodSecurityContext) corev1.PodSecurityContext {
	if sc == nil {
		return corev1.PodSecurityContext{}
	}
	return *sc
}

// containerSecurity returns the container security context for comparison,
// with nil read as empty and procMount "Default" read as unset. The whole
// context is compared so that privileged: true, added capabilities or a root
// user on the live Deployment are reverted.
func containerSecurity(sc *corev1.SecurityContext) corev1.SecurityContext {
	if sc == nil {
		return corev1.SecurityContext{}
	}
	out := *sc
	if out.ProcMount != nil && *out.ProcMount == corev1.DefaultProcMount {
		out.ProcMount = nil
	}
	return out
}

// probeHandler drops the probe timings, which the API server defaults.
func probeHandler(p *corev1.Probe) *corev1.ProbeHandler {
	if p == nil {
		return nil
	}
	return &p.ProbeHandler
}

// mapContains reports whether every key of want is in got with the same value.
func mapContains(got, want map[string]string) bool {
	for k, v := range want {
		if gv, ok := got[k]; !ok || gv != v {
			return false
		}
	}
	return true
}

// mergeMaps returns base with overlay applied, or nil when both are empty.
func mergeMaps(base, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

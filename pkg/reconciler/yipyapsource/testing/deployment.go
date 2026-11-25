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

package testing

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// RestartedAtAnnotation is the pod template annotation kubectl rollout
// restart sets.
const RestartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"

// WithServerDefaults returns a copy of dep with the fields the API server
// fills in on create, as the informer cache would hold it.
func WithServerDefaults(dep *appsv1.Deployment) *appsv1.Deployment {
	out := dep.DeepCopy()
	i32 := func(v int32) *int32 { return &v }
	pct := intstr.FromString("25%")

	out.ResourceVersion = "1"
	out.Generation = 1
	out.Spec.RevisionHistoryLimit = i32(10)
	out.Spec.ProgressDeadlineSeconds = i32(600)
	if out.Spec.Strategy.Type == "" {
		out.Spec.Strategy.Type = appsv1.RollingUpdateDeploymentStrategyType
	}
	if out.Spec.Strategy.Type == appsv1.RollingUpdateDeploymentStrategyType && out.Spec.Strategy.RollingUpdate == nil {
		out.Spec.Strategy.RollingUpdate = &appsv1.RollingUpdateDeployment{MaxUnavailable: &pct, MaxSurge: &pct}
	}

	ps := &out.Spec.Template.Spec
	ps.RestartPolicy = corev1.RestartPolicyAlways
	ps.DNSPolicy = corev1.DNSClusterFirst
	ps.SchedulerName = corev1.DefaultSchedulerName
	if ps.SecurityContext == nil {
		ps.SecurityContext = &corev1.PodSecurityContext{}
	}
	if ps.ServiceAccountName != "" {
		ps.DeprecatedServiceAccount = ps.ServiceAccountName
	}
	for i := range ps.Volumes {
		if pv := ps.Volumes[i].Projected; pv != nil {
			if pv.DefaultMode == nil {
				pv.DefaultMode = i32(0o644)
			}
			for j := range pv.Sources {
				if t := pv.Sources[j].ServiceAccountToken; t != nil && t.ExpirationSeconds == nil {
					exp := int64(3600)
					t.ExpirationSeconds = &exp
				}
			}
		}
	}
	for i := range ps.Containers {
		c := &ps.Containers[i]
		c.ImagePullPolicy = corev1.PullIfNotPresent
		c.TerminationMessagePath = corev1.TerminationMessagePathDefault
		c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
		for _, p := range []*corev1.Probe{c.LivenessProbe, c.ReadinessProbe} {
			if p == nil {
				continue
			}
			p.TimeoutSeconds = 1
			p.PeriodSeconds = 10
			p.SuccessThreshold = 1
			p.FailureThreshold = 3
		}
		for j := range c.Ports {
			if c.Ports[j].Protocol == "" {
				c.Ports[j].Protocol = corev1.ProtocolTCP
			}
		}
	}
	return out
}

// WithRestartedAt returns a copy of dep whose pod template carries the
// annotation kubectl rollout restart adds.
func WithRestartedAt(dep *appsv1.Deployment, at string) *appsv1.Deployment {
	out := dep.DeepCopy()
	if out.Spec.Template.Annotations == nil {
		out.Spec.Template.Annotations = map[string]string{}
	}
	out.Spec.Template.Annotations[RestartedAtAnnotation] = at
	return out
}

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

// Package resources renders the adapter Deployment that a YipYapSource's
// controller manages.
package resources

import (
	"encoding/json"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"knative.dev/pkg/apis"
	"knative.dev/pkg/kmeta"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
)

const (
	// SourceLabelKey labels the adapter Deployment with the owning YipYapSource name.
	SourceLabelKey = "sources.yipyap.run/source"

	// ContainerName is the receive-adapter container name inside the Deployment pod.
	// Tests and drift detection rely on this being stable.
	ContainerName = "source"

	// CursorVolumeName / CursorMountPath back the adapter's poll-cursor state.
	CursorVolumeName = "cursor"
	CursorMountPath  = "/var/run/yipyap"

	// AdapterHTTPPort is the port the adapter exposes /healthz and /readyz on.
	AdapterHTTPPort int32 = 8080
)

// Labels returns the immutable label set applied to the adapter Deployment and
// its pod template. Kept separate so the controller can reuse it for
// selector/matchLabels/informer filters.
func Labels(src *v1alpha1.YipYapSource) map[string]string {
	return map[string]string{
		SourceLabelKey: src.Name,
	}
}

// MakeAdapterDeployment renders the adapter Deployment for a YipYapSource,
// pointing it at the resolved sink URI and configured adapter image. The
// returned Deployment is fully owned by the source (via a controller owner
// reference), so Kubernetes GC will clean it up when the CR is deleted.
func MakeAdapterDeployment(src *v1alpha1.YipYapSource, sink *apis.URL, image string) *appsv1.Deployment {
	replicas := int32(1)
	labels := Labels(src)

	env := []corev1.EnvVar{
		{
			Name:  "K_SINK",
			Value: sink.String(),
		},
		{
			Name:  "YIPYAP_BASE_URL",
			Value: src.Spec.BaseURL,
		},
		{
			Name: "YIPYAP_API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: src.Spec.APIKeyRef.Name,
					},
					Key: src.Spec.APIKeyRef.Key,
				},
			},
		},
		{
			Name:  "YIPYAP_MODE",
			Value: string(src.Spec.Mode),
		},
	}

	if src.Spec.Filter != nil && len(src.Spec.Filter.Types) > 0 {
		// Forward every type glob as a comma-separated list. The adapter
		// splits these back out and sends one filter query param per type,
		// which the alerts server OR-matches. Type globs (e.g.
		// "run.yipyap.alert.*") never contain commas, so CSV is unambiguous.
		env = append(env, corev1.EnvVar{
			Name:  "YIPYAP_EVENT_FILTER",
			Value: strings.Join(src.Spec.Filter.Types, ","),
		})
	}

	if src.Spec.CloudEventOverrides != nil && len(src.Spec.CloudEventOverrides.Extensions) > 0 {
		if v, err := json.Marshal(struct {
			Extensions map[string]string `json:"extensions"`
		}{Extensions: src.Spec.CloudEventOverrides.Extensions}); err == nil {
			env = append(env, corev1.EnvVar{
				Name:  "K_CE_OVERRIDES",
				Value: string(v),
			})
		}
	}

	probeHandler := corev1.ProbeHandler{
		HTTPGet: &corev1.HTTPGetAction{
			Port:   intstr.FromInt(int(AdapterHTTPPort)),
			Scheme: corev1.URISchemeHTTP,
		},
	}
	liveness := probeHandler.DeepCopy()
	liveness.HTTPGet.Path = "/healthz"
	readiness := probeHandler.DeepCopy()
	readiness.HTTPGet.Path = "/readyz"

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  ContainerName,
			Image: image,
			Ports: []corev1.ContainerPort{{
				Name:          "http",
				ContainerPort: AdapterHTTPPort,
				Protocol:      corev1.ProtocolTCP,
			}},
			Env: env,
			VolumeMounts: []corev1.VolumeMount{{
				Name:      CursorVolumeName,
				MountPath: CursorMountPath,
			}},
			LivenessProbe:  &corev1.Probe{ProbeHandler: *liveness},
			ReadinessProbe: &corev1.Probe{ProbeHandler: *readiness},
		}},
		Volumes: []corev1.Volume{{
			Name: CursorVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		}},
	}
	if src.Spec.ServiceAccountName != "" {
		podSpec.ServiceAccountName = src.Spec.ServiceAccountName
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: src.Namespace,
			Name:      kmeta.ChildName(src.Name+"-source-", string(src.UID)),
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				*kmeta.NewControllerRef(src),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: podSpec,
			},
		},
	}
}

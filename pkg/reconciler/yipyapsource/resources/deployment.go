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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	"knative.dev/pkg/kmeta"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
)

const (
	// SourceLabelKey labels the adapter Deployment with the owning YipYapSource
	// name. The value is the name itself when it fits in a label value (63
	// characters), otherwise a truncated name plus a hash (see LabelValue).
	SourceLabelKey = "sources.yipyap.run/source"

	// ContainerName is the receive-adapter container name inside the Deployment pod.
	// Tests and drift detection rely on this being stable.
	ContainerName = "source"

	// CursorVolumeName / CursorMountPath back the adapter's poll-cursor state.
	CursorVolumeName = "cursor"
	CursorMountPath  = "/var/run/yipyap"

	// AdapterHTTPPort is the port the adapter exposes /healthz and /readyz on.
	AdapterHTTPPort int32 = 8080

	// AdapterTerminationGracePeriodSeconds gives the adapter time after
	// SIGTERM to finish delivering the in-flight event (its default
	// YIPYAP_SHUTDOWN_GRACE is 25s) and confirm it to the server (10s
	// budget), with slack for process exit.
	AdapterTerminationGracePeriodSeconds int64 = 40

	// EnvCACerts carries the sink's PEM CA bundle (Addressable.CACerts) to
	// the adapter. Same name as the Knative eventing adapter framework.
	EnvCACerts = "K_CA_CERTS"

	// EnvAudience carries the sink's OIDC audience (Addressable.Audience).
	// Same name as the Knative eventing adapter framework.
	EnvAudience = "K_AUDIENCE"

	// EnvSinkTokenPath points the adapter at the projected token it sends
	// as a bearer token to an OIDC sink.
	EnvSinkTokenPath = "YIPYAP_SINK_TOKEN_PATH"

	// OIDCTokenVolumeName / OIDCTokenMountPath / OIDCTokenFile hold the
	// projected ServiceAccount token for an OIDC sink.
	OIDCTokenVolumeName = "oidc-token"
	OIDCTokenMountPath  = "/var/run/secrets/yipyap/oidc"
	OIDCTokenFile       = "token"

	// OIDCTokenExpirationSeconds is the projected token lifetime. The
	// kubelet rotates the file at 80% of it, and the adapter reads the file
	// on every send, so it always presents a current token.
	OIDCTokenExpirationSeconds int64 = 3600

	// oidcServiceAccountSuffix follows the Knative eventing naming for
	// per-source OIDC identities: <name>-oidc-<group>-<kind>.
	oidcServiceAccountSuffix = "-oidc-sources.yipyap.run-yipyapsource"
)

// Adapter container resources. The adapter holds one event at a time, so a
// small request is enough. Memory is capped so a fault cannot starve the
// node. CPU has no limit, which avoids throttling during bursts.
var (
	AdapterCPURequest    = resource.MustParse("10m")
	AdapterMemoryRequest = resource.MustParse("32Mi")
	AdapterMemoryLimit   = resource.MustParse("128Mi")
)

// Labels returns the immutable label set applied to the adapter Deployment and
// its pod template. Kept separate so the controller can reuse it for
// selector/matchLabels/informer filters.
func Labels(src *v1alpha1.YipYapSource) map[string]string {
	return map[string]string{
		SourceLabelKey: LabelValue(src.Name),
	}
}

// LabelValue bounds a source name to the 63-character label value limit.
// Object names may be up to 253 characters, so a long name is cut and
// suffixed with a hash of the full name (kmeta.ChildName), which keeps values
// unique per name. Names of 63 characters or less are returned unchanged, so
// existing adapter Deployments keep their selectors.
func LabelValue(name string) string {
	return kmeta.ChildName(name, "")
}

// DeploymentName is the name of the adapter Deployment for src.
func DeploymentName(src *v1alpha1.YipYapSource) string {
	return kmeta.ChildName(src.Name+"-source-", string(src.UID))
}

// OIDCServiceAccountName is the ServiceAccount the controller creates for
// src when its sink has an OIDC audience and spec.serviceAccountName is
// unset. A dedicated account keeps the sink's EventPolicy from having to
// trust the namespace's default ServiceAccount, which every pod shares.
func OIDCServiceAccountName(src *v1alpha1.YipYapSource) string {
	return kmeta.ChildName(src.Name, oidcServiceAccountSuffix)
}

// AdapterServiceAccountName is the ServiceAccount the adapter pod runs as
// for the resolved sink. With an OIDC audience it is always the generated
// per-source account, even when spec.serviceAccountName is set: the sink
// audience is chosen by whoever controls spec.sink (or the sink's
// Addressable), and a projected token for an audience the API server
// accepts would hand that party an API token for the named account. The
// generated account has no RBAC, so that token carries nothing beyond what
// every ServiceAccount in the namespace already has. The reconciler copies
// the named account's imagePullSecrets onto the pod instead. Without an
// audience it is spec.serviceAccountName, or "" (the namespace default).
func AdapterServiceAccountName(src *v1alpha1.YipYapSource, sink *duckv1.Addressable) string {
	if sinkAudience(sink) != "" {
		return OIDCServiceAccountName(src)
	}
	return src.Spec.ServiceAccountName
}

// SinkHasAudience reports whether the resolved sink expects an OIDC token.
func SinkHasAudience(sink *duckv1.Addressable) bool {
	return sinkAudience(sink) != ""
}

// MakeOIDCServiceAccount renders the generated OIDC ServiceAccount for src.
// It is owned by the source, so it is removed with it. It never gets an
// API token mounted: the adapter receives only the audience-bound token
// from the projected volume.
func MakeOIDCServiceAccount(src *v1alpha1.YipYapSource) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: src.Namespace,
			Name:      OIDCServiceAccountName(src),
			Labels:    Labels(src),
			OwnerReferences: []metav1.OwnerReference{
				*kmeta.NewControllerRef(src),
			},
		},
		AutomountServiceAccountToken: ptr(false),
	}
}

func sinkAudience(sink *duckv1.Addressable) string {
	if sink == nil || sink.Audience == nil {
		return ""
	}
	return *sink.Audience
}

// MakeAdapterDeployment renders the adapter Deployment for a sink given
// only by URI (no CA certificates, no OIDC audience). See
// MakeAdapterDeploymentForSink.
func MakeAdapterDeployment(src *v1alpha1.YipYapSource, sink *apis.URL, image string) *appsv1.Deployment {
	return MakeAdapterDeploymentForSink(src, &duckv1.Addressable{URL: sink}, image)
}

// MakeAdapterDeploymentForSink renders the adapter Deployment for a
// YipYapSource, pointing it at the resolved sink address and configured
// adapter image. The sink's CA bundle goes to the adapter as K_CA_CERTS.
// When the sink has an OIDC audience, the pod gets K_AUDIENCE and a
// projected ServiceAccount token for that audience, and runs as
// AdapterServiceAccountName. The returned Deployment is fully owned by the
// source (via a controller owner reference), so Kubernetes GC will clean it
// up when the CR is deleted.
func MakeAdapterDeploymentForSink(src *v1alpha1.YipYapSource, sink *duckv1.Addressable, image string) *appsv1.Deployment {
	replicas := int32(1)
	labels := Labels(src)

	env := []corev1.EnvVar{
		{
			Name:  "K_SINK",
			Value: sink.URL.String(),
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
		{
			// The server keys delivery state (consumer and confirmed
			// cursor) on this, so two sources in one org with the same
			// filters each get every event.
			Name:  "YIPYAP_SOURCE_ID",
			Value: string(src.UID),
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
	if src.Spec.Filter != nil && len(src.Spec.Filter.Severities) > 0 {
		// Severities are a fixed enum without commas. The adapter sends one
		// severity query param per value and also filters locally (see
		// docs/delivery.md, Filters).
		env = append(env, corev1.EnvVar{
			Name:  "YIPYAP_FILTER_SEVERITIES",
			Value: strings.Join(src.Spec.Filter.Severities, ","),
		})
	}
	if src.Spec.Filter != nil && len(src.Spec.Filter.MonitorIDs) > 0 {
		// Validation rejects monitor IDs that contain a comma, so CSV is
		// unambiguous here.
		env = append(env, corev1.EnvVar{
			Name:  "YIPYAP_FILTER_MONITOR_IDS",
			Value: strings.Join(src.Spec.Filter.MonitorIDs, ","),
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

	if sink.CACerts != nil && *sink.CACerts != "" {
		env = append(env, corev1.EnvVar{Name: EnvCACerts, Value: *sink.CACerts})
	}

	volumes := []corev1.Volume{{
		Name: CursorVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}}
	mounts := []corev1.VolumeMount{{
		Name:      CursorVolumeName,
		MountPath: CursorMountPath,
	}}
	if aud := sinkAudience(sink); aud != "" {
		env = append(env,
			corev1.EnvVar{Name: EnvAudience, Value: aud},
			corev1.EnvVar{Name: EnvSinkTokenPath, Value: OIDCTokenMountPath + "/" + OIDCTokenFile},
		)
		// Mounted explicitly because automountServiceAccountToken is off.
		// The token's audience is the sink, so the API server rejects it:
		// it lets the adapter prove its identity to the sink and nothing
		// else. DefaultMode and ExpirationSeconds are set to the values the
		// API server would default, so drift detection sees no change.
		volumes = append(volumes, corev1.Volume{
			Name: OIDCTokenVolumeName,
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					DefaultMode: ptr(int32(0o644)),
					Sources: []corev1.VolumeProjection{{
						ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
							Audience:          aud,
							ExpirationSeconds: ptr(OIDCTokenExpirationSeconds),
							Path:              OIDCTokenFile,
						},
					}},
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      OIDCTokenVolumeName,
			MountPath: OIDCTokenMountPath,
			ReadOnly:  true,
		})
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

	grace := AdapterTerminationGracePeriodSeconds
	podSpec := corev1.PodSpec{
		TerminationGracePeriodSeconds: &grace,
		// The adapter never calls the Kubernetes API, so it gets no token.
		AutomountServiceAccountToken: ptr(false),
		// Meets the Pod Security Standards "restricted" profile. The image
		// runs as a numeric non-root user.
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: ptr(true),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
		Containers: []corev1.Container{{
			Name:  ContainerName,
			Image: image,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr(false),
				// The only file the adapter writes is its cursor, which
				// lives on the emptyDir at CursorMountPath.
				ReadOnlyRootFilesystem: ptr(true),
				RunAsNonRoot:           ptr(true),
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    AdapterCPURequest,
					corev1.ResourceMemory: AdapterMemoryRequest,
				},
				Limits: corev1.ResourceList{
					corev1.ResourceMemory: AdapterMemoryLimit,
				},
			},
			Ports: []corev1.ContainerPort{{
				Name:          "http",
				ContainerPort: AdapterHTTPPort,
				Protocol:      corev1.ProtocolTCP,
			}},
			Env:            env,
			VolumeMounts:   mounts,
			LivenessProbe:  &corev1.Probe{ProbeHandler: *liveness},
			ReadinessProbe: &corev1.Probe{ProbeHandler: *readiness},
		}},
		Volumes: volumes,
	}
	// Unset means the namespace's "default" ServiceAccount. Without an
	// OIDC sink no token is mounted, and the identity only matters for
	// imagePullSecrets. With one, see AdapterServiceAccountName.
	podSpec.ServiceAccountName = AdapterServiceAccountName(src, sink)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: src.Namespace,
			Name:      DeploymentName(src),
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				*kmeta.NewControllerRef(src),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			// Recreate stops the old adapter (which drains and confirms its
			// in-flight event) before the new one starts, so two adapters for
			// one source never consume at the same time.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
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

func ptr[T any](v T) *T { return &v }

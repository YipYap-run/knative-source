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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"knative.dev/pkg/apis"
	"knative.dev/pkg/apis/duck"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	"knative.dev/pkg/kmeta"
	"knative.dev/pkg/webhook/resourcesemantics"
)

// +genclient
// +genreconciler
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// YipYapSource is a Knative event source that emits YipYap CloudEvents
// (alerts, monitor state, maintenance windows) into a Knative sink.
type YipYapSource struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state of the YipYapSource (from the client).
	Spec YipYapSourceSpec `json:"spec"`

	// Status communicates the observed state of the YipYapSource (from the controller).
	// +optional
	Status YipYapSourceStatus `json:"status,omitempty"`
}

// GetGroupVersionKind returns the GroupVersionKind.
func (*YipYapSource) GetGroupVersionKind() schema.GroupVersionKind {
	return SchemeGroupVersion.WithKind("YipYapSource")
}

var (
	// Check that YipYapSource can be validated and defaulted.
	_ apis.Validatable = (*YipYapSource)(nil)
	_ apis.Defaultable = (*YipYapSource)(nil)
	// Check that we can create OwnerReferences to a YipYapSource.
	_ kmeta.OwnerRefable = (*YipYapSource)(nil)
	// Check that YipYapSource is a runtime.Object.
	_ runtime.Object = (*YipYapSource)(nil)
	// Check that YipYapSource satisfies resourcesemantics.GenericCRD.
	_ resourcesemantics.GenericCRD = (*YipYapSource)(nil)
	// Check that YipYapSource implements the Conditions duck type.
	_ = duck.VerifyType(&YipYapSource{}, &duckv1.Conditions{})
	// Check that the type conforms to the duck Knative Resource shape.
	_ duckv1.KRShaped = (*YipYapSource)(nil)
)

// YipYapSourceSpec holds the desired state of the YipYapSource (from the client).
type YipYapSourceSpec struct {
	// SourceSpec is embedded from Knative's Source duck type. It provides:
	//   - Sink (destination; duckv1.Destination)
	//   - CloudEventOverrides (extension attrs injected on every event)
	// See knative.dev/pkg/apis/duck/v1.
	duckv1.SourceSpec `json:",inline"`

	// APIKeyRef selects a Secret containing the org-scoped YipYap API key.
	// The adapter Deployment reads the key via envFrom/valueFrom.
	APIKeyRef APIKeyReference `json:"apiKeyRef"`

	// BaseURL is the yipyap console URL. Defaults to https://console.yipyap.run
	// on SaaS. Override for self-hosted FOSS deployments.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// Filter narrows the event types this source delivers.
	// +optional
	Filter *YipYapSourceFilter `json:"filter,omitempty"`

	// Mode selects the transport between the adapter and yipyap.
	// One of "auto" (default; follows server hint), "poll", or "stream".
	// +optional
	Mode YipYapSourceMode `json:"mode,omitempty"`

	// ServiceAccountName optionally pins the adapter Deployment's
	// ServiceAccount. Defaults to a controller-managed SA.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// APIKeyReference points at a Secret holding the YipYap API key.
type APIKeyReference struct {
	// Name of the Secret in the same namespace as the YipYapSource.
	Name string `json:"name"`
	// Key within the Secret whose value is the API key. Defaults to "api-key".
	// +optional
	Key string `json:"key,omitempty"`
}

// YipYapSourceFilter narrows the event stream by type, monitor, or severity.
type YipYapSourceFilter struct {
	// Types is a list of CloudEvent type globs (e.g. "run.yipyap.alert.*").
	// Empty list delivers every type.
	// +optional
	Types []string `json:"types,omitempty"`

	// MonitorIDs optionally restricts to events scoped to specific monitors.
	// Empty list delivers events from every monitor.
	// +optional
	MonitorIDs []string `json:"monitorIDs,omitempty"`

	// Severities optionally restricts alert events to the listed severities.
	// Valid values: "info", "warning", "critical".
	// Does not affect monitor/maintenance events.
	// +optional
	Severities []string `json:"severities,omitempty"`
}

// YipYapSourceMode is the transport mode between the adapter and yipyap.
// +kubebuilder:validation:Enum=auto;poll;stream
type YipYapSourceMode string

const (
	// YipYapSourceModeAuto lets the server hint decide transport.
	YipYapSourceModeAuto YipYapSourceMode = "auto"
	// YipYapSourceModePoll forces periodic polling.
	YipYapSourceModePoll YipYapSourceMode = "poll"
	// YipYapSourceModeStream forces a long-lived streaming connection.
	YipYapSourceModeStream YipYapSourceMode = "stream"
)

// YipYapSourceStatus communicates the observed state of the YipYapSource (from the controller).
type YipYapSourceStatus struct {
	// SourceStatus embeds Knative's Source duck-type status:
	//   - Status (conditions, observedGeneration)
	//   - SinkURI (resolved sink address)
	//   - CloudEventAttributes (advertised attributes for this source)
	//   - Auth (auto-generated ServiceAccount subject, when OIDC is enabled)
	duckv1.SourceStatus `json:",inline"`

	// DeploymentName is the name of the adapter Deployment managed by this
	// YipYapSource. Surfaces for operator visibility.
	// +optional
	DeploymentName string `json:"deploymentName,omitempty"`

	// LastEventTime is when the adapter most recently forwarded an event.
	// Populated via annotation feedback from the adapter; may lag briefly.
	// +optional
	LastEventTime *metav1.Time `json:"lastEventTime,omitempty"`

	// EventCount is a running total of events forwarded by this source.
	// Resets on controller restart; not authoritative for billing.
	// +optional
	EventCount int64 `json:"eventCount,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// YipYapSourceList is a list of YipYapSource resources.
type YipYapSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []YipYapSource `json:"items"`
}

// GetStatus retrieves the status of the resource. Implements the KRShaped interface.
func (ss *YipYapSource) GetStatus() *duckv1.Status {
	return &ss.Status.Status
}

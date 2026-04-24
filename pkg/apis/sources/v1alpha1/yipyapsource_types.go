/*
Copyright 2019 The Knative Authors.

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
	// inherits duck/v1 SourceSpec, which currently provides:
	// * Sink - a reference to an object that will resolve to a domain name or
	//   a URI directly to use as the sink.
	// * CloudEventOverrides - defines overrides to control the output format
	//   and modifications of the event sent to the sink.
	duckv1.SourceSpec `json:",inline"`

	// ServiceAccountName holds the name of the Kubernetes service account
	// as which the underlying K8s resources should be run. If unspecified
	// this will default to the "default" service account for the namespace
	// in which the YipYapSource exists.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Interval is the time interval between events.
	//
	// The string format is a sequence of decimal numbers, each with optional
	// fraction and a unit suffix, such as "300ms", "-1.5h" or "2h45m". Valid time
	// units are "ns", "us" (or "µs"), "ms", "s", "m", "h". If unspecified
	// this will default to "10s".
	Interval string `json:"interval"`
}

const (
	// YipYapSourceConditionReady is set when the revision is starting to materialize
	// runtime resources, and becomes true when those resources are ready.
	YipYapSourceConditionReady = apis.ConditionReady
)

// YipYapSourceStatus communicates the observed state of the YipYapSource (from the controller).
type YipYapSourceStatus struct {
	// inherits duck/v1 SourceStatus, which currently provides:
	// * ObservedGeneration - the 'Generation' of the Service that was last
	//   processed by the controller.
	// * Conditions - the latest available observations of a resource's current
	//   state.
	// * SinkURI - the current active sink URI that has been configured for the
	//   Source.
	duckv1.SourceStatus `json:",inline"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// YipYapSourceList is a list of YipYapSource resources
type YipYapSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []YipYapSource `json:"items"`
}

// GetStatus retrieves the status of the resource. Implements the KRShaped interface.
func (ss *YipYapSource) GetStatus() *duckv1.Status {
	return &ss.Status.Status
}

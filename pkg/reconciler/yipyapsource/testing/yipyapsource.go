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

// Package testing provides table-test builders for YipYapSource.
//
// The builders match the `With...` option-function idiom used by knative.dev
// reconciler table tests: construct an object with the minimum required shape
// via NewYipYapSource(name, namespace, opts...) and apply additional mutators
// to represent the state-under-test.
package testing

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
)

// YipYapSourceOption is a functional option that mutates a YipYapSource.
type YipYapSourceOption func(*v1alpha1.YipYapSource)

// NewYipYapSource returns a YipYapSource with the given name/namespace; each
// supplied option runs in order to produce the final state.
func NewYipYapSource(name, namespace string, opts ...YipYapSourceOption) *v1alpha1.YipYapSource {
	src := &v1alpha1.YipYapSource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	for _, opt := range opts {
		opt(src)
	}
	// Populate TypeMeta so that the fake clients encode/decode it correctly.
	src.SetGroupVersionKind(v1alpha1.SchemeGroupVersion.WithKind("YipYapSource"))
	return src
}

// WithYipYapSourceUID stamps a stable UID on the source. Required whenever the
// test checks the rendered Deployment name, which derives from UID via
// kmeta.ChildName.
func WithYipYapSourceUID(uid string) YipYapSourceOption {
	return func(s *v1alpha1.YipYapSource) {
		s.UID = types.UID(uid)
	}
}

// WithYipYapSourceSpec replaces the full spec.
func WithYipYapSourceSpec(spec v1alpha1.YipYapSourceSpec) YipYapSourceOption {
	return func(s *v1alpha1.YipYapSource) {
		s.Spec = spec
	}
}

// WithInitYipYapSourceConditions initializes the condition set so
// SinkProvided/Deployed/Ready are each Unknown. Controllers do this on entry
// to ReconcileKind.
func WithInitYipYapSourceConditions(s *v1alpha1.YipYapSource) {
	s.Status.InitializeConditions()
}

// WithYipYapSourceSink marks SinkProvided=True and stamps the SinkURI.
func WithYipYapSourceSink(uri *apis.URL) YipYapSourceOption {
	return func(s *v1alpha1.YipYapSource) {
		s.Status.MarkSink(uri)
	}
}

// WithYipYapSourceSinkNotFound marks SinkProvided=False with the given reason.
func WithYipYapSourceSinkNotFound(reason, msg string) YipYapSourceOption {
	return func(s *v1alpha1.YipYapSource) {
		s.Status.MarkNoSink(reason, "%s", msg)
	}
}

// WithYipYapSourceDeployed marks Deployed=True.
func WithYipYapSourceDeployed(s *v1alpha1.YipYapSource) {
	s.Status.MarkDeployed()
}

// WithYipYapSourceNotDeployed marks Deployed=False with the given reason.
func WithYipYapSourceNotDeployed(reason, msg string) YipYapSourceOption {
	return func(s *v1alpha1.YipYapSource) {
		s.Status.MarkNotDeployed(reason, "%s", msg)
	}
}

// WithYipYapSourceDeploying marks Deployed=Unknown with the given reason.
func WithYipYapSourceDeploying(reason, msg string) YipYapSourceOption {
	return func(s *v1alpha1.YipYapSource) {
		s.Status.MarkDeploying(reason, "%s", msg)
	}
}

// WithYipYapSourceDeploymentName stamps the status.deploymentName field that
// the controller surfaces after creating/updating the adapter Deployment.
func WithYipYapSourceDeploymentName(name string) YipYapSourceOption {
	return func(s *v1alpha1.YipYapSource) {
		s.Status.DeploymentName = name
	}
}

// WithYipYapSourceStatusObservedGeneration stamps observedGeneration on the
// source's status. The controller always sets this to src.Generation on entry.
func WithYipYapSourceStatusObservedGeneration(gen int64) YipYapSourceOption {
	return func(s *v1alpha1.YipYapSource) {
		s.Status.ObservedGeneration = gen
	}
}

// WithYipYapSourceCloudEventAttributes advertises the fixed list of CloudEvent
// types emitted by the adapter.
func WithYipYapSourceCloudEventAttributes(attrs []duckv1.CloudEventAttributes) YipYapSourceOption {
	return func(s *v1alpha1.YipYapSource) {
		s.Status.CloudEventAttributes = attrs
	}
}

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
	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"
)

const (
	// YipYapSourceConditionReady is the top-level readiness condition.
	YipYapSourceConditionReady = apis.ConditionReady

	// YipYapSourceConditionSinkProvided reflects whether the sink URI has
	// been successfully resolved.
	YipYapSourceConditionSinkProvided apis.ConditionType = "SinkProvided"

	// YipYapSourceConditionDeployed reflects whether the adapter Deployment
	// is Available per the Deployment controller.
	YipYapSourceConditionDeployed apis.ConditionType = "Deployed"

	// YipYapSourceConditionBaseURLAllowed reflects whether spec.baseURL
	// passes the operator policy (https, no user info, allowlisted host).
	// The controller does not run the adapter while it is False.
	YipYapSourceConditionBaseURLAllowed apis.ConditionType = "BaseURLAllowed"

	// YipYapSourceConditionConnected reflects whether the running adapter
	// can talk to the YipYap API with its API key and deliver to the sink.
	// The controller reads it from the adapter's status endpoint, so a
	// rejected key shows up as Ready=False with reason APIKeyRejected.
	YipYapSourceConditionConnected apis.ConditionType = "Connected"
)

// yipyapSourceCondSet is the set of conditions tracked on every YipYapSource.
var yipyapSourceCondSet = apis.NewLivingConditionSet(
	YipYapSourceConditionBaseURLAllowed,
	YipYapSourceConditionSinkProvided,
	YipYapSourceConditionDeployed,
	YipYapSourceConditionConnected,
)

// GetConditionSet returns the condition set (duckv1.KRShaped).
func (*YipYapSource) GetConditionSet() apis.ConditionSet {
	return yipyapSourceCondSet
}

// GetCondition returns the condition currently associated with the given type, or nil.
func (s *YipYapSourceStatus) GetCondition(t apis.ConditionType) *apis.Condition {
	return yipyapSourceCondSet.Manage(s).GetCondition(t)
}

// InitializeConditions initializes all conditions to Unknown.
func (s *YipYapSourceStatus) InitializeConditions() {
	yipyapSourceCondSet.Manage(s).InitializeConditions()
}

// IsReady returns true if the resource is ready overall.
func (s *YipYapSourceStatus) IsReady() bool {
	return yipyapSourceCondSet.Manage(s).IsHappy()
}

// MarkSink marks the source as having a resolved sink. Passing nil leaves
// the SinkProvided condition Unknown (the sink has not yet resolved).
func (s *YipYapSourceStatus) MarkSink(uri *apis.URL) {
	s.SinkURI = uri
	if uri != nil && uri.String() != "" {
		yipyapSourceCondSet.Manage(s).MarkTrue(YipYapSourceConditionSinkProvided)
	} else {
		yipyapSourceCondSet.Manage(s).MarkUnknown(YipYapSourceConditionSinkProvided,
			"SinkEmpty", "Sink has no URI")
	}
}

// MarkSinkAddress records the resolved sink address: its URI, the CA
// certificates for an https sink and the OIDC audience. Fields the address
// does not carry are cleared, so a sink that drops TLS or OIDC does not
// leave stale values behind. A nil address or one without a URI leaves
// SinkProvided Unknown, as MarkSink does.
func (s *YipYapSourceStatus) MarkSinkAddress(addr *duckv1.Addressable) {
	if addr == nil {
		s.SinkCACerts = nil
		s.SinkAudience = nil
		s.MarkSink(nil)
		return
	}
	s.SinkCACerts = nonEmpty(addr.CACerts)
	s.SinkAudience = nonEmpty(addr.Audience)
	s.MarkSink(addr.URL)
}

// MarkOIDCIdentity records the ServiceAccount whose tokens the adapter
// sends to an OIDC sink, so an EventPolicy can name this source. An empty
// name clears it (the sink has no audience).
func (s *YipYapSourceStatus) MarkOIDCIdentity(serviceAccountName string) {
	if serviceAccountName == "" {
		s.Auth = nil
		return
	}
	name := serviceAccountName
	s.Auth = &duckv1.AuthStatus{ServiceAccountName: &name}
}

func nonEmpty(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	v := *p
	return &v
}

// MarkNoSink marks the source as unable to resolve its sink.
func (s *YipYapSourceStatus) MarkNoSink(reason, format string, args ...interface{}) {
	yipyapSourceCondSet.Manage(s).MarkFalse(YipYapSourceConditionSinkProvided, reason, format, args...)
}

// MarkDeployed marks the adapter Deployment as Available.
func (s *YipYapSourceStatus) MarkDeployed() {
	yipyapSourceCondSet.Manage(s).MarkTrue(YipYapSourceConditionDeployed)
}

// MarkNotDeployed marks the adapter as not yet/no-longer available.
func (s *YipYapSourceStatus) MarkNotDeployed(reason, format string, args ...interface{}) {
	yipyapSourceCondSet.Manage(s).MarkFalse(YipYapSourceConditionDeployed, reason, format, args...)
}

// MarkDeploying marks the adapter as rolling out but not yet Available.
func (s *YipYapSourceStatus) MarkDeploying(reason, format string, args ...interface{}) {
	yipyapSourceCondSet.Manage(s).MarkUnknown(YipYapSourceConditionDeployed, reason, format, args...)
}

// MarkBaseURLAllowed marks spec.baseURL as accepted by the operator policy.
func (s *YipYapSourceStatus) MarkBaseURLAllowed() {
	yipyapSourceCondSet.Manage(s).MarkTrue(YipYapSourceConditionBaseURLAllowed)
}

// MarkBaseURLNotAllowed marks spec.baseURL as rejected by the operator policy.
func (s *YipYapSourceStatus) MarkBaseURLNotAllowed(reason, format string, args ...interface{}) {
	yipyapSourceCondSet.Manage(s).MarkFalse(YipYapSourceConditionBaseURLAllowed, reason, format, args...)
}

// MarkConnected marks the adapter as connected to the API and the sink.
func (s *YipYapSourceStatus) MarkConnected() {
	yipyapSourceCondSet.Manage(s).MarkTrue(YipYapSourceConditionConnected)
}

// MarkNotConnected marks the adapter as unable to use the API or the sink,
// for example with reason APIKeyRejected.
func (s *YipYapSourceStatus) MarkNotConnected(reason, format string, args ...interface{}) {
	yipyapSourceCondSet.Manage(s).MarkFalse(YipYapSourceConditionConnected, reason, format, args...)
}

// MarkConnecting marks the adapter's connection state as not yet known,
// for example while its pod starts.
func (s *YipYapSourceStatus) MarkConnecting(reason, format string, args ...interface{}) {
	yipyapSourceCondSet.Manage(s).MarkUnknown(YipYapSourceConditionConnected, reason, format, args...)
}

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
)

// yipyapSourceCondSet is the set of conditions tracked on every YipYapSource.
var yipyapSourceCondSet = apis.NewLivingConditionSet(
	YipYapSourceConditionSinkProvided,
	YipYapSourceConditionDeployed,
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

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
	appsv1 "k8s.io/api/apps/v1"
	"knative.dev/eventing/pkg/apis/duck"
	"knative.dev/pkg/apis"
)

const (
	// SampleConditionReady has status True when the YipYapSource is ready to send events.
	SampleConditionReady = apis.ConditionReady

	// SampleConditionSinkProvided has status True when the YipYapSource has been configured with a sink target.
	SampleConditionSinkProvided apis.ConditionType = "SinkProvided"

	// SampleConditionDeployed has status True when the YipYapSource has had it's deployment created.
	SampleConditionDeployed apis.ConditionType = "Deployed"
)

var SampleCondSet = apis.NewLivingConditionSet(
	SampleConditionSinkProvided,
	SampleConditionDeployed,
)

// GetCondition returns the condition currently associated with the given type, or nil.
func (s *YipYapSourceStatus) GetCondition(t apis.ConditionType) *apis.Condition {
	return SampleCondSet.Manage(s).GetCondition(t)
}

// InitializeConditions sets relevant unset conditions to Unknown state.
func (s *YipYapSourceStatus) InitializeConditions() {
	SampleCondSet.Manage(s).InitializeConditions()
}

// GetConditionSet returns YipYapSource ConditionSet.
func (*YipYapSource) GetConditionSet() apis.ConditionSet {
	return SampleCondSet
}

// MarkSink sets the condition that the source has a sink configured.
func (s *YipYapSourceStatus) MarkSink(uri *apis.URL) {
	s.SinkURI = uri
	if len(uri.String()) > 0 {
		SampleCondSet.Manage(s).MarkTrue(SampleConditionSinkProvided)
	} else {
		SampleCondSet.Manage(s).MarkUnknown(SampleConditionSinkProvided, "SinkEmpty", "Sink has resolved to empty.")
	}
}

// MarkNoSink sets the condition that the source does not have a sink configured.
func (s *YipYapSourceStatus) MarkNoSink(reason, messageFormat string, messageA ...interface{}) {
	SampleCondSet.Manage(s).MarkFalse(SampleConditionSinkProvided, reason, messageFormat, messageA...)
}

// PropagateDeploymentAvailability uses the availability of the provided Deployment to determine if
// SampleConditionDeployed should be marked as true or false.
func (s *YipYapSourceStatus) PropagateDeploymentAvailability(d *appsv1.Deployment) {
	if duck.DeploymentIsAvailable(&d.Status, false) {
		SampleCondSet.Manage(s).MarkTrue(SampleConditionDeployed)
	} else {
		// I don't know how to propagate the status well, so just give the name of the Deployment
		// for now.
		SampleCondSet.Manage(s).MarkFalse(SampleConditionDeployed, "DeploymentUnavailable", "The Deployment '%s' is unavailable.", d.Name)
	}
}

// IsReady returns true if the resource is ready overall.
func (s *YipYapSourceStatus) IsReady() bool {
	return SampleCondSet.Manage(s).IsHappy()
}

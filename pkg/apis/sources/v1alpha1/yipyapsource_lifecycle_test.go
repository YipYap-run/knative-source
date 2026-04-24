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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"knative.dev/pkg/apis"
)

func TestYipYapSourceStatus_InitializeConditions(t *testing.T) {
	s := &YipYapSourceStatus{}
	s.InitializeConditions()

	for _, ct := range []apis.ConditionType{
		YipYapSourceConditionReady,
		YipYapSourceConditionSinkProvided,
		YipYapSourceConditionDeployed,
	} {
		got := s.GetCondition(ct)
		if got == nil {
			t.Errorf("condition %q not initialized", ct)
			continue
		}
		if got.Status != corev1.ConditionUnknown {
			t.Errorf("condition %q status = %q, want %q", ct, got.Status, corev1.ConditionUnknown)
		}
	}
}

func TestYipYapSourceStatus_MarkSink_SetsConditionTrue(t *testing.T) {
	s := &YipYapSourceStatus{}
	s.InitializeConditions()

	uri, _ := apis.ParseURL("http://broker-ingress.knative-eventing.svc.cluster.local/ns/default")
	s.MarkSink(uri)

	if s.SinkURI == nil || s.SinkURI.String() != uri.String() {
		t.Errorf("SinkURI = %v, want %v", s.SinkURI, uri)
	}

	got := s.GetCondition(YipYapSourceConditionSinkProvided)
	if got == nil || got.Status != corev1.ConditionTrue {
		t.Errorf("SinkProvided condition = %v, want True", got)
	}
}

func TestYipYapSourceStatus_MarkSink_NilUnknown(t *testing.T) {
	s := &YipYapSourceStatus{}
	s.InitializeConditions()

	s.MarkSink(nil)

	got := s.GetCondition(YipYapSourceConditionSinkProvided)
	if got == nil || got.Status != corev1.ConditionUnknown {
		t.Errorf("SinkProvided condition = %v, want Unknown", got)
	}
}

func TestYipYapSourceStatus_MarkNoSink_FalseWithReason(t *testing.T) {
	s := &YipYapSourceStatus{}
	s.InitializeConditions()

	s.MarkNoSink("BrokerMissing", "broker %q not found", "default")

	got := s.GetCondition(YipYapSourceConditionSinkProvided)
	if got == nil {
		t.Fatal("SinkProvided condition not found")
	}
	if got.Status != corev1.ConditionFalse {
		t.Errorf("SinkProvided status = %q, want False", got.Status)
	}
	if got.Reason != "BrokerMissing" {
		t.Errorf("Reason = %q, want BrokerMissing", got.Reason)
	}
	if got.Message != `broker "default" not found` {
		t.Errorf("Message = %q, want %q", got.Message, `broker "default" not found`)
	}
}

func TestYipYapSourceStatus_MarkDeployed_SetsReadyOnce_SinkProvided(t *testing.T) {
	s := &YipYapSourceStatus{}
	s.InitializeConditions()

	// Only deployed; sink still unknown.
	s.MarkDeployed()

	ready := s.GetCondition(YipYapSourceConditionReady)
	if ready == nil {
		t.Fatal("Ready condition not found")
	}
	if ready.Status == corev1.ConditionTrue {
		t.Errorf("Ready = True, expected not-True while SinkProvided is Unknown")
	}
	if s.IsReady() {
		t.Errorf("IsReady() = true, expected false while SinkProvided is Unknown")
	}

	// Now provide sink too.
	uri, _ := apis.ParseURL("http://sink.example/")
	s.MarkSink(uri)

	if !s.IsReady() {
		t.Errorf("IsReady() = false, expected true after both conditions are True")
	}
	ready = s.GetCondition(YipYapSourceConditionReady)
	if ready.Status != corev1.ConditionTrue {
		t.Errorf("Ready = %q, want True", ready.Status)
	}
}

func TestYipYapSourceStatus_MarkNotDeployed(t *testing.T) {
	s := &YipYapSourceStatus{}
	s.InitializeConditions()

	s.MarkNotDeployed("DeploymentUnavailable", "Deployment %q is not available", "adapter")

	got := s.GetCondition(YipYapSourceConditionDeployed)
	if got == nil {
		t.Fatal("Deployed condition not found")
	}
	if got.Status != corev1.ConditionFalse {
		t.Errorf("Deployed status = %q, want False", got.Status)
	}
	if got.Reason != "DeploymentUnavailable" {
		t.Errorf("Reason = %q, want DeploymentUnavailable", got.Reason)
	}
}

func TestYipYapSourceStatus_MarkDeploying(t *testing.T) {
	s := &YipYapSourceStatus{}
	s.InitializeConditions()

	s.MarkDeploying("Rollout", "Deployment %q rolling out", "adapter")

	got := s.GetCondition(YipYapSourceConditionDeployed)
	if got == nil {
		t.Fatal("Deployed condition not found")
	}
	if got.Status != corev1.ConditionUnknown {
		t.Errorf("Deployed status = %q, want Unknown", got.Status)
	}
	if got.Reason != "Rollout" {
		t.Errorf("Reason = %q, want Rollout", got.Reason)
	}
}

func TestYipYapSourceStatus_FullLifecycle(t *testing.T) {
	s := &YipYapSourceStatus{}
	s.InitializeConditions()

	if s.IsReady() {
		t.Fatal("IsReady() true on fresh init, expected false")
	}

	uri, _ := apis.ParseURL("http://broker.example/")
	s.MarkSink(uri)
	if s.IsReady() {
		t.Errorf("IsReady() true after only MarkSink, expected false")
	}

	s.MarkDeployed()
	if !s.IsReady() {
		t.Errorf("IsReady() false after MarkSink + MarkDeployed, expected true")
	}
}

func TestYipYapSource_GetConditionSet(t *testing.T) {
	y := &YipYapSource{}
	cs := y.GetConditionSet()
	if got, want := cs.GetTopLevelConditionType(), YipYapSourceConditionReady; got != want {
		t.Errorf("top-level condition = %q, want %q", got, want)
	}
}

func TestYipYapSource_GetStatus(t *testing.T) {
	y := &YipYapSource{}
	if y.GetStatus() != &y.Status.Status {
		t.Errorf("GetStatus did not return &Status.Status")
	}
}

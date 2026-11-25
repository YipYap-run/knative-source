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

// Package adapterstatus is the contract between the receive adapter's
// status endpoint and the controller that reads it. The adapter serves a
// Status as JSON on Path; the controller copies Reason and Message onto the
// YipYapSource's Connected condition.
package adapterstatus

import (
	"strings"
	"time"
	"unicode"
)

// Path is the adapter's status endpoint, served on the same port as
// /healthz and /readyz.
const Path = "/status"

// Reasons the adapter reports when it is not ready. They are the condition
// reasons users see in `kubectl get yipyapsources`.
const (
	// ReasonAPIKeyRejected: the YipYap API answered 401 repeatedly.
	ReasonAPIKeyRejected = "APIKeyRejected"
	// ReasonAPIForbidden: the YipYap API answered 403 repeatedly.
	ReasonAPIForbidden = "APIForbidden"
	// ReasonAPIUnreachable: no successful API round trip within the window.
	ReasonAPIUnreachable = "APIUnreachable"
	// ReasonSinkUnavailable: the sink has refused or failed every delivery
	// attempt for longer than the window.
	ReasonSinkUnavailable = "SinkUnavailable"
)

// MaxMessageLen bounds Status.Message as read by the controller.
const MaxMessageLen = 256

// Status is the JSON body of the adapter's status endpoint. The endpoint
// answers 200 whether or not the adapter is ready; Ready carries the
// verdict.
type Status struct {
	Ready   bool   `json:"ready"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// LastSuccess is the last successful round trip to the YipYap API.
	LastSuccess *time.Time `json:"lastSuccess,omitempty"`
}

// KnownReason reports whether r is one of the reasons above.
func KnownReason(r string) bool {
	switch r {
	case ReasonAPIKeyRejected, ReasonAPIForbidden, ReasonAPIUnreachable, ReasonSinkUnavailable:
		return true
	}
	return false
}

// CleanMessage drops control characters and bounds m to MaxMessageLen
// runes, so a message read off the pod network is safe to put in a
// condition.
func CleanMessage(m string) string {
	var b strings.Builder
	n := 0
	for _, r := range m {
		if unicode.IsControl(r) {
			r = ' '
		}
		if n == MaxMessageLen {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

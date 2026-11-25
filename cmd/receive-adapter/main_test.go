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

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/YipYap-run/knative-source/cmd/receive-adapter/source"
	"github.com/YipYap-run/knative-source/pkg/adapterstatus"
)

// TestHealthMux_LivenessIgnoresAuthErrors: with the API key rejected,
// /readyz and /status report it while /healthz stays 200, so the kubelet
// never restarts the pod over a bad key.
func TestHealthMux_LivenessIgnoresAuthErrors(t *testing.T) {
	h := source.NewHealth(nil)
	for range source.AuthFailureThreshold {
		h.APIFailed(&source.APIStatusError{Op: "poll", StatusCode: http.StatusUnauthorized})
	}
	mux := healthMux(h, &source.Stats{})

	for path, want := range map[string]int{
		"/healthz":         http.StatusOK,
		"/readyz":          http.StatusServiceUnavailable,
		adapterstatus.Path: http.StatusOK,
		"/metrics":         http.StatusOK,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", path, rec.Code, want)
		}
	}
}

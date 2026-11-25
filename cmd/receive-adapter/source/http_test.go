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

package source

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestServerClientsRefuseRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	cfg := &Config{BaseURL: redirect.URL, APIKey: "key"}
	clients := map[string]*http.Client{
		"run":     newServerClient(time.Minute),
		"poll":    NewPollClient(cfg).httpClient,
		"stream":  NewStreamClient(cfg).httpClient,
		"confirm": newConfirmer(cfg).httpClient,
	}
	for name, client := range clients {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, redirect.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer key")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusTemporaryRedirect {
				t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusTemporaryRedirect)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("redirect target received %d requests, want 0", n)
	}
}

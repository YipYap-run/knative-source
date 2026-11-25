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

// Command stub is a stand-in YipYap API and CloudEvents sink for the kind
// smoke test in CI. It is test tooling and is never published.
//
// Environment:
//
//	STUB_API_KEY  bearer key the API accepts (required)
//	STUB_TLS_DIR  directory holding tls.crt and tls.key (default /tls)
//
// The API listens with TLS on :8443. The sink and /healthz listen on :8080.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	key := os.Getenv("STUB_API_KEY")
	if key == "" {
		log.Error("STUB_API_KEY is required")
		os.Exit(1)
	}
	dir := os.Getenv("STUB_TLS_DIR")
	if dir == "" {
		dir = "/tls"
	}

	apiSrv := &http.Server{
		Addr:              ":8443",
		Handler:           newAPI(key, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	sinkSrv := &http.Server{
		Addr:              ":8080",
		Handler:           newSink(log),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 2)
	go func() {
		errc <- apiSrv.ListenAndServeTLS(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"))
	}()
	go func() { errc <- sinkSrv.ListenAndServe() }()
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("listener stopped", "error", err)
		os.Exit(1)
	}
}

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
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YipYap-run/knative-source/cmd/receive-adapter/source"
)

const testKey = "yy_smoke_test_key"

// TestAdapterRunsAgainstStub runs the real adapter (source.Run, as the
// receive-adapter binary does) against the stub, so the stub stays in step
// with the adapter's wire contract. The smoke job depends on this exchange:
// the adapter takes the poll hint, delivers the one event to the sink, then
// confirms it with since= on the next poll. TLS is left out here; main
// serves the same handler with ListenAndServeTLS.
func TestAdapterRunsAgainstStub(t *testing.T) {
	var logs syncBuffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	apiSrv := httptest.NewServer(newAPI(testKey, log))
	defer apiSrv.Close()
	snk := newSink(log)
	sinkSrv := httptest.NewServer(snk)
	defer sinkSrv.Close()

	cfg, err := source.LoadConfig(func(k string) string {
		switch k {
		case "K_SINK":
			return sinkSrv.URL
		case "YIPYAP_API_KEY":
			return testKey
		case "YIPYAP_BASE_URL":
			return apiSrv.URL
		case "YIPYAP_SOURCE_ID":
			return "uid-1"
		case "YIPYAP_CURSOR_PATH":
			return filepath.Join(t.TempDir(), "cursor")
		case "YIPYAP_SHUTDOWN_GRACE":
			return "0s"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = source.Run(ctx, cfg)
	}()

	confirmed := "since=" + firstCursor
	for !strings.Contains(logs.String(), confirmed) {
		if ctx.Err() != nil {
			t.Fatalf("adapter never confirmed the smoke event; logs:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if got := snk.received(); !slices.Contains(got, SmokeEventID) {
		t.Errorf("sink received %v, want %s", got, SmokeEventID)
	}
	out := logs.String()
	if !strings.Contains(out, "source_id=uid-1") {
		t.Errorf("stub did not log the source ID; logs:\n%s", out)
	}
	if strings.Contains(out, logBadAuth) {
		t.Errorf("stub rejected the adapter's key; logs:\n%s", out)
	}
	if strings.Contains(out, testKey) {
		t.Error("stub logged the API key")
	}
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of two
// servers and reads from the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestAPIRejectsWrongKey(t *testing.T) {
	var logs bytes.Buffer
	h := newAPI(testKey, slog.New(slog.NewTextHandler(&logs, nil)))
	for _, auth := range []string{"", "Bearer wrong", testKey, "Bearer " + testKey + "x"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cloudevents/poll", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", auth, rec.Code)
		}
	}
	if !strings.Contains(logs.String(), logBadAuth) {
		t.Errorf("rejections not logged; logs:\n%s", logs.String())
	}
}

func TestAPIAck(t *testing.T) {
	var logs bytes.Buffer
	h := newAPI(testKey, slog.New(slog.NewTextHandler(&logs, nil)))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cloudevents/ack", strings.NewReader(`{"cursor":"c1"}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", rec.Code)
	}
	if !strings.Contains(logs.String(), "cursor=c1") {
		t.Errorf("ack not logged; logs:\n%s", logs.String())
	}
}

func TestSinkHealthAndRejectsNonEvents(t *testing.T) {
	s := httptest.NewServer(newSink(slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer s.Close()
	resp, err := http.Get(s.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz status %d, want 200", resp.StatusCode)
	}
	resp, err = http.Post(s.URL+"/", "text/plain", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("non-event POST status %d, want 400", resp.StatusCode)
	}
}

// TestSmokeScriptMatchesStubLogs keeps the strings smoke.sh greps for in
// step with the stub.
func TestSmokeScriptMatchesStubLogs(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "smoke.sh"))
	if err != nil {
		t.Fatal(err)
	}
	msgs := []string{logPoll, logAck, logSink, logBadAuth}
	want := make([]string, 0, len(msgs)+2)
	want = append(want, SmokeEventID, firstCursor)
	for _, msg := range msgs {
		want = append(want, `"msg":"`+msg+`"`)
	}
	for _, s := range want {
		if !strings.Contains(string(b), s) {
			t.Errorf("smoke.sh does not reference %q", s)
		}
	}
}

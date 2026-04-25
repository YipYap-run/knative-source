package source

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2"
	"github.com/oklog/ulid/v2"
)

// newTestPollClient builds a PollClient pointed at the supplied baseURL with
// sensible defaults for tests.
func newTestPollClient(t *testing.T, baseURL, apiKey, filter string) *PollClient {
	t.Helper()
	cursorPath := filepath.Join(t.TempDir(), "cursor")
	return &PollClient{
		baseURL:    baseURL,
		apiKey:     apiKey,
		filter:     filter,
		interval:   10 * time.Millisecond,
		cursorPath: cursorPath,
		httpClient: &http.Client{Timeout: 2 * time.Second},
	}
}

// makeEvent builds a minimal valid CloudEvent envelope for test servers to
// return in their responses.
func makeEvent(t *testing.T, ceType string) ce.Event {
	t.Helper()
	ev := ce.NewEvent()
	ev.SetID(ulid.Make().String())
	ev.SetType(ceType)
	ev.SetSource("poll-test")
	ev.SetSpecVersion("1.0")
	if err := ev.SetData(ce.ApplicationJSON, map[string]string{"k": "v"}); err != nil {
		t.Fatalf("set data: %v", err)
	}
	return ev
}

// TestPollClient_Fetch: httptest server returns 2 events; fetch decodes them
// and surfaces the cursor.
func TestPollClient_Fetch(t *testing.T) {
	ev1 := makeEvent(t, "run.yipyap.alert.firing")
	ev2 := makeEvent(t, "run.yipyap.alert.resolved")
	payload := map[string]any{
		"events":      []ce.Event{ev1, ev2},
		"next_cursor": "cursor-abc",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	c := newTestPollClient(t, srv.URL, "api-key", "")
	next, events, err := c.fetch(t.Context(), "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if next != "cursor-abc" {
		t.Fatalf("want cursor 'cursor-abc', got %q", next)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	if events[0].Type() != "run.yipyap.alert.firing" {
		t.Fatalf("event[0] type: %q", events[0].Type())
	}
	if events[1].Type() != "run.yipyap.alert.resolved" {
		t.Fatalf("event[1] type: %q", events[1].Type())
	}
}

// TestPollClient_CursorPersistence: write + read cursor round-trip via the
// on-disk cursor file.
func TestPollClient_CursorPersistence(t *testing.T) {
	c := newTestPollClient(t, "http://unused", "k", "")

	// Missing file → empty cursor, no error.
	got, err := c.readCursor()
	if err != nil {
		t.Fatalf("readCursor on missing file: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty cursor on missing file, got %q", got)
	}

	// Write, then read back.
	want := "cursor-xyz"
	if err := c.writeCursor(want); err != nil {
		t.Fatalf("writeCursor: %v", err)
	}
	got, err = c.readCursor()
	if err != nil {
		t.Fatalf("readCursor: %v", err)
	}
	if got != want {
		t.Fatalf("cursor roundtrip: got %q, want %q", got, want)
	}

	// Overwrite check — writeCursor must be idempotent per call.
	if err := c.writeCursor("cursor-new"); err != nil {
		t.Fatalf("writeCursor 2: %v", err)
	}
	got, err = c.readCursor()
	if err != nil {
		t.Fatalf("readCursor 2: %v", err)
	}
	if got != "cursor-new" {
		t.Fatalf("overwrite: got %q, want 'cursor-new'", got)
	}
}

// TestPollClient_HTTPError: a 500 from the server surfaces as an error.
func TestPollClient_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestPollClient(t, srv.URL, "k", "")
	_, _, err := c.fetch(t.Context(), "")
	if err == nil {
		t.Fatalf("expected error on 500, got nil")
	}
}

// TestPollClient_AuthHeaderAttached: the outbound request carries the
// Authorization: Bearer <apikey> header.
func TestPollClient_AuthHeaderAttached(t *testing.T) {
	const key = "secret-key-123"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events":      []ce.Event{},
			"next_cursor": "",
		})
	}))
	defer srv.Close()

	c := newTestPollClient(t, srv.URL, key, "")
	if _, _, err := c.fetch(t.Context(), ""); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	want := "Bearer " + key
	if gotAuth != want {
		t.Fatalf("auth header: got %q, want %q", gotAuth, want)
	}
}

// TestPollClient_FilterQueryParam: the supplied filter rides the URL's
// ?filter= parameter.
func TestPollClient_FilterQueryParam(t *testing.T) {
	var gotFilter, gotSince string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFilter = r.URL.Query().Get("filter")
		gotSince = r.URL.Query().Get("since")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events":      []ce.Event{},
			"next_cursor": "",
		})
	}))
	defer srv.Close()

	c := newTestPollClient(t, srv.URL, "k", "run.yipyap.alert.*")
	if _, _, err := c.fetch(t.Context(), "prev-cursor"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotFilter != "run.yipyap.alert.*" {
		t.Fatalf("filter query: got %q, want %q", gotFilter, "run.yipyap.alert.*")
	}
	if gotSince != "prev-cursor" {
		t.Fatalf("since query: got %q, want %q", gotSince, "prev-cursor")
	}
}

// TestPollClient_CursorAtomicWrite: writeCursor doesn't leave a partial file
// behind on failure. We can't easily simulate the failure path, but we can
// at least verify the temp-file cleanup: no stray .tmp siblings remain after
// a successful write.
func TestPollClient_CursorAtomicWrite(t *testing.T) {
	c := newTestPollClient(t, "http://unused", "k", "")
	if err := c.writeCursor("abc"); err != nil {
		t.Fatalf("writeCursor: %v", err)
	}
	dir := filepath.Dir(c.cursorPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("stray temp file left behind: %s", e.Name())
		}
	}
	// Confirm the target exists and has the expected content.
	b, err := os.ReadFile(c.cursorPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "abc" {
		t.Fatalf("cursor content: got %q, want 'abc'", string(b))
	}
}


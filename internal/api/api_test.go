// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
)

// syncBuffer is a log destination that tests can read while the server
// writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type testServer struct {
	*Server
	db  *db.DB
	log *syncBuffer
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "runnel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	id, err := d.InstanceID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := New(d, logger, Info{Name: "Test", Version: "0.0.0-test", InstanceID: id})
	return &testServer{Server: s, db: d, log: logs}
}

// do sends one request to the handler and decodes a JSON answer into out,
// when out is not nil.
func (ts *testServer) do(t *testing.T, method, target string, body io.Reader, out any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	rec := httptest.NewRecorder()
	ts.Handler().ServeHTTP(rec, req)
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %d %q: %v", method, target, rec.Code, rec.Body.String(), err)
		}
	}
	return rec
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestServerInfo(t *testing.T) {
	ts := newTestServer(t)
	var got map[string]any
	rec := ts.do(t, "GET", "/api/v1/server", nil, &got)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}
	want := map[string]any{
		"product":     "runnel",
		"name":        "Test",
		"version":     "0.0.0-test",
		"instance_id": ts.info.InstanceID,
		"setup_done":  false,
		"api":         map[string]any{"version": 1.0, "features": []any{}},
		"transcode":   map[string]any{"hw": "none", "video": []any{}, "depth10": []any{}, "slots": 0.0},
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("GET /server\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestUnknownEndpoint(t *testing.T) {
	ts := newTestServer(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/nothing"},
		{"POST", "/api/v1/server"},
	} {
		var e apiError
		rec := ts.do(t, c.method, c.path, nil, &e)
		if rec.Code != 404 || e.Error.Code != "not_found" {
			t.Errorf("%s %s: %d %+v", c.method, c.path, rec.Code, e)
		}
	}
}

func TestPanicIsRecoveredAndLogged(t *testing.T) {
	ts := newTestServer(t)
	ts.mux.Handle("GET /api/v1/boom", ts.json(func(w http.ResponseWriter, r *http.Request) {
		panic("deliberate")
	}))
	var e apiError
	rec := ts.do(t, "GET", "/api/v1/boom", nil, &e)
	if rec.Code != 500 || e.Error.Code != "internal" {
		t.Fatalf("%d %+v", rec.Code, e)
	}
	id := strings.TrimPrefix(e.Error.Message, "internal error, logged as ")
	if len(id) != 12 {
		t.Fatalf("message %q carries no request id", e.Error.Message)
	}
	logs := ts.log.String()
	if !strings.Contains(logs, "msg=panic id="+id) || !strings.Contains(logs, "deliberate") {
		t.Fatalf("the panic is not in the log under id %s:\n%s", id, logs)
	}
	if !strings.Contains(logs, "msg=request id="+id) || !strings.Contains(logs, "status=500") {
		t.Fatalf("no request line with status 500:\n%s", logs)
	}
}

func TestLogLeavesOutTheQuery(t *testing.T) {
	ts := newTestServer(t)
	ts.do(t, "GET", "/api/v1/server?t=rn_secretvalue", nil, nil)
	logs := ts.log.String()
	if strings.Contains(logs, "secretvalue") {
		t.Fatalf("the query string was logged:\n%s", logs)
	}
	if !strings.Contains(logs, "path=/api/v1/server") || !strings.Contains(logs, "status=200") {
		t.Fatalf("no request line:\n%s", logs)
	}
}

func TestJSONRoutesReachTheConnection(t *testing.T) {
	ts := newTestServer(t)
	var deadlineErr error
	ts.mux.Handle("GET /api/v1/deadline", ts.json(func(w http.ResponseWriter, r *http.Request) {
		// The json wrapper has already set a deadline. Setting one again
		// shows whether the statusWriter lets ResponseController reach
		// the connection.
		deadlineErr = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(jsonDeadline))
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("the request context has no deadline")
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	srv := httptest.NewServer(ts.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/deadline")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || deadlineErr != nil {
		t.Fatalf("status %d, SetWriteDeadline: %v", resp.StatusCode, deadlineErr)
	}
}

// readFromCounter is a ResponseWriter that counts ReadFrom calls.
type readFromCounter struct {
	*httptest.ResponseRecorder
	calls int
}

func (w *readFromCounter) ReadFrom(r io.Reader) (int64, error) {
	w.calls++
	return io.Copy(w.ResponseRecorder, r)
}

func TestStatusWriterPassesReadFrom(t *testing.T) {
	under := &readFromCounter{ResponseRecorder: httptest.NewRecorder()}
	sw := &statusWriter{ResponseWriter: under}
	// http.ServeContent copies through an io.LimitedReader, which has no
	// WriteTo, so io.Copy takes the writer's ReadFrom.
	n, err := io.Copy(sw, io.LimitReader(strings.NewReader("twelve bytes"), 12))
	if err != nil || n != 12 || under.calls != 1 || sw.status != 200 {
		t.Fatalf("copied %d (%v), ReadFrom calls %d, status %d", n, err, under.calls, sw.status)
	}
}

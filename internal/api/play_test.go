// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/scan"
)

// countGate counts what holds it.
type countGate struct {
	mu sync.Mutex
	n  int
}

func (g *countGate) Open() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n == 0
}

func (g *countGate) Hold() func() {
	g.mu.Lock()
	g.n++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.n--
			g.mu.Unlock()
		})
	}
}

func (g *countGate) held() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPrepareAndTheGate(t *testing.T) {
	b := newBrowse(t)
	g := &countGate{}
	b.Server.gate = g
	b.Server.firstByteHold = 300 * time.Millisecond
	b.Server.prepareHold = 600 * time.Millisecond
	f := b.fileOf(t, "Paper Comets")
	other := b.fileOf(t, "The Salt Road")
	prepare := func(id int64, body any, token string) int {
		return b.call(t, "POST", "/api/v1/files/"+idStr(id)+"/prepare", token, "", body, nil).Code
	}
	raw := func(id int64) int {
		req := httptest.NewRequest("GET", "/api/v1/files/"+idStr(id)+"/raw?t="+b.token, nil)
		req.Header.Set("Range", "bytes=0-99")
		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	// A prepare holds the gate until its file's playback starts. The
	// playback holds it itself until shortly after its first byte.
	if code := prepare(f.ID, map[string]any{"mode": "direct", "start_ms": 512000}, b.token); code != 204 || g.held() != 1 {
		t.Fatalf("prepare: %d, gate held %d times", code, g.held())
	}
	if code := raw(f.ID); code != 206 {
		t.Fatalf("raw: %d", code)
	}
	if g.held() != 1 {
		t.Fatalf("after the playback started: gate held %d times, want once, by the playback", g.held())
	}
	waitUntil(t, "the playback lets the gate go", func() bool { return g.held() == 0 })

	// A newer prepare of the same token replaces the one before it. One
	// that no playback follows lets the gate go after prepareHold.
	prepare(f.ID, map[string]any{"mode": "direct"}, b.token)
	start := time.Now()
	prepare(other.ID, map[string]any{"mode": "direct"}, b.token)
	if g.held() != 1 {
		t.Fatalf("two prepares of one token: gate held %d times", g.held())
	}
	raw(f.ID) // not the file prepared now: the prepare keeps its hold
	waitUntil(t, "the prepare lets the gate go", func() bool { return g.held() == 0 })
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Fatalf("the prepare let the gate go after %v", waited)
	}

	var e apiError
	for _, c := range []struct {
		id   int64
		body any
		want int
		code string
	}{
		{f.ID, map[string]any{"mode": "transcode", "v": "hevc", "depth": 10}, 409, "unsupported"},
		{f.ID, map[string]any{"mode": "copy"}, 400, "bad_request"},
		{f.ID, nil, 400, "bad_request"},
		{99999, map[string]any{"mode": "direct"}, 404, "not_found"},
	} {
		e = apiError{}
		if rec := b.call(t, "POST", "/api/v1/files/"+idStr(c.id)+"/prepare", b.token, "", c.body, &e); rec.Code != c.want || e.Error.Code != c.code {
			t.Errorf("prepare %v: %d %+v, want %d %s", c.body, rec.Code, e, c.want, c.code)
		}
	}
	if code := prepare(f.ID, map[string]any{"mode": "direct"}, ""); code != 401 {
		t.Fatalf("prepare without a token: %d", code)
	}
	if g.held() != 0 {
		t.Fatalf("a refused prepare holds the gate: %d", g.held())
	}

	// A file that is missing: 503, and nothing holds the gate.
	path := filepath.Join(b.films, "Paper Comets (2019)/Paper Comets (2019).mkv")
	os.Rename(path, path+".away")
	ctx := context.Background()
	s := scan.New(b.db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	lib, _ := b.db.Library(ctx, 1)
	s.ScanLibrary(ctx, lib)
	e = apiError{}
	if rec := b.call(t, "POST", "/api/v1/files/"+idStr(f.ID)+"/prepare", b.token, "", map[string]any{"mode": "direct"}, &e); rec.Code != 503 || e.Error.Code != "unavailable" {
		t.Fatalf("prepare of a missing file: %d %+v", rec.Code, e)
	}
	if code := raw(f.ID); code != 503 || g.held() != 0 {
		t.Fatalf("raw of a missing file: %d, gate held %d times", code, g.held())
	}
}

func TestFile(t *testing.T) {
	b := newBrowse(t)
	want := b.fileOf(t, "Paper Comets")
	var got fileBody
	if rec := b.call(t, "GET", "/api/v1/files/"+idStr(want.ID), b.token, "", nil, &got); rec.Code != 200 ||
		string(mustJSON(got)) != string(mustJSON(want)) {
		t.Fatalf("%d %+v, want the detail's %+v", rec.Code, got, want)
	}
	// A file not offered for playback, one ffprobe could not read, is not
	// there either.
	files, _ := b.db.LibraryFiles(context.Background(), 1)
	var broken int64
	for _, f := range files {
		if f.RelPath == "Broken Lantern (2001).mkv" {
			broken = f.ID
		}
	}
	if broken == 0 {
		t.Fatal("no unreadable file")
	}
	var e apiError
	for _, id := range []int64{broken, 99999} {
		e = apiError{}
		if rec := b.call(t, "GET", "/api/v1/files/"+idStr(id), b.token, "", nil, &e); rec.Code != 404 || e.Error.Code != "not_found" {
			t.Errorf("file %d: %d %+v", id, rec.Code, e)
		}
	}
	if rec := b.call(t, "GET", "/api/v1/files/"+idStr(want.ID), "", "", nil, nil); rec.Code != 401 {
		t.Fatalf("without a token: %d", rec.Code)
	}
}

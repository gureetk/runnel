// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// scansAsked records the scans the API asks for, and says that a scan
// walks the library walking names.
type scansAsked struct {
	mu      sync.Mutex
	ids     []int64
	walking int64
	forgot  []int64 // roots
}

func (s *scansAsked) Forget(ids []int64) {
	s.mu.Lock()
	s.forgot = append(s.forgot, ids...)
	s.mu.Unlock()
}

func (s *scansAsked) Walking(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.walking != 0 && s.walking == id
}

func (s *scansAsked) Request(id int64) {
	s.mu.Lock()
	s.ids = append(s.ids, id)
	s.mu.Unlock()
}

func (s *scansAsked) got() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.ids...)
}

func TestCreateLibrary(t *testing.T) {
	ts := newTestServer(t)
	scans := &scansAsked{}
	ts.scans = scans
	local := "127.0.0.1:40000"
	films := filepath.Join(t.TempDir(), "films")
	more := filepath.Join(t.TempDir(), "more films")
	os.MkdirAll(films, 0o755)
	os.MkdirAll(filepath.Join(more, "inner"), 0o755)

	var lib adminLibraryBody
	rec := ts.call(t, "POST", "/api/v1/admin/libraries", ts.adminToken, local,
		map[string]any{"name": "Films", "kind": "movies", "roots": []string{films + "/", more}}, &lib)
	if rec.Code != 201 || lib.ID == 0 || lib.Kind != "movies" || lib.Profile != "default" || len(lib.Roots) != 2 ||
		lib.Roots[0].Path != films || !lib.Roots[0].Available {
		t.Fatalf("%d %+v", rec.Code, lib)
	}
	if got := scans.got(); len(got) != 1 || got[0] != lib.ID {
		t.Fatalf("scans asked for: %v, want the new library's first scan", got)
	}

	var e apiError
	for _, c := range []struct {
		body   map[string]any
		status int
		code   string
	}{
		{map[string]any{"name": "Films", "kind": "movies", "roots": []string{t.TempDir()}}, 409, "conflict"},
		{map[string]any{"name": "Inside", "kind": "movies", "roots": []string{filepath.Join(more, "inner")}}, 409, "conflict"},
		{map[string]any{"name": "Around", "kind": "movies", "roots": []string{filepath.Dir(films)}}, 409, "conflict"},
		{map[string]any{"name": "Shows", "kind": "music", "roots": []string{t.TempDir()}}, 400, "bad_request"},
		{map[string]any{"name": "Shows", "kind": "shows", "profile": "kids", "roots": []string{t.TempDir()}}, 400, "bad_request"},
		{map[string]any{"name": "Shows", "kind": "shows", "roots": []string{}}, 400, "bad_request"},
		{map[string]any{"name": "Shows", "kind": "shows", "roots": []string{"relative/path"}}, 400, "bad_request"},
		{map[string]any{"name": "Shows", "kind": "shows", "roots": []string{filepath.Join(t.TempDir(), "absent")}}, 400, "bad_request"},
		{map[string]any{"name": "", "kind": "shows", "roots": []string{t.TempDir()}}, 400, "bad_request"},
	} {
		e = apiError{}
		rec := ts.call(t, "POST", "/api/v1/admin/libraries", ts.adminToken, local, c.body, &e)
		if rec.Code != c.status || e.Error.Code != c.code {
			t.Errorf("%v: %d %+v, want %d %s", c.body, rec.Code, e, c.status, c.code)
		}
	}
	if got := scans.got(); len(got) != 1 {
		t.Fatalf("a refused library asked for a scan: %v", got)
	}

	rec = ts.call(t, "POST", "/api/v1/admin/libraries", ts.adminToken, local,
		map[string]any{"name": "Anime", "kind": "shows", "profile": "anime", "roots": []string{t.TempDir()}}, &lib)
	if rec.Code != 201 || lib.Profile != "anime" {
		t.Fatalf("an anime library: %d %+v", rec.Code, lib)
	}
	// Admin only.
	if rec := ts.call(t, "POST", "/api/v1/admin/libraries", "", "", map[string]any{}, nil); rec.Code != 401 {
		t.Fatalf("without a token: %d", rec.Code)
	}
}

func TestScanRequest(t *testing.T) {
	ts := newTestServer(t)
	scans := &scansAsked{}
	ts.scans = scans
	local := "127.0.0.1:40000"
	dir := t.TempDir()
	var lib adminLibraryBody
	ts.call(t, "POST", "/api/v1/admin/libraries", ts.adminToken, local,
		map[string]any{"name": "Films", "kind": "movies", "roots": []string{dir}}, &lib)

	if rec := ts.call(t, "POST", "/api/v1/admin/scan", ts.adminToken, local, map[string]any{"library_id": lib.ID}, nil); rec.Code != 202 {
		t.Fatalf("one library: %d", rec.Code)
	}
	if rec := ts.call(t, "POST", "/api/v1/admin/scan", ts.adminToken, local, map[string]any{}, nil); rec.Code != 202 {
		t.Fatalf("every library: %d", rec.Code)
	}
	if rec := ts.call(t, "POST", "/api/v1/admin/scan", ts.adminToken, local, nil, nil); rec.Code != 202 {
		t.Fatalf("no body: %d", rec.Code)
	}
	var e apiError
	if rec := ts.call(t, "POST", "/api/v1/admin/scan", ts.adminToken, local, map[string]any{"library_id": 99}, &e); rec.Code != 404 || e.Error.Code != "not_found" {
		t.Fatalf("an unknown library: %d %+v", rec.Code, e)
	}
	got := scans.got()
	if len(got) != 4 || got[1] != lib.ID || got[2] != 0 || got[3] != 0 {
		t.Fatalf("scans asked for: %v", got)
	}
}

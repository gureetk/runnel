// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/model"
)

const local = "127.0.0.1:40000"

type adminLibrariesBody struct {
	Libraries []libraryStateBody `json:"libraries"`
}

// filmFiles are files of films with the titles given, each in its folder.
func filmFiles(titles ...string) []db.SeenFile {
	var out []db.SeenFile
	for i, title := range titles {
		out = append(out, db.SeenFile{RelPath: title + "/" + title + ".mkv", Kind: "video", Size: int64(100 + i), MtimeNS: 1})
	}
	return out
}

func TestAdminLibraries(t *testing.T) {
	ts := newTestServer(t)
	scans := &scansAsked{}
	ts.scans = scans
	ctx := context.Background()
	lib, err := ts.db.CreateLibrary(ctx, "Films", "movies", "default", []string{t.TempDir(), t.TempDir()}, 1)
	if err != nil {
		t.Fatal(err)
	}
	c := db.ScanChanges{New: filmFiles("Low Water (2001)", "Salt Road (2004)"), Result: model.ScanResult{At: 5, MS: 7, Added: 2}}
	if err := ts.db.ApplyScan(ctx, lib.Roots[0].ID, c, 5, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := library.Build(ctx, ts.db, lib.ID, 5); err != nil {
		t.Fatal(err)
	}
	get := func() libraryStateBody {
		t.Helper()
		var body adminLibrariesBody
		if rec := ts.call(t, "GET", "/api/v1/admin/libraries", ts.adminToken, local, nil, &body); rec.Code != 200 || len(body.Libraries) != 1 {
			t.Fatalf("%d %+v", rec.Code, body)
		}
		return body.Libraries[0]
	}

	l := get()
	if l.Items != 2 || l.Scan.State != "idle" || l.Scan.Done != nil || len(l.Roots) != 2 || l.Roots[1].LastScan != nil {
		t.Fatalf("%+v", l)
	}
	if ls := l.Roots[0].LastScan; ls == nil || ls.At != 5 || ls.MS != 7 || ls.Added != 2 || ls.Error != nil {
		t.Fatalf("last scan: %+v", ls)
	}

	if _, err := ts.db.QueueProbes(ctx, 0, 1, 6); err != nil {
		t.Fatal(err)
	}
	if l = get(); l.Scan.State != "probing" || *l.Scan.Done != 0 || *l.Scan.Total != 2 {
		t.Fatalf("probing: %+v", l.Scan)
	}
	scans.mu.Lock()
	scans.walking = lib.ID
	scans.mu.Unlock()
	if l = get(); l.Scan.State != "walking" || l.Scan.Total != nil {
		t.Fatalf("walking: %+v", l.Scan)
	}
	scans.mu.Lock()
	scans.walking = 0
	scans.mu.Unlock()

	// A folder whose last scan failed, with nothing running.
	if _, err := ts.db.CancelWork(ctx, "probe", lib.ID); err != nil {
		t.Fatal(err)
	}
	if err := ts.db.RootUnavailable(ctx, lib.Roots[1].ID, model.ScanResult{At: 8, Error: "the folder cannot be listed"}); err != nil {
		t.Fatal(err)
	}
	l = get()
	if l.Scan.State != "failed" || l.Roots[1].Available || l.Roots[1].LastScan == nil || *l.Roots[1].LastScan.Error != "the folder cannot be listed" {
		t.Fatalf("failed: %+v", l)
	}

	ts.addUser(t, "ada", "correct horse", true)
	ts.addUser(t, "noor", "correct horse", false)
	var a loginAnswer
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("noor", "correct horse", "d"), &a)
	if rec := ts.call(t, "GET", "/api/v1/admin/libraries", a.Token, "", nil, nil); rec.Code != 403 {
		t.Fatalf("someone who is no admin: %d", rec.Code)
	}
}

func TestChangeLibraryAPI(t *testing.T) {
	ts := newTestServer(t)
	scans := &scansAsked{}
	ts.scans = scans
	ctx := context.Background()
	dirA, dirB, dirC := t.TempDir(), t.TempDir(), t.TempDir()
	films, _ := ts.db.CreateLibrary(ctx, "Films", "movies", "default", []string{dirA, dirB}, 1)
	ts.db.CreateLibrary(ctx, "Shows", "shows", "default", []string{t.TempDir()}, 1)
	if err := ts.db.ApplyScan(ctx, films.Roots[1].ID, db.ScanChanges{New: filmFiles("North Light (1999)")}, 2, 0); err != nil {
		t.Fatal(err)
	}
	library.Build(ctx, ts.db, films.ID, 2)
	path := "/api/v1/admin/libraries/" + idStr(films.ID)

	var e apiError
	for _, c := range []struct {
		body   any
		status int
		code   string
	}{
		{map[string]any{"kind": "shows"}, 400, "bad_request"},
		{map[string]any{"colour": "blue"}, 400, "bad_request"},
		{map[string]any{"name": nil}, 400, "bad_request"},
		{map[string]any{"name": " Films"}, 400, "bad_request"},
		{map[string]any{"name": 3}, 400, "bad_request"},
		{map[string]any{"profile": "kids"}, 400, "bad_request"},
		{map[string]any{"add_roots": []string{"relative"}}, 400, "bad_request"},
		{map[string]any{"add_roots": []string{filepath.Join(dirC, "absent")}}, 400, "bad_request"},
		{map[string]any{"remove_roots": []int64{999}}, 400, "bad_request"},
		{map[string]any{"remove_roots": []int64{films.Roots[0].ID, films.Roots[1].ID}}, 400, "bad_request"},
		{map[string]any{"name": "Shows"}, 409, "conflict"},
		{map[string]any{"add_roots": []string{dirA}}, 409, "conflict"},
	} {
		e = apiError{}
		if rec := ts.call(t, "PATCH", path, ts.adminToken, local, c.body, &e); rec.Code != c.status || e.Error.Code != c.code {
			t.Errorf("%v: %d %+v, want %d %s", c.body, rec.Code, e, c.status, c.code)
		}
	}
	if rec := ts.call(t, "PATCH", "/api/v1/admin/libraries/99", ts.adminToken, local, map[string]any{"name": "X"}, &e); rec.Code != 404 {
		t.Fatalf("no such library: %d", rec.Code)
	}
	if got := scans.got(); len(got) != 0 {
		t.Fatalf("refused changes asked for scans: %v", got)
	}

	var lib adminLibraryBody
	rec := ts.call(t, "PATCH", path, ts.adminToken, local, map[string]any{"name": "Cinema", "profile": "anime",
		"add_roots": []string{dirC}, "remove_roots": []int64{films.Roots[1].ID}}, &lib)
	if rec.Code != 200 || lib.Name != "Cinema" || lib.Profile != "anime" || len(lib.Roots) != 2 ||
		lib.Roots[0].Path != dirA || lib.Roots[1].Path != dirC {
		t.Fatalf("%d %+v", rec.Code, lib)
	}
	if got := scans.got(); !slices.Equal(got, []int64{films.ID}) {
		t.Fatalf("scans asked for: %v, want the library's", got)
	}
	if !slices.Equal(scans.forgot, []int64{films.Roots[1].ID}) {
		t.Fatalf("folders forgotten: %v", scans.forgot)
	}
	if items, _ := ts.db.LibraryItems(ctx, films.ID); len(items) != 0 {
		t.Fatalf("the removed folder's film is left: %+v", items)
	}
	// A rename alone asks for no scan.
	if rec := ts.call(t, "PATCH", path, ts.adminToken, local, map[string]any{"name": "Films"}, &lib); rec.Code != 200 || lib.Name != "Films" {
		t.Fatalf("%d %+v", rec.Code, lib)
	}
	if got := scans.got(); len(got) != 1 {
		t.Fatalf("a rename asked for a scan: %v", got)
	}

	if rec := ts.call(t, "DELETE", path, ts.adminToken, local, nil, nil); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if len(scans.forgot) != 3 || scans.forgot[1] != films.Roots[0].ID {
		t.Fatalf("folders forgotten: %v, want the library's %d and %d too", scans.forgot, films.Roots[0].ID, lib.Roots[1].ID)
	}
	if rec := ts.call(t, "DELETE", path, ts.adminToken, local, nil, &e); rec.Code != 404 || e.Error.Code != "not_found" {
		t.Fatalf("delete again: %d %+v", rec.Code, e)
	}
	if libs, _ := ts.db.Libraries(ctx); len(libs) != 1 || libs[0].Name != "Shows" {
		t.Fatalf("libraries left: %+v", libs)
	}
}

// TestRemovalsReachACopy: a folder taken out of a library, a library
// removed, and a library and a folder added after them reach a copy kept
// with /changes as they reach one made from zero (docs/04-api.md,
// section 5).
func TestRemovalsReachACopy(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	b := &browse{testServer: ts}
	ts.addUser(t, "ada", "correct horse", true)
	var a loginAnswer
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("ada", "correct horse", "d"), &a)
	b.token = a.Token

	films, _ := ts.db.CreateLibrary(ctx, "Films", "movies", "default", []string{t.TempDir(), t.TempDir()}, 1)
	shows, _ := ts.db.CreateLibrary(ctx, "Shows", "shows", "default", []string{t.TempDir()}, 1)
	b.applyAndBuild(t, films, db.ScanChanges{New: filmFiles("Low Water (2001)", "Salt Road (2004)")}, 0)
	second := films
	second.Roots = films.Roots[1:]
	b.applyAndBuild(t, second, db.ScanChanges{New: filmFiles("North Light (1999)")}, 0)
	var eps []db.SeenFile
	for e := 1; e <= 3; e++ {
		eps = append(eps, db.SeenFile{RelPath: fmt.Sprintf("Harbor Lantern (2010)/Season 01/Harbor Lantern - S01E%02d.mkv", e),
			Kind: "video", Size: int64(900 + e), MtimeNS: 1})
	}
	b.applyAndBuild(t, shows, db.ScanChanges{New: eps}, 0)

	var page itemsPage
	b.call(t, "GET", "/api/v1/items?library="+idStr(shows.ID), b.token, "", nil, &page)
	series := page.Items[0].ID
	b.call(t, "PUT", "/api/v1/items/"+idStr(series)+"/played", b.token, "", nil, nil)
	b.call(t, "PUT", "/api/v1/series/"+idStr(series)+"/prefs", b.token, "", map[string]any{"sub_mode": "on"}, nil)
	b.call(t, "PUT", "/api/v1/items/"+idStr(series)+"/list", b.token, "", nil, nil)

	kept := newCopy()
	b.sync(t, b.token, 500, kept)
	if len(kept.items) != 3+1+1+3 || len(kept.libraries) != 2 || len(kept.state) != 3 || len(kept.prefs) != 1 || len(kept.list) != 1 {
		t.Fatalf("%d items, %d libraries, %d states, %d choices, %d listings", len(kept.items), len(kept.libraries),
			len(kept.state), len(kept.prefs), len(kept.list))
	}

	// The second folder of films goes, and so does the library of shows.
	// A new library of shows and a new folder of films follow.
	if rec := b.call(t, "PATCH", "/api/v1/admin/libraries/"+idStr(films.ID), ts.adminToken, local,
		map[string]any{"remove_roots": []int64{films.Roots[1].ID}}, nil); rec.Code != 200 {
		t.Fatalf("remove a folder: %d", rec.Code)
	}
	if rec := b.call(t, "DELETE", "/api/v1/admin/libraries/"+idStr(shows.ID), ts.adminToken, local, nil, nil); rec.Code != 204 {
		t.Fatalf("remove a library: %d", rec.Code)
	}
	anime, _ := ts.db.CreateLibrary(ctx, "Anime", "shows", "anime", []string{t.TempDir()}, 2)
	b.applyAndBuild(t, anime, db.ScanChanges{New: eps[:2]}, 0)
	dir := t.TempDir()
	var changed adminLibraryBody
	b.call(t, "PATCH", "/api/v1/admin/libraries/"+idStr(films.ID), ts.adminToken, local, map[string]any{"add_roots": []string{dir}}, &changed)
	third := films
	third.Roots = []model.Root{{ID: changed.Roots[1].ID, LibraryID: films.ID, Path: dir}}
	b.applyAndBuild(t, third, db.ScanChanges{New: filmFiles("Tidewater (2020)")}, 0)

	b.sync(t, b.token, 500, kept)
	fresh := newCopy()
	b.sync(t, b.token, 500, fresh)
	if d := kept.diff(fresh); d != "" {
		t.Fatalf("the copy kept up to date differs from a fresh one: %s", d)
	}
	if len(fresh.items) != 2+1+1+1+2 || len(fresh.libraries) != 2 || len(fresh.state) != 0 || len(fresh.list) != 0 {
		t.Fatalf("%d items, %d libraries, %d states, %d listings", len(fresh.items), len(fresh.libraries), len(fresh.state), len(fresh.list))
	}
}

type fsBody struct {
	Path      string         `json:"path"`
	Parent    *string        `json:"parent"`
	Truncated bool           `json:"truncated"`
	Folders   []fsFolderBody `json:"folders"`
}

func TestFolderBrowser(t *testing.T) {
	ts := newTestServer(t)
	root := t.TempDir()
	for _, d := range []string{"films/a", "films/b", "shows", ".hidden", "locked"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "films", "notes.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(root, "file.mkv"), nil, 0o644)
	os.Symlink(filepath.Join(root, "films"), filepath.Join(root, "linked"))
	os.Symlink(filepath.Join(root, "file.mkv"), filepath.Join(root, "linked-file"))
	os.Chmod(filepath.Join(root, "locked"), 0)
	defer os.Chmod(filepath.Join(root, "locked"), 0o755)

	var body fsBody
	if rec := ts.call(t, "GET", "/api/v1/admin/fs?path="+root+"/", ts.adminToken, local, nil, &body); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	var names []string
	for _, f := range body.Folders {
		names = append(names, f.Name)
	}
	if body.Path != root || body.Parent == nil || *body.Parent != filepath.Dir(root) || body.Truncated ||
		!slices.Equal(names, []string{"films", "linked", "locked", "shows"}) {
		t.Fatalf("%+v", body)
	}
	if f := body.Folders[0]; f.Path != filepath.Join(root, "films") || !f.Readable || f.Entries == nil || *f.Entries != 3 {
		t.Fatalf("films: %+v", f)
	}
	if f := body.Folders[3]; !f.Readable || *f.Entries != 0 {
		t.Fatalf("shows: %+v", f)
	}
	// Root reads every folder; anyone else is refused this one.
	if os.Geteuid() != 0 {
		if f := body.Folders[2]; f.Readable || f.Entries != nil {
			t.Fatalf("locked: %+v", f)
		}
	}

	if rec := ts.call(t, "GET", "/api/v1/admin/fs", ts.adminToken, local, nil, &body); rec.Code != 200 || body.Path != "/" || body.Parent != nil {
		t.Fatalf("without a path: %d %+v", rec.Code, body)
	}
	var e apiError
	for _, c := range []struct {
		query  string
		status int
	}{
		{"?path=" + filepath.Join(root, "file.mkv"), 404},
		{"?path=" + filepath.Join(root, "absent"), 404},
		{"?path=relative", 400},
		{"?path=/&depth=2", 400},
	} {
		if rec := ts.call(t, "GET", "/api/v1/admin/fs"+c.query, ts.adminToken, local, nil, &e); rec.Code != c.status {
			t.Errorf("%s: %d %+v, want %d", c.query, rec.Code, e, c.status)
		}
	}

	many := t.TempDir()
	for i := range fsMaxFolders + 1 {
		os.Mkdir(filepath.Join(many, fmt.Sprintf("f%04d", i)), 0o755)
	}
	if rec := ts.call(t, "GET", "/api/v1/admin/fs?path="+many, ts.adminToken, local, nil, &body); rec.Code != 200 ||
		!body.Truncated || len(body.Folders) != fsMaxFolders || body.Folders[0].Name != "f0000" {
		t.Fatalf("a folder of %d folders: %d, %d listed, truncated %v", fsMaxFolders+1, rec.Code, len(body.Folders), body.Truncated)
	}
}

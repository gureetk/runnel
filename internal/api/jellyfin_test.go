// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/meta/jellyfin"
	"github.com/gureetk/runnel/internal/meta/jellyfin/standin"
	"github.com/gureetk/runnel/internal/tasks"
)

func TestJellyfinUsers(t *testing.T) {
	ts := newTestServer(t)
	jf := &standin.Server{Key: "0123456789abcdef0123456789abcdef", Version: "12.4.0",
		Users: []standin.User{{ID: "5b4d20b7e5df4f2a8a2d3e4f5a6b7c8d", Name: "noor"}, {ID: "4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c", Name: "Ada"}}}
	url := jf.Start(t).URL

	var body struct {
		Version string `json:"version"`
		Users   []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"users"`
	}
	if rec := ts.call(t, "POST", "/api/v1/admin/import/jellyfin/users", ts.adminToken, local,
		map[string]string{"url": url, "api_key": jf.Key}, &body); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	if body.Version != "12.4.0" || len(body.Users) != 2 || body.Users[0].Name != "Ada" || body.Users[0].ID != "4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c" {
		t.Fatalf("%+v", body)
	}

	gone := httptest.NewServer(nil)
	gone.Close()
	var e apiError
	for _, c := range []struct {
		body   map[string]string
		status int
		code   string
	}{
		{map[string]string{"url": url, "api_key": "another-key"}, 400, "bad_key"},
		{map[string]string{"url": gone.URL, "api_key": jf.Key}, 502, "upstream"},
		{map[string]string{"url": "jellyfin.lan", "api_key": jf.Key}, 400, "bad_request"},
		{map[string]string{"url": url, "api_key": "a,b"}, 400, "bad_request"},
		{map[string]string{"url": url}, 400, "bad_request"},
	} {
		e = apiError{}
		if rec := ts.call(t, "POST", "/api/v1/admin/import/jellyfin/users", ts.adminToken, local, c.body, &e); rec.Code != c.status || e.Error.Code != c.code {
			t.Errorf("%v: %d %+v, want %d %s", c.body, rec.Code, e, c.status, c.code)
		}
	}
	ts.addUser(t, "ada", "correct horse", true)
	noor := ts.signIn(t, "noor", false, "tv")
	if rec := ts.call(t, "POST", "/api/v1/admin/import/jellyfin/users", noor, "", map[string]string{"url": url, "api_key": jf.Key}, nil); rec.Code != 403 {
		t.Fatalf("someone who is no admin: %d", rec.Code)
	}
}

func TestImportAPI(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	ts.addUser(t, "ada", "correct horse", true)
	gate := tasks.NewGate()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	im := jellyfin.NewImporter(ts.db, t.TempDir(), gate, log)
	q := tasks.New(ts.db, gate, log)
	q.Register(im.Kind())
	ts.imports = im
	drain := func() {
		t.Helper()
		if err := q.Drain(ctx); err != nil {
			t.Fatal(err)
		}
	}
	films, _ := ts.db.CreateLibrary(ctx, "Films", "movies", "default", []string{"/srv/films"}, 1)
	b := &browse{testServer: ts}
	b.applyAndBuild(t, films, db.ScanChanges{New: filmFiles("Low Water (2001)")}, 0)
	jf := &standin.Server{Key: "0123456789abcdef0123456789abcdef", Version: "12.4.0",
		Users: []standin.User{{ID: "4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c", Name: "Ada"}},
		Items: []standin.Item{{ID: "a1", Type: "Movie", Path: "/films/Low Water (2001)/Low Water (2001).mkv",
			ProviderIDs: map[string]string{"Tmdb": "101"},
			UserData:    map[string]standin.UserData{"4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c": {Played: true, PlayCount: 1, LastPlayedDate: "2026-10-01T20:00:00Z"}}},
			{ID: "a2", Type: "Movie", Path: "/films/Gone (2000)/Gone (2000).mkv"}}}
	url := jf.Start(t).URL
	body := map[string]any{"url": url, "api_key": jf.Key, "users": map[string]string{"ada": "ada"},
		"paths": []map[string]string{{"from": "/films", "to": "/srv/films"}}}

	var e apiError
	if rec := ts.call(t, "GET", "/api/v1/admin/import/jellyfin", ts.adminToken, local, nil, &e); rec.Code != 404 {
		t.Fatalf("before any import: %d", rec.Code)
	}
	if rec := ts.call(t, "POST", "/api/v1/admin/import/jellyfin", ts.adminToken, local, body, nil); rec.Code != 202 {
		t.Fatalf("start: %d", rec.Code)
	}
	drain()
	var rep jellyfin.Report
	if rec := ts.call(t, "GET", "/api/v1/admin/import/jellyfin", ts.adminToken, local, nil, &rep); rec.Code != 200 ||
		rep.State != "done" || !rep.DryRun || rep.Joined != 1 || rep.NotJoined != 1 || rep.Matches != 1 || rep.States["ada"] != 1 ||
		len(rep.Missing) != 1 || rep.Missing[0].Reason != "no file at that path here" {
		t.Fatalf("a dry run, unless asked otherwise: %d %+v", rec.Code, rep)
	}
	body["dry_run"] = false
	release := gate.Hold()
	if rec := ts.call(t, "POST", "/api/v1/admin/import/jellyfin", ts.adminToken, local, body, nil); rec.Code != 202 {
		t.Fatalf("start the import: %d", rec.Code)
	}
	if rec := ts.call(t, "POST", "/api/v1/admin/import/jellyfin", ts.adminToken, local, body, &e); rec.Code != 409 || e.Error.Code != "conflict" {
		t.Fatalf("while one runs: %d %+v", rec.Code, e)
	}
	ts.call(t, "GET", "/api/v1/admin/import/jellyfin", ts.adminToken, local, nil, &rep)
	if rep.State != "running" {
		t.Fatalf("held at the gate: %+v", rep)
	}
	release()
	drain()
	ts.call(t, "GET", "/api/v1/admin/import/jellyfin?full=1", ts.adminToken, local, nil, &rep)
	if rep.State != "done" || rep.DryRun || rep.States["ada"] != 1 {
		t.Fatalf("the import: %+v", rep)
	}

	for _, c := range []struct {
		body   map[string]any
		status int
		code   string
	}{
		{map[string]any{"url": url, "api_key": jf.Key, "users": map[string]string{"kim": "ada"}}, 400, "bad_request"},
		{map[string]any{"url": url, "api_key": "wrong-key"}, 400, "bad_key"},
		{map[string]any{"url": "jellyfin.lan", "api_key": jf.Key}, 400, "bad_request"},
	} {
		e = apiError{}
		if rec := ts.call(t, "POST", "/api/v1/admin/import/jellyfin", ts.adminToken, local, c.body, &e); rec.Code != c.status || e.Error.Code != c.code {
			t.Errorf("%v: %d %+v, want %d %s", c.body, rec.Code, e, c.status, c.code)
		}
	}
	if rec := ts.call(t, "GET", "/api/v1/admin/import/jellyfin?full=yes", ts.adminToken, local, nil, &e); rec.Code != 400 {
		t.Fatalf("full=yes: %d", rec.Code)
	}
}

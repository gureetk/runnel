// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"testing"

	"github.com/gureetk/runnel/internal/db"
)

func TestParseReport(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	local := "127.0.0.1:40000"
	shows, err := ts.db.CreateLibrary(ctx, "Shows", "shows", "default", []string{t.TempDir()}, 1)
	if err != nil {
		t.Fatal(err)
	}
	films, _ := ts.db.CreateLibrary(ctx, "Films", "movies", "default", []string{t.TempDir()}, 1)
	add := func(lib int64, root int64, paths ...string) {
		var c db.ScanChanges
		for _, p := range paths {
			c.New = append(c.New, db.SeenFile{RelPath: p, Kind: "video", Size: 10, MtimeNS: 1})
		}
		if err := ts.db.ApplyScan(ctx, root, c, 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	add(shows.ID, shows.Roots[0].ID,
		"Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv",
		"Harbor Lantern (2021)/Season 01/Harbor Lantern - 02.mkv",
		"Harbor Lantern (2021)/Season 01/Other Title 03.mkv",
		"Harbor Lantern (2021)/Season 01/Pilot.mkv")
	add(films.ID, films.Roots[0].ID, "Paper Comets (2019)/Paper Comets (2019).mkv", "Glass River/Glass River.mkv")
	known, _ := ts.db.KnownFiles(ctx, films.Roots[0].ID)
	bad := known["Paper Comets (2019)/Paper Comets (2019).mkv"]
	ts.db.SaveProbeError(ctx, db.ProbeTarget{FileID: bad.ID, Size: 10, MtimeNS: 1}, "Invalid data found when processing input", "", 1, 2)

	var r struct {
		Libraries []reportLibrary    `json:"libraries"`
		Files     []reportFile       `json:"files"`
		Unread    []reportUnreadable `json:"unreadable"`
	}
	rec := ts.call(t, "GET", "/api/v1/admin/parse-report", ts.adminToken, local, nil, &r)
	if rec.Code != 200 || len(r.Libraries) != 2 {
		t.Fatalf("%d %+v", rec.Code, r)
	}
	s := r.Libraries[0]
	if s.Files != 4 || s.Counts["explicit"] != 1 || s.Counts["anchored"] != 1 || s.Counts["guess"] != 1 || s.Counts["none"] != 1 || s.Counts["dash"] != 0 {
		t.Fatalf("shows: %+v", s)
	}
	f := r.Libraries[1]
	if f.Files != 2 || f.Counts["explicit"] != 1 || f.Counts["guess"] != 1 || f.Unreadable != 1 {
		t.Fatalf("films: %+v", f)
	}
	// Listed by default: the guess and none classes.
	if len(r.Files) != 3 {
		t.Fatalf("%d files listed, want 3: %+v", len(r.Files), r.Files)
	}
	for _, row := range r.Files {
		if row.Class != "guess" && row.Class != "none" {
			t.Fatalf("listed: %+v", row)
		}
		if row.Path == "Harbor Lantern (2021)/Season 01/Other Title 03.mkv" && (row.Episode == nil || *row.Episode != 3 || *row.Season != 1) {
			t.Fatalf("a guess: %+v", row)
		}
	}
	if len(r.Unread) != 1 || r.Unread[0].Error != "Invalid data found when processing input" {
		t.Fatalf("unreadable: %+v", r.Unread)
	}

	rec = ts.call(t, "GET", "/api/v1/admin/parse-report?class=all&library=1", ts.adminToken, local, nil, &r)
	if rec.Code != 200 || len(r.Libraries) != 1 || len(r.Files) != 4 {
		t.Fatalf("all of library 1: %d %+v", rec.Code, r)
	}
	var e apiError
	if rec := ts.call(t, "GET", "/api/v1/admin/parse-report?class=sure", ts.adminToken, local, nil, &e); rec.Code != 400 {
		t.Fatalf("a bad class: %d", rec.Code)
	}
	if rec := ts.call(t, "GET", "/api/v1/admin/parse-report?library=9", ts.adminToken, local, nil, &e); rec.Code != 404 {
		t.Fatalf("an unknown library: %d", rec.Code)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"strings"
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
	if s.Files != 4 || s.Counts["explicit"] != 1 || s.Counts["anchored"] != 1 || s.Counts["guess"] != 1 || s.Counts["unnumbered"] != 1 || s.Counts["none"] != 0 || s.Counts["dash"] != 0 {
		t.Fatalf("shows: %+v", s)
	}
	f := r.Libraries[1]
	if f.Files != 2 || f.Counts["explicit"] != 1 || f.Counts["guess"] != 1 || f.Unreadable != 1 {
		t.Fatalf("films: %+v", f)
	}
	// Listed by default: the guess, unnumbered and none classes.
	if len(r.Files) != 3 {
		t.Fatalf("%d files listed, want 3: %+v", len(r.Files), r.Files)
	}
	for _, row := range r.Files {
		if row.Class != "guess" && row.Class != "unnumbered" && row.Class != "none" {
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
	if rec := ts.call(t, "GET", "/api/v1/admin/parse-report?noted=yes", ts.adminToken, local, nil, &e); rec.Code != 400 {
		t.Fatalf("a bad noted: %d", rec.Code)
	}
}

// TestParseReportNotes checks the net for a wrong number in a trusted class:
// files that make one item and whose names do not tell them apart get a
// note, are counted, and are listed by default and with noted=1.
func TestParseReportNotes(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	local := "127.0.0.1:40000"
	shows, err := ts.db.CreateLibrary(ctx, "Shows", "shows", "default", []string{t.TempDir()}, 1)
	if err != nil {
		t.Fatal(err)
	}
	films, _ := ts.db.CreateLibrary(ctx, "Films", "movies", "default", []string{t.TempDir()}, 1)
	add := func(root int64, paths ...string) {
		var c db.ScanChanges
		for _, p := range paths {
			c.New = append(c.New, db.SeenFile{RelPath: p, Kind: "video", Size: 10, MtimeNS: 1})
		}
		if err := ts.db.ApplyScan(ctx, root, c, 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	add(shows.Roots[0].ID,
		// Two releases of one episode, a file and the same name with a v2: noted.
		"Salt Road (2010)/Season 01/Salt Road - 05.mkv",
		"Salt Road (2010)/Season 01/Salt Road - 05v2.mkv",
		// Two versions with labels: not noted.
		"Harbor Lantern (2021)/Season 01/Harbor Lantern S01E01 - 1080p.mkv",
		"Harbor Lantern (2021)/Season 01/Harbor Lantern S01E01 - 720p.mkv",
		// A folder that is not a season folder: noted on its own.
		"Harbor Lantern (2021)/S02/Harbor Lantern S02E01.mkv",
		// Without a number, with the reason as its note: not counted as noted.
		"Harbor Lantern (2021)/Season 01/Pilot.mkv")
	add(films.Roots[0].ID,
		"Paper Comets (2019)/Paper Comets (2019).mkv",
		"Paper Comets (2019)/Paper Comets (2019).mp4", // the same name twice: noted
		"Glass River (2011)/Glass River (2011) - cd1.mkv",
		"Glass River (2011)/Glass River (2011) - cd2.mkv")

	var r struct {
		Libraries []reportLibrary `json:"libraries"`
		Files     []reportFile    `json:"files"`
	}
	if rec := ts.call(t, "GET", "/api/v1/admin/parse-report", ts.adminToken, local, nil, &r); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	if r.Libraries[0].Noted != 3 || r.Libraries[1].Noted != 2 {
		t.Fatalf("noted: %+v", r.Libraries)
	}
	listed := map[string]reportFile{}
	for _, f := range r.Files {
		listed[f.Path] = f
	}
	for _, p := range []string{"Salt Road (2010)/Season 01/Salt Road - 05.mkv", "Salt Road (2010)/Season 01/Salt Road - 05v2.mkv"} {
		if f, ok := listed[p]; !ok || f.Class != "anchored" || !strings.Contains(f.Note, "one of 2 files that make S01E05") {
			t.Errorf("%s: %+v", p, f)
		}
	}
	if f := listed["Harbor Lantern (2021)/S02/Harbor Lantern S02E01.mkv"]; f.Note != `in the folder "S02", which is not a season folder` {
		t.Errorf("a folder that is not a season folder: %+v", f)
	}
	if f := listed["Paper Comets (2019)/Paper Comets (2019).mp4"]; !strings.Contains(f.Note, "one of 2 files that make the film") {
		t.Errorf("a film made by two files of one name: %+v", f)
	}
	for _, p := range []string{"Harbor Lantern (2021)/Season 01/Harbor Lantern S01E01 - 1080p.mkv", "Glass River (2011)/Glass River (2011) - cd1.mkv"} {
		if f, ok := listed[p]; ok {
			t.Errorf("versions and parts with good names are listed: %+v", f)
		}
	}
	if f, ok := listed["Harbor Lantern (2021)/Season 01/Pilot.mkv"]; !ok || f.Class != "unnumbered" || f.Episode != nil || f.Season == nil || *f.Season != 1 {
		t.Errorf("an episode without a number is still listed, with no number: %+v", f)
	}
	if len(r.Files) != 6 {
		t.Errorf("%d files listed, want the 5 noted and the one in none: %+v", len(r.Files), r.Files)
	}

	// noted=1 lists the noted files of every class, and nothing else.
	if rec := ts.call(t, "GET", "/api/v1/admin/parse-report?noted=1", ts.adminToken, local, nil, &r); rec.Code != 200 || len(r.Files) != 5 {
		t.Fatalf("noted=1: %d %+v", rec.Code, r.Files)
	}
	for _, f := range r.Files {
		if f.Note == "" || f.Class == "none" {
			t.Errorf("noted=1 lists %+v", f)
		}
	}
	// A class alone lists that class only, notes or not.
	if rec := ts.call(t, "GET", "/api/v1/admin/parse-report?class=explicit", ts.adminToken, local, nil, &r); rec.Code != 200 || len(r.Files) != 7 {
		t.Fatalf("class=explicit: %d %d files", rec.Code, len(r.Files))
	}
}

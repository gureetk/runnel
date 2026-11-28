// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/meta/tmdb"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/probe"
	"github.com/gureetk/runnel/internal/scan"
)

// withMatcher gives a fetch environment the matcher, as serve.go wires it.
func (e *fetchEnv) withMatcher() *Matcher {
	keys := func(context.Context) (string, error) { return e.key, nil }
	m := NewMatcher(e.db, e.client, keys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Queued = e.q.Wake
	m.Matched = e.f.Fetch
	e.q.Register(m.Kind())
	e.f.Match = m
	return m
}

// hit is one title in a search's answer: a film's, or with series set a
// series'.
type hit struct {
	id              int
	title, original string
	date            string
}

// searched sets a search's answer as TMDB gives it.
func (e *fetchEnv) searched(series bool, title string, year int, hits ...hit) {
	var results []any
	for _, h := range hits {
		r := map[string]any{"id": h.id, "popularity": 1.5, "poster_path": "/p" + strconv.Itoa(h.id) + ".jpg"}
		if h.original == "" {
			h.original = h.title
		}
		if series {
			r["name"], r["original_name"], r["first_air_date"] = h.title, h.original, h.date
		} else {
			r["title"], r["original_title"], r["release_date"] = h.title, h.original, h.date
		}
		results = append(results, r)
	}
	if results == nil {
		results = []any{}
	}
	b, _ := json.Marshal(map[string]any{"page": 1, "results": results, "total_pages": 1, "total_results": len(results)})
	if series {
		e.page(tmdb.SeriesSearchPath, tmdb.SeriesSearchQuery(title, year), string(b))
	} else {
		e.page(tmdb.MovieSearchPath, tmdb.MovieSearchQuery(title, year), string(b))
	}
}

// asked counts the requests to a path.
func (e *fetchEnv) asked(path string) int {
	n := 0
	for _, r := range e.tmdb.Requests() {
		if strings.HasSuffix(r.URL.Path, path) {
			n++
		}
	}
	return n
}

type noted struct {
	Reason     string  `json:"reason"`
	Candidates int     `json:"candidates"`
	Best       float64 `json:"best"`
	Title      string  `json:"title"`
	Year       *int    `json:"year"`
}

func (e *fetchEnv) note(id int64) noted {
	e.t.Helper()
	s, err := e.db.MatchNote(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	var n noted
	if s != "" {
		json.Unmarshal([]byte(s), &n)
	}
	return n
}

// TestMatchFilms: films without an id are searched for by the title and
// year of their names, and the best candidate is taken when it scores at
// least 0.90 and leads by 0.10 (06 §6.2, P4), then fetched. Near misses,
// with invented titles: the same title in two years, a sequel and its
// first film, a remake, a title in another script by its original title
// and romanised, a year that TMDB gives as the next one, and a folder
// without a year whose title two films share. What takes no match is on
// the review list with why, or unmatched without a result. A second
// round makes no request, and no item's id changes (01 §6, criteria 2, 3
// and 6, in a sandbox).
func TestMatchFilms(t *testing.T) {
	ctx := context.Background()
	e := newFetchEnv(t, "movies")
	e.withMatcher()
	films := []string{
		"Salt Road (2005)", "Tide Runner (2005)", "Tide Runner 2 (2008)", "Glass Harbor (2016)", "Glass Harbor",
		"波の音 (2010)", "Nami no Oto (2010)", "Winter Letters (2018)", "Ferry Signal (2016)",
	}
	for _, f := range films {
		e.put(f + "/" + f + ".mkv")
	}
	e.searched(false, "Salt Road", 2005, hit{202, "Salt Road", "", "2019-05-01"}, hit{201, "Salt Road", "", "2005-02-11"})
	e.searched(false, "Tide Runner", 2005, hit{301, "Tide Runner", "", "2005-07-01"}, hit{302, "Tide Runner 2", "", "2008-07-01"})
	e.searched(false, "Tide Runner 2", 2008, hit{302, "Tide Runner 2", "", "2008-07-01"}, hit{301, "Tide Runner", "", "2005-07-01"})
	e.searched(false, "Glass Harbor", 2016, hit{401, "Glass Harbor", "", "1958-10-01"}, hit{402, "Glass Harbor", "", "2016-03-04"})
	e.searched(false, "Glass Harbor", 0, hit{401, "Glass Harbor", "", "1958-10-01"}, hit{402, "Glass Harbor", "", "2016-03-04"})
	e.searched(false, "波の音", 2010, hit{501, "Sound of Waves", "波の音", "2010-09-01"})
	e.searched(false, "Nami no Oto", 2010, hit{501, "Sound of Waves", "波の音", "2010-09-01"})
	e.searched(false, "Winter Letters", 2018)
	e.searched(false, "Winter Letters", 0, hit{601, "Winter Letters", "", "2019-01-20"}, hit{602, "Winter Lettering", "", "1999-01-01"})
	e.searched(false, "Ferry Signal", 2016)
	e.searched(false, "Ferry Signal", 0)
	for _, id := range []int{201, 301, 302, 402, 501, 601} {
		e.page(tmdb.MoviePath(strconv.Itoa(id)), tmdb.MovieQuery(), movieJSON(id, "Fetched "+strconv.Itoa(id), "An overview.", "2005-01-01"))
	}

	ids := map[string]int64{}
	e.scan()
	for _, f := range films {
		ids[f] = e.itemIn(f).ID
	}
	for f, want := range map[string]string{
		"Salt Road (2005)": "201", "Tide Runner (2005)": "301", "Tide Runner 2 (2008)": "302", "Glass Harbor (2016)": "402",
		"波の音 (2010)": "501", "Winter Letters (2018)": "601",
	} {
		m := e.match(ids[f])
		if m.State != "matched" || m.ExternalIDs["tmdb"] != want || m.Confidence < Accept {
			t.Errorf("%s: %+v, want matched to %s", f, m, want)
		}
		if c, _ := e.db.Item(ctx, ids[f]); c.Title != "Fetched "+want {
			t.Errorf("%s: not fetched after its match: %q", f, c.Title)
		}
	}
	for f, want := range map[string]noted{
		"Glass Harbor":       {Reason: "too_close", Candidates: 2, Best: 0.9, Title: "Glass Harbor"},
		"Nami no Oto (2010)": {Reason: "low_score", Candidates: 1, Best: 0.4, Title: "Nami no Oto"},
	} {
		m, n := e.match(ids[f]), e.note(ids[f])
		if m.State != "review" || len(m.ExternalIDs) != 0 || n.Reason != want.Reason || n.Candidates != want.Candidates ||
			n.Best != want.Best || n.Title != want.Title {
			t.Errorf("%s: %+v %+v, want on the review list: %+v", f, m, n, want)
		}
	}
	if m, n := e.match(ids["Ferry Signal (2016)"]), e.note(ids["Ferry Signal (2016)"]); m.State != "unmatched" ||
		n.Reason != "no_results" || n.Year == nil || *n.Year != 2016 {
		t.Errorf("no results: %+v %+v", m, n)
	}
	// A year with no title near it asks again without the year; one with
	// a title near it asks once.
	if n := e.asked("/search/movie"); n != 11 {
		t.Errorf("%d searches, want 11: two for Winter Letters and Ferry Signal, one for each other film", n)
	}

	// A second round, a rescan and a nightly round within 30 days make no
	// request, and change no item's id.
	before := e.requests()
	e.scan()
	if _, err := e.f.Queue(ctx, 0, true); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if got := e.requests(); got != before {
		t.Errorf("a second round made %d requests", got-before)
	}
	for _, f := range films {
		if got := e.itemIn(f).ID; got != ids[f] {
			t.Errorf("%s: id %d, was %d", f, got, ids[f])
		}
	}

	// A renamed folder keeps its item, which takes a fingerprint, and is
	// searched for under its new name.
	known, _ := e.db.KnownFiles(ctx, e.lib.Roots[0].ID)
	for _, k := range known {
		p := filepath.Join(e.root, filepath.FromSlash(k.RelPath))
		fp, err := scan.Fingerprint(p)
		if err != nil {
			t.Fatal(err)
		}
		e.db.SaveProbe(ctx, db.ProbeTarget{FileID: k.ID, Path: p, Size: k.Size, MtimeNS: k.MtimeNS}, model.Probe{}, fp, probe.Version, 1)
	}
	os.Rename(filepath.Join(e.root, "Ferry Signal (2016)"), filepath.Join(e.root, "Ferry Signals (2016)"))
	e.searched(false, "Ferry Signals", 2016, hit{701, "Ferry Signals", "", "2016-06-06"})
	e.page(tmdb.MoviePath("701"), tmdb.MovieQuery(), movieJSON(701, "Ferry Signals", "", "2016-06-06"))
	e.scan()
	if c := e.itemIn("Ferry Signals (2016)"); c.ID != ids["Ferry Signal (2016)"] || e.match(c.ID).ExternalIDs["tmdb"] != "701" {
		t.Errorf("a renamed folder: %+v %+v", c, e.match(c.ID))
	}

	// An item's refresh searches again, asking the provider.
	before = e.asked("/search/movie")
	if err := e.f.Refresh(ctx, ids["Glass Harbor"], e.lib.ID); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if got := e.asked("/search/movie") - before; got != 1 || e.match(ids["Glass Harbor"]).State != "review" {
		t.Errorf("a refresh of an item on the review list: %d searches, %+v", got, e.match(ids["Glass Harbor"]))
	}

	// Without a key nothing is searched for.
	e.key = ""
	e.put("Copper Tower (2007)/Copper Tower (2007).mkv")
	before = e.requests()
	e.scan()
	if e.requests() != before {
		t.Error("searched without a key")
	}
}

// itemIn finds the film or series built from a folder.
func (e *fetchEnv) itemIn(folder string) db.Card {
	e.t.Helper()
	items, err := e.db.LibraryItems(context.Background(), e.lib.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, it := range items {
		if (it.Type == "movie" || it.Type == "series") && it.PathKey == folder {
			c, err := e.db.Item(context.Background(), it.ID)
			if err != nil {
				e.t.Fatal(err)
			}
			return c
		}
	}
	e.t.Fatalf("no item for %q", folder)
	return db.Card{}
}

// TestMatchInAMixedLibrary: a film and a series of one name, in a mixed
// library, are each searched for among their own kind, the series by the
// year of its first air date; a film or a series with an id in its name,
// one left unmatched on purpose, and an episode are not searched for.
func TestMatchInAMixedLibrary(t *testing.T) {
	e := newFetchEnv(t, "mixed")
	e.withMatcher()
	e.put("Copper Tower (2007)/Copper Tower (2007).mkv")
	e.put("Copper Tower (2010)/Season 01/Copper Tower - S01E01.mkv")
	e.put("Copper Tower (2010)/Season 01/Copper Tower - S01E02.mkv")
	e.put("Quiet Station (2012) [tmdbid-900]/Quiet Station (2012).mkv")
	e.searched(false, "Copper Tower", 2007, hit{801, "Copper Tower", "", "2007-04-04"})
	e.searched(true, "Copper Tower", 2010, hit{802, "Copper Tower", "", "2010-09-09"})
	e.page(tmdb.MoviePath("801"), tmdb.MovieQuery(), movieJSON(801, "Copper Tower", "", "2007-04-04"))
	e.page(tmdb.MoviePath("900"), tmdb.MovieQuery(), movieJSON(900, "Quiet Station", "", "2012-01-01"))
	series, _ := json.Marshal(map[string]any{"id": 802, "name": "Copper Tower", "first_air_date": "2010-09-09",
		"seasons": []any{map[string]any{"season_number": 1, "episode_count": 2}}})
	e.page(tmdb.SeriesPath("802"), tmdb.SeriesQuery(), string(series))
	e.scan()
	if m := e.match(e.itemIn("Copper Tower (2007)").ID); m.ExternalIDs["tmdb"] != "801" || m.State != "matched" {
		t.Errorf("the film: %+v", m)
	}
	if m := e.match(e.itemIn("Copper Tower (2010)").ID); m.ExternalIDs["tmdb"] != "802" || m.State != "matched" {
		t.Errorf("the series: %+v", m)
	}
	if e.asked("/search/movie") != 1 || e.asked("/search/tv") != 1 {
		t.Errorf("searches: %d films, %d series", e.asked("/search/movie"), e.asked("/search/tv"))
	}
}

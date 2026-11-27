// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"context"
	"maps"
	"testing"

	"github.com/gureetk/runnel/internal/db"
)

// match reads an item's match state and its provider ids.
func (e *env) match(id int64) db.Match {
	e.t.Helper()
	m, err := e.db.MatchOf(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

// TestIDsInNames: the ids a film's or a series' names carry are its match
// (docs/06-library-and-metadata.md, sections 4.5 and 6.1), written by the
// build, kept when the name loses them, replaced when the name names
// another title, and written once: a second build changes nothing.
func TestIDsInNames(t *testing.T) {
	films := newEnv(t, "movies")
	films.put("Paper Comets (2019) [tmdbid-101]/Paper Comets (2019) [tmdbid-101].mkv")
	// A file's ids add what its folder does not name.
	films.put("Salt Road (2010)/Salt Road (2010) [imdbid-tt0000202].mkv")
	films.put("Quiet Station (2012) [tmdbid-303]/Quiet Station (2012) [imdbid-tt0000303] - 1080p.mkv")
	films.put("Quiet Station (2012) [tmdbid-303]/Quiet Station (2012) [imdbid-tt0000303] - 720p.mkv")
	films.put("No Ids (2001)/No Ids (2001).mkv")
	films.scan()

	items := films.items()
	for title, want := range map[string]map[string]string{
		"Paper Comets":  {"tmdb": "101"},
		"Salt Road":     {"imdb": "tt0000202"},
		"Quiet Station": {"tmdb": "303", "imdb": "tt0000303"},
		"No Ids":        {},
	} {
		it := byTitle(items, "movie", title)
		if it == nil {
			t.Fatalf("no film %q in %+v", title, items)
		}
		m := films.match(it.ID)
		state := "matched"
		if len(want) == 0 {
			state = "unmatched"
		}
		if m.State != state || !maps.Equal(m.ExternalIDs, want) {
			t.Errorf("%s: %s %v, want %s %v", title, m.State, m.ExternalIDs, state, want)
		}
	}
	if res, err := Build(context.Background(), films.db, films.lib.ID, 1); err != nil || res != (db.BuildResult{}) {
		t.Errorf("a second build changed %+v (%v)", res, err)
	}

	// The import's ids that agree, and add, are kept with the name's.
	paper := byTitle(items, "movie", "Paper Comets")
	if _, err := films.db.ImportMatches(context.Background(), []db.ImportMatch{{ItemID: paper.ID,
		IDs: map[string]string{"tmdb": "101", "imdb": "tt0000101"}}}, true); err != nil {
		t.Fatal(err)
	}
	if res, err := Build(context.Background(), films.db, films.lib.ID, 1); err != nil || res != (db.BuildResult{}) {
		t.Errorf("a build after an import that agrees changed %+v (%v)", res, err)
	}

	// A name that names another title replaces every id, and the film is
	// stamped; one that loses its ids keeps them.
	before := films.seq()
	films.fingerprint()
	films.move("Paper Comets (2019) [tmdbid-101]", "Paper Comets (2019) [tmdbid-111]")
	films.move("Salt Road (2010)/Salt Road (2010) [imdbid-tt0000202].mkv", "Salt Road (2010)/Salt Road (2010).mkv")
	films.scan()
	items = films.items()
	moved := byTitle(items, "movie", "Paper Comets")
	if moved == nil || moved.ID != paper.ID {
		t.Fatalf("the renamed film is %+v, want id %d", moved, paper.ID)
	}
	if m := films.match(moved.ID); m.State != "matched" || !maps.Equal(m.ExternalIDs, map[string]string{"tmdb": "111"}) {
		t.Errorf("after a new id in its name: %s %v, want tmdb 111 alone", m.State, m.ExternalIDs)
	}
	if moved.UpdatedSeq <= before {
		t.Errorf("the film with a new id was not stamped: %d, before %d", moved.UpdatedSeq, before)
	}
	salt := byTitle(items, "movie", "Salt Road")
	if m := films.match(salt.ID); m.State != "matched" || m.ExternalIDs["imdb"] != "tt0000202" {
		t.Errorf("a name that lost its ids: %s %v, want the id kept", m.State, m.ExternalIDs)
	}

	// A series by its folder's name; a lone file by its own.
	shows := newEnv(t, "shows")
	shows.put("Harbor Lantern (2021) [tvdbid-404]/Season 01/Harbor Lantern - S01E01 [tvdbid-999].mkv")
	shows.scan()
	series := byTitle(shows.items(), "series", "Harbor Lantern")
	if m := shows.match(series.ID); m.State != "matched" || !maps.Equal(m.ExternalIDs, map[string]string{"tvdb": "404"}) {
		t.Errorf("series: %s %v, want tvdb 404 from its folder", m.State, m.ExternalIDs)
	}
	lone := newEnv(t, "movies")
	lone.put("Copper Tower (2007) [imdbid-tt0000505].mkv")
	lone.scan()
	tower := byTitle(lone.items(), "movie", "Copper Tower")
	if m := lone.match(tower.ID); m.State != "matched" || m.ExternalIDs["imdb"] != "tt0000505" {
		t.Errorf("a lone file: %s %v", m.State, m.ExternalIDs)
	}
}

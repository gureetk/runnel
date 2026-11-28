// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"context"
	"slices"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/parse"
)

func (e *env) card(id int64) db.Card {
	e.t.Helper()
	c, err := e.db.Item(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) build() db.BuildResult {
	e.t.Helper()
	res, err := Build(context.Background(), e.db, e.lib.ID, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

// TestMetadataOnAFilm: what a provider says of a film is on its card, its
// title stays through builds, a second write of the same changes nothing,
// its alternative titles are searched, and after 180 days without a
// refresh it goes, and the name titles the film again (D53).
func TestMetadataOnAFilm(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "movies")
	e.put("Papr Comets (2019)/Papr Comets (2019).mkv")
	e.scan()
	film := byTitle(e.items(), "movie", "Papr Comets")
	m := db.Metadata{Title: "Paper Comets", SortTitle: parse.SortTitle("Paper Comets"), OriginalTitle: "Papierkometen",
		Overview: "Two sisters chart a comet from a rooftop.", Date: "2019-03-08", RuntimeMS: 6240000, Rating: 7.2,
		ContentRating: "PG", Genres: []string{"Family", "Drama", "Family"}, Aliases: []string{"Rooftop Comets"},
		IDs: map[string]string{"imdb": "tt0000101", "tmdb": "999"}}
	if err := e.db.ApplyMetadata(ctx, film.ID, m, 1000); err != nil {
		t.Fatal(err)
	}
	c := e.card(film.ID)
	if c.Title != "Paper Comets" || c.Year != 2019 || c.Overview != m.Overview || c.Date != "2019-03-08" ||
		c.RuntimeMS != 6240000 || c.Rating != 7.2 || c.ContentRating != "PG" || c.OriginalTitle != "Papierkometen" ||
		!slices.Equal(c.Genres, []string{"Drama", "Family"}) {
		t.Errorf("the card after metadata: %+v", c)
	}
	if match, _ := e.db.MatchOf(ctx, film.ID); match.ExternalIDs["imdb"] != "tt0000101" || match.ExternalIDs["tmdb"] != "" {
		t.Errorf("ids: %v, want imdb from the details and no tmdb id written by them", match.ExternalIDs)
	}
	seq := c.Seq
	if err := e.db.ApplyMetadata(ctx, film.ID, m, 2000); err != nil {
		t.Fatal(err)
	}
	if c := e.card(film.ID); c.Seq != seq {
		t.Errorf("the same metadata again stamped the film: %d, was %d", c.Seq, seq)
	}
	if res := e.build(); res != (db.BuildResult{}) {
		t.Errorf("a build after metadata changed %+v", res)
	}
	if c := e.card(film.ID); c.Title != "Paper Comets" {
		t.Errorf("the build wrote the name's title over the provider's: %q", c.Title)
	}
	for _, q := range []string{"Rooftop", "Papierkom", "Paper Com"} {
		found, err := e.db.Search(ctx, q, []string{"movie"}, 10)
		if err != nil || len(found) != 1 || found[0].ID != film.ID {
			t.Errorf("search %q: %+v (%v)", q, found, err)
		}
	}

	// D53: not refreshed since before 1500, it goes back to its name.
	n, err := e.db.DropStale(ctx, 1500)
	if err != nil || n != 0 {
		t.Fatalf("dropped %d (%v), want none: it was refreshed at 2000", n, err)
	}
	if n, err = e.db.DropStale(ctx, 2001); err != nil || n != 1 {
		t.Fatalf("dropped %d (%v), want the film", n, err)
	}
	if res := e.build(); res.Changed != 1 {
		t.Errorf("the build after the drop changed %+v, want the film's title", res)
	}
	c = e.card(film.ID)
	if c.Title != "Papr Comets" || c.Overview != "" || c.Date != "" || c.Rating != 0 || c.Genres != nil || c.OriginalTitle != "" {
		t.Errorf("after the drop and a build: %+v", c)
	}
	if match, _ := e.db.MatchOf(ctx, film.ID); match.ExternalIDs["imdb"] != "tt0000101" {
		t.Errorf("the drop took the ids: %v", match.ExternalIDs)
	}
	if found, _ := e.db.Search(ctx, "Rooftop", []string{"movie"}, 10); len(found) != 0 {
		t.Errorf("an alternative title is still searched after the drop: %+v", found)
	}
}

// TestMetadataOnASeason: a season's episodes take their provider's titles
// by number, one file of two episodes both titles, one the provider does
// not have keeps its own, and an episode that gets another number loses
// what was said of its old one. A series in one running count waits.
func TestMetadataOnASeason(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "shows")
	e.put("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv")
	e.put("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E02-E03.mkv")
	e.put("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E09.mkv")
	e.put("Harbor Lantern (2021)/Season 01/Harbor Lantern - 04.5.mkv")
	e.put("Salt Road (2010)/Salt Road - 001.mkv")
	e.put("Salt Road (2010)/Salt Road - 120.mkv")
	e.scan()
	e.fingerprint()
	items := e.items()
	series := byTitle(items, "series", "Harbor Lantern")
	s := db.SeasonMetadata{Number: 1, Title: "Season 1", Overview: "The harbor freezes.", Date: "2021-04-05", Episodes: []db.EpisodeMetadata{
		{Number: 1, Title: "The Quiet Morning", Overview: "A storm closes the harbor.", Date: "2021-04-05", RuntimeMS: 1440000, Rating: 7.9},
		{Number: 2, Title: "Low Tide", Date: "2021-04-12", RuntimeMS: 1400000},
		{Number: 3, Title: "North Light", Date: "2021-04-19", RuntimeMS: 1500000},
	}}
	if err := e.db.ApplySeason(ctx, series.ID, s, parse.SortTitle, 1000); err != nil {
		t.Fatal(err)
	}
	seasons, eps := children(e.items(), series.ID)
	if len(seasons) != 1 || seasons[0].Title != "Season 1" {
		t.Errorf("seasons: %+v", seasons)
	}
	if c := e.card(seasons[0].ID); c.Overview != "The harbor freezes." || c.Date != "2021-04-05" {
		t.Errorf("the season's card: %+v", c)
	}
	titles := map[int]string{}
	for _, ep := range eps {
		titles[ep.Episode] = ep.Title
	}
	if titles[1] != "The Quiet Morning" || titles[2] != "Low Tide / North Light" || titles[9] != "Episode 9" || titles[0] != "Harbor Lantern - 04.5" {
		t.Errorf("episode titles: %v", titles)
	}
	two := episodeAt(eps, 1, 2)
	if c := e.card(two.ID); c.RuntimeMS != 2900000 || c.Date != "2021-04-12" {
		t.Errorf("a file of two episodes: %+v", c)
	}
	if res := e.build(); res != (db.BuildResult{}) {
		t.Errorf("a build after a season's metadata changed %+v", res)
	}

	// The first episode's file renamed to episode 5: it keeps its id and
	// loses what was said of episode 1.
	first := episodeAt(eps, 1, 1)
	e.move("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv", "Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E05.mkv")
	e.scan()
	_, eps = children(e.items(), series.ID)
	moved := episodeAt(eps, 1, 5)
	if moved == nil || moved.ID != first.ID || moved.Title != "Episode 5" {
		t.Fatalf("the renumbered episode: %+v, want id %d titled Episode 5", moved, first.ID)
	}
	if c := e.card(moved.ID); c.Overview != "" || c.Rating != 0 || c.Date != "" {
		t.Errorf("the renumbered episode kept its old metadata: %+v", c)
	}

	// A running count is left to its mapping (06, section 6.5).
	running := byTitle(items, "series", "Salt Road")
	if err := e.db.ApplySeason(ctx, running.ID, db.SeasonMetadata{Number: 1, Episodes: []db.EpisodeMetadata{{Number: 1, Title: "The Long Swim"}}}, parse.SortTitle, 1000); err != nil {
		t.Fatal(err)
	}
	_, eps = children(e.items(), running.ID)
	for _, ep := range eps {
		if ep.Title == "The Long Swim" {
			t.Errorf("an episode of a running count took a season's title by number: %+v", ep)
		}
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/meta/tmdb"
	"github.com/gureetk/runnel/internal/meta/tmdb/standin"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/scan"
	"github.com/gureetk/runnel/internal/tasks"
)

// inventedKey is the stand-in's key: an API key in form, not one TMDB
// gave anyone.
const inventedKey = "0000invented0000key0000"

type fetchEnv struct {
	t     *testing.T
	db    *db.DB
	root  string
	lib   model.Library
	s     *scan.Scanner
	q     *tasks.Queue
	f     *Fetcher
	tmdb  *standin.Server
	key   string
	built int
}

func newFetchEnv(t *testing.T, kind string) *fetchEnv {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "runnel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	root := filepath.Join(t.TempDir(), "media")
	os.MkdirAll(root, 0o755)
	lib, err := d.CreateLibrary(ctx, "Test", kind, "default", []string{root}, 1)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &fetchEnv{t: t, db: d, root: root, lib: lib, tmdb: &standin.Server{APIKey: inventedKey, Pages: map[string]string{}}, key: inventedKey}
	ts := e.tmdb.Start(t)
	keys := func(context.Context) (string, error) { return e.key, nil }
	client := tmdb.New(d, keys, "test", log)
	client.SetBase(ts.URL + "/3")
	e.f = NewFetcher(d, client, keys, log)
	e.q = tasks.New(d, tasks.NewGate(), log)
	e.f.Queued = e.q.Wake
	e.f.Resume = func() { e.q.Resume("fetch") }
	e.q.Register(e.f.Kind())
	e.s = scan.New(d, log)
	e.s.Built = func(ctx context.Context, id int64, scope db.Scope) error {
		if _, err := library.BuildIn(ctx, d, id, scope, 1); err != nil {
			return err
		}
		e.built++
		_, err := e.f.Queue(ctx, id, false)
		return err
	}
	return e
}

func (e *fetchEnv) put(rel string) {
	p := filepath.Join(e.root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(rel), 0o644)
	then := time.Now().Add(-time.Hour)
	os.Chtimes(p, then, then)
}

// scan scans the library, and runs the fetches its build queued.
func (e *fetchEnv) scan() {
	e.t.Helper()
	lib, _ := e.db.Library(context.Background(), e.lib.ID)
	if _, err := e.s.ScanLibrary(context.Background(), lib); err != nil {
		e.t.Fatal(err)
	}
	e.drain()
}

func (e *fetchEnv) drain() {
	e.t.Helper()
	if err := e.q.Drain(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

// page sets the stand-in's answer to a request as the client asks it.
func (e *fetchEnv) page(path string, q map[string][]string, body string) {
	e.tmdb.Set(tmdb.CacheKey(path, q), body)
}

func (e *fetchEnv) requests() int { return len(e.tmdb.Requests()) }

// fetches are the fetch tasks; a scan queues probes too.
func (e *fetchEnv) fetches() []db.TaskRow {
	all, err := e.db.Tasks(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	var out []db.TaskRow
	for _, t := range all {
		if t.Kind == "fetch" {
			out = append(out, t)
		}
	}
	return out
}

func (e *fetchEnv) item(typ, title string) db.Card {
	e.t.Helper()
	items, err := e.db.LibraryItems(context.Background(), e.lib.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, it := range items {
		if it.Type == typ && it.Title == title {
			c, err := e.db.Item(context.Background(), it.ID)
			if err != nil {
				e.t.Fatal(err)
			}
			return c
		}
	}
	e.t.Fatalf("no %s %q in %+v", typ, title, items)
	return db.Card{}
}

func (e *fetchEnv) match(id int64) db.Match {
	m, err := e.db.MatchOf(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func movieJSON(id int, title, overview, date string) string {
	b, _ := json.Marshal(map[string]any{
		"id": id, "title": title, "original_title": title, "overview": overview, "release_date": date,
		"runtime": 104, "vote_average": 7.234, "vote_count": 120, "genres": []any{map[string]any{"id": 18, "name": "Drama"}},
		"imdb_id":      "tt" + strings.Repeat("0", 6) + "1",
		"external_ids": map[string]any{"imdb_id": "tt0000001"},
		"release_dates": map[string]any{"results": []any{
			map[string]any{"iso_3166_1": "DE", "release_dates": []any{map[string]any{"certification": "12", "type": 3}}},
			map[string]any{"iso_3166_1": "US", "release_dates": []any{
				map[string]any{"certification": "", "type": 1}, map[string]any{"certification": "PG", "type": 4},
				map[string]any{"certification": "PG-13", "type": 3}}},
		}},
		"credits": map[string]any{"cast": []any{}, "crew": []any{}},
		"images":  map[string]any{"backdrops": []any{}, "logos": []any{}, "posters": []any{}},
	})
	return string(b)
}

// TestFetchFilms: a film matched by an id in its name gets its details; an
// IMDb id finds TMDB's; an id TMDB does not have puts the film on the
// review list; a film without one is not asked for. A second full refresh
// makes no request (exit criterion 6), and an item's refresh asks again.
func TestFetchFilms(t *testing.T) {
	e := newFetchEnv(t, "movies")
	e.put("Papr Comets (2019) [tmdbid-101]/Papr Comets (2019).mkv")
	e.put("Salt Road (2010) [imdbid-tt0000202]/Salt Road (2010).mkv")
	e.put("Copper Tower (2007) [tmdbid-404]/Copper Tower (2007).mkv")
	e.put("Quiet Station (2012)/Quiet Station (2012).mkv")
	e.page("/movie/101", tmdb.MovieQuery(), movieJSON(101, "Paper Comets", "Two sisters chart a comet.", "2019-03-08"))
	e.page("/find/tt0000202", tmdb.FindQuery("imdb_id"), `{"movie_results":[{"id":202}],"tv_results":[]}`)
	e.page("/movie/202", tmdb.MovieQuery(), movieJSON(202, "Salt Road", "A road by the sea.", "2010-06-01"))
	e.scan()

	paper := e.item("movie", "Paper Comets")
	if paper.Overview != "Two sisters chart a comet." || paper.Date != "2019-03-08" || paper.Year != 2019 ||
		paper.RuntimeMS != 104*60000 || paper.Rating != 7.2 || paper.ContentRating != "PG-13" || len(paper.Genres) != 1 {
		t.Errorf("Paper Comets: %+v", paper)
	}
	if m := e.match(paper.ID); m.State != "matched" || m.ExternalIDs["tmdb"] != "101" || m.ExternalIDs["imdb"] != "tt0000001" {
		t.Errorf("Paper Comets' match: %+v", m)
	}
	salt := e.item("movie", "Salt Road")
	if m := e.match(salt.ID); m.ExternalIDs["tmdb"] != "202" || salt.Overview != "A road by the sea." {
		t.Errorf("Salt Road, found by its IMDb id: %+v %+v", m, salt)
	}
	tower := e.item("movie", "Copper Tower")
	var why note
	if c, err := e.db.MatchNote(context.Background(), tower.ID); err != nil || json.Unmarshal([]byte(c), &why) != nil {
		t.Fatalf("Copper Tower's note: %q (%v)", c, err)
	}
	if m := e.match(tower.ID); m.State != "review" || why != (note{Reason: "id_not_found", Provider: "tmdb", Value: "404"}) {
		t.Errorf("Copper Tower, whose id TMDB has not: %+v %+v", m, why)
	}
	if quiet := e.item("movie", "Quiet Station"); quiet.Overview != "" {
		t.Errorf("a film without an id was fetched: %+v", quiet)
	}
	asked := e.requests()
	if asked != 4 {
		t.Errorf("%d requests, want 4: two films, one search by IMDb id, one id TMDB has not", asked)
	}

	// Exit criterion 6: a full refresh of an unchanged library makes none,
	// twice; so does a scan that builds it again.
	for range 2 {
		if _, err := e.f.Queue(context.Background(), e.lib.ID, true); err != nil {
			t.Fatal(err)
		}
		e.drain()
	}
	e.scan()
	if n := e.requests(); n != asked {
		t.Errorf("a second full refresh made %d requests", n-asked)
	}
	// An item's refresh always asks.
	if err := e.f.Refresh(context.Background(), paper.ID, e.lib.ID); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := e.requests(); n != asked+1 {
		t.Errorf("an item's refresh made %d requests, want 1", n-asked)
	}
}

// TestFetchSeries: a series' details and the seasons it holds, by
// number; a season TMDB does not list keeps its titles and is not asked
// for again; a new episode takes its title from the season kept in the
// cache; a series that airs is asked again after a week, and only its
// last season with it.
func TestFetchSeries(t *testing.T) {
	ctx := context.Background()
	e := newFetchEnv(t, "shows")
	e.put("Harbor Lantern (2021) [tmdbid-500]/Season 01/Harbor Lantern - S01E01.mkv")
	e.put("Harbor Lantern (2021) [tmdbid-500]/Season 01/Harbor Lantern - S01E02.mkv")
	e.put("Harbor Lantern (2021) [tmdbid-500]/Season 02/Harbor Lantern - S02E01.mkv")
	e.put("Harbor Lantern (2021) [tmdbid-500]/Season 05/Harbor Lantern - S05E01.mkv")
	e.put("Harbor Lantern (2021) [tmdbid-500]/Specials/Harbor Lantern - S00E01.mkv")
	series := `{"id":500,"name":"Harbor Lantern","original_name":"Hafenlaterne","overview":"A harbor town.","first_air_date":"2021-04-05",
		"episode_run_time":[24],"vote_average":7.9,"vote_count":40,"genres":[{"id":18,"name":"Drama"}],"status":"Returning Series",
		"in_production":true,"next_episode_to_air":{"season_number":2},"last_episode_to_air":{"season_number":2},
		"seasons":[{"season_number":0,"episode_count":1},{"season_number":1,"episode_count":3},{"season_number":2,"episode_count":2}],
		"alternative_titles":{"results":[{"iso_3166_1":"GB","title":"The Lantern of the Harbour"}]},
		"content_ratings":{"results":[{"iso_3166_1":"US","rating":"TV-14"}]},
		"external_ids":{"imdb_id":"tt0000500","tvdb_id":5000},"aggregate_credits":{"cast":[],"crew":[]},"images":{}}`
	season := func(n int, names ...string) string {
		var eps []map[string]any
		for i, name := range names {
			eps = append(eps, map[string]any{"episode_number": i + 1, "name": name, "air_date": "2021-04-05", "runtime": 24,
				"vote_average": 7.5, "vote_count": 3})
		}
		b, _ := json.Marshal(map[string]any{"season_number": n, "name": map[int]string{0: "Specials", 1: "Season 1", 2: "Season 2"}[n],
			"air_date": "2021-04-05", "episodes": eps})
		return string(b)
	}
	e.page("/tv/500", tmdb.SeriesQuery(), series)
	e.page("/tv/500/season/0", tmdb.SeasonQuery(), season(0, "The Lantern Maker"))
	e.page("/tv/500/season/1", tmdb.SeasonQuery(), season(1, "The Quiet Morning", "Low Tide", "North Light"))
	e.page("/tv/500/season/2", tmdb.SeasonQuery(), season(2, "Ice on the Bay"))
	e.scan()

	s := e.item("series", "Harbor Lantern")
	if s.Overview != "A harbor town." || s.ContentRating != "TV-14" || s.RuntimeMS != 24*60000 || s.OriginalTitle != "Hafenlaterne" {
		t.Errorf("the series: %+v", s)
	}
	if m := e.match(s.ID); m.ExternalIDs["tvdb"] != "5000" || m.ExternalIDs["imdb"] != "tt0000500" {
		t.Errorf("its ids: %v", m.ExternalIDs)
	}
	if found, _ := e.db.Search(ctx, "Harbour", []string{"series"}, 5); len(found) != 1 {
		t.Errorf("its alternative title is not searched: %+v", found)
	}
	for _, want := range []string{"The Quiet Morning", "Low Tide", "Ice on the Bay", "The Lantern Maker"} {
		e.item("episode", want)
	}
	e.item("episode", "Episode 1") // S05E01: TMDB lists no season 5
	if n := e.requests(); n != 4 {
		t.Errorf("%d requests, want 4: the series and its seasons 0, 1 and 2", n)
	}

	// A new episode: the season is read from the cache.
	e.put("Harbor Lantern (2021) [tmdbid-500]/Season 01/Harbor Lantern - S01E03.mkv")
	e.scan()
	e.item("episode", "North Light")
	if n := e.requests(); n != 4 {
		t.Errorf("a new episode made %d requests", n-4)
	}

	// Eight days on, a series that airs is fetched again, and its last
	// season; its older ones wait for their 30 days.
	e.f.now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	for _, k := range []string{"/tv/500", "/tv/500/season/0", "/tv/500/season/1", "/tv/500/season/2"} {
		q := tmdb.SeasonQuery()
		if k == "/tv/500" {
			q = tmdb.SeriesQuery()
		}
		ck := tmdb.CacheKey(k, q)
		c, ok, err := e.db.CacheGet(ctx, tmdb.Provider, ck)
		if err != nil || !ok {
			t.Fatalf("%s is not cached (%v)", ck, err)
		}
		if err := e.db.CachePut(ctx, tmdb.Provider, ck, c.Status, c.Body, c.FetchedAt-8*24*3600*1000); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.f.Queue(ctx, e.lib.ID, true); err != nil {
		t.Fatal(err)
	}
	e.drain()
	var asked []string
	for _, r := range e.tmdb.Requests()[4:] {
		asked = append(asked, r.URL.Path)
	}
	if strings.Join(asked, " ") != "/3/tv/500 /3/tv/500/season/2" {
		t.Errorf("a week on, the requests are %v, want the series and its last season", asked)
	}
}

// TestFetchWaits: without a key nothing is queued; a provider that asks to
// wait pauses the fetches without counting the run; a key it refuses
// pauses them until a new one is set.
func TestFetchWaits(t *testing.T) {
	ctx := context.Background()
	e := newFetchEnv(t, "movies")
	e.key = ""
	e.put("Paper Comets (2019) [tmdbid-101]/Paper Comets (2019).mkv")
	e.page("/movie/101", tmdb.MovieQuery(), movieJSON(101, "Paper Comets", "Two sisters chart a comet.", "2019-03-08"))
	e.scan()
	if all := e.fetches(); len(all) != 0 || e.requests() != 0 {
		t.Fatalf("without a key: tasks %+v, %d requests", all, e.requests())
	}

	e.key = inventedKey
	e.tmdb.Busy = []standin.Busy{{RetryAfter: "30"}}
	if _, err := e.f.Queue(ctx, e.lib.ID, false); err != nil {
		t.Fatal(err)
	}
	e.drain()
	all := e.fetches()
	if len(all) != 1 || all[0].State != "queued" || all[0].Attempts != 0 || !e.q.Paused("fetch") {
		t.Fatalf("after a 429: %+v, paused %v", all, e.q.Paused("fetch"))
	}

	// A refused key pauses the kind; a new key ends the pause.
	e2 := newFetchEnv(t, "movies")
	e2.key = "0000another0000key0000"
	e2.put("Paper Comets (2019) [tmdbid-101]/Paper Comets (2019).mkv")
	e2.scan()
	if !e2.q.Paused("fetch") {
		t.Fatal("a refused key did not pause the fetches")
	}
	e2.key = inventedKey
	e2.page("/movie/101", tmdb.MovieQuery(), movieJSON(101, "Paper Comets", "Two sisters chart a comet.", "2019-03-08"))
	e2.f.KeySet(ctx)
	e2.drain()
	if c := e2.item("movie", "Paper Comets"); c.Overview == "" || e2.q.Paused("fetch") {
		t.Errorf("after a new key: %+v, paused %v", c, e2.q.Paused("fetch"))
	}
}

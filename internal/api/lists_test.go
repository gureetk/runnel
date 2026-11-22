// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func titlesOf(p itemsPage) string {
	var out []string
	for _, c := range p.Items {
		out = append(out, c.Title)
	}
	return strings.Join(out, ", ")
}

func TestListSorts(t *testing.T) {
	b := newBrowse(t)
	get := func(query string) itemsPage {
		t.Helper()
		var p itemsPage
		if rec := b.call(t, "GET", "/api/v1/items?"+query, b.token, "", nil, &p); rec.Code != 200 {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body.String())
		}
		return p
	}
	// every reads a list a page of two at a time, by cursor.
	every := func(query string) string {
		t.Helper()
		var all []string
		p := get(query + "&limit=2")
		for {
			for _, c := range p.Items {
				all = append(all, c.Title)
			}
			if p.Next == nil {
				return strings.Join(all, ", ")
			}
			p = get(query + "&limit=2&cursor=" + *p.Next)
		}
	}
	for query, want := range map[string]string{
		"library=1":                       "Broken Lantern, An Echo Garden, Paper Comets, The Salt Road",
		"library=1&order=desc":            "The Salt Road, Paper Comets, An Echo Garden, Broken Lantern",
		"library=1&sort=date":             "Paper Comets, An Echo Garden, The Salt Road, Broken Lantern",
		"library=1&sort=date&order=asc":   "Broken Lantern, The Salt Road, An Echo Garden, Paper Comets",
		"library=1&sort=rating&order=asc": "Broken Lantern, An Echo Garden, Paper Comets, The Salt Road", // none rated: by title
		"library=1&sort=rating":           "Broken Lantern, An Echo Garden, Paper Comets, The Salt Road",
		"library=1&sort=added":            "Broken Lantern, An Echo Garden, Paper Comets, The Salt Road", // added together
	} {
		if got := every(query); got != want {
			t.Errorf("%s: %s, want %s", query, got, want)
		}
	}
	// Pages by position, with the total.
	if p := get("library=1&offset=2"); titlesOf(p) != "Paper Comets, The Salt Road" || p.Total == nil || *p.Total != 4 {
		t.Fatalf("from the third: %s %v", titlesOf(p), p.Total)
	}

	// What the user played.
	film := b.film(t, "Paper Comets")
	b.call(t, "PUT", "/api/v1/items/"+idStr(film.ID)+"/played", b.token, "", nil, nil)
	if p := get("library=1&played=1"); titlesOf(p) != "Paper Comets" || *p.Total != 1 {
		t.Fatalf("played: %s", titlesOf(p))
	}
	if p := get("library=1&played=0"); titlesOf(p) != "Broken Lantern, An Echo Garden, The Salt Road" {
		t.Fatalf("not played: %s", titlesOf(p))
	}
	eps := b.episodes(t)
	if p := get("library=2&played=0"); titlesOf(p) != "Harbor Lantern" {
		t.Fatalf("a series with an episode to play: %s", titlesOf(p))
	}
	b.call(t, "PUT", "/api/v1/items/played", b.token, "", map[string]any{"ids": []int64{eps[0].ID, eps[1].ID, eps[2].ID}}, nil)
	if p := get("library=2&played=1"); titlesOf(p) != "Harbor Lantern" {
		t.Fatalf("a series whose specials alone are left: %s", titlesOf(p))
	}

	// A genre: none until metadata arrives. Some items by id, in order.
	if p := get("genre=Drama"); len(p.Items) != 0 || *p.Total != 0 {
		t.Fatalf("a genre: %+v", p)
	}
	series := b.series(t)
	if p := get("ids=" + idStr(series.ID) + "," + idStr(eps[1].ID) + ",99999," + idStr(film.ID)); titlesOf(p) != "Harbor Lantern, Episode 2, Paper Comets" ||
		p.Items[1].State == nil || !p.Items[2].State.Played {
		t.Fatalf("by id: %s", titlesOf(p))
	}

	// Letters: where each begins in the list by title.
	var letters struct {
		Total   int
		Letters []struct {
			Letter        string
			Offset, Count int
		}
	}
	b.call(t, "GET", "/api/v1/items/letters?library=1", b.token, "", nil, &letters)
	if letters.Total != 4 || string(mustJSON(letters.Letters)) != `[{"Letter":"B","Offset":0,"Count":1},{"Letter":"E","Offset":1,"Count":1},{"Letter":"P","Offset":2,"Count":1},{"Letter":"S","Offset":3,"Count":1}]` {
		t.Fatalf("letters %+v", letters)
	}

	date := get("library=1&sort=date&limit=1")
	many := make([]string, 201)
	for i := range many {
		many[i] = "1"
	}
	for _, query := range []string{
		"library=1&collection=3", "person=4", "library=1&sort=listed", "library=1&order=up", "library=1&offset=-1",
		"library=1&offset=1&cursor=" + *date.Next, "library=1&cursor=" + *date.Next, "list=1&library=1",
		"ids=1,x", "ids=" + strings.Join(many, ","), "ids=1&library=1", "parent=" + idStr(series.ID) + "&sort=added",
		"series=" + idStr(series.ID) + "&played=1", "library=1&type=episode", "parent=" + idStr(series.ID) + "&library=2",
	} {
		var e apiError
		if rec := b.call(t, "GET", "/api/v1/items?"+query, b.token, "", nil, &e); rec.Code != 400 {
			t.Errorf("%s: %d %+v", query, rec.Code, e)
		}
	}
}

func TestHome(t *testing.T) {
	b := newBrowse(t)
	type home struct {
		Continue []cardBody `json:"continue"`
		NextUp   []cardBody `json:"next_up"`
		Recent   []cardBody `json:"recent"`
		List     []cardBody `json:"list"`
	}
	get := func(query string) home {
		t.Helper()
		var h home
		if rec := b.call(t, "GET", "/api/v1/home"+query, b.token, "", nil, &h); rec.Code != 200 {
			t.Fatalf("home%s: %d", query, rec.Code)
		}
		return h
	}
	// The fixture's files were found now, by the real clock: the test
	// clock starts there too.
	b.clockMu.Lock()
	b.clock = time.Now()
	b.clockMu.Unlock()
	h := get("")
	if len(h.Continue)+len(h.NextUp)+len(h.List) != 0 || len(h.Recent) != 5 {
		t.Fatalf("a new server's Home: %+v", h)
	}
	var series cardBody
	for _, c := range h.Recent {
		if c.Type == "series" {
			series = c
		}
	}
	if series.NewEpisodes == nil || *series.NewEpisodes != 4 || series.Counts == nil || series.Counts.Unplayed == nil {
		t.Fatalf("a series folds its new episodes: %+v", series)
	}

	film := b.film(t, "Paper Comets")
	file := b.fileOf(t, "Paper Comets").ID
	eps := b.episodes(t)
	b.call(t, "POST", "/api/v1/progress", b.token, "", map[string]any{"session": "h", "item_id": film.ID, "file_id": file,
		"position_ms": 40_000, "state": "paused", "mode": "direct"}, nil)
	b.call(t, "PUT", "/api/v1/items/"+idStr(eps[0].ID)+"/played", b.token, "", nil, nil)
	b.call(t, "PUT", "/api/v1/items/"+idStr(film.ID)+"/list", b.token, "", nil, nil)
	h = get("")
	if len(h.Continue) != 1 || h.Continue[0].ID != film.ID || h.Continue[0].State.PositionMS != 40_000 ||
		len(h.NextUp) != 1 || h.NextUp[0].ID != eps[1].ID || len(h.List) != 1 || h.List[0].ListedAt == nil {
		t.Fatalf("Home after a little use: %+v", h)
	}
	if h := get("?limit=1&library=1"); len(h.Recent) != 1 || h.Recent[0].Type != "movie" || len(h.NextUp) != 0 || len(h.Continue) != 1 {
		t.Fatalf("one library, one card a row: %+v", h)
	}

	// The tag moves with the day, as Recently added does.
	rec := b.call(t, "GET", "/api/v1/home", b.token, "", nil, nil)
	tag := rec.Header().Get("ETag")
	req := httptest.NewRequest("GET", "/api/v1/home", nil)
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("If-None-Match", tag)
	same := httptest.NewRecorder()
	b.Handler().ServeHTTP(same, req)
	if same.Code != 304 {
		t.Fatalf("Home with its tag: %d", same.Code)
	}
	b.advance(31 * 24 * time.Hour)
	if after := b.call(t, "GET", "/api/v1/home", b.token, "", nil, nil); after.Header().Get("ETag") == tag {
		t.Fatal("the tag stayed after a month")
	}
	if h := get(""); len(h.Recent) != 0 || len(h.Continue) != 1 {
		t.Fatalf("after a month nothing is recent: %+v", h)
	}
	for _, query := range []string{"?limit=0", "?limit=201", "?library=99", "?sort=title"} {
		var e apiError
		if rec := b.call(t, "GET", "/api/v1/home"+query, b.token, "", nil, &e); rec.Code != 400 && rec.Code != 404 {
			t.Errorf("%s: %d", query, rec.Code)
		}
	}
}

func TestSearchAndGenres(t *testing.T) {
	b := newBrowse(t)
	search := func(query string) string {
		t.Helper()
		var p itemsPage
		if rec := b.call(t, "GET", "/api/v1/search?"+query, b.token, "", nil, &p); rec.Code != 200 {
			t.Fatalf("%s: %d", query, rec.Code)
		}
		return titlesOf(p)
	}
	for query, want := range map[string]string{
		"q=lantern":                   "Broken Lantern, Harbor Lantern",
		"q=LANTERN&types=series":      "Harbor Lantern",
		"q=the":                       "The Salt Road",
		"q=pa":                        "Paper Comets",
		"q=sa":                        "The Salt Road",
		"q=e&limit=2":                 "An Echo Garden",
		"q=episode%202&types=episode": "Episode 2",
		"q=nothing%20like%20it":       "",
	} {
		if got := search(query); got != want {
			t.Errorf("%s: %q, want %q", query, got, want)
		}
	}
	for _, query := range []string{"q=", "q=%20", "q=a&types=person", "q=a&limit=0", "q=a&sort=title"} {
		if rec := b.call(t, "GET", "/api/v1/search?"+query, b.token, "", nil, nil); rec.Code != 400 {
			t.Errorf("%s: %d", query, rec.Code)
		}
	}

	var genres struct {
		Genres []any `json:"genres"`
	}
	if rec := b.call(t, "GET", "/api/v1/genres", b.token, "", nil, &genres); rec.Code != 200 || genres.Genres == nil || len(genres.Genres) != 0 {
		t.Fatalf("genres before metadata: %d %+v", rec.Code, genres)
	}
	if rec := b.call(t, "GET", "/api/v1/genres?library=99", b.token, "", nil, nil); rec.Code != 404 {
		t.Fatalf("genres of no library: %d", rec.Code)
	}
}

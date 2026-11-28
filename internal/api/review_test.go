// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/meta"
)

type reviewPage struct {
	Items  []reviewItemBody `json:"items"`
	Counts map[string]int   `json:"counts"`
	Next   *string          `json:"next"`
}

type matchAnswer struct {
	Match       matchBody         `json:"match"`
	ExternalIDs map[string]string `json:"external_ids"`
}

// fakeMatches answers every search with the same candidates, and keeps
// what it was asked.
type fakeMatches struct {
	asked []string
	cands []meta.Candidate
}

func (f *fakeMatches) Search(ctx context.Context, typ, title string, year int, age time.Duration) ([]meta.Candidate, error) {
	f.asked = append(f.asked, typ+" "+title+" "+idStr(int64(year)))
	return f.cands, nil
}

func (b *browse) review(t *testing.T, query string) reviewPage {
	t.Helper()
	var p reviewPage
	if rec := b.call(t, "GET", "/api/v1/admin/review"+query, b.token, "", nil, &p); rec.Code != 200 {
		t.Fatalf("review%s: %d %s", query, rec.Code, rec.Body.String())
	}
	return p
}

func onList(p reviewPage, id int64) *reviewItemBody {
	for i := range p.Items {
		if p.Items[i].ID == id {
			return &p.Items[i]
		}
	}
	return nil
}

// TestReviewAndMatch: everything without a match is on the review list
// with why, an episode without a number too (D51), in pages with counts;
// a match by hand, leaving an item unmatched and forgetting a match take
// it off the list and put it back, keep it by its folder, and never change
// an item's id (docs/04-api.md, section 8.4; 01 §6, criterion 5).
func TestReviewAndMatch(t *testing.T) {
	b := newBrowseWith(t,
		"Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv",
		"Harbor Lantern (2021)/Season 01/Harbor Lantern - The Lighthouse.mkv")
	f := &fakeFetches{}
	b.fetches = f
	ctx := context.Background()
	film, other, series := b.film(t, "Paper Comets"), b.film(t, "The Salt Road"), b.series(t)
	if _, err := b.db.ApplySearch(ctx, other.ID, "The Salt Road", 2005, db.SearchOutcome{Score: 0.9, Candidates: 2,
		Note: `{"reason":"too_close","candidates":2,"best":0.9,"second":0.9,"title":"The Salt Road","year":2005,"at":1}`}); err != nil {
		t.Fatal(err)
	}

	p := b.review(t, "")
	var unnumbered *reviewItemBody
	for i := range p.Items {
		if p.Items[i].Type == "episode" {
			unnumbered = &p.Items[i]
		}
	}
	if unnumbered == nil || unnumbered.State != "review" || string(unnumbered.Why) != `{"reason":"unnumbered"}` ||
		unnumbered.SeriesID != series.ID || unnumbered.Season == nil || *unnumbered.Season != 1 ||
		!strings.HasSuffix(unnumbered.Path, "/Harbor Lantern (2021)/Season 01/Harbor Lantern - The Lighthouse.mkv") {
		t.Fatalf("the episode without a number: %+v", unnumbered)
	}
	it := onList(p, film.ID)
	if it == nil || it.State != "unmatched" || string(it.Why) != `{"reason":"not_searched"}` || it.Year == nil || *it.Year != 2019 ||
		it.Library != "Films" || !strings.HasSuffix(it.Path, "/Paper Comets (2019)") {
		t.Fatalf("a film never searched for: %+v", it)
	}
	if it := onList(p, other.ID); it == nil || it.State != "review" || string(it.Why) != `{"best":0.9,"candidates":2,"reason":"too_close","second":0.9}` {
		t.Fatalf("a film on review: %+v", it)
	}
	if onList(p, series.ID) == nil || p.Counts["unmatched"] != 4 || p.Counts["review"] != 2 || len(p.Items) != 6 || p.Next != nil {
		t.Fatalf("the list: %+v", p)
	}
	if p := b.review(t, "?state=review"); len(p.Items) != 2 {
		t.Errorf("state=review: %+v", p.Items)
	}
	// Pages of two.
	seen := map[int64]bool{}
	q := "?limit=2"
	for range 4 {
		p := b.review(t, q)
		for _, it := range p.Items {
			seen[it.ID] = true
		}
		if p.Next == nil {
			break
		}
		q = "?limit=2&cursor=" + *p.Next
	}
	if len(seen) != 6 {
		t.Errorf("paging saw %d of 6", len(seen))
	}
	for _, bad := range []string{"?state=other", "?limit=0", "?cursor=nonsense"} {
		if rec := b.call(t, "GET", "/api/v1/admin/review"+bad, b.token, "", nil, nil); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}

	// A match by hand.
	path := func(id int64) string { return "/api/v1/admin/items/" + idStr(id) + "/match" }
	var m matchAnswer
	if rec := b.call(t, "PUT", path(film.ID), b.token, "", json.RawMessage(`{"provider":"tmdb","value":"101"}`), &m); rec.Code != 200 ||
		m.Match.State != "manual" || m.ExternalIDs["tmdb"] != "101" || len(f.refreshed) != 1 || f.refreshed[0] != film.ID {
		t.Fatalf("a match by hand: %d %+v %v", rec.Code, m, f.refreshed)
	}
	// Left unmatched on purpose.
	m = matchAnswer{}
	if rec := b.call(t, "PUT", path(other.ID), b.token, "", json.RawMessage(`{"provider":"none"}`), &m); rec.Code != 200 ||
		m.Match.State != "ignored" || len(m.ExternalIDs) != 0 {
		t.Fatalf("left unmatched: %d %+v", rec.Code, m)
	}
	// The episode without a number, left as it is.
	m = matchAnswer{}
	if rec := b.call(t, "PUT", path(unnumbered.ID), b.token, "", json.RawMessage(`{"provider":"none"}`), &m); rec.Code != 200 ||
		m.Match.State != "ignored" {
		t.Fatalf("an episode left as it is: %d %+v", rec.Code, m)
	}
	p = b.review(t, "")
	if onList(p, film.ID) != nil || onList(p, other.ID) != nil || onList(p, unnumbered.ID) != nil || p.Counts["unmatched"] != 3 || p.Counts["review"] != 0 {
		t.Fatalf("after matching: %+v", p)
	}

	// A build keeps what the folders' overrides say, and the ids.
	for _, lib := range []int64{1, 2} {
		if _, err := library.BuildIn(ctx, b.db, lib, nil, 2); err != nil {
			t.Fatal(err)
		}
	}
	if got := b.film(t, "Paper Comets"); got.ID != film.ID {
		t.Fatalf("a build gave the film id %d, was %d", got.ID, film.ID)
	}
	if mm, _ := b.db.MatchOf(ctx, film.ID); mm.State != "manual" || mm.ExternalIDs["tmdb"] != "101" {
		t.Errorf("after a build: %+v", mm)
	}
	if mm, _ := b.db.MatchOf(ctx, other.ID); mm.State != "ignored" {
		t.Errorf("left unmatched, after a build: %+v", mm)
	}

	// Forgetting a match puts the item back, searched for again.
	f.refreshed = nil
	for _, id := range []int64{film.ID, other.ID, unnumbered.ID} {
		m = matchAnswer{}
		if rec := b.call(t, "DELETE", path(id), b.token, "", nil, &m); rec.Code != 200 || m.Match.State != "unmatched" || len(m.ExternalIDs) != 0 {
			t.Fatalf("forget %d: %d %+v", id, rec.Code, m)
		}
	}
	if p := b.review(t, ""); len(p.Items) != 6 || len(f.refreshed) != 3 {
		t.Errorf("after forgetting: %+v, refreshed %v", p.Items, f.refreshed)
	}
	if got := b.film(t, "Paper Comets"); got.ID != film.ID {
		t.Errorf("forgetting gave the film id %d", got.ID)
	}

	// What takes no match, and what is malformed.
	var season itemsPage
	b.call(t, "GET", "/api/v1/items?parent="+idStr(series.ID), b.token, "", nil, &season)
	var numbered itemsPage
	b.call(t, "GET", "/api/v1/items?parent="+idStr(season.Items[0].ID), b.token, "", nil, &numbered)
	for _, c := range []struct {
		id   int64
		body string
		code int
	}{
		{season.Items[0].ID, `{"provider":"none"}`, 400},
		{numbered.Items[0].ID, `{"provider":"none"}`, 400},
		{unnumbered.ID, `{"provider":"tmdb","value":"5"}`, 400},
		{film.ID, `{"provider":"imdb","value":"tt1"}`, 400},
		{film.ID, `{"provider":"tmdb","value":"0101"}`, 400},
		{film.ID, `{"provider":"tmdb","value":"abc"}`, 400},
		{film.ID, `{"provider":"tmdb","value":"5","season_offset":1}`, 400},
		{series.ID, `{"provider":"tmdb","value":"5","numbering":"other"}`, 400},
		{99999, `{"provider":"none"}`, 404},
	} {
		if rec := b.call(t, "PUT", path(c.id), b.token, "", json.RawMessage(c.body), nil); rec.Code != c.code {
			t.Errorf("PUT %d %s: %d, want %d", c.id, c.body, rec.Code, c.code)
		}
	}
	m = matchAnswer{}
	if rec := b.call(t, "PUT", path(series.ID), b.token, "", json.RawMessage(`{"provider":"tmdb","value":"5","season_offset":1,"numbering":"absolute"}`), &m); rec.Code != 200 ||
		m.Match.State != "manual" {
		t.Errorf("a series with an offset: %d %+v", rec.Code, m)
	}
}

// TestCandidates: GET /admin/items/{id}/candidates searches for a film or
// a series by its own title and year, or by q and year, and answers the
// scored candidates with a link to each poster; without a key it is 409.
func TestCandidates(t *testing.T) {
	b := newBrowse(t)
	b.fetches = &fakeFetches{}
	fm := &fakeMatches{cands: []meta.Candidate{
		{Provider: "tmdb", ID: "402", Title: "Paper Comets", OriginalTitle: "Paper Comets", Year: 2019, PosterPath: "/p402.jpg", Score: 1},
		{Provider: "tmdb", ID: "403", Title: "Paper Comet", Year: 0, Score: 0.72},
	}}
	b.matches = fm
	film := b.film(t, "Paper Comets")
	path := "/api/v1/admin/items/" + idStr(film.ID) + "/candidates"
	var e apiError
	if rec := b.call(t, "GET", path, b.token, "", nil, &e); rec.Code != 409 || e.Error.Code != "no_key" {
		t.Fatalf("without a key: %d %+v", rec.Code, e)
	}
	key := "0000invented0000key0000"
	if err := b.db.SetMetadataKey(context.Background(), &key, 1); err != nil {
		t.Fatal(err)
	}
	raw := b.call(t, "GET", path, b.token, "", nil, nil).Body.String()
	want := `{"query":"Paper Comets","year":2019,"candidates":[` +
		`{"provider":"tmdb","id":"402","title":"Paper Comets","original_title":"Paper Comets","year":2019,"poster":"https://image.tmdb.org/t/p/w154/p402.jpg","score":1},` +
		`{"provider":"tmdb","id":"403","title":"Paper Comet","original_title":"","year":null,"poster":null,"score":0.72}]}`
	if strings.TrimSpace(raw) != want {
		t.Errorf("candidates:\n%s\nwant\n%s", raw, want)
	}
	b.call(t, "GET", path+"?q=Paper+Comet&year=", b.token, "", nil, nil)
	b.call(t, "GET", path+"?q=Paper+Comet&year=2018", b.token, "", nil, nil)
	if strings.Join(fm.asked, "|") != "movie Paper Comets 2019|movie Paper Comet 0|movie Paper Comet 2018" {
		t.Errorf("asked %v", fm.asked)
	}
	var season itemsPage
	b.call(t, "GET", "/api/v1/items?parent="+idStr(b.series(t).ID), b.token, "", nil, &season)
	for _, bad := range []string{path + "?year=19", path + "?provider=imdb", "/api/v1/admin/items/" + idStr(season.Items[0].ID) + "/candidates"} {
		if rec := b.call(t, "GET", bad, b.token, "", nil, nil); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
}

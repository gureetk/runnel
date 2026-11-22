// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/model"
)

type changesPage struct {
	Since      int64             `json:"since"`
	Upto       int64             `json:"upto"`
	Latest     int64             `json:"latest"`
	Reset      bool              `json:"reset"`
	Items      []json.RawMessage `json:"items"`
	State      []json.RawMessage `json:"state"`
	Prefs      []json.RawMessage `json:"prefs"`
	List       []json.RawMessage `json:"list"`
	Deleted    []int64           `json:"deleted"`
	Libraries  []json.RawMessage `json:"libraries"`
	LibraryIDs []int64           `json:"library_ids"`
}

// row is what a test needs to know of any row of a page.
type row struct {
	ID        int64  `json:"id"`
	ItemID    int64  `json:"item_id"`
	SeriesID  int64  `json:"series_id"`
	Type      string `json:"type"`
	LibraryID int64  `json:"library_id"`
	Season    *int   `json:"season"`
	ListedAt  *int64 `json:"listed_at"`
	Seq       int64  `json:"seq"`
}

// localCopy is a client's copy of the library, built from /changes as
// docs/04-api.md, section 5, tells a client to build it.
type localCopy struct {
	latest                    int64
	items, state, prefs, list map[int64]json.RawMessage
	libraries                 map[int64]json.RawMessage
	rows                      map[int64]row // items
}

func newCopy() *localCopy {
	return &localCopy{items: map[int64]json.RawMessage{}, state: map[int64]json.RawMessage{}, prefs: map[int64]json.RawMessage{},
		list: map[int64]json.RawMessage{}, libraries: map[int64]json.RawMessage{}, rows: map[int64]row{}}
}

func decodeRow(t *testing.T, raw json.RawMessage) row {
	t.Helper()
	var r row
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// sync brings a copy up to date a page at a time, and checks every page:
// whole sequence values only, its size, and that it leaves no episode or
// season without its parents. It returns how many pages it took.
func (b *browse) sync(t *testing.T, token string, limit int, c *localCopy) int {
	t.Helper()
	pageOfSeq := map[int64]int{}
	pages := 0
	for {
		var p changesPage
		target := "/api/v1/changes?since=" + strconv.FormatInt(c.latest, 10) + "&limit=" + strconv.Itoa(limit)
		if rec := b.call(t, "GET", target, token, "", nil, &p); rec.Code != 200 {
			t.Fatalf("%s: %d", target, rec.Code)
		}
		pages++
		if p.Reset {
			t.Fatalf("a page from %d was reset", c.latest)
		}
		if p.Since != c.latest || p.Upto < p.Since || p.Upto > p.Latest {
			t.Fatalf("page %d: since %d upto %d latest %d", pages, p.Since, p.Upto, p.Latest)
		}
		counted := 0
		count := func(seq int64) {
			if seq <= p.Since || seq > p.Upto {
				return
			}
			counted++
			if at, ok := pageOfSeq[seq]; ok && at != pages {
				t.Fatalf("value %d is split over pages %d and %d", seq, at, pages)
			}
			pageOfSeq[seq] = pages
		}
		for _, raw := range p.Items {
			r := decodeRow(t, raw)
			if r.Seq > p.Upto && r.Type != "series" && r.Type != "season" {
				t.Fatalf("an item above upto that is no parent: %s", raw)
			}
			count(r.Seq)
			c.items[r.ID], c.rows[r.ID] = raw, r
		}
		for _, raw := range p.State {
			r := decodeRow(t, raw)
			count(r.Seq)
			c.state[r.ItemID] = raw
		}
		for _, raw := range p.Prefs {
			r := decodeRow(t, raw)
			count(r.Seq)
			c.prefs[r.SeriesID] = raw
		}
		for _, raw := range p.List {
			r := decodeRow(t, raw)
			count(r.Seq)
			c.list[r.ItemID] = raw
		}
		for _, raw := range p.Libraries {
			r := decodeRow(t, raw)
			count(r.Seq)
			c.libraries[r.ID] = raw
		}
		counted += len(p.Deleted)
		for _, id := range p.Deleted {
			delete(c.items, id)
			delete(c.rows, id)
			delete(c.state, id)
			delete(c.list, id)
			delete(c.prefs, id)
		}
		if counted > max(limit, db.StampBatch) {
			t.Fatalf("page %d holds %d rows, with a limit of %d", pages, counted, limit)
		}
		live := map[int64]bool{}
		for _, id := range p.LibraryIDs {
			live[id] = true
		}
		for id, r := range c.rows {
			if !live[r.LibraryID] {
				delete(c.items, id)
				delete(c.rows, id)
			}
		}
		// Applied on its own, the copy has every episode's season and
		// series, and every season's series.
		seasons := map[string]bool{}
		for _, r := range c.rows {
			if r.Type == "season" {
				seasons[fmt.Sprint(r.SeriesID, "/", *r.Season)] = true
			}
		}
		for _, r := range c.rows {
			if r.Type != "season" && r.Type != "episode" {
				continue
			}
			if _, ok := c.rows[r.SeriesID]; !ok || (r.Type == "episode" && !seasons[fmt.Sprint(r.SeriesID, "/", *r.Season)]) {
				t.Fatalf("after page %d, %s %d has no parent in the copy", pages, r.Type, r.ID)
			}
		}
		c.latest = p.Upto
		if p.Upto == p.Latest {
			return pages
		}
	}
}

// equal reports how two copies differ: in items, state, the listings that
// are on and the choices that are set, and libraries.
func (c *localCopy) diff(o *localCopy) string {
	on := func(m map[int64]json.RawMessage, key string) map[int64]string {
		out := map[int64]string{}
		for k, v := range m {
			var f map[string]any
			json.Unmarshal(v, &f)
			if key != "" && f[key] == nil {
				continue
			}
			out[k] = string(v)
		}
		return out
	}
	for _, part := range []struct {
		name string
		a, b map[int64]string
	}{
		{"items", on(c.items, ""), on(o.items, "")},
		{"state", on(c.state, ""), on(o.state, "")},
		{"list", on(c.list, "listed_at"), on(o.list, "listed_at")},
		{"prefs", on(c.prefs, "sub_mode"), on(o.prefs, "sub_mode")},
		{"libraries", on(c.libraries, ""), on(o.libraries, "")},
	} {
		if !maps.Equal(part.a, part.b) {
			for k, v := range part.a {
				if part.b[k] != v {
					return fmt.Sprintf("%s %d: %s against %s", part.name, k, v, part.b[k])
				}
			}
			for k, v := range part.b {
				if _, ok := part.a[k]; !ok {
					return fmt.Sprintf("%s %d: missing, against %s", part.name, k, v)
				}
			}
		}
	}
	return ""
}

// bigLibraries adds a films library of 3,000 films and a shows library of
// 200 series, each of four seasons of 20 episodes: 20,000 items. The
// files are rows only: nothing is probed.
func bigLibraries(t *testing.T, ts *testServer) (films, shows model.Library) {
	t.Helper()
	ctx := context.Background()
	var err error
	if films, err = ts.db.CreateLibrary(ctx, "Films", "movies", "default", []string{"/big/films"}, 1); err != nil {
		t.Fatal(err)
	}
	if shows, err = ts.db.CreateLibrary(ctx, "Shows", "shows", "default", []string{"/big/shows"}, 1); err != nil {
		t.Fatal(err)
	}
	var f, s []db.SeenFile
	for i := 1; i <= 3000; i++ {
		name := fmt.Sprintf("Film %04d (%d)", i, 1950+i%70)
		f = append(f, db.SeenFile{RelPath: name + "/" + name + ".mkv", Kind: "video", Size: int64(i), MtimeNS: 1})
	}
	for i := 1; i <= 200; i++ {
		for season := 1; season <= 4; season++ {
			for e := 1; e <= 20; e++ {
				s = append(s, db.SeenFile{RelPath: fmt.Sprintf("Series %03d (2010)/Season %02d/Series %03d - S%02dE%02d.mkv", i, season, i, season, e),
					Kind: "video", Size: int64(i*1000 + season*100 + e), MtimeNS: 1})
			}
		}
	}
	b := &browse{testServer: ts}
	b.applyAndBuild(t, films, db.ScanChanges{New: f}, 0)
	b.applyAndBuild(t, shows, db.ScanChanges{New: s}, 0)
	return films, shows
}

func (b *browse) applyAndBuild(t *testing.T, lib model.Library, c db.ScanChanges, purgeBefore int64) {
	t.Helper()
	ctx := context.Background()
	now := b.now().UnixMilli()
	if err := b.db.ApplyScan(ctx, lib.Roots[0].ID, c, now, purgeBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := library.Build(ctx, b.db, lib.ID, now); err != nil {
		t.Fatal(err)
	}
}

// TestFirstSyncOfTwentyThousandItems copies a library of 20,000 items
// from /changes, then lets a day of scans and playback pass, and checks
// that the copy brought up to date equals a copy made from zero
// (docs/01-roadmap.md, section 4, exit criterion 4).
func TestFirstSyncOfTwentyThousandItems(t *testing.T) {
	if testing.Short() {
		t.Skip("builds 20,000 items")
	}
	ts := newTestServer(t)
	films, shows := bigLibraries(t, ts)
	b := &browse{testServer: ts}
	ts.addUser(t, "ada", "correct horse", true)
	var a loginAnswer
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("ada", "correct horse", "d"), &a)
	b.token = a.Token

	first := newCopy()
	pages := b.sync(t, b.token, 2000, first)
	if len(first.items) != 20_000 || len(first.libraries) != 2 {
		t.Fatalf("%d items and %d libraries in %d pages", len(first.items), len(first.libraries), pages)
	}
	if pages < 10 {
		t.Fatalf("20,000 items in %d pages of at most 2,000", pages)
	}

	// A day: new episodes in seasons that exist, and a new season, which
	// counts again in seasons and series stamped long after their first
	// episodes; films removed; playback, marks, a list and track choices.
	var more []db.SeenFile
	for i := 1; i <= 50; i++ {
		more = append(more, db.SeenFile{RelPath: fmt.Sprintf("Series %03d (2010)/Season 01/Series %03d - S01E21.mkv", i, i),
			Kind: "video", Size: int64(900_000 + i), MtimeNS: 1})
	}
	more = append(more, db.SeenFile{RelPath: "Series 001 (2010)/Season 05/Series 001 - S05E01.mkv", Kind: "video", Size: 950_000, MtimeNS: 1})
	b.applyAndBuild(t, shows, db.ScanChanges{New: more}, 0)
	known, err := ts.db.KnownFiles(context.Background(), films.Roots[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var gone []int64
	for rel, f := range known {
		if len(gone) < 30 && strings.HasSuffix(rel, "(1951).mkv") {
			gone = append(gone, f.ID)
		}
	}
	b.applyAndBuild(t, films, db.ScanChanges{Missing: gone}, 0)
	b.advance(time.Second)
	b.applyAndBuild(t, films, db.ScanChanges{}, b.now().UnixMilli())

	var page itemsPage
	b.call(t, "GET", "/api/v1/items?library="+idStr(shows.ID)+"&limit=3", b.token, "", nil, &page)
	series, other := page.Items[0], page.Items[1]
	b.call(t, "GET", "/api/v1/items?parent="+idStr(series.ID), b.token, "", nil, &page)
	if code := b.call(t, "PUT", "/api/v1/items/"+idStr(page.Items[0].ID)+"/played", b.token, "", nil, nil).Code; code != 204 {
		t.Fatalf("mark a season: %d", code)
	}
	b.call(t, "GET", "/api/v1/items?library="+idStr(films.ID)+"&limit=5", b.token, "", nil, &page)
	for i, c := range page.Items {
		b.call(t, "PUT", "/api/v1/items/"+idStr(c.ID)+"/list", b.token, "", nil, nil)
		if i == 0 {
			b.call(t, "DELETE", "/api/v1/items/"+idStr(c.ID)+"/list", b.token, "", nil, nil)
		}
		b.call(t, "PUT", "/api/v1/items/played", b.token, "", map[string]any{"ids": []int64{c.ID}}, nil)
	}
	b.call(t, "PUT", "/api/v1/series/"+idStr(series.ID)+"/prefs", b.token, "", map[string]any{"audio_lang": "eng", "sub_mode": "on"}, nil)
	b.call(t, "DELETE", "/api/v1/series/"+idStr(series.ID)+"/prefs", b.token, "", nil, nil)
	b.call(t, "PUT", "/api/v1/series/"+idStr(other.ID)+"/prefs", b.token, "", map[string]any{"sub_mode": "off"}, nil)

	b.sync(t, b.token, 500, first)
	fresh := newCopy()
	b.sync(t, b.token, 500, fresh)
	if d := first.diff(fresh); d != "" {
		t.Fatalf("the copy kept up to date differs from a fresh one: %s", d)
	}
	if len(fresh.items) != 20_000+51+1-len(gone) || len(fresh.state) == 0 || len(fresh.list) == 0 {
		t.Fatalf("%d items, %d states, %d listings", len(fresh.items), len(fresh.state), len(fresh.list))
	}
}

func TestChangesRefusals(t *testing.T) {
	b := newBrowse(t)
	for _, target := range []string{"/api/v1/changes?since=-1", "/api/v1/changes?limit=0", "/api/v1/changes?limit=2001",
		"/api/v1/changes?since=abc", "/api/v1/changes?library=1"} {
		var e apiError
		if rec := b.call(t, "GET", target, b.token, "", nil, &e); rec.Code != 400 {
			t.Errorf("%s: %d %+v", target, rec.Code, e)
		}
	}
	if rec := b.call(t, "GET", "/api/v1/changes", "", "", nil, nil); rec.Code != 401 {
		t.Fatalf("without a token: %d", rec.Code)
	}
	// Ahead of this database's sequence: the copy came from another.
	var p changesPage
	b.call(t, "GET", "/api/v1/changes?since=999999", b.token, "", nil, &p)
	if !p.Reset || len(p.Items) != 0 || len(p.LibraryIDs) != 2 {
		t.Fatalf("a page from the future %+v", p)
	}
}

func TestETags(t *testing.T) {
	b := newBrowse(t)
	film := b.film(t, "Paper Comets")
	for _, target := range []string{"/api/v1/items?library=1", "/api/v1/items/" + idStr(film.ID)} {
		rec := b.call(t, "GET", target, b.token, "", nil, nil)
		tag := rec.Header().Get("ETag")
		if rec.Code != 200 || tag == "" || rec.Header().Get("Cache-Control") != "private, no-cache" {
			t.Fatalf("%s: %d %q %q", target, rec.Code, tag, rec.Header().Get("Cache-Control"))
		}
		get := func(inm string) *httptest.ResponseRecorder {
			req := httptest.NewRequest("GET", target, nil)
			req.Header.Set("Authorization", "Bearer "+b.token)
			req.Header.Set("If-None-Match", inm)
			out := httptest.NewRecorder()
			b.Handler().ServeHTTP(out, req)
			return out
		}
		if r := get(tag); r.Code != 304 || r.Body.Len() != 0 || r.Header().Get("ETag") != tag {
			t.Fatalf("%s with its tag: %d %q", target, r.Code, r.Body.String())
		}
		if r := get(`W/"other", ` + tag); r.Code != 304 {
			t.Fatalf("%s with its tag in a list: %d", target, r.Code)
		}
		// A change anywhere moves the tag.
		b.call(t, "PUT", "/api/v1/items/"+idStr(film.ID)+"/list", b.token, "", nil, nil)
		b.call(t, "DELETE", "/api/v1/items/"+idStr(film.ID)+"/list", b.token, "", nil, nil)
		if r := get(tag); r.Code != 200 || r.Header().Get("ETag") == tag {
			t.Fatalf("%s after a change: %d", target, r.Code)
		}
	}
	// New track defaults move the tag of a detail, whose prefs they change.
	target := "/api/v1/items/" + idStr(film.ID)
	tag := b.call(t, "GET", target, b.token, "", nil, nil).Header().Get("ETag")
	b.call(t, "PUT", "/api/v1/auth/me/prefs", b.token, "", map[string]any{"sub_mode": "on"}, nil)
	if after := b.call(t, "GET", target, b.token, "", nil, nil).Header().Get("ETag"); after == tag {
		t.Fatal("new defaults kept the tag")
	}
	// Other calls are not to be stored.
	if cc := b.call(t, "GET", "/api/v1/libraries", b.token, "", nil, nil).Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("libraries: %q", cc)
	}
}

func TestSyncedCountsRows(t *testing.T) {
	ts := newTestServer(t)
	ts.synced(3000, true) // a sync's end, below the count: kept
	ts.synced(1500, false)
	if ts.changesRows != 4500 {
		t.Fatalf("counted %d", ts.changesRows)
	}
	ts.synced(600, true) // past the count, at a sync's end: handed back
	if ts.changesRows != 0 {
		t.Fatalf("after handing back: %d", ts.changesRows)
	}
}

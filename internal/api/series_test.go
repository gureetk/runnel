// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/scan"
)

func idStr(n int64) string { return strconv.FormatInt(n, 10) }

// series finds the one series of the shows library.
func (b *browse) series(t *testing.T) cardBody {
	t.Helper()
	var page itemsPage
	b.call(t, "GET", "/api/v1/items?library=2", b.token, "", nil, &page)
	if len(page.Items) != 1 {
		t.Fatalf("series %+v", page)
	}
	return page.Items[0]
}

func TestSeriesSeasonsAndEpisodes(t *testing.T) {
	b := newBrowse(t)
	series := b.series(t)
	if series.Counts == nil || series.Counts.Episodes != 3 || series.Counts.Seasons == nil || *series.Counts.Seasons != 2 || !series.Available {
		t.Fatalf("a series card counts its episodes and seasons outside the specials: %+v %+v", series, series.Counts)
	}

	// The detail: numbering, and the seasons in order, specials last.
	var d detailBody
	b.call(t, "GET", "/api/v1/items/"+idStr(series.ID), b.token, "", nil, &d)
	if d.Numbering != "seasons" || d.Seasons == nil || d.Files != nil || d.Match.State != "unmatched" {
		t.Fatalf("series detail %+v", d)
	}
	var got []string
	for _, s := range *d.Seasons {
		got = append(got, s.Title+":"+strconv.Itoa(s.Counts.Episodes))
	}
	if string(mustJSON(got)) != `["Season 1:2","Season 2:1","Specials:1"]` {
		t.Fatalf("seasons %v", got)
	}

	// The same seasons as cards, one page at a time.
	var p1, p2 itemsPage
	b.call(t, "GET", "/api/v1/items?parent="+idStr(series.ID)+"&limit=2", b.token, "", nil, &p1)
	if len(p1.Items) != 2 || p1.Next == nil || *p1.Total != 3 || p1.Items[0].Type != "season" || *p1.Items[0].Season != 1 ||
		p1.Items[0].SeriesTitle != "Harbor Lantern" || p1.Items[0].Counts == nil || p1.Items[0].Counts.Seasons != nil {
		t.Fatalf("seasons, first page %+v", p1)
	}
	b.call(t, "GET", "/api/v1/items?parent="+idStr(series.ID)+"&limit=2&cursor="+*p1.Next, b.token, "", nil, &p2)
	if len(p2.Items) != 1 || p2.Next != nil || p2.Items[0].Title != "Specials" || *p2.Items[0].Season != 0 {
		t.Fatalf("seasons, second page %+v", p2)
	}
	season1 := p1.Items[0]

	// A season's episodes.
	var eps itemsPage
	b.call(t, "GET", "/api/v1/items?parent="+idStr(season1.ID)+"&type=episode", b.token, "", nil, &eps)
	if len(eps.Items) != 2 || *eps.Total != 2 || eps.Items[0].Episode.N != 1 || eps.Items[1].Episode.N != 2 || !eps.Items[0].Available {
		t.Fatalf("episodes of season 1 %+v", eps)
	}

	// Neighbours run through the seasons, specials last.
	var all itemsPage
	b.call(t, "GET", "/api/v1/items?series="+idStr(series.ID)+"&type=episode", b.token, "", nil, &all)
	for i, e := range all.Items {
		var ed detailBody
		b.call(t, "GET", "/api/v1/items/"+idStr(e.ID), b.token, "", nil, &ed)
		if ed.Files == nil || len(*ed.Files) != 1 || ed.Seasons != nil {
			t.Fatalf("episode detail %+v", ed)
		}
		if i == 0 && ed.Prev != nil || i > 0 && (ed.Prev == nil || ed.Prev.ID != all.Items[i-1].ID || ed.Prev.Title != "") {
			t.Errorf("episode %d: prev %+v", i, ed.Prev)
		}
		last := i == len(all.Items)-1
		if last && ed.Next != nil {
			t.Errorf("the last episode has a next: %+v", ed.Next)
		}
		if !last {
			n := all.Items[i+1]
			if ed.Next == nil || ed.Next.ID != n.ID || ed.Next.Title != n.Title || *ed.Next.Season != *n.Season || *ed.Next.Episode != *n.Episode {
				t.Errorf("episode %d: next %+v, want %+v", i, ed.Next, n)
			}
		}
	}

	var e apiError
	for target, want := range map[string]int{
		"/api/v1/items?parent=" + idStr(series.ID) + "&type=episode":  400,
		"/api/v1/items?parent=" + idStr(season1.ID) + "&type=season":  400,
		"/api/v1/items?parent=" + idStr(b.film(t, "Paper Comets").ID): 404,
		"/api/v1/items?parent=abc":                                    404,
		"/api/v1/items?parent=" + idStr(series.ID) + "&cursor=junk":   400,
	} {
		e = apiError{}
		if rec := b.call(t, "GET", target, b.token, "", nil, &e); rec.Code != want {
			t.Errorf("%s: %d %+v, want %d", target, rec.Code, e, want)
		}
	}
}

func TestAvailableFollowsTheFiles(t *testing.T) {
	b := newBrowse(t)
	os.Remove(filepath.Join(b.shows, "Harbor Lantern (2021)/Season 02/Harbor Lantern - S02E01.mkv"))
	ctx := context.Background()
	s := scan.New(b.db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Built = func(ctx context.Context, id int64, scope db.Scope) error {
		_, err := library.BuildIn(ctx, b.db, id, scope, 2)
		return err
	}
	lib, _ := b.db.Library(ctx, 2)
	if _, err := s.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	series := b.series(t)
	var seasons itemsPage
	b.call(t, "GET", "/api/v1/items?parent="+idStr(series.ID), b.token, "", nil, &seasons)
	avail := map[string]bool{}
	for _, c := range seasons.Items {
		avail[c.Title] = c.Available
	}
	if !series.Available || !avail["Season 1"] || avail["Season 2"] || !avail["Specials"] {
		t.Fatalf("series %v, seasons %v: season 2's only file is missing", series.Available, avail)
	}
	var eps itemsPage
	b.call(t, "GET", "/api/v1/items?series="+idStr(series.ID)+"&type=episode", b.token, "", nil, &eps)
	for _, e := range eps.Items {
		if e.Available != (*e.Season != 2) {
			t.Errorf("episode %+v", e)
		}
	}
}

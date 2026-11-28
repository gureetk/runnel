// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"strconv"
	"sync"
	"testing"
)

type fakeFetches struct {
	mu        sync.Mutex
	refreshed []int64 // items
	queued    []int64 // libraries
	keys      int
}

func (f *fakeFetches) Queue(ctx context.Context, libraryID int64, again bool) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if again {
		f.queued = append(f.queued, libraryID)
	}
	return 0, nil
}

func (f *fakeFetches) Refresh(ctx context.Context, itemID, libraryID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed = append(f.refreshed, itemID)
	return nil
}

func (f *fakeFetches) KeySet(ctx context.Context) {
	f.mu.Lock()
	f.keys++
	f.mu.Unlock()
}

// TestRefresh: POST /admin/items/{id}/refresh and
// POST /admin/libraries/{id}/refresh (docs/04-api.md, sections 8.2 and
// 8.4) queue the fetches, a season or an episode its series', and answer
// 409 no_key without a metadata key.
func TestRefresh(t *testing.T) {
	b := newBrowse(t)
	f := &fakeFetches{}
	b.fetches = f
	film := b.film(t, "Paper Comets")
	path := func(id int64) string { return "/api/v1/admin/items/" + strconv.FormatInt(id, 10) + "/refresh" }
	var e apiError
	if rec := b.call(t, "POST", path(film.ID), b.token, "", nil, &e); rec.Code != 409 || e.Error.Code != "no_key" {
		t.Fatalf("without a key: %d %+v", rec.Code, e)
	}
	if rec := b.call(t, "POST", "/api/v1/admin/libraries/1/refresh", b.token, "", nil, &e); rec.Code != 409 || e.Error.Code != "no_key" {
		t.Fatalf("a library without a key: %d %+v", rec.Code, e)
	}
	key := "0000invented0000key0000"
	if err := b.db.SetMetadataKey(context.Background(), &key, 1); err != nil {
		t.Fatal(err)
	}
	if rec := b.call(t, "POST", path(film.ID), b.token, "", nil, nil); rec.Code != 202 {
		t.Fatalf("a film's refresh: %d %s", rec.Code, rec.Body)
	}
	var page itemsPage
	b.call(t, "GET", "/api/v1/items?library=2", b.token, "", nil, &page)
	series := page.Items[0].ID
	b.call(t, "GET", "/api/v1/items?parent="+strconv.FormatInt(series, 10), b.token, "", nil, &page)
	season := page.Items[0].ID
	b.call(t, "GET", "/api/v1/items?parent="+strconv.FormatInt(season, 10), b.token, "", nil, &page)
	episode := page.Items[0].ID
	for _, id := range []int64{season, episode} {
		if rec := b.call(t, "POST", path(id), b.token, "", nil, nil); rec.Code != 202 {
			t.Fatalf("refresh of %d: %d %s", id, rec.Code, rec.Body)
		}
	}
	if len(f.refreshed) != 3 || f.refreshed[0] != film.ID || f.refreshed[1] != series || f.refreshed[2] != series {
		t.Errorf("refreshed %v, want the film, then its series twice", f.refreshed)
	}
	if rec := b.call(t, "POST", path(99999), b.token, "", nil, &e); rec.Code != 404 {
		t.Errorf("an item that does not exist: %d", rec.Code)
	}
	if rec := b.call(t, "POST", "/api/v1/admin/libraries/2/refresh", b.token, "", nil, nil); rec.Code != 202 || len(f.queued) != 1 || f.queued[0] != 2 {
		t.Errorf("a library's refresh: %d, queued %v", rec.Code, f.queued)
	}
	if rec := b.call(t, "POST", "/api/v1/admin/libraries/9/refresh", b.token, "", nil, &e); rec.Code != 404 {
		t.Errorf("a library that does not exist: %d", rec.Code)
	}
	bo := b.signIn(t, "bo", false, "laptop")
	if rec := b.call(t, "POST", path(film.ID), bo, "", nil, nil); rec.Code != 403 {
		t.Errorf("not an admin: %d", rec.Code)
	}
}

type hurriedImages struct {
	mu      sync.Mutex
	seasons []int64
}

func (h *hurriedImages) Path(key, kind string, width int) string { return "" }

func (h *hurriedImages) Hurry(ctx context.Context, season int64) {
	h.mu.Lock()
	h.seasons = append(h.seasons, season)
	h.mu.Unlock()
}

// TestOpeningASeasonHurriesItsThumbs: the first page of a season's
// episodes moves their thumbs forward (docs/06-library-and-metadata.md,
// section 8); a series' seasons, and a later page, do not.
func TestOpeningASeasonHurriesItsThumbs(t *testing.T) {
	b := newBrowse(t)
	h := &hurriedImages{}
	b.images = h
	var page itemsPage
	b.call(t, "GET", "/api/v1/items?library=2", b.token, "", nil, &page)
	series := page.Items[0].ID
	b.call(t, "GET", "/api/v1/items?parent="+strconv.FormatInt(series, 10), b.token, "", nil, &page)
	season := page.Items[0].ID
	b.call(t, "GET", "/api/v1/items?parent="+strconv.FormatInt(season, 10)+"&limit=1", b.token, "", nil, &page)
	if page.Next != nil {
		b.call(t, "GET", "/api/v1/items?parent="+strconv.FormatInt(season, 10)+"&limit=1&cursor="+*page.Next, b.token, "", nil, &page)
	}
	if len(h.seasons) != 1 || h.seasons[0] != season {
		t.Errorf("hurried %v, want season %d once", h.seasons, season)
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/parse"
)

// inCollection gives a film of the films library what a provider says of
// it, with the collection it names, or none with "".
func (b *browse) inCollection(t *testing.T, title, date, collection string) cardBody {
	t.Helper()
	film := b.film(t, title)
	if err := b.db.ApplyMetadata(context.Background(), film.ID, db.Metadata{Title: title, SortTitle: parse.SortTitle(title),
		Date: date, IDs: map[string]string{"tmdb_collection": collection}}, 1); err != nil {
		t.Fatal(err)
	}
	return film
}

// TestCollectionsInTheAPI: a collection's card and detail, its films'
// collection_id and their details' collection, GET /items?collection=,
// search, and its going through /changes (docs/04-api.md, sections 2.3,
// 4.3 and 5).
func TestCollectionsInTheAPI(t *testing.T) {
	b := newBrowse(t)
	ctx := context.Background()
	comets := b.inCollection(t, "Paper Comets", "2019-03-08", "900")
	echo := b.inCollection(t, "An Echo Garden", "2011-05-20", "900")
	b.inCollection(t, "The Salt Road", "2005-01-01", "")
	made, err := b.db.SyncCollections(ctx, 1, nil, map[string]string{"900": "Garden and Comet Collection"}, parse.SortTitle, 1)
	if err != nil || len(made) != 1 {
		t.Fatalf("made %v, %v", made, err)
	}
	coll := made[0]

	if got := b.film(t, "Paper Comets"); got.CollectionID == nil || *got.CollectionID != coll {
		t.Errorf("a film's card: collection_id %v, want %d", got.CollectionID, coll)
	}
	if got := b.film(t, "The Salt Road"); got.CollectionID != nil {
		t.Errorf("a film of no collection: collection_id %v", *got.CollectionID)
	}
	var page itemsPage
	b.call(t, "GET", "/api/v1/items?library=1", b.token, "", nil, &page)
	for _, c := range page.Items {
		if c.Type == "collection" {
			t.Errorf("a library's list holds a collection: %+v", c)
		}
	}
	var film detailBody
	b.call(t, "GET", "/api/v1/items/"+idStr(comets.ID), b.token, "", nil, &film)
	if film.Collection == nil || film.Collection.ID != coll || film.Collection.Title != "Garden and Comet Collection" {
		t.Errorf("a film's detail: collection %+v", film.Collection)
	}

	var detail collectionBody
	if rec := b.call(t, "GET", "/api/v1/items/"+idStr(coll), b.token, "", nil, &detail); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if detail.Type != "collection" || detail.Counts == nil || detail.Counts.Films == nil || *detail.Counts.Films != 2 ||
		detail.Counts.Episodes != nil || detail.State != nil || !detail.Available {
		t.Errorf("a collection's detail: %+v", detail)
	}
	if len(detail.Items) != 2 || detail.Items[0].ID != echo.ID || detail.Items[1].ID != comets.ID {
		t.Errorf("its films, by release date: %+v", detail.Items)
	}
	if detail.Resume == nil || detail.Resume.ID != echo.ID {
		t.Errorf("resume %+v, want the first film", detail.Resume)
	}
	b.call(t, "PUT", "/api/v1/items/"+idStr(echo.ID)+"/played", b.token, "", nil, nil)
	b.call(t, "GET", "/api/v1/items/"+idStr(coll), b.token, "", nil, &detail)
	if detail.Resume == nil || detail.Resume.ID != comets.ID {
		t.Errorf("resume %+v, want the first film not played", detail.Resume)
	}
	b.call(t, "PUT", "/api/v1/items/"+idStr(coll)+"/played", b.token, "", nil, nil)
	b.call(t, "GET", "/api/v1/items/"+idStr(coll), b.token, "", nil, &detail)
	if detail.Resume != nil || !detail.Items[1].State.Played {
		t.Errorf("after the collection is marked played: %+v", detail)
	}

	for _, query := range []string{"collection=" + idStr(coll), "library=1&collection=" + idStr(coll), "collection=" + idStr(coll) + "&type=movie"} {
		page = itemsPage{}
		if rec := b.call(t, "GET", "/api/v1/items?"+query, b.token, "", nil, &page); rec.Code != 200 ||
			len(page.Items) != 2 || page.Items[0].ID != echo.ID || page.Items[1].ID != comets.ID {
			t.Errorf("%s: %d %+v", query, rec.Code, page.Items)
		}
	}
	b.call(t, "GET", "/api/v1/items?collection="+idStr(coll)+"&sort=date&order=desc", b.token, "", nil, &page)
	if len(page.Items) != 2 || page.Items[0].ID != comets.ID {
		t.Errorf("newest first: %+v", page.Items)
	}
	for query, code := range map[string]int{
		"collection=" + idStr(coll) + "&type=series": 400,
		"collection=" + idStr(comets.ID):             404,
		"collection=x":                               404,
		"collection=99999":                           404,
	} {
		var e apiError
		if rec := b.call(t, "GET", "/api/v1/items?"+query, b.token, "", nil, &e); rec.Code != code {
			t.Errorf("%s: %d %+v, want %d", query, rec.Code, e, code)
		}
	}
	var found struct{ Items []cardBody }
	b.call(t, "GET", "/api/v1/search?q=comet+collection&types=collection", b.token, "", nil, &found)
	if len(found.Items) != 1 || found.Items[0].ID != coll || *found.Items[0].Counts.Films != 2 {
		t.Errorf("search: %+v", found.Items)
	}

	var changes struct {
		Items   []cardBody `json:"items"`
		Deleted []int64    `json:"deleted"`
		Latest  int64      `json:"latest"`
	}
	b.call(t, "GET", "/api/v1/changes?since=0", b.token, "", nil, &changes)
	seen := false
	for _, c := range changes.Items {
		seen = seen || c.ID == coll
	}
	if !seen {
		t.Errorf("/changes does not carry the collection")
	}
	since := changes.Latest
	b.inCollection(t, "An Echo Garden", "2011-05-20", "")
	if _, err := b.db.SyncCollections(ctx, 1, nil, nil, parse.SortTitle, 2); err != nil {
		t.Fatal(err)
	}
	changes.Items, changes.Deleted = nil, nil
	b.call(t, "GET", "/api/v1/changes?since="+idStr(since), b.token, "", nil, &changes)
	if len(changes.Deleted) != 1 || changes.Deleted[0] != coll {
		t.Errorf("deleted %v, want the collection", changes.Deleted)
	}
	for _, c := range changes.Items {
		if c.ID == comets.ID && c.CollectionID != nil {
			t.Errorf("the film left alone still has collection_id %d", *c.CollectionID)
		}
	}
	if rec := b.call(t, "GET", "/api/v1/items/"+idStr(coll), b.token, "", nil, nil); rec.Code != 404 {
		t.Errorf("a collection that went: %d", rec.Code)
	}
}

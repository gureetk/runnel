// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/meta/tmdb"
)

// filmIn is a film's details that name a collection, or none with 0.
func filmIn(id int, title string, collection int) string {
	var m map[string]any
	json.Unmarshal([]byte(movieJSON(id, title, "An overview.", "2019-03-08")), &m)
	if collection != 0 {
		m["belongs_to_collection"] = map[string]any{"id": collection, "name": "Paper Comets Collection",
			"poster_path": "/cp.jpg", "backdrop_path": "/cb.jpg"}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// TestCollections is exit criterion 7: a collection appears when the
// library holds two of its films, with its own details, and goes when it
// holds one, as when a film's details no longer name it or a film's file
// goes (docs/06-library-and-metadata.md, section 6.6).
func TestCollections(t *testing.T) {
	ctx := context.Background()
	e := newFetchEnv(t, "movies")
	e.put("Paper Comets (2019) [tmdbid-101]/Paper Comets (2019).mkv")
	e.put("Paper Comets 2 (2023) [tmdbid-102]/Paper Comets 2 (2023).mkv")
	e.put("Salt Road (2010) [tmdbid-103]/Salt Road (2010).mkv")
	e.page("/movie/101", tmdb.MovieQuery(), filmIn(101, "Paper Comets", 900))
	e.page("/movie/102", tmdb.MovieQuery(), filmIn(102, "Paper Comets: The Second Winter", 900))
	e.page("/movie/103", tmdb.MovieQuery(), filmIn(103, "Salt Road", 0))
	e.page("/collection/900", tmdb.CollectionQuery(), `{"id":900,"name":"The Paper Comets Films","overview":"Both films.",
		"poster_path":"/cp.jpg","backdrop_path":"/cb.jpg","parts":[]}`)
	e.scan()

	coll := e.item("collection", "The Paper Comets Films")
	if coll.Overview != "Both films." || coll.Episodes != 2 || !coll.Available {
		t.Errorf("the collection: %+v, want its details, two films, available", coll)
	}
	one, two, salt := e.item("movie", "Paper Comets"), e.item("movie", "Paper Comets: The Second Winter"), e.item("movie", "Salt Road")
	if one.CollectionID != coll.ID || two.CollectionID != coll.ID || salt.CollectionID != 0 {
		t.Errorf("collection ids: %d %d %d, want %d %d 0", one.CollectionID, two.CollectionID, salt.CollectionID, coll.ID, coll.ID)
	}
	if found, _ := e.db.Search(ctx, "Paper Comets Films", []string{"collection"}, 5); len(found) != 1 {
		t.Errorf("the collection is not searched by its title: %+v", found)
	}
	asked := e.requests()
	if asked != 4 {
		t.Errorf("%d requests, want 4: three films and the collection", asked)
	}
	if _, err := e.f.Queue(ctx, e.lib.ID, true); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := e.requests(); n != asked {
		t.Errorf("a second full refresh made %d requests", n-asked)
	}

	// The second film's details no longer name it: it goes, with a
	// tombstone, and the first film points at nothing.
	e.page("/movie/102", tmdb.MovieQuery(), filmIn(102, "Paper Comets: The Second Winter", 0))
	if err := e.f.Refresh(ctx, two.ID, e.lib.ID); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if all, _ := e.db.LibraryItems(ctx, e.lib.ID); len(all) != 3 {
		t.Errorf("items after the collection went: %+v", all)
	}
	if c := e.item("movie", "Paper Comets"); c.CollectionID != 0 || c.Seq <= one.Seq {
		t.Errorf("the film left alone: %+v, want no collection, stamped", c)
	}
	ch, err := e.db.Changes(ctx, 1, coll.Seq, 100)
	if err != nil {
		t.Fatal(err)
	}
	var deleted []int64
	ch.Deleted(ctx, func(id int64) error { deleted = append(deleted, id); return nil })
	ch.Close()
	if len(deleted) != 1 || deleted[0] != coll.ID {
		t.Errorf("deleted since: %v, want the collection", deleted)
	}

	// Named again, it comes back; the file of one film going takes it away.
	e.page("/movie/102", tmdb.MovieQuery(), filmIn(102, "Paper Comets: The Second Winter", 900))
	if err := e.f.Refresh(ctx, two.ID, e.lib.ID); err != nil {
		t.Fatal(err)
	}
	e.drain()
	again := e.item("collection", "The Paper Comets Films")
	if again.ID == coll.ID {
		t.Errorf("a collection made again kept the old id %d", coll.ID)
	}
	// A film whose file is only missing stays, unavailable, and so does
	// the collection; once its row goes, the collection goes too.
	os.RemoveAll(filepath.Join(e.root, "Paper Comets (2019) [tmdbid-101]"))
	e.scan()
	if c := e.item("collection", "The Paper Comets Films"); c.Episodes != 2 || !c.Available {
		t.Errorf("with one film missing: %+v, want two films, available", c)
	}
	if err := e.db.ApplyScan(ctx, e.lib.Roots[0].ID, db.ScanChanges{}, 1, 1<<62); err != nil {
		t.Fatal(err)
	}
	if _, err := library.Build(ctx, e.db, e.lib.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.f.Built(ctx, e.lib.ID); err != nil {
		t.Fatal(err)
	}
	for _, it := range must(e.db.LibraryItems(ctx, e.lib.ID)) {
		if it.Type == "collection" {
			t.Errorf("a collection of one film is still there: %+v", it)
		}
	}
}

// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"strings"
	"testing"
)

// TestCollectionsKeepToALibrary: a collection is counted in one library,
// so two libraries holding one film each of it show none, and a library
// holding two shows its own; a sync of other collections leaves it, and one
// that changes nothing stamps nothing (docs/03-data-model.md, section 4).
func TestCollectionsKeepToALibrary(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	films := func(name string, folders ...string) int64 {
		t.Helper()
		lib, err := d.CreateLibrary(ctx, name, "movies", "default", []string{"/media/" + name}, 1)
		if err != nil {
			t.Fatal(err)
		}
		var seen []SeenFile
		for i, f := range folders {
			seen = append(seen, SeenFile{RelPath: f + "/" + f + ".mkv", Kind: "video", Size: int64(1000 + i), MtimeNS: 1})
		}
		if err := d.ApplyScan(ctx, lib.Roots[0].ID, ScanChanges{New: seen}, 2, 0); err != nil {
			t.Fatal(err)
		}
		files, _ := d.LibraryFiles(ctx, lib.ID)
		var want []DesiredFilm
		for _, f := range files {
			folder, _, _ := strings.Cut(f.RelPath, "/")
			want = append(want, DesiredFilm{RootID: lib.Roots[0].ID, Key: folder, Title: folder, Files: []DesiredFile{{ID: f.ID}}})
		}
		if _, err := d.BuildItems(ctx, lib.ID, want, nil, strings.ToLower, 3); err != nil {
			t.Fatal(err)
		}
		items, _ := d.LibraryItems(ctx, lib.ID)
		for _, it := range items {
			if err := d.ApplyMetadata(ctx, it.ID, Metadata{Title: it.Title, SortTitle: it.SortTitle,
				IDs: map[string]string{"tmdb_collection": "900"}}, 4); err != nil {
				t.Fatal(err)
			}
		}
		return lib.ID
	}
	a := films("A", "Paper Comets (2019)")
	b := films("B", "Paper Comets 2 (2023)")
	c := films("C", "Paper Comets (2019)", "Paper Comets 2 (2023)")
	// Counting another collection, as after a film's fetch, leaves this
	// one alone.
	if made, err := d.SyncCollections(ctx, c, []string{"901"}, nil, strings.ToLower, 5); err != nil || len(made) != 0 {
		t.Fatalf("made %v, %v", made, err)
	}
	for _, lib := range []int64{a, b, c} {
		if _, err := d.SyncCollections(ctx, lib, nil, nil, strings.ToLower, 5); err != nil {
			t.Fatal(err)
		}
	}
	count := func(lib int64) (n int) {
		items, _ := d.LibraryItems(ctx, lib)
		for _, it := range items {
			if it.Type == "collection" {
				n++
			}
		}
		return n
	}
	if count(a) != 0 || count(b) != 0 || count(c) != 1 {
		t.Errorf("collections: %d %d %d, want 0 0 1", count(a), count(b), count(c))
	}
	seq, _ := d.Seq(ctx)
	made, err := d.SyncCollections(ctx, c, nil, nil, strings.ToLower, 6)
	if err != nil || len(made) != 0 {
		t.Fatalf("made %v, %v", made, err)
	}
	if again, _ := d.Seq(ctx); again != seq {
		t.Errorf("a sync that changes nothing moved the sequence from %d to %d", seq, again)
	}

	// Its details dropped after 180 days without a fetch (D53), it has no
	// name to fall back to.
	items, _ := d.LibraryItems(ctx, c)
	var coll ItemSummary
	for _, it := range items {
		if it.Type == "collection" {
			coll = it
		}
	}
	if err := d.ApplyMetadata(ctx, coll.ID, Metadata{Title: "The Paper Comets Films", SortTitle: "paper comets films",
		Overview: "Both films."}, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DropStale(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Item(ctx, coll.ID); got.Title != "Collection" || got.Overview != "" {
		t.Errorf("a stale collection: %q %q", got.Title, got.Overview)
	}
}

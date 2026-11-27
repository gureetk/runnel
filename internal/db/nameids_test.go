// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"maps"
	"strings"
	"testing"
)

// TestIDsInNamesLeaveAManualMatch: the ids in a name never change an item
// matched by hand or left unmatched on purpose
// (docs/06-library-and-metadata.md, section 6.1).
func TestIDsInNamesLeaveAManualMatch(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	lib, err := d.CreateLibrary(ctx, "Films", "movies", "default", []string{"/media/films"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	root := lib.Roots[0].ID
	seen := []SeenFile{
		{RelPath: "Paper Comets (2019)/Paper Comets (2019).mkv", Kind: "video", Size: 1000, MtimeNS: 1},
		{RelPath: "Salt Road (2010)/Salt Road (2010).mkv", Kind: "video", Size: 1001, MtimeNS: 1},
	}
	if err := d.ApplyScan(ctx, root, ScanChanges{New: seen}, 2, 0); err != nil {
		t.Fatal(err)
	}
	files, _ := d.LibraryFiles(ctx, lib.ID)
	build := func(ids map[string]string) {
		t.Helper()
		var want []DesiredFilm
		for _, f := range files {
			folder, _, _ := strings.Cut(f.RelPath, "/")
			want = append(want, DesiredFilm{RootID: root, Key: folder, Title: folder, IDs: ids, Files: []DesiredFile{{ID: f.ID}}})
		}
		if _, err := d.BuildItems(ctx, lib.ID, want, nil, strings.ToLower, 3); err != nil {
			t.Fatal(err)
		}
	}
	build(map[string]string{"tmdb": "101"})
	items, _ := d.LibraryItems(ctx, lib.ID)
	if len(items) != 2 {
		t.Fatalf("items: %+v", items)
	}
	for i, state := range []string{"manual", "ignored"} {
		if err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
			_, err := tx.tx.ExecContext(ctx, `UPDATE items SET match_state = ? WHERE id = ?`, state, items[i].ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	build(map[string]string{"tmdb": "202", "imdb": "tt0000202"})
	for i, state := range []string{"manual", "ignored"} {
		m, err := d.MatchOf(ctx, items[i].ID)
		if err != nil {
			t.Fatal(err)
		}
		if m.State != state || !maps.Equal(m.ExternalIDs, map[string]string{"tmdb": "101"}) {
			t.Errorf("%s: %s %v, want it left as it was", state, m.State, m.ExternalIDs)
		}
	}
}

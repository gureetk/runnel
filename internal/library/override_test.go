// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gureetk/runnel/internal/db"
)

// TestOverridesByFolder: a match made by hand is kept by its folder, so
// that the item a build makes again for that folder, after the old one
// went, is matched the same way, before any id in its name; and it goes
// with its item when the folder is renamed, so that a new folder of the
// old name is not matched by it (docs/06-library-and-metadata.md,
// section 6.1; 03 §4).
func TestOverridesByFolder(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "movies")
	e.put("Salt Road (2005)/Salt Road (2005).mkv")
	e.put("Glass Harbor (2016) [tmdbid-55]/Glass Harbor (2016).mkv")
	e.scan()
	e.fingerprint()
	salt := byTitle(e.items(), "movie", "Salt Road")
	glass := byTitle(e.items(), "movie", "Glass Harbor")
	if err := e.db.SetOverride(ctx, salt.ID, db.Override{Provider: "tmdb", Value: "7"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetOverride(ctx, glass.ID, db.Override{Provider: "none"}, 1); err != nil {
		t.Fatal(err)
	}
	e.scan()
	if m, _ := e.db.MatchOf(ctx, glass.ID); m.State != "ignored" || len(m.ExternalIDs) != 0 {
		t.Fatalf("an id in the name came back over the override: %+v", m)
	}

	// The film goes, files and all, and comes back: a new item, matched
	// as its folder was.
	os.RemoveAll(filepath.Join(e.root, "Salt Road (2005)"))
	e.scan()
	if err := e.db.ApplyScan(ctx, e.lib.Roots[0].ID, db.ScanChanges{}, 1, 1<<62); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(ctx, e.db, e.lib.ID, 3); err != nil {
		t.Fatal(err)
	}
	e.put("Salt Road (2005)/Salt Road (2005).mkv")
	e.scan()
	again := byTitle(e.items(), "movie", "Salt Road")
	if again.ID == salt.ID {
		t.Fatal("the film's item stayed though its file went")
	}
	if m, _ := e.db.MatchOf(ctx, again.ID); m.State != "manual" || m.ExternalIDs["tmdb"] != "7" {
		t.Fatalf("the new item of the folder: %+v", m)
	}

	// Renamed, the folder keeps its item and its match; a new folder of
	// the old name is not matched by it.
	e.fingerprint()
	e.move("Salt Road (2005)/Salt Road (2005).mkv", "Salt Road (2006)/Salt Road (2006).mkv")
	os.Remove(filepath.Join(e.root, "Salt Road (2005)"))
	e.scan()
	renamed := byTitle(e.items(), "movie", "Salt Road")
	if renamed.ID != again.ID || renamed.PathKey != "Salt Road (2006)" {
		t.Fatalf("after the rename: %+v, want item %d", renamed, again.ID)
	}
	if m, _ := e.db.MatchOf(ctx, renamed.ID); m.State != "manual" || m.ExternalIDs["tmdb"] != "7" {
		t.Fatalf("the renamed folder's match: %+v", m)
	}
	e.put("Salt Road (2005)/Salt Road (2005).mkv") // random bytes: another file
	e.scan()
	for _, it := range e.items() {
		if it.PathKey == "Salt Road (2005)" {
			if m, _ := e.db.MatchOf(ctx, it.ID); m.State != "unmatched" {
				t.Errorf("a new folder of the old name: %+v", m)
			}
		}
	}

	// Forgetting the match removes the folder's row: a build leaves the
	// item unmatched.
	if err := e.db.ForgetMatch(ctx, renamed.ID); err != nil {
		t.Fatal(err)
	}
	e.scan()
	if m, _ := e.db.MatchOf(ctx, renamed.ID); m.State != "unmatched" || len(m.ExternalIDs) != 0 {
		t.Errorf("after forgetting: %+v", m)
	}
}

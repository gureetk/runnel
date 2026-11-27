// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
)

// scanFolders scans some folders of the library's one root, as the watcher
// asks for, and builds what they hold. The env's hook then checks that a
// full build finds nothing left to change.
func (e *env) scanFolders(folders ...string) {
	e.t.Helper()
	lib, _ := e.db.Library(context.Background(), e.lib.ID)
	if err := e.s.ScanFolders(context.Background(), lib, map[int64][]string{lib.Roots[0].ID: folders}); err != nil {
		e.t.Fatal(err)
	}
}

// seqs maps each item to its updated_seq.
func seqs(items []db.ItemSummary) map[int64]int64 {
	out := map[int64]int64{}
	for _, it := range items {
		out[it.ID] = it.UpdatedSeq
	}
	return out
}

// TestABuildOfSomeFolders: a scan of some folders builds the items of
// their top folders and leaves every other item alone, unread
// (docs/06-library-and-metadata.md, section 5; issue 59).
func TestABuildOfSomeFolders(t *testing.T) {
	e := newEnv(t, "shows")
	e.put("Glass Harbor (2020)/Season 01/Glass Harbor - S01E01.mkv")
	e.put("Glass Harbor (2020)/Season 01/Glass Harbor - S01E02.mkv")
	e.put("Quiet Station (2012)/Season 01/Quiet Station - S01E01.mkv")
	e.put("Copper Tower (2007)/Season 01/Copper Tower - S01E01.mkv")
	e.scan()
	e.fingerprint()
	before := seqs(e.items())

	// A new episode in one series' folder.
	e.put("Glass Harbor (2020)/Season 01/Glass Harbor - S01E03.mkv")
	e.scanFolders("Glass Harbor (2020)/Season 01")
	items := e.items()
	glass, quiet := byTitle(items, "series", "Glass Harbor"), byTitle(items, "series", "Quiet Station")
	_, eps := children(items, glass.ID)
	if len(eps) != 3 || before[glass.ID] == glass.UpdatedSeq {
		t.Fatalf("the series that changed: %+v, %d episodes", glass, len(eps))
	}
	for _, it := range items {
		if it.ID != glass.ID && it.SeriesID != glass.ID && before[it.ID] != it.UpdatedSeq {
			t.Errorf("%s %q was written by a build of another folder", it.Type, it.Title)
		}
	}

	e.fingerprint()

	// An episode's file goes to another series' folder, and both are
	// scanned, as the watcher would: the episode follows its file.
	e2 := episodeAt(eps, 1, 2)
	e.move("Glass Harbor (2020)/Season 01/Glass Harbor - S01E02.mkv", "Quiet Station (2012)/Season 01/Quiet Station - S01E02.mkv")
	e.scanFolders("Glass Harbor (2020)/Season 01", "Quiet Station (2012)/Season 01")
	_, qeps := children(e.items(), quiet.ID)
	if moved := episodeAt(qeps, 1, 2); moved == nil || moved.ID != e2.ID {
		t.Fatalf("the moved episode: %+v, want id %d", moved, e2.ID)
	}

	// A file found missing in one scan and at another series' folder in a
	// later scan of that folder alone: the build reads the folder of the
	// item that plays it too, and the episode follows it.
	_, geps := children(e.items(), glass.ID)
	e3 := episodeAt(geps, 1, 3)
	from := filepath.Join(e.root, "Glass Harbor (2020)", "Season 01", "Glass Harbor - S01E03.mkv")
	data, _ := os.ReadFile(from)
	os.Remove(from)
	e.scanFolders("Glass Harbor (2020)/Season 01")
	if still := episodeAt(children2(e.items(), glass.ID), 1, 3); still == nil || still.ID != e3.ID {
		t.Fatalf("a missing file's episode stays, unavailable: %+v", still)
	}
	e.write("Copper Tower (2007)/Season 01/Copper Tower - S01E02.mkv", data)
	e.scanFolders("Copper Tower (2007)/Season 01")
	copper := byTitle(e.items(), "series", "Copper Tower")
	if moved := episodeAt(children2(e.items(), copper.ID), 1, 2); moved == nil || moved.ID != e3.ID {
		t.Fatalf("the episode found again in another folder: %+v, want id %d", moved, e3.ID)
	}
	if left := children2(e.items(), glass.ID); len(left) != 1 {
		t.Fatalf("the series it left: %+v", left)
	}
}

func children2(items []db.ItemSummary, seriesID int64) []db.ItemSummary {
	_, eps := children(items, seriesID)
	return eps
}

// TestASomeFoldersBuildReadsTheWholeTopFolder: a new folder can change how
// everything under its top folder reads, here a grouping folder that is
// one no longer, so a build of it reads all of its top folder.
func TestASomeFoldersBuildReadsTheWholeTopFolder(t *testing.T) {
	e := newEnv(t, "shows")
	e.put("series/Glass Harbor (2020)/Season 01/Glass Harbor - S01E01.mkv")
	e.put("series/Quiet Station (2012)/Season 01/Quiet Station - S01E01.mkv")
	e.put("Copper Tower (2007)/Season 01/Copper Tower - S01E01.mkv")
	e.scan()
	e.fingerprint()
	if got := kindsOf(e.items()); !slices.Equal(got, []string{"series Copper Tower (2007)",
		"series series/Glass Harbor (2020)", "series series/Quiet Station (2012)"}) {
		t.Fatalf("items %q", got)
	}
	e.put("series/Odd Show/Odd Show - 01.mkv")
	e.scanFolders("series/Odd Show")
	if got := kindsOf(e.items()); !slices.Equal(got, []string{"series Copper Tower (2007)", "series series"}) {
		t.Fatalf("items %q, want the folder read as one series again", got)
	}
	os.RemoveAll(filepath.Join(e.root, "series", "Odd Show"))
	e.scanFolders("series")
	e.purge()
	if got := kindsOf(e.items()); !slices.Equal(got, []string{"series Copper Tower (2007)",
		"series series/Glass Harbor (2020)", "series series/Quiet Station (2012)"}) {
		t.Fatalf("items %q", got)
	}
}

// write writes a file with the given bytes, an hour old.
func (e *env) write(rel string, data []byte) {
	e.t.Helper()
	p := filepath.Join(e.root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		e.t.Fatal(err)
	}
	then := time.Now().Add(-time.Hour)
	os.Chtimes(p, then, then)
}

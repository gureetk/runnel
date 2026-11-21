// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/probe"
	"github.com/gureetk/runnel/internal/scan"
)

type env struct {
	t    *testing.T
	db   *db.DB
	s    *scan.Scanner
	root string
	lib  model.Library
}

func newEnv(t *testing.T, kind string) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "runnel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	root := filepath.Join(t.TempDir(), "media")
	os.MkdirAll(root, 0o755)
	lib, err := d.CreateLibrary(ctx, "Test", kind, "default", []string{root}, 1)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: d, root: root, lib: lib}
	e.s = scan.New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.s.Built = func(ctx context.Context, id int64) error { _, err := Build(ctx, d, id, 1); return err }
	return e
}

func (e *env) put(rel string) {
	p := filepath.Join(e.root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	b := make([]byte, 3000)
	rand.Read(b)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		e.t.Fatal(err)
	}
	// A file modified in the last 10 seconds is left alone as one still
	// being written.
	then := time.Now().Add(-time.Hour)
	os.Chtimes(p, then, then)
}

func (e *env) move(from, to string) {
	os.MkdirAll(filepath.Dir(filepath.Join(e.root, to)), 0o755)
	if err := os.Rename(filepath.Join(e.root, from), filepath.Join(e.root, to)); err != nil {
		e.t.Fatal(err)
	}
}

// scan scans the library, which builds its items.
func (e *env) scan() {
	e.t.Helper()
	lib, _ := e.db.Library(context.Background(), e.lib.ID)
	if _, err := e.s.ScanLibrary(context.Background(), lib); err != nil {
		e.t.Fatal(err)
	}
}

// fingerprint stores every present file's fingerprint, as its probe would.
func (e *env) fingerprint() {
	e.t.Helper()
	known, _ := e.db.KnownFiles(context.Background(), e.lib.Roots[0].ID)
	for _, k := range known {
		if k.MissingSince != 0 {
			continue
		}
		p := filepath.Join(e.root, filepath.FromSlash(k.RelPath))
		fp, err := scan.Fingerprint(p)
		if err != nil {
			e.t.Fatal(err)
		}
		e.db.SaveProbe(context.Background(), db.ProbeTarget{FileID: k.ID, Path: p, Size: k.Size, MtimeNS: k.MtimeNS}, model.Probe{}, fp, probe.Version, 1)
	}
}

func (e *env) items() []db.ItemSummary {
	e.t.Helper()
	it, err := e.db.LibraryItems(context.Background(), e.lib.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return it
}

func (e *env) seq() int64 {
	s, _ := e.db.Seq(context.Background())
	return s
}

func byTitle(items []db.ItemSummary, typ, title string) *db.ItemSummary {
	for i := range items {
		if items[i].Type == typ && items[i].Title == title {
			return &items[i]
		}
	}
	return nil
}

func TestFilms(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("The Salt Road (2005)/The Salt Road (2005).mkv")
	e.put("Paper Comets (2019) [tmdbid-1]/Paper Comets (2019).mkv")
	e.put("Lone Lantern (1999).mp4")
	e.put("Glass River/Glass River.mkv")
	e.put("Paper Comets (2019) [tmdbid-1]/extras/Making of.mkv") // an extra: no item
	e.scan()

	items := e.items()
	if len(items) != 4 {
		t.Fatalf("%d items: %+v", len(items), items)
	}
	salt := byTitle(items, "movie", "The Salt Road")
	if salt == nil || salt.Year != 2005 || salt.SortTitle != "salt road" || salt.PathKey != "The Salt Road (2005)" || len(salt.FileIDs) != 1 {
		t.Fatalf("%+v", salt)
	}
	if lone := byTitle(items, "movie", "Lone Lantern"); lone == nil || lone.PathKey != "Lone Lantern (1999).mp4" {
		t.Fatalf("a lone file's film: %+v", lone)
	}
	if glass := byTitle(items, "movie", "Glass River"); glass == nil || glass.Year != 0 {
		t.Fatalf("a film without a year: %+v", glass)
	}
	if ids, _ := e.db.SearchTitles(context.Background(), "omet"); len(ids) != 1 || ids[0] != byTitle(items, "movie", "Paper Comets").ID {
		t.Fatalf("search for a film's title: %v", ids)
	}

	// Building again changes nothing and takes no sequence value.
	before := e.seq()
	if res, err := Build(context.Background(), e.db, e.lib.ID, 2); err != nil || res != (db.BuildResult{}) {
		t.Fatalf("a second build: %+v %v", res, err)
	}
	if e.seq() != before {
		t.Fatal("a build that changed nothing took a sequence value")
	}

	// A second file in a film's folder plays the same film.
	e.put("The Salt Road (2005)/The Salt Road (2005) - 720p.mkv")
	e.scan()
	salt2 := byTitle(e.items(), "movie", "The Salt Road")
	if salt2.ID != salt.ID || len(salt2.FileIDs) != 2 || salt2.UpdatedSeq <= salt.UpdatedSeq {
		t.Fatalf("with a second file: %+v (before %+v)", salt2, salt)
	}
}

func TestFilmFolderRenamed(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("Paper Comets (2018)/Paper Comets (2018).mkv")
	e.scan()
	e.fingerprint()
	old := byTitle(e.items(), "movie", "Paper Comets")

	// The year was wrong: the folder and the file are renamed.
	e.move("Paper Comets (2018)/Paper Comets (2018).mkv", "Paper Comets (2019)/Paper Comets (2019).mkv")
	os.Remove(filepath.Join(e.root, "Paper Comets (2018)"))
	e.scan()
	items := e.items()
	if len(items) != 1 || items[0].ID != old.ID || items[0].Year != 2019 || items[0].PathKey != "Paper Comets (2019)" {
		t.Fatalf("after the rename: %+v; want item %d kept, with the new key and year", items, old.ID)
	}
	if ts, _ := e.db.TombstonesSince(context.Background(), 0, 10); len(ts) != 0 {
		t.Fatalf("a renamed film left tombstones: %+v", ts)
	}
}

func TestFilmsRemovedTogether(t *testing.T) {
	// Two items deleted in one transaction share its sequence value.
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv")
	e.put("B Film (2002)/B Film (2002).mkv")
	e.put("C Film (2003)/C Film (2003).mkv")
	e.scan()
	gone := map[int64]bool{}
	for _, it := range e.items() {
		if it.Title != "C Film" {
			gone[it.ID] = true
		}
	}
	// Their files go missing, and after 30 days their rows are purged: a
	// purge with a cut-off in the future stands in for the wait.
	os.RemoveAll(filepath.Join(e.root, "A Film (2001)"))
	os.RemoveAll(filepath.Join(e.root, "B Film (2002)"))
	e.scan()
	if len(e.items()) != 3 {
		t.Fatal("films whose files are only missing were removed")
	}
	if err := e.db.ApplyScan(context.Background(), e.lib.Roots[0].ID, db.ScanChanges{}, 1, 1<<62); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), e.db, e.lib.ID, 3); err != nil {
		t.Fatal(err)
	}
	ts, _ := e.db.TombstonesSince(context.Background(), 0, 10)
	if len(ts) != 2 || ts[0].Seq != ts[1].Seq || !gone[ts[0].ItemID] || !gone[ts[1].ItemID] {
		t.Fatalf("tombstones %+v, want two at one sequence value for %v", ts, gone)
	}
	if len(e.items()) != 1 {
		t.Fatalf("%d items left", len(e.items()))
	}
	if ids, _ := e.db.SearchTitles(context.Background(), "Film"); len(ids) != 1 {
		t.Fatalf("the search table still holds removed items: %v", ids)
	}
}

func TestSeries(t *testing.T) {
	e := newEnv(t, "shows")
	e.put("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv")
	e.put("Harbor Lantern (2021)/Season 01/[Group] Harbor Lantern S01E02 [1080p].mkv")
	e.put("Harbor Lantern (2021)/Season 02/Harbor Lantern - S02E01.mkv")
	e.put("Harbor Lantern (2021)/Specials/Harbor Lantern - S00E01.mkv")
	e.put("Harbor Lantern (2021)/Season 01/Pilot.mkv") // none: no number, no item
	e.put("Salt Road (2010)/[Group] Salt Road - 112.mkv")
	e.put("Stray - S01E01.mkv") // none: not in a series folder
	e.scan()

	items := e.items()
	harbor := byTitle(items, "series", "Harbor Lantern")
	salt := byTitle(items, "series", "Salt Road")
	if harbor == nil || harbor.Year != 2021 || salt == nil {
		t.Fatalf("series: %+v", items)
	}
	eps := map[[2]int]db.ItemSummary{}
	for _, it := range items {
		if it.Type == "episode" && it.SeriesID == harbor.ID {
			eps[[2]int{it.Season, it.Episode}] = it
		}
	}
	if len(eps) != 4 || eps[[2]int{1, 2}].Title != "Episode 2" || eps[[2]int{0, 1}].ID == 0 || eps[[2]int{2, 1}].ID == 0 {
		t.Fatalf("episodes of Harbor Lantern: %+v", eps)
	}
	if n := len(items); n != 11 {
		t.Fatalf("%d items, want 2 series, 4 seasons and 5 episodes", n)
	}
	var running db.ItemSummary
	for _, it := range items {
		if it.SeriesID == salt.ID {
			running = it
		}
	}
	if running.Season != 1 || running.Episode != 112 {
		t.Fatalf("a running count: %+v", running)
	}

	// A new episode takes a new sequence value, and so does its series.
	before := e.seq()
	e.put("Harbor Lantern (2021)/Season 02/Harbor Lantern - S02E02.mkv")
	e.scan()
	harbor2 := byTitle(e.items(), "series", "Harbor Lantern")
	if harbor2.UpdatedSeq <= before || harbor2.ID != harbor.ID {
		t.Fatalf("the series did not move with its new episode: %+v", harbor2)
	}
	// Nothing changed: nothing moves.
	before = e.seq()
	e.scan()
	if e.seq() != before {
		t.Fatal("a scan and build that changed nothing took a sequence value")
	}
}

func TestSeriesFolderRenamed(t *testing.T) {
	e := newEnv(t, "shows")
	e.put("Harbor Lanterns (2021)/Season 01/Harbor Lanterns - S01E01.mkv")
	e.put("Harbor Lanterns (2021)/Season 01/Harbor Lanterns - S01E02.mkv")
	e.scan()
	e.fingerprint()
	before := map[string]int64{}
	for _, it := range e.items() {
		before[it.Type+it.Title] = it.ID
	}
	e.move("Harbor Lanterns (2021)", "Harbor Lantern (2021)")
	e.scan()
	after := e.items()
	if len(after) != 4 {
		t.Fatalf("%+v", after)
	}
	for _, it := range after {
		want := before[it.Type+it.Title]
		if it.Type == "series" {
			want = before["seriesHarbor Lanterns"]
			if it.Title != "Harbor Lantern" || it.PathKey != "Harbor Lantern (2021)" {
				t.Errorf("series %+v", it)
			}
		}
		if it.ID != want {
			t.Errorf("%s %q: id %d, want %d kept", it.Type, it.Title, it.ID, want)
		}
	}
}

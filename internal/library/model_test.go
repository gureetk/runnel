// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/parse"
)

// purge removes the rows of the files that are missing, as 30 days would,
// and builds the items again.
func (e *env) purge() {
	e.t.Helper()
	if err := e.db.ApplyScan(context.Background(), e.lib.Roots[0].ID, db.ScanChanges{}, 1, 1<<62); err != nil {
		e.t.Fatal(err)
	}
	if _, err := Build(context.Background(), e.db, e.lib.ID, 3); err != nil {
		e.t.Fatal(err)
	}
}

// children lists a series' seasons and episodes.
func children(items []db.ItemSummary, seriesID int64) (seasons, episodes []db.ItemSummary) {
	for _, it := range items {
		if it.SeriesID != seriesID {
			continue
		}
		switch it.Type {
		case "season":
			seasons = append(seasons, it)
		case "episode":
			episodes = append(episodes, it)
		}
	}
	return seasons, episodes
}

func episodeAt(eps []db.ItemSummary, season, episode int) *db.ItemSummary {
	for i := range eps {
		if eps[i].Season == season && eps[i].Episode == episode {
			return &eps[i]
		}
	}
	return nil
}

func labels(it *db.ItemSummary) []string {
	var out []string
	for _, f := range it.Files {
		out = append(out, fmt.Sprintf("%q/%d", f.Label, f.Part))
	}
	slices.Sort(out)
	return out
}

func TestSeasonsAndSpecials(t *testing.T) {
	e := newEnv(t, "shows")
	e.put("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv")
	e.put("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E03-E04.mkv")
	e.put("Harbor Lantern (2021)/Season 02/Harbor Lantern - S02E01.mkv")
	e.put("Harbor Lantern (2021)/Specials/Harbor Lantern - S00E01.mkv")
	e.scan()
	items := e.items()
	series := byTitle(items, "series", "Harbor Lantern")
	if series == nil || series.Numbering != "seasons" {
		t.Fatalf("series %+v", series)
	}
	seasons, eps := children(items, series.ID)
	titles := map[int]string{}
	ids := map[int]int64{}
	for _, s := range seasons {
		titles[s.Season], ids[s.Season] = s.Title, s.ID
		if s.ParentID != series.ID {
			t.Errorf("season %+v: its parent is the series", s)
		}
	}
	if len(seasons) != 3 || titles[1] != "Season 1" || titles[2] != "Season 2" || titles[0] != "Specials" {
		t.Fatalf("seasons %+v", seasons)
	}
	for _, ep := range eps {
		if ep.ParentID != ids[ep.Season] {
			t.Errorf("episode %+v: its parent is its season", ep)
		}
	}
	two := episodeAt(eps, 1, 3)
	if two == nil || two.End != 4 || two.Title != "Episodes 3–4" {
		t.Fatalf("a file that holds two episodes: %+v", two)
	}

	// A season that loses its last episode goes, with a tombstone, and its
	// series takes a new sequence value for its new counts.
	os.Remove(filepath.Join(e.root, "Harbor Lantern (2021)/Season 02/Harbor Lantern - S02E01.mkv"))
	e.scan()
	before := e.seq()
	e.purge()
	items = e.items()
	if seasons, _ := children(items, series.ID); len(seasons) != 2 {
		t.Fatalf("seasons after the last episode of season 2 went: %+v", seasons)
	}
	if s := byTitle(items, "series", "Harbor Lantern"); s.UpdatedSeq <= before {
		t.Fatalf("the series did not move: %+v", s)
	}
	ts, _ := e.db.TombstonesSince(context.Background(), before, 10)
	if len(ts) != 2 || (ts[0].ItemID != ids[2] && ts[1].ItemID != ids[2]) {
		t.Fatalf("tombstones %+v: want season 2 (%d) and its episode", ts, ids[2])
	}
}

func TestVersionsAndParts(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("Paper Comets (2019)/Paper Comets (2019).mkv")
	e.put("Paper Comets (2019)/Paper Comets (2019) - 720p.mkv")
	e.put("Paper Comets (2019)/Paper Comets (2019) - Directors Cut.mkv")
	e.put("Glass River (2011)/Glass River (2011) - cd1.mkv")
	e.put("Glass River (2011)/Glass River (2011) - cd2.mkv")
	e.put("Salt Road (2005)/Salt Road (2005).mkv")
	e.put("Salt Road (2005)/Salt Road (2005).mp4")
	e.put("Lone Lantern - Part 2 (1999).mkv")
	e.scan()
	e.fingerprint()
	items := e.items()
	if got := labels(byTitle(items, "movie", "Paper Comets")); !slices.Equal(got, []string{`""/0`, `"720p"/0`, `"Directors Cut"/0`}) {
		t.Fatalf("versions: %v", got)
	}
	parts := byTitle(items, "movie", "Glass River").Files
	if len(parts) != 2 || parts[0].Part != 1 || parts[1].Part != 2 || parts[0].Label != "" || parts[1].Label != "" {
		t.Fatalf("parts: %+v", parts)
	}
	if got := labels(byTitle(items, "movie", "Salt Road")); !slices.Equal(got, []string{`"mkv"/0`, `"mp4"/0`}) {
		t.Fatalf("two versions whose names tie are told apart by their extension: %v", got)
	}
	if lone := byTitle(items, "movie", "Lone Lantern - Part 2"); lone == nil || labels(lone)[0] != `""/0` {
		t.Fatalf("a part token alone is the title's: %+v", lone)
	}

	// A label that changes is a change to the film; a build after it
	// changes nothing more.
	paper := byTitle(items, "movie", "Paper Comets")
	e.move("Paper Comets (2019)/Paper Comets (2019) - 720p.mkv", "Paper Comets (2019)/Paper Comets (2019) - 1080p.mkv")
	e.scan()
	after := byTitle(e.items(), "movie", "Paper Comets")
	if got := labels(after); !slices.Equal(got, []string{`""/0`, `"1080p"/0`, `"Directors Cut"/0`}) || after.UpdatedSeq <= paper.UpdatedSeq {
		t.Fatalf("relabelled: %v %+v", got, after)
	}
	before := e.seq()
	if res, err := Build(context.Background(), e.db, e.lib.ID, 2); err != nil || res != (db.BuildResult{}) || e.seq() != before {
		t.Fatalf("a second build changed something: %+v %v", res, err)
	}
}

// TestEpisodesKeepTheirIDs checks what keeps an episode's id, which watch
// state hangs on (docs/06-library-and-metadata.md, section 5).
func TestEpisodesKeepTheirIDs(t *testing.T) {
	e := newEnv(t, "shows")
	for _, n := range []string{"01", "02", "03"} {
		e.put("Salt Road (2010)/Salt Road - " + n + ".mkv")
	}
	e.put("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E05.mkv")
	e.scan()
	e.fingerprint()
	salt := byTitle(e.items(), "series", "Salt Road")
	_, eps := children(e.items(), salt.ID)
	ids := map[int]int64{}
	for _, ep := range eps {
		ids[ep.Episode] = ep.ID
	}
	if salt.Numbering != "seasons" || len(ids) != 3 {
		t.Fatalf("before: %+v %+v", salt, eps)
	}

	// A renamed file whose number changes: the episode follows its file.
	harbor := byTitle(e.items(), "series", "Harbor Lantern")
	_, hep := children(e.items(), harbor.ID)
	e.move("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E05.mkv", "Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E06.mkv")
	e.scan()
	if _, hep2 := children(e.items(), harbor.ID); len(hep2) != 1 || hep2[0].ID != hep[0].ID || hep2[0].Episode != 6 {
		t.Fatalf("a renamed file: %+v, want item %d at episode 6", hep2, hep[0].ID)
	}

	// A running count past what a season holds makes the series one count:
	// every episode takes its running number as its key, and keeps its id.
	e.put("Salt Road (2010)/Salt Road - 100.mkv")
	e.scan()
	items := e.items()
	salt = byTitle(items, "series", "Salt Road")
	_, eps = children(items, salt.ID)
	if salt.Numbering != "absolute" || len(eps) != 4 {
		t.Fatalf("after episode 100: %+v %+v", salt, eps)
	}
	for _, ep := range eps {
		if ep.Absolute == 0 || ep.Season != 1 || ep.Episode != ep.Absolute {
			t.Errorf("an episode of a running count: %+v, want its running number in absolute, shown in season 1", ep)
		}
		if want, ok := ids[ep.Absolute]; ok && ep.ID != want {
			t.Errorf("episode %d changed id from %d to %d", ep.Absolute, want, ep.ID)
		}
	}
	if ts, _ := e.db.TombstonesSince(context.Background(), 0, 10); len(ts) != 0 {
		t.Fatalf("tombstones %+v: nothing was deleted", ts)
	}

	// A special keeps its number in a running count, and a special's number
	// does not make one.
	e.put("Salt Road (2010)/Specials/Salt Road - 01.mkv")
	e.put("Harbor Lantern (2021)/Specials/Harbor Lantern - 101.mkv")
	e.scan()
	_, eps = children(e.items(), salt.ID)
	if sp := episodeAt(eps, 0, 1); len(eps) != 5 || sp == nil || sp.Absolute != 0 || len(sp.FileIDs) != 1 {
		t.Fatalf("a special in a running count: %+v", eps)
	}
	if h := byTitle(e.items(), "series", "Harbor Lantern"); h.Numbering != "seasons" {
		t.Fatalf("a special numbered 101 made a running count: %+v", h)
	}
	os.Remove(filepath.Join(e.root, "Salt Road (2010)/Specials/Salt Road - 01.mkv"))

	// Back under 100: the episodes go back to seasons, with their ids.
	os.Remove(filepath.Join(e.root, "Salt Road (2010)/Salt Road - 100.mkv"))
	e.scan()
	e.purge()
	salt = byTitle(e.items(), "series", "Salt Road")
	_, eps = children(e.items(), salt.ID)
	if salt.Numbering != "seasons" || len(eps) != 3 {
		t.Fatalf("after episode 100 went: %+v %+v", salt, eps)
	}
	for _, ep := range eps {
		if ep.Absolute != 0 || ep.ID != ids[ep.Episode] {
			t.Errorf("back in seasons: %+v, want id %d", ep, ids[ep.Episode])
		}
	}
}

// TestMappedRunningCountKeepsIDs builds a running count as stage 4's
// mapping will: the same running numbers, shown in the provider's seasons.
// The episodes keep their ids, because their key is the running number.
func TestMappedRunningCountKeepsIDs(t *testing.T) {
	e := newEnv(t, "shows")
	for _, n := range []string{"001", "002", "027", "028", "100"} {
		e.put("Salt Road (2010)/Salt Road - " + n + ".mkv")
	}
	e.scan()
	salt := byTitle(e.items(), "series", "Salt Road")
	_, eps := children(e.items(), salt.ID)
	ids, file := map[int]int64{}, map[int]int64{}
	for _, ep := range eps {
		ids[ep.Absolute], file[ep.Absolute] = ep.ID, ep.FileIDs[0]
	}
	desired := db.DesiredSeries{RootID: salt.RootID, Key: salt.PathKey, Title: salt.Title, Year: salt.Year, Absolute: true}
	for _, m := range []struct{ abs, season, episode int }{{1, 1, 1}, {2, 1, 2}, {27, 2, 1}, {28, 2, 2}, {100, 4, 16}} {
		desired.Episodes = append(desired.Episodes, db.DesiredEpisode{Season: m.season, Episode: m.episode, Absolute: m.abs,
			Files: []db.DesiredFile{{ID: file[m.abs]}}})
	}
	if _, err := e.db.BuildItems(context.Background(), e.lib.ID, nil, []db.DesiredSeries{desired}, parse.SortTitle, 5); err != nil {
		t.Fatal(err)
	}
	seasons, eps := children(e.items(), salt.ID)
	if len(seasons) != 3 || len(eps) != 5 {
		t.Fatalf("seasons %+v, episodes %+v", seasons, eps)
	}
	for _, ep := range eps {
		if ep.ID != ids[ep.Absolute] {
			t.Errorf("episode %d: id %d, want %d", ep.Absolute, ep.ID, ids[ep.Absolute])
		}
	}
	if ep := episodeAt(eps, 4, 16); ep == nil || ep.Absolute != 100 {
		t.Fatalf("the mapped numbers: %+v", eps)
	}
}

// TestALargeBuildStampsInBatches builds more films than one sequence value
// may be stamped on (docs/03-data-model.md, section 5).
func TestALargeBuildStampsInBatches(t *testing.T) {
	e := newEnv(t, "movies")
	const n = 2*db.StampBatch + 234
	var c db.ScanChanges
	for i := range n {
		c.New = append(c.New, db.SeenFile{RelPath: fmt.Sprintf("Film %04d (2001)/Film %04d (2001).mkv", i, i), Kind: "video", Size: 1, MtimeNS: 1})
	}
	if err := e.db.ApplyScan(context.Background(), e.lib.Roots[0].ID, c, 1, 0); err != nil {
		t.Fatal(err)
	}
	before := e.seq()
	if res, err := Build(context.Background(), e.db, e.lib.ID, 1); err != nil || res.Added != n {
		t.Fatalf("%+v %v", res, err)
	}
	perSeq := map[int64]int{}
	for _, it := range e.items() {
		perSeq[it.UpdatedSeq]++
	}
	most := 0
	for _, k := range perSeq {
		most = max(most, k)
	}
	if most > db.StampBatch || len(perSeq) != 3 || e.seq() != before+3 {
		t.Fatalf("%d films over %d sequence values, at most %d on one; the sequence moved %d", n, len(perSeq), most, e.seq()-before)
	}
}

// TestLeadingNumbersInAFolder builds a series whose files begin with a
// number before a dash, and reports them anchored: their numbers are
// trusted, and none of them is a guess.
func TestLeadingNumbersInAFolder(t *testing.T) {
	e := newEnv(t, "shows")
	e.put("Paper Comets (2005)/Season 01/001 - Paper Comets - The Kite.mkv")
	e.put("Paper Comets (2005)/Season 01/002 - The Lost Lantern.mkv")
	e.put("Paper Comets (2005)/Season 01/003 - Help Find Their 2 Lost Bears.mkv")
	e.scan()
	series := byTitle(e.items(), "series", "Paper Comets")
	if series == nil {
		t.Fatal("no series")
	}
	_, eps := children(e.items(), series.ID)
	for n := 1; n <= 3; n++ {
		if episodeAt(eps, 1, n) == nil {
			t.Errorf("no S01E%02d among %+v", n, eps)
		}
	}
	if len(eps) != 3 {
		t.Fatalf("episodes %+v", eps)
	}
	rows, err := Report(context.Background(), e.db, []model.Library{e.lib})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Class != "anchored" || r.Note != "" {
			t.Errorf("%s: %s %q, want anchored with no note", r.Path, r.Class, r.Note)
		}
	}
}

// TestEpisodesWithoutANumberKeepTheirIDs builds D51's episodes without a
// number: each is its file, and keeps its id as the file is renamed to a
// name that gives a number, as any episode follows its files. A build that
// finds nothing changed changes nothing.
func TestEpisodesWithoutANumberKeepTheirIDs(t *testing.T) {
	e := newEnv(t, "shows")
	e.put("Copper Tower (2007)/Season 01/Copper Tower - S01E08.mkv")
	e.put("Copper Tower (2007)/Season 01/[Grp]Copper Tower_-_08.5_(1080p).mkv")
	e.put("Copper Tower (2007)/Season 01/Copper Tower - Behind the Lighthouse.mkv")
	e.put("Copper Tower (2007)/Copper Tower - A Walk on the Pier.mkv")
	e.scan()
	e.fingerprint()
	series := byTitle(e.items(), "series", "Copper Tower")
	if series == nil {
		t.Fatal("no series")
	}
	_, eps := children(e.items(), series.ID)
	if len(eps) != 4 {
		t.Fatalf("episodes %+v", eps)
	}
	var decimal, behind, pier *db.ItemSummary
	for i := range eps {
		switch eps[i].Title {
		case "Copper Tower - 08.5":
			decimal = &eps[i]
		case "Copper Tower - Behind the Lighthouse":
			behind = &eps[i]
		case "Copper Tower - A Walk on the Pier":
			pier = &eps[i]
		}
	}
	if decimal == nil || behind == nil || pier == nil || decimal.Season != 1 || behind.Season != 1 || pier.Season != 0 ||
		decimal.Episode != 0 || behind.Episode != 0 {
		t.Fatalf("episodes without a number: %+v", eps)
	}
	before := e.seq()
	e.scan()
	if e.seq() != before {
		t.Fatal("a build that found nothing changed took a sequence value")
	}

	// Renamed to give a number, the 8.5 keeps its id and becomes episode 10.
	e.move("Copper Tower (2007)/Season 01/[Grp]Copper Tower_-_08.5_(1080p).mkv", "Copper Tower (2007)/Season 01/Copper Tower - S01E10.mkv")
	e.scan()
	_, eps = children(e.items(), series.ID)
	ten := episodeAt(eps, 1, 10)
	if ten == nil || ten.ID != decimal.ID || ten.Title != "Episode 10" {
		t.Fatalf("the renamed file's episode: %+v, want id %d", ten, decimal.ID)
	}
}

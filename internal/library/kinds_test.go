// SPDX-License-Identifier: GPL-3.0-or-later

package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/parse"
)

// kindsOf lists a library's films and series as "type key", sorted.
func kindsOf(items []db.ItemSummary) []string {
	var out []string
	for _, it := range items {
		if it.Type == "movie" || it.Type == "series" {
			out = append(out, it.Type+" "+it.PathKey)
		}
	}
	slices.Sort(out)
	return out
}

func (e *env) report() map[string]ReportRow {
	e.t.Helper()
	lib, _ := e.db.Library(context.Background(), e.lib.ID)
	rows, err := Report(context.Background(), e.db, []model.Library{lib})
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]ReportRow{}
	for _, r := range rows {
		out[r.Path] = r
	}
	return out
}

// TestMixedLibrary is exit criterion 9 of stage 4 (docs/01-roadmap.md,
// section 6): in a mixed library every folder that holds a season folder,
// an SxxEyy token or two anchored episode numbers is a series, and every
// other folder is a film. The parse report names each film folder whose
// files look like episodes.
func TestMixedLibrary(t *testing.T) {
	e := newEnv(t, "mixed")
	// Series: by a season folder, by a token, by two numbers after the
	// series' title, and by a folder's own numbering.
	e.put("Glass Harbor (2020)/Season 01/[Grp] Glass Harbor - 01.mkv")
	e.put("Quiet Station (2012)/Quiet Station - S01E01.mkv")
	e.put("Copper Tower (2007)/[Grp] Copper Tower - 01 [1080p].mkv")
	e.put("Copper Tower (2007)/[Grp] Copper Tower - 02 [1080p].mkv")
	e.put("Paper Comets (2005)/017 - The Kite.mkv")
	e.put("Paper Comets (2005)/018 - The Ferry.mkv")
	// Films: one video, a number in the title, a franchise's number,
	// versions, and parts, which give two numbers after the title but are
	// read as parts.
	e.put("Winter Letters (2018)/[Grp] Winter Letters (BD 1080p) [1A2B3C4D].mkv")
	e.put("7 Lanterns (2007)/[Grp] 7 Lanterns (BD 1080p).mkv")
	e.put("Tide Runner The Lost Reef (2005)/[Grp] Tide Runner - Movie 06 - The Lost Reef.mkv")
	e.put("Amber Atlas (1969)/Amber Atlas (1969) - 1080p.mkv")
	e.put("Amber Atlas (1969)/Amber Atlas (1969) - 720p.mkv")
	e.put("Salt Road (2010)/Salt Road (2010) - Part 1.mkv")
	e.put("Salt Road (2010)/Salt Road (2010) - Part 2.mkv")
	// A film's folder whose files look like episodes: numbers after a dash,
	// under a name that is not the folder's.
	e.put("Ferry Signal (2016)/[Grp] Night Ferry - 01.mkv")
	e.put("Ferry Signal (2016)/[Grp] Night Ferry - 02.mkv")
	// Lone files at the top: a film, and one with a token, which makes no
	// item.
	e.put("Lone Lantern (1999).mp4")
	e.put("Stray Cove - S01E01.mkv")
	e.scan()

	got := kindsOf(e.items())
	want := []string{
		"movie 7 Lanterns (2007)", "movie Amber Atlas (1969)", "movie Ferry Signal (2016)", "movie Lone Lantern (1999).mp4",
		"movie Salt Road (2010)", "movie Tide Runner The Lost Reef (2005)", "movie Winter Letters (2018)",
		"series Copper Tower (2007)", "series Glass Harbor (2020)", "series Paper Comets (2005)", "series Quiet Station (2012)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("items:\n got %q\nwant %q", got, want)
	}
	items := e.items()
	if salt := byTitle(items, "movie", "Salt Road"); len(salt.Files) != 2 || salt.Files[0].Part != 1 || salt.Files[1].Part != 2 {
		t.Errorf("a film in two parts: %+v", salt.Files)
	}
	paper := byTitle(items, "series", "Paper Comets")
	_, eps := children(items, paper.ID)
	if len(eps) != 2 || episodeAt(eps, 1, 17) == nil || episodeAt(eps, 1, 18) == nil {
		t.Errorf("a folder's own numbering: %+v", eps)
	}

	rep := e.report()
	for path, class := range map[string]string{
		"Glass Harbor (2020)/Season 01/[Grp] Glass Harbor - 01.mkv": parse.Anchored,
		"Quiet Station (2012)/Quiet Station - S01E01.mkv":           parse.Explicit,
		"Paper Comets (2005)/017 - The Kite.mkv":                    parse.Anchored,
		"7 Lanterns (2007)/[Grp] 7 Lanterns (BD 1080p).mkv":         parse.Explicit,
		"Lone Lantern (1999).mp4":                                   parse.Explicit,
		"Stray Cove - S01E01.mkv":                                   parse.None,
	} {
		if r := rep[path]; r.Class != class {
			t.Errorf("%s: class %s, want %s", path, r.Class, class)
		}
	}
	if r := rep["7 Lanterns (2007)/[Grp] 7 Lanterns (BD 1080p).mkv"]; r.IsEpisode || r.Title != "7 Lanterns" || r.Year != 2007 {
		t.Errorf("a film's file in the report: %+v", r)
	}
	if r := rep["Paper Comets (2005)/017 - The Kite.mkv"]; !r.IsEpisode || r.Season != 1 || r.Episode != 17 {
		t.Errorf("an episode's file in the report: %+v", r)
	}
	// The report names the film folder whose files look like episodes, and
	// no other.
	for path, r := range rep {
		looks := strings.Contains(r.Note, filmLooksLikeEpisodes)
		if want := strings.HasPrefix(path, "Ferry Signal (2016)/"); looks != want {
			t.Errorf("%s: noted as a film that looks like episodes %v, want %v (%q)", path, looks, want, r.Note)
		}
	}
	if r := rep["Stray Cove - S01E01.mkv"]; r.Note == "" {
		t.Error("a lone file with a token says why it makes no item")
	}
}

// TestGroupingFolders is exit criterion 10 of stage 4: films and series
// inside grouping folders, nested or not, are built as if their folders
// were at the top of the root, in films, shows and mixed libraries. A
// grouping folder makes no item.
func TestGroupingFolders(t *testing.T) {
	films := []string{
		"films/Amber Atlas (1969)/Amber Atlas (1969).mkv",
		"films/classics/Winter Letters (2018)/Winter Letters (2018).mkv",
		"films/classics/older/Tide Runner (1952)/Tide Runner (1952).mkv",
		"Paper Comets (2019)/Paper Comets (2019).mkv",
	}
	shows := []string{
		"series/Glass Harbor (2020)/Season 03/Glass Harbor - S03E01.mkv",
		"series/Quiet Station/Season 01/Quiet Station - S01E01.mkv",
		"series/anime/Copper Tower (2007)/Season 01/[Grp] Copper Tower - 01.mkv",
		"Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv",
	}
	for _, c := range []struct {
		kind  string
		files []string
		want  []string
	}{
		{"movies", films, []string{"movie Paper Comets (2019)", "movie films/Amber Atlas (1969)",
			"movie films/classics/Winter Letters (2018)", "movie films/classics/older/Tide Runner (1952)"}},
		{"shows", shows, []string{"series Harbor Lantern (2021)", "series series/Glass Harbor (2020)",
			"series series/Quiet Station", "series series/anime/Copper Tower (2007)"}},
		{"mixed", append(slices.Clone(films), shows...), []string{"movie Paper Comets (2019)", "movie films/Amber Atlas (1969)",
			"movie films/classics/Winter Letters (2018)", "movie films/classics/older/Tide Runner (1952)",
			"series Harbor Lantern (2021)", "series series/Glass Harbor (2020)", "series series/Quiet Station",
			"series series/anime/Copper Tower (2007)"}},
	} {
		t.Run(c.kind, func(t *testing.T) {
			e := newEnv(t, c.kind)
			for _, f := range c.files {
				e.put(f)
			}
			e.scan()
			items := e.items()
			if got := kindsOf(items); !slices.Equal(got, c.want) {
				t.Fatalf("items:\n got %q\nwant %q", got, c.want)
			}
			for _, it := range items {
				switch strings.ToLower(it.Title) {
				case "films", "classics", "older", "series", "anime":
					t.Errorf("a grouping folder made an item: %+v", it)
				}
			}
			if f := byTitle(items, "movie", "Winter Letters"); f != nil && (f.Year != 2018 || f.SortTitle != "winter letters") {
				t.Errorf("a film in a grouping folder: %+v", f)
			}
			if s := byTitle(items, "series", "Copper Tower"); s != nil {
				_, eps := children(items, s.ID)
				if len(eps) != 1 || eps[0].Episode != 1 {
					t.Errorf("a series in a nested grouping folder: %+v", eps)
				}
			}
			for _, r := range e.report() {
				if r.Class == parse.None || r.Class == parse.Guess {
					t.Errorf("%s: class %s", r.Path, r.Class)
				}
			}
			// Nothing changed: nothing moves.
			before := e.seq()
			e.scan()
			if e.seq() != before {
				t.Error("a scan that changed nothing took a sequence value")
			}
		})
	}
}

// A folder in a grouping folder's place that has a folder without a year
// or a season folder in it is read as before: one title.
func TestNotAGroupingFolder(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("Odd Lot/Ferry Signal/Ferry Signal.mkv")
	e.put("Odd Lot/Salt Road (2010)/Salt Road.mkv")
	e.scan()
	if got := kindsOf(e.items()); !slices.Equal(got, []string{"movie Odd Lot"}) {
		t.Fatalf("items %q", got)
	}
}

// played marks the items played for a person.
func (e *env) played(user int64, items ...*db.ItemSummary) {
	e.t.Helper()
	var ids []int64
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	if _, err := e.db.MarkPlayed(context.Background(), user, ids, true, 5); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) isPlayed(user, item int64) bool {
	e.t.Helper()
	st, err := e.db.StatesOf(context.Background(), user, []int64{item})
	if err != nil {
		e.t.Fatal(err)
	}
	return st[item].Played
}

func (e *env) toMixed() {
	e.t.Helper()
	kind := "mixed"
	if _, err := e.db.ChangeLibrary(context.Background(), e.lib.ID, db.LibraryChange{Kind: &kind}, 6); err != nil {
		e.t.Fatal(err)
	}
	e.scan()
}

// keyed gives each item a key by what it is: a film or a series by its
// folder, a season by its series' folder and number, an episode by those
// and its own number and title.
func keyed(items []db.ItemSummary) map[string]db.ItemSummary {
	folder := map[int64]string{}
	for _, it := range items {
		if it.Type == "series" {
			folder[it.ID] = it.PathKey
		}
	}
	out := map[string]db.ItemSummary{}
	for _, it := range items {
		k := it.Type + " " + it.PathKey
		switch it.Type {
		case "season":
			k = fmt.Sprintf("season %s %d", folder[it.SeriesID], it.Season)
		case "episode":
			k = fmt.Sprintf("episode %s %d %d %s", folder[it.SeriesID], it.Season, it.Episode, it.Title)
		}
		out[k] = it
	}
	return out
}

func (e *env) user() int64 {
	e.t.Helper()
	u, err := e.db.CreateUser(context.Background(), "ada", nil, true, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

// TestFilmsChangedToMixed is half of exit criterion 11 of stage 4: a films
// library changed to mixed keeps every item that reads the same, with its
// id and watch state. A film whose folder now reads as a series' goes, and
// its watch state with it (D49).
func TestFilmsChangedToMixed(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("Amber Atlas (1969)/Amber Atlas (1969).mkv")
	e.put("Amber Atlas (1969)/Amber Atlas (1969) - 720p.mkv")
	e.put("films/Winter Letters (2018)/Winter Letters (2018).mkv")
	e.put("Lone Lantern (1999).mp4")
	e.put("Glass Harbor (2020)/Glass Harbor - S01E01.mkv")
	e.put("Glass Harbor (2020)/Glass Harbor - S01E02.mkv")
	e.scan()
	e.fingerprint()
	ada := e.user()
	items := e.items()
	amber, winter := byTitle(items, "movie", "Amber Atlas"), byTitle(items, "movie", "Winter Letters")
	lone, glass := byTitle(items, "movie", "Lone Lantern"), byTitle(items, "movie", "Glass Harbor")
	e.played(ada, amber, winter, lone, glass)

	e.toMixed()
	items = e.items()
	for _, kept := range []*db.ItemSummary{amber, winter, lone} {
		now := byTitle(items, "movie", kept.Title)
		if now == nil || now.ID != kept.ID || now.PathKey != kept.PathKey || len(now.Files) != len(kept.Files) {
			t.Errorf("%s: %+v, want id %d kept", kept.Title, now, kept.ID)
			continue
		}
		if !e.isPlayed(ada, now.ID) {
			t.Errorf("%s lost its watch state", kept.Title)
		}
	}
	if byTitle(items, "movie", "Glass Harbor") != nil {
		t.Error("a folder of episodes is still a film")
	}
	series := byTitle(items, "series", "Glass Harbor")
	if series == nil || series.ID == glass.ID {
		t.Fatalf("the series: %+v", series)
	}
	_, eps := children(items, series.ID)
	if len(eps) != 2 {
		t.Fatalf("its episodes: %+v", eps)
	}
	for _, ep := range eps {
		if e.isPlayed(ada, ep.ID) {
			t.Errorf("a new episode has the old film's watch state: %+v", ep)
		}
	}
	ts, _ := e.db.TombstonesSince(context.Background(), 0, 10)
	if len(ts) != 1 || ts[0].ItemID != glass.ID {
		t.Errorf("tombstones %+v, want the old film's", ts)
	}
	lib, _ := e.db.Library(context.Background(), e.lib.ID)
	if lib.Kind != "mixed" {
		t.Errorf("kind %q", lib.Kind)
	}
}

// TestShowsChangedToMixed is the other half of exit criterion 11: a shows
// library changed to mixed keeps every series that reads the same, with
// the ids of its seasons and episodes and their watch state. A series
// whose folder now reads as a film's goes, and its watch state with it.
func TestShowsChangedToMixed(t *testing.T) {
	e := newEnv(t, "shows")
	e.put("Glass Harbor (2020)/Season 01/Glass Harbor - S01E01.mkv")
	e.put("Glass Harbor (2020)/Season 01/Glass Harbor - S01E02.mkv")
	e.put("Copper Tower (2007)/[Grp] Copper Tower - 01.mkv")
	e.put("Copper Tower (2007)/[Grp] Copper Tower - 02.mkv")
	e.put("Paper Comets (2005)/Season 01/017 - The Kite.mkv")
	// A film in its own folder: in a shows library, a series with one
	// episode without a number (D51).
	e.put("Winter Letters (2018)/[Grp] Winter Letters (BD 1080p).mkv")
	e.scan()
	e.fingerprint()
	ada := e.user()
	items := e.items()
	before := keyed(items)
	var all []*db.ItemSummary
	for i := range items {
		if items[i].Type == "episode" {
			all = append(all, &items[i])
		}
	}
	e.played(ada, all...)
	winter := byTitle(items, "series", "Winter Letters")

	e.toMixed()
	items = e.items()
	kept := 0
	for k, it := range keyed(items) {
		if strings.Contains(k, "Winter Letters") {
			if before[k].ID == it.ID {
				t.Errorf("an item of the series that became a film kept its id: %+v", it)
			}
			continue
		}
		if before[k].ID != it.ID {
			t.Errorf("%s: id %d, want %d kept", k, it.ID, before[k].ID)
			continue
		}
		kept++
		if it.Type == "episode" && !e.isPlayed(ada, it.ID) {
			t.Errorf("%s lost its watch state", k)
		}
	}
	// Three series, three seasons and five episodes read the same.
	if kept != 11 {
		t.Errorf("%d items kept, want 11", kept)
	}
	film := byTitle(items, "movie", "Winter Letters")
	if film == nil || film.PathKey != "Winter Letters (2018)" || film.ID == winter.ID || e.isPlayed(ada, film.ID) {
		t.Errorf("the film: %+v", film)
	}
	if byTitle(items, "series", "Winter Letters") != nil {
		t.Error("the series that became a film is still there")
	}
}

// TestSeriesReadThroughAsAGroupingFolder: a folder that was one series,
// and that is read through as a grouping folder now, gives its id to the
// series that holds its oldest file, which keeps its episodes' ids and
// watch state, as an item follows its files (docs/06-library-and-metadata.md,
// section 5). The other series in it are new, and an episode whose files
// went to one of them follows its files there.
func TestSeriesReadThroughAsAGroupingFolder(t *testing.T) {
	e := newEnv(t, "shows")
	// One folder without a year, holding two series' folders. Before
	// grouping folders, a folder of folders without years was one series:
	// here a folder of a year-less series in it is the difference.
	e.put("series/Glass Harbor (2020)/Season 01/Glass Harbor - S01E01.mkv")
	e.put("series/Glass Harbor (2020)/Season 01/Glass Harbor - S01E02.mkv")
	e.put("series/Quiet Station/Quiet Station - S02E01.mkv")
	e.scan()
	e.fingerprint()
	ada := e.user()
	items := e.items()
	old := byTitle(items, "series", "series")
	if old == nil {
		t.Fatalf("the folder is not one series yet: %q", kindsOf(items))
	}
	_, eps := children(items, old.ID)
	for i := range eps {
		e.played(ada, &eps[i])
	}
	e1, e2, s2 := *episodeAt(eps, 1, 1), *episodeAt(eps, 1, 2), *episodeAt(eps, 2, 1)

	// Quiet Station gets a season folder: now each folder in "series" is a
	// series', and "series" is read through.
	e.move("series/Quiet Station/Quiet Station - S02E01.mkv", "series/Quiet Station/Season 02/Quiet Station - S02E01.mkv")
	e.scan()
	items = e.items()
	if got := kindsOf(items); !slices.Equal(got, []string{"series series/Glass Harbor (2020)", "series series/Quiet Station"}) {
		t.Fatalf("items %q", got)
	}
	glass, quiet := byTitle(items, "series", "Glass Harbor"), byTitle(items, "series", "Quiet Station")
	if glass.ID != old.ID || quiet.ID == old.ID {
		t.Errorf("series ids: %d and %d, want %d for the one with the oldest file", glass.ID, quiet.ID, old.ID)
	}
	_, geps := children(items, glass.ID)
	_, qeps := children(items, quiet.ID)
	for _, c := range []struct {
		got  *db.ItemSummary
		want db.ItemSummary
	}{{episodeAt(geps, 1, 1), e1}, {episodeAt(geps, 1, 2), e2}, {episodeAt(qeps, 2, 1), s2}} {
		if c.got == nil || c.got.ID != c.want.ID || !e.isPlayed(ada, c.got.ID) {
			t.Errorf("episode %+v, want id %d kept with its watch state", c.got, c.want.ID)
		}
	}
}

// A change of kind other than to mixed, or from mixed, is refused, and
// changes nothing.
func TestOnlyToMixed(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "shows")
	for _, k := range []string{"movies", "videos", "anime"} {
		if _, err := e.db.ChangeLibrary(ctx, e.lib.ID, db.LibraryChange{Kind: &k}, 2); err != db.ErrKindChange {
			t.Errorf("shows to %s: %v", k, err)
		}
	}
	same := "shows"
	if lib, err := e.db.ChangeLibrary(ctx, e.lib.ID, db.LibraryChange{Kind: &same}, 2); err != nil || lib.Kind != "shows" {
		t.Errorf("shows to shows: %+v %v", lib, err)
	}
	e.toMixed()
	for _, k := range []string{"movies", "shows"} {
		if _, err := e.db.ChangeLibrary(ctx, e.lib.ID, db.LibraryChange{Kind: &k}, 2); err != db.ErrKindChange {
			t.Errorf("mixed to %s: %v", k, err)
		}
	}
	if lib, _ := e.db.Library(ctx, e.lib.ID); lib.Kind != "mixed" {
		t.Errorf("kind %q", lib.Kind)
	}
}

// In a mixed library a folder can change kind as files come and go: one
// video numbered after the title is a film, a second makes it a series. The
// folder's new item takes its key, and the old one goes with a tombstone.
func TestAFolderChangesKind(t *testing.T) {
	e := newEnv(t, "mixed")
	e.put("Night Ferry (2016)/[Grp] Night Ferry - 01.mkv")
	e.scan()
	film := byTitle(e.items(), "movie", "Night Ferry")
	if film == nil {
		t.Fatalf("one video: %q", kindsOf(e.items()))
	}
	e.put("Night Ferry (2016)/[Grp] Night Ferry - 02.mkv")
	e.scan()
	if got := kindsOf(e.items()); !slices.Equal(got, []string{"series Night Ferry (2016)"}) {
		t.Fatalf("two videos: %q", got)
	}
	series := byTitle(e.items(), "series", "Night Ferry")
	ts, _ := e.db.TombstonesSince(context.Background(), 0, 10)
	if len(ts) != 1 || ts[0].ItemID != film.ID {
		t.Errorf("tombstones %+v, want the film's", ts)
	}
	// The second goes, and its row after 30 days.
	os.Remove(filepath.Join(e.root, "Night Ferry (2016)", "[Grp] Night Ferry - 02.mkv"))
	e.scan()
	e.purge()
	if got := kindsOf(e.items()); !slices.Equal(got, []string{"movie Night Ferry (2016)"}) {
		t.Fatalf("one video again: %q", got)
	}
	if f := byTitle(e.items(), "movie", "Night Ferry"); f.ID == film.ID || f.ID == series.ID {
		t.Errorf("the film again: %+v", f)
	}
}

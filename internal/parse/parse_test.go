// SPDX-License-Identifier: GPL-3.0-or-later

package parse

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestNames checks every row of testdata/names.tsv.
func TestNames(t *testing.T) {
	f, err := os.Open("testdata/names.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	rows := 0
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		c := strings.Split(text, "\t")
		if len(c) != 8 {
			t.Fatalf("line %d: %d columns, want 8", line, len(c))
		}
		rows++
		kind, rel, class, title := c[0], c[1], c[2], c[3]
		num := func(s string) (int, bool) {
			if s == "-" {
				return 0, false
			}
			n, err := strconv.Atoi(s)
			if err != nil {
				t.Fatalf("line %d: %q is not a number", line, s)
			}
			return n, true
		}
		year, _ := num(c[4])
		switch kind {
		case "film":
			got := ParseFilm(rel)
			if got.Class != class || got.Title != title || got.Year != year {
				t.Errorf("line %d: %s\n got %s %q %d\nwant %s %q %d", line, rel, got.Class, got.Title, got.Year, class, title, year)
			}
		case "episode":
			got := ParseEpisode(rel)
			bad := got.Class != class || got.SeriesTitle != title || got.SeriesYear != year
			if s, ok := num(c[5]); ok && got.Season != s {
				bad = true
			}
			if e, ok := num(c[6]); ok && got.Episode != e {
				bad = true
			}
			if e, ok := num(c[7]); ok && got.EpisodeEnd != e {
				bad = true
			}
			if bad {
				t.Errorf("line %d: %s\n got %s %q %d S%d E%d-%d\nwant %s %q %s S%s E%s-%s",
					line, rel, got.Class, got.SeriesTitle, got.SeriesYear, got.Season, got.Episode, got.EpisodeEnd,
					class, title, c[4], c[5], c[6], c[7])
			}
		default:
			t.Fatalf("line %d: kind %q", line, kind)
		}
	}
	if rows < 50 {
		t.Fatalf("only %d rows", rows)
	}
}

func TestSeasonDisagreementIsNoted(t *testing.T) {
	e := ParseEpisode("Harbor Lantern (2021)/Season 03/Harbor Lantern - S01E13.mkv")
	if e.Season != 1 || e.Note == "" {
		t.Fatalf("%+v: the name's season is taken, and the report says the folder disagrees", e)
	}
	if e := ParseEpisode("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E13.mkv"); e.Note != "" {
		t.Fatalf("a note where folder and name agree: %q", e.Note)
	}
}

// TestNotes checks what the report is told about a name: a folder that is
// not a season folder, two numbers that disagree, a decimal.
func TestNotes(t *testing.T) {
	for rel, want := range map[string]string{
		"Glass Harbor (2020)/S01/Glass Harbor - 05.mkv":                         `in the folder "S01", which is not a season folder`,
		"Glass Harbor (2020)/Season 01 (2020)/Glass Harbor - 05.mkv":            `in the folder "Season 01 (2020)", which is not a season folder`,
		"Glass Harbor (2020)/Season 01/Disc 1/Glass Harbor - 05.mkv":            `in the folder "Disc 1", which is not a season folder`,
		"Glass Harbor (2020)/S03/Glass Harbor - S03E05.mkv":                     `in the folder "S03", which is not a season folder`,
		"Winter Kitchen (2019)/Season 01/[Grp] Winter Kitchen 3 - 05.mkv":       "the number after the title, 3, and the number after the dash, 5, differ",
		"Quiet Station (2018)/Season 01/[Grp] Quiet Station - 12.5 [1080p].mkv": "a decimal episode number: an episode without a number, after episode 12",
		"Harbor Lantern (2021)/Season 03/Harbor Lantern - S01E13.mkv":           "the name says season 01, its folder season 3",
		"Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E05.mkv":           "",
		"Harbor Lantern (2021)/Harbor Lantern - 05.mkv":                         "",
		"Harbor Lantern (2021)/Specials/Harbor Lantern - 05.mkv":                "",
		"Winter Kitchen (2019)/Season 02/[Grp] Winter Kitchen 2 - 05.mkv":       "",
	} {
		if got := ParseEpisode(rel).Note; got != want {
			t.Errorf("%s\n note %q\n want %q", rel, got, want)
		}
	}
}

func TestSeasonFolder(t *testing.T) {
	for dir, want := range map[string]int{"Season 01": 1, "Season 1": 1, "season 12": 12, "Season00": 0, "Season 2019": 2019, "Specials": 0, "specials": 0} {
		if n, ok := SeasonFolder(dir); !ok || n != want {
			t.Errorf("%q: %d %v, want %d", dir, n, ok, want)
		}
	}
	// Jellyfin's documentation names Season and a number; these are not.
	for _, dir := range []string{"S01", "SE01", "Season 01 (2020)", "Glass Harbor Season 2", "Extras", "1"} {
		if _, ok := SeasonFolder(dir); ok {
			t.Errorf("%q read as a season folder", dir)
		}
	}
}

func TestPart(t *testing.T) {
	type want struct {
		n      int
		before string
	}
	for in, w := range map[string]want{
		"Paper Comets-cd1":                       {1, "Paper Comets"},
		"Paper Comets - CD2":                     {2, "Paper Comets"},
		"Paper Comets (2019) - part2":            {2, "Paper Comets (2019)"},
		"Paper Comets (2019) [Part 3]":           {3, "Paper Comets (2019)"},
		"Paper Comets (2019)(disc 1)":            {1, "Paper Comets (2019)"},
		"Paper Comets (2019).dvd_2":              {2, "Paper Comets (2019)"},
		"Paper Comets (2019) - pt b":             {2, "Paper Comets (2019)"},
		"Paper Comets (2019) - Disk D":           {4, "Paper Comets (2019)"},
		"Harbor Lantern S02E03 Part 1":           {1, "Harbor Lantern S02E03"},
		"Harbor Lantern (2021) S01E01-part-2":    {2, "Harbor Lantern (2021) S01E01"},
		"Harbor Lantern S01E01 - 1080p - part1":  {1, "Harbor Lantern S01E01 - 1080p"},
		"Paper Comets (2019)":                    {0, "Paper Comets (2019)"},
		"Paper Comets (2019) - part2 [1080p]":    {0, "Paper Comets (2019) - part2 [1080p]"},
		"Departure 2":                            {0, "Departure 2"},
		"The Second Part":                        {0, "The Second Part"},
		"Paper Comets (2019) - Director's Cut 2": {0, "Paper Comets (2019) - Director's Cut 2"},
	} {
		if n, before := Part(in); n != w.n || before != w.before {
			t.Errorf("Part(%q) = %d %q, want %d %q", in, n, before, w.n, w.before)
		}
	}
}

func TestDistinct(t *testing.T) {
	for _, c := range []struct {
		names []string
		want  bool
	}{
		{[]string{"Paper Comets (2019)"}, true},
		{[]string{"Paper Comets (2019)", "Paper Comets (2019) - 1080p"}, true},
		// Jellyfin's examples of versions, of a film and of an episode.
		{[]string{"Paper Comets (2019) - 2160p", "Paper Comets (2019) - 1080p", "Paper Comets (2019) - Directors Cut"}, true},
		{[]string{"Harbor Lantern S01E01 - 1080p", "Harbor Lantern S01E01 - 720p", "Harbor Lantern S01E01 - Extended"}, true},
		{[]string{"Paper Comets (2019) - Directors Cut", "Paper Comets (2019) - Theatrical Cut"}, true},
		{[]string{"Paper Comets (2019) [1080p]", "Paper Comets (2019) [720p]"}, true},
		// Parts, and parts of one version beside another version.
		{[]string{"Paper Comets-cd1", "Paper Comets-cd2"}, true},
		{[]string{"S01E01 - 720p - Part 1", "S01E01 - 720p - Part 2", "S01E01 - 1080p"}, true},
		{[]string{"Harbor Lantern S01E01 - 1080p - part1", "Harbor Lantern S01E01 - 1080p - part2", "Harbor Lantern S01E01 - 4K"}, true},
		// Names that differ by a number only: episodes, more likely.
		{[]string{"[Grp] Winter Kitchen 2 - 05", "[Grp] Winter Kitchen 2 - 06", "[Grp] Winter Kitchen 2 - 07"}, false},
		{[]string{"[Grp] Winter Kitchen 2 - 05 [1080p]", "[Grp] Winter Kitchen 2 - 06 [1080p]"}, false},
		{[]string{"Harbor Lantern - S01E05", "Harbor Lantern - 05"}, false},
		{[]string{"Harbor Lantern - S01E05", "Harbor Lantern - S01E05-E06"}, false},
		{[]string{"Quiet Station - 12", "Quiet Station - 12.5"}, false},
		{[]string{"Salt Road - 05", "Salt Road - 05v2"}, false},
		// Names that do not differ: two folders, or two extensions.
		{[]string{"Harbor Lantern - S01E05", "Harbor Lantern - S01E05"}, false},
		// Two parts with the same number.
		{[]string{"Paper Comets-cd1", "Paper Comets - cd1"}, false},
	} {
		if got := Distinct(c.names); got != c.want {
			t.Errorf("Distinct(%q) = %v, want %v", c.names, got, c.want)
		}
	}
}

func TestSeriesKey(t *testing.T) {
	a := ParseEpisode("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv")
	b := ParseEpisode("Harbor Lantern (2021)/Harbor Lantern - S02E01.mkv")
	if a.SeriesKey != "Harbor Lantern (2021)" || b.SeriesKey != a.SeriesKey {
		t.Fatalf("keys %q and %q", a.SeriesKey, b.SeriesKey)
	}
	if f := ParseFilm("Lone Lantern (1999).mp4"); f.Key != "Lone Lantern (1999).mp4" {
		t.Fatalf("a lone film's key %q: the file", f.Key)
	}
	if f := ParseFilm("Paper Comets (2019)/extra/x.mkv"); f.Key != "Paper Comets (2019)" {
		t.Fatalf("a film's key %q: its folder", f.Key)
	}
}

func TestSortTitle(t *testing.T) {
	for in, want := range map[string]string{
		"The Salt Road": "salt road", "A Quiet Morning": "quiet morning", "An Echo": "echo",
		"Theory of Glass": "theory of glass", "The": "the", "Ångström Road": "ångström road",
	} {
		if got := SortTitle(in); got != want {
			t.Errorf("SortTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLeadingNumbers reads a folder whose videos begin their names with a
// number before a dash (docs/06-library-and-metadata.md, section 4.3):
// each such number alone would be a guess.
func TestLeadingNumbers(t *testing.T) {
	type want struct {
		class   string
		episode int
	}
	cases := []struct {
		name string
		rels []string
		want []want
	}{
		{"every video has one, and no two are the same: trusted", []string{
			"Paper Comets (2005)/Season 01/001 - Paper Comets - The Kite.mkv",
			"Paper Comets (2005)/Season 01/002 - The Lost Lantern.mkv",
			"Paper Comets (2005)/Season 01/003 - Help Find Their 2 Lost Bears.mkv",
			"Paper Comets (2005)/Season 01/104 - Paper Comets - The Island.mkv",
		}, []want{{Anchored, 1}, {Anchored, 2}, {Anchored, 3}, {Anchored, 104}}},
		{"underscores, and a version suffix", []string{
			"Paper Comets (2005)/Season 02/[Grp] 01_-_The Kite.mkv",
			"Paper Comets (2005)/Season 02/02v2 - The Lost Lantern.mkv",
		}, []want{{Anchored, 1}, {Anchored, 2}}},
		{"one video has none: as before", []string{
			"Paper Comets (2005)/Season 01/001 - The Kite.mkv",
			"Paper Comets (2005)/Season 01/The Lost Lantern 2.mkv",
		}, []want{{Guess, 1}, {Guess, 2}}},
		{"two are the same: as before", []string{
			"Paper Comets (2005)/Season 01/005 - The Kite.mkv",
			"Paper Comets (2005)/Season 01/005 - The Kite 2.mkv",
		}, []want{{Guess, 5}, {Guess, 2}}},
		{"a name with a token keeps it; the others are trusted", []string{
			"Paper Comets (2005)/Season 01/001 - Paper Comets - S01E01.mkv",
			"Paper Comets (2005)/Season 01/002 - The Lost Lantern.mkv",
		}, []want{{Explicit, 1}, {Anchored, 2}}},
		{"a decimal follows the numbering and is not read", []string{
			"Paper Comets (2005)/Season 01/008 - The Kite.mkv",
			"Paper Comets (2005)/Season 01/08.5 - Recap.mkv",
			"Paper Comets (2005)/Season 01/009 - The Lost Lantern.mkv",
		}, []want{{Anchored, 8}, {Unnumbered, 0}, {Anchored, 9}}},
		{"the series' year is not a leading number", []string{
			"Paper Comets (2005)/2005 - Paper Comets.mkv",
		}, []want{{Guess, 2005}}},
		{"each folder by itself", []string{
			"Paper Comets (2005)/Season 01/001 - The Kite.mkv",
			"Paper Comets (2005)/Season 02/001 - The Kite Returns.mkv",
			"Paper Comets (2005)/Season 03/Paper Comets 4.mkv",
		}, []want{{Anchored, 1}, {Anchored, 1}, {Anchored, 4}}},
		{"no dash after the number: as before", []string{
			"Paper Comets (2005)/Season 01/001 The Kite.mkv",
			"Paper Comets (2005)/Season 01/002 The Lost Lantern.mkv",
		}, []want{{Guess, 1}, {Guess, 2}}},
	}
	for _, c := range cases {
		got := ParseEpisodes(c.rels)
		for i, w := range c.want {
			if got[i].Class != w.class || got[i].Episode != w.episode {
				t.Errorf("%s: %s\n got %s %d, want %s %d", c.name, c.rels[i], got[i].Class, got[i].Episode, w.class, w.episode)
			}
		}
	}
	// A note about the folder stays; nothing else is added.
	got := ParseEpisodes([]string{
		"Paper Comets (2005)/Extra Disc/001 - The Kite.mkv",
		"Paper Comets (2005)/Extra Disc/002 - The Lost Lantern.mkv",
	})
	if got[0].Class != Anchored || got[0].Note != `in the folder "Extra Disc", which is not a season folder` {
		t.Errorf("%+v", got[0])
	}
}

// TestUnnumbered reads the episodes without a number of D51: where they sit,
// their title, and a decimal's place.
func TestUnnumbered(t *testing.T) {
	cases := []struct {
		rel     string
		season  int
		decimal float64
		title   string
	}{
		{"Quiet Station (2012)/Season 00/[Grp] Quiet Station S1 - S02.mkv", 0, 0, "Quiet Station S1 - S02"},
		{"Copper Tower (2007)/Season 01/[Grp]Copper Tower_-_08.5_(1080p).mkv", 1, 8.5, "Copper Tower - 08.5"},
		{"Copper Tower (2007)/Season 02/Copper Tower - 12.25.mkv", 2, 12.25, "Copper Tower - 12.25"},
		{"Copper Tower (2007)/Copper Tower - Behind the Lighthouse.mkv", 0, 0, "Copper Tower - Behind the Lighthouse"},
		{"Copper Tower (2007)/Copper Tower - 08.5.mkv", 1, 8.5, "Copper Tower - 08.5"},
		{"Copper Tower (2007)/Season 03/[Grp].mkv", 3, 0, "[Grp]"},
	}
	for _, c := range cases {
		e := ParseEpisode(c.rel)
		if e.Class != Unnumbered || e.Season != c.season || e.Decimal != c.decimal || e.Title != c.title || e.Episode != 0 {
			t.Errorf("%s: %+v, want season %d, decimal %v, title %q", c.rel, e, c.season, c.decimal, c.title)
		}
	}
	for _, rel := range []string{"Lone File.mkv", "(2001)/Season 01/x.mkv"} {
		if e := ParseEpisode(rel); e.Class != None {
			t.Errorf("%s: %s, want none: it makes no item", rel, e.Class)
		}
	}
}

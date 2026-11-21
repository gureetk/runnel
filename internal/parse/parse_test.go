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

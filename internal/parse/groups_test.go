// SPDX-License-Identifier: GPL-3.0-or-later

package parse

import "testing"

// TestGroups reads grouping folders by the rule of
// docs/06-library-and-metadata.md, section 4.9: a folder without a year,
// with no video and no season folder of its own, each of whose folders has
// a year, holds a season folder, or is a grouping folder itself.
func TestGroups(t *testing.T) {
	cases := []struct{ rel, group string }{
		// Rooted a level above the titles: a folder for each category.
		{"films/Amber Atlas (1969)/[Grp] Amber Atlas (BD 1080p).mkv", "films"},
		{"films/Winter Letters (2018)/Winter Letters (2018).mkv", "films"},
		{"series/Glass Harbor (2020)/Season 03/Glass Harbor - S03E01.mkv", "series"},
		// A series without a year counts by its season folder.
		{"series/Quiet Station/Season 01/Quiet Station - S01E01.mkv", "series"},
		// Nested: each folder in "anime" is a grouping folder itself.
		{"anime/films/Tide Runner (2005)/Tide Runner.mkv", "anime/films"},
		{"anime/shows/Copper Tower (2007)/Season 01/Copper Tower - S01E01.mkv", "anime/shows"},
		// Titles at the top.
		{"Paper Comets (2019)/Paper Comets (2019).mkv", ""},
		{"Lone Lantern (1999).mp4", ""},
		// A folder without a year that holds a video is a title's.
		{"Glass River/Glass River.mkv", ""},
		// One that holds a season folder is a series'.
		{"Harbor Lantern/Season 01/Harbor Lantern - S01E01.mkv", ""},
		{"Harbor Lantern/Season 02/Harbor Lantern - S02E01.mkv", ""},
		// A folder in it without a year or a season folder, and that is no
		// grouping folder, keeps the folder a title's.
		{"Odd Lot/Ferry Signal/Ferry Signal.mkv", ""},
		{"Odd Lot/Salt Road (2010)/Salt Road.mkv", ""},
		// A video of its own keeps it a title's too.
		{"Box Set/Box Film (2001)/Box Film.mkv", ""},
		{"Box Set/Box Set.mkv", ""},
		// A folder with a year is a title's, whatever it holds.
		{"Night Ferry (2016)/Night Ferry One (2016)/Night Ferry One.mkv", ""},
		// A season folder in a series' folder below a grouping folder.
		{"collected/Hollow Atlas (2026)/Season 01/Season 01/Hollow Atlas - S01E01.mkv", "collected"},
	}
	rels := make([]string, len(cases))
	for i, c := range cases {
		rels[i] = c.rel
	}
	got := Groups(rels)
	for i, c := range cases {
		if got[i] != c.group {
			t.Errorf("%s: grouping folders %q, want %q", c.rel, got[i], c.group)
		}
	}
	// A folder is read by what the root holds: alone, "Odd Lot" holds one
	// folder with a year, and is read through.
	if g := Groups([]string{"Odd Lot/Salt Road (2010)/Salt Road.mkv"}); g[0] != "Odd Lot" {
		t.Errorf("a folder whose only folder has a year: %q", g[0])
	}
}

func TestHasToken(t *testing.T) {
	for rel, want := range map[string]bool{
		"Stray - S01E01.mkv":              true,
		"[Grp] Stray_S02E03 [1080p].mkv":  true,
		"Lone Lantern (1999).mp4":         false,
		"Stray - 01.mkv":                  false,
		"Season 01/Stray - S01E01.mkv":    true,
		"[S01E01] Stray.mkv":              false, // inside brackets: not read
		"Stray.Cove.S1.E2.mkv":            true,
		"Stray Cove S01 E02 - Night.mkv":  true,
		"Stray Cove S01.E02 - Night.mkv":  true,
		"Stray Cove S01___E02.mkv":        true,
		"Stray Cove S01____E02.mkv":       false, // more than three between them
		"Stray Cove (S01E02).mkv":         false,
		"Stray Cove - Special S00E01.mkv": true,
	} {
		if got := HasToken(rel); got != want {
			t.Errorf("%s: %v, want %v", rel, got, want)
		}
	}
}

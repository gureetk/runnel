// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import "testing"

func TestNormalise(t *testing.T) {
	for in, want := range map[string]string{
		"The Salt Road":                 "salt road",
		"Tide Runner: The Lost Reef":    "tide runner the lost reef",
		"Tide Runner - The Lost Reef":   "tide runner the lost reef",
		"Winter's Letters":              "winters letters",
		"Copper & Glass":                "copper and glass",
		"Ōkami no Uta":                  "okami no uta",
		"Café Lumière":                  "cafe lumiere",
		"波の音":                           "波の音",
		"A":                             "a",
		"  Glass   Harbor (Remastered)": "glass harbor remastered",
	} {
		if got := normalise(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// TestScore: the near misses of a real library, with invented titles
// (P4: taken at 0.90 with a lead of 0.10).
func TestScore(t *testing.T) {
	for _, c := range []struct {
		name                string
		title               string
		year                int
		candTitle, candOrig string
		candYear            int
		want                float64
	}{
		{"same title and year", "Salt Road", 2005, "Salt Road", "", 2005, 1},
		{"a leading article", "The Salt Road", 2005, "Salt Road", "", 2005, 1},
		{"a year apart", "Winter Letters", 2018, "Winter Letters", "", 2019, 0.9},
		{"no year in the name", "Glass Harbor", 0, "Glass Harbor", "", 1958, 0.9},
		{"the same title another year", "Salt Road", 2005, "Salt Road", "", 2019, 0.8},
		{"a sequel", "Tide Runner", 2005, "Tide Runner 2", "", 2008, 0.692},
		{"the original title", "波の音", 2010, "Sound of Waves", "波の音", 2010, 1},
		{"a romanised title", "Nami no Oto", 2010, "Sound of Waves", "波の音", 2010, 0.4},
	} {
		if got := round3(Score(c.title, c.year, c.candTitle, c.candOrig, c.candYear)); got != round3(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, round3(c.want))
		}
	}
}

func TestDecide(t *testing.T) {
	c := func(scores ...float64) []Candidate {
		var out []Candidate
		for _, s := range scores {
			out = append(out, Candidate{Score: s})
		}
		return out
	}
	for _, tc := range []struct {
		cands []Candidate
		want  string
	}{
		{nil, "no_results"},
		{c(1), ""},
		{c(0.9), ""},
		{c(0.899), "low_score"},
		{c(1, 0.9), ""},
		{c(1, 0.901), "too_close"},
		{c(0.9, 0.9), "too_close"},
		{c(1, 0.692), ""},
	} {
		if got := decide(tc.cands).reason; got != tc.want {
			t.Errorf("%v: %q, want %q", tc.cands, got, tc.want)
		}
	}
}

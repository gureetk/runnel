// SPDX-License-Identifier: GPL-3.0-or-later

package parse

import (
	"maps"
	"testing"
)

func TestIDs(t *testing.T) {
	for _, c := range []struct {
		name string
		want map[string]string
	}{
		{"Paper Comets (2019)", nil},
		{"Paper Comets (2019) [tmdbid-101]", map[string]string{"tmdb": "101"}},
		{"Paper Comets (2019) [imdbid-tt0000101]", map[string]string{"imdb": "tt0000101"}},
		{"Harbor Lantern (2021) [tvdbid-202]", map[string]string{"tvdb": "202"}},
		// Several together, either separator, any case, spaces inside.
		{"Paper Comets (2019) [imdbid-tt0000101][TMDBID=101]", map[string]string{"imdb": "tt0000101", "tmdb": "101"}},
		{"Paper Comets (2019) [ImdbId-TT0000101] [tvdbid= 303 ]", map[string]string{"imdb": "tt0000101", "tvdb": "303"}},
		// The first of a provider counts.
		{"Paper Comets [tmdbid-101] [tmdbid-102]", map[string]string{"tmdb": "101"}},
		// Values of the wrong form, other providers, other brackets.
		{"Paper Comets [tmdbid-tt101] [imdbid-101] [tvdbid-]", nil},
		{"Paper Comets [anidbid-101] {tmdb-101} (tmdbid-101)", nil},
		{"Paper Comets (2019) [imdbid-tt0000101] - 1080p", map[string]string{"imdb": "tt0000101"}},
	} {
		if got := IDs(c.name); !maps.Equal(got, c.want) {
			t.Errorf("IDs(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

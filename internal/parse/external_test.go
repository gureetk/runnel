// SPDX-License-Identifier: GPL-3.0-or-later

package parse

import "testing"

func TestParseTrack(t *testing.T) {
	for _, c := range []struct {
		name, base string
		ok         bool
		want       Track
	}{
		// The examples of Jellyfin's documentation, with the video's own base.
		{"Film.default.srt", "Film", true, Track{Kind: "subtitle", Codec: "subrip", Default: true}},
		{"Film.default.en.forced.ass", "Film", true, Track{Kind: "subtitle", Codec: "ass", Lang: "eng", Default: true, Forced: true}},
		{"Film.forced.en.dts", "Film", true, Track{Kind: "audio", Codec: "dts", Lang: "eng", Forced: true}},
		{"Film.en.sdh.srt", "Film", true, Track{Kind: "subtitle", Codec: "subrip", Lang: "eng", HearingImpaired: true}},
		{"Film.English Commentary.en.mp3", "Film", true, Track{Kind: "audio", Codec: "mp3", Lang: "eng", Title: "English Commentary"}},
		{"Series Name A (2021) S01E01 Title.ja.ass", "Series Name A (2021) S01E01 Title", true, Track{Kind: "subtitle", Codec: "ass", Lang: "jpn"}},
		{"Series Name A (2021) S01E01 Title.commentary.ja.aac", "Series Name A (2021) S01E01 Title", true, Track{Kind: "audio", Codec: "aac", Lang: "jpn", Title: "commentary"}},
		// hi alone is Hindi; beside another language it is a flag.
		{"Film.hi.srt", "Film", true, Track{Kind: "subtitle", Codec: "subrip", Lang: "hin"}},
		{"Film.en.hi.srt", "Film", true, Track{Kind: "subtitle", Codec: "subrip", Lang: "eng", HearingImpaired: true}},
		// Three letters, both kinds kept as the bibliographic code; foreign
		// and cc; a name with no fields; title words kept in their case.
		{"Paper Comets (2019).deu.foreign.ssa", "Paper Comets (2019)", true, Track{Kind: "subtitle", Codec: "ass", Lang: "ger", Forced: true}},
		{"Paper Comets (2019).fre.cc.vtt", "Paper Comets (2019)", true, Track{Kind: "subtitle", Codec: "webvtt", Lang: "fre", HearingImpaired: true}},
		{"Paper Comets (2019).srt", "Paper Comets (2019)", true, Track{Kind: "subtitle", Codec: "subrip"}},
		{"Paper Comets (2019).Signs.Songs.eng.SUP", "Paper Comets (2019)", true, Track{Kind: "subtitle", Lang: "eng", Title: "Signs Songs"}},
		{"Paper Comets (2019).jpn.mka", "Paper Comets (2019)", true, Track{Kind: "audio", Lang: "jpn"}},
		// Not this video's.
		{"Paper Comets (2019) - 720p.eng.srt", "Paper Comets (2019)", false, Track{}},
		{"Paper Comets.eng.srt", "Paper Comets (2019)", false, Track{}},
		{"Paper Comets (2019).eng.nfo", "Paper Comets (2019)", false, Track{}},
	} {
		got, ok := ParseTrack(c.name, c.base)
		if ok != c.ok || got != c.want {
			t.Errorf("%q of %q: %+v %v, want %+v %v", c.name, c.base, got, ok, c.want, c.ok)
		}
	}
}

func TestTrackOwner(t *testing.T) {
	bases := []string{"Film (2001)", "Film (2001) - 720p", "Film.Extended", "Film"}
	for name, want := range map[string]string{
		"Film (2001).eng.srt":         "Film (2001)",
		"Film (2001) - 720p.eng.srt":  "Film (2001) - 720p",
		"Film.Extended.eng.srt":       "Film.Extended",
		"Film.eng.srt":                "Film",
		"Glass River (2011).eng.srt":  "",
		"Film (2001) - 1080p.eng.srt": "",
	} {
		if got := TrackOwner(name, bases); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
	if TrackKind("a.FLAC") != "audio" || TrackKind("a.srt") != "subtitle" || TrackKind("a.mkv") != "" {
		t.Fatal("track kinds")
	}
}

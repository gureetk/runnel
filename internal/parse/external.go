// SPDX-License-Identifier: GPL-3.0-or-later

package parse

import (
	"path"
	"strings"
)

// External subtitles and audio (docs/06-library-and-metadata.md, section
// 4.7).

// trackKinds are the extensions of the files that are a video's tracks
// when they sit beside it. codec is ffprobe's name for what the
// extension holds, or "" where the extension does not say: a .sub is
// MicroDVD text or VobSub pictures, and an .mka can hold any audio.
var trackKinds = map[string]struct{ kind, codec string }{
	".srt": {"subtitle", "subrip"}, ".ass": {"subtitle", "ass"}, ".ssa": {"subtitle", "ass"},
	".vtt": {"subtitle", "webvtt"}, ".sub": {"subtitle", ""}, ".sup": {"subtitle", ""},
	".mka": {"audio", ""}, ".ac3": {"audio", "ac3"}, ".flac": {"audio", "flac"}, ".opus": {"audio", "opus"},
	".dts": {"audio", "dts"}, ".mp3": {"audio", "mp3"}, ".aac": {"audio", "aac"},
}

// TrackKind is "subtitle" or "audio" for a file whose extension makes it
// a track of the video beside it, and "" for any other.
func TrackKind(name string) string {
	return trackKinds[strings.ToLower(path.Ext(name))].kind
}

// Track is what a track's file name says about it.
type Track struct {
	Kind            string // "subtitle" or "audio"
	Codec           string // from the extension; "" when it does not say
	Lang            string // ISO 639-2, bibliographic; "" when the name gives none
	Title           string
	Default         bool
	Forced          bool
	HearingImpaired bool
}

// ParseTrack reads the name of a track file as a track of the video whose
// base name is videoBase. ok is false when the name is not one of that
// video's tracks: it must be the base name and a dot, then fields
// separated by dots, then the extension. Each field is a language, a flag
// or part of the title, as Jellyfin names them.
func ParseTrack(name, videoBase string) (t Track, ok bool) {
	ext := path.Ext(name)
	k, isTrack := trackKinds[strings.ToLower(ext)]
	stem := strings.TrimSuffix(name, ext)
	if !isTrack || videoBase == "" {
		return Track{}, false
	}
	var rest string
	switch {
	case stem == videoBase:
	case strings.HasPrefix(stem, videoBase+"."):
		rest = stem[len(videoBase)+1:]
	default:
		return Track{}, false
	}
	t = Track{Kind: k.kind, Codec: k.codec}
	var title []string
	hi := false
	for _, f := range strings.Split(rest, ".") {
		f = strings.TrimSpace(f)
		lower := strings.ToLower(f)
		switch {
		case f == "":
		case lower == "default":
			t.Default = true
		case lower == "forced" || lower == "foreign":
			t.Forced = true
		case lower == "sdh" || lower == "cc":
			t.HearingImpaired = true
		case lower == "hi":
			// Hindi by itself, hearing impaired beside another language.
			hi = true
		case t.Lang == "" && language(lower) != "":
			t.Lang = language(lower)
		default:
			title = append(title, f)
		}
	}
	if hi {
		if t.Lang == "" {
			t.Lang = "hin"
		} else {
			t.HearingImpaired = true
		}
	}
	t.Title = strings.Join(title, " ")
	return t, true
}

// language is the ISO 639-2 bibliographic code of a two or three letter
// code, or "".
func language(code string) string {
	switch len(code) {
	case 2:
		return twoLetter[code]
	case 3:
		if b, ok := terminology[code]; ok {
			return b
		}
		if threeLetter[code] {
			return code
		}
	}
	return ""
}

// TrackOwner picks, among the base names of the videos in a folder, the
// one whose track a file is: the longest that its name begins with,
// followed by a dot or by its extension. It returns "" for none.
func TrackOwner(name string, videoBases []string) string {
	best := ""
	for _, b := range videoBases {
		if len(b) > len(best) {
			if _, ok := ParseTrack(name, b); ok {
				best = b
			}
		}
	}
	return best
}

func setOf(words string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(words) {
		out[w] = true
	}
	return out
}

// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import "testing"

func TestWhatCounts(t *testing.T) {
	for name, want := range map[string]bool{
		"Invented Film (2001).mkv": true, "a.MKV": true, "a.mp4": true, "a.m4v": true, "a.mov": true,
		"a.avi": true, "a.ts": true, "a.m2ts": true, "a.webm": true, "a.wmv": true, "a.mpg": true, "a.mpeg": true,
		"a.srt": false, "a.jpg": false, "a.nfo": false, "a": false,
	} {
		if got := isVideo(name); got != want {
			t.Errorf("isVideo(%q) = %v", name, got)
		}
	}
	for name, want := range map[string]bool{
		".hidden.mkv": true, "a.mkv.part": true, "a.mkv.partial": true, "a.mkv.!qB": true, "a.mkv.tmp": true,
		"sample.mkv": true, "Sample.mp4": true, "a.mkv": false, "a sample of light.mkv": false,
	} {
		if got := ignoredFile(name); got != want {
			t.Errorf("ignoredFile(%q) = %v", name, got)
		}
	}
	for name, want := range map[string]bool{".git": true, "@eaDir": true, "lost+found": true, "Season 01": false} {
		if got := ignoredDir(name); got != want {
			t.Errorf("ignoredDir(%q) = %v", name, got)
		}
	}
}

func TestExtras(t *testing.T) {
	for rel, want := range map[string]bool{
		"Invented Film (2001)/Invented Film (2001).mkv":                   false,
		"Invented Film (2001)/extras/Making of.mkv":                       true,
		"Invented Film (2001)/Behind The Scenes/Day one.mkv":              true,
		"Invented Film (2001)/trailers/Teaser.mp4":                        true,
		"Invented Film (2001)/Invented Film (2001)-trailer.mkv":           true,
		"Invented Film (2001)/Invented Film (2001) trailer.mkv":           true,
		"Invented Film (2001)/Invented Film (2001).sample.mkv":            true,
		"Invented Film (2001)/Invented Film (2001)-deletedscene.mkv":      true,
		"Invented Film (2001)/trailer.mkv":                                true,
		"Other (2010)/Other (2010).mkv":                                   false, // a film called Other
		"Invented Show (2020)/Season 01/Invented Show - S01E01.mkv":       false,
		"Invented Show (2020)/Specials/Invented Show - S00E01.mkv":        false,
		"Invented Show (2020)/featurettes/Cast talk.mkv":                  true,
		"Invented Show (2020)/Season 01/interviews/Invented Show - x.mkv": true,
		"Lone Film (1999).mkv":                                            false,
		"Space Trailer Park (1999)/Space Trailer Park (1999).mkv":         false,
		"Invented Film (2001)/Invented Film (2001) - Directors Cut.mkv":   false,
		"Invented Film (2001)/Invented Film (2001)_sample.mkv":            true,
		"Invented Film (2001)/Invented Film (2001)-behindthescenes.mkv":   true,
		"Invented Film (2001)/Invented Film (2001)-featurette.mkv":        true,
		"Invented Film (2001)/Invented Film (2001) - part1.mkv":           false,
		"Invented Film (2001)/theme-music/Theme.mkv":                      true,
		"Invented Film (2001)/backdrops/Loop.mkv":                         true,
		"Invented Film (2001)/shorts/Short.mkv":                           true,
		"Invented Film (2001)/samples/A.mkv":                              true,
		"Invented Film (2001)/scenes/A.mkv":                               true,
		"Invented Film (2001)/clips/A.mkv":                                true,
		"Invented Film (2001)/other/A.mkv":                                true,
		"Invented Film (2001)/interviews/A.mkv":                           true,
		"Invented Film (2001)/deleted scenes/A.mkv":                       true,
		"Invented Film (2001)/featurettes/A.mkv":                          true,
		"Invented Film (2001)/Invented Film (2001)-interview.mkv":         true,
		"Invented Film (2001)/Invented Film (2001)-clip.mkv":              true,
		"Invented Film (2001)/Invented Film (2001)-scene.mkv":             true,
		"Invented Film (2001)/Invented Film (2001)-short.mkv":             true,
		"Invented Film (2001)/Invented Film (2001)-other.mkv":             true,
		"Invented Film (2001)/Invented Film (2001)-extra.mkv":             true,
		"Invented Film (2001)/Invented Film (2001)-deleted.mkv":           true,
		"Invented Film (2001)/Invented Film (2001)-sample.mkv":            true,
	} {
		if got := isExtra(rel); got != want {
			t.Errorf("isExtra(%q) = %v", rel, got)
		}
	}
}

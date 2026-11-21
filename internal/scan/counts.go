// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"path"
	"strings"
)

// What a scan counts, and what it leaves out
// (docs/06-library-and-metadata.md, sections 2.3 and 4.6).

var videoExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".mov": true, ".avi": true, ".ts": true,
	".m2ts": true, ".webm": true, ".wmv": true, ".mpg": true, ".mpeg": true,
}

// isVideo reports whether a file name has one of the video extensions.
func isVideo(name string) bool {
	return videoExtensions[strings.ToLower(path.Ext(name))]
}

// ignoredDir reports whether a folder is left out, with all it holds.
func ignoredDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "@eaDir" || name == "lost+found"
}

// ignoredFile reports whether a file is left out: hidden files, files
// still being downloaded, and files named sample.
func ignoredFile(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(name, ".") {
		return true
	}
	for _, s := range []string{".part", ".partial", ".!qb", ".tmp"} {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	return strings.TrimSuffix(lower, path.Ext(lower)) == "sample"
}

// extrasFolders and extrasSuffixes are Jellyfin's, as its documentation
// for films gives them.
var extrasFolders = map[string]bool{
	"behind the scenes": true, "deleted scenes": true, "interviews": true, "scenes": true,
	"samples": true, "shorts": true, "featurettes": true, "clips": true, "other": true,
	"extras": true, "trailers": true, "theme-music": true, "backdrops": true,
}

var extrasSuffixes = []string{
	"-trailer", ".trailer", "_trailer", " trailer",
	"-sample", ".sample", "_sample", " sample",
	"-scene", "-clip", "-interview", "-behindthescenes", "-deleted", "-deletedscene",
	"-featurette", "-short", "-other", "-extra",
}

// isExtra reports whether the file at rel, a path relative to its root
// with / between folders, is an extra: inside an extras folder below the
// root's own folders, with an extras suffix, or named trailer.
func isExtra(rel string) bool {
	dirs := strings.Split(path.Dir(rel), "/")
	// dirs[0] is a film's or a series' own folder (or "." for a file at the
	// top), so a film called "Other" is not taken for an extras folder.
	for _, d := range dirs[1:] {
		if extrasFolders[strings.ToLower(d)] {
			return true
		}
	}
	base := strings.ToLower(path.Base(rel))
	base = strings.TrimSuffix(base, path.Ext(base))
	if base == "trailer" {
		return true
	}
	for _, s := range extrasSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	return false
}
